// orchestrate_command.go — 模块 5 /orchestrate 命令定义 + Orchestrator 聚合器（H10 Phase 1 编排层 §3.2 / 02 §2.5.1 / §2.7.5）。
// ；落地：通过 command.go registerBuiltinCommands 注册（orchestrateCommands 在 command_list.go）
// H10 新增 /orchestrate 命令；落地：本模块实现 HandleStart/Status/Resume/Abort/Clarify/Plan/Contract
// ；落地：HandleStart 写 state.json，HandleResume 读 state.json 恢复
// §2.4 S1 + §4 S4；落地：新增 4 字段 + functional options + 3 Handle 方法 + guard 工厂
// SNAPSHOT: cmd=/orchestrate; subcmds={start,status,list,resume,abort,clarify,plan,contract}; register=Registry.registerBuiltinCommands(via orchestrateCommands in command_list.go); path=internal/orchestrator/orchestrate_command.go; aggregate={StateMachine,Router,Dispatcher,Scheduler,BudgetTracker,QualityStore,ProfileStore,CapabilityCache,Clarifier,PlanGenerator,Contractor,AskUserGateway}; Phase6_new_fields={clarifier,planGenerator,contractor,askUser}; options={WithClarifier,WithPlanGenerator,WithContractor,WithAskUser}; guards={checkClarifyOutputGuard,checkPlanOutputGuard,checkContractOutputGuard,checkImplementOutputGuard}

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// CommandSpec 子命令规格（编排层 §3.2，用于描述 /orchestrate 子命令）。
// ★ 批 4：Description 拆双语（对齐批 1 `ChoiceOption.DescriptionZH` 先例）——
// Description 为主行可独立读懂的英文；DescriptionZH 非空时 usageHelp 追加缩进中文辅行。
type CommandSpec struct {
	Name          string
	Description   string
	DescriptionZH string
	Usage         string
}

// OrchestrateSubcommands /orchestrate 子命令枚举（编排层 §3.2 + Phase 6 扩展）。
const (
	OrchestrateSubStart    = "start"
	OrchestrateSubStatus   = "status"
	OrchestrateSubList     = "list"
	OrchestrateSubResume   = "resume"
	OrchestrateSubAbort    = "abort"
	OrchestrateSubClarify  = "clarify"  // Phase 6 新增
	OrchestrateSubPlan     = "plan"     // Phase 6 新增
	OrchestrateSubContract = "contract" // Phase 6 新增
)

// Orchestrator 编排器聚合所有编排层组件（编排层 §3.2）。
// 嵌入主进程 goroutine，不作为 daemon worker。
// 聚合：StateMachine + Router + Dispatcher + Scheduler + BudgetTracker + Phase 0 地基层（QualityStore/ProfileStore/CapabilityCache）。
// Phase 6 新增字段（SNAPSHOT）：clarifier/planGenerator/contractor/askUser。
type Orchestrator struct {
	baseDir       string // .whale/ 根目录（SaveState/LoadState 内部拼接 orchestrator/ 子目录）
	router        *Router
	dispatcher    *Dispatcher
	scheduler     *Scheduler
	budget        *BudgetTracker
	quality       *QualityStore
	profile       *ProfileStore
	capability    *CapabilityCache
	clarifier     *Clarifier     // Phase 6 新增（SNAPSHOT）
	planGenerator *PlanGenerator // Phase 6 新增（SNAPSHOT）
	contractor    *Contractor    // Phase 6 新增（SNAPSHOT）
	askUser       AskUserGateway // Phase 6 新增（SNAPSHOT）
}

// OrchestratorOption functional option 类型（Phase 6 S4）。
type OrchestratorOption func(*Orchestrator)

// WithClarifier 注入 Clarifier（Phase 6 S4）。
// llm 为 AIClient 实现（通常为 OrchestratorLLMAdapter）；tracer 为 trace 存储（可为 nil）。
func WithClarifier(llm AIClient, tracer *TraceStore) OrchestratorOption {
	return func(o *Orchestrator) {
		o.clarifier = NewClarifier(llm, tracer)
	}
}

// WithPlanGenerator 注入 PlanGenerator（Phase 6 S4）。
func WithPlanGenerator(llm AIClient, tracer *TraceStore) OrchestratorOption {
	return func(o *Orchestrator) {
		o.planGenerator = NewPlanGenerator(llm, tracer)
	}
}

// WithContractor 注入 Contractor（Phase 6 S4）。
func WithContractor(askUser AskUserGateway, tracer *TraceStore) OrchestratorOption {
	return func(o *Orchestrator) {
		o.contractor = NewContractor(askUser, tracer)
	}
}

// WithAskUser 注入 AskUserGateway（Phase 6 S4）。
func WithAskUser(askUser AskUserGateway) OrchestratorOption {
	return func(o *Orchestrator) {
		o.askUser = askUser
	}
}

// NewOrchestrator 构造编排器（编排层 §3.2 + Phase 6 functional options）。
// baseDir 为 .whale/ 根目录（SaveState/LoadState 内部拼接 orchestrator/ 子目录，编排层 §2.4）。
// agent 为 N1 Agent 接口实现（只读使用，不改签名，）。
// opts 为 functional options（WithClarifier/WithPlanGenerator/WithContractor/WithAskUser）。
func NewOrchestrator(baseDir string, agent N1Agent, quality *QualityStore, profile *ProfileStore, capability *CapabilityCache, budgetMode string, opts ...OrchestratorOption) *Orchestrator {
	orchDir := filepath.Join(baseDir, "orchestrator")
	graph := NewDependencyGraph()
	dispatcher := NewDispatcher(agent, orchDir)
	scheduler := NewScheduler(graph, dispatcher)
	router := NewRouterFromPhase0(profile, budgetMode)

	// 默认总 token 上限（，2026-10-09：预算轴 = provider 真报的 token；可由调用方后续替换）
	budget := NewBudgetTracker(DefaultTotalTokensCap)

	o := &Orchestrator{
		baseDir:    baseDir, // 保留 .whale/ 根，SaveState/LoadState 内部拼接 orchestrator/
		router:     router,
		dispatcher: dispatcher,
		scheduler:  scheduler,
		budget:     budget,
		quality:    quality,
		profile:    profile,
		capability: capability,
	}

	// 应用 functional options
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// orchDir 返回编排器工作目录（baseDir + orchestrator/）。
func (o *Orchestrator) orchDir() string {
	return filepath.Join(o.baseDir, "orchestrator")
}

// Handle 处理 /orchestrate 命令入口（Phase 6 S4 便捷方法，）。
// 内部创建 OrchestrateCmd 并委托其 Handle 方法，避免 main.go 显式构造 cmd。
// args 格式："<subcommand> [args...]"，如 "start 实现登录功能" / "clarify T-001"。
// 返回（输出文本, error）。
func (o *Orchestrator) Handle(ctx context.Context, args string) (string, error) {
	cmd := NewOrchestrateCmd(o)
	return cmd.Handle(ctx, args)
}

// OrchestrateCmd /orchestrate 命令处理器（02 §2.5.1 命令注册，02 §2.7.5 契约）。
// 通过 Registry.registerBuiltinCommands 注册（orchestrateCommands 在 command_list.go），不自定义入口。
type OrchestrateCmd struct {
	orch *Orchestrator
}

// NewOrchestrateCmd 构造 /orchestrate 命令处理器（编排层 §3.2）。
func NewOrchestrateCmd(orch *Orchestrator) *OrchestrateCmd {
	return &OrchestrateCmd{orch: orch}
}

// Commands 返回 /orchestrate 子命令列表（start/status/list/resume/abort/clarify/plan/contract，编排层 §3.2 + Phase 6）。
func (c *OrchestrateCmd) Commands() []CommandSpec {
	return []CommandSpec{
		{Name: OrchestrateSubStart, Description: "Start an orchestration task", DescriptionZH: "启动编排任务", Usage: "/orchestrate start <requirement>"},
		{Name: OrchestrateSubStatus, Description: "Show orchestration task status", DescriptionZH: "查看当前编排任务状态", Usage: "/orchestrate status [task-id]"},
		{Name: OrchestrateSubList, Description: "List all orchestration tasks", DescriptionZH: "列出所有编排任务", Usage: "/orchestrate list"},
		{Name: OrchestrateSubResume, Description: "Resume an interrupted task", DescriptionZH: "恢复中断的编排任务", Usage: "/orchestrate resume <task-id>"},
		{Name: OrchestrateSubAbort, Description: "Abort an orchestration task", DescriptionZH: "中止编排任务", Usage: "/orchestrate abort <task-id>"},
		{Name: OrchestrateSubClarify, Description: "Run clarify Stage (generate clarifying questions)", DescriptionZH: "执行 clarify Stage（生成澄清问题）", Usage: "/orchestrate clarify <task-id>"},
		{Name: OrchestrateSubPlan, Description: "Run plan Stage (generate subtask plan)", DescriptionZH: "执行 plan Stage（生成子任务方案）", Usage: "/orchestrate plan <task-id>"},
		{Name: OrchestrateSubContract, Description: "Run contract Stage (user confirms the plan)", DescriptionZH: "执行 contract Stage（用户确认方案）", Usage: "/orchestrate contract <task-id>"},
	}
}

// Handle 处理 /orchestrate 命令入口（解析子命令并分发，编排层 §3.2 + Phase 6）。
// args 格式："<subcommand> [args...]"，如 "start 实现登录功能" / "status T-001" / "clarify T-001"。
// 返回（输出文本, error）。
func (c *OrchestrateCmd) Handle(ctx context.Context, args string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return c.usageHelp(), nil
	}

	subcmd := parts[0]
	rest := strings.TrimSpace(strings.TrimPrefix(args, subcmd))

	switch subcmd {
	case OrchestrateSubStart:
		return c.HandleStart(ctx, rest)
	case OrchestrateSubStatus:
		return c.HandleStatus(ctx, rest)
	case OrchestrateSubList:
		return c.HandleList(ctx)
	case OrchestrateSubResume:
		return c.HandleResume(ctx, rest)
	case OrchestrateSubAbort:
		return c.HandleAbort(ctx, rest)
	case OrchestrateSubClarify:
		return c.HandleClarify(ctx, rest)
	case OrchestrateSubPlan:
		return c.HandlePlan(ctx, rest)
	case OrchestrateSubContract:
		return c.HandleContract(ctx, rest)
	default:
		return c.usageHelp(), nil
	}
}

// HandleStart 处理 /orchestrate start <需求>：创建 task_id + 初始化 state.json + 进入 clarify Stage（编排层 §3.2）。
// requirement 为用户需求描述，非空时创建新任务。
// Phase 6 新增：requirement 持久化到 state.json 的 Requirement 字段（SNAPSHOT）。
func (c *OrchestrateCmd) HandleStart(ctx context.Context, requirement string) (string, error) {
	if strings.TrimSpace(requirement) == "" {
		return util.Bi("Usage: /orchestrate start <requirement>", "用法: /orchestrate start <需求描述>"), fmt.Errorf("orchestrate: requirement must not be empty")
	}

	// 生成 task_id（格式：T-YYYYMMDD-HHMMSS）
	taskID := fmt.Sprintf("T-%s", time.Now().Format("20060102-150405"))

	// 创建状态机
	sm := NewStateMachine(taskID)

	// 推进到 clarify Stage（init → clarify）
	if err := sm.Advance(); err != nil {
		return "", fmt.Errorf("orchestrate start: advance to clarify: %w", err)
	}

	// 生成 state 快照并持久化（含 Requirement 字段，Phase 6 SNAPSHOT）
	state := sm.Checkpoint()
	state.Requirement = requirement
	if err := SaveState(c.orch.baseDir, taskID, state); err != nil {
		return "", fmt.Errorf("orchestrate start: save state: %w", err)
	}

	// ★ 批 4：整句式双语（en/zh 两条平行模板各 Sprintf 一次再交 util.Bi，
	// 见双语技能 §7「片段双语 = 整句式并排构建」）。
	return util.Bi(
		fmt.Sprintf("Orchestration task created\n  Task ID: %s\n  Requirement: %s\n  Stage: clarify\n  State: persisted to state.json\n  Next: /orchestrate clarify %s", taskID, requirement, taskID),
		fmt.Sprintf("编排任务已创建\n  Task ID: %s\n  需求: %s\n  当前阶段: clarify\n  状态: 已持久化到 state.json\n  下一步: /orchestrate clarify %s", taskID, requirement, taskID),
	), nil
}

// taskNotFound 任务不存在提示（6 处共用同一事实 ⇒ 收口单一声明，/）。
func taskNotFound(taskID string) string {
	return util.Bi(fmt.Sprintf("Task %s does not exist", taskID), fmt.Sprintf("任务 %s 不存在", taskID))
}

// HandleStatus 处理 /orchestrate status：扫描 state.json，输出任务状态（编排层 §3.2）。
// taskID 为空时输出所有任务，非空时输出指定任务。
func (c *OrchestrateCmd) HandleStatus(ctx context.Context, taskID string) (string, error) {
	if taskID != "" {
		// 查询指定任务
		state, err := LoadState(c.orch.baseDir, taskID)
		if err != nil {
			return "", fmt.Errorf("orchestrate status: load state %s: %w", taskID, err)
		}
		if state == nil {
			return taskNotFound(taskID), nil
		}
		return formatTaskStatus(state), nil
	}

	// 扫描所有任务
	tasks, err := c.scanAllTasks()
	if err != nil {
		return "", fmt.Errorf("orchestrate status: scan tasks: %w", err)
	}
	if len(tasks) == 0 {
		return util.Bi("No orchestration tasks yet (create one with /orchestrate start <requirement>)", "暂无编排任务（使用 /orchestrate start <需求> 创建）"), nil
	}

	var sb strings.Builder
	sb.WriteString(util.Bi(
		fmt.Sprintf("Orchestration tasks (%d total):\n", len(tasks)),
		fmt.Sprintf("编排任务列表（共 %d 个）:\n", len(tasks)),
	))
	for _, s := range tasks {
		sb.WriteString(formatTaskStatus(&s))
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// HandleList 处理 /orchestrate list：列出所有编排任务（编排层 §3.2）。
func (c *OrchestrateCmd) HandleList(ctx context.Context) (string, error) {
	tasks, err := c.scanAllTasks()
	if err != nil {
		return "", fmt.Errorf("orchestrate list: scan tasks: %w", err)
	}
	if len(tasks) == 0 {
		return util.Bi("No orchestration tasks", "暂无编排任务"), nil
	}

	var sb strings.Builder
	sb.WriteString(util.Bi(
		fmt.Sprintf("Orchestration tasks (%d total):\n", len(tasks)),
		fmt.Sprintf("编排任务（共 %d 个）:\n", len(tasks)),
	))
	for _, s := range tasks {
		sb.WriteString(util.Bi(
			fmt.Sprintf("  %s  stage=%s  updated=%d\n", s.TaskID, s.Stage, s.UpdatedAt),
			fmt.Sprintf("  %s  阶段=%s  更新时间=%d\n", s.TaskID, s.Stage, s.UpdatedAt),
		))
	}
	return sb.String(), nil
}

// HandleResume 处理 /orchestrate resume <task-id>：断点续传，扫描 subtasks/status.json 恢复（编排层 §2.5 / §3.2）。
func (c *OrchestrateCmd) HandleResume(ctx context.Context, taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return util.Bi("Usage: /orchestrate resume <task-id>", "用法: /orchestrate resume <task-id>"), fmt.Errorf("orchestrate resume: task-id must not be empty")
	}

	state, err := LoadState(c.orch.baseDir, taskID)
	if err != nil {
		return "", fmt.Errorf("orchestrate resume: load state %s: %w", taskID, err)
	}
	if state == nil {
		return taskNotFound(taskID), nil
	}

	// 检查任务状态
	if state.Stage == StageDone {
		return util.Bi(
			fmt.Sprintf("Task %s is already done; nothing to resume", taskID),
			fmt.Sprintf("任务 %s 已完成，无需恢复", taskID),
		), nil
	}
	if state.Stage != StageInterrupted {
		return util.Bi(
			fmt.Sprintf("Task %s is at stage %s; nothing to resume (only interrupted tasks can be resumed)", taskID, state.Stage),
			fmt.Sprintf("任务 %s 当前阶段为 %s，无需恢复（仅 interrupted 状态可恢复）", taskID, state.Stage),
		), nil
	}

	// 从 interrupted 恢复：Restore 状态机
	sm := NewStateMachine(taskID)
	if err := sm.Restore(*state); err != nil {
		return "", fmt.Errorf("orchestrate resume: restore state: %w", err)
	}

	return util.Bi(
		fmt.Sprintf("Task %s resumed\n  Previous stage: %s\n  State: restored into the state machine; you can continue driving it", taskID, state.Stage),
		fmt.Sprintf("任务 %s 已恢复\n  恢复前阶段: %s\n  状态: 已 Restore 到状态机，可继续推进", taskID, state.Stage),
	), nil
}

// HandleAbort 处理 /orchestrate abort <task-id>：标记 interrupted，保留 state.json 供审查（编排层 §3.2）。
func (c *OrchestrateCmd) HandleAbort(ctx context.Context, taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return util.Bi("Usage: /orchestrate abort <task-id>", "用法: /orchestrate abort <task-id>"), fmt.Errorf("orchestrate abort: task-id must not be empty")
	}

	state, err := LoadState(c.orch.baseDir, taskID)
	if err != nil {
		return "", fmt.Errorf("orchestrate abort: load state %s: %w", taskID, err)
	}
	if state == nil {
		return taskNotFound(taskID), nil
	}

	// 标记为 interrupted（保留 state.json 供审查）
	state.Stage = StageInterrupted
	state.UpdatedAt = time.Now().Unix()
	if err := SaveState(c.orch.baseDir, taskID, *state); err != nil {
		return "", fmt.Errorf("orchestrate abort: save state: %w", err)
	}

	return util.Bi(
		fmt.Sprintf("Task %s aborted\n  Stage: interrupted\n  state.json kept for review", taskID),
		fmt.Sprintf("任务 %s 已中止\n  当前阶段: interrupted\n  state.json 已保留供审查", taskID),
	), nil
}

// usageHelp 返回 /orchestrate 用法帮助（编排层 §3.2）。
// ★ 批 4：Description 英文主行 + DescriptionZH 缩进中文辅行（批 1 renderChoiceUI 同款形态）。
func (c *OrchestrateCmd) usageHelp() string {
	var sb strings.Builder
	sb.WriteString(util.Bi("/orchestrate — H10 intelligent orchestration\n\n", "/orchestrate — H10 智能编排系统\n\n"))
	sb.WriteString(util.Bi("Subcommands:\n", "子命令:\n"))
	for _, cmd := range c.Commands() {
		sb.WriteString(fmt.Sprintf("  %-8s %s\n", cmd.Name, cmd.Description))
		if cmd.DescriptionZH != "" {
			sb.WriteString(util.CopyIndent + cmd.DescriptionZH + "\n")
		}
		sb.WriteString(util.Bi(
			fmt.Sprintf("           Usage: %s\n", cmd.Usage),
			fmt.Sprintf("           用法: %s\n", cmd.Usage),
		))
	}
	return sb.String()
}

// scanAllTasks 扫描 .whale/orchestrator/ 下所有任务目录，返回 State 列表（编排层 §2.5）。
func (c *OrchestrateCmd) scanAllTasks() ([]State, error) {
	orchDir := c.orch.orchDir()
	entries, err := os.ReadDir(orchDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 目录不存在 = 无任务
		}
		return nil, err
	}

	var tasks []State
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskID := entry.Name()
		// 跳过非任务目录（quality.jsonl / capability_cache.json 等是文件，不会进这里）
		state, err := LoadState(c.orch.baseDir, taskID)
		if err != nil {
			continue // 跳过无法读取的任务
		}
		if state != nil {
			tasks = append(tasks, *state)
		}
	}
	return tasks, nil
}

// formatTaskStatus 格式化单个任务状态输出（编排层 §3.2）。
func formatTaskStatus(state *State) string {
	if state == nil {
		return util.Bi("(no state)", "（无状态）")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  Task ID: %s\n", state.TaskID))
	sb.WriteString(util.Bi(
		fmt.Sprintf("  Stage: %s\n", state.Stage),
		fmt.Sprintf("  阶段: %s\n", state.Stage),
	))
	sb.WriteString(util.Bi(
		fmt.Sprintf("  Subtasks: %d\n", len(state.SubTasks)),
		fmt.Sprintf("  子任务数: %d\n", len(state.SubTasks)),
	))
	sb.WriteString(util.Bi(
		fmt.Sprintf("  Updated at: %d\n", state.UpdatedAt),
		fmt.Sprintf("  更新时间: %d\n", state.UpdatedAt),
	))
	if state.Requirement != "" {
		sb.WriteString(util.Bi(
			fmt.Sprintf("  Requirement: %s\n", state.Requirement),
			fmt.Sprintf("  需求: %s\n", state.Requirement),
		))
	}
	if len(state.SubTasks) > 0 {
		sb.WriteString(util.Bi("  Subtasks:\n", "  子任务:\n"))
		for _, st := range state.SubTasks {
			sb.WriteString(util.Bi(
				fmt.Sprintf("    %s  stage=%s  status=%s  depth=%d\n", st.SubTaskID, st.Stage, st.Status, st.Depth),
				fmt.Sprintf("    %s  阶段=%s  状态=%s  深度=%d\n", st.SubTaskID, st.Stage, st.Status, st.Depth),
			))
		}
	}
	return sb.String()
}

// === Phase 6 新增：HandleClarify / HandlePlan / HandleContract + guard 工厂 + 辅助方法 ===

// HandleClarify 处理 /orchestrate clarify <task-id>：调 LLM 生成澄清问题 + 调 AskUser 提问 + 合并答案 + 推进到 plan Stage（Phase 6 S1）。
// 降级策略（§11.2）：
// - LLM 失败 → 跳过 clarify，直接进 plan Stage
// - LLM 返回空 questions → 直接进 plan Stage
// - AskUser 失败/用户取消/超时 → 进 StageInterrupted
func (c *OrchestrateCmd) HandleClarify(ctx context.Context, taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return util.Bi("Usage: /orchestrate clarify <task-id>", "用法: /orchestrate clarify <task-id>"), fmt.Errorf("orchestrate clarify: task-id must not be empty")
	}

	state, err := LoadState(c.orch.baseDir, taskID)
	if err != nil {
		return "", fmt.Errorf("orchestrate clarify: load state %s: %w", taskID, err)
	}
	if state == nil {
		return taskNotFound(taskID), nil
	}
	if state.Stage != StageClarify {
		return util.Bi(
			fmt.Sprintf("Task %s is at stage %s; cannot run clarify (needs the clarify stage)", taskID, state.Stage),
			fmt.Sprintf("任务 %s 当前阶段为 %s，无法执行 clarify（需 clarify 阶段）", taskID, state.Stage),
		), nil
	}

	// 0. clarifier 未注入 → 跳过 clarify，直接进 plan（降级）
	if c.orch.clarifier == nil {
		return c.advanceToStage(ctx, taskID, *state, StagePlan,
			util.Bi("Clarifier not injected; skipping clarify", "clarifier 未注入，跳过 clarify"))
	}

	// 1. 调 LLM 生成澄清问题
	questions, err := c.orch.clarifier.Generate(ctx, state.Requirement)
	if err != nil {
		// 降级：跳过 clarify，直接进 plan（§11.2）
		return c.advanceToStage(ctx, taskID, *state, StagePlan,
			util.Bi(fmt.Sprintf("LLM generation failed; skipping clarify: %v", err), fmt.Sprintf("LLM 生成失败，跳过 clarify: %v", err)))
	}
	if len(questions) == 0 {
		// 边界 case：LLM 生成 0 个问题，直接进 plan（§11.2）
		return c.advanceToStage(ctx, taskID, *state, StagePlan,
			util.Bi("LLM returned 0 questions; going straight to plan", "LLM 生成 0 个问题，直接进 plan"))
	}

	// 2. 调 AskUser 提问（askUser 未注入时直接进 plan）
	if c.orch.askUser == nil {
		return c.advanceToStage(ctx, taskID, *state, StagePlan,
			util.Bi("AskUser not injected; skipping questions", "askUser 未注入，跳过提问"))
	}
	answers, err := c.orch.askUser.Ask(ctx, questions)
	if err != nil {
		// 用户取消或超时：进 StageInterrupted（§11.2）
		return c.markInterrupted(ctx, taskID, *state, "clarify AskUser failed: "+err.Error())
	}

	// 3. 合并答案到 requirement
	state.Requirement = c.orch.clarifier.MergeAnswers(state.Requirement, answers)
	state.ClarifyAnswers = answers

	// 4. 推进状态机 clarify → plan（注册 guard）
	// 注：StateMachine.Checkpoint 仅返回 TaskID/Stage/UpdatedAt（不携带 Requirement 等 Phase 6 字段），
	// 因此 guard 需通过闭包捕获当前 state 才能校验 Requirement 非空。
	sm := NewStateMachine(taskID)
	if err := sm.Restore(*state); err != nil {
		return "", fmt.Errorf("orchestrate clarify: restore state: %w", err)
	}
	capturedState := *state
	sm.RegisterGuard("checkClarifyOutput", func(_ State) error {
		return checkClarifyOutputGuard(capturedState)
	})
	if err := sm.Advance(); err != nil {
		// guard 失败：进 StageInterrupted
		return c.markInterrupted(ctx, taskID, *state, "clarify guard failed: "+err.Error())
	}

	// 5. 持久化新状态
	newState := sm.Checkpoint()
	newState.Requirement = state.Requirement
	newState.ClarifyAnswers = state.ClarifyAnswers
	if err := SaveState(c.orch.baseDir, taskID, newState); err != nil {
		return "", fmt.Errorf("orchestrate clarify: save state: %w", err)
	}

	return util.Bi(
		fmt.Sprintf("clarify Stage complete\n  Task ID: %s\n  Questions generated: %d\n  Answers merged: %d\n  Stage: plan\n  Next: /orchestrate plan %s",
			taskID, len(questions), len(answers), taskID),
		fmt.Sprintf("clarify Stage 完成\n  Task ID: %s\n  生成问题数: %d\n  合并答案数: %d\n  当前阶段: plan\n  下一步: /orchestrate plan %s",
			taskID, len(questions), len(answers), taskID),
	), nil
}

// HandlePlan 处理 /orchestrate plan <task-id>：调 LLM 生成子任务方案 + 推进到 contract Stage（Phase 6 S1）。
// 降级策略（§11.2）：
// - LLM 失败 → 降级单子任务
// - LLM 返回空 SubTasks → 降级单子任务
// - planGenerator 未注入 → 降级单子任务
func (c *OrchestrateCmd) HandlePlan(ctx context.Context, taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return util.Bi("Usage: /orchestrate plan <task-id>", "用法: /orchestrate plan <task-id>"), fmt.Errorf("orchestrate plan: task-id must not be empty")
	}

	state, err := LoadState(c.orch.baseDir, taskID)
	if err != nil {
		return "", fmt.Errorf("orchestrate plan: load state %s: %w", taskID, err)
	}
	if state == nil {
		return taskNotFound(taskID), nil
	}
	if state.Stage != StagePlan {
		return util.Bi(
			fmt.Sprintf("Task %s is at stage %s; cannot run plan (needs the plan stage)", taskID, state.Stage),
			fmt.Sprintf("任务 %s 当前阶段为 %s，无法执行 plan（需 plan 阶段）", taskID, state.Stage),
		), nil
	}

	var plan *GeneratedPlan
	degradeReasonEN := ""
	degradeReasonZH := ""

	// planGenerator 未注入 → 降级单子任务
	if c.orch.planGenerator == nil {
		plan = (&PlanGenerator{}).DegradeToSingleTask(state.Requirement)
		degradeReasonEN, degradeReasonZH = "planGenerator not injected; degrading to a single task", "planGenerator 未注入，降级单子任务"
	} else {
		plan, err = c.orch.planGenerator.Generate(ctx, state.Requirement)
		if err != nil {
			// 降级：单子任务（§11.2）
			plan = c.orch.planGenerator.DegradeToSingleTask(state.Requirement)
			degradeReasonEN = fmt.Sprintf("LLM generation failed; degrading to a single task: %v", err)
			degradeReasonZH = fmt.Sprintf("LLM 生成失败，降级单子任务: %v", err)
		} else if len(plan.SubTasks) == 0 {
			// 边界 case：LLM 返回空 SubTasks，降级单子任务（§11.2）
			plan = c.orch.planGenerator.DegradeToSingleTask(state.Requirement)
			degradeReasonEN, degradeReasonZH = "LLM returned empty SubTasks; degrading to a single task", "LLM 返回空 SubTasks，降级单子任务"
		}
	}

	// 序列化 plan 到 state
	planJSON, err := SerializePlan(plan)
	if err != nil {
		return "", fmt.Errorf("orchestrate plan: serialize plan: %w", err)
	}
	state.GeneratedPlanJSON = planJSON

	// 推进状态机 plan → contract（注册 guard）
	// 注：guard 通过闭包捕获当前 state（含 GeneratedPlanJSON），因 StateMachine.Checkpoint 不携带 Phase 6 字段。
	sm := NewStateMachine(taskID)
	if err := sm.Restore(*state); err != nil {
		return "", fmt.Errorf("orchestrate plan: restore state: %w", err)
	}
	capturedState := *state
	sm.RegisterGuard("checkPlanOutput", func(_ State) error {
		return checkPlanOutputGuard(capturedState)
	})
	if err := sm.Advance(); err != nil {
		return c.markInterrupted(ctx, taskID, *state, "plan guard failed: "+err.Error())
	}

	// 持久化新状态
	newState := sm.Checkpoint()
	newState.Requirement = state.Requirement
	newState.ClarifyAnswers = state.ClarifyAnswers
	newState.GeneratedPlanJSON = state.GeneratedPlanJSON
	if err := SaveState(c.orch.baseDir, taskID, newState); err != nil {
		return "", fmt.Errorf("orchestrate plan: save state: %w", err)
	}

	degradeMsgEN := ""
	degradeMsgZH := ""
	if degradeReasonEN != "" {
		degradeMsgEN = "\n  Degrade reason: " + degradeReasonEN
		degradeMsgZH = "\n  降级原因: " + degradeReasonZH
	}
	return util.Bi(
		fmt.Sprintf("plan Stage complete\n  Task ID: %s\n  Subtasks: %d\n  Stage: contract\n  Next: /orchestrate contract %s%s",
			taskID, len(plan.SubTasks), taskID, degradeMsgEN),
		fmt.Sprintf("plan Stage 完成\n  Task ID: %s\n  子任务数: %d\n  当前阶段: contract\n  下一步: /orchestrate contract %s%s",
			taskID, len(plan.SubTasks), taskID, degradeMsgZH),
	), nil
}

// HandleContract 处理 /orchestrate contract <task-id>：向用户展示方案 + 等待确认 + 推进到 implement Stage（Phase 6 S1）。
// 降级策略（§11.2）：
// - 用户拒绝 → 进 StageInterrupted
// - 用户超时 → 进 StageInterrupted
// - contractor 未注入 → 直接进 implement（跳过确认）
func (c *OrchestrateCmd) HandleContract(ctx context.Context, taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return util.Bi("Usage: /orchestrate contract <task-id>", "用法: /orchestrate contract <task-id>"), fmt.Errorf("orchestrate contract: task-id must not be empty")
	}

	state, err := LoadState(c.orch.baseDir, taskID)
	if err != nil {
		return "", fmt.Errorf("orchestrate contract: load state %s: %w", taskID, err)
	}
	if state == nil {
		return taskNotFound(taskID), nil
	}
	if state.Stage != StageContract {
		return util.Bi(
			fmt.Sprintf("Task %s is at stage %s; cannot run contract (needs the contract stage)", taskID, state.Stage),
			fmt.Sprintf("任务 %s 当前阶段为 %s，无法执行 contract（需 contract 阶段）", taskID, state.Stage),
		), nil
	}

	// 反序列化 plan
	plan, err := DeserializePlan(state.GeneratedPlanJSON)
	if err != nil {
		return "", fmt.Errorf("orchestrate contract: deserialize plan: %w", err)
	}
	if plan == nil {
		// plan 为空，直接进 implement（降级）
		return c.advanceToStage(ctx, taskID, *state, StageImplement,
			util.Bi("Plan is empty; skipping contract", "plan 为空，跳过 contract"))
	}

	// contractor 未注入 → 直接进 implement（跳过确认）
	if c.orch.contractor == nil {
		return c.advanceToStage(ctx, taskID, *state, StageImplement,
			util.Bi("Contractor not injected; skipping confirmation", "contractor 未注入，跳过确认"))
	}

	// 调 Contractor.Confirm 等待用户确认
	result, err := c.orch.contractor.Confirm(ctx, plan)
	if err != nil {
		// 用户拒绝/超时 → 进 StageInterrupted（§11.2）
		return c.markInterrupted(ctx, taskID, *state, "contract failed: "+err.Error())
	}
	if !result.Accepted {
		return c.markInterrupted(ctx, taskID, *state,
			util.Bi("Plan rejected by user", "用户拒绝方案"))
	}

	// 用户接受（可能含修改），推进到 implement
	// 注：guard 通过闭包捕获当前 state（含 GeneratedPlanJSON），因 StateMachine.Checkpoint 不携带 Phase 6 字段。
	sm := NewStateMachine(taskID)
	if err := sm.Restore(*state); err != nil {
		return "", fmt.Errorf("orchestrate contract: restore state: %w", err)
	}
	capturedState := *state
	sm.RegisterGuard("checkContractOutput", func(_ State) error {
		return checkContractOutputGuard(capturedState)
	})
	if err := sm.Advance(); err != nil {
		return c.markInterrupted(ctx, taskID, *state, "contract guard failed: "+err.Error())
	}

	newState := sm.Checkpoint()
	newState.Requirement = state.Requirement
	newState.ClarifyAnswers = state.ClarifyAnswers
	newState.GeneratedPlanJSON = state.GeneratedPlanJSON
	if err := SaveState(c.orch.baseDir, taskID, newState); err != nil {
		return "", fmt.Errorf("orchestrate contract: save state: %w", err)
	}

	modifiedMsgEN := ""
	modifiedMsgZH := ""
	if result.Modified != "" {
		// result.Modified 是用户提供的修改内容（调用方数据，任意语言）⇒ 两侧共用同一值，
		// 只双语化我们自己写的标签（技能 §4：别把形态门施加到调用方数据上）。
		modifiedMsgEN = "\n  User modifications: " + result.Modified
		modifiedMsgZH = "\n  用户修改: " + result.Modified
	}
	// ★ 注意：「implement 阶段将在 contract 后自动执行」是 已定性的**虚假承诺**
	// （全仓无任何执行体派发子任务）—— 本批只按原语义翻译、不改语义， 拍板时一并改。
	return util.Bi(
		fmt.Sprintf("contract Stage complete\n  Task ID: %s\n  User confirmed: accepted\n  Stage: implement\n  Note: the implement Stage is expected to run automatically after contract; use /orchestrate status %s to check progress%s",
			taskID, taskID, modifiedMsgEN),
		fmt.Sprintf("contract Stage 完成\n  Task ID: %s\n  用户确认: 接受\n  当前阶段: implement\n  说明: implement 阶段将在 contract 后自动执行，使用 /orchestrate status %s 查看进度%s",
			taskID, taskID, modifiedMsgZH),
	), nil
}

// advanceToStage 辅助方法：跳过当前 Stage，直接推进到目标 Stage（Phase 6 S1）。
// 用于降级场景（LLM 失败/未注入）。
// 调用方需确保 from → to 是合法转移（legalTransitions）。
func (c *OrchestrateCmd) advanceToStage(ctx context.Context, taskID string, state State, targetStage string, reason string) (string, error) {
	sm := NewStateMachine(taskID)
	if err := sm.Restore(state); err != nil {
		return "", fmt.Errorf("orchestrate advanceToStage: restore state: %w", err)
	}
	// 直接设置 current 为 targetStage（跳过中间 Stage）
	// H1-3 修复：SetCurrent 现在校验 degradeTransitions 白名单，失败时返回 error。
	// 降级失败时走 interrupted 兜底，避免非法状态跳转。
	if err := sm.SetCurrent(targetStage); err != nil {
		// 降级目标非法（如直接跳到 done 跳过测试），走 interrupted 兜底
		if intErr := sm.SetCurrent(StageInterrupted); intErr != nil {
			return "", fmt.Errorf("orchestrate advanceToStage: set current %q failed (%w) and fallback to interrupted also failed: %v", targetStage, err, intErr)
		}
		newState := sm.Checkpoint()
		newState.Requirement = state.Requirement
		newState.ClarifyAnswers = state.ClarifyAnswers
		newState.GeneratedPlanJSON = state.GeneratedPlanJSON
		_ = SaveState(c.orch.baseDir, taskID, newState)
		return "", fmt.Errorf("orchestrate advanceToStage: rejected target %q (not in degrade whitelist): %w", targetStage, err)
	}
	newState := sm.Checkpoint()
	newState.Requirement = state.Requirement
	newState.ClarifyAnswers = state.ClarifyAnswers
	newState.GeneratedPlanJSON = state.GeneratedPlanJSON
	if err := SaveState(c.orch.baseDir, taskID, newState); err != nil {
		return "", fmt.Errorf("orchestrate advanceToStage: save state: %w", err)
	}
	return util.Bi(
		fmt.Sprintf("Stage jump\n  Task ID: %s\n  Jump to: %s\n  Reason: %s\n  Next: /orchestrate %s %s",
			taskID, targetStage, reason, targetStage, taskID),
		fmt.Sprintf("Stage 跳转\n  Task ID: %s\n  跳转到: %s\n  原因: %s\n  下一步: /orchestrate %s %s",
			taskID, targetStage, reason, targetStage, taskID),
	), nil
}

// markInterrupted 辅助方法：标记任务为 interrupted 并持久化（Phase 6 S1）。
// 用于 AskUser 失败/用户取消/guard 失败等场景。
func (c *OrchestrateCmd) markInterrupted(ctx context.Context, taskID string, state State, reason string) (string, error) {
	state.Stage = StageInterrupted
	state.UpdatedAt = time.Now().Unix()
	if err := SaveState(c.orch.baseDir, taskID, state); err != nil {
		return "", fmt.Errorf("orchestrate markInterrupted: save state: %w", err)
	}
	return util.Bi(
		fmt.Sprintf("Task entered interrupted\n  Task ID: %s\n  Reason: %s\n  Resume: /orchestrate resume %s", taskID, reason, taskID),
		fmt.Sprintf("任务进入 interrupted\n  Task ID: %s\n  原因: %s\n  恢复: /orchestrate resume %s", taskID, reason, taskID),
	), nil
}

// === Guard 工厂函数（Phase 6 S4，吸纳审查优化 3）===
// guard 函数在 NewStateMachine 后注册（不能全局注册，因 StateMachine.guards 是实例字段）。

// checkClarifyOutputGuard clarify Stage 输出完整性校验（Phase 6 S4）。
// 校验：Requirement 非空（clarify 后 requirement 必须存在）。
func checkClarifyOutputGuard(state State) error {
	if strings.TrimSpace(state.Requirement) == "" {
		return fmt.Errorf("check_clarify_output: Requirement must not be empty after clarify")
	}
	return nil
}

// checkPlanOutputGuard plan Stage 输出完整性校验（Phase 6 S4）。
// 校验：GeneratedPlanJSON 非空（plan 后必须有方案）。
func checkPlanOutputGuard(state State) error {
	if strings.TrimSpace(state.GeneratedPlanJSON) == "" {
		return fmt.Errorf("check_plan_output: GeneratedPlanJSON must not be empty after plan")
	}
	return nil
}

// checkContractOutputGuard contract Stage 输出完整性校验（Phase 6 S4）。
// 校验：GeneratedPlanJSON 仍存在（contract 不修改 plan，仅确认）。
func checkContractOutputGuard(state State) error {
	if strings.TrimSpace(state.GeneratedPlanJSON) == "" {
		return fmt.Errorf("check_contract_output: GeneratedPlanJSON must not be empty after contract")
	}
	return nil
}

// checkImplementOutputGuard implement Stage 输出完整性校验（Phase 6 S4）。
// 校验：SubTasks 非空（implement 后必须有子任务执行结果）。
func checkImplementOutputGuard(state State) error {
	if len(state.SubTasks) == 0 {
		return fmt.Errorf("check_implement_output: SubTasks must not be empty after implement")
	}
	return nil
}
