package responsestore

import (
	"testing"
	"time"

	"github.com/leookun/devin-2api/internal/llm"
)

func userMsg(text string) llm.Message {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}

func TestPutGetRoundTrip(t *testing.T) {
	store := New(time.Minute, 10)
	id, err := store.Put("caller-a", []llm.Message{userMsg("hello")})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty id")
	}
	got, ok := store.Get("caller-a", id)
	if !ok {
		t.Fatal("expected the entry to resolve for its own caller")
	}
	if len(got) != 1 || got[0].Role() != llm.MessageRoleUser {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

// 跨租户必须是 fail-closed：A 的 id 不能为 B 解析成功，否则可重放读到他人会话。
func TestCrossCallerIsRejected(t *testing.T) {
	store := New(time.Minute, 10)
	id, _ := store.Put("caller-a", []llm.Message{userMsg("secret")})
	if _, ok := store.Get("caller-b", id); ok {
		t.Fatal("caller-b must NOT resolve caller-a's id")
	}
	// 且越权尝试不得销毁原主的会话。
	if _, ok := store.Get("caller-a", id); !ok {
		t.Fatal("a cross-caller lookup must not evict the legitimate owner's entry")
	}
}

// 存的是快照：调用方事后修改自己的切片不得串改已存记录。
func TestSnapshotIsCopied(t *testing.T) {
	store := New(time.Minute, 10)
	messages := []llm.Message{userMsg("first")}
	id, _ := store.Put("c", messages)
	messages[0] = userMsg("mutated")
	got, ok := store.Get("c", id)
	if !ok {
		t.Fatal("expected hit")
	}
	if got[0].(llm.UserMessage).Content[0].(llm.TextContent).Text != "first" {
		t.Fatal("stored snapshot was mutated through the caller's slice")
	}
}

func TestExpiry(t *testing.T) {
	store := New(time.Minute, 10)
	base := time.Now()
	store.now = func() time.Time { return base }
	id, _ := store.Put("c", []llm.Message{userMsg("x")})

	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, ok := store.Get("c", id); ok {
		t.Fatal("expired entry must not resolve")
	}
}

func TestMaxSizeEvictsOldest(t *testing.T) {
	store := New(time.Hour, 2)
	first, _ := store.Put("c", []llm.Message{userMsg("1")})
	second, _ := store.Put("c", []llm.Message{userMsg("2")})
	third, _ := store.Put("c", []llm.Message{userMsg("3")})

	if store.Len() != 2 {
		t.Fatalf("expected cap at 2, got %d", store.Len())
	}
	if _, ok := store.Get("c", first); ok {
		t.Fatal("the oldest entry should have been evicted")
	}
	if _, ok := store.Get("c", second); !ok {
		t.Fatal("second entry should survive")
	}
	if _, ok := store.Get("c", third); !ok {
		t.Fatal("third entry should survive")
	}
}

// 空 id / 空消息集不落库，避免污染表。
func TestEmptyInputsIgnored(t *testing.T) {
	store := New(time.Minute, 10)
	store.PutWithID("c", "", []llm.Message{userMsg("x")})
	store.PutWithID("c", "resp_x", nil)
	if store.Len() != 0 {
		t.Fatalf("expected empty table, got %d", store.Len())
	}
}
