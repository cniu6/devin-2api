// 本文件定义 OpenAI Responses 请求 JSON 到中间 LLM 模型的转换。
//
// Package responses 定义 OpenAI Responses HTTP 协议与中间 LLM 模型之间的编解码。
package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leookun/devin-2api/internal/api/common"
	"github.com/leookun/devin-2api/internal/llm"
)

// Request 是 OpenAI Responses 请求中本适配器支持的字段集合。
type Request struct {
	// Model 是请求使用的模型标识。
	Model string `json:"model"`
	// Instructions 是独立于 input 的系统提示词。
	Instructions string `json:"instructions,omitempty"`
	// Input 是字符串或 Responses input item 数组。
	Input json.RawMessage `json:"input"`
	// Tools 是 OpenAI function 工具定义。
	Tools []Tool `json:"tools,omitempty"`
	// Stream 表示是否请求流式响应。
	Stream bool `json:"stream,omitempty"`
	// MaxOutputTokens 是可选的输出 token 上限。
	MaxOutputTokens *int `json:"max_output_tokens,omitempty"`
	// Temperature 是可选的采样温度。
	Temperature *float64 `json:"temperature,omitempty"`
	// TopP 是可选的核采样阈值。
	TopP *float64 `json:"top_p,omitempty"`
	// Seed 是可选的采样种子。
	Seed *int64 `json:"seed,omitempty"`
	// ToolChoice 是可选的工具调用约束（字符串或对象）。
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	// ParallelToolCalls 为 false 时禁止单轮并行工具调用。
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`
	// Truncation 是可选的上下文截断策略，仅用于响应回显。
	Truncation string `json:"truncation,omitempty"`
	// Metadata 是可选的调用方元数据，仅在响应中原样回显。
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// Reasoning 是可选的推理配置，仅在响应中原样回显。
	Reasoning json.RawMessage `json:"reasoning,omitempty"`
	// Text 是可选的文本格式配置，仅在响应中原样回显。
	Text json.RawMessage `json:"text,omitempty"`
	// PreviousResponseID 是上游 Responses 会话关联标识。
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	// Store 控制服务端是否保留本次响应供后续 previous_response_id 引用。
	// 用指针区分“未提供”与“显式 false”：OpenAI 的默认值是 true。
	Store *bool `json:"store,omitempty"`
	// ServiceTier 是可选的服务等级，透传上游 CompletionConfiguration.service_tier。
	ServiceTier string `json:"service_tier,omitempty"`
}

// Tool 是 OpenAI Responses function 工具定义。
type Tool struct {
	// Type 固定为 function。
	Type string `json:"type"`
	// Name 是工具名称。
	Name string `json:"name"`
	// Description 是工具用途说明。
	Description string `json:"description,omitempty"`
	// Parameters 是工具输入 JSON Schema。
	Parameters json.RawMessage `json:"parameters"`
}

// AdaptedRequest 是 OpenAI 请求转换后的中间请求和生成选项。
type AdaptedRequest struct {
	// Context 是供应商无关的完整对话上下文。
	Context llm.RequestMessages
	// Options 是本次生成所需的协议选项。
	Options RequestOptions
}

// RequestOptions 保存不属于对话历史的生成控制参数。
type RequestOptions struct {
	// Stream 表示调用方是否请求流式响应。
	Stream bool
	// MaxOutputTokens 是可选的输出 token 上限。
	MaxOutputTokens *int
	// Temperature 是可选的采样温度。
	Temperature *float64
	// PreviousResponseID 是调用方提供的上游响应关联标识。
	PreviousResponseID string
	// Store 表示调用方是否希望服务端保留本次响应（OpenAI 默认 true）。
	Store bool
	// Echo 保存需要原样回显到响应体的请求侧字段（OpenAI 契约）。
	Echo ResponseEcho
}

// ItemResolver 按 id 取回此前保存的 output item JSON，用于展开 item_reference。
type ItemResolver func(id string) (json.RawMessage, bool)

// PendingToolName 是工具结果引用了本请求内不存在的 call_id 时的占位名；
// 历史注入后由 ResolvePendingToolNames 回填真实名称。
const PendingToolName = "__devin2api_pending_tool__"

// decodeState 保存一次 input 解码的上下文。
type decodeState struct {
	resolve ItemResolver
	// allowDangling 为 true 时允许 function_call_output 引用尚未出现的 call_id
	// （previous_response_id 链式调用里 call 在服务端历史中）。
	allowDangling bool
	depth         int
}

// DecodeRequest 将 OpenAI Responses JSON 请求转换为中间请求。
func DecodeRequest(data []byte) (AdaptedRequest, error) {
	return DecodeRequestWithResolver(data, nil)
}

// DecodeRequestWithResolver 与 DecodeRequest 相同，但能借助 resolver 展开 item_reference。
func DecodeRequestWithResolver(data []byte, resolve ItemResolver) (AdaptedRequest, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&request); err != nil {
		return AdaptedRequest{}, fmt.Errorf("decode responses request: %w", err)
	}
	if request.Model == "" {
		return AdaptedRequest{}, errors.New("responses request model is required")
	}

	context := llm.RequestMessages{Model: request.Model, SystemPrompt: request.Instructions}
	state := &decodeState{resolve: resolve, allowDangling: request.PreviousResponseID != ""}
	if err := state.appendInputMessages(&context, request.Input); err != nil {
		return AdaptedRequest{}, err
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			continue
		}
		schema := tool.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		context.Tools = append(context.Tools, llm.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}
	if err := context.Validate(); err != nil {
		return AdaptedRequest{}, fmt.Errorf("validate adapted request: %w", err)
	}

	context.MaxTokens = request.MaxOutputTokens
	context.Temperature = request.Temperature
	context.TopP = request.TopP
	context.Seed = request.Seed
	if request.ParallelToolCalls != nil {
		context.DisableParallelToolCalls = !*request.ParallelToolCalls
	}
	toolChoice, err := llm.ParseOpenAIToolChoice(request.ToolChoice)
	if err != nil {
		return AdaptedRequest{}, fmt.Errorf("tool_choice: %w", err)
	}
	context.ToolChoice = toolChoice
	context.ServiceTier = request.ServiceTier
	if len(request.Text) > 0 {
		var text struct {
			Format json.RawMessage `json:"format"`
		}
		if json.Unmarshal(request.Text, &text) == nil {
			context.SystemPrompt = common.AppendSystemInstruction(context.SystemPrompt, common.ResponseFormatInstruction(text.Format))
		}
	}
	return AdaptedRequest{
		Context: context,
		Options: RequestOptions{
			Stream:             request.Stream,
			MaxOutputTokens:    request.MaxOutputTokens,
			Temperature:        request.Temperature,
			PreviousResponseID: request.PreviousResponseID,
			// 未提供 store 时按 OpenAI 契约取 true。
			Store: request.Store == nil || *request.Store,
			Echo: ResponseEcho{
				Instructions:       request.Instructions,
				Temperature:        request.Temperature,
				TopP:               request.TopP,
				MaxOutputTokens:    request.MaxOutputTokens,
				PreviousResponseID: request.PreviousResponseID,
				ToolChoice:         request.ToolChoice,
				ParallelToolCalls:  request.ParallelToolCalls,
				Truncation:         request.Truncation,
				Metadata:           request.Metadata,
				Reasoning:          request.Reasoning,
				Text:               request.Text,
				Tools:              marshalToolsEcho(request.Tools),
			},
		},
	}, nil
}

// marshalToolsEcho 把解析后的工具列表重新序列化，供响应体原样回显。
func marshalToolsEcho(tools []Tool) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return nil
	}
	return raw
}

func (state *decodeState) appendInputMessages(context *llm.RequestMessages, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		context.Messages = append(context.Messages, llm.UserMessage{
			Content:     []llm.Content{llm.TextContent{Text: text}},
			TimestampMS: time.Now().UnixMilli(),
		})
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("decode responses input: %w", err)
	}
	for index, item := range items {
		if err := state.appendInputItem(context, item); err != nil {
			return fmt.Errorf("input[%d]: %w", index, err)
		}
	}
	return nil
}

func (state *decodeState) appendInputItem(context *llm.RequestMessages, raw json.RawMessage) error {
	var header struct {
		Type string `json:"type"`
		Role string `json:"role"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return fmt.Errorf("decode input item: %w", err)
	}
	if header.Type == "" && header.Role != "" {
		header.Type = "message"
	}
	switch header.Type {
	case "message":
		return appendMessageItem(context, raw, header.Role)
	case "function_call":
		var item struct {
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		arguments := json.RawMessage(item.Arguments)
		if !json.Valid(arguments) || len(bytes.TrimSpace(arguments)) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		appendAssistantContent(context, llm.ToolCall{ID: item.CallID, Name: item.Name, Arguments: arguments})
		return nil
	case "reasoning":
		// 回放 reasoning item：encrypted_content 是我们输出的思考签名，
		// 丢掉它会让开启思考的模型在多轮工具调用里失去思考链。
		var item struct {
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		var text strings.Builder
		for _, part := range append(item.Summary, item.Content...) {
			if text.Len() > 0 && part.Text != "" {
				text.WriteString("\n")
			}
			text.WriteString(part.Text)
		}
		if text.Len() == 0 && item.EncryptedContent == "" {
			return nil
		}
		appendAssistantContent(context, llm.ThinkingContent{
			Thinking:          text.String(),
			ThinkingSignature: item.EncryptedContent,
			Redacted:          text.Len() == 0,
		})
		return nil
	case "item_reference":
		if state.resolve == nil || state.depth > 0 {
			return fmt.Errorf("item_reference %q cannot be resolved", header.ID)
		}
		resolved, ok := state.resolve(header.ID)
		if !ok {
			return fmt.Errorf("item_reference %q not found (unknown, expired, or stored with store:false)", header.ID)
		}
		state.depth++
		defer func() { state.depth-- }()
		return state.appendInputItem(context, resolved)
	case "function_call_output":
		var item struct {
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		output, err := common.RawOutputText(item.Output)
		if err != nil {
			return err
		}
		toolName := findToolName(context.Messages, item.CallID)
		if toolName == "" {
			if !state.allowDangling {
				return fmt.Errorf("function call output references unknown call_id %q", item.CallID)
			}
			toolName = PendingToolName
		}
		context.Messages = append(context.Messages, llm.ToolResultMessage{
			ToolCallID:  item.CallID,
			ToolName:    toolName,
			Content:     []llm.Content{llm.TextContent{Text: output}},
			TimestampMS: time.Now().UnixMilli(),
		})
		return nil
	default:
		return nil
	}
}

func findToolName(messages []llm.Message, callID string) string {
	for index := len(messages) - 1; index >= 0; index-- {
		assistant, ok := messages[index].(llm.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Content {
			call, ok := block.(llm.ToolCall)
			if ok && call.ID == callID {
				return call.Name
			}
		}
	}
	return ""
}

func appendMessageItem(context *llm.RequestMessages, raw json.RawMessage, role string) error {
	switch role {
	case "user", "assistant", "system", "developer":
	default:
		return nil
	}
	var item struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	content, err := common.DecodeContent(item.Content)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	switch role {
	case "user":
		context.Messages = append(context.Messages, llm.UserMessage{Content: content, TimestampMS: time.Now().UnixMilli()})
	case "assistant":
		appendAssistantContent(context, content...)
	case "system", "developer":
		text := common.ContentText(content)
		if context.SystemPrompt != "" && text != "" {
			context.SystemPrompt += "\n"
		}
		context.SystemPrompt += text
	}
	return nil
}

// appendAssistantContent 把 reasoning / function_call / assistant message 这类相邻的
// 助手侧 item 合并进同一条 AssistantMessage：Responses 把一轮助手输出拆成多个 item，
// 而上游一轮助手消息必须同时携带思考、文字和并行工具调用，逐条拆开会打乱轮次。
func appendAssistantContent(context *llm.RequestMessages, content ...llm.Content) {
	if count := len(context.Messages); count > 0 {
		if last, ok := context.Messages[count-1].(llm.AssistantMessage); ok {
			last.Content = append(last.Content, content...)
			if hasToolCall(last.Content) {
				last.StopReason = llm.StopReasonToolUse
			}
			context.Messages[count-1] = last
			return
		}
	}
	message := llm.AssistantMessage{Content: content, TimestampMS: time.Now().UnixMilli()}
	if hasToolCall(content) {
		message.StopReason = llm.StopReasonToolUse
	}
	context.Messages = append(context.Messages, message)
}

func hasToolCall(content []llm.Content) bool {
	for _, block := range content {
		if _, ok := block.(llm.ToolCall); ok {
			return true
		}
	}
	return false
}

// ResolvePendingToolNames 在服务端历史注入后，为占位名的工具结果回填真实工具名；
// 仍查不到的退化为通用名 "tool"，保证上游请求结构合法。
func ResolvePendingToolNames(context llm.RequestMessages) llm.RequestMessages {
	for index, message := range context.Messages {
		result, ok := message.(llm.ToolResultMessage)
		if !ok || result.ToolName != PendingToolName {
			continue
		}
		result.ToolName = findToolName(context.Messages[:index], result.ToolCallID)
		if result.ToolName == "" {
			result.ToolName = "tool"
		}
		context.Messages[index] = result
	}
	return context
}
