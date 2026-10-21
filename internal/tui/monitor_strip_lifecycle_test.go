package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
)

// monitor_strip_lifecycle_test.go — 「Monitor 常驻面只反映 running」判据（B2，2026-09-30）。
//
// 用户真机原话：「摘要区（形如 `📡 mon_1 · … · ⏳ running · N lines`），这个一直放在底部，
// 是不是不好啊，我 stop 了它也没有消失，只是显示 stop 的状态。」
//
// 病灶（实测）：renderMonitorStrip（render_extras.go:506）直接渲染 mgr.List() **全量**、
// 不看状态；monitorBadge（:580）还**有意**保留了残留分支 —— running==0 时渲染
// `📡 N monitor(s) done (/monitors)` ⇒ 终态记录永久占屏（最多 5 行）。
//
// 口径对齐两类（皆为 file:line 实读）：
// - 同类 CLI：对标证据件 —— 常驻面 = 单行 DIM，
// 只列 running terminals，进程结束即 retain 移除。
// - 同类 CLI：src/components/tasks/BackgroundTaskStatus.tsx:195-197 ——
// `if (runningTasks.length === 0) { return null }`，无运行任务整块消失。
//
// 生产入口：NewModel（生产唯一装配点）→ MonitorMgr（真实 Manager）→
//
//	Start/Stop（真进程，走生产出口）→ Model.View()（tea 主循环的渲染出口）。
//
// ★★ 配对格（缺一半即假绿）：①「运行中必须在常驻面可见」与 ②「结束后必须消失」
// **必须成对读** —— 若有人把 renderMonitorStrip 改成 `return ""`，② 照样绿，
// 而 ① 会先红。只断言 ② 是典型的负向断言被"上游恒空"蒙对（production-path-tdd R3-③）。
func TestMonitorStrip_ReflectsRunningOnly(t *testing.T) {
	m := newTUIRegressionModel(t)
	// View() 在未 Ready 时只返回 loading 占位；宽度 -4 后 ≤0 时 strip 自己返回空串。
	// 两者都是"观测不到"的假绿源，必须先排掉。
	m.uiService.SetReady(true)
	m.uiService.SetWidth(120)

	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		t.Fatal("夹具不健全：MonitorMgr 为 nil（事件/投影都无从谈起）")
	}
	t.Cleanup(mgr.StopAll)

	proc, err := mgr.Start("sleep 30", "strip lifecycle fixture", "")
	if err != nil {
		t.Fatalf("起真实监控进程失败（夹具不健全）：%v", err)
	}
	if !proc.WaitForStatus(monitor.StatusRunning, 10*time.Second) {
		t.Fatalf("夹具不健全：进程未进入 running（status=%v）", proc.GetStatus())
	}

	// ① 正向半边：运行中必须在常驻面可见。
	runningView := m.View()
	if !strings.Contains(runningView, proc.ID) {
		t.Fatalf("运行中的 Monitor %s 未出现在界面常驻面 ⇒ 本判据不可判别"+
			"（strip 恒空也会让下面的「结束后消失」断言为真）", proc.ID)
	}
	if !strings.Contains(runningView, "running") {
		t.Fatalf("常驻面未标出 running 状态：\n%s", runningView)
	}

	// 制造终态：走**生产出口** Stop（正是用户报障的那条路），不是手工改状态。
	if err := mgr.Stop(proc.ID); err != nil {
		t.Fatalf("Stop 失败：%v", err)
	}
	if n := mgr.RunningCount(); n != 0 {
		t.Fatalf("夹具不健全：Stop 后仍有 %d 个 running", n)
	}

	// ② 负向半边：结束后必须从常驻面消失（用户诉求）。
	stoppedView := m.View()
	if strings.Contains(stoppedView, proc.ID) {
		t.Fatalf("已停止的 Monitor %s 仍占常驻面 —— 用户报障「我 stop 了它也没有消失」：\n%s",
			proc.ID, stoppedView)
	}
	if b := m.monitorBadge(); b != "" {
		t.Fatalf("无运行进程时状态栏角标仍非空：%q（残留的 done 分支应删除）", b)
	}
}

// TestMonitorStrip_ManyRunningCollapsesToOneRow：多台运行时只占**一行** + 溢出计数。
//
// 对标两类：同类 CLI 的 footer 恒为单行（`N background terminal(s) running · /ps to view`），
// 同类 CLI 的 pill 也是单行。多台时具体 id 由按需出口（/monitors）给，不挤在常驻面。
//
// 直调 renderMonitorStrip 是刻意的：被测行为就是"渲染层怎么裁"，
// 接线点（app.go:2998 把它拼进 View）已核实，故本格不重复断言接线。
func TestMonitorStrip_ManyRunningCollapsesToOneRow(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.uiService.SetReady(true)
	m.uiService.SetWidth(120)

	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		t.Fatal("夹具不健全：MonitorMgr 为 nil")
	}
	t.Cleanup(mgr.StopAll)

	for i := 0; i < 2; i++ {
		proc, err := mgr.Start("sleep 30", "strip row fixture", "")
		if err != nil {
			t.Fatalf("起第 %d 个监控进程失败：%v", i+1, err)
		}
		if !proc.WaitForStatus(monitor.StatusRunning, 10*time.Second) {
			t.Fatalf("第 %d 个进程未进入 running", i+1)
		}
	}

	strip := m.renderMonitorStrip()
	if rows := strings.Count(strip, "📡 "); rows != 1 {
		t.Fatalf("常驻面应只占 1 行详情（对齐同类 CLI 的单行口径），实际 %d 行：\n%s",
			rows, strip)
	}
	if monitorStripMaxRows != 1 {
		t.Fatalf("行数上限应为 1（口径常量本身也要对），实际 %d", monitorStripMaxRows)
	}
	if !strings.Contains(strip, "2 monitor") && !strings.Contains(strip, "+1 more") {
		t.Fatalf("被折叠的台数必须以计数形式可见（不得静默吞掉）：\n%s", strip)
	}
}
