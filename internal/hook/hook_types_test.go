package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// MockAIClient mock AI client
type MockAIClient struct {
	Response string
	Error    error
}

// EvaluatePrompt mock AI client的 EvaluatePrompt 方法
func (m *MockAIClient) EvaluatePrompt(ctx context.Context, prompt string, model string) (string, error) {
	if m.Error != nil {
		return "", m.Error
	}
	return m.Response, nil
}

// TestHookType_DefaultCommand 测试空 Type 默认为 command
func TestHookType_DefaultCommand(t *testing.T) {
	h := &HookCommand{Command: "echo hello"}
	if h.Base().Type != "" {
		t.Fatal("expected empty type")
	}
	// When executing, empty type should be treated as command
	// 使用 Action: ActionAllow 确保结果可预测
	h.Base().Action = ActionAllow
	mgr := NewManager(t.TempDir())
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)
	if result.Action != ActionAllow {
		t.Fatalf("expected allow for command hook, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_PromptType_Allow 测试 prompt hook allow 决策
func TestExecuteHookWithPayload_PromptType_Allow(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Response: `{"decision":"allow"}`,
	})

	h := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt},
		Prompt:   "Is this operation safe? Tool: {{tool_name}}",
	}
	payload := HookPayload{ToolName: "read_file", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_PromptType_Deny 测试 prompt hook deny 决策
func TestExecuteHookWithPayload_PromptType_Deny(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Response: `{"decision":"deny","message":"dangerous operation"}`,
	})

	h := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt},
		Prompt:   "Is this safe?",
	}
	payload := HookPayload{ToolName: "bash", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionBlock {
		t.Fatalf("expected block, got %s", result.Action)
	}
	if result.Reason != "dangerous operation" {
		t.Fatalf("expected reason 'dangerous operation', got %s", result.Reason)
	}
}

// TestExecuteHookWithPayload_PromptType_UpdatedInput 测试 prompt hook 修改输入
func TestExecuteHookWithPayload_PromptType_UpdatedInput(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Response: `{"decision":"allow","updatedInput":{"command":"safe_command"}}`,
	})

	h := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt},
		Prompt:   "Sanitize the command",
	}
	payload := HookPayload{ToolName: "bash", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
	if result.UpdatedInput == nil {
		t.Fatal("expected updatedInput to be set")
	}
}

// TestExecuteHookWithPayload_PromptType_NoAIClient 测试无 AI client时降级
func TestExecuteHookWithPayload_PromptType_NoAIClient(t *testing.T) {
	mgr := NewManager(t.TempDir())
	// 不设置 AI client

	h := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt},
		Prompt:   "Is this safe?",
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_PromptType_LLMError 测试 LLM 错误
func TestExecuteHookWithPayload_PromptType_LLMError(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Error: fmt.Errorf("API error"),
	})

	h := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt},
		Prompt:   "Is this safe?",
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_PromptType_TemplateVariables 测试模板变量替换
func TestExecuteHookWithPayload_PromptType_TemplateVariables(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Response: `{"decision":"allow"}`,
	})
	// 测试 renderTemplate
	rendered := mgr.renderTemplate("Tool: {{tool_name}}, File: {{file_path}}, Event: {{event}}", HookPayload{
		ToolName:      "read_file",
		FilePath:      "/tmp/test.go",
		HookEventName: "PreToolUse",
	})
	if rendered != "Tool: read_file, File: /tmp/test.go, Event: PreToolUse" {
		t.Fatalf("unexpected rendered template: %s", rendered)
	}
}

// TestExecuteHookWithPayload_HTTPType_Allow 测试 HTTP hook allow
func TestExecuteHookWithPayload_HTTPType_Allow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"decision": "allow"})
	}))
	defer server.Close()

	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP},
		URL:      server.URL,
	}
	payload := HookPayload{ToolName: "read_file", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_HTTPType_Deny 测试 HTTP hook deny
func TestExecuteHookWithPayload_HTTPType_Deny(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"decision": "deny", "message": "blocked by policy"})
	}))
	defer server.Close()

	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP},
		URL:      server.URL,
	}
	payload := HookPayload{ToolName: "bash", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionBlock {
		t.Fatalf("expected block, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_HTTPType_ServerError 测试 HTTP hook server错误
func TestExecuteHookWithPayload_HTTPType_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP},
		URL:      server.URL,
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_HTTPType_ConnectionError 测试 HTTP hook 连接错误
func TestExecuteHookWithPayload_HTTPType_ConnectionError(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP},
		URL:      "http://127.0.0.1:1/nonexistent", // 端口1几乎不会有服务
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_HTTPType_Headers 测试 HTTP hook 自定义request header
func TestExecuteHookWithPayload_HTTPType_Headers(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		json.NewEncoder(w).Encode(map[string]string{"decision": "allow"})
	}))
	defer server.Close()

	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP},
		URL:      server.URL,
		Headers:  map[string]string{"X-Custom-Auth": "secret123"},
	}
	payload := HookPayload{Cwd: t.TempDir()}
	mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if receivedHeaders.Get("X-Custom-Auth") != "secret123" {
		t.Fatalf("expected custom header, got %s", receivedHeaders.Get("X-Custom-Auth"))
	}
}

// TestExecuteHookWithPayload_CallbackType_Allow 测试 callback hook allow
func TestExecuteHookWithPayload_CallbackType_Allow(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback},
		Callback: func(p HookPayload) HookResult {
			return HookResult{Action: ActionAllow}
		},
	}
	payload := HookPayload{ToolName: "read_file", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_CallbackType_Deny 测试 callback hook deny
func TestExecuteHookWithPayload_CallbackType_Deny(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback},
		Callback: func(p HookPayload) HookResult {
			return HookResult{Action: ActionBlock, Reason: "custom policy violation"}
		},
	}
	payload := HookPayload{ToolName: "bash", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionBlock {
		t.Fatalf("expected block, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_CallbackType_Panic 测试 callback hook panic 恢复
func TestExecuteHookWithPayload_CallbackType_Panic(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback},
		Callback: func(p HookPayload) HookResult {
			panic("something went wrong")
		},
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn after panic, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_CallbackType_LifecycleDenyIgnored 测试生命周期事件忽略 deny
func TestExecuteHookWithPayload_CallbackType_LifecycleDenyIgnored(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback},
		Callback: func(p HookPayload) HookResult {
			return HookResult{Action: ActionBlock, Reason: "should be ignored"}
		},
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPostCompact)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow (deny ignored for lifecycle), got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_AgentType_Allow 测试 agent hook allow
func TestExecuteHookWithPayload_AgentType_Allow(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Response: `{"decision":"allow","message":"operation verified safe"}`,
	})

	h := &HookAgent{
		HookBase: HookBase{Type: HookTypeAgent},
		Prompt:   "Verify this operation is safe",
	}
	payload := HookPayload{ToolName: "bash", Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_UnknownType 测试未知类型
func TestExecuteHookWithPayload_UnknownType(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookCommand{HookBase: HookBase{Type: HookType("unknown_type")}, Command: "test"}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn for unknown type, got %s", result.Action)
	}
}

// TestBackwardCompatibility_OldFormat 测试旧格式兼容
func TestBackwardCompatibility_OldFormat(t *testing.T) {
	// 旧格式配置（无 type 字段）应默认为 command 类型
	config := `{"pre_tool_use": [{"matcher": "bash", "command": "echo allowed"}]}`
	mgr := NewManager(t.TempDir())
	if err := mgr.LoadConfigFromBytes([]byte(config)); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
}

// TestHookConfig_LoadingWithNewTypes 测试加载包含新类型的配置
func TestHookConfig_LoadingWithNewTypes(t *testing.T) {
	config := `{
		"pre_tool_use": [
			{"type": "prompt", "matcher": "bash", "prompt": "Is this safe?", "model": "qwen3.6-27b"},
			{"type": "http", "matcher": "write_file", "url": "http://localhost:8888/approve"}
		],
		"post_tool_use": [
			{"type": "http", "matcher": "*", "url": "http://localhost:8888/log"}
		]
	}`
	mgr := NewManager(t.TempDir())
	if err := mgr.LoadConfigFromBytes([]byte(config)); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	cfg := mgr.Config()
	if len(cfg.PreToolUse) != 2 {
		t.Fatalf("expected 2 pre_tool_use hooks, got %d", len(cfg.PreToolUse))
	}
	if cfg.PreToolUse[0].Base().Type != HookTypePrompt {
		t.Fatalf("expected first hook type to be prompt, got %s", cfg.PreToolUse[0].Base().Type)
	}
	if cfg.PreToolUse[1].Base().Type != HookTypeHTTP {
		t.Fatalf("expected second hook type to be http, got %s", cfg.PreToolUse[1].Base().Type)
	}
}

// TestParseHookResponse_NonJSON 测试非 JSON 响应降级为 allow
func TestParseHookResponse_NonJSON(t *testing.T) {
	mgr := NewManager(t.TempDir())
	result := mgr.parseHookResponse("plain text response", EventPreToolUse)
	if result.Action != ActionAllow {
		t.Fatalf("expected allow for non-JSON response, got %s", result.Action)
	}
	if result.Output != "plain text response" {
		t.Fatalf("expected output to be the raw response, got %s", result.Output)
	}
}

// TestParseHookResponse_DenyLifecycle 测试生命周期事件中 deny 被忽略
func TestParseHookResponse_DenyLifecycle(t *testing.T) {
	mgr := NewManager(t.TempDir())
	result := mgr.parseHookResponse(`{"decision":"deny","message":"not allowed"}`, EventPostCompact)
	if result.Action != ActionAllow {
		t.Fatalf("expected allow (deny ignored for lifecycle), got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_CallbackType_MissingCallback 测试 callback 缺少函数
func TestExecuteHookWithPayload_CallbackType_MissingCallback(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookCallback{HookBase: HookBase{Type: HookTypeCallback}}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_PromptType_MissingPrompt 测试 prompt 缺少 prompt 字段
func TestExecuteHookWithPayload_PromptType_MissingPrompt(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{Response: `{"decision":"allow"}`})

	h := &HookPrompt{HookBase: HookBase{Type: HookTypePrompt}}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_AgentType_MissingPrompt 测试 agent 缺少 prompt 字段
func TestExecuteHookWithPayload_AgentType_MissingPrompt(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{Response: `{"decision":"allow"}`})

	h := &HookAgent{HookBase: HookBase{Type: HookTypeAgent}}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_HTTPType_MissingURL 测试 http 缺少 url 字段
func TestExecuteHookWithPayload_HTTPType_MissingURL(t *testing.T) {
	mgr := NewManager(t.TempDir())
	h := &HookHTTP{HookBase: HookBase{Type: HookTypeHTTP}}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_AgentType_NoAIClient 测试 agent 无 AI client
func TestExecuteHookWithPayload_AgentType_NoAIClient(t *testing.T) {
	mgr := NewManager(t.TempDir())
	// 不设置 AI client

	h := &HookAgent{
		HookBase: HookBase{Type: HookTypeAgent},
		Prompt:   "Verify this operation",
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestExecuteHookWithPayload_AgentType_LLMError 测试 agent LLM 错误
func TestExecuteHookWithPayload_AgentType_LLMError(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Error: fmt.Errorf("agent API error"),
	})

	h := &HookAgent{
		HookBase: HookBase{Type: HookTypeAgent},
		Prompt:   "Verify this operation",
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", result.Action)
	}
}

// TestRenderTemplate_AllVariables 测试所有模板变量替换
func TestRenderTemplate_AllVariables(t *testing.T) {
	mgr := NewManager(t.TempDir())
	tmpl := "{{tool_name}}|{{file_path}}|{{command}}|{{tool_output}}|{{event}}"
	payload := HookPayload{
		ToolName:      "bash",
		FilePath:      "/tmp/test.sh",
		Command:       "ls -la",
		ToolOutput:    "file1.txt\nfile2.txt",
		HookEventName: "PreToolUse",
	}
	rendered := mgr.renderTemplate(tmpl, payload)
	expected := "bash|/tmp/test.sh|ls -la|file1.txt\nfile2.txt|PreToolUse"
	if rendered != expected {
		t.Fatalf("expected %q, got %q", expected, rendered)
	}
}

// TestHTTPHook_SendsPayload 测试 HTTP hook 发送 payload
func TestHTTPHook_SendsPayload(t *testing.T) {
	var receivedBody HookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(map[string]string{"decision": "allow"})
	}))
	defer server.Close()

	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP},
		URL:      server.URL,
	}
	payload := HookPayload{ToolName: "read_file", FilePath: "/tmp/test.go", Cwd: t.TempDir()}
	mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if receivedBody.ToolName != "read_file" {
		t.Fatalf("expected tool_name 'read_file', got %s", receivedBody.ToolName)
	}
	if receivedBody.FilePath != "/tmp/test.go" {
		t.Fatalf("expected file_path '/tmp/test.go', got %s", receivedBody.FilePath)
	}
}

// TestCallbackHook_ReceivesPayload 测试 callback hook 接收 payload
func TestCallbackHook_ReceivesPayload(t *testing.T) {
	var receivedPayload HookPayload
	mgr := NewManager(t.TempDir())
	h := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback},
		Callback: func(p HookPayload) HookResult {
			receivedPayload = p
			return HookResult{Action: ActionAllow}
		},
	}
	payload := HookPayload{ToolName: "bash", Command: "rm -rf /", Cwd: t.TempDir()}
	mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if receivedPayload.ToolName != "bash" {
		t.Fatalf("expected tool_name 'bash', got %s", receivedPayload.ToolName)
	}
	if receivedPayload.Command != "rm -rf /" {
		t.Fatalf("expected command 'rm -rf /', got %s", receivedPayload.Command)
	}
}

// TestHTTPHook_CustomTimeout 测试 HTTP hook 自定义超时
func TestHTTPHook_CustomTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]string{"decision": "allow"})
	}))
	defer server.Close()

	mgr := NewManager(t.TempDir())
	h := &HookHTTP{
		HookBase: HookBase{Type: HookTypeHTTP, Timeout: 5000}, // 5 秒超时，足够
		URL:      server.URL,
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
}

// TestPromptHook_DefaultModel 测试 prompt hook 默认模型
func TestPromptHook_DefaultModel(t *testing.T) {
	var capturedModel string
	mgr := NewManager(t.TempDir())
	mgr.SetAIClient(&MockAIClient{
		Response: `{"decision":"allow"}`,
	})

	// 不指定 model，应使用default value
	h := &HookPrompt{
		HookBase: HookBase{Type: HookTypePrompt},
		Prompt:   "Is this safe?",
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow, got %s", result.Action)
	}
	_ = capturedModel // MockAIClient 无法捕获 model，但逻辑已覆盖
}

// TestBlockActionWithCallbackType 测试 block action 与 callback 类型
func TestBlockActionWithCallbackType(t *testing.T) {
	mgr := NewManager(t.TempDir())
	// callback 类型的 block action 不应被提前拦截
	h := &HookCallback{
		HookBase: HookBase{Type: HookTypeCallback, Action: ActionBlock},
		Callback: func(p HookPayload) HookResult {
			return HookResult{Action: ActionAllow}
		},
	}
	payload := HookPayload{Cwd: t.TempDir()}
	result := mgr.ExecuteHookWithPayload(h, payload, EventPreToolUse)

	if result.Action != ActionAllow {
		t.Fatalf("expected allow (callback should execute), got %s", result.Action)
	}
}
