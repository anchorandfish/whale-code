// confirm.go — 子 agent 工具确认的**类型化**决策，以及可携带结构化标记的失败载体。
//
// 归属：R3（S17 D1/D2/D3 合并 S4 的 L3.2）。2026-09-14。
//
// 解决两个独立但同源的缺陷：
//
// 1. **决策塌成一个 bool**（S17 D1）。修复前子 agent 确认通道返回 bool，于是四种
// 成因 —— 用户拒绝 / 父任务 ctx 取消 / 等待超时 / 确认槽被占用 —— 在下游全部
// 被写成同一句 `rejected by user (sub-agent confirmation)`。
// 后果：用户按了 Y 也可能被判"用户拒绝"；而"用户根本没被问过"（槽位占用、
// 无通道）同样伪装成"用户拒绝"。
// 对标同类 CLI `ReviewDecision{Approved, ApprovedForSession, Denied, TimedOut, Abort}`
// 与 同类 CLI `behavior{allow,deny,ask}`：决策必须**类型化**，且必须**留痕**。
//
// 2. **FailureKind 被丢弃**（L3.2 / S4-F6）。子 agent 内被拒时 `fmt.Errorf` 只留文本，
// 终局拒绝种类（sandbox_denied / policy_denied）在传递链上蒸发，于是子 agent
// 被拒后可以自由换工具重试（现场表现为"改用替代工具"）。
// 契约见 `R1_FailureKind传递契约_2026-09-14.md` §4（C1–C4）。
//
// 硬纪律：**不得**把 kind 塞进字符串再靠解析取回（R1 契约 C4）—— 那正是本文件要消灭的
// "靠本地化文本判断"。
package tools

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// D1 · 类型化决策（S17 §4.2）
// ---------------------------------------------------------------------------

// ConfirmDecision 是子 agent 工具确认的**结果类别**。
// 显式取值，零值非法 —— 避免"忘了设"被下游误读成某个既定语义。
type ConfirmDecision string

const (
	// DecisionApproved 用户批准本次调用。
	DecisionApproved ConfirmDecision = "approved"
	// DecisionApprovedSession 用户批准本会话内同类调用（写入 policy 免问表）。
	DecisionApprovedSession ConfirmDecision = "approved_session"
	// DecisionApprovedSessionPrefix 用户批准「该命令前缀」的本会话免问（，2026-10-06）。
	//
	// 粒度**比 DecisionApprovedSession 更细**：后者按"工具 + 整条输入"逐字免问；
	// 本档按"命令头词"免问，且仅对**单一简单命令**成立（复合/含替换/含重定向的命令
	// 不进入前缀范围）。危险/破坏性命令永不由此档免问。
	// 它是**放行类**决策（Approved() 认它），但**不得**被当成全量会话批准。
	DecisionApprovedSessionPrefix ConfirmDecision = "approved_session_prefix"
	// DecisionDenied 拒绝。具体成因看 ConfirmSource —— **只有 SourceUser 才是用户拒绝**。
	DecisionDenied ConfirmDecision = "denied"
	// DecisionCancelled 请求被取消（父任务 ctx 结束 / 用户中断）。**不是**用户拒绝。
	DecisionCancelled ConfirmDecision = "cancelled"
	// DecisionTimedOut 等待用户决策超过安全网上限。**不是**用户拒绝。
	DecisionTimedOut ConfirmDecision = "timed_out"
)

// ConfirmSource 说明决策的**成因方**，是"能不能说'用户拒绝'"的唯一判据。
type ConfirmSource string

const (
	// SourceUser 用户在确认框上做出了选择。
	SourceUser ConfirmSource = "user"
	// SourceCtxCancel 父任务 ctx 取消/中断，等待被打断。
	SourceCtxCancel ConfirmSource = "ctx-cancelled"
	// SourceTimeout 等待用户决策超时。
	SourceTimeout ConfirmSource = "timeout"
	// SourceSlotGuard 确认槽已被占用，用户**从未被询问**（S17 D3）。
	SourceSlotGuard ConfirmSource = "slot-guard"
	// SourceNoChannel 无确认通道（AgentConfirmFn/OnConfirm 均未接线），fail-closed。
	SourceNoChannel ConfirmSource = "no-channel"
	// SourcePipeline 管线校验阶段自动拒绝（**非用户决策** —— 用户从未被询问）。
	//
	// G9-④：这是第 5 类**非用户**成因，且比前四类更"早" ——
	// 它根本不经过确认通道：`ToolPipeline.Execute` 在 PermissionStage 之前
	// （CustomValidateStage / rule_gate / 工作区边界）就短路返回了，
	// 用户**连确认框都没见过**。
	//
	// 因此 G9-002 把"被管线拒绝"写进账本 `~/.whale/audit/tool-approval.jsonl` 时
	// **必须**用它，而**不得**用 SourceUser 凑：那会谎称"用户拒绝"，
	// 与 :42 的硬纪律（只有 SourceUser 才是用户拒绝）及 Explain 的文案契约直接冲突。
	SourcePipeline ConfirmSource = "pipeline"
)

// ConfirmOutcome 一次确认请求的类型化结果（Decision × Source）。
// 零值（Source == ""）表示"本路径不涉及确认"，调用方须据此跳过。
type ConfirmOutcome struct {
	Decision ConfirmDecision
	Source   ConfirmSource
}

// Approved 报告是否放行执行。
//
// 把「命令前缀」档一并纳入 —— 它同样是用户给出的放行。
func (o ConfirmOutcome) Approved() bool {
	return o.Decision == DecisionApproved ||
		o.Decision == DecisionApprovedSession ||
		o.Decision == DecisionApprovedSessionPrefix
}

// Involved 报告本结果是否来自确认路径。
func (o ConfirmOutcome) Involved() bool {
	return o.Source != ""
}

// Explain 生成**不误导**的拒绝说明（英文为主，与项目 UI 文案约定一致）。
//
// 硬纪律（S17 §4.2）：只有 SourceUser 允许出现"用户拒绝"；ctx 取消 / 超时 /
// 槽位占用一律据实说明 —— 它们的共同点是**用户可能根本没被问过**。
func (o ConfirmOutcome) Explain(toolName string) string {
	switch o.Source {
	case SourceUser:
		if o.Decision == DecisionCancelled {
			return fmt.Sprintf("tool %s cancelled by user before execution", toolName)
		}
		return fmt.Sprintf("tool %s denied by user; operation was not run", toolName)
	case SourceCtxCancel:
		return fmt.Sprintf("tool %s was not run: request cancelled (parent task ended or interrupted) — the user was never asked", toolName)
	case SourceTimeout:
		return fmt.Sprintf("tool %s was not run: timed out waiting for a user decision — the user was never asked", toolName)
	case SourceSlotGuard:
		return fmt.Sprintf("tool %s was not run: another confirmation was already pending, so the user was never asked (confirm slot busy)", toolName)
	case SourceNoChannel:
		return fmt.Sprintf("tool %s was not run: no confirmation channel is available for the sub-agent", toolName)
	case SourcePipeline:
		// G9-④：据实 —— 管线自己拒的，**用户从未被询问**，不得出现"用户拒绝"字样。
		return fmt.Sprintf("tool %s was not run: rejected by the tool pipeline before execution (validation/permission stage) — the user was never asked", toolName)
	default:
		return fmt.Sprintf("tool %s was not run (decision=%s source=%s)", toolName, o.Decision, o.Source)
	}
}

// ---------------------------------------------------------------------------
// · 非交互入口的「管线终局拒绝」映射（单一实现点）
// ---------------------------------------------------------------------------

// DenialResult 把一次「管线终局拒绝」映射成**非交互入口**（`tui/runner_core.go`
// 的 executeToolsAndContinue、`tui/plain.go` 的 --plain 路径、以及 future bridge）
// 可直接使用的工具结果。
//
// ★ 为什么必须收口到这里：修复前 `runner_core.go:1436` 与 `plain.go:1994`
// **各写了一遍**同样的三行，两处**同时**把"管线拒绝"写成
// `"user cancelled tool execution"` —— 即 `tools/confirm.go:42` 硬纪律明令禁止的
// 那句谎（管线在确认之前就短路了，用户**连确认框都没见过**）。
// 本仓"命令面双实现"是复发坑（“：同一条命令在两个 UI 实现两遍，只改一侧）；
// 因此这里收成**唯一实现**，两条入口只做薄转发。
//
// 文案选择**只由结构化的 `ConfirmSource` 决定**（R4：可判定的结构化事实不得由
// 散文关键词断言）：
// - `SourceUser` ⇒ 用户拒绝（唯一允许说"用户拒绝"的取值）；
// - `SourcePipeline` ⇒ 管线前置拒绝（用户从未被询问）；
// - 零值 / 未知 ⇒ **中性兜底**，绝不默认写成"用户取消"。
//
// 同时**透传 `FailureKind`**：修复前这两条入口丢弃了拒绝种类，于是
// `FailureKind.Terminal()` 在非交互路径上恒为假 —— 模型可以在同一条 deny 规则下
// 换工具重试（与 G9-① 在 bubbletea 侧修掉的是同一个漏点）。
func DenialResult(callID, toolName string, pr *PipelineResult) Result {
	msg := ""
	if pr != nil {
		msg = pr.Rejection
	}
	if msg == "" {
		switch {
		case pr != nil && pr.ConfirmSource == SourceUser:
			msg = ConfirmOutcome{Decision: DecisionDenied, Source: SourceUser}.Explain(toolName)
		case pr != nil && pr.ConfirmSource == SourcePipeline:
			msg = ConfirmOutcome{Decision: DecisionDenied, Source: SourcePipeline}.Explain(toolName)
		default:
			msg = fmt.Sprintf("tool %s was not run (the pipeline reported no reason)", toolName)
		}
	}
	kind := FailureKind("")
	if pr != nil {
		kind = pr.FailureKind
	}
	return Result{
		ID:          callID,
		Name:        toolName,
		Output:      msg,
		IsError:     true,
		FailureKind: kind,
	}
}

// ---------------------------------------------------------------------------
// L3.2 · 可携带结构化标记的失败载体
// ---------------------------------------------------------------------------

// ToolExecError 是 executeToolCall 的结构化失败，把 `FailureKind`（终局拒绝种类）
// 与 `Confirm`（确认结果）**经 error 接口**带到 runMultiRound，
// 而不是像修复前那样只留一句格式化文本（R1 契约 §4 C1 / C4）。
//
// 它同时承担两个角色：
// - Kind != "" ⇒ **终局拒绝**（IK1：禁止重试/升级），runMultiRound 据此终止该轮次；
// - Confirm.Source != "" ⇒ 本失败来自确认交互，文案必须按 Source 生成（D1）。
//
// 二者可以同时为空（普通失败，如命令非零退出）—— 此时行为与修复前一致，
// **不得**为了"看起来结构化"而滥用 Kind（IK2）。
type ToolExecError struct {
	Tool    string
	Kind    FailureKind    // 非空 ⇒ 终局拒绝
	Confirm ConfirmOutcome // Source 非空 ⇒ 确认路径
	Msg     string
}

// Error 实现 error 接口。优先返回已经本地化的 Msg（工具输出原文）。
func (e *ToolExecError) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" {
		return e.Msg
	}
	if e.Confirm.Involved() {
		return e.Confirm.Explain(e.Tool)
	}
	return fmt.Sprintf("tool %s failed", e.Tool)
}

// Terminal 报告本次失败是否为**终局拒绝**（IK1）。
//
// G5：判据由 `Kind != ""` 改为 `Kind.Terminal()` —— 终局性现在是
// FailureKind 的显式属性（tools.go 的 failureKindSpecs.Terminal），而不是"值非空"的副产物。
// 起因：新增 patch_conflict（可恢复的普通失败）时发现，若沿用非空判据，
// 一次补丁失配就会终结整个用户回合。对既有两个 Kind 行为完全不变（都登记为终局）。
func (e *ToolExecError) Terminal() bool {
	return e != nil && e.Kind.Terminal()
}

// FailureKindOf 提取 error 链上的 FailureKind；无则返回零值。
//
// 这是"靠结构化标记判断"的单一入口：调用方（executor / 编排 / TUI）**不得**
// 退回解析本地化文本（R1 契约 C4）。
func FailureKindOf(err error) FailureKind {
	var tee *ToolExecError
	if errors.As(err, &tee) && tee != nil {
		return tee.Kind
	}
	// 兼容既有哨兵（bash / file 的沙箱拒绝）—— 它们不走 ToolExecError。
	switch {
	case errors.Is(err, errSandboxDenied), errors.Is(err, errSandboxDeniedSoft):
		return FailureKindSandboxDenied
	}
	return ""
}

// ConfirmOutcomeOf 提取 error 链上的确认结果；非确认类失败返回 ok=false。
func ConfirmOutcomeOf(err error) (ConfirmOutcome, bool) {
	var tee *ToolExecError
	if errors.As(err, &tee) && tee != nil && tee.Confirm.Involved() {
		return tee.Confirm, true
	}
	return ConfirmOutcome{}, false
}
