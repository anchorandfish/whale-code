package filehistory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFullGC_RemovesUnreferenced 验证 fullGC 删除无引用的内容块（TC-501）
func TestFullGC_RemovesUnreferenced(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "tracked content")

	// 写入有引用的内容
	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	h.Snapshot("msg-001", "first", 1)

	// 写入无引用的内容（孤儿块）
	writeContent(h.contentDir, []byte("orphan content 1"))
	writeContent(h.contentDir, []byte("orphan content 2"))

	// 执行 GC
	removed, freed, err := h.fullGC()
	if err != nil {
		t.Fatalf("fullGC: %v", err)
	}

	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}
	if freed <= 0 {
		t.Error("expected positive freed bytes")
	}
}

// TestFullGC_PreservesReferenced 验证 fullGC 保留有引用的内容块（TC-504）
func TestFullGC_PreservesReferenced(t *testing.T) {
	h, workDir := newTestFileHistory(t)
	file1 := writeFileHelper(t, workDir, "file1.go", "v1")

	h.Track("msg-001", "tc-001", "write_file", []string{file1})
	h.TrackAfter("msg-001", "tc-001", []string{file1})
	h.Snapshot("msg-001", "first", 1)

	// 记录被引用的 hash
	entry := h.findTrackEntry("msg-001", "tc-001")
	if entry == nil {
		t.Fatal("track entry not found")
	}
	referencedHash := *entry.Files[0].BeforeHash

	// 执行 GC
	removed, _, err := h.fullGC()
	if err != nil {
		t.Fatalf("fullGC: %v", err)
	}

	if removed != 0 {
		t.Errorf("expected 0 removed (all referenced), got %d", removed)
	}

	// 验证被引用的内容仍存在
	if !contentExists(h.contentDir, referencedHash) {
		t.Error("referenced content should still exist after GC")
	}
}

// TestLazyGC_ProcessesPending 验证懒回收处理 gc-pending 清单（TC-502）
func TestLazyGC_ProcessesPending(t *testing.T) {
	h, _ := newTestFileHistory(t)

	// 写入一些无引用的内容
	hash1, _ := writeContent(h.contentDir, []byte("pending content 1"))
	hash2, _ := writeContent(h.contentDir, []byte("pending content 2"))

	// 标记为待 GC
	if err := h.markForGC([]string{hash1, hash2}); err != nil {
		t.Fatalf("markForGC: %v", err)
	}

	// 验证 gc-pending 文件存在
	if _, err := os.Stat(h.gcPendingFile); err != nil {
		t.Fatalf("gc-pending file not created: %v", err)
	}

	// 执行懒回收
	removed, _, err := h.lazyGC()
	if err != nil {
		t.Fatalf("lazyGC: %v", err)
	}

	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	// 验证内容已被删除
	if contentExists(h.contentDir, hash1) {
		t.Error("content1 should be removed by lazyGC")
	}
	if contentExists(h.contentDir, hash2) {
		t.Error("content2 should be removed by lazyGC")
	}
}

// TestEmergencyGC_ByLRU 验证紧急 GC 按 LRU 删除（TC-503）
func TestEmergencyGC_ByLRU(t *testing.T) {
	h, _ := newTestFileHistory(t)

	// 写入多个无引用的内容块
	for i := 0; i < 5; i++ {
		writeContent(h.contentDir, []byte("unreferenced content "+string(rune('A'+i))))
	}

	// 获取当前目录大小
	currentSize, _ := contentDirSize(h.contentDir)
	if currentSize == 0 {
		t.Fatal("expected non-zero content dir size")
	}

	// 设置一个较低的阈值，触发紧急 GC
	maxBytes := currentSize / 2
	removed, freed, err := h.emergencyGC(maxBytes)
	if err != nil {
		t.Fatalf("emergencyGC: %v", err)
	}

	if removed == 0 {
		t.Error("expected at least 1 removed by emergencyGC")
	}
	if freed <= 0 {
		t.Error("expected positive freed bytes")
	}

	// 验证目录大小已降低
	newSize, _ := contentDirSize(h.contentDir)
	if newSize >= currentSize {
		t.Error("content dir size should decrease after emergencyGC")
	}
}

// TestContentDirSize 验证计算目录大小
func TestContentDirSize(t *testing.T) {
	dir := t.TempDir()

	// 写入几个文件
	os.WriteFile(filepath.Join(dir, "file1"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(dir, "file2"), []byte("world!"), 0644)

	size, err := contentDirSize(dir)
	if err != nil {
		t.Fatalf("contentDirSize: %v", err)
	}

	// "hello" = 5 bytes, "world!" = 6 bytes
	if size != 11 {
		t.Errorf("expected size=11, got %d", size)
	}
}

// TestContentDirSize_Empty 验证空目录大小为 0
func TestContentDirSize_Empty(t *testing.T) {
	dir := t.TempDir()

	size, err := contentDirSize(dir)
	if err != nil {
		t.Fatalf("contentDirSize: %v", err)
	}
	if size != 0 {
		t.Errorf("expected 0 for empty dir, got %d", size)
	}
}

// TestGC_PublicMethod 验证公共 GC 方法
func TestGC_PublicMethod(t *testing.T) {
	h, _ := newTestFileHistory(t)

	// 写入无引用内容
	writeContent(h.contentDir, []byte("orphan"))

	removed, freed, err := h.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if removed < 1 {
		t.Errorf("expected at least 1 removed, got %d", removed)
	}
	if freed <= 0 {
		t.Error("expected positive freed bytes")
	}
}

// TestMarkForGC_Empty 验证空列表标记为 no-op
func TestMarkForGC_Empty(t *testing.T) {
	h, _ := newTestFileHistory(t)

	err := h.markForGC(nil)
	if err != nil {
		t.Errorf("markForGC with nil should not error: %v", err)
	}

	// gc-pending 文件不应该被创建
	if _, err := os.Stat(h.gcPendingFile); !os.IsNotExist(err) {
		t.Error("gc-pending file should not be created for empty list")
	}
}
