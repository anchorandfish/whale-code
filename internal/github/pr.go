package github

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// PRCreator creates PR in one click
// - Detect current branch
// - AI generates commit message / PR title / PR description
// - git add + commit + push + pr create
type PRCreator struct {
	client   *Client
	aiClient AIClient
	workDir  string
}

// NewPRCreator creates a PR creator
func NewPRCreator(client *Client, aiClient AIClient, workDir string) *PRCreator {
	return &PRCreator{
		client:   client,
		aiClient: aiClient,
		workDir:  workDir,
	}
}

// CreatePR is the complete PR creation flow
// 1. Detect current branch
// 2. git add all changes
// 3. AI generates commit message
// 4. git commit
// 5. git push
// 6. AI generates PR title and description
// 7. Create PR
func (p *PRCreator) CreatePR(ctx context.Context, baseBranch string) (*PRInfo, error) {
	// Detect current branch
	branch, err := detectCurrentBranch(p.workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to detect current branch: %w", err)
	}

	// If on main/master branch, prompt user to create a new branch first
	if branch == "main" || branch == "master" {
		return nil, fmt.Errorf("currently on %s branch, please create a new branch before submitting PR", branch)
	}

	// Set default target branch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// H3/：收集改动文件列表 + .whaleignore 预扫描后 git add。
	changedFiles, err := p.gitChangedFiles()
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}
	if err := p.gitAdd(changedFiles); err != nil {
		return nil, fmt.Errorf("git add failed: %w", err)
	}

	// Get diff for AI generation
	diff, err := p.gitDiff()
	if err != nil {
		return nil, fmt.Errorf("failed to get diff: %w", err)
	}

	if strings.TrimSpace(diff) == "" {
		return nil, fmt.Errorf("no staged changes, cannot create PR")
	}

	// AI generates commit message
	commitMsg, err := generateCommitMessage(ctx, p.aiClient, diff)
	if err != nil {
		return nil, fmt.Errorf("failed to generate commit message: %w", err)
	}

	// commit message 写入前检测密钥泄露（D7-2，REF-OWASP-LLM02 + REF-）
	if leaked, hit := detectSecretLeakForCommit(commitMsg); hit {
		return nil, fmt.Errorf("⚠️ [SECRET_LEAK_BLOCKED] commit message 检测到密钥模式，已阻断提交：\n%s\n请重新生成 commit message", leaked)
	}

	// git commit
	if err := p.gitCommit(commitMsg); err != nil {
		return nil, fmt.Errorf("git commit failed: %w", err)
	}

	// git push
	if err := p.gitPush(branch); err != nil {
		return nil, fmt.Errorf("git push failed: %w", err)
	}

	// AI generates PR title
	title, err := generatePRTitle(ctx, p.aiClient, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to generate PR title: %w", err)
	}

	// AI generates PR description
	body, err := generatePRBody(ctx, p.aiClient, diff, title)
	if err != nil {
		return nil, fmt.Errorf("failed to generate PR description: %w", err)
	}

	// D7-2 安全增强：PR title / body 写入前检测密钥泄露（REF-OWASP-LLM02 + REF-）。
	// AI 生成的 title / body 可能复述 diff 中的密钥，提交到 GitHub 前必须阻断。
	// 检测点放在 generatePRTitle/generatePRBody 之后、CreatePR API 调用之前，fail fast。
	if leaked, hit := secret.DetectLeakWithLabels(title); hit {
		return nil, fmt.Errorf("⚠️ [SECRET_LEAK_BLOCKED] PR title 检测到密钥模式，已阻断创建：\n%s\n请重新生成 PR title", leaked)
	}
	if leaked, hit := secret.DetectLeakWithLabels(body); hit {
		return nil, fmt.Errorf("⚠️ [SECRET_LEAK_BLOCKED] PR body 检测到密钥模式，已阻断创建：\n%s\n请重新生成 PR body", leaked)
	}

	// Detect repository info
	owner, repo, err := p.client.DetectRepo(p.workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to detect repository info: %w", err)
	}

	// Create PR
	prReq := CreatePRRequest{
		Title: title,
		Body:  body,
		Head:  branch,
		Base:  baseBranch,
	}

	prInfo, err := p.client.CreatePR(ctx, owner, repo, prReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}

	return prInfo, nil
}

// detectCurrentBranch detects the current git branch
func detectCurrentBranch(workDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workDir
	cmd.Env = secret.SafeEnv("GIT_") // H3：子进程 env 白名单
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// generateCommitMessage uses AI to generate commit message
func generateCommitMessage(ctx context.Context, aiClient AIClient, diff string) (string, error) {
	req := AIRequest{
		SystemPrompt: []string{"You are a Git commit message generation expert. Please generate a concise and accurate commit message based on the code changes. Only output the commit message itself, no other explanations."},
		Messages: []AIMessage{
			{Role: "user", Content: fmt.Sprintf("Please generate a commit message based on the following diff:\n```diff\n%s\n```", diff)},
		},
		MaxTokens: 256,
	}

	result, err := callAISimple(ctx, aiClient, req)
	if err != nil {
		return "", err
	}

	// Clean up result: remove possible quote wrapping
	result = strings.Trim(result, "\"'`")
	// Take only the first line as commit message
	if idx := strings.Index(result, "\n"); idx >= 0 {
		result = result[:idx]
	}

	return strings.TrimSpace(result), nil
}

// generatePRTitle uses AI to generate PR title
func generatePRTitle(ctx context.Context, aiClient AIClient, commitMsg string) (string, error) {
	req := AIRequest{
		SystemPrompt: []string{"You are a PR title generation expert. Please generate a concise PR title based on the commit message. Only output the title itself, no other explanations."},
		Messages: []AIMessage{
			{Role: "user", Content: fmt.Sprintf("Please generate a PR title based on the following commit message:\n%s", commitMsg)},
		},
		MaxTokens: 128,
	}

	result, err := callAISimple(ctx, aiClient, req)
	if err != nil {
		return "", err
	}

	result = strings.Trim(result, "\"'`")
	if idx := strings.Index(result, "\n"); idx >= 0 {
		result = result[:idx]
	}

	return strings.TrimSpace(result), nil
}

// generatePRBody uses AI to generate PR description
func generatePRBody(ctx context.Context, aiClient AIClient, diff string, title string) (string, error) {
	req := AIRequest{
		SystemPrompt: []string{"You are a PR description generation expert. Please generate a detailed PR description based on code changes and title, using Markdown format."},
		Messages: []AIMessage{
			{Role: "user", Content: fmt.Sprintf("PR title: %s\n\nPlease generate a PR description based on the following diff:\n```diff\n%s\n```", title, diff)},
		},
		MaxTokens: 1024,
	}

	result, err := callAISimple(ctx, aiClient, req)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(result), nil
}

// callAISimple calls AI and collects complete text response
func callAISimple(ctx context.Context, aiClient AIClient, req AIRequest) (string, error) {
	events, err := aiClient.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI call failed: %w", err)
	}

	var builder strings.Builder
	for event := range events {
		switch e := event.(type) {
		case AITextDeltaEvent:
			builder.WriteString(e.Content)
		case AIErrorEvent:
			return "", fmt.Errorf("AI returned error: %w", e.Err)
		case AIMessageStopEvent:
			// Normal end
		}
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("AI returned no content")
	}

	return result, nil
}

// gitChangedFiles 收集工作区所有改动文件列表（含未跟踪文件），供 gitAdd 预扫描。
// 使用 git status --porcelain 解析，对标同类 CLI git_status 包装。
func (p *PRCreator) gitChangedFiles() ([]string, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = p.workDir
	cmd.Env = secret.SafeEnv("GIT_") // H3：子进程 env 白名单
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get git status: %w", err)
	}
	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		if len(line) < 3 {
			continue
		}
		// porcelain 格式：XY filename（X=staged, Y=worktree）
		file := strings.TrimSpace(line[2:])
		// 处理重命名格式 "old -> new"
		if idx := strings.Index(file, " -> "); idx >= 0 {
			file = file[idx+4:]
		}
		file = strings.Trim(file, "\"")
		if file != "" {
			files = append(files, file)
		}
	}
	return files, nil
}

// gitAdd 执行 git add 显式文件列表 + .whaleignore 预扫描。
// 敏感文件（.env/*.pem/id_*/.aws/credentials.json 等）被阻断并返回 ErrSecretLeak。
func (p *PRCreator) gitAdd(files []string) error {
	// 预扫描：过滤 .whaleignore 命中的敏感文件
	safe, blocked := secret.ScanWhaleIgnore(files)
	if len(blocked) > 0 {
		return secret.WrapLeak(fmt.Errorf("sensitive files detected: %v", blocked))
	}
	if len(safe) == 0 {
		return nil // 无文件需要 add
	}
	args := append([]string{"add"}, safe...)
	cmd := exec.Command("git", args...)
	cmd.Dir = p.workDir
	cmd.Env = secret.SafeEnv("GIT_", "GITHUB_TOKEN") // H3：子进程 env 白名单
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w\n%s", err, string(output))
	}
	return nil
}

// gitDiff gets the staged diff
func (p *PRCreator) gitDiff() (string, error) {
	cmd := exec.Command("git", "diff", "--cached")
	cmd.Dir = p.workDir
	cmd.Env = secret.SafeEnv("GIT_") // H3：子进程 env 白名单
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get diff: %w", err)
	}
	return string(output), nil
}

// gitCommit executes git commit
func (p *PRCreator) gitCommit(message string) error {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = p.workDir
	cmd.Env = secret.SafeEnv("GIT_", "GITHUB_TOKEN") // H3：子进程 env 白名单
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, string(output))
	}
	return nil
}

// gitPush executes git push
func (p *PRCreator) gitPush(branch string) error {
	cmd := exec.Command("git", "push", "-u", "origin", branch)
	cmd.Dir = p.workDir
	cmd.Env = secret.SafeEnv("GIT_", "GITHUB_TOKEN") // H3：子进程 env 白名单
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, string(output))
	}
	return nil
}

// getWorkDir gets the absolute path of the working directory
func getWorkDir(workDir string) string {
	if workDir == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(workDir))
}

// runGitCommand executes git command in working directory
func runGitCommand(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	cmd.Env = secret.SafeEnv("GIT_", "GITHUB_TOKEN") // H3：子进程 env 白名单
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// detectSecretLeakForCommit 检测 commit message 是否含密钥模式，命中时返回命中的规则标签列表。
// D7-2 重构：逻辑收口到 secret.DetectLeakWithLabels 共用函数（消除与 tools 包重复实现）。
// fail-open：内部 panic 时返回 ("", false) 不阻断 PR 创建主流程（REF-OWASP-LLM02 + REF-）。
// 供 PRCreator.CreatePR 在 git commit 前调用。
func detectSecretLeakForCommit(msg string) (labels string, hit bool) {
	return secret.DetectLeakWithLabels(msg)
}
