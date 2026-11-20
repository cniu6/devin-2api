package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/llm"
)

func toolFinal() *llm.AssistantMessage {
	return &llm.AssistantMessage{
		ResponseModel: "gpt-test", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{llm.ToolCall{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"ls"}`)}},
	}
}

func postResponses(t *testing.T, application *App, body string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	application.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

// GET 回放与 DELETE 删除已保存的响应；store:false 的响应查不到。
func TestResponsesGetAndDelete(t *testing.T) {
	application := newResponsesTestApp(okFinal())
	code, created := postResponses(t, application, `{"model":"gpt-test","input":"hi"}`)
	if code != http.StatusOK {
		t.Fatalf("create status %d", code)
	}
	id := created["id"].(string)

	get := httptest.NewRecorder()
	application.Router().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/responses/"+id, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), id) {
		t.Fatalf("GET = %d %s", get.Code, get.Body.String())
	}

	del := httptest.NewRecorder()
	application.Router().ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/v1/responses/"+id, nil))
	if del.Code != http.StatusOK || !strings.Contains(del.Body.String(), `"deleted":true`) {
		t.Fatalf("DELETE = %d %s", del.Code, del.Body.String())
	}
	again := httptest.NewRecorder()
	application.Router().ServeHTTP(again, httptest.NewRequest(http.MethodGet, "/v1/responses/"+id, nil))
	if again.Code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d", again.Code)
	}

	_, unstored := postResponses(t, application, `{"model":"gpt-test","input":"hi","store":false}`)
	miss := httptest.NewRecorder()
	application.Router().ServeHTTP(miss, httptest.NewRequest(http.MethodGet, "/v1/responses/"+unstored["id"].(string), nil))
	if miss.Code != http.StatusNotFound {
		t.Fatalf("store:false response must not be retrievable, got %d", miss.Code)
	}
}

// Codex 链式工具调用：只发 function_call_output + previous_response_id，
// 服务端必须补齐历史并回填工具名，而不是 400 "unknown call_id"。
func TestResponsesChainedToolOutputResolvesHistory(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonToolUse, Message: toolFinal()}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	_, first := postResponses(t, application, `{"model":"gpt-test","input":"list files"}`)

	fake.events = []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: okFinal()}}
	code, body := postResponses(t, application, `{"model":"gpt-test","previous_response_id":"`+first["id"].(string)+`","input":[`+
		`{"type":"function_call_output","call_id":"call_1","output":"a.txt"},`+
		`{"type":"message","role":"user","content":"thanks"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	messages := fake.lastRequest.Messages
	if len(messages) != 4 {
		t.Fatalf("want history(2)+tool result+user = 4 messages, got %d", len(messages))
	}
	result, ok := messages[2].(llm.ToolResultMessage)
	if !ok || result.ToolName != "shell" {
		t.Fatalf("tool result name not resolved: %#v", messages[2])
	}
}

// item_reference 展开为此前保存的 output item。
func TestResponsesItemReferenceExpands(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonToolUse, Message: toolFinal()}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	_, first := postResponses(t, application, `{"model":"gpt-test","input":"list files"}`)
	itemID := first["output"].([]any)[0].(map[string]any)["id"].(string)

	fake.events = []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: okFinal()}}
	code, body := postResponses(t, application, `{"model":"gpt-test","input":[`+
		`{"type":"message","role":"user","content":"list files"},`+
		`{"type":"item_reference","id":"`+itemID+`"},`+
		`{"type":"function_call_output","call_id":"call_1","output":"a.txt"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	assistant, ok := fake.lastRequest.Messages[1].(llm.AssistantMessage)
	if !ok || len(assistant.Content) != 1 {
		t.Fatalf("item_reference not expanded: %#v", fake.lastRequest.Messages)
	}
	code, _ = postResponses(t, application, `{"model":"gpt-test","input":[{"type":"item_reference","id":"fc_missing"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown item_reference should be 400, got %d", code)
	}
}

// 一条 WebSocket 连接承载多轮 response.create。
func TestResponsesWebSocketMultiTurn(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: okFinal()}}}
	server := httptest.NewServer(New(fake, config.ServerConfig{Listen: ":0"}, nil).Router())
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for turn := 0; turn < 2; turn++ {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","response":{"model":"gpt-test","input":"hi"}}`)); err != nil {
			t.Fatal(err)
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("turn %d: %v", turn, err)
			}
			if strings.Contains(string(data), `"type":"response.completed"`) {
				break
			}
		}
	}
	// 错误帧带 type:error 且连接保持可用。
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","response":{"input":"no model"}}`))
	_, data, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(data), `"type":"error"`) {
		t.Fatalf("error frame = %s, %v", data, err)
	}
}
