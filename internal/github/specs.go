package github

import (
	"fmt"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// toolSpecs 是 GitHub 工具面的**唯一**声明表（12 个工具）。
//
// 提为包级变量不只是"复用"：能力面装配（availability.go 的 ApplyToolAvailability）必须遍历
// **同一份**表 —— 否则"有哪些 github 工具"会有两份声明，新增工具时漏改一处就静默漂移
// （谓词默认可用 ⇒ 直接退回 「无凭据也进工具面」）。
var toolSpecs = []tools.Spec{
	{
		Name:        "github_create_pr",
		Description: "Create a GitHub Pull Request. Auto-detects current branch, AI generates title and description.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{},
			"properties": map[string]interface{}{
				"base_branch": map[string]interface{}{
					"type":        "string",
					"description": "Target branch (default main)",
				},
			},
		},
		RequiredPermission: tools.LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	},
	{
		Name:        "github_review_pr",
		Description: "Review a GitHub Pull Request using AI, generate review comments and publish as PR review.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"pr_number"},
			"properties": map[string]interface{}{
				"pr_number": map[string]interface{}{
					"type":        "integer",
					"description": "PR number",
				},
				"quick": map[string]interface{}{
					"type":        "boolean",
					"description": "Quick review mode (summary only), default false",
				},
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (auto-detected by default)",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name (auto-detected by default)",
				},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},
	{
		Name:        "github_list_prs",
		Description: "List Pull Requests of a GitHub repository.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{},
			"properties": map[string]interface{}{
				"state": map[string]interface{}{
					"type":        "string",
					"description": "PR state filter: open / closed / all (default open)",
					"enum":        []string{"open", "closed", "all"},
				},
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (auto-detected by default)",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name (auto-detected by default)",
				},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},
	{
		Name:        "github_get_pr_diff",
		Description: "Get the diff content of a GitHub Pull Request.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"pr_number"},
			"properties": map[string]interface{}{
				"pr_number": map[string]interface{}{
					"type":        "integer",
					"description": "PR number",
				},
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (auto-detected by default)",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name (auto-detected by default)",
				},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},
	{
		Name:        "github_merge_pr",
		Description: "Merge a GitHub Pull Request (using squash merge).",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"pr_number"},
			"properties": map[string]interface{}{
				"pr_number": map[string]interface{}{
					"type":        "integer",
					"description": "PR number",
				},
				"owner": map[string]interface{}{
					"type":        "string",
					"description": "Repository owner (auto-detected by default)",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository name (auto-detected by default)",
				},
			},
		},
		RequiredPermission: tools.LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	},

	// --- Issue tools ---

	{
		Name:        "github_list_issues",
		Description: "List Issues of a GitHub repository. Supports filtering by state.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo"},
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":  map[string]interface{}{"type": "string", "description": "Repository name"},
				"state": map[string]interface{}{"type": "string", "description": "Issue state (open/closed/all)", "default": "open"},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},
	{
		Name:        "github_create_issue",
		Description: "Create a GitHub Issue.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo", "title"},
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":  map[string]interface{}{"type": "string", "description": "Repository name"},
				"title": map[string]interface{}{"type": "string", "description": "Issue title"},
				"body":  map[string]interface{}{"type": "string", "description": "Issue body"},
			},
		},
		RequiredPermission: tools.LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	},
	{
		Name:        "github_get_issue",
		Description: "Get GitHub Issue details.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo", "issue_number"},
			"properties": map[string]interface{}{
				"owner":        map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":         map[string]interface{}{"type": "string", "description": "Repository name"},
				"issue_number": map[string]interface{}{"type": "integer", "description": "Issue number"},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},

	// --- Actions tools ---

	{
		Name:        "github_list_workflows",
		Description: "List GitHub Actions Workflows of a repository.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo"},
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":  map[string]interface{}{"type": "string", "description": "Repository name"},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},
	{
		Name:        "github_list_workflow_runs",
		Description: "List GitHub Actions Workflow runs, supports filtering by branch.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo"},
			"properties": map[string]interface{}{
				"owner":  map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":   map[string]interface{}{"type": "string", "description": "Repository name"},
				"branch": map[string]interface{}{"type": "string", "description": "Filter by branch"},
			},
		},
		RequiredPermission: tools.LevelReadOnly,
		ConcurrencySafe:    true,
	},
	{
		Name:        "github_trigger_workflow",
		Description: "Trigger a GitHub Actions Workflow run.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo", "workflow_id", "ref"},
			"properties": map[string]interface{}{
				"owner":       map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":        map[string]interface{}{"type": "string", "description": "Repository name"},
				"workflow_id": map[string]interface{}{"type": "integer", "description": "Workflow ID"},
				"ref":         map[string]interface{}{"type": "string", "description": "Git ref (branch/tag)"},
				"inputs":      map[string]interface{}{"type": "object", "description": "Workflow input parameters"},
			},
		},
		RequiredPermission: tools.LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	},
	{
		Name:        "github_rerun_workflow",
		Description: "Rerun a GitHub Actions Workflow.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"owner", "repo", "run_id"},
			"properties": map[string]interface{}{
				"owner":  map[string]interface{}{"type": "string", "description": "Repository owner"},
				"repo":   map[string]interface{}{"type": "string", "description": "Repository name"},
				"run_id": map[string]interface{}{"type": "integer", "description": "Workflow run ID"},
			},
		},
		RequiredPermission: tools.LevelWorkspaceWrite,
		ConcurrencySafe:    false,
	},
}

// RegisterSpecs registers GitHub tool specs to Registry
//
// ★ S3（裁定 C=②「工具面 = 能力面」）的 GitHub 面 —— 修 。
//
// 此前：无 GITHUB_TOKEN 时这 12 个工具**照样**注册进模型工具面（`NewGitHubTools` 读 token
// 后不校验、`RegisterSpecs`/`RegisterHandlers` 无条件执行），模型真去调才拿到
// `GITHUB_TOKEN environment variable not set` —— 即"对不存在的能力做出承诺"，
// 白占 12 份 schema 的 token，并把"缺配置"伪装成"工具可用"。同包的 MCP 发现路径
// 早就有这层前置短路（`len(config.Servers) == 0` ⇒ 直接返回），GitHub 路径一直没有。
//
// 为什么落在这一层：`AllDirect()` / `List()` / `Get()` / `DeferredToolDirectory()`
// 全都遍历注册表 ⇒ 不注册即自动不在任何"按注册表派生"的面里，无需逐面打补丁。
// 三面中的另两面各由**同一个谓词**负责：档位目录见 toollevel.go 的出口过滤（对任何已注册
// 名字生效，含"只增不减"的静态快照），执行门见 tools.go 的 `RegisterHandlers`。
func (g *GitHubTools) RegisterSpecs(registry *tools.Registry) error {
	for _, spec := range toolSpecs {
		if !tools.ToolAvailable(spec.Name) {
			continue
		}
		if err := registry.RegisterMCP(spec); err != nil {
			// If conflicts with built-in tools, try to register as plugin tool
			if err2 := registry.RegisterPlugin(spec); err2 != nil {
				return fmt.Errorf("failed to register GitHub tool %q: %w", spec.Name, err)
			}
		}
	}

	return nil
}
