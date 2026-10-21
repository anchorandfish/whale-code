package command

// daemonNotWiredNotice 是「本入口未接线守护进程控制面」的**唯一**文案来源。
//
// 为什么必须共享：/tasks 与 /daemon list 回答的是**同一个问题**（后台任务在不在跑）。
// 未接线时给出两个不同答案（"Background tasks: (none)" vs "Daemon not initialized"）
// = 同一入口下两个互相矛盾的事实断言 —— 而两者其实都只说明"没有控制通道"，
// 既不说明"没有任务"，也不必然说明"没有 daemon"（daemon 是按用户共享的进程，
// 可由 CLI/TUI 等其它入口启动，见 internal/daemon 的 ~/.whale/daemon 路径）。
//
// 契约（勿改）：必须①含子串 "not initialized"（8 条 daemon 命令与
// TestDaemonCommandsNoCallback 以此为契约）②不含任何任务数量断言
// （none / 0 tasks / no tasks / 暂无）。
func daemonNotWiredNotice() string {
	return "Daemon not initialized in this entry point"
}

// daemonCommands 返回 Daemon 类别的内置命令
//
// ★ 2026-10-12 收回隐藏（，用户口径「v1 无承诺的都不要有」）：
// 10-03 曾按审计件 §3-乙（/tasks 可见 ⇒ 必须管得着）全家族取消隐藏，但 daemon 家族
// 不在 10-10 判据册的 v1 承诺清单 ⇒ 收回；handler 保留、手输仍可执行，
// 登记见 内部工作回执（以后改版可整批恢复）。
func (r *Registry) daemonCommands() []Command {
	return []Command{
		{Name: "daemon start", Hidden: true, Description: "启动守护进程", Usage: "/daemon start", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("start"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon stop", Hidden: true, Description: "停止守护进程", Usage: "/daemon stop", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("stop"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon restart", Hidden: true, Description: "重启守护进程", Usage: "/daemon restart", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("restart"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon status", Hidden: true, Description: "查看守护进程状态", Usage: "/daemon status", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("status"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon add", Hidden: true, Description: "添加后台任务", Usage: "/daemon add <prompt>", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("add " + args), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon list", Hidden: true, Description: "列出后台任务", Usage: "/daemon list", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("list"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon cancel", Hidden: true, Description: "取消后台任务", Usage: "/daemon cancel <id>", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("cancel " + args), nil
				}
				return daemonNotWiredNotice(), nil
			}},
		{Name: "daemon log", Hidden: true, Description: "查看守护进程日志", Usage: "/daemon log", Category: "Daemon",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("log"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
	}
}
