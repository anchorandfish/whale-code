package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// newTestCache creates a FileContentCache with test-friendly config.
func newTestCache(t testing.TB) (*FileContentCache, string) {
	t.Helper()
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.MaxFileSize = 1 * 1024 * 1024
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	return cache, tmpDir
}

// writeFileWithMtime writes a file and sets its modification time.
func writeFileWithMtime(t testing.TB, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
}

// TestFileCache_FirstRead tests that first read returns FULL and creates cache entry
func TestFileCache_FirstRead(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	writeFileWithMtime(t, testFile, content, time.Now())

	result := cache.Read(testFile)

	if result.Status != CacheFull {
		t.Errorf("expected CacheFull, got %s", result.Status)
	}
	if result.Content != content {
		t.Errorf("expected content %q, got %q", content, result.Content)
	}
	if result.TokensSaved != 0 {
		t.Errorf("expected 0 tokens saved, got %d", result.TokensSaved)
	}
	if cache.EntryCount() != 1 {
		t.Errorf("expected 1 cache entry, got %d", cache.EntryCount())
	}
}

// TestFileCache_UnchangedHit tests that second read with unchanged file returns UNCHANGED
func TestFileCache_UnchangedHit(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	mtime := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// first read
	cache.Read(testFile)

	// second read - should be UNCHANGED
	result := cache.Read(testFile)

	if result.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged, got %s", result.Status)
	}
	if !strings.Contains(result.Content, "[FILE_UNCHANGED:") {
		t.Errorf("expected UNCHANGED marker, got %q", result.Content)
	}
	if result.TokensSaved <= 0 {
		t.Errorf("expected tokens saved > 0, got %d", result.TokensSaved)
	}
}

// TestFileCache_ContentUnchangedMtimeChanged tests that mtime change but same content returns UNCHANGED
func TestFileCache_ContentUnchangedMtimeChanged(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	mtime1 := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	writeFileWithMtime(t, testFile, content, mtime1)

	// first read
	cache.Read(testFile)

	// change mtime but keep content same
	mtime2 := time.Now().Truncate(time.Second)
	// also change size by rewriting same content (size won't change)
	// but mtime will change
	if err := os.Chtimes(testFile, mtime2, mtime2); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	result := cache.Read(testFile)

	if result.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged (content same, mtime changed), got %s", result.Status)
	}
}

// TestFileCache_SmallChange tests that small change returns DIFF
func TestFileCache_SmallChange(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")

	// create a file with many lines
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")
	mtime := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// first read
	cache.Read(testFile)

	// make a small change (1 line out of 100)
	lines[49] = "line 50 MODIFIED"
	newContent := strings.Join(lines, "\n")
	mtime2 := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, newContent, mtime2)

	result := cache.Read(testFile)

	if result.Status != CacheDiff {
		t.Errorf("expected CacheDiff, got %s", result.Status)
	}
	if !strings.Contains(result.Content, "[FILE_MODIFIED:") {
		t.Errorf("expected FILE_MODIFIED marker, got %q", result.Content)
	}
	if result.DiffStats == nil {
		t.Error("expected DiffStats to be set")
	}
	if result.TokensSaved <= 0 {
		t.Errorf("expected tokens saved > 0 for diff, got %d", result.TokensSaved)
	}
}

// TestFileCache_LargeChange tests that large change returns FULL
func TestFileCache_LargeChange(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")

	// create a file with some lines
	content := "line 1\nline 2\nline 3\nline 4\nline 5"
	mtime := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// first read
	cache.Read(testFile)

	// completely change the file
	newContent := "completely different content\nwith new lines\nthat are totally new\nand more new lines\nand even more"
	mtime2 := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, newContent, mtime2)

	result := cache.Read(testFile)

	if result.Status != CacheFull {
		t.Errorf("expected CacheFull for large change, got %s", result.Status)
	}
	if result.Content != newContent {
		t.Errorf("expected new content, got %q", result.Content)
	}
}

// TestFileCache_InvalidateAfterWrite tests that invalidation removes cache entry
func TestFileCache_InvalidateAfterWrite(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	writeFileWithMtime(t, testFile, content, time.Now())

	// first read - creates cache entry
	cache.Read(testFile)
	if cache.EntryCount() != 1 {
		t.Fatalf("expected 1 entry, got %d", cache.EntryCount())
	}

	// invalidate
	cache.Invalidate(testFile)
	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 entries after invalidate, got %d", cache.EntryCount())
	}

	// next read should be FULL again
	result := cache.Read(testFile)
	if result.Status != CacheFull {
		t.Errorf("expected CacheFull after invalidate, got %s", result.Status)
	}
}

// TestFileCache_InvalidateAll tests that InvalidateAll clears all entries
func TestFileCache_InvalidateAll(t *testing.T) {
	cache, tmpDir := newTestCache(t)

	for i := 0; i < 5; i++ {
		testFile := filepath.Join(tmpDir, fmt.Sprintf("test%d.txt", i))
		writeFileWithMtime(t, testFile, fmt.Sprintf("content %d", i), time.Now())
		cache.Read(testFile)
	}

	if cache.EntryCount() != 5 {
		t.Fatalf("expected 5 entries, got %d", cache.EntryCount())
	}

	cache.InvalidateAll()

	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 entries after InvalidateAll, got %d", cache.EntryCount())
	}
}

// TestFileCache_InvalidateByPrefix tests prefix-based invalidation
func TestFileCache_InvalidateByPrefix(t *testing.T) {
	cache, tmpDir := newTestCache(t)

	subDir1 := filepath.Join(tmpDir, "sub1")
	subDir2 := filepath.Join(tmpDir, "sub2")
	os.MkdirAll(subDir1, 0755)
	os.MkdirAll(subDir2, 0755)

	file1 := filepath.Join(subDir1, "a.txt")
	file2 := filepath.Join(subDir2, "b.txt")
	writeFileWithMtime(t, file1, "content a", time.Now())
	writeFileWithMtime(t, file2, "content b", time.Now())

	cache.Read(file1)
	cache.Read(file2)

	if cache.EntryCount() != 2 {
		t.Fatalf("expected 2 entries, got %d", cache.EntryCount())
	}

	cache.InvalidateByPrefix(subDir1)

	if cache.EntryCount() != 1 {
		t.Errorf("expected 1 entry after prefix invalidate, got %d", cache.EntryCount())
	}
}

// TestFileCache_LRUEviction tests that LRU eviction works when MaxEntries is exceeded
func TestFileCache_LRUEviction(t *testing.T) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.MaxEntries = 5
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	tmpDir := t.TempDir()

	// add more entries than MaxEntries
	for i := 0; i < 10; i++ {
		testFile := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		writeFileWithMtime(t, testFile, fmt.Sprintf("content %d", i), time.Now())
		cache.Read(testFile)
	}

	// should have evicted down to MaxEntries (or slightly less)
	if cache.EntryCount() > config.MaxEntries {
		t.Errorf("expected <= %d entries after eviction, got %d", config.MaxEntries, cache.EntryCount())
	}
}

// TestFileCache_BinaryFileSkip tests that binary files are not cached
func TestFileCache_BinaryFileSkip(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "binary.bin")

	// create binary content with null bytes
	binaryContent := make([]byte, 100)
	for i := range binaryContent {
		binaryContent[i] = byte(i % 256)
	}
	// add some null bytes
	binaryContent[0] = 0
	binaryContent[1] = 0
	if err := os.WriteFile(testFile, binaryContent, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result := cache.Read(testFile)

	// binary files should return FULL but not be cached
	if result.Status != CacheFull {
		t.Errorf("expected CacheFull for binary file, got %s", result.Status)
	}
	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 cache entries for binary file, got %d", cache.EntryCount())
	}
}

// TestFileCache_LargeFileSkip tests that files exceeding MaxFileSize are not cached
func TestFileCache_LargeFileSkip(t *testing.T) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.MaxFileSize = 100 // very small limit
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "large.txt")
	largeContent := strings.Repeat("a", 200) // exceeds 100 byte limit
	writeFileWithMtime(t, testFile, largeContent, time.Now())

	result := cache.Read(testFile)

	if result.Status != CacheFull {
		t.Errorf("expected CacheFull for large file, got %s", result.Status)
	}
	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 cache entries for large file, got %d", cache.EntryCount())
	}
}

// TestFileCache_PersistAndRestore tests persistence and restoration of cache metadata
func TestFileCache_PersistAndRestore(t *testing.T) {
	tmpDir := t.TempDir()
	persistPath := filepath.Join(tmpDir, "cache.json")

	config := DefaultFileCacheConfig()
	config.PersistToDisk = true
	config.PersistPath = persistPath

	cache := NewFileContentCache(config, ai.NewTokenEstimator())

	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	writeFileWithMtime(t, testFile, content, time.Now())

	// read to populate cache
	cache.Read(testFile)

	// persist
	if err := cache.Persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// verify file exists
	if _, err := os.Stat(persistPath); err != nil {
		t.Fatalf("persist file not created: %v", err)
	}

	// create new cache and restore
	cache2 := NewFileContentCache(config, ai.NewTokenEstimator())
	if err := cache2.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if cache2.EntryCount() != 1 {
		t.Errorf("expected 1 entry after restore, got %d", cache2.EntryCount())
	}

	// second read should be UNCHANGED (metadata restored, content will be re-read)
	result := cache2.Read(testFile)
	if result.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged after restore, got %s", result.Status)
	}
}

// TestFileCache_ConcurrentAccess tests concurrent read safety
func TestFileCache_ConcurrentAccess(t *testing.T) {
	cache, tmpDir := newTestCache(t)

	// create multiple test files
	var files []string
	for i := 0; i < 10; i++ {
		f := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		writeFileWithMtime(t, f, fmt.Sprintf("content %d", i), time.Now())
		files = append(files, f)
	}

	var wg sync.WaitGroup
	// concurrent reads
	for goroutine := 0; goroutine < 5; goroutine++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, f := range files {
				cache.Read(f)
			}
		}()
	}

	// concurrent invalidations
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, f := range files {
			cache.Invalidate(f)
		}
	}()

	wg.Wait()
	// if we get here without panic or race, test passes
}

// TestFileCache_DiffThreshold tests diff threshold boundary
func TestFileCache_DiffThreshold(t *testing.T) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiffThreshold = 0.3 // 30% threshold
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "test.txt")

	// 10 lines
	lines := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"}
	content := strings.Join(lines, "\n")
	mtime := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	cache.Read(testFile)

	// change 1 line out of 10 = 10% < 30% → DIFF
	lines[0] = "l1-modified"
	newContent := strings.Join(lines, "\n")
	mtime2 := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, newContent, mtime2)

	result := cache.Read(testFile)
	if result.Status != CacheDiff {
		t.Errorf("expected CacheDiff for 10%% change (threshold 30%%), got %s", result.Status)
	}

	// now change 5 lines out of 10 = 50% > 30% → FULL
	cache.Read(testFile) // re-cache
	for i := 0; i < 5; i++ {
		lines[i] = fmt.Sprintf("l%d-changed", i+1)
	}
	newContent2 := strings.Join(lines, "\n")
	mtime3 := time.Now().Truncate(time.Second).Add(5 * time.Second)
	writeFileWithMtime(t, testFile, newContent2, mtime3)

	result2 := cache.Read(testFile)
	if result2.Status != CacheFull {
		t.Errorf("expected CacheFull for 50%% change (threshold 30%%), got %s", result2.Status)
	}
}

// TestFileCache_SymlinkResolution tests that symlinks are resolved for cache keys
func TestFileCache_SymlinkResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require admin privileges on Windows")
	}

	cache, tmpDir := newTestCache(t)
	targetFile := filepath.Join(tmpDir, "target.txt")
	linkFile := filepath.Join(tmpDir, "link.txt")

	content := "symlink content"
	writeFileWithMtime(t, targetFile, content, time.Now())

	if err := os.Symlink(targetFile, linkFile); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// read via symlink
	result1 := cache.Read(linkFile)
	if result1.Status != CacheFull {
		t.Errorf("expected CacheFull for first read, got %s", result1.Status)
	}

	// read via target - should be UNCHANGED (same cache key after symlink resolution)
	result2 := cache.Read(targetFile)
	if result2.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged for target (same as symlink), got %s", result2.Status)
	}
}

// TestFileCache_MetricsAccuracy tests that metrics are recorded correctly
func TestFileCache_MetricsAccuracy(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	mtime := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// first read - FULL
	cache.Read(testFile)

	// second read - UNCHANGED
	cache.Read(testFile)

	metrics := cache.Metrics().Snapshot()

	if metrics.TotalReads != 2 {
		t.Errorf("expected 2 total reads, got %d", metrics.TotalReads)
	}
	if metrics.FullReads != 1 {
		t.Errorf("expected 1 full read, got %d", metrics.FullReads)
	}
	if metrics.UnchangedHits != 1 {
		t.Errorf("expected 1 unchanged hit, got %d", metrics.UnchangedHits)
	}
	if metrics.TotalTokensSaved <= 0 {
		t.Errorf("expected tokens saved > 0, got %d", metrics.TotalTokensSaved)
	}
}

// TestFileCache_DisabledCache tests that disabled cache always returns FULL
func TestFileCache_DisabledCache(t *testing.T) {
	config := DefaultFileCacheConfig()
	config.Enabled = false
	config.PersistToDisk = false
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "test.txt")
	content := "test content"
	writeFileWithMtime(t, testFile, content, time.Now())

	result1 := cache.Read(testFile)
	result2 := cache.Read(testFile)

	if result1.Status != CacheFull {
		t.Errorf("expected CacheFull when disabled, got %s", result1.Status)
	}
	if result2.Status != CacheFull {
		t.Errorf("expected CacheFull when disabled, got %s", result2.Status)
	}
	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 entries when disabled, got %d", cache.EntryCount())
	}
}

// TestFileCache_NonexistentFile tests reading a nonexistent file
func TestFileCache_NonexistentFile(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	nonexistentFile := filepath.Join(tmpDir, "nonexistent.txt")

	result := cache.Read(nonexistentFile)

	if result.Status != CacheError {
		t.Errorf("expected CacheError for nonexistent file, got %s", result.Status)
	}
}

// TestFileCache_Clear tests that Clear removes all entries and resets metrics
func TestFileCache_Clear(t *testing.T) {
	cache, tmpDir := newTestCache(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "content", time.Now())

	cache.Read(testFile)
	if cache.EntryCount() != 1 {
		t.Fatalf("expected 1 entry, got %d", cache.EntryCount())
	}

	cache.Clear()

	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 entries after clear, got %d", cache.EntryCount())
	}
	metrics := cache.Metrics().Snapshot()
	if metrics.TotalReads != 0 {
		t.Errorf("expected 0 total reads after clear, got %d", metrics.TotalReads)
	}
}

// TestFileCache_TopSavedFiles tests TopSavedFiles ranking
func TestFileCache_TopSavedFiles(t *testing.T) {
	cache, tmpDir := newTestCache(t)

	// create files with different read counts
	for i := 0; i < 5; i++ {
		f := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		writeFileWithMtime(t, f, fmt.Sprintf("content %d", i), time.Now())
		cache.Read(f)
		// read file 0 more times to give it more savings
		for j := 0; j < i; j++ {
			cache.Read(f)
		}
	}

	top := cache.TopSavedFiles(3)
	if len(top) != 3 {
		t.Errorf("expected 3 top files, got %d", len(top))
	}

	// should be sorted by tokens saved descending
	for i := 1; i < len(top); i++ {
		if top[i].TokensSaved > top[i-1].TokensSaved {
			t.Errorf("top files not sorted by tokens saved descending")
		}
	}
}

// TestFileCache_FormatUnchangedMessage tests the UNCHANGED message format
func TestFileCache_FormatUnchangedMessage(t *testing.T) {
	cache, _ := newTestCache(t)
	entry := &FileCacheEntry{
		Path:      "/test/file.go",
		SHA256:    "abcdef1234567890",
		LineCount: 42,
	}

	msg := cache.formatUnchangedMessage(entry)

	if !strings.Contains(msg, "/test/file.go") {
		t.Errorf("message should contain path: %s", msg)
	}
	if !strings.Contains(msg, "42") {
		t.Errorf("message should contain line count: %s", msg)
	}
	if !strings.Contains(msg, "abcdef12") {
		t.Errorf("message should contain hash prefix: %s", msg)
	}
}

// TestFileCache_FormatDiffMessage tests the DIFF message format
func TestFileCache_FormatDiffMessage(t *testing.T) {
	cache, _ := newTestCache(t)

	msg := cache.formatDiffMessage("/test/file.go", "--- a/file.go\n+++ b/file.go\n")

	if !strings.Contains(msg, "[FILE_MODIFIED:") {
		t.Errorf("message should contain FILE_MODIFIED marker: %s", msg)
	}
	if !strings.Contains(msg, "/test/file.go") {
		t.Errorf("message should contain path: %s", msg)
	}
}

// TestReadFileWithCache_Integration tests Executor + FileCache end-to-end
func TestReadFileWithCache_Integration(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// attach file cache
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	e.FileCache = NewFileContentCache(config, ai.NewTokenEstimator())

	testFile := filepath.Join(tmpDir, "integration.txt")
	content := "line 1\nline 2\nline 3"
	writeFileWithMtime(t, testFile, content, time.Now())

	// first read - should return full content with line numbers
	result1 := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})
	if result1.IsError {
		t.Fatalf("first read failed: %s", result1.Output)
	}
	if !strings.Contains(result1.Output, "line 1") {
		t.Errorf("first read should contain file content: %s", result1.Output)
	}

	// second read - should return UNCHANGED marker
	result2 := e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})
	if result2.IsError {
		t.Fatalf("second read failed: %s", result2.Output)
	}
	if !strings.Contains(result2.Output, "[FILE_UNCHANGED:") {
		t.Errorf("second read should contain UNCHANGED marker: %s", result2.Output)
	}
}

// TestWriteInvalidatesCache tests that write_file invalidates the cache
func TestWriteInvalidatesCache(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	e.FileCache = NewFileContentCache(config, ai.NewTokenEstimator())

	testFile := filepath.Join(tmpDir, "write_test.txt")
	writeFileWithMtime(t, testFile, "original content", time.Now())

	// first read - caches the file
	e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})

	// write new content
	e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "write_file",
		Input: mustMarshal(map[string]string{"path": testFile, "content": "new content"}),
	})

	// read again - should return FULL (cache was invalidated)
	result := e.Execute(context.Background(), Call{
		ID:    "3",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})

	if result.IsError {
		t.Fatalf("read after write failed: %s", result.Output)
	}
	// should contain the new content with line numbers, not UNCHANGED marker
	if strings.Contains(result.Output, "[FILE_UNCHANGED:") {
		t.Errorf("cache should have been invalidated after write: %s", result.Output)
	}
	if !strings.Contains(result.Output, "new content") {
		t.Errorf("should contain new content: %s", result.Output)
	}
}

// TestEditInvalidatesCache tests that edit_file invalidates the cache
func TestEditInvalidatesCache(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	e.FileCache = NewFileContentCache(config, ai.NewTokenEstimator())

	testFile := filepath.Join(tmpDir, "edit_test.txt")
	writeFileWithMtime(t, testFile, "hello world\nsecond line", time.Now())

	// first read - caches the file
	e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})

	// edit the file
	e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "edit_file",
		Input: mustMarshal(map[string]string{"path": testFile, "old_text": "hello", "new_text": "goodbye"}),
	})

	// read again - should return FULL (cache was invalidated)
	result := e.Execute(context.Background(), Call{
		ID:    "3",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})

	if result.IsError {
		t.Fatalf("read after edit failed: %s", result.Output)
	}
	if strings.Contains(result.Output, "[FILE_UNCHANGED:") {
		t.Errorf("cache should have been invalidated after edit: %s", result.Output)
	}
	if !strings.Contains(result.Output, "goodbye") {
		t.Errorf("should contain edited content: %s", result.Output)
	}
}

// TestReadFile_OffsetLimitSkipsCache tests that offset/limit reads don't use cache
func TestReadFile_OffsetLimitSkipsCache(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	e.FileCache = NewFileContentCache(config, ai.NewTokenEstimator())

	testFile := filepath.Join(tmpDir, "offset_test.txt")
	lines := []string{"line 1", "line 2", "line 3", "line 4", "line 5"}
	writeFileWithMtime(t, testFile, strings.Join(lines, "\n"), time.Now())

	// read with offset - should NOT use cache, should return line numbers
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "read_file",
		Input: mustMarshal(map[string]interface{}{"path": testFile, "offset": 2, "limit": 2}),
	})

	if result.IsError {
		t.Fatalf("offset read failed: %s", result.Output)
	}
	if !strings.Contains(result.Output, "line 2") {
		t.Errorf("should contain line 2: %s", result.Output)
	}
	if strings.Contains(result.Output, "line 1") {
		t.Errorf("should not contain line 1 (offset=2): %s", result.Output)
	}
}

// TestFileCache_SkipPattern tests that skip patterns are respected
func TestFileCache_SkipPattern(t *testing.T) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	tmpDir := t.TempDir()

	// create a file in a node_modules directory
	nodeModulesDir := filepath.Join(tmpDir, "node_modules")
	os.MkdirAll(nodeModulesDir, 0755)
	testFile := filepath.Join(nodeModulesDir, "package.js")
	writeFileWithMtime(t, testFile, "module.exports = {};", time.Now())

	result := cache.Read(testFile)

	// should return FULL but not be cached (skip pattern match)
	if result.Status != CacheFull {
		t.Errorf("expected CacheFull for skip pattern file, got %s", result.Status)
	}
	if cache.EntryCount() != 0 {
		t.Errorf("expected 0 entries for skip pattern file, got %d", cache.EntryCount())
	}
}

// BenchmarkFileCache_UnchangedHit benchmarks UNCHANGED hit performance
func BenchmarkFileCache_UnchangedHit(b *testing.B) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	tmpDir := b.TempDir()

	testFile := filepath.Join(tmpDir, "bench.txt")
	content := strings.Repeat("benchmark line\n", 100)
	writeFileWithMtime(b, testFile, content, time.Now())

	// prime the cache
	cache.Read(testFile)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Read(testFile)
	}
}

// BenchmarkFileCache_SHA256_100KB benchmarks SHA256 computation for 100KB file
func BenchmarkFileCache_SHA256_100KB(b *testing.B) {
	data := make([]byte, 100*1024)
	for i := range data {
		data[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeSHA256(data)
	}
}

// BenchmarkFileCache_Diff_1000Lines benchmarks diff generation for 1000 lines
func BenchmarkFileCache_Diff_1000Lines(b *testing.B) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	_ = NewFileContentCache(config, ai.NewTokenEstimator()) // 预热构造（历史遗留，实际基准使用 cache2）
	tmpDir := b.TempDir()

	testFile := filepath.Join(tmpDir, "diff_bench.txt")
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")
	writeFileWithMtime(b, testFile, content, time.Now())

	// modify a few lines
	lines[500] = "line 500 MODIFIED"
	lines[501] = "line 501 MODIFIED"
	newContent := strings.Join(lines, "\n")
	writeFileWithMtime(b, testFile, newContent, time.Now().Add(time.Second))

	// prime cache with original content
	cache2 := NewFileContentCache(config, ai.NewTokenEstimator())
	writeFileWithMtime(b, testFile, content, time.Now().Add(-time.Hour))
	cache2.Read(testFile)
	writeFileWithMtime(b, testFile, newContent, time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// reset cache to force diff
		cache2.entries[filepath.Clean(testFile)].Content = content
		cache2.entries[filepath.Clean(testFile)].SHA256 = "old"
		cache2.Read(testFile)
	}
}

// ============================================================
// T1-P1-B4 集成测试：磁盘 LRU 缓存集成（S5 增量）
// ============================================================

// TestFileCache_DiskCacheHitAfterRestart 重启后磁盘缓存命中。
// 场景：cache1 读取文件 → 创建 cache2（共享磁盘缓存目录）→ cache2 读取同一文件应命中磁盘缓存。
func TestFileCache_DiskCacheHitAfterRestart(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultFileCacheConfig()
	config.PersistToDisk = false // 禁用元数据 JSON，仅测试磁盘 LRU
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line 1\nline 2\nline 3"
	mtime := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// 第一次：创建 cache1，读取文件（写入磁盘缓存）
	cache1 := NewFileContentCache(config, ai.NewTokenEstimator())
	result1 := cache1.Read(testFile)
	if result1.Status != CacheFull {
		t.Fatalf("expected CacheFull on first read, got %s", result1.Status)
	}

	// 验证磁盘缓存有条目
	if cache1.diskStore == nil {
		t.Fatal("expected diskStore to be initialized")
	}
	metrics1 := cache1.DiskMetrics()
	if metrics1.DiskEntryCount != 1 {
		t.Errorf("expected 1 disk entry, got %d", metrics1.DiskEntryCount)
	}

	// 模拟重启：创建 cache2（共享同一磁盘缓存目录）
	cache2 := NewFileContentCache(config, ai.NewTokenEstimator())
	result2 := cache2.Read(testFile)

	// 应命中磁盘缓存，返回 CacheUnchanged
	if result2.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged after restart (disk hit), got %s", result2.Status)
	}
}

// TestFileCache_DiskCacheFallback 磁盘缓存初始化失败时静默降级。
// 场景：RootDir 父路径是普通文件（无法创建目录）→ NewFileContentCache 应静默降级为仅内存模式 → Read 仍能工作。
func TestFileCache_DiskCacheFallback(t *testing.T) {
	tmpDir := t.TempDir()
	// 创建一个普通文件作为 RootDir 的父路径（MkdirAll 必失败）
	filePath := filepath.Join(tmpDir, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0600); err != nil {
		t.Fatalf("create file: %v", err)
	}

	config := DefaultFileCacheConfig()
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(filePath, "subdir") // 父路径是文件，MkdirAll 必失败

	// NewFileContentCache 应静默降级
	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	if cache.diskStore != nil {
		t.Error("expected diskStore=nil on failure, got non-nil")
	}

	// Read 应仍能工作（仅内存模式）
	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello", time.Now())

	result := cache.Read(testFile)
	if result.Status != CacheFull {
		t.Errorf("expected CacheFull in fallback mode, got %s", result.Status)
	}
}

// TestFileCache_InvalidateDeletesDisk 同步删除磁盘缓存。
// 场景：Read 写入磁盘 → Invalidate 应同步删除磁盘条目。
func TestFileCache_InvalidateDeletesDisk(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello", time.Now())

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	cache.Read(testFile)

	// 验证磁盘缓存有条目
	if cache.diskStore.Metrics().DiskEntryCount != 1 {
		t.Fatalf("expected 1 disk entry, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}

	// Invalidate 应同步删除磁盘
	cache.Invalidate(testFile)

	if cache.diskStore.Metrics().DiskEntryCount != 0 {
		t.Errorf("expected 0 disk entries after invalidate, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}
}

// TestFileCache_InvalidateAllDeletesDisk InvalidateAll 同步删除磁盘缓存。
func TestFileCache_InvalidateAllDeletesDisk(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	// 写入 3 个文件并读取（缓存到磁盘）
	for i := 0; i < 3; i++ {
		testFile := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		writeFileWithMtime(t, testFile, fmt.Sprintf("content %d", i), time.Now())
	}

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	for i := 0; i < 3; i++ {
		cache.Read(filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i)))
	}

	if cache.diskStore.Metrics().DiskEntryCount != 3 {
		t.Fatalf("expected 3 disk entries, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}

	cache.InvalidateAll()

	if cache.diskStore.Metrics().DiskEntryCount != 0 {
		t.Errorf("expected 0 disk entries after InvalidateAll, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}
}

// TestFileCache_InvalidateByPrefixDeletesDisk InvalidateByPrefix 同步删除磁盘缓存。
func TestFileCache_InvalidateByPrefixDeletesDisk(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	subDir1 := filepath.Join(tmpDir, "sub1")
	subDir2 := filepath.Join(tmpDir, "sub2")
	os.MkdirAll(subDir1, 0755)
	os.MkdirAll(subDir2, 0755)

	file1 := filepath.Join(subDir1, "a.txt")
	file2 := filepath.Join(subDir2, "b.txt")
	writeFileWithMtime(t, file1, "content a", time.Now())
	writeFileWithMtime(t, file2, "content b", time.Now())

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	cache.Read(file1)
	cache.Read(file2)

	if cache.diskStore.Metrics().DiskEntryCount != 2 {
		t.Fatalf("expected 2 disk entries, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}

	// 按前缀删除 sub1
	cache.InvalidateByPrefix(subDir1)

	// 应剩 1 个（sub2/b.txt）
	if cache.diskStore.Metrics().DiskEntryCount != 1 {
		t.Errorf("expected 1 disk entry after prefix invalidate, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}
}

// TestFileCache_DiskCacheMetrics 磁盘缓存指标准确性。
func TestFileCache_DiskCacheMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello world", time.Now())

	cache := NewFileContentCache(config, ai.NewTokenEstimator())

	// 首次读取 → FULL + 写入磁盘
	cache.Read(testFile)

	metrics := cache.DiskMetrics()
	if metrics.DiskWrites != 1 {
		t.Errorf("expected DiskWrites=1, got %d", metrics.DiskWrites)
	}
	if metrics.DiskEntryCount != 1 {
		t.Errorf("expected DiskEntryCount=1, got %d", metrics.DiskEntryCount)
	}
}

// TestFileCache_DiskCacheThreeStageDetection 三级降级链验证。
// 场景：cache1 读取文件 → 修改文件 mtime（不动内容）→ cache2 读取应通过 Stage 2 (SHA256) 命中 UNCHANGED。
func TestFileCache_DiskCacheThreeStageDetection(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	content := "unchanged content"
	mtime := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// cache1 读取文件
	cache1 := NewFileContentCache(config, ai.NewTokenEstimator())
	cache1.Read(testFile)

	// 修改 mtime 但不动内容（touch）
	newMtime := time.Now().Truncate(time.Second)
	if err := os.Chtimes(testFile, newMtime, newMtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// cache2 读取，磁盘 hit + Stage 2 (SHA256 一致) → UNCHANGED
	cache2 := NewFileContentCache(config, ai.NewTokenEstimator())
	result := cache2.Read(testFile)

	if result.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged via disk + SHA256 detection, got %s", result.Status)
	}
}

// TestFileCache_DiskCacheContentChangedDiff 内容变更时复用磁盘内容做 diff。
// 场景：cache1 读取文件 → 修改文件内容（小变更）→ cache2 读取应通过 Stage 3 返回 DIFF。
func TestFileCache_DiskCacheContentChangedDiff(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	// 创建大文件便于触发 diff 而非 full
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")
	mtime := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	writeFileWithMtime(t, testFile, content, mtime)

	// cache1 读取文件
	cache1 := NewFileContentCache(config, ai.NewTokenEstimator())
	cache1.Read(testFile)

	// 修改一行（小变更）
	lines[49] = "line 50 MODIFIED"
	newContent := strings.Join(lines, "\n")
	newMtime := time.Now().Truncate(time.Second)
	writeFileWithMtime(t, testFile, newContent, newMtime)

	// cache2 读取，磁盘 hit + Stage 3 (内容变) → DIFF
	cache2 := NewFileContentCache(config, ai.NewTokenEstimator())
	result := cache2.Read(testFile)

	// 应返回 DIFF（小变更）或 FULL（大变更），不应是 UNCHANGED
	if result.Status == CacheUnchanged {
		t.Errorf("expected non-UNCHANGED for content change, got %s", result.Status)
	}
	if result.Status != CacheDiff && result.Status != CacheFull {
		t.Errorf("expected CacheDiff or CacheFull, got %s", result.Status)
	}
}

// TestFileCache_DiskCacheClosePersist Close 持久化磁盘索引。
func TestFileCache_DiskCacheClosePersist(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")
	config.DiskCache.SyncMode = SyncOnEvict // Close 时持久化

	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello", time.Now())

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	cache.Read(testFile)

	if err := cache.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 验证 index.json 已持久化
	indexPath := filepath.Join(tmpDir, "cache", "index.json")
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("expected index.json after Close: %v", err)
	}
}

// TestFileCache_DiskCachePersistSync Persist 同步磁盘索引。
func TestFileCache_DiskCachePersistSync(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = true
	config.PersistPath = filepath.Join(tmpDir, "meta.json")
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello", time.Now())

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	cache.Read(testFile)

	// Persist 应同时持久化元数据 JSON 和磁盘索引
	if err := cache.Persist(); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// 验证元数据 JSON
	if _, err := os.Stat(filepath.Join(tmpDir, "meta.json")); err != nil {
		t.Errorf("expected meta.json: %v", err)
	}
	// 验证磁盘索引
	indexPath := filepath.Join(tmpDir, "cache", "index.json")
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("expected cache/index.json: %v", err)
	}
}

// TestFileCache_DiskCacheDisabledBackwardCompat 未启用磁盘缓存时向后兼容。
// 场景：DiskCache.Enabled=false（默认）→ diskStore=nil → 现有行为不变。
func TestFileCache_DiskCacheDisabledBackwardCompat(t *testing.T) {
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = false // 显式禁用

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	if cache.diskStore != nil {
		t.Error("expected diskStore=nil when DiskCache.Enabled=false")
	}

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello", time.Now())

	// 应仍能正常读取（仅内存模式）
	result1 := cache.Read(testFile)
	if result1.Status != CacheFull {
		t.Errorf("expected CacheFull, got %s", result1.Status)
	}

	// 第二次读取应 UNCHANGED（内存命中）
	result2 := cache.Read(testFile)
	if result2.Status != CacheUnchanged {
		t.Errorf("expected CacheUnchanged, got %s", result2.Status)
	}

	// DiskMetrics 应返回零值
	metrics := cache.DiskMetrics()
	if metrics.DiskEntryCount != 0 || metrics.DiskWrites != 0 {
		t.Errorf("expected zero disk metrics when disabled, got %+v", metrics)
	}
}

// TestFileCache_DiskCacheClearSyncsDiskClear Clear 同步清理磁盘索引。
func TestFileCache_DiskCacheClearSyncsDiskClear(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")

	testFile := filepath.Join(tmpDir, "test.txt")
	writeFileWithMtime(t, testFile, "hello", time.Now())

	cache := NewFileContentCache(config, ai.NewTokenEstimator())
	cache.Read(testFile)

	if cache.diskStore.Metrics().DiskEntryCount != 1 {
		t.Fatalf("expected 1 disk entry before clear, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}

	cache.Clear()

	// Clear 后磁盘索引应清空（注：磁盘文件由 GC 清理，索引立即清空）
	if cache.diskStore.Metrics().DiskEntryCount != 0 {
		t.Errorf("expected 0 disk entries after Clear, got %d", cache.diskStore.Metrics().DiskEntryCount)
	}
}

// TestFileCache_DiskCacheConcurrentAccessWithDisk 启用磁盘缓存时并发读写安全。
func TestFileCache_DiskCacheConcurrentAccessWithDisk(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultFileCacheConfig()
	config.PersistToDisk = false
	config.DiskCache.Enabled = true
	config.DiskCache.RootDir = filepath.Join(tmpDir, "cache")
	config.MaxEntries = 100 // 放大上限避免内存淘汰干扰

	cache := NewFileContentCache(config, ai.NewTokenEstimator())

	// 创建 10 个测试文件
	var files []string
	for i := 0; i < 10; i++ {
		f := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		writeFileWithMtime(t, f, fmt.Sprintf("content %d", i), time.Now())
		files = append(files, f)
	}

	var wg sync.WaitGroup
	// 5 个 goroutine 并发读取
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, f := range files {
				cache.Read(f)
			}
		}()
	}
	// 1 个 goroutine 并发 Invalidate
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, f := range files {
			cache.Invalidate(f)
		}
	}()

	wg.Wait()
	// 不发生 panic / race 即通过（race 由 -race 检测器验证）
}
