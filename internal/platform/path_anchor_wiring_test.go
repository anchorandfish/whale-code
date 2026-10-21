// path_anchor_wiring_test.go — 批 1 的**接线结构守卫**（R6b）。
//
// 为什么需要它：行为面测试在本机（POSIX）上测不到 Windows 分支——
// `AnchorsToWorkDir` 的 windows flavor 只有 runtime.GOOS==windows 才生效，
// 而 2026-10-13 实测并发写者把 4 处生产接线覆盖回 `filepath.IsAbs` 旧码后，
// 本机全部测试**依然绿**（假绿）。因此判据必须换**结构面（AST）**：
// 直接解析生产源码，断言四个调用点的函数体内——
// 1. `platform.AnchorsToWorkDir` 恰好命中 1 次（key 绝对路径 · 命中==1 · 否则静默假通过）；
// 2. 旧模式 `filepath.IsAbs(<target>)` 在该函数体内命中 0 次。
//
// 判别性：接线被覆盖回旧码 ⇒ 断言 1 红（命中 0）；新代码若退回 IsAbs ⇒ 断言 2 红。
package platform

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// wiringPoints 四个生产调用点（R3⑤ 同源穷举的完整名单）。
// file 相对本包测试目录（internal/platform/）；fn 为目标函数名。
var wiringPoints = []struct {
	file string
	fn   string
}{
	{"../sandbox/sandbox.go", "redirectOutsideWorkDir"},
	{"../tools/apply_diff.go", "resolvePatchTargetPath"},
	{"../tools/pipeline.go", "selfTargetedCall"},
	{"../tools/powershell/path_validation.go", "Validate"},
}

// countSelectorCalls 统计函数体内 `pkg.sel(...)` 形态的调用次数。
// 包名按 X 标识符字面比较（这些文件里 filepath/platform 都是点内名，无同名遮蔽）。
func countSelectorCalls(fn *ast.FuncDecl, pkg, sel string) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selExpr, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selExpr.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == pkg && selExpr.Sel.Name == sel {
			n++
		}
		return true
	})
	return n
}

func TestGuard_W090_AnchorWiringPresent(t *testing.T) {
	for _, wp := range wiringPoints {
		fset := token.NewFileSet()
		abs, err := filepath.Abs(wp.file)
		if err != nil {
			t.Fatalf("%s: Abs: %v", wp.file, err)
		}
		f, err := parser.ParseFile(fset, abs, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", wp.file, err)
		}
		var fn *ast.FuncDecl
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Name.Name == wp.fn {
				fn = fd
				break
			}
		}
		if fn == nil {
			t.Fatalf("%s: 找不到函数 %s（被改名/移动？请同步更新本守卫）", wp.file, wp.fn)
		}
		if got := countSelectorCalls(fn, "platform", "AnchorsToWorkDir"); got != 1 {
			t.Errorf("%s.%s: platform.AnchorsToWorkDir 命中 %d 次，want 恰好 1 ——  接线丢失（被并发覆盖回旧码？）", wp.file, wp.fn, got)
		}
		if got := countSelectorCalls(fn, "filepath", "IsAbs"); got != 0 {
			t.Errorf("%s.%s: filepath.IsAbs 命中 %d 次，want 0 —— 锚定判定退回 POSIX 直觉（ 缺陷本体）", wp.file, wp.fn, got)
		}
	}
}
