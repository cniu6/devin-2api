// 本文件验证 Devin 请求字段映射和响应增量聚合。
package devin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	devinproto "local/devinproto"

	"connectrpc.com/connect"
	"github.com/leookun/devin-2api/internal/debuglog"
	"github.com/leookun/devin-2api/internal/llm"
	"google.golang.org/protobuf/proto"
)

// fakeDevinResponseReceiver 为 responseStream 测试提供确定顺序的 protobuf 帧。
type fakeDevinResponseReceiver struct {
	// responses 是等待消费的响应帧。
	responses []*devinproto.GetChatMessageResponse
	// index 是下一次 Receive 尝试读取的位置。
	index int
	// current 是最近一次成功读取的响应帧。
	current *devinproto.GetChatMessageResponse
}

// Receive 前进到下一帧。
func (receiver *fakeDevinResponseReceiver) Receive() bool {
	if receiver.index >= len(receiver.responses) {
		return false
	}
	receiver.current = receiver.responses[receiver.index]
	receiver.index++
	return true
}

// Msg 返回最近一次成功读取的帧。
func (receiver *fakeDevinResponseReceiver) Msg() *devinproto.GetChatMessageResponse {
	return receiver.current
}

// Err 模拟正常 EOF。
func (receiver *fakeDevinResponseReceiver) Err() error { return nil }

func TestBuildRequestMapsLoopMessages(t *testing.T) {
	request := llm.RequestMessages{
		SystemPrompt: "system",
		Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}},
			llm.AssistantMessage{Content: []llm.Content{
				llm.ThinkingContent{Thinking: "think", ThinkingSignature: "sig"},
				llm.ToolCall{ID: "call-1", Name: "exec", Arguments: json.RawMessage(`{"command":"ls"}`)},
			}},
			llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "exec", IsError: true, Content: []llm.Content{llm.TextContent{Text: "failed"}}},
		},
		Tools: []llm.ToolDefinition{
			{Name: "exec", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", Description: "read file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
		},
	}
	converted, err := buildRequest(request, Config{BaseURL: "https://example.com", Token: "token", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := "system\n\n# tools descriptions\n<tool name=\"exec\">\n1. run\n</tool>\n<tool name=\"read\">\n1. read file\n</tool>"
	if converted.GetPrompt() != wantPrompt || converted.GetChatModelUid() != "model" {
		t.Fatalf("top-level request = %#v", converted)
	}
	if len(converted.GetChatMessagePrompts()) != 3 {
		t.Fatalf("message count = %d, want 3", len(converted.GetChatMessagePrompts()))
	}
	assistant := converted.GetChatMessagePrompts()[1]
	if assistant.GetSource() != devinproto.ExaCodeiumCommonPb_ChatMessageSource_ExaCodeiumCommonPb_ChatMessageSource_CHAT_MESSAGE_SOURCE_SYSTEM {
		t.Fatalf("assistant source = %v", assistant.GetSource())
	}
	if assistant.GetThinking() != "think" || assistant.GetSignature() != "sig" || len(assistant.GetToolCalls()) != 1 {
		t.Fatalf("assistant prompt = %#v", assistant)
	}
	historicalCall := assistant.GetToolCalls()[0]
	if historicalCall.GetName() != "exec" || historicalCall.GetArgumentsJson() != `{"command":"ls"}` {
		t.Fatalf("historical tool call = %#v", historicalCall)
	}
	toolResult := converted.GetChatMessagePrompts()[2]
	if toolResult.GetToolCallId() != "call-1" || !toolResult.GetToolResultIsError() {
		t.Fatalf("tool result = %#v", toolResult)
	}
	if len(converted.GetTools()) != 2 || converted.GetTools()[0].GetName() != "exec" || converted.GetTools()[1].GetName() != "read" {
		t.Fatalf("tools = %#v", converted.GetTools())
	}
	if converted.GetTools()[0].GetDescription() != "exec" || converted.GetTools()[1].GetDescription() != "read" {
		t.Fatalf("sanitized descriptions = %q/%q", converted.GetTools()[0].GetDescription(), converted.GetTools()[1].GetDescription())
	}
	if len(converted.GetMetadata().GetF()) != 732 {
		t.Fatalf("fingerprint length = %d, want 732", len(converted.GetMetadata().GetF()))
	}
	if converted.GetMetadata().GetExtensionVersion() != "3000.2.17" || converted.GetMetadata().GetIdeVersion() != "3000.2.17" {
		t.Fatalf("client versions = %q/%q, want 3000.2.17", converted.GetMetadata().GetExtensionVersion(), converted.GetMetadata().GetIdeVersion())
	}
	if converted.GetRequestType() != devinproto.ChatMessageRequestType_CHAT_MESSAGE_REQUEST_TYPE_CASCADE {
		t.Fatalf("request type = %v, want CASCADE", converted.GetRequestType())
	}
	if converted.GetCascadeId() == "" || converted.GetExecutionId() == "" {
		t.Fatalf("cascade/execution IDs = %q/%q, want non-empty", converted.GetCascadeId(), converted.GetExecutionId())
	}
	trajectory := converted.GetTrajectoryReference()
	if trajectory == nil || trajectory.GetTrajectoryId() == "" {
		t.Fatalf("trajectory reference = %#v, want ID", trajectory)
	}
	if trajectory.GetTrajectoryType() != devinproto.ExaCortexPb_CortexTrajectoryType_ExaCortexPb_CortexTrajectoryType_CORTEX_TRAJECTORY_TYPE_CASCADE {
		t.Fatalf("trajectory type = %v, want CASCADE", trajectory.GetTrajectoryType())
	}
	if trajectory.GetStepType() != devinproto.ExaCortexPb_CortexStepType_ExaCortexPb_CortexStepType_CORTEX_STEP_TYPE_USER_INPUT {
		t.Fatalf("trajectory step type = %v, want USER_INPUT", trajectory.GetStepType())
	}
	if converted.GetPlannerMode() != devinproto.ExaCodeiumCommonPb_ConversationalPlannerMode_ExaCodeiumCommonPb_ConversationalPlannerMode_CONVERSATIONAL_PLANNER_MODE_DEFAULT {
		t.Fatalf("planner mode = %v, want DEFAULT", converted.GetPlannerMode())
	}
	if converted.ProviderSource != nil {
		t.Fatalf("provider source = %v, want absent", converted.GetProviderSource())
	}
	if converted.GetConfiguration().GetMaxNewlines() != 400 {
		t.Fatalf("max newlines = %d, want 400", converted.GetConfiguration().GetMaxNewlines())
	}
}

// TestValidateImagesForModelRejectsGLM 验证无视觉模型带图时返回可读错误（透传给客户端）。
func TestValidateImagesForModelRejectsGLM(t *testing.T) {
	request := llm.RequestMessages{
		Model: "glm-5-2",
		Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{
			llm.TextContent{Text: "see"},
			llm.ImageContent{Data: "AAAA", MIMEType: "image/png"},
		}}},
	}
	emptyAdapter := &Adapter{}
	err := emptyAdapter.validateImagesForModel(request, "glm-5-2")
	if err == nil {
		t.Fatal("expected error for glm-5-2 + image")
	}
	if !strings.Contains(err.Error(), "does not support image") {
		t.Fatalf("error = %v, want does not support image", err)
	}
	if err := emptyAdapter.validateImagesForModel(request, "swe-1-7"); err != nil {
		t.Fatalf("swe-1-7 should allow images: %v", err)
	}
	if err := emptyAdapter.validateImagesForModel(llm.RequestMessages{Model: "glm-5-2", Messages: []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hi"}}},
	}}, "glm-5-2"); err != nil {
		t.Fatalf("text-only glm should pass: %v", err)
	}
}

// TestConnectErrorPassthrough 验证 Connect 错误 message 原样保留。
func TestConnectErrorPassthrough(t *testing.T) {
	err := connectError(connect.NewError(connect.CodeInvalidArgument, errors.New("model does not support images")))
	if err == nil || !strings.Contains(err.Error(), "invalid_argument") || !strings.Contains(err.Error(), "model does not support images") {
		t.Fatalf("connectError = %v", err)
	}
}

// TestBuildRequestOmitsHistoricalImages 验证多轮里只有最新用户消息挂 Images，历史图改占位。
func TestBuildRequestOmitsHistoricalImages(t *testing.T) {
	request := llm.RequestMessages{
		Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{
				llm.TextContent{Text: "see this"},
				llm.ImageContent{Data: "AAAA", MIMEType: "image/png"},
			}},
			llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}},
			llm.UserMessage{Content: []llm.Content{
				llm.TextContent{Text: "and this"},
				llm.ImageContent{Data: "BBBB", MIMEType: "image/jpeg"},
			}},
		},
	}
	converted, err := buildRequest(request, Config{BaseURL: "https://example.com", Token: "token", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	prompts := converted.GetChatMessagePrompts()
	if len(prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(prompts))
	}
	if len(prompts[0].GetImages()) != 0 {
		t.Fatalf("history images = %#v, want empty", prompts[0].GetImages())
	}
	if !strings.Contains(prompts[0].GetPrompt(), "[Image omitted from history]") {
		t.Fatalf("history prompt = %q, want image placeholder", prompts[0].GetPrompt())
	}
	if len(prompts[2].GetImages()) != 1 || prompts[2].GetImages()[0].GetBase64Data() != "BBBB" {
		t.Fatalf("latest images = %#v, want BBBB", prompts[2].GetImages())
	}
	if strings.Contains(prompts[2].GetPrompt(), "[Image omitted from history]") {
		t.Fatalf("latest prompt should keep real image, got %q", prompts[2].GetPrompt())
	}
}

// TestBuildRequestAttachesImagesInSameTurn 验证同一轮中 UserMessage(image) + ToolResultMessage 都挂图片。
// Anthropic 客户端常把 image 和 tool_result 放在同一条 user 消息里，解码后拆成两条；
// 旧逻辑仅挂最后一条，导致图片丢失。
func TestBuildRequestAttachesImagesInSameTurn(t *testing.T) {
	request := llm.RequestMessages{
		Messages: []llm.Message{
			llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}},
			llm.UserMessage{Content: []llm.Content{
				llm.TextContent{Text: "see this"},
				llm.ImageContent{Data: "AAAA", MIMEType: "image/png"},
			}},
			llm.ToolResultMessage{ToolCallID: "tc1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "file content"}}},
		},
	}
	converted, err := buildRequest(request, Config{BaseURL: "https://example.com", Token: "token", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	prompts := converted.GetChatMessagePrompts()
	// prompts: [assistant, user(image), tool_result]
	if len(prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(prompts))
	}
	// user 消息在 assistant 之后，属于当前轮，图片应保留
	if len(prompts[1].GetImages()) != 1 || prompts[1].GetImages()[0].GetBase64Data() != "AAAA" {
		t.Fatalf("current-turn user images = %#v, want AAAA", prompts[1].GetImages())
	}
	if strings.Contains(prompts[1].GetPrompt(), "[Image omitted from history]") {
		t.Fatalf("current-turn prompt should keep real image, got %q", prompts[1].GetPrompt())
	}
}

// TestBuildRequestWithoutToolsKeepsPromptUnchanged 的测试动机是确保工具转换不会污染纯文本请求。
func TestBuildRequestWithoutToolsKeepsPromptUnchanged(t *testing.T) {
	request := llm.RequestMessages{SystemPrompt: "system", Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}}}}
	converted, err := buildRequest(request, Config{BaseURL: "https://example.com", Token: "token", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if converted.GetPrompt() != "system" {
		t.Fatalf("prompt = %q, want unchanged system prompt", converted.GetPrompt())
	}
	if len(converted.GetTools()) != 0 {
		t.Fatalf("tools = %#v, want none", converted.GetTools())
	}
}

// TestBuildRequestIgnoresEmptyToolDescriptions 的测试动机是避免没有说明文本的工具生成空提示章节。
func TestBuildRequestIgnoresEmptyToolDescriptions(t *testing.T) {
	request := llm.RequestMessages{
		SystemPrompt: "system\n",
		Tools: []llm.ToolDefinition{
			{Name: "empty", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", Description: "  read a file  ", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	converted, err := buildRequest(request, Config{BaseURL: "https://example.com", Token: "token", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	want := "system\n\n# tools descriptions\n<tool name=\"read\">\n1. read a file\n</tool>"
	if converted.GetPrompt() != want {
		t.Fatalf("prompt = %q, want %q", converted.GetPrompt(), want)
	}
}

// TestResponseDecoderMapsOneFrameToOrderedEvents 的测试动机是明确一个 Devin protobuf 帧可以包含多个 loop 语义。
func TestResponseDecoderMapsOneFrameToOrderedEvents(t *testing.T) {
	decoder := newResponseDecoder("model")
	events := decoder.start()
	if len(events) != 1 || events[0].Type != llm.ResponseEventStart {
		t.Fatalf("start events = %#v", events)
	}
	events = decoder.decode(&devinproto.GetChatMessageResponse{
		DeltaThinking:  proto.String("think"),
		DeltaSignature: proto.String("sig"),
		DeltaText:      proto.String("answer"),
		DeltaToolCalls: []*devinproto.ExaCodeiumCommonPb_ChatToolCall{{
			Id: proto.String("call"), Name: proto.String("exec"), ArgumentsJson: proto.String(`{"command":"ls"}`),
		}},
	})
	want := []llm.ResponseEventType{
		llm.ResponseEventThinkingStart,
		llm.ResponseEventThinkingDelta,
		llm.ResponseEventThinkingEnd,
		llm.ResponseEventTextStart,
		llm.ResponseEventTextDelta,
		llm.ResponseEventTextEnd,
		llm.ResponseEventToolCallStart,
		llm.ResponseEventToolCallDelta,
	}
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(want), events)
	}
	for index, eventType := range want {
		if events[index].Type != eventType {
			t.Fatalf("event[%d] = %q, want %q", index, events[index].Type, eventType)
		}
	}
	partial := events[len(events)-1].Partial
	if partial == nil || len(partial.Content) != 3 {
		t.Fatalf("partial = %#v, want three content blocks", partial)
	}
	thinking := partial.Content[0].(llm.ThinkingContent)
	if thinking.Thinking != "think" || thinking.ThinkingSignature != "sig" {
		t.Fatalf("thinking = %#v", thinking)
	}
}

// TestResponseDecoderAggregatesToolArgumentFragments 的测试动机是保证事件保留原始增量，同时最终工具调用具有完整参数。
func TestResponseDecoderAggregatesToolArgumentFragments(t *testing.T) {
	decoder := newResponseDecoder("model")
	decoder.start()
	first := decoder.decode(&devinproto.GetChatMessageResponse{DeltaToolCalls: []*devinproto.ExaCodeiumCommonPb_ChatToolCall{{Id: proto.String("call"), Name: proto.String("exec")}}})
	if len(first) != 1 || first[0].Type != llm.ResponseEventToolCallStart {
		t.Fatalf("first events = %#v, want tool start with tool call", first)
	}
	if first[0].ToolCallID != "call" || first[0].ToolName != "exec" {
		t.Fatalf("start tool identity = %q/%q", first[0].ToolCallID, first[0].ToolName)
	}
	second := decoder.decode(&devinproto.GetChatMessageResponse{DeltaToolCalls: []*devinproto.ExaCodeiumCommonPb_ChatToolCall{{ArgumentsJson: proto.String(`{"command":"`)}}})
	if err := second[0].Validate(); err != nil {
		t.Fatalf("incomplete tool delta Validate() error = %v", err)
	}
	partialCall := second[0].Partial.Content[0].(llm.ToolCall)
	if string(partialCall.Arguments) != `{}` {
		t.Fatalf("incomplete partial arguments = %s, want {}", partialCall.Arguments)
	}
	third := decoder.decode(&devinproto.GetChatMessageResponse{DeltaToolCalls: []*devinproto.ExaCodeiumCommonPb_ChatToolCall{{ArgumentsJson: proto.String(`ls"}`)}}})
	stopEvents := decoder.decode(&devinproto.GetChatMessageResponse{StopReason: devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_FUNCTION_CALL.Enum()})
	if second[0].ToolCallID != "call" || third[0].ToolCallID != "call" {
		t.Fatalf("tool delta IDs = %q, %q", second[0].ToolCallID, third[0].ToolCallID)
	}
	if second[0].Delta != `{"command":"` || third[0].Delta != `ls"}` {
		t.Fatalf("tool deltas = %q, %q", second[0].Delta, third[0].Delta)
	}
	if len(stopEvents) != 0 {
		t.Fatalf("stop events = %#v, want no final event before EOF", stopEvents)
	}
	events := decoder.finish(nil)
	done := events[len(events)-1]
	if done.Type != llm.ResponseEventDone || done.Message == nil {
		t.Fatalf("done event = %#v", done)
	}
	call := done.Message.Content[0].(llm.ToolCall)
	if string(call.Arguments) != `{"command":"ls"}` {
		t.Fatalf("arguments = %s", call.Arguments)
	}
	if done.Message.StopReason != llm.StopReasonToolUse {
		t.Fatalf("stop reason = %q", done.Message.StopReason)
	}
}

// TestResponseDecoderConsumesUsageAfterStopReason 的测试动机是匹配 Devin 在停止原因后发送最终 token 统计帧的真实顺序。
func TestResponseDecoderConsumesUsageAfterStopReason(t *testing.T) {
	decoder := newResponseDecoder("model")
	decoder.start()
	decoder.decode(&devinproto.GetChatMessageResponse{DeltaText: proto.String("complete")})
	stopEvents := decoder.decode(&devinproto.GetChatMessageResponse{
		StopReason: devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_STOP_PATTERN.Enum(),
	})
	if len(stopEvents) != 0 || decoder.finished {
		t.Fatalf("stop frame events = %#v, finished = %v; want continued upstream consumption", stopEvents, decoder.finished)
	}
	usageEvents := decoder.decode(&devinproto.GetChatMessageResponse{Usage: &devinproto.ExaCodeiumCommonPb_ModelUsageStats{
		InputTokens: proto.Uint64(167), OutputTokens: proto.Uint64(61), CacheReadTokens: proto.Uint64(12195),
	}})
	if len(usageEvents) != 0 {
		t.Fatalf("usage frame events = %#v, want metadata-only frame", usageEvents)
	}
	events := decoder.finish(nil)
	done := events[len(events)-1]
	if done.Type != llm.ResponseEventDone || done.Reason != llm.StopReasonStop || done.Message == nil {
		t.Fatalf("done event = %#v", done)
	}
	usage := done.Message.Usage
	if usage.Input != 167 || usage.Output != 61 || usage.CacheRead != 12195 || usage.CacheWrite != 0 || usage.TotalTokens != 12423 {
		t.Fatalf("usage = %#v, want captured Devin totals", usage)
	}
}

// TestResponseStreamReadsUsageFrameAfterStopReason 的测试动机是保证 transport 不会因 stop 帧提前停止读取后续 usage 帧。
func TestResponseStreamReadsUsageFrameAfterStopReason(t *testing.T) {
	receiver := &fakeDevinResponseReceiver{responses: []*devinproto.GetChatMessageResponse{
		{DeltaText: proto.String("complete"), MessageId: proto.String("message-1")},
		{StopReason: devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_STOP_PATTERN.Enum()},
		{Usage: &devinproto.ExaCodeiumCommonPb_ModelUsageStats{
			ModelUid: proto.String("actual-model"), InputTokens: proto.Uint64(167), OutputTokens: proto.Uint64(61), CacheReadTokens: proto.Uint64(12195),
		}},
		{},
	}}
	stream := &responseStream{upstream: receiver, decoder: newResponseDecoder("requested-model")}
	var done llm.ResponseEvent
	for {
		event, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == llm.ResponseEventDone {
			done = event
		}
	}
	if receiver.index != len(receiver.responses) {
		t.Fatalf("read frame count = %d, want %d", receiver.index, len(receiver.responses))
	}
	if done.Message == nil || done.Message.ResponseID != "message-1" || done.Message.ResponseModel != "actual-model" {
		t.Fatalf("done message identity = %#v", done.Message)
	}
	if done.Message.Usage.TotalTokens != 12423 {
		t.Fatalf("done usage = %#v", done.Message.Usage)
	}
}

// TestResponseDecoderAcceptsContentBeforeNormalEOF 的测试动机是匹配 Devin 以统计帧和正常 Connect EOF 结束、但不发送 stop_reason 的真实行为。
func TestResponseDecoderAcceptsContentBeforeNormalEOF(t *testing.T) {
	decoder := newResponseDecoder("model")
	decoder.start()
	decoder.decode(&devinproto.GetChatMessageResponse{DeltaText: proto.String("complete")})
	events := decoder.finish(nil)
	done := events[len(events)-1]
	if done.Type != llm.ResponseEventDone || done.Reason != llm.StopReasonStop || done.Message == nil {
		t.Fatalf("done event = %#v", done)
	}
}

// TestResponseDecoderRejectsEmptyNormalEOF 的测试动机是避免把未产生任何内容的异常空流误报为成功。
func TestResponseDecoderRejectsEmptyNormalEOF(t *testing.T) {
	decoder := newResponseDecoder("model")
	decoder.start()
	event := decoder.finish(nil)[0]
	if event.Type != llm.ResponseEventError || event.Error == nil || event.Error.ErrorMessage != "Devin stream ended without generated content" {
		t.Fatalf("event = %#v, want empty-stream error", event)
	}
}

// TestResponseDecoderCompletesPartialWithThinking 验证 STOP_REASON_PARTIAL 不吞掉已生成的思考/文本。
func TestResponseDecoderCompletesPartialWithThinking(t *testing.T) {
	decoder := newResponseDecoder("model")
	decoder.start()
	decoder.decode(&devinproto.GetChatMessageResponse{DeltaThinking: proto.String("think")})
	decoder.decode(&devinproto.GetChatMessageResponse{DeltaText: proto.String("hello"), StopReason: devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_PARTIAL.Enum()})
	events := decoder.finish(nil)
	done := events[len(events)-1]
	if done.Type != llm.ResponseEventDone || done.Reason != llm.StopReasonLength || done.Message == nil {
		t.Fatalf("done event = %#v, want done with length", done)
	}
	if done.Message.Content[0].(llm.ThinkingContent).Thinking != "think" {
		t.Fatalf("thinking missing or wrong: %#v", done.Message.Content)
	}
	if done.Message.Content[1].(llm.TextContent).Text != "hello" {
		t.Fatalf("text missing or wrong: %#v", done.Message.Content)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := []struct {
		input devinproto.ExaCodeiumCommonPb_StopReason
		want  llm.StopReason
	}{
		{devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_MAX_TOKENS, llm.StopReasonLength},
		{devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_INCOMPLETE, llm.StopReasonLength},
		{devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_PARTIAL, llm.StopReasonLength},
		{devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_FUNCTION_CALL, llm.StopReasonToolUse},
		{devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_ERROR, llm.StopReasonError},
		{devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_STOP_PATTERN, llm.StopReasonStop},
	}
	for _, testCase := range cases {
		if got := mapStopReason(testCase.input); got != testCase.want {
			t.Fatalf("mapStopReason(%v) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

// TestRecordProtoJSONRedactsMetadata 的测试动机是确保 Devin 原始请求可诊断但不会写出 token 和设备指纹。
func TestRecordProtoJSONRedactsMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	recorder := debuglog.NewManager(root).Start(debuglog.RequestMeta{Method: "POST", Path: "/v1/responses"})
	request := &devinproto.GetChatMessageRequest{
		Metadata: &devinproto.ExaCodeiumCommonPb_Metadata{ApiKey: proto.String("secret-token"), F: proto.String("fingerprint")},
		Prompt:   proto.String("hello"),
	}
	recordProtoJSON(recorder, "03-devin-request.json", request)
	recordProtoJSON(recorder, "04-devin-response.jsonl", &devinproto.GetChatMessageResponse{DeltaText: proto.String("world")})
	recorder.Complete(debuglog.Completion{})

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, entries[0].Name())
	requestLog, err := os.ReadFile(filepath.Join(directory, "03-devin-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(requestLog), "secret-token") || strings.Contains(string(requestLog), "fingerprint") {
		t.Fatalf("request log contains credentials: %s", requestLog)
	}
	responseLog, err := os.ReadFile(filepath.Join(directory, "04-devin-response.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(responseLog), `"deltaText":"world"`) {
		t.Fatalf("response log = %s", responseLog)
	}
	if strings.Contains(string(responseLog), `"seq":`) || strings.Contains(string(responseLog), `"data":`) {
		t.Fatalf("raw protobuf response must not use an event envelope: %s", responseLog)
	}
}
