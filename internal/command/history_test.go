// history_test.go — 命令历史持久化测试（M09-01 改造，）
package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestHistoryStore_Record 测试记录 + 查询基本流程
func TestHistoryStore_Record(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")
	h := NewHistoryStore(path, 100)

	if err := h.Record("/help", "session-1"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if err := h.Record("/model gpt-4", "session-1"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	entries := h.Query("", 10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// 最近优先：第一条应为 /model gpt-4
	if entries[0].Command != "/model gpt-4" {
		t.Errorf("expected /model gpt-4 first, got %s", entries[0].Command)
	}
	if entries[1].Command != "/help" {
		t.Errorf("expected /help second, got %s", entries[1].Command)
	}
}

// TestHistoryStore_Query_Prefix 测试前缀过滤
func TestHistoryStore_Query_Prefix(t *testing.T) {
	h := NewHistoryStore("", 100) // 不持久化
	_ = h.Record("/help", "")
	_ = h.Record("/model gpt-4", "")
	_ = h.Record("/history", "")
	_ = h.Record("/config", "")

	// 前缀 /h 应匹配 /help 和 /history
	entries := h.Query("/h", 10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries with prefix /h, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Command != "/help" && e.Command != "/history" {
			t.Errorf("unexpected entry: %s", e.Command)
		}
	}
}

// TestHistoryStore_Query_Prefix_CaseInsensitive 测试前缀过滤大小写不敏感
func TestHistoryStore_Query_Prefix_CaseInsensitive(t *testing.T) {
	h := NewHistoryStore("", 100)
	_ = h.Record("/HELP", "")
	_ = h.Record("/model", "")

	entries := h.Query("/h", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry with prefix /h (case insensitive), got %d", len(entries))
	}
	if entries[0].Command != "/HELP" {
		t.Errorf("expected /HELP, got %s", entries[0].Command)
	}
}

// TestHistoryStore_Query_Limit 测试数量限制
func TestHistoryStore_Query_Limit(t *testing.T) {
	h := NewHistoryStore("", 100)
	for i := 0; i < 50; i++ {
		_ = h.Record("/cmd", "")
	}

	entries := h.Query("", 10)
	if len(entries) != 10 {
		t.Fatalf("expected 10 entries with limit 10, got %d", len(entries))
	}
}

// TestHistoryStore_Reload 测试 Reloadable 接口
func TestHistoryStore_Reload(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")

	// 先写一个文件
	h1 := NewHistoryStore(path, 100)
	_ = h1.Record("/help", "session-1")
	_ = h1.Record("/model", "session-1")

	// 创建新的 store（内存为空），通过 Reload 加载文件
	h2 := NewHistoryStore("", 100)
	if err := h2.Reload(path); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	entries := h2.Query("", 10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after Reload, got %d", len(entries))
	}
}

// TestHistoryStore_FileNotFound 测试文件不存在降级
func TestHistoryStore_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.jsonl")

	// 文件不存在时应正常构造，返回空历史
	h := NewHistoryStore(path, 100)
	entries := h.Query("", 10)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nonexistent file, got %d", len(entries))
	}
}

// TestHistoryStore_CorruptLine 测试损坏行跳过
func TestHistoryStore_CorruptLine(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")

	// 写入 1 行正常 + 1 行损坏 + 1 行正常
	validEntry, _ := json.Marshal(HistoryEntry{Command: "/help", Timestamp: time.Now()})
	corruptLine := []byte("{invalid json}")
	validEntry2, _ := json.Marshal(HistoryEntry{Command: "/model", Timestamp: time.Now()})
	content := append(validEntry, '\n')
	content = append(content, corruptLine...)
	content = append(content, '\n')
	content = append(content, validEntry2...)
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	h := NewHistoryStore(path, 100)
	entries := h.Query("", 10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (corrupt line skipped), got %d", len(entries))
	}
}

// TestHistoryStore_MaxEntries 测试 FIFO 淘汰
func TestHistoryStore_MaxEntries(t *testing.T) {
	h := NewHistoryStore("", 5) // 仅内存，最多 5 条
	for i := 0; i < 10; i++ {
		_ = h.Record("/cmd", "")
	}
	entries := h.Query("", 100)
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries after FIFO eviction, got %d", len(entries))
	}
}

// TestHistoryStore_ConcurrentRecord 测试并发记录（race 检测）
func TestHistoryStore_ConcurrentRecord(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")
	h := NewHistoryStore(path, 1000)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = h.Record("/concurrent", "")
		}(i)
	}
	wg.Wait()

	entries := h.Query("", 1000)
	if len(entries) != 20 {
		t.Errorf("expected 20 entries, got %d", len(entries))
	}
}

// TestHistoryStore_EmptyCmd 测试空命令不记录
func TestHistoryStore_EmptyCmd(t *testing.T) {
	h := NewHistoryStore("", 100)
	if err := h.Record("", "session-1"); err != nil {
		t.Errorf("Record empty cmd should not error, got %v", err)
	}
	entries := h.Query("", 10)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty cmd, got %d", len(entries))
	}
}

// TestHistoryStore_Reload_PathEmpty 测试 Reload 空路径报错
func TestHistoryStore_Reload_PathEmpty(t *testing.T) {
	h := NewHistoryStore("", 100)
	err := h.Reload("")
	if err == nil {
		t.Error("expected error for empty path, got nil")
	}
}
