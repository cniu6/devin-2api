package responses

import (
	"encoding/json"
	"testing"

	"github.com/leookun/devin-2api/internal/llm"
)

// decodeStore 解出 DecodeRequest 得到的 Store 值。
func decodeStore(t *testing.T, body string) bool {
	t.Helper()
	adapted, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return adapted.Options.Store
}

// OpenAI 契约：store 缺省为 true。
func TestDecodeRequestStoreDefaultsToTrue(t *testing.T) {
	if !decodeStore(t, `{"model":"m","input":"hi"}`) {
		t.Fatal("an omitted store field must default to true")
	}
}

// 显式 false 必须被解析出来；用指针正是为了把"未提供"与"false"区分开，
// 否则两者都会落到零值 false，缺省语义就被破坏了。
func TestDecodeRequestStoreExplicitFalse(t *testing.T) {
	if decodeStore(t, `{"model":"m","input":"hi","store":false}`) {
		t.Fatal("store:false must decode to false")
	}
}

func TestDecodeRequestStoreExplicitTrue(t *testing.T) {
	if !decodeStore(t, `{"model":"m","input":"hi","store":true}`) {
		t.Fatal("store:true must decode to true")
	}
}

// encodeStore 取出编码后响应体里的 store 字段。
func encodeStore(t *testing.T, message *llm.AssistantMessage, store ...bool) bool {
	t.Helper()
	body, err := EncodeResponse(message, store...)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var projection struct {
		Store *bool `json:"store"`
	}
	if err := json.Unmarshal(body, &projection); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if projection.Store == nil {
		t.Fatal("response body must expose a store field")
	}
	return *projection.Store
}

func okMessage() *llm.AssistantMessage {
	return &llm.AssistantMessage{
		ResponseID:    "resp_upstream",
		ResponseModel: "test-model",
		Content:       []llm.Content{llm.TextContent{Text: "hi"}},
		StopReason:    llm.StopReasonStop,
	}
}

// 非流式响应必须如实回显：恒定 true 会让写了 store:false 的调用方
// 以为会话被保留，而后续引用必然落空。
func TestEncodeResponseEchoesStoreFalse(t *testing.T) {
	if encodeStore(t, okMessage(), false) {
		t.Fatal("store:false must be echoed as false")
	}
}

func TestEncodeResponseEchoesStoreTrue(t *testing.T) {
	if !encodeStore(t, okMessage(), true) {
		t.Fatal("store:true must be echoed as true")
	}
}

// 既有两参调用点必须保持原语义（默认为 true），否则所有现有调用方都会静默变化。
func TestEncodeResponseWithoutStoreArgKeepsTrue(t *testing.T) {
	if !encodeStore(t, okMessage()) {
		t.Fatal("a two-argument EncodeResponse call must keep echoing true")
	}
}

// streamStore 从流式事件里取出所有出现过的 store 值。
func streamStore(t *testing.T, store bool) []bool {
	t.Helper()
	encoder := NewStreamEncoder("test-model")
	encoder.SetStore(store)
	var seen []bool
	events := []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: okMessage()},
	}
	for _, event := range events {
		encoded, err := encoder.Encode(event)
		if err != nil {
			t.Fatalf("Encode(%v): %v", event.Type, err)
		}
		for _, sse := range encoded {
			var frame struct {
				Response struct {
					Store *bool `json:"store"`
				} `json:"response"`
			}
			if err := json.Unmarshal(sse.Data, &frame); err != nil {
				continue
			}
			if frame.Response.Store != nil {
				seen = append(seen, *frame.Response.Store)
			}
		}
	}
	return seen
}

// 流式的 created 与 completed 两个事件都必须回显真实值。
func TestStreamEncoderEchoesStoreFalse(t *testing.T) {
	seen := streamStore(t, false)
	if len(seen) == 0 {
		t.Fatal("stream events never exposed a store field")
	}
	for _, value := range seen {
		if value {
			t.Fatal("stream echoed store=true for a store:false request")
		}
	}
}

func TestStreamEncoderEchoesStoreTrue(t *testing.T) {
	seen := streamStore(t, true)
	if len(seen) == 0 {
		t.Fatal("stream events never exposed a store field")
	}
	for _, value := range seen {
		if !value {
			t.Fatal("stream echoed store=false for a store:true request")
		}
	}
}

// NewStreamEncoder 不调用 SetStore 时保持 true，保证既有调用点不受影响。
func TestNewStreamEncoderDefaultsToStoreTrue(t *testing.T) {
	encoder := NewStreamEncoder("test-model")
	if !encoder.store {
		t.Fatal("a fresh StreamEncoder must default to store=true")
	}
}
