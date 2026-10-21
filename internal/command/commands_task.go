package command

// taskCommands 返回 Task 类别的内置命令（N1 subagent / Task tool，）。
//
// 命令：
// - /task list：列出所有 Agent 子任务（pending/running/completed/failed/cancelled）
// - /task cancel <id>：取消指定子任务（cancel 传播到后代）
// - /task show <id>：显示单个子任务详情
//
// 与 /tasks（daemon 后台任务）区分：/task 用于 Agent 编排子任务。
// 所有子命令委托给 OnTaskCommand 回调（由 app.go 接线时注入）。
//
// ★ 2026-10-12 收回隐藏（，用户口径「v1 无承诺的都不要有」）：
// 10-03 曾按审计件 §3-乙（唯一用户出口 + 与 /tasks 消歧）取消隐藏，但 task 家族
// 不在 10-10 判据册的 v1 承诺清单 ⇒ 收回；handler 保留、手输仍可执行，登记见 回执。
func (r *Registry) taskCommands() []Command {
	return []Command{
		{Name: "task list", Hidden: true, Description: "列出子 Agent 任务", Usage: "/task list", Category: "Task",
			Handler: func(args string) (string, error) {
				if r.OnTaskCommand != nil {
					return r.OnTaskCommand("list"), nil
				}
				return "Task manager not initialized", nil
			}},
		{Name: "task cancel", Hidden: true, Description: "取消子 Agent 任务", Usage: "/task cancel <id>", Category: "Task",
			Handler: func(args string) (string, error) {
				if r.OnTaskCommand != nil {
					return r.OnTaskCommand("cancel " + args), nil
				}
				return "Task manager not initialized", nil
			}},
		{Name: "task show", Hidden: true, Description: "显示子 Agent 任务详情", Usage: "/task show <id>", Category: "Task",
			Handler: func(args string) (string, error) {
				if r.OnTaskCommand != nil {
					return r.OnTaskCommand("show " + args), nil
				}
				return "Task manager not initialized", nil
			}},
	}
}
