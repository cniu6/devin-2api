// 本文件定义 Anthropic Messages 最终 JSON 和 SSE 事件编码。
package messages

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leookun/devin-2api/internal/api/common"
	"github.com/leookun/devin-2api/internal/llm"
)

// SSEEvent 是单个 Anthropic SSE 事件。
type SSEEvent struct {
	Name string
	Data []byte
}

// StreamEncoder 保存一次 Anthropic Messages 流的协议状态。
type StreamEncoder struct {
	model      string
	messageID  string
	finished   bool
	index      int
	blocks     []*contentBlockState
	usage      llm.Usage
	blockIndex int
}

type contentBlockState struct {
	index     int
	kind      string
	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	toolID    string
	toolName  string
	input     strings.Builder
}

// NewStreamEncoder 为一次 Anthropic Messages 流创建编码状态。
func NewStreamEncoder(model string) *StreamEncoder {
	return &StreamEncoder{
		model:      model,
		messageID:  newAnthropicMessageID(),
		blockIndex: -1,
	}
}

// EncodeResponse 把最终助手消息编码为非流式 Anthropic Messages JSON。
func EncodeResponse(message *llm.AssistantMessage) ([]byte, error) {
	if message == nil {
		return nil, errors.New("response message is nil")
	}
	model := message.ResponseModel
	if model == "" {
		model = message.Model
	}
	if model == "" {
		model = "claude"
	}
	response := map[string]any{
		"id":          newAnthropicMessageID(),
		"type":        "message",
		"role":        "assistant",
		"content":     messageToAnthropic(message),
		"model":       model,
		"stop_reason": anthropicStopReason(message.StopReason),
		"usage":       anthropicUsage(message.Usage),
	}
	return json.Marshal(response)
}

// Encode 把一个中间响应事件展开为有序 Anthropic SSE 事件。
func (encoder *StreamEncoder) Encode(event llm.ResponseEvent) ([]SSEEvent, error) {
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("validate response event: %w", err)
	}
	if encoder.finished {
		return nil, errors.New("anthropic message stream is already done")
	}
	switch event.Type {
	case llm.ResponseEventStart:
		return encoder.start(event), nil
	case llm.ResponseEventTextStart:
		return encoder.startText(event), nil
	case llm.ResponseEventTextDelta:
		return encoder.textDelta(event), nil
	case llm.ResponseEventTextEnd:
		return encoder.endText(event), nil
	case llm.ResponseEventThinkingStart:
		return encoder.startThinking(event), nil
	case llm.ResponseEventThinkingDelta:
		return encoder.thinkingDelta(event), nil
	case llm.ResponseEventThinkingEnd:
		return encoder.endThinking(event), nil
	case llm.ResponseEventToolCallStart:
		return encoder.startToolUse(event), nil
	case llm.ResponseEventToolCallDelta:
		return encoder.toolUseDelta(event), nil
	case llm.ResponseEventToolCallEnd:
		return encoder.endToolUse(event), nil
	case llm.ResponseEventDone:
		return encoder.finish(event), nil
	case llm.ResponseEventError:
		return encoder.failed(event), nil
	default:
		return nil, fmt.Errorf("unsupported response event type %q", event.Type)
	}
}

func (encoder *StreamEncoder) start(event llm.ResponseEvent) []SSEEvent {
	if event.Message != nil {
		encoder.usage = event.Message.Usage
	}
	return []SSEEvent{encoder.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":          encoder.messageID,
			"type":        "message",
			"role":        "assistant",
			"content":     []any{},
			"model":       encoder.model,
			"stop_reason": nil,
			"usage": map[string]any{
				"input_tokens":  encoder.usage.Input,
				"output_tokens": 0,
			},
		},
	})}
}

func (encoder *StreamEncoder) startText(event llm.ResponseEvent) []SSEEvent {
	encoder.blockIndex = event.ContentIndex
	state := &contentBlockState{index: event.ContentIndex, kind: "text"}
	encoder.blocks = append(encoder.blocks, state)
	return []SSEEvent{
		encoder.event("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         event.ContentIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		}),
	}
}

func (encoder *StreamEncoder) textDelta(event llm.ResponseEvent) []SSEEvent {
	state := encoder.block(event.ContentIndex, "text")
	if state == nil {
		return nil
	}
	state.text.WriteString(event.Delta)
	return []SSEEvent{encoder.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": event.ContentIndex,
		"delta": map[string]any{"type": "text_delta", "text": event.Delta},
	})}
}

func (encoder *StreamEncoder) endText(event llm.ResponseEvent) []SSEEvent {
	state := encoder.block(event.ContentIndex, "text")
	if state == nil {
		return nil
	}
	text := event.Content
	if text == "" {
		text = state.text.String()
	}
	return []SSEEvent{encoder.event("content_block_stop", map[string]any{
		"type":          "content_block_stop",
		"index":         event.ContentIndex,
		"content_block": map[string]any{"type": "text", "text": text},
	})}
}

func (encoder *StreamEncoder) startThinking(event llm.ResponseEvent) []SSEEvent {
	encoder.blockIndex = event.ContentIndex
	state := &contentBlockState{index: event.ContentIndex, kind: "thinking"}
	encoder.blocks = append(encoder.blocks, state)
	return []SSEEvent{encoder.event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         event.ContentIndex,
		"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""},
	})}
}

func (encoder *StreamEncoder) thinkingDelta(event llm.ResponseEvent) []SSEEvent {
	state := encoder.block(event.ContentIndex, "thinking")
	if state == nil {
		return nil
	}
	state.thinking.WriteString(event.Delta)
	return []SSEEvent{encoder.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": event.ContentIndex,
		"delta": map[string]any{"type": "thinking_delta", "thinking": event.Delta},
	})}
}

func (encoder *StreamEncoder) endThinking(event llm.ResponseEvent) []SSEEvent {
	state := encoder.block(event.ContentIndex, "thinking")
	if state == nil {
		return nil
	}
	thinking := event.Content
	if thinking == "" {
		thinking = state.thinking.String()
	}
	if event.Partial != nil && event.ContentIndex < len(event.Partial.Content) {
		if t, ok := event.Partial.Content[event.ContentIndex].(llm.ThinkingContent); ok {
			state.signature.WriteString(t.ThinkingSignature)
		}
	}
	block := map[string]any{"type": "thinking", "thinking": thinking}
	if sig := state.signature.String(); sig != "" {
		block["signature"] = sig
	}
	return []SSEEvent{encoder.event("content_block_stop", map[string]any{
		"type":          "content_block_stop",
		"index":         event.ContentIndex,
		"content_block": block,
	})}
}

func (encoder *StreamEncoder) startToolUse(event llm.ResponseEvent) []SSEEvent {
	encoder.blockIndex = event.ContentIndex
	state := &contentBlockState{index: event.ContentIndex, kind: "tool_use", toolID: event.ToolCallID, toolName: event.ToolName}
	encoder.blocks = append(encoder.blocks, state)
	return []SSEEvent{encoder.event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         event.ContentIndex,
		"content_block": map[string]any{"type": "tool_use", "id": event.ToolCallID, "name": event.ToolName, "input": map[string]any{}},
	})}
}

func (encoder *StreamEncoder) toolUseDelta(event llm.ResponseEvent) []SSEEvent {
	state := encoder.block(event.ContentIndex, "tool_use")
	if state == nil {
		return nil
	}
	state.input.WriteString(event.Delta)
	return []SSEEvent{encoder.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": event.ContentIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": event.Delta},
	})}
}

func (encoder *StreamEncoder) endToolUse(event llm.ResponseEvent) []SSEEvent {
	state := encoder.block(event.ContentIndex, "tool_use")
	if state == nil {
		return nil
	}
	input := state.input.String()
	if event.ToolCall != nil {
		input = string(event.ToolCall.Arguments)
		state.toolID = event.ToolCall.ID
		state.toolName = event.ToolCall.Name
	}
	var parsed any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil || parsed == nil {
		parsed = map[string]any{}
	}
	return []SSEEvent{encoder.event("content_block_stop", map[string]any{
		"type":          "content_block_stop",
		"index":         event.ContentIndex,
		"content_block": map[string]any{"type": "tool_use", "id": state.toolID, "name": state.toolName, "input": parsed},
	})}
}

func (encoder *StreamEncoder) finish(event llm.ResponseEvent) []SSEEvent {
	encoder.finished = true
	if event.Message != nil {
		encoder.usage = event.Message.Usage
	}
	delta := map[string]any{"stop_reason": anthropicStopReason(event.Reason), "stop_sequence": nil}
	events := []SSEEvent{
		encoder.event("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": delta,
			"usage": map[string]any{"output_tokens": encoder.usage.Output},
		}),
		encoder.event("message_stop", map[string]any{"type": "message_stop"}),
	}
	return events
}

func (encoder *StreamEncoder) failed(event llm.ResponseEvent) []SSEEvent {
	encoder.finished = true
	message := "anthropic message stream failed"
	if event.Error != nil && event.Error.ErrorMessage != "" {
		message = event.Error.ErrorMessage
	}
	// Anthropic 官方流式错误格式：
	// event: error
	// data: {"type":"error","error":{"type":"...","message":"..."}}
	return []SSEEvent{encoder.event("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    common.AnthropicErrorType(message),
			"message": message,
		},
	})}
}

func (encoder *StreamEncoder) block(index int, kind string) *contentBlockState {
	for _, state := range encoder.blocks {
		if state.index == index && state.kind == kind {
			return state
		}
	}
	return nil
}

func (encoder *StreamEncoder) event(name string, payload map[string]any) SSEEvent {
	data, _ := json.Marshal(payload)
	return SSEEvent{Name: name, Data: data}
}

func messageToAnthropic(message *llm.AssistantMessage) []any {
	var blocks []any
	for _, block := range message.Content {
		switch content := block.(type) {
		case llm.TextContent:
			blocks = append(blocks, map[string]any{"type": "text", "text": content.Text})
		case llm.ThinkingContent:
			b := map[string]any{"type": "thinking", "thinking": content.Thinking}
			if content.ThinkingSignature != "" {
				b["signature"] = content.ThinkingSignature
			}
			blocks = append(blocks, b)
		case llm.ToolCall:
			var parsed any
			if err := json.Unmarshal(content.Arguments, &parsed); err != nil || parsed == nil {
				parsed = map[string]any{}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": content.ID, "name": content.Name, "input": parsed})
		}
	}
	return blocks
}

func anthropicUsage(usage llm.Usage) map[string]any {
	return map[string]any{
		"input_tokens":                usage.Input,
		"output_tokens":               usage.Output,
		"cache_creation_input_tokens": usage.CacheWrite,
		"cache_read_input_tokens":     usage.CacheRead,
	}
}

func anthropicStopReason(reason llm.StopReason) any {
	switch reason {
	case llm.StopReasonToolUse:
		return "tool_use"
	case llm.StopReasonLength:
		return "max_tokens"
	case llm.StopReasonStop:
		return "end_turn"
	case llm.StopReasonContentFilter:
		return "refusal"
	case llm.StopReasonError, llm.StopReasonAborted:
		// Anthropic 合法枚举里没有通用 error；"refusal" 是最接近的合法值，
		// 此前输出的 "error" 会被 SDK 校验直接拒绝。
		return "refusal"
	default:
		return nil
	}
}

func newAnthropicMessageID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("msg_%x", time.Now().UnixNano())
	}
	return "msg_" + hex.EncodeToString(value)
}
