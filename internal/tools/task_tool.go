// Package tools — N1 subagent / Task tool 的**执行体**。
//
// ⚠️ G7：本文件**不再实现 tools.Tool 接口** —— 它是 Task tool 的**执行入口**，
// 不是 Task 的模型可见定义。
//
// 为什么（G6 实测 → G7 结构性根治）：
//
//	模型真正看到的 Task 定义**不在本文件**，而在 `tools.go` 的 `[]Spec` 表
//	（`registerBuiltinTools` → `registry.Get("Task")` → 子 agent 面 `AgentContext.buildAgentTools`
//	/ 主会话面 `registry.AllDirect()`）。G6 实测本文件此前对 `Tool` 接口的**整段实现**
//	（16 个方法：Name / Prompt / Description / Schema / ValidateInput / CheckPermissions /
//	IsEnabled / IsConcurrencySafe / IsReadOnly / IsDestructive / InterruptBehavior / Exposure /
//	RequiredSandboxLevel / MapToolResultToAPI / RenderToolUseMessage / RenderToolResultMessage）
//	**全仓零调用点** —— 生产码只调 `Call`（`executor.go` 的 `taskHandler`，且每次新建实例）。
//	留着那 16 个方法＝留一个「读本文件会以为它才是 Task 定义」的审计陷阱
//	（G6-① 已因此险些改错落点）⇒ G7 删死方法，只留执行入口。
//
//	守卫：`g6_tools_not_in_toolface_test.go` 的 **P2** 用 AST 钉住「本文件只暴露 Call」，
//	钉住降级本身；加回任何一个 Tool 面方法都会让它变红。
//
// 设计要点（现由 Call + 上游 taskHandler 承载）：
// - 只读编排：Task tool 不直接修改工作树，实际写操作由子任务内工具各自走权限检查（v2 P-09）；
// 子任务复用父 Pipeline，每个工具调用经 7 阶段管道校验。
// - 权限面：Task 工具本身**不经 Tool 接口的权限路径**（已无 CheckPermissions）；
// 原「Task 工具恒 Allow」这一属性由此变为**结构上不可能被违反**，
// 其表征见 `subagent_audit_char_test.go` 的 `TestChar_S4_I2_TaskToolIsNotATool`
// （G7 有意翻转：表征对象已不存在，改用更强的结构断言）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// TaskTool 是 Task tool 的**执行体**：持有 AgentTaskManager，Call 委托 manager.Spawn。
//
// ⚠️ 它**不是** tools.Tool 实现（见文件头）—— 唯一消费者是 `executor.go` 的 `taskHandler`。
// 不要在此加回 Tool 接口方法（Name / Prompt / Schema / …）：那会重建「双真相源」，
// 下一个读代码的人会再次以为模型看到的是本文件。
type TaskTool struct {
	manager *AgentTaskManager
}

// NewTaskTool 创建 TaskTool 执行体。
// manager：Agent 任务管理器，负责子任务的编排与生命周期。
func NewTaskTool(manager *AgentTaskManager) *TaskTool {
	return &TaskTool{manager: manager}
}

// taskToolParams 是 Task tool 的 JSON 入参结构。
//
// ⚠️ G6：`Tools` **保留**只为向后兼容（旧调用方 / 模型提示缓存里的旧 schema 入参），
// 它**不参与任何能力判定**，也不在任何 Schema 里被广告（模型可见定义在 tools.go 的 Task Spec）。
// 能力唯一真相源 = 子 agent 的 AgentType（角色面）∩ ParentToolSet（agent.go:1235）。
type taskToolParams struct {
	Prompt string   `json:"prompt"`
	Tools  []string `json:"tools,omitempty"` // 兼容保留：不参与能力判定，见上
	Model  string   `json:"model,omitempty"`
}

// Call 执行 Task tool：
// 1. 解析 JSON input（prompt/model；tools 为兼容字段，见 taskToolParams）
// 2. 从 ctx 提取父任务信息（parentID/parentDepth）
// 3. 构建 AgentInput（ParentCtx = &ctx）
// 4. 调用 manager.Spawn
// 5. 返回 ToolResult
func (t *TaskTool) Call(ctx context.Context, input json.RawMessage) (<-chan ToolProgress, *ToolResult, error) {
	var params taskToolParams
	if err := json.Unmarshal(input, &params); err != nil {
		return nil, &ToolResult{Content: fmt.Sprintf("parse input failed: %v", err), IsError: true}, nil
	}
	if params.Prompt == "" {
		return nil, &ToolResult{Content: "prompt is required for Task tool", IsError: true}, nil
	}

	// 从 ctx 提取父任务信息（parentID/parentDepth）
	parentID, parentDepth := extractParentInfo(ctx)

	// 构建 AgentInput（ParentCtx = &ctx，携带 AgentID 与深度信息）
	// 注意：parentCtx 指向 ctx 本身，子任务通过 extractAgentID 提取 AgentID
	//
	// L2：Task 的子任务角色在**这里显式声明** —— 本工具的参数里没有 agent_type
	// （taskToolParams 只有 prompt/tools/model），因此不能再依赖 prompt 关键字推断
	// （inferAgentType 已删除）。Task 的语义是「执行一个独立子任务」，故声明为通用
	// 写角色 coder；它是**声明**而非默认值，因此仍然被强制（childHiddenTools 照常扣除）。
	parentCtx := ctx
	agentInput := AgentInput{
		Prompt:    params.Prompt,
		AgentType: string(AgentTypeCoder),
		Tools:     params.Tools, // G6：兼容搬运 —— AgentInput.Tools 生产码零读取点，不参与工具面
		Model:     params.Model,
		ParentCtx: &parentCtx,
	}

	// 调用 manager.Spawn
	out, err := t.manager.Spawn(ctx, agentInput, parentID, parentDepth)
	if err != nil {
		errMsg := fmt.Sprintf("Task failed: %v", err)
		if out.TaskID != "" {
			errMsg = fmt.Sprintf("Task %s failed: %v", out.TaskID, err)
		}
		return nil, &ToolResult{
			Content:     errMsg,
			IsError:     true,
			FailureKind: out.FailureKind,
			Confirm:     out.Confirm,
		}, nil
	}

	// 成功：返回子任务结果与 TaskID
	content := out.Result
	if content == "" {
		content = fmt.Sprintf("Task %s completed (no output)", out.TaskID)
	}
	rendered := fmt.Sprintf("[Task %s %s]\n%s", out.TaskID, out.Status.String(), content)

	// R3/L3.2：子 agent 内被**终局拒绝**时，产物必须带结构化标记且
	// IsError=true。修复前这里恒 IsError=false ⇒ Task 工具"看起来成功"
	// （现场：/task list 仍标 ✅），上层拿不到"子 agent 被拒"这一事实，
	// 子 agent 也就失去了被终止轮次的理由（F6：被拒后自由换工具重试）。
	// IK3：只有 IsError=true 才允许携带 FailureKind。
	if out.FailureKind != "" {
		return nil, &ToolResult{
			Content:     rendered,
			IsError:     true,
			FailureKind: out.FailureKind,
			Confirm:     out.Confirm,
		}, nil
	}
	return nil, &ToolResult{Content: rendered}, nil
}
