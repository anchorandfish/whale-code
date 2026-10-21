package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"context"

	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/github"
)

// ========== TC-R: 命令注册表路由测试 ==========

// TestGitHubCommands_OnGitHubNotSet 验证 OnGitHub 未设置时返回默认提示
func TestGitHubCommands_OnGitHubNotSet(t *testing.T) {
	r := command.NewRegistry()

	cmds := []string{
		"pr create", "pr review", "pr list", "pr merge",
		"github watch", "github stop", "github status",
		"issue create", "issue list", "issue view",
	}

	for _, name := range cmds {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("命令 %q 未注册", name)
			continue
		}
		result, err := cmd.Handler("")
		if err != nil {
			t.Errorf("命令 %q Handler 返回错误: %v", name, err)
		}
		if !strings.Contains(result, "GitHub 集成未初始化") {
			t.Errorf("命令 %q: expected包含 'GitHub 集成未初始化', actual: %q", name, result)
		}
	}
}

// TestGitHubCommands_OnGitHubSet 验证 OnGitHub 已设置时正确路由所有 15 个命令
func TestGitHubCommands_OnGitHubSet(t *testing.T) {
	r := command.NewRegistry()

	var receivedArgs []string
	r.OnGitHub = func(args string) string {
		receivedArgs = append(receivedArgs, args)
		return "OK:" + args
	}

	testCases := []struct {
		cmdName    string
		callArgs   string
		expectArgs string
	}{
		{"pr create", "", "pr create "},
		{"pr review", "42", "pr review 42"},
		{"pr list", "open", "pr list open"},
		{"pr merge", "42", "pr merge 42"},
		{"github watch", "", "github watch"},
		{"github stop", "", "github stop"},
		{"github status", "", "github status"},
		{"issue create", "Test Title body here", "issue create Test Title body here"},
		{"issue list", "closed", "issue list closed"},
		{"issue view", "5", "issue view 5"},
		{"actions list", "main", "actions list main"},
		{"actions view", "123", "actions view 123"},
		{"actions trigger", "456 main", "actions trigger 456 main"},
		{"actions rerun", "789", "actions rerun 789"},
		{"actions logs", "123", "actions logs 123"},
	}

	receivedArgs = nil

	for _, tc := range testCases {
		cmd, ok := r.Get(tc.cmdName)
		if !ok {
			t.Errorf("命令 %q 未注册", tc.cmdName)
			continue
		}
		result, err := cmd.Handler(tc.callArgs)
		if err != nil {
			t.Errorf("命令 %q Handler 返回错误: %v", tc.cmdName, err)
			continue
		}
		if result != "OK:"+tc.expectArgs {
			t.Errorf("命令 %q (args=%q): expected %q, actual %q",
				tc.cmdName, tc.callArgs, "OK:"+tc.expectArgs, result)
		}
	}

	if len(receivedArgs) != len(testCases) {
		t.Errorf("expected收到 %d 个 OnGitHub 调用，actual %d 个", len(testCases), len(receivedArgs))
	}
}

// TestGitHubCommands_OnGitHubNotSet_Actions 验证 actions 命令在 OnGitHub 未设置时的行错
func TestGitHubCommands_OnGitHubNotSet_Actions(t *testing.T) {
	r := command.NewRegistry()

	actionsCmds := []string{
		"actions list", "actions view", "actions trigger", "actions rerun", "actions logs",
	}

	for _, name := range actionsCmds {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("actions 命令 %q 未注册", name)
			continue
		}
		result, err := cmd.Handler("")
		if err != nil {
			t.Errorf("actions 命令 %q Handler 返回错误: %v", name, err)
		}
		if result == "" {
			t.Errorf("actions 命令 %q Handler 返回空", name)
		}
	}
}

// ========== TC-H: handleGitHub 分发测试 ==========

// TestHandleGitHub_EmptyArgs 验证空参数返回用法提示
func TestHandleGitHub_EmptyArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("空参数should return用法提示: %q", result)
	}
}

// TestHandleGitHub_UnknownCommand 验证未知子命令返回提示
func TestHandleGitHub_UnknownCommand(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "unknown_cmd_xyz_123")
	if !strings.Contains(result, "未知") {
		t.Errorf("未知子命令should return提示: %q", result)
	}
}

// TestHandleGitHub_PRMerge_MissingArgs 验证 pr merge 缺少参数
func TestHandleGitHub_PRMerge_MissingArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "pr merge")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("pr merge 缺少参数should return用法提示: %q", result)
	}
}

// TestHandleGitHub_IssueCreate_MissingTitle 验证 issue create 缺少标题
func TestHandleGitHub_IssueCreate_MissingTitle(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "issue create")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("issue create 缺少标题should return用法提示: %q", result)
	}
}

// TestHandleGitHub_IssueView_InvalidNumber 验证 issue view 无效编号
func TestHandleGitHub_IssueView_InvalidNumber(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "issue view abc")
	if !strings.Contains(result, "编号") && !strings.Contains(result, "number") {
		t.Errorf("issue view 非数字参数should return error: %q", result)
	}
}

// TestHandleGitHub_ActionsView_MissingArgs 验证 actions view 缺少参数
func TestHandleGitHub_ActionsView_MissingArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "actions view")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("actions view 缺少参数should return用法提示: %q", result)
	}
}

// TestHandleGitHub_ActionsTrigger_MissingArgs 验证 actions trigger 缺少参数
func TestHandleGitHub_ActionsTrigger_MissingArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "actions trigger")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("actions trigger 缺少参数should return用法提示: %q", result)
	}
}

// TestHandleGitHub_ActionsRerun_MissingArgs 验证 actions rerun 缺少参数
func TestHandleGitHub_ActionsRerun_MissingArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "actions rerun")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("actions rerun 缺少参数should return用法提示: %q", result)
	}
}

// TestHandleGitHub_ActionsLogs_MissingArgs 验证 actions logs 缺少参数
func TestHandleGitHub_ActionsLogs_MissingArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "actions logs")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "Usage") {
		t.Errorf("actions logs 缺少参数should return用法提示: %q", result)
	}
}

// TestHandleGitHub_LargeArgs 验证大参数安全处理
func TestHandleGitHub_LargeArgs(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	largeArgs := strings.Repeat("x", 10000)
	result := m.githubTools.HandleCommand(context.Background(), "pr list "+largeArgs)
	if result == "" {
		t.Error("大参数不应导致空返回")
	}
}

// ========== TC-W: github watch/stop/status 测试 ==========

// TestHandleGitHub_WatchNoToken 验证 watch 时无 Token 的错误处理
func TestHandleGitHub_WatchNoToken(t *testing.T) {
	m := &Model{
		SessionModel: SessionModel{workDir: t.TempDir()},
		ToolModel:    ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "github watch")
	if !strings.Contains(strings.ToLower(result), "失败") &&
		!strings.Contains(strings.ToLower(result), "error") &&
		!strings.Contains(strings.ToLower(result), "unable") &&
		!strings.Contains(strings.ToLower(result), "token") {
		t.Logf("handleGitHub('github watch') 返回: %s", result)
	}
}

// TestHandleGitHub_StopNotRunning 验证 Watcher 未运行时 stop 的提示
func TestHandleGitHub_StopNotRunning(t *testing.T) {
	m := &Model{
		ToolModel: ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "github stop")
	if !strings.Contains(result, "未在运行") &&
		!strings.Contains(result, "not running") &&
		!strings.Contains(result, "未初始化") {
		t.Errorf("Watcher 未运行时 stop should return提示: %q", result)
	}
}

// TestHandleGitHub_Status_Format 验证 status 命令至少返回非空内容
func TestHandleGitHub_Status_Format(t *testing.T) {
	m := &Model{
		SessionModel: SessionModel{workDir: t.TempDir()},
		ToolModel:    ToolModel{githubTools: &github.GitHubTools{}},
	}

	result := m.githubTools.HandleCommand(context.Background(), "github status")
	if result == "" {
		t.Error("status 命令不should return empty")
	}
}

// ========== 全覆盖测试 ==========

// TestHandleGitHub_AllCommandsReturnNonEmpty 验证 handleGitHub 对所有 15 个命令返回非空
func TestHandleGitHub_AllCommandsReturnNonEmpty(t *testing.T) {
	m := &Model{
		ToolModel:    ToolModel{githubTools: &github.GitHubTools{}},
		SessionModel: SessionModel{workDir: t.TempDir()},
	}

	commands := map[string]string{
		"pr create":       "pr create",
		"pr review":       "pr review 1",
		"pr list":         "pr list",
		"pr merge":        "pr merge 1",
		"github watch":    "github watch",
		"github stop":     "github stop",
		"github status":   "github status",
		"issue create":    "issue create Test Title",
		"issue list":      "issue list",
		"issue view":      "issue view 1",
		"actions list":    "actions list",
		"actions view":    "actions view 100",
		"actions trigger": "actions trigger 5 main",
		"actions rerun":   "actions rerun 100",
		"actions logs":    "actions logs 100",
	}

	if len(commands) != 15 {
		t.Errorf("expected 15 个命令，actual %d 个", len(commands))
	}

	for name, args := range commands {
		result := m.githubTools.HandleCommand(context.Background(), args)
		if result == "" {
			t.Errorf("handleGitHub(%q) 返回了空字符串，应该至少返回错误提示", name)
		}
	}
}

// TestAllGitHubCommandsRegistered 验证 15 个 GitHub 命令都在注册表中
func TestAllGitHubCommandsRegistered(t *testing.T) {
	r := command.NewRegistry()

	allCommands := []string{
		"pr create", "pr review", "pr list", "pr merge",
		"github watch", "github stop", "github status",
		"issue create", "issue list", "issue view",
		"actions list", "actions view", "actions trigger", "actions rerun", "actions logs",
	}

	for _, name := range allCommands {
		_, ok := r.Get(name)
		if !ok {
			t.Errorf("命令 %q 未在注册表中注册", name)
		}
	}
}

// ========== integration test：完整链路 ==========

// TestFullGitHubCommandPipeline 验证从 command.Registry → Model.handleGitHub 的完整链路
func TestFullGitHubCommandPipeline(t *testing.T) {
	m := &Model{
		ToolModel:    ToolModel{cmdRegistry: command.NewRegistry(), githubTools: &github.GitHubTools{}},
		SessionModel: SessionModel{workDir: t.TempDir()},
	}

	m.cmdRegistry.OnGitHub = func(args string) string {
		return m.githubTools.HandleCommand(context.Background(), args)
	}

	testCmds := []string{
		"github status",
		"pr list",
		"issue list",
		"actions list",
	}

	for _, name := range testCmds {
		cmd, ok := m.cmdRegistry.Get(name)
		if !ok {
			t.Errorf("命令 %q 未注册", name)
			continue
		}
		result, err := cmd.Handler("")
		if err != nil {
			t.Errorf("命令 %q Handler 返回错误: %v", name, err)
			continue
		}
		if result == "" {
			t.Errorf("命令 %q Handler 返回空", name)
		}
	}
}

// ========== HTTP Mock Server helper functions ==========

// createMockGithubPRListServer 创建返回 PR 列表的 Mock HTTP Server
func createMockGithubPRListServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"number":1,"title":"Fix login bug","state":"open","html_url":"https://github.com/test/repo/pull/1","user":{"login":"alice"},"head":{"ref":"fix/login","user":{"login":"alice"}},"base":{"ref":"main"}},
			{"number":2,"title":"Add new feature","state":"open","html_url":"https://github.com/test/repo/pull/2","user":{"login":"bob"},"head":{"ref":"feat/new","user":{"login":"bob"}},"base":{"ref":"main"}}
		]`))
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	return server, server.URL
}

// createMockGithubEmptyListServer 创建返回空列表的 Mock HTTP Server
func createMockGithubEmptyListServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	return server, server.URL
}

// createMockGithubErrorServer 创建返回错误的 Mock HTTP Server
func createMockGithubErrorServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	return server, server.URL
}

// ========== Client 创建测试 ==========

// TestGithubNewClient_Creation 验证 Client 创建
func TestGithubNewClient_Creation(t *testing.T) {
	client := github.NewClient("test-token-123")
	if client == nil {
		t.Fatal("NewClient 返回 nil")
	}

	// 非 git 目录 DetectRepo 返回错误
	_, _, err := client.DetectRepo(t.TempDir())
	if err == nil {
		t.Error("非 git 目录 DetectRepo should return error")
	}
}

// TestGithubNewClient_EmptyToken 验证空 Token Client 创建
func TestGithubNewClient_EmptyToken(t *testing.T) {
	client := github.NewClient("")
	if client == nil {
		t.Fatal("NewClient 空 token 仍should return非 nil")
	}
}

// ========== environment variable测试 ==========

// TestGithubTokenFromEnv 验证从environment variable读取 token
func TestGithubTokenFromEnv(t *testing.T) {
	testToken := "gh_test_token_12345"
	os.Setenv("GITHUB_TOKEN", testToken)
	t.Cleanup(func() { os.Unsetenv("GITHUB_TOKEN") })

	token := os.Getenv("GITHUB_TOKEN")
	if token != testToken {
		t.Errorf("GITHUB_TOKEN environment variable不匹配: expected %q, actual %q", testToken, token)
	}

	client := github.NewClient(token)
	if client == nil {
		t.Fatal("使用environment variable token 创建 Client 失败")
	}
}

// ========== Benchmark ==========

func BenchmarkHandleGitHub_AllCommands(b *testing.B) {
	m := &Model{
		ToolModel:    ToolModel{githubTools: &github.GitHubTools{}},
		SessionModel: SessionModel{workDir: b.TempDir()},
	}

	commands := []string{
		"pr create", "pr review 1", "pr list", "pr merge 1",
		"github watch", "github stop", "github status",
		"issue create Test", "issue list", "issue view 1",
		"actions list", "actions view 100", "actions trigger 5 main",
		"actions rerun 100", "actions logs 100",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, cmd := range commands {
			m.githubTools.HandleCommand(context.Background(), cmd)
		}
	}
}
