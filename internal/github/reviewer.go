package github

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// AIEvent is the AI streaming event interface (decoupled from ai.Event)
type AIEvent interface {
	eventType() string
}

// AITextDeltaEvent is a text delta event
type AITextDeltaEvent struct {
	Content string
}

func (AITextDeltaEvent) eventType() string { return "text_delta" }

// AIErrorEvent is an error event
type AIErrorEvent struct {
	Err error
}

func (AIErrorEvent) eventType() string { return "error" }

// AIMessageStopEvent is a message stop event
type AIMessageStopEvent struct{}

func (AIMessageStopEvent) eventType() string { return "message_stop" }

// AIRequest is the AI request structure (decoupled from ai.Request)
//
// ★ 2026-10-04（A4）删除 EffortLevel 死字段：本包所有 AIRequest 构造点
// （reviewer.go / reviewer_multi.go / pr.go）都没有设置过它，值恒为 0，
// 唯一读者是 adapter.go 把它转发进 ai.Request（那边同批删除）。保留它会让
// 人误以为「GitHub 审查支持 reasoning effort」——实际从未发送。
type AIRequest struct {
	SystemPrompt []string    `json:"system_prompt"`
	Messages     []AIMessage `json:"messages"`
	MaxTokens    int         `json:"max_tokens"`
	Model        string      `json:"model"`
}

// AIMessage is an AI message entry
type AIMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// AIClient is the AI client interface (avoids direct dependency on ai.SimpleClient, convenient for test mocking)
type AIClient interface {
	// Stream streaming call — returns event channel
	Stream(ctx context.Context, req AIRequest) (<-chan AIEvent, error)
}

// Reviewer uses AI to review PRs
// - Get PR diff
// - Build AI review prompt
// - Call AI to generate review comments
// - Format as Markdown comment
type Reviewer struct {
	client   *Client
	aiClient AIClient
}

// NewReviewer creates a PR reviewer
func NewReviewer(client *Client, aiClient AIClient) *Reviewer {
	return &Reviewer{
		client:   client,
		aiClient: aiClient,
	}
}

// maxDiffSize is the maximum diff size allowed for review (100KB)
const maxDiffSize = 100 * 1024

// ReviewPR reviews the specified PR, returns formatted review comment
func (r *Reviewer) ReviewPR(ctx context.Context, owner, repo string, prNumber int, quick bool) (string, error) {
	if err := r.client.CheckWritePermission(ctx, owner, repo); err != nil {
		return "", fmt.Errorf("permission check failed: %w", err)
	}

	diff, err := r.client.GetPRDiff(ctx, owner, repo, prNumber)
	if err != nil {
		return "", fmt.Errorf("failed to get diff for PR #%d: %w", prNumber, err)
	}

	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("diff for PR #%d is empty, cannot review", prNumber)
	}

	if len(diff) > maxDiffSize {
		// 本批次保留 100KB 硬截断（DiffSplitter 属 P1，届时替代），并记录日志便于排查
		log.Printf("[reviewer] diff 截断: 原始 %d 字节，限制 %d 字节", len(diff), maxDiffSize)
		diff = diff[:maxDiffSize] + fmt.Sprintf("\n\n... (diff truncated: original size %d bytes, limit %d bytes)", len(diff), maxDiffSize)
	}

	prompt := buildReviewPrompt(diff, quick)

	review, err := r.callAIReview(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("AI review failed: %w", err)
	}

	comment := formatReviewComment(review)

	return comment, nil
}

// callAIReview calls AI to generate review comments and collects complete response
func (r *Reviewer) callAIReview(ctx context.Context, prompt string) (string, error) {
	req := AIRequest{
		SystemPrompt: []string{"You are a senior code review expert. Please perform a professional review of the following code changes."},
		Messages: []AIMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 4096,
	}

	events, err := r.aiClient.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI call failed: %w", err)
	}

	var builder strings.Builder
	for event := range events {
		switch e := event.(type) {
		case AITextDeltaEvent:
			builder.WriteString(e.Content)
		case AIErrorEvent:
			return "", fmt.Errorf("AI returned error: %w", e.Err)
		case AIMessageStopEvent:
			// normal end
		}
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("AI returned no content")
	}

	return result, nil
}

// buildReviewPrompt builds code review prompt
func buildReviewPrompt(diff string, quick bool) string {
	var promptBuilder strings.Builder

	promptBuilder.WriteString("Please review the code changes in the following Pull Request.\n\n")

	if quick {
		promptBuilder.WriteString("**Quick review mode**: Please provide concise summary review comments, including:\n")
		promptBuilder.WriteString("1. Change summary (1-2 sentences)\n")
		promptBuilder.WriteString("2. Main concerns or potential issues\n")
		promptBuilder.WriteString("3. Overall assessment and suggestions\n\n")
	} else {
		promptBuilder.WriteString("**Deep review mode**: Please review file by file in detail, including:\n")
		promptBuilder.WriteString("1. Code quality and readability\n")
		promptBuilder.WriteString("2. Potential bugs and edge cases\n")
		promptBuilder.WriteString("3. Performance and security considerations\n")
		promptBuilder.WriteString("4. Best practices and improvement suggestions\n")
		promptBuilder.WriteString("5. Test coverage suggestions\n\n")
	}

	promptBuilder.WriteString("---\n\n**Code Changes Diff:**\n```diff\n")
	promptBuilder.WriteString(diff)
	promptBuilder.WriteString("\n```\n")

	return promptBuilder.String()
}

// formatReviewComment formats AI review result as GitHub PR comment
func formatReviewComment(review string) string {
	var builder strings.Builder

	builder.WriteString("## 🤖 AI Code Review\n\n")
	builder.WriteString(review)
	builder.WriteString("\n\n---\n")
	builder.WriteString("*This comment was auto-generated by Whale Code AI*\n")

	return builder.String()
}
