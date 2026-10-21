package command

import "github.com/anchorandfish/whale-code/internal/util"

// githubCommands 返回 GitHub 类别的内置命令
func (r *Registry) githubCommands() []Command {
	return []Command{
		{Name: "pr create", Hidden: true, Description: "一键创建 Pull Request", Usage: "/pr create", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("pr create " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "pr review", Hidden: true, Description: "AI 审查 Pull Request", Usage: "/pr review [PR_number]", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("pr review " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "pr list", Hidden: true, Description: "列出 Pull Request", Usage: "/pr list [state]", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("pr list " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "pr merge", Hidden: true, Description: "合并 Pull Request", Usage: "/pr merge <PR_number>", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("pr merge " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "github watch", Hidden: true, Description: "监听 GitHub 仓库事件", Usage: "/github watch", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("github watch"), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "github stop", Hidden: true, Description: "停止监听 GitHub 事件", Usage: "/github stop", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("github stop"), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "github status", Hidden: true, Description: "显示 GitHub 连接状态", Usage: "/github status", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("github status"), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "actions list", Hidden: true, Description: "列出 GitHub Actions 工作流运行", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("actions list " + args), nil
				}
				return "Use /actions list to view recent workflow runs", nil
			}},
		{Name: "actions view", Hidden: true, Description: "查看工作流运行详情", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("actions view " + args), nil
				}
				return "Use /actions view <run_id> to view run details", nil
			}},
		{Name: "actions trigger", Hidden: true, Description: "触发工作流运行", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("actions trigger " + args), nil
				}
				return "Use /actions trigger <workflow_id> <ref> to trigger workflow", nil
			}},
		{Name: "actions rerun", Hidden: true, Description: "重新运行工作流", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("actions rerun " + args), nil
				}
				return "Use /actions rerun <run_id> to rerun workflow", nil
			}},
		{Name: "actions logs", Hidden: true, Description: "查看工作流运行日志", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("actions logs " + args), nil
				}
				return "Use /actions logs <run_id> to view run logs", nil
			}},
		{Name: "issue create", Hidden: true, Description: "创建 GitHub Issue", Usage: "/issue create <title>", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("issue create " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "issue list", Hidden: true, Description: "列出 GitHub Issue", Usage: "/issue list [state]", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("issue list " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
		{Name: "issue view", Hidden: true, Description: "查看 GitHub Issue", Usage: "/issue view <number>", Category: "GitHub",
			Handler: func(args string) (string, error) {
				if r.OnGitHub != nil {
					return r.OnGitHub("issue view " + args), nil
				}
				return util.Bi("GitHub integration is not initialized", "GitHub 集成未初始化"), nil
			}},
	}
}
