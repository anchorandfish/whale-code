// Package tools — 文件缓存磁盘持久化层（T1-P1-B4）
//
// DiskStore 提供磁盘 LRU 缓存，对标同类 CLI @2026-03-31 本地快照架构：
// - 磁盘内容存储：~/.whale/cache/files/<sha256-prefix-2>/<sha256> 单文件（按 SHA256 前两字符分桶）
// - 内存索引：map[path]*DiskIndexEntry 含 mtime/size/sha256/diskPath/lastAccess
// - LRU 容量上限：MaxDiskBytes（字节数）+ MaxDiskEntries（条目数）双重上限
// - TTL：单条 entry 可选 TTL，过期等同 miss
// - 跨平台路径：filepath.Join + filepath.ToSlash 自动处理 Windows 路径分隔符
//
// 设计原则：
// - 任何磁盘错误静默降级到 miss，永不阻塞 Read（对标 file_cache.go readFull 设计）
// - 原子写入：复用 util.AtomicWriteFileNoFsync 防止进程崩溃损坏
// - 与现有内存 LRU（FileContentCache.entries）协同：内存 miss → 磁盘 hit → 三段式检测
package tools

import (
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// DiskSyncMode 同步模式枚举。
type DiskSyncMode int

const (
	// SyncOnWrite 写入即同步落盘（强一致，慢）。默认模式。
	SyncOnWrite DiskSyncMode = iota
	// SyncOnEvict 仅 LRU 淘汰时落盘（弱一致，快）。
	SyncOnEvict
	// Async 异步批量落盘（最终一致，最快）。当前实现与 SyncOnEvict 等价。
	Async
)

// DiskCacheConfig 磁盘缓存配置（扩展 FileCacheConfig，向后兼容）。
// REF-外部-同类 CLI @2026-03-31 - 磁盘 LRU 架构
// REF-外部-Go os.UserHomeDir https://pkg.go.dev/os#UserHomeDir @2026-07-20
// REF-外部-Go path/filepath https://pkg.go.dev/path/filepath @2026-07-20
type DiskCacheConfig struct {
	// Enabled 是否启用磁盘内容缓存。默认 false，需显式启用。
	Enabled bool `json:"enabled"`
	// RootDir 缓存根目录。空表示 ~/.whale/cache/files/。
	RootDir string `json:"root_dir"`
	// MaxDiskBytes 磁盘容量上限（默认 256MB）。
	MaxDiskBytes int64 `json:"max_disk_bytes"`
	// MaxDiskEntries 磁盘条目上限（默认 1000）。
	MaxDiskEntries int `json:"max_disk_entries"`
	// TTL 单条 TTL。0 = 永不过期；默认 7 天。
	TTL time.Duration `json:"ttl"`
	// SyncMode 同步模式。默认 SyncOnWrite（强一致）。
	SyncMode DiskSyncMode `json:"sync_mode"`
	// VerifyOnRead 启用读取时 SHA256 校验。默认 false（性能优先）。
	// 启用时 Get 多读一次磁盘文件做 hash 校验，约 +5ms 延迟。
	VerifyOnRead bool `json:"verify_on_read"`
}

// DiskIndexEntry 磁盘缓存索引条目（仅元数据，文件内容存磁盘文件）。
type DiskIndexEntry struct {
	Path       string    `json:"path"`        // 原始文件绝对路径（cache key）
	SHA256     string    `json:"sha256"`      // 文件内容 SHA256
	Size       int64     `json:"size"`        // 原文件大小
	ModTime    time.Time `json:"mod_time"`    // 原文件 mtime
	LineCount  int       `json:"line_count"`  // 行数
	TokenEst   int       `json:"token_est"`   // token 估算
	DiskPath   string    `json:"disk_path"`   // 磁盘缓存文件相对路径（相对 RootDir）
	DiskSize   int64     `json:"disk_size"`   // 磁盘缓存文件字节数
	CreatedAt  time.Time `json:"created_at"`  // 缓存创建时间
	LastAccess time.Time `json:"last_access"` // 最近访问时间（LRU 依据）
	HitCount   int       `json:"hit_count"`   // 命中次数
}

// DiskCacheContent 磁盘缓存返回的完整内容。
// 吸纳审查优化 3：包含 Entry 字段供三段式检测复用元数据。
type DiskCacheContent struct {
	Entry   *DiskIndexEntry // 索引元数据（mtime/size/sha256）
	Content string          // 文件内容
}

// DiskCacheMetrics 磁盘缓存指标。
type DiskCacheMetrics struct {
	DiskHits        int   `json:"disk_hits"`        // 磁盘命中数
	DiskMisses      int   `json:"disk_misses"`      // 磁盘未命中数
	DiskWrites      int   `json:"disk_writes"`      // 磁盘写入数
	DiskEvictions   int   `json:"disk_evictions"`   // LRU 淘汰数
	DiskBytes       int64 `json:"disk_bytes"`       // 当前磁盘缓存字节数
	DiskEntryCount  int   `json:"disk_entry_count"` // 当前磁盘缓存条目数
	CorruptionCount int   `json:"corruption_count"` // SHA256 校验失败次数（吸纳优化 4）
}

// DiskStore 磁盘 LRU 缓存存储。
// 纯 Go 实现，无 cgo 依赖，对标同类 CLI @2026-03-31 文件系统 + 内存索引架构。
type DiskStore struct {
	config  DiskCacheConfig
	rootDir string                     // 归一化后的根目录
	index   map[string]*DiskIndexEntry // 内存索引：path → entry
	mu      sync.Mutex
	metrics *DiskCacheMetrics
}

// persistedDiskIndex 索引持久化 JSON 结构。
type persistedDiskIndex struct {
	Version int                        `json:"version"`
	Entries map[string]*DiskIndexEntry `json:"entries"`
}

// DefaultDiskCacheConfig 返回默认磁盘缓存配置。
// REF-外部-同类 CLI @2026-03-31 - 默认 256MB / 1000 条目 / 7 天 TTL
func DefaultDiskCacheConfig() DiskCacheConfig {
	return DiskCacheConfig{
		Enabled:        false,              // 默认禁用，需显式启用
		RootDir:        "",                 // 空表示 ~/.whale/cache/files/
		MaxDiskBytes:   256 * 1024 * 1024,  // 256MB
		MaxDiskEntries: 1000,               // 1000 条目
		TTL:            7 * 24 * time.Hour, // 7 天
		SyncMode:       SyncOnWrite,        // 强一致默认
		VerifyOnRead:   false,              // 性能优先
	}
}

// NewDiskStore 创建磁盘缓存存储。
// 自动调用 RestoreIndex 恢复索引 + cleanupOrphans 清理孤儿文件。
// 失败时返回 error，调用方应静默降级为仅内存模式。
func NewDiskStore(config DiskCacheConfig) (*DiskStore, error) {
	rootDir := config.RootDir
	if rootDir == "" {
		// 收口：Dir() 返回空（home 不可得）⇒ 报错降级，行为同前。
		if whalehome.Dir() == "" {
			return nil, fmt.Errorf("home directory unavailable")
		}
		rootDir = filepath.Join(whalehome.Dir(), "cache", "files")
	}
	rootDir = filepath.Clean(rootDir)

	// 创建根目录（含父目录）
	if err := os.MkdirAll(rootDir, 0700); err != nil {
		return nil, err
	}

	s := &DiskStore{
		config:  config,
		rootDir: rootDir,
		index:   make(map[string]*DiskIndexEntry),
		metrics: &DiskCacheMetrics{},
	}

	// 恢复索引（失败静默降级为空索引）
	_ = s.RestoreIndex()

	// 清理孤儿文件（失败静默忽略）
	s.cleanupOrphans()

	return s, nil
}

// diskPathForSHA256 根据 SHA256 计算磁盘存储相对路径。
// 格式：<sha256-prefix-2>/<sha256>，对标同类 CLI @2026-03-31 分桶，避免单目录文件数过多。
// REF-外部-同类 CLI @2026-03-31 - SHA256 分桶架构
func diskPathForSHA256(sha256 string) string {
	if len(sha256) < 2 {
		// 异常 SHA256（过短），归到 others 桶
		return filepath.Join("others", sha256)
	}
	return filepath.Join(sha256[:2], sha256)
}

// resolveDiskPath 将相对 DiskPath 解析为绝对路径。
func (s *DiskStore) resolveDiskPath(diskPath string) string {
	return filepath.Join(s.rootDir, diskPath)
}

// Get 读取磁盘缓存条目（含文件内容）。
// 吸纳审查优化 3：返回 DiskCacheContent 含 Entry 元数据。
// 吸纳审查优化 6：VerifyOnRead=true 时多读一次校验 SHA256。
// 吸纳审查优化 4：SHA256 不匹配时删除磁盘文件 + 删除索引 + 返回 miss。
func (s *DiskStore) Get(path string) (*DiskCacheContent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.index[path]
	if !exists {
		s.metrics.DiskMisses++
		return nil, false
	}

	// TTL 过期检查
	if s.config.TTL > 0 && time.Since(entry.CreatedAt) > s.config.TTL {
		s.removeFromDiskLocked(path)
		s.metrics.DiskMisses++
		return nil, false
	}

	// 读取磁盘文件
	absDiskPath := s.resolveDiskPath(entry.DiskPath)
	data, err := os.ReadFile(absDiskPath)
	if err != nil {
		// 文件丢失/损坏，删索引
		s.removeFromDiskLocked(path)
		s.metrics.DiskMisses++
		return nil, false
	}

	// SHA256 校验（吸纳优化 6，默认关闭）
	if s.config.VerifyOnRead {
		actualHash := computeSHA256(data)
		if actualHash != entry.SHA256 {
			// 吸纳优化 4：损坏处理
			s.removeFromDiskLocked(path)
			s.metrics.CorruptionCount++
			s.metrics.DiskMisses++
			return nil, false
		}
	}

	// 更新访问时间 + 命中计数
	entry.LastAccess = time.Now()
	entry.HitCount++
	s.metrics.DiskHits++

	return &DiskCacheContent{
		Entry:   entry,
		Content: string(data),
	}, true
}

// Put 写入磁盘缓存条目（含文件内容）。
// 流程：1) 计算 diskPath → 2) AtomicWriteFileNoFsync 写文件 → 3) 更新索引 → 4) Evict
// 失败时静默忽略 + 触发 LRU Evict + 重试 1 次（吸纳审查优化 4）。
func (s *DiskStore) Put(path string, content []byte, sha256 string, size int64, mtime time.Time, lineCount, tokenEst int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	diskPath := diskPathForSHA256(sha256)
	absDiskPath := s.resolveDiskPath(diskPath)

	// 写入磁盘文件（原子写入，无 fsync 提升性能）
	if err := util.AtomicWriteFileNoFsync(absDiskPath, content, 0600); err != nil {
		// 吸纳优化 4：失败时触发 Evict + 重试 1 次
		s.evictLocked()
		if err2 := util.AtomicWriteFileNoFsync(absDiskPath, content, 0600); err2 != nil {
			return err2
		}
	}

	// 更新索引
	now := time.Now()
	oldEntry, hasOld := s.index[path]
	if hasOld {
		// 旧条目磁盘文件可能不同（SHA256 变化），删除旧文件
		if oldEntry.DiskPath != diskPath {
			oldAbsPath := s.resolveDiskPath(oldEntry.DiskPath)
			// 静默忽略删除错误（孤儿 GC 兜底）
			_ = os.Remove(oldAbsPath)
			s.metrics.DiskBytes -= oldEntry.DiskSize
		} else {
			// 同 diskPath，更新字节数差值
			s.metrics.DiskBytes -= oldEntry.DiskSize
		}
	}

	s.index[path] = &DiskIndexEntry{
		Path:       path,
		SHA256:     sha256,
		Size:       size,
		ModTime:    mtime,
		LineCount:  lineCount,
		TokenEst:   tokenEst,
		DiskPath:   diskPath,
		DiskSize:   int64(len(content)),
		CreatedAt:  now,
		LastAccess: now,
		HitCount:   0,
	}
	s.metrics.DiskBytes += int64(len(content))
	s.metrics.DiskEntryCount = len(s.index)
	s.metrics.DiskWrites++

	// LRU 淘汰
	s.evictLocked()

	// 修复：SyncOnWrite 模式每次 Put 持久化索引（强一致默认）。
	// SyncOnEvict/Async 模式仅淘汰时持久化（已在 evictLocked 中处理）。
	if s.config.SyncMode == SyncOnWrite {
		_ = s.persistIndexLocked()
	}

	return nil
}

// Delete 删除磁盘缓存条目（含磁盘文件）。
// 索引删除失败静默忽略；磁盘文件删除失败静默忽略（孤儿 GC 兜底）。
func (s *DiskStore) Delete(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeFromDiskLocked(path)
	return nil
}

// DeleteByPrefix 按路径前缀批量删除。
// 返回删除条目数。
func (s *DiskStore) DeleteByPrefix(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	// 收集要删除的 path（避免遍历时修改 map）
	var toDelete []string
	for path := range s.index {
		if startsWithPath(path, prefix) {
			toDelete = append(toDelete, path)
		}
	}
	for _, path := range toDelete {
		s.removeFromDiskLocked(path)
		removed++
	}
	return removed
}

// startsWithPath 跨平台路径前缀匹配。
// 同时处理 / 与 \ 分隔符，避免 Windows 路径匹配失败。
func startsWithPath(path, prefix string) bool {
	// 归一化两端为斜杠风格比较
	normPath := filepath.ToSlash(path)
	normPrefix := filepath.ToSlash(prefix)
	if len(normPath) < len(normPrefix) {
		return false
	}
	if normPath[:len(normPrefix)] != normPrefix {
		return false
	}
	// 完整前缀匹配或前缀以 / 结尾或 path 在前缀后是 /
	if len(normPrefix) == 0 ||
		normPrefix[len(normPrefix)-1] == '/' ||
		len(normPath) == len(normPrefix) ||
		normPath[len(normPrefix)] == '/' {
		return true
	}
	return false
}

// Evict 执行 LRU 淘汰（按 MaxDiskBytes / MaxDiskEntries 上限）。
// 淘汰策略：低 HitCount + 旧 LastAccess 优先（与现有 evictLocked savings 权重一致思路）。
// 返回淘汰条目数。
func (s *DiskStore) Evict() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.index)
	s.evictLocked()
	return before - len(s.index)
}

// evictLocked 执行 LRU 淘汰（调用方持锁）。
// SyncOnEvict/Async 模式下，发生淘汰后持久化索引（弱一致语义）。
func (s *DiskStore) evictLocked() {
	evicted := false
	// 条目数上限
	for len(s.index) > s.config.MaxDiskEntries {
		s.evictOneLocked()
		evicted = true
	}
	// 字节数上限
	for s.metrics.DiskBytes > s.config.MaxDiskBytes && len(s.index) > 0 {
		s.evictOneLocked()
		evicted = true
	}
	// SyncOnEvict/Async 模式：淘汰后持久化索引
	if evicted && (s.config.SyncMode == SyncOnEvict || s.config.SyncMode == Async) {
		_ = s.persistIndexLocked()
	}
}

// evictOneLocked 淘汰一个最不活跃的条目（低 HitCount + 旧 LastAccess 优先）。
// 与现有 FileContentCache.evictLocked 思路一致：低 savings 优先 + 旧 LastAccess 优先。
func (s *DiskStore) evictOneLocked() {
	var victim string
	var victimEntry *DiskIndexEntry
	for path, entry := range s.index {
		if victimEntry == nil {
			victim, victimEntry = path, entry
			continue
		}
		// 低 HitCount 优先（<3 视为冷数据，对标 evictLocked 的 savings<100 判定）
		victimCold := victimEntry.HitCount < 3
		entryCold := entry.HitCount < 3
		if entryCold && !victimCold {
			victim, victimEntry = path, entry
			continue
		}
		if !entryCold && victimCold {
			continue
		}
		// 同冷热度下 LastAccess 旧者优先
		if entry.LastAccess.Before(victimEntry.LastAccess) {
			victim, victimEntry = path, entry
		}
	}
	if victim != "" {
		s.removeFromDiskLocked(victim)
		s.metrics.DiskEvictions++
	}
}

// removeFromDiskLocked 删除索引条目 + 磁盘文件（调用方持锁）。
// 静默忽略磁盘文件删除错误（孤儿 GC 兜底）。
func (s *DiskStore) removeFromDiskLocked(path string) {
	entry, exists := s.index[path]
	if !exists {
		return
	}
	absDiskPath := s.resolveDiskPath(entry.DiskPath)
	_ = os.Remove(absDiskPath)
	s.metrics.DiskBytes -= entry.DiskSize
	if s.metrics.DiskBytes < 0 {
		s.metrics.DiskBytes = 0
	}
	delete(s.index, path)
	s.metrics.DiskEntryCount = len(s.index)
}

// PersistIndex 持久化索引到 <RootDir>/index.json。
// 使用 AtomicWriteFileNoFsync（索引非关键数据，可重建）。
func (s *DiskStore) PersistIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistIndexLocked()
}

// persistIndexLocked 持久化索引（调用方持锁）。
func (s *DiskStore) persistIndexLocked() error {
	indexPath := filepath.Join(s.rootDir, "index.json")
	data := persistedDiskIndex{
		Version: 1,
		Entries: s.index,
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWriteFileNoFsync(indexPath, jsonData, 0600)
}

// RestoreIndex 从 <RootDir>/index.json 恢复索引。
// JSON 损坏时静默忽略 + 重建空索引（吸纳审查优化 4）。
// 恢复后校验磁盘文件存在性，不存在则删除索引条目。
func (s *DiskStore) RestoreIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	indexPath := filepath.Join(s.rootDir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil // 文件不存在正常
	}

	var persisted persistedDiskIndex
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil // JSON 损坏静默忽略，重建空索引
	}

	// 校验磁盘文件存在性 + TTL 过期检查
	for path, entry := range persisted.Entries {
		absDiskPath := s.resolveDiskPath(entry.DiskPath)
		info, err := os.Stat(absDiskPath)
		if err != nil {
			continue // 磁盘文件丢失，跳过此条目
		}
		// TTL 过期检查
		if s.config.TTL > 0 && time.Since(entry.CreatedAt) > s.config.TTL {
			_ = os.Remove(absDiskPath) // 删除过期文件
			continue
		}
		// 修正 DiskSize 为实际文件大小（防索引与实际不一致）
		entry.DiskSize = info.Size()
		s.index[path] = entry
		s.metrics.DiskBytes += entry.DiskSize
	}
	s.metrics.DiskEntryCount = len(s.index)
	return nil
}

// cleanupOrphans 清理孤儿文件（启动时调用）。
// 扫描 <RootDir> 下所有 <sha256-prefix-2>/<sha256> 文件，不在索引中的删除。
// 静默忽略所有错误（GC 是 best-effort）。
func (s *DiskStore) cleanupOrphans() int {
	// 收集索引中所有 DiskPath
	s.mu.Lock()
	indexed := make(map[string]bool, len(s.index))
	for _, entry := range s.index {
		indexed[entry.DiskPath] = true
	}
	s.mu.Unlock()

	removed := 0
	// 扫描根目录下所有 <prefix>/<sha256> 文件
	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		return 0
	}
	for _, prefixEntry := range entries {
		if !prefixEntry.IsDir() {
			continue // 跳过 index.json 等非分桶文件
		}
		prefix := prefixEntry.Name()
		prefixDir := filepath.Join(s.rootDir, prefix)
		subEntries, err := os.ReadDir(prefixDir)
		if err != nil {
			continue
		}
		for _, fileEntry := range subEntries {
			if fileEntry.IsDir() {
				continue
			}
			// 相对路径，使用 ToSlash 保证跨平台一致
			relPath := filepath.ToSlash(filepath.Join(prefix, fileEntry.Name()))
			if !indexed[relPath] {
				if err := os.Remove(filepath.Join(s.rootDir, relPath)); err == nil {
					removed++
				}
			}
		}
	}
	return removed
}

// Close 关闭磁盘存储。
// SyncMode=SyncOnEvict/Async 时持久化索引；SyncOnWrite 时已实时持久化无需再写。
func (s *DiskStore) Close() error {
	if s.config.SyncMode == SyncOnEvict || s.config.SyncMode == Async {
		return s.PersistIndex()
	}
	return nil
}

// Metrics 返回磁盘缓存指标快照。
func (s *DiskStore) Metrics() DiskCacheMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.metrics
}

// entryPaths 返回所有索引 path（按 LastAccess 升序），用于调试与测试。
func (s *DiskStore) entryPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	type pathTime struct {
		path   string
		access time.Time
	}
	var items []pathTime
	for path, entry := range s.index {
		items = append(items, pathTime{path, entry.LastAccess})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].access.Before(items[j].access)
	})
	result := make([]string, len(items))
	for i, item := range items {
		result[i] = item.path
	}
	return result
}
