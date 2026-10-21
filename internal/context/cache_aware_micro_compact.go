// Package context — cache-aware micro-compaction
package context

import (
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// IsUnchangedFileResult checks if a message contains a FILE_UNCHANGED marker
// from the file content cache. These messages are ideal compaction targets
// because the model already knows the file content via Prompt Cache.
func IsUnchangedFileResult(msg session.Message) bool {
	if msg.Role != session.RoleTool {
		return false
	}
	for _, block := range msg.Blocks {
		if block.Type == "tool_result" && strings.Contains(block.Output, "[FILE_UNCHANGED:") {
			return true
		}
	}
	return false
}

// IsModifiedFileResult checks if a message contains a FILE_MODIFIED marker
// from the file content cache (diff mode).
func IsModifiedFileResult(msg session.Message) bool {
	if msg.Role != session.RoleTool {
		return false
	}
	for _, block := range msg.Blocks {
		if block.Type == "tool_result" && strings.Contains(block.Output, "[FILE_MODIFIED:") {
			return true
		}
	}
	return false
}

// CacheAwareMicroCompactConfig is the cache-aware micro-compaction config
// Skip micro-compaction during cache validity period to avoid breaking cache prefix
type CacheAwareMicroCompactConfig struct {
	// CacheTTL cache time-to-live (default 1 hour)
	CacheTTL time.Duration
	// PreserveRecent preserves the most recent N tool results from truncation
	PreserveRecent int
	// TruncationThreshold triggers truncation when tool result content exceeds this character count
	TruncationThreshold int
	// CacheEditingSupported whether cache editing operations are supported
	CacheEditingSupported bool
}

// CacheEditOperation represents a cache edit operation for cache-aware micro-compaction
type CacheEditOperation struct {
	Type           string `json:"type"`            // "delete" or "replace"
	CacheReference string `json:"cache_reference"` // reference to the cache entry
}

// DefaultCacheAwareMicroCompactConfig returns default cache-aware micro-compaction config
func DefaultCacheAwareMicroCompactConfig() CacheAwareMicroCompactConfig {
	return CacheAwareMicroCompactConfig{
		CacheTTL:              1 * time.Hour,
		PreserveRecent:        5,
		TruncationThreshold:   500,
		CacheEditingSupported: false,
	}
}

// CacheAwareMicroCompact performs cache-aware micro-compaction
// If cache is still valid (now - lastCacheTime < CacheTTL), only compact messages after cacheBoundaryIndex
// If cache has expired, perform regular micro-compaction on all messages
// If cache editing is supported, generate cache edit operations instead of directly modifying messages
func CacheAwareMicroCompact(
	messages []session.Message,
	config CacheAwareMicroCompactConfig,
	lastCacheTime time.Time,
	now time.Time,
	cacheBoundaryIndex int,
) []session.Message {
	if len(messages) == 0 {
		return messages
	}

	if cacheBoundaryIndex < 0 {
		cacheBoundaryIndex = 0
	}
	if cacheBoundaryIndex > len(messages) {
		cacheBoundaryIndex = len(messages)
	}

	microConfig := MicroCompactConfig{
		PreserveRecent:      config.PreserveRecent,
		TimeThreshold:       5 * time.Minute,
		TruncationThreshold: config.TruncationThreshold,
	}

	if isCacheActive(lastCacheTime, now, config.CacheTTL) {
		if cacheBoundaryIndex >= len(messages) {
			return messages
		}
		afterBoundary := messages[cacheBoundaryIndex:]
		compacted := MicroCompact(afterBoundary, microConfig)
		result := make([]session.Message, 0, cacheBoundaryIndex+len(compacted))
		result = append(result, messages[:cacheBoundaryIndex]...)
		result = append(result, compacted...)
		return result
	}

	return MicroCompact(messages, microConfig)
}

// GenerateCacheEdits generates cache edit operation list
// When cache editing is supported, generate delete operations instead of directly modifying messages
// Iterate through old tool results in messages, generate delete edits for content exceeding truncation threshold
//
// Deprecated: This function is reserved for future use and not currently active.
// It will be called when CacheEditingSupported is set to true.
func GenerateCacheEdits(messages []session.Message, config CacheAwareMicroCompactConfig) []CacheEditOperation {
	var edits []CacheEditOperation

	if len(messages) == 0 {
		return edits
	}

	preservedCount := 0
	nowMs := time.Now().UnixMilli()

	// Iterate from newest to oldest
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]

		// Only process tool result messages
		if msg.Role != session.RoleTool {
			continue
		}

		// Determine if it is a recent message (within 5 minutes)
		elapsedMs := nowMs - msg.Timestamp
		isRecent := elapsedMs < 5*60*1000 // 5 minutes = 300000 milliseconds

		// Recent messages or not exceeding preserve count, keep directly
		if isRecent || preservedCount < config.PreserveRecent {
			preservedCount++
			continue
		}

		// Generate delete edit for old tool results
		for _, block := range msg.Blocks {
			if block.Type == "tool_result" && len(block.Output) > config.TruncationThreshold {
				ref := fmt.Sprintf("tool_result:%s:%s", block.ToolName, block.ToolUseID)
				edits = append(edits, CacheEditOperation{
					Type:           "delete",
					CacheReference: ref,
				})
			}
		}
	}

	return edits
}

// isCacheActive determines if cache is still valid
// Cache is valid when the difference between now and lastCacheTime is less than CacheTTL
func isCacheActive(lastCacheTime time.Time, now time.Time, ttl time.Duration) bool {
	if lastCacheTime.IsZero() {
		return false
	}
	return now.Sub(lastCacheTime) < ttl
}
