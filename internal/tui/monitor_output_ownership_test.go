package tui

// monitor_output_ownership_test.go — 「后台进程输出归属」批次红测（A-1/A-2/A-3/A-5，2026-09-30）。
//
// 起因（真机手测 I-7.1）：Monitor 的输出被逐行 addMessage 直推进主对话流 ⇒
// 1 行/秒刷屏、`Turn complete` 之后仍在写、用户无法停止。
//
// 生产入口（**不许**直调 handleMonitorEvent 造绿 —— 那是被测函数本身，直调等于绕过
// 「事件怎么到达主循环」这一层，见 production-path-tdd R2）：
//
//	NewModel（lifecycle.go，生产唯一装配点：建 monitorCh / monitorEscape，
// 并注册 SetOutputCallback / SetDoneCallback）
// → monitorCh → waitForMonitorEvent（app.go，Init 里排的同一个 tea.Cmd）
// → Model.Update（app.go，tea 主循环入口；streamDoneMsg 与 monitorEvent 都由它分派）
//
// 「回合结束」也走生产入口：把 streamDoneMsg 交给 Update，由
// handleStreamDone → finishTurn(true) → closeTurn 落地边界。
//
// before 自检读数（改动前 = git 808ef83 基线，见回执 内部工作回执）：
// - TestMonitorOwnership_NoStreamWriteAfterTurnEnd：**红**（output/done 都无条件 addMessage）
// - TestMonitorDone_AuthorBoundaryFork：**红**（after-turn 臂写进了主对话流）
// - TestMonitorDropCounters_ExactAndDoneNotLost：**编译失败**（计数是新符号；语义判别见其内注释）
// - TestTurnBoundary_StructuralNotAMessage：**红**（标记是消息 ⇒ 进 Messages()）

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/session"
)

// feedMonitorEventThroughProduction 把一条 Monitor 事件走**生产入口**喂进主循环：
// 真实 channel → waitForMonitorEvent（NewModel 在 Init 里排的同一个 Cmd）→ Update。
func feedMonitorEventThroughProduction(t *testing.T, m *Model, ev monitorEvent) *Model {
	t.Helper()
	ch := m.toolService.MonitorCh()
	if ch == nil {
		t.Fatal("夹具不健全：monitorCh 为 nil ⇒ 事件到不了主循环，判据不可信")
	}
	ch <- ev
	msg := waitForMonitorEvent(ch)()
	if msg == nil {
		t.Fatal("waitForMonitorEvent 返回 nil ⇒ 生产入口断路，判据不可信")
	}
	return applyUpdateForTest(t, m, msg)
}

// applyUpdateForTest 走 tea 主循环入口，并把 Update 的返回值还原成指针稳定的 *Model。
func applyUpdateForTest(t *testing.T, m *Model, msg interface{}) *Model {
	t.Helper()
	next, _ := m.Update(msg)
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update 返回 %T，不是 Model ⇒ 主循环入口形态与生产不一致", next)
	}
	nm.reassignServices()
	return &nm
}

// endTurnThroughProduction 走生产入口跑完一整轮：streamDoneMsg → Update →
// handleStreamDone → finishTurn(true) → closeTurn。
//
// 必须**逐条复刻生产的轮次起点**（stream.go 的 sendInput 分支）：
// ① SetState(StateStreaming) —— 轮内状态；② SetRequestID(...) —— 因为 Update 对
// streamDoneMsg 先过 `isStaleStreamRequest`（requestID != "" && != 当前 ID ⇒ 直接丢弃），
// 请求号对不上就永远走不到 finishTurn，判据会以"夹具不健全"的样子假失败。
func endTurnThroughProduction(t *testing.T, m *Model) *Model {
	t.Helper()
	reqID := fmt.Sprintf("ownership-turn-end-%d", time.Now().UnixNano())
	m.sessionService.SetState(StateStreaming)
	m.aiService.SetRequestID(reqID)
	m = applyUpdateForTest(t, m, streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "回答正文"}},
		usage:     &session.TokenUsage{},
		model:     "test-model",
		requestID: reqID,
	})
	return m
}

// 红测 1（before 必须红）：**turn 已结束后，Monitor 事件不得写进主对话流**。
//
// 这是 I-7.1 的直接复现：日志里 `Turn complete 02:22:27` 之后 `02:22:30` 还在 tick。
// 断言"消息列表长度不变"——因为改造前 output/done 都是无条件 addMessage。
func TestMonitorOwnership_NoStreamWriteAfterTurnEnd(t *testing.T) {
	cases := []struct {
		name string
		ev   monitorEvent
	}{
		{"output", monitorEvent{eventType: "output", monitorID: "mon_1", line: "[fixture] tick 2026-09-30T02:22:30Z"}},
		{"done", monitorEvent{eventType: "done", monitorID: "mon_1", status: monitor.StatusFinished}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTUIRegressionModel(t)
			m = endTurnThroughProduction(t, m)

			// 夹具健全性：必须真的"轮已结束"，否则本用例测的不是那个分支。
			//
			// ★ 这里刻意**只**用 A-1 自己的判据（turnWriteOpen ← 会话状态），
			// 不用 turnHasEnded()（那是 A-3 的事实）：两族判据若互相当前置，
			// 反例注入时会出现"拆 A-3 把 A-1 的用例也染红"的耦合，
			// 红集不再互不重叠 ⇒ 归因不清（production-path-tdd R4-10）。
			if m.turnWriteOpen() {
				t.Fatal("夹具不健全：回合收尾后仍在轮内 ⇒ 无法判别")
			}
			if m.sessionService.State() != StateIdle {
				t.Fatalf("夹具不健全：回合收尾后 State=%v，期望 StateIdle", m.sessionService.State())
			}

			before := len(m.sessionService.Messages())
			seenBefore := m.consumedOutput()

			m = feedMonitorEventThroughProduction(t, m, tc.ev)

			after := len(m.sessionService.Messages())
			if after != before {
				last := ""
				if after > 0 {
					last = m.sessionService.Messages()[after-1]
				}
				t.Fatalf("turn 结束后 Monitor %s 事件仍写进了主对话流：消息数 %d → %d\n最后一条：%q",
					tc.name, before, after, last)
			}

			// 正向留痕自检（R3-③）：证明事件**到达并被消费**，而不是"没走这条路所以没写"。
			// 没有这条，负向断言会被"上游短路"蒙对。
			if tc.ev.eventType == "output" && m.consumedOutput() != seenBefore+1 {
				t.Fatalf("正向留痕缺失：output 事件应被消费计数（%d → %d）⇒ 上面的长度断言可能是假绿",
					seenBefore, m.consumedOutput())
			}
		})
	}
}

// 红测 2（before 必须红）：A-1 的作者边界**是有分流的**，两支都要验（R9）。
//
//	after-turn 臂（StateIdle） ⇒ 不得进主消息流（这是 I-7.1 的那个洞）
//	in-turn 臂（StateStreaming） ⇒ **仍应进**（防"一刀切把 done 全禁掉"的过度拦截）
//
// 只验其中一支 = 假绿：前者会被"done 永远不会写"蒙对，后者会被"done 总会写"蒙对。
func TestMonitorDone_AuthorBoundaryFork(t *testing.T) {
	doneEv := monitorEvent{eventType: "done", monitorID: "mon_7", status: monitor.StatusFinished}

	t.Run("after-turn-blocked", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m = endTurnThroughProduction(t, m)
		if m.sessionService.State() != StateIdle {
			t.Fatalf("夹具不健全：回合收尾后 State=%v，期望 StateIdle", m.sessionService.State())
		}

		before := len(m.sessionService.Messages())
		m = feedMonitorEventThroughProduction(t, m, doneEv)
		if after := len(m.sessionService.Messages()); after != before {
			t.Fatalf("StateIdle 下 Async 作者写进了主消息流：%d → %d", before, after)
		}
		if m.suppressedAsync() == 0 {
			t.Fatal("正向留痕缺失：边界应记下被拦的异步写入（suppressedAsync 仍为 0）⇒ 断言可能是假绿")
		}
	})

	t.Run("in-turn-still-writes", func(t *testing.T) {
		m := newTUIRegressionModel(t)
		m.sessionService.SetState(StateStreaming) // 轮内
		if !m.turnWriteOpen() {
			t.Fatal("夹具不健全：StateStreaming 下应处于轮内")
		}

		before := len(m.sessionService.Messages())
		m = feedMonitorEventThroughProduction(t, m, doneEv)
		after := len(m.sessionService.Messages())

		if after != before+1 {
			t.Fatalf("轮内 done 应当到达（否则是过度拦截）：%d → %d", before, after)
		}
		last := m.sessionService.Messages()[after-1]
		if !strings.Contains(last, "mon_7") {
			t.Fatalf("轮内 done 的落点消息未点名 monitor id（用户看不到是谁结束了）：%q", last)
		}
	})
}

// 红测 3：A-5 —— 丢弃必须可计数，且计数**等于实际丢弃数**；done（状态迁移）不得丢。
//
// 生产入口：走 TUI 自己的 Manager（NewModel 里已注册回调的真实实例），
// 起一个真实进程灌 >256 行，**不排空 channel**（等价于主循环被长渲染占住）。
//
// 期望读数（可解析地精确）：
//
//	produced = proc.GetLines()（Monitor 权威副本的累计行数，见 monitor.Process.Lines）
//	容量 = monitorCh 缓冲 256（lifecycle.go 的 make(chan monitorEvent, 256)）
//	⇒ droppedOutput == produced - 256，且 droppedDone == 0
//
// ★ 为什么 done 也是这一条的要害：**同样的"满了"条件下，output 该丢、done 不该丢**。
// 这一对读数才是有判别力的形状 —— 只看 droppedOutput 无法区分
// "只丢采样" 与 "采样和迁移一起丢"（后者正是改造前 `default:` 的行为）。
//
// ★ before 态说明（诚实边界）：改造前这三处是 `default:` 空分支，**没有任何计数符号**，
// 所以本用例的 before 是 [build failed]（编译期红），不构成语义判别性证据。
// 语义判别性由上面的**成对读数**承担：若把 done 改回 `default:` 丢弃，
// droppedDone 会变成非 0 而断言转红（见回执的反例注入）。
func TestMonitorDropCounters_ExactAndDoneNotLost(t *testing.T) {
	m := newTUIRegressionModel(t)

	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		t.Fatal("夹具不健全：MonitorMgr 为 nil")
	}
	ch := m.toolService.MonitorCh()
	const capacity = 256
	if got := cap(ch); got != capacity {
		t.Fatalf("夹具与生产常量脱节：monitorCh 容量 = %d，期望 %d（lifecycle.go）", got, capacity)
	}
	// 取空 channel，保证初始满员度是确定值（否则读数不可复现）。
	for {
		select {
		case <-ch:
			continue
		default:
		}
		break
	}

	const lines = 400
	proc, err := mgr.Start("seq 1 400", "drop-counter fixture", "")
	if err != nil {
		t.Fatalf("起真实监控进程失败（夹具不健全）：%v", err)
	}
	if strings.TrimSpace(proc.Command) == "" {
		t.Fatal("夹具不健全：进程命令为空")
	}

	// done 已改为不丢（阻塞发送）⇒ 无人排空时它会**阻塞在发送上**，
	// waitProcess 不会返回；但进程状态在回调**之前**就已落到终态（monitor.go:520 → :530），
	// 所以按状态轮询即可，不需要等 goroutine 结束。
	if !proc.WaitForStatus(monitor.StatusFinished, 30*time.Second) {
		t.Fatalf("夹具不健全：进程未在期限内结束（status=%v, lines=%d）",
			proc.GetStatus(), proc.GetLines())
	}
	// 收尾：解开阻塞的 done 发送，避免把 goroutine 留在测试里（LIFO：注册在 TempDir 之后）。
	t.Cleanup(func() {
		m.toolService.MonitorEscape().close()
		mgr.StopAll()
	})

	// ★ 必须先等"生产端真的停了"，两个读数才是同一时刻的事实。
	// 为什么不能只看 status：readStream（逐行 AddLine + onOutput）与 waitProcess
	// （cmd.Wait 后置终态）是**两条独立 goroutine，彼此没有 join**（monitor.go:457 / :484），
	// 所以"状态已终态"并不蕴含"所有行都已投递" ⇒ 直接读会得到 off-by-one 的假红/假绿。
	//
	// 两条措施，都不放宽断言精度（仍是**精确等式**）：
	// ① 静默期：连续 1.5s（30×50ms）两个读数都不变 ⇒ 生产端已停；
	// ② 双读夹逼：把 produced 夹在两次 droppedOutput 读取之间，要求两次读数相等
	// ⇒ 排除"读 GetLines 与读 droppedOutput 之间又投递了一行"的窗口。
	produced, dropped := -1, -1
	stable := 0
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		d1 := m.droppedOutput()
		p := proc.GetLines()
		d2 := m.droppedOutput()
		if d1 == d2 && p == produced && d1 == dropped {
			stable++
			if stable >= 30 {
				produced, dropped = p, d1
				break
			}
		} else {
			stable = 0
			produced, dropped = p, d2
		}
		time.Sleep(50 * time.Millisecond)
	}
	if stable < 30 {
		t.Fatalf("夹具不健全：20s 内读数未静止（produced=%d dropped=%d）", produced, dropped)
	}

	if produced <= capacity {
		t.Fatalf("夹具不健全：只产出 %d 行（≤ 容量 %d），灌不出丢弃，判据不可判别", produced, capacity)
	}

	// ★ 精确不变量必须与**实际入队**同口径，不能与"容量"同口径（原判据的病灶，2026-10-06 修）。
	//
	// 为什么 produced-capacity 会偶发差 1：done 走**阻塞发送**，而 waitProcess 与 readStream
	// 是两条**无 join** 的 goroutine（本用例上面已记：状态终态不蕴含所有行已投递）。
	// ⇒ 进程可能先终态，done 在最后几行 output 之前**先抢到一个槽位**（此时 channel 尚未满，
	// 阻塞发送照样成功）；随后 output 只填得下 capacity-1 个，被丢 = produced-(capacity-1)。
	// ⇒ 原式在"done 抢槽"这条路径上恒差 1，且是否抢到槽由调度决定 ⇒ 偶发红（145 vs 144）。
	//
	// 正确口径：续行"每个 output 要么入队、要么计数丢弃"这一条恒等式 ——
	// droppedOutput == 产出 output 尝试数 - 真实入队的 output 事件数
	// 期望值因此从**通道里数出来的 output 事件数**取，而不是从容量推算 ⇒ 与 done 是否抢槽无关。
	//
	// 数法：稳定后把通道排空并分类计数（此时已 1.5s 无新投递，读数即终值）。
	queuedOutput, queuedDone := 0, 0
	for {
		select {
		case ev := <-ch:
			switch ev.eventType {
			case "output":
				queuedOutput++
			case "done":
				queuedDone++
			}
			continue
		default:
		}
		break
	}
	if queuedDone > 0 {
		// 观测留痕（不参与断言）：通道里出现 done，说明它要么在 fill 阶段抢了槽、
		// 要么在本次排空腾出槽位后被解锁进入。两种情形下 queuedOutput 都已如实反映。
		t.Logf("通道内 done=%d、入队 output=%d（容量 %d）", queuedDone, queuedOutput, capacity)
	}

	wantDropped := produced - queuedOutput
	if got := m.droppedOutput(); got != wantDropped {
		t.Fatalf("丢采样计数与实际丢弃数不符：droppedOutput=%d，实际应为 %d（产出 %d - 入队 output %d）",
			got, wantDropped, produced, queuedOutput)
	}
	if got := m.droppedDone(); got != 0 {
		t.Fatalf("done（状态迁移）被丢弃了 %d 次 —— 界面上会永久停在 running 而进程已退出；"+
			"同样的『满』条件下 output 丢、done 不丢才是正确形状", got)
	}
}

// 判据 6：A-3 ——「回合结束」必须是**结构**，不是流里的一句装饰文案。
//
// 三条同时成立才算：
// 1. 可判别事实：turnHasEnded() 为真（承载体在 m.turn，删掉 closeTurn 的赋值即转假）；
// 2. 用户可见：渲染转录里有轮末标记（标记由状态投影，删掉投影即消失）；
// 3. 不占消息身份：显示消息列表与模型侧 session 里**都不得**有它。
//
// 改造前 3 必红（标记就是一条 addMessage 进去的消息）—— 这正是"删代码不报错"的病灶：
// 旧形态删掉那行，界面只是少一行字，测试仍可全绿。
func TestTurnBoundary_StructuralNotAMessage(t *testing.T) {
	m := newTUIRegressionModel(t)
	m = endTurnThroughProduction(t, m)

	if !m.turnHasEnded() {
		t.Fatal("A-3：回合收尾后 turnHasEnded() 为假 ⇒ 边界没有结构性承载体")
	}

	// 2. 渲染面必须有标记（状态 → 渲染的投影接上了）。
	rendered := m.viewportContent
	// ★ 2026-10-07（用户拍板）再收紧：结束标志收敛为单行
	// `━━ ✓ <YYYY-MM-DD HH:MM:SS> ━━`（去轮次号、去中英文重复、时刻带完整年月日）。
	// 逐轮持久化判据见 TestTurnBoundary_EachTurnLeavesPersistentMark / …PastMarkSurvivesNextTurn。
	if !turnDoneMarkPattern.MatchString(rendered) {
		t.Fatalf("轮末结束标志未出现在渲染转录里（状态没投影到界面）：\n%q", rendered)
	}
	if !strings.Contains(rendered, m.turn.closedAt.Format("15:04:05")) {
		t.Fatalf("轮末标记的时间应取自回合结束时刻 %s（不得是渲染时刻）：\n%q",
			m.turn.closedAt.Format("15:04:05"), rendered)
	}

	// 3. 不占消息身份。
	for i, msg := range m.sessionService.Messages() {
		if strings.Contains(msg, "━━") {
			t.Fatalf("A-3 违例：轮末标记仍占消息身份（显示列表第 %d 条）：%q", i, msg)
		}
	}
	for i, smsg := range m.sessionService.CurrentSession().GetMessages() {
		for _, b := range smsg.Blocks {
			if strings.Contains(b.Text, "━━") {
				t.Fatalf("I5 违例：标记泄漏进模型侧 session（第 %d 条消息）", i)
			}
		}
	}
}
