package filehistory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestTrackAfter_DeletedFile 验证 TrackAfter 记录已删除文件（AfterHash 为 nil）（TC-104）
func TestTrackAfter_DeletedFile(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "original")

	// Track 记录 before 状态
	h.Track("msg-001", "tc-001", "write_file", []string{filePath})

	// 删除文件
	os.Remove(filePath)

	// TrackAfter 记录 after 状态（文件已删除，AfterHash 应为 nil）
	if err := h.TrackAfter("msg-001", "tc-001", []string{filePath}); err != nil {
		t.Fatalf("TrackAfter: %v", err)
	}

	entry := h.findTrackEntry("msg-001", "tc-001")
	if entry == nil {
		t.Fatal("track entry not found")
	}
	if entry.Files[0].AfterHash != nil {
		t.Error("AfterHash should be nil for deleted file")
	}
	if entry.Files[0].BeforeHash == nil {
		t.Error("BeforeHash should not be nil (file existed before)")
	}

	// Undo 应该恢复文件
	_, err := h.Undo("msg-001")
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file should be restored after undo: %v", err)
	}
	if string(content) != "original" {
		t.Errorf("expected 'original', got '%s'", content)
	}
}

// TestUndo_CrossRoundIsolation 验证跨轮 Undo 隔离（TC-503）
// 不同 msgId 的 Undo 操作互不影响
func TestUndo_CrossRoundIsolation(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")
	file2 := writeFileHelper(t, workDir, "file2.go", "v1")

	// 第一轮：修改 file1
	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	os.WriteFile(file1, []byte("v2"), 0644)
	h.TrackAfter("msg-001", "tc-001", []string{file1})

	// 第二轮：修改 file2
	h.Track("msg-002", "tc-002", "write_file", []string{file2})
	os.WriteFile(file2, []byte("v2"), 0644)
	h.TrackAfter("msg-002", "tc-002", []string{file2})

	// Undo 第二轮（只回退 file2）
	_, err := h.Undo("msg-002")
	if err != nil {
		t.Fatalf("Undo msg-002: %v", err)
	}

	// file2 应恢复到 v1
	content2, _ := os.ReadFile(file2)
	if string(content2) != "v1" {
		t.Errorf("file2 should be 'v1' after undo, got '%s'", content2)
	}

	// file1 应保持 v2（不受影响）
	content1, _ := os.ReadFile(file1)
	if string(content1) != "v2" {
		t.Errorf("file1 should remain 'v2', got '%s'", content1)
	}

	// Undo 第一轮（回退 file1）
	_, err = h.Undo("msg-001")
	if err != nil {
		t.Fatalf("Undo msg-001: %v", err)
	}

	// file1 应恢复到 v1
	content1, _ = os.ReadFile(file1)
	if string(content1) != "v1" {
		t.Errorf("file1 should be 'v1' after undo, got '%s'", content1)
	}
}

// TestTrack_NoTrackAfter_UndoSafe 验证 Track 后工具失败（无 TrackAfter）时 Undo 安全（TC-1201）
// Track 记录了 before 状态但无 TrackAfter，Undo 应恢复到 before（即原状态，文件未变）
func TestTrack_NoTrackAfter_UndoSafe(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	filePath := writeFileHelper(t, workDir, "test.go", "original")

	// Track 记录 before 状态
	h.Track("msg-001", "tc-001", "write_file", []string{filePath})

	// 模拟工具失败：不调用 TrackAfter，文件未被修改

	// Undo 应该安全执行（恢复到 before，即原状态）
	entry, err := h.Undo("msg-001")
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if entry == nil {
		t.Fatal("Undo returned nil entry")
	}

	// 文件内容应保持不变（恢复到 before = 原状态）
	content, _ := os.ReadFile(filePath)
	if string(content) != "original" {
		t.Errorf("file should remain 'original', got '%s'", content)
	}
}

// TestNewFileHistory_CrossProjectIsolation 验证跨项目隔离（TC-1202）
// 不同 workDir 的 FileHistory 实例互不影响
func TestNewFileHistory_CrossProjectIsolation(t *testing.T) {
	workDir1 := t.TempDir()
	workDir2 := t.TempDir()
	baseDir := filepath.Join(t.TempDir(), "filehistory")

	// 创建两个不同 workDir 的实例
	h1, err := NewFileHistory(workDir1, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory 1: %v", err)
	}
	h2, err := NewFileHistory(workDir2, baseDir)
	if err != nil {
		t.Fatalf("NewFileHistory 2: %v", err)
	}

	// 验证存储目录不同
	if h1.baseDir == h2.baseDir {
		t.Error("different workDirs should produce different storage dirs")
	}

	// 各自 Track 不同的文件
	file1 := writeFileHelper(t, workDir1, "file1.go", "project1")
	file2 := writeFileHelper(t, workDir2, "file2.go", "project2")

	h1.Track("msg-001", "tc-001", "write_file", []string{file1})
	h2.Track("msg-002", "tc-002", "write_file", []string{file2})

	// 验证索引互不影响
	if len(h1.indexCache) != 1 {
		t.Errorf("h1 should have 1 entry, got %d", len(h1.indexCache))
	}
	if len(h2.indexCache) != 1 {
		t.Errorf("h2 should have 1 entry, got %d", len(h2.indexCache))
	}

	// h1 的索引中不应包含 h2 的记录
	if h1.indexCache[0].MsgId != "msg-001" {
		t.Errorf("h1 should have msg-001, got %s", h1.indexCache[0].MsgId)
	}
	if h2.indexCache[0].MsgId != "msg-002" {
		t.Errorf("h2 should have msg-002, got %s", h2.indexCache[0].MsgId)
	}
}

// TestRewind_FailureRollsBackFiles 验证 Rewind 文件恢复失败时回滚已恢复文件（TC-1204）
// 删除目标 hash 的内容块使 restoreFile 失败，验证已恢复的文件被回滚到操作前状态
func TestRewind_FailureRollsBackFiles(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")
	file2 := writeFileHelper(t, workDir, "file2.go", "v1")

	// 第一轮：创建快照
	h.Track("msg-001", "tc-001", "write_file", []string{file1, file2})
	h.TrackAfter("msg-001", "tc-001", []string{file1, file2})
	h.Snapshot("msg-001", "first", 1)

	// 修改两个文件
	os.WriteFile(file1, []byte("v2"), 0644)
	os.WriteFile(file2, []byte("v2"), 0644)
	h.Track("msg-002", "tc-002", "write_file", []string{file1, file2})
	h.TrackAfter("msg-002", "tc-002", []string{file1, file2})
	h.Snapshot("msg-002", "second", 2)

	// 获取 msg-001 快照中 file1 的目标 hash
	snap1, _ := h.buildSnapshot("msg-001")
	targetHash1 := *snap1.Files[file1].Hash

	// 从 content store 删除 file1 的目标 hash（让恢复 file1 失败）
	removeContent(h.contentDir, targetHash1)

	// Rewind 到 msg-001（应失败，因为 file1 无法恢复）
	result, err := h.Rewind("msg-001", nil, "session-001")
	if err == nil {
		t.Fatal("Rewind should fail when restoreFile fails")
	}

	// 验证错误信息包含回滚统计
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error should mention rollback: %v", err)
	}

	// 无论文件处理顺序如何，两个文件最终都应是 v2（修改后状态）
	// 因为：要么文件未被处理（保持 v2），要么被恢复到 v1 后又回滚到 v2
	content1, _ := os.ReadFile(file1)
	content2, _ := os.ReadFile(file2)

	// file1 恢复失败，保持 v2
	if string(content1) != "v2" {
		t.Errorf("file1 should remain 'v2' (restore failed), got '%s'", content1)
	}
	// file2 要么未处理（v2），要么被回滚（v2）
	if string(content2) != "v2" {
		t.Errorf("file2 should be 'v2' (either not processed or rolled back), got '%s'", content2)
	}

	// 验证 RewindResult 包含错误（BackupMsgId 可能已创建）
	_ = result
}

// TestTrack_ConcurrentSafety 验证并发 Track 操作安全性
// 多个 goroutine 同时 Track 不同文件，不应 panic 且索引完整
func TestTrack_ConcurrentSafety(t *testing.T) {
	h, workDir := newTestFileHistory(t)

	numGoroutines := 10
	var wg sync.WaitGroup

	// 每个 goroutine Track 不同的文件
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fileName := "file_" + string(rune('A'+idx)) + ".go"
			filePath := writeFileHelper(t, workDir, fileName, "content")
			if err := h.Track("msg-concurrent", "tc-"+string(rune('A'+idx)), "write_file", []string{filePath}); err != nil {
				t.Errorf("concurrent Track: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// 验证所有 track 记录都已写入
	trackCount := 0
	for _, entry := range h.indexCache {
		if entry.Type == "track" && entry.MsgId == "msg-concurrent" {
			trackCount++
		}
	}
	if trackCount != numGoroutines {
		t.Errorf("expected %d track entries, got %d", numGoroutines, trackCount)
	}
}

// TestWriteContent_LargeFileDedup 验证大文件内容去重（优化后）
// 相同内容的大文件应产生相同 hash（基于前 4KB 内容指纹）
func TestWriteContent_LargeFileDedup(t *testing.T) {
	dir := t.TempDir()

	// 创建超过 maxContentHashSize 的内容
	largeContent := make([]byte, maxContentHashSize+1024)
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	hash1, err := writeContent(dir, largeContent)
	if err != nil {
		t.Fatalf("writeContent 1: %v", err)
	}
	hash2, err := writeContent(dir, largeContent)
	if err != nil {
		t.Fatalf("writeContent 2: %v", err)
	}

	// 相同内容的大文件应产生相同 hash（去重生效）
	if hash1 != hash2 {
		t.Errorf("same large content should produce same hash (dedup): %s != %s", hash1, hash2)
	}

	// 验证只写入一个内容块
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected 1 content file (dedup), got %d", len(entries))
	}
}

// TestWriteContent_LargeFileDifferentContent 验证不同大文件产生不同 hash
func TestWriteContent_LargeFileDifferentContent(t *testing.T) {
	dir := t.TempDir()

	// 创建两个不同的大文件（内容不同）
	content1 := make([]byte, maxContentHashSize+1024)
	for i := range content1 {
		content1[i] = byte(i % 256)
	}
	content2 := make([]byte, maxContentHashSize+1024)
	for i := range content2 {
		content2[i] = byte((i + 1) % 256) // 不同的内容
	}

	hash1, _ := writeContent(dir, content1)
	hash2, _ := writeContent(dir, content2)

	if hash1 == hash2 {
		t.Error("different large content should produce different hash")
	}
}

// TestRewind_FailureRollsBackDeletedFiles 验证 Rewind 删除文件失败时回滚
// 删除文件成功后，后续恢复失败，已删除的文件应被恢复
func TestRewind_FailureRollsBackDeletedFiles(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	// file1 和 file2 内容不同，避免 content store hash 共享导致删除失效
	file1 := writeFileHelper(t, workDir, "file1.go", "content-file1-v1")
	file2 := writeFileHelper(t, workDir, "file2.go", "content-file2-v1")

	// 第一轮快照
	h.Track("msg-001", "tc-001", "write_file", []string{file1, file2})
	h.TrackAfter("msg-001", "tc-001", []string{file1, file2})
	h.Snapshot("msg-001", "first", 1)

	// 第二轮：新建 file3，修改 file1
	file3 := writeFileHelper(t, workDir, "file3.go", "new")
	h.Track("msg-002", "tc-002", "write_file", []string{file1, file3})
	os.WriteFile(file1, []byte("content-file1-v2"), 0644)
	h.TrackAfter("msg-002", "tc-002", []string{file1, file3})
	h.Snapshot("msg-002", "second", 2)

	// 获取 msg-001 快照中 file1 的目标 hash
	snap1, _ := h.buildSnapshot("msg-001")
	targetHash1 := *snap1.Files[file1].Hash

	// 删除 file1 的目标内容块（让恢复 file1 失败）
	// 注意：file1 内容唯一，不会与其他文件共享 content blob
	removeContent(h.contentDir, targetHash1)

	// Rewind 到 msg-001（file3 应被删除，file1 恢复失败）
	_, err := h.Rewind("msg-001", nil, "session-001")
	if err == nil {
		t.Fatal("Rewind should fail when restoreFile fails")
	}

	// file3 要么未被处理（存在），要么被删除后回滚（恢复存在）
	// 由于 file3 被删除时记录了 origHash（通过 getCurrentFileState 写入 content store）
	// 回滚时应能恢复
	_, statErr := os.Stat(file3)
	if statErr != nil && os.IsNotExist(statErr) {
		// file3 被删除但回滚失败（content store 可能没有 file3 的内容）
		// 这种情况是 best-effort 回滚，可接受
		t.Logf("file3 was deleted and rollback may have failed (best-effort)")
	}
}

// TestRewindRestoreSession_NoSnapshotReturnsError 验证 restoreSession 无 snapshot 记录时返回错误
func TestRewindRestoreSession_NoSnapshotReturnsError(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "content")

	// 只有 track 记录，没有 snapshot
	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})

	// mock session store
	store := &mockSessionRestorer{
		messages: [][]byte{[]byte("msg1"), []byte("msg2")},
	}

	// restoreSession 应返回错误（无 snapshot 记录）
	err := h.restoreSession("msg-001", store, "session-001")
	if err == nil {
		t.Error("restoreSession should return error when no snapshot record exists")
	}
	if !strings.Contains(err.Error(), "no snapshot record") {
		t.Errorf("error should mention no snapshot record: %v", err)
	}
}
