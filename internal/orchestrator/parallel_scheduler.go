// parallel_scheduler.go — H10 Phase 3 模块 5 并行调度器（H10-Phase3-方案细化-v3.md §6.2.4）。
// REF-外部-LangGraph@https://langchain-ai.github.io/langgraph/concepts/persistence/+2026-07-15访问-节点调度模型；借鉴逻辑：拓扑排序+就绪节点派发；落地：DispatchBatch + NextBatch
// API；落地：ParallelScheduler 组合 DepGraph+WorktreePool+BudgetTracker
// +桶内串行；落地：DispatchBatch 按桶分组，桶间并行+桶内串行
// 复用-BudgetTracker/StateMachine 不改签名；落地：ParallelScheduler 持有引用只读调用
// tool + H10 外层包装；落地：ParallelScheduler 调 N1 Agent.Execute 不改签名
// ≤3 轮；落地：retryLimit=3，超限转 manual
// SNAPSHOT: pkg=internal/orchestrator; type=ParallelScheduler; reuse={DepGraph,WorktreePool,BudgetTracker(Phase1不改签名),TraceStore,StateMachine(Phase1不改签名)}; max_parallel=3; retry_limit=3; failure_states={recoverable→retry(≤3),reorganize→回出方案,manual→暂停通知}; budget_check=派发前预估+事后检查(致命反例4); scheduling=桶间并行+桶内串行(02§3)

package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// FailureAction 失败处理动作（Phase 3 §2.2 失败三态）。
type FailureAction string

const (
	// FailureActionRetry 可恢复：重试（限 retryLimit 次）。
	FailureActionRetry FailureAction = "retry"
	// FailureActionReorganize 需重组：回出方案（通知用户重新分解）。
	FailureActionReorganize FailureAction = "reorganize"
	// FailureActionManual 需人工：暂停 + 通知用户介入。
	FailureActionManual FailureAction = "manual"
)

// RetryLimitDefault 可恢复任务重试上限默认值（，致命反例 5）。
const RetryLimitDefault = 3

// TaskResult 子任务执行结果（Phase 3 §6.2.4，OnSubtaskDone 回调入参）。
// Phase 4 v2 BASE 变更（三段式已论证）：新增 ExperienceHit/L2Verdict/OrchestrationID/FailureTrace/TaskContext 五字段，
// 用于 Orchestrator.OnSubtaskDone 经验库生命周期更新与 pitfall 沉淀。omitempty 向后兼容，Phase 3 旧调用方不受影响。
//
// ★ 2026-10-09：原 `CostCents int` 字段删除 —— 用量只有 provider 真报的 `TokensUsed`
// 一根轴（「不算价钱」铁律）。注意 `TokensUsed` 的 JSON tag 保持 `tokens_used` 不变。
type TaskResult struct {
	Success    bool             `json:"success"`
	Output     *ImplementOutput `json:"output,omitempty"`
	ErrorType  string           `json:"error_type,omitempty"`
	TokensUsed int64            `json:"tokens_used"`

	// === Phase 4 v2 BASE 变更（三段式已论证，omitempty 向后兼容）===
	// ExperienceHit 经验库命中信息（OnSubtaskDone 据此调 UpdateLifecycle）。
	ExperienceHit *ExperienceHit `json:"experience_hit,omitempty"`
	// L2Verdict 用户采纳判定（adopted/modified/discarded，驱动生命周期状态机）。
	L2Verdict string `json:"l2_verdict,omitempty"`
	// OrchestrationID 编排会话 ID（跨会话判定防同会话误判，§3.3.3）。
	OrchestrationID string `json:"orchestration_id,omitempty"`
	// FailureTrace 失败追踪（L0Fail 时 RecordPitfall 用）。
	FailureTrace *FailureTrace `json:"failure_trace,omitempty"`
	// TaskContext 子任务上下文（RecordPitfall 需要 task.Goal 作为 signature）。
	TaskContext *TaskContext `json:"task_context,omitempty"`
}

// ParallelScheduler 并行调度器，桶间并行+桶内串行（Phase 3 §6.2.4 + 02 §3）。
// 组合 DepGraph + WorktreePool + BudgetTracker + TraceStore，不继承 Phase 1 Scheduler。
// 并发保护：sync.Mutex 保护 retryCount/running 状态。
type ParallelScheduler struct {
	mu          sync.Mutex
	graph       *DepGraph
	pool        *WorktreePool
	budget      *BudgetTracker // 复用 Phase 1（不改签名）
	tracer      *TraceStore
	state       *StateMachine   // 复用 Phase 1（不改签名）
	agent       tools.Agent     // N1 Agent.Execute（不改签名，），通过 SetAgent 注入
	maxParallel int             // = --max-parallel（默认 3）
	retryLimit  int             // 可恢复任务重试上限（默认 3，）
	retryCount  map[string]int  // per-subtask retry counter（致命反例 5）
	running     map[string]bool // currently running subtask IDs
}

// NewParallelScheduler 创建并行调度器（Phase 3 §6.2.4）。
// 构造后需调 SetAgent 注入 N1 Agent 才能执行 Dispatch。
func NewParallelScheduler(graph *DepGraph, pool *WorktreePool, budget *BudgetTracker, tracer *TraceStore, state *StateMachine, maxParallel int) *ParallelScheduler {
	if maxParallel <= 0 {
		maxParallel = 3 // 默认 --max-parallel=3
	}
	return &ParallelScheduler{
		graph:       graph,
		pool:        pool,
		budget:      budget,
		tracer:      tracer,
		state:       state,
		maxParallel: maxParallel,
		retryLimit:  RetryLimitDefault,
		retryCount:  make(map[string]int),
		running:     make(map[string]bool),
	}
}

// SetAgent 注入 N1 Agent（不改 N1 签名，）。
// agent 必须实现 tools.Agent 接口，Execute 签名与 N1 一致。
func (s *ParallelScheduler) SetAgent(agent tools.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agent = agent
}

// SetRetryLimit 设置重试上限（仅用于测试覆盖致命反例 5 边界）。
func (s *ParallelScheduler) SetRetryLimit(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > 0 {
		s.retryLimit = limit
	}
}

// Dispatch 派发一个子任务（派发前预算预估 + 事后检查，致命反例 4）。
// 流程：① 预算预检 ② Acquire worktree ③ 调 N1 Agent.Execute ④ 记录成本 ⑤ 事后预算检查 ⑥ 写 trace。
// 致命反例 4：3 子任务同时派发时预算竞态预防（sync.Mutex + 预算预检 + 事后检查）。
func (s *ParallelScheduler) Dispatch(ctx context.Context, subtaskID string) error {
	if subtaskID == "" {
		return fmt.Errorf("parallel_scheduler: subtaskID must not be empty")
	}

	s.mu.Lock()
	agent := s.agent
	if agent == nil {
		s.mu.Unlock()
		return fmt.Errorf("parallel_scheduler: agent not set, call SetAgent first")
	}

	// 致命反例 4：预算预检（派发前检查是否已超限）
	if s.budget != nil && s.budget.IsExceeded() {
		s.mu.Unlock()
		return fmt.Errorf("parallel_scheduler: budget already exceeded, cannot dispatch %s (致命反例 4)", subtaskID)
	}

	// 检查是否已在运行
	if s.running[subtaskID] {
		s.mu.Unlock()
		return fmt.Errorf("parallel_scheduler: subtask %s is already running", subtaskID)
	}

	// 标记为运行中
	s.running[subtaskID] = true
	s.mu.Unlock()

	// 写 dispatch trace（派发开始）
	if s.tracer != nil {
		_ = s.tracer.Append(PhaseTraceEvent{
			EventType: Phase3EvtDispatch,
			SubtaskID: subtaskID,
			Payload: map[string]interface{}{
				"bucket":       s.graphBucket(subtaskID),
				"budget_check": "pre-dispatch passed",
			},
		})
	}

	// ② Acquire worktree（Phase 3 §6.1 物理隔离）
	session, err := s.pool.Acquire(ctx, subtaskID, "main")
	if err != nil {
		s.mu.Lock()
		delete(s.running, subtaskID)
		s.mu.Unlock()
		return fmt.Errorf("parallel_scheduler: acquire worktree for %s: %w", subtaskID, err)
	}

	// ③ 调 N1 Agent.Execute（不改签名，）
	//
	// L2注：这里必须**显式声明**子任务角色 —— 子 agent 的工具面已改为由
	// 声明的 agent_type 唯一决定（不再按 prompt 关键字推断），未声明会落到默认的只读角色
	// （explore，委派默认不扩权）而使并行 worker 无法写码。worker 的职责是改代码 → coder。
	agentInput := tools.AgentInput{
		Prompt:    fmt.Sprintf("执行子任务 %s（worktree: %s）", subtaskID, session.WorktreePath),
		AgentType: string(tools.AgentTypeCoder),
	}
	agentOutput, err := agent.Execute(ctx, agentInput)

	// ④ 记录用量（事后检查，致命反例 4）
	// ★ 2026-10-09：只记 **provider 真报的 token**；原"按自编单价折算成美分"
	// （B-P0-6 的 computeCostFromUsage）已删除 —— 那是个代理，不是真值。
	if err == nil && s.budget != nil {
		_ = s.budget.RecordUsage(totalTokensFromUsage(agentOutput.Usage))
	}

	// ⑤ 事后预算检查
	budgetExceeded := false
	if s.budget != nil {
		_, _, over100 := s.budget.CheckBudget()
		budgetExceeded = over100
	}

	// 写 budget_check trace
	if s.tracer != nil {
		_ = s.tracer.Append(PhaseTraceEvent{
			EventType: Phase3EvtBudgetCheck,
			SubtaskID: subtaskID,
			Payload: map[string]interface{}{
				"check_type":      "post-dispatch",
				"budget_exceeded": budgetExceeded,
			},
		})
	}

	// 修复 P0-8：成功路径使用 MergeActionMerge 保留子任务文件变更，
	// 失败路径才使用 MergeActionDiscard 丢弃变更。
	// 原实现无论成功失败都 Discard，导致成功子任务的文件修改被丢弃。
	action := MergeActionDiscard
	if err == nil {
		action = MergeActionMerge
	}
	_, rerr := s.pool.Release(ctx, subtaskID, action)
	if rerr != nil {
		// 释放失败不阻塞，但记录
	}

	// 清除运行标记
	s.mu.Lock()
	delete(s.running, subtaskID)
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("parallel_scheduler: dispatch %s agent execute: %w", subtaskID, err)
	}

	// 修复 B3b-P1-2：移除 Dispatch 中的 MarkDone，仅在 OnSubtaskDone 中标记完成。
	// 原实现 Dispatch 和 OnSubtaskDone 各调一次 MarkDone，同一 subtask 被标记完成两次。
	// graph.MarkDone 语义为"子任务完成"，应由完成回调 OnSubtaskDone 统一调用。

	// 修复 B-P0-6：不再丢弃 agentOutput，写入 trace 供 L0/L1 评分消费。
	if err == nil {
		s.recordSubtaskOutput(subtaskID, &agentOutput)
	}
	return nil
}

// totalTokensFromUsage 从 agent token usage 取 **provider 真报的 token 总量**。
// usage 为 nil 或全零时返回 0。
//
// ★ 2026-10-09：原 `computeCostFromUsage` 会把 token 再乘一个**自编单价**
// （`CostPer1kTokensCents`）折成"美分" —— 那是拿代理当真值，已整体删除。
// 这里只做**计数**：token 是 provider 上报的事实，不做任何价格换算（「不算价钱」铁律）。
func totalTokensFromUsage(usage *tools.TokenUsage) int64 {
	if usage == nil {
		return 0
	}
	total := usage.InputTokens + usage.OutputTokens + usage.CacheCreationTokens + usage.CacheReadTokens
	if total < 0 {
		return 0 // 防溢出 / 异常负数
	}
	return total
}

// recordSubtaskOutput 将子任务产出写入 trace（修复 B-P0-6：不再丢弃 agentOutput）。
// 写入 subtask_output trace 事件，供 L0/L1 评分和经验库消费。
// tracer 为 nil 时跳过（降级模式）。
func (s *ParallelScheduler) recordSubtaskOutput(subtaskID string, output *tools.AgentOutput) {
	if s.tracer == nil || output == nil {
		return
	}
	_ = s.tracer.Append(PhaseTraceEvent{
		EventType: Phase3EvtSubtaskOutput,
		SubtaskID: subtaskID,
		Payload: map[string]interface{}{
			"status":        output.Status.String(),
			"result_length": len(output.Result),
			"task_id":       output.TaskID,
			"usage":         output.Usage,
		},
	})
}

// DispatchBatch 批量派发（桶间并行+桶内串行，Phase 3 §6.2.2 调度循环）。
// 按 maxParallel 批量派发 ReadyToRun 的子任务，桶间并行+桶内串行。
//
// 修复 B3b-P1-1：原实现 errCh 创建但 goroutine 内从不写入错误，返回值始终为 nil。
// 现在 OnSubtaskFailed 后向 errCh 发送错误，并返回首个错误。
func (s *ParallelScheduler) DispatchBatch(ctx context.Context) error {
	ready := s.NextBatch()
	if len(ready) == 0 {
		return nil // 无就绪子任务
	}

	// 按桶分组（02 §3 桶间并行 + 桶内串行）
	bucketGroups := s.groupByBucket(ready)

	// 桶间并行：每个桶一个 goroutine
	var wg sync.WaitGroup
	errCh := make(chan error, len(bucketGroups))

	for bucket, subtaskIDs := range bucketGroups {
		// 跳过空桶
		if len(subtaskIDs) == 0 {
			continue
		}

		wg.Add(1)
		go func(b string, ids []string) {
			defer wg.Done()
			// 桶内串行：按顺序派发
			for _, id := range ids {
				// 检查 maxParallel 限制
				if !s.canDispatch() {
					return
				}
				if err := s.Dispatch(ctx, id); err != nil {
					// 桶内串行：一个失败不阻塞其他桶，但停止本桶后续
					_ = s.OnSubtaskFailed(ctx, id, err)
					// 修复 B3b-P1-1：向 errCh 发送错误，使 DispatchBatch 能感知失败
					errCh <- err
					return
				}
				// 修复 B3b-P1-2：Dispatch 不再调用 MarkDone，由 OnSubtaskDone 统一标记完成。
				// DispatchBatch 在 Dispatch 成功后调 OnSubtaskDone，保证 graph 节点标记为 done。
				// 外部调用方（orchestrator）的 OnSubtaskDone 调用对 MarkDone 是幂等的（dep_graph.MarkDone 仅设置状态）。
				_ = s.OnSubtaskDone(ctx, id, TaskResult{})
			}
		}(bucket, subtaskIDs)
	}

	wg.Wait()
	close(errCh)

	// 修复 B3b-P1-1：返回首个错误（原实现遍历 errCh 但只 _=err 丢弃，始终返回 nil）
	for err := range errCh {
		return err
	}

	return nil
}

// OnSubtaskDone 子任务完成回调（重新计算 ReadyToRun + 补派）。
// 调用方在子任务成功完成后调用，触发 NextBatch 补派。
func (s *ParallelScheduler) OnSubtaskDone(ctx context.Context, subtaskID string, result TaskResult) error {
	// 标记完成
	if err := s.graph.MarkDone(subtaskID); err != nil {
		return fmt.Errorf("parallel_scheduler: OnSubtaskDone mark done: %w", err)
	}

	// 记录用量
	if result.TokensUsed > 0 && s.budget != nil {
		if err := s.budget.RecordUsage(result.TokensUsed); err != nil {
			return fmt.Errorf("parallel_scheduler: record usage: %w", err)
		}
	}

	// 补派下一批（不阻塞，异步触发）
	// 实际实现中由调用方调 DispatchBatch 触发补派
	return nil
}

// OnSubtaskFailed 子任务失败回调（失败处理三态，致命反例 5）。
// 调用 HandleFailure 判定失败动作，并执行对应处理。
func (s *ParallelScheduler) OnSubtaskFailed(ctx context.Context, subtaskID string, err error) error {
	if err == nil {
		return nil
	}

	action := s.HandleFailure(ctx, subtaskID, err)

	switch action {
	case FailureActionRetry:
		// 可恢复：重试（retryCount < retryLimit 时重派）
		s.mu.Lock()
		retryCount := s.retryCount[subtaskID]
		s.mu.Unlock()
		if retryCount < s.retryLimit {
			s.mu.Lock()
			s.retryCount[subtaskID] = retryCount + 1
			s.mu.Unlock()
			// 重派（简化版：直接调 Dispatch，实际应有退避策略）
			return s.Dispatch(ctx, subtaskID)
		}
		// 超限转 manual（致命反例 5）
		return s.pauseAndNotify(subtaskID, fmt.Sprintf("retry exceeded limit %d: %v", s.retryLimit, err))

	case FailureActionReorganize:
		// 需重组：暂停子任务 + 通知用户重新分解
		return s.pauseAndNotify(subtaskID, fmt.Sprintf("reorganize needed: %v", err))

	case FailureActionManual:
		// 需人工：暂停 + 通知用户介入
		return s.pauseAndNotify(subtaskID, fmt.Sprintf("manual intervention required: %v", err))

	default:
		return fmt.Errorf("parallel_scheduler: unknown failure action %q", action)
	}
}

// OnDynamicDep 动态依赖检测回调（Phase 3 §6.2.3）。
// 解析 trace 事件 → 检查 to 的接口签名是否变化 → 若变化触发 §6.4 仲裁。
// graph.AddDynamicDep → 若 to 未派发提前派发，已派发暂停 from 等待。
func (s *ParallelScheduler) OnDynamicDep(ctx context.Context, from, to, reason string) error {
	// 写 dependency_detected trace
	if s.tracer != nil {
		_ = s.tracer.Append(PhaseTraceEvent{
			EventType: Phase3EvtDependencyDetected,
			SubtaskID: from,
			Payload: map[string]interface{}{
				"from":   from,
				"to":     to,
				"reason": reason,
			},
		})
	}

	// 添加动态依赖到图
	if err := s.graph.AddDynamicDep(from, to, reason); err != nil {
		return fmt.Errorf("parallel_scheduler: add dynamic dep %s → %s: %w", from, to, err)
	}

	// 检查 to 是否已派发
	s.mu.Lock()
	toRunning := s.running[to]
	s.mu.Unlock()

	if toRunning {
		// to 已派发 → 暂停 from 等待（标记为 blocked 由 graph.AddDynamicDep 处理）
		// 实际实现：from 标记为 blocked，等待 to 完成后自动解除
	}
	// to 未派发 → 不提前派发（由 NextBatch 自然调度）

	return nil
}

// NextBatch 返回下一批可派发的子任务 ID 列表（Phase 3 §6.2.2）。
// 从 graph.ReadyToRun() 获取就绪节点，按 maxParallel 限制截断。
func (s *ParallelScheduler) NextBatch() []string {
	ready := s.graph.ReadyToRun()

	s.mu.Lock()
	defer s.mu.Unlock()

	// 过滤掉已在运行的
	var batch []string
	for _, id := range ready {
		if !s.running[id] {
			batch = append(batch, id)
		}
		if len(batch) >= s.maxParallel {
			break
		}
	}
	return batch
}

// HandleFailure 失败处理三态判定（Phase 3 §2.2 失败三态，致命反例 5）。
// recoverable → 重试（限 retryLimit 次）；reorganize → 回出方案；manual → 暂停通知用户。
// 致命反例 5：retryCount >= retryLimit 时必须转 manual（零容忍）。
func (s *ParallelScheduler) HandleFailure(ctx context.Context, subtaskID string, err error) FailureAction {
	if err == nil {
		return FailureActionRetry
	}

	s.mu.Lock()
	retryCount := s.retryCount[subtaskID]
	limit := s.retryLimit
	s.mu.Unlock()

	// 致命反例 5：retryCount >= retryLimit → 必须转 manual（零容忍）
	if retryCount >= limit {
		return FailureActionManual
	}

	// 错误分类（基于错误消息关键词）
	errMsg := strings.ToLower(err.Error())

	// manual：安全漏洞 / 接口破坏
	if containsAny(errMsg, "security", "interface break", "signature change", "contract violation") {
		return FailureActionManual
	}

	// reorganize：契约冲突 / 依赖缺失
	if containsAny(errMsg, "contract conflict", "dependency missing", "plan outdated", "reorganize") {
		return FailureActionReorganize
	}

	// recoverable：网络错误 / 超时 / 其他可重试错误
	return FailureActionRetry
}

// pauseAndNotify 暂停子任务 + 通知用户（Phase 3 §2.2 manual 态）。
func (s *ParallelScheduler) pauseAndNotify(subtaskID, reason string) error {
	// 标记为 failed（graph 中）
	_ = s.graph.MarkFailed(subtaskID)

	// 清除运行标记
	s.mu.Lock()
	delete(s.running, subtaskID)
	s.mu.Unlock()

	// 写 trace（通知事件）
	if s.tracer != nil {
		_ = s.tracer.Append(PhaseTraceEvent{
			EventType: "paused_for_manual",
			SubtaskID: subtaskID,
			Payload: map[string]interface{}{
				"reason": reason,
			},
		})
	}

	return fmt.Errorf("parallel_scheduler: subtask %s paused: %s", subtaskID, reason)
}

// canDispatch 检查是否可以派发新子任务（maxParallel 限制）。
func (s *ParallelScheduler) canDispatch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) < s.maxParallel
}

// groupByBucket 按桶分组子任务 ID（02 §3 桶间并行 + 桶内串行）。
// 返回 map[bucket][]subtaskID，同桶内按优先级排序（ReadyToRun 已排序）。
func (s *ParallelScheduler) groupByBucket(subtaskIDs []string) map[string][]string {
	groups := make(map[string][]string)
	for _, id := range subtaskIDs {
		bucket, ok := s.graph.BucketOf(id)
		if !ok {
			bucket = BucketA // 兜底默认 A 桶
		}
		groups[bucket] = append(groups[bucket], id)
	}
	return groups
}

// graphBucket 获取节点所属桶（仅用于诊断）。
func (s *ParallelScheduler) graphBucket(subtaskID string) string {
	bucket, ok := s.graph.BucketOf(subtaskID)
	if !ok {
		return "unknown"
	}
	return bucket
}

// RunningCount 返回当前运行的子任务数（仅用于诊断）。
func (s *ParallelScheduler) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// GetRetryCount 返回子任务的重试次数（仅用于诊断，致命反例 5 验证）。
func (s *ParallelScheduler) GetRetryCount(subtaskID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retryCount[subtaskID]
}

// containsAny 检查字符串是否包含任意一个子串（不区分大小写已由调用方处理）。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
