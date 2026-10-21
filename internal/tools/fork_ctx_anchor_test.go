package tools

// ── ：fork 子 agent 的寿命锚点挂错 ⇒ 工具一返回就被 cancel（真机 2026-10-10 15:31 实死）──
//
// 缺陷（源码链，全部 file:line 已核）：
// 调度器给每次工具调用建短命 ctx：scheduler.go:284 `toolCtx := WithCancel(s.ctx)`，
// 执行完立刻 cancel()（:308）；TUI 批次路径同构（tool_exec.go:155/283 建批次 ctx，
// 269 批尾 cancel）。Agent 工具的 fork 分支把这个 ctx **原样**传给
// handleForkAgent（web.go:544）→ RunForkAsync（web.go:604）→
// `forkCtx := WithTimeout(parentCtx, a.Timeout)`（agent.go:928）。
// ⇒ 工具刚返回 "launched in background" 回执，fork 的 ctx 就死了 ⇒
// 第一轮 StreamWithRetry 报 `context canceled` ⇒ agent.go:509 抛
// `agent stream error` ⇒ FailTask。真机转录：`duration: 0s`、`result: (none yet)`。
//
// 修复（拍板 2026-10-10，第一性 + 竞品对齐）：
// ① 寿命归 owner：fork 锚到 Executor 会话级 ctx（Close 时收杀，承接 H1-2「父取消传播」意图，
// 但锚点从「工具调用」升级为「会话」）；工具调用 ctx 只保留**值**（WithoutCancel）。
// 对标 同类 CLI：spawn_agent 经 session.services.agent_control 注册（multi_agents/spawn.rs:122-140），
// 子 agent 寿命与 tool invocation 上下文解耦。
// ② 去寿命闹钟：fork 不设 120s 墙钟（拍板：对齐 同类 CLI `DEFAULT_AGENT_JOB_MAX_RUNTIME_SECONDS=None`
// / 同类 CLI 后台任务框架无 timeout 字段）。防失控靠结构闸：MaxRounds=200 + 编排层 token 总闸。
//
// ── 判别性自证（production-path-tdd R4/R6）────────────────────────────────
// 判据 A（钉缺陷）before 必红：fork 继承 toolCtx，cancel 落在模型请求进行中
// ⇒ `agent stream error: context canceled`（与真机转录逐字同因）。
// 判据 B（保 owner 收杀）before 必红：fork 只继承调用方 ctx（= Background），
// Close 与它无关 ⇒ 永远 running。
// 变体注入（-overlay，不碰磁盘）：V1 撤 WithoutCancel ⇒ 只 A 红；V2 摘 AfterFunc ⇒ 只 B 红。
// 红集互不相交 ⇒ 两个语义各自承重（R4-10）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// w065BlockingClient 模拟真实模型调用的耗时：Stream 阻塞到被放行或 ctx 取消。
//
// 为什么不能用现成 makeTextStream（秒回）：真机上 fork 死于「cancel 落在模型请求进行中」；
// 秒回 mock 会让 fork 在 cancel 落地前就跑完 ⇒ before 态随机绿 ⇒ 判别性塌掉（R4-7）。
type w065BlockingClient struct{ release chan struct{} }

func (m *w065BlockingClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.release:
		return makeTextStream("w065-fork-result"), nil
	}
}
func (m *w065BlockingClient) Model() string   { return "test-model" }
func (m *w065BlockingClient) SetModel(string) {}
func (m *w065BlockingClient) Close() error    { return nil }

// launchW065Fork 走生产入口 Executor.Execute 起 fork，返回 fork task id。
func launchW065Fork(t *testing.T, e *Executor, toolCtx context.Context) string {
	t.Helper()
	res := e.Execute(toolCtx, rbCall(t, "Agent", map[string]interface{}{
		"prompt":     "produce the w065 marker",
		"agent_type": "fork",
	}))
	if res.IsError {
		t.Fatalf("Agent(agent_type=fork) 启动失败：%s", res.Output)
	}
	return rbParseForkIDFromReceipt(t, res.Output)
}

// w065PollForkStatus 轮询 ForkStatus 直到回执含 want，超时则带最后一次回执报红。
func w065PollForkStatus(t *testing.T, e *Executor, forkID, want, timeoutMsg string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for {
		r := e.Execute(context.Background(), rbCall(t, "ForkStatus", map[string]string{"task_id": forkID}))
		if r.IsError {
			t.Fatalf("ForkStatus(%s) 报错：%s", forkID, r.Output)
		}
		last = r.Output
		if strings.Contains(last, want) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s；forkID=%s 最后一次回执=%q", timeoutMsg, forkID, last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 判据 A（钉缺陷）：复刻生产批尾 cancel（scheduler.go:308 / tool_exec.go:269）。
// 工具调用返回后调度器立刻 cancel 本次调用的 ctx —— fork 的寿命不得挂在这条 ctx 上。
func TestW065_Fork_SurvivesToolCallCtxCancel(t *testing.T) {
	e := newReadbackExecutor(t)
	mc := &w065BlockingClient{release: make(chan struct{})}
	e.AIClient = mc

	toolCtx, cancelTool := context.WithCancel(context.Background())
	defer cancelTool()
	forkID := launchW065Fork(t, e, toolCtx)

	cancelTool()      // ★ 生产批尾 cancel 的精确复刻
	close(mc.release) // 再放行模型响应（真机上 = 模型正常返回）；先 cancel 后放行，排除时序蒙对

	last := w065PollForkStatus(t, e, forkID, "w065-fork-result",
		"超时：fork 未能活过工具调用 ctx 的取消（寿命锚点仍挂在工具调用上？）")
	if !strings.Contains(last, "completed") {
		t.Fatalf("fork 应活过工具调用 ctx 取消并 completed；got=%q", last)
	}
}

// 判据 B（保 owner 收杀语义，承接 H1-2 意图）：会话关闭（Executor.Close → 会话级 ctx）
// 必须能杀死仍在运行的 fork —— 寿命归 owner，不是墙钟。
func TestW065_Fork_KilledBySessionClose(t *testing.T) {
	e := newReadbackExecutor(t)
	mc := &w065BlockingClient{release: make(chan struct{})}
	e.AIClient = mc
	defer close(mc.release) // 收尾放行，保证 mock goroutine 退出（夹具纪律）

	forkID := launchW065Fork(t, e, context.Background())

	e.Close() // 生产退出收尾（main.go defer runner.Close() → runnerCore.Close → Executor.Close）

	last := w065PollForkStatus(t, e, forkID, "failed",
		"超时：Executor.Close 后 fork 未进入终态（owner 收杀未生效？）")
	if !strings.Contains(last, "failed") {
		t.Fatalf("Close 后 fork 应回 failed 终态；got=%q", last)
	}
}
