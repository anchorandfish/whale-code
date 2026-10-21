// Package tools — file content cache for reducing repeated file read tokens
//
// FileContentCache provides a three-stage detection mechanism to avoid sending
// unchanged file content to the LLM:
// - Stage 1 (fast): mtime + size unchanged → UNCHANGED
// - Stage 2 (slow): SHA256 hash comparison → UNCHANGED if content identical
// - Stage 3 (diff): content changed → DIFF if small change, FULL if large change
//
// Design principles:
// - Any cache error MUST silently degrade to FULL mode (never block reads)
// - Write operations invalidate cache entries proactively
// - Content is memory-only; only metadata is persisted to disk
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/diff"
	"github.com/anchorandfish/whale-code/internal/util"
)

// CacheStatus represents the cache read status
type CacheStatus string

const (
	CacheFull      CacheStatus = "full"      // first read / cache miss, return full content
	CacheUnchanged CacheStatus = "unchanged" // file unchanged, return marker
	CacheDiff      CacheStatus = "diff"      // file changed, return diff
	CacheError     CacheStatus = "error"     // read failed, fallback to no-cache mode
)

// FileCacheEntry represents a single file cache entry
type FileCacheEntry struct {
	// --- file identity ---
	Path   string `json:"path"`   // absolute path (cache key)
	SHA256 string `json:"sha256"` // file content SHA256 hash

	// --- file metadata (for fast change detection) ---
	Size    int64     `json:"size"`     // file size in bytes
	ModTime time.Time `json:"mod_time"` // file modification time

	// --- cached content ---
	Content   string `json:"-"`          // full file content (memory only, not persisted)
	LineCount int    `json:"line_count"` // line count
	TokenEst  int    `json:"token_est"`  // estimated token count

	// --- cache statistics ---
	ReadCount     int       `json:"read_count"`      // total read count
	LastReadAt    time.Time `json:"last_read_at"`    // last read time
	CacheHitCount int       `json:"cache_hit_count"` // cache hit count (UNCHANGED + DIFF)
	TokensSaved   int64     `json:"tokens_saved"`    // cumulative tokens saved

	// --- status ---
	Status    CacheStatus `json:"status"`     // last read status
	CreatedAt time.Time   `json:"created_at"` // cache creation time
}

// CacheReadResult is the result of a cache-aware file read
type CacheReadResult struct {
	// --- returned content ---
	Content string      `json:"content"` // content to return to the model
	Status  CacheStatus `json:"status"`  // read status

	// --- statistics ---
	FilePath     string `json:"file_path"`
	FullTokens   int    `json:"full_tokens"`   // estimated tokens for full content
	ActualTokens int    `json:"actual_tokens"` // estimated tokens for actual returned content
	TokensSaved  int    `json:"tokens_saved"`  // tokens saved this read

	// --- diff details (only set when Status == CacheDiff) ---
	DiffStats *DiffStatsDetail `json:"diff_stats,omitempty"`
}

// DiffStatsDetail holds diff statistics
type DiffStatsDetail struct {
	AddedLines   int     `json:"added_lines"`
	RemovedLines int     `json:"removed_lines"`
	DiffRatio    float64 `json:"diff_ratio"` // changed lines / total lines
}

// FileCacheConfig holds file cache configuration
type FileCacheConfig struct {
	// --- feature toggle ---
	Enabled bool `json:"enabled"`

	// --- cache capacity ---
	MaxEntries  int `json:"max_entries"`   // max cache entries (default 200)
	MaxMemoryMB int `json:"max_memory_mb"` // max memory usage in MB (default 100)

	// --- diff strategy ---
	DiffThreshold    float64 `json:"diff_threshold"`     // diff/full < this → return diff (default 0.5)
	DiffContextLines int     `json:"diff_context_lines"` // diff context lines (default 3)

	// --- file filtering ---
	MaxFileSize  int64    `json:"max_file_size"` // skip caching files larger than this (default 1MB)
	SkipPatterns []string `json:"skip_patterns"` // skip cache for these patterns

	// --- persistence ---
	PersistToDisk bool   `json:"persist_to_disk"` // persist metadata to disk (default true)
	PersistPath   string `json:"persist_path"`    // persistence path (default ~/.whale/file-cache.json)

	// --- return format ---
	UnchangedMessage string `json:"unchanged_message"` // unchanged message template

	// --- disk LRU content cache ( T1-P1-B4) ---
	// 启用后文件内容持久化到 ~/.whale/cache/files/<sha256-prefix-2>/<sha256>，
	// 进程重启后内存 LRU miss 时查询磁盘 LRU，避免重新读原文件。
	// 与现有 PersistToDisk（元数据 JSON）正交，向后兼容。
	DiskCache DiskCacheConfig `json:"disk_cache"`
}

// DefaultFileCacheConfig returns default configuration
func DefaultFileCacheConfig() FileCacheConfig {
	return FileCacheConfig{
		Enabled:          true,
		MaxEntries:       200,
		MaxMemoryMB:      100,
		DiffThreshold:    0.5,
		DiffContextLines: 3,
		MaxFileSize:      1 * 1024 * 1024, // 1MB
		SkipPatterns: []string{
			".git/", "node_modules/", "__pycache__/", ".svn/",
			"vendor/", "dist/", "build/", ".next/",
		},
		PersistToDisk:    true,
		PersistPath:      "",
		UnchangedMessage: "[FILE_UNCHANGED: {{.Path}}, {{.LineCount}} lines, hash={{.Hash}}]",
		DiskCache:        DefaultDiskCacheConfig(),
	}
}

// FileContentCache is the file content cache
type FileContentCache struct {
	config    FileCacheConfig
	entries   map[string]*FileCacheEntry
	mu        sync.Mutex
	estimator *ai.TokenEstimator
	metrics   *FileCacheMetrics
	tmpl      *template.Template
	// diskStore 磁盘 LRU 内容缓存（T1-P1-B4）。
	// nil 时禁用磁盘缓存，仅内存模式（向后兼容）。
	// 启用时 Read 三级降级链：内存 LRU → 磁盘 LRU → 原始文件三段式检测。
	diskStore *DiskStore
}

// NewFileContentCache creates a new file content cache
func NewFileContentCache(config FileCacheConfig, estimator *ai.TokenEstimator) *FileContentCache {
	if estimator == nil {
		estimator = ai.NewTokenEstimator()
	}

	c := &FileContentCache{
		config:    config,
		entries:   make(map[string]*FileCacheEntry),
		estimator: estimator,
		metrics:   NewFileCacheMetrics(),
	}

	// parse unchanged message template
	if config.UnchangedMessage != "" {
		c.tmpl, _ = template.New("unchanged").Parse(config.UnchangedMessage)
	}

	// T1-P1-B4：启用磁盘 LRU 内容缓存。
	// 失败静默降级为仅内存模式（diskStore = nil），不影响现有功能。
	if config.DiskCache.Enabled {
		store, err := NewDiskStore(config.DiskCache)
		if err == nil {
			c.diskStore = store
		}
	}

	return c
}

// Metrics returns the cache metrics
func (c *FileContentCache) Metrics() *FileCacheMetrics {
	return c.metrics
}

// Read performs a cache-aware file read
// Returns CacheReadResult containing content to return to the model and statistics.
// Any cache error silently degrades to FULL mode.
func (c *FileContentCache) Read(path string) *CacheReadResult {
	absPath := filepath.Clean(path)

	// resolve symlinks for consistent cache keys
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = filepath.Clean(resolved)
	}

	// stage 0: pre-checks
	if !c.config.Enabled {
		return c.readFull(absPath)
	}

	if c.shouldSkip(absPath) {
		return c.readFull(absPath)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// stage 1: fast path — mtime + size detection
	stat, err := os.Stat(absPath)
	if err != nil {
		return c.readFullLocked(absPath)
	}

	// skip files that are too large
	if stat.Size() > c.config.MaxFileSize {
		return c.readFullLocked(absPath)
	}

	entry, exists := c.entries[absPath]

	// T1-P1-B4：三级降级链 —— 内存 LRU miss 时查询磁盘 LRU。
	// 磁盘 hit 时复用磁盘索引元数据 + 三段式检测，避免重新读原文件。
	if !exists && c.diskStore != nil {
		if result := c.readFromDiskLocked(absPath, stat); result != nil {
			return result
		}
	}

	if !exists {
		// first read: read full content + cache it
		return c.readAndCacheLocked(absPath, stat)
	}

	// fast path: mtime and size unchanged → likely unchanged
	if entry.ModTime.Equal(stat.ModTime()) && entry.Size == stat.Size() {
		entry.ReadCount++
		entry.CacheHitCount++
		entry.LastReadAt = time.Now()
		entry.TokensSaved += int64(entry.TokenEst)

		msg := c.formatUnchangedMessage(entry)
		c.metrics.recordRead(CacheUnchanged, entry.TokenEst, entry.TokenEst)

		return &CacheReadResult{
			Content:      msg,
			Status:       CacheUnchanged,
			FilePath:     absPath,
			FullTokens:   entry.TokenEst,
			ActualTokens: c.estimateTokens(msg),
			TokensSaved:  entry.TokenEst,
		}
	}

	// stage 2: slow path — content hash detection
	newContent, err := os.ReadFile(absPath)
	if err != nil {
		return c.readFullLocked(absPath)
	}

	newHash := computeSHA256(newContent)

	if newHash == entry.SHA256 {
		// mtime/size changed but content didn't (e.g. touch, git checkout same content)
		entry.ModTime = stat.ModTime()
		entry.Size = stat.Size()
		entry.ReadCount++
		entry.CacheHitCount++
		entry.LastReadAt = time.Now()
		entry.TokensSaved += int64(entry.TokenEst)

		msg := c.formatUnchangedMessage(entry)
		c.metrics.recordRead(CacheUnchanged, entry.TokenEst, entry.TokenEst)

		return &CacheReadResult{
			Content:      msg,
			Status:       CacheUnchanged,
			FilePath:     absPath,
			FullTokens:   entry.TokenEst,
			ActualTokens: c.estimateTokens(msg),
			TokensSaved:  entry.TokenEst,
		}
	}

	// stage 3: content changed — diff evaluation
	return c.handleChangedLocked(absPath, entry, newContent, newHash, stat)
}

// readFromDiskLocked 内存 LRU miss 时从磁盘 LRU 恢复（T1-P1-B4）。
// 必须在 c.mu 持有后调用；磁盘 hit 时复用磁盘索引元数据 + 三段式检测。
// 返回 nil 表示磁盘 miss，调用方应继续走 readAndCacheLocked。
// 吸纳审查优化 2、3：磁盘 hit 时复用磁盘索引 mtime/size/sha256 + 三段式检测。
func (c *FileContentCache) readFromDiskLocked(absPath string, stat os.FileInfo) *CacheReadResult {
	// 注意锁顺序：c.mu 已持有 → 进入 diskStore.mu，单向无环
	diskContent, ok := c.diskStore.Get(absPath)
	if !ok {
		return nil
	}

	diskEntry := diskContent.Entry
	now := time.Now()

	// Stage 1：mtime + size 检测，命中则恢复到内存 LRU 并返回 UNCHANGED
	if diskEntry.ModTime.Equal(stat.ModTime()) && diskEntry.Size == stat.Size() {
		memEntry := &FileCacheEntry{
			Path:          absPath,
			SHA256:        diskEntry.SHA256,
			Size:          diskEntry.Size,
			ModTime:       diskEntry.ModTime,
			Content:       diskContent.Content,
			LineCount:     diskEntry.LineCount,
			TokenEst:      diskEntry.TokenEst,
			ReadCount:     1,
			LastReadAt:    now,
			CacheHitCount: 1,
			TokensSaved:   int64(diskEntry.TokenEst),
			Status:        CacheUnchanged,
			CreatedAt:     now,
		}
		c.entries[absPath] = memEntry
		c.evictLocked()

		msg := c.formatUnchangedMessage(memEntry)
		c.metrics.recordRead(CacheUnchanged, memEntry.TokenEst, memEntry.TokenEst)
		return &CacheReadResult{
			Content:      msg,
			Status:       CacheUnchanged,
			FilePath:     absPath,
			FullTokens:   memEntry.TokenEst,
			ActualTokens: c.estimateTokens(msg),
			TokensSaved:  memEntry.TokenEst,
		}
	}

	// Stage 2：mtime/size 变 → SHA256 校验磁盘内容是否仍有效
	newContent, err := os.ReadFile(absPath)
	if err != nil {
		// 读原文件失败，但磁盘内容仍可用 → 返回磁盘内容作为 FULL（保证不阻塞）
		memEntry := &FileCacheEntry{
			Path:       absPath,
			SHA256:     diskEntry.SHA256,
			Size:       diskEntry.Size,
			ModTime:    diskEntry.ModTime,
			Content:    diskContent.Content,
			LineCount:  diskEntry.LineCount,
			TokenEst:   diskEntry.TokenEst,
			ReadCount:  1,
			LastReadAt: now,
			CreatedAt:  now,
			Status:     CacheFull,
		}
		c.entries[absPath] = memEntry
		c.evictLocked()
		c.metrics.recordRead(CacheFull, memEntry.TokenEst, 0)
		return &CacheReadResult{
			Content:      diskContent.Content,
			Status:       CacheFull,
			FilePath:     absPath,
			FullTokens:   memEntry.TokenEst,
			ActualTokens: memEntry.TokenEst,
			TokensSaved:  0,
		}
	}

	newHash := computeSHA256(newContent)

	// 磁盘内容 SHA256 与原文件一致 → 内容未变（mtime touch），恢复 UNCHANGED
	if newHash == diskEntry.SHA256 {
		memEntry := &FileCacheEntry{
			Path:          absPath,
			SHA256:        diskEntry.SHA256,
			Size:          stat.Size(),
			ModTime:       stat.ModTime(),
			Content:       diskContent.Content,
			LineCount:     diskEntry.LineCount,
			TokenEst:      diskEntry.TokenEst,
			ReadCount:     1,
			LastReadAt:    now,
			CacheHitCount: 1,
			TokensSaved:   int64(diskEntry.TokenEst),
			Status:        CacheUnchanged,
			CreatedAt:     now,
		}
		c.entries[absPath] = memEntry
		c.evictLocked()
		// 同步更新磁盘索引的 mtime/size（保持磁盘与原文件元数据一致）
		_ = c.diskStore.Put(absPath, []byte(diskContent.Content), diskEntry.SHA256, stat.Size(), stat.ModTime(), diskEntry.LineCount, diskEntry.TokenEst)

		msg := c.formatUnchangedMessage(memEntry)
		c.metrics.recordRead(CacheUnchanged, memEntry.TokenEst, memEntry.TokenEst)
		return &CacheReadResult{
			Content:      msg,
			Status:       CacheUnchanged,
			FilePath:     absPath,
			FullTokens:   memEntry.TokenEst,
			ActualTokens: c.estimateTokens(msg),
			TokensSaved:  memEntry.TokenEst,
		}
	}

	// Stage 3：内容变 → diff/full，复用磁盘内容作为"旧内容"做 diff
	memEntry := &FileCacheEntry{
		Path:       absPath,
		SHA256:     diskEntry.SHA256,
		Size:       diskEntry.Size,
		ModTime:    diskEntry.ModTime,
		Content:    diskContent.Content,
		LineCount:  diskEntry.LineCount,
		TokenEst:   diskEntry.TokenEst,
		ReadCount:  0,
		LastReadAt: now,
		CreatedAt:  now,
	}
	c.entries[absPath] = memEntry

	result := c.handleChangedLocked(absPath, memEntry, newContent, newHash, stat)

	// 同步更新磁盘缓存为新内容
	_ = c.diskStore.Put(absPath, newContent, newHash, stat.Size(), stat.ModTime(), memEntry.LineCount, memEntry.TokenEst)

	return result
}

// handleChangedLocked processes a file whose content has changed.
// Must be called with c.mu held.
func (c *FileContentCache) handleChangedLocked(
	absPath string,
	entry *FileCacheEntry,
	newContent []byte,
	newHash string,
	stat os.FileInfo,
) *CacheReadResult {
	newStr := string(newContent)
	newLines := strings.Split(newStr, "\n")
	newTokenEst := c.estimator.EstimateTextTokens(newStr)

	// generate diff
	change := diff.FileChange{Path: absPath, OldContent: entry.Content, NewContent: newStr}
	// PlainConfig：diffContent 会随读取结果回给模型与剪贴板，属数据层，不带上 ANSI。
	diffContent := diff.Render(change, diff.PlainConfig())
	added, removed := diff.CountChanges(change)
	changedLines := added + removed
	totalLines := len(newLines)
	if totalLines == 0 {
		totalLines = 1
	}
	diffRatio := float64(changedLines) / float64(totalLines)

	// update cache entry
	entry.Content = newStr
	entry.SHA256 = newHash
	entry.Size = stat.Size()
	entry.ModTime = stat.ModTime()
	entry.LineCount = totalLines
	entry.TokenEst = newTokenEst
	entry.ReadCount++
	entry.LastReadAt = time.Now()

	if diffRatio < c.config.DiffThreshold {
		// small change → return diff
		diffTokens := c.estimator.EstimateTextTokens(diffContent)
		saved := newTokenEst - diffTokens
		if saved < 0 {
			saved = 0
		}
		entry.TokensSaved += int64(saved)
		entry.CacheHitCount++

		msg := c.formatDiffMessage(absPath, diffContent)
		c.metrics.recordRead(CacheDiff, newTokenEst, saved)

		return &CacheReadResult{
			Content:      msg,
			Status:       CacheDiff,
			FilePath:     absPath,
			FullTokens:   newTokenEst,
			ActualTokens: diffTokens,
			TokensSaved:  saved,
			DiffStats: &DiffStatsDetail{
				AddedLines:   added,
				RemovedLines: removed,
				DiffRatio:    diffRatio,
			},
		}
	}

	// large change → return full content
	c.metrics.recordRead(CacheFull, newTokenEst, 0)

	return &CacheReadResult{
		Content:      newStr,
		Status:       CacheFull,
		FilePath:     absPath,
		FullTokens:   newTokenEst,
		ActualTokens: newTokenEst,
		TokensSaved:  0,
	}
}

// readFull reads the file without caching (fallback / disabled cache).
// Does NOT acquire c.mu.
func (c *FileContentCache) readFull(absPath string) *CacheReadResult {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return &CacheReadResult{
			Content:  "",
			Status:   CacheError,
			FilePath: absPath,
		}
	}
	content := string(data)
	tokens := c.estimateTokens(content)
	c.metrics.recordRead(CacheFull, tokens, 0)
	return &CacheReadResult{
		Content:      content,
		Status:       CacheFull,
		FilePath:     absPath,
		FullTokens:   tokens,
		ActualTokens: tokens,
		TokensSaved:  0,
	}
}

// readFullLocked is readFull but for use when c.mu is already held.
func (c *FileContentCache) readFullLocked(absPath string) *CacheReadResult {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return &CacheReadResult{
			Content:  "",
			Status:   CacheError,
			FilePath: absPath,
		}
	}
	content := string(data)
	tokens := c.estimateTokens(content)
	c.metrics.recordRead(CacheFull, tokens, 0)
	return &CacheReadResult{
		Content:      content,
		Status:       CacheFull,
		FilePath:     absPath,
		FullTokens:   tokens,
		ActualTokens: tokens,
		TokensSaved:  0,
	}
}

// readAndCacheLocked reads a file for the first time and caches it.
// Must be called with c.mu held.
func (c *FileContentCache) readAndCacheLocked(absPath string, stat os.FileInfo) *CacheReadResult {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return c.readFullLocked(absPath)
	}

	content := string(data)
	hash := computeSHA256(data)
	lines := strings.Split(content, "\n")
	tokenEst := c.estimator.EstimateTextTokens(content)

	now := time.Now()
	entry := &FileCacheEntry{
		Path:       absPath,
		SHA256:     hash,
		Size:       stat.Size(),
		ModTime:    stat.ModTime(),
		Content:    content,
		LineCount:  len(lines),
		TokenEst:   tokenEst,
		ReadCount:  1,
		LastReadAt: now,
		CreatedAt:  now,
		Status:     CacheFull,
	}

	c.entries[absPath] = entry
	c.evictLocked()

	// T1-P1-B4：同步写入磁盘 LRU 内容缓存。
	// 失败静默忽略（磁盘满 / 权限错误），不影响内存缓存功能。
	if c.diskStore != nil {
		_ = c.diskStore.Put(absPath, data, hash, stat.Size(), stat.ModTime(), len(lines), tokenEst)
	}

	c.metrics.recordRead(CacheFull, tokenEst, 0)

	return &CacheReadResult{
		Content:      content,
		Status:       CacheFull,
		FilePath:     absPath,
		FullTokens:   tokenEst,
		ActualTokens: tokenEst,
		TokensSaved:  0,
	}
}

// Invalidate invalidates the cache entry for the given path.
// Called after write_file / edit_file success.
func (c *FileContentCache) Invalidate(path string) {
	absPath := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = filepath.Clean(resolved)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[absPath]; exists {
		delete(c.entries, absPath)
		c.metrics.recordInvalidation()
	}

	// T1-P1-B4：同步删除磁盘 LRU 缓存条目。
	// 失败静默忽略（孤儿 GC 兜底）。
	if c.diskStore != nil {
		_ = c.diskStore.Delete(absPath)
	}
}

// InvalidateAll invalidates all cache entries.
// Called after git checkout / git reset etc.
func (c *FileContentCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := len(c.entries)

	// T1-P1-B4：清空前收集 path 用于磁盘缓存清理。
	// 注意：磁盘 Delete 会获取 diskStore.mu，锁顺序 c.mu → diskStore.mu 单向无环。
	if c.diskStore != nil {
		for path := range c.entries {
			_ = c.diskStore.Delete(path)
		}
	}

	c.entries = make(map[string]*FileCacheEntry)
	c.metrics.addInvalidations(count)
}

// InvalidateByPrefix invalidates all cache entries under the given directory prefix.
// 与 Read 的缓存键保持一致的归一化：Clean + EvalSymlinks，避免 Windows 8.3 短名
// 与长名不一致导致前缀匹配失败（预存在 bug 修复）。
func (c *FileContentCache) InvalidateByPrefix(prefix string) {
	absPrefix := filepath.Clean(prefix)
	// 与 Read 的缓存键归一化保持一致：解析符号链接
	if resolved, err := filepath.EvalSymlinks(absPrefix); err == nil {
		absPrefix = filepath.Clean(resolved)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	for path := range c.entries {
		if strings.HasPrefix(path, absPrefix) {
			delete(c.entries, path)
			removed++
		}
	}
	c.metrics.addInvalidations(removed)

	// T1-P1-B4：同步删除磁盘 LRU 缓存条目（按前缀批量）。
	if c.diskStore != nil {
		c.diskStore.DeleteByPrefix(absPrefix)
	}
}

// shouldSkip checks if a file should skip caching (binary, too large, pattern match).
// Does NOT acquire c.mu.
func (c *FileContentCache) shouldSkip(absPath string) bool {
	// check skip patterns
	normalized := filepath.ToSlash(absPath)
	for _, pattern := range c.config.SkipPatterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}

	// check file size via stat
	if info, err := os.Stat(absPath); err == nil {
		if info.Size() > c.config.MaxFileSize {
			return true
		}
	}

	// check binary file
	if isBinaryFile(absPath) {
		return true
	}

	return false
}

// evictLocked performs LRU eviction.
// Must be called with c.mu held.
func (c *FileContentCache) evictLocked() {
	if len(c.entries) <= c.config.MaxEntries {
		return
	}

	type pathTime struct {
		path        string
		lastRead    time.Time
		tokensSaved int64
	}

	var candidates []pathTime
	for path, entry := range c.entries {
		candidates = append(candidates, pathTime{path, entry.LastReadAt, entry.TokensSaved})
	}

	// sort: evict low-savings + least-recently-read first
	sort.Slice(candidates, func(i, j int) bool {
		iSavings := candidates[i].tokensSaved
		jSavings := candidates[j].tokensSaved
		if iSavings < 100 && jSavings >= 100 {
			return true
		}
		if jSavings < 100 && iSavings >= 100 {
			return false
		}
		return candidates[i].lastRead.Before(candidates[j].lastRead)
	})

	// evict down to capacity (evict a few extra to avoid frequent triggers)
	evictCount := len(c.entries) - c.config.MaxEntries + 10
	if evictCount > len(candidates) {
		evictCount = len(candidates)
	}
	for i := 0; i < evictCount; i++ {
		delete(c.entries, candidates[i].path)
	}
}

// formatUnchangedMessage renders the UNCHANGED message using the template.
func (c *FileContentCache) formatUnchangedMessage(entry *FileCacheEntry) string {
	hashPrefix := entry.SHA256
	if len(hashPrefix) > 8 {
		hashPrefix = hashPrefix[:8]
	}

	data := struct {
		Path      string
		LineCount int
		Hash      string
	}{
		Path:      entry.Path,
		LineCount: entry.LineCount,
		Hash:      hashPrefix,
	}

	if c.tmpl != nil {
		var sb strings.Builder
		if err := c.tmpl.Execute(&sb, data); err == nil {
			return sb.String()
		}
	}

	return fmt.Sprintf("[FILE_UNCHANGED: %s, %d lines, hash=%s]", entry.Path, entry.LineCount, hashPrefix)
}

// formatDiffMessage formats the DIFF message with header and diff content.
func (c *FileContentCache) formatDiffMessage(absPath, diff string) string {
	return fmt.Sprintf("[FILE_MODIFIED: %s, diff from previous read]\n%s", absPath, diff)
}

// estimateTokens estimates token count for text.
func (c *FileContentCache) estimateTokens(text string) int {
	return c.estimator.EstimateTextTokens(text)
}

// persistPath returns the persistence file path.
func (c *FileContentCache) persistPath() string {
	if c.config.PersistPath != "" {
		return c.config.PersistPath
	}
	// 收口：Dir() 返回空（home 不可得）⇒ 返回空串（无持久化），行为同前。
	if whalehome.Dir() == "" {
		return ""
	}
	return filepath.Join(whalehome.Dir(), "file-cache.json")
}

// Persist saves cache metadata to disk.
// Content is NOT persisted (memory-only).
func (c *FileContentCache) Persist() error {
	if !c.config.PersistToDisk {
		return nil
	}

	path := c.persistPath()
	if path == "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	type persistedEntry struct {
		Path          string    `json:"path"`
		SHA256        string    `json:"sha256"`
		Size          int64     `json:"size"`
		ModTime       time.Time `json:"mod_time"`
		LineCount     int       `json:"line_count"`
		TokenEst      int       `json:"token_est"`
		ReadCount     int       `json:"read_count"`
		CacheHitCount int       `json:"cache_hit_count"`
		TokensSaved   int64     `json:"tokens_saved"`
	}

	type persistedData struct {
		Version int                       `json:"version"`
		Entries map[string]persistedEntry `json:"entries"`
	}

	data := persistedData{
		Version: 1,
		Entries: make(map[string]persistedEntry),
	}

	for path, entry := range c.entries {
		data.Entries[path] = persistedEntry{
			Path:          entry.Path,
			SHA256:        entry.SHA256,
			Size:          entry.Size,
			ModTime:       entry.ModTime,
			LineCount:     entry.LineCount,
			TokenEst:      entry.TokenEst,
			ReadCount:     entry.ReadCount,
			CacheHitCount: entry.CacheHitCount,
			TokensSaved:   entry.TokensSaved,
		}
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	if err := util.AtomicWriteFile(path, jsonData, 0600); err != nil {
		return err
	}

	// T1-P1-B4：同步持久化磁盘 LRU 索引（SyncOnEvict/Async 模式下需要）。
	// SyncOnWrite 模式下 Put 时已实时持久化，此处为幂等重写。
	if c.diskStore != nil {
		_ = c.diskStore.PersistIndex()
	}
	return nil
}

// Restore loads cache metadata from disk.
// Content is NOT restored (will be filled on first read).
func (c *FileContentCache) Restore() error {
	if !c.config.PersistToDisk {
		return nil
	}

	path := c.persistPath()
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil // file not found is normal
	}

	type persistedEntry struct {
		Path          string    `json:"path"`
		SHA256        string    `json:"sha256"`
		Size          int64     `json:"size"`
		ModTime       time.Time `json:"mod_time"`
		LineCount     int       `json:"line_count"`
		TokenEst      int       `json:"token_est"`
		ReadCount     int       `json:"read_count"`
		CacheHitCount int       `json:"cache_hit_count"`
		TokensSaved   int64     `json:"tokens_saved"`
	}

	type persistedData struct {
		Version int                       `json:"version"`
		Entries map[string]persistedEntry `json:"entries"`
	}

	var persisted persistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil // corrupt file, silently ignore
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for path, pe := range persisted.Entries {
		// verify file still exists
		if _, err := os.Stat(path); err != nil {
			continue
		}
		entry := &FileCacheEntry{
			Path:          pe.Path,
			SHA256:        pe.SHA256,
			Size:          pe.Size,
			ModTime:       pe.ModTime,
			LineCount:     pe.LineCount,
			TokenEst:      pe.TokenEst,
			ReadCount:     pe.ReadCount,
			CacheHitCount: pe.CacheHitCount,
			TokensSaved:   pe.TokensSaved,
			CreatedAt:     time.Now(),
		}
		// Content is empty, will be filled on first Read
		c.entries[path] = entry
	}

	return nil
}

// Clear clears all cache entries and resets metrics.
func (c *FileContentCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// T1-P1-B4：清空前先收集 path 用于磁盘 LRU 索引清理。
	// 注意：必须在清空 c.entries 之前收集，否则遍历空 map 无法删除磁盘条目。
	if c.diskStore != nil {
		for path := range c.entries {
			_ = c.diskStore.Delete(path)
		}
	}

	c.entries = make(map[string]*FileCacheEntry)
	c.metrics = NewFileCacheMetrics()
}

// Close 关闭文件缓存（T1-P1-B4）。
// 持久化内存 LRU 元数据 + 磁盘 LRU 索引；进程退出前调用。
// 失败时返回第一个遇到的错误，但继续尝试持久化其他数据。
func (c *FileContentCache) Close() error {
	var firstErr error

	if err := c.Persist(); err != nil && firstErr == nil {
		firstErr = err
	}

	if c.diskStore != nil {
		if err := c.diskStore.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// DiskMetrics 返回磁盘 LRU 缓存指标快照（T1-P1-B4）。
// diskStore 为 nil 时返回零值。
func (c *FileContentCache) DiskMetrics() DiskCacheMetrics {
	if c.diskStore == nil {
		return DiskCacheMetrics{}
	}
	return c.diskStore.Metrics()
}

// EntryCount returns the current number of cache entries.
func (c *FileContentCache) EntryCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// TopSavedFiles returns the top N files by tokens saved.
func (c *FileContentCache) TopSavedFiles(n int) []FileCacheRankEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	var entries []FileCacheRankEntry
	for _, entry := range c.entries {
		hitRate := 0.0
		if entry.ReadCount > 0 {
			hitRate = float64(entry.CacheHitCount) / float64(entry.ReadCount)
		}
		entries = append(entries, FileCacheRankEntry{
			Path:        entry.Path,
			ReadCount:   entry.ReadCount,
			TokensSaved: entry.TokensSaved,
			HitRate:     hitRate,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TokensSaved > entries[j].TokensSaved
	})

	if len(entries) > n {
		entries = entries[:n]
	}
	return entries
}

// computeSHA256 computes the SHA256 hash of data and returns hex string.
func computeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// Note: isBinaryFile is defined in binary_detect.go (T7 统一实现) with
// extension-based quick detection plus content scanning. That canonical
// implementation is used by file_cache.go (see ReadFileEntry), the search
// engine, and the pipeline.
