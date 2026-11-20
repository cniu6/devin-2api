// 本文件负责把 Devin protobuf 响应帧解释为有序的中间响应事件。
package devin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	devinproto "local/devinproto"

	"github.com/leookun/devin-2api/internal/llm"
)

// responseDecoder 保存一次 Devin 请求内的响应累计状态和内容映射。
type responseDecoder struct {
	// model 是请求使用的 Devin 模型标识。
	model string
	// partial 是当前累计形成的助手消息。
	partial llm.AssistantMessage
	// text 是当前累计的文字块。
	text *llm.TextContent
	// textBuilder 累计文字增量，避免 O(n²) 字符串拼接。
	textBuilder strings.Builder
	// textIdx 是文字块在 partial.Content 中的位置。
	textIdx int
	// textOpen 表示文字块已经开始但尚未结束。
	textOpen bool
	// thinking 是当前累计的思考块。
	thinking *llm.ThinkingContent
	// thinkingBuilder 累计思考正文。
	thinkingBuilder strings.Builder
	// thinkingSigBuilder 累计思考签名。
	thinkingSigBuilder strings.Builder
	// thinkIdx 是思考块在 partial.Content 中的位置。
	thinkIdx int
	// thinkingOpen 表示思考块已经开始但尚未结束。
	thinkingOpen bool
	// tools 保存正在累计参数的工具调用。
	tools []*toolState
	// started 表示已经生成 start 事件。
	started bool
	// finished 表示已经生成 done 或 error 事件。
	finished bool
	// hasStopReason 表示 Devin 已经显式返回停止原因。
	hasStopReason bool
	// stopReason 保存 Devin 声明的最终停止原因，等待上游 EOF 后用于完成响应。
	stopReason llm.StopReason
	// providerRefusal 表示上游供应商在 usage 中标记了拒答。
	providerRefusal bool
	// geminiSignature 是本轮 Gemini 思考签名（base64），完成时挂到工具调用上。
	geminiSignature string
}

// toolState 保存一次 Devin 工具调用的累计状态。
type toolState struct {
	// call 是当前累计形成的工具调用。
	call llm.ToolCall
	// contentIdx 是调用在 partial.Content 中的位置。
	contentIdx int
	// arguments 累计 Devin 返回的工具参数 JSON 片段。
	arguments strings.Builder
	// emitted 表示原始工具调用已经加入 partial 并产生 start 事件。
	emitted bool
}

func newResponseDecoder(model string) *responseDecoder {
	return &responseDecoder{model: model}
}

func (decoder *responseDecoder) start() []llm.ResponseEvent {
	if decoder.started || decoder.finished {
		return nil
	}
	decoder.started = true
	decoder.partial = llm.AssistantMessage{
		API: "connect", Provider: "devin", Model: decoder.model,
		StopReason: llm.StopReasonPending, TimestampMS: time.Now().UnixMilli(),
	}
	return []llm.ResponseEvent{{Type: llm.ResponseEventStart, Partial: &decoder.partial}}
}

func (decoder *responseDecoder) decode(response *devinproto.GetChatMessageResponse) []llm.ResponseEvent {
	if response == nil || decoder.finished {
		return nil
	}
	decoder.updateMetadata(response)
	events := make([]llm.ResponseEvent, 0, 6)
	if response.GetDeltaThinking() != "" || response.GetDeltaSignature() != "" || response.GetThinkingRedacted() {
		events = append(events, decoder.endText()...)
		events = append(events, decoder.decodeThinking(response)...)
	}
	if response.GetDeltaText() != "" {
		events = append(events, decoder.endThinking()...)
		events = append(events, decoder.decodeText(response.GetDeltaText())...)
	}
	for _, delta := range response.GetDeltaToolCalls() {
		events = append(events, decoder.endThinking()...)
		events = append(events, decoder.endText()...)
		events = append(events, decoder.decodeTool(delta)...)
	}
	if response.GetStopReason() != devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_UNSPECIFIED {
		decoder.hasStopReason = true
		decoder.stopReason = mapStopReason(response.GetStopReason())
	}
	return events
}

func (decoder *responseDecoder) finish(upstreamErr error) []llm.ResponseEvent {
	if decoder.finished {
		return nil
	}
	if upstreamErr != nil {
		// 流中途/结束时的 Connect 错误同样透传原文。
		return decoder.fail(connectError(upstreamErr))
	}
	if !decoder.hasStopReason && len(decoder.partial.Content) == 0 && len(decoder.tools) == 0 {
		return decoder.fail(errors.New("Devin stream ended without generated content"))
	}
	reason := decoder.stopReason
	if !decoder.hasStopReason {
		reason = llm.StopReasonStop
	}
	if reason == llm.StopReasonError {
		return decoder.fail(errors.New("Devin stopped with an error"))
	}
	return decoder.complete(reason)
}

func (decoder *responseDecoder) updateMetadata(response *devinproto.GetChatMessageResponse) {
	if response.MessageId != nil {
		decoder.partial.ResponseID = response.GetMessageId()
	}
	if response.ActualModelUid != nil {
		decoder.partial.ResponseModel = response.GetActualModelUid()
	}
	if timestamp := response.GetTimestamp(); timestamp != nil {
		decoder.partial.TimestampMS = time.Unix(timestamp.GetSeconds(), int64(timestamp.GetNanos())).UnixMilli()
	}
	if usage := response.GetUsage(); usage != nil {
		if decoder.partial.ResponseModel == "" && usage.ModelUid != nil {
			decoder.partial.ResponseModel = usage.GetModelUid()
		}
		if usage.InputTokens != nil {
			decoder.partial.Usage.Input = int64(usage.GetInputTokens())
		}
		if usage.OutputTokens != nil {
			decoder.partial.Usage.Output = int64(usage.GetOutputTokens())
		}
		if usage.CacheReadTokens != nil {
			decoder.partial.Usage.CacheRead = int64(usage.GetCacheReadTokens())
		}
		if usage.CacheWriteTokens != nil {
			decoder.partial.Usage.CacheWrite = int64(usage.GetCacheWriteTokens())
		}
		decoder.partial.Usage.TotalTokens = decoder.partial.Usage.Input + decoder.partial.Usage.Output + decoder.partial.Usage.CacheRead + decoder.partial.Usage.CacheWrite
		// 供应商明确拒答（provider_refusal）按内容过滤语义上报，
		// 而不是任由默认 stop 掩盖拦截事实。
		if usage.GetProviderRefusal() {
			decoder.providerRefusal = true
		}
	}
	// Gemini 思考签名是消息级字段，可能早于或晚于工具调用帧到达；
	// 先暂存，complete 时挂到本轮工具调用上，重放时原样回传。
	if sig := response.GetGeminiThoughtSignature(); len(sig) > 0 {
		decoder.geminiSignature = base64.StdEncoding.EncodeToString(sig)
	}
	if cost := response.GetCommittedCreditCost(); cost != 0 {
		decoder.partial.Usage.CreditCost = int64(cost)
	} else if cost := response.GetCreditCost(); cost != 0 {
		decoder.partial.Usage.CreditCost = int64(cost)
	}
}

func (decoder *responseDecoder) decodeThinking(response *devinproto.GetChatMessageResponse) []llm.ResponseEvent {
	events := make([]llm.ResponseEvent, 0, 2)
	if !decoder.thinkingOpen {
		decoder.thinking = &llm.ThinkingContent{Redacted: response.GetThinkingRedacted()}
		decoder.thinkingBuilder.Reset()
		decoder.thinkingSigBuilder.Reset()
		decoder.partial.Content = append(decoder.partial.Content, *decoder.thinking)
		decoder.thinkIdx = len(decoder.partial.Content) - 1
		decoder.thinkingOpen = true
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventThinkingStart, ContentIndex: decoder.thinkIdx, Partial: &decoder.partial})
	}
	if delta := response.GetDeltaThinking(); delta != "" {
		decoder.thinkingBuilder.WriteString(delta)
	}
	if sig := response.GetDeltaSignature(); sig != "" {
		decoder.thinkingSigBuilder.WriteString(sig)
	}
	decoder.thinking.Redacted = decoder.thinking.Redacted || response.GetThinkingRedacted()
	// 思考正文在 endThinking 再 materialize；签名通常较短，每帧同步签名避免 startReasoning 拿不到。
	decoder.thinking.ThinkingSignature = decoder.thinkingSigBuilder.String()
	decoder.partial.Content[decoder.thinkIdx] = *decoder.thinking
	if delta := response.GetDeltaThinking(); delta != "" {
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventThinkingDelta, ContentIndex: decoder.thinkIdx, Delta: delta, Partial: &decoder.partial})
	}
	return events
}

func (decoder *responseDecoder) decodeText(delta string) []llm.ResponseEvent {
	events := make([]llm.ResponseEvent, 0, 2)
	if !decoder.textOpen {
		decoder.text = &llm.TextContent{}
		decoder.textBuilder.Reset()
		decoder.partial.Content = append(decoder.partial.Content, *decoder.text)
		decoder.textIdx = len(decoder.partial.Content) - 1
		decoder.textOpen = true
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventTextStart, ContentIndex: decoder.textIdx, Partial: &decoder.partial})
	}
	// 用 Builder 累加，避免每帧产生越来越大的新字符串。
	decoder.textBuilder.WriteString(delta)
	events = append(events, llm.ResponseEvent{Type: llm.ResponseEventTextDelta, ContentIndex: decoder.textIdx, Delta: delta, Partial: &decoder.partial})
	return events
}

func (decoder *responseDecoder) decodeTool(delta *devinproto.ExaCodeiumCommonPb_ChatToolCall) []llm.ResponseEvent {
	if delta == nil {
		return nil
	}
	state := decoder.findTool(delta.GetId())
	if state == nil {
		state = &toolState{
			call:       llm.ToolCall{ID: delta.GetId(), Name: delta.GetName(), Arguments: json.RawMessage(`{}`)},
			contentIdx: -1,
		}
		decoder.tools = append(decoder.tools, state)
	}
	if delta.GetId() != "" {
		state.call.ID = delta.GetId()
	}
	if delta.GetName() != "" {
		state.call.Name = delta.GetName()
	}
	fragment := delta.GetArgumentsJson()
	hasFragment := delta.ArgumentsJson != nil
	if hasFragment {
		state.arguments.WriteString(fragment)
	}
	return decoder.decodeNativeTool(state, fragment, hasFragment)
}

// decodeNativeTool 保留 Devin 原生工具名称和参数增量语义。
func (decoder *responseDecoder) decodeNativeTool(state *toolState, fragment string, hasFragment bool) []llm.ResponseEvent {
	events := make([]llm.ResponseEvent, 0, 2)
	if !state.emitted {
		state.contentIdx = len(decoder.partial.Content)
		state.emitted = true
		decoder.partial.Content = append(decoder.partial.Content, state.call)
		events = append(events, llm.ResponseEvent{
			Type: llm.ResponseEventToolCallStart, ContentIndex: state.contentIdx,
			ToolCallID: state.call.ID, ToolName: state.call.Name, Partial: &decoder.partial,
		})
	}
	// 工具参数在 complete 中一次性解析并写入，避免每帧 O(n) 拷贝/校验。
	if hasFragment {
		events = append(events, llm.ResponseEvent{
			Type: llm.ResponseEventToolCallDelta, ContentIndex: state.contentIdx,
			ToolCallID: state.call.ID, Delta: fragment, Partial: &decoder.partial,
		})
	}
	return events
}

func (decoder *responseDecoder) endThinking() []llm.ResponseEvent {
	if !decoder.thinkingOpen || decoder.thinking == nil {
		return nil
	}
	decoder.thinkingOpen = false
	// 只在思考块结束时一次性生成完整思考与签名。
	decoder.thinking.Thinking = decoder.thinkingBuilder.String()
	decoder.thinking.ThinkingSignature = decoder.thinkingSigBuilder.String()
	decoder.partial.Content[decoder.thinkIdx] = *decoder.thinking
	return []llm.ResponseEvent{{
		Type: llm.ResponseEventThinkingEnd, ContentIndex: decoder.thinkIdx,
		Content: decoder.thinking.Thinking, Partial: &decoder.partial,
	}}
}

func (decoder *responseDecoder) endText() []llm.ResponseEvent {
	if !decoder.textOpen || decoder.text == nil {
		return nil
	}
	decoder.textOpen = false
	// 只在内容块结束时一次性生成完整文字，避免 O(n²) 拷贝。
	decoder.text.Text = decoder.textBuilder.String()
	decoder.partial.Content[decoder.textIdx] = *decoder.text
	return []llm.ResponseEvent{{
		Type: llm.ResponseEventTextEnd, ContentIndex: decoder.textIdx,
		Content: decoder.text.Text, Partial: &decoder.partial,
	}}
}

func (decoder *responseDecoder) findTool(id string) *toolState {
	for _, state := range decoder.tools {
		if id != "" && state.call.ID == id {
			return state
		}
	}
	if id == "" && len(decoder.tools) > 0 {
		return decoder.tools[len(decoder.tools)-1]
	}
	return nil
}

func (decoder *responseDecoder) complete(reason llm.StopReason) []llm.ResponseEvent {
	if decoder.finished {
		return nil
	}
	events := make([]llm.ResponseEvent, 0, len(decoder.tools)*3+3)
	// 供应商拒答优先于普通结束，如实上报拦截而不是谎报 stop。
	if decoder.providerRefusal && (reason == llm.StopReasonStop || reason == llm.StopReasonPending || reason == "") {
		reason = llm.StopReasonContentFilter
	}
	decoder.partial.StopReason = reason
	events = append(events, decoder.endThinking()...)
	events = append(events, decoder.endText()...)
	for _, state := range decoder.tools {
		if !state.emitted {
			continue
		}
		// 在结束时一次性把 Builder 中的完整参数转成 JSON，避免中间反复解析/拷贝。
		state.call.Arguments = json.RawMessage(state.arguments.String())
		if state.call.ThoughtSignature == "" {
			state.call.ThoughtSignature = decoder.geminiSignature
		}
		if !isJSONObject(state.call.Arguments) {
			state.call.Arguments = json.RawMessage(`{}`)
		}
		decoder.partial.Content[state.contentIdx] = state.call
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventToolCallEnd, ContentIndex: state.contentIdx, ToolCall: &state.call, Partial: &decoder.partial})
	}
	events = append(events, llm.ResponseEvent{Type: llm.ResponseEventDone, Reason: reason, Message: &decoder.partial})
	decoder.finished = true
	return events
}

func (decoder *responseDecoder) fail(err error) []llm.ResponseEvent {
	if decoder.finished {
		return nil
	}
	decoder.partial.StopReason = llm.StopReasonError
	decoder.partial.ErrorMessage = err.Error()
	decoder.finished = true
	return []llm.ResponseEvent{{Type: llm.ResponseEventError, Reason: llm.StopReasonError, Error: &decoder.partial}}
}

func mapStopReason(reason devinproto.ExaCodeiumCommonPb_StopReason) llm.StopReason {
	switch reason {
	case devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_MAX_TOKENS,
		devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_INCOMPLETE,
		devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_PARTIAL:
		// INCOMPLETE/PARTIAL 都表示模型没有生成完整回复，按长度截断处理。
		return llm.StopReasonLength
	case devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_MAX_NEWLINES,
		devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_MIN_LOG_PROB:
		// 命中 token/换行/logprob 上限同属「被长度截断」，不能按正常 stop 报给客户端。
		return llm.StopReasonLength
	case devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_FUNCTION_CALL:
		return llm.StopReasonToolUse
	case devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_CONTENT_FILTER:
		return llm.StopReasonContentFilter
	case devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_ERROR,
		devinproto.ExaCodeiumCommonPb_StopReason_ExaCodeiumCommonPb_StopReason_STOP_REASON_NONFINITE_LOGIT_OR_PROB:
		// 异常终止是失败而非正常结束。
		return llm.StopReasonError
	default:
		return llm.StopReasonStop
	}
}
