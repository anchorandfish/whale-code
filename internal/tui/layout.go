package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// layout.go — 「屏幕上每一行归谁」的唯一事实源（N-MOUSE-ROW，2026-09-30）。
//
// 背景（用户真机报障：「tui 页面上鼠标选中的位置，复制到的却是它上面的部分，产生了位移」）：
//
//	View() 渲染出的行数若超过终端高度，bubbletea v1.2.4
//	（standard_renderer.go:186-188）会**从渲染缓冲顶部砍掉多出来的行** ——
//	因为它无法把光标移进 scrollback：
//
// if r.height > 0 && len(newLines) > r.height {
// newLines = newLines[len(newLines)-r.height:]
// }
//
//	于是「屏幕第 Y 行」= 渲染缓冲第 Y+overflow 行。而模型侧原本用
//	「各区块高度估算值」反推坐标（inputAreaTop - vp.Height 之类），
//	只要估算漏了一个区块（monitor strip）或少算一行（preview 换行），
//	坐标就整体位移 overflow 行 —— 表现正是「选这里、复制到上面」。
//
// 因此这里的数字**只由 View() 真正拼接时实测得出**，鼠标命中测试只读、不算。
// 新增任何常驻区块都必须登记进 buildView 的区块表：不登记 ⇒ 它既不进高度预算
// （渲染超行）也不会出现在坐标账本里（鼠标位移），两处症状同源、同门守住。

// screenRegion 是 View() 的一个占屏区块及其在渲染缓冲中的行区间。
//
// ★ 它是「屏幕第几行归谁」的**逐行答案**（批 C / C-2，2026-10-08）：鼠标选区
// 从「viewport 坐标」升格为「屏幕坐标」后，需要把**任意**屏幕行反查回区块
// （从而知道它是不是可选文本、属于哪个区块、在文档中的序位）。此前只有
// chatTop/inputTop 两个 Y 阈值，弹窗/提示/状态栏都不在账本里 ⇒ 选不中。
type screenRegion struct {
	name    string
	viewTop int    // 该区块首行在**渲染缓冲**中的行号（未扣 overflow）
	rows    int    // 该区块占的行数
	content string // 该区块渲染文本（未剥 ANSI）
}

// screenLayout 是当前帧的屏幕几何账本，坐标一律为【屏幕坐标系】
// （即已扣除 bubbletea 从顶部砍掉的 overflow 行）。
type screenLayout struct {
	// valid 表示账本可用：View() 处于 loading/quitting 占位时不产生有效几何。
	valid bool
	total int // 渲染缓冲总行数
	// overflow 是 bubbletea 从渲染缓冲顶部砍掉的行数（total - 终端高度）。
	overflow  int
	chatTop   int // 聊天区首行在屏幕上的行号
	chatRows  int // 聊天区占的行数
	inputTop  int // 输入区首行在屏幕上的行号
	inputRows int // 输入区占的行数
	regions   []screenRegion
}

// chatBottom 返回聊天区末行的下一行（即 monitor/input 区起点）。
func (l screenLayout) chatBottom() int { return l.chatTop + l.chatRows }

// regionAtViewRow 返回渲染缓冲第 viewRow 行所属的区块。
//
// 未命中（越界 / 账本不可用）返回 ok=false —— 调用方必须据此回退，不得猜测。
func (l screenLayout) regionAtViewRow(viewRow int) (screenRegion, bool) {
	if !l.valid {
		return screenRegion{}, false
	}
	for _, r := range l.regions {
		if viewRow >= r.viewTop && viewRow < r.viewTop+r.rows {
			return r, true
		}
	}
	return screenRegion{}, false
}

// regionAtScreenRow 同 regionAtViewRow，但入参是**屏幕行号**（自动加回 overflow）。
func (l screenLayout) regionAtScreenRow(screenRow int) (screenRegion, bool) {
	return l.regionAtViewRow(screenRow + l.overflow)
}

// buildView 产出最终视图字符串**和**几何账本。
//
// 它是「屏幕上有什么、各占第几行」的唯一事实源：View() 用它渲染，
// 鼠标命中测试用 measureScreenLayout() 读它。两者同源，估算误差无处藏身。
func (m Model) buildView() (string, screenLayout) {
	m.reassignServices()
	if m.sessionService.Quitting() {
		return "\nSession saved. Goodbye!\n\n", screenLayout{}
	}
	if !m.uiService.Ready() {
		return titleStyle.Render("Whale Code loading...") + "\n", screenLayout{}
	}

	var regions []screenRegion
	add := func(name, content string) {
		// 空块不占行：旧实现用固定 Sprintf 模板拼接，monitorStrip/toast 为空时
		// 会留下空行（多渲染一行 ⇒ 又一处位移源）。
		if content == "" {
			return
		}
		regions = append(regions, screenRegion{name: name, content: content})
	}

	if m.toast != nil {
		add("toast", m.toast.View())
	}
	// A-2：Monitor 摘要区 —— 后台进程在主界面的唯一呈现，无则不占屏。
	add("chat", m.uiService.Viewport().View())
	add("monitor", m.renderMonitorStrip())
	add("input", m.renderInputAreaForState())
	add("status", m.renderStatusBar())

	// 拼接 + 逐块累计行号（"\n" join 不产生额外空行）。
	viewTop := 0
	for i := range regions {
		regions[i].viewTop = viewTop
		regions[i].rows = lipgloss.Height(regions[i].content)
		viewTop += regions[i].rows
	}
	parts := make([]string, 0, len(regions))
	for _, r := range regions {
		parts = append(parts, r.content)
	}
	view := strings.Join(parts, "\n")

	total := lipgloss.Height(view)
	overflow := total - m.uiService.Height()
	if overflow < 0 {
		overflow = 0
	}
	chatIdx := -1
	for i, r := range regions {
		if r.name == "chat" {
			chatIdx = i
			break
		}
	}
	if chatIdx < 0 {
		// 聊天区恒非空（viewport 会用空行补足高度）；真缺失时账本不可用，
		// 鼠标路由回退到估算值，不会静默产生位移。
		return view, screenLayout{}
	}
	inputIdx := -1
	for i, r := range regions {
		if r.name == "input" {
			inputIdx = i
			break
		}
	}
	if inputIdx < 0 {
		inputIdx = len(regions) - 1
	}
	l := screenLayout{
		valid:     true,
		total:     total,
		overflow:  overflow,
		regions:   regions,
		chatTop:   regions[chatIdx].viewTop - overflow,
		chatRows:  regions[chatIdx].rows,
		inputTop:  regions[inputIdx].viewTop - overflow,
		inputRows: regions[inputIdx].rows,
	}
	// 鼠标整屏高亮：一次性后处理（只对**选中行**剥 ANSI 上底色，未选中行保留原样式）。
	view = m.highlightScreenSelection(view, l)
	return view, l
}

// measureScreenLayout 渲染当前帧并给出几何账本（与 View() 同一实现）。
//
// 只在低频鼠标事件（press/release）调用一次；拖选期间的 motion 沿用
// beginMouseSelection 时的快照，避免每次指针移动都整帧重渲染。
func (m Model) measureScreenLayout() screenLayout {
	_, l := m.buildView()
	return l
}

// isWheelButton 判定滚轮事件（滚轮不带位移语义，且频率高，不必实测几何）。
// 常量名对齐 bubbletea v1.2.4 mouse.go:31-34 MouseEvent.IsWheel。
func isWheelButton(b tea.MouseButton) bool {
	return b == tea.MouseButtonWheelUp || b == tea.MouseButtonWheelDown ||
		b == tea.MouseButtonWheelLeft || b == tea.MouseButtonWheelRight
}

// mouseGeometry 给出本次鼠标事件应使用的屏幕几何（账本 + 输入区/聊天区首行行号）。
//
// 取数优先级（渲染实测 > 估算）：
// 1. 拖选进行中 ⇒ 沿用 beginMouseSelection 时的实测快照（一次拖选一套坐标）；
// 2. 低频事件（press/release）⇒ buildView 实测，连同**区块表**一起返回（C-2）；
// 3. 高频事件（滚轮、非拖选的 motion）与「还没渲染过」⇒ 回退 recalcViewportHeight 的估算值。
//
// 回退不是"另一套正确实现"，只是为了让 loading 帧/单测夹具不至于把命中测试
// 打到 nil；真机每一帧都渲染过，走的是 1/2 两条。
func (m Model) mouseGeometry(msg tea.MouseMsg) (screenLayout, int, int) {
	inputTop := m.lastInputAreaTop
	chatTop := inputTop - m.uiService.Viewport().Height
	if chatTop < 0 {
		chatTop = 0
	}
	if m.mouseSelection.dragging {
		return screenLayout{}, inputTop, m.mouseSelection.geom.chatTopScreen()
	}
	if isWheelButton(msg.Button) || msg.Action == tea.MouseActionMotion {
		return screenLayout{}, inputTop, chatTop
	}
	if l := m.measureScreenLayout(); l.valid {
		return l, l.inputTop, l.chatTop
	}
	return screenLayout{}, inputTop, chatTop
}

// renderInputAreaForState 按会话状态返回输入区渲染结果（View 与布局账本共用，
// 避免「渲染一套、测高另一套」）。
func (m Model) renderInputAreaForState() string {
	switch {
	case m.sessionService.State() == StateWaitingChoice && m.toolService.PendingChoice() != nil:
		return m.renderChoiceUI()
	case m.sessionService.State() == StateWaitingConfirm && m.toolService.PendingConfirm() != nil:
		return m.renderConfirmUI()
	case m.sessionService.State() == StateViewingDoc:
		return m.renderDocNavUI()
	default:
		return m.renderInputArea()
	}
}
