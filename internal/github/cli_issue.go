package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// handleIssueCreateCmd handles /issue create command
func (g *GitHubTools) handleIssueCreateCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /issue create <title> [body]"
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	parts := strings.SplitN(args, " ", 2)
	title := parts[0]
	body := ""
	if len(parts) > 1 {
		body = parts[1]
	}

	issueInfo, err := g.client.CreateIssue(ctx, owner, repo, title, body)
	if err != nil {
		return fmt.Sprintf("Failed to create Issue: %v", err)
	}

	return fmt.Sprintf("Issue created successfully: #%d %s\n%s", issueInfo.Number, issueInfo.Title, issueInfo.HTMLURL)
}

// handleIssueListCmd handles /issue list command
func (g *GitHubTools) handleIssueListCmd(ctx context.Context, args string) string {
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

	issues, err := g.client.ListIssues(ctx, owner, repo, state)
	if err != nil {
		return fmt.Sprintf("Failed to list Issues: %v", err)
	}

	if len(issues) == 0 {
		return "No matching Issues found"
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d Issues:\n\n", len(issues)))
	for _, issue := range issues {
		labels := strings.Join(issue.Labels, ", ")
		if labels != "" {
			labels = " [" + labels + "]"
		}
		builder.WriteString(fmt.Sprintf("- #%d %s [%s]%s by %s\n  %s\n\n",
			issue.Number, issue.Title, issue.State, labels, issue.User, issue.HTMLURL))
	}
	return builder.String()
}

// handleIssueViewCmd handles /issue view command
func (g *GitHubTools) handleIssueViewCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /issue view <number>"
	}

	issueNumber, err := strconv.Atoi(strings.TrimSpace(args))
	if err != nil {
		return fmt.Sprintf("Invalid Issue number: %s", args)
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	issue, err := g.client.GetIssue(ctx, owner, repo, issueNumber)
	if err != nil {
		return fmt.Sprintf("Failed to get Issue: %v", err)
	}

	labels := strings.Join(issue.Labels, ", ")
	if labels != "" {
		labels = "\nLabels: " + labels
	}
	body := issue.Body
	if body == "" {
		body = "(No body)"
	}

	return fmt.Sprintf("Issue #%d: %s\nState: %s\nAuthor: %s%s\n\n%s\n\n%s",
		issue.Number, issue.Title, issue.State, issue.User, labels, body, issue.HTMLURL)
}
