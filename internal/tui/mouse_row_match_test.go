package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/notify"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// mouse_row_match_test.go — 「鼠标选中屏幕第 k 行 ⇒ 复制到第 k 行的文本」判据（N-MOUSE-ROW）。
//
// 用户真机报障：「tui 页面上鼠标选中的位置，复制到的却是它上面的部分，产生了位移」。
//
// 物理机制（实测 + bubbletea 源码实读）：
// - bubbletea v1.2.4 standard_renderer.go:186-188：
// `if r.height > 0 && len(newLines) > r.height { newLines = newLines[len(newLines)-r.height:] }`
// ⇒ View() 渲染行数超过终端高度时，**顶部多出来的行被直接丢弃**（无法把光标移进 scrollback）。
// - 于是「屏幕第 Y 行」= 渲染缓冲第 Y+overflow 行，而模型侧所有坐标
// （lastInputAreaTop / chatAreaTop）都是按「渲染第 0 行 = 屏幕第 0 行」算的
// ⇒ 只要 View() 行数 ≠ 终端高度，鼠标拖选就整体位移 overflow 行。
//
// 判据形态（结构面，不依赖散文关键词）：把 View() 交给**与 bubbletea 同一条裁剪规则**得到
// 「用户在屏幕上真正看到的行」，再用生产入口（Update(MouseMsg)）拖选屏幕同一行，
// 断言复制文本 == 该行渲染文本。故意让布局多渲染 1 行 ⇒ 本测试必须转红。
//
// 生产入口：NewModel → WindowSizeMsg → MonitorMgr.Start（真进程）→ Model.Update(MouseMsg) →
// mouseSelectionText()（finishMouseSelection 交给剪贴板的就是它的返回值）。

// screenLines 复刻 bubbletea 的裁剪规则：渲染缓冲超出终端高度时从**顶部**丢弃。
// 参考 bubbletea v1.2.4 standard_renderer.go:186-188。
func screenLines(t *testing.T, m *Model) []string {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	overflow := len(lines) - m.uiService.Height()
	if overflow < 0 {
		overflow = 0
	}
	return lines[overflow:]
}

// selectScreenRow 走生产入口拖选屏幕上第 row 行，返回将写入剪贴板的文本。
func selectScreenRow(t *testing.T, m *Model, row int) string {
	t.Helper()
	res, _ := m.Update(tea.MouseMsg{
		X: 0, Y: row, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	mm := res.(Model)
	res2, _ := mm.Update(tea.MouseMsg{
		X: 200, Y: row, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion,
	})
	updated := res2.(Model)
	return updated.mouseSelectionText()
}

func newMouseRowFixture(t *testing.T, height int) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
	mm := res.(Model)
	mm.reassignServices()
	lines := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("R%02d- content line %d", i, i))
	}
	mm.setViewportContent(strings.Join(lines, "\n"))
	mm.uiService.Viewport().GotoTop()
	mm.recalcViewportHeight()
	return &mm
}

// assertScreenRowMatchesSelection 断言：屏幕第 row 行渲染出的文本 == 拖选该行复制到的文本。
func assertScreenRowMatchesSelection(t *testing.T, name string, m *Model, row int) {
	t.Helper()
	// 每行独立起一次拖选：否则上一次的 dragging 状态会污染下一次 press 的路由。
	m.clearMouseSelection()
	screen := screenLines(t, m)
	if row >= len(screen) {
		t.Fatalf("[%s] 屏幕行号 %d 越界（共 %d 行）", name, row, len(screen))
	}
	want := strings.TrimRight(ansi.Strip(screen[row]), " ")
	got := selectScreenRow(t, m, row)
	if got != want {
		t.Errorf("[%s] 屏幕第 %d 行位移：\n  屏幕上看到的 = %q\n  实际复制到的 = %q",
			name, row, want, got)
	}
}

func TestMouseSelectionMatchesRenderedRow_Baseline(t *testing.T) {
	m := newMouseRowFixture(t, 35)
	rows := chatRowsToProbe(t, m)
	for _, row := range rows {
		assertScreenRowMatchesSelection(t, "baseline", m, row)
	}
}

// chatRowsToProbe 给出要抽样的屏幕行号：聊天区首行、次行、中间行、末行。
// 必须基于实测的 chatTop —— toast 叠加时聊天区首行并不在屏幕第 0 行。
func chatRowsToProbe(t *testing.T, m *Model) []int {
	t.Helper()
	lm := m.measureScreenLayout()
	if !lm.valid || lm.chatRows < 2 {
		t.Fatalf("夹具不健全：几何账本不可用（valid=%v chatRows=%d）", lm.valid, lm.chatRows)
	}
	return []int{
		lm.chatTop,
		lm.chatTop + 1,
		lm.chatTop + lm.chatRows/2,
		lm.chatTop + lm.chatRows - 1,
	}
}

func TestMouseSelectionMatchesRenderedRow_WithMonitorStrip(t *testing.T) {
	m := newMouseRowFixture(t, 35)
	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		t.Fatal("夹具不健全：MonitorMgr 为 nil")
	}
	t.Cleanup(mgr.StopAll)
	proc, err := mgr.Start("sleep 30", "mouse row fixture", "")
	if err != nil {
		t.Fatalf("夹具不健全：起监控进程失败 %v", err)
	}
	if !proc.WaitForStatus(monitor.StatusRunning, 10*time.Second) {
		t.Fatal("夹具不健全：进程未进入 running")
	}
	// 配对格：先确认 strip 真的出现（否则本测试会被「恒空」蒙成假绿）。
	if !strings.Contains(m.View(), proc.ID) {
		t.Fatal("夹具不健全：monitor strip 未渲染，本判据不可判别")
	}
	// 真机上 strip 的出现/消失由 monitorEvent 驱动重算（handleMonitorEvent）。
	// 测试里事件是异步的，为确定性显式重算一次同一条预算（同源、不是另一套逻辑）。
	m.recalcViewportHeight()
	if rows := lipgloss.Height(m.View()); rows != m.uiService.Height() {
		t.Errorf("渲染行数 %d ≠ 终端高度 %d ⇒ bubbletea 会砍掉顶部 %d 行，鼠标坐标必然位移",
			rows, m.uiService.Height(), rows-m.uiService.Height())
	}
	for _, row := range chatRowsToProbe(t, m) {
		assertScreenRowMatchesSelection(t, "monitor strip", m, row)
	}
}

// TestMouseSelectionMatchesRenderedRow_WithToast 覆盖 toast 叠加场景。
//
// 这条场景不是凑数：拖选复制成功后**必然**弹 toast（mouse_selection.go
// finishMouseSelection 推 "Copied selection"），而旧 View() 用固定 Sprintf 模板拼接，
// monitorStrip 为空串时仍占一行 ⇒ toast 一出现就多渲染一行 ⇒ 下一次拖选即位移。
// 修法不是"补一个 if"，而是空区块根本不进拼接表（layout.go buildView）。
func TestMouseSelectionMatchesRenderedRow_WithToast(t *testing.T) {
	m := newMouseRowFixture(t, 35)
	if m.notifier == nil {
		t.Fatal("夹具不健全：notifier 为 nil")
	}
	nf := notify.NewNotification("mouse-row-fixture", "Clipboard", "Copied selection / 已复制选中内容")
	nf.Priority = notify.PriorityLow
	nf.TimeoutMs = 2500

	// 生产入口：notifier.Push → waitForPush Cmd 桥接出的 toastPushMsg。
	m.notifier.Push(nf)
	res, _ := m.Update(toastPushMsg{notification: &nf})
	mm := res.(Model)
	if mm.toast == nil || !mm.toast.visible {
		t.Fatal("夹具不健全：toast 未显示，本判据不可判别")
	}
	if !strings.Contains(mm.View(), "已复制选中内容") {
		t.Fatal("夹具不健全：toast 未渲染进 View")
	}

	if rows := lipgloss.Height(mm.View()); rows != mm.uiService.Height() {
		t.Errorf("toast 叠加后渲染行数 %d ≠ 终端高度 %d", rows, mm.uiService.Height())
	}
	for _, row := range chatRowsToProbe(t, &mm) {
		assertScreenRowMatchesSelection(t, "toast", &mm, row)
	}
}

// TestMouseSelectionMatchesRenderedRow_Streaming 覆盖流式输出态（Stop 按钮替换 Send）。
// 状态切换会改变输入区高度，预算与渲染必须一起变。
func TestMouseSelectionMatchesRenderedRow_Streaming(t *testing.T) {
	m := newMouseRowFixture(t, 35)
	m.state = StateStreaming
	m.aiService.SetRequestID("mouse-row-streaming")
	m.recalcViewportHeight()

	if rows := lipgloss.Height(m.View()); rows != m.uiService.Height() {
		t.Errorf("streaming 态渲染行数 %d ≠ 终端高度 %d", rows, m.uiService.Height())
	}
	for _, row := range chatRowsToProbe(t, m) {
		assertScreenRowMatchesSelection(t, "streaming", m, row)
	}
}

// TestMouseSelectionMatchesRenderedRow_WithQueuedPreview 覆盖排队输入预览。
//
// 这一格是**诊断用**：输入区高度在高频态走 estimateInputAreaHeight 估算，
// 其中 preview 行数用 wrapTextSimple 估算（app.go estimatePreviewLines）。
// 估算若与真实渲染差 N 行，View() 就超行 N 行 ⇒ 同样的位移。
// 坐标已改实测，所以位移不会出现；但超行仍会让顶部内容被 bubbletea 砍掉，
// 因此这里仍断言「渲染行数 == 终端高度」，红了就说明估算还有差。
func TestMouseSelectionMatchesRenderedRow_WithQueuedPreview(t *testing.T) {
	m := newMouseRowFixture(t, 35)
	long := strings.Repeat("排队输入预览长文本换行", 12)
	if !m.EnqueueInput(QueuedUserMessage{Text: long}) {
		t.Fatal("夹具不健全：入队失败")
	}
	m.EnqueueInput(QueuedUserMessage{Text: "第二条排队输入"})
	m.recalcViewportHeight()

	if !strings.Contains(m.View(), "第二条排队输入") {
		t.Fatal("夹具不健全：预览未渲染，本判据不可判别")
	}
	if rows := lipgloss.Height(m.View()); rows != m.uiService.Height() {
		t.Errorf("排队预览态渲染行数 %d ≠ 终端高度 %d（preview 行数估算与真实渲染不一致）",
			rows, m.uiService.Height())
	}
	for _, row := range chatRowsToProbe(t, m) {
		assertScreenRowMatchesSelection(t, "queued preview", m, row)
	}
}

// TestMonitorEventRebalancesViewportBudget 判别「Monitor 事件会重算高度预算」。
// 删掉 handleMonitorEvent 里的 recalcViewportHeight ⇒ 本测试转红（strip 出现后
// View() 超行，而预算无人纠正）。
//
// ★ 期望口径（2026-10-14 Linux 真机分诊修正）：预算的权威公式是
// recalcViewportHeight 的 `Height - sb - ia - toast - monitor`。Monitor 启动
// 不只让 strip 出现——状态栏的 Monitor 角标（A-4，monitorBadge 置顶插入）也会
// 随进程出现，packStatusFields 装箱可能让它**多占一行**（是否多占取决于总宽度
// ＝ 字段宽 + workspace 路径长 + emoji 宽度口径，逐机不同）。故期望增量必须
// 把状态栏高度差（sbDelta）一并计入，否则在「角标挤进新行」的机器上恒差 1
// （Linux VM 实测：before=23 sb 4→5 strip=2 ⇒ after=20，旧期望 21 恒红）。
// 计入 sbDelta 后判别力不变：删掉 recalc ⇒ after 停在 before ⇒ 仍转红。
func TestMonitorEventRebalancesViewportBudget(t *testing.T) {
	m := newMouseRowFixture(t, 35)
	before := m.uiService.Viewport().Height
	sbBefore := lipgloss.Height(m.renderStatusBar())

	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		t.Fatal("夹具不健全：MonitorMgr 为 nil")
	}
	t.Cleanup(mgr.StopAll)
	proc, err := mgr.Start("sleep 30", "budget fixture", "")
	if err != nil {
		t.Fatalf("夹具不健全：起监控进程失败 %v", err)
	}
	if !proc.WaitForStatus(monitor.StatusRunning, 10*time.Second) {
		t.Fatal("夹具不健全：进程未进入 running")
	}
	stripRows := lipgloss.Height(m.renderMonitorStrip())
	if stripRows == 0 {
		t.Fatal("夹具不健全：monitor strip 为空，本判据不可判别")
	}
	sbAfter := lipgloss.Height(m.renderStatusBar())

	// 生产入口：真的把一个 monitor 事件喂进 handleMonitorEvent。
	m.handleMonitorEvent(monitorEvent{eventType: "output", monitorID: proc.ID})
	after := m.uiService.Viewport().Height
	expect := before - stripRows - (sbAfter - sbBefore)
	if after != expect {
		t.Fatalf("Monitor 摘要区＋角标出现后 viewport 应让出 %d 行（strip %d + 状态栏增量 %d；%d→%d），实际 %d",
			before-expect, stripRows, sbAfter-sbBefore, before, expect, after)
	}
	if rows := lipgloss.Height(m.View()); rows != m.uiService.Height() {
		t.Fatalf("重算后渲染行数 %d 仍 ≠ 终端高度 %d", rows, m.uiService.Height())
	}
}
