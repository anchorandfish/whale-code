package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- Issue API unit test ---

// TestListIssues 测试列出 Issue
func TestListIssues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues" {
			t.Errorf("Expected path /repos/owner/repo/issues, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("Expected state=open, got %s", r.URL.Query().Get("state"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"number":1,"title":"Bug","state":"open","html_url":"https://github.com/owner/repo/issues/1","body":"desc","user":{"login":"test"},"labels":[{"name":"bug"}]}]`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	issues, err := client.ListIssues(context.Background(), "owner", "repo", "open")
	if err != nil {
		t.Fatalf("ListIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(issues))
	}
	if issues[0].Number != 1 {
		t.Errorf("Expected number 1, got %d", issues[0].Number)
	}
	if issues[0].Title != "Bug" {
		t.Errorf("Expected title 'Bug', got %q", issues[0].Title)
	}
	if len(issues[0].Labels) != 1 || issues[0].Labels[0] != "bug" {
		t.Errorf("Expected labels [bug], got %v", issues[0].Labels)
	}
}

// TestCreateIssue 测试创建 Issue
func TestCreateIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"number":42,"title":"New Issue","state":"open","html_url":"https://github.com/owner/repo/issues/42","body":"body text","user":{"login":"test"},"labels":[]}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	issue, err := client.CreateIssue(context.Background(), "owner", "repo", "New Issue", "body text")
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("Expected number 42, got %d", issue.Number)
	}
	if issue.Title != "New Issue" {
		t.Errorf("Expected title 'New Issue', got %q", issue.Title)
	}
}

// TestGetIssue 测试获取 Issue 详情
func TestGetIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/repos/owner/repo/issues/7"
		if r.URL.Path != expectedPath {
			t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"number":7,"title":"Detail","state":"open","html_url":"https://github.com/owner/repo/issues/7","body":"details","user":{"login":"test"},"labels":[{"name":"enhancement"}]}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	issue, err := client.GetIssue(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if issue.Number != 7 {
		t.Errorf("Expected number 7, got %d", issue.Number)
	}
}

// TestCloseIssue 测试关闭 Issue
func TestCloseIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("Expected PATCH, got %s", r.Method)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["state"] != "closed" {
			t.Errorf("Expected state=closed, got %v", body["state"])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"number":7,"title":"Detail","state":"closed","html_url":"","body":"","user":{"login":"test"},"labels":[]}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	err := client.CloseIssue(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}
}

// --- GitHub Actions API unit test ---

// TestListWorkflows 测试列出 Workflows
func TestListWorkflows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/actions/workflows" {
			t.Errorf("Expected path /repos/owner/repo/actions/workflows, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"workflows":[{"id":1,"name":"CI","state":"active","path":".github/workflows/ci.yml"},{"id":2,"name":"Deploy","state":"active","path":".github/workflows/deploy.yml"}]}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	workflows, err := client.ListWorkflows(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("ListWorkflows failed: %v", err)
	}
	if len(workflows) != 2 {
		t.Fatalf("Expected 2 workflows, got %d", len(workflows))
	}
	if workflows[0].Name != "CI" {
		t.Errorf("Expected name 'CI', got %q", workflows[0].Name)
	}
}

// TestListWorkflowRuns 测试列出 Workflow 运行记录
func TestListWorkflowRuns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"workflow_runs":[{"id":101,"name":"CI","status":"completed","conclusion":"success","workflow_id":1,"head_branch":"main","run_number":42,"html_url":"https://github.com/owner/repo/actions/runs/101","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:05:00Z"}]}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	runs, err := client.ListWorkflowRuns(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("ListWorkflowRuns failed: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("Expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != "completed" {
		t.Errorf("Expected status 'completed', got %q", runs[0].Status)
	}
	if runs[0].Conclusion != "success" {
		t.Errorf("Expected conclusion 'success', got %q", runs[0].Conclusion)
	}
}

// TestGetWorkflowRun 测试获取 Workflow 运行详情
func TestGetWorkflowRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/repos/owner/repo/actions/runs/101"
		if r.URL.Path != expectedPath {
			t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":101,"name":"CI","status":"completed","conclusion":"success","workflow_id":1,"head_branch":"main","run_number":42,"html_url":"https://github.com/owner/repo/actions/runs/101","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:05:00Z"}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	run, err := client.GetWorkflowRun(context.Background(), "owner", "repo", 101)
	if err != nil {
		t.Fatalf("GetWorkflowRun failed: %v", err)
	}
	if run.ID != 101 {
		t.Errorf("Expected ID 101, got %d", run.ID)
	}
}

// TestGetWorkflowJobs 测试获取 Workflow Jobs
func TestGetWorkflowJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jobs":[{"id":201,"name":"build","status":"completed","conclusion":"success","runner_name":"ubuntu-latest","started_at":"2024-01-01T00:00:00Z","completed_at":"2024-01-01T00:03:00Z"}]}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	jobs, err := client.GetWorkflowJobs(context.Background(), "owner", "repo", 101)
	if err != nil {
		t.Fatalf("GetWorkflowJobs failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("Expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Name != "build" {
		t.Errorf("Expected name 'build', got %q", jobs[0].Name)
	}
}

// TestTriggerWorkflow 测试触发 Workflow
func TestTriggerWorkflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	err := client.TriggerWorkflow(context.Background(), "owner", "repo", 1, "main", nil)
	if err != nil {
		t.Fatalf("TriggerWorkflow failed: %v", err)
	}
}

// TestRerunWorkflow 测试重新运行 Workflow
func TestRerunWorkflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	err := client.RerunWorkflow(context.Background(), "owner", "repo", 101)
	if err != nil {
		t.Fatalf("RerunWorkflow failed: %v", err)
	}
}

// TestGetWorkflowLogs 测试获取 Workflow 日志
func TestGetWorkflowLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Run build\nStep 1: checkout\nStep 2: build\nSuccess!")
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	logs, err := client.GetWorkflowLogs(context.Background(), "owner", "repo", 101)
	if err != nil {
		t.Fatalf("GetWorkflowLogs failed: %v", err)
	}
	if logs == "" {
		t.Error("Expected non-empty logs")
	}
}

// --- API 错误处理测试 ---

// TestListIssues_Error 测试列出 Issue 错误处理
func TestListIssues_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"message":"Not Found"}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	_, err := client.ListIssues(context.Background(), "owner", "repo", "open")
	if err == nil {
		t.Error("Should fail with 404")
	}
}

// TestCreateIssue_Error 测试创建 Issue 错误处理
func TestCreateIssue_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"message":"Forbidden"}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	_, err := client.CreateIssue(context.Background(), "owner", "repo", "title", "body")
	if err == nil {
		t.Error("Should fail with 403")
	}
}

// TestListWorkflows_Error 测试列出 Workflows 错误处理
func TestListWorkflows_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"message":"Not Found"}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	_, err := client.ListWorkflows(context.Background(), "owner", "repo")
	if err == nil {
		t.Error("Should fail with 404")
	}
}

// TestTriggerWorkflow_Error 测试触发 Workflow 错误处理
func TestTriggerWorkflow_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(w, `{"message":"Validation Failed"}`)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	err := client.TriggerWorkflow(context.Background(), "owner", "repo", 1, "main", nil)
	if err == nil {
		t.Error("Should fail with 422")
	}
}

// --- 数据结构测试 ---

// TestIssueInfo_Fields 测试 IssueInfo 字段
func TestIssueInfo_Fields(t *testing.T) {
	issue := IssueInfo{
		Number:  1,
		Title:   "Test",
		State:   "open",
		User:    "testuser",
		HTMLURL: "https://github.com/owner/repo/issues/1",
		Body:    "description",
		Labels:  []string{"bug", "help wanted"},
	}
	if issue.Number != 1 {
		t.Errorf("Number = %d, want 1", issue.Number)
	}
	if len(issue.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(issue.Labels))
	}
}

// TestWorkflowRunInfo_Fields 测试 WorkflowRunInfo 字段
func TestWorkflowRunInfo_Fields(t *testing.T) {
	run := WorkflowRunInfo{
		ID:         101,
		Name:       "CI",
		Status:     "completed",
		Conclusion: "success",
		WorkflowID: 1,
		HeadBranch: "main",
		RunNumber:  42,
	}
	if run.ID != 101 {
		t.Errorf("ID = %d, want 101", run.ID)
	}
	if run.Status != "completed" {
		t.Errorf("Status = %q, want %q", run.Status, "completed")
	}
}

// TestWorkflowJobInfo_Fields 测试 WorkflowJobInfo 字段
func TestWorkflowJobInfo_Fields(t *testing.T) {
	job := WorkflowJobInfo{
		ID:         201,
		Name:       "build",
		Status:     "completed",
		Conclusion: "success",
		RunnerName: "ubuntu-latest",
	}
	if job.RunnerName != "ubuntu-latest" {
		t.Errorf("RunnerName = %q, want %q", job.RunnerName, "ubuntu-latest")
	}
}

// TestWorkflowInfo_Fields 测试 WorkflowInfo 字段
func TestWorkflowInfo_Fields(t *testing.T) {
	wf := WorkflowInfo{
		ID:    1,
		Name:  "CI",
		State: "active",
		Path:  ".github/workflows/ci.yml",
	}
	if wf.Name != "CI" {
		t.Errorf("Name = %q, want %q", wf.Name, "CI")
	}
}
