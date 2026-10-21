package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// n137TailLines 只用于失败信息：截最后 n 行，避免把整屏刷进日志。
func n137TailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// 守卫：**非底部时到达的内容必须留下可见痕迹**。
//
// 缺陷形态（2026-09-23 实测）：`addMessage` 对「用户主动触发的回执」与「被动到达的流式输出」
// 共用同一条 followTail 策略 —— 非底部时保持原滚动位置**且零提示**。于是用户上滚读历史时敲
// `/tool 99`，输出被追加到屏幕之外，界面毫无变化（实测该帧只发出 1255 字节 = 输入框被清空，
// 输出行命中 0）。用户据此判断"命令没生效 / 历史丢了"。
//
// 本用例从**生产入口**进入（`Update` + Enter → `sendInput` → `handleCommand` → `addMessage`），
// 不断言内部结构，只断言两件用户可见事实：
// - ① 阅读位置**不被回跳**（不打断读历史）；
// - ② 状态栏出现**可见的未读角标**（不再静默）。
//
// 判别性自检（before 必须红）：把 `addMessage` 的 `m.unreadBelow++` 与
// `renderStatusBar` 的角标注入同时退回（见回执 §A/B），本条在 ⑥⑦ 两处必须转红。
func TestN137_NonBottomCommandReceipt_ShowsUnreadBadge(t *testing.T) {
	m := newTUIRegressionModel(t)

	// 1) 走生产入口确定终端尺寸（同时把 Ready 置位，视口高度才有值）
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cur := updated.(Model)
	cur.reassignServices()

	// 2) 灌入足够多历史，使视口真的可滚动
	for i := 0; i < 80; i++ {
		cur.addMessage(fmt.Sprintf("history line %d", i))
	}
	cur.reassignServices()
	vp := cur.uiService.Viewport()
	vp.GotoBottom()
	if !vp.AtBottom() {
		t.Fatalf("前置不成立：应可滚动且当前在底部（total=%d height=%d）", vp.TotalLineCount(), vp.Height)
	}

	// 3) 模拟"用户正在读历史"：上滚离开底部
	vp.HalfViewUp()
	if vp.AtBottom() {
		t.Fatal("前置不成立：上滚后应已离开底部")
	}
	offsetBefore := vp.YOffset

	// 4) 用户主动提交一条 slash 命令（生产入口：Enter → sendInput → handleCommand → addMessage）
	cur.uiService.Textarea().Focus()
	cur.uiService.Textarea().SetValue("/tool 99")
	updated2, _ := cur.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := updated2.(Model)
	after.reassignServices()

	// 前提自检：命令确实生效了（消息数增加），否则后面的断言无意义
	if len(after.sessionService.Messages()) <= 80 {
		t.Fatalf("前置不成立：提交 /tool 99 后消息数未增加（%d）—— 生产入口没走到 addMessage",
			len(after.sessionService.Messages()))
	}

	// 5) 用户可见事实①：阅读位置不被回跳
	if got := after.uiService.Viewport().YOffset; got != offsetBefore {
		t.Errorf(": 非底部提交不得回跳（不打断阅读），YOffset 应保持 %d，got %d", offsetBefore, got)
	}

	// 6) 用户可见事实②（结构侧）：未读被点亮
	if after.unreadBelow == 0 {
		t.Error(": 非底部提交后未读未点亮（unreadBelow=0）—— 这正是『敲了命令像没反应』的回归")
	}

	// 7) 用户可见事实②（渲染侧）：状态栏真的出现了角标文案
	view := ansi.Strip(after.View())
	if !strings.Contains(view, "Unread:") {
		t.Errorf(": 状态栏应出现未读角标，view 尾部:\n%s", n137TailLines(view, 8))
	}

	// 8) 用例前提本身要成立：新到达的输出**确实落在屏幕外**（否则"看不见"不是本次现象）
	msgs := after.sessionService.Messages()
	newest := strings.TrimSpace(ansi.Strip(msgs[len(msgs)-1]))
	if newest != "" && strings.Contains(view, newest) {
		t.Errorf(": 用例前提不成立 —— 最新输出其实在可视窗口内，无需角标；新输出=%q", newest)
	}

	// 9) 回到底部：角标**当帧**消失，且未读计数清零
	after.uiService.Viewport().GotoBottom()
	if v := ansi.Strip(after.View()); strings.Contains(v, "Unread:") {
		t.Errorf(": 回到底部后角标应消失，view 尾部:\n%s", n137TailLines(v, 8))
	}
	updated3, _ := after.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	final := updated3.(Model)
	if final.unreadBelow != 0 {
		t.Errorf(": 回到底部后未读计数应清零，got %d", final.unreadBelow)
	}
}

// TestN137_AtBottomAddMessage_NoBadge 反向半边：**在底部**追加内容不得点亮角标
// （防止修复越界 —— 把"跟随底部"的正常阅读也变成噪声）。
//
// ★ 本条**不具判别性**（before / after 两态皆绿），按 `production-path-tdd` R4-7 的口径明示：
// before 态里 `unreadBelow` 永远是 0，因此"无角标"对它同样成立。
// 它的作用只是**防越界**（把正常情况也点亮），**不得**当判别器读。
func TestN137_AtBottomAddMessage_NoBadge(t *testing.T) {
	m := newTUIRegressionModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cur := updated.(Model)
	cur.reassignServices()

	for i := 0; i < 80; i++ {
		cur.addMessage(fmt.Sprintf("history line %d", i))
	}
	cur.reassignServices()
	cur.uiService.Viewport().GotoBottom()

	cur.addMessage("at-bottom receipt")
	if cur.unreadBelow != 0 {
		t.Errorf(": 在底部追加内容不得点亮角标，got %d", cur.unreadBelow)
	}
	if v := ansi.Strip(cur.View()); strings.Contains(v, "Unread:") {
		t.Errorf(": 在底部不应出现角标，view 尾部:\n%s", n137TailLines(v, 8))
	}
}
