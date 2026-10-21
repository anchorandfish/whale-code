// Package filehistory — cross_session_test.go 跨会话快照读取/恢复测试（候选 A）。
package filehistory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGetSnapshotFromSession_NotFound 验证源会话不存在或无快照时返回 error。
func TestGetSnapshotFromSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	h, err := NewFileHistory(tmpDir, filepath.Join(tmpDir, "fh"))
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	// 不存在的源会话 + msgId
	_, err = h.GetSnapshotFromSession("nonexistent-session", "nonexistent-msg")
	if err == nil {
		t.Error("GetSnapshotFromSession with nonexistent session should return error")
	}
}

// TestGetSnapshotFromSession_EmptyArgs 验证空参数返回 error。
func TestGetSnapshotFromSession_EmptyArgs(t *testing.T) {
	tmpDir := t.TempDir()
	h, err := NewFileHistory(tmpDir, filepath.Join(tmpDir, "fh"))
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	_, err = h.GetSnapshotFromSession("", "msg-1")
	if err == nil {
		t.Error("GetSnapshotFromSession with empty sessionId should return error")
	}
	_, err = h.GetSnapshotFromSession("session-A", "")
	if err == nil {
		t.Error("GetSnapshotFromSession with empty msgId should return error")
	}
}

// TestGetSnapshotFromSession_CrossSessionRead 验证跨会话快照读取。
// 场景：在 session-A 创建快照 → 切换到 session-B → 从 session-B 读取 session-A 的快照。
func TestGetSnapshotFromSession_CrossSessionRead(t *testing.T) {
	tmpDir := t.TempDir()
	h, err := NewFileHistory(tmpDir, filepath.Join(tmpDir, "fh"))
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	// 切换到 session-A
	if err := h.SetCurrentSession("session-A"); err != nil {
		t.Fatalf("SetCurrentSession(session-A): %v", err)
	}

	// 在 session-A 创建一个测试文件并追踪
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := h.Track("msg-1", "tool-call-1", "test_tool", []string{testFile}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// 在 session-A 创建快照
	snap, err := h.Snapshot("snap-1", "test-snapshot", 1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}

	// 切换到 session-B
	if err := h.SetCurrentSession("session-B"); err != nil {
		t.Fatalf("SetCurrentSession(session-B): %v", err)
	}

	// 从 session-B 读取 session-A 的快照（跨会话）
	crossSnap, err := h.GetSnapshotFromSession("session-A", "snap-1")
	if err != nil {
		t.Fatalf("GetSnapshotFromSession(session-A, snap-1): %v", err)
	}
	if crossSnap == nil {
		t.Fatal("GetSnapshotFromSession returned nil snapshot")
	}
	if crossSnap.MsgId != "snap-1" {
		t.Errorf("crossSnap.MsgId = %q, want snap-1", crossSnap.MsgId)
	}
	if crossSnap.Label != "test-snapshot" {
		t.Errorf("crossSnap.Label = %q, want test-snapshot", crossSnap.Label)
	}
}

// TestRestoreFromSnapshot_NilSnapshot 验证 nil 快照返回 error。
func TestRestoreFromSnapshot_NilSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	h, err := NewFileHistory(tmpDir, filepath.Join(tmpDir, "fh"))
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	_, err = h.RestoreFromSnapshot(nil, nil, "session-A")
	if err == nil {
		t.Error("RestoreFromSnapshot with nil snapshot should return error")
	}
}

// TestRestoreFromSnapshot_NoSessionStore 验证无 SessionRestorer 时仅恢复文件。
func TestRestoreFromSnapshot_NoSessionStore(t *testing.T) {
	tmpDir := t.TempDir()
	h, err := NewFileHistory(tmpDir, filepath.Join(tmpDir, "fh"))
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	// 构造空快照（无文件变更）
	snap := &Snapshot{
		MsgId:           "test-msg",
		Files:           make(map[string]*FileState),
		SessionMsgCount: 0,
	}

	// 无 SessionRestorer → 仅恢复文件（无文件变更 → 0 restored）
	result, err := h.RestoreFromSnapshot(snap, nil, "session-A")
	if err != nil {
		t.Fatalf("RestoreFromSnapshot with nil sessionStore: %v", err)
	}
	if result == nil {
		t.Fatal("RestoreFromSnapshot returned nil result")
	}
	if result.SessionRestored {
		t.Error("SessionRestored should be false with nil sessionStore")
	}
}
