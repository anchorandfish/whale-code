// pattern_validator_test.go — H12 模式验证器反例测试（TDD 先行，§2.3）。
// 覆盖：
// - 冷启动保护：前 10 任务不验证
// - 四级流转：unverified→provisionally_verified→verified
// - 降级：L2=discarded 连续 3 次 → 退回
// - 终态：L2 连续 5 次 discarded → deprecated
// - 反例：L2 评分缺失 → 跳过验证，不升级
// - 反例：L1Score<60 不升级
//
// 设计说明 §2.3；落地：四级流转 + 冷启动 + 反例
// SNAPSHOT: tests={ColdStart_Under10_NoValidate, Unverified_TwoAdoptedHighL1_ToProvisionallyVerified, ProvisionallyVerified_FourAdoptedHighL1_ToVerified, Verified_ThreeDiscarded_ToProvisionallyVerified, ProvisionallyVerified_ThreeDiscarded_ToUnverified, Any_FiveDiscarded_ToDeprecated, NoL2Records_NoOp, L1ScoreUnder60_NoUpgrade, EmptyPatternID_ReturnsErr}
package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newTestQualityStore 创建测试用 QualityStore。
func newTestQualityStore(t *testing.T) *QualityStore {
	t.Helper()
	q, err := NewQualityStore(filepath.Join(t.TempDir(), "quality.jsonl"))
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	return q
}

// appendL2Record 辅助：追加一条 L2 评分到 QualityStore。
func appendL2Record(t *testing.T, q *QualityStore, taskID, patternID, l2Verdict string, l1Score int, ts time.Time) {
	t.Helper()
	rec := QualityRecord{
		TaskID:    taskID,
		TaskType:  TaskTypeImplement,
		Model:     ModelSonnet,
		L0Pass:    true,
		L1Score:   l1Score,
		L2Verdict: l2Verdict,
		Timestamp: ts.UTC().Format(time.RFC3339),
		PatternID: patternID,
	}
	if err := q.Append(rec); err != nil {
		t.Fatalf("Append L2 record: %v", err)
	}
}

// TestPatternValidator_ColdStart_Under10_NoValidate 冷启动期不验证（§2.3）。
func TestPatternValidator_ColdStart_Under10_NoValidate(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	// taskCounter=0（< 10）→ 直接返回 nil
	if v.GetTaskCount() != 0 {
		t.Fatalf("initial taskCounter = %d, want 0", v.GetTaskCount())
	}
	if err := v.Validate(context.Background(), "any-pattern"); err != nil {
		t.Errorf("cold start (counter<10) should return nil, got %v", err)
	}

	// 增至 9 仍不验证
	for i := 0; i < 9; i++ {
		v.IncrementTaskCount()
	}
	if v.GetTaskCount() != 9 {
		t.Fatalf("taskCounter = %d, want 9", v.GetTaskCount())
	}
	if err := v.Validate(context.Background(), "any-pattern"); err != nil {
		t.Errorf("cold start (counter=9<10) should return nil, got %v", err)
	}

	// 增至 10 才开始验证
	v.IncrementTaskCount()
	if v.GetTaskCount() != 10 {
		t.Fatalf("taskCounter = %d, want 10", v.GetTaskCount())
	}
}

// TestPatternValidator_Unverified_TwoAdoptedHighL1_ToProvisionallyVerified
// unverified → provisionally_verified：L2 连续 2 次 adopted + L1Score>=60（§2.3）。
func TestPatternValidator_Unverified_TwoAdoptedHighL1_ToProvisionallyVerified(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	// 注入 unverified pattern
	pid := "test-pattern-1"
	pattern := makeTestPattern(pid, "fix bug in auth", ScopeProject, VerifiedStateUnverified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 注入 2 条 L2=adopted + L1Score>=60 记录
	now := time.Now().UTC()
	appendL2Record(t, q, "task-a", pid, L2Adopted, 75, now.Add(-2*time.Hour))
	appendL2Record(t, q, "task-b", pid, L2Adopted, 80, now.Add(-1*time.Hour))

	// 增加任务计数器超过冷启动阈值
	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateProvisionallyVerified {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateProvisionallyVerified)
	}
}

// TestPatternValidator_ProvisionallyVerified_FourAdoptedHighL1_ToVerified
// provisionally_verified → verified：再 2 次 adopted + L1Score>=60（累计 4 次）（§2.3）。
func TestPatternValidator_ProvisionallyVerified_FourAdoptedHighL1_ToVerified(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-2"
	pattern := makeTestPattern(pid, "implement feature X", ScopeProject, VerifiedStateProvisionallyVerified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 注入 4 条 L2=adopted + L1Score>=60 记录（累计 4 次）
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		appendL2Record(t, q, "task-c-"+string(rune('A'+i)), pid, L2Adopted, 70, now.Add(time.Duration(-4+i)*time.Hour))
	}

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateVerified {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateVerified)
	}
}

// TestPatternValidator_Verified_ThreeDiscarded_ToProvisionallyVerified
// verified → provisionally_verified：L2 连续 3 次 discarded（§2.3）。
func TestPatternValidator_Verified_ThreeDiscarded_ToProvisionallyVerified(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-3"
	pattern := makeTestPattern(pid, "review code", ScopeProject, VerifiedStateVerified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		appendL2Record(t, q, "task-d-"+string(rune('A'+i)), pid, L2Discarded, 30, now.Add(time.Duration(-3+i)*time.Hour))
	}

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateProvisionallyVerified {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateProvisionallyVerified)
	}
}

// TestPatternValidator_ProvisionallyVerified_ThreeDiscarded_ToUnverified
// provisionally_verified → unverified：L2 连续 3 次 discarded（§2.3）。
func TestPatternValidator_ProvisionallyVerified_ThreeDiscarded_ToUnverified(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-4"
	pattern := makeTestPattern(pid, "refactor module", ScopeProject, VerifiedStateProvisionallyVerified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		appendL2Record(t, q, "task-e-"+string(rune('A'+i)), pid, L2Discarded, 20, now.Add(time.Duration(-3+i)*time.Hour))
	}

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateUnverified)
	}
}

// TestPatternValidator_Any_FiveDiscarded_ToDeprecated 任意 → deprecated：L2 连续 5 次 discarded（终态，§2.3）。
func TestPatternValidator_Any_FiveDiscarded_ToDeprecated(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-5"
	pattern := makeTestPattern(pid, "deprecated feature", ScopeProject, VerifiedStateUnverified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		appendL2Record(t, q, "task-f-"+string(rune('A'+i)), pid, L2Discarded, 10, now.Add(time.Duration(-5+i)*time.Hour))
	}

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("VerifiedState = %q, want %q (terminal)", got.VerifiedState, VerifiedStateDeprecated)
	}
}

// TestPatternValidator_NoL2Records_NoOp 反例：无 L2 评分不升级（§2.3 降级）。
func TestPatternValidator_NoL2Records_NoOp(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-6"
	pattern := makeTestPattern(pid, "no L2 records", ScopeProject, VerifiedStateUnverified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("no L2 records should not upgrade, got %q", got.VerifiedState)
	}
}

// TestPatternValidator_L1ScoreUnder60_NoUpgrade 反例：L1Score<60 不升级（§2.3）。
func TestPatternValidator_L1ScoreUnder60_NoUpgrade(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-7"
	pattern := makeTestPattern(pid, "low L1 score", ScopeProject, VerifiedStateUnverified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 注入 2 条 L2=adopted 但 L1Score<60
	now := time.Now().UTC()
	appendL2Record(t, q, "task-g1", pid, L2Adopted, 50, now.Add(-2*time.Hour))
	appendL2Record(t, q, "task-g2", pid, L2Adopted, 55, now.Add(-1*time.Hour))

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("L1Score<60 should not upgrade, got %q", got.VerifiedState)
	}
}

// TestPatternValidator_EmptyPatternID_ReturnsErr patternID 空返回 error。
func TestPatternValidator_EmptyPatternID_ReturnsErr(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), ""); err == nil {
		t.Errorf("empty patternID should return error")
	}
}

// TestPatternValidator_DeprecatedState_NoTransition deprecated 终态不流转。
func TestPatternValidator_DeprecatedState_NoTransition(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	pid := "test-pattern-8"
	pattern := makeTestPattern(pid, "already deprecated", ScopeProject, VerifiedStateDeprecated)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 注入 5 条 L2=adopted（即便全 adopted，deprecated 也不应流转）
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		appendL2Record(t, q, "task-h-"+string(rune('A'+i)), pid, L2Adopted, 90, now.Add(time.Duration(-5+i)*time.Hour))
	}

	for i := 0; i < 10; i++ {
		v.IncrementTaskCount()
	}

	if err := v.Validate(context.Background(), pid); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("deprecated terminal state should not transition, got %q", got.VerifiedState)
	}
}

// TestPatternValidator_NilDeps_Degrade experience/quality 为 nil 降级。
func TestPatternValidator_NilDeps_Degrade(t *testing.T) {
	v := NewPatternValidator(nil, nil)
	if err := v.Validate(context.Background(), "any"); err != nil {
		t.Errorf("nil deps should degrade silently, got %v", err)
	}
}

// TestPatternValidator_CtxCancelled_ReturnsErr ctx 取消返回 ctx.Err()。
func TestPatternValidator_CtxCancelled_ReturnsErr(t *testing.T) {
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	v := NewPatternValidator(s, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := v.Validate(ctx, "any"); err == nil {
		t.Errorf("cancelled ctx should return ctx.Err()")
	}
}
