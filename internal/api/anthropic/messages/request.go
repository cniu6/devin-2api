// 本文件定义 Anthropic Messages API 请求 JSON 到中间 LLM 模型的转换。
package messages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leookun/devin-2api/internal/api/common"
	"github.com/leookun/devin-2api/internal/llm"
)

// Request 是 Anthropic Messages 请求中本适配器支持的字段集合。
type Request struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"`
	MaxTokens     int             `json:"max_tokens"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
}

// Message 是 Anthropic 消息条目。
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Tool 是 Anthropic 工具定义。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolResult 是 Anthropic 工具结果内容块。
type ToolResult struct {
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ToolUse 是 Anthropic 助手历史中的工具调用。
type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// AdaptedRequest 是 Anthropic 请求转换后的中间请求和生成选项。
type AdaptedRequest struct {
	Context llm.RequestMessages
	Options RequestOptions
}

// RequestOptions 保存不属于对话历史的生成控制参数。
type RequestOptions struct {
	Stream          bool
	MaxOutputTokens int
	Temperature     *float64
}

// DecodeRequest 将 Anthropic Messages JSON 请求转换为中间请求。
func DecodeRequest(data []byte) (AdaptedRequest, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&request); err != nil {
		return AdaptedRequest{}, fmt.Errorf("decode anthropic request: %w", err)
	}
	if request.Model == "" {
		return AdaptedRequest{}, errors.New("anthropic request model is required")
	}
	if len(request.Messages) == 0 {
		return AdaptedRequest{}, errors.New("anthropic request messages are required")
	}

	context := llm.RequestMessages{Model: request.Model}
	if len(bytes.TrimSpace(request.System)) > 0 && !bytes.Equal(bytes.TrimSpace(request.System), []byte("null")) {
		if err := appendSystem(&context, request.System); err != nil {
			return AdaptedRequest{}, err
		}
	}
	if err := appendMessages(&context, request.Messages); err != nil {
		return AdaptedRequest{}, err
	}
	for _, tool := range request.Tools {
		schema := tool.InputSchema
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

	if request.MaxTokens > 0 {
		maxTokens := request.MaxTokens
		context.MaxTokens = &maxTokens
	}
	context.Temperature = request.Temperature
	context.TopP = request.TopP
	context.TopK = request.TopK
	context.StopPatterns = request.StopSequences
	toolChoice, disableParallel, err := llm.ParseAnthropicToolChoice(request.ToolChoice)
	if err != nil {
		return AdaptedRequest{}, fmt.Errorf("tool_choice: %w", err)
	}
	context.ToolChoice = toolChoice
	context.DisableParallelToolCalls = disableParallel
	return AdaptedRequest{
		Context: context,
		Options: RequestOptions{
			Stream:          request.Stream,
			MaxOutputTokens: request.MaxTokens,
			Temperature:     request.Temperature,
		},
	}, nil
}

func appendSystem(context *llm.RequestMessages, raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		context.SystemPrompt = text
		return nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return fmt.Errorf("decode anthropic system: %w", err)
	}
	for _, part := range parts {
		var block struct {
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if err := json.Unmarshal(part, &block); err != nil {
			return err
		}
		if block.Type != "text" {
			continue
		}
		if hasCacheControl(block.CacheControl) {
			context.SystemCacheControl = true
		}
		if context.SystemPrompt != "" && block.Text != "" {
			context.SystemPrompt += "\n"
		}
		context.SystemPrompt += block.Text
	}
	return nil
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
	case "user":
		messages, err := decodeAnthropicUserMessages(context.Messages, message.Content)
		if err != nil {
			return err
		}
		context.Messages = append(context.Messages, messages...)
	case "assistant":
		content, cached, err := decodeAssistantContent(message.Content)
		if err != nil {
			return err
		}
		context.Messages = append(context.Messages, llm.AssistantMessage{
			Content:      content,
			TimestampMS:  time.Now().UnixMilli(),
			CacheControl: cached,
		})
	default:
		// 忽略未知角色。
	}
	return nil
}

// decodeAnthropicUserMessages 把 Anthropic user 消息 content 拆分为一个或多个中间消息。
// tool_result 内容块会生成独立的 llm.ToolResultMessage。
func decodeAnthropicUserMessages(messages []llm.Message, raw json.RawMessage) ([]llm.Message, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []llm.Message{llm.UserMessage{
			Content:     []llm.Content{llm.TextContent{Text: ""}},
			TimestampMS: time.Now().UnixMilli(),
		}}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []llm.Message{llm.UserMessage{
			Content:     []llm.Content{llm.TextContent{Text: text}},
			TimestampMS: time.Now().UnixMilli(),
		}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("decode user content: %w", err)
	}

	var result []llm.Message
	var currentUserContent []llm.Content
	currentUserCached := false
	flushUser := func() {
		if len(currentUserContent) == 0 {
			return
		}
		result = append(result, llm.UserMessage{
			Content:      currentUserContent,
			TimestampMS:  time.Now().UnixMilli(),
			CacheControl: currentUserCached,
		})
		currentUserContent = nil
		currentUserCached = false
	}

	for index, part := range parts {
		var header struct {
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			ToolUseID    string          `json:"tool_use_id"`
			Content      json.RawMessage `json:"content"`
			IsError      bool            `json:"is_error"`
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if err := json.Unmarshal(part, &header); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", index, err)
		}
		cached := hasCacheControl(header.CacheControl)
		switch header.Type {
		case "text":
			currentUserContent = append(currentUserContent, llm.TextContent{Text: header.Text})
			currentUserCached = currentUserCached || cached
		case "image":
			image, err := common.DecodeImagePart(part)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: %w", index, err)
			}
			currentUserContent = append(currentUserContent, image)
			currentUserCached = currentUserCached || cached
		case "tool_result":
			flushUser()
			tool, err := decodeToolResult(messages, header.ToolUseID, header.Content, header.IsError)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: %w", index, err)
			}
			tool.CacheControl = cached
			result = append(result, tool)
			messages = append(messages, tool)
		default:
			continue
		}
	}
	flushUser()
	return result, nil
}

// decodeAssistantContent 解码助手历史内容块，并报告是否携带 cache_control 断点。
func decodeAssistantContent(raw json.RawMessage) ([]llm.Content, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []llm.Content{llm.TextContent{Text: ""}}, false, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []llm.Content{llm.TextContent{Text: text}}, false, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, false, fmt.Errorf("decode assistant content: %w", err)
	}
	content := make([]llm.Content, 0, len(parts))
	cached := false
	for index, part := range parts {
		var header struct {
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			Thinking     string          `json:"thinking"`
			Signature    string          `json:"signature"`
			Data         string          `json:"data"`
			ID           string          `json:"id"`
			Name         string          `json:"name"`
			Input        json.RawMessage `json:"input"`
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if err := json.Unmarshal(part, &header); err != nil {
			return nil, false, fmt.Errorf("content[%d]: %w", index, err)
		}
		cached = cached || hasCacheControl(header.CacheControl)
		switch header.Type {
		case "text":
			content = append(content, llm.TextContent{Text: header.Text})
		case "thinking":
			// Anthropic 思考块正文在 thinking 字段、签名在 signature；
			// 签名必须原样回传，否则开启 extended thinking 的多轮工具调用会被上游拒绝。
			thinking := header.Thinking
			if thinking == "" {
				thinking = header.Text
			}
			content = append(content, llm.ThinkingContent{Thinking: thinking, ThinkingSignature: header.Signature})
		case "redacted_thinking":
			if header.Data != "" {
				content = append(content, llm.ThinkingContent{ThinkingSignature: header.Data, Redacted: true})
			}
		case "tool_use":
			args := header.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			content = append(content, llm.ToolCall{ID: header.ID, Name: header.Name, Arguments: args})
		default:
			continue
		}
	}
	return content, cached, nil
}

// hasCacheControl 判断内容块是否携带非空 cache_control。
func hasCacheControl(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func decodeToolResult(messages []llm.Message, toolUseID string, raw json.RawMessage, isError bool) (llm.ToolResultMessage, error) {
	if toolUseID == "" {
		return llm.ToolResultMessage{}, errors.New("tool_result requires tool_use_id")
	}
	name := findToolNameByToolUseID(messages, toolUseID)
	content, err := decodeAnthropicContent(raw)
	if err != nil {
		return llm.ToolResultMessage{}, err
	}
	if len(content) == 0 {
		content = []llm.Content{llm.TextContent{Text: ""}}
	}
	return llm.ToolResultMessage{
		ToolCallID:  toolUseID,
		ToolName:    name,
		Content:     content,
		IsError:     isError,
		TimestampMS: time.Now().UnixMilli(),
	}, nil
}

// decodeAnthropicContent 把原始 JSON 解码为 text / image 内容块。
func decodeAnthropicContent(raw json.RawMessage) ([]llm.Content, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []llm.Content{llm.TextContent{Text: ""}}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []llm.Content{llm.TextContent{Text: text}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("decode content: %w", err)
	}
	content := make([]llm.Content, 0, len(parts))
	for index, part := range parts {
		var header struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(part, &header); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", index, err)
		}
		switch header.Type {
		case "text":
			content = append(content, llm.TextContent{Text: header.Text})
		case "image":
			image, err := common.DecodeImagePart(part)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: %w", index, err)
			}
			content = append(content, image)
		default:
			continue
		}
	}
	return content, nil
}

// findToolNameByToolUseID 在前面 assistant 消息的工具调用中查找工具名。
func findToolNameByToolUseID(messages []llm.Message, toolUseID string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		assistant, ok := messages[i].(llm.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Content {
			call, ok := block.(llm.ToolCall)
			if ok && call.ID == toolUseID {
				return call.Name
			}
		}
	}
	return "tool"
}
