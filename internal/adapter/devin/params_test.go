// 本文件验证生成参数、tool_choice 与 stop_reason 的映射。
package devin

import (
	"testing"

	devinproto "local/devinproto"

	"github.com/leookun/devin-2api/internal/llm"
)

func TestBuildRequestForwardsGenerationParamsAndToolChoice(t *testing.T) {
	maxTok := 512
	temp := 0.2
	topP := 0.8
	topK := 20
	seed := int64(42)
	req := llm.RequestMessages{
		Model: "swe-2-high",
		Messages: []llm.Message{llm.UserMessage{
			Content: []llm.Content{llm.TextContent{Text: "hi"}},
		}},
		MaxTokens:                &maxTok,
		Temperature:              &temp,
		TopP:                     &topP,
		TopK:                     &topK,
		StopPatterns:             []string{"END"},
		Seed:                     &seed,
		DisableParallelToolCalls: true,
		ToolChoice:               &llm.ToolChoice{Mode: llm.ToolChoiceNamed, ToolName: "run"},
	}

	built, err := buildRequest(req, Config{Token: "t", Model: "swe-2-high"})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	cfg := built.GetConfiguration()
	if cfg.GetMaxTokens() != 512 || cfg.GetTemperature() != 0.2 || cfg.GetTopP() != 0.8 ||
		cfg.GetTopK() != 20 || cfg.GetSeed() != 42 {
		t.Fatalf("generation config not forwarded: %+v", cfg)
	}
	if len(cfg.GetStopPatterns()) != 1 || cfg.GetStopPatterns()[0] != "END" {
		t.Fatalf("stop patterns = %v", cfg.GetStopPatterns())
	}
	if !built.GetDisableParallelToolCalls() {
		t.Fatal("disable_parallel_tool_calls not forwarded")
	}
	if built.GetToolChoice().GetToolName() != "run" {
		t.Fatalf("tool_choice = %+v", built.GetToolChoice())
	}
}

func TestMapStopReasonMaxNewlinesAndContentFilter(t *testing.T) {
	if got := mapStopReason(devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_MAX_NEWLINES); got != llm.StopReasonLength {
		t.Fatalf("MAX_NEWLINES = %q, want length", got)
	}
	if got := mapStopReason(devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_CONTENT_FILTER); got != llm.StopReasonError {
		t.Fatalf("CONTENT_FILTER = %q, want error", got)
	}
}
