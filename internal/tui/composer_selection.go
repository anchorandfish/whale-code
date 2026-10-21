// composer_selection.go — 输入框（composer）文字选区。
//
// bubbles textarea v0.20.0 不支持文字选区：Shift+方向键作为无 Runes 的 KeyMsg
// 被 textarea 丢弃，Backspace/Delete 只删单字符，用户无法选中已输入的文字
// 进行复制/删除/替换。本文件在不改变 textarea 其他行为的前提下叠加选区层：
//
// - Shift+方向键/Home/End（含 Alt 词级组合）：从光标扩展选区；锚点固定在
// 选区起点，另一端始终是光标。光标移动转成 textarea 原生按键（Left/Home
// 等），复用其软换行/宽字符移动逻辑，避免二次实现产生行为分叉
// - Backspace/Delete：删除整个选区；输入字符/粘贴：整体替换选区
// - Ctrl+C（idle + 输入框聚焦）：复制选区（终端惯例：复制不清除选区）；
// 无选区时保持原"无副作用"行为
// - Esc：清除选区；普通 Left/Right：折叠到选区边缘（macOS/终端惯例）
// - 渲染：复刻 textarea wrap()（含软换行行尾追加空格的 quirk），把选区
// 映射到可视行叠加高亮；caret 落在选区端点时以反显重绘（textarea 原生
// caret 的 ANSI 被 Strip 后需补回）。映射失配时原样返回原视图，绝不
// 破坏既有渲染
package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	exportpkg "github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// composerSelectionState 输入框选区状态：anchor 是选区锚点（逻辑行号 +
// 行内 rune 列），另一端始终为 textarea 当前光标位置。值类型随 Model 复制。
type composerSelectionState struct {
	active    bool
	anchorRow int
	anchorCol int
}

// composerNavMemState 纵向移动的列记忆（，说明见 Model.composerNavMem）。
type composerNavMemState struct {
	active               bool
	cursorRow, cursorCol int
	desiredCol           int
}

// composerCaretStyle 选区渲染时 caret 的反显样式。textarea 原生 caret 的
// ANSI 在 ansi.Strip 后丢失，重建可视行时需补回，否则选区高亮会吞掉 caret。
var composerCaretStyle = lipgloss.NewStyle().Reverse(true)

// imageTokenStyle 是图片占位符 [Image #N] 在输入框内的成块样式（⑨）。
// 占位符在**删除语义**上已经是原子单位（见 deleteImagePlaceholderAtomically），
// 观感上也要是一个整体 —— 用户才看得出「这不是 10 个字符，是 1 张图」。
// 不用 Reverse：caret 已占用反显，两者会撞成同一副样子。
var imageTokenStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#3C3489", Dark: "#CECBF6"}).
	Background(lipgloss.AdaptiveColor{Light: "#EEEDFE", Dark: "#3C3489"})

// composerCursorPos 返回 textarea 光标的逻辑坐标（行号 + 行内 rune 列）。
// textarea 未导出 col，用 LineInfo 还原：StartColumn + ColumnOffset 之和
// 恰为内部 m.col（wrap 边界分支 StartColumn=m.col、ColumnOffset=0 亦成立）。
func composerCursorPos(ta *textarea.Model) (row, col int) {
	row = ta.Line()
	li := ta.LineInfo()
	col = li.StartColumn + li.ColumnOffset
	return
}

// composerValueLines 返回 textarea 的逻辑行列表（与内部 value 逐行一致）。
func composerValueLines(ta *textarea.Model) []string {
	value := ta.Value()
	if value == "" {
		return []string{""}
	}
	return strings.Split(value, "\n")
}

// composerWrap 复刻 bubbles textarea 的 wrap()（textarea.go:1394），含软换行
// 行尾追加空格的 quirk。任何偏差都会导致复刻可视行与 View() 实际渲染错位、
// renderComposerDecorations 窗口匹配失败而静默跳过高亮，因此必须逐字符对齐：
// 行/词宽度用 uniseg.StringWidth、末字符宽度用 runewidth.RuneWidth，
// 与 bubbles 内部完全一致。
func composerWrap(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], composerRepeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], composerRepeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else {
			// 末字符可能是双宽字符，加上它会超宽时提前折行。
			lastCharLen := runewidth.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], composerRepeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], composerRepeatSpaces(spaces)...)
	}

	return lines
}

func composerRepeatSpaces(n int) []rune {
	return []rune(strings.Repeat(" ", n))
}

// composerVisualRows 返回全部逻辑行经 wrap 后的可视行文本。
func composerVisualRows(lines []string, width int) []string {
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, r := range composerWrap([]rune(line), width) {
			rows = append(rows, string(r))
		}
	}
	return rows
}

// composerSelectionRange 归一化选区为 [startRow,startCol,endRow,endCol)。
// anchor 越界（textarea 内容被历史导航等外部替换）时返回 ok=false，
// 选区视为失效而非静默作用到错误文本上。
func (m Model) composerSelectionRange() (startRow, startCol, endRow, endCol int, ok bool) {
	if !m.composerSelection.active {
		return 0, 0, 0, 0, false
	}
	ta := m.uiService.Textarea()
	lines := composerValueLines(ta)
	aRow, aCol := m.composerSelection.anchorRow, m.composerSelection.anchorCol
	if aRow < 0 || aRow >= len(lines) || aCol < 0 || aCol > len([]rune(lines[aRow])) {
		return 0, 0, 0, 0, false
	}
	cRow, cCol := composerCursorPos(ta)
	if aRow < cRow || (aRow == cRow && aCol <= cCol) {
		return aRow, aCol, cRow, cCol, true
	}
	return cRow, cCol, aRow, aCol, true
}

// composerSelectionText 返回选区文本（空选区/失效选区返回空串）。
func (m Model) composerSelectionText() string {
	sRow, sCol, eRow, eCol, ok := m.composerSelectionRange()
	if !ok || (sRow == eRow && sCol == eCol) {
		return ""
	}
	lines := composerValueLines(m.uiService.Textarea())
	parts := make([]string, 0, eRow-sRow+1)
	for y := sRow; y <= eRow; y++ {
		r := []rune(lines[y])
		switch {
		case sRow == eRow:
			parts = append(parts, string(r[sCol:eCol]))
		case y == sRow:
			parts = append(parts, string(r[sCol:]))
		case y == eRow:
			parts = append(parts, string(r[:eCol]))
		default:
			parts = append(parts, lines[y])
		}
	}
	return strings.Join(parts, "\n")
}

func (m *Model) clearComposerSelection() {
	m.composerSelection = composerSelectionState{}
	// 拖选状态一并终止：选区被任何路径清除（Esc/输入替换/外部替换）后，
	// 残留 dragging 会让后续 motion 用陈旧锚点重建选区（composer_mouse_test
	// TestComposerMouseDragInterruptedBy* 锁定的行为）。
	m.composerMouseDrag = composerMouseDragState{}
}

// composerSelectionKeyEnabled 判断当前上下文是否允许输入框选区交互：
// textarea 聚焦、焦点不在 Send/Stop 按钮、非 vim normal、无 typeahead 弹窗、
// 会话处于可编辑输入的状态（状态集与 canAcceptComposerText 一致）。
func (m *Model) composerSelectionKeyEnabled() bool {
	ta := m.uiService.Textarea()
	if !ta.Focused() || m.uiService.InputFocusedOnSend() || m.uiService.InputFocusedOnStop() {
		return false
	}
	if m.uiService.VimMode() && m.uiService.VimModeState() == VimNormal {
		return false
	}
	if m.suggestionState != nil && m.suggestionState.Visible {
		return false
	}
	switch m.sessionService.State() {
	case StateIdle, StateStreaming, StateToolExec:
		return true
	default:
		return false
	}
}

// handleComposerSelectionKey 处理与选区相关的按键。
// 返回 true 表示已消费（不再转发 textarea）；false 表示透传到原有路径
// （可能已先删除选区，如输入替换）。光标移动一律转成 textarea 原生按键。
func (m *Model) handleComposerSelectionKey(msg tea.KeyMsg) bool {
	if !m.composerSelectionKeyEnabled() {
		return false
	}
	ta := m.uiService.Textarea()

	// Shift+方向键/Home/End（Alt 组合按词移动）：扩展选区。
	var moveKey tea.KeyType
	switch msg.Type {
	case tea.KeyShiftLeft:
		moveKey = tea.KeyLeft
	case tea.KeyShiftRight:
		moveKey = tea.KeyRight
	case tea.KeyShiftUp:
		moveKey = tea.KeyUp
	case tea.KeyShiftDown:
		moveKey = tea.KeyDown
	case tea.KeyShiftHome:
		moveKey = tea.KeyHome
	case tea.KeyShiftEnd:
		moveKey = tea.KeyEnd
	}
	if moveKey != 0 {
		if !m.composerSelection.active {
			row, col := composerCursorPos(ta)
			m.composerSelection.anchorRow, m.composerSelection.anchorCol = row, col
		}
		// 扩选移动落在占位符单元上时同样整块跨过（与普通导航同口径），
		// 否则选区起点/终点会落在占位符内部，与视觉成块渲染对不上。
		if (moveKey == tea.KeyLeft || moveKey == tea.KeyRight) && !msg.Alt {
			if m.navImagePlaceholderAtomically(moveKey == tea.KeyLeft) {
				m.composerSelection.active = true
				return true
			}
		}
		*ta, _ = ta.Update(tea.KeyMsg{Type: moveKey, Alt: msg.Alt})
		// 扩选的上下跨行落进占位符内部时，光标端吸附到最近边界
		//（锚点不动，选区起点/终点不再骑在占位符上）。
		if moveKey == tea.KeyUp || moveKey == tea.KeyDown {
			m.snapComposerCursorOutOfPlaceholder()
		}
		m.composerSelection.active = true
		return true
	}

	if !m.composerSelection.active {
		// 图片占位符 [Image #N]（连同尾随分隔空格）在**导航**上也是原子
		// 单元 —— Left/Right/Ctrl+B/Ctrl+F 落在它上面时整块跳过，光标永远不落进
		// 占位符内部（落进去后按退格会整块删掉用户视觉上「不在旁边」的图）。
		switch msg.Type {
		case tea.KeyLeft, tea.KeyRight, tea.KeyCtrlB, tea.KeyCtrlF:
			if !msg.Alt && m.navImagePlaceholderAtomically(msg.Type == tea.KeyLeft || msg.Type == tea.KeyCtrlB) {
				return true
			}
		}
		// 按词移动（alt+←→ / alt+b/f）——词边界不认识占位符，会停在
		// "Image|#" 这类内部边界上；原生移动后按方向吸附出单元。
		switch {
		case msg.Alt && msg.Type == tea.KeyLeft, msg.String() == "alt+b":
			if m.wordMovePlaceholderAware(true) {
				return true
			}
		case msg.Alt && msg.Type == tea.KeyRight, msg.String() == "alt+f":
			if m.wordMovePlaceholderAware(false) {
				return true
			}
		}
		// 上下跨行 —— 光标列保持会骑到占位符头上；原生移动后吸附到
		// 最近边界（视觉边界让路给历史召回，见 verticalMovePlaceholderAware）。
		switch msg.Type {
		case tea.KeyUp, tea.KeyDown, tea.KeyCtrlP, tea.KeyCtrlN:
			if m.verticalMovePlaceholderAware(msg) {
				return true
			}
		}
		// ctrl+t 交换窗碰到占位符本体 ⇒ 消费不动作（宁可不换位，不可拆图）。
		if msg.String() == "ctrl+t" && m.composerTransposeWouldBreakPlaceholder() {
			return true
		}
		// alt+u/l/c 词大小写键骑在占位符上 ⇒ 把单元当一个词整体跳过
		//（不改文本 —— 改了大小写占位符就识别不了，图会静默从队列消失）。
		switch msg.String() {
		case "alt+u", "alt+l", "alt+c":
			if m.caseKeySkipPlaceholderUnit() {
				return true
			}
		}
		// ⑨：空选区下，图片占位符 [Image #N] 是一个**原子单位** ——
		// Backspace/Delete/删词键落在它上面时一次删净，而不是逐字符啃成 "[Image #"。
		return m.deleteImagePlaceholderAtomically(msg)
	}

	sRow, sCol, eRow, eCol, ok := m.composerSelectionRange()
	empty := !ok || (sRow == eRow && sCol == eCol)
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyDelete:
		// 空选区退回普通单字符删除行为。
		if empty {
			m.clearComposerSelection()
			return false
		}
		m.deleteComposerSelection()
		return true
	case tea.KeyEsc:
		m.clearComposerSelection()
		return true
	case tea.KeyRunes:
		if msg.Paste || empty {
			m.clearComposerSelection()
			return false
		}
		// 先删选区再透传：输入字符落在原选区起点 → 整体替换选区。
		m.deleteComposerSelection()
		return false
	case tea.KeyLeft:
		// macOS/终端惯例：有选区时 Left 折叠到选区起点（本次不额外移动）。
		if empty {
			m.clearComposerSelection()
			return false
		}
		m.collapseComposerSelection(sRow, sCol)
		return true
	case tea.KeyRight:
		if empty {
			m.clearComposerSelection()
			return false
		}
		m.collapseComposerSelection(eRow, eCol)
		return true
	case tea.KeyUp, tea.KeyDown, tea.KeyHome, tea.KeyEnd, tea.KeyEnter,
		tea.KeyCtrlA, tea.KeyCtrlB, tea.KeyCtrlE, tea.KeyCtrlF, tea.KeyCtrlN, tea.KeyCtrlP:
		// 普通移动键：清除选区后透传，光标移动交给 textarea 原有逻辑。
		m.clearComposerSelection()
		return false
	}
	return false
}

// collapseComposerSelection 清除选区并把光标折叠到指定边缘坐标。
func (m *Model) collapseComposerSelection(row, col int) {
	m.clearComposerSelection()
	m.composerMoveCursorTo(row, col)
}

// composerMoveCursorTo 把 textarea 光标移动到逻辑坐标 (row, col)。
// textarea 未导出行号 setter；逐次 KeyUp/KeyDown 以 ta.Line() 为准逼近
// 目标行（软换行行内只移动可视行、不会跳过逻辑行，循环必然收敛），
// 最后 SetCursor 精确落列（SetCursor 在当前行内按 rune 索引定位）。
func (m *Model) composerMoveCursorTo(row, col int) {
	ta := m.uiService.Textarea()
	for ta.Line() > row {
		*ta, _ = ta.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	for ta.Line() < row {
		*ta, _ = ta.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	ta.SetCursor(col)
}

// deleteComposerRange 删除输入框内 [startRow,startCol)..(endRow,endCol) 区间的文本，
// 并把光标落到区间起点。textarea 未导出删除原语，这里按行重建 value 后 SetValue；
// SetValue 会清空 wrap memo 缓存，但不是逐键热路径，代价可接受。
func (m *Model) deleteComposerRange(startRow, startCol, endRow, endCol int) {
	ta := m.uiService.Textarea()
	lines := composerValueLines(ta)
	if startRow >= len(lines) || endRow >= len(lines) {
		return
	}
	if startRow == endRow {
		r := []rune(lines[startRow])
		if startCol <= len(r) && endCol <= len(r) {
			lines[startRow] = string(append(r[:startCol:startCol], r[endCol:]...))
		}
	} else {
		first := []rune(lines[startRow])
		last := []rune(lines[endRow])
		if startCol <= len(first) && endCol <= len(last) {
			lines[startRow] = string(append(first[:startCol:startCol], last[endCol:]...))
			lines = append(lines[:startRow+1:startRow+1], lines[endRow+1:]...)
		}
	}
	ta.SetValue(strings.Join(lines, "\n"))
	m.composerMoveCursorTo(startRow, startCol)
}

// deleteComposerSelection 删除选区文本并把光标落到选区起点。
func (m *Model) deleteComposerSelection() {
	sRow, sCol, eRow, eCol, ok := m.composerSelectionRange()
	m.clearComposerSelection()
	if !ok || (sRow == eRow && sCol == eCol) {
		return
	}
	m.deleteComposerRange(sRow, sCol, eRow, eCol)
}

// deleteImagePlaceholderAtomically 处理「图片占位符的原子删除」（⑨）。
//
// 背景：占位符 [Image #N] 在输入框里是**纯文本**，textarea 只会逐字符/逐词删 ——
// 删一张图要按 10 次退格，而且删剩 "[Image #" 就已经被判为「放弃这张图」，
// 残渣还留在框里。对标 同类 CLI（`bottom_pane/textarea.rs`：占位符是 buffer 内的元素，
// `delete_backward` 走 `prev_atomic_boundary` ⇒ 一次退格删整个元素）与 同类 CLI
// （`PromptInput.tsx` 的原子 chip），whale 不重写 composer，而是在既有的
// Backspace/Delete 拦截点（本文件）上加同款「原子边界」判定。
//
// 返回 true 表示按键已被消费（不再透传给 textarea）。
func (m *Model) deleteImagePlaceholderAtomically(msg tea.KeyMsg) bool {
	var backspace bool
	switch {
	case msg.Type == tea.KeyBackspace, msg.Type == tea.KeyCtrlH:
		backspace = true
	case msg.Type == tea.KeyDelete:
		backspace = false
	case msg.String() == "ctrl+w", msg.String() == "alt+backspace":
		// 删词键：若按词边界删，光标贴着占位符右端时只会啃掉编号（剩 "[Image #]"）。
		backspace = true
	case msg.String() == "alt+d", msg.String() == "alt+delete":
		backspace = false
	default:
		return false
	}

	ta := m.uiService.Textarea()
	row, col := composerCursorPos(ta)
	start, end, ok := imagePlaceholderDeleteRange(composerValueLines(ta), row, col, backspace)
	if !ok {
		return false // 没贴着占位符 ⇒ 原样放行，普通文本的删除语义不变。
	}
	m.deleteComposerRange(row, start, row, end)
	// 占位符没了 ⇒ 对应图片出队（⑥ 的队列同步）并重编号（⑦）。
	// 本按键被消费后不会再走 routeKeyToComposerTextarea，必须在这里显式同步一次。
	m.reconcilePendingImages()
	return true
}

// navImagePlaceholderAtomically 处理「图片占位符的原子导航」。
//
// Left/Right（含同义的 Ctrl+B/Ctrl+F、Shift 扩选）落在占位符单元上时整块跳过：
// 向左跳到本体前、向右跳到单元尾（含分隔空格）。核心判定在纯函数
// imagePlaceholderNavCol（clipboard.go，与 ⑨ 的 imagePlaceholderDeleteRange
// 同一单元口径）。返回 true 表示按键已被消费（不再透传给 textarea）。
func (m *Model) navImagePlaceholderAtomically(left bool) bool {
	ta := m.uiService.Textarea()
	row, col := composerCursorPos(ta)
	lines := composerValueLines(ta)
	if row < 0 || row >= len(lines) {
		return false
	}
	target, ok := imagePlaceholderNavCol(lines[row], col, left)
	if !ok {
		return false
	}
	m.composerMoveCursorTo(row, target)
	return true
}

// snapComposerCursorOutOfPlaceholder 把当前光标从占位符单元内部吸附到最近边界
// （：上下跨行 / 扩选跨行的落点吸附；平局取本体前）。不在单元上时不动。
func (m *Model) snapComposerCursorOutOfPlaceholder() {
	ta := m.uiService.Textarea()
	row, col := composerCursorPos(ta)
	lines := composerValueLines(ta)
	if row < 0 || row >= len(lines) {
		return
	}
	if target, ok := imagePlaceholderSnapCol(lines[row], col); ok {
		m.composerMoveCursorTo(row, target)
	}
}

// wordMovePlaceholderAware 处理「按词移动的占位符吸附」。
// 先让 textarea 原生按词移动（完整保留其 uniseg 词边界语义），落点骑在
// 占位符单元内部时按方向吸附出单元。输入框没有任何占位符时返回 false
// （不拦截，键走原生路径，行为与从前逐字节一致）。
func (m *Model) wordMovePlaceholderAware(left bool) bool {
	ta := m.uiService.Textarea()
	if !strings.Contains(ta.Value(), "[Image #") {
		return false
	}
	key := tea.KeyRight
	if left {
		key = tea.KeyLeft
	}
	*ta, _ = ta.Update(tea.KeyMsg{Type: key, Alt: true})
	row, col := composerCursorPos(ta)
	lines := composerValueLines(ta)
	if row < 0 || row >= len(lines) {
		return true
	}
	if target, ok := imagePlaceholderWordMoveCol(lines[row], col, left); ok {
		m.composerMoveCursorTo(row, target)
	}
	return true
}

// verticalMovePlaceholderAware 处理「上下跨行的占位符吸附」。
//
// 原生 ↑/↓ 保持光标列（bubbles 用 lastCharOffset 记忆），落在占位符内部时吸附
// 到最近边界；吸附用的 SetCursor 会清掉 bubbles 的列记忆 ⇒ 自记一份
// （composerNavMem），连续纵向移动期间恢复「用户真正想要的列」。
//
// 让路纪律：光标在**视觉边界**（首可视行按 ↑ / 末可视行按 ↓）时返回 false ——
// app.go 的 navigateInputHistory（输入历史召回）只在这两个位置接手，拦截会
// 坏掉历史导航。输入框没有占位符时同样返回 false（键走原生路径）。
func (m *Model) verticalMovePlaceholderAware(msg tea.KeyMsg) bool {
	ta := m.uiService.Textarea()
	if !strings.Contains(ta.Value(), "[Image #") {
		return false
	}
	up := msg.Type == tea.KeyUp || msg.Type == tea.KeyCtrlP
	row, col := composerCursorPos(ta)
	li := ta.LineInfo()
	if (up && row == 0 && li.RowOffset == 0) ||
		(!up && row == ta.LineCount()-1 && li.RowOffset >= li.Height-1) {
		return false // 视觉边界：让路给输入历史召回
	}

	memActive := m.composerNavMem.active &&
		m.composerNavMem.cursorRow == row && m.composerNavMem.cursorCol == col
	if !memActive {
		m.composerNavMem = composerNavMemState{}
	}

	*ta, _ = ta.Update(msg) // 原生移动（软换行可视行口径完整保留）
	newRow, newCol := composerCursorPos(ta)
	if newRow == row {
		// 软换行行内可视行移动：占位符吸附只发生在跨逻辑行时，原样放行。
		m.composerNavMem = composerNavMemState{}
		return true
	}

	lines := composerValueLines(ta)
	runes := []rune(lines[newRow])
	target := newCol
	desired := newCol
	if memActive {
		desired = min(m.composerNavMem.desiredCol, len(runes))
		target = desired
	}
	if snapped, ok := imagePlaceholderSnapCol(lines[newRow], target); ok {
		target = snapped
	}
	m.composerNavMem = composerNavMemState{
		active:     true,
		cursorRow:  newRow,
		cursorCol:  target,
		desiredCol: desired,
	}
	if target != newCol {
		m.composerMoveCursorTo(newRow, target)
	}
	return true
}

// composerTransposeWouldBreakPlaceholder 判定 ctrl+t 的交换窗是否碰到占位符
// 本体（："] " 被换成 " ]" 后占位符识别不了 ⇒ 静默丢图）。
func (m *Model) composerTransposeWouldBreakPlaceholder() bool {
	ta := m.uiService.Textarea()
	row, col := composerCursorPos(ta)
	lines := composerValueLines(ta)
	if row < 0 || row >= len(lines) {
		return false
	}
	return imagePlaceholderTransposeHits(lines[row], col)
}

// caseKeySkipPlaceholderUnit 处理 alt+u/l/c 骑在占位符上的情形：
// 把整个单元当一个词跳过 —— 不改文本（改了就识别不了），光标推到单元尾。
// 光标不在单元上时返回 false，原生词变换照常。
func (m *Model) caseKeySkipPlaceholderUnit() bool {
	ta := m.uiService.Textarea()
	row, col := composerCursorPos(ta)
	lines := composerValueLines(ta)
	if row < 0 || row >= len(lines) {
		return false
	}
	if unitEnd, ok := imagePlaceholderUnitAtCol(lines[row], col); ok {
		m.composerMoveCursorTo(row, unitEnd)
		return true
	}
	return false
}

// copyComposerSelection 复制输入框选区文本到系统剪贴板。
// 复制不清除选区（终端惯例）；写剪贴板结果经 clipboardWriteResultMsg 回流，
// 失败时由既有处理分支展示错误信息。
func (m *Model) copyComposerSelection(text string) tea.Cmd {
	clipboard := exportpkg.NewClipboard()
	if m.notifier != nil {
		notification := notify.NewNotification(
			"composer-selection-copy",
			"Clipboard",
			fmt.Sprintf("Copied selection / 已复制选中内容 (%d chars)", len([]rune(text))),
		)
		notification.Priority = notify.PriorityLow
		notification.TimeoutMs = 2500
		m.notifier.Push(notification)
	}
	return func() tea.Msg {
		return clipboardWriteResultMsg{err: clipboard.Copy(text)}
	}
}

// composerMatchWindowOffset 定位 textarea.View() 首行对应全部可视行的偏移。
// textarea 内部 viewport 会随光标滚动，View() 只显示 height 行的窗口；
// 通过逐行内容匹配（忽略行尾 padding 空格）+ caret 约束（repositionView
// 保证 caret 必在窗口内）确定偏移。匹配失败返回 -1。
func composerMatchWindowOffset(viewLines, visualRows []string, height, caretVisual int) int {
	if len(viewLines) == 0 {
		return 0
	}
	matchAt := func(k int) bool {
		if k < 0 || k > len(visualRows) {
			return false
		}
		for i := 0; i < len(viewLines); i++ {
			want := ""
			if k+i < len(visualRows) {
				want = visualRows[k+i]
			}
			if strings.TrimRight(viewLines[i], " ") != strings.TrimRight(want, " ") {
				return false
			}
		}
		return true
	}
	// caret 必在窗口内，优先扫描 caret 约束区间（O(height) 而非 O(总行数)）。
	lo := max(0, caretVisual-height+1)
	hi := min(caretVisual, len(visualRows))
	for k := lo; k <= hi; k++ {
		if matchAt(k) {
			return k
		}
	}
	// 兜底：caret 约束失败（异常状态）时退化为全量内容匹配的最小偏移。
	for k := 0; k <= len(visualRows); k++ {
		if matchAt(k) {
			return k
		}
	}
	return -1
}

// composerDecoratedView 是输入框内容的**唯一**渲染形态：
// textarea 视图 + 输入框装饰（选区高亮 / 占位符成块 + 超链接）。
//
// 所有渲染输入框的出口都必须走这里（当前两处：正常运行态与选择面板的补充输入子态）。
// 任一出口绕开它，占位符就会在该出口退化成纯文本（⑧ 链接与 ⑨ 成块各半修）——
// 与"CLI 子命令双实现"同族的同源点分裂。护栏见
// TestW104_AllComposerOutletsUseSingleDecoratedForm。
func (m Model) composerDecoratedView() string {
	return m.renderComposerDecorations(m.uiService.Textarea().View())
}

// renderComposerDecorations 在 textarea 视图上叠加输入框内的两类装饰（⑨）：
//
//	① 文字选区高亮（本文件上半部分的选区层）；
//	② 图片占位符 [Image #N] 成块 —— 与它的原子删除语义一致（"一眼看出是一个整体"）。
//
// 装饰失效或可视行映射失配时原样返回原视图（显示降级，非故障），不破坏既有渲染。
func (m Model) renderComposerDecorations(view string) string {
	sRow, sCol, eRow, eCol, ok := m.composerSelectionRange()
	selEmpty := !ok || (sRow == eRow && sCol == eCol)

	ta := m.uiService.Textarea()
	width := ta.Width()
	lines := composerValueLines(ta)
	// 待发图片队列：占位符编号 → 落盘文件（⑧ 的超链接目标）。
	images := m.uiService.PendingImages()
	tokensByLine := make([][]imagePlaceholderSpan, len(lines))
	hasToken := false
	for i, line := range lines {
		if spans := imagePlaceholderSpans(line); len(spans) > 0 {
			tokensByLine[i] = spans
			hasToken = true
		}
	}
	if selEmpty && !hasToken {
		return view
	}

	// 全部可视行 + 每行在逻辑行内的 rune 起点（grid 坐标，含 wrap 尾随空格）。
	type visualRow struct {
		line  int
		start int
		runes []rune
	}
	var vrows []visualRow
	for i, line := range lines {
		start := 0
		for _, r := range composerWrap([]rune(line), width) {
			vrows = append(vrows, visualRow{line: i, start: start, runes: r})
			start += len(r)
		}
	}

	// caret 的可视行号与行内 rune 偏移。
	li := ta.LineInfo()
	caretVisual := 0
	for i := 0; i < ta.Line() && i < len(lines); i++ {
		caretVisual += len(composerWrap([]rune(lines[i]), width))
	}
	caretVisual += li.RowOffset

	viewLines := strings.Split(ansi.Strip(view), "\n")
	offset := composerMatchWindowOffset(viewLines, composerVisualRows(lines, width), ta.Height(), caretVisual)
	if offset < 0 {
		return view
	}

	for i := 0; i < len(viewLines); i++ {
		vi := offset + i
		if vi >= len(vrows) {
			break // 其余为 EndOfBuffer padding 行，保持原样
		}
		v := vrows[vi]
		selFrom, selTo := 0, 0
		if v.line >= sRow && v.line <= eRow {
			from := 0
			if v.line == sRow {
				from = sCol
			}
			to := len([]rune(lines[v.line]))
			if v.line == eRow {
				to = eCol
			}
			selFrom = max(from, v.start) - v.start
			selTo = min(to, v.start+len(v.runes)) - v.start
		}
		hasCaret := caretVisual == vi
		caretAt := -1
		if hasCaret {
			caretAt = li.ColumnOffset
		}
		rendered := composerRenderRow(v.runes, selFrom, selTo, localTokenSpans(tokensByLine[v.line], v.start, len(v.runes), images), caretAt, hasCaret)
		// 补齐原行宽（保留 textarea 行尾 padding），避免高亮行短一截
		// 导致输入框边框在选区期间抖动。
		if pad := displayWidth(viewLines[i]) - ansi.StringWidth(rendered); pad > 0 {
			rendered += strings.Repeat(" ", pad)
		}
		viewLines[i] = rendered
	}
	return strings.Join(viewLines, "\n")
}

// composerSpan 是可视行内的一个 rune 区间 [from,to)。
// uri 非空时该区间渲染为 OSC 8 超链接（⑧：点 [Image #N] 打开落盘的原图）。
type composerSpan struct {
	from, to int
	uri      string
}

// localTokenSpans 把某逻辑行上的占位符区间换算成某个可视行内的局部区间。
// 占位符被软换行拆开时两侧可视行各得一段 —— 渲染上仍连续成块（各自带同一 uri）。
func localTokenSpans(spans []imagePlaceholderSpan, lineStart, rowLen int, images []PendingImage) []composerSpan {
	if len(spans) == 0 {
		return nil
	}
	var out []composerSpan
	for _, s := range spans {
		if s.start >= lineStart+rowLen || s.end <= lineStart {
			continue // 与该可视行无交集
		}
		from := max(s.start, lineStart) - lineStart
		to := min(s.end, lineStart+rowLen) - lineStart
		if to > from {
			out = append(out, composerSpan{from: from, to: to, uri: imagePlaceholderURI(s.num, images)})
		}
	}
	return out
}

// composerSpanAt 返回完整覆盖 [from,to) 的占位符区间；无则 ok=false。
func composerSpanAt(tokens []composerSpan, from, to int) (composerSpan, bool) {
	for _, tk := range tokens {
		if from >= tk.from && to <= tk.to {
			return tk, true
		}
	}
	return composerSpan{}, false
}

// composerRenderRow 重建单个可视行：占位符成块（imageTokenStyle）、选区段加高亮、
// caret 列以反显重绘。占位符优先于选区高亮 —— 被选中时仍保持自己的身份。
// 选区高亮不含行尾 wrap 追加的空格（与终端原生选择一致），被裁掉的空格
// 以普通样式补回以维持行宽。
func composerRenderRow(runes []rune, selFrom, selTo int, tokens []composerSpan, caretAt int, hasCaret bool) string {
	n := len(runes)
	if hasCaret && caretAt >= n {
		// caret 落在 wrap 尾随空格之后：textarea 行末会渲染一个空字符 caret。
		runes = append(append([]rune{}, runes...), ' ')
		n++
	}
	clamp := func(v int) int { return min(max(v, 0), n) }
	selFrom, selTo = clamp(selFrom), clamp(selTo)
	if selTo < selFrom {
		selFrom, selTo = selTo, selFrom
	}

	points := []int{0, n, selFrom, selTo}
	for _, tk := range tokens {
		points = append(points, clamp(tk.from), clamp(tk.to))
	}
	if hasCaret {
		caretAt = clamp(caretAt)
		points = append(points, caretAt, caretAt+1)
	}
	sort.Ints(points)
	uniq := points[:0]
	for _, p := range points {
		if len(uniq) == 0 || uniq[len(uniq)-1] != p {
			uniq = append(uniq, p)
		}
	}

	var b strings.Builder
	for k := 0; k+1 < len(uniq); k++ {
		from, to := uniq[k], uniq[k+1]
		if to <= from {
			continue
		}
		if hasCaret && from == caretAt && to == caretAt+1 {
			b.WriteString(composerCaretStyle.Render(string(runes[from:to])))
			continue
		}
		seg := string(runes[from:to])
		if tk, ok := composerSpanAt(tokens, from, to); ok {
			// ⑨：占位符成块显示（语义上原子，观感上也必须是一个整体）。
			rendered := imageTokenStyle.Render(seg)
			// ⑧：有落盘文件时，把这块文本变成可点击的超链接（点击打开原图）。
			// 链接**包在成块样式外面** —— 终端里 OSC 8 与 SGR 各自独立，嵌套后点击与配色都在。
			if tk.uri != "" {
				rendered = osc8Link(tk.uri, rendered)
			}
			b.WriteString(rendered)
			continue
		}
		if from >= selFrom && to <= selTo {
			trimmed := strings.TrimRight(seg, " ")
			if trimmed != "" {
				b.WriteString(mouseSelectionStyle.Render(trimmed))
				b.WriteString(seg[len(trimmed):])
			} else {
				b.WriteString(seg)
			}
		} else {
			b.WriteString(seg)
		}
	}
	return b.String()
}
