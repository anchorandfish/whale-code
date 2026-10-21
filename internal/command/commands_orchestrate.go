package command

// orchestrateCommands 返回 H10 智能编排系统的 /orchestrate 命令（02 §2.5.1 命令注册，02 §2.7.5 契约）。
// 通过 registerBuiltinCommands 注册（不自定义入口，零容忍）。
// 子命令：start <需求> / status [task-id] / list / resume <task-id> / abort <task-id>。
func (r *Registry) orchestrateCommands() []Command {
	return []Command{
		{Name: "orchestrate", Hidden: true, Description: "运行智能编排任务（子命令：start/status/list/resume/abort）", Usage: "/orchestrate <subcommand> [args]", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnOrchestrate != nil {
					return r.OnOrchestrate(args)
				}
				return "Orchestrator not initialized", nil
			}},
	}
}
