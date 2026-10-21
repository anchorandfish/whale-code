package monitor

import (
	"fmt"
	"testing"
	"time"
)

// monitor_record_retention_test.go — 「已完成监控记录有界保留」判据（B1，2026-09-30）。
//
// 病灶（实测，见 新发现_分诊规则与登记表）：
//
//	Manager.processes **只增不减** —— waitProcess 从不 delete（monitor.go:490-533），
//	Remove(id) 全仓零生产调用（Grep 核实：连测试都没引用过）⇒ 已完成记录无界累积，
//	每条最多 DefaultMaxLines(10000) 行输出常驻内存。长期会话里 /monitors 列表越用越长。
//
// 修法（第一性，**不是**「结束即删」）：
//
//	给保留一个**界** —— 只保留最近 DefaultMaxRetainedFinished 条已完成记录，淘汰最旧的。
//	★ 为什么不能「结束即删」：/monitors tail <id> 是监控输出**唯一**的读取出口
//
// （A-2 之后输出不再进对话流），而 done 回执恰好提示 `tail <id>`
// ⇒ 结束即删会让「刚结束」的进程立刻不可 tail、回执指向不存在的目标 = 用户可见退化。
// 病根是「保留无界」，不是「保留」。这与两类竞品的形态一致：
// 同类 CLI 的常驻 pill 只数 running，历史留在按需对话框（BackgroundTasksDialog）。
//
// 生产入口：NewManager（零参数，与生产同构）→ Start（真进程，非 Windows 走 sh -c）。
//
// before 态：本判据引用 DefaultMaxRetainedFinished（**新符号**）⇒ before 是 [build failed]。
// 按 production-path-tdd R4-6，**编译期红不算语义判别性证据** ⇒ 判别性由**反例注入**承担：
// 把 pruneFinishedLocked 的首行改成 `return`（其余一行不动）⇒ 本判据必须转红
// （读数记在 内部工作回执 的回执里）。这与同目录 TestMonitorDropCounters 的处理一致。
func TestFinishedRecordsAreBounded(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.StopAll)

	// 用 done 回调做同步点：prune 在 onDone **之前**执行（同一次 waitProcess 内），
	// 因此「收到全部 done」蕴含「全部 prune 已跑完」—— 断言不必靠 sleep 猜时机。
	doneCh := make(chan string, DefaultMaxRetainedFinished+1)
	m.SetDoneCallback(func(id string, _ MonitorStatus, _ int, _ string) {
		doneCh <- id
	})

	total := DefaultMaxRetainedFinished + 1
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		proc, err := m.Start("echo retention-fixture", "retention fixture", "")
		if err != nil {
			t.Fatalf("第 %d 个监控进程启动失败（夹具不健全）：%v", i+1, err)
		}
		ids = append(ids, proc.ID)
		select {
		case <-doneCh:
		case <-time.After(30 * time.Second):
			t.Fatalf("第 %d 个进程未在期限内结束（status=%v）", i+1, proc.GetStatus())
		}
	}

	// 夹具健全性：id 必须形如 mon_<n> 且严格递增（「淘汰最旧」的判别依赖它）。
	if want := fmt.Sprintf("mon_%d", total); ids[len(ids)-1] != want {
		t.Fatalf("夹具与生产 id 生成脱节：最后一个 id = %q，期望 %q", ids[len(ids)-1], want)
	}
	if n := m.RunningCount(); n != 0 {
		t.Fatalf("夹具不健全：仍有 %d 个进程在跑，记录裁剪无从判别", n)
	}

	list := m.List()
	if len(list) != DefaultMaxRetainedFinished {
		t.Fatalf("已完成记录未被裁到上限：List() = %d 条，期望 %d 条 —— "+
			"无界累积正是  的病灶（每条最多 %d 行输出常驻内存）",
			len(list), DefaultMaxRetainedFinished, DefaultMaxLines)
	}

	// 淘汰的必须是**最旧**的，不是随机的（List 走 map 遍历，顺序不可依赖）。
	if _, ok := m.Get(ids[0]); ok {
		t.Fatalf("最旧的记录 %s 未被淘汰 ⇒ 淘汰顺序不是「最旧优先」", ids[0])
	}
	// 最新的一定还在：否则「保留最近 N 条」的语义不成立，
	// 且会让刚结束的进程立刻不可 `tail`（done 回执恰好提示它）。
	if _, ok := m.Get(ids[len(ids)-1]); !ok {
		t.Fatalf("最新的记录 %s 被淘汰 ⇒ 刚结束的进程会立刻不可 `tail`（用户可见退化）",
			ids[len(ids)-1])
	}
}

// TestDefaultRetentionIsNonZero 守卫「默认上限 > 0」。
//
// 上限为 0 在语义上**等价于「结束即删」** ⇒ /monitors tail <id> 对刚结束的进程立刻失效，
// 而 done 回执恰好提示它。这条守卫把该退化钉死在默认值上。
func TestDefaultRetentionIsNonZero(t *testing.T) {
	if DefaultMaxRetainedFinished <= 0 {
		t.Fatalf("DefaultMaxRetainedFinished = %d ⇒ 等价于「结束即删」；"+
			"/monitors tail <id> 会对刚结束的进程立刻失效，而 done 回执恰好提示它",
			DefaultMaxRetainedFinished)
	}
}
