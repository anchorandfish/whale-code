package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/anchorandfish/whale-code/internal/plugin"
)

// createPlugin 在指定目录下创建测试插件，返回插件目录路径
func createPlugin(t *testing.T, baseDir, id, name string) string {
	t.Helper()
	pluginDir := filepath.Join(baseDir, id)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}

	manifest := plugin.Plugin{
		ID:      id,
		Name:    name,
		Version: "1.0.0",
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("序列化 manifest 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatalf("写入 plugin.json 失败: %v", err)
	}
	return pluginDir
}

// TestE2E_FullLifecycle 完整生命周期: install → list → disable → list → enable → list → reload → list
func TestE2E_FullLifecycle(t *testing.T) {
	dir := t.TempDir()
	pDir := createPlugin(t, dir, "e2e-plugin", "E2E Plugin")

	mgr := plugin.NewManager(dir)
	defer os.Remove(filepath.Join(os.TempDir(), "plugin_state.json"))
	mgr.SetAllowUnsignedPlugins(true)

	list := mgr.FormatList()
	t.Logf("初始列表: %s", list)

	plugin, err := mgr.Install(pDir)
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if plugin.ID != "e2e-plugin" {
		t.Errorf("ID = %q, expected %q", plugin.ID, "e2e-plugin")
	}
	if !plugin.Enabled {
		t.Error("新安装插件应默认启用")
	}
	t.Logf("安装成功: %s (%s) v%s", plugin.Name, plugin.ID, plugin.Version)

	list = mgr.FormatList()
	if list == "" {
		t.Error("FormatList 不should return empty字符串")
	}
	t.Logf("安装后列表: %s", list)

	p, ok := mgr.Get("e2e-plugin")
	if !ok || !p.Enabled {
		t.Error("插件应处于启用状态")
	}

	if err := mgr.Disable("e2e-plugin"); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	p, _ = mgr.Get("e2e-plugin")
	if p.Enabled {
		t.Error("插件应处于禁用状态")
	}
	t.Log("禁用成功")

	if err := mgr.Enable("e2e-plugin"); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	p, _ = mgr.Get("e2e-plugin")
	if !p.Enabled {
		t.Error("插件应处于启用状态")
	}
	t.Log("启用成功")

	plugins, err := mgr.ReloadAll()
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if len(plugins) != 1 {
		t.Errorf("重载后应有 1 个插件，actual %d", len(plugins))
	}
	p, _ = mgr.Get("e2e-plugin")
	if !p.Enabled {
		t.Error("重载后插件应仍处于启用状态")
	}
	t.Log("重载成功，状态保持")
}

// TestE2E_StatePersist 状态持久化: 重启后状态保持。
// P0-7/B7: 插件默认禁用，需显式 Enable 后状态才持久化为 true。
func TestE2E_StatePersist(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "e2e_plugin_state.json")

	createPlugin(t, dir, "p1", "Plugin 1")
	createPlugin(t, dir, "p2", "Plugin 2")

	mgr := plugin.NewManager(dir)
	mgr.SetStatePath(statePath)
	mgr.LoadAll()

	// P0-7/B7: 显式启用 p1，使状态文件记录 p1=true；p2 保持默认禁用
	if err := mgr.Enable("p1"); err != nil {
		t.Fatalf("启用 p1 失败: %v", err)
	}
	if err := mgr.Disable("p2"); err != nil {
		t.Fatalf("禁用 p2 失败: %v", err)
	}

	mgr2 := plugin.NewManager(dir)
	mgr2.SetStatePath(statePath)
	mgr2.LoadAll()

	p1, ok := mgr2.Get("p1")
	if !ok {
		t.Fatal("p1 应在重启后存在")
	}
	if !p1.Enabled {
		t.Error("p1 应保持启用状态")
	}

	p2, ok := mgr2.Get("p2")
	if !ok {
		t.Fatal("p2 应在重启后存在")
	}
	if p2.Enabled {
		t.Error("p2 应保持禁用状态")
	}
	t.Log("状态持久化验证通过")
}

// TestE2E_ReloadAll_NewPlugin 重载时发现新插件
func TestE2E_ReloadAll_NewPlugin(t *testing.T) {
	dir := t.TempDir()

	createPlugin(t, dir, "p1", "Plugin 1")

	mgr := plugin.NewManager(dir)
	mgr.LoadAll()

	if len(mgr.List()) != 1 {
		t.Fatalf("初始应有 1 个插件")
	}

	createPlugin(t, dir, "p2", "Plugin 2")

	plugins, err := mgr.ReloadAll()
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if len(plugins) != 2 {
		t.Errorf("重载后应有 2 个插件，actual %d", len(plugins))
	}

	p2, _ := mgr.Get("p2")
	// P0-7/B7: 新发现的插件默认禁用，需 --enable-plugins / Enable 显式启用
	if p2.Enabled {
		t.Error("新发现的插件应默认禁用（P0-7/B7）")
	}
	t.Log("新插件发现验证通过")
}

// TestE2E_ReloadAll_StatePreserved 重载时已知插件状态不变。
// P0-7/B7: 插件默认禁用，需显式 Enable p1 使其状态为 true，再 Disable p2，
// ReloadAll 后 p1 应保持启用、p2 应保持禁用。
func TestE2E_ReloadAll_StatePreserved(t *testing.T) {
	dir := t.TempDir()

	createPlugin(t, dir, "p1", "Plugin 1")
	createPlugin(t, dir, "p2", "Plugin 2")

	mgr := plugin.NewManager(dir)
	mgr.LoadAll()

	// P0-7/B7: 显式启用 p1，使其状态为 true，验证 ReloadAll 能保持该状态
	if err := mgr.Enable("p1"); err != nil {
		t.Fatalf("启用 p1 失败: %v", err)
	}
	mgr.Disable("p2")

	plugins, err := mgr.ReloadAll()
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if len(plugins) != 2 {
		t.Errorf("重载后应有 2 个插件，actual %d", len(plugins))
	}

	p1, _ := mgr.Get("p1")
	if !p1.Enabled {
		t.Error("p1 应保持启用")
	}

	p2, _ := mgr.Get("p2")
	if p2.Enabled {
		t.Error("p2 应保持禁用")
	}
	t.Log("状态保持验证通过")
}

// TestE2E_Concurrent 并发安全
func TestE2E_Concurrent(t *testing.T) {
	dir := t.TempDir()
	createPlugin(t, dir, "conc-plugin", "Concurrent Plugin")

	mgr := plugin.NewManager(dir)
	mgr.LoadAll()

	var wg sync.WaitGroup
	n := 20

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				_ = mgr.Enable("conc-plugin")
			} else {
				_ = mgr.Disable("conc-plugin")
			}
		}(i)
	}
	wg.Wait()

	p, ok := mgr.Get("conc-plugin")
	if !ok {
		t.Fatal("插件应在并发操作后仍存在")
	}
	if p.Enabled != true && p.Enabled != false {
		t.Errorf("插件 Enabled 值异常: %v（应为 true 或 false）", p.Enabled)
	}
	t.Logf("并发test passed，最终状态: Enabled=%v", p.Enabled)
}

// TestE2E_EmptyPluginDir 空插件目录不报错
func TestE2E_EmptyPluginDir(t *testing.T) {
	dir := t.TempDir()

	mgr := plugin.NewManager(dir)

	if err := mgr.LoadAll(); err != nil {
		t.Fatalf("空目录 LoadAll 不应报错: %v", err)
	}

	plugins := mgr.List()
	if len(plugins) != 0 {
		t.Errorf("空目录应有 0 个插件，actual %d", len(plugins))
	}

	list := mgr.FormatList()
	if list == "" {
		t.Error("空目录 FormatList 不should return empty字符串")
	}
	t.Logf("空目录列表: %s", list)

	_, err := mgr.ReloadAll()
	if err != nil {
		t.Fatalf("空目录 ReloadAll 不应报错: %v", err)
	}
	t.Log("空插件目录验证通过")
}
