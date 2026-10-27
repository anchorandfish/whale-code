// wait.go — **事件驱动**的等待原语（替代 `time.Sleep` + 断言）。
//
// # 为什么需要这个文件
//
// 测试里写 `time.Sleep(3*time.Second)` 然后断言，等价于断言
// 「这台机器能在 3 秒内把这件事做完」。这条断言与**被测功能无关**：
// 换台慢机器、或者同时跑别的测试，它就红；而在快机器上它又永远绿 ——
// 于是它既不能证明功能对，也不能证明功能错，只能证明「这台机器当时不快不忙」。
//
// 2026-10-27 实测（whale-code `internal/daemon`）：`TestCancelRunningTaskKillsIgnoringProcessGroup`
// 用 3s 预算等"子进程起来"；正常路径实测 0.4s，但**偶发**超过 3s ⇒ 变成约 20% 概率的
// 抛硬币判据，最后靠 CI「失败包重试一次」兜着。这是**症状被按住了，病还在**。
//
// # 判据该等什么
//
// 把「事件会不会发生」（语义，必须严格）与「操作系统多久把它做完」（机械，不该被测）
// 分开。前者用本函数的 `cond` 断言；后者只允许有一个**挂死兜底**上界，不允许有
// 「预期延迟 × 常数」的阈值。所以：
//
//	budget ≠ 延迟阈值，而是**挂死检测器**：
//	  · 由 `t.Deadline()` 推导（默认 30s，且给测试框架留 5s 收尾余量）；
//	  · 数量级远高于任何合理延迟 ⇒ 负载波动跨不过去；
//	  · 真挂了仍然会失败，且失败点落在这条断言上（而不是 `panic: test timed out`）。
//
// # 与 `testing/synctest` 的分工（Go 1.25+）
//
// 纯内存协程（无真实 I/O）优先用标准库 `testing/synctest`：它把时间虚拟化，
// `time.Sleep` 在气泡内瞬间返回，`synctest.Wait()` 等的是**所有协程都阻塞**这个调度事实。
// 但 synctest **管不到**真实子进程、文件系统、socket（这些不是「可持久阻塞」），
// 本仓大量 daemon/worker 用例正属此类 ⇒ 用本文件的 `Eventually`。
package testutil

import (
	"fmt"
	"time"
)

// DefaultWaitBudget 是等待的默认上界——**挂死兜底**，不是延迟阈值。
//
// 30s 的理由：本仓最慢的正常路径（起子进程 + 若干次 fsync）实测在亚秒到数秒级，
// 30s 留了一个数量级以上的余量；真正卡死时仍会失败，只是晚 30s。
const DefaultWaitBudget = 30 * time.Second

// waitPollInterval 是两次求值之间的间隔。它只影响**发现**事件的及时性，
// 不影响结论（事件迟早被看到）⇒ 取小值以缩短绿灯用时。
const waitPollInterval = 5 * time.Millisecond

// waitDeadlineMargin 是从 `t.Deadline()` 推导预算时留给测试框架收尾的时间。
// 少了它，等待会吃掉整个包的超时，失败会表现成 `panic: test timed out`，
// 而不是可读的断言失败。
const waitDeadlineMargin = 5 * time.Second

// WaitTB 是本文件所需的 `testing.TB` 子集。
//
// ★ 这里单独定义接口（而不是复用本包的 `TB`）：`Deadline()` 只有 `*testing.T/B`
// 提供，而 `TB` 的既有调用方不需要它。`*testing.T` 天然满足本接口。
type WaitTB interface {
	Helper()
	Fatalf(format string, args ...any)
	Deadline() (deadline time.Time, ok bool)
}

// Eventually 等待 cond 成立，超时则用 what 作为失败信息。
//
// 预算取 `waitBudget`（挂死兜底）。需要报告现场状态时用 EventuallyObserving。
func Eventually(t WaitTB, what string, cond func() bool) {
	t.Helper()
	EventuallyWithin(t, waitBudget(t), what, cond)
}

// EventuallyObserving 与 Eventually 相同，但失败时额外调用 state() 把现场打出来。
//
// ★ 为什么强制传 state：一条「X 没发生」的失败信息，读的人无法判断
// 是「X 的哪个前提没成立」。本仓 2026-10-27 那条 flaky 就是栽在这里 ——
// 报「task 与子进程没起来」，但到底是状态没到 running、还是父 PID 文件没写、
// 还是子 PID 文件没写，全靠猜。
func EventuallyObserving(t WaitTB, what string, cond func() bool, state func() string) {
	t.Helper()
	EventuallyWithinObserving(t, waitBudget(t), what, cond, state)
}

// EventuallyWithin 用显式预算等待 cond 成立。
//
// ★ 只有一种情况该用它：**预算本身就是语义**（例如「这条命令必须在 10s 读超时之前返回」）。
// 此时要拿**被测的语义常量**当上界（如 `UDSReadTimeout`），并在注释里写明为什么，
// 不要写 `3*time.Second` 这种拍出来的数。
func EventuallyWithin(t WaitTB, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	EventuallyWithinObserving(t, budget, what, cond, nil)
}

// EventuallyWithinObserving 是全部实现的落点。
func EventuallyWithinObserving(t WaitTB, budget time.Duration, what string, cond func() bool, state func() string) {
	t.Helper()

	start := time.Now()
	deadline := start.Add(budget)
	polls := 0
	for {
		if cond() {
			return
		}
		polls++
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(waitPollInterval)
	}

	observed := ""
	if state != nil {
		observed = "；现场：" + state()
	}
	t.Fatalf("%s：等待 %v 超时（求值 %d 次）%s\n"+
		"★ 这是**挂死兜底**，不是延迟阈值：若事件确实会发生、只是更慢，"+
		"说明被测路径的延迟已经超出 30s 量级，请先查它为什么这么慢，"+
		"而不是把预算调大（调大等于把这条判据重新变回『等固定秒数』）。",
		what, time.Since(start), polls, observed)
}

// waitBudget 由测试自己的 deadline 推导等待上界，并留出收尾余量。
//
// 无 deadline（极少数场景）或余量已不足时退化为 DefaultWaitBudget / 1s 下限。
func waitBudget(t WaitTB) time.Duration {
	budget := DefaultWaitBudget
	if dl, ok := t.Deadline(); ok {
		if remaining := time.Until(dl) - waitDeadlineMargin; remaining < budget {
			budget = remaining
		}
	}
	if budget < time.Second {
		budget = time.Second
	}
	return budget
}

// PollInterval 暴露给需要在别处复用同一节奏的调用方（读数一致，便于横向比较）。
func PollInterval() time.Duration { return waitPollInterval }

// FormatObserved 把若干「名字=值」拼成一行现场串（省得每个调用点各写一遍 Sprintf）。
func FormatObserved(pairs ...any) string {
	s := ""
	for i := 0; i+1 < len(pairs); i += 2 {
		if s != "" {
			s += " "
		}
		s += fmt.Sprintf("%v=%v", pairs[i], pairs[i+1])
	}
	return s
}
