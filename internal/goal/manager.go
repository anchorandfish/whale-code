package goal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// objectiveMaxLen objective 最大长度（对标同类 CLI）。
const objectiveMaxLen = 4000

// defaultMinContinueInterval 默认最小续跑间隔（秒，）。
// 对标同类 CLI goal_state_lock Semaphore + blocked 状态隐式退避。
const defaultMinContinueInterval int64 = 5

// blockedThreshold 转 blocked 的连续阻塞计数阈值（T7 反例：1 轮不转）。
// 对标同类 CLI handle_update 只能设 complete/blocked + 连续 3 轮。
const blockedThreshold = 3

// CompactHook 续跑前上下文压缩回调（N11 只读复用，）。
//
// runnerCore 注入时桥接到 CompactionPipeline.Execute 或 Estimator（v4 修正）。
// 注意：runnerCore.handleCompact 签名是 (args string) string，不匹配此签名，
//
//	桥接需适配器（建议调 CompactionPipeline.Execute 或独立调 Estimator）。
//
// 返回 postCompactTokens（压缩后 token 数）+ err。
type CompactHook func(ctx context.Context, threadID string) (postCompactTokens int, err error)

// TurnLauncher 续跑时启动新 turn 的回调。
//
// TUI 层注入时桥接到 Model.SendPrompt(prompt string)（app.go:1540，v4 修正）。
// Plain/Bridge 层注入时桥接到 plainRunner.Run(continuationPrompt) 或等价启动逻辑。
type TurnLauncher func(ctx context.Context, threadID string, continuationPrompt string) error

// UserInputChecker 检查用户是否有待处理输入（续跑前调用，用户输入优先，）。
//
// TUI 层注入时桥接到 m.sessionService.State() != StateIdle（v4 修正：无 inputBuffer.HasPending()）。
// Plain/Bridge 层注入时始终返回 false（无 TUI 输入缓冲区）。
type UserInputChecker func() bool

// EvidenceCollector 收集本轮证据的回调。
//
// runnerCore 注入时桥接到已有数据源：
// - FilesChanged → Executor.FileHistoryGetter().ListSnapshots()
// - TestResult/CommandOutput → session 最近 N 条 tool_result 消息
// - Summary → session 最近一条 assistant 消息文本
type EvidenceCollector func(ctx context.Context, threadID string, turnResult TurnResult) (Evidence, error)

// Manager goal 管理器（N26 主设计接口，一会话一 goal）。
//
// 对标同类 CLI ThreadGoal + 同类 CLI /goal v2.1.139。
// 8 个核心方法签名严格按改进方案 §3.2：SetGoal/GetGoal/ClearGoal/UpdateStatus/
// EditObjective/OnTurnEnd/Evaluate/ContinueIfIdle。
type Manager struct {
	store Store

	// evalAgent N1 只读实例（评估 Agent，Model=claude-3-haiku，AgentID="goal-eval"）。
	// ⚠️ G-T-001：它的**只读性来自 eval.go 里显式声明的 AgentType=explore**
	// （工具面 = roleToolSet ∩ ParentToolSet），**不是**来自 `AgentInput.Tools=[]`
	// —— 后者是兼容字段、生产码零读取点，删/改它都不会改变工具面。
	evalAgent tools.Agent
	// metrics N2 只读复用（全局累加）。
	metrics *metrics.Metrics
	// usageTracker N2 by-agent 分桶（，v4 新增）。
	usageTracker *ai.UsageTracker

	// 回调桥接：runnerCore/TUI 注入具体实现，goal 包不依赖 tui/context 包
	compact           CompactHook       // N11 compact 回调
	launchTurn        TurnLauncher      // 续跑启动 turn 回调
	hasUserInput      UserInputChecker  // 用户输入检查回调
	collectEvidenceFn EvidenceCollector // 证据收集回调

	mu sync.Mutex // 保护内存中的 active goal 操作（Save/UpdateStatus 等复合操作）

	// evalModelMu 专门保护 evalModel 字段的并发读写（P0-9 修复）。
	// 独立于 m.mu，原因：runEvaluation/recordEvalUsage 会在 m.mu.Lock() 持有期间被调用
	// （如 MarkCompleteDirect），若复用 m.mu 的 RLock 会导致自死锁（Go RWMutex 不可重入）。
	evalModelMu sync.RWMutex

	// prompts 提示词模板集合。
	prompts *PromptTemplates

	// MinContinueInterval 最小续跑间隔（秒，）。
	// 默认 5 秒，防止 413 reactive compact 未完成时密集重试。
	MinContinueInterval int64

	// evalModel 评估 Agent 使用的快速模型（M25-04：可配置 fast model）。
	// 默认 "claude-3-haiku"（对标同类 CLI "fast model checks whether the condition holds"）。
	// 可通过 SetEvalModel 运行时替换（如切换到 gemini-flash / deepseek-chat 等更便宜的快速模型）。
	// SNAPSHOT: pkg=internal/goal; field=Manager.evalModel; type=string; default="claude-3-haiku"; mutator=SetEvalModel; consumer=runEvaluation/recordEvalUsage
	evalModel string
}

// NewManager 创建 Manager（+）。
//
// 参数：
// - store: 持久化（JSON 文件实现，）
// - evalAgent: 评估 Agent（N1 只读实例，）
// - metricsSvc: N2 全局累加（只读复用）
// - usageTracker: N2 by-agent 分桶（，v4 新增）
// - compact: N11 compact 回调（，runnerCore 注入桥接）
// - launchTurn: 续跑启动 turn 回调（，TUI/Plain/Bridge 注入）
// - hasUserInput: 用户输入检查回调（，TUI 注入，Plain/Bridge 返回 false）
// - collectEvidenceFn: 证据收集回调（，runnerCore 注入）
//
// MinContinueInterval 默认 5 秒。
// prompts 默认 NewPromptTemplates()，可通过 SetPromptTemplates 替换。
func NewManager(
	store Store,
	evalAgent tools.Agent,
	metricsSvc *metrics.Metrics,
	usageTracker *ai.UsageTracker,
	compact CompactHook,
	launchTurn TurnLauncher,
	hasUserInput UserInputChecker,
	collectEvidenceFn EvidenceCollector,
) *Manager {
	return &Manager{
		store:               store,
		evalAgent:           evalAgent,
		metrics:             metricsSvc,
		usageTracker:        usageTracker,
		compact:             compact,
		launchTurn:          launchTurn,
		hasUserInput:        hasUserInput,
		collectEvidenceFn:   collectEvidenceFn,
		prompts:             NewPromptTemplates(),
		MinContinueInterval: defaultMinContinueInterval,
		evalModel:           defaultEvalModel, // M25-04：默认 fast model，可经 SetEvalModel 替换
	}
}

// SetEvalModel 设置评估 Agent 使用的快速模型（M25-04：可配置 fast model）。
//
// 传入空字符串时忽略（保留当前值，防止误清空）。
// 运行时并发安全：使用 evalModelMu 写锁(Lock)保护 evalModel 字段（P0-9 修复）。
// 独立于 m.mu，避免 runEvaluation 在 m.mu.Lock() 持有期间调用时 RLock 自死锁。
// 典型场景：用户配置 gemini-flash / deepseek-chat 等更便宜的快速模型替代 claude-3-haiku。
// 字段可配置化；落地：替换 eval.go 硬编码常量
// REF-外部-同类 CLI@2026-03-31-fast model 评估模式；落地：对标 "fast model checks whether the condition holds"
func (m *Manager) SetEvalModel(model string) {
	if model == "" {
		return // 空字符串忽略，防误清空
	}
	m.evalModelMu.Lock()
	defer m.evalModelMu.Unlock()
	m.evalModel = model
}

// GetEvalModel 返回当前评估 Agent 使用的模型（测试用）。
//
// P0-9 修复：使用 evalModelMu.RLock 替代 m.mu.Lock，允许并发读且不与 m.mu 冲突。
func (m *Manager) GetEvalModel() string {
	m.evalModelMu.RLock()
	defer m.evalModelMu.RUnlock()
	return m.evalModel
}

// SetPromptTemplates 替换提示词模板（测试或本地化时使用）。
func (m *Manager) SetPromptTemplates(p *PromptTemplates) {
	if p != nil {
		m.prompts = p
	}
}

// SetGoal 设置目标（替换需确认，T2 反例）。
//
// 返回被替换的旧 goal（若有），调用方决定是否弹确认框（对标同类 CLI）。
// objective 校验：非空，≤4000 字符（对标同类 CLI）。
// IsUntrusted 检测：objective 含敏感指令模式时置 true（+ ，T5 反例）。
//
// 状态规则：
// - 已有 active/paused/blocked/budget_limited/usage_limited → 返回旧 goal 供确认（T2 反例）
// - 已有 complete → 直接替换（对标同类 CLI）
// - 无旧 goal → 直接创建
func (m *Manager) SetGoal(ctx context.Context, threadID, objective string, tokenBudget int64) (*Goal, error) {
	if threadID == "" {
		return nil, errors.New("goal: threadID is empty")
	}
	if err := validateObjective(objective); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 读取旧 goal（文件不存在视为无旧 goal）
	oldGoal, err := m.store.Load(threadID)
	if err != nil && !errors.Is(err, errNotFoundSentinel) && !isNotExistErr(err) {
		return nil, fmt.Errorf("goal: load old goal: %w", err)
	}

	now := nowUnix()
	newGoal := &Goal{
		GoalID:      generateGoalID(),
		ThreadID:    threadID,
		Objective:   objective,
		Status:      StatusActive,
		TokenBudget: tokenBudget,
		CreatedAt:   now,
		UpdatedAt:   now,
		IsUntrusted: detectUntrusted(objective), // 正则检测
	}

	if err := m.store.Save(threadID, newGoal); err != nil {
		return nil, fmt.Errorf("goal: save new goal: %w", err)
	}

	// 返回旧 goal 供调用方决定是否弹确认框（T2 反例）
	// 若旧 goal 已 complete 或不存在，调用方应直接替换；否则应弹确认
	return oldGoal, nil
}

// GetGoal 读取当前 goal（store.Load，文件不存在返回 nil）。
func (m *Manager) GetGoal(ctx context.Context, threadID string) (*Goal, error) {
	g, err := m.store.Load(threadID)
	if err != nil {
		if isNotExistErr(err) {
			return nil, nil // 文件不存在视为无 goal（S4 反例：查看不崩溃）
		}
		return nil, fmt.Errorf("goal: get goal: %w", err)
	}
	return g, nil
}

// ClearGoal 清除目标（store.Delete + 清内存缓存）。
//
// 文件不存在不报错（幂等）。
func (m *Manager) ClearGoal(ctx context.Context, threadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.Delete(threadID); err != nil {
		return fmt.Errorf("goal: clear goal: %w", err)
	}
	return nil
}

// UpdateStatus 更新状态（仅允许用户设 paused/active；complete/blocked 由评估流程设，防误调）。
//
// resume 时若 status=blocked 重置 blocked_count=0（对标同类 CLI handle_update）。
//
// 状态机流转（改进方案 §5）：
// - Active → Paused：用户 /goal pause
// - Paused → Active：用户 /goal resume
// - BudgetLimited/UsageLimited → Active：预算/限流恢复后用户 resume
// - Blocked → Active：用户介入后 resume（重置 blocked_count）
func (m *Manager) UpdateStatus(ctx context.Context, threadID string, status Status) error {
	// 仅允许用户设 paused/active（complete/blocked 由评估流程设）
	if status != StatusPaused && status != StatusActive {
		return fmt.Errorf("goal: UpdateStatus only allows paused/active, got %s (complete/blocked set by eval flow)", status)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	g, err := m.store.Load(threadID)
	if err != nil {
		return fmt.Errorf("goal: load for UpdateStatus: %w", err)
	}
	if g == nil {
		return errors.New("goal: no active goal to update")
	}

	// resume 时若旧 status=blocked 重置 blocked_count（对标同类 CLI handle_update）
	if status == StatusActive && g.Status == StatusBlocked {
		g.BlockedCount = 0
	}

	g.Status = status
	g.UpdatedAt = nowUnix()
	if err := m.store.Save(threadID, g); err != nil {
		return fmt.Errorf("goal: save updated status: %w", err)
	}
	return nil
}

// MarkCompleteDirect 由 /goal done 子命令直接标记完成。
//
// 评估 Agent 可用 → 调 Evaluate 确认，结果带 EvalResult。
// 不可用或用户强制 force=true → 直接标 complete（带 EvalResult.Unverified=true，T9 反例）。
//
// 返回 EvalResult（含 Unverified 标记）+ error。
func (m *Manager) MarkCompleteDirect(ctx context.Context, threadID string, force bool) (*EvalResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	g, err := m.store.Load(threadID)
	if err != nil {
		return nil, fmt.Errorf("goal: load for MarkCompleteDirect: %w", err)
	}
	if g == nil {
		return nil, errors.New("goal: no active goal to mark complete")
	}

	var evalResult *EvalResult
	if !force && m.evalAgent != nil {
		// 评估 Agent 可用 → 调 Evaluate 确认（收集当前证据）
		evidence, evErr := m.collectEvidence(ctx, threadID, TurnResult{ThreadID: threadID})
		if evErr != nil {
			log.Warn(fmt.Sprintf("[goal] collect evidence for /goal done failed: %v", evErr))
			evidence = Evidence{}
		}
		evalResult, err = m.runEvaluation(ctx, g, evidence)
		if err != nil {
			// 评估失败降级（T6 反例：不阻塞）
			evalResult, _ = m.runEvaluationFallback(ctx, g, evidence, err)
		}
	} else {
		// force=true 或评估 Agent 不可用 → 直接标 complete（T9 反例：必须带 Unverified=true）
		evalResult = &EvalResult{
			Aligned:    true,
			Completed:  true,
			Suggestion: "marked complete by /goal done without eval",
			Unverified: true, // 降级标记
		}
	}

	// 强制标记 complete（即使评估 aligned=false，因为是用户主动确认）
	g.Status = StatusComplete
	g.UpdatedAt = nowUnix()
	if err := m.store.Save(threadID, g); err != nil {
		return evalResult, fmt.Errorf("goal: save complete: %w", err)
	}
	return evalResult, nil
}

// EditObjective 编辑目标文本（保留 status/budget/用量，运行时冲突矩阵第 9 行）。
//
// 编辑后 UpdatedAt 更新，下轮续跑注入新 objective。
func (m *Manager) EditObjective(ctx context.Context, threadID, newObjective string) error {
	if err := validateObjective(newObjective); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	g, err := m.store.Load(threadID)
	if err != nil {
		return fmt.Errorf("goal: load for EditObjective: %w", err)
	}
	if g == nil {
		return errors.New("goal: no active goal to edit")
	}

	g.Objective = newObjective
	g.IsUntrusted = detectUntrusted(newObjective) // 重新检测
	g.UpdatedAt = nowUnix()
	if err := m.store.Save(threadID, g); err != nil {
		return fmt.Errorf("goal: save edited objective: %w", err)
	}
	return nil
}

// OnTurnEnd turn 结束钩子（注册到 runner_core，）。
//
// 流程（v3 修订：删除旧步骤 10 上下文使用率检查， 依赖 413 reactive compact）：
// 1. 加载 goal，若 status≠active 直接 return（终态不续跑，T1 反例）
// 2. 累加 tokens_used / cost_used / time_used（N2 核算）
// 3. 检查预算：超限转 budget_limited + 注入收尾提示词（T3 反例）
// 4. 检查错误：usage_limited 走 N6 GOAL_USAGE_LIMITED；其他错误累加 blocked_count
// 5. 收集本轮 Evidence（调 collectEvidenceFn 回调，）
// 6. 调 Evaluate 评估
// 7. 评估完成（aligned+completed+证据非空）→ 标 complete（T4 反例）
// 8. 评估阻塞（blocked_count≥3 + 评估确认）→ 标 blocked（T7 反例）
// 9. 检查用户输入队列：调 hasUserInput() 回调，有输入中断续跑（，运行时冲突矩阵第 4 行）
//
// 10. ContinueIfIdle 注入 continuation + 启动新 turn
//
// ⚠️ 上下文使用率不在 goal 层主动检查，依赖 413 响应触发 runnerCore 已有的 reactive compact。
//
// P0-7 修复：步骤 1 的 store.Load 移入 mu.Lock() 之后，消除 read-modify-write 竞态。
// 原实现 Load 在锁外执行，返回旧指针；Lock 后修改旧指针并 Save，覆盖并发修改。
// 注意：Load 是文件 I/O，锁持有时间会增加，但这是保证 RMW 原子性的必要代价。
func (m *Manager) OnTurnEnd(ctx context.Context, threadID string, turnResult TurnResult) error {
	// P0-7 修复：Load 移入锁内，防止 read-modify-write 竞态
	m.mu.Lock()
	// 步骤 1：加载 goal，若 status≠active 直接 return（T1 反例：终态不续跑）
	g, err := m.store.Load(threadID)
	if err != nil {
		m.mu.Unlock()
		if isNotExistErr(err) {
			return nil // 无 goal，no-op（钩子对非 goal 会话是 no-op）
		}
		return fmt.Errorf("goal: OnTurnEnd load: %w", err)
	}
	if g == nil {
		m.mu.Unlock()
		return nil // 无 goal，no-op
	}
	if g.Status != StatusActive {
		m.mu.Unlock()
		return nil // T1 反例：终态/暂停/阻塞/限流均不续跑
	}

	// 步骤 2：累加用量（N2 核算）
	g.TokensUsed += turnResult.TokensUsed
	g.TimeUsedSec += turnResult.DurationSec

	// 步骤 4：检查错误（usage_limited 走 N6 GOAL_USAGE_LIMITED，其他错误累加 blocked_count）
	if turnResult.Error != nil {
		if turnResult.IsUsageLimited {
			// N6 GOAL_USAGE_LIMITED：转 usage_limited 终态
			g.Status = StatusUsageLimited
			g.UpdatedAt = nowUnix()
			if err := m.store.Save(threadID, g); err != nil {
				m.mu.Unlock()
				return fmt.Errorf("goal: save usage_limited: %w", err)
			}
			m.mu.Unlock()
			log.Warn(fmt.Sprintf("[goal] thread %s marked usage_limited: %v", threadID, turnResult.Error))
			return nil // 终态不续跑
		}
		// 其他错误：累加 blocked_count（不立即转 blocked，T7 反例：需 ≥3 + 评估确认）
		g.BlockedCount++
	}

	// 步骤 3：检查预算（T3 反例：超限转 budget_limited + 收尾）
	if exceeded, reason := m.checkBudget(g); exceeded {
		g.Status = StatusBudgetLimited
		g.UpdatedAt = nowUnix()
		if err := m.store.Save(threadID, g); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("goal: save budget_limited: %w", err)
		}
		// 释放锁后注入收尾提示词（避免长操作持锁）
		m.mu.Unlock()
		log.Warn(fmt.Sprintf("[goal] thread %s marked budget_limited: %s", threadID, reason))
		// T3 反例：注入收尾提示词
		if err := m.injectClosingPrompt(ctx, g); err != nil {
			log.Warn(fmt.Sprintf("[goal] inject closing prompt failed: %v", err))
		}
		return nil // 终态不续跑
	}

	// 保存用量累加结果
	g.UpdatedAt = nowUnix()
	// 修复 B3b-P1-5：将 Save 移到锁外，减少持锁期间的 I/O 阻塞。
	// Load 仍在锁内（P0-7 修复保持），仅 Save 移出。
	// 同 threadID 的 OnTurnEnd 不会并发（turn 串行），跨 threadID 各自独立 goal 文件，无竞争。
	m.mu.Unlock()
	if err := m.store.Save(threadID, g); err != nil {
		return fmt.Errorf("goal: save usage accumulation: %w", err)
	}

	// 步骤 5：收集本轮 Evidence（调 collectEvidenceFn 回调，）
	evidence, evErr := m.collectEvidence(ctx, threadID, turnResult)
	if evErr != nil {
		log.Warn(fmt.Sprintf("[goal] collect evidence failed: %v", evErr))
		evidence = Evidence{} // 失败用空证据，T4 反例防护仍生效
	}

	// 步骤 6：调 Evaluate 评估
	evalResult, evalErr := m.runEvaluation(ctx, g, evidence)
	if evalErr != nil {
		// T6 反例：评估失败降级为模型自审 + GOAL_EVAL_FAILED 告警，不阻塞续跑
		evalResult, _ = m.runEvaluationFallback(ctx, g, evidence, evalErr)
	}

	// 步骤 7：评估完成判定（T4 反例：需 aligned+completed+证据非空）
	if evalResult != nil && evalResult.Aligned && evalResult.Completed && !evidence.IsEmpty() {
		m.mu.Lock()
		g.Status = StatusComplete
		g.UpdatedAt = nowUnix()
		if err := m.store.Save(threadID, g); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("goal: save complete: %w", err)
		}
		m.mu.Unlock()
		log.Warn(fmt.Sprintf("[goal] thread %s marked complete (eval confirmed)", threadID))
		return nil // 终态不续跑
	}

	// 步骤 8：评估阻塞判定（T7 反例：blocked_count≥3 + 评估确认）
	if evalResult != nil && !evalResult.Aligned {
		// blocked_count 已在步骤 4 累加（仅当 turnResult.Error != nil）
		// 这里补充：评估 aligned=false 但无错误时也累加（连续无进展）
		m.mu.Lock()
		if turnResult.Error == nil {
			g.BlockedCount++
		}
		if g.BlockedCount >= blockedThreshold {
			g.Status = StatusBlocked
			g.UpdatedAt = nowUnix()
			if err := m.store.Save(threadID, g); err != nil {
				m.mu.Unlock()
				return fmt.Errorf("goal: save blocked: %w", err)
			}
			m.mu.Unlock()
			log.Warn(fmt.Sprintf("[goal] thread %s marked blocked (count=%d)", threadID, g.BlockedCount))
			return nil // 终态不续跑
		}
		// 保存累加的 blocked_count
		// 修复 B3b-P1-5：Save 移到锁外，减少持锁 I/O 阻塞
		m.mu.Unlock()
		if err := m.store.Save(threadID, g); err != nil {
			return fmt.Errorf("goal: save blocked_count: %w", err)
		}
	} else if evalResult != nil && evalResult.Aligned {
		// 评估通过：重置 blocked_count
		// 但若本轮有 turn error（步骤 4 已累加），不重置：
		// 测试 TestOnTurnEnd_TurnErrorAccumulatesBlockedCount 期望
		// turn error 即使在评估 aligned 时也累加，避免错误被评估通过"洗白"。
		// 只有评估通过且本轮无错误时才重置。
		if turnResult.Error != nil {
			// 本轮有错误：保留步骤 4 累加的 blocked_count
			// 修复 B3b-P1-5：此分支不修改 g，仅需持久化步骤 1 的变更，无需加锁
			if err := m.store.Save(threadID, g); err != nil {
				return fmt.Errorf("goal: save (turn error retain blocked_count): %w", err)
			}
		} else if g.BlockedCount > 0 {
			// 修复 B3b-P1-5：Save 移到锁外，减少持锁 I/O 阻塞
			m.mu.Lock()
			g.BlockedCount = 0
			g.UpdatedAt = nowUnix()
			m.mu.Unlock()
			if err := m.store.Save(threadID, g); err != nil {
				return fmt.Errorf("goal: save reset blocked_count: %w", err)
			}
		}
	}

	// 步骤 9：检查用户输入队列（，运行时冲突矩阵第 4 行：用户输入优先）
	if m.hasUserInput != nil && m.hasUserInput() {
		log.Warn(fmt.Sprintf("[goal] thread %s: user input pending, skip continue", threadID))
		return nil // 用户输入优先，中断续跑
	}

	// 步骤 10：ContinueIfIdle 注入 continuation + 启动新 turn
	return m.ContinueIfIdle(ctx, threadID)
}

// Evaluate 独立评估（，对外公开接口）。
//
// 输入：objective + 本轮证据 + 工作树状态。
// 输出：EvalResult。
// 失败处理：超时/错误降级为模型自审 + GOAL_EVAL_FAILED 告警（T6 反例）。
func (m *Manager) Evaluate(ctx context.Context, g *Goal, evidence Evidence) (*EvalResult, error) {
	if g == nil {
		return nil, errors.New("goal: Evaluate called with nil goal")
	}
	result, err := m.runEvaluation(ctx, g, evidence)
	if err != nil {
		// T6 反例：降级不阻塞
		return m.runEvaluationFallback(ctx, g, evidence, err)
	}
	return result, nil
}

// ContinueIfIdle 空闲续跑（注入 continuation 后启动新 turn，对标同类 CLI try_start_turn_if_idle）。
//
// continuation 作为 InternalModelContextFragment 追加（不破坏 N3 cache，运行时冲突矩阵第 6 行）。
// v3 修订（+）：
// 1. 退避检查：if time.Now().Unix()-g.LastContinueAt < m.MinContinueInterval { return nil }
// 2. 加载 goal，status≠active 直接 return
// 3. 用户输入检查：if m.hasUserInput() { return nil }（用户输入优先，）
// 4. 构造 continuation prompt（调 prompts.BuildContinuation）
// 5. 调 m.launchTurn(ctx, threadID, continuation) 启动新 turn
// 6. 更新 g.LastContinueAt = time.Now().Unix()
func (m *Manager) ContinueIfIdle(ctx context.Context, threadID string) error {
	// 步骤 1：退避检查
	g, err := m.store.Load(threadID)
	if err != nil {
		if isNotExistErr(err) {
			return nil // 无 goal，no-op
		}
		return fmt.Errorf("goal: ContinueIfIdle load: %w", err)
	}
	if g == nil {
		return nil // 无 goal，no-op
	}
	if g.Status != StatusActive {
		return nil // T1 反例：非 active 不续跑
	}

	// 退避检查：MinContinueInterval 内不重复续跑
	if m.MinContinueInterval > 0 && g.LastContinueAt > 0 {
		if now := nowUnix(); now-g.LastContinueAt < m.MinContinueInterval {
			return nil // 退避中，跳过
		}
	}

	// 步骤 3：用户输入检查（，运行时冲突矩阵第 4 行）
	if m.hasUserInput != nil && m.hasUserInput() {
		return nil // 用户输入优先
	}

	// 步骤 4：构造 continuation prompt
	// 评估 Agent 输出（若 OnTurnEnd 已调用）作为审计段落
	evidence, _ := m.collectEvidence(ctx, threadID, TurnResult{ThreadID: threadID})
	continuation := m.prompts.BuildContinuation(g, evidence, nil)

	// 步骤 5：调 launchTurn 启动新 turn
	if m.launchTurn == nil {
		return nil // launchTurn 未注入（测试或非 TUI 模式），no-op
	}
	if err := m.launchTurn(ctx, threadID, continuation); err != nil {
		return fmt.Errorf("goal: launch turn: %w", err)
	}

	// 步骤 6：更新 LastContinueAt
	m.mu.Lock()
	defer m.mu.Unlock()
	g.LastContinueAt = nowUnix()
	g.UpdatedAt = g.LastContinueAt
	if err := m.store.Save(threadID, g); err != nil {
		return fmt.Errorf("goal: save last_continue_at: %w", err)
	}
	return nil
}

// Restore 跨会话恢复（S15 反例：resume 时读 JSON，恢复 active goal）。
//
// 对标同类 CLI restore_after_resume。
// resume 时读 JSON，active 恢复续跑；paused 保持暂停（不调 ContinueIfIdle）。
func (m *Manager) Restore(ctx context.Context, threadID string) error {
	g, err := m.store.Load(threadID)
	if err != nil {
		if isNotExistErr(err) {
			return nil // 无 goal，no-op
		}
		return fmt.Errorf("goal: restore: %w", err)
	}
	if g == nil {
		return nil // 无 goal，no-op
	}
	// T8 反例：恢复读 JSON，保留 tokens_used（不归零）
	if g.Status == StatusActive {
		// active 恢复续跑（对标同类 CLI continue_if_idle）
		return m.ContinueIfIdle(ctx, threadID)
	}
	// paused 保持暂停（不调 ContinueIfIdle）
	return nil
}

// ListGoals 列出所有持久化的 goal（对标同类 CLI thread_goal_list，/goal list 子命令）。
//
// 委托给 store.List()，返回所有 thread 的 goal 列表（按 UpdatedAt 降序由 store 实现）。
func (m *Manager) ListGoals(ctx context.Context) ([]*Goal, error) {
	return m.store.List()
}

// --- 校验与工具函数 ---

// validateObjective 校验 objective 非空 + ≤4000 字符（对标同类 CLI）。
func validateObjective(objective string) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("goal: objective is empty")
	}
	if len(objective) > objectiveMaxLen {
		return fmt.Errorf("goal: objective too long: %d > %d", len(objective), objectiveMaxLen)
	}
	return nil
}

// untrustedPatterns 敏感指令模式（，轻量正则 + 启发式，不用 LLM 检测）。
//
// 对标同类 CLI 无 IsUntrusted + 同类 CLI 无显式防护；N26 创新点。
var untrustedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)忽略以上`),
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|above|prior)`),
	regexp.MustCompile(`(?i)delete\s+all\s+files`),
	regexp.MustCompile(`(?i)rm\s+-rf\s+/`),
	regexp.MustCompile(`(?i)you\s+are\s+now`),
	regexp.MustCompile(`(?i)忽略前面`),
	regexp.MustCompile(`(?i)忽略所有`),
	regexp.MustCompile(`(?i)system\s+prompt`),
	regexp.MustCompile(`(?i)ignore\s+instructions`),
	regexp.MustCompile(`(?i)删除所有文件`),
	regexp.MustCompile(`(?i)清空所有`),
}

// detectUntrusted 检测 objective 是否含敏感指令模式。
//
// 轻量正则匹配，不用 LLM 检测（成本/延迟考量）。
// 命中任一模式返回 true（标记 IsUntrusted=true，T5 反例：评估 prompt 拒绝执行）。
func detectUntrusted(objective string) bool {
	for _, p := range untrustedPatterns {
		if p.MatchString(objective) {
			return true
		}
	}
	return false
}

// generateGoalID 生成 GoalID（UUID 风格，对标同类 CLI goal_id）。
//
// 使用 crypto/rand 生成 16 字节随机数，hex 编码为 32 字符字符串。
// 不引入 uuid 库（避免新依赖，WHALE.md §6.1）。
func generateGoalID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 极端情况：rand 失败，用时间戳兜底（不阻塞）
		return fmt.Sprintf("goal-%d", nowUnix())
	}
	return "goal-" + hex.EncodeToString(b)
}

// errNotFoundSentinel 内部哨兵错误（保留向后兼容，但 mockStore 应使用 os.ErrNotExist 与生产一致）。
var errNotFoundSentinel = errors.New("goal: not found")

// errNotExistStd 绑定到 os.ErrNotExist，用于 isNotExistErr 判断。
// 修复 H1-P0-1：原设计用 store.go init 绑定，但 init 从未实现，导致恒为 "not initialized"。
// 改为包级 var 直接赋值 os.ErrNotExist（Go 允许跨文件 var 初始化，无需 init 函数）。
// internal/goal/store.go::fileStore.Load 文件不存在返回 os.ErrNotExist。
var errNotExistStd = os.ErrNotExist

// isNotExistErr 判断是否"文件不存在"错误（兼容 os.ErrNotExist 与 sentinel）。
// 修复 H1-P0-1：errNotExistStd 现在正确绑定到 os.ErrNotExist，生产路径（fileStore.Load
// 返回 os.ErrNotExist）能被正确识别，OnTurnEnd 在无 goal 场景返回 no-op 而非错误。
func isNotExistErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errNotFoundSentinel) {
		return true
	}
	return errors.Is(err, errNotExistStd)
}
