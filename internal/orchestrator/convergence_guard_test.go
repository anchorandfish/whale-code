// convergence_guard_test.go — H12 收敛保护器反例测试（TDD 先行，§2.5）。
// 覆盖三件套：
// - ShouldStop：<1% 连续 2 次停止
// - InCooldown：24h 冷却期
// - RecordDelta：3 次反转触发回滚
//
// 设计说明 §2.5；落地：三件套反例 + 持久化
// SNAPSHOT: tests={ShouldStop_SmallChange_Twice, ShouldStop_ResetOnLargeChange, InCooldown_24h, InCooldown_NeverTuned_NoCooldown, RecordDelta_ThreeReversals_Rollback, RecordDelta_TwoNotEnough, Persistence_LoadAfterWrite, Persistence_EmptyPath_MemoryOnly}
package orchestrator

import (
	"path/filepath"
	"testing"
	"time"
)

// fixedNow 返回固定时间用于测试。
func fixedNow() time.Time {
	return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
}

// TestShouldStop_SmallChange_Twice 连续 2 次 <1% 变化触发停止（§2.5.1）。
func TestShouldStop_SmallChange_Twice(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	before := map[string]int{"a": 50, "b": 50}
	// 第一次 <1% 变化（a 加 0，即不变）
	after1 := map[string]int{"a": 50, "b": 50}
	if g.ShouldStop(before, after1) {
		t.Errorf("第一次小变化不应停止，smallChangeCount=%d", g.GetSmallChangeCount())
	}
	if g.GetSmallChangeCount() != 1 {
		t.Errorf("first small change count = %d, want 1", g.GetSmallChangeCount())
	}

	// 第二次 <1% 变化 → 触发停止
	after2 := map[string]int{"a": 50, "b": 50}
	if !g.ShouldStop(before, after2) {
		t.Errorf("连续 2 次小变化应停止")
	}
}

// TestShouldStop_ResetOnLargeChange 大变化重置 smallChangeCount（§2.5.1）。
func TestShouldStop_ResetOnLargeChange(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	before := map[string]int{"a": 50, "b": 50}
	// 第一次小变化
	after1 := map[string]int{"a": 50, "b": 50}
	g.ShouldStop(before, after1)
	if g.GetSmallChangeCount() != 1 {
		t.Fatalf("first small change count = %d, want 1", g.GetSmallChangeCount())
	}

	// 第二次大变化（变化率 >1%，a 减少 5 即 10%）
	after2 := map[string]int{"a": 45, "b": 50}
	if g.ShouldStop(before, after2) {
		t.Errorf("大变化不应停止")
	}
	if g.GetSmallChangeCount() != 0 {
		t.Errorf("after large change, count = %d, want 0 (reset)", g.GetSmallChangeCount())
	}
}

// TestShouldStop_BeforeZero_NoStop before 全零不停止（避免除零）。
func TestShouldStop_BeforeZero_NoStop(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	before := map[string]int{}
	after := map[string]int{}
	if g.ShouldStop(before, after) {
		t.Errorf("before all zero should not stop")
	}
}

// TestInCooldown_24h 24h 冷却期生效（§2.5.2）。
func TestInCooldown_24h(t *testing.T) {
	g := NewConvergenceGuard("")

	// 固定时间基线
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	g.SetNowFunc(func() time.Time { return t0 })
	if err := g.UpdateLastTune(); err != nil {
		t.Fatalf("UpdateLastTune: %v", err)
	}

	// t0 + 1h 应在冷却期
	g.SetNowFunc(func() time.Time { return t0.Add(1 * time.Hour) })
	if !g.InCooldown() {
		t.Errorf("t0+1h should be in cooldown")
	}

	// t0 + 23h59m 仍在冷却期
	g.SetNowFunc(func() time.Time { return t0.Add(23*time.Hour + 59*time.Minute) })
	if !g.InCooldown() {
		t.Errorf("t0+23h59m should be in cooldown")
	}

	// t0 + 24h+1s 超出冷却期
	g.SetNowFunc(func() time.Time { return t0.Add(24*time.Hour + time.Second) })
	if g.InCooldown() {
		t.Errorf("t0+24h+1s should not be in cooldown")
	}
}

// TestInCooldown_NeverTuned_NoCooldown 从未调优无冷却期（§2.5.2）。
func TestInCooldown_NeverTuned_NoCooldown(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)
	if g.InCooldown() {
		t.Errorf("never tuned should not be in cooldown")
	}
}

// TestRecordDelta_ThreeReversals_Rollback 3 次反转触发回滚（§2.5.3，+-+ 模式）。
func TestRecordDelta_ThreeReversals_Rollback(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	// 第 1 次：正向变化
	delta1 := map[string]int{"a": 5, "b": 3}
	if g.RecordDelta(delta1) {
		t.Errorf("first delta should not trigger rollback")
	}

	// 第 2 次：负向变化
	delta2 := map[string]int{"a": -5, "b": -3}
	if g.RecordDelta(delta2) {
		t.Errorf("second delta should not trigger rollback")
	}

	// 第 3 次：正向变化 → 触发 +-+ 模式
	delta3 := map[string]int{"a": 5, "b": 3}
	if !g.RecordDelta(delta3) {
		t.Errorf("third delta (reversal +-+) should trigger rollback")
	}
}

// TestRecordDelta_ThreeReversals_NegativePattern 3 次反转触发回滚（-+- 模式）。
func TestRecordDelta_ThreeReversals_NegativePattern(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	// 第 1 次：负向变化
	delta1 := map[string]int{"a": -5, "b": -3}
	if g.RecordDelta(delta1) {
		t.Errorf("first delta should not trigger rollback")
	}

	// 第 2 次：正向变化
	delta2 := map[string]int{"a": 5, "b": 3}
	if g.RecordDelta(delta2) {
		t.Errorf("second delta should not trigger rollback")
	}

	// 第 3 次：负向变化 → 触发 -+- 模式
	delta3 := map[string]int{"a": -5, "b": -3}
	if !g.RecordDelta(delta3) {
		t.Errorf("third delta (reversal -+-) should trigger rollback")
	}
}

// TestRecordDelta_TwoNotEnough 2 次反转不触发回滚（§2.5.3）。
func TestRecordDelta_TwoNotEnough(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	delta1 := map[string]int{"a": 5}
	if g.RecordDelta(delta1) {
		t.Errorf("1 delta should not trigger rollback")
	}

	delta2 := map[string]int{"a": -5}
	if g.RecordDelta(delta2) {
		t.Errorf("2 deltas should not trigger rollback (need 3)")
	}
}

// TestRecordDelta_SameDirection_NoReversal 同方向不触发回滚。
func TestRecordDelta_SameDirection_NoReversal(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	// 3 次同向不触发
	for i := 0; i < 3; i++ {
		if g.RecordDelta(map[string]int{"a": 5}) {
			t.Errorf("same direction should not trigger rollback (iter %d)", i)
		}
	}
}

// TestRecordDelta_ZeroDelta_NoReversal 零 delta 不触发回滚。
func TestRecordDelta_ZeroDelta_NoReversal(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	// 3 次零 delta 不触发（多数票为 0）
	for i := 0; i < 3; i++ {
		if g.RecordDelta(map[string]int{"a": 0, "b": 0}) {
			t.Errorf("zero delta should not trigger rollback (iter %d)", i)
		}
	}
}

// TestConvergenceGuard_Persistence_LoadAfterWrite 持久化写入后重新加载。
func TestConvergenceGuard_Persistence_LoadAfterWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last_tune.json")

	// 第一次构造 + 写入
	g1 := NewConvergenceGuard(path)
	t0 := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	g1.SetNowFunc(func() time.Time { return t0 })
	if err := g1.UpdateLastTune(); err != nil {
		t.Fatalf("UpdateLastTune: %v", err)
	}

	// 第二次构造 + 加载（崩溃恢复）
	g2 := NewConvergenceGuard(path)
	if !g2.GetLastTuneAt().Equal(t0) {
		t.Errorf("after reload, lastTuneAt = %v, want %v", g2.GetLastTuneAt(), t0)
	}
}

// TestConvergenceGuard_Persistence_EmptyPath_MemoryOnly persistencePath 空时仅内存。
func TestConvergenceGuard_Persistence_EmptyPath_MemoryOnly(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)
	if err := g.UpdateLastTune(); err != nil {
		t.Fatalf("UpdateLastTune (memory only): %v", err)
	}
	if g.GetLastTuneAt().IsZero() {
		t.Errorf("memory-only lastTuneAt should not be zero after UpdateLastTune")
	}
}

// TestConvergenceGuard_ResetSmallChangeCount 重置 smallChangeCount。
func TestConvergenceGuard_ResetSmallChangeCount(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	before := map[string]int{"a": 50}
	after := map[string]int{"a": 50}
	g.ShouldStop(before, after)
	if g.GetSmallChangeCount() != 1 {
		t.Fatalf("after 1 small change, count = %d, want 1", g.GetSmallChangeCount())
	}
	g.ResetSmallChangeCount()
	if g.GetSmallChangeCount() != 0 {
		t.Errorf("after reset, count = %d, want 0", g.GetSmallChangeCount())
	}
}

// TestConvergenceGuard_DeltaHistoryLimit delta 历史保留最近 5 个。
func TestConvergenceGuard_DeltaHistoryLimit(t *testing.T) {
	g := NewConvergenceGuard("")
	g.SetNowFunc(fixedNow)

	// 写 7 次 delta
	for i := 0; i < 7; i++ {
		g.RecordDelta(map[string]int{"a": 1})
	}
	hist := g.GetDeltaHistory()
	if len(hist) != ConvergenceGuardDeltaHistoryLimit {
		t.Errorf("delta history len = %d, want %d", len(hist), ConvergenceGuardDeltaHistoryLimit)
	}
}
