package ai

// cache_reference.go 反例测试（P1 修复验证）
//
// 验证 CacheReferenceRegistry 实现真正的 LRU（Least Recently Used）淘汰策略。
// 修复前：Lookup 不更新访问顺序，实际为 FIFO（First In First Out）。
// 修复后：Lookup/Register 均将条目移到末尾（最近使用），淘汰从头部（最久未用）移除。

import (
	"testing"
)

// TestCacheReference_LRU_LookupUpdatesOrder 反例测试（P1 修复核心验证）。
// 验证 Lookup 后条目移到最近使用位置，不会在下一次淘汰中被移除。
// 修复前（FIFO）：Lookup 不更新顺序，A 作为最早插入仍会被淘汰。
// 修复后（LRU）：Lookup A 后 A 移到末尾，B 成为最久未用被淘汰。
func TestCacheReference_LRU_LookupUpdatesOrder(t *testing.T) {
	r := NewCacheReferenceRegistryWithCapacity(3)
	r.Register("A", 0, 0)
	r.Register("B", 1, 0)
	r.Register("C", 2, 0)

	// Lookup A，使 A 成为最近使用
	r.Lookup("A")

	// 插入 D，容量满需淘汰
	// LRU 期望淘汰 B（最久未用），而非 A（最近 Lookup 过）
	r.Register("D", 3, 0)

	if r.Lookup("A") == nil {
		t.Error("LRU 修复验证失败：A 刚被 Lookup 过，不应被淘汰\n" +
			"  期望: A 仍在（Lookup 更新了访问顺序）\n" +
			"  实际: A 被淘汰（FIFO 行为，Lookup 不更新顺序）")
	}
	if r.Lookup("B") != nil {
		t.Error("LRU 修复验证失败：B 未被 Lookup，应作为最久未用被淘汰\n" +
			"  期望: B 被淘汰（LRU 最久未用）\n" +
			"  实际: B 仍在（FIFO 淘汰了 A 而非 B）")
	}
}

// TestCacheReference_LRU_RegisterExistingUpdatesOrder 反例测试（P1 修复辅助验证）。
// 验证 Register 已有键时移到末尾（最近使用），不会在下一次淘汰中被移除。
func TestCacheReference_LRU_RegisterExistingUpdatesOrder(t *testing.T) {
	r := NewCacheReferenceRegistryWithCapacity(3)
	r.Register("A", 0, 0)
	r.Register("B", 1, 0)
	r.Register("C", 2, 0)

	// 重新 Register A（更新位置），使 A 成为最近使用
	r.Register("A", 10, 10)

	// 插入 D，容量满需淘汰
	// LRU 期望淘汰 B（最久未用），而非 A（刚被 Register 更新过）
	r.Register("D", 3, 0)

	if r.Lookup("A") == nil {
		t.Error("LRU 修复验证失败：A 刚被 Register 更新，不应被淘汰")
	}
	// 验证 A 的位置已更新
	entry := r.Lookup("A")
	if entry != nil && (entry.MessageIndex != 10 || entry.ContentIndex != 10) {
		t.Errorf("A 的位置应更新为 (10,10)，实际 (%d,%d)", entry.MessageIndex, entry.ContentIndex)
	}
	if r.Lookup("B") != nil {
		t.Error("LRU 修复验证失败：B 未被更新，应作为最久未用被淘汰")
	}
}

// TestCacheReference_LRU_EvictionOrder 验证 LRU 淘汰顺序正确。
// 连续 Lookup 不同条目后，淘汰顺序应为最久未用优先。
func TestCacheReference_LRU_EvictionOrder(t *testing.T) {
	r := NewCacheReferenceRegistryWithCapacity(3)
	r.Register("A", 0, 0)
	r.Register("B", 1, 0)
	r.Register("C", 2, 0)

	// 访问顺序：A → C → B（B 最近使用，A 最久未用）
	r.Lookup("A")
	r.Lookup("C")
	r.Lookup("B")

	// 插入 D，淘汰 A（最久未用）
	r.Register("D", 3, 0)
	if r.Lookup("A") != nil {
		t.Error("A 应被淘汰（最久未用）")
	}
	if r.Lookup("B") == nil {
		t.Error("B 应保留（最近使用）")
	}
	if r.Lookup("C") == nil {
		t.Error("C 应保留")
	}
	if r.Lookup("D") == nil {
		t.Error("D 应保留（刚插入）")
	}
}

// TestCacheReference_BasicOperations 验证基本 CRUD 操作。
func TestCacheReference_BasicOperations(t *testing.T) {
	r := NewCacheReferenceRegistry()

	// Register
	r.Register("tool_1", 0, 0)
	r.Register("tool_2", 1, 1)

	// Lookup
	entry := r.Lookup("tool_1")
	if entry == nil {
		t.Fatal("Lookup tool_1 返回 nil")
	}
	if entry.MessageIndex != 0 || entry.ContentIndex != 0 {
		t.Errorf("位置 = (%d,%d), 期望 (0,0)", entry.MessageIndex, entry.ContentIndex)
	}

	// Size
	if r.Size() != 2 {
		t.Errorf("Size = %d, 期望 2", r.Size())
	}

	// Clear
	r.Clear()
	if r.Size() != 0 {
		t.Errorf("Clear 后 Size = %d, 期望 0", r.Size())
	}
}

// TestCacheReference_LookupReturnsCopy 验证 Lookup 返回副本，外部修改不影响内部数据。
func TestCacheReference_LookupReturnsCopy(t *testing.T) {
	r := NewCacheReferenceRegistry()
	r.Register("tool_1", 5, 10)

	entry := r.Lookup("tool_1")
	if entry == nil {
		t.Fatal("Lookup 返回 nil")
	}
	entry.MessageIndex = 999

	// 再次 Lookup，验证内部数据未被修改
	entry2 := r.Lookup("tool_1")
	if entry2.MessageIndex != 5 {
		t.Errorf("内部数据被外部修改：MessageIndex = %d, 期望 5", entry2.MessageIndex)
	}
}

// TestCacheReference_MaxCapacity 验证 MaxCapacity 返回配置的最大容量。
func TestCacheReference_MaxCapacity(t *testing.T) {
	r := NewCacheReferenceRegistryWithCapacity(50)
	if r.MaxCapacity() != 50 {
		t.Errorf("MaxCapacity = %d, 期望 50", r.MaxCapacity())
	}

	// 默认容量
	r2 := NewCacheReferenceRegistry()
	if r2.MaxCapacity() != defaultMaxCacheRefEntries {
		t.Errorf("默认 MaxCapacity = %d, 期望 %d", r2.MaxCapacity(), defaultMaxCacheRefEntries)
	}
}
