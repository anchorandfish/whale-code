package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/typeahead"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestSlashCompletionUsesRegistryPriorityAndBilingualDescriptions(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("/")
	m.updateTypeaheadSuggestions()

	state := m.suggestionState
	if !state.Visible {
		t.Fatal("slash command popup should be visible")
	}
	if len(state.Completions) <= typeahead.MaxSuggestions {
		t.Fatalf("slash popup returned %d commands; it must not use the %d-item file limit",
			len(state.Completions), typeahead.MaxSuggestions)
	}
	// /cost 已全链路移除：补全列表（registry.All 转换）不得再出现
	if findCompletion(state.Completions, "cost") != nil {
		t.Fatal("cost command is still suggested in slash completion")
	}
	if got := state.Completions[0].DisplayText; got != "model" {
		t.Fatalf("first common command = %q, want model", got)
	}
	if got := state.Completions[1].DisplayText; got != "permissions" {
		t.Fatalf("second common command = %q, want permissions", got)
	}

	permissions := findCompletion(state.Completions, "permissions")
	if permissions == nil {
		t.Fatal("permissions command is missing")
	}
	if permissions.Description == "" || permissions.SecondaryDescription == "" {
		t.Fatalf("permissions descriptions = %q / %q", permissions.Description, permissions.SecondaryDescription)
	}
	if plan := findCompletion(state.Completions, "plan"); plan != nil {
		t.Fatal("stub commands should not be suggested")
	}

	view := state.renderCompletionPopup(120)
	plain := ansi.Strip(view)
	// G12：`permissions` 的英文描述已按用户要求改为**写明轴归属**
	// （旧值 "View or change what Whale Code can do" 把它写成了"总的权限开关"）。
	// 本条断言随之改为**结构性**的，不再写死措辞：
	// ① 英文描述里必须点出沙箱轴（说明它不含沙箱）
	// ② 中文副描述必须同样点出"不含沙箱"
	// ③ 弹窗里**英文在前、中文在后**（本条测试的原意）
	if !strings.Contains(permissions.Description, "sandbox") {
		t.Errorf("G12：permissions 英文描述必须点明它与沙箱轴的关系（不含沙箱）；got %q",
			permissions.Description)
	}
	if !strings.Contains(permissions.SecondaryDescription, "不含沙箱") {
		t.Errorf("G12：permissions 中文副描述必须点明不含沙箱；got %q", permissions.SecondaryDescription)
	}
	idxEN := strings.Index(plain, permissions.Description)
	// 取前 3 个**符文**（不是 3 个字节！中文字符多字节，按字节切会切出非法 UTF-8）。
	zhPrefix := string([]rune(permissions.SecondaryDescription)[:3])
	idxZH := strings.Index(plain, zhPrefix)
	if idxEN < 0 || idxZH < 0 {
		t.Fatalf("弹窗里必须同时出现英文描述与中文副描述，got %q", plain)
	}
	if idxEN > idxZH {
		t.Fatalf("弹窗必须先英后中（英文 idx=%d 应小于中文 idx=%d），got %q", idxEN, idxZH, plain)
	}
	if !utf8.ValidString(view) {
		t.Fatal("popup must remain valid UTF-8")
	}
}

func TestSlashCompletionFiltersMultiwordCommandsAndClosesForArguments(t *testing.T) {
	m := newTUIRegressionModel(t)

	m.textarea.SetValue("/session sw")
	m.updateTypeaheadSuggestions()
	if got := completionDisplayNames(m.suggestionState.Completions); len(got) != 1 || got[0] != "session switch" {
		t.Fatalf("multiword suggestions = %v", got)
	}

	m.textarea.SetValue("/permissions ")
	m.updateTypeaheadSuggestions()
	if m.suggestionState.Visible {
		t.Fatal("exact command followed by a space should close the command popup")
	}

	m.textarea.SetValue("hello /")
	m.updateTypeaheadSuggestions()
	if m.suggestionState.Visible {
		t.Fatal("slash in prose should not open the command popup")
	}
}

func TestSlashCompletionKeyboardInteractions(t *testing.T) {
	t.Run("enter completes without executing", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/per")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/permissions " {
			t.Fatalf("textarea = %q", got)
		}
		if updated.suggestionState.Visible {
			t.Fatal("popup should close after completion")
		}
		if updated.sessionService.State() != StateIdle {
			t.Fatal("selecting a command must not execute it")
		}
	})

	t.Run("tab completes common prefix", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/per")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/permissions " {
			t.Fatalf("textarea = %q", got)
		}
	})

	t.Run("tab does not add a separator when the prefix cannot grow", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/s")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/s" {
			t.Fatalf("tab changed an ambiguous prefix to %q", got)
		}
		if !updated.suggestionState.Visible {
			t.Fatal("ambiguous prefix should keep the popup open")
		}
	})

	// 2026-09-20（会话多开）：`session` 现在是**已注册的父命令**（裸 /session 打开选择器），
	// 于是 /sess + Tab 与既有的同类父命令 `checkpoint` **行为对齐**：
	// 实测 `/chec` + Tab ⇒ `"/checkpoint "` 且弹窗关闭；`/sess` + Tab ⇒ `"/session "` 同理。
	// 旧断言 `"/session"` + 弹窗保持可见，编码的是「裸 session 尚未注册」这一**旧事实**
	// （当时候选里没有 `session` 本身，公共前缀 = "session" 却不等于任何候选 ⇒ 走
	// applySlashPrefix 分支）。注册父命令后该分支不再命中，属预期语义变化，非缺陷。
	// 产物面判别器（该输入是否**可执行**）放在 TestSessionCommandDispatchOpensPicker，
	// 那里有真实会话可载入，避免本用例为了断言副作用而引入 session store 依赖。
	t.Run("tab completes a registered parent command", func(t *testing.T) {
		// ★ ：`/session` 根已收回隐藏（v1 无承诺）⇒ `/sess` + Tab 不再
		// 补出 "/session "（旧断言编码的是「session 父命令可见」这一旧事实，有意翻转）。
		// 「父命令 Tab 补全」机制的**正向判别**改用仍可见的父命令 /checkpoint 承载
		// （既有实测先例：/chec + Tab ⇒ "/checkpoint " 且弹窗关闭，与 /checkpoint 对齐语义同源）。
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/chec")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/checkpoint " {
			t.Fatalf("tab should complete the registered parent command, got %q", got)
		}
		if updated.suggestionState.Visible {
			t.Fatal("completing a full command should close the popup (parity with /checkpoint)")
		}
	})

	t.Run("tab does not complete a reclaimed root command", func(t *testing.T) {
		// 反向对照：收回的父命令不得被 Tab 补全成 "/session "（可见面收敛的结果面）。
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/sess")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got == "/session " {
			t.Fatal("/session 已收回隐藏，Tab 不应再补出 \"/session \"（手输仍可执行，但提示面不承诺）")
		}
	})

	t.Run("escape closes", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		updated := updatedModel.(Model)
		if updated.suggestionState.Visible {
			t.Fatal("escape should close the popup")
		}
		if got := updated.textarea.Value(); got != "/" {
			t.Fatalf("escape changed the input to %q", got)
		}
	})

	t.Run("arrow keys move selection", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/")
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		updated := updatedModel.(Model)
		if updated.suggestionState.Selected != 1 {
			t.Fatalf("selected = %d, want 1", updated.suggestionState.Selected)
		}
	})

	t.Run("enter refreshes stale debounce state", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/per")
		m.updateTypeaheadSuggestions()
		m.textarea.SetValue("/perm")

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/permissions " {
			t.Fatalf("stale completion produced %q", got)
		}
	})

	t.Run("middle cursor replaces the complete command token", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/session sw")
		setTextareaCursorByteOffset(&m.textarea, "/session s", len("/session s"))
		m.updateTypeaheadSuggestions()
		m.suggestionState.MoveDown() // select "session switch" over "session save"

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/session switch " {
			t.Fatalf("middle-cursor completion produced %q", got)
		}
	})

	t.Run("right-hand partial token is replaced", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/perx")
		setTextareaCursorByteOffset(&m.textarea, "/per", len("/per"))
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/permissions " {
			t.Fatalf("partial-token completion produced %q", got)
		}
	})

	t.Run("completion preserves the next line", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		setTextareaCursorByteOffset(&m.textarea, "/per\nnotes", len("/per"))
		m.updateTypeaheadSuggestions()

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/permissions\nnotes" {
			t.Fatalf("completion should preserve the next line, got %q", got)
		}
	})

	t.Run("enter sends a fully typed command instead of adding a space", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/permissions")
		m.updateTypeaheadSuggestions()
		if !m.suggestionState.Visible {
			t.Fatal("popup should be visible for a fully typed command")
		}

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if updated.suggestionState.Visible {
			t.Fatal("popup should close after send")
		}
		if got := updated.textarea.Value(); got != "" {
			t.Fatalf("fully typed command should be sent on first Enter, textarea = %q", got)
		}
		history := updated.sessionService.InputHistory()
		if len(history) != 1 || history[0] != "/permissions" {
			t.Fatalf("input history = %v, want [/permissions]", history)
		}
	})

	t.Run("enter sends a fully typed prefix of multiword commands", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.textarea.SetValue("/model")
		m.updateTypeaheadSuggestions()
		if !m.suggestionState.Visible {
			t.Fatal("popup should be visible for /model")
		}

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "" {
			t.Fatalf("fully typed /model should be sent on first Enter, textarea = %q", got)
		}
		history := updated.sessionService.InputHistory()
		if len(history) != 1 || history[0] != "/model" {
			t.Fatalf("input history = %v, want [/model]", history)
		}
	})

	t.Run("enter still completes when text follows the fully typed command", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		setTextareaCursorByteOffset(&m.textarea, "/permissions\nnotes", len("/permissions"))
		m.updateTypeaheadSuggestions()
		if !m.suggestionState.Visible {
			t.Fatal("popup should be visible with cursor right after the command")
		}

		updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := updatedModel.(Model)
		if got := updated.textarea.Value(); got != "/permissions\nnotes" {
			t.Fatalf("following text must be preserved, textarea = %q", got)
		}
		if len(updated.sessionService.InputHistory()) != 0 {
			t.Fatal("completion should apply instead of sending when text follows the token")
		}
	})
}

func TestSlashCompletionDebounceAndNarrowRendering(t *testing.T) {
	m := newTUIRegressionModel(t)
	if !m.shouldDebounceTypeahead("/") {
		t.Fatal("slash input should schedule completion debounce")
	}
	if !m.shouldDebounceTypeahead("pasted /") {
		t.Fatal("pasted slash should schedule completion debounce")
	}

	state := &SuggestionState{
		Visible:        true,
		CurrentTrigger: typeahead.TriggerSlash,
		Completions: []typeahead.Completion{{
			DisplayText:          "permissions",
			Description:          "View or change what Whale Code can do",
			SecondaryDescription: "查看或切换权限",
			Trigger:              typeahead.TriggerSlash,
		}},
	}
	view := state.renderCompletionPopup(24)
	if !utf8.ValidString(view) {
		t.Fatal("narrow command popup must remain valid UTF-8")
	}
	if width := lipgloss.Width(view); width > 24 {
		t.Fatalf("narrow command popup width = %d", width)
	}
}

func TestSlashCompletionRecalculatesViewportForPopup(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.uiService.SetWidth(120)
	m.uiService.SetHeight(40)
	m.uiService.Viewport().Width = 116
	m.recalcViewportHeight()
	baselineHeight := m.uiService.Viewport().Height

	m.textarea.SetValue("/")
	m.updateTypeaheadSuggestions()
	if !m.suggestionState.Visible {
		t.Fatal("slash command popup should be visible")
	}
	if got := m.uiService.Viewport().Height; got >= baselineHeight {
		t.Fatalf("popup should reserve input rows, baseline %d got %d", baselineHeight, got)
	}

	m.textarea.SetValue("plain text")
	m.updateTypeaheadSuggestions()
	if got := m.uiService.Viewport().Height; got != baselineHeight {
		t.Fatalf("closing popup should restore viewport height, baseline %d got %d", baselineHeight, got)
	}
}

func TestSlashCompletionEscapeRestoresViewport(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.uiService.SetWidth(120)
	m.uiService.SetHeight(40)
	m.uiService.Viewport().Width = 116
	m.recalcViewportHeight()
	baselineHeight := m.uiService.Viewport().Height

	m.textarea.SetValue("/")
	m.updateTypeaheadSuggestions()
	if got := m.uiService.Viewport().Height; got >= baselineHeight {
		t.Fatalf("popup should reserve input rows, baseline %d got %d", baselineHeight, got)
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	if updated.suggestionState.Visible {
		t.Fatal("escape should close the popup")
	}
	if got := updated.uiService.Viewport().Height; got != baselineHeight {
		t.Fatalf("escape should restore viewport height, baseline %d got %d", baselineHeight, got)
	}
}

// TestSlashCompletionUnicodeLeadingWhitespaceIsEscape ★ （2026-09-20 批 C）：语义按 §0 裁决**反转**。
//
// 原断言：`\u00a0/per`（Unicode 前导空白）**仍**弹 slash 补全（= 旧的 `typeahead.go:398 TrimLeft 后判`）。
// 现语义：前导空白是**正式逃逸语义**（对齐 同类 CLI 的 `input_starts_with_space`），**三入口统一**
// ⇒ 补全侧不得再弹。<-- 原测试名 TestSlashCompletionMatchesUnicodeLeadingWhitespace 编码的是被废弃的旧语义。
//
// ★ paired 设计（防"把判据改坏成恒不弹"也算通过）：无前导空白必须**仍弹**（控制组）。
func TestSlashCompletionUnicodeLeadingWhitespaceIsEscape(t *testing.T) {
	// 控制组：无前导空白 ⇒ 必须弹。若这条也红，说明判据被打坏、而不是语义改变。
	ctrl := newTUIRegressionModel(t)
	ctrl.textarea.SetValue("/per")
	ctrl.updateTypeaheadSuggestions()
	if !ctrl.suggestionState.Visible {
		t.Fatal("/per 应弹命令补全（控制组）—— 此处不弹说明判据被打坏")
	}

	// 实验组 1：Unicode 前导空白 ⇒ 逃逸，不弹。
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("\u00a0/per")
	m.updateTypeaheadSuggestions()
	if m.suggestionState.Visible {
		t.Error("Unicode 前导空白应视为逃逸（普通文本）⇒ 不得弹命令补全")
	}

	// 实验组 2：ASCII 前导空格 ⇒ 同样逃逸（三入口统一）。
	m2 := newTUIRegressionModel(t)
	m2.textarea.SetValue(" /per")
	m2.updateTypeaheadSuggestions()
	if m2.suggestionState.Visible {
		t.Error("ASCII 前导空格应视为逃逸 ⇒ 不得弹命令补全")
	}
}

// TestSlashCompletionExcludesHiddenCommands v0.1 命令面收敛：隐藏命令不出现在 slash 弹窗，核心命令保留
func TestSlashCompletionExcludesHiddenCommands(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.textarea.SetValue("/")
	m.updateTypeaheadSuggestions()

	// ★ 2026-10-03：memory / fast 已取消隐藏 ⇒ 移出本表（弹窗现在**应当**提示它们）
	for _, name := range []string{"loop", "commit", "output-style", "privacy", "search", "stats", "summary"} {
		if findCompletion(m.suggestionState.Completions, name) != nil {
			t.Errorf("hidden command %q should not be suggested", name)
		}
	}
	for _, name := range []string{"model", "diff", "compact", "permissions", "usage", "context", "rewind", "undo"} {
		if findCompletion(m.suggestionState.Completions, name) == nil {
			t.Errorf("core command %q should still be suggested", name)
		}
	}
}

func findCompletion(completions []typeahead.Completion, name string) *typeahead.Completion {
	for index := range completions {
		if completions[index].DisplayText == name {
			return &completions[index]
		}
	}
	return nil
}

func completionDisplayNames(completions []typeahead.Completion) []string {
	names := make([]string, 0, len(completions))
	for _, completion := range completions {
		names = append(names, completion.DisplayText)
	}
	return names
}
