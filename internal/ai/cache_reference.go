// Package ai — cache_reference registry module
// Tracks tool_use_id → tool_result position mapping for cache_reference attachment
package ai

import "sync"

// defaultMaxCacheRefEntries is the default maximum number of entries before LRU eviction kicks in
const defaultMaxCacheRefEntries = 1000

// CacheReferenceEntry maps tool_use_id to its tool_result position
type CacheReferenceEntry struct {
	// ToolUseID is the unique ID of the tool call
	ToolUseID string
	// MessageIndex is the position of the message in the list
	MessageIndex int
	// ContentIndex is the position of the content block in the message
	ContentIndex int
}

// CacheReferenceRegistry is the registry tracking tool_use_id → tool_result mapping
// Used to attach cache_reference on tool_result blocks within the cache prefix.
// Implements true LRU eviction to bound memory growth for long-running sessions.
// 修复 P1：原实现为 FIFO（Lookup 不更新访问顺序），改为真正 LRU（Lookup/Register 均移到末尾）。
type CacheReferenceRegistry struct {
	entries    map[string]*CacheReferenceEntry
	order      []string // LRU 访问顺序（front=最久未用，back=最近使用）
	maxEntries int
	mu         sync.RWMutex
}

// NewCacheReferenceRegistry creates a new registry with default max entries
func NewCacheReferenceRegistry() *CacheReferenceRegistry {
	return NewCacheReferenceRegistryWithCapacity(defaultMaxCacheRefEntries)
}

// NewCacheReferenceRegistryWithCapacity creates a new registry with the given max entry count
func NewCacheReferenceRegistryWithCapacity(maxEntries int) *CacheReferenceRegistry {
	if maxEntries <= 0 {
		maxEntries = defaultMaxCacheRefEntries
	}
	return &CacheReferenceRegistry{
		entries:    make(map[string]*CacheReferenceEntry),
		maxEntries: maxEntries,
	}
}

// Register adds mapping from tool_use_id to its position.
// If tool_use_id already exists, updates the mapping and moves it to most-recently-used.
// If the registry is at capacity, evicts the least-recently-used entry (front of order).
// 修复 P1：已有键更新时移到末尾（真正 LRU），原实现不更新顺序（FIFO 行为）。
func (r *CacheReferenceRegistry) Register(toolUseID string, msgIndex, contentIndex int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[toolUseID]; exists {
		// 更新已有条目并移到末尾（最近使用）
		r.entries[toolUseID] = &CacheReferenceEntry{
			ToolUseID:    toolUseID,
			MessageIndex: msgIndex,
			ContentIndex: contentIndex,
		}
		r.moveToEndLocked(toolUseID)
		return
	}

	// 容量满时淘汰最久未用（front）
	if len(r.order) >= r.maxEntries {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}

	r.entries[toolUseID] = &CacheReferenceEntry{
		ToolUseID:    toolUseID,
		MessageIndex: msgIndex,
		ContentIndex: contentIndex,
	}
	r.order = append(r.order, toolUseID)
}

// Lookup looks up position info for the specified tool_use_id.
// Returns a copy to prevent external modification of internal data.
// 修复 P1：Lookup 时将条目移到末尾（最近使用），实现真正 LRU。
// 原实现用 RLock 不更新顺序（FIFO 行为），现改为 Lock + moveToEnd。
func (r *CacheReferenceRegistry) Lookup(toolUseID string) *CacheReferenceEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.entries[toolUseID]
	if !ok {
		return nil
	}
	// LRU：访问后移到末尾（最近使用）
	r.moveToEndLocked(toolUseID)
	copy := *entry
	return &copy
}

// moveToEndLocked 将指定 toolUseID 移到 order 末尾（调用方须持锁）。
// 从 order 中删除该键再追加到末尾，实现 LRU 最近使用更新。
func (r *CacheReferenceRegistry) moveToEndLocked(toolUseID string) {
	for i, id := range r.order {
		if id == toolUseID {
			// 从位置 i 删除，追加到末尾
			r.order = append(r.order[:i], r.order[i+1:]...)
			r.order = append(r.order, toolUseID)
			return
		}
	}
}

// Clear clears all entries
func (r *CacheReferenceRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries = make(map[string]*CacheReferenceEntry)
	r.order = r.order[:0]
}

// Size returns the number of registered entries
func (r *CacheReferenceRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.entries)
}

// MaxCapacity returns the configured maximum number of entries
func (r *CacheReferenceRegistry) MaxCapacity() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.maxEntries
}
