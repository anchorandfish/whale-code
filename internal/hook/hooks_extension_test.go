// Package hook — Hooks 系统扩展unit test
package hook

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestHookEventType_IsLifecycleEvent 生命周期事件判断
func TestHookEventType_IsLifecycleEvent(t *testing.T) {
	lifecycleEvents := []HookEventType{
		EventPostCompact, EventSessionStop, EventNotification, EventSessionStart, EventStop,
	}
	toolEvents := []HookEventType{
		EventPreToolUse, EventPostToolUse, EventPreRead, EventPostRead,
		EventPreWrite, EventPostWrite, EventPreEdit, EventPostEdit,
		EventPreBash, EventPostBash, EventPreSearch, EventPostSearch,
		EventPreGit, EventPostGit, EventPreWeb, EventPostWeb,
		EventPreAgent, EventPostAgent,
	}

	for _, e := range lifecycleEvents {
		if !e.IsLifecycleEvent() {
			t.Errorf("%s should be a lifecycle event", e)
		}
	}

	for _, e := range toolEvents {
		if e.IsLifecycleEvent() {
			t.Errorf("%s should NOT be a lifecycle event", e)
		}
	}
}

// TestHookConfig_AllEventTypes 验证 22 种事件类型全部可配置
func TestHookConfig_AllEventTypes(t *testing.T) {
	config := HookConfig{
		PreToolUse:   []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostToolUse:  []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreRead:      []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostRead:     []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreWrite:     []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostWrite:    []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreEdit:      []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostEdit:     []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreBash:      []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostBash:     []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreSearch:    []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostSearch:   []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreGit:       []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostGit:      []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreWeb:       []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostWeb:      []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PreAgent:     []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostAgent:    []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		SessionStart: []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		SessionStop:  []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		Stop:         []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		PostCompact:  []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
		Notification: []Hook{&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}}},
	}

	// 验证 JSON 序列化/反序列化
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	var parsed HookConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	// 验证所有 23 个字段都有数据
	eventCount := 0
	if len(parsed.PreToolUse) > 0 {
		eventCount++
	}
	if len(parsed.PostToolUse) > 0 {
		eventCount++
	}
	if len(parsed.PreRead) > 0 {
		eventCount++
	}
	if len(parsed.PostRead) > 0 {
		eventCount++
	}
	if len(parsed.PreWrite) > 0 {
		eventCount++
	}
	if len(parsed.PostWrite) > 0 {
		eventCount++
	}
	if len(parsed.PreEdit) > 0 {
		eventCount++
	}
	if len(parsed.PostEdit) > 0 {
		eventCount++
	}
	if len(parsed.PreBash) > 0 {
		eventCount++
	}
	if len(parsed.PostBash) > 0 {
		eventCount++
	}
	if len(parsed.PreSearch) > 0 {
		eventCount++
	}
	if len(parsed.PostSearch) > 0 {
		eventCount++
	}
	if len(parsed.PreGit) > 0 {
		eventCount++
	}
	if len(parsed.PostGit) > 0 {
		eventCount++
	}
	if len(parsed.PreWeb) > 0 {
		eventCount++
	}
	if len(parsed.PostWeb) > 0 {
		eventCount++
	}
	if len(parsed.PreAgent) > 0 {
		eventCount++
	}
	if len(parsed.PostAgent) > 0 {
		eventCount++
	}
	if len(parsed.SessionStart) > 0 {
		eventCount++
	}
	if len(parsed.SessionStop) > 0 {
		eventCount++
	}
	if len(parsed.Stop) > 0 {
		eventCount++
	}
	if len(parsed.PostCompact) > 0 {
		eventCount++
	}
	if len(parsed.Notification) > 0 {
		eventCount++
	}

	if eventCount != 23 {
		t.Errorf("Expected 23 event types, got %d", eventCount)
	}
}

// TestRunPreReadHooks 读取前钩子
func TestRunPreReadHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreRead: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "read_file", Pattern: "*.env", Action: ActionBlock, Reason: "cannot read .env files"}},
		},
	})

	result := m.RunPreReadHooks(".env")
	if result.Action != ActionBlock {
		t.Errorf("Expected block for .env, got %q", result.Action)
	}
	if result.Reason != "cannot read .env files" {
		t.Errorf("Expected reason, got %q", result.Reason)
	}

	// 非 .env 文件应允许
	result2 := m.RunPreReadHooks("main.go")
	if result2.Action != ActionAllow {
		t.Errorf("Expected allow for main.go, got %q", result2.Action)
	}
}

// TestRunPreWriteHooks 写入前钩子
func TestRunPreWriteHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreWrite: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Pattern: "*.lock", Action: ActionBlock, Reason: "cannot write lock files"}},
		},
	})

	result := m.RunPreWriteHooks("package.lock")
	if result.Action != ActionBlock {
		t.Errorf("Expected block for .lock, got %q", result.Action)
	}
}

// TestRunPreSearchHooks 搜索前钩子
func TestRunPreSearchHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreSearch: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "search", Action: ActionBlock, Reason: "search disabled"}},
		},
	})

	result := m.RunPreSearchHooks("TODO")
	if result.Action != ActionBlock {
		t.Errorf("Expected block, got %q", result.Action)
	}
}

// TestRunPreGitHooks Git 操作前钩子
func TestRunPreGitHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreGit: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "git", Action: ActionBlock, Reason: "git operations disabled"}},
		},
	})

	result := m.RunPreGitHooks("push")
	if result.Action != ActionBlock {
		t.Errorf("Expected block, got %q", result.Action)
	}
}

// TestRunPreWebHooks Web 请求前钩子
func TestRunPreWebHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreWeb: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "web", Action: ActionBlock, Reason: "web requests disabled"}},
		},
	})

	result := m.RunPreWebHooks("https://example.com")
	if result.Action != ActionBlock {
		t.Errorf("Expected block, got %q", result.Action)
	}
}

// TestRunPreAgentHooks 子proxy启动前钩子
func TestRunPreAgentHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreAgent: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "agent", Action: ActionBlock, Reason: "agents disabled"}},
		},
	})

	result := m.RunPreAgentHooks("coder")
	if result.Action != ActionBlock {
		t.Errorf("Expected block, got %q", result.Action)
	}
}

// TestRunNotificationHooks 通知钩子
func TestRunNotificationHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		Notification: []Hook{
			&HookCommand{HookBase: HookBase{Action: ActionBlock, Reason: "notification blocked"}},
		},
	})

	// Notification 是生命周期事件，deny 被忽略
	result := m.RunNotificationHooks("test message")
	if result.Action != ActionAllow {
		t.Errorf("Lifecycle deny should be ignored, got %q", result.Action)
	}
}

// TestParseHookJSONControl_AllowDecision allow 决策
func TestParseHookJSONControl_AllowDecision(t *testing.T) {
	stdout := []byte(`{"decision":"allow"}`)
	control, parsed := parseHookJSONControl(stdout)
	if !parsed {
		t.Error("Should parse valid JSON")
	}
	if control.Decision != "allow" {
		t.Errorf("Expected allow, got %q", control.Decision)
	}
}

// TestParseHookJSONControl_DenyDecision deny 决策
func TestParseHookJSONControl_DenyDecision(t *testing.T) {
	stdout := []byte(`{"decision":"deny","message":"not allowed"}`)
	control, parsed := parseHookJSONControl(stdout)
	if !parsed {
		t.Error("Should parse valid JSON")
	}
	if control.Decision != "deny" {
		t.Errorf("Expected deny, got %q", control.Decision)
	}
	if control.Message != "not allowed" {
		t.Errorf("Expected message, got %q", control.Message)
	}
}

// TestParseHookJSONControl_UpdatedInput 带更新输入
func TestParseHookJSONControl_UpdatedInput(t *testing.T) {
	stdout := []byte(`{"decision":"allow","updatedInput":{"path":"new.go"}}`)
	control, parsed := parseHookJSONControl(stdout)
	if !parsed {
		t.Error("Should parse valid JSON")
	}
	if len(control.UpdatedInput) == 0 {
		t.Error("Expected updatedInput")
	}

	var input map[string]string
	json.Unmarshal(control.UpdatedInput, &input)
	if input["path"] != "new.go" {
		t.Errorf("Expected path=new.go, got %v", input)
	}
}

// TestParseHookJSONControl_NoControlKeys 无控制键
func TestParseHookJSONControl_NoControlKeys(t *testing.T) {
	stdout := []byte(`{"random":"data"}`)
	_, parsed := parseHookJSONControl(stdout)
	if parsed {
		t.Error("Should not parse JSON without control keys")
	}
}

// TestParseHookJSONControl_InvalidJSON 无效 JSON
func TestParseHookJSONControl_InvalidJSON(t *testing.T) {
	stdout := []byte(`not json at all`)
	_, parsed := parseHookJSONControl(stdout)
	if parsed {
		t.Error("Should not parse invalid JSON")
	}
}

// TestLifecycleDenyIgnored_PostCompact PostCompact deny 被忽略
func TestLifecycleDenyIgnored_PostCompact(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostCompact: []Hook{
			&HookCommand{HookBase: HookBase{Action: ActionBlock, Reason: "compaction denied"}},
		},
	})

	result := m.RunPostCompactHooks()
	if result.Action != ActionAllow {
		t.Errorf("PostCompact deny should be ignored, got %q", result.Action)
	}
	if len(result.Messages) == 0 {
		t.Error("Expected warning message about ignored deny")
	}
}

// TestLifecycleDenyIgnored_Notification Notification deny 被忽略
func TestLifecycleDenyIgnored_Notification(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		Notification: []Hook{
			&HookCommand{HookBase: HookBase{Action: ActionBlock, Reason: "notification denied"}},
		},
	})

	result := m.RunNotificationHooks("test")
	if result.Action != ActionAllow {
		t.Errorf("Notification deny should be ignored, got %q", result.Action)
	}
}

// TestToolDenyNotIgnored_PreRead PreRead deny 正常生效
func TestToolDenyNotIgnored_PreRead(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreRead: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "read_file", Action: ActionBlock, Reason: "read blocked"}},
		},
	})

	result := m.RunPreReadHooks("main.go")
	if result.Action != ActionBlock {
		t.Errorf("PreRead deny should work, got %q", result.Action)
	}
}

// TestToolDenyNotIgnored_PreBash PreBash deny 正常生效
func TestToolDenyNotIgnored_PreBash(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreBash: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionBlock, Reason: "bash blocked"}},
		},
	})

	result := m.RunPreBashHooks("rm -rf /")
	if result.Action != ActionBlock {
		t.Errorf("PreBash deny should work, got %q", result.Action)
	}
}

// TestHookPayload_PreToolUse PreToolUse payload
func TestHookPayload_PreToolUse(t *testing.T) {
	payload := HookPayload{
		HookEventName: string(EventPreToolUse),
		ToolName:      "read_file",
		ToolInputJSON: `{"path":"main.go"}`,
		Cwd:           "/project",
	}

	data, _ := json.Marshal(payload)
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["hook_event_name"] != "PreToolUse" {
		t.Errorf("Expected PreToolUse, got %v", parsed["hook_event_name"])
	}
	if parsed["tool_name"] != "read_file" {
		t.Errorf("Expected read_file, got %v", parsed["tool_name"])
	}
	if parsed["cwd"] != "/project" {
		t.Errorf("Expected /project, got %v", parsed["cwd"])
	}
}

// TestHookPayload_PostCompact PostCompact payload
func TestHookPayload_PostCompact(t *testing.T) {
	payload := HookPayload{
		HookEventName:         string(EventPostCompact),
		SessionPath:           "/session/abc",
		RemovedMessageCount:   10,
		ResultingMessageCount: 5,
		Cwd:                   "/project",
	}

	data, _ := json.Marshal(payload)
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["hook_event_name"] != "PostCompact" {
		t.Errorf("Expected PostCompact, got %v", parsed["hook_event_name"])
	}
	if parsed["session_path"] != "/session/abc" {
		t.Errorf("Expected session path, got %v", parsed["session_path"])
	}
}

// TestBuildHookEnv_PreToolUse PreToolUse environment variable
func TestBuildHookEnv_PreToolUse(t *testing.T) {
	payload := HookPayload{
		ToolName:      "read_file",
		ToolInputJSON: `{"path":"main.go"}`,
	}
	env := buildHookEnv(payload, EventPreToolUse)

	foundEvent := false
	foundToolName := false
	for _, e := range env {
		if strings.HasPrefix(e, "HOOK_EVENT=PreToolUse") {
			foundEvent = true
		}
		if strings.HasPrefix(e, "HOOK_TOOL_NAME=read_file") {
			foundToolName = true
		}
	}

	if !foundEvent {
		t.Error("Expected HOOK_EVENT in env")
	}
	if !foundToolName {
		t.Error("Expected HOOK_TOOL_NAME in env")
	}
}

// TestBuildHookEnv_PostCompact PostCompact environment variable
func TestBuildHookEnv_PostCompact(t *testing.T) {
	payload := HookPayload{
		SessionPath:         "/session/abc",
		RemovedMessageCount: 10,
	}
	env := buildHookEnv(payload, EventPostCompact)

	foundEvent := false
	foundSession := false
	for _, e := range env {
		if strings.HasPrefix(e, "HOOK_EVENT=PostCompact") {
			foundEvent = true
		}
		if strings.HasPrefix(e, "HOOK_SESSION_PATH=/session/abc") {
			foundSession = true
		}
	}

	if !foundEvent {
		t.Error("Expected HOOK_EVENT in env")
	}
	if !foundSession {
		t.Error("Expected HOOK_SESSION_PATH in env")
	}
}

// TestYAMLKeyToHooks 新增事件 YAML 解析
func TestYAMLKeyToHooks(t *testing.T) {
	yamlContent := `pre_read:
  - matcher: read_file
    pattern: "*.env"
    action: block
    reason: cannot read env files
post_search:
  - matcher: search
    action: allow
pre_git:
  - matcher: git
    action: block
    reason: git disabled
pre_web:
  - matcher: web
    action: block
pre_agent:
  - matcher: agent
    action: allow
notification:
  - action: allow
`

	tmpDir := t.TempDir()
	configPath := tmpDir + "/hooks.yaml"
	os.WriteFile(configPath, []byte(yamlContent), 0644)

	m := NewManager("")
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("Failed to load YAML: %v", err)
	}

	cfg := m.Config()

	if len(cfg.PreRead) != 1 {
		t.Errorf("Expected 1 pre_read hook, got %d", len(cfg.PreRead))
	}
	if len(cfg.PostSearch) != 1 {
		t.Errorf("Expected 1 post_search hook, got %d", len(cfg.PostSearch))
	}
	if len(cfg.PreGit) != 1 {
		t.Errorf("Expected 1 pre_git hook, got %d", len(cfg.PreGit))
	}
	if len(cfg.PreWeb) != 1 {
		t.Errorf("Expected 1 pre_web hook, got %d", len(cfg.PreWeb))
	}
	if len(cfg.PreAgent) != 1 {
		t.Errorf("Expected 1 pre_agent hook, got %d", len(cfg.PreAgent))
	}
	if len(cfg.Notification) != 1 {
		t.Errorf("Expected 1 notification hook, got %d", len(cfg.Notification))
	}

	// 验证 PreRead 内容
	if cfg.PreRead[0].Base().Pattern != "*.env" {
		t.Errorf("Expected pattern *.env, got %q", cfg.PreRead[0].Base().Pattern)
	}
	if cfg.PreRead[0].Base().Action != ActionBlock {
		t.Errorf("Expected block action, got %q", cfg.PreRead[0].Base().Action)
	}
}

// TestHookResult_UpdatedInput HookResult 的 UpdatedInput 字段
func TestHookResult_UpdatedInput(t *testing.T) {
	updated := `{"path":"new.go"}`
	result := HookResult{
		Action:       ActionAllow,
		UpdatedInput: &updated,
	}

	if result.UpdatedInput == nil {
		t.Error("UpdatedInput should not be nil")
	}
	if *result.UpdatedInput != updated {
		t.Errorf("Expected %q, got %q", updated, *result.UpdatedInput)
	}
}

// TestHookResult_Messages HookResult 的 Messages 字段
func TestHookResult_Messages(t *testing.T) {
	result := HookResult{
		Action:   ActionAllow,
		Messages: []string{"msg1", "msg2"},
	}

	if len(result.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(result.Messages))
	}
}

// TestRunPostReadHooks 读取后钩子
func TestRunPostReadHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostRead: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "read_file", Action: ActionAllow}},
		},
	})

	result := m.RunPostReadHooks("main.go", "file content")
	if result.Action != ActionAllow {
		t.Errorf("Expected allow, got %q", result.Action)
	}
}

// TestRunPostWriteHooks 写入后钩子
func TestRunPostWriteHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostWrite: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionAllow}},
		},
	})

	result := m.RunPostWriteHooks("main.go")
	if result.Action != ActionAllow {
		t.Errorf("Expected allow, got %q", result.Action)
	}
}

// TestRunPostSearchHooks 搜索后钩子
func TestRunPostSearchHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostSearch: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "search", Action: ActionAllow}},
		},
	})

	result := m.RunPostSearchHooks("TODO", "results")
	if result.Action != ActionAllow {
		t.Errorf("Expected allow, got %q", result.Action)
	}
}

// TestRunPostGitHooks Git 操作后钩子
func TestRunPostGitHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostGit: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "git", Action: ActionAllow}},
		},
	})

	result := m.RunPostGitHooks("status", "output")
	if result.Action != ActionAllow {
		t.Errorf("Expected allow, got %q", result.Action)
	}
}

// TestRunPostWebHooks Web 请求后钩子
func TestRunPostWebHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostWeb: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "web", Action: ActionAllow}},
		},
	})

	result := m.RunPostWebHooks("https://example.com", "content")
	if result.Action != ActionAllow {
		t.Errorf("Expected allow, got %q", result.Action)
	}
}

// TestRunPostAgentHooks 子proxy完成后钩子
func TestRunPostAgentHooks(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostAgent: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "agent", Action: ActionAllow}},
		},
	})

	result := m.RunPostAgentHooks("coder", "result")
	if result.Action != ActionAllow {
		t.Errorf("Expected allow, got %q", result.Action)
	}
}
