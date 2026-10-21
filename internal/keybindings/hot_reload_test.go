// hot_reload_test.go — M11-02 keybindings 配置热重载测试（收尾补全）。
//
// 覆盖 5 个测试：Start 触发重载 / 防抖动 / 重载失败保留旧配置 / Stop 不再监听 / 并发安全。
//
// 对标 VSCode keybindings.json 修改后自动生效；C14 复用 hook.HotReloader 同构模式
// （fsnotify + 500ms 防抖 + 监听目录兼容原子替换）。
//
// 注意：路径需含 /.whale/ 子目录通过 isKeybindingsPathAllowed 白名单校验
// （loader.go:52-74）。
package keybindings

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeKeybindingsConfig 写入 keybindings.json 配置文件（含 .whale/ 子目录通过路径校验）。
// writeKeybindingsConfig 在指定 dir 下创建 .whale/keybindings.json 并写入 content。
func writeKeybindingsConfig(t *testing.T, dir, content string) string {
	t.Helper()
	configDir := filepath.Join(dir, ".whale")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("MkdirAll %s: %v", configDir, err)
	}
	configPath := filepath.Join(configDir, "keybindings.json")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile %s: %v", configPath, err)
	}
	return configPath
}

// waitForReloadCondition 轮询等待 ReloadCount 满足 cond 或超时，返回最终 ReloadCount。
// waitForReloadCondition 防止单次 sleep 过长导致测试慢，采用 50ms 粒度轮询。
func waitForReloadCondition(hr *HotReloader, timeout time.Duration, cond func(int64) bool) int64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c := hr.ReloadCount()
		if cond(c) {
			return c
		}
		time.Sleep(50 * time.Millisecond)
	}
	return hr.ReloadCount()
}

// TestHotReloader_Start 验证写入配置文件后 500ms 防抖触发 reload。
//
// 流程：创建 vim 配置（含 x→delete_char 绑定）→ Manager 加载 → HotReloader 启动
// → 改写配置（追加 y→delete_char 绑定）→ 等待防抖 + reload
// → 验证 ReloadCount > 0 且 Manager 仍可正常 Parse（验证 reload 不破坏 Manager）。
//
// 注意：manager.go load() 的 preservedMode 设计意图是"用户 SetMode 后 Reload 不被文件覆盖"，
// 因此本测试不验证 mode 切换（vim→emacs），改为验证 ReloadCount + Parse 正常。
// 改写配置只追加 binding（mode 保持 vim），避开 preservedMode 行为。
func TestHotReloader_Start(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeKeybindingsConfig(t, tmpDir, `{"mode":"vim","bindings":[]}`)

	mgr, err := NewManager(configPath, "")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if mode := mgr.Mode(); mode != "vim" {
		t.Fatalf("initial Mode = %q, want vim", mode)
	}

	hr, err := NewHotReloader(mgr, configPath, "")
	if err != nil {
		t.Fatalf("NewHotReloader failed: %v", err)
	}
	if err := hr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer hr.Stop()

	// 等 watcher 完全启动
	time.Sleep(200 * time.Millisecond)

	// 改写配置：追加 binding（mode 保持 vim，避开 preservedMode）
	newContent := `{"mode":"vim","bindings":[{"key":"x","action":"delete_char","mode":"normal"}]}`
	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 等待防抖（500ms）+ reload，最多 2 秒
	count := waitForReloadCondition(hr, 2*time.Second, func(c int64) bool { return c > 0 })
	if count == 0 {
		t.Fatal("ReloadCount = 0, want > 0 (reload should trigger after file change)")
	}

	// 验证 Manager 仍可正常 Parse（reload 不破坏 Manager 状态）
	// 输入 'x' 在 normal 模式应解析为 delete_char action
	action := mgr.Parse(Key{Type: 0, Runes: []rune{'x'}}, "normal")
	if action.Type == ActionPassthrough {
		t.Error("Manager.Parse('x', normal) = ActionPassthrough, want delete_char (reload should load new binding)")
	}

	// 验证 Manager.Mode 仍为 vim（preservedMode 设计行为，文件未改 mode）
	if mode := mgr.Mode(); mode != "vim" {
		t.Errorf("Manager.Mode() = %q, want vim (preservedMode keeps old mode on reload)", mode)
	}
}

// TestHotReloader_Debounce 验证 500ms 内多次写入只触发 1 次 reload。
//
// 流程：连续写 3 次配置（间隔 < 500ms）→ 等待防抖结束 → 验证 ReloadCount == 1。
func TestHotReloader_Debounce(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeKeybindingsConfig(t, tmpDir, `{"mode":"vim","bindings":[]}`)

	mgr, err := NewManager(configPath, "")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	hr, err := NewHotReloader(mgr, configPath, "")
	if err != nil {
		t.Fatalf("NewHotReloader failed: %v", err)
	}
	if err := hr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer hr.Stop()

	time.Sleep(200 * time.Millisecond)

	// 500ms 内连续 3 次写入（每次内容不同，触发 fsnotify 事件）
	for i := 0; i < 3; i++ {
		content := `{"mode":"vim","bindings":[{"key":"x","action":"delete_char","mode":"normal"}]}`
		if i == 1 {
			content = `{"mode":"vim","bindings":[{"key":"y","action":"delete_char","mode":"normal"}]}`
		} else if i == 2 {
			content = `{"mode":"vim","bindings":[{"key":"z","action":"delete_char","mode":"normal"}]}`
		}
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile[%d]: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond) // < debounceInterval(500ms)
	}

	// 等待防抖结束 + reload，最多 2 秒
	count := waitForReloadCondition(hr, 2*time.Second, func(c int64) bool { return c > 0 })
	if count == 0 {
		t.Fatal("ReloadCount = 0, want > 0 (at least 1 reload after debounce)")
	}

	// 再等 1 秒确保没有第二次 reload
	time.Sleep(1 * time.Second)
	finalCount := hr.ReloadCount()
	if finalCount > 1 {
		t.Errorf("ReloadCount = %d, want <= 1 (debounce should coalesce 3 writes into 1 reload)", finalCount)
	}
}

// TestHotReloader_ReloadError_KeepOldConfig 验证写坏配置时保留旧配置。
//
// 流程：vim 配置 → Manager 加载 → HotReloader 启动 → 写入非法 JSON
// → 等待 reload 触发 → 验证 ReloadCount 不增加 + LastReloadError 非 nil + Manager.Mode 仍为 vim。
func TestHotReloader_ReloadError_KeepOldConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeKeybindingsConfig(t, tmpDir, `{"mode":"vim","bindings":[]}`)

	mgr, err := NewManager(configPath, "")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	hr, err := NewHotReloader(mgr, configPath, "")
	if err != nil {
		t.Fatalf("NewHotReloader failed: %v", err)
	}
	if err := hr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer hr.Stop()

	time.Sleep(200 * time.Millisecond)

	// 写入非法 JSON（语法错误，LoadAll 会返回 error）
	if err := os.WriteFile(configPath, []byte(`{invalid json`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 等待 reload 触发（失败也会调 setLastError，但 ReloadCount 不增加）
	time.Sleep(1500 * time.Millisecond)

	// ReloadCount 应为 0（失败不计入成功重载）
	if count := hr.ReloadCount(); count != 0 {
		t.Errorf("ReloadCount = %d, want 0 (failed reload should not increment)", count)
	}

	// LastReloadError 应非 nil
	if err := hr.LastReloadError(); err == nil {
		t.Error("LastReloadError = nil, want error (invalid JSON should set last error)")
	}

	// Manager.Mode 应仍为 vim（旧配置保留）
	if mode := mgr.Mode(); mode != "vim" {
		t.Errorf("Manager.Mode() = %q, want vim (old config preserved on reload error)", mode)
	}
}

// TestHotReloader_Stop 验证 Stop 后不再监听文件变更。
//
// 流程：启动 HotReloader → Stop → 修改配置 → 等待 → 验证 ReloadCount 仍为 0。
func TestHotReloader_Stop(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeKeybindingsConfig(t, tmpDir, `{"mode":"vim","bindings":[]}`)

	mgr, err := NewManager(configPath, "")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	hr, err := NewHotReloader(mgr, configPath, "")
	if err != nil {
		t.Fatalf("NewHotReloader failed: %v", err)
	}
	if err := hr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	hr.Stop() // 停止监听

	// 修改配置
	if err := os.WriteFile(configPath, []byte(`{"mode":"emacs","bindings":[]}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 等待足够长以确保没有 reload 触发
	time.Sleep(1500 * time.Millisecond)

	if count := hr.ReloadCount(); count != 0 {
		t.Errorf("ReloadCount = %d, want 0 (Stop should disable watching)", count)
	}
	if mode := mgr.Mode(); mode != "vim" {
		t.Errorf("Manager.Mode() = %q, want vim (no reload after Stop)", mode)
	}
}

// TestHotReloader_ConcurrentSafe 验证并发写文件 + 读指标 + 调用 Manager.Parse 不 panic（-race 通过）。
//
// 多 goroutine 同时：写配置文件、读 ReloadCount/LastReloadError、调 mgr.Parse，
// 验证 HotReloader 内部锁（debounceMu + errMu）与 Manager RWMutex 协同正确。
func TestHotReloader_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeKeybindingsConfig(t, tmpDir, `{"mode":"vim","bindings":[]}`)

	mgr, err := NewManager(configPath, "")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	hr, err := NewHotReloader(mgr, configPath, "")
	if err != nil {
		t.Fatalf("NewHotReloader failed: %v", err)
	}
	if err := hr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer hr.Stop()

	time.Sleep(200 * time.Millisecond)

	configs := []string{
		`{"mode":"vim","bindings":[]}`,
		`{"mode":"emacs","bindings":[]}`,
		`{"mode":"vim","bindings":[{"key":"x","action":"delete_char","mode":"normal"}]}`,
	}

	var wg sync.WaitGroup
	// 5 个 goroutine 写文件
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				content := configs[(id+j)%len(configs)]
				_ = os.WriteFile(configPath, []byte(content), 0644)
				time.Sleep(20 * time.Millisecond)
			}
		}(i)
	}

	// 5 个 goroutine 读指标
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = hr.ReloadCount()
				_ = hr.LastReloadError()
			}
		}()
	}

	// 5 个 goroutine 调用 Manager.Parse（读锁）
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = mgr.Parse(Key{Type: 0, Runes: []rune{'j'}}, "normal")
			}
		}()
	}

	wg.Wait()
}
