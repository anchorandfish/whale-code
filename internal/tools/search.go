package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// globHandler use glob modesearchfile
func (e *Executor) globHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input GlobInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.Pattern == "" {
			return "", fmt.Errorf("search pattern cannot be empty")
		}

		searchDir := input.Path
		if searchDir == "" {
			searchDir = workDir
		}

		// C14: 文件操作工具调 sandbox.ValidatePath（Glob 搜索目录需在允许范围内）
		if err := e.validatePathSandbox(ctx, searchDir); err != nil {
			// 软沙箱拒绝（S11 G1-B 定型，G1-A 收口）：文案不变（模型契约），
			// 以 errSandboxDeniedSoft 让 executeDirect 打上 FailureKind=SandboxDenied。
			return fmt.Sprintf("Sandbox denied: %s", err.Error()), errSandboxDeniedSoft
		}

		// recordsearchcontext
		if e.SearchCtx != nil {
			e.SearchCtx.RecordSearch(fmt.Sprintf("glob:%s", input.Pattern))
		}

		engine := NewSearchEngine(searchDir)
		output, err := engine.Glob(&input)
		if err != nil {
			return "", fmt.Errorf("searchfailed: %w", err)
		}

		// applicationsearchresultbudgetcontrol
		budget := DefaultSearchResultBudget()
		result := formatGlobOutput(output, searchDir)
		result = budget.TruncateOutput(result)

		return result, nil
	}
}

// grepHandler uses regex to search file content
func (e *Executor) grepHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input GrepInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.Pattern == "" {
			return "", fmt.Errorf("search pattern cannot be empty")
		}

		searchDir := input.Path
		if searchDir == "" {
			searchDir = workDir
		}

		// C14: 文件操作工具调 sandbox.ValidatePath（Grep 搜索目录需在允许范围内）
		if err := e.validatePathSandbox(ctx, searchDir); err != nil {
			// 软沙箱拒绝（S11 G1-B 定型，G1-A 收口）：文案不变（模型契约），
			// 以 errSandboxDeniedSoft 让 executeDirect 打上 FailureKind=SandboxDenied。
			return fmt.Sprintf("Sandbox denied: %s", err.Error()), errSandboxDeniedSoft
		}

		// recordsearchcontext
		if e.SearchCtx != nil {
			e.SearchCtx.RecordSearch(fmt.Sprintf("grep:%s", input.Pattern))
		}

		engine := NewSearchEngine(searchDir)
		output, err := engine.Grep(&input)
		if err != nil {
			return "", fmt.Errorf("searchfailed: %w", err)
		}

		// applicationsearchresultbudgetcontrol
		budget := DefaultSearchResultBudget()
		result := formatGrepOutput(output, searchDir)
		result = budget.TruncateOutput(result)

		return result, nil
	}
}
