// ask_cache.go — T3 S3 提问缓存。
// 5 分钟 TTL 防止 AI 在同一对话中重复问相同问题（REF-降级策略-优化3）。
// 缓存 key = category + ":" + sha256(question)[:8]（防同问题不同 category 共享答案）。
// v0.2 边界：不持久化（进程内 map 即可，v0.3 再加）。
package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// AskCacheTTLDefault 默认缓存 TTL（5 分钟，REF-降级策略-优化3）。
const AskCacheTTLDefault = 5 * time.Minute

// AskCacheEntry 提问缓存条目（REF-T3-S3-新增）。
// Answer：缓存的答案；ExpiresAt：过期时间（time.Now().Add(TTL)）。
type AskCacheEntry struct {
	Answer    tools.ClarifyAnswer
	ExpiresAt time.Time
}

// AskCache 提问缓存（REF-T3-S3-新增，5 分钟 TTL 防同问题重复问）。
// 并发安全：Bubbletea Update 是单线程的，但 Get/Set 仍加锁防 future use 跨 goroutine。
type AskCache struct {
	mu      sync.RWMutex
	entries map[string]AskCacheEntry // key: category + ":" + sha256(question)[:8]
	ttl     time.Duration
}

// NewAskCache 创建提问缓存实例（REF-T3-S3-新增）。
// 默认 TTL 5 分钟（可通过 SetTTL 修改，主要用于测试）。
func NewAskCache() *AskCache {
	return &AskCache{
		entries: make(map[string]AskCacheEntry),
		ttl:     AskCacheTTLDefault,
	}
}

// SetTTL 修改缓存 TTL（主要用于测试场景，REF-T3-S3-新增）。
// 必须在 Get/Set 调用前设置，避免并发问题。
func (c *AskCache) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}

// cacheKey 生成缓存 key（REF-降级策略-优化3：category + sha256(question)[:8]）。
// sha256 截断前 8 字符（4 字节）足以防日常对话场景的 question 碰撞。
func (c *AskCache) cacheKey(category, question string) string {
	h := sha256.Sum256([]byte(question))
	return category + ":" + hex.EncodeToString(h[:])[:8]
}

// Get 查询缓存（REF-T3-S3-新增）。
// 命中且未过期时返回 (answer, true)；否则返回 (零值, false)。
// 注意：命中后不会自动延长 TTL（5 分钟固定窗口）。
func (c *AskCache) Get(category, question string) (tools.ClarifyAnswer, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[c.cacheKey(category, question)]
	if !ok || time.Now().After(entry.ExpiresAt) {
		return tools.ClarifyAnswer{}, false
	}
	return entry.Answer, true
}

// Set 写入缓存（REF-T3-S3-新增）。
// 自动设置过期时间 time.Now().Add(ttl)。
func (c *AskCache) Set(category, question string, answer tools.ClarifyAnswer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[c.cacheKey(category, question)] = AskCacheEntry{
		Answer:    answer,
		ExpiresAt: time.Now().Add(c.ttl),
	}
}

// Cleanup 清理过期条目（REF-T3-S3-新增）。
// 建议在每轮对话结束时调用，避免缓存无限增长。
func (c *AskCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.ExpiresAt) {
			delete(c.entries, k)
		}
	}
}

// Size 返回当前缓存条目数（含过期未清理，主要用于测试）。
func (c *AskCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
