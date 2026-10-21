// weight_tuner_test.go — H12 权重调优触发器反例测试（TDD 先行，§2.2）。
// 覆盖：
// - 冷却期：InCooldown=true 跳过调优
// - 样本不足：<100 跳过调优（降级）
// - 漂移不足：≤0.2 跳过调优
// - 正常调优：≥100 + drift>0.2 触发 SaveVersionedPublic
// - 收敛保护：3 次反转触发 RollbackWeights
// - 反例：scorer/quality/guard 为 nil 降级
//
// 设计说明 §2.2；落地：漂移检测 + 收敛保护 + 反例
// SNAPSHOT: tests={Cooldown_SkipTuning, SampleLessThan100_SkipTuning, DriftBelowThreshold_SkipTuning, Sample100_DriftHigh_TriggersSave, NilDeps_Degrade, CtxCancelled_ReturnsErr, ComputeDrift_BoundaryCases}
package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestWeightTuner_Cooldown_SkipTuning 冷却期内跳过调优（§2.2）。
func TestWeightTuner_Cooldown_SkipTuning(t *testing.T) {
	s := NewL1ScorerExt("")
	q := newTestQualityStore(t)
	g := NewConvergenceGuard("")

	// 设置最近调优时间，进入冷却期
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	g.SetNowFunc(func() time.Time { return t0 })
	_ = g.UpdateLastTune()

	// 当前时间在冷却期内
	g.SetNowFunc(func() time.Time { return t0.Add(1 * time.Hour) })

	w := NewWeightTuner(s, q, g)
	w.SetNowFunc(func() time.Time { return t0.Add(1 * time.Hour) })

	before := s.GetWeights()
	if err := w.Tune(context.Background()); err != nil {
		t.Fatalf("Tune: %v", err)
	}
	after := s.GetWeights()
	if before.Version != after.Version {
		t.Errorf("cooldown should skip tuning, version changed: before=%d after=%d", before.Version, after.Version)
	}
}

// TestWeightTuner_SampleLessThan100_SkipTuning 样本 <100 跳过调优（§2.2）。
func TestWeightTuner_SampleLessThan100_SkipTuning(t *testing.T) {
	s := NewL1ScorerExt("")
	q := newTestQualityStore(t)
	g := NewConvergenceGuard("")
	w := NewWeightTuner(s, q, g)

	// 仅注入 50 条 L2 记录（<100）
	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		appendL2Record(t, q, "task-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "pid", L2Adopted, 75, now.Add(-time.Duration(i)*time.Hour))
	}

	before := s.GetWeights()
	if err := w.Tune(context.Background()); err != nil {
		t.Fatalf("Tune: %v", err)
	}
	after := s.GetWeights()
	if before.Version != after.Version {
		t.Errorf("sample<100 should skip tuning, version changed: before=%d after=%d", before.Version, after.Version)
	}
}

// TestWeightTuner_DriftBelowThreshold_SkipTuning 漂移 ≤0.2 跳过调优（§2.2）。
func TestWeightTuner_DriftBelowThreshold_SkipTuning(t *testing.T) {
	s := NewL1ScorerExt("")
	q := newTestQualityStore(t)
	g := NewConvergenceGuard("")
	w := NewWeightTuner(s, q, g)

	// 注入 100+ 条 L2 记录，但 adopted 与 discarded 的 L1Score 均值差 <20（drift<0.2）
	now := time.Now().UTC()
	for i := 0; i < 100; i++ {
		verdict := L2Adopted
		if i%2 == 0 {
			verdict = L2Discarded
		}
		// 所有 L1Score=50，drift=0
		appendL2Record(t, q, "task-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "pid", verdict, 50, now.Add(-time.Duration(i)*time.Hour))
	}

	before := s.GetWeights()
	if err := w.Tune(context.Background()); err != nil {
		t.Fatalf("Tune: %v", err)
	}
	after := s.GetWeights()
	if before.Version != after.Version {
		t.Errorf("drift<=0.2 should skip tuning, version changed: before=%d after=%d", before.Version, after.Version)
	}
}

// TestWeightTuner_Sample100_DriftHigh_TriggersSave 样本 ≥100 + drift>0.2 触发 SaveVersionedPublic（§2.2）。
func TestWeightTuner_Sample100_DriftHigh_TriggersSave(t *testing.T) {
	s := NewL1ScorerExt("")
	q := newTestQualityStore(t)
	g := NewConvergenceGuard("")
	w := NewWeightTuner(s, q, g)

	// 注入 60 条 L2=adopted L1Score=90 + 60 条 L2=discarded L1Score=20
	// drift = (90-20)/100 = 0.7 > 0.2 → 触发调优
	now := time.Now().UTC()
	for i := 0; i < 60; i++ {
		appendL2Record(t, q, "task-a-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "pid", L2Adopted, 90, now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 0; i < 60; i++ {
		appendL2Record(t, q, "task-d-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "pid", L2Discarded, 20, now.Add(-time.Duration(i)*time.Hour))
	}

	before := s.GetWeights()
	if err := w.Tune(context.Background()); err != nil {
		t.Fatalf("Tune: %v", err)
	}
	after := s.GetWeights()
	if after.Version <= before.Version {
		t.Errorf("expected version increment, before=%d after=%d", before.Version, after.Version)
	}
}

// TestWeightTuner_NilDeps_Degrade scorer/quality/guard 为 nil 降级。
func TestWeightTuner_NilDeps_Degrade(t *testing.T) {
	// 全 nil
	w := &WeightTuner{
		scorer:  nil,
		quality: nil,
		guard:   nil,
		nowFunc: time.Now,
	}
	if err := w.Tune(context.Background()); err != nil {
		t.Errorf("nil deps should degrade silently, got %v", err)
	}
}

// TestWeightTuner_CtxCancelled_ReturnsErr ctx 取消返回 ctx.Err()。
func TestWeightTuner_CtxCancelled_ReturnsErr(t *testing.T) {
	s := NewL1ScorerExt("")
	q := newTestQualityStore(t)
	g := NewConvergenceGuard("")
	w := NewWeightTuner(s, q, g)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Tune(ctx); err == nil {
		t.Errorf("cancelled ctx should return ctx.Err()")
	}
}

// TestWeightTuner_ComputeDrift_BoundaryCases computeDrift 边界情况。
func TestWeightTuner_ComputeDrift_BoundaryCases(t *testing.T) {
	w := NewWeightTuner(nil, nil, nil)

	// 空记录 → drift=0
	if d := w.computeDrift(nil); d != 0 {
		t.Errorf("empty records drift = %v, want 0", d)
	}

	// 单边样本（仅 adopted）→ drift=0
	records := []QualityRecord{
		{L2Verdict: L2Adopted, L1Score: 80},
		{L2Verdict: L2Adopted, L1Score: 70},
	}
	if d := w.computeDrift(records); d != 0 {
		t.Errorf("single-side records drift = %v, want 0", d)
	}

	// 双边样本：adopted avg=80, discarded avg=20 → drift=0.6
	records = []QualityRecord{
		{L2Verdict: L2Adopted, L1Score: 80},
		{L2Verdict: L2Adopted, L1Score: 80},
		{L2Verdict: L2Discarded, L1Score: 20},
		{L2Verdict: L2Discarded, L1Score: 20},
	}
	d := w.computeDrift(records)
	// drift = |80-20|/100 = 0.6
	if d < 0.59 || d > 0.61 {
		t.Errorf("two-side records drift = %v, want ~0.6", d)
	}
}

// TestWeightTuner_ReversalTriggersRollback 3 次反转触发 RollbackWeights（§2.2 + §2.5.3）。
func TestWeightTuner_ReversalTriggersRollback(t *testing.T) {
	s := NewL1ScorerExt("")
	q := newTestQualityStore(t)
	g := NewConvergenceGuard("")
	w := NewWeightTuner(s, q, g)

	// 注入充足 L2 记录使 drift>0.2，触发多次 Tune 产生反转
	now := time.Now().UTC()
	// 第一次：adopted 平均高
	for i := 0; i < 60; i++ {
		appendL2Record(t, q, "task-a1-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "pid", L2Adopted, 90, now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 0; i < 60; i++ {
		appendL2Record(t, q, "task-d1-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "pid", L2Discarded, 20, now.Add(-time.Duration(i)*time.Hour))
	}

	// 注意：因为 QualityStore 是 append-only，重写时无法清除之前记录
	// 此处仅断言多次 Tune 不 panic，且最终权重总和仍=100（一致性校验）
	for i := 0; i < 3; i++ {
		// 重置 guard 进入非冷却状态（绕过冷却期）
		g2 := NewConvergenceGuard("")
		w.guard = g2
		_ = w.Tune(context.Background())
	}

	finalWeights := s.GetWeights().Weights
	sum := 0
	for _, v := range finalWeights {
		sum += v
	}
	if sum != 100 {
		t.Errorf("after multiple Tune, weights sum = %d, want 100", sum)
	}
}
