package github

import (
	"context"

	"fmt"

	"os"

	"strconv"

	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// GitHubTools is the GitHub tools manager
type GitHubTools struct {
	client        *Client
	reviewer      *Reviewer
	multiReviewer *MultiAgentReviewer
	creator       *PRCreator
	adapter       *AIAdapter
	workDir       string
}

// NewGitHubTools creates GitHub toolset
func NewGitHubTools(aiClient ai.Client, workDir string) *GitHubTools {
	// ★ 凭据的**唯一**读点：键名与能力面装配共用 GitTokenEnvVar（availability.go），
	// 且在此 TrimSpace。两件事都是刻意的：
	// - 共用一个常量 ⇒ 谓词说"有凭据"与客户端真拿到凭据必然是同一事实（同源）；
	// - 去空白 ⇒ 否则 ".env 里多打一个空格"会让 checkToken() 判成"有 token"，
	// 然后真的发出 `Authorization: Bearer ` ⇒ 401，错误形态从"没配"变成"鉴权失败"。
	token := strings.TrimSpace(os.Getenv(GitTokenEnvVar))
	client := NewClient(token)
	adapter := NewAIAdapter(aiClient)

	return &GitHubTools{
		client:        client,
		adapter:       adapter,
		reviewer:      NewReviewer(client, adapter),
		multiReviewer: NewMultiAgentReviewer(client, adapter),
		creator:       NewPRCreator(client, adapter, workDir),
		workDir:       workDir,
	}
}

// GetAIAdapter returns the AIAdapter for external recovery config injection
func (g *GitHubTools) GetAIAdapter() *AIAdapter {
	return g.adapter
}

// RegisterHandlers registers GitHub tool handlers to Executor
func (g *GitHubTools) RegisterHandlers(executor *tools.Executor) error {
	handlers := map[string]tools.Handler{
		"github_create_pr":          g.createPRHandler(),
		"github_review_pr":          g.reviewPRHandler(),
		"github_list_prs":           g.listPRsHandler(),
		"github_get_pr_diff":        g.getPRDiffHandler(),
		"github_merge_pr":           g.mergePRHandler(),
		"github_list_issues":        g.listIssuesHandler(),
		"github_create_issue":       g.createIssueHandler(),
		"github_get_issue":          g.getIssueHandler(),
		"github_list_workflows":     g.listWorkflowsHandler(),
		"github_list_workflow_runs": g.listWorkflowRunsHandler(),
		"github_trigger_workflow":   g.triggerWorkflowHandler(),
		"github_rerun_workflow":     g.rerunWorkflowHandler(),
	}

	for name, handler := range handlers {
		// ★ 执行门与注册点共用**同一谓词**（S3 · 裁定 C=② 的 GitHub 面）。
		//
		// 为什么不能只靠"注册点不建 spec"：生产管线会在 schema_validate 阶段按注册表拦
		// （fail-closed），它自动跟随注册点；但非管线路径（Pipeline == nil ⇒ executeDirect）
		// 只查 handler 表 ⇒ 不补这道闸门，同一个事实会在两条路上给出不同答案
		// （一条"不可用"、一条"缺少 token 的提示"）—— 与 executor.go 里 WebSearch 的处理同因。
		if !tools.ToolAvailable(name) {
			continue
		}
		if err := executor.RegisterHandler(name, handler); err != nil {
			return fmt.Errorf("failed to register GitHub tool handler %q: %w", name, err)
		}
	}

	return nil
}

// resolveOwnerRepo resolves owner and repo, prefers parameter values, auto-detects otherwise
func (g *GitHubTools) resolveOwnerRepo(owner, repo string) (string, string, error) {
	if owner != "" && repo != "" {
		return owner, repo, nil
	}
	return g.client.DetectRepo(g.workDir)
}

// checkToken checks if GitHub Token is set
func (g *GitHubTools) checkToken() error {
	if g.client.token == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable not set, please set GitHub Personal Access Token")
	}
	return nil
}

// HandleCommand handles user-side GitHub slash command dispatch
func (g *GitHubTools) HandleCommand(ctx context.Context, args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return g.usage()
	}

	switch {
	case strings.HasPrefix(args, "pr create"):
		return g.preCheckThen(g.handlePRCreateCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "pr create")))
	case strings.HasPrefix(args, "pr review"):
		if args == "pr review" || strings.TrimSpace(strings.TrimPrefix(args, "pr review")) == "" {
			return "用法: /pr review <PR编号> [--quick|--multi]"
		}
		return g.preCheckThen(g.handlePRReviewCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "pr review")))
	case strings.HasPrefix(args, "pr list"):
		return g.preCheckThen(g.handlePRListCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "pr list")))
	case strings.HasPrefix(args, "pr merge"):
		if args == "pr merge" || strings.TrimSpace(strings.TrimPrefix(args, "pr merge")) == "" {
			return "用法: /pr merge <PR编号>"
		}
		return g.preCheckThen(g.handlePRMergeCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "pr merge")))
	case args == "github status":
		if g.client == nil {
			return "GitHub 集成未初始化，请重启 Whale Code"
		}
		return g.handleStatus()
	case args == "github stop":
		if g.client == nil {
			return "GitHub 集成未初始化，请重启 Whale Code"
		}
		return "GitHub 事件监听未在运行（监听功能已移除）"
	case args == "github watch":
		if g.client == nil {
			return "GitHub 集成未初始化，请重启 Whale Code"
		}
		return "GitHub 事件监听功能已移除（个人开发机无公网入口）"
	case strings.HasPrefix(args, "issue create"):
		if args == "issue create" || strings.TrimSpace(strings.TrimPrefix(args, "issue create")) == "" {
			return "用法: /issue create <标题> [正文]"
		}
		return g.preCheckThen(g.handleIssueCreateCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "issue create")))
	case strings.HasPrefix(args, "issue list"):
		return g.preCheckThen(g.handleIssueListCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "issue list")))
	case strings.HasPrefix(args, "issue view"):
		subArgs := strings.TrimSpace(strings.TrimPrefix(args, "issue view"))
		if subArgs == "" {
			return "用法: /issue view <编号>"
		}
		if _, err := strconv.Atoi(subArgs); err != nil {
			return fmt.Sprintf("无效的 Issue 编号: %s", subArgs)
		}
		return g.preCheckThen(g.handleIssueViewCmd, ctx, subArgs)
	case strings.HasPrefix(args, "actions list"):
		return g.preCheckThen(g.handleActionsListCmd, ctx, strings.TrimSpace(strings.TrimPrefix(args, "actions list")))
	case strings.HasPrefix(args, "actions view"):
		subArgs := strings.TrimSpace(strings.TrimPrefix(args, "actions view"))
		if subArgs == "" {
			return "用法: /actions view <运行ID>"
		}
		return g.preCheckThen(g.handleActionsViewCmd, ctx, subArgs)
	case strings.HasPrefix(args, "actions trigger"):
		subArgs := strings.TrimSpace(strings.TrimPrefix(args, "actions trigger"))
		parts := strings.Fields(subArgs)
		if len(parts) < 2 {
			return "用法: /actions trigger <工作流ID> <引用>"
		}
		return g.preCheckThen(g.handleActionsTriggerCmd, ctx, subArgs)
	case strings.HasPrefix(args, "actions rerun"):
		subArgs := strings.TrimSpace(strings.TrimPrefix(args, "actions rerun"))
		if subArgs == "" {
			return "用法: /actions rerun <运行ID>"
		}
		return g.preCheckThen(g.handleActionsRerunCmd, ctx, subArgs)
	case strings.HasPrefix(args, "actions logs"):
		subArgs := strings.TrimSpace(strings.TrimPrefix(args, "actions logs"))
		if subArgs == "" {
			return "用法: /actions logs <运行ID>"
		}
		return g.preCheckThen(g.handleActionsLogsCmd, ctx, subArgs)
	default:
		return fmt.Sprintf("未知的 GitHub 子命令: %s\n%s", args, g.usage())
	}
}

// preCheckThen checks if client is initialized first, then executes handler
// preCheckThen 先检查 client 是否初始化，再执行 handler
func (g *GitHubTools) preCheckThen(handler func(context.Context, string) string, ctx context.Context, args string) string {
	if g.client == nil {
		return "GitHub 集成未初始化，请重启 Whale Code"
	}
	return handler(ctx, args)
}

// usage returns GitHub command usage hints
// usage 返回 GitHub 命令用法提示
func (g *GitHubTools) usage() string {
	return "用法:\n" +
		"  /pr create [base_branch]  — 创建 Pull Request\n" +
		"  /pr review <number>       — AI 审查 Pull Request（支持 --quick / --multi）\n" +
		"  /pr list [state]          — 列出 Pull Request\n" +
		"  /pr merge <number>        — 合并 Pull Request\n" +
		"  /github watch             — 监听仓库事件\n" +
		"  /github stop              — 停止事件监听\n" +
		"  /github status            — 显示连接状态\n" +
		"  /issue create <title>     — 创建 Issue\n" +
		"  /issue list [state]       — 列出 Issue\n" +
		"  /issue view <number>      — 查看 Issue 详情\n" +
		"  /actions list [branch]    — 列出 Workflow 运行\n" +
		"  /actions view <run_id>    — 查看运行详情\n" +
		"  /actions trigger <wf_id> <ref> — 触发 Workflow\n" +
		"  /actions rerun <run_id>   — 重新运行 Workflow\n" +
		"  /actions logs <run_id>    — 查看运行日志"
}

// Close releases resources held by GitHubTools.
// Note: webhook/watcher background listening was removed (personal users have no public ingress);
// this method is retained as a no-op for caller compatibility.
func (g *GitHubTools) Close() {
	// webhook/watcher 已移除（个人开发机无公网入口），无后台资源需释放
}
