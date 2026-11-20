// Package dashboard 实现管理面板：模型列表、价格筛选、账户用量。
// password 为空时无需登录直接进入；非空时走 session cookie。
package dashboard

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/leookun/devin-2api/internal/httpproxy"
	"github.com/leookun/devin-2api/internal/stats"

	devinproto "local/devinproto"
	"local/devinproto/devinprotoconnect"

	"google.golang.org/protobuf/proto"
)

const (
	clientName    = "windsurf"
	clientVersion = "1.48.2"
	// seatUserStatusPath 与 Windsurf 官方 / WindsurfAPI 一致的 JSON Connect 路径。
	// 生成的 connect 包名 ExaSeatManagementPb_SeatManagementService 在上游会 404。
	seatUserStatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"
)

// Handler 是面板 HTTP 处理器。
type Handler struct {
	password      string
	baseURL       string
	token         string
	apiClient     devinprotoconnect.ApiServerServiceClient
	httpClient    *http.Client
	baseTransport http.RoundTripper
	sessionMu     sync.RWMutex
	sessionTokens map[string]time.Time
	// loginFailures 按来源 IP 记录登录失败，防止在线爆破面板密码。
	loginFailures map[string]*loginFailure

	// 面板数据缓存：模型目录、供应商列表、模型状态均不经常变化，缓存可显著降低上游压力。
	cacheMu             sync.RWMutex
	cacheTTL            time.Duration
	catalogCache        *modelCatalog
	catalogExpiry       time.Time
	providersCache      []map[string]any
	providersExpiry     time.Time
	modelStatusesCache  []map[string]any
	modelStatusesExpiry time.Time
	// stats 是可选的请求统计采集器。
	stats *stats.Collector
}

// loginFailure 是单个来源的失败计数窗口。
type loginFailure struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

const (
	loginMaxFailures = 5
	loginWindow      = 15 * time.Minute
	loginLockout     = 15 * time.Minute
)

// loginSource 取 TCP 对端地址作为限流键：X-Forwarded-For 可被伪造来绕过限流，不采信。
func loginSource(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// loginLocked 报告来源是否处于锁定期，并返回剩余时长。
func (h *Handler) loginLocked(source string, now time.Time) (time.Duration, bool) {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	failure := h.loginFailures[source]
	if failure == nil || !now.Before(failure.lockedUntil) {
		return 0, false
	}
	return failure.lockedUntil.Sub(now), true
}

// recordLoginResult 更新失败计数：成功清零，窗口内失败达到上限即锁定。
func (h *Handler) recordLoginResult(source string, ok bool, now time.Time) {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	if h.loginFailures == nil {
		h.loginFailures = map[string]*loginFailure{}
	}
	if ok {
		delete(h.loginFailures, source)
		return
	}
	failure := h.loginFailures[source]
	if failure == nil || now.Sub(failure.windowStart) > loginWindow {
		failure = &loginFailure{windowStart: now}
		h.loginFailures[source] = failure
	}
	failure.count++
	if failure.count >= loginMaxFailures {
		failure.lockedUntil = now.Add(loginLockout)
		failure.count = 0
		failure.windowStart = now
	}
	// 顺手清理过期记录与会话，防止长期运行时表无限增长。
	for key, entry := range h.loginFailures {
		if now.Sub(entry.windowStart) > loginWindow && !now.Before(entry.lockedUntil) {
			delete(h.loginFailures, key)
		}
	}
	for token, expiry := range h.sessionTokens {
		if now.After(expiry) {
			delete(h.sessionTokens, token)
		}
	}
}

// modelCatalog 是上游返回的模型目录：模型配置 + 客户端模型分组（分类）。
type modelCatalog struct {
	models []map[string]any
	sorts  []map[string]any
}

// New 创建面板处理器。password 为空表示开放访问。proxy 为可选代理地址。
// forceHTTP1 为 true 时强制 HTTP/1.1，与 adapter 保持一致的连接模型。
func New(password, baseURL, token, proxy string, forceHTTP1 bool) *Handler {
	base, err := httpproxy.NewTransport(proxy, forceHTTP1)
	if err != nil {
		// 代理配置错误时回退到默认 transport，保证面板仍可尝试工作。
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	transport := &authTransport{base: base, token: token}
	// 面板可能遇到上游长时思考/排队，超时与 ResponseHeaderTimeout 对齐。
	httpClient := &http.Client{Transport: transport, Timeout: 610 * time.Second}
	return &Handler{
		password:      password,
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         token,
		apiClient:     devinprotoconnect.NewApiServerServiceClient(httpClient, baseURL),
		httpClient:    httpClient,
		baseTransport: base,
		sessionTokens: make(map[string]time.Time),
		cacheTTL:      5 * time.Minute,
	}
}

// Register 将面板路由注册到 mux。有 token 即可启用；密码仅控制是否登录。
func (h *Handler) Register(mux interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}) {
	mux.Get("/panel", h.servePanel)
	mux.Post("/panel/login", h.handleLogin)
	mux.Get("/panel/api/status", h.apiStatus)
	mux.Get("/panel/api/models", h.apiModels)
	mux.Get("/panel/api/stats", h.apiStats)
}

// SetStats 注入请求统计采集器；nil 时 /panel/api/stats 返回空统计。
func (h *Handler) SetStats(collector *stats.Collector) {
	h.stats = collector
}

func (h *Handler) apiStats(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if h.stats == nil {
		_ = json.NewEncoder(w).Encode(stats.Snapshot{})
		return
	}
	_ = json.NewEncoder(w).Encode(h.stats.Snapshot())
}

func (h *Handler) servePanel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.password != "" && !h.isAuthenticated(r) {
		_, _ = w.Write([]byte(loginPage))
		return
	}
	_, _ = w.Write([]byte(dashboardPage))
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if h.password == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"open":true}`))
		return
	}
	source, now := loginSource(r), time.Now()
	if remaining, locked := h.loginLocked(source, now); locked {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", strconv.Itoa(int(remaining.Seconds())+1))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, `{"error":"失败次数过多，请 %d 分钟后再试"}`, int(remaining.Minutes())+1)
		return
	}
	// 先哈希再比较：ConstantTimeCompare 对长度不等会立即返回，泄露密码长度。
	provided, expected := sha256.Sum256([]byte(r.FormValue("password"))), sha256.Sum256([]byte(h.password))
	if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		h.recordLoginResult(source, false, now)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"密码错误"}`))
		return
	}
	h.recordLoginResult(source, true, now)
	sessionID := generateSessionID()
	h.sessionMu.Lock()
	h.sessionTokens[sessionID] = time.Now().Add(24 * time.Hour)
	h.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "devin_panel_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *Handler) isAuthenticated(r *http.Request) bool {
	if h.password == "" {
		return true
	}
	cookie, err := r.Cookie("devin_panel_session")
	if err != nil {
		return false
	}
	h.sessionMu.RLock()
	expiry, ok := h.sessionTokens[cookie.Value]
	h.sessionMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		h.sessionMu.Lock()
		delete(h.sessionTokens, cookie.Value)
		h.sessionMu.Unlock()
		return false
	}
	return true
}

func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if h.isAuthenticated(r) {
		return true
	}
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"未授权"}`))
	return false
}

func (h *Handler) apiStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}
	// 面板聚合多个上游调用，给足时间避免单个慢接口拖垮整体；
	// 与 ResponseHeaderTimeout 对齐，允许上游长时思考/排队。
	ctx, cancel := context.WithTimeout(r.Context(), 610*time.Second)
	defer cancel()

	result := map[string]any{"upstream": h.baseURL}
	var resultMu sync.Mutex

	// 正确路径：JSON Connect SeatManagement GetUserStatus（Bearer + metadata.api_key）
	if user, plan, planInfo, err := h.fetchUserStatus(ctx); err != nil {
		resultMu.Lock()
		result["user_status_error"] = err.Error()
		resultMu.Unlock()
	} else {
		resultMu.Lock()
		if user != nil {
			result["user"] = user
		}
		if plan != nil {
			result["plan_status"] = plan
		}
		if planInfo != nil {
			result["plan_info"] = planInfo
		}
		resultMu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		capResp, err := h.apiClient.CheckChatCapacity(ctx, connect.NewRequest(&devinproto.CheckChatCapacityRequest{
			Metadata: buildMetadata(h.token),
		}))
		resultMu.Lock()
		defer resultMu.Unlock()
		if err != nil {
			result["capacity_error"] = err.Error()
			return
		}
		result["capacity"] = map[string]any{
			"has_capacity":    capResp.Msg.GetHasCapacity(),
			"message":         capResp.Msg.GetMessage(),
			"active_sessions": capResp.Msg.GetActiveSessions(),
		}
	}()

	go func() {
		defer wg.Done()
		statusResp, err := h.apiClient.GetStatus(ctx, connect.NewRequest(&devinproto.GetStatusRequest{
			Metadata: buildMetadata(h.token),
		}))
		resultMu.Lock()
		defer resultMu.Unlock()
		if err != nil {
			result["status_error"] = err.Error()
			return
		}
		st := statusResp.Msg.GetStatus()
		result["ide_status"] = map[string]any{
			"level":   shortEnum(st.GetLevel().String(), "STATUS_LEVEL_"),
			"message": st.GetMessage(),
		}
		result["show_review_prompt"] = statusResp.Msg.GetShowReviewPrompt()
	}()

	go func() {
		defer wg.Done()
		statuses := h.cachedModelStatuses(ctx)
		resultMu.Lock()
		defer resultMu.Unlock()
		if statuses != nil {
			result["model_statuses"] = statuses
		}
	}()

	go func() {
		defer wg.Done()
		providers := h.cachedProviders(ctx)
		resultMu.Lock()
		defer resultMu.Unlock()
		if providers != nil {
			result["providers"] = providers
		}
	}()

	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// fetchUserStatus 调用官方 seat_management JSON Connect 路径。
func (h *Handler) fetchUserStatus(ctx context.Context) (user, plan, planInfo map[string]any, err error) {
	bodyObj := map[string]any{
		"metadata": map[string]any{
			"api_key":           h.token,
			"extension_name":    clientName,
			"extension_version": clientVersion,
			"ide_name":          clientName,
			"ide_version":       clientVersion,
			"locale":            "en",
			"os":                "windows",
		},
	}
	payload, err := json.Marshal(bodyObj)
	if err != nil {
		return nil, nil, nil, err
	}
	url := h.baseURL + seatUserStatusPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+h.token)

	// 使用不带 Basic 改写的 client，避免 authTransport 覆盖 Bearer；但复用代理 transport。
	// 与 ResponseHeaderTimeout 对齐，允许上游长时思考/排队。
	client := &http.Client{Timeout: 610 * time.Second, Transport: h.baseTransport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("GetUserStatus HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, nil, fmt.Errorf("decode GetUserStatus: %w", err)
	}
	us, _ := root["userStatus"].(map[string]any)
	if us == nil {
		us, _ = root["user_status"].(map[string]any)
	}
	if us == nil {
		return nil, nil, nil, fmt.Errorf("GetUserStatus: empty userStatus")
	}

	user = map[string]any{
		"name":                strAny(us["name"]),
		"email":               strAny(us["email"]),
		"pro":                 boolAny(us["pro"]),
		"user_id":             strAny(us["userId"], us["user_id"]),
		"team_id":             strAny(us["teamId"], us["team_id"]),
		"teams_tier":          shortEnum(strAny(us["teamsTier"], us["teams_tier"]), "TEAMS_TIER_"),
		"used_prompt_credits": numAny(us["userUsedPromptCredits"], us["user_used_prompt_credits"]),
		"used_flow_credits":   numAny(us["userUsedFlowCredits"], us["user_used_flow_credits"]),
		"max_premium_chat":    numAny(us["maxNumPremiumChatMessages"], us["max_num_premium_chat_messages"]),
	}

	ps, _ := us["planStatus"].(map[string]any)
	if ps == nil {
		ps, _ = us["plan_status"].(map[string]any)
	}
	if ps != nil {
		plan = map[string]any{
			"available_prompt_credits": numAny(ps["availablePromptCredits"], ps["available_prompt_credits"]),
			"available_flow_credits":   numAny(ps["availableFlowCredits"], ps["available_flow_credits"]),
			"available_flex_credits":   numAny(ps["availableFlexCredits"], ps["available_flex_credits"]),
			"used_flex_credits":        numAny(ps["usedFlexCredits"], ps["used_flex_credits"]),
			"used_flow_credits":        numAny(ps["usedFlowCredits"], ps["used_flow_credits"]),
			"used_prompt_credits":      numAny(ps["usedPromptCredits"], ps["used_prompt_credits"]),
			"daily_quota_remaining":    numAny(ps["dailyQuotaRemainingPercent"], ps["daily_quota_remaining_percent"]),
			"weekly_quota_remaining":   numAny(ps["weeklyQuotaRemainingPercent"], ps["weekly_quota_remaining_percent"]),
			"daily_quota_reset":        numAny(ps["dailyQuotaResetAtUnix"], ps["daily_quota_reset_at_unix"]),
			"weekly_quota_reset":       numAny(ps["weeklyQuotaResetAtUnix"], ps["weekly_quota_reset_at_unix"]),
			"acu_consumed":             numAny(ps["acuConsumed"], ps["acu_consumed"]),
			"acu_limit":                numAny(ps["acuLimit"], ps["acu_limit"]),
			"overage_balance_micros":   numAny(ps["overageBalanceMicros"], ps["overage_balance_micros"]),
			"plan_start":               strAny(ps["planStart"], ps["plan_start"]),
			"plan_end":                 strAny(ps["planEnd"], ps["plan_end"]),
		}
		pi, _ := ps["planInfo"].(map[string]any)
		if pi == nil {
			pi, _ = ps["plan_info"].(map[string]any)
		}
		if pi != nil {
			plan["plan_name"] = strAny(pi["planName"], pi["plan_name"])
			plan["monthly_prompt_credits"] = numAny(pi["monthlyPromptCredits"], pi["monthly_prompt_credits"])
			plan["monthly_flow_credits"] = numAny(pi["monthlyFlowCredits"], pi["monthly_flow_credits"])
			plan["billing_strategy"] = shortEnum(strAny(pi["billingStrategy"], pi["billing_strategy"]), "BILLING_STRATEGY_")
			plan["is_teams"] = boolAny(pi["isTeams"], pi["is_teams"])
			plan["is_enterprise"] = boolAny(pi["isEnterprise"], pi["is_enterprise"])
			plan["can_buy_more"] = boolAny(pi["canBuyMoreCredits"], pi["can_buy_more_credits"])
			plan["has_paid_features"] = boolAny(pi["hasPaidFeatures"], pi["has_paid_features"])
		}
	}

	// 顶层 planInfo（部分响应会挂在 root）
	if top, ok := root["planInfo"].(map[string]any); ok {
		planInfo = map[string]any{
			"plan_name":                 strAny(top["planName"], top["plan_name"]),
			"monthly_prompt_credits":    numAny(top["monthlyPromptCredits"], top["monthly_prompt_credits"]),
			"monthly_flow_credits":      numAny(top["monthlyFlowCredits"], top["monthly_flow_credits"]),
			"billing_strategy":          shortEnum(strAny(top["billingStrategy"], top["billing_strategy"]), "BILLING_STRATEGY_"),
			"is_teams":                  boolAny(top["isTeams"], top["is_teams"]),
			"is_enterprise":             boolAny(top["isEnterprise"], top["is_enterprise"]),
			"has_paid_features":         boolAny(top["hasPaidFeatures"], top["has_paid_features"]),
			"max_premium_chat_messages": numAny(top["maxNumPremiumChatMessages"], top["max_num_premium_chat_messages"]),
		}
	} else if plan != nil {
		if name, ok := plan["plan_name"]; ok {
			planInfo = map[string]any{
				"plan_name":              name,
				"monthly_prompt_credits": plan["monthly_prompt_credits"],
				"monthly_flow_credits":   plan["monthly_flow_credits"],
				"billing_strategy":       plan["billing_strategy"],
				"is_teams":               plan["is_teams"],
				"is_enterprise":          plan["is_enterprise"],
				"has_paid_features":      plan["has_paid_features"],
			}
		}
	}
	return user, plan, planInfo, nil
}

func (h *Handler) apiModels(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}
	// 模型目录可能较大，给足时间并复用缓存；与 ResponseHeaderTimeout 对齐。
	ctx, cancel := context.WithTimeout(r.Context(), 610*time.Second)
	defer cancel()

	catalog, err := h.cachedCatalog(ctx)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"%s"}`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"models": catalog.models, "model_sorts": catalog.sorts})
}

func (h *Handler) cachedCatalog(ctx context.Context) (*modelCatalog, error) {
	h.cacheMu.RLock()
	if h.catalogCache != nil && time.Now().Before(h.catalogExpiry) {
		cached := h.catalogCache
		h.cacheMu.RUnlock()
		return cached, nil
	}
	h.cacheMu.RUnlock()

	resp, err := h.apiClient.GetCascadeModelConfigs(ctx, connect.NewRequest(&devinproto.GetCascadeModelConfigsRequest{
		Metadata: buildMetadata(h.token),
	}))
	if err != nil {
		return nil, err
	}

	var models []map[string]any
	seen := make(map[string]bool)
	buildEntry := func(c *devinproto.ExaCodeiumCommonPb_ClientModelConfig, command bool) map[string]any {
		uid := c.GetModelUid()
		if uid == "" && c.GetModelOrAlias() != nil {
			uid = c.GetModelOrAlias().GetModelUid()
		}
		costTier := "unspecified"
		switch c.GetModelCostTier() {
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_FREE:
			costTier = "free"
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_LOW:
			costTier = "low"
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_MEDIUM:
			costTier = "medium"
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_HIGH:
			costTier = "high"
		}

		mult := c.GetCreditMultiplier()
		multKnown := mult != 0 || costTier == "free"
		pricingType := shortEnum(c.GetPricingType().String(), "MODEL_PRICING_TYPE_")
		provider := shortEnum(c.GetProvider().String(), "MODEL_PROVIDER_")
		apiProvider := shortEnum(c.GetApiProvider().String(), "API_PROVIDER_")
		if pricingType == "UNSPECIFIED" {
			pricingType = ""
		}
		if provider == "UNSPECIFIED" {
			provider = ""
		}
		if provider == "13" {
			// 枚举 13 比抓取的 proto 更新；上游分组名为 Meta（Muse Wonder 系列）。
			provider = "META"
		}
		if apiProvider == "UNSPECIFIED" {
			apiProvider = ""
		}

		m := map[string]any{
			"uid":                 uid,
			"label":               c.GetLabel(),
			"description":         c.GetDescription(),
			"cost_tier":           costTier,
			"credit_multiplier":   mult,
			"multiplier_known":    multKnown,
			"pricing_type":        pricingType,
			"provider":            provider,
			"api_provider":        apiProvider,
			"max_tokens":          c.GetMaxTokens(),
			"disabled":            c.GetDisabled(),
			"is_premium":          c.GetIsPremium(),
			"is_beta":             c.GetIsBeta(),
			"is_new":              c.GetIsNew(),
			"is_recommended":      c.GetIsRecommended(),
			"supports_images":     c.GetSupportsImages(),
			"is_capacity_limited": c.GetIsCapacityLimited(),
			"supports_legacy":     c.GetSupportsLegacy(),
		}

		var dims []map[string]any
		var inputPrice, cachedPrice, outputPrice float64
		var hasInput, hasCached, hasOutput bool
		for _, d := range c.GetModelDimensions() {
			dim := map[string]any{
				"label":       d.GetLabel(),
				"value":       d.GetValue(),
				"min":         d.GetMinRange(),
				"max":         d.GetMaxRange(),
				"denominator": d.GetDenominator(),
				"kind":        shortEnum(d.GetKind().String(), "MODEL_DIMENSION_KIND_"),
				"info":        d.GetInfo(),
			}
			dims = append(dims, dim)
			switch strings.ToLower(d.GetLabel()) {
			case "input":
				inputPrice, hasInput = float64(d.GetValue()), true
			case "cached input", "cached_input", "cache read", "cache_read":
				cachedPrice, hasCached = float64(d.GetValue()), true
			case "output":
				outputPrice, hasOutput = float64(d.GetValue()), true
			}
		}
		if len(dims) > 0 {
			m["dimensions"] = dims
		}
		if hasInput {
			m["price_input"] = inputPrice
		}
		if hasCached {
			m["price_cached"] = cachedPrice
		}
		if hasOutput {
			m["price_output"] = outputPrice
		}

		if ps := c.GetPromoStatus(); ps != nil && ps.GetIsActive() {
			promo := map[string]any{"active": true, "label": ps.GetLabel()}
			if ed := ps.GetEndDate(); ed != nil && ed.GetSeconds() != 0 {
				promo["end_date"] = time.Unix(ed.GetSeconds(), int64(ed.GetNanos())).UTC().Format(time.RFC3339)
			}
			m["promo"] = promo
		}
		if fs := c.GetFastStatus(); fs != nil && fs.GetIsActive() {
			m["fast"] = map[string]any{"active": true, "tooltip": fs.GetTooltip()}
		}
		if fm := c.GetModelFamilyMetadata(); fm != nil {
			m["family"] = fm.GetModelFamilyLabel()
			m["is_default_in_family"] = fm.GetIsDefaultModelInFamily() || c.GetIsDefaultModelInFamily()
		}
		if dr := c.GetDisabledReason(); dr != nil {
			m["disabled_reason"] = dr.GetShortReason()
			m["disabled_description"] = dr.GetDescription()
		}
		if c.GetBetaWarningMessage() != "" {
			m["beta_warning"] = c.GetBetaWarningMessage()
		}
		if command {
			m["is_command"] = true
		}
		return m
	}
	for _, c := range resp.Msg.GetClientModelConfigs() {
		if e := buildEntry(c, false); e != nil {
			if uid, _ := e["uid"].(string); uid != "" {
				seen[uid] = true
			}
			models = append(models, e)
		}
	}
	// 合并命令模型目录（commit 信息等命令用模型，如 MODEL_PRIVATE_2 "Claude 4.5 Sonnet"）：
	// 不在 picker 中但可直接调用；已在目录中的去重；失败不影响主目录。
	if cmdResp, err := h.apiClient.GetCommandModelConfigs(ctx, connect.NewRequest(&devinproto.GetCommandModelConfigsRequest{
		Metadata: buildMetadata(h.token),
	})); err == nil {
		for _, c := range cmdResp.Msg.GetClientModelConfigs() {
			if e := buildEntry(c, true); e != nil {
				uid, _ := e["uid"].(string)
				if uid == "" || seen[uid] {
					continue
				}
				seen[uid] = true
				models = append(models, e)
			}
		}
	}

	// 客户端模型分组（分类）：上游下发的 picker 分组结构，面板用它做模型分类列/筛选。
	var sorts []map[string]any
	for _, s := range resp.Msg.GetClientModelSorts() {
		var groups []map[string]any
		for _, g := range s.GetGroups() {
			groups = append(groups, map[string]any{
				"name":         g.GetGroupName(),
				"model_labels": g.GetModelLabels(),
			})
		}
		sorts = append(sorts, map[string]any{
			"name":           s.GetName(),
			"display_metric": s.GetDisplayMetric(),
			"groups":         groups,
		})
	}
	catalog := &modelCatalog{models: models, sorts: sorts}

	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	// 请求期间可能有其他 goroutine 已写入缓存，不覆盖更热数据。
	if h.catalogCache != nil && time.Now().Before(h.catalogExpiry) {
		return h.catalogCache, nil
	}
	h.catalogCache = catalog
	h.catalogExpiry = time.Now().Add(h.cacheTTL)
	return catalog, nil
}

func buildMetadata(token string) *devinproto.ExaCodeiumCommonPb_Metadata {
	fingerprint, _ := randomHex(32)
	return &devinproto.ExaCodeiumCommonPb_Metadata{
		ApiKey:           proto.String(token),
		ExtensionName:    proto.String(clientName),
		ExtensionVersion: proto.String(clientVersion),
		IdeName:          proto.String(clientName),
		IdeVersion:       proto.String(clientVersion),
		Locale:           proto.String("en"),
		Os:               proto.String("win"),
		F:                proto.String(fingerprint),
	}
}

func (h *Handler) cachedProviders(ctx context.Context) []map[string]any {
	h.cacheMu.RLock()
	if h.providersCache != nil && time.Now().Before(h.providersExpiry) {
		cached := h.providersCache
		h.cacheMu.RUnlock()
		return cached
	}
	h.cacheMu.RUnlock()

	providerResp, err := h.apiClient.GetModelProviders(ctx, connect.NewRequest(&devinproto.GetModelProvidersRequest{}))
	if err != nil {
		return nil
	}
	var providers []map[string]any
	for _, p := range providerResp.Msg.GetModelProviders() {
		providers = append(providers, map[string]any{
			"provider":     shortEnum(p.GetProvider().String(), "MODEL_PROVIDER_"),
			"display_name": p.GetDisplayName(),
		})
	}

	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if h.providersCache != nil && time.Now().Before(h.providersExpiry) {
		return h.providersCache
	}
	h.providersCache = providers
	h.providersExpiry = time.Now().Add(h.cacheTTL)
	return providers
}

func (h *Handler) cachedModelStatuses(ctx context.Context) []map[string]any {
	h.cacheMu.RLock()
	if h.modelStatusesCache != nil && time.Now().Before(h.modelStatusesExpiry) {
		cached := h.modelStatusesCache
		h.cacheMu.RUnlock()
		return cached
	}
	h.cacheMu.RUnlock()

	modelStatusResp, err := h.apiClient.GetModelStatuses(ctx, connect.NewRequest(&devinproto.GetModelStatusesRequest{
		Metadata: buildMetadata(h.token),
	}))
	if err != nil {
		return nil
	}
	var statuses []map[string]any
	for _, s := range modelStatusResp.Msg.GetModelStatusInfos() {
		statuses = append(statuses, map[string]any{
			"model":  shortEnum(s.GetModel().String(), "MODEL_"),
			"status": shortEnum(s.GetStatus().String(), "MODEL_STATUS_"),
		})
	}

	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if h.modelStatusesCache != nil && time.Now().Before(h.modelStatusesExpiry) {
		return h.modelStatusesCache
	}
	h.modelStatusesCache = statuses
	h.modelStatusesExpiry = time.Now().Add(h.cacheTTL)
	return statuses
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	// ApiServer Connect 客户端沿用 Basic token-token；Seat 单独走 Bearer。
	if clone.Header.Get("Authorization") == "" {
		clone.Header.Set("Authorization", "Basic "+t.token+"-"+t.token)
	}
	return t.base.RoundTrip(clone)
}

func randomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func generateSessionID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shortEnum(full, prefix string) string {
	if full == "" {
		return ""
	}
	for _, p := range []string{
		"ExaCodeiumCommonPb_ModelProvider_MODEL_PROVIDER_",
		"ExaCodeiumCommonPb_APIProvider_API_PROVIDER_",
		"ExaCodeiumCommonPb_ModelPricingType_MODEL_PRICING_TYPE_",
		"ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_",
		"ExaCodeiumCommonPb_ModelDimensionKind_MODEL_DIMENSION_KIND_",
		"ExaCodeiumCommonPb_StatusLevel_STATUS_LEVEL_",
		"ExaCodeiumCommonPb_ModelStatus_MODEL_STATUS_",
		"ExaCodeiumCommonPb_TeamsTier_TEAMS_TIER_",
		"ExaCodeiumCommonPb_BillingStrategy_BILLING_STRATEGY_",
		"ExaCodeiumCommonPb_Model_",
		"MODEL_PROVIDER_", "API_PROVIDER_", "MODEL_PRICING_TYPE_", "MODEL_COST_TIER_",
		"MODEL_DIMENSION_KIND_", "STATUS_LEVEL_", "MODEL_STATUS_", "TEAMS_TIER_", "BILLING_STRATEGY_",
		"MODEL_",
		prefix,
	} {
		if p == "" {
			continue
		}
		if idx := strings.Index(full, p); idx >= 0 {
			return full[idx+len(p):]
		}
	}
	if i := strings.LastIndex(full, "_"); i >= 0 && i+1 < len(full) {
		return full[i+1:]
	}
	return full
}

func strAny(vals ...any) string {
	for _, v := range vals {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case json.Number:
			return t.String()
		case float64:
			return fmt.Sprintf("%.0f", t)
		default:
			s := fmt.Sprint(t)
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func boolAny(vals ...any) bool {
	for _, v := range vals {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t
		case string:
			return t == "true" || t == "1"
		}
	}
	return false
}

func numAny(vals ...any) any {
	for _, v := range vals {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case float64, float32, int, int32, int64, json.Number:
			return t
		case string:
			if t != "" {
				return t
			}
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
