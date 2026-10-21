package command

// configCommands 返回 Config 类别的内置命令
func (r *Registry) configCommands() []Command {
	return []Command{
		{Name: "hooks", Hidden: true, Description: "管理 hook 配置（list/add/remove）", Usage: "/hooks [list|add <event> <type> <action> [options]|remove <event> <index>]", Category: "Config",
			Handler: func(args string) (string, error) {
				if r.OnHooks != nil {
					return r.OnHooks(args), nil
				}
				return "Hook system not initialized, create .whale/hooks.json in the working directory", nil
			}},
		{Name: "mcp", Description: "管理 MCP 服务器连接（list/add/remove/restart）", Usage: "/mcp [list|add <name> <cmd>|remove|restart <name>]", Category: "Config",
			Handler: func(args string) (string, error) {
				if r.OnMcp != nil {
					return r.OnMcp(args), nil
				}
				return "MCP system not initialized, create .whale/mcp.json in the working directory", nil
			}},
		{Name: "migrate", Hidden: true, Description: "从旧版本迁移配置", Usage: "/migrate [check|run]", Category: "Config",
			Handler: func(args string) (string, error) {
				if r.OnMigrate != nil {
					return r.OnMigrate(args), nil
				}
				return "Config migration: no config directory found, nothing to migrate", nil
			}},
		{Name: "env", Hidden: true, Description: "显示/设置环境变量", Usage: "/env [KEY[=VALUE]|unset KEY]", Category: "Config",
			Handler: func(args string) (string, error) {
				if r.OnEnv != nil {
					return r.OnEnv(args), nil
				}
				if args == "" {
					return "Environment variables:\n  (Use /env KEY=VALUE to set)", nil
				}
				return "Environment variable: " + args, nil
			}},
		{Name: "output-style", Hidden: true, Description: "切换输出样式", Usage: "/output-style [concise|verbose|technical|default]", Category: "Config",
			Handler: func(args string) (string, error) {
				if r.OnOutputStyle != nil {
					return WrapOutputStyleError(r.OnOutputStyle(args))
				}
				// 未接线 fallback 与正式 handler 共用同一实现，文案单源；
				// 无效风格同样经 WrapOutputStyleError 走 N6 ErrorID wrap（§3.9 形态不变）。
				return WrapOutputStyleError(NewOutputStyleCommandHandler(nil)(args))
			}},
		{Name: "terminal-setup", Description: "配置终端集成", Category: "Config",
			Handler: func(args string) (string, error) {
				if r.OnTerminalSetup != nil {
					return r.OnTerminalSetup(args), nil
				}
				return "Terminal integration setup:\n  Add the following to your shell config:\n  eval $(whale shell-hook)", nil
			}},
	}
}
