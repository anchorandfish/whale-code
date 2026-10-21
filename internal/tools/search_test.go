package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsBinaryFile 测试二进制文件检测
func TestIsBinaryFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name string
		ext  string
		want bool
	}{
		{"exe", ".exe", true},
		{"dll", ".dll", true},
		{"png", ".png", true},
		{"jpg", ".jpg", true},
		{"zip", ".zip", true},
		{"pdf", ".pdf", true},
		{"mp3", ".mp3", true},
		{"go", ".go", false},
		{"py", ".py", false},
		{"txt", ".txt", false},
		{"json", ".json", false},
		{"yaml", ".yaml", false},
		{"md", ".md", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(tmpDir, "test"+tt.ext)
			os.WriteFile(path, []byte("text content"), 0644)
			got := isBinaryFile(path)
			if got != tt.want {
				t.Errorf("isBinaryFile(%q) = %v, want %v", tt.ext, got, tt.want)
			}
		})
	}
}

// TestDoubleStarGlob 测试递归 glob 匹配
func TestDoubleStarGlob(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "src"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "src", "util.go"), []byte("package src"), 0644)

	t.Run("match go files with **", func(t *testing.T) {
		matches, err := globWithDoubleStar(tmpDir, "**/*.go", nil)
		if err != nil {
			t.Fatalf("globWithDoubleStar failed: %v", err)
		}
		if len(matches) < 3 {
			t.Errorf("expected at least 3 matches, got %d", len(matches))
		}
	})

	t.Run("match specific pattern", func(t *testing.T) {
		matches, err := globWithDoubleStar(tmpDir, "*.go", nil)
		if err != nil {
			t.Fatalf("globWithDoubleStar failed: %v", err)
		}
		if len(matches) < 1 {
			t.Errorf("expected at least 1 match, got %d", len(matches))
		}
	})
}

// TestSkipDirs 测试特定目录被跳过
func TestSkipDirs(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, ".git", "objects"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".git", "objects", "file.dat"), []byte("git"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg", "index.js"), []byte("js"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)

	matches, err := globWithDoubleStar(tmpDir, "**/*", nil)
	if err != nil {
		t.Fatalf("globWithDoubleStar failed: %v", err)
	}

	for _, m := range matches {
		rel, _ := filepath.Rel(tmpDir, m)
		if len(rel) > 4 && rel[:5] == ".git"+string(filepath.Separator) {
			t.Errorf("should skip .git directory, but found %s", rel)
		}
		if len(rel) > 13 && rel[:14] == "node_modules"+string(filepath.Separator) {
			t.Errorf("should skip node_modules directory, but found %s", rel)
		}
	}
}

// ============================================================================
// 新增搜索工具测试
// ============================================================================

// TestGlobHandler_Normal 测试 glob 搜索返回匹配的文件
func TestGlobHandler_Normal(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "c.go"), []byte("c"), 0644)

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "glob_search",
		Input: mustMarshal(map[string]string{"pattern": "*.txt", "path": tmpDir}),
	})

	if result.IsError {
		t.Fatalf("glob 搜索失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "a.txt") {
		t.Errorf("结果should contain a.txt，actual: %s", result.Output)
	}
	if !strings.Contains(result.Output, "b.txt") {
		t.Errorf("结果should contain b.txt，actual: %s", result.Output)
	}
	if strings.Contains(result.Output, "c.go") {
		t.Errorf("结果should not contain c.go，actual: %s", result.Output)
	}
}

// TestGlobHandler_WithPattern 测试在特定目录中使用模式搜索
func TestGlobHandler_WithPattern(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建子目录和文件
	subDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(subDir, "util.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("readme"), 0644)

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "glob_search",
		Input: mustMarshal(map[string]string{"pattern": "**/*.go", "path": tmpDir}),
	})

	if result.IsError {
		t.Fatalf("glob 搜索失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "main.go") {
		t.Errorf("结果should contain main.go，actual: %s", result.Output)
	}
	if !strings.Contains(result.Output, "util.go") {
		t.Errorf("结果should contain util.go，actual: %s", result.Output)
	}
}

// TestGrepHandler_Content 测试内容搜索返回匹配行
func TestGrepHandler_Content(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world\nfoo bar\nhello go"), 0644)

	result := e.Execute(context.Background(), Call{
		ID:   "1",
		Name: "grep_search",
		Input: mustMarshal(map[string]string{
			"pattern": "hello",
			"path":    tmpDir,
		}),
	})

	if result.IsError {
		t.Fatalf("grep 搜索失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("结果should contain 'hello'，actual: %s", result.Output)
	}
}

// TestGrepHandler_FilesWithMatches 测试 files_with_matches 模式
func TestGrepHandler_FilesWithMatches(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	os.WriteFile(filepath.Join(tmpDir, "match1.txt"), []byte("hello world"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "match2.txt"), []byte("hello go"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "nomatch.txt"), []byte("foo bar"), 0644)

	result := e.Execute(context.Background(), Call{
		ID:   "1",
		Name: "grep_search",
		Input: mustMarshal(map[string]string{
			"pattern":     "hello",
			"path":        tmpDir,
			"output_mode": "files_with_matches",
		}),
	})

	if result.IsError {
		t.Fatalf("grep files_with_matches 失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "match1.txt") {
		t.Errorf("结果should contain match1.txt，actual: %s", result.Output)
	}
	if !strings.Contains(result.Output, "match2.txt") {
		t.Errorf("结果should contain match2.txt，actual: %s", result.Output)
	}
	if strings.Contains(result.Output, "nomatch.txt") {
		t.Errorf("结果should not contain nomatch.txt，actual: %s", result.Output)
	}
}

// TestGrepHandler_Count 测试 count 模式
func TestGrepHandler_Count(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	os.WriteFile(filepath.Join(tmpDir, "count.txt"), []byte("hello\nhello\nworld"), 0644)

	result := e.Execute(context.Background(), Call{
		ID:   "1",
		Name: "grep_search",
		Input: mustMarshal(map[string]string{
			"pattern":     "hello",
			"path":        tmpDir,
			"output_mode": "count",
		}),
	})

	if result.IsError {
		t.Fatalf("grep count 失败: %s", result.Output)
	}
	// should contain计数信息
	if !strings.Contains(result.Output, "count.txt") {
		t.Errorf("count 模式结果should contain文件名，actual: %s", result.Output)
	}
}

// TestGrepHandler_CaseInsensitive 测试大小写不敏感搜索
func TestGrepHandler_CaseInsensitive(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	os.WriteFile(filepath.Join(tmpDir, "case.txt"), []byte("Hello World\nHELLO GO"), 0644)

	result := e.Execute(context.Background(), Call{
		ID:   "1",
		Name: "grep_search",
		Input: mustMarshal(map[string]interface{}{
			"pattern":          "hello",
			"path":             tmpDir,
			"case_insensitive": true,
		}),
	})

	if result.IsError {
		t.Fatalf("大小写不敏感搜索失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Hello") {
		t.Errorf("大小写不敏感搜索应匹配 'Hello'，actual: %s", result.Output)
	}
}

// TestGrepHandler_ContextLines test context行搜索
func TestGrepHandler_ContextLines(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	content := "line 1\nline 2\nTARGET LINE\nline 4\nline 5"
	os.WriteFile(filepath.Join(tmpDir, "context.txt"), []byte(content), 0644)

	result := e.Execute(context.Background(), Call{
		ID:   "1",
		Name: "grep_search",
		Input: mustMarshal(map[string]interface{}{
			"pattern":       "TARGET",
			"path":          tmpDir,
			"context_lines": 1,
		}),
	})

	if result.IsError {
		t.Fatalf("上下文行搜索失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "TARGET") {
		t.Errorf("结果should contain 'TARGET'，actual: %s", result.Output)
	}
}
