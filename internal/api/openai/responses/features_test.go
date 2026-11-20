package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leookun/devin-2api/internal/llm"
)

// reasoning + function_call 合并为同一助手轮，encrypted_content 回放为思考签名。
func TestDecodeReasoningItemsMergeIntoAssistantTurn(t *testing.T) {
	adapted, err := DecodeRequest([]byte(`{"model":"m","text":{"format":{"type":"json_object"}},"input":[
		{"type":"message","role":"user","content":"q"},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"sig"},
		{"type":"function_call","call_id":"c1","name":"run","arguments":"{}"},
		{"type":"function_call","call_id":"c2","name":"run","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"a"},
		{"type":"function_call_output","call_id":"c2","output":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := adapted.Context.Messages
	if len(messages) != 4 {
		t.Fatalf("want user + 1 merged assistant + 2 results, got %d", len(messages))
	}
	assistant := messages[1].(llm.AssistantMessage)
	if len(assistant.Content) != 3 || assistant.StopReason != llm.StopReasonToolUse {
		t.Fatalf("assistant = %+v", assistant)
	}
	if thinking := assistant.Content[0].(llm.ThinkingContent); thinking.Thinking != "think" || thinking.ThinkingSignature != "sig" {
		t.Fatalf("thinking = %+v", thinking)
	}
	if !strings.Contains(adapted.Context.SystemPrompt, "JSON object") {
		t.Fatal("text.format instruction missing")
	}
}

func TestDanglingToolOutputOnlyAllowedWithPreviousResponse(t *testing.T) {
	body := `{"model":"m","input":[{"type":"function_call_output","call_id":"c9","output":"x"}]}`
	if _, err := DecodeRequest([]byte(body)); err == nil {
		t.Fatal("dangling call_id without previous_response_id must fail")
	}
	adapted, err := DecodeRequest([]byte(strings.Replace(body, `"model":"m"`, `"model":"m","previous_response_id":"resp_1"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	history := []llm.Message{llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "c9", Name: "grep", Arguments: json.RawMessage(`{}`)}}}}
	merged, resolution, _ := WithHistory(adapted.Context, "resp_1", "c", func(string, string) ([]llm.Message, bool) { return history, true })
	if !resolution.Injected {
		t.Fatal("tool-output-only turn must receive history")
	}
	if name := ResolvePendingToolNames(merged).Messages[1].(llm.ToolResultMessage).ToolName; name != "grep" {
		t.Fatalf("tool name = %q", name)
	}
}
