// Package tools — file cache metrics
package tools

import (
	"fmt"
	"sync"
)

// FileCacheMetrics holds file cache metrics
type FileCacheMetrics struct {
	mu sync.RWMutex

	// --- request statistics ---
	TotalReads    int `json:"total_reads"`    // total read count
	UnchangedHits int `json:"unchanged_hits"` // UNCHANGED hit count
	DiffHits      int `json:"diff_hits"`      // DIFF hit count
	FullReads     int `json:"full_reads"`     // FULL read count (including first reads)
	Invalidations int `json:"invalidations"`  // active invalidation count

	// --- token statistics ---
	TotalTokensSaved int64 `json:"total_tokens_saved"` // cumulative tokens saved
	TotalTokensFull  int64 `json:"total_tokens_full"`  // total tokens if not cached

	// --- cache state ---
	EntryCount    int     `json:"entry_count"`     // current cache entry count
	MemoryUsageMB float64 `json:"memory_usage_mb"` // current memory usage
}

// FileCacheRankEntry represents a file cache ranking entry
type FileCacheRankEntry struct {
	Path        string  `json:"path"`
	ReadCount   int     `json:"read_count"`
	TokensSaved int64   `json:"tokens_saved"`
	HitRate     float64 `json:"hit_rate"` // (UnchangedHits + DiffHits) / ReadCount
}

// NewFileCacheMetrics creates a new metrics instance
func NewFileCacheMetrics() *FileCacheMetrics {
	return &FileCacheMetrics{}
}

// recordRead records a read operation.
// fullTokens is the estimated tokens for full content.
// saved is the tokens saved this read.
func (m *FileCacheMetrics) recordRead(status CacheStatus, fullTokens, saved int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalReads++
	m.TotalTokensFull += int64(fullTokens)
	m.TotalTokensSaved += int64(saved)

	switch status {
	case CacheUnchanged:
		m.UnchangedHits++
	case CacheDiff:
		m.DiffHits++
	case CacheFull, CacheError:
		m.FullReads++
	}
}

// recordInvalidation records a single invalidation
func (m *FileCacheMetrics) recordInvalidation() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Invalidations++
}

// addInvalidations adds to the invalidation count
func (m *FileCacheMetrics) addInvalidations(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Invalidations += count
}

// UpdateCacheState updates the cache state (entry count, memory usage)
func (m *FileCacheMetrics) UpdateCacheState(entryCount int, memoryUsageMB float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EntryCount = entryCount
	m.MemoryUsageMB = memoryUsageMB
}

// RecordFileCacheHit records a file cache hit (called from executor integration)
func (m *FileCacheMetrics) RecordFileCacheHit(path string, status CacheStatus, fullTokens, tokensSaved int) {
	m.recordRead(status, fullTokens, tokensSaved)
}

// Snapshot returns a copy of the current metrics
func (m *FileCacheMetrics) Snapshot() FileCacheMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return FileCacheMetrics{
		TotalReads:       m.TotalReads,
		UnchangedHits:    m.UnchangedHits,
		DiffHits:         m.DiffHits,
		FullReads:        m.FullReads,
		Invalidations:    m.Invalidations,
		TotalTokensSaved: m.TotalTokensSaved,
		TotalTokensFull:  m.TotalTokensFull,
		EntryCount:       m.EntryCount,
		MemoryUsageMB:    m.MemoryUsageMB,
	}
}

// HitRate returns the overall hit rate
func (m *FileCacheMetrics) HitRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.TotalReads == 0 {
		return 0
	}
	return float64(m.UnchangedHits+m.DiffHits) / float64(m.TotalReads)
}

// SavingsRate returns the token savings rate
func (m *FileCacheMetrics) SavingsRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.TotalTokensFull == 0 {
		return 0
	}
	return float64(m.TotalTokensSaved) / float64(m.TotalTokensFull)
}

// FormatReport formats a human-readable metrics report
func (m *FileCacheMetrics) FormatReport() string {
	snap := m.Snapshot()
	hitRate := 0.0
	if snap.TotalReads > 0 {
		hitRate = float64(snap.UnchangedHits+snap.DiffHits) / float64(snap.TotalReads) * 100
	}
	savingsRate := 0.0
	if snap.TotalTokensFull > 0 {
		savingsRate = float64(snap.TotalTokensSaved) / float64(snap.TotalTokensFull) * 100
	}
	return fmt.Sprintf(
		"File Cache Metrics:\n"+
			"  Reads: %d (unchanged: %d, diff: %d, full: %d)\n"+
			"  Hit rate: %.1f%%\n"+
			"  Tokens saved: %d (%.1f%% of %d)\n"+
			"  Cache entries: %d (%.1f MB)\n"+
			"  Invalidations: %d",
		snap.TotalReads, snap.UnchangedHits, snap.DiffHits, snap.FullReads,
		hitRate,
		snap.TotalTokensSaved, savingsRate, snap.TotalTokensFull,
		snap.EntryCount, snap.MemoryUsageMB,
		snap.Invalidations,
	)
}
