// 本文件定义 OpenAI Responses 最终 JSON 和带完整 item 生命周期的 typed SSE 编码。
package responses

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/leookun/devin-2api/internal/api/common"
	"github.com/leookun/devin-2api/internal/llm"
)

// SSEEvent 是 OpenAI Responses typed SSE 的单个事件。
type SSEEvent struct {
	// Name 是 SSE event 字段值。
	Name string
	// Data 是 JSON 编码的 SSE data 内容。
	Data []byte
}

// StreamEncoder 保存一次 HTTP Responses 流的协议状态和完整 output items。
type StreamEncoder struct {
	// model 是对外 Responses 请求使用的模型标识。
	model string
	// responseID 是本次 HTTP Response 的稳定 resp_ 标识。
	responseID string
	// createdAt 是 Response 创建时的 Unix 秒时间戳。
	createdAt int64
	// sequenceNumber 是下一个 SSE 事件的连续序号。
	sequenceNumber int64
	// items 按中间内容块下标保存正在生成或已经结束的 output item。
	items map[int]*streamItem
	// output 按 output_index 保存已经结束、可供下一轮重放的 output item。
	output []any
	// started 表示 response.created 和 response.in_progress 已经发出。
	started bool
	// completed 表示终止事件已经发出。
	completed bool
	// store 记录本次响应是否会被服务端保留，用于在响应体里如实回显。
	store bool
}

// streamItem 保存一个 reasoning、function_call 或 message output item 的编码状态。
type streamItem struct {
	// kind 是 Responses output item 的类型。
	kind string
	// id 是 rs_、fc_ 或 msg_ 开头的 item 标识。
	id string
	// outputIndex 是 item 在 Response output 数组中的下标。
	outputIndex int
	// callID 是 function_call 与 function_call_output 关联的业务标识。
	callID string
	// name 是 function_call 的工具名称。
	name string
	// contentIndex 是 message 内 output_text part 的下标。
	contentIndex int
	// value 累计文字、思考摘要或工具参数。
	value strings.Builder
	// encryptedContent 是可重放的思考签名；空值表示供应商未提供。
	encryptedContent string
	// closed 表示 item 已产生 output_item.done。
	closed bool
}

// NewStreamEncoder 为一次 HTTP Responses 请求创建独立的 SSE 编码状态。
func NewStreamEncoder(model string) *StreamEncoder {
	return &StreamEncoder{
		model:      model,
		responseID: newResponseID("resp"),
		createdAt:  time.Now().Unix(),
		items:      make(map[int]*streamItem),
		store:      true,
	}
}

// SetStore 记录本次响应是否真的会被服务端保留，使响应体的 store 字段如实回显。
// 恒定回显 true 会在调用方写了 store:false 时给出与实际行为相反的答案。
func (encoder *StreamEncoder) SetStore(store bool) {
	encoder.store = store
}

// EncodeResponse 将最终助手消息编码为非流式 Responses JSON 响应。
// store 为可选参数：不传时按 true 回显，保持既有调用点语义不变。
func EncodeResponse(message *llm.AssistantMessage, store ...bool) ([]byte, error) {
	if message == nil {
		return nil, fmt.Errorf("response message is nil")
	}
	output, err := outputFromMessage(message)
	if err != nil {
		return nil, err
	}
	model := message.ResponseModel
	if model == "" {
		model = message.Model
	}
	responseID := message.ResponseID
	if !strings.HasPrefix(responseID, "resp_") {
		responseID = newResponseID("resp")
	}
	createdAt := time.UnixMilli(message.TimestampMS).Unix()
	if message.TimestampMS <= 0 {
		createdAt = time.Now().Unix()
	}
	status := responseStatus(message.StopReason)
	storeValue := true
	if len(store) > 0 {
		storeValue = store[0]
	}
	response := baseResponse(responseID, model, createdAt, status, storeValue)
	if status == "completed" {
		response["completed_at"] = time.Now().Unix()
	}
	response["output"] = output
	response["usage"] = responseUsage(message.Usage)
	return json.Marshal(response)
}

// Encode 将一个中间响应事件展开为零个或多个有序 Responses SSE 事件。
func (encoder *StreamEncoder) Encode(event llm.ResponseEvent) ([]SSEEvent, error) {
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("validate response event: %w", err)
	}
	if encoder.completed {
		return nil, fmt.Errorf("response stream is already completed")
	}
	switch event.Type {
	case llm.ResponseEventStart:
		return encoder.start(), nil
	case llm.ResponseEventThinkingStart:
		return encoder.startReasoning(event)
	case llm.ResponseEventThinkingDelta:
		return encoder.reasoningDelta(event)
	case llm.ResponseEventThinkingEnd:
		return encoder.endReasoning(event)
	case llm.ResponseEventTextStart:
		return encoder.startText(event)
	case llm.ResponseEventTextDelta:
		return encoder.textDelta(event)
	case llm.ResponseEventTextEnd:
		return encoder.endText(event)
	case llm.ResponseEventToolCallStart:
		return encoder.startToolCall(event)
	case llm.ResponseEventToolCallDelta:
		return encoder.toolCallDelta(event)
	case llm.ResponseEventToolCallEnd:
		return encoder.endToolCall(event)
	case llm.ResponseEventDone:
		return encoder.done(event)
	case llm.ResponseEventError:
		return encoder.failed(event), nil
	default:
		return nil, fmt.Errorf("unsupported response event type %q", event.Type)
	}
}

func (encoder *StreamEncoder) start() []SSEEvent {
	if encoder.started {
		return nil
	}
	encoder.started = true
	created := baseResponse(encoder.responseID, encoder.model, encoder.createdAt, "in_progress", encoder.store)
	return []SSEEvent{
		encoder.emit("response.created", map[string]any{"response": created}),
		encoder.emit("response.in_progress", map[string]any{"response": created}),
	}
}

func (encoder *StreamEncoder) startReasoning(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.newItem(event.ContentIndex, "reasoning", "rs")
	if err != nil {
		return nil, err
	}
	if thinking, ok := contentAt[llm.ThinkingContent](event.Partial, event.ContentIndex); ok {
		item.encryptedContent = thinking.ThinkingSignature
	}
	addedItem := map[string]any{"id": item.id, "type": "reasoning", "summary": []any{}}
	if item.encryptedContent != "" {
		addedItem["encrypted_content"] = item.encryptedContent
	}
	return []SSEEvent{
		encoder.emit("response.output_item.added", map[string]any{"output_index": item.outputIndex, "item": addedItem}),
		encoder.emit("response.reasoning_summary_part.added", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": ""},
		}),
	}, nil
}

func (encoder *StreamEncoder) reasoningDelta(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.item(event.ContentIndex, "reasoning")
	if err != nil {
		return nil, err
	}
	item.value.WriteString(event.Delta)
	return []SSEEvent{encoder.emit("response.reasoning_summary_text.delta", map[string]any{
		"item_id": item.id, "output_index": item.outputIndex, "summary_index": 0, "delta": event.Delta,
	})}, nil
}

func (encoder *StreamEncoder) endReasoning(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.item(event.ContentIndex, "reasoning")
	if err != nil {
		return nil, err
	}
	text := event.Content
	if text == "" {
		text = item.value.String()
	}
	if thinking, ok := contentAt[llm.ThinkingContent](event.Partial, event.ContentIndex); ok && thinking.ThinkingSignature != "" {
		item.encryptedContent = thinking.ThinkingSignature
	}
	completedItem := map[string]any{
		"id": item.id, "type": "reasoning",
		"summary": []any{map[string]any{"type": "summary_text", "text": text}},
	}
	if item.encryptedContent != "" {
		completedItem["encrypted_content"] = item.encryptedContent
	}
	encoder.closeItem(item, completedItem)
	return []SSEEvent{
		encoder.emit("response.reasoning_summary_text.done", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "summary_index": 0, "text": text,
		}),
		encoder.emit("response.reasoning_summary_part.done", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": text},
		}),
		encoder.emit("response.output_item.done", map[string]any{"output_index": item.outputIndex, "item": completedItem}),
	}, nil
}

func (encoder *StreamEncoder) startText(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.newItem(event.ContentIndex, "message", "msg")
	if err != nil {
		return nil, err
	}
	item.contentIndex = 0
	return []SSEEvent{
		encoder.emit("response.output_item.added", map[string]any{
			"output_index": item.outputIndex,
			"item":         map[string]any{"id": item.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}},
		}),
		encoder.emit("response.content_part.added", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "content_index": item.contentIndex,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}},
		}),
	}, nil
}

func (encoder *StreamEncoder) textDelta(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.item(event.ContentIndex, "message")
	if err != nil {
		return nil, err
	}
	item.value.WriteString(event.Delta)
	return []SSEEvent{encoder.emit("response.output_text.delta", map[string]any{
		"item_id": item.id, "output_index": item.outputIndex, "content_index": item.contentIndex,
		"delta": event.Delta, "logprobs": []any{},
	})}, nil
}

func (encoder *StreamEncoder) endText(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.item(event.ContentIndex, "message")
	if err != nil {
		return nil, err
	}
	text := event.Content
	if text == "" {
		text = item.value.String()
	}
	part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
	completedItem := map[string]any{
		"id": item.id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part},
	}
	encoder.closeItem(item, completedItem)
	return []SSEEvent{
		encoder.emit("response.output_text.done", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "content_index": item.contentIndex,
			"text": text, "logprobs": []any{},
		}),
		encoder.emit("response.content_part.done", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "content_index": item.contentIndex, "part": part,
		}),
		encoder.emit("response.output_item.done", map[string]any{"output_index": item.outputIndex, "item": completedItem}),
	}, nil
}

func (encoder *StreamEncoder) startToolCall(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.newItem(event.ContentIndex, "function_call", "fc")
	if err != nil {
		return nil, err
	}
	item.callID = event.ToolCallID
	item.name = event.ToolName
	return []SSEEvent{encoder.emit("response.output_item.added", map[string]any{
		"output_index": item.outputIndex,
		"item": map[string]any{
			"id": item.id, "type": "function_call", "status": "in_progress", "arguments": "",
			"call_id": item.callID, "name": item.name,
		},
	})}, nil
}

func (encoder *StreamEncoder) toolCallDelta(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.item(event.ContentIndex, "function_call")
	if err != nil {
		return nil, err
	}
	item.value.WriteString(event.Delta)
	return []SSEEvent{encoder.emit("response.function_call_arguments.delta", map[string]any{
		"item_id": item.id, "output_index": item.outputIndex, "delta": event.Delta,
	})}, nil
}

func (encoder *StreamEncoder) endToolCall(event llm.ResponseEvent) ([]SSEEvent, error) {
	item, err := encoder.item(event.ContentIndex, "function_call")
	if err != nil {
		return nil, err
	}
	arguments := item.value.String()
	if event.ToolCall != nil {
		arguments = string(event.ToolCall.Arguments)
		item.callID = event.ToolCall.ID
		item.name = event.ToolCall.Name
	}
	completedItem := map[string]any{
		"id": item.id, "type": "function_call", "status": "completed", "arguments": arguments,
		"call_id": item.callID, "name": item.name,
	}
	encoder.closeItem(item, completedItem)
	return []SSEEvent{
		encoder.emit("response.function_call_arguments.done", map[string]any{
			"item_id": item.id, "output_index": item.outputIndex, "arguments": arguments,
		}),
		encoder.emit("response.output_item.done", map[string]any{"output_index": item.outputIndex, "item": completedItem}),
	}, nil
}

func (encoder *StreamEncoder) done(event llm.ResponseEvent) ([]SSEEvent, error) {
	for _, item := range encoder.items {
		if !item.closed {
			return nil, fmt.Errorf("cannot finish response with open %s item at output index %d", item.kind, item.outputIndex)
		}
	}
	encoder.completed = true
	response := baseResponse(encoder.responseID, encoder.model, encoder.createdAt, responseStatus(event.Reason), encoder.store)
	response["output"] = encoder.completedOutput()
	response["usage"] = responseUsage(event.Message.Usage)
	eventName := "response.completed"
	if event.Reason == llm.StopReasonLength {
		eventName = "response.incomplete"
		response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	} else {
		response["completed_at"] = time.Now().Unix()
	}
	return []SSEEvent{encoder.emit(eventName, map[string]any{"response": response})}, nil
}

func (encoder *StreamEncoder) failed(event llm.ResponseEvent) []SSEEvent {
	encoder.completed = true
	message := "response stream failed"
	if event.Error != nil && event.Error.ErrorMessage != "" {
		message = event.Error.ErrorMessage
	}
	// OpenAI Responses API 中，流式失败应发送 response.failed 事件，
	// 包含 status="failed" 的 response 对象与 error 字段。
	errorType := common.OpenAIErrorType(message)
	response := baseResponse(encoder.responseID, encoder.model, encoder.createdAt, "failed", encoder.store)
	response["error"] = map[string]any{"message": message, "type": errorType, "code": nil, "param": nil}
	return []SSEEvent{encoder.emit("response.failed", map[string]any{
		"response": response,
		"error":    map[string]any{"message": message, "type": errorType},
	})}
}

func (encoder *StreamEncoder) newItem(contentIndex int, kind string, prefix string) (*streamItem, error) {
	if _, exists := encoder.items[contentIndex]; exists {
		return nil, fmt.Errorf("content index %d already has an output item", contentIndex)
	}
	item := &streamItem{
		kind: kind, id: newResponseID(prefix), outputIndex: len(encoder.output), contentIndex: 0,
	}
	encoder.items[contentIndex] = item
	encoder.output = append(encoder.output, nil)
	return item, nil
}

func (encoder *StreamEncoder) item(contentIndex int, kind string) (*streamItem, error) {
	item := encoder.items[contentIndex]
	if item == nil {
		return nil, fmt.Errorf("content index %d has no active output item", contentIndex)
	}
	if item.kind != kind {
		return nil, fmt.Errorf("content index %d is %q, want %q", contentIndex, item.kind, kind)
	}
	if item.closed {
		return nil, fmt.Errorf("content index %d output item is already closed", contentIndex)
	}
	return item, nil
}

func (encoder *StreamEncoder) closeItem(item *streamItem, output any) {
	item.closed = true
	encoder.output[item.outputIndex] = output
}

func (encoder *StreamEncoder) completedOutput() []any {
	output := make([]any, 0, len(encoder.output))
	for _, item := range encoder.output {
		if item != nil {
			output = append(output, item)
		}
	}
	return output
}

func (encoder *StreamEncoder) emit(name string, payload map[string]any) SSEEvent {
	payload["type"] = name
	payload["sequence_number"] = encoder.sequenceNumber
	encoder.sequenceNumber++
	data, _ := json.Marshal(payload)
	return SSEEvent{Name: name, Data: data}
}

func baseResponse(id string, model string, createdAt int64, status string, store bool) map[string]any {
	// 对齐 OpenAI Response 对象的稳定字段。store 必须回显真实值：
	// 恒定输出 true 会让写了 store:false 的调用方以为会话被保留。
	return map[string]any{
		"id": id, "object": "response", "created_at": createdAt, "status": status,
		"error": nil, "incomplete_details": nil, "instructions": nil, "model": model,
		"output": []any{}, "parallel_tool_calls": true, "previous_response_id": nil,
		"reasoning": map[string]any{"effort": nil, "summary": nil}, "store": store,
		"temperature": nil, "top_p": nil, "truncation": "disabled",
		"tool_choice": "auto", "tools": []any{}, "usage": nil, "metadata": map[string]any{},
		"max_output_tokens": nil, "text": map[string]any{"format": map[string]any{"type": "text"}},
	}
}

func responseUsage(usage llm.Usage) map[string]any {
	reasoningTokens := int64(0)
	if usage.Reasoning != nil {
		reasoningTokens = *usage.Reasoning
	}
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	total := usage.TotalTokens
	if total == 0 {
		total = inputTokens + usage.Output
	}
	return map[string]any{
		"input_tokens": inputTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens": usage.CacheRead, "cache_write_tokens": usage.CacheWrite,
		},
		"output_tokens":         usage.Output,
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoningTokens},
		"total_tokens":          total,
	}
}

func outputFromMessage(message *llm.AssistantMessage) ([]any, error) {
	// OpenAI 常见顺序：reasoning → function_call → message；稳定排序避免 IDE 只读 output[0] 当 message。
	var reasonings, toolCalls, messages []any
	for _, block := range message.Content {
		switch content := block.(type) {
		case llm.TextContent:
			messages = append(messages, map[string]any{
				"id": newResponseID("msg"), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": content.Text, "annotations": []any{}}},
			})
		case llm.ThinkingContent:
			item := map[string]any{
				"id": newResponseID("rs"), "type": "reasoning", "status": "completed",
				"summary": []any{map[string]any{"type": "summary_text", "text": content.Thinking}},
			}
			if content.ThinkingSignature != "" {
				item["encrypted_content"] = content.ThinkingSignature
			}
			reasonings = append(reasonings, item)
		case llm.ToolCall:
			toolCalls = append(toolCalls, map[string]any{
				"id": newResponseID("fc"), "type": "function_call", "status": "completed",
				"call_id": content.ID, "name": content.Name, "arguments": string(content.Arguments),
			})
		default:
			return nil, fmt.Errorf("unsupported response content type %T", block)
		}
	}
	output := make([]any, 0, len(reasonings)+len(toolCalls)+len(messages))
	output = append(output, reasonings...)
	output = append(output, toolCalls...)
	output = append(output, messages...)
	return output, nil
}

func contentAt[T llm.Content](message *llm.AssistantMessage, index int) (T, bool) {
	var zero T
	if message == nil || index < 0 || index >= len(message.Content) {
		return zero, false
	}
	content, ok := message.Content[index].(T)
	return content, ok
}

func newResponseID(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("%s_%x", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(value)
}

func responseStatus(reason llm.StopReason) string {
	if reason == llm.StopReasonLength {
		return "incomplete"
	}
	if reason == llm.StopReasonError || reason == llm.StopReasonAborted {
		return "failed"
	}
	return "completed"
}
