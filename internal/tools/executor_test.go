package tools

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/plugin"
)

// TestExecutor_Execute_Success test execution已注册的工具处理器并获取结果
func TestExecutor_Execute_Success(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "test output", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "test_tool",
		Input: `{}`,
	})

	if result.IsError {
		t.Errorf("expected执行成功，但得到错误: %s", result.Output)
	}
	if result.Output != "test output" {
		t.Errorf("Output = %q, want %q", result.Output, "test output")
	}
	if result.ID != "1" {
		t.Errorf("ID = %q, want %q", result.ID, "1")
	}
	if result.Name != "test_tool" {
		t.Errorf("Name = %q, want %q", result.Name, "test_tool")
	}
}

// TestExecutor_Execute_UnknownTool test execution未注册的工具时返回错误
func TestExecutor_Execute_UnknownTool(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "nonexistent_tool",
		Input: `{}`,
	})

	if !result.IsError {
		t.Error("expected未知工具返回错误，但 IsError 为 false")
	}
	if result.Output == "" {
		t.Error("错误输出should not be empty")
	}
}

// TestExecutor_Execute_InvalidInput 测试无效输入（非 JSON / 空输入）返回错误
func TestExecutor_Execute_InvalidInput(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	tests := []struct {
		name  string
		input string
	}{
		{"空输入", ""},
		{"非JSON", "not json at all"},
		{"数组JSON", "[1,2,3]"},
		{"字符串JSON", `"hello"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := e.Execute(context.Background(), Call{
				ID:    "1",
				Name:  "bash",
				Input: tt.input,
			})
			if !result.IsError {
				t.Errorf("无效输入 %q should return error", tt.input)
			}
		})
	}
}

// TestExecutor_ExecuteBatch 测试批量执行：安全工具并发执行，非安全工具顺序执行
func TestExecutor_ExecuteBatch(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册两个自定义工具：一个安全、一个不安全
	var safeCount int64
	err := e.RegisterHandler("safe_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		atomic.AddInt64(&safeCount, 1)
		return "safe result", nil
	})
	if err != nil {
		t.Fatalf("注册 safe_tool 失败: %v", err)
	}

	var unsafeCount int64
	err = e.RegisterHandler("unsafe_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		atomic.AddInt64(&unsafeCount, 1)
		return "unsafe result", nil
	})
	if err != nil {
		t.Fatalf("注册 unsafe_tool 失败: %v", err)
	}

	// 手动将 safe_tool 标记为并发安全（通过注册到 Registry）
	r.builtin["safe_tool"] = Spec{
		Name:            "safe_tool",
		ConcurrencySafe: true,
	}

	calls := []Call{
		{ID: "1", Name: "safe_tool", Input: `{}`},
		{ID: "2", Name: "safe_tool", Input: `{}`},
		{ID: "3", Name: "unsafe_tool", Input: `{}`},
		{ID: "4", Name: "unsafe_tool", Input: `{}`},
	}

	results := e.ExecuteBatch(context.Background(), calls)

	if len(results) != 4 {
		t.Fatalf("expected 4 个结果，得到 %d 个", len(results))
	}

	// 验证结果按原始顺序返回
	if results[0].ID != "1" || results[1].ID != "2" {
		t.Errorf("安全工具结果顺序错误: got IDs %q, %q", results[0].ID, results[1].ID)
	}
	if results[2].ID != "3" || results[3].ID != "4" {
		t.Errorf("非安全工具结果顺序错误: got IDs %q, %q", results[2].ID, results[3].ID)
	}

	// 验证安全工具并发执行了
	if atomic.LoadInt64(&safeCount) != 2 {
		t.Errorf("safe_tool 执行次数 = %d, want 2", safeCount)
	}
	if atomic.LoadInt64(&unsafeCount) != 2 {
		t.Errorf("unsafe_tool 执行次数 = %d, want 2", unsafeCount)
	}

	// 验证输出内容
	for i := 0; i < 2; i++ {
		if results[i].Output != "safe result" {
			t.Errorf("results[%d].Output = %q, want %q", i, results[i].Output, "safe result")
		}
	}
	for i := 2; i < 4; i++ {
		if results[i].Output != "unsafe result" {
			t.Errorf("results[%d].Output = %q, want %q", i, results[i].Output, "unsafe result")
		}
	}
}

// TestExecutor_RegisterHandler 测试注册自定义处理器（成功和重复注册失败）
func TestExecutor_RegisterHandler(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 首次注册应成功
	err := e.RegisterHandler("custom_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "custom output", nil
	})
	if err != nil {
		t.Fatalf("首次注册应成功: %v", err)
	}

	// 验证处理器可以执行
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "custom_tool",
		Input: `{}`,
	})
	if result.IsError {
		t.Errorf("执行自定义工具失败: %s", result.Output)
	}
	if result.Output != "custom output" {
		t.Errorf("Output = %q, want %q", result.Output, "custom output")
	}

	// 重复注册应失败
	err = e.RegisterHandler("custom_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "should not register", nil
	})
	if err == nil {
		t.Error("重复注册should return error")
	}
}

// TestExecutor_ExecuteWithPipeline test passed六阶段管道执行工具调用
func TestExecutor_ExecuteWithPipeline(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("pipeline_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "pipeline output", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 注册到 Registry 以通过 Schema 验证
	r.builtin["pipeline_tool"] = Spec{
		Name:        "pipeline_tool",
		Description: "测试管道工具",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}

	// 初始化管道（使用最高权限策略，no hook）
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipeline(hookMgr, policy, tmpDir)

	// 通过管道执行
	pr := e.ExecuteWithPipeline(context.Background(), Call{
		ID:    "1",
		Name:  "pipeline_tool",
		Input: `{"message":"hello"}`,
	})

	if !pr.Proceed {
		t.Errorf("管道应通过，but was rejected: %s", pr.Rejection)
	}
	if pr.Result == nil {
		t.Fatal("管道结果不should be nil")
	}
	if pr.Result.Output != "pipeline output" {
		t.Errorf("Output = %q, want %q", pr.Result.Output, "pipeline output")
	}
	if len(pr.StageLogs) == 0 {
		t.Error("应有阶段日志")
	}

	// 验证管道未初始化时的降级行为
	e2 := NewExecutor(r, tmpDir)
	err = e2.RegisterHandler("no_pipeline_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "fallback output", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	pr2 := e2.ExecuteWithPipeline(context.Background(), Call{
		ID:    "2",
		Name:  "no_pipeline_tool",
		Input: `{}`,
	})
	if !pr2.Proceed {
		t.Errorf("无管道降级执行应成功，but was rejected: %s", pr2.Rejection)
	}
	if pr2.Result == nil {
		t.Fatal("降级结果不should be nil")
	}
	if pr2.Result.Output != "fallback output" {
		t.Errorf("降级 Output = %q, want %q", pr2.Result.Output, "fallback output")
	}
}

// TestExecutor_Execute_HandlerError 测试处理器返回错误时的结果
func TestExecutor_Execute_HandlerError(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("error_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "", fmt.Errorf("something went wrong")
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "error_tool",
		Input: `{}`,
	})

	if !result.IsError {
		t.Error("处理器返回错误时 IsError 应为 true")
	}
	if result.Output == "" {
		t.Error("错误输出should not be empty")
	}
}

// TestExecutor_Execute_HandlerErrorWithOutput 测试处理器返回错误同时有部分输出
func TestExecutor_Execute_HandlerErrorWithOutput(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	err := e.RegisterHandler("partial_error_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "partial output", fmt.Errorf("partial error")
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "partial_error_tool",
		Input: `{}`,
	})

	if !result.IsError {
		t.Error("应标记为错误")
	}
	// 错误消息should contain部分输出
	if result.Output == "" {
		t.Error("错误输出should not be empty")
	}
}

// TestExecutor_Execute_KnownTool test execution已知工具（read_file）返回正确结果且 IsError=false
func TestExecutor_Execute_KnownTool(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 先写入一个文件
	testFile := tmpDir + "/known_test.txt"
	writeResult := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "write_file",
		Input: mustMarshal(map[string]string{"path": testFile, "content": "known tool test"}),
	})
	if writeResult.IsError {
		t.Fatalf("写入文件失败: %s", writeResult.Output)
	}

	// 使用 read_file 读取
	result := e.Execute(context.Background(), Call{
		ID:    "2",
		Name:  "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})

	if result.IsError {
		t.Errorf("read_file 应成功，但得到错误: %s", result.Output)
	}
	if result.ID != "2" {
		t.Errorf("result.ID = %q, want %q", result.ID, "2")
	}
	if result.Name != "read_file" {
		t.Errorf("result.Name = %q, want %q", result.Name, "read_file")
	}
	if !strings.Contains(result.Output, "known tool test") {
		t.Errorf("输出should contain文件内容，actual: %s", result.Output)
	}
}

// TestExecutor_ExecuteBatch_ConcurrentSafe 测试多个并发安全工具（glob_search）并行执行
func TestExecutor_ExecuteBatch_ConcurrentSafe(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 创建测试文件
	os.WriteFile(tmpDir+"/a.txt", []byte("a"), 0644)
	os.WriteFile(tmpDir+"/b.txt", []byte("b"), 0644)

	calls := []Call{
		{ID: "1", Name: "glob_search", Input: mustMarshal(map[string]string{"pattern": "*.txt", "path": tmpDir})},
		{ID: "2", Name: "glob_search", Input: mustMarshal(map[string]string{"pattern": "*.txt", "path": tmpDir})},
		{ID: "3", Name: "glob_search", Input: mustMarshal(map[string]string{"pattern": "*.txt", "path": tmpDir})},
	}

	results := e.ExecuteBatch(context.Background(), calls)

	if len(results) != 3 {
		t.Fatalf("expected 3 个结果，得到 %d 个", len(results))
	}

	for i, r := range results {
		if r.IsError {
			t.Errorf("results[%d] 不should be error: %s", i, r.Output)
		}
	}
}

// TestExecutor_ExecuteBatch_Unsafe 测试多个非并发安全工具（write_file）顺序执行
func TestExecutor_ExecuteBatch_Unsafe(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	testFile1 := tmpDir + "/unsafe1.txt"
	testFile2 := tmpDir + "/unsafe2.txt"

	calls := []Call{
		{ID: "1", Name: "write_file", Input: mustMarshal(map[string]string{"path": testFile1, "content": "content1"})},
		{ID: "2", Name: "write_file", Input: mustMarshal(map[string]string{"path": testFile2, "content": "content2"})},
	}

	results := e.ExecuteBatch(context.Background(), calls)

	if len(results) != 2 {
		t.Fatalf("expected 2 个结果，得到 %d 个", len(results))
	}

	for i, r := range results {
		if r.IsError {
			t.Errorf("results[%d] 不should be error: %s", i, r.Output)
		}
	}

	// 验证文件内容正确
	data1, err := os.ReadFile(testFile1)
	if err != nil || string(data1) != "content1" {
		t.Errorf("文件1内容不正确: %q", string(data1))
	}
	data2, err := os.ReadFile(testFile2)
	if err != nil || string(data2) != "content2" {
		t.Errorf("文件2内容不正确: %q", string(data2))
	}
}

// TestExecutor_FileChangeCallback 已删除（阶段 5：OnFileChange 回调随 undo 包一并移除）

// TestExecutor_Execute_NormalizeToolName test execution时工具名自动specification化（别名支持）
func TestExecutor_Execute_NormalizeToolName(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 使用别名 "read" 调用 read_file
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "read",
		Input: `{"path":"/nonexistent/file.txt"}`,
	})

	// 应该specification化为 read_file 并执行（文件不存在会报错，但不是"未知工具"错误）
	if !result.IsError {
		// 如果文件碰巧存在也不会到这里，但主要验证不是"未知工具"错误
	}
	if result.Name != "read_file" {
		t.Errorf("Name = %q, want %q（应specification化）", result.Name, "read_file")
	}
}

// TestExecutor_ToolResultInjection test tool执行结果可被格式化为 tool_result 消息
// 验证执行工具后获取的 Result 能正确转换为 tool_result 格式，
// 包含 tool_use_id 和 output 字段，以便注入回对话消息流
func TestExecutor_ToolResultInjection(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册自定义处理器
	err := e.RegisterHandler("inject_test_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "tool execution result", nil
	})
	if err != nil {
		t.Fatalf("注册处理器失败: %v", err)
	}

	// 执行工具
	result := e.Execute(context.Background(), Call{
		ID:    "tool_call_001",
		Name:  "inject_test_tool",
		Input: `{}`,
	})

	if result.IsError {
		t.Fatalf("工具执行不应出错: %s", result.Output)
	}

	// 验证 Result 字段可用于构建 tool_result 消息
	if result.ID != "tool_call_001" {
		t.Errorf("Result.ID = %q, want %q（对应 tool_use_id）", result.ID, "tool_call_001")
	}
	if result.Name != "inject_test_tool" {
		t.Errorf("Result.Name = %q, want %q", result.Name, "inject_test_tool")
	}
	if result.Output != "tool execution result" {
		t.Errorf("Result.Output = %q, want %q", result.Output, "tool execution result")
	}

	// mock将 Result 格式化为 tool_result 消息（Anthropic 格式）
	toolResultMsg := map[string]interface{}{
		"role": "tool",
		"content": []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": result.ID,
				"output":      result.Output,
			},
		},
	}

	// 验证 tool_result 消息结构
	if role, _ := toolResultMsg["role"].(string); role != "tool" {
		t.Errorf("tool_result role = %q, want %q", role, "tool")
	}
	content, ok := toolResultMsg["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("tool_result content 应有 1 个元素")
	}
	block, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatal("tool_result content[0] 类型不正确")
	}
	if block["type"] != "tool_result" {
		t.Errorf("block type = %q, want %q", block["type"], "tool_result")
	}
	if block["tool_use_id"] != "tool_call_001" {
		t.Errorf("block tool_use_id = %q, want %q", block["tool_use_id"], "tool_call_001")
	}
	if block["output"] != "tool execution result" {
		t.Errorf("block output = %q, want %q", block["output"], "tool execution result")
	}

	// 也验证错误结果的 tool_result 格式
	errResult := e.Execute(context.Background(), Call{
		ID:    "tool_call_002",
		Name:  "nonexistent_tool",
		Input: `{}`,
	})
	if !errResult.IsError {
		t.Error("未知工具should return error结果")
	}
	// 错误结果也应能格式化为 tool_result，is_error 标记为 true
	errToolResultBlock := map[string]interface{}{
		"type":        "tool_result",
		"tool_use_id": errResult.ID,
		"output":      errResult.Output,
		"is_error":    errResult.IsError,
	}
	if errToolResultBlock["is_error"] != true {
		t.Error("错误工具结果的 is_error 应为 true")
	}
}

// TestExecutor_PartialFailure 测试批量执行中的部分失败
// 执行一个包含成功和失败工具的批量调用，
// 验证成功的工具返回正确结果，失败的工具记录错误信息
func TestExecutor_PartialFailure(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册一个会成功的工具
	err := e.RegisterHandler("success_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "success output", nil
	})
	if err != nil {
		t.Fatalf("注册 success_tool 失败: %v", err)
	}

	// 注册一个会失败的工具
	err = e.RegisterHandler("failure_tool", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "", fmt.Errorf("intentional failure")
	})
	if err != nil {
		t.Fatalf("注册 failure_tool 失败: %v", err)
	}

	// 将 success_tool 标记为并发安全
	r.builtin["success_tool"] = Spec{
		Name:            "success_tool",
		ConcurrencySafe: true,
	}

	calls := []Call{
		{ID: "1", Name: "success_tool", Input: `{}`},
		{ID: "2", Name: "failure_tool", Input: `{}`},
	}

	results := e.ExecuteBatch(context.Background(), calls)

	if len(results) != 2 {
		t.Fatalf("expected 2 个结果，得到 %d 个", len(results))
	}

	// 验证第一个工具成功
	if results[0].IsError {
		t.Errorf("success_tool 不应出错，但得到: %s", results[0].Output)
	}
	if results[0].Output != "success output" {
		t.Errorf("success_tool Output = %q, want %q", results[0].Output, "success output")
	}
	if results[0].ID != "1" {
		t.Errorf("success_tool ID = %q, want %q", results[0].ID, "1")
	}

	// 验证第二个工具失败
	if !results[1].IsError {
		t.Error("failure_tool 应标记为错误")
	}
	if results[1].Output == "" {
		t.Error("failure_tool 错误输出should not be empty")
	}
	if !strings.Contains(results[1].Output, "intentional failure") {
		t.Errorf("failure_tool 输出should contain错误信息，actual: %q", results[1].Output)
	}
	if results[1].ID != "2" {
		t.Errorf("failure_tool ID = %q, want %q", results[1].ID, "2")
	}

	// 验证结果按原始顺序返回
	if results[0].Name != "success_tool" {
		t.Errorf("results[0].Name = %q, want success_tool", results[0].Name)
	}
	if results[1].Name != "failure_tool" {
		t.Errorf("results[1].Name = %q, want failure_tool", results[1].Name)
	}
}

// createTestPluginDir 创建包含指定插件的临时目录结构，返回 workDir
// 用于测试 Executor 的插件注册/注销功能
func createTestPluginDir(t *testing.T, pluginID string, toolDefs []plugin.ToolDef) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, ".whale", "plugins", pluginID)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}

	manifest := plugin.Plugin{
		ID:      pluginID,
		Name:    pluginID,
		Version: "1.0.0",
		Enabled: true,
		Tools:   toolDefs,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("序列化插件清单失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644); err != nil {
		t.Fatalf("写入 plugin.json 失败: %v", err)
	}

	return dir
}

// TestUnregisterPluginTools 测试注销插件工具：从 Registry 和 handlers 中移除指定插件的工具
func TestUnregisterPluginTools(t *testing.T) {
	// 创建包含测试插件的临时目录
	pluginID := "test-plugin"
	toolDefs := []plugin.ToolDef{
		{
			Name:        "test_plugin_tool",
			Description: "Test plugin tool",
			Command:     "echo test",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	workDir := createTestPluginDir(t, pluginID, toolDefs)

	reg := NewRegistry()
	exec := NewExecutor(reg, workDir)
	// P0-7/B7: 插件默认禁用，需显式启用后工具才注册
	exec.EnableAllPlugins()

	// 验证插件工具已注册
	_, ok := reg.Get("test_plugin_tool")
	if !ok {
		t.Fatal("插件工具应该已被注册")
	}

	// 验证 handler 已注册
	if _, ok := exec.handlers["test_plugin_tool"]; !ok {
		t.Fatal("插件工具 handler 应该已被注册")
	}

	// 注销插件工具
	exec.UnregisterPluginTools(pluginID)

	// 验证工具已从 Registry 中移除
	_, ok = reg.Get("test_plugin_tool")
	if ok {
		t.Error("注销后插件工具应从 Registry 中移除")
	}

	// 验证 handler 已移除
	if _, ok := exec.handlers["test_plugin_tool"]; ok {
		t.Error("注销后插件工具 handler 应被移除")
	}
}

// TestRegisterSinglePluginTools 测试注册单个插件工具：将指定插件的工具注册到 Registry 和 handlers
func TestRegisterSinglePluginTools(t *testing.T) {
	// 创建包含测试插件的临时目录
	pluginID := "test-plugin"
	toolDefs := []plugin.ToolDef{
		{
			Name:        "test_plugin_tool",
			Description: "Test plugin tool",
			Command:     "echo test",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	workDir := createTestPluginDir(t, pluginID, toolDefs)

	reg := NewRegistry()
	exec := NewExecutor(reg, workDir)

	// 先禁用并注销插件
	exec.PluginMgr.Disable(pluginID)
	exec.UnregisterPluginTools(pluginID)

	// 验证工具已移除
	_, ok := reg.Get("test_plugin_tool")
	if ok {
		t.Fatal("注销后工具应不存在")
	}

	// 重新启用并注册
	if err := exec.PluginMgr.Enable(pluginID); err != nil {
		t.Fatalf("启用插件失败: %v", err)
	}
	if err := exec.RegisterSinglePluginTools(pluginID); err != nil {
		t.Fatalf("RegisterSinglePluginTools 失败: %v", err)
	}

	// 验证工具已重新注册
	_, ok = reg.Get("test_plugin_tool")
	if !ok {
		t.Error("启用后插件工具应被注册到 Registry")
	}

	// 验证 handler 已注册
	if _, ok := exec.handlers["test_plugin_tool"]; !ok {
		t.Error("启用后插件工具 handler 应被注册")
	}
}

// TestScheduleCronTool 测试 schedule_cron 工具是否在 Registry 中注册
func TestScheduleCronTool(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("schedule_cron")
	if !ok {
		t.Error("schedule_cron tool should be registered")
	}
	if spec.Name != "schedule_cron" {
		t.Errorf("Expected name 'schedule_cron', got '%s'", spec.Name)
	}
}

// TestUserChoiceTool 测试 user_choice 工具是否在 Registry 中注册
func TestUserChoiceTool(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("user_choice")
	if !ok {
		t.Error("user_choice tool should be registered")
	}
	_ = spec
}

// TestConfigGetTool 测试 config_get 工具是否在 Registry 中注册
func TestConfigGetTool(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("config_get")
	if !ok {
		t.Error("config_get tool should be registered")
	}
	_ = spec
}

// TestConfigSetTool 测试 config_set 工具是否在 Registry 中注册
func TestConfigSetTool(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("config_set")
	if !ok {
		t.Error("config_set tool should be registered")
	}
	_ = spec
}

// TestExportSessionTool 测试 export_session 工具是否在 Registry 中注册
func TestExportSessionTool(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("export_session")
	if !ok {
		t.Error("export_session tool should be registered")
	}
	_ = spec
}

// TestStructuredOutputTool 测试 structured_output 工具是否在 Registry 中注册
func TestStructuredOutputTool(t *testing.T) {
	reg := NewRegistry()
	spec, ok := reg.Get("structured_output")
	if !ok {
		t.Error("structured_output tool should be registered")
	}
	_ = spec
}

// TestNewToolsCount test tool总数是否达到 36 个
func TestNewToolsCount(t *testing.T) {
	reg := NewRegistry()
	count := len(reg.List())
	if count < 36 {
		t.Errorf("Expected at least 36 tools, got %d", count)
	}
}

// TestScheduleCronHandler 测试 schedule_cron 处理器是否注册且能正常执行
func TestScheduleCronHandler(t *testing.T) {
	exec := NewExecutor(NewRegistry(), t.TempDir())
	handler, ok := exec.handlers["schedule_cron"]
	if !ok {
		t.Error("schedule_cron handler should be registered")
	}
	result, err := handler(context.Background(), json.RawMessage(`{"schedule": "0 * * * *", "command": "echo hello"}`))
	if err != nil {
		t.Errorf("schedule_cron handler failed: %v", err)
	}
	if result == "" {
		t.Error("schedule_cron handler should return non-empty result")
	}
}

// TestConfigGetHandler 测试 config_get 处理器是否注册且能正常执行
func TestConfigGetHandler(t *testing.T) {
	dir := t.TempDir()
	exec := NewExecutor(NewRegistry(), dir)
	handler, ok := exec.handlers["config_get"]
	if !ok {
		t.Error("config_get handler should be registered")
	}
	result, err := handler(context.Background(), json.RawMessage(`{"key": "model"}`))
	if err != nil {
		t.Errorf("config_get handler failed: %v", err)
	}
	if result == "" {
		t.Error("config_get handler should return non-empty result")
	}
}

// TestConfigSetHandler 测试 config_set 处理器是否注册且能正常执行
func TestConfigSetHandler(t *testing.T) {
	dir := t.TempDir()
	exec := NewExecutor(NewRegistry(), dir)
	handler, ok := exec.handlers["config_set"]
	if !ok {
		t.Error("config_set handler should be registered")
	}
	result, err := handler(context.Background(), json.RawMessage(`{"key": "model", "value": "gpt-4"}`))
	if err != nil {
		t.Errorf("config_set handler failed: %v", err)
	}
	if result == "" {
		t.Error("config_set handler should return non-empty result")
	}
}

// TestUserChoiceHandler 测试 user_choice 处理器是否注册且能正常执行
func TestUserChoiceHandler(t *testing.T) {
	exec := NewExecutor(NewRegistry(), t.TempDir())
	handler, ok := exec.handlers["user_choice"]
	if !ok {
		t.Error("user_choice handler should be registered")
	}
	result, err := handler(context.Background(), json.RawMessage(`{"question": "Which approach?", "options": ["A", "B", "C"]}`))
	if err != nil {
		t.Errorf("user_choice handler failed: %v", err)
	}
	if result == "" {
		t.Error("user_choice handler should return non-empty result")
	}
}

// TestExportSessionHandler 测试 export_session 处理器是否注册且能正常执行
func TestExportSessionHandler(t *testing.T) {
	dir := t.TempDir()
	exec := NewExecutor(NewRegistry(), dir)
	handler, ok := exec.handlers["export_session"]
	if !ok {
		t.Error("export_session handler should be registered")
	}
	result, err := handler(context.Background(), json.RawMessage(`{"path": "session_export.json"}`))
	if err != nil {
		t.Errorf("export_session handler failed: %v", err)
	}
	if result == "" {
		t.Error("export_session handler should return non-empty result")
	}
}

// TestStructuredOutputHandler 测试 structured_output 处理器是否注册且能正常执行
func TestStructuredOutputHandler(t *testing.T) {
	exec := NewExecutor(NewRegistry(), t.TempDir())
	handler, ok := exec.handlers["structured_output"]
	if !ok {
		t.Error("structured_output handler should be registered")
	}
	result, err := handler(context.Background(), json.RawMessage(`{"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}`))
	if err != nil {
		t.Errorf("structured_output handler failed: %v", err)
	}
	if result == "" {
		t.Error("structured_output handler should return non-empty result")
	}
}

// TestPluginEnableDisableCycle 测试插件的完整启用/禁用循环：注册→注销→重新注册
func TestPluginEnableDisableCycle(t *testing.T) {
	// 创建包含测试插件的临时目录
	pluginID := "cycle-plugin"
	toolDefs := []plugin.ToolDef{
		{
			Name:        "cycle_tool",
			Description: "Cycle test tool",
			Command:     "echo cycle",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	workDir := createTestPluginDir(t, pluginID, toolDefs)

	reg := NewRegistry()
	exec := NewExecutor(reg, workDir)
	// P0-7/B7: 插件默认禁用，需显式启用后工具才注册
	exec.EnableAllPlugins()

	// 验证初始注册状态
	_, ok := reg.Get("cycle_tool")
	if !ok {
		t.Fatal("插件工具初始应已注册")
	}

	// 禁用插件并注销工具
	if err := exec.PluginMgr.Disable(pluginID); err != nil {
		t.Fatalf("禁用插件失败: %v", err)
	}
	exec.UnregisterPluginTools(pluginID)

	// 验证工具已移除
	_, ok = reg.Get("cycle_tool")
	if ok {
		t.Error("禁用后工具应从 Registry 中移除")
	}
	if _, ok := exec.handlers["cycle_tool"]; ok {
		t.Error("禁用后 handler 应被移除")
	}

	// 重新启用并注册
	if err := exec.PluginMgr.Enable(pluginID); err != nil {
		t.Fatalf("重新启用插件失败: %v", err)
	}
	if err := exec.RegisterSinglePluginTools(pluginID); err != nil {
		t.Fatalf("RegisterSinglePluginTools 失败: %v", err)
	}

	// 验证工具已重新注册
	_, ok = reg.Get("cycle_tool")
	if !ok {
		t.Error("重新启用后工具应被注册到 Registry")
	}
	if _, ok := exec.handlers["cycle_tool"]; !ok {
		t.Error("重新启用后 handler 应被注册")
	}
}

// TestRegisterSinglePluginTools_RecordsFailures 验证单插件工具注册失败被收集（M20-04）。
// 构造一个工具 name 与 builtin 冲突的场景，RegisterSinglePluginTools 应记录失败项到
// process-global 累计器，可通过 DrainToolRegistrationFailures 取出。
func TestRegisterSinglePluginTools_RecordsFailures(t *testing.T) {
	// 重置累计器，确保测试从干净状态开始
	ResetToolRegistrationFailures()
	defer ResetToolRegistrationFailures()

	pluginID := "conflict-plugin"
	// 工具名 "bash" 与 builtin 冲突 → RegisterPlugin 应返回错误
	toolDefs := []plugin.ToolDef{
		{
			Name:        "bash", // 与 builtin 冲突
			Description: "Conflict tool",
			Command:     "echo conflict",
		},
	}
	workDir := createTestPluginDir(t, pluginID, toolDefs)

	reg := NewRegistry()
	exec := NewExecutor(reg, workDir)
	exec.PluginMgr.Enable(pluginID)

	// NewExecutor 内部已调用 RegisterPluginTools（plugin manifest Enabled=true，
	// "bash" 与 builtin 冲突已记录 1 条失败）。为隔离测试 RegisterSinglePluginTools
	// 的行为，在调用前重置累计器。
	ResetToolRegistrationFailures()

	// RegisterSinglePluginTools 应返回 nil（失败被收集而非返回 error）
	if err := exec.RegisterSinglePluginTools(pluginID); err != nil {
		t.Fatalf("RegisterSinglePluginTools 不应直接返回 error: %v", err)
	}

	// Drain 累计器，应有一条失败项：plugin=conflict-plugin tool=bash
	failures := DrainToolRegistrationFailures()
	if len(failures) != 1 {
		t.Fatalf("应有 1 条失败记录，实际: %d", len(failures))
	}
	if failures[0].PluginID != pluginID {
		t.Errorf("失败项 PluginID = %q, 期望 %q", failures[0].PluginID, pluginID)
	}
	if failures[0].ToolName != "bash" {
		t.Errorf("失败项 ToolName = %q, 期望 %q", failures[0].ToolName, "bash")
	}
	if failures[0].Reason == nil {
		t.Error("失败项 Reason 不应为 nil")
	}
	if !strings.Contains(failures[0].Reason.Error(), "bash") {
		t.Errorf("失败项 Reason 应包含 \"bash\"，实际: %v", failures[0].Reason)
	}
}

// TestRegisterPluginTools_RecordsMultipleFailures 验证多插件工具注册失败全部被收集（M20-04）。
// 构造两个插件，各自有一个冲突工具，RegisterPluginTools 后应累计 2 条失败项。
func TestRegisterPluginTools_RecordsMultipleFailures(t *testing.T) {
	// 重置累计器
	ResetToolRegistrationFailures()
	defer ResetToolRegistrationFailures()

	// 构造两个插件目录：每个插件含一个与 builtin 冲突的工具（read_file / write_file）
	dir := t.TempDir()
	plugin1Dir := filepath.Join(dir, ".whale", "plugins", "conflict-1")
	if err := os.MkdirAll(plugin1Dir, 0755); err != nil {
		t.Fatalf("创建 plugin1 目录失败: %v", err)
	}
	manifest1 := plugin.Plugin{
		ID:      "conflict-1",
		Name:    "Conflict 1",
		Version: "1.0.0",
		Enabled: true,
		Tools: []plugin.ToolDef{
			{Name: "read_file", Description: "Conflict 1", Command: "echo 1"},
		},
	}
	data1, _ := json.MarshalIndent(manifest1, "", "  ")
	os.WriteFile(filepath.Join(plugin1Dir, "plugin.json"), data1, 0644)

	plugin2Dir := filepath.Join(dir, ".whale", "plugins", "conflict-2")
	if err := os.MkdirAll(plugin2Dir, 0755); err != nil {
		t.Fatalf("创建 plugin2 目录失败: %v", err)
	}
	manifest2 := plugin.Plugin{
		ID:      "conflict-2",
		Name:    "Conflict 2",
		Version: "1.0.0",
		Enabled: true,
		Tools: []plugin.ToolDef{
			{Name: "write_file", Description: "Conflict 2", Command: "echo 2"},
		},
	}
	data2, _ := json.MarshalIndent(manifest2, "", "  ")
	os.WriteFile(filepath.Join(plugin2Dir, "plugin.json"), data2, 0644)

	reg := NewRegistry()
	exec := NewExecutor(reg, dir)

	// EnableAllPlugins 触发 RegisterPluginTools
	// 注意：NewExecutor 内部已调用一次 RegisterPluginTools，但插件默认禁用，应无失败。
	// 现在显式 enable 全部，触发第二次注册。
	exec.EnableAllPlugins()

	// Drain 累计器，应有 2 条失败项
	failures := DrainToolRegistrationFailures()
	if len(failures) != 2 {
		t.Fatalf("应有 2 条失败记录（来自 conflict-1 + conflict-2），实际: %d", len(failures))
	}

	// 收集所有 (PluginID, ToolName) 对，验证两个失败项均存在
	type pair struct{ pluginID, toolName string }
	got := make(map[pair]bool)
	for _, f := range failures {
		got[pair{f.PluginID, f.ToolName}] = true
	}
	expected := []pair{
		{"conflict-1", "read_file"},
		{"conflict-2", "write_file"},
	}
	for _, e := range expected {
		if !got[e] {
			t.Errorf("未找到预期失败项 plugin=%s tool=%s", e.pluginID, e.toolName)
		}
	}
}

// TestDrainToolRegistrationFailures_Idempotent 验证 Drain 后再次调用返回空（M20-04）。
func TestDrainToolRegistrationFailures_Idempotent(t *testing.T) {
	ResetToolRegistrationFailures()
	defer ResetToolRegistrationFailures()

	// 直接构造一条失败项
	recordToolRegistrationFailure("p1", "tool1", fmt.Errorf("test error"))

	// 第一次 Drain 应返回 1 条
	first := DrainToolRegistrationFailures()
	if len(first) != 1 {
		t.Fatalf("第一次 Drain 应返回 1 条，实际: %d", len(first))
	}

	// 第二次 Drain 应返回 0 条（已 drain）
	second := DrainToolRegistrationFailures()
	if len(second) != 0 {
		t.Errorf("第二次 Drain 应返回 0 条（已 drain），实际: %d", len(second))
	}
}

// TestFormatToolRegistrationFailures_EmptyAndNonEmpty 验证格式化函数对空与非空列表的处理（M20-04）。
func TestFormatToolRegistrationFailures_EmptyAndNonEmpty(t *testing.T) {
	// 空列表应返回空字符串
	if s := FormatToolRegistrationFailures(nil); s != "" {
		t.Errorf("空列表应返回空字符串，实际: %q", s)
	}
	if s := FormatToolRegistrationFailures([]ToolRegistrationFailure{}); s != "" {
		t.Errorf("空切片应返回空字符串，实际: %q", s)
	}

	// 非空列表应包含每条记录的关键字段
	failures := []ToolRegistrationFailure{
		{PluginID: "p1", ToolName: "t1", Reason: fmt.Errorf("conflict")},
		{PluginID: "p2", ToolName: "t2", Reason: fmt.Errorf("invalid")},
	}
	s := FormatToolRegistrationFailures(failures)
	if !strings.Contains(s, "p1") {
		t.Errorf("格式化结果应包含 \"p1\"，实际: %q", s)
	}
	if !strings.Contains(s, "t1") {
		t.Errorf("格式化结果应包含 \"t1\"，实际: %q", s)
	}
	if !strings.Contains(s, "conflict") {
		t.Errorf("格式化结果应包含 \"conflict\"，实际: %q", s)
	}
	if !strings.Contains(s, "p2") {
		t.Errorf("格式化结果应包含 \"p2\"，实际: %q", s)
	}
}

// TestBuildPluginSignatureVerifier_WiresNewSignatureVerifier 桶 X5 死代码清理反例测试（R-3 后向兼容）：
// 验证 buildPluginSignatureVerifier 接线 plugin.NewSignatureVerifier，使该工厂不再是零调用死代码。
// 修复前：NewSignatureVerifier 仅测试引用；修复后：buildPluginSignatureVerifier 在生产路径调用它。
// R-3：函数签名从无参改为接收 config.PluginSignatureConfig，零值配置走 env 降级路径（桶 X5 行为）。
func TestBuildPluginSignatureVerifier_WiresNewSignatureVerifier(t *testing.T) {
	// 场景1：未配置公钥且非必需 → 返回非 nil（noopVerifier，NewSignatureVerifier 内置降级）
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")
	verifier, err := buildPluginSignatureVerifier(config.PluginSignatureConfig{})
	if err != nil {
		t.Fatalf("buildPluginSignatureVerifier(零值) 返回 error: %v", err)
	}
	if verifier == nil {
		t.Fatal("buildPluginSignatureVerifier(零值) 返回 nil，期望非 nil SignatureVerifier（接线 NewSignatureVerifier）")
	}
	// noopVerifier.Verify 应返回 nil（无签名要求时放行）
	if err := verifier.Verify(t.TempDir()); err != nil {
		t.Errorf("noopVerifier.Verify 应返回 nil（未配置签名），实际: %v", err)
	}

	// 场景2：env 配置 Required=true → 仍返回非 nil（ed25519Verifier 或降级实现）
	// 零值 cfg（Enabled=false）走 env fallback 路径，保留桶 X5 行为（向后兼容）
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "true")
	verifier2, err := buildPluginSignatureVerifier(config.PluginSignatureConfig{})
	if err != nil {
		t.Fatalf("buildPluginSignatureVerifier(env Required=true) 返回 error: %v", err)
	}
	if verifier2 == nil {
		t.Fatal("buildPluginSignatureVerifier(env Required=true) 返回 nil，期望非 nil")
	}
}

// TestNewExecutor_WiresSignatureVerifier 桶 X5 端到端反例测试（R-3 后向兼容）：
// 验证 NewExecutor 构造后 PluginMgr 已注入 SignatureVerifier
// （接线链路：NewExecutor → NewExecutorWithConfig → buildPluginSignatureVerifier → SetSignatureVerifier）。
// R-3：NewExecutor 改为 NewExecutorWithConfig 的包装，nil cfg 走 env 降级路径（桶 X5 行为）。
func TestNewExecutor_WiresSignatureVerifier(t *testing.T) {
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")
	reg := NewRegistry()
	exec := NewExecutor(reg, t.TempDir())
	// PluginMgr.signatureVerifier 字段通过 SetSignatureVerifier 注入，无法直接断言私有字段。
	// 间接验证：Install 未签名插件时走 signatureVerifier 分支（fail-closed 或 noopVerifier 放行）。
	// 此处仅验证 PluginMgr 非 nil 且未 panic，证明接线代码路径被执行。
	if exec.PluginMgr == nil {
		t.Fatal("NewExecutor 后 PluginMgr 为 nil")
	}
}

// TestBuildPluginSignatureVerifier_ConfigEnabled_ValidPublicKey R-3 反例测试 1：
// 验证 config.PluginSignature.Enabled=true + PublicKeyPath 有效 → 使用 ed25519Verifier。
// 期望：返回非 nil verifier 且无 error，Verify 一个无 plugin.json 的目录时返回 "read manifest" 错误
// （证明是 ed25519Verifier 而非 noopVerifier，noopVerifier.Verify 总返回 nil）。
func TestBuildPluginSignatureVerifier_ConfigEnabled_ValidPublicKey(t *testing.T) {
	// 准备一个临时的 ed25519 公钥文件（base64 编码文本）
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey 失败: %v", err)
	}
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKey)
	keyFile := filepath.Join(t.TempDir(), "pub.key")
	if err := os.WriteFile(keyFile, []byte(pubKeyB64), 0o600); err != nil {
		t.Fatalf("写入公钥文件失败: %v", err)
	}

	// 清理 env，确保走 config 路径而非 env fallback
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")

	cfg := config.PluginSignatureConfig{
		Enabled:       true,
		Required:      false,
		PublicKeyPath: keyFile,
	}
	verifier, err := buildPluginSignatureVerifier(cfg)
	if err != nil {
		t.Fatalf("buildPluginSignatureVerifier 期望成功，实际 error: %v", err)
	}
	if verifier == nil {
		t.Fatal("buildPluginSignatureVerifier 期望非 nil verifier")
	}
	// 验证返回的是 ed25519Verifier（非 noopVerifier）：
	// noopVerifier.Verify 总返回 nil；ed25519Verifier.Verify 在 plugin.json 不存在时返回 "read manifest" 错误。
	emptyDir := t.TempDir()
	verifyErr := verifier.Verify(emptyDir)
	if verifyErr == nil {
		t.Error("期望 ed25519Verifier.Verify 返回 error（plugin.json 不存在），实际 nil（可能是 noopVerifier）")
	} else if !strings.Contains(verifyErr.Error(), "read manifest") {
		t.Errorf("期望 error 包含 \"read manifest\"，实际: %v", verifyErr)
	}
}

// TestBuildPluginSignatureVerifier_ConfigEnabled_RequiredNoKey_FailClosed R-3 反例测试 2：
// 验证 config.PluginSignature.Enabled=true + PublicKeyPath 为空 + Required=true → fail-closed error。
// 期望：返回 nil verifier 和非 nil error，拒绝降级到 noopVerifier。
func TestBuildPluginSignatureVerifier_ConfigEnabled_RequiredNoKey_FailClosed(t *testing.T) {
	// 清理 env，确保不触发 env fallback
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")

	cfg := config.PluginSignatureConfig{
		Enabled:       true,
		Required:      true,
		PublicKeyPath: "", // 故意留空
	}
	verifier, err := buildPluginSignatureVerifier(cfg)
	if err == nil {
		t.Fatal("期望 fail-closed error（Enabled=true + Required=true + PublicKeyPath 空），实际无 error")
	}
	if verifier != nil {
		t.Errorf("期望 nil verifier（fail-closed），实际非 nil: %T", verifier)
	}
	// 验证 error 消息包含关键信息（fail-closed 标识）
	if !strings.Contains(err.Error(), "fail-closed") {
		t.Errorf("error 消息应包含 \"fail-closed\"，实际: %v", err)
	}
}

// TestBuildPluginSignatureVerifier_ConfigDisabled_Noop R-3 反例测试 3：
// 验证 config.PluginSignature.Enabled=false → noopVerifier（向后兼容）。
// 期望：env 也未设置时返回 noopVerifier（Verify 总返回 nil）。
func TestBuildPluginSignatureVerifier_ConfigDisabled_Noop(t *testing.T) {
	// 清理 env
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")

	cfg := config.PluginSignatureConfig{
		Enabled: false, // 显式 false
	}
	verifier, err := buildPluginSignatureVerifier(cfg)
	if err != nil {
		t.Fatalf("Enabled=false 不应返回 error: %v", err)
	}
	if verifier == nil {
		t.Fatal("期望非 nil noopVerifier")
	}
	// noopVerifier.Verify 总返回 nil
	if err := verifier.Verify(t.TempDir()); err != nil {
		t.Errorf("noopVerifier.Verify 应返回 nil，实际: %v", err)
	}
}

// TestBuildPluginSignatureVerifier_ConfigUnset_EnvRequiredTrue_BackwardCompat R-3 反例测试 4：
// 验证 config 未配置（零值）+ 环境变量 WHALE_PLUGIN_SIGNING_REQUIRED=true → 使用环境变量（向后兼容）。
// 期望：返回非 nil verifier（ed25519Verifier，lazy fail-closed），无 error。
// 注意：此场景不显式 fail-closed（保留桶 X5 行为，NewSignatureVerifier 内置 lazy fail-closed）。
func TestBuildPluginSignatureVerifier_ConfigUnset_EnvRequiredTrue_BackwardCompat(t *testing.T) {
	// 设置 env，模拟桶 X5 接线行为
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "true")

	// 零值 cfg（Enabled=false）→ 走 env fallback
	cfg := config.PluginSignatureConfig{}
	verifier, err := buildPluginSignatureVerifier(cfg)
	if err != nil {
		t.Fatalf("env fallback 不应返回 error（向后兼容）: %v", err)
	}
	if verifier == nil {
		t.Fatal("期望非 nil verifier（env Required=true → ed25519Verifier）")
	}
	// ed25519Verifier.Verify 在 PublicKey="" 时报错 "public key not configured"
	// （lazy fail-closed，与桶 X5 行为一致）
	verifyErr := verifier.Verify(t.TempDir())
	if verifyErr == nil {
		t.Error("期望 lazy fail-closed error（PublicKey 空），实际 Verify 返回 nil")
	}
}

// TestBuildPluginSignatureVerifier_ConfigAndEnvBothUnset_Noop R-3 反例测试 5：
// 验证 config 和环境变量都未配置 → noopVerifier（向后兼容）。
// 期望：返回 noopVerifier，Verify 总返回 nil。
func TestBuildPluginSignatureVerifier_ConfigAndEnvBothUnset_Noop(t *testing.T) {
	// 清理 env
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")

	cfg := config.PluginSignatureConfig{}
	verifier, err := buildPluginSignatureVerifier(cfg)
	if err != nil {
		t.Fatalf("零值配置不应返回 error: %v", err)
	}
	if verifier == nil {
		t.Fatal("期望非 nil noopVerifier")
	}
	if err := verifier.Verify(t.TempDir()); err != nil {
		t.Errorf("noopVerifier.Verify 应返回 nil，实际: %v", err)
	}
}

// TestNewExecutorWithConfig_FailClosedAbortsConstruction R-3 端到端反例测试：
// 验证 NewExecutorWithConfig 在 fail-closed 场景下返回 error，不构造 Executor。
// 期望：cfg.PluginSignature.Enabled=true + Required=true + PublicKeyPath="" → 返回 error。
func TestNewExecutorWithConfig_FailClosedAbortsConstruction(t *testing.T) {
	// 清理 env，确保不触发 env fallback
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")

	cfg := &config.Config{
		PluginSignature: config.PluginSignatureConfig{
			Enabled:       true,
			Required:      true,
			PublicKeyPath: "", // 故意留空触发 fail-closed
		},
	}
	reg := NewRegistry()
	exec, err := NewExecutorWithConfig(reg, t.TempDir(), cfg)
	if err == nil {
		t.Fatal("期望 fail-closed error，实际无 error")
	}
	if exec != nil {
		t.Errorf("期望 nil Executor（fail-closed），实际非 nil")
	}
}

// TestNewExecutorWithConfig_NilCfg_BackwardCompat R-3 后向兼容端到端测试：
// 验证 NewExecutorWithConfig 接收 nil cfg 时退化为桶 X5 行为（env 降级，不 fail-closed）。
// 期望：返回非 nil Executor 且无 error。
func TestNewExecutorWithConfig_NilCfg_BackwardCompat(t *testing.T) {
	// 清理 env
	t.Setenv("WHALE_PLUGIN_SIGNING_PUBLIC_KEY", "")
	t.Setenv("WHALE_PLUGIN_SIGNING_REQUIRED", "")

	reg := NewRegistry()
	exec, err := NewExecutorWithConfig(reg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("nil cfg 不应返回 error: %v", err)
	}
	if exec == nil {
		t.Fatal("期望非 nil Executor")
	}
	if exec.PluginMgr == nil {
		t.Fatal("PluginMgr 为 nil")
	}
}

// batchMockTool 描述一个用于 ExecuteBatch 时序验证的 mock 工具：
// 包含工具名、并发安全标记、模拟执行延迟与固定输出，便于构造可预测耗时的并发/串行场景。
type batchMockTool struct {
	name            string
	concurrencySafe bool
	latency         time.Duration
	output          string
}

// registerBatchMockTool 将 batchMockTool 注册到 Executor（handler 闭包内按 latency 休眠后返回 output）
// 并同步在 Registry 的 builtin map 中标记 ConcurrencySafe 属性，使 IsConcurrencySafe 分桶生效。
func registerBatchMockTool(t *testing.T, e *Executor, r *Registry, m batchMockTool) {
	t.Helper()
	err := e.RegisterHandler(m.name, func(ctx context.Context, input json.RawMessage) (string, error) {
		if m.latency > 0 {
			time.Sleep(m.latency)
		}
		return m.output, nil
	})
	if err != nil {
		t.Fatalf("注册 %s 失败: %v", m.name, err)
	}
	r.builtin[m.name] = Spec{
		Name:            m.name,
		ConcurrencySafe: m.concurrencySafe,
	}
}

// TestExecuteBatch_ReadOnlyToolsParallel 验证 ExecuteBatch 对并发安全（只读）工具的并行执行能力：
// 3 个 ConcurrencySafe=true 的 mock 工具各自 sleep 100ms，并行执行总耗时应远小于串行的 300ms，
// 据此证明 safe 工具走 goroutine+sem 并发路径而非串行。
func TestExecuteBatch_ReadOnlyToolsParallel(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	mocks := []batchMockTool{
		{name: "ro_a", concurrencySafe: true, latency: 100 * time.Millisecond, output: "A"},
		{name: "ro_b", concurrencySafe: true, latency: 100 * time.Millisecond, output: "B"},
		{name: "ro_c", concurrencySafe: true, latency: 100 * time.Millisecond, output: "C"},
	}
	for _, m := range mocks {
		registerBatchMockTool(t, e, r, m)
	}

	calls := []Call{
		{ID: "1", Name: "ro_a", Input: `{}`},
		{ID: "2", Name: "ro_b", Input: `{}`},
		{ID: "3", Name: "ro_c", Input: `{}`},
	}

	start := time.Now()
	results := e.ExecuteBatch(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d 个", len(results))
	}
	expected := []string{"A", "B", "C"}
	for i, want := range expected {
		if results[i].IsError {
			t.Errorf("results[%d] 不应为错误: %s", i, results[i].Output)
		}
		if results[i].Output != want {
			t.Errorf("results[%d].Output = %q, want %q", i, results[i].Output, want)
		}
	}
	// 串行需 ~300ms，并行应 ~100ms+调度开销；250ms 阈值足以区分并行与串行
	if elapsed >= 250*time.Millisecond {
		t.Errorf("并行执行耗时 %v 超过 250ms，疑似未并行（串行需 ~300ms）", elapsed)
	}
}

// TestExecuteBatch_WriteToolsSequential 验证 ExecuteBatch 对非并发安全（写）工具的串行执行：
// 3 个 ConcurrencySafe=false 的 mock 工具各自 sleep 100ms，串行执行总耗时应接近 300ms，
// 据此证明 unsafe 工具走 for 循环串行路径而非并发。
func TestExecuteBatch_WriteToolsSequential(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	mocks := []batchMockTool{
		{name: "wr_a", concurrencySafe: false, latency: 100 * time.Millisecond, output: "WA"},
		{name: "wr_b", concurrencySafe: false, latency: 100 * time.Millisecond, output: "WB"},
		{name: "wr_c", concurrencySafe: false, latency: 100 * time.Millisecond, output: "WC"},
	}
	for _, m := range mocks {
		registerBatchMockTool(t, e, r, m)
	}

	calls := []Call{
		{ID: "1", Name: "wr_a", Input: `{}`},
		{ID: "2", Name: "wr_b", Input: `{}`},
		{ID: "3", Name: "wr_c", Input: `{}`},
	}

	start := time.Now()
	results := e.ExecuteBatch(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d 个", len(results))
	}
	expected := []string{"WA", "WB", "WC"}
	for i, want := range expected {
		if results[i].IsError {
			t.Errorf("results[%d] 不应为错误: %s", i, results[i].Output)
		}
		if results[i].Output != want {
			t.Errorf("results[%d].Output = %q, want %q", i, results[i].Output, want)
		}
	}
	// 串行需 ~300ms，允许 20ms 误差下限（低于 280ms 说明疑似并发）
	if elapsed < 280*time.Millisecond {
		t.Errorf("串行执行耗时 %v 不足 280ms，疑似未串行（串行需 ~300ms）", elapsed)
	}
}

// TestExecuteBatch_ResultOrderPreserved 验证 ExecuteBatch 即使并发执行，结果切片顺序仍与输入调用顺序一一对应：
// 混合 safe/unsafe 工具按 A/B/C 顺序调用，结果索引必须对齐（results[idx] 由 idx 写入，与执行完成先后无关）。
func TestExecuteBatch_ResultOrderPreserved(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 故意让 safe 工具延迟大于 unsafe，确保 safe 与 unsafe 完成先后不同，但仍按索引对齐
	mocks := []batchMockTool{
		{name: "ord_a", concurrencySafe: true, latency: 40 * time.Millisecond, output: "A"},
		{name: "ord_b", concurrencySafe: false, latency: 10 * time.Millisecond, output: "B"},
		{name: "ord_c", concurrencySafe: true, latency: 20 * time.Millisecond, output: "C"},
	}
	for _, m := range mocks {
		registerBatchMockTool(t, e, r, m)
	}

	calls := []Call{
		{ID: "1", Name: "ord_a", Input: `{}`},
		{ID: "2", Name: "ord_b", Input: `{}`},
		{ID: "3", Name: "ord_c", Input: `{}`},
	}

	results := e.ExecuteBatch(context.Background(), calls)

	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d 个", len(results))
	}
	expected := []string{"A", "B", "C"}
	for i, want := range expected {
		if results[i].Output != want {
			t.Errorf("results[%d].Output = %q, want %q（结果顺序与输入不一致）", i, results[i].Output, want)
		}
		if results[i].IsError {
			t.Errorf("results[%d] 不应为错误: %s", i, results[i].Output)
		}
	}
}

// TestExecuteBatch_MixedSafeUnsafe 验证 ExecuteBatch 混合 safe/unsafe 工具时的分桶与执行时序：
// 2 个 safe 工具（各 100ms）应并发、1 个 unsafe 工具（50ms）在 safe 全部完成后串行执行，
// 总耗时应 ≈ max(safe)+unsafe = 150ms；若 safe 未并发则 ~250ms，据此区分。
func TestExecuteBatch_MixedSafeUnsafe(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	mocks := []batchMockTool{
		{name: "mix_safe1", concurrencySafe: true, latency: 100 * time.Millisecond, output: "S1"},
		{name: "mix_safe2", concurrencySafe: true, latency: 100 * time.Millisecond, output: "S2"},
		{name: "mix_unsafe1", concurrencySafe: false, latency: 50 * time.Millisecond, output: "U1"},
	}
	for _, m := range mocks {
		registerBatchMockTool(t, e, r, m)
	}

	calls := []Call{
		{ID: "1", Name: "mix_safe1", Input: `{}`},
		{ID: "2", Name: "mix_safe2", Input: `{}`},
		{ID: "3", Name: "mix_unsafe1", Input: `{}`},
	}

	start := time.Now()
	results := e.ExecuteBatch(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d 个", len(results))
	}
	expected := []string{"S1", "S2", "U1"}
	for i, want := range expected {
		if results[i].Output != want {
			t.Errorf("results[%d].Output = %q, want %q", i, results[i].Output, want)
		}
		if results[i].IsError {
			t.Errorf("results[%d] 不应为错误: %s", i, results[i].Output)
		}
	}
	// safe 并行（~100ms）+ unsafe 串行（~50ms）≈ 150ms；全串行 ~250ms；200ms 阈值区分两者
	if elapsed >= 200*time.Millisecond {
		t.Errorf("混合执行耗时 %v 超过 200ms，safe 工具疑似未并行（safe 并行+unsafe 串行应 ~150ms）", elapsed)
	}
}

// TestExecuteBatch_ConcurrencyLimit 验证 ExecuteBatch 对 safe 工具的并发上限（maxConcurrentSafeTools=8）：
// 10 个 safe 工具各 sleep 50ms，受信号量限制分 2 批执行（8+2），总耗时应 >= ~100ms，
// 据此证明信号量确实限制了并发度而非全部 10 个同时执行（全并行则 ~50ms）。
func TestExecuteBatch_ConcurrencyLimit(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 注册单个 safe 工具，调用 10 次以探测并发上限
	registerBatchMockTool(t, e, r, batchMockTool{
		name:            "limit_safe",
		concurrencySafe: true,
		latency:         50 * time.Millisecond,
		output:          "OK",
	})

	calls := make([]Call, 10)
	for i := range calls {
		calls[i] = Call{ID: fmt.Sprintf("%d", i), Name: "limit_safe", Input: `{}`}
	}

	start := time.Now()
	results := e.ExecuteBatch(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 10 {
		t.Fatalf("期望 10 个结果，得到 %d 个", len(results))
	}
	for i, res := range results {
		if res.IsError {
			t.Errorf("results[%d] 不应为错误: %s", i, res.Output)
		}
		if res.Output != "OK" {
			t.Errorf("results[%d].Output = %q, want %q", i, res.Output, "OK")
		}
	}
	// 并发上限 8：10 个任务分 2 批（8+2），每批 50ms，总耗时应 >= ~100ms
	// 若无并发上限（全并行）则 ~50ms；>= 90ms 可区分（留 10ms 误差给调度抖动）
	if elapsed < 90*time.Millisecond {
		t.Errorf("并发上限执行耗时 %v 不足 90ms，疑似并发上限未生效（2 批应 ~100ms）", elapsed)
	}
}
