// Package whale_lint 提供 whale-code 自定义反偷懒检查器。
// 对标 golangci-lint 底层 go/analysis 框架，采用方案 A 独立二进制（地基层 §2.3 v0.3）。
// REF-外部-go/analysis@https://pkg.go.dev/golang.org/x/tools/go/analysis+2026-07-15访问-analyzer 框架
// analyzer 框架；落地：非 plugin，方案 A 独立二进制（Windows 兼容）
// plugin Windows 不可用；落地：禁用 go build -buildmode=plugin
// SNAPSHOT: framework=go/analysis 兼容（stdlib go/ast 实现，无外部依赖）; binary=cmd/whale-lint（方案A独立二进制）; no_plugin=true; windows_compatible=true

// 注意：本包不导入 plugin 包（PIT-004 Windows 不可用），使用 stdlib go/ast + go/parser 实现。
// Analyzer 结构与 golang.org/x/tools/go/analysis.Analyzer 字段兼容，便于未来迁移到完整 go/analysis 框架。

package whale_lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Analyzer whale-lint 主 analyzer，聚合所有子检查器（结构兼容 go/analysis.Analyzer）。
// 字段对齐 golang.org/x/tools/go/analysis.Analyzer：Name/Doc/Run/Requires。
type Analyzer struct {
	Name     string
	Doc      string
	Requires []*Analyzer
	Run      func(pass *Pass) (interface{}, error)
}

// Pass 传递给 analyzer.Run 的上下文（结构兼容 go/analysis.Pass）。
// Phase 0 简化版：仅含必要字段；Phase 1 接入完整 go/analysis 时替换为 *analysis.Pass。
type Pass struct {
	Dir    string           // 扫描根目录
	Fset   *token.FileSet   // 文件位置集合
	Files  []*ast.File      // 解析后的 AST 文件列表
	Report func(Diagnostic) // 报告诊断结果的回调
}

// Diagnostic 诊断结果（结构兼容 go/analysis.Diagnostic）。
type Diagnostic struct {
	Pos     token.Position
	Message string
}

// NewAnalyzer 创建 whale-lint 主 analyzer，聚合所有子检查器。
// 对标 golangci-lint 底层 go/analysis 框架，可被 golangci-lint 通过 enable-only 调用（结构兼容）。
func NewAnalyzer() *Analyzer {
	return &Analyzer{
		Name: "whale-lint",
		Doc:  "whale-code 反偷懒检查器：error path / TODO / godoc / citation / nolint / panic-in-lib",
		Run:  runAll,
	}
}

// runAll 主 analyzer 的 Run 函数：解析目录下所有 .go 文件 → 逐个跑子检查器。
func runAll(pass *Pass) (interface{}, error) {
	// 收集目录下所有 .go 文件（递归）
	files, err := collectGoFiles(pass.Dir)
	if err != nil {
		return nil, err
	}

	// 解析每个文件为 AST
	fset := token.NewFileSet()
	var astFiles []*ast.File
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			continue // 解析失败的文件跳过（不阻塞其他文件检查）
		}
		astFiles = append(astFiles, af)
	}
	pass.Fset = fset
	pass.Files = astFiles

	// 逐个跑子检查器
	for _, af := range astFiles {
		CheckErrorPath(pass, af)
		CheckTODO(pass, af)
		CheckDocCoverage(pass, af)
		CheckCitation(pass, af)
		CheckNoLint(pass, af)
		CheckPanicInLib(pass, af)
	}
	return nil, nil
}

// collectGoFiles 递归收集 dir 下所有 .go 文件（排除 _test.go 与 vendor/）。
func collectGoFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的路径
		}
		// 跳过 vendor 目录
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == "testdata" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// 只处理 .go 文件，排除 _test.go
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// Run 在指定目录上跑 whale-lint 主 analyzer，返回所有诊断结果。
// 这是外部调用入口（cmd/whale-lint/main.go 与 L1Scorer 都用此方法）。
func Run(dir string) ([]Diagnostic, error) {
	var diags []Diagnostic
	pass := &Pass{
		Dir:    dir,
		Report: func(d Diagnostic) { diags = append(diags, d) },
	}
	_, err := runAll(pass)
	return diags, err
}
