package tui

import (
	"encoding/json"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// jsonQuote 把命令原文编码成 JSON 字符串字面量（测试夹具，避免手写转义出错）。
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// 判别器（先红）：审批面板「允许该命令前缀」选项 + 决策枚举接线。
//
// 关注点：
// · 选项**只在命令载体工具**上出现（bash / Monitor）—— 对 write_file 说
// "允许该命令前缀"没有意义，属误导；
// · 决策枚举 `DecisionApproveSessionPrefix` 被 `isApproveDecision`（SSOT）覆盖，
// 且被审计映射**显式**覆盖（不得靠 default 兜底）；
// · 生产路径：面板按 [P] → confirmResultMsg → `Policy.RememberPrefix` 建立前缀记忆。

func hasPrefixOption(opts []ApprovalOption) bool {
	for _, o := range opts {
		if o.Decision == DecisionApproveSessionPrefix && o.Shortcut == "p" {
			return true
		}
	}
	return false
}

func decisionsOf(opts []ApprovalOption) []ApprovalDecision {
	out := make([]ApprovalDecision, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Decision)
	}
	return out
}

// 命令载体 + 单一简单命令 ⇒ 有 [P]；非命令载体 / elicitation / **没有可授予的前缀** ⇒ 无 [P]。
func TestW022_PrefixOptionOnlyForCommandBearingTools(t *testing.T) {
	const simpleCmd = `{"command":"seq 1 9"}`

	for _, tool := range []string{"bash", "Monitor"} {
		opts := approvalOptionsFor(ApprovalKindPipeline, tool, simpleCmd)
		if !hasPrefixOption(opts) {
			t.Errorf("：命令载体工具 %q 的审批面板必须提供「允许该命令前缀」选项, got %v",
				tool, decisionsOf(opts))
		}
		// 顺序：[Y][A][P][N]
		got := decisionsOf(opts)
		want := []ApprovalDecision{
			DecisionApproveOnce, DecisionApproveSession, DecisionApproveSessionPrefix, DecisionDeny,
		}
		if len(got) != len(want) {
			t.Fatalf("：%q 选项数 = %d, want %d (%v)", tool, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("：%q 选项顺序 = %v, want %v", tool, got, want)
			}
		}
	}

	for _, tc := range []struct {
		kind ApprovalKind
		tool string
	}{
		{ApprovalKindFileModify, "write_file"},
		{ApprovalKindFileModify, "edit_file"},
		{ApprovalKindPipeline, "write_file"}, // pipeline 里的写类工具也不得出现
		{ApprovalKindPipeline, "git_commit"},
		{ApprovalKindMcpElicitation, "bash"}, // elicitation 恒 3 选项
	} {
		opts := approvalOptionsFor(tc.kind, tc.tool, simpleCmd)
		if hasPrefixOption(opts) {
			t.Errorf("：非命令载体（kind=%v tool=%q）不得出现前缀选项, got %v",
				tc.kind, tc.tool, decisionsOf(opts))
		}
	}
}

// ★ 诚实性：当命令**根本没有前缀**可授予时，不得显示 [P]。
//
// 复合 / 含替换 / 含重定向的命令结构性地进不了前缀范围（`cmdrules.CommandHead` 返回 ""）。
// 若此时仍显示 [P]，用户点了它**什么也不会发生** —— 在安全 UI 上给一个"看起来能放宽、
// 实际是空操作"的档位，比不显示更糟。
func TestW022_NoPrefixOptionWhenCommandHasNoHead(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"用户报的复合形态", "sleep 70 && echo final_check"}, // ← 的原始现象就属此类
		{"分号", "sleep 70 ; echo x"},
		{"管道", "sleep 70 | cat"},
		{"命令替换", "echo $(date)"},
		{"反引号", "echo `date`"},
		{"写重定向", "seq 1 9 > /tmp/x"},
		{"读重定向", "seq 1 9 < /dev/null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"command":` + jsonQuote(tc.cmd) + `}`
			opts := approvalOptionsFor(ApprovalKindPipeline, "bash", input)
			if hasPrefixOption(opts) {
				t.Fatalf("（诚实性）：%q 没有可授予的前缀，不得显示 [P]（否则是空操作）, got %v",
					tc.cmd, decisionsOf(opts))
			}
			// 基础三档仍须齐全（不能因为隐藏 [P] 而少给用户选项）
			if n := len(opts); n != 3 {
				t.Fatalf("：隐藏 [P] 后应仍为 3 档, got %d (%v)", n, decisionsOf(opts))
			}
		})
	}
}

// 基础选项集保持不变（既有测试与既有 UI 依赖 3 选项）。
func TestW022_BaseOptionsUnchanged(t *testing.T) {
	for _, k := range []ApprovalKind{ApprovalKindExec, ApprovalKindFileModify, ApprovalKindPipeline} {
		if n := len(buildApprovalOptions(k)); n != 3 {
			t.Errorf("：buildApprovalOptions(%v) 基础集应仍为 3 项, got %d", k, n)
		}
	}
	if n := len(buildApprovalOptions(ApprovalKindMcpElicitation)); n != 3 {
		t.Errorf("：elicitation 应仍为 3 项, got %d", n)
	}
}

// isApproveDecision 是「放行类决策」的 SSOT（取代散落的 `== Once || == Session`）。
func TestW022_IsApproveDecisionSSOT(t *testing.T) {
	for _, d := range []ApprovalDecision{
		DecisionApproveOnce, DecisionApproveSession, DecisionApproveSessionPrefix,
	} {
		if !isApproveDecision(d) {
			t.Errorf("：isApproveDecision(%v) = false, want true", d)
		}
	}
	for _, d := range []ApprovalDecision{DecisionDeny, DecisionCancel} {
		if isApproveDecision(d) {
			t.Errorf("：isApproveDecision(%v) = true, want false", d)
		}
	}
}

// 审计映射必须**显式**覆盖新决策，且 tools 侧 Approved() 认它。
func TestW022_AuditMappingCoversPrefix(t *testing.T) {
	got, ok := confirmDecisionOf(DecisionApproveSessionPrefix)
	if !ok {
		t.Fatal("：confirmDecisionOf 必须显式覆盖 DecisionApproveSessionPrefix（不得靠 default 兜底）")
	}
	if got != tools.DecisionApprovedSessionPrefix {
		t.Fatalf("：审计映射 = %q, want %q", got, tools.DecisionApprovedSessionPrefix)
	}
	outcome := tools.ConfirmOutcome{Decision: tools.DecisionApprovedSessionPrefix, Source: tools.SourceUser}
	if !outcome.Approved() {
		t.Fatal("：tools.ConfirmOutcome.Approved() 必须把 approved_session_prefix 视为放行")
	}
}

// collectMsgs 执行一个 tea.Cmd（展开 tea.BatchMsg）并收集它产出的全部消息。
// Update 的返回值通常是 tea.Batch(…)（其中还含光标闪烁等无关 cmd）⇒ 必须展开后筛选。
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch m := c().(type) {
		case tea.BatchMsg:
			for _, sub := range m {
				walk(sub)
			}
		case nil:
		default:
			out = append(out, m)
		}
	}
	walk(cmd)
	return out
}

// 生产路径 E2E：面板按 [P] → confirmResultMsg → Policy 前缀记忆建立。
//
// ★ 夹具纪律（本仓的"值复制陷阱"）：`newApprovalTestModel` 返回的 Model 里，
// `sessionService` 指针指向**构造时那个局部副本**的嵌入字段，而不是我手上这份的
// `SessionModel` —— 直接 `m.sessionService.SetState(...)` 会写进另一份存储，随后
// Update 顶部的 `reassignServices()` 一绑定就"复位"，表现为状态读了 0、按键落到
// textarea。⇒ 必须先 `m.reassignServices()` 把指针绑回本地副本（既有夹具
// deltaFMainSessionPending / panel_scroll_keys 同款做法）。
func TestW022_ChoosePrefixKeyWritesPolicyPrefix(t *testing.T) {
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home) // 本用例会触发审计落盘 ⇒ 必须隔离 HOME

	m := newApprovalTestModel(t)
	guTrackModelTeardown(t, &m)
	m.reassignServices()
	pol := m.toolService.Policy()
	if pol == nil {
		t.Fatal("precondition：测试 Model 应已装配 Policy")
	}

	const input = `{"command":"seq 1 9"}`
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:  "bash",
		ToolInput: input,
		RiskLevel: "moderate",
		Kind:      ApprovalKindPipeline,
		Options:   approvalOptionsFor(ApprovalKindPipeline, "bash", input),
	})
	m.sessionService.SetState(StateWaitingConfirm)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd == nil {
		t.Fatal("：按 [P] 应返回 non-nil cmd（发射 confirmResultMsg）")
	}
	var msg confirmResultMsg
	found := false
	for _, cand := range collectMsgs(cmd) {
		if cr, ok := cand.(confirmResultMsg); ok {
			msg, found = cr, true
			break
		}
	}
	if !found {
		t.Fatal("：按 [P] 应产出 confirmResultMsg")
	}
	if !msg.approved {
		t.Fatal("：[P] 属批准类决策，confirmResultMsg.approved 必须为 true")
	}
	if msg.decision != DecisionApproveSessionPrefix {
		t.Fatalf("：[P] 决策 = %v, want DecisionApproveSessionPrefix", msg.decision)
	}

	// 消费点（Update 的 confirmResultMsg 分支）应把前缀写进 Policy。
	// 注意：返回的 tea.Cmd 是**惰性**的，此处不调用 ⇒ 不会真的执行命令。
	m2 := r3AsModel(t, updated)
	m2.reassignServices()
	if _, _ = m2.Update(msg); !pol.HasCommandPrefix("bash", `{"command":"seq 1 3"}`) {
		t.Fatal("：选择 [P] 后应在 Policy 上建立 `seq` 前缀记忆（同头单一命令随之免问）")
	}
	// 反向：前缀记忆不等于全量本会话批准
	if pol.Has("bash", `{"command":"seq 1 3"}`) {
		t.Fatal("：[P] 不得退化成「允许本会话」的全量批准")
	}
}
