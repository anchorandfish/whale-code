package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepoForWorktree 在指定目录初始化一个 git 仓库并创建初始提交
func initGitRepoForWorktree(t *testing.T, dir string) string {
	t.Helper()

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init 失败: %v, 输出: %s", err, string(output))
	}

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

// createInitialCommitForWorktree 在 git 仓库中创建初始提交
func createInitialCommitForWorktree(t *testing.T, repoDir string) {
	t.Helper()

	testFile := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = repoDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add 失败: %v, 输出: %s", err, string(output))
	}

	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = repoDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit 失败: %v, 输出: %s", err, string(output))
	}
}

// --- UT-001 ~ UT-006: 名称验证和生成 ---

// TestValidateWorktreeSlug_Valid 测试合法的 worktree 名称
func TestValidateWorktreeSlug_Valid(t *testing.T) {
	validNames := []string{
		"fix-bug",
		"feature_x",
		"v2.0",
		"my-feature/123",
		"test.name",
		"a",
		"worktree-123",
	}
	for _, name := range validNames {
		if err := ValidateWorktreeSlug(name); err != nil {
			t.Errorf("expected名称 %q 合法，但返回错误: %v", name, err)
		}
	}
}

// TestValidateWorktreeSlug_InvalidChars 测试包含非法字符的名称
func TestValidateWorktreeSlug_InvalidChars(t *testing.T) {
	invalidNames := []string{
		"fix bug",
		"中文",
		"hack@",
		"test#1",
		"hello world",
	}
	for _, name := range invalidNames {
		if err := ValidateWorktreeSlug(name); err == nil {
			t.Errorf("expected名称 %q 不合法，但通过了验证", name)
		}
	}
}

// TestValidateWorktreeSlug_TooLong 测试过长的名称
func TestValidateWorktreeSlug_TooLong(t *testing.T) {
	longName := strings.Repeat("a", 65)
	if err := ValidateWorktreeSlug(longName); err == nil {
		t.Error("expected 65 字符的名称rejected，但通过了验证")
	}

	// 恰好 64 字符应该合法
	exactName := strings.Repeat("a", 64)
	if err := ValidateWorktreeSlug(exactName); err != nil {
		t.Errorf("expected 64 字符的名称合法，但返回错误: %v", err)
	}
}

// TestValidateWorktreeSlug_Empty 测试空名称
func TestValidateWorktreeSlug_Empty(t *testing.T) {
	if err := ValidateWorktreeSlug(""); err != nil {
		t.Errorf("expected空名称合法（将自动生成），但返回错误: %v", err)
	}
}

// TestValidateWorktreeSlug_PathTraversal 测试path traversal攻击
func TestValidateWorktreeSlug_PathTraversal(t *testing.T) {
	traversalNames := []string{
		"../hack",
		"foo/../../bar",
		"/absolute-path",
	}
	for _, name := range traversalNames {
		if err := ValidateWorktreeSlug(name); err == nil {
			t.Errorf("expected名称 %q rejected（path traversal），但通过了验证", name)
		}
	}
}

// TestWorktreeBranchName 测试分支名生成
func TestWorktreeBranchName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"fix-bug", "worktree-fix-bug"},
		{"feature", "worktree-feature"},
		{"v2.0", "worktree-v2.0"},
	}
	for _, tt := range tests {
		result := WorktreeBranchName(tt.input)
		if result != tt.expected {
			t.Errorf("WorktreeBranchName(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

// TestGenerateRandomSlug 测试随机 slug 生成
func TestGenerateRandomSlug(t *testing.T) {
	slug := GenerateRandomSlug()
	if slug == "" {
		t.Error("GenerateRandomSlug() 返回空字符串")
	}
	if !strings.HasPrefix(slug, "wt-") {
		t.Errorf("expected slug 以 'wt-' 开头，得到: %q", slug)
	}
	// 验证只包含合法字符
	if err := ValidateWorktreeSlug(slug); err != nil {
		t.Errorf("生成的 slug %q 未通过验证: %v", slug, err)
	}
	// 多次调用应生成不同的 slug
	slug2 := GenerateRandomSlug()
	if slug == slug2 {
		t.Error("两次调用 GenerateRandomSlug 返回了相同的值")
	}
}

// --- UT-010 ~ UT-014: WorktreeManager 状态管理 ---

// TestManager_InitialState 测试 Manager 初始状态
func TestManager_InitialState(t *testing.T) {
	mgr := NewWorktreeManager("/tmp/test")
	if mgr.GetCurrentSession() != nil {
		t.Error("新建 Manager 的 session should be nil")
	}
	if mgr.IsInWorktree() {
		t.Error("新建 Manager 不应在 worktree 中")
	}
}

// TestManager_SetSession test setup session
func TestManager_SetSession(t *testing.T) {
	mgr := NewWorktreeManager("/tmp/test")
	session := &WorktreeSession{
		OriginalCwd:    "/tmp/test",
		WorktreePath:   "/tmp/test/.whale/worktrees/fix-bug",
		WorktreeName:   "fix-bug",
		WorktreeBranch: "worktree-fix-bug",
		SessionID:      "test-session",
	}

	mgr.SetSession(session)

	if mgr.GetCurrentSession() == nil {
		t.Error("SetSession 后 session 不should be nil")
	}
	if !mgr.IsInWorktree() {
		t.Error("SetSession 后 IsInWorktree should return true")
	}
	if mgr.GetWorkDir() != session.WorktreePath {
		t.Errorf("GetWorkDir() = %q, expected %q", mgr.GetWorkDir(), session.WorktreePath)
	}
}

// TestManager_ClearSession 测试清除 session
func TestManager_ClearSession(t *testing.T) {
	mgr := NewWorktreeManager("/tmp/test")
	mgr.SetSession(&WorktreeSession{WorktreePath: "/tmp/test/.whale/worktrees/fix-bug"})
	mgr.ClearSession()

	if mgr.GetCurrentSession() != nil {
		t.Error("ClearSession 后 session should be nil")
	}
	if mgr.IsInWorktree() {
		t.Error("ClearSession 后 IsInWorktree should return false")
	}
}

// TestManager_GetWorkDir 测试 GetWorkDir 在不同状态下返回正确路径
func TestManager_GetWorkDir(t *testing.T) {
	mgr := NewWorktreeManager("/tmp/original")

	// 不在 worktree 中
	if mgr.GetWorkDir() != "/tmp/original" {
		t.Errorf("不在 worktree 时 GetWorkDir() = %q, expected %q", mgr.GetWorkDir(), "/tmp/original")
	}

	// 在 worktree 中
	mgr.SetSession(&WorktreeSession{WorktreePath: "/tmp/original/.whale/worktrees/test"})
	if mgr.GetWorkDir() != "/tmp/original/.whale/worktrees/test" {
		t.Errorf("在 worktree 时 GetWorkDir() = %q, expected %q", mgr.GetWorkDir(), "/tmp/original/.whale/worktrees/test")
	}
}

// TestManager_GetOriginalWorkDir 测试 GetOriginalWorkDir 始终返回原始目录
func TestManager_GetOriginalWorkDir(t *testing.T) {
	mgr := NewWorktreeManager("/tmp/original")
	mgr.SetSession(&WorktreeSession{WorktreePath: "/tmp/original/.whale/worktrees/test"})

	if mgr.GetOriginalWorkDir() != "/tmp/original" {
		t.Errorf("GetOriginalWorkDir() = %q, expected %q", mgr.GetOriginalWorkDir(), "/tmp/original")
	}
}

// --- integration test: 真实 Git 操作 ---

// TestEnterWorktree_Basic 测试基本进入 worktree 流程
func TestEnterWorktree_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	session, err := mgr.EnterWorktree(ctx, "fix-bug", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	// 验证 session 字段
	if session.OriginalCwd != tmpDir {
		t.Errorf("OriginalCwd = %q, expected %q", session.OriginalCwd, tmpDir)
	}
	if session.WorktreeName != "fix-bug" {
		t.Errorf("WorktreeName = %q, expected %q", session.WorktreeName, "fix-bug")
	}
	if session.WorktreeBranch != "worktree-fix-bug" {
		t.Errorf("WorktreeBranch = %q, expected %q", session.WorktreeBranch, "worktree-fix-bug")
	}
	if session.SessionID != "test-session" {
		t.Errorf("SessionID = %q, expected %q", session.SessionID, "test-session")
	}

	// 验证 worktree 目录存在
	if _, err := os.Stat(session.WorktreePath); os.IsNotExist(err) {
		t.Errorf("worktree 目录不存在: %s", session.WorktreePath)
	}

	// 验证分支存在
	helper := NewGitHelper(tmpDir)
	branches, err := helper.BranchList(ctx)
	if err != nil {
		t.Fatalf("获取分支列表失败: %v", err)
	}
	if !strings.Contains(branches, "worktree-fix-bug") {
		t.Errorf("分支列表中未找到 worktree-fix-bug: %s", branches)
	}

	// 验证 Manager 状态
	if !mgr.IsInWorktree() {
		t.Error("EnterWorktree 后 IsInWorktree should return true")
	}

	// 清理: 退出 worktree
	_, _, err = mgr.ExitWorktree(ctx, "remove", true)
	if err != nil {
		t.Fatalf("清理 ExitWorktree 失败: %v", err)
	}
}

// TestEnterWorktree_AutoName 测试自动生成名称
func TestEnterWorktree_AutoName(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	session, err := mgr.EnterWorktree(ctx, "", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 自动名称失败: %v", err)
	}

	if session.WorktreeName == "" {
		t.Error("自动生成的名称should not be empty")
	}
	if !strings.HasPrefix(session.WorktreeBranch, "worktree-") {
		t.Errorf("自动生成的分支名应以 'worktree-' 开头，得到: %q", session.WorktreeBranch)
	}

	// 清理
	_, _, err = mgr.ExitWorktree(ctx, "remove", true)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
}

// TestEnterWorktree_NotGitRepo 测试非 Git 仓库
func TestEnterWorktree_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	// 不初始化 git

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	_, err := mgr.EnterWorktree(ctx, "test", "test-session")
	if err == nil {
		t.Error("expected非 Git 仓库返回错误")
	}
	if !strings.Contains(err.Error(), "Git 仓库") {
		t.Errorf("错误消息should contain 'Git 仓库'，得到: %v", err)
	}
}

// TestEnterWorktree_AlreadyInWorktree test nested进入
func TestEnterWorktree_AlreadyInWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	// 第一次进入
	_, err := mgr.EnterWorktree(ctx, "first", "test-session")
	if err != nil {
		t.Fatalf("第一次 EnterWorktree 失败: %v", err)
	}

	// 第二次进入应失败
	_, err = mgr.EnterWorktree(ctx, "second", "test-session")
	if err == nil {
		t.Error("expected嵌套进入返回错误")
	}
	if !strings.Contains(err.Error(), "已在 worktree") {
		t.Errorf("错误消息should contain '已在 worktree'，得到: %v", err)
	}

	// 清理
	_, _, err = mgr.ExitWorktree(ctx, "remove", true)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
}

// TestEnterWorktree_InvalidName 测试非法名称
func TestEnterWorktree_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	_, err := mgr.EnterWorktree(ctx, "bad name!", "test-session")
	if err == nil {
		t.Error("expected非法名称返回错误")
	}
	if !strings.Contains(err.Error(), "名称验证失败") {
		t.Errorf("错误消息should contain '名称验证失败'，得到: %v", err)
	}
}

// TestExitWorktree_Keep 测试保留模式退出
func TestExitWorktree_Keep(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	session, err := mgr.EnterWorktree(ctx, "keep-test", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	worktreePath := session.WorktreePath

	// 退出并保留
	_, _, err = mgr.ExitWorktree(ctx, "keep", false)
	if err != nil {
		t.Fatalf("ExitWorktree keep 失败: %v", err)
	}

	// worktree 目录应仍存在
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Error("keep 模式退出后 worktree 目录不应被删除")
	}

	// Manager 状态应已清除
	if mgr.IsInWorktree() {
		t.Error("退出后 IsInWorktree should return false")
	}

	// 清理: 手动删除 worktree
	helper := NewGitHelper(tmpDir)
	helper.Run(ctx, "worktree", "remove", worktreePath)
}

// TestExitWorktree_Remove 测试删除模式退出
func TestExitWorktree_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	session, err := mgr.EnterWorktree(ctx, "remove-test", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	worktreePath := session.WorktreePath

	// 退出并删除
	_, _, err = mgr.ExitWorktree(ctx, "remove", false)
	if err != nil {
		t.Fatalf("ExitWorktree remove 失败: %v", err)
	}

	// worktree 目录应不存在
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Error("remove 模式退出后 worktree 目录应被删除")
	}

	// Manager 状态应已清除
	if mgr.IsInWorktree() {
		t.Error("退出后 IsInWorktree should return false")
	}
}

// TestExitWorktree_RemoveWithChanges 测试有变更时删除的安全机制
func TestExitWorktree_RemoveWithChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	session, err := mgr.EnterWorktree(ctx, "unsafe-test", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	// 在 worktree 中创建文件
	newFile := filepath.Join(session.WorktreePath, "unsafe.txt")
	if err := os.WriteFile(newFile, []byte("test content\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	// 不带 discard_changes 删除should be rejected
	_, _, err = mgr.ExitWorktree(ctx, "remove", false)
	if err == nil {
		t.Error("expected有变更时 remove rejected")
	}
	if !strings.Contains(err.Error(), "未提交文件") {
		t.Errorf("错误消息should contain '未提交文件'，得到: %v", err)
	}

	// 带 discard_changes=true 应成功
	_, _, err = mgr.ExitWorktree(ctx, "remove", true)
	if err != nil {
		t.Fatalf("discard_changes=true 时 ExitWorktree 失败: %v", err)
	}

	// worktree 应被删除
	if _, err := os.Stat(session.WorktreePath); !os.IsNotExist(err) {
		t.Error("discard_changes=true 删除后 worktree 目录应不存在")
	}
}

// TestExitWorktree_NotInSession 测试不在 worktree 中时退出
func TestExitWorktree_NotInSession(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	_, _, err := mgr.ExitWorktree(ctx, "keep", false)
	if err == nil {
		t.Error("expected不在 worktree 中时退出返回错误")
	}
	if !strings.Contains(err.Error(), "不在 worktree") {
		t.Errorf("错误消息should contain '不在 worktree'，得到: %v", err)
	}
}

// TestCountWorktreeChanges 测试变更统计
func TestCountWorktreeChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	session, err := mgr.EnterWorktree(ctx, "changes-test", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	// 干净的 worktree
	summary, err := mgr.CountWorktreeChanges(ctx)
	if err != nil {
		t.Fatalf("CountWorktreeChanges 失败: %v", err)
	}
	if summary.ChangedFiles != 0 {
		t.Errorf("干净 worktree expected ChangedFiles=0，得到 %d", summary.ChangedFiles)
	}

	// 添加文件
	newFile := filepath.Join(session.WorktreePath, "new.txt")
	if err := os.WriteFile(newFile, []byte("content\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	summary, err = mgr.CountWorktreeChanges(ctx)
	if err != nil {
		t.Fatalf("CountWorktreeChanges 失败: %v", err)
	}
	if summary.ChangedFiles == 0 {
		t.Error("添加文件后expected ChangedFiles > 0")
	}

	// 清理
	_, _, err = mgr.ExitWorktree(ctx, "remove", true)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
}

// TestCountWorktreeChanges_NotInSession 测试不在 worktree 中时统计变更
func TestCountWorktreeChanges_NotInSession(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	_, err := mgr.CountWorktreeChanges(ctx)
	if err == nil {
		t.Error("expected不在 worktree 中时统计变更返回错误")
	}
}

// TestListWorktrees 测试列出 worktree
func TestListWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	// 进入 worktree
	_, err := mgr.EnterWorktree(ctx, "list-test", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	// 列出 worktree
	list, err := mgr.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees 失败: %v", err)
	}
	if !strings.Contains(list, "list-test") {
		t.Errorf("worktree 列表should contain 'list-test'，得到: %s", list)
	}

	// 清理
	_, _, err = mgr.ExitWorktree(ctx, "remove", true)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
}

// TestListWorktrees_NotGitRepo 测试非 Git 仓库列出 worktree
func TestListWorktrees_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	_, err := mgr.ListWorktrees(ctx)
	if err == nil {
		t.Error("expected非 Git 仓库返回错误")
	}
}

// TestFullWorktreeLifecycle 测试完整的 worktree 生命周期
func TestFullWorktreeLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoForWorktree(t, tmpDir)
	createInitialCommitForWorktree(t, tmpDir)

	mgr := NewWorktreeManager(tmpDir)
	ctx := context.Background()

	// 1. 进入 worktree
	session, err := mgr.EnterWorktree(ctx, "lifecycle", "test-session")
	if err != nil {
		t.Fatalf("EnterWorktree 失败: %v", err)
	}

	// 2. 在 worktree 中修改文件
	modFile := filepath.Join(session.WorktreePath, "lifecycle.txt")
	if err := os.WriteFile(modFile, []byte("lifecycle test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	// 3. 验证变更被检测到
	summary, err := mgr.CountWorktreeChanges(ctx)
	if err != nil {
		t.Fatalf("CountWorktreeChanges 失败: %v", err)
	}
	if summary.ChangedFiles == 0 {
		t.Error("修改文件后expected检测到变更")
	}

	// 4. 保留退出
	_, _, err = mgr.ExitWorktree(ctx, "keep", false)
	if err != nil {
		t.Fatalf("ExitWorktree keep 失败: %v", err)
	}

	// 5. worktree 保留
	if _, err := os.Stat(session.WorktreePath); os.IsNotExist(err) {
		t.Error("keep 模式退出后 worktree 应保留")
	}

	// 6. 清理: 手动删除
	helper := NewGitHelper(tmpDir)
	helper.Run(ctx, "worktree", "remove", session.WorktreePath)
}
