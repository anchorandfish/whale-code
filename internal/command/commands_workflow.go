package command

// workflowCommands 返回 H10 Phase 2 工作流模板的 /workflow 命令（编排层 §6.4，02 §2.5.1 命令注册）。
// 通过 registerBuiltinCommands 注册（不自定义入口，零容忍）。
// 子命令：list / run <name> --input "需求" / save-as <name> [--task <task-id>]。
// 采用闭包 + 回调模式（与 OnOrchestrate/OnGoal 一致），单入口 + args 解析避免 commandCompletions 爆炸。
func (r *Registry) workflowCommands() []Command {
	return []Command{
		{Name: "workflow", Hidden: true, Description: "管理工作流模板（子命令：list/run/save-as）", Usage: "/workflow <subcommand> [args]", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnWorkflow != nil {
					return r.OnWorkflow(args)
				}
				return "Workflow commands not initialized", nil
			}},
	}
}
