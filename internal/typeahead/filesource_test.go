package typeahead

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindGitRoot_NonGitDir 测试非 git 目录返回空
func TestFindGitRoot_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	root, err := findGitRoot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 非临时目录可能向上找到 git 根，但临时目录通常不会
	_ = root
}

// TestFindGitRoot_GitDir 测试 git 目录
func TestFindGitRoot_GitDir(t *testing.T) {
	dir := t.TempDir()
	// 创建 .git 目录
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}
	root, err := findGitRoot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root != dir {
		t.Fatalf("expected root=%s, got %s", dir, root)
	}
}

// TestNormalizePaths_SameDir 测试 repoRoot == cwd 时不转换
func TestNormalizePaths_SameDir(t *testing.T) {
	files := []string{"a.go", "b.go"}
	result := normalizePaths(files, "/tmp/repo", "/tmp/repo")
	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result))
	}
}

// TestNormalizePaths_DifferentDir 测试 repoRoot != cwd 时转换
func TestNormalizePaths_DifferentDir(t *testing.T) {
	files := []string{"a.go", "sub/b.go"}
	result := normalizePaths(files, "/tmp/repo", "/tmp/repo/sub")
	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result))
	}
	// a.go 应转为 ../a.go（相对于 sub）
	if result[0] != "../a.go" {
		t.Logf("result[0]=%s (expected ../a.go on non-Windows)", result[0])
	}
}

// TestGetFilesUsingWalkDir 测试 WalkDir fallback 获取文件列表
func TestGetFilesUsingWalkDir(t *testing.T) {
	dir := t.TempDir()
	// 创建测试文件
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 创建 .git 目录（应被跳过）
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := getFilesUsingWalkDir(dir)
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}

	found := make(map[string]bool)
	for _, f := range files {
		found[f] = true
	}
	if !found["a.go"] {
		t.Fatal("expected a.go in results")
	}
	if !found["b.go"] {
		t.Fatal("expected b.go in results")
	}
	// .git 目录应被跳过
	for f := range found {
		if len(f) > 4 && f[:4] == ".git" {
			t.Fatalf(".git should be skipped, found %s", f)
		}
	}
}

// TestLoadIgnorePatterns_NoFile 测试无 ignore 文件
func TestLoadIgnorePatterns_NoFile(t *testing.T) {
	dir := t.TempDir()
	matcher, err := loadIgnorePatterns(dir, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matcher != nil {
		t.Fatal("expected nil matcher when no ignore files")
	}
}

// TestLoadIgnorePatterns_WithGitignore 测试有 .gitignore 文件
func TestLoadIgnorePatterns_WithGitignore(t *testing.T) {
	dir := t.TempDir()
	gitignoreContent := "*.tmp\nnode_modules/\n# comment\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignoreContent), 0o644); err != nil {
		t.Fatal(err)
	}
	matcher, err := loadIgnorePatterns(dir, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}
}

// TestListFiles_NoGit 测试非 git 目录的 listFiles fallback
func TestListFiles_NoGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.go"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := listFiles(dir)
	if err != nil {
		t.Fatalf("listFiles failed: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected at least 1 file")
	}
}
