package command

// authCommands 返回 Auth 类别的内置命令
func (r *Registry) authCommands() []Command {
	return []Command{
		{Name: "login", Description: "登录 API 账号", Category: "Auth", Stub: true,
			Handler: func(args string) (string, error) {
				return "Enter API Key (or set environment variable):\n  export ANTHROPIC_API_KEY=xxx\n  export DASHSCOPE_API_KEY=xxx", nil
			}},
		{Name: "logout", Description: "退出登录 API 账号", Category: "Auth", Stub: true,
			Handler: func(args string) (string, error) {
				return "Logged out — API Key removed from session", nil
			}},
	}
}
