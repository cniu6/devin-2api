// 本文件定义助手聚合响应、用量、诊断信息和增量响应事件。
//
// agent loop 的语义等价，不是 Provider 响应结构等价
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
)

// StopReason 表示模型生成停止的原因。
type StopReason string

const (
	StopReasonPending StopReason = "pending"
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
	// StopReasonContentFilter 表示生成被上游内容安全策略拦截；
	// 必须与普通错误区分，各协议才能如实映射（content_filter/refusal）。
	StopReasonContentFilter StopReason = "contentFilter"
)

// AssistantMessage 表示供应商无关的助手消息，也是最终响应消息。
type AssistantMessage struct {
	// Content 是助手生成的文字、思考和工具调用内容块。
	Content []Content
	// API 是生成消息所使用的上游 API 协议标识。
	API string
	// Provider 是生成消息的模型供应商标识。
	Provider string
	// Model 是请求时选择的模型标识。
	Model string
	// ResponseModel 是供应商在响应中返回的实际模型标识。
	ResponseModel string
	// ResponseID 是供应商分配的响应标识，可用于延续会话或诊断。
	ResponseID string
	// Diagnostics 是转换或流式处理过程中收集的非主响应诊断信息。
	Diagnostics []AssistantMessageDiagnostic
	// Usage 是本条响应累计的 token 与费用用量。
	Usage Usage
	// StopReason 是当前或最终生成状态。
	StopReason StopReason
	// ErrorMessage 是生成失败或中止时的可读错误信息。
	ErrorMessage string
	// TimestampMS 是创建消息时的 Unix 毫秒时间戳。
	TimestampMS int64
}

// ResponseMessage 是最终助手响应在兼容服务中的语义别名。
type ResponseMessage = AssistantMessage

// Role 返回助手角色。
func (AssistantMessage) Role() MessageRole { return MessageRoleAssistant }

// Validate 检查助手消息。
func (message AssistantMessage) Validate() error {
	if err := validateContent(message.Content, ContentTypeText, ContentTypeThinking, ContentTypeToolCall); err != nil {
		return err
	}
	if message.StopReason != "" && !message.StopReason.valid() {
		return fmt.Errorf("invalid stop reason %q", message.StopReason)
	}
	return message.Usage.Validate()
}

// Usage 保存一次助手响应或工具执行的累计用量。
type Usage struct {
	// Input 是输入 token 数。
	Input int64
	// Output 是输出 token 数。
	Output int64
	// CacheRead 是从提示缓存中读取的 token 数。
	CacheRead int64
	// CacheWrite 是写入提示缓存的 token 数。
	CacheWrite int64
	// CacheWrite1h 是写入一小时缓存的 token 数；nil 表示供应商未提供。
	CacheWrite1h *int64
	// Reasoning 是输出 token 中属于推理的子集；nil 表示供应商未提供。
	Reasoning *int64
	// TotalTokens 是供应商报告或适配器计算的总 token 数。
	TotalTokens int64
	// Cost 是按统一币种归一化后的费用明细。
	Cost UsageCost
	// CreditCost 是上游按 credit 计费的本次消耗；0 表示供应商未提供。
	CreditCost int64
}

// Validate 检查用量字段均为非负值。
func (usage Usage) Validate() error {
	if usage.Input < 0 || usage.Output < 0 || usage.CacheRead < 0 || usage.CacheWrite < 0 || usage.TotalTokens < 0 {
		return errors.New("usage values cannot be negative")
	}
	if usage.CacheWrite1h != nil && *usage.CacheWrite1h < 0 {
		return errors.New("one-hour cache write usage cannot be negative")
	}
	if usage.Reasoning != nil && *usage.Reasoning < 0 {
		return errors.New("reasoning usage cannot be negative")
	}
	return usage.Cost.Validate()
}

// UsageCost 保存一次用量对应的费用分项。
type UsageCost struct {
	// Input 是输入 token 费用。
	Input float64
	// Output 是输出 token 费用。
	Output float64
	// CacheRead 是缓存读取费用。
	CacheRead float64
	// CacheWrite 是缓存写入费用。
	CacheWrite float64
	// Total 是上述费用的总和或供应商报告的总费用。
	Total float64
}

// Validate 检查费用字段均为非负值。
func (cost UsageCost) Validate() error {
	if cost.Input < 0 || cost.Output < 0 || cost.CacheRead < 0 || cost.CacheWrite < 0 || cost.Total < 0 {
		return errors.New("usage costs cannot be negative")
	}
	return nil
}

// AssistantMessageDiagnostic 保存不改变主响应结果的诊断记录。
type AssistantMessageDiagnostic struct {
	// Type 是诊断事件的稳定类型标识。
	Type string
	// TimestampMS 是产生诊断时的 Unix 毫秒时间戳。
	TimestampMS int64
	// Error 是可选的错误摘要。
	Error *DiagnosticErrorInfo
	// Details 是可选的结构化诊断详情。
	Details json.RawMessage
}

// DiagnosticErrorInfo 保存跨供应商可移植的错误信息。
type DiagnosticErrorInfo struct {
	// Name 是错误类型或异常名称。
	Name string
	// Message 是错误的可读说明。
	Message string
	// Stack 是可选的原始调用栈。
	Stack string
	// Code 是供应商返回的字符串或数字错误码。
	Code any
}

// ResponseEventType 标识响应流中的增量事件种类。
type ResponseEventType string

const (
	ResponseEventStart         ResponseEventType = "start"
	ResponseEventTextStart     ResponseEventType = "text_start"
	ResponseEventTextDelta     ResponseEventType = "text_delta"
	ResponseEventTextEnd       ResponseEventType = "text_end"
	ResponseEventThinkingStart ResponseEventType = "thinking_start"
	ResponseEventThinkingDelta ResponseEventType = "thinking_delta"
	ResponseEventThinkingEnd   ResponseEventType = "thinking_end"
	ResponseEventToolCallStart ResponseEventType = "toolcall_start"
	ResponseEventToolCallDelta ResponseEventType = "toolcall_delta"
	ResponseEventToolCallEnd   ResponseEventType = "toolcall_end"
	ResponseEventDone          ResponseEventType = "done"
	ResponseEventError         ResponseEventType = "error"
)

// ResponseEvent 是供应商无关的助手响应增量事件。
type ResponseEvent struct {
	// Type 是本事件的种类。
	Type ResponseEventType
	// ContentIndex 是本事件对应的助手内容块下标。
	ContentIndex int
	// Delta 是本次新增的文字、思考或尚未完成的工具参数 JSON 片段。
	Delta string
	// Content 是文字或思考内容块结束时的完整内容。
	Content string
	// Partial 是处理本事件后累计形成的助手消息，包含当前累计用量。
	Partial *AssistantMessage
	// ToolCallID 是工具调用开始和参数增量事件关联的稳定调用标识。
	ToolCallID string
	// ToolName 是工具调用开始事件声明的工具名称。
	ToolName string
	// ToolCall 是工具调用结束时已经解析完成的调用对象。
	ToolCall *ToolCall
	// Reason 是完成或失败事件的停止原因。
	Reason StopReason
	// Message 是正常完成时的最终助手消息。
	Message *AssistantMessage
	// Error 是失败或中止时的最终错误助手消息。
	Error *AssistantMessage
}

// Validate 检查响应事件与其事件种类所需字段一致。
func (event ResponseEvent) Validate() error {
	switch event.Type {
	case ResponseEventStart:
		return requirePartial(event)
	case ResponseEventTextStart, ResponseEventThinkingStart:
		return requireIndexedPartial(event)
	case ResponseEventToolCallStart:
		if err := requireIndexedPartial(event); err != nil {
			return err
		}
		if event.ToolCallID == "" {
			return errors.New("tool call start event requires a tool call ID")
		}
		if event.ToolName == "" {
			return errors.New("tool call start event requires a tool name")
		}
		return nil
	case ResponseEventTextDelta, ResponseEventThinkingDelta:
		if err := requireIndexedPartial(event); err != nil {
			return err
		}
		if event.Delta == "" {
			return errors.New("delta event requires a delta")
		}
		return nil
	case ResponseEventToolCallDelta:
		if err := requireIndexedPartial(event); err != nil {
			return err
		}
		if event.ToolCallID == "" {
			return errors.New("tool call delta event requires a tool call ID")
		}
		return nil
	case ResponseEventTextEnd, ResponseEventThinkingEnd:
		return requireIndexedPartial(event)
	case ResponseEventToolCallEnd:
		if err := requireIndexedPartial(event); err != nil {
			return err
		}
		if event.ToolCall == nil {
			return errors.New("tool call end event requires a tool call")
		}
		return event.ToolCall.Validate()
	case ResponseEventDone:
		if event.Reason != StopReasonStop && event.Reason != StopReasonLength && event.Reason != StopReasonToolUse {
			return fmt.Errorf("invalid done reason %q", event.Reason)
		}
		if event.Message == nil {
			return errors.New("done event requires a final message")
		}
		return event.Message.Validate()
	case ResponseEventError:
		if event.Reason != StopReasonError && event.Reason != StopReasonAborted {
			return fmt.Errorf("invalid error reason %q", event.Reason)
		}
		if event.Error == nil {
			return errors.New("error event requires a final error message")
		}
		return event.Error.Validate()
	default:
		return fmt.Errorf("unknown response event type %q", event.Type)
	}
}

func requirePartial(event ResponseEvent) error {
	if event.Partial == nil {
		return fmt.Errorf("%s event requires a partial message", event.Type)
	}
	return event.Partial.Validate()
}

func requireIndexedPartial(event ResponseEvent) error {
	if event.ContentIndex < 0 {
		return fmt.Errorf("%s event requires a non-negative content index", event.Type)
	}
	return requirePartial(event)
}

func (reason StopReason) valid() bool {
	switch reason {
	case StopReasonPending, StopReasonStop, StopReasonLength, StopReasonToolUse, StopReasonError, StopReasonAborted, StopReasonContentFilter:
		return true
	default:
		return false
	}
}
