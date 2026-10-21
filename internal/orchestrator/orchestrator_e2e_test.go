// orchestrator_e2e_test.go — H10 Phase 6 模块 5 编排器端到端测试（TDD 先行，03-实施prompt.md §5.5）。
// 覆盖：完整 clarify → plan → contract → implement 流转，验证 state.json Stage=Done + trace.jsonl 含 4 个 Stage 事件。
// §5.5 端到端测试；落地：1 个核心 E2E 测试 + 部分降级 E2E
// ；落地：E2E 含降级路径覆盖
// SNAPSHOT: tests={E2E_Orchestrate_StartToImplement, E2E_ClarifyFail_DegradeToPlan, E2E_UserReject_Interrupted}

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestE2E_Orchestrate_StartToImplement 完整 start → clarify → plan → contract → implement 流转（S5 端到端）。
// mock LLM 返回 2 个澄清问题 + 3 个子任务，mock AskUser 返回答案 + 接受方案，
// 验证最终 state.Stage=implement + 各阶段 Requirement/ClarifyAnswers/GeneratedPlanJSON 字段持久化。
func TestE2E_Orchestrate_StartToImplement(t *testing.T) {
	t.Parallel()

	// 准备 mock LLM：clarify 返回 2 问题，plan 返回 3 子任务
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		// 根据 prompt 内容区分 clarify/plan 调用
		if strings.Contains(prompt, "需求澄清助手") {
			return `{"questions":[{"id":"Q1","question":"使用什么测试框架?","options":[{"label":"标准库","value":"testing"}]},{"id":"Q2","question":"是否需要 mock?"}]}`, nil
		}
		if strings.Contains(prompt, "任务拆解助手") {
			return `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1","files_to_edit":["foo_test.go"]},{"subtask_id":"S-002","type":"implement","complexity":"T2","depends_on":["S-001"]},{"subtask_id":"S-003","type":"review","complexity":"T1","depends_on":["S-001","S-002"]}],"reason":"按模块拆分","est_cost":500}`, nil
		}
		return "", nil
	}}

	// 准备 mock AskUser：clarify 返回 2 答案，contract 返回接受
	mockAskUser := &MockAskUserGateway{
		AskFunc: func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
			return []ClarifyAnswer{
				{QuestionID: "Q1", Value: "testing"},
				{QuestionID: "Q2", Value: "是"},
			}, nil
		},
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{Accepted: true}, nil
		},
	}

	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithPlanGenerator(mockLLM, nil),
		WithContractor(mockAskUser, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)

	// Step 1: /orchestrate start "为 internal/foo 加单元测试"
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")
	t.Logf("Step 1 start: taskID=%s", taskID)

	// 验证初始 state=clarify
	state, _ := LoadState(baseDir, taskID)
	if state.Stage != StageClarify {
		t.Fatalf("Step 1 后 state.Stage = %s, want clarify", state.Stage)
	}
	if state.Requirement != "为 internal/foo 加单元测试" {
		t.Errorf("Step 1 后 Requirement = %s, want '为 internal/foo 加单元测试'", state.Requirement)
	}

	// Step 2: /orchestrate clarify <task-id>
	_, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Step 2 clarify: %v", err)
	}
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StagePlan {
		t.Fatalf("Step 2 后 state.Stage = %s, want plan", state.Stage)
	}
	if !strings.Contains(state.Requirement, "[澄清问答]") {
		t.Errorf("Step 2 后 Requirement 应含 [澄清问答] 段，got: %s", state.Requirement)
	}
	if len(state.ClarifyAnswers) != 2 {
		t.Errorf("Step 2 后 ClarifyAnswers 数 = %d, want 2", len(state.ClarifyAnswers))
	}
	t.Logf("Step 2 clarify 完成: stage=plan")

	// Step 3: /orchestrate plan <task-id>
	_, err = cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Step 3 plan: %v", err)
	}
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageContract {
		t.Fatalf("Step 3 后 state.Stage = %s, want contract", state.Stage)
	}
	if state.GeneratedPlanJSON == "" {
		t.Error("Step 3 后 GeneratedPlanJSON 不应为空")
	}
	plan, _ := DeserializePlan(state.GeneratedPlanJSON)
	if plan == nil || len(plan.SubTasks) != 3 {
		t.Errorf("Step 3 后 plan.SubTasks 数 = %d, want 3", len(plan.SubTasks))
	}
	t.Logf("Step 3 plan 完成: stage=contract, subtasks=%d", len(plan.SubTasks))

	// Step 4: /orchestrate contract <task-id>
	_, err = cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Step 4 contract: %v", err)
	}
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageImplement {
		t.Fatalf("Step 4 后 state.Stage = %s, want implement", state.Stage)
	}
	// 验证 GeneratedPlanJSON 仍保留（contract 不修改 plan）
	if state.GeneratedPlanJSON == "" {
		t.Error("Step 4 后 GeneratedPlanJSON 应保留")
	}
	t.Logf("Step 4 contract 完成: stage=implement")

	// 端到端验证：所有字段持久化
	if state.Requirement == "" {
		t.Error("E2E 后 Requirement 不应为空")
	}
	if len(state.ClarifyAnswers) != 2 {
		t.Errorf("E2E 后 ClarifyAnswers 数 = %d, want 2", len(state.ClarifyAnswers))
	}
	if state.GeneratedPlanJSON == "" {
		t.Error("E2E 后 GeneratedPlanJSON 不应为空")
	}
	t.Logf("E2E 验证完成: requirement/answers/plan 全部持久化")
}

// TestE2E_ClarifyFail_DegradeToPlan clarify LLM 失败，降级跳过 clarify 直接到 plan（S5 端到端降级）。
func TestE2E_ClarifyFail_DegradeToPlan(t *testing.T) {
	t.Parallel()

	callCount := 0
	var mu sync.Mutex
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		// 第一次调用 clarify 失败
		if strings.Contains(prompt, "需求澄清助手") {
			return "", errors.New("llm: service unavailable")
		}
		// plan 调用返回单子任务
		return `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T2"}],"reason":"降级","est_cost":300}`, nil
	}}
	mockAskUser := &MockAskUserGateway{
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{Accepted: true}, nil
		},
	}

	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithPlanGenerator(mockLLM, nil),
		WithContractor(mockAskUser, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	// clarify 失败 → 降级跳过
	_, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("clarify 失败应降级（不返回 error），got: %v", err)
	}
	state, _ := LoadState(baseDir, taskID)
	if state.Stage != StagePlan {
		t.Errorf("clarify 失败应降级进 plan，state.Stage = %s", state.Stage)
	}
	if len(state.ClarifyAnswers) != 0 {
		t.Errorf("clarify 失败时 ClarifyAnswers 应为空，got %d", len(state.ClarifyAnswers))
	}

	// plan 正常执行
	_, err = cmd.HandlePlan(context.Background(), taskID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageContract {
		t.Errorf("plan 后 state.Stage = %s, want contract", state.Stage)
	}

	// contract 接受
	_, err = cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageImplement {
		t.Errorf("contract 后 state.Stage = %s, want implement", state.Stage)
	}
}

// TestE2E_UserReject_Interrupted 用户在 contract 阶段拒绝方案，进 interrupted（S5 端到端反例）。
func TestE2E_UserReject_Interrupted(t *testing.T) {
	t.Parallel()

	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		if strings.Contains(prompt, "需求澄清助手") {
			return `{"questions":[]}`, nil // 空 questions 直接进 plan
		}
		return `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1"}],"reason":"单子任务","est_cost":200}`, nil
	}}
	mockAskUser := &MockAskUserGateway{
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{Accepted: false}, nil // 用户拒绝
		},
	}

	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithPlanGenerator(mockLLM, nil),
		WithContractor(mockAskUser, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "为 internal/foo 加单元测试")

	// clarify 空 questions 直接进 plan
	_, _ = cmd.HandleClarify(context.Background(), taskID)
	state, _ := LoadState(baseDir, taskID)
	if state.Stage != StagePlan {
		t.Fatalf("clarify 空问题应进 plan，state.Stage = %s", state.Stage)
	}

	// plan 生成单子任务
	_, _ = cmd.HandlePlan(context.Background(), taskID)
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageContract {
		t.Fatalf("plan 后应进 contract，state.Stage = %s", state.Stage)
	}

	// contract 用户拒绝 → interrupted
	_, err := cmd.HandleContract(context.Background(), taskID)
	if err != nil {
		t.Fatalf("用户拒绝不应返回 Go error，got: %v", err)
	}
	state, _ = LoadState(baseDir, taskID)
	if state.Stage != StageInterrupted {
		t.Errorf("用户拒绝应进 interrupted，state.Stage = %s", state.Stage)
	}

	// 验证 GeneratedPlanJSON 在 interrupted 状态仍保留（供审查）
	if state.GeneratedPlanJSON == "" {
		t.Error("interrupted 状态应保留 GeneratedPlanJSON 供审查")
	}
}

// TestE2E_FullLifecycle_WithTrace 完整生命周期 + trace 事件验证（S5 端到端 + trace）。
func TestE2E_FullLifecycle_WithTrace(t *testing.T) {
	t.Parallel()

	tracer := makeTestTraceStore(t)
	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		if strings.Contains(prompt, "需求澄清助手") {
			return `{"questions":[{"id":"Q1","question":"Q1"}]}`, nil
		}
		return `{"subtasks":[{"subtask_id":"S-001","type":"implement","complexity":"T1"}],"reason":"test","est_cost":100}`, nil
	}}
	mockAskUser := &MockAskUserGateway{
		AskFunc: func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
			return []ClarifyAnswer{{QuestionID: "Q1", Value: "answer"}}, nil
		},
		ConfirmFunc: func(ctx context.Context, plan *GeneratedPlan) (ConfirmResult, error) {
			return ConfirmResult{Accepted: true}, nil
		},
	}

	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, tracer),
		WithPlanGenerator(mockLLM, tracer),
		WithContractor(mockAskUser, tracer),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "test requirement")

	// clarify → plan → contract
	_, _ = cmd.HandleClarify(context.Background(), taskID)
	_, _ = cmd.HandlePlan(context.Background(), taskID)
	_, _ = cmd.HandleContract(context.Background(), taskID)

	state, _ := LoadState(baseDir, taskID)
	if state.Stage != StageImplement {
		t.Errorf("E2E 后 state.Stage = %s, want implement", state.Stage)
	}

	// 验证 trace.jsonl 含 clarify_started/completed + plan_started/completed + contract_started/completed
	events, err := tracer.QueryBySubtask("clarify")
	if err != nil {
		t.Fatalf("QueryBySubtask(clarify): %v", err)
	}
	if len(events) < 2 {
		t.Errorf("clarify trace 事件数 = %d, want >= 2（started + completed）", len(events))
	}

	planEvents, err := tracer.QueryBySubtask("plan")
	if err != nil {
		t.Fatalf("QueryBySubtask(plan): %v", err)
	}
	if len(planEvents) < 2 {
		t.Errorf("plan trace 事件数 = %d, want >= 2", len(planEvents))
	}

	contractEvents, err := tracer.QueryBySubtask("contract")
	if err != nil {
		t.Fatalf("QueryBySubtask(contract): %v", err)
	}
	if len(contractEvents) < 2 {
		t.Errorf("contract trace 事件数 = %d, want >= 2", len(contractEvents))
	}

	// 验证至少 6 个 Stage 事件（3 Stage × 2 事件）
	totalEvents := len(events) + len(planEvents) + len(contractEvents)
	if totalEvents < 6 {
		t.Errorf("总 trace 事件数 = %d, want >= 6（3 Stage × started+completed）", totalEvents)
	}
}

// TestE2E_AskUserError_Interrupted AskUser 在 clarify 阶段失败 → interrupted（S5 端到端反例）。
func TestE2E_AskUserError_Interrupted(t *testing.T) {
	t.Parallel()

	mockLLM := &mockAIClient{completeFn: func(ctx context.Context, prompt string) (string, error) {
		return `{"questions":[{"id":"Q1","question":"Q1"}]}`, nil
	}}
	mockAskUser := &MockAskUserGateway{
		AskFunc: func(ctx context.Context, questions []ClarifyQuestion) ([]ClarifyAnswer, error) {
			return nil, errors.New("tui: program exited")
		},
	}

	orch, baseDir := makeTestOrchestratorWithOptions(t,
		WithClarifier(mockLLM, nil),
		WithAskUser(mockAskUser),
	)
	cmd := NewOrchestrateCmd(orch)
	taskID := startTaskForTest(t, cmd, "test")

	_, err := cmd.HandleClarify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("AskUser 失败不应返回 Go error（应进 interrupted），got: %v", err)
	}
	state, _ := LoadState(baseDir, taskID)
	if state.Stage != StageInterrupted {
		t.Errorf("AskUser 失败应进 interrupted，state.Stage = %s", state.Stage)
	}
}

// TestE2E_Handle_Dispatch_Subcommand 通过 Orchestrator.Handle 入口分发子命令（S5 端到端入口）。
func TestE2E_Handle_Dispatch_Subcommand(t *testing.T) {
	t.Parallel()

	orch, _ := makeTestOrchestratorWithOptions(t)
	ctx := context.Background()

	// /orchestrate（无参数）
	output, err := orch.Handle(ctx, "")
	if err != nil {
		t.Fatalf("Handle empty: %v", err)
	}
	if !strings.Contains(output, "子命令") {
		t.Errorf("Handle empty 应返回帮助，got: %s", output)
	}

	// /orchestrate start
	output, err = orch.Handle(ctx, "start test requirement")
	if err != nil {
		t.Fatalf("Handle start: %v", err)
	}
	if !strings.Contains(output, "Task ID") {
		t.Errorf("Handle start 应返回 Task ID，got: %s", output)
	}

	// /orchestrate status
	output, err = orch.Handle(ctx, "status")
	if err != nil {
		t.Fatalf("Handle status: %v", err)
	}
	if !strings.Contains(output, "编排任务") {
		t.Errorf("Handle status 应返回任务列表，got: %s", output)
	}

	// /orchestrate list
	output, err = orch.Handle(ctx, "list")
	if err != nil {
		t.Fatalf("Handle list: %v", err)
	}
	if !strings.Contains(output, "编排任务") {
		t.Errorf("Handle list 应返回任务列表，got: %s", output)
	}

	// /orchestrate unknown_subcmd（应回退到帮助）
	output, err = orch.Handle(ctx, "unknown_subcmd")
	if err != nil {
		t.Fatalf("Handle unknown: %v", err)
	}
	if !strings.Contains(output, "子命令") {
		t.Errorf("Handle unknown 应回退到帮助，got: %s", output)
	}
}
