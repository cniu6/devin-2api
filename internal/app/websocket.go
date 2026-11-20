// 本文件为 Codex 等使用 OpenAI Responses WebSocket transport 的客户端提供 WebSocket 接入。
//
// OpenAI Responses WebSocket 模式把 HTTP/SSE 的事件流映射为 WebSocket 文本帧：
// 客户端发送 {"type":"response.create", ...} JSON，服务端把每个 SSE 事件的 data JSON
// 作为一条 WebSocket 文本消息发回，直到 response.completed 或 error。
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// 允许跨源（Codex 本地调用可能使用不同 origin）。
	CheckOrigin: func(r *http.Request) bool { return true },
	// Codex/OpenAI Responses WebSocket 协商此子协议。
	Subprotocols: []string{"responses_websockets=2026-02-06"},
	// 给足读写缓冲，避免高频事件时频繁系统调用。
	ReadBufferSize:  8192,
	WriteBufferSize: 8192,
}

// wsResponseWriter 把 HTTP SSE 响应解析为单个 WebSocket 文本帧。
// writeProtocolStream 写入的每段 SSE 帧被暂存在 buf 中，遇到 "\n\n" 分隔时拆成完整事件，
// 只把 data 行作为 JSON 文本帧发回客户端（与 OpenAI Responses WebSocket 协议一致）。
type wsResponseWriter struct {
	conn       *websocket.Conn
	header     http.Header
	statusCode int
	buf        []byte
	err        error
}

func newWSResponseWriter(conn *websocket.Conn) *wsResponseWriter {
	return &wsResponseWriter{
		conn:   conn,
		header: make(http.Header),
	}
}

func (w *wsResponseWriter) Header() http.Header        { return w.header }
func (w *wsResponseWriter) WriteHeader(statusCode int) { w.statusCode = statusCode }

func (w *wsResponseWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.Index(w.buf, []byte("\n\n"))
		if idx < 0 {
			break
		}
		frame := w.buf[:idx]
		w.buf = w.buf[idx+2:]
		if err := w.writeFrame(frame); err != nil {
			w.err = err
			return 0, err
		}
	}
	return len(p), nil
}

func (w *wsResponseWriter) Flush() {
	// writeProtocolStream 在每次写入 SSE 后都会 Flush；
	// 但实际消息在 Write 遇到 "\n\n" 时已经发送，这里不需要额外动作。
}

// flushRemainder 把缓冲区里不足一个 SSE 帧的剩余内容作为文本帧发出。
// 非流式错误路径（writeJSONError/writeLoggedError）只写单行 JSON+"\n"，
// 不含 "\n\n" 分隔——不冲刷的话错误永远不会到达客户端，连接只能静默关闭。
func (w *wsResponseWriter) flushRemainder() {
	if w.err != nil {
		return
	}
	rest := bytes.TrimSpace(w.buf)
	w.buf = nil
	if len(rest) == 0 {
		return
	}
	if err := w.conn.WriteMessage(websocket.TextMessage, rest); err != nil {
		w.err = err
	}
}

// writeFrame 解析单条 SSE 帧，把 data 行作为 JSON 文本消息发出。
// 如果遇到 event: error 事件，同样只把 data 发回，让客户端按 OpenAI 协议处理。
func (w *wsResponseWriter) writeFrame(frame []byte) error {
	var data []byte
	lines := bytes.Split(frame, []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			data = line[len("data: "):]
		}
	}
	if len(data) == 0 {
		return nil
	}
	return w.conn.WriteMessage(websocket.TextMessage, data)
}

// responsesWebSocket 处理 OpenAI Responses WebSocket 连接。
// 只处理单轮 response.create；多轮 response.append 可在此基础上扩展。
func (application *App) responsesWebSocket(writer http.ResponseWriter, request *http.Request) {
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// 设置 WebSocket 读/写超时，避免死连接长期占用。
	_ = conn.SetReadDeadline(time.Now().Add(600 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(600 * time.Second))

	// 读取第一条 response.create 消息。
	messageType, body, err := conn.ReadMessage()
	if err != nil {
		log.Printf("websocket read failed: %v", err)
		return
	}
	if messageType != websocket.TextMessage {
		log.Printf("websocket received non-text message type %d", messageType)
		return
	}

	// OpenAI WebSocket 模式不写 stream 字段，等价于 stream=true。
	// 把 body 解析后强制加上 stream=true，确保走流式分支。
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("websocket parse request failed: %v", err)
		return
	}
	// OpenAI 包装格式 {"type":"response.create"|"response.append","response":{...}}：
	// 真正的请求字段在 response 子对象里；不解包会因缺 model 解码失败（且错误静默）。
	if inner, ok := payload["response"].(map[string]any); ok && payload["type"] != nil {
		payload = inner
	}
	payload["stream"] = true
	body, err = json.Marshal(payload)
	if err != nil {
		log.Printf("websocket encode request failed: %v", err)
		return
	}

	// 构造一个内部 POST /v1/responses 请求，让现有 createResponses 处理。
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()

	// 监听连接关闭，及时取消上游流。
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
		}
	}()

	innerRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if err != nil {
		log.Printf("websocket create inner request failed: %v", err)
		return
	}
	innerRequest.Header.Set("Content-Type", "application/json")
	innerRequest.RemoteAddr = request.RemoteAddr

	wsWriter := newWSResponseWriter(conn)
	application.createResponses(wsWriter, innerRequest)
	// 冲刷非 SSE 残余（错误 JSON 没有 "\n\n" 分隔，不冲就到不了客户端）。
	wsWriter.flushRemainder()

	// 如果 createResponses 没有发送 completed/error，尝试补一个干净的关闭。
	if wsWriter.err == nil {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}
}

// createResponsesWebSocket 是 /v1/responses 的 WebSocket upgrade 入口。
// 它只在请求包含 WebSocket upgrade 头时升级连接；否则应由 chi POST 路由处理。
func (application *App) createResponsesWebSocket(writer http.ResponseWriter, request *http.Request) {
	if !websocket.IsWebSocketUpgrade(request) {
		// 非 WebSocket 请求应走正常 POST 路由；这里返回 405，提示需要 POST。
		writer.Header().Set("Allow", "POST")
		http.Error(writer, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	application.responsesWebSocket(writer, request)
}
