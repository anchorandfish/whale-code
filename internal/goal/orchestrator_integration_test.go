// orchestrator_integration_test.go — M25-02 goal 与 H10 编排器集成测试。
//
// 覆盖 GoalOrchestratorBridge 的去重、用量累加、自动评估、降级策略。

package goal

import (
	"context"
	"testing"
)

// TestNewGoalOrchestratorBridge_NilManager 验证 nil manager 返回 nil bridge。
func TestNewGoalOrchestratorBridge_NilManager(t *testing.T) {
	b := NewGoalOrchestratorBridge(nil)
	if b != nil {
		t.Errorf("NewGoalOrchestratorBridge(nil) = %v, want nil", b)
	}
}

// TestNewGoalOrchestratorBridge_ValidManager 验证有效 manager 返回非 nil bridge。
func TestNewGoalOrchestratorBridge_ValidManager(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)
	if b == nil {
		t.Fatal("NewGoalOrchestratorBridge(valid) returned nil")
	}
	if b.GetSubtaskCount() != 0 {
		t.Errorf("initial GetSubtaskCount() = %d, want 0", b.GetSubtaskCount())
	}
}

// TestOnSubtaskCompleted_EmptyThreadID 验证空 ThreadID 被跳过。
func TestOnSubtaskCompleted_EmptyThreadID(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:  "",
		SubtaskID: "sub-1",
		Success:   true,
	})
	if b.GetSubtaskCount() != 0 {
		t.Errorf("GetSubtaskCount() after empty ThreadID = %d, want 0", b.GetSubtaskCount())
	}
}

// TestOnSubtaskCompleted_EmptySubtaskID 验证空 SubtaskID 被跳过。
func TestOnSubtaskCompleted_EmptySubtaskID(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:  "thread-1",
		SubtaskID: "",
		Success:   true,
	})
	if b.GetSubtaskCount() != 0 {
		t.Errorf("GetSubtaskCount() after empty SubtaskID = %d, want 0", b.GetSubtaskCount())
	}
}

// TestOnSubtaskCompleted_Dedup 验证同一 subtaskID 不重复处理。
func TestOnSubtaskCompleted_Dedup(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	// 预存 active goal
	_ = store.Save("thread-1", newTestActiveGoal("thread-1"))

	event := SubtaskCompletionEvent{
		ThreadID:    "thread-1",
		SubtaskID:   "sub-1",
		Success:     true,
		TokensUsed:  100,
		DurationSec: 10,
	}

	// 第一次调用
	b.OnSubtaskCompleted(context.Background(), event)
	if b.GetSubtaskCount() != 1 {
		t.Errorf("GetSubtaskCount() after first call = %d, want 1", b.GetSubtaskCount())
	}

	// 第二次调用同一 subtaskID（应被去重）
	b.OnSubtaskCompleted(context.Background(), event)
	if b.GetSubtaskCount() != 1 {
		t.Errorf("GetSubtaskCount() after dedup call = %d, want 1 (dedup)", b.GetSubtaskCount())
	}

	if !b.IsEvaluated("sub-1") {
		t.Errorf("IsEvaluated(sub-1) = false, want true")
	}
}

// TestOnSubtaskCompleted_GoalProgressAccumulation 验证 goal 用量累加正确。
func TestOnSubtaskCompleted_GoalProgressAccumulation(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	// 预存 active goal
	originalGoal := newTestActiveGoal("thread-1")
	originalGoal.TokensUsed = 1000
	originalGoal.TimeUsedSec = 100
	_ = store.Save("thread-1", originalGoal)

	// 第一个子任务
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:    "thread-1",
		SubtaskID:   "sub-1",
		Success:     true,
		TokensUsed:  200,
		DurationSec: 30,
	})

	// 第二个子任务
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:    "thread-1",
		SubtaskID:   "sub-2",
		Success:     true,
		TokensUsed:  300,
		DurationSec: 40,
	})

	// 验证累加结果
	g, _ := store.Load("thread-1")
	if g.TokensUsed != 1500 { // 1000 + 200 + 300
		t.Errorf("TokensUsed = %d, want 1500", g.TokensUsed)
	}
	if g.TimeUsedSec != 170 { // 100 + 30 + 40
		t.Errorf("TimeUsedSec = %d, want 170", g.TimeUsedSec)
	}
}

// TestOnSubtaskCompleted_NoGoalNoop 验证无 goal 时 no-op（不报错）。
func TestOnSubtaskCompleted_NoGoalNoop(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	// 无 goal，应 no-op 不报错
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:   "no-such-thread",
		SubtaskID:  "sub-1",
		Success:    true,
		TokensUsed: 100,
	})

	if b.GetSubtaskCount() != 1 {
		t.Errorf("GetSubtaskCount() = %d, want 1 (count incremented even without goal)", b.GetSubtaskCount())
	}
}

// TestOnSubtaskCompleted_NonActiveGoalNoop 验证非 active goal 时用量不累加。
func TestOnSubtaskCompleted_NonActiveGoalNoop(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	// 预存 paused goal
	pausedGoal := newTestActiveGoal("thread-1")
	pausedGoal.Status = StatusPaused
	pausedGoal.TokensUsed = 1000
	_ = store.Save("thread-1", pausedGoal)

	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:   "thread-1",
		SubtaskID:  "sub-1",
		Success:    true,
		TokensUsed: 200,
	})

	// 验证 paused goal 用量未变
	g, _ := store.Load("thread-1")
	if g.TokensUsed != 1000 {
		t.Errorf("TokensUsed for paused goal = %d, want 1000 (should not accumulate)", g.TokensUsed)
	}
}

// TestSetAutoEval_EnableAutoEval 验证开启自动评估后达到 interval 触发评估。
func TestSetAutoEval_EnableAutoEval(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	// 预存 active goal
	_ = store.Save("thread-1", newTestActiveGoal("thread-1"))

	// 开启自动评估，interval=2（每 2 个子任务评估一次）
	b.SetAutoEval(true, 2)

	// 第 1 个子任务（count=1，1%2≠0，不评估）
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:  "thread-1",
		SubtaskID: "sub-1",
		Success:   true,
	})

	// 第 2 个子任务（count=2，2%2=0，触发评估）
	// 注：evalAgent=nil → runEvaluation 返回降级（不 panic）
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:  "thread-1",
		SubtaskID: "sub-2",
		Success:   true,
	})

	// 验证两个子任务都已处理
	if b.GetSubtaskCount() != 2 {
		t.Errorf("GetSubtaskCount() = %d, want 2", b.GetSubtaskCount())
	}
}

// TestSetAutoEval_DisableAutoEval 验证关闭自动评估后不触发评估。
func TestSetAutoEval_DisableAutoEval(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	_ = store.Save("thread-1", newTestActiveGoal("thread-1"))

	// 确保默认关闭
	b.SetAutoEval(false, 0)

	for i := 0; i < 10; i++ {
		b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
			ThreadID:  "thread-1",
			SubtaskID: "sub-" + string(rune('0'+i)),
			Success:   true,
		})
	}

	// 验证不触发评估不 panic（evalAgent=nil 降级路径）
	if b.GetSubtaskCount() != 10 {
		t.Errorf("GetSubtaskCount() = %d, want 10", b.GetSubtaskCount())
	}
}

// TestReset 验证 Reset 清空状态。
func TestReset(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	_ = store.Save("thread-1", newTestActiveGoal("thread-1"))

	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:  "thread-1",
		SubtaskID: "sub-1",
		Success:   true,
	})

	if b.GetSubtaskCount() != 1 {
		t.Fatalf("GetSubtaskCount() before reset = %d, want 1", b.GetSubtaskCount())
	}

	b.Reset()

	if b.GetSubtaskCount() != 0 {
		t.Errorf("GetSubtaskCount() after reset = %d, want 0", b.GetSubtaskCount())
	}
	if b.IsEvaluated("sub-1") {
		t.Errorf("IsEvaluated(sub-1) after reset = true, want false")
	}
}

// TestOnSubtaskCompleted_SubtaskFailureNotBlocking 验证子任务失败不阻塞（致命反例 1）。
func TestOnSubtaskCompleted_SubtaskFailureNotBlocking(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	_ = store.Save("thread-1", newTestActiveGoal("thread-1"))

	// 子任务失败
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:   "thread-1",
		SubtaskID:  "sub-failed",
		Success:    false,
		TokensUsed: 50,
	})

	// 验证失败子任务仍被计数（不阻塞）
	if b.GetSubtaskCount() != 1 {
		t.Errorf("GetSubtaskCount() after failed subtask = %d, want 1 (failure not blocking)", b.GetSubtaskCount())
	}

	// 验证 goal 状态仍为 active（未转 blocked）
	g, _ := store.Load("thread-1")
	if g.Status != StatusActive {
		t.Errorf("goal Status after subtask failure = %v, want %v (should remain active)", g.Status, StatusActive)
	}
}

// TestOnSubtaskCompleted_MultipleThreads 验证多 thread 独立累加。
func TestOnSubtaskCompleted_MultipleThreads(t *testing.T) {
	store := newMockStore()
	m := newTestManager(store, nil)
	b := NewGoalOrchestratorBridge(m)

	_ = store.Save("thread-1", newTestActiveGoal("thread-1"))
	_ = store.Save("thread-2", newTestActiveGoal("thread-2"))

	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:   "thread-1",
		SubtaskID:  "sub-1",
		TokensUsed: 100,
	})
	b.OnSubtaskCompleted(context.Background(), SubtaskCompletionEvent{
		ThreadID:   "thread-2",
		SubtaskID:  "sub-2",
		TokensUsed: 200,
	})

	g1, _ := store.Load("thread-1")
	g2, _ := store.Load("thread-2")

	if g1.TokensUsed != 100 {
		t.Errorf("thread-1 TokensUsed = %d, want 100", g1.TokensUsed)
	}
	if g2.TokensUsed != 200 {
		t.Errorf("thread-2 TokensUsed = %d, want 200", g2.TokensUsed)
	}
}
