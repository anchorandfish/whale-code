package tools

// ── A + B 判据：Monitor 的模型面「说明书写实话」与「拉得有的放矢」 ─────────────
//
// 背景（2026-10-13 设计评审，手测卡② §19）：whale 的 Monitor 是**即发即忘**
// （Start 起进程后立刻返回 id，工具结果里只有 id —— internal/tools/monitor.go:47-51），
// 之后**没有任何事件回到模型**（done 只进显示缓冲，见卡② §18.2 / §19.2）。
// 对比 同类 CLI：exec_command 带 yield_time_ms，返回体**本身**就含 exit_code
// （"when the command finished during this call"）/ session_id（"when the process is
// still running"）—— 对标证据件。
// ⇒ 模型据结果决定要不要再 write_stdin 拉，**拉得有的放矢**。
//
// 本文件两条判据（各自独立、都能在 before 态判别）：
//
//	A. Spec 描述必须诚实：
// 不得承诺 "Completion is reported when the process exits."（模型收不到）；
// 不得指模型去用人类斜杠命令 /monitors（模型调不到， 同类）；
// 必须给出模型真能走的读回入口 MonitorTail。
//	B. MonitorTail 必须暴露 wait_ms 并真的等：
// 进程仍 running 时，等到进程结束（或等待预算耗尽）再返回，
// 让「完没完 / 退码多少」出现在**同一次工具结果**里（对齐 同类 CLI yield）。
//
// before 自检（R4-7）：改动前 Spec 描述含 "Completion is reported" 与 "/monitors"、
// 且 MonitorTail 无 wait_ms ⇒ 两条判据在 before 态**均红**。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
)

// TestMonitorSpec_DescriptionIsHonest 判据 A。
func TestMonitorSpec_DescriptionIsHonest(t *testing.T) {
	r := NewRegistry()
	spec, ok := r.Get("Monitor")
	if !ok {
		t.Fatal("Monitor spec 不存在 ⇒ 判据不可信")
	}
	desc := spec.Description

	// A-1：不得承诺「进程结束会汇报」——模型收不到（卡② §18.2 已证 done 只进显示缓冲）。
	if strings.Contains(desc, "Completion is reported") {
		t.Errorf("描述仍在承诺完成通知（模型收不到，会等一个永不到来的通知）：\n%q", desc)
	}
	// A-2：不得把人类斜杠命令当模型入口（同类：模型调不到 /monitors）。
	if strings.Contains(desc, "/monitors") {
		t.Errorf("描述让模型用人类斜杠命令 /monitors（模型调不到）：\n%q", desc)
	}
	// A-3：必须给出模型真能走的读回入口（只删不给路 = 半修）。
	if !strings.Contains(desc, "MonitorTail") {
		t.Errorf("描述未给出模型可走的读回入口 MonitorTail：\n%q", desc)
	}
}

// TestMonitorTailSpec_HasWaitParameter 判据 B（Spec 面）。
func TestMonitorTailSpec_HasWaitParameter(t *testing.T) {
	r := NewRegistry()
	spec, ok := r.Get("MonitorTail")
	if !ok {
		t.Fatal("MonitorTail spec 不存在 ⇒ 判据不可信")
	}
	props, _ := spec.InputSchema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatalf("MonitorTail spec 无 properties：%+v", spec.InputSchema)
	}
	if _, ok := props["wait_ms"]; !ok {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		t.Fatalf("MonitorTail 未暴露 wait_ms 参数 ⇒ 模型无法等到进程结束（对齐同类 CLI 的 yield_time_ms）；现有参数=%v", keys)
	}
}

// TestMonitorTailHandler_WaitsForCompletion 判据 B（行为面）。
//
// 夹具：起一个 ~400ms 自行结束的进程，**立刻**读回：
// - wait_ms=3000 ⇒ 必须等到结束，返回体给最终状态（finished/failed）与退码，**不得**报 running；
// - 不传 wait_ms ⇔ wait_ms=0 ⇒ 立即返回，报 running（证明"等待"确实是 wait_ms 驱动的，
// 不是无条件阻塞 —— 否则这条会被"永远阻塞"蒙对）。
func TestMonitorTailHandler_WaitsForCompletion(t *testing.T) {
	e := &Executor{handlers: make(map[string]Handler), MonitorMgr: monitor.NewManager()}
	t.Cleanup(e.MonitorMgr.StopAll)

	workDir := t.TempDir()
	proc, err := e.MonitorMgr.Start("sh -c 'sleep 0.4'", "wait probe", workDir)
	if err != nil {
		t.Fatalf("起探针进程失败：%v", err)
	}
	handler := e.monitorTailHandler()

	// 臂 1：不传 wait_ms ⇒ 立即返回，进程仍在 running。
	raw1, _ := json.Marshal(map[string]interface{}{"monitor_id": proc.ID})
	out1, err := handler(context.Background(), raw1)
	if err != nil {
		t.Fatalf("MonitorTail(no wait) 出错：%v", err)
	}
	if !strings.Contains(out1, string(monitor.StatusRunning)) {
		t.Errorf("不传 wait_ms 时应立即返回 running（等待应只由 wait_ms 驱动）：\n%s", out1)
	}

	// 臂 2：带 wait_ms=3000 ⇒ 等到进程结束，返回最终状态 + 退码。
	raw2, _ := json.Marshal(map[string]interface{}{"monitor_id": proc.ID, "wait_ms": 3000})
	start := time.Now()
	out2, err := handler(context.Background(), raw2)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("MonitorTail(wait_ms) 出错：%v", err)
	}
	if strings.Contains(out2, string(monitor.StatusRunning)) {
		t.Fatalf("wait_ms 未生效：进程应已结束，返回体仍报 running（耗时 %v）：\n%s", elapsed, out2)
	}
	if !strings.Contains(out2, "exit=0") {
		t.Errorf("等待结束时返回体应含最终退码 exit=0：\n%s", out2)
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("wait_ms 声称等到结束却只用了 %v（进程约 400ms 才结束）⇒ 可能是假绿", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("wait_ms=3000 却耗时 %v ⇒ 等待预算未生效", elapsed)
	}
	_ = fmt.Sprint(proc.ID)
}
