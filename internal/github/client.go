// Package github — GitHub API client and integration features
// Provides PR management, code review, event polling and other GitHub workflow integration
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// escapePath 对 URL 路径段进行转义（B5-P1-2 修复）。
// 用于 owner/repo 等 path 参数，防止特殊字符构造路径注入/SSRF。
func escapePath(s string) string {
	return url.PathEscape(s)
}

// escapeQuery 对 URL 查询参数进行转义（B5-P1-2 修复）。
// 用于 state/branch 等 query 参数，防止特殊字符注入。
func escapeQuery(s string) string {
	return url.QueryEscape(s)
}

// githubAPIBase is the GitHub REST API v3 base URL
const githubAPIBase = "https://api.github.com"

// PRInfo is basic PR information
type PRInfo struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	User    string `json:"user"`
	HTMLURL string `json:"html_url"`
	HeadRef string `json:"head_ref"`
	BaseRef string `json:"base_ref"`
}

// CreatePRRequest is the create PR request parameters
type CreatePRRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

// RateLimitInfo is API rate limit information
type RateLimitInfo struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
}

// ghPRResponse is the GitHub API PR response structure
type ghPRResponse struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref  string `json:"ref"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// ghRateLimitResponse is the GitHub API rate limit response structure
type ghRateLimitResponse struct {
	Resources struct {
		Core struct {
			Limit     int   `json:"limit"`
			Remaining int   `json:"remaining"`
			Reset     int64 `json:"reset"`
		} `json:"core"`
	} `json:"resources"`
}

// ghErrorResponse is the GitHub API error response structure
type ghErrorResponse struct {
	Message string `json:"message"`
}

// Client is the GitHub API client
// - Authenticates using GITHUB_TOKEN environment variable
// - Calls via GitHub REST API v3
// - Auto-handles rate limiting
// - Supports getting repo info, PR list, PR diff, creating PR, posting comments
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a GitHub API client
func NewClient(token string) *Client {
	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: githubAPIBase,
	}
}

// DetectRepo detects repo owner and name from git remote (supports HTTPS and SSH formats)
func (c *Client) DetectRepo(workDir string) (owner, repo string, err error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to get git remote: %w", err)
	}

	remoteURL := strings.TrimSpace(string(output))

	pattern := regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?$`)
	matches := pattern.FindStringSubmatch(remoteURL)
	if len(matches) == 3 {
		return matches[1], matches[2], nil
	}

	return "", "", fmt.Errorf("cannot parse repo info from remote URL: %s", remoteURL)
}

// doRequest executes GitHub API request, auto-handles rate limiting and authentication
// B5-P1-5 修复：token 为空时 fail-closed 返回错误，不再发送 Authorization: Bearer 头。
// 原 MCP 工具调用路径（RegisterHandlers → 各 handler → Client 方法）未调用 checkToken，
// 导致空 token 时仍发送 Bearer 头，可能在日志中暴露请求意图。
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	// B5-P1-5 修复：token 为空时拒绝请求（fail-closed）
	if c.token == "" {
		return nil, fmt.Errorf("github client: token is empty (set GITHUB_TOKEN environment variable)")
	}
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Auto-retry: wait when rate limited
	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, err
	}

	// Check rate limit
	if resp.StatusCode == http.StatusForbidden {
		remaining := resp.Header.Get("X-RateLimit-Remaining")
		if remaining == "0" {
			resp.Body.Close()
			resetStr := resp.Header.Get("X-RateLimit-Reset")
			return nil, fmt.Errorf("API rate limit exhausted, reset time: %s", resetStr)
		}
	}

	return resp, nil
}

// doWithRetry executes HTTP request, supports rate limit retry
func (c *Client) doWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	var originalBody []byte
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			originalBody, _ = io.ReadAll(body)
			body.Close()
		}
	}
	if originalBody == nil && req.Body != nil {
		originalBody, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(originalBody))
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		var reqBody io.Reader
		if originalBody != nil {
			reqBody = bytes.NewReader(originalBody)
		}

		newReq := req.Clone(ctx)
		if reqBody != nil {
			newReq.Body = io.NopCloser(reqBody)
		}

		resp, err := c.httpClient.Do(newReq)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			resetStr := resp.Header.Get("X-RateLimit-Reset")
			if resetStr != "" {
				var resetUnix int64
				fmt.Sscanf(resetStr, "%d", &resetUnix)
				waitDuration := time.Until(time.Unix(resetUnix, 0))
				if waitDuration > 0 {
					// B5-P1-3: 移除 waitDuration < 5*time.Minute 上限，
					// 统一等待到 rate limit reset，避免长重置周期退化为快速重试。
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(waitDuration):
						continue
					}
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				continue
			}
		}

		return resp, nil
	}

	return nil, fmt.Errorf("request retry limit exhausted")
}

// ListPRs lists Pull Requests of the repository with pagination support
func (c *Client) ListPRs(ctx context.Context, owner, repo, state string) ([]PRInfo, error) {
	if state == "" {
		state = "open"
	}

	var allPRs []PRInfo
	page := 1
	perPage := 30

	for {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&per_page=%d&page=%d", escapePath(owner), escapePath(repo), escapeQuery(state), perPage, page)
		resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to list PRs: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("failed to list PRs (HTTP %d): %s", resp.StatusCode, string(body))
		}

		var prs []ghPRResponse
		if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to parse PR list: %w", err)
		}
		resp.Body.Close()

		for _, pr := range prs {
			allPRs = append(allPRs, convertPRResponse(pr))
		}

		if len(prs) < perPage {
			break
		}
		page++
	}

	return allPRs, nil
}

// GetPRDiff gets PR diff content
// B5-P1-2 修复：owner/repo 使用 url.PathEscape 转义防止路径注入。
// B5-P1-5 修复：token 为空时 fail-closed 返回错误。
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	if c.token == "" {
		return "", fmt.Errorf("github client: token is empty (set GITHUB_TOKEN environment variable)")
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", escapePath(owner), escapePath(repo), prNumber)

	diffURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, diffURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get PR diff: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get PR diff (HTTP %d): %s", resp.StatusCode, string(body))
	}

	diff, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read diff content: %w", err)
	}

	return string(diff), nil
}

// CreatePR creates a Pull Request
func (c *Client) CreatePR(ctx context.Context, owner, repo string, req CreatePRRequest) (*PRInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls", escapePath(owner), escapePath(repo))
	resp, err := c.doRequest(ctx, http.MethodPost, path, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create PR (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var prResp ghPRResponse
	if err := json.NewDecoder(resp.Body).Decode(&prResp); err != nil {
		return nil, fmt.Errorf("failed to parse PR response: %w", err)
	}

	result := convertPRResponse(prResp)
	return &result, nil
}

// MergePR merges a Pull Request
func (c *Client) MergePR(ctx context.Context, owner, repo string, prNumber int) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", escapePath(owner), escapePath(repo), prNumber)
	body := map[string]interface{}{
		"merge_method": "squash",
	}
	resp, err := c.doRequest(ctx, http.MethodPut, path, body)
	if err != nil {
		return fmt.Errorf("failed to merge PR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to merge PR (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// CreateComment posts a comment on a PR
func (c *Client) CreateComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", escapePath(owner), escapePath(repo), prNumber)
	reqBody := map[string]interface{}{
		"body": body,
	}
	resp, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return fmt.Errorf("failed to post comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to post comment (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetRateLimit gets API rate limit information
func (c *Client) GetRateLimit(ctx context.Context) (*RateLimitInfo, error) {
	path := "/rate_limit"
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get rate limit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get rate limit (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var rlResp ghRateLimitResponse
	if err := json.NewDecoder(resp.Body).Decode(&rlResp); err != nil {
		return nil, fmt.Errorf("failed to parse rate limit response: %w", err)
	}

	return &RateLimitInfo{
		Limit:     rlResp.Resources.Core.Limit,
		Remaining: rlResp.Resources.Core.Remaining,
		ResetAt:   time.Unix(rlResp.Resources.Core.Reset, 0),
	}, nil
}

// IssueInfo is basic Issue information
type IssueInfo struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	State   string   `json:"state"`
	User    string   `json:"user"`
	HTMLURL string   `json:"html_url"`
	Body    string   `json:"body"`
	Labels  []string `json:"labels"`
}

// ghIssueResponse is the GitHub API Issue response structure
type ghIssueResponse struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// ListIssues lists Issues of the repository
func (c *Client) ListIssues(ctx context.Context, owner, repo, state string) ([]IssueInfo, error) {
	if state == "" {
		state = "open"
	}
	path := fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=30", escapePath(owner), escapePath(repo), escapeQuery(state))
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list Issues: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list Issues (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var issues []ghIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, fmt.Errorf("failed to parse Issue list: %w", err)
	}

	result := make([]IssueInfo, len(issues))
	for i, issue := range issues {
		result[i] = convertIssueResponse(issue)
	}
	return result, nil
}

// CreateIssue creates an Issue
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string) (*IssueInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", escapePath(owner), escapePath(repo))
	reqBody := map[string]interface{}{
		"title": title,
		"body":  body,
	}
	resp, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create Issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create Issue (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var issueResp ghIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issueResp); err != nil {
		return nil, fmt.Errorf("failed to parse Issue response: %w", err)
	}

	result := convertIssueResponse(issueResp)
	return &result, nil
}

// CreateIssueWithLabels 创建 Issue 并附加 labels。
// M19-04 改造新增：/feedback 直创 GitHub Issue 需要打 user-feedback 标签。
// labels 为空切片时不传 labels 字段，等价于 CreateIssue。
// 仓库不存在对应 label 时 GitHub API 返回 422，调用方需自行处理降级。
func (c *Client) CreateIssueWithLabels(ctx context.Context, owner, repo, title, body string, labels []string) (*IssueInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", escapePath(owner), escapePath(repo))
	reqBody := map[string]interface{}{
		"title": title,
		"body":  body,
	}
	if len(labels) > 0 {
		reqBody["labels"] = labels
	}
	resp, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create Issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create Issue (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var issueResp ghIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issueResp); err != nil {
		return nil, fmt.Errorf("failed to parse Issue response: %w", err)
	}

	result := convertIssueResponse(issueResp)
	return &result, nil
}

// SetBaseURL 覆盖 GitHub API 基础 URL，仅供测试注入 mock server 使用。
// 生产代码不应调用此方法；默认值为 https://api.github.com。
func (c *Client) SetBaseURL(url string) {
	c.baseURL = url
}

// SetHTTPClient 覆盖内部 http.Client，仅供测试注入 httptest.Server.Client() 使用。
// 生产代码不应调用此方法；默认 client 超时 30 秒。
func (c *Client) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// GetIssue gets Issue details
func (c *Client) GetIssue(ctx context.Context, owner, repo string, issueNumber int) (*IssueInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", escapePath(owner), escapePath(repo), issueNumber)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get Issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get Issue (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var issueResp ghIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issueResp); err != nil {
		return nil, fmt.Errorf("failed to parse Issue response: %w", err)
	}

	result := convertIssueResponse(issueResp)
	return &result, nil
}

// CloseIssue closes an Issue
func (c *Client) CloseIssue(ctx context.Context, owner, repo string, issueNumber int) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", escapePath(owner), escapePath(repo), issueNumber)
	reqBody := map[string]interface{}{
		"state": "closed",
	}
	resp, err := c.doRequest(ctx, http.MethodPatch, path, reqBody)
	if err != nil {
		return fmt.Errorf("failed to close Issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to close Issue (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// convertIssueResponse converts GitHub API response to internal IssueInfo structure
func convertIssueResponse(issue ghIssueResponse) IssueInfo {
	labels := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		labels[i] = l.Name
	}
	return IssueInfo{
		Number:  issue.Number,
		Title:   issue.Title,
		State:   issue.State,
		User:    issue.User.Login,
		HTMLURL: issue.HTMLURL,
		Body:    issue.Body,
		Labels:  labels,
	}
}

// WorkflowInfo is basic Workflow information
type WorkflowInfo struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Path  string `json:"path"`
}

// WorkflowRunInfo is Workflow run information
type WorkflowRunInfo struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`     // queued, in_progress, completed
	Conclusion string    `json:"conclusion"` // success, failure, cancelled, timed_out
	WorkflowID int64     `json:"workflow_id"`
	HeadBranch string    `json:"head_branch"`
	RunNumber  int       `json:"run_number"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WorkflowJobInfo is Workflow Job information
type WorkflowJobInfo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	RunnerName  string `json:"runner_name"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

// ghWorkflowResponse is the GitHub API Workflow response structure
type ghWorkflowResponse struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Path  string `json:"path"`
}

// ghWorkflowRunResponse is the GitHub API Workflow Run response structure
type ghWorkflowRunResponse struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	WorkflowID int64     `json:"workflow_id"`
	HeadBranch string    `json:"head_branch"`
	RunNumber  int       `json:"run_number"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ghWorkflowJobResponse is the GitHub API Workflow Job response structure
type ghWorkflowJobResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	RunnerName  string `json:"runner_name"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

// ListWorkflows lists Workflows of the repository
func (c *Client) ListWorkflows(ctx context.Context, owner, repo string) ([]WorkflowInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows", escapePath(owner), escapePath(repo))
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list Workflows: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list Workflows (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var listResp struct {
		Workflows []ghWorkflowResponse `json:"workflows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to parse Workflow list: %w", err)
	}

	result := make([]WorkflowInfo, len(listResp.Workflows))
	for i, wf := range listResp.Workflows {
		result[i] = WorkflowInfo{
			ID:    wf.ID,
			Name:  wf.Name,
			State: wf.State,
			Path:  wf.Path,
		}
	}
	return result, nil
}

// ListWorkflowRuns lists Workflow run records
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo, branch string) ([]WorkflowRunInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=20", escapePath(owner), escapePath(repo))
	if branch != "" {
		path += "&branch=" + escapeQuery(branch)
	}
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list Workflow runs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list Workflow runs (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var listResp struct {
		WorkflowRuns []ghWorkflowRunResponse `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to parse Workflow run list: %w", err)
	}

	result := make([]WorkflowRunInfo, len(listResp.WorkflowRuns))
	for i, run := range listResp.WorkflowRuns {
		result[i] = WorkflowRunInfo{
			ID:         run.ID,
			Name:       run.Name,
			Status:     run.Status,
			Conclusion: run.Conclusion,
			WorkflowID: run.WorkflowID,
			HeadBranch: run.HeadBranch,
			RunNumber:  run.RunNumber,
			HTMLURL:    run.HTMLURL,
			CreatedAt:  run.CreatedAt,
			UpdatedAt:  run.UpdatedAt,
		}
	}
	return result, nil
}

// GetWorkflowRun gets Workflow run details
func (c *Client) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (*WorkflowRunInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d", escapePath(owner), escapePath(repo), runID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get Workflow run details: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get Workflow run details (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var runResp ghWorkflowRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
		return nil, fmt.Errorf("failed to parse Workflow run details: %w", err)
	}

	result := WorkflowRunInfo{
		ID:         runResp.ID,
		Name:       runResp.Name,
		Status:     runResp.Status,
		Conclusion: runResp.Conclusion,
		WorkflowID: runResp.WorkflowID,
		HeadBranch: runResp.HeadBranch,
		RunNumber:  runResp.RunNumber,
		HTMLURL:    runResp.HTMLURL,
		CreatedAt:  runResp.CreatedAt,
		UpdatedAt:  runResp.UpdatedAt,
	}
	return &result, nil
}

// GetWorkflowJobs gets Job list for a Workflow run
func (c *Client) GetWorkflowJobs(ctx context.Context, owner, repo string, runID int64) ([]WorkflowJobInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", escapePath(owner), escapePath(repo), runID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get Workflow Jobs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get Workflow Jobs (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var listResp struct {
		Jobs []ghWorkflowJobResponse `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to parse Workflow Jobs: %w", err)
	}

	result := make([]WorkflowJobInfo, len(listResp.Jobs))
	for i, job := range listResp.Jobs {
		result[i] = WorkflowJobInfo{
			ID:          job.ID,
			Name:        job.Name,
			Status:      job.Status,
			Conclusion:  job.Conclusion,
			RunnerName:  job.RunnerName,
			StartedAt:   job.StartedAt,
			CompletedAt: job.CompletedAt,
		}
	}
	return result, nil
}

// TriggerWorkflow triggers a Workflow
func (c *Client) TriggerWorkflow(ctx context.Context, owner, repo string, workflowID int64, ref string, inputs map[string]string) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%d/dispatches", escapePath(owner), escapePath(repo), workflowID)
	reqBody := map[string]interface{}{
		"ref":    ref,
		"inputs": inputs,
	}
	resp, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return fmt.Errorf("failed to trigger Workflow: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to trigger Workflow (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// RerunWorkflow reruns a Workflow
func (c *Client) RerunWorkflow(ctx context.Context, owner, repo string, runID int64) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/rerun", escapePath(owner), escapePath(repo), runID)
	resp, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return fmt.Errorf("failed to rerun Workflow: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to rerun Workflow (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetWorkflowLogs fetches Workflow run logs
func (c *Client) GetWorkflowLogs(ctx context.Context, owner, repo string, runID int64) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/logs", escapePath(owner), escapePath(repo), runID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", fmt.Errorf("fetch Workflow loggingfailed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound {
		// logs redirected to download URL
		location := resp.Header.Get("Location")
		if location != "" {
			return fmt.Sprintf("logs download URL: %s", location), nil
		}
		return "logs redirected but no download URL found", nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("fetch Workflow loggingfailed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	logData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read log content: %w", err)
	}

	result := string(logData)
	if len(result) > 10000 {
		result = result[:10000] + "\n... (loggingtruncate)"
	}

	return result, nil
}

// convertPRResponse converts GitHub API response to internal PRInfo structure
func convertPRResponse(pr ghPRResponse) PRInfo {
	userLogin := pr.User.Login
	if userLogin == "" && pr.Head.User.Login != "" {
		userLogin = pr.Head.User.Login
	}
	return PRInfo{
		Number:  pr.Number,
		Title:   pr.Title,
		State:   pr.State,
		User:    userLogin,
		HTMLURL: pr.HTMLURL,
		HeadRef: pr.Head.Ref,
		BaseRef: pr.Base.Ref,
	}
}

// CheckWritePermission checks if the current token user has write permission on the repo
func (c *Client) CheckWritePermission(ctx context.Context, owner, repo string) error {
	path := fmt.Sprintf("/repos/%s/%s", escapePath(owner), escapePath(repo))
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("failed to check repo permission: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to check repo permission (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var repoInfo struct {
		Permissions struct {
			Admin bool `json:"admin"`
			Push  bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return fmt.Errorf("failed to parse repo permission: %w", err)
	}

	if !repoInfo.Permissions.Admin && !repoInfo.Permissions.Push {
		return fmt.Errorf("no write permission on %s/%s: current user does not have push or admin access", owner, repo)
	}

	return nil
}
