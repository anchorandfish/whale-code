//go:build integration

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// setupRGTestProject 在临时目录中创建用于 ripgrep integration test的项目结构
// 包含multi-tier目录、多种文件类型和不同的内容模式
func setupRGTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// 创建子目录
	subDirs := []string{
		"src",
		"src/utils",
		"pkg",
	}
	for _, d := range subDirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
	}

	// 创建测试文件
	files := map[string]string{
		"main.go":             "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"src/app.go":          "package src\n\nfunc Run() {\n\tprintln(\"app run\")\n}\n",
		"src/utils/helper.go": "package utils\n\nfunc Help() {\n\tprintln(\"help\")\n}\n\nfunc main() {\n\tHelp()\n}\n",
		"pkg/config.go":       "package pkg\n\nconst Name = \"whale\"\n",
		"readme.txt":          "This is a readme file.\nIt has multiple lines.\nGoodbye.\n",
	}

	for name, content := range files {
		fullPath := filepath.Join(dir, name)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("写入文件失败: %v", err)
		}
	}

	return dir
}

// skipIfNoRipgrep 如果系统未安装 rg 则跳过测试
func skipIfNoRipgrep(t *testing.T) string {
	t.Helper()
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep (rg) 未安装，跳过integration test")
	}
	return rgPath
}

// TestRipgrepEngine_Grep_Basic 测试 ripgrep 引擎的基本搜索功能
// 验证 rg 能正确搜索文件内容并返回匹配结果
func TestRipgrepEngine_Grep_Basic(t *testing.T) {
	rgPath := skipIfNoRipgrep(t)
	dir := setupRGTestProject(t)

	engine := &RipgrepEngine{rgPath: rgPath, workDir: dir}

	// 测试基本正则搜索
	output, err := engine.Grep(&GrepInput{
		Pattern: "func main",
		Path:    dir,
	})
	if err != nil {
		t.Fatalf("Grep 执行失败: %v", err)
	}

	// 应至少匹配到两个文件中的 func main
	if len(output.Files) == 0 {
		t.Fatal("expected找到匹配结果，但结果为空")
	}

	// 验证匹配内容包含 "func main"
	totalMatches := 0
	for _, f := range output.Files {
		totalMatches += f.MatchCount
	}
	if totalMatches < 2 {
		t.Errorf("expected至少 2 处匹配，actual %d 处", totalMatches)
	}

	// 测试no match的情况
	output, err = engine.Grep(&GrepInput{
		Pattern: "zzz_nonexistent_pattern_zzz",
		Path:    dir,
	})
	if err != nil {
		t.Fatalf("Grep 执行失败: %v", err)
	}
	if len(output.Files) != 0 {
		t.Errorf("expectedno match结果，但得到 %d 个文件", len(output.Files))
	}
}

// TestRipgrepEngine_Grep_SameResultsAsGoEngine 比较 ripgrep 引擎与 Go 原生引擎的搜索结果
// 两种引擎对相同输入should return相同的文件集合和匹配行号
func TestRipgrepEngine_Grep_SameResultsAsGoEngine(t *testing.T) {
	rgPath := skipIfNoRipgrep(t)
	dir := setupRGTestProject(t)

	rgEngine := &RipgrepEngine{rgPath: rgPath, workDir: dir}
	goEngine := &GoSearchEngine{workDir: dir}

	patterns := []string{
		"func main",
		"println",
		"package",
	}

	for _, pattern := range patterns {
		t.Run("pattern="+pattern, func(t *testing.T) {
			rgOutput, err := rgEngine.Grep(&GrepInput{
				Pattern:    pattern,
				Path:       dir,
				OutputMode: "content",
			})
			if err != nil {
				t.Fatalf("RipgrepEngine Grep 失败: %v", err)
			}

			goOutput, err := goEngine.Grep(&GrepInput{
				Pattern:    pattern,
				Path:       dir,
				OutputMode: "content",
			})
			if err != nil {
				t.Fatalf("GoSearchEngine Grep 失败: %v", err)
			}

			// 收集两种引擎匹配的文件路径集合
			rgFiles := extractMatchedFiles(rgOutput, dir)
			goFiles := extractMatchedFiles(goOutput, dir)

			// 比较文件集合
			if !equalStringSlices(rgFiles, goFiles) {
				t.Errorf("文件集合不一致\nrg 文件:  %v\ngo 文件: %v", rgFiles, goFiles)
			}

			// 比较每个文件中的匹配行号
			rgLineMap := extractLineNumbers(rgOutput, dir)
			goLineMap := extractLineNumbers(goOutput, dir)

			for file, goLines := range goLineMap {
				rgLines, ok := rgLineMap[file]
				if !ok {
					t.Errorf("文件 %s: rg 未匹配，go 匹配行号 %v", file, goLines)
					continue
				}
				if !equalIntSlices(rgLines, goLines) {
					t.Errorf("文件 %s: 行号不一致\nrg:  %v\ngo: %v", file, rgLines, goLines)
				}
			}
		})
	}
}

// TestRipgrepEngine_Grep_ContextLines 测试 ripgrep 引擎的上下文行功能
// 验证 ContextLines 参数能正确返回匹配行周围的上下文行
func TestRipgrepEngine_Grep_ContextLines(t *testing.T) {
	rgPath := skipIfNoRipgrep(t)
	dir := setupRGTestProject(t)

	engine := &RipgrepEngine{rgPath: rgPath, workDir: dir}

	// 搜索 "hello"，带 1 行上下文
	output, err := engine.Grep(&GrepInput{
		Pattern:      "hello",
		Path:         dir,
		ContextLines: 1,
		OutputMode:   "content",
	})
	if err != nil {
		t.Fatalf("Grep 执行失败: %v", err)
	}

	if len(output.Files) == 0 {
		t.Fatal("expected找到匹配结果，但结果为空")
	}

	// 验证结果中包含上下文行（IsContext=true 的行）
	hasContext := false
	for _, f := range output.Files {
		for _, m := range f.Matches {
			if m.IsContext {
				hasContext = true
				break
			}
		}
		if hasContext {
			break
		}
	}
	if !hasContext {
		t.Error("expected包含上下文行（IsContext=true），但未找到")
	}

	// 验证上下文行的行号与匹配行号相邻
	for _, f := range output.Files {
		matchLines := make(map[int]bool)
		contextLines := make(map[int]bool)
		for _, m := range f.Matches {
			if m.IsContext {
				contextLines[m.LineNumber] = true
			} else {
				matchLines[m.LineNumber] = true
			}
		}
		// 每个上下文行应该与某个匹配行相邻
		for ctxLine := range contextLines {
			adjacent := false
			for matchLine := range matchLines {
				if ctxLine == matchLine-1 || ctxLine == matchLine+1 {
					adjacent = true
					break
				}
			}
			if !adjacent {
				t.Errorf("上下文行 %d 不与任何匹配行相邻", ctxLine)
			}
		}
	}
}

// extractMatchedFiles 从 GrepOutput 中提取匹配文件的相对路径并排序
func extractMatchedFiles(output *GrepOutput, baseDir string) []string {
	var files []string
	for _, f := range output.Files {
		rel, err := filepath.Rel(baseDir, f.Path)
		if err != nil {
			rel = f.Path
		}
		// 统一路径分隔符
		rel = strings.ReplaceAll(rel, "\\", "/")
		files = append(files, rel)
	}
	sort.Strings(files)
	return files
}

// extractLineNumbers 从 GrepOutput 中提取每个文件的匹配行号（排除上下文行）
func extractLineNumbers(output *GrepOutput, baseDir string) map[string][]int {
	result := make(map[string][]int)
	for _, f := range output.Files {
		rel, err := filepath.Rel(baseDir, f.Path)
		if err != nil {
			rel = f.Path
		}
		rel = strings.ReplaceAll(rel, "\\", "/")
		var lines []int
		for _, m := range f.Matches {
			if !m.IsContext {
				lines = append(lines, m.LineNumber)
			}
		}
		sort.Ints(lines)
		if len(lines) > 0 {
			result[rel] = lines
		}
	}
	return result
}

// TestRipgrepEngine_Glob 测试 ripgrep 引擎的 Glob 文件路径搜索功能
// 验证 RipgrepEngine.Glob() 能正确匹配 glob 模式并返回对应文件列表
func TestRipgrepEngine_Glob(t *testing.T) {
	rgPath := skipIfNoRipgrep(t)
	dir := setupRGTestProject(t)

	engine := &RipgrepEngine{rgPath: rgPath, workDir: dir}

	t.Run("BasicGlob", func(t *testing.T) {
		// 测试基本 glob 搜索，使用 "*.go" 模式匹配所有 Go 文件
		output, err := engine.Glob(&GlobInput{
			Pattern: "*.go",
			Path:    dir,
		})
		if err != nil {
			t.Fatalf("Glob 执行失败: %v", err)
		}

		// 应至少匹配到 main.go
		if len(output.Files) == 0 {
			t.Fatal("expected找到匹配文件，但结果为空")
		}

		// 验证结果中所有文件路径都以 .go 结尾
		for _, f := range output.Files {
			if !strings.HasSuffix(f, ".go") {
				t.Errorf("expected文件以 .go 结尾，actual: %s", f)
			}
		}
	})

	t.Run("NestedGlob", func(t *testing.T) {
		// test nested目录的 glob 模式匹配
		output, err := engine.Glob(&GlobInput{
			Pattern: "**/*.go",
			Path:    dir,
		})
		if err != nil {
			t.Fatalf("Glob 执行失败: %v", err)
		}

		// 应匹配到multi-tier目录下的 Go 文件
		if len(output.Files) < 3 {
			t.Errorf("expected至少 3 个 Go 文件，actual %d 个", len(output.Files))
		}

		// 验证结果中包含子目录的文件
		hasNestedFile := false
		for _, f := range output.Files {
			rel, _ := filepath.Rel(dir, f)
			rel = strings.ReplaceAll(rel, "\\", "/")
			if strings.Contains(rel, "/") {
				hasNestedFile = true
				break
			}
		}
		if !hasNestedFile {
			t.Error("expected包含子目录中的 Go 文件，但未找到")
		}
	})

	t.Run("NoMatch", func(t *testing.T) {
		// 测试no match的模式
		output, err := engine.Glob(&GlobInput{
			Pattern: "*.nonexistent_ext",
			Path:    dir,
		})
		if err != nil {
			t.Fatalf("Glob 执行失败: %v", err)
		}
		if len(output.Files) != 0 {
			t.Errorf("expectedno match结果，actual得到 %d 个文件", len(output.Files))
		}
	})

	t.Run("TxtGlob", func(t *testing.T) {
		// 测试匹配 .txt 文件
		output, err := engine.Glob(&GlobInput{
			Pattern: "*.txt",
			Path:    dir,
		})
		if err != nil {
			t.Fatalf("Glob 执行失败: %v", err)
		}
		if len(output.Files) == 0 {
			t.Fatal("expected找到 .txt 文件，但结果为空")
		}
		for _, f := range output.Files {
			if !strings.HasSuffix(f, ".txt") {
				t.Errorf("expected文件以 .txt 结尾，actual: %s", f)
			}
		}
	})
}

// equalStringSlices 比较两个字符串切片是否相等
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// equalIntSlices 比较两个整数切片是否相等
func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
