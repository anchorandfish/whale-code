package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// handlePRCreateCmd handles /pr create command
func (g *GitHubTools) handlePRCreateCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if g.creator == nil {
		return "PR creator not initialized"
	}
	baseBranch := strings.TrimSpace(args)
	prInfo, err := g.creator.CreatePR(ctx, baseBranch)
	if err != nil {
		return fmt.Sprintf("Failed to create PR: %v", err)
	}
	return fmt.Sprintf("PR created successfully: #%d %s\n%s", prInfo.Number, prInfo.Title, prInfo.HTMLURL)
}

// handlePRReviewCmd handles /pr review command
// 支持三种模式：
//   - 默认：单 Agent 深度审查，发布为 Issues 评论
//   - --quick：单 Agent 快速审查
//   - --multi：多 Agent 并行审查，发布为 Reviews API 行级评论（inline）
func (g *GitHubTools) handlePRReviewCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /pr review <PR_number> [--quick|--multi]"
	}

	parts := strings.Fields(args)
	prNumStr := parts[0]
	quick := len(parts) > 1 && parts[1] == "--quick"
	multi := len(parts) > 1 && parts[1] == "--multi"

	prNumber, err := strconv.Atoi(prNumStr)
	if err != nil {
		return fmt.Sprintf("Invalid PR number: %s", prNumStr)
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	// 多 Agent 模式：使用 Reviews API 发布 inline 行级评论
	if multi {
		if g.multiReviewer == nil {
			return "Multi-agent reviewer not initialized"
		}
		result, err := g.multiReviewer.ReviewPR(ctx, owner, repo, prNumber)
		if err != nil {
			return fmt.Sprintf("Multi-agent review failed: %v", err)
		}
		if err := g.client.PostMultiReviewResult(ctx, owner, repo, prNumber, result); err != nil {
			return fmt.Sprintf("Multi-agent review completed but publish failed: %v\n\n%s", err, result.Summary)
		}
		return fmt.Sprintf("PR #%d multi-agent review completed (%d findings: 🔴%d 🟡%d 🟢%d), inline comments published\n\n%s",
			prNumber, result.Metrics.TotalFindings, result.Metrics.Blockers, result.Metrics.Warnings, result.Metrics.Suggestions, result.Summary)
	}

	if g.reviewer == nil {
		return "Reviewer not initialized"
	}

	comment, err := g.reviewer.ReviewPR(ctx, owner, repo, prNumber, quick)
	if err != nil {
		return fmt.Sprintf("Review failed: %v", err)
	}

	if err := g.client.CreateComment(ctx, owner, repo, prNumber, comment); err != nil {
		return fmt.Sprintf("Review completed but comment publish failed: %v\n\n%s", err, comment)
	}

	return fmt.Sprintf("PR #%d review completed and comment published\n\n%s", prNumber, comment)
}

// handlePRListCmd handles /pr list command
func (g *GitHubTools) handlePRListCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}

	state := "open"
	if args != "" {
		state = args
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	prs, err := g.client.ListPRs(ctx, owner, repo, state)
	if err != nil {
		return fmt.Sprintf("Failed to list PRs: %v", err)
	}

	if len(prs) == 0 {
		return "No matching Pull Requests found"
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d Pull Requests:\n\n", len(prs)))
	for _, pr := range prs {
		builder.WriteString(fmt.Sprintf("- #%d %s [%s] by %s (%s → %s)\n  %s\n\n",
			pr.Number, pr.Title, pr.State, pr.User, pr.HeadRef, pr.BaseRef, pr.HTMLURL))
	}
	return builder.String()
}

// handlePRMergeCmd handles /pr merge command
func (g *GitHubTools) handlePRMergeCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /pr merge <PR_number>"
	}

	prNumber, err := strconv.Atoi(strings.TrimSpace(args))
	if err != nil {
		return fmt.Sprintf("Invalid PR number: %s", args)
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	if err := g.client.MergePR(ctx, owner, repo, prNumber); err != nil {
		return fmt.Sprintf("Failed to merge PR: %v", err)
	}

	return fmt.Sprintf("PR #%d merged successfully", prNumber)
}

// handleStatus handles /github status command
func (g *GitHubTools) handleStatus() string {
	var sb strings.Builder
	sb.WriteString("GitHub connection status\n\n")

	if g.client.token == "" {
		sb.WriteString("GITHUB_TOKEN: not set\n")
	} else {
		sb.WriteString("GITHUB_TOKEN: set\n")
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		sb.WriteString(fmt.Sprintf("Repository: %v\n", err))
	} else {
		sb.WriteString(fmt.Sprintf("Repository: %s/%s\n", owner, repo))
	}

	return sb.String()
}
