// 本文件定义三种 API 协议在 app 层的统一适配边界。
package app

import (
	"fmt"

	"github.com/leookun/devin-2api/internal/api/anthropic/messages"
	"github.com/leookun/devin-2api/internal/api/openai/chat"
	"github.com/leookun/devin-2api/internal/api/openai/responses"
	"github.com/leookun/devin-2api/internal/debuglog"
	"github.com/leookun/devin-2api/internal/llm"
)

// protocolEncoder 抽象流式与非流式协议编码。
type protocolEncoder interface {
	// NewStreamEncoder 创建与本次 HTTP 请求绑定的流式编码器。
	NewStreamEncoder(model string, includeUsage bool) streamEncoder
	// EncodeFinal 把最终助手消息编码为完整的非流式 JSON 响应体。
	EncodeFinal(message *llm.AssistantMessage) ([]byte, error)
	// SSEFormat 把单个 SSE 事件格式化为可写入客户端的字节。
	SSEFormat(name string, data []byte) []byte
}

// streamEncoder 抽象三种协议共有的中间事件编码。
type streamEncoder interface {
	Encode(event llm.ResponseEvent) ([]eventWire, error)
}

type eventWire struct {
	Name string
	Data []byte
}

// protocolOptions 保存三个协议都需要的生成控制选项。
type protocolOptions struct {
	Stream       bool
	IncludeUsage bool
	// Store 只有 Responses 协议会填：false 表示调用方拒绝服务端保留会话。
	Store bool
}

// responsesProtocol 实现 OpenAI Responses API 协议。
type responsesProtocol struct{}

func (p responsesProtocol) NewStreamEncoder(model string, _ bool) streamEncoder {
	return &responsesStreamAdapter{responses.NewStreamEncoder(model)}
}

func (p responsesProtocol) EncodeFinal(message *llm.AssistantMessage) ([]byte, error) {
	return responses.EncodeResponse(message)
}

// EncodeFinalWithStore 编码非流式响应，并如实回显 store。
func (p responsesProtocol) EncodeFinalWithStore(message *llm.AssistantMessage, store bool) ([]byte, error) {
	return responses.EncodeResponse(message, store)
}

// NewResponsesStreamEncoder 创建流式编码器，并把真实的 store 值传给它。
func NewResponsesStreamEncoder(model string, store bool) streamEncoder {
	encoder := responses.NewStreamEncoder(model)
	encoder.SetStore(store)
	return &responsesStreamAdapter{encoder}
}

func (p responsesProtocol) SSEFormat(name string, data []byte) []byte {
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", name, data)
}

// chatProtocol 实现 OpenAI Chat Completions 协议。
type chatProtocol struct{}

func (p chatProtocol) NewStreamEncoder(model string, includeUsage bool) streamEncoder {
	return &chatStreamAdapter{chat.NewStreamEncoder(model, includeUsage)}
}

func (p chatProtocol) EncodeFinal(message *llm.AssistantMessage) ([]byte, error) {
	return chat.EncodeResponse(message)
}

func (p chatProtocol) SSEFormat(name string, data []byte) []byte {
	// OpenAI Chat Completions 使用 data-only SSE；[DONE] 作为流终止标记。
	if name == "[DONE]" {
		return []byte("data: [DONE]\n\n")
	}
	return fmt.Appendf(nil, "data: %s\n\n", data)
}

// anthropicProtocol 实现 Anthropic Messages 协议。
type anthropicProtocol struct{}

func (p anthropicProtocol) NewStreamEncoder(model string, _ bool) streamEncoder {
	return &anthropicStreamAdapter{messages.NewStreamEncoder(model)}
}

func (p anthropicProtocol) EncodeFinal(message *llm.AssistantMessage) ([]byte, error) {
	return messages.EncodeResponse(message)
}

func (p anthropicProtocol) SSEFormat(name string, data []byte) []byte {
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", name, data)
}

type responsesStreamAdapter struct{ encoder *responses.StreamEncoder }

func (a *responsesStreamAdapter) Encode(event llm.ResponseEvent) ([]eventWire, error) {
	raw, err := a.encoder.Encode(event)
	if err != nil {
		return nil, err
	}
	out := make([]eventWire, 0, len(raw))
	for _, e := range raw {
		out = append(out, eventWire{Name: e.Name, Data: e.Data})
	}
	return out, nil
}

type chatStreamAdapter struct{ encoder *chat.StreamEncoder }

func (a *chatStreamAdapter) Encode(event llm.ResponseEvent) ([]eventWire, error) {
	raw, err := a.encoder.Encode(event)
	if err != nil {
		return nil, err
	}
	out := make([]eventWire, 0, len(raw))
	for _, e := range raw {
		out = append(out, eventWire{Name: e.Name, Data: e.Data})
	}
	return out, nil
}

type anthropicStreamAdapter struct{ encoder *messages.StreamEncoder }

func (a *anthropicStreamAdapter) Encode(event llm.ResponseEvent) ([]eventWire, error) {
	raw, err := a.encoder.Encode(event)
	if err != nil {
		return nil, err
	}
	out := make([]eventWire, 0, len(raw))
	for _, e := range raw {
		out = append(out, eventWire{Name: e.Name, Data: e.Data})
	}
	return out, nil
}

// decodeRequest 把具体协议的解码结果统一为中间请求和公共选项。
type decodeRequestFunc func([]byte) (llm.RequestMessages, protocolOptions, error)

func decodeResponsesRequest(data []byte) (llm.RequestMessages, protocolOptions, error) {
	adapted, err := responses.DecodeRequest(data)
	if err != nil {
		return llm.RequestMessages{}, protocolOptions{}, err
	}
	return adapted.Context, protocolOptions{
		Stream:       adapted.Options.Stream,
		IncludeUsage: false,
		// 这是 store 从解码层走进 handler 的唯一通路；丢掉它会让
		// 调用方的 store:false 被静默忽略。
		Store: adapted.Options.Store,
	}, nil
}

func decodeChatRequest(data []byte) (llm.RequestMessages, protocolOptions, error) {
	adapted, err := chat.DecodeRequest(data)
	if err != nil {
		return llm.RequestMessages{}, protocolOptions{}, err
	}
	return adapted.Context, protocolOptions{
		Stream:       adapted.Options.Stream,
		IncludeUsage: adapted.Options.IncludeUsage,
	}, nil
}

func decodeAnthropicRequest(data []byte) (llm.RequestMessages, protocolOptions, error) {
	adapted, err := messages.DecodeRequest(data)
	if err != nil {
		return llm.RequestMessages{}, protocolOptions{}, err
	}
	return adapted.Context, protocolOptions{
		Stream:       adapted.Options.Stream,
		IncludeUsage: false,
	}, nil
}

// logRequestMessages 把中间请求消息投影为 JSON 调试日志。
func logRequestMessages(recorder *debuglog.Recorder, messages llm.RequestMessages) {
	if recorder == nil {
		return
	}
	recorder.WriteJSON("02-request-messages.json", debuglog.RequestMessagesProjection(messages))
}
