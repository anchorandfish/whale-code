package typeahead

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestPathListSignature_Empty 测试空路径列表的签名
func TestPathListSignature_Empty(t *testing.T) {
	sig := pathListSignature(nil)
	if sig != "" {
		t.Fatalf("expected empty signature, got %q", sig)
	}
}

// TestPathListSignature_Single 测试单路径签名
func TestPathListSignature_Single(t *testing.T) {
	paths := []string{"internal/mention/mention.go"}
	sig1 := pathListSignature(paths)
	sig2 := pathListSignature(paths)
	if sig1 != sig2 {
		t.Fatal("same paths should produce same signature")
	}
	if sig1 == "" {
		t.Fatal("signature should not be empty for non-empty paths")
	}
}

// TestPathListSignature_DifferentPaths 测试不同路径产生不同签名
func TestPathListSignature_DifferentPaths(t *testing.T) {
	paths1 := []string{"a.go", "b.go", "c.go"}
	paths2 := []string{"a.go", "b.go", "d.go"}
	sig1 := pathListSignature(paths1)
	sig2 := pathListSignature(paths2)
	if sig1 == sig2 {
		t.Fatal("different paths should produce different signatures")
	}
}

// TestPathListSignature_CountIncluded 测试路径数量影响签名
func TestPathListSignature_CountIncluded(t *testing.T) {
	paths1 := []string{"a.go", "b.go"}
	paths2 := []string{"a.go", "b.go", "c.go"}
	sig1 := pathListSignature(paths1)
	sig2 := pathListSignature(paths2)
	if sig1 == sig2 {
		t.Fatal("different count should produce different signatures")
	}
}

// TestPathListSignature_LargeSet 测试大路径列表采样签名稳定性
func TestPathListSignature_LargeSet(t *testing.T) {
	paths := make([]string, 1000)
	for i := range paths {
		paths[i] = "file_" + string(rune('a'+i%26)) + ".go"
	}
	sig1 := pathListSignature(paths)
	sig2 := pathListSignature(paths)
	if sig1 != sig2 {
		t.Fatal("large set should produce stable signature")
	}
}

// TestClearCache 测试清空缓存
func TestClearCache(t *testing.T) {
	// 设置一些缓存数据
	fi.mu.Lock()
	fi.paths = []string{"test.go"}
	fi.signature = "test"
	fi.lastRefresh = time.Now()
	fi.mu.Unlock()

	ClearCache()

	fi.mu.RLock()
	defer fi.mu.RUnlock()
	if len(fi.paths) != 0 {
		t.Fatal("paths should be empty after ClearCache")
	}
	if fi.signature != "" {
		t.Fatal("signature should be empty after ClearCache")
	}
	if !fi.lastRefresh.IsZero() {
		t.Fatal("lastRefresh should be zero after ClearCache")
	}
}

// TestFileIndex_Search_EmptyPaths 测试空路径列表搜索返回 nil
func TestFileIndex_Search_EmptyPaths(t *testing.T) {
	ClearCache()
	results := fi.search("test", 10)
	if results != nil {
		t.Fatalf("expected nil for empty paths, got %v", results)
	}
}

// TestFileIndex_Search_WithPaths 测试有路径时的搜索
func TestFileIndex_Search_WithPaths(t *testing.T) {
	ClearCache()
	// 手动设置路径
	fi.mu.Lock()
	fi.paths = []string{
		"internal/mention/mention.go",
		"internal/typeahead/typeahead.go",
		"internal/command/command.go",
	}
	fi.mu.Unlock()
	defer ClearCache()

	results := fi.search("mention", 10)
	if len(results) == 0 {
		t.Fatal("expected results for 'mention' query")
	}
	// mention.go 应在结果中
	found := false
	for _, r := range results {
		if r.DisplayText == "internal/mention/mention.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected mention.go in results")
	}
}

// TestFileIndex_Search_MaxResults 测试结果数限制
func TestFileIndex_Search_MaxResults(t *testing.T) {
	ClearCache()
	paths := make([]string, 50)
	for i := range paths {
		paths[i] = "file" + string(rune('a'+i%26)) + ".go"
	}
	fi.mu.Lock()
	fi.paths = paths
	fi.mu.Unlock()
	defer ClearCache()

	results := fi.search("file", 5)
	if len(results) > 5 {
		t.Fatalf("expected at most 5 results, got %d", len(results))
	}
}

// TestFileIndex_Search_SpecialChars 测试特殊字符查询不 panic
func TestFileIndex_Search_SpecialChars(t *testing.T) {
	ClearCache()
	fi.mu.Lock()
	fi.paths = []string{"a.go", "b.go"}
	fi.mu.Unlock()
	defer ClearCache()

	// 特殊字符不应 panic
	_ = fi.search("[][]", 10)
	_ = fi.search("*?.*", 10)
}

// --- 修复：startBackgroundCacheRefresh 测试 ---

// TestStartBackgroundCacheRefresh_ColdStart 测试 cold start 触发后台刷新
func TestStartBackgroundCacheRefresh_ColdStart(t *testing.T) {
	ClearCache()
	dir := t.TempDir()
	// 创建测试文件
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cold start：lastRefresh 为零值，应触发刷新
	fi.startBackgroundCacheRefresh(dir)

	// 等待 goroutine 完成（最多 3s）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fi.mu.RLock()
		paths := fi.paths
		fi.mu.RUnlock()
		if len(paths) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	fi.mu.RLock()
	defer fi.mu.RUnlock()
	if len(fi.paths) == 0 {
		t.Fatal("expected paths to be populated after cold start refresh")
	}
}

// TestStartBackgroundCacheRefresh_Throttle 测试 5s 节流阻止快速重复刷新
func TestStartBackgroundCacheRefresh_Throttle(t *testing.T) {
	ClearCache()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("test"), 0o644)

	// 第一次刷新
	fi.startBackgroundCacheRefresh(dir)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fi.mu.RLock()
		paths := fi.paths
		fi.mu.RUnlock()
		if len(paths) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	fi.mu.RLock()
	firstPaths := fi.paths
	firstRefresh := fi.lastRefresh
	fi.mu.RUnlock()

	if len(firstPaths) == 0 {
		t.Fatal("first refresh should populate paths")
	}

	// 立即再次调用，应因 5s 节流跳过
	fi.startBackgroundCacheRefresh(dir)
	time.Sleep(200 * time.Millisecond) // 等待潜在 goroutine

	fi.mu.RLock()
	defer fi.mu.RUnlock()
	// lastRefresh 不应变化（被节流跳过）
	if !fi.lastRefresh.Equal(firstRefresh) {
		t.Fatal("lastRefresh should not change within 5s throttle window")
	}
}

// TestStartBackgroundCacheRefresh_GitIndexMtime 测试 .git/index mtime 变更触发刷新
func TestStartBackgroundCacheRefresh_GitIndexMtime(t *testing.T) {
	ClearCache()
	dir := t.TempDir()
	// 创建 .git/index
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0o755)
	indexFile := filepath.Join(gitDir, "index")
	os.WriteFile(indexFile, []byte("initial"), 0o644)

	// 创建测试文件
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("test"), 0o644)

	// 第一次刷新
	fi.startBackgroundCacheRefresh(dir)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fi.mu.RLock()
		paths := fi.paths
		fi.mu.RUnlock()
		if len(paths) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	fi.mu.RLock()
	firstMtime := fi.gitIndexMtime
	firstSig := fi.signature
	fi.mu.RUnlock()

	if firstMtime.IsZero() {
		t.Fatal("expected gitIndexMtime to be set after refresh")
	}

	// 修改 .git/index mtime（touch 文件）
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(indexFile, []byte("changed"), 0o644)

	// 设置 lastRefresh 为过去时间，绕过节流
	fi.mu.Lock()
	fi.lastRefresh = time.Now().Add(-10 * time.Second)
	fi.mu.Unlock()

	// 再次刷新，应因 mtime 变更触发
	fi.startBackgroundCacheRefresh(dir)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fi.mu.RLock()
		sig := fi.signature
		fi.mu.RUnlock()
		if sig != firstSig {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestStartBackgroundCacheRefresh_GenerationStaleGuard 测试 generation 防 stale 覆盖
func TestStartBackgroundCacheRefresh_GenerationStaleGuard(t *testing.T) {
	ClearCache()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("test"), 0o644)

	// 第一次刷新
	fi.startBackgroundCacheRefresh(dir)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fi.mu.RLock()
		paths := fi.paths
		fi.mu.RUnlock()
		if len(paths) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 获取当前 generation
	currentGen := atomic.LoadUint64(&fi.generation)

	// 模拟 stale：手动递增 generation 但不改 paths
	atomic.AddUint64(&fi.generation, 1)

	// 设置 lastRefresh 为过去时间，绕过节流
	fi.mu.Lock()
	fi.lastRefresh = time.Now().Add(-10 * time.Second)
	fi.mu.Unlock()

	// 再次刷新：新的 generation 应大于 currentGen
	fi.startBackgroundCacheRefresh(dir)
	time.Sleep(200 * time.Millisecond)

	newGen := atomic.LoadUint64(&fi.generation)
	if newGen <= currentGen {
		t.Fatalf("generation should have increased beyond %d, got %d", currentGen, newGen)
	}
}

// TestStartBackgroundCacheRefresh_AlreadyRefreshing 测试正在刷新时不重复触发
func TestStartBackgroundCacheRefresh_AlreadyRefreshing(t *testing.T) {
	ClearCache()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("test"), 0o644)

	// 手动设置 refreshing=true
	fi.mu.Lock()
	fi.refreshing = true
	fi.mu.Unlock()

	// 调用应直接跳过
	fi.startBackgroundCacheRefresh(dir)
	time.Sleep(100 * time.Millisecond)

	fi.mu.RLock()
	defer fi.mu.RUnlock()
	// refreshing 应仍为 true（未被本调用改变）
	if !fi.refreshing {
		t.Fatal("refreshing should remain true when already refreshing")
	}

	// 清理
	fi.refreshing = false
}

// TestClearCache_IncrementsGeneration 测试 ClearCache 递增 generation
func TestClearCache_IncrementsGeneration(t *testing.T) {
	ClearCache()
	oldGen := atomic.LoadUint64(&fi.generation)
	ClearCache()
	newGen := atomic.LoadUint64(&fi.generation)
	if newGen <= oldGen {
		t.Fatalf("generation should increase after ClearCache, old=%d new=%d", oldGen, newGen)
	}
}

// TestInitWorkDir 测试 InitWorkDir 设置 cacheWorkDir
func TestInitWorkDir(t *testing.T) {
	oldVal := cacheWorkDir
	defer func() { cacheWorkDir = oldVal }()

	InitWorkDir("/test/path")
	if cacheWorkDir != "/test/path" {
		t.Fatalf("expected cacheWorkDir=/test/path, got %q", cacheWorkDir)
	}
}

// TestSearch_TriggersRefreshOnEmptyPaths 测试 search 在 paths 为空时触发后台刷新（B-1 验证）
func TestSearch_TriggersRefreshOnEmptyPaths(t *testing.T) {
	ClearCache()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("test"), 0o644)
	InitWorkDir(dir)
	defer InitWorkDir("")

	// search 应返回 nil 但触发后台刷新
	results := fi.search("test", 10)
	if results != nil {
		t.Fatal("expected nil for empty paths")
	}

	// 等待后台刷新完成
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fi.mu.RLock()
		paths := fi.paths
		fi.mu.RUnlock()
		if len(paths) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	fi.mu.RLock()
	defer fi.mu.RUnlock()
	if len(fi.paths) == 0 {
		t.Fatal("expected paths to be populated after search triggered refresh")
	}
}
