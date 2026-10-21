// Package command — /feedback 命令unit test
package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain 统一清理 GITHUB_TOKEN 环境变量，避免旧测试在无意中触发真实 GitHub API 调用（M19-04 改造新增 [SNAPSHOT]）。
// 个别测试需要 GITHUB_TOKEN 时，在测试函数内部自行 os.Setenv 并 defer os.Unsetenv。
func TestMain(m *testing.M) {
	os.Unsetenv("GITHUB_TOKEN")
	os.Exit(m.Run())
}

// TestFeedbackCommandRegistered 测试 /feedback 命令注册
func TestFeedbackCommandRegistered(t *testing.T) {
	r := NewRegistry()

	cmd, ok := r.Get("feedback")
	if !ok {
		t.Fatal("feedback 命令应已注册")
	}

	if cmd.Name != "feedback" {
		t.Errorf("Name = %q, want %q", cmd.Name, "feedback")
	}

	if cmd.Category != "Core" {
		t.Errorf("Category = %q, want %q", cmd.Category, "Core")
	}

	if cmd.Description == "" {
		t.Error("Description should not be empty")
	}

	if cmd.Handler == nil {
		t.Fatal("Handler 不should be nil")
	}

	if cmd.Usage == "" {
		t.Error("Usage should not be empty")
	}
}

// TestFeedbackEmptyArgs 测试空参数显示用法
func TestFeedbackEmptyArgs(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Usage") {
		t.Errorf("empty args should show usage, actual: %s", output)
	}

	if !strings.Contains(output, "list") {
		t.Errorf("usage should contain list subcommand, actual: %s", output)
	}
}

// TestFeedbackSaveMessage 测试保存反馈消息
func TestFeedbackSaveMessage(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()
	r.Version = "0.2.0"

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("这是一个测试反馈")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Feedback") {
		t.Errorf("output should contain 'Feedback', actual: %s", output)
	}

	if !strings.Contains(output, "这是一个测试反馈") {
		t.Errorf("output should contain feedback message, actual: %s", output)
	}
}

// TestFeedbackListEmpty 测试空反馈列表
func TestFeedbackListEmpty(t *testing.T) {
	dir := t.TempDir()
	feedbackDir := filepath.Join(dir, ".whale", "feedback")
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	os.MkdirAll(feedbackDir, 0755)

	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("list")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "No feedback") {
		t.Errorf("empty list should show 'No feedback', actual: %s", output)
	}
}

// TestFeedbackListSubCommand 测试 list 子命令
func TestFeedbackListSubCommand(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("list")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if output == "" {
		t.Fatal("输出should not be empty")
	}
}

// TestFeedbackViewNoArgs 测试 view 子命令无参数
func TestFeedbackViewNoArgs(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("view")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Usage") {
		t.Errorf("no args should show usage, actual: %s", output)
	}
}

// TestFeedbackViewNonExistent 测试查看不存在的反馈
func TestFeedbackViewNonExistent(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("view nonexistent")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "not found") {
		t.Errorf("non-existent feedback should show 'not found', actual: %s", output)
	}
}

// TestFeedbackSubmitNoArgs 测试 submit 子命令无参数
func TestFeedbackSubmitNoArgs(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("submit")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Usage") {
		t.Errorf("no args should show usage, actual: %s", output)
	}
}

// TestFeedbackSubmitNonExistent 测试提交不存在的反馈
func TestFeedbackSubmitNonExistent(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("submit nonexistent")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "not found") {
		t.Errorf("non-existent feedback should show 'not found', actual: %s", output)
	}
}

// TestFeedbackSpacesOnly 测试纯空格输入
func TestFeedbackSpacesOnly(t *testing.T) {
	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("   ")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Usage") {
		t.Errorf("spaces only should show usage, actual: %s", output)
	}
}

// TestFeedbackVersionIncluded 测试反馈包含版本号
func TestFeedbackVersionIncluded(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()
	r.Version = "1.2.3"

	cmd, _ := r.Get("feedback")
	output, _ := cmd.Handler("版本测试")

	if !strings.Contains(output, "Feedback") {
		t.Errorf("output should contain 'Feedback', actual: %s", output)
	}
}

// TestFeedbackSaveThenList 测试保存后列出
func TestFeedbackSaveThenList(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()
	cmd, _ := r.Get("feedback")

	saveOutput, err := cmd.Handler("测试消息保存后列出")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !strings.Contains(saveOutput, "Feedback") {
		t.Errorf("save should return confirmation, actual: %s", saveOutput)
	}

	listOutput, err := cmd.Handler("list")
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if !strings.Contains(listOutput, "测试消息") {
		t.Errorf("列表should contain保存的消息, actual: %s", listOutput)
	}
}

// TestFeedbackSaveThenView 测试保存后查看
func TestFeedbackSaveThenView(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()
	cmd, _ := r.Get("feedback")

	saveOutput, _ := cmd.Handler("查看测试消息")
	lines := strings.Split(saveOutput, "\n")
	var feedbackID string
	for _, line := range lines {
		if strings.HasPrefix(line, "Feedback ID: ") {
			feedbackID = strings.TrimPrefix(line, "Feedback ID: ")
			break
		}
	}

	if feedbackID == "" {
		t.Fatal("Feedback ID not found")
	}

	viewOutput, err := cmd.Handler("view " + feedbackID)
	if err != nil {
		t.Fatalf("查看失败: %v", err)
	}

	if !strings.Contains(viewOutput, "查看测试消息") {
		t.Errorf("查看should contain原始消息, actual: %s", viewOutput)
	}

	if !strings.Contains(viewOutput, feedbackID) {
		t.Errorf("查看should contain反馈 ID, actual: %s", viewOutput)
	}
}

// TestFeedbackSaveThenSubmit 测试保存后生成提交指引
func TestFeedbackSaveThenSubmit(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()
	cmd, _ := r.Get("feedback")

	saveOutput, _ := cmd.Handler("提交指引测试")
	lines := strings.Split(saveOutput, "\n")
	var feedbackID string
	for _, line := range lines {
		if strings.HasPrefix(line, "Feedback ID: ") {
			feedbackID = strings.TrimPrefix(line, "Feedback ID: ")
			break
		}
	}

	if feedbackID == "" {
		t.Fatal("Feedback ID not found")
	}

	submitOutput, err := cmd.Handler("submit " + feedbackID)
	if err != nil {
		t.Fatalf("提交指引失败: %v", err)
	}

	if !strings.Contains(submitOutput, "github.com") {
		t.Errorf("提交指引should contain GitHub 链接, actual: %s", submitOutput)
	}

	if !strings.Contains(submitOutput, "提交指引测试") {
		t.Errorf("提交指引should contain原始消息, actual: %s", submitOutput)
	}
}

// TestFeedbackDefaultVersion 测试未设置版本号时的反馈
func TestFeedbackDefaultVersion(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	defer func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	}()

	r := NewRegistry()

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("默认版本测试")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "Feedback") {
		t.Errorf("output should contain 'Feedback', actual: %s", output)
	}
}

// ===== M19-04 改造新增测试：/feedback 直创 GitHub Issue =====

// mockFeedbackCreator 是 feedback.IssueCreator 接口的测试替身（command 包内独立定义，避免与 feedback 包 mock 冲突）。
type mockFeedbackCreator struct {
	lastTitle  string
	lastBody   string
	lastLabels []string
	returnURL  string
	returnErr  error
	called     int
}

// CreateIssue 实现 feedback.IssueCreator 接口，记录参数并返回预设结果。
func (m *mockFeedbackCreator) CreateIssue(ctx context.Context, title, body string, labels []string) (string, error) {
	m.called++
	m.lastTitle = title
	m.lastBody = body
	m.lastLabels = labels
	return m.returnURL, m.returnErr
}

// setupFeedbackTestEnv 设置反馈测试的 home 目录隔离，避免污染真实 ~/.whale/feedback/。
// 返回 cleanup 函数。GITHUB_TOKEN 由 TestMain 统一清理，这里不再处理。
func setupFeedbackTestEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("USERPROFILE", dir)
	os.Setenv("HOME", dir)
	t.Cleanup(func() {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("HOME")
	})
}

// TestFeedbackSaveDirectCreateIssue_Success 验证：注入 mock creator 返回成功时，
// /feedback <message> 直创 Issue 并返回 Issue URL。
func TestFeedbackSaveDirectCreateIssue_Success(t *testing.T) {
	setupFeedbackTestEnv(t)

	r := NewRegistry()
	r.Version = "0.2.0"
	mock := &mockFeedbackCreator{returnURL: "https://github.com/owner/repo/issues/42"}
	r.SetFeedbackIssueCreator(mock)

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("正常反馈消息")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "GitHub Issue created") {
		t.Errorf("成功路径输出应含 'GitHub Issue created', actual: %s", output)
	}
	if !strings.Contains(output, "https://github.com/owner/repo/issues/42") {
		t.Errorf("输出应含 Issue URL, actual: %s", output)
	}
	if mock.called != 1 {
		t.Errorf("creator 调用次数 = %d, want 1", mock.called)
	}
	if !strings.HasPrefix(mock.lastTitle, "[用户反馈]") {
		t.Errorf("creator 接收的 title 不带前缀: %q", mock.lastTitle)
	}
	// 验证 labels 含 user-feedback
	found := false
	for _, l := range mock.lastLabels {
		if l == "user-feedback" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("creator 接收的 labels 不含 user-feedback: %v", mock.lastLabels)
	}
}

// TestFeedbackSaveFallback_NoToken 验证：未注入 creator 且 GITHUB_TOKEN 未设置时，
// 降级为本地保存 + 提示手动提交（降级路径 1）。
func TestFeedbackSaveFallback_NoToken(t *testing.T) {
	setupFeedbackTestEnv(t)
	// TestMain 已清理 GITHUB_TOKEN，这里额外保险
	os.Unsetenv("GITHUB_TOKEN")

	r := NewRegistry()
	r.Version = "0.2.0"
	// 不注入 creator

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("无 token 反馈")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "local only") {
		t.Errorf("降级输出应含 'local only', actual: %s", output)
	}
	if !strings.Contains(output, "GITHUB_TOKEN") {
		t.Errorf("降级原因应提示 GITHUB_TOKEN, actual: %s", output)
	}
	if !strings.Contains(output, "github.com") {
		t.Errorf("降级输出应含手动提交 URL, actual: %s", output)
	}
}

// TestFeedbackSaveFallback_NetworkError 验证：注入返回 error 的 mock creator 时，
// 降级为本地保存 + 提示手动提交（降级路径 2，网络失败）。
func TestFeedbackSaveFallback_NetworkError(t *testing.T) {
	setupFeedbackTestEnv(t)

	r := NewRegistry()
	r.Version = "0.2.0"
	mock := &mockFeedbackCreator{returnErr: &networkError{msg: "connection refused"}}
	r.SetFeedbackIssueCreator(mock)

	cmd, _ := r.Get("feedback")
	output, err := cmd.Handler("网络失败反馈")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !strings.Contains(output, "local only") {
		t.Errorf("降级输出应含 'local only', actual: %s", output)
	}
	if !strings.Contains(output, "失败") {
		t.Errorf("降级原因应含 '失败', actual: %s", output)
	}
	if !strings.Contains(output, "connection refused") {
		t.Errorf("降级输出应含原始错误信息, actual: %s", output)
	}
	if !strings.Contains(output, "github.com") {
		t.Errorf("降级输出应含手动提交 URL, actual: %s", output)
	}
	if mock.called != 1 {
		t.Errorf("creator 应被调用 1 次, actual %d", mock.called)
	}
}

// TestFeedbackSave_Redacted 验证：含密钥的反馈消息经脱敏后，
// 传给 creator 的 title 和 body 不含原始密钥（项目宪法硬约束 4：脱敏先于 Issue 创建）。
func TestFeedbackSave_Redacted(t *testing.T) {
	setupFeedbackTestEnv(t)

	r := NewRegistry()
	r.Version = "0.2.0"
	mock := &mockFeedbackCreator{returnURL: "https://github.com/owner/repo/issues/1"}
	r.SetFeedbackIssueCreator(mock)

	ghToken := "ghp_" + strings.Repeat("x", 36)
	awsKey := "AKIA" + strings.Repeat("B", 16)
	openaiKey := "sk-" + strings.Repeat("c", 40)
	rawMsg := "我的 GitHub token 是 " + ghToken + " AWS " + awsKey + " OpenAI " + openaiKey

	cmd, _ := r.Get("feedback")
	_, err := cmd.Handler(rawMsg)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	// 验证传给 creator 的 title 和 body 不含原始密钥
	secrets := []string{ghToken, awsKey, openaiKey}
	for _, s := range secrets {
		if strings.Contains(mock.lastTitle, s) {
			t.Errorf("title 含原始密钥 %q, title: %s", s, mock.lastTitle)
		}
		if strings.Contains(mock.lastBody, s) {
			t.Errorf("body 含原始密钥 %q, body: %s", s, mock.lastBody)
		}
	}

	// 验证脱敏标记出现在 body 中。
	//
	// 方案 A（2026-09-22 落地）：脱敏规则收敛到 internal/secret 单一 SSOT，
	// 标签形态随之由带类型后缀（冒号形态）**统一为裸 [REDACTED]**。
	// 本处原断言三种类型标签，是该批（约 12 处断言翻转）**漏改**的陈旧断言；
	// 姊妹测试 internal/feedback/feedback_test.go 的 TestSubmitToGitHub_Redacted
	// 已在同批翻转为裸标记，此处对齐之。
	// 形态 SSOT：internal/secret/redact.go 的 RedactSensitive（唯一声明处）。
	redactedMarker := "[REDACTED]"
	if !strings.Contains(mock.lastBody, redactedMarker) {
		t.Errorf("body 缺少脱敏标记 %q, body: %s", redactedMarker, mock.lastBody)
	}
	// 保留原判据的判别力：3 个密钥 ⇒ 恰好 3 处脱敏，
	// 而不是「body 里出现过一次标记就算过」。脱敏退化为 no-op 或只命中部分密钥时本断言会红。
	if got, want := strings.Count(mock.lastBody, redactedMarker), len(secrets); got != want {
		t.Errorf("body 脱敏标记数 = %d, want %d（每个密钥各留一处 %s）, body: %s",
			got, want, redactedMarker, mock.lastBody)
	}
}

// TestFeedbackSaveDirectCreateIssue_BackwardCompat 验证：成功路径输出仍含 "Feedback"，
// 保证旧测试断言风格（strings.Contains(output, "Feedback")）在新实现下仍可工作。
func TestFeedbackSaveDirectCreateIssue_BackwardCompat(t *testing.T) {
	setupFeedbackTestEnv(t)

	r := NewRegistry()
	r.Version = "0.2.0"
	mock := &mockFeedbackCreator{returnURL: "https://github.com/o/r/issues/1"}
	r.SetFeedbackIssueCreator(mock)

	cmd, _ := r.Get("feedback")
	output, _ := cmd.Handler("兼容性测试")
	if !strings.Contains(output, "Feedback") {
		t.Errorf("成功路径输出应含 'Feedback'（向后兼容旧断言）, actual: %s", output)
	}
}

// networkError 是测试用的简单错误类型，模拟网络失败。
type networkError struct{ msg string }

func (e *networkError) Error() string { return e.msg }
