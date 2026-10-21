package config

// 反例测试（Bug Reproduction Tests）
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
// go test ./internal/config/ -run TestBug_ -v
//
// 预期结果：当前代码下所有 TestBug_ 测试应失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试应通过（证明 bug 已修）

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBug_WouldLoseAuthStateMissing 对应 FACT-CONFIG-001
// 写入前必须检查 wouldLoseAuthState，防止默认配置覆盖 OAuth token
// 当前实现：Save 无 wouldLoseAuthState 检查，无条件覆盖已有配置
func TestBug_WouldLoseAuthStateMissing(t *testing.T) {
	// FACT-CONFIG-001
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	// 收口后 Loader.Save 的目标目录从 whalehome.Dir()（$WHALE_HOME > $HOME/.whale）解析，
	// 不再取 l.userDir ⇒ 只传 NewLoader(dir, "") 已不再构成隔离。
	// 缺这行时本用例会写到**宿主的真实** ~/.whale/settings.json（实测：把用户的 model 覆写成空值）。
	t.Setenv("WHALE_HOME", settingsDir)
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 写入包含 OAuth token 的配置（模拟已登录状态）
	// 注意：whale-code 的 Config 结构体当前无 oauth 字段，这里用原始 JSON 模拟
	// 真实场景中 同类工具 的 config.json 包含 oauthAccount 字段
	existingData := `{"model":"existing","oauth_token":"secret-abc123","oauth_account":{"token":"bearer-xyz"}}`
	if err := os.WriteFile(settingsPath, []byte(existingData), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 用默认配置 Save（模拟未登录的默认配置覆盖已登录配置）
	loader := NewLoader(dir, "")
	cfg := DefaultConfig()
	err := loader.Save(&cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 读取保存后的文件，验证 OAuth token 是否丢失
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var saved map[string]interface{}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// 期望：Save 应检测到 wouldLoseAuthState 并警告/阻止覆盖
	// 实际：Save 无条件覆盖，oauth_token 丢失
	if _, ok := saved["oauth_token"]; !ok {
		t.Errorf("BUG [FACT-CONFIG-001]: Save 没有 wouldLoseAuthState 检查\n" +
			"  原配置: 包含 oauth_token (已登录状态)\n" +
			"  保存后: oauth_token 丢失 (被默认配置覆盖)\n" +
			"  期望: Save 应检查 wouldLoseAuthState，警告或阻止覆盖\n" +
			"  实际: Save 无条件覆盖，丢失 OAuth token (GH #3117)\n" +
			"  证据: 外部参考实现 wouldLoseAuthState\n" +
			"  whale-code: internal/config/config.go Save 无此检查")
	}
}

// TestBug_MergeIntoMissingFields 对应 FACT-CONFIG-002
// 配置合并必须覆盖所有字段，当前 mergeInto 遗漏 McpServers/Hooks 等字段
// ★ D 批：custom_pricing 已随成本线删除，不再是"应合并字段"。
func TestBug_MergeIntoMissingFields(t *testing.T) {
	// FACT-CONFIG-002
	cfg := DefaultConfig()
	raw := map[string]interface{}{
		"mcp_servers": map[string]interface{}{
			"test-server": map[string]interface{}{
				"command": "test-cmd",
				"args":    []interface{}{"--flag"},
			},
		},
		"hooks": map[string]interface{}{
			"pre_tool_use": []interface{}{
				map[string]interface{}{"command": "echo", "shell": "bash"},
			},
		},
		"output_style":          "verbose",
		"whale_env":             map[string]interface{}{"KEY": "value"},
		"cache_break_threshold": 0.1,
		"metrics_enabled":       true,
		"metrics_addr":          ":8080",
	}

	mergeInto(&cfg, raw)

	// 期望：所有字段都被合并
	// 实际：mergeInto 不处理这 8 个字段，全部丢失
	if len(cfg.McpServers) == 0 {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 mcp_servers\n" +
			"  期望: McpServers 包含 test-server\n" +
			"  实际: McpServers 为空\n" +
			"  原因: config.go:258-299 mergeInto 无 mcp_servers 处理分支\n" +
			"  证据: 外部参考实现 mergeInto 合并所有字段")
	}

	if len(cfg.Hooks.PreToolUse) == 0 {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 hooks\n" +
			"  期望: Hooks.PreToolUse 包含 1 条规则\n" +
			"  实际: Hooks.PreToolUse 为空\n" +
			"  原因: config.go:258-299 mergeInto 无 hooks 处理分支")
	}

	if cfg.OutputStyle != OutputVerbose {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 output_style\n"+
			"  期望: OutputStyle = verbose\n"+
			"  实际: OutputStyle = %s\n"+
			"  原因: config.go:258-299 mergeInto 无 output_style 处理分支",
			cfg.OutputStyle)
	}

	if len(cfg.WhaleEnv) == 0 || cfg.WhaleEnv["KEY"] != "value" {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 whale_env\n"+
			"  期望: WhaleEnv[KEY] = value\n"+
			"  实际: WhaleEnv = %v\n"+
			"  原因: config.go:258-299 mergeInto 无 whale_env 处理分支",
			cfg.WhaleEnv)
	}

	if cfg.CacheBreakThreshold != 0.1 {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 cache_break_threshold\n"+
			"  期望: CacheBreakThreshold = 0.1\n"+
			"  实际: CacheBreakThreshold = %f\n"+
			"  原因: config.go:258-299 mergeInto 无 cache_break_threshold 处理分支",
			cfg.CacheBreakThreshold)
	}

	if !cfg.MetricsEnabled {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 metrics_enabled\n"+
			"  期望: MetricsEnabled = true\n"+
			"  实际: MetricsEnabled = %v\n"+
			"  原因: config.go:258-299 mergeInto 无 metrics_enabled 处理分支",
			cfg.MetricsEnabled)
	}

	if cfg.MetricsAddr != ":8080" {
		t.Errorf("BUG [FACT-CONFIG-002]: mergeInto 不合并 metrics_addr\n"+
			"  期望: MetricsAddr = :8080\n"+
			"  实际: MetricsAddr = %s\n"+
			"  原因: config.go:258-299 mergeInto 无 metrics_addr 处理分支",
			cfg.MetricsAddr)
	}
}

// TestBug_ConfigNoLockfile 对应 FACT-CONFIG-003
// 配置写入必须用 lockfile 防止并发写入冲突
// 当前实现：Save 无 lockfile 机制，多进程并发写入会导致配置损坏
func TestBug_ConfigNoLockfile(t *testing.T) {
	// FACT-CONFIG-003
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// 创建一个假的 lockfile，看 Save 是否尊重它
	// 如果 Save 使用 lockfile，它应该等待或检测到已有 lock
	lockPath := filepath.Join(settingsDir, "settings.json.lock")
	if err := os.WriteFile(lockPath, []byte("fake-lock-pid-12345"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	loader := NewLoader(dir, "")
	cfg := DefaultConfig()
	cfg.Model = "test-model"

	// 如果 Save 使用 lockfile，它应等待或失败
	// 如果 Save 不使用 lockfile，它会直接写入
	done := make(chan error, 1)
	go func() {
		done <- loader.Save(&cfg)
	}()

	select {
	case err := <-done:
		// Save 完成，说明没有尊重 lockfile
		if err != nil {
			t.Fatalf("Save failed: %v", err)
		}
		t.Errorf("BUG [FACT-CONFIG-003]: Save 没有使用 lockfile\n" +
			"  期望: Save 应获取 lockfile，检测到已有 lock 时等待或失败\n" +
			"  实际: Save 忽略已有 lockfile，直接写入\n" +
			"  原因: config.go:341-366 Save 无 lockfile 获取/释放逻辑\n" +
			"  证据: 外部参考实现 saveConfigWithLock\n" +
			"  影响: 多进程并发写入导致配置损坏")
	case <-time.After(2 * time.Second):
		// 超时只说明 Save 仍在等待锁；继续等待它收尾，避免测试退出时
		// goroutine 仍占用 TempDir，导致 t.TempDir 清理失败。
		err := <-done
		if err == nil {
			t.Errorf("BUG [FACT-CONFIG-003]: Save 等待锁期间最终成功，未正确尊重 lockfile")
		} else if !strings.Contains(err.Error(), "acquire lock") {
			t.Errorf("Save 返回了非预期错误: %v", err)
		}
		t.Logf("Save 等待锁后按预期失败: %v", err)
	}
}

// TestBug_ConfigBackupRemovedOnSuccess 对应 FACT-CONFIG-004
// 配置写入必须原子化 + 备份，备份应保留最近 5 个
// 当前实现：备份在成功写入后被删除，不保留历史
// 3-C-4 修复后：备份改为时间戳命名（settings.json.<timestamp>.bak），检查任意时间戳备份存在
func TestBug_ConfigBackupRemovedOnSuccess(t *testing.T) {
	// FACT-CONFIG-004
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 第一次写入：创建初始配置
	if err := os.WriteFile(settingsPath, []byte(`{"model":"v1"}`), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 第二次写入：Save 覆盖
	loader := NewLoader(dir, "")
	cfg := DefaultConfig()
	cfg.Model = "v2"
	if err := loader.Save(&cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 检查备份是否被保留（时间戳命名备份 settings.json.<timestamp>.bak）
	// 期望：备份应保留（时间戳备份，保留最近 5 个）
	// 实际（修复前）：备份在成功写入后被删除
	entries, _ := os.ReadDir(settingsDir)
	backupCount := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "settings.json.") && strings.HasSuffix(name, ".bak") {
			backupCount++
		}
	}
	if backupCount == 0 {
		t.Errorf("BUG [FACT-CONFIG-004]: 备份在成功写入后被删除\n" +
			"  期望: 备份应保留（时间戳备份，保留最近 5 个）\n" +
			"  实际: 无 settings.json.*.bak 备份文件（已被删除）\n" +
			"  原因: config.go Save 成功后 os.Remove(backupPath) 删除备份\n" +
			"  证据: 外部参考实现 保留最近 5 个备份\n" +
			"  影响: 崩溃后无法回滚到上一个有效配置")
	}
}

// TestBug_ValidateErrorIgnored 对应 FACT-CONFIG-005
// Validate 错误不能被忽略
// 当前实现：Load 中 Validate 错误被 _ = err 显式忽略
func TestBug_ValidateErrorIgnored(t *testing.T) {
	// FACT-CONFIG-005
	projectDir := t.TempDir()
	whaleDir := filepath.Join(projectDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// 写入包含多个无效配置的文件
	settings := map[string]interface{}{
		"permission_mode":     "totally-invalid-mode",
		"context_window":      100, // 小窗口合法（不再回填），此测试断言的是 permission_mode 回填行为
		"compaction_strategy": "nonexistent-strategy",
		"output_style":        "invalid-style",
		"session_budget":      -999.0,
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 清理环境变量
	origModel := os.Getenv("WHALE_MODEL")
	os.Unsetenv("WHALE_MODEL")
	defer func() {
		if origModel != "" {
			os.Setenv("WHALE_MODEL", origModel)
		}
	}()

	loader := NewLoader("", projectDir)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 期望：Load 应返回验证错误或至少记录日志
	// 实际：Load 返回 nil error，验证错误被 _ = err 忽略
	// 验证：配置被静默修正（无效值被替换为默认值），但用户不知道
	if cfg.PermissionMode == PermWorkspaceWrite {
		t.Errorf("BUG [FACT-CONFIG-005]: Validate 错误被忽略\n" +
			"  输入: permission_mode=totally-invalid-mode, context_window=100\n" +
			"  期望: Load 应返回验证错误或记录警告日志\n" +
			"  实际: Load 返回 nil error，无效配置被静默修正为默认值\n" +
			"  原因: config.go:178-183 _ = err // validation errors are non-fatal\n" +
			"  证据: 通用原则 - Validate 错误不能被忽略\n" +
			"  影响: 用户不知道配置有误，难以排查问题")
	}
}

// TestBug_ConfigBackupNotTimestamped 对应 FACT-SESS-005
// 全局配置写入前必须备份，备份应时间戳命名并保留最近 5 个
// 当前实现：备份名为 settings.json.bak（非时间戳），且成功后删除
func TestBug_ConfigBackupNotTimestamped(t *testing.T) {
	// FACT-SESS-005
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 初始配置
	if err := os.WriteFile(settingsPath, []byte(`{"model":"v1"}`), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	loader := NewLoader(dir, "")

	// 连续 Save 3 次，检查是否有时间戳备份
	for i := 0; i < 3; i++ {
		cfg := DefaultConfig()
		cfg.Model = fmt.Sprintf("v%d", i+2)
		if err := loader.Save(&cfg); err != nil {
			t.Fatalf("Save %d failed: %v", i+1, err)
		}
		time.Sleep(10 * time.Millisecond) // 确保时间戳不同
	}

	// 检查是否有时间戳备份文件
	// 期望：应有多个时间戳备份（如 settings.json.20240101.120000.bak）
	// 实际：只有 settings.json.bak（且已被删除）
	entries, _ := os.ReadDir(settingsDir)
	timestampedBackups := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "settings.json") && strings.HasSuffix(name, ".bak") {
			// 时间戳备份格式应包含日期数字（如 .20240101. 或 .2024-）
			if strings.Contains(name, ".20") || strings.Contains(name, ".19") {
				timestampedBackups++
			}
		}
	}

	if timestampedBackups == 0 {
		t.Errorf("BUG [FACT-SESS-005]: 备份不是时间戳命名，且未保留历史\n"+
			"  期望: 多次 Save 后应有多个时间戳备份（保留最近 5 个）\n"+
			"  实际: 无时间戳备份文件（timestampedBackups=%d）\n"+
			"  原因: config.go:356-364 只创建 settings.json.bak 且成功后删除\n"+
			"  证据: 外部参考实现 创建时间戳备份保留最近 5 个\n"+
			"  影响: 无法回滚到历史配置版本",
			timestampedBackups)
	}
}

// TestBug_MemorySelectorSessionUsedRace 对应 FACT-CONC-001
// 共享状态修改必须原子化
// 当前实现：MemorySelector.sessionUsed 改为 atomic.Int64，并发调用安全
func TestBug_MemorySelectorSessionUsedRace(t *testing.T) {
	// FACT-CONC-001
	ms := NewMemorySelector(nil, nil, nil)

	// 并发修改 sessionUsed（模拟并发 SelectRelevant 调用）
	// 修复后 sessionUsed 为 atomic.Int64，用 Add 原子累加
	var wg sync.WaitGroup
	n := 1000
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ms.sessionUsed.Add(1) // 原子并发修改
		}()
	}
	wg.Wait()

	expected := int64(n)
	if got := ms.sessionUsed.Load(); got != expected {
		t.Errorf("BUG [FACT-CONC-001]: MemorySelector.sessionUsed 并发修改数据竞争\n"+
			"  期望: %d\n"+
			"  实际: %d (丢失更新)\n"+
			"  原因: memory_v2.go sessionUsed 未用 atomic 保护\n"+
			"  正确做法: 用 sync/atomic.Int64 保护 sessionUsed 修改\n"+
			"  验证: go test -race ./internal/config/ -run TestBug_MemorySelectorSessionUsedRace\n"+
			"  证据: 通用并发原则 - 共享状态修改必须原子化",
			expected, got)
	}
}
