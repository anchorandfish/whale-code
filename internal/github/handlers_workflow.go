package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// listWorkflowsHandler lists Workflows tool handler
func (g *GitHubTools) listWorkflowsHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner string `json:"owner"`
			Repo  string `json:"repo"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}

		workflows, err := g.client.ListWorkflows(ctx, input.Owner, input.Repo)
		if err != nil {
			return "", err
		}

		if len(workflows) == 0 {
			return "No Workflows found", nil
		}

		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("Found %d Workflows:\n\n", len(workflows)))
		for _, wf := range workflows {
			builder.WriteString(fmt.Sprintf("- #%d %s [%s] (%s)\n", wf.ID, wf.Name, wf.State, wf.Path))
		}

		return builder.String(), nil
	}
}

// listWorkflowRunsHandler lists Workflow runs tool handler
func (g *GitHubTools) listWorkflowRunsHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner  string `json:"owner"`
			Repo   string `json:"repo"`
			Branch string `json:"branch"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}

		runs, err := g.client.ListWorkflowRuns(ctx, input.Owner, input.Repo, input.Branch)
		if err != nil {
			return "", err
		}

		if len(runs) == 0 {
			return "No Workflow runs found", nil
		}

		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("Found %d Workflow runs:\n\n", len(runs)))
		for _, run := range runs {
			conclusion := run.Conclusion
			if conclusion == "" {
				conclusion = run.Status
			}
			builder.WriteString(fmt.Sprintf("- #%d %s [%s] branch:%s run:#%d %s\n  %s\n\n",
				run.ID, run.Name, conclusion, run.HeadBranch, run.RunNumber, run.CreatedAt.Format("2006-01-02 15:04"), run.HTMLURL))
		}

		return builder.String(), nil
	}
}

// triggerWorkflowHandler triggers Workflow tool handler
func (g *GitHubTools) triggerWorkflowHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner      string            `json:"owner"`
			Repo       string            `json:"repo"`
			WorkflowID int64             `json:"workflow_id"`
			Ref        string            `json:"ref"`
			Inputs     map[string]string `json:"inputs"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.WorkflowID <= 0 {
			return "", fmt.Errorf("Workflow ID must be a positive integer")
		}
		if input.Ref == "" {
			return "", fmt.Errorf("Git ref cannot be empty")
		}

		if err := g.client.TriggerWorkflow(ctx, input.Owner, input.Repo, input.WorkflowID, input.Ref, input.Inputs); err != nil {
			return "", err
		}

		return fmt.Sprintf("Workflow #%d triggered (ref: %s)", input.WorkflowID, input.Ref), nil
	}
}

// rerunWorkflowHandler reruns Workflow tool handler
func (g *GitHubTools) rerunWorkflowHandler() tools.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := g.checkToken(); err != nil {
			return "", err
		}

		var input struct {
			Owner string `json:"owner"`
			Repo  string `json:"repo"`
			RunID int64  `json:"run_id"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return "", fmt.Errorf("failed to parse input: %w", err)
		}
		if input.RunID <= 0 {
			return "", fmt.Errorf("Run ID must be a positive integer")
		}

		if err := g.client.RerunWorkflow(ctx, input.Owner, input.Repo, input.RunID); err != nil {
			return "", err
		}

		return fmt.Sprintf("Workflow run #%d re-triggered", input.RunID), nil
	}
}
