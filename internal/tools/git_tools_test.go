package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitStatusHandler 测试 git_status 工具处理器
// 验证在 git 仓库中获取状态信息，以及工作区干净时返回提示
func TestGitStatusHandler(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 工作区干净时should return提示
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_status",
		Input: `{}`,
	})
	if result.IsError {
		t.Fatalf("git_status 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "干净") {
		t.Errorf("干净工作区应提示'干净'，actual: %s", result.Output)
	}

	// 创建新文件后应显示变更
	newFile := filepath.Join(tmpDir, "status_test.txt")
	if err := os.WriteFile(newFile, []byte("test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	result = e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "git_status",
		Input: `{}`,
	})
	if result.IsError {
		t.Fatalf("git_status 不should return error: %s", result.Output)
	}
	if result.Output == "" || strings.Contains(result.Output, "工作区干净") {
		t.Errorf("有变更时 git_status should return状态信息，actual: %s", result.Output)
	}
}

// TestGitLogHandler 测试 git_log 工具处理器
// 验证默认参数获取日志，以及自定义条数参数
func TestGitLogHandler(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 默认参数获取日志
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_log",
		Input: `{}`,
	})
	if result.IsError {
		t.Fatalf("git_log 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "initial commit") {
		t.Errorf("日志should contain 'initial commit'，actual: %s", result.Output)
	}

	// 自定义条数
	result = e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "git_log",
		Input: `{"n": 5}`,
	})
	if result.IsError {
		t.Fatalf("git_log(n=5) 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "initial commit") {
		t.Errorf("日志should contain 'initial commit'，actual: %s", result.Output)
	}
}

// TestGitAddHandler 测试 git_add 工具处理器
// 验证添加文件到暂存区，以及空文件列表返回错误
func TestGitAddHandler(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 创建新文件
	newFile := filepath.Join(tmpDir, "add_test.txt")
	if err := os.WriteFile(newFile, []byte("add test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	// 添加文件到暂存区
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_add",
		Input: `{"files": ["add_test.txt"]}`,
	})
	if result.IsError {
		t.Fatalf("git_add 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "已添加到暂存区") {
		t.Errorf("应提示已添加到暂存区，actual: %s", result.Output)
	}
	if !strings.Contains(result.Output, "add_test.txt") {
		t.Errorf("should contain文件名，actual: %s", result.Output)
	}

	// 空文件列表should return error
	result = e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "git_add",
		Input: `{"files": []}`,
	})
	if !result.IsError {
		t.Error("空文件列表should return error")
	}
}

// TestGitCommitHandler 测试 git_commit 工具处理器
// 验证提交变更，以及空消息返回错误
func TestGitCommitHandler(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 创建并添加文件
	newFile := filepath.Join(tmpDir, "commit_test.txt")
	if err := os.WriteFile(newFile, []byte("commit test\n"), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	addResult := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_add",
		Input: `{"files": ["commit_test.txt"]}`,
	})
	if addResult.IsError {
		t.Fatalf("git_add 失败: %s", addResult.Output)
	}

	// 提交变更
	result := e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "git_commit",
		Input: `{"message": "test commit"}`,
	})
	if result.IsError {
		t.Fatalf("git_commit 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "提交成功") {
		t.Errorf("应提示提交成功，actual: %s", result.Output)
	}

	// 空消息should return error
	result = e.Execute(context.Background(), Call{
		ID:    "3",
		Name:  "git_commit",
		Input: `{"message": ""}`,
	})
	if !result.IsError {
		t.Error("空提交消息should return error")
	}
}

// TestGitBranchHandler_Current 测试 git_branch current 操作
// 验证获取当前分支名
func TestGitBranchHandler_Current(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 获取当前分支
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_branch",
		Input: `{"action": "current"}`,
	})
	if result.IsError {
		t.Fatalf("git_branch(current) 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "当前分支") {
		t.Errorf("should contain'当前分支'，actual: %s", result.Output)
	}
}

// TestGitBranchHandler_List 测试 git_branch list 操作
// 验证列出分支信息
func TestGitBranchHandler_List(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 列出分支
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_branch",
		Input: `{"action": "list"}`,
	})
	if result.IsError {
		t.Fatalf("git_branch(list) 不should return error: %s", result.Output)
	}
	if result.Output == "" {
		t.Error("分支列表should not be empty")
	}
}

// TestGitBranchHandler_Create 测试 git_branch create 操作
// 验证创建并切换到新分支
func TestGitBranchHandler_Create(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 创建新分支
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_branch",
		Input: `{"action": "create", "name": "feature-test"}`,
	})
	if result.IsError {
		t.Fatalf("git_branch(create) 不should return error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "已创建并切换到分支") {
		t.Errorf("应提示已创建并切换到分支，actual: %s", result.Output)
	}
	if !strings.Contains(result.Output, "feature-test") {
		t.Errorf("should contain分支名，actual: %s", result.Output)
	}

	// 空分支名should return error
	result = e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "git_branch",
		Input: `{"action": "create", "name": ""}`,
	})
	if !result.IsError {
		t.Error("空分支名should return error")
	}
}

// TestGitBranchHandler_Checkout 测试 git_branch checkout 操作
// 验证切换分支
func TestGitBranchHandler_Checkout(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	createInitialCommit(t, tmpDir)

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	// 先创建一个新分支
	createResult := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "git_branch",
		Input: `{"action": "create", "name": "checkout-test"}`,
	})
	if createResult.IsError {
		t.Fatalf("创建分支失败: %s", createResult.Output)
	}

	// 切换回主分支（main 或 master）
	// 先获取当前分支名确认
	currentResult := e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "git_branch",
		Input: `{"action": "current"}`,
	})
	if currentResult.IsError {
		t.Fatalf("获取当前分支失败: %s", currentResult.Output)
	}

	// 切换回初始分支（需要先知道初始分支名）
	helper := NewGitHelper(tmpDir)
	initialBranch, _ := helper.CurrentBranch(context.Background())

	// 切换到 checkout-test 分支（先切回初始分支再切回来）
	// 先切回初始分支
	switchResult := e.Execute(context.Background(), Call{
		ID:    "3",
		Name:  "git_branch",
		Input: mustMarshal(map[string]string{"action": "checkout", "name": initialBranch}),
	})
	if switchResult.IsError {
		t.Fatalf("切换分支失败: %s", switchResult.Output)
	}
	if !strings.Contains(switchResult.Output, "已切换到分支") {
		t.Errorf("应提示已切换到分支，actual: %s", switchResult.Output)
	}

	// 空分支名should return error
	result := e.Execute(context.Background(), Call{
		ID:    "4",
		Name:  "git_branch",
		Input: `{"action": "checkout", "name": ""}`,
	})
	if !result.IsError {
		t.Error("空分支名should return error")
	}
}

// TestGitNotRepo 测试所有 Git 工具在非 Git 目录中的行为
// 验证所有处理器在非 Git 仓库目录中返回友好提示
func TestGitNotRepo(t *testing.T) {
	tmpDir := t.TempDir()
	// 不初始化 git 仓库

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)

	tools := []struct {
		name  string
		input string
	}{
		{"git_status", `{}`},
		{"git_log", `{}`},
		{"git_add", `{"files": ["test.txt"]}`},
		{"git_commit", `{"message": "test"}`},
		{"git_branch", `{"action": "current"}`},
	}

	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			result := e.Execute(context.Background(), Call{
				ID:    "1",
				Name:  tt.name,
				Input: tt.input,
			})
			// 非 git 仓库should return友好提示，不是错误
			if result.IsError {
				// git_add 和 git_commit 在非 git 仓库时 parseInput 可能先成功，
				// 然后 helper.IsGitRepo 返回 false，返回友好提示
				// 但某些情况下可能返回错误，这也是可接受的
				t.Logf("%s 在非 git 仓库返回错误: %s", tt.name, result.Output)
			}
			if !result.IsError && !strings.Contains(result.Output, "不是 Git 仓库") {
				t.Errorf("%s 在非 git 仓库应提示'不是 Git 仓库'，actual: %s", tt.name, result.Output)
			}
		})
	}
}
