// approval_audit.go — TUI 侧审批留痕接线（delta F，2026-09-14）。
//
// 本文件只做三件事：
// 1. 把 tui 的 `ApprovalDecision`（iota）**显式**映射到 tools 的 string 决策
// （唯一真相源见 tools/confirm.go）—— **不靠 default 兜底**。
// 2. 组装 `bridge.ApprovalRecord`（补齐 session / request id）。
// 3. 落盘，并把**写失败**做成可见（Q5：不阻断，但绝不静默失败）。
//
// 与 S9 delta E 的关系：本文件**只写**，从不把审计内容读回 Policy ——
// 「留痕」不得退化成「持久授权」（守卫测试 TestDeltaF_T1_AuditIsNotReadBackAsAuthorization）。
package tui

import (
	"fmt"
	"log/slog"

	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// approvalAuditRelPath 是审批留痕文件相对 $HOME 的路径（单一真相源）。
const approvalAuditRelPath = ".whale/audit/tool-approval.jsonl"

// confirmDecisionOf 把 tui 的 ApprovalDecision 映射为 tools 的 ConfirmDecision 字符串。
//
// ★ 显式映射表，**不用 default 兜底**：`decisionToElicitationAction`（app.go）正是
// 用 default 把未知值吞成 Cancel 的先例 —— 审计里这么写会把"未知决策"伪装成
// "用户取消"。这里 ok=false 表示遇到未登记的取值，调用方必须**如实记空**、不得猜。
func confirmDecisionOf(d ApprovalDecision) (tools.ConfirmDecision, bool) {
	switch d {
	case DecisionApproveOnce:
		return tools.DecisionApproved, true
	case DecisionApproveSession:
		return tools.DecisionApprovedSession, true
	case DecisionApproveSessionPrefix:
		// 命令前缀档必须**显式**登记 —— 缺了它 ok=false 会把
		// 一次真实授权如实记成**空**（不猜），审计里就等于"用户什么都没做"。
		return tools.DecisionApprovedSessionPrefix, true
	case DecisionDeny:
		return tools.DecisionDenied, true
	case DecisionCancel:
		return tools.DecisionCancelled, true
	default:
		return "", false
	}
}

// approvalAuditSink 取审批留痕通道（nil-safe）。
//
// ★ 走 ToolService 接口，而不是 Model 上的值字段：Model 是**按值复制**的
//
//	（bubbletea 的 Update 每次返回新值，见 app.go 的 programRef 长注释）。
//	值字段会被写进"调用它的那份副本"，而真正被驱动的那份永远是零值 ⇒
//	**静默失效、完全不可见**。接口内部含指针 ⇒ Model 值复制后底层实例共享。
func (m *Model) approvalAuditSink() bridge.ApprovalAudit {
	if m.toolService == nil {
		return nil
	}
	return m.toolService.ApprovalAudit()
}

// newApprovalRecord 组装一条审批留痕（自动补齐 session / request id）。
func (m *Model) newApprovalRecord(origin bridge.ApprovalOrigin, tool, input, decision, source string) bridge.ApprovalRecord {
	rec := bridge.ApprovalRecord{
		Origin:    origin,
		Tool:      tool,
		ToolInput: input,
		Decision:  decision,
		Source:    source,
	}
	if m.sessionService != nil {
		rec.SessionID = m.sessionService.SessionName()
	}
	if m.aiService != nil {
		rec.RequestID = m.aiService.RequestID()
	}
	return rec
}

// recordApproval 写一条审批留痕（**唯一落盘出口**）；失败不阻断审批，但必须可见。
//
// 可见性 = ① whale.log（slog）+ ② TUI transcript 一行。
// 静默失败比"不留痕"更危险 —— 用户会以为有留痕。
func (m *Model) recordApproval(rec bridge.ApprovalRecord) {
	sink := m.approvalAuditSink()
	if sink == nil {
		return // 未接线（Plain/Bridge 模式，或测试未配置）⇒ 不落盘，也不算失败
	}
	if err := sink.Record(rec); err != nil {
		slog.Warn("approval audit write failed", "tool", rec.Tool, "decision", rec.Decision, "err", err)
		m.addMessage(errorStyle.Render(fmt.Sprintf(
			"⚠️ Approval audit write failed (decision still applies) / 审批留痕写入失败（决策仍然生效）: %v", err)))
	}
}

// recordApprovalDecision 记录一次 tui 侧决策（ApprovalDecision 口径）。
// mcpServer 仅 elicitation 用，其余传 ""。
func (m *Model) recordApprovalDecision(origin bridge.ApprovalOrigin, tool, input string, d ApprovalDecision, src tools.ConfirmSource, mcpServer string) {
	dec, ok := confirmDecisionOf(d)
	if !ok {
		// 如实记空 + 告警，**不猜**成某个既定语义。
		slog.Warn("approval audit: unrecognised ApprovalDecision (recorded as empty, not guessed)",
			"decision_int", int(d), "tool", tool)
	}
	rec := m.newApprovalRecord(origin, tool, input, string(dec), string(src))
	rec.McpServer = mcpServer
	m.recordApproval(rec)
}

// recordConfirmOutcome 记录一次 tools 口径的确认结果（子 agent 路径直接用类型化结果，
// 不经过 tui 的 iota 枚举，因此无需映射）。
func (m *Model) recordConfirmOutcome(origin bridge.ApprovalOrigin, tool, input string, o tools.ConfirmOutcome) {
	m.recordApproval(m.newApprovalRecord(origin, tool, input, string(o.Decision), string(o.Source)))
}

// approvalOriginOfConfirm 从 PendingConfirm 推导审批留痕的 Origin：
// 子代理回传通道非 nil ⇒ tui-subagent；elicitation ⇒ tui-elicitation；其余 ⇒ tui-main。
// 与决策侧三入口（recordApprovalDecision / recordConfirmOutcome / elicitation）的既有口径一致。
func approvalOriginOfConfirm(pc *PendingConfirm) bridge.ApprovalOrigin {
	switch {
	case pc.AgentReplyCh != nil:
		return bridge.OriginTUISubagent
	case pc.Kind == ApprovalKindMcpElicitation:
		return bridge.OriginTUIElicitation
	default:
		return bridge.OriginTUIMain
	}
}

// recordApprovalRequestTool 记录「确认弹窗已被置入 TUI 待决状态」—— request 侧留痕
// （AuditOpToolApprovalRequest，2026-10-07）。
//
// 为什么挂在 SetPendingConfirm：这里是所有确认面（pipeline / agent / elicitation）
// 进入待决状态的**唯一汇合点**。决策侧只有用户拍板的时刻，缺了请求时刻就无法判别
// 「用户晚点批准」与「弹窗延迟浮现」（后者是真 UI 缺陷）——两条经 ToolInputHash
// 关联，时间差即 pending 时长。
//
// 失败可见性：与 recordApproval 同标准（Q5）——不阻断确认流程，但必须留痕；
// ToolModel 无消息面板访问权，故可见化走 slog（whale.log）而非 TUI transcript。
func (t *ToolModel) recordApprovalRequestTool(pc *PendingConfirm) {
	sink := t.approvalAudit
	if sink == nil {
		return // 未接线（Plain/Bridge 模式，或测试未配置）⇒ 不落盘，也不算失败
	}
	origin := approvalOriginOfConfirm(pc)
	rec := bridge.ApprovalRecord{
		Origin:    origin,
		Tool:      pc.ToolName,
		ToolInput: pc.ToolInput,
		McpServer: pc.McpServerName,
	}
	if err := sink.RecordRequest(rec); err != nil {
		slog.Warn("approval request audit write failed", "tool", pc.ToolName, "err", err)
	}
	slog.Info("[audit] tool approval request surfaced", "tool", pc.ToolName, "origin", string(origin))
}
