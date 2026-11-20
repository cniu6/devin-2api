package messages

import (
	"testing"

	"github.com/leookun/devin-2api/internal/llm"
)

// 思考块的签名与 cache_control 断点必须在历史回放时保留。
func TestDecodeRequestKeepsThinkingSignatureAndCacheControl(t *testing.T) {
	adapted, err := DecodeRequest([]byte(`{"model":"claude","max_tokens":64,
		"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"q","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"plan","signature":"sig1"},
				{"type":"redacted_thinking","data":"enc"},
				{"type":"tool_use","id":"t1","name":"run","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok","cache_control":{"type":"ephemeral"}}]}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := adapted.Context
	if !ctx.SystemCacheControl {
		t.Fatal("system cache_control lost")
	}
	if user := ctx.Messages[0].(llm.UserMessage); !user.CacheControl {
		t.Fatal("user cache_control lost")
	}
	assistant := ctx.Messages[1].(llm.AssistantMessage)
	thinking := assistant.Content[0].(llm.ThinkingContent)
	if thinking.Thinking != "plan" || thinking.ThinkingSignature != "sig1" {
		t.Fatalf("thinking = %+v", thinking)
	}
	if redacted := assistant.Content[1].(llm.ThinkingContent); !redacted.Redacted || redacted.ThinkingSignature != "enc" {
		t.Fatalf("redacted thinking = %+v", redacted)
	}
	if result := ctx.Messages[2].(llm.ToolResultMessage); !result.CacheControl {
		t.Fatal("tool_result cache_control lost")
	}
}

func TestAnthropicStopReasonsAreValidEnums(t *testing.T) {
	for reason, want := range map[llm.StopReason]string{
		llm.StopReasonContentFilter: "refusal", llm.StopReasonError: "refusal", llm.StopReasonLength: "max_tokens",
	} {
		if got := anthropicStopReason(reason); got != want {
			t.Fatalf("%s -> %v, want %s", reason, got, want)
		}
	}
}
