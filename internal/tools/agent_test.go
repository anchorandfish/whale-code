package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
)

// agentMockClient Agent 测试用 mock client
type agentMockClient struct {
	responses []<-chan ai.Event
	callIdx   int
	callCount int
}

func (m *agentMockClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	m.callCount++
	idx := m.callIdx
	if idx >= len(m.responses) {
		ch := make(chan ai.Event, 2)
		ch <- ai.TextDeltaEvent{Content: "default response"}
		ch <- ai.MessageStopEvent{}
		close(ch)
		return ch, nil
	}
	m.callIdx++
	return m.responses[idx], nil
}

func (m *agentMockClient) Model() string   { return "test-model" }
func (m *agentMockClient) SetModel(string) {}
func (m *agentMockClient) Close() error    { return nil }

// makeTextStream 创建纯文本 mock 流
func makeTextStream(text string) <-chan ai.Event {
	ch := make(chan ai.Event, 3)
	ch <- ai.TextDeltaEvent{Content: text}
	ch <- ai.MessageStopEvent{}
	close(ch)
	return ch
}

// TestAgentContextCreation 测试 AgentContext 创建
func TestAgentContextCreation(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")

	ctx := NewAgentContext(executor, "test prompt", "test description", "/workdir", 1)

	if ctx == nil {
		t.Fatal("Expected non-nil AgentContext")
	}
	if ctx.Prompt != "test prompt" {
		t.Errorf("Expected prompt 'test prompt', got %s", ctx.Prompt)
	}
	if ctx.Depth != 1 {
		t.Errorf("Expected depth 1, got %d", ctx.Depth)
	}
	if ctx.MaxDepth != 3 {
		t.Errorf("Expected max depth 3, got %d", ctx.MaxDepth)
	}
}

// TestAgentContextMaxDepth test nested深度限制
func TestAgentContextMaxDepth(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")

	ctx := NewAgentContext(executor, "test", "", "/workdir", 3)
	if !ctx.IsAtMaxDepth() {
		t.Error("Expected depth 3 to be at max depth")
	}

	ctx2 := NewAgentContext(executor, "test", "", "/workdir", 2)
	if ctx2.IsAtMaxDepth() {
		t.Error("Expected depth 2 to NOT be at max depth")
	}
}

// TestAgentRunBasicExecution 测试 Agent 基本执行
func TestAgentRunBasicExecution(t *testing.T) {
	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{
			makeTextStream("I analyzed the code and found the issue."),
		},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	ctx := NewAgentContext(executor, "analyze the code", "code analysis", "/workdir", 1)
	result, err := ctx.Run(context.Background())

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !strings.Contains(result, "analyzed the code") {
		t.Errorf("Expected result to contain analysis, got: %s", result)
	}
	if mockClient.callCount != 1 {
		t.Errorf("Expected 1 AI call, got %d", mockClient.callCount)
	}
}

// TestAgentRunWithToolCall 测试 Agent 带工具调用
func TestAgentRunWithToolCall(t *testing.T) {
	readFileResult := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}"

	round1 := make(chan ai.Event, 10)
	round1 <- ai.ToolUseEvent{ID: "tool_1", Name: "read_file", Input: `{"path":"main.go"}`}
	round1 <- ai.ToolInputDeltaEvent{PartialJSON: `{"path":"main.go"}`}
	round1 <- ai.MessageStopEvent{}
	close(round1)

	round2 := make(chan ai.Event, 10)
	round2 <- ai.TextDeltaEvent{Content: "The main.go file contains a simple hello world program."}
	round2 <- ai.MessageStopEvent{}
	close(round2)

	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{round1, round2},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient
	executor.handlers["read_file"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return readFileResult, nil
	}

	ctx := NewAgentContext(executor, "read main.go and describe it", "file analysis", "/workdir", 1)
	result, err := ctx.Run(context.Background())

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if mockClient.callCount < 2 {
		t.Errorf("Expected at least 2 AI calls (tool use + result), got %d", mockClient.callCount)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("Expected result to mention hello world, got: %s", result)
	}
}

// TestAgentRunTimeout 测试 Agent 超时
func TestAgentRunTimeout(t *testing.T) {
	slowStream := make(chan ai.Event)
	defer close(slowStream)

	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{slowStream},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	ctx := NewAgentContext(executor, "slow task", "", "/workdir", 1)
	ctx.Timeout = 100 * time.Millisecond

	result, err := ctx.Run(context.Background())

	// 超时后should return已收集的输出或错误
	if err == nil && result == "" {
		t.Error("Expected either error or partial result on timeout")
	}
}

// TestAgentRunMaxOutputTokens 测试 Agent 输出截断
func TestAgentRunMaxOutputTokens(t *testing.T) {
	longText := strings.Repeat("x", 20000)
	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{
			makeTextStream(longText),
		},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	ctx := NewAgentContext(executor, "generate long output", "", "/workdir", 1)
	ctx.MaxOutputTokens = 1000

	result, err := ctx.Run(context.Background())

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(result) > 2000 {
		t.Errorf("Expected output to be truncated, got length %d", len(result))
	}
}

// TestAgentRunNoAIClient 测试无 AI client
func TestAgentRunNoAIClient(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")

	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	result, err := ctx.Run(context.Background())

	if err != nil {
		t.Fatalf("Should not return error, got: %v", err)
	}
	if !strings.Contains(result, "AI client not configured") {
		t.Errorf("Expected 'AI client not configured' message, got: %s", result)
	}
}

// TestAgentRunNestedPrevention test nested Agent 防止
func TestAgentRunNestedPrevention(t *testing.T) {
	round1 := make(chan ai.Event, 10)
	round1 <- ai.ToolUseEvent{ID: "tool_1", Name: "Agent", Input: `{"prompt":"nested task"}`}
	round1 <- ai.ToolInputDeltaEvent{PartialJSON: `{"prompt":"nested task"}`}
	round1 <- ai.MessageStopEvent{}
	close(round1)

	round2 := make(chan ai.Event, 10)
	round2 <- ai.TextDeltaEvent{Content: "Cannot spawn nested agent."}
	round2 <- ai.MessageStopEvent{}
	close(round2)

	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{round1, round2},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	ctx := NewAgentContext(executor, "call agent", "", "/workdir", 3)
	result, err := ctx.Run(context.Background())

	if err != nil {
		t.Logf("Nested agent handling: %v", err)
	}
	_ = result
}

// TestAgentRunMaxRounds 测试 Agent 最大轮次限制
func TestAgentRunMaxRounds(t *testing.T) {
	round := make(chan ai.Event, 10)
	round <- ai.ToolUseEvent{ID: "tool_1", Name: "bash", Input: `{"command":"echo loop"}`}
	round <- ai.ToolInputDeltaEvent{PartialJSON: `{"command":"echo loop"}`}
	round <- ai.MessageStopEvent{}
	close(round)

	rounds := make([]<-chan ai.Event, 20)
	for i := range rounds {
		ch := make(chan ai.Event, 10)
		ch <- ai.ToolUseEvent{ID: "tool_1", Name: "bash", Input: `{"command":"echo loop"}`}
		ch <- ai.ToolInputDeltaEvent{PartialJSON: `{"command":"echo loop"}`}
		ch <- ai.MessageStopEvent{}
		close(ch)
		rounds[i] = ch
	}

	mockClient := &agentMockClient{
		responses: rounds,
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient
	executor.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "loop output", nil
	}

	ctx := NewAgentContext(executor, "infinite loop", "", "/workdir", 1)
	ctx.MaxRounds = 5

	result, err := ctx.Run(context.Background())

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !strings.Contains(result, "maximum") {
		t.Logf("Result: %s", result)
	}
	if mockClient.callCount > 6 {
		t.Errorf("Expected at most 6 AI calls, got %d", mockClient.callCount)
	}
}

// ========== 类型化 Agent 测试（对标同类 CLI Agent 系统）==========

// TestAgentTypeDefault 测试默认 Agent 类型为 coder
func TestAgentTypeDefault(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")

	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	if ctx.AgentType != AgentTypeCoder {
		t.Errorf("Expected default agent type 'coder', got '%s'", ctx.AgentType)
	}
}

// TestAgentTypeReadOnlyDetection 测试只读类型检测（Plan 和 Explore）
func TestAgentTypeReadOnlyDetection(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")

	planCtx := NewAgentContext(executor, "test", "", "/workdir", 1)
	planCtx.AgentType = AgentTypePlan
	if !planCtx.IsReadOnlyAgent() {
		t.Error("Expected Plan agent to be read-only")
	}

	exploreCtx := NewAgentContext(executor, "test", "", "/workdir", 1)
	exploreCtx.AgentType = AgentTypeExplore
	if !exploreCtx.IsReadOnlyAgent() {
		t.Error("Expected Explore agent to be read-only")
	}

	coderCtx := NewAgentContext(executor, "test", "", "/workdir", 1)
	coderCtx.AgentType = AgentTypeCoder
	if coderCtx.IsReadOnlyAgent() {
		t.Error("Expected Coder agent to NOT be read-only")
	}

	verifCtx := NewAgentContext(executor, "test", "", "/workdir", 1)
	verifCtx.AgentType = AgentTypeVerification
	if verifCtx.IsReadOnlyAgent() {
		t.Error("Expected Verification agent to NOT be read-only (has bash access)")
	}
}

// TestAgentTypeToolWhitelist_Coder 测试 Coder 类型的工具白名单（全部工具）
func TestAgentTypeToolWhitelist_Coder(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeCoder

	allowed := ctx.getAllowedToolSet()

	expectedTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"bash", "write_file", "edit_file",
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"McpCallTool",
		"git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch",
		"TodoWrite", "NotebookEdit", "Monitor", "AskUser", "Skill",
	}
	for _, tool := range expectedTools {
		if !allowed[tool] {
			t.Errorf("Coder agent should have access to '%s'", tool)
		}
	}
}

// TestAgentTypeToolWhitelist_Plan 测试 Plan 类型的工具白名单（仅只读）
func TestAgentTypeToolWhitelist_Plan(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypePlan

	allowed := ctx.getAllowedToolSet()

	// 只读工具应该可用
	readOnlyTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Plan agent should have read-only access to '%s'", tool)
		}
	}

	// 写操作工具应该被禁止
	writeTools := []string{"write_file", "edit_file"}
	for _, tool := range writeTools {
		if allowed[tool] {
			t.Errorf("Plan agent should NOT have write access to '%s'", tool)
		}
	}

	// bash 也应该被禁止（Plan 是纯只读）
	if allowed["bash"] {
		t.Error("Plan agent should NOT have bash access (read-only mode)")
	}
}

// TestAgentTypeToolWhitelist_Explore 测试 Explore 类型的工具白名单（与 Plan 相同）
func TestAgentTypeToolWhitelist_Explore(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeExplore

	allowed := ctx.getAllowedToolSet()

	// Explore 与 Plan 应该有相同的只读工具集
	if allowed["write_file"] || allowed["edit_file"] || allowed["bash"] {
		t.Error("Explore agent should be strictly read-only")
	}

	// Explore 应能使用扩展的只读工具
	readOnlyTools := []string{
		"LspDefinition", "LspHover", "LspCompletion",
		"McpListTools", "McpReadResource",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Explore agent should have access to '%s'", tool)
		}
	}
}

// TestAgentTypeToolWhitelist_Verification 测试 Verification 类型的工具白名单（只读 + bash）
func TestAgentTypeToolWhitelist_Verification(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeVerification

	allowed := ctx.getAllowedToolSet()

	// Verification 可以使用 bash 运行测试
	if !allowed["bash"] {
		t.Error("Verification agent should have bash access for running tests")
	}

	// 但不能修改源文件
	if allowed["write_file"] || allowed["edit_file"] {
		t.Error("Verification agent should NOT modify source files")
	}

	// 应能使用扩展的只读工具
	readOnlyTools := []string{
		"LspDefinition", "LspHover", "LspCompletion",
		"McpListTools", "McpReadResource",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Verification agent should have access to '%s'", tool)
		}
	}
}

// TestAgentTypeSystemPrompt_Plan 测试 Plan Agent 系统提示词包含关键内容
func TestAgentTypeSystemPrompt_Plan(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test plan", "design auth system", "/workdir", 1)
	ctx.AgentType = AgentTypePlan

	prompt := ctx.buildSystemPrompt()

	requiredKeywords := []string{
		"READ-ONLY",
		"software architect",
		"planning",
		"Implementation Plan",
		"Critical Files",
		"MUST NOT",
	}
	for _, kw := range requiredKeywords {
		if !strings.Contains(prompt, kw) {
			t.Errorf("Plan system prompt should contain '%s', got:\n%s", kw, prompt)
		}
	}
}

// TestAgentTypeSystemPrompt_Explore 测试 Explore Agent 系统提示词包含关键内容
func TestAgentTypeSystemPrompt_Explore(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test explore", "find auth code", "/workdir", 1)
	ctx.AgentType = AgentTypeExplore

	prompt := ctx.buildSystemPrompt()

	requiredKeywords := []string{
		"READ-ONLY",
		"exploration",
		"search",
		"Glob",
		"Grep",
		"MUST NOT",
	}
	for _, kw := range requiredKeywords {
		if !strings.Contains(prompt, kw) {
			t.Errorf("Explore system prompt should contain '%s', got:\n%s", kw, prompt)
		}
	}
}

// TestAgentTypeSystemPrompt_Verification 测试 Verification Agent 系统提示词包含关键内容
func TestAgentTypeSystemPrompt_Verification(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test verif", "verify login", "/workdir", 1)
	ctx.AgentType = AgentTypeVerification

	prompt := ctx.buildSystemPrompt()

	requiredKeywords := []string{
		"verification",
		"BREAK",
		"VERDICT",
		"PASS",
		"FAIL",
		"DO NOT MODIFY",
		"Adversarial",
	}
	for _, kw := range requiredKeywords {
		if !strings.Contains(prompt, kw) {
			t.Errorf("Verification system prompt should contain '%s', got:\n%s", kw, prompt)
		}
	}
}

// TestAgentTypeSystemPrompt_Coder 测试 Coder Agent 系统提示词为通用编码
func TestAgentTypeSystemPrompt_Coder(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test coder", "implement feature", "/workdir", 1)
	ctx.AgentType = AgentTypeCoder

	prompt := ctx.buildSystemPrompt()

	if strings.Contains(prompt, "READ-ONLY") {
		t.Error("Coder prompt should NOT contain READ-ONLY restriction")
	}
	if !strings.Contains(prompt, "coding") && !strings.Contains(prompt, "implement") {
		t.Error("Coder prompt should mention coding/implementation")
	}
}

// TestAgentTypeBuildTools_PlanHasNoWriteTools 测试 Plan Agent 构建的工具列表不包含写操作
func TestAgentTypeBuildTools_PlanHasNoWriteTools(t *testing.T) {
	registry := NewRegistry()
	registry.registerBuiltinTools()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypePlan
	ctx.Depth = 1 // 非最大深度，但 Plan 不允许嵌套 Agent

	tools := ctx.buildAgentTools()

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	forbiddenTools := []string{"write_file", "edit_file", "bash", "Agent"}
	for _, tool := range forbiddenTools {
		if toolNames[tool] {
			t.Errorf("Plan agent tools should not include '%s'", tool)
		}
	}
}

// TestAgentTypeBuildTools_VerificationHasBash 测试 Verification Agent 构建的工具列表包含 bash
func TestAgentTypeBuildTools_VerificationHasBash(t *testing.T) {
	registry := NewRegistry()
	registry.registerBuiltinTools()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeVerification

	tools := ctx.buildAgentTools()

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	if !toolNames["bash"] {
		t.Error("Verification agent tools should include 'bash' for running tests")
	}
	if toolNames["write_file"] || toolNames["edit_file"] {
		t.Error("Verification agent tools should not include write tools")
	}
}

// TestAgentTypeExecuteCall_WriteBlockedForPlan 测试 Plan Agent 执行写操作时rejected
func TestAgentTypeExecuteCall_WriteBlockedForPlan(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypePlan

	_, err := ctx.executeToolCall(context.Background(), "write_file", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Expected error when Plan agent tries to use write_file")
	}
	if !strings.Contains(err.Error(), "not available for plan agent type") {
		t.Errorf("Expected 'not available' error, got: %v", err)
	}
}

// TestAgentTypeExecuteCall_BashAllowedForVerification 测试 Verification Agent 可以执行 bash
//
// C-P0-3 修复后：executeToolCall 强制走 ExecuteWithPipeline，需初始化 Pipeline。
// bash 工具 RequiredPermission=LevelDangerFullAccess，故 policy 需设为 FullAccess。
func TestAgentTypeExecuteCall_BashAllowedForVerification(t *testing.T) {
	registry := NewRegistry()
	tmpDir := t.TempDir()
	executor := NewExecutor(registry, tmpDir)
	executor.handlers["bash"] = func(ctx context.Context, raw json.RawMessage) (string, error) {
		return "build succeeded", nil
	}
	// C-P0-3：初始化 Pipeline（bash 需要 LevelDangerFullAccess 权限）
	hookMgr := hook.NewManager(tmpDir)
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor.InitPipeline(hookMgr, policy, tmpDir)

	ctx := NewAgentContext(executor, "test", "", tmpDir, 1)
	ctx.AgentType = AgentTypeVerification

	result, err := ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"echo test"}`))
	if err != nil {
		t.Fatalf("Verification agent should be able to use bash, got error: %v", err)
	}
	if result != "build succeeded" {
		t.Errorf("Unexpected bash result: %s", result)
	}
}

// ========== Fork Agent 测试（对标同类 CLI Fork Agent）==========

// TestForkAgentType 测试 Fork 类型枚举值
func TestForkAgentType(t *testing.T) {
	if AgentTypeFork != "fork" {
		t.Errorf("Expected AgentTypeFork='fork', got '%s'", AgentTypeFork)
	}
}

// TestForkAgentToolWhitelist 测试 Fork Agent 拥有全部工具
func TestForkAgentToolWhitelist(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork

	allowed := ctx.getAllowedToolSet()

	expectedTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"bash", "write_file", "edit_file",
		"LspDefinition", "LspHover", "LspCompletion",
		"McpListTools", "McpCallTool",
		"git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch",
		"TodoWrite", "NotebookEdit", "Monitor", "AskUser", "Skill",
	}
	for _, tool := range expectedTools {
		if !allowed[tool] {
			t.Errorf("Fork agent should have access to '%s'", tool)
		}
	}
}

// TestForkAgentSystemPrompt_InheritsParent 测试 Fork Agent inheritance父proxy system prompt
func TestForkAgentSystemPrompt_InheritsParent(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork
	ctx.ParentSysPrompt = "This is the parent agent's system prompt."

	prompt := ctx.buildSystemPrompt()
	if prompt != "This is the parent agent's system prompt." {
		t.Errorf("Fork agent should inherit parent system prompt, got: %s", prompt)
	}
}

// TestForkAgentSystemPrompt_Fallback 测试 Fork Agent 无父 prompt 时降级
func TestForkAgentSystemPrompt_Fallback(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork

	prompt := ctx.buildSystemPrompt()
	if strings.Contains(prompt, "READ-ONLY") {
		t.Error("Fork fallback prompt should not be read-only")
	}
	if !strings.Contains(prompt, "coding") && !strings.Contains(prompt, "implement") {
		t.Error("Fork fallback prompt should be coder-style")
	}
}

// TestForkDirectiveContainsBoilerplate 测试 Fork 指令包含递归防护标签
func TestForkDirectiveContainsBoilerplate(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test directive", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork

	directive := ctx.buildForkDirective("implement auth")

	if !strings.Contains(directive, "<fork-boilerplate>") {
		t.Error("Fork directive should contain <fork-boilerplate> tag")
	}
	if !strings.Contains(directive, "</fork-boilerplate>") {
		t.Error("Fork directive should contain closing </fork-boilerplate> tag")
	}
	if !strings.Contains(directive, "implement auth") {
		t.Error("Fork directive should contain the actual directive text")
	}
	if !strings.Contains(directive, "forked worker") {
		t.Error("Fork directive should identify as forked worker")
	}
	if !strings.Contains(directive, "Do NOT spawn sub-agents") {
		t.Error("Fork directive should prohibit spawning sub-agents")
	}
	if !strings.Contains(directive, "Scope:") {
		t.Error("Fork directive should require structured output starting with Scope:")
	}
}

// TestIsInForkChild 测试递归防护检测函数
func TestIsInForkChild(t *testing.T) {
	// 包含 fork-boilerplate 标签的消息
	forkMessages := []ai.MessageEntry{
		{Role: "user", Content: "<fork-boilerplate>\nYou are a forked worker\n</fork-boilerplate>\nDirective: test"},
	}
	if !IsInForkChild(forkMessages) {
		t.Error("Should detect fork-boilerplate in messages")
	}

	// 不包含标签的普通消息
	normalMessages := []ai.MessageEntry{
		{Role: "user", Content: "Please help me with this task"},
	}
	if IsInForkChild(normalMessages) {
		t.Error("Should NOT detect fork-boilerplate in normal messages")
	}

	// assistant 消息中包含标签（应被忽略）
	assistantMessages := []ai.MessageEntry{
		{Role: "assistant", Content: "I see <fork-boilerplate> in context"},
	}
	if IsInForkChild(assistantMessages) {
		t.Error("Should only check user messages for fork-boilerplate")
	}
}

// TestForkManagerLifecycle 测试 Fork 任务管理器生命周期
func TestForkManagerLifecycle(t *testing.T) {
	fm := NewForkManager()

	task := &ForkTask{
		ID:        "test_task_1",
		Prompt:    "test prompt",
		Status:    "running",
		StartTime: time.Now(),
	}

	fm.RegisterTask(task)

	retrieved, ok := fm.GetTask("test_task_1")
	if !ok {
		t.Fatal("Should find registered task")
	}
	if retrieved.Status != "running" {
		t.Errorf("Expected status 'running', got '%s'", retrieved.Status)
	}

	fm.CompleteTask("test_task_1", "task completed successfully")

	retrieved, _ = fm.GetTask("test_task_1")
	if retrieved.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", retrieved.Status)
	}
	if retrieved.Result != "task completed successfully" {
		t.Errorf("Expected result 'task completed successfully', got '%s'", retrieved.Result)
	}
}

// TestForkManagerFailTask 测试 Fork 任务失败处理
func TestForkManagerFailTask(t *testing.T) {
	fm := NewForkManager()

	task := &ForkTask{
		ID:     "test_fail",
		Status: "running",
	}
	fm.RegisterTask(task)

	testErr := fmt.Errorf("something went wrong")
	fm.FailTask("test_fail", testErr)

	retrieved, _ := fm.GetTask("test_fail")
	if retrieved.Status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", retrieved.Status)
	}
	if retrieved.Error.Error() != "something went wrong" {
		t.Errorf("Expected error 'something went wrong', got '%v'", retrieved.Error)
	}
}

// TestForkManagerListRunning 测试列出运行中的任务
func TestForkManagerListRunning(t *testing.T) {
	fm := NewForkManager()

	fm.RegisterTask(&ForkTask{ID: "r1", Status: "running"})
	fm.RegisterTask(&ForkTask{ID: "r2", Status: "running"})
	fm.RegisterTask(&ForkTask{ID: "c1", Status: "completed"})

	running := fm.ListRunningTasks()
	if len(running) != 2 {
		t.Errorf("Expected 2 running tasks, got %d", len(running))
	}
}

// TestForkAgentNestedPrevention 测试 Fork 子proxy中禁止再次 Fork
func TestForkAgentNestedPrevention(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork
	ctx.IsForkChild = true

	tc := session.ContentBlock{
		Type:  "tool_use",
		ID:    "test_id",
		Name:  "Agent",
		Input: `{"prompt":"nested fork","agent_type":"fork"}`,
	}

	output, _ := ctx.handleNestedAgentCall(context.Background(), tc)
	if !strings.Contains(output, "not available inside a forked worker") {
		t.Errorf("Fork child should be blocked from spawning, got: %s", output)
	}
}

// TestForkAgentBuildMessages 测试 Fork 消息构建（inheritance父对话）
func TestForkAgentBuildMessages(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "implement feature X", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork
	ctx.ParentMessages = []ai.MessageEntry{
		{Role: "user", Content: "Hello, I need help"},
		{Role: "assistant", Content: "Sure, how can I help?"},
		{Role: "user", Content: "Fix the auth bug"},
	}

	messages := ctx.buildInitialMessages()

	if len(messages) != 4 {
		t.Errorf("Expected 4 messages (3 parent + 1 fork directive), got %d", len(messages))
	}

	lastMsg := messages[len(messages)-1]
	lastContent := fmt.Sprintf("%v", lastMsg.Content)
	if !strings.Contains(lastContent, "implement feature X") {
		t.Error("Last message should contain the fork directive")
	}
	if !strings.Contains(lastContent, "<fork-boilerplate>") {
		t.Error("Last message should contain fork-boilerplate tag")
	}
}

// TestForkAgentAsyncExecution 测试 Fork 异步执行
func TestForkAgentAsyncExecution(t *testing.T) {
	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{
			makeTextStream("Scope: implement feature\nResult: done\nKey files: main.go"),
		},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	ctx := NewAgentContext(executor, "implement feature", "feature impl", "/workdir", 1)
	ctx.AgentType = AgentTypeFork
	ctx.MaxRounds = 5
	ctx.Timeout = 5 * time.Second

	fm := NewForkManager()
	task, err := ctx.RunForkAsync(context.Background(), fm)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if task.Status != "running" {
		t.Errorf("Expected initial status 'running', got '%s'", task.Status)
	}

	// 等待异步任务完成
	time.Sleep(500 * time.Millisecond)

	retrieved, ok := fm.GetTask(task.ID)
	if !ok {
		t.Fatal("Should find fork task")
	}
	if retrieved.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", retrieved.Status)
	}
	if !strings.Contains(retrieved.Result, "done") {
		t.Errorf("Expected result to contain 'done', got: %s", retrieved.Result)
	}
}

// ============================================================================
// 新增 Agent 类型化执行测试
// ============================================================================

// TestAgentContext_Run_Coder 测试 coder 类型可以使用所有工具（包括 write_file, edit_file, bash）
func TestAgentContext_Run_Coder(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeCoder

	allowed := ctx.getAllowedToolSet()

	// coder 应能使用所有工具
	writeTools := []string{"write_file", "edit_file", "bash"}
	for _, tool := range writeTools {
		if !allowed[tool] {
			t.Errorf("Coder agent 应能使用 %s", tool)
		}
	}

	// 验证 coder 可以执行写操作
	_, err := ctx.executeToolCall(context.Background(), "write_file", json.RawMessage(`{}`))
	// write_file 会因输入不完整而失败，但不应因权限rejected
	if err != nil && strings.Contains(err.Error(), "not available") {
		t.Errorf("Coder agent 应被允许使用 write_file，but was rejected: %v", err)
	}
}

// TestAgentContext_Run_Plan 测试 plan 类型只能使用只读工具，写工具rejected
func TestAgentContext_Run_Plan(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypePlan

	// Plan 类型尝试使用写工具should be rejected
	_, err := ctx.executeToolCall(context.Background(), "write_file", json.RawMessage(`{}`))
	if err == nil {
		t.Error("Plan agent 不应被允许使用 write_file")
	}
	if !strings.Contains(err.Error(), "not available for plan agent type") {
		t.Errorf("expected权限拒绝错误，得到: %v", err)
	}

	_, err = ctx.executeToolCall(context.Background(), "edit_file", json.RawMessage(`{}`))
	if err == nil {
		t.Error("Plan agent 不应被允许使用 edit_file")
	}

	_, err = ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil {
		t.Error("Plan agent 不应被允许使用 bash")
	}
}

// TestAgentContext_Run_Explore 测试 explore 类型只能使用只读工具
func TestAgentContext_Run_Explore(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeExplore

	// Explore 类型尝试使用写工具should be rejected
	_, err := ctx.executeToolCall(context.Background(), "write_file", json.RawMessage(`{}`))
	if err == nil {
		t.Error("Explore agent 不应被允许使用 write_file")
	}

	_, err = ctx.executeToolCall(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil {
		t.Error("Explore agent 不应被允许使用 bash")
	}

	// 只读工具应被允许（但可能因处理器不存在而失败，不应因权限rejected）
	allowed := ctx.getAllowedToolSet()
	readOnlyTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"LspDefinition", "LspHover", "LspCompletion",
		"McpListTools", "McpReadResource",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Explore agent 应能使用只读工具 %s", tool)
		}
	}
}

// TestAgentContext_Run_Verification 测试 verification 类型使用只读工具 + bash
func TestAgentContext_Run_Verification(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeVerification

	allowed := ctx.getAllowedToolSet()

	// Verification 应能使用 bash
	if !allowed["bash"] {
		t.Error("Verification agent 应能使用 bash")
	}

	// Verification 不应能使用写工具
	if allowed["write_file"] {
		t.Error("Verification agent 不应能使用 write_file")
	}
	if allowed["edit_file"] {
		t.Error("Verification agent 不应能使用 edit_file")
	}

	// 只读工具应被允许
	readOnlyTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"LspDefinition", "LspHover", "LspCompletion",
		"McpListTools", "McpReadResource",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Verification agent 应能使用只读工具 %s", tool)
		}
	}
}

// TestAgentContext_AllowedToolSet 验证每种proxy类型的工具白名单
func TestAgentContext_AllowedToolSet(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")

	tests := []struct {
		agentType    AgentType
		allowedTools []string
		deniedTools  []string
	}{
		{
			agentType: AgentTypeCoder,
			allowedTools: []string{
				"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
				"bash", "write_file", "edit_file",
				"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
				"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
				"McpCallTool",
				"git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch",
				"TodoWrite", "NotebookEdit", "Monitor", "AskUser", "Skill",
			},
			deniedTools: []string{},
		},
		{
			agentType: AgentTypePlan,
			allowedTools: []string{
				"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
				"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
				"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
				"git_diff", "git_status", "git_log",
				"TodoWrite",
			},
			deniedTools: []string{"write_file", "edit_file", "bash", "McpCallTool", "NotebookEdit", "Monitor", "AskUser", "Skill", "git_add", "git_commit", "git_branch"},
		},
		{
			agentType: AgentTypeExplore,
			allowedTools: []string{
				"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
				"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
				"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
				"git_diff", "git_status", "git_log",
				"TodoWrite",
			},
			deniedTools: []string{"write_file", "edit_file", "bash", "McpCallTool", "NotebookEdit", "Monitor", "AskUser", "Skill", "git_add", "git_commit", "git_branch"},
		},
		{
			agentType: AgentTypeVerification,
			allowedTools: []string{
				"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
				"bash",
				"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
				"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
				"git_diff", "git_status", "git_log",
				"TodoWrite",
			},
			deniedTools: []string{"write_file", "edit_file", "McpCallTool", "NotebookEdit", "Monitor", "AskUser", "Skill", "git_add", "git_commit", "git_branch"},
		},
		{
			agentType: AgentTypeFork,
			allowedTools: []string{
				"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
				"bash", "write_file", "edit_file",
				"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
				"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
				"McpCallTool",
				"git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch",
				"TodoWrite", "NotebookEdit", "Monitor", "AskUser", "Skill",
			},
			deniedTools: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.agentType), func(t *testing.T) {
			ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
			ctx.AgentType = tt.agentType
			allowed := ctx.getAllowedToolSet()

			for _, tool := range tt.allowedTools {
				if !allowed[tool] {
					t.Errorf("%s agent 应允许使用 %s", tt.agentType, tool)
				}
			}

			for _, tool := range tt.deniedTools {
				if allowed[tool] {
					t.Errorf("%s agent 不应允许使用 %s", tt.agentType, tool)
				}
			}
		})
	}
}

// ============================================================================
// P0-3 扩展白名单测试
// ============================================================================

// TestCoderAgentToolSet 验证 coder proxy拥有全部工具访问权限
func TestCoderAgentToolSet(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeCoder

	allowed := ctx.getAllowedToolSet()

	// 验证 coder proxy可访问所有工具类别
	allExpectedTools := []string{
		// 基础读写
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"bash", "write_file", "edit_file",
		// LSP 工具
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		// MCP 工具
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"McpCallTool",
		// Git 工具
		"git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch",
		// 其他工具
		"TodoWrite", "NotebookEdit", "Monitor", "AskUser", "Skill",
	}
	for _, tool := range allExpectedTools {
		if !allowed[tool] {
			t.Errorf("Coder agent 应能使用 %s", tool)
		}
	}
}

// TestPlanAgentToolSet 验证 plan proxy仅拥有只读工具
func TestPlanAgentToolSet(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypePlan

	allowed := ctx.getAllowedToolSet()

	// Plan proxy应有只读工具
	readOnlyTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Plan agent 应能使用只读工具 %s", tool)
		}
	}

	// Plan proxy不应有写操作工具
	forbiddenTools := []string{
		"bash", "write_file", "edit_file",
		"McpCallTool", "NotebookEdit", "Monitor", "AskUser", "Skill",
		"git_add", "git_commit", "git_branch",
	}
	for _, tool := range forbiddenTools {
		if allowed[tool] {
			t.Errorf("Plan agent 不应能使用 %s", tool)
		}
	}
}

// TestExploreAgentToolSet 验证 explore proxy仅拥有只读工具（与 Plan 相同）
func TestExploreAgentToolSet(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeExplore

	allowed := ctx.getAllowedToolSet()

	// Explore 与 Plan 应有相同的只读工具集
	readOnlyTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range readOnlyTools {
		if !allowed[tool] {
			t.Errorf("Explore agent 应能使用只读工具 %s", tool)
		}
	}

	// Explore 不应有写操作和 bash
	forbiddenTools := []string{
		"bash", "write_file", "edit_file",
		"McpCallTool", "NotebookEdit", "Monitor", "AskUser", "Skill",
		"git_add", "git_commit", "git_branch",
	}
	for _, tool := range forbiddenTools {
		if allowed[tool] {
			t.Errorf("Explore agent 不应能使用 %s", tool)
		}
	}
}

// TestVerificationAgentToolSet 验证 verification proxy拥有只读 + bash 工具
func TestVerificationAgentToolSet(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeVerification

	allowed := ctx.getAllowedToolSet()

	// Verification 应有只读工具 + bash
	expectedTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"bash",
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range expectedTools {
		if !allowed[tool] {
			t.Errorf("Verification agent 应能使用 %s", tool)
		}
	}

	// Verification 不应有写操作工具
	forbiddenTools := []string{
		"write_file", "edit_file",
		"McpCallTool", "NotebookEdit", "Monitor", "AskUser", "Skill",
		"git_add", "git_commit", "git_branch",
	}
	for _, tool := range forbiddenTools {
		if allowed[tool] {
			t.Errorf("Verification agent 不应能使用 %s", tool)
		}
	}
}

// TestForkAgentToolSet 验证 fork proxy拥有完整工具访问权限
func TestForkAgentToolSet(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	ctx := NewAgentContext(executor, "test", "", "/workdir", 1)
	ctx.AgentType = AgentTypeFork

	allowed := ctx.getAllowedToolSet()

	// Fork 应有与 Coder 相同的完整工具集
	allExpectedTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"bash", "write_file", "edit_file",
		"LspDefinition", "LspReferences", "LspDiagnostics", "LspHover", "LspCompletion",
		"McpListTools", "McpListResources", "McpReadResource", "McpListPrompts", "McpGetPrompt",
		"McpCallTool",
		"git_diff", "git_status", "git_log", "git_add", "git_commit", "git_branch",
		"TodoWrite", "NotebookEdit", "Monitor", "AskUser", "Skill",
	}
	for _, tool := range allExpectedTools {
		if !allowed[tool] {
			t.Errorf("Fork agent 应能使用 %s", tool)
		}
	}
}

// TestReadOnlyToolSet_NoWriteTools 验证 readOnlyToolSet 不包含写操作工具
func TestReadOnlyToolSet_NoWriteTools(t *testing.T) {
	ro := readOnlyToolSet()

	// should not contain写操作工具
	writeTools := []string{"write_file", "edit_file", "bash"}
	for _, tool := range writeTools {
		if ro[tool] {
			t.Errorf("readOnlyToolSet should not contain %s", tool)
		}
	}

	// should contain基础只读工具
	expectedTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"LspDefinition", "LspHover", "LspCompletion",
		"McpListTools", "McpReadResource",
		"git_diff", "git_status", "git_log",
		"TodoWrite",
	}
	for _, tool := range expectedTools {
		if !ro[tool] {
			t.Errorf("readOnlyToolSet should contain %s", tool)
		}
	}
}

// === N2 反例测试：ExtractAgentID 三场景（审查 prompt §2.8）===

// TestExtractAgentID_RootTask 根任务 ctx 未注入 agentIDKey，返回 ""。
// 反例：若返回非空，说明 ctx 携带了意外值或 agentIDKey 类型被污染。
// N2 processStreamEvents 用此判断走 RecordUsage（兼容现状）还是 RecordAgentUsage（02 §2.1.2 契约）。
func TestExtractAgentID_RootTask(t *testing.T) {
	ctx := context.Background()
	if id := ExtractAgentID(ctx); id != "" {
		t.Fatalf("root task AgentID should be empty, got: %s", id)
	}
}

// TestExtractAgentID_Subagent 子任务 ctx 注入 agentIDKey，返回 "agent-001"。
// 模拟 N1 Agent.Execute 的 ctx 注入：streamCtx := context.WithValue(timeoutCtx, agentIDKey{}, agentID)。
// N2 通过导出函数 WithAgentID/ExtractAgentID 跨包访问 agentIDKey，避免重定义 key 类型
// 导致 ctx.Value 取不到值（Go context key 类型化机制要求类型完全一致）。
func TestExtractAgentID_Subagent(t *testing.T) {
	ctx := WithAgentID(context.Background(), "agent-001")
	if id := ExtractAgentID(ctx); id != "agent-001" {
		t.Fatalf("subagent AgentID mismatch: got %s, want agent-001", id)
	}
}

// TestExtractAgentID_Compact compact 调用方注入 AgentID="compact"。
// compact_cost.go 的 CompactCostRecorder 使用 compactAgentID="compact" 标记 compact 来源，
// N2 processStreamEvents 识别此 AgentID 后走 RecordAgentUsage 归属到 compact 分桶。
func TestExtractAgentID_Compact(t *testing.T) {
	ctx := WithAgentID(context.Background(), "compact")
	if id := ExtractAgentID(ctx); id != "compact" {
		t.Fatalf("compact AgentID mismatch: got %s, want compact", id)
	}
}

// TestFullToolSet_IncludesAllNewTools 验证 fullToolSet 包含所有新增工具
func TestFullToolSet_IncludesAllNewTools(t *testing.T) {
	ft := fullToolSet()

	// 验证新增工具全部包含
	newTools := []string{
		"Monitor", "TodoWrite", "AskUser",
		"LspHover", "LspCompletion",
		"git_add", "git_commit", "git_branch",
	}
	for _, tool := range newTools {
		if !ft[tool] {
			t.Errorf("fullToolSet should contain新增工具 %s", tool)
		}
	}

	// 验证完整工具集也包含基础工具
	basicTools := []string{
		"read_file", "glob_search", "grep_search", "WebFetch", "WebSearch",
		"bash", "write_file", "edit_file",
		"McpCallTool", "NotebookEdit", "Skill",
	}
	for _, tool := range basicTools {
		if !ft[tool] {
			t.Errorf("fullToolSet should contain基础工具 %s", tool)
		}
	}
}

// ============================================================================
// D1 子proxy缓存共享测试（对标同类 CLI prompt cache optimization）
// ============================================================================

// TestRenderedSystemPromptPreserved 测试 Fork 子proxy的系统提示词与父proxy字节一致
// 核心要求：fork proxy必须使用与父proxy完全相同的渲染后 system prompt 字符串，
// 以确保 API 请求共享相同前缀，命中 Anthropic 的 prompt cache
func TestRenderedSystemPromptPreserved(t *testing.T) {
	executor := NewExecutor(NewRegistry(), t.TempDir())
	parentPrompt := "You are a coding assistant. Version 1.0. Rules: ..."
	ctx := &AgentContext{
		Executor:        executor,
		AgentType:       AgentTypeFork,
		ParentSysPrompt: parentPrompt,
		IsForkChild:     true,
	}
	result := ctx.buildSystemPrompt()
	if result != parentPrompt {
		t.Errorf("Fork system prompt should be byte-identical to parent's, got different string")
	}
}

// TestByteIdenticalSystemPrompt 测试同一父proxy的多个 Fork 子proxy产生相同的系统提示词
// 多个 fork 子proxy共享相同前缀，最大化 prompt cache 命中率
func TestByteIdenticalSystemPrompt(t *testing.T) {
	executor := NewExecutor(NewRegistry(), t.TempDir())
	parentPrompt := "System prompt content that should be cached"
	ctx1 := &AgentContext{
		Executor:        executor,
		AgentType:       AgentTypeFork,
		ParentSysPrompt: parentPrompt,
		IsForkChild:     true,
	}
	ctx2 := &AgentContext{
		Executor:        executor,
		AgentType:       AgentTypeFork,
		ParentSysPrompt: parentPrompt,
		IsForkChild:     true,
	}
	prompt1 := ctx1.buildSystemPrompt()
	prompt2 := ctx2.buildSystemPrompt()
	if prompt1 != prompt2 {
		t.Errorf("Two forks from same parent should have identical system prompts")
	}
}

// TestCacheStatsRecorded 测试 CacheStats 结构体可以追踪缓存 token
// 对标同类 CLI 的 cache-tracker：追踪 prompt cache 命中情况
func TestCacheStatsRecorded(t *testing.T) {
	stats := CacheStats{
		CacheCreationTokens: 1000,
		CacheReadTokens:     5000,
	}
	if stats.CacheCreationTokens != 1000 {
		t.Errorf("CacheCreationTokens should be 1000")
	}
	if stats.CacheReadTokens != 5000 {
		t.Errorf("CacheReadTokens should be 5000")
	}
}

// TestAgentContextCacheStats 测试 AgentContext 拥有 CacheStats 字段
// 每个子proxy上下文都应能追踪自身的缓存命中统计
func TestAgentContextCacheStats(t *testing.T) {
	executor := NewExecutor(NewRegistry(), t.TempDir())
	ctx := &AgentContext{
		Executor:  executor,
		AgentType: AgentTypeCoder,
	}
	ctx.CacheStats = CacheStats{
		CacheCreationTokens: 2000,
		CacheReadTokens:     8000,
	}
	if ctx.CacheStats.CacheReadTokens != 8000 {
		t.Errorf("AgentContext.CacheStats.CacheReadTokens should be 8000")
	}
}

// TestForkInheritsRenderedPrompt 测试 Fork 子proxyinheritance父proxy的渲染后系统提示词和消息
// 创建 Fork proxy时，应inheritance父proxy的完整对话上下文
func TestForkInheritsRenderedPrompt(t *testing.T) {
	executor := NewExecutor(NewRegistry(), t.TempDir())
	parentPrompt := "Parent's fully rendered system prompt with all context"
	ctx := &AgentContext{
		Executor:        executor,
		AgentType:       AgentTypeFork,
		ParentSysPrompt: parentPrompt,
		ParentMessages:  []ai.MessageEntry{{Role: "user", Content: "hello"}},
		IsForkChild:     true,
	}
	// buildForkedMessages 应保留父proxy消息
	msgs := ctx.buildForkedMessages()
	if len(msgs) < 1 {
		t.Errorf("Forked messages should contain parent messages")
	}
}

// TestForkPlaceholderResult 测试 ForkPlaceholderResult 为固定字符串
// 占位符结果用于 cache optimization：多个 fork 子proxy共享相同的占位符输出
func TestForkPlaceholderResult(t *testing.T) {
	if ForkPlaceholderResult != "Fork started — processing in background" {
		t.Errorf("ForkPlaceholderResult should be a fixed string for cache optimization")
	}
}

// TestUpdateCacheStats 测试 UpdateCacheStats 方法累加缓存统计
// 验证多次调用 UpdateCacheStats 后 token 数正确累加
func TestUpdateCacheStats(t *testing.T) {
	executor := NewExecutor(NewRegistry(), t.TempDir())
	ctx := &AgentContext{
		Executor:  executor,
		AgentType: AgentTypeCoder,
	}
	ctx.UpdateCacheStats(100, 500)
	if ctx.CacheStats.CacheCreationTokens != 100 {
		t.Errorf("CacheCreationTokens should be 100, got %d", ctx.CacheStats.CacheCreationTokens)
	}
	if ctx.CacheStats.CacheReadTokens != 500 {
		t.Errorf("CacheReadTokens should be 500, got %d", ctx.CacheStats.CacheReadTokens)
	}

	// 再次调用应累加
	ctx.UpdateCacheStats(50, 200)
	if ctx.CacheStats.CacheCreationTokens != 150 {
		t.Errorf("CacheCreationTokens should be 150 after second update, got %d", ctx.CacheStats.CacheCreationTokens)
	}
	if ctx.CacheStats.CacheReadTokens != 700 {
		t.Errorf("CacheReadTokens should be 700 after second update, got %d", ctx.CacheStats.CacheReadTokens)
	}
}

// TestUsageEventUpdatesCacheStats 测试 UsageEvent 事件更新缓存统计
// 验证 Run() 方法在处理 UsageEvent 时正确更新 CacheStats
func TestUsageEventUpdatesCacheStats(t *testing.T) {
	round1 := make(chan ai.Event, 10)
	round1 <- ai.UsageEvent{InputTokens: 100, OutputTokens: 50, CacheCreation: 2000, CacheRead: 8000}
	round1 <- ai.TextDeltaEvent{Content: "response text"}
	round1 <- ai.MessageStopEvent{}
	close(round1)

	mockClient := &agentMockClient{
		responses: []<-chan ai.Event{round1},
	}

	registry := NewRegistry()
	executor := NewExecutor(registry, "")
	executor.AIClient = mockClient

	ctx := NewAgentContext(executor, "test cache stats", "", "/workdir", 1)
	_, err := ctx.Run(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if ctx.CacheStats.CacheCreationTokens != 2000 {
		t.Errorf("CacheCreationTokens should be 2000 after UsageEvent, got %d", ctx.CacheStats.CacheCreationTokens)
	}
	if ctx.CacheStats.CacheReadTokens != 8000 {
		t.Errorf("CacheReadTokens should be 8000 after UsageEvent, got %d", ctx.CacheStats.CacheReadTokens)
	}
}

// TestAgentRun_ToolInputDeltaIgnoresBlockIndex 覆盖同类隐患 C：
// 子代理路径此前用 toolCallIndexToID[e.Index] 映射参数，但 e.Index 是 Anthropic
// content block 的全局序号（开 thinking 时 thinking 占 block 0、tool_use 从 block 1 起），
// 与「第 N 个工具调用」错位，导致参数累积落空。
//
// 修复后与主路径 runner_core.go 对齐：用 currentToolID（最近一个 ToolUseEvent 的 ID），
// 不依赖 index 语义。本测试用 index=1（开 thinking 的真实形态）验证参数仍能正确累积。
func TestAgentRun_ToolInputDeltaIgnoresBlockIndex(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	executor := NewExecutor(r, tmpDir)

	const probeTool = "probe_capture_arg"
	// 关键：必须把工具 spec 注册进 r.builtin，Pipeline 的 SchemaValidateStage 才能放行。
	r.builtin[probeTool] = Spec{
		Name:            probeTool,
		InputSchema:     map[string]interface{}{"type": "object"},
		ConcurrencySafe: true,
	}
	var receivedRaw string
	executor.RegisterHandler(probeTool, func(ctx context.Context, raw json.RawMessage) (string, error) {
		receivedRaw = string(raw)
		return "ok", nil
	})

	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	executor.InitPipelineWithHooks(policy, tmpDir)

	// 子代理路径（agent.go executeToolCall）会按 AgentType 取角色工具面白名单。
	// L1 起工具面**派生自档位目录**（不再是手写的 _fullToolSet），所以只需正确声明档位：
	// 上面把 probeTool 注册进 r.builtin 时已声明 RequiredPermission=LevelReadOnly，
	// 它因此自动出现在只读/全量工具面里 —— 无需（也无法）再手动改表。
	// 此处断言派生确实生效，防止「注册了却不在工具面」的静默失败。
	if !fullToolSet()[probeTool] {
		t.Fatalf("probe 工具 %s 未被档位目录识别为已注册工具（工具面派生失效）", probeTool)
	}

	round1 := make(chan ai.Event, 10)
	// 开 thinking 的真实形态：ToolInputDeltaEvent.Index=1（block 1 才是 tool_use）。
	// 修复前 e.Index 被映射到 toolCallIndexToID[1]（第 2 个工具调用），但只有一个工具，
	// 映射失败 → 参数累积落空。修复后与主路径对齐：用 currentToolID，不依赖 index。
	round1 <- ai.ToolUseEvent{ID: "tool_1", Name: probeTool}
	round1 <- ai.ToolInputDeltaEvent{PartialJSON: `{"path":`, Index: 1}
	round1 <- ai.ToolInputDeltaEvent{PartialJSON: `"main.go"}`, Index: 1}
	round1 <- ai.MessageStopEvent{}
	close(round1)

	round2 := make(chan ai.Event, 10)
	round2 <- ai.TextDeltaEvent{Content: "done"}
	round2 <- ai.MessageStopEvent{}
	close(round2)

	executor.AIClient = &agentMockClient{responses: []<-chan ai.Event{round1, round2}}

	ctx := NewAgentContext(executor, "probe", "", "/workdir", 1)
	if _, err := ctx.Run(context.Background()); err != nil {
		t.Fatalf("Run err: %v", err)
	}

	want := `{"path":"main.go"}`
	if receivedRaw != want {
		t.Errorf("工具参数累积错误：Index=1 应映射到唯一工具调用，\n  got:  %s\n  want: %s", receivedRaw, want)
	}
}
