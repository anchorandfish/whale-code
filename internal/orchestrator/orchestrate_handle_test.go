// orchestrate_handle_test.go — H10 Phase 6 模块 5 HandleClarify/HandlePlan/HandleContract 集成测试（TDD 先行，03-实施prompt.md §5.4）。
// 覆盖：HandleClarify happy path / AskUser 取消进 interrupted / LLM 空问题直接进 plan /
// HandlePlan happy path / LLM 空子任务降级单子任务 /
// HandleContract accepted / rejected 进 interrupted。
// §5.4 S1 Handle 三 Stage 测试；落地：7 个核心测试
// ；落地：各 Stage 降级路径覆盖
// SNAPSHOT: tests={HandleClarify_HappyPath, HandleClarify_AskUserCancel, HandleClarify_LLMEmptyQuestions, HandleClarify_NoClarifier_Degrade, HandlePlan_HappyPath, HandlePlan_LLMEmptySubTasks, HandlePlan_LLMError_DegradeSingle, HandleContract_Accepted, HandleContract_Rejected, HandleContract_NoContractor_Skip}

package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestOrchestratorWithOptions 构造带 functional options 的测试用 Orchestrator（S5）。
// 复用 makeTestOrchestrator 的地基（QualityStore/ProfileStore/CapabilityCache），追加 Phase 6 options。
func makeTestOrchestratorWithOptions(t *testing.T, opts ...OrchestratorOption) (*Orchestrator, string) {
	t.Helper()
	baseDir := t.TempDir()
	agent := &mockN1Agent{}

	qualityPath := filepath.Join(baseDir, "orchestrator", "quality.jsonl")
	quality, err := NewQualityStore(qualityPath)
	if err != nil {
		t.Fatalf("NewQualityStore: %v", err)
	}
	profile := NewProfileStore()
	capPath := filepath.Join(baseDir, "orchestrator", "capability_cache.json")
	capability, err := NewCapabilityCache(capPath)
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}

	orch := NewOrchestrator(baseDir, agent, quality, profile, capability, BudgetModeBalanced, opts...)
	return orch, baseDir
}

// startTaskForTest 辅助函数：通过 HandleStart 创建任务并返回 task ID（S5）。
func startTaskForTest(t *testing.T, cmd *OrchestrateCmd, requirement string) string {
	t.Helper()
	output, err := cmd.HandleStart(context.Background(), requirement)
	if err != nil {
		t.Fatalf("HandleStart: %v", err)
	}
	tasks, err := cmd.scanAllTasks()
	if err != nil {
		t.Fatalf("scanAllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after start, got %d", len(tasks))
	}
	_ = output
	return tasks[0].TaskID
}

// TestHandleClarify_HappyPath 完整 clarify → plan 流转（S5）。
// mock AIClient 返回 2 个问题，mock AskUser 返回 2 个答案，验证 state 推进到 plan + Requirement 含合并答案。
func TestHandleClarify_HappyPath(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return `{"questions":[{"id":"Q1","question":"使用什么测试框架?","options":[{"label":"标准库","value":"testing"}]},{"id":"Q2","question":"是否需要 mock?"}]}`, nil
	}}
	mockAskUser := &MockAskUserGateway{
		AskFunc: func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
			return []ClarifyAnswer{
				{QuestionID: "Q1", Value: "testing"},
				{QuestionID: "Q2", Value: "是"},
			}, nil
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	output, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandleClarify: %v", err)
	}
	if !strings.Contains(output, "clarify Stage 完成") {
		t.Errorf("输出应包含 'clarify Stage 完成'，got: %s", output)
	}

	// 验证 state 推进到 plan
	state, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StagePlan {
		t.Errorf("state.Stage = %s, want %s", state.Stage, StagePlan)
	}
	// 验证 Requirement 含合并的答案
	if !strings.Contains(state.Requirement, "[澄清问答]") {
		t.Errorf("Requirement 应含 [澄清问答] 段，got: %s", state.Requirement)
	}
	if !strings.Contains(state.Requirement, "Q1: testing") {
		t.Errorf("Requirement 应含 Q1 答案，got: %s", state.Requirement)
	}
	// 验证 ClarifyAnswers 持久化
	if len(state.ClarifyAnswers) != 2 {
		t.Errorf("ClarifyAnswers 数 = %d, want 2", len(state.ClarifyAnswers))
	}
}

// TestHandleClarify_AskUserCancel 用户取消，进 StageInterrupted（S5）。
// mock AIClient 返回问题，mock AskUser 返回 error，验证 state 推进到 interrupted。
func TestHandleClarify_AskUserCancel(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return `{"questions":[{"id":"Q1","question":"Q1"}]}`, nil
	}}
	mockAskUser := &MockAskUserGateway{
		AskFunc: func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
			return nil, errors.New("user cancelled")
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	output, err := cmd.HandleClarify(context.Background(), taskID)
	// 不应返回 Go error（用户取消是业务流程，进 interrupted）
	if err != nil {
		t.Fatalf("HandleClarify AskUser 取消不应返回 Go error，got: %v", err)
	}
	if !strings.Contains(output, "interrupted") {
		t.Errorf("输出应包含 'interrupted'，got: %s", output)
	}

	state, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StageInterrupted {
		t.Errorf("state.Stage = %s, want %s", state.Stage, StageInterrupted)
	}
}

// TestHandleClarify_LLMEmptyQuestions LLM 空 questions，直接进 plan（S5 边界 case）。
// mock AIClient 返回不含 JSON 的输出（视为 0 个问题），验证 state 直接推进到 plan。
func TestHandleClarify_LLMEmptyQuestions(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return "抱歉，无法生成问题", nil
	}}
	mockAskUser := &MockAskUserGateway{
		AskFunc: func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
			t.Fatal("空 questions 时不应调用 AskUser")
			return nil, nil
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	output, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandleClarify: %v", err)
	}
	if !strings.Contains(output, "Stage 跳转") && !strings.Contains(output, "0 个问题") {
		t.Errorf("输出应提示跳转/0 个问题，got: %s", output)
	}

	state, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StagePlan {
		t.Errorf("state.Stage = %s, want %s（空问题应直接进 plan）", state.Stage, StagePlan)
	}
}

// TestHandleClarify_LLMError_DegradeToPlan LLM 调用失败，降级跳过 clarify 直接进 plan（S5）。
func TestHandleClarify_LLMError_DegradeToPlan(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return "", errors.New("llm: timeout")
	}}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	_, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LLM 失败应降级跳过 clarify（不返回 error），got: %v", err)
	}

	state, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StagePlan {
		t.Errorf("LLM 失败应降级进 plan，state.Stage = %s, want %s", state.Stage, StagePlan)
	}
}

// TestHandleClarify_NoClarifier_DegradeToPlan clarifier 未注入，跳过 clarify 直接进 plan（S5 降级）。
func TestHandleClarify_NoClarifier_DegradeToPlan(t *testing.T) {
	t.Parallel()
	orch, baseDir := makeTestOrchestratorWithOptions(t) // 无 options
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	_, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("clarifier 未注入应降级跳过 clarify，got: %v", err)
	}

	state, err := LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StagePlan {
		t.Errorf("clarifier 未注入应降级进 plan，state.Stage = %s, want %s", state.Stage, StagePlan)
	}
}

// TestHandleClarify_WrongStage 非 clarify 阶段调用应拒绝（S5 健壮性）。
func TestHandleClarify_WrongStage(t *testing.T) {
	t.Parallel()
	orch, _ := makeTestOrchestratorWithOptions(t)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	// 手动推进到 plan 阶段
	state, _ := LoadState(orch.baseDir, taskID)
	state.Stage = StagePlan
	_ = SaveState(orch.baseDir, taskID, *state)

	output, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandleClarify wrong stage 不应返回 Go error，got: %v", err)
	}
	if !strings.Contains(output, "无法执行 clarify") {
		t.Errorf("输出应提示无法执行 clarify，got: %s", output)
	}
}

// TestHandleClarify_EmptyTaskID 空 taskID 应返回 error（S5 健壮性）。
func TestHandleClarify_EmptyTaskID(t *testing.T) {
	t.Parallel()
	orch, _ := makeTestOrchestratorWithOptions(t)
	cmd := NewOrchestrateCmd(orch)

	_, err := cmd.HandleClarify(context.Background(), "  ")
	if err == nil {
		t.Fatal("空 taskID 应返回 error")
	}
}

// TestHandlePlan_HappyPath 完整 plan → contract 流转（S5）。
// mock AIClient 返回 3 个子任务，验证 state 推进到 contract + GeneratedPlanJSON 非空。
func TestHandlePlan_HappyPath(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1"},{"subtask_id":"S-002","type":"implement","complexity":"T2","depends_on":["S-001"]},{"subtask_id":"S-003","type":"review","complexity":"T1","depends_on":["S-001","S-002"]}],"reason":"按模块拆分","est_cost":500}`, nil
	}}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithPlanGenerator(mockLLM, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	// 先推进到 plan 阶段（手动跳过 clarify）
	state, _ := LoadState(baseDir, taskID)
	state.Stage = StagePlan
	_ = SaveState(baseDir, taskID, *state)

	output, err := cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandlePlan: %v", err)
	}
	if !strings.Contains(output, "plan Stage 完成") {
		t.Errorf("输出应包含 'plan Stage 完成'，got: %s", output)
	}
	if !strings.Contains(output, "子任务数: 3") {
		t.Errorf("输出应包含 '子任务数: 3'，got: %s", output)
	}

	// 验证 state 推进到 contract
	state, err = LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StageContract {
		t.Errorf("state.Stage = %s, want %s", state.Stage, StageContract)
	}
	if state.GeneratedPlanJSON == "" {
		t.Error("GeneratedPlanJSON 不应为空")
	}
	// 验证 plan 可反序列化
	plan, err := DeserializePlan(state.GeneratedPlanJSON)
	if err != nil {
		t.Fatalf("DeserializePlan: %v", err)
	}
	if len(plan.SubTasks) != 3 {
		t.Errorf("plan.SubTasks 数 = %d, want 3", len(plan.SubTasks))
	}
}

// TestHandlePlan_LLMEmptySubTasks LLM 返回空 SubTasks，降级单子任务（S5 边界 case）。
func TestHandlePlan_LLMEmptySubTasks(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return "抱歉，无法拆分任务", nil
	}}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithPlanGenerator(mockLLM, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StagePlan
	_ = SaveState(baseDir, taskID, *state)

	output, err := cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandlePlan: %v", err)
	}
	if !strings.Contains(output, "降级") {
		t.Errorf("输出应提示降级，got: %s", output)
	}

	state, err = LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StageContract {
		t.Errorf("state.Stage = %s, want %s", state.Stage, StageContract)
	}
	// 验证降级为单子任务
	plan, _ := DeserializePlan(state.GeneratedPlanJSON)
	if plan == nil || len(plan.SubTasks) != 1 {
		t.Errorf("降级方案应有 1 个子任务，got %v", plan)
	}
}

// TestHandlePlan_LLMError_DegradeSingle LLM 失败降级单子任务（S5）。
func TestHandlePlan_LLMError_DegradeSingle(t *testing.T) {
	t.Parallel()
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return "", errors.New("llm: rate limit")
	}}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithPlanGenerator(mockLLM, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StagePlan
	_ = SaveState(baseDir, taskID, *state)

	_, err := cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LLM 失败应降级单子任务（不返回 error），got: %v", err)
	}

	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageContract {
		t.Errorf("LLM 失败应降级进 contract，state.Stage = %s", state.Stage)
	}
	plan, _ := DeserializePlan(state.GeneratedPlanJSON)
	if plan == nil || len(plan.SubTasks) != 1 {
		t.Errorf("LLM 失败应降级为 1 个子任务，got %v", plan)
	}
}

// TestHandlePlan_NoPlanGenerator_DegradeSingle planGenerator 未注入降级单子任务（S5 降级）。
func TestHandlePlan_NoPlanGenerator_DegradeSingle(t *testing.T) {
	t.Parallel()
	orch, baseDir := makeTestOrchestratorWithOptions(t) // 无 options
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StagePlan
	_ = SaveState(baseDir, taskID, *state)

	_, err := cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("planGenerator 未注入应降级单子任务，got: %v", err)
	}

	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageContract {
		t.Errorf("planGenerator 未注入应降级进 contract，state.Stage = %s", state.Stage)
	}
	plan, _ := DeserializePlan(state.GeneratedPlanJSON)
	if plan == nil || len(plan.SubTasks) != 1 {
		t.Errorf("planGenerator 未注入应降级为 1 个子任务，got %v", plan)
	}
}

// TestHandleContract_Accepted 用户接受方案，推进到 implement（S5）。
func TestHandleContract_Accepted(t *testing.T) {
	t.Parallel()
	mockAskUser := &MockAskUserGateway{
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{Accepted: true}, nil
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithContractor(mockAskUser, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	// 手动推进到 contract 阶段（含 GeneratedPlanJSON）
	state, _ := LoadState(baseDir, taskID)
	state.Stage = StageContract
	plan := (&PlanGenerator{}).DegradeToSingleTask("test")
	planJSON, _ := SerializePlan(plan)
	state.GeneratedPlanJSON = planJSON
	_ = SaveState(baseDir, taskID, *state)

	output, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("HandleContract accepted: %v", err)
	}
	if !strings.Contains(output, "contract Stage 完成") {
		t.Errorf("输出应包含 'contract Stage 完成'，got: %s", output)
	}

	state, err = LoadState(baseDir, taskID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Stage != StageImplement {
		t.Errorf("state.Stage = %s, want %s", state.Stage, StageImplement)
	}
}

// TestHandleContract_Rejected 用户拒绝方案，进 StageInterrupted（S5）。
func TestHandleContract_Rejected(t *testing.T) {
	t.Parallel()
	mockAskUser := &MockAskUserGateway{
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{Accepted: false}, nil
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithContractor(mockAskUser, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StageContract
	plan := (&PlanGenerator{}).DegradeToSingleTask("test")
	planJSON, _ := SerializePlan(plan)
	state.GeneratedPlanJSON = planJSON
	_ = SaveState(baseDir, taskID, *state)

	output, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("用户拒绝不应返回 Go error（应进 interrupted），got: %v", err)
	}
	if !strings.Contains(output, "interrupted") {
		t.Errorf("输出应包含 'interrupted'，got: %s", output)
	}

	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageInterrupted {
		t.Errorf("用户拒绝应进 interrupted，state.Stage = %s", state.Stage)
	}
}

// TestHandleContract_Timeout 用户超时，进 StageInterrupted（S5）。
func TestHandleContract_Timeout(t *testing.T) {
	t.Parallel()
	mockAskUser := &MockAskUserGateway{
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{}, context.DeadlineExceeded
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithContractor(mockAskUser, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StageContract
	plan := (&PlanGenerator{}).DegradeToSingleTask("test")
	planJSON, _ := SerializePlan(plan)
	state.GeneratedPlanJSON = planJSON
	_ = SaveState(baseDir, taskID, *state)

	_, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("超时应进 interrupted（不返回 Go error），got: %v", err)
	}

	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageInterrupted {
		t.Errorf("超时应进 interrupted，state.Stage = %s", state.Stage)
	}
}

// TestHandleContract_NoContractor_Skip contractor 未注入跳过确认直接进 implement（S5 降级）。
func TestHandleContract_NoContractor_Skip(t *testing.T) {
	t.Parallel()
	orch, baseDir := makeTestOrchestratorWithOptions(t) // 无 options
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StageContract
	plan := (&PlanGenerator{}).DegradeToSingleTask("test")
	planJSON, _ := SerializePlan(plan)
	state.GeneratedPlanJSON = planJSON
	_ = SaveState(baseDir, taskID, *state)

	_, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("contractor 未注入应跳过确认，got: %v", err)
	}

	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageImplement {
		t.Errorf("contractor 未注入应直接进 implement，state.Stage = %s", state.Stage)
	}
}

// TestHandleContract_NilPlan_Skip plan 为空时跳过 contract 直接进 implement（S5 边界 case）。
func TestHandleContract_NilPlan_Skip(t *testing.T) {
	t.Parallel()
	mockAskUser := &MockAskUserGateway{
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			t.Fatal("plan 为空时不应调用 ConfirmContract")
			return ConfirmResult{}, nil
		},
	}
	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithContractor(mockAskUser, nil),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	state, _ := LoadState(baseDir, taskID)
	state.Stage = StageContract
	state.GeneratedPlanJSON = "" // 空 plan
	_ = SaveState(baseDir, taskID, *state)

	_, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("plan 为空应跳过 contract，got: %v", err)
	}

	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageImplement {
		t.Errorf("plan 为空应直接进 implement，state.Stage = %s", state.Stage)
	}
}

// TestHandleContract_WrongStage 非 contract 阶段调用应拒绝（S5 健壮性）。
func TestHandleContract_WrongStage(t *testing.T) {
	t.Parallel()
	orch, _ := makeTestOrchestratorWithOptions(t)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "test")

	// 任务初始为 clarify 阶段
	output, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("wrong stage 不应返回 Go error，got: %v", err)
	}
	if !strings.Contains(output, "无法执行 contract") {
		t.Errorf("输出应提示无法执行 contract，got: %s", output)
	}
}

// TestHandlePlan_WrongStage 非 plan 阶段调用应拒绝（S5 健壮性）。
func TestHandlePlan_WrongStage(t *testing.T) {
	t.Parallel()
	orch, _ := makeTestOrchestratorWithOptions(t)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "test")

	// 任务初始为 clarify 阶段
	output, err := cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("wrong stage 不应返回 Go error，got: %v", err)
	}
	if !strings.Contains(output, "无法执行 plan") {
		t.Errorf("输出应提示无法执行 plan，got: %s", output)
	}
}

// TestHandle_NonexistentTaskID 不存在的 task 应返回提示（S5 健壮性）。
func TestHandle_NonexistentTaskID(t *testing.T) {
	t.Parallel()
	orch, _ := makeTestOrchestratorWithOptions(t)
	cmd := NewOrchestrateCmd(orch)

	output, err := cmd.HandleClarify(context.Background(), "T-nonexistent")
	if err != nil {
		t.Fatalf("不存在的 task 不应返回 Go error，got: %v", err)
	}
	if !strings.Contains(output, "不存在") {
		t.Errorf("输出应提示 '不存在'，got: %s", output)
	}
}
