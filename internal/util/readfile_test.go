package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFileSafe_UTF8 正常 UTF-8 文件读取
func TestReadFileSafe_UTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf8.txt")
	content := "hello, 世界"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Content != content {
		t.Errorf("Content = %q, want %q", result.Content, content)
	}
	if result.Encoding != EncodingUTF8 {
		t.Errorf("Encoding = %q, want %q", result.Encoding, EncodingUTF8)
	}
	if result.Skipped {
		t.Error("Skipped = true, want false")
	}
}

// TestReadFileSafe_UTF8BOM UTF-8 BOM 文件，BOM 被剥
func TestReadFileSafe_UTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf8bom.txt")
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("Content = %q, want %q（BOM 应被剥）", result.Content, "hello")
	}
	if result.Encoding != EncodingUTF8BOM {
		t.Errorf("Encoding = %q, want %q", result.Encoding, EncodingUTF8BOM)
	}
}

// TestReadFileSafe_UTF16LE UTF-16 LE 文件转码为 UTF-8
func TestReadFileSafe_UTF16LE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf16le.txt")
	data := []byte{0xFF, 0xFE, 'h', 0x00, 'i', 0x00}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Content != "hi" {
		t.Errorf("Content = %q, want %q", result.Content, "hi")
	}
	if result.Encoding != EncodingUTF16LE {
		t.Errorf("Encoding = %q, want %q", result.Encoding, EncodingUTF16LE)
	}
}

// TestReadFileSafe_GBK GBK 文件转码为 UTF-8
func TestReadFileSafe_GBK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gbk.txt")
	// "你好" GBK，重复一次凑足 8 字节确保检测命中
	data := []byte{0xC4, 0xE3, 0xBA, 0xC3, 0xC4, 0xE3, 0xBA, 0xC3}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Content != "你好你好" {
		t.Errorf("Content = %q, want %q", result.Content, "你好你好")
	}
	if result.Encoding != EncodingGBK {
		t.Errorf("Encoding = %q, want %q", result.Encoding, EncodingGBK)
	}
}

// TestReadFileSafe_BinarySkipped 二进制文件返回占位符
func TestReadFileSafe_BinarySkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.png")
	data := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !result.Skipped {
		t.Error("Skipped = false, want true（二进制应跳过）")
	}
	if !strings.Contains(result.Content, "binary") {
		t.Errorf("Content = %q, want contains 'binary'", result.Content)
	}
}

// TestReadFileSafe_TooLarge 文件超过 maxBytes 返回 Skipped=true
func TestReadFileSafe_TooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	data := []byte(strings.Repeat("a", 1000))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 100)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !result.Skipped {
		t.Error("Skipped = false, want true（过大应跳过）")
	}
	if !strings.Contains(result.Content, "too large") {
		t.Errorf("Content = %q, want contains 'too large'", result.Content)
	}
	if result.Bytes != 1000 {
		t.Errorf("Bytes = %d, want 1000（反映真实大小）", result.Bytes)
	}
}

// TestReadFileSafe_NotExist 文件不存在返回 error
func TestReadFileSafe_NotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent")
	_, err := ReadFileSafe(path, 0)
	if err == nil {
		t.Error("err = nil, want non-nil（文件不存在应报错）")
	}
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

// TestReadFileSafe_NoLimit maxBytes=0 表示不限制
func TestReadFileSafe_NoLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large_ok.txt")
	data := []byte(strings.Repeat("a", 10000))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Skipped {
		t.Error("Skipped = true, want false（maxBytes=0 不限制）")
	}
	if result.Content != string(data) {
		t.Errorf("Content 长度 = %d, want %d", len(result.Content), len(data))
	}
}

// TestReadFileSafe_BytesAccurate ReadFileResult.Bytes 反映真实大小
func TestReadFileSafe_BytesAccurate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sized.txt")
	data := []byte("hello") // 5 bytes
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ReadFileSafe(path, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Bytes != 5 {
		t.Errorf("Bytes = %d, want 5", result.Bytes)
	}
}
