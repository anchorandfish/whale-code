// budget_tracker_test.go — H10 Phase 5 桶 3 预算追踪器扩展反例测试（TDD 先行）。
// 覆盖致命反例：
// - 致命反例 3：BudgetTracker 并发锁竞争（3 子任务同时 CheckBeforeDispatch）
// - 致命反例 5：BudgetState 持久化失败（磁盘满/权限）降级
// - 致命反例 6：80% 两作用域同时触发（编排总预算 + 子任务 budget_hint）去重
// line 972-991-双层预算 + 并行预算竞态预防；落地：BudgetTrackerExt 实现
// + 派发前预估算法；落地：CheckBeforeDispatch
// SNAPSHOT: tests={ConcurrentCheckBeforeDispatch_NoDeadlock(致命3), ColdStart_UseStaticMatrix, ExceedBudget_ReturnFalse, 80PercentNotifyOnce(致命6), 80PercentNotify_OutsideLock(致命3关联), RecordActual_BudgetExceeded, BudgetExceeded_NotOverrideL0Pass, PersistFailure_Degrade(致命5), 80PercentTwoScope_Dedup(致命6)}
//
// ★ 2026-10-09：本文件断言的**美分口径全部改为 token 口径**；cost 相关用例（仅 cost 超限）删除。

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

// mockProfileUpdater mock ProfileUpdater 实现（测试用）。
type mockProfileUpdater struct {
	avgTokens int64
	err       error
}

func (m *mockProfileUpdater) UpdateOnTaskComplete(ctx context.Context, rec QualityRecord) error {
	return nil
}
func (m *mockProfileUpdater) QueryAvgTokens(taskType, model string) (int64, error) {
	return m.avgTokens, m.err
}
func (m *mockProfileUpdater) QuerySuccessRate(taskType, model string) (float64, error) {
	return 0.8, nil
}
func (m *mockProfileUpdater) GetProfile(taskType, model string) (ModelProfile, error) {
	return ModelProfile{}, nil
}

// newTestBudgetTrackerExt 创建测试用 BudgetTrackerExt。
// ★ ：totalTokens 为编排任务总 **token** 上限。
func newTestBudgetTrackerExt(totalTokens int64, pu ProfileUpdater, qs *QualityStore, notifier Notifier, statePath string) *BudgetTrackerExt {
	state := &BudgetStateExt{
		OrchestrationID:  "test-orch-1",
		TotalTokensCap:   totalTokens,
		SubtaskEstimates: make(map[string]Estimate),
	}
	return NewBudgetTrackerExt(state, pu, qs, notifier, statePath)
}

// TestBudgetTrackerExt_ConcurrentCheckBeforeDispatch_NoDeadlock 致命反例 3：
// 3 goroutine 同时 CheckBeforeDispatch，sync.Mutex + 锁外通知 + sync.Once，-race 无死锁。
// 零容忍：并发 CheckBeforeDispatch 必须无死锁（锁内不调 notify）。
func TestBudgetTrackerExt_ConcurrentCheckBeforeDispatch_NoDeadlock(t *testing.T) {
	t.Parallel()
	// mock ProfileUpdater 返回 1000 tokens 预估（不冷启动）
	pu := &mockProfileUpdater{avgTokens: 1000}
	// 用 mockNotifier 收集通知
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(10000, pu, nil, notif, "") // 总上限 10000 token

	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t3", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}

	const N = 3
	var wg sync.WaitGroup
	wg.Add(N)
	var successCount atomic.Int64
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			ok, err := b.CheckBeforeDispatch(context.Background(), subtasks)
			if err != nil {
				t.Errorf("CheckBeforeDispatch: %v", err)
				return
			}
			if ok {
				successCount.Add(1)
			}
		}()
	}
	// 等待所有 goroutine 完成（防死锁：用 timeout）
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// 正常完成
	case <-time.After(5 * time.Second):
		t.Fatal("致命反例 3：CheckBeforeDispatch 死锁（5s 超时）")
	}

	// 3 次都应成功（总上限 10000 - 3*3000 = 1000 > 0）
	if successCount.Load() != N {
		t.Errorf("应全部成功，got=%d/%d", successCount.Load(), N)
	}
}

// TestBudgetTrackerExt_ColdStart_UseStaticMatrix EffectiveSampleCount<10 → 用静态 token 矩阵预估。
func TestBudgetTrackerExt_ColdStart_UseStaticMatrix(t *testing.T) {
	// mock ProfileUpdater 返回 ErrColdStart
	pu := &mockProfileUpdater{err: ErrColdStart}
	b := newTestBudgetTrackerExt(1_000_000, pu, nil, nil, "")

	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-haiku"},
	}
	ok, err := b.CheckBeforeDispatch(context.Background(), subtasks)
	if err != nil {
		t.Fatalf("CheckBeforeDispatch 冷启动应成功: %v", err)
	}
	if !ok {
		t.Error("冷启动总预算未超限应返回 true")
	}

	// ★ ：静态矩阵现在给的是 **token** 预估（sonnet=100000, haiku=30000）
	snap := b.Snapshot()
	if snap.SubtaskEstimates["t1"].EstimatedTokens != 100000 {
		t.Errorf("冷启动 sonnet 静态 token 矩阵应=100000，got=%d", snap.SubtaskEstimates["t1"].EstimatedTokens)
	}
	if snap.SubtaskEstimates["t2"].EstimatedTokens != 30000 {
		t.Errorf("冷启动 haiku 静态 token 矩阵应=30000，got=%d", snap.SubtaskEstimates["t2"].EstimatedTokens)
	}
}

// TestBudgetTrackerExt_ExceedBudget_ReturnFalse 已用+预估>总上限 → 返回 false + ErrBudgetExceeded。
func TestBudgetTrackerExt_ExceedBudget_ReturnFalse(t *testing.T) {
	pu := &mockProfileUpdater{avgTokens: 100}           // 每个 100 token
	b := newTestBudgetTrackerExt(500, pu, nil, nil, "") // 总上限 500 token

	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t3", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t4", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t5", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t6", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	} // 6 * 100 = 600 > 500
	ok, err := b.CheckBeforeDispatch(context.Background(), subtasks)
	if ok {
		t.Error("6*100=600 > 总上限 500，应返回 false")
	}
	if err != ErrBudgetExceeded {
		t.Errorf("应返回 ErrBudgetExceeded，got=%v", err)
	}
}

// TestBudgetTrackerExt_80PercentNotifyOnce 致命反例 6 关联：
// 多次触发 80% → sync.Once 只通知 1 次。
func TestBudgetTrackerExt_80PercentNotifyOnce(t *testing.T) {
	pu := &mockProfileUpdater{avgTokens: 50} // 每个 50 token
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(100, pu, nil, notif, "") // 总上限 100 token，80% 阈值=80

	// 第 1 次：2 个子任务 * 50 = 100 > 80 → 触发 80% 通知（100 > 100 为假，未超限）
	subtasks1 := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks1)

	if notif.Count() != 1 {
		t.Errorf("第 1 次 80%% 应通知 1 次，got=%d", notif.Count())
	}
	if !b.Is80PercentNotified() {
		t.Error("Is80PercentNotified 应=true")
	}

	// 第 2 次：再触发 80%（应不通知，sync.Once 保证只 1 次）
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks1)
	if notif.Count() != 1 {
		t.Errorf("致命反例 6：sync.Once 应保证只通知 1 次，got=%d", notif.Count())
	}
}

// TestBudgetTrackerExt_80PercentNotify_OutsideLock 致命反例 3 关联：
// 80% 通知在锁外执行（防死锁，v0.3 改进 2）。
// 验证：CheckBeforeDispatch 持锁时不阻塞通知 channel 发送。
func TestBudgetTrackerExt_80PercentNotify_OutsideLock(t *testing.T) {
	pu := &mockProfileUpdater{avgTokens: 50}
	ch := make(chan struct{}, 1) // 带缓冲 channel
	b := newTestBudgetTrackerExt(100, pu, nil, nil, "")
	b.Register80PercentNotifier(ch)

	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks)

	// 验证 channel 收到通知
	select {
	case <-ch:
		// 成功
	case <-time.After(1 * time.Second):
		t.Error("80% 通知应在锁外发送到 channel")
	}
}

// TestBudgetTrackerExt_80PercentNotify_NonBlockingChannel channel 满时不阻塞。
func TestBudgetTrackerExt_80PercentNotify_NonBlockingChannel(t *testing.T) {
	pu := &mockProfileUpdater{avgTokens: 50}
	ch := make(chan struct{}) // 无缓冲 channel，无消费者
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(100, pu, nil, notif, "")
	b.Register80PercentNotifier(ch)

	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}

	// 应在 1s 内完成（非阻塞发送，channel 满时降级到 Notifier）
	done := make(chan struct{})
	go func() {
		_, _ = b.CheckBeforeDispatch(context.Background(), subtasks)
		close(done)
	}()
	select {
	case <-done:
		// 成功
	case <-time.After(1 * time.Second):
		t.Fatal("CheckBeforeDispatch 应在 1s 内完成（非阻塞通知）")
	}
}

// TestBudgetTrackerExt_RecordActual_BudgetExceeded 实际超限 → BudgetExceeded=true + 写 quality.jsonl。
func TestBudgetTrackerExt_RecordActual_BudgetExceeded(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(10000, nil, qs, notif, "")

	// budget_hint: TokensCap=1000；实际用量 2000 → 超限
	budgetHint := BudgetHint{TokensCap: 1000}
	result, err := b.RecordActual(context.Background(), "agent-1", 2000, budgetHint)
	if err != nil {
		t.Fatalf("RecordActual: %v", err)
	}
	if !result.BudgetExceeded {
		t.Error("实际超限 BudgetExceeded 应=true")
	}
	if result.ExceedReason != ExceedReasonTokens {
		t.Errorf("ExceedReason 应=tokens_exceeded，got=%s", result.ExceedReason)
	}

	// 验证 quality.jsonl 写入
	rec, err := qs.QueryByTaskID("agent-1")
	if err != nil {
		t.Fatalf("QueryByTaskID: %v", err)
	}
	if !rec.BudgetExceeded {
		t.Error("quality.jsonl 应记录 budget_exceeded=true")
	}
	if rec.ExceedReason != ExceedReasonTokens {
		t.Errorf("quality.jsonl ExceedReason 应=tokens_exceeded，got=%s", rec.ExceedReason)
	}
}

// TestBudgetTrackerExt_BudgetExceeded_NotOverrideL0Pass 致命反例关联：
// budget_exceeded 独立字段不覆盖 l0_pass（§8.3 line 983 关键约束）。
func TestBudgetTrackerExt_BudgetExceeded_NotOverrideL0Pass(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	b := newTestBudgetTrackerExt(10000, nil, qs, nil, "")

	// L0 通过但 budget 超限（两者解耦）
	budgetHint := BudgetHint{TokensCap: 1000}
	result, err := b.RecordActual(context.Background(), "agent-1", 2000, budgetHint)
	if err != nil {
		t.Fatalf("RecordActual: %v", err)
	}

	// 验证 budget_exceeded=true 但 l0_pass=true（独立字段不覆盖）
	if !result.BudgetExceeded {
		t.Error("budget_exceeded 应=true（超限）")
	}

	// 读 quality.jsonl 验证 l0_pass=true 且 budget_exceeded=true（两者独立）
	rec, err := qs.QueryByTaskID("agent-1")
	if err != nil {
		t.Fatalf("QueryByTaskID: %v", err)
	}
	if !rec.BudgetExceeded {
		t.Error("quality.jsonl budget_exceeded 应=true")
	}
	if !rec.L0Pass {
		t.Error("quality.jsonl l0_pass 应=true（独立字段，不被 budget_exceeded 覆盖）")
	}
}

// TestBudgetTrackerExt_PersistFailure_Degrade 致命反例 5：
// state.json 写入失败 → 内存保留 + 告警 + 完成后重写。
// 零容忍：持久化失败不能阻塞主流程。
func TestBudgetTrackerExt_PersistFailure_Degrade(t *testing.T) {
	// 用"文件作为目录"路径模拟持久化失败（Linux root 可创建任意目录，
	// 但文件下不能创建子目录 → MkdirAll 失败）
	tmpDir := t.TempDir()
	blockerFile := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blockerFile, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	// statePath = blocker/state.json → blocker 是文件不是目录 → MkdirAll 失败
	statePath := filepath.Join(blockerFile, "state.json")
	pu := &mockProfileUpdater{avgTokens: 1000}
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(10000, pu, nil, notif, statePath)

	budgetHint := BudgetHint{TokensCap: 1000}
	_, err := b.RecordActual(context.Background(), "agent-1", 500, budgetHint)
	if err != nil {
		t.Fatalf("致命反例 5：持久化失败不应阻塞 RecordActual，got err=%v", err)
	}

	// 验证内存状态保留（UsedTokens 已累加）
	if b.UsedTokens() != 500 {
		t.Errorf("内存 UsedTokens 应=500，got=%d", b.UsedTokens())
	}

	// 验证持久化失败标志已设置
	if !b.IsPersistFailed() {
		t.Error("致命反例 5：IsPersistFailed 应=true")
	}

	// 验证告警已发送
	if notif.Count() == 0 {
		t.Error("致命反例 5：持久化失败应发送告警通知")
	}

	// 完成后强制重写（仍是失败路径，但不应 panic）
	_ = b.ForceRewriteState()
}

// TestBudgetTrackerExt_PersistSuccess 持久化成功。
func TestBudgetTrackerExt_PersistSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	pu := &mockProfileUpdater{avgTokens: 1000}
	b := newTestBudgetTrackerExt(10000, pu, nil, nil, statePath)

	budgetHint := BudgetHint{TokensCap: 1000}
	_, err := b.RecordActual(context.Background(), "agent-1", 500, budgetHint)
	if err != nil {
		t.Fatalf("RecordActual: %v", err)
	}

	// 验证 state.json 存在
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatal("state.json 应存在")
	}
	if b.IsPersistFailed() {
		t.Error("持久化成功时 IsPersistFailed 应=false")
	}
}

// TestBudgetTrackerExt_80PercentTwoScope_Dedup 致命反例 6：
// 80% 编排总预算维度 vs 80% 子任务 tokens_cap 维度同时触发 → 5min 窗口合并。
// 零容忍：两作用域不能同时向用户呈现 2 个 80% 通知。
func TestBudgetTrackerExt_80PercentTwoScope_Dedup(t *testing.T) {
	pu := &mockProfileUpdater{avgTokens: 50}
	notif := &mockNotifier{}
	b := newTestBudgetTrackerExt(100, pu, nil, notif, "") // 总上限 100 token，80% 阈值=80

	// 编排总预算 80% 触发（2 个子任务 * 50 = 100 > 80）
	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
		{TaskID: "t2", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks)

	firstCount := notif.Count()
	if firstCount != 1 {
		t.Errorf("第 1 次编排总预算 80%% 应通知 1 次，got=%d", firstCount)
	}

	// 子任务 tokens_cap 80% 触发（同一 BudgetTrackerExt 5min 内）
	// 调用 CheckBeforeDispatch 再次（模拟子任务 80% 检查点）
	// sync.Once 保证 5min 窗口内不重复通知
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks)

	if notif.Count() != firstCount {
		t.Errorf("致命反例 6：5min 窗口内两作用域应去重，应仍=%d，got=%d", firstCount, notif.Count())
	}
}

// TestBudgetTrackerExt_Snapshot 取快照不影响内部状态。
func TestBudgetTrackerExt_Snapshot(t *testing.T) {
	pu := &mockProfileUpdater{avgTokens: 50}
	b := newTestBudgetTrackerExt(1000, pu, nil, nil, "")

	subtasks := []SubtaskInfo{
		{TaskID: "t1", TaskType: TaskTypeImplement, Model: "claude-sonnet"},
	}
	_, _ = b.CheckBeforeDispatch(context.Background(), subtasks)

	snap := b.Snapshot()
	if snap.OrchestrationID != "test-orch-1" {
		t.Errorf("Snapshot OrchestrationID 应=test-orch-1，got=%s", snap.OrchestrationID)
	}
	if len(snap.SubtaskEstimates) != 1 {
		t.Errorf("Snapshot SubtaskEstimates 应=1，got=%d", len(snap.SubtaskEstimates))
	}

	// 修改快照不应影响内部状态
	snap.SubtaskEstimates["t2"] = Estimate{TaskID: "t2"}
	snap2 := b.Snapshot()
	if _, ok := snap2.SubtaskEstimates["t2"]; ok {
		t.Error("Snapshot 应返回副本，外部修改不应影响内部")
	}
}

// TestBudgetTrackerExt_ExceededSubtasks 超预算子任务列表正确。
func TestBudgetTrackerExt_ExceededSubtasks(t *testing.T) {
	tmpDir := t.TempDir()
	qs, err := NewQualityStore(tmpDir + "/quality.jsonl")
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	b := newTestBudgetTrackerExt(10000, nil, qs, nil, "")

	budgetHint := BudgetHint{TokensCap: 1000}
	_, _ = b.RecordActual(context.Background(), "agent-1", 2000, budgetHint)
	_, _ = b.RecordActual(context.Background(), "agent-2", 2000, budgetHint)

	exceeded := b.ExceededSubtasks()
	if len(exceeded) != 2 {
		t.Errorf("应返回 2 个超限子任务，got=%d", len(exceeded))
	}
}

// TestBudgetTrackerExt_TokensExceededOnly token 超限 → tokens_exceeded。
func TestBudgetTrackerExt_TokensExceededOnly(t *testing.T) {
	b := newTestBudgetTrackerExt(10000, nil, nil, nil, "")
	budgetHint := BudgetHint{TokensCap: 1000}
	// actual tokens=2000 > 1000 → 超限
	result, err := b.RecordActual(context.Background(), "agent-1", 2000, budgetHint)
	if err != nil {
		t.Fatalf("RecordActual: %v", err)
	}
	if !result.BudgetExceeded {
		t.Error("BudgetExceeded 应=true")
	}
	if result.ExceedReason != ExceedReasonTokens {
		t.Errorf("ExceedReason 应=tokens_exceeded，got=%s", result.ExceedReason)
	}
}

// TestBudgetTrackerExt_NoExceed_NotExceeded 未超限 → BudgetExceeded=false。
func TestBudgetTrackerExt_NoExceed_NotExceeded(t *testing.T) {
	b := newTestBudgetTrackerExt(10000, nil, nil, nil, "")
	budgetHint := BudgetHint{TokensCap: 5000}
	// actual tokens=1000 < 5000 → 未超
	result, err := b.RecordActual(context.Background(), "agent-1", 1000, budgetHint)
	if err != nil {
		t.Fatalf("RecordActual: %v", err)
	}
	if result.BudgetExceeded {
		t.Error("BudgetExceeded 应=false（未超限）")
	}
	if result.ExceedReason != "" {
		t.Errorf("ExceedReason 应空，got=%s", result.ExceedReason)
	}
}

// TestBudgetTrackerExt_ZeroBudgetHint_NoExceed BudgetHint 为 0（无硬上限）→ 不超限。
func TestBudgetTrackerExt_ZeroBudgetHint_NoExceed(t *testing.T) {
	b := newTestBudgetTrackerExt(10000, nil, nil, nil, "")
	budgetHint := BudgetHint{TokensCap: 0} // 无硬上限
	result, err := b.RecordActual(context.Background(), "agent-1", 999999, budgetHint)
	if err != nil {
		t.Fatalf("RecordActual: %v", err)
	}
	if result.BudgetExceeded {
		t.Error("BudgetHint=0 无硬上限，不应超限")
	}
}

// TestBudgetTrackerExt_StaticTokensByModel 静态矩阵 3 档 **token** 上限。
// ★ ：原 staticCostByModel（美分）删除，改 token。
func TestBudgetTrackerExt_StaticTokensByModel(t *testing.T) {
	b := newTestBudgetTrackerExt(10000, nil, nil, nil, "")
	cases := []struct {
		model    string
		expected int64
	}{
		{"claude-haiku", 30000},
		{"claude-sonnet", 100000},
		{"claude-opus", 200000},
		{"unknown", 50000},
	}
	for _, c := range cases {
		tokens := b.staticTokensByModel(c.model)
		if tokens != c.expected {
			t.Errorf("model=%s 应=%d，got=%d", c.model, c.expected, tokens)
		}
	}
}

// TestBudgetTrackerExt_UsedTokensAccumulate UsedTokens 正确累加。
func TestBudgetTrackerExt_UsedTokensAccumulate(t *testing.T) {
	b := newTestBudgetTrackerExt(100000, nil, nil, nil, "")
	budgetHint := BudgetHint{TokensCap: 100000}

	_, _ = b.RecordActual(context.Background(), "agent-1", 1000, budgetHint)
	_, _ = b.RecordActual(context.Background(), "agent-2", 2000, budgetHint)

	if b.UsedTokens() != 3000 {
		t.Errorf("UsedTokens 应=3000，got=%d", b.UsedTokens())
	}
}
