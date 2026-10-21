package plugin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// createTestPluginDir 创建包含测试插件的临时目录，返回临时目录路径。
// command 使用跨平台可执行命令 `go version`（替代 echo，Windows 下 echo 是 cmd 内建非独立可执行文件）。
func createTestPluginDir(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	pluginDir := filepath.Join(tempDir, ".whale", "plugins", "test-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}

	manifest := `{
  "id": "test-plugin",
  "name": "Test Plugin",
  "version": "1.0.0",
  "description": "A test plugin",
  "tools": [
    {
      "name": "test_greet",
      "description": "Greet someone",
      "input_schema": {
        "type": "object",
        "properties": {
          "name": {"type": "string", "description": "Name to greet"}
        }
      },
      "command": "go version"
    }
  ]
}`

	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写入 plugin.json 失败: %v", err)
	}
	return tempDir
}

// TestExecutor_RegisterPluginTools 测试插件工具注册到 Registry
func TestExecutor_RegisterPluginTools(t *testing.T) {
	tempDir := createTestPluginDir(t)
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, tempDir)
	// P0-7/B7: 插件默认禁用，需显式启用后工具才注册
	executor.EnableAllPlugins()

	// 验证插件工具已注册到 Registry
	spec, ok := registry.Get("test_greet")
	if !ok {
		t.Fatal("插件工具 test_greet 未在 Registry 中找到")
	}
	if spec.Name != "test_greet" {
		t.Errorf("工具名称 = %q, expected %q", spec.Name, "test_greet")
	}
	if spec.Description != "Greet someone" {
		t.Errorf("工具描述 = %q, expected %q", spec.Description, "Greet someone")
	}

	// 验证 Executor 的 PluginMgr 已加载插件
	pluginTools := executor.PluginMgr.GetEnabledTools()
	if len(pluginTools) == 0 {
		t.Fatal("PluginMgr 未加载任何插件工具")
	}
	found := false
	for _, td := range pluginTools {
		if td.Name == "test_greet" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("PluginMgr 中未找到 test_greet 工具")
	}

	// 验证工具出现在 All() 列表中
	allSpecs := registry.All()
	found = false
	for _, s := range allSpecs {
		if s.Name == "test_greet" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("test_greet 未出现在 registry.All() 列表中")
	}
}

// TestExecutor_PluginToolHandler 测试插件工具处理函数执行插件命令。
// 使用跨平台命令 `go version`（替代 echo，Windows 下 echo 非 independent executable）。
func TestExecutor_PluginToolHandler(t *testing.T) {
	tempDir := createTestPluginDir(t)
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, tempDir)
	// P0-7/B7: 插件默认禁用，需显式启用后工具才注册
	executor.EnableAllPlugins()

	// 构造工具调用
	input := map[string]interface{}{"name": "World"}
	inputJSON, _ := json.Marshal(input)

	call := tools.Call{
		ID:    "test-1",
		Name:  "test_greet",
		Input: string(inputJSON),
	}

	result := executor.Execute(context.Background(), call)
	if result.IsError {
		t.Fatalf("插件工具执行失败: %s", result.Output)
	}

	// 验证输出包含 "go version"（go version 输出形如 "go version go1.24.0 ..."）
	output := strings.ReplaceAll(result.Output, "\r\n", "\n")
	if !strings.Contains(output, "go version") {
		t.Errorf("输出 = %q, expected 包含 'go version'", result.Output)
	}
}

// TestExecutor_PluginToolNoConflict 测试插件工具名称与内置工具冲突时返回错误
func TestExecutor_PluginToolNoConflict(t *testing.T) {
	registry := tools.NewRegistry()

	// 尝试用内置工具名注册插件工具
	spec := tools.Spec{
		Name:        "bash",
		Description: "Conflicting plugin tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}

	err := registry.RegisterPlugin(spec)
	if err == nil {
		t.Fatal("expected注册冲突工具返回错误，但返回了 nil")
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("错误信息 = %q, expected包含 %q", err.Error(), "conflicts")
	}
}

// TestExecutor_NoPlugins 测试无插件目录时 registerPluginTools 不报错
func TestExecutor_NoPlugins(t *testing.T) {
	// 使用空临时目录（无 .whale/plugins 子目录）
	tempDir := t.TempDir()
	registry := tools.NewRegistry()

	// 不应 panic 或报错
	executor := tools.NewExecutor(registry, tempDir)
	if executor == nil {
		t.Fatal("Executor 不should be nil")
	}

	// 验证没有注册任何插件工具
	allSpecs := registry.All()
	for _, s := range allSpecs {
		if s.Name == "test_greet" {
			t.Fatal("不应注册任何插件工具，但发现了 test_greet")
		}
	}

	// 验证 PluginMgr 可正常工作
	pluginTools := executor.PluginMgr.GetEnabledTools()
	if len(pluginTools) != 0 {
		t.Fatalf("无插件目录时 GetEnabledTools should return empty，actual返回 %d 项", len(pluginTools))
	}
}

// createCustomPluginDir 创建包含指定 command 的测试插件目录。
// 供补充测试复用（Uninstall 注销、env 隔离、sandbox 拦截）。
func createCustomPluginDir(t *testing.T, command string) string {
	t.Helper()
	tempDir := t.TempDir()
	pluginDir := filepath.Join(tempDir, ".whale", "plugins", "custom-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}
	manifest := map[string]interface{}{
		"id":      "custom-plugin",
		"name":    "Custom Plugin",
		"version": "1.0.0",
		"tools": []map[string]interface{}{
			{
				"name":        "custom_tool",
				"description": "Custom tool for testing",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"arg": map[string]interface{}{"type": "string"},
					},
				},
				"command": command,
			},
		},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatalf("写入 plugin.json 失败: %v", err)
	}
	return tempDir
}

// TestPluginToolHandler_UninstallRemovesTool 验证 P0-5/B6：Uninstall 触发回调注销工具后，
// 调用该插件工具应返回 "unknown tool"（工具已从 Registry 和 handlers 移除）。
func TestPluginToolHandler_UninstallRemovesTool(t *testing.T) {
	tempDir := createCustomPluginDir(t, "go version")
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, tempDir)
	executor.EnableAllPlugins()

	// 初始工具可调用
	call := tools.Call{ID: "u-1", Name: "custom_tool", Input: "{}"}
	result := executor.Execute(context.Background(), call)
	if result.IsError {
		t.Fatalf("工具初始应可调用: %s", result.Output)
	}

	// Uninstall 触发 onUninstall 回调 → UnregisterPluginTools
	if err := executor.PluginMgr.Uninstall("custom-plugin"); err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}

	// 卸载后工具应不可调用
	result2 := executor.Execute(context.Background(), call)
	if !result2.IsError {
		t.Fatal("Uninstall 后调用工具应返回错误")
	}
	if !strings.Contains(result2.Output, "unknown tool") {
		t.Errorf("错误应包含 'unknown tool', got: %s", result2.Output)
	}

	// Registry 中也不应存在
	if _, ok := registry.Get("custom_tool"); ok {
		t.Error("Registry 中不应再存在 custom_tool")
	}
}

// TestPluginToolHandler_EnvIsolation 验证 P0-6：pluginToolHandler 执行命令时
// env 白名单剥离密钥变量（GITHUB_TOKEN/*_API_KEY 等），子进程无法读取宿主密钥。
func TestPluginToolHandler_EnvIsolation(t *testing.T) {
	// 设置敏感环境变量（t.Setenv 会在测试结束自动还原）
	t.Setenv("GITHUB_TOKEN", "leak-secret-github")
	t.Setenv("MY_API_KEY", "leak-secret-api")

	// 跨平台打印 env 命令：Windows 用 cmd /c set，Unix 用 env
	var command string
	if runtime.GOOS == "windows" {
		command = "cmd /c set"
	} else {
		command = "env"
	}

	tempDir := createCustomPluginDir(t, command)
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, tempDir)
	executor.EnableAllPlugins()

	call := tools.Call{ID: "e-1", Name: "custom_tool", Input: "{}"}
	result := executor.Execute(context.Background(), call)
	if result.IsError {
		t.Fatalf("插件工具执行失败: %s", result.Output)
	}

	// 输出不应包含敏感变量值（env 白名单剥离了 *_TOKEN/*_API_KEY）
	for _, secret := range []string{"leak-secret-github", "leak-secret-api"} {
		if strings.Contains(result.Output, secret) {
			t.Errorf("env 隔离失败：输出包含敏感值 %q（应被白名单剥离）\nOutput: %s", secret, result.Output)
		}
	}
	// 也不应包含变量名 GITHUB_TOKEN / MY_API_KEY
	if strings.Contains(strings.ToUpper(result.Output), "GITHUB_TOKEN") {
		t.Errorf("env 隔离失败：输出包含 GITHUB_TOKEN 变量名\nOutput: %s", result.Output)
	}
}

// TestPluginToolHandler_SandboxBlocksDangerous 验证 P0-3：sandbox.Manager 拦截危险命令。
// 配置 AllowCommands 白名单仅允许 go，执行 "rm -rf /" 应被 sandbox 拒绝，返回 "sandbox denied"。
func TestPluginToolHandler_SandboxBlocksDangerous(t *testing.T) {
	tempDir := createCustomPluginDir(t, "rm -rf /")
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, tempDir)

	// 注入 sandbox.Manager：AllowCommands 白名单仅允许 go，rm 不在白名单应被阻止
	sbCfg := &sandbox.SandboxConfig{
		WorkDir:       tempDir,
		AllowCommands: []string{"go"},
	}
	sbMgr := sandbox.NewManager(sbCfg, sandbox.SandboxLevelRelaxed)
	executor.SetSandboxMgr(sbMgr)

	executor.EnableAllPlugins()

	call := tools.Call{ID: "s-1", Name: "custom_tool", Input: "{}"}
	result := executor.Execute(context.Background(), call)

	// sandbox 拒绝时 pluginToolHandler 返回**软哨兵** errSandboxDeniedSoft（G1-A 收口，2026-09-14）：
	// 文案不变、IsError=false，但 executeDirect 会打上 FailureKind=SandboxDenied。
	if !strings.Contains(result.Output, "sandbox denied") {
		t.Errorf("sandbox 应拦截危险命令 'rm -rf /'，输出应包含 'sandbox denied'\nOutput: %s\nIsError: %v", result.Output, result.IsError)
	}
	if result.IsError {
		t.Errorf("插件沙箱拒绝应保持软语义 IsError=false，实际 true；output=%s", result.Output)
	}
	if result.FailureKind != tools.FailureKindSandboxDenied {
		t.Errorf("插件沙箱拒绝应携带 FailureKind=%q，实际 %q", tools.FailureKindSandboxDenied, result.FailureKind)
	}
}
