// Package github — / M19-05 review 结果文件哈希缓存
//
// 提供基于文件内容哈希的审查结果缓存：
// - 相同文件内容（SHA-256 哈希一致）跳过 AI 审查，直接返回缓存结果
// - 节约 AI 调用成本（重复审查相同文件不再付费）
// - 持久化到磁盘（~/.whale/cache/review_hashes.json），跨会话复用
//
// 设计原则（02-审查报告 §2.5 过度设计检查）：
// - 仅缓存 ReviewResult（不缓存 diff 等中间数据）
// - 持久化用 JSON（不引入 SQLite/BoltDB）
// - 不强制调用方使用哈希缓存（Reviewer 集成可选）
// - 并发安全（sync.RWMutex 保护 entries map）
//
// 对标 webpack cache 持久化模式
// [webpack.js.org/configuration/cache](https://webpack.js.org/configuration/cache/) @2026-07-21
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// defaultMaxHashCacheEntries 哈希缓存默认最大条目数（防止无限制增长）
// 超过时按 ReviewedAt 最旧优先淘汰（FIFO）
const defaultMaxHashCacheEntries = 1000

// HashCacheEntry 哈希缓存条目。
//
// key 为 fileHash（SHA-256），value 为审查结果 + 元数据。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存条目结构
type HashCacheEntry struct {
	FilePath     string       `json:"file_path"`     // 文件路径（仅记录，不参与 key）
	FileHash     string       `json:"file_hash"`     // 文件内容 SHA-256（参与 key）
	ReviewResult ReviewResult `json:"review_result"` // 审查结果（Summary + Findings + Metrics）
	ReviewedAt   time.Time    `json:"reviewed_at"`   // 审查时间（用于 TTL 判断）
}

// HashCache 文件哈希缓存。
//
// entries key 为 fileHash（SHA-256），value 为审查结果。
// 并发安全：sync.RWMutex 保护 entries map（Get 读锁 / Put 写锁）。
// 持久化：Save 写 JSON 文件 + Load 读 JSON 文件（原子写入：先写临时文件再 rename）。
//
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存结构
type HashCache struct {
	entries    map[string]HashCacheEntry // key: fileHash
	mu         sync.RWMutex
	path       string // 持久化路径（~/.whale/cache/review_hashes.json）
	maxEntries int    // 最大条目数（默认 1000，超过时 FIFO 淘汰）
}

// NewHashCache 创建文件哈希缓存。
//
// path 为持久化文件路径（如 ~/.whale/cache/review_hashes.json）。
// 首次调用不会自动 Load（调用方需显式调用 Load）。
// maxEntries <= 0 时使用默认值 defaultMaxHashCacheEntries。
//
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存构造
func NewHashCache(path string) *HashCache {
	return &HashCache{
		entries:    make(map[string]HashCacheEntry),
		path:       path,
		maxEntries: defaultMaxHashCacheEntries,
	}
}

// SetMaxEntries 设置最大条目数（<=0 使用默认值 1000）。
// 已有条目数超过新值时不立即淘汰（下次 Put 时淘汰）。
func (c *HashCache) SetMaxEntries(max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if max <= 0 {
		c.maxEntries = defaultMaxHashCacheEntries
	} else {
		c.maxEntries = max
	}
}

// ComputeFileHash 计算文件内容的 SHA-256 哈希。
//
// 输入为文件内容字节流，输出为 64 字符十六进制字符串。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希计算
func ComputeFileHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// Get 根据文件哈希查找缓存。
//
// 修复 B5-P1-13：原签名 Get(filePath, fileHash) 的 filePath 参数被忽略（死代码），
// 误导调用方以为会按路径+哈希双重校验。实际 entries 以 fileHash 为唯一 key。
// 现移除 filePath 参数，签名与实际查找逻辑一致。
//
// 命中返回 (*ReviewResult, true)，未命中返回 (nil, false)。
// 并发安全：读锁保护 entries map。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存查找
func (c *HashCache) Get(fileHash string) (*ReviewResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[fileHash]
	if !ok {
		return nil, false
	}
	// fileHash 匹配即命中（哈希一致即内容一致），FilePath 仅用于调试
	result := entry.ReviewResult
	return &result, true
}

// Put 存入审查结果到缓存。
//
// 修复 B5-P1-13：原实现永远返回 nil（无真实错误场景），签名声明 error 误导调用方。
// 现改为无返回值，与实际行为一致（map 赋值和 FIFO 淘汰均不会失败）。
//
// key 为 fileHash（SHA-256），value 为 ReviewResult + 元数据。
// 超过 maxEntries 时按 ReviewedAt 最旧优先淘汰（FIFO）。
// 并发安全：写锁保护 entries map。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存存入
func (c *HashCache) Put(filePath, fileHash string, result ReviewResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[fileHash] = HashCacheEntry{
		FilePath:     filePath,
		FileHash:     fileHash,
		ReviewResult: result,
		ReviewedAt:   time.Now(),
	}

	// 超过 maxEntries 时淘汰最旧条目（FIFO）
	if len(c.entries) > c.maxEntries {
		c.evictOldestLocked()
	}
}

// evictOldestLocked 淘汰最旧条目（需持写锁）。
// 遍历 entries 找 ReviewedAt 最早的条目删除。
func (c *HashCache) evictOldestLocked() {
	if len(c.entries) == 0 {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	first := true
	for key, entry := range c.entries {
		if first || entry.ReviewedAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.ReviewedAt
			first = false
		}
	}
	delete(c.entries, oldestKey)
}

// Save 持久化缓存到磁盘（原子写入：先写临时文件再 rename）。
//
// 文件路径为 c.path，权限 0600（仅用户可读写）。
// 失败返回 error（调用方应日志告警但不阻塞审查）。
// 并发安全：写锁保护 entries map（序列化期间）。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存持久化
func (c *HashCache) Save() error {
	if c.path == "" {
		return fmt.Errorf("hash cache path is empty")
	}

	c.mu.RLock()
	// 复制 entries 到局部变量（避免长时间持锁）
	entriesCopy := make(map[string]HashCacheEntry, len(c.entries))
	for k, v := range c.entries {
		entriesCopy[k] = v
	}
	c.mu.RUnlock()

	data, err := json.MarshalIndent(entriesCopy, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hash cache: %w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create hash cache dir: %w", err)
	}

	// 原子写入：先写临时文件再 rename
	tmpPath := c.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write hash cache tmp file: %w", err)
	}
	if err := os.Rename(tmpPath, c.path); err != nil {
		return fmt.Errorf("rename hash cache file: %w", err)
	}
	return nil
}

// Load 从磁盘加载缓存。
//
// 文件不存在时启动空缓存（首次运行，静默降级）。
// 文件解析失败时启动空缓存 + 返回 error（调用方应日志告警）。
// 并发安全：写锁保护 entries map（加载期间）。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存加载
func (c *HashCache) Load() error {
	if c.path == "" {
		return fmt.Errorf("hash cache path is empty")
	}

	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在是正常降级路径，不返回 error
			return nil
		}
		return fmt.Errorf("read hash cache file: %w", err)
	}

	var entries map[string]HashCacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal hash cache: %w", err)
	}

	c.mu.Lock()
	c.entries = entries
	if c.entries == nil {
		c.entries = make(map[string]HashCacheEntry)
	}
	c.mu.Unlock()
	return nil
}

// Clear 清空缓存（内存 + 磁盘）。
//
// 内存：清空 entries map。磁盘：删除缓存文件（如存在）。
// 并发安全：写锁保护 entries map。
// hash_cache.go @2026-07-21 SNAPSHOT: 哈希缓存清空
func (c *HashCache) Clear() error {
	c.mu.Lock()
	c.entries = make(map[string]HashCacheEntry)
	c.mu.Unlock()

	if c.path == "" {
		return nil
	}
	// 删除磁盘文件（不存在时返回 nil）
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove hash cache file: %w", err)
	}
	return nil
}

// Size 返回当前缓存条目数（并发安全）。
func (c *HashCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
