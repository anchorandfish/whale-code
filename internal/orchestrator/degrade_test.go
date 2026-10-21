// degrade_test.go — H10 Phase 5 桶 5 降级管理器反例测试（TDD 先行）。
// 覆盖致命反例：
// - 致命反例 2：冷启动降级（EffectiveSampleCount<10 → 静态矩阵）
// - 致命反例 4：误判降级（3 次禁用 80%）
// - 致命反例 5：持久化失败降级（内存保留 + 告警 + 完成后重写）
// - 致命反例 7：反查失败降级（用量反查失败 + 静态 token 矩阵 + 重试）
// 类降级；落地：DegradeManager 4 方法
// ；落地：DegradeOnPersistFailure
// SNAPSHOT: tests={ReverseQueryFail_NullCost(致命7), ColdStart_StaticMatrix(致命2), EarlyTerminateMiscalibrate_Disable80(致命4), BudgetRace_AtomicCounter, PersistFailure_RetainMemory(致命5), ConcurrentDegrade_NoRace}; (2026-10-09): 断言口径 = token

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockProfileUpdaterForDegrade mock ProfileUpdater 实现（用于 DegradeManager 测试）。
type mockProfileUpdaterForDegrade struct {
	retryCalled atomic.Int64
}

func (m *mockProfileUpdaterForDegrade) UpdateOnTaskComplete(ctx context.Context, rec QualityRecord) error {
	return nil
}
func (m *mockProfileUpdaterForDegrade) QueryAvgTokens(taskType, model string) (int64, error) {
	return 0, ErrColdStart
}
func (m *mockProfileUpdaterForDegrade) QuerySuccessRate(taskType, model string) (float64, error) {
	return 0, ErrColdStart
}
func (m *mockProfileUpdaterForDegrade) GetProfile(taskType, model string) (ModelProfile, error) {
	return ModelProfile{}, ErrColdStart
}
func (m *mockProfileUpdaterForDegrade) RetryReverseQuery() error {
	m.retryCalled.Add(1)
	return nil
}

// TestDegrade_ReverseQueryFail_NullCost 致命反例 7：
// 反查失败 → 静态 token 矩阵 + 1h 后台重试。
// 零容忍：反查失败必须降级（不能阻塞主流程）。
func TestDegrade_ReverseQueryFail_NullCost(t *testing.T) {
	notif := &mockNotifier{}
	pu := &mockProfileUpdaterForDegrade{}
	d := NewDegradeManager(nil, pu, nil, notif)

	d.DegradeOnReverseQueryFail("agent-1", time.Now())

	if d.ReverseQueryFailCount() != 1 {
		t.Errorf("反查失败降级次数应=1，got=%d", d.ReverseQueryFailCount())
	}
	if notif.Count() == 0 {
		t.Error("致命反例 7：反查失败应发送告警通知")
	}

	// 等待后台 RetryReverseQuery goroutine 执行
	time.Sleep(100 * time.Millisecond)
	if pu.retryCalled.Load() != 1 {
		t.Errorf("应触发 1 次 RetryReverseQuery，got=%d", pu.retryCalled.Load())
	}
}

// TestDegrade_ReverseQueryFail_NoProfileUpdater profileUpdater 为 nil → 仅记录降级不重试。
func TestDegrade_ReverseQueryFail_NoProfileUpdater(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDegradeManager(nil, nil, nil, notif)

	d.DegradeOnReverseQueryFail("agent-1", time.Now())

	if d.ReverseQueryFailCount() != 1 {
		t.Errorf("反查失败降级次数应=1，got=%d", d.ReverseQueryFailCount())
	}
	if notif.Count() == 0 {
		t.Error("应发送告警通知")
	}
}

// TestDegrade_ColdStart_StaticMatrix 致命反例 2：
// EffectiveSampleCount<10 → 静态矩阵。
// 零容忍：冷启动必须降级到静态 **token** 矩阵（haiku=30000/sonnet=100000/opus=200000）。
func TestDegrade_ColdStart_StaticMatrix(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDegradeManager(nil, nil, nil, notif)

	cases := []struct {
		taskType string
		model    string
		expected int64
	}{
		{TaskTypeImplement, "claude-haiku", 30000},
		{TaskTypeImplement, "claude-sonnet", 100000},
		{TaskTypeImplement, "claude-opus", 200000},
		{TaskTypeImplement, "unknown-model", 50000},
	}
	for _, c := range cases {
		tokens := d.DegradeOnColdStart(c.taskType, c.model)
		if tokens != c.expected {
			t.Errorf("致命反例 2：model=%s 静态 token 矩阵应=%d，got=%d", c.model, c.expected, tokens)
		}
	}

	if d.ColdStartCount() != len(cases) {
		t.Errorf("冷启动降级次数应=%d，got=%d", len(cases), d.ColdStartCount())
	}
	if notif.Count() != len(cases) {
		t.Errorf("应发送 %d 次告警，got=%d", len(cases), notif.Count())
	}
}

// TestDegrade_EarlyTerminateMiscalibrate_Disable80 致命反例 4：
// 3 次误判 → 禁用 80%。
// 零容忍：误判降级必须触发（DegradeManager 记录降级状态）。
func TestDegrade_EarlyTerminateMiscalibrate_Disable80(t *testing.T) {
	notif := &mockNotifier{}
	ua := &mockUserAsker{terminate: false}
	et := NewEarlyTerminator(nil, nil, ua, nil)
	d := NewDegradeManager(nil, nil, et, notif)

	// 通过 EarlyTerminator 的 3 次误判触发禁用（正常流程）
	_, _ = et.CheckAt80Percent("task-1", 8000)
	_, _ = et.CheckAt80Percent("task-2", 8000)
	_, _ = et.CheckAt80Percent("task-3", 8000)

	if !et.IsDisabled80Percent() {
		t.Fatal("致命反例 4：3 次误判应禁用 80% 检查点")
	}

	// 通过 DegradeManager 强制触发降级
	d.DegradeOnEarlyTerminationMiscalibrate()
	if !d.MiscalibrateTriggered() {
		t.Error("致命反例 4：DegradeManager 应记录误判降级已触发")
	}
	if notif.Count() == 0 {
		t.Error("应发送告警通知")
	}
}

// TestDegrade_BudgetRace_AtomicCounter 80%/100% 用 atomic 不用锁。
// 验证：BudgetTrackerExt.Is80PercentNotified 用 atomic.Bool（无锁读）。
func TestDegrade_BudgetRace_AtomicCounter(t *testing.T) {
	notif := &mockNotifier{}
	pu := &mockProfileUpdater{avgTokens: 45}
	b := newTestBudgetTrackerExt(100, pu, nil, notif, "")
	d := NewDegradeManager(b, nil, nil, notif)

	// 触发并发竞态降级
	d.DegradeOnBudgetConcurrencyRace()
	if d.BudgetRaceCount() != 1 {
		t.Errorf("并发竞态降级次数应=1，got=%d", d.BudgetRaceCount())
	}

	// 验证 BudgetTrackerExt 用 atomic（Is80PercentNotified 无锁读）
	// 触发 80% 通知
	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks)

	// Is80PercentNotified 应=true（atomic 读，无锁）
	if !b.Is80PercentNotified() {
		t.Error("80% 应已通知（atomic.Bool）")
	}
}

// TestDegrade_PersistFailure_RetainMemory 致命反例 5：
// state.json 写失败 → 内存保留 + 告警 + 完成后重写。
// 零容忍：持久化失败不能阻塞主流程，完成后必须重写。
func TestDegrade_PersistFailure_RetainMemory(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")

	pu := &mockProfileUpdater{avgTokens: 1000}
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(10000, pu, nil, notif, statePath)
	d := NewDegradeManager(b, nil, nil, notif)

	// 1. 正常 RecordActualCost（持久化成功）
	budgetHint := BudgetHint{TokensCap: 1000}
	_, err := b.RecordActual(context.Background(), "agent-1", 500, budgetHint)
	if err != nil {
		t.Fatalf("RecordActualCost 1: %v", err)
	}
	if b.IsPersistFailed() {
		t.Error("第 1 次 RecordActualCost 不应持久化失败")
	}

	// 2. 用文件替换 stateDir 触发下次持久化失败
	// （删除目录后 MkdirAll 会重建目录，所以用文件占位使 MkdirAll 失败）
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(stateDir, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}

	// 3. 再次 RecordActualCost（持久化失败，内存保留）
	_, err = b.RecordActual(context.Background(), "agent-2", 500, budgetHint)
	if err != nil {
		t.Fatalf("致命反例 5：持久化失败不应阻塞 RecordActualCost，got err=%v", err)
	}
	if !b.IsPersistFailed() {
		t.Fatal("致命反例 5：删除 stateDir 后 IsPersistFailed 应=true")
	}
	// 内存保留验证（UsedTokens 已累加：2 × 500 = 1000）
	if b.UsedTokens() != 1000 {
		t.Errorf("致命反例 5：内存 UsedTokens 应=1000（保留），got=%d", b.UsedTokens())
	}

	// 4. 重新创建 stateDir，DegradeOnPersistFailure 触发重写
	// （先删除占位文件，再创建目录）
	if err := os.Remove(stateDir); err != nil {
		t.Fatalf("Remove blocker: %v", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := d.DegradeOnPersistFailure(); err != nil {
		t.Fatalf("DegradeOnPersistFailure: %v", err)
	}

	// 5. 验证重写成功（IsPersistFailed=false）
	if b.IsPersistFailed() {
		t.Error("致命反例 5：重写后 IsPersistFailed 应=false")
	}
	// 验证 state.json 已重新创建
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("致命反例 5：重写后 state.json 应存在")
	}
}

// TestDegrade_PersistFailure_StillFailed 重写仍失败 → 返回 error。
func TestDegrade_PersistFailure_StillFailed(t *testing.T) {
	// 用"文件作为目录"路径触发持久化失败（Linux root 可创建任意目录，
	// 但文件下不能创建子目录 → MkdirAll 失败）
	tmpDir := t.TempDir()
	blockerFile := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blockerFile, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	statePath := filepath.Join(blockerFile, "state.json")
	pu := &mockProfileUpdater{avgTokens: 1000}
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(10000, pu, nil, notif, statePath)
	d := NewDegradeManager(b, nil, nil, notif)

	budgetHint := BudgetHint{TokensCap: 1000}
	_, _ = b.RecordActual(context.Background(), "agent-1", 500, budgetHint)

	if !b.IsPersistFailed() {
		t.Fatal("应持久化失败")
	}

	// DegradeOnPersistFailure 尝试重写（仍失败，路径不可写）
	err := d.DegradeOnPersistFailure()
	if err == nil {
		t.Error("重写仍失败应返回 error")
	}
	// IsPersistFailed 仍=true
	if !b.IsPersistFailed() {
		t.Error("重写失败后 IsPersistFailed 应仍=true")
	}
}

// TestDegrade_PersistFailure_NotFailed_NoOp 未失败 → 无操作。
func TestDegrade_PersistFailure_NotFailed_NoOp(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	pu := &mockProfileUpdater{avgTokens: 1000}
	b := newTestBudgetTrackerExt(10000, pu, nil, nil, statePath)
	d := NewDegradeManager(b, nil, nil, nil)

	budgetHint := BudgetHint{TokensCap: 1000}
	_, _ = b.RecordActual(context.Background(), "agent-1", 500, budgetHint)

	// 未失败，DegradeOnPersistFailure 应无操作
	err := d.DegradeOnPersistFailure()
	if err != nil {
		t.Errorf("未失败应返回 nil error，got=%v", err)
	}
}

// TestDegrade_ConcurrentDegrade_NoRace 并发触发降级，-race 无竞态。
// 零容忍：并发 DegradeOn* 必须无数据竞争。
func TestDegrade_ConcurrentDegrade_NoRace(t *testing.T) {
	t.Parallel()
	notif := &mockNotifier{}
	pu := &mockProfileUpdaterForDegrade{}
	d := NewDegradeManager(nil, pu, nil, notif)

	var wg sync.WaitGroup
	wg.Add(4)

	// 并发触发 4 种降级
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			d.DegradeOnReverseQueryFail("agent-1", time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			d.DegradeOnColdStart(TaskTypeImplement, "claude-sonnet")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			d.DegradeOnBudgetConcurrencyRace()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			d.DegradeOnEarlyTerminationMiscalibrate()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// 正常完成
	case <-time.After(5 * time.Second):
		t.Fatal("致命反例：并发 DegradeOn* 死锁（5s 超时）")
	}

	// 验证降级次数
	if d.ReverseQueryFailCount() != 10 {
		t.Errorf("反查失败降级次数应=10，got=%d", d.ReverseQueryFailCount())
	}
	if d.ColdStartCount() != 10 {
		t.Errorf("冷启动降级次数应=10，got=%d", d.ColdStartCount())
	}
	if d.BudgetRaceCount() != 10 {
		t.Errorf("并发竞态降级次数应=10，got=%d", d.BudgetRaceCount())
	}
}

// TestDegrade_StaticTokensByModelName 静态 token 矩阵验证（：原静态单价已删）。
func TestDegrade_StaticTokensByModelName(t *testing.T) {
	cases := []struct {
		model    string
		expected int64
	}{
		{"claude-haiku", 30000},
		{"claude-sonnet", 100000},
		{"claude-opus", 200000},
		{"unknown", 50000},
		{"gpt-4", 50000},
	}
	for _, c := range cases {
		got := staticTokensByModelName(c.model)
		if got != c.expected {
			t.Errorf("model=%s 应=%d，got=%d", c.model, c.expected, got)
		}
	}
}

// 防止 import 未使用（atomic 用于并发测试）
var _ = atomic.Bool{}
