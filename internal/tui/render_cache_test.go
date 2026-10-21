// Package tui - render_cache tests ( M10-02)
//
// 验证 LRU 渲染缓存的双约束淘汰（条目数 + 字节数）。
package tui

import (
	"strings"
	"testing"
)

// TestLRURenderCache_ByteLimit 验证字节上限触发淘汰。
//
// 写入 10 条 × 1MB，maxBytes=5MB，验证最终内存 ≤ 5MB 且保留最近访问的条目。
func TestLRURenderCache_ByteLimit(t *testing.T) {
	// maxEntries=100（不触发条目约束），maxBytes=5MB
	cache := NewLRURenderCache(100, 5*1024*1024).(*lruRenderCache)
	bigContent := strings.Repeat("x", 1024*1024) // 1MB
	renderFn := func() (string, int, error) { return bigContent, 1, nil }

	// 写入 10 条 1MB
	for i := 0; i < 10; i++ {
		cache.GetOrRender(string(rune('a'+i)), renderFn)
	}

	stats := cache.Stats()
	if stats.MemoryBytes > 5*1024*1024 {
		t.Errorf("MemoryBytes = %d, want <= %d", stats.MemoryBytes, 5*1024*1024)
	}
	if stats.Entries > 5 {
		t.Errorf("Entries = %d, want <= 5（字节约束淘汰后）", stats.Entries)
	}
	// 至少保留 1 条（不淘汰空缓存）
	if stats.Entries < 1 {
		t.Errorf("Entries = %d, want >= 1", stats.Entries)
	}
}

// TestLRURenderCache_DualConstraint 验证条目未达上限但字节达上限时触发淘汰。
//
// maxEntries=100（不触发），maxBytes=3MB；写入 5 条 × 1MB，验证淘汰至 3 条。
func TestLRURenderCache_DualConstraint(t *testing.T) {
	cache := NewLRURenderCache(100, 3*1024*1024).(*lruRenderCache)
	bigContent := strings.Repeat("y", 1024*1024)
	renderFn := func() (string, int, error) { return bigContent, 1, nil }

	for i := 0; i < 5; i++ {
		cache.GetOrRender(string(rune('a'+i)), renderFn)
	}

	stats := cache.Stats()
	if stats.MemoryBytes > 3*1024*1024 {
		t.Errorf("MemoryBytes = %d, want <= 3MB", stats.MemoryBytes)
	}
	if stats.Entries > 3 {
		t.Errorf("Entries = %d, want <= 3", stats.Entries)
	}
}

// TestLRURenderCache_SingleEntryExceedsByteLimit 验证单条 > maxBytes 时保留 1 条。
//
// maxBytes=1MB；写入 1 条 2MB，验证不无限淘汰（保留 1 条）。
func TestLRURenderCache_SingleEntryExceedsByteLimit(t *testing.T) {
	cache := NewLRURenderCache(100, 1024*1024).(*lruRenderCache) // 1MB
	bigContent := strings.Repeat("z", 2*1024*1024)               // 2MB
	renderFn := func() (string, int, error) { return bigContent, 1, nil }

	cache.GetOrRender("k1", renderFn)

	stats := cache.Stats()
	if stats.Entries != 1 {
		t.Errorf("Entries = %d, want 1（单条超 maxBytes 时保留）", stats.Entries)
	}
}

// TestLRURenderCache_DisabledByteLimit 验证 maxBytes=0 时仅条目约束。
//
// maxEntries=3，maxBytes=0；写入 5 条，验证按条目淘汰至 3 条。
func TestLRURenderCache_DisabledByteLimit(t *testing.T) {
	cache := NewLRURenderCache(3, 0).(*lruRenderCache)
	renderFn := func() (string, int, error) { return "content", 1, nil }

	for i := 0; i < 5; i++ {
		cache.GetOrRender(string(rune('a'+i)), renderFn)
	}

	stats := cache.Stats()
	if stats.Entries != 3 {
		t.Errorf("Entries = %d, want 3（maxBytes=0 时仅条目约束）", stats.Entries)
	}
}

// TestLRURenderCache_ByteLimitKeepsRecent 验证字节淘汰保留最近访问的条目。
//
// 写入 a/b/c（各 1MB，maxBytes=2MB），访问 a 后写入 d，验证 a + d 保留。
func TestLRURenderCache_ByteLimitKeepsRecent(t *testing.T) {
	cache := NewLRURenderCache(100, 2*1024*1024).(*lruRenderCache)
	content := strings.Repeat("x", 1024*1024)
	renderFn := func() (string, int, error) { return content, 1, nil }

	cache.GetOrRender("a", renderFn)
	cache.GetOrRender("b", renderFn)
	cache.GetOrRender("c", renderFn)
	// 访问 a，使其成为最近使用
	cache.GetOrRender("a", renderFn)
	// 写入 d，触发淘汰（b 应被淘汰，a + c/d 保留）
	cache.GetOrRender("d", renderFn)

	stats := cache.Stats()
	if stats.Entries > 2 {
		t.Errorf("Entries = %d, want <= 2", stats.Entries)
	}
	// a 应存在（最近访问）
	if _, ok := cache.index["a"]; !ok {
		t.Error("a 应保留（最近访问）")
	}
}
