package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newComposerMouseModel 构造输入框鼠标拖选测试模型：
// lastInputAreaTop=10、lastPreviewHeight=0 → textarea 文字首行 Y=11（上边框 1 行）；
// 文字区 X 偏移 = 左边框(1)+PaddingLeft(1)=2，即 xCell = X-2。
// 所有用例走 m.Update(tea.MouseMsg{...}) 生产入口（接线完整性），
// 不直接调用内部函数。
func newComposerMouseModel(t *testing.T) *Model {
	t.Helper()
	m := newComposerSelectionModel(t)
	m.textarea.SetHeight(3)
	m.lastInputAreaTop = 10
	m.lastPreviewHeight = 0
	return m
}

func composerUpdateMouse(t *testing.T, m *Model, msg tea.MouseMsg) *Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return modelFromTea(updated)
}

func composerMousePress(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func composerMouseMotion(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
}

func composerMouseRelease(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease}
}

// TestComposerMouseDragSelectsAcrossLines 验证输入框内按下→拖动→释放建立跨行选区，
// 释放后选区保留（编辑器惯例：松手不清除，可继续 Ctrl+C 复制 / Backspace 删除）。
func TestComposerMouseDragSelectsAcrossLines(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello\nworld")

	// press Y=11（文字首行）X=4 → col 2；motion Y=12（第二行）X=5 → col 3
	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(5, 12))
	m = composerUpdateMouse(t, m, composerMouseRelease(5, 12))

	if got, want := m.composerSelectionText(), "llo\nwor"; got != want {
		t.Fatalf("鼠标拖选选区文本 = %q, want %q", got, want)
	}
	if !m.composerSelection.active {
		t.Fatal("释放后选区应保留（active）")
	}
}

// TestComposerMouseClickPositionsCursorOnSecondLine 验证点击定位光标支持多行
// （修复原 setCursorByMouseX 只能定位第一行的限制）：点击第二行行末，光标落 (1,3)。
func TestComposerMouseClickPositionsCursorOnSecondLine(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("abc\ndef")

	m = composerUpdateMouse(t, m, composerMousePress(5, 12))
	m = composerUpdateMouse(t, m, composerMouseRelease(5, 12))

	if got := m.composerSelectionText(); got != "" {
		t.Fatalf("单击不应产生选区，got %q", got)
	}
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 1 || col != 3 {
		t.Fatalf("点击第二行行末后光标 = (%d,%d), want (1,3)", row, col)
	}
}

// TestComposerMouseClickCollapsesSelection 验证已有键盘选区时点击输入框其他位置，
// 选区清空且光标移动到点击位置（终端编辑器惯例）。
func TestComposerMouseClickCollapsesSelection(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello")

	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got, want := m.composerSelectionText(), "lo"; got != want {
		t.Fatalf("前置：键盘选区 = %q, want %q", got, want)
	}

	// 点击 (0,4)：X=6 → xCell=4 → col 4
	m = composerUpdateMouse(t, m, composerMousePress(6, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(6, 11))

	if m.composerSelection.active {
		t.Fatal("点击应清除既有选区")
	}
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 4 {
		t.Fatalf("点击后光标 = (%d,%d), want (0,4)", row, col)
	}
}

// TestComposerMouseDragCJKWideRunes 验证 CJK 宽字符的 cell→rune 列换算：
// 中文每字占 2 cell，拖选 "文测"（rune 区间 [1,3)）。
func TestComposerMouseDragCJKWideRunes(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("中文测试")

	m = composerUpdateMouse(t, m, composerMousePress(4, 11))  // xCell=2 → rune col 1
	m = composerUpdateMouse(t, m, composerMouseMotion(8, 11)) // xCell=6 → rune col 3
	m = composerUpdateMouse(t, m, composerMouseRelease(8, 11))

	if got, want := m.composerSelectionText(), "文测"; got != want {
		t.Fatalf("CJK 拖选选区 = %q, want %q", got, want)
	}
}

// TestComposerMouseDragBackspaceDeletes 验证拖选后 Backspace 删除整个选区（结果断言）。
func TestComposerMouseDragBackspaceDeletes(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello world")

	// press col 2 → motion col 8：选区 [2,8) = "llo wo"
	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(10, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(10, 11))
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyBackspace})

	if got, want := m.textarea.Value(), "herld"; got != want {
		t.Fatalf("拖选+Backspace 后输入 = %q, want %q", got, want)
	}
}

// TestComposerMouseDragHighlightRenders 验证拖选产生的选区经渲染层叠加高亮。
func TestComposerMouseDragHighlightRenders(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(10, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(10, 11))

	view := m.renderComposerDecorations(m.textarea.View())
	if !strings.Contains(view, mouseSelectionStyle.Render("llo wo")) {
		t.Fatalf("渲染结果应包含高亮选区片段 %q，got %q", "llo wo", view)
	}
}

// TestComposerMouseDragClampsBeyondLastRow 验证拖动超出 textarea 底部时选区
// 延伸到内容末尾（clamp 到最后一行行末）。
func TestComposerMouseDragClampsBeyondLastRow(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("abc\ndef")

	m = composerUpdateMouse(t, m, composerMousePress(2, 11))   // col 0
	m = composerUpdateMouse(t, m, composerMouseMotion(20, 14)) // Y 超出文字区（11+3-1=13）
	m = composerUpdateMouse(t, m, composerMouseRelease(20, 14))

	if got, want := m.composerSelectionText(), "abc\ndef"; got != want {
		t.Fatalf("超出底部拖选选区 = %q, want %q", got, want)
	}
}

// TestComposerMouseDragReverseToTopClampsToStart 验证从下往上拖出顶部时选区
// 收敛到内容开头 (0,0)。
func TestComposerMouseDragReverseToTopClampsToStart(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("abc\ndef")

	// press 第二行行末 (1,3)，向上拖出文字区顶部（Y=9 < textTop=11）
	m = composerUpdateMouse(t, m, composerMousePress(5, 12))
	m = composerUpdateMouse(t, m, composerMouseMotion(2, 9))
	m = composerUpdateMouse(t, m, composerMouseRelease(2, 9))

	if got, want := m.composerSelectionText(), "abc\ndef"; got != want {
		t.Fatalf("反向拖出顶部选区 = %q, want %q", got, want)
	}
}

// TestComposerMouseDragAcrossSoftWrappedRows 验证软换行场景：跨可视行拖选时
// 屏幕坐标正确映射回逻辑列（第二可视行内偏移 + 行起点）。
func TestComposerMouseDragAcrossSoftWrappedRows(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetWidth(10)
	m.textarea.SetValue("aaaa bbbb cccc")

	// wrap 宽度 10 → 可视行 "aaaa bbbb " / "cccc "
	m = composerUpdateMouse(t, m, composerMousePress(2, 11))  // xCell=0 → col 0
	m = composerUpdateMouse(t, m, composerMouseMotion(4, 12)) // 第二可视行 xCell=2 → 逻辑 col 10+2=12
	m = composerUpdateMouse(t, m, composerMouseRelease(4, 12))

	if got, want := m.composerSelectionText(), "aaaa bbbb cc"; got != want {
		t.Fatalf("软换行跨行拖选选区 = %q, want %q", got, want)
	}
}

// TestComposerMouseDragDisabledWhenSendFocused 验证焦点在 Send 按钮时鼠标交互
// 不启动拖选、不移动光标（保持 Tab 焦点态）。
func TestComposerMouseDragDisabledWhenSendFocused(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello")
	m.uiService.SetInputFocusedOnSend(true)

	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(8, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(8, 11))

	if m.composerSelection.active {
		t.Fatal("Send 按钮聚焦时不应产生输入框选区")
	}
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 5 {
		t.Fatalf("Send 聚焦时光标不应移动，got (%d,%d), want (0,5)", row, col)
	}
}

// TestComposerMouseDragVimNormalNotSelecting 验证 vim normal 模式下拖选不激活选区
// （与键盘选区守卫 composerSelectionKeyEnabled 保持一致）。
func TestComposerMouseDragVimNormalNotSelecting(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello world")
	m.uiService.SetVimMode(true)
	m.uiService.SetVimModeState(VimNormal)

	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(10, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(10, 11))

	if m.composerSelection.active {
		t.Fatal("vim normal 模式下拖选不应激活选区")
	}
}

// TestComposerMouseDragCtrlCCopies 验证拖选后 idle 状态 Ctrl+C 复制选区
// （复用键盘选区的复制路径，拖选与键盘选区共享同一状态机）。
func TestComposerMouseDragCtrlCCopies(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(10, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(10, 11))

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = modelFromTea(updated)
	if cmd == nil {
		t.Fatal("拖选后 Ctrl+C 应返回剪贴板复制命令")
	}
	msg := cmd()
	if _, ok := msg.(clipboardWriteResultMsg); !ok {
		t.Fatalf("复制命令应产生 clipboardWriteResultMsg，got %T", msg)
	}
}

// TestComposerMouseDragInterruptedByEsc 验证拖选进行中按 Esc 清除选区后，
// 后续 motion 不得以陈旧锚点重建选区（否则会选中用户从未选中的文本，
// Backspace 将删除错误内容——数据丢失风险）。
func TestComposerMouseDragInterruptedByEsc(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello world")

	// 按下 col 2 并拖到 col 8：选区 [2,8) = "llo wo"
	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(10, 11))
	if got, want := m.composerSelectionText(), "llo wo"; got != want {
		t.Fatalf("前置：拖选中选区 = %q, want %q", got, want)
	}

	// 不松开鼠标直接按 Esc：选区被清除，拖选状态必须一并终止
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.composerSelection.active {
		t.Fatal("Esc 应清除选区")
	}
	if m.composerMouseDrag.dragging {
		t.Fatal("Esc 清除选区后拖选状态必须一并终止（陈旧锚点重建风险）")
	}

	// 继续移动指针：不得重建任何选区
	m = composerUpdateMouse(t, m, composerMouseMotion(6, 11))
	if got := m.composerSelectionText(); got != "" {
		t.Fatalf("Esc 打断后 motion 不得重建选区，got %q", got)
	}

	// release 收尾：无选区残留
	m = composerUpdateMouse(t, m, composerMouseRelease(6, 11))
	if m.composerSelection.active || m.composerMouseDrag.dragging {
		t.Fatal("release 后不应有选区/拖选残留")
	}
}

// TestComposerMouseDragInterruptedByTyping 验证拖选中输入字符（删除选区替换路径）
// 同样终止拖选，后续 motion 不重建。
func TestComposerMouseDragInterruptedByTyping(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("hello world")

	m = composerUpdateMouse(t, m, composerMousePress(4, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(10, 11))

	// 输入字符：整体替换选区（"llo wo" → "X"）
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if got, want := m.textarea.Value(), "heXrld"; got != want {
		t.Fatalf("输入替换选区后输入 = %q, want %q", got, want)
	}
	if m.composerMouseDrag.dragging {
		t.Fatal("输入打断后拖选状态必须终止")
	}

	m = composerUpdateMouse(t, m, composerMouseMotion(8, 11))
	if m.composerSelection.active {
		t.Fatal("输入打断后 motion 不得重建选区")
	}
}

// TestComposerMouseDragInScrolledViewport 验证 textarea 内容超出高度窗口、
// 内部 viewport 已滚动（composerMatchWindowOffset > 0）时屏幕坐标仍映射到
// 正确的逻辑行。textarea 高 3、内容 4 行，SetValue 后光标在末行 → 窗口滚动
// 显示 line1..line3（offset=1）；点击首可见行应命中 line1 而非 line0。
func TestComposerMouseDragInScrolledViewport(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("aaa\nbbb\nccc\nddd")

	// bubbles v0.20.0 两个坑的组合：
	//  1. SetValue 不触发 viewport 滚动（repositionView 仅在 Update() 内调用）；
	//  2. repositionView 的 LineDown 依赖 viewport 已有内容（SetContent 由
	//     View() 填充），从未渲染过时 no-op。
	// 因此：先 View() 一次填充 viewport，再发末行 KeyDown（光标无变化）
	// 驱动 Update → repositionView → 窗口滚至 offset=1。
	_ = m.textarea.View()
	m = composerUpdateKey(t, m, tea.KeyMsg{Type: tea.KeyDown})

	// KeyDown 会触发输入区高度重算（3→5 行）并改写 lastInputAreaTop（10→8）。
	// 重新钉住布局常量，保证 y=11 仍指向文字首行（本用例的坐标契约）。
	m.lastInputAreaTop = 10
	m.lastPreviewHeight = 0

	// 确认窗口已滚动：首可见行应为 "bbb"（offset=1）
	viewFirst := strings.Split(m.textarea.View(), "\n")[0]
	if !strings.HasPrefix(strings.TrimRight(viewFirst, " "), "bbb") {
		t.Fatalf("前置：textarea 窗口应滚动到首行=bbb，got %q", viewFirst)
	}

	// press 首可见行 col 0 → 拖到 col 2：选区应为 "bb"（line1），而非 "aa"（line0）
	m = composerUpdateMouse(t, m, composerMousePress(2, 11))
	m = composerUpdateMouse(t, m, composerMouseMotion(4, 11))
	m = composerUpdateMouse(t, m, composerMouseRelease(4, 11))

	if got, want := m.composerSelectionText(), "bb"; got != want {
		t.Fatalf("滚动窗口内拖选选区 = %q, want %q（offset 映射错误）", got, want)
	}
}
