package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ============================================================
// inline 行级评论（P0-2）
//
// 使用 GitHub Reviews API（POST /repos/{owner}/{repo}/pulls/{pr_number}/reviews）
// 发布行级评论，而非 Issues comments API（/issues/{n}/comments）。
// 评论格式含严重度 emoji（🔴🟡🟢），精确到代码行。
// ============================================================

// ReviewEvent Review 事件类型。
// 对应 GitHub Reviews API 的 event 字段。
type ReviewEvent string

const (
	// ReviewEventComment 仅评论（不 approve / 不 request changes）。
	ReviewEventComment ReviewEvent = "COMMENT"
	// ReviewEventApprove 批准 PR。
	ReviewEventApprove ReviewEvent = "APPROVE"
	// ReviewEventRequestChanges 请求修改（阻塞合并）。
	ReviewEventRequestChanges ReviewEvent = "REQUEST_CHANGES"
)

// ReviewRequest GitHub Review 请求。
// 通过 Reviews API 一次性提交总结评论 + N 条行级评论。
type ReviewRequest struct {
	Event    ReviewEvent     `json:"event"`    // COMMENT / APPROVE / REQUEST_CHANGES
	Body     string          `json:"body"`     // 总结评论（PR 顶级评论）
	Comments []ReviewComment `json:"comments"` // 行级评论
}

// ReviewComment 行级评论。对应 Reviews API comments 数组元素。
// 行级评论通过 subject_type=line + line 定位到具体代码行。
type ReviewComment struct {
	Path        string `json:"path"`                   // 文件路径（必填）
	SubjectType string `json:"subject_type,omitempty"` // "line"（默认）/ "file"
	Line        int    `json:"line,omitempty"`         // 行号（subject_type=line 时必填）
	Side        string `json:"side,omitempty"`         // "RIGHT"（默认，新增侧）/ "LEFT"
	StartLine   int    `json:"start_line,omitempty"`   // 多行评论起始行（可选）
	StartSide   string `json:"start_side,omitempty"`   // 多行评论起始侧（可选）
	Body        string `json:"body"`                   // 评论内容（含严重度 emoji）
}

// PostReview 通过 Reviews API 发布 PR Review（含行级评论）。
// 端点：POST /repos/{owner}/{repo}/pulls/{pr_number}/reviews
// 这是发布 inline 行级评论的正确 API，区别于 Issues comments API。
// 成功返回 HTTP 200。
func (c *Client) PostReview(ctx context.Context, owner, repo string, prNumber int, review ReviewRequest) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	resp, err := c.doRequest(ctx, http.MethodPost, path, review)
	if err != nil {
		return fmt.Errorf("failed to post review: %w", err)
	}
	defer resp.Body.Close()

	// Reviews API 成功返回 200（非 201）
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to post review (HTTP %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// PostMultiReviewResult 将多 Agent 审查结果作为 Review 发布到 PR。
// 总结评论作为 Review body，每条 Finding（有 file+line）作为行级评论。
// 仅评论，不 approve / 不 request changes（保留人工决策）。
func (c *Client) PostMultiReviewResult(ctx context.Context, owner, repo string, prNumber int, result *ReviewResult) error {
	req := ReviewRequest{
		Event:    ReviewEventComment,
		Body:     result.Summary,
		Comments: FindingsToReviewComments(result.Findings),
	}
	return c.PostReview(ctx, owner, repo, prNumber, req)
}

// FindingsToReviewComments 将审查发现批量转换为行级评论。
// 仅保留有文件路径与有效行号的发现（无行号的发现无法定位到行，
// 已包含在总结评论的表格中）。
func FindingsToReviewComments(findings []Finding) []ReviewComment {
	comments := make([]ReviewComment, 0, len(findings))
	for _, f := range findings {
		if f.File == "" || f.Line <= 0 {
			continue
		}
		comments = append(comments, ReviewComment{
			Path:        f.File,
			SubjectType: "line",
			Line:        f.Line,
			Side:        "RIGHT",
			Body:        FormatFindingComment(f),
		})
	}
	return comments
}

// FormatFindingComment 将单个 Finding 格式化为 inline 评论正文。
// 格式含严重度 emoji（🔴🟡🟢）、严重度标签、来源 Agent、标题、说明、修复建议。
// 对标 01 文档 §6.1 P0-2 的评论格式规范。
func FormatFindingComment(f Finding) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s **%s** (%s)\n\n", f.Severity.Emoji(), severityLabel(f.Severity), f.Agent))
	if f.Title != "" {
		b.WriteString(f.Title)
		b.WriteString("\n\n")
	}
	if f.Body != "" {
		b.WriteString(f.Body)
		b.WriteString("\n\n")
	}
	if f.Suggestion != "" {
		b.WriteString("**建议修复**：\n```\n")
		b.WriteString(f.Suggestion)
		b.WriteString("\n```\n")
	}
	if f.Verified {
		b.WriteString("\n✅ 已通过交叉验证\n")
	}
	return b.String()
}

// severityLabel 返回严重度的展示标签（首字母大写）。
// blocker → Blocker，warning → Warning，suggestion → Suggestion。
func severityLabel(s Severity) string {
	switch s {
	case SeverityBlocker:
		return "Blocker"
	case SeverityWarning:
		return "Warning"
	case SeveritySuggestion:
		return "Suggestion"
	}
	if s == "" {
		return "Unknown"
	}
	return strings.ToUpper(string(s[:1])) + string(s[1:])
}

// postReviewRequestPayload 用于测试：返回 PostReview 将发送的 JSON 体。
// 仅测试用途，便于断言 event/body/comments 字段。
func postReviewRequestPayload(review ReviewRequest) string {
	data, _ := json.Marshal(review)
	return string(data)
}
