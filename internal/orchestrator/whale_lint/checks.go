// checks.go — whale-lint 6 个子检查器实现（go/ast 扫描，地基层 §2.3）。
// 每个检查器导出一个 Check<Name>(pass, file) 函数，由 runAll 逐文件调用。

package whale_lint

import (
	"go/ast"
	"strings"
)

// CheckErrorPath 检查测试文件是否有 error path（assert.Error/require.Error）。
// 双指标（地基层 §4.4）：① 是否存在 error path ② error path 占比。
// 此函数扫描源码文件（非 test），检查是否有 error 处理逻辑（保守：有 recover/recover() 即算）。
// 注意：真正的 test 文件 error path 检查由 L1Scorer.testCoverageScore 负责（基于文件名）。
func CheckErrorPath(pass *Pass, file *ast.File) {
	// 扫描函数声明，检查是否有 error 返回值处理
	hasErrorHandling := false
	ast.Inspect(file, func(n ast.Node) bool {
		// 检查 if err != nil 模式
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// 简化：if 条件含 "err" 标识符即视为 error path
		if containsIdent(ifStmt.Cond, "err") {
			hasErrorHandling = true
		}
		return true
	})

	// 如果文件有函数返回 error 但无 error 处理，报告
	hasErrorReturn := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil || fn.Type.Results == nil {
			continue
		}
		for _, field := range fn.Type.Results.List {
			if isErrorType(field.Type) {
				hasErrorReturn = true
				break
			}
		}
	}

	if hasErrorReturn && !hasErrorHandling {
		pos := pass.Fset.Position(file.Pos())
		pass.Report(Diagnostic{
			Pos:     pos,
			Message: "missing error path: function returns error but no error handling found",
		})
	}
}

// CheckTODO 检查交付代码中 TODO/FIXME（地基层 §2.3，0=满分）。
func CheckTODO(pass *Pass, file *ast.File) {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			text := strings.ToLower(c.Text)
			if strings.Contains(text, "todo") || strings.Contains(text, "fixme") {
				pos := pass.Fset.Position(c.Pos())
				pass.Report(Diagnostic{
					Pos:     pos,
					Message: "TODO/FIXME found in delivery code: " + strings.TrimSpace(c.Text),
				})
			}
		}
	}
}

// CheckDocCoverage 检查公开符号 godoc 覆盖率（地基层 §2.3）。
// 公开符号（首字母大写）的 FuncDecl/GenDecl 必须有紧邻的 godoc 注释。
func CheckDocCoverage(pass *Pass, file *ast.File) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// 跳过非公开函数（首字母小写）
			if d.Name == nil || !d.Name.IsExported() {
				continue
			}
			// 检查是否有 godoc（紧邻函数声明的注释组）
			if d.Doc == nil || strings.TrimSpace(d.Doc.Text()) == "" {
				pos := pass.Fset.Position(d.Pos())
				name := ""
				if d.Name != nil {
					name = d.Name.Name
				}
				pass.Report(Diagnostic{
					Pos:     pos,
					Message: "missing godoc for exported function: " + name,
				})
			}
		case *ast.GenDecl:
			// 检查 type/var/const 公开声明
			if d.Doc == nil || strings.TrimSpace(d.Doc.Text()) == "" {
				// 仅检查含公开 TypeSpec 的 GenDecl
				hasExported := false
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name != nil && ts.Name.IsExported() {
						hasExported = true
						break
					}
				}
				if hasExported {
					pos := pass.Fset.Position(d.Pos())
					pass.Report(Diagnostic{
						Pos:     pos,
						Message: "missing godoc for exported type declaration",
					})
				}
			}
		}
	}
}

// CheckCitation 检查注释中 file:line 引用比例（地基层 §2.3）。
// Phase 0 简化：仅统计注释中含 file:line 模式的数量，不强制阈值（Phase 1 接入完整实现）。
func CheckCitation(pass *Pass, file *ast.File) {
	// Phase 0 占位：不报告诊断，仅统计（地基层 §2.3 引用规范 6% 权重）
	// Phase 1 接入完整实现：检查公开符号注释中是否含 /REF-外部 标签
	_ = file
}

// CheckNoLint 检查裸 //nolint（禁裸 nolint，必须带 linter 名+解释，地基层 §2.3）。
// 合法格式：//nolint:errcheck // reason
// 非法格式：//nolint（无 linter 名或无解释）
func CheckNoLint(pass *Pass, file *ast.File) {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			text := c.Text
			// 检测 //nolint 或 /*nolint*/
			if !strings.Contains(text, "nolint") {
				continue
			}
			// 合法格式：//nolint:lintername // reason
			// 非法格式：//nolint（裸 nolint，无冒号+linter 名）
			if isBareNolint(text) {
				pos := pass.Fset.Position(c.Pos())
				pass.Report(Diagnostic{
					Pos:     pos,
					Message: "bare //nolint forbidden: must specify linter name and reason (e.g. //nolint:errcheck // reason)",
				})
			}
		}
	}
}

// isBareNolint 判断是否为裸 nolint（无 linter 名或无解释）。
// 合法：//nolint:errcheck // reason / //nolint:errcheck
// 非法：//nolint / //nolint //
func isBareNolint(text string) bool {
	// 去除注释前缀 // 或 /*
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(t, "//")
	t = strings.TrimPrefix(t, "/*")
	t = strings.TrimSuffix(t, "*/")
	t = strings.TrimSpace(t)

	// 必须以 nolint: 开头（含 linter 名）
	if !strings.HasPrefix(t, "nolint:") {
		// 如果只是 "nolint" 或 "nolint // reason"（无冒号），视为裸 nolint
		if strings.HasPrefix(t, "nolint") {
			return true
		}
		return false
	}
	return false
}

// CheckPanicInLib 检查 internal/ 下 panic 调用（地基层 §2.3）。
// lib 层（internal/）禁止 panic，必须返回 error。
func CheckPanicInLib(pass *Pass, file *ast.File) {
	// 检查文件路径是否在 internal/ 下
	filename := pass.Fset.Position(file.Pos()).Filename
	if !strings.Contains(filename, "internal") {
		return // 非 internal/ 文件不检查
	}

	// 扫描 panic() 调用
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "panic" {
			pos := pass.Fset.Position(call.Pos())
			pass.Report(Diagnostic{
				Pos:     pos,
				Message: "panic() forbidden in internal/ library code: must return error instead",
			})
		}
		return true
	})
}

// ============ 辅助函数 ============

// containsIdent 检查表达式树中是否含指定名称的标识符。
func containsIdent(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// isErrorType 判断类型表达式是否为 error 接口。
func isErrorType(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "error"
}
