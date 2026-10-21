package command

import "github.com/anchorandfish/whale-code/internal/util"

// monitorsNotWiredNotice 是「本入口未接线后台监控控制面」的**唯一**文案来源（同姿态）。
//
// 为什么不能沿用手写的 "No active monitors"：那句话断言了一个**运行期事实**
// （"当前没有监控在跑"），而真实状态只是"本入口没有监控控制通道" ——
// 未接线时既不知道有没有监控在跑，也停不掉任何一个。把「未接线」说成「空态」
// 正是 （/tasks 未接线却报 "none"）的同一反模式：**未接线不得断言事实**。
//
// 为什么不复用 daemonNotWiredNotice()：那不是"同一事实"。 要求同源的是
// 「/tasks 与 /daemon list 回答同一个问题（后台任务在不在跑）」这一对；而 /monitors
// 问的是 **monitor.Manager** 管的另一批进程，两者的接线状态彼此独立
// （TUI 里监控已接线、daemon 未必）—— 共用一句话会把两件独立的事绑成一个断言。
//
// 契约（勿改）：① 含子串 "not wired"（「未接线」的机械锚点）；
// ② 不含任何运行期存在性/数量断言（no active / none / 0 monitors / 暂无 / 没有）。
func monitorsNotWiredNotice() string {
	return util.Bi(
		"Monitor control is not wired in this entry point",
		"本入口未接线监控控制面",
	)
}

// stubPlannedFeature 返回「规划中功能」桩命令的**唯一**诚实降级文案（C-5，2026-10-10）。
// mark/unmark/tag 共用同一实现，避免三份拷贝漂移。
func stubPlannedFeature(name string) func(args string) (string, error) {
	return func(args string) (string, error) {
		return util.Bi(
			"/"+name+" is not yet implemented (planned feature)",
			"/"+name+" 尚未实现（规划中功能）",
		), nil
	}
}

// coreCommands 返回 Core 类别的内置命令
func (r *Registry) coreCommands() []Command {
	return []Command{
		// 2026-10-14 Usage 审计：FormatHelp 早就支持 --all/--search/--filter（/M09-04），
		// 但 Usage 一直没写 ⇒ 三个既有能力全部不可发现（本轮同类问题的最后一批实例）。
		{Name: "help", Description: "显示命令列表", Usage: "/help [--all | --search <keyword> | --filter <cat>]", Category: "Core",
			Handler: func(args string) (string, error) { return FormatHelp(r, args), nil }},
		{Name: "compact", Description: "压缩上下文", Usage: "/compact [force] [--ai]", Category: "Core",
			Handler: func(args string) (string, error) { return "Compressing...", nil }},
		{Name: "archive", Description: "查看/导出压缩归档（只读）", Usage: "/archive [view N|export path]", Category: "Core",
			Handler: func(args string) (string, error) {
				return util.Bi("The archive command is only available in an interactive session", "archive 命令仅在交互会话中可用"), nil
			}},
		{Name: "clear", Description: "清空对话", Usage: "/clear --confirm", Category: "Core",
			Handler: func(args string) (string, error) { return "Conversation cleared", nil }},
		{Name: "exit", Description: "退出程序", Category: "Core",
			Handler: func(args string) (string, error) { return "__EXIT__", nil }},
		{Name: "version", Description: "显示版本", Category: "Core",
			Handler: func(args string) (string, error) { return "Whale Code v" + r.Version, nil }},
		{Name: "status", Description: "显示完整状态", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnStatus != nil {
					return r.OnStatus(), nil
				}
				return "Status: Use /context for context info, /usage for token details", nil
			}},
		{Name: "mouse", Hidden: true, Description: "切换 TUI 鼠标捕获（on/off/status）", Usage: "/mouse on|off|status", Category: "Core",
			Handler: func(args string) (string, error) {
				return util.Bi("Mouse mode can only be toggled in the TUI; use /mouse on|off|status.", "鼠标模式仅在 TUI 中可切换；请使用 /mouse on|off|status。"), nil
			}},
		{Name: "doctor", Description: "诊断检查（环境/配置/连通性）", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnDoctor != nil {
					return r.OnDoctor(args)
				}
				return util.Bi("doctor is not wired", "doctor 未接线"), nil
			}},
		{Name: "think", Description: "切换思考模式", Usage: "/think on|off|default", Category: "Core",
			Handler: func(args string) (string, error) {
				switch args {
				case "on":
					return "Thinking mode enabled\nWarning: the model will output its thinking process; token usage may increase 4~26x", nil
				case "off":
					return "Thinking mode disabled", nil
				default:
					return "Thinking mode reset to default", nil
				}
			}},
		{Name: "model", Description: "查看/切换模型", Usage: "/model [add|remove|edit|info] [name] [options]", Category: "Model",
			Handler: func(args string) (string, error) {
				if args == "" {
					return "Current model info", nil
				}
				return "Model switched to: " + args, nil
			}},
		{Name: "models", Description: "列出所有可用模型", Category: "Model",
			Handler: func(args string) (string, error) { return "Available models list", nil }},
		// ★ 2026-10-12 收回隐藏（，用户口径「v1 无承诺的都不要有」）：
		// 10-03 曾因 README 宣传取消隐藏，但 /fast 不在 10-10 判据册的 v1 承诺清单 ⇒ 收回；
		// handler 保留、手输仍可执行，登记见 内部工作回执。
		{Name: "fast", Hidden: true, Description: "快速模式开关（切换到快速模型）", Usage: "/fast [on|off|status]", Category: "Model",
			Handler: func(args string) (string, error) { return r.handleFastCommand(args) }},
		// ★ 2026-09-30（用户真机反馈后拍板）：**取消 Hidden**，并把家族子命令一并登记。
		//
		// 为什么原先是 Hidden：V0.1-HIDDEN 按「产品显要度」收敛命令面，它被归入"小众调参/调试"组
		// （见 command_hidden_test.go 的 hiddenCommandSamples）。那个分类**在该轴上是自洽的**，
		// 但轴本身漏了一个取值：**「是不是唯一出口」**。
		// `/monitors stop <id>` 是**单个后台监控进程的唯一停止出口**（TUI 侧
		// `internal/tui/format.go handleMonitors` 是 `MonitorMgr().Stop()` 的唯一用户入口）。
		//
		// 代价是实测出来的：Hidden ⇒ 默认 /help 不列、`/` 弹窗不提示（completion_popup.go:252
		// 直接 `continue`）、did-you-mean 不建议 ⇒ 用户**只能靠已知**去敲它。
		// 而上一轮把"怎么停"的提示写在了 `/monitors` **自己的输出里** —— 要看到它，得先知道它。
		// 形式化表述：**唯一出口不可发现 = 该能力不存在**。
		//
		// 为什么连子命令一起登记（有意翻转 内部工作回执…回执 §3
		// 的「Usage 逐字不变、不新增 tail」）：弹窗是对**注册名**做前缀匹配（typeahead.go:146），
		// 且 token 带尾随空格时精确命中即返回 nil（typeahead.go:130-136）⇒ 只登记 `monitors`
		// 的话，敲 `/monitors ` 弹窗直接关闭，`stop`/`tail` 永远不可能被提示。
		// 该回执拒登 tail 的理由是「注册侧即使接线也无法提供」——但 TUI 由
		// `isTUICommandHandledLocally("monitors")` **短路**、从不走注册侧 Handler，
		// 故 Usage 声明的是**命令面能力**而非**回调能力**，而 TUI 确实支持 tail。
		// （`show` 是 handleMonitors 里 tail 的**兼容别名**，有意不登记、不宣传 ⇒ 不构成承诺。）
		{Name: "monitors", Description: "列出/停止/查看监控进程", Usage: "/monitors [stop <id> | tail <id>]", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnMonitors != nil {
					return r.OnMonitors(args), nil
				}
				return monitorsNotWiredNotice(), nil
			}},
		// 家族子命令：注册全名（与 /task list、/task cancel、/checkpoint save 同构）。
		// 注册全名有两个不可省的作用：① 弹窗可提示；② `command.Parse` 靠 `knownPrefixes`
		// 才能把 `/monitors stop m-1` 切成 (name="monitors stop", args="m-1")。
		{Name: "monitors stop", Description: "停止指定的监控进程", Usage: "/monitors stop <id>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnMonitors != nil {
					return r.OnMonitors("stop " + args), nil
				}
				return monitorsNotWiredNotice(), nil
			}},
		{Name: "monitors tail", Description: "查看监控进程的近期输出", Usage: "/monitors tail <id>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnMonitors != nil {
					return r.OnMonitors("tail " + args), nil
				}
				return monitorsNotWiredNotice(), nil
			}},
		{Name: "upgrade", Description: "检查并安装更新", Usage: "/upgrade [check|install]", Category: "Core",
			Handler: func(args string) (string, error) { return r.handleUpgrade(args) }},
		{Name: "feedback", Description: "提交反馈或 Bug 报告", Usage: "/feedback <message>", Category: "Core",
			Handler: func(args string) (string, error) { return r.handleFeedback(args) }},
		{Name: "compact-strategy", Hidden: true, Description: "切换压缩策略", Usage: "/compact-strategy balanced|preserve-user|aggressive", Category: "Core",
			Handler: func(args string) (string, error) {
				strategies := []string{"balanced", "preserve-user", "aggressive"}
				if args == "" {
					if r.OnCompactStrategy != nil {
						return r.OnCompactStrategy(""), nil
					}
					return "Current compaction strategy: balanced\nOptions: balanced, preserve-user, aggressive", nil
				}
				valid := false
				for _, s := range strategies {
					if args == s {
						valid = true
						break
					}
				}
				if !valid {
					return "Invalid strategy: " + args + "\nOptions: balanced, preserve-user, aggressive", nil
				}
				if r.OnCompactStrategy != nil {
					return r.OnCompactStrategy(args), nil
				}
				return "Compaction strategy switched to: " + args + " (Note: strategy only takes effect in memory, reset on restart)", nil
			}},
		{Name: "context", Description: "显示当前上下文信息", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnContext != nil {
					return r.OnContext(), nil
				}
				return "Context info:\n  Messages: -\n  Token usage: -\n  Compactions: -\n  Cache hit rate: -", nil
			}},
		{Name: "plan", Description: "切换计划模式（只读）", Usage: "/plan [on|off]", Category: "Core", Stub: true,
			Handler: func(args string) (string, error) {
				switch args {
				case "on":
					return "Plan mode enabled — AI will only analyze, not execute", nil
				case "off":
					return "Plan mode disabled", nil
				default:
					return "Plan mode toggled", nil
				}
			}},
		{Name: "privacy", Hidden: true, Description: "显示/设置隐私选项", Usage: "/privacy [show|analytics-off|analytics-on]", Category: "Core",
			Handler: func(args string) (string, error) {
				if args == "" || args == "show" {
					return "Privacy settings:\n  Telemetry analytics: Off\n  Error reporting: Off\n  Usage data: Not shared", nil
				}
				return "Privacy settings updated: " + args, nil
			}},
		{Name: "stats", Hidden: true, Description: "显示详细统计", Category: "Core",
			Handler: func(args string) (string, error) {
				return r.handleStats(), nil
			}},
		{Name: "summary", Hidden: true, Description: "生成对话摘要", Category: "Core",
			Handler: func(args string) (string, error) {
				return r.handleSummary(), nil
			}},
		{Name: "thinkback", Hidden: true, Description: "回顾思考过程", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnThinkback != nil {
					return r.OnThinkback(), nil
				}
				return "No thinking data available for this session", nil
			}},
		{Name: "usage", Description: "显示 token 用量详情", Category: "Core",
			Handler: func(args string) (string, error) {
				return r.handleUsage(), nil
			}},
		{Name: "voice", Description: "切换语音输入模式", Usage: "/voice [on|off]", Category: "Core", Stub: true,
			Handler: func(args string) (string, error) {
				switch args {
				case "on":
					return "Voice input mode enabled", nil
				case "off":
					return "Voice input mode disabled", nil
				default:
					return "Voice input mode toggled", nil
				}
			}},
		{Name: "break-cache", Description: "强制清除提示缓存", Category: "Core", Stub: true,
			Handler: func(args string) (string, error) {
				return "Cache cleared — next request will send full content", nil
			}},
		// mark/unmark/tag（C-5 守卫门修复，2026-10-10）：TUI 曾保留这三个「诚实降级」case，
		// 但 command 包从未注册 ⇒ handleCommand 先 Get() 失败回 Unknown，case 永远走不到
		// （守卫门 internal/tui/command_registry_guard_test.go 首跑即红）。修复 = 注册侧补
		// Stub + TUI 删死 case，命令面 SSOT 收口回注册表。Handler 共用同一实现。
		{Name: "mark", Description: "记录会话要点", Usage: "/mark <note>", Category: "Core", Stub: true,
			Handler: stubPlannedFeature("mark")},
		{Name: "unmark", Description: "移除会话标记", Usage: "/unmark <note>", Category: "Core", Stub: true,
			Handler: stubPlannedFeature("unmark")},
		{Name: "tag", Description: "添加会话标签", Usage: "/tag <tag>", Category: "Core", Stub: true,
			Handler: stubPlannedFeature("tag")},
		{Name: "onboarding", Description: "重新运行引导", Usage: "/onboarding [skip]", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnOnboarding != nil {
					return r.OnOnboarding(args)
				}
				return r.handleOnboarding(), nil
			}},
		{Name: "tasks", Description: "显示后台任务列表", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnDaemon != nil {
					return r.OnDaemon("list"), nil
				}
				return daemonNotWiredNotice(), nil
			}},
	}
}
