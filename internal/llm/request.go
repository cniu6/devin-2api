// 本文件定义与具体模型供应商无关的请求上下文、消息、内容块和工具。
//
// Package llm 定义与具体模型供应商无关的请求消息和响应事件。
//
//	agent loop 的语义等价，不是 Provider 请求结构等价
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MessageRole 标识一条中间消息在对话中的角色。
type MessageRole string

const (
	MessageRoleUser       MessageRole = "user"
	MessageRoleAssistant  MessageRole = "assistant"
	MessageRoleToolResult MessageRole = "toolResult"
)

// ContentType 标识一个消息内容块的种类。
type ContentType string

const (
	ContentTypeText     ContentType = "text"
	ContentTypeThinking ContentType = "thinking"
	ContentTypeImage    ContentType = "image"
	ContentTypeToolCall ContentType = "toolCall"
)

// RequestMessages 是发送给任意供应商适配器的完整请求上下文。
type RequestMessages struct {
	// Model 是调用方指定的模型标识；空表示由适配器使用默认配置。
	Model string
	// SystemPrompt 是独立于普通消息历史的系统提示词。
	SystemPrompt string
	// SystemCacheControl 表示调用方要求缓存系统提示词（Anthropic cache_control）。
	SystemCacheControl bool
	// Messages 是按时间顺序排列、可跨供应商重放的完整对话历史。
	Messages []Message
	// Tools 是本次请求允许模型调用的工具定义。
	Tools []ToolDefinition
	// ToolChoice 是调用方对工具调用的约束；nil 表示交给模型自行决定。
	ToolChoice *ToolChoice
	// DisableParallelToolCalls 为 true 时禁止模型单轮并行发起多个工具调用。
	DisableParallelToolCalls bool
	// MaxTokens 是可选的输出 token 上限；nil 表示由适配器应用默认值。
	MaxTokens *int
	// Temperature 是可选的采样温度；nil 表示由适配器应用默认值。
	Temperature *float64
	// TopP 是可选的核采样阈值；nil 表示由适配器应用默认值。
	TopP *float64
	// TopK 是可选的 top-k 采样阈值；nil 表示由适配器应用默认值。
	TopK *int
	// StopPatterns 是可选的停止序列；命中任一序列时上游应结束生成。
	StopPatterns []string
	// Seed 是可选的采样种子；nil 表示由上游自行选择。
	Seed *int64
	// ServiceTier 是可选的服务等级（OpenAI service_tier）；空表示上游默认。
	ServiceTier string
}

// ToolChoiceMode 描述工具调用约束的模式。
type ToolChoiceMode string

const (
	// ToolChoiceAuto 表示模型自行决定是否调用工具。
	ToolChoiceAuto ToolChoiceMode = "auto"
	// ToolChoiceNone 表示禁止模型调用任何工具。
	ToolChoiceNone ToolChoiceMode = "none"
	// ToolChoiceRequired 表示模型必须调用某个工具。
	ToolChoiceRequired ToolChoiceMode = "required"
	// ToolChoiceNamed 表示模型必须调用指定名称的工具。
	ToolChoiceNamed ToolChoiceMode = "named"
)

// ToolChoice 是跨协议归一化的工具调用约束。
type ToolChoice struct {
	// Mode 是约束模式；空值等价于 ToolChoiceAuto。
	Mode ToolChoiceMode
	// ToolName 是 ToolChoiceNamed 模式下必须调用的工具名。
	ToolName string
}

// Message 是用户、助手或工具结果消息的统一接口。
type Message interface {
	// Role 返回消息在对话中的角色。
	Role() MessageRole
	// Validate 检查消息是否满足中间层约束。
	Validate() error
}

// Content 是文字、思考、图片或工具调用内容块的统一接口。
type Content interface {
	// ContentType 返回内容块的种类。
	ContentType() ContentType
	// Validate 检查内容块是否满足中间层约束。
	Validate() error
}

// TextContent 表示普通文字内容块。
type TextContent struct {
	// Text 是向用户展示或作为上下文重放的文字。
	Text string
	// TextSignature 是供应商返回的文字签名或序列化签名，重放时应原样保留。
	TextSignature string
}

// ContentType 返回文字内容类型。
func (TextContent) ContentType() ContentType { return ContentTypeText }

// Validate 检查文字内容块。
func (TextContent) Validate() error { return nil }

// ThinkingContent 表示模型的思考或推理内容块。
type ThinkingContent struct {
	// Thinking 是可见的思考内容；加密思考场景下可以为空。
	Thinking string
	// ThinkingSignature 是供应商签名或加密后的不透明载荷，重放时应原样保留。
	ThinkingSignature string
	// Redacted 表示思考正文已被供应商隐藏，签名中可能保存可重放载荷。
	Redacted bool
}

// ContentType 返回思考内容类型。
func (ThinkingContent) ContentType() ContentType { return ContentTypeThinking }

// Validate 检查思考内容块。
func (content ThinkingContent) Validate() error {
	if content.Redacted && content.ThinkingSignature == "" {
		return errors.New("redacted thinking content requires a signature")
	}
	return nil
}

// ImageContent 表示以 base64 编码传递的图片附件。
type ImageContent struct {
	// Data 是不含 data URL 前缀的 base64 图片数据。
	Data string
	// MIMEType 是图片的媒体类型，例如 image/png。
	MIMEType string
}

// ContentType 返回图片内容类型。
func (ImageContent) ContentType() ContentType { return ContentTypeImage }

// Validate 检查图片内容块。
func (content ImageContent) Validate() error {
	if content.Data == "" {
		return errors.New("image data is required")
	}
	if content.MIMEType == "" {
		return errors.New("image MIME type is required")
	}
	return nil
}

// ToolCall 表示助手发起的一次工具调用。
type ToolCall struct {
	// ID 是供应商分配的调用标识，用于关联后续工具结果。
	ID string
	// Name 是要调用的工具名称。
	Name string
	// Arguments 是模型增量拼接完成后的 JSON 参数对象。
	Arguments json.RawMessage
	// ThoughtSignature 是部分供应商附加到工具调用上的思考签名，重放时应原样保留。
	ThoughtSignature string
}

// ContentType 返回工具调用内容类型。
func (ToolCall) ContentType() ContentType { return ContentTypeToolCall }

// Validate 检查工具调用。
func (call ToolCall) Validate() error {
	if call.ID == "" {
		return errors.New("tool call ID is required")
	}
	if call.Name == "" {
		return errors.New("tool call name is required")
	}
	if !validJSONObject(call.Arguments) {
		return errors.New("tool call arguments must be a JSON object")
	}
	return nil
}

// UserMessage 表示一条用户消息。
type UserMessage struct {
	// Content 是用户提交的文字和图片内容块。
	Content []Content
	// TimestampMS 是创建消息时的 Unix 毫秒时间戳。
	TimestampMS int64
	// CacheControl 表示调用方把本条消息标记为提示缓存断点。
	CacheControl bool
}

// Role 返回用户角色。
func (UserMessage) Role() MessageRole { return MessageRoleUser }

// Validate 检查用户消息。
func (message UserMessage) Validate() error {
	return validateContent(message.Content, ContentTypeText, ContentTypeImage)
}

// ToolResultMessage 表示一次工具调用的执行结果。
type ToolResultMessage struct {
	// ToolCallID 是本结果所对应的工具调用标识。
	ToolCallID string
	// ToolName 是被执行的工具名称。
	ToolName string
	// Content 是返回给模型的文字和图片内容块。
	Content []Content
	// Details 是仅供应用层保存和展示、不一定发送给模型的结构化详情。
	Details json.RawMessage
	// Usage 是执行工具本身产生的可选用量，不计入助手响应主用量。
	Usage *Usage
	// AddedToolNames 是本次结果触发延迟加载后新增的工具名称。
	AddedToolNames []string
	// IsError 表示工具执行是否失败。
	IsError bool
	// TimestampMS 是创建消息时的 Unix 毫秒时间戳。
	TimestampMS int64
	// CacheControl 表示调用方把本条消息标记为提示缓存断点。
	CacheControl bool
}

// Role 返回工具结果角色。
func (ToolResultMessage) Role() MessageRole { return MessageRoleToolResult }

// Validate 检查工具结果消息。
func (message ToolResultMessage) Validate() error {
	if message.ToolCallID == "" {
		return errors.New("tool result call ID is required")
	}
	if message.ToolName == "" {
		return errors.New("tool result name is required")
	}
	if err := validateContent(message.Content, ContentTypeText, ContentTypeImage); err != nil {
		return err
	}
	if len(message.Details) > 0 && !json.Valid(message.Details) {
		return errors.New("tool result details must be valid JSON")
	}
	if message.Usage != nil {
		return message.Usage.Validate()
	}
	return nil
}

// ToolDefinition 定义模型可以调用的一个工具。
type ToolDefinition struct {
	// Name 是工具的稳定名称。
	Name string
	// Description 是提供给模型的工具用途说明。
	Description string
	// InputSchema 是描述工具输入对象的 JSON Schema。
	InputSchema json.RawMessage
}

// Validate 检查工具定义。
func (tool ToolDefinition) Validate() error {
	if tool.Name == "" {
		return errors.New("tool name is required")
	}
	if !validJSONObject(tool.InputSchema) {
		return errors.New("tool input schema must be a JSON object")
	}
	return nil
}

// Validate 检查完整请求上下文。
func (request RequestMessages) Validate() error {
	for index, message := range request.Messages {
		if message == nil {
			return fmt.Errorf("message %d is nil", index)
		}
		if err := message.Validate(); err != nil {
			return fmt.Errorf("message %d (%s): %w", index, message.Role(), err)
		}
	}
	for index, tool := range request.Tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("tool %d: %w", index, err)
		}
	}
	return nil
}

func validateContent(content []Content, allowed ...ContentType) error {
	allowedTypes := make(map[ContentType]struct{}, len(allowed))
	for _, contentType := range allowed {
		allowedTypes[contentType] = struct{}{}
	}
	for index, block := range content {
		if block == nil {
			return fmt.Errorf("content block %d is nil", index)
		}
		if _, ok := allowedTypes[block.ContentType()]; !ok {
			return fmt.Errorf("content block %d has disallowed type %q", index, block.ContentType())
		}
		if err := block.Validate(); err != nil {
			return fmt.Errorf("content block %d (%s): %w", index, block.ContentType(), err)
		}
	}
	return nil
}

func validJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}
