// plan_generator_test.go — H10 Phase 6 模块 5 PlanGenerator 单元测试（TDD 先行，03-实施prompt.md §5.2）。
// 覆盖：Generate 成功（2-5 子任务）/ Generate LLM 失败返回 error / Generate 空 SubTasks（边界 case，降级单子任务）/
// DegradeToSingleTask 降级方案 / SerializePlan + DeserializePlan 序列化往返。
// §5.2 S1 PlanGenerator 测试；落地：3 个核心测试 + 边界/序列化补充
// ≤3；落地：生成的子任务 SubTaskID 唯一
// ；落地：空 SubTasks 降级为单子任务
// SNAPSHOT: tests={Generate_HappyPath, Generate_LLMError, Generate_EmptySubTasks, Generate_InvalidJSON, Generate_SubTasksTruncatedToMax, Generate_DuplicateIDsDeduped, DegradeToSingleTask, SerializeDeserialize_RoundTrip}

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// makeTestPlanGeneratorWithMock 构造测试用 PlanGenerator + mock AIClient（便于复用，S5）。
func makeTestPlanGeneratorWithMock(completeFn func(ctx context.Context, prompt string) (string, error)) (*PlanGenerator, *mockAIClient) {
	client := &mockAIClient{completeFn: completeFn}
	return NewPlanGenerator(client, nil), client
}

// TestPlanGenerator_Generate_HappyPath mock AIClient 返回 3 个子任务，验证 Generate 解析正确（S5）。
// 覆盖 happy path：LLM 返回合法 JSON 含 3 个子任务，验证 SubTasks/Reason/EstCost 字段。
func TestPlanGenerator_Generate_HappyPath(t *testing.T) {
	t.Parallel()
	mockOutput := `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1","files_to_read":["foo.go"],"files_to_edit":["foo_test.go"],"depends_on":[]},{"subtask_id":"S-002","type":"implement","complexity":"T2","files_to_read":["bar.go"],"files_to_edit":["bar_test.go"],"depends_on":["S-001"]},{"subtask_id":"S-003","type":"review","complexity":"T1","files_to_read":[],"files_to_edit":[],"depends_on":["S-001","S-002"]}],"reason":"按模块拆分","est_cost":500}`
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	plan, err := p.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate happy path: %v", err)
	}
	if plan == nil {
		t.Fatal("plan 不应为 nil")
	}
	if len(plan.SubTasks) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(plan.SubTasks))
	}
	if plan.SubTasks[0].SubTaskID != "S-001" {
		t.Errorf("SubTasks[0].SubTaskID = %s, want S-001", plan.SubTasks[0].SubTaskID)
	}
	if plan.SubTasks[0].Type != TaskTypeImplement {
		t.Errorf("SubTasks[0].Type = %s, want %s", plan.SubTasks[0].Type, TaskTypeImplement)
	}
	if plan.SubTasks[0].Complexity != ComplexityT1 {
		t.Errorf("SubTasks[0].Complexity = %s, want %s", plan.SubTasks[0].Complexity, ComplexityT1)
	}
	if len(plan.SubTasks[1].DependsOn) != 1 || plan.SubTasks[1].DependsOn[0] != "S-001" {
		t.Errorf("SubTasks[1].DependsOn = %v, want [S-001]", plan.SubTasks[1].DependsOn)
	}
	if plan.Reason != "按模块拆分" {
		t.Errorf("plan.Reason = %s, want '按模块拆分'", plan.Reason)
	}
	if plan.EstCost != 500 {
		t.Errorf("plan.EstCost = %d, want 500", plan.EstCost)
	}
}

// TestPlanGenerator_Generate_LLMError mock AIClient 返回 error，验证 Generate 返回 error（S5）。
// 覆盖降级路径：LLM 调用失败时 Generate 不应吞错（调用方降级单子任务）。
func TestPlanGenerator_Generate_LLMError(t *testing.T) {
	t.Parallel()
	llmErr := errors.New("llm: timeout")
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return "", llmErr
	})

	plan, err := p.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err == nil {
		t.Fatal("Generate LLM error 应返回 error，got nil")
	}
	if !strings.Contains(err.Error(), "llm complete") {
		t.Errorf("error 应包含 'llm complete'，got: %v", err)
	}
	if plan != nil {
		t.Errorf("LLM 失败时 plan 应为 nil，got %+v", plan)
	}
}

// TestPlanGenerator_Generate_EmptySubTasks mock AIClient 返回不含 JSON 的输出，验证 Generate 返回空 SubTasks（S5 边界 case）。
// 覆盖边界 case：LLM 返回无 JSON 时返回空 SubTasks plan（调用方降级单子任务，§11.2）。
func TestPlanGenerator_Generate_EmptySubTasks(t *testing.T) {
	t.Parallel()
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return "抱歉，我无法拆分此任务", nil
	})

	plan, err := p.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate 边界 case 不应返回 error（应返回空 plan），got: %v", err)
	}
	if plan == nil {
		t.Fatal("边界 case plan 不应为 nil（应为空 SubTasks plan）")
	}
	if len(plan.SubTasks) != 0 {
		t.Errorf("无 JSON 输出时 SubTasks 应为空，got %d", len(plan.SubTasks))
	}
}

// TestPlanGenerator_Generate_EmptySubTasksArray mock AIClient 返回空 subtasks 数组，验证 Generate 返回空 SubTasks（S5 边界 case 2）。
func TestPlanGenerator_Generate_EmptySubTasksArray(t *testing.T) {
	t.Parallel()
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return `{"subtasks":[],"reason":"空","est_cost":0}`, nil
	})

	plan, err := p.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err != nil {
		t.Fatalf("Generate 空 subtasks 数组不应返回 error，got: %v", err)
	}
	if len(plan.SubTasks) != 0 {
		t.Errorf("空 subtasks 数组应返回 0 个子任务，got %d", len(plan.SubTasks))
	}
}

// TestPlanGenerator_Generate_InvalidJSON mock AIClient 返回损坏 JSON，验证 Generate 返回 error（S5 健壮性）。
func TestPlanGenerator_Generate_InvalidJSON(t *testing.T) {
	t.Parallel()
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return `{"subtasks":[invalid json]}`, nil
	})

	_, err := p.Generate(context.Background(), "为 internal/foo 加单元测试")
	if err == nil {
		t.Fatal("损坏 JSON 应返回 error")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("error 应包含 'unmarshal'，got: %v", err)
	}
}

// TestPlanGenerator_Generate_SubTasksTruncatedToMax 子任务数超过 10 应截断（防 LLM 过拆，S5）。
func TestPlanGenerator_Generate_SubTasksTruncatedToMax(t *testing.T) {
	t.Parallel()
	// 构造 15 个子任务
	var subs []string
	for i := 0; i < 15; i++ {
		subs = append(subs, `{"subtask_id":"S-00`+string(rune('1'+i))+`","type":"implement","complexity":"T1"}`)
	}
	mockOutput := `{"subtasks":[` + strings.Join(subs, ",") + `],"reason":"过拆","est_cost":1000}`
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	plan, err := p.Generate(context.Background(), "test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(plan.SubTasks) != PlanGeneratorMaxSubTasks {
		t.Errorf("超过 max 的子任务数应截断为 %d，got %d", PlanGeneratorMaxSubTasks, len(plan.SubTasks))
	}
}

// TestPlanGenerator_Generate_DuplicateIDsDeduped 重复 SubTaskID 应去重（S5）。
func TestPlanGenerator_Generate_DuplicateIDsDeduped(t *testing.T) {
	t.Parallel()
	mockOutput := `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1"},{"subtask_id":"S-001","type":"review","complexity":"T2"},{"subtask_id":"S-002","type":"implement","complexity":"T1"}]}`
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	plan, err := p.Generate(context.Background(), "test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(plan.SubTasks) != 2 {
		t.Errorf("重复 SubTaskID 应去重为 2 个，got %d", len(plan.SubTasks))
	}
}

// TestPlanGenerator_Generate_InvalidTypeDefaultsToImplement 非法 Type 应默认为 implement（S5）。
func TestPlanGenerator_Generate_InvalidTypeDefaultsToImplement(t *testing.T) {
	t.Parallel()
	mockOutput := `{"subtasks":[{"subtask_id":"S-001","type":"unknown_type","complexity":"T1"}]}`
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	plan, err := p.Generate(context.Background(), "test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if plan.SubTasks[0].Type != TaskTypeImplement {
		t.Errorf("非法 Type 应默认为 %s，got %s", TaskTypeImplement, plan.SubTasks[0].Type)
	}
}

// TestPlanGenerator_Generate_InvalidComplexityDefaultsToT2 非法 Complexity 应默认为 T2（S5）。
func TestPlanGenerator_Generate_InvalidComplexityDefaultsToT2(t *testing.T) {
	t.Parallel()
	mockOutput := `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T9"}]}`
	p, _ := makeTestPlanGeneratorWithMock(func(ctx context.Context, prompt string) (string, error) {
		return mockOutput, nil
	})

	plan, err := p.Generate(context.Background(), "test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if plan.SubTasks[0].Complexity != ComplexityT2 {
		t.Errorf("非法 Complexity 应默认为 %s，got %s", ComplexityT2, plan.SubTasks[0].Complexity)
	}
}

// TestPlanGenerator_DegradeToSingleTask 验证 DegradeToSingleTask 构造单个 implement 子任务（S5）。
// 覆盖降级路径：LLM 失败/空 SubTasks 时降级为单子任务兜底。
func TestPlanGenerator_DegradeToSingleTask(t *testing.T) {
	t.Parallel()
	p := &PlanGenerator{}
	plan := p.DegradeToSingleTask("为 internal/foo 加单元测试")
	if plan == nil {
		t.Fatal("DegradeToSingleTask 不应返回 nil")
	}
	if len(plan.SubTasks) != 1 {
		t.Fatalf("降级方案应有 1 个子任务，got %d", len(plan.SubTasks))
	}
	if plan.SubTasks[0].SubTaskID != "S-001" {
		t.Errorf("降级子任务 ID = %s, want S-001", plan.SubTasks[0].SubTaskID)
	}
	if plan.SubTasks[0].Type != TaskTypeImplement {
		t.Errorf("降级子任务 Type = %s, want %s", plan.SubTasks[0].Type, TaskTypeImplement)
	}
	if plan.SubTasks[0].Complexity != ComplexityT2 {
		t.Errorf("降级子任务 Complexity = %s, want %s", plan.SubTasks[0].Complexity, ComplexityT2)
	}
	if !strings.Contains(plan.Reason, "降级") {
		t.Errorf("降级原因应包含 '降级'，got: %s", plan.Reason)
	}
}

// TestPlanGenerator_SerializeDeserialize_RoundTrip 验证 SerializePlan + DeserializePlan 序列化往返一致（S5）。
func TestPlanGenerator_SerializeDeserialize_RoundTrip(t *testing.T) {
	t.Parallel()
	original := &GeneratedPlan{
		SubTasks: []SubTaskDef{
			{SubTaskID: "S-001", Type: TaskTypeImplement, Complexity: ComplexityT1, FilesToRead: []string{"a.go"}},
			{SubTaskID: "S-002", Type: TaskTypeReview, Complexity: ComplexityT2, DependsOn: []string{"S-001"}},
		},
		Reason:  "测试序列化",
		EstCost: 300,
	}

	jsonStr, err := SerializePlan(original)
	if err != nil {
		t.Fatalf("SerializePlan: %v", err)
	}
	if jsonStr == "" {
		t.Fatal("序列化结果不应为空")
	}

	got, err := DeserializePlan(jsonStr)
	if err != nil {
		t.Fatalf("DeserializePlan: %v", err)
	}
	if got == nil {
		t.Fatal("反序列化结果不应为 nil")
	}
	if len(got.SubTasks) != 2 {
		t.Fatalf("反序列化 SubTasks 数 = %d, want 2", len(got.SubTasks))
	}
	if got.SubTasks[0].SubTaskID != "S-001" {
		t.Errorf("SubTasks[0].SubTaskID = %s, want S-001", got.SubTasks[0].SubTaskID)
	}
	if got.Reason != "测试序列化" {
		t.Errorf("Reason = %s, want '测试序列化'", got.Reason)
	}
	if got.EstCost != 300 {
		t.Errorf("EstCost = %d, want 300", got.EstCost)
	}
}

// TestPlanGenerator_SerializeNilPlan 验证 SerializePlan 对 nil 返回空字符串（S5 边界 case）。
func TestPlanGenerator_SerializeNilPlan(t *testing.T) {
	t.Parallel()
	got, err := SerializePlan(nil)
	if err != nil {
		t.Fatalf("SerializePlan(nil) 不应返回 error，got: %v", err)
	}
	if got != "" {
		t.Errorf("SerializePlan(nil) 应返回空字符串，got: %s", got)
	}
}

// TestPlanGenerator_DeserializeEmptyString 验证 DeserializePlan 对空字符串返回 nil（S5 边界 case）。
func TestPlanGenerator_DeserializeEmptyString(t *testing.T) {
	t.Parallel()
	got, err := DeserializePlan("   ")
	if err != nil {
		t.Fatalf("DeserializePlan(空) 不应返回 error，got: %v", err)
	}
	if got != nil {
		t.Errorf("DeserializePlan(空) 应返回 nil，got %+v", got)
	}
}

// TestPlanGenerator_Generate_NilReceiver nil receiver 应返回 error 不 panic（S5 健壮性）。
func TestPlanGenerator_Generate_NilReceiver(t *testing.T) {
	t.Parallel()
	var p *PlanGenerator
	_, err := p.Generate(context.Background(), "test")
	if err == nil {
		t.Fatal("nil receiver Generate 应返回 error")
	}
}

// TestPlanGenerator_Generate_EmptyRequirement 空需求应返回 error（S5 健壮性）。
func TestPlanGenerator_Generate_EmptyRequirement(t *testing.T) {
	t.Parallel()
	p, _ := makeTestPlanGeneratorWithMock(nil)
	_, err := p.Generate(context.Background(), "   ")
	if err == nil {
		t.Fatal("空需求应返回 error")
	}
	if !strings.Contains(err.Error(), "requirement must not be empty") {
		t.Errorf("error 应包含 'requirement must not be empty'，got: %v", err)
	}
}
