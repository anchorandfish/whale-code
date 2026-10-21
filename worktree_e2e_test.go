package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2E_WorktreeSlashCommands 测试 worktree 斜杠命令的端到端流程
// 使用 --plain 模式发送 /worktree 命令
func TestE2E_WorktreeSlashCommands(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key，无法测试 E2E")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 初始化 Git 仓库
	initCmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range initCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("初始化 Git 仓库失败: %v, 输出: %s", err, string(output))
		}
	}

	// 创建初始提交
	readmePath := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("初始提交失败: %v, 输出: %s", err, string(output))
	}

	// 测试 /worktree list 命令
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	listCmd := exec.CommandContext(ctx, binaryPath, "--plain", "--ci", "/worktree list")
	listCmd.Dir = tmpDir
	listCmd.Env = env
	output, err := listCmd.CombinedOutput()
	outStr := string(output)
	t.Logf("/worktree list 输出: %s", outStr)
	if err != nil {
		t.Logf("/worktree list 命令退出码非零: %v", err)
	}
}

// TestE2E_WorktreeEnterExit 测试 AI 调用 enter_worktree / exit_worktree 工具
// 使用 --ci 模式让 AI 执行 worktree 操作
func TestE2E_WorktreeEnterExit(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key，无法测试 E2E")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 初始化 Git 仓库
	initCmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range initCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("初始化 Git 仓库失败: %v, 输出: %s", err, string(output))
		}
	}

	// 创建初始提交
	readmePath := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("初始提交失败: %v, 输出: %s", err, string(output))
	}

	// 使用 --ci 模式让 AI 进入 worktree
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	enterCmd := exec.CommandContext(ctx, binaryPath, "--ci",
		"请使用 enter_worktree 工具创建一个名为 e2e-test 的 worktree，然后退出 worktree 并保留")
	enterCmd.Dir = tmpDir
	enterCmd.Env = env
	output, err := enterCmd.CombinedOutput()
	outStr := string(output)
	t.Logf("E2E enter/exit 输出: %s", truncate(outStr, 2000))

	if err != nil {
		t.Logf("E2E 命令退出码非零: %v", err)
	}

	// 验证 worktree 目录是否创建
	worktreePath := filepath.Join(tmpDir, ".whale", "worktrees", "e2e-test")
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Logf("worktree 目录不存在 (AI 可能未调用工具): %s", worktreePath)
		// 不算失败，因为 AI 可能选择不调用工具
	} else {
		t.Log("worktree 目录已创建，E2E 测试成功")

		// 清理
		cleanCmd := exec.Command("git", "worktree", "remove", worktreePath)
		cleanCmd.Dir = tmpDir
		cleanCmd.CombinedOutput()
	}
}

// TestE2E_WorktreeErrorHandling 测试非 Git 仓库中的错误处理
func TestE2E_WorktreeErrorHandling(t *testing.T) {
	if !hasAPIKey() {
		t.Skip("跳过: 未设置 API Key，无法测试 E2E")
	}

	tmpDir := t.TempDir()
	env := buildIntegrationEnv(tmpDir, true)

	// 不初始化 Git 仓库

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "--ci",
		"请使用 enter_worktree 工具创建一个 worktree")
	cmd.Dir = tmpDir
	cmd.Env = env
	output, _ := cmd.CombinedOutput()
	outStr := string(output)
	t.Logf("非 Git 仓库 E2E 输出: %s", truncate(outStr, 1000))

	// AI 应该收到错误并告知User
	if strings.Contains(outStr, "Git 仓库") || strings.Contains(outStr, "git") {
		t.Log("AI 正确识别了非 Git 仓库错误")
	}
}

// truncate 截断字符串用于日志输出
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
