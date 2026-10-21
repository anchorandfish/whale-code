package tui

// skill_trust_wiring_gate_test.go — /skill trust 两侧接线存在性结构门（2026-10-11，§2 选项 a）。
//
// 为什么是 AST 结构门：TUI Model 侧的回调接线发生在巨型 NewModel 构造器内部（lifecycle.go），
// 无法用轻量夹具端到端驱动；按 R4-7 换结构面 —— 在目标函数体内断言 OnSkillManage 赋值存在。
// 本门是"正向存在性"断言，天然可判别：删掉任一侧接线 ⇒ 红。
//
// 覆盖两侧（：新增命令两侧都要验，漏一侧 = 半修）：
// - runner_core.go registerCommonCommands（覆盖 runnerCore 系：TUI runner / plain / bridge）
// - lifecycle.go NewModel（覆盖 TUI Model 自身的 cmdRegistry）

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// assertOnSkillManageWired 断言目标文件中存在对 OnSkillManage 的赋值，且落在指定函数体内。
func assertOnSkillManageWired(t *testing.T, file, funcName, selectorPrefix string) {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var found bool
	var foundPos token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != funcName {
			return true
		}
		ast.Inspect(fd, func(inner ast.Node) bool {
			as, ok := inner.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				// 接收者形态不钉死：生产里字段挂在 cmdRegistry 上（rc.CmdRegistry.OnSkillManage /
				// m.cmdRegistry.OnSkillManage），判的是「函数体内存在 OnSkillManage 赋值」这一事实，
				// 不是接收者拼写 —— 删掉任一侧接线仍然红（判别性不变）。
				if sel.Sel.Name == "OnSkillManage" {
					found = true
					foundPos = as.Pos()
				}
			}
			return true
		})
		return true
	})

	if !found {
		t.Fatalf("skill trust 出口断线：%s 的 %s 内没有 %s.OnSkillManage 赋值 —— 拦截文案指向的命令在该入口不可达（幽灵命令复发）", file, funcName, selectorPrefix)
	}
	_ = foundPos
}

func TestSkillTrustWiredInRegisterCommonCommands(t *testing.T) {
	assertOnSkillManageWired(t, "runner_core.go", "registerCommonCommands", "rc")
}

func TestSkillTrustWiredInNewModel(t *testing.T) {
	assertOnSkillManageWired(t, "lifecycle.go", "NewModel", "m")
}
