// Package hook — 钩子系统的unit test
package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestManager_LoadConfig_JSON 测试从 JSON 文件加载hook config
func TestManager_LoadConfig_JSON(t *testing.T) {
	tmpDir := t.TempDir()

	// 构造 JSON 格式的hook config
	config := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionBlock, Reason: "no writes allowed"}},
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
		},
		PostToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionAllow}, Command: "echo done"},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("serialize config失败: %v", err)
	}

	configPath := filepath.Join(tmpDir, "hooks.json")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("写入config file失败: %v", err)
	}

	m := NewManager(tmpDir)
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config失败: %v", err)
	}

	loaded := m.Config()

	// 验证 PreToolUse 列表
	if len(loaded.PreToolUse) != 2 {
		t.Errorf("PreToolUse 数量错误: got %d, want 2", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Matcher != "write_file" {
		t.Errorf("PreToolUse[0].Matcher 错误: got %q, want %q", loaded.PreToolUse[0].Base().Matcher, "write_file")
	}
	if loaded.PreToolUse[0].Base().Action != ActionBlock {
		t.Errorf("PreToolUse[0].Action 错误: got %q, want %q", loaded.PreToolUse[0].Base().Action, ActionBlock)
	}
	if loaded.PreToolUse[0].Base().Reason != "no writes allowed" {
		t.Errorf("PreToolUse[0].Reason 错误: got %q, want %q", loaded.PreToolUse[0].Base().Reason, "no writes allowed")
	}
	if loaded.PreToolUse[1].Base().Matcher != "*" {
		t.Errorf("PreToolUse[1].Matcher 错误: got %q, want %q", loaded.PreToolUse[1].Base().Matcher, "*")
	}
	if loaded.PreToolUse[1].Base().Action != ActionAllow {
		t.Errorf("PreToolUse[1].Action 错误: got %q, want %q", loaded.PreToolUse[1].Base().Action, ActionAllow)
	}

	// 验证 PostToolUse 列表
	if len(loaded.PostToolUse) != 1 {
		t.Errorf("PostToolUse 数量错误: got %d, want 1", len(loaded.PostToolUse))
	}
	if loaded.PostToolUse[0].Base().Matcher != "bash" {
		t.Errorf("PostToolUse[0].Matcher 错误: got %q, want %q", loaded.PostToolUse[0].Base().Matcher, "bash")
	}
	if loaded.PostToolUse[0].(*HookCommand).Command != "echo done" {
		t.Errorf("PostToolUse[0].Command 错误: got %q, want %q", loaded.PostToolUse[0].(*HookCommand).Command, "echo done")
	}
}

// TestManager_LoadConfig_YAML 测试从 YAML 文件加载hook config
func TestManager_LoadConfig_YAML(t *testing.T) {
	tmpDir := t.TempDir()

	// 构造简单 YAML 格式的hook config（兼容 parseSimpleYAML 解析器）
	yamlContent := `pre_tool_use:
  - matcher: write_file
    action: block
    reason: no writes allowed
  - matcher: "*"
    action: allow
post_tool_use:
  - matcher: bash
    action: allow
    command: echo done
`

	configPath := filepath.Join(tmpDir, "hooks.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("写入config file失败: %v", err)
	}

	m := NewManager(tmpDir)
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config失败: %v", err)
	}

	loaded := m.Config()

	// 验证 PreToolUse 列表
	if len(loaded.PreToolUse) != 2 {
		t.Errorf("PreToolUse 数量错误: got %d, want 2", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Matcher != "write_file" {
		t.Errorf("PreToolUse[0].Matcher 错误: got %q, want %q", loaded.PreToolUse[0].Base().Matcher, "write_file")
	}
	if loaded.PreToolUse[0].Base().Action != ActionBlock {
		t.Errorf("PreToolUse[0].Action 错误: got %q, want %q", loaded.PreToolUse[0].Base().Action, ActionBlock)
	}
	if loaded.PreToolUse[0].Base().Reason != "no writes allowed" {
		t.Errorf("PreToolUse[0].Reason 错误: got %q, want %q", loaded.PreToolUse[0].Base().Reason, "no writes allowed")
	}
	if loaded.PreToolUse[1].Base().Matcher != "*" {
		t.Errorf("PreToolUse[1].Matcher 错误: got %q, want %q", loaded.PreToolUse[1].Base().Matcher, "*")
	}
	if loaded.PreToolUse[1].Base().Action != ActionAllow {
		t.Errorf("PreToolUse[1].Action 错误: got %q, want %q", loaded.PreToolUse[1].Base().Action, ActionAllow)
	}

	// 验证 PostToolUse 列表
	if len(loaded.PostToolUse) != 1 {
		t.Errorf("PostToolUse 数量错误: got %d, want 1", len(loaded.PostToolUse))
	}
	if loaded.PostToolUse[0].Base().Matcher != "bash" {
		t.Errorf("PostToolUse[0].Matcher 错误: got %q, want %q", loaded.PostToolUse[0].Base().Matcher, "bash")
	}
	if loaded.PostToolUse[0].(*HookCommand).Command != "echo done" {
		t.Errorf("PostToolUse[0].Command 错误: got %q, want %q", loaded.PostToolUse[0].(*HookCommand).Command, "echo done")
	}
}

// TestMatchPreHook 测试pre-hook匹配：精确匹配和通配符匹配
func TestMatchPreHook(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionBlock, Reason: "no writes"}},
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
		},
	})

	// matcher="write_file" 应匹配 write_file 工具
	h := m.MatchPreHook("write_file", "/tmp/test.go")
	if h == nil {
		t.Fatal("expected匹配到 write_file 钩子，但返回 nil")
	}
	if h.Base().Matcher != "write_file" {
		t.Errorf("Matcher 错误: got %q, want %q", h.Base().Matcher, "write_file")
	}
	if h.Base().Action != ActionBlock {
		t.Errorf("Action 错误: got %q, want %q", h.Base().Action, ActionBlock)
	}

	// matcher="*" 应匹配所有工具（此处 read_file 不在精确匹配列表中，应匹配通配符）
	h2 := m.MatchPreHook("read_file", "/tmp/test.go")
	if h2 == nil {
		t.Fatal("expected匹配到 * 钩子，但返回 nil")
	}
	if h2.Base().Matcher != "*" {
		t.Errorf("Matcher 错误: got %q, want %q", h2.Base().Matcher, "*")
	}

	// write_file 工具应优先匹配精确的 matcher，而非通配符
	h3 := m.MatchPreHook("write_file", "/tmp/test.go")
	if h3 == nil || h3.Base().Matcher != "write_file" {
		t.Error("write_file 应优先匹配精确的 matcher，而非通配符")
	}
}

// TestMatchPostHook 测试post-hook匹配逻辑
func TestMatchPostHook(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		PostToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionAllow}, Command: "echo done"},
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
		},
	})

	// matcher="bash" 应匹配 bash 工具
	h := m.MatchPostHook("bash", "")
	if h == nil {
		t.Fatal("expected匹配到 bash 钩子，但返回 nil")
	}
	if h.Base().Matcher != "bash" {
		t.Errorf("Matcher 错误: got %q, want %q", h.Base().Matcher, "bash")
	}

	// matcher="*" 应匹配所有工具
	h2 := m.MatchPostHook("read_file", "")
	if h2 == nil {
		t.Fatal("expected匹配到 * 钩子，但返回 nil")
	}
	if h2.Base().Matcher != "*" {
		t.Errorf("Matcher 错误: got %q, want %q", h2.Base().Matcher, "*")
	}
}

// TestExecuteHook_Block 测试阻止动作：Action=block 时工具不执行
func TestExecuteHook_Block(t *testing.T) {
	m := NewManager("")
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "write_file",
			Action:  ActionBlock,
			Reason:  "写入操作被禁止",
		},
	}

	result := m.ExecuteHook(h, "/tmp/test.go")

	if result.Action != ActionBlock {
		t.Errorf("Action 错误: got %q, want %q", result.Action, ActionBlock)
	}
	if result.Reason != "写入操作被禁止" {
		t.Errorf("Reason 错误: got %q, want %q", result.Reason, "写入操作被禁止")
	}
	if result.Output != "" {
		t.Errorf("Block 动作不应有输出: got %q", result.Output)
	}
}

// TestExecuteHook_Allow 测试允许动作：Action=allow 且无命令时工具正常执行
func TestExecuteHook_Allow(t *testing.T) {
	m := NewManager("")
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "read_file",
			Action:  ActionAllow,
		},
	}

	result := m.ExecuteHook(h, "/tmp/test.go")

	if result.Action != ActionAllow {
		t.Errorf("Action 错误: got %q, want %q", result.Action, ActionAllow)
	}
	if result.Output != "" {
		t.Errorf("无命令的 Allow 动作不应有输出: got %q", result.Output)
	}
	if result.Error != nil {
		t.Errorf("Allow 动作不应有错误: %v", result.Error)
	}
}

// TestExecuteHook_Ask 测试询问动作：Action=ask 且有命令时，需要User确认
func TestExecuteHook_Ask(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "bash",
			Action:  ActionAsk,
			Reason:  "需要确认执行命令",
		},
		Command: "echo check",
	}

	result := m.ExecuteHook(h, "")

	if result.Action != ActionAsk {
		t.Errorf("Action 错误: got %q, want %q", result.Action, ActionAsk)
	}
	if result.Reason != "需要确认执行命令" {
		t.Errorf("Reason 错误: got %q, want %q", result.Reason, "需要确认执行命令")
	}
}

// TestExecuteHook_Command 测试命令执行：Command 字段指定的命令被执行并捕获输出
func TestExecuteHook_Command(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "bash",
			Action:  ActionAllow,
		},
		Command: "echo hello",
	}

	result := m.ExecuteHook(h, "")

	if result.Action != ActionAllow {
		t.Errorf("Action 错误: got %q, want %q", result.Action, ActionAllow)
	}
	if result.Error != nil {
		t.Errorf("命令执行不应有错误: %v", result.Error)
	}

	output := strings.TrimSpace(result.Output)
	if output != "hello" {
		t.Errorf("输出错误: got %q, want %q", output, "hello")
	}
}

func TestExecuteHook_TemplateReplacement(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)

	testFile := filepath.Join(tmpDir, "test.go")
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "echo $env:HOOK_FILE"
	} else {
		cmd = "echo $HOOK_FILE"
	}
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "write_file",
			Action:  ActionAllow,
		},
		Command: cmd,
	}

	result := m.ExecuteHook(h, testFile)

	if result.Error != nil {
		t.Errorf("命令执行不应有错误: %v", result.Error)
	}

	output := strings.TrimSpace(result.Output)
	normalizedOutput := filepath.ToSlash(output)
	normalizedFile := filepath.ToSlash(testFile)
	if !strings.Contains(normalizedOutput, normalizedFile) && !strings.Contains(output, testFile) {
		t.Errorf("输出should contain文件路径 %q, got %q", testFile, output)
	}
}

// TestRunPostHooks_Modified 测试post-hook修改工具输出：PostHook 执行命令后 Modified 字段被设置
func TestRunPostHooks_Modified(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetConfig(HookConfig{
		PostToolUse: []Hook{
			&HookCommand{
				HookBase: HookBase{
					Matcher: "read_file",
					Action:  ActionAllow,
				},
				Command: "echo modified_output",
			},
		},
	})

	input := `{"path": "/tmp/test.go"}`
	result := m.RunPostHooks("read_file", input, "original output")

	if result.Modified == nil {
		t.Fatal("expected Modified 不为 nil，但得到 nil")
	}

	modified := strings.TrimSpace(*result.Modified)
	if modified != "modified_output" {
		t.Errorf("Modified 输出错误: got %q, want %q", modified, "modified_output")
	}
}

// TestMatchGlob 测试 glob 模式匹配：*.go 匹配 main.go，不匹配 main.py
func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.py", false},
		{"*", "anything", true},
		{"write_file", "write_file", true},
		{"write_file", "read_file", false},
		{"*.go", "src/main.go", true},     // 不含 / 的模式匹配 basename
		{"src/*.go", "src/main.go", true}, // 含 / 的模式匹配完整路径
		{"src/*.go", "lib/main.go", false},
	}

	for _, tt := range tests {
		got := matchGlob(tt.pattern, tt.name)
		if got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

// TestExtractPathFromInput 测试从 JSON 输入中提取 path 字段
func TestExtractPathFromInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "正常路径提取",
			input: `{"path": "/tmp/test.go"}`,
			want:  "/tmp/test.go",
		},
		{
			name:  "Windows 路径提取",
			input: `{"path": "C:\\Users\\test\\file.go"}`,
			want:  `C:\Users\test\file.go`,
		},
		{
			name:  "无 path 字段",
			input: `{"content": "hello"}`,
			want:  "",
		},
		{
			name:  "无效 JSON",
			input: "not json",
			want:  "",
		},
		{
			name:  "path 为非字符串类型",
			input: `{"path": 123}`,
			want:  "",
		},
		{
			name:  "空 JSON 对象",
			input: `{}`,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPathFromInput(tt.input)
			if got != tt.want {
				t.Errorf("extractPathFromInput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestMatchStopHook 测试停止钩子匹配逻辑
func TestMatchStopHook(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		Stop: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}, Command: "echo session_ended"},
		},
	})

	h := m.MatchStopHook("any_tool", "")
	if h == nil {
		t.Fatal("expected匹配到 Stop 钩子，但返回 nil")
	}
	if h.Base().Matcher != "*" {
		t.Errorf("Matcher 错误: got %q, want %q", h.Base().Matcher, "*")
	}
}

// TestRunStopHooks 测试停止钩子执行
func TestRunStopHooks(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetConfig(HookConfig{
		Stop: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}, Command: "echo stop_hook"},
		},
	})

	result := m.RunStopHooks()
	if result.Action != ActionAllow {
		t.Errorf("Action 错误: got %q, want %q", result.Action, ActionAllow)
	}
	if result.Error != nil {
		t.Errorf("停止钩子执行不应有错误: %v", result.Error)
	}
	output := strings.TrimSpace(result.Output)
	if output != "stop_hook" {
		t.Errorf("输出错误: got %q, want %q", output, "stop_hook")
	}
}

// TestRunStopHooks_Block 测试停止钩子阻止动作
// Stop 是生命周期事件，deny 被忽略（对标同类 CLI）
func TestRunStopHooks_Block(t *testing.T) {
	m := NewManager("")
	m.SetConfig(HookConfig{
		Stop: []Hook{
			&HookCommand{HookBase: HookBase{Action: ActionBlock, Reason: "停止被禁止"}},
		},
	})

	result := m.RunStopHooks()
	// 生命周期事件：deny 被忽略，返回 Allow + 警告消息
	if result.Action != ActionAllow {
		t.Errorf("Action 错误: got %q, want %q (lifecycle deny should be ignored)", result.Action, ActionAllow)
	}
	if len(result.Messages) == 0 {
		t.Error("Expected warning message about ignored deny")
	}
}

// TestStopHook_YAML 测试 YAML 配置中包含 stop 钩子
func TestStopHook_YAML(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `pre_tool_use:
  - matcher: write_file
    action: block
stop:
  - matcher: "*"
    action: allow
    command: echo session_saved
`
	configPath := filepath.Join(tmpDir, "hooks.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("写入config file失败: %v", err)
	}

	m := NewManager(tmpDir)
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config失败: %v", err)
	}

	loaded := m.Config()

	if len(loaded.Stop) != 1 {
		t.Errorf("Stop 钩子数量错误: got %d, want 1", len(loaded.Stop))
	}
	if loaded.Stop[0].Base().Matcher != "*" {
		t.Errorf("Stop[0].Matcher 错误: got %q, want %q", loaded.Stop[0].Base().Matcher, "*")
	}
	if loaded.Stop[0].(*HookCommand).Command != "echo session_saved" {
		t.Errorf("Stop[0].Command 错误: got %q, want %q", loaded.Stop[0].(*HookCommand).Command, "echo session_saved")
	}
}

// TestPostCompactHook 测试压缩后钩子执行
func TestPostCompactHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		PostCompact: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}, Command: "echo compact-done"},
		},
	})
	result := mgr.RunPostCompactHooks()
	if result.Action != ActionAllow {
		t.Error("PostCompact hook should allow")
	}
}

// TestSessionStartHook 测试会话启动钩子执行
func TestSessionStartHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		SessionStart: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}, Command: "echo session-started"},
		},
	})
	result := mgr.RunSessionStartHooks()
	if result.Action != ActionAllow {
		t.Error("SessionStart hook should allow")
	}
}

// TestSessionStopHook 测试会话停止钩子执行
func TestSessionStopHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		SessionStop: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionBlock, Reason: "blocked"}},
		},
	})
	result := mgr.RunSessionStopHooks()
	// SessionStop 是生命周期事件，deny 被忽略
	if result.Action != ActionAllow {
		t.Errorf("SessionStop lifecycle deny should be ignored, got %q", result.Action)
	}
	if len(result.Messages) == 0 {
		t.Error("Expected warning message about ignored deny")
	}
}

// TestPreEditHook 测试编辑前钩子执行
func TestPreEditHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		PreEdit: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "edit_file", Pattern: "*.go", Action: ActionAllow}, Command: "echo pre-edit"},
		},
	})
	result := mgr.RunPreEditHooks("edit_file", "test.go")
	if result.Action != ActionAllow {
		t.Error("PreEdit hook should allow for .go files")
	}
}

// TestPostEditHook 测试编辑后钩子执行
func TestPostEditHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		PostEdit: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "edit_file", Pattern: "*.go", Action: ActionAllow}, Command: "echo post-edit"},
		},
	})
	result := mgr.RunPostEditHooks("edit_file", "test.go", "old content", "new content")
	if result.Action != ActionAllow {
		t.Error("PostEdit hook should allow")
	}
}

// TestPreBashHook 测试 Bash 执行前钩子
func TestPreBashHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		PreBash: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionBlock, Reason: "bash blocked"}},
		},
	})
	result := mgr.RunPreBashHooks("rm -rf /")
	if result.Action != ActionBlock {
		t.Error("PreBash hook should block dangerous command")
	}
}

// TestPostBashHook 测试 Bash 执行后钩子
func TestPostBashHook(t *testing.T) {
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		PostBash: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionAllow}, Command: "echo post-bash"},
		},
	})
	result := mgr.RunPostBashHooks("echo hello", "hello\n")
	if result.Action != ActionAllow {
		t.Error("PostBash hook should allow")
	}
}

// TestHookChain 测试多钩子链式执行
func TestHookChain(t *testing.T) {
	// 多个钩子应按顺序触发
	mgr := NewManager("")
	mgr.SetConfig(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionAllow}, Command: "echo pre1"},
		},
		PreBash: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionAllow}, Command: "echo pre2"},
		},
	})

	// PreToolUse 先触发
	result1 := mgr.RunPreHooks("bash", `{"command": "echo test"}`)
	if result1.Action != ActionAllow {
		t.Error("PreToolUse hook should allow")
	}

	// PreBash 后触发
	result2 := mgr.RunPreBashHooks("echo test")
	if result2.Action != ActionAllow {
		t.Error("PreBash hook should allow")
	}
}

// TestAddHook_RespectsEnabledFalse 回归测试：AddHook 不应强制覆盖 Enabled=false
// 修复前：AddHook 中 `if !b.Enabled { b.Enabled = true }` 会将用户显式设置的 false 改为 true
// 修复后：Enabled 字段不被强制修改，调用方需自行设置
func TestAddHook_RespectsEnabledFalse(t *testing.T) {
	m := NewManager(t.TempDir())

	// 显式设置 Enabled=false 的钩子
	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "write_file",
			Action:  ActionBlock,
			Reason:  "disabled hook",
			Enabled: false,
		},
		Command: "echo should_not_run",
	}

	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook failed: %v", err)
	}

	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}

	// 验证 Enabled 仍为 false（未被 AddHook 强制改为 true）
	if cfg.PreToolUse[0].Base().Enabled {
		t.Error("BUG: AddHook should not force Enabled=true when caller explicitly set Enabled=false")
	}
}

// TestAddHook_DefaultEnabledTrue 回归测试：显式设 Enabled=true 的钩子保持 true
func TestAddHook_DefaultEnabledTrue(t *testing.T) {
	m := NewManager(t.TempDir())

	h := &HookCommand{
		HookBase: HookBase{
			Matcher: "bash",
			Action:  ActionBlock,
			Reason:  "enabled hook",
			Enabled: true,
		},
	}

	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook failed: %v", err)
	}

	cfg := m.Config()
	if !cfg.PreToolUse[0].Base().Enabled {
		t.Error("Enabled should remain true after AddHook")
	}
}

// TestToHook_SetsSourceConfig 回归测试：toHook 应将 Source 设为 SourceConfig
// 修复前：从配置文件反序列化的钩子 Source 为空字符串（json:"-" 不参与序列化）
// 修复后：toHook 统一设为 SourceConfig，与 RemoveAllPluginHooks 过滤逻辑兼容
func TestToHook_SetsSourceConfig(t *testing.T) {
	raw := hookRaw{
		Type:    HookTypeCommand,
		Matcher: "*",
		Command: "echo test",
	}
	h := raw.toHook()
	if h.Base().Source != SourceConfig {
		t.Errorf("toHook should set Source=SourceConfig, got %q", h.Base().Source)
	}

	// 验证从 JSON 加载的钩子也有 Source
	config := `{"pre_tool_use": [{"matcher": "bash", "command": "echo allowed"}]}`
	mgr := NewManager(t.TempDir())
	if err := mgr.LoadConfigFromBytes([]byte(config)); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	cfg := mgr.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
	if cfg.PreToolUse[0].Base().Source != SourceConfig {
		t.Errorf("loaded hook should have Source=SourceConfig, got %q", cfg.PreToolUse[0].Base().Source)
	}
}
