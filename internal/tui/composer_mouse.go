// composer_mouse.go — 输入框（composer）鼠标拖选。
//
// bubbles textarea v0.20.0 不处理 MouseMsg：点击输入框仅由 TUI 侧手动 SetCursor
// 定位光标，拖动事件被丢弃，用户无法用鼠标选中已输入的文字。本文件在不改变
// 对话区拖选（mouse_selection.go）与键盘选区（composer_selection.go）的前提下，
// 为输入框叠加鼠标拖选状态机，并与键盘选区共享 composerSelectionState：
//
// - press（textarea 文字区）：清除旧选区 → 光标精确定位到点击的逻辑行列
// （修复 setCursorByMouseX 多行只能定位第一行的限制）→ 记录锚点
// - motion：光标随指针移动，选区 = 锚点..光标（渲染高亮复用 renderComposerDecorations）
// - release：结束拖选，选区保留（编辑器惯例：随后可 Ctrl+C 复制 / Backspace
// 删除 / 输入替换，全部复用键盘选区路径）
// - 屏幕坐标 → 逻辑坐标：复刻 textarea wrap 后按可视行映射，窗口偏移复用
// composerMatchWindowOffset 内容匹配（textarea 未导出内部 viewport 滚动量）
// - 指针拖出文字区上下界：clamp 到首个/末个可视行（拖出顶部=选到开头，
// 拖出底部=选到末尾）
// - 守卫与键盘选区一致（composerSelectionKeyEnabled）：vim normal、typeahead
// 弹窗、Send/Stop 焦点、非可编辑状态不启动拖选（仍保留光标定位）
package tui

import (
	"log"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// composerMouseDragState 输入框鼠标拖选状态机：dragging 表示按住左键拖选中。
// 选区本身存于 composerSelection（锚点+光标），与键盘选区共用同一状态。
type composerMouseDragState struct {
	dragging bool
	// textTop 是本次拖选开始时**实测**的输入框文字首行屏幕 Y（layout.go）。
	// 一次拖选一套坐标：既避免高频 motion 反复整帧渲染，也保证拖动中途
	// 布局变化（toast/Monitor 摘要区出现）不会让选区半路改坐标系而跳行。
	textTop int
}

// composerTextXOffset 输入框文字区相对屏幕坐标的 X 偏移：
// inputBoxStyle 左边框(1) + PaddingLeft(1)。
const composerTextXOffset = 2

// composerTextTop 返回 textarea 文字首行的屏幕 Y：
// 输入区顶部 + 排队输入预览高度 + inputBoxStyle 上边框(1)。
//
// N-MOUSE-ROW：输入区顶部取**渲染实测**（layout.go），与对话区拖选
// 共用同一套账本。旧值 lastInputAreaTop 是 recalcViewportHeight 的估算值：
// 只要有一个占屏区块漏进高度预算，View() 就比终端多几行、bubbletea 从顶部砍行
// ⇒ 屏幕 Y 与模型 Y 差几行 ⇒ 输入框点击/拖选同样位移（与对话区拖选同一根因）。
// 未渲染过（loading 帧/单测夹具）时回退估算值。
func (m Model) composerTextTop() int {
	if l := m.measureScreenLayout(); l.valid {
		return l.inputTop + m.lastPreviewHeight + 1
	}
	return m.lastInputAreaTop + m.lastPreviewHeight + 1
}

// composerVisualRowInfo 单个可视行及其逻辑定位：
// line 是逻辑行号，start 是该可视行首 rune 在逻辑行内的偏移，
// runes 含 wrap 尾随空格 quirk（与 renderComposerDecorations 同构）。
type composerVisualRowInfo struct {
	line  int
	start int
	runes []rune
}

// composerVisualRowsInfo 构造全部可视行的逻辑索引（每逻辑行独立计 start，
// 软换行行尾 quirk 空格只出现在该行最后一个可视行尾部，不影响行内偏移）。
func composerVisualRowsInfo(lines []string, width int) []composerVisualRowInfo {
	var vrows []composerVisualRowInfo
	for i, line := range lines {
		start := 0
		for _, r := range composerWrap([]rune(line), width) {
			vrows = append(vrows, composerVisualRowInfo{line: i, start: start, runes: r})
			start += len(r)
		}
	}
	return vrows
}

// composerPointFromScreen 把输入框区域内的屏幕坐标映射为 textarea 逻辑坐标
// (row, col)。窗口偏移通过 View() 内容匹配推断（textarea 未导出内部 viewport
// YOffset）；匹配失败返回 ok=false（调用方保持原有降级行为，不产生错误选区）。
// Y 超出文字区时 clamp 到首个/末个可视行；X 超出时 clamp 到行末。
func (m Model) composerPointFromScreen(x, y int) (row, col int, ok bool) {
	return m.composerPointFromScreenAt(x, y, m.composerTextTop())
}

// composerPointFromScreenAt 同 composerPointFromScreen，但显式传入文字首行屏幕 Y
// （拖选期间复用按下时实测的快照，见 composerMouseDragState.textTop）。
func (m Model) composerPointFromScreenAt(x, y, textTop int) (row, col int, ok bool) {
	ta := m.uiService.Textarea()

	// ★ ：空输入短路。bubbles textarea 在「值为空 + 光标在 (0,0) + Placeholder 非空」时
	// View() 走 placeholderView 分支（vendor/github.com/charmbracelet/bubbles/textarea/textarea.go:1092-1094），
	// 渲染的是 **Placeholder 文本**；而下方窗口匹配所用的 visualRows 是由 value 复刻 wrap 得到
	// ⇒ 两者必然失配（实测 width=10：view[0]="Enter=send" vs visualRows[0]=" "）。
	// 此时逻辑坐标唯一确定为 (0,0)，无需也无法用内容匹配推断窗口偏移。
	// 旧实现在空输入框上每次点击/拖动都 fail-closed 并写一条 canary，是 whale.log 的
	// 噪声源（真机实测单次成簇 15 条）。
	// 守卫条件与 bubbles 的 placeholder 分支逐字对齐 —— bubbles 改条件时同步此处。
	if ta.Placeholder != "" && ta.Value() == "" {
		if r, c := composerCursorPos(ta); r == 0 && c == 0 {
			return 0, 0, true
		}
	}

	width := ta.Width()
	lines := composerValueLines(ta)

	vrows := composerVisualRowsInfo(lines, width)
	if len(vrows) == 0 {
		return 0, 0, true
	}

	// 光标可视行号（与 renderComposerDecorations 同款计算）。
	li := ta.LineInfo()
	caretVisual := 0
	for i := 0; i < ta.Line() && i < len(lines); i++ {
		caretVisual += len(composerWrap([]rune(lines[i]), width))
	}
	caretVisual += li.RowOffset

	viewLines := strings.Split(ansi.Strip(ta.View()), "\n")
	offset := composerMatchWindowOffset(viewLines, composerVisualRows(lines, width), ta.Height(), caretVisual)
	if offset < 0 {
		// canary：wrap 复刻与 textarea 实际渲染失配（如 bubbles 升级改渲染）
		// 会让拖选静默失效。此处 fail-closed 不产生错误选区，但必须留观测出口，
		// 否则该类 bug 不可见（renderComposerDecorations 的同款失配共享此症状）。
		log.Printf("[tui] composer mouse selection: window offset match failed (canary)")
		return 0, 0, false
	}

	vi := offset + (y - textTop)
	if vi < 0 {
		vi = 0
	}
	if vi >= len(vrows) {
		vi = len(vrows) - 1
	}
	v := vrows[vi]

	// 屏幕 X → 可视行内 rune 列（宽字符按显示宽度累计）。
	xCell := x - composerTextXOffset
	if xCell < 0 {
		xCell = 0
	}
	runecol := 0
	accum := 0
	for _, r := range v.runes {
		if accum >= xCell {
			break
		}
		accum += runewidth.RuneWidth(r)
		runecol++
	}
	col = v.start + runecol
	// wrap 尾随空格 quirk 会让点击行尾右侧落列超过逻辑行末：clamp。
	if lineLen := len([]rune(lines[v.line])); col > lineLen {
		col = lineLen
	}
	return v.line, col, true
}

// beginComposerMouseDrag 处理输入框文字区的鼠标按下：
// 清除旧选区 → 精确定位光标 → 记录锚点并进入拖选状态。
// 守卫与键盘选区一致（vim normal、typeahead、Send/Stop 焦点等不启动拖选，
// 但保留光标定位）；坐标映射失败时保持调用前已有定位（降级不报错）。
func (m *Model) beginComposerMouseDrag(x, y int) {
	m.clearComposerSelection()
	enabled := m.composerSelectionKeyEnabled()
	// press 时实测一次几何，整个拖选沿用（注释见 composerMouseDragState.textTop）。
	textTop := m.composerTextTop()
	row, col, ok := m.composerPointFromScreenAt(x, y, textTop)
	if ok {
		// 点击落点骑在占位符单元内部时吸附到最近边界（平局取本体前）——
		// 光标不落进 [Image #N] 里面，拖选锚点（= 按下处）随之同点。
		row, col = m.composerSnapPoint(row, col)
		m.composerMoveCursorTo(row, col)
	}
	if !enabled || !ok {
		return
	}
	m.composerMouseDrag.dragging = true
	m.composerMouseDrag.textTop = textTop
	// 锚点 = 按下位置；另一端（光标）随拖动移动（extendComposerMouseDrag）。
	m.composerSelection.anchorRow, m.composerSelection.anchorCol = row, col
}

// extendComposerMouseDrag 处理拖选中的鼠标移动：光标移到指针位置，
// 选区（锚点..光标）随之扩展并激活高亮。
func (m *Model) extendComposerMouseDrag(x, y int) {
	if !m.composerMouseDrag.dragging {
		return
	}
	// 复用按下时实测的 textTop：不重新整帧渲染，也不中途改坐标系。
	row, col, ok := m.composerPointFromScreenAt(x, y, m.composerMouseDrag.textTop)
	if !ok {
		return
	}
	// 拖选光标端同样不骑进占位符内部（锚点已按 press 吸附）。
	row, col = m.composerSnapPoint(row, col)
	m.composerMoveCursorTo(row, col)
	m.composerSelection.active = true
}

// composerSnapPoint 把一个输入框逻辑坐标吸附出占位符单元。
// 不在任何单元内部时原样返回。
func (m Model) composerSnapPoint(row, col int) (int, int) {
	lines := composerValueLines(m.uiService.Textarea())
	if row < 0 || row >= len(lines) {
		return row, col
	}
	if target, ok := imagePlaceholderSnapCol(lines[row], col); ok {
		return row, target
	}
	return row, col
}

// endComposerMouseDrag 处理松开左键：结束拖选；空选区（单击未拖动）清除
// 选区状态，非空选区保留供后续 Ctrl+C 复制 / Backspace 删除 / 输入替换。
func (m *Model) endComposerMouseDrag() {
	if !m.composerMouseDrag.dragging {
		return
	}
	m.composerMouseDrag.dragging = false
	if m.composerSelection.active && m.composerSelectionText() == "" {
		m.clearComposerSelection()
	}
}
