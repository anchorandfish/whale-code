package tui

// tui_turn_boundary_test.go — 轮末边界「持久化」批次。
//
// 背景（用户可见现象）：长会话回看时**每轮没有稳定的分界**。A-3把轮末标记
// 从「一条消息」改成「渲染侧对 m.turn 的投影」后，副作用是它**只描述最后一轮**：
// - 全会话最多存在 1 条标记；
// - 一旦新一轮开始（turnWriteOpen 为真），它立刻消失。
// ⇒ 历史里任何一轮都没有锚点，"下回找"只能靠肉眼。
//
// 本批目标（只做"每轮留一条持久边界"，不含导航/搜索）：每结束一轮就往显示侧固化一条
// 边界，位置落在**该轮内容之后、下一轮之前**，且**永不进模型侧**（不变式 I5）。
//
// 判据各盯一维（判别性证明见回执的注入表）：
// ① EachTurnLeavesPersistentMark —— 存在性（每轮各一条）
// ② MarkSitsAtTurnBoundary —— 位置（不是全堆末尾）
// ③ PastMarkSurvivesNextTurn —— 持久性（新一轮不清掉旧锚点）
// ④ NotPersistedAsMessageOrModelContent —— 不泄漏（A-3 契约 + I5）
// ⑤ SurvivesLongSession —— 长会话可见性
// ⑥ InterruptedTurnLeavesMark —— 中断轮也留标志（2026-10-07 新增）
//
// before 自检：①②③⑤ 在改动前必须红；④ 在改动前**绿**（它是防越界守卫，不是判别器）。
//
// ★ 2026-10-07（用户拍板）用户可见文案再变：结束标志由「两行 + 轮次号 + 中英文各一遍」
// （`━━ ✓ Turn #N complete · HH:MM:SS ━━` ＋ ` 第 N 轮回答已结束`）收敛为**单行**
// `━━ ✓ <YYYY-MM-DD HH:MM:SS> ━━`（正常）/ `━━ ⏹ <…> ━━`（中断），且**中断轮也留标志**。
// 结构面（每轮一条、插回原位、不进 session）不变，只换字面量 ⇒ 本文件断言同改（R10）。
//
// ★ 判别力要点：只断言 `contains "━━ ✓ "` 对**旧格式也成立**（旧文案含该前缀），
// 故这里用**完整正则**（钉死单行形态与完整年月日）＋**反向断言**（旧的两行啰嗦形态必须消失）双管。

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/session"
)

// turnDoneMarkPattern / turnInterruptedMarkPattern 是**新契约**结束标志的完整形态：
// 单行、符号 + 完整年月日时刻、无轮次号、无第二行。旧格式（含 `Turn #N complete`）不匹配。
var (
	turnDoneMarkPattern        = regexp.MustCompile(`━━ ✓ \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} ━━`)
	turnInterruptedMarkPattern = regexp.MustCompile(`━━ ⏹ \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} ━━`)
)

// assertNoVerboseMarker 反向断言：旧的啰嗦形态（轮次号 + 中英文各一遍）必须已消失。
func assertNoVerboseMarker(t *testing.T, rendered string) {
	t.Helper()
	if strings.Contains(rendered, "Turn #") || strings.Contains(rendered, "轮回答已结束") {
		t.Fatalf("结束标志应已收敛为单行（去掉轮次号与中英文重复），旧形态仍在：\n%s", rendered)
	}
}

// runOneTurn 在生产入口上跑完一整轮：置轮内状态 → 追加用户回显 → 交给 handleStreamDone 收尾。
//
// 用 handleStreamDone 而不是直接调 closeTurn：只有走完真实收尾链，才能保证
// 「closeTurn 被调用时本轮消息已全部进入显示缓冲」这个前提在生产上成立。
func runOneTurn(t *testing.T, m *Model, question, answer string) {
	t.Helper()
	m.sessionService.SetState(StateStreaming)
	m.addMessage(formatUserMessage(question, 0))
	_ = m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: answer}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: "turn-boundary-" + question,
		thinking:  "",
	})
}

// ① 存在性：每轮各留一条结束标志，且形态为新契约的单行完整时刻。
func TestTurnBoundary_EachTurnLeavesPersistentMark(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	runOneTurn(t, m, "第一轮的提问", "第一轮的回答")
	runOneTurn(t, m, "第二轮的提问", "第二轮的回答")

	rendered := ansi.Strip(m.viewportContent)
	if got := len(turnDoneMarkPattern.FindAllString(rendered, -1)); got != 2 {
		t.Fatalf("每轮应各留一条结束标志（共 2 条，形态 `━━ ✓ <YYYY-MM-DD HH:MM:SS> ━━`），实际 %d 条：\n%s", got, rendered)
	}
	assertNoVerboseMarker(t, rendered)
}

// ② 位置：第1轮边界必须排在该轮内容之后、下一轮提问之前（⇒ 不是全堆末尾）。
func TestTurnBoundary_MarkSitsAtTurnBoundary(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	runOneTurn(t, m, "第一轮的提问", "第一轮的回答")
	runOneTurn(t, m, "第二轮的提问", "第二轮的回答")

	rendered := ansi.Strip(m.viewportContent)
	locs := turnDoneMarkPattern.FindAllStringIndex(rendered, -1)
	iA1 := strings.Index(rendered, "第一轮的回答")
	iQ2 := strings.Index(rendered, "第二轮的提问")
	iA2 := strings.Index(rendered, "第二轮的回答")
	// 夹具健全性：缺一，位置断言就没有对象。
	if len(locs) != 2 || iA1 < 0 || iQ2 < 0 || iA2 < 0 {
		t.Fatalf("夹具不健全（marks=%d iA1=%d iQ2=%d iA2=%d），位置断言不可判：\n%s",
			len(locs), iA1, iQ2, iA2, rendered)
	}
	iMark1, iMark2 := locs[0][0], locs[1][0]
	if !(iA1 < iMark1) {
		t.Fatalf("第1轮边界应排在该轮内容**之后**（iA1=%d iMark1=%d）：\n%s", iA1, iMark1, rendered)
	}
	if !(iMark1 < iQ2) {
		t.Fatalf("第1轮边界应排在**下一轮提问之前** —— 否则边界只是全堆在末尾，回看照样定不了位（iMark1=%d iQ2=%d）：\n%s",
			iMark1, iQ2, rendered)
	}
	if !(iA2 < iMark2) {
		t.Fatalf("第二条边界应排在第二轮回答之后，不得两条都怼在末尾（iA2=%d iMark2=%d）：\n%s", iA2, iMark2, rendered)
	}
}

// ③ 持久性：历史锚点不因新一轮开始而消失 —— A-3 副作用的直接反面。
func TestTurnBoundary_PastMarkSurvivesNextTurn(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	runOneTurn(t, m, "第一轮的提问", "第一轮的回答")

	// 开始第二轮：只置轮内状态 + 追加用户回显，尚未收尾。
	m.sessionService.SetState(StateStreaming)
	m.addMessage(formatUserMessage("第二轮的提问", 0))

	rendered := ansi.Strip(m.viewportContent)
	if got := len(turnDoneMarkPattern.FindAllString(rendered, -1)); got != 1 {
		t.Fatalf("第一轮的边界不应因新一轮开始而消失、第二轮也不应提前出现边界（期望 1 条），实际 %d 条：\n%s", got, rendered)
	}
}

// ④ 不泄漏：边界是显示侧结构，**不得**占消息身份、更不得进模型侧（不变式 I5）。
func TestTurnBoundary_NotPersistedAsMessageOrModelContent(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	runOneTurn(t, m, "第一轮的提问", "第一轮的回答")
	runOneTurn(t, m, "第二轮的提问", "第二轮的回答")

	for i, msg := range m.sessionService.Messages() {
		if strings.Contains(ansi.Strip(msg), "━━") {
			t.Fatalf("轮末边界不得占显示消息身份（第 %d 条）：%q", i, msg)
		}
	}
	for i, smsg := range m.sessionService.CurrentSession().GetMessages() {
		for _, b := range smsg.Blocks {
			if strings.Contains(b.Text, "━━") {
				t.Fatalf("I5 违例：轮末边界泄漏进模型侧 session（第 %d 条消息）", i)
			}
		}
	}
}

// ⑤ 生产长会话路径：用户报障的是**长会话回看**（显示消息 >50 条）。前四条用短会话，
// 覆盖不到长会话窗口。这里先把缓冲填到越过 streamRenderThreshold，再跑**一轮真实回合**，
// 分两处断言：全量转录里边界排在回答之后（位置），且滚到底的可见窗口里能看到它（用户可见）。
func TestTurnBoundary_SurvivesLongSession(t *testing.T) {
	m := newTurnLayoutModel(t, 116)
	res, _ := m.Update(tea.WindowSizeMsg{Width: 116, Height: 20})
	mm := res.(Model)
	mm.reassignServices()
	m = &mm

	// 填充历史越过阈值（不触发循环守卫，保证用例确定性、快）。
	for i := 0; i < streamRenderThreshold; i++ {
		m.sessionService.AddMessage(fmt.Sprintf("历史填充消息 %d", i))
	}
	runOneTurn(t, m, "长会话提问", "长会话回答")

	if len(m.sessionService.Messages()) <= streamRenderThreshold {
		t.Fatalf("夹具未越过长会话阈值：msgs=%d threshold=%d",
			len(m.sessionService.Messages()), streamRenderThreshold)
	}

	// 位置：全量转录里，本轮边界必须排在本轮回答之后。
	full := ansi.Strip(m.renderMessages())
	iA := strings.Index(full, "长会话回答")
	loc := turnDoneMarkPattern.FindStringIndex(full)
	if !(iA >= 0 && loc != nil && loc[0] > iA) {
		t.Fatalf("长会话转录里边界未排在本轮回答之后（iA=%d loc=%v）", iA, loc)
	}

	// 用户可见：滚到底的窗口里看得到这条边界。
	m.setViewportContent(m.wrapViewportContent(m.renderMessages()))
	m.uiService.Viewport().GotoBottom()
	view := ansi.Strip(m.uiService.Viewport().View())
	if !turnDoneMarkPattern.MatchString(view) {
		t.Fatalf("长会话滚到底仍看不到本轮边界（回看无锚点）：\n%s", view)
	}
}

// ⑥ 中断轮也必须留结束标志（2026-10-07 用户反馈：突然中断就"没有结束标志"）。
//
// 走生产中断入口 stopExecution（Stop / Ctrl+C 的共同终点），断言**结果面**：
// 渲染转录里出现中断标志（⏹ + 完整年月日时刻），且原先独立的
// `Stopped │ <日期> │ interrupted` 行已被合并掉（每轮只有一个结束标志）。
func TestTurnBoundary_InterruptedTurnLeavesMark(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.aiService.SetRequestID("interrupt-mark-req")
	m.activeCell.AppendText("部分输出")

	res, _ := m.stopExecution()
	mm := res.(Model)
	mm.reassignServices()

	display := ansi.Strip(mm.renderMessages())
	if !turnInterruptedMarkPattern.MatchString(display) {
		t.Fatalf("中断轮必须留结束标志（━━ ⏹ + 完整时刻）：\n%s", display)
	}
	if got := len(turnInterruptedMarkPattern.FindAllString(display, -1)); got != 1 {
		t.Fatalf("中断轮应恰好一条结束标志，实际 %d 条：\n%s", got, display)
	}
	// 用户可见面：closeTurn 会主动刷新视口，标志必须真的出现在 viewportContent 里
	// （只断言 renderMessages 会漏掉"状态有、界面没刷新"这一类假绿）。
	if !turnInterruptedMarkPattern.MatchString(ansi.Strip(mm.viewportContent)) {
		t.Fatalf("中断标志未刷新进视口（用户看不到）：\n%s", ansi.Strip(mm.viewportContent))
	}
	if strings.Contains(display, "Stopped") || strings.Contains(display, "interrupted") {
		t.Fatalf("中断行应已合并进结束标志，不得再有独立的 Stopped/interrupted 行：\n%s", display)
	}
}
