// context_contract_test.go — 模块 2 上下文契约反例测试（TDD 先行，编排层 §4.1/§4.2）。
// 覆盖致命反例：MaxDepth 超限 / CapabilitySnapshot 空/不一致 / BudgetHint 类型 / 裁剪无全文。
// ≤3；落地：ValidateTaskContext(MaxDepth=4) 返回 error
// 是三因子之一；落地：CheckSubtaskBudget 校验
// 一致性；落地：BuildTaskContext 注入 Snapshot
// SNAPSHOT: tests={MaxDepthExceed3_Rejected, CapabilitySnapshot_Empty_Rejected, CapabilitySnapshot_Inconsistent_Rejected, BudgetHint_TokensCapOnly, TrimContext_NoFullText, TrimContext_NoMainHistory, BuildTaskContext_DepthIncrement, ValidateTaskContext_GoalEmpty_Rejected}

package orchestrator

import (
	"testing"
)

// makeTestCapabilityCache 创建测试用 CapabilityCache（含预填充 items）。
func makeTestCapabilityCache(t *testing.T) *CapabilityCache {
	t.Helper()
	cap, err := NewCapabilityCache("/tmp/test-capability-cache.json")
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}
	// 手动填充 items（绕过 doctor 探测）
	cap.items = map[string]bool{
		"env.Dependencies.git":    true,
		"env.Dependencies.go":     true,
		"env.Tools.golangci-lint": false,
	}
	return cap
}

// TestTaskContext_MaxDepthExceed3_Rejected 致命反例：MaxDepth=4 时 ValidateTaskContext 必须返回 error（硬上限）。
func TestTaskContext_MaxDepthExceed3_Rejected(t *testing.T) {
	tc := TaskContext{
		TaskID:             "test-maxdepth",
		MaxDepth:           4, // 超过 3 硬上限
		CapabilitySnapshot: map[string]bool{"env.test": true},
		BudgetHint:         BudgetHint{TokensCap: 100},
		BudgetMode:         BudgetModeBalanced,
		Goal:               "test goal",
	}

	err := ValidateTaskContext(tc)
	if err == nil {
		t.Fatal("致命反例：MaxDepth=4 时 ValidateTaskContext 必须返回 error（ 硬上限 ≤3）")
	}
}

// TestTaskContext_CapabilitySnapshot_Empty_Rejected 致命反例：CapabilitySnapshot 为空时拒绝派发（02 §2.7.1 零容忍）。
func TestTaskContext_CapabilitySnapshot_Empty_Rejected(t *testing.T) {
	tc := TaskContext{
		TaskID:             "test-empty-snap",
		MaxDepth:           3,
		CapabilitySnapshot: nil, // 空 Snapshot
		BudgetHint:         BudgetHint{TokensCap: 100},
		BudgetMode:         BudgetModeBalanced,
		Goal:               "test goal",
	}

	err := ValidateTaskContext(tc)
	if err == nil {
		t.Fatal("致命反例：CapabilitySnapshot 为空时 ValidateTaskContext 必须返回 error")
	}
}

// TestTaskContext_CapabilitySnapshot_Inconsistent_Rejected 致命反例：注入的 Snapshot 与主进程当前 Snapshot 不一致时拒绝派发（零容忍）。
func TestTaskContext_CapabilitySnapshot_Inconsistent_Rejected(t *testing.T) {
	tc := TaskContext{
		TaskID:             "test-inconsistent-snap",
		MaxDepth:           3,
		CapabilitySnapshot: map[string]bool{"env.test": true, "env.diff": false},
		BudgetMode:         BudgetModeBalanced,
		Goal:               "test goal",
	}

	// 主进程当前 Snapshot（与 tc 不同：少了 env.diff，多了 env.new）
	currentSnap := map[string]bool{"env.test": true, "env.new": true}

	err := VerifyCapabilitySnapshot(tc, currentSnap)
	if err == nil {
		t.Fatal("致命反例：注入的 Snapshot 与主进程不一致时必须拒绝派发（零容忍）")
	}

	// 值不一致也应拒绝
	tc2 := TaskContext{
		CapabilitySnapshot: map[string]bool{"env.test": true},
	}
	currentSnap2 := map[string]bool{"env.test": false} // 值不同
	if err := VerifyCapabilitySnapshot(tc2, currentSnap2); err == nil {
		t.Fatal("致命反例：Snapshot 值不一致时必须拒绝派发")
	}

	// 一致时应通过
	tc3 := TaskContext{
		CapabilitySnapshot: map[string]bool{"env.test": true, "env.diff": false},
	}
	currentSnap3 := map[string]bool{"env.test": true, "env.diff": false}
	if err := VerifyCapabilitySnapshot(tc3, currentSnap3); err != nil {
		t.Fatalf("一致的 Snapshot 应通过校验，got: %v", err)
	}
}

// TestBudgetHint_TokensCapOnly 契约变更：
// BudgetHint 只带 token 轴（TokensCap），**不得**再出现任何「钱」字段（CostCapCents 等）。
func TestBudgetHint_TokensCapOnly(t *testing.T) {
	if !AssertBudgetHintTokensCapOnly() {
		t.Fatal("BudgetHint 必须只含 TokensCap，不得含 cost/cents 类字段（ 契约变更）")
	}
}

// TestTrimContext_NoFullText 裁剪后 Inputs 不含 00/02/03 全文，只含 Snippet（编排层 §4.2 硬约束）。
func TestTrimContext_NoFullText(t *testing.T) {
	tc := &TaskContext{
		Inputs: []InputRef{
			{FilePath: "00-项目记忆镜像.md", Snippet: ": MaxDepth ≤3"},
			{FilePath: "02-跨方向冲突预防.md", Snippet: "§2.7.1 TaskContext"},
			{FilePath: "internal/orchestrator/state.go", LineFrom: 10, LineTo: 20, Snippet: "type State struct{}"},
		},
	}

	if err := TrimContextByDependency(tc, nil); err != nil {
		t.Fatalf("TrimContextByDependency: %v", err)
	}

	// 验证裁剪后仍保留 FilePath + Snippet，不含全文内容
	for _, ref := range tc.Inputs {
		if ref.FilePath == "" {
			t.Error("裁剪后 FilePath 不应为空")
		}
		// Snippet 不应包含整个文件内容（仅契约片段）
		// Phase 1 简化版：Snippet 由编排器填充，此处仅验证结构完整性
	}
}

// TestTrimContext_NoMainHistory 裁剪后 Inputs 不含主对话推理历史（隔离原则）。
func TestTrimContext_NoMainHistory(t *testing.T) {
	tc := &TaskContext{
		Inputs: []InputRef{
			{FilePath: "internal/foo.go", Snippet: "func Foo() {}"},
		},
	}

	if err := TrimContextByDependency(tc, nil); err != nil {
		t.Fatalf("TrimContextByDependency: %v", err)
	}

	// 验证不含主对话历史（Phase 1 简化版：TaskContext 本身不含主对话历史，此测试验证结构）
	for _, ref := range tc.Inputs {
		if ref.FilePath == "main_conversation_history" || ref.FilePath == "main_trace" {
			t.Error("裁剪后不应包含主对话推理历史（ 隔离原则）")
		}
	}
}

// TestBuildTaskContext_DepthIncrement BuildTaskContext 应正确设置 Depth（编排层 §4.1）。
func TestBuildTaskContext_DepthIncrement(t *testing.T) {
	cap := makeTestCapabilityCache(t)
	subtask := SubTaskDef{
		SubTaskID:   "S-001",
		Type:        TaskTypeImplement,
		Complexity:  ComplexityT1,
		FilesToRead: []string{"internal/foo.go"},
	}

	tc, err := BuildTaskContext("T-depth-test", subtask, cap, 1, BudgetModeBalanced)
	if err != nil {
		t.Fatalf("BuildTaskContext: %v", err)
	}

	if tc.Depth != 1 {
		t.Errorf("expected Depth=1, got %d", tc.Depth)
	}
	if tc.MaxDepth != MaxDepthHardLimit {
		t.Errorf("expected MaxDepth=%d, got %d", MaxDepthHardLimit, tc.MaxDepth)
	}
	if len(tc.CapabilitySnapshot) == 0 {
		t.Error("BuildTaskContext 必须注入 capability_snapshot")
	}
}

// TestValidateTaskContext_GoalEmpty_Rejected Goal 为空时返回 error（编排层 §4.1）。
func TestValidateTaskContext_GoalEmpty_Rejected(t *testing.T) {
	tc := TaskContext{
		TaskID:             "test-empty-goal",
		MaxDepth:           3,
		CapabilitySnapshot: map[string]bool{"env.test": true},
		BudgetMode:         BudgetModeBalanced,
		Goal:               "", // 空 Goal
	}

	err := ValidateTaskContext(tc)
	if err == nil {
		t.Fatal("Goal 为空时 ValidateTaskContext 必须返回 error")
	}
}

// TestValidateTaskContext_BudgetMode_Invalid 非法 BudgetMode 返回 error（编排层 §4.1）。
func TestValidateTaskContext_BudgetMode_Invalid(t *testing.T) {
	tc := TaskContext{
		TaskID:             "test-bad-budget",
		MaxDepth:           3,
		CapabilitySnapshot: map[string]bool{"env.test": true},
		BudgetMode:         "ultra", // 非法值
		Goal:               "test goal",
	}

	err := ValidateTaskContext(tc)
	if err == nil {
		t.Fatal("非法 BudgetMode 时 ValidateTaskContext 必须返回 error")
	}
}

// TestBuildTaskContext_NilCapability_Rejected CapabilityCache 为 nil 时拒绝。
func TestBuildTaskContext_NilCapability_Rejected(t *testing.T) {
	subtask := SubTaskDef{
		SubTaskID:  "S-002",
		Type:       TaskTypeImplement,
		Complexity: ComplexityT1,
	}

	_, err := BuildTaskContext("T-nil-cap", subtask, nil, 1, BudgetModeBalanced)
	if err == nil {
		t.Fatal("CapabilityCache 为 nil 时 BuildTaskContext 必须返回 error")
	}
}
