package tui

// transcript_scroll_test.go — 判据。
//
// # 真机报障
//
// 2026-10-06 T-5 手测（会话 01a11062-…）：显示消息数跨过 50 条后，TUI 历史
// **跳回很旧的一屏且切不回最新**（键盘、滚轮都回不来）。
//
// # 根因
//
// renderMessages 在 len(msgs) > streamRenderThreshold(50) 时走"虚拟滚动"路径，
// 只把**可见窗口**喂给 viewport（RenderVisible(YOffset)），且 wrapViewportContent
// 不补空行 ⇒ TotalLineCount ≈ Height ⇒ maxOffset = 0 ⇒ AtBottom() 恒真、YOffset 恒 0
// ⇒ 永远渲染从第 0 条起的最旧窗口，且没有内容可下滑到最新。
//
// 该分支另有两处只在 >50 条时现形的错：① 对**已渲染**串再跑一遍 glamour（双重渲染）；
// ② 丢掉消息间的分隔线（全量路径有）。
//
// # 判据形态（断言**结果**，不是"执行了"）
//
// 1. 超阈值时 renderMessages() 的产出必须包含**全部**消息（首条 + 末条都能找到）；
// 2. 把产出喂给 viewport 后，总行数 > 视口高度（真的可滚动）；
// 3. GotoBottom() 后视口的**可见窗口**包含末条消息（用户能回到最新）。
//
// 修前：① 末条不可见（只渲染了最旧一屏）→ 红；② 分隔线为零 → 红；③ TotalLineCount==Height → 红。
//
// 夹具从生产入口进入：NewModel → WindowSizeMsg（!Ready 分支）→ sessionService.AddMessage
// → 生产渲染链 renderMessages/wrapViewportContent/setViewportContent → 断言落在
// viewport 的 TotalLineCount / View()（用户可见量）上。

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestTranscriptScroll_OverThreshold_KeepsFullScrollableContent 覆盖 ：
// 超阈值后转录仍是全量、可滚动、能跳到最新。
func TestTranscriptScroll_OverThreshold_KeepsFullScrollableContent(t *testing.T) {
	m, _ := guModelIsolatedHome(t)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	mm := res.(Model)
	mm.reassignServices()

	// 稳定越过阈值，且总行数远超视口高度。
	const n = streamRenderThreshold + 12 // 62
	for i := 0; i < n; i++ {
		mm.sessionService.AddMessage(fmt.Sprintf("R%d transcript line", i))
	}

	content := mm.wrapViewportContent(mm.renderMessages())
	mm.setViewportContent(content)
	mm.uiService.Viewport().GotoBottom()

	plain := ansi.Strip(content)
	last := fmt.Sprintf("R%d transcript line", n-1)

	// ① 全量内容：首条与末条都在产出里。
	if !strings.Contains(plain, "R0 transcript line") {
		t.Fatalf("超阈值转录丢了首条消息（只渲染了可见窗口）")
	}
	if !strings.Contains(plain, last) {
		t.Fatalf("超阈值转录丢了末条消息 %q：视口无法到达最新内容", last)
	}
	// ② 分隔线未被丢弃（虚拟滚动路径不拼分隔符，全量路径拼）。
	if !strings.Contains(plain, "─") {
		t.Fatalf("超阈值转录丢了消息间分隔线")
	}
	// ③ 真的可滚动：内容行数必须超过视口高度，否则 offset 被钉死。
	vp := mm.uiService.Viewport()
	if vp.TotalLineCount() <= vp.Height {
		t.Fatalf("超阈值转录不可滚动：TotalLineCount=%d Height=%d（maxOffset 被钉死在 0）",
			vp.TotalLineCount(), vp.Height)
	}
	// ④ 跳到最新后，可见窗口里能看见末条消息。
	bottomView := ansi.Strip(vp.View())
	if !strings.Contains(bottomView, last) {
		t.Fatalf("GotoBottom 后可见窗口仍看不到最新消息 %q；可见窗口=%q", last, bottomView)
	}
}
