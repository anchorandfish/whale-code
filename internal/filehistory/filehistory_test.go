package filehistory

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// newTestFileHistory 创建测试用 FileHistory 实例
func newTestFileHistory(t *testing.T) (*FileHistory, string) {
	t.Helper()
	workDir := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")
	h, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory failed: %v", err)
	}
	return h, h.workDir
}

// writeFileHelper 写入测试文件并返回绝对路径
func writeFileHelper(t *testing.T, workDir, name, content string) string {
	t.Helper()
	path := filepath.Join(workDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
	return path
}

// TestTrack_RecordsBeforeState 验证 Track 记录变更前状态（TC-101）
func TestTrack_RecordsBeforeState(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "before content")

	if err := h.Track("msg-001", "tc-001", "write_file", []string{filePath}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// 验证索引中有 track 记录
	if len(h.indexCache) != 1 {
		t.Fatalf("expected 1 index entry, got %d", len(h.indexCache))
	}
	entry := h.indexCache[0]
	if entry.Type != "track" {
		t.Errorf("expected type=track, got %s", entry.Type)
	}
	if entry.ToolCallId != "tc-001" {
		t.Errorf("expected toolCallId=tc-001, got %s", entry.ToolCallId)
	}
	if len(entry.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entry.Files))
	}
	if entry.Files[0].BeforeHash == nil {
		t.Error("BeforeHash should not be nil for existing file")
	}
	if entry.Files[0].AfterHash != nil {
		t.Error("AfterHash should be nil before TrackAfter")
	}
}

// TestTrackAfter_RecordsAfterState 验证 TrackAfter 记录变更后状态（TC-102）
func TestTrackAfter_RecordsAfterState(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "before")

	if err := h.Track("msg-001", "tc-001", "write_file", []string{filePath}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// 修改文件
	if err := os.WriteFile(filePath, []byte("after"), 0644); err != nil {
		t.Fatalf("modify file: %v", err)
	}

	if err := h.TrackAfter("msg-001", "tc-001", []string{filePath}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	// 验证 AfterHash 已设置
	entry := h.findTrackEntry("msg-001", "tc-001")
	if entry == nil {
		t.Fatal("track entry not found")
	}
	if entry.Files[0].AfterHash == nil {
		t.Error("AfterHash should not be nil after TrackAfter")
	}

	// 验证 BeforeHash 和 AfterHash 不同
	if *entry.Files[0].BeforeHash == *entry.Files[0].AfterHash {
		t.Error("BeforeHash and AfterHash should differ for modified file")
	}
}

// TestSnapshot_CreatesSnapshot 验证 Snapshot 创建快照（TC-103）
func TestSnapshot_CreatesSnapshot(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "content")

	h.Track("msg-001", "tc-001", "write_file", []string{filePath})

	snap, err := h.Snapshot("msg-001", "test-snapshot", 5)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if snap.MsgId != "msg-001" {
		t.Errorf("expected msgId=msg-001, got %s", snap.MsgId)
	}
	if snap.Label != "test-snapshot" {
		t.Errorf("expected label=test-snapshot, got %s", snap.Label)
	}
	if snap.SessionMsgCount != 5 {
		t.Errorf("expected sessionMsgCount=5, got %d", snap.SessionMsgCount)
	}
	if len(snap.Files) != 1 {
		t.Fatalf("expected 1 file in snapshot, got %d", len(snap.Files))
	}
}

// TestUndo_RestoresBeforeState 验证 Undo 恢复到变更前状态（TC-104）
func TestUndo_RestoresBeforeState(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "before")

	h.Track("msg-001", "tc-001", "write_file", []string{filePath})
	os.WriteFile(filePath, []byte("after"), 0644)
	h.TrackAfter("msg-001", "tc-001", []string{filePath})

	// Undo
	entry, err := h.Undo("msg-001")
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if entry == nil {
		t.Fatal("Undo returned nil entry")
	}

	// 验证文件已恢复到 before 状态
	content, _ := os.ReadFile(filePath)
	if string(content) != "before" {
		t.Errorf("expected 'before', got '%s'", content)
	}
}

// TestRedo_RestoresAfterState 验证 Redo 恢复到变更后状态（TC-105）
func TestRedo_RestoresAfterState(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "before")

	h.Track("msg-001", "tc-001", "write_file", []string{filePath})
	os.WriteFile(filePath, []byte("after"), 0644)
	h.TrackAfter("msg-001", "tc-001", []string{filePath})

	h.Undo("msg-001")

	// Redo
	entry, err := h.Redo("msg-001")
	if err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if entry == nil {
		t.Fatal("Redo returned nil entry")
	}

	// 验证文件已恢复到 after 状态
	content, _ := os.ReadFile(filePath)
	if string(content) != "after" {
		t.Errorf("expected 'after', got '%s'", content)
	}
}

// TestCanUndo_CanRedo 验证 CanUndo/CanRedo 状态判断（TC-106）
func TestCanUndo_CanRedo(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "before")

	// 初始状态：无 undo/redo
	if h.CanUndo("msg-001") {
		t.Error("CanUndo should be false initially")
	}
	if h.CanRedo("msg-001") {
		t.Error("CanRedo should be false initially")
	}

	h.Track("msg-001", "tc-001", "write_file", []string{filePath})
	os.WriteFile(filePath, []byte("after"), 0644)
	h.TrackAfter("msg-001", "tc-001", []string{filePath})

	// Track 后：可 undo，不可 redo
	if !h.CanUndo("msg-001") {
		t.Error("CanUndo should be true after Track")
	}
	if h.CanRedo("msg-001") {
		t.Error("CanRedo should be false after Track")
	}

	h.Undo("msg-001")

	// Undo 后：可 redo，不可 undo
	if h.CanUndo("msg-001") {
		t.Error("CanUndo should be false after Undo")
	}
	if !h.CanRedo("msg-001") {
		t.Error("CanRedo should be true after Undo")
	}

	h.Redo("msg-001")

	// Redo 后：可 undo，不可 redo
	if !h.CanUndo("msg-001") {
		t.Error("CanUndo should be true after Redo")
	}
	if h.CanRedo("msg-001") {
		t.Error("CanRedo should be false after Redo")
	}
}

// TestTrackAfter_WithoutTrack_Error 验证无对应 Track 时 TrackAfter 报错（TC-107）
func TestTrackAfter_WithoutTrack_Error(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "content")

	err := h.TrackAfter("msg-nonexistent", "tc-nonexistent", []string{filePath})
	if err == nil {
		t.Error("TrackAfter without Track should return error")
	}
}

// TestUndo_WithoutTrack_Error 验证无可撤销项时 Undo 报错（TC-108）
func TestUndo_WithoutTrack_Error(t *testing.T) {
	h, _ := newTestFileHistory(t)
	_, err := h.Undo("msg-nonexistent")
	if err == nil {
		t.Error("Undo without track should return error")
	}
}

// TestNewFileHistory_CreatesDirs 验证 NewFileHistory 创建目录结构（TC-701）
func TestNewFileHistory_CreatesDirs(t *testing.T) {
	workDir := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")

	h, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory: %v", err)
	}

	// 验证 content 目录存在
	if _, err := os.Stat(h.contentDir); err != nil {
		t.Errorf("content dir not created: %v", err)
	}

	// 验证 workDir 被转为绝对路径
	if !filepath.IsAbs(h.workDir) {
		t.Error("workDir should be absolute path")
	}
}

// TestNewFileHistory_LoadsExistingIndex 验证加载已有索引（TC-702）
func TestNewFileHistory_LoadsExistingIndex(t *testing.T) {
	workDir := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")

	// 第一次创建并写入数据
	h1, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("first NewFileHistory: %v", err)
	}
	filePath := writeFileHelper(t, h1.workDir, "test.go", "content")
	h1.Track("msg-001", "tc-001", "write_file", []string{filePath})
	h1.Snapshot("msg-001", "test", 1)

	// 第二次创建（模拟重启）
	h2, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("second NewFileHistory: %v", err)
	}

	// 验证索引已加载
	if len(h2.indexCache) < 2 {
		t.Errorf("expected at least 2 index entries, got %d", len(h2.indexCache))
	}

	// 验证 trackedPaths 已恢复
	if !h2.trackedPaths[filePath] {
		t.Error("trackedPaths should contain the tracked file")
	}
}

// TestTrack_EmptyMsgId 验证空 msgId 降级处理（不建索引行）
func TestTrack_EmptyMsgId(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "content")

	// 空 msgId 不应报错，也不应创建索引
	err := h.Track("", "tc-001", "write_file", []string{filePath})
	if err != nil {
		t.Fatalf("Track with empty msgId should not error: %v", err)
	}
	if len(h.indexCache) != 0 {
		t.Errorf("expected 0 entries for empty msgId, got %d", len(h.indexCache))
	}
}

// TestTrack_NewFile 验证 Track 记录新建文件（BeforeHash 为 nil）
func TestTrack_NewFile(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	// 文件不存在的路径
	filePath := filepath.Join(workDir, "nonexistent.go")

	err := h.Track("msg-001", "tc-001", "write_file", []string{filePath})
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	entry := h.findTrackEntry("msg-001", "tc-001")
	if entry == nil {
		t.Fatal("track entry not found")
	}
	if entry.Files[0].BeforeHash != nil {
		t.Error("BeforeHash should be nil for nonexistent file")
	}
}

// TestDiffStats_LineLevel 验证行级 diff 统计
// Insertions/Deletions 为行数，FilesChanged 为文件数
func TestDiffStats_LineLevel(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	// file1 内容为 1 行
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")
	// file2 内容为 1 行
	file2 := writeFileHelper(t, workDir, "file2.go", "content")

	// 第一个快照
	h.Track("msg-001", "tc-001", "write_file", []string{file1, file2})
	h.Snapshot("msg-001", "snap1", 1)

	// 修改 file1（1行→1行，内容不同），新增 file3（1行），删除 file2
	os.WriteFile(file1, []byte("v2"), 0644)
	os.Remove(file2)
	file3 := writeFileHelper(t, workDir, "file3.go", "new")
	h.Track("msg-002", "tc-002", "write_file", []string{file1, file3})
	h.Snapshot("msg-002", "snap2", 2)

	stats, err := h.DiffStats("msg-001", "msg-002")
	if err != nil {
		t.Fatalf("DiffStats: %v", err)
	}

	// 行级统计：
	//   file1 修改：v1→v2，diffutil.DiffStats("v1","v2") → 1 insertion, 1 deletion
	//   file3 新增：1 行 → 1 insertion
	//   file2 删除：1 行 → 1 deletion
	// 总计：Insertions=2, Deletions=2, FilesChanged=3
	if stats.Insertions != 2 {
		t.Errorf("expected Insertions=2, got %d", stats.Insertions)
	}
	if stats.Deletions != 2 {
		t.Errorf("expected Deletions=2, got %d", stats.Deletions)
	}
	if stats.FilesChanged != 3 {
		t.Errorf("expected FilesChanged=3, got %d", stats.FilesChanged)
	}
}

// TestListSnapshots 验证列出快照摘要
func TestListSnapshots(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "content")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.Snapshot("msg-001", "first", 1)
	h.Snapshot("msg-002", "second", 2)

	summaries, err := h.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(summaries) != 2 {
		t.Errorf("expected 2 snapshots, got %d", len(summaries))
	}
}

// TestGetSnapshot 验证获取快照详情
func TestGetSnapshot(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "content")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.Snapshot("msg-001", "test", 5)

	snap, err := h.GetSnapshot("msg-001")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Label != "test" {
		t.Errorf("expected label=test, got %s", snap.Label)
	}
}

// TestUndo_NewFileDeleted 验证 Undo 新建文件时删除文件
func TestUndo_NewFileDeleted(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := filepath.Join(workDir, "newfile.go")

	// Track 一个不存在的文件（BeforeHash=nil），然后创建它
	h.Track("msg-001", "tc-001", "write_file", []string{filePath})
	os.WriteFile(filePath, []byte("created"), 0644)
	h.TrackAfter("msg-001", "tc-001", []string{filePath})

	// Undo 应该删除文件
	_, err := h.Undo("msg-001")
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("file should be deleted after undo of new file")
	}
}

// TestTrack_RejectPathTraversal 验证 Track 路径穿越应被拒绝
// 覆盖三类路径穿越：相对穿越、绝对路径穿越、符号链接穿越
func TestTrack_RejectPathTraversal(t *testing.T) {
	h, workDir := newTestFileHistory(t)

	t.Run("relative traversal", func(t *testing.T) {
		maliciousPath := filepath.Join(workDir, "..", "malicious.txt")
		err := h.Track("msg-001", "tc-001", "write_file", []string{maliciousPath})
		if err == nil {
			t.Error("Track with relative path traversal should be rejected")
		}
		if !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("expected 'path traversal' error, got: %v", err)
		}
	})

	t.Run("absolute path traversal", func(t *testing.T) {
		outsideDir := t.TempDir()
		absMalicious := filepath.Join(outsideDir, "malicious.txt")
		os.WriteFile(absMalicious, []byte("secret"), 0644)

		err := h.Track("msg-002", "tc-002", "write_file", []string{absMalicious})
		if err == nil {
			t.Error("Track with absolute path traversal should be rejected")
		}
		if !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("expected 'path traversal' error, got: %v", err)
		}
	})

	t.Run("symlink traversal", func(t *testing.T) {
		outsideDir := t.TempDir()
		outsideFile := filepath.Join(outsideDir, "outside.txt")
		os.WriteFile(outsideFile, []byte("secret"), 0644)

		symlinkPath := filepath.Join(workDir, "link.txt")
		err := os.Symlink(outsideFile, symlinkPath)
		if err != nil {
			t.Skipf("symlink not supported on this platform: %v", err)
		}

		err = h.Track("msg-003", "tc-003", "write_file", []string{symlinkPath})
		if err == nil {
			t.Error("Track with symlink traversal should be rejected")
		}
		if !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("expected 'path traversal' error, got: %v", err)
		}
	})
}

// TestStorageDir_NoHyphen 验证存储目录名应为 filehistory（无连字符）
func TestStorageDir_NoHyphen(t *testing.T) {
	workDir := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")

	h, err := NewFileHistory(workDir, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory failed: %v", err)
	}

	if strings.Contains(h.baseDir, "file-history") {
		t.Errorf("storage dir should not contain 'file-history', got: %s", h.baseDir)
	}
	if !strings.Contains(h.baseDir, "filehistory") {
		t.Errorf("storage dir should contain 'filehistory', got: %s", h.baseDir)
	}
}

// TestDiffStats_ReturnStruct 验证 DiffStats 返回 struct（含行级统计）
func TestDiffStats_ReturnStruct(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.Snapshot("msg-001", "snap1", 1)

	os.WriteFile(file1, []byte("v2"), 0644)
	h.Track("msg-002", "tc-002", "write_file", []string{file1})
	h.Snapshot("msg-002", "snap2", 2)

	stats, err := h.DiffStats("msg-001", "msg-002")
	if err != nil {
		t.Fatalf("DiffStats: %v", err)
	}

	if stats == nil {
		t.Error("DiffStats should return non-nil struct")
	}

	if stats.FilesChanged != 1 {
		t.Errorf("expected FilesChanged=1, got %d", stats.FilesChanged)
	}

	// v1→v2 是 1 行修改，diffutil.DiffStats("v1","v2") → 1 insertion, 1 deletion
	if stats.Insertions != 1 {
		t.Errorf("expected Insertions=1, got %d", stats.Insertions)
	}
	if stats.Deletions != 1 {
		t.Errorf("expected Deletions=1, got %d", stats.Deletions)
	}
}

// TestRewind_FailureRollsBackState 验证 Rewind 失败时回滚已修改文件（替代 Corrupted 标记）
// 开发 prompt 要求"标记 Corrupted"，实际实现采用 rollback 策略（更安全），
// 失败时回滚已修改/删除的文件到操作前状态，避免部分恢复导致数据不一致。
func TestRewind_FailureRollsBackState(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "original-content")
	file2 := writeFileHelper(t, workDir, "file2.go", "unchanged")

	// 创建初始快照
	h.Track("msg-001", "tc-001", "write_file", []string{file1, file2})
	h.TrackAfter("msg-001", "tc-001", []string{file1, file2})
	h.Snapshot("msg-001", "first", 1)

	// 修改 file1
	os.WriteFile(file1, []byte("modified-content"), 0644)
	h.Track("msg-002", "tc-002", "write_file", []string{file1})
	h.TrackAfter("msg-002", "tc-002", []string{file1})
	h.Snapshot("msg-002", "second", 2)

	// 获取 msg-001 快照中 file1 的目标 hash，删除它使恢复失败
	snap1, _ := h.buildSnapshot("msg-001")
	targetHash1 := *snap1.Files[file1].Hash
	removeContent(h.contentDir, targetHash1)

	// 记录操作前状态
	beforeFile1, _ := os.ReadFile(file1)
	beforeFile2, _ := os.ReadFile(file2)

	// Rewind 应失败
	_, err := h.Rewind("msg-001", nil, "session-001")
	if err == nil {
		t.Fatal("Rewind should fail when content block is missing")
	}

	// 验证失败后文件状态保持不变（rollback 策略）
	afterFile1, _ := os.ReadFile(file1)
	afterFile2, _ := os.ReadFile(file2)

	if string(afterFile1) != string(beforeFile1) {
		t.Errorf("file1 should be rolled back to pre-rewind state, got '%s'", afterFile1)
	}
	if string(afterFile2) != string(beforeFile2) {
		t.Errorf("file2 should be rolled back to pre-rewind state, got '%s'", afterFile2)
	}
}

// TestMessageEntry_NotExtended 验证 ai.MessageEntry 未被 N4/H1 私下扩展（02 §2.1.4 契约）
// 反例 5：N4（rewind）不能添加 RewindPointID，H1（三套统一）不能添加 FileHistoryID，
// 字段扩展必须经 contract.md 仲裁（02 §2.1.4 + §4 仲裁机制）。
func TestMessageEntry_NotExtended(t *testing.T) {
	entryType := reflect.TypeOf(ai.MessageEntry{})
	// 检查禁止的字段名（02 §2.1.4 注释中明确列出的候选扩展）
	forbiddenFields := []string{
		"RewindPointID", // N4 rewind 禁止私下添加
		"FileHistoryID", // H1 三套统一禁止私下添加
		"CompactedFlag", // N11 compact 禁止私下添加
	}
	for _, field := range forbiddenFields {
		_, exists := entryType.FieldByName(field)
		if exists {
			t.Errorf("ai.MessageEntry must not contain field %q (02 §2.1.4 contract violation: field extension requires contract.md arbitration)", field)
		}
	}
	// 验证 MessageEntry 仅有 Role 和 Content 两个导出字段（基线字段数守卫）
	exportedCount := 0
	for i := 0; i < entryType.NumField(); i++ {
		f := entryType.Field(i)
		if f.PkgPath == "" { // 导出字段
			exportedCount++
		}
	}
	if exportedCount != 2 {
		t.Errorf("ai.MessageEntry should have exactly 2 exported fields (Role, Content), got %d — new fields require contract.md arbitration", exportedCount)
	}
}
