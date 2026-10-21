package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// newComposerSelectionModel 构造一个可交互的输入框测试模型：
// textarea 聚焦、宽度 60（wrap 宽度）、会话处于 Idle。
// 返回 *Model：newTUIRegressionModel 已 reassignServices，指针形态保证
// m.textarea 字段与 m.uiService.Textarea() 指向同一内存，避免服务别名分叉。
func newComposerSelectionModel(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.uiService.SetWidth(80)
	m.uiService.SetHeight(24)
	m.uiService.Textarea().SetWidth(60)
	m.uiService.Textarea().Focus()
	return m
}

func composerUpdateKey(t *testing.T, m *Model, msg tea.KeyMsg) *Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return modelFromTea(updated)
}

// modelFromTea 把 Update 返回的 tea.Model 转回 *Model 并 reassignServices，
// 保证后续 m.textarea 字段访问与 m.uiService.Textarea() 指向同一内存。
func modelFromTea(tm tea.Model) *Model {
	m := tm.(Model)
	m.reassignServices()
	return &m
}

// TestComposerShiftArrowsSelectText 验证 Shift+方向键从光标扩展选区并计算选区文本。
func TestComposerShiftArrowsSelectText(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if !m.composerSelection.active {
		t.Fatal("Shift+Left 应激活输入框选区")
	}
	if got, want := m.composerSelectionText(), "o"; got != want {
		t.Fatalf("选区文本 = %q, want %q", got, want)
	}

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got, want := m.composerSelectionText(), "lo"; got != want {
		t.Fatalf("再次 Shift+Left 后选区文本 = %q, want %q", got, want)
	}

	// 反向扩展：选区端点应正常归一化
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	if got, want := m.composerSelectionText(), "o"; got != want {
		t.Fatalf("Shift+Right 收缩后选区文本 = %q, want %q", got, want)
	}
}

// TestComposerShiftHomeSelectsLine 验证 Shift+Home 从行尾选到行首。
func TestComposerShiftHomeSelectsLine(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftHome})
	if got, want := m.composerSelectionText(), "hello world"; got != want {
		t.Fatalf("选区文本 = %q, want %q", got, want)
	}
	row, col := composerCursorPos(m.uiService.Textarea())
	if row != 0 || col != 0 {
		t.Fatalf("Shift+Home 后光标 = (%d,%d), want (0,0)", row, col)
	}
}

// TestComposerMultiLineSelectionText 验证跨逻辑行选区的文本拼装。
func TestComposerMultiLineSelectionText(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("abc\ndef")

	// 光标移到 (0,2)：home → up → left（普通移动键，不应产生选区）
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyHome})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.composerSelection.active {
		t.Fatal("普通移动键不应激活选区")
	}

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftDown})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftEnd})
	if got, want := m.composerSelectionText(), "c\ndef"; got != want {
		t.Fatalf("跨行选区文本 = %q, want %q", got, want)
	}
}

// TestComposerSelectionRenderHighlight 验证选区在输入框渲染中以高亮样式呈现，
// 且光标（caret）在选区端点以反显样式重绘（textarea 原生 caret 的 ANSI 被 Strip 后需补回）。
func TestComposerSelectionRenderHighlight(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})

	view := m.renderComposerDecorations(m.uiService.Textarea().View())
	if !strings.Contains(view, mouseSelectionStyle.Render("ld")) {
		t.Fatalf("渲染结果应包含高亮选区片段 %q，got %q", "ld", view)
	}
	if !strings.Contains(view, composerCaretStyle.Render("r")) {
		t.Fatalf("渲染结果应包含反显 caret %q，got %q", "r", view)
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "hello world") {
		t.Fatalf("渲染后纯文本内容被破坏，got %q", plain)
	}
}

// TestComposerSelectionRenderAcrossWrappedRows 验证软换行跨可视行的选区高亮。
func TestComposerSelectionRenderAcrossWrappedRows(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetWidth(10)
	m.textarea.SetValue("aaaa bbbb cccc")

	// 行尾 Shift+Home 全选：wrap 宽度 10 下渲染为两行 "aaaa bbbb " / "cccc "
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftHome})

	view := m.renderComposerDecorations(m.uiService.Textarea().View())
	// 第一可视行：caret 在 [0,1)，其后选区 [1,10) 即 "aaa bbbb "
	if !strings.Contains(view, mouseSelectionStyle.Render("aaa bbbb")) {
		t.Fatalf("第一可视行应包含高亮 %q，got %q", "aaa bbbb", view)
	}
	// 第二可视行：选区 "cccc"（不含 wrap 尾随空格）
	if !strings.Contains(view, mouseSelectionStyle.Render("cccc")) {
		t.Fatalf("第二可视行应包含高亮 %q，got %q", "cccc", view)
	}
}

// TestComposerBackspaceDeletesSelection 验证有选区时 Backspace 删除整个选区并保留光标位置正确。
func TestComposerBackspaceDeletesSelection(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyBackspace})

	if got, want := m.textarea.Value(), "hello wo"; got != want {
		t.Fatalf("删除选区后输入 = %q, want %q", got, want)
	}
	if m.composerSelection.active {
		t.Fatal("删除选区后应清除选区状态")
	}
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 8 {
		t.Fatalf("删除选区后光标 = (%d,%d), want (0,8)", row, col)
	}
}

// TestComposerBackspaceDeletesMultiLineSelection 验证跨行删除的拼接与光标回归。
func TestComposerBackspaceDeletesMultiLineSelection(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("abc\ndef\nghi")

	// 光标在 (2,3)（SetValue 后在末尾），Shift+Home 全选最后一行，再 Shift+Up 扩到 (1,0)
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftHome})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftUp})
	if got, want := m.composerSelectionText(), "def\nghi"; got != want {
		t.Fatalf("跨行选区文本 = %q, want %q", got, want)
	}
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyDelete})

	if got, want := m.textarea.Value(), "abc\n"; got != want {
		t.Fatalf("删除跨行选区后输入 = %q, want %q", got, want)
	}
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 1 || col != 0 {
		t.Fatalf("删除跨行选区后光标 = (%d,%d), want (1,0)", row, col)
	}
}

// TestComposerTypingReplacesSelection 验证输入字符整体替换选区。
func TestComposerTypingReplacesSelection(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})

	if got, want := m.textarea.Value(), "hello woX"; got != want {
		t.Fatalf("输入替换选区后输入 = %q, want %q", got, want)
	}
}

// TestComposerCtrlCAddsSelectionCopyInIdle 验证 idle 且有选区时 Ctrl+C 复制选区；
// 无选区时保持原行为（无副作用）。
func TestComposerCtrlCAddsSelectionCopyInIdle(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = modelFromTea(updated)
	if cmd == nil {
		t.Fatal("有选区时 Ctrl+C 应返回剪贴板复制命令")
	}
	msg := cmd()
	if _, ok := msg.(clipboardWriteResultMsg); !ok {
		t.Fatalf("复制命令应产生 clipboardWriteResultMsg，got %T", msg)
	}
	// 复制后选区保留（标准行为：复制不清除选区）
	if got, want := m.composerSelectionText(), "ld"; got != want {
		t.Fatalf("复制后选区文本 = %q, want %q", got, want)
	}

	// 无选区时 Ctrl+C 在 idle 下保持无副作用
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = modelFromTea(updated)
	if got, want := m.composerSelectionText(), "ld"; got != want {
		t.Fatalf("复制后选区文本 = %q, want %q", got, want)
	}
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.composerSelection.active {
		t.Fatal("Esc 应清除选区")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = modelFromTea(updated)
	if cmd != nil {
		t.Fatal("无选区时 idle 下 Ctrl+C 应保持无副作用（cmd=nil）")
	}
}

// TestComposerPlainMovementClearsSelection 验证普通移动键按终端惯例清除选区且不影响原光标行为。
func TestComposerPlainMovementClearsSelection(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})

	if m.composerSelection.active {
		t.Fatal("普通 Left 键应清除选区")
	}
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 9 {
		t.Fatalf("清除选区后光标 = (%d,%d), want (0,9)", row, col)
	}
}

// TestComposerSelectionDisabledWhenComposerNotFocused 验证 Send 按钮聚焦（Tab）时
// 选区操作不生效，避免污染隐藏的输入框。
func TestComposerSelectionDisabledWhenComposerNotFocused(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello")
	m.uiService.SetInputFocusedOnSend(true)

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if m.composerSelection.active {
		t.Fatal("输入框未聚焦时不应产生选区")
	}
}

// TestComposerWordSelectionWithAltShiftArrows 验证 Alt+Shift+左右按词扩展选区。
func TestComposerWordSelectionWithAltShiftArrows(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true})
	if got, want := m.composerSelectionText(), "world"; got != want {
		t.Fatalf("Alt+Shift+Left 词级选区 = %q, want %q", got, want)
	}
}

// TestComposerWrapMatchesTextareaRendering 验证复刻的 wrap 算法与 textarea 实际渲染一致
// （窗口匹配应成功，即 renderComposerDecorations 不因映射失败而跳过高亮）。
func TestComposerWrapMatchesTextareaRendering(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetWidth(10)
	m.textarea.SetValue("中文测试 abcdefghij 复刻一致")

	// 触发一次真实渲染，确认 textarea View 行可被复刻 wrap 匹配
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftHome})

	ta := m.uiService.Textarea()
	lines := composerValueLines(ta)
	visual := composerVisualRows(lines, ta.Width())
	stripped := strings.Split(ansi.Strip(ta.View()), "\n")

	_, col := composerCursorPos(ta)
	_ = col
	caretVisual := 0
	for i := 0; i < ta.Line(); i++ {
		caretVisual += len(composerWrap([]rune(lines[i]), ta.Width()))
	}
	caretVisual += ta.LineInfo().RowOffset

	if offset := composerMatchWindowOffset(stripped, visual, ta.Height(), caretVisual); offset != 0 {
		t.Fatalf("wrap 复刻应匹配 textarea 首行窗口（offset=0），got %d", offset)
	}
}
