// 本文件在会话快照之外保存完整 Response 对象与其 output item 索引，
// 支撑 GET/DELETE /v1/responses/{id} 与 input 中的 item_reference。
package responsestore

import (
	"encoding/json"

	"github.com/leookun/devin-2api/internal/llm"
)

// itemKeyPrefix 把 output item 与响应条目隔离在同一张 LRU 表的不同命名空间。
const itemKeyPrefix = "item:"

// PutResponse 保存会话快照、完整 Response 对象，并为 output 中每个带 id 的 item 建索引。
func (store *Store) PutResponse(caller, id string, messages []llm.Message, response []byte) {
	if id == "" || len(messages) == 0 {
		return
	}
	store.PutWithID(caller, id, messages)
	var projection struct {
		Output []json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(response, &projection)
	keys := make([]string, 0, len(projection.Output))
	for _, raw := range projection.Output {
		var item struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &item) != nil || item.ID == "" {
			continue
		}
		keys = append(keys, itemKeyPrefix+item.ID)
		store.putRaw(caller, itemKeyPrefix+item.ID, append([]byte(nil), raw...))
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if element, ok := store.items[id]; ok {
		stored := element.Value.(*entry)
		stored.response = append([]byte(nil), response...)
		stored.itemKeys = keys
	}
}

func (store *Store) putRaw(caller, key string, raw []byte) {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	store.evictExpiredLocked(now)
	if element, exists := store.items[key]; exists {
		stored := element.Value.(*entry)
		stored.caller, stored.raw, stored.expires = caller, raw, now.Add(store.ttl)
		store.order.MoveToFront(element)
		return
	}
	store.items[key] = store.order.PushFront(&entry{key: key, caller: caller, raw: raw, expires: now.Add(store.ttl)})
	for store.order.Len() > store.maxSize {
		store.removeLocked(store.order.Back())
	}
}

// lookupLocked 取回未过期且属于 caller 的条目；跨租户与不存在同样表现为查不到。
func (store *Store) lookupLocked(caller, key string) *entry {
	element, exists := store.items[key]
	if !exists {
		return nil
	}
	stored := element.Value.(*entry)
	if store.now().After(stored.expires) {
		store.removeLocked(element)
		return nil
	}
	if stored.caller != caller {
		return nil
	}
	store.order.MoveToFront(element)
	return stored
}

// GetResponse 返回已保存的完整 Response 对象 JSON。
func (store *Store) GetResponse(caller, id string) ([]byte, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored := store.lookupLocked(caller, id)
	if stored == nil || len(stored.response) == 0 {
		return nil, false
	}
	return append([]byte(nil), stored.response...), true
}

// GetItem 返回某个 output item 的原始 JSON，供 item_reference 展开。
func (store *Store) GetItem(caller, itemID string) ([]byte, bool) {
	if itemID == "" {
		return nil, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	stored := store.lookupLocked(caller, itemKeyPrefix+itemID)
	if stored == nil || len(stored.raw) == 0 {
		return nil, false
	}
	return append([]byte(nil), stored.raw...), true
}

// Delete 删除一个响应及其 item 索引；不存在或属于其他 caller 时返回 false。
func (store *Store) Delete(caller, id string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored := store.lookupLocked(caller, id)
	if stored == nil || len(stored.raw) > 0 {
		return false
	}
	for _, key := range stored.itemKeys {
		if element, ok := store.items[key]; ok && element.Value.(*entry).caller == caller {
			store.removeLocked(element)
		}
	}
	store.removeLocked(store.items[id])
	return true
}
