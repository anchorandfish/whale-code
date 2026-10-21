package tools

// 沙箱「本条命令已获用户逐条批准」的**唯一传递通道**（，2026-10-05）。
//
// 为什么需要它：审批与执行在这个仓里是**两条独立的链** ——
//
//	审批链：ExecuteWithConfirm → Pipeline.Execute → SandboxPrecheckStage（决定问不问）
//	执行链：handler → bash.go prepareSandbox → Sandbox.Validate（独立复检一次）
//
// 用户的 Yes 只写进了 `Call.Approved`，而 handler 的签名是
// `func(ctx context.Context, input json.RawMessage) (string, error)`（executor.go:41）
// —— **handler 看不到 Call** ⇒ 执行链无从得知"这条命令刚刚被批准过"，
// 于是在 `prepareSandbox` 再否决一次。这正是用户报告的现象：
// **"允许了还会被拒绝"**。
//
// 传递方式选 context，理由两条：
// 1. 它天然沿着 `ExecuteWithConfirm → Pipeline.Execute(ctx) → handler(ctx)
// → prepareSandbox(ctx)` 这条既有链路走，不需要改任何函数签名；
// 2. 它是**逐次**的。若改成 Executor 上的可变字段，并发批次
// （`ExecuteBatch` 最多 8 路，executor.go:992）会把 A 调用的批准泄漏给 B 调用。
//
// ⚠️ **唯一合法产生点** = `ExecuteStage.Execute`（pipeline.go）里
// `state.UserApproved == true` 的那次 ctx 包装（收口，2026-10-08）。
// 批准事实本身在 `ToolPipeline.Execute` 头部从**原始 Call**取定
// （`call.Approved && !call.ApprovedByRule`，先于规则门 / 会话记忆改写，
// 存 `PipelineState.UserApproved`）—— 三条入口形态
// （plain 确认重跑 / TUI 面板批准重跑 / 子代理批准恢复）都以
// `Approved=true` 重入管线 ⇒ 结构性全覆盖，新入口亦然。
//
// ★ 第二产生点 = `sanctionedContext` 的 `call.SandboxEscalated`
// 分支：on-failure 沙箱升级的批准重跑（用户对「沙箱卡住了这条命令」按 Yes）。
// 置位点唯一 = `ExecuteWithConfirm`（executor.go），仍走「整条管线重入」，
// 与 Approved 同生命周期（逐条、不写盘、不记忆）。细节见 sanctionedContext 注释。
// 以下两类**都不得**产生它：
// - 规则推导的免问（`Call.ApprovedByRule`）：那是"随仓库分发的 settings.json
// 替用户做了决定"（已为危险档钉死同款纪律）；
// - 会话记忆批准（`policy.Has`）：那是"**别的**命令的批准"，不能跨命令放大。
//
// 消费点只有一个：`bash.go` 的 `prepareSandbox`。它据此让该条命令在**沙箱外**执行
// —— 语义与 同类 CLI 的 `BypassSandboxFirstAttempt` 逐字对应
// （对标证据件，见 ruleengine.go `VerdictTier` 注释）。

import (
	"context"
	"strings"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

type sandboxSanctionKey struct{}

// withSandboxSanction 标记"本条命令的**可批准档**沙箱规则已获用户逐条批准"。
func withSandboxSanction(ctx context.Context) context.Context {
	return context.WithValue(ctx, sandboxSanctionKey{}, true)
}

// sandboxSanctioned 报告本次调用是否携带"用户对沙箱规则的逐条批准"。
//
// nil ctx 视为未批准（fail-closed）：不能因为调用方传了 nil 就把沙箱打开。
func sandboxSanctioned(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(sandboxSanctionKey{}).(bool)
	return v
}

// sanctionedContext 判断"本次批准"是否需要向执行链下传沙箱批准标记，需要则包一层 ctx。
//
// 判据**不是**"用户按了 Yes"，而是"用户按 Yes 的这条命令，其沙箱判定恰好落在
// **可批准档**"。两者差在哪儿：bash 命令的确认框还可能来自危险档
// （`DangerousGuardStage`）或权限层 —— 那些确认与"沙箱规则"无关，
// 不该因为一次 Yes 就把这条命令放到沙箱外跑。用同一规则引擎现算，避免这个放大。
//
// ★ 第二分支 —— on-failure 沙箱升级（`call.SandboxEscalated`）：
// 升级的对象恰恰是 **TierPass** 命令（规则引擎眼里无害、OS 沙箱才拒绝的，如读
// `~/.ssh`、写工作区外、断网连网）⇒ 既有 TierConfirm 分支**够不着**它们，
// 必须由升级标记补位。边界：
// - 置位点唯一 = `ExecuteWithConfirm` 在用户对升级询问按 Yes 后（tools.go 字段注释），
// 仍是「用户批准」语义，不是规则推导（纪律不破）；
// - 消费点不变 = `prepareSandbox` 的 sanctioned 路径 ⇒ HardDeny 恒拒
// （`ValidateSanctioned` sandbox.go:201）、validator Critical 恒拦（bash.go）——
// 升级继承 sanctioned 通道的**全部**纵深防御，不是旁路。
func sanctionedContext(ctx context.Context, sbx *sandbox.Sandbox, call Call) context.Context {
	if sbx == nil || !sbx.Enabled() {
		return ctx
	}
	if !permission.IsCommandBearingTool(call.Name) {
		return ctx
	}
	cmd := pipelineExtractField(call.Input, "command")
	if strings.TrimSpace(cmd) == "" {
		return ctx
	}
	// 升级重跑 ⇒ 无论档位（通常 TierPass）都出沙箱 —— 这是用户对
	// 「沙箱卡住了这条命令」的显式回应；纵深防御在消费点（prepareSandbox）继承。
	if call.SandboxEscalated {
		return withSandboxSanction(ctx)
	}
	if tier, _, _ := sbx.Classify(cmd); tier == sandbox.TierConfirm {
		return withSandboxSanction(ctx)
	}
	return ctx
}
