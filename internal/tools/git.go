package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/anchorandfish/whale-code/internal/safebuf"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/util"
)

// GitHelper is a Git operation helper
type GitHelper struct {
	workDir string
}

// branchNameRegex validates branch names for create and checkout operations
var branchNameRegex = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// NewGitHelper creates a Git helper
func NewGitHelper(workDir string) *GitHelper {
	return &GitHelper{workDir: workDir}
}

// Run 执行 Git 命令，应用 S-2/S-3 安全加固（98 文档高优先级安全修复）。
//
// S-2 修复（子进程环境变量泄露）：通过 secret.SafeEnv("GIT_") 白名单过滤环境变量，
// 仅保留 PATH/HOME/USER/LANG/LC_ALL/TERM + GIT_* 前缀变量，
// 剥离 ANTHROPIC_API_KEY/OPENAI_API_KEY 等密钥变量，防止 AI 执行 env 窃密。
// 对标同类 CLI exec_policy env_clear + envs / 同类 CLI BashEnv 白名单。
//
// S-3 修复（Git Hook 钩子注入）：在所有 git 命令前注入 -c core.hooksPath=/dev/null，
// 禁用用户仓库 .git/hooks/ 下的所有钩子（pre-commit/post-commit/post-checkout 等），
// 防止攻击者诱导 git commit/checkout 触发恶意钩子绕过 whale 沙箱执行 RCE。
// 对标同类 CLI 默认 --no-verify / 同类 CLI exec_policy 标记 hooks 受限。
// 跨平台：/dev/null 在 Linux/macOS 为空设备，在 Windows 为不存在的路径，
// git 均视为"hooks 目录不存在"→ 跳过所有钩子，行为一致。
//
// 参数：
// - ctx: 上下文（用于超时/取消）
// - args: git 子命令参数（如 "status", "--porcelain=v1"）
//
// 返回：trim 后的 stdout（stderr 在 stdout 为空时回退），以及执行错误
func (g *GitHelper) Run(ctx context.Context, args ...string) (string, error) {
	// S-3：注入 core.hooksPath=/dev/null 禁用所有 git hooks（防御钩子注入 RCE）
	// 放在用户 args 之前，确保全局 -c 配置生效
	hookSafeArgs := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", hookSafeArgs...)
	cmd.Dir = g.workDir
	// S-2：环境变量白名单过滤，剥离密钥变量（API_KEY/TOKEN/SECRET 等）
	// 保留 GIT_* 前缀变量以支持 GIT_SSH/GIT_AUTHOR_NAME 等 git 工作所需配置
	cmd.Env = secret.SafeEnv("GIT_")

	var stdout, stderr safebuf.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 && output == "" {
		output = stderr.String()
	}

	return strings.TrimSpace(output), err
}

// IsGitRepo checks if it is a Git repository
func (g *GitHelper) IsGitRepo(ctx context.Context) bool {
	_, err := g.Run(ctx, "rev-parse", "--git-dir")
	return err == nil
}

// Status fetch Git state
func (g *GitHelper) Status(ctx context.Context) (string, error) {
	return g.Run(ctx, "status", "--porcelain=v1")
}

// Diff fetch Git diff
func (g *GitHelper) Diff(ctx context.Context, staged bool) (string, error) {
	if staged {
		return g.Run(ctx, "diff", "--cached")
	}
	return g.Run(ctx, "diff")
}

// Log fetch Git logging
func (g *GitHelper) Log(ctx context.Context, n int) (string, error) {
	return g.Run(ctx, "log", fmt.Sprintf("-%d", n), "--oneline")
}

// BranchList fetches branch list
func (g *GitHelper) BranchList(ctx context.Context) (string, error) {
	return g.Run(ctx, "branch", "-a")
}

// CurrentBranch fetches the current branch
func (g *GitHelper) CurrentBranch(ctx context.Context) (string, error) {
	return g.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

// Commit commit
func (g *GitHelper) Commit(ctx context.Context, message string) (string, error) {
	return g.Run(ctx, "commit", "-m", message)
}

// Add addfile
func (g *GitHelper) Add(ctx context.Context, files ...string) (string, error) {
	args := append([]string{"add"}, files...)
	return g.Run(ctx, args...)
}

// gitDiffHandler 是 git_diff 工具的处理器，返回工作区的 Git 差异
func (e *Executor) gitDiffHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		helper := NewGitHelper(workDir)
		if !helper.IsGitRepo(ctx) {
			return util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"), nil
		}

		var input struct {
			Staged bool `json:"staged"`
		}
		parseInput(raw, &input)

		diff, err := helper.Diff(ctx, input.Staged)
		if err != nil {
			return "", fmt.Errorf("fetch diff failed: %w", err)
		}
		if diff == "" {
			return "no changes", nil
		}
		if len(diff) > 30000 {
			// 收口：走唯一权威 TruncateTail（真实丢弃量标记）。
			diff = TruncateTail(diff, 30000)
		}
		return diff, nil
	}
}

// gitStatusHandler 是 git_status 工具的处理器，返回当前工作区的 Git 状态
func (e *Executor) gitStatusHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		helper := NewGitHelper(workDir)
		if !helper.IsGitRepo(ctx) {
			return util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"), nil
		}
		status, err := helper.Status(ctx)
		if err != nil {
			return "", fmt.Errorf("fetchstatefailed: %w", err)
		}
		if status == "" {
			return util.Bi("Working tree clean, nothing to commit", "工作区干净，没有变更"), nil
		}
		return status, nil
	}
}

// gitLogHandler 是 git_log 工具的处理器，返回 Git 提交日志
func (e *Executor) gitLogHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		helper := NewGitHelper(workDir)
		if !helper.IsGitRepo(ctx) {
			return util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"), nil
		}
		var input struct {
			N       int    `json:"n"`
			Oneline bool   `json:"oneline"`
			Path    string `json:"path"`
		}
		parseInput(raw, &input)
		if input.N <= 0 {
			input.N = 10
		}
		log, err := helper.Log(ctx, input.N)
		if err != nil {
			return "", fmt.Errorf("fetchloggingfailed: %w", err)
		}
		return log, nil
	}
}

// gitAddHandler 是 git_add 工具的处理器，将指定文件添加到暂存区
func (e *Executor) gitAddHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		helper := NewGitHelper(workDir)
		if !helper.IsGitRepo(ctx) {
			return util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"), nil
		}
		var input struct {
			Files []string `json:"files"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if len(input.Files) == 0 {
			return "", fmt.Errorf("file list cannot be empty")
		}
		for _, f := range input.Files {
			if strings.HasPrefix(f, "-") {
				return "", fmt.Errorf("invalid file path %q: file paths cannot start with '-'", f)
			}
			// Reject common git option separators
			if f == "--" {
				return "", fmt.Errorf("invalid file path %q: '--' is not allowed", f)
			}
		}
		result, err := helper.Add(ctx, input.Files...)
		if err != nil {
			return "", fmt.Errorf("addfilefailed: %w", err)
		}
		return util.BiSame("Added to staging area: %s (%s)", "已添加到暂存区：%s（%s）", strings.Join(input.Files, ", "), result), nil
	}
}

// gitCommitHandler 是 git_commit 工具的处理器，提交暂存区的变更
func (e *Executor) gitCommitHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		helper := NewGitHelper(workDir)
		if !helper.IsGitRepo(ctx) {
			return util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"), nil
		}
		var input struct {
			Message string `json:"message"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.Message == "" {
			return "", fmt.Errorf("commit message cannot be empty")
		}

		status, err := helper.Status(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to check repository status: %w", err)
		}
		hasStaged := false
		for _, line := range strings.Split(status, "\n") {
			if len(line) > 0 && (line[0] == 'A' || line[0] == 'M' || line[0] == 'D' || line[0] == 'R') {
				hasStaged = true
				break
			}
		}
		if !hasStaged {
			return "no staged changes to commit. Use git add to stage changes first.", nil
		}

		result, err := helper.Commit(ctx, input.Message)
		if err != nil {
			return "", fmt.Errorf("commitfailed: %w", err)
		}
		return util.BiSame("Commit succeeded: %s", "提交成功：%s", result), nil
	}
}

// gitBranchHandler 是 git_branch 工具的处理器，支持查询当前分支、列出分支、创建并切换分支、切换分支
func (e *Executor) gitBranchHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		helper := NewGitHelper(workDir)
		if !helper.IsGitRepo(ctx) {
			return util.Bi("Not a Git repository: the current directory is not inside a Git work tree", "当前目录不是 Git 仓库"), nil
		}
		var input struct {
			Action string `json:"action"`
			Name   string `json:"name"`
			All    bool   `json:"all"`
		}
		parseInput(raw, &input)

		switch input.Action {
		case "current", "":
			branch, err := helper.CurrentBranch(ctx)
			if err != nil {
				return "", fmt.Errorf("failed to get current branch: %w", err)
			}
			return util.BiSame("Current branch: %s", "当前分支：%s", branch), nil
		case "list":
			args := []string{"branch"}
			if input.All {
				args = append(args, "-a")
			}
			result, err := helper.Run(ctx, args...)
			if err != nil {
				return "", fmt.Errorf("failed to list branches: %w", err)
			}
			return result, nil
		case "create":
			if input.Name == "" {
				return "", fmt.Errorf("branch name cannot be empty")
			}
			if !branchNameRegex.MatchString(input.Name) {
				return "", fmt.Errorf("branch name contains invalid characters: %q (only letters, digits, dots, underscores, hyphens, and slashes are allowed)", input.Name)
			}
			result, err := helper.Run(ctx, "checkout", "-b", input.Name)
			if err != nil {
				return "", fmt.Errorf("createbranchfailed: %w", err)
			}
			// invalidate all cache after branch creation/switch (files may change)
			if e.FileCache != nil {
				e.FileCache.InvalidateAll()
			}
			return util.BiSame("Created and switched to branch: %s (%s)", "已创建并切换到分支：%s（%s）", input.Name, result), nil
		case "checkout":
			if input.Name == "" {
				return "", fmt.Errorf("branch name cannot be empty")
			}
			if !branchNameRegex.MatchString(input.Name) {
				return "", fmt.Errorf("branch name contains invalid characters: %q (only letters, digits, dots, underscores, hyphens, and slashes are allowed)", input.Name)
			}
			result, err := helper.Run(ctx, "checkout", input.Name)
			if err != nil {
				return "", fmt.Errorf("togglebranchfailed: %w", err)
			}
			// invalidate all cache after branch switch (files may change)
			if e.FileCache != nil {
				e.FileCache.InvalidateAll()
			}
			return util.BiSame("Switched to branch: %s (%s)", "已切换到分支：%s（%s）", input.Name, result), nil
		default:
			return "", fmt.Errorf("unknown action: %s, supported: list, create, checkout, current", input.Action)
		}
	}
}
