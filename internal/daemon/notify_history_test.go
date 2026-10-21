// Package daemon — M24-05 NotifyHistory 测试（SNAPSHOT）
//
// 测试覆盖：
// - Append 追加 + 自动 ID/Timestamp
// - Query 按类型/时间范围/limit 过滤
// - Cleanup 清理过期
// - MaxEntries 淘汰最旧
// - Save/Load 持久化往返
// - Load 文件不存在
// - 纯内存模式（无 persistPath）
package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

// TestNotifyHistory_AppendAutoID 自动生成 ID
func TestNotifyHistory_AppendAutoID(t *testing.T) {
	h := NewNotifyHistory("")
	err := h.Append(NotifyEntry{
		Type:    EventTaskCompleted,
		Payload: "test",
	})
	if err != nil {
		t.Fatalf("Append err: %v", err)
	}
	if h.Size() != 1 {
		t.Fatalf("Size = %d, want 1", h.Size())
	}
	entries := h.Query(NotifyQuery{Type: EventTaskCompleted})
	if len(entries) != 1 {
		t.Fatalf("Query len = %d, want 1", len(entries))
	}
	if entries[0].ID == "" {
		t.Error("ID is empty, want auto-generated")
	}
	if entries[0].Timestamp == 0 {
		t.Error("Timestamp = 0, want auto-filled")
	}
}

// TestNotifyHistory_AppendExplicitID 显式 ID 不被覆盖
func TestNotifyHistory_AppendExplicitID(t *testing.T) {
	h := NewNotifyHistory("")
	err := h.Append(NotifyEntry{
		ID:   "custom-id",
		Type: EventTaskCompleted,
	})
	if err != nil {
		t.Fatalf("Append err: %v", err)
	}
	entries := h.Query(NotifyQuery{})
	if entries[0].ID != "custom-id" {
		t.Errorf("ID = %q, want custom-id", entries[0].ID)
	}
}

// TestNotifyHistory_QueryByType 按类型过滤
func TestNotifyHistory_QueryByType(t *testing.T) {
	h := NewNotifyHistory("")
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 1000})
	_ = h.Append(NotifyEntry{Type: EventTaskFailed, Timestamp: 2000})
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 3000})

	completed := h.Query(NotifyQuery{Type: EventTaskCompleted})
	if len(completed) != 2 {
		t.Errorf("completed len = %d, want 2", len(completed))
	}
	failed := h.Query(NotifyQuery{Type: EventTaskFailed})
	if len(failed) != 1 {
		t.Errorf("failed len = %d, want 1", len(failed))
	}
}

// TestNotifyHistory_QueryByTimeRange 按时间范围过滤
func TestNotifyHistory_QueryByTimeRange(t *testing.T) {
	h := NewNotifyHistory("")
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 1000})
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 2000})
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 3000})

	// 查询 [1500, 2500] 范围
	result := h.Query(NotifyQuery{StartTime: 1500, EndTime: 2500})
	if len(result) != 1 {
		t.Errorf("time range query len = %d, want 1", len(result))
	}
	if result[0].Timestamp != 2000 {
		t.Errorf("Timestamp = %d, want 2000", result[0].Timestamp)
	}
}

// TestNotifyHistory_QueryLimit limit 限制
func TestNotifyHistory_QueryLimit(t *testing.T) {
	h := NewNotifyHistory("")
	for i := 0; i < 10; i++ {
		_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: int64(i + 1)})
	}

	result := h.Query(NotifyQuery{Limit: 3})
	if len(result) != 3 {
		t.Errorf("limit query len = %d, want 3", len(result))
	}
	// 应返回最新的 3 条（按时间降序）
	if result[0].Timestamp != 10 {
		t.Errorf("result[0].Timestamp = %d, want 10 (newest first)", result[0].Timestamp)
	}
}

// TestNotifyHistory_Cleanup 清理过期
func TestNotifyHistory_Cleanup(t *testing.T) {
	h := NewNotifyHistory("")
	h.SetTTL(1 * time.Hour)

	// 2 小时前的条目（应被清理）
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: old})
	// 当前条目（应保留）
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: time.Now().UnixMilli()})

	removed := h.Cleanup()
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if h.Size() != 1 {
		t.Errorf("Size after cleanup = %d, want 1", h.Size())
	}
}

// TestNotifyHistory_MaxEntriesEvict 超过 MaxEntries 淘汰最旧
func TestNotifyHistory_MaxEntriesEvict(t *testing.T) {
	h := NewNotifyHistory("")
	h.SetMaxEntries(3)

	for i := 0; i < 5; i++ {
		_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: int64(i + 1)})
	}

	if h.Size() != 3 {
		t.Errorf("Size = %d, want 3 (max entries)", h.Size())
	}

	// 应保留最新的 3 条（Timestamp 3, 4, 5）
	entries := h.Query(NotifyQuery{})
	if entries[0].Timestamp != 5 {
		t.Errorf("newest Timestamp = %d, want 5", entries[0].Timestamp)
	}
}

// TestNotifyHistory_PersistAndLoad 持久化往返
func TestNotifyHistory_PersistAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.jsonl")
	h1 := NewNotifyHistory(path)
	_ = h1.Append(NotifyEntry{Type: EventTaskCompleted, Payload: "event1", Timestamp: 1000})
	_ = h1.Append(NotifyEntry{Type: EventTaskFailed, Payload: "event2", Timestamp: 2000})

	// 新实例加载
	h2 := NewNotifyHistory(path)
	if err := h2.Load(); err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if h2.Size() != 2 {
		t.Errorf("after load Size = %d, want 2", h2.Size())
	}

	// 验证内容
	entries := h2.Query(NotifyQuery{})
	if len(entries) != 2 {
		t.Fatalf("Query len = %d, want 2", len(entries))
	}
}

// TestNotifyHistory_LoadMissingFile 文件不存在返回 nil
func TestNotifyHistory_LoadMissingFile(t *testing.T) {
	h := NewNotifyHistory(filepath.Join(t.TempDir(), "missing.jsonl"))
	err := h.Load()
	if err != nil {
		t.Errorf("Load missing file err: %v, want nil", err)
	}
	if h.Size() != 0 {
		t.Errorf("Size = %d, want 0", h.Size())
	}
}

// TestNotifyHistory_MemoryOnlyMode 无 persistPath 时不持久化
func TestNotifyHistory_MemoryOnlyMode(t *testing.T) {
	h := NewNotifyHistory("") // 纯内存
	err := h.Append(NotifyEntry{Type: EventTaskCompleted})
	if err != nil {
		t.Fatalf("Append err: %v", err)
	}
	// 纯内存模式 Append 不应尝试写文件，err 始终为 nil
}

// TestNotifyHistory_Save 全量保存
func TestNotifyHistory_Save(t *testing.T) {
	path := filepath.Join(t.TempDir(), "save.jsonl")
	h := NewNotifyHistory(path)
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 1000})

	if err := h.Save(); err != nil {
		t.Fatalf("Save err: %v", err)
	}

	// 验证文件存在且可加载
	h2 := NewNotifyHistory(path)
	if err := h2.Load(); err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if h2.Size() != 1 {
		t.Errorf("after load Size = %d, want 1", h2.Size())
	}
}

// TestNotifyHistory_QueryAll 查询所有（无过滤条件）
func TestNotifyHistory_QueryAll(t *testing.T) {
	h := NewNotifyHistory("")
	_ = h.Append(NotifyEntry{Type: EventTaskCompleted, Timestamp: 1000})
	_ = h.Append(NotifyEntry{Type: EventTaskFailed, Timestamp: 2000})
	_ = h.Append(NotifyEntry{Type: EventTaskStarted, Timestamp: 3000})

	all := h.Query(NotifyQuery{})
	if len(all) != 3 {
		t.Errorf("all len = %d, want 3", len(all))
	}
	// 最新在前
	if all[0].Timestamp != 3000 {
		t.Errorf("all[0].Timestamp = %d, want 3000 (newest first)", all[0].Timestamp)
	}
}
