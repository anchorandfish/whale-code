package ai

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
)

// CacheMetrics is the cache metrics tracking
// modeled after a comparable CLI cache metrics: tracks cache hit rate and token savings
type CacheMetrics struct {
	mu                    sync.RWMutex `json:"-"`
	TotalRequests         int          `json:"total_requests"`
	TotalInputTokens      int          `json:"total_input_tokens"`
	TotalOutputTokens     int          `json:"total_output_tokens"`
	TotalCacheReadTokens  int          `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens int          `json:"total_cache_write_tokens"`
	TotalAnomalies        int          `json:"total_anomalies"`
}

// NewCacheMetrics creates a cache metrics tracker
func NewCacheMetrics() *CacheMetrics {
	return &CacheMetrics{}
}

// RecordRequest records cache metrics for an API request
func (m *CacheMetrics) RecordRequest(inputTokens, outputTokens, cacheReadTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalRequests++
	m.TotalInputTokens += inputTokens
	m.TotalOutputTokens += outputTokens
	m.TotalCacheReadTokens += cacheReadTokens

	// cacheWrite = inputTokens - cacheReadTokens is the standard formula
	// When cacheRead > inputTokens (anomalous provider reporting), clamp to 0
	// and track the anomaly separately to avoid underestimating metrics
	cacheWrite := inputTokens - cacheReadTokens
	if cacheWrite < 0 {
		cacheWrite = 0
		m.TotalAnomalies++
	}
	m.TotalCacheWriteTokens += cacheWrite
}

// RecordAnomaly 记录一次缓存异常事件（H13-G1）。
//
// 当 cachedInputTokens > estimatedTokens（provider 异常上报 cache 大于 total）
// 时由外部调用此方法递增 TotalAnomalies 计数器。
// 与 RecordRequest 内部的 anomaly 检测互补：RecordRequest 在 cacheRead > inputTokens
// 时自动计数，RecordAnomaly 供 BodyAfterPrefix scope 在独立 anomaly 检测路径使用。
func (m *CacheMetrics) RecordAnomaly() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalAnomalies++
}

// CacheHitRate calculates cache hit rate
// hit rate = cache read tokens / (new write tokens + cache read tokens)
func (m *CacheMetrics) CacheHitRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.TotalCacheWriteTokens + m.TotalCacheReadTokens
	if total == 0 {
		return 0
	}
	return float64(m.TotalCacheReadTokens) / float64(total)
}

// FormatReport formats the cache metrics report
func (m *CacheMetrics) FormatReport() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.TotalCacheWriteTokens + m.TotalCacheReadTokens
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(m.TotalCacheReadTokens) / float64(total) * 100
	}
	report := fmt.Sprintf("Cache Metrics:\n"+
		"  Requests: %d\n"+
		"  Input tokens: %d\n"+
		"  Output tokens: %d\n"+
		"  Cache read: %d\n"+
		"  Cache write: %d\n"+
		"  Hit rate: %.1f%%",
		m.TotalRequests, m.TotalInputTokens, m.TotalOutputTokens,
		m.TotalCacheReadTokens, m.TotalCacheWriteTokens, hitRate)
	if m.TotalAnomalies > 0 {
		report += fmt.Sprintf("\n  Anomalies: %d", m.TotalAnomalies)
	}
	return report
}

// CacheStrategy cache policy
// modeled after a comparable CLI cache strategy: optimizes request structure to maximize cache hits
type CacheStrategy struct {
	mu               sync.Mutex
	prevStableHashes map[uint64]bool // hashes of components confirmed stable in previous request
}

// NewCacheStrategy createcache policy
func NewCacheStrategy() *CacheStrategy {
	return &CacheStrategy{
		prevStableHashes: make(map[uint64]bool),
	}
}

// minLengthForStableFallback is the minimum length in bytes for a component
// to be considered stable when it doesn't match any keyword
const minLengthForStableFallback = 200

// ReorderSystemPrompt reorders system prompt components
// places seemingly stable content (rules, tool definitions) first
// places dynamic content (user context, memory) last.
//
// Uses three-tier classification:
//  1. Keyword matching (rules, tools, definitions → stable)
//  2. Length-based fallback: long components (>200 chars) without keywords
//     are likely code templates or structured content → treated as stable
//  3. Hash-based fallback: for short components without keywords, check if
//     the content hash matches a previously confirmed stable component
func (s *CacheStrategy) ReorderSystemPrompt(components []string) []string {
	if len(components) <= 1 {
		return components
	}

	// heuristic rule: includes "rule", "tool", "definition", "system" keywords are treated as stable content
	var stable, dynamic []string
	stableKeywords := []string{
		"rule", "tool", "definition", "system", "guideline", "instruction", "specification",
		"规则", "工具", "定义", "系统", "指南", "指令", "规范",
		"schema", "function", "api", "protocol", "format", "template",
	}

	s.mu.Lock()
	prevHashes := s.prevStableHashes
	s.mu.Unlock()

	for _, c := range components {
		lower := strings.ToLower(c)
		isStable := false
		for _, kw := range stableKeywords {
			if strings.Contains(lower, kw) {
				isStable = true
				break
			}
		}
		if isStable {
			stable = append(stable, c)
		} else if len(c) >= minLengthForStableFallback {
			// Long text without keywords is likely a code template or structured prompt
			stable = append(stable, c)
		} else if prevHashes[fnvHashString(c)] {
			// Short component whose content hash matches a previously stable component
			stable = append(stable, c)
		} else {
			dynamic = append(dynamic, c)
		}
	}

	return append(stable, dynamic...)
}

// UpdateStableSet updates the set of confirmed stable component hashes
// Should be called after each request completes, passing the components that were placed in the stable section
func (s *CacheStrategy) UpdateStableSet(stableComponents []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear old set and repopulate with hashes of current stable components
	s.prevStableHashes = make(map[uint64]bool, len(stableComponents))
	for _, c := range stableComponents {
		s.prevStableHashes[fnvHashString(c)] = true
	}
}

// fnvHashString computes a FNV-1a 64-bit hash of a string
func fnvHashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
