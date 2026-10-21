package platform

import (
	"runtime"
	"strings"
	"testing"
)

// TestFilePathToURI_WindowsDrive 验证 Windows 盘符转换
func TestFilePathToURI_WindowsDrive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过 Windows 特定测试")
	}

	uri := FilePathToURI(`C:\Users\foo\bar.go`)
	expected := "file:///C:/Users/foo/bar.go"
	if uri != expected {
		t.Errorf("FilePathToURI = %q，期望 %q", uri, expected)
	}
}

// TestFilePathToURI_UnixPath 验证 Unix 路径转换
func TestFilePathToURI_UnixPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("跳过 Unix 特定测试")
	}

	uri := FilePathToURI("/home/user/foo.go")
	expected := "file:///home/user/foo.go"
	if uri != expected {
		t.Errorf("FilePathToURI = %q，期望 %q", uri, expected)
	}
}

// TestFilePathToURI_ChinesePath 反例：中文路径正确 URL 编码
func TestFilePathToURI_ChinesePath(t *testing.T) {
	var input, expectedPrefix string
	if runtime.GOOS == "windows" {
		input = `C:\中文\测试.go`
		expectedPrefix = "file:///C:/%E4%B8%AD%E6%96%87/"
	} else {
		input = "/home/中文/测试.go"
		expectedPrefix = "file:///home/%E4%B8%AD%E6%96%87/"
	}

	uri := FilePathToURI(input)
	if !strings.HasPrefix(uri, expectedPrefix) {
		t.Errorf("中文路径 URI 应以 %q 开头，实际: %s", expectedPrefix, uri)
	}
	// 验证中文被编码（不应直接出现中文字符）
	if strings.Contains(uri, "中文") {
		t.Error("中文应被 URL 编码，不应直接出现在 URI 中")
	}
}

// TestFilePathToURI_UNCPath 验证 UNC 路径转换
func TestFilePathToURI_UNCPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过 Windows 特定测试（UNC 路径）")
	}
	uri := FilePathToURI(`\\server\share\path`)
	// UNC 路径：\\server\share → file://server/share
	if !strings.HasPrefix(uri, "file://server/") {
		t.Errorf("UNC 路径 URI 应以 file://server/ 开头，实际: %s", uri)
	}
}

// TestURIToFilePath_WindowsDrive 验证 Windows 盘符反向转换
func TestURIToFilePath_WindowsDrive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跳过 Windows 特定测试")
	}

	path, err := URIToFilePath("file:///C:/Users/foo/bar.go")
	if err != nil {
		t.Fatalf("URIToFilePath 失败: %v", err)
	}
	expected := `C:\Users\foo\bar.go`
	if path != expected {
		t.Errorf("URIToFilePath = %q，期望 %q", path, expected)
	}
}

// TestURIToFilePath_ChinesePath 反例：中文路径正确 URL 解码
func TestURIToFilePath_ChinesePath(t *testing.T) {
	var uri, expected string
	if runtime.GOOS == "windows" {
		uri = "file:///C:/%E4%B8%AD%E6%96%87/%E6%B5%8B%E8%AF%95.go"
		expected = `C:\中文\测试.go`
	} else {
		uri = "file:///home/%E4%B8%AD%E6%96%87/%E6%B5%8B%E8%AF%95.go"
		expected = "/home/中文/测试.go"
	}

	path, err := URIToFilePath(uri)
	if err != nil {
		t.Fatalf("URIToFilePath 失败: %v", err)
	}
	if path != expected {
		t.Errorf("URIToFilePath = %q，期望 %q", path, expected)
	}
}

// TestURIToFilePath_MalformedURI 反例：畸形 URI 返回 error 不 panic
func TestURIToFilePath_MalformedURI(t *testing.T) {
	// 不是 file:// 前缀
	_, err := URIToFilePath("http://example.com/path")
	if err == nil {
		t.Error("非 file:// 前缀的 URI 应返回 error")
	}

	// 空字符串
	_, err = URIToFilePath("")
	if err == nil {
		t.Error("空字符串应返回 error")
	}

	// 畸形 URL 编码（%XX 无效）
	_, err = URIToFilePath("file:///path%ZZ")
	if err == nil {
		t.Error("畸形 URL 编码应返回 error")
	}
}

// TestURIToFilePath_UNCPath 验证 UNC 路径反向转换
func TestURIToFilePath_UNCPath(t *testing.T) {
	path, err := URIToFilePath("file://server/share/path")
	if err != nil {
		t.Fatalf("URIToFilePath 失败: %v", err)
	}
	if runtime.GOOS == "windows" {
		expected := `\\server\share\path`
		if path != expected {
			t.Errorf("URIToFilePath = %q，期望 %q", path, expected)
		}
	}
	// 非 Windows 平台至少应包含 server/share
	if !strings.Contains(path, "server") || !strings.Contains(path, "share") {
		t.Errorf("UNC 路径应包含 server/share，实际: %s", path)
	}
}

// TestURI_RoundTrip 验证 FilePathToURI → URIToFilePath 往返一致
func TestURI_RoundTrip(t *testing.T) {
	var original string
	if runtime.GOOS == "windows" {
		original = `C:\Users\test user\file name.go`
	} else {
		original = "/home/test user/file name.go"
	}

	uri := FilePathToURI(original)
	back, err := URIToFilePath(uri)
	if err != nil {
		t.Fatalf("往返转换失败: %v", err)
	}
	if back != original {
		t.Errorf("往返不一致：原始 %q → URI %q → 返回 %q", original, uri, back)
	}
}
