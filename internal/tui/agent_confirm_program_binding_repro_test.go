// agent_confirm_program_binding_repro_test.go — P0 复现 → 回归守卫
// （2026-09-13 T-F 手测发现；2026-09-14 按方案 G 修复）。
//
// 现象（修复前）：TUI 真实入口里让子 agent 执行 `rm -rf /tmp/abc`，子 agent 回传
// "Error: tool bash rejected by user (sub-agent confirmation)"，
// 但会话里**从未弹出过确认 overlay**（用户侧无任何可应答的界面）——
// 即"用户拒绝"是伪造的归因。user 的 ③ 就是这个：他记得看到过
// "允许本次"（那是**主会话**的确认），而子 agent 的确认根本没到达界面。
//
// 根因（代码路径，非猜测）：
// - lifecycle.go:83 func NewModel(...) Model —— **按值返回**
// - lifecycle.go:419 m.executor.AgentConfirmFn = m.agentConfirmBridge
// 该行在 NewModel 内部，绑定方法捕获的是 NewModel 的**局部副本**。
// - main.go:326 model := tui.NewModel(...) // main 的副本（与上面不是同一个对象）
// - main.go:364 p := tea.NewProgram(model) // Program 内部又持有一份副本
// - main.go:366 model.SetProgram(p) // 只写 main 那份副本
// ⇒ 绑定接收者的 program 字段**永远是 nil**（旧实现里 program 是普通结构字段，不共享）。
// - agent_confirm.go:33 prog := m.program
// - agent_confirm.go:34 if prog == nil { return false } ← 微秒级静默返回 false
// ⇒ agent.go 收到 confirmed=false → "tool %s rejected by user (sub-agent confirmation)"
//
// 修复（方案 G，共享持有者）：program 改为 `*programRef`（在 NewModel 中创建一次的
// 小对象），SetProgram 与全部读取点都经它。指针字段在副本间共享 ⇒ 任意副本都能
// 看到已注入的 Program，"注入到哪份副本"这个隐式契约从根上消失。
// 同一修法顺带修好全部同因受害者（/doctor、/upgrade、/memory edit、异步编排、MCP elicitation）。
//
// 判据（可判别，非"构造出来的绿"）：
// - 生产接线顺序（NewModel → NewProgram → SetProgram）下调用**已接线**的
// exec.AgentConfirmFn：若它拿到非 nil program，prog.Send 会在未运行的
// Program 上阻塞（bubbletea v1: msgs 为无缓冲 channel、ctx 未初始化 →
// select 永久阻塞），调用**不会立刻返回**；若 program 为 nil 则立即返回 false。
// - 因此"是否在毫秒级内返回"就是判别器。见同文件 TestAgentConfirmFnBoundReceiverControl
// 的 A/B 对照：显式重绑到 SetProgram 过的那份 Model 后，同一判据给出相反结果。
// - 修复前的红：A 组 3.037µs 提前返回；修复后：A 组与对照组同判为"未提前返回"。
//
// 注意：断言"字段非 nil"或"Update 返回的副本 program 非 nil"都**没有**判别力
// （后者在修复前后都成立）——必须**调用那个已接线的函数**。见 production-path-tdd R1b。
package tui

import (
	"context"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// newProgramWithContext 复刻 main.go:326/364/366 的接线顺序（版）。
//
// 测试里的 program 从不 Run，桥 goroutine 的 prog.Send 会
// 阻塞在无缓冲 msgs channel 上（select 只多等 p.ctx.Done，而 NewProgram 默认 ctx
// 是永不取消的 Background）⇒ 卡死 goroutine 永久泄漏。因此这里用**可取消 ctx**
// 建 program，cleanup 时 cancel ⇒ 卡死的 Send 经 Done 分支解锁退出。
// 不影响两个守卫测试的判据：测量窗口在 cleanup 之前完成。
// 返回 cancelProg 供判别测试**显式**取消（cleanup 在测试体之后才跑，测不到 settle）。
func newProgramWithContext(t *testing.T) (Model, *tea.Program, context.CancelFunc) {
	t.Helper()
	progCtx, cancelProg := context.WithCancel(context.Background())
	t.Cleanup(cancelProg)
	cfg := &config.Config{PermissionMode: "workspace-write"}
	model := NewModel(cfg, newTestClient("test-model"), newTestRegistry())
	p := tea.NewProgram(model, tea.WithContext(progCtx)) // 与 main.go:364 同构（Program 内部持有副本）
	model.SetProgram(p)                                  // 与 main.go:366 同构
	return model, p, cancelProg
}

// installProgramLikeMain 与 main.go 接线同构的便捷封装（隐藏 cancel，守卫测试用）。
func installProgramLikeMain(t *testing.T) (Model, *tea.Program) {
	t.Helper()
	model, p, _ := newProgramWithContext(t)
	return model, p
}

// callConfirmFn 调用 fn 并返回"耗时是否在 quick 阈值内"。
// quick=true 表示立即返回（program 为 nil）；quick=false 表示被 prog.Send 阻塞（program 活着）。
// settled 在 fn **真正返回**时关闭（判别用）：调用方 4s 超时返回后，
// 若 fn 仍卡在 prog.Send 上，settled 永不关闭 ⇒ 可判定 goroutine 永久泄漏。
func callConfirmFn(t *testing.T, fn func(context.Context, string, string) tools.ConfirmOutcome) (elapsed time.Duration, quick bool, settled <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	type res struct{ d time.Duration }
	done := make(chan res, 1)
	settledCh := make(chan struct{})
	settled = settledCh
	go func() {
		start := time.Now()
		_ = fn(ctx, "bash", `{"command":"rm -rf /tmp/abc"}`)
		close(settledCh) // fn 返回（含 ctx 取消后的正常出口）才关闭
		done <- res{time.Since(start)}
	}()

	select {
	case r := <-done:
		return r.d, r.d < 100*time.Millisecond, settled
	case <-time.After(4 * time.Second):
		// 被 prog.Send 阻塞（Program 未运行、无人消费 msgs）→ 说明 program 非 nil
		return 4 * time.Second, false, settled
	}
}

// TestAgentConfirmFnBoundReceiverSeesProgram 生产接线下的回归守卫（P0）。
// 断言：quick=false —— bridge 到达 prog.Send/等待用户决策，而非静默返回。
// 修复前该断言为红（3.037µs 提前返回，program 恒 nil）；修复后应恒绿。
// 注入反例（把 programRef 的共享实例改成各副本独立）即可把它打红 ⇒ 有判别力。
func TestAgentConfirmFnBoundReceiverSeesProgram(t *testing.T) {
	model, _ := installProgramLikeMain(t)

	exec := model.toolService.Executor()
	if exec == nil || exec.AgentConfirmFn == nil {
		t.Fatal("前置失败：executor.AgentConfirmFn 未接线")
	}

	elapsed, quick, _ := callConfirmFn(t, exec.AgentConfirmFn)
	if quick {
		t.Fatalf("P0 复现：已接线的 AgentConfirmFn 在 %v 内就返回了 false —— "+
			"绑定接收者的 program 为 nil，子 agent 确认 overlay 在生产中永不弹出，"+
			"所有 NeedConfirm 被伪造成 'rejected by user (sub-agent confirmation)'"+
			"（期望：阻塞等待用户决策，不早于 ctx/超时）", elapsed)
	}
	t.Logf("OK：bridge 未提前返回（被 Send/等待阻塞），耗时口径 %v", elapsed)
}

// TestAgentConfirmFnBoundReceiverControl A/B 对照（判别力证明）：
// 把 AgentConfirmFn 显式重绑到「SetProgram 已生效的那份 Model」上，同一判据必须给出相反结果。
// 若本测试也给出 quick=true，说明判据本身没有判别力，上一个测试的结论无效。
func TestAgentConfirmFnBoundReceiverControl(t *testing.T) {
	model, _ := installProgramLikeMain(t)

	exec := model.toolService.Executor()
	if exec == nil {
		t.Fatal("前置失败：Executor 未初始化")
	}
	// 对照组：绑定 receiver 就是 SetProgram 那份 Model
	exec.AgentConfirmFn = model.agentConfirmBridge

	elapsed, quick, _ := callConfirmFn(t, exec.AgentConfirmFn)
	if quick {
		t.Fatalf("判据无判别力：对照组也在 %v 内返回，无法区分 program 是否为 nil", elapsed)
	}
	t.Logf("OK：对照组被判为「未提前返回」，耗时口径 %v —— 判据可判别", elapsed)
}

// TestW095_ProbeGoroutineSettlesAfterProgramRelease 回归判据。
//
// 现象（panic 栈实锤，/tmp/repro.log goroutine 70/98）：本文件的探针 goroutine
// 卡死在 agentConfirmBridge → prog.Send（agent_confirm.go:53）。bubbletea v1 的 Send
// 是 `select { case <-p.ctx.Done(): case p.msgs <- msg: }` —— 测试里 program 从不 Run，
// msgs 无人接收，而 NewProgram 默认 ctx 是永不取消的 Background ⇒ goroutine **永久**
// 卡死（卡点在桥自身 select 之前，10 分钟安全网 agentConfirmTimeout 不可达）。
// 全量跑一次泄 2 个（两个守卫测试各一）；泄漏不阻塞测试退出，但属"永不归还"资源。
//
// 判别器：callConfirmFn 返回的 settled 只在 fn 真正返回后关闭。修复后 program 的
// ctx 可取消 ⇒ 显式 cancelProg ⇒ 卡死的 Send 经 Done 分支解锁 → 桥自身 select 见
// 测试 ctx（2s）已超时 → 返回。修复前本测试 5s 必红（settled 永不关闭）；
// 注入反例（改回 tea.NewProgram(model) 默认 Background ctx）即可复红。
func TestW095_ProbeGoroutineSettlesAfterProgramRelease(t *testing.T) {
	model, _, cancelProg := newProgramWithContext(t)

	exec := model.toolService.Executor()
	if exec == nil || exec.AgentConfirmFn == nil {
		t.Fatal("前置失败：executor.AgentConfirmFn 未接线")
	}

	_, _, settled := callConfirmFn(t, exec.AgentConfirmFn)

	// callConfirmFn 的调用方已在 4s 处返回（fn 仍卡在 prog.Send），现在取消 program ctx
	cancelProg()

	select {
	case <-settled:
		// 桥 goroutine 已退出：prog.Send 经 program ctx Done 解锁，桥经测试 ctx 超时出口返回
	case <-time.After(5 * time.Second):
		t.Fatal("：取消 program ctx 后探针 goroutine 仍未退出 —— " +
			"prog.Send 未按 p.ctx.Done 解锁 ⇒ goroutine 永久泄漏（每次全量泄 2 个）。" +
			"修法：newProgramWithContext 以 tea.WithContext(可取消 ctx) 建 program")
	}
}
