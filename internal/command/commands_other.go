package command

// otherCommands 返回 Other 类别的内置命令
func (r *Registry) otherCommands() []Command {
	return []Command{
		// ★ 2026-10-12 14:50 恢复可见（用户拍板）：/help 里该能力不可发现 ⇒ 单点翻回。
		// 仍是 10-10 判据册之外的承诺外增量，
		// 登记： 回执 §2（恢复原因 + 用户拍板时间），勿当「漏标」重新隐藏。
		{Name: "memory", Description: "记忆管理：list/edit/show/add/delete", Usage: "/memory [list|edit|show|add|delete <file>]", Category: "Memory",
			Handler: func(args string) (string, error) {
				if r.OnMemory != nil {
					return r.OnMemory(args)
				}
				return "Memory: feature not connected", nil
			}},
		{Name: "config", Description: "显示配置", Usage: "/config [env]", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnConfig != nil {
					return r.OnConfig(args), nil
				}
				return "Use /env to view environment variables, /output-style for output settings", nil
			}},
		// 2026-10-14 Usage 审计：TUI 本地实现（turn_diff.go parseDiffArgs）接受 --stat 与
		// --file <name>（或裸过滤词），Usage 一直没写 ⇒ 既有能力不可发现。
		// Usage 声明的是命令面能力而非注册侧回调能力（/monitors 判例同一口径）。
		{Name: "diff", Description: "显示 Git 更改", Usage: "/diff [--stat] [--file <name>]", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnDiff != nil {
					return r.OnDiff(), nil
				}
				return "Use `git diff` in terminal, or ask AI to show changes", nil
			}},
		{Name: "agents", Hidden: true, Description: "显示 AI 助手", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnAgents != nil {
					return r.OnAgents(), nil
				}
				return "Available agents: Coder, Plan, Explore, Verification", nil
			}},
		{Name: "skills", Description: "显示技能", Usage: "/skills [name]", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnSkills != nil {
					return r.OnSkills(), nil
				}
				return "No skills available. Add skill files to .whale/skills/ directory", nil
			}},
		{Name: "rewind", Description: "回退对话到之前的轮次", Usage: "/rewind [#N | <id>] [--preview]  (no arg: picker)", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnRewind != nil {
					return r.OnRewind(args), nil
				}
				return "Rewind not initialized", nil
			}},
		{Name: "undo", Description: "撤销文件更改", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnUndo != nil {
					return r.OnUndo(), nil
				}
				return "Undo not initialized", nil
			}},
		{Name: "redo", Description: "重做文件更改", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnRedo != nil {
					return r.OnRedo(), nil
				}
				return "Redo not initialized", nil
			}},
		{Name: "init", Description: "初始化项目配置", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnInit != nil {
					return r.OnInit(args)
				}
				return "Creates .whale/ directory with default config. Ask AI to initialize the project.", nil
			}},
		// 2026-10-14 Usage 审计：TUI 本地实现（commands_plugin.go handleTheme）接受 mono，
		// Usage 只写了 light|dark ⇒ mono 主题不可发现。
		{Name: "theme", Description: "切换主题", Usage: "/theme light|dark|mono", Category: "Other",
			Handler: func(args string) (string, error) { return "Theme: " + args, nil }},
		{Name: "cron add", Hidden: true, Description: "添加 cron 任务", Usage: "/cron add \"0 9 * * 1-5\" \"prompt\"", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnCronAdd != nil {
					return r.OnCronAdd(args), nil
				}
				return "Cron jobs not initialized. Start the daemon first with /daemon start", nil
			}},
		{Name: "cron list", Hidden: true, Description: "列出 cron 任务", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnCronList != nil {
					return r.OnCronList(), nil
				}
				return "Cron jobs not initialized. Start the daemon first with /daemon start", nil
			}},
		{Name: "cron remove", Hidden: true, Description: "移除 cron 任务", Usage: "/cron remove <id>", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnCronRemove != nil {
					return r.OnCronRemove(args), nil
				}
				return "Cron jobs not initialized. Start the daemon first with /daemon start", nil
			}},
		{Name: "search", Hidden: true, Description: "跨会话搜索", Usage: "/search [--regex|--case-sensitive|--session <id>] <keyword>", Category: "Core",
			Handler: func(args string) (string, error) {
				if r.OnSearch != nil {
					return r.OnSearch(args), nil
				}
				return "Search feature not initialized", nil
			}},
		{Name: "fcache", Hidden: true, Description: "文件缓存管理", Usage: "/fcache [detail|clear|top]", Category: "Other",
			Handler: func(args string) (string, error) {
				if r.OnFileCache != nil {
					return r.OnFileCache(args), nil
				}
				return "File cache not initialized", nil
			}},
		// /tool 命令查看最近一条工具结果完整输出（S1 工具结果折叠配套，对标同类 CLI Ctrl+T overlay）。
		// Handler 返回空字符串，真实输出由 tui.handleCommand 的 switch name case "tool" 接管。
		// REF-外部: 同类 CLI pager_overlay.rs TranscriptOverlay
		// 2026-10-14 Usage 审计：TUI 本地实现（format.go handleToolCommand）支持 /tool <N>
		// 回看第 N 条工具结果，且缺省 = 最近一条；Usage 一直没写。
		{Name: "tool", Description: "查看最近一条工具结果的完整输出", Usage: "/tool [N]", Category: "Other",
			Handler: func(args string) (string, error) { return "", nil }},
	}
}
