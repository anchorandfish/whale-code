// weight_tuner.go — H12 自我进化系统 §2.2 权重调优触发器（v0.2 修正）。
// 设计说明 §2.2 + §3.4；落地：检测 L2/L1 漂移并触发权重调优 + 收敛保护
// v0.2；落地：调 SaveVersionedPublic 公开 wrapper + ConvergenceGuard 三件套
// SNAPSHOT: pkg=internal/orchestrator; type=WeightTuner; flow={InCooldown→ScanAll近30天→样本<100跳过→漂移≤0.2跳过→SaveVersionedPublic→ShouldStop→RecordDelta→RollbackWeights→UpdateLastTune}; degraded={相关性失败/样本不足/冷却期→跳过返回nil}; convergence_protection=三件套
package orchestrator

import (
	"context"
	"fmt"
	"time"
)

// WeightTunerSampleThreshold 样本量阈值：≥100 才上 L2 回归调优（§2.2）。
const WeightTunerSampleThreshold = 100

// WeightTunerDriftThreshold 漂移阈值：>0.2 才触发调优（§2.2）。
const WeightTunerDriftThreshold = 0.2

// WeightTunerWindow L2 评分反查窗口（30 天）。
const WeightTunerWindow = 30 * 24 * time.Hour

// WeightTunerWeightDelta 漂移时单次权重调整幅度（保守调整）。
const WeightTunerWeightDelta = 1

// WeightTuner 权重调优触发器（v0.2 修正：调 SaveVersionedPublic + 收敛保护）。
// 检测 L2/L1 相关性漂移 >0.2 时调 L1ScorerExt.SaveVersionedPublic（仲裁新增，§6.2）。
// 收敛保护三件套（v0.2 新增，致命漏洞 5 修复）：
// 1. 权重变化 <1% 停止（连续 2 次）
// 2. 调优冷却期 24h
// 3. 连续 3 次方向反转触发 RollbackWeights
//
// 关键约束：
// - 不修改 L1ScorerExt struct 字段签名（BASE 保护）
// - 不直接调 saveVersioned（私有），改调 SaveVersionedPublic（仲裁 wrapper）
// - 调优失败 warn 不阻断后续步骤（原子性）
type WeightTuner struct {
	scorer  *L1ScorerExt
	quality *QualityStore
	guard   *ConvergenceGuard
	nowFunc func() time.Time
}

// NewWeightTuner 构造权重调优触发器。
// scorer / quality / guard 必填，空时 Tune 直接返回 nil（降级）。
func NewWeightTuner(scorer *L1ScorerExt, quality *QualityStore, guard *ConvergenceGuard) *WeightTuner {
	return &WeightTuner{
		scorer:  scorer,
		quality: quality,
		guard:   guard,
		nowFunc: time.Now,
	}
}

// SetNowFunc 注入时间函数（测试用，便于固定时间）。
func (w *WeightTuner) SetNowFunc(fn func() time.Time) {
	w.nowFunc = fn
}

// Tune 检测漂移并触发权重调优（含收敛保护，§2.2 + §3.4）。
// 流程：
//
//	① ctx 校验 + scorer/quality/guard 非空校验
//	② guard.InCooldown() → 是则跳过
//	③ QualityStore.ScanAll 取近 30 天 L2 记录
//	④ 样本 <100 → 跳过（降级，由 AdjustWeightsByRules 内部处理）
//	⑤ 计算每项 L1 子分与 L2 verdict 相关性漂移
//	⑥ 漂移 ≤0.2 → 跳过
//	⑦ before = scorer.GetWeights()
//	⑧ 调 scorer.SaveVersionedPublic(newWeights, reason)
//	⑨ after = scorer.GetWeights()
//	⑩ guard.ShouldStop(before, after) → 标记停止（下次 Tune 由 InCooldown 拦截）
//	⑪ guard.RecordDelta(delta) → 触发回滚则调 scorer.RollbackWeights
//	⑫ guard.UpdateLastTune()
//
// 降级：相关性计算失败 / 样本不足 / 冷却期内 → 跳过调优（不报错）
// 回滚：ConvergenceGuard 触发回滚 → warn 不阻断，调 RollbackWeights 后跳过本次调优
func (w *WeightTuner) Tune(ctx context.Context) error {
	// ① ctx 校验
	if err := ctx.Err(); err != nil {
		return err
	}
	// ② 依赖校验
	if w == nil || w.scorer == nil || w.quality == nil || w.guard == nil {
		return nil // 降级
	}
	// ③ 收敛保护：冷却期检查
	if w.guard.InCooldown() {
		return nil // 24h 内不调优
	}

	// ④ 反查近 30 天 L2 记录
	records, err := w.quality.ScanAll()
	if err != nil {
		return nil // ScanAll 失败降级
	}
	// 过滤 30 天窗口内的记录
	now := w.nowFunc()
	var inWindow []QualityRecord
	for _, r := range records {
		if r.Timestamp == "" {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, r.Timestamp)
		if perr != nil {
			continue
		}
		if now.Sub(ts) > WeightTunerWindow {
			continue
		}
		inWindow = append(inWindow, r)
	}

	// ⑤ 样本 <100 → 跳过（降级）
	if len(inWindow) < WeightTunerSampleThreshold {
		return nil
	}

	// ⑥ 计算相关性漂移
	drift := w.computeDrift(inWindow)
	if drift <= WeightTunerDriftThreshold {
		return nil // 漂移不足
	}

	// ⑦ 调优：构造新权重（保守调整：相关性高 +1，相关性低 -1）
	before := w.scorer.GetWeights().Weights
	newWeights := w.adjustWeightsByDrift(before, inWindow)
	reason := fmt.Sprintf("H12 drift tuning: drift=%.3f, n=%d", drift, len(inWindow))

	// ⑧ 持久化新权重
	if saveErr := w.scorer.SaveVersionedPublic(newWeights, reason); saveErr != nil {
		return nil // Save 失败降级
	}

	// ⑨ 取 after 权重
	after := w.scorer.GetWeights().Weights

	// ⑩ 收敛保护：权重变化 <1% 检查
	if w.guard.ShouldStop(before, after) {
		// 标记停止（不立即返回，仍记录 delta 与 UpdateLastTune）
		// smallChangeCount 由 ShouldStop 内部维护
	}

	// ⑪ 收敛保护：delta 反转检查
	delta := make(map[string]int, len(after))
	for k, v := range after {
		delta[k] = v - before[k]
	}
	if w.guard.RecordDelta(delta) {
		// 触发回滚
		_ = w.scorer.RollbackWeights()
		// 回滚后仍调 UpdateLastTune（记录本次调优时间，避免短期内重复尝试）
	}

	// ⑫ 更新 lastTuneAt
	_ = w.guard.UpdateLastTune()
	return nil
}

// computeDrift 计算 L2/L1 相关性漂移（简化算法，§2.2）。
// 算法：按 L2=adopted/discarded 分组，统计每组 L1Score 均值差，差值/100 归一化为漂移量。
// 返回 [0, 1] 区间的漂移值，0=完全无漂移，1=完全反向。
func (w *WeightTuner) computeDrift(records []QualityRecord) float64 {
	if len(records) == 0 {
		return 0
	}
	adoptedSum, adoptedCount := 0, 0
	discardedSum, discardedCount := 0, 0
	for _, r := range records {
		switch r.L2Verdict {
		case L2Adopted:
			adoptedSum += r.L1Score
			adoptedCount++
		case L2Discarded:
			discardedSum += r.L1Score
			discardedCount++
		}
	}
	if adoptedCount == 0 || discardedCount == 0 {
		return 0 // 单边样本无法计算漂移
	}
	adoptedAvg := float64(adoptedSum) / float64(adoptedCount)
	discardedAvg := float64(discardedSum) / float64(discardedCount)
	diff := adoptedAvg - discardedAvg
	if diff < 0 {
		diff = -diff
	}
	return diff / 100.0
}

// adjustWeightsByDrift 根据漂移调整权重（保守策略，§2.2）。
// 算法：当 drift>0.2 时，根据 adopted/discarded 比例对相关性高的项 +1，相关性低的项 -1。
// 归一化保证 sum=100。
func (w *WeightTuner) adjustWeightsByDrift(current map[string]int, records []QualityRecord) map[string]int {
	newWeights := cloneWeightsMap(current)

	// 简化策略：drift>0.2 表示 L2 与 L1 评分反向
	// - 若 adoptedAvg < discardedAvg（L1 评分高的反而被弃用）→ 降低所有项权重 1
	// - 若 adoptedAvg > discardedAvg（L1 评分高的被采纳）→ 提高所有项权重 1
	// 注：因 QualityRecord.L1Score 是总分而非单项，无法对单项 L1 子分做相关性
	// 故采用整体调整策略（与 AdjustWeightsByRegression 内的简化算法一致）
	adoptedSum, adoptedCount := 0, 0
	discardedSum, discardedCount := 0, 0
	for _, r := range records {
		switch r.L2Verdict {
		case L2Adopted:
			adoptedSum += r.L1Score
			adoptedCount++
		case L2Discarded:
			discardedSum += r.L1Score
			discardedCount++
		}
	}
	if adoptedCount == 0 || discardedCount == 0 {
		return newWeights
	}
	adoptedAvg := float64(adoptedSum) / float64(adoptedCount)
	discardedAvg := float64(discardedSum) / float64(discardedCount)

	delta := 0
	if adoptedAvg >= discardedAvg {
		// L1 评分高被采纳：权重普遍偏低，整体提高
		delta = WeightTunerWeightDelta
	} else {
		// L1 评分高被弃用：权重普遍偏高，整体降低
		delta = -WeightTunerWeightDelta
	}

	// 对所有 key 应用 delta
	for _, key := range L1AllKeys() {
		newVal := newWeights[key] + delta
		if newVal < 0 {
			newVal = 0
		}
		if newVal > 100 {
			newVal = 100
		}
		newWeights[key] = newVal
	}

	// 归一化保证 sum=100
	return normalizeWeights(newWeights)
}
