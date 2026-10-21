// orchestrator.go — H10 Phase 4 模块 7 Orchestrator.Dispatch/OnSubtaskDone 集成（Phase 4 §4.2）。
// 集成点；落地：Dispatch/OnSubtaskDone 包装 + 委托 ParallelScheduler
// ParallelScheduler-委托关系；落地：o.scheduler.Dispatch/OnSubtaskDone 不改签名
// BASE 变更 pattern_id 写入；落地：OnSubtaskDone 写 quality.jsonl pattern_id
// 不可用降级；落地：rerank 失败时不注入 + 走三因子（防死循环，致命反例 2）
// SNAPSHOT: pkg=internal/orchestrator; type=Orchestrator; fields={scheduler(*ParallelScheduler),state(*StateMachine),router(*Router),expStore(*ExperienceStore),subtasks(map[string]*TaskContext),qualityStore(*QualityStore)}; delegation=ParallelScheduler.Dispatch/OnSubtaskDone(不改签名); integration={Dispatch:经验库Match+InjectContext+router四因子SelectModel,OnSubtaskDone:生命周期UpdateLifecycle+pitfall RecordPitfall+quality.jsonl pattern_id}; fatal_counterexample={2:rerank失败降级不阻塞防死循环}

package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/log"
)

// ExperienceOrchestrator 经验编排器（Phase 4 扩展，组合 Phase 3 ParallelScheduler）。
// Dispatch/OnSubtaskDone 是 ParallelScheduler 对应方法的编排层包装：
// 经验库注入需在编排层完成（Scheduler 不持有 expStore 字段），故由 ExperienceOrchestrator 包装并委托。
// 本包装不修改 ParallelScheduler.Dispatch/OnSubtaskDone 签名（02 §4 仲裁无需触发，属内部组合非接口扩展）。
// 注：命名为 ExperienceOrchestrator 避免与 orchestrate_command.go 的 Orchestrator（Phase 1 命令层聚合器）冲突。
type ExperienceOrchestrator struct {
	mu           sync.RWMutex
	scheduler    *ParallelScheduler // Phase 3（Dispatch/OnSubtaskDone 委托此字段）
	state        *StateMachine      // Phase 1-2
	router       *Router            // Phase 2（三因子，Phase 4 扩展为四因子 §3.6）
	expStore     *ExperienceStore   // Phase 4 新增（经验库检索 + 注入 + 生命周期）
	qualityStore *QualityStore      // Phase 0（OnSubtaskDone 写 quality.jsonl pattern_id）

	// subtasks 注册表：subtaskID → TaskContext（Dispatch 前由 RegisterSubtask 注册）。
	// State.SubTasks 不含 TaskContext，故 ExperienceOrchestrator 自维护映射供 Dispatch 检索。
	subtasks map[string]*TaskContext

	// evolutionLoop H12 自我进化闭环（Phase 6 接线点，默认 nil=不触发）。
	// 由 main.go 在 evolution.enabled=true 时注入。
	// OnTaskDone 完成后异步调用 evolutionLoop.Run（不阻塞主流程）。
	evolutionLoop *EvolutionLoop
}

// NewExperienceOrchestrator 创建经验编排器（Phase 4 §4.2）。
// scheduler/state/router/expStore 为必填，qualityStore 可选（nil 时跳过 quality.jsonl 写入）。
func NewExperienceOrchestrator(scheduler *ParallelScheduler, state *StateMachine, router *Router, expStore *ExperienceStore, qualityStore *QualityStore) *ExperienceOrchestrator {
	return &ExperienceOrchestrator{
		scheduler:    scheduler,
		state:        state,
		router:       router,
		expStore:     expStore,
		qualityStore: qualityStore,
		subtasks:     make(map[string]*TaskContext),
	}
}

// RegisterSubtask 注册子任务上下文（Dispatch 前调用，Phase 4 §4.2）。
// ExperienceOrchestrator 自维护 subtaskID → TaskContext 映射，因 State.SubTasks 不含 TaskContext。
func (o *ExperienceOrchestrator) RegisterSubtask(subtaskID string, tc *TaskContext) error {
	if subtaskID == "" {
		return fmt.Errorf("orchestrator: subtaskID must not be empty")
	}
	if tc == nil {
		return fmt.Errorf("orchestrator: TaskContext must not be nil")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.subtasks[subtaskID] = tc
	return nil
}

// GetSubtask 获取已注册的子任务上下文（测试用）。
func (o *ExperienceOrchestrator) GetSubtask(subtaskID string) (*TaskContext, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	tc, ok := o.subtasks[subtaskID]
	return tc, ok
}

// Dispatch 派发子任务（Phase 4 §4.2，委托 Phase 3 ParallelScheduler.Dispatch）。
// 流程：① 检索经验库 Match ② 命中且 verified/provisionally_verified → InjectContext
// ③ router.SelectModel 四因子决策 ④ 委托 scheduler.Dispatch。
// rerank 失败时降级为不注入 + 走三因子（§5.1，致命反例 2：防死循环）。
func (o *ExperienceOrchestrator) Dispatch(ctx context.Context, subtaskID string) error {
	if subtaskID == "" {
		return fmt.Errorf("orchestrator: subtaskID must not be empty")
	}
	if o.scheduler == nil {
		return fmt.Errorf("orchestrator: scheduler is nil")
	}
	if o.expStore == nil {
		// 无经验库时直接委托 scheduler（兼容降级）
		return o.scheduler.Dispatch(ctx, subtaskID)
	}

	// ① 查找已注册的 TaskContext
	o.mu.RLock()
	tc, ok := o.subtasks[subtaskID]
	o.mu.RUnlock()
	if !ok || tc == nil {
		// 未注册 TaskContext，直接委托（不注入经验）
		return o.scheduler.Dispatch(ctx, subtaskID)
	}

	// ② 经验库检索（rerank 失败降级为不注入，§5.1，致命反例 2：防死循环）
	hits, matchErr := o.expStore.Match(tc.Goal, inferTaskTypeFromGoal(tc.Goal), extractTagsFromTaskContext(tc))
	if matchErr == nil && len(hits) > 0 {
		best := hits[0]
		// 仅 provisionally_verified / verified 注入（unverified / deprecated 不注入，致命反例 1/5）
		if best.VerifiedState != VerifiedStateUnverified && best.VerifiedState != VerifiedStateDeprecated {
			if injectErr := o.expStore.InjectContext(tc, best); injectErr == nil {
				// 注入成功：ExperienceHit 已写入 tc
			} else {
				// 修复 B3b-P1-6：InjectContext 失败时清除 tc.ExperienceHit。
				// 原实现注入失败时不清除，若 tc 为重试场景（同 subtaskID 多次 Dispatch），
				// 上一轮注入的 ExperienceHit 会残留，导致 OnSubtaskDone 仍记录错误 PatternID。
				tc.ExperienceHit = nil
			}
			// 注入失败（如 unverified/deprecated）→ 不注入，走三因子（不阻塞）
		}
	}
	// matchErr != nil 或 len(hits)==0 → 不注入，走三因子（§5.1 降级，致命反例 2：防死循环）

	// ③ router 四因子决策（因子 4 经验库命中已在 tc.ExperienceHit 中）
	if o.router != nil {
		if model, err := o.router.SelectModel(tc); err == nil && model != "" {
			tc.Model = model
		}
	}

	// ④ 委托 ParallelScheduler.Dispatch（不改签名）
	return o.scheduler.Dispatch(ctx, subtaskID)
}

// OnSubtaskDone 子任务完成回调（Phase 4 §4.2，委托 Phase 3 ParallelScheduler.OnSubtaskDone）。
// 流程：① 委托 scheduler.OnSubtaskDone ② 经验库生命周期更新（UpdateLifecycle）
// ③ 失败时沉淀 pitfall（RecordPitfall）④ 写 quality.jsonl pattern_id（v2 BASE 变更）。
func (o *ExperienceOrchestrator) OnSubtaskDone(ctx context.Context, subtaskID string, result TaskResult) error {
	if o.scheduler != nil {
		// ① 委托 ParallelScheduler.OnSubtaskDone（不改签名）
		// 修复 B3b-P1-3：原实现忽略委托错误（_=），导致 graph.MarkDone 等失败被静默吞掉。
		// 现至少记录 warn 日志，便于运维排查（仍不阻塞经验库更新，保持降级语义）。
		if err := o.scheduler.OnSubtaskDone(ctx, subtaskID, result); err != nil {
			log.Warn(fmt.Sprintf("[orchestrator] scheduler.OnSubtaskDone delegate error for %s: %v", subtaskID, err))
		}
	}

	if o.expStore == nil {
		return nil
	}

	// ② 经验库生命周期更新（有 ExperienceHit 时）
	if result.ExperienceHit != nil && result.ExperienceHit.PatternID != "" {
		signal := TaskCompletionSignal{
			L0Pass:          extractL0Pass(result),
			L1Score:         extractL1Score(result),
			L2Verdict:       result.L2Verdict,
			CompletedAt:     time.Now().UTC().Format(time.RFC3339),
			OrchestrationID: result.OrchestrationID,
			PatternID:       result.ExperienceHit.PatternID,
		}
		// 生命周期更新失败不阻塞（best-effort）
		_ = o.expStore.UpdateLifecycle(result.ExperienceHit.PatternID, signal)
	}

	// ③ 失败时沉淀 pitfall（L0Fail + 有 FailureTrace + 有 TaskContext）
	if !extractL0Pass(result) && result.FailureTrace != nil && result.TaskContext != nil {
		_ = o.expStore.RecordPitfall(result.TaskContext, *result.FailureTrace)
	}

	// ④ 写 quality.jsonl pattern_id（v2 BASE 变更， 三段式已论证）
	if o.qualityStore != nil && result.ExperienceHit != nil && result.ExperienceHit.PatternID != "" {
		rec := QualityRecord{
			TaskID:          subtaskID,
			OrchestrationID: result.OrchestrationID,
			TaskType:        inferTaskTypeFromGoal(extractGoal(result)),
			L0Pass:          extractL0Pass(result),
			L1Score:         extractL1Score(result),
			L2Verdict:       result.L2Verdict,
			Timestamp:       time.Now().UTC().Format(time.RFC3339),
			PatternID:       result.ExperienceHit.PatternID,
		}
		_ = o.qualityStore.Append(rec)
	}

	return nil
}

// extractL0Pass 从 TaskResult 提取 L0Pass（优先 Output.L0Pass，回退 Success）。
func extractL0Pass(result TaskResult) bool {
	if result.Output != nil {
		return result.Output.L0Pass
	}
	return result.Success
}

// extractL1Score 从 TaskResult 提取 L1Score（优先 Output.L1Score，回退 0）。
func extractL1Score(result TaskResult) int {
	if result.Output != nil {
		return result.Output.L1Score
	}
	return 0
}

// extractGoal 从 TaskResult 提取 Goal（优先 TaskContext.Goal）。
func extractGoal(result TaskResult) string {
	if result.TaskContext != nil {
		return result.TaskContext.Goal
	}
	return ""
}

// extractTagsFromTaskContext 从 TaskContext 提取 tags（FilesTouched 扩展名 + Goal tokens）。
func extractTagsFromTaskContext(tc *TaskContext) []string {
	if tc == nil {
		return nil
	}
	tagSet := make(map[string]struct{})
	// Goal tokens 作为 tag
	for _, tok := range tokenize(tc.Goal) {
		tagSet[tok] = struct{}{}
	}
	// PriorFiles 扩展名作为 tag
	for _, f := range tc.PriorFiles {
		ext := ""
		for i := len(f) - 1; i >= 0; i-- {
			if f[i] == '.' {
				ext = f[i:]
				break
			}
			if f[i] == '/' || f[i] == '\\' {
				break
			}
		}
		if ext != "" {
			tagSet[ext] = struct{}{}
		}
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	return tags
}

// SetEvolutionLoop 注入 H12 自我进化闭环（Phase 6 接线点，§3.1）。
// 传入 nil 可关闭 H12（Feature Flag 关闭时）。
// 该方法不参与 Dispatch/OnSubtaskDone 主路径，仅 OnTaskDone 钩子使用。
// 并发安全：写锁保护 evolutionLoop 字段。
func (o *ExperienceOrchestrator) SetEvolutionLoop(loop *EvolutionLoop) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.evolutionLoop = loop
}

// GetEvolutionLoop 返回当前注入的 EvolutionLoop（测试用）。
func (o *ExperienceOrchestrator) GetEvolutionLoop() *EvolutionLoop {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.evolutionLoop
}

// OnTaskDone 任务完成钩子（Phase 6 新增，H12 触发点）。
// 在 OnSubtaskDone 完成后调用（由 main.go 显式调用，不改 OnSubtaskDone 签名）。
// 若 evolutionLoop 为 nil 或未启用 → 直接返回 nil（零成本降级）。
// 否则异步调 evolutionLoop.Run（不阻塞主流程，失败仅 warn）。
//
// 参数：
// - ctx：上下文（用于超时控制）
// - taskID：当前任务 ID（必填，空返回 error）
// - trace：任务执行 trace（用于 PatternExtractor.Extract，可空）
//
// 返回：仅返回 taskID 空校验错误与 ctx 超时错误，evolutionLoop.Run 失败不返回（warn 不阻断）。
func (o *ExperienceOrchestrator) OnTaskDone(ctx context.Context, taskID string, trace []TraceEntry) error {
	if taskID == "" {
		return fmt.Errorf("orchestrator: taskID must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.RLock()
	loop := o.evolutionLoop
	o.mu.RUnlock()
	if loop == nil || !loop.IsEnabled() || !loop.IsAutoRun() {
		return nil // 未启用 H12 或 autoRun=false，零成本降级（Feature Flag 双开关语义 §3.1+§4.2）
	}
	// 异步触发自我进化闭环（不阻塞主流程）
	// 注：使用 context.Background() 避免 ctx 取消导致 Run 中途退出
	// （任务已完成，闭环进化是事后行为，不应受主 ctx 影响）
	go func() {
		_ = loop.Run(context.Background(), taskID, trace)
	}()
	return nil
}
