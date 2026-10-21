package command

// reviewCommands 返回 Review 类别的内置命令。
// /review 为本地多 Agent 代码审查命令，只读、非破坏性，结果仅输出到 TUI 会话记录，不发布到 GitHub。
func (r *Registry) reviewCommands() []Command {
	return []Command{
		{Name: "review", Description: "本地多 Agent 代码审查（只读，不发布到 GitHub）", Usage: "/review [branch|uncommitted|commit <hash>|custom]", Category: "Review",
			Handler: func(args string) (string, error) {
				if r.OnReview != nil {
					return r.OnReview(args), nil
				}
				return "Local review not initialized", nil
			}},
	}
}
