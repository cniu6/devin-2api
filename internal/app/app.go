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
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/leookun/devin-2api/internal/adapter"
	"github.com/leookun/devin-2api/internal/api/common"
	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/debuglog"
	"github.com/leookun/devin-2api/internal/llm"
	"github.com/leookun/devin-2api/internal/responsestore"
	respproto "github.com/leookun/devin-2api/internal/api/openai/responses"
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
		protected.Post("/v1/chat/completions", application.createChatCompletions)
		protected.Post("/v1/messages", application.createMessages)
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
		// 非 OpenAI 标准字段，供面板/客户端识别是否可传图。
		entry["supports_images"] = m.SupportsImages
		data = append(data, entry)
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"object": "list", "data": data})
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
			created := m.Created
			if created == 0 {
				created = time.Now().Unix()
			}
			ownedBy := m.OwnedBy
			if ownedBy == "" {
				ownedBy = "devin"
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": m.ID, "object": "model", "created": created, "owned_by": ownedBy,
				"supports_images": m.SupportsImages,
			})
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
	application.createCompletion(writer, request, decodeResponsesRequest, responsesProtocol{})
}

func (application *App) createChatCompletions(writer http.ResponseWriter, request *http.Request) {
	application.createCompletion(writer, request, decodeChatRequest, chatProtocol{})
}

func (application *App) createMessages(writer http.ResponseWriter, request *http.Request) {
	application.createCompletion(writer, request, decodeAnthropicRequest, anthropicProtocol{})
}

func (application *App) createCompletion(
	writer http.ResponseWriter,
	request *http.Request,
	decoder decodeRequestFunc,
	protocol protocolEncoder,
) {
	recorder := application.debugManager.Start(debuglog.RequestMeta{Method: request.Method, Path: request.URL.Path})
	completion := debuglog.Completion{StatusCode: http.StatusInternalServerError, Result: "failed"}
	defer func() { recorder.Complete(completion) }()

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
		messages = merged
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
		application.rememberStreamExchange(messages, message, encoder)
		return
	}
	message, err := collectFinalMessage(ctx, stream, recorder)
	if err != nil {
		completion.StatusCode = mapProviderErrorStatus(err)
		writeLoggedError(writer, recorder, "response_event", completion.StatusCode, err)
		return
	}
	updateCompletionIdentity(&completion, message)
	body, err = protocol.EncodeFinal(message)
	if err != nil {
		completion.StatusCode = http.StatusInternalServerError
		writeLoggedError(writer, recorder, "http_encode", completion.StatusCode, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write(body); err != nil {
		completion.Result = "disconnected"
		recorder.WriteError("client_disconnected", err)
		return
	}
	recorder.AppendJSONL("06-http-response.jsonl", "response", json.RawMessage(body))
	completion.StatusCode = http.StatusOK
	completion.Result = "completed"
	application.rememberResponsesExchange(messages, message, body)
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
	application.responses.PutWithID(caller, projection.ID, history)
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
	encoder := protocol.NewStreamEncoder(model, options.IncludeUsage)
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
	application.responses.PutWithID(caller, responseID, history)
}
