// 本文件定义 OpenAI Chat Completions 请求 JSON 到中间 LLM 模型的转换。
package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leookun/devin-2api/internal/api/common"
	"github.com/leookun/devin-2api/internal/llm"
)

// Request 是 OpenAI Chat Completions 请求中本适配器支持的字段集合。
type Request struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
}

// Message 是 Chat Completions 消息条目。
type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// ToolCall 是助手消息中的工具调用（也用于流式增量）。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 是工具调用的函数部分。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool 是 OpenAI Chat function 工具定义。
type Tool struct {
	Type     string       `json:"type"`
	Function FunctionTool `json:"function"`
}

// FunctionTool 是 function 工具详情。
type FunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// StreamOptions 是流式额外选项。
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// AdaptedRequest 是 Chat 请求转换后的中间请求和生成选项。
type AdaptedRequest struct {
	Context llm.RequestMessages
	Options RequestOptions
}

// RequestOptions 保存不属于对话历史的生成控制参数。
type RequestOptions struct {
	Stream          bool
	IncludeUsage    bool
	MaxOutputTokens *int
	Temperature     *float64
}

// DecodeRequest 将 OpenAI Chat Completions JSON 请求转换为中间请求。
func DecodeRequest(data []byte) (AdaptedRequest, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&request); err != nil {
		return AdaptedRequest{}, fmt.Errorf("decode chat request: %w", err)
	}
	if request.Model == "" {
		return AdaptedRequest{}, errors.New("chat request model is required")
	}
	if len(request.Messages) == 0 {
		return AdaptedRequest{}, errors.New("chat request messages are required")
	}

	context := llm.RequestMessages{Model: request.Model}
	if err := appendMessages(&context, request.Messages); err != nil {
		return AdaptedRequest{}, err
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			continue
		}
		schema := tool.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		context.Tools = append(context.Tools, llm.ToolDefinition{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: schema,
		})
	}
	if err := context.Validate(); err != nil {
		return AdaptedRequest{}, fmt.Errorf("validate adapted request: %w", err)
	}

	maxTokens := request.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = request.MaxTokens
	}
	context.MaxTokens = maxTokens
	context.Temperature = request.Temperature
	context.TopP = request.TopP
	context.Seed = request.Seed
	if request.ParallelToolCalls != nil {
		context.DisableParallelToolCalls = !*request.ParallelToolCalls
	}
	stopPatterns, err := parseStopPatterns(request.Stop)
	if err != nil {
		return AdaptedRequest{}, err
	}
	context.StopPatterns = stopPatterns
	toolChoice, err := llm.ParseOpenAIToolChoice(request.ToolChoice)
	if err != nil {
		return AdaptedRequest{}, fmt.Errorf("tool_choice: %w", err)
	}
	context.ToolChoice = toolChoice
	return AdaptedRequest{
		Context: context,
		Options: RequestOptions{
			Stream:          request.Stream,
			IncludeUsage:    request.StreamOptions != nil && request.StreamOptions.IncludeUsage,
			MaxOutputTokens: maxTokens,
			Temperature:     request.Temperature,
		},
	}, nil
}

// parseStopPatterns 解析 OpenAI 的 stop 字段：字符串或字符串数组。
func parseStopPatterns(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("stop must be a string or string array: %w", err)
	}
	return list, nil
}

func appendMessages(context *llm.RequestMessages, messages []Message) error {
	for index, message := range messages {
		if err := appendMessage(context, message); err != nil {
			return fmt.Errorf("message[%d]: %w", index, err)
		}
	}
	return nil
}

func appendMessage(context *llm.RequestMessages, message Message) error {
	switch message.Role {
	case "system", "developer":
		content, err := common.DecodeContent(message.Content)
		if err != nil {
			return err
		}
		text := common.ContentText(content)
		if context.SystemPrompt != "" && text != "" {
			context.SystemPrompt += "\n"
		}
		context.SystemPrompt += text
	case "user":
		content, err := decodeUserContent(message.Content)
		if err != nil {
			return err
		}
		context.Messages = append(context.Messages, llm.UserMessage{
			Content:     content,
			TimestampMS: time.Now().UnixMilli(),
		})
	case "assistant":
		content, err := decodeAssistantContent(message)
		if err != nil {
			return err
		}
		context.Messages = append(context.Messages, llm.AssistantMessage{
			Content:     content,
			TimestampMS: time.Now().UnixMilli(),
		})
	case "tool":
		if message.ToolCallID == "" {
			return errors.New("tool message requires tool_call_id")
		}
		content, err := common.DecodeContent(message.Content)
		if err != nil {
			return err
		}
		name, err := findToolName(context.Messages, message.ToolCallID)
		if err != nil {
			return err
		}
		context.Messages = append(context.Messages, llm.ToolResultMessage{
			ToolCallID:  message.ToolCallID,
			ToolName:    name,
			Content:     content,
			TimestampMS: time.Now().UnixMilli(),
		})
	default:
		// 忽略未知角色。
	}
	return nil
}

func decodeUserContent(raw json.RawMessage) ([]llm.Content, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []llm.Content{llm.TextContent{Text: ""}}, nil
	}
	return common.DecodeContent(raw)
}

func decodeAssistantContent(message Message) ([]llm.Content, error) {
	var content []llm.Content
	if len(bytes.TrimSpace(message.Content)) > 0 && !bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) {
		decoded, err := common.DecodeContent(message.Content)
		if err != nil {
			return nil, err
		}
		content = append(content, decoded...)
	}
	for _, call := range message.ToolCalls {
		if call.Type != "" && call.Type != "function" {
			continue
		}
		args := json.RawMessage(call.Function.Arguments)
		if !llmIsJSONObject(args) {
			args = json.RawMessage(`{}`)
		}
		content = append(content, llm.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: args,
		})
	}
	return content, nil
}

func findToolName(messages []llm.Message, callID string) (string, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		assistant, ok := messages[index].(llm.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Content {
			call, ok := block.(llm.ToolCall)
			if ok && call.ID == callID {
				return call.Name, nil
			}
		}
	}
	return "", fmt.Errorf("tool message references unknown tool_call_id %q", callID)
}

func llmIsJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}
