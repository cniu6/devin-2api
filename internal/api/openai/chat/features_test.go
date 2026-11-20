package chat

import (
	"strings"
	"testing"

	"github.com/leookun/devin-2api/internal/llm"
)

func TestDecodeRequestCacheControlFormatAndTier(t *testing.T) {
	adapted, err := DecodeRequest([]byte(`{"model":"m","service_tier":"flex",
		"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}},
		"messages":[
			{"role":"system","content":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"q","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":"plain"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := adapted.Context
	if !ctx.SystemCacheControl || !ctx.Messages[0].(llm.UserMessage).CacheControl || ctx.Messages[1].(llm.UserMessage).CacheControl {
		t.Fatal("cache_control mapping wrong")
	}
	if ctx.ServiceTier != "flex" {
		t.Fatalf("service tier = %q", ctx.ServiceTier)
	}
	if !strings.HasPrefix(ctx.SystemPrompt, "sys\n\n") || !strings.Contains(ctx.SystemPrompt, `{"type":"object"}`) {
		t.Fatalf("response_format instruction missing: %q", ctx.SystemPrompt)
	}
}

func TestFinishReasonSeparatesContentFilterFromError(t *testing.T) {
	if finishReason(llm.StopReasonContentFilter) != "content_filter" || finishReason(llm.StopReasonError) != nil {
		t.Fatal("content_filter must only be reported for real filter stops")
	}
}
