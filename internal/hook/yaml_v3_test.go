// yaml_v3_test.go — YAML 1.2 解析器测试（/ T1-P2-G5-M16YAML解析器升级）。
//
// 测试覆盖：
// - 块标量多行字符串（block scalar）
// - 锚点引用（anchor/alias）
// - 嵌套映射（headers）
// - 流式语法
// - LoadConfig 从 YAML 文件加载
// - 错误信息含行号
// - JSON 优先兼容
//
// 子审查优化 4 吸纳：覆盖多行字符串 / 块标量 / 嵌套映射 / 锚点引用。
package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestYAMLV3_BlockScalar 块标量多行字符串（YAML 1.2 字面量块）
func TestYAMLV3_BlockScalar(t *testing.T) {
	yamlData := `
pre_tool_use:
  - type: command
    matcher: "*"
    command: |-
      echo hello
      echo world
`
	var cfg HookConfig
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
	cmd, ok := cfg.PreToolUse[0].(*HookCommand)
	if !ok {
		t.Fatalf("expected HookCommand, got %T", cfg.PreToolUse[0])
	}
	if !strings.Contains(cmd.Command, "echo hello") || !strings.Contains(cmd.Command, "echo world") {
		t.Errorf("block scalar not parsed: %q", cmd.Command)
	}
}

// TestYAMLV3_Anchor 锚点引用（YAML 1.2 anchor/alias）
func TestYAMLV3_Anchor(t *testing.T) {
	yamlData := `
pre_tool_use:
  - &hook1
    type: command
    matcher: "*"
    command: echo hello
  - *hook1
`
	var cfg HookConfig
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(cfg.PreToolUse) != 2 {
		t.Fatalf("expected 2 hooks (anchor + alias), got %d", len(cfg.PreToolUse))
	}
	cmd1, ok := cfg.PreToolUse[0].(*HookCommand)
	if !ok {
		t.Fatalf("expected HookCommand[0], got %T", cfg.PreToolUse[0])
	}
	cmd2, ok := cfg.PreToolUse[1].(*HookCommand)
	if !ok {
		t.Fatalf("expected HookCommand[1], got %T", cfg.PreToolUse[1])
	}
	if cmd1.Command != cmd2.Command {
		t.Errorf("anchor not resolved: %q vs %q", cmd1.Command, cmd2.Command)
	}
}

// TestYAMLV3_NestedMap 嵌套映射（headers）
func TestYAMLV3_NestedMap(t *testing.T) {
	yamlData := `
pre_tool_use:
  - type: http
    matcher: "*"
    url: http://example.com
    headers:
      X-Custom-Header: value1
      Authorization: Bearer token
`
	var cfg HookConfig
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
	hh, ok := cfg.PreToolUse[0].(*HookHTTP)
	if !ok {
		t.Fatalf("expected HookHTTP, got %T", cfg.PreToolUse[0])
	}
	if len(hh.Headers) != 2 {
		t.Errorf("expected 2 headers, got %d", len(hh.Headers))
	}
	if hh.Headers["X-Custom-Header"] != "value1" {
		t.Errorf("header not parsed: %v", hh.Headers)
	}
	if hh.Headers["Authorization"] != "Bearer token" {
		t.Errorf("Authorization header not parsed: %v", hh.Headers)
	}
}

// TestYAMLV3_FlowStyle 流式语法验证
func TestYAMLV3_FlowStyle(t *testing.T) {
	// 流式语法（headers 使用 flow map）
	yamlData := `
pre_tool_use:
  - type: http
    matcher: "*"
    url: http://example.com
    headers: {X-A: "1", X-B: "2"}
`
	var cfg HookConfig
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
	hh, ok := cfg.PreToolUse[0].(*HookHTTP)
	if !ok {
		t.Fatalf("expected HookHTTP, got %T", cfg.PreToolUse[0])
	}
	if len(hh.Headers) != 2 {
		t.Errorf("expected 2 headers from flow map, got %d", len(hh.Headers))
	}
}

// TestYAMLV3_LoadConfig LoadConfig 从 YAML 文件加载
func TestYAMLV3_LoadConfig(t *testing.T) {
	yamlData := `
pre_tool_use:
  - type: command
    matcher: "*"
    command: echo loaded
    timeout: 5000
post_tool_use:
  - type: callback
    matcher: "Read"
`
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "hooks.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlData), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	m := NewManager(tmpDir)
	if err := m.LoadConfig(yamlPath); err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse, got %d", len(cfg.PreToolUse))
	}
	cmd, ok := cfg.PreToolUse[0].(*HookCommand)
	if !ok {
		t.Errorf("expected HookCommand, got %T", cfg.PreToolUse[0])
	}
	if cmd.Timeout != 5000 {
		t.Errorf("expected timeout 5000, got %d", cmd.Timeout)
	}
	if len(cfg.PostToolUse) != 1 {
		t.Errorf("expected 1 PostToolUse, got %d", len(cfg.PostToolUse))
	}
}

// TestYAMLV3_LoadConfig_ErrorWithLine 错误信息含路径
func TestYAMLV3_LoadConfig_ErrorWithLine(t *testing.T) {
	// 故意缩进错误的 YAML
	badYAML := `pre_tool_use:
   - type: command
    matcher: "*"
`
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "hooks.yaml")
	if err := os.WriteFile(yamlPath, []byte(badYAML), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	m := NewManager(tmpDir)
	err := m.LoadConfig(yamlPath)
	if err == nil {
		t.Fatal("expected error for bad YAML indentation")
	}
	// 子审查优化 5：错误信息应包含路径
	if !strings.Contains(err.Error(), tmpDir) && !strings.Contains(err.Error(), "hooks.yaml") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

// TestYAMLV3_BackwardCompatJSON JSON 优先兼容
func TestYAMLV3_BackwardCompatJSON(t *testing.T) {
	jsonData := `{"pre_tool_use":[{"type":"command","matcher":"*","command":"echo json"}]}`
	var cfg HookConfig
	if err := json.Unmarshal([]byte(jsonData), &cfg); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
	cmd, ok := cfg.PreToolUse[0].(*HookCommand)
	if !ok {
		t.Fatalf("expected HookCommand, got %T", cfg.PreToolUse[0])
	}
	if cmd.Command != "echo json" {
		t.Errorf("expected 'echo json', got %q", cmd.Command)
	}
}

// TestYAMLV3_LoadConfigFromBytes LoadConfigFromBytes 字节加载
func TestYAMLV3_LoadConfigFromBytes(t *testing.T) {
	yamlData := []byte(`
pre_tool_use:
  - type: command
    matcher: "*"
    command: echo from_bytes
`)
	m := NewManager(t.TempDir())
	if err := m.LoadConfigFromBytes(yamlData); err != nil {
		t.Fatalf("LoadConfigFromBytes failed: %v", err)
	}
	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse, got %d", len(cfg.PreToolUse))
	}
}

// TestYAMLV3_ParseConfigBytes 热重载场景的 parseConfigBytes
func TestYAMLV3_ParseConfigBytes(t *testing.T) {
	yamlData := []byte(`
pre_tool_use:
  - type: command
    matcher: "*"
    command: echo reload
`)
	cfg, err := parseConfigBytes(yamlData)
	if err != nil {
		t.Fatalf("parseConfigBytes failed: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse, got %d", len(cfg.PreToolUse))
	}
}

// TestYAMLV3_ParseConfigBytes_GarbageInput 垃圾输入拒绝
func TestYAMLV3_ParseConfigBytes_GarbageInput(t *testing.T) {
	// 非空但无 YAML/JSON 结构的内容
	garbage := []byte("this is not valid yaml or json at all !!!")
	_, err := parseConfigBytes(garbage)
	if err == nil {
		t.Error("expected error for garbage input, got nil")
	}
}

// TestYAMLV3_EmptyConfig 允许空配置
func TestYAMLV3_EmptyConfig(t *testing.T) {
	// 空配置应被接受（用户清空所有钩子）
	emptyYAML := []byte("")
	cfg, err := parseConfigBytes(emptyYAML)
	if err != nil {
		t.Fatalf("expected no error for empty config, got: %v", err)
	}
	if !isConfigEmpty(cfg) {
		t.Error("expected empty config")
	}
}

// TestYAMLV3_CommentSupport 注释支持
func TestYAMLV3_CommentSupport(t *testing.T) {
	yamlData := `
# 这是注释
pre_tool_use:
  - type: command  # 行内注释
    matcher: "*"
    command: echo commented
`
	var cfg HookConfig
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
}
