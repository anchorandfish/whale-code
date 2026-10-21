// deltaF_approval_audit_test.go — delta F（工具审批决策的持久化留痕）判别性验收。
//
// 归属：delta F。与 S9 delta E「授权记忆（严格会话级、不落盘）」
// 是**两回事**：本文件同时钉住"每次决策都留痕"（delta F 要的东西）与
// "留痕**不得**被回读成授权"（delta E 的不变量不得被 delta F 破坏）。
//
// 生产入口（全部经 Model.Update 调度，**不**直调 finishAgentConfirm / 审计函数）：
//
//	app.go case agentConfirmRequestMsg → StateWaitingConfirm → handleAgentConfirmKeyMsg 子 agent 4 态
//	app.go StateWaitingConfirm 内联 switch → confirmResultMsg → case confirmResultMsg 主会话 exec/file/pipeline
//	app.go StateWaitingConfirm → handleElicitationKeyMsg → resolveElicitation MCP elicitation 3 态
//
// ⚠️ 本文件刻意**不**引用 delta F 新增的任何符号（NewApprovalAudit / ApprovalRecord …）：
// 这样同一份测试能在"before"（overlay 回退到原始码）与"after"两颗二进制上同时编译，
// 从而给出**可判别**的红/绿对照 —— before 必然红（恒为 0 条），after 必须绿。
// 审计文件路径按**契约**拼接，而不是问生产代码要（否则"路径写错"这类缺陷会被测试一起复制）。
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// deltaFAuditRelPath 是审批留痕文件的契约相对路径（相对 $HOME）。
const deltaFAuditRelPath = ".whale/audit/tool-approval.jsonl"

// deltaFAuditFile 返回指定 HOME 下的审批留痕文件。
func deltaFAuditFile(home string) string {
	return filepath.Join(home, filepath.FromSlash(deltaFAuditRelPath))
}

// deltaFAuditEntries 读取并解析审批留痕（JSONL）；文件不存在返回 nil。
func deltaFAuditEntries(t *testing.T, home string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(deltaFAuditFile(home))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读取审批留痕失败: %v", err)
	}
	body := strings.TrimRight(string(data), "\n")
	if body == "" {
		return nil
	}
	var out []map[string]any
	for _, ln := range strings.Split(body, "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("审批留痕行不是合法 JSON: %q: %v", ln, err)
		}
		out = append(out, e)
	}
	return out
}

// deltaFDecisionEntries 返回**决策侧**条目（operation=tool_approval）。
//
// 2026-10-07 起账本里还有 request 侧条目（operation=tool_approval_request，
// 确认弹窗置入待决时写一条，见 ToolModel.recordApprovalRequestTool）——
// 「一次决策恰好一条」的不变量只对决策侧成立，故断言前按 operation 过滤。
func deltaFDecisionEntries(t *testing.T, home string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range deltaFAuditEntries(t, home) {
		if e["operation"] == "tool_approval" {
			out = append(out, e)
		}
	}
	return out
}

// deltaFRequestEntries 返回**请求侧**条目（operation=tool_approval_request）。
func deltaFRequestEntries(t *testing.T, home string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range deltaFAuditEntries(t, home) {
		if e["operation"] == "tool_approval_request" {
			out = append(out, e)
		}
	}
	return out
}

// deltaFAssertRequestPaired 断言 request 侧留痕恰好 1 条、字段正确、
// 且与决策条目经 tool_input_hash 配对（pending 时长可归因的判据面）。
// 判别力：修复前不写 request 条目 ⇒ 本断言恒红；弹窗置入待决漏挂 ⇒ 同样翻红。
func deltaFAssertRequestPaired(t *testing.T, name, home, wantOrigin string, decisionEntry map[string]any) {
	t.Helper()
	reqs := deltaFRequestEntries(t, home)
	if len(reqs) != 1 {
		t.Fatalf("%s：request 侧留痕应**恰好 1 条**，实际 %d 条"+
			"（弹窗置入待决未留痕 ⇒ 「用户晚点批准 / 弹窗延迟浮现」不可归因）", name, len(reqs))
	}
	r := reqs[0]
	want := map[string]any{
		"operation":       "tool_approval_request",
		"actor":           "system",
		"decision":        "requested",
		"decision_source": "surfaced",
		"origin":          wantOrigin,
	}
	for k, v := range want {
		if got := r[k]; got != v {
			t.Errorf("%s：request 字段 %s = %v，want %v", name, k, got, v)
		}
	}
	if r["tool_input_hash"] == "" || r["tool_input_hash"] != decisionEntry["tool_input_hash"] {
		t.Errorf("%s：request 与 decision 的 tool_input_hash 未配对：%v vs %v",
			name, r["tool_input_hash"], decisionEntry["tool_input_hash"])
	}
}

// deltaFRawAudit 返回审批留痕文件的原始字节（不存在返回空）。
func deltaFRawAudit(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(deltaFAuditFile(home))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("读取审批留痕失败: %v", err)
	}
	return string(data)
}

// deltaFSendCmd 求值 Update 返回的 cmd 并回灌得到的 msg。
// 这不是"手工构造前提"——它正是 bubbletea 事件循环做的事：
// cmd() 产出 confirmResultMsg，下一轮 Update 消费它。审计挂在**消费点**，
// 跳过这一跳测到的只是半条生产路径。
func deltaFSendCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	updated, _ := m.Update(msg)
	return r3AsModel(t, updated)
}

// deltaFSubagentPending 构造处于「子 agent 确认中」状态的 Model（HOME 指向临时目录）。
//
// G-U 轮补：`t.Setenv("HOME", t.TempDir())` 后必须补**生产退出收尾**（CancelWiring），
// 否则进程级后台落盘协程会在 TempDir RemoveAll 之后仍写 `$HOME/.whale`
// ⇒ 随机 `TempDir RemoveAll cleanup: ... directory not empty`（非断言失败、时序相关）。
func deltaFSubagentPending(t *testing.T, toolName, input string) (Model, chan tools.ConfirmOutcome, string) {
	t.Helper()
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)

	m := newApprovalTestModel(t)
	guTrackModelTeardown(t, &m)
	m.reassignServices()

	replyCh := make(chan tools.ConfirmOutcome, 1)
	updated, _ := m.Update(agentConfirmRequestMsg{toolName: toolName, toolInput: input, replyCh: replyCh})
	m2 := r3AsModel(t, updated)
	if m2.toolService.PendingConfirm() == nil || m2.sessionService.State() != StateWaitingConfirm {
		t.Fatal("前置失败：子 agent 确认槽未按预期占用")
	}
	return m2, replyCh, home
}

// deltaFKey 把单字符键转成 tea.KeyMsg。'esc' 特判为 KeyEsc。
func deltaFKey(k string) tea.KeyMsg {
	if k == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// ---------------------------------------------------------------------------
// T2 · 每一次决策都必须留痕（G-f2：「批准不留痕」是本次要消灭的缺口）
// ---------------------------------------------------------------------------

// TestDeltaF_T2_SubagentDecisionsAreAudited 子 agent 路径 4 态全部留痕。
//
// 修复前：批准（y/a）与拒绝（n/esc）**都不落盘**，只有拒绝会 addMessage（内存态）；
// 进程退出后「谁批准了什么」完全无据可查。
func TestDeltaF_T2_SubagentDecisionsAreAudited(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		decision string
	}{
		{"allow-once", "y", "approved"},
		{"allow-session", "a", "approved_session"},
		{"deny", "n", "denied"},
		{"esc-cancel", "esc", "cancelled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, replyCh, home := deltaFSubagentPending(t, "bash", `{"command":"ls"}`)

			updated, _ := m.Update(deltaFKey(c.key))
			_ = r3AsModel(t, updated)

			entries := deltaFDecisionEntries(t, home)
			if len(entries) != 1 {
				t.Fatalf("delta F 失败：%s 后审批**决策**留痕应**恰好 1 条**，实际 %d 条"+
					"（修复前批准/拒绝均不落盘 ⇒ 恒为 0）", c.name, len(entries))
			}
			e := entries[0]
			want := map[string]any{
				"operation":       "tool_approval",
				"tool":            "bash",
				"decision":        c.decision,
				"decision_source": "user",
				"origin":          "tui-subagent",
				"actor":           "user",
			}
			for k, v := range want {
				if got := e[k]; got != v {
					t.Errorf("%s：字段 %s = %v，want %v", c.name, k, got, v)
				}
			}
			deltaFAssertRequestPaired(t, c.name, home, "tui-subagent", e)
			// 决策必须真的回传给子 agent（留痕不得改变原语义）
			select {
			case got := <-replyCh:
				if got.Source != tools.SourceUser {
					t.Errorf("%s：回传 Source = %q，want user", c.name, got.Source)
				}
			default:
				t.Errorf("%s：按键后 replyCh 未收到决策", c.name)
			}
		})
	}
}

// TestDeltaF_T2b_SlotGuardDecisionIsAudited 槽位占用（用户从未被询问）也必须留痕。
//
// 该出口**绕过** finishAgentConfirm 直接经 replyCh 返回（app.go 槽位守卫分支），
// 是子 agent 路径上的**第二个**决策漏斗 —— 只挂 finishAgentConfirm 会漏掉它。
func TestDeltaF_T2b_SlotGuardDecisionIsAudited(t *testing.T) {
	m, _, home := deltaFSubagentPending(t, "write_file", `{"path":"a.txt"}`)

	// 第二个请求（另一个子 agent 并发）→ 槽位已占用
	secondCh := make(chan tools.ConfirmOutcome, 1)
	updated, _ := m.Update(agentConfirmRequestMsg{toolName: "bash", toolInput: `{"command":"ls"}`, replyCh: secondCh})
	_ = r3AsModel(t, updated)

	entries := deltaFDecisionEntries(t, home)
	if len(entries) != 1 {
		t.Fatalf("槽位占用后应留痕恰好 1 条（决策侧），实际 %d 条", len(entries))
	}
	if got := entries[0]["decision_source"]; got != "slot-guard" {
		t.Errorf("decision_source = %v，want slot-guard（用户未被询问，不得写成用户拒绝）", got)
	}
	if got := entries[0]["decision"]; got != "denied" {
		t.Errorf("decision = %v，want denied", got)
	}
}

// ---------------------------------------------------------------------------
// T1 · 「只写不读」不变量（本任务独有）
// ---------------------------------------------------------------------------

// TestDeltaF_T1_AuditIsNotReadBackAsAuthorization 留痕**不得**退化成持久授权。
//
// 判据：一次 deny 之后，同工具同入参的 Policy 判定必须**仍然需要确认**。
// 若有人（或未来的某个"贴心"改动）把审计文件回读进 Policy.memory，
// needConfirm 会变成 false ⇒ 本用例翻红。
//
// 判别力证明（铁律：守卫测试必须证明可判别）：见同目录 deltaF_probe_readback_test.go
// 的 TestDeltaF_Probe_ReadbackTurnsT1Red —— 在测试进程内注入「把这条 deny 回读成
// 授权」这一反例，断言本用例的判据（needConfirm）必须翻红。若探针失效，说明本用例
// 对回读不敏感（假绿），必须重写判据。
func TestDeltaF_T1_AuditIsNotReadBackAsAuthorization(t *testing.T) {
	const tool = "bash"
	const input = `{"command":"ls"}`

	m, _, home := deltaFSubagentPending(t, tool, input)

	updated, _ := m.Update(deltaFKey("n")) // 明确拒绝
	m2 := r3AsModel(t, updated)

	if got := len(deltaFDecisionEntries(t, home)); got != 1 {
		t.Fatalf("前置失败：应留痕 1 条（决策侧），实际 %d 条", got)
	}

	pol := m2.toolService.Policy()
	if pol == nil {
		t.Fatal("前置失败：Policy 为 nil，无法验证「不得回读」")
	}
	allowed, needConfirm := pol.Check(tool, input)
	if !needConfirm {
		t.Fatalf("T1 失败：审计条目被回读成了授权（Check 返回 needConfirm=false, allowed=%v）—— "+
			"「留痕」退化为「持久授权」，S9 delta E 的严格会话级不变量被打破", allowed)
	}
}

// ---------------------------------------------------------------------------
// T3 · 主会话三态 + Esc 语义（修掉「同一 Esc 两条路径两个 Decision」）
// ---------------------------------------------------------------------------

// deltaFMainSessionPending 构造主会话内联确认态（Kind=Exec，无 AgentReplyCh）。
func deltaFMainSessionPending(t *testing.T) (Model, string) {
	t.Helper()
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)

	m := newApprovalTestModel(t)
	guTrackModelTeardown(t, &m)
	m.reassignServices()
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:  "bash",
		ToolInput: `{"command":"ls"}`,
		RiskLevel: "test",
		Kind:      ApprovalKindExec,
		Options:   buildApprovalOptions(ApprovalKindExec),
	})
	m.sessionService.SetState(StateWaitingConfirm)
	return m, home
}

// TestDeltaF_T3_MainSessionDecisionsAreAudited 主会话 exec 路径三态留痕。
//
// ★ Esc 语义修正：exec 路径的 Esc 原先发 DecisionDeny（与 elicitation 的
// DecisionCancel、子 agent 的 DecisionCancelled 三方不一致），导致**同一个物理按键**
// 在两处落出两种语义。对齐 R3/D1 已确立的「Esc = interrupt（取消），不是拒绝」后，
// exec 路径的 Esc 也必须落 cancelled。
func TestDeltaF_T3_MainSessionDecisionsAreAudited(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		decision string
	}{
		{"allow-once", "y", "approved"},
		{"allow-session", "a", "approved_session"},
		{"deny", "n", "denied"},
		{"esc-cancel", "esc", "cancelled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, home := deltaFMainSessionPending(t)

			updated, cmd := m.Update(deltaFKey(c.key))
			m2 := r3AsModel(t, updated)
			m2 = deltaFSendCmd(t, m2, cmd) // 走完 confirmResultMsg 这一跳（审计挂消费点）

			entries := deltaFDecisionEntries(t, home)
			if len(entries) != 1 {
				t.Fatalf("主会话 %s 后应留痕恰好 1 条（决策侧），实际 %d 条", c.name, len(entries))
			}
			e := entries[0]
			if got := e["decision"]; got != c.decision {
				t.Errorf("decision = %v，want %q", got, c.decision)
			}
			if got := e["origin"]; got != "tui-main" {
				t.Errorf("origin = %v，want tui-main", got)
			}
			if got := e["tool"]; got != "bash" {
				t.Errorf("tool = %v，want bash", got)
			}
			deltaFAssertRequestPaired(t, "主会话 "+c.name, home, "tui-main", e)
		})
	}
}

// ---------------------------------------------------------------------------
// T3b · MCP elicitation 三态（交接文档漏掉的第 6 个决策面）
// ---------------------------------------------------------------------------

// TestDeltaF_T3b_ElicitationDecisionsAreAudited elicitation 的 3 态必须留痕。
//
// 它的出口是 resolveElicitation → elicitationResponseMsg，**完全不经过**
// confirmResultMsg ⇒ 只挂 confirmResultMsg 时 elicitaiton 一条都不会留痕。
func TestDeltaF_T3b_ElicitationDecisionsAreAudited(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		decision string
	}{
		{"accept", "y", "approved"},
		{"decline", "n", "denied"},
		{"esc-cancel", "esc", "cancelled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := newIsolatedTestHome(t)
			t.Setenv("HOME", home)
			m := newApprovalTestModel(t)
			guTrackModelTeardown(t, &m)
			m.reassignServices()
			setElicitationPendingConfirm(&m, 0)
			m.sessionService.SetState(StateWaitingConfirm)

			updated, _ := m.Update(deltaFKey(c.key))
			_ = r3AsModel(t, updated)

			entries := deltaFDecisionEntries(t, home)
			if len(entries) != 1 {
				t.Fatalf("elicitation %s 后应留痕恰好 1 条（决策侧）"+
					"（它的出口不经 confirmResultMsg ⇒ 漏挂即恒为 0）", c.name)
			}
			e := entries[0]
			if got := e["decision"]; got != c.decision {
				t.Errorf("decision = %v，want %q", got, c.decision)
			}
			if got := e["origin"]; got != "tui-elicitation" {
				t.Errorf("origin = %v，want tui-elicitation", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T4 · 脱敏：入参里的密钥绝不允许进审计文件
// ---------------------------------------------------------------------------

// TestDeltaF_T4_AuditNeverStoresRawSecret 入参含 Anthropic key 时，落盘不得出现明文。
func TestDeltaF_T4_AuditNeverStoresRawSecret(t *testing.T) {
	const secretBody = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA"
	input := `{"command":"curl -H 'Authorization: Bearer ` + secretBody + `' https://example.com"}`

	m, _, home := deltaFSubagentPending(t, "bash", input)
	updated, _ := m.Update(deltaFKey("y"))
	_ = r3AsModel(t, updated)

	raw := deltaFRawAudit(t, home)
	if raw == "" {
		t.Fatal("前置失败：未产生审批留痕")
	}
	if strings.Contains(raw, secretBody) || strings.Contains(raw, "sk-ant-") {
		t.Fatalf("T4 失败：审计文件里出现密钥明文: %s", raw)
	}
	// 关联能力不能因此丢失：hash 必须仍在（否则留痕失去"对什么做的决策"）
	if !strings.Contains(raw, "sha256:") {
		t.Fatalf("T4 失败：脱敏后应保留 tool_input_hash 供关联: %s", raw)
	}
}
