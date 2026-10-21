package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// helper functions
// ============================================================================

// setupTestProject 创建标准测试项目，包含 .gitignore、源文件、应忽略文件和二进制文件
func setupTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// .gitignore
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\ndist/\n*.log\n"), 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}

	// 源文件
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatalf("创建 src 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package src\nfunc App() {}"), 0644); err != nil {
		t.Fatalf("创建 src/app.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "utils.go"), []byte("package src\nfunc Utils() {}"), 0644); err != nil {
		t.Fatalf("创建 src/utils.go 失败: %v", err)
	}

	// 应被忽略的文件
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0755); err != nil {
		t.Fatalf("创建 vendor 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "dep.go"), []byte("package vendor"), 0644); err != nil {
		t.Fatalf("创建 vendor/dep.go 失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0755); err != nil {
		t.Fatalf("创建 dist 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "bundle.js"), []byte("bundle"), 0644); err != nil {
		t.Fatalf("创建 dist/bundle.js 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("log content"), 0644); err != nil {
		t.Fatalf("创建 debug.log 失败: %v", err)
	}

	// 二进制文件
	binaryContent := append([]byte("package"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(filepath.Join(dir, "binary_data"), binaryContent, 0644); err != nil {
		t.Fatalf("创建 binary_data 失败: %v", err)
	}

	return dir
}

// containsPath 检查 GrepFileMatch 列表中是否包含指定名称的路径
func containsPath(files []GrepFileMatch, name string) bool {
	for _, f := range files {
		if strings.Contains(f.Path, name) {
			return true
		}
	}
	return false
}

// totalMatches 统计 GrepOutput 中的总匹配行数
func totalMatches(output *GrepOutput) int {
	total := 0
	for _, f := range output.Files {
		total += len(f.Matches)
	}
	return total
}

// allMatchTexts 提取 GrepOutput 中所有匹配行的文本内容
func allMatchTexts(output *GrepOutput) []string {
	var texts []string
	for _, f := range output.Files {
		for _, m := range f.Matches {
			texts = append(texts, m.Text)
		}
	}
	return texts
}

// sortedFilePaths 获取排序后的文件路径列表
func sortedFilePaths(output *GrepOutput) []string {
	paths := make([]string, len(output.Files))
	for i, f := range output.Files {
		paths[i] = f.Path
	}
	sort.Strings(paths)
	return paths
}

// ============================================================================
// 2.1 .gitignore 遵循 — gitignoreMatcher unit test
// ============================================================================

// TestGitignoreMatcher_BasicIgnore 测试基本 .gitignore 规则
func TestGitignoreMatcher_BasicIgnore(t *testing.T) {
	dir := t.TempDir()
	gitignoreContent := "vendor/\ndist/\n*.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}

	// 创建文件
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0755); err != nil {
		t.Fatalf("创建 vendor 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "dep.go"), []byte("package vendor"), 0644); err != nil {
		t.Fatalf("创建 vendor/dep.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("log"), 0644); err != nil {
		t.Fatalf("创建 debug.log 失败: %v", err)
	}

	matcher, err := newGitignoreMatcher(dir)
	if err != nil {
		t.Fatalf("newGitignoreMatcher 失败: %v", err)
	}

	// 验证
	if !matcher.ShouldSkip("vendor/dep.go", false) {
		t.Error("vendor/ 应被忽略")
	}
	if !matcher.ShouldSkip("debug.log", false) {
		t.Error("*.log 应被忽略")
	}
	if matcher.ShouldSkip("main.go", false) {
		t.Error("main.go 不应被忽略")
	}
}

// TestGitignoreMatcher_NestedGitignore test nested .gitignore（通过 GoSearchEngine.Grep 验证）
func TestGitignoreMatcher_NestedGitignore(t *testing.T) {
	dir := t.TempDir()
	// 根目录 .gitignore
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644); err != nil {
		t.Fatalf("创建根 .gitignore 失败: %v", err)
	}
	// 子目录 .gitignore
	subDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("创建 src 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, ".gitignore"), []byte("*.tmp\n"), 0644); err != nil {
		t.Fatalf("创建子目录 .gitignore 失败: %v", err)
	}

	// 创建测试文件
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("log content"), 0644); err != nil {
		t.Fatalf("创建 debug.log 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "cache.tmp"), []byte("cache content"), 0644); err != nil {
		t.Fatalf("创建 cache.tmp 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "content|main",
		OutputMode: "files_with_matches",
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	// debug.log 和 cache.tmp 应被忽略
	for _, f := range output.Files {
		if strings.Contains(f.Path, "debug.log") {
			t.Error("debug.log 应被根 .gitignore 忽略")
		}
		if strings.Contains(f.Path, "cache.tmp") {
			t.Error("cache.tmp 应被子目录 .gitignore 忽略")
		}
	}
	// main.go 应被搜索到
	if !containsPath(output.Files, "main.go") {
		t.Error("src/main.go 应被搜索到")
	}
}

// TestGitignoreMatcher_Negation 测试 .gitignore 否定规则
func TestGitignoreMatcher_Negation(t *testing.T) {
	dir := t.TempDir()
	gitignoreContent := "*.log\n!important.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}

	matcher, err := newGitignoreMatcher(dir)
	if err != nil {
		t.Fatalf("newGitignoreMatcher 失败: %v", err)
	}

	if !matcher.ShouldSkip("debug.log", false) {
		t.Error("*.log 应被忽略")
	}
	// 否定规则：important.log 不应被忽略
	if matcher.ShouldSkip("important.log", false) {
		t.Error("important.log 被 ! 否定规则排除，不应被忽略")
	}
}

// ============================================================================
// 2.1.2 GoSearchEngine Grep unit test
// ============================================================================

// TestGoSearchEngine_Grep_RespectsGitignore 测试 Grep 遵循 .gitignore
func TestGoSearchEngine_Grep_RespectsGitignore(t *testing.T) {
	dir := setupTestProject(t)

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "package",
		OutputMode: "files_with_matches",
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	// vendor/dep.go 不应出现在结果中
	for _, f := range output.Files {
		if strings.Contains(f.Path, "vendor") {
			t.Error("vendor/ 应被 .gitignore 忽略")
		}
		if strings.Contains(f.Path, "dist") {
			t.Error("dist/ 应被 .gitignore 忽略")
		}
	}
	// main.go 应出现在结果中
	if !containsPath(output.Files, "main.go") {
		t.Error("main.go 应被搜索到")
	}
}

// TestGoSearchEngine_Grep_SkipsBinaryFiles 测试 Grep 跳过二进制文件
func TestGoSearchEngine_Grep_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	// 创建文本文件
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\nfunc main() {}"), 0644); err != nil {
		t.Fatalf("创建 hello.go 失败: %v", err)
	}
	// 创建二进制文件（含 NUL 字节）
	binaryContent := append([]byte("package"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(filepath.Join(dir, "binary_data"), binaryContent, 0644); err != nil {
		t.Fatalf("创建 binary_data 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "package",
		OutputMode: "files_with_matches",
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	if !containsPath(output.Files, "hello.go") {
		t.Error("hello.go 应被搜索到")
	}
	if containsPath(output.Files, "binary_data") {
		t.Error("二进制文件应被跳过")
	}
}

// ============================================================================
// 2.2 GrepInput 扩展 — output_mode unit test
// ============================================================================

// TestGoSearchEngine_Grep_OutputModeFilesWithMatches 测试 files_with_matches 模式
func TestGoSearchEngine_Grep_OutputModeFilesWithMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package alpha\nfunc Alpha() {}"), 0644); err != nil {
		t.Fatalf("创建 a.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package beta\nfunc Beta() {}"), 0644); err != nil {
		t.Fatalf("创建 b.go 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "package",
		OutputMode: "files_with_matches",
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	// files_with_matches 模式应只返回文件路径，不返回行内容
	if len(output.Files) != 2 {
		t.Errorf("expected 2 个文件，actual %d 个", len(output.Files))
	}
	for _, f := range output.Files {
		if len(f.Matches) != 0 {
			t.Errorf("files_with_matches 模式不should return行内容，但 %s 有 %d 条匹配", f.Path, len(f.Matches))
		}
	}
}

// TestGoSearchEngine_Grep_OutputModeContent 测试 content 模式
func TestGoSearchEngine_Grep_OutputModeContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package alpha\nfunc Alpha() {}"), 0644); err != nil {
		t.Fatalf("创建 a.go 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "Alpha",
		OutputMode: "content",
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	if len(output.Files) != 1 {
		t.Fatalf("expected 1 个文件，actual %d 个", len(output.Files))
	}
	if len(output.Files[0].Matches) != 1 {
		t.Fatalf("expected 1 条匹配，actual %d 条", len(output.Files[0].Matches))
	}
	if !strings.Contains(output.Files[0].Matches[0].Text, "Alpha") {
		t.Errorf("匹配行should contain 'Alpha'，actual: %s", output.Files[0].Matches[0].Text)
	}
	if output.Files[0].Matches[0].LineNumber != 2 {
		t.Errorf("expected行号 2，actual %d", output.Files[0].Matches[0].LineNumber)
	}
}

// TestGoSearchEngine_Grep_OutputModeCount 测试 count 模式
func TestGoSearchEngine_Grep_OutputModeCount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package alpha\nfunc Alpha() { Alpha() }"), 0644); err != nil {
		t.Fatalf("创建 a.go 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "Alpha",
		OutputMode: "count",
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	if len(output.Files) != 1 {
		t.Fatalf("expected 1 个文件，actual %d 个", len(output.Files))
	}
	if output.Files[0].MatchCount != 2 {
		t.Errorf("expected匹配数 2，actual %d", output.Files[0].MatchCount)
	}
}

// ============================================================================
// 2.2.2 上下文行unit test
// ============================================================================

// TestGoSearchEngine_Grep_ContextLines 测试 -C 上下文行
func TestGoSearchEngine_Grep_ContextLines(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3 TARGET\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("创建 test.txt 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:      "TARGET",
		OutputMode:   "content",
		ContextLines: 1,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	// should contain匹配行 + 前后各 1 行
	matches := output.Files[0].Matches
	if len(matches) != 3 {
		t.Fatalf("should return 3 行（1 上下文 + 1 匹配 + 1 上下文），actual %d 行", len(matches))
	}
	if !strings.Contains(matches[0].Text, "line2") {
		t.Errorf("第一行上下文should contain 'line2'，actual: %s", matches[0].Text)
	}
	if !strings.Contains(matches[1].Text, "TARGET") {
		t.Errorf("匹配行should contain 'TARGET'，actual: %s", matches[1].Text)
	}
	if !strings.Contains(matches[2].Text, "line4") {
		t.Errorf("最后一行上下文should contain 'line4'，actual: %s", matches[2].Text)
	}
}

// TestGoSearchEngine_Grep_BeforeAfterContext 测试 -B/-A 上下文行
func TestGoSearchEngine_Grep_BeforeAfterContext(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3 TARGET\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("创建 test.txt 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:       "TARGET",
		OutputMode:    "content",
		BeforeContext: 2,
		AfterContext:  1,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	matches := output.Files[0].Matches
	if len(matches) != 4 {
		t.Fatalf("should return 4 行（2 before + 1 match + 1 after），actual %d 行", len(matches))
	}
}

// ============================================================================
// 2.2.3 大小写忽略unit test
// ============================================================================

// TestGoSearchEngine_Grep_CaseInsensitive 测试 -i 大小写忽略
func TestGoSearchEngine_Grep_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.go"), []byte("func getUser() {}\nfunc GetUser() {}"), 0644); err != nil {
		t.Fatalf("创建 test.go 失败: %v", err)
	}

	// 不忽略大小写 — 只匹配小写
	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:         "getUser",
		OutputMode:      "content",
		CaseInsensitive: false,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}
	if totalMatches(output) != 1 {
		t.Errorf("精确匹配应只找到 1 处，actual %d 处", totalMatches(output))
	}

	// 忽略大小写 — 匹配两种
	output, err = engine.Grep(&GrepInput{
		Pattern:         "getUser",
		OutputMode:      "content",
		CaseInsensitive: true,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}
	if totalMatches(output) != 2 {
		t.Errorf("忽略大小写应找到 2 处，actual %d 处", totalMatches(output))
	}
}

// ============================================================================
// 2.2.4 head_limit / offset unit test
// ============================================================================

// TestGoSearchEngine_Grep_HeadLimit 测试 head_limit
func TestGoSearchEngine_Grep_HeadLimit(t *testing.T) {
	dir := t.TempDir()
	// 创建有 10 处匹配的文件
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("match_%d", i))
	}
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("创建 test.txt 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "match_",
		OutputMode: "content",
		HeadLimit:  3,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}
	if totalMatches(output) != 3 {
		t.Errorf("head_limit=3 应只返回 3 条，actual %d 条", totalMatches(output))
	}
}

// TestGoSearchEngine_Grep_Offset 测试 offset
func TestGoSearchEngine_Grep_Offset(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("match_%d", i))
	}
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("创建 test.txt 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{
		Pattern:    "match_",
		OutputMode: "content",
		Offset:     5,
		HeadLimit:  3,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}
	matches := allMatchTexts(output)
	if len(matches) != 3 {
		t.Fatalf("expected 3 条匹配，actual %d 条", len(matches))
	}
	if !strings.Contains(matches[0], "match_5") {
		t.Errorf("offset=5 应从第 6 条开始，actual: %s", matches[0])
	}
}

// ============================================================================
// 2.2.5 多行模式unit test
// ============================================================================

// TestGoSearchEngine_Grep_Multiline 测试多行模式
func TestGoSearchEngine_Grep_Multiline(t *testing.T) {
	dir := t.TempDir()
	content := "func main() {\n    return nil\n}\nfunc other() {\n    return nil\n}"
	if err := os.WriteFile(filepath.Join(dir, "test.go"), []byte(content), 0644); err != nil {
		t.Fatalf("创建 test.go 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}

	// 多行模式：匹配跨行的模式
	output, err := engine.Grep(&GrepInput{
		Pattern:    "main.*return",
		OutputMode: "content",
		Multiline:  true,
	})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}
	if totalMatches(output) < 1 {
		t.Error("多行模式应匹配跨行内容")
	}
}

// ============================================================================
// 2.3 二进制文件检测unit test
// ============================================================================

// TestIsBinaryFile_ByExtension 测试扩展名判断
func TestIsBinaryFile_ByExtension(t *testing.T) {
	dir := t.TempDir()

	binaryExts := []string{".exe", ".dll", ".png", ".jpg", ".zip", ".pdf", ".pyc"}
	for _, ext := range binaryExts {
		path := filepath.Join(dir, "test"+ext)
		if err := os.WriteFile(path, []byte("text"), 0644); err != nil {
			t.Fatalf("创建 %s 失败: %v", ext, err)
		}
		if !isBinaryFile(path) {
			t.Errorf("%s 应被判定为二进制", ext)
		}
	}

	textExts := []string{".go", ".py", ".ts", ".md", ".json", ".yaml"}
	for _, ext := range textExts {
		path := filepath.Join(dir, "test"+ext)
		if err := os.WriteFile(path, []byte("text content"), 0644); err != nil {
			t.Fatalf("创建 %s 失败: %v", ext, err)
		}
		if isBinaryFile(path) {
			t.Errorf("%s 应被判定为文本", ext)
		}
	}
}

// TestIsBinaryFile_ByContent 测试内容检测（NUL 字节）
func TestIsBinaryFile_ByContent(t *testing.T) {
	dir := t.TempDir()

	// 无扩展名 + 含 NUL 字节
	path := filepath.Join(dir, "noext_binary")
	if err := os.WriteFile(path, append([]byte("hello"), 0x00, 0x01), 0644); err != nil {
		t.Fatalf("创建 noext_binary 失败: %v", err)
	}
	if !isBinaryFile(path) {
		t.Error("含 NUL 字节应被判定为二进制")
	}

	// 无扩展名 + 纯文本
	path2 := filepath.Join(dir, "noext_text")
	if err := os.WriteFile(path2, []byte("hello world"), 0644); err != nil {
		t.Fatalf("创建 noext_text 失败: %v", err)
	}
	if isBinaryFile(path2) {
		t.Error("纯文本不应被判定为二进制")
	}
}

// TestIsBinaryFile_EmptyFile 测试空文件
func TestIsBinaryFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("创建 empty 文件失败: %v", err)
	}
	if isBinaryFile(path) {
		t.Error("空文件不应被判定为二进制")
	}
}

// ============================================================================
// 2.4 glob ** fixedunit test
// ============================================================================

// TestGlobWithDoubleStar_Basic 测试基本 ** 匹配
func TestGlobWithDoubleStar_Basic(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "api"), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package src"), 0644); err != nil {
		t.Fatalf("创建 src/app.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "api", "handler.go"), []byte("package api"), 0644); err != nil {
		t.Fatalf("创建 src/api/handler.go 失败: %v", err)
	}

	matches, err := globWithDoubleStar(dir, "**/*.go", nil)
	if err != nil {
		t.Fatalf("globWithDoubleStar 失败: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("**/*.go 应匹配所有层级的 .go 文件，expected 3 个，actual %d 个", len(matches))
	}
}

// TestGlobWithDoubleStar_Nested 测试深层嵌套
func TestGlobWithDoubleStar_Nested(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "c", "deep.go"), []byte("package deep"), 0644); err != nil {
		t.Fatalf("创建 deep.go 失败: %v", err)
	}

	matches, err := globWithDoubleStar(dir, "**/*.go", nil)
	if err != nil {
		t.Fatalf("globWithDoubleStar 失败: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("** 应匹配任意深度，expected 1 个，actual %d 个", len(matches))
	}
}

// TestGlobWithDoubleStar_RespectsGitignore 测试 ** 遵循 .gitignore
func TestGlobWithDoubleStar_RespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\n"), 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0755); err != nil {
		t.Fatalf("创建 vendor 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "dep.go"), []byte("package dep"), 0644); err != nil {
		t.Fatalf("创建 vendor/dep.go 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}

	// 创建 gitignore matcher
	matcher, err := newGitignoreMatcher(dir)
	if err != nil {
		t.Fatalf("newGitignoreMatcher 失败: %v", err)
	}

	matches, err := globWithDoubleStar(dir, "**/*.go", matcher)
	if err != nil {
		t.Fatalf("globWithDoubleStar 失败: %v", err)
	}
	for _, m := range matches {
		if strings.Contains(m, "vendor") {
			t.Errorf("glob 应遵循 .gitignore，但匹配到: %s", m)
		}
	}
}

// ============================================================================
// 3.1 搜索结果预算控制unit test
// ============================================================================

// TestSearchResultBudget_TruncateOutput test output截断
func TestSearchResultBudget_TruncateOutput(t *testing.T) {
	budget := &SearchResultBudget{
		MaxGrepLines:   3,
		MaxResultChars: 1024,
		TruncationHint: "[truncated]",
	}

	// 行数超限
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	result := budget.TruncateOutput(longOutput)
	if !strings.Contains(result, "[truncated]") {
		t.Error("行数超限时should contain截断提示")
	}
	// 保留前 3 行
	if !strings.HasPrefix(result, "line1\nline2\nline3") {
		t.Errorf("应保留前 3 行，actual: %s", result[:50])
	}

	// 字符数超限
	hugeOutput := strings.Repeat("x", 2048)
	result = budget.TruncateOutput(hugeOutput)
	if !strings.Contains(result, "[truncated]") {
		t.Error("字符数超限时should contain截断提示")
	}
	if len(result) > 1024+len("[truncated]") {
		t.Errorf("截断后长度应不超过 %d，actual %d", 1024+len("[truncated]"), len(result))
	}

	// 未超限
	shortOutput := "line1\nline2\n"
	result = budget.TruncateOutput(shortOutput)
	if result != shortOutput {
		t.Error("未超限不应截断")
	}
}

// ============================================================================
// 3.2 搜索上下文追踪unit test
// ============================================================================

// TestSearchContext_RecordAndQuery 测试搜索记录与查询
func TestSearchContext_RecordAndQuery(t *testing.T) {
	ctx := NewSearchContext()

	ctx.RecordSearch("getUserById")
	ctx.RecordSearch("handleRequest")
	ctx.RecordRead("/app/src/main.go")

	if !ctx.HasSearched("getUserById") {
		t.Error("应记录已搜索的模式 getUserById")
	}
	if !ctx.HasSearched("handleRequest") {
		t.Error("应记录已搜索的模式 handleRequest")
	}
	if ctx.HasSearched("nonexistent") {
		t.Error("未搜索的模式should return false")
	}

	found := false
	for _, f := range ctx.ReadFiles {
		if f == "/app/src/main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Error("应记录已读取的文件 /app/src/main.go")
	}
}

// TestSearchContext_ConcurrentAccess test concurrent safety
func TestSearchContext_ConcurrentAccess(t *testing.T) {
	ctx := NewSearchContext()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx.RecordSearch(fmt.Sprintf("pattern_%d", n))
			ctx.RecordRead(fmt.Sprintf("/file_%d.go", n))
			ctx.HasSearched(fmt.Sprintf("pattern_%d", n))
		}(i)
	}
	wg.Wait()

	if len(ctx.SearchPatterns) != 100 {
		t.Errorf("并发写入后应有 100 条搜索记录，actual %d 条", len(ctx.SearchPatterns))
	}
	if len(ctx.ReadFiles) != 100 {
		t.Errorf("并发写入后应有 100 条文件记录，actual %d 条", len(ctx.ReadFiles))
	}
}

// ============================================================================
// BOM 编码处理测试
// ============================================================================

// TestStripBOM_UTF8BOM 测试剥离 UTF-8 BOM
func TestStripBOM_UTF8BOM(t *testing.T) {
	input := []byte{0xEF, 0xBB, 0xBF, 'v', 'e', 'n', 'd', 'o', 'r', '/'}
	expected := []byte("vendor/")
	result := stripBOM(input)
	if string(result) != string(expected) {
		t.Errorf("stripBOM(UTF-8 BOM) = %q, want %q", result, expected)
	}
}

// TestStripBOM_UTF16LE 测试剥离 UTF-16 LE BOM 并转换
func TestStripBOM_UTF16LE(t *testing.T) {
	// UTF-16 LE: FF FE + 每字符两字节（ASCII 字符第二字节为 0）
	input := []byte{0xFF, 0xFE, 'v', 0x00, 'e', 0x00, 'n', 0x00}
	expected := []byte("ven")
	result := stripBOM(input)
	if string(result) != string(expected) {
		t.Errorf("stripBOM(UTF-16 LE) = %q, want %q", result, expected)
	}
}

// TestStripBOM_NoBOM 测试无 BOM 的数据保持不变
func TestStripBOM_NoBOM(t *testing.T) {
	input := []byte("vendor/\ndist/\n")
	result := stripBOM(input)
	if string(result) != string(input) {
		t.Errorf("stripBOM(no BOM) = %q, want %q", result, input)
	}
}

// TestCompileGitignoreFile_UTF8BOM 测试带 UTF-8 BOM 的 .gitignore 文件能正确编译
func TestCompileGitignoreFile_UTF8BOM(t *testing.T) {
	dir := t.TempDir()

	// 创建带 UTF-8 BOM 的 .gitignore
	gitignoreContent := append([]byte{0xEF, 0xBB, 0xBF}, []byte("vendor/\ndist/\n*.log\n")...)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), gitignoreContent, 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}

	// 创建源文件和应被忽略的文件
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0755); err != nil {
		t.Fatalf("创建 vendor 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "dep.go"), []byte("package vendor"), 0644); err != nil {
		t.Fatalf("创建 vendor/dep.go 失败: %v", err)
	}

	// 编译 .gitignore
	parser, err := compileGitignoreFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("compileGitignoreFile 失败: %v", err)
	}

	// 验证 vendor 目录被忽略
	if !parser.MatchesPath("vendor/dep.go") {
		t.Error("带 UTF-8 BOM 的 .gitignore 应该匹配 vendor/dep.go")
	}
}

// TestCompileGitignoreFile_UTF16LE 测试带 UTF-16 LE BOM 的 .gitignore 文件能正确编译
func TestCompileGitignoreFile_UTF16LE(t *testing.T) {
	dir := t.TempDir()

	// 创建带 UTF-16 LE BOM 的 .gitignore
	// FF FE + "vendor/\n" 的 UTF-16 LE 编码
	var content []byte
	content = append(content, 0xFF, 0xFE) // BOM
	for _, c := range "vendor/\r\n" {
		content = append(content, byte(c), 0x00)
	}

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), content, 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}

	// 编译 .gitignore
	parser, err := compileGitignoreFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("compileGitignoreFile 失败: %v", err)
	}

	// 验证 vendor 目录被忽略
	if !parser.MatchesPath("vendor/dep.go") {
		t.Error("带 UTF-16 LE BOM 的 .gitignore 应该匹配 vendor/dep.go")
	}
}

// TestGoSearchEngine_Grep_UTF8BOMGitignore 测试 GoSearchEngine 在 UTF-8 BOM .gitignore 下正确排除目录
func TestGoSearchEngine_Grep_UTF8BOMGitignore(t *testing.T) {
	dir := t.TempDir()

	// 创建带 UTF-8 BOM 的 .gitignore
	gitignoreContent := append([]byte{0xEF, 0xBB, 0xBF}, []byte("vendor/\ndist/\n*.log\n")...)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), gitignoreContent, 0644); err != nil {
		t.Fatalf("创建 .gitignore 失败: %v", err)
	}

	// 创建源文件和应被忽略的文件
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("创建 main.go 失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0755); err != nil {
		t.Fatalf("创建 vendor 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "dep.go"), []byte("package vendor"), 0644); err != nil {
		t.Fatalf("创建 vendor/dep.go 失败: %v", err)
	}

	engine := &GoSearchEngine{workDir: dir}
	output, err := engine.Grep(&GrepInput{Pattern: "package"})
	if err != nil {
		t.Fatalf("Grep 失败: %v", err)
	}

	if containsPath(output.Files, "vendor") {
		t.Error("带 UTF-8 BOM 的 .gitignore 下，搜索结果should not contain vendor 目录的文件")
	}
	if !containsPath(output.Files, "main.go") {
		t.Error("搜索结果should contain main.go")
	}
}

// ============================================================================
// Section 18: 性能benchmark
// ============================================================================

// setupBenchDir 创建包含 100 个文件的benchmark目录
func setupBenchDir(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()

	for i := 0; i < 100; i++ {
		filename := fmt.Sprintf("file_%03d.go", i)
		content := fmt.Sprintf("package bench\n// File %d\nfunc Func%d() {}\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
			b.Fatalf("创建 %s 失败: %v", filename, err)
		}
	}

	return dir
}

// BenchmarkSearchEngine_Grep benchmark Grep 在 100 文件目录上的搜索性能
func BenchmarkSearchEngine_Grep(b *testing.B) {
	dir := setupBenchDir(b)
	engine := &GoSearchEngine{workDir: dir}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := engine.Grep(&GrepInput{
			Pattern:    "Func",
			OutputMode: "files_with_matches",
		})
		if err != nil {
			b.Fatalf("Grep 失败: %v", err)
		}
	}
}

// BenchmarkSearchEngine_Glob benchmark Glob 在 100 文件目录上的匹配性能
func BenchmarkSearchEngine_Glob(b *testing.B) {
	dir := setupBenchDir(b)
	engine := &GoSearchEngine{workDir: dir}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := engine.Glob(&GlobInput{
			Pattern: "**/*.go",
		})
		if err != nil {
			b.Fatalf("Glob 失败: %v", err)
		}
	}
}
