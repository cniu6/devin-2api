// 本文件定义 HTTP 应用、chi 路由和供应商适配器的串联逻辑。
//
// Package app 负责组装 HTTP 路由并连接 API 编解码与供应商适配器。
package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/leookun/devin-2api/internal/adapter"
	"github.com/leookun/devin-2api/internal/api/common"
	respproto "github.com/leookun/devin-2api/internal/api/openai/responses"
	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/debuglog"
	"github.com/leookun/devin-2api/internal/llm"
	"github.com/leookun/devin-2api/internal/responsestore"
	"github.com/leookun/devin-2api/internal/stats"
)

// DashboardRegistrar 描述面板路由注册所需的最小能力。
type DashboardRegistrar interface {
	Register(mux interface {
		Get(pattern string, handlerFn http.HandlerFunc)
		Post(pattern string, handlerFn http.HandlerFunc)
	})
}

const (
	// readHeaderTimeout 是防止慢速请求头连接长期占用资源的内部策略。
	readHeaderTimeout = 60 * time.Second
	// readTimeout 限制请求体读取总时长，防止慢速客户端长期占用连接。
	readTimeout = 120 * time.Second
	// writeTimeout 限制响应写入总时长；SSE 流可能较长，这里给足余量。
	writeTimeout = 30 * time.Minute
	// idleTimeout 是 keep-alive 连接两次请求之间的内部空闲策略。
	idleTimeout = 360 * time.Second
	// defaultMaxConcurrency 是默认同时处理的 /v1/* 请求数上限。
	defaultMaxConcurrency = 1024
)

// App 保存 HTTP 应用依赖和服务配置。
type App struct {
	// adapter 是供应商无关请求与上游协议之间的适配器。
	adapter adapter.Adapter
	// serverConfig 是 HTTP 服务运行配置。
	serverConfig config.ServerConfig
	// debugManager 为每次兼容 API 请求创建独立的写盘日志。
	debugManager *debuglog.Manager
	// dashboard 是可选的管理面板处理器；nil 表示不启用面板。
	dashboard DashboardRegistrar
	// apiKey 是可选的 OpenAI 兼容接口访问密钥；为空则不校验。
	apiKey string
	// concurrency 限制同时处理的 /v1/* 请求数。
	concurrency chan struct{}
	// stats 收集请求级 token/耗时统计，供 /panel/api/stats 展示。
	stats *stats.Collector
	// responses 保存 Responses API 的服务端会话，供 previous_response_id 补齐历史。
	responses *responsestore.Store
}

// New 创建一个使用指定供应商适配器的 HTTP 应用。
func New(providerAdapter adapter.Adapter, serverConfig config.ServerConfig, debugManager *debuglog.Manager) *App {
	limit := serverConfig.MaxConcurrency
	if limit <= 0 {
		limit = defaultMaxConcurrency
	}
	return &App{
		adapter:      providerAdapter,
		serverConfig: serverConfig,
		debugManager: debugManager,
		concurrency:  make(chan struct{}, limit),
		responses:    responsestore.New(0, 0),
	}
}

// SetAPIKey 设置 OpenAI 兼容接口的访问密钥；应在 Router/HTTPServer 之前调用。
func (application *App) SetAPIKey(apiKey string) {
	application.apiKey = apiKey
}

// SetDashboard 注入管理面板处理器。
func (application *App) SetDashboard(d DashboardRegistrar) {
	application.dashboard = d
}

// SetStats 注入请求统计采集器；nil 表示关闭统计。
func (application *App) SetStats(collector *stats.Collector) {
	application.stats = collector
}

// Router 返回应用的 chi HTTP 路由。
func (application *App) Router() http.Handler {
	router := chi.NewRouter()
	router.Get("/healthz", application.health)
	router.Group(func(protected chi.Router) {
		protected.Use(application.concurrencyMiddleware)
		protected.Use(application.apiKeyMiddleware)
		protected.Get("/v1/models", application.listModels)
		protected.Get("/v1/models/{model}", application.getModel)
		protected.Get("/v1/responses", application.createResponsesWebSocket)
		protected.Post("/v1/responses", application.createResponses)
		protected.Get("/v1/responses/{id}", application.getResponse)
		protected.Delete("/v1/responses/{id}", application.deleteResponse)
		protected.Post("/v1/chat/completions", application.createChatCompletions)
		protected.Post("/v1/messages", application.createMessages)
		protected.Post("/v1/audio/transcriptions", application.createTranscription)
	})
	if application.dashboard != nil {
		application.dashboard.Register(router)
	}
	return router
}

// HTTPServer 创建带有应用路由和超时配置的 HTTP 服务。
func (application *App) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              application.serverConfig.Listen,
		Handler:           application.Router(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

func (application *App) health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"status":"ok"}` + "\n"))
}

// listModels 返回 OpenAI 兼容的 GET /v1/models 列表。
func (application *App) listModels(writer http.ResponseWriter, request *http.Request) {
	if application.adapter == nil {
		writeJSONError(writer, http.StatusServiceUnavailable, "provider adapter is not configured")
		return
	}
	models, err := application.adapter.ListModels(request.Context())
	if err != nil {
		writeJSONError(writer, http.StatusBadGateway, err.Error())
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, modelEntry(m))
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"object": "list", "data": data})
}

// modelEntry 把目录中的模型信息序列化为 /v1/models 条目：
// 前三项是 OpenAI 契约字段，其余为上游目录的非标准扩展字段（缺省不输出）。
func modelEntry(m adapter.ModelInfo) map[string]any {
	created := m.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	ownedBy := m.OwnedBy
	if ownedBy == "" {
		ownedBy = "devin"
	}
	entry := map[string]any{
		"id": m.ID, "object": "model", "created": created, "owned_by": ownedBy,
	}
	entry["supports_images"] = m.SupportsImages
	if m.Label != "" {
		entry["label"] = m.Label
	}
	if m.Description != "" {
		entry["description"] = m.Description
	}
	if m.Family != "" {
		entry["family"] = m.Family
	}
	if m.Provider != "" {
		entry["provider"] = m.Provider
	}
	if m.APIProvider != "" {
		entry["api_provider"] = m.APIProvider
	}
	if m.CostTier != "" {
		entry["cost_tier"] = m.CostTier
	}
	if m.PricingType != "" {
		entry["pricing_type"] = m.PricingType
	}
	if m.CreditMultiplier != 0 {
		entry["credit_multiplier"] = m.CreditMultiplier
	}
	if m.MaxTokens > 0 {
		entry["context_window"] = m.MaxTokens
	}
	if m.Promo != nil {
		promo := map[string]any{"active": true, "label": m.Promo.Label}
		if m.Promo.EndDate > 0 {
			promo["end_date"] = time.Unix(m.Promo.EndDate, 0).UTC().Format(time.RFC3339)
		}
		entry["promo"] = promo
	}
	flags := map[string]bool{
		"is_beta": m.IsBeta, "is_new": m.IsNew, "is_recommended": m.IsRecommended,
		"is_premium": m.IsPremium, "is_capacity_limited": m.IsCapacityLimited,
		"is_command": m.IsCommand, "disabled": m.Disabled,
	}
	for key, value := range flags {
		if value {
			entry[key] = true
		}
	}
	if m.BetaWarning != "" {
		entry["beta_warning"] = m.BetaWarning
	}
	return entry
}

// getModel 返回 OpenAI 兼容的 GET /v1/models/{model}。
func (application *App) getModel(writer http.ResponseWriter, request *http.Request) {
	if application.adapter == nil {
		writeJSONError(writer, http.StatusServiceUnavailable, "provider adapter is not configured")
		return
	}
	id := chi.URLParam(request, "model")
	if id == "" {
		writeJSONError(writer, http.StatusBadRequest, "model id is required")
		return
	}
	models, err := application.adapter.ListModels(request.Context())
	if err != nil {
		writeJSONError(writer, http.StatusBadGateway, err.Error())
		return
	}
	for _, m := range models {
		if m.ID == id {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(modelEntry(m))
			return
		}
	}
	writeJSONError(writer, http.StatusNotFound, fmt.Sprintf("model %q not found", id))
}

func writeJSONError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": "invalid_request_error", "code": nil, "param": nil},
	})
}

func writeAuthError(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": "unauthenticated", "code": nil, "param": nil},
	})
}

// concurrencyMiddleware 限制同时处理的 /v1/* 请求数，避免上游阻塞时资源耗尽。
func (application *App) concurrencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case application.concurrency <- struct{}{}:
			defer func() { <-application.concurrency }()
			next.ServeHTTP(writer, request)
		default:
			writeJSONError(writer, http.StatusServiceUnavailable, "server is busy, please try again later")
		}
	})
}

// apiKeyMiddleware 校验 OpenAI 兼容接口的 API Key。
// 支持标准 Authorization: Bearer <key> 与兼容头 X-Api-Key: <key>。
func (application *App) apiKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.TrimSpace(application.apiKey) == "" {
			next.ServeHTTP(writer, request)
			return
		}

		var provided string
		if auth := request.Header.Get("Authorization"); auth != "" {
			const prefix = "Bearer "
			if strings.HasPrefix(auth, prefix) {
				provided = strings.TrimSpace(auth[len(prefix):])
			}
		}
		if provided == "" {
			if key := request.Header.Get("X-Api-Key"); key != "" {
				provided = strings.TrimSpace(key)
			}
		}
		if provided == "" {
			writeAuthError(writer, "Missing API key")
			return
		}

		expectedHash := sha256.Sum256([]byte(application.apiKey))
		providedHash := sha256.Sum256([]byte(provided))
		if subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
			writeAuthError(writer, "Invalid API key")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (application *App) createResponses(writer http.ResponseWriter, request *http.Request) {
	var resolve respproto.ItemResolver
	if application.responses != nil {
		caller := application.callerKey()
		resolve = func(id string) (json.RawMessage, bool) {
			raw, ok := application.responses.GetItem(caller, id)
			return raw, ok
		}
	}
	application.createCompletion(writer, request, decodeResponsesRequestWith(resolve), responsesProtocol{})
}

// callerKey 是 Responses 会话存储的租户键（单 API key 部署下即该 key）。
func (application *App) callerKey() string {
	if application.apiKey == "" {
		return "default"
	}
	return application.apiKey
}

// getResponse 实现 GET /v1/responses/{id}：回放已保存（store 未关闭）的 Response 对象。
func (application *App) getResponse(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	if application.responses != nil {
		if body, ok := application.responses.GetResponse(application.callerKey(), id); ok {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(body)
			return
		}
	}
	writeResponseNotFound(writer, id)
}

// deleteResponse 实现 DELETE /v1/responses/{id}：删除会话及其 output item 索引。
func (application *App) deleteResponse(writer http.ResponseWriter, request *http.Request) {
	id := chi.URLParam(request, "id")
	if application.responses == nil || !application.responses.Delete(application.callerKey(), id) {
		writeResponseNotFound(writer, id)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"id": id, "object": "response.deleted", "deleted": true})
}

func writeResponseNotFound(writer http.ResponseWriter, id string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
		"message": fmt.Sprintf("Response with id '%s' not found.", id),
		"type":    "invalid_request_error", "param": nil, "code": "not_found",
	}})
}

func (application *App) createChatCompletions(writer http.ResponseWriter, request *http.Request) {
	application.createCompletion(writer, request, decodeChatRequest, chatProtocol{})
}

func (application *App) createMessages(writer http.ResponseWriter, request *http.Request) {
	application.createCompletion(writer, request, decodeAnthropicRequest, anthropicProtocol{})
}

// createTranscription 处理 OpenAI 兼容的 POST /v1/audio/transcriptions：
// multipart/form-data 中的 file 字段为音频（wav/mp3/m4a 等，上限 25MiB），
// 上游 GetTranscription 转写后以 {"text": ...} 返回；model 等附加字段透传忽略。
func (application *App) createTranscription(writer http.ResponseWriter, request *http.Request) {
	startedAt := time.Now()
	result := "failed"
	var respData []byte
	defer func() {
		if application.stats == nil {
			return
		}
		entry := stats.Entry{Path: request.URL.Path, IP: clientIP(request), Model: "transcription", Result: result, DurationMS: time.Since(startedAt).Milliseconds()}
		if len(request.Header) > 0 {
			entry.ReqHeaders = make(map[string]string, len(request.Header))
			for name, values := range request.Header {
				if sensitiveHeaders[http.CanonicalHeaderKey(name)] {
					continue
				}
				entry.ReqHeaders[name] = strings.Join(values, ", ")
			}
		}
		entry.RespBody = stats.TruncateBody(respData)
		application.stats.Record(entry)
	}()
	if application.adapter == nil {
		writeJSONError(writer, http.StatusServiceUnavailable, "provider adapter is not configured")
		return
	}
	transcriber, ok := application.adapter.(adapter.AudioTranscriber)
	if !ok {
		writeJSONError(writer, http.StatusServiceUnavailable, "audio transcription is not supported by the configured provider")
		return
	}
	// 与 OpenAI 25MB 上限对齐；multipart 解析只留 file 字段。
	request.Body = http.MaxBytesReader(writer, request.Body, 25<<20)
	if err := request.ParseMultipartForm(4 << 20); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "expected multipart/form-data with a file field")
		return
	}
	file, _, err := request.FormFile("file")
	if err != nil {
		writeJSONError(writer, http.StatusBadRequest, "multipart field \"file\" is required")
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(file)
	if err != nil || len(audio) == 0 {
		writeJSONError(writer, http.StatusBadRequest, "failed to read audio file")
		return
	}
	text, err := transcriber.TranscribeAudio(request.Context(), audio)
	if err != nil {
		writeJSONError(writer, mapProviderErrorStatus(err), err.Error())
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	respData, _ = json.Marshal(map[string]any{"text": text})
	_, _ = writer.Write(respData)
	result = "completed"
}

func (application *App) createCompletion(
	writer http.ResponseWriter,
	request *http.Request,
	decoder decodeRequestFunc,
	protocol protocolEncoder,
) {
	recorder := application.debugManager.Start(debuglog.RequestMeta{Method: request.Method, Path: request.URL.Path})
	completion := debuglog.Completion{StatusCode: http.StatusInternalServerError, Result: "failed"}
	startedAt := time.Now()
	var finalMessage *llm.AssistantMessage
	var reqBodyCopy, respBody []byte
	defer func() {
		recorder.Complete(completion)
		application.recordStats(request, reqBodyCopy, respBody, completion, finalMessage, startedAt)
	}()

	if application.adapter == nil {
		completion.StatusCode = http.StatusServiceUnavailable
		writeLoggedError(writer, recorder, "provider_configuration", completion.StatusCode, errors.New("provider adapter is not configured"))
		return
	}
	// 图片 base64 会显著放大 JSON；与常见 IDE 多图请求对齐到 32MiB。
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 32<<20))
	if err != nil {
		completion.StatusCode = http.StatusBadRequest
		writeLoggedError(writer, recorder, "http_read", completion.StatusCode, fmt.Errorf("read request: %w", err))
		return
	}
	reqBodyCopy = body
	recorder.WriteJSON("01-http-request.json", httpRequestProjection(request, body))
	messages, options, err := decoder(body)
	if err != nil {
		completion.StatusCode = http.StatusBadRequest
		writeLoggedError(writer, recorder, "http_decode", completion.StatusCode, err)
		return
	}
	completion.Model = messages.Model
	completion.Stream = options.Stream
	// previous_response_id 只有 Responses 协议会填；其余协议该字段恒为空，
	// WithHistory 原样返回，行为与改造前一致。
	//
	// 解析不到 / 客户端已带完整历史都不打断请求（兼容性优先，见 WithHistory 注释），
	// 但要把结果写成可观测事实，而不是退回"每轮盲答且无人知道"。
	if options.PreviousResponseID != "" {
		caller := application.apiKey
		if caller == "" {
			caller = "default"
		}
		merged, resolution, historyErr := respproto.WithHistory(messages, options.PreviousResponseID, caller, application.responses.Get)
		if historyErr != nil {
			completion.StatusCode = http.StatusBadRequest
			writeLoggedError(writer, recorder, "previous_response_id", completion.StatusCode, historyErr)
			return
		}
		messages = respproto.ResolvePendingToolNames(merged)
		switch {
		case resolution.Injected:
			recorder.WriteJSON("02b-history-injected.json", map[string]any{
				"previous_response_id": options.PreviousResponseID,
				"history_messages":     resolution.HistoryCount,
			})
		case resolution.ClientSuppliedFullHistory:
		case resolution.UnknownID:
			recorder.WriteJSON("02b-history-unresolved.json", map[string]any{
				"previous_response_id": options.PreviousResponseID,
				"reason":               "unknown, expired, or owned by another caller",
			})
		}
	}
	recorder.WriteJSON("02-request-messages.json", debuglog.RequestMessagesProjection(messages))
	ctx := debuglog.WithRecorder(request.Context(), recorder)
	stream, err := application.adapter.Stream(ctx, messages)
	if err != nil {
		completion.StatusCode = mapProviderErrorStatus(err)
		writeLoggedError(writer, recorder, "provider_stream", completion.StatusCode, err)
		return
	}
	if options.Stream {
		firstEvent, err := receiveEvent(ctx, stream, recorder)
		if err == nil && firstEvent.Type == llm.ResponseEventError {
			if firstEvent.Error != nil && firstEvent.Error.ErrorMessage != "" {
				err = errors.New(firstEvent.Error.ErrorMessage)
			} else {
				err = errors.New("response stream returned an error event immediately")
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			completion.StatusCode = mapProviderErrorStatus(err)
			writeLoggedError(writer, recorder, "provider_stream", completion.StatusCode, err)
			return
		}

		completion.StatusCode = http.StatusOK
		message, encoder, streamErr := writeProtocolStream(ctx, writer, stream, recorder, protocol, messages.Model, options, firstEvent, err)
		updateCompletionIdentity(&completion, message)
		finalMessage = message
		if streamErr != nil {
			if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
				completion.Result = "disconnected"
				recorder.WriteError("client_disconnected", streamErr)
			} else {
				recorder.WriteError("http_stream", streamErr)
			}
			return
		}
		completion.Result = "completed"
		if options.Store {
			application.rememberStreamExchange(messages, message, encoder)
		}
		return
	}
	message, err := collectFinalMessage(ctx, stream, recorder)
	if err != nil {
		completion.StatusCode = mapProviderErrorStatus(err)
		writeLoggedError(writer, recorder, "response_event", completion.StatusCode, err)
		return
	}
	updateCompletionIdentity(&completion, message)
	finalMessage = message
	body, err = encodeFinal(protocol, message, options)
	if err != nil {
		completion.StatusCode = http.StatusInternalServerError
		writeLoggedError(writer, recorder, "http_encode", completion.StatusCode, err)
		return
	}
	respBody = body
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write(body); err != nil {
		completion.Result = "disconnected"
		recorder.WriteError("client_disconnected", err)
		return
	}
	recorder.AppendJSONL("06-http-response.jsonl", "response", json.RawMessage(body))
	completion.StatusCode = http.StatusOK
	completion.Result = "completed"
	if options.Store {
		application.rememberResponsesExchange(messages, message, body)
	}
}

// rememberResponsesExchange 用「客户端实际收到的那个响应 id」作为键保存会话。
//
// 为什么不能直接用 reply.ResponseID：协议层（responses.EncodeResponse）在
// ResponseID 不以 "resp_" 开头时会本地另生成一个返回给客户端。若这里按
// reply.ResponseID 存，客户端下一轮回传的是生成值，必然查不到。
// 因此从编码后的响应体里读回 id，保证两侧落到同一个键上。
func (application *App) rememberResponsesExchange(
	requestMessages llm.RequestMessages,
	reply *llm.AssistantMessage,
	encoded []byte,
) {
	if application.responses == nil || reply == nil || len(encoded) == 0 {
		return
	}
	var projection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(encoded, &projection); err != nil || projection.ID == "" {
		return
	}
	caller := application.apiKey
	if caller == "" {
		caller = "default"
	}
	history := make([]llm.Message, 0, len(requestMessages.Messages)+1)
	history = append(history, requestMessages.Messages...)
	history = append(history, *reply)
	application.responses.PutResponse(caller, projection.ID, history, encoded)
}

// sensitiveHeaders 不计入「最近请求」详情的请求头（含密钥）。
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"X-Api-Key":           true,
	"Cookie":              true,
	"Proxy-Authorization": true,
}

// clientIP 取请求方 IP：优先 X-Forwarded-For 首个 hop，否则 RemoteAddr 主机部分。
func clientIP(request *http.Request) string {
	if xff := request.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

// recordStats 在请求收口处写入统计；finalMessage 为 nil 时（失败/断开）仅计请求与耗时。
// reqBody/respBody 为面板「最近请求」详情，仅内存保留、截断到上限。
func (application *App) recordStats(request *http.Request, reqBody, respBody []byte, completion debuglog.Completion, message *llm.AssistantMessage, startedAt time.Time) {
	if application.stats == nil {
		return
	}
	entry := stats.Entry{
		Path:       request.URL.Path,
		IP:         clientIP(request),
		Model:      completion.Model,
		Result:     completion.Result,
		Status:     completion.StatusCode,
		DurationMS: time.Since(startedAt).Milliseconds(),
		ReqBody:    stats.TruncateBody(reqBody),
	}
	if len(request.Header) > 0 {
		entry.ReqHeaders = make(map[string]string, len(request.Header))
		for name, values := range request.Header {
			if sensitiveHeaders[http.CanonicalHeaderKey(name)] {
				continue
			}
			entry.ReqHeaders[name] = strings.Join(values, ", ")
		}
	}
	if len(respBody) > 0 {
		entry.RespBody = stats.TruncateBody(respBody)
	} else if message != nil {
		// 流式路径没有整包响应字节，序列化最终消息（含用量/停止原因）作为详情。
		if data, err := json.Marshal(message); err == nil {
			entry.RespBody = stats.TruncateBody(data)
		}
	}
	if message != nil {
		usage := message.Usage
		entry.Input = usage.Input
		entry.Output = usage.Output
		entry.CacheRead = usage.CacheRead
		entry.CacheWrite = usage.CacheWrite
		if usage.TotalTokens > 0 {
			entry.Total = usage.TotalTokens
		} else {
			entry.Total = usage.Input + usage.Output
		}
	}
	application.stats.Record(entry)
}

func writeProtocolStream(
	ctx context.Context,
	writer http.ResponseWriter,
	stream llm.ResponseStream,
	recorder *debuglog.Recorder,
	protocol protocolEncoder,
	model string,
	options protocolOptions,
	firstEvent llm.ResponseEvent,
	firstErr error,
) (*llm.AssistantMessage, streamEncoder, error) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return nil, nil, errors.New("streaming response writer does not support flushing")
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	encoder := newStreamEncoderWithStore(protocol, model, options)
	var latest *llm.AssistantMessage
	event, err := firstEvent, firstErr
	for {
		if errors.Is(err, io.EOF) {
			return latest, encoder, nil
		}
		if err != nil {
			return latest, encoder, err
		}
		latest = eventMessage(event, latest)
		encodedEvents, encodeErr := encoder.Encode(event)
		if encodeErr != nil {
			return latest, encoder, encodeErr
		}
		for _, encoded := range encodedEvents {
			if _, wErr := writer.Write(protocol.SSEFormat(encoded.Name, encoded.Data)); wErr != nil {
				return latest, encoder, wErr
			}
			if encoded.Name == "[DONE]" {
				recorder.AppendJSONL("06-http-response.jsonl", encoded.Name, string(encoded.Data))
			} else {
				recorder.AppendJSONL("06-http-response.jsonl", encoded.Name, json.RawMessage(encoded.Data))
			}
			flusher.Flush()
		}
		if event.Type == llm.ResponseEventError {
			// 错误 SSE 已在上面循环写出，这里直接返回错误供外层记录失败日志。
			if event.Error != nil && event.Error.ErrorMessage != "" {
				return latest, encoder, errors.New(event.Error.ErrorMessage)
			}
			return latest, encoder, errors.New("response stream returned an error event")
		}
		event, err = receiveEvent(ctx, stream, recorder)
	}
}

func collectFinalMessage(ctx context.Context, stream llm.ResponseStream, recorder *debuglog.Recorder) (*llm.AssistantMessage, error) {
	var final *llm.AssistantMessage
	for {
		event, err := receiveEvent(ctx, stream, recorder)
		if errors.Is(err, io.EOF) {
			if final == nil {
				return nil, errors.New("response stream ended without a final message")
			}
			return final, nil
		}
		if err != nil {
			return nil, err
		}
		switch event.Type {
		case llm.ResponseEventDone:
			if event.Message == nil {
				return nil, errors.New("done event has no final message")
			}
			final = event.Message
		case llm.ResponseEventError:
			if event.Error == nil {
				return nil, errors.New("error event has no error message")
			}
			return nil, errors.New(event.Error.ErrorMessage)
		}
	}
}

func receiveEvent(ctx context.Context, stream llm.ResponseStream, recorder *debuglog.Recorder) (llm.ResponseEvent, error) {
	event, err := stream.Recv(ctx)
	if err == nil {
		recorder.AppendJSONL("05-response-events.jsonl", string(event.Type), debuglog.ResponseEventProjection(event))
	}
	return event, err
}

func eventMessage(event llm.ResponseEvent, fallback *llm.AssistantMessage) *llm.AssistantMessage {
	if event.Message != nil {
		return event.Message
	}
	if event.Error != nil {
		return event.Error
	}
	if event.Partial != nil {
		return event.Partial
	}
	return fallback
}

func updateCompletionIdentity(completion *debuglog.Completion, message *llm.AssistantMessage) {
	if message == nil {
		return
	}
	completion.Provider = message.Provider
	if message.ResponseModel != "" {
		completion.Model = message.ResponseModel
	} else if message.Model != "" {
		completion.Model = message.Model
	}
}

func httpRequestProjection(request *http.Request, body []byte) map[string]any {
	var parsedBody any
	if err := json.Unmarshal(body, &parsedBody); err != nil {
		parsedBody = string(body)
	}
	return map[string]any{
		"method": request.Method,
		"path":   request.URL.Path,
		"headers": map[string]string{
			"accept":       request.Header.Get("Accept"),
			"content_type": request.Header.Get("Content-Type"),
			"user_agent":   request.Header.Get("User-Agent"),
		},
		"body": parsedBody,
	}
}

func writeLoggedError(writer http.ResponseWriter, recorder *debuglog.Recorder, stage string, status int, err error) {
	recorder.WriteError(stage, err)
	message := err.Error()
	errorType := common.OpenAIErrorType(message)
	// 客户端可修正的错误用 invalid_request_error，便于 IDE 直接展示。
	if status == http.StatusBadRequest ||
		strings.Contains(message, "does not support image") ||
		strings.Contains(message, "invalid_argument") ||
		strings.HasPrefix(message, "invalid_argument:") {
		errorType = "invalid_request_error"
		if status >= 500 {
			status = http.StatusBadRequest
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	// 透传完整 message，不改写上游文案。
	response := map[string]any{"error": map[string]any{
		"message": message,
		"type":    errorType,
		"code":    nil,
		"param":   nil,
	}}
	_ = json.NewEncoder(writer).Encode(response)
	recorder.AppendJSONL("06-http-response.jsonl", "error", response)
}

// mapProviderErrorStatus 将上游/适配器错误映射为合适的 HTTP 状态，message 仍原样透传。
func mapProviderErrorStatus(err error) int {
	if err == nil {
		return http.StatusBadGateway
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "does not support image"),
		strings.Contains(msg, "invalid_argument"),
		strings.HasPrefix(msg, "invalid_argument:"),
		strings.Contains(msg, "file_id images"),
		strings.Contains(msg, "only data URL"),
		strings.Contains(msg, "validate Devin request"),
		strings.Contains(msg, "validate adapted request"):
		return http.StatusBadRequest
	case strings.Contains(msg, "unauthenticated"),
		strings.HasPrefix(msg, "unauthenticated:"):
		return http.StatusUnauthorized
	case strings.Contains(msg, "permission_denied"),
		strings.HasPrefix(msg, "permission_denied:"):
		return http.StatusForbidden
	case strings.Contains(msg, "not_found"),
		strings.HasPrefix(msg, "not_found:"):
		return http.StatusNotFound
	case strings.Contains(msg, "resource_exhausted"),
		strings.HasPrefix(msg, "resource_exhausted:"):
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

// streamIDProvider 由能自报响应 id 的流式编码器实现（目前只有 Responses）。
type streamIDProvider interface {
	ResponseID() string
}

// rememberStreamExchange 保存一次流式 Responses 会话。
//
// 流式响应公布的 id 由 StreamEncoder 在构造时生成，与 message.ResponseID 无关，
// 事后无法从上游字段反推；因此必须从编码器取回**客户端真正看到的那个 id**，
// 否则存下的键永远无法被下一轮 previous_response_id 命中。
func (application *App) rememberStreamExchange(
	requestMessages llm.RequestMessages,
	reply *llm.AssistantMessage,
	encoder streamEncoder,
) {
	if application.responses == nil || reply == nil || encoder == nil {
		return
	}
	provider, ok := encoder.(streamIDProvider)
	if !ok {
		return
	}
	responseID := provider.ResponseID()
	if responseID == "" {
		return
	}
	caller := application.apiKey
	if caller == "" {
		caller = "default"
	}
	history := make([]llm.Message, 0, len(requestMessages.Messages)+1)
	history = append(history, requestMessages.Messages...)
	history = append(history, *reply)
	var final []byte
	if finalProvider, ok := encoder.(interface{ FinalResponse() []byte }); ok {
		final = finalProvider.FinalResponse()
	}
	application.responses.PutResponse(caller, responseID, history, final)
}

// encodeFinal 编码非流式响应。
// Responses 协议的 store 字段和请求侧参数必须如实反映：恒定 true 会让写了
// store:false 的调用方以为会话被保留，而事实上它并没有。
func encodeFinal(
	protocol protocolEncoder,
	message *llm.AssistantMessage,
	options protocolOptions,
) ([]byte, error) {
	if responds, ok := protocol.(responsesProtocol); ok {
		return responds.EncodeFinalWithEcho(message, options.Store, options.Echo)
	}
	return protocol.EncodeFinal(message)
}

// newStreamEncoderWithStore 创建流式编码器，并把真实的 store 值交给 Responses 编码器。
func newStreamEncoderWithStore(
	protocol protocolEncoder,
	model string,
	options protocolOptions,
) streamEncoder {
	if _, ok := protocol.(responsesProtocol); ok {
		return NewResponsesStreamEncoder(model, options.Store, options.Echo)
	}
	return protocol.NewStreamEncoder(model, options.IncludeUsage)
}
