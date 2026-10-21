// .go — （T-3）在 internal/tui 一侧的判别性验收。
//
// 被修的设计错误（真机手测 T-1 踩中，2026-09-19 00:38）：
//
//	模型**如实回答**「我查看了当前可用的工具列表，没有找到 WebSearch 工具…所以**我无法**直接调用它」
//	—— 这正是 T-1 票面要求的期望行为（`检索联网_重建后手测总清单_2026-09-18.md:117`）——
//	却因为命中散文关键词「我无法」被判成「AI 拒绝执行」，并弹出 force execute 提示。
//
//	该提示在此场景**机制上无效**：模型不是主观拒绝、是没有能力，注入「请执行而非拒绝」后
//	它手上仍无该工具 ⇒ 只会把它推向**编造搜索结果**，即 T-1 票面明列的失败态②（幻觉）。
//	⇒ 判据自相矛盾：**T-1 通过 ⟺ 必然报误报**。
//
// 方案 A（用户 2026-09-19 01:2x 放行「解锁窗口，按方案 A 处理」）：
//
//	① 删掉**散文自动断言**（原 `DetectRefusal` 命中 ⇒ 打印 ⚠️ + 弹提示）；
//	② ⛔ 分支收敛为 R4 文档化的**二项合取**：
//
// 本轮有结构化拒绝事件（roundDeniedKind 非空，由 toolResultMsg 漏斗写入）
// ∧ 散文在谈安全约束（ai.MentionsSecurityConstraint，**辅助**信号）；
//
//	③ **保留**用户主动输入「我确认强制执行」/「force execute」触发的强制覆盖（不自动弹）。
//
// ⇒ 这一半**不在本文件重复判定**，由既有的 `TestForceExecuteConfirmationIsVisibleInTranscript`
// （`tui_regression_test.go`，本单未动）承重。
//
// ★ 本文件的判别力边界（如实登记，勿当全绿）：
// - **T1 / T3 是判别器** —— 修前必红、修后转绿（先红后绿已实测，见回执 §判别力自证）。
// - **T2 不是判别器**，是**回归守卫**（修前就绿）：锁 R4 的承重判据不得被本次改动连坐删掉。
//
// ★ 反向依赖（先看这里再改）：`r4_refusal_ledger_test.go` 的
// `TestR4_StructuredOnlyWithoutProseFallsBackToOrdinaryBranch` 与 `TestR4_LedgerIsScopedToUserTurn`、
// `tui_regression_test.go` 的 `TestSecurityProseAloneDoesNotClaimPolicyBlock` /
// `TestOrdinaryRefusalStatusIsBilingual` 断言的是**被删掉的那条分支** ⇒ 本单**有意翻转**它们
// （见回执「有意翻转清单」）。R4 的**正例**
// （`TestR4_StructuredDenialDrivesSecurityPolicyBanner`）**未动、必须继续绿**。
package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/charmbracelet/x/ansi"
)

// n039MissingToolProse 是 T-1 真机输出的**同形文本**（不是编出来的场景）：
// 含「我无法」= 旧关键词表的命中项，且**本轮没有任何结构化拒绝事件**。
const n039MissingToolProse = "我查看了当前可用的工具列表，没有找到 WebSearch 工具。" +
	"我目前有的相关工具是 WebFetch。WebSearch 不在我的工具集中，所以我无法直接调用它。"

// n039SandboxTalkWithoutRefusalWord 谈到了沙箱约束，但**不含任何拒绝词**。
// 它用来钉 R4 文档化的二项合取的**第二项**：散文只需「在谈约束」，不需要「在拒绝」。
const n039SandboxTalkWithoutRefusalWord = "当前沙箱配置为 workspace-write，因此 curl 会失败；你可以改成显式放行。"

// TestN039_HonestMissingToolReportIsNotAnAIRefusal · ★判别器（T1）。
//
// 本轮**无**结构化拒绝事件（roundDeniedKind 为空）⇒ 模型如实说「我没有这个工具、无法调用」
// **不得**产生任何「AI 拒绝执行」断言，也**不得**弹出强制覆盖提示。
func TestN039_HonestMissingToolReportIsNotAnAIRefusal(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.roundDeniedKind != "" {
		t.Fatalf("前置失败：新会话账本应为空，实际 %q", m.roundDeniedKind)
	}

	m.checkAISafety(n039MissingToolProse, false)
	transcript := ansi.Strip(strings.Join(m.Messages(), "\n"))

	if strings.Contains(transcript, "AI refusal") {
		t.Fatalf("如实报告工具不存在不是 AI 拒绝；把它判成拒绝 = 与 T-1 票面期望直接冲突"+
			"（这正是  的缺陷本体）: %q", transcript)
	}
	if strings.Contains(transcript, "To override an AI-only refusal") {
		t.Fatalf("不得弹出强制覆盖提示：该提示在此场景机制上无效，"+
			"只会把模型推向「用户强制压力下编造结果」（T-1 失败态②）: %q", transcript)
	}
}

// TestN039_StructuredDenialStillBannerWithRefusalWordProse · 回归守卫（T2，**不是判别器**）。
//
// 结构化拒绝事件已入账 ⇒ 必须仍走 ⛔ 分支（R4 的承重判据不得被本次改动连坐删掉）。
// 这段散文同时含拒绝词与约束词，是真机上最常见的形态。
func TestN039_StructuredDenialStillBannerWithRefusalWordProse(t *testing.T) {
	m := newTUIRegressionModel(t)
	if m.roundDeniedKind != "" {
		t.Fatalf("前置失败：新会话账本应为空，实际 %q", m.roundDeniedKind)
	}
	// 经**生产入口**（toolResultMsg 漏斗）入账，不手工置字段。
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{r4SandboxDenied()}})
	mm := updated.(Model)
	if mm.roundDeniedKind != tools.FailureKindSandboxDenied {
		t.Fatalf("前置失败：结构化拒绝未入账，实际 %q", mm.roundDeniedKind)
	}

	mm.checkAISafety(r4SecurityProse, false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))

	if !strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("结构化拒绝 ∧ 散文谈约束 ⇒ 必须走 ⛔ 分支（R4 承重判据）: %q", transcript)
	}
	if strings.Contains(transcript, "AI refusal") {
		t.Fatalf("⛔ 分支应独占输出，不得同时出现已删除的普通拒绝提示: %q", transcript)
	}
}

// TestN039_BannerNoLongerRequiresRefusalWording · ★判别器（T3）。
//
// 修前：⛔ 分支被错误地**嵌在** `DetectRefusal` 命中之内 ⇒ 「谈约束但没在拒绝」的散文拿不到 ⛔
// （即 R4 文档写的二项合取实际是三项，第三项是冗余的散文拒绝词）。
// 修后：收敛为文档化的二项合取 ⇒ 该散文**必须**拿到 ⛔。
func TestN039_BannerNoLongerRequiresRefusalWording(t *testing.T) {
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(toolResultMsg{results: []tools.Result{r4SandboxDenied()}})
	mm := updated.(Model)
	if mm.roundDeniedKind != tools.FailureKindSandboxDenied {
		t.Fatalf("前置失败：结构化拒绝未入账，实际 %q", mm.roundDeniedKind)
	}

	mm.checkAISafety(n039SandboxTalkWithoutRefusalWord, false)
	transcript := ansi.Strip(strings.Join(mm.Messages(), "\n"))

	if !strings.Contains(transcript, "force confirmation cannot override it") {
		t.Fatalf("R4 判据 = 结构化事件 ∧ 散文谈约束（**不需要**散文在拒绝）⇒ 必须出 ⛔: %q", transcript)
	}
}
