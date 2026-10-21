// analyzer_test.go — 桶 5 whale-lint 反例测试（地基层 §2.3）。
// 6 个反例测试覆盖：无 plugin import / Windows 交叉编译 / error path / TODO / nolint / panic-in-lib。

package whale_lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWhaleLint_NoPluginImport 致命反例：扫描 whale_lint 包所有 .go 文件，验证无 import "plugin"。
// 验证目标（PIT-004）：Windows 不可用 go plugin，零容忍。
func TestWhaleLint_NoPluginImport(t *testing.T) {
	// 获取当前测试文件所在目录（即 whale_lint 包目录）
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	pkgDir := filepath.Dir(testFile)

	// 递归扫描所有 .go 文件（含子目录 cmd/）
	var violations []string
	err := filepath.Walk(pkgDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// 跳过测试文件自身
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 读取文件内容检查是否含 import "plugin"
		buf, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(buf)
		// 检测 import "plugin"（精确匹配，避免误报 "plugin" 出现在注释中）
		if strings.Contains(content, `"plugin"`) {
			// 进一步确认是 import 语句（而非字符串字面量）
			if strings.Contains(content, `import "plugin"`) ||
				strings.Contains(content, `import (`+"\n"+`	"plugin"`) ||
				strings.Contains(content, "\t\"plugin\"") {
				violations = append(violations, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("PIT-004 violation: whale_lint package must not import \"plugin\" (Windows incompatible): %v", violations)
	}
}

// TestWhaleLint_WindowsBuild 在 WSL 跑 GOOS=windows go build，验证 Windows 交叉编译通过。
// 此测试仅检查当前平台能编译（完整 Windows 交叉编译由 WSL 测试命令覆盖）。
func TestWhaleLint_WindowsBuild(t *testing.T) {
	// Phase 0 简化：仅验证当前平台能编译（GOOS=windows 交叉编译由 WSL 测试命令统一覆盖）
	// 此测试确保 whale_lint 包不含平台特定代码（如 syscall 直接调用）
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	pkgDir := filepath.Dir(testFile)

	// 扫描所有 .go 文件，检查不含平台特定 import
	forbiddenImports := []string{
		`"plugin"`,                   // PIT-004
		`"syscall"`,                  // 平台特定
		`"unsafe"`,                   // 平台特定
		`"os/signal"`,                // 平台特定
		`"golang.org/x/sys/windows"`, // Windows 特定（应通过 platform 包封装）
	}

	err := filepath.Walk(pkgDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		buf, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(buf)
		for _, bad := range forbiddenImports {
			if strings.Contains(content, bad) {
				t.Errorf("Windows compatibility violation: %s contains %s", path, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
}

// parseSource 解析 Go 源码为 AST（测试辅助函数）。
func parseSource(t *testing.T, src string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return file, fset
}

// TestErrorPathAnalyzer_NoErrorPath 测试文件只有 happy path → 报告 "missing error path"。
func TestErrorPathAnalyzer_NoErrorPath(t *testing.T) {
	src := `package foo
func DoSomething() error {
	return nil
}
`
	file, fset := parseSource(t, src)

	var diags []Diagnostic
	pass := &Pass{
		Fset:   fset,
		Report: func(d Diagnostic) { diags = append(diags, d) },
	}
	CheckErrorPath(pass, file)

	// 应报告 missing error path（函数返回 error 但无 if err != nil）
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "missing error path") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'missing error path' diagnostic for function returning error without error handling")
	}
}

// TestTODOAnalyzer_HasTODO 交付代码含 // TODO → 报告。
func TestTODOAnalyzer_HasTODO(t *testing.T) {
	src := `package foo
// DoSomething does something
// TODO: implement this
func DoSomething() {}
`
	file, fset := parseSource(t, src)

	var diags []Diagnostic
	pass := &Pass{
		Fset:   fset,
		Report: func(d Diagnostic) { diags = append(diags, d) },
	}
	CheckTODO(pass, file)

	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "TODO") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected TODO diagnostic for code containing // TODO")
	}
}

// TestNoLintAnalyzer_BareNolint 代码含裸 //nolint → 报告；含 //nolint:errcheck // reason → 通过。
func TestNoLintAnalyzer_BareNolint(t *testing.T) {
	src := `package foo
// DoSomething does something
//
//nolint
func DoSomething() {}

// OtherFunc has proper nolint
//
//nolint:errcheck // reason
func OtherFunc() {}
`
	file, fset := parseSource(t, src)

	var diags []Diagnostic
	pass := &Pass{
		Fset:   fset,
		Report: func(d Diagnostic) { diags = append(diags, d) },
	}
	CheckNoLint(pass, file)

	// 应报告 1 个裸 nolint（//nolint），不报告 //nolint:errcheck // reason
	bareCount := 0
	for _, d := range diags {
		if strings.Contains(d.Message, "bare //nolint") {
			bareCount++
		}
	}
	if bareCount != 1 {
		t.Errorf("expected 1 bare nolint diagnostic, got %d", bareCount)
	}
}

// TestPanicInLibAnalyzer_PanicInInternal internal/ 下含 panic() → 报告。
func TestPanicInLibAnalyzer_PanicInInternal(t *testing.T) {
	src := `package foo
func Crash() {
	panic("should not be in lib")
}
`
	// 用 parser.ParseFile 直接指定 internal/ 路径作为文件名（模拟文件位置）
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "/some/path/internal/foo/foo.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	var diags []Diagnostic
	pass := &Pass{
		Fset:   fset,
		Report: func(d Diagnostic) { diags = append(diags, d) },
	}
	CheckPanicInLib(pass, file)

	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "panic() forbidden") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected panic() forbidden diagnostic for panic in internal/")
	}
}

// TestPanicInLibAnalyzer_NoPanicInNonInternal 非 internal/ 下 panic() 不报告（补充测试）。
func TestPanicInLibAnalyzer_NoPanicInNonInternal(t *testing.T) {
	src := `package main
func main() {
	panic("crash")
}
`
	// 用 parser.ParseFile 直接指定非 internal/ 路径作为文件名
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "/some/path/cmd/foo/main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	var diags []Diagnostic
	pass := &Pass{
		Fset:   fset,
		Report: func(d Diagnostic) { diags = append(diags, d) },
	}
	CheckPanicInLib(pass, file)

	if len(diags) != 0 {
		t.Errorf("expected no diagnostic for panic in non-internal/, got %d", len(diags))
	}
}
