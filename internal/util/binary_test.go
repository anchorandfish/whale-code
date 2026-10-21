package util

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsBinary_ByExtension 扩展名命中 BinaryExts 返回 true
func TestIsBinary_ByExtension(t *testing.T) {
	exts := []string{".exe", ".png", ".zip", ".pdf", ".mp4", ".wasm"}
	for _, ext := range exts {
		path := "test" + ext
		if !IsBinaryExt(path) {
			t.Errorf("IsBinaryExt(%q) = false, want true", path)
		}
		// 创建临时文件验证 IsBinary
		tmpPath := filepath.Join(t.TempDir(), "test"+ext)
		if err := os.WriteFile(tmpPath, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if !IsBinary(tmpPath) {
			t.Errorf("IsBinary(%q) = false, want true（扩展名命中）", tmpPath)
		}
	}
}

// TestIsBinary_ExtCaseInsensitive 扩展名大小写不敏感
func TestIsBinary_ExtCaseInsensitive(t *testing.T) {
	exts := []string{".EXE", ".PNG", ".ZIP", ".PDF"}
	for _, ext := range exts {
		if !IsBinaryExt("test" + ext) {
			t.Errorf("IsBinaryExt(%q) = false, want true（大小写不敏感）", "test"+ext)
		}
	}
}

// TestIsBinary_NoExtension 无扩展名返回 false（除非内容含 NUL）
func TestIsBinary_NoExtension(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "noext")
	if err := os.WriteFile(tmpPath, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	if IsBinary(tmpPath) {
		t.Errorf("IsBinary(noext text) = true, want false")
	}
}

// TestIsBinary_ByContent 内容含 NUL 字节返回 true
func TestIsBinary_ByContent(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "content_with_nul")
	data := []byte{'h', 'e', 'l', 'l', 'o', 0, 'w', 'o', 'r', 'l', 'd'}
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if !IsBinary(tmpPath) {
		t.Errorf("IsBinary(含 NUL 内容) = false, want true")
	}
}

// TestIsBinary_EmptyFile 空文件返回 false
func TestIsBinary_EmptyFile(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(tmpPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if IsBinary(tmpPath) {
		t.Errorf("IsBinary(空文件) = true, want false")
	}
}

// TestIsBinary_OpenFailure 打开失败返回 false（不报错）
func TestIsBinary_OpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent")
	if IsBinary(path) {
		t.Errorf("IsBinary(不存在) = true, want false")
	}
}
