package command

// goalCommands 返回 Automation 类别的内置命令（N26 长期目标系统，）。
//
// 采用单入口 + args 解析模式（对标 /review），而非注册 9 个独立命令（避免 commandCompletions 爆炸）。
// 所有子命令委托给 OnGoal 回调（由 app.go 接线时注入 goal.Manager 的 args 解析层）。
//
// 命令：
// - /goal set <objective>：设置长期目标（覆盖旧目标，返回旧目标供确认）
// - /goal status：查看当前目标状态（active/paused/blocked/usage_limited/budget_limited/complete）
// - /goal pause：暂停目标续跑（不丢失状态）
// - /goal resume：恢复目标续跑（重置 blocked_count）
// - /goal clear：清除目标（终态）
// - /goal edit <objective>：修改目标文本（触发 IsUntrusted 重检测）
// - /goal done [--force]：直接标记完成（跳过评估 Agent，标 Unverified=true，）
// - /goal list：列出历史目标（持久化 JSON）
// - /goal help：查看帮助
//
// 对标同类 CLI 的 goal-extension 与同类 CLI /goal v2.1.139。
func (r *Registry) goalCommands() []Command {
	return []Command{
		{Name: "goal", Hidden: true, Description: "管理长期目标（子命令见 /goal help）", Usage: "/goal <subcommand> [args]", Category: "Automation",
			Handler: func(args string) (string, error) {
				if r.OnGoal != nil {
					return r.OnGoal(args), nil
				}
				return "Goal manager not initialized", nil
			}},
	}
}
