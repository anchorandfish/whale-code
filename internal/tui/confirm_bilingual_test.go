// confirm_bilingual_test.go — 决策选择面双语文案守卫。
//
// 守两个**活线**弹窗的形态（全仓 SSOT = util.Bi：英文主行 + 缩进中文辅行，
// 含汉字的行必须以 CopyIndent 开头）：
//
//	A. 确认/审批弹窗（renderLegacyConfirmUI —— StateWaitingConfirm 的唯一渲染面）；
//	B. 方案确认弹窗（renderChoiceUI —— handleOrchContractRequest 的生产出口）。
//
// ★ 盒式渲染器例外（技能 §4）：lipgloss Border+Padding 让每行前面都有边框与内边距，
// 「整行以 copyIndent 开头」会误红 ⇒ 等价判据 = **中文之前的那一段以 copyIndent 结尾**。
// ★ 判别力自证：改造前的真实框体文案（行内并列）必须被判违规；反例文本取自
// 内部工作回执 回执里留存的改前原文，不是臆造。
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/orchestrator"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/util"
)

// boxedChineseIndentViolation 盒式渲染器的形态判据：含汉字的行，
// **第一个汉字之前的那一段**必须以 CopyIndent 结尾（空 = 合规）。
// 判据逻辑自己写（不调生产的 AssertChineseOnlyOnIndentedLines —— 共享 bug ⇒ 假绿），
// 汉字判定走 util.HasHan（字符集同源）。
func boxedChineseIndentViolation(out string) string {
	for _, line := range strings.Split(out, "\n") {
		idx := strings.IndexFunc(line, func(r rune) bool { return util.HasHan(string(r)) })
		if idx < 0 {
			continue
		}
		if !strings.HasSuffix(line[:idx], util.CopyIndent) {
			return line
		}
	}
	return ""
}

// TestW083B1_ConfirmDialogPassesFormGate 审批/确认弹窗（活线 renderLegacyConfirmUI）：
// 框体文案 + 选项 + Risk 行（管线拼好的双语串）全部过形态门；
// 且英文主行要素齐全（标题 / 选项主行 / 框架说明）。
func TestW083B1_ConfirmDialogPassesFormGate(t *testing.T) {
	m := newScreenSelectionFixture(t, 120, 30, 6)
	// RiskLevel 用生产形态的双语串（tools/pipeline.go 的产物）
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:  "bash",
		ToolInput: `{"command":"rm file.txt"}`,
		RiskLevel: "destructive operation detected: bash rm (Destructive-tier command name) (user confirmation required)\n" +
			util.CopyIndent + "检测到破坏性操作：bash rm（破坏性档命令名）（需要用户确认）",
		Kind: ApprovalKindPipeline,
	})
	m.sessionService.SetState(StateWaitingConfirm)

	out := ansi.Strip(m.renderConfirmUI())
	if v := boxedChineseIndentViolation(out); v != "" {
		t.Errorf("确认弹窗形态违规行: %q\n全量:\n%s", v, out)
	}
	for _, want := range []string{"Safety confirmation", "Operation: Run command", "Command: rm file.txt",
		"Decision flow:", "Approval scope:", "Risk:", "[Y] Allow once", "[A] Allow for this session",
		"[N] Deny, do not run", "[P] Allow this command prefix"} {
		if !strings.Contains(out, want) {
			t.Errorf("确认弹窗应含英文要素 %q\n全量:\n%s", want, out)
		}
	}
	// 中文辅行要素（缩进后出现）
	for _, want := range []string{"安全确认", "允许本次", "允许本会话", "拒绝，不执行", "允许该命令前缀", "检测到破坏性操作"} {
		if !strings.Contains(out, want) {
			t.Errorf("确认弹窗应含中文辅行要素 %q\n全量:\n%s", want, out)
		}
	}
}

// TestW083B1_ContractDialogPassesFormGate 方案确认弹窗：走**生产入口**
// handleOrchContractRequest（orch_bridge.go 的活线），验证选项与渲染产物。
func TestW083B1_ContractDialogPassesFormGate(t *testing.T) {
	m := newScreenSelectionFixture(t, 120, 30, 6)
	plan := &orchestrator.GeneratedPlan{
		SubTasks: []orchestrator.SubTaskDef{
			{SubTaskID: "T1", Type: "code", Complexity: "T2", FilesToRead: []string{"a.go"}},
		},
	}
	req := orchestrator.ContractRequest{
		Plan:   plan,
		RespCh: make(chan orchestrator.ContractResponse, 1),
	}
	m.handleOrchContractRequest(OrchContractRequestMsg{Request: req})

	pc := m.toolService.PendingChoice()
	if pc == nil {
		t.Fatal("前置不成立：handleOrchContractRequest 应设置 PendingChoice（否则本判据不可判别）")
	}
	// ① 选项数据面：Label 英文主行（无汉字）+ DescriptionZH 中文辅行（有汉字）；
	// Value 逐字不变（ParseContractAnswer 按 Value 匹配 —— 功能不受文案影响）。
	wantValues := []string{"accept", "reject", "modify"}
	if len(pc.Options) != 3 {
		t.Fatalf("应有 3 个选项，got %d", len(pc.Options))
	}
	for i, opt := range pc.Options {
		if opt.Value != wantValues[i] {
			t.Errorf("选项 %d 的 Value 必须逐字不变：got %q want %q", i, opt.Value, wantValues[i])
		}
		if util.HasHan(opt.Label) {
			t.Errorf("选项 %d 的 Label（英文主行）不得含汉字：%q", i, opt.Label)
		}
		if opt.DescriptionZH == "" || !util.HasHan(opt.DescriptionZH) {
			t.Errorf("选项 %d 应有中文辅行 DescriptionZH：%q", i, opt.DescriptionZH)
		}
	}
	// ② 提问首行英文主行
	if !strings.HasPrefix(pc.Prompt, "Please confirm the plan:") {
		t.Errorf("Prompt 首行应为英文主行：%q", strings.SplitN(pc.Prompt, "\n", 2)[0])
	}
	// ③ 渲染产物过盒式形态门
	out := ansi.Strip(m.renderChoiceUI())
	if v := boxedChineseIndentViolation(out); v != "" {
		t.Errorf("方案确认弹窗形态违规行: %q\n全量:\n%s", v, out)
	}
	for _, want := range []string{"Accept plan", "Reject plan", "Modify plan", "Subtask plan (1 subtasks):"} {
		if !strings.Contains(out, want) {
			t.Errorf("方案确认弹窗应含英文要素 %q\n全量:\n%s", want, out)
		}
	}
}

// TestW083B1_BoxedFormGateCatchesCounterExample 判别力自证：改造前的真实文案
// （确认框行内并列 / 纯中文选项）必须被盒式判据抓到。
func TestW083B1_BoxedFormGateCatchesCounterExample(t *testing.T) {
	olds := []string{
		// 改前 renderLegacyConfirmUI 框体（行内并列）
		"│  Safety confirmation / 安全确认",
		"│  Risk / 风险: destructive operation detected: bash rm (破坏性档命令名) (user confirmation required)",
		"│  Decision flow / 判定流程: permission approval first, then sandbox and network checks / 先审批权限，再检查沙箱和网络",
		"│   [Y] Allow once / 允许本次",
		// 改前 approvalTargetSummary（行内并列）
		"│  Operation / 操作: Run command / 执行命令",
		"│  Command / 命令: rm file.txt",
		// 注：改前的纯中文选项（"接受方案"）**不在**本判据的射程内 —— 选项行本身
		// 带 2 空格缩进，与辅行缩进同形 ⇒ 盒式判据分不清「缩进的中文标签」与
		// 「缩进的中文辅行」。纯中文标签由**数据面**判据抓
		// （TestW083B1_ContractDialogPassesFormGate ①：Label 不得含汉字）。
	}
	for _, old := range olds {
		if v := boxedChineseIndentViolation(old); v == "" {
			t.Errorf("改前文案必须被判违规：%q", old)
		}
	}
	// 对照：合规形态不得误红
	oks := []string{
		"│  Safety confirmation",
		"│     安全确认",
		"│   [Y] Allow once",
		"│      允许本次",
		"│  Risk: destructive operation detected: bash rm (Destructive-tier command name) (user confirmation required)",
		"│     检测到破坏性操作：bash rm（破坏性档命令名）（需要用户确认）",
	}
	for _, ok := range oks {
		if v := boxedChineseIndentViolation(ok); v != "" {
			t.Errorf("合规形态不得误红：%q", ok)
		}
	}
}

// TestW083B2_ConfirmDialogHeightBudget 小终端高度预算守卫。
//
// 背景：B1 把确认弹窗每行都加中文辅行 ⇒ 框体 25 行；30 行终端 + 5 行状态栏 ⇒
// lastInputAreaTop=0，输入区被顶出屏幕（既有面板夹具红，A/B 证实本次引入，
// 见 内部工作回执 回执 §5）。B' 把辅行收拢到决策要素（标题/选项/
// Risk 内容）后框体 20 行。本守卫防的是**同类复发**：今后任何给弹窗加行的改动
// （再加辅行、加选项、加说明）超过小终端预算即红。
//
// 判据落在**生产量**上：生产入口（SetPendingConfirm/SetState 拦截点 +
// recalcViewportHeight），断言 lastInputAreaTop。参数 = 仓库既有小终端代理
// （panelScrollFixtureHeight=30，panel_scroll_keys_test.go:51）+ 面板夹具同宽 100。
//
// **分层阈值**（不追求所有形态都 >= 3，理由见下）：
// - 基础形态 >= 3：3 是 recalcViewportHeight 的视口钳位下限，>= 3 ⟺ 弹窗+状态栏+
// 视口恰好装满终端、不被 bubbletea 截顶 ⇒ 稳态下有真视口；
// - 应力形态 >= 1：双语 Risk（内容驱动，换行 3 行）与等待计时（transient +2 行）
// 是内容/瞬态高度，为其砍空行会伤及**所有**状态的可扫读性（安全对话框的标题/
// 摘要/选项之间的呼吸位）。功能底线 = 输入区可见（> 0）⇒ 应力形态只守 1。
//
// 判别性自证：B1 全辅行版在基础形态下必红（25+5=30 ⇒ lastInputAreaTop=0）。
func TestW083B2_ConfirmDialogHeightBudget(t *testing.T) {
	pipelineRisk := "destructive operation detected: bash rm (Destructive-tier command name) (user confirmation required)\n" +
		util.CopyIndent + "检测到破坏性操作：bash rm（破坏性档命令名）（需要用户确认）"
	cases := []struct {
		name   string
		minTop int
		apply  func(t *testing.T, m *Model)
	}{
		{"exec-production-options", 3, func(t *testing.T, m *Model) {
			// 选项走生产构造器（approvalOptionsFor：含 [P] 前缀项 = 基础形态最坏）
			m.toolService.SetPendingConfirm(&PendingConfirm{
				ToolName:  "bash",
				ToolInput: `{"command":"rm file.txt"}`,
				RiskLevel: "moderate",
				Kind:      ApprovalKindExec,
				Options:   approvalOptionsFor(ApprovalKindExec, "bash", `{"command":"rm file.txt"}`),
			})
			m.sessionService.SetState(StateWaitingConfirm)
		}},
		{"file-modify-with-content", 3, func(t *testing.T, m *Model) {
			m.toolService.SetPendingConfirm(&PendingConfirm{
				ToolName:  "write_file",
				ToolInput: `{"path":"a.txt","content":"curl https://example.com"}`,
				RiskLevel: "file-modify",
				Kind:      ApprovalKindFileModify,
				Options:   approvalOptionsFor(ApprovalKindFileModify, "write_file", `{"path":"a.txt","content":"curl https://example.com"}`),
			})
			m.sessionService.SetState(StateWaitingConfirm)
		}},
		{"exec-pipeline-bilingual-risk", 1, func(t *testing.T, m *Model) {
			// 应力：RiskLevel 用生产形态的双语串（tools/pipeline.go 的产物，换行 3 行）
			m.toolService.SetPendingConfirm(&PendingConfirm{
				ToolName:  "bash",
				ToolInput: `{"command":"rm file.txt"}`,
				RiskLevel: pipelineRisk,
				Kind:      ApprovalKindPipeline,
			})
			m.sessionService.SetState(StateWaitingConfirm)
		}},
		{"exec-waiting-timer", 1, func(t *testing.T, m *Model) {
			m.toolService.SetPendingConfirm(&PendingConfirm{
				ToolName:  "bash",
				ToolInput: `{"command":"rm file.txt"}`,
				RiskLevel: "moderate",
				Kind:      ApprovalKindExec,
				Options:   approvalOptionsFor(ApprovalKindExec, "bash", `{"command":"rm file.txt"}`),
			})
			m.sessionService.SetState(StateWaitingConfirm)
			// 应力：「等，但出声」等待计时行（biSame 两行）只在等待期出现
			m.approvalWaitSecs = 2
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newScreenSelectionFixture(t, 100, panelScrollFixtureHeight, 6)
			c.apply(t, m)
			m.recalcViewportHeight()
			if m.lastInputAreaTop < c.minTop {
				t.Fatalf("确认弹窗超出小终端（%d 行）高度预算：lastInputAreaTop=%d < %d\n"+
					"（功能底线 = 输入区可见 > 0；稳态还应 >= 3 免截顶。加行改动请走 B' 收拢辅行或接受本红）\n框体:\n%s",
					panelScrollFixtureHeight, m.lastInputAreaTop, c.minTop, ansi.Strip(m.renderConfirmUI()))
			}
		})
	}
}

// TestW083B2_BlockedMessagesPassFormGate 拦截/拒绝类消息（活线， 第二小队）。
//
// 覆盖四条生产路径的输出（全部过**生产形态门** util.AssertChineseOnlyOnIndentedLines）：
//
// - abortRestOfBatch（批内短路占位；executeTools 与 executePipelineRemaining 共用）；
// - pipelineRejectionResult（策略拒绝；Pipeline != nil 的生产路径 tool_exec.go:340）；
// - completeBlockedTurn（终局拒绝轮次解释；沙箱/权限两种，真方法 + 读回 session 最后一条）；
// - plainConfirmPrompt（--ci 模式确认提示，plain.go OnConfirm 闭包的生产文案）。
//
// 判别性自证：改前原文（行内并列 / 无缩进中文段）必须被判红。
// 不在本队范围：tool_exec.go:64/87 的同类文案在 `Pipeline == nil` 回退分支里 ——
// 生产三入口（lifecycle.go:533 / runner_core.go:552 / bridge.go:274）都调
// InitPipelineWithHooks ⇒ 该分支生产不可达（P-002 已定性），按纪律不改死码。
func TestW083B2_BlockedMessagesPassFormGate(t *testing.T) {
	gate := func(name, out string) {
		t.Helper()
		if v := util.AssertChineseOnlyOnIndentedLines(out); v != "" {
			t.Errorf("%s 形态违规行: %q\n全量:\n%s", name, v, out)
		}
	}

	// (1) abortRestOfBatch：一批 2 条，从第 1 条起短路 ⇒ 只补 1 条占位。
	calls := []session.ContentBlock{
		{ID: "call-0", Name: "bash"},
		{ID: "call-1", Name: "bash"},
	}
	results := abortRestOfBatch(nil, calls, 1, "call-0")
	if len(results) != 1 || results[0].ID != "call-1" {
		t.Fatalf("夹具不健全：abortRestOfBatch 应只补 call-1，got %+v", results)
	}
	gate("abortRestOfBatch", results[0].Output)

	// (2) pipelineRejectionResult：策略拒绝形态（FailureKind=PolicyDenied）。
	rej := pipelineRejectionResult(
		session.ContentBlock{ID: "call-x", Name: "bash"},
		&tools.PipelineResult{
			FailureKind: tools.FailureKindPolicyDenied,
			Rejection:   "denied by rule rm-rf-root",
		})
	gate("pipelineRejectionResult", rej.Output)

	// (3) completeBlockedTurn：真方法 + 读回 session 最后一条 assistant 消息。
	for _, kind := range []tools.FailureKind{tools.FailureKindSandboxDenied, tools.FailureKindPolicyDenied} {
		m := newScreenSelectionFixture(t, 120, 30, 6)
		m.completeBlockedTurn(kind)
		msgs := m.sessionService.CurrentSession().GetMessages()
		if len(msgs) == 0 {
			t.Fatalf("夹具不健全：completeBlockedTurn 后 session 应有消息")
		}
		last := msgs[len(msgs)-1]
		if last.Role != session.RoleAssistant || len(last.Blocks) == 0 {
			t.Fatalf("夹具不健全：最后一条应为 assistant 文本消息，got role=%v blocks=%d", last.Role, len(last.Blocks))
		}
		gate("completeBlockedTurn/"+string(kind), last.Blocks[0].Text)
	}

	// (4) plainConfirmPrompt：--ci 模式确认提示（含 approvalTargetSummary 英文单行摘要）。
	gate("plainConfirmPrompt", plainConfirmPrompt("bash", `{"command":"rm file.txt"}`))

	// (5) 判别性自证：改前原文必须被红（否则本守卫是假绿）。
	oldForms := []string{
		"Not run / 未执行\n这一批已在前一条调用（call-0）被拒绝后终止；本调用既未被询问，也未被执行。",
		"Permission denied / 权限拒绝\nTool / 工具: bash\nReason / 原因: denied by rule\nNo operation was run / 操作未执行",
		"The command was blocked by the active sandbox. No command was run.\n\n命令已被当前沙箱拦截，未执行。",
		"Permission denied. No operation was run.\n\n权限拒绝。操作未执行。",
		"\nSafety confirmation / 安全确认\nOperation: Run command\nAllow once or deny / 允许本次或拒绝\n",
	}
	for _, old := range oldForms {
		if v := util.AssertChineseOnlyOnIndentedLines(old); v == "" {
			t.Errorf("改前原文必须被判违规（判别性失效）: %q", old)
		}
	}
}
