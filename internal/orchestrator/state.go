// state.go — 模块 1 状态机快照 State + 持久化 SaveState/LoadState（H10 Phase 1 编排层 §2.1 / §2.4）。
// struct 字段定义；落地：TaskID/Stage/SubTasks/UpdatedAt 4 字段与 02 完全一致
// ；落地：SaveState/LoadState JSON 单文件覆盖写
// ≤3；落地：SubTaskState.Depth 字段
// §2.4 S1 State 扩展；落地：新增 Requirement 字段（SNAPSHOT）+ ClarifyAnswers + GeneratedPlanJSON
// SNAPSHOT: pkg=internal/orchestrator; type=State; fields={TaskID:string,Stage:string,SubTasks:[]SubTaskState,UpdatedAt:int64,Requirement:string,ClarifyAnswers:[]ClarifyAnswer,GeneratedPlanJSON:string}; Stage∈{clarify,plan,contract,implement,review,test,done,interrupted}; MaxDepth≤3; persistence=JSON单文件覆盖写; path=.whale/orchestrator/<task-id>/state.json; Phase6_new_fields={Requirement,ClarifyAnswers,GeneratedPlanJSON}; backward_compat=omitempty向后兼容

package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Stage 枚举常量（编排层 §2.1，02 §2.7.1）。
const (
	StageInit        = "init"
	StageClarify     = "clarify"
	StagePlan        = "plan"
	StageContract    = "contract"
	StageImplement   = "implement"
	StageReview      = "review"
	StageTest        = "test"
	StageDone        = "done"
	StageInterrupted = "interrupted"
)

// SubTaskStatus 枚举常量（编排层 §2.1）。
const (
	SubTaskStatusPending     = "pending"
	SubTaskStatusRunning     = "running"
	SubTaskStatusCompleted   = "completed"
	SubTaskStatusInterrupted = "interrupted"
)

// MaxDepthHardLimit 递归深度硬上限（编排层 §3.5）。
const MaxDepthHardLimit = 3

// State 状态机快照（02 §2.7.1 一致， 状态全持久化可恢复）。
// Stage 枚举：clarify/plan/contract/implement/review/test/done + interrupted（用户叫停）。
// Phase 6 新增字段（SNAPSHOT）：
// - Requirement：用户需求描述（/orchestrate start 传入 + clarify 合并答案后更新）
// - ClarifyAnswers：clarify Stage 用户回答列表（断点续传时恢复提问上下文）
// - GeneratedPlanJSON：plan Stage 生成的方案 JSON（contract Stage 展示给用户）
//
// 三个新字段均 omitempty 向后兼容，旧 state.json 反序列化时为零值（Phase 6 之前的任务不受影响）。
type State struct {
	TaskID            string          `json:"task_id"`
	Stage             string          `json:"stage"`
	SubTasks          []SubTaskState  `json:"subtasks"`
	UpdatedAt         int64           `json:"updated_at"`
	Requirement       string          `json:"requirement,omitempty"`         // Phase 6 新增（SNAPSHOT）
	ClarifyAnswers    []ClarifyAnswer `json:"clarify_answers,omitempty"`     // Phase 6 新增（SNAPSHOT）
	GeneratedPlanJSON string          `json:"generated_plan_json,omitempty"` // Phase 6 新增（SNAPSHOT）
}

// SubTaskState 子任务状态（编排层 §2.1）。
type SubTaskState struct {
	SubTaskID  string `json:"subtask_id"`
	Stage      string `json:"stage"`
	Status     string `json:"status"`
	Depth      int    `json:"depth"`
	AgentID    string `json:"agent_id"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// SaveState 持久化 state.json 到 .whale/orchestrator/<task-id>/state.json（JSON 单文件覆盖写，编排层 §2.4）。
// 路径穿越防护：taskID 含 ".." 或 "/" 或 "\" 时拒绝（防注入）。
func SaveState(baseDir, taskID string, state State) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}

	dir := filepath.Join(baseDir, "orchestrator", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", dir, err)
	}

	path := filepath.Join(dir, "state.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}

	// 原子写入：先写临时文件再 rename（防写入中断导致文件损坏）
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("state: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("state: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// LoadState 从 state.json 读取（断点续传启动时调用，编排层 §2.5）。
// 路径穿越防护：taskID 含 ".." 或 "/" 或 "\" 时拒绝。
// 文件不存在时返回 (nil, nil)，调用方按需初始化。
func LoadState(baseDir, taskID string) (*State, error) {
	if err := validateTaskID(taskID); err != nil {
		return nil, err
	}

	path := filepath.Join(baseDir, "orchestrator", taskID, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("state: unmarshal: %w", err)
	}
	return &state, nil
}

// validateTaskID 校验 taskID 不含路径穿越字符（防注入，编排层 §2.4 安全要求）。
// 规则：非空 + 不含 .. + 不含 / + 不含 \ 。
func validateTaskID(taskID string) error {
	if taskID == "" {
		return fmt.Errorf("state: taskID must not be empty")
	}
	if strings.Contains(taskID, "..") {
		return fmt.Errorf("state: taskID contains '..': %s", taskID)
	}
	if strings.ContainsAny(taskID, "/\\") {
		return fmt.Errorf("state: taskID contains path separator: %s", taskID)
	}
	return nil
}
