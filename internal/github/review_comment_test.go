package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPostReview_VerifiesReviewsAPI 验证 PostReview 命中 Reviews API 端点
// （/pulls/{n}/reviews，而非 /issues/{n}/comments），且请求体字段正确。
func TestPostReview_VerifiesReviewsAPI(t *testing.T) {
	var (
		gotPath string
		gotBody ReviewRequest
	)
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("请求方法应为 POST，实际 %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":1}`))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	review := ReviewRequest{
		Event: ReviewEventComment,
		Body:  "总结评论",
		Comments: []ReviewComment{
			{Path: "a.go", SubjectType: "line", Line: 42, Side: "RIGHT", Body: "🔴 Blocker"},
		},
	}
	if err := client.PostReview(context.Background(), "owner", "repo", 7, review); err != nil {
		t.Fatalf("PostReview 失败: %v", err)
	}

	// 关键约束：必须命中 Reviews API，而非 Issues comments API
	if !strings.HasSuffix(gotPath, "/repos/owner/repo/pulls/7/reviews") {
		t.Errorf("应命中 Reviews API /pulls/7/reviews，实际 %s", gotPath)
	}
	if strings.Contains(gotPath, "/issues/") {
		t.Errorf("不应命中 Issues comments API: %s", gotPath)
	}
	if gotBody.Event != "COMMENT" {
		t.Errorf("event 字段应为 COMMENT，实际 %s", gotBody.Event)
	}
	if gotBody.Body != "总结评论" {
		t.Errorf("body 不匹配: %s", gotBody.Body)
	}
	if len(gotBody.Comments) != 1 || gotBody.Comments[0].Path != "a.go" || gotBody.Comments[0].Line != 42 {
		t.Errorf("comments 不匹配: %+v", gotBody.Comments)
	}
}

// TestPostReview_Non200ReturnsError 验证非 200 响应返回错误。
func TestPostReview_Non200ReturnsError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"validation failed"}`))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	err := client.PostReview(context.Background(), "o", "r", 1, ReviewRequest{Event: ReviewEventComment})
	if err == nil {
		t.Fatalf("非 200 应返回错误")
	}
	if !strings.Contains(err.Error(), "HTTP 422") {
		t.Errorf("错误应含 HTTP 422，实际 %v", err)
	}
}

// TestFindingsToReviewComments 验证发现到行级评论的转换与过滤。
func TestFindingsToReviewComments(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityBlocker, File: "a.go", Line: 10, Title: "T1", Agent: "logic-bug"},
		{Severity: SeverityWarning, File: "b.go", Line: 0, Title: "无行号"}, // 应被过滤
		{Severity: SeveritySuggestion, File: "", Line: 5, Title: "无文件"},  // 应被过滤
		{Severity: SeverityBlocker, File: "c.go", Line: 3, Title: "T2", Agent: "security"},
	}
	comments := FindingsToReviewComments(findings)
	if len(comments) != 2 {
		t.Fatalf("应保留 2 条行级评论，实际 %d", len(comments))
	}
	if comments[0].Path != "a.go" || comments[0].Line != 10 || comments[0].SubjectType != "line" || comments[0].Side != "RIGHT" {
		t.Errorf("第 1 条评论字段不匹配: %+v", comments[0])
	}
	if comments[1].Path != "c.go" || comments[1].Line != 3 {
		t.Errorf("第 2 条评论字段不匹配: %+v", comments[1])
	}
}

// TestFormatFindingComment 验证评论格式含严重度 emoji 与标签。
func TestFormatFindingComment(t *testing.T) {
	t.Run("blocker 含 emoji 与建议", func(t *testing.T) {
		f := Finding{
			Severity:   SeverityBlocker,
			Agent:      "logic-bug",
			Title:      "nil ptr",
			Body:       "deref nil",
			Suggestion: "add nil check",
			Verified:   true,
		}
		got := FormatFindingComment(f)
		if !strings.Contains(got, "🔴") {
			t.Errorf("应含 🔴 emoji: %s", got)
		}
		if !strings.Contains(got, "Blocker") {
			t.Errorf("应含 Blocker 标签: %s", got)
		}
		if !strings.Contains(got, "logic-bug") {
			t.Errorf("应含来源 Agent: %s", got)
		}
		if !strings.Contains(got, "nil ptr") {
			t.Errorf("应含标题: %s", got)
		}
		if !strings.Contains(got, "add nil check") {
			t.Errorf("应含建议: %s", got)
		}
		if !strings.Contains(got, "✅") {
			t.Errorf("已验证应含 ✅: %s", got)
		}
	})
	t.Run("warning 与 suggestion emoji 正确", func(t *testing.T) {
		w := FormatFindingComment(Finding{Severity: SeverityWarning, Title: "w", Body: "b"})
		if !strings.Contains(w, "🟡") || !strings.Contains(w, "Warning") {
			t.Errorf("warning 格式错误: %s", w)
		}
		s := FormatFindingComment(Finding{Severity: SeveritySuggestion, Title: "s", Body: "b"})
		if !strings.Contains(s, "🟢") || !strings.Contains(s, "Suggestion") {
			t.Errorf("suggestion 格式错误: %s", s)
		}
	})
	t.Run("无建议时不输出建议块", func(t *testing.T) {
		got := FormatFindingComment(Finding{Severity: SeverityBlocker, Title: "t", Body: "b"})
		if strings.Contains(got, "建议修复") {
			t.Errorf("无建议时不应输出建议块: %s", got)
		}
	})
}

// TestSeverityLabel 验证严重度标签首字母大写。
func TestSeverityLabel(t *testing.T) {
	cases := map[Severity]string{
		SeverityBlocker:    "Blocker",
		SeverityWarning:    "Warning",
		SeveritySuggestion: "Suggestion",
	}
	for sev, want := range cases {
		if got := severityLabel(sev); got != want {
			t.Errorf("severityLabel(%v) = %s, want %s", sev, got, want)
		}
	}
}

// TestPostMultiReviewResult 验证多 Agent 结果作为 Review 发布。
func TestPostMultiReviewResult(t *testing.T) {
	var gotBody ReviewRequest
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":1}`))
	}
	server := createTestServer(handler)
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	result := &ReviewResult{
		Summary: "## summary",
		Findings: []Finding{
			{Severity: SeverityBlocker, File: "a.go", Line: 5, Title: "T", Body: "B", Agent: "logic-bug"},
		},
	}
	if err := client.PostMultiReviewResult(context.Background(), "o", "r", 3, result); err != nil {
		t.Fatalf("PostMultiReviewResult 失败: %v", err)
	}
	if gotBody.Body != "## summary" {
		t.Errorf("body 应为 summary，实际 %s", gotBody.Body)
	}
	if gotBody.Event != "COMMENT" {
		t.Errorf("event 应为 COMMENT，实际 %s", gotBody.Event)
	}
	if len(gotBody.Comments) != 1 || gotBody.Comments[0].Line != 5 {
		t.Errorf("行级评论不匹配: %+v", gotBody.Comments)
	}
}
