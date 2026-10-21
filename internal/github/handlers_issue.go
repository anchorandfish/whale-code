package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// listIssuesHandler lists Issue tool handler
func (g *GitHubTools) listIssuesHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner string `json:"owner"`
			Repo  string `json:"repo"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}

		issues, err := g.client.ListIssues(ctx, input.Owner, input.Repo, input.State)
		if err != nil {
			return "", err
		}

		if len(issues) == 0 {
			return "No matching Issues found", nil
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

		return builder.String(), nil
	}
}

// createIssueHandler creates Issue tool handler
func (g *GitHubTools) createIssueHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner string `json:"owner"`
			Repo  string `json:"repo"`
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.Title == "" {
			return "", fmt.Errorf("Issue title cannot be empty")
		}

		issueInfo, err := g.client.CreateIssue(ctx, input.Owner, input.Repo, input.Title, input.Body)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("Issue created successfully: #%d %s\n%s", issueInfo.Number, issueInfo.Title, issueInfo.HTMLURL), nil
	}
}

// getIssueHandler gets Issue details tool handler
func (g *GitHubTools) getIssueHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner       string `json:"owner"`
			Repo        string `json:"repo"`
			IssueNumber int    `json:"issue_number"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.IssueNumber <= 0 {
			return "", fmt.Errorf("Issue number must be a positive integer")
		}

		issue, err := g.client.GetIssue(ctx, input.Owner, input.Repo, input.IssueNumber)
		if err != nil {
			return "", err
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
			issue.Number, issue.Title, issue.State, issue.User, labels, body, issue.HTMLURL), nil
	}
}
