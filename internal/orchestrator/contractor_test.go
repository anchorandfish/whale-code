// contractor_test.go — H10 Phase 6 模块 5 Contractor 单元测试（TDD 先行，03-实施prompt.md §5.3）。
// 覆盖：Confirm 用户接受 / Confirm 用户拒绝返回 ErrContractRejected / Confirm 用户修改 /
// Confirm 超时返回 ErrContractTimeout / 空方案直接接受。
// §5.3 S1 Contractor 测试；落地：4 个核心测试 + 边界补充
// ；落地：用户拒绝/超时进 StageInterrupted
// SNAPSHOT: tests={Confirm_Accepted, Confirm_Rejected, Confirm_Modified, Confirm_Timeout, Confirm_NilPlan, Confirm_NilGateway, BuildContractQuestion, ParseContractAnswer}

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// makeTestContractorWithMock 构造测试用 Contractor + MockAskUserGateway（便于复用，S5）。
func makeTestContractorWithMock(confirmFn func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error)) (*Contractor, *MockAskUserGateway) {
	mock := &MockAskUserGateway{ConfirmFunc: confirmFn}
	return NewContractor(mock, nil), mock
}

// makeTestPlanForContract 构造测试用 GeneratedPlan（contract Stage 展示用，S5）。
func makeTestPlanForContract() *GeneratedPlan {
	return &GeneratedPlan{
		SubTasks: []SubTaskDef{
			{SubTaskID: "S-001", Type: TaskTypeImplement, Complexity: ComplexityT1},
			{SubTaskID: "S-002", Type: TaskTypeReview, Complexity: ComplexityT2, DependsOn: []string{"S-001"}},
		},
		Reason:  "测试方案",
		EstCost: 300,
	}
}

// TestContractor_Confirm_Accepted 用户接受方案，验证 Confirm 返回 Accepted=true（S5）。
// 覆盖 happy path：mock ConfirmContract 返回 Accepted=true，验证 trace 写入 + 返回值。
func TestContractor_Confirm_Accepted(t *testing.T) {
	t.Parallel()
	c, _ := makeTestContractorWithMock(func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		return ConfirmResult{Accepted: true}, nil
	})

	result, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if err != nil {
		t.Fatalf("Confirm accepted: %v", err)
	}
	if !result.Accepted {
		t.Error("用户接受时 result.Accepted 应为 true")
	}
	if result.Modified != "" {
		t.Errorf("用户接受未修改时 Modified 应为空，got: %s", result.Modified)
	}
}

// TestContractor_Confirm_Rejected 用户拒绝方案，验证 Confirm 返回 ErrContractRejected（S5）。
// 覆盖降级路径：mock ConfirmContract 返回 Accepted=false，验证返回 ErrContractRejected。
func TestContractor_Confirm_Rejected(t *testing.T) {
	t.Parallel()
	c, _ := makeTestContractorWithMock(func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		return ConfirmResult{Accepted: false}, nil
	})

	result, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if !errors.Is(err, ErrContractRejected) {
		t.Fatalf("用户拒绝时应返回 ErrContractRejected，got: %v", err)
	}
	if result.Accepted {
		t.Error("用户拒绝时 result.Accepted 应为 false")
	}
}

// TestContractor_Confirm_Modified 用户修改方案（Accepted=true + Modified 非空），验证 Confirm 返回正确（S5）。
// 覆盖用户修改场景：mock ConfirmContract 返回 Accepted=true + Modified 字段。
func TestContractor_Confirm_Modified(t *testing.T) {
	t.Parallel()
	modifiedDesc := "增加 S-003 子任务覆盖边界 case"
	c, _ := makeTestContractorWithMock(func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		return ConfirmResult{Accepted: true, Modified: modifiedDesc}, nil
	})

	result, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if err != nil {
		t.Fatalf("Confirm modified: %v", err)
	}
	if !result.Accepted {
		t.Error("用户修改但接受时 result.Accepted 应为 true")
	}
	if result.Modified != modifiedDesc {
		t.Errorf("result.Modified = %s, want %s", result.Modified, modifiedDesc)
	}
}

// TestContractor_Confirm_Timeout 用户确认超时，验证 Confirm 返回 ErrContractTimeout（S5）。
// 覆盖超时降级：mock ConfirmContract 返回 context.DeadlineExceeded，验证转 ErrContractTimeout。
func TestContractor_Confirm_Timeout(t *testing.T) {
	t.Parallel()
	c, _ := makeTestContractorWithMock(func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		return ConfirmResult{}, context.DeadlineExceeded
	})

	_, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if !errors.Is(err, ErrContractTimeout) {
		t.Fatalf("超时应返回 ErrContractTimeout，got: %v", err)
	}
}

// TestContractor_Confirm_GatewayError gateway 返回其他 error，验证 Confirm 透传（S5）。
func TestContractor_Confirm_GatewayError(t *testing.T) {
	t.Parallel()
	gwErr := errors.New("gateway: channel closed")
	c, _ := makeTestContractorWithMock(func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		return ConfirmResult{}, gwErr
	})

	_, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if err == nil {
		t.Fatal("gateway error 时 Confirm 应返回 error")
	}
	if !errors.Is(err, gwErr) {
		t.Errorf("Confirm 应透传 gateway error，got: %v", err)
	}
}

// TestContractor_Confirm_NilPlan 空方案应直接接受（向后兼容，S5 边界 case）。
func TestContractor_Confirm_NilPlan(t *testing.T) {
	t.Parallel()
	c, _ := makeTestContractorWithMock(nil)

	result, err := c.Confirm(context.Background(), nil)
	if err != nil {
		t.Fatalf("空方案 Confirm 不应返回 error，got: %v", err)
	}
	if !result.Accepted {
		t.Error("空方案应直接接受（Accepted=true）")
	}
}

// TestContractor_Confirm_EmptySubTasks 空 SubTasks 应直接接受（S5 边界 case）。
func TestContractor_Confirm_EmptySubTasks(t *testing.T) {
	t.Parallel()
	c, _ := makeTestContractorWithMock(nil)

	result, err := c.Confirm(context.Background(), &GeneratedPlan{SubTasks: nil})
	if err != nil {
		t.Fatalf("空 SubTasks Confirm 不应返回 error，got: %v", err)
	}
	if !result.Accepted {
		t.Error("空 SubTasks 应直接接受")
	}
}

// TestContractor_Confirm_NilGateway gateway 未注入应返回 error（S5 健壮性）。
func TestContractor_Confirm_NilGateway(t *testing.T) {
	t.Parallel()
	c := NewContractor(nil, nil)
	_, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if err == nil {
		t.Fatal("gateway 未注入时应返回 error")
	}
	if !strings.Contains(err.Error(), "askUser gateway not configured") {
		t.Errorf("error 应包含 'askUser gateway not configured'，got: %v", err)
	}
}

// TestContractor_Confirm_NilReceiver nil receiver 应返回 error 不 panic（S5 健壮性）。
func TestContractor_Confirm_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *Contractor
	_, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if err == nil {
		t.Fatal("nil receiver Confirm 应返回 error")
	}
}

// TestContractor_Confirm_TraceWritten 验证 Confirm 时 trace 事件被写入（S5）。
func TestContractor_Confirm_TraceWritten(t *testing.T) {
	t.Parallel()
	tracer := makeTestTraceStore(t)
	mock := &MockAskUserGateway{ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		return ConfirmResult{Accepted: true}, nil
	}}
	c := NewContractor(mock, tracer)

	_, err := c.Confirm(context.Background(), makeTestPlanForContract())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	// 验证 trace.jsonl 含 contract_started + contract_completed 事件
	events, err := tracer.QueryBySubtask("contract")
	if err != nil {
		t.Fatalf("QueryBySubtask: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 trace events, got %d", len(events))
	}
	foundStart := false
	foundComplete := false
	for _, e := range events {
		if e.EventType == "contract_started" {
			foundStart = true
		}
		if e.EventType == "contract_completed" {
			foundComplete = true
		}
	}
	if !foundStart {
		t.Error("trace 应包含 contract_started 事件")
	}
	if !foundComplete {
		t.Error("trace 应包含 contract_completed 事件")
	}
}

// TestContractor_Confirm_ContextCanceled context 取消时不应死锁（S5 健壮性）。
func TestContractor_Confirm_ContextCanceled(t *testing.T) {
	t.Parallel()
	c, _ := makeTestContractorWithMock(func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
		// 等待 context 取消
		<-ctx.Done()
		return ConfirmResult{}, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := c.Confirm(ctx, makeTestPlanForContract())
	if err == nil {
		t.Fatal("context 取消时应返回 error")
	}
}

// TestBuildContractQuestion 验证 BuildContractQuestion 构造的提问含 3 个选项（接受/拒绝/修改，S5）。
func TestBuildContractQuestion(t *testing.T) {
	t.Parallel()
	plan := makeTestPlanForContract()
	q := BuildContractQuestion(plan)

	if q.ID != "CONTRACT_CONFIRM" {
		t.Errorf("Question.ID = %s, want CONTRACT_CONFIRM", q.ID)
	}
	if len(q.Options) != 3 {
		t.Fatalf("expected 3 options (accept/reject/modify), got %d", len(q.Options))
	}
	// 验证选项 value
	values := map[string]bool{"accept": false, "reject": false, "modify": false}
	for _, opt := range q.Options {
		values[opt.Value] = true
	}
	for v, found := range values {
		if !found {
			t.Errorf("缺少选项 value=%s", v)
		}
	}
	if !strings.Contains(q.Question, "请确认") {
		t.Errorf("Question 文本应包含 '请确认'，got: %s", q.Question)
	}
}

// TestParseContractAnswer 验证 ParseContractAnswer 解析各种用户输入（S5）。
func TestParseContractAnswer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		value    string
		accepted bool
		modified bool
	}{
		{"accept_english", "accept", true, false},
		{"accept_chinese", "接受", true, false},
		{"yes_short", "y", true, false},
		{"reject_english", "reject", false, false},
		{"reject_chinese", "拒绝", false, false},
		{"no_short", "n", false, false},
		{"modify_english", "modify", true, true},
		{"modify_chinese", "修改", true, true},
		{"free_input_modification", "请增加单元测试覆盖", true, true},
		{"empty_input_rejected", "   ", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ParseContractAnswer(ClarifyAnswer{QuestionID: "CONTRACT_CONFIRM", Value: tc.value})
			if result.Accepted != tc.accepted {
				t.Errorf("Accepted = %v, want %v", result.Accepted, tc.accepted)
			}
			if tc.modified && result.Modified == "" {
				t.Errorf("Modified 应非空（用户修改场景），got: %q", result.Modified)
			}
			if !tc.modified && result.Modified != "" {
				t.Errorf("Modified 应为空（非修改场景），got: %q", result.Modified)
			}
		})
	}
}
