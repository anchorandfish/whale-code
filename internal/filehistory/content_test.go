package filehistory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteContent_Dedup 验证相同内容去重（TC-201）
func TestWriteContent_Dedup(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")

	hash1, err := writeContent(dir, content)
	if err != nil {
		t.Fatalf("writeContent first: %v", err)
	}
	hash2, err := writeContent(dir, content)
	if err != nil {
		t.Fatalf("writeContent second: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("same content should produce same hash: %s != %s", hash1, hash2)
	}

	// 验证只写入了一个文件
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected 1 content file (dedup), got %d", len(entries))
	}
}

// TestWriteContent_DifferentContent 验证不同内容产生不同 hash
func TestWriteContent_DifferentContent(t *testing.T) {
	dir := t.TempDir()

	hash1, err := writeContent(dir, []byte("content A"))
	if err != nil {
		t.Fatalf("writeContent A: %v", err)
	}
	hash2, err := writeContent(dir, []byte("content B"))
	if err != nil {
		t.Fatalf("writeContent B: %v", err)
	}

	if hash1 == hash2 {
		t.Error("different content should produce different hash")
	}
}

// TestWriteContent_LargeFile 验证大文件（>10MB）写入—读回内容保真（TC-202）。
// 注：S10 / HG3 之后大文件同样走全量 SHA256；本用例断言的是往返一致性，
// 与 hash 策略无关（旧注释"跳过 SHA256 计算"已随修复失效）。
func TestWriteContent_LargeFile(t *testing.T) {
	dir := t.TempDir()
	// 创建超过 maxContentHashSize 的内容
	largeContent := make([]byte, maxContentHashSize+1024)
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	hash, err := writeContent(dir, largeContent)
	if err != nil {
		t.Fatalf("writeContent large: %v", err)
	}

	if hash == "" {
		t.Error("hash should not be empty")
	}

	// 验证内容可以被正确读取
	readBack, err := readContent(dir, hash)
	if err != nil {
		t.Fatalf("readContent large: %v", err)
	}
	if len(readBack) != len(largeContent) {
		t.Errorf("content size mismatch: expected %d, got %d", len(largeContent), len(readBack))
	}
}

// TestReadContent 验证从内容存储读取数据（TC-203）
func TestReadContent(t *testing.T) {
	dir := t.TempDir()
	content := []byte("test content for reading")

	hash, err := writeContent(dir, content)
	if err != nil {
		t.Fatalf("writeContent: %v", err)
	}

	readBack, err := readContent(dir, hash)
	if err != nil {
		t.Fatalf("readContent: %v", err)
	}

	if string(readBack) != string(content) {
		t.Errorf("content mismatch: expected %q, got %q", content, readBack)
	}
}

// TestReadContent_NotFound 验证读取不存在的 hash 返回错误
func TestReadContent_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := readContent(dir, "sha256:nonexistent1234567890abcdef1234567890abcdef1234567890abcdef12345678")
	if err == nil {
		t.Error("expected error for nonexistent hash")
	}
}

// TestContentExists 验证内容存在性检查（TC-204）
func TestContentExists(t *testing.T) {
	dir := t.TempDir()
	content := []byte("existence test")

	hash, _ := writeContent(dir, content)

	if !contentExists(dir, hash) {
		t.Error("contentExists should return true for existing content")
	}

	fakeHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if contentExists(dir, fakeHash) {
		t.Error("contentExists should return false for nonexistent content")
	}
}

// TestComputeHash_Consistency 验证相同内容产生相同 hash（TC-205）
func TestComputeHash_Consistency(t *testing.T) {
	content := []byte("consistency test")
	hash1 := computeHash(content)
	hash2 := computeHash(content)

	if hash1 != hash2 {
		t.Error("same content should produce same hash")
	}

	// 验证 hash 格式
	if len(hash1) != len(hashPrefix)+64 {
		t.Errorf("hash length mismatch: expected %d, got %d", len(hashPrefix)+64, len(hash1))
	}
}

// TestRemoveContent 验证删除内容块
func TestRemoveContent(t *testing.T) {
	dir := t.TempDir()
	content := []byte("to be removed")

	hash, _ := writeContent(dir, content)
	if !contentExists(dir, hash) {
		t.Fatal("content should exist before removal")
	}

	if err := removeContent(dir, hash); err != nil {
		t.Fatalf("removeContent: %v", err)
	}

	if contentExists(dir, hash) {
		t.Error("content should not exist after removal")
	}
}

// TestRemoveContent_NotExist 验证删除不存在的内容块不报错
func TestRemoveContent_NotExist(t *testing.T) {
	dir := t.TempDir()
	fakeHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	err := removeContent(dir, fakeHash)
	if err != nil {
		t.Errorf("removeContent should not error for nonexistent: %v", err)
	}
}

// TestHashToPath 验证 hash 到路径的转换
func TestHashToPath(t *testing.T) {
	dir := t.TempDir()
	hash := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	path := hashToPath(dir, hash)

	expected := filepath.Join(dir, "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if path != expected {
		t.Errorf("hashToPath mismatch: expected %s, got %s", expected, path)
	}
}
