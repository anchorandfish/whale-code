package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Model != "" {
		t.Errorf("Model = %s, want empty (must be set by user)", cfg.Model)
	}
	if cfg.PermissionMode != PermWorkspaceWrite {
		t.Errorf("PermissionMode = %s, want workspace-write", cfg.PermissionMode)
	}
	// DefaultConfig 设 ContextWindow=131072（config.go:116），非 0
	if cfg.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %d, want 131072", cfg.ContextWindow)
	}
}

func TestDefaultConfigPtr(t *testing.T) {
	cfg := DefaultConfigPtr()
	if cfg == nil {
		t.Fatal("DefaultConfigPtr returned nil")
	}
	if cfg.Model != "" {
		t.Errorf("Model = %s, want empty (must be set by user)", cfg.Model)
	}
}

func TestLoaderLoad(t *testing.T) {
	// 清理environment variable，防止 .env 文件中的 WHALE_MODEL 影响测试
	origModel := os.Getenv("WHALE_MODEL")
	os.Unsetenv("WHALE_MODEL")
	defer func() {
		if origModel != "" {
			os.Setenv("WHALE_MODEL", origModel)
		}
	}()

	// 收口：discoverSources 里 user 级 settings.json 取自 whalehome.Dir()（$WHALE_HOME），
	// 不再取 l.userDir ⇒ 不注入时本用例会读到宿主**真实** ~/.whale/settings.json，
	// 于是「无配置 ⇒ model 为空」的断言变成「本机装了什么模型」（环境依赖的假红/假绿）。
	t.Setenv("WHALE_HOME", filepath.Join(t.TempDir(), ".whale"))
	loader := NewLoader("/nonexistent/home", "/nonexistent/cwd")
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Model != "" {
		t.Errorf("Default Model = %s, want empty (must be set by user)", cfg.Model)
	}
}

func TestMergeInto(t *testing.T) {
	cfg := DefaultConfig()
	raw := map[string]interface{}{
		"model":          "gpt-4o",
		"context_window": float64(128000),
	}
	mergeInto(&cfg, raw)

	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %s, want gpt-4o", cfg.Model)
	}
	if cfg.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", cfg.ContextWindow)
	}
}

func TestGetString(t *testing.T) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
	}

	val, ok := getString(m, "key1")
	if !ok || val != "value1" {
		t.Errorf("getString(key1) = %s, %v", val, ok)
	}

	_, ok = getString(m, "key2")
	if ok {
		t.Error("getString(key2) should return false for non-string")
	}
}

func TestGetFloat(t *testing.T) {
	m := map[string]interface{}{
		"num": float64(3.14),
	}

	val, ok := getFloat(m, "num")
	if !ok || val != 3.14 {
		t.Errorf("getFloat(num) = %f, %v", val, ok)
	}
}

func TestGetBool(t *testing.T) {
	m := map[string]interface{}{
		"flag": true,
	}

	val, ok := getBool(m, "flag")
	if !ok || !val {
		t.Errorf("getBool(flag) = %v, %v", val, ok)
	}
}

// TestLoad_MultipleSources 测试多源加载优先级（env > .whale/config.json > default）
// 验证项目级config file能覆盖default value，且environment variable优先级最高
func TestLoad_MultipleSources(t *testing.T) {
	// 清理environment variable，防止 .env 文件中的 WHALE_MODEL 影响测试
	origModel := os.Getenv("WHALE_MODEL")
	os.Unsetenv("WHALE_MODEL")
	defer func() {
		if origModel != "" {
			os.Setenv("WHALE_MODEL", origModel)
		}
	}()

	projectDir := t.TempDir()

	// 创建 .whale/settings.json config file
	whaleDir := filepath.Join(projectDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败: %v", err)
	}

	settings := map[string]interface{}{
		"model":          "config-model",
		"context_window": 128000,
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("serialize config失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("写入 settings.json 失败: %v", err)
	}

	loader := NewLoader("", projectDir)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}

	// 验证config file值覆盖了default value
	if cfg.Model != "config-model" {
		t.Errorf("Model = %q, expected %q（来自config file）", cfg.Model, "config-model")
	}
	if cfg.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, expected 128000（来自config file）", cfg.ContextWindow)
	}
}

// TestLoad_EnvironmentOverrides 测试三模型字段的 env fallback 语义（P6 候选 B）。
//
// 新语义：WHALE_MODEL 仅当 settings.json 的 model 字段为空时才作为 fallback 生效；
// settings.json 显式配置优先于环境变量（反转旧的"env 总是覆盖"行为）。
func TestLoad_EnvironmentOverrides(t *testing.T) {
	// 子用例 1：settings.json model 非空时，env 不覆盖（settings.json 优先）
	t.Run("settings_nonempty_env_ignored", func(t *testing.T) {
		projectDir := t.TempDir()

		// 创建config file
		whaleDir := filepath.Join(projectDir, ".whale")
		if err := os.MkdirAll(whaleDir, 0755); err != nil {
			t.Fatalf("创建 .whale 目录失败: %v", err)
		}

		settings := map[string]interface{}{
			"model": "file-model",
		}
		data, err := json.Marshal(settings)
		if err != nil {
			t.Fatalf("serialize config失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
			t.Fatalf("写入 settings.json 失败: %v", err)
		}

		// 设置environment variable
		os.Setenv("WHALE_MODEL", "env-model")
		defer os.Unsetenv("WHALE_MODEL")

		loader := NewLoader("", projectDir)
		cfg, err := loader.Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}

		// settings.json 非空时 env 不覆盖（P6 候选 B 新语义）
		if cfg.Model != "file-model" {
			t.Errorf("Model = %q, expected %q（settings.json 优先于 env）", cfg.Model, "file-model")
		}
	})

	// 子用例 2：settings.json model 为空时，env 作为 fallback 生效
	t.Run("settings_empty_env_fallback", func(t *testing.T) {
		projectDir := t.TempDir()

		whaleDir := filepath.Join(projectDir, ".whale")
		if err := os.MkdirAll(whaleDir, 0755); err != nil {
			t.Fatalf("创建 .whale 目录失败: %v", err)
		}

		// settings.json 不设置 model 字段（mergeInto 后 cfg.Model 为空）
		settings := map[string]interface{}{}
		data, err := json.Marshal(settings)
		if err != nil {
			t.Fatalf("serialize config失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
			t.Fatalf("写入 settings.json 失败: %v", err)
		}

		os.Setenv("WHALE_MODEL", "env-model")
		defer os.Unsetenv("WHALE_MODEL")

		loader := NewLoader("", projectDir)
		cfg, err := loader.Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}

		// settings.json 为空时 env 作为 fallback
		if cfg.Model != "env-model" {
			t.Errorf("Model = %q, expected %q（env 作为 fallback）", cfg.Model, "env-model")
		}
	})
}

// TestMergeInto_CamelAndSnakeCase 测试 context_window 蛇形键名映射到 Config.ContextWindow
// 验证 mergeInto 函数支持下划线式键名合并（与 JSON tag 蛇形契约一致）
func TestMergeInto_CamelAndSnakeCase(t *testing.T) {
	// 测试下划线式键名（与 JSON tag 一致，mergeInto 只识别蛇形）
	t.Run("snake_case", func(t *testing.T) {
		cfg := DefaultConfig()
		raw := map[string]interface{}{
			"context_window": float64(96000),
		}
		mergeInto(&cfg, raw)
		if cfg.ContextWindow != 96000 {
			t.Errorf("context_window: ContextWindow = %d, expected 96000", cfg.ContextWindow)
		}
	})
}

// TestConfigValidation 测试无效 PermissionMode 使用default value；负数 ContextWindow 被修正为default value
// validate config加载时的容错处理逻辑
func TestConfigValidation(t *testing.T) {
	t.Run("无效 PermissionMode 使用default value", func(t *testing.T) {
		cfg := DefaultConfig()
		raw := map[string]interface{}{
			"permission_mode": "invalid-mode",
		}
		mergeInto(&cfg, raw)
		// 无效模式会被设置为字符串值，但不会崩溃
		if cfg.PermissionMode != PermissionMode("invalid-mode") {
			t.Errorf("PermissionMode = %q, expected %q", cfg.PermissionMode, "invalid-mode")
		}
	})

	t.Run("负数 ContextWindow 保持为 0（由模型decided）", func(t *testing.T) {
		projectDir := t.TempDir()

		whaleDir := filepath.Join(projectDir, ".whale")
		if err := os.MkdirAll(whaleDir, 0755); err != nil {
			t.Fatalf("创建 .whale 目录失败: %v", err)
		}

		settings := map[string]interface{}{
			"context_window": -1,
		}
		data, err := json.Marshal(settings)
		if err != nil {
			t.Fatalf("serialize config失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
			t.Fatalf("写入 settings.json 失败: %v", err)
		}

		loader := NewLoader("", projectDir)
		cfg, err := loader.Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}

		// 负数 ContextWindow 应保持为 0（由模型decided）
		if cfg.ContextWindow != 0 {
			t.Errorf("ContextWindow = %d, expected 0（由模型decided）", cfg.ContextWindow)
		}
	})
}

// TestValidate_SmallContextWindowPreserved 验证小窗口值（< 4096）不再被回填为 4096。
// 背景：压缩阈值测试需要小 context_window（如 800）快速触发 Warning/ShouldCompact/Blocked。
// ContextWindow > 0 表示用户显式设置，应尊重原值；0 = 由模型决定；负数由 validate() 拦截。
// 压缩模块安全性：CompactThreshold 在 window-buffer ≤ 0 时回退 window×85%（下限 1），无除零风险。
func TestValidate_SmallContextWindowPreserved(t *testing.T) {
	cases := []int{1, 100, 800, 4095}
	for _, w := range cases {
		cfg := DefaultConfig()
		cfg.ContextWindow = w
		errs := cfg.Validate()
		if cfg.ContextWindow != w {
			t.Errorf("ContextWindow = %d, want %d（小窗口应原样保留，不应回填 4096）", cfg.ContextWindow, w)
		}
		for _, err := range errs {
			if strings.Contains(err.Error(), "context_window") {
				t.Errorf("window=%d 时不应产生 context_window 相关错误，got: %v", w, err)
			}
		}
	}
}

// TestLoad_EnvSmallContextWindow 验证 WHALE_CONTEXT_WINDOW=800 经 Load 后保持 800（用户实测场景）。
func TestLoad_EnvSmallContextWindow(t *testing.T) {
	orig := os.Getenv("WHALE_CONTEXT_WINDOW")
	os.Setenv("WHALE_CONTEXT_WINDOW", "800")
	defer func() {
		if orig == "" {
			os.Unsetenv("WHALE_CONTEXT_WINDOW")
		} else {
			os.Setenv("WHALE_CONTEXT_WINDOW", orig)
		}
	}()

	loader := NewLoader("/nonexistent/home", "/nonexistent/cwd")
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ContextWindow != 800 {
		t.Errorf("ContextWindow = %d, want 800（环境变量小窗口应生效，不被回填 4096）", cfg.ContextWindow)
	}
}

// TestApprovalDefaultPolicy 测试 DefaultConfig 返回的审批策略默认值为 on-request（B0 §5 最安全策略）。
func TestApprovalDefaultPolicy(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Approval.Policy != "on-request" {
		t.Errorf("Approval.Policy = %q, want %q（B0 §5 默认 PolicyOnRequest）", cfg.Approval.Policy, "on-request")
	}
}

// TestApprovalLoadValidPolicy 测试从 JSON 配置文件加载合法审批策略能正确读入 Config.Approval.Policy。
func TestApprovalLoadValidPolicy(t *testing.T) {
	cases := []string{"untrusted", "on-failure", "on-request", "never"}
	for _, policy := range cases {
		t.Run(policy, func(t *testing.T) {
			projectDir := t.TempDir()
			whaleDir := filepath.Join(projectDir, ".whale")
			if err := os.MkdirAll(whaleDir, 0755); err != nil {
				t.Fatalf("创建 .whale 目录失败: %v", err)
			}
			settings := map[string]interface{}{
				"approval": map[string]interface{}{
					"policy": policy,
				},
			}
			data, err := json.Marshal(settings)
			if err != nil {
				t.Fatalf("serialize config 失败: %v", err)
			}
			if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
				t.Fatalf("写入 settings.json 失败: %v", err)
			}

			loader := NewLoader("", projectDir)
			cfg, err := loader.Load()
			if err != nil {
				t.Fatalf("Load 失败: %v", err)
			}
			if cfg.Approval.Policy != policy {
				t.Errorf("Approval.Policy = %q, want %q", cfg.Approval.Policy, policy)
			}
		})
	}
}

// TestApprovalInvalidPolicyFallback 测试 JSON 中写入非法 policy 时 Validate 回填默认 on-request。
func TestApprovalInvalidPolicyFallback(t *testing.T) {
	projectDir := t.TempDir()
	whaleDir := filepath.Join(projectDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败: %v", err)
	}
	settings := map[string]interface{}{
		"approval": map[string]interface{}{
			"policy": "invalid",
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("serialize config 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("写入 settings.json 失败: %v", err)
	}

	loader := NewLoader("", projectDir)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	// 非法 policy 应被 Validate 回填为 on-request
	if cfg.Approval.Policy != "on-request" {
		t.Errorf("Approval.Policy = %q, want %q（非法值应回填默认）", cfg.Approval.Policy, "on-request")
	}
}

// TestApprovalMergeInto 测试 approval 字段能从 raw map 合并到 Config.Approval.Policy。
func TestApprovalMergeInto(t *testing.T) {
	cfg := DefaultConfig()
	raw := map[string]interface{}{
		"approval": map[string]interface{}{
			"policy": "never",
		},
	}
	mergeInto(&cfg, raw)
	if cfg.Approval.Policy != "never" {
		t.Errorf("Approval.Policy = %q, want %q", cfg.Approval.Policy, "never")
	}

	// approval 键缺失时不应改动已有值
	cfg2 := DefaultConfig()
	mergeInto(&cfg2, map[string]interface{}{"model": "gpt-4o"})
	if cfg2.Approval.Policy != "on-request" {
		t.Errorf("Approval.Policy = %q, want %q（缺失 approval 键应保持默认）", cfg2.Approval.Policy, "on-request")
	}

	// policy 子键缺失时不应改动已有值
	cfg3 := DefaultConfig()
	mergeInto(&cfg3, map[string]interface{}{
		"approval": map[string]interface{}{},
	})
	if cfg3.Approval.Policy != "on-request" {
		t.Errorf("Approval.Policy = %q, want %q（缺失 policy 子键应保持默认）", cfg3.Approval.Policy, "on-request")
	}
}

// === 3-C-3 Validate 错误记录测试 ===

// TestLoad_ValidateErrorLogged 验证 Validate 错误通过 log 记录（对应 TestBug_ValidateErrorIgnored 修复）
// 无效 permission_mode 应回填到 PermReadOnly（fail-closed），而非 PermWorkspaceWrite。
// Load 不应因 Validate 错误失败（保留向后兼容，仅 log.Printf 警告）。
func TestLoad_ValidateErrorLogged(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	os.MkdirAll(settingsDir, 0700)
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 写一个含非法 permission_mode 的 config
	invalidCfg := map[string]interface{}{
		"permission_mode": "invalid-mode",
		"config_version":  1,
	}
	data, _ := json.MarshalIndent(invalidCfg, "", "  ")
	os.WriteFile(settingsPath, data, 0600)

	// 清理环境变量
	origModel := os.Getenv("WHALE_MODEL")
	os.Unsetenv("WHALE_MODEL")
	defer func() {
		if origModel != "" {
			os.Setenv("WHALE_MODEL", origModel)
		}
	}()

	loader := NewLoader(dir, "")
	cfg, err := loader.Load()

	// Load 不应因 Validate 错误失败（保留向后兼容，仅 log.Printf）
	if err != nil {
		t.Fatalf("Load 不应因 Validate 错误失败: %v", err)
	}

	// Validate 应自动回填默认值（副作用保留），无效 permission_mode → PermReadOnly（fail-closed）
	if cfg.PermissionMode != PermReadOnly {
		t.Errorf("Validate 未回填 PermReadOnly，PermissionMode = %q（无效输入应 fail-closed）", cfg.PermissionMode)
	}
}

// === custom_pricing 已废弃（D 批 2026-10-05：成本线删除）===

// TestCustomPricing_RemovedFromConfig 结构判据：settings.json 里残留的 custom_pricing
// 不再有对应字段，解析时被静默忽略。
//
// ★ 为什么必须有这条：旧 settings.json 与用户手册里都出现过 custom_pricing。
// 删掉字段后必须证明"老配置写了也不报错、也不生效"，否则升级后老配置会变成加载失败。
func TestCustomPricing_RemovedFromConfig(t *testing.T) {
	oldJSON := `{
        "custom_pricing": [
            {"model": "legacy-model", "input_per_million": 1.0, "output_per_million": 2.0}
        ]
    }`

	var cfg Config
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatalf("含已废弃 custom_pricing 的配置应仍能解析（未知键应被忽略）: %v", err)
	}
}

// === H8 配置化脱钩测试（/109/110） ===

// TestH8_DefaultConfig_HardcodedValues 验证 13 个 H8 新增字段的默认值与原硬编码值一致（零行为变化，）
func TestH8_DefaultConfig_HardcodedValues(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"StreamTimeoutSeconds", cfg.StreamTimeoutSeconds, 300},
		{"CIMaxIterations", cfg.CIMaxIterations, 50},
		{"CIMaxConsecutiveFailures", cfg.CIMaxConsecutiveFailures, 3},
		{"CIPermission", cfg.CIPermission, string(PermDangerFullAccess)},
		{"CITimeoutMinutes", cfg.CITimeoutMinutes, 10},
		{"MaxSnap", cfg.MaxSnap, 50},
		{"MaxSearchMatchesPerSession", cfg.MaxSearchMatchesPerSession, 5},
		{"BudgetWarnPercent", cfg.BudgetWarnPercent, 0.75},
		{"BudgetCompactPercent", cfg.BudgetCompactPercent, 0.80},
		{"BudgetBlockPercent", cfg.BudgetBlockPercent, 0.97},
		{"AutoCompact", cfg.AutoCompact, true},
		{"AutoCompactBufferTokens", cfg.AutoCompactBufferTokens, 13000},
		{"InputHistoryLimit", cfg.InputHistoryLimit, 500},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v（原硬编码值）", tc.name, tc.got, tc.want)
		}
	}
	// AvailableModels 默认 nil（各模块检测到 nil 时用内置默认列表）
	if cfg.AvailableModels != nil {
		t.Errorf("AvailableModels = %v, want nil（默认留空）", cfg.AvailableModels)
	}
}

// TestH8_EnvOverride_AllFields 验证 12 个 H8 新增 env 变量能覆盖默认值（统一 WHALE_ 前缀）
func TestH8_EnvOverride_AllFields(t *testing.T) {
	// 设置全部 12 个 env 变量
	envs := map[string]string{
		"WHALE_STREAM_TIMEOUT":         "600",
		"WHALE_CI_MAX_ITERATIONS":      "100",
		"WHALE_CI_MAX_FAILURES":        "5",
		"WHALE_MAX_TURNS_PER_REQUEST":  "25",
		"WHALE_CI_PERMISSION":          string(PermReadOnly),
		"WHALE_CI_TIMEOUT":             "20",
		"WHALE_MAX_SNAP":               "100",
		"WHALE_MAX_SEARCH_MATCHES":     "10",
		"WHALE_BUDGET_WARN_PERCENT":    "0.8",
		"WHALE_BUDGET_COMPACT_PERCENT": "0.9",
		"WHALE_BUDGET_BLOCK_PERCENT":   "0.95",
		"WHALE_AUTOCOMPACT_BUFFER":     "20000",
		"WHALE_INPUT_HISTORY_LIMIT":    "1000",
	}
	for k, v := range envs {
		os.Setenv(k, v)
	}
	defer func() {
		for k := range envs {
			os.Unsetenv(k)
		}
	}()

	cfg := DefaultConfig()
	// applyEnvironmentOverrides 是 *Loader 的方法（config.go:361），需通过 Loader 调用
	loader := NewLoader("", "")
	loader.applyEnvironmentOverrides(&cfg)

	cases := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"StreamTimeoutSeconds", cfg.StreamTimeoutSeconds, 600},
		{"CIMaxIterations", cfg.CIMaxIterations, 100},
		{"CIMaxConsecutiveFailures", cfg.CIMaxConsecutiveFailures, 5},
		{"MaxTurnsPerRequest", cfg.MaxTurnsPerRequest, 25},
		{"CIPermission", cfg.CIPermission, string(PermReadOnly)},
		{"CITimeoutMinutes", cfg.CITimeoutMinutes, 20},
		{"MaxSnap", cfg.MaxSnap, 100},
		{"MaxSearchMatchesPerSession", cfg.MaxSearchMatchesPerSession, 10},
		{"BudgetWarnPercent", cfg.BudgetWarnPercent, 0.8},
		{"BudgetCompactPercent", cfg.BudgetCompactPercent, 0.9},
		{"BudgetBlockPercent", cfg.BudgetBlockPercent, 0.95},
		{"AutoCompactBufferTokens", cfg.AutoCompactBufferTokens, 20000},
		{"InputHistoryLimit", cfg.InputHistoryLimit, 1000},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v（env 覆盖值）", tc.name, tc.got, tc.want)
		}
	}
}

// TestH8_Validate_FallbackToDefault 验证 Validate 对非法值回填默认值（12 字段，）
func TestH8_Validate_FallbackToDefault(t *testing.T) {
	cfg := DefaultConfig()
	// 设置全部非法值
	cfg.StreamTimeoutSeconds = 10      // < 30
	cfg.CIMaxIterations = 0            // < 1
	cfg.CIMaxConsecutiveFailures = 0   // < 1
	cfg.CIPermission = "invalid-perm"  // 非合法 PermissionMode
	cfg.CITimeoutMinutes = 0           // < 1
	cfg.MaxSnap = 0                    // < 1
	cfg.MaxSearchMatchesPerSession = 0 // < 1
	cfg.BudgetWarnPercent = 1.5        // > 1
	cfg.BudgetCompactPercent = -0.1    // < 0
	cfg.BudgetBlockPercent = 2.0       // > 1
	cfg.AutoCompactBufferTokens = 500  // < 1000
	cfg.InputHistoryLimit = 5          // < 10

	errs := cfg.Validate()
	if len(errs) != 12 {
		t.Errorf("Validate 返回 %d 个错误, want 12", len(errs))
	}

	// 验证回填后的值
	cases := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"StreamTimeoutSeconds", cfg.StreamTimeoutSeconds, 30},
		{"CIMaxIterations", cfg.CIMaxIterations, 50},
		{"CIMaxConsecutiveFailures", cfg.CIMaxConsecutiveFailures, 3},
		{"CIPermission", cfg.CIPermission, string(PermDangerFullAccess)},
		{"CITimeoutMinutes", cfg.CITimeoutMinutes, 10},
		{"MaxSnap", cfg.MaxSnap, 50},
		{"MaxSearchMatchesPerSession", cfg.MaxSearchMatchesPerSession, 5},
		{"BudgetWarnPercent", cfg.BudgetWarnPercent, 0.75},
		{"BudgetCompactPercent", cfg.BudgetCompactPercent, 0.80},
		{"BudgetBlockPercent", cfg.BudgetBlockPercent, 0.97},
		{"AutoCompactBufferTokens", cfg.AutoCompactBufferTokens, 13000},
		{"InputHistoryLimit", cfg.InputHistoryLimit, 500},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v（回填默认值）", tc.name, tc.got, tc.want)
		}
	}
}

// TestH8_BackwardCompat_OldSettingsJSON 验证旧 settings.json（无 H8 新字段）能正常加载并使用默认值
func TestH8_BackwardCompat_OldSettingsJSON(t *testing.T) {
	projectDir := t.TempDir()
	whaleDir := filepath.Join(projectDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败: %v", err)
	}

	// 旧格式 settings.json：仅含 model + context_window + permission_mode，无任何 H8 新字段
	oldSettings := map[string]interface{}{
		"model":           "gpt-4o",
		"context_window":  128000,
		"permission_mode": string(PermWorkspaceWrite),
	}
	data, err := json.Marshal(oldSettings)
	if err != nil {
		t.Fatalf("serialize config 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("写入 settings.json 失败: %v", err)
	}

	// 清理可能影响测试的 H8 env 变量
	h8Envs := []string{
		"WHALE_STREAM_TIMEOUT", "WHALE_CI_MAX_ITERATIONS", "WHALE_CI_MAX_FAILURES",
		"WHALE_CI_PERMISSION", "WHALE_CI_TIMEOUT", "WHALE_MAX_SNAP",
		"WHALE_MAX_SEARCH_MATCHES", "WHALE_BUDGET_WARN_PERCENT",
		"WHALE_BUDGET_COMPACT_PERCENT", "WHALE_BUDGET_BLOCK_PERCENT",
		"WHALE_AUTOCOMPACT_BUFFER", "WHALE_INPUT_HISTORY_LIMIT",
	}
	for _, e := range h8Envs {
		os.Unsetenv(e)
	}

	loader := NewLoader("", projectDir)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load 旧格式 settings.json 失败: %v", err)
	}

	// 验证旧字段正确加载
	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", cfg.Model)
	}
	if cfg.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", cfg.ContextWindow)
	}
	// 验证 H8 新字段使用默认值（零迁移）
	if cfg.StreamTimeoutSeconds != 300 {
		t.Errorf("StreamTimeoutSeconds = %d, want 300（默认值）", cfg.StreamTimeoutSeconds)
	}
	if cfg.CIMaxIterations != 50 {
		t.Errorf("CIMaxIterations = %d, want 50（默认值）", cfg.CIMaxIterations)
	}
	if cfg.AutoCompactBufferTokens != 13000 {
		t.Errorf("AutoCompactBufferTokens = %d, want 13000（默认值）", cfg.AutoCompactBufferTokens)
	}
	if cfg.InputHistoryLimit != 500 {
		t.Errorf("InputHistoryLimit = %d, want 500（默认值）", cfg.InputHistoryLimit)
	}
}

// TestMigrateWithBackup_NotExists 验证源文件不存在时跳过备份。
// 期望：返回空路径 + nil 错误（不视为错误）。
func TestMigrateWithBackup_NotExists(t *testing.T) {
	backupPath, err := migrateWithBackup("/nonexistent/path/settings.json")
	if err != nil {
		t.Errorf("源文件不存在时应返回 nil 错误，实际 %v", err)
	}
	if backupPath != "" {
		t.Errorf("源文件不存在时应返回空路径，实际 %q", backupPath)
	}
}

// TestMigrateWithBackup_Success 验证源文件存在时成功创建备份。
// 期望：返回非空备份路径 + nil 错误，备份文件内容与原文件一致。
func TestMigrateWithBackup_Success(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "settings.json")
	srcContent := []byte(`{"model":"test","config_version":0}`)
	if err := os.WriteFile(srcPath, srcContent, 0644); err != nil {
		t.Fatalf("写入源文件失败: %v", err)
	}

	backupPath, err := migrateWithBackup(srcPath)
	if err != nil {
		t.Fatalf("migrateWithBackup 失败: %v", err)
	}
	if backupPath == "" {
		t.Fatalf("期望返回非空备份路径，实际空")
	}

	// 验证备份文件存在
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("读取备份文件失败: %v", err)
	}
	if string(backupData) != string(srcContent) {
		t.Errorf("备份文件内容应与原文件一致，实际 backup=%q src=%q", backupData, srcContent)
	}

	// 验证备份文件名格式：<src>.<timestamp>.bak（R6 统一：与 Save 的 timestamp.bak 格式对齐）
	if !strings.HasPrefix(backupPath, srcPath+".") || !strings.HasSuffix(backupPath, ".bak") {
		t.Errorf("备份文件名应以 %s.<timestamp>.bak 格式，实际 %q", srcPath, backupPath)
	}
}

// TestLoad_AutoBackupBeforeMigration 验证加载旧版本配置时自动创建备份。
// V3-1-3：config_version=0（旧版本）触发迁移前自动备份。
// 期望：Load 成功 + 备份文件存在 + ConfigVersion 升级到 CurrentConfigVersion。
func TestLoad_AutoBackupBeforeMigration(t *testing.T) {
	projectDir := t.TempDir()
	whaleDir := filepath.Join(projectDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatalf("创建 .whale 目录失败: %v", err)
	}

	// 旧版本 settings.json：config_version=0
	oldSettings := map[string]interface{}{
		"model":          "gpt-4o",
		"config_version": 0,
	}
	data, err := json.Marshal(oldSettings)
	if err != nil {
		t.Fatalf("serialize config 失败: %v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("写入 settings.json 失败: %v", err)
	}

	// 清理可能影响测试的环境变量
	os.Unsetenv("WHALE_MODEL")

	loader := NewLoader("", projectDir)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}

	// 验证 ConfigVersion 已升级
	if cfg.ConfigVersion != CurrentConfigVersion {
		t.Errorf("ConfigVersion = %d, want %d", cfg.ConfigVersion, CurrentConfigVersion)
	}

	// 验证备份文件已创建（格式：settings.json.<timestamp>.bak，R6 统一命名）
	entries, err := os.ReadDir(whaleDir)
	if err != nil {
		t.Fatalf("读取 .whale 目录失败: %v", err)
	}
	foundBackup := false
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "settings.json.") && strings.HasSuffix(name, ".bak") {
			foundBackup = true
			// 验证备份文件内容是旧版本（config_version=0）
			backupData, readErr := os.ReadFile(filepath.Join(whaleDir, name))
			if readErr != nil {
				t.Errorf("读取备份文件 %s 失败: %v", name, readErr)
			}
			if !strings.Contains(string(backupData), `"config_version":0`) {
				t.Errorf("备份文件应含旧版本 config_version=0，实际 %s", backupData)
			}
			break
		}
	}
	if !foundBackup {
		t.Errorf("期望存在 settings.json.<timestamp>.bak 备份文件，实际未找到")
	}
}
