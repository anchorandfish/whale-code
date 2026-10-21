package tui

import (
	"fmt"
	"strings"
)

// handleWorkflow 处理 /workflow 子命令（P0-15 诚实降级实现）。
// 子命令：
//   - list：列出已保存的工作流模板（当前无持久化实现，返回空列表提示）
//   - run <name>：运行指定工作流（尚未实现，诚实降级提示）
//   - save-as <name>：将当前编排任务保存为工作流模板（尚未实现，诚实降级提示）
//   - 无参数：返回用法说明
//
// 设计说明：避免返回虚假成功信息，对未实现的能力明确告知用户，
// 替换原先 OnWorkflow 为 nil 时返回的 "Workflow commands not initialized" 提示。
func (m *Model) handleWorkflow(args string) (string, error) {
	args = strings.TrimSpace(args)
	parts := strings.Fields(args)

	// 无参数：返回用法
	if len(parts) == 0 {
		return "Usage: /workflow <subcommand> [args]\n" +
			"Subcommands:\n" +
			"  list                List saved workflow templates\n" +
			"  run <name>          Run a saved workflow\n" +
			"  save-as <name>      Save current orchestration task as a workflow template", nil
	}

	subCmd := parts[0]
	switch subCmd {
	case "list":
		// list：当前无持久化工作流存储，诚实返回空列表
		return "No saved workflows", nil
	case "run":
		// run <name>：运行工作流尚未实现，诚实降级
		return "Workflow run not yet implemented", nil
	case "save-as":
		// save-as <name>：保存工作流模板尚未实现，诚实降级
		return "Workflow save not yet implemented", nil
	default:
		return fmt.Sprintf("Unknown subcommand: %s\nUsage: /workflow [list|run <name>|save-as <name>]", subCmd), nil
	}
}
