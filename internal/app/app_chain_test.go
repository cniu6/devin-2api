package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/llm"
)

func newResponsesTestApp(final *llm.AssistantMessage) *App {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final,
	}}}
	return New(fake, config.ServerConfig{Listen: ":0"}, nil)
}

func okFinal() *llm.AssistantMessage {
	return &llm.AssistantMessage{
		ResponseID: "resp-internal", ResponseModel: "gpt-test",
		Content: []llm.Content{llm.TextContent{Text: "hi"}}, StopReason: llm.StopReasonStop,
	}
}

// 兼容性红线：带一个未知/过期的 previous_response_id 不得打断请求。
// README 保证 "previous_response_id is accepted but currently does not enable
// server-side state retention"，改造前这类请求是能正常返回的。
func TestResponsesUnknownPreviousIDStillSucceeds(t *testing.T) {
	application := newResponsesTestApp(okFinal())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-test","previous_response_id":"resp_does_not_exist","input":"second"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("an unknown previous_response_id must not break a working request, got %d: %s",
			response.Code, response.Body.String())
	}
}

// 客户端自带完整历史的请求必须完全不受影响。
func TestResponsesClientSuppliedHistoryUnchanged(t *testing.T) {
	application := newResponsesTestApp(okFinal())
	body := `{"model":"gpt-test","previous_response_id":"resp_x","input":[` +
		`{"type":"message","role":"user","content":"one"},` +
		`{"type":"message","role":"assistant","content":"answer"},` +
		`{"type":"message","role":"user","content":"two"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

// 无 id 的普通请求与改造前一致。
func TestResponsesWithoutPreviousIDUnchanged(t *testing.T) {
	application := newResponsesTestApp(okFinal())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

// 服务端确实存了会话：一轮成功请求之后 store 里应有一条记录。
func TestResponsesStoresSessionAfterSuccess(t *testing.T) {
	application := newResponsesTestApp(okFinal())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-test","input":"hello"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if application.responses.Len() == 0 {
		t.Fatal("a successful response must be remembered for the next chained turn")
	}
}
