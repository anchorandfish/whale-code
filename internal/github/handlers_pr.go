package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// createPRHandler creates PR tool handler
func (g *GitHubTools) createPRHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			BaseBranch string `json:"base_branch"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}

		prInfo, err := g.creator.CreatePR(ctx, input.BaseBranch)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("PR created successfully: #%d %s\n%s", prInfo.Number, prInfo.Title, prInfo.HTMLURL), nil
	}
}

// reviewPRHandler reviews PR tool handler
func (g *GitHubTools) reviewPRHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			PRNumber int    `json:"pr_number"`
			Quick    bool   `json:"quick"`
			Owner    string `json:"owner"`
			Repo     string `json:"repo"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.PRNumber <= 0 {
			return "", fmt.Errorf("PR number must be a positive integer")
		}

		owner, repo, err := g.resolveOwnerRepo(input.Owner, input.Repo)
		if err != nil {
			return "", fmt.Errorf("failed to detect repo info: %w", err)
		}

		comment, err := g.reviewer.ReviewPR(ctx, owner, repo, input.PRNumber, input.Quick)
		if err != nil {
			return "", err
		}

		// Publish review comment
		if err := g.client.CreateComment(ctx, owner, repo, input.PRNumber, comment); err != nil {
			// Comment publish failure does not affect returning review result
			return fmt.Sprintf("Review completed but comment publish failed: %v\n\n%s", err, comment), nil
		}

		return fmt.Sprintf("PR #%d review completed and comment published\n\n%s", input.PRNumber, comment), nil
	}
}

// listPRsHandler lists PR tool handler
func (g *GitHubTools) listPRsHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			State string `json:"state"`
			Owner string `json:"owner"`
			Repo  string `json:"repo"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}

		owner, repo, err := g.resolveOwnerRepo(input.Owner, input.Repo)
		if err != nil {
			return "", fmt.Errorf("failed to detect repo info: %w", err)
		}

		prs, err := g.client.ListPRs(ctx, owner, repo, input.State)
		if err != nil {
			return "", err
		}

		if len(prs) == 0 {
			return "No matching Pull Requests found", nil
		}

		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("Found %d Pull Requests:\n\n", len(prs)))
		for _, pr := range prs {
			builder.WriteString(fmt.Sprintf("- #%d %s [%s] by %s (%s → %s)\n  %s\n\n",
				pr.Number, pr.Title, pr.State, pr.User, pr.HeadRef, pr.BaseRef, pr.HTMLURL))
		}

		return builder.String(), nil
	}
}

// getPRDiffHandler gets PR diff tool handler
func (g *GitHubTools) getPRDiffHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			PRNumber int    `json:"pr_number"`
			Owner    string `json:"owner"`
			Repo     string `json:"repo"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.PRNumber <= 0 {
			return "", fmt.Errorf("PR number must be a positive integer")
		}

		owner, repo, err := g.resolveOwnerRepo(input.Owner, input.Repo)
		if err != nil {
			return "", fmt.Errorf("failed to detect repo info: %w", err)
		}

		diff, err := g.client.GetPRDiff(ctx, owner, repo, input.PRNumber)
		if err != nil {
			return "", err
		}

		return diff, nil
	}
}

// mergePRHandler merges PR tool handler
func (g *GitHubTools) mergePRHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			PRNumber int    `json:"pr_number"`
			Owner    string `json:"owner"`
			Repo     string `json:"repo"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.PRNumber <= 0 {
			return "", fmt.Errorf("PR number must be a positive integer")
		}

		owner, repo, err := g.resolveOwnerRepo(input.Owner, input.Repo)
		if err != nil {
			return "", fmt.Errorf("failed to detect repo info: %w", err)
		}

		if err := g.client.MergePR(ctx, owner, repo, input.PRNumber); err != nil {
			return "", err
		}

		return fmt.Sprintf("PR #%d merged successfully", input.PRNumber), nil
	}
}
