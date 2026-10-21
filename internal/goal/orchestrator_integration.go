// orchestrator_integration.go — M25-02 goal 与 H10 编排器子任务触发评估桥接。
//
// 设计目标：当 H10 ExperienceOrchestrator 完成子任务时，通知 goal Manager 更新进度
// 并在适当时机触发评估。避免 goal 包直接依赖 orchestrator 包（防循环依赖），
// 采用回调注入 + 适配器模式（与 桥接模式一致）。
//
// 桥接流程（main.go 接线）：
// 1. main.go 创建 GoalOrchestratorBridge（持有 *goal.Manager）
// 2. main.go 在 orchestrator.OnSubtaskDone 完成后调用 bridge.OnSubtaskCompleted
// 3. bridge 将子任务结果转为 SubtaskCompletionEvent 传给 Manager
// 4. Manager 累加 goal 用量 + 可选触发评估（当子任务对齐 objective 时）
//
// 与 H10 编排器集成点；落地：SubtaskCompletionEvent + GoalOrchestratorBridge
// ；落地：与 compact/launchTurn/hasUserInput 同模式
// 钩子；落地：main.go 在 OnSubtaskDone 后调 bridge
// REF-外部-同类 CLI@commit:N/A-无独立 goal 评估；落地：N26 创新点（子任务级 goal 进度追踪）
// SNAPSHOT: pkg=internal/goal; type=SubtaskCompletionEvent; fields={ThreadID,SubtaskID,Success,TokensUsed,DurationSec,Summary}( 2026-10-09: CostCents 已删); bridge=GoalOrchestratorBridge; integration=OnSubtaskCompleted→Manager.OnTurnEnd 风格更新; fatal_counterexample={1:子任务失败不阻塞 goal 续跑,2:评估失败降级不中断编排}

package goal

import (
	"context"
	"fmt"
	"sync"

	"github.com/anchorandfish/whale-code/internal/log"
)

// SubtaskCompletionEvent 子任务完成事件（H10 编排器 → goal 桥接载体）。
//
// 由 main.go 接线层从 orchestrator.TaskResult 转换而来，goal 包不依赖 orchestrator 类型。
// 字段语义对齐 TurnResult（tokens/cost/duration/error→success），便于复用 OnTurnEnd 评估逻辑。
type SubtaskCompletionEvent struct {
	// ThreadID 会话 ID（用于定位 goal，必填）。
	ThreadID string
	// SubtaskID 子任务 ID（用于日志追踪，必填）。
	SubtaskID string
	// Success 子任务是否成功（true=L0Pass，false=失败）。
	Success bool
	// TokensUsed 子任务消耗的 token 数（累加到 goal.TokensUsed）。
	TokensUsed int64
	// DurationSec 子任务执行时长（秒，累加到 goal.TimeUsedSec）。
	DurationSec int64
	// Summary 子任务执行摘要（注入证据的 Summary 字段，可选）。
	Summary string
}

// GoalOrchestratorBridge goal 与 H10 编排器的桥接器（M25-02）。
//
// 持有 *Manager 引用，提供子任务完成回调接口。
// main.go 创建后注入 orchestrator 的回调链（不修改 orchestrator 接口签名）。
// 并发安全：内部无状态（所有状态在 Manager 中，Manager 自带 mutex）。
type GoalOrchestratorBridge struct {
	manager *Manager

	// mu 保护评估触发去重（同一 subtaskID 不重复触发评估）。
	mu            sync.Mutex
	evaluatedSubs map[string]bool // 已触发评估的 subtaskID 集合

	// enableAutoEval 是否在子任务完成时自动触发评估（默认 false，仅累加用量）。
	// true 时每 N 个子任务触发一次评估（evalInterval 控制）。
	enableAutoEval bool
	// evalInterval 触发评估的子任务间隔（默认 5，每 5 个子任务评估一次）。
	evalInterval int
	// subtaskCount 子任务计数（用于 evalInterval 判断）。
	subtaskCount int
}

// NewGoalOrchestratorBridge 创建桥接器（M25-02）。
//
// 参数：
// - manager: goal.Manager 实例（必填，nil 返回 nil）
//
// 默认配置：enableAutoEval=false（仅累加用量，不自动触发评估）。
// 调用方可通过 SetAutoEval(true, interval) 开启自动评估。
func NewGoalOrchestratorBridge(manager *Manager) *GoalOrchestratorBridge {
	if manager == nil {
		return nil
	}
	return &GoalOrchestratorBridge{
		manager:        manager,
		evaluatedSubs:  make(map[string]bool),
		enableAutoEval: false, // 默认仅累加用量，不自动评估（防过度调用评估 Agent）
		evalInterval:   5,     // 默认每 5 个子任务评估一次
	}
}

// SetAutoEval 配置自动评估模式（M25-02）。
//
// 参数：
// - enabled: true 开启自动评估（每 interval 个子任务触发一次）
// - interval: 评估间隔（≤0 时用默认值 5）
//
// 典型场景：长任务拆分为多子任务时，开启自动评估可在中途检测偏离。
// 关闭时（enabled=false）仅累加用量，评估由 OnTurnEnd 统一触发。
func (b *GoalOrchestratorBridge) SetAutoEval(enabled bool, interval int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enableAutoEval = enabled
	if interval > 0 {
		b.evalInterval = interval
	}
}

// OnSubtaskCompleted 子任务完成回调（M25-02，供 main.go 在 orchestrator.OnSubtaskDone 后调用）。
//
// 流程：
// 1. 校验 event（ThreadID/SubtaskID 非空）
// 2. 去重检查（同一 subtaskID 不重复处理）
// 3. 累加 goal 用量（tokens/cost/duration）
// 4. 若 enableAutoEval 且达到 evalInterval → 触发轻量评估
// 5. 子任务失败不阻塞 goal（致命反例 1：子任务失败仅记录，不转 blocked）
//
// 并发安全：mu 保护 evaluatedSubs + subtaskCount。
// 失败策略：所有错误仅 log.Warn，不返回 error（不阻塞编排器主流程）。
func (b *GoalOrchestratorBridge) OnSubtaskCompleted(ctx context.Context, event SubtaskCompletionEvent) {
	if b == nil || b.manager == nil {
		return
	}
	if event.ThreadID == "" || event.SubtaskID == "" {
		log.Warn("[goal-bridge] OnSubtaskCompleted: ThreadID or SubtaskID empty, skip")
		return
	}

	b.mu.Lock()
	// 去重检查（同一 subtaskID 不重复处理）
	if b.evaluatedSubs[event.SubtaskID] {
		b.mu.Unlock()
		return
	}
	b.evaluatedSubs[event.SubtaskID] = true
	b.subtaskCount++
	shouldEval := b.enableAutoEval && b.subtaskCount%b.evalInterval == 0
	b.mu.Unlock()

	// 步骤 3：累加 goal 用量（复用 OnTurnEnd 的累加逻辑，但不触发续跑）
	if err := b.updateGoalProgress(ctx, event); err != nil {
		log.Warn(fmt.Sprintf("[goal-bridge] update goal progress failed for thread %s subtask %s: %v",
			event.ThreadID, event.SubtaskID, err))
		// 不返回，继续评估检查（致命反例 1：用量更新失败不阻塞评估）
	}

	// 步骤 4：可选触发轻量评估（达到 interval 时）
	if shouldEval {
		if err := b.triggerLightweightEval(ctx, event); err != nil {
			log.Warn(fmt.Sprintf("[goal-bridge] lightweight eval failed for thread %s subtask %s: %v",
				event.ThreadID, event.SubtaskID, err))
			// 致命反例 2：评估失败降级不中断编排
		}
	}
}

// updateGoalProgress 累加 goal 用量（M25-02，内部方法）。
//
// 复用 Manager.store.Load/Save 累加 tokens/cost/duration。
// 不触发评估或续跑（仅用量更新，评估由 OnTurnEnd 或 triggerLightweightEval 负责）。
// 若 goal 不存在或非 active → no-op（子任务仍可完成，goal 是可选的）。
//
// P0-7 修复：将 store.Load 移入 mu.Lock() 之后，消除 read-modify-write 竞态。
// 原实现 Load 在锁外执行，返回旧指针；Lock 后修改旧指针并 Save，覆盖并发修改。
// 注意：Load 是文件 I/O，锁持有时间会增加，但这是保证 RMW 原子性的必要代价。
func (b *GoalOrchestratorBridge) updateGoalProgress(ctx context.Context, event SubtaskCompletionEvent) error {
	// P0-7 修复：Load 移入锁内，防止 read-modify-write 竞态
	b.manager.mu.Lock()
	defer b.manager.mu.Unlock()

	g, err := b.manager.store.Load(event.ThreadID)
	if err != nil {
		if isNotExistErr(err) {
			return nil // 无 goal，no-op（子任务完成不依赖 goal 存在）
		}
		return fmt.Errorf("load goal: %w", err)
	}
	if g == nil || g.Status != StatusActive {
		return nil // 无 goal 或非 active，no-op
	}

	g.TokensUsed += event.TokensUsed
	g.TimeUsedSec += event.DurationSec
	g.UpdatedAt = nowUnix()
	if err := b.manager.store.Save(event.ThreadID, g); err != nil {
		return fmt.Errorf("save goal progress: %w", err)
	}
	return nil
}

// triggerLightweightEval 触发轻量评估（M25-02，内部方法）。
//
// 收集当前证据（Summary 优先用子任务摘要）+ 调 runEvaluation。
// 评估失败降级不阻塞（致命反例 2：仅 log.Warn）。
// 评估结果不直接标 complete/blocked（由 OnTurnEnd 统一判定，避免子任务级误判）。
func (b *GoalOrchestratorBridge) triggerLightweightEval(ctx context.Context, event SubtaskCompletionEvent) error {
	g, err := b.manager.store.Load(event.ThreadID)
	if err != nil || g == nil {
		return nil // 无 goal，no-op
	}
	if g.Status != StatusActive {
		return nil // 非 active 不评估
	}

	// 构造轻量证据（仅 Summary，完整证据由 OnTurnEnd 的 collectEvidenceFn 收集）
	evidence := Evidence{
		Summary: event.Summary,
	}
	if event.Summary == "" {
		evidence.Summary = fmt.Sprintf("subtask %s completed, success=%v", event.SubtaskID, event.Success)
	}

	// 调 runEvaluation（复用 Manager 的评估逻辑，含 M25-04 可配置 model）
	evalResult, err := b.manager.runEvaluation(ctx, g, evidence)
	if err != nil {
		// 降级不阻塞（致命反例 2）
		log.Warn(fmt.Sprintf("[goal-bridge] eval failed for thread %s: %v, fallback", event.ThreadID, err))
		return nil
	}

	// 仅记录评估结果，不直接改 goal 状态（状态流转由 OnTurnEnd 统一管理，防子任务级误判）
	if evalResult != nil {
		log.Warn(fmt.Sprintf("[goal-bridge] thread %s subtask %s eval: aligned=%v completed=%v deviation=%s",
			event.ThreadID, event.SubtaskID, evalResult.Aligned, evalResult.Completed, evalResult.Deviation))
	}
	return nil
}

// GetSubtaskCount 返回已处理的子任务数（测试用）。
func (b *GoalOrchestratorBridge) GetSubtaskCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subtaskCount
}

// IsEvaluated 检查指定 subtaskID 是否已处理（测试用）。
func (b *GoalOrchestratorBridge) IsEvaluated(subtaskID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.evaluatedSubs[subtaskID]
}

// Reset 清空桥接器状态（测试用，生产环境不调用）。
func (b *GoalOrchestratorBridge) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evaluatedSubs = make(map[string]bool)
	b.subtaskCount = 0
}
