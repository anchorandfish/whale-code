// workflow_command.go — H10 Phase 2 模块 2 /workflow 命令实现（编排层 §6.4）。
// ；落地：/workflow list/run/save-as 三命令
// ；落地：通过 command_list.go registerBuiltinCommands 注册（workflowCommands）
// 复用-StateMachine/Dispatcher/Router/BudgetTracker/TraceWriter；落地：executeStages 复用 Phase 1 编排骨架，不改签名
// ≤3 硬上限；落地：validateMaxDepth 致命反例 3 校验
// ；落地：HandleRun 写 state.json，HandleSaveAs 读 state.json
// SNAPSHOT: cmd=/workflow; subcmds={list,run,save-as}; register=command_list.go.registerBuiltinCommands(via workflowCommands); reuse={StateMachine,Dispatcher,Router,BudgetTracker,TraceWriter,CapabilityCache}; max_depth≤3(); path_traversal=validateSaveAsName+validateTemplateName; store=.whale/workflows/<name>.template.json

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WorkflowSubcommands /workflow 子命令枚举（编排层 §6.4）。
const (
	WorkflowSubList   = "list"
	WorkflowSubRun    = "run"
	WorkflowSubSaveAs = "save-as"
)

// WorkflowStageTypeInteractive / agent / auto Stage 类型枚举（编排层 §6.2）。
const (
	WorkflowStageTypeInteractive = "interactive"
	WorkflowStageTypeAgent       = "agent"
	WorkflowStageTypeAuto        = "auto"
)

// WorkflowVarMaxDepth 模板变量名：max_depth（致命反例 3 校验入口，）。
// 用户可通过 Variables 注入 max_depth，renderVariables 解析后由 validateMaxDepth 校验 ≤3。
const WorkflowVarMaxDepth = "max_depth"

// WorkflowVarTaskDesc 模板变量名：task_desc（--input 注入）。
const WorkflowVarTaskDesc = "task_desc"

// WorkflowCommands /workflow 命令组（编排层 §6.4）。
// 通过 command_list.go registerBuiltinCommands 注册（02 §2.5.1）。
// 复用 Phase 1 已落地模块：Dispatcher / Router / BudgetTracker，以及 Phase 0 CapabilityCache。
// StateMachine 与 TraceWriter 为 per-task 实例，在 HandleRun 中按 task_id 创建（不复用全局实例）。
type WorkflowCommands struct {
	loader     *TemplateLoader  // 模板加载器（项目级 + 全局级）
	dispatcher *Dispatcher      // Phase 1 复用：子任务派发
	router     *Router          // Phase 1 复用：三因子路由
	budget     *BudgetTracker   // Phase 1 复用：双层预算
	capability *CapabilityCache // Phase 0 复用：BuildTaskContext 注入 capability_snapshot
	baseDir    string           // .whale/ 根目录（SaveState/LoadState 内部拼接 orchestrator/ 子目录）
}

// NewWorkflowCommands 构造 /workflow 命令组（编排层 §6.4）。
// baseDir 为 .whale/ 根目录。capability 为 Phase 0 CapabilityCache（BuildTaskContext 必需）。
func NewWorkflowCommands(loader *TemplateLoader, dispatcher *Dispatcher, router *Router, budget *BudgetTracker, baseDir string, capability *CapabilityCache) *WorkflowCommands {
	return &WorkflowCommands{
		loader:     loader,
		dispatcher: dispatcher,
		router:     router,
		budget:     budget,
		capability: capability,
		baseDir:    baseDir,
	}
}

// Handle 处理 /workflow 命令入口（解析子命令并分发，编排层 §6.4）。
// args 格式："<subcommand> [args...]"，如 "list" / "run feat-login --input 实现登录" / "save-as my-tpl"。
// 返回（输出文本, error）。
func (c *WorkflowCommands) Handle(ctx context.Context, args string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return c.usageHelp(), nil
	}

	subcmd := parts[0]
	rest := parts[1:]

	switch subcmd {
	case WorkflowSubList:
		return c.HandleList(ctx, rest)
	case WorkflowSubRun:
		return c.HandleRun(ctx, rest)
	case WorkflowSubSaveAs:
		return c.HandleSaveAs(ctx, rest)
	default:
		return c.usageHelp(), nil
	}
}

// HandleList 处理 /workflow list（编排层 §6.4.1）。
// 扫描项目级 + 全局级目录，按优先级合并，输出表格。
func (c *WorkflowCommands) HandleList(ctx context.Context, args []string) (string, error) {
	templates, err := c.loader.List()
	if err != nil {
		return "", fmt.Errorf("workflow list: %w", err)
	}
	if len(templates) == 0 {
		return "暂无工作流模板（使用 /workflow save-as <name> 从当前任务保存）", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("工作流模板（共 %d 个）:\n", len(templates)))
	sb.WriteString(fmt.Sprintf("  %-20s %-10s %-12s %s\n", "Name", "Version", "Stages", "Desc"))
	for _, t := range templates {
		desc := t.Desc
		if len(desc) > 40 {
			desc = desc[:37] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %-20s %-10s %-12d %s\n", t.Name, t.Version, len(t.Stages), desc))
	}
	return sb.String(), nil
}

// HandleRun 处理 /workflow run <name> --input "需求"（编排层 §6.4.2）。
// 流程：解析 args → 加载模板 → 渲染变量 → MaxDepth 校验（致命反例 3）→ 创建 task_id → 构造 TaskContext → executeStages。
func (c *WorkflowCommands) HandleRun(ctx context.Context, args []string) (string, error) {
	name, input, err := parseRunArgs(args)
	if err != nil {
		return "用法: /workflow run <name> --input \"需求\"", err
	}

	// 1. 加载模板（路径穿越校验在 Load 内）
	tpl, err := c.loader.Load(name)
	if err != nil {
		return "", fmt.Errorf("workflow run: load template %q: %w", name, err)
	}

	// 2. 渲染变量（--input 注入 task_desc）
	rendered, err := c.renderVariables(tpl, input)
	if err != nil {
		return "", fmt.Errorf("workflow run: render variables: %w", err)
	}

	// 3. MaxDepth 校验（致命反例 3，零容忍，）
	if err := c.validateMaxDepth(rendered); err != nil {
		return "", fmt.Errorf("workflow run: %w", err)
	}

	// 4. 创建 task_id（格式：T-YYYYMMDD-HHMMSS，与 /orchestrate start 一致）
	taskID := fmt.Sprintf("T-%s", time.Now().Format("20060102-150405"))

	// 5. 创建状态机 + 推进到 clarify Stage（init → clarify）
	sm := NewStateMachine(taskID)
	if err := sm.Advance(); err != nil {
		return "", fmt.Errorf("workflow run: advance to clarify: %w", err)
	}

	// 6. 持久化 state.json
	state := sm.Checkpoint()
	if err := SaveState(c.baseDir, taskID, state); err != nil {
		return "", fmt.Errorf("workflow run: save state: %w", err)
	}

	// 7. 按 stages 顺序执行
	if err := c.executeStages(ctx, rendered, taskID); err != nil {
		return "", fmt.Errorf("workflow run: execute stages: %w", err)
	}

	return fmt.Sprintf("工作流已启动\n  Task ID: %s\n  模板: %s v%s\n  Stage 数: %d\n  状态: 已持久化到 state.json", taskID, tpl.Name, tpl.Version, len(tpl.Stages)), nil
}

// HandleSaveAs 处理 /workflow save-as <name> [--task <task-id>]（编排层 §6.4.3）。
// 流程：name 路径穿越校验（致命反例 4）→ 读取 state.json + Stage 输入输出 → 反向构造 WorkflowTemplate → Save。
// taskID 默认使用最近一个任务（扫描 .whale/orchestrator/ 取最新 UpdatedAt）；--task 可显式指定。
func (c *WorkflowCommands) HandleSaveAs(ctx context.Context, args []string) (string, error) {
	name, taskID, err := parseSaveAsArgs(args)
	if err != nil {
		return "用法: /workflow save-as <name> [--task <task-id>]", err
	}

	// 1. name 路径穿越校验（致命反例 4，零容忍）
	if err := validateSaveAsName(name); err != nil {
		return "", fmt.Errorf("workflow save-as: %w", err)
	}

	// 2. taskID 未指定时取最近一个任务
	if taskID == "" {
		taskID, err = c.findLatestTaskID()
		if err != nil {
			return "", fmt.Errorf("workflow save-as: find latest task: %w", err)
		}
		if taskID == "" {
			return "", fmt.Errorf("workflow save-as: no task found, use --task <task-id> to specify")
		}
	}

	// 3. 读取 state.json
	state, err := LoadState(c.baseDir, taskID)
	if err != nil {
		return "", fmt.Errorf("workflow save-as: load state %s: %w", taskID, err)
	}
	if state == nil {
		return "", fmt.Errorf("workflow save-as: task %s not found", taskID)
	}

	// 4. 反向构造 WorkflowTemplate
	tpl, err := c.buildTemplateFromState(state, name)
	if err != nil {
		return "", fmt.Errorf("workflow save-as: build template: %w", err)
	}

	// 5. 保存（Save 内会再过 ValidateTemplate + 路径穿越校验 + 计算 ContentHash）
	if err := c.loader.Save(tpl); err != nil {
		return "", fmt.Errorf("workflow save-as: save: %w", err)
	}

	return fmt.Sprintf("工作流模板已保存\n  Name: %s\n  Version: %s\n  Stage 数: %d\n  ContentHash: %s\n  来源任务: %s", tpl.Name, tpl.Version, len(tpl.Stages), tpl.Metadata.ContentHash, taskID), nil
}

// renderVariables 渲染模板变量（编排层 §6.4.2）。
// --input 填入 task_desc 变量；max_depth 变量保留原值（由 validateMaxDepth 校验）。
// 渲染规则：Stages[].Prompt 中 {{.task_desc}} 替换为 input。
func (c *WorkflowCommands) renderVariables(tpl *WorkflowTemplate, input string) (*WorkflowTemplate, error) {
	if tpl == nil {
		return nil, fmt.Errorf("renderVariables: template must not be nil")
	}
	// 深拷贝避免污染原模板
	rendered := *tpl
	rendered.Stages = make([]StageTpl, len(tpl.Stages))
	for i, s := range tpl.Stages {
		s.Prompt = strings.ReplaceAll(s.Prompt, "{{."+WorkflowVarTaskDesc+"}}", input)
		rendered.Stages[i] = s
	}
	return &rendered, nil
}

// validateMaxDepth 校验模板 max_depth 变量 ≤ MaxDepthHardLimit（致命反例 3，零容忍，）。
// 模板 Variables 中若声明 max_depth 变量，其 Default 值必须 ≤ 3。
func (c *WorkflowCommands) validateMaxDepth(tpl *WorkflowTemplate) error {
	for _, v := range tpl.Variables {
		if v.Name == WorkflowVarMaxDepth && v.Default != "" {
			md, err := strconv.Atoi(v.Default)
			if err != nil {
				return fmt.Errorf("validateMaxDepth: parse max_depth %q: %w", v.Default, err)
			}
			if md > MaxDepthHardLimit {
				return fmt.Errorf("validateMaxDepth: max_depth %d exceeds hard limit %d", md, MaxDepthHardLimit)
			}
			if md < 1 {
				return fmt.Errorf("validateMaxDepth: max_depth %d must be >= 1", md)
			}
		}
	}
	return nil
}

// executeStages 按 stages 顺序执行 StateMachine.Advance（复用 Phase 1 编排骨架，编排层 §6.4.2）。
// 流程：① 每个 Stage 调 stateMachine.Advance() 推进 ② agent/auto 类型调 dispatcher.Dispatch ③ 每步写 trace ④ 预算检查。
// interactive 类型 Stage 仅推进状态机（不派发，等待用户输入）。
func (c *WorkflowCommands) executeStages(ctx context.Context, tpl *WorkflowTemplate, taskID string) error {
	// 加载已持久化的 state.json 恢复状态机（HandleRun 已写入）
	state, err := LoadState(c.baseDir, taskID)
	if err != nil {
		return fmt.Errorf("executeStages: load state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("executeStages: state for task %s not found", taskID)
	}

	sm := NewStateMachine(taskID)
	if err := sm.Restore(*state); err != nil {
		return fmt.Errorf("executeStages: restore state: %w", err)
	}

	// 创建 per-task TraceWriter（路径：.whale/orchestrator/<task-id>/trace.jsonl）
	tracePath := filepath.Join(c.baseDir, "orchestrator", taskID, "trace.jsonl")
	tw, err := NewTraceWriter(tracePath)
	if err != nil {
		return fmt.Errorf("executeStages: new trace writer: %w", err)
	}
	defer tw.Close()

	// 预算检查：若已超 100% 直接拒绝
	if _, _, over100 := c.budget.CheckBudget(); over100 {
		return fmt.Errorf("executeStages: total budget exceeded (100%%), abort before dispatch")
	}

	// 按模板 stages 顺序推进
	for _, stage := range tpl.Stages {
		// ① 推进状态机
		if err := sm.Advance(); err != nil {
			return fmt.Errorf("executeStages: advance to %s: %w", stage.ID, err)
		}

		// ② 写 trace：start 事件
		if err := tw.Append(TraceEntry{
			SubTaskID: stage.ID,
			Depth:     1,
			Event:     TraceEventStart,
			Stage:     stage.ID,
			Timestamp: time.Now().Unix(),
		}); err != nil {
			return fmt.Errorf("executeStages: trace start %s: %w", stage.ID, err)
		}

		// ③ agent/auto 类型 Stage 派发子任务（复用 Phase 1 Dispatcher）
		if stage.Type == WorkflowStageTypeAgent || stage.Type == WorkflowStageTypeAuto {
			// 构造 SubTaskDef（从 StageTpl 转换）
			subtask := SubTaskDef{
				SubTaskID:  stage.ID,
				Type:       stage.Type,
				Complexity: ComplexityT2, // 默认中等复杂度
				DependsOn:  stage.DependsOn,
			}

			// 构造 TaskContext（BuildTaskContext 内部已校验 MaxDepth≤3 / CapabilitySnapshot 非空）
			tc, err := BuildTaskContext(taskID, subtask, c.capability, 1, c.router.BudgetMode())
			if err != nil {
				return fmt.Errorf("executeStages: build task context for %s: %w", stage.ID, err)
			}

			// 派发（Dispatcher.Dispatch 内部 checkDepth 已校验）
			_, dispatchErr := c.dispatcher.Dispatch(ctx, tc, subtask)
			if dispatchErr != nil {
				// 写 trace：fail 事件
				_ = tw.Append(TraceEntry{
					SubTaskID: stage.ID,
					Depth:     1,
					Event:     TraceEventFail,
					Stage:     stage.ID,
					Timestamp: time.Now().Unix(),
					ErrorType: ErrorTypeNone,
				})
				return fmt.Errorf("executeStages: dispatch %s: %w", stage.ID, dispatchErr)
			}
		}

		// ④ 写 trace：end 事件
		if err := tw.Append(TraceEntry{
			SubTaskID: stage.ID,
			Depth:     1,
			Event:     TraceEventEnd,
			Stage:     stage.ID,
			Timestamp: time.Now().Unix(),
		}); err != nil {
			return fmt.Errorf("executeStages: trace end %s: %w", stage.ID, err)
		}

		// ⑤ 持久化 state.json（每步 checkpoint）
		checkpoint := sm.Checkpoint()
		if err := SaveState(c.baseDir, taskID, checkpoint); err != nil {
			return fmt.Errorf("executeStages: save state after %s: %w", stage.ID, err)
		}
	}

	return nil
}

// parseRunArgs 解析 /workflow run args（编排层 §6.4.2）。
// 格式：<name> [--input "需求"]。
// name 必填，--input 可选（缺省时 task_desc 渲染为空字符串）。
func parseRunArgs(args []string) (name, input string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("run: missing template name")
	}
	name = args[0]
	for i := 1; i < len(args); i++ {
		if args[i] == "--input" {
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("run: --input requires a value")
			}
			i++
			input = args[i]
		}
	}
	return name, input, nil
}

// parseSaveAsArgs 解析 /workflow save-as args（编排层 §6.4.3）。
// 格式：<name> [--task <task-id>]。
// name 必填，--task 可选（缺省时取最近任务）。
func parseSaveAsArgs(args []string) (name, taskID string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("save-as: missing template name")
	}
	name = args[0]
	for i := 1; i < len(args); i++ {
		if args[i] == "--task" {
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("save-as: --task requires a value")
			}
			i++
			taskID = args[i]
		}
	}
	return name, taskID, nil
}

// validateSaveAsName 校验 save-as name 不含路径穿越字符（致命反例 4，零容忍，编排层 §6.4.3）。
// 复用 validateTemplateName 规则：非空 + 不含 .. + 不含 / \ 。
func validateSaveAsName(name string) error {
	return validateTemplateName(name)
}

// findLatestTaskID 扫描 .whale/orchestrator/ 目录，返回 UpdatedAt 最大的 task_id（编排层 §6.4.3）。
func (c *WorkflowCommands) findLatestTaskID() (string, error) {
	orchDir := filepath.Join(c.baseDir, "orchestrator")
	entries, err := os.ReadDir(orchDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var latestID string
	var latestUpdatedAt int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tid := entry.Name()
		state, err := LoadState(c.baseDir, tid)
		if err != nil || state == nil {
			continue
		}
		if state.UpdatedAt > latestUpdatedAt {
			latestUpdatedAt = state.UpdatedAt
			latestID = tid
		}
	}
	return latestID, nil
}

// buildTemplateFromState 从 state.json 反向构造 WorkflowTemplate（编排层 §6.4.3）。
// stages 从 state.SubTasks 推导；若无 SubTasks 则使用默认 6 Stage 序列。
func (c *WorkflowCommands) buildTemplateFromState(state *State, name string) (*WorkflowTemplate, error) {
	if state == nil {
		return nil, fmt.Errorf("buildTemplateFromState: state must not be nil")
	}

	// 构造 stages：若 state.SubTasks 非空则从 SubTasks 推导，否则使用默认 6 Stage
	var stages []StageTpl
	if len(state.SubTasks) > 0 {
		for _, st := range state.SubTasks {
			stages = append(stages, StageTpl{
				ID:     st.SubTaskID,
				Type:   WorkflowStageTypeAgent,
				OnFail: OnFailRetryOnce,
				Prompt: "prompts/" + st.Stage + ".md",
			})
		}
	} else {
		// 无 SubTasks 时使用默认 6 Stage 序列（与 Phase 1 defaultStages 对齐）
		for _, ds := range defaultStages {
			stages = append(stages, StageTpl{
				ID:           ds.ID,
				Type:         ds.Type,
				UserConfirm:  ds.UserConfirm,
				AdvanceGuard: ds.AdvanceGuard,
				OnFail:       ds.OnFail,
				Prompt:       "prompts/" + ds.ID + ".md",
			})
		}
	}

	return &WorkflowTemplate{
		Schema:  TemplateSchemaVersion,
		Name:    name,
		Version: "1.0.0",
		Desc:    fmt.Sprintf("从任务 %s 反向构造的模板", state.TaskID),
		Stages:  stages,
		Metadata: TemplateMeta{
			Author:    "whale-workflow-save-as",
			CreatedAt: time.Now().Unix(),
			Tags:      []string{"saved-from-task"},
		},
	}, nil
}

// usageHelp 返回 /workflow 用法帮助（编排层 §6.4）。
func (c *WorkflowCommands) usageHelp() string {
	var sb strings.Builder
	sb.WriteString("/workflow — H10 工作流模板\n\n")
	sb.WriteString("子命令:\n")
	sb.WriteString(fmt.Sprintf("  %-8s %s\n", WorkflowSubList, "列出所有可用模板"))
	sb.WriteString(fmt.Sprintf("           用法: /workflow %s\n", WorkflowSubList))
	sb.WriteString(fmt.Sprintf("  %-8s %s\n", WorkflowSubRun, "运行指定模板（渲染变量 + 创建 task + 执行 stages）"))
	sb.WriteString(fmt.Sprintf("           用法: /workflow %s <name> --input \"需求\"\n", WorkflowSubRun))
	sb.WriteString(fmt.Sprintf("  %-8s %s\n", WorkflowSubSaveAs, "从当前任务保存为模板（反向构造 WorkflowTemplate）"))
	sb.WriteString(fmt.Sprintf("           用法: /workflow %s <name> [--task <task-id>]\n", WorkflowSubSaveAs))
	return sb.String()
}
