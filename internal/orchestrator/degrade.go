// degrade.go — H10 Phase 5 桶 5 降级管理器（落地 §5 四类降级策略）。
// REF-外部-arxiv:2306.05685-budget 概念在资源不足时降级；落地：§5.1 反查失败降级 + §5.2 冷启动降级
// 反查失败降级（cost 列 null + 静态矩阵 + 1h 重试）；落地：DegradeOnReverseQueryFail
// （EffectiveSampleCount<10 + 静态矩阵）；落地：DegradeOnColdStart
// （3 次禁用 80%）；落地：DegradeOnEarlyTerminationMiscalibrate
// （锁内上限检查 + atomic 计数器）；落地：DegradeOnBudgetConcurrencyRace
// （内存保留 + 告警 + 完成后重写）；落地：BudgetTrackerExt.ForceRewriteState（已实现）
// SNAPSHOT: pkg=internal/orchestrator; type=DegradeManager; degrade={reverse_query_fail:null cost+静态矩阵+1h重试,cold_start:EffectiveSampleCount<10+静态矩阵,early_terminate_miscalibrate:3次禁用80%,budget_race:锁内上限检查+atomic计数器}; persist_failure=内存保留+告警+完成后重写; concurrency=sync.Mutex(degradeMu)

package orchestrator

import (
	"fmt"
	"sync"
	"time"
)

// RetryableProfileUpdater 可重试的画像表更新器接口（Phase 5 §5.1 反查失败重试）。
// 组合 ProfileUpdater + RetryReverseQuery，*profileUpdater 自动满足（已实现 RetryReverseQuery）。
// 落地 §5.1：DegradeOnReverseQueryFail 调用 RetryReverseQuery 触发 1h 后台重试。
type RetryableProfileUpdater interface {
	ProfileUpdater
	// RetryReverseQuery 反查重试（1h 周期，轻量）。
	RetryReverseQuery() error
}

// DegradeManager Phase 5 四类降级策略管理器（落地 §5）。
// 职责：
// - DegradeOnReverseQueryFail：N2 反查失败 → cost 列 null + 静态矩阵 + 1h 后台重试
// - DegradeOnColdStart：画像表冷启动 → 路由器静态矩阵 + L1 初版权重 + BudgetTracker 固定阈值
// - DegradeOnEarlyTerminationMiscalibrate：早期终止误判 → 禁用 80% 检查点
// - DegradeOnBudgetConcurrencyRace：预算并发竞态 → 锁内只做上限检查 + atomic 计数器
//
// 设计说明：DegradeManager 是门面（Facade），实际降级逻辑由各组件内部实现：
// - 反查失败降级：profileUpdater.QueryAvgCost 返回 ErrReverseQueryNotFound 时调用方降级
// - 冷启动降级：profileUpdater.GetProfile 返回 ErrColdStart 时调用方降级
// - 误判降级：earlyTerminator.miscalibrateCount 达阈值时 disabled80Percent=true
// - 并发竞态降级：budgetTracker.CheckBeforeDispatch 锁内只检查，锁外通知
//
// DegradeManager 作为统一入口，触发降级 + 记录日志 + 触发相关组件协同降级。
// 并发安全：degradeMu sync.Mutex 保护降级状态记录。
type DegradeManager struct {
	budgetTracker   *BudgetTrackerExt       // Phase 5 预算追踪器（IsPersistFailed + ForceRewriteState）
	profileUpdater  RetryableProfileUpdater // 画像表更新器（RetryReverseQuery 触发重试）
	earlyTerminator *EarlyTerminator        // 早期终止检查器（miscalibrateCount + disabled80Percent）
	notifier        Notifier                // 异步通知（降级告警用，可为 nil）

	// degradeMu 保护降级状态记录（防并发触发同种降级）
	degradeMu sync.Mutex

	// 降级状态记录（诊断用）
	reverseQueryFailCount int  // 反查失败降级次数
	coldStartCount        int  // 冷启动降级次数
	miscalibrateTriggered bool // 误判降级是否已触发
	budgetRaceCount       int  // 并发竞态降级次数

	// nowFunc 用于测试注入时间
	nowFunc func() time.Time
}

// NewDegradeManager 创建降级管理器。
// budgetTracker 可为 nil（跳过持久化失败降级）；profileUpdater 可为 nil（跳过反查失败重试）；
// earlyTerminator 可为 nil（跳过误判降级）；notifier 可为 nil（告警丢弃）。
// profileUpdater 类型为 RetryableProfileUpdater（组合 ProfileUpdater + RetryReverseQuery），
// *profileUpdater 已实现 RetryReverseQuery 自动满足，调用方传 nil 跳过反查失败重试。
func NewDegradeManager(budgetTracker *BudgetTrackerExt, profileUpdater RetryableProfileUpdater, earlyTerminator *EarlyTerminator, notifier Notifier) *DegradeManager {
	return &DegradeManager{
		budgetTracker:   budgetTracker,
		profileUpdater:  profileUpdater,
		earlyTerminator: earlyTerminator,
		notifier:        notifier,
		nowFunc:         time.Now,
	}
}

// SetNowFunc 注入时间函数（测试用）。
func (d *DegradeManager) SetNowFunc(f func() time.Time) {
	d.degradeMu.Lock()
	defer d.degradeMu.Unlock()
	d.nowFunc = f
}

// notify 异步通知（notifier 为 nil 时丢弃）。
func (d *DegradeManager) notify(message string) {
	if d.notifier != nil {
		d.notifier.NotifyAsync(message)
	}
}

// DegradeOnReverseQueryFail N2 反查失败降级（落地 §5.1）。
// 触发：ReverseQueryCost 返回 ErrReverseQueryNotFound（或 metrics 为 nil）。
// 降级：
// - 画像表 cost 列 null（profileUpdater.QueryAvgCost 返回 ErrReverseQueryNotFound 时调用方降级到静态矩阵）
// - BudgetTracker 用静态矩阵（BudgetTrackerExt.coldStartEstimate 已实现）
// - 后台 1h 重试（profileUpdater.RetryReverseQuery 已实现）
//
// agentID 是反查失败的子任务 ID，ts 是反查时间戳。
// profileUpdater 为 nil 时仅记录降级状态 + 通知。
func (d *DegradeManager) DegradeOnReverseQueryFail(agentID string, ts time.Time) {
	d.degradeMu.Lock()
	d.reverseQueryFailCount++
	count := d.reverseQueryFailCount
	d.degradeMu.Unlock()

	d.notify(fmt.Sprintf("降级触发：N2 反查失败 (agentID=%s, count=%d) → cost 列 null + 静态矩阵 + 1h 后台重试",
		agentID, count))

	// 触发 1h 后台重试（profileUpdater 为 nil 时跳过）
	if d.profileUpdater != nil {
		// RetryReverseQuery 是同步方法，实际应在后台 goroutine 执行
		// 此处简化为同步调用（实际编排器应异步调用）
		go func() {
			_ = d.profileUpdater.RetryReverseQuery()
		}()
	}
}

// DegradeOnColdStart 画像表冷启动降级（落地 §5.2）。
// 触发：EffectiveSampleCount<10（profileUpdater.GetProfile 返回 ErrColdStart）。
// 降级：
// - 路由器用静态能力矩阵
// - L1Scorer 用初版权重（DefaultL1WeightsExt 已实现）
// - BudgetTracker 用固定 token 阈值（BudgetTrackerExt.staticTokensByModel 已实现）
//
// taskType 和 model 是冷启动的画像表 key。
// 返回静态估算**用量（token）**（静态矩阵，便于调用方使用）。
//
// ★ 2026-10-09：返回值从"静态单价（美分）"改为"静态 token 上限"（删「钱」维）。
func (d *DegradeManager) DegradeOnColdStart(taskType, model string) (staticTokens int64) {
	d.degradeMu.Lock()
	d.coldStartCount++
	count := d.coldStartCount
	d.degradeMu.Unlock()

	// 静态 token 矩阵估算
	staticTokens = staticTokensByModelName(model)

	d.notify(fmt.Sprintf("降级触发：冷启动 (taskType=%s, model=%s, count=%d) → 静态矩阵 tokens=%d",
		taskType, model, count, staticTokens))

	return staticTokens
}

// DegradeOnEarlyTerminationMiscalibrate 早期终止误判降级（落地 §5.3）。
// 触发：连续 3 次误判（用户选继续），或编排器检测到误判模式后主动调用。
// 降级：禁用 80% 检查点（earlyTerminator.ForceDisable80Percent）。
//
// earlyTerminator 为 nil 时仅记录降级状态 + 通知。
// 注意：正常流程下 earlyTerminator.CheckAt80Percent 内部已自动处理误判计数，
// 本方法供外部强制触发降级（如编排器检测到误判模式后主动禁用）。
// B-P1 修复：旧实现空操作（仅设 flag + notify），现补实现调用 ForceDisable80Percent。
func (d *DegradeManager) DegradeOnEarlyTerminationMiscalibrate() {
	d.degradeMu.Lock()
	d.miscalibrateTriggered = true
	d.degradeMu.Unlock()

	d.notify("降级触发：早期终止误判 → 禁用 80% 检查点")

	// B-P1 修复：实际调用 ForceDisable80Percent 禁用 80% 检查点（旧实现空操作）
	if d.earlyTerminator != nil {
		d.earlyTerminator.ForceDisable80Percent()
	}
}

// DegradeOnBudgetConcurrencyRace 预算追踪并发竞态降级（落地 §5.4）。
// 触发：并行 3 子任务同时 CheckBeforeDispatch。
// 降级：
// - 锁内只做上限检查（BudgetTrackerExt.CheckBeforeDispatch 已实现：锁内检查，锁外通知）
// - 80%/100% 用 atomic 计数器（BudgetTrackerExt.notified80 atomic.Bool 已实现）
//
// budgetTracker 为 nil 时仅记录降级状态 + 通知。
// 注意：BudgetTrackerExt 已内置并发竞态防护（sync.Mutex + 锁外通知 + atomic.Bool），
// 本方法作为门面记录降级触发，实际防护由 BudgetTrackerExt 内部实现。
func (d *DegradeManager) DegradeOnBudgetConcurrencyRace() {
	d.degradeMu.Lock()
	d.budgetRaceCount++
	count := d.budgetRaceCount
	d.degradeMu.Unlock()

	d.notify(fmt.Sprintf("降级触发：预算并发竞态 (count=%d) → 锁内上限检查 + atomic 计数器", count))

	// budgetTracker 为 nil 时跳过
	// BudgetTrackerExt 已内置并发防护，此处无需额外操作
}

// DegradeOnPersistFailure 持久化失败降级（落地 §6.1 v0.3 建议 5）。
// 触发：BudgetTrackerExt.IsPersistFailed()=true。
// 降级：内存保留 + 告警 + 完成后强制重写（BudgetTrackerExt.ForceRewriteState 已实现）。
// 返回 error（重写失败时返回，便于调用方记录）。
func (d *DegradeManager) DegradeOnPersistFailure() error {
	d.degradeMu.Lock()
	defer d.degradeMu.Unlock()

	if d.budgetTracker == nil {
		return nil
	}

	if !d.budgetTracker.IsPersistFailed() {
		return nil // 未失败，无需降级
	}

	d.notify("降级触发：持久化失败 → 内存保留 + 告警 + 完成后强制重写")

	// 触发强制重写（致命反例 5）
	if err := d.budgetTracker.ForceRewriteState(); err != nil {
		return fmt.Errorf("degrade: ForceRewriteState failed: %w", err)
	}
	return nil
}

// ReverseQueryFailCount 返回反查失败降级次数（诊断用）。
func (d *DegradeManager) ReverseQueryFailCount() int {
	d.degradeMu.Lock()
	defer d.degradeMu.Unlock()
	return d.reverseQueryFailCount
}

// ColdStartCount 返回冷启动降级次数（诊断用）。
func (d *DegradeManager) ColdStartCount() int {
	d.degradeMu.Lock()
	defer d.degradeMu.Unlock()
	return d.coldStartCount
}

// MiscalibrateTriggered 返回误判降级是否已触发（诊断用）。
func (d *DegradeManager) MiscalibrateTriggered() bool {
	d.degradeMu.Lock()
	defer d.degradeMu.Unlock()
	return d.miscalibrateTriggered
}

// BudgetRaceCount 返回并发竞态降级次数（诊断用）。
func (d *DegradeManager) BudgetRaceCount() int {
	d.degradeMu.Lock()
	defer d.degradeMu.Unlock()
	return d.budgetRaceCount
}

// staticTokensByModelName 按模型名返回静态 **token** 上限（总纲 §8.2 line 956-959）。
//
// ★ 2026-10-09：原 `staticCostByModelName`（haiku=2¢/sonnet=8¢/opus=45¢）已删 ——
// 那是"我们自己编的单价"，不是 provider 真值。降级估算改走 token 轴（与 BudgetTrackerExt.staticTokensByModel 同构）。
func staticTokensByModelName(model string) int64 {
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
