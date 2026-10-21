package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// （2026-10-19 用户直报， ⑨ 的缺失半边）：
//
// ⑨ 只把**删除**原子化了（Backspace/Delete/删词键一次删净整个 [Image #N]），
// **左右导航**仍是纯文本逐字符 —— 光标会落进占位符内部（"[Image #3|]"）。
// 后果：① 按左右键时看到「光标钻进占位符里面」而不是整体跳过；
// ② 光标在内部这种用户看不见的态下按退格，按「内部也整块删」的规则删掉的是
// 「光标视觉上不在旁边」的那张图（用户体感 = "我光标在图2和图3之间，删掉的是图3"）。
//
// 修法口径（与 ⑨ 的删除范围同源）：**原子单元 = 占位符本体 [Image #N] + 其后的
// 一个分隔空格**。Left/Right（含 Ctrl+B/F、Shift 扩选）落到单元上时整块跳过，
// 光标只落在单元边界（本体前 / 分隔空格后），永远不落在占位符内部。
// 对标（本地快照可核实）：同类 CLI 的占位符是 buffer 内的**元素**，
// 左右移动走 `next_atomic_boundary`/`prev_atomic_boundary`，与 `delete_backward` 同口径。
//
// 判据全部走生产入口 m.Update（R1b-2 指针复位同 w104Apply）。

// 主用例（左）：光标在 3 张图末尾，一次 ← 应跳到 [Image #3] 本体之前，
// 而不是钻进占位符内部（"[Image #3|]"）。
func TestW111_LeftJumpsOverPlaceholderAsWholeUnit(t *testing.T) {
	m := w104PasteImages(t, 3)
	// 贴 3 张后文本 = "[Image #1] [Image #2] [Image #3] "（33 rune），光标在末尾。

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft})

	_, col := composerCursorPos(m.uiService.Textarea())
	if col != 22 {
		t.Fatalf(" 一次 ← 应从 33 整块跳到 [Image #3] 前（col=22），actual col=%d", col)
	}
}

// 主用例（右）：光标在行首，两次 → 应依次落在 [Image #1] 单元之后（22? 否——
// 单元=本体+分隔空格，第一次 → 落 col=11，第二次 → 落 col=22）。
func TestW111_RightJumpsUnitByUnit(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("[Image #1] [Image #2] abc")
	m.textarea.SetCursor(0)

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	_, col := composerCursorPos(m.uiService.Textarea())
	if col != 11 {
		t.Fatalf(" 第一次 → 应落在 [Image #1]+空格 之后（col=11），actual col=%d", col)
	}

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	_, col = composerCursorPos(m.uiService.Textarea())
	if col != 22 {
		t.Fatalf(" 第二次 → 应落在 [Image #2]+空格 之后（col=22），actual col=%d", col)
	}
}

// 光标已在占位符内部（外部路径落入，如鼠标点击 / 修复前的按键）时，
// ← 应回到本体前、→ 应跳到单元后——不把内部位置当合法停留点。
func TestW111_NavFromInsidePlaceholderEscapesToBoundary(t *testing.T) {
	m := w104PasteImages(t, 1)
	m.textarea.SetValue("[Image #1] 看")
	m.textarea.SetCursor(4) // "[Ima|ge #1]"

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	_, col := composerCursorPos(m.uiService.Textarea())
	if col != 0 {
		t.Fatalf(" 内部 ← 应回到本体前（col=0），actual col=%d", col)
	}

	m.textarea.SetCursor(4)
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	_, col = composerCursorPos(m.uiService.Textarea())
	if col != 11 {
		t.Fatalf(" 内部 → 应跳到单元后（col=11），actual col=%d", col)
	}
}

// 反向半边（不得越界）：普通文本的左右移动仍是逐字符。
func TestW111_PlainTextNavigationUnchanged(t *testing.T) {
	m := newComposerSelectionModel(t)
	m.textarea.SetValue("abc")
	m.textarea.SetCursor(2)

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	_, col := composerCursorPos(m.uiService.Textarea())
	if col != 1 {
		t.Fatalf(" 不得改变普通文本逐字符导航，← 后 col=1，actual col=%d", col)
	}

	m.textarea.SetCursor(0)
	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	_, col = composerCursorPos(m.uiService.Textarea())
	if col != 1 {
		t.Fatalf(" 不得改变普通文本逐字符导航，→ 后 col=1，actual col=%d", col)
	}
}

// 反向半边（不得越界）：光标远离占位符时（普通文字段内），一次 ← 只走一个字符。
func TestW111_FarFromPlaceholderMovesOneChar(t *testing.T) {
	m := w104PasteImages(t, 2)
	m.textarea.SetValue("[Image #1] [Image #2] abc") // 光标在末尾 col=25

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyLeft})

	_, col := composerCursorPos(m.uiService.Textarea())
	if col != 24 {
		t.Fatalf(" 远离占位符时 ← 仍应逐字符（col=24），actual col=%d", col)
	}
}

// Ctrl+B / Ctrl+F 是本 composer 的同义移动键：同样按原子单元跳过。
func TestW111_EmacsMovementKeysJumpAtomically(t *testing.T) {
	m := w104PasteImages(t, 1) // "[Image #1] " 末尾 col=11

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	_, col := composerCursorPos(m.uiService.Textarea())
	if col != 0 {
		t.Fatalf(" Ctrl+B 应整块跳到本体前（col=0），actual col=%d", col)
	}

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	_, col = composerCursorPos(m.uiService.Textarea())
	if col != 11 {
		t.Fatalf(" Ctrl+F 应整块跳到单元后（col=11），actual col=%d", col)
	}
}

// Shift+← 扩选同样整块跨过单元：选区锚点在原光标处，新光标落在本体前。
func TestW111_ShiftLeftExtendsSelectionAtomically(t *testing.T) {
	m := w104PasteImages(t, 2) // "[Image #1] [Image #2] " 末尾 col=22

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})

	sRow, sCol, eRow, eCol, ok := m.composerSelectionRange()
	if !ok {
		t.Fatal(" Shift+← 应建立选区")
	}
	if sRow != 0 || sCol != 11 || eRow != 0 || eCol != 22 {
		t.Fatalf(" Shift+← 选区应整块覆盖 [Image #2]+空格（(0,11)-(0,22)），actual (%d,%d)-(%d,%d)",
			sRow, sCol, eRow, eCol)
	}
}

// 口径统一：原子单元 = 本体+分隔空格，故 Delete（向右删）在单元左端
// 也应连分隔空格一起删净（不留孤立空格）。此为对 ⑨ 期望的**有意翻转**，
// 理由：单元口径三处（导航/左删/右删）必须同源，否则 "[Image #1] 看" 右删后残留 " 看"。
func TestW111_ForwardDeleteSwallowsSeparatorSpace(t *testing.T) {
	m := w104PasteImages(t, 1)
	m.textarea.SetValue("[Image #1] 看")
	m.textarea.SetCursor(0)

	m = w111Apply(t, m, tea.KeyMsg{Type: tea.KeyDelete})

	if got, want := m.textarea.Value(), "看"; got != want {
		t.Fatalf(" 向右删应连分隔空格一起删净，actual %q want %q", got, want)
	}
	if n := len(m.uiService.PendingImages()); n != 0 {
		t.Fatalf(" 占位符删净 ⇒ 该图应出队，actual %d 张", n)
	}
}

// w111Apply 与 w104Apply 同法：走生产入口 Update + 指针复位。
func w111Apply(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	return w104Apply(t, m, msg)
}
