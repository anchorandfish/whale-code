// sandbox_escalate.go — on-failure 沙箱升级的 TUI 桥（，2026-10-14）。
//
// 背景： 给 bash 命令加了「沙箱内 EPERM ⇒ 问一次用户 ⇒ 出沙箱重跑这一条」
// 的逃生门（Executor.OnSandboxEscalate）。plain 模式走终端 Y/N；本文件把它接进
// bubbletea 主线（bridge 模式）——机制与 agent_confirm.go 的子代理确认桥同款：
//
//	bridge（工具执行 goroutine 中同步阻塞）
//
// → program.Send(sandboxEscalateRequestMsg)
// → app.go 复用确认 overlay（占同一个确认槽，带单槽守卫）
// → 按键经 handleSandboxEscalateKeyMsg 决策
// → replyCh 回传 bool → executor 以 sanctioned 通道出沙箱重跑（或维持拒绝）
//
// 语义边界（与子代理确认的两点刻意差异）：
// 1. **不写 policy**——升级是逐条、不记忆的（拍板）：没有「本会话免问」档，
// 决策不落 Policy 缓存，只回传 bool。
// 2. **fail-closed 出口只有两个**（无 ctx 参数 ⇒ 无 ctx-cancel 出口）：
// program 未绑定 / 等待超时 ⇒ false（维持拒绝），且都留 canary 日志。
package tui

import (
	"log"
	"time"

	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// sandboxEscalateRequestMsg 沙箱升级询问请求（从工具执行 goroutine 发出）。
type sandboxEscalateRequestMsg struct {
	toolName string
	// command 被沙箱卡住的命令原文（面板上让用户看懂要放行的是什么）。
	command string
	// reason EPERM 现场摘录（诚实报告的输出片段）。
	reason  string
	replyCh chan bool
}

// sandboxEscalateBridge 是注入 executor.OnSandboxEscalate 的桥实现
// （接线点：lifecycle.go NewModel，紧邻 AgentConfirmFn）。
func (m *Model) sandboxEscalateBridge(toolName, command, reason string) bool {
	prog := m.program.get()
	if prog == nil {
		// fail-closed + 可观测：未绑定 = 装配缺陷，用户没被问过 ≠ 批准。
		log.Printf("[sandbox-escalate] no program bound (tool=%s): escalation overlay cannot be shown", toolName)
		return false
	}
	replyCh := make(chan bool, 1)
	prog.Send(sandboxEscalateRequestMsg{toolName: toolName, command: command, reason: reason, replyCh: replyCh})
	select {
	case approved := <-replyCh:
		return approved
	case <-time.After(agentConfirmTimeout):
		// 安全网（与子代理确认同上限）：超时 ≠ 用户拒绝，留 canary 供定位。
		log.Printf("[sandbox-escalate] timed out after %v waiting for user decision (tool=%s)", agentConfirmTimeout, toolName)
		return false
	}
}

// sandboxEscalateOptions 升级面板的两档选项（刻意只有两档：无「本会话免问」）。
func sandboxEscalateOptions() []ApprovalOption {
	return []ApprovalOption{
		{
			Label:    "Yes — rerun outside sandbox (this command only)",
			LabelZH:  "  是——沙箱外重跑（仅本条命令生效，不记忆）",
			Decision: DecisionApproveOnce,
			Shortcut: "y",
		},
		{
			Label:    "No — keep the sandbox denial",
			LabelZH:  "  否——维持沙箱拒绝",
			Decision: DecisionDeny,
			Shortcut: "n",
		},
	}
}

// handleSandboxEscalateKeyMsg 处理沙箱升级面板的按键（StateWaitingConfirm 分支入口拦截）。
// 与主会话确认流的差异（刻意，同 agent confirm）：不走 resetStreamState/StateIdle——
// 主流仍在工具执行的 goroutine 中阻塞等待回传，状态应回到 StateToolExec。
func (m *Model) handleSandboxEscalateKeyMsg(msg tea.KeyMsg, pc *PendingConfirm) (tea.Model, tea.Cmd) {
	finish := func(approved bool, outcome tools.ConfirmOutcome) {
		if ch := pc.SandboxEscalateCh; ch != nil {
			ch <- approved
		}
		m.toolService.SetPendingConfirm(nil)
		m.sessionService.SetState(StateToolExec)
		m.recalcViewportHeight()
		// delta F 纪律：两态决策都留痕（拒绝不留痕 ⇒「弹了窗却不知为何被拒」无从查起）。
		m.recordConfirmOutcome(bridge.OriginTUISandboxEscalate, pc.ToolName, pc.ToolInput, outcome)
		// 批准也给一条可见消息：出沙箱重跑是罕见路径，用户应能在 transcript 里看到它发生过。
		if approved {
			m.addMessage(toolStyle.Render("Sandbox escalation approved — rerunning outside sandbox (this command only)"))
		}
	}
	switch msg.String() {
	case "up":
		if pc.SelectIdx > 0 {
			pc.SelectIdx--
		}
		return m, nil
	case "down":
		if pc.SelectIdx < len(sandboxEscalateOptions())-1 {
			pc.SelectIdx++
		}
		return m, nil
	case "enter":
		opts := sandboxEscalateOptions()
		if pc.SelectIdx >= 0 && pc.SelectIdx < len(opts) &&
			isApproveDecision(opts[pc.SelectIdx].Decision) {
			finish(true, tools.ConfirmOutcome{Decision: tools.DecisionApproved, Source: tools.SourceUser})
			return m, nil
		}
		finish(false, tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceUser})
		return m, nil
	case "y":
		finish(true, tools.ConfirmOutcome{Decision: tools.DecisionApproved, Source: tools.SourceUser})
		return m, nil
	case "n":
		finish(false, tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceUser})
		return m, nil
	case "esc", "ctrl+c":
		// esc = interrupt（对齐 agent confirm 的 D1 语义）：不执行，但不是「用户拒绝」。
		finish(false, tools.ConfirmOutcome{Decision: tools.DecisionCancelled, Source: tools.SourceUser})
		return m, nil
	}
	// 快捷键兜底（1/2 等未来扩展；当前两档 y/n 已在上面显式处理）
	for _, option := range sandboxEscalateOptions() {
		if option.Shortcut == "" || !equalFoldKey(msg.String(), option.Shortcut) {
			continue
		}
		if isApproveDecision(option.Decision) {
			finish(true, tools.ConfirmOutcome{Decision: tools.DecisionApproved, Source: tools.SourceUser})
			return m, nil
		}
		finish(false, tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceUser})
		return m, nil
	}
	return m, nil
}
