package tools

import (
	"strings"
	"testing"
)

func TestBugHunterManager_New_DefaultStopped(t *testing.T) {
	bh := NewBugHunterManager()
	if bh.IsRunning() {
		t.Error("初始状态应为停止")
	}
	if bh.IssueCount() != 0 {
		t.Error("初始问题数应为 0")
	}
}

func TestBugHunterManager_Start(t *testing.T) {
	bh := NewBugHunterManager()
	result := bh.Start()
	if !bh.IsRunning() {
		t.Error("Start 后应运行中")
	}
	if !strings.Contains(result, "已启动") {
		t.Errorf("should return启动消息，actual: %s", result)
	}
}

func TestBugHunterManager_Stop(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	result := bh.Stop()
	if bh.IsRunning() {
		t.Error("Stop 后应停止")
	}
	if !strings.Contains(result, "已停止") {
		t.Errorf("should return停止消息，actual: %s", result)
	}
}

func TestBugHunterManager_Start_AlreadyRunning(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	result := bh.Start()
	if !strings.Contains(result, "已在运行") {
		t.Errorf("should return已在运行消息，actual: %s", result)
	}
}

func TestBugHunterManager_Stop_AlreadyStopped(t *testing.T) {
	bh := NewBugHunterManager()
	result := bh.Stop()
	if !strings.Contains(result, "未运行") {
		t.Errorf("should return未运行消息，actual: %s", result)
	}
}

func TestBugHunterManager_AddIssue(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	issue := BugIssue{
		File:        "main.go",
		Line:        42,
		Severity:    "high",
		Description: "未检查 nil 指针",
		Status:      "open",
	}
	bh.AddIssue(issue)
	if bh.IssueCount() != 1 {
		t.Errorf("问题数应为 1，actual: %d", bh.IssueCount())
	}
	issues := bh.ListIssues("open")
	if len(issues) != 1 || issues[0].File != "main.go" {
		t.Error("返回的问题列表不正确")
	}
}

func TestBugHunterManager_AddIssue_NotRunning(t *testing.T) {
	bh := NewBugHunterManager()
	issue := BugIssue{
		File:        "main.go",
		Line:        42,
		Severity:    "high",
		Description: "bug",
		Status:      "open",
	}
	bh.AddIssue(issue)
	if bh.IssueCount() != 1 {
		t.Errorf("未运行时也应能添加问题，actual: %d", bh.IssueCount())
	}
}

func TestBugHunterManager_GetSystemPrompt(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	prompt := bh.GetSystemPrompt()
	if prompt == "" {
		t.Error("运行时should return non-empty提示词")
	}
	if !strings.Contains(prompt, "BugHunter") {
		t.Error("提示词should contain模式名称")
	}
}

func TestBugHunterManager_GetSystemPrompt_Stopped(t *testing.T) {
	bh := NewBugHunterManager()
	if bh.GetSystemPrompt() != "" {
		t.Error("停止时should return empty字符串")
	}
}

func TestBugHunterManager_Status_Running(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	bh.AddIssue(BugIssue{File: "a.go", Line: 1, Severity: "high", Description: "bug", Status: "open"})
	status := bh.Status()
	if !strings.Contains(status, "运行中") {
		t.Error("状态should contain'运行中'")
	}
	if !strings.Contains(status, "1") {
		t.Error("状态should contain问题数")
	}
}

func TestBugHunterManager_Status_Stopped(t *testing.T) {
	bh := NewBugHunterManager()
	status := bh.Status()
	if !strings.Contains(status, "未运行") {
		t.Error("状态should contain'未运行'")
	}
}

func TestBugHunterManager_MarkIssueFixed(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	bh.AddIssue(BugIssue{File: "a.go", Line: 1, Severity: "high", Description: "bug", Status: "open"})
	err := bh.MarkIssueFixed("BH-001")
	if err != nil {
		t.Errorf("标记fixed失败: %v", err)
	}
	openIssues := bh.ListIssues("open")
	if len(openIssues) != 0 {
		t.Error("标记后不应有未fixed问题")
	}
}

func TestBugHunterManager_MarkIssueFixed_NotFound(t *testing.T) {
	bh := NewBugHunterManager()
	err := bh.MarkIssueFixed("BH-999")
	if err == nil {
		t.Error("不存在的问题should return error")
	}
}

func TestBugHunterManager_ListIssues(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	bh.AddIssue(BugIssue{File: "a.go", Line: 1, Severity: "high", Description: "bug1", Status: "open"})
	bh.AddIssue(BugIssue{File: "b.go", Line: 2, Severity: "low", Description: "bug2", Status: "open"})
	bh.MarkIssueFixed("BH-001")

	all := bh.ListIssues("")
	if len(all) != 2 {
		t.Errorf("应列出所有 2 个问题，actual: %d", len(all))
	}
	open := bh.ListIssues("open")
	if len(open) != 1 {
		t.Errorf("应有 1 个未fixed问题，actual: %d", len(open))
	}
	fixed := bh.ListIssues("fixed")
	if len(fixed) != 1 {
		t.Errorf("应有 1 个已fixed问题，actual: %d", len(fixed))
	}
}

func TestBugHunterManager_Stop_Summary(t *testing.T) {
	bh := NewBugHunterManager()
	bh.Start()
	bh.AddIssue(BugIssue{File: "a.go", Line: 1, Severity: "high", Description: "bug1", Status: "open"})
	bh.AddIssue(BugIssue{File: "b.go", Line: 2, Severity: "low", Description: "bug2", Status: "open"})
	bh.MarkIssueFixed("BH-001")

	result := bh.Stop()
	if !strings.Contains(result, "发现 2") {
		t.Errorf("应显示发现问题数，actual: %s", result)
	}
	if !strings.Contains(result, "已fixed 1") {
		t.Errorf("应显示fixed数，actual: %s", result)
	}
	if !strings.Contains(result, "待处理 1") {
		t.Errorf("应显示待处理数，actual: %s", result)
	}
}
