// Package ai — V3-6-3 AI 拒绝覆盖
//
// 用户**主动**输入确认词时，用 prompt 注入让模型别在措辞上推诿（不绕过沙箱/权限层）。
//
// ★ （2026-09-19 · 用户放行方案 A）：本包**原先还有一套散文关键词拒绝检测**
// （`zhRefusalPatterns` / `enRefusalRe` / `DetectRefusal` / `ForceExecutePrompt`），**已整体删除**。
// 删除理由（三条，别再把它加回来）：
//
// 1. 「模型主观选择拒绝」在本协议下**没有结构化事实源**（`FinishReason` 只能表达 content_filter）
// ⇒ 按「证据不足时应静默」，该事实**不得**被自动断言。
// 这与本仓铁律同源：`domains/结构化判据与散文关键词.md`（可判定的结构化事实必须由结构化通道断言）。
// 2. 真机 T-1 实测它**必然误报**：模型如实回答「我没有 WebSearch 工具…所以我无法直接调用它」
// —— 这正是 T-1 票面要求的期望行为 —— 却命中关键词「我无法」被判成 AI 拒绝，
// 并弹出机制上无效的 force 提示 ⇒ **T-1 通过 ⟺ 必然报误报**（判据自相矛盾）。
// 3. 误报的代价不是"多一条提示"：force 注入「请执行而非拒绝」，但该场景下模型**不是拒绝、是没有能力**
// ⇒ 只会被推向**编造结果**（T-1 票面明列的失败态②）。**把正确行为推向错误行为。**
//
// 行业对照：同类 CLI **零自动拒绝检测**，`refusal` 全仓仅 2 处命中且均为人工反馈分类
// （`对标证据件`）—— 靠用户报，不靠关键词扫。
// 结构门（防回退）：`.go`。
//
// ⚠️ R4（S17 P3 · V3-6-3）仍然成立且**不得退**：本包里 `MentionsSecurityConstraint` 只是**辅助**信号
// （它读散文，散文不是事实）。「执行被安全策略拦截」只能由结构化事件（`tools.Result.FailureKind` =
// `sandbox_denied` / `policy_denied`）断言；判据收口在 `internal/tui/stream.go checkAISafety`
// 的**二项合取**。调用方契约见该函数注释。
package ai

import (
	"strings"
)

// RefusalHandler 处理 AI 拒绝执行场景（仅保留"用户主动确认"这一半）。
// 用户主动确认后，通过 prompt 注入告诉模型可以执行；不绕过沙箱/权限层。
type RefusalHandler struct {
	enabled          bool // 是否启用强制执行
	forceExecuteMode bool // 当前是否处于强制执行模式
}

// securityConstraintPatterns 识别"文本在谈执行策略约束"（沙箱/权限/网络）。
//
// ⚠️ R4（S17 P3）：这是**辅助**信号，不是判据。它对任何"提到"沙箱的正常回答都返回 true
// （为了覆盖全，表只会越加越长），因此**单独使用必然误报**。
// 权威判据是结构化的 tools.Result.FailureKind 事件；本函数只能作为合取的第二项，
// 在结构化拒绝事件已成立时用来确认措辞。调用方契约见 internal/tui/stream.go checkAISafety。
//
// 原名 IsSecurityPolicyRefusal 会把"文本谈到了约束"误读成"发生了拒绝"——即本 bug 的成因，故改名。
var securityConstraintPatterns = []string{
	"sandbox", "networkisolated", "network isolation", "security policy",
	"permission denied", "not permitted", "not allowed", "blocked",
	"沙箱", "网络隔离", "安全策略", "权限不足", "权限拒绝", "被拦截", "禁止执行",
}

// forceExecuteConfirmPhrases 强制执行确认词（大小写不敏感，精确匹配）。
var forceExecuteConfirmPhrases = []string{
	"我确认强制执行",
	"确认强制执行",
	"force execute",
}

// forceExecuteSystemMessageTpl 强制执行注入到 system message 的提示模板。
// 明确告知 AI 仍需遵守沙箱和权限约束（审查修订建议-3）。
const forceExecuteSystemMessageTpl = "用户已确认强制执行此任务，请执行而非拒绝。注意：仍需遵守沙箱和权限约束。"

// NewRefusalHandler 创建拒绝处理器。
// enabled: 是否启用强制执行机制。
func NewRefusalHandler(enabled bool) *RefusalHandler {
	return &RefusalHandler{
		enabled: enabled,
	}
}

// MentionsSecurityConstraint reports whether the text **talks about** a sandbox,
// permission, network, or security-policy constraint.
//
// R4（S17 P3）：这是**辅助**信号，不是判据。它对任何"提到"沙箱的正常回答都返回 true
// （为了覆盖全，表只会越加越长），因此**单独使用必然误报**。
// 权威判据是结构化的 tools.Result.FailureKind 事件；本函数只能作为合取的第二项，
// 在结构化拒绝事件已成立时用来确认措辞。调用方契约见 internal/tui/stream.go checkAISafety。
func (h *RefusalHandler) MentionsSecurityConstraint(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, pattern := range securityConstraintPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// IsForceExecuteMode reports whether the one-shot AI refusal override is active.
func (h *RefusalHandler) IsForceExecuteMode() bool {
	return h != nil && h.forceExecuteMode
}

// IsForceExecuteConfirm 检查用户输入是否为强制执行确认。
// 确认词："我确认强制执行" / "force execute" / "确认强制执行"
// 大小写不敏感，去除首尾空白后精确匹配。
func (h *RefusalHandler) IsForceExecuteConfirm(input string) bool {
	if input == "" {
		return false
	}
	trimmed := strings.TrimSpace(strings.ToLower(input))
	for _, phrase := range forceExecuteConfirmPhrases {
		if trimmed == strings.ToLower(phrase) {
			return true
		}
	}
	return false
}

// SetForceExecuteMode 设置强制执行模式。
// enabled: true 表示进入强制执行模式（用户已确认）。
func (h *RefusalHandler) SetForceExecuteMode(enabled bool) {
	h.forceExecuteMode = enabled
}

// GetForceExecuteSystemMessage 返回注入到 system message 的强制执行提示。
// 此提示让 AI 知道用户已确认强制执行，应执行任务而非拒绝。
// 仅在 forceExecuteMode 为 true 时返回非空消息。
// 明确告知 AI 仍需遵守沙箱和权限约束（审查修订建议-3）。
func (h *RefusalHandler) GetForceExecuteSystemMessage() string {
	if !h.forceExecuteMode {
		return ""
	}
	return forceExecuteSystemMessageTpl
}
