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
	"fmt"
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
	// HTTP 错误体是 {"error":{...}}；WebSocket 契约要求带 "type":"error"，客户端才能识别。
	var envelope map[string]json.RawMessage
	if json.Unmarshal(rest, &envelope) == nil {
		if _, typed := envelope["type"]; !typed {
			if _, isError := envelope["error"]; isError {
				envelope["type"] = json.RawMessage(`"error"`)
				if wrapped, err := json.Marshal(envelope); err == nil {
					rest = wrapped
				}
			}
		}
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

// wsIdleTimeout 是 WebSocket 连接两轮请求之间允许的最长空闲时间。
const wsIdleTimeout = 10 * time.Minute

// responsesWebSocket 处理 OpenAI Responses WebSocket 连接。
// 一条连接承载多轮 response.create（Codex 会复用连接并以 previous_response_id 串联），
// 每轮按顺序处理；连接断开会取消正在进行的上游流。
func (application *App) responsesWebSocket(writer http.ResponseWriter, request *http.Request) {
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	connCtx, cancelConn := context.WithCancel(request.Context())
	defer cancelConn()

	// 单一读协程：gorilla 只允许一个并发读者。读失败（断开/超时）即取消整条连接。
	incoming := make(chan []byte, 8)
	go func() {
		defer close(incoming)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
			messageType, body, err := conn.ReadMessage()
			if err != nil {
				cancelConn()
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}
			select {
			case incoming <- body:
			case <-connCtx.Done():
				return
			}
		}
	}()

	for {
		var body []byte
		select {
		case <-connCtx.Done():
			return
		case message, ok := <-incoming:
			if !ok {
				return
			}
			body = message
		}
		payload, skip, err := wsRequestPayload(body)
		if skip {
			continue
		}
		if err != nil {
			if writeErr := writeWSError(conn, "invalid_request_error", err.Error()); writeErr != nil {
				return
			}
			continue
		}
		if !application.serveWSTurn(connCtx, conn, request, payload) {
			return
		}
	}
}

// wsRequestPayload 解包一条客户端消息为 /v1/responses 请求体。
// 非 response.create 的控制消息（无 response 包装、无 model）返回 skip。
func wsRequestPayload(body []byte) ([]byte, bool, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("invalid JSON message: %w", err)
	}
	// OpenAI 包装格式 {"type":"response.create","response":{...}}：
	// 真正的请求字段在 response 子对象里；不解包会因缺 model 解码失败。
	if inner, ok := payload["response"].(map[string]any); ok && payload["type"] != nil {
		payload = inner
	} else if kind, _ := payload["type"].(string); kind != "" && kind != "response.create" {
		if _, hasModel := payload["model"]; !hasModel {
			return nil, true, nil
		}
	}
	delete(payload, "type")
	// WebSocket 模式不写 stream 字段，等价于 stream=true。
	payload["stream"] = true
	encoded, err := json.Marshal(payload)
	return encoded, false, err
}

// serveWSTurn 处理一轮请求；返回 false 表示连接已不可用。
func (application *App) serveWSTurn(connCtx context.Context, conn *websocket.Conn, outer *http.Request, body []byte) bool {
	ctx, cancel := context.WithCancel(connCtx)
	defer cancel()
	innerRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if err != nil {
		return writeWSError(conn, "server_error", err.Error()) == nil
	}
	for key, values := range outer.Header {
		switch http.CanonicalHeaderKey(key) {
		case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol":
			continue
		}
		innerRequest.Header[key] = values
	}
	innerRequest.Header.Set("Content-Type", "application/json")
	innerRequest.RemoteAddr = outer.RemoteAddr

	_ = conn.SetWriteDeadline(time.Now().Add(wsIdleTimeout))
	wsWriter := newWSResponseWriter(conn)
	application.createResponses(wsWriter, innerRequest)
	// 冲刷非 SSE 残余：错误 JSON 没有空行分隔，不冲就到不了客户端。
	wsWriter.flushRemainder()
	return wsWriter.err == nil && connCtx.Err() == nil
}

// writeWSError 以 OpenAI WebSocket 契约的 {"type":"error"} 帧报告错误。
func writeWSError(conn *websocket.Conn, kind, message string) error {
	frame, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
	return conn.WriteMessage(websocket.TextMessage, frame)
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
