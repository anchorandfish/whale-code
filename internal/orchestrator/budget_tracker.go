// budget_tracker.go — H10 Phase 5 桶 3 预算追踪器扩展（总纲 §8.3 双层硬预算 + Phase 5 §4.3 BudgetState）。
// REF-外部-arxiv:2306.05685-budget 概念在资源不足时降级；落地：§5.4 冷启动降级 + 派发前预估
// line 972-991-双层预算 + 并行预算竞态预防已定思路；落地：BudgetTrackerExt 实现
// 6 字段 + 派发前预估算法；落地：CheckBeforeDispatch
// ；落地：sync.Once 保证 80% 只通知 1 次
// 变更三段式（quality.jsonl 新增 budget_exceeded + exceed_reason，沉淀）
// SNAPSHOT: pkg=internal/orchestrator; type=BudgetTrackerExt+BudgetState+Estimate+BudgetResult+TerminateDecision; state=BudgetState{OrchestrationID,TotalTokensCap,UsedTokens,SubtaskEstimates,ExceededSubtasks}; concurrency=sync.Mutex+sync.Once+atomic; cold_start=EffectiveSampleCount<10用静态 token 矩阵; persist_failure=内存保留+告警+完成后重写; BASE变更=(budget_exceeded+exceed_reason)
//
// ★ 2026-10-09：本文件的「钱」维（TotalCostCents/UsedCostCents/ActualCostCents/
// EstimatedCost/staticCostByModel/CostCapCents）已整体删除 —— 预算轴只有 provider 真报的 token。
// 与现有 budget.go 的 BudgetTracker struct 共存：Phase 1 BudgetTracker 是简单计数器（RecordUsage/CheckBudget），
// Phase 5 BudgetTrackerExt 是完整预算追踪器（派发前预估 + 锁外通知 + 持久化 + budget_exceeded 写 quality.jsonl）。
// 不改 Phase 1 BudgetTracker 公开签名（零容忍约束）。

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// BudgetThreshold80Percent 总预算 80% 通知阈值（§8.3 三档介入）。
const BudgetThreshold80Percent = 80

// BudgetThreshold100Percent 总预算 100% 暂停阈值（§8.3 三档介入）。
const BudgetThreshold100Percent = 100

// Percent80DedupWindow 80% 两作用域去重窗口（5min，v0.3 改进 6）。
const Percent80DedupWindow = 5 * time.Minute

// ErrBudgetExceeded 总预算超限（暂停派发）。
var ErrBudgetExceeded = fmt.Errorf("budget_tracker: total budget exceeded (used + estimated > total)")

// ErrPersistFailed 持久化失败（致命反例 5：降级到内存保留）。
var ErrPersistFailed = fmt.Errorf("budget_tracker: persist state.json failed (degraded to memory-only)")

// SubtaskInfo 派发前子任务信息（CheckBeforeDispatch 输入，简化版 SubTask）。
// 与现有 context_contract.go SubTaskDef 区分：SubtaskInfo 是预算检查输入，不含依赖图字段。
type SubtaskInfo struct {
	TaskID   string
	TaskType string
	Model    string
}

// BudgetStateExt 单次编排任务的预算追踪状态（内存 + 持久化到 state.json）。
// 与现有 budget.go 的 BudgetTracker struct 区分：BudgetStateExt 是完整状态对象（含 OrchestrationID/Estimates/Exceeded）。
// 落地 §4.3 BudgetState 6 字段（v0.3 改进 2：notifyOnce + notified80 移出锁外，sync.Once 保证 80% 只通知 1 次）。
// 注意：sync 原语（mu/notifyOnce/notified80/last80NotifyTime）已移至 BudgetTrackerExt，
// BudgetStateExt 是纯数据结构（可安全拷贝 + 序列化，vet lock-copy 安全）。
//
// ★ 2026-10-09：TotalCostCents/UsedCostCents 已删 —— 只有 token 轴。
type BudgetStateExt struct {
	OrchestrationID  string              `json:"orchestration_id"`  // 关联 QualityRecord.OrchestrationID
	TotalTokensCap   int64               `json:"total_tokens_cap"`  // 编排任务总 token 上限（0=不限）
	UsedTokens       int64               `json:"used_tokens"`       // 已消耗 token（增量累加，provider 真报）
	SubtaskEstimates map[string]Estimate `json:"subtask_estimates"` // 子任务 ID → 派发前预估
	ExceededSubtasks []string            `json:"exceeded_subtasks"` // 已标记 budget_exceeded 的子任务 ID
}

// Estimate 派发前预估（落地 §8.3 line 989 "已用 + Σ预估 > 总预算"）。
//
// ★ 2026-10-09：EstimatedCost 已删 —— 预估只给 token。
type Estimate struct {
	TaskID          string `json:"task_id"`
	EstimatedTokens int64  `json:"estimated_tokens"`
	Model           string `json:"model"`     // 选定模型
	TaskType        string `json:"task_type"` // 用于反查 ModelProfile
}

// BudgetResultExt 子任务完成后的预算检查结果（落地 §8.3 line 983 budget_exceeded 独立字段）。
// BASE 变更：BudgetExceeded + ExceedReason 写入 quality.jsonl 的 budget_exceeded + exceed_reason 字段。
//
// ★ 2026-10-09：ActualCostCents 已删 —— 只有 ActualTokens。
type BudgetResultExt struct {
	TaskID         string `json:"task_id"`
	ActualTokens   int64  `json:"actual_tokens"`
	BudgetExceeded bool   `json:"budget_exceeded"` // 独立字段，不覆盖 l0_pass（§8.3 line 983）
	ExceedReason   string `json:"exceed_reason"`   // tokens_exceeded
}

// TerminateDecision 早期终止决策（委托 EarlyTerminator，桶 4）。
type TerminateDecision struct {
	ShouldTerminate bool
	Reason          string
	Checkpoint      string // "20%" / "50%" / "80%"
}

// ExceedReason 枚举（§8.3 超限原因）。
// ★ 2026-10-09：cost_exceeded / both 已删 —— 只有 token 一条轴。
const (
	ExceedReasonTokens = "tokens_exceeded"
)

// BudgetTrackerExt 编排任务预算追踪器扩展（Phase 5 §4.1/§4.3）。
// 职责：
// - 派发前预估：CheckBeforeDispatch 调 ProfileUpdater.QueryAvgTokens，超总预算返回 ErrBudgetExceeded
// - 80% 通知：sync.Once 保证同编排任务只通知 1 次，锁外执行（防死锁，v0.3 改进 2）
// - 事后检查：RecordActual 检查实际 tokens，超限标记 budget_exceeded 写 quality.jsonl
// - 持久化：Snapshot 到 state.json，失败降级（致命反例 5：内存保留 + 告警 + 完成后重写）
//
// 并发安全：sync.Mutex 保护 UsedCostCents/SubtaskEstimates，sync.Once 保证 80% 通知幂等，
// atomic.Bool 保证 notified80 无锁读（致命反例 3 关联）。
// 与现有 budget.go BudgetTracker struct 共存，不改 Phase 1 公开签名。
type BudgetTrackerExt struct {
	state          *BudgetStateExt // 预算状态（纯数据，无锁）
	profileUpdater ProfileUpdater  // ProfileUpdater 注入（QueryAvgTokens 调用）
	qualityStore   *QualityStore   // QualityStore 注入（budget_exceeded 写 quality.jsonl）
	notifier       Notifier        // 异步通知（80% + 持久化失败告警用）
	statePath      string          // state.json 路径（持久化用，""=不持久化）

	// 并发控制（从 BudgetStateExt 移出，避免 lock-copy vet 警告）
	mu               sync.Mutex  // 保护 state 字段（UsedTokens/SubtaskEstimates 等）
	notifyOnce       sync.Once   // 保证 80% 只通知 1 次（sync.Once）
	notified80       atomic.Bool // 80% 是否已通知（atomic，防竞态）
	last80NotifyTime time.Time   // 80% 两作用域去重（5min 窗口合并）

	// 持久化失败状态（致命反例 5）
	persistFailed   atomic.Bool // 是否持久化失败（降级模式）
	persistFailedAt time.Time   // 持久化失败时间（完成后强制重写）

	// 80% 通知 channel（注册式，v0.2 改进 10）
	notifyCh chan<- struct{} // 80% 通知 channel（调用方管理生命周期）

	// EarlyTerminator 引用（CheckEarlyTermination 委托）
	earlyTerminator *EarlyTerminator // 可为 nil（测试或简化模式）
}

// NewBudgetTrackerExt 创建预算追踪器扩展。
// state 是初始预算状态（OrchestrationID + TotalTokensCap 等）。
// profileUpdater 可为 nil（降级用静态矩阵）；qualityStore 可为 nil（不写 quality.jsonl）；
// notifier 可为 nil（告警丢弃）；statePath 为 "" 时不持久化（仅内存）。
func NewBudgetTrackerExt(state *BudgetStateExt, profileUpdater ProfileUpdater, qualityStore *QualityStore, notifier Notifier, statePath string) *BudgetTrackerExt {
	if state == nil {
		state = &BudgetStateExt{
			SubtaskEstimates: make(map[string]Estimate),
		}
	}
	if state.SubtaskEstimates == nil {
		state.SubtaskEstimates = make(map[string]Estimate)
	}
	return &BudgetTrackerExt{
		state:          state,
		profileUpdater: profileUpdater,
		qualityStore:   qualityStore,
		notifier:       notifier,
		statePath:      statePath,
	}
}

// Register80PercentNotifier 注册 80% 通知 channel（v0.2 改进 10）。
// 编排任务总预算达 80% 时向 ch 发送 struct{}{}（落地 §8.3 line 984）。
// ch 生命周期由调用方（编排器）管理，BudgetTracker 只在 80% 阈值触发时发送。
func (b *BudgetTrackerExt) Register80PercentNotifier(ch chan<- struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifyCh = ch
}

// SetEarlyTerminator 注入 EarlyTerminator 引用（CheckEarlyTermination 委托用）。
func (b *BudgetTrackerExt) SetEarlyTerminator(et *EarlyTerminator) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.earlyTerminator = et
}

// CheckBeforeDispatch 派发前预估检查（落地 §8.3 line 987-989）。
// 算法：
// - 锁内：遍历 subtasks 调 ProfileUpdater.QueryAvgTokens 累加预估 token
// - 冷启动（EffectiveSampleCount<10）：降级用静态 token 矩阵（§5.2）
// - 检查 UsedTokens + totalEstimatedTokens > TotalTokensCap → 返回 false（暂停派发）
// - 检查 80% 阈值：UsedTokens + totalEstimatedTokens > TotalTokensCap*80/100 → shouldNotify80=true
// - 锁外：sync.Once + select+default 非阻塞发送 80% 通知（v0.3 改进 2：防死锁）
//
// ★ 2026-10-09：判定**只按 token 一条轴**；原 `UsedCostCents + Σcost > TotalCostCents`
// 已删（那是拿自编单价当真值）。
//
// 返回 true=可派发，false=超预算暂停派发。
// 致命反例 3：并发 CheckBeforeDispatch 无死锁（锁内不调 notify，锁外调 sync.Once）。
func (b *BudgetTrackerExt) CheckBeforeDispatch(ctx context.Context, subtasks []SubtaskInfo) (bool, error) {
	var totalEstimatedTokens int64
	var shouldNotify80 bool

	b.mu.Lock()
	for _, st := range subtasks {
		// v0.2 改进 1：调用 ProfileUpdater 内存查询，不直接调 N2 反查接口
		avgTokens, err := b.queryAvgTokensSafe(st.TaskType, st.Model)
		if err != nil {
			// 冷启动或反查失败降级：用静态 token 矩阵（§5.1/§5.2）
			est := b.coldStartEstimate(st)
			b.state.SubtaskEstimates[st.TaskID] = est
			totalEstimatedTokens += est.EstimatedTokens
			continue
		}
		b.state.SubtaskEstimates[st.TaskID] = Estimate{
			TaskID:          st.TaskID,
			EstimatedTokens: avgTokens,
			Model:           st.Model,
			TaskType:        st.TaskType,
		}
		totalEstimatedTokens += avgTokens
	}

	// 检查总预算（§8.3 line 989）：唯一判据 = token
	if b.state.UsedTokens+totalEstimatedTokens > b.state.TotalTokensCap {
		b.mu.Unlock()
		return false, ErrBudgetExceeded
	}

	// 80% 阈值通知（§8.3 line 984）
	// v0.2 改进 11：此 80% 是编排总预算维度（token），与 §4.6.1 早期终止的 80%（子任务 tokens_cap）是不同作用域
	if b.state.TotalTokensCap > 0 {
		threshold80 := b.state.TotalTokensCap * BudgetThreshold80Percent / 100
		if b.state.UsedTokens+totalEstimatedTokens > threshold80 {
			shouldNotify80 = true
		}
	}
	b.mu.Unlock() // 锁外执行通知（v0.3 改进 2：防死锁）

	// 锁外通知（sync.Once 保证同编排任务只通知 1 次）
	if shouldNotify80 {
		b.notify80PercentOnce()
	}

	return true, nil
}

// queryAvgTokensSafe 安全调用 ProfileUpdater.QueryAvgTokens（profileUpdater 为 nil 时降级）。
func (b *BudgetTrackerExt) queryAvgTokensSafe(taskType, model string) (int64, error) {
	if b.profileUpdater == nil {
		return 0, ErrColdStart
	}
	return b.profileUpdater.QueryAvgTokens(taskType, model)
}

// coldStartEstimate 冷启动 + 反查失败降级估算（§5.1/§5.2）。
// 落地总纲 §8.2 line 956-959 静态 **token** 矩阵（原按模型给"美分单价"的 staticCostByModel 已删）。
func (b *BudgetTrackerExt) coldStartEstimate(st SubtaskInfo) Estimate {
	return Estimate{
		TaskID:          st.TaskID,
		EstimatedTokens: b.staticTokensByModel(st.Model),
		Model:           st.Model,
		TaskType:        st.TaskType,
	}
}

// staticTokensByModel 按模型返回静态 token 上限。
func (b *BudgetTrackerExt) staticTokensByModel(model string) int64 {
	switch {
	case contains(model, "haiku"):
		return 30000
	case contains(model, "sonnet"):
		return 100000
	case contains(model, "opus"):
		return 200000
	default:
		return 50000
	}
}

// notify80PercentOnce 80% 通知（sync.Once 保证幂等，锁外执行防死锁）。
// 落地 §8.3 line 984 + v0.3 改进 2（锁外）+ v0.3 改进 6（5min 去重，编排总预算 vs 子任务 tokens_cap）。
// 致命反例 6：两作用域同时触发时 5min 窗口合并（按编排总预算优先）。
func (b *BudgetTrackerExt) notify80PercentOnce() {
	// sync.Once 保证同编排任务只通知 1 次（sync.Once 内部已并发安全）
	b.notifyOnce.Do(func() {
		b.mu.Lock()
		now := time.Now()
		// 5min 去重窗口（v0.3 改进 6：两作用域合并）
		if !b.last80NotifyTime.IsZero() && now.Sub(b.last80NotifyTime) < Percent80DedupWindow {
			b.mu.Unlock()
			return // 5min 内已通知，跳过
		}
		b.last80NotifyTime = now
		ch := b.notifyCh
		notifier := b.notifier
		b.mu.Unlock()

		// 非阻塞发送到 channel（无消费者时不阻塞）
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
				// channel 满或无消费者，降级到 Notifier
				if notifier != nil {
					notifier.NotifyAsync("编排任务总预算已达 80%，建议检查是否加预算/终止/降级")
				}
			}
		} else if notifier != nil {
			notifier.NotifyAsync("编排任务总预算已达 80%，建议检查是否加预算/终止/降级")
		}
		// atomic 标记已通知
		b.notified80.Store(true)
	})
}

// Is80PercentNotified 80% 是否已通知（atomic 读，无锁）。
func (b *BudgetTrackerExt) Is80PercentNotified() bool {
	return b.notified80.Load()
}

// RecordActual 事后检查（落地 §8.3 line 990 + 983 budget_exceeded 标记）。
// v0.2 致命 1 修复：BudgetExceeded 写入 quality.jsonl 的 budget_exceeded 字段（BASE 变更）。
// 算法：
// - 锁内：累加 UsedTokens，检查是否超 budget_hint（TaskContext.BudgetHint.TokensCap）
// - 超限 → BudgetExceeded=true + ExceedReason=tokens_exceeded
// - 锁外：写 quality.jsonl 的 budget_exceeded + exceed_reason 字段
//
// ★ 2026-10-09：原名 RecordActualCost、带 actualCostCents 入参 —— 已删「钱」维。
//
// budget_exceeded 是独立字段，不覆盖 l0_pass（§8.3 line 983 关键约束）。
// qualityStore 为 nil 时跳过持久化（仅内存）。
func (b *BudgetTrackerExt) RecordActual(ctx context.Context, agentID string, actualTokens int64, budgetHint BudgetHint) (BudgetResultExt, error) {
	result := BudgetResultExt{
		ActualTokens: actualTokens,
	}

	// 检查是否超 budget_hint（唯一判据 = token）
	if budgetHint.TokensCap > 0 && actualTokens > int64(budgetHint.TokensCap) {
		result.BudgetExceeded = true
		result.ExceedReason = ExceedReasonTokens
	}

	// 锁内累加 + 标记 exceeded subtask
	b.mu.Lock()
	b.state.UsedTokens += actualTokens
	if result.BudgetExceeded {
		b.state.ExceededSubtasks = append(b.state.ExceededSubtasks, agentID)
	}
	b.mu.Unlock()

	// 锁外：写 quality.jsonl 的 budget_exceeded + exceed_reason 字段
	// 致命反例 5 关联：持久化失败不阻塞主流程
	if b.qualityStore != nil && result.BudgetExceeded {
		rec := QualityRecord{
			TaskID:         agentID,
			L0Pass:         true, // L0 独立判定（不覆盖，§8.3 line 983）
			BudgetExceeded: true,
			ExceedReason:   result.ExceedReason,
			Timestamp:      time.Now().UTC().Format(time.RFC3339),
		}
		if err := b.qualityStore.Append(rec); err != nil {
			// 持久化失败不阻塞（致命反例 5：内存保留 + 告警）
			if b.notifier != nil {
				b.notifier.NotifyAsync(fmt.Sprintf("quality.jsonl 写入 budget_exceeded 失败: %v", err))
			}
		}
	}

	// 持久化 BudgetState 到 state.json（致命反例 5：失败降级）
	if b.statePath != "" {
		if err := b.persistState(); err != nil {
			// 致命反例 5：持久化失败 → 内存保留 + 告警 + 完成后重写
			b.persistFailed.Store(true)
			b.persistFailedAt = time.Now()
			if b.notifier != nil {
				b.notifier.NotifyAsync(fmt.Sprintf("state.json 持久化失败，已降级为内存模式: %v", err))
			}
		}
	}

	return result, nil
}

// cloneStateLocked 手动拷贝 BudgetStateExt 数据字段（纯数据结构，无锁，vet 安全）。
// 调用方必须持有 b.mu 锁。返回的副本包含独立的 map/slice（深拷贝）。
// sync 原语（mu/notifyOnce/notified80/last80NotifyTime）在 BudgetTrackerExt 上，不在此结构内。
func (b *BudgetTrackerExt) cloneStateLocked() BudgetStateExt {
	stateCopy := BudgetStateExt{
		OrchestrationID:  b.state.OrchestrationID,
		TotalTokensCap:   b.state.TotalTokensCap,
		UsedTokens:       b.state.UsedTokens,
		ExceededSubtasks: append([]string{}, b.state.ExceededSubtasks...),
	}
	// 深拷贝 SubtaskEstimates
	if b.state.SubtaskEstimates != nil {
		stateCopy.SubtaskEstimates = make(map[string]Estimate, len(b.state.SubtaskEstimates))
		for k, v := range b.state.SubtaskEstimates {
			stateCopy.SubtaskEstimates[k] = v
		}
	}
	return stateCopy
}

// persistState 持久化 BudgetState 到 state.json（原子写入）。
func (b *BudgetTrackerExt) persistState() error {
	if b.statePath == "" {
		return nil
	}
	b.mu.Lock()
	stateCopy := b.cloneStateLocked()
	b.mu.Unlock()

	if dir := filepath.Dir(b.statePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("budget_tracker: mkdir %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(stateCopy, "", "  ")
	if err != nil {
		return fmt.Errorf("budget_tracker: marshal state: %w", err)
	}
	tmp := b.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("budget_tracker: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, b.statePath); err != nil {
		return fmt.Errorf("budget_tracker: rename %s → %s: %w", tmp, b.statePath, err)
	}
	return nil
}

// ForceRewriteState 致命反例 5：编排任务完成后强制重写 state.json。
// 落地 §6.1 "持久化失败降级" + "完成后强制重写一次"。
// 若重写成功，清除 persistFailed 标志。
func (b *BudgetTrackerExt) ForceRewriteState() error {
	if b.statePath == "" {
		return nil
	}
	if err := b.persistState(); err != nil {
		return fmt.Errorf("ForceRewriteState: %w", err)
	}
	b.persistFailed.Store(false)
	return nil
}

// IsPersistFailed 持久化是否失败（致命反例 5 降级状态查询）。
func (b *BudgetTrackerExt) IsPersistFailed() bool {
	return b.persistFailed.Load()
}

// CheckEarlyTermination 早期终止检查（委托 EarlyTerminator，桶 4）。
// earlyTerminator 为 nil 时返回零值（不终止）。
func (b *BudgetTrackerExt) CheckEarlyTermination(ctx context.Context, taskID string) (TerminateDecision, error) {
	b.mu.Lock()
	et := b.earlyTerminator
	b.mu.Unlock()
	if et == nil {
		return TerminateDecision{}, nil
	}
	// 委托 EarlyTerminator（桶 4 实现）
	// 简化：调用 et.CheckAt80Percent
	shouldTerm, err := et.CheckAt80Percent(taskID, b.state.UsedTokens)
	if err != nil {
		return TerminateDecision{}, err
	}
	return TerminateDecision{
		ShouldTerminate: shouldTerm,
		Reason:          "80% checkpoint reached",
		Checkpoint:      "80%",
	}, nil
}

// Snapshot 取预算状态快照（断点续传用，§5.1 state.json 配合）。
// 返回状态副本（防外部修改内部状态）。
func (b *BudgetTrackerExt) Snapshot() BudgetStateExt {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 使用 cloneStateLocked 避免拷贝锁（vet 安全）
	return b.cloneStateLocked()
}

// UsedTokens 返回已用 token（诊断用）。
func (b *BudgetTrackerExt) UsedTokens() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state.UsedTokens
}

// ExceededSubtasks 返回已超预算子任务列表（诊断用）。
func (b *BudgetTrackerExt) ExceededSubtasks() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string{}, b.state.ExceededSubtasks...)
}
