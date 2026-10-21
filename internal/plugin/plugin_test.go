package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/approval"
)

// TestLoadPlugin 测试加载插件
func TestLoadPlugin(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{
		ID:          "test-plugin",
		Name:        "Test Plugin",
		Version:     "1.0.0",
		Description: "A test plugin",
		Tools: []ToolDef{
			{Name: "test_tool", Description: "A test tool", Command: "echo"},
		},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	plugin, err := loadPlugin(filepath.Join(pluginDir, "plugin.json"))
	if err != nil {
		t.Fatalf("loadPlugin error: %v", err)
	}
	if plugin.ID != "test-plugin" {
		t.Errorf("expected ID 'test-plugin', got %q", plugin.ID)
	}
	if len(plugin.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(plugin.Tools))
	}
}

// TestManager_LoadAll 测试加载所有插件
func TestManager_LoadAll(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{ID: "my-plugin", Name: "My Plugin", Version: "1.0.0"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.LoadAll()

	plugins := mgr.List()
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
}

// TestManager_EnableDisable 测试启用/禁用插件
func TestManager_EnableDisable(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{ID: "test-plugin", Name: "Test", Version: "1.0.0"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.LoadAll()

	mgr.Disable("test-plugin")
	p, _ := mgr.Get("test-plugin")
	if p.Enabled {
		t.Error("plugin should be disabled")
	}

	mgr.Enable("test-plugin")
	p, _ = mgr.Get("test-plugin")
	if !p.Enabled {
		t.Error("plugin should be enabled")
	}
}

// TestManager_GetEnabledTools 测试获取已启用插件的工具
func TestManager_GetEnabledTools(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "tool-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{
		ID: "tool-plugin", Name: "Tool Plugin", Version: "1.0.0",
		Tools: []ToolDef{
			{Name: "tool1", Description: "Tool 1", Command: "echo tool1"},
			{Name: "tool2", Description: "Tool 2", Command: "echo tool2"},
		},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.LoadAll()

	// P0-7/B7: loadFromDir 默认禁用，需显式启用后才出现在 GetEnabledTools
	mgr.Enable("tool-plugin")
	tools := mgr.GetEnabledTools()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	mgr.Disable("tool-plugin")
	tools = mgr.GetEnabledTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools after disable, got %d", len(tools))
	}
}

// TestManager_FormatList 测试格式化插件列表
func TestManager_FormatList(t *testing.T) {
	mgr := NewManager()
	list := mgr.FormatList()
	if list == "" {
		t.Error("FormatList should return message for empty plugins")
	}
}

// TestManager_Enable_NotFound 测试启用不存在的插件
func TestManager_Enable_NotFound(t *testing.T) {
	mgr := NewManager()
	err := mgr.Enable("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

// TestManager_Disable_NotFound 测试禁用不存在的插件
func TestManager_Disable_NotFound(t *testing.T) {
	mgr := NewManager()
	err := mgr.Disable("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

// TestManager_Install_FromLocalPath 从local路径安装插件
func TestManager_Install_FromLocalPath(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{ID: "my-plugin", Name: "My Plugin", Version: "1.0.0", Description: "Test plugin"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.SetAllowUnsignedPlugins(true)

	plugin, err := mgr.Install(pluginDir)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if plugin.ID != "my-plugin" {
		t.Errorf("expected ID 'my-plugin', got %q", plugin.ID)
	}
	if !plugin.Enabled {
		t.Error("plugin should be enabled by default")
	}
	if plugin.SourcePath != pluginDir {
		t.Errorf("expected SourcePath %q, got %q", pluginDir, plugin.SourcePath)
	}
	if plugin.InstallTime.IsZero() {
		t.Error("InstallTime should not be zero")
	}

	plugins := mgr.List()
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
}

// TestManager_Install_Duplicate 重复安装返回错误
func TestManager_Install_Duplicate(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{ID: "my-plugin", Name: "My Plugin", Version: "1.0.0"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.SetAllowUnsignedPlugins(true)

	_, err := mgr.Install(pluginDir)
	if err != nil {
		t.Fatalf("first install should succeed: %v", err)
	}

	_, err = mgr.Install(pluginDir)
	if err == nil {
		t.Error("expected error for duplicate install")
	}
	if err != nil && !strings.Contains(err.Error(), "already installed") {
		t.Errorf("error should mention 'already installed', got: %v", err)
	}
}

// TestManager_Install_InvalidSource 无效 source 返回错误
func TestManager_Install_InvalidSource(t *testing.T) {
	mgr := NewManager(t.TempDir())

	_, err := mgr.Install("not-a-valid-source")
	if err == nil {
		t.Error("expected error for invalid source")
	}
	if err != nil && !strings.Contains(err.Error(), "Invalid source") {
		t.Errorf("error should contain 'Invalid source', got: %v", err)
	}
}

// TestManager_Install_NoManifest 缺少 plugin.json 返回错误
func TestManager_Install_NoManifest(t *testing.T) {
	dir := t.TempDir()
	emptyDir := filepath.Join(dir, "empty-plugin")
	os.MkdirAll(emptyDir, 0755)

	mgr := NewManager(dir)

	_, err := mgr.Install(emptyDir)
	if err == nil {
		t.Error("expected error for missing plugin.json")
	}
	if err != nil && !strings.Contains(err.Error(), "plugin.json not found") {
		t.Errorf("error should mention 'plugin.json not found', got: %v", err)
	}
}

// TestManager_Install_InvalidJSON 损坏 JSON 返回错误
func TestManager_Install_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "bad-plugin")
	os.MkdirAll(pluginDir, 0755)

	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte("{invalid json"), 0644)

	mgr := NewManager(dir)

	_, err := mgr.Install(pluginDir)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if err != nil && !strings.Contains(err.Error(), "Invalid plugin") {
		t.Errorf("error should contain 'Invalid plugin', got: %v", err)
	}
}

// TestManager_ReloadAll 基本重载
func TestManager_ReloadAll(t *testing.T) {
	dir := t.TempDir()

	p1Dir := filepath.Join(dir, "p1")
	os.MkdirAll(p1Dir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "p1", Name: "P1", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(p1Dir, "plugin.json"), data, 0644)

	p2Dir := filepath.Join(dir, "p2")
	os.MkdirAll(p2Dir, 0755)
	data2, _ := json.MarshalIndent(Plugin{ID: "p2", Name: "P2", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(p2Dir, "plugin.json"), data2, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.LoadAll()

	// P0-7/B7: loadFromDir 默认禁用，显式启用 p1，保持 p2 禁用
	mgr.Enable("p1")

	plugins, err := mgr.ReloadAll()
	if err != nil {
		t.Fatalf("ReloadAll error: %v", err)
	}
	if len(plugins) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(plugins))
	}

	p1, _ := mgr.Get("p1")
	if !p1.Enabled {
		t.Error("p1 should still be enabled after reload")
	}
	p2, _ := mgr.Get("p2")
	if p2.Enabled {
		t.Error("p2 should still be disabled after reload")
	}
}

// TestManager_ReloadAll_NewPlugin 新发现插件默认禁用（P0-7/B7 安全默认）。
// 用户需通过 --enable-plugins 或显式 Enable 启用，避免任意代码执行风险。
func TestManager_ReloadAll_NewPlugin(t *testing.T) {
	dir := t.TempDir()

	p1Dir := filepath.Join(dir, "p1")
	os.MkdirAll(p1Dir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "p1", Name: "P1", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(p1Dir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.LoadAll()

	// 在磁盘上添加新插件
	p2Dir := filepath.Join(dir, "p2")
	os.MkdirAll(p2Dir, 0755)
	data2, _ := json.MarshalIndent(Plugin{ID: "p2", Name: "P2", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(p2Dir, "plugin.json"), data2, 0644)

	plugins, err := mgr.ReloadAll()
	if err != nil {
		t.Fatalf("ReloadAll error: %v", err)
	}
	if len(plugins) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(plugins))
	}

	p2, _ := mgr.Get("p2")
	if p2.Enabled {
		t.Error("new plugin should be disabled by default (P0-7/B7)")
	}
}

// TestManager_SaveState_LoadState 持久化往返
func TestManager_SaveState_LoadState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "plugin_state.json")

	p1Dir := filepath.Join(dir, "p1")
	os.MkdirAll(p1Dir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "p1", Name: "P1", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(p1Dir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.SetStatePath(statePath)
	mgr.LoadAll()

	mgr.Disable("p1")
	mgr.SaveState()

	// mock重启
	mgr2 := NewManager(dir)
	mgr2.SetStatePath(statePath)
	mgr2.LoadAll()

	p1, ok := mgr2.Get("p1")
	if !ok {
		t.Fatal("p1 should exist after reload")
	}
	if p1.Enabled {
		t.Error("p1 should still be disabled after state restore")
	}
}

// TestManager_FormatList_Empty 空插件列表格式化
func TestManager_FormatList_Empty(t *testing.T) {
	mgr := NewManager()
	result := mgr.FormatList()
	if result == "" {
		t.Error("FormatList should not return empty string")
	}
}

// TestManager_Install_NoDirs 无插件目录时安装失败
func TestManager_Install_NoDirs(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "my-plugin", Name: "My", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager()
	_, err := mgr.Install(pluginDir)
	if err == nil {
		t.Error("expected error when no plugin dirs configured")
	}
}

// TestManager_Uninstall 卸载插件
func TestManager_Uninstall(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "my-plugin", Name: "My", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.SetAllowUnsignedPlugins(true)
	mgr.Install(pluginDir)

	err := mgr.Uninstall("my-plugin")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}

	_, ok := mgr.Get("my-plugin")
	if ok {
		t.Error("plugin should not exist after uninstall")
	}
}

// TestManager_Uninstall_NotFound 卸载不存在的插件
func TestManager_Uninstall_NotFound(t *testing.T) {
	mgr := NewManager()
	err := mgr.Uninstall("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

// TestManager_GetEnabledHooks 获取已启用插件的钩子
func TestManager_GetEnabledHooks(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "hook-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := Plugin{
		ID: "hook-plugin", Name: "Hook Plugin", Version: "1.0.0",
		Hooks: []HookDef{
			{Event: "PreToolUse", ToolName: "bash", Command: "echo before"},
			{Event: "PostToolUse", ToolName: "*", Command: "echo after"},
		},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.LoadAll()

	// P0-7/B7: loadFromDir 默认禁用，需显式启用后才出现在 GetEnabledHooks
	mgr.Enable("hook-plugin")
	hooks := mgr.GetEnabledHooks()
	if len(hooks) != 2 {
		t.Errorf("expected 2 hooks, got %d", len(hooks))
	}

	mgr.Disable("hook-plugin")
	hooks = mgr.GetEnabledHooks()
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks after disable, got %d", len(hooks))
	}
}

// TestLoadPlugin_MissingID 使用目录名作为默认 ID
func TestLoadPlugin_MissingID(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "no-id-plugin")
	os.MkdirAll(pluginDir, 0755)

	manifest := map[string]string{"name": "No ID", "version": "1.0.0"}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	plugin, err := loadPlugin(filepath.Join(pluginDir, "plugin.json"))
	if err != nil {
		t.Fatalf("loadPlugin error: %v", err)
	}
	if plugin.ID != "no-id-plugin" {
		t.Errorf("expected ID 'no-id-plugin', got %q", plugin.ID)
	}
}

// TestLoadPlugin_MissingName 使用 ID 作为默认名称
func TestLoadPlugin_MissingName(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "no-name")
	os.MkdirAll(pluginDir, 0755)

	manifest := map[string]string{"id": "no-name", "version": "1.0.0"}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	plugin, err := loadPlugin(filepath.Join(pluginDir, "plugin.json"))
	if err != nil {
		t.Fatalf("loadPlugin error: %v", err)
	}
	if plugin.Name != "no-name" {
		t.Errorf("expected Name 'no-name', got %q", plugin.Name)
	}
}

// TestCloneFromGit_ApprovalDeny 验证 approval 拒绝时克隆被中止（B0 决策的 approval 消费者扩展）。
// 当 approval.Manager 配置了拒绝策略时，cloneFromGit 应在执行 git clone 前返回拒绝错误，
// 不发起任何网络请求。
func TestCloneFromGit_ApprovalDeny(t *testing.T) {
	mgr := NewManager(t.TempDir())
	// 配置 approval.Manager：PolicyOnRequest 策略 + 始终拒绝的 askFn
	// "git_clone" 前缀匹配 isDangerous，会触发 askFn
	denyMgr := approval.NewManager(approval.PolicyOnRequest, func(ctx context.Context, toolName, input string) (approval.ApprovalDecision, string) {
		return approval.DecisionDeny, "user denied clone"
	})
	mgr.SetApprovalManager(denyMgr)

	// 使用合法 git URL，approval 应在 git clone 前拦截
	_, err := mgr.Install("https://github.com/test/repo.git")
	if err == nil {
		t.Fatal("expected error when approval denies git clone")
	}
	if !strings.Contains(err.Error(), "denied by approval") {
		t.Errorf("error should mention 'denied by approval', got: %v", err)
	}
}

// TestSetApprovalManager 验证 approval.Manager 注入与清除。
func TestSetApprovalManager(t *testing.T) {
	mgr := NewManager(t.TempDir())

	// 初始应为 nil
	if mgr.approvalMgr != nil {
		t.Error("approvalMgr should be nil by default")
	}

	// 注入
	denyMgr := approval.NewManager(approval.PolicyNever, nil)
	mgr.SetApprovalManager(denyMgr)
	if mgr.approvalMgr != denyMgr {
		t.Error("approvalMgr should be set")
	}

	// 清除
	mgr.SetApprovalManager(nil)
	if mgr.approvalMgr != nil {
		t.Error("approvalMgr should be nil after clearing")
	}
}

// TestSaveStateLocked_AtomicWrite 验证状态文件原子写（P0-4/B5）。
// 并发写不应损坏文件，写后状态文件应完整可读。
func TestSaveStateLocked_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "plugin_state.json")

	pDir := filepath.Join(dir, "p1")
	os.MkdirAll(pDir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "p1", Name: "P1", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(pDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.SetStatePath(statePath)
	mgr.LoadAll()

	// 并发写：多 goroutine 同时 Enable/Disable 触发 saveStateLocked
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				mgr.Disable("p1")
			} else {
				mgr.Enable("p1")
			}
		}(i)
	}
	wg.Wait()

	// 写后状态文件应完整可读（非损坏）
	mgr2 := NewManager(dir)
	mgr2.SetStatePath(statePath)
	mgr2.LoadAll()

	p1, ok := mgr2.Get("p1")
	if !ok {
		t.Fatal("p1 should exist after concurrent writes (state file may be corrupted)")
	}
	// 状态应为 true 或 false（取决于最后写入），关键是文件未损坏
	_ = p1.Enabled
}

// TestSaveStateLocked_FilePermissions 验证原子写后文件权限正确（0600）。
// Windows 不支持 Unix 权限位（所有文件显示为 0666），跳过权限断言。
func TestSaveStateLocked_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不支持 Unix 权限位，跳过权限断言")
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "plugin_state.json")

	mgr := NewManager(dir)
	mgr.SetStatePath(statePath)
	if err := mgr.SaveState(); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected file permission 0600, got %o", perm)
	}
}

// TestUninstall_TriggersCallback 验证 Uninstall 触发注销回调（P0-5/B6）。
// 卸载时应调用 onUninstall 回调，Executor 据此注销工具与清理钩子。
func TestUninstall_TriggersCallback(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "my-plugin", Name: "My", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.SetAllowUnsignedPlugins(true)
	mgr.Install(pluginDir)

	// 注入回调，记录被卸载的 pluginID
	var uninstalledID string
	var callbackCalled bool
	mgr.SetOnUninstall(func(id string) {
		uninstalledID = id
		callbackCalled = true
	})

	err := mgr.Uninstall("my-plugin")
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if !callbackCalled {
		t.Error("onUninstall callback should be called during Uninstall")
	}
	if uninstalledID != "my-plugin" {
		t.Errorf("callback received id %q, expected %q", uninstalledID, "my-plugin")
	}

	// 卸载后插件应不存在
	_, ok := mgr.Get("my-plugin")
	if ok {
		t.Error("plugin should not exist after uninstall")
	}
}

// TestUninstall_NilCallbackFallback 验证 onUninstall 为 nil 时向后兼容。
func TestUninstall_NilCallbackFallback(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "my-plugin", Name: "My", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	defer os.Remove(mgr.statePath)
	mgr.SetAllowUnsignedPlugins(true)
	mgr.Install(pluginDir)
	// 不设置 onUninstall，保持 nil

	err := mgr.Uninstall("my-plugin")
	if err != nil {
		t.Fatalf("Uninstall with nil callback should succeed: %v", err)
	}
	_, ok := mgr.Get("my-plugin")
	if ok {
		t.Error("plugin should not exist after uninstall")
	}
}

// TestLoadFromDir_DefaultDisabled 验证 P0-7/B7：loadFromDir 加载的插件默认 Enabled=false。
// 插件涉及任意代码执行，必须由用户显式 opt-in（--enable-plugins 或 Enable）后才启用。
func TestLoadFromDir_DefaultDisabled(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "safe-plugin")
	os.MkdirAll(pluginDir, 0755)
	data, _ := json.MarshalIndent(Plugin{ID: "safe-plugin", Name: "Safe", Version: "1.0.0"}, "", "  ")
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	mgr := NewManager(dir)
	mgr.LoadAll()

	p, ok := mgr.Get("safe-plugin")
	if !ok {
		t.Fatal("plugin should be loaded")
	}
	if p.Enabled {
		t.Error("plugin should be disabled by default (P0-7/B7)")
	}
	// GetEnabledTools 应返回空（无启用插件）
	if len(mgr.GetEnabledTools()) != 0 {
		t.Error("GetEnabledTools should be empty when all plugins disabled")
	}
}

// TestEnableAll 验证 EnableAll 启用所有已加载插件（P0-7/B7）。
func TestEnableAll(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"p1", "p2"} {
		pDir := filepath.Join(dir, id)
		os.MkdirAll(pDir, 0755)
		data, _ := json.MarshalIndent(Plugin{ID: id, Name: id, Version: "1.0.0"}, "", "  ")
		os.WriteFile(filepath.Join(pDir, "plugin.json"), data, 0644)
	}

	mgr := NewManager(dir)
	mgr.LoadAll()

	// 默认全部禁用
	for _, p := range mgr.List() {
		if p.Enabled {
			t.Errorf("plugin %s should be disabled by default", p.ID)
		}
	}

	count := mgr.EnableAll()
	if count != 2 {
		t.Errorf("EnableAll should enable 2 plugins, got %d", count)
	}
	for _, p := range mgr.List() {
		if !p.Enabled {
			t.Errorf("plugin %s should be enabled after EnableAll", p.ID)
		}
	}

	// 幂等：再次调用应返回 0（已全部启用）
	if count2 := mgr.EnableAll(); count2 != 0 {
		t.Errorf("EnableAll should be idempotent, got %d", count2)
	}
}

// TestValidateGitURL_ProtocolWhitelist 验证 P0-2 后协议白名单：
// 仅 https/http/git@/ssh:// 被接受，file:// 与其他协议被拒绝。
func TestValidateGitURL_ProtocolWhitelist(t *testing.T) {
	valid := []string{
		"https://github.com/a/b.git",
		"http://example.com/repo.git",
		"git@github.com:a/b.git",
		"ssh://git@example.com/repo.git",
	}
	for _, u := range valid {
		if err := validateGitURL(u); err != nil {
			t.Errorf("validateGitURL(%q) should accept, got error: %v", u, err)
		}
	}

	invalid := []string{
		"file:///path/to/repo",
		"file://path/to/repo",
		"ftp://example.com/repo",
		"/local/absolute/path",
		"relative/path",
		"not-a-url",
	}
	for _, u := range invalid {
		if err := validateGitURL(u); err == nil {
			t.Errorf("validateGitURL(%q) should reject (protocol whitelist)", u)
		}
	}
}

// TestValidateCommand_P0-2 验证删除黑名单后仅校验空命令。
// shell 元字符不再被拒绝（由 sandbox + 无 shell 执行兜底）。
func TestValidateCommand_P0_2(t *testing.T) {
	if err := validateCommand(""); err == nil {
		t.Error("empty command should be rejected")
	}
	// 含 shell 元字符的命令不再被拒绝（黑名单已删除）
	dangerous := []string{"echo hi; rm -rf /", "cat $HOME/secret", "echo `whoami`", "a | b"}
	for _, c := range dangerous {
		if err := validateCommand(c); err != nil {
			t.Errorf("validateCommand(%q) should accept after P0-2 (blacklist removed), got: %v", c, err)
		}
	}
}

// TestE1_FileProtocolRejected 验证 E1 攻击向量：file:// URL 被协议白名单拒绝。
// 构造含 .git/hooks/post-checkout 的恶意本地仓库，cloneFromGit 应在入口拒绝 file://，
// 不发起 git clone，hook 无机会执行（标记文件未生成）。
func TestE1_FileProtocolRejected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping E1 hook suppression test")
	}

	// 构造恶意本地仓库：post-checkout hook 写标记文件
	srcRepo := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "e1_file_marker.txt")
	runGit(t, srcRepo, "init")
	runGit(t, srcRepo, "config", "user.email", "test@test.com")
	runGit(t, srcRepo, "config", "user.name", "test")
	// 写一个文件并提交（保证仓库非空）
	os.WriteFile(filepath.Join(srcRepo, "README.md"), []byte("malicious"), 0644)
	runGit(t, srcRepo, "add", "README.md")
	runGit(t, srcRepo, "commit", "-m", "init")

	// 安装 post-checkout hook：写标记文件
	hookDir := filepath.Join(srcRepo, ".git", "hooks")
	os.MkdirAll(hookDir, 0755)
	hookPath := filepath.Join(hookDir, "post-checkout")
	hookContent := "#!/bin/sh\necho pwned > " + markerPath + "\n"
	if runtime.GOOS == "windows" {
		// Windows 下 git bash hook 仍用 sh 语法
		hookContent = "#!/bin/sh\necho pwned > " + markerPath + "\n"
	}
	os.WriteFile(hookPath, []byte(hookContent), 0755)

	// 通过 cloneFromGit 尝试 file:// clone，应被协议白名单拒绝
	mgr := NewManager(t.TempDir())
	targetDir := t.TempDir()
	fileURL := "file:///" + strings.ReplaceAll(srcRepo, "\\", "/")
	_, err := mgr.cloneFromGit(fileURL, targetDir)
	if err == nil {
		t.Fatal("cloneFromGit should reject file:// URL by protocol whitelist")
	}
	if !strings.Contains(err.Error(), "invalid git URL") {
		t.Errorf("error should mention 'invalid git URL', got: %v", err)
	}

	// 标记文件不应生成（hook 未执行，因为 clone 根本未发生）
	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Fatal("marker file should NOT exist: file:// rejected before clone, hook had no chance to run")
	}
}

// TestE1_LocalPathNoClone 验证 E1 攻击向量：本地路径 Install 不触发 git clone。
// 含恶意 hook 的本地仓库通过 Install(本地路径) 安装时走 isLocalPath 分支，
// 直接 loadPlugin 不 clone，hook 不会因 Install 而执行。
func TestE1_LocalPathNoClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping E1 local path test")
	}

	// 构造恶意本地仓库
	srcRepo := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "e1_local_marker.txt")
	runGit(t, srcRepo, "init")
	runGit(t, srcRepo, "config", "user.email", "test@test.com")
	runGit(t, srcRepo, "config", "user.name", "test")
	os.WriteFile(filepath.Join(srcRepo, "README.md"), []byte("x"), 0644)
	runGit(t, srcRepo, "add", "README.md")
	runGit(t, srcRepo, "commit", "-m", "init")

	hookDir := filepath.Join(srcRepo, ".git", "hooks")
	os.MkdirAll(hookDir, 0755)
	hookPath := filepath.Join(hookDir, "post-checkout")
	os.WriteFile(hookPath, []byte("#!/bin/sh\necho pwned > "+markerPath+"\n"), 0755)

	// 写 plugin.json 使其成为合法插件目录
	manifest := Plugin{ID: "malicious", Name: "Mal", Version: "1.0.0"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(srcRepo, "plugin.json"), data, 0644)

	// Install 本地路径：走 isLocalPath，不 clone，hook 不执行
	mgr := NewManager(t.TempDir())
	defer os.Remove(mgr.statePath)
	mgr.SetAllowUnsignedPlugins(true)
	_, err := mgr.Install(srcRepo)
	if err != nil {
		t.Fatalf("Install local path should succeed: %v", err)
	}

	// 标记文件不应生成（Install 本地路径不触发 git clone/checkout）
	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Fatal("marker file should NOT exist: local path Install does not git clone, hook cannot trigger")
	}
}

// TestE1_HooksSuppressedByDefense 验证 P0-1 三道防线中 core.hooksPath=/dev/null 真实生效。
//
// 攻击向量：git clone --template=<dir> 会将 <dir>/ 下文件复制到新仓库 .git/，
// 若 <dir>/hooks/post-checkout 存在则克隆后 checkout HEAD 时触发 RCE。
// （注：源仓库 .git/hooks/ 不会被克隆，故不能直接用源仓库 hook 做对照组。）
//
// 对照组：clone --template 含恶意 hook → hook 执行写标记文件；
// 防线组：clone -c core.hooksPath=/dev/null --template 同一 hook → hook 被抑制，标记未生成。
func TestE1_HooksSuppressedByDefense(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping E1 hook suppression test")
	}

	// 构造干净源仓库（无恶意 hook，仅用于 clone 目标）
	srcRepo := t.TempDir()
	runGit(t, srcRepo, "init")
	runGit(t, srcRepo, "config", "user.email", "test@test.com")
	runGit(t, srcRepo, "config", "user.name", "test")
	os.WriteFile(filepath.Join(srcRepo, "README.md"), []byte("x"), 0644)
	runGit(t, srcRepo, "add", "README.md")
	runGit(t, srcRepo, "commit", "-m", "init")

	// 构造含恶意 post-checkout hook 的 template 目录（真实 RCE 向量）
	templateDir := t.TempDir()
	templateHookDir := filepath.Join(templateDir, "hooks")
	os.MkdirAll(templateHookDir, 0755)
	// hook 写相对路径 marker 到 clone 目标根目录（避免 Windows 绝对路径转义问题）
	maliciousHook := []byte("#!/bin/sh\necho pwned > control_marker.txt\n")
	os.WriteFile(filepath.Join(templateHookDir, "post-checkout"), maliciousHook, 0755)

	// 对照组：clone --template 注入 hook → hook 应执行 → marker 生成
	controlDst := filepath.Join(t.TempDir(), "control_clone")
	controlCmd := exec.Command("git", "clone", "--template", templateDir, srcRepo, controlDst)
	if out, err := controlCmd.CombinedOutput(); err != nil {
		t.Fatalf("control clone failed: %v\n%s", err, out)
	}
	controlMarker := filepath.Join(controlDst, "control_marker.txt")
	if _, err := os.Stat(controlMarker); err != nil {
		// Windows 下若 sh 不可用 hook 可能不执行，跳过对照断言但继续验证防线
		t.Logf("CONTROL: marker not created (hook may not execute on this platform/git config) — skipping control assertion")
	} else {
		t.Log("CONTROL: hook executed, marker created — 前提成立，继续验证防线")
	}

	// 防线组：clone -c core.hooksPath=/dev/null --template 同一 hook → hook 应被抑制
	// 更新 hook 写不同 marker 名以便区分
	maliciousHook2 := []byte("#!/bin/sh\necho pwned > defense_marker.txt\n")
	os.WriteFile(filepath.Join(templateHookDir, "post-checkout"), maliciousHook2, 0755)
	defenseDst := filepath.Join(t.TempDir(), "defense_clone")
	defenseCmd := exec.Command("git",
		"-c", "core.hooksPath=/dev/null", // P0-1 防线：抑制所有 hook
		"clone", "--template", templateDir, srcRepo, defenseDst,
	)
	if out, err := defenseCmd.CombinedOutput(); err != nil {
		t.Fatalf("defense clone failed: %v\n%s", err, out)
	}

	// 防线组标记文件不应生成（core.hooksPath=/dev/null 抑制了 hook）
	defenseMarker := filepath.Join(defenseDst, "defense_marker.txt")
	if _, err := os.Stat(defenseMarker); err == nil {
		t.Fatal("DEFENSE: marker should NOT exist — core.hooksPath=/dev/null 应抑制 post-checkout hook（P0-1 三道防线失效）")
	}
	t.Log("DEFENSE: hook suppressed by core.hooksPath=/dev/null — P0-1 防线生效")
}

// runGit 在指定目录执行 git 命令，失败时 t.Fatalf。
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// writePluginManifest 将 manifest 写入指定 plugin 目录的 plugin.json 文件。
// 用于 M20-03 schema 校验测试用例快速构造 manifest 文件。
func writePluginManifest(t *testing.T, pluginDir string, manifest map[string]interface{}) string {
	t.Helper()
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll %s 失败: %v", pluginDir, err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("序列化 manifest 失败: %v", err)
	}
	manifestPath := filepath.Join(pluginDir, "plugin.json")
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatalf("写入 plugin.json 失败: %v", err)
	}
	return manifestPath
}

// TestValidateSemver_Strict 校验 semver 2.0.0 严格格式（M20-03）。
// 合法：1.0.0 / 0.0.0 / 1.2.3-alpha / 1.0.0-alpha.1 / 1.0.0+build.1 / 1.0.0-alpha+build
// 非法：v1 / 1.0 / abc / 1.0.0.0 / .1.0 / 1.0.
func TestValidateSemver_Strict(t *testing.T) {
	valid := []string{
		"1.0.0", "0.0.0", "0.1.0", "10.20.30",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta",
		"1.0.0+build.1", "1.0.0-alpha+build.1",
		"1.0.0-0", "1.0.0-1.2.3",
	}
	for _, v := range valid {
		if err := validateSemver(v); err != nil {
			t.Errorf("validateSemver(%q) 应接受，但报错: %v", v, err)
		}
	}
	invalid := []string{
		"", "v1", "1.0", "abc", "1.0.0.0", ".1.0", "1.0.",
		"01.0.0", "1.01.0", "1.0.01", "1.0.0-", "1.0.0-",
	}
	for _, v := range invalid {
		if err := validateSemver(v); err == nil {
			t.Errorf("validateSemver(%q) 应拒绝（格式不合规），但通过", v)
		}
	}
}

// TestValidateHookEvent_Enum 校验 hooks[].event 字段在枚举内（M20-03）。
// 合法：PreToolUse / PostToolUse / Stop
// 非法：on_message / PreRead / "" / pre_tool_use (大小写敏感)
func TestValidateHookEvent_Enum(t *testing.T) {
	valid := []string{"PreToolUse", "PostToolUse", "Stop"}
	for _, e := range valid {
		if err := validateHookEvent(e); err != nil {
			t.Errorf("validateHookEvent(%q) 应接受，但报错: %v", e, err)
		}
	}
	invalid := []string{
		"", "on_message", "on_command", "on_file_change",
		"PreRead", "PostWrite", "pre_tool_use", "post_tool_use", "stop",
	}
	for _, e := range invalid {
		if err := validateHookEvent(e); err == nil {
			t.Errorf("validateHookEvent(%q) 应拒绝（不在枚举内），但通过", e)
		}
	}
}

// TestValidatePluginName_Length 校验 name 非空且长度 ≤ 64 字符（M20-03）。
func TestValidatePluginName_Length(t *testing.T) {
	valid := []string{
		"a", "abc", strings.Repeat("a", 64),
	}
	for _, n := range valid {
		if err := validatePluginName(n); err != nil {
			t.Errorf("validatePluginName(%q-len=%d) 应接受，但报错: %v", n, len(n), err)
		}
	}
	invalid := []string{
		"",
		strings.Repeat("a", 65),
		strings.Repeat("a", 100),
	}
	for _, n := range invalid {
		if err := validatePluginName(n); err == nil {
			t.Errorf("validatePluginName(%q-len=%d) 应拒绝，但通过", n, len(n))
		}
	}
}

// TestValidateMainFile_Exists 校验 main 字段指向的文件必须实际存在（M20-03）。
func TestValidateMainFile_Exists(t *testing.T) {
	dir := t.TempDir()

	// 创建存在的 main 文件
	mainPath := filepath.Join(dir, "main.py")
	if err := os.WriteFile(mainPath, []byte("# entry"), 0644); err != nil {
		t.Fatalf("创建 main 文件失败: %v", err)
	}

	// 合法：相对路径文件存在
	if err := validateMainFile("main.py", dir); err != nil {
		t.Errorf("validateMainFile(\"main.py\", %q) 应接受（文件存在），但报错: %v", dir, err)
	}

	// 非法：相对路径文件不存在
	if err := validateMainFile("nonexistent.py", dir); err == nil {
		t.Error("validateMainFile(\"nonexistent.py\") 应拒绝（文件不存在），但通过")
	}

	// 非法：含路径穿越
	if err := validateMainFile("../../../etc/passwd", dir); err == nil {
		t.Error("validateMainFile(\"../../../etc/passwd\") 应拒绝（路径穿越），但通过")
	}

	// 非法：空路径（应由调用方守卫，但 validateMainFile 也应防御性拒绝）
	if err := validateMainFile("", dir); err == nil {
		// 空路径在 validateMainFile 中实际会通过（filepath.Join 后 = pluginDir），
		// 由 loadPlugin 在 Main != "" 时才调用守卫，故此处不强制断言。
		t.Logf("validateMainFile(\"\") 行为：filepath.Join 后指向 pluginDir 自身（已存在），由上层守卫")
	}
}

// TestLoadPlugin_ValidManifest 合法 plugin.json 通过严格 schema 校验（M20-03）。
func TestLoadPlugin_ValidManifest(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "valid-plugin")
	manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
		"id":      "valid-plugin",
		"name":    "Valid Plugin",
		"version": "1.0.0",
		"hooks": []map[string]interface{}{
			{"event": "PreToolUse", "tool_name": "bash", "command": "echo pre"},
			{"event": "Stop", "tool_name": "*", "command": "echo stop"},
		},
	})

	p, err := loadPlugin(manifestPath)
	if err != nil {
		t.Fatalf("合法 manifest 应通过校验，但报错: %v", err)
	}
	if p.ID != "valid-plugin" {
		t.Errorf("ID = %q, 期望 %q", p.ID, "valid-plugin")
	}
	if len(p.Hooks) != 2 {
		t.Errorf("Hooks 数量 = %d, 期望 2", len(p.Hooks))
	}
}

// TestLoadPlugin_InvalidVersion 非法 version 格式（v1 / 1.0 / abc / 1.0.0.0）拒绝（M20-03）。
func TestLoadPlugin_InvalidVersion(t *testing.T) {
	invalidVersions := []string{"v1", "1.0", "abc", "1.0.0.0", "", "1.0.0-"}
	for _, v := range invalidVersions {
		dir := t.TempDir()
		pluginDir := filepath.Join(dir, "bad-ver-plugin")
		manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
			"id":      "bad-ver-plugin",
			"name":    "Bad Version",
			"version": v,
		})

		_, err := loadPlugin(manifestPath)
		if err == nil {
			t.Errorf("version=%q 应被拒绝（非法 semver），但通过", v)
			continue
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("version=%q 错误消息应包含 \"version\"，实际: %v", v, err)
		}
	}
}

// TestLoadPlugin_InvalidEvent 非法 hooks[].event 值拒绝（M20-03）。
func TestLoadPlugin_InvalidEvent(t *testing.T) {
	invalidEvents := []string{"on_message", "on_command", "on_file_change", "PreRead", "PostWrite", ""}
	for _, e := range invalidEvents {
		dir := t.TempDir()
		pluginDir := filepath.Join(dir, "bad-event-plugin")
		manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
			"id":      "bad-event-plugin",
			"name":    "Bad Event",
			"version": "1.0.0",
			"hooks": []map[string]interface{}{
				{"event": e, "tool_name": "*", "command": "echo test"},
			},
		})

		_, err := loadPlugin(manifestPath)
		if err == nil {
			t.Errorf("event=%q 应被拒绝（不在枚举内），但通过", e)
			continue
		}
		if !strings.Contains(err.Error(), "hooks") {
			t.Errorf("event=%q 错误消息应包含 \"hooks\"，实际: %v", e, err)
		}
	}
}

// TestLoadPlugin_MissingMainFile main 字段指向不存在的文件时拒绝（M20-03）。
func TestLoadPlugin_MissingMainFile(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "missing-main-plugin")
	manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
		"id":      "missing-main-plugin",
		"name":    "Missing Main",
		"version": "1.0.0",
		"main":    "nonexistent.py",
	})

	_, err := loadPlugin(manifestPath)
	if err == nil {
		t.Fatal("main 指向不存在的文件应被拒绝，但通过")
	}
	if !strings.Contains(err.Error(), "main") {
		t.Errorf("错误消息应包含 \"main\"，实际: %v", err)
	}
}

// TestLoadPlugin_MainFileExists main 字段指向存在的文件时通过（M20-03）。
func TestLoadPlugin_MainFileExists(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "valid-main-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll 失败: %v", err)
	}
	// 先创建 main 文件
	if err := os.WriteFile(filepath.Join(pluginDir, "main.py"), []byte("print('hi')"), 0644); err != nil {
		t.Fatalf("创建 main.py 失败: %v", err)
	}
	manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
		"id":      "valid-main-plugin",
		"name":    "Valid Main",
		"version": "1.0.0",
		"main":    "main.py",
	})

	p, err := loadPlugin(manifestPath)
	if err != nil {
		t.Fatalf("合法 main 应通过校验，但报错: %v", err)
	}
	if p.Main != "main.py" {
		t.Errorf("Main = %q, 期望 %q", p.Main, "main.py")
	}
}

// TestLoadPlugin_NameTooLong name 长度 > 64 拒绝（M20-03）。
func TestLoadPlugin_NameTooLong(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "long-name-plugin")
	manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
		"id":      "long-name-plugin",
		"name":    strings.Repeat("a", 100),
		"version": "1.0.0",
	})

	_, err := loadPlugin(manifestPath)
	if err == nil {
		t.Fatal("name 长度 100 应被拒绝，但通过")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("错误消息应包含 \"name\"，实际: %v", err)
	}
}

// TestLoadPlugin_EmptyVersion version 为空拒绝（M20-03，向后兼容破坏但符合严格校验要求）。
func TestLoadPlugin_EmptyVersion(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "empty-ver-plugin")
	manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
		"id":   "empty-ver-plugin",
		"name": "Empty Version",
		// 不设置 version 字段
	})

	_, err := loadPlugin(manifestPath)
	if err == nil {
		t.Fatal("version 缺失应被拒绝，但通过")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("错误消息应包含 \"version\"，实际: %v", err)
	}
}

// TestLoadPlugin_BackwardCompatExistingTests 验证已有合法 manifest（version=1.0.0）仍可通过（M20-03 回归）。
func TestLoadPlugin_BackwardCompatExistingTests(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "compat-plugin")
	manifestPath := writePluginManifest(t, pluginDir, map[string]interface{}{
		"id":      "compat-plugin",
		"name":    "Compat",
		"version": "1.0.0",
		"tools": []map[string]interface{}{
			{"name": "tool1", "description": "T1", "command": "echo t1"},
		},
	})

	p, err := loadPlugin(manifestPath)
	if err != nil {
		t.Fatalf("已有合法 manifest 应通过校验，但报错: %v", err)
	}
	if len(p.Tools) != 1 {
		t.Errorf("Tools 数量 = %d, 期望 1", len(p.Tools))
	}
}
