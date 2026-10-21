// profile_updater_test.go — H10 Phase 5 桶 2 模型画像表更新器反例测试（TDD 先行）。
// 覆盖致命反例：
// - 致命反例 2：EffectiveSampleCount<10 降级（不触发画像表查询）
// - 致命反例 7：quality.jsonl 与 UsageRecord 反查未命中（降级到画像表估算 + 重试）
// - 致命反例 10：cache_hit_rate<50% 告警去重（24h 窗口）
// + 时间衰减；落地：incrementalUpdateLocked + decayWeight
// <50% 告警；落地：CheckCacheHitRate
// SNAPSHOT: tests={EffectiveSampleCountLessThan10_Degrade(致命2), EMA_UpdateCorrect, DecayWeight_30_90_Over90, RebuildAtStartup_FullScan, Reconcile_MismatchDetected, RetryReverseQuery_1hCycle, CheckCacheHitRate_LessThan50_Alert(致命10), CheckCacheHitRate_Dedup24h(致命10), ConcurrentUpdate_NoRace, ReverseQueryFail_NoUsage(致命7)}; (2026-10-09): 断言口径 = token

package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockMetricsClient mock MetricsClient 实现（测试用）。
type mockMetricsClient struct {
	inputTokens   int64
	outputTokens  int64
	cacheCreation int64
	cacheRead     int64
	err           error
	called        int
}

func (m *mockMetricsClient) ReverseQueryUsage(agentID, timestamp string) (int64, int64, int64, int64, error) {
	m.called++
	return m.inputTokens, m.outputTokens, m.cacheCreation, m.cacheRead, m.err
}

// mockNotifier mock Notifier 实现（测试用）。
type mockNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (n *mockNotifier) NotifyAsync(message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, message)
}

func (n *mockNotifier) Count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.messages)
}

// TestProfileUpdater_EffectiveSampleCountLessThan10_Degrade 致命反例 2：
// 9 条旧记录（权重 0.1）+ 1 条新 → EffectiveSampleCount≈1.9 <10 → 降级到静态矩阵。
// 零容忍：EffectiveSampleCount<10 必须降级，不能查询画像表（§5.2）。
func TestProfileUpdater_EffectiveSampleCountLessThan10_Degrade(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	// 固定时间便于测试
	now := time.Now()
	u.SetNowFunc(func() time.Time { return now })

	// 9 条 100 天前的记录（age > 90d → weight=0.1）
	for i := 0; i < 9; i++ {
		rec := QualityRecord{
			TaskID:      fmt.Sprintf("old-%d", i),
			TaskType:    TaskTypeImplement,
			Model:       "claude-sonnet",
			L0Pass:      true,
			L1Score:     80,
			L2Verdict:   L2Adopted,
			DurationSec: 100,
			Timestamp:   now.Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339),
		}
		if err := u.UpdateOnTaskComplete(context.Background(), rec); err != nil {
			t.Fatalf("UpdateOnTaskComplete %d: %v", i, err)
		}
	}

	// 1 条新记录（weight=1.0）
	rec := QualityRecord{
		TaskID:      "new-1",
		TaskType:    TaskTypeImplement,
		Model:       "claude-sonnet",
		L0Pass:      true,
		L1Score:     80,
		L2Verdict:   L2Adopted,
		DurationSec: 100,
		Timestamp:   now.UTC().Format(time.RFC3339),
	}
	if err := u.UpdateOnTaskComplete(context.Background(), rec); err != nil {
		t.Fatalf("UpdateOnTaskComplete new: %v", err)
	}

	// 验证 EffectiveSampleCount ≈ 9*0.1 + 1.0 = 1.9 < 10（致命反例 2）
	profile, err := u.GetProfile(TaskTypeImplement, "claude-sonnet")
	if err != ErrColdStart {
		t.Errorf("致命反例 2：EffectiveSampleCount≈1.9 应返回 ErrColdStart，got err=%v profile=%+v", err, profile)
	}
	if profile.EffectiveSampleCount >= EffectiveSampleCountThreshold {
		t.Errorf("EffectiveSampleCount 应 < %g，got=%g", EffectiveSampleCountThreshold, profile.EffectiveSampleCount)
	}

	// 验证 QueryAvgTokens 冷启动降级
	tokens, qerr := u.QueryAvgTokens(TaskTypeImplement, "claude-sonnet")
	if qerr != ErrColdStart {
		t.Errorf("致命反例 2：QueryAvgTokens 应返回 ErrColdStart，got err=%v tokens=%d", qerr, tokens)
	}
}

// TestProfileUpdater_EffectiveSampleCount10_NoDegrade 10 条新记录 → EffectiveSampleCount=10 → 不降级。
func TestProfileUpdater_EffectiveSampleCount10_NoDegrade(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	u.SetNowFunc(func() time.Time { return now })

	// 10 条新记录（weight=1.0）→ EffectiveSampleCount=10
	for i := 0; i < 10; i++ {
		rec := QualityRecord{
			TaskID:      fmt.Sprintf("new-%d", i),
			TaskType:    TaskTypeImplement,
			Model:       "claude-sonnet",
			L0Pass:      true,
			L1Score:     80,
			L2Verdict:   L2Adopted,
			DurationSec: 100,
			Timestamp:   now.UTC().Format(time.RFC3339),
		}
		if err := u.UpdateOnTaskComplete(context.Background(), rec); err != nil {
			t.Fatalf("UpdateOnTaskComplete %d: %v", i, err)
		}
	}

	// EffectiveSampleCount=10 → 不降级
	profile, err := u.GetProfile(TaskTypeImplement, "claude-sonnet")
	if err != nil {
		t.Errorf("EffectiveSampleCount=10 不应降级，got err=%v", err)
	}
	if profile.EffectiveSampleCount < EffectiveSampleCountThreshold {
		t.Errorf("EffectiveSampleCount 应 ≥ %g，got=%g", EffectiveSampleCountThreshold, profile.EffectiveSampleCount)
	}
}

// TestProfileUpdater_EMA_UpdateCorrect EMA 更新后 SuccessRate 正确。
func TestProfileUpdater_EMA_UpdateCorrect(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	u.SetNowFunc(func() time.Time { return now })

	// 第 1 条 L0=pass → SuccessRate=1.0
	rec1 := QualityRecord{
		TaskID:    "t1",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		L0Pass:    true,
		L1Score:   80,
		L2Verdict: L2Adopted,
		Timestamp: now.UTC().Format(time.RFC3339),
	}
	_ = u.UpdateOnTaskComplete(context.Background(), rec1)

	profile, _ := u.GetProfile(TaskTypeImplement, "claude-sonnet")
	// 冷启动（EffectiveSampleCount=1<10）应返回 ErrColdStart，但 profile 仍有值
	if profile.SuccessRate != 1.0 {
		t.Errorf("第 1 条 L0=pass 后 SuccessRate 应=1.0，got=%f", profile.SuccessRate)
	}

	// 第 2 条 L0=fail → EMA 更新（alpha=1/2, weight=1.0）
	// new = 1.0 + (1/2)*1.0*(0.0-1.0) = 1.0 - 0.5 = 0.5
	rec2 := QualityRecord{
		TaskID:    "t2",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		L0Pass:    false,
		L1Score:   40,
		L2Verdict: L2Discarded,
		Timestamp: now.UTC().Format(time.RFC3339),
	}
	_ = u.UpdateOnTaskComplete(context.Background(), rec2)

	profile2, _ := u.GetProfile(TaskTypeImplement, "claude-sonnet")
	if profile2.SuccessRate != 0.5 {
		t.Errorf("第 2 条 L0=fail 后 EMA SuccessRate 应=0.5，got=%f", profile2.SuccessRate)
	}
}

// TestProfileUpdater_DecayWeight_30_90_Over90 3 档权重 1.0/0.5/0.1。
func TestProfileUpdater_DecayWeight_30_90_Over90(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	u.SetNowFunc(func() time.Time { return now })

	// <30d → 1.0
	ts1 := now.Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if w := u.decayWeightLocked(ts1); w != 1.0 {
		t.Errorf("<30d 应=1.0，got=%f", w)
	}

	// 30-90d → 0.5
	ts2 := now.Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if w := u.decayWeightLocked(ts2); w != 0.5 {
		t.Errorf("30-90d 应=0.5，got=%f", w)
	}

	// >90d → 0.1
	ts3 := now.Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if w := u.decayWeightLocked(ts3); w != 0.1 {
		t.Errorf(">90d 应=0.1，got=%f", w)
	}

	// 空时间戳 → 1.0（保守）
	if w := u.decayWeightLocked(""); w != 1.0 {
		t.Errorf("空时间戳应=1.0，got=%f", w)
	}
}

// TestProfileUpdater_RebuildAtStartup_FullScan 全量扫描后 profiles 正确。
func TestProfileUpdater_RebuildAtStartup_FullScan(t *testing.T) {
	// 用临时 QualityStore
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	// 写入 3 条记录
	records := []QualityRecord{
		{
			TaskID:    "t1",
			TaskType:  TaskTypeImplement,
			Model:     "claude-sonnet",
			L0Pass:    true,
			L1Score:   80,
			L2Verdict: L2Adopted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			MetricsCorrelation: MetricsCorrelation{
				AgentID:   "agent-1",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
		},
		{
			TaskID:    "t2",
			TaskType:  TaskTypeImplement,
			Model:     "claude-sonnet",
			L0Pass:    false,
			L1Score:   40,
			L2Verdict: L2Discarded,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			MetricsCorrelation: MetricsCorrelation{
				AgentID:   "agent-2",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
		},
		{
			TaskID:    "t3",
			TaskType:  TaskTypeBugFix,
			Model:     "claude-haiku",
			L0Pass:    true,
			L1Score:   90,
			L2Verdict: L2Adopted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			MetricsCorrelation: MetricsCorrelation{
				AgentID:   "agent-3",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	for _, rec := range records {
		if err := qs.Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Rebuild
	u := NewProfileUpdater(qs, nil, nil).(*profileUpdater)
	if err := u.RebuildAtStartup(); err != nil {
		t.Fatalf("RebuildAtStartup: %v", err)
	}

	// 验证 profiles 数量 = 2（implement+sonnet, bug-fix+haiku）
	if u.ProfilesCount() != 2 {
		t.Errorf("ProfilesCount 应=2，got=%d", u.ProfilesCount())
	}
}

// TestProfileUpdater_Reconcile_MismatchDetected model 不一致 → 返回 MismatchRecord。
func TestProfileUpdater_Reconcile_MismatchDetected(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	// 写入 1 条 metrics_correlation 为空的记录（反查失败模拟）
	rec := QualityRecord{
		TaskID:    "t1",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		L0Pass:    true,
		L1Score:   80,
		L2Verdict: L2Adopted,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		// MetricsCorrelation 留空 → Reconcile 应识别为 mismatch
	}
	if err := qs.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	u := NewProfileUpdater(qs, nil, nil).(*profileUpdater)
	mismatches, err := u.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(mismatches) == 0 {
		t.Error("应有 mismatch（metrics_correlation 为空）")
	}
	if mismatches[0].TaskID != "t1" {
		t.Errorf("mismatch TaskID 应=t1，got=%s", mismatches[0].TaskID)
	}
}

// TestProfileUpdater_RetryReverseQuery_1hCycle 反查失败记录重试。
func TestProfileUpdater_RetryReverseQuery_1hCycle(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	// 写入 2 条记录：1 条 2 天前（age>1d，触发重试），1 条 1 小时前（age<1d，不触发）
	oldRec := QualityRecord{
		TaskID:    "old-t1",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		Timestamp: time.Now().Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339),
		MetricsCorrelation: MetricsCorrelation{
			AgentID:   "agent-old",
			Timestamp: time.Now().Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	newRec := QualityRecord{
		TaskID:    "new-t1",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		MetricsCorrelation: MetricsCorrelation{
			AgentID:   "agent-new",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := qs.Append(oldRec); err != nil {
		t.Fatalf("Append old: %v", err)
	}
	if err := qs.Append(newRec); err != nil {
		t.Fatalf("Append new: %v", err)
	}

	// mock metrics 反查成功
	mockMetrics := &mockMetricsClient{}
	u := NewProfileUpdater(qs, mockMetrics, nil).(*profileUpdater)

	// 执行 RetryReverseQuery（threshold=1 天，找出 age>1 天的记录重试）
	if err := u.RetryReverseQuery(); err != nil {
		t.Fatalf("RetryReverseQuery: %v", err)
	}

	// 验证 mock 被调用（至少 1 次，针对 old-t1）
	if mockMetrics.called < 1 {
		t.Errorf("RetryReverseQuery 应至少调用 1 次 metrics.ReverseQueryUsage，got=%d", mockMetrics.called)
	}
}

// TestProfileUpdater_CheckCacheHitRate_LessThan50_Alert 致命反例 10：
// hitRate=0.3 < 50% → 异步通知。
// 零容忍：cache_hit_rate<50% 必须告警。
func TestProfileUpdater_CheckCacheHitRate_LessThan50_Alert(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	// 写入记录（cache_read=30, cache_creation=70 → hitRate=0.3）
	rec := QualityRecord{
		TaskID:    "t1",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		MetricsCorrelation: MetricsCorrelation{
			AgentID:   "agent-1",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := qs.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// mock metrics 返回 cache_read=30, cache_creation=70 → hitRate=0.3 < 0.5
	mockMetrics := &mockMetricsClient{
		cacheCreation: 70,
		cacheRead:     30,
	}
	mockNotif := &mockNotifier{}
	u := NewProfileUpdater(qs, mockMetrics, mockNotif).(*profileUpdater)

	if err := u.CheckCacheHitRate("t1"); err != nil {
		t.Fatalf("CheckCacheHitRate: %v", err)
	}

	if mockNotif.Count() == 0 {
		t.Error("致命反例 10：hitRate=0.3 < 0.5 应触发告警通知")
	}
}

// TestProfileUpdater_CheckCacheHitRate_Dedup24h 致命反例 10 关联：
// 同 task_type 24h 内只告警 1 次（防连续告警打扰用户）。
func TestProfileUpdater_CheckCacheHitRate_Dedup24h(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	// 写入 2 条同 task_type 的记录
	now := time.Now()
	for i := 0; i < 2; i++ {
		rec := QualityRecord{
			TaskID:    fmt.Sprintf("t%d", i),
			TaskType:  TaskTypeImplement,
			Model:     "claude-sonnet",
			Timestamp: now.UTC().Format(time.RFC3339),
			MetricsCorrelation: MetricsCorrelation{
				AgentID:   fmt.Sprintf("agent-%d", i),
				Timestamp: now.UTC().Format(time.RFC3339),
			},
		}
		if err := qs.Append(rec); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	mockMetrics := &mockMetricsClient{
		cacheCreation: 70,
		cacheRead:     30, // hitRate=0.3 < 0.5
	}
	mockNotif := &mockNotifier{}
	u := NewProfileUpdater(qs, mockMetrics, mockNotif).(*profileUpdater)

	// 第 1 次 CheckCacheHitRate：应告警
	if err := u.CheckCacheHitRate("t0"); err != nil {
		t.Fatalf("CheckCacheHitRate t0: %v", err)
	}
	if mockNotif.Count() != 1 {
		t.Errorf("第 1 次应告警 1 次，got=%d", mockNotif.Count())
	}

	// 第 2 次 CheckCacheHitRate（同 task_type，24h 内）：应去重，不告警
	if err := u.CheckCacheHitRate("t1"); err != nil {
		t.Fatalf("CheckCacheHitRate t1: %v", err)
	}
	if mockNotif.Count() != 1 {
		t.Errorf("致命反例 10：24h 内同 task_type 应只告警 1 次，got=%d", mockNotif.Count())
	}
}

// TestProfileUpdater_CheckCacheHitRate_HighHitRate_NoAlert hitRate=0.8 > 50% → 不告警。
func TestProfileUpdater_CheckCacheHitRate_HighHitRate_NoAlert(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	rec := QualityRecord{
		TaskID:    "t1",
		TaskType:  TaskTypeImplement,
		Model:     "claude-sonnet",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		MetricsCorrelation: MetricsCorrelation{
			AgentID:   "agent-1",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := qs.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// hitRate=0.8 > 0.5
	mockMetrics := &mockMetricsClient{
		cacheCreation: 20,
		cacheRead:     80,
	}
	mockNotif := &mockNotifier{}
	u := NewProfileUpdater(qs, mockMetrics, mockNotif).(*profileUpdater)

	if err := u.CheckCacheHitRate("t1"); err != nil {
		t.Fatalf("CheckCacheHitRate: %v", err)
	}
	if mockNotif.Count() != 0 {
		t.Errorf("hitRate=0.8 > 0.5 不应告警，got=%d", mockNotif.Count())
	}
}

// TestProfileUpdater_ConcurrentUpdate_NoRace 多 goroutine 同时 UpdateOnTaskComplete，-race 无竞态。
func TestProfileUpdater_ConcurrentUpdate_NoRace(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	u.SetNowFunc(func() time.Time { return now })

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			rec := QualityRecord{
				TaskID:    fmt.Sprintf("t%d", idx),
				TaskType:  TaskTypeImplement,
				Model:     "claude-sonnet",
				L0Pass:    true,
				L1Score:   80,
				L2Verdict: L2Adopted,
				Timestamp: now.UTC().Format(time.RFC3339),
			}
			_ = u.UpdateOnTaskComplete(context.Background(), rec)
		}(i)
	}
	wg.Wait()

	// 验证最总记录数正确
	profile, _ := u.GetProfile(TaskTypeImplement, "claude-sonnet")
	if profile.SampleCount != N {
		t.Errorf("SampleCount 应=%d，got=%d", N, profile.SampleCount)
	}
}

// TestProfileUpdater_ReverseQueryFail_NoUsage 致命反例 7：
// quality.jsonl 与 UsageRecord 反查未命中 → 降级到画像表估算 + 重试。
// 零容忍：反查失败必须降级，不能阻塞编排。
// ★ ：断言口径 = token（画像表估算 profileAvgTokens = AvgDurationSec × 100）。
func TestProfileUpdater_ReverseQueryFail_NullCost(t *testing.T) {
	// mock metrics 反查失败
	mockMetrics := &mockMetricsClient{err: ErrReverseQueryNotFound}
	u := NewProfileUpdater(nil, mockMetrics, nil).(*profileUpdater)
	now := time.Now()
	u.SetNowFunc(func() time.Time { return now })

	// 先注入 10 条新记录使画像表不冷启动
	for i := 0; i < 10; i++ {
		rec := QualityRecord{
			TaskID:      fmt.Sprintf("t%d", i),
			TaskType:    TaskTypeImplement,
			Model:       "claude-sonnet",
			L0Pass:      true,
			L1Score:     80,
			L2Verdict:   L2Adopted,
			DurationSec: 100,
			Timestamp:   now.UTC().Format(time.RFC3339),
		}
		_ = u.UpdateOnTaskComplete(context.Background(), rec)
	}

	// QueryAvgTokens 应降级到画像表估算（反查失败不阻塞）
	tokens, err := u.QueryAvgTokens(TaskTypeImplement, "claude-sonnet")
	if err != nil {
		t.Errorf("致命反例 7：反查失败应降级到画像表估算，不应返回 err，got=%v", err)
	}
	// 应返回画像表估算 profileAvgTokens = AvgDurationSec(100) × 100 = 10000
	if tokens != 10000 {
		t.Errorf("致命反例 7：反查失败应返回画像表估算 10000 token，got=%d", tokens)
	}
}

// TestProfileUpdater_QueryAvgTokens_ColdStart 冷启动 QueryAvgTokens 返回 ErrColdStart。
func TestProfileUpdater_QueryAvgTokens_ColdStart(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)

	// 无任何记录 → 冷启动
	tokens, err := u.QueryAvgTokens(TaskTypeImplement, "claude-sonnet")
	if err != ErrColdStart {
		t.Errorf("冷启动应返回 ErrColdStart，got err=%v tokens=%d", err, tokens)
	}
}

// TestProfileUpdater_ProfileAvgTokens（2026-10-09）：
// 画像表降级估算 = AvgDurationSec × 100（token）。原 staticEstimate（自定价美分）已删。
func TestProfileUpdater_ProfileAvgTokens(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	cases := []struct {
		durationSec int
		expected    int64
	}{
		{0, 0},
		{50, 5000},
		{100, 10000},
	}
	for _, c := range cases {
		got := u.profileAvgTokens(ModelProfile{TaskType: TaskTypeImplement, Model: "claude-sonnet", AvgDurationSec: c.durationSec})
		if got != c.expected {
			t.Errorf("AvgDurationSec=%d 应=%d token，got=%d", c.durationSec, c.expected, got)
		}
	}
}

// TestQualityStore_OnAppend 回调注册后被触发（验证）。
func TestQualityStore_OnAppend(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	var triggered []QualityRecord
	var mu sync.Mutex
	if err := qs.OnAppend(func(rec QualityRecord) {
		mu.Lock()
		defer mu.Unlock()
		triggered = append(triggered, rec)
	}); err != nil {
		t.Fatalf("OnAppend: %v", err)
	}

	// Append 1 条记录
	rec := QualityRecord{
		TaskID:    "t1",
		TaskType:  TaskTypeImplement,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if err := qs.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(triggered) != 1 {
		t.Errorf("OnAppend 应触发 1 次，got=%d", len(triggered))
	}
	if triggered[0].TaskID != "t1" {
		t.Errorf("回调应收到 t1，got=%s", triggered[0].TaskID)
	}
}

// TestQualityStore_OnAppend_NilRejected nil 回调被拒绝。
func TestQualityStore_OnAppend_NilRejected(t *testing.T) {
	qs, err := NewQualityStore("/tmp/test.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	if err := qs.OnAppend(nil); err == nil {
		t.Error("nil 回调应被拒绝")
	}
}

// TestQualityStore_ScanFailedReverseQueries 反查失败扫描（验证）。
func TestQualityStore_ScanFailedReverseQueries(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}

	// 1 条 metrics_correlation 为空的记录（反查失败）
	rec1 := QualityRecord{
		TaskID:    "failed-t1",
		TaskType:  TaskTypeImplement,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		// MetricsCorrelation 留空
	}
	// 1 条正常的记录
	rec2 := QualityRecord{
		TaskID:    "ok-t1",
		TaskType:  TaskTypeImplement,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		MetricsCorrelation: MetricsCorrelation{
			AgentID:   "agent-1",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := qs.Append(rec1); err != nil {
		t.Fatalf("Append rec1: %v", err)
	}
	if err := qs.Append(rec2); err != nil {
		t.Fatalf("Append rec2: %v", err)
	}

	// threshold=0：找出 metrics_correlation 为空的记录
	failed, err := qs.ScanFailedReverseQueries(0)
	if err != nil {
		t.Fatalf("ScanFailedReverseQueries: %v", err)
	}
	if len(failed) != 1 {
		t.Errorf("应返回 1 条反查失败记录，got=%d", len(failed))
	}
	if failed[0].TaskID != "failed-t1" {
		t.Errorf("应返回 failed-t1，got=%s", failed[0].TaskID)
	}

	// threshold=-1：禁用扫描
	failed, err = qs.ScanFailedReverseQueries(-1)
	if err != nil {
		t.Fatalf("ScanFailedReverseQueries -1: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("threshold=-1 应返回空，got=%d", len(failed))
	}
}

// === B-P0-2 反例测试（桶 X2 修复回核）===

// seedProfileForTokens 预置画像表使其不冷启动，并向 quality.jsonl 写入带 MetricsCorrelation 的记录。
// durationSec 控制画像表 AvgDurationSec（影响 profileAvgTokens 估算值）。
// 返回 QualityStore 供调用方注入 metrics 后调 QueryAvgCost。
func seedProfileForTokens(t *testing.T, durationSec int) (*QualityStore, *profileUpdater) {
	t.Helper()
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	now := time.Now().UTC()
	// 写入 10 条带 MetricsCorrelation 的记录到 quality.jsonl（供 aggregateTokensFromMetrics 扫描）
	// 同时调 UpdateOnTaskComplete 让 profiles map 的 EffectiveSampleCount >= 10（不冷启动）
	// metrics/notifier 暂传 nil，由调用方构造完整 profileUpdater 后再调 QueryAvgCost
	u := NewProfileUpdater(qs, nil, nil).(*profileUpdater)
	u.SetNowFunc(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		rec := QualityRecord{
			TaskID:      fmt.Sprintf("bp02-t%d", i),
			TaskType:    TaskTypeImplement,
			Model:       "claude-sonnet",
			L0Pass:      true,
			L1Score:     80,
			L2Verdict:   L2Adopted,
			DurationSec: durationSec,
			Timestamp:   now.UTC().Format(time.RFC3339),
			MetricsCorrelation: MetricsCorrelation{
				AgentID:   fmt.Sprintf("bp02-agent-%d", i),
				Timestamp: now.UTC().Format(time.RFC3339),
			},
		}
		if err := qs.Append(rec); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if err := u.UpdateOnTaskComplete(context.Background(), rec); err != nil {
			t.Fatalf("UpdateOnTaskComplete %d: %v", i, err)
		}
	}
	// 验证不冷启动
	profile, gerr := u.GetProfile(TaskTypeImplement, "claude-sonnet")
	if gerr != nil {
		t.Fatalf("预置失败：画像表应不冷启动，got err=%v", gerr)
	}
	if profile.EffectiveSampleCount < EffectiveSampleCountThreshold {
		t.Fatalf("预置失败：EffectiveSampleCount=%g < %g", profile.EffectiveSampleCount, EffectiveSampleCountThreshold)
	}
	return qs, u
}

// TestProfileUpdater_QueryAvgTokens_WithMetrics_ReturnsDynamic（2026-10-09）：
// metrics 注入时 QueryAvgTokens 返回动态聚合 **token**（mockMetrics 反查值），而非画像表估算。
// 验证目标（B-P0-2 修复）：metrics 非 nil + qualityStore 非 nil → aggregateTokensFromMetrics 反查聚合。
func TestProfileUpdater_QueryAvgTokens_WithMetrics_ReturnsDynamic(t *testing.T) {
	_, u := seedProfileForTokens(t, 100)
	// 注入 mockMetrics 反查 input=1000 + output=500 = 1500 token/条
	mockMetrics := &mockMetricsClient{inputTokens: 1000, outputTokens: 500}
	u.metrics = mockMetrics

	tokens, err := u.QueryAvgTokens(TaskTypeImplement, "claude-sonnet")
	if err != nil {
		t.Fatalf(" 正例：metrics 注入时 QueryAvgTokens 不应返回 error，got=%v", err)
	}
	// 应返回动态聚合值 1500（10 条记录各 1500 的均值），而非画像表估算 10000
	if tokens != 1500 {
		t.Errorf(" 正例：应返回动态聚合 token=1500，got=%d（画像表估算=10000）", tokens)
	}
	// 验证 mock 被调用（说明走了 aggregateTokensFromMetrics 路径）
	if mockMetrics.called == 0 {
		t.Error(" 正例：metrics.ReverseQueryUsage 应被调用（说明走动态聚合路径）")
	}
}

// TestProfileUpdater_QueryAvgTokens_WithoutMetrics_ReturnsProfileEstimate（2026-10-09）：
// metrics 未注入时 QueryAvgTokens 返回画像表估算（profileAvgTokens = AvgDurationSec × 100）。
// 反例核心：metrics=nil 时不应调用 ReverseQueryUsage。
func TestProfileUpdater_QueryAvgTokens_WithoutMetrics_ReturnsProfileEstimate(t *testing.T) {
	_, u := seedProfileForTokens(t, 100)

	// 注入一个 mockMetrics 仅用于检测是否被调用（不应被调用），随后置 nil 模拟未注入
	detectMetrics := &mockMetricsClient{}
	u.metrics = detectMetrics
	u.metrics = nil

	tokens, err := u.QueryAvgTokens(TaskTypeImplement, "claude-sonnet")
	if err != nil {
		t.Fatalf(" 反例：metrics=nil 时 QueryAvgTokens 不应返回 error，got=%v", err)
	}
	// 应返回画像表估算（AvgDurationSec=100 → 100×100 = 10000 token）
	if tokens != 10000 {
		t.Errorf(" 反例：metrics=nil 时应返回画像表估算 10000 token，got=%d", tokens)
	}
	// 验证 mock 未被调用（说明未走动态聚合路径）
	if detectMetrics.called != 0 {
		t.Errorf(" 反例：metrics=nil 时不应调用 ReverseQueryUsage，got called=%d", detectMetrics.called)
	}
}

// TestProfileUpdater_QueryAvgTokens_Aggregate_FiltersByTaskTypeModel（2026-10-09）：
// aggregateTokensFromMetrics 严格按 (taskType, model) 过滤，不混入其他 taskType/model 的记录。
func TestProfileUpdater_QueryAvgTokens_Aggregate_FiltersByTaskTypeModel(t *testing.T) {
	qs, u := seedProfileForTokens(t, 100)
	// 额外写入 5 条 (TaskTypeBugFix, claude-haiku) 的记录（不应被 (implement, sonnet) 查询命中）
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		rec := QualityRecord{
			TaskID:      fmt.Sprintf("bp02-other-%d", i),
			TaskType:    TaskTypeBugFix,
			Model:       "claude-haiku",
			L0Pass:      true,
			L1Score:     70,
			L2Verdict:   L2Adopted,
			DurationSec: 50,
			Timestamp:   now.UTC().Format(time.RFC3339),
			MetricsCorrelation: MetricsCorrelation{
				AgentID:   fmt.Sprintf("bp02-other-agent-%d", i),
				Timestamp: now.UTC().Format(time.RFC3339),
			},
		}
		if err := qs.Append(rec); err != nil {
			t.Fatalf("Append other %d: %v", i, err)
		}
	}
	mockMetrics := &mockMetricsClient{inputTokens: 1000, outputTokens: 500}
	u.metrics = mockMetrics

	tokens, err := u.QueryAvgTokens(TaskTypeImplement, "claude-sonnet")
	if err != nil {
		t.Fatalf("QueryAvgTokens: %v", err)
	}
	// 应返回 1500（仅 (implement, sonnet) 的 10 条记录聚合，不含 (bug-fix, haiku) 的 5 条）
	if tokens != 1500 {
		t.Errorf(" 边界：应仅聚合 (implement, sonnet) 的 10 条记录，tokens=1500，got=%d", tokens)
	}
	// mock 应被调用 10 次（仅 implement+sonnet 的记录），而非 15 次
	if mockMetrics.called != 10 {
		t.Errorf(" 边界：ReverseQueryUsage 应被调用 10 次（仅 implement+sonnet），got=%d", mockMetrics.called)
	}
}
