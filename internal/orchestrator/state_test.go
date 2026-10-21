// state_test.go — 模块 1 状态机反例测试（TDD 先行，编排层 §2.1-§2.5）。
// 覆盖致命反例：done→implement 拒绝 / 路径穿越拒绝 / 断点续传状态保留 / 并发安全。
// ；落地：SaveState/LoadState + Restore 测试
// ≤3；落地：SubTaskState.Depth 字段
// SNAPSHOT: tests={DoneToImplement_Rejected, IllegalCrossStage_Rejected, InterruptedToAny_Allowed, AdvanceGuardFailed_RetryOnce, Restore_StatePreserved, SaveLoadState_RoundTrip, SaveState_PathTraversal_Rejected, ConcurrentAdvance_MutexProtected}

package orchestrator

import (
	"sync"
	"testing"
)

// TestStateMachine_DoneToImplement_Rejected 致命反例：done→implement 必须拒绝（编排层 §2.2，零容忍）。
// 验证目标：CanTransition(done, implement) 返回 false，从 done 调 Advance 返回 error。
func TestStateMachine_DoneToImplement_Rejected(t *testing.T) {
	sm := NewStateMachine("test-done-to-implement")

	// 直接校验 CanTransition
	if sm.CanTransition(StageDone, StageImplement) {
		t.Fatal("致命反例：CanTransition(done, implement) 必须返回 false，done 是终态不允许任何转移")
	}

	// 校验从 done 调 Advance 返回 error
	sm.current = StageDone
	if err := sm.Advance(); err == nil {
		t.Fatal("致命反例：从 done 调 Advance 必须返回 error（终态不允许推进）")
	}
}

// TestStateMachine_IllegalCrossStage_Rejected 非法跨 Stage 跳跃必须拒绝（编排层 §2.2）。
// 验证目标：clarify→done 跨 Stage 跳跃拒绝。
func TestStateMachine_IllegalCrossStage_Rejected(t *testing.T) {
	sm := NewStateMachine("test-cross-stage")

	if sm.CanTransition(StageClarify, StageDone) {
		t.Fatal("clarify→done 跨 Stage 跳跃必须拒绝（必须经过 plan/contract/implement/review/test）")
	}
	if sm.CanTransition(StageContract, StageClarify) {
		t.Fatal("contract→clarify 跨 Stage 回退必须拒绝（仅允许 contract→plan 回退）")
	}
}

// TestStateMachine_InterruptedToAny_Allowed interrupted 状态可恢复到任意 Stage（用户选恢复点，编排层 §2.2）。
func TestStateMachine_InterruptedToAny_Allowed(t *testing.T) {
	sm := NewStateMachine("test-interrupted")

	// interrupted → 任意 Stage 都应允许（除了 done）
	stages := []string{StageInit, StageClarify, StagePlan, StageContract, StageImplement, StageReview, StageTest}
	for _, to := range stages {
		if !sm.CanTransition(StageInterrupted, to) {
			t.Errorf("interrupted→%s 必须允许（用户选恢复点）", to)
		}
	}

	// interrupted→done 必须拒绝（不能从 interrupted 直接到 done）
	if sm.CanTransition(StageInterrupted, StageDone) {
		t.Fatal("interrupted→done 必须拒绝（需经过 test Stage）")
	}
}

// TestStateMachine_AdvanceGuardFailed_RetryOnce advance_guard 校验失败按 OnFail=retry_once 处理（1 轮硬上限）。
func TestStateMachine_AdvanceGuardFailed_RetryOnce(t *testing.T) {
	sm := NewStateMachine("test-guard-fail")
	sm.RegisterGuard("checkClarifyOutput", func(state State) error {
		return errGuardFailedForTest
	})

	// 推进到 clarify
	if err := sm.Advance(); err != nil {
		t.Fatalf("第一次 Advance (init→clarify) 失败: %v", err)
	}
	if sm.Current() != StageClarify {
		t.Fatalf("expected clarify, got %s", sm.Current())
	}

	// 第一次 guard 失败 → retry_once（留在当前 Stage）
	err := sm.Advance()
	if err == nil {
		t.Fatal("advance_guard 校验失败时必须返回 error")
	}
	if sm.Current() != StageClarify {
		t.Fatalf("retry_once 应留在当前 Stage，got %s", sm.Current())
	}

	// 第二次 guard 仍失败 → interrupt（1 轮硬上限）
	err = sm.Advance()
	if err == nil {
		t.Fatal("第二次 guard 失败必须返回 error")
	}
	if sm.Current() != StageInterrupted {
		t.Fatalf("1 轮硬上限后应 interrupt，got %s", sm.Current())
	}
}

// TestStateMachine_Restore_StatePreserved Checkpoint 后 Restore 验证状态一致（防断点续传状态丢失）。
func TestStateMachine_Restore_StatePreserved(t *testing.T) {
	sm := NewStateMachine("test-restore")
	sm.RegisterGuard("checkClarifyOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkPlanOutput", func(state State) error { return nil })

	// 推进到 plan
	if err := sm.Advance(); err != nil { // init→clarify
		t.Fatalf("Advance init→clarify: %v", err)
	}
	if err := sm.Advance(); err != nil { // clarify→plan
		t.Fatalf("Advance clarify→plan: %v", err)
	}

	// Checkpoint → Restore
	snap := sm.Checkpoint()
	if snap.TaskID != "test-restore" {
		t.Errorf("Checkpoint TaskID mismatch: got %s", snap.TaskID)
	}
	if snap.Stage != StagePlan {
		t.Errorf("Checkpoint Stage mismatch: got %s, want %s", snap.Stage, StagePlan)
	}

	// 新建状态机，Restore 后验证
	sm2 := NewStateMachine("test-restore-2")
	if err := sm2.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if sm2.Current() != StagePlan {
		t.Errorf("Restore 后 Stage 不一致: got %s, want %s", sm2.Current(), StagePlan)
	}
	if sm2.TaskID() != "test-restore" {
		t.Errorf("Restore 后 TaskID 不一致: got %s, want test-restore", sm2.TaskID())
	}
}

// TestSaveLoadState_RoundTrip SaveState 后 LoadState 验证 State 完全一致（编排层 §2.4）。
func TestSaveLoadState_RoundTrip(t *testing.T) {
	baseDir := t.TempDir()

	original := State{
		TaskID: "T-roundtrip-001",
		Stage:  StageImplement,
		SubTasks: []SubTaskState{
			{
				SubTaskID:  "S-001",
				Stage:      StageImplement,
				Status:     SubTaskStatusCompleted,
				Depth:      1,
				AgentID:    "agent-abc",
				StartedAt:  1721019845,
				FinishedAt: 1721019900,
			},
		},
		UpdatedAt: 1721019900,
	}

	if err := SaveState(baseDir, "T-roundtrip-001", original); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := LoadState(baseDir, "T-roundtrip-001")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil")
	}
	if loaded.TaskID != original.TaskID {
		t.Errorf("TaskID mismatch: got %s, want %s", loaded.TaskID, original.TaskID)
	}
	if loaded.Stage != original.Stage {
		t.Errorf("Stage mismatch: got %s, want %s", loaded.Stage, original.Stage)
	}
	if loaded.UpdatedAt != original.UpdatedAt {
		t.Errorf("UpdatedAt mismatch: got %d, want %d", loaded.UpdatedAt, original.UpdatedAt)
	}
	if len(loaded.SubTasks) != 1 {
		t.Fatalf("SubTasks length mismatch: got %d, want 1", len(loaded.SubTasks))
	}
	if loaded.SubTasks[0].SubTaskID != "S-001" {
		t.Errorf("SubTaskID mismatch: got %s", loaded.SubTasks[0].SubTaskID)
	}
	if loaded.SubTasks[0].Depth != 1 {
		t.Errorf("Depth mismatch: got %d, want 1", loaded.SubTasks[0].Depth)
	}
}

// TestSaveState_PathTraversal_Rejected 致命反例：taskID 含 ".." 或 "/" 时拒绝（路径穿越防护，编排层 §2.4 安全要求）。
func TestSaveState_PathTraversal_Rejected(t *testing.T) {
	baseDir := t.TempDir()
	state := State{TaskID: "bad", Stage: StageClarify, UpdatedAt: 1}

	// 含 ".." 的 taskID
	if err := SaveState(baseDir, "../escape", state); err == nil {
		t.Fatal("致命反例：taskID 含 '..' 时 SaveState 必须返回 error（路径穿越防护）")
	}

	// 含 "/" 的 taskID
	if err := SaveState(baseDir, "a/b", state); err == nil {
		t.Fatal("致命反例：taskID 含 '/' 时 SaveState 必须返回 error（路径穿越防护）")
	}

	// 含 "\" 的 taskID（Windows 路径分隔符）
	if err := SaveState(baseDir, "a\\b", state); err == nil {
		t.Fatal("致命反例：taskID 含 '\\' 时 SaveState 必须返回 error（路径穿越防护）")
	}

	// 空 taskID
	if err := SaveState(baseDir, "", state); err == nil {
		t.Fatal("致命反例：空 taskID 时 SaveState 必须返回 error")
	}

	// LoadState 同样校验
	if _, err := LoadState(baseDir, "../escape"); err == nil {
		t.Fatal("LoadState taskID 含 '..' 时必须返回 error")
	}
}

// TestStateMachine_ConcurrentAdvance_MutexProtected 两个 goroutine 同时 Advance，sync.Mutex 防竞态（编排层 §2.3）。
func TestStateMachine_ConcurrentAdvance_MutexProtected(t *testing.T) {
	sm := NewStateMachine("test-concurrent")
	sm.RegisterGuard("checkClarifyOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkPlanOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkContractOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkImplementOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkReviewOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkTestOutput", func(state State) error { return nil })

	var wg sync.WaitGroup
	wg.Add(2)

	// 两个 goroutine 同时 Advance
	go func() {
		defer wg.Done()
		_ = sm.Advance()
	}()
	go func() {
		defer wg.Done()
		_ = sm.Advance()
	}()

	wg.Wait()

	// 验证最终 Stage 是合法的（clarify 或之后，不可能是 init）
	cur := sm.Current()
	if cur == StageInit {
		t.Fatal("并发 Advance 后 Stage 仍是 init，状态机未推进")
	}
}

// TestStateMachine_FullFlow_FromInitToDone 完整流程 init→clarify→plan→contract→implement→review→test→done（编排层 §2.2）。
func TestStateMachine_FullFlow_FromInitToDone(t *testing.T) {
	sm := NewStateMachine("test-full-flow")
	// 注册所有 guard 为通过
	sm.RegisterGuard("checkClarifyOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkPlanOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkContractOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkImplementOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkReviewOutput", func(state State) error { return nil })
	sm.RegisterGuard("checkTestOutput", func(state State) error { return nil })

	stages := []string{StageClarify, StagePlan, StageContract, StageImplement, StageReview, StageTest, StageDone}
	for _, expected := range stages {
		if err := sm.Advance(); err != nil {
			t.Fatalf("Advance to %s: %v", expected, err)
		}
		if sm.Current() != expected {
			t.Errorf("expected %s, got %s", expected, sm.Current())
		}
	}
}

// errGuardFailedForTest 测试用 guard 失败错误。
var errGuardFailedForTest = &guardError{msg: "guard failed for test"}

type guardError struct{ msg string }

func (e *guardError) Error() string { return e.msg }
