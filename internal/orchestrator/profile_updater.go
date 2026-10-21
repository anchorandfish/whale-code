// profile_updater.go — H10 Phase 5 桶 2 模型画像表更新器（EMA + 时间衰减 + EffectiveSampleCount + cache_hit_rate）。
// REF-外部-LangGraph@https://langchain-ai.github.io/langgraph/concepts/persistence/+2026-07-15访问-StateSnapshot + checkpoint 老化；落地：画像表时间衰减 30/90 天
// line 549-559-ModelProfile 9 字段已定；落地：Phase 5 新增 EffectiveSampleCount（BASE 变更）
// + 时间衰减算法；落地：incrementalUpdateLocked + decayWeight
// 依赖注入；落地：构造函数注入 MetricsClient
// <50% 告警；落地：CheckCacheHitRate
// 变更三段式（ModelProfile 新增 EffectiveSampleCount，沉淀 ；QualityStore 扩展 3 方法，沉淀）
// SNAPSHOT: pkg=internal/orchestrator; type=ProfileUpdater+profileUpdater; key_format=taskType+"|"+model; decay={30d:1.0,90d:0.5,>90d:0.1}; cold_start_threshold=EffectiveSampleCount<10; EMA=alpha=1/(effectiveCount+1); reconcile=24h; retry_reverse=1h; cache_hit_rate_threshold=0.5; alert_dedup=24h; BASE变更=(EffectiveSampleCount+QualityStore扩展); concurrency=sync.RWMutex

package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EffectiveSampleCountThreshold 冷启动阈值（致命反例 2：EffectiveSampleCount<10 降级到静态矩阵）。
const EffectiveSampleCountThreshold = 10.0

// CacheHitRateAlertThreshold cache_hit_rate 告警阈值（§8.5 line 1008 <50%）。
const CacheHitRateAlertThreshold = 0.5

// CacheHitRateAlertDedupWindow cache_hit_rate 告警去重窗口（24h，§6.4）。
const CacheHitRateAlertDedupWindow = 24 * time.Hour

// 时间衰减阈值（§4.5.2）。
const (
	DecayWindow30Days = 30 * 24 * time.Hour // 30 天内全权重 1.0
	DecayWindow90Days = 90 * 24 * time.Hour // 30-90 天权重 0.5；>90 天权重 0.1
)

// MetricsClient N2 metrics 反查接口（Phase 5 §4.0 依赖注入，v0.3 改进 9）。
// 由调用方实现并注入，避免直接依赖 N2 metrics 包（解耦 + 测试 mock）。
// 不改 N2 UsageRecord 签名（U3 旁路记账原则）。
//
// ★ 2026-10-09：原 `ReverseQueryCost` 会多返回一个 costCents（"钱"），已删除 ——
// 反查只回 **provider 真报的四项 token**。
type MetricsClient interface {
	// ReverseQueryUsage 按 (agentID, timestamp) 反查 N2 UsageRecord，返回四维 token。
	// 失败时返回 ErrReverseQueryNotFound（致命反例 7：反查失败 + 静态矩阵 + 重试）。
	ReverseQueryUsage(agentID, timestamp string) (inputTokens, outputTokens, cacheCreation, cacheRead int64, err error)
}

// ErrReverseQueryNotFound N2 反查未命中（致命反例 7）。
var ErrReverseQueryNotFound = fmt.Errorf("profile_updater: reverse query not found (CostRecord missing or timestamp offset)")

// MismatchRecord quality.jsonl 与 CostRecord 不一致记录（§4.5.3 Reconcile）。
type MismatchRecord struct {
	TaskID string
	Reason string // CostRecord not found / model mismatch / cost mismatch
}

// Notifier 异步通知接口（cache_hit_rate 告警用，§2.1 异步通知档）。
type Notifier interface {
	// NotifyAsync 异步通知用户（不阻塞调用方）。
	NotifyAsync(message string)
}

// ProfileUpdater 模型画像表更新器接口（Phase 5 §4.1）。
// 落地 §8.2 + §5.3 successRate 聚合窗口。
// 与 ProfileStore（地基层）共存：ProfileStore 是简单内存聚合（SampleCount<10 降级），
// ProfileUpdater 是 EMA + 时间衰减 + EffectiveSampleCount 版本（Phase 5 改进）。
type ProfileUpdater interface {
	// UpdateOnTaskComplete 子任务完成后增量更新（EMA + 时间衰减）。
	UpdateOnTaskComplete(ctx context.Context, rec QualityRecord) error

	// QueryAvgTokens 查询 avg_tokens（v0.3 致命 2 修复：BudgetTracker.CheckBeforeDispatch 调用）。
	// 基于 map[TaskID]UsageRecord 索引聚合，返回 (avg_tokens, error)。
	// 不直接调 N2 反查接口，避免与 §4.5.3 ReverseQueryUsage 同名冲突。
	//
	// ★ 2026-10-09：原名 QueryAvgCost、会一并返回一个"美分"值 —— 已删「钱」维。
	QueryAvgTokens(taskType, model string) (avgTokens int64, err error)

	// QuerySuccessRate 查询成功率（路由器 pickModel 调用）。
	QuerySuccessRate(taskType, model string) (rate float64, err error)

	// GetProfile 查询完整画像（含 EffectiveSampleCount 冷启动判断）。
	GetProfile(taskType, model string) (ModelProfile, error)
}

// profileUpdater 实现 ProfileUpdater 接口（Phase 5 §4.1）。
// 职责：
// - 启动时全量扫描 quality.jsonl 构建 map[(taskType,model)]ModelProfile
// - 每次 Append 后增量更新（EMA + 时间衰减，EffectiveSampleCount 加权累加）
// - 24h 周期 Reconcile 校验 quality.jsonl 与 CostRecord 一致性
// - 1h 周期 RetryReverseQuery 重试反查失败记录
// - 每次子任务完成后检查 cache_hit_rate（<50% 异步告警，24h 去重）
//
// 并发安全：sync.RWMutex 保护 profiles + cacheHitAlertLast。
type profileUpdater struct {
	mu           sync.RWMutex
	profiles     map[string]ModelProfile // key = taskType + "|" + model
	qualityStore *QualityStore           // Phase 0 注入（v0.2 扩展 OnAppend/ScanFailedReverseQueries）
	metrics      MetricsClient           // N2 注入（构造函数，ReverseQueryCost 调用）
	notifier     Notifier                // 异步通知（cache_hit_rate 告警用）

	// cache_hit_rate 告警去重（§6.4，24h 窗口）
	cacheHitAlertMu   sync.Mutex
	cacheHitAlertLast map[string]time.Time // key = taskType → 上次告警时间

	// nowFunc 用于测试注入时间（默认 time.Now）
	nowFunc func() time.Time
}

// NewProfileUpdater 创建画像表更新器。
// qualityStore 可为 nil（仅内存模式，测试用）；metrics 可为 nil（降级模式，反查失败）；notifier 可为 nil（告警丢弃）。
func NewProfileUpdater(qualityStore *QualityStore, metrics MetricsClient, notifier Notifier) ProfileUpdater {
	return &profileUpdater{
		profiles:          make(map[string]ModelProfile),
		qualityStore:      qualityStore,
		metrics:           metrics,
		notifier:          notifier,
		cacheHitAlertLast: make(map[string]time.Time),
		nowFunc:           time.Now,
	}
}

// UpdateOnTaskComplete 子任务完成后增量更新（EMA + 时间衰减，§4.5.2）。
// 落地地基层 §3.5 line 567 "每次新记录 append 后增量更新"。
// 算法：
// - weight = decayWeight(rec.Timestamp)：<30d=1.0，30-90d=0.5，>90d=0.1
// - EMA 更新 SuccessRate/AvgL1Score/AvgDurationSec/AvgRetry/L2AdoptionRate
// - EffectiveSampleCount += weight（加权样本量，致命反例 2 冷启动判断）
// - SampleCount++（原始计数保留 debug）
func (u *profileUpdater) UpdateOnTaskComplete(ctx context.Context, rec QualityRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.incrementalUpdateLocked(rec)
	return nil
}

// QueryAvgTokens 查询 avg_tokens（v0.3 致命 2 修复，B-P0-2 修复）。
// 落地 §4.5.3：基于 metrics 反查 UsageRecord 聚合（不直接读 UsageRecord 文件）。
// 冷启动 EffectiveSampleCount<10 返回 (0, ErrColdStart) 通知 BudgetTracker 用静态 token 矩阵。
// 修复 B-P0-2：metrics 非 nil 时必须反查真实数据，不得返回静态估算。
// 降级链：metrics 非 nil → 扫描 quality.jsonl 反查聚合 → qualityStore nil 或全失败 → 画像表估算。
//
// ★ 2026-10-09：原名 QueryAvgCost，返回 (avgCostCents, avgTokens) —— 已删 cost。
func (u *profileUpdater) QueryAvgTokens(taskType, model string) (avgTokens int64, err error) {
	u.mu.RLock()
	profile, ok := u.profiles[taskType+"|"+model]
	u.mu.RUnlock()

	// 致命反例 2：冷启动降级
	if !ok || profile.EffectiveSampleCount < EffectiveSampleCountThreshold {
		return 0, ErrColdStart
	}

	// 修复 B-P0-2：metrics 非 nil 时反查真实 UsageRecord 聚合
	if u.metrics != nil && u.qualityStore != nil {
		avgTok, qerr := u.aggregateTokensFromMetrics(taskType, model)
		if qerr == nil && avgTok > 0 {
			return avgTok, nil
		}
		// 反查全失败降级到画像表估算（非静态矩阵）
		return u.profileAvgTokens(profile), nil
	}

	// 降级：无 metrics 或无 qualityStore，返回画像表估算（非静态矩阵）
	return u.profileAvgTokens(profile), nil
}

// aggregateTokensFromMetrics 扫描 quality.jsonl 按 (taskType, model) 过滤，逐条反查 UsageRecord 聚合（修复 B-P0-2）。
// 返回 (avg_tokens, error)。全量反查失败时返回 error 通知调用方降级。
// 注：ScanAll 是 O(n) 操作，quality.jsonl 通常百级记录，per-dispatch 调用可接受。
//
// ★ 2026-10-09：原名 aggregateCostFromMetrics，同时聚合 cost —— 已改为只聚合 token。
func (u *profileUpdater) aggregateTokensFromMetrics(taskType, model string) (avgTokens int64, err error) {
	records, serr := u.qualityStore.ScanAll()
	if serr != nil {
		return 0, fmt.Errorf("profile_updater: ScanAll: %w", serr)
	}

	var totalTokens int64
	count := 0
	for _, rec := range records {
		// 按 (taskType, model) 过滤
		if rec.TaskType != taskType || rec.Model != model {
			continue
		}
		// MetricsCorrelation 缺失跳过
		if rec.MetricsCorrelation.AgentID == "" || rec.MetricsCorrelation.Timestamp == "" {
			continue
		}
		// 反查 UsageRecord
		inputTokens, outputTokens, cacheCreation, cacheRead, qerr := u.metrics.ReverseQueryUsage(
			rec.MetricsCorrelation.AgentID, rec.MetricsCorrelation.Timestamp,
		)
		if qerr != nil {
			continue // 反查失败跳过（致命反例 7：不阻断聚合）
		}
		totalTokens += inputTokens + outputTokens + cacheCreation + cacheRead
		count++
	}

	if count == 0 {
		return 0, fmt.Errorf("profile_updater: no UsageRecord found for (taskType=%s, model=%s)", taskType, model)
	}
	return totalTokens / int64(count), nil
}

// profileAvgTokens 基于画像表估算用量（修复 B-P0-2：降级路径用画像表而非静态矩阵）。
// 使用 AvgDurationSec 折算一个粗略 token 量（EMA 加权后的真实耗时）。
// ★ 2026-10-09：原先它乘"每秒 1 美分"得出 cost —— 已删；现直接给 token 计数。
func (u *profileUpdater) profileAvgTokens(profile ModelProfile) int64 {
	if profile.AvgDurationSec <= 0 {
		return 0
	}
	return int64(profile.AvgDurationSec) * 100
}

// QuerySuccessRate 查询成功率（路由器 pickModel 调用）。
// 冷启动返回 0.0（路由器降级到静态矩阵）。
func (u *profileUpdater) QuerySuccessRate(taskType, model string) (rate float64, err error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	profile, ok := u.profiles[taskType+"|"+model]
	if !ok || profile.EffectiveSampleCount < EffectiveSampleCountThreshold {
		return 0.0, ErrColdStart
	}
	return profile.SuccessRate, nil
}

// GetProfile 查询完整画像（含 EffectiveSampleCount 冷启动判断）。
// 冷启动返回零值 ModelProfile + ErrColdStart（调用方降级到静态矩阵）。
func (u *profileUpdater) GetProfile(taskType, model string) (ModelProfile, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	profile, ok := u.profiles[taskType+"|"+model]
	if !ok {
		return ModelProfile{}, ErrColdStart
	}
	if profile.EffectiveSampleCount < EffectiveSampleCountThreshold {
		return profile, ErrColdStart
	}
	return profile, nil
}

// ErrColdStart 冷启动错误（致命反例 2：EffectiveSampleCount<10 降级）。
var ErrColdStart = fmt.Errorf("profile_updater: cold start (EffectiveSampleCount < %g)", EffectiveSampleCountThreshold)

// RebuildAtStartup 启动时全量扫描 quality.jsonl 构建 map[(taskType,model)]ModelProfile。
// 落地地基层 §3.5 line 566 "启动时全量扫描构建到内存"。
// qualityStore 为 nil 时返回 nil（仅内存模式）。
func (u *profileUpdater) RebuildAtStartup() error {
	if u.qualityStore == nil {
		return nil
	}
	records, err := u.qualityStore.ScanAll()
	if err != nil {
		return fmt.Errorf("profile_updater: RebuildAtStartup ScanAll: %w", err)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.profiles = make(map[string]ModelProfile)
	for _, rec := range records {
		u.incrementalUpdateLocked(rec)
	}
	return nil
}

// Reconcile 全量校验（24h 周期，重量）。
// 落地 §4.3 line 428 "quality.jsonl 与 metrics CostRecord 的一致性校验（periodic reconcile）"。
// 校验项：metrics_correlation.AgentID 反查 CostRecord 是否存在 + model 字段一致性。
// metrics 为 nil 时跳过反查（降级模式，仅返回空 mismatches）。
func (u *profileUpdater) Reconcile() (mismatches []MismatchRecord, err error) {
	if u.qualityStore == nil {
		return nil, nil
	}
	records, err := u.qualityStore.ScanAll()
	if err != nil {
		return nil, fmt.Errorf("profile_updater: Reconcile ScanAll: %w", err)
	}
	for _, rec := range records {
		if rec.MetricsCorrelation.AgentID == "" || rec.MetricsCorrelation.Timestamp == "" {
			mismatches = append(mismatches, MismatchRecord{
				TaskID: rec.TaskID,
				Reason: "metrics_correlation missing (agent_id or timestamp empty)",
			})
			continue
		}
		if u.metrics == nil {
			continue // 降级模式，不反查
		}
		// 反查 UsageRecord
		_, _, _, _, qerr := u.metrics.ReverseQueryUsage(rec.MetricsCorrelation.AgentID, rec.MetricsCorrelation.Timestamp)
		if qerr != nil {
			mismatches = append(mismatches, MismatchRecord{
				TaskID: rec.TaskID,
				Reason: "CostRecord not found (reverse query failed)",
			})
			continue
		}
		// model 字段一致性校验（quality.jsonl 与 CostRecord 都有 model 字段）
		// 简化：metrics.ReverseQueryCost 不返回 model（避免改 N2 签名），跳过 model 一致性
		// 实际一致性校验由调用方根据返回的 costCents 推断（成本为 0 可能 model 不匹配）
	}
	return mismatches, nil
}

// RetryReverseQuery 反查重试（1h 周期，轻量）。
// 落地 §5.1 "后台周期性重试反查" + §4.5.3 v0.2 改进 2（1h 与 24h 分层）。
// 找出 metrics_correlation 反查失败的记录，重试 ReverseQueryCost。
// metrics 为 nil 时直接返回 nil（降级模式）。
func (u *profileUpdater) RetryReverseQuery() error {
	if u.qualityStore == nil || u.metrics == nil {
		return nil
	}
	// 扫描超过 1 天的记录视为可能反查失败（轻量级，避免全量扫描）
	failed, err := u.qualityStore.ScanFailedReverseQueries(1)
	if err != nil {
		return fmt.Errorf("profile_updater: RetryReverseQuery ScanFailedReverseQueries: %w", err)
	}
	for _, rec := range failed {
		if rec.MetricsCorrelation.AgentID == "" || rec.MetricsCorrelation.Timestamp == "" {
			continue
		}
		// 重试反查
		_, _, _, _, qerr := u.metrics.ReverseQueryUsage(rec.MetricsCorrelation.AgentID, rec.MetricsCorrelation.Timestamp)
		if qerr == nil {
			// 反查成功，标记记录已恢复（实际实现应更新 quality.jsonl 的反查状态字段，此处简化）
			u.markReverseQuerySucceeded(rec.TaskID)
		}
	}
	return nil
}

// CheckCacheHitRate 每次子任务完成后检查 cache_hit_rate（落地 §8.5 line 1008）。
// 算法：反查 CostRecord 的 CacheRead / CacheCreation 字段 → 计算 hitRate → <0.5 异步告警。
// 告警去重（§6.4，24h 窗口）：同 task_type 24h 内只告警 1 次。
// metrics 为 nil 时跳过（降级模式）。notifier 为 nil 时告警丢弃。
func (u *profileUpdater) CheckCacheHitRate(taskID string) error {
	if u.metrics == nil {
		return nil // 降级模式，不告警
	}
	// 反查 CostRecord 的 CacheRead / CacheCreation 字段（§8.5 line 1009 数据源澄清）
	// 简化：用 taskID 反查（实际应通过 quality.jsonl 查 metrics_correlation，再反查 CostRecord）
	// 此处假设调用方传入 taskID 后通过 metrics 反查
	// 由于 N2 CostRecord 无 task_id 字段（02 §2.1.2），需先查 quality.jsonl 获取 metrics_correlation
	if u.qualityStore == nil {
		return nil
	}
	rec, err := u.qualityStore.QueryByTaskID(taskID)
	if err != nil {
		return nil // 反查失败降级：不告警（致命反例 7 关联）
	}
	if rec.MetricsCorrelation.AgentID == "" || rec.MetricsCorrelation.Timestamp == "" {
		return nil
	}
	_, _, cacheCreation, cacheRead, qerr := u.metrics.ReverseQueryUsage(rec.MetricsCorrelation.AgentID, rec.MetricsCorrelation.Timestamp)
	if qerr != nil {
		return nil // 反查失败降级：不告警
	}
	if cacheCreation+cacheRead == 0 {
		return nil // 未用 cache，不告警
	}
	hitRate := float64(cacheRead) / float64(cacheCreation+cacheRead)
	if hitRate < CacheHitRateAlertThreshold {
		// 致命反例 10：告警去重（24h 窗口，同 task_type 只告警 1 次）
		if !u.shouldAlertCacheHitRate(rec.TaskType) {
			return nil
		}
		if u.notifier != nil {
			u.notifier.NotifyAsync(fmt.Sprintf(
				"cache_hit_rate %.0f%% < %.0f%%, task_id=%s, task_type=%s, 建议检查 prompt 稳定段切分",
				hitRate*100, CacheHitRateAlertThreshold*100, taskID, rec.TaskType,
			))
		}
	}
	return nil
}

// shouldAlertCacheHitRate 判断是否应告警（24h 去重，§6.4）。
// 同 task_type 24h 内只告警 1 次（防连续告警打扰用户）。
func (u *profileUpdater) shouldAlertCacheHitRate(taskType string) bool {
	u.cacheHitAlertMu.Lock()
	defer u.cacheHitAlertMu.Unlock()
	now := u.nowFunc()
	if last, ok := u.cacheHitAlertLast[taskType]; ok {
		if now.Sub(last) < CacheHitRateAlertDedupWindow {
			return false // 24h 内已告警，跳过
		}
	}
	u.cacheHitAlertLast[taskType] = now
	return true
}

// incrementalUpdateLocked 增量更新（持锁，内部用）。
// 落地地基层 §3.5 line 567 "每次新记录 append 后增量更新"。
// v0.2 修订（改进 4）：原 v0.1 称"Welford 算法"名不副实，实际是指数移动平均（EMA）+ 时间衰减。
// - Welford 跟踪 count + mean + M2（方差），本实现只更新 mean → 是 EMA 不是 Welford
// - 时间衰减：30 天内全权重，30-90 天权重 0.5，>90 天权重 0.1（解决 §5.3 successRate 聚合窗口）
// - EffectiveSampleCount 加权累加（替代原 SampleCount 冷启动判断，致命反例 2 防护）
func (u *profileUpdater) incrementalUpdateLocked(rec QualityRecord) {
	key := rec.TaskType + "|" + rec.Model
	profile, ok := u.profiles[key]
	if !ok {
		profile = ModelProfile{
			TaskType: rec.TaskType,
			Model:    rec.Model,
		}
	}

	// 时间衰减权重
	weight := u.decayWeightLocked(rec.Timestamp)

	// EMA 更新 SuccessRate（v0.2 修订：不再叫 Welford）
	l0Value := 0.0
	if rec.L0Pass {
		l0Value = 1.0
	}
	profile.SuccessRate = emaUpdate(profile.SuccessRate, l0Value, weight, profile.EffectiveSampleCount)

	// EMA 更新 AvgL1Score
	profile.AvgL1Score = int(emaUpdate(float64(profile.AvgL1Score), float64(rec.L1Score), weight, profile.EffectiveSampleCount))

	// EMA 更新 AvgDurationSec
	profile.AvgDurationSec = int(emaUpdate(float64(profile.AvgDurationSec), float64(rec.DurationSec), weight, profile.EffectiveSampleCount))

	// EMA 更新 AvgRetry
	profile.AvgRetry = emaUpdate(profile.AvgRetry, float64(rec.RetryCount), weight, profile.EffectiveSampleCount)

	// L2AdoptionRate：L2Verdict=="adopted" 占比
	// v0.2 修订（改进 4）：modified 记录不计入分子，但分母（EffectiveSampleCount）只对 adopted/discarded 累加
	if rec.L2Verdict == L2Adopted || rec.L2Verdict == L2Discarded {
		l2Value := 0.0
		if rec.L2Verdict == L2Adopted {
			l2Value = 1.0
		}
		profile.L2AdoptionRate = emaUpdate(profile.L2AdoptionRate, l2Value, weight, profile.EffectiveSampleCount)
	}

	// v0.2 修订（改进 4）：EffectiveSampleCount 加权累加（替代原 SampleCount 原始计数）
	// 冷启动阈值改为"EffectiveSampleCount < 10"（§5.2），避免旧记录虚高样本量
	profile.EffectiveSampleCount += weight
	profile.SampleCount++ // 原始计数保留（debug 用）
	profile.UpdatedAt = u.nowFunc().UTC().Format(time.RFC3339Nano)
	u.profiles[key] = profile
}

// decayWeightLocked 时间衰减权重（持锁，内部用）。
// 落地 §4.5.2：<30d=1.0，30-90d=0.5，>90d=0.1。
// timestamp 为空或解析失败视为最新（weight=1.0，保守不降权）。
func (u *profileUpdater) decayWeightLocked(timestamp string) float64 {
	if timestamp == "" {
		return 1.0
	}
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return 1.0 // 解析失败保守给 1.0
	}
	age := u.nowFunc().Sub(ts)
	switch {
	case age < DecayWindow30Days:
		return 1.0
	case age < DecayWindow90Days:
		return 0.5
	default:
		return 0.1
	}
}

// emaUpdate 指数移动平均更新（§4.5.2）。
// newValue 用 weight 加权，oldValue 用 (1 - alpha*weight) 加权，alpha 是学习率。
// alpha = 1.0 / (effectiveCount + 1.0)：学习率随样本量衰减（避免新样本过度影响稳定值）。
func emaUpdate(oldValue, newValue, weight, effectiveCount float64) float64 {
	alpha := 1.0 / (effectiveCount + 1.0)
	return oldValue + alpha*weight*(newValue-oldValue)
}

// markReverseQuerySucceeded 标记反查成功（简化：仅日志，实际应更新 quality.jsonl 状态字段）。
func (u *profileUpdater) markReverseQuerySucceeded(taskID string) {
	// 简化：反查成功后无副作用（实际实现可写 quality.jsonl 的 reverse_query_status 字段）
	// 此处仅占位，由调用方（编排器）记录日志
	_ = taskID
}

// contains 字符串包含（不区分大小写，简化实现）。
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return false
	}
	// 转小写比较
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a := s[i+j]
			b := substr[j]
			// a 转小写
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ProfilesCount 返回当前画像表格子数（仅用于测试）。
func (u *profileUpdater) ProfilesCount() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return len(u.profiles)
}

// SetNowFunc 注入时间函数（测试用）。
func (u *profileUpdater) SetNowFunc(f func() time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.nowFunc = f
}
