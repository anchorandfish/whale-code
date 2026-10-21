package bridge

import (
	"sync"
	"time"
)

// JtiCache 是 JWT jti 的内存去重 cache（线程安全，）。
// 防重放：JWT jti claim 已用则拒绝，TTL = exp（过期自动清理）。
// 对标 RFC 7519 §4.1.7 jti 建议，超越同类 CLI 的"无应用层防重放"。
type JtiCache struct {
	mu      sync.Mutex
	entries map[string]int64 // jti -> exp（Unix 秒）
}

// NewJtiCache 创建空的 jti 去重 cache。
func NewJtiCache() *JtiCache {
	return &JtiCache{entries: make(map[string]int64)}
}

// IsReplayed 检查 jti 是否已被使用（重放检测）。
// 若 jti 已在 cache 中且未过期，返回 true（重放）。
// 若 jti 未用过或已过期，返回 false 并记录 jti（TTL = exp）。
// exp <= 0 时视为永久（不清理，但不建议）。
// 惰性清理：每次调用时清理已过期条目，避免独立 goroutine。
func (c *JtiCache) IsReplayed(jti string, exp int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix()
	// 惰性清理过期条目
	for k, v := range c.entries {
		if v > 0 && now > v {
			delete(c.entries, k)
		}
	}
	// 检查重放
	if cachedExp, ok := c.entries[jti]; ok {
		if cachedExp <= 0 || now <= cachedExp {
			return true // 重放
		}
		// 已过期，删除旧条目
		delete(c.entries, jti)
	}
	// 记录新 jti
	c.entries[jti] = exp
	return false
}

// Size 返回当前 cache 条目数（测试用）。
func (c *JtiCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
