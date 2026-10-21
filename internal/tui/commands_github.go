package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/github"
)

// handleBranchList show git branches
func (m *Model) handleBranchList() string {
	cmd := exec.Command("git", "branch", "-a", "--color=never")
	cmd.Dir = m.sessionService.WorkDir()
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v\nNot a git repository?", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var current string
	var others []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "* ") {
			current = strings.TrimPrefix(line, "* ")
		} else {
			name := strings.TrimPrefix(line, "remotes/origin/")
			others = append(others, name)
		}
	}
	var sb strings.Builder
	sb.WriteString("Git Branches:\n")
	if current != "" {
		sb.WriteString(fmt.Sprintf(" * %s (current)\n", current))
	}
	for _, b := range others {
		sb.WriteString(fmt.Sprintf(" %s\n", b))
	}
	return sb.String()
}

// handleCommit auto commit changes
func (m *Model) handleCommit() string {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = m.sessionService.WorkDir()
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v", err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return "Nothing to commit"
	}

	warning := fmt.Sprintf("⚠️ This will commit ALL changes (git add -A), including your own edits.\n%d file(s) changed.\nProceeding...", len(strings.Split(strings.TrimSpace(string(output)), "\n")))
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = m.sessionService.WorkDir()
	if _, err := addCmd.Output(); err != nil {
		return fmt.Sprintf("git add failed: %v", err)
	}
	diffCmd := exec.Command("git", "diff", "--cached", "--stat")
	diffCmd.Dir = m.sessionService.WorkDir()
	statOutput, _ := diffCmd.Output()
	commitMsg := fmt.Sprintf("Whale Code auto-commit\n\n%s", string(statOutput))
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = m.sessionService.WorkDir()
	result, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Commit failed: %v\n%s", err, string(result))
	}
	lines := strings.Split(string(result), "\n")
	summary := ""
	if len(lines) > 0 {
		summary = lines[0]
	}
	return warning + "\n" + fmt.Sprintf("Committed: %s", summary)
}

// handleWorktreeList show worktrees
func (m *Model) handleWorktreeList() string {
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = m.sessionService.WorkDir()
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Git error: %v", err)
	}
	return fmt.Sprintf("Worktrees:\n%s", string(output))
}

// handleWorktreeAdd creates a worktree
func (m *Model) handleWorktreeAdd(args string) string {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return "Usage: /worktree add <path> [branch]"
	}

	path := parts[0]
	branch := ""
	if len(parts) > 1 {
		branch = parts[1]
	}

	cmdArgs := []string{"worktree", "add", path}
	if branch != "" {
		cmdArgs = append(cmdArgs, "-b", branch)
	}
	cmdArgs = append(cmdArgs, "HEAD")

	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = m.sessionService.WorkDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Failed to create worktree: %v\n%s", err, string(output))
	}
	return fmt.Sprintf("Worktree created:\n%s", string(output))
}

// handleWorktreeRemove removes a worktree
func (m *Model) handleWorktreeRemove(args string) string {
	path := strings.TrimSpace(args)
	if path == "" {
		return "Usage: /worktree remove <path>"
	}

	cmd := exec.Command("git", "worktree", "remove", path)
	cmd.Dir = m.sessionService.WorkDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Failed to remove worktree: %v\n%s", err, string(output))
	}
	return fmt.Sprintf("Worktree removed: %s", path)
}

// handleWorktreePrune prunes stale worktrees
func (m *Model) handleWorktreePrune() string {
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Dir = m.sessionService.WorkDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Failed to prune worktree: %v\n%s", err, string(output))
	}
	result := "Cleaned up stale worktree references"
	if len(strings.TrimSpace(string(output))) > 0 {
		result += fmt.Sprintf("\n%s", string(output))
	}
	return result
}

// handleDiff 展示文件变更（：**薄转发** —— 唯一实现见 turn_diff.go 的 handleDiffFor）。
//
// 旧形态是本文件与 runner_core.go 各写一遍 ~90 行（连 `--stat` 间距都不同），
// 且两侧都把 `beforeContent` 写死成空串 ⇒ 从来没有显示过真实 diff。收口理由见 handleDiffFor。
func (m *Model) handleDiff(args string) string {
	return handleDiffFor(m.toolService.Executor(), m.sessionService.WorkDir(), args)
}

// handleCommitPushPR one-click commit + push + create PR
func (m *Model) handleCommitPushPR() string {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return "❌ GITHUB_TOKEN environment variable not set\nPlease run: set GITHUB_TOKEN=ghp_your_token_here"
	}

	rgCmd := exec.Command("git", "rev-parse", "--git-dir")
	rgCmd.Dir = m.sessionService.WorkDir()
	if err := rgCmd.Run(); err != nil {
		return "❌ current directory is not a Git repository"
	}

	if m.aiService.Client() == nil {
		return "❌ AI client not initialized, cannot generate commit message"
	}

	ghClient := github.NewClient(token)
	adapter := github.NewAIAdapter(m.aiService.Client())
	adapter.SetRecoveryConfig(m.aiService.RecoveryCoordinator(), m.aiService.RetryConfig(), m.aiService.Fallback())
	creator := github.NewPRCreator(ghClient, adapter, m.sessionService.WorkDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prInfo, err := creator.CreatePR(ctx, "")
	if err != nil {
		return fmt.Sprintf("❌ Create PR failed: %v", err)
	}

	return fmt.Sprintf("✅ One-click operation complete!\n Committed and pushed\n PR #%d created: %s\n %s", prInfo.Number, prInfo.Title, prInfo.HTMLURL)
}
