package tui

// askuser_freetext_typing_test.go — 判别测试：补充输入子态下「按键真能进 textarea」。
//
// # 为什么单开一个文件
//
// 既有 askuser_free_text_test.go 的 J3 只断言 `choiceFreeTextMode == true` 与
// `Textarea().Focused() == true`，J4 则直接用 `Textarea().SetValue(...)` 注入文本 ——
// **两条都没走「敲键 → 进输入框」这条生产路径**。于是出现最危险的一类假绿：
// 状态被置上了、单测全绿，而用户按 i 之后一个字都打不进（手测现象「按 i 并不能输入我的答案」）。
//
// ★ 本文件的判据全部从生产入口进入（`checkAskUserChoice` → `SetPendingChoice` → `SetState`），
// 再发**真实 `tea.KeyMsg`**，断言落在 `Textarea().Value()` 上 —— 修前必红。
//
// # 与输入合并器的关系（测试为什么需要 typeAtHumanPace）
//
// 生产里按键先过输入合并器（input_coalesce.go）：间隔 < 24ms 的 KeyRunes 会被缓冲，
// 由 8ms 的 flush tick 批量落地。单测里没有真实流逝的时间，连续两次按键必然被判成「粘贴突发」
// ⇒ 字符停在缓冲里、`Value()` 看不见（`tea.Tick` 返回的 cmd 在单测中不会被执行）。
// typeAtHumanPace 在每次按键前把「上次按键时间」推远一秒 —— 这只是**模拟时间流逝**（等价于用户
// 隔一秒敲一下），不是替被测代码制造前提、更不是 mock 掉守卫。合并器缓冲路径另由
// TestN127_PasteBurstLosesNoCharacter 用生产的同步刷新函数覆盖。

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// typeAtHumanPace 逐 rune 发送真实 KeyRunes 事件，并在每次按键前把合并器时间戳推远一秒，
// 使按键走「正常打字」路径（直接进 textarea），而不是被误判为粘贴突发留在缓冲里。
func typeAtHumanPace(t *testing.T, m *Model, text string) *Model {
	t.Helper()
	for _, r := range text {
		m.inputCoalesce.lastTime = time.Now().Add(-time.Second)
		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		next := updatedModel.(Model)
		next.reassignServices()
		m = &next
	}
	return m
}

// enterAskUserFreeTextMode 按 i 进入补充输入子态，并断言确实进去了。
func enterAskUserFreeTextMode(t *testing.T, m *Model) *Model {
	t.Helper()
	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	updated := updatedModel.(Model)
	updated.reassignServices()
	if !updated.choiceFreeTextMode {
		t.Fatal("前置失败：按 i 未进入补充输入子态")
	}
	return &updated
}

// TestN127_TypedCharactersReachTextarea 是 的核心判别器。
//
// 修前：`StateWaitingChoice` 分支末尾无条件 `return m, nil` 把所有字符键吞掉
// ⇒ 敲完 "OAuth2" 后 `Textarea().Value()` 仍是 `""`（红）。
// 修后：字符经 routeKeyToComposerTextarea 进入 textarea ⇒ `"OAuth2"`（绿）。
func TestN127_TypedCharactersReachTextarea(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))

	m = typeAtHumanPace(t, m, "OAuth2")

	if got := m.uiService.Textarea().Value(); got != "OAuth2" {
		t.Fatalf("★ 补充输入子态下敲入的字符必须进入 textarea：want %q, got %q\n"+
			"（修前症状：字符被 StateWaitingChoice 分支末尾的无条件 return 静默丢弃）", "OAuth2", got)
	}
}

// TestN127_PasteBurstLosesNoCharacter 覆盖另一条输入形态：单事件多 rune（非 bracketed 终端把粘贴拆包）。
// 合并器可能把字符留在缓冲里 —— 用生产的 flushCoalescedInputSync 落地后再断言**一个字符都没丢**。
func TestN127_PasteBurstLosesNoCharacter(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("OAuth2")})
	m = func() *Model {
		u := updatedModel.(Model)
		u.reassignServices()
		return &u
	}()

	// 生产路径：突发缓冲由 8ms flush tick 落地；单测不跑 tea 循环，改用同一个生产函数同步落地。
	m.flushCoalescedInputSync()

	if got := m.uiService.Textarea().Value(); got != "OAuth2" {
		t.Fatalf("★ 突发（粘贴）输入不得丢字符：want %q, got %q", "OAuth2", got)
	}
}

// TestN127_NavigationKeysAreAlsoTyped 钉「必须放行**全部**字符键」这条边界：
// j / k 既是选项导航键、也是普通字母。子态下它们必须当字符用 —— 否则 `json` 的 j、`Key` 的 k 会被继续吞。
//
// 修前：case "down","j" 命中 ⇒ 字符丢失、且把高亮往下挪（红）。
func TestN127_NavigationKeysAreAlsoTyped(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))

	m = typeAtHumanPace(t, m, "json")

	if got := m.uiService.Textarea().Value(); got != "json" {
		t.Fatalf("★ 含 j/k 的字符必须当字符处理：want %q, got %q", "json", got)
	}
	if got := m.toolService.ChoiceIdx(); got != 0 {
		t.Fatalf("子态下 j 不得挪动选项高亮：want 0, got %d", got)
	}
}

// TestN127_ArrowKeysDoNotMoveHighlightInSubmode 子态下方向键属于文本光标操作，不得改选项高亮。
//
// 修前：case "up","k" 命中 ⇒ 高亮从 1 退回 0（红）。
func TestN127_ArrowKeysDoNotMoveHighlightInSubmode(t *testing.T) {
	m := newAskUserPendingModel(t)
	m.toolService.SetChoiceIdx(1) // 高亮第 2 项，给方向键留出可观察的位移空间
	m = enterAskUserFreeTextMode(t, m)

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if got := updated.toolService.ChoiceIdx(); got != 1 {
		t.Fatalf("子态下 ↑ 不得挪动选项高亮：want 1, got %d", got)
	}
}

// TestN127_EmptyEnterKeepsPanelOpen 空答案守卫：
// 什么都不填就 enter，不得闭合面板、不得回传空答案给模型（修前 cmd 非 nil、面板被关掉 ⇒ 红）。
func TestN127_EmptyEnterKeepsPanelOpen(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if cmd != nil {
		t.Fatalf("★ 空内容 enter 不得回传结果（否则模型收到空答案白跑一轮）：got cmd = %#v", cmd())
	}
	if !updated.choiceFreeTextMode {
		t.Fatal("空内容 enter 后应留在补充输入子态")
	}
	if updated.toolService.PendingChoice() == nil {
		t.Fatal("空内容 enter 后 PendingChoice 应保留")
	}
}

// TestN127_TypedThenEnterSubmitsTypedText 端到端：敲入的字符必须原样成为回传的 FreeText。
// 这是用户视角的那条不变量 —— 「我打进去的字，就是模型收到的字」。
func TestN127_TypedThenEnterSubmitsTypedText(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))
	m = typeAtHumanPace(t, m, "OAuth2")

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if cmd == nil {
		t.Fatal("非空内容 enter 应返回选择结果命令")
	}
	result, ok := cmd().(choiceResultMsg)
	if !ok {
		t.Fatalf("应返回 choiceResultMsg，got %#v", cmd())
	}
	if result.FreeText != "OAuth2" {
		t.Fatalf("回传的 FreeText 必须是用户敲入的内容：want %q, got %q", "OAuth2", result.FreeText)
	}
	if result.Answer != "jwt" {
		t.Fatalf("高亮仍在第 1 项时 Answer 应保留 jwt：got %q", result.Answer)
	}
	if updated.choiceFreeTextMode {
		t.Fatal("提交后应复位补充输入子态")
	}
}

// TestN127_EscInSubmodeReturnsToSelection 反向保（回归钉）：esc 属显式控制键，**不转发**给 textarea，
// 仍走「先退回选择态、不取消」的既有语义 —— 我这次动的正是这条分支，必须钉住。
func TestN127_EscInSubmodeReturnsToSelection(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if cmd != nil {
		t.Fatalf("子态 esc 只应退回选择态，不应产生结果命令：got %#v", cmd())
	}
	if updated.choiceFreeTextMode {
		t.Fatal("子态 esc 应退出补充输入子态")
	}
	if updated.toolService.PendingChoice() == nil {
		t.Fatal("子态 esc 不应取消整个提问（PendingChoice 应保留）")
	}
}

// TestN129_TypedTextIsVisibleInChoiceUI 是 的核心判别器：用户视角的不变量
// ——「我按 i 打进去的字，屏幕上必须能看到」。
//
// 修前：renderChoiceUI 在补充输入子态只渲染提示文案（"Type your answer…"），
// **从不渲染 textarea 的内容**；而 View() 在 StateWaitingChoice 用本函数
// **完全替换**输入区（app.go:2781）⇒ 字符确实进了 textarea（已保证），
// 但屏幕上**没有任何地方**显示它 —— 手测现象「按 i 之后打的字看不见」。
func TestN129_TypedTextIsVisibleInChoiceUI(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))
	m = typeAtHumanPace(t, m, "VISIBLE_ANSWER_XYZ")

	// 前置：字符确实进了 textarea（的保证，避免把两个缺陷混在一起）。
	if got := m.uiService.Textarea().Value(); got != "VISIBLE_ANSWER_XYZ" {
		t.Fatalf("前置失败：字符未进 textarea，got %q", got)
	}

	out := m.renderChoiceUI()
	if !strings.Contains(out, "VISIBLE_ANSWER_XYZ") {
		t.Fatalf("★ 补充输入子态下，用户敲入的内容必须在选择 UI 内可见：\n%s\n"+
			"（修前症状：renderChoiceUI 只渲染提示、不渲染 textarea，用户看不到自己打的字）", out)
	}
}

// TestN129_TypedTextVisibleInFullView 端到端：用户真实看到的是 View() 的输出，
// 整屏拼接后也必须含用户敲入的字（防止「renderChoiceUI 修了但没被 View 用上」）。
func TestN129_TypedTextVisibleInFullView(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))
	m.uiService.SetReady(true) // View() 未 Ready 时只返回 loading 占位
	m = typeAtHumanPace(t, m, "VISIBLE_ANSWER_XYZ")

	out := m.View()
	if !strings.Contains(out, "VISIBLE_ANSWER_XYZ") {
		t.Fatalf("★ 整屏 View() 必须显示用户敲入的内容：\n%s", out)
	}
}

// TestN130_FreeTextSubmitEchoesAnswer 判别器：提交自由输入后，**对话里必须回显**用户答案。
//
// 缺陷（2026-09-22 真机，会话存档 01a0c516 L31）：自由输入提交路径只把答案回传给模型，
// 屏幕上**什么都不加**（选项路径有 ` → <label>` 回显，本路径没有）。用户敲完 enter 后
// 只见选择框消失、输入框里的字还在 ⇒ 判断为"没送出去 / 没回复"（实际答案已送达模型）。
func TestN130_FreeTextSubmitEchoesAnswer(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))
	m = typeAtHumanPace(t, m, "回复我hello")

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if cmd == nil {
		t.Fatal("前置失败：非空内容 enter 应返回选择结果命令")
	}
	all := strings.Join(updated.messages.All(), "\n")
	if !strings.Contains(all, "回复我hello") {
		t.Fatalf("★ 提交后必须在对话里回显用户的自由输入（否则用户以为输入没生效）：\n%s", all)
	}
}

// TestN130_FreeTextSubmitClearsComposer 判别器：提交后输入框不得残留已提交内容。
//
// 真机现象：`i` 子态里敲的字在提交、选择框消失之后**仍留在输入框**
// （提交路径只 Blur、未清空）—— 这是"我的输入到底送出去没有"的主要误导来源。
func TestN130_FreeTextSubmitClearsComposer(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))
	m = typeAtHumanPace(t, m, "回复我hello")
	if got := m.uiService.Textarea().Value(); got != "回复我hello" {
		t.Fatalf("前置失败：字符未进 textarea，got %q", got)
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if got := updated.uiService.Textarea().Value(); got != "" {
		t.Fatalf("★ 提交后输入框必须清空，实际残留 %q（用户会误以为输入没送出去）", got)
	}
}

// TestN130_EscBackClearsComposer 反例守卫：esc 退回选择态同样不得留下草稿。
//
// 语义依据：再次进入子态（`i` 或选中 Other）本就会 SetValue("")，草稿早已被放弃
// ⇒ 留在输入框里只会在选择框消失后变成残留文字。before/after 都必须绿（esc 的
// 「退回选择态、不取消提问」语义由 TestN127_EscInSubmodeReturnsToSelection 钉住）。
func TestN130_EscBackClearsComposer(t *testing.T) {
	m := enterAskUserFreeTextMode(t, newAskUserPendingModel(t))
	m = typeAtHumanPace(t, m, "half-typed")

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	updated.reassignServices()

	if cmd != nil {
		t.Fatal("子态 esc 不应产生结果命令（既有语义不得改变）")
	}
	if updated.choiceFreeTextMode {
		t.Fatal("子态 esc 应退回选择态")
	}
	if updated.toolService.PendingChoice() == nil {
		t.Fatal("子态 esc 不应取消提问（PendingChoice 应保留）")
	}
	if got := updated.uiService.Textarea().Value(); got != "" {
		t.Fatalf("★ esc 退回选择态后输入框不得残留草稿，实际 %q", got)
	}
}
