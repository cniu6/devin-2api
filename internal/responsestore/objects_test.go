package responsestore

import (
	"testing"

	"github.com/leookun/devin-2api/internal/llm"
)

func TestPutResponseIndexesItemsAndDeletes(t *testing.T) {
	store := New(0, 0)
	messages := []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "q"}}}}
	store.PutResponse("a", "resp_1", messages, []byte(`{"id":"resp_1","output":[{"id":"msg_1","type":"message"}]}`))
	if _, ok := store.GetResponse("a", "resp_1"); !ok {
		t.Fatal("response not stored")
	}
	if _, ok := store.GetItem("b", "msg_1"); ok {
		t.Fatal("items must be caller-scoped")
	}
	if raw, ok := store.GetItem("a", "msg_1"); !ok || string(raw) != `{"id":"msg_1","type":"message"}` {
		t.Fatalf("item = %s", raw)
	}
	if store.Delete("b", "resp_1") || store.Delete("a", "item:msg_1") {
		t.Fatal("cross-caller delete or item-key delete must fail")
	}
	if !store.Delete("a", "resp_1") {
		t.Fatal("delete failed")
	}
	if _, ok := store.GetItem("a", "msg_1"); ok {
		t.Fatal("items must be removed with their response")
	}
}
