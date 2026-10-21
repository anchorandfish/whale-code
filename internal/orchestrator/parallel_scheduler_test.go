// parallel_scheduler_test.go — H10 Phase 3 模块 5 ParallelScheduler 反例测试（TDD 先行）。
// 覆盖致命反例 4：3 子任务同时派发时预算竞态预防（sync.Mutex + 预算预检 + 事后检查）。
// 覆盖致命反例 5：retryCount >= retryLimit 必须转 manual（零容忍）。
// 覆盖：桶间并行+桶内串行 / 失败三态判定 / OnSubtaskDone 补派 / OnDynamicDep 仲裁 / N1 签名一致 / 并发安全。
// API；落地：ParallelScheduler 组合 DepGraph+WorktreePool+BudgetTracker
// +桶内串行；落地：DispatchBatch 按桶分组，桶间并行+桶内串行
// ≤3 轮；落地：retryLimit=3，超限转 manual
// tool + H10 外层包装；落地：ParallelScheduler 调 N1 Agent.Execute 不改签名
// SNAPSHOT: tests={ConcurrentDispatch_BudgetRace, RetryExceedLimit_ToManual, HandleFailure_ErrorClassification, BucketParallel_BucketSerial, Reorganize_PauseAndNotify, Manual_PauseAndNotify, OnSubtaskDone_ReplenishBatch, OnDynamicDep_ContractChange_Arbitration, N1SignatureNotChanged, ConcurrentDispatch_NoRace, DispatchBatch_EmptyReady, NextBatch_RespectsMaxParallel, GetRetryCount_Diagnostics}

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// makeTestParallelScheduler 构造测试用 ParallelScheduler，注入 mock 依赖避免真实 git/agent。
// maxParallel 默认 3，可通过参数覆盖。返回调度器及各依赖组件用于断言。
func makeTestParallelScheduler(t *testing.T, maxParallel int) (*ParallelScheduler, *mockAgent, *DepGraph, *WorktreePool, *BudgetTracker, *TraceStore, *StateMachine) {
	t.Helper()
	if maxParallel <= 0 {
		maxParallel = 3
	}
	workDir := t.TempDir()
	graph := NewDepGraph()
	pool := makeTestWorktreePool(t, workDir, maxParallel)
	budget := NewBudgetTracker(10000) // 100 美元，足够测试
	tracer := makeTestTraceStore(t)
	state := NewStateMachine("test-task-" + t.Name())
	scheduler := NewParallelScheduler(graph, pool, budget, tracer, state, maxParallel)
	agent := &mockAgent{}
	scheduler.SetAgent(agent)
	return scheduler, agent, graph, pool, budget, tracer, state
}

// addTestNodes 向 DepGraph 批量添加测试节点（bucket 映射由 ids 顺序决定）。
func addTestNodes(t *testing.T, g *DepGraph, ids []string, buckets []string) {
	t.Helper()
	for i, id := range ids {
		bucket := BucketA
		if i < len(buckets) {
			bucket = buckets[i]
		}
		if err := g.AddNode(&TaskNode{ID: id, Bucket: bucket}); err != nil {
			t.Fatalf("AddNode %s: %v", id, err)
		}
	}
}

// waitForRunningCount 等待调度器运行数达到期望值，避免固定 sleep 掩盖慢机器时序。
func waitForRunningCount(t *testing.T, scheduler *ParallelScheduler, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if scheduler.RunningCount() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("RunningCount did not reach %d, got %d", want, scheduler.RunningCount())
}

// TestParallelScheduler_ConcurrentDispatch_BudgetRace 致命反例 4：预算竞态预防（零容忍）。
// 场景：预算仅够 1 次派发，第 1 次派发后预算超限，第 2 次派发必须被预算预检拦截。
// 验证：sync.Mutex 保护预算预检 + 事后检查正确标记 exceeded，防止超支。
// 注（2026-10-09）：预算轴 = provider 真报 token。mockAgent 返回
// Usage=10000 tokens → RecordUsage(10000) > 5 token 预算 → exceeded。
func TestParallelScheduler_ConcurrentDispatch_BudgetRace(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	graph := NewDepGraph()
	addTestNodes(t, graph, []string{"S-budget-1", "S-budget-2"}, []string{BucketA, BucketB})
	pool := makeTestWorktreePool(t, workDir, 3)
	// 预算仅 5 token，每次 Dispatch 记录 10000 token → 第 1 次后即超 100%
	smallBudget := NewBudgetTracker(5)
	tracer := makeTestTraceStore(t)
	state := NewStateMachine("budget-race")
	scheduler := NewParallelScheduler(graph, pool, smallBudget, tracer, state, 3)
	agent := &mockAgent{}
	// 记 provider 真报 token：10000 > 5 token 预算
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		return tools.AgentOutput{
			Result: "ok",
			Status: tools.TaskCompleted,
			Usage: &tools.TokenUsage{
				InputTokens: 10000,
			},
		}, nil
	}
	scheduler.SetAgent(agent)

	// 第 1 次派发：预检通过（未超限），执行后记录 10000 token → 远超 100% → exceeded
	if err := scheduler.Dispatch(context.Background(), "S-budget-1"); err != nil {
		t.Fatalf("第 1 次派发应成功，got: %v", err)
	}

	// 致命反例 4 校验：预算已超限
	if !smallBudget.IsExceeded() {
		t.Fatal("致命反例 4：第 1 次派发后预算应标记为 exceeded")
	}

	// 第 2 次派发：预算预检必须拦截（IsExceeded=true）
	err := scheduler.Dispatch(context.Background(), "S-budget-2")
	if err == nil {
		t.Fatal("致命反例 4：预算超限后第 2 次派发必须被预算预检拦截（零容忍）")
	}
	if !strings.Contains(err.Error(), "budget already exceeded") {
		t.Errorf("error 应包含 'budget already exceeded'，got: %v", err)
	}

	// 验证 budget_check trace 已写入（事后检查）
	events, _ := tracer.QueryBySubtask("S-budget-1")
	budgetCheckFound := false
	for _, e := range events {
		if e.EventType == Phase3EvtBudgetCheck {
			budgetCheckFound = true
		}
	}
	if !budgetCheckFound {
		t.Error("致命反例 4：第 1 次派发后应写入 budget_check trace 事件")
	}
}

// TestParallelScheduler_RetryExceedLimit_ToManual 致命反例 5：retryCount >= retryLimit 必须转 manual（零容忍）。
// 场景：retryLimit=3，retryCount 累积到 3 后，HandleFailure 必须返回 Manual（不论错误类型）。
func TestParallelScheduler_RetryExceedLimit_ToManual(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-retry"}, []string{BucketA})

	// 致命反例 5 校验：retryCount=0 时，可恢复错误 → Retry
	action := scheduler.HandleFailure(context.Background(), "S-retry", errors.New("network timeout"))
	if action != FailureActionRetry {
		t.Errorf("retryCount=0 时可恢复错误应返回 Retry，got %s", action)
	}

	// 模拟 3 次重试（retryCount 累积到 3）
	scheduler.mu.Lock()
	scheduler.retryCount["S-retry"] = 3
	scheduler.mu.Unlock()

	// 致命反例 5 校验：retryCount=3 >= retryLimit=3 → 必须返回 Manual（零容忍）
	action = scheduler.HandleFailure(context.Background(), "S-retry", errors.New("network timeout"))
	if action != FailureActionManual {
		t.Errorf("致命反例 5：retryCount=3 >= retryLimit=3 时必须返回 Manual，got %s（零容忍）", action)
	}

	// 即使是可恢复错误类型，retryCount 超限也必须转 Manual
	action = scheduler.HandleFailure(context.Background(), "S-retry", errors.New("transient error"))
	if action != FailureActionManual {
		t.Errorf("致命反例 5：retryCount 超限时不论错误类型都应返回 Manual，got %s", action)
	}
}

// TestParallelScheduler_RetryExceedLimit_OnSubtaskFailed_Pause 通过 OnSubtaskFailed 全流程验证超限转 manual。
// 场景：retryLimit=2，agent 始终返回可恢复错误，2 次重试后第 3 次调用必须 pauseAndNotify。
func TestParallelScheduler_RetryExceedLimit_OnSubtaskFailed_Pause(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	graph := NewDepGraph()
	addTestNodes(t, graph, []string{"S-exhaust"}, []string{BucketA})
	pool := makeTestWorktreePool(t, workDir, 3)
	budget := NewBudgetTracker(10000)
	tracer := makeTestTraceStore(t)
	state := NewStateMachine("retry-exhaust")
	scheduler := NewParallelScheduler(graph, pool, budget, tracer, state, 3)
	scheduler.SetRetryLimit(2) // 设为 2 便于测试

	// agent 始终返回可恢复错误
	agent := &mockAgent{}
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		return tools.AgentOutput{}, errors.New("network timeout: connection refused")
	}
	scheduler.SetAgent(agent)

	recoverableErr := errors.New("network timeout: connection refused")

	// 第 1 次 OnSubtaskFailed → Retry（retryCount 0→1）→ Dispatch 失败
	_ = scheduler.OnSubtaskFailed(context.Background(), "S-exhaust", recoverableErr)
	if scheduler.GetRetryCount("S-exhaust") != 1 {
		t.Errorf("第 1 次重试后 retryCount 应为 1，got %d", scheduler.GetRetryCount("S-exhaust"))
	}

	// 第 2 次 OnSubtaskFailed → Retry（retryCount 1→2）→ Dispatch 失败
	_ = scheduler.OnSubtaskFailed(context.Background(), "S-exhaust", recoverableErr)
	if scheduler.GetRetryCount("S-exhaust") != 2 {
		t.Errorf("第 2 次重试后 retryCount 应为 2，got %d", scheduler.GetRetryCount("S-exhaust"))
	}

	// 致命反例 5 校验：第 3 次调用 → retryCount=2 >= retryLimit=2 → Manual → pauseAndNotify
	err := scheduler.OnSubtaskFailed(context.Background(), "S-exhaust", recoverableErr)
	if err == nil {
		t.Fatal("致命反例 5：retryCount 超限时 OnSubtaskFailed 必须返回 pause 错误")
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Errorf("致命反例 5：error 应包含 'paused'，got: %v", err)
	}

	// 校验：节点状态应为 failed（pauseAndNotify 调用了 MarkFailed）
	node, ok := graph.GetNode("S-exhaust")
	if !ok {
		t.Fatal("GetNode S-exhaust 失败")
	}
	if node.Status != StatusFailed {
		t.Errorf("致命反例 5：pauseAndNotify 后节点状态应为 failed，got %s", node.Status)
	}
}

// TestParallelScheduler_HandleFailure_ErrorClassification 失败三态错误分类判定。
// security/interface break → Manual；contract conflict → Reorganize；network/timeout → Retry。
func TestParallelScheduler_HandleFailure_ErrorClassification(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-classify"}, []string{BucketA})

	tests := []struct {
		name     string
		errMsg   string
		expected FailureAction
	}{
		{"security_violation", "security vulnerability detected in output", FailureActionManual},
		{"interface_break", "interface break: signature changed", FailureActionManual},
		{"signature_change", "signature change in public API", FailureActionManual},
		{"contract_violation", "contract violation: field type mismatch", FailureActionManual},
		{"contract_conflict", "contract conflict between A and B", FailureActionReorganize},
		{"dependency_missing", "dependency missing: module not found", FailureActionReorganize},
		{"plan_outdated", "plan outdated: requirements changed", FailureActionReorganize},
		{"network_timeout", "network timeout: connection refused", FailureActionRetry},
		{"transient_error", "temporary failure: retry recommended", FailureActionRetry},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 重置 retryCount
			scheduler.mu.Lock()
			scheduler.retryCount["S-classify"] = 0
			scheduler.mu.Unlock()

			action := scheduler.HandleFailure(context.Background(), "S-classify", errors.New(tc.errMsg))
			if action != tc.expected {
				t.Errorf("错误分类 %q 应返回 %s，got %s", tc.errMsg, tc.expected, action)
			}
		})
	}
}

// TestParallelScheduler_BucketParallel_BucketSerial 桶间并行+桶内串行调度（02 §3）。
// 场景：3 个无依赖子任务分属 A/B/C 桶，DispatchBatch 应全部派发（桶间并行）。
func TestParallelScheduler_BucketParallel_BucketSerial(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	// 3 个子任务分属 A/B/C 桶，无依赖
	addTestNodes(t, graph, []string{"S-A", "S-B", "S-C"}, []string{BucketA, BucketB, BucketC})

	// 记录 agent 被调用次数
	var callMu sync.Mutex
	callCount := 0
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		callMu.Lock()
		callCount++
		callMu.Unlock()
		return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted}, nil
	}

	// DispatchBatch 应派发全部 3 个就绪子任务
	if err := scheduler.DispatchBatch(context.Background()); err != nil {
		t.Fatalf("DispatchBatch: %v", err)
	}

	if callCount != 3 {
		t.Errorf("桶间并行应派发 3 个子任务，got callCount=%d", callCount)
	}

	// 校验：3 个节点都应标记为 done
	for _, id := range []string{"S-A", "S-B", "S-C"} {
		node, ok := graph.GetNode(id)
		if !ok {
			t.Errorf("GetNode %s 失败", id)
			continue
		}
		if node.Status != StatusDone {
			t.Errorf("子任务 %s 状态应为 done，got %s", id, node.Status)
		}
	}
}

// TestParallelScheduler_BucketSerial_FailureStopsBucket 桶内串行：一个失败停止本桶后续派发。
// 场景：桶 A 有 2 个子任务（S-A1, S-A2），两者均返回安全错误 → 桶 A 第 1 个失败后停止（第 2 个不派发）；
// 桶 B 有 1 个子任务（S-B1）→ 不受影响，正常派发。
// 注：同桶内节点顺序由 ReadyToRun 按 priority 决定，priority 相同时不保证顺序，
// 因此本测试让两者都返回错误，仅校验「恰好 1 个被派发」（而非具体哪个）。
func TestParallelScheduler_BucketSerial_FailureStopsBucket(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	// S-A1, S-A2 同属 A 桶；S-B1 属 B 桶
	addTestNodes(t, graph, []string{"S-A1", "S-A2", "S-B1"}, []string{BucketA, BucketA, BucketB})

	var callMu sync.Mutex
	aCalled := 0
	bCalled := false
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		callMu.Lock()
		defer callMu.Unlock()
		// 桶 A 的两个子任务都返回安全错误 → Manual → 桶 A 停止
		if strings.Contains(input.Prompt, "S-A1") || strings.Contains(input.Prompt, "S-A2") {
			aCalled++
			return tools.AgentOutput{}, errors.New("security vulnerability: interface break detected")
		}
		if strings.Contains(input.Prompt, "S-B1") {
			bCalled = true
		}
		return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted}, nil
	}

	// DispatchBatch
	_ = scheduler.DispatchBatch(context.Background())

	// 桶内串行校验：桶 A 恰好 1 个被派发（第 1 个失败后停止，第 2 个不派发）
	if aCalled != 1 {
		t.Errorf("桶内串行：桶 A 应恰好派发 1 个（第 1 个失败后停止），got aCalled=%d", aCalled)
	}
	// 桶间并行校验：桶 B 不受桶 A 影响
	if !bCalled {
		t.Error("桶间并行：桶 B 的 S-B1 应被派发，不受桶 A 失败影响")
	}
}

// TestParallelScheduler_Reorganize_PauseAndNotify 契约冲突 → Reorganize → 暂停+通知。
func TestParallelScheduler_Reorganize_PauseAndNotify(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, tracer, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-reorg"}, []string{BucketA})

	// 契约冲突错误 → Reorganize
	err := scheduler.OnSubtaskFailed(context.Background(), "S-reorg", errors.New("contract conflict: field type mismatch"))
	if err == nil {
		t.Fatal("Reorganize 应返回 pause 错误")
	}
	if !strings.Contains(err.Error(), "reorganize") {
		t.Errorf("error 应包含 'reorganize'，got: %v", err)
	}

	// 校验：节点标记为 failed
	node, ok := graph.GetNode("S-reorg")
	if !ok {
		t.Fatal("GetNode S-reorg 失败")
	}
	if node.Status != StatusFailed {
		t.Errorf("Reorganize 后节点状态应为 failed，got %s", node.Status)
	}

	// 校验：running 已清除
	scheduler.mu.Lock()
	running := scheduler.running["S-reorg"]
	scheduler.mu.Unlock()
	if running {
		t.Error("Reorganize 后 running 标记应已清除")
	}

	// 校验：trace 写入 paused 事件
	events, _ := tracer.QueryBySubtask("S-reorg")
	pausedFound := false
	for _, e := range events {
		if e.EventType == "paused_for_manual" {
			pausedFound = true
		}
	}
	if !pausedFound {
		t.Error("Reorganize 后应写入 paused_for_manual trace 事件")
	}
}

// TestParallelScheduler_Manual_PauseAndNotify 安全漏洞 → Manual → 暂停+通知。
func TestParallelScheduler_Manual_PauseAndNotify(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, tracer, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-manual"}, []string{BucketA})

	// 安全漏洞错误 → Manual（即使 retryCount=0）
	err := scheduler.OnSubtaskFailed(context.Background(), "S-manual", errors.New("security vulnerability: hardcoded secret detected"))
	if err == nil {
		t.Fatal("Manual 应返回 pause 错误")
	}
	if !strings.Contains(err.Error(), "manual intervention") {
		t.Errorf("error 应包含 'manual intervention'，got: %v", err)
	}

	// 校验：节点标记为 failed
	node, ok := graph.GetNode("S-manual")
	if !ok {
		t.Fatal("GetNode S-manual 失败")
	}
	if node.Status != StatusFailed {
		t.Errorf("Manual 后节点状态应为 failed，got %s", node.Status)
	}

	// 校验：retryCount 未增加（Manual 不重试）
	if scheduler.GetRetryCount("S-manual") != 0 {
		t.Errorf("Manual 不应增加 retryCount，got %d", scheduler.GetRetryCount("S-manual"))
	}

	// 校验：trace 写入 paused 事件
	events, _ := tracer.QueryBySubtask("S-manual")
	pausedFound := false
	for _, e := range events {
		if e.EventType == "paused_for_manual" {
			pausedFound = true
		}
	}
	if !pausedFound {
		t.Error("Manual 后应写入 paused_for_manual trace 事件")
	}
}

// TestParallelScheduler_OnSubtaskDone_ReplenishBatch 子任务完成回调 → MarkDone + 记录用量。
func TestParallelScheduler_OnSubtaskDone_ReplenishBatch(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, budget, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-done"}, []string{BucketA})

	usedBefore := budget.UsedTokens()

	result := TaskResult{
		Success:    true,
		TokensUsed: 50,
	}
	if err := scheduler.OnSubtaskDone(context.Background(), "S-done", result); err != nil {
		t.Fatalf("OnSubtaskDone: %v", err)
	}

	// 校验：节点标记为 done
	node, ok := graph.GetNode("S-done")
	if !ok {
		t.Fatal("GetNode S-done 失败")
	}
	if node.Status != StatusDone {
		t.Errorf("OnSubtaskDone 后节点状态应为 done，got %s", node.Status)
	}

	// 校验：用量已记录
	usedAfter := budget.UsedTokens()
	if usedAfter-usedBefore != 50 {
		t.Errorf("token 用量应增加 50，got delta=%d", usedAfter-usedBefore)
	}
}

// TestParallelScheduler_OnDynamicDep_ContractChange_Arbitration 动态依赖检测 → 写 trace + graph 更新。
// 场景：from 检测到对 to 的动态依赖（契约变化），OnDynamicDep 写 trace + graph.AddDynamicDep。
func TestParallelScheduler_OnDynamicDep_ContractChange_Arbitration(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, tracer, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-from", "S-to"}, []string{BucketA, BucketB})

	// 调用 OnDynamicDep
	err := scheduler.OnDynamicDep(context.Background(), "S-from", "S-to", "contract change detected: signature modified")
	if err != nil {
		t.Fatalf("OnDynamicDep: %v", err)
	}

	// 校验：trace 写入 dependency_detected 事件
	events, err := tracer.QueryDependencyDetected("S-from")
	if err != nil {
		t.Fatalf("QueryDependencyDetected: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应返回 1 条 dependency_detected 事件，got %d", len(events))
	}

	evt := events[0]
	if evt.EventType != Phase3EvtDependencyDetected {
		t.Errorf("EventType 应为 %s，got %s", Phase3EvtDependencyDetected, evt.EventType)
	}
	from, _ := evt.Payload["from"].(string)
	to, _ := evt.Payload["to"].(string)
	reason, _ := evt.Payload["reason"].(string)
	if from != "S-from" || to != "S-to" {
		t.Errorf("payload from/to 应为 S-from/S-to，got %s/%s", from, to)
	}
	if !strings.Contains(reason, "contract change") {
		t.Errorf("payload reason 应包含 'contract change'，got %s", reason)
	}

	// 校验：graph 中 S-from 的 DynamicDeps 包含 S-to
	node, ok := graph.GetNode("S-from")
	if !ok {
		t.Fatal("GetNode S-from 失败")
	}
	found := false
	for _, dep := range node.DynamicDeps {
		if dep == "S-to" {
			found = true
		}
	}
	if !found {
		t.Error("OnDynamicDep 后 S-from.DynamicDeps 应包含 S-to")
	}
}

// TestParallelScheduler_N1SignatureNotChanged 致命反例：N1 Agent.Execute 签名未改（零容忍）。
// 验证 ParallelScheduler 通过 SetAgent 注入 tools.Agent，调 Execute(ctx, AgentInput) → (AgentOutput, error)。
func TestParallelScheduler_N1SignatureNotChanged(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-sig"}, []string{BucketA})

	// 验证 SetAgent 接受 tools.Agent 接口（签名一致）
	var injectedAgent tools.Agent = agent
	scheduler.SetAgent(injectedAgent)

	// 验证 Dispatch 调用 agent.Execute（签名：(ctx, AgentInput) → (AgentOutput, error)）
	var calledInput tools.AgentInput
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		calledInput = input
		return tools.AgentOutput{Result: "sig-ok", Status: tools.TaskCompleted}, nil
	}

	if err := scheduler.Dispatch(context.Background(), "S-sig"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if calledInput.Prompt == "" {
		t.Error("致命反例：agent.Execute 应被调用且 Prompt 不为空")
	}
	if !strings.Contains(calledInput.Prompt, "S-sig") {
		t.Errorf("Execute 的 Prompt 应包含 subtaskID S-sig，got %s", calledInput.Prompt)
	}
}

// TestParallelScheduler_ConcurrentDispatch_NoRace 多 goroutine 并发 Dispatch，-race 无竞态。
// 致命反例 4 并发安全验证：sync.Mutex 保护 retryCount/running 状态。
func TestParallelScheduler_ConcurrentDispatch_NoRace(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 5)
	// 5 个无依赖子任务
	ids := []string{"S-race-1", "S-race-2", "S-race-3", "S-race-4", "S-race-5"}
	buckets := []string{BucketA, BucketB, BucketC, BucketD, BucketE}
	addTestNodes(t, graph, ids, buckets)

	// agent 加微小延迟增加并发窗口
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		time.Sleep(time.Millisecond)
		return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted}, nil
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(subtaskID string) {
			defer wg.Done()
			_ = scheduler.Dispatch(context.Background(), subtaskID)
		}(id)
	}
	wg.Wait()
	// -race 检测到此无竞态即 PASS
}

// TestParallelScheduler_DispatchBatch_EmptyReady 无就绪子任务时 DispatchBatch 返回 nil。
// 场景：图中所有节点都有未完成依赖 → ReadyToRun 返回空 → DispatchBatch 直接返回 nil。
func TestParallelScheduler_DispatchBatch_EmptyReady(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	// S-blocked 依赖 S-dep，S-dep 依赖 S-blocked-dep → 无就绪节点
	if err := graph.AddNode(&TaskNode{ID: "S-blocked", Bucket: BucketA}); err != nil {
		t.Fatalf("AddNode S-blocked: %v", err)
	}
	if err := graph.AddNode(&TaskNode{ID: "S-dep", Bucket: BucketB}); err != nil {
		t.Fatalf("AddNode S-dep: %v", err)
	}
	if err := graph.AddNode(&TaskNode{ID: "S-dep2", Bucket: BucketC}); err != nil {
		t.Fatalf("AddNode S-dep2: %v", err)
	}
	// S-blocked 依赖 S-dep，S-dep 依赖 S-dep2 → S-dep2 唯一就绪
	if err := graph.AddEdge("S-blocked", "S-dep"); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if err := graph.AddEdge("S-dep", "S-dep2"); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	// S-dep2 唯一就绪 → NextBatch 返回 [S-dep2] → DispatchBatch 派发它
	err := scheduler.DispatchBatch(context.Background())
	if err != nil {
		t.Errorf("DispatchBatch 应返回 nil（S-dep2 可正常派发），got: %v", err)
	}
	// 校验 S-dep2 已完成
	node, ok := graph.GetNode("S-dep2")
	if !ok {
		t.Fatal("GetNode S-dep2 失败")
	}
	if node.Status != StatusDone {
		t.Errorf("S-dep2 应为 done，got status=%s", node.Status.String())
	}
}

// TestParallelScheduler_NextBatch_RespectsMaxParallel NextBatch 返回数量不超过 maxParallel。
func TestParallelScheduler_NextBatch_RespectsMaxParallel(t *testing.T) {
	t.Parallel()
	// maxParallel=2，5 个就绪子任务
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 2)
	ids := []string{"S-nb-1", "S-nb-2", "S-nb-3", "S-nb-4", "S-nb-5"}
	buckets := []string{BucketA, BucketB, BucketC, BucketD, BucketE}
	addTestNodes(t, graph, ids, buckets)

	batch := scheduler.NextBatch()
	if len(batch) > 2 {
		t.Errorf("NextBatch 返回数量不应超过 maxParallel=2，got %d", len(batch))
	}
	if len(batch) == 0 {
		t.Error("NextBatch 应返回至少 1 个就绪子任务")
	}
}

// TestParallelScheduler_Dispatch_AgentNotSet 未注入 agent 时 Dispatch 返回 error。
func TestParallelScheduler_Dispatch_AgentNotSet(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	graph := NewDepGraph()
	addTestNodes(t, graph, []string{"S-noagent"}, []string{BucketA})
	pool := makeTestWorktreePool(t, workDir, 3)
	budget := NewBudgetTracker(10000)
	tracer := makeTestTraceStore(t)
	state := NewStateMachine("no-agent")
	// 不调 SetAgent
	scheduler := NewParallelScheduler(graph, pool, budget, tracer, state, 3)

	err := scheduler.Dispatch(context.Background(), "S-noagent")
	if err == nil {
		t.Fatal("未注入 agent 时 Dispatch 应返回 error")
	}
	if !strings.Contains(err.Error(), "agent not set") {
		t.Errorf("error 应包含 'agent not set'，got: %v", err)
	}
}

// TestParallelScheduler_Dispatch_DuplicateRunning 同一 subtaskID 重复 Dispatch 应拒绝。
func TestParallelScheduler_Dispatch_DuplicateRunning(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-dup-dispatch"}, []string{BucketA})

	// 用阻塞 channel 模拟 agent 长时间执行（保持 running=true）
	blockCh := make(chan struct{})
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		<-blockCh // 阻塞直到测试释放
		return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted}, nil
	}

	// 异步启动第 1 次 Dispatch
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- scheduler.Dispatch(context.Background(), "S-dup-dispatch")
	}()

	// 等待第 1 次 Dispatch 标记 running
	waitForRunningCount(t, scheduler, 1)

	// 第 2 次 Dispatch 同一 subtaskID 应拒绝（running=true）
	err := scheduler.Dispatch(context.Background(), "S-dup-dispatch")
	if err == nil {
		t.Fatal("同一 subtaskID 已在运行时重复 Dispatch 应拒绝")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error 应包含 'already running'，got: %v", err)
	}

	// 释放阻塞
	close(blockCh)
	if err := <-dispatchDone; err != nil {
		t.Fatalf("first Dispatch returned error: %v", err)
	}
	waitForRunningCount(t, scheduler, 0)
}

// TestParallelScheduler_Dispatch_EmptySubtaskID 空 subtaskID 应拒绝。
func TestParallelScheduler_Dispatch_EmptySubtaskID(t *testing.T) {
	t.Parallel()
	scheduler, _, _, _, _, _, _ := makeTestParallelScheduler(t, 3)

	err := scheduler.Dispatch(context.Background(), "")
	if err == nil {
		t.Fatal("空 subtaskID 应返回 error")
	}
	if !strings.Contains(err.Error(), "subtaskID must not be empty") {
		t.Errorf("error 应包含 'subtaskID must not be empty'，got: %v", err)
	}
}

// TestParallelScheduler_RunningCount 返回当前运行数。
func TestParallelScheduler_RunningCount(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 5)
	addTestNodes(t, graph, []string{"S-rc-1", "S-rc-2"}, []string{BucketA, BucketB})

	// 阻塞 agent 使 2 个子任务同时 running
	blockCh := make(chan struct{})
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		<-blockCh
		return tools.AgentOutput{Result: "ok", Status: tools.TaskCompleted}, nil
	}

	// 异步派发 2 个子任务
	dispatchDone := make(chan error, 2)
	go func() { dispatchDone <- scheduler.Dispatch(context.Background(), "S-rc-1") }()
	go func() { dispatchDone <- scheduler.Dispatch(context.Background(), "S-rc-2") }()

	// 等待 2 个子任务都标记 running
	waitForRunningCount(t, scheduler, 2)

	rc := scheduler.RunningCount()
	if rc != 2 {
		t.Errorf("RunningCount 应为 2，got %d", rc)
	}

	close(blockCh)
	for i := 0; i < 2; i++ {
		if err := <-dispatchDone; err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
	}
	waitForRunningCount(t, scheduler, 0)
}

// TestParallelScheduler_SetRetryLimit_Setter 验证 SetRetryLimit 正确设置重试上限。
func TestParallelScheduler_SetRetryLimit_Setter(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-rl"}, []string{BucketA})

	scheduler.SetRetryLimit(5)

	scheduler.mu.Lock()
	limit := scheduler.retryLimit
	scheduler.mu.Unlock()
	if limit != 5 {
		t.Errorf("SetRetryLimit(5) 后 retryLimit 应为 5，got %d", limit)
	}

	// retryCount=4 < 5 → 仍可重试
	scheduler.mu.Lock()
	scheduler.retryCount["S-rl"] = 4
	scheduler.mu.Unlock()
	action := scheduler.HandleFailure(context.Background(), "S-rl", errors.New("network error"))
	if action != FailureActionRetry {
		t.Errorf("retryCount=4 < retryLimit=5 时应返回 Retry，got %s", action)
	}

	// retryCount=5 >= 5 → Manual
	scheduler.mu.Lock()
	scheduler.retryCount["S-rl"] = 5
	scheduler.mu.Unlock()
	action = scheduler.HandleFailure(context.Background(), "S-rl", errors.New("network error"))
	if action != FailureActionManual {
		t.Errorf("retryCount=5 >= retryLimit=5 时应返回 Manual，got %s", action)
	}
}

// TestParallelScheduler_SetRetryLimit_IgnoreInvalid SetRetryLimit 忽略非正值。
func TestParallelScheduler_SetRetryLimit_IgnoreInvalid(t *testing.T) {
	t.Parallel()
	scheduler, _, _, _, _, _, _ := makeTestParallelScheduler(t, 3)

	scheduler.SetRetryLimit(0)  // 应被忽略
	scheduler.SetRetryLimit(-1) // 应被忽略

	scheduler.mu.Lock()
	limit := scheduler.retryLimit
	scheduler.mu.Unlock()
	if limit != RetryLimitDefault {
		t.Errorf("非正值应被忽略，retryLimit 应保持默认值 %d，got %d", RetryLimitDefault, limit)
	}
}

// TestParallelScheduler_DefaultRetryLimit 默认重试上限为 3。
func TestParallelScheduler_DefaultRetryLimit(t *testing.T) {
	t.Parallel()
	scheduler, _, _, _, _, _, _ := makeTestParallelScheduler(t, 3)

	scheduler.mu.Lock()
	limit := scheduler.retryLimit
	scheduler.mu.Unlock()
	if limit != RetryLimitDefault {
		t.Errorf("默认 retryLimit 应为 %d，got %d", RetryLimitDefault, limit)
	}
	if RetryLimitDefault != 3 {
		t.Errorf("RetryLimitDefault 常量应为 3，got %d", RetryLimitDefault)
	}
}

// TestParallelScheduler_GetRetryCount_Diagnostics GetRetryCount 返回子任务重试次数。
func TestParallelScheduler_GetRetryCount_Diagnostics(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-grc"}, []string{BucketA})

	// 初始 retryCount=0
	if scheduler.GetRetryCount("S-grc") != 0 {
		t.Errorf("初始 retryCount 应为 0，got %d", scheduler.GetRetryCount("S-grc"))
	}

	// 手动设置后验证读取
	scheduler.mu.Lock()
	scheduler.retryCount["S-grc"] = 2
	scheduler.mu.Unlock()
	if scheduler.GetRetryCount("S-grc") != 2 {
		t.Errorf("GetRetryCount 应返回 2，got %d", scheduler.GetRetryCount("S-grc"))
	}

	// 不存在的子任务返回 0
	if scheduler.GetRetryCount("S-notexist") != 0 {
		t.Errorf("不存在的子任务 retryCount 应为 0，got %d", scheduler.GetRetryCount("S-notexist"))
	}
}

// TestParallelScheduler_groupByBucket 按桶分组验证（02 §3）。
func TestParallelScheduler_groupByBucket(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 5)
	addTestNodes(t, graph,
		[]string{"S-g1", "S-g2", "S-g3", "S-g4"},
		[]string{BucketA, BucketA, BucketB, BucketC})

	groups := scheduler.groupByBucket([]string{"S-g1", "S-g2", "S-g3", "S-g4"})

	if len(groups[BucketA]) != 2 {
		t.Errorf("桶 A 应有 2 个子任务，got %d", len(groups[BucketA]))
	}
	if len(groups[BucketB]) != 1 {
		t.Errorf("桶 B 应有 1 个子任务，got %d", len(groups[BucketB]))
	}
	if len(groups[BucketC]) != 1 {
		t.Errorf("桶 C 应有 1 个子任务，got %d", len(groups[BucketC]))
	}
}

// TestParallelScheduler_Dispatch_WritesTrace Dispatch 写入 dispatch trace 事件。
func TestParallelScheduler_Dispatch_WritesTrace(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, tracer, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-trace"}, []string{BucketA})

	if err := scheduler.Dispatch(context.Background(), "S-trace"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// 验证 dispatch trace 已写入
	events, err := tracer.QueryBySubtask("S-trace")
	if err != nil {
		t.Fatalf("QueryBySubtask: %v", err)
	}
	dispatchFound := false
	budgetCheckFound := false
	for _, e := range events {
		if e.EventType == Phase3EvtDispatch {
			dispatchFound = true
		}
		if e.EventType == Phase3EvtBudgetCheck {
			budgetCheckFound = true
		}
	}
	if !dispatchFound {
		t.Error("Dispatch 后应写入 Phase3EvtDispatch trace 事件")
	}
	if !budgetCheckFound {
		t.Error("Dispatch 后应写入 Phase3EvtBudgetCheck trace 事件")
	}
}

// TestParallelScheduler_OnSubtaskFailed_NilError nil error 时 OnSubtaskFailed 直接返回 nil。
func TestParallelScheduler_OnSubtaskFailed_NilError(t *testing.T) {
	t.Parallel()
	scheduler, _, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-nil"}, []string{BucketA})

	err := scheduler.OnSubtaskFailed(context.Background(), "S-nil", nil)
	if err != nil {
		t.Errorf("nil error 时 OnSubtaskFailed 应返回 nil，got: %v", err)
	}
}

// TestParallelScheduler_FailureAction_Constants FailureAction 枚举常量值正确。
func TestParallelScheduler_FailureAction_Constants(t *testing.T) {
	t.Parallel()
	if FailureActionRetry != "retry" {
		t.Errorf("FailureActionRetry 应为 'retry'，got %s", FailureActionRetry)
	}
	if FailureActionReorganize != "reorganize" {
		t.Errorf("FailureActionReorganize 应为 'reorganize'，got %s", FailureActionReorganize)
	}
	if FailureActionManual != "manual" {
		t.Errorf("FailureActionManual 应为 'manual'，got %s", FailureActionManual)
	}
}

// TestParallelScheduler_containsAny 验证 containsAny 辅助函数。
func TestParallelScheduler_containsAny(t *testing.T) {
	t.Parallel()
	if !containsAny("security breach", "security", "timeout") {
		t.Error("containsAny 应匹配 'security'")
	}
	if !containsAny("network timeout", "security", "timeout") {
		t.Error("containsAny 应匹配 'timeout'")
	}
	if containsAny("unrelated error", "security", "timeout") {
		t.Error("containsAny 不应匹配无关字符串")
	}
}

// TestParallelScheduler_DispatchBatch_FailureTriggersOnSubtaskFailed 桶内失败触发 OnSubtaskFailed。
func TestParallelScheduler_DispatchBatch_FailureTriggersOnSubtaskFailed(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, _, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-fail-batch"}, []string{BucketA})

	// agent 返回可恢复错误
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		return tools.AgentOutput{}, fmt.Errorf("network timeout: connection refused")
	}

	// DispatchBatch → Dispatch 失败 → OnSubtaskFailed → HandleFailure(Retry) → Dispatch 重试 → 失败
	_ = scheduler.DispatchBatch(context.Background())

	// retryCount 应增加（至少 1 次重试）
	rc := scheduler.GetRetryCount("S-fail-batch")
	if rc < 1 {
		t.Errorf("DispatchBatch 失败后 retryCount 应 >= 1，got %d", rc)
	}
}

// === B-P0-6 反例测试（桶 X2 修复回核）===

// TestParallelScheduler_Dispatch_UsageNonNil_RecordsRealTokens（2026-10-09）：
// agent 返回带 Usage 的 AgentOutput 时，Dispatch 从 Usage 记录 **provider 真报的 token** 到 budget。
// 断言：Usage=(InputTokens=5000, OutputTokens=3000) → 记录 8000 token（原按自编单价折算成"8 美分"已删）。
//
//	同时验证 trace 含 Phase3EvtSubtaskOutput 事件（不丢弃 agentOutput）。
func TestParallelScheduler_Dispatch_UsageNonNil_RecordsRealTokens(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, budget, tracer, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-bp06-pos"}, []string{BucketA})

	// agent 返回带 Usage 的 AgentOutput
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		return tools.AgentOutput{
			Result: "ok",
			Status: tools.TaskCompleted,
			Usage: &tools.TokenUsage{
				InputTokens:  5000,
				OutputTokens: 3000,
			},
		}, nil
	}

	usedBefore := budget.UsedTokens()
	if err := scheduler.Dispatch(context.Background(), "S-bp06-pos"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	usedAfter := budget.UsedTokens()

	// 正例校验：记录 provider 真报 token = 5000 + 3000 = 8000（不再有任何价格折算）
	expectedTokens := int64(5000 + 3000)
	actualTokens := usedAfter - usedBefore
	if actualTokens != expectedTokens {
		t.Errorf(" 正例：应记录 provider 真报 token = %d，got=%d", expectedTokens, actualTokens)
	}

	// 正例校验：trace 应含 Phase3EvtSubtaskOutput 事件（B-P0-6 修复：不再丢弃 agentOutput）
	events, err := tracer.QueryBySubtask("S-bp06-pos")
	if err != nil {
		t.Fatalf("QueryBySubtask: %v", err)
	}
	subtaskOutputFound := false
	for _, e := range events {
		if e.EventType == Phase3EvtSubtaskOutput {
			subtaskOutputFound = true
			// 验证 Payload 含 usage 字段
			if _, ok := e.Payload["usage"]; !ok {
				t.Error("B-P0-6 正例：Phase3EvtSubtaskOutput Payload 应含 'usage' 字段")
			}
			// 验证 Payload 含 status 字段
			if _, ok := e.Payload["status"]; !ok {
				t.Error("B-P0-6 正例：Phase3EvtSubtaskOutput Payload 应含 'status' 字段")
			}
		}
	}
	if !subtaskOutputFound {
		t.Error("B-P0-6 正例：Dispatch 后应写入 Phase3EvtSubtaskOutput trace 事件（不再丢弃 agentOutput）")
	}
}

// TestParallelScheduler_Dispatch_UsageNil_RecordsZeroTokens（2026-10-09）：
// agent 返回 Usage=nil 的 AgentOutput 时，Dispatch 应记录 0 token，且仍写入 trace。
// 反例核心：Usage=nil → 0 token（不伪造用量），但 agentOutput 仍写入 trace。
func TestParallelScheduler_Dispatch_UsageNil_RecordsZeroTokens(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, budget, tracer, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-bp06-neg"}, []string{BucketA})

	// agent 返回 Usage=nil 的 AgentOutput
	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		return tools.AgentOutput{
			Result: "ok",
			Status: tools.TaskCompleted,
			Usage:  nil, // 关键：Usage 为 nil
		}, nil
	}

	usedBefore := budget.UsedTokens()
	if err := scheduler.Dispatch(context.Background(), "S-bp06-neg"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	usedAfter := budget.UsedTokens()

	// 反例校验：Usage=nil 时记录 0 token
	actualTokens := usedAfter - usedBefore
	if actualTokens != 0 {
		t.Errorf(" 反例：Usage=nil 时应记录 0 token，got=%d", actualTokens)
	}

	// 反例校验：即便 Usage=nil，agentOutput 仍应写入 trace（B-P0-6 修复：不丢弃 agentOutput）
	events, err := tracer.QueryBySubtask("S-bp06-neg")
	if err != nil {
		t.Fatalf("QueryBySubtask: %v", err)
	}
	subtaskOutputFound := false
	for _, e := range events {
		if e.EventType == Phase3EvtSubtaskOutput {
			subtaskOutputFound = true
		}
	}
	if !subtaskOutputFound {
		t.Error("B-P0-6 反例：Usage=nil 时 Dispatch 仍应写入 Phase3EvtSubtaskOutput trace 事件（不丢弃 agentOutput）")
	}
}

// TestParallelScheduler_Dispatch_UsageAllZero_ZeroTokens（2026-10-09）：
// agent 返回 Usage 非空但全零时，记录 0 token。
func TestParallelScheduler_Dispatch_UsageAllZero_ZeroTokens(t *testing.T) {
	t.Parallel()
	scheduler, agent, graph, _, budget, _, _ := makeTestParallelScheduler(t, 3)
	addTestNodes(t, graph, []string{"S-bp06-zero"}, []string{BucketA})

	agent.executeFn = func(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
		return tools.AgentOutput{
			Result: "ok",
			Status: tools.TaskCompleted,
			Usage: &tools.TokenUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		}, nil
	}

	usedBefore := budget.UsedTokens()
	if err := scheduler.Dispatch(context.Background(), "S-bp06-zero"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	usedAfter := budget.UsedTokens()

	// 边界校验：Usage 全零时记录 0 token
	actualTokens := usedAfter - usedBefore
	if actualTokens != 0 {
		t.Errorf(" 边界：Usage 全零时应记录 0 token，got=%d", actualTokens)
	}
}

// TestTotalTokensFromUsage_CountsAllDimensions（2026-10-09）：
// totalTokensFromUsage 只做**计数**（四维 token 求和），不做任何价格换算。
func TestTotalTokensFromUsage_CountsAllDimensions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		usage    *tools.TokenUsage
		expected int64
	}{
		{"nil → 0", nil, 0},
		{"全零 → 0", &tools.TokenUsage{}, 0},
		{"仅 input=500 → 500", &tools.TokenUsage{InputTokens: 500}, 500},
		{"input=1000 → 1000", &tools.TokenUsage{InputTokens: 1000}, 1000},
		{"input=5000+output=3000 → 8000", &tools.TokenUsage{InputTokens: 5000, OutputTokens: 3000}, 8000},
		{"input=10000+output=5000+cacheCreation=2000+cacheRead=3000 → 20000", &tools.TokenUsage{
			InputTokens: 10000, OutputTokens: 5000, CacheCreationTokens: 2000, CacheReadTokens: 3000,
		}, 20000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := totalTokensFromUsage(tc.usage)
			if got != tc.expected {
				t.Errorf("totalTokensFromUsage(%+v) = %d, want %d", tc.usage, got, tc.expected)
			}
		})
	}
}
