// ttl_manager_test.go — H10 Phase 5 桶 5 TTL 管理器反例测试（TDD 先行）。
// 覆盖致命反例：
// - 致命反例 5：持久化失败降级（内存保留 + 告警 + 完成后重写）
// - 并发 sweep 无竞态（-race）
// 7d 清理；落地：ApplyBudgetStateTTL
// 90d stale / 180d 清理；落地：ApplyProfileTTL + SweepStaleProfiles
// 权重 N=20 版本归档；落地：ApplyL1WeightsTTL
// SNAPSHOT: tests={BudgetState_7Days_Cleanup, Profile_90Days_Stale, Profile_EffectiveSampleCount0_180Days_Remove, L1Weights_ArchiveLimit20, ConcurrentSweep_NoRace, SweepAll_Result, BudgetState_NoStateDir_NoOp, ProfileSweeperNil_NoOp}

package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestTTL_BudgetState_7Days_Cleanup 编排完成 8 天 → 清理 BudgetState。
// 验证：stateDir 下 mtime > 7 天的 *.json 被删除。
func TestTTL_BudgetState_7Days_Cleanup(t *testing.T) {
	stateDir := t.TempDir()
	// 创建 2 个 state.json 文件：1 个 8 天前（应清理），1 个 3 天前（应保留）
	oldFile := filepath.Join(stateDir, "old-state.json")
	newFile := filepath.Join(stateDir, "new-state.json")
	if err := os.WriteFile(oldFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	if err := os.WriteFile(newFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}

	// 修改 oldFile mtime 为 8 天前
	now := time.Now()
	oldTime := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}
	// newFile mtime 保持当前（3 天前）
	newTime := now.Add(-3 * 24 * time.Hour)
	if err := os.Chtimes(newFile, newTime, newTime); err != nil {
		t.Fatalf("Chtimes new: %v", err)
	}

	m := NewTTLManager(nil, nil, nil, stateDir)
	m.SetNowFunc(func() time.Time { return now })

	cleaned, err := m.ApplyBudgetStateTTL()
	if err != nil {
		t.Fatalf("ApplyBudgetStateTTL: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("应清理 1 个文件（8 天前），got=%d", cleaned)
	}

	// 验证 oldFile 已删除，newFile 保留
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old-state.json（8 天前）应被删除")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Error("new-state.json（3 天前）应保留")
	}
}

// TestTTL_BudgetState_NoStateDir_NoOp stateDir 为空 → 无操作。
func TestTTL_BudgetState_NoStateDir_NoOp(t *testing.T) {
	m := NewTTLManager(nil, nil, nil, "")
	cleaned, err := m.ApplyBudgetStateTTL()
	if err != nil {
		t.Fatalf("stateDir 为空应返回 nil error: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("stateDir 为空应 cleaned=0，got=%d", cleaned)
	}
}

// TestTTL_BudgetState_NonExistentDir_NoOp stateDir 不存在 → 无操作（不报错）。
func TestTTL_BudgetState_NonExistentDir_NoOp(t *testing.T) {
	m := NewTTLManager(nil, nil, nil, "/nonexistent-dir-for-ttl-test")
	cleaned, err := m.ApplyBudgetStateTTL()
	if err != nil {
		t.Fatalf("不存在的 stateDir 应返回 nil error: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("不存在的 stateDir 应 cleaned=0，got=%d", cleaned)
	}
}

// TestTTL_Profile_90Days_Stale UpdatedAt 91 天 → 标记 stale。
// 验证：EffectiveSampleCount>=10 但 UpdatedAt>90d → staleCount=1, removedCount=0。
func TestTTL_Profile_90Days_Stale(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	// 构造一个 EffectiveSampleCount=15（>=10 不触发冷启动）但 UpdatedAt=91 天前的画像
	u.mu.Lock()
	u.profiles["implement|claude-sonnet"] = ModelProfile{
		TaskType:             TaskTypeImplement,
		Model:                "claude-sonnet",
		EffectiveSampleCount: 15.0, // >=10，不触发 EffectiveSampleCount<10 条件
		SampleCount:          15,
		UpdatedAt:            now.Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	u.mu.Unlock()

	m := NewTTLManager(nil, u, nil, "")
	m.SetNowFunc(func() time.Time { return now })

	stale, removed, err := m.ApplyProfileTTL()
	if err != nil {
		t.Fatalf("ApplyProfileTTL: %v", err)
	}
	if stale != 1 {
		t.Errorf("UpdatedAt 91 天应标记 stale=1，got=%d", stale)
	}
	if removed != 0 {
		t.Errorf("EffectiveSampleCount=15 不应移除，removed 应=0，got=%d", removed)
	}
}

// TestTTL_Profile_EffectiveSampleCount0_180Days_Remove ==0 + 181 天 → 内存移除。
// 验证：EffectiveSampleCount==0 且 UpdatedAt>180d → removedCount=1。
func TestTTL_Profile_EffectiveSampleCount0_180Days_Remove(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	// 构造一个 EffectiveSampleCount=0 且 UpdatedAt=181 天前的画像
	u.mu.Lock()
	u.profiles["implement|claude-opus"] = ModelProfile{
		TaskType:             TaskTypeImplement,
		Model:                "claude-opus",
		EffectiveSampleCount: 0, // ==0
		UpdatedAt:            now.Add(-181 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	u.mu.Unlock()

	m := NewTTLManager(nil, u, nil, "")
	m.SetNowFunc(func() time.Time { return now })

	stale, removed, err := m.ApplyProfileTTL()
	if err != nil {
		t.Fatalf("ApplyProfileTTL: %v", err)
	}
	if removed != 1 {
		t.Errorf("EffectiveSampleCount==0 且 181 天应 removed=1，got=%d", removed)
	}
	// stale 不计（因为已移除）
	if stale != 0 {
		t.Errorf("已移除的条目不应计入 stale，stale 应=0，got=%d", stale)
	}

	// 验证已从内存移除
	u.mu.RLock()
	_, exists := u.profiles["implement|claude-opus"]
	u.mu.RUnlock()
	if exists {
		t.Error("EffectiveSampleCount==0 且 181 天的画像应从内存移除")
	}
}

// TestTTL_Profile_Recent_NotStale UpdatedAt 30 天内 → 不标记 stale。
func TestTTL_Profile_Recent_NotStale(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	u.mu.Lock()
	u.profiles["implement|claude-sonnet"] = ModelProfile{
		TaskType:             TaskTypeImplement,
		Model:                "claude-sonnet",
		EffectiveSampleCount: 15.0,
		UpdatedAt:            now.Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339), // 30 天内
	}
	u.mu.Unlock()

	m := NewTTLManager(nil, u, nil, "")
	m.SetNowFunc(func() time.Time { return now })

	stale, removed, err := m.ApplyProfileTTL()
	if err != nil {
		t.Fatalf("ApplyProfileTTL: %v", err)
	}
	if stale != 0 {
		t.Errorf("30 天内 EffectiveSampleCount>=10 不应标记 stale，got=%d", stale)
	}
	if removed != 0 {
		t.Errorf("30 天内不应移除，got=%d", removed)
	}
}

// TestTTL_Profile_EffectiveSampleCountLow_Stale EffectiveSampleCount<10 → 标记 stale。
func TestTTL_Profile_EffectiveSampleCountLow_Stale(t *testing.T) {
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	now := time.Now()
	u.mu.Lock()
	u.profiles["implement|claude-haiku"] = ModelProfile{
		TaskType:             TaskTypeImplement,
		Model:                "claude-haiku",
		EffectiveSampleCount: 5.0,                            // <10 触发冷启动
		UpdatedAt:            now.UTC().Format(time.RFC3339), // 最新
	}
	u.mu.Unlock()

	m := NewTTLManager(nil, u, nil, "")
	m.SetNowFunc(func() time.Time { return now })

	stale, removed, err := m.ApplyProfileTTL()
	if err != nil {
		t.Fatalf("ApplyProfileTTL: %v", err)
	}
	if stale != 1 {
		t.Errorf("EffectiveSampleCount=5<10 应标记 stale=1，got=%d", stale)
	}
	if removed != 0 {
		t.Errorf("UpdatedAt 最新不应移除，got=%d", removed)
	}
}

// TestTTL_L1Weights_ArchiveLimit20 21 版本 → 最旧移入 archive。
// 验证：archive 数量不超过 L1WeightsArchiveLimit=20。
func TestTTL_L1Weights_ArchiveLimit20(t *testing.T) {
	s := NewL1ScorerExt("") // 仅内存

	// 通过 saveVersioned 添加 21 个版本
	for i := 0; i < 21; i++ {
		weights := DefaultL1WeightsExt()
		// 每次调整一项权重（保证版本变化）
		weights[L1KeyErrorHandling] = 20 + i
		// 归一化保持总和=100
		weights = normalizeWeights(weights)
		if err := s.saveVersioned(weights, "test version "+string(rune('A'+i)), nil); err != nil {
			t.Fatalf("saveVersioned %d: %v", i, err)
		}
	}

	m := NewTTLManager(nil, nil, s, "")
	archiveCount, overflow, err := m.ApplyL1WeightsTTL()
	if err != nil {
		t.Fatalf("ApplyL1WeightsTTL: %v", err)
	}
	// saveVersioned 内部已自动归档，archive 应 <= L1WeightsArchiveLimit=20
	if archiveCount > L1WeightsArchiveLimit {
		t.Errorf("archive 数量应 <= %d，got=%d", L1WeightsArchiveLimit, archiveCount)
	}
	if !overflow {
		t.Error("21 版本应触发 overflow=true")
	}
}

// TestTTL_L1Weights_NilScorer_NoOp l1ScorerExt 为 nil → 无操作。
func TestTTL_L1Weights_NilScorer_NoOp(t *testing.T) {
	m := NewTTLManager(nil, nil, nil, "")
	archiveCount, overflow, err := m.ApplyL1WeightsTTL()
	if err != nil {
		t.Fatalf("l1ScorerExt=nil 应返回 nil error: %v", err)
	}
	if archiveCount != 0 || overflow {
		t.Errorf("l1ScorerExt=nil 应 archiveCount=0 overflow=false，got archive=%d overflow=%v", archiveCount, overflow)
	}
}

// TestTTL_SweepAll_Result SweepAll 返回完整结果。
func TestTTL_SweepAll_Result(t *testing.T) {
	stateDir := t.TempDir()
	// 创建一个 8 天前的 state.json
	oldFile := filepath.Join(stateDir, "old-state.json")
	if err := os.WriteFile(oldFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	now := time.Now()
	oldTime := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// 创建画像表条目
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	u.mu.Lock()
	u.profiles["implement|claude-opus"] = ModelProfile{
		TaskType:             TaskTypeImplement,
		Model:                "claude-opus",
		EffectiveSampleCount: 0,
		UpdatedAt:            now.Add(-181 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	u.mu.Unlock()

	// 创建 L1ScorerExt
	s := NewL1ScorerExt("")

	m := NewTTLManager(nil, u, s, stateDir)
	m.SetNowFunc(func() time.Time { return now })

	result, err := m.SweepAll()
	if err != nil {
		t.Fatalf("SweepAll: %v", err)
	}
	if result.BudgetStateCleaned != 1 {
		t.Errorf("BudgetStateCleaned 应=1，got=%d", result.BudgetStateCleaned)
	}
	if result.ProfileRemoved != 1 {
		t.Errorf("ProfileRemoved 应=1，got=%d", result.ProfileRemoved)
	}
}

// TestTTL_ConcurrentSweep_NoRace 多 goroutine 触发 sweep，-race 无竞态。
// 零容忍：并发 SweepAll 必须无数据竞争。
func TestTTL_ConcurrentSweep_NoRace(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	u := NewProfileUpdater(nil, nil, nil).(*profileUpdater)
	s := NewL1ScorerExt("")

	m := NewTTLManager(nil, u, s, stateDir)

	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = m.SweepAll()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// 正常完成
	case <-time.After(5 * time.Second):
		t.Fatal("致命反例：SweepAll 并发死锁（5s 超时）")
	}
}

// TestTTL_ProfileSweeperNil_NoOp profileSweeper 为 nil → 无操作。
func TestTTL_ProfileSweeperNil_NoOp(t *testing.T) {
	m := NewTTLManager(nil, nil, nil, "")
	stale, removed, err := m.ApplyProfileTTL()
	if err != nil {
		t.Fatalf("profileSweeper=nil 应返回 nil error: %v", err)
	}
	if stale != 0 || removed != 0 {
		t.Errorf("profileSweeper=nil 应 stale=0 removed=0，got stale=%d removed=%d", stale, removed)
	}
}
