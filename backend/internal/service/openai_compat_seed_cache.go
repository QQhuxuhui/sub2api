package service

import (
	"container/list"
	"encoding/json"
	"sync"

	"github.com/gin-gonic/gin"
)

const (
	compatSeedCacheMaxEntries    = 1024
	compatSeedCacheMaxBytes      = 32 << 20
	compatSeedCacheMaxEntryBytes = 64 << 10

	openAISessionSeedMemoKey        = "openai_session_seed_memo"
	openAISessionSeedMemoMaxEntries = 16
	openAISessionSeedMemoMaxValues  = 64 << 20
)

// compatSeedCache 缓存 normalizeCompatSeedJSON 的结果。同一客户端的 tools 和系统提示
// 在每个请求里重复出现，规范化又是纯函数，用完整原文做 key 不会碰撞。
var compatSeedCache = newCompatSeedLRU(compatSeedCacheMaxEntries, compatSeedCacheMaxBytes, compatSeedCacheMaxEntryBytes)

type compatSeedLRUEntry struct {
	key   string
	value string
}

type compatSeedLRU struct {
	mu            sync.Mutex
	maxEntries    int
	maxBytes      int
	maxEntryBytes int
	bytes         int
	order         *list.List
	items         map[string]*list.Element
}

func newCompatSeedLRU(maxEntries, maxBytes, maxEntryBytes int) *compatSeedLRU {
	return &compatSeedLRU{
		maxEntries:    maxEntries,
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		order:         list.New(),
		items:         make(map[string]*list.Element),
	}
}

func (l *compatSeedLRU) get(raw json.RawMessage) (string, bool) {
	if len(raw) > l.maxEntryBytes {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	element, ok := l.items[string(raw)]
	if !ok {
		return "", false
	}
	l.order.MoveToFront(element)
	entry, _ := element.Value.(*compatSeedLRUEntry)
	return entry.value, true
}

func (l *compatSeedLRU) put(raw json.RawMessage, value string) {
	size := len(raw) + len(value)
	if size > l.maxEntryBytes {
		return
	}
	key := string(raw)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.items[key]; ok {
		return
	}
	l.items[key] = l.order.PushFront(&compatSeedLRUEntry{key: key, value: value})
	l.bytes += size
	for len(l.items) > l.maxEntries || l.bytes > l.maxBytes {
		oldest := l.order.Back()
		entry, _ := oldest.Value.(*compatSeedLRUEntry)
		l.order.Remove(oldest)
		delete(l.items, entry.key)
		l.bytes -= len(entry.key) + len(entry.value)
	}
}

// openAISessionSeedMemo 在一个请求内记住大片段（放不进 compatSeedCache 的图片、长历史）
// 的规范化结果，让 handler 和 Forward 的两次种子计算只做一次。只给单个请求的 goroutine
// 顺序使用。raw 是请求体的只读视图，不额外占内存；value 占内存，所以 Forward 入口就把
// memo 从 context 取走，避免等上游首字和长流期间继续保活。
type openAISessionSeedMemo struct {
	entries    []openAISessionSeedMemoEntry
	valueBytes int
}

type openAISessionSeedMemoEntry struct {
	raw   string
	value string
}

func (m *openAISessionSeedMemo) normalize(raw string) string {
	for i := range m.entries {
		if m.entries[i].raw == raw {
			return m.entries[i].value
		}
	}
	value := normalizeCompatSeedRaw(raw)
	if len(raw)+len(value) > compatSeedCacheMaxEntryBytes &&
		len(m.entries) < openAISessionSeedMemoMaxEntries &&
		m.valueBytes+len(value) <= openAISessionSeedMemoMaxValues {
		m.entries = append(m.entries, openAISessionSeedMemoEntry{raw: raw, value: value})
		m.valueBytes += len(value)
	}
	return value
}

// EnableOpenAISessionSeedMemo 为当前请求开启种子 memo，由 Responses 入口在首次计算
// 会话哈希之前调用。
func EnableOpenAISessionSeedMemo(c *gin.Context) {
	if c != nil {
		c.Set(openAISessionSeedMemoKey, &openAISessionSeedMemo{})
	}
}

func openAISessionSeedMemoFromContext(c *gin.Context) *openAISessionSeedMemo {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAISessionSeedMemoKey)
	if !ok {
		return nil
	}
	memo, _ := value.(*openAISessionSeedMemo)
	return memo
}

// takeOpenAISessionSeedMemo 取走 memo 并让 context 不再持有它，之后的
// GenerateSessionHash 不会再填充。
func takeOpenAISessionSeedMemo(c *gin.Context) *openAISessionSeedMemo {
	memo := openAISessionSeedMemoFromContext(c)
	if memo != nil {
		c.Set(openAISessionSeedMemoKey, (*openAISessionSeedMemo)(nil))
	}
	return memo
}
