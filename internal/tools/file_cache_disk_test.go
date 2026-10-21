// Package tools — 文件缓存磁盘持久化测试（T1-P1-B4）
//
// 测试矩阵 30 个用例，覆盖：
// - Put/Get/Delete 基础（8）
// - LRU 淘汰（5）
// - TTL 过期（3）
// - 跨平台路径（4）
// - 持久化恢复（4）
// - 孤儿 GC（2）
// - SHA256 校验（2）
// - DeleteByPrefix（2）
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestDiskStore 创建测试用 DiskStore。
// MaxDiskBytes=1024 + MaxDiskEntries=5 便于触发 LRU 淘汰测试。
// TTL=0 默认不过期，TTL 测试单独覆盖。
func newTestDiskStore(t testing.TB) (*DiskStore, string) {
	t.Helper()
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.MaxDiskBytes = 1024
	config.MaxDiskEntries = 5
	config.TTL = 0
	store, err := NewDiskStore(config)
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}
	return store, tmpDir
}

// ============================================================
// §1 Put/Get/Delete 基础（8 个用例）
// ============================================================

// TestDiskStore_PutAndGet 基础 Put + Get 流程
func TestDiskStore_PutAndGet(t *testing.T) {
	store, _ := newTestDiskStore(t)
	content := []byte("hello world")
	sha := computeSHA256(content)

	if err := store.Put("/tmp/test.txt", content, sha, int64(len(content)), time.Now(), 1, 2); err != nil {
		t.Fatalf("Put: %v", err)
	}

	result, ok := store.Get("/tmp/test.txt")
	if !ok {
		t.Fatal("expected hit, got miss")
	}
	if result.Content != string(content) {
		t.Errorf("content mismatch: got %q, want %q", result.Content, string(content))
	}
	if result.Entry.SHA256 != sha {
		t.Errorf("sha256 mismatch: got %s, want %s", result.Entry.SHA256, sha)
	}
	if result.Entry.HitCount != 1 {
		t.Errorf("expected HitCount=1 after Get, got %d", result.Entry.HitCount)
	}
}

// TestDiskStore_GetMiss 未命中场景
func TestDiskStore_GetMiss(t *testing.T) {
	store, _ := newTestDiskStore(t)

	_, ok := store.Get("/nonexistent/path")
	if ok {
		t.Error("expected miss for non-existent path")
	}

	metrics := store.Metrics()
	if metrics.DiskMisses != 1 {
		t.Errorf("expected DiskMisses=1, got %d", metrics.DiskMisses)
	}
}

// TestDiskStore_Delete 删除条目
func TestDiskStore_Delete(t *testing.T) {
	store, _ := newTestDiskStore(t)
	content := []byte("hello")
	sha := computeSHA256(content)

	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)

	if err := store.Delete("/tmp/test.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, ok := store.Get("/tmp/test.txt"); ok {
		t.Error("expected miss after delete")
	}

	metrics := store.Metrics()
	if metrics.DiskEntryCount != 0 {
		t.Errorf("expected DiskEntryCount=0, got %d", metrics.DiskEntryCount)
	}
}

// TestDiskStore_DeleteMiss 删除不存在的条目（应无错误）
func TestDiskStore_DeleteMiss(t *testing.T) {
	store, _ := newTestDiskStore(t)

	if err := store.Delete("/nonexistent"); err != nil {
		t.Errorf("Delete on missing path should not error: %v", err)
	}
}

// TestDiskStore_OverwritePut 覆盖写入（SHA256 变化）
func TestDiskStore_OverwritePut(t *testing.T) {
	store, _ := newTestDiskStore(t)

	content1 := []byte("hello")
	sha1 := computeSHA256(content1)
	_ = store.Put("/tmp/test.txt", content1, sha1, 5, time.Now(), 1, 1)

	content2 := []byte("hello world!")
	sha2 := computeSHA256(content2)
	_ = store.Put("/tmp/test.txt", content2, sha2, 12, time.Now(), 1, 2)

	result, ok := store.Get("/tmp/test.txt")
	if !ok {
		t.Fatal("expected hit after overwrite")
	}
	if result.Content != string(content2) {
		t.Errorf("content mismatch after overwrite: got %q, want %q", result.Content, string(content2))
	}
	if result.Entry.SHA256 != sha2 {
		t.Errorf("sha256 mismatch after overwrite: got %s, want %s", result.Entry.SHA256, sha2)
	}

	metrics := store.Metrics()
	if metrics.DiskEntryCount != 1 {
		t.Errorf("expected DiskEntryCount=1 after overwrite, got %d", metrics.DiskEntryCount)
	}
}

// TestDiskStore_EmptyContent 空内容写入
func TestDiskStore_EmptyContent(t *testing.T) {
	store, _ := newTestDiskStore(t)
	content := []byte("")
	sha := computeSHA256(content)

	if err := store.Put("/tmp/empty.txt", content, sha, 0, time.Now(), 0, 0); err != nil {
		t.Fatalf("Put empty content: %v", err)
	}

	result, ok := store.Get("/tmp/empty.txt")
	if !ok {
		t.Fatal("expected hit for empty content")
	}
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
}

// TestDiskStore_LargeContent 大内容写入
func TestDiskStore_LargeContent(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.MaxDiskBytes = 10 * 1024 * 1024 // 10MB
	config.MaxDiskEntries = 100
	store, err := NewDiskStore(config)
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}

	// 1MB 内容
	content := bytes.Repeat([]byte("a"), 1024*1024)
	sha := computeSHA256(content)

	if err := store.Put("/tmp/large.txt", content, sha, int64(len(content)), time.Now(), 1024, 262144); err != nil {
		t.Fatalf("Put large content: %v", err)
	}

	result, ok := store.Get("/tmp/large.txt")
	if !ok {
		t.Fatal("expected hit for large content")
	}
	if len(result.Content) != len(content) {
		t.Errorf("content size mismatch: got %d, want %d", len(result.Content), len(content))
	}
}

// TestDiskStore_PutUpdatesIndex Put 后索引元数据正确
func TestDiskStore_PutUpdatesIndex(t *testing.T) {
	store, _ := newTestDiskStore(t)
	content := []byte("test content")
	sha := computeSHA256(content)
	mtime := time.Now().Truncate(time.Second)

	_ = store.Put("/tmp/test.txt", content, sha, int64(len(content)), mtime, 3, 10)

	result, ok := store.Get("/tmp/test.txt")
	if !ok {
		t.Fatal("expected hit")
	}
	if result.Entry.Path != "/tmp/test.txt" {
		t.Errorf("Path mismatch: got %s", result.Entry.Path)
	}
	if result.Entry.Size != int64(len(content)) {
		t.Errorf("Size mismatch: got %d, want %d", result.Entry.Size, len(content))
	}
	if !result.Entry.ModTime.Equal(mtime) {
		t.Errorf("ModTime mismatch: got %v, want %v", result.Entry.ModTime, mtime)
	}
	if result.Entry.LineCount != 3 {
		t.Errorf("LineCount mismatch: got %d, want 3", result.Entry.LineCount)
	}
	if result.Entry.TokenEst != 10 {
		t.Errorf("TokenEst mismatch: got %d, want 10", result.Entry.TokenEst)
	}
}

// ============================================================
// §2 LRU 淘汰（5 个用例）
// ============================================================

// TestDiskStore_EvictByEntries 条目数上限淘汰
func TestDiskStore_EvictByEntries(t *testing.T) {
	store, _ := newTestDiskStore(t)
	// MaxDiskEntries = 5，写入 8 个条目触发淘汰
	for i := 0; i < 8; i++ {
		content := []byte(fmt.Sprintf("content %d", i))
		sha := computeSHA256(content)
		path := fmt.Sprintf("/tmp/file%d.txt", i)
		_ = store.Put(path, content, sha, int64(len(content)), time.Now(), 1, 2)
	}

	metrics := store.Metrics()
	if metrics.DiskEvictions == 0 {
		t.Error("expected evictions > 0")
	}
	if metrics.DiskEntryCount > 5 {
		t.Errorf("DiskEntryCount %d > MaxDiskEntries 5", metrics.DiskEntryCount)
	}
}

// TestDiskStore_EvictByBytes 字节数上限淘汰
func TestDiskStore_EvictByBytes(t *testing.T) {
	store, _ := newTestDiskStore(t)
	// MaxDiskBytes = 1024，每个条目 200 字节，6 个条目即超限
	for i := 0; i < 6; i++ {
		content := bytes.Repeat([]byte{byte(i)}, 200)
		sha := computeSHA256(content)
		path := fmt.Sprintf("/tmp/file%d.txt", i)
		_ = store.Put(path, content, sha, 200, time.Now(), 1, 2)
	}

	metrics := store.Metrics()
	if metrics.DiskEvictions == 0 {
		t.Error("expected evictions > 0")
	}
	if metrics.DiskBytes > 1024 {
		t.Errorf("DiskBytes %d > MaxDiskBytes 1024", metrics.DiskBytes)
	}
}

// TestDiskStore_EvictPriority_HitCount 低 HitCount 优先淘汰
func TestDiskStore_EvictPriority_HitCount(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.MaxDiskEntries = 3
	config.MaxDiskBytes = 10 * 1024 * 1024
	store, _ := NewDiskStore(config)

	// 写入 3 个条目
	for i := 0; i < 3; i++ {
		content := []byte(fmt.Sprintf("content %d", i))
		sha := computeSHA256(content)
		_ = store.Put(fmt.Sprintf("/tmp/file%d.txt", i), content, sha, int64(len(content)), time.Now(), 1, 2)
	}

	// 多次 Get file0 + file1 提升其 HitCount
	for i := 0; i < 5; i++ {
		_, _ = store.Get("/tmp/file0.txt")
		_, _ = store.Get("/tmp/file1.txt")
	}
	// file2 保持 HitCount=0（冷数据）

	// 写入第 4 个条目触发淘汰，应淘汰 file2（HitCount=0）
	content4 := []byte("content 4")
	sha4 := computeSHA256(content4)
	_ = store.Put("/tmp/file4.txt", content4, sha4, int64(len(content4)), time.Now(), 1, 2)

	// file2 应被淘汰
	if _, ok := store.Get("/tmp/file2.txt"); ok {
		t.Error("expected file2 to be evicted (low HitCount)")
	}
	// file0 应仍存在
	if _, ok := store.Get("/tmp/file0.txt"); !ok {
		t.Error("expected file0 to still exist (high HitCount)")
	}
}

// TestDiskStore_EvictPriority_LastAccess 旧 LastAccess 优先淘汰
func TestDiskStore_EvictPriority_LastAccess(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.MaxDiskEntries = 3
	config.MaxDiskBytes = 10 * 1024 * 1024
	store, _ := NewDiskStore(config)

	// 写入 file0（最早访问）
	content0 := []byte("content0")
	_ = store.Put("/tmp/file0.txt", content0, computeSHA256(content0), 8, time.Now(), 1, 2)
	time.Sleep(10 * time.Millisecond)

	// 写入 file1
	content1 := []byte("content1")
	_ = store.Put("/tmp/file1.txt", content1, computeSHA256(content1), 8, time.Now(), 1, 2)
	time.Sleep(10 * time.Millisecond)

	// 写入 file2（最近访问）
	content2 := []byte("content2")
	_ = store.Put("/tmp/file2.txt", content2, computeSHA256(content2), 8, time.Now(), 1, 2)

	// 同 HitCount=0，应淘汰 file0（最早 LastAccess）
	content3 := []byte("content3")
	_ = store.Put("/tmp/file3.txt", content3, computeSHA256(content3), 8, time.Now(), 1, 2)

	if _, ok := store.Get("/tmp/file0.txt"); ok {
		t.Error("expected file0 to be evicted (oldest LastAccess)")
	}
	if _, ok := store.Get("/tmp/file2.txt"); !ok {
		t.Error("expected file2 to still exist (newest LastAccess)")
	}
}

// TestDiskStore_EvictTriggersOnPut Put 时自动触发淘汰
func TestDiskStore_EvictTriggersOnPut(t *testing.T) {
	store, _ := newTestDiskStore(t)
	// MaxDiskEntries = 5
	for i := 0; i < 5; i++ {
		content := []byte(fmt.Sprintf("c%d", i))
		_ = store.Put(fmt.Sprintf("/tmp/f%d", i), content, computeSHA256(content), 2, time.Now(), 1, 1)
	}

	metricsBefore := store.Metrics()
	if metricsBefore.DiskEvictions != 0 {
		t.Errorf("expected 0 evictions before limit, got %d", metricsBefore.DiskEvictions)
	}

	// 第 6 个条目触发淘汰
	content := []byte("c5")
	_ = store.Put("/tmp/f5", content, computeSHA256(content), 2, time.Now(), 1, 1)

	metricsAfter := store.Metrics()
	if metricsAfter.DiskEvictions == 0 {
		t.Error("expected evictions > 0 after exceeding limit")
	}
}

// ============================================================
// §3 TTL 过期（3 个用例）
// ============================================================

// TestDiskStore_TTLExpiry TTL 过期
func TestDiskStore_TTLExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.TTL = time.Hour
	store, _ := NewDiskStore(config)

	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)

	// 立即 Get 应命中
	if _, ok := store.Get("/tmp/test.txt"); !ok {
		t.Error("expected hit before TTL expiry")
	}

	// 直接推进条目时间，避免依赖调度时延造成全仓并发测试假失败。
	store.mu.Lock()
	store.index["/tmp/test.txt"].CreatedAt = time.Now().Add(-2 * config.TTL)
	store.mu.Unlock()

	// Get 应 miss
	if _, ok := store.Get("/tmp/test.txt"); ok {
		t.Error("expected miss after TTL expiry")
	}
}

// TestDiskStore_TTLZeroNeverExpires TTL=0 永不过期
func TestDiskStore_TTLZeroNeverExpires(t *testing.T) {
	store, _ := newTestDiskStore(t)
	// TTL=0（默认配置）

	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)

	// 等待一段时间
	time.Sleep(50 * time.Millisecond)

	// 仍应命中
	if _, ok := store.Get("/tmp/test.txt"); !ok {
		t.Error("expected hit with TTL=0 (never expires)")
	}
}

// TestDiskStore_TTLExpiredOnRestore 恢复时清理过期条目
func TestDiskStore_TTLExpiredOnRestore(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.TTL = 50 * time.Millisecond

	// 第一个 store 写入条目
	store1, _ := NewDiskStore(config)
	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store1.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)
	_ = store1.PersistIndex()

	// 等待 TTL 过期
	time.Sleep(80 * time.Millisecond)

	// 新 store 恢复索引，应清理过期条目
	store2, _ := NewDiskStore(config)

	metrics := store2.Metrics()
	if metrics.DiskEntryCount != 0 {
		t.Errorf("expected 0 entries after TTL expiry on restore, got %d", metrics.DiskEntryCount)
	}
}

// ============================================================
// §4 跨平台路径（4 个用例）
// ============================================================

// TestDiskStore_WindowsPathSeparator 跨平台路径分隔符
func TestDiskStore_WindowsPathSeparator(t *testing.T) {
	store, _ := newTestDiskStore(t)

	// 使用 filepath.Join 处理路径（跨平台）
	path := filepath.Join("/tmp", "sub", "file.txt")
	content := []byte("test")
	sha := computeSHA256(content)
	_ = store.Put(path, content, sha, 4, time.Now(), 1, 1)

	// 用相同路径查询应命中
	result, ok := store.Get(path)
	if !ok {
		t.Fatal("expected hit")
	}
	if result.Content != "test" {
		t.Errorf("content mismatch: %q", result.Content)
	}
}

// TestDiskStore_SymlinkPath 符号链接路径处理
func TestDiskStore_SymlinkPath(t *testing.T) {
	store, _ := newTestDiskStore(t)

	// 直接使用路径（不依赖符号链接解析，DiskStore 不做 EvalSymlinks）
	path := "/tmp/real.txt"
	content := []byte("real content")
	sha := computeSHA256(content)
	_ = store.Put(path, content, sha, int64(len(content)), time.Now(), 1, 2)

	result, ok := store.Get(path)
	if !ok {
		t.Fatal("expected hit")
	}
	if result.Content != "real content" {
		t.Errorf("content mismatch: %q", result.Content)
	}
}

// TestDiskStore_RelativePathResolve 相对路径解析
func TestDiskStore_RelativePathResolve(t *testing.T) {
	store, _ := newTestDiskStore(t)

	// DiskStore 不做 Clean，调用方负责（与 file_cache.go 一致）
	// 此用例验证：相同字符串作为 key 能命中
	path := "relative/path/file.txt"
	content := []byte("relative")
	sha := computeSHA256(content)
	_ = store.Put(path, content, sha, int64(len(content)), time.Now(), 1, 1)

	result, ok := store.Get(path)
	if !ok {
		t.Fatal("expected hit for relative path")
	}
	if result.Content != "relative" {
		t.Errorf("content mismatch: %q", result.Content)
	}
}

// TestDiskStore_SHA256BucketPath SHA256 前两字符分桶路径
func TestDiskStore_SHA256BucketPath(t *testing.T) {
	store, tmpDir := newTestDiskStore(t)

	content := []byte("bucket test")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/bucket.txt", content, sha, int64(len(content)), time.Now(), 1, 1)

	// 验证磁盘文件路径为 <prefix-2>/<sha256>
	expectedRelPath := filepath.Join(sha[:2], sha)
	absDiskPath := filepath.Join(tmpDir, expectedRelPath)
	if _, err := os.Stat(absDiskPath); err != nil {
		t.Errorf("expected disk file at %s, got error: %v", absDiskPath, err)
	}

	// 验证 diskPathForSHA256 函数
	if got := diskPathForSHA256(sha); got != filepath.Join(sha[:2], sha) {
		t.Errorf("diskPathForSHA256: got %q, want %q", got, filepath.Join(sha[:2], sha))
	}

	// 验证短 SHA256 走 others 桶
	if got := diskPathForSHA256("a"); got != filepath.Join("others", "a") {
		t.Errorf("diskPathForSHA256 short: got %q, want %q", got, filepath.Join("others", "a"))
	}
}

// ============================================================
// §5 持久化恢复（4 个用例）
// ============================================================

// TestDiskStore_PersistAndRestore 持久化 + 恢复
func TestDiskStore_PersistAndRestore(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	store1, _ := NewDiskStore(config)

	// 写入多个条目
	for i := 0; i < 3; i++ {
		content := []byte(fmt.Sprintf("content %d", i))
		sha := computeSHA256(content)
		_ = store1.Put(fmt.Sprintf("/tmp/file%d.txt", i), content, sha, int64(len(content)), time.Now(), 1, 2)
	}

	// 显式持久化索引
	if err := store1.PersistIndex(); err != nil {
		t.Fatalf("PersistIndex: %v", err)
	}

	// 验证 index.json 存在
	indexPath := filepath.Join(tmpDir, "index.json")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("index.json not created: %v", err)
	}

	// 新 store 恢复
	store2, _ := NewDiskStore(config)

	metrics := store2.Metrics()
	if metrics.DiskEntryCount != 3 {
		t.Errorf("expected 3 entries after restore, got %d", metrics.DiskEntryCount)
	}

	// 验证条目内容
	result, ok := store2.Get("/tmp/file0.txt")
	if !ok {
		t.Error("expected hit after restore")
	}
	if result.Content != "content 0" {
		t.Errorf("content mismatch after restore: %q", result.Content)
	}
}

// TestDiskStore_RestoreCorruptJSON 损坏 JSON 静默重建空索引
func TestDiskStore_RestoreCorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir

	// 写入损坏的 index.json
	indexPath := filepath.Join(tmpDir, "index.json")
	if err := os.WriteFile(indexPath, []byte("{corrupt json"), 0600); err != nil {
		t.Fatalf("write corrupt index: %v", err)
	}

	// NewDiskStore 应静默处理，重建空索引
	store, err := NewDiskStore(config)
	if err != nil {
		t.Fatalf("NewDiskStore with corrupt JSON: %v", err)
	}

	metrics := store.Metrics()
	if metrics.DiskEntryCount != 0 {
		t.Errorf("expected 0 entries on corrupt JSON, got %d", metrics.DiskEntryCount)
	}
}

// TestDiskStore_RestoreMissingDiskFile 磁盘文件丢失时跳过索引条目
func TestDiskStore_RestoreMissingDiskFile(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	store1, _ := NewDiskStore(config)

	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store1.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)
	_ = store1.PersistIndex()

	// 手动删除磁盘文件（保留 index.json）
	entry := store1.index["/tmp/test.txt"]
	absDiskPath := store1.resolveDiskPath(entry.DiskPath)
	if err := os.Remove(absDiskPath); err != nil {
		t.Fatalf("remove disk file: %v", err)
	}

	// 新 store 恢复，应跳过磁盘文件丢失的条目
	store2, _ := NewDiskStore(config)
	metrics := store2.Metrics()
	if metrics.DiskEntryCount != 0 {
		t.Errorf("expected 0 entries (disk file missing), got %d", metrics.DiskEntryCount)
	}
}

// TestDiskStore_PersistAfterEvict 淘汰后持久化索引一致
func TestDiskStore_PersistAfterEvict(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.MaxDiskEntries = 3
	store1, _ := NewDiskStore(config)

	// 写入 5 个条目，触发淘汰（保留 3 个）
	for i := 0; i < 5; i++ {
		content := []byte(fmt.Sprintf("c%d", i))
		_ = store1.Put(fmt.Sprintf("/tmp/f%d", i), content, computeSHA256(content), 2, time.Now(), 1, 1)
	}
	_ = store1.PersistIndex()

	// 新 store 恢复，应只有 3 个条目
	store2, _ := NewDiskStore(config)
	metrics := store2.Metrics()
	if metrics.DiskEntryCount != 3 {
		t.Errorf("expected 3 entries after restore, got %d", metrics.DiskEntryCount)
	}
}

// ============================================================
// §6 孤儿 GC（2 个用例）
// ============================================================

// TestDiskStore_CleanupOrphans 清理孤儿文件
func TestDiskStore_CleanupOrphans(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	store, _ := NewDiskStore(config)

	// 写入 2 个条目
	for i := 0; i < 2; i++ {
		content := []byte(fmt.Sprintf("content %d", i))
		sha := computeSHA256(content)
		_ = store.Put(fmt.Sprintf("/tmp/file%d.txt", i), content, sha, int64(len(content)), time.Now(), 1, 2)
	}

	// 手动创建孤儿文件（不在索引中）
	orphanDir := filepath.Join(tmpDir, "ab")
	_ = os.MkdirAll(orphanDir, 0700)
	orphanFile := filepath.Join(orphanDir, "orphanhash")
	_ = os.WriteFile(orphanFile, []byte("orphan"), 0600)

	// 调用 cleanupOrphans
	removed := store.cleanupOrphans()
	if removed < 1 {
		t.Errorf("expected at least 1 orphan removed, got %d", removed)
	}

	// 验证孤儿文件已被删除
	if _, err := os.Stat(orphanFile); err == nil {
		t.Error("expected orphan file to be removed")
	}
}

// TestDiskStore_CleanupOrphansNoIndex 无索引时清理所有文件
func TestDiskStore_CleanupOrphansNoIndex(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir

	// 先创建孤儿文件
	orphanDir := filepath.Join(tmpDir, "cd")
	_ = os.MkdirAll(orphanDir, 0700)
	orphanFile := filepath.Join(orphanDir, "orphanhash")
	_ = os.WriteFile(orphanFile, []byte("orphan"), 0600)

	// 新 store 启动时自动 cleanupOrphans
	store, _ := NewDiskStore(config)

	// 验证孤儿文件已被删除
	if _, err := os.Stat(orphanFile); err == nil {
		t.Error("expected orphan file to be removed on startup")
	}

	metrics := store.Metrics()
	if metrics.DiskEntryCount != 0 {
		t.Errorf("expected 0 entries, got %d", metrics.DiskEntryCount)
	}
}

// ============================================================
// §7 SHA256 校验（2 个用例，吸纳优化 4、6）
// ============================================================

// TestDiskStore_VerifyOnReadPass SHA256 校验通过
func TestDiskStore_VerifyOnReadPass(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.VerifyOnRead = true
	store, _ := NewDiskStore(config)

	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)

	// 校验通过，应命中
	result, ok := store.Get("/tmp/test.txt")
	if !ok {
		t.Fatal("expected hit with VerifyOnRead pass")
	}
	if result.Content != "hello" {
		t.Errorf("content mismatch: %q", result.Content)
	}

	metrics := store.Metrics()
	if metrics.CorruptionCount != 0 {
		t.Errorf("expected CorruptionCount=0, got %d", metrics.CorruptionCount)
	}
}

// TestDiskStore_VerifyOnReadFail SHA256 校验失败处理（吸纳优化 4）
func TestDiskStore_VerifyOnReadFail(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.VerifyOnRead = true
	store, _ := NewDiskStore(config)

	// Put 正常内容
	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)

	// 篡改磁盘文件
	entry, _ := store.index["/tmp/test.txt"]
	absDiskPath := store.resolveDiskPath(entry.DiskPath)
	_ = os.WriteFile(absDiskPath, []byte("corrupted"), 0600)

	// Get 应 miss + CorruptionCount++
	if _, ok := store.Get("/tmp/test.txt"); ok {
		t.Error("expected miss on corruption")
	}
	if store.Metrics().CorruptionCount != 1 {
		t.Errorf("expected CorruptionCount=1, got %d", store.Metrics().CorruptionCount)
	}

	// 索引应已删除
	if _, ok := store.index["/tmp/test.txt"]; ok {
		t.Error("expected index entry to be removed after corruption")
	}
}

// ============================================================
// §8 DeleteByPrefix（2 个用例）
// ============================================================

// TestDiskStore_DeleteByPrefix 按前缀批量删除
func TestDiskStore_DeleteByPrefix(t *testing.T) {
	store, _ := newTestDiskStore(t)

	// 写入多个条目
	paths := []string{
		"/tmp/sub1/a.txt",
		"/tmp/sub1/b.txt",
		"/tmp/sub2/c.txt",
		"/tmp/other.txt",
	}
	for _, p := range paths {
		content := []byte(p)
		_ = store.Put(p, content, computeSHA256(content), int64(len(content)), time.Now(), 1, 2)
	}

	// 删除 /tmp/sub1/ 前缀
	removed := store.DeleteByPrefix("/tmp/sub1/")
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	// 验证 sub1 下条目已删除
	if _, ok := store.Get("/tmp/sub1/a.txt"); ok {
		t.Error("expected /tmp/sub1/a.txt to be deleted")
	}
	// 验证 sub2 + other 仍存在
	if _, ok := store.Get("/tmp/sub2/c.txt"); !ok {
		t.Error("expected /tmp/sub2/c.txt to still exist")
	}
	if _, ok := store.Get("/tmp/other.txt"); !ok {
		t.Error("expected /tmp/other.txt to still exist")
	}
}

// TestDiskStore_DeleteByPrefixNoMatch 前缀无匹配
func TestDiskStore_DeleteByPrefixNoMatch(t *testing.T) {
	store, _ := newTestDiskStore(t)

	content := []byte("hello")
	_ = store.Put("/tmp/test.txt", content, computeSHA256(content), 5, time.Now(), 1, 1)

	removed := store.DeleteByPrefix("/nonexistent/")
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}

	if _, ok := store.Get("/tmp/test.txt"); !ok {
		t.Error("expected original entry to still exist")
	}
}

// ============================================================
// §9 额外测试：并发安全 + 跨平台 startsWithPath
// ============================================================

// TestDiskStore_ConcurrentAccess 并发读写安全
func TestDiskStore_ConcurrentAccess(t *testing.T) {
	store, _ := newTestDiskStore(t)
	// 放大上限避免淘汰干扰并发测试
	store.config.MaxDiskEntries = 100
	store.config.MaxDiskBytes = 10 * 1024 * 1024

	var wg sync.WaitGroup
	// 5 个 goroutine 并发 Put
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				path := fmt.Sprintf("/tmp/g%d-f%d.txt", id, j)
				content := []byte(fmt.Sprintf("content %d-%d", id, j))
				_ = store.Put(path, content, computeSHA256(content), int64(len(content)), time.Now(), 1, 2)
			}
		}(i)
	}
	// 5 个 goroutine 并发 Get
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				path := fmt.Sprintf("/tmp/g%d-f%d.txt", id, j)
				_, _ = store.Get(path)
			}
		}(i)
	}
	wg.Wait()
	// 不发生 panic / race 即通过（race 由 -race 检测器验证）
}

// TestDiskStore_StartsWithPath 跨平台路径前缀匹配
func TestDiskStore_StartsWithPath(t *testing.T) {
	cases := []struct {
		path   string
		prefix string
		want   bool
	}{
		{"/tmp/sub/file.txt", "/tmp/sub", true},
		{"/tmp/sub/file.txt", "/tmp/sub/", true},
		{"/tmp/sub/file.txt", "/tmp", true},
		{"/tmp/sub/file.txt", "/tmp/", true},
		{"/tmp/sub/file.txt", "/tmp/other", false},
		{"/tmp/sub/file.txt", "/tmp/sub2", false}, // 前缀字符匹配但非完整段
		{"/tmp/sub/file.txt", "", true},           // 空前缀
		{"short", "/very/long/prefix", false},     // path 短于 prefix
	}
	for i, c := range cases {
		got := startsWithPath(c.path, c.prefix)
		if got != c.want {
			t.Errorf("case %d: startsWithPath(%q, %q) = %v, want %v", i, c.path, c.prefix, got, c.want)
		}
	}
}

// TestDiskStore_Metrics 指标准确性
func TestDiskStore_Metrics(t *testing.T) {
	store, _ := newTestDiskStore(t)

	// 写入 3 个条目
	for i := 0; i < 3; i++ {
		content := []byte(fmt.Sprintf("content %d", i))
		_ = store.Put(fmt.Sprintf("/tmp/file%d.txt", i), content, computeSHA256(content), int64(len(content)), time.Now(), 1, 2)
	}

	metrics := store.Metrics()
	if metrics.DiskWrites != 3 {
		t.Errorf("expected DiskWrites=3, got %d", metrics.DiskWrites)
	}
	if metrics.DiskEntryCount != 3 {
		t.Errorf("expected DiskEntryCount=3, got %d", metrics.DiskEntryCount)
	}

	// Get 2 次（命中）+ 1 次（未命中）
	_, _ = store.Get("/tmp/file0.txt")
	_, _ = store.Get("/tmp/file1.txt")
	_, _ = store.Get("/tmp/nonexistent")

	metrics = store.Metrics()
	if metrics.DiskHits != 2 {
		t.Errorf("expected DiskHits=2, got %d", metrics.DiskHits)
	}
	if metrics.DiskMisses != 1 {
		t.Errorf("expected DiskMisses=1, got %d", metrics.DiskMisses)
	}
}

// TestDiskStore_Close 关闭存储
func TestDiskStore_Close(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.SyncMode = SyncOnEvict // Close 时持久化索引
	store, _ := NewDiskStore(config)

	content := []byte("hello")
	_ = store.Put("/tmp/test.txt", content, computeSHA256(content), 5, time.Now(), 1, 1)

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 验证 index.json 已持久化（SyncOnEvict 模式下 Close 触发 PersistIndex）
	indexPath := filepath.Join(tmpDir, "index.json")
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("expected index.json after Close: %v", err)
	}
}

// TestDiskStore_PersistedDiskIndexJSONFormat 索引 JSON 格式校验
func TestDiskStore_PersistedDiskIndexJSONFormat(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	store, _ := NewDiskStore(config)

	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 2)
	_ = store.PersistIndex()

	// 读取 index.json 验证 JSON 格式
	indexPath := filepath.Join(tmpDir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}

	var persisted persistedDiskIndex
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal index.json: %v", err)
	}
	if persisted.Version != 1 {
		t.Errorf("expected Version=1, got %d", persisted.Version)
	}
	if len(persisted.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(persisted.Entries))
	}
	entry, ok := persisted.Entries["/tmp/test.txt"]
	if !ok {
		t.Fatal("expected entry for /tmp/test.txt")
	}
	if entry.SHA256 != sha {
		t.Errorf("SHA256 mismatch: got %s, want %s", entry.SHA256, sha)
	}
	if entry.DiskPath != filepath.Join(sha[:2], sha) {
		t.Errorf("DiskPath mismatch: got %s", entry.DiskPath)
	}
}

// TestDiskStore_DefaultDiskCacheConfig 默认配置校验
func TestDiskStore_DefaultDiskCacheConfig(t *testing.T) {
	cfg := DefaultDiskCacheConfig()
	if cfg.Enabled != false {
		t.Errorf("expected Enabled=false by default, got %v", cfg.Enabled)
	}
	if cfg.MaxDiskBytes != 256*1024*1024 {
		t.Errorf("expected MaxDiskBytes=256MB, got %d", cfg.MaxDiskBytes)
	}
	if cfg.MaxDiskEntries != 1000 {
		t.Errorf("expected MaxDiskEntries=1000, got %d", cfg.MaxDiskEntries)
	}
	if cfg.TTL != 7*24*time.Hour {
		t.Errorf("expected TTL=7d, got %v", cfg.TTL)
	}
	if cfg.SyncMode != SyncOnWrite {
		t.Errorf("expected SyncMode=SyncOnWrite, got %v", cfg.SyncMode)
	}
	if cfg.VerifyOnRead != false {
		t.Errorf("expected VerifyOnRead=false, got %v", cfg.VerifyOnRead)
	}
}

// TestDiskStore_RepeatedGetRepeatedHit 反复 Get 应累加 HitCount
func TestDiskStore_RepeatedGetRepeatedHit(t *testing.T) {
	store, _ := newTestDiskStore(t)

	content := []byte("hello")
	sha := computeSHA256(content)
	_ = store.Put("/tmp/test.txt", content, sha, 5, time.Now(), 1, 1)

	// 连续 Get 5 次
	for i := 0; i < 5; i++ {
		result, ok := store.Get("/tmp/test.txt")
		if !ok {
			t.Fatalf("expected hit on iteration %d", i)
		}
		if result.Entry.HitCount != i+1 {
			t.Errorf("iteration %d: expected HitCount=%d, got %d", i, i+1, result.Entry.HitCount)
		}
	}

	metrics := store.Metrics()
	if metrics.DiskHits != 5 {
		t.Errorf("expected DiskHits=5, got %d", metrics.DiskHits)
	}
}

// TestDiskStore_PutSameSHA256Overwrite 相同 SHA256 覆盖（path 变化）
func TestDiskStore_PutSameSHA256Overwrite(t *testing.T) {
	store, _ := newTestDiskStore(t)

	content := []byte("same content")
	sha := computeSHA256(content)

	// 同 SHA256 写入到不同 path
	_ = store.Put("/tmp/path1.txt", content, sha, int64(len(content)), time.Now(), 1, 2)
	_ = store.Put("/tmp/path2.txt", content, sha, int64(len(content)), time.Now(), 1, 2)

	// 两个 path 都应命中（共享同一磁盘文件）
	if _, ok := store.Get("/tmp/path1.txt"); !ok {
		t.Error("expected path1 hit")
	}
	if _, ok := store.Get("/tmp/path2.txt"); !ok {
		t.Error("expected path2 hit")
	}

	metrics := store.Metrics()
	if metrics.DiskEntryCount != 2 {
		t.Errorf("expected DiskEntryCount=2, got %d", metrics.DiskEntryCount)
	}
}

// TestDiskStore_DiskPathForSHA256 分桶路径函数
func TestDiskStore_DiskPathForSHA256(t *testing.T) {
	cases := []struct {
		sha  string
		want string
	}{
		{"abcdef", filepath.Join("ab", "abcdef")},
		{"0123456789", filepath.Join("01", "0123456789")},
		{"a", filepath.Join("others", "a")}, // 短 SHA256
		{"", filepath.Join("others", "")},   // 空 SHA256
	}
	for i, c := range cases {
		got := diskPathForSHA256(c.sha)
		// 跨平台：使用 ToSlash 比较避免 Windows \ 与 Linux / 差异
		if filepath.ToSlash(got) != filepath.ToSlash(c.want) {
			t.Errorf("case %d: diskPathForSHA256(%q) = %q, want %q", i, c.sha, got, c.want)
		}
	}
}

// TestDiskStore_EntryPathsSortedByLastAccess entryPaths 按 LastAccess 升序
func TestDiskStore_EntryPathsSortedByLastAccess(t *testing.T) {
	store, _ := newTestDiskStore(t)
	store.config.MaxDiskEntries = 100
	store.config.MaxDiskBytes = 10 * 1024 * 1024

	// 写入 3 个条目，间隔时间
	_ = store.Put("/tmp/first", []byte("1"), computeSHA256([]byte("1")), 1, time.Now(), 1, 1)
	time.Sleep(10 * time.Millisecond)
	_ = store.Put("/tmp/second", []byte("2"), computeSHA256([]byte("2")), 1, time.Now(), 1, 1)
	time.Sleep(10 * time.Millisecond)
	_ = store.Put("/tmp/third", []byte("3"), computeSHA256([]byte("3")), 1, time.Now(), 1, 1)

	paths := store.entryPaths()
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(paths))
	}
	// 升序：first → second → third
	if paths[0] != "/tmp/first" {
		t.Errorf("expected paths[0]=/tmp/first, got %s", paths[0])
	}
	if paths[2] != "/tmp/third" {
		t.Errorf("expected paths[2]=/tmp/third, got %s", paths[2])
	}
}

// TestDiskStore_LargeNumberOfEntries 大量条目压力测试
func TestDiskStore_LargeNumberOfEntries(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = tmpDir
	config.MaxDiskEntries = 50
	config.MaxDiskBytes = 10 * 1024 * 1024
	store, _ := NewDiskStore(config)

	// 写入 100 个条目，触发多轮淘汰
	for i := 0; i < 100; i++ {
		content := []byte(fmt.Sprintf("content %d", i))
		_ = store.Put(fmt.Sprintf("/tmp/file%d.txt", i), content, computeSHA256(content), int64(len(content)), time.Now(), 1, 2)
	}

	metrics := store.Metrics()
	if metrics.DiskEntryCount > 50 {
		t.Errorf("DiskEntryCount %d > MaxDiskEntries 50", metrics.DiskEntryCount)
	}
	if metrics.DiskEvictions < 50 {
		t.Errorf("expected DiskEvictions >= 50, got %d", metrics.DiskEvictions)
	}
}

// TestDiskStore_DeleteByPrefixWindowsSeparator 跨平台前缀删除（Windows 风格）
func TestDiskStore_DeleteByPrefixWindowsSeparator(t *testing.T) {
	store, _ := newTestDiskStore(t)

	// 使用 filepath.Join 构造跨平台路径
	subDir := filepath.Join("/tmp", "sub", "dir")
	path1 := filepath.Join(subDir, "a.txt")
	path2 := filepath.Join(subDir, "b.txt")
	path3 := filepath.Join("/tmp", "other.txt")

	_ = store.Put(path1, []byte("a"), computeSHA256([]byte("a")), 1, time.Now(), 1, 1)
	_ = store.Put(path2, []byte("b"), computeSHA256([]byte("b")), 1, time.Now(), 1, 1)
	_ = store.Put(path3, []byte("c"), computeSHA256([]byte("c")), 1, time.Now(), 1, 1)

	removed := store.DeleteByPrefix(subDir)
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	if _, ok := store.Get(path1); ok {
		t.Error("expected path1 to be deleted")
	}
	if _, ok := store.Get(path3); !ok {
		t.Error("expected path3 to still exist")
	}
}

// TestDiskStore_NewDiskStoreFailure NewDiskStore 失败场景。
// 使用一个已存在的普通文件作为父路径，MkdirAll 会因父路径是文件而非目录失败。
func TestDiskStore_NewDiskStoreFailure(t *testing.T) {
	tmpDir := t.TempDir()
	// 创建一个普通文件
	filePath := filepath.Join(tmpDir, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0600); err != nil {
		t.Fatalf("create file: %v", err)
	}
	// 尝试在文件下创建子目录（必然失败：父路径不是目录）
	config := DefaultDiskCacheConfig()
	config.Enabled = true
	config.RootDir = filepath.Join(filePath, "subdir")

	// MkdirAll 应失败
	store, err := NewDiskStore(config)
	if err == nil {
		t.Error("expected error for invalid RootDir (parent is a file)")
	}
	if store != nil {
		t.Error("expected nil store on error")
	}
}

// TestDiskStore_StringRepresentation 字符串表示（防止 panic）
func TestDiskStore_StringRepresentation(t *testing.T) {
	store, _ := newTestDiskStore(t)
	_ = store.Put("/tmp/test.txt", []byte("hello"), computeSHA256([]byte("hello")), 5, time.Now(), 1, 1)

	// 简单验证 Metrics() 返回的字符串包含关键字段
	metrics := store.Metrics()
	str := fmt.Sprintf("%+v", metrics)
	if !strings.Contains(str, "DiskHits") {
		t.Errorf("metrics string missing DiskHits: %s", str)
	}
}
