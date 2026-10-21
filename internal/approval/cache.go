package approval

import (
	"sync"
	"time"
)

// ApprovalDecision 审批决策结果。
type ApprovalDecision int

const (
	// DecisionAllow 允许执行（可来自缓存或用户确认）。
	DecisionAllow ApprovalDecision = 0
	// DecisionDeny 拒绝执行。
	DecisionDeny ApprovalDecision = 1
	// DecisionAsk 需要暂停并向用户询问。
	DecisionAsk ApprovalDecision = 2
)

// cacheEntry 缓存条目，记录决策与写入时间。
type cacheEntry struct {
	decision ApprovalDecision
	at       time.Time
}

// Cache 审批缓存（LRU + TTL）。
//
// 通过 toolName + inputHash 组合作为键缓存审批决策，
// 实现 "AllowAlways" 免再问语义。超过 maxEntries 时按 LRU 淘汰最旧条目；
// 超过 ttl 的条目视为过期，Get 时返回 ok=false。
type Cache struct {
	mu         sync.Mutex
	items      map[string]cacheEntry
	order      []string // LRU 顺序，order[0] 为最旧，order[len-1] 为最新
	maxEntries int
	ttl        time.Duration
}

// NewCache 创建审批缓存，maxEntries 为最大条目数，ttl 为单条有效期。
// maxEntries <= 0 时不做容量淘汰（仅 TTL 过期）。
func NewCache(maxEntries int, ttl time.Duration) *Cache {
	return &Cache{
		items:      make(map[string]cacheEntry),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

// Get 查询缓存。inputHash 通常为 sha256(input) 的十六进制串。
// 命中且未过期返回 (decision, true)，否则返回 (0, false)。
func (c *Cache) Get(toolName, inputHash string) (ApprovalDecision, bool) {
	key := cacheKey(toolName, inputHash)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[key]
	if !ok {
		return 0, false
	}
	if c.ttl > 0 && time.Since(entry.at) >= c.ttl {
		// 过期：惰性删除
		c.removeLocked(key)
		return 0, false
	}
	// 命中：提升为最新使用
	c.touchLocked(key)
	return entry.decision, true
}

// Set 写入缓存。超过 maxEntries 时淘汰最旧条目。
func (c *Cache) Set(toolName, inputHash string, decision ApprovalDecision) {
	key := cacheKey(toolName, inputHash)

	c.mu.Lock()
	defer c.mu.Unlock()

	// 已存在则更新并提升
	if _, ok := c.items[key]; ok {
		c.items[key] = cacheEntry{decision: decision, at: time.Now()}
		c.touchLocked(key)
		return
	}

	// 容量淘汰
	for c.maxEntries > 0 && len(c.items) >= c.maxEntries && len(c.order) > 0 {
		c.removeLocked(c.order[0])
	}

	c.items[key] = cacheEntry{decision: decision, at: time.Now()}
	c.order = append(c.order, key)
}

// Clear 清空全部缓存。
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheEntry)
	c.order = nil
}

// removeLocked 删除指定 key（调用方持锁）。
func (c *Cache) removeLocked(key string) {
	delete(c.items, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// touchLocked 将指定 key 提升至 LRU 队尾（最新使用）（调用方持锁）。
func (c *Cache) touchLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, key)
			break
		}
	}
}

// cacheKey 生成缓存键，组合 toolName 与 inputHash 以避免不同工具同名输入冲突。
func cacheKey(toolName, inputHash string) string {
	return toolName + "\x00" + inputHash
}
