// model_profile_test.go — 桶 3 ProfileStore + ModelProfile 反例测试（地基层 §3.5）。
// 4 个反例测试覆盖：冷启动返回 nil / 聚合正确性 / 并发更新 / 无 cost 字段（SNAPSHOT 防护）。

package orchestrator

import (
	"reflect"
	"sync"
	"testing"
)

// TestProfileStore_ColdStart_ReturnsNil sample_count < 10 时 Get 返回 nil。
// 验证目标（地基层 §4.6）：冷启动 < 10 样本降级，路由器降级用静态能力矩阵。
func TestProfileStore_ColdStart_ReturnsNil(t *testing.T) {
	p := NewProfileStore()

	// 写入 9 条记录（< 10，冷启动）
	for i := 0; i < 9; i++ {
		p.Update(QualityRecord{
			TaskType: TaskTypeBugFix,
			Model:    "gpt-4o",
			L0Pass:   true,
			L1Score:  80,
		})
	}

	// Get 应返回 nil（冷启动）
	if prof := p.Get(TaskTypeBugFix, "gpt-4o"); prof != nil {
		t.Errorf("cold start (sample_count=9) must return nil, got SampleCount=%d", prof.SampleCount)
	}

	// 写入第 10 条记录（达到阈值）
	p.Update(QualityRecord{
		TaskType: TaskTypeBugFix,
		Model:    "gpt-4o",
		L0Pass:   true,
		L1Score:  90,
	})

	// Get 应返回非 nil（达到 10 样本）
	prof := p.Get(TaskTypeBugFix, "gpt-4o")
	if prof == nil {
		t.Fatal("sample_count=10 must return non-nil profile")
	}
	if prof.SampleCount != 10 {
		t.Errorf("expected SampleCount=10, got %d", prof.SampleCount)
	}
}

// TestProfileStore_Aggregation 写入 3 条同 (task_type, model) 记录，验证 SuccessRate 正确。
// 验证目标（地基层 §3.5）：聚合计算 SuccessRate/AvgL1Score/L2AdoptionRate 正确。
func TestProfileStore_Aggregation(t *testing.T) {
	p := NewProfileStore()

	// 写入 3 条记录：2 条 L0Pass=true + 1 条 L0Pass=false → SuccessRate=2/3
	records := []QualityRecord{
		{TaskType: TaskTypeImplement, Model: "claude-3.5", L0Pass: true, L1Score: 80, L2Verdict: L2Adopted, DurationSec: 30, RetryCount: 0},
		{TaskType: TaskTypeImplement, Model: "claude-3.5", L0Pass: true, L1Score: 90, L2Verdict: L2Adopted, DurationSec: 60, RetryCount: 1},
		{TaskType: TaskTypeImplement, Model: "claude-3.5", L0Pass: false, L1Score: 50, L2Verdict: L2Discarded, DurationSec: 90, RetryCount: 2},
	}

	// 用 ReloadFromRecords 一次性重建（避免冷启动 nil 检查）
	p.ReloadFromRecords(records)

	// 直接读内部状态（绕过 Get 的冷启动检查）
	p.mu.RLock()
	key := profileKey{TaskType: TaskTypeImplement, Model: "claude-3.5"}
	prof, ok := p.profiles[key]
	p.mu.RUnlock()
	if !ok {
		t.Fatal("profile not found after ReloadFromRecords")
	}

	// 验证聚合值
	if prof.SampleCount != 3 {
		t.Errorf("expected SampleCount=3, got %d", prof.SampleCount)
	}
	expectedSuccessRate := 2.0 / 3.0 // 2/3 通过
	if !floatApprox(prof.SuccessRate, expectedSuccessRate, 0.001) {
		t.Errorf("expected SuccessRate=%f, got %f", expectedSuccessRate, prof.SuccessRate)
	}
	expectedAvgL1 := (80 + 90 + 50) / 3 // 73
	if prof.AvgL1Score != expectedAvgL1 {
		t.Errorf("expected AvgL1Score=%d, got %d", expectedAvgL1, prof.AvgL1Score)
	}
	expectedL2Adoption := 2.0 / 3.0 // 2/3 adopted
	if !floatApprox(prof.L2AdoptionRate, expectedL2Adoption, 0.001) {
		t.Errorf("expected L2AdoptionRate=%f, got %f", expectedL2Adoption, prof.L2AdoptionRate)
	}
}

// TestProfileStore_ConcurrentUpdate 两个 goroutine 同时 Update，验证 sync.RWMutex 防竞态。
// 验证目标（地基层 §3.5）：并发安全，最终 SampleCount 等于两次 Update 之和。
func TestProfileStore_ConcurrentUpdate(t *testing.T) {
	p := NewProfileStore()

	const N = 100
	var wg sync.WaitGroup
	wg.Add(2)

	// goroutine 1：写入 N 条 task=bug-fix, model=gpt-4o
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			p.Update(QualityRecord{
				TaskType: TaskTypeBugFix,
				Model:    "gpt-4o",
				L0Pass:   true,
				L1Score:  80,
			})
		}
	}()

	// goroutine 2：写入 N 条 task=bug-fix, model=gpt-4o（同格子，会竞争）
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			p.Update(QualityRecord{
				TaskType: TaskTypeBugFix,
				Model:    "gpt-4o",
				L0Pass:   true,
				L1Score:  90,
			})
		}
	}()

	wg.Wait()

	// 验证：应有 2N 条记录（无丢失）
	p.mu.RLock()
	key := profileKey{TaskType: TaskTypeBugFix, Model: "gpt-4o"}
	prof, ok := p.profiles[key]
	p.mu.RUnlock()
	if !ok {
		t.Fatal("profile not found after concurrent updates")
	}
	if prof.SampleCount != 2*N {
		t.Errorf("expected SampleCount=%d (no lost updates), got %d", 2*N, prof.SampleCount)
	}
}

// TestModelProfile_NoCostFields 致命反例：验证 ModelProfile 不含 AvgCostCents/AvgTokensIn（v0.3 删除，SNAPSHOT 防护）。
// 验证目标（PIT-002 防护 +）：cost 字段归 N2 CostRecord，ModelProfile 不污染。
func TestModelProfile_NoCostFields(t *testing.T) {
	prof := ModelProfile{}
	rt := reflect.TypeOf(prof)

	// v0.3 删除的 cost 字段黑名单（SNAPSHOT 防护）
	forbidden := []string{
		"AvgCostCents", "AvgTokensIn", "AvgCacheHit", // v0.2 字段
		"CostCents", "InputTokens", "OutputTokens", "CacheCreation", "CacheRead",
	}
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		for _, bad := range forbidden {
			if name == bad {
				t.Errorf("PIT-002/SNAPSHOT violation: ModelProfile must not contain cost field %q (v0.3 deleted)", name)
			}
		}
	}
}

// floatApprox 浮点数近似比较（容忍 epsilon）。
func floatApprox(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}
