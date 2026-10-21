// marketplace_test.go — M25-03 skill 市场远程仓库拉取测试。
//
// 覆盖 RemoteSource/Marketplace 的注册、拉取、解压、缓存、降级策略。
// 注：httptest 相关测试在 Windows Winsock 损坏时可能失败，WSL 环境正常。

package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewMarketplace_NilManager 验证 nil manager 返回 nil。
func TestNewMarketplace_NilManager(t *testing.T) {
	m := NewMarketplace(nil, "/tmp/cache")
	if m != nil {
		t.Errorf("NewMarketplace(nil, _) = %v, want nil", m)
	}
}

// TestNewMarketplace_EmptyCacheDir 验证空 cacheDir 返回 nil。
func TestNewMarketplace_EmptyCacheDir(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, "")
	if m != nil {
		t.Errorf("NewMarketplace(_, empty) = %v, want nil", m)
	}
}

// TestNewMarketplace_Valid 验证有效参数返回非 nil。
func TestNewMarketplace_Valid(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	if m == nil {
		t.Fatal("NewMarketplace(valid) returned nil")
	}
}

// TestAddSource_EmptyName 验证空 Name 返回错误。
func TestAddSource_EmptyName(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	err := m.AddSource(RemoteSource{Name: "", URL: "https://example.com/skills.zip"})
	if err == nil {
		t.Error("AddSource with empty Name should return error")
	}
}

// TestAddSource_EmptyURL 验证空 URL 返回错误。
func TestAddSource_EmptyURL(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	err := m.AddSource(RemoteSource{Name: "test", URL: ""})
	if err == nil {
		t.Error("AddSource with empty URL should return error")
	}
}

// TestAddSource_InvalidURL 验证非 http/https URL 返回错误。
func TestAddSource_InvalidURL(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	err := m.AddSource(RemoteSource{Name: "test", URL: "ftp://example.com/skills.zip"})
	if err == nil {
		t.Error("AddSource with ftp:// URL should return error")
	}
}

// TestAddSource_Valid 验证有效 source 注册成功。
func TestAddSource_Valid(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	err := m.AddSource(RemoteSource{Name: "test", URL: "https://example.com/skills.zip"})
	if err != nil {
		t.Errorf("AddSource valid returned error: %v", err)
	}
	sources := m.ListSources()
	if len(sources) != 1 {
		t.Errorf("ListSources() len = %d, want 1", len(sources))
	}
	if sources[0].Name != "test" {
		t.Errorf("sources[0].Name = %q, want %q", sources[0].Name, "test")
	}
}

// TestAddSource_DuplicateOverwrite 验证同名 source 覆盖旧配置。
func TestAddSource_DuplicateOverwrite(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	_ = m.AddSource(RemoteSource{Name: "test", URL: "https://old.example.com/skills.zip"})
	_ = m.AddSource(RemoteSource{Name: "test", URL: "https://new.example.com/skills.zip"})

	sources := m.ListSources()
	if len(sources) != 1 {
		t.Errorf("ListSources() len after overwrite = %d, want 1", len(sources))
	}
	if sources[0].URL != "https://new.example.com/skills.zip" {
		t.Errorf("sources[0].URL = %q, want new URL", sources[0].URL)
	}
}

// TestRemoveSource 验证移除 source。
func TestRemoveSource(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	_ = m.AddSource(RemoteSource{Name: "test1", URL: "https://example.com/1.zip"})
	_ = m.AddSource(RemoteSource{Name: "test2", URL: "https://example.com/2.zip"})

	m.RemoveSource("test1")
	sources := m.ListSources()
	if len(sources) != 1 {
		t.Errorf("ListSources() len after remove = %d, want 1", len(sources))
	}
	if sources[0].Name != "test2" {
		t.Errorf("sources[0].Name = %q, want %q", sources[0].Name, "test2")
	}
}

// TestRemoveSource_NotExist 验证移除不存在的 source 是 no-op。
func TestRemoveSource_NotExist(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	_ = m.AddSource(RemoteSource{Name: "test1", URL: "https://example.com/1.zip"})

	m.RemoveSource("nonexistent")
	sources := m.ListSources()
	if len(sources) != 1 {
		t.Errorf("ListSources() len after remove nonexistent = %d, want 1", len(sources))
	}
}

// TestFetchAll_NoSources 验证无 source 时 FetchAll 返回 nil。
func TestFetchAll_NoSources(t *testing.T) {
	mgr := NewManager()
	m := NewMarketplace(mgr, os.TempDir())
	err := m.FetchAll(context.Background(), false)
	if err != nil {
		t.Errorf("FetchAll with no sources returned error: %v", err)
	}
}

// TestFetchOne_DownloadFailure 验证下载失败返回错误（无缓存时）。
func TestFetchOne_DownloadFailure(t *testing.T) {
	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)
	_ = m.AddSource(RemoteSource{
		Name: "test",
		URL:  "http://127.0.0.1:1/nonexistent.zip", // 必然失败的端口
	})

	err := m.FetchOne(context.Background(), RemoteSource{
		Name: "test",
		URL:  "http://127.0.0.1:1/nonexistent.zip",
	}, true)
	if err == nil {
		t.Error("FetchOne with unreachable URL should return error")
	}
}

// TestFetchOne_SuccessfulDownloadAndExtract 验证成功下载+解压+加载。
// 注：此测试依赖 httptest，Windows Winsock 损坏时可能失败，WSL 正常。
func TestFetchOne_SuccessfulDownloadAndExtract(t *testing.T) {
	// 创建测试 zip 内容
	zipContent := createTestZip(t, map[string]string{
		"skill1.md": "---\nname: skill1\ndescription: test skill 1\n---\nThis is skill 1 prompt.",
		"skill2.md": "---\nname: skill2\ndescription: test skill 2\n---\nThis is skill 2 prompt.",
	})

	// 创建 mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipContent)
	}))
	defer server.Close()

	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)
	_ = m.AddSource(RemoteSource{Name: "test", URL: server.URL})

	err := m.FetchOne(context.Background(), RemoteSource{Name: "test", URL: server.URL}, true)
	if err != nil {
		t.Fatalf("FetchOne failed: %v", err)
	}

	// 验证 skill 被加载
	s1, ok := mgr.Get("skill1")
	if !ok {
		t.Error("skill1 not loaded after fetch")
	} else if s1.Description != "test skill 1" {
		t.Errorf("skill1 description = %q, want %q", s1.Description, "test skill 1")
	}

	s2, ok := mgr.Get("skill2")
	if !ok {
		t.Error("skill2 not loaded after fetch")
	} else if s2.Description != "test skill 2" {
		t.Errorf("skill2 description = %q, want %q", s2.Description, "test skill 2")
	}

	// 验证来源标记为 plugin
	if s1.Source != "plugin" {
		t.Errorf("skill1 Source = %q, want %q", s1.Source, "plugin")
	}
}

// TestFetchOne_CacheReuse 验证缓存复用（第二次不下载）。
func TestFetchOne_CacheReuse(t *testing.T) {
	zipContent := createTestZip(t, map[string]string{
		"cached.md": "---\nname: cached\ndescription: cached skill\n---\nCached prompt.",
	})

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipContent)
	}))
	defer server.Close()

	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)

	// 第一次拉取（forceRefresh=true）
	err := m.FetchOne(context.Background(), RemoteSource{Name: "test", URL: server.URL}, true)
	if err != nil {
		t.Fatalf("First FetchOne failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("HTTP call count after first fetch = %d, want 1", callCount)
	}

	// 第二次拉取（forceRefresh=false，应使用缓存）
	err = m.FetchOne(context.Background(), RemoteSource{Name: "test", URL: server.URL}, false)
	if err != nil {
		t.Fatalf("Second FetchOne (cache) failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("HTTP call count after cached fetch = %d, want 1 (should use cache)", callCount)
	}
}

// TestFetchAll_PartialFailure 验证部分源失败不阻塞其他源（致命反例 1）。
func TestFetchAll_PartialFailure(t *testing.T) {
	zipContent := createTestZip(t, map[string]string{
		"good.md": "---\nname: good\ndescription: good skill\n---\nGood prompt.",
	})

	goodServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipContent)
	}))
	defer goodServer.Close()

	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)
	_ = m.AddSource(RemoteSource{Name: "good", URL: goodServer.URL})
	_ = m.AddSource(RemoteSource{Name: "bad", URL: "http://127.0.0.1:1/bad.zip"})

	err := m.FetchAll(context.Background(), true)
	if err == nil {
		t.Error("FetchAll with one bad source should return error")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should mention failed source 'bad', got: %v", err)
	}

	// 验证 good source 仍被加载
	if _, ok := mgr.Get("good"); !ok {
		t.Error("good skill should be loaded despite bad source failure")
	}
}

// TestClearCache 验证清除指定源缓存。
func TestClearCache(t *testing.T) {
	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)

	// 创建缓存目录
	targetDir := filepath.Join(cacheDir, "test")
	_ = os.MkdirAll(targetDir, 0o755)
	_ = os.WriteFile(filepath.Join(targetDir, "test.md"), []byte("test"), 0o644)

	err := m.ClearCache("test")
	if err != nil {
		t.Errorf("ClearCache failed: %v", err)
	}
	if dirExists(targetDir) {
		t.Error("cache dir should not exist after ClearCache")
	}
}

// TestClearCache_NonExist 验证清除不存在的缓存是 no-op。
func TestClearCache_NonExist(t *testing.T) {
	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)

	err := m.ClearCache("nonexistent")
	if err != nil {
		t.Errorf("ClearCache nonexistent returned error: %v", err)
	}
}

// TestClearCache_EmptyName 验证空名称返回错误。
func TestClearCache_EmptyName(t *testing.T) {
	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)

	err := m.ClearCache("")
	if err == nil {
		t.Error("ClearCache with empty name should return error")
	}
}

// TestClearAllCache 验证清除所有缓存。
func TestClearAllCache(t *testing.T) {
	mgr := NewManager()
	cacheDir := t.TempDir()
	m := NewMarketplace(mgr, cacheDir)

	_ = os.MkdirAll(filepath.Join(cacheDir, "source1"), 0o755)
	_ = os.MkdirAll(filepath.Join(cacheDir, "source2"), 0o755)

	err := m.ClearAllCache()
	if err != nil {
		t.Errorf("ClearAllCache failed: %v", err)
	}
	if dirExists(cacheDir) {
		t.Error("cache dir should not exist after ClearAllCache")
	}
}

// TestListRemote 验证列出远程 skill。
func TestListRemote(t *testing.T) {
	zipContent := createTestZip(t, map[string]string{
		"remote1.md":    "---\nname: remote1\n---\nPrompt 1.",
		"remote2.skill": "---\nname: remote2\n---\nPrompt 2.",
		"readme.txt":    "not a skill",
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipContent)
	}))
	defer server.Close()

	mgr := NewManager()
	m := NewMarketplace(mgr, t.TempDir())

	infos, err := m.ListRemote(context.Background(), RemoteSource{Name: "test", URL: server.URL})
	if err != nil {
		t.Fatalf("ListRemote failed: %v", err)
	}

	// 应只包含 .md 和 .skill 文件（2 个，不含 readme.txt）
	if len(infos) != 2 {
		t.Errorf("ListRemote returned %d infos, want 2", len(infos))
	}

	names := map[string]bool{}
	for _, info := range infos {
		names[info.Name] = true
		if info.Source != "test" {
			t.Errorf("info.Source = %q, want %q", info.Source, "test")
		}
	}
	if !names["remote1"] {
		t.Error("remote1 not in list")
	}
	if !names["remote2"] {
		t.Error("remote2 not in list")
	}
}

// TestFetchOne_AuthToken 验证 AuthToken 添加 Authorization 头。
func TestFetchOne_AuthToken(t *testing.T) {
	zipContent := createTestZip(t, map[string]string{
		"authed.md": "---\nname: authed\ndescription: authed skill\n---\nAuthed prompt.",
	})

	var receivedAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipContent)
	}))
	defer server.Close()

	mgr := NewManager()
	m := NewMarketplace(mgr, t.TempDir())

	err := m.FetchOne(context.Background(), RemoteSource{
		Name:      "test",
		URL:       server.URL,
		AuthToken: "my-secret-token",
	}, true)
	if err != nil {
		t.Fatalf("FetchOne with auth token failed: %v", err)
	}

	if receivedAuthHeader != "Bearer my-secret-token" {
		t.Errorf("Authorization header = %q, want %q", receivedAuthHeader, "Bearer my-secret-token")
	}
}

// createTestZip 创建测试用 zip 文件内容（内存）。
//
// files: 文件名 → 内容映射，打包为 zip 返回字节切片。
func createTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	// 用 bytes.Buffer 创建内存 zip（二进制安全，不用 strings.Builder）
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Errorf("create zip entry %s: %v", name, err)
			return nil
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Errorf("write zip entry %s: %v", name, err)
			return nil
		}
	}
	if err := zw.Close(); err != nil {
		t.Errorf("close zip writer: %v", err)
		return nil
	}
	return buf.Bytes()
}
