package typeahead

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindSimilarFile_SameNameDifferentExt 测试同名不同扩展名
func TestFindSimilarFile_SameNameDifferentExt(t *testing.T) {
	dir := t.TempDir()
	// 创建 foo.tsx 和 foo.ts
	if err := os.WriteFile(filepath.Join(dir, "foo.tsx"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo.ts"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 查找 foo.ts → 应返回 foo.tsx
	result := FindSimilarFile("foo.ts", dir)
	if result != "foo.tsx" {
		t.Fatalf("expected foo.tsx, got %q", result)
	}
}

// TestFindSimilarFile_Levenshtein 测试 levenshtein 兜底
func TestFindSimilarFile_Levenshtein(t *testing.T) {
	dir := t.TempDir()
	// 创建 foo.go，查找 fop.go
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := FindSimilarFile("fop.go", dir)
	if result != "foo.go" {
		t.Fatalf("expected foo.go (levenshtein=1), got %q", result)
	}
}

// TestFindSimilarFile_NoMatch 测试无相似文件返回空
func TestFindSimilarFile_NoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "completely_different.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := FindSimilarFile("foo.ts", dir)
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

// TestFindSimilarFile_EmptyDir 测试空目录返回空
func TestFindSimilarFile_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	result := FindSimilarFile("foo.ts", dir)
	if result != "" {
		t.Fatalf("expected empty result for empty dir, got %q", result)
	}
}

// TestFindSimilarFile_SameNamePriority 测试同名优先于 levenshtein
func TestFindSimilarFile_SameNamePriority(t *testing.T) {
	dir := t.TempDir()
	// 创建 foo.tsx 和 fop.ts
	if err := os.WriteFile(filepath.Join(dir, "foo.tsx"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fop.ts"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 查找 foo.ts → 同名优先（foo.tsx），不返回 fop.ts（levenshtein=1）
	result := FindSimilarFile("foo.ts", dir)
	if result != "foo.tsx" {
		t.Fatalf("expected foo.tsx (same name priority), got %q", result)
	}
}

// TestFindSimilarFile_DirectoryPath 测试带目录路径的文件
func TestFindSimilarFile_DirectoryPath(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "config.yaml"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 查找 sub/config.yml → 应返回 config.yaml
	result := FindSimilarFile(filepath.Join("sub", "config.yml"), dir)
	if result != "config.yaml" {
		t.Fatalf("expected config.yaml, got %q", result)
	}
}

// TestLevenshtein_Basic 测试基本 levenshtein 距离
func TestLevenshtein_Basic(t *testing.T) {
	tests := []struct {
		a, b     string
		expected int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"fop", "foo", 1},
		{"", "abc", 3},
	}

	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.expected)
		}
	}
}

// TestLevenshtein_Symmetric 测试 levenshtein 对称性
func TestLevenshtein_Symmetric(t *testing.T) {
	pairs := [][2]string{
		{"abc", "def"},
		{"hello", "world"},
		{"foo.ts", "foo.tsx"},
	}
	for _, p := range pairs {
		d1 := levenshtein(p[0], p[1])
		d2 := levenshtein(p[1], p[0])
		if d1 != d2 {
			t.Errorf("levenshtein not symmetric: %q→%q=%d, %q→%q=%d", p[0], p[1], d1, p[1], p[0], d2)
		}
	}
}

// TestMin3 测试 min3 辅助函数
func TestMin3(t *testing.T) {
	if min3(3, 2, 1) != 1 {
		t.Fatal("min3(3,2,1) should be 1")
	}
	if min3(1, 2, 3) != 1 {
		t.Fatal("min3(1,2,3) should be 1")
	}
	if min3(2, 1, 3) != 1 {
		t.Fatal("min3(2,1,3) should be 1")
	}
	if min3(0, 0, 0) != 0 {
		t.Fatal("min3(0,0,0) should be 0")
	}
}
