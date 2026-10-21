package permission

// capability_rule_family_test.go — 能力级 deny 规则（Read(*) 家族别名）。
//
// 背景（2026-09-13 T-B 手测）：deny 规则此前**只按工具名**匹配，读取侧没有
// 与 Edit(*) 对称的家族别名 ⇒ 配 `read_file(*)` 之后，`grep_search` 仍能静默读到
// 同一文件内容（生产路径实测见 internal/tools/deny_rule_bypass_test.go）。
// "禁止读文件"这个**用户意图**用单工具名规则表达不了。
//
// 本文件钉住修复后的契约：
// - `Read(*)` 命中读取侧全部内建工具（与 Edit(*) 对称）；
// - 边界负例：不得误伤写入侧 / 任意执行 / 外部信任域 / 非读取工具
// （否则 deny Read(*) 会产生与"读"无关的副作用）；
// ★ ：**全只读**的命令载体调用（如 `bash cat X`）**计入**读家族，
// 故不在负例内；非只读命令载体（`bash mkdir`）仍在负例内
// （双面行为由 read_effect_view_test.go 单独钉）；
// - `read_file(*)`（单工具名）语义不变 —— 精确工具名优先于家族别名；
// - `Edit(*)` 家族行为零回归。
//
// 对标同类 CLI 的能力分桶
// agents/ToolSelector.tsx:51-58（READ_ONLY = {Glob,Grep,FileRead,WebFetch,…}；
// EDIT = {FileEdit,FileWrite,NotebookEdit}）。本项目沿用同一思路，但**有意收窄**
// read 家族为「披露既有文件内容/结构」的工具，不含 TodoWrite/TaskList 等
// "非破坏性但非读取"的工具（见 capability.go 的 Capability / CapabilitiesOf 文档）。

import "testing"

// familyHit 用给定规则串匹配一次工具调用。
func familyHit(t *testing.T, rule, tool, input string) bool {
	t.Helper()
	r, err := ParseRuleString(rule, RuleDeny, SourceUserSettings)
	if err != nil {
		t.Fatalf("解析规则 %q 失败: %v", rule, err)
	}
	return r.Match(tool, input)
}

// TestReadCapabilityFamily_MatchesAllReadTools 修复目标：Read(*) 覆盖读取侧。
func TestReadCapabilityFamily_MatchesAllReadTools(t *testing.T) {
	// 期望被 Read(*) 命中的读取侧工具。
	// 判据不依赖输入内容（家族别名只在内容为 "*" 时生效，见 rules.go）。
	readTools := []string{
		"read_file",   // 直接读文件内容
		"grep_search", // ← 本次要堵的静默绕过路径
		"glob_search", // 披露文件结构
		"git_diff",    // 经 diff 披露文件内容
		"git_status",  // 披露工作区结构
		"git_log",     // 披露历史
	}
	for _, tool := range readTools {
		if !familyHit(t, "Read(*)", tool, `{"path":"/etc/shadow"}`) {
			t.Errorf("Read(*) 应命中读取侧工具 %q，实际未命中", tool)
		}
	}
}

// TestReadCapabilityFamily_DoesNotOverreach 判别力对照：
// 若实现时图省事把"非破坏性工具全集"（agent.go _readOnlyToolSet）整表复用，
// 本用例会红 —— 那会把 bash/写入侧/外部域一起 deny，产生与"读"无关的副作用。
func TestReadCapabilityFamily_DoesNotOverreach(t *testing.T) {
	mustNotMatch := []string{
		// ★ ：`bash` 已从本负例表**移出**。理由见文件头 ——
		// 当命令可判定为**全只读**时，`Read(*)` 现在**会**命中该次 bash 调用
		// （效果视图）。本表只保留"静态上就与读无关"的工具。
		"write_file",      // 写入侧
		"edit_file",       // 写入侧
		"NotebookEdit",    // 写入侧
		"TodoWrite",       // 非读取（Claude 归 READ_ONLY 是因"非破坏性"，不是"能读"）
		"TaskList",        // 非读取
		"WebFetch",        // 外部网络信任域，不是本机文件
		"WebSearch",       // 同上
		"McpReadResource", // 外部信任域
		"Sleep",           // 显然非读取
	}
	for _, tool := range mustNotMatch {
		if familyHit(t, "Read(*)", tool, `{"command":"cat /etc/shadow"}`) {
			t.Errorf("Read(*) 不得命中 %q（超出「读取本机既有内容」的能力边界）", tool)
		}
	}
}

// TestReadCapabilityFamily_ExactToolNamePrecedence 精确工具名优先：
// read_file(*) 仍是"只针对 read_file"，不被家族别名泛化。
func TestReadCapabilityFamily_ExactToolNamePrecedence(t *testing.T) {
	if !familyHit(t, "read_file(*)", "read_file", `{"path":"/tmp/a"}`) {
		t.Error("read_file(*) 必须命中 read_file")
	}
	if familyHit(t, "read_file(*)", "grep_search", `{"path":"/tmp"}`) {
		t.Error("read_file(*) 不得命中 grep_search（精确工具名语义）")
	}
	// 裸工具名（无内容）不是家族别名：与 Edit 既有语义一致。
	if familyHit(t, "Read", "read_file", `{"path":"/tmp/a"}`) {
		t.Error("裸 Read（无 *）不应按家族匹配，只应按工具名精确匹配")
	}
}

// TestReadCapabilityFamily_EditFamilyUnchanged Edit(*) 零回归。
func TestReadCapabilityFamily_EditFamilyUnchanged(t *testing.T) {
	cases := []struct {
		tool string
		want bool
	}{
		{"write_file", true}, {"edit_file", true}, {"NotebookEdit", true},
		{"read_file", false}, {"bash", false},
	}
	for _, c := range cases {
		if got := familyHit(t, "Edit(*)", c.tool, `{"path":"/tmp/a"}`); got != c.want {
			t.Errorf("Edit(*) 匹配 %q = %v，期望 %v（既有语义不得回归）", c.tool, got, c.want)
		}
	}
}

// TestReadCapabilityFamily_Negation `!(Read(*))` 取反语义：
// 命中"不在读取家族里"的工具 —— 用于表达「只允许非读取工具」。
func TestReadCapabilityFamily_Negation(t *testing.T) {
	if familyHit(t, "!(Read(*))", "read_file", `{"path":"/tmp/a"}`) {
		t.Error("!(Read(*)) 不应命中 read_file（它在家族内）")
	}
	if familyHit(t, "!(Read(*))", "grep_search", `{"path":"/tmp"}`) {
		t.Error("!(Read(*)) 不应命中 grep_search（它在家族内）")
	}
	// ★ ：`ls` 是只读命令 ⇒ 该次 bash 调用现在**计入**读家族 ⇒
	// `!(Read(*))` 不再命中它。改用**非只读**样本保持本用例原意（"不在读家族内的工具"）。
	if !familyHit(t, "!(Read(*))", "bash", `{"command":"mkdir -p /tmp/w025"}`) {
		t.Error("!(Read(*)) 应命中非只读 bash（不在读取家族内）")
	}
	// 对称面：只读 bash 计入读家族 ⇒ !(Read(*)) 不得命中它。
	if familyHit(t, "!(Read(*))", "bash", `{"command":"cat /etc/shadow"}`) {
		t.Error("!(Read(*)) 不应命中只读 bash（ 效果视图后它计入读家族）")
	}
}
