package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// PromptState is the prompt state snapshot
// Records key prompt component hashes before API call, for detecting cache breaks
type PromptState struct {
	// SystemPromptHash is the SHA256 hash of the system prompt
	SystemPromptHash string
	// ToolSchemasHash is the SHA256 hash of tool definitions (hash of concatenated schemas)
	ToolSchemasHash string
	// ModelHash is the SHA256 hash of the model name
	ModelHash string
	// CacheControlHash is the SHA256 hash of cache_control configuration
	CacheControlHash string
}

// CacheBreakResult is the cache break detection result
// Contains whether break was detected, break reason, cache read drop ratio, and details
type CacheBreakResult struct {
	// IsBreak whether cache break was detected
	IsBreak bool
	// BreakReason is the break reason description
	BreakReason string
	// CacheReadDrop cache read token drop ratio (0.0 ~ 1.0)
	CacheReadDrop float64
	// Details break details (key-value pairs)
	Details map[string]string
}

// defaultBreakThreshold is the default cache read drop threshold (5%)
const defaultBreakThreshold = 0.05

// minCacheMissTokens is the minimum absolute token decrease required to
// trigger a cache break (FACT-AI-003). A small absolute drop (e.g. 600 tokens)
// should not trigger a break even if the percentage exceeds the threshold,
// because it is likely just normal cache churn rather than a real cache break.
const minCacheMissTokens = 2000

// CacheBreakDetector is the cache break detector
// Two-phase detection: record state before API call, check cache read token drop after API response
// Determines cache break when cache read token drop exceeds the configured threshold
type CacheBreakDetector struct {
	mu                   sync.RWMutex
	prevPromptState      *PromptState
	prevCacheReadTokens  int
	cacheDeletionPending bool
	compactionPending    bool
	breakThreshold       float64
	cachingStyle         CachingStyle // 方案 C：DeepSeek（CachingPromptCacheKey）路径跳过被动检测
	recentBreaks         []string     // 方案 R-2：记录近期断裂原因，模型切换时清空避免跨模型误判
}

// NewCacheBreakDetector creates a new cache break detector with default 5% threshold
func NewCacheBreakDetector() *CacheBreakDetector {
	return NewCacheBreakDetectorWithThreshold(defaultBreakThreshold)
}

// NewCacheBreakDetectorWithThreshold creates a new cache break detector with the given threshold
// threshold is the fractional drop (e.g., 0.05 = 5%) below which cache reads must fall to trigger a break
func NewCacheBreakDetectorWithThreshold(threshold float64) *CacheBreakDetector {
	if threshold <= 0 || threshold > 1.0 {
		threshold = defaultBreakThreshold
	}
	return &CacheBreakDetector{
		breakThreshold: threshold,
	}
}

// NewCacheBreakDetectorWithStyle creates a new cache break detector with the given threshold and caching style.
// 方案 C：cachingStyle 决定是否跳过被动检测（DeepSeek 路径无 cache_read_tokens 数据基础）。
func NewCacheBreakDetectorWithStyle(threshold float64, style CachingStyle) *CacheBreakDetector {
	d := NewCacheBreakDetectorWithThreshold(threshold)
	d.cachingStyle = style
	return d
}

// SetBreakThreshold updates the cache break detection threshold at runtime
func (d *CacheBreakDetector) SetBreakThreshold(threshold float64) {
	if threshold <= 0 || threshold > 1.0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.breakThreshold = threshold
}

// RecordPromptState records prompt state before API call
// Saves hashes of system prompt, tool definitions, model name, and cache_control configuration,
// So that after API response they can be compared with current state to detect changes causing cache breaks
func (d *CacheBreakDetector) RecordPromptState(systemPrompt string, toolSchemas []string, model string, cacheControlHash string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.prevPromptState = &PromptState{
		SystemPromptHash: computeHashSHA256(systemPrompt),
		ToolSchemasHash:  computeHashSHA256(strings.Join(toolSchemas, "|")),
		ModelHash:        computeHashSHA256(model),
		CacheControlHash: computeHashSHA256(cacheControlHash),
	}
}

// GetLastPromptState returns a copy of the last recorded prompt state
// Returns nil if no state has been recorded yet
func (d *CacheBreakDetector) GetLastPromptState() *PromptState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.prevPromptState == nil {
		return nil
	}
	state := *d.prevPromptState
	return &state
}

// CheckForCacheBreak checks whether a cache break occurred after API response
// Compares current prompt state with previously recorded state, detects if cache read token drop exceeds threshold
// If break detected, returns break reason and drop ratio; otherwise returns nil
func (d *CacheBreakDetector) CheckForCacheBreak(cacheReadTokens int, currentPromptState PromptState) *CacheBreakResult {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 方案 C：DeepSeek（CachingPromptCacheKey）路径跳过被动检测。
	// 原因：DeepSeek 响应不含 cache_read_tokens，被动检测无数据基础；
	// 改为依赖 NotifyCacheDeletion / NotifyCompaction 主动通知。
	// 后续若 DeepSeek 返回 cached_tokens，可在此追加 C1 分支。
	if d.cachingStyle == CachingPromptCacheKey {
		d.prevCacheReadTokens = cacheReadTokens
		return nil
	}

	if d.prevPromptState == nil {
		d.prevCacheReadTokens = cacheReadTokens
		return nil
	}

	// If there is a pending cache deletion event, skip break detection (expected behavior)
	if d.cacheDeletionPending {
		d.cacheDeletionPending = false
		d.prevCacheReadTokens = cacheReadTokens
		return nil
	}

	// If there is a pending compaction event, reset baseline and skip detection
	if d.compactionPending {
		d.compactionPending = false
		d.prevCacheReadTokens = cacheReadTokens
		return nil
	}

	// Detect prompt state changes
	details := make(map[string]string)
	reasons := []string{}

	if currentPromptState.SystemPromptHash != d.prevPromptState.SystemPromptHash {
		reasons = append(reasons, "system_prompt_changed")
		details["system_prompt"] = fmt.Sprintf("hash changed from %s to %s", d.prevPromptState.SystemPromptHash[:8], currentPromptState.SystemPromptHash[:8])
	}

	if currentPromptState.ToolSchemasHash != d.prevPromptState.ToolSchemasHash {
		reasons = append(reasons, "tool_schemas_changed")
		details["tool_schemas"] = fmt.Sprintf("hash changed from %s to %s", d.prevPromptState.ToolSchemasHash[:8], currentPromptState.ToolSchemasHash[:8])
	}

	if currentPromptState.ModelHash != d.prevPromptState.ModelHash {
		reasons = append(reasons, "model_changed")
		details["model"] = fmt.Sprintf("hash changed from %s to %s", d.prevPromptState.ModelHash[:8], currentPromptState.ModelHash[:8])
	}

	if currentPromptState.CacheControlHash != d.prevPromptState.CacheControlHash {
		reasons = append(reasons, "cache_control_changed")
		details["cache_control"] = fmt.Sprintf("hash changed from %s to %s", d.prevPromptState.CacheControlHash[:8], currentPromptState.CacheControlHash[:8])
	}

	// Calculate cache read token drop ratio and absolute decrease
	var drop float64
	prevRead := d.prevCacheReadTokens
	absDecrease := 0
	if prevRead > 0 {
		absDecrease = prevRead - cacheReadTokens
		if absDecrease < 0 {
			absDecrease = 0
		}
		drop = float64(absDecrease) / float64(prevRead)
		if drop < 0 {
			drop = 0
		}
	}

	// FACT-AI-003: dual-condition detection — a cache break is only reported
	// when BOTH the percentage drop exceeds the threshold AND the absolute
	// token decrease exceeds minCacheMissTokens. This prevents false positives
	// from small absolute fluctuations (e.g. 6% of a small baseline = 600
	// tokens, which is normal churn not a real break).
	if drop > d.breakThreshold && absDecrease >= minCacheMissTokens {
		breakReason := "cache_read_drop"
		if len(reasons) > 0 {
			breakReason = strings.Join(reasons, ", ")
		}
		details["cache_read_drop"] = fmt.Sprintf("%.2f%%", drop*100)
		details["prev_cache_read"] = fmt.Sprintf("%d", prevRead)
		details["current_cache_read"] = fmt.Sprintf("%d", cacheReadTokens)

		// Update baseline after recording details
		d.prevCacheReadTokens = cacheReadTokens

		return &CacheBreakResult{
			IsBreak:       true,
			BreakReason:   breakReason,
			CacheReadDrop: drop,
			Details:       details,
		}
	}

	// Update baseline
	d.prevCacheReadTokens = cacheReadTokens

	return nil
}

// NotifyCacheDeletion notifies cache deletion event
// Called when cache deletion is expected (e.g. TTL expiration), preventing false cache break reports
func (d *CacheBreakDetector) NotifyCacheDeletion() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cacheDeletionPending = true
}

// NotifyCompaction notifies compaction event
// Compaction changes context structure, need to reset cache read baseline
func (d *CacheBreakDetector) NotifyCompaction() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.compactionPending = true
}

// Reset resets all tracking state
// Clears previously recorded prompt state and cache read baseline
func (d *CacheBreakDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prevPromptState = nil
	d.prevCacheReadTokens = 0
	d.cacheDeletionPending = false
	d.compactionPending = false
	d.recentBreaks = nil
}

// UpdateCachingStyle 更新缓存策略风格并重置基线（方案 R-2）。
// 模型切换（DeepSeek ↔ claude）时调用：
//   - 更新 cachingStyle，下次 CheckForCacheBreak 用新 style 判定
//   - 清空 prevPromptState（不同模型 system prompt 可能不同，hash 比对无意义）
//   - 清空 prevCacheReadTokens（不同模型 token 计数语义不同）
//   - 清空 recentBreaks（避免跨模型误判）
//
// 并发安全：使用写锁保护。
func (d *CacheBreakDetector) UpdateCachingStyle(style CachingStyle) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cachingStyle = style
	d.prevPromptState = nil
	d.prevCacheReadTokens = 0
	d.recentBreaks = nil
	d.cacheDeletionPending = false
	d.compactionPending = false
}

// Threshold returns the current break detection threshold
func (d *CacheBreakDetector) Threshold() float64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.breakThreshold
}

// computeHashSHA256 computes SHA256 hash for general use
func computeHashSHA256(data string) string {
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}
