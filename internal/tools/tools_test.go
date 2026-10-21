package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}

	all := r.All()
	if len(all) == 0 {
		t.Error("Registry should have builtin tools")
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()

	spec, ok := r.Get("bash")
	if !ok {
		t.Error("bash tool should exist")
	}
	if spec.Name != "bash" {
		t.Errorf("Name = %s, want bash", spec.Name)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("nonexistent tool should not exist")
	}
}

func TestRegistryDefinitions(t *testing.T) {
	r := NewRegistry()
	defs := r.Definitions()
	if len(defs) == 0 {
		t.Error("Definitions should not be empty")
	}

	for _, def := range defs {
		if def["name"] == nil {
			t.Error("Definition should have name")
		}
	}
}

func TestValidateInput(t *testing.T) {
	err := ValidateInput("bash", "")
	if err == nil {
		t.Error("空输入should return error")
	}

	err = ValidateInput("bash", "not json")
	if err == nil {
		t.Error("非 JSON 输入should return error")
	}

	err = ValidateInput("bash", "[]")
	if err == nil {
		t.Error("数组输入should return error")
	}

	err = ValidateInput("bash", `{"command":"ls"}`)
	if err != nil {
		t.Errorf("有效 JSON 对象不should return error: %v", err)
	}
}

func TestNormalizeToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"read", "read_file"},
		{"write", "write_file"},
		{"edit", "edit_file"},
		{"glob", "glob_search"},
		{"grep", "grep_search"},
		{"search", "WebSearch"},
		{"fetch", "WebFetch"},
		{"bash", "bash"},
	}

	for _, tt := range tests {
		if got := NormalizeToolName(tt.input); got != tt.want {
			t.Errorf("NormalizeToolName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsConcurrencySafe(t *testing.T) {
	r := NewRegistry()
	if !r.IsConcurrencySafe("read_file") {
		t.Error("read_file should be concurrency safe")
	}
	if r.IsConcurrencySafe("bash") {
		t.Error("bash should not be concurrency safe")
	}
}

func TestFormatToolResult(t *testing.T) {
	result := Result{ID: "1", Name: "bash", Output: "hello", IsError: false}
	formatted := FormatToolResultStruct(result)
	if !strings.Contains(formatted, "hello") {
		t.Errorf("FormatToolResultStruct = %s, want to contain hello", formatted)
	}
	if !strings.Contains(formatted, "bash") {
		t.Errorf("FormatToolResultStruct = %s, want to contain tool name", formatted)
	}

	errResult := Result{ID: "1", Name: "bash", Output: "failed", IsError: true}
	formatted = FormatToolResultStruct(errResult)
	if !strings.Contains(formatted, "❌") {
		t.Error("错误结果should contain错误标识")
	}
}

// TestFormatToolResultFold 验证工具结果 head+tail 行级折叠（对标同类 CLI output_lines）。
// REF-外部: 同类 CLI
func TestFormatToolResultFold(t *testing.T) {
	// 1. 短输出（5 行）不折叠
	shortOutput := "line1\nline2\nline3\nline4\nline5"
	shortResult := Result{ID: "1", Name: "cat", Output: shortOutput, IsError: false}
	shortFormatted := FormatToolResultStruct(shortResult)
	if strings.Contains(shortFormatted, "… +") {
		t.Errorf("短输出不应折叠，got: %s", shortFormatted)
	}
	if !strings.Contains(shortFormatted, "line5") {
		t.Errorf("短输出应包含最后一行 line5，got: %s", shortFormatted)
	}

	// 2. 长输出（20 行）折叠为 head 5 + 省略号 + tail 5
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	longOutput := strings.Join(lines, "\n")
	longResult := Result{ID: "2", Name: "grep", Output: longOutput, IsError: false}
	longFormatted := FormatToolResultStruct(longResult)
	// 应包含 head（line1-5）
	if !strings.Contains(longFormatted, "line1") || !strings.Contains(longFormatted, "line5") {
		t.Errorf("长输出应包含 head line1-5，got: %s", longFormatted)
	}
	// 应包含 tail（line16-20）
	if !strings.Contains(longFormatted, "line16") || !strings.Contains(longFormatted, "line20") {
		t.Errorf("长输出应包含 tail line16-20，got: %s", longFormatted)
	}
	// 应包含省略号提示（含 /tool）
	if !strings.Contains(longFormatted, "… +") {
		t.Errorf("长输出应包含省略号提示，got: %s", longFormatted)
	}
	if !strings.Contains(longFormatted, "/tool") {
		t.Errorf("省略号应提示 /tool 命令，got: %s", longFormatted)
	}
	// 不应包含中间行（line6-15）
	if strings.Contains(longFormatted, "line6") {
		t.Errorf("长输出不应包含中间行 line6，got: %s", longFormatted)
	}

	// 3. 错误输出不折叠
	errResult := Result{ID: "3", Name: "bash", Output: longOutput, IsError: true}
	errFormatted := FormatToolResultStruct(errResult)
	if strings.Contains(errFormatted, "… +") {
		t.Errorf("错误输出不应折叠，got: %s", errFormatted)
	}
	if !strings.Contains(errFormatted, "❌") {
		t.Errorf("错误输出应包含 ❌，got: %s", errFormatted)
	}

	// 4. \r\n 归一化（Windows 兼容，子审查建议1）
	crlfOutput := "line1\r\nline2\r\nline3\r\nline4\r\nline5\r\nline6\r\nline7\r\nline8\r\nline9\r\nline10\r\nline11\r\nline12"
	crlfResult := Result{ID: "4", Name: "cat", Output: crlfOutput, IsError: false}
	crlfFormatted := FormatToolResultStruct(crlfResult)
	if strings.Contains(crlfFormatted, "\r") {
		t.Errorf("\\r\\n 应归一化为 \\n，不应残留 \\r，got: %s", crlfFormatted)
	}
	if !strings.Contains(crlfFormatted, "… +") {
		t.Errorf("12 行应折叠，got: %s", crlfFormatted)
	}

	// 5. 恰好 10 行（Head+Tail）不折叠
	exactOutput := strings.Join([]string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"}, "\n")
	exactResult := Result{ID: "5", Name: "cat", Output: exactOutput, IsError: false}
	exactFormatted := FormatToolResultStruct(exactResult)
	if strings.Contains(exactFormatted, "… +") {
		t.Errorf("恰好 10 行（Head+Tail）不应折叠，got: %s", exactFormatted)
	}

	// 6. 11 行边界折叠（子审查建1补测：实施阈值 > Head+Tail，11 行应折叠）
	// 11 行 = head 5 + 省略 1 行 + tail 5，省略号提示 +1 lines
	elevenLines := strings.Join([]string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11"}, "\n")
	elevenResult := Result{ID: "6", Name: "cat", Output: elevenLines, IsError: false}
	elevenFormatted := FormatToolResultStruct(elevenResult)
	if !strings.Contains(elevenFormatted, "… +1 lines") {
		t.Errorf("11 行应折叠为 head5 + 省略1行 + tail5，got: %s", elevenFormatted)
	}
	if !strings.Contains(elevenFormatted, "l1") || !strings.Contains(elevenFormatted, "l5") {
		t.Errorf("11 行折叠应保留 head l1-l5，got: %s", elevenFormatted)
	}
	if !strings.Contains(elevenFormatted, "l7") || !strings.Contains(elevenFormatted, "l11") {
		t.Errorf("11 行折叠应保留 tail l7-l11，got: %s", elevenFormatted)
	}
	if strings.Contains(elevenFormatted, "l6") {
		t.Errorf("11 行折叠应丢弃中间行 l6，got: %s", elevenFormatted)
	}

	// 7. 空输出不折叠（反例2，子审查维度5）
	emptyResult := Result{ID: "7", Name: "cat", Output: "", IsError: false}
	emptyFormatted := FormatToolResultStruct(emptyResult)
	if strings.Contains(emptyFormatted, "… +") {
		t.Errorf("空输出不应折叠，got: %s", emptyFormatted)
	}

	// 8. 单行输出不折叠（反例3，子审查维度5）
	singleResult := Result{ID: "8", Name: "echo", Output: "only one line", IsError: false}
	singleFormatted := FormatToolResultStruct(singleResult)
	if strings.Contains(singleFormatted, "… +") {
		t.Errorf("单行输出不应折叠，got: %s", singleFormatted)
	}
}

func TestFormatToolCall(t *testing.T) {
	call := Call{ID: "1", Name: "bash", Input: `{"command":"ls"}`}
	formatted := FormatToolCall(call)
	if formatted == "" {
		t.Error("FormatToolCall should not be empty")
	}
}

func TestExecutorExecute(t *testing.T) {
	r := NewRegistry()
	e := NewExecutor(r, t.TempDir())

	t.Run("未知工具", func(t *testing.T) {
		result := e.Execute(context.Background(), Call{
			ID: "1", Name: "nonexistent", Input: `{}`,
		})
		if !result.IsError {
			t.Error("未知工具should return error")
		}
	})

	t.Run("无效输入", func(t *testing.T) {
		result := e.Execute(context.Background(), Call{
			ID: "1", Name: "bash", Input: "not json",
		})
		if !result.IsError {
			t.Error("无效输入should return error")
		}
	})

	t.Run("read_file 不存在的文件", func(t *testing.T) {
		result := e.Execute(context.Background(), Call{
			ID: "1", Name: "read_file", Input: `{"path":"/nonexistent/file.txt"}`,
		})
		if !result.IsError {
			t.Error("读取不存在的文件should return error")
		}
	})
}

func TestExecutorWriteAndRead(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	testFile := tmpDir + "/test.txt"
	writeResult := e.Execute(context.Background(), Call{
		ID: "1", Name: "write_file",
		Input: mustMarshal(map[string]string{"path": testFile, "content": "hello world"}),
	})
	if writeResult.IsError {
		t.Fatalf("写入文件失败: %s", writeResult.Output)
	}

	readResult := e.Execute(context.Background(), Call{
		ID: "2", Name: "read_file",
		Input: mustMarshal(map[string]string{"path": testFile}),
	})
	if readResult.IsError {
		t.Fatalf("读取文件失败: %s", readResult.Output)
	}
}

func mustMarshal(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// TestRegistry_RegisterAndGet test passed内置 map 注册工具并获取正确的 Spec，未注册工具返回 false
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()

	// 已注册的内置工具应能获取
	spec, ok := r.Get("bash")
	if !ok {
		t.Error("内置工具 bash 应存在")
	}
	if spec.Name != "bash" {
		t.Errorf("spec.Name = %q, want %q", spec.Name, "bash")
	}
	// L1（2026-09-14 用户拍板）：bash 的能力需求是「在工作区跑命令」= WorkspaceWrite。
	// 此前声明 LevelDangerFullAccess 是把风险维度误当能力档；一旦声明成为运行时唯一真相源，
	// bash 会在默认档（workspace-write）被直接拒绝。危险命令由二次确认护栏单独承担。
	if spec.RequiredPermission != LevelWorkspaceWrite {
		t.Errorf("bash RequiredPermission = %v, want %v", spec.RequiredPermission, LevelWorkspaceWrite)
	}

	// 未注册的工具should return false
	_, ok = r.Get("nonexistent_tool_xyz")
	if ok {
		t.Error("未注册的工具should return false")
	}
}

// TestRegistry_BuiltinTools 测试 All() 返回的列表包含所有内置工具
func TestRegistry_BuiltinTools(t *testing.T) {
	r := NewRegistry()
	all := r.All()

	// 内置工具名称列表（与 registerBuiltinTools 中注册的一致）
	expectedTools := []string{
		"bash", "read_file", "write_file", "edit_file",
		"glob_search", "grep_search", "WebFetch", "WebSearch",
		"Agent", "TodoWrite", "AskUser", "Skill",
		"McpListTools", "McpCallTool", "McpListPrompts", "McpGetPrompt",
		"McpListResources", "McpReadResource",
		"LspDefinition", "LspReferences", "LspDiagnostics",
		"NotebookEdit", "Monitor",
	}

	allNames := make(map[string]bool)
	for _, spec := range all {
		allNames[spec.Name] = true
	}

	for _, name := range expectedTools {
		if !allNames[name] {
			t.Errorf("内置工具 %q 未在 All() 中找到", name)
		}
	}

	if len(all) < len(expectedTools) {
		t.Errorf("All() 返回 %d 个工具，expected至少 %d 个", len(all), len(expectedTools))
	}
}

// TestRegistry_MCPPluginPriority 测试同名工具在 builtin/plugin/mcp 中的优先级：Get 返回 builtin 版本
func TestRegistry_MCPPluginPriority(t *testing.T) {
	r := NewRegistry()

	// 注册同名的 plugin 和 mcp 工具
	pluginSpec := Spec{Name: "bash", Description: "plugin bash"}
	mcpSpec := Spec{Name: "bash", Description: "mcp bash"}

	// bash 已在 builtin 中，RegisterPlugin 和 RegisterMCP 应报错
	err := r.RegisterPlugin(pluginSpec)
	if err == nil {
		t.Error("注册与 builtin 同名的 plugin 工具should return error")
	}

	err = r.RegisterMCP(mcpSpec)
	if err == nil {
		t.Error("注册与 builtin 同名的 MCP 工具should return error")
	}

	// Get should return builtin 版本
	spec, ok := r.Get("bash")
	if !ok {
		t.Error("bash 应存在")
	}
	if spec.Description == "plugin bash" || spec.Description == "mcp bash" {
		t.Error("Get should return builtin 版本，而非 plugin/mcp 版本")
	}
}

// TestRegistry_ConflictDetection 测试 RegisterMCP 注册与 builtin 同名的工具不会覆盖
func TestRegistry_ConflictDetection(t *testing.T) {
	r := NewRegistry()

	// 获取原始 bash 的描述
	originalSpec, _ := r.Get("bash")
	originalDesc := originalSpec.Description

	// 尝试用 MCP 注册同名工具
	err := r.RegisterMCP(Spec{
		Name:        "bash",
		Description: "malicious mcp bash",
	})
	if err == nil {
		t.Error("RegisterMCP 与 builtin 冲突时should return error")
	}

	// 验证原始工具未被覆盖
	spec, _ := r.Get("bash")
	if spec.Description != originalDesc {
		t.Error("MCP 注册冲突后，builtin 工具不应被覆盖")
	}
}

// TestFormatToolResultStringDefaultTruncation tests default truncation with 3000-char result
func TestFormatToolResultStringDefaultTruncation(t *testing.T) {
	// Reset default
	origMax := MaxToolResultLength
	MaxToolResultLength = 2000
	defer func() { MaxToolResultLength = origMax }()

	longResult := strings.Repeat("a", 3000)
	formatted := FormatToolResult(longResult, 0) // maxLen=0 uses default

	if len(formatted) > 2100 { // 2000 + truncation message
		t.Errorf("result should be truncated to ~2000 chars, got %d", len(formatted))
	}
	if !strings.Contains(formatted, "truncated") {
		t.Error("truncated result should contain truncation notice")
	}
	if !strings.Contains(formatted, "1000 characters") {
		t.Error("truncation notice should mention 1000 characters")
	}
}

// TestFormatToolResultStringCustomTruncation tests custom truncation via Config
func TestFormatToolResultStringCustomTruncation(t *testing.T) {
	r := NewRegistry()
	e := NewExecutor(r, t.TempDir())
	e.Config["tool_result_max_length"] = "5000"

	longResult := strings.Repeat("x", 6000)
	formatted := e.formatResult(longResult)

	if !strings.Contains(formatted, "truncated") {
		t.Error("result should be truncated with custom max length")
	}
	if !strings.Contains(formatted, "1000 characters") {
		t.Error("truncation notice should mention 1000 characters")
	}
}

// TestFormatToolResultStringNoTruncation tests disabling truncation with very large max
func TestFormatToolResultStringNoTruncation(t *testing.T) {
	r := NewRegistry()
	e := NewExecutor(r, t.TempDir())
	e.Config["tool_result_max_length"] = "999999"

	result := strings.Repeat("y", 3000)
	formatted := e.formatResult(result)

	if strings.Contains(formatted, "truncated") {
		t.Error("result should not be truncated with very large max length")
	}
	if formatted != result {
		t.Error("result should be returned as-is when max length is very large")
	}
}

// TestFormatToolResultStringEmpty tests empty string result
func TestFormatToolResultStringEmpty(t *testing.T) {
	formatted := FormatToolResult("", 0)
	if formatted != "" {
		t.Errorf("empty result should return empty string, got: %q", formatted)
	}
}

// TestExecutorFormatResult tests Executor.formatResult reads from Config
// 验证 Executor.formatResult 从 Config 读取截断长度配置。
// 注意：默认 MaxToolResultLength 为 50000（N16 修正，对齐业内既有实现
// DEFAULT_MAX_RESULT_SIZE_CHARS），所以测试输入需超过 50000 才会触发默认截断；
// 用小值（3000）不会触发默认截断，会被原样返回。
func TestExecutorFormatResult(t *testing.T) {
	r := NewRegistry()
	e := NewExecutor(r, t.TempDir())

	// No config set — uses default MaxToolResultLength (50000)
	// 输入超过默认 50000 才会被截断
	longResult := strings.Repeat("z", 60000)
	formatted := e.formatResult(longResult)
	if !strings.Contains(formatted, "truncated") {
		t.Error("should truncate with default max length (50000) when input exceeds it")
	}

	// 输入未超过默认值时不截断
	shortResult := strings.Repeat("z", 3000)
	formattedShort := e.formatResult(shortResult)
	if strings.Contains(formattedShort, "truncated") {
		t.Error("should not truncate when input is below default max length (50000)")
	}

	// Set config to a small value
	e.Config["tool_result_max_length"] = "100"
	formatted = e.formatResult(strings.Repeat("a", 200))
	if !strings.Contains(formatted, "truncated") {
		t.Error("should truncate with custom max length from Config")
	}
	if !strings.Contains(formatted, "100 characters") {
		t.Error("truncation notice should mention 100 characters")
	}
}
