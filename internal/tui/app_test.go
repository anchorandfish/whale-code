package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// TestMain 隔离 HOME 到临时目录，避免 filehistory/permissions 等
// 持久化文件被多个测试共享导致 flaky（参见 docs/使用说明/07-挂网前补充建议.md §1.4）。
// 每次测试运行创建独立 HOME，结束后清理。
func TestMain(m *testing.M) {
	// 创建临时 HOME 目录
	tmpHome, err := os.MkdirTemp("", "whale-tui-test-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MkdirTemp failed: %v\n", err)
		os.Exit(1)
	}
	// 测试结束后清理
	defer os.RemoveAll(tmpHome)

	// 设置 HOME 环境变量，让 filehistory 等模块初始化到临时目录
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome) // Windows 兼容
	defer os.Unsetenv("HOME")
	defer os.Unsetenv("USERPROFILE")

	// 创建 .whale 子目录（部分模块直接拼接路径不创建）
	os.MkdirAll(filepath.Join(tmpHome, ".whale"), 0755)

	code := m.Run()
	os.Exit(code)
}

// newTestClient creates a test AI client
// 使用虚拟 API Key 和 Base URL，仅用于初始化，不actual发起请求
func newTestClient(model string) *ai.SimpleClient {
	return ai.NewSimpleClient(model, "test-key", "http://localhost:0", ai.ProviderOpenAI)
}

// newTestRegistry creates a test模型注册表
// 注册一个测试模型，使 NewModel 不会因空注册表面出错
func newTestRegistry() *ai.ModelRegistry {
	reg := ai.NewModelRegistry()
	reg.Add(&ai.ModelConfig{
		Name:            "test-model",
		Provider:        ai.ProviderOpenAI,
		BaseURL:         "http://localhost:0",
		APIKey:          "test-key",
		ContextWindow:   200000,
		MaxOutputTokens: 16384,
	})
	reg.Add(&ai.ModelConfig{
		Name:            "initial-model",
		Provider:        ai.ProviderOpenAI,
		BaseURL:         "http://localhost:0",
		APIKey:          "test-key",
		ContextWindow:   200000,
		MaxOutputTokens: 16384,
	})
	return reg
}

// TestApp_New 测试创建 TUI 应用实例
// 通过 NewModel 创建 Model 实例，验证关键字段被正确初始化，
// 包括 modelName、workDir、config 等
func TestApp_New(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite

	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	if m.modelName != "test-model" {
		t.Errorf("expected modelName = %q，actual %q", "test-model", m.modelName)
	}
	if m.config == nil {
		t.Error("config 不should be nil")
	}
	if m.config.PermissionMode != config.PermWorkspaceWrite {
		t.Errorf("expected PermissionMode = %q，actual %q", config.PermWorkspaceWrite, m.config.PermissionMode)
	}
	if m.workDir == "" {
		t.Error("workDir should not be empty")
	}
	if m.session == nil {
		t.Error("session 不should be nil")
	}
	if m.registry == nil {
		t.Error("registry 不should be nil")
	}
	if m.policy == nil {
		t.Error("policy 不should be nil")
	}
	if m.state != StateIdle {
		t.Errorf("初始状态应为 StateIdle(%d)，actual %d", StateIdle, m.state)
	}
	if m.quitting {
		t.Error("初始 quitting 应为 false")
	}
}

// TestApp_SetModel test setup模型名称
// 验证 Model 的 modelName 字段可被正确设置和读取，
// 这是 /model 命令切换模型的核心逻辑
func TestApp_SetModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "initial-model"

	client := newTestClient("initial-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	if m.modelName != "initial-model" {
		t.Errorf("初始 modelName expected %q，actual %q", "initial-model", m.modelName)
	}

	// mock切换模型
	m.modelName = "switched-model"
	if m.modelName != "switched-model" {
		t.Errorf("切换后 modelName expected %q，actual %q", "switched-model", m.modelName)
	}
}

// TestApp_SetPrompt test setup初始提示
// 验证 SendPrompt 方法能正确设置 textarea 的值，
// 这是 -p command line argument模式的核心功能
func TestApp_SetPrompt(t *testing.T) {
	cfg := config.DefaultConfig()
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	prompt := "help me write a Go HTTP server"
	m.SendPrompt(prompt)

	// 验证 textarea 的值被正确设置
	if m.textarea.Value() != prompt {
		t.Errorf("SendPrompt 后 textarea 值expected %q，actual %q", prompt, m.textarea.Value())
	}
}

// TestApp_Version verifies that startup is concise and leaves runtime details
// to the fixed footer.
func TestApp_Version(t *testing.T) {
	model := "qwen3.5-plus"
	workDir := "/home/user/project"
	permMode := "workspace-write"

	welcome := buildWelcome(model, workDir, permMode)

	// 验证版本号
	if !strings.Contains(welcome, "Whale Code v0.1.0") {
		t.Error("欢迎信息should contain版本号 'Whale Code v0.1.0'")
	}

	for _, duplicate := range []string{model, workDir, permMode} {
		if strings.Contains(welcome, duplicate) {
			t.Errorf("welcome should not duplicate footer value %q", duplicate)
		}
	}

	// 验证包含常用命令提示
	if !strings.Contains(welcome, "/help") {
		t.Error("欢迎信息should contain /help 命令")
	}
	if !strings.Contains(welcome, "/copy all") {
		t.Error("welcome should contain history copy command")
	}
}

// TestApp_BuildAvailableModels 测试构建可用模型列表
// 验证 buildAvailableModels 函数能从配置中提取模型名列表
func TestApp_BuildAvailableModels(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "primary-model"
	cfg.FallbackModel = "fallback-model"

	models := buildAvailableModels(&cfg)

	if len(models) != 2 {
		t.Fatalf("expected 2 个模型，actual %d 个", len(models))
	}
	if models[0] != "primary-model" {
		t.Errorf("第一个模型expected %q，actual %q", "primary-model", models[0])
	}
	if models[1] != "fallback-model" {
		t.Errorf("第二个模型expected %q，actual %q", "fallback-model", models[1])
	}
}

// TestApp_BuildAvailableModels_Empty 测试空配置的模型列表
// 验证当 Model 和 FallbackModel 都为空时，返回空列表
func TestApp_BuildAvailableModels_Empty(t *testing.T) {
	cfg := config.DefaultConfig()

	models := buildAvailableModels(&cfg)

	if len(models) != 0 {
		t.Errorf("空配置expected 0 个模型，actual %d 个", len(models))
	}
}

// TestApp_TruncateRunes 测试字符串截断函数
// 验证 truncateRunes 能正确截断超长字符串并在末尾添加省略号，
// 对短字符串保持不变，正确处理 UTF-8 字符
func TestApp_TruncateRunes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		n      int
		expect string
	}{
		{"短字符串不截断", "hello", 10, "hello"},
		{"精确长度不截断", "hello", 5, "hello"},
		{"超长截断加省略号", "hello world", 5, "hello..."},
		{"空字符串", "", 5, ""},
		{"中文截断", "你好世界测试", 3, "你好世..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.input, tt.n)
			if got != tt.expect {
				t.Errorf("truncateRunes(%q, %d) = %q，expected %q", tt.input, tt.n, got, tt.expect)
			}
		})
	}
}

// TestApp_PluralS 测试英文复数后缀函数
// 验证 pluralS 在数量为 1 时返回空字符串，其他情况返回 "s"
func TestApp_PluralS(t *testing.T) {
	if pluralS(1) != "" {
		t.Errorf("pluralS(1) expected空字符串，actual %q", pluralS(1))
	}
	if pluralS(0) != "s" {
		t.Errorf("pluralS(0) expected \"s\"，actual %q", pluralS(0))
	}
	if pluralS(2) != "s" {
		t.Errorf("pluralS(2) expected \"s\"，actual %q", pluralS(2))
	}
}

// TestApp_ConfigToPermissionLevel 测试配置权限模式 → 运行期档位的转换。
// 走生产装配入口 permission.NewPolicyFromModeName（configToPermissionLevel 已并入
// 该路径），验证旧配置里的已删除模式也被兼容翻译。
func TestApp_ConfigToPermissionLevel(t *testing.T) {
	tests := []struct {
		name   string
		mode   config.PermissionMode
		expect string
	}{
		{"read-only mode", config.PermReadOnly, "read-only"},
		{"工作区写入模式", config.PermWorkspaceWrite, "workspace-write"},
		{"full access mode", config.PermDangerFullAccess, "danger-full-access"},
		// 阶段 2 收模式：旧配置里的已删除模式必须被兼容翻译（而非落到 default 的 ww）
		{"legacy accept-edits", config.PermissionMode("accept-edits"), "workspace-write"},
		{"legacy dont-ask", config.PermissionMode("dont-ask"), "danger-full-access"},
		{"legacy plan", config.PermissionMode("plan"), "read-only"},
		{"legacy auto", config.PermissionMode("auto"), "workspace-write"},
		{"legacy bubble", config.PermissionMode("bubble"), "workspace-write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := permission.NewPolicyFromModeName(string(tt.mode))
			if p.Mode().String() != tt.expect {
				t.Errorf("NewPolicyFromModeName(%q).Mode() = %q，expected %q", tt.mode, p.Mode().String(), tt.expect)
			}
		})
	}
}

func TestPermissionsCommandOpensModePicker(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)

	output := m.handleCommand("/permissions")
	if !strings.Contains(output, "当前权限：workspace-write") {
		t.Fatalf("权限菜单输出应显示当前模式，actual %q", output)
	}
	if m.sessionService.State() != StateWaitingChoice {
		t.Fatalf("/permissions 应进入选择状态，actual %v", m.sessionService.State())
	}
	pending := m.toolService.PendingChoice()
	if pending == nil || pending.ToolID != permissionModeChoiceToolID {
		t.Fatal("/permissions 应创建专用权限选择，而不是 AskUser 选择")
	}
	if pending.Options[m.toolService.ChoiceIdx()].Value != "workspace-write" {
		t.Fatalf("当前模式应默认选中 workspace-write，actual %q", pending.Options[m.toolService.ChoiceIdx()].Value)
	}
}

func TestPermissionsModePickerEnterAppliesMode(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)
	m.handleCommand("/permissions")

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := updatedModel.(Model)
	updated.reassignServices()
	updatedModel, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = updatedModel.(Model)
	updated.reassignServices()
	if cmd == nil {
		t.Fatal("Enter 应返回权限选择结果命令")
	}
	result, ok := cmd().(choiceResultMsg)
	if !ok || result.ToolID != permissionModeChoiceToolID || result.Cancelled || result.Answer != "read-only" {
		t.Fatalf("Enter 结果错误：%#v", result)
	}
	updatedModel, _ = updated.Update(result)
	updated = updatedModel.(Model)
	updated.reassignServices()
	if got := updated.toolService.Policy().Mode(); got != permission.LevelReadOnly {
		t.Fatalf("Enter 应应用 read-only，actual %s", got)
	}
	if updated.sessionService.State() != StateIdle {
		t.Fatalf("应用权限后应回到 Idle，actual %v", updated.sessionService.State())
	}
}

func TestPermissionsModePickerEscKeepsMode(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.toolService.Policy().SetMode(permission.LevelWorkspaceWrite)
	m.handleCommand("/permissions")

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := updatedModel.(Model)
	updated.reassignServices()
	if cmd == nil {
		t.Fatal("Esc 应返回权限取消结果命令")
	}
	result, ok := cmd().(choiceResultMsg)
	if !ok || result.ToolID != permissionModeChoiceToolID || !result.Cancelled {
		t.Fatalf("Esc 结果错误：%#v", result)
	}
	updatedModel, _ = updated.Update(result)
	updated = updatedModel.(Model)
	updated.reassignServices()
	if got := updated.toolService.Policy().Mode(); got != permission.LevelWorkspaceWrite {
		t.Fatalf("Esc 不应修改权限，actual %s", got)
	}
	if updated.sessionService.State() != StateIdle {
		t.Fatalf("取消权限菜单后应回到 Idle，actual %v", updated.sessionService.State())
	}
}

func TestPermissionsExplicitModeCommandRemainsCompatible(t *testing.T) {
	m := newTUIRegressionModel(t)
	output := m.handleCommand("/permissions read-only")
	if !strings.Contains(output, "Permission: read-only") {
		t.Fatalf("完整权限命令应继续工作，actual %q", output)
	}
	if got := m.toolService.Policy().Mode(); got != permission.LevelReadOnly {
		t.Fatalf("完整权限命令应切换模式，actual %s", got)
	}
}

// TestUpdate_WindowResize 测试 WindowSizeMsg 更新 width/height
func TestUpdate_WindowResize(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	// 发送窗口大小消息
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	updated := newModel.(Model)

	if updated.width != 120 {
		t.Errorf("width = %d, want 120", updated.width)
	}
	if updated.height != 40 {
		t.Errorf("height = %d, want 40", updated.height)
	}
	if !updated.ready {
		t.Error("ready 应为 true")
	}
}

// TestUpdate_KeyboardInput 测试 KeyMsg 字母键添加到输入缓冲区
func TestUpdate_KeyboardInput(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	// mock输入字母 'a'
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	newModel, _ := m.Update(keyMsg)
	updated := newModel.(Model)

	// textarea should contain输入的字母
	if !strings.Contains(updated.textarea.Value(), "a") {
		t.Errorf("textarea 值should contain 'a'，actual: %q", updated.textarea.Value())
	}
}

// TestSendInput_SlashCommand 测试以 "/" 开头的输入触发命令处理
func TestSendInput_SlashCommand(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	// 设置斜杠命令输入
	m.textarea.SetValue("/help")
	newModel, _ := m.sendInput()
	updated := newModel.(Model)

	// 验证命令输出被添加到消息中
	found := false
	for _, msg := range updated.Messages() {
		if strings.Contains(msg, "help") || strings.Contains(msg, "命令") {
			found = true
			break
		}
	}
	if !found {
		t.Error("斜杠命令 /help 应产生包含 'help' 或 '命令' 的输出消息")
	}
}

// TestSendInput_NormalMessage 测试普通文本输入创建User消息
func TestSendInput_NormalMessage(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	// 设置普通文本输入
	m.textarea.SetValue("hello world")
	newModel, _ := m.sendInput()
	updated := newModel.(Model)

	// 验证User消息被添加到消息列表
	found := false
	for _, msg := range updated.Messages() {
		if strings.Contains(msg, "hello world") {
			found = true
			break
		}
	}
	if !found {
		t.Error("普通文本输入应产生包含 'hello world' 的User消息")
	}

	// 验证状态变为 streaming
	if updated.state != StateStreaming {
		t.Errorf("state = %d, want StateStreaming(%d)", updated.state, StateStreaming)
	}
}

// TestBuildSystemPrompt 测试系统提示包含工具列表、项目上下文、记忆和权限模式
func TestBuildSystemPrompt(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	prompts := m.buildSystemPrompt()

	// 合并所有提示用于检查（N15：buildSystemPrompt 返回 []ai.PromptLayerBlock）
	combined := strings.Join(layerBlocksToStrings(prompts), " ")

	// 验证包含工具列表提示
	if !strings.Contains(combined, "tool") && !strings.Contains(combined, "Tool") {
		t.Error("系统提示should contain工具相关说明")
	}

	// 验证包含工作目录（项目上下文）
	if !strings.Contains(combined, "Working directory") {
		t.Error("系统提示should contain工作目录信息")
	}

	// 验证包含当前模型
	if !strings.Contains(combined, "Current model") {
		t.Error("系统提示should contain当前模型信息")
	}

	// 验证包含搜索策略提示（来自 tools.SearchStrategyPrompt）
	if !strings.Contains(combined, "search") && !strings.Contains(combined, "Search") {
		t.Error("系统提示should contain搜索策略提示")
	}
}

// TestHandleStreamEvent 测试 handleStreamEvent 方法对各种流事件的处理
// 验证不同类型的 AI 事件能正确更新 Model 的内部状态
func TestHandleStreamEvent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	t.Run("TextDeltaEvent_追加文本并设置dirty", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()

		events := make(chan ai.Event)
		close(events)

		m.handleStreamEvent(ai.TextDeltaEvent{Content: "Hello "}, events)
		m.handleStreamEvent(ai.TextDeltaEvent{Content: "World"}, events)

		if got := m.streamingText(); got != "Hello World" {
			t.Errorf("streamingText expected %q，actual %q", "Hello World", got)
		}
		if m.currentText != "" {
			t.Errorf("生产流式路径不应双写 currentText，actual %q", m.currentText)
		}
		if !m.dirty {
			t.Error("处理 TextDeltaEvent 后 dirty 标志应为 true")
		}
	})

	t.Run("ThinkingDeltaEvent_追加思考内容", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()

		events := make(chan ai.Event)
		close(events)

		m.handleStreamEvent(ai.ThinkingDeltaEvent{Content: "让我想想..."}, events)
		m.handleStreamEvent(ai.ThinkingDeltaEvent{Content: "继续思考"}, events)

		if got := m.streamingThinking(); got != "让我想想...继续思考" {
			t.Errorf("streamingThinking expected %q，actual %q", "让我想想...继续思考", got)
		}
		if m.currentThinking != "" {
			t.Errorf("生产流式路径不应双写 currentThinking，actual %q", m.currentThinking)
		}
	})

	t.Run("ToolUseEvent_添加待执行工具并初始化输入缓冲", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()

		events := make(chan ai.Event)
		close(events)

		m.handleStreamEvent(ai.ToolUseEvent{ID: "tool_1", Name: "read_file"}, events)

		if len(m.pendingTools) != 1 {
			t.Fatalf("pendingTools 长度expected 1，actual %d", len(m.pendingTools))
		}
		if m.pendingTools[0].ID != "tool_1" {
			t.Errorf("pendingTools[0].ID expected %q，actual %q", "tool_1", m.pendingTools[0].ID)
		}
		if m.pendingTools[0].Name != "read_file" {
			t.Errorf("pendingTools[0].Name expected %q，actual %q", "read_file", m.pendingTools[0].Name)
		}
		if m.pendingTools[0].Type != "tool_use" {
			t.Errorf("pendingTools[0].Type expected %q，actual %q", "tool_use", m.pendingTools[0].Type)
		}
		if _, ok := m.toolInputBuf["tool_1"]; !ok {
			t.Error("toolInputBuf should contain key 'tool_1'")
		}
		if m.currentToolID != "tool_1" {
			t.Errorf("currentToolID expected %q，actual %q", "tool_1", m.currentToolID)
		}
	})

	t.Run("ToolUseEvent_带Input初始化输入缓冲", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()

		events := make(chan ai.Event)
		close(events)

		m.handleStreamEvent(ai.ToolUseEvent{ID: "tool_2", Name: "bash", Input: `{"command":"ls"}`}, events)

		if m.toolInputBuf["tool_2"] != `{"command":"ls"}` {
			t.Errorf("toolInputBuf[tool_2] expected %q，actual %q", `{"command":"ls"}`, m.toolInputBuf["tool_2"])
		}
	})

	t.Run("UsageEvent_按请求累计快照记账", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()
		m.aiService.SetRequestID("usage-request")
		m.beginRequestUsage("usage-request")

		events := make(chan ai.Event)
		close(events)

		m.handleStreamEvent(ai.UsageEvent{InputTokens: 100, OutputTokens: 50}, events)
		m.handleStreamEvent(ai.UsageEvent{InputTokens: 200, OutputTokens: 80}, events)
		m.handleStreamEvent(ai.MessageStopEvent{}, events)

		if m.inputTokens != 200 {
			t.Errorf("inputTokens expected 200，actual %d", m.inputTokens)
		}
		if m.outputTokens != 80 {
			t.Errorf("outputTokens expected 80，actual %d", m.outputTokens)
		}
	})

	t.Run("MessageStopEvent_触发finalizeStream", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()
		m.activeCell.AppendText("测试文本")

		events := make(chan ai.Event)
		close(events)

		cmd := m.handleStreamEvent(ai.MessageStopEvent{}, events)

		// finalizeStream 返回一个 tea.Cmd，其产生的消息类型应为 streamDoneMsg
		if cmd == nil {
			t.Error("MessageStopEvent should return非 nil 的 tea.Cmd（finalizeStream）")
		}
	})

	t.Run("ErrorEvent_状态回到idle并添加错误消息", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()
		m.currentText = "进行中的文本"

		events := make(chan ai.Event)
		close(events)

		cmd := m.handleStreamEvent(ai.ErrorEvent{Err: fmt.Errorf("连接超时")}, events)

		if m.state != StateIdle {
			t.Errorf("ErrorEvent 后状态应为 StateIdle(%d)，actual %d", StateIdle, m.state)
		}
		if cmd != nil {
			t.Error("ErrorEvent 后should return nil cmd")
		}
		// 验证错误消息被添加到消息列表
		found := false
		for _, msg := range m.Messages() {
			if strings.Contains(msg, "AI Error") || strings.Contains(msg, "连接超时") {
				found = true
				break
			}
		}
		if !found {
			t.Error("ErrorEvent 后应添加包含 'AI Error' 的错误消息")
		}
	})
}

// TestExecuteTools 测试 executeTools 方法的权限检查逻辑
// 验证在不同权限模式下工具执行的行为差异
func TestExecuteTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	t.Run("danger-full-access模式_工具直接执行", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.policy.SetMode(permission.LevelDangerFullAccess)
		m.state = StateToolExec
		m.pendingTools = []session.ContentBlock{
			{Type: "tool_use", ID: "t1", Name: "read_file", Input: `{"path":"test.go"}`},
		}

		cmd := m.executeTools()

		// danger-full-access 模式下应直接执行，返回非 nil 的 tea.Cmd
		if cmd == nil {
			t.Error("danger-full-access 模式下 executeTools should return非 nil 的 tea.Cmd")
		}
		// 状态不应变为 StateWaitingConfirm
		if m.state == StateWaitingConfirm {
			t.Error("danger-full-access 模式下状态不应变为 StateWaitingConfirm")
		}
	})

	t.Run("workspace-write模式_受保护路径写仍需确认", func(t *testing.T) {
		// ⚠️ 2026-09-15 有意翻转（记录在案，勿"修回"）：本用例原断言 write_file 到普通
		// 相对路径「必须确认」，依赖旧语义「ww 档下任何写调用都要问」。新语义对齐业内既有实现
		// assess_patch_safety：**目标落在工作区内且非受保护路径的写**免确认
		// （internal/tools/pipeline.go workspaceContainedWrite）⇒ 该断言不再成立，
		// 且原 fixture 会真的写出 internal/tui/test.go 污染工作树。
		// 改用受保护路径 .git/config（permission.ProtectedPaths 含 ".git" 片段）：
		// SafetyCheckStage 排在 PermissionStage 之前，命中即短路返回 NeedConfirm，
		// 免问收窄**结构上够不着**它 —— 于是本用例从"写工具都要问"收窄为更准确的
		// 「豁免不得绕过受保护路径」，判别力更强而非更弱。
		t.Chdir(t.TempDir()) // 防御：即便将来本用例改为执行，也不会污染仓库工作树
		m := NewModel(&cfg, client, reg)
		m.policy.SetMode(permission.LevelWorkspaceWrite)
		m.state = StateToolExec
		m.pendingTools = []session.ContentBlock{
			{Type: "tool_use", ID: "t2", Name: "write_file", Input: `{"path":".git/config","content":"hello"}`},
		}

		cmd := m.executeTools()

		// Pipeline confirmation is discovered by the asynchronous command so the
		// pipeline remains the single source of safety decisions.
		if cmd == nil {
			t.Fatal("workspace-write protected-path write should return a command")
		}
		result := cmd()
		if _, ok := result.(pipelineConfirmMsg); !ok {
			t.Errorf("expected pipelineConfirmMsg for protected-path write, got %T", result)
		}
	})

	t.Run("workspace-write模式_读工具直接执行", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.policy.SetMode(permission.LevelWorkspaceWrite)
		m.state = StateToolExec
		m.pendingTools = []session.ContentBlock{
			{Type: "tool_use", ID: "t3", Name: "read_file", Input: `{"path":"test.go"}`},
		}

		cmd := m.executeTools()

		// workspace-write 模式下 read_file 不需确认，应直接执行
		if m.state == StateWaitingConfirm {
			t.Error("workspace-write 模式下 read_file 不应触发确认")
		}
		if cmd == nil {
			t.Error("workspace-write 模式下 read_file should return非 nil 的 tea.Cmd（直接执行）")
		}
	})

	t.Run("read-only模式_写工具返回终止型权限拒绝", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.reassignServices()
		m.policy.SetMode(permission.LevelReadOnly)
		m.state = StateToolExec
		m.pendingTools = []session.ContentBlock{
			{Type: "tool_use", ID: "read-only-write", Name: "write_file", Input: `{"path":"approval-marker.txt","content":"CHANGED"}`},
		}

		cmd := m.executeTools()
		if cmd == nil {
			t.Fatal("read-only rejection should return an asynchronous tool result")
		}
		msg := cmd()
		toolMsg, ok := msg.(toolResultMsg)
		if !ok || len(toolMsg.results) != 1 {
			t.Fatalf("read-only rejection returned %T with results=%v", msg, toolMsg.results)
		}
		result := toolMsg.results[0]
		if result.FailureKind != tools.FailureKindPolicyDenied {
			t.Fatalf("failure kind = %q, want %q", result.FailureKind, tools.FailureKindPolicyDenied)
		}
		// ★ ：专属分支标记随双语文案改为英文主行
		// （形态 SSOT = util.Bi；改前是行内并列 "Permission denied / 权限拒绝"）。
		if strings.Contains(result.Output, "Pipeline rejected") || !strings.Contains(result.Output, "Permission denied\n") {
			t.Fatalf("permission result should be concise and user-facing, got %q", result.Output)
		}
	})

	t.Run("异步工具结果绑定启动时request代次", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.reassignServices()
		m.policy.SetMode(permission.LevelDangerFullAccess)
		m.state = StateToolExec
		m.aiService.SetRequestID("old-request")
		m.pendingTools = []session.ContentBlock{
			{Type: "tool_use", ID: "t4", Name: "read_file", Input: `{"path":"missing-file"}`},
		}
		cmd := m.executeTools()
		if cmd == nil {
			t.Fatal("expected asynchronous tool command")
		}
		m.aiService.SetRequestID("new-request")
		msg := cmd()
		result, ok := msg.(toolResultMsg)
		if !ok {
			t.Fatalf("tool command returned %T, want toolResultMsg", msg)
		}
		if result.requestID != "old-request" {
			t.Fatalf("tool result requestID=%q, want old-request", result.requestID)
		}
	})
}

// TestEmptyInput_Ignored 测试空输入被忽略
// 验证当输入为空字符串或仅包含空白字符时，应用不会从 StateIdle state transition，
// 也不会向消息列表添加任何内容
func TestEmptyInput_Ignored(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	t.Run("空字符串输入被忽略", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		initialMsgCount := len(m.Messages())

		m.textarea.SetValue("")
		newModel, _ := m.sendInput()
		updated := newModel.(Model)

		if updated.state != StateIdle {
			t.Errorf("空输入后状态应为 StateIdle(%d)，actual %d", StateIdle, updated.state)
		}
		if len(updated.Messages()) != initialMsgCount {
			t.Errorf("空输入后消息数不应增加: expected %d，actual %d", initialMsgCount, len(updated.Messages()))
		}
	})

	t.Run("仅空白字符输入被忽略", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		initialMsgCount := len(m.Messages())

		m.textarea.SetValue("   \t\n  ")
		newModel, _ := m.sendInput()
		updated := newModel.(Model)

		if updated.state != StateIdle {
			t.Errorf("空白输入后状态应为 StateIdle(%d)，actual %d", StateIdle, updated.state)
		}
		if len(updated.Messages()) != initialMsgCount {
			t.Errorf("空白输入后消息数不应增加: expected %d，actual %d", initialMsgCount, len(updated.Messages()))
		}
	})
}

// TestLongInput_Processed 测试超长输入被正常处理
// 验证 10000 字符的长输入能被正常添加到消息和会话中，
// 不会因长度限制而被忽略或截断
func TestLongInput_Processed(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	// 构造 10000 字符的长输入
	longInput := strings.Repeat("a", 10000)
	m.textarea.SetValue(longInput)
	newModel, _ := m.sendInput()
	updated := newModel.(Model)

	// 验证state transition为 streaming
	if updated.state != StateStreaming {
		t.Errorf("长输入后状态应为 StateStreaming(%d)，actual %d", StateStreaming, updated.state)
	}

	// 验证消息被添加到消息列表
	found := false
	for _, msg := range updated.Messages() {
		if strings.Contains(msg, longInput[:100]) {
			found = true
			break
		}
	}
	if !found {
		t.Error("长输入应被添加到消息列表中")
	}

	// 验证消息被添加到会话
	if len(updated.session.GetMessages()) == 0 {
		t.Error("长输入应被添加到会话消息中")
	} else if updated.session.GetMessages()[0].TextContent() != longInput {
		t.Errorf("会话消息content length = %d, expected 10000", len(updated.session.GetMessages()[0].TextContent()))
	}
}

// TestStateTransitions 测试完整的state transition周期
// 验证 Model 在各种操作下的状态变化符合预期
func TestStateTransitions(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	t.Run("StateIdle到StateStreaming_发送普通消息", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)

		if m.state != StateIdle {
			t.Errorf("初始状态应为 StateIdle(%d)，actual %d", StateIdle, m.state)
		}

		m.textarea.SetValue("hello world")
		newModel, _ := m.sendInput()
		updated := newModel.(Model)

		if updated.state != StateStreaming {
			t.Errorf("发送消息后状态应为 StateStreaming(%d)，actual %d", StateStreaming, updated.state)
		}
	})

	t.Run("StateStreaming到StateIdle_MessageStopEvent完成流", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()
		m.activeCell.AppendText("AI 回复内容")

		events := make(chan ai.Event)
		close(events)

		cmd := m.handleStreamEvent(ai.MessageStopEvent{}, events)

		// MessageStopEvent 触发 finalizeStream，返回 streamDoneMsg 的 Cmd
		if cmd == nil {
			t.Fatal("MessageStopEvent should return非 nil 的 tea.Cmd")
		}

		// mock streamDoneMsg 被处理（无工具调用时回到 idle）
		msg := cmd()
		doneMsg, ok := msg.(streamDoneMsg)
		if !ok {
			t.Fatalf("MessageStopEvent 产生的消息应为 streamDoneMsg，actual %T", msg)
		}

		if len(doneMsg.blocks) == 0 {
			t.Error("有文本内容时 blocks should not be empty")
		}
	})

	t.Run("StateStreaming到StateIdle_ErrorEvent中断流", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.state = StateStreaming
		m.resetStreamState()

		events := make(chan ai.Event)
		close(events)

		m.handleStreamEvent(ai.ErrorEvent{Err: fmt.Errorf("网络错误")}, events)

		if m.state != StateIdle {
			t.Errorf("ErrorEvent 后状态应为 StateIdle(%d)，actual %d", StateIdle, m.state)
		}
	})
}

// TestApp_StatusBarContent test state栏显示运行时关键信息
// 验证 renderStatusBar 输出包含关键字段
func TestApp_StatusBarContent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	m.ready = true
	m.width = 120
	m.height = 40

	// 设置 token 使用量以验证上下文信息
	m.inputTokens = 500
	m.outputTokens = 200

	statusBar := m.renderStatusBar()

	// 验证状态栏包含模型名称
	if !strings.Contains(statusBar, "test-model") {
		t.Error("状态栏should contain模型名称 'test-model'")
	}

	// 验证状态栏包含权限模式
	if !strings.Contains(statusBar, "workspace-write") {
		t.Error("状态栏should contain权限模式 'workspace-write'")
	}

	// 验证状态栏包含清晰的上下文字段和竖线分隔符
	if !strings.Contains(statusBar, "Context:") {
		t.Error("状态栏should contain上下文信息 'Context:'")
	}
	if !strings.Contains(statusBar, " │ ") {
		t.Error("状态栏字段should use竖线分隔")
	}
}

func TestStatusFooterKeepsRequiredFieldsOnNarrowTerminals(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "primary-model"
	cfg.FallbackModel = "fallback-model"
	cfg.FastModel = "fast-model"
	m := NewModel(&cfg, newTestClient("primary-model"), newTestRegistry())
	m.ready = true
	m.width = 24
	m.height = 24
	m.recalcViewportHeight()
	status := m.renderStatusBar()
	for _, field := range []string{"Model:", "Fallback:", "Fast:", "Context", "Permission:", "Workspace:"} {
		if !strings.Contains(status, field) {
			t.Fatalf("narrow footer missing %q: %s", field, status)
		}
	}
	if got := lipgloss.Width(status); got > m.width {
		t.Fatalf("footer width %d exceeds terminal width %d", got, m.width)
	}
	if got := lipgloss.Height(status); got < 3 {
		t.Fatalf("footer should wrap into multiple rows, height=%d", got)
	}
}

func TestMouseCommandTogglesTerminalMode(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.SetMouseMode(true, true)
	if output := m.handleCommand("/mouse off"); !strings.Contains(output, "disabled") {
		t.Fatalf("/mouse off output = %q", output)
	}
	if m.mouseEnabled || m.mouseIdlePreference || m.mouseAutoDuringActive || m.pendingCommandCmd == nil {
		t.Fatal("/mouse off should disable current, idle, and automatic mouse capture")
	}
	m.pendingCommandCmd = nil
	if output := m.handleCommand("/mouse on"); !strings.Contains(output, "enabled") {
		t.Fatalf("/mouse on output = %q", output)
	}
	if !m.mouseEnabled || !m.mouseIdlePreference || m.mouseAutoDuringActive || m.pendingCommandCmd == nil {
		t.Fatal("/mouse on should enable persistent mouse capture and queue a Bubble Tea command")
	}
}

func TestActionButtonFitsNarrowRows(t *testing.T) {
	for _, width := range []int{4, 6, 10, 16, 40} {
		for _, stopping := range []bool{false, true} {
			view := renderActionButton(width, stopping, false, "")
			if lipgloss.Width(view) > width && width > 0 {
				t.Fatalf("button width %d exceeds row %d (stopping=%v): %q", lipgloss.Width(view), width, stopping, view)
			}
			if !strings.Contains(view, "╭") {
				t.Fatalf("button lost top-left border at row %d (stopping=%v): %q", width, stopping, view)
			}
		}
	}
}

func TestInputActionRowFitsNarrowRows(t *testing.T) {
	for _, width := range []int{4, 6, 10, 16, 24, 40, 80} {
		view := renderInputActionRow(width, false, false, "")
		if got := lipgloss.Width(view); got > width {
			t.Fatalf("input action row width %d exceeds available width %d: %q", got, width, view)
		}
		if got := lipgloss.Height(view); got != 3 {
			t.Fatalf("input action row height %d, want stable height 3 at width %d", got, width)
		}
	}
}

// TestApp_RenderNoPanic 测试主视图在各种状态下渲染不会 panic
// 验证 idle、streaming、tool execution 等状态下 View() 均可安全调用
func TestApp_RenderNoPanic(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	client := newTestClient("test-model")
	reg := newTestRegistry()

	t.Run("idle状态渲染不panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("idle 状态 View() 发生 panic: %v", r)
			}
		}()
		m := NewModel(&cfg, client, reg)
		m.ready = true
		m.width = 120
		m.height = 40
		_ = m.View()
	})

	t.Run("streaming状态渲染不panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("streaming 状态 View() 发生 panic: %v", r)
			}
		}()
		m := NewModel(&cfg, client, reg)
		m.ready = true
		m.width = 120
		m.height = 40
		m.state = StateStreaming
		m.activeCell.AppendText("正在生成回复...")
		_ = m.View()
	})

	t.Run("tool执行状态渲染不panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("tool 执行状态 View() 发生 panic: %v", r)
			}
		}()
		m := NewModel(&cfg, client, reg)
		m.ready = true
		m.width = 120
		m.height = 40
		m.state = StateToolExec
		m.pendingTools = []session.ContentBlock{
			{Type: "tool_use", ID: "t1", Name: "bash", Input: `{"command":"ls"}`},
		}
		_ = m.View()
	})

	t.Run("等待确认状态渲染不panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("等待确认状态 View() 发生 panic: %v", r)
			}
		}()
		m := NewModel(&cfg, client, reg)
		m.ready = true
		m.width = 120
		m.height = 40
		m.state = StateWaitingConfirm
		m.pendingConfirm = &PendingConfirm{
			ToolName:  "bash",
			ToolInput: `{"command":"rm -rf /"}`,
			RiskLevel: "dangerous",
		}
		_ = m.View()
	})

	t.Run("等待选择状态渲染不panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("等待选择状态 View() 发生 panic: %v", r)
			}
		}()
		m := NewModel(&cfg, client, reg)
		m.ready = true
		m.width = 120
		m.height = 40
		m.state = StateWaitingChoice
		m.pendingChoice = &PendingChoice{
			Prompt: "请选择一个选项",
			Options: []ChoiceOption{
				{Label: "选项A", Value: "a", Description: "描述A"},
				{Label: "选项B", Value: "b", Description: "描述B"},
			},
			ToolID: "tool-1",
		}
		_ = m.View()
	})
}

// TestVimMode_NormalToInsert 测试按 'i' 从 VimNormal 切换到 VimInsert 模式
func TestVimMode_NormalToInsert(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	m.vimMode = true
	m.vimModeState = VimNormal

	// mock按下 'i' 键
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}
	newModel, _ := m.Update(keyMsg)
	updated := newModel.(Model)

	if updated.vimModeState != VimInsert {
		t.Errorf("按 'i' 后expected VimInsert(%d)，actual %d", VimInsert, updated.vimModeState)
	}
}

// TestVimMode_InsertToNormal 测试按 Escape 从 VimInsert 切换到 VimNormal 模式
func TestVimMode_InsertToNormal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	m.vimMode = true
	m.vimModeState = VimInsert

	// mock按下 Escape 键
	keyMsg := tea.KeyMsg{Type: tea.KeyEsc}
	newModel, _ := m.Update(keyMsg)
	updated := newModel.(Model)

	if updated.vimModeState != VimNormal {
		t.Errorf("按 Escape 后expected VimNormal(%d)，actual %d", VimNormal, updated.vimModeState)
	}
}

// statusBarPlain 把状态栏渲染结果压成单行（去边框 + 折叠换行与连续空格）。
//
// 为什么需要（2026-10-21，导出树实测）：状态栏按宽度分栏，"Workspace: <cwd>" 的长度
// 随**检出路径**变化（同一份代码在 /Users/... 与 /private/tmp/... 下不同），宽度用尽时
// 会在这里折行，且折行点是**下一个 token 之前**，于是右侧边框 │ 正好插进
// "Mode:" 与 "NORMAL" 之间（实测得到 "Mode: │ NORMAL"）。
// 原断言 `strings.Contains(sb, "Mode: NORMAL")` 因此变成「断言检出路径长度」而不是
// 「断言模式指示器」——判据要的是语义，故：去边框、折叠空白后再匹配。
func statusBarPlain(t *testing.T, m *Model) string {
	t.Helper()
	sb := strings.NewReplacer(
		"│", " ", "─", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ",
	).Replace(m.renderStatusBar())
	return strings.Join(strings.Fields(sb), " ")
}

// TestVimMode_StatusBar 测试 vim 模式指示器在状态栏中的显示
func TestVimMode_StatusBar(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	t.Run("VimNormal模式显示NORMAL", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.vimMode = true
		m.vimModeState = VimNormal
		m.ready = true
		m.width = 120
		m.height = 40

		statusBar := statusBarPlain(t, &m)
		if !strings.Contains(statusBar, "Mode: NORMAL") {
			t.Error("VimNormal 模式下状态栏should contain 'Mode: NORMAL'")
		}
	})

	t.Run("VimInsert模式显示INSERT", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.vimMode = true
		m.vimModeState = VimInsert
		m.ready = true
		m.width = 120
		m.height = 40

		statusBar := statusBarPlain(t, &m)
		if !strings.Contains(statusBar, "Mode: INSERT") {
			t.Error("VimInsert 模式下状态栏should contain 'Mode: INSERT'")
		}
	})

	t.Run("vim模式关闭时不显示模式指示器", func(t *testing.T) {
		m := NewModel(&cfg, client, reg)
		m.vimMode = false
		m.ready = true
		m.width = 120
		m.height = 40

		statusBar := statusBarPlain(t, &m)
		if strings.Contains(statusBar, "Mode: NORMAL") || strings.Contains(statusBar, "Mode: INSERT") {
			t.Error("vim 模式关闭时状态栏should not contain模式指示器")
		}
	})
}

// TestVimMode_DefaultOff 测试 vim 模式默认关闭
func TestVimMode_DefaultOff(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)

	if m.vimMode {
		t.Error("vim 模式默认应为关闭")
	}
}

// TestKeybindingsCommandRemoved /keybindings 命令已删除（非 vim 用户无运行时切换需求）。
// 从生产调度入口 handleCommand 断言命令不再被处理，且不会静默吞掉输入。
func TestKeybindingsCommandRemoved(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	output := m.handleCommand("/keybindings vim")

	// 命令必须走 Unknown 路径（有明确指引），而非空输出或静默成功
	if output == "" {
		t.Error("/keybindings 不应静默返回空输出")
	}
	if strings.Contains(output, "Vim mode enabled") {
		t.Errorf("/keybindings vim 不应再切换 vim 模式，actual: %s", output)
	}
	if m.vimMode {
		t.Error("/keybindings vim 不应再置位 vimMode")
	}
}

// TestVimMode_NormalModeKeysDontInsert 测试 VimNormal 模式下按键不会插入文本
func TestVimMode_NormalModeKeysDontInsert(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	m.vimMode = true
	m.vimModeState = VimNormal
	m.ready = true
	m.width = 120
	m.height = 40

	// mock按下 'a' 键（在 VimNormal 模式下不应插入）
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	newModel, _ := m.Update(keyMsg)
	updated := newModel.(Model)

	if updated.textarea.Value() != "" {
		t.Errorf("VimNormal 模式下按 'a' 不应插入文本，actual: %q", updated.textarea.Value())
	}
}

// TestVimMode_InsertModeTypesNormally 测试 VimInsert 模式下正常输入
func TestVimMode_InsertModeTypesNormally(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	client := newTestClient("test-model")
	reg := newTestRegistry()

	m := NewModel(&cfg, client, reg)
	m.vimMode = true
	m.vimModeState = VimInsert
	m.ready = true
	m.width = 120
	m.height = 40

	// mock按下 'a' 键（在 VimInsert 模式下应插入）
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	newModel, _ := m.Update(keyMsg)
	updated := newModel.(Model)

	if !strings.Contains(updated.textarea.Value(), "a") {
		t.Errorf("VimInsert 模式下按 'a' 应插入文本，actual: %q", updated.textarea.Value())
	}
}
