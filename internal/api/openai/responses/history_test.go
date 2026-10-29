package responses

import (
	"testing"

	"github.com/leookun/devin-2api/internal/llm"
)

func textUser(text string) llm.Message {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}

func textAssistant(text string) llm.Message {
	return llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}

// 空 id：必须逐字原样返回，且不调用 lookup。
func TestWithHistoryNoIDIsPassThrough(t *testing.T) {
	base := llm.RequestMessages{Model: "m", Messages: []llm.Message{textUser("only turn")}}
	got, resolution, err := WithHistory(base, "", "caller", func(string, string) ([]llm.Message, bool) {
		t.Fatal("lookup must not be called when previous_response_id is empty")
		return nil, false
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got.Messages))
	}
	if resolution.Injected || resolution.UnknownID {
		t.Fatalf("a no-op must not report injection or unknown id: %+v", resolution)
	}
}

// 兼容性优先：客户端已自带多轮历史时不注入，哪怕 id 有效。
func TestWithHistoryRespectsClientSuppliedHistory(t *testing.T) {
	base := llm.RequestMessages{Model: "m", Messages: []llm.Message{
		textUser("turn one"), textAssistant("answer one"), textUser("turn two"),
	}}
	got, resolution, err := WithHistory(base, "resp_1", "caller", func(string, string) ([]llm.Message, bool) {
		t.Fatal("lookup must not be called when the client already sent full history")
		return nil, false
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("client-supplied history must be left untouched, got %d messages", len(got.Messages))
	}
	if !resolution.ClientSuppliedFullHistory || resolution.Injected {
		t.Fatalf("expected ClientSuppliedFullHistory only, got %+v", resolution)
	}
}

// 真链式：单条消息 + 有效 id => 历史前置，本轮仍在末尾。
func TestWithHistoryPrependsForSingleTurn(t *testing.T) {
	base := llm.RequestMessages{Model: "m", Messages: []llm.Message{textUser("second question")}}
	history := []llm.Message{textUser("first question"), textAssistant("first answer")}

	got, resolution, err := WithHistory(base, "resp_1", "caller", func(caller, id string) ([]llm.Message, bool) {
		if caller != "caller" || id != "resp_1" {
			t.Fatalf("lookup got caller=%q id=%q", caller, id)
		}
		return history, true
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages (2 history + 1 new), got %d", len(got.Messages))
	}
	if got.Messages[0].Role() != llm.MessageRoleUser || got.Messages[1].Role() != llm.MessageRoleAssistant {
		t.Fatalf("history order wrong: %v", got.Messages)
	}
	if got.Messages[2].Role() != llm.MessageRoleUser {
		t.Fatalf("the new turn must stay last, got %v", got.Messages[2].Role())
	}
	if !resolution.Injected || resolution.HistoryCount != 2 {
		t.Fatalf("expected 2 injected messages, got %+v", resolution)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("merged context must validate: %v", err)
	}
}

// 未知 id 不报错（兼容性优先），但必须置位 UnknownID 让调用方可观测。
func TestWithHistoryUnknownIDIsObservableNotFatal(t *testing.T) {
	base := llm.RequestMessages{Model: "m", Messages: []llm.Message{textUser("q")}}
	got, resolution, err := WithHistory(base, "resp_missing", "caller", func(string, string) ([]llm.Message, bool) {
		return nil, false
	})
	if err != nil {
		t.Fatalf("an unknown id must NOT break a previously working request: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected the original single message, got %d", len(got.Messages))
	}
	if !resolution.UnknownID {
		t.Fatal("an unknown id must be reported through the resolution for logging")
	}
}

// 有 id 但没配 store：同样不报错，置位 UnknownID。
func TestWithHistoryMissingStoreIsObservable(t *testing.T) {
	base := llm.RequestMessages{Model: "m", Messages: []llm.Message{textUser("q")}}
	_, resolution, err := WithHistory(base, "resp_1", "caller", nil)
	if err != nil {
		t.Fatalf("missing store must not fail the request: %v", err)
	}
	if !resolution.UnknownID {
		t.Fatal("expected UnknownID when no store is configured")
	}
}
