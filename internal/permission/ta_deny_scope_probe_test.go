package permission

import (
	"testing"
)

// TestProbe_S4_TA_DenyReadFileScope 是 2026-09-13 TUI 手测（T-A/T-B 混合场景）的现场探针。
//
// 用户现场：配了「读文件禁止」（read_file deny），6 个子 agent 里 5 个被拒、
// 1 个自称「改用替代工具」并成功读出 README 第一行（内容经核对为真：# Whale Code）。
//
// 本探针钉住一个事实：**deny 规则的作用域是「工具名」，不是「能力」**。
// 因此 deny read_file 之后，grep_search / glob_search / bash 等同样具备「读取文件内容」
// 能力的工具仍被放行 —— 这就是那 1 个子 agent 能读出来的原因。
//
// 对照：写入侧在 rules.go:174-182 有一个 `Edit(*)` 家族别名，一条规则覆盖
// write_file/edit_file/notebookedit；**读取侧没有对称的家族别名**。
func TestProbe_S4_TA_DenyReadFileScope(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	rs := NewRuleSet()
	rs.Add(PermissionRule{
		ToolName:    "read_file",
		RuleContent: "*",
		Behavior:    RuleDeny,
		Source:      SourceProjectSettings,
	})
	p.SetRuleSet(rs)

	// 各工具的「读取文件内容」能力与策略判定
	cases := []struct {
		tool    string
		input   string
		canRead string // 该工具是否具备读出文件内容的能力
	}{
		{"read_file", `{"path":"README.md"}`, "yes"},
		{"grep_search", `{"pattern":"#","path":"README.md"}`, "yes"},
		{"glob_search", `{"pattern":"README.md"}`, "no(仅列名)"},
		{"bash", `{"command":"head -1 README.md"}`, "yes"},
		{"git_diff", `{"path":"README.md"}`, "yes"},
	}

	denied := 0
	for _, c := range cases {
		allowed, needConfirm := p.Check(c.tool, c.input)
		t.Logf("tool=%-12s canRead=%-10s allowed=%-5v needConfirm=%v", c.tool, c.canRead, allowed, needConfirm)
		if !allowed {
			denied++
		}
	}

	t.Logf("tally: denied=%d/5 (只有 read_file 应被拒)", denied)
	t.Logf("结论：deny read_file(*) 的拦截面 = 1/5；bash 与 grep_search 是完全等价的读取逃逸口")

	// 探针前提自检：被点名 deny 的工具必须真的被拒，否则探针本身失效
	if allowed, _ := p.Check("read_file", `{"path":"README.md"}`); allowed {
		t.Fatalf("探针前提破：read_file 未被 deny 规则拒绝，说明规则系统未生效")
	}
	// 判据（当前行为，表征）：读取家族里除 read_file 外全部放行
	if allowed, _ := p.Check("grep_search", `{"pattern":"#","path":"README.md"}`); !allowed {
		t.Fatalf("行为已变：grep_search 现在也被拒了 —— 读取家族别名可能已被补上，请复核本探针结论")
	}
}

// TestProbe_S4_TA_DenyRuleFamilyAsymmetry 对比写入侧家族别名与读取侧缺失。
func TestProbe_S4_TA_DenyRuleFamilyAsymmetry(t *testing.T) {
	editRule := PermissionRule{
		ToolName: "Edit", RuleContent: "*", Behavior: RuleDeny, Source: SourceProjectSettings,
	}
	readRule := PermissionRule{
		ToolName: "Read", RuleContent: "*", Behavior: RuleDeny, Source: SourceProjectSettings,
	}

	in := `{"path":"a.txt"}`
	for _, tool := range []string{"write_file", "edit_file", "NotebookEdit", "read_file", "grep_search"} {
		t.Logf("Edit(*) matches %-12s = %-5v | Read(*) matches %-12s = %v",
			tool, editRule.Match(tool, in), tool, readRule.Match(tool, in))
	}

	if !editRule.Match("edit_file", in) {
		t.Fatalf("探针前提破：Edit(*) 家族别名应覆盖 edit_file")
	}
	if readRule.Match("read_file", in) {
		t.Logf("注意：Read(*) 别名已存在 —— 读取侧不对称性已被修复")
	} else {
		t.Logf("确认不对称：写入侧有 Edit(*) 家族别名，读取侧无等价 Read(*) —— deny read_file(*) 覆盖 1 个工具，deny Edit(*) 覆盖 3 个")
	}
}
