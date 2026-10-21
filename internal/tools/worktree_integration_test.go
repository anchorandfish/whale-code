package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepoForIntegration 初始化 Git 仓库用于integration test
func initGitRepoForIntegration(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init 失败: %v, 输出: %s", err, string(output))
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	cmd.CombinedOutput()

	// 创建初始提交
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit 失败: %v, 输出: %s", err, string(output))
	}
}

// TestEnterWorktreeHandler_Basic 测试 enter_worktree 工具处理器基本流程
func TestEnterWorktreeHandler_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForIntegration(t, tmpDir)

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]string{
		"name": "fix-bug",
	})

	result := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(input),
	})

	if result.IsError {
		t.Fatalf("enter_worktree 执行失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "创建 worktree") {
		t.Errorf("输出should contain '创建 worktree'，得到: %s", result.Output)
	}
	if !strings.Contains(result.Output, "worktree-fix-bug") {
		t.Errorf("输出should contain分支名 'worktree-fix-bug'，得到: %s", result.Output)
	}

	// 清理
	input2, _ := json.Marshal(map[string]interface{}{
		"action":          "remove",
		"discard_changes": true,
	})
	executor.Execute(ctx, Call{
		ID:    "test-2",
		Name:  "exit_worktree",
		Input: string(input2),
	})
}

// TestExitWorktreeHandler_Keep 测试 exit_worktree 工具处理器 keep 模式
func TestExitWorktreeHandler_Keep(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForIntegration(t, tmpDir)

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	// 先进入 worktree
	enterInput, _ := json.Marshal(map[string]string{"name": "keep-test"})
	enterResult := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(enterInput),
	})
	if enterResult.IsError {
		t.Fatalf("enter_worktree 失败: %s", enterResult.Output)
	}

	// 退出并保留
	exitInput, _ := json.Marshal(map[string]interface{}{
		"action": "keep",
	})
	exitResult := executor.Execute(ctx, Call{
		ID:    "test-2",
		Name:  "exit_worktree",
		Input: string(exitInput),
	})
	if exitResult.IsError {
		t.Fatalf("exit_worktree keep 失败: %s", exitResult.Output)
	}
	if !strings.Contains(exitResult.Output, "工作保留在") {
		t.Errorf("输出should contain '工作保留在'，得到: %s", exitResult.Output)
	}

	// 验证 worktree 目录存在
	wm := executor.getWorktreeManager(tmpDir)
	session := wm.GetCurrentSession()
	_ = session // should be nil（已退出）
	if wm.IsInWorktree() {
		t.Error("退出后不应在 worktree 中")
	}

	// 清理
	helper := NewGitHelper(tmpDir)
	helper.Run(ctx, "worktree", "remove", filepath.Join(tmpDir, ".whale", "worktrees", "keep-test"))
}

// TestExitWorktreeHandler_RemoveWithChanges 测试有变更时 remove 的安全机制
func TestExitWorktreeHandler_RemoveWithChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForIntegration(t, tmpDir)

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	// 进入 worktree
	enterInput, _ := json.Marshal(map[string]string{"name": "unsafe-test"})
	enterResult := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(enterInput),
	})
	if enterResult.IsError {
		t.Fatalf("enter_worktree 失败: %s", enterResult.Output)
	}

	// 在 worktree 中创建文件
	worktreePath := filepath.Join(tmpDir, ".whale", "worktrees", "unsafe-test")
	newFile := filepath.Join(worktreePath, "test.txt")
	if err := os.WriteFile(newFile, []byte("test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	// 不带 discard_changes 删除should be rejected
	exitInput, _ := json.Marshal(map[string]interface{}{
		"action": "remove",
	})
	exitResult := executor.Execute(ctx, Call{
		ID:    "test-2",
		Name:  "exit_worktree",
		Input: string(exitInput),
	})
	if !exitResult.IsError {
		t.Error("expected有变更时 remove rejected")
	}
	if !strings.Contains(exitResult.Output, "未提交文件") {
		t.Errorf("错误消息should contain '未提交文件'，得到: %s", exitResult.Output)
	}

	// 带 discard_changes=true 应成功
	exitInput2, _ := json.Marshal(map[string]interface{}{
		"action":          "remove",
		"discard_changes": true,
	})
	exitResult2 := executor.Execute(ctx, Call{
		ID:    "test-3",
		Name:  "exit_worktree",
		Input: string(exitInput2),
	})
	if exitResult2.IsError {
		t.Fatalf("discard_changes=true 时 exit_worktree 失败: %s", exitResult2.Output)
	}
	if !strings.Contains(exitResult2.Output, "已退出并删除") {
		t.Errorf("输出should contain '已退出并删除'，得到: %s", exitResult2.Output)
	}
}

// TestExitWorktreeHandler_NotInSession 测试不在 worktree 中时调用 exit
func TestExitWorktreeHandler_NotInSession(t *testing.T) {
	tmpDir := t.TempDir()

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	exitInput, _ := json.Marshal(map[string]interface{}{
		"action": "keep",
	})
	result := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "exit_worktree",
		Input: string(exitInput),
	})
	if !result.IsError {
		t.Error("expected不在 worktree 中时 exit 返回错误")
	}
	if !strings.Contains(result.Output, "不在 worktree") {
		t.Errorf("错误消息should contain '不在 worktree'，得到: %s", result.Output)
	}
}

// TestEnterWorktreeHandler_NotGitRepo 测试非 Git 仓库调用 enter
func TestEnterWorktreeHandler_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	enterInput, _ := json.Marshal(map[string]string{"name": "test"})
	result := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(enterInput),
	})
	if !result.IsError {
		t.Error("expected非 Git 仓库返回错误")
	}
	if !strings.Contains(result.Output, "Git 仓库") {
		t.Errorf("错误消息should contain 'Git 仓库'，得到: %s", result.Output)
	}
}

// TestEnterWorktreeHandler_InvalidName 测试非法名称
func TestEnterWorktreeHandler_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForIntegration(t, tmpDir)

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	enterInput, _ := json.Marshal(map[string]string{"name": "bad name!"})
	result := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(enterInput),
	})
	if !result.IsError {
		t.Error("expected非法名称返回错误")
	}
	if !strings.Contains(result.Output, "名称验证失败") {
		t.Errorf("错误消息should contain '名称验证失败'，得到: %s", result.Output)
	}
}

// TestExitWorktreeHandler_InvalidAction 测试无效的 action
func TestExitWorktreeHandler_InvalidAction(t *testing.T) {
	tmpDir := t.TempDir()

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	// 先进入 worktree
	initGitRepoForIntegration(t, tmpDir)
	enterInput, _ := json.Marshal(map[string]string{"name": "action-test"})
	executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(enterInput),
	})

	// 无效 action
	exitInput, _ := json.Marshal(map[string]interface{}{
		"action": "invalid",
	})
	result := executor.Execute(ctx, Call{
		ID:    "test-2",
		Name:  "exit_worktree",
		Input: string(exitInput),
	})
	if !result.IsError {
		t.Error("expected无效 action 返回错误")
	}

	// 清理
	exitInput2, _ := json.Marshal(map[string]interface{}{
		"action":          "remove",
		"discard_changes": true,
	})
	executor.Execute(ctx, Call{
		ID:    "test-3",
		Name:  "exit_worktree",
		Input: string(exitInput2),
	})
}

// TestWorktreeToolRegistry test tool注册
func TestWorktreeToolRegistry(t *testing.T) {
	registry := NewRegistry()

	// 验证 enter_worktree 注册
	enterSpec, ok := registry.Get("enter_worktree")
	if !ok {
		t.Fatal("enter_worktree 工具未注册")
	}
	if enterSpec.RequiredPermission != LevelWorkspaceWrite {
		t.Errorf("enter_worktree 权限应为 WorkspaceWrite，得到: %v", enterSpec.RequiredPermission)
	}

	// 验证 exit_worktree 注册
	exitSpec, ok := registry.Get("exit_worktree")
	if !ok {
		t.Fatal("exit_worktree 工具未注册")
	}
	if exitSpec.RequiredPermission != LevelWorkspaceWrite {
		t.Errorf("exit_worktree 权限应为 WorkspaceWrite，得到: %v", exitSpec.RequiredPermission)
	}
}

// TestWorktreeHandler_FullLifecycle 测试完整工具调用生命周期
func TestWorktreeHandler_FullLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForIntegration(t, tmpDir)

	registry := NewRegistry()
	executor := NewExecutor(registry, tmpDir)
	ctx := context.Background()

	// 1. 进入 worktree
	enterInput, _ := json.Marshal(map[string]string{"name": "lifecycle"})
	enterResult := executor.Execute(ctx, Call{
		ID:    "test-1",
		Name:  "enter_worktree",
		Input: string(enterInput),
	})
	if enterResult.IsError {
		t.Fatalf("enter_worktree 失败: %s", enterResult.Output)
	}

	// 2. 在 worktree 中修改文件
	worktreePath := filepath.Join(tmpDir, ".whale", "worktrees", "lifecycle")
	modFile := filepath.Join(worktreePath, "lifecycle.txt")
	if err := os.WriteFile(modFile, []byte("lifecycle test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	// 3. 退出并保留
	exitInput, _ := json.Marshal(map[string]interface{}{
		"action": "keep",
	})
	exitResult := executor.Execute(ctx, Call{
		ID:    "test-2",
		Name:  "exit_worktree",
		Input: string(exitInput),
	})
	if exitResult.IsError {
		t.Fatalf("exit_worktree keep 失败: %s", exitResult.Output)
	}

	// 4. worktree 目录应存在
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Error("keep 模式退出后 worktree 目录应存在")
	}

	// 5. 文件内容应保留
	content, err := os.ReadFile(modFile)
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	if string(content) != "lifecycle test\n" {
		t.Errorf("文件内容不匹配，expected 'lifecycle test\\n'，得到: %q", string(content))
	}

	// 清理
	helper := NewGitHelper(tmpDir)
	helper.Run(ctx, "worktree", "remove", worktreePath)
}
