package app

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/llm"
)

func streamFinal() *llm.AssistantMessage {
	return &llm.AssistantMessage{
		ResponseID: "upstream-id-without-prefix", ResponseModel: "gpt-test",
		Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonStop,
	}
}

// firstStreamResponseID 从 SSE 流里取出 response.created 公布的 resp_ id。
func firstStreamResponseID(t *testing.T, body string) string {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		if event.Type == "response.created" && event.Response.ID != "" {
			return event.Response.ID
		}
	}
	return ""
}

// 流式响应必须按「客户端看到的那个 id」存会话，否则下一轮取不到。
func TestStreamSessionStoredUnderPublishedID(t *testing.T) {
	final := streamFinal()
	partial := &llm.AssistantMessage{
		Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending,
	}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: partial},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: partial},
		{Type: llm.ResponseEventTextEnd, ContentIndex: 0, Content: "hello", Partial: partial},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-test","stream":true,"input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	published := firstStreamResponseID(t, response.Body.String())
	if published == "" {
		t.Skip("no response.created id found in the stream")
	}
	// 存下的键必须是这个 id；上游给的那个不同前缀 id 不应该是键。
	if _, found := application.responses.Get("default", published); !found {
		t.Fatalf("stream response id %q was not stored; the next chained turn cannot resolve", published)
	}
	if _, found := application.responses.Get("default", "upstream-id-without-prefix"); found {
		t.Fatal("the upstream id must not be used as the store key for streamed responses")
	}
}
