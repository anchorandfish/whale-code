package tui

import (
	"fmt"
	"strings"
	"time"

	exportpkg "github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/notify"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// mouse_selection.go — 鼠标拖选复制（批 C / C-1 + C-2）。
//
// 第一性原理（换前提）：**复制的真值由内容模型定义，不由屏幕（有损投影）定义**。
// 分两层，缺一不可：
// - C-1「这一行是什么内容」：渲染产地登记 source map（省略行 → 完整原文）；
// 复制按**逻辑行**取数（硬折行的 N 段视觉行只取一次）—— 见 transcript_source.go。
// - C-2「这一行在屏幕哪里」：选区从 viewport 坐标升格为**屏幕坐标**，
// 文本来源 = buildView() 那一帧拼接出的**整屏文档**（五区共用一套坐标）。
//
// 修前只有「中间那段对话」能拖选：确认框/选择面板/提示/状态栏只是 buildView 里
// 另外几个区块的拼接，根本不进 viewport ⇒ 选不中。现在五个区块都进同一份文档，
// 可分区选、可跨区块连选。

type mouseSelectionPoint struct {
	x int
	y int
}

// screenDocGeom 是「屏幕行 ↔ 文档行」的换算几何，**一次拖选固定一套**。
//
// 文档（screenDoc）按屏幕顺序把五个区块拼成一份可复制文本：
//
//	文档 = [preRows 个聊天前区块行] ‖ [chatFullLen 个聊天区全量内容行] ‖ [其余区块行]
//
// 其中聊天区是唯一的**可滚动**段：屏幕上看到的是它的 [YOffset, YOffset+chatVisible)
// 窗口；其余区块恒全量可见。于是屏幕行 → 文档行要按段落分别换算（见 docRowOfViewRow）。
type screenDocGeom struct {
	overflow     int // bubbletea 从渲染缓冲顶部砍掉的行数
	preRows      int // 聊天区之前的文档行数（toast）
	chatViewTop  int // 聊天区首行在**渲染缓冲**中的行号
	chatVisible  int // 聊天区可见行数（= viewport 高度）
	chatFullLen  int // 聊天区全部内容行数（含滚出屏幕的历史）
	afterViewTop int // 聊天区之后第一个区块在渲染缓冲中的行号

	// noScroll 表示文档里没有「全量聊天内容」（退化：只有可见内容）⇒
	// 换算聊天区时忽略 YOffset，否则会把可见窗口整体平移出错。
	noScroll bool
}

// chatTopScreen 返回聊天区首行的屏幕行号（可为负 ⇒ 该行被 bubbletea 从顶部砍掉）。
func (g screenDocGeom) chatTopScreen() int { return g.chatViewTop - g.overflow }

// chatBottomScreen 返回聊天区末行的下一行（屏幕行号）。
func (g screenDocGeom) chatBottomScreen() int { return g.chatTopScreen() + g.chatVisible }

// docRowOfViewRow 把**渲染缓冲行号**换算为文档行号。
func (g screenDocGeom) docRowOfViewRow(viewRow, yOffset int) int {
	switch {
	case viewRow < g.chatViewTop:
		// 聊天区之前的区块（toast）：文档里排在最前，行号与渲染缓冲一致。
		return viewRow
	case viewRow < g.chatViewTop+g.chatVisible:
		if g.noScroll {
			return g.preRows + (viewRow - g.chatViewTop)
		}
		return g.preRows + yOffset + (viewRow - g.chatViewTop)
	default:
		return g.preRows + g.chatFullLen + (viewRow - g.afterViewTop)
	}
}

// docRowOfScreenRow 同 docRowOfViewRow，但入参是**屏幕行号**（自动加回 overflow）。
func (g screenDocGeom) docRowOfScreenRow(screenRow, yOffset int) int {
	return g.docRowOfViewRow(screenRow+g.overflow, yOffset)
}

// screenDoc 是「整屏可复制文档」：五行并列数组，与 lines 一一对齐。
//
// - lines[k] ：第 k 条**文档行**的显示文本（剥 ANSI、去行尾空白）；
// - sources[k]：该行的真值来源（省略行 → 完整原文，见 transcript_source.go）；
// - owner[k] ：该行所属的**逻辑行**号（硬折行的 N 条视觉行同号；-1 = 无映射）；
// - logical ：逻辑行文本（折行前），复制按它取数、只取一次。
type screenDoc struct {
	lines   []string
	sources []lineSource
	owner   []int
	logical []string
	geom    screenDocGeom
}

// mouseSelectionState 以「屏幕坐标」存储选区：start/end.y 是**文档行号**
// （由屏幕行经 geom 换算，见 screenDocGeom），不随视口滚动变化 —— 拖选期间
// 配合边缘自动滚动把选区延伸到可视范围之外时，仍能取到完整文本。
//
// lines/sources/owner/logical 与 geom 在 press 时**同一次快照**产出；
// 它们的构造入口是 screenDocFromLayout / screenDocFallback，二者是唯一产地。
type mouseSelectionState struct {
	dragging bool
	hasText  bool
	start    mouseSelectionPoint
	end      mouseSelectionPoint

	lines   []string
	sources []lineSource
	owner   []int
	logical []string
	geom    screenDocGeom

	// pointerX/pointerY 是最近一次指针的屏幕坐标，供自动滚动 tick 使用
	//（指针停在边缘不动时没有 motion 事件，tick 需要记住位置继续滚动）。
	pointerX int
	pointerY int
	// autoScrolling 表示已有一条边缘自动滚动 tick 链在运行，防止重复调度。
	autoScrolling bool
}

const (
	// mouseSelectionScrollStep 是边缘自动滚动每个 tick 滚动的行数。
	mouseSelectionScrollStep = 3
	// mouseSelectionScrollPeriod 是边缘自动滚动的 tick 间隔。
	mouseSelectionScrollPeriod = 50 * time.Millisecond
)

// mouseSelectionAutoScrollMsg 驱动拖选边缘自动滚动的定时消息。
type mouseSelectionAutoScrollMsg struct{}

var mouseSelectionStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#4C566A")).
	Foreground(lipgloss.Color("#FFFFFF"))

// beginMouseSelection 处理左键按下：判断该行是否是可选文本，是则建立选区快照。
//
// 返回 false 表示「这一行不归拖选管」（按钮/输入框文字区/补全弹窗等可交互区），
// 调用方应继续走原有的输入区路由 —— 这保证「点击 Send/Stop、点输入框定位光标」
// 的行为完全不变。
//
// 账本可用时按**区块表**判定（五区共用一套坐标，见 layout.go screenLayout）；
// 账本不可用（loading 帧/未渲染夹具）时退回估算的聊天区窗口，即退化为「仅对话区可选」。
func (m *Model) beginMouseSelection(x, screenY int, l screenLayout, estChatTop, estInputTop int) bool {
	var doc screenDoc
	var ok bool
	if l.valid {
		r, found := l.regionAtScreenRow(screenY)
		if !found || !m.regionSelectable(r.name) {
			return false
		}
		doc, ok = m.screenDocFromLayout(l)
	} else {
		if screenY < estChatTop || screenY >= estInputTop {
			return false
		}
		doc, ok = m.screenDocFallback(estChatTop)
	}
	if !ok || len(doc.lines) == 0 {
		return false
	}
	y := clampMouseSelectionY(
		doc.geom.docRowOfScreenRow(screenY, m.uiService.Viewport().YOffset), len(doc.lines))
	m.mouseSelection = mouseSelectionState{
		dragging: true,
		start:    mouseSelectionPoint{x: max(0, x), y: y},
		end:      mouseSelectionPoint{x: max(0, x), y: y},
		lines:    doc.lines,
		sources:  doc.sources,
		owner:    doc.owner,
		logical:  doc.logical,
		geom:     doc.geom,
		pointerX: max(0, x),
		pointerY: screenY,
	}
	return true
}

// regionSelectable 判定某个区块是否是「可选文本区」。
//
// 可选的：toast / chat / monitor / status（纯文本展示区），以及**面板态**下的
// input（确认框/选择面板/文档导航没有 textarea 与按钮，整块是文本）。
// 不可选的：普通输入态的 input —— 那里有 textarea 拖选（composer_mouse.go）、
// Send/Stop 按钮点击、补全弹窗，保留既有交互（点击行为完全不变）。
func (m Model) regionSelectable(name string) bool {
	switch name {
	case "toast", "chat", "monitor", "status":
		return true
	case "input":
		switch m.sessionService.State() {
		case StateWaitingChoice, StateWaitingConfirm, StateViewingDoc:
			return true
		}
	}
	return false
}

// screenDocFromLayout 由**实测账本**构造整屏文档（C-2 的唯一产地，真机走这条）。
func (m Model) screenDocFromLayout(l screenLayout) (screenDoc, bool) {
	chatIdx := -1
	for i, r := range l.regions {
		if r.name == "chat" {
			chatIdx = i
			break
		}
	}
	if chatIdx < 0 {
		return screenDoc{}, false
	}
	chat := l.regions[chatIdx]
	afterViewTop := l.total
	var after []screenRegion
	if chatIdx+1 < len(l.regions) {
		afterViewTop = l.regions[chatIdx+1].viewTop
		after = l.regions[chatIdx+1:]
	}

	doc := screenDoc{}
	appendLiteral := func(content string) {
		for _, ln := range strings.Split(ansi.Strip(content), "\n") {
			doc.lines = append(doc.lines, strings.TrimRight(ln, " "))
			doc.sources = append(doc.sources, lineSource{})
			doc.owner = append(doc.owner, -1)
		}
	}
	for _, r := range l.regions[:chatIdx] {
		appendLiteral(r.content)
	}
	preRows := len(doc.lines)

	chatLines, chatSrcs, chatOwner, chatLogical := m.chatDocLines(chat)
	for k, ln := range chatLines {
		doc.lines = append(doc.lines, ln)
		src := lineSource{}
		if k < len(chatSrcs) {
			src = chatSrcs[k]
		}
		doc.sources = append(doc.sources, src)
		own := -1
		if k < len(chatOwner) {
			own = chatOwner[k]
		}
		doc.owner = append(doc.owner, own)
	}
	doc.logical = chatLogical

	for _, r := range after {
		appendLiteral(r.content)
	}

	doc.geom = screenDocGeom{
		overflow:     l.overflow,
		preRows:      preRows,
		chatViewTop:  chat.viewTop,
		chatVisible:  chat.rows,
		chatFullLen:  len(chatLines),
		afterViewTop: afterViewTop,
		noScroll:     len(m.viewportContent) == 0,
	}
	return doc, len(doc.lines) > 0
}

// screenDocFallback 由**估算几何**构造文档（账本不可用时的退化路径）。
//
// 它不是"另一套正确实现"：真机每一帧都渲染过、走 screenDocFromLayout。
// 这里只保证 loading 帧/单测夹具下命中测试不至于打到 nil，且逐字等价于修前
// （仅有对话区、且按 YOffset 支持滚出屏幕的拖选）。
func (m Model) screenDocFallback(estChatTop int) (screenDoc, bool) {
	content := m.viewportContent
	noScroll := false
	if content == "" {
		content = m.uiService.Viewport().View()
		noScroll = true
	}
	raw := strings.Split(ansi.Strip(content), "\n")
	doc := screenDoc{}
	for k, ln := range raw {
		doc.lines = append(doc.lines, strings.TrimRight(ln, " "))
		src := lineSource{}
		if k < len(m.viewportSources) {
			src = m.viewportSources[k]
		}
		doc.sources = append(doc.sources, src)
		own := -1
		if k < len(m.viewportOwner) {
			own = m.viewportOwner[k]
		}
		doc.owner = append(doc.owner, own)
	}
	doc.logical = m.viewportLogical
	doc.geom = screenDocGeom{
		overflow:     0,
		preRows:      0,
		chatViewTop:  estChatTop,
		chatVisible:  m.uiService.Viewport().Height,
		chatFullLen:  len(raw),
		afterViewTop: len(raw),
		noScroll:     noScroll,
	}
	return doc, len(doc.lines) > 0
}

// chatDocLines 返回聊天区在文档里的逐行内容与来源。
//
// 优先取**全量**视口内容（含滚出屏幕的历史行），使边缘自动滚动可复制全部历史；
// 缺失（裸 setViewportContent / 夹具）时退化为当前可见内容，并标记 noScroll。
func (m Model) chatDocLines(chat screenRegion) ([]string, []lineSource, []int, []string) {
	if len(m.viewportContent) > 0 {
		lines := strings.Split(ansi.Strip(m.viewportContent), "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " ")
		}
		// 补足到可见窗口底部：viewport 会把内容补齐到高度，缺的那些是空行，
		// 也要有对应文档行，否则「屏幕行 ↔ 文档行」的换算会在尾部错位。
		if need := m.uiService.Viewport().YOffset + chat.rows; len(lines) < need {
			lines = append(lines, make([]string, need-len(lines))...)
		}
		return lines, m.viewportSources, m.viewportOwner, m.viewportLogical
	}
	lines := strings.Split(ansi.Strip(chat.content), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines, nil, nil, nil
}

// updateMouseSelection 用拖选快照的几何换算文档行（不再按次传入，杜绝
// 「press 与 motion 用了两套坐标」造成的半路跳行）。
func (m *Model) updateMouseSelection(x, screenY int) {
	if !m.mouseSelection.dragging || len(m.mouseSelection.lines) == 0 {
		return
	}
	m.mouseSelection.pointerX = max(0, x)
	m.mouseSelection.pointerY = screenY
	y := clampMouseSelectionY(
		m.mouseSelection.geom.docRowOfScreenRow(screenY, m.uiService.Viewport().YOffset),
		len(m.mouseSelection.lines))
	m.mouseSelection.end = mouseSelectionPoint{x: max(0, x), y: y}
	m.mouseSelection.hasText = m.mouseSelectionText() != ""
}

// mouseSelectionEdgeDirection 返回拖选自动滚动方向：-1 向上（露出更早内容），
// 1 向下（露出更新内容），0 指针不在聊天区的边缘行。
//
// ★ 与修前的差别：修前把「聊天区末行**及以下**」都算下边缘，于是拖到状态栏/输入区
// 也会滚动。C-2 把选区升格到整屏后，聊天区以下的行是**可选的别的区块文本**，
// 不应再触发滚动 ⇒ 下边缘收窄为「聊天区末行这一行」。
func mouseSelectionEdgeDirection(screenY, chatTop, chatBottom int) int {
	if chatBottom <= chatTop {
		return 0
	}
	switch {
	case screenY <= chatTop:
		return -1
	case screenY >= chatBottom-1 && screenY < chatBottom:
		return 1
	default:
		return 0
	}
}

// updateMouseSelectionAutoScroll 在拖选 motion 时维护指针位置并按需启停
// 边缘自动滚动 tick 链。返回新启动的 tick Cmd（已在滚动中或不在边缘时为 nil）。
func (m *Model) updateMouseSelectionAutoScroll(x, screenY int) tea.Cmd {
	if !m.mouseSelection.dragging {
		return nil
	}
	m.mouseSelection.pointerX = max(0, x)
	m.mouseSelection.pointerY = screenY
	g := m.mouseSelection.geom
	if mouseSelectionEdgeDirection(screenY, g.chatTopScreen(), g.chatBottomScreen()) == 0 {
		m.mouseSelection.autoScrolling = false
		return nil
	}
	if m.mouseSelection.autoScrolling {
		return nil
	}
	m.mouseSelection.autoScrolling = true
	return mouseSelectionAutoScrollTick()
}

func mouseSelectionAutoScrollTick() tea.Cmd {
	return tea.Tick(mouseSelectionScrollPeriod, func(time.Time) tea.Msg {
		return mouseSelectionAutoScrollMsg{}
	})
}

// handleMouseSelectionAutoScroll 处理自动滚动 tick：指针停在聊天区边缘时滚动视口
// 并把选区终点延伸到滚出的行；到达内容边界或拖选结束后停止 tick 链。
func (m *Model) handleMouseSelectionAutoScroll() tea.Cmd {
	sel := &m.mouseSelection
	if !sel.dragging || !sel.autoScrolling {
		sel.autoScrolling = false
		return nil
	}
	// 坐标口径与拖选开始时一致（sel.geom），避免 tick 用另一套估算值导致跳行。
	vp := m.uiService.Viewport()
	before := vp.YOffset
	switch mouseSelectionEdgeDirection(sel.pointerY, sel.geom.chatTopScreen(), sel.geom.chatBottomScreen()) {
	case -1:
		vp.LineUp(mouseSelectionScrollStep)
	case 1:
		vp.LineDown(mouseSelectionScrollStep)
	}
	if vp.YOffset == before {
		// 已到内容顶部/底部，继续 tick 也不会滚动。
		sel.autoScrolling = false
		return nil
	}
	m.updateMouseSelection(sel.pointerX, sel.pointerY)
	return mouseSelectionAutoScrollTick()
}

func (m *Model) finishMouseSelection() tea.Cmd {
	if !m.mouseSelection.dragging {
		return nil
	}
	m.mouseSelection.dragging = false
	m.mouseSelection.autoScrolling = false
	text := m.mouseSelectionText()
	if strings.TrimSpace(text) == "" {
		m.clearMouseSelection()
		return nil
	}
	m.mouseSelection.hasText = true

	clipboard := exportpkg.NewClipboard()
	if m.notifier != nil {
		notification := notify.NewNotification(
			"mouse-selection-copy",
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

func (m *Model) clearMouseSelection() {
	m.mouseSelection = mouseSelectionState{}
}

// highlightScreenSelection 给当前帧的选中行上底色（终端式高亮）。
//
// C-2：选区跨整屏 ⇒ 高亮也必须跨整屏。做法是对拼好的视图做**一次后处理**：
// 只对选中的屏幕行剥 ANSI 上底色，未选中行原样保留（不牺牲整屏配色）。
// 单元格映射走与复制**同一份** geom（屏幕行 → 文档行），保证"高亮到哪"与
// "复制到什么"读的是同一份事实。
//
// 陈旧帧防护：某行当前文本与快照不一致时跳过该行，避免高亮错位到无关文本。
func (m Model) highlightScreenSelection(view string, l screenLayout) string {
	sel := m.mouseSelection
	if (!sel.dragging && !sel.hasText) || len(sel.lines) == 0 {
		return view
	}
	start, end := normalizedMouseSelection(sel.start, sel.end, len(sel.lines))
	yOffset := m.uiService.Viewport().YOffset
	lines := strings.Split(view, "\n")
	changed := false
	for i := range lines {
		docRow := sel.geom.docRowOfViewRow(i, yOffset)
		if docRow < start.y || docRow > end.y || docRow < 0 || docRow >= len(sel.lines) {
			continue
		}
		plain := strings.TrimRight(ansi.Strip(lines[i]), " ")
		if plain != strings.TrimRight(sel.lines[docRow], " ") {
			continue
		}
		from, to := 0, displayWidth(plain)
		switch {
		case start.y == end.y:
			from, to = start.x, end.x
		case docRow == start.y:
			from = start.x
		case docRow == end.y:
			to = end.x
		}
		if to < from {
			from, to = to, from
		}
		if to <= from {
			continue
		}
		prefix, selected, suffix := splitDisplayCells(plain, from, to)
		lines[i] = prefix + mouseSelectionStyle.Render(selected) + suffix
		changed = true
	}
	if !changed {
		return view
	}
	return strings.Join(lines, "\n")
}

// mouseSelectionText 返回选区将写入剪贴板的文本。
//
// 取数规则（批 C 换前提后）：
// - **按逻辑行取数**，不按视觉行：选区覆盖的连续视觉行若同属一条逻辑行
// （硬折行 的 N 段），整组只取一次 ⇒ 复制得到整条逻辑行，中间不断开。
// - 组内若登记了**省略原文**（思考预览 / 命令截断 / 工具结果折叠，见
// transcript_source.go）⇒ 整组用原文替换，**不按显示列切片**（摘要的列号与
// 原文无对应关系）。
// - 其余行取逻辑行的连续切片（起点/终点按各视觉行在逻辑行内的列偏移换算）。
//
// 快照退化（无 source map）时按旧行为逐视觉行切片，即"退化必须等于修前"。
func (m Model) mouseSelectionText() string {
	sel := m.mouseSelection
	if len(sel.lines) == 0 {
		return ""
	}
	start, end := normalizedMouseSelection(sel.start, sel.end, len(sel.lines))
	if start.y == end.y && start.x == end.x {
		return ""
	}
	parts := make([]string, 0, end.y-start.y+1)
	for y := start.y; y <= end.y; {
		groupEnd := y
		if y < len(sel.owner) {
			owner := sel.owner[y]
			for groupEnd+1 <= end.y && groupEnd+1 < len(sel.owner) && sel.owner[groupEnd+1] == owner {
				groupEnd++
			}
		}
		from := 0
		if y == start.y {
			from = start.x
		}
		to := displayWidth(strings.TrimRight(sel.lines[groupEnd], " "))
		if groupEnd == end.y {
			to = end.x
		}
		parts = append(parts, mouseSelectionGroupText(sel, y, groupEnd, from, to))
		y = groupEnd + 1
	}
	return strings.Join(parts, "\n")
}

// mouseSelectionGroupText 返回「同属一条逻辑行的一组视觉行 [y0,y1]」在选区内的文本
// （from/to 分别是组首、组末视觉行内的选中列）。
//
// 两条取数规则：
// 1. 组首视觉行登记了**省略原文** ⇒ 整组返回该原文（摘要的列号与原文无对应关系）。
// 2. 否则返回**逻辑行**的连续切片：起点 = 组首视觉行在逻辑行内的列偏移 + from，
// 终点 = 末视觉行的列偏移 + to。于是被硬折成 N 段的逻辑行复制出来是**整条**。
//
// 无 source map（快照退化）时退回旧的"逐视觉行按显示列切片"行为。
func mouseSelectionGroupText(sel mouseSelectionState, y0, y1, from, to int) string {
	if src := sourceAt(sel.sources, y0); src.hasOverride() {
		return src.full
	}
	owner := -1
	if y0 < len(sel.owner) {
		owner = sel.owner[y0]
	}
	if owner < 0 || owner >= len(sel.logical) {
		parts := make([]string, 0, y1-y0+1)
		for k := y0; k <= y1; k++ {
			line := strings.TrimRight(sel.lines[k], " ")
			switch {
			case y0 == y1:
				parts = append(parts, displaySlice(line, from, to))
			case k == y0:
				parts = append(parts, displaySlice(line, from, displayWidth(line)))
			case k == y1:
				parts = append(parts, displaySlice(line, 0, to))
			default:
				parts = append(parts, line)
			}
		}
		return strings.Join(parts, "\n")
	}
	startCol := from
	for k := y0 - 1; k >= 0 && k < len(sel.owner) && sel.owner[k] == owner; k-- {
		startCol += displayWidth(strings.TrimRight(sel.lines[k], " "))
	}
	endCol := to
	for k := y0; k < y1 && k < len(sel.owner) && sel.owner[k] == owner; k++ {
		endCol += displayWidth(strings.TrimRight(sel.lines[k], " "))
	}
	return displaySlice(sel.logical[owner], startCol, endCol)
}

func normalizedMouseSelection(start, end mouseSelectionPoint, lineCount int) (mouseSelectionPoint, mouseSelectionPoint) {
	start.y = clampMouseSelectionY(start.y, lineCount)
	end.y = clampMouseSelectionY(end.y, lineCount)
	start.x = max(0, start.x)
	end.x = max(0, end.x)
	if start.y > end.y || (start.y == end.y && start.x > end.x) {
		return end, start
	}
	return start, end
}

func clampMouseSelectionY(y, lineCount int) int {
	if lineCount <= 0 {
		return 0
	}
	if y < 0 {
		return 0
	}
	if y >= lineCount {
		return lineCount - 1
	}
	return y
}

func displayWidth(s string) int {
	return runewidth.StringWidth(s)
}

func displaySlice(s string, from, to int) string {
	if to < from {
		from, to = to, from
	}
	_, selected, _ := splitDisplayCells(s, max(0, from), max(0, to))
	return selected
}

func splitDisplayCells(s string, from, to int) (prefix, selected, suffix string) {
	if to < from {
		from, to = to, from
	}
	from = max(0, from)
	to = max(from, to)
	var p, sel, suf strings.Builder
	column := 0
	for _, r := range s {
		width := runewidth.RuneWidth(r)
		if width < 0 {
			width = 0
		}
		next := column + width
		switch {
		case next <= from:
			p.WriteRune(r)
		case column >= to:
			suf.WriteRune(r)
		default:
			sel.WriteRune(r)
		}
		column = next
	}
	return p.String(), sel.String(), suf.String()
}
