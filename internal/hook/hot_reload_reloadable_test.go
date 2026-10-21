// hot_reload_reloadable_test.go — Reloadable 接口抽象测试（C14 复用承诺）
// 验证 HotReloader 可为任意 Reloadable 对象复用，而非仅绑定 *Manager
package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockReloadable 用于测试的 Reloadable 实现（不依赖 *Manager）
type mockReloadable struct {
	mu       sync.Mutex
	path     string
	content  string
	reloadN  int64
	failNext bool
}

// Reload 实现 Reloadable 接口
func (m *mockReloadable) Reload(path string) error {
	m.mu.Lock()
	if m.failNext {
		m.failNext = false
		m.mu.Unlock()
		return fmt.Errorf("mock reload failed")
	}
	m.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = path
	m.content = string(data)
	atomic.AddInt64(&m.reloadN, 1)
	return nil
}

func (m *mockReloadable) SetFailNext() {
	m.mu.Lock()
	m.failNext = true
	m.mu.Unlock()
}

// TestManager_ImplementsReloadable 验证 *Manager 实现 Reloadable 接口
func TestManager_ImplementsReloadable(t *testing.T) {
	var _ Reloadable = (*Manager)(nil)
	t.Log("✓ *Manager 实现 Reloadable 接口")
}

// TestHotReloader_Reloadable_Mock 验证 HotReloader 可为 mock Reloadable 触发重载
func TestHotReloader_Reloadable_Mock(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mock.json")

	// 初始文件
	if err := os.WriteFile(configPath, []byte(`{"v":1}`), 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	m := &mockReloadable{}
	h, err := NewHotReloaderForReloadable(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()

	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 修改文件
	if err := os.WriteFile(configPath, []byte(`{"v":2}`), 0644); err != nil {
		t.Fatalf("modify file: %v", err)
	}

	// 等待重载
	if !waitForReloadCount(t, h, 1, 3*time.Second) {
		t.Fatalf("expected reload count >= 1, got %d", h.ReloadCount())
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.content != `{"v":2}` {
		t.Errorf("expected content {\"v\":2}, got %s", m.content)
	}
}

// TestHotReloader_Reloadable_ReloadFailure 验证 Reload 失败时保留旧状态
func TestHotReloader_Reloadable_ReloadFailure(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mock.json")

	if err := os.WriteFile(configPath, []byte(`{"v":1}`), 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	m := &mockReloadable{}
	h, err := NewHotReloaderForReloadable(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()

	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 触发一次成功重载
	if err := os.WriteFile(configPath, []byte(`{"v":2}`), 0644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	if !waitForReloadCount(t, h, 1, 3*time.Second) {
		t.Fatalf("expected reload count >= 1, got %d", h.ReloadCount())
	}

	// 触发一次失败重载
	m.SetFailNext()
	if err := os.WriteFile(configPath, []byte(`{"v":3}`), 0644); err != nil {
		t.Fatalf("modify file for failure: %v", err)
	}
	// 等待错误状态
	if !waitForErrorState(t, h, true, 3*time.Second) {
		t.Fatal("expected error state after failed reload")
	}

	// 内存内容应仍为上次成功的 {"v":2}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.content != `{"v":2}` {
		t.Errorf("expected content to remain {\"v\":2} after failed reload, got %s", m.content)
	}
}

// TestHotReloader_Manager_BackwardCompat 验证旧 NewHotReloader(mgr, path) 向后兼容
func TestHotReloader_Manager_BackwardCompat(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")

	// 初始配置
	initialConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionBlock}},
		},
	}
	data, _ := json.Marshal(initialConfig)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	m := NewManager(tmpDir)
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config: %v", err)
	}

	// 旧 API：NewHotReloader(mgr, path)
	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader (legacy API): %v", err)
	}
	defer h.Stop()

	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 修改配置
	newConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
		},
	}
	newData, _ := json.Marshal(newConfig)
	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		t.Fatalf("modify config: %v", err)
	}

	// 等待重载
	if !waitForReloadCount(t, h, 1, 3*time.Second) {
		t.Fatalf("expected reload count >= 1, got %d", h.ReloadCount())
	}

	// 验证 Manager 配置已更新
	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse hook, got %d", len(cfg.PreToolUse))
	}
	if cfg.PreToolUse[0].Base().Matcher != "*" {
		t.Errorf("expected matcher '*' after reload, got %s", cfg.PreToolUse[0].Base().Matcher)
	}
}

// TestManager_Reload_FullReplacement 验证 Manager.Reload 是全量替换而非追加合并
// 这是 OPT-01 的关键设计点：Reload 行为等价于原 reloadNow（全量替换），
// 而非 LoadConfig（追加合并）
func TestManager_Reload_FullReplacement(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")

	// 初始配置：1 个 PreToolUse 钩子
	initialConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionBlock}},
		},
	}
	data, _ := json.Marshal(initialConfig)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	m := NewManager(tmpDir)
	// 先用 LoadConfig 加载（追加模式）
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(m.Config().PreToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse after LoadConfig, got %d", len(m.Config().PreToolUse))
	}

	// 再次 LoadConfig（追加模式，应变为 2 个）
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config again: %v", err)
	}
	if len(m.Config().PreToolUse) != 2 {
		t.Fatalf("expected 2 PreToolUse after second LoadConfig (append mode), got %d", len(m.Config().PreToolUse))
	}

	// 用 Reload 加载（全量替换，应变为 1 个）
	if err := m.Reload(configPath); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(m.Config().PreToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse after Reload (full replacement), got %d", len(m.Config().PreToolUse))
	}
}

// TestManager_Reload_FileNotFound 验证 Reload 文件不存在时报错
func TestManager_Reload_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir)
	nonexistentPath := filepath.Join(tmpDir, "nonexistent.json")

	err := m.Reload(nonexistentPath)
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}
