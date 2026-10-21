package command

// sessionCommands 返回 Session 类别的内置命令
// P0-4 修复：所有桩实现改为回调模式（对齐 OnImport），
// 回调为 nil 时回退到魔法字符串（供 TUI app.go / Bridge bridge.go 拦截），
// 回调非 nil 时直接返回真实结果（供 PlainRunner 等非 TUI 模式使用）。
func (r *Registry) sessionCommands() []Command {
	return []Command{
		{Name: "name", Description: "重命名会话", Usage: "/name <name>", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnRename != nil {
					return r.OnRename(args), nil
				}
				if args == "" {
					return "Usage: /name <name>", nil
				}
				return "Session renamed to: " + args, nil
			}},
		{Name: "session", Hidden: true, Description: "切换并载入已保存的会话（↑↓/Enter）", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionList != nil {
					return r.OnSessionList(), nil
				}
				return "__SESSION_PICK__", nil
			}},
		{Name: "session pick", Hidden: true, Description: "切换并载入已保存的会话（↑↓/Enter）", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionList != nil {
					return r.OnSessionList(), nil
				}
				return "__SESSION_PICK__", nil
			}},
		{Name: "session list", Description: "列出所有会话（含轮次/时间）", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionList != nil {
					return r.OnSessionList(), nil
				}
				return "__SESSION_LIST__", nil
			}},
		{Name: "session save", Description: "保存当前会话", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionSave != nil {
					return r.OnSessionSave(), nil
				}
				return "Session saved", nil
			}},
		{Name: "session switch", Description: "切换会话", Usage: "/session switch <id>", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionSwitch != nil {
					return r.OnSessionSwitch(args), nil
				}
				return "Switched to session: " + args, nil
			}},
		{Name: "resume", Description: "恢复上一个会话", Usage: "/resume [session_id]", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnResume != nil {
					return r.OnResume(args), nil
				}
				return "__RESUME__:" + args, nil
			}},
		{Name: "sessions", Description: "列出所有会话", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionList != nil {
					return r.OnSessionList(), nil
				}
				return "__SESSIONS__", nil
			}},
		{Name: "session merge", Hidden: true, Description: "合并会话", Usage: "/session merge <id1> <id2>", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionMerge != nil {
					return r.OnSessionMerge(args), nil
				}
				return "Session merged", nil
			}},
		{Name: "import", Hidden: true, Description: "从 JSON 文件导入会话", Usage: "/import <path>", Category: "Session",
			Handler: func(args string) (string, error) {
				// P0-14：优先调用 TUI 接线的真实导入逻辑，nil 时回退到 handleImport 桩
				if r.OnImport != nil {
					return r.OnImport(args)
				}
				return handleImport(args)
			}},
		{Name: "session-split", Hidden: true, Description: "在指定位置分割当前会话", Usage: "/session-split [message_index]", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnSessionSplit != nil {
					return r.OnSessionSplit(args), nil
				}
				return "__SESSION_SPLIT__:" + args, nil
			}},
		{Name: "rename", Description: "重命名会话（/name 别名）", Usage: "/rename <name>", Category: "Session",
			Handler: func(args string) (string, error) {
				if r.OnRename != nil {
					return r.OnRename(args), nil
				}
				if args == "" {
					return "Usage: /rename <name>", nil
				}
				return "Session renamed to: " + args, nil
			}},
	}
}
