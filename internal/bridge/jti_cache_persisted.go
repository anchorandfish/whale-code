// Package bridge — M21-01 JtiCache 磁盘持久化（SNAPSHOT）
//
// 本文件为 JtiCache 增加磁盘持久化能力，重启后仍能检测重放。
// 对标同类 CLI workSecret 的持久化机制。
//
// 设计要点（候选 A 选定）：
// - 接口签名：JtiCachePersisted 包装 JtiCache，增加 Save/Load
// - 结构体字段：JtiCacheFile 含 Version/Entries/SavedAt
// - 分层权责：本文件只做"持久化"，重放检测仍由 JtiCache.IsReplayed
// - 降级策略：磁盘读写失败仅记录日志，不阻断重放检测（内存仍可用）
// - TTL规则：过期条目在 Save 时过滤，避免持久化无效数据
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// jtiCacheFile 是 JtiCache 持久化文件结构（SNAPSHOT-1）。
type jtiCacheFile struct {
	Version int              `json:"version"`  // 格式版本（当前 1）
	Entries map[string]int64 `json:"entries"`  // jti -> exp（Unix 秒）
	SavedAt int64            `json:"saved_at"` // 保存时间（Unix 秒）
}

// JtiCachePersisted 是带磁盘持久化的 JtiCache（SNAPSHOT-2）。
//
// 在 JtiCache 基础上增加：
// - Load：从磁盘加载历史 jti（重启后恢复防重放状态）
// - Save：将当前 jti 写入磁盘（供下次重启加载）
// - 定期 Save（由调用方触发，避免每次 IsReplayed 都写盘）
//
// 线程安全：内部复用 JtiCache 的 mutex，Save/Load 额外加持久化 mutex。
type JtiCachePersisted struct {
	*JtiCache
	persistPath string     // 持久化文件路径
	persistMu   sync.Mutex // 保护持久化读写
}

// NewJtiCachePersisted 创建带持久化的 JtiCache（SNAPSHOT-3）。
//
// 参数：
// - persistPath: 持久化文件路径（如 ~/.whale/bridge/jti_cache.json）
//
// persistPath 为空时退化为基础 JtiCache（无持久化，向后兼容）。
func NewJtiCachePersisted(persistPath string) *JtiCachePersisted {
	return &JtiCachePersisted{
		JtiCache:    NewJtiCache(),
		persistPath: persistPath,
	}
}

// Load 从磁盘加载历史 jti（SNAPSHOT-4）。
//
// 行为：
// - persistPath 为空时 no-op（向后兼容）
// - 文件不存在时 no-op（首次启动）
// - 文件损坏时返回 error（调用方决定是否继续）
// - 加载后合并到内存 cache（已过期的条目会被过滤）
func (c *JtiCachePersisted) Load() error {
	if c.persistPath == "" {
		return nil
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()

	data, err := os.ReadFile(c.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次启动，无文件
		}
		return fmt.Errorf("jti cache: load: %w", err)
	}

	var file jtiCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("jti cache: parse: %w", err)
	}

	// 合并到内存 cache（过滤已过期）
	now := time.Now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	for jti, exp := range file.Entries {
		if exp > 0 && now > exp {
			continue // 已过期，跳过
		}
		c.entries[jti] = exp
	}
	return nil
}

// Save 将当前 jti 写入磁盘（SNAPSHOT-5）。
//
// 行为：
// - persistPath 为空时 no-op（向后兼容）
// - 过滤已过期条目（避免持久化无效数据）
// - 原子写入（tempfile + rename，防中途崩溃损坏）
// - 目录自动创建
func (c *JtiCachePersisted) Save() error {
	if c.persistPath == "" {
		return nil
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()

	now := time.Now().Unix()
	c.mu.Lock()
	// 构造持久化数据（过滤已过期）
	entries := make(map[string]int64, len(c.entries))
	for jti, exp := range c.entries {
		if exp > 0 && now > exp {
			continue // 已过期，不持久化
		}
		entries[jti] = exp
	}
	c.mu.Unlock()

	file := jtiCacheFile{
		Version: 1,
		Entries: entries,
		SavedAt: now,
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("jti cache: marshal: %w", err)
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(c.persistPath), 0700); err != nil {
		return fmt.Errorf("jti cache: mkdir: %w", err)
	}

	// 原子写入：先写临时文件，再 rename
	tmpPath := c.persistPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("jti cache: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, c.persistPath); err != nil {
		return fmt.Errorf("jti cache: rename: %w", err)
	}
	return nil
}

// IsReplayedAndSave 是 IsReplayed + Save 的组合（SNAPSHOT-6）。
//
// 用于"检测+持久化"原子操作场景：
// - 先调 JtiCache.IsReplayed 检测
// - 非重放时立即 Save（确保新 jti 持久化）
// - Save 失败仅记录日志，不阻断（内存 cache 仍有效）
//
// 返回值与 IsReplayed 一致。
func (c *JtiCachePersisted) IsReplayedAndSave(jti string, exp int64) bool {
	replayed := c.IsReplayed(jti, exp)
	if !replayed {
		// 非重放，持久化新 jti（失败不阻断）
		_ = c.Save()
	}
	return replayed
}
