// dispatcher.go — 模块 4 U2 外层包装 Dispatcher + N1Agent 接口抽象 + input/output.json + 重派机制（H10 Phase 1 编排层 §3.4）。
// REF-外部-同类CLI@2026-03-31快照-Task tool 同步派发；借鉴逻辑：同步 Execute + input/output 隔离；落地：Dispatcher.Dispatch 同步调 N1.Agent.Execute
// Agent.Execute 接口只读使用；落地：N1Agent interface 抽象，不改 N1 签名
// tool + H10 外层包装；落地：Dispatcher 写 input.json/output.json + 重派机制
// ≤3；落地：checkDepth 派发前调用（v0.2 修正接入）
// SNAPSHOT: pkg=internal/orchestrator; type=Dispatcher; method=Dispatch(ctx,TaskContext,SubTaskDef)(*ImplementOutput,error); N1_iface=N1Agent.Execute(不改签名); sync=true(Phase1); retry_max=3; checkDepth=派发前调用(v0.2修正接入); depth=tc.Depth+1(v0.2修正不再硬编码1); redispatch=git回滚+重派; status∈{pending,running,completed,interrupted}

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// N1Agent N1 Agent 接口抽象（只读使用 N1.Agent.Execute，，不改签名）。
// 02 §2.7.2 契约：H10 只读 Agent.Execute(ctx, AgentInput) (AgentOutput, error)。
// N1Agent 是 H10 包装层接口，方法签名形状与 N1 Agent.Execute 一致：(ctx, input) → (output, error)。
// 不修改 N1 的 Agent.Execute 签名，仅在外层包装。
type N1Agent interface {
	Execute(ctx context.Context, input N1AgentInput) (N1AgentOutput, error)
}

// N1AgentInput H10 包装层输入（映射 N1 AgentInput，不改 N1 签名，编排层 §3.4）。
type N1AgentInput struct {
	Prompt  string
	Tools   []string
	Model   string
	TaskCtx TaskContext
}

// N1AgentOutput H10 包装层输出（映射 N1 AgentOutput，不改 N1 签名，编排层 §3.4）。
type N1AgentOutput struct {
	CodeDiff     string
	TransferDoc  string
	FilesTouched []string
}

// Dispatcher 子任务派发器（H10 外层包装，U2 决策，编排层 §3.4）。
// 包装 N1 Agent.Execute 同步派发，派发前写 input.json，完成后写 output.json，中断后重派。
type Dispatcher struct {
	agent   N1Agent
	baseDir string
	mu      sync.Mutex
	// gitRollbackFn 可注入的 git 回滚函数（重派前调用，便于测试 mock）
	gitRollbackFn func(workDir string) error
}

// NewDispatcher 构造 Dispatcher，baseDir 为 .whale/orchestrator/<task-id>/。
func NewDispatcher(agent N1Agent, baseDir string) *Dispatcher {
	return &Dispatcher{
		agent:   agent,
		baseDir: baseDir,
	}
}

// SetGitRollbackFn 设置 git 回滚函数（重派前调用，便于测试注入）。
func (d *Dispatcher) SetGitRollbackFn(fn func(workDir string) error) {
	d.gitRollbackFn = fn
}

// Dispatch 同步派发子任务（Phase 1 单串行，同步足够，编排层 §3.4）。
// 流程：① checkDepth ② validateSubtaskID（防路径穿越） ③ writeInput + writeStatus(running) ④ agent.Execute 同步 ⑤ writeOutput + writeStatus(completed)
func (d *Dispatcher) Dispatch(ctx context.Context, tc TaskContext, subtask SubTaskDef) (*ImplementOutput, error) {
	// 0. 派发前：检查递归深度（硬上限 ≤3，v0.2 修正：接入 checkDepth，不再跳过）
	if err := d.checkDepth(tc); err != nil {
		return nil, err
	}
	depth := tc.Depth + 1

	// 0.1 派发前：校验 subtaskID 防路径穿越（审查 v1 致命漏洞修复：原 validateSubtaskID 死代码致路径穿越）
	if err := validateSubtaskID(subtask.SubTaskID); err != nil {
		return nil, err
	}

	subtaskDir := filepath.Join(d.baseDir, "subtasks", subtask.SubTaskID)
	if err := os.MkdirAll(subtaskDir, 0o755); err != nil {
		return nil, fmt.Errorf("dispatcher: mkdir %s: %w", subtaskDir, err)
	}

	// 1. 派发前：写 input.json + status.json
	if err := d.writeInput(subtaskDir, tc, subtask); err != nil {
		return nil, err
	}
	if err := d.writeStatus(subtaskDir, SubTaskStatusRunning, depth); err != nil {
		return nil, err
	}

	// 2. 派发：调 N1 Agent.Execute（同步，不改签名，）
	n1Input := d.buildN1Input(tc, subtask)
	n1Output, err := d.agent.Execute(ctx, n1Input)
	if err != nil {
		// 派发失败：标记 interrupted
		_ = d.writeStatus(subtaskDir, SubTaskStatusInterrupted, depth)
		return nil, fmt.Errorf("dispatcher: dispatch %s: %w", subtask.SubTaskID, err)
	}

	// 3. 完成后：写 output.json + status.json
	implOutput := d.parseN1Output(n1Output)
	if err := d.writeOutput(subtaskDir, implOutput); err != nil {
		return nil, err
	}
	if err := d.writeStatus(subtaskDir, SubTaskStatusCompleted, depth); err != nil {
		return nil, err
	}

	return implOutput, nil
}

// checkDepth 检查递归深度（硬上限 ≤3，编排层 §3.5）。
// v0.2 修正：派发前必须调用 checkDepth，不再跳过；depth = tc.Depth + 1，不再硬编码 1。
func (d *Dispatcher) checkDepth(parentCtx TaskContext) error {
	depth := parentCtx.Depth + 1
	if depth > MaxDepthHardLimit {
		return fmt.Errorf("dispatcher: max depth exceeded: %d > %d", depth, MaxDepthHardLimit)
	}
	return nil
}

// writeInput 写 input.json 到 subtasks/<subtask-id>/input.json（U2 外层包装，编排层 §3.4）。
func (d *Dispatcher) writeInput(subtaskDir string, tc TaskContext, subtask SubTaskDef) error {
	input := struct {
		TaskContext TaskContext `json:"task_context"`
		SubTaskDef  SubTaskDef  `json:"subtask_def"`
		WrittenAt   int64       `json:"written_at"`
	}{
		TaskContext: tc,
		SubTaskDef:  subtask,
		WrittenAt:   time.Now().Unix(),
	}

	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return fmt.Errorf("dispatcher: marshal input: %w", err)
	}

	path := filepath.Join(subtaskDir, "input.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("dispatcher: write input %s: %w", path, err)
	}
	return nil
}

// writeOutput 写 output.json 到 subtasks/<subtask-id>/output.json（U2 外层包装，编排层 §3.4）。
func (d *Dispatcher) writeOutput(subtaskDir string, output *ImplementOutput) error {
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("dispatcher: marshal output: %w", err)
	}

	path := filepath.Join(subtaskDir, "output.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("dispatcher: write output %s: %w", path, err)
	}
	return nil
}

// writeStatus 写 status.json（pending/running/completed/interrupted，编排层 §3.4）。
func (d *Dispatcher) writeStatus(subtaskDir, status string, depth int) error {
	st := struct {
		Status    string `json:"status"`
		Depth     int    `json:"depth"`
		UpdatedAt int64  `json:"updated_at"`
	}{
		Status:    status,
		Depth:     depth,
		UpdatedAt: time.Now().Unix(),
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("dispatcher: marshal status: %w", err)
	}

	path := filepath.Join(subtaskDir, "status.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("dispatcher: write status %s: %w", path, err)
	}
	return nil
}

// buildN1Input 构造 N1 AgentInput（不改 N1 签名，H10 包装层注入 TaskContext，编排层 §3.4）。
func (d *Dispatcher) buildN1Input(tc TaskContext, subtask SubTaskDef) N1AgentInput {
	return N1AgentInput{
		Prompt:  fmt.Sprintf("执行子任务 %s: %s", subtask.SubTaskID, tc.Goal),
		Tools:   tc.Tools,
		Model:   "", // 由 Router 选模型后注入
		TaskCtx: tc,
	}
}

// parseN1Output 解析 N1 AgentOutput 为 ImplementOutput（编排层 §3.4）。
func (d *Dispatcher) parseN1Output(n1Output N1AgentOutput) *ImplementOutput {
	return &ImplementOutput{
		CodeDiff:     n1Output.CodeDiff,
		TransferDoc:  n1Output.TransferDoc,
		FilesTouched: n1Output.FilesTouched,
		L0Pass:       true, // 由编排器后续 L0 评分覆盖
		L1Score:      0,    // 由编排器后续 L1 评分覆盖
		ErrorType:    ErrorTypeNone,
	}
}

// Redispatch 重派子任务（重派机制，最多 3 次，编排层 §3.4）。
// 重派前：git 回滚到派发前 commit（重派幂等性）。
// 重派触发条件：agent.Execute 中途主进程崩溃，下次启动检测 status=running 但无 output.json。
func (d *Dispatcher) Redispatch(ctx context.Context, tc TaskContext, subtask SubTaskDef, retryCount int) (*ImplementOutput, error) {
	if retryCount >= 3 {
		return nil, fmt.Errorf("dispatcher: redispatch %s exceeded max retry 3", subtask.SubTaskID)
	}

	// 重派前：git 回滚（重派幂等性）
	if d.gitRollbackFn != nil {
		if err := d.gitRollbackFn(d.baseDir); err != nil {
			return nil, fmt.Errorf("dispatcher: git rollback before redispatch: %w", err)
		}
	}

	// 重新派发
	return d.Dispatch(ctx, tc, subtask)
}

// CheckRecovery 断点续传恢复检查（编排层 §2.5 v0.2 补充）。
// 扫描 subtasks/<subtask-id>/status.json，判断是否需要恢复。
// 返回值：status 字符串 + 是否需要重派 + error。
func (d *Dispatcher) CheckRecovery(subtaskID string) (status string, needsRedispatch bool, err error) {
	// 审查 v1 致命漏洞修复：校验 subtaskID 防路径穿越（原 validateSubtaskID 死代码）
	if err := validateSubtaskID(subtaskID); err != nil {
		return "", false, err
	}
	subtaskDir := filepath.Join(d.baseDir, "subtasks", subtaskID)
	statusPath := filepath.Join(subtaskDir, "status.json")
	outputPath := filepath.Join(subtaskDir, "output.json")

	// 读 status.json
	statusData, err := os.ReadFile(statusPath)
	if err != nil {
		if os.IsNotExist(err) {
			return SubTaskStatusPending, false, nil // 未派发过
		}
		return "", false, fmt.Errorf("dispatcher: read status %s: %w", statusPath, err)
	}

	var st struct {
		Status string `json:"status"`
		Depth  int    `json:"depth"`
	}
	if err := json.Unmarshal(statusData, &st); err != nil {
		return "", false, fmt.Errorf("dispatcher: unmarshal status: %w", err)
	}

	switch st.Status {
	case SubTaskStatusCompleted:
		return SubTaskStatusCompleted, false, nil
	case SubTaskStatusRunning:
		// 检查 output.json 是否存在且完整（v0.2 补充：崩溃窗口恢复）
		if _, err := os.Stat(outputPath); err == nil {
			// output.json 存在 → 校验完整性
			if isOutputComplete(outputPath) {
				return SubTaskStatusCompleted, false, nil // 视为 completed
			}
		}
		// status=running 但无 output.json 或不完整 → 标记 interrupted，需重派
		_ = d.writeStatus(subtaskDir, SubTaskStatusInterrupted, st.Depth)
		return SubTaskStatusInterrupted, true, nil
	case SubTaskStatusInterrupted:
		return SubTaskStatusInterrupted, true, nil
	case SubTaskStatusPending:
		return SubTaskStatusPending, true, nil
	default:
		return st.Status, false, nil
	}
}

// isOutputComplete 校验 output.json 完整性（能否正常 unmarshal，编排层 §2.5）。
func isOutputComplete(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var out ImplementOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return false
	}
	return true
}

// MarkInterrupted 标记子任务为 interrupted（防状态丢失，编排层 §2.5）。
func (d *Dispatcher) MarkInterrupted(subtaskID string, depth int) error {
	// 审查 v1 致命漏洞修复：校验 subtaskID 防路径穿越（原 validateSubtaskID 死代码）
	if err := validateSubtaskID(subtaskID); err != nil {
		return err
	}
	subtaskDir := filepath.Join(d.baseDir, "subtasks", subtaskID)
	return d.writeStatus(subtaskDir, SubTaskStatusInterrupted, depth)
}

// validateSubtaskID 校验 subtaskID 不含路径穿越字符（防注入）。
func validateSubtaskID(subtaskID string) error {
	if subtaskID == "" {
		return fmt.Errorf("dispatcher: subtaskID must not be empty")
	}
	if strings.Contains(subtaskID, "..") {
		return fmt.Errorf("dispatcher: subtaskID contains '..': %s", subtaskID)
	}
	if strings.ContainsAny(subtaskID, "/\\") {
		return fmt.Errorf("dispatcher: subtaskID contains path separator: %s", subtaskID)
	}
	return nil
}
