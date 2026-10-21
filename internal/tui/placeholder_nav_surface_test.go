package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// （2026-10-19 全按键面排查，用户拍板「现在就修」）： 只堵了
// Left/Right/Ctrl+B/F/Shift+←→，仍有 6 条路径能把光标送进 [Image #N] 内部
// 或直接把占位符改坏（词移动、上下跨行、鼠标点击、ctrl+t、词大小写键）。
//
// 修法口径（与 同源）：占位符单元 = 本体 + 尾随分隔空格 ——
// - 词移动（alt+←→ / alt+b / alt+f）：原生移动后按方向吸附出单元；
// - 上下跨行（↑↓ / ctrl+n/p）：原生移动后吸附到最近边界（平局取本体前）；
// - 鼠标点击/拖选：落点吸附到最近边界；
// - ctrl+t：交换窗 {col-1,col} 会碰坏本体 ⇒ 消费不动作（宁可不换位，不可拆图）；
// - 词大小写（alt+u/l/c）：光标在本体上 ⇒ 把单元当一个词整体跳过（不改文本）。
//
// 判据全部走生产入口 m.Update（同 w104Apply/w111Apply）。

// ---- 词级移动 ----

func TestW112_WordLeftSkipsPlaceholderUnit(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("[Image #1] [Image #2] abc")
	m.textarea.SetCursor(25) // 末尾

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 22 {
		t.Fatalf(" 词左移第 1 步应到 abc 前（col=22），actual %d", col)
	}
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 11 {
		t.Fatalf(" 词左移第 2 步应整块跨过 [Image #2]（col=11），actual %d（不得钻进占位符内部 18）", col)
	}
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 0 {
		t.Fatalf(" 词左移第 3 步应到行首（col=0），actual %d", col)
	}
}

func TestW112_WordRightSkipsPlaceholderUnit(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("[Image #1] [Image #2] abc")
	m.textarea.SetCursor(0)

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 11 {
		t.Fatalf(" 词右移第 1 步应整块跨过 [Image #1]（col=11），actual %d（不得钻进 6）", col)
	}
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 22 {
		t.Fatalf(" 词右移第 2 步应整块跨过 [Image #2]（col=22），actual %d", col)
	}

	// alt+f 与 alt+right 同义
	m.textarea.SetCursor(0)
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true})
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 11 {
		t.Fatalf(" alt+f 应与 alt+right 同口径（col=11），actual %d", col)
	}
}

// ---- 上下跨行（列保持） ----

func TestW112_VerticalMoveSnapsOutOfPlaceholder(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello\n[Image #1] world")
	// 注意：bubbles SetValue 后光标停在文本末尾（Reset+InsertString），
	// SetCursor 只作用于当前行 —— 跨行定位必须走生产函数。
	m.composerMoveCursorTo(0, 3) // hell|o

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	row, col := composerCursorPos(m.uiService.Textarea())
	if row != 1 || col != 0 {
		t.Fatalf(" ↓ 落进占位符内部 (1,3) 应回吸到最近边界 (1,0)，actual (%d,%d)", row, col)
	}

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 3 {
		t.Fatalf(" ↑ 回到原位 (0,3)，actual (%d,%d)", row, col)
	}
}

// 反向半边：普通文本的上下移动不受影响。
func TestW112_VerticalMovePlainTextUnchanged(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("abc\ndef")
	m.composerMoveCursorTo(0, 1) // a|bc

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 1 || col != 1 {
		t.Fatalf(" 不得改变普通文本跨行列保持，actual (%d,%d)", row, col)
	}
}

// ---- 鼠标点击 ----

// 点击落点吸附：点在占位符字样上（col 5，"Image" 中间）→ 最近边界平局取本体前（col 0）；
// 点在普通文字上 → 原样落列。
func TestW112_MouseClickSnapsToPlaceholderBoundary(t *testing.T) {
	m := newComposerMouseModel(t)
	m.textarea.SetValue("[Image #1] abc")

	m = composerUpdateMouse(t, m, composerMousePress(2+5, 11)) // col 5
	m = composerUpdateMouse(t, m, composerMouseRelease(2+5, 11))
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 0 {
		t.Fatalf(" 点击占位符内部应吸附到本体前 (0,0)，actual (%d,%d)", row, col)
	}

	m = composerUpdateMouse(t, m, composerMousePress(2+12, 11)) // col 12（"abc" 中间）
	m = composerUpdateMouse(t, m, composerMouseRelease(2+12, 11))
	if row, col := composerCursorPos(m.uiService.Textarea()); row != 0 || col != 12 {
		t.Fatalf(" 点击普通文字应原样落列 (0,12)，actual (%d,%d)", row, col)
	}
}

// ---- ctrl+t ----

func TestW112_CtrlTDoesNotBreakPlaceholder(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("[Image #1] abc")
	m.textarea.SetCursor(10) // 交换窗 {"]" ," "} ⇒ 会把占位符改坏

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})

	if got, want := m.textarea.Value(), "[Image #1] abc"; got != want {
		t.Fatalf(" ctrl+t 不得碰坏占位符（宁可不换位），actual %q want %q", got, want)
	}
	if n := len(m.uiService.PendingImages()); n != 0 && n != 1 {
		t.Fatalf(" 队列不得异常，actual %d", n)
	}
}

// 反向半边：远离占位符的 ctrl+t 仍是原生换位。
func TestW112_CtrlTFarFromPlaceholderStillTransposes(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("[Image #1] abc")
	m.textarea.SetCursor(13) // 交换窗 {"b","c"}

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})

	if got, want := m.textarea.Value(), "[Image #1] acb"; got != want {
		t.Fatalf(" 不得改变远离占位符的换位语义，actual %q want %q", got, want)
	}
}

// ---- 词大小写键 ----

func TestW112_CaseKeysSkipPlaceholderUnit(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("[Image #1] abc")
	m.textarea.SetCursor(0)

	// alt+l 会把 "[Image" 变 "[image" ⇒ 应整块跳过单元（不改文本，光标到单元尾）
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true})
	if got, want := m.textarea.Value(), "[Image #1] abc"; got != want {
		t.Fatalf(" alt+l 不得碰坏占位符本体，actual %q want %q", got, want)
	}
	if _, col := composerCursorPos(m.uiService.Textarea()); col != 11 {
		t.Fatalf(" alt+l 应把单元当一个词整体跳过（col=11），actual %d", col)
	}

	// 光标已在单元尾之后 ⇒ 原生语义生效（abc → ABC）
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}, Alt: true})
	if got, want := m.textarea.Value(), "[Image #1] ABC"; got != want {
		t.Fatalf(" 单元外的 alt+u 应保持原生大写语义，actual %q want %q", got, want)
	}
}

// ---- Shift+↓ 扩选同样吸附 ----

func TestW112_ShiftDownSnapsOutOfPlaceholder(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("hello\n[Image #1] world")
	m.composerMoveCursorTo(0, 3) // hell|o

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyShiftDown})

	sRow, sCol, eRow, eCol, ok := m.composerSelectionRange()
	if !ok {
		t.Fatal(" Shift+↓ 应建立选区")
	}
	// 锚点 (0,3)，光标吸附后 (1,0)
	if sRow != 0 || sCol != 3 || eRow != 1 || eCol != 0 {
		t.Fatalf(" Shift+↓ 选区应为 (0,3)-(1,0)（光标端吸附出占位符），actual (%d,%d)-(%d,%d)",
			sRow, sCol, eRow, eCol)
	}
}
