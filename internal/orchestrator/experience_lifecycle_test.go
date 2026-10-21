// experience_lifecycle_test.go — H10 Phase 4 模块 4 InjectContext + 四级生命周期状态机反例测试（TDD 先行，Phase 4 §3.2.4/§3.3）。
// 覆盖致命反例 1：unverified 不升级（L1Score<60 防同质化偏差误升）+ 同会话不升 verified（防误判）。
// 覆盖致命反例 5：deprecated pattern 不注入（防过时 pattern 误用）。
// ；落地：unverified→provisionally_verified→verified→deprecated
// 升级条件加严 L1Score>=60；落地：防同质化偏差（同源）
// SNAPSHOT: tests={UpdateLifecycle_UnverifiedToProvisionallyVerified, UpdateLifecycle_UnverifiedNoUpgrade_L1ScoreLow(致命反例1), UpdateLifecycle_UnverifiedToDeprecated_L0Fail, UpdateLifecycle_ProvisionallyToVerified_CrossSession, UpdateLifecycle_ProvisionallyToVerified_SameSession_NoUpgrade, UpdateLifecycle_VerifiedToDeprecated_LowRateAndOld, UpdateLifecycle_ProvisionallyToUnverified_L2Discarded, InjectContext_Unverified_NotInjected(致命反例1), InjectContext_Deprecated_NotInjected(致命反例5), InjectContext_Verified_Injected, InjectContext_NoModelOverride, InjectContext_NilTask_Rejected, InjectContext_NilHit_Rejected, RecordPitfall_NewPitfall, RecordPitfall_ExistingPitfall_IncrementOccurrences, RecordPitfall_NilTask_Rejected, RecordPitfall_EmptyGoal_Rejected}

package orchestrator

import (
	"strings"
	"testing"
	"time"
)

// makeTestTaskContext 构造测试用 TaskContext（含必填字段）。
func makeTestTaskContext(taskID, goal string) *TaskContext {
	return &TaskContext{
		TaskID:             taskID,
		MaxDepth:           3,
		CapabilitySnapshot: map[string]bool{"sonnet": true, "haiku": true},
		BudgetHint:         BudgetHint{TokensCap: 200000},
		BudgetMode:         BudgetModeBalanced,
		Goal:               goal,
	}
}

// TestUpdateLifecycle_UnverifiedToProvisionallyVerified L0Pass + L2=adopted + L1Score=70 → 升 provisionally_verified。
func TestUpdateLifecycle_UnverifiedToProvisionallyVerified(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-upgrade-1", "fix upgrade bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	signal := TaskCompletionSignal{
		L0Pass:          true,
		L1Score:         70,
		L2Verdict:       L2Adopted,
		OrchestrationID: "orch-session-1",
	}
	if err := s.UpdateLifecycle("p-upgrade-1", signal); err != nil {
		t.Fatalf("UpdateLifecycle: %v", err)
	}

	got, err := s.Get("p-upgrade-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateProvisionallyVerified {
		t.Errorf("L0Pass+L2=adopted+L1Score=70 应升 provisionally_verified, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_UnverifiedNoUpgrade_L1ScoreLow 致命反例 1：L0Pass + L2=adopted + L1Score=50 → 不升级（v2 加严，防同质化偏差误升）。
func TestUpdateLifecycle_UnverifiedNoUpgrade_L1ScoreLow(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-no-upgrade-low", "fix no upgrade bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// L1Score=50 < 60 阈值，不应升级
	signal := TaskCompletionSignal{
		L0Pass:          true,
		L1Score:         50,
		L2Verdict:       L2Adopted,
		OrchestrationID: "orch-session-1",
	}
	if err := s.UpdateLifecycle("p-no-upgrade-low", signal); err != nil {
		t.Fatalf("UpdateLifecycle: %v", err)
	}

	got, err := s.Get("p-no-upgrade-low")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("致命反例 1：L1Score=50 < 60 不应升级, got %s（防同质化偏差误升）", got.VerifiedState)
	}
}

// TestUpdateLifecycle_UnverifiedNoUpgrade_L0Fail L0Fail + L2=adopted → 不升级（L0Pass 必须为 true）。
func TestUpdateLifecycle_UnverifiedNoUpgrade_L0Fail(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-no-upgrade-l0fail", "fix l0fail bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	signal := TaskCompletionSignal{
		L0Pass:          false,
		L1Score:         70,
		L2Verdict:       L2Adopted,
		OrchestrationID: "orch-session-1",
	}
	_ = s.UpdateLifecycle("p-no-upgrade-l0fail", signal)

	got, _ := s.Get("p-no-upgrade-l0fail")
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("L0Fail 不应升级, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_UnverifiedToDeprecated_L0Fail L0Fail + L2=discarded → 直接 deprecated。
func TestUpdateLifecycle_UnverifiedToDeprecated_L0Fail(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-deprecate-1", "fix deprecate bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	signal := TaskCompletionSignal{
		L0Pass:          false,
		L1Score:         0,
		L2Verdict:       L2Discarded,
		OrchestrationID: "orch-session-1",
	}
	if err := s.UpdateLifecycle("p-deprecate-1", signal); err != nil {
		t.Fatalf("UpdateLifecycle: %v", err)
	}

	got, err := s.Get("p-deprecate-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("L0Fail+L2=discarded 应直接 deprecated, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_ProvisionallyToVerified_CrossSession 致命反例 1 关联：3 次不同 orchestration_id 的 L2=adopted → 升 verified（防同会话误判）。
func TestUpdateLifecycle_ProvisionallyToVerified_CrossSession(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-cross-session", "fix cross session bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 第 1 次：unverified → provisionally_verified（orch-id-1）
	_ = s.UpdateLifecycle("p-cross-session", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-id-1",
	})

	// 第 2 次：provisionally_verified + L2=adopted（orch-id-2，不同会话）
	_ = s.UpdateLifecycle("p-cross-session", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-id-2",
	})

	// 第 3 次：provisionally_verified + L2=adopted（orch-id-3，不同会话）→ 应升 verified
	if err := s.UpdateLifecycle("p-cross-session", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-id-3",
	}); err != nil {
		t.Fatalf("UpdateLifecycle 3rd: %v", err)
	}

	got, err := s.Get("p-cross-session")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateVerified {
		t.Errorf("3 次不同 orchestration_id 的 L2=adopted 应升 verified, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_ProvisionallyToVerified_SameSession_NoUpgrade 致命反例 1 关联：3 次同 orchestration_id → 不升级（防同会话误判）。
func TestUpdateLifecycle_ProvisionallyToVerified_SameSession_NoUpgrade(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-same-session", "fix same session bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 第 1 次：unverified → provisionally_verified（同 orch-id）
	_ = s.UpdateLifecycle("p-same-session", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "same-orch-id",
	})

	// 第 2 次：provisionally_verified + L2=adopted（同 orch-id）
	_ = s.UpdateLifecycle("p-same-session", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "same-orch-id",
	})

	// 第 3 次：provisionally_verified + L2=adopted（同 orch-id）→ 不应升 verified（同会话）
	_ = s.UpdateLifecycle("p-same-session", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "same-orch-id",
	})

	got, err := s.Get("p-same-session")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState == VerifiedStateVerified {
		t.Errorf("致命反例 1：3 次同 orchestration_id 不应升 verified（防同会话误判）, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_ProvisionallyToUnverified_L2Discarded provisionally_verified + L2=discarded → 退回 unverified。
func TestUpdateLifecycle_ProvisionallyToUnverified_L2Discarded(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	// 先升到 provisionally_verified
	p := makeTestPattern("p-back-to-unverified", "fix back bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_ = s.UpdateLifecycle("p-back-to-unverified", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-1",
	})

	// L2=discarded → 退回 unverified
	_ = s.UpdateLifecycle("p-back-to-unverified", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Discarded, OrchestrationID: "orch-2",
	})

	got, err := s.Get("p-back-to-unverified")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("provisionally_verified + L2=discarded 应退回 unverified, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_VerifiedToDeprecated_LowRateAndOld verified + l2_rate<0.3 + 90天未用 → 降 deprecated。
func TestUpdateLifecycle_VerifiedToDeprecated_LowRateAndOld(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)

	// 注入固定时间（now = 2026-01-01）
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetNowFunc(func() time.Time { return fixedNow })

	// 构造 verified pattern：LastUsedAt = 100 天前，L2AdoptionRate=0.2 < 0.3
	oldTime := fixedNow.Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("p-verified-deprecate", "fix verified deprecate bug", ScopeGlobal, VerifiedStateVerified)
	p.LastUsedAt = oldTime
	p.L2AdoptionRate = 0.2
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 触发 UpdateLifecycle（verified 状态）
	_ = s.UpdateLifecycle("p-verified-deprecate", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-1",
	})

	got, err := s.Get("p-verified-deprecate")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("verified + l2_rate=0.2 + 100天未用 应降 deprecated, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_VerifiedNoDegrade_HighRate verified + l2_rate=0.5（>=0.3）→ 不降级。
func TestUpdateLifecycle_VerifiedNoDegrade_HighRate(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetNowFunc(func() time.Time { return fixedNow })

	oldTime := fixedNow.Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("p-verified-no-degrade", "fix no degrade bug", ScopeGlobal, VerifiedStateVerified)
	p.LastUsedAt = oldTime
	p.L2AdoptionRate = 0.5 // >= 0.3 不降级
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_ = s.UpdateLifecycle("p-verified-no-degrade", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-1",
	})

	got, _ := s.Get("p-verified-no-degrade")
	if got.VerifiedState != VerifiedStateVerified {
		t.Errorf("l2_rate=0.5 >= 0.3 不应降级, got %s", got.VerifiedState)
	}
}

// TestUpdateLifecycle_VerifiedNoDegrade_RecentUse verified + 最近使用 → 不降级（即使 l2_rate<0.3）。
func TestUpdateLifecycle_VerifiedNoDegrade_RecentUse(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetNowFunc(func() time.Time { return fixedNow })

	// LastUsedAt = 10 天前（< 90 天）
	recentTime := fixedNow.Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("p-verified-recent", "fix recent bug", ScopeGlobal, VerifiedStateVerified)
	p.LastUsedAt = recentTime
	p.L2AdoptionRate = 0.2 // < 0.3 但最近使用过
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_ = s.UpdateLifecycle("p-verified-recent", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted, OrchestrationID: "orch-1",
	})

	got, _ := s.Get("p-verified-recent")
	if got.VerifiedState != VerifiedStateVerified {
		t.Errorf("最近使用（10天 < 90天）不应降级, got %s", got.VerifiedState)
	}
}

// TestInjectContext_Unverified_NotInjected 致命反例 1：VerifiedState=unverified → 不注入（§3.3.2 注入策略）。
func TestInjectContext_Unverified_NotInjected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-inject-unverified", "fix inject bug", ScopeGlobal, VerifiedStateUnverified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("T-inject-1", "fix inject bug")
	err := s.InjectContext(tc, p)
	if err == nil {
		t.Fatal("致命反例 1：unverified pattern 不应注入（§3.3.2 注入策略）")
	}
	if !strings.Contains(err.Error(), "unverified") {
		t.Errorf("error 应含 'unverified', got: %v", err)
	}
	// ExperienceHit 不应被设置
	if tc.ExperienceHit != nil {
		t.Error("unverified pattern 不应注入 ExperienceHit")
	}
}

// TestInjectContext_Deprecated_NotInjected 致命反例 5：VerifiedState=deprecated → 不注入（防过时 pattern 误用）。
func TestInjectContext_Deprecated_NotInjected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-inject-deprecated", "fix deprecated bug", ScopeGlobal, VerifiedStateDeprecated)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("T-inject-2", "fix deprecated bug")
	err := s.InjectContext(tc, p)
	if err == nil {
		t.Fatal("致命反例 5：deprecated pattern 不应注入（防过时 pattern 误用）")
	}
	if !strings.Contains(err.Error(), "deprecated") {
		t.Errorf("error 应含 'deprecated', got: %v", err)
	}
	if tc.ExperienceHit != nil {
		t.Error("deprecated pattern 不应注入 ExperienceHit")
	}
}

// TestInjectContext_Verified_Injected verified/provisionally_verified pattern → 注入 PriorSolution/PriorFiles/ExperienceHit。
func TestInjectContext_Verified_Injected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-inject-verified", "fix verified inject bug", ScopeGlobal, VerifiedStateVerified)
	p.Steps = []Step{{Action: "edit_file", Target: "main.go", Description: "步骤1"}}
	p.FilesTouched = []string{"main.go", "util.go"}
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("T-inject-3", "fix verified inject bug")
	if err := s.InjectContext(tc, p); err != nil {
		t.Fatalf("InjectContext: %v", err)
	}

	// 验证 PriorSolution 注入
	if len(tc.PriorSolution) == 0 {
		t.Error("verified pattern 应注入 PriorSolution")
	}
	// 验证 PriorFiles 注入
	if len(tc.PriorFiles) != 2 {
		t.Errorf("PriorFiles 应有 2 个文件, got %d", len(tc.PriorFiles))
	}
	// 验证 ExperienceHit 注入
	if tc.ExperienceHit == nil {
		t.Fatal("verified pattern 应注入 ExperienceHit")
	}
	if tc.ExperienceHit.PatternID != "p-inject-verified" {
		t.Errorf("ExperienceHit.PatternID mismatch: got %s", tc.ExperienceHit.PatternID)
	}
	if tc.ExperienceHit.VerifiedState != VerifiedStateVerified {
		t.Errorf("ExperienceHit.VerifiedState mismatch: got %s", tc.ExperienceHit.VerifiedState)
	}
}

// TestInjectContext_ProvisionallyVerified_Injected provisionally_verified pattern → 也注入。
func TestInjectContext_ProvisionallyVerified_Injected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-inject-prov", "fix prov inject bug", ScopeGlobal, VerifiedStateProvisionallyVerified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("T-inject-4", "fix prov inject bug")
	if err := s.InjectContext(tc, p); err != nil {
		t.Fatalf("InjectContext: %v", err)
	}
	if tc.ExperienceHit == nil {
		t.Error("provisionally_verified pattern 应注入 ExperienceHit")
	}
}

// TestInjectContext_NoModelOverride InjectContext 不强制覆盖 task.Model（v2 修正，router 决策）。
func TestInjectContext_NoModelOverride(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-no-override", "fix no override bug", ScopeGlobal, VerifiedStateVerified)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("T-no-override", "fix no override bug")
	tc.Model = "sonnet" // 预设模型
	if err := s.InjectContext(tc, p); err != nil {
		t.Fatalf("InjectContext: %v", err)
	}
	// InjectContext 不应覆盖 task.Model（router 第四因子决策，§3.6）
	if tc.Model != "sonnet" {
		t.Errorf("InjectContext 不应覆盖 task.Model, got %s（应保留原值 sonnet）", tc.Model)
	}
	// 也不应覆盖 BudgetMode / BudgetHint
	if tc.BudgetMode != BudgetModeBalanced {
		t.Errorf("InjectContext 不应覆盖 BudgetMode, got %s", tc.BudgetMode)
	}
}

// TestInjectContext_NilTask_Rejected task=nil → 返回 error。
func TestInjectContext_NilTask_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-nil-task", "fix nil task bug", ScopeGlobal, VerifiedStateVerified)
	_ = s.Save(p)

	err := s.InjectContext(nil, p)
	if err == nil {
		t.Error("task=nil 必须返回 error")
	}
}

// TestInjectContext_NilHit_Rejected hit=nil → 返回 error。
func TestInjectContext_NilHit_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	tc := makeTestTaskContext("T-nil-hit", "fix nil hit bug")

	err := s.InjectContext(tc, nil)
	if err == nil {
		t.Error("hit=nil 必须返回 error")
	}
}

// TestInjectContext_UpdatesLastUsedAt InjectContext 后 LastUsedAt 更新。
func TestInjectContext_UpdatesLastUsedAt(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetNowFunc(func() time.Time { return fixedNow })

	p := makeTestPattern("p-last-used", "fix last used bug", ScopeGlobal, VerifiedStateVerified)
	oldTime := fixedNow.Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p.LastUsedAt = oldTime
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tc := makeTestTaskContext("T-last-used", "fix last used bug")
	if err := s.InjectContext(tc, p); err != nil {
		t.Fatalf("InjectContext: %v", err)
	}

	// 验证 LastUsedAt 已更新为 now
	got, _ := s.Get("p-last-used")
	if got.LastUsedAt == oldTime {
		t.Error("InjectContext 后 LastUsedAt 应更新为当前时间")
	}
}

// TestRecordPitfall_NewPitfall 首次失败 → 创建 pitfall。
func TestRecordPitfall_NewPitfall(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	tc := makeTestTaskContext("T-pitfall-1", "fix pitfall bug")

	failure := FailureTrace{
		ErrorType:    ErrorTypeCompileError,
		ErrorMessage: "undefined: foo",
		FailedStep:   "edit_file main.go",
		FilesTouched: []string{"main.go"},
	}
	if err := s.RecordPitfall(tc, failure); err != nil {
		t.Fatalf("RecordPitfall: %v", err)
	}

	// 验证 pitfall 已创建
	pitfalls, err := s.ListPitfallsByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListPitfallsByScope: %v", err)
	}
	if len(pitfalls) != 1 {
		t.Fatalf("应创建 1 个 pitfall, got %d", len(pitfalls))
	}
	if pitfalls[0].Occurrences != 1 {
		t.Errorf("新 pitfall Occurrences 应为 1, got %d", pitfalls[0].Occurrences)
	}
	if pitfalls[0].Symptom != "undefined: foo" {
		t.Errorf("pitfall Symptom mismatch: got %s", pitfalls[0].Symptom)
	}
}

// TestRecordPitfall_ExistingPitfall_IncrementOccurrences 同 task_signature 再次失败 → Occurrences++。
func TestRecordPitfall_ExistingPitfall_IncrementOccurrences(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	tc := makeTestTaskContext("T-pitfall-2", "fix repeat pitfall bug")

	failure1 := FailureTrace{
		ErrorType:    ErrorTypeCompileError,
		ErrorMessage: "first error",
		FilesTouched: []string{"main.go"},
	}
	if err := s.RecordPitfall(tc, failure1); err != nil {
		t.Fatalf("RecordPitfall 1st: %v", err)
	}

	// 同 task_signature 再次失败
	failure2 := FailureTrace{
		ErrorType:    ErrorTypeTestFailure,
		ErrorMessage: "second error",
		FilesTouched: []string{"main.go", "util.go"},
	}
	if err := s.RecordPitfall(tc, failure2); err != nil {
		t.Fatalf("RecordPitfall 2nd: %v", err)
	}

	pitfalls, _ := s.ListPitfallsByScope(ScopeProject)
	if len(pitfalls) != 1 {
		t.Fatalf("同 task_signature 应累加到同一 pitfall, got %d pitfalls", len(pitfalls))
	}
	if pitfalls[0].Occurrences != 2 {
		t.Errorf("Occurrences 应为 2, got %d", pitfalls[0].Occurrences)
	}
	// Symptom 应更新为最新错误
	if pitfalls[0].Symptom != "second error" {
		t.Errorf("Symptom 应更新为最新错误, got %s", pitfalls[0].Symptom)
	}
}

// TestRecordPitfall_NilTask_Rejected task=nil → 返回 error。
func TestRecordPitfall_NilTask_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	err := s.RecordPitfall(nil, FailureTrace{ErrorMessage: "test"})
	if err == nil {
		t.Error("task=nil 必须返回 error")
	}
}

// TestRecordPitfall_EmptyGoal_Rejected task.Goal 为空 → 返回 error。
func TestRecordPitfall_EmptyGoal_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	tc := makeTestTaskContext("T-empty-goal", "")
	err := s.RecordPitfall(tc, FailureTrace{ErrorMessage: "test"})
	if err == nil {
		t.Error("task.Goal 为空必须返回 error")
	}
}

// TestUpdateLifecycle_EmptyPatternID_Rejected patternID 为空 → 返回 error。
func TestUpdateLifecycle_EmptyPatternID_Rejected(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	err := s.UpdateLifecycle("", TaskCompletionSignal{})
	if err == nil {
		t.Error("patternID 为空必须返回 error")
	}
}

// TestUpdateLifecycle_NotFoundPattern pattern 不存在 → 返回 error。
func TestUpdateLifecycle_NotFoundPattern(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	err := s.UpdateLifecycle("nonexistent-pattern", TaskCompletionSignal{
		L0Pass: true, L1Score: 70, L2Verdict: L2Adopted,
	})
	if err == nil {
		t.Error("pattern 不存在必须返回 error")
	}
}

// TestIsCrossSession 跨会话判定逻辑（直接测试内部方法）。
func TestIsCrossSession(t *testing.T) {
	t.Parallel()
	s := newTestExperienceStore(t)
	p := makeTestPattern("p-cross-test", "fix cross test bug", ScopeGlobal, VerifiedStateProvisionallyVerified)
	_ = s.Save(p)

	// 注入 3 个不同 orchestration_id
	s.mu.Lock()
	s.provisionalAdoptions["p-cross-test"] = []string{"orch-1", "orch-2", "orch-3"}
	s.mu.Unlock()

	if !s.isCrossSession(p) {
		t.Error("3 个不同 orchestration_id 应判定为跨会话")
	}

	// 注入 3 个相同 orchestration_id
	s.mu.Lock()
	s.provisionalAdoptions["p-cross-test"] = []string{"same-orch", "same-orch", "same-orch"}
	s.mu.Unlock()

	if s.isCrossSession(p) {
		t.Error("3 个相同 orchestration_id 不应判定为跨会话（防同会话误判）")
	}

	// 注入 2 个不同 orchestration_id（< 3 次，不满足数量要求）
	s.mu.Lock()
	s.provisionalAdoptions["p-cross-test"] = []string{"orch-1", "orch-2"}
	s.mu.Unlock()

	if s.isCrossSession(p) {
		t.Error("2 次 L2=adopted（< 3）不应判定为跨会话升级条件满足")
	}
}
