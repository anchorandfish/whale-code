// Package github — / M19-05 review 结果文件哈希缓存测试
//
// 测试覆盖：
// - ComputeFileHash：SHA-256 哈希计算
// - NewHashCache：构造函数 + 默认值
// - SetMaxEntries：设置最大条目数
// - Get/Put：缓存查找 + 存入 + 命中/未命中
// - Save/Load：持久化 + 加载 + 原子写入
// - Clear：清空内存 + 删除磁盘文件
// - Size：条目数统计
// - evictOldestLocked：FIFO 淘汰
// - 并发安全：多 goroutine 并发 Get/Put
//
// 对标 webpack cache 持久化模式
// [webpack.js.org/configuration/cache](https://webpack.js.org/configuration/cache/) @2026-07-21
package github

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// === ComputeFileHash 测试 ===

// TestComputeFileHash 测试 SHA-256 哈希计算。
func TestComputeFileHash(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string // 期望的 SHA-256 十六进制字符串
	}{
		{
			name:    "empty content",
			content: []byte(""),
			want:    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:    "hello world",
			content: []byte("hello world"),
			want:    "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
		{
			name:    "single char",
			content: []byte("a"),
			want:    "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeFileHash(tt.content)
			if got != tt.want {
				t.Errorf("ComputeFileHash() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestComputeFileHash_Deterministic 测试相同内容哈希一致。
func TestComputeFileHash_Deterministic(t *testing.T) {
	content := []byte("same content")
	hash1 := ComputeFileHash(content)
	hash2 := ComputeFileHash(content)
	if hash1 != hash2 {
		t.Errorf("same content should produce same hash: %q vs %q", hash1, hash2)
	}
}

// TestComputeFileHash_DifferentContent 测试不同内容哈希不一致。
func TestComputeFileHash_DifferentContent(t *testing.T) {
	hash1 := ComputeFileHash([]byte("content1"))
	hash2 := ComputeFileHash([]byte("content2"))
	if hash1 == hash2 {
		t.Error("different content should produce different hashes")
	}
}

// === NewHashCache 测试 ===

// TestNewHashCache 测试构造函数。
func TestNewHashCache(t *testing.T) {
	cache := NewHashCache("/tmp/test_cache.json")
	if cache.path != "/tmp/test_cache.json" {
		t.Errorf("path = %q, want '/tmp/test_cache.json'", cache.path)
	}
	if cache.entries == nil {
		t.Error("entries map is nil")
	}
	if len(cache.entries) != 0 {
		t.Errorf("entries size = %d, want 0", len(cache.entries))
	}
	if cache.maxEntries != defaultMaxHashCacheEntries {
		t.Errorf("maxEntries = %d, want %d", cache.maxEntries, defaultMaxHashCacheEntries)
	}
}

// TestNewHashCache_EmptyPath 测试空路径构造。
func TestNewHashCache_EmptyPath(t *testing.T) {
	cache := NewHashCache("")
	if cache.path != "" {
		t.Errorf("path = %q, want empty", cache.path)
	}
}

// === SetMaxEntries 测试 ===

// TestSetMaxEntries 测试设置最大条目数。
func TestSetMaxEntries(t *testing.T) {
	cache := NewHashCache("/tmp/test.json")

	cache.SetMaxEntries(500)
	if cache.maxEntries != 500 {
		t.Errorf("maxEntries = %d, want 500", cache.maxEntries)
	}

	cache.SetMaxEntries(0) // 0 → 默认值
	if cache.maxEntries != defaultMaxHashCacheEntries {
		t.Errorf("maxEntries = %d, want %d (default)", cache.maxEntries, defaultMaxHashCacheEntries)
	}

	cache.SetMaxEntries(-1) // 负数 → 默认值
	if cache.maxEntries != defaultMaxHashCacheEntries {
		t.Errorf("maxEntries = %d, want %d (default)", cache.maxEntries, defaultMaxHashCacheEntries)
	}
}

// === Get/Put 测试 ===

// TestHashCache_PutAndGet 测试存入 + 查找。
func TestHashCache_PutAndGet(t *testing.T) {
	cache := NewHashCache("")

	result := ReviewResult{
		Summary:  "test summary",
		Findings: []Finding{{File: "test.go", Line: 10, Severity: "warning"}},
		Metrics:  ReviewMetrics{TotalFindings: 1},
	}

	fileHash := ComputeFileHash([]byte("test content"))
	cache.Put("/path/to/file.go", fileHash, result)

	// 命中查找
	got, ok := cache.Get(fileHash)
	if !ok {
		t.Fatal("Get returned not ok after Put")
	}
	if got.Summary != "test summary" {
		t.Errorf("Summary = %q, want 'test summary'", got.Summary)
	}
	if len(got.Findings) != 1 {
		t.Errorf("Findings count = %d, want 1", len(got.Findings))
	}
	if got.Metrics.TotalFindings != 1 {
		t.Errorf("Metrics.TotalFindings = %d, want 1", got.Metrics.TotalFindings)
	}
}

// TestHashCache_GetMiss 测试未命中查找。
func TestHashCache_GetMiss(t *testing.T) {
	cache := NewHashCache("")

	got, ok := cache.Get("nonexistent-hash")
	if ok {
		t.Error("Get returned ok for nonexistent hash")
	}
	if got != nil {
		t.Error("Get returned non-nil for nonexistent hash")
	}
}

// TestHashCache_GetAfterClear 测试 Clear 后 Get 未命中。
func TestHashCache_GetAfterClear(t *testing.T) {
	cache := NewHashCache("")

	result := ReviewResult{Summary: "test"}
	fileHash := ComputeFileHash([]byte("content"))
	cache.Put("/file.go", fileHash, result)

	// Clear 前命中
	_, ok := cache.Get(fileHash)
	if !ok {
		t.Error("Get should hit before Clear")
	}

	// Clear 后未命中
	cache.Clear()
	_, ok = cache.Get(fileHash)
	if ok {
		t.Error("Get should miss after Clear")
	}
}

// === FIFO 淘汰测试 ===

// TestHashCache_EvictOldest 测试超过 maxEntries 时淘汰最旧条目。
func TestHashCache_EvictOldest(t *testing.T) {
	cache := NewHashCache("")
	cache.SetMaxEntries(3)

	// 存入 4 个条目（超过 maxEntries=3）
	for i := 0; i < 4; i++ {
		content := []byte("content-" + string(rune('a'+i)))
		hash := ComputeFileHash(content)
		cache.Put("/file"+string(rune('a'+i))+".go", hash, ReviewResult{
			Summary: "review-" + string(rune('a'+i)),
		})
		// 确保时间递增（ReviewedAt 用于 FIFO 淘汰）
		time.Sleep(1 * time.Millisecond)
	}

	// 应淘汰最旧的（content-a）
	if cache.Size() != 3 {
		t.Errorf("Size = %d, want 3 (after eviction)", cache.Size())
	}

	// content-a 应被淘汰
	hashA := ComputeFileHash([]byte("content-a"))
	_, ok := cache.Get(hashA)
	if ok {
		t.Error("oldest entry (content-a) should be evicted")
	}

	// content-d 应存在
	hashD := ComputeFileHash([]byte("content-d"))
	_, ok = cache.Get(hashD)
	if !ok {
		t.Error("newest entry (content-d) should still exist")
	}
}

// === Save/Load 测试 ===

// TestHashCache_SaveAndLoad 测试持久化 + 加载。
func TestHashCache_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "review_hashes.json")

	// 创建缓存 + 存入数据
	cache1 := NewHashCache(cachePath)
	result := ReviewResult{
		Summary:  "persisted summary",
		Findings: []Finding{{File: "test.go", Line: 5, Severity: "error"}},
		Metrics:  ReviewMetrics{TotalFindings: 1, Blockers: 1},
	}
	fileHash := ComputeFileHash([]byte("persisted content"))
	cache1.Put("/persisted/file.go", fileHash, result)

	// Save
	if err := cache1.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}

	// 验证文件权限 0600
	info, _ := os.Stat(cachePath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("file permission = %v, want 0600", info.Mode().Perm())
	}

	// 创建新缓存 + Load
	cache2 := NewHashCache(cachePath)
	if err := cache2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 验证数据一致
	got, ok := cache2.Get(fileHash)
	if !ok {
		t.Fatal("Get after Load returned not ok")
	}
	if got.Summary != "persisted summary" {
		t.Errorf("Summary = %q, want 'persisted summary'", got.Summary)
	}
	if len(got.Findings) != 1 || got.Findings[0].File != "test.go" {
		t.Errorf("Findings mismatch: %+v", got.Findings)
	}
	if got.Metrics.TotalFindings != 1 || got.Metrics.Blockers != 1 {
		t.Errorf("Metrics mismatch: %+v", got.Metrics)
	}
}

// TestHashCache_LoadFileNotExists 测试加载不存在的文件（静默降级）。
func TestHashCache_LoadFileNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "nonexistent.json")

	cache := NewHashCache(cachePath)
	err := cache.Load()
	if err != nil {
		t.Errorf("Load on nonexistent file should return nil, got: %v", err)
	}
	if cache.Size() != 0 {
		t.Errorf("Size = %d, want 0 after Load nonexistent", cache.Size())
	}
}

// TestHashCache_LoadInvalidJSON 测试加载非法 JSON 文件（返回 error）。
func TestHashCache_LoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "invalid.json")

	// 写入非法 JSON
	if err := os.WriteFile(cachePath, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write invalid JSON: %v", err)
	}

	cache := NewHashCache(cachePath)
	err := cache.Load()
	if err == nil {
		t.Error("Load on invalid JSON should return error, got nil")
	}
}

// TestHashCache_SaveEmptyPath 测试空路径 Save 返回 error。
func TestHashCache_SaveEmptyPath(t *testing.T) {
	cache := NewHashCache("")
	err := cache.Save()
	if err == nil {
		t.Error("Save with empty path should return error, got nil")
	}
}

// TestHashCache_LoadEmptyPath 测试空路径 Load 返回 error。
func TestHashCache_LoadEmptyPath(t *testing.T) {
	cache := NewHashCache("")
	err := cache.Load()
	if err == nil {
		t.Error("Load with empty path should return error, got nil")
	}
}

// TestHashCache_SaveAtomic 测试原子写入（先写 .tmp 再 rename）。
func TestHashCache_SaveAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "review_hashes.json")

	cache := NewHashCache(cachePath)
	cache.Put("/file.go", ComputeFileHash([]byte("content")), ReviewResult{Summary: "test"})

	// Save
	if err := cache.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 验证 .tmp 文件不存在（已 rename）
	tmpPath := cachePath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file should not exist after Save (atomic rename): %v", err)
	}

	// 验证主文件存在
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("main cache file should exist after Save: %v", err)
	}
}

// === Clear 测试 ===

// TestHashCache_ClearMemory 测试清空内存缓存。
func TestHashCache_ClearMemory(t *testing.T) {
	cache := NewHashCache("")
	cache.Put("/file1.go", "hash1", ReviewResult{Summary: "1"})
	cache.Put("/file2.go", "hash2", ReviewResult{Summary: "2"})

	if cache.Size() != 2 {
		t.Errorf("Size = %d, want 2 before Clear", cache.Size())
	}

	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("Size = %d, want 0 after Clear", cache.Size())
	}
}

// TestHashCache_ClearDisk 测试清空磁盘文件。
func TestHashCache_ClearDisk(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "review_hashes.json")

	cache := NewHashCache(cachePath)
	cache.Put("/file.go", "hash", ReviewResult{Summary: "test"})
	cache.Save()

	// 验证文件存在
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file should exist before Clear: %v", err)
	}

	// Clear
	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// 验证文件已删除
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("cache file should not exist after Clear: %v", err)
	}
}

// TestHashCache_ClearNonExistentFile 测试 Clear 不存在的文件不报错。
func TestHashCache_ClearNonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "nonexistent.json")

	cache := NewHashCache(cachePath)
	// 不 Save，直接 Clear
	if err := cache.Clear(); err != nil {
		t.Errorf("Clear on nonexistent file should not error: %v", err)
	}
}

// === Size 测试 ===

// TestHashCache_Size 测试条目数统计。
func TestHashCache_Size(t *testing.T) {
	cache := NewHashCache("")

	if cache.Size() != 0 {
		t.Errorf("Size = %d, want 0", cache.Size())
	}

	cache.Put("/file1.go", "hash1", ReviewResult{})
	if cache.Size() != 1 {
		t.Errorf("Size = %d, want 1", cache.Size())
	}

	cache.Put("/file2.go", "hash2", ReviewResult{})
	if cache.Size() != 2 {
		t.Errorf("Size = %d, want 2", cache.Size())
	}

	// 相同 hash 覆盖（Size 不变）
	cache.Put("/file2-updated.go", "hash2", ReviewResult{})
	if cache.Size() != 2 {
		t.Errorf("Size = %d, want 2 (same hash overwrite)", cache.Size())
	}
}

// === 并发安全测试 ===

// TestHashCache_ConcurrentGetPut 测试并发 Get/Put（数据竞争检测）。
func TestHashCache_ConcurrentGetPut(t *testing.T) {
	cache := NewHashCache("")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		// 写 goroutine
		go func(idx int) {
			defer wg.Done()
			content := []byte("content-" + string(rune('a'+idx%26)) + string(rune('0'+idx/26)))
			hash := ComputeFileHash(content)
			cache.Put("/file"+string(rune('0'+idx))+".go", hash, ReviewResult{
				Summary: "review-" + string(rune('0'+idx)),
			})
		}(i)
		// 读 goroutine
		go func(idx int) {
			defer wg.Done()
			content := []byte("content-" + string(rune('a'+idx%26)) + string(rune('0'+idx/26)))
			hash := ComputeFileHash(content)
			cache.Get(hash)
		}(i)
	}
	wg.Wait()
	// 无 panic + 无数据竞争即通过
}

// TestHashCache_ConcurrentSave 测试并发 Save（数据竞争检测）。
func TestHashCache_ConcurrentSave(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "review_hashes.json")

	cache := NewHashCache(cachePath)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cache.Put("/file"+string(rune('0'+idx))+".go", "hash"+string(rune('0'+idx)), ReviewResult{})
		}(i)
	}
	wg.Wait()

	// 单次 Save（并发 Save 可能导致文件竞争，本测试仅验证 Put 并发安全）
	if err := cache.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
}

// === 集成测试 ===

// TestHashCache_PutGetSaveLoadClear 测试完整生命周期。
func TestHashCache_PutGetSaveLoadClear(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "review_hashes.json")

	// 1. 创建缓存
	cache := NewHashCache(cachePath)

	// 2. Put 数据
	result1 := ReviewResult{Summary: "review 1", Findings: []Finding{{File: "a.go", Line: 1}}}
	hash1 := ComputeFileHash([]byte("content 1"))
	cache.Put("/file1.go", hash1, result1)

	result2 := ReviewResult{Summary: "review 2", Findings: []Finding{{File: "b.go", Line: 2}}}
	hash2 := ComputeFileHash([]byte("content 2"))
	cache.Put("/file2.go", hash2, result2)

	// 3. Get 验证
	got1, ok := cache.Get(hash1)
	if !ok || got1.Summary != "review 1" {
		t.Errorf("Get file1: ok=%v summary=%q", ok, func() string {
			if got1 != nil {
				return got1.Summary
			}
			return ""
		}())
	}

	// 4. Save
	if err := cache.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 5. Load 到新缓存
	cache2 := NewHashCache(cachePath)
	if err := cache2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 6. Get 验证持久化数据
	got2, ok := cache2.Get(hash2)
	if !ok || got2.Summary != "review 2" {
		t.Errorf("Get file2 after Load: ok=%v summary=%q", ok, func() string {
			if got2 != nil {
				return got2.Summary
			}
			return ""
		}())
	}

	// 7. Clear
	if err := cache2.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// 8. 验证清空
	if cache2.Size() != 0 {
		t.Errorf("Size after Clear = %d, want 0", cache2.Size())
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("cache file should not exist after Clear: %v", err)
	}
}

// TestHashCache_OverwriteSameHash 测试相同 hash 覆盖更新。
func TestHashCache_OverwriteSameHash(t *testing.T) {
	cache := NewHashCache("")
	hash := ComputeFileHash([]byte("same content"))

	// 第一次 Put
	cache.Put("/file1.go", hash, ReviewResult{Summary: "first"})

	// 第二次 Put（相同 hash，不同 FilePath + 不同 ReviewResult）
	cache.Put("/file2.go", hash, ReviewResult{Summary: "second"})

	// Get 应返回第二次的结果
	got, ok := cache.Get(hash)
	if !ok {
		t.Fatal("Get returned not ok after overwrite")
	}
	if got.Summary != "second" {
		t.Errorf("Summary = %q, want 'second' (overwritten)", got.Summary)
	}

	// Size 应为 1（相同 hash 覆盖）
	if cache.Size() != 1 {
		t.Errorf("Size = %d, want 1 (same hash overwrite)", cache.Size())
	}
}

// TestHashCache_MultipleFilesSameContent 测试多文件相同内容（哈希一致）共享缓存。
func TestHashCache_MultipleFilesSameContent(t *testing.T) {
	cache := NewHashCache("")

	// 两个文件内容相同
	content := []byte("identical content")
	hash := ComputeFileHash(content)

	// Put 第一个文件
	cache.Put("/file1.go", hash, ReviewResult{Summary: "shared review"})

	// Get 第二个文件（相同哈希）
	got, ok := cache.Get(hash)
	if !ok {
		t.Fatal("Get with same hash but different filePath should hit")
	}
	if got.Summary != "shared review" {
		t.Errorf("Summary = %q, want 'shared review'", got.Summary)
	}
}
