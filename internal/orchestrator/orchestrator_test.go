// orchestrator_test.go — H10 Phase 4 模块 7 ExperienceOrchestrator 反例测试（TDD 先行，Phase 4 §4.2）。
// 覆盖致命反例 2：Rerank 失败时降级为三因子路由，不阻塞防死循环（零容忍）。
// 覆盖：Dispatch 经验注入/不注入/降级/委托 / OnSubtaskDone 生命周期更新/pitfall 沉淀/quality.jsonl pattern_id。
// 集成点；落地：Dispatch/OnSubtaskDone 包装 + 委托 ParallelScheduler
// 不可用降级；落地：rerank 失败时不注入 + 走三因子（防死循环，致命反例 2）
// SNAPSHOT: tests={Dispatch_ExperienceHit_Injected, Dispatch_Unverified_NotInjected, Dispatch_RerankFail_DegradeToThreeFactor(致命反例2), Dispatch_DelegatesScheduler, OnSubtaskDone_LifecycleUpdated, OnSubtaskDone_PitfallRecordedOnFailure, OnSubtaskDone_QualityJsonlPatternIDWritten, SchedulerSignature_NotChanged}

package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestOrchestrator 创建测试用 ExperienceOrchestrator（含全部依赖，无 QualityStore）。
// scheduler 无 agent 注入，Dispatch 会返回 error 但经验注入/模型选择已完成（可验证副作用）。
func newTestOrchestrator(t *testing.T) *ExperienceOrchestrator {
	t.Helper()
	return newTestOrchestratorInternal(t, false)
}

// newTestOrchestratorWithQuality 创建带 QualityStore 的测试用 ExperienceOrchestrator。
func newTestOrchestratorWithQuality(t *testing.T) (*ExperienceOrchestrator, *QualityStore) {
	t.Helper()
	o := newTestOrchestratorInternal(t, true)
	return o, o.qualityStore
}

// newTestOrchestratorInternal 内部构造器（可选 QualityStore）。
func newTestOrchestratorInternal(t *testing.T, withQuality bool) *ExperienceOrchestrator {
	t.Helper()
	tempDir := t.TempDir()

	graph := NewDepGraph()
	pool := NewWorktreePool(tempDir, 3)
	budget := NewBudgetTracker(100000) // 大预算，不触发 exceeded
	tracer, err := NewTraceStore(filepath.Join(tempDir, "trace.jsonl"))
	if err != nil {
		t.Fatalf("NewTraceStore: %v", err)
	}
	state := NewStateMachine("test-task")
	scheduler := NewParallelScheduler(graph, pool, budget, tracer, state, 3)

	router := makeTestRouter(BudgetModeBalanced)
	expStore := newTestExperienceStore(t)

	var qs *QualityStore
	if withQuality {
		qs, err = NewQualityStore(filepath.Join(tempDir, "quality.jsonl"))
		if err != nil {
			t.Fatalf("NewQualityStore: %v", err)
		}
	}

	return NewExperienceOrchestrator(scheduler, state, router, expStore, qs)
}

// addTestNodeToGraph 将测试子任务节点加入 DepGraph（OnSubtaskDone 需要 MarkDone）。
func addTestNodeToGraph(t *testing.T, o *ExperienceOrchestrator, subtaskID string) {
	t.Helper()
	node := &TaskNode{
		ID:     subtaskID,
		Bucket: BucketA,
		Status: StatusPending,
	}
	if err := o.scheduler.graph.AddNode(node); err != nil {
		t.Fatalf("AddNode %s: %v", subtaskID, err)
	}
}

// TestOrchestrator_Dispatch_ExperienceHit_Injected verified pattern 命中 → 注入 ExperienceHit 到 TaskContext。
func TestOrchestrator_Dispatch_ExperienceHit_Injected(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	// 保存 verified pattern（TaskSignature 含 "bug-fix" 匹配 task_type，tokens 与 Goal 重叠）
	p := makeTestPattern("p-inject-hit", "bug-fix auth module", ScopeGlobal, VerifiedStateVerified)
	if err := o.expStore.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("S-inject-hit", "fix bug in auth module")
	if err := o.RegisterSubtask("S-inject-hit", tc); err != nil {
		t.Fatalf("RegisterSubtask: %v", err)
	}

	// Dispatch 会失败（scheduler 无 agent），但经验注入在委托前完成
	_ = o.Dispatch(context.Background(), "S-inject-hit")

	// 验证 ExperienceHit 已注入
	if tc.ExperienceHit == nil {
		t.Fatal("verified pattern 命中后应注入 ExperienceHit，got nil")
	}
	if tc.ExperienceHit.PatternID != "p-inject-hit" {
		t.Errorf("ExperienceHit.PatternID 期望 p-inject-hit，got %s", tc.ExperienceHit.PatternID)
	}
	if tc.ExperienceHit.VerifiedState != VerifiedStateVerified {
		t.Errorf("ExperienceHit.VerifiedState 期望 verified，got %s", tc.ExperienceHit.VerifiedState)
	}
	// PriorSolution / PriorFiles 也应注入
	if len(tc.PriorSolution) == 0 {
		t.Error("PriorSolution 应已注入（非空 Steps）")
	}
	if len(tc.PriorFiles) == 0 {
		t.Error("PriorFiles 应已注入（非空 FilesTouched）")
	}
}

// TestOrchestrator_Dispatch_Unverified_NotInjected unverified pattern 命中但不注入（致命反例 1 关联）。
func TestOrchestrator_Dispatch_Unverified_NotInjected(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	p := makeTestPattern("p-unverified", "bug-fix parser crash", ScopeGlobal, VerifiedStateUnverified)
	if err := o.expStore.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("S-unverified", "fix bug in parser crash")
	if err := o.RegisterSubtask("S-unverified", tc); err != nil {
		t.Fatalf("RegisterSubtask: %v", err)
	}

	_ = o.Dispatch(context.Background(), "S-unverified")

	// unverified 不应注入
	if tc.ExperienceHit != nil {
		t.Fatalf("unverified pattern 不应注入 ExperienceHit，got %+v（致命反例 1 关联）", tc.ExperienceHit)
	}
	if len(tc.PriorSolution) != 0 {
		t.Error("unverified pattern 不应注入 PriorSolution")
	}
}

// TestOrchestrator_Dispatch_RerankFail_DegradeToThreeFactor 致命反例 2（零容忍）：
// 经验库 Match 失败（index=nil 模拟 rerank/系统故障）→ 不注入 + 走三因子路由 + 不阻塞防死循环。
func TestOrchestrator_Dispatch_RerankFail_DegradeToThreeFactor(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	// 保存 verified pattern（若 index 正常会被命中）
	p := makeTestPattern("p-rerank-fail", "bug-fix memory leak", ScopeGlobal, VerifiedStateVerified)
	if err := o.expStore.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 模拟 rerank/系统故障：将 index 设为 nil → Match 返回 error "inverted index not set"
	o.expStore.SetIndex(nil)

	tc := makeTestTaskContext("S-rerank-fail", "fix bug in memory leak")
	if err := o.RegisterSubtask("S-rerank-fail", tc); err != nil {
		t.Fatalf("RegisterSubtask: %v", err)
	}

	// Dispatch 应返回（不阻塞防死循环），即使 Match 失败
	err := o.Dispatch(context.Background(), "S-rerank-fail")
	// Dispatch 可能返回 scheduler error（agent not set），但不应阻塞/panic
	_ = err

	// 致命反例 2 断言 1：ExperienceHit 未注入（降级为三因子）
	if tc.ExperienceHit != nil {
		t.Fatalf("致命反例 2：Match 失败时不应注入 ExperienceHit，got %+v（应降级为三因子路由）", tc.ExperienceHit)
	}

	// 致命反例 2 断言 2：Model 仍被设置（三因子路由正常工作）
	if tc.Model == "" {
		t.Fatal("致命反例 2：Match 失败时三因子路由应仍设置 Model，got 空字符串（降级不应阻塞路由）")
	}
	// bug-fix/T1 基线 = sonnet（balanced 模式无降级/升级，冷启动）
	if tc.Model != ModelSonnet {
		t.Errorf("致命反例 2：三因子路由应返回基线 sonnet（bug-fix/T1/balanced/冷启动），got %s", tc.Model)
	}
}

// TestOrchestrator_Dispatch_DelegatesScheduler 验证 Dispatch 委托 ParallelScheduler.Dispatch（不改签名）。
func TestOrchestrator_Dispatch_DelegatesScheduler(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	tc := makeTestTaskContext("S-delegate", "fix bug in utils")
	if err := o.RegisterSubtask("S-delegate", tc); err != nil {
		t.Fatalf("RegisterSubtask: %v", err)
	}

	// scheduler 无 agent → Dispatch 应返回 "agent not set" error（证明委托生效）
	err := o.Dispatch(context.Background(), "S-delegate")
	if err == nil {
		t.Fatal("Dispatch 应委托 scheduler.Dispatch 并返回 error（scheduler 无 agent）")
	}
}

// TestOrchestrator_OnSubtaskDone_LifecycleUpdated ExperienceHit + L0Pass + L2=adopted → 生命周期状态升级。
func TestOrchestrator_OnSubtaskDone_LifecycleUpdated(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	// 保存 unverified pattern
	p := makeTestPattern("p-lifecycle", "bug-fix race condition", ScopeGlobal, VerifiedStateUnverified)
	if err := o.expStore.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	addTestNodeToGraph(t, o, "S-lifecycle")

	result := TaskResult{
		Success: true,
		Output: &ImplementOutput{
			L0Pass:  true,
			L1Score: 70,
		},
		L2Verdict:       L2Adopted,
		OrchestrationID: "orch-session-lifecycle",
		ExperienceHit: &ExperienceHit{
			PatternID:     "p-lifecycle",
			Confidence:    0.7,
			VerifiedState: VerifiedStateUnverified,
		},
	}

	if err := o.OnSubtaskDone(context.Background(), "S-lifecycle", result); err != nil {
		t.Fatalf("OnSubtaskDone: %v", err)
	}

	// 验证生命周期更新：unverified + L0Pass + L2=adopted + L1Score=70 → provisionally_verified
	got, err := o.expStore.Get("p-lifecycle")
	if err != nil {
		t.Fatalf("Get pattern: %v", err)
	}
	if got.VerifiedState != VerifiedStateProvisionallyVerified {
		t.Errorf("生命周期更新后 VerifiedState 期望 provisionally_verified，got %s", got.VerifiedState)
	}
}

// TestOrchestrator_OnSubtaskDone_PitfallRecordedOnFailure L0Fail + FailureTrace → 沉淀 pitfall。
func TestOrchestrator_OnSubtaskDone_PitfallRecordedOnFailure(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	addTestNodeToGraph(t, o, "S-pitfall")

	tc := makeTestTaskContext("S-pitfall", "fix bug in network layer")
	result := TaskResult{
		Success: false,
		Output: &ImplementOutput{
			L0Pass:  false,
			L1Score: 30,
		},
		ErrorType:       "compile_error",
		OrchestrationID: "orch-session-pitfall",
		FailureTrace: &FailureTrace{
			ErrorType:    "compile_error",
			ErrorMessage: "undefined: foo",
			FailedStep:   "edit_file main.go",
			FilesTouched: []string{"main.go"},
		},
		TaskContext: tc,
	}

	if err := o.OnSubtaskDone(context.Background(), "S-pitfall", result); err != nil {
		t.Fatalf("OnSubtaskDone: %v", err)
	}

	// 验证 pitfall 已沉淀（RecordPitfall 默认写入 project scope）
	pitfalls, err := o.expStore.ListPitfallsByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListPitfallsByScope: %v", err)
	}
	if len(pitfalls) == 0 {
		t.Fatal("L0Fail + FailureTrace 应沉淀 pitfall，got 0 pitfalls")
	}
	// 验证 pitfall 内容
	found := false
	for _, pf := range pitfalls {
		if pf.TaskSignature == "fix bug in network layer" {
			found = true
			if pf.Occurrences < 1 {
				t.Errorf("pitfall Occurrences 期望 >= 1，got %d", pf.Occurrences)
			}
			if pf.Symptom != "undefined: foo" {
				t.Errorf("pitfall Symptom 期望 'undefined: foo'，got %s", pf.Symptom)
			}
			break
		}
	}
	if !found {
		t.Error("未找到匹配 task_signature 的 pitfall")
	}
}

// TestOrchestrator_OnSubtaskDone_QualityJsonlPatternIDWritten ExperienceHit → quality.jsonl 写入 pattern_id。
func TestOrchestrator_OnSubtaskDone_QualityJsonlPatternIDWritten(t *testing.T) {
	t.Parallel()
	o, qs := newTestOrchestratorWithQuality(t)

	addTestNodeToGraph(t, o, "S-quality")

	result := TaskResult{
		Success: true,
		Output: &ImplementOutput{
			L0Pass:  true,
			L1Score: 85,
		},
		L2Verdict:       L2Adopted,
		OrchestrationID: "orch-session-quality",
		ExperienceHit: &ExperienceHit{
			PatternID:     "p-quality-write",
			Confidence:    0.9,
			VerifiedState: VerifiedStateVerified,
		},
	}

	if err := o.OnSubtaskDone(context.Background(), "S-quality", result); err != nil {
		t.Fatalf("OnSubtaskDone: %v", err)
	}

	// 验证 quality.jsonl 写入 pattern_id
	rec, err := qs.QueryByTaskID("S-quality")
	if err != nil {
		t.Fatalf("QueryByTaskID: %v", err)
	}
	if rec.PatternID != "p-quality-write" {
		t.Errorf("quality.jsonl PatternID 期望 p-quality-write，got %s", rec.PatternID)
	}
	if rec.L2Verdict != L2Adopted {
		t.Errorf("quality.jsonl L2Verdict 期望 adopted，got %s", rec.L2Verdict)
	}
	if !rec.L0Pass {
		t.Error("quality.jsonl L0Pass 期望 true")
	}
	if rec.L1Score != 85 {
		t.Errorf("quality.jsonl L1Score 期望 85，got %d", rec.L1Score)
	}
}

// TestOrchestrator_SchedulerSignature_NotChanged 验证 ParallelScheduler.Dispatch/OnSubtaskDone 签名与 Phase 3 一致。
func TestOrchestrator_SchedulerSignature_NotChanged(t *testing.T) {
	t.Parallel()
	o := newTestOrchestrator(t)

	// 验证 scheduler 非空（ParallelScheduler 组合关系存在）
	if o.scheduler == nil {
		t.Fatal("ExperienceOrchestrator.scheduler 不应为 nil（委托关系必须存在）")
	}

	// 验证 Dispatch 签名：(ctx context.Context, subtaskID string) error
	ctx := context.Background()
	err := o.scheduler.Dispatch(ctx, "")
	if err == nil {
		t.Fatal("Dispatch(空 subtaskID) 应返回 error（签名契约：subtaskID must not be empty）")
	}

	// 验证 OnSubtaskDone 签名：(ctx context.Context, subtaskID string, result TaskResult) error
	err = o.scheduler.OnSubtaskDone(ctx, "S-sig-test", TaskResult{})
	// OnSubtaskDone 可能返回 error（MarkDone 失败等），但签名应匹配
	_ = err

	// 验证 ExperienceOrchestrator 也保持相同签名（包装层不改签名）
	err = o.Dispatch(ctx, "")
	if err == nil {
		t.Fatal("ExperienceOrchestrator.Dispatch(空 subtaskID) 应返回 error")
	}
	err = o.OnSubtaskDone(ctx, "S-sig-test", TaskResult{})
	_ = err
}
