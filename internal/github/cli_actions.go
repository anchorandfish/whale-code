package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// handleActionsListCmd handles /actions list command
func (g *GitHubTools) handleActionsListCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	branch := strings.TrimSpace(args)
	runs, err := g.client.ListWorkflowRuns(ctx, owner, repo, branch)
	if err != nil {
		return fmt.Sprintf("Failed to list Workflow runs: %v", err)
	}

	if len(runs) == 0 {
		return "No Workflow runs found"
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d Workflow runs:\n\n", len(runs)))
	for _, run := range runs {
		conclusion := run.Conclusion
		if conclusion == "" {
			conclusion = run.Status
		}
		builder.WriteString(fmt.Sprintf("- #%d %s [%s] branch:%s run:#%d %s\n  %s\n\n",
			run.ID, run.Name, conclusion, run.HeadBranch, run.RunNumber,
			run.CreatedAt.Format("2006-01-02 15:04"), run.HTMLURL))
	}
	return builder.String()
}

// handleActionsViewCmd handles /actions view command
func (g *GitHubTools) handleActionsViewCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /actions view <run_id>"
	}

	runID, err := strconv.ParseInt(strings.TrimSpace(args), 10, 64)
	if err != nil {
		return fmt.Sprintf("Invalid run ID: %s", args)
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	run, err := g.client.GetWorkflowRun(ctx, owner, repo, runID)
	if err != nil {
		return fmt.Sprintf("Failed to get run details: %v", err)
	}

	jobs, _ := g.client.GetWorkflowJobs(ctx, owner, repo, runID)

	var sb strings.Builder
	conclusion := run.Conclusion
	if conclusion == "" {
		conclusion = run.Status
	}
	sb.WriteString(fmt.Sprintf("Workflow run #%d\n\n", run.ID))
	sb.WriteString(fmt.Sprintf("Name: %s\n", run.Name))
	sb.WriteString(fmt.Sprintf("Status: %s\n", conclusion))
	sb.WriteString(fmt.Sprintf("Branch: %s\n", run.HeadBranch))
	sb.WriteString(fmt.Sprintf("Run number: #%d\n", run.RunNumber))
	sb.WriteString(fmt.Sprintf("Created: %s\n", run.CreatedAt.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("URL: %s\n", run.HTMLURL))

	if len(jobs) > 0 {
		sb.WriteString(fmt.Sprintf("\n%d Jobs:\n", len(jobs)))
		for _, job := range jobs {
			jobConclusion := job.Conclusion
			if jobConclusion == "" {
				jobConclusion = job.Status
			}
			sb.WriteString(fmt.Sprintf("  - %s [%s]", job.Name, jobConclusion))
			if job.RunnerName != "" {
				sb.WriteString(fmt.Sprintf(" runner: %s", job.RunnerName))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// handleActionsTriggerCmd handles /actions trigger command
func (g *GitHubTools) handleActionsTriggerCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return "Usage: /actions trigger <workflow_id> <ref>"
	}

	workflowID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return fmt.Sprintf("Invalid Workflow ID: %s", parts[0])
	}
	ref := parts[1]

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	if err := g.client.TriggerWorkflow(ctx, owner, repo, workflowID, ref, nil); err != nil {
		return fmt.Sprintf("Failed to trigger Workflow: %v", err)
	}

	return fmt.Sprintf("Workflow #%d triggered (ref: %s)", workflowID, ref)
}

// handleActionsRerunCmd handles /actions rerun command
func (g *GitHubTools) handleActionsRerunCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /actions rerun <run_id>"
	}

	runID, err := strconv.ParseInt(strings.TrimSpace(args), 10, 64)
	if err != nil {
		return fmt.Sprintf("Invalid run ID: %s", args)
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	if err := g.client.RerunWorkflow(ctx, owner, repo, runID); err != nil {
		return fmt.Sprintf("Failed to rerun Workflow: %v", err)
	}

	return fmt.Sprintf("Workflow run #%d re-triggered", runID)
}

// handleActionsLogsCmd handles /actions logs command
func (g *GitHubTools) handleActionsLogsCmd(ctx context.Context, args string) string {
	if err := g.checkToken(); err != nil {
		return err.Error()
	}
	if args == "" {
		return "Usage: /actions logs <run_id>"
	}

	runID, err := strconv.ParseInt(strings.TrimSpace(args), 10, 64)
	if err != nil {
		return fmt.Sprintf("Invalid run ID: %s", args)
	}

	owner, repo, err := g.resolveOwnerRepo("", "")
	if err != nil {
		return fmt.Sprintf("Failed to detect repository info: %v", err)
	}

	logs, err := g.client.GetWorkflowLogs(ctx, owner, repo, runID)
	if err != nil {
		return fmt.Sprintf("Failed to get logs: %v", err)
	}

	return fmt.Sprintf("Workflow run #%d logs:\n\n%s", runID, logs)
}
