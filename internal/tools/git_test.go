package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo 在指定目录初始化一个 git 仓库并创建初始提交
// 返回仓库路径，如果初始化失败则调用 t.Fatal
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()

	// git init
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init 失败: %v, 输出: %s", err, string(output))
	}

	// 配置User信息（某些 git 操作需要）
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config email 失败: %v, 输出: %s", err, string(output))
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config name 失败: %v, 输出: %s", err, string(output))
	}

	return dir
}

// createInitialCommit 在 git 仓库中创建初始提交
// 使用 Go 标准库写入文件，确保跨平台compatibility
func createInitialCommit(t *testing.T, repoDir string) {
	t.Helper()

	// 使用 Go 标准库创建文件，避免 Windows cmd echo 的compatibility问题
	testFile := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	// git add
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = repoDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add 失败: %v, 输出: %s", err, string(output))
	}

	// git commit
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = repoDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit 失败: %v, 输出: %s", err, string(output))
	}
}

// TestGitHelper_IsGitRepo_True 测试在 git 仓库内返回 true
// 验证 IsGitRepo 在有效的 git 仓库目录中返回 true
func TestGitHelper_IsGitRepo_True(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	if !helper.IsGitRepo(ctx) {
		t.Error("在 git 仓库内expected IsGitRepo 返回 true")
	}
}

// TestGitHelper_IsGitRepo_False 测试在非 git 仓库目录返回 false
// 验证 IsGitRepo 在非 git 仓库目录中返回 false
func TestGitHelper_IsGitRepo_False(t *testing.T) {
	tmpDir := t.TempDir()
	// 不执行 git init

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	if helper.IsGitRepo(ctx) {
		t.Error("在非 git 仓库目录expected IsGitRepo 返回 false")
	}
}

// TestGitHelper_Status 测试获取 git 状态
// 验证 Status 方法能正确返回工作区文件状态
func TestGitHelper_Status(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	// 初始提交后，工作区应该是干净的
	status, err := helper.Status(ctx)
	if err != nil {
		t.Fatalf("Status 不expected返回错误: %v", err)
	}
	if status != "" {
		t.Logf("初始提交后状态: %q（可能包含换行符等）", status)
	}

	// 创建一个新文件，检查状态变化
	newFile := filepath.Join(tmpDir, "newfile.txt")
	if err := os.WriteFile(newFile, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("创建新文件失败: %v", err)
	}

	status, err = helper.Status(ctx)
	if err != nil {
		t.Fatalf("添加文件后 Status 不expected返回错误: %v", err)
	}
	// 新文件应出现在状态中（untracked 或 modified）
	if status == "" {
		t.Error("添加新文件后expected Status 返回非空结果")
	}
}

// TestGitHelper_Diff 测试获取 git diff 输出
// 验证 Diff 方法能正确返回文件差异
func TestGitHelper_Diff(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	// 修改文件
	readmeFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Modified Project\n"), 0644); err != nil {
		t.Fatalf("修改文件失败: %v", err)
	}

	// 未暂存的 diff
	diff, err := helper.Diff(ctx, false)
	if err != nil {
		t.Fatalf("Diff 不expected返回错误: %v", err)
	}
	if !strings.Contains(diff, "Modified") {
		t.Errorf("expected diff 包含 'Modified'，得到: %s", diff)
	}

	// 暂存后测试 staged diff
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	cmd.CombinedOutput()

	stagedDiff, err := helper.Diff(ctx, true)
	if err != nil {
		t.Fatalf("Diff(staged) 不expected返回错误: %v", err)
	}
	if !strings.Contains(stagedDiff, "Modified") {
		t.Errorf("expected staged diff 包含 'Modified'，得到: %s", stagedDiff)
	}
}

// TestGitHelper_Log 测试获取 git 日志
// 验证 Log 方法能正确返回提交日志
func TestGitHelper_Log(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	log, err := helper.Log(ctx, 5)
	if err != nil {
		t.Fatalf("Log 不expected返回错误: %v", err)
	}
	if !strings.Contains(log, "initial commit") {
		t.Errorf("expected日志包含 'initial commit'，得到: %s", log)
	}
}

// TestGitHelper_BranchList 测试获取分支列表
// 验证 BranchList 方法能正确返回分支信息
func TestGitHelper_BranchList(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	branches, err := helper.BranchList(ctx)
	if err != nil {
		t.Fatalf("BranchList 不expected返回错误: %v", err)
	}
	// 应至少包含当前分支
	if branches == "" {
		t.Error("expected BranchList 返回非空结果")
	}
}

// TestGitHelper_CurrentBranch 测试获取当前分支名
// 验证 CurrentBranch 方法能正确返回当前分支名称
func TestGitHelper_CurrentBranch(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	branch, err := helper.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("CurrentBranch 不expected返回错误: %v", err)
	}
	if branch == "" {
		t.Error("expected CurrentBranch 返回非空分支名")
	}
	// 初始化后默认分支可能是 main 或 master
	if branch != "main" && branch != "master" {
		t.Logf("当前分支名: %q（非 main/master，可能为自定义默认分支）", branch)
	}
}

// TestGitHelper_NotGitRepo 测试在非 git 目录执行操作返回错误
// 验证在非 git 仓库目录中执行 git 操作时能正确返回错误
func TestGitHelper_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	// 不初始化 git 仓库

	helper := NewGitHelper(tmpDir)
	ctx := context.Background()

	// Status 在非 git 仓库should return error
	_, err := helper.Status(ctx)
	if err == nil {
		t.Error("在非 git 仓库执行 Status expected返回错误")
	}

	// Diff 在非 git 仓库should return error
	_, err = helper.Diff(ctx, false)
	if err == nil {
		t.Error("在非 git 仓库执行 Diff expected返回错误")
	}

	// Log 在非 git 仓库should return error
	_, err = helper.Log(ctx, 5)
	if err == nil {
		t.Error("在非 git 仓库执行 Log expected返回错误")
	}

	// BranchList 在非 git 仓库should return error
	_, err = helper.BranchList(ctx)
	if err == nil {
		t.Error("在非 git 仓库执行 BranchList expected返回错误")
	}

	// CurrentBranch 在非 git 仓库should return error
	_, err = helper.CurrentBranch(ctx)
	if err == nil {
		t.Error("在非 git 仓库执行 CurrentBranch expected返回错误")
	}
}
