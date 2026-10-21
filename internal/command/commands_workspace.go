package command

// workspaceCommands 返回 Workspace 类别的内置命令
func (r *Registry) workspaceCommands() []Command {
	return []Command{
		{Name: "add-dir", Description: "添加额外工作目录", Usage: "/add-dir <path>", Category: "Workspace",
			Handler: func(args string) (string, error) {
				if args == "" {
					return "Usage: /add-dir <path>", nil
				}
				return "Working directory added: " + args, nil
			}},
		{Name: "files", Hidden: true, Description: "列出项目文件概览", Usage: "/files [pattern]", Category: "Workspace",
			Handler: func(args string) (string, error) {
				if r.OnFiles != nil {
					return r.OnFiles(args), nil
				}
				return "Use `ls` or ask AI to list project files", nil
			}},
		{Name: "insights", Hidden: true, Description: "显示项目洞察", Category: "Workspace",
			Handler: func(args string) (string, error) {
				if r.OnInsights != nil {
					return r.OnInsights(), nil
				}
				return "Project insights not available. Ask AI to analyze the project.", nil
			}},
	}
}
