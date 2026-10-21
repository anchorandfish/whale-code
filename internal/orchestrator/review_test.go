// review_test.go — H10 Phase 2 模块 4 ReviewSubconversation 接口契约测试（总纲 §5.4）。
// 验证 noop 实现返回 pass + Round=1 不 panic，接口签名稳定（Phase 3 实现时不可破坏）。
// ；落地：noop 接口契约测试
// /020-审查 1 轮硬上限；落地：ValidateReviewRequest 校验 MaxRound=1
// ；落地：NoPlan=false 时 PlanDoc 必须非空
// SNAPSHOT: tests={NoopReview_ReturnsPass, MaxRoundMustBeOne, InterfaceContract, ValidateReviewRequest_AllCases}

package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// TestNoopReview_Review_ReturnsPass noop Review 返回 verdict=pass, round=1 不 panic（接口契约）。
func TestNoopReview_Review_ReturnsPass(t *testing.T) {
	r := NewReviewSubconversation()
	report, err := r.Review(context.Background(), ReviewRequest{
		TaskID:     "T-test-001",
		Stage:      StageReview,
		NoPlan:     true,
		Dimensions: 3,
		MaxRound:   1,
		Content:    "some code to review",
	})
	if err != nil {
		t.Errorf("noop Review 必须返回 nil error，got: %v", err)
	}
	if report == nil {
		t.Fatal("noop Review 必须返回非 nil report")
	}
	if report.Verdict != ReviewVerdictPass {
		t.Errorf("noop Review verdict 必须 = %q，got %q", ReviewVerdictPass, report.Verdict)
	}
	if report.Round != 1 {
		t.Errorf("noop Review Round 必须 = 1（/020），got %d", report.Round)
	}
}

// TestReviewRequest_MaxRoundMustBeOne 致命反例：MaxRound != 1 的请求必须被拒绝（/020，零容忍）。
func TestReviewRequest_MaxRoundMustBeOne(t *testing.T) {
	cases := []struct {
		name     string
		req      *ReviewRequest
		wantErr  bool
		errMatch string
	}{
		{
			name: "MaxRound=0 拒绝",
			req: &ReviewRequest{
				TaskID:   "T-001",
				Stage:    StageReview,
				MaxRound: 0,
				Content:  "code",
				NoPlan:   true,
			},
			wantErr:  true,
			errMatch: "MaxRound must be 1",
		},
		{
			name: "MaxRound=2 拒绝（红队凑数陷阱防护）",
			req: &ReviewRequest{
				TaskID:   "T-001",
				Stage:    StageReview,
				MaxRound: 2,
				Content:  "code",
				NoPlan:   true,
			},
			wantErr:  true,
			errMatch: "MaxRound must be 1",
		},
		{
			name: "MaxRound=3 拒绝（红队凑数陷阱防护）",
			req: &ReviewRequest{
				TaskID:   "T-001",
				Stage:    StageReview,
				MaxRound: 3,
				Content:  "code",
				NoPlan:   true,
			},
			wantErr:  true,
			errMatch: "MaxRound must be 1",
		},
		{
			name: "MaxRound=1 通过",
			req: &ReviewRequest{
				TaskID:   "T-001",
				Stage:    StageReview,
				MaxRound: 1,
				Content:  "code",
				NoPlan:   true,
			},
			wantErr: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateReviewRequest(c.req)
			if c.wantErr {
				if err == nil {
					t.Fatalf("致命反例未拦截：%s 必须返回 error", c.name)
				}
				if c.errMatch != "" && !strings.Contains(err.Error(), c.errMatch) {
					t.Errorf("error 应包含 %q，got: %v", c.errMatch, err)
				}
			} else {
				if err != nil {
					t.Errorf("合法请求不应返回 error，got: %v", err)
				}
			}
		})
	}
}

// TestValidateReviewRequest_AllCases 覆盖 ValidateReviewRequest 全部分支（/016/020）。
func TestValidateReviewRequest_AllCases(t *testing.T) {
	t.Run("nil 请求拒绝", func(t *testing.T) {
		if err := ValidateReviewRequest(nil); err == nil {
			t.Fatal("nil 请求必须返回 error")
		}
	})

	t.Run("TaskID 空拒绝", func(t *testing.T) {
		req := &ReviewRequest{
			TaskID:   "",
			Stage:    StageReview,
			MaxRound: 1,
			Content:  "code",
			NoPlan:   true,
		}
		if err := ValidateReviewRequest(req); err == nil {
			t.Fatal("TaskID 空必须返回 error")
		}
	})

	t.Run("Stage 空拒绝", func(t *testing.T) {
		req := &ReviewRequest{
			TaskID:   "T-001",
			Stage:    "",
			MaxRound: 1,
			Content:  "code",
			NoPlan:   true,
		}
		if err := ValidateReviewRequest(req); err == nil {
			t.Fatal("Stage 空必须返回 error")
		}
	})

	t.Run("Content 空拒绝", func(t *testing.T) {
		req := &ReviewRequest{
			TaskID:   "T-001",
			Stage:    StageReview,
			MaxRound: 1,
			Content:  "",
			NoPlan:   true,
		}
		if err := ValidateReviewRequest(req); err == nil {
			t.Fatal("Content 空必须返回 error")
		}
	})

	t.Run("NoPlan=false 但 PlanDoc 空拒绝", func(t *testing.T) {
		req := &ReviewRequest{
			TaskID:   "T-001",
			Stage:    StageReview,
			MaxRound: 1,
			Content:  "code",
			NoPlan:   false, // 喂方案审查，但 PlanDoc 空
		}
		err := ValidateReviewRequest(req)
		if err == nil {
			t.Fatal("NoPlan=false 但 PlanDoc 空必须返回 error")
		}
		if !strings.Contains(err.Error(), "PlanDoc") {
			t.Errorf("error 应包含 'PlanDoc'，got: %v", err)
		}
	})

	t.Run("NoPlan=false 且 PlanDoc 非空通过", func(t *testing.T) {
		req := &ReviewRequest{
			TaskID:   "T-001",
			Stage:    StagePlan,
			MaxRound: 1,
			Content:  "code",
			NoPlan:   false,
			PlanDoc:  "plan.md",
		}
		if err := ValidateReviewRequest(req); err != nil {
			t.Errorf("合法请求不应返回 error，got: %v", err)
		}
	})
}

// TestReviewSubconversation_InterfaceContract 验证 ReviewSubconversation 接口签名稳定（Phase 3 实现时不可破坏）。
// 通过方法集校验，确保 Review 方法签名不变。
func TestReviewSubconversation_InterfaceContract(t *testing.T) {
	r := NewReviewSubconversation()

	// 验证方法签名稳定（Phase 3 实现时不可破坏）
	_ = func(ctx context.Context, req ReviewRequest) (*ReviewReport, error) {
		return r.Review(ctx, req)
	}

	// 验证 ReviewReport struct 字段稳定
	report := &ReviewReport{
		Verdict:      ReviewVerdictPass,
		FatalFlaws:   nil, // 0 个就是 0 个，不许凑数
		Suggestions:  []string{"suggestion-1"},
		Round:        1,
		EvidenceURLs: []string{"https://example.com/evidence"},
	}
	if report.Round != 1 {
		t.Errorf("ReviewReport.Round 必须 = 1（/020），got %d", report.Round)
	}
	if report.Verdict != ReviewVerdictPass && report.Verdict != ReviewVerdictFail {
		t.Errorf("ReviewReport.Verdict 必须 = pass/fail，got %q", report.Verdict)
	}

	// 验证 ReviewRequest struct 字段稳定
	req := ReviewRequest{
		TaskID:     "T-001",
		Stage:      StageReview,
		NoPlan:     true,
		Dimensions: 3,
		MaxRound:   1,
		Content:    "code",
		PlanDoc:    "",
	}
	if req.MaxRound != 1 {
		t.Errorf("ReviewRequest.MaxRound 必须 = 1（/020），got %d", req.MaxRound)
	}
}
