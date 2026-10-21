package tui

// askuser_free_text_test.go — AskUser「补充自由输入」的生产路径守卫（2026-09-21 · 对齐 同类 CLI request_user_input）。
//
// 覆盖三件事（对应改码方案判据 J3/J4/J6）：
// - J3：按 i 键进入「补充输入」子态（choiceFreeTextMode=true + textarea 聚焦）；
// - J4：补充后 enter 回传同时带 Answer + FreeText（两者都保留）；
// - J6：本地菜单（权限/会话/安全套餐）不受污染 —— 按 i 不进入补充态、不追加 Other。
//
// 判别性（production-path-tdd）：从真实入口触发 —— 构造 AskUser tools.Result → checkAskUserChoice
// → SetPendingChoice + SetState(StateWaitingChoice)，再发真实 tea.KeyMsg；不 mock 守卫。

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// newAskUserPendingModel 从生产入口构造一个处于 StateWaitingChoice、带着 AskUser PendingChoice 的 Model。
// 走 checkAskUserChoice（生产拦截点）而非手工拼 PendingChoice，确保 Other 追加等逻辑被真实执行。
//
// 2026-09-23 · ：本夹具显式声明 `AllowFreeText: true`，与 `newAskUserPanelFromRealSession`
// （`false`）配成一对 —— 证明**追加 Other 与该字段无关**（两条路径都追加）。
// 曾让追加以该字段为条件（已撤回）：同类 CLI 的 `is_other` 由服务端强制 true、
// 同类 CLI 亦无条件追加（[`...textOptions, otherOption]`），模型本就关不掉自填口。
func newAskUserPendingModel(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)

	raw, _ := json.Marshal(tools.AskUserResult{
		Question:      "用 JWT 还是 Session？",
		Category:      "dialogue-clarify",
		DefaultIdx:    0,
		AllowFreeText: true,
		Options: []tools.AskUserOption{
			{Label: "JWT", Value: "jwt", Description: "无状态 Token"},
			{Label: "Session", Value: "session"},
		},
	})
	results := []tools.Result{{ID: "ask-1", Name: "AskUser", Output: string(raw)}}
	choices, ok := m.checkAskUserChoice(results)
	if !ok || len(choices) != 1 {
		t.Fatalf("前置失败：checkAskUserChoice 应返回 1 个 choice，got %d (ok=%v)", len(choices), ok)
	}
	m.toolService.SetPendingChoice(&choices[0])
	m.toolService.SetChoiceIdx(0)
	m.sessionService.SetState(StateWaitingChoice)
	m.reassignServices()
	return m
}

// TestAskUserFreeText_IKeyEntersFreeTextMode (J3)
// 按 i 应进入补充输入子态：choiceFreeTextMode=true 且 textarea 聚焦。
func TestAskUserFreeText_IKeyEntersFreeTextMode(t *testing.T) {
	m := newAskUserPendingModel(t)

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if !updated.choiceFreeTextMode {
		t.Fatal("按 i 后应进入补充输入子态（choiceFreeTextMode=true）")
	}
	if !updated.uiService.Textarea().Focused() {
		t.Fatal("按 i 后 textarea 应聚焦")
	}
	if cmd != nil {
		t.Fatalf("进入补充态不应立即产生回传命令，got %#v", cmd)
	}
	// PendingChoice 应仍保留（未提交）
	if updated.toolService.PendingChoice() == nil {
		t.Fatal("进入补充态后 PendingChoice 应仍保留，等待用户输入")
	}
}

// TestAskUserFreeText_EnterSubmitsBoth (J4)
// 补充自由文本后按 enter，回传的 choiceResultMsg 应同时带 Answer（选中的预设项）和 FreeText。
func TestAskUserFreeText_EnterSubmitsBoth(t *testing.T) {
	m := newAskUserPendingModel(t)

	// 进入补充态
	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	updated := updatedModel.(Model)
	updated.reassignServices()

	// 输入自由文本
	updated.uiService.Textarea().SetValue("其实我们团队规范是用 OAuth2")
	// 当前高亮仍是第 0 项（jwt）
	updatedModel, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = updatedModel.(Model)
	updated.reassignServices()

	if cmd == nil {
		t.Fatal("enter 应返回选择结果命令")
	}
	result, ok := cmd().(choiceResultMsg)
	if !ok {
		t.Fatalf("应返回 choiceResultMsg，got %#v", cmd())
	}
	if result.Cancelled {
		t.Fatal("补充提交不应是取消")
	}
	if result.Answer != "jwt" {
		t.Fatalf("Answer 应保留选中的预设项 jwt，got %q", result.Answer)
	}
	if result.FreeText != "其实我们团队规范是用 OAuth2" {
		t.Fatalf("FreeText 应保留补充文本，got %q", result.FreeText)
	}
	if updated.choiceFreeTextMode {
		t.Fatal("提交后应复位补充输入子态")
	}
}

// TestAskUserFreeText_OtherOptionEntersFreeTextMode (J2 交互面)
// 选中 Other（None of the above）应进入补充输入子态。
func TestAskUserFreeText_OtherOptionEntersFreeTextMode(t *testing.T) {
	m := newAskUserPendingModel(t)

	pending := m.toolService.PendingChoice()
	if pending == nil {
		t.Fatal("前置失败：无 PendingChoice")
	}
	// 高亮移到最后一个选项（Other）
	otherIdx := len(pending.Options) - 1
	if pending.Options[otherIdx].Value != otherOptionValue {
		t.Fatalf("最后一个选项应为 Other，got %+v", pending.Options[otherIdx])
	}
	m.toolService.SetChoiceIdx(otherIdx)

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if !updated.choiceFreeTextMode {
		t.Fatal("选中 Other 后应进入补充输入子态")
	}
	if cmd != nil {
		t.Fatalf("选中 Other 不应立即回传，got %#v", cmd)
	}
}

// TestAskUserFreeText_LocalMenuNotPolluted (J6)
// 本地菜单（权限模式选择器）不追加 Other、按 i 不进入补充态。
func TestAskUserFreeText_LocalMenuNotPolluted(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(0)
	m.handleCommand("/permissions")

	pending := m.toolService.PendingChoice()
	if pending == nil {
		t.Fatal("前置失败：/permissions 未打开选择器")
	}
	// 本地菜单不应追加 Other
	if hasOtherOption(pending.Options) {
		t.Fatalf("本地菜单不应追加 Other，Options=%+v", pending.Options)
	}

	// 按 i 不应进入补充态（本地菜单无 Other）
	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	updated := updatedModel.(Model)
	updated.reassignServices()
	if updated.choiceFreeTextMode {
		t.Fatal("本地菜单按 i 不应进入补充输入子态")
	}
}
