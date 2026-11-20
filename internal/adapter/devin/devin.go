// 本文件实现 RequestMessages 与 Devin Connect RPC 的双向转换。
//
// Package devin 负责一次 Devin GetChatMessage 调用及其响应事件转换，不执行工具或 agent loop。
package devin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	devinproto "local/devinproto"
	"local/devinproto/devinprotoconnect"

	"connectrpc.com/connect"
	"github.com/leookun/devin-2api/internal/adapter"
	"github.com/leookun/devin-2api/internal/debuglog"
	"github.com/leookun/devin-2api/internal/httpproxy"
	"github.com/leookun/devin-2api/internal/llm"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	clientName    = "chisel"
	clientVersion = "3000.2.17"
)

// Config 保存 Devin adapter 的固定上游配置。
type Config struct {
	// BaseURL 是 Devin Connect 服务的基础地址。
	BaseURL string
	// Token 是 Devin session token；不会写入日志。
	Token string
	// Model 是 Devin chat model UID。
	Model string
	// Proxy 是可选的 HTTP/HTTPS/SOCKS5 代理地址；为空时直连或走系统环境变量。
	// 也是分流未命中时的兜底出口。
	Proxy string
	// ProxyPools 是动态命名的代理池：池内多个地址按请求轮询分摊；
	// 配合 Routes 把不同模型/分组引到不同出口。
	ProxyPools map[string][]string
	// Routes 是模型分流规则：按 uid/标签/系列/分组通配符顺序匹配，命中即止。
	Routes []httpproxy.Route
	// ForceHTTP1 为 true 时强制 HTTP/1.1，每请求独立连接，避免 HTTP/2 单连接多 stream 并发瓶颈。
	ForceHTTP1 bool
	// MaxNewlines 是单次输出的换行上限；<=0 取 DefaultMaxNewlines。
	MaxNewlines int
}

// DefaultMaxNewlines 是未配置时的输出换行上限。
const DefaultMaxNewlines = 400

// DefaultModelAlias 是请求里代表「上游默认模型」的保留模型名，
// 解析为 GetCascadeModelConfigs.default_override_model_config，取不到则用 devin.model。
const DefaultModelAlias = "default"

// modelMeta 是模型目录中用于分流匹配的附加标识。
type modelMeta struct {
	label          string
	family         string
	groups         []string
	supportsImages bool
	hasImageInfo   bool
}

// Adapter 调用 Devin 的 ApiServerService/GetChatMessage。
type Adapter struct {
	config         Config
	client         devinprotoconnect.ApiServerServiceClient
	apiClient      devinprotoconnect.ApiServerServiceClient
	base           http.RoundTripper
	router         *httpproxy.Router
	modelsMu       sync.RWMutex
	models         []adapter.ModelInfo
	modelMeta      map[string]modelMeta
	defaultModel   string
	modelsExpiry   time.Time
	modelsCacheTTL time.Duration
}

var _ adapter.Adapter = (*Adapter)(nil)

// New 创建 Devin adapter。
func New(config Config) (*Adapter, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, errors.New("devin base URL is required")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("devin token is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("devin model is required")
	}
	base, err := httpproxy.NewTransport(config.Proxy, config.ForceHTTP1)
	if err != nil {
		return nil, fmt.Errorf("create proxy transport: %w", err)
	}
	transport := &authTransport{base: base, token: config.Token}

	// SSE 流需要长期保持连接，不能设置 Client.Timeout；
	// 但 Transport 层的 ResponseHeaderTimeout 已限制首包等待时间。
	streamClient := devinprotoconnect.NewApiServerServiceClient(&http.Client{Transport: transport}, config.BaseURL)

	// 普通 API 调用（如模型目录）设置整体超时，避免慢请求长时间占用 goroutine；
	// 需要大于 ResponseHeaderTimeout，给 body 读取留余量。
	apiHTTPClient := &http.Client{Transport: transport, Timeout: 610 * time.Second}
	apiClient := devinprotoconnect.NewApiServerServiceClient(apiHTTPClient, config.BaseURL)

	a := &Adapter{
		config:         config,
		client:         streamClient,
		apiClient:      apiClient,
		base:           base,
		modelsCacheTTL: 5 * time.Minute,
	}
	// 配置了代理池或分流规则时构建路由器；非法配置在启动时直接报错。
	if len(config.ProxyPools) > 0 || len(config.Routes) > 0 {
		router, err := httpproxy.NewRouter(config.ProxyPools, config.Routes, base, config.ForceHTTP1)
		if err != nil {
			return nil, fmt.Errorf("proxy routing: %w", err)
		}
		a.router = router
	}
	return a, nil
}

// Stream 将一份中间请求转换为 Devin RPC，并返回一份中间响应事件流。
func (adapter *Adapter) Stream(ctx context.Context, request llm.RequestMessages) (llm.ResponseStream, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate Devin request: %w", err)
	}
	model := adapter.resolveModel(ctx, request.Model)
	if err := adapter.validateImagesForModel(request, model); err != nil {
		return nil, err
	}
	cfg := adapter.config
	cfg.Model = model
	protoRequest, err := buildRequest(request, cfg)
	if err != nil {
		return nil, err
	}
	recorder := debuglog.FromContext(ctx)
	recordProtoJSON(recorder, "03-devin-request.json", protoRequest)
	stream, err := adapter.streamClientFor(ctx, model).GetChatMessage(ctx, connect.NewRequest(protoRequest))
	if err != nil {
		recorder.WriteError("devin_connect", err)
		// 透传上游 Connect 错误原文，不包一层模糊前缀。
		return nil, connectError(err)
	}
	return &responseStream{upstream: stream, decoder: newResponseDecoder(model), recorder: recorder}, nil
}

// resolveModel 把空模型与保留别名 default 解析为实际 uid：
// 优先上游目录下发的默认覆盖模型，其次配置的 devin.model。
func (adapter *Adapter) resolveModel(ctx context.Context, requested string) string {
	model := strings.TrimSpace(requested)
	if model != "" && !strings.EqualFold(model, DefaultModelAlias) {
		return model
	}
	if model != "" {
		adapter.ensureMeta(ctx)
		adapter.modelsMu.RLock()
		override := adapter.defaultModel
		adapter.modelsMu.RUnlock()
		if override != "" {
			return override
		}
	}
	return adapter.config.Model
}

// validateImagesForModel 在本地尽早拒绝「无视觉能力模型 + 图片」组合，错误信息对客户端可读。
// 优先采信上游目录的 supports_images；目录未知时退到启发式名单。
func (adapter *Adapter) validateImagesForModel(request llm.RequestMessages, model string) error {
	if !requestHasImages(request) {
		return nil
	}
	supported := modelLikelySupportsImages(model)
	adapter.modelsMu.RLock()
	if meta, ok := adapter.modelMeta[model]; ok && meta.hasImageInfo {
		supported = meta.supportsImages
	}
	adapter.modelsMu.RUnlock()
	if !supported {
		return fmt.Errorf("model %q does not support image inputs (supports_images=false); use a vision-capable model or remove images", model)
	}
	return nil
}

func requestHasImages(request llm.RequestMessages) bool {
	for _, message := range request.Messages {
		var content []llm.Content
		switch m := message.(type) {
		case llm.UserMessage:
			content = m.Content
		case llm.ToolResultMessage:
			content = m.Content
		default:
			continue
		}
		for _, block := range content {
			if _, ok := block.(llm.ImageContent); ok {
				return true
			}
		}
	}
	return false
}

// modelLikelySupportsImages 用已知无视觉模型名单；不确定时放行让上游裁决。
func modelLikelySupportsImages(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	// 与 GetCascadeModelConfigs.supports_images=false 的常见 uid 对齐。
	noVisionPrefixes := []string{
		"glm-5-2", "glm-5", "glm-4.7", "glm-4-7", "glm-4",
		"deepseek", "kimi-k2", "qwen3-coder",
	}
	for _, p := range noVisionPrefixes {
		if m == p || strings.HasPrefix(m, p+"-") || strings.HasPrefix(m, p+"_") {
			return false
		}
	}
	if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3-mini") || strings.HasPrefix(m, "o4-mini") {
		return false
	}
	return true
}

// connectError 提取 Connect 错误的 code + message，原样返回给 HTTP 客户端。
func connectError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		msg := strings.TrimSpace(connectErr.Message())
		if msg == "" {
			msg = connectErr.Error()
		}
		return fmt.Errorf("%s: %s", connectErr.Code(), msg)
	}
	return err
}

// streamClientFor 按分流规则为本次请求选择 stream client；
// 未命中或命中 fallback 时复用启动时构建的默认 client，避免每请求重建 Transport。
func (a *Adapter) streamClientFor(ctx context.Context, model string) devinprotoconnect.ApiServerServiceClient {
	if a.router == nil {
		return a.client
	}
	transport := a.router.TransportFor(a.routeIDs(ctx, model)...)
	if transport == a.base {
		return a.client
	}
	return devinprotoconnect.NewApiServerServiceClient(&http.Client{Transport: &authTransport{base: transport, token: a.config.Token}}, a.config.BaseURL)
}

// routeIDs 返回模型用于分流匹配的全部标识：uid + 目录中的 标签/系列/分组。
func (a *Adapter) routeIDs(ctx context.Context, model string) []string {
	ids := []string{model}
	m, ok := a.ensureMeta(ctx)[model]
	if !ok {
		return ids
	}
	if m.label != "" {
		ids = append(ids, m.label)
	}
	if m.family != "" {
		ids = append(ids, m.family)
	}
	return append(ids, m.groups...)
}

// ensureMeta 返回 uid→元信息 快照；未缓存时随本次请求拉取（与 ListModels 共享 TTL 缓存）。
// 拉取失败返回空表，分流退化为仅按 uid 匹配。
func (a *Adapter) ensureMeta(ctx context.Context) map[string]modelMeta {
	a.modelsMu.RLock()
	if a.modelMeta != nil && time.Now().Before(a.modelsExpiry) {
		meta := a.modelMeta
		a.modelsMu.RUnlock()
		return meta
	}
	a.modelsMu.RUnlock()
	if _, _, err := a.catalog(ctx); err != nil {
		return map[string]modelMeta{}
	}
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	if a.modelMeta == nil {
		return map[string]modelMeta{}
	}
	return a.modelMeta
}

// ListModels 通过 GetCascadeModelConfigs 拉取可用模型目录，结果带 TTL 缓存。
func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	models, _, err := a.catalog(ctx)
	return models, err
}

// catalog 拉取模型目录快照：OpenAI 模型列表 + 分流匹配所需的元信息。
// models 与 meta 同源同 TTL，ListModels 与 routeIDs 共用一份缓存。
func (a *Adapter) catalog(ctx context.Context) ([]adapter.ModelInfo, map[string]modelMeta, error) {
	a.modelsMu.RLock()
	if a.models != nil && time.Now().Before(a.modelsExpiry) {
		models, meta := a.models, a.modelMeta
		a.modelsMu.RUnlock()
		return models, meta, nil
	}
	a.modelsMu.RUnlock()

	resp, err := a.apiClient.GetCascadeModelConfigs(ctx, connect.NewRequest(&devinproto.GetCascadeModelConfigsRequest{
		Metadata: a.metadata(),
	}))
	if err != nil {
		return nil, nil, fmt.Errorf("Devin GetCascadeModelConfigs: %w", err)
	}

	// 上游下发的默认覆盖模型（IDE 未选模型时的默认项），供保留别名 default 使用。
	defaultOverride := ""
	if override := resp.Msg.GetDefaultOverrideModelConfig(); override != nil {
		defaultOverride = override.GetModelUid()
		if defaultOverride == "" && override.GetModelOrAliasDeprecated() != nil {
			defaultOverride = override.GetModelOrAliasDeprecated().GetModelUid()
		}
	}
	// 分组索引：picker 分组按 model label 归组，先建 label→组名 映射。
	labelGroups := map[string][]string{}
	for _, s := range resp.Msg.GetClientModelSorts() {
		for _, g := range s.GetGroups() {
			for _, l := range g.GetModelLabels() {
				labelGroups[l] = append(labelGroups[l], g.GetGroupName())
			}
		}
	}

	now := time.Now().Unix()
	models := make([]adapter.ModelInfo, 0, len(resp.Msg.GetClientModelConfigs()))
	meta := make(map[string]modelMeta, len(resp.Msg.GetClientModelConfigs()))
	seen := make(map[string]struct{}, len(resp.Msg.GetClientModelConfigs()))
	add := func(c *devinproto.ExaCodeiumCommonPb_ClientModelConfig, command bool) {
		uid := c.GetModelUid()
		if uid == "" && c.GetModelOrAlias() != nil {
			uid = c.GetModelOrAlias().GetModelUid()
		}
		if uid == "" {
			return
		}
		m := modelMeta{label: c.GetLabel(), groups: labelGroups[c.GetLabel()], supportsImages: c.GetSupportsImages(), hasImageInfo: true}
		if fm := c.GetModelFamilyMetadata(); fm != nil {
			m.family = fm.GetModelFamilyLabel()
		}
		meta[uid] = m
		if c.GetDisabled() {
			return
		}
		if _, ok := seen[uid]; ok {
			return
		}
		seen[uid] = struct{}{}
		ownedBy := "devin"
		if p := c.GetProvider().String(); p != "" {
			if i := strings.LastIndex(p, "_"); i >= 0 && i+1 < len(p) {
				ownedBy = strings.ToLower(p[i+1:])
			} else if p == "13" {
				// 枚举 13 比抓取的 proto 更新；上游分组名为 Meta（Muse Wonder 系列）。
				ownedBy = "meta"
			}
		}
		info := adapter.ModelInfo{
			ID: uid, Created: now, OwnedBy: ownedBy, SupportsImages: c.GetSupportsImages(),
			Label: c.GetLabel(), Description: c.GetDescription(), Family: m.family,
			Provider:         enumShort(c.GetProvider().String(), "MODEL_PROVIDER_"),
			APIProvider:      enumShort(c.GetApiProvider().String(), "API_PROVIDER_"),
			CostTier:         strings.ToLower(enumShort(c.GetModelCostTier().String(), "MODEL_COST_TIER_")),
			PricingType:      enumShort(c.GetPricingType().String(), "MODEL_PRICING_TYPE_"),
			CreditMultiplier: float64(c.GetCreditMultiplier()), MaxTokens: int64(c.GetMaxTokens()),
			BetaWarning: c.GetBetaWarningMessage(),
			IsBeta:      c.GetIsBeta(), IsNew: c.GetIsNew(), IsRecommended: c.GetIsRecommended(),
			IsPremium: c.GetIsPremium(), IsCapacityLimited: c.GetIsCapacityLimited(),
			IsCommand: command,
		}
		if info.Provider == "13" {
			info.Provider = "META"
		}
		if ps := c.GetPromoStatus(); ps != nil && ps.GetIsActive() {
			info.Promo = &adapter.ModelPromo{Label: ps.GetLabel()}
			if ed := ps.GetEndDate(); ed != nil {
				info.Promo.EndDate = ed.GetSeconds()
			}
		}
		models = append(models, info)
	}
	for _, c := range resp.Msg.GetClientModelConfigs() {
		add(c, false)
	}
	// 合并命令模型目录（commit 信息等命令用模型，如 MODEL_PRIVATE_2 "Claude 4.5 Sonnet"）：
	// 不在 chat picker 中但可直接调用；失败不影响主目录。
	if cmdResp, err := a.apiClient.GetCommandModelConfigs(ctx, connect.NewRequest(&devinproto.GetCommandModelConfigsRequest{
		Metadata: a.metadata(),
	})); err == nil {
		for _, c := range cmdResp.Msg.GetClientModelConfigs() {
			add(c, true)
		}
	}
	// 用户显式配置的 model（如 gpt5.6）即使不在 Devin 返回的列表中，也应可被发现和调用。
	if configured := strings.TrimSpace(a.config.Model); configured != "" {
		if _, ok := seen[configured]; !ok {
			models = append(models, adapter.ModelInfo{
				ID: configured, Created: now, OwnedBy: "devin",
				// 配置模型无法从 Devin 获取图片能力，默认按支持图片处理更友好。
				SupportsImages: true,
			})
		}
	}

	a.modelsMu.Lock()
	defer a.modelsMu.Unlock()
	// 请求期间可能有其他请求已写入缓存，避免覆盖更热的数据。
	if a.models != nil && time.Now().Before(a.modelsExpiry) {
		return a.models, a.modelMeta, nil
	}
	a.models = models
	a.modelMeta = meta
	a.defaultModel = defaultOverride
	a.modelsExpiry = time.Now().Add(a.modelsCacheTTL)
	return models, meta, nil
}

// TranscribeAudio 调上游 GetTranscription 把音频字节转写为文本（whisper 系）。
func (a *Adapter) TranscribeAudio(ctx context.Context, audio []byte) (string, error) {
	resp, err := a.apiClient.GetTranscription(ctx, connect.NewRequest(&devinproto.GetTranscriptionRequest{
		Metadata:  a.metadata(),
		AudioData: audio,
	}))
	if err != nil {
		return "", connectError(err)
	}
	return resp.Msg.GetTranscribedText(), nil
}

// metadata 构建上游 RPC 通用元信息。
func (a *Adapter) metadata() *devinproto.ExaCodeiumCommonPb_Metadata {
	return &devinproto.ExaCodeiumCommonPb_Metadata{
		ApiKey:           proto.String(a.config.Token),
		ExtensionName:    proto.String(clientName),
		ExtensionVersion: proto.String(clientVersion),
		IdeName:          proto.String(clientName),
		IdeVersion:       proto.String(clientVersion),
		Locale:           proto.String("en"),
		Os:               proto.String("mac"),
	}
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (transport *authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Basic "+transport.token+"-"+transport.token)
	return transport.base.RoundTrip(clone)
}

func buildRequest(request llm.RequestMessages, config Config) (*devinproto.GetChatMessageRequest, error) {
	fingerprint, err := randomHex(366)
	if err != nil {
		return nil, fmt.Errorf("generate Devin device fingerprint: %w", err)
	}
	trajectoryID := randomUUID()
	cascadeID := randomUUID()
	executionID := randomUUID()
	metadata := &devinproto.ExaCodeiumCommonPb_Metadata{
		ApiKey:           proto.String(config.Token),
		ExtensionName:    proto.String(clientName),
		ExtensionVersion: proto.String(clientVersion),
		IdeName:          proto.String(clientName),
		IdeVersion:       proto.String(clientVersion),
		Locale:           proto.String("en"),
		Os:               proto.String("mac"),
		F:                proto.String(fingerprint),
	}
	configuration := &devinproto.ExaCodeiumCommonPb_CompletionConfiguration{
		NumCompletions: proto.Uint64(1),
		MaxTokens:      proto.Uint64(128000),
		MaxNewlines:    proto.Uint64(uint64(maxNewlines(config.MaxNewlines))),
		Temperature:    proto.Float64(1),
		TopK:           proto.Uint64(40),
		TopP:           proto.Float64(0.95),
	}
	// 客户端显式提供的生成参数优先透传；缺省沿用 IDE 默认采样配置。
	if request.MaxTokens != nil && *request.MaxTokens > 0 {
		configuration.MaxTokens = proto.Uint64(uint64(*request.MaxTokens))
	}
	if request.Temperature != nil {
		configuration.Temperature = proto.Float64(*request.Temperature)
	}
	if request.TopP != nil {
		configuration.TopP = proto.Float64(*request.TopP)
	}
	if request.TopK != nil && *request.TopK > 0 {
		configuration.TopK = proto.Uint64(uint64(*request.TopK))
	}
	if len(request.StopPatterns) > 0 {
		configuration.StopPatterns = request.StopPatterns
	}
	if request.Seed != nil {
		configuration.Seed = proto.Uint64(uint64(*request.Seed))
	}
	if tier := strings.TrimSpace(request.ServiceTier); tier != "" && tier != "auto" {
		configuration.ServiceTier = proto.String(tier)
	}
	// 末条是助手消息 = Anthropic 预填充（prefill）：告诉上游从该半截回复续写。
	if count := len(request.Messages); count > 0 {
		if _, ok := request.Messages[count-1].(llm.AssistantMessage); ok {
			configuration.LastMessageIsPartial = proto.Bool(true)
		}
	}
	result := &devinproto.GetChatMessageRequest{
		Metadata:      metadata,
		Prompt:        proto.String(sanitizeSystemPrompt(withToolDescriptions(request.SystemPrompt, request.Tools))),
		ChatModelUid:  proto.String(config.Model),
		RequestType:   devinproto.ChatMessageRequestType_CHAT_MESSAGE_REQUEST_TYPE_CASCADE.Enum(),
		Configuration: configuration,
		TrajectoryReference: &devinproto.ExaCortexPb_CortexTrajectoryReference{
			TrajectoryId:   proto.String(trajectoryID),
			TrajectoryType: devinproto.ExaCortexPb_CortexTrajectoryType_ExaCortexPb_CortexTrajectoryType_CORTEX_TRAJECTORY_TYPE_CASCADE.Enum(),
			StepType:       devinproto.ExaCortexPb_CortexStepType_ExaCortexPb_CortexStepType_CORTEX_STEP_TYPE_USER_INPUT.Enum(),
		},
		CascadeId:   proto.String(cascadeID),
		PlannerMode: devinproto.ExaCodeiumCommonPb_ConversationalPlannerMode_ExaCodeiumCommonPb_ConversationalPlannerMode_CONVERSATIONAL_PLANNER_MODE_DEFAULT.Enum(),
		ExecutionId: proto.String(executionID),
	}
	// Devin/Cascade 只可靠接受「当前轮」图片；历史图进 Images 会 invalid_argument。
	// 当前轮 = 最后一条 AssistantMessage 之后的所有 user/tool 消息。
	// Anthropic 客户端常把 image 和 tool_result 放在同一条 user 消息里，
	// 解码后拆成 UserMessage + ToolResultMessage 两条；仅挂最后一条会丢失图片。
	lastAssistantIndex := -1
	for index, message := range request.Messages {
		if _, ok := message.(llm.AssistantMessage); ok {
			lastAssistantIndex = index
		}
	}
	for index, message := range request.Messages {
		converted, err := convertMessage(message, index > lastAssistantIndex)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		result.ChatMessagePrompts = append(result.ChatMessagePrompts, converted...)
	}
	for _, tool := range request.Tools {
		converted, err := convertToolDefinition(tool)
		if err != nil {
			return nil, err
		}
		result.Tools = append(result.Tools, converted)
	}
	if request.SystemCacheControl {
		result.SystemPromptCacheOptions = ephemeralCache()
	}
	if request.DisableParallelToolCalls {
		result.DisableParallelToolCalls = proto.Bool(true)
	}
	if choice := convertToolChoice(request.ToolChoice); choice != nil {
		result.ToolChoice = choice
	}
	return result, nil
}

func maxNewlines(configured int) int {
	if configured > 0 {
		return configured
	}
	return DefaultMaxNewlines
}

// ephemeralCache 构造与 Anthropic cache_control:{type:ephemeral} 对应的上游缓存断点。
func ephemeralCache() *devinproto.ExaChatPb_PromptCacheOptions {
	return &devinproto.ExaChatPb_PromptCacheOptions{
		Type: devinproto.ExaChatPb_CacheControlType_ExaChatPb_CacheControlType_CACHE_CONTROL_TYPE_EPHEMERAL.Enum(),
	}
}

// enumShort 从 proto 枚举全名提取短名：
// "ExaCodeiumCommonPb_ModelProvider_MODEL_PROVIDER_ANTHROPIC" → "ANTHROPIC"；
// 未匹配前缀时退化为最后一个下划线分段；"UNSPECIFIED" 与空串按空处理。
func enumShort(full, prefix string) string {
	if full == "" {
		return ""
	}
	if idx := strings.Index(full, prefix); idx >= 0 {
		full = full[idx+len(prefix):]
	} else if i := strings.LastIndex(full, "_"); i >= 0 && i+1 < len(full) {
		full = full[i+1:]
	}
	if full == "UNSPECIFIED" {
		return ""
	}
	return full
}

// convertToolChoice 把归一化工具约束映射为上游 ChatToolChoice：
// auto/none/required 走 option_name 枚举语义，named 走 tool_name。
func convertToolChoice(choice *llm.ToolChoice) *devinproto.ExaChatPb_ChatToolChoice {
	if choice == nil {
		return nil
	}
	if choice.Mode == llm.ToolChoiceNamed {
		if choice.ToolName == "" {
			return nil
		}
		return &devinproto.ExaChatPb_ChatToolChoice{
			Choice: &devinproto.ExaChatPb_ChatToolChoice_ToolName{ToolName: choice.ToolName},
		}
	}
	mode := choice.Mode
	if mode == "" {
		mode = llm.ToolChoiceAuto
	}
	return &devinproto.ExaChatPb_ChatToolChoice{
		Choice: &devinproto.ExaChatPb_ChatToolChoice_OptionName{OptionName: string(mode)},
	}
}

// convertMessage 将中间消息转为 Devin ChatMessagePrompt。
// attachImages 为 true 时才把 ImageContent 写入 Images（仅最新用户轮）；历史图改成文本占位。
func convertMessage(message llm.Message, attachImages bool) ([]*devinproto.ExaChatPb_ChatMessagePrompt, error) {
	switch message := message.(type) {
	case llm.UserMessage:
		prompt := promptForContent(devinproto.ExaCodeiumCommonPb_ChatMessageSource_ExaCodeiumCommonPb_ChatMessageSource_CHAT_MESSAGE_SOURCE_USER, message.Content, attachImages)
		if message.CacheControl {
			prompt.PromptCacheOptions = ephemeralCache()
		}
		return []*devinproto.ExaChatPb_ChatMessagePrompt{prompt}, nil
	case llm.AssistantMessage:
		// 助手历史不回传图片；若有意外 ImageContent 同样占位。
		prompt := promptForContent(devinproto.ExaCodeiumCommonPb_ChatMessageSource_ExaCodeiumCommonPb_ChatMessageSource_CHAT_MESSAGE_SOURCE_SYSTEM, message.Content, false)
		for _, block := range message.Content {
			if call, ok := block.(llm.ToolCall); ok {
				prompt.ToolCalls = append(prompt.ToolCalls, &devinproto.ExaCodeiumCommonPb_ChatToolCall{
					Id:            proto.String(call.ID),
					Name:          proto.String(call.Name),
					ArgumentsJson: proto.String(string(call.Arguments)),
				})
				// Gemini 要求带工具调用的历史轮次回传思考签名，否则多轮工具调用会被拒。
				if len(prompt.GeminiThoughtSignature) == 0 && call.ThoughtSignature != "" {
					if signature, err := base64.StdEncoding.DecodeString(call.ThoughtSignature); err == nil {
						prompt.GeminiThoughtSignature = signature
					}
				}
			}
		}
		if message.CacheControl {
			prompt.PromptCacheOptions = ephemeralCache()
		}
		return []*devinproto.ExaChatPb_ChatMessagePrompt{prompt}, nil
	case llm.ToolResultMessage:
		prompt := promptForContent(devinproto.ExaCodeiumCommonPb_ChatMessageSource_ExaCodeiumCommonPb_ChatMessageSource_CHAT_MESSAGE_SOURCE_TOOL, message.Content, attachImages)
		prompt.ToolCallId = proto.String(message.ToolCallID)
		prompt.ToolResultIsError = proto.Bool(message.IsError)
		if message.CacheControl {
			prompt.PromptCacheOptions = ephemeralCache()
		}
		return []*devinproto.ExaChatPb_ChatMessagePrompt{prompt}, nil
	default:
		return nil, fmt.Errorf("unsupported message type %T", message)
	}
}

func promptForContent(source devinproto.ExaCodeiumCommonPb_ChatMessageSource, content []llm.Content, attachImages bool) *devinproto.ExaChatPb_ChatMessagePrompt {
	prompt := &devinproto.ExaChatPb_ChatMessagePrompt{
		MessageId: proto.String(randomID()),
		Source:    source.Enum(),
	}
	var text strings.Builder
	for _, block := range content {
		switch block := block.(type) {
		case llm.TextContent:
			text.WriteString(block.Text)
		case llm.ThinkingContent:
			prompt.Thinking = proto.String(block.Thinking)
			if block.ThinkingSignature != "" {
				prompt.Signature = proto.String(block.ThinkingSignature)
			}
			prompt.ThinkingRedacted = proto.Bool(block.Redacted)
		case llm.ImageContent:
			if !attachImages {
				// 与 WindsurfAPI 一致：历史图不进 Images，避免上游 invalid_argument。
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString("[Image omitted from history]")
				continue
			}
			// Devin/Windsurf ImageData：纯 base64（无 data: 前缀）+ mime_type。
			data := block.Data
			if strings.HasPrefix(data, "data:") {
				if _, encoded, ok := strings.Cut(data, ","); ok {
					data = encoded
				}
			}
			mimeType := block.MIMEType
			if mimeType == "" {
				mimeType = "image/png"
			}
			prompt.Images = append(prompt.Images, &devinproto.ExaCodeiumCommonPb_ImageData{
				Base64Data: proto.String(data),
				MimeType:   proto.String(mimeType),
			})
		}
	}
	prompt.Prompt = proto.String(text.String())
	return prompt
}

func randomID() string {
	return randomUUID()
}

func randomUUID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func randomHex(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// responseStream 从 Connect 上游按需读取帧并依次返回 decoder 生成的事件。
type responseStream struct {
	// upstream 是 Devin Connect 返回的服务端流。
	upstream devinResponseReceiver
	// decoder 将一个 Devin protobuf 帧转换为零个或多个中间响应事件。
	decoder *responseDecoder
	// recorder 记录 Devin 原始响应帧；nil 表示禁用调试日志。
	recorder *debuglog.Recorder
	// started 表示是否已经请求 decoder 产生 start 事件。
	started bool
	// finished 表示 decoder 已经生成最终事件，不再读取上游。
	finished bool
	// queue 保存已经转换、等待调用方读取的中间响应事件。
	queue []llm.ResponseEvent
}

// devinResponseReceiver 描述 responseStream 消费 Devin 服务端流所需的最小能力。
type devinResponseReceiver interface {
	// Receive 前进到下一帧，并报告是否成功取得消息。
	Receive() bool
	// Msg 返回最近一次成功取得的响应帧。
	Msg() *devinproto.GetChatMessageResponse
	// Err 返回流结束时的错误；正常 EOF 返回 nil。
	Err() error
}

func (stream *responseStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	for len(stream.queue) == 0 && !stream.finished {
		if err := ctx.Err(); err != nil {
			return llm.ResponseEvent{}, err
		}
		if !stream.started {
			stream.started = true
			stream.queue = append(stream.queue, stream.decoder.start()...)
			break
		}
		if !stream.upstream.Receive() {
			stream.queue = append(stream.queue, stream.decoder.finish(stream.upstream.Err())...)
			stream.finished = true
			break
		}
		response := stream.upstream.Msg()
		recordProtoJSON(stream.recorder, "04-devin-response.jsonl", response)
		stream.queue = append(stream.queue, stream.decoder.decode(response)...)
		stream.finished = stream.decoder.finished
	}
	if len(stream.queue) > 0 {
		event := stream.queue[0]
		stream.queue = stream.queue[1:]
		return event, nil
	}
	return llm.ResponseEvent{}, io.EOF
}

func recordProtoJSON(recorder *debuglog.Recorder, name string, message proto.Message) {
	if recorder == nil || message == nil {
		return
	}
	data, err := protojson.Marshal(message)
	if err != nil {
		recorder.WriteError("devin_proto_encode", err)
		return
	}
	if strings.HasSuffix(name, ".jsonl") {
		recorder.AppendValueJSONL(name, json.RawMessage(data))
		return
	}
	recorder.WriteJSON(name, json.RawMessage(data))
}
