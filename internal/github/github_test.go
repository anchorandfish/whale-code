package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// ---- Mock AIClient 用于测试 ----

// MockAIClient mock AI client，用于unit test
type MockAIClient struct {
	Response string
	Error    error
}

// Stream implementation AIClient interface，返回预设的响应
func (m *MockAIClient) Stream(ctx context.Context, req AIRequest) (<-chan AIEvent, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	out := make(chan AIEvent, 2)
	out <- AITextDeltaEvent{Content: m.Response}
	out <- AIMessageStopEvent{}
	close(out)
	return out, nil
}

// ---- Client 测试 ----

func TestNewClient(t *testing.T) {
	client := NewClient("test-token")
	if client.token != "test-token" {
		t.Errorf("expected token=test-token，actual %s", client.token)
	}
	if client.baseURL != githubAPIBase {
		t.Errorf("expected baseURL=%s，actual %s", githubAPIBase, client.baseURL)
	}
	if client.httpClient == nil {
		t.Error("httpClient 不should be nil")
	}
	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("expected Timeout=30s，actual %v", client.httpClient.Timeout)
	}
}

func TestDetectRepo_InvalidRemote(t *testing.T) {
	client := NewClient("test")
	// 无效路径should return error
	_, _, err := client.DetectRepo("C:\\nonexistent\\path\\12345")
	if err == nil {
		t.Error("无效路径should return error")
	}
}

// createTestServer 创建mock GitHub API 的测试server
func createTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

// buildPRListJSON 构建 PR 列表的 JSON 响应字符串
func buildPRListJSON() string {
	return `[
		{
			"number": 1,
			"title": "Fix login bug",
			"state": "open",
			"html_url": "https://github.com/test/repo/pull/1",
			"user": {"login": "alice"},
			"head": {"ref": "fix/login", "user": {"login": "alice"}},
			"base": {"ref": "main"}
		},
		{
			"number": 2,
			"title": "Add new feature",
			"state": "open",
			"html_url": "https://github.com/test/repo/pull/2",
			"user": {"login": "bob"},
			"head": {"ref": "feat/new", "user": {"login": "bob"}},
			"base": {"ref": "main"}
		}
	]`
}

func TestListPRs(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("认证头不正确")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(buildPRListJSON()))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	prs, err := client.ListPRs(context.Background(), "test", "repo", "open")
	if err != nil {
		t.Fatalf("列出 PR 失败: %v", err)
	}

	if len(prs) != 2 {
		t.Fatalf("expected 2 个 PR，actual %d", len(prs))
	}

	if prs[0].Number != 1 || prs[0].Title != "Fix login bug" {
		t.Errorf("第一个 PR 信息不匹配: %+v", prs[0])
	}
	if prs[1].Number != 2 || prs[1].Title != "Add new feature" {
		t.Errorf("第二个 PR 信息不匹配: %+v", prs[1])
	}
}

func TestListPRs_ErrorStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ghErrorResponse{Message: "Not Found"})
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	_, err := client.ListPRs(context.Background(), "nonexistent", "repo", "open")
	if err == nil {
		t.Fatal("expected返回错误")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("错误信息should contain状态码: %v", err)
	}
}

func TestGetPRDiff(t *testing.T) {
	expectedDiff := "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-func main() {}\n+func main() { fmt.Println() }\n"

	handler := func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "v3.diff") {
			t.Errorf("expected Accept 包含 v3.diff，actual %s", accept)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(expectedDiff))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	diff, err := client.GetPRDiff(context.Background(), "test", "repo", 42)
	if err != nil {
		t.Fatalf("获取 diff 失败: %v", err)
	}

	if diff != expectedDiff {
		t.Errorf("diff 内容不匹配:\nexpected:\n%s\nactual:\n%s", expectedDiff, diff)
	}
}

func TestCreatePR(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST 方法，actual %s", r.Method)
		}

		var req CreatePRRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := fmt.Sprintf(`{
			"number": 10,
			"title": "%s",
			"state": "open",
			"html_url": "https://github.com/test/repo/pull/10",
			"user": {"login": "tester"},
			"head": {"ref": "%s", "user": {"login": "tester"}},
			"base": {"ref": "%s"}
		}`, req.Title, req.Head, req.Base)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(resp))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	req := CreatePRRequest{
		Title: "Test PR",
		Body:  "Test body",
		Head:  "feature-branch",
		Base:  "main",
	}

	prInfo, err := client.CreatePR(context.Background(), "test", "repo", req)
	if err != nil {
		t.Fatalf("创建 PR 失败: %v", err)
	}

	if prInfo.Number != 10 {
		t.Errorf("expected PR Number=10，actual %d", prInfo.Number)
	}
	if prInfo.Title != "Test PR" {
		t.Errorf("expected Title=Test PR，actual %s", prInfo.Title)
	}
	if prInfo.HeadRef != "feature-branch" {
		t.Errorf("expected HeadRef=feature-branch，actual %s", prInfo.HeadRef)
	}
	if prInfo.BaseRef != "main" {
		t.Errorf("expected BaseRef=main，actual %s", prInfo.BaseRef)
	}
}

func TestMergePR(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT 方法，actual %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"merged":  true,
			"message": "Pull Request successfully merged",
		})
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	err := client.MergePR(context.Background(), "test", "repo", 5)
	if err != nil {
		t.Fatalf("合并 PR 失败: %v", err)
	}
}

func TestCreateComment(t *testing.T) {
	var receivedBody string
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST 方法，actual %s", r.Method)
		}

		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		receivedBody = req["body"].(string)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         12345,
			"body":       receivedBody,
			"user":       map[string]string{"login": "bot"},
			"created_at": time.Now().Format(time.RFC3339),
		})
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	commentBody := "## Review\n\nGood job!"
	err := client.CreateComment(context.Background(), "test", "repo", 7, commentBody)
	if err != nil {
		t.Fatalf("发布评论失败: %v", err)
	}

	if receivedBody != commentBody {
		t.Errorf("评论内容不匹配: expected %s，actual %s", commentBody, receivedBody)
	}
}

func TestGetRateLimit(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		resp := fmt.Sprintf(`{
			"resources": {
				"core": {
					"limit": 5000,
					"remaining": 4998,
					"reset": %d
				}
			}
		}`, time.Now().Add(1*time.Hour).Unix())
		w.Write([]byte(resp))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	rl, err := client.GetRateLimit(context.Background())
	if err != nil {
		t.Fatalf("获取速率限制失败: %v", err)
	}

	if rl.Limit != 5000 {
		t.Errorf("expected Limit=5000，actual %d", rl.Limit)
	}
	if rl.Remaining != 4998 {
		t.Errorf("expected Remaining=4998，actual %d", rl.Remaining)
	}
	if rl.ResetAt.IsZero() {
		t.Error("ResetAt 不should be zero value")
	}
}

func TestConvertPRResponse(t *testing.T) {
	jsonStr := `{
		"number": 99,
		"title": "Test PR",
		"state": "open",
		"html_url": "https://example.com/pr/99",
		"user": {"login": "user1"},
		"head": {"ref": "branch-a", "user": {"login": "user1"}},
		"base": {"ref": "main"}
	}`
	var pr ghPRResponse
	json.Unmarshal([]byte(jsonStr), &pr)

	info := convertPRResponse(pr)

	if info.Number != 99 {
		t.Errorf("Number 不匹配")
	}
	if info.User != "user1" {
		t.Errorf("User 不匹配: %s", info.User)
	}
	if info.HeadRef != "branch-a" {
		t.Errorf("HeadRef 不匹配: %s", info.HeadRef)
	}
}

// ---- Reviewer 测试 ----

func TestBuildReviewPrompt_Quick(t *testing.T) {
	diff := "+func hello() { println() }"
	prompt := buildReviewPrompt(diff, true)

	if !strings.Contains(prompt, "Quick review mode") {
		t.Error("快速模式提示词should contain'Quick review mode'")
	}
	if strings.Contains(prompt, "Deep review") {
		t.Error("快速模式should not contain深度审查内容")
	}
	if !strings.Contains(prompt, diff) {
		t.Error("提示词should contain diff 内容")
	}
}

func TestBuildReviewPrompt_Deep(t *testing.T) {
	diff := "-old\n+new"
	prompt := buildReviewPrompt(diff, false)

	if !strings.Contains(prompt, "Deep review mode") {
		t.Error("深度模式提示词should contain'Deep review mode'")
	}
	if !strings.Contains(prompt, "Code quality and readability") {
		t.Error("深度模式should contain代码质量检查项")
	}
	if !strings.Contains(prompt, diff) {
		t.Error("提示词should contain diff 内容")
	}
}

func TestFormatReviewComment(t *testing.T) {
	review := "This is a review comment."
	comment := formatReviewComment(review)

	if !strings.HasPrefix(comment, "## 🤖 AI Code Review") {
		t.Errorf("评论应以标题开头，actual: %s", comment[:20])
	}
	if !strings.Contains(comment, review) {
		t.Error("评论should contain审查内容")
	}
	if !strings.HasSuffix(comment, "*This comment was auto-generated by Whale Code AI*\n") {
		t.Error("评论应以署名结尾")
	}
}

func TestReviewer_ReviewPR(t *testing.T) {
	mockAI := &MockAIClient{Response: "代码质量良好，无重大问题。"}

	handler := func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "v3.diff") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"permissions": map[string]bool{"admin": true, "push": true},
		})
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	reviewer := NewReviewer(client, mockAI)
	comment, err := reviewer.ReviewPR(context.Background(), "test", "repo", 1, false)
	if err != nil {
		t.Fatalf("审查 PR 失败: %v", err)
	}

	if !strings.Contains(comment, "代码质量良好") {
		t.Errorf("评论should contain AI 审查结果: %s", comment)
	}
	if !strings.Contains(comment, "🤖 AI Code Review") {
		t.Error("评论应有标准格式标题")
	}
}

func TestReviewer_ReviewPR_EmptyDiff(t *testing.T) {
	mockAI := &MockAIClient{Response: "review"}

	handler := func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "v3.diff") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("")) // 空 diff
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"permissions": map[string]bool{"admin": true, "push": true},
		})
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	reviewer := NewReviewer(client, mockAI)
	_, err := reviewer.ReviewPR(context.Background(), "test", "repo", 1, false)
	if err == nil {
		t.Fatal("空 diff should return error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("错误消息不匹配: %v", err)
	}
}

// ---- PRCreator 测试 ----

func TestDetectCurrentBranch(t *testing.T) {
	// 非目录路径should return error
	_, err := detectCurrentBranch("C:\\nonexistent\\path\\12345")
	if err == nil {
		t.Error("无效路径should return error")
	}
}

func TestGenerateCommitMessage(t *testing.T) {
	mockAI := &MockAIClient{Response: "fix: correct login validation logic"}

	msg, err := generateCommitMessage(context.Background(), mockAI, "+func fix() {}")
	if err != nil {
		t.Fatalf("生成 commit message 失败: %v", err)
	}

	if msg != "fix: correct login validation logic" {
		t.Errorf("commit message 不匹配: %s", msg)
	}
}

func TestGeneratePRTitle(t *testing.T) {
	mockAI := &MockAIClient{Response: "Fix login validation error"}

	title, err := generatePRTitle(context.Background(), mockAI, "fix: correct login validation logic")
	if err != nil {
		t.Fatalf("生成 PR 标题失败: %v", err)
	}

	if title != "Fix login validation error" {
		t.Errorf("PR 标题不匹配: %s", title)
	}
}

func TestGeneratePRBody(t *testing.T) {
	mockAI := &MockAIClient{Response: "## Summary\nFixed the login bug."}

	body, err := generatePRBody(context.Background(), mockAI, "+fix code", "Fix bug")
	if err != nil {
		t.Fatalf("生成 PR 描述失败: %v", err)
	}

	if body != "## Summary\nFixed the login bug." {
		t.Errorf("PR 描述不匹配: %s", body)
	}
}

// ---- Tools Handler 测试 ----

func TestCheckToken_EmptyToken(t *testing.T) {
	client := NewClient("")
	gt := &GitHubTools{client: client, workDir: "."}

	err := gt.checkToken()
	if err == nil {
		t.Error("空 token should return error")
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("错误消息应提及 GITHUB_TOKEN: %v", err)
	}
}

func TestCheckToken_HasToken(t *testing.T) {
	client := NewClient("valid-token")
	gt := &GitHubTools{client: client, workDir: "."}

	err := gt.checkToken()
	if err != nil {
		t.Errorf("有效 token 不should return error: %v", err)
	}
}

func TestResolveOwnerRepo_FromParams(t *testing.T) {
	client := NewClient("token")
	gt := &GitHubTools{client: client, workDir: "."}

	owner, repo, err := gt.resolveOwnerRepo("myorg", "myrepo")
	if err != nil {
		t.Fatalf("解析仓库失败: %v", err)
	}
	if owner != "myorg" || repo != "myrepo" {
		t.Errorf("参数值未被使用: %s/%s", owner, repo)
	}
}

// ---- AIAdapter 测试 ----

// mockStreamClient mock ai.Client 用于测试 AIAdapter
type mockStreamClient struct {
	events chan ai.Event
}

func (m *mockStreamClient) Stream(_ context.Context, _ ai.Request) (<-chan ai.Event, error) {
	return m.events, nil
}
func (m *mockStreamClient) Model() string     { return "test-model" }
func (m *mockStreamClient) SetModel(_ string) {}
func (m *mockStreamClient) Close() error      { return nil }

func TestAIAdapter_Stream(t *testing.T) {
	// 创建一个简单的 mock 来测试适配器转换逻辑
	mockEvents := make(chan ai.Event, 3)
	mockEvents <- ai.TextDeltaEvent{Content: "hello "}
	mockEvents <- ai.TextDeltaEvent{Content: "world"}
	mockEvents <- ai.MessageStopEvent{}
	close(mockEvents)

	mockClient := &mockStreamClient{events: mockEvents}
	adapter := NewAIAdapter(mockClient)

	events, err := adapter.Stream(context.Background(), AIRequest{})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}

	var result strings.Builder
	for e := range events {
		switch ev := e.(type) {
		case AITextDeltaEvent:
			result.WriteString(ev.Content)
		case AIMessageStopEvent:
			// 正常结束
		default:
			t.Errorf("意外的事件类型: %T", e)
		}
	}

	output := result.String()
	if output != "hello world" {
		t.Errorf("输出不匹配: expected 'hello world'，actual '%s'", output)
	}
}

// ---- 边界情况测试 ----

func TestBuildReviewPrompt_LargeDiff(t *testing.T) {
	largeDiff := strings.Repeat("+line of code\n", 10000)
	prompt := buildReviewPrompt(largeDiff, false)

	if len(prompt) < len(largeDiff) {
		t.Error("大型 diff 应完整包含在提示词中")
	}
}

func TestFormatReviewComment_SpecialChars(t *testing.T) {
	review := "Code has <script>alert('xss')</script> issue"
	comment := formatReviewComment(review)

	if !strings.Contains(comment, review) {
		t.Error("特殊字符内容应保留在评论中")
	}
}

func TestListPRs_AllState(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("state")
		if query != "all" {
			t.Errorf("expected state=all，actual %s", query)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	prs, err := client.ListPRs(context.Background(), "test", "repo", "all")
	if err != nil {
		t.Fatalf("列出 PR 失败: %v", err)
	}
	if len(prs) != 0 {
		t.Errorf("expected 0 个 PR，actual %d", len(prs))
	}
}

// ===== M19-04 改造新增测试：CreateIssueWithLabels + SetBaseURL + SetHTTPClient =====

// TestCreateIssueWithLabels 验证：带 labels 的 Issue 创建走通，labels 字段被正确序列化到请求体。
func TestCreateIssueWithLabels(t *testing.T) {
	var capturedBody map[string]interface{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST，actual %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("认证头不正确: %s", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Errorf("解析请求体失败: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{
			"number": 42,
			"title": "[用户反馈] 测试",
			"state": "open",
			"html_url": "https://github.com/owner/repo/issues/42",
			"user": {"login": "tester"},
			"labels": [{"name": "user-feedback"}]
		}`))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(server.Client())

	issue, err := client.CreateIssueWithLabels(context.Background(), "owner", "repo", "[用户反馈] 测试", "body content", []string{"user-feedback"})
	if err != nil {
		t.Fatalf("CreateIssueWithLabels 失败: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("expected Number=42，actual %d", issue.Number)
	}
	if issue.HTMLURL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("expected HTMLURL，actual %s", issue.HTMLURL)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "user-feedback" {
		t.Errorf("expected Labels=[user-feedback]，actual %v", issue.Labels)
	}
	// 验证请求体含 labels 字段
	labelsRaw, ok := capturedBody["labels"]
	if !ok {
		t.Fatalf("请求体缺少 labels 字段, body: %v", capturedBody)
	}
	labels, ok := labelsRaw.([]interface{})
	if !ok {
		t.Fatalf("labels 字段不是数组, got: %T", labelsRaw)
	}
	if len(labels) != 1 || labels[0] != "user-feedback" {
		t.Errorf("expected labels=[user-feedback]，actual %v", labels)
	}
	// 验证 title 字段正确传递
	if capturedBody["title"] != "[用户反馈] 测试" {
		t.Errorf("title 不匹配, got: %v", capturedBody["title"])
	}
}

// TestCreateIssueWithLabels_EmptyLabels 验证：labels 为空时不传 labels 字段（等价于 CreateIssue）。
func TestCreateIssueWithLabels_EmptyLabels(t *testing.T) {
	var capturedBody map[string]interface{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number":1,"title":"t","state":"open","html_url":"u","user":{"login":"x"}}`))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(server.Client())

	_, err := client.CreateIssueWithLabels(context.Background(), "o", "r", "t", "b", nil)
	if err != nil {
		t.Fatalf("CreateIssueWithLabels 失败: %v", err)
	}
	if _, ok := capturedBody["labels"]; ok {
		t.Errorf("labels 为空时不应传 labels 字段, body: %v", capturedBody)
	}
}

// TestCreateIssueWithLabels_HTTPError 验证：非 201 状态码返回错误。
func TestCreateIssueWithLabels_HTTPError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(server.Client())

	_, err := client.CreateIssueWithLabels(context.Background(), "o", "r", "t", "b", []string{"user-feedback"})
	if err == nil {
		t.Fatal("非 201 状态码应返回错误")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("错误应含状态码 422, got: %v", err)
	}
}

// TestSetBaseURL 验证：SetBaseURL 覆盖默认 baseURL。
func TestSetBaseURL(t *testing.T) {
	client := NewClient("token")
	if client.baseURL != "https://api.github.com" {
		t.Errorf("默认 baseURL 应为 https://api.github.com，actual %s", client.baseURL)
	}
	client.SetBaseURL("http://mock.local")
	if client.baseURL != "http://mock.local" {
		t.Errorf("SetBaseURL 后 baseURL = %s，want http://mock.local", client.baseURL)
	}
}

// TestSetHTTPClient 验证：SetHTTPClient 覆盖默认 httpClient。
func TestSetHTTPClient(t *testing.T) {
	client := NewClient("token")
	original := client.httpClient
	custom := &http.Client{Timeout: 5 * time.Second}
	client.SetHTTPClient(custom)
	if client.httpClient != custom {
		t.Error("SetHTTPClient 后 httpClient 未替换")
	}
	if client.httpClient == original {
		t.Error("SetHTTPClient 应替换原 httpClient")
	}
	if client.httpClient.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v，want 5s", client.httpClient.Timeout)
	}
}

// benchmark：大量 PR 列表性能
func BenchmarkListPRs(b *testing.B) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		prs := make([]map[string]interface{}, 100)
		for i := range prs {
			prs[i] = map[string]interface{}{
				"number":   i + 1,
				"title":    fmt.Sprintf("PR #%d", i+1),
				"state":    "open",
				"html_url": fmt.Sprintf("https://github.com/test/repo/pull/%d", i+1),
				"user":     map[string]string{"login": "tester"},
				"head":     map[string]interface{}{"ref": "branch", "user": map[string]string{"login": "tester"}},
				"base":     map[string]interface{}{"ref": "main"},
			}
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(prs)
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.ListPRs(context.Background(), "test", "repo", "open")
	}
}
