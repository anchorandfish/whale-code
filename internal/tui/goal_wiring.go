// Package tui — goal_wiring.go N26 goal 命令接线层（+）
//
// 本文件实现 TUI 路径的 goal.Manager 桥接：
// - evalAgentAdapter：轻量 tools.Agent 实现（包装 AI client，只读执行评估 prompt）
// - newGoalManager：构造 goal.Manager + 注入 4 个回调桥接（compact/launchTurn/hasUserInput/collectEvidence）
// - goalCmdOutput：/goal 子命令解析 + 分发到 Manager 方法（Model 与 runnerCore 共用）
//
// 对标同类 CLI 的 goal-extension 与同类 CLI /goal v2.1.139。
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/goal"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// evalAgentAdapter 轻量评估 Agent 实现（N1 只读复用，）。
//
// 包装 AI client，仅执行 prompt → 返回文本输出，不走工具执行链路（Tools=空）。
// 对标同类 CLI "fast model checks whether the condition holds"。
// 不维护状态，每次 Execute 独立调用（v4 修订：消除竞态）。
type evalAgentAdapter struct {
	client *ai.SimpleClient
}

// Execute 实现 tools.Agent 接口，调用 AI client 流式输出并收集文本。
//
// 流程：
// 1. 构造 ai.Request（Model 来自 AgentInput，空时 fallback 到 client 默认模型）
// 2. 调 client.Stream 收集 TextDeltaEvent
// 3. 返回 AgentOutput（Result=拼接文本，Usage=token 用量）
//
// 超时由调用方（goal.eval.go runEvaluation）通过 context 控制。
func (a *evalAgentAdapter) Execute(ctx context.Context, input tools.AgentInput) (tools.AgentOutput, error) {
	if a.client == nil {
		return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("eval agent: AI client not configured")
	}

	model := input.Model
	if model == "" {
		model = a.client.Model()
	}

	req := ai.Request{
		Model:     model,
		MaxTokens: 1280, // evalMaxOutputTokens（对标 eval.go const）
		Messages: []ai.MessageEntry{
			{Role: "user", Content: input.Prompt},
		},
	}

	stream, err := a.client.Stream(ctx, req)
	if err != nil {
		return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("eval agent stream: %w", err)
	}

	var sb strings.Builder
	var usage tools.TokenUsage
	for event := range stream {
		switch e := event.(type) {
		case ai.TextDeltaEvent:
			sb.WriteString(e.Content)
		case ai.UsageEvent:
			usage.InputTokens = int64(e.InputTokens)
			usage.OutputTokens = int64(e.OutputTokens)
			usage.CacheCreationTokens = int64(e.CacheCreation)
			usage.CacheReadTokens = int64(e.CacheRead)
		case ai.ErrorEvent:
			return tools.AgentOutput{Status: tools.TaskFailed}, fmt.Errorf("eval agent stream error: %w", e.Err)
		}
	}

	return tools.AgentOutput{
		Result: sb.String(),
		Status: tools.TaskCompleted,
		Usage:  &usage,
	}, nil
}

// newGoalManager 构造 goal.Manager 并注入 TUI 桥接回调。
//
// 参数：
// - homeDir：用户主目录（goal JSON 存储于 ~/.whale/goals/）
// - client：AI client（包装为 evalAgentAdapter）
// - metricsSvc：N2 全局累加（metrics.Default）
// - usageTracker：N2 by-agent 分桶
// - sendPrompt：续跑时启动新 turn 的回调（m.SendPrompt）
// - hasUserInput：用户输入检查（textarea 非空判断）
// - collectEvidence：证据收集回调（从 FileHistory + session 提取）
//
// compact 回调当前为 no-op（：依赖 413 reactive compact，不在 goal 层主动检查）。
// 后续若需主动 compact，可在此注入 ctxpkg.CompactionPipeline.Execute。
func newGoalManager(
	homeDir string,
	client *ai.SimpleClient,
	metricsSvc *metrics.Metrics,
	usageTracker *ai.UsageTracker,
	sendPrompt func(prompt string),
	hasUserInput func() bool,
	collectEvidence func(ctx context.Context, threadID string, turnResult goal.TurnResult) (goal.Evidence, error),
) *goal.Manager {
	// 1. store：JSON 文件持久化（~/.whale/goals/）
	store := goal.NewFileStore(homeDir + "/.whale/goals")

	// 2. evalAgent：轻量评估 Agent（包装 AI client）
	evalAgent := &evalAgentAdapter{client: client}

	// 3. compact 回调：no-op（，依赖 413 reactive compact）
	compactFn := func(ctx context.Context, threadID string) (int, error) {
		return 0, nil
	}

	// 4. launchTurn 回调：调 sendPrompt 启动新 turn
	launchTurnFn := func(ctx context.Context, threadID string, continuationPrompt string) error {
		if sendPrompt == nil {
			return fmt.Errorf("goal: sendPrompt callback not configured")
		}
		sendPrompt(continuationPrompt)
		return nil
	}

	// 5. hasUserInput 回调：检查 textarea 是否有内容
	hasInputFn := func() bool {
		if hasUserInput == nil {
			return false
		}
		return hasUserInput()
	}

	// 6. collectEvidenceFn 回调：委托给传入的收集函数
	collectEvidenceFn := func(ctx context.Context, threadID string, turnResult goal.TurnResult) (goal.Evidence, error) {
		if collectEvidence == nil {
			return goal.Evidence{}, nil
		}
		return collectEvidence(ctx, threadID, turnResult)
	}

	return goal.NewManager(
		store,
		evalAgent,
		metricsSvc,
		usageTracker,
		compactFn,
		launchTurnFn,
		hasInputFn,
		collectEvidenceFn,
	)
}

// goalCmdOutput 是 `/goal` 命令的**唯一**实现。
//
// ★ 2026-10-13（批 3）结构性收口：`Model.handleGoalCmd` 与
// `runnerCore.handleRunnerGoalCmd` 原为**两份逐字重复**的实现（“「双实现必漂移」），
// 差异仅在取字段的方式（`m.sessionService.SessionName()` vs `rc.SessionName`）。
// 现统一收进本函数，两处只做薄转发 ⇒ 逻辑与文案不可能再各改一边。
//
// 子命令：
// - set <objective>：设置目标（默认预算 100K tokens）
// - status：查看当前目标状态（未带子命令时默认）
// - pause / resume：暂停 / 恢复目标续跑
// - clear：清除目标
// - edit <objective>：修改目标文本
// - done [--force]：直接标记完成
// - list：列出历史目标
// - help：查看帮助
func goalCmdOutput(manager *goal.Manager, threadID, args string) string {
	if manager == nil {
		return bi("Goal manager not initialized", "目标管理器未初始化")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
	subCmd := parts[0]
	if subCmd == "" {
		subCmd = "status"
	}

	var subArgs string
	if len(parts) > 1 {
		subArgs = strings.TrimSpace(parts[1])
	}

	switch subCmd {
	case "set":
		if subArgs == "" {
			return bi("Usage: /goal set <objective>", "用法：/goal set <目标>")
		}
		// 默认预算 100K tokens（对标同类 CLI 默认预算）
		oldGoal, err := manager.SetGoal(ctx, threadID, subArgs, 100000)
		if err != nil {
			return biSame("Failed to set goal: %v", "设置目标失败: %v", err)
		}
		if oldGoal != nil && oldGoal.Status == goal.StatusActive {
			return biSame("Replaced the previous goal (was: %s)\nNew goal set: %s",
				"已替换旧目标（旧目标: %s）\n新目标已设置: %s", oldGoal.Objective, subArgs)
		}
		return biSame("Goal set: %s", "目标已设置: %s", subArgs)

	case "status":
		g, err := manager.GetGoal(ctx, threadID)
		if err != nil {
			return biSame("Failed to read goal: %v", "读取目标失败: %v", err)
		}
		if g == nil {
			return bi("No goal for the current session. Use /goal set <objective> to set one.",
				"当前会话无目标。使用 /goal set <objective> 设置目标。")
		}
		return biSame("Goal: %s\nStatus: %s\nCreated: %s\nUpdated: %s\nIsUntrusted: %v",
			"目标: %s\n状态: %s\n创建时间: %s\n更新时间: %s\nIsUntrusted: %v",
			g.Objective, g.Status,
			time.Unix(g.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			time.Unix(g.UpdatedAt, 0).Format("2006-01-02 15:04:05"),
			g.IsUntrusted)

	case "pause":
		if err := manager.UpdateStatus(ctx, threadID, goal.StatusPaused); err != nil {
			return biSame("Failed to pause: %v", "暂停失败: %v", err)
		}
		return bi("Goal paused", "目标已暂停")

	case "resume":
		if err := manager.UpdateStatus(ctx, threadID, goal.StatusActive); err != nil {
			return biSame("Failed to resume: %v", "恢复失败: %v", err)
		}
		return bi("Goal resumed", "目标已恢复")

	case "clear":
		if err := manager.ClearGoal(ctx, threadID); err != nil {
			return biSame("Failed to clear: %v", "清除失败: %v", err)
		}
		return bi("Goal cleared", "目标已清除")

	case "edit":
		if subArgs == "" {
			return bi("Usage: /goal edit <objective>", "用法：/goal edit <目标>")
		}
		if err := manager.EditObjective(ctx, threadID, subArgs); err != nil {
			return biSame("Failed to edit: %v", "修改失败: %v", err)
		}
		return biSame("Goal updated: %s", "目标已修改: %s", subArgs)

	case "done":
		force := strings.Contains(subArgs, "--force")
		evalResult, err := manager.MarkCompleteDirect(ctx, threadID, force)
		if err != nil {
			return biSame("Failed to mark complete: %v", "标记完成失败: %v", err)
		}
		if evalResult != nil && evalResult.Unverified {
			return bi("Goal marked complete (⚠️ unverified: evaluation agent skipped, )",
				"目标已标记完成（⚠️ 未验证：跳过评估 Agent，）")
		}
		return bi("Goal marked complete", "目标已标记完成")

	case "list":
		goals, err := manager.ListGoals(ctx)
		if err != nil {
			return biSame("Failed to list goals: %v", "列出失败: %v", err)
		}
		if len(goals) == 0 {
			return bi("No goal history", "无历史目标")
		}
		var sb strings.Builder
		sb.WriteString(biSame("Goal history (%d):", "历史目标 (%d 条):", len(goals)) + "\n")
		for i, g := range goals {
			sb.WriteString(fmt.Sprintf("  %d. [%s] %s\n", i+1, g.Status, g.Objective))
		}
		return sb.String()

	case "help":
		return bi("Goal command help:\n"+
			"  /goal set <objective>  Set a long-term goal\n"+
			"  /goal status           Show the current goal status\n"+
			"  /goal pause            Pause goal continuation\n"+
			"  /goal resume           Resume goal continuation\n"+
			"  /goal clear            Clear the goal\n"+
			"  /goal edit <objective> Edit the goal text\n"+
			"  /goal done [--force]   Mark complete directly\n"+
			"  /goal list             List goal history\n"+
			"  /goal help             Show this help",
			"Goal 命令帮助:\n"+
				"  /goal set <objective>  设置长期目标\n"+
				"  /goal status           查看当前目标状态\n"+
				"  /goal pause            暂停目标续跑\n"+
				"  /goal resume           恢复目标续跑\n"+
				"  /goal clear            清除目标\n"+
				"  /goal edit <objective> 修改目标文本\n"+
				"  /goal done [--force]   直接标记完成\n"+
				"  /goal list             列出历史目标\n"+
				"  /goal help             查看帮助")

	default:
		return biSame("Unknown subcommand: %s\nUse /goal help for help", "未知子命令: %s\n使用 /goal help 查看帮助", subCmd)
	}
}

// handleGoalCmd 解析 /goal 子命令并分发到 goal.Manager（TUI 路径，）。
//
// 逻辑与文案见 goalCmdOutput（与 runnerCore.handleRunnerGoalCmd 同源）。
func (m *Model) handleGoalCmd(args string) string {
	return goalCmdOutput(m.goalManager, m.sessionService.SessionName(), args)
}

// collectGoalEvidence 收集本轮证据（从 FileHistory + session 消息提取，）。
//
// Evidence 字段：
// - FilesChanged：从 FileHistory 获取最近快照的文件列表（S3 修复：补齐提取，强化 T4 防护）
// - TestResult：从 session 最近 N 条 tool_result 提取测试输出摘要
// - CommandOutput：从 session tool_result 提取关键命令输出
// - Summary：从 session 最近一条 assistant 消息文本提取
func (m *Model) collectGoalEvidence(ctx context.Context, threadID string, turnResult goal.TurnResult) (goal.Evidence, error) {
	var evidence goal.Evidence

	// 1. FilesChanged：从 FileHistory 获取最近快照的文件列表（S3 修复）
	// 取最新快照的 Files map keys 作为本轮改动文件证据
	if exec := m.toolService.Executor(); exec != nil {
		if fh := exec.FileHistoryGetter(); fh != nil {
			if snapshots, err := fh.ListSnapshots(); err == nil && len(snapshots) > 0 {
				// 取最新快照（ListSnapshots 返回顺序按 indexCache，最新在末尾）
				latest := snapshots[len(snapshots)-1]
				if snap, err := fh.GetSnapshot(latest.MsgId); err == nil && snap != nil {
					for path := range snap.Files {
						evidence.FilesChanged = append(evidence.FilesChanged, path)
					}
				}
			}
		}
	}

	// 2. Summary：从 session 最近一条 assistant 消息提取
	if sess := m.sessionService.CurrentSession(); sess != nil {
		msgs := sess.GetMessages()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "assistant" {
				for _, block := range msgs[i].Blocks {
					if block.Type == "text" && block.Text != "" {
						evidence.Summary = block.Text
						break
					}
				}
				break
			}
		}

		// 3. TestResult / CommandOutput：从最近 tool_result 提取
		for i := len(msgs) - 1; i >= 0 && i >= len(msgs)-10; i-- {
			if msgs[i].Role == "tool" {
				for _, block := range msgs[i].Blocks {
					if block.Type == "tool_result" && block.Text != "" {
						if strings.Contains(block.Text, "PASS") || strings.Contains(block.Text, "FAIL") || strings.Contains(block.Text, "test") {
							if evidence.TestResult == "" {
								evidence.TestResult = block.Text
							}
						} else if evidence.CommandOutput == "" {
							evidence.CommandOutput = block.Text
						}
					}
				}
			}
		}
	}

	return evidence, nil
}

// hasUserInputPending 检查 textarea 是否有未发送的用户输入（，续跑前调用）。
func (m *Model) hasUserInputPending() bool {
	if m.uiService.Textarea() == nil {
		return false
	}
	return strings.TrimSpace(m.uiService.Textarea().Value()) != ""
}

// handleRunnerGoalCmd 解析 /goal 子命令并分发到 goal.Manager（plain/bridge 路径，）。
//
// 与 Model.handleGoalCmd 共用 goalCmdOutput（2026-10-13 “ 收口：原为逐字重复的第二份实现）。
// plain/bridge 模式下 hasUserInput/collectEvidence 回调为 nil（评估 Agent 不强依赖）。
func (rc *runnerCore) handleRunnerGoalCmd(args string) string {
	return goalCmdOutput(rc.goalManager, rc.SessionName, args)
}
