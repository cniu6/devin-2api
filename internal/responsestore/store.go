// Package responsestore 保存 Responses API 的服务端会话状态。
//
// 背景（这是本模块存在的唯一理由）：`/v1/responses` 的招牌能力是「服务端持有会话」——
// 客户端只发本轮新消息 + previous_response_id，服务端补齐其之前的全部历史。
//
// 实测缺陷：改造前 DecodeRequest 会解析 PreviousResponseID 并透传进 RequestOptions，
// 但全仓没有任何消费点（grep 只命中定义与赋值）。于是链式客户端每轮只发出**一条**
// 消息，模型每轮盲答：不报错、不告警，产出流利但无上下文的回答，调用方无从诊断。
//
// 设计取舍：
//   - 每条记录存**完整累积消息列表**，不存父指针。查找是 O(1)，且中途驱逐不会
//     静默截断历史——要么整条命中，要么查不到（fail-closed）。
//   - 记录按 callerKey 隔离。租户 A 的 id 绝不能为租户 B 解析成功，否则可用重放
//     读到他人会话。
//   - 有界：TTL + 硬上限 + LRU 驱逐，防止单个调用方把内存吃满或冲掉他人会话。
//   - store:false 的请求不落库（OpenAI 契约），这类响应之后不能用作 previous_response_id。
package responsestore

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/leookun/devin-2api/internal/llm"
)

// Defaults 是内存表的安全上限，不是性能目标。
const (
	DefaultTTL     = 30 * time.Minute
	DefaultMaxSize = 2048
)

// entry 是一条已保存的会话快照。
type entry struct {
	key      string
	caller   string
	messages []llm.Message
	expires  time.Time
}

// Store 是有界的 Responses 会话存储，并发安全。
type Store struct {
	mu      sync.Mutex
	items   map[string]*list.Element
	order   *list.List // 头部最近使用，尾部最旧
	ttl     time.Duration
	maxSize int
	now     func() time.Time // 便于测试注入
}

// New 构造一个 Store；ttl<=0 或 maxSize<=0 时取默认值。
func New(ttl time.Duration, maxSize int) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	return &Store{
		items:   make(map[string]*list.Element),
		order:   list.New(),
		ttl:     ttl,
		maxSize: maxSize,
		now:     time.Now,
	}
}

// NewID 生成一个新的响应标识。用随机串而非计数器：id 会被回传给客户端，
// 可预测的序号会让「猜 id 读别人会话」从不可能变成容易。
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, randErr := rand.Read(buf); randErr != nil {
		return "", fmt.Errorf("generate response id: %w", randErr)
	}
	return "resp_" + hex.EncodeToString(buf), nil
}

// Put 保存一条会话快照并返回其标识。
//
// messages 会被整体拷贝进一条新切片，避免调用方后续 append 影响到已存记录
// （底层数组共享是这类「存快照」最常见的静默串改来源）。
func (store *Store) Put(caller string, messages []llm.Message) (string, error) {
	id, err := NewID()
	if err != nil {
		return "", err
	}
	store.PutWithID(caller, id, messages)
	return id, nil
}

// PutWithID 用指定标识保存快照；已存在则覆盖。
func (store *Store) PutWithID(caller, id string, messages []llm.Message) {
	if id == "" || len(messages) == 0 {
		return
	}
	snapshot := make([]llm.Message, len(messages))
	copy(snapshot, messages)

	store.mu.Lock()
	defer store.mu.Unlock()

	now := store.now()
	store.evictExpiredLocked(now)

	if element, exists := store.items[id]; exists {
		element.Value.(*entry).caller = caller
		element.Value.(*entry).messages = snapshot
		element.Value.(*entry).expires = now.Add(store.ttl)
		store.order.MoveToFront(element)
		return
	}
	element := store.order.PushFront(&entry{
		key:      id,
		caller:   caller,
		messages: snapshot,
		expires:  now.Add(store.ttl),
	})
	store.items[id] = element

	for store.order.Len() > store.maxSize {
		oldest := store.order.Back()
		if oldest == nil {
			break
		}
		store.removeLocked(oldest)
	}
}

// Get 取回某个 caller 的会话快照。
//
// 「id 存在但属于别的 caller」与「id 不存在」对客户端都表现为查不到（fail-closed）；
// 前者不驱逐该条目——另一租户被误发 id 不应销毁其会话。
func (store *Store) Get(caller, id string) ([]llm.Message, bool) {
	if id == "" {
		return nil, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	element, exists := store.items[id]
	if !exists {
		return nil, false
	}
	stored := element.Value.(*entry)
	if store.now().After(stored.expires) {
		store.removeLocked(element)
		return nil, false
	}
	if stored.caller != caller {
		// 不驱逐：另一租户的 id 被误发不应销毁其会话。
		return nil, false
	}
	store.order.MoveToFront(element)
	snapshot := make([]llm.Message, len(stored.messages))
	copy(snapshot, stored.messages)
	return snapshot, true
}

// Len 返回当前条目数（导出给测试与诊断）。
func (store *Store) Len() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.order.Len()
}

func (store *Store) evictExpiredLocked(now time.Time) {
	for element := store.order.Back(); element != nil; {
		previous := element.Prev()
		if now.After(element.Value.(*entry).expires) {
			store.removeLocked(element)
		}
		element = previous
	}
}

func (store *Store) removeLocked(element *list.Element) {
	store.order.Remove(element)
	delete(store.items, element.Value.(*entry).key)
}
