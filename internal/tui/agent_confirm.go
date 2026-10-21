// agent_confirm.go — 子代理工具确认的 TUI 桥（G4，2026-09-08 真实冒烟缺口）。
// 背景：子代理（Agent 工具内部 AgentContext）在主会话工具执行的 tea.Cmd goroutine
// 中同步运行，无法复用主会话的 pause-resume 确认流（那是主循环逐步骤暂停）。
// 桥接方式：executor.AgentConfirmFn 把 NeedConfirm 请求经 program.Send 转为
// agentConfirmRequestMsg → TUI 复用现有 PendingConfirm overlay 弹确认 →
// 用户决策经 replyCh 回传 → 子代理继续（Approved=true 重跑 pipeline）。
// 语义对标同类 CLI subagent：子代理的敏感操作路由给用户审批而非静默拒绝。
package tui

import (
	"context"
	"log"
	"time"

	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// agentConfirmTimeout 是子代理确认等待的安全网上限（防御 program.Send 消息
// 丢失等异常）。常规路径下无需等待到此值：父任务 Run 自身的 Timeout（默认
// 120s）先经 ctx.Done 触发返回。超时视为拒绝（fail-closed）。
const agentConfirmTimeout = 10 * time.Minute

// agentConfirmRequestMsg 子代理工具确认请求（从 AgentConfirmFn goroutine 发出）。
type agentConfirmRequestMsg struct {
	toolName  string
	toolInput string
	// replyCh 回传**类型化**决策（R3/D1，2026-09-14）。
	// 修复前是 chan bool，四种成因在下游被塌成同一句"用户拒绝"。
	replyCh chan tools.ConfirmOutcome
}

// agentConfirmBridge 是注入 executor.AgentConfirmFn 的桥实现。
//
// 阻塞等待用户决策。三个"非用户"出口**各自具名**（R3/D1，2026-09-14）：
// - ctx 取消（父任务结束/中断）→ Cancelled / ctx-cancelled —— **不是**用户拒绝
// - 等待超时 → TimedOut / timeout —— **不是**用户拒绝
// - program 未绑定（装配缺陷） → Denied / no-channel —— 用户根本没被问过
//
// 修复前三者都 `return false`，下游一律写成 "rejected by user (sub-agent confirmation)"。
func (m *Model) agentConfirmBridge(ctx context.Context, toolName, input string) tools.ConfirmOutcome {
	prog := m.program.get()
	if prog == nil {
		// 可观测出口（P0 修复，2026-09-13）：此前这里静默 return false，下游一律
		// 归因为 "rejected by user" ⇒ "弹了窗却不知为何被拒"根本无从定位。
		// 走到这里说明装配缺陷（NewModel 未创建 programRef，或 SetProgram 未调用），
		// 必须留痕。（修复前该分支在生产**恒真**：绑定接收者是一份死副本。）
		log.Printf("[agent-confirm] no program bound (tool=%s): confirmation overlay cannot be shown", toolName)
		return tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceNoChannel}
	}
	replyCh := make(chan tools.ConfirmOutcome, 1)
	prog.Send(agentConfirmRequestMsg{toolName: toolName, toolInput: input, replyCh: replyCh})
	select {
	case outcome := <-replyCh:
		return outcome
	case <-ctx.Done():
		// 可观测出口（对抗式审查 P2）：取消≠用户拒绝，留 canary 供定位
		log.Printf("[agent-confirm] ctx cancelled while waiting for user decision (tool=%s)", toolName)
		return tools.ConfirmOutcome{Decision: tools.DecisionCancelled, Source: tools.SourceCtxCancel}
	case <-time.After(agentConfirmTimeout):
		// 可观测出口：超时≠用户拒绝（真实应答窗口受 Run Timeout 约束，通常更短）
		log.Printf("[agent-confirm] timed out after %v waiting for user decision (tool=%s)", agentConfirmTimeout, toolName)
		return tools.ConfirmOutcome{Decision: tools.DecisionTimedOut, Source: tools.SourceTimeout}
	}
}

// handleAgentConfirmKeyMsg 处理子代理确认 overlay 的按键（StateWaitingConfirm 分支入口拦截）。
// 与主会话确认流的差异（刻意）：不走 resetStreamState/StateIdle——主流仍在
// Agent 工具执行的 goroutine 中，子代理等待回复后继续，状态应回到 StateToolExec。
func (m *Model) handleAgentConfirmKeyMsg(msg tea.KeyMsg, pc *PendingConfirm) (tea.Model, tea.Cmd) {
	options := pc.Options
	if len(options) == 0 {
		options = approvalOptionsFor(pc.Kind, pc.ToolName, pc.ToolInput)
	}
	// approve 把面板上的一档**放行类**决策落到 policy 与回传结果上（收口）。
	//
	// ★ 为什么参数是 ApprovalDecision 而不是 bool：修复前签名是 `approve(session bool)`，
	// 只有「本次 / 本会话」两档能表达；新增「命令前缀」档时 bool 无法承载
	// （会把前缀档静默记成全量会话批准）。改为决策枚举后，记忆写入与回传决策
	// 由**同一个 switch** 决定，不可能分叉。
	approve := func(d ApprovalDecision) {
		if pol := m.toolService.Policy(); pol != nil {
			switch d {
			case DecisionApproveSession:
				// 会话级批准写入 policy：后续同工具+入参的子代理调用免确认（与主会话语义一致）
				pol.Remember(pc.ToolName, pc.ToolInput, true)
			case DecisionApproveSessionPrefix:
				// 命令前缀档 —— 只记**头词**（同头单一简单命令免问），不记整条输入。
				pol.RememberPrefix(pc.ToolName, pc.ToolInput)
			}
		}
		decision := tools.DecisionApproved
		switch d {
		case DecisionApproveSession:
			decision = tools.DecisionApprovedSession
		case DecisionApproveSessionPrefix:
			decision = tools.DecisionApprovedSessionPrefix
		}
		m.finishAgentConfirm(pc, tools.ConfirmOutcome{Decision: decision, Source: tools.SourceUser})
	}
	switch msg.String() {
	case "up":
		if pc.SelectIdx > 0 {
			pc.SelectIdx--
		}
		return m, nil
	case "down":
		if pc.SelectIdx < len(options)-1 {
			pc.SelectIdx++
		}
		return m, nil
	case "enter":
		if pc.SelectIdx >= 0 && pc.SelectIdx < len(options) {
			decision := options[pc.SelectIdx].Decision
			if isApproveDecision(decision) {
				approve(decision)
				return m, nil
			}
		}
		// 落在非批准选项 = 用户在确认框上明确拒绝
		m.finishAgentConfirm(pc, tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceUser})
		return m, nil
	case "esc", "ctrl+c":
		// D1（R3）：用户主动中断该请求。对齐业内既有实现 —— ESC 是 interrupt
		// （`[Request interrupted by user]`），不是"拒绝了这次工具调用"。
		// 两者都不执行，但语义不同，不能混成一个 Decision。
		m.finishAgentConfirm(pc, tools.ConfirmOutcome{Decision: tools.DecisionCancelled, Source: tools.SourceUser})
		return m, nil
	}
	// 快捷键（1/2/y/n 等，与主会话 overlay 一致）
	for _, option := range options {
		if option.Shortcut == "" {
			continue
		}
		if !equalFoldKey(msg.String(), option.Shortcut) {
			continue
		}
		if isApproveDecision(option.Decision) {
			approve(option.Decision)
			return m, nil
		}
		// 快捷键落到非批准选项（n / 2 等）= 用户明确拒绝
		m.finishAgentConfirm(pc, tools.ConfirmOutcome{Decision: tools.DecisionDenied, Source: tools.SourceUser})
		return m, nil
	}
	return m, nil
}

// finishAgentConfirm 结束子代理确认：回传类型化决策、**留痕**、恢复状态。
// replyCh 缓冲为 1，单次发送不阻塞。
func (m *Model) finishAgentConfirm(pc *PendingConfirm, outcome tools.ConfirmOutcome) {
	reply := pc.AgentReplyCh
	m.toolService.SetPendingConfirm(nil)
	// Agent 工具仍在执行中，状态回到工具执行态
	m.sessionService.SetState(StateToolExec)
	m.recalcViewportHeight()

	// D2（R3，2026-09-14）：**拒绝必须在 transcript 留痕**。
	//
	// 修复前子 agent 路径的拒绝**一条消息都不留**（三个出口只调 finishAgentConfirm），
	// 于是"弹了窗却不知道原因"无从查起 —— 这是 2026-09-13 现场排查的真实障碍
	// （只能靠事后翻日志与会话文件反推）。对照主会话：其确认拒绝会打印
	// "Denied; operation was not run / 已拒绝，操作未执行"（app.go）。
	//
	// 文案由 outcome.Explain 按 Source 生成 —— 只有 SourceUser 才说"用户拒绝"。
	if outcome.Involved() && !outcome.Approved() {
		m.addMessage(errorStyle.Render(outcome.Explain(pc.ToolName)))
	}

	// delta F（H1，2026-09-14）：审批决策留痕 —— 子 agent 4 态（y / a / n / esc）的
	// **唯一汇聚点**就是这里。位置刻意放在 replyCh 回传**之前**：留痕失败不得改变
	// 回传语义（Q5：不阻断）。
	m.recordConfirmOutcome(bridge.OriginTUISubagent, pc.ToolName, pc.ToolInput, outcome)

	if reply != nil {
		reply <- outcome
	}
}

// equalFoldKey 忽略大小写的按键匹配。
func equalFoldKey(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
