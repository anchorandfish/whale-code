// r4_refusal_ledger_test.go — R4（S17 P3 · V3-6-3「拒绝检测收口」）在 internal/tui 一侧的判别性验收。
//
// 被修的设计错误：
//
//	stream.go checkAISafety 原先用**纯散文关键词**（ai.IsSecurityPolicyRefusal）决定是否断言
//	「执行被安全策略拦截」。任何**提到**沙箱的正常回答都会命中 → 走 ⛔ 分支并 return，
//	用户从此看不到可以走的 AI-only 强制确认路径（误报 → 功能永久丢失）。
//
// R4 判据（方案 B · 用户 2026-09-14 拍板「关键词降级为辅助（结构化 ∧ 散文 合取）」）：
//
//	⛔ 分支 = Model.roundDeniedKind 非空 ← 权威：本轮真实发生过结构化拒绝事件
//
// ∧ MentionsSecurityConstraint(text) ← 辅助：文本在谈安全约束
//
// 账本写入点 = toolResultMsg 漏斗（app.go，单一写入点）；清理点 = resetAIDetectors（每用户回合）。
// 之所以**不能**把账本记在 handleToolResults 里：AskUser 主拦截路径（app.go checkAskUserChoice
// 命中时）不经 handleToolResults，同批到达的沙箱/权限拒绝会被整批忽略 —— 见正例用例的前置①。
//
// 负例（纯散文、本轮无结构化事件 ⇒ 不得断言策略拦截）在 tui_regression_test.go 的
// TestSecurityProseAloneDoesNotClaimPolicyBlock。
//
// ★ （2026-09-19 · 用户放行方案 A）：**「AI 自身拒绝」那一半的散文判据已整体删除。**
// 本文件是 R4 **保留下来**的那一半（结构化 ∧ 辅助）的承重判据，**未动、必须继续绿**。
// 被有意翻转的两条见各自注释（`...FallsBackToSilence` / `TestR4_LedgerIsScopedToUserTurn`）。
package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/charmbracelet/x/ansi"
)

// r4SecurityProse 命中 MentionsSecurityConstraint（"沙箱"/"网络隔离"），
// 是"模型自己在散文里谈安全约束"的典型形态，用作合取的第二项。
//
// ★ 后它**不再**用来命中"拒绝词"：`DetectRefusal` 已删。注意它仍含「我无法」字样 ——
// 这是**有意保留**的：它用来证明"散文里的拒绝词已经完全不参与判定"（T2 用的就是它）。
const r4SecurityProse = "抱歉，当前沙箱启用了网络隔离，我无法执行 curl。"

// r4SandboxDenied 是一条**真实形态**的结构化拒绝结果。
func r4SandboxDenied() tools.Result {
	return tools.Result{
		ID:          "bash-1",
		Name:        "bash",
		Output:      "sandbox denied / 沙箱拒绝\nCommand / 命令: curl https://example.com\nNo command was run / 命令未执行",
		IsError:     true,
		FailureKind: tools.FailureKindSandboxDenied,
	}
}

// TestR4_StructuredDenialDrivesSecurityPolicyBanner · 正例（生产入口 toolResultMsg 漏斗）。
//
// 三条断言各锁一件事：
// 1. 前置①：这批结果确实走了 AskUser 主拦截路径（回合**继续**）—— 从而证明"观察到结构化拒绝
// 但回合未终止"是生产可达状态，而不是测试手工编的前提；
// 2. 前置②：结构化拒绝确实入了账（权威判据到位）；
// 3. 合取成立 ⇒ 必须 ⛔，且**不展示** force 提示（真实策略强制挡住的东西，force 也挡不住）。
func TestR4_StructuredDenialDrivesSecurityPolicyBanner(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.roundDeniedKind != "" {
		t.Fatalf("前置失败：新会话账本应为空，实际 %q", m.roundDeniedKind)
	}

	// 同批结果 = AskUser（触发主拦截路径）+ 沙箱拒绝（结构化事件）。
	results := []tools.Result{
		{
			ID: "a1", Name: "AskUser", IsError: false,
			Output: `{"question":"要继续吗","options":[{"label":"继续","value":"go"}]}`,
		},
		r4SandboxDenied(),
	}
	updated, _ := m.Update(toolResultMsg{results: results})
	mm := updated.(Model)

	if got := mm.sessionService.State(); got != StateWaitingChoice {
		t.Fatalf("前置①失败：应命中 AskUser 主拦截路径并停在 WaitingChoice，实际 %v"+
			"（若为 StateIdle 说明走了 handleToolResults 的终局路径，本用例失去判别力）", got)
	}
	if mm.roundDeniedKind != tools.FailureKindSandboxDenied {
		t.Fatalf("前置②失败：结构化拒绝未入账，roundDeniedKind=%q（账本记错了层？）", mm.roundDeniedKind)
	}

	mm.checkAISafety(r4SecurityProse, false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))

	if !strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("结构化拒绝 ∧ 散文谈约束 ⇒ 必须走 ⛔ 分支: %q", transcript)
	}
	if strings.Contains(transcript, "To override an AI-only refusal") {
		t.Fatalf("策略强制真实发生，不得再给会被它挡住的 AI-only 强制提示: %q", transcript)
	}
	if strings.Contains(transcript, "AI refusal / AI 拒绝执行") {
		t.Fatalf("⛔ 分支应独占输出，不得同时出现普通拒绝提示: %q", transcript)
	}
}

// TestR4_StructuredOnlyWithoutProseFallsBackToSilence · 合取的另一半：
// 有结构化拒绝但文本**没在谈**安全约束 ⇒ 不出 ⛔。
//
// ★ （2026-09-19 · 方案 A）**有意翻转**（用户 2026-09-19 01:2x 放行）：
//
//	本用例原来叫 `TestR4_StructuredOnlyWithoutProseFallsBackToOrdinaryBranch`，断言
//	"走**普通拒绝分支**"（即 ⚠️ AI refusal 状态行）。那条分支已因误报被整体删除
//	（真机 T-1：模型如实答「我没有这个工具…所以我无法调用」= T-1 的期望行为，却被判成拒绝；
//
// 自动弹出的 force 提示在此场景机制上无效，只会把模型推向编造结果）。
//
//	⇒ 现在的契约是**静默**。原用例注释自己写明了这是"收敛到方案 A 的那一步的入口"——本单就是那一步。
//
// 仍然承重的部分（不得再放宽）：⛔ 需要**两个**条件同时成立；只有结构化事件、散文没谈约束时不出 ⛔。
// 另一半（散文谈约束但没有拒绝词也要出 ⛔）见 `.go` T3。
func TestR4_StructuredOnlyWithoutProseFallsBackToSilence(t *testing.T) {
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{r4SandboxDenied()}})
	mm := updated.(Model)
	if mm.roundDeniedKind != tools.FailureKindSandboxDenied {
		t.Fatalf("前置失败：结构化拒绝未入账，实际 %q", mm.roundDeniedKind)
	}

	// 不谈沙箱/权限/网络。
	mm.checkAISafety("我无法确定用哪种方式实现。", false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))

	if strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("散文未谈安全约束 ⇒ 合取不成立，不得断言策略拦截: %q", transcript)
	}
	if strings.Contains(transcript, "AI refusal") {
		t.Fatalf(" 后散文不得单独产生任何拒绝断言（静默）: %q", transcript)
	}
}

// TestR4_LedgerIsScopedToUserTurn · 账本按**用户回合**清理（resetAIDetectors，经生产入口 sendInput）。
//
// 否则上一回合发生过的沙箱拒绝会一直挂在账本上，把下一回合任何"提到沙箱"的正常答复
// 都判成策略拦截——即把本 bug 从"误报一次"升级成"此后永久误报"。
//
// ★ **有意翻转**后半段：原断言"清空后应回到普通拒绝分支并给出 force 路径"，
// 那条分支已删 ⇒ 现在要求**静默**。清理生效的证明方式改为：同一段散文在
// ① 已入账时 ⇒ 出 ⛔；② 清空后 ⇒ **什么都不出**。（两者可判别 ⇒ 仍能证明清理真的生效，
// 而不是"账本恰好为空"。）
func TestR4_LedgerIsScopedToUserTurn(t *testing.T) {
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{r4SandboxDenied()}})
	mm := updated.(Model)
	if mm.roundDeniedKind == "" {
		t.Fatal("前置失败：真实 FailureKind 应已入账")
	}

	// ① 已入账 + 谈约束 ⇒ 必须出 ⛔（证明判据本身在线，不是恒不出）
	mm.checkAISafety(r4SecurityProse, false)
	if !strings.Contains(ansi.Strip(strings.Join(mm.Messages(), "\n")), "force confirmation cannot override it") {
		t.Fatal("前置失败：结构化事件在账本上时，谈约束的散文必须出 ⛔（否则本用例失去对照）")
	}

	// 生产入口：新用户回合开始（sendInput 内部调 resetAIDetectors）。
	mm.textarea.SetValue("换个方式再试一次")
	nextModel, _ := mm.sendInput()
	next := nextModel.(Model)
	if next.roundDeniedKind != "" {
		t.Fatalf("新用户回合必须清空账本，实际 %q（上一回合的拒绝会污染这一回合的判定）", next.roundDeniedKind)
	}

	// ② 清空后同一段散文 ⇒ 静默（既不出 ⛔，也不出任何拒绝断言）。
	// ⚠️ 只看**本步新增**的消息：① 的 ⛔ 与用户输入都留在同一个消息列表里，
	// 直接搜全量会被上一步的残留误伤（这不是判据松紧问题，是断言范围问题）。
	base := len(next.Messages())
	next.checkAISafety(r4SecurityProse, false)
	transcript := ansi.Strip(strings.Join(next.Messages()[base:], "\n"))
	if strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("账本已按回合清空 ⇒ 纯散文不得断言策略拦截: %q", transcript)
	}
	if strings.Contains(transcript, "AI refusal") {
		t.Fatalf(" 后散文不得单独产生任何拒绝断言（静默）: %q", transcript)
	}
}

// TestR4_PolicyDeniedAlsoEntersLedger · 账本不止认 sandbox_denied：policy_denied 同样是权威结构化信号。
func TestR4_PolicyDeniedAlsoEntersLedger(t *testing.T) {
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{{
		ID: "w1", Name: "write_file", IsError: true,
		Output:      "Permission denied / 权限拒绝\nNo operation was run / 操作未执行",
		FailureKind: tools.FailureKindPolicyDenied,
	}}})
	mm := updated.(Model)
	if mm.roundDeniedKind != tools.FailureKindPolicyDenied {
		t.Fatalf("policy_denied 应入账，实际 %q", mm.roundDeniedKind)
	}

	mm.checkAISafety(r4SecurityProse, false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))
	if !strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("策略拒绝成立 ⇒ 必须走 ⛔ 分支: %q", transcript)
	}
}

// TestR4_PlainFailureDoesNotEnterLedger · 零值保持零值（IK2 同口径）：
// 普通执行失败（非安全拒绝）不得入账，否则会凭空造出"策略拦截"。
func TestR4_PlainFailureDoesNotEnterLedger(t *testing.T) {
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{{
		ID: "b2", Name: "bash", IsError: true, Output: "exit status 127",
	}}})
	mm := updated.(Model)
	if mm.roundDeniedKind != "" {
		t.Fatalf("普通失败不得入账，实际 %q", mm.roundDeniedKind)
	}

	mm.checkAISafety(r4SecurityProse, false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))
	if strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("本轮无结构化拒绝事件 ⇒ 纯散文不得断言策略拦截: %q", transcript)
	}
}
