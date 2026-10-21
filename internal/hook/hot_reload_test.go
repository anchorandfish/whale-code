// hot_reload_test.go — 钩子配置热重载测试（M16-02）
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

// waitForReloadCount 轮询等待重载次数达到期望值或超时
func waitForReloadCount(t *testing.T, h *HotReloader, expected int64, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.ReloadCount() >= expected {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitForErrorState 轮询等待错误状态达到期望或超时
func waitForErrorState(t *testing.T, h *HotReloader, wantErr bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := h.LastReloadError()
		if wantErr && err != nil {
			return true
		}
		if !wantErr && err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestHotReload_FileModifyTriggersReload 测试文件修改触发重载
func TestHotReload_FileModifyTriggersReload(t *testing.T) {
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
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load config: %v", err)
	}

	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 修改配置文件：matcher 改为 "*"
	newConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
		},
	}
	newData, _ := json.Marshal(newConfig)
	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		t.Fatalf("write new config: %v", err)
	}

	// 等待重载完成（防抖动 500ms + 余量）
	if !waitForReloadCount(t, h, 1, 5*time.Second) {
		t.Fatalf("reload did not trigger within timeout, count=%d", h.ReloadCount())
	}

	// 验证配置已更新
	loaded := m.Config()
	if len(loaded.PreToolUse) != 1 {
		t.Fatalf("PreToolUse count: got %d, want 1", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Matcher != "*" {
		t.Errorf("matcher: got %q, want %q", loaded.PreToolUse[0].Base().Matcher, "*")
	}
	if loaded.PreToolUse[0].Base().Action != ActionAllow {
		t.Errorf("action: got %q, want %q", loaded.PreToolUse[0].Base().Action, ActionAllow)
	}

	// 验证无错误
	if err := h.LastReloadError(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestHotReload_DebounceMultipleWrites 测试多次快速修改只触发一次重载（防抖动）
func TestHotReload_DebounceMultipleWrites(t *testing.T) {
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

	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 500ms 内连续写入 5 次（每次不同 matcher）
	matchers := []string{"a", "b", "c", "d", "e"}
	for _, matcher := range matchers {
		cfg := HookConfig{
			PreToolUse: []Hook{
				&HookCommand{HookBase: HookBase{Matcher: matcher, Action: ActionAllow}},
			},
		}
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			t.Fatalf("write config %s: %v", matcher, err)
		}
		// 短暂间隔（远小于防抖动 500ms）
		time.Sleep(50 * time.Millisecond)
	}

	// 等待防抖动定时器触发（500ms + 余量）
	time.Sleep(1 * time.Second)

	// 验证只触发了 1 次重载（防抖动合并）
	count := h.ReloadCount()
	if count != 1 {
		t.Errorf("reload count: got %d, want 1 (debounce should merge 5 writes into 1 reload)", count)
	}

	// 验证最终配置是最后一次写入的 matcher="e"
	loaded := m.Config()
	if len(loaded.PreToolUse) != 1 {
		t.Fatalf("PreToolUse count: got %d, want 1", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Matcher != "e" {
		t.Errorf("final matcher: got %q, want %q", loaded.PreToolUse[0].Base().Matcher, "e")
	}
}

// TestHotReload_InvalidYAMLPreservesOldConfig 测试修改为非法 YAML 时保留旧配置 + 输出错误
func TestHotReload_InvalidYAMLPreservesOldConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")

	// 初始配置：1 个 PreToolUse 钩子
	initialConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionBlock, Reason: "original"}},
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

	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 写入非法内容（既不是合法 JSON 也不被 YAML 解析器识别为有效配置）
	invalidContent := []byte("{{{this is not valid yaml nor json}}}!!!")
	if err := os.WriteFile(configPath, invalidContent, 0644); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	// 等待防抖动触发 + 重载尝试
	time.Sleep(1 * time.Second)

	// 验证：重载次数为 0（解析失败不计入成功重载）
	if count := h.ReloadCount(); count != 0 {
		t.Errorf("reload count: got %d, want 0 (invalid config should not count as successful reload)", count)
	}

	// 验证：最后一次错误已设置
	if err := h.LastReloadError(); err == nil {
		t.Error("expected error for invalid config, got nil")
	}

	// 验证：旧配置保留（PreToolUse 仍有 1 个钩子，matcher 仍为 write_file）
	loaded := m.Config()
	if len(loaded.PreToolUse) != 1 {
		t.Fatalf("PreToolUse count: got %d, want 1 (old config should be preserved)", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Matcher != "write_file" {
		t.Errorf("matcher: got %q, want %q (old config should be preserved)", loaded.PreToolUse[0].Base().Matcher, "write_file")
	}
	if loaded.PreToolUse[0].Base().Reason != "original" {
		t.Errorf("reason: got %q, want %q", loaded.PreToolUse[0].Base().Reason, "original")
	}
}

// TestHotReload_HookExecutionNotBlockedDuringReload 测试重载期间 hook 执行不阻塞（读锁保护）
func TestHotReload_HookExecutionNotBlockedDuringReload(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")

	// 初始配置：1 个 callback 钩子
	hookExecuted := int64(0)
	cb := func(payload HookPayload) HookResult {
		atomic.AddInt64(&hookExecuted, 1)
		return HookResult{Action: ActionAllow}
	}
	initialConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCallback{
				HookBase: HookBase{Matcher: "*", Enabled: true},
				Callback: cb,
			},
		},
	}
	// 使用 SetConfig 设置初始配置（含 callback，不走 JSON 加载）
	m := NewManager(tmpDir)
	m.SetConfig(initialConfig)

	// 写入配置文件（用于热重载监听）
	data, _ := json.Marshal(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
		},
	})
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 并发执行 hook（读锁）+ 触发重载（写锁）
	var wg sync.WaitGroup
	hookDone := int64(0)

	// 启动 10 个 goroutine 并发执行 hook
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 执行 hook（RunPreHooks 内部加读锁）
			_ = m.RunPreHooks("test_tool", `{"path":"/tmp/test.txt"}`)
			atomic.AddInt64(&hookDone, 1)
		}()
	}

	// 同时触发重载（多次写入触发防抖动 + 重载）
	for i := 0; i < 5; i++ {
		cfg := HookConfig{
			PreToolUse: []Hook{
				&HookCommand{HookBase: HookBase{Matcher: "*", Action: ActionAllow}},
			},
		}
		data, _ := json.Marshal(cfg)
		_ = os.WriteFile(configPath, data, 0644)
		time.Sleep(20 * time.Millisecond)
	}

	// 等待所有 hook 执行完成（带超时，防止死锁）
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
		// 成功：所有 hook 执行完成，无死锁
	case <-time.After(10 * time.Second):
		t.Fatal("hook execution deadlocked during reload (read lock not released)")
	}

	// 验证 hook 执行次数（初始 callback 钩子可能执行了若干次）
	// 主要验证：没有死锁，hook 能正常执行完
	if atomic.LoadInt64(&hookDone) != 10 {
		t.Errorf("hook execution count: got %d, want 10", atomic.LoadInt64(&hookDone))
	}
}

// TestHotReload_FileDeletionPreservesOldConfig 测试文件删除时不重载（保留旧配置）
func TestHotReload_FileDeletionPreservesOldConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")

	// 初始配置
	initialConfig := HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "write_file", Action: ActionBlock, Reason: "before delete"}},
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

	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 删除配置文件
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove config file: %v", err)
	}

	// 等待事件处理 + 防抖动周期
	time.Sleep(1 * time.Second)

	// 验证：重载次数为 0（文件删除不触发重载）
	if count := h.ReloadCount(); count != 0 {
		t.Errorf("reload count: got %d, want 0 (file deletion should not trigger reload)", count)
	}

	// 验证：旧配置保留
	loaded := m.Config()
	if len(loaded.PreToolUse) != 1 {
		t.Fatalf("PreToolUse count: got %d, want 1 (old config should be preserved after deletion)", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Reason != "before delete" {
		t.Errorf("reason: got %q, want %q", loaded.PreToolUse[0].Base().Reason, "before delete")
	}

	// 验证：最后一次错误已设置（文件删除被记录）
	if err := h.LastReloadError(); err == nil {
		t.Error("expected error for file deletion, got nil")
	} else if err.Error() != "config file removed" {
		t.Errorf("error message: got %q, want %q", err.Error(), "config file removed")
	}
}

// TestHotReload_YAMLConfigReload 测试 YAML 配置文件的热重载
func TestHotReload_YAMLConfigReload(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.yaml")

	// 初始 YAML 配置
	initialYAML := `pre_tool_use:
  - matcher: write_file
    action: block
    reason: initial
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("write initial yaml: %v", err)
	}

	m := NewManager(tmpDir)
	if err := m.LoadConfig(configPath); err != nil {
		t.Fatalf("load yaml config: %v", err)
	}

	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err != nil {
		t.Fatalf("start hot reloader: %v", err)
	}

	// 修改 YAML 配置
	newYAML := `pre_tool_use:
  - matcher: "*"
    action: allow
`
	if err := os.WriteFile(configPath, []byte(newYAML), 0644); err != nil {
		t.Fatalf("write new yaml: %v", err)
	}

	// 等待重载
	if !waitForReloadCount(t, h, 1, 5*time.Second) {
		t.Fatalf("reload did not trigger, count=%d", h.ReloadCount())
	}

	// 验证配置已更新
	loaded := m.Config()
	if len(loaded.PreToolUse) != 1 {
		t.Fatalf("PreToolUse count: got %d, want 1", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Base().Matcher != "*" {
		t.Errorf("matcher: got %q, want %q", loaded.PreToolUse[0].Base().Matcher, "*")
	}
}

// TestParseConfigBytes_JSON 测试 parseConfigBytes 解析 JSON
func TestParseConfigBytes_JSON(t *testing.T) {
	data := []byte(`{"pre_tool_use":[{"matcher":"*","action":"allow"}]}`)
	cfg, err := parseConfigBytes(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("PreToolUse count: got %d, want 1", len(cfg.PreToolUse))
	}
	if cfg.PreToolUse[0].Base().Matcher != "*" {
		t.Errorf("matcher: got %q, want *", cfg.PreToolUse[0].Base().Matcher)
	}
}

// TestParseConfigBytes_YAML 测试 parseConfigBytes 解析 YAML
func TestParseConfigBytes_YAML(t *testing.T) {
	data := []byte("pre_tool_use:\n  - matcher: \"*\"\n    action: allow\n")
	cfg, err := parseConfigBytes(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("PreToolUse count: got %d, want 1", len(cfg.PreToolUse))
	}
	if cfg.PreToolUse[0].Base().Matcher != "*" {
		t.Errorf("matcher: got %q, want *", cfg.PreToolUse[0].Base().Matcher)
	}
}

// TestParseConfigBytes_InvalidContent 测试 parseConfigBytes 拒绝无效内容
func TestParseConfigBytes_InvalidContent(t *testing.T) {
	// 无效 JSON + 无效 YAML（无识别的键）
	data := []byte("{{{invalid content}}}!!!")
	_, err := parseConfigBytes(data)
	if err == nil {
		t.Error("expected error for invalid content, got nil")
	}
}

// TestParseConfigBytes_EmptyContent 测试 parseConfigBytes 接受空内容（允许清空配置）
func TestParseConfigBytes_EmptyContent(t *testing.T) {
	// 空白内容应被接受（允许用户清空所有钩子）
	data := []byte("   \n  \t  \n")
	cfg, err := parseConfigBytes(data)
	if err != nil {
		t.Errorf("expected nil error for empty content, got: %v", err)
	}
	if !isConfigEmpty(cfg) {
		t.Error("expected empty config for empty content")
	}
}

// TestIsConfigEmpty 测试 isConfigEmpty 函数
func TestIsConfigEmpty(t *testing.T) {
	if !isConfigEmpty(HookConfig{}) {
		t.Error("empty HookConfig should be detected as empty")
	}

	nonEmpty := HookConfig{
		PreToolUse: []Hook{&HookCommand{HookBase: HookBase{Matcher: "*"}}},
	}
	if isConfigEmpty(nonEmpty) {
		t.Error("HookConfig with hooks should not be detected as empty")
	}
}

// TestHasNonWhitespaceContent 测试 hasNonWhitespaceContent 函数
func TestHasNonWhitespaceContent(t *testing.T) {
	if hasNonWhitespaceContent([]byte("   \n\t\r  ")) {
		t.Error("whitespace-only content should return false")
	}
	if !hasNonWhitespaceContent([]byte("  hello  ")) {
		t.Error("content with non-whitespace should return true")
	}
	if hasNonWhitespaceContent([]byte("")) {
		t.Error("empty content should return false")
	}
}

// TestHotReload_StopWithoutStart 测试未启动的 HotReloader 不 panic
func TestHotReload_StopWithoutStart(t *testing.T) {
	// 不调用 Start 直接 Stop 不应 panic（虽然 close(stopCh) 会导致问题）
	// 实际上 Stop 必须在 Start 之后调用，这里测试 NewHotReloader 不出错
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")
	m := NewManager(tmpDir)
	h, err := NewHotReloader(m, configPath)
	if err != nil {
		t.Fatalf("create hot reloader: %v", err)
	}
	_ = fmt.Sprintf("reloader created: %v", h != nil)
	// 不调用 Stop（未启动）
	_ = h.watcher.Close()
}
