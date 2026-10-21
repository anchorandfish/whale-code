// context_contract.go — 模块 2 上下文契约 TaskContext + BudgetHint + InputRef + Boundary + 上下文裁剪（H10 Phase 1 编排层 §4.1/§4.2）。
// 字段定义；落地：TaskID/MaxDepth/CapabilitySnapshot/BudgetHint/ParentTaskID 5 字段与 02 完全一致
// 七要素；落地：Goal/Inputs/Boundary/Tools/Checks 对应总纲七要素
// ≤3；落地：MaxDepth int 字段 + ValidateTaskContext 校验
// （BudgetHint 是因子3）；落地：BudgetHint struct + BudgetMode 扩展字段
// 一致性；落地：BuildTaskContext 调 CapabilityCache.Snapshot() 注入
// SNAPSHOT: pkg=internal/orchestrator; type=TaskContext; 02fields={TaskID,MaxDepth,CapabilitySnapshot,BudgetHint,ParentTaskID}; extfields={Depth,PromptVersion,BudgetMode,Goal,Inputs,Boundary,Tools,Checks}; MaxDepth≤3; BudgetHint={TokensCap:int}( 2026-10-09: CostCapCents 已删); BudgetMode=扩展字段(economy/balanced/premium); capability_snapshot=map[string]bool(子任务只读,与主进程一致)

package orchestrator

import (
	"fmt"
	"reflect"
	"strings"
)

// BudgetMode 枚举常量（三因子之一，编排层 §4.1 扩展字段）。
const (
	BudgetModeEconomy  = "economy"
	BudgetModeBalanced = "balanced"
	BudgetModePremium  = "premium"
)

// TaskContext 上下文契约（H10 外层包装写入 input.json，U2 决策，编排层 §4.1）。
// 字段严格对齐 02 §2.7.1：TaskID/MaxDepth/CapabilitySnapshot/BudgetHint/ParentTaskID 5 字段不可变更。
// 扩展字段（Depth/PromptVersion/BudgetMode/Goal/Inputs/Boundary/Tools/Checks）在编排层 §4.1 补全。
// Phase 4 新增字段（PriorSolution/PriorFiles/ExperienceHit）用于经验库注入，omitempty 向后兼容。
type TaskContext struct {
	// === 02 §2.7.1 必填字段（不可变更） ===
	TaskID             string          `json:"task_id"`
	MaxDepth           int             `json:"max_depth"`
	CapabilitySnapshot map[string]bool `json:"capability_snapshot"`
	BudgetHint         BudgetHint      `json:"budget_hint"`
	ParentTaskID       string          `json:"parent_task_id,omitempty"`

	// === 总纲 §5.2 扩展字段（编排层 §4.1 补全） ===
	Depth         int    `json:"depth"`
	PromptVersion string `json:"prompt_version"`
	BudgetMode    string `json:"budget_mode"`

	// === 任务包字段（总纲 §5.2 七要素） ===
	Goal     string     `json:"goal"`
	Inputs   []InputRef `json:"inputs"`
	Boundary Boundary   `json:"boundary"`
	Tools    []string   `json:"tools"`
	Checks   []string   `json:"checks"`

	// === Phase 4 §3.1.3b 经验库注入字段（omitempty 向后兼容） ===
	// PriorSolution 命中 pattern 的 Steps（子任务感知先验解法）
	// PriorFiles 命中 pattern 的 FilesTouched（子任务感知先验涉及文件）
	// ExperienceHit 命中信息（pattern_id + confidence + verified_state），router 第四因子据此降级
	PriorSolution []Step         `json:"prior_solution,omitempty"`
	PriorFiles    []string       `json:"prior_files,omitempty"`
	ExperienceHit *ExperienceHit `json:"experience_hit,omitempty"`

	// Model 子任务执行模型（router.SelectModel 写入，子任务派发时使用）
	Model string `json:"model,omitempty"`
}

// BudgetHint 子任务硬预算（02 §2.7.1 契约字段）。
//
// ★ 2026-10-09（，契约变更）：**删除 `CostCapCents`** —— 预算轴收敛到唯一的一根：
// provider 真报的 token（「不算价钱」铁律，2026-10-05 拍板）。契约文档 02 §2.7.1 同步。
type BudgetHint struct {
	TokensCap int `json:"tokens_cap"`
}

// InputRef 输入引用（精确到行，编排层 §4.1）。
type InputRef struct {
	FilePath string `json:"file_path"`
	LineFrom int    `json:"line_from,omitempty"`
	LineTo   int    `json:"line_to,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
}

// Boundary 边界（白名单 + 黑名单，编排层 §4.1）。
type Boundary struct {
	CanEdit    []string `json:"can_edit"`
	CannotEdit []string `json:"cannot_edit"`
}

// SubTaskDef 子任务定义（路由器/调度器输入，编排层 §3.2.2）。
// 与 Phase 0 SubTask（执行时使用）区分：SubTaskDef 是派发前的定义，SubTask 是执行时的载体。
type SubTaskDef struct {
	SubTaskID   string   `json:"subtask_id"`
	Type        string   `json:"type"`
	Complexity  string   `json:"complexity"`
	FilesToRead []string `json:"files_to_read"`
	FilesToEdit []string `json:"files_to_edit"`
	DependsOn   []string `json:"depends_on"`
}

// ImplementOutput implement Stage 输出（编排层 §3.2.4）。
type ImplementOutput struct {
	CodeDiff     string   `json:"code_diff"`
	TransferDoc  string   `json:"transfer_doc"`
	FilesTouched []string `json:"files_touched"`
	L0Pass       bool     `json:"l0_pass"`
	L1Score      int      `json:"l1_score"`
	ErrorType    string   `json:"error_type"`
}

// DependencyGraph 依赖图占位声明（完整实现在 scheduler.go 模块 6）。
// 此处仅声明 TrimContextByDependency 的参数类型，避免循环依赖。
// Phase 1 单串行，依赖图退化为线性序列（编排层 §3.1）。

// BuildTaskContext 编排器生成 TaskContext（含依赖图裁剪 + capability_snapshot 注入，编排层 §4.1/§4.2）。
// 必须调用 CapabilityCache.Snapshot() 注入 capability_snapshot，确保与主进程一致（02 §2.7.1，零容忍）。
func BuildTaskContext(taskID string, subtask SubTaskDef, cap *CapabilityCache, depth int, budgetMode string) (TaskContext, error) {
	if cap == nil {
		return TaskContext{}, fmt.Errorf("context_contract: CapabilityCache must not be nil")
	}

	// 调用 Snapshot() 注入 capability_snapshot（与主进程一致，零容忍）
	snapshot := cap.Snapshot()
	if len(snapshot) == 0 {
		return TaskContext{}, fmt.Errorf("context_contract: capability_snapshot is empty (run ProbeViaDoctor first)")
	}

	tc := TaskContext{
		TaskID:             taskID,
		MaxDepth:           MaxDepthHardLimit, // ≤3 硬上限
		CapabilitySnapshot: snapshot,
		BudgetHint:         defaultBudgetHint(budgetMode),
		Depth:              depth,
		BudgetMode:         budgetMode,
		Goal:               fmt.Sprintf("执行子任务 %s（类型: %s, 复杂度: %s）", subtask.SubTaskID, subtask.Type, subtask.Complexity),
		Inputs:             buildInputsFromSubtask(subtask),
		Boundary: Boundary{
			CanEdit:    subtask.FilesToEdit,
			CannotEdit: []string{}, // 由编排器按需填充
		},
		Tools:  defaultToolsForType(subtask.Type),
		Checks: defaultChecksForType(subtask.Type),
	}

	if err := ValidateTaskContext(tc); err != nil {
		return TaskContext{}, fmt.Errorf("context_contract: ValidateTaskContext: %w", err)
	}

	return tc, nil
}

// ValidateTaskContext 校验 TaskContext 合法性（MaxDepth ≤3 / CapabilitySnapshot 非空 / Goal 非空，编排层 §4.1）。
func ValidateTaskContext(tc TaskContext) error {
	// MaxDepth ≤3 硬上限
	if tc.MaxDepth > MaxDepthHardLimit {
		return fmt.Errorf("context_contract: MaxDepth %d exceeds hard limit %d", tc.MaxDepth, MaxDepthHardLimit)
	}
	// CapabilitySnapshot 非空（02 §2.7.1 零容忍）
	if len(tc.CapabilitySnapshot) == 0 {
		return fmt.Errorf("context_contract: CapabilitySnapshot must not be empty (02 §2.7.1)")
	}
	// Goal 非空
	if tc.Goal == "" {
		return fmt.Errorf("context_contract: Goal must not be empty")
	}
	// TaskID 非空
	if tc.TaskID == "" {
		return fmt.Errorf("context_contract: TaskID must not be empty")
	}
	// BudgetMode 合法性
	switch tc.BudgetMode {
	case BudgetModeEconomy, BudgetModeBalanced, BudgetModePremium:
		// 合法
	default:
		return fmt.Errorf("context_contract: invalid BudgetMode %q (must be economy/balanced/premium)", tc.BudgetMode)
	}
	return nil
}

// VerifyCapabilitySnapshot 校验注入的 Snapshot 与主进程当前 Snapshot 是否一致（零容忍，编排层 §4.1）。
// 派发时若 CapabilitySnapshot 与主进程当前 Snapshot 不一致 → 拒绝派发并返回 error。
func VerifyCapabilitySnapshot(tc TaskContext, currentSnapshot map[string]bool) error {
	if len(currentSnapshot) == 0 {
		return fmt.Errorf("context_contract: currentSnapshot must not be empty")
	}
	if len(tc.CapabilitySnapshot) != len(currentSnapshot) {
		return fmt.Errorf("context_contract: capability_snapshot size mismatch (tc=%d, current=%d)", len(tc.CapabilitySnapshot), len(currentSnapshot))
	}
	// 逐键比较（map 深度比较）
	for k, v := range tc.CapabilitySnapshot {
		cv, ok := currentSnapshot[k]
		if !ok {
			return fmt.Errorf("context_contract: capability_snapshot key %q missing in current snapshot", k)
		}
		if v != cv {
			return fmt.Errorf("context_contract: capability_snapshot key %q value mismatch (tc=%v, current=%v)", k, v, cv)
		}
	}
	return nil
}

// TrimContextByDependency 按依赖图裁剪上下文（编排层 §4.2，三步：依赖图分析 + import 分析 + 路径精确化）。
// Phase 1 简化版：depGraph 为 nil 时跳过依赖图分析，仅做基本裁剪（去除全文引用，仅保留 Snippet）。
func TrimContextByDependency(tc *TaskContext, depGraph *DependencyGraph) error {
	if tc == nil {
		return fmt.Errorf("context_contract: TaskContext must not be nil")
	}

	// Phase 1 简化裁剪：确保 Inputs 不含全文引用（仅含 Snippet，编排层 §4.2 硬约束）
	trimmed := make([]InputRef, 0, len(tc.Inputs))
	for _, ref := range tc.Inputs {
		// 如果 Snippet 为空但有 FilePath，保留引用（子任务自行读取）
		// 如果 Snippet 非空，仅传 Snippet 不传全文
		trimmedRef := InputRef{
			FilePath: ref.FilePath,
			LineFrom: ref.LineFrom,
			LineTo:   ref.LineTo,
			Snippet:  ref.Snippet,
		}
		trimmed = append(trimmed, trimmedRef)
	}
	tc.Inputs = trimmed

	// 依赖图分析（Phase 3 扩展为完整 DAG，Phase 1 depGraph 可为 nil）
	if depGraph != nil {
		// Phase 1 单串行：依赖图退化为线性序列，不额外裁剪
		// Phase 3 扩展为按依赖图仅注入同桶 + 上游桶的契约章节
	}

	return nil
}

// defaultBudgetHint 根据 budgetMode 返回默认 BudgetHint（三因子之一）。
// ★ 2026-10-09：只给 token 上限（原先同时给 CostCapCents 的美分预设已删除）。
func defaultBudgetHint(budgetMode string) BudgetHint {
	switch budgetMode {
	case BudgetModeEconomy:
		return BudgetHint{TokensCap: 50000}
	case BudgetModePremium:
		return BudgetHint{TokensCap: 500000}
	default: // BudgetModeBalanced
		return BudgetHint{TokensCap: 200000}
	}
}

// buildInputsFromSubtask 从 SubTaskDef 构造 Inputs（编排层 §4.2）。
func buildInputsFromSubtask(subtask SubTaskDef) []InputRef {
	inputs := make([]InputRef, 0, len(subtask.FilesToRead))
	for _, f := range subtask.FilesToRead {
		inputs = append(inputs, InputRef{
			FilePath: f,
			// LineFrom/LineTo/Snippet 由编排器按依赖图裁剪时填充
		})
	}
	return inputs
}

// defaultToolsForType 根据 task type 返回默认工具列表（编排层 §4.1）。
func defaultToolsForType(taskType string) []string {
	switch taskType {
	case TaskTypePlan, "explore":
		return []string{"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch"}
	case TaskTypeReview:
		return []string{"read_file", "glob_search", "grep_search", "bash"}
	default:
		return []string{"read_file", "glob_search", "grep_search", "bash", "write_file", "edit_file"}
	}
}

// defaultChecksForType 根据 task type 返回默认检查项（编排层 §4.1）。
func defaultChecksForType(taskType string) []string {
	switch taskType {
	case TaskTypeImplement:
		return []string{"go build ./...", "go test ./...", "golangci-lint run --level=error"}
	case TaskTypeReview:
		return []string{"verdict=pass"}
	default:
		return []string{"go build ./..."}
	}
}

// AssertBudgetHintTokensCapOnly 致命反例防护（，2026-10-09）：
// BudgetHint 必须**只**带 token 轴（TokensCap），**不得**再出现任何「钱」字段
// （CostCapCents 等）。用 reflect 扫结构面 —— 防有人把自定价字段加回来
// （与 model_profile_test 的「不得含 cost 字段」守卫**方向同向**）。
func AssertBudgetHintTokensCapOnly() bool {
	rt := reflect.TypeOf(BudgetHint{})
	if _, ok := rt.FieldByName("TokensCap"); !ok {
		return false
	}
	for i := 0; i < rt.NumField(); i++ {
		name := strings.ToLower(rt.Field(i).Name)
		if strings.Contains(name, "cost") || strings.Contains(name, "cents") {
			return false
		}
	}
	return true
}
