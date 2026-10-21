// scorer_test.go — 桶 2 L0/L1/L2 三层评分反例测试（TDD 先行，地基层 §4.3/§4.4/§4.5/§4.6）。
// 7 个反例测试覆盖：L0 build/test 失败 / L1 越界 / L1 无 error path 打折 / L2 discarded 无 reason / L2 隐式排除。

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ============ L0 测试 ============

// TestL0_BuildFailure_ReturnsFalse L0 build 失败时 L0Pass=false（地基层 §4.3）。
func TestL0_BuildFailure_ReturnsFalse(t *testing.T) {
	scorer := &L0Scorer{
		workDir: "/tmp",
		runCmd: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			if name == "go" && len(args) > 0 && args[0] == "build" {
				return "build error: undefined foo", errors.New("exit 1")
			}
			return "", nil
		},
		checkContract: func(task SubTask) error { return nil },
	}
	pass, err := scorer.Calc(context.Background(), SubTask{})
	if pass {
		t.Fatal("L0 must return false when go build fails (防偷懒)")
	}
	if err == nil {
		t.Fatal("L0 must return error when go build fails")
	}
	if !strings.Contains(err.Error(), "build") {
		t.Errorf("error should mention build, got: %v", err)
	}
}

// TestL0_TestFailure_ReturnsFalse 致命反例：go test 失败时 L0Pass=false，不允许伪造通过。
// 验证目标（地基层 §4.3）：go test 失败时 Calc 必须返回 false，防 L0 偷懒。
func TestL0_TestFailure_ReturnsFalse(t *testing.T) {
	scorer := &L0Scorer{
		workDir: "/tmp",
		runCmd: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			if name == "go" && len(args) > 0 && args[0] == "build" {
				return "", nil // build 通过
			}
			if name == "go" && len(args) > 0 && args[0] == "test" {
				return "FAIL: TestFoo", errors.New("exit 1")
			}
			return "", nil
		},
		checkContract: func(task SubTask) error { return nil },
	}
	pass, err := scorer.Calc(context.Background(), SubTask{})
	if pass {
		t.Fatal("L0 must return false when go test fails (致命反例：防 L0 失败但 l0_pass=true 偷懒)")
	}
	if err == nil || !strings.Contains(err.Error(), "test") {
		t.Errorf("L0 must return error mentioning test, got: %v", err)
	}
}

// ============ L1 测试 ============

// TestL1_ScoreOutOfBounds_Rejected 致命反例：L1 评分结果必须 ∈ [0,100]，越界返回 error。
// 验证目标（地基层 §4.4）：注入越界打分函数（返回 1.5 或 -0.5），Calc 必须返回 error。
func TestL1_ScoreOutOfBounds_Rejected(t *testing.T) {
	// 构造一个会让总分超过 1.0 的打分函数（每项返回 1.5 → 加权后 > 1.0 → 分数 > 100）
	scorer := &L1Scorer{
		weights: L1Weights{
			Error:          0.22,
			TestCoverage:   0.22,
			TestQuality:    0.17,
			Style:          0.11,
			Doc:            0.11,
			TODO:           0.11,
			Citation:       0.06,
			DiffReasonable: 0.0,
		},
		scoreFns: L1ScoreFns{
			Error:          func(task SubTask) float64 { return 1.5 }, // 越界
			TestCoverage:   func(task SubTask) float64 { return 1.0 },
			TestQuality:    func(task SubTask) float64 { return 1.0 },
			Style:          func(task SubTask) float64 { return 1.0 },
			Doc:            func(task SubTask) float64 { return 1.0 },
			TODO:           func(task SubTask) float64 { return 1.0 },
			Citation:       func(task SubTask) float64 { return 1.0 },
			DiffReasonable: func(task SubTask) float64 { return 0.0 },
		},
	}

	score, err := scorer.Calc(SubTask{})
	if err == nil {
		t.Fatalf("L1 must return error when score out of bounds, got score=%d", score)
	}
	if !strings.Contains(err.Error(), "out of bounds") {
		t.Errorf("error should mention 'out of bounds', got: %v", err)
	}
}

// TestL1_NoErrorPath_HalfScore 无 error path 测试时 testCoverageScore 打折（coverRate * 0.5）。
// 验证目标（地基层 §4.4 双指标）：无 error path 时 coverRate * 0.5，防偷懒只写 happy path。
func TestL1_NoErrorPath_HalfScore(t *testing.T) {
	// 子任务有测试文件但文件名不含 "error" → checkErrorPath 返回 false → 打 5 折
	task := SubTask{
		TestFiles: []string{"foo_test.go", "bar_test.go"}, // 无 "error" 关键字
	}
	score := defaultTestCoverageScore(task)
	expected := 0.5 * 0.5 // coverRate=0.5（Phase 0 占位）× 0.5（无 error path 打折）
	if score != expected {
		t.Errorf("expected testCoverageScore=%f (coverRate*0.5), got %f", expected, score)
	}

	// 对比：有 error path 时不打折
	task2 := SubTask{
		TestFiles: []string{"error_path_test.go"}, // 含 "error"
	}
	score2 := defaultTestCoverageScore(task2)
	expected2 := 0.5 // coverRate=0.5 × 1.0（有 error path 不打折）
	if score2 != expected2 {
		t.Errorf("expected testCoverageScore=%f (coverRate*1.0), got %f", expected2, score2)
	}
}

// ============ L2 测试 ============

// TestL2_DiscardedWithoutReason_Rejected L2=discarded 但 reason 为空 → 返回 error。
// 验证目标（地基层 §4.5）：discarded 必须填 reason a/b/c/d/e。
func TestL2_DiscardedWithoutReason_Rejected(t *testing.T) {
	s := NewL2Scorer()
	_, err := s.RecordExplicit(L2Discarded, 80, "") // reason 为空
	if err == nil {
		t.Fatal("L2 discarded without reason must return error")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error should mention reason, got: %v", err)
	}

	// 反向验证：填了合法 reason 应通过
	res, err := s.RecordExplicit(L2Discarded, 80, L2ReasonRequirement)
	if err != nil {
		t.Fatalf("discarded with valid reason should pass, got: %v", err)
	}
	if res.Reason != L2ReasonRequirement {
		t.Errorf("expected reason=%s, got %s", L2ReasonRequirement, res.Reason)
	}
}

// TestL2_Implicit_ExcludedByCommitMsg commit msg 含 "refactor" → 不写隐式判定。
// 验证目标（地基层 §4.6 排除条件 2）：refactor/rename/merge/cherry-pick/upstream 排除。
func TestL2_Implicit_ExcludedByCommitMsg(t *testing.T) {
	s := NewL2Scorer()
	verdict, err := s.RecordImplicit(
		DiffStats{Insertions: 5, Deletions: 2, FilesChanged: 1},
		"refactor: extract helper function",
		1.0,   // 1 天前交付
		false, // 无显式 L2
	)
	if err != nil {
		t.Fatalf("RecordImplicit should not error on exclusion, got: %v", err)
	}
	if verdict != "" {
		t.Errorf("expected empty verdict (excluded by commit msg), got %q", verdict)
	}

	// 测试所有排除关键字
	for _, kw := range []string{"rename", "merge", "cherry-pick", "upstream"} {
		v, _ := s.RecordImplicit(
			DiffStats{Insertions: 5, Deletions: 2, FilesChanged: 1},
			"fix: "+kw+" something",
			1.0, false,
		)
		if v != "" {
			t.Errorf("keyword %q should exclude, got verdict=%q", kw, v)
		}
	}
}

// TestL2_Implicit_ExcludedByHasExplicit 已有显式 L2 → 不写隐式判定。
// 验证目标（地基层 §4.6 排除条件 4）：已有显式 L2 不覆盖。
func TestL2_Implicit_ExcludedByHasExplicit(t *testing.T) {
	s := NewL2Scorer()
	verdict, err := s.RecordImplicit(
		DiffStats{Insertions: 5, Deletions: 2, FilesChanged: 1},
		"fix: small change",
		1.0,
		true, // 已有显式 L2 → 排除
	)
	if err != nil {
		t.Fatalf("RecordImplicit should not error on exclusion, got: %v", err)
	}
	if verdict != "" {
		t.Errorf("expected empty verdict (excluded by hasExplicit), got %q", verdict)
	}
}

// TestL2_Implicit_NormalCase 正常场景（无排除）应返回隐式判定。
// 补充测试：覆盖 adopted（无改动）/ modified（小改动）/ discarded（大改动）三种判定。
func TestL2_Implicit_NormalCase(t *testing.T) {
	s := NewL2Scorer()

	// 场景 1：无改动 → implicit adopted
	v, _ := s.RecordImplicit(DiffStats{}, "no change", 0.5, false)
	if v != L2Adopted {
		t.Errorf("no change should return adopted, got %q", v)
	}

	// 场景 2：小改动（< 20 行）→ implicit modified
	v, _ = s.RecordImplicit(DiffStats{Insertions: 5, Deletions: 2, FilesChanged: 1}, "fix typo", 0.5, false)
	if v != L2Modified {
		t.Errorf("small change should return modified, got %q", v)
	}

	// 场景 3：大改动（≥ 50 行）→ implicit discarded
	v, _ = s.RecordImplicit(DiffStats{Insertions: 30, Deletions: 25, FilesChanged: 5}, "rewrite", 0.5, false)
	if v != L2Discarded {
		t.Errorf("large change should return discarded, got %q", v)
	}

	// 场景 4：> 7 天后改动 → 排除
	v, _ = s.RecordImplicit(DiffStats{Insertions: 5, Deletions: 2, FilesChanged: 1}, "fix", 10.0, false)
	if v != "" {
		t.Errorf(">7 days should exclude, got %q", v)
	}
}
