package tui

// button_press_feedback_test.go — 档B：按钮「按下高亮 + 松开开火」。
//
// 用户报障：「鼠标移到 copy all 和 send/stop 的按键那里没有变成箭头，
// 这样不好用户知道自己有没有选中」。档A（悬停高亮 + OSC 22 指针）另见
// button_hover_test.go；本文件是**零成本档**：现行 1002 鼠标模式下就够用。
//
// 语义（对齐 GUI 标准按钮，也与档A 的悬停态共用同一套 active 样式）：
// - 按下（mousedown）：登记按下态并**立即**以 active 样式高亮，**不开火**；
// - 松开（mouseup）：才开火（copy/send/stop 各自的生产动作），并清零按下态。
// 为什么不开火放在松开：旧实现「按下即开火」对 send 而言按下态永远渲染不到
// （同一帧 state 就翻成 Streaming、按钮换成 Stop）⇒ 用户要的「点下去有反馈」
// 在 send 上结构性地不可见。按下即高亮、松开才动作，三个按钮就都有反馈。
//
// 生产入口：NewModel → WindowSizeMsg → Model.Update(tea.MouseMsg) → View()。
// 判据面：模型态（pressedBtn 登记/清零）+ 渲染面（按钮行 ANSI 差异）。
// 渲染面必须强制颜色档：go test 的 stdout 是管道 ⇒ lipgloss 退化成 Ascii ⇒
// 所有样式被剥掉，「高亮与否」在字节面上不可判别（假绿）。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// forceANSIColors 让 lipgloss 在测试里输出真 ANSI（含粗体/颜色序列）。
// 用 cleanup 还原，避免污染同包其他测试的渲染期望。
func forceANSIColors(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// actionRowY 复刻 app.go Update 按钮行命中几何的同一公式（不新造第二套坐标；
// 与 stop_interaction_test.go / tui_regression_test.go 的既有点击夹具同源）。
func actionRowY(m *Model) int {
	return m.lastInputAreaTop + m.lastPreviewHeight + m.uiService.Textarea().Height() + inputBoxStyle.GetVerticalFrameSize()
}

func actionRowHeight(m *Model) int {
	return lipgloss.Height(renderInputActionRow(m.uiService.Width()-4,
		m.sessionService.State() != StateIdle, false, ""))
}

// viewLineWith 取 View() 中含 needle 的**原文行**（保留 ANSI）—— 按下高亮的判别面。
func viewLineWith(t *testing.T, m *Model, needle string) string {
	t.Helper()
	for _, ln := range strings.Split(m.View(), "\n") {
		if strings.Contains(ansi.Strip(ln), needle) {
			return ln
		}
	}
	t.Fatalf("View() 中找不到含 %q 的行", needle)
	return ""
}

func pressMsg(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Type: tea.MouseLeft, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func releaseMsg(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease}
}

// hasBold 检测行内是否存在粗体 SGR。termenv 会把 Bold 与颜色合并成单条序列
// （实测 `\x1b[1;38;5;117m`），孤立匹配 `\x1b[1m` 会漏判 ⇒ 两种形态都认。
// 这是「active 样式是否生效」的判别面：normal 按钮样式没有任何粗体。
func hasBold(s string) bool {
	return strings.Contains(s, "\x1b[1m") || strings.Contains(s, "\x1b[1;")
}

// TestButtonPressHighlightsWithoutFiring 是档B 的主判别器：
// 按下**只**进入按下态（高亮、不开火），松开才开火。
func TestButtonPressHighlightsWithoutFiring(t *testing.T) {
	forceANSIColors(t)
	m := newTUIRegressionModel(t)
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	updated := updatedModel.(Model)
	updated.reassignServices()
	updated.recalcViewportHeight()

	rowY := actionRowY(&updated)
	copyWidth := lipgloss.Width(renderCopyAllButton(updated.uiService.Width()-4, false))
	if copyWidth == 0 {
		t.Fatal("夹具不健全：100 列终端应显示 Copy all 按钮")
	}
	cx, cy := copyWidth/2, rowY+1

	before := viewLineWith(t, &updated, "Copy all")
	if hasBold(before) {
		t.Fatal("夹具不健全：未按下时按钮行不应含粗体（active 样式）")
	}

	// 生产入口：在 Copy all 上按下左键。
	pressRes, pressCmd := updated.Update(pressMsg(cx, cy))
	pressed := pressRes.(Model)
	if pressed.pressedBtn != "copy" {
		t.Fatalf("按下 Copy all 应登记按下态，got %q", pressed.pressedBtn)
	}
	if pressCmd != nil {
		t.Fatal("按下不应立即开火（GUI 按钮语义：松开才开火）")
	}

	after := viewLineWith(t, &pressed, "Copy all")
	if after == before {
		t.Fatal("按下后按钮行渲染应变化（高亮），实际与按下前逐字节相同")
	}
	if !hasBold(after) {
		t.Fatalf("按下后按钮行应含 active 样式（粗体序列），got %q", after)
	}
}

// TestButtonReleaseFiresCopyAll 覆盖 Copy all 的完整点击：按下高亮 → 松开开火。
func TestButtonReleaseFiresCopyAll(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText("user prompt"),
		session.AssistantText("assistant reply", "test-model", nil),
	})
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	updated := updatedModel.(Model)
	updated.reassignServices()
	updated.recalcViewportHeight()

	rowY := actionRowY(&updated)
	copyWidth := lipgloss.Width(renderCopyAllButton(updated.uiService.Width()-4, false))
	if copyWidth == 0 {
		t.Fatal("夹具不健全：100 列终端应显示 Copy all 按钮")
	}
	cx, cy := copyWidth/2, rowY+1
	beforeNotifications := len(updated.notifier.List())

	pressRes, pressCmd := updated.Update(pressMsg(cx, cy))
	if pressCmd != nil {
		t.Fatal("按下阶段不应开火")
	}
	pressed := pressRes.(Model)
	if pressed.pressedBtn != "copy" {
		t.Fatalf("按下态未登记：%q", pressed.pressedBtn)
	}

	releaseRes, releaseCmd := pressed.Update(releaseMsg(cx, cy))
	released := releaseRes.(Model)
	if releaseCmd == nil {
		t.Fatal("松开 Copy all 应返回剪贴板写入命令")
	}
	if released.pressedBtn != "" {
		t.Fatalf("松开后按下态应清零，got %q", released.pressedBtn)
	}
	if released.state != StateIdle {
		t.Fatal("Copy all 不应改变会话状态")
	}
	if len(released.notifier.List()) != beforeNotifications+1 {
		t.Fatal("Copy all 应推送 toast 反馈")
	}
}

// TestButtonPressReleaseFiresSend 覆盖 Send：按下可见高亮（state 仍 Idle），
// 松开才启动回合 —— 这正是旧「按下即开火」结构上做不到的那一格。
func TestButtonPressReleaseFiresSend(t *testing.T) {
	m := newTUIRegressionModel(t)
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	updated := updatedModel.(Model)
	updated.reassignServices()
	updated.recalcViewportHeight()
	updated.textarea.SetValue("hello from mouse")
	updated.recalcViewportHeight()

	rowY := actionRowY(&updated)
	rowH := actionRowHeight(&updated)
	btnView := renderActionButton(updated.uiService.Width()-4, false, false, "")
	btnW := lipgloss.Width(btnView)
	btnX := updated.uiService.Width() - 4 - btnW
	sx, sy := btnX+btnW/2, rowY+rowH/2

	pressRes, pressCmd := updated.Update(pressMsg(sx, sy))
	pressed := pressRes.(Model)
	if pressed.pressedBtn != "send" {
		t.Fatalf("按下 Send 应登记按下态，got %q", pressed.pressedBtn)
	}
	if pressCmd != nil {
		t.Fatal("按下 Send 不应立即开火")
	}
	if pressed.state != StateIdle {
		t.Fatal("按下 Send 不应立即改变状态（按下高亮可见的前提）")
	}

	releaseRes, releaseCmd := pressed.Update(releaseMsg(sx, sy))
	sent := releaseRes.(Model)
	if sent.state != StateStreaming {
		t.Fatalf("松开 Send 应启动回合，state=%d", sent.state)
	}
	if releaseCmd == nil {
		t.Fatal("松开 Send 应返回流式命令")
	}
	if sent.pressedBtn != "" {
		t.Fatalf("松开后按下态应清零，got %q", sent.pressedBtn)
	}
}

// TestStuckButtonPressSelfHealsOnKey 覆盖终端吞掉 release 的兜底：
// 指针移出终端窗口外松开时不会收到 release，任何键盘活动都应取消视觉按下态。
func TestStuckButtonPressSelfHealsOnKey(t *testing.T) {
	forceANSIColors(t)
	m := newTUIRegressionModel(t)
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	updated := updatedModel.(Model)
	updated.reassignServices()
	updated.recalcViewportHeight()

	rowY := actionRowY(&updated)
	copyWidth := lipgloss.Width(renderCopyAllButton(updated.uiService.Width()-4, false))
	if copyWidth == 0 {
		t.Fatal("夹具不健全：100 列终端应显示 Copy all 按钮")
	}

	pressRes, _ := updated.Update(pressMsg(copyWidth/2, rowY+1))
	pressed := pressRes.(Model)
	if pressed.pressedBtn != "copy" {
		t.Fatalf("按下态未登记：%q", pressed.pressedBtn)
	}

	keyRes, _ := pressed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	healed := keyRes.(Model)
	if healed.pressedBtn != "" {
		t.Fatalf("键盘活动后按下态应自愈清零，got %q", healed.pressedBtn)
	}
	if hasBold(viewLineWith(t, &healed, "Copy all")) {
		t.Fatal("自愈后按钮行不应再高亮")
	}
}

// TestReleaseWithoutPressDoesNotFire 覆盖无登记按下的 release（如拖选后的松开
// 已被选择状态机消费之外的裸 release）：不得开火任何按钮动作。
func TestReleaseWithoutPressDoesNotFire(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		session.UserText("user prompt"),
		session.AssistantText("assistant reply", "test-model", nil),
	})
	updatedModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	updated := updatedModel.(Model)
	updated.reassignServices()
	updated.recalcViewportHeight()
	beforeNotifications := len(updated.notifier.List())

	res, cmd := updated.Update(releaseMsg(5, 5))
	got := res.(Model)
	if cmd != nil {
		t.Fatal("无登记按下的 release 不应开火")
	}
	if len(got.notifier.List()) != beforeNotifications {
		t.Fatal("无登记按下的 release 不应产生 toast")
	}
	if got.state != StateIdle {
		t.Fatal("无登记按下的 release 不应改变会话状态")
	}
}
