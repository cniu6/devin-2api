// 本文件验证缓存断点、Gemini 签名回放、预填充、服务等级、默认模型别名等上游字段映射。
package devin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	devinproto "local/devinproto"

	"github.com/leookun/devin-2api/internal/llm"
	"google.golang.org/protobuf/proto"
)

func TestBuildRequestMapsCacheSignaturePrefillAndTier(t *testing.T) {
	signature := base64.StdEncoding.EncodeToString([]byte("sig-bytes"))
	request := llm.RequestMessages{
		Model: "gemini", SystemPrompt: "sys", SystemCacheControl: true, ServiceTier: "priority",
		Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "q"}}, CacheControl: true},
			llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "c1", Name: "run", Arguments: json.RawMessage(`{}`), ThoughtSignature: signature}}},
			llm.ToolResultMessage{ToolCallID: "c1", ToolName: "run", Content: []llm.Content{llm.TextContent{Text: "ok"}}},
			llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "Partial"}}},
		},
	}
	built, err := buildRequest(request, Config{Token: "t", Model: "gemini"})
	if err != nil {
		t.Fatal(err)
	}
	ephemeral := devinproto.ExaChatPb_CacheControlType_ExaChatPb_CacheControlType_CACHE_CONTROL_TYPE_EPHEMERAL
	if built.GetSystemPromptCacheOptions().GetType() != ephemeral {
		t.Fatal("system cache_control not forwarded")
	}
	prompts := built.GetChatMessagePrompts()
	if prompts[0].GetPromptCacheOptions().GetType() != ephemeral || prompts[2].GetPromptCacheOptions() != nil {
		t.Fatal("message cache_control must be forwarded only where marked")
	}
	if string(prompts[1].GetGeminiThoughtSignature()) != "sig-bytes" {
		t.Fatalf("gemini signature not replayed: %q", prompts[1].GetGeminiThoughtSignature())
	}
	cfg := built.GetConfiguration()
	if !cfg.GetLastMessageIsPartial() || cfg.GetServiceTier() != "priority" || cfg.GetMaxNewlines() != DefaultMaxNewlines {
		t.Fatalf("configuration = %+v", cfg)
	}

	plain, _ := buildRequest(llm.RequestMessages{Model: "m", Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "q"}}}}}, Config{Token: "t", Model: "m", MaxNewlines: 5000})
	if plain.GetSystemPromptCacheOptions() != nil || plain.GetConfiguration().LastMessageIsPartial != nil ||
		plain.GetConfiguration().ServiceTier != nil || plain.GetConfiguration().GetMaxNewlines() != 5000 {
		t.Fatalf("omitted fields must keep defaults: %+v", plain.GetConfiguration())
	}
}

func TestDecoderAttachesGeminiSignatureAndRefusal(t *testing.T) {
	decoder := newResponseDecoder("gemini")
	decoder.start()
	decoder.decodeTool(&devinproto.ExaCodeiumCommonPb_ChatToolCall{Id: proto.String("c1"), Name: proto.String("run"), ArgumentsJson: proto.String(`{}`)})
	decoder.updateMetadata(&devinproto.GetChatMessageResponse{
		GeminiThoughtSignature: []byte("sig"),
		CreditCost:             proto.Int32(7),
		Usage:                  &devinproto.ExaCodeiumCommonPb_ModelUsageStats{ProviderRefusal: proto.Bool(true)},
	})
	events := decoder.complete(llm.StopReasonStop)
	done := events[len(events)-1]
	if done.Reason != llm.StopReasonContentFilter {
		t.Fatalf("provider refusal must surface as content filter, got %q", done.Reason)
	}
	call := done.Message.Content[0].(llm.ToolCall)
	if call.ThoughtSignature != base64.StdEncoding.EncodeToString([]byte("sig")) {
		t.Fatalf("signature = %q", call.ThoughtSignature)
	}
	if done.Message.Usage.CreditCost != 7 {
		t.Fatalf("credit cost = %d", done.Message.Usage.CreditCost)
	}
}

func TestResolveModelDefaultAlias(t *testing.T) {
	a := &Adapter{config: Config{Model: "configured"}}
	if got := a.resolveModel(context.Background(), ""); got != "configured" {
		t.Fatalf("empty model = %q", got)
	}
	if got := a.resolveModel(context.Background(), "claude-x"); got != "claude-x" {
		t.Fatalf("explicit model = %q", got)
	}
	a.modelMeta = map[string]modelMeta{}
	a.defaultModel = "upstream-default"
	a.modelsExpiry = farFuture
	if got := a.resolveModel(context.Background(), "default"); got != "upstream-default" {
		t.Fatalf("default alias = %q", got)
	}
}

func TestImageValidationTrustsCatalog(t *testing.T) {
	request := llm.RequestMessages{Model: "glm-5", Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.ImageContent{Data: "AAAA", MIMEType: "image/png"}}}}}
	a := &Adapter{modelMeta: map[string]modelMeta{
		"glm-5":   {supportsImages: true, hasImageInfo: true},
		"swe-1-7": {supportsImages: false, hasImageInfo: true},
	}}
	if err := a.validateImagesForModel(request, "glm-5"); err != nil {
		t.Fatalf("catalog says glm-5 supports images: %v", err)
	}
	if err := a.validateImagesForModel(request, "swe-1-7"); err == nil {
		t.Fatal("catalog says swe-1-7 has no vision")
	}
}

var farFuture = time.Now().Add(24 * time.Hour)
