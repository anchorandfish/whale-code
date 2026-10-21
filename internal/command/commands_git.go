package command

import "github.com/anchorandfish/whale-code/internal/util"

// gitCommands 返回 Git 类别的内置命令
func (r *Registry) gitCommands() []Command {
	return []Command{
		{Name: "branch list", Hidden: true, Description: "显示分支", Category: "Git",
			Handler: func(args string) (string, error) {
				if r.OnBranchList != nil {
					return r.OnBranchList(), nil
				}
				return "Use `git branch` in terminal to list branches", nil
			}},
		{Name: "commit", Hidden: true, Description: "自动提交", Category: "Git",
			Handler: func(args string) (string, error) {
				if r.OnCommit != nil {
					return r.OnCommit(), nil
				}
				return "Use `git commit` in terminal, or ask AI to commit changes", nil
			}},
		{Name: "worktree list", Hidden: true, Description: "显示工作树", Category: "Git",
			Handler: func(args string) (string, error) {
				if r.OnWorktreeList != nil {
					return r.OnWorktreeList(), nil
				}
				return "Use `git worktree list` in terminal", nil
			}},
		{Name: "worktree add", Hidden: true, Description: "创建工作树", Usage: "/worktree add <path> [branch]", Category: "Git",
			Handler: func(args string) (string, error) {
				return util.Bi("⚠️ /worktree add is only available in interactive TUI/Bridge mode. Use it in the whale interactive UI, or run `git worktree add` manually.", "⚠️ /worktree add 仅在交互式 TUI/Bridge 模式下可用。请在 whale 交互界面中使用，或手动执行 `git worktree add`。"), nil
			}},
		{Name: "worktree remove", Hidden: true, Description: "移除工作树", Usage: "/worktree remove <path>", Category: "Git",
			Handler: func(args string) (string, error) {
				return util.Bi("⚠️ /worktree remove is only available in interactive TUI/Bridge mode. Use it in the whale interactive UI, or run `git worktree remove` manually.", "⚠️ /worktree remove 仅在交互式 TUI/Bridge 模式下可用。请在 whale 交互界面中使用，或手动执行 `git worktree remove`。"), nil
			}},
		{Name: "worktree prune", Hidden: true, Description: "清理过期工作树", Category: "Git",
			Handler: func(args string) (string, error) {
				if r.OnWorktreePrune != nil {
					return r.OnWorktreePrune(), nil
				}
				return "Use `git worktree prune` in terminal", nil
			}},
		{Name: "commit-push-pr", Hidden: true, Description: "一键提交+推送+创建 PR", Usage: "/commit-push-pr", Category: "Git",
			Handler: func(args string) (string, error) {
				return "COMMIT_PUSH_PR_REQUEST", nil
			}},
	}
}
