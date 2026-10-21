package ai

import (
	"os"
	"path/filepath"
	"testing"
)

// --- ModelRegistry 测试 ---

// TestRegistryAdd 测试添加模型
func TestRegistryAdd(t *testing.T) {
	r := NewModelRegistry()
	cfg := &ModelConfig{Name: "test-model", Provider: ProviderOpenAI, BaseURL: "https://api.test.com", APIKey: "sk-test"}
	r.Add(cfg)

	got := r.Get("test-model")
	if got == nil {
		t.Fatal("expected model to be added, got nil")
	}
	if got.Name != "test-model" {
		t.Errorf("expected name test-model, got %s", got.Name)
	}
	if got.BaseURL != "https://api.test.com" {
		t.Errorf("expected baseURL https://api.test.com, got %s", got.BaseURL)
	}
}

// TestRegistryAddOverwrite 测试重复添加同名模型（覆盖）
func TestRegistryAddOverwrite(t *testing.T) {
	r := NewModelRegistry()
	r.Add(&ModelConfig{Name: "test", Provider: ProviderOpenAI, BaseURL: "https://old.com"})
	r.Add(&ModelConfig{Name: "test", Provider: ProviderOpenAI, BaseURL: "https://new.com"})

	got := r.Get("test")
	if got.BaseURL != "https://new.com" {
		t.Errorf("expected baseURL https://new.com (overwrite), got %s", got.BaseURL)
	}
}

// TestRegistryRemove 测试移除模型
func TestRegistryRemove(t *testing.T) {
	r := NewModelRegistry()
	r.Add(&ModelConfig{Name: "test-model"})
	ok := r.Remove("test-model")
	if !ok {
		t.Fatal("expected remove to succeed")
	}
	if r.Get("test-model") != nil {
		t.Fatal("expected model to be removed")
	}
}

// TestRegistryRemoveNotFound 测试移除不存在的模型
func TestRegistryRemoveNotFound(t *testing.T) {
	r := NewModelRegistry()
	ok := r.Remove("not-exist")
	if ok {
		t.Fatal("expected remove to fail for non-existent model")
	}
}

// TestRegistryList 测试列出所有模型（排序）
func TestRegistryList(t *testing.T) {
	r := NewModelRegistry()
	r.Add(&ModelConfig{Name: "zeta"})
	r.Add(&ModelConfig{Name: "alpha"})
	r.Add(&ModelConfig{Name: "middle"})

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 models, got %d", len(list))
	}
	if list[0].Name != "alpha" || list[1].Name != "middle" || list[2].Name != "zeta" {
		t.Errorf("expected sorted order [alpha, middle, zeta], got [%s, %s, %s]", list[0].Name, list[1].Name, list[2].Name)
	}
}

// TestRegistryListEmpty 测试空注册表
func TestRegistryListEmpty(t *testing.T) {
	r := NewModelRegistry()
	list := r.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}
}

// --- FillDefaults 测试 ---

// TestFillDefaultsContextWindow 测试从内置规格表 padding context_window
func TestFillDefaultsContextWindow(t *testing.T) {
	cfg := &ModelConfig{Name: "qwen3.5-flash", ContextWindow: 0}
	cfg.FillDefaults()
	if cfg.ContextWindow != 1048576 {
		t.Errorf("expected context_window 1048576, got %d", cfg.ContextWindow)
	}
}

// TestFillDefaultsMaxOutput 测试从内置规格表 padding max_output_tokens
func TestFillDefaultsMaxOutput(t *testing.T) {
	cfg := &ModelConfig{Name: "qwen3.5-flash", MaxOutputTokens: 0}
	cfg.FillDefaults()
	if cfg.MaxOutputTokens != 65536 {
		t.Errorf("expected max_output_tokens 65536, got %d", cfg.MaxOutputTokens)
	}
}

// TestFillDefaultsUnknownModel 测试未知模型使用default value
func TestFillDefaultsUnknownModel(t *testing.T) {
	cfg := &ModelConfig{Name: "my-custom-model"}
	cfg.FillDefaults()
	if cfg.ContextWindow != 200000 {
		t.Errorf("expected context_window 200000, got %d", cfg.ContextWindow)
	}
	if cfg.MaxOutputTokens != 16384 {
		t.Errorf("expected max_output_tokens 16384, got %d", cfg.MaxOutputTokens)
	}
}

// TestFillDefaultsBaseURLOpenAI 测试缺少 base_url 时 openai default value
func TestFillDefaultsBaseURLOpenAI(t *testing.T) {
	cfg := &ModelConfig{Provider: ProviderOpenAI, BaseURL: ""}
	cfg.FillDefaults()
	if cfg.BaseURL != openAIDefaultBase {
		t.Errorf("expected baseURL %s, got %s", openAIDefaultBase, cfg.BaseURL)
	}
}

// TestFillDefaultsBaseURLAnthropic 测试缺少 base_url 时 anthropic default value
func TestFillDefaultsBaseURLAnthropic(t *testing.T) {
	cfg := &ModelConfig{Provider: ProviderAnthropic, BaseURL: ""}
	cfg.FillDefaults()
	if cfg.BaseURL != anthropicBaseURL {
		t.Errorf("expected baseURL %s, got %s", anthropicBaseURL, cfg.BaseURL)
	}
}

// --- SetModelConfig 测试 ---

// TestSetModelConfigDifferentProvider 测试切换到不同 provider
func TestSetModelConfigDifferentProvider(t *testing.T) {
	c := NewSimpleClient("old-model", "sk-old", "https://old.com", ProviderOpenAI)
	c.SetModelConfig(&ModelConfig{Name: "claude", Provider: ProviderAnthropic, BaseURL: "https://api.anthropic.com", APIKey: "sk-ant-xxx"})

	if c.provider != ProviderAnthropic {
		t.Errorf("expected provider anthropic, got %s", c.provider)
	}
	if c.baseURL != "https://api.anthropic.com" {
		t.Errorf("expected baseURL https://api.anthropic.com, got %s", c.baseURL)
	}
	if c.apiKey != "sk-ant-xxx" {
		t.Errorf("expected apiKey sk-ant-xxx, got %s", c.apiKey)
	}
	if c.model != "claude" {
		t.Errorf("expected model claude, got %s", c.model)
	}
}

// TestSetModelConfigDifferentBaseURL 测试切换到不同 baseURL
func TestSetModelConfigDifferentBaseURL(t *testing.T) {
	c := NewSimpleClient("model", "sk-key", "https://dashscope.aliyuncs.com/compatible-mode/v1", ProviderOpenAI)
	c.SetModelConfig(&ModelConfig{Name: "deepseek-chat", Provider: ProviderOpenAI, BaseURL: "https://api.deepseek.com", APIKey: "sk-yyy"})

	if c.baseURL != "https://api.deepseek.com" {
		t.Errorf("expected baseURL https://api.deepseek.com, got %s", c.baseURL)
	}
}

// TestSetModelConfigTrailingSlash 测试 baseURL 尾部斜杠处理
func TestSetModelConfigTrailingSlash(t *testing.T) {
	c := NewSimpleClient("model", "sk-key", "https://old.com", ProviderOpenAI)
	c.SetModelConfig(&ModelConfig{BaseURL: "https://api.test.com/"})

	if c.baseURL != "https://api.test.com" {
		t.Errorf("expected baseURL without trailing slash, got %s", c.baseURL)
	}
}

// --- 配置持久化测试 ---

// TestSaveAndLoadModels 测试保存并重新加载
func TestSaveAndLoadModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	r := NewModelRegistry()
	r.Add(&ModelConfig{Name: "model-a", Provider: ProviderOpenAI, BaseURL: "https://a.com", APIKey: "sk-a", ContextWindow: 128000})
	r.Add(&ModelConfig{Name: "model-b", Provider: ProviderOpenAI, BaseURL: "https://b.com", APIKey: "sk-b", ContextWindow: 65536})

	if err := SaveModels(path, r); err != nil {
		t.Fatalf("SaveModels failed: %v", err)
	}

	loaded, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels failed: %v", err)
	}

	if len(loaded.List()) != 2 {
		t.Fatalf("expected 2 models, got %d", len(loaded.List()))
	}
	got := loaded.Get("model-a")
	if got == nil || got.BaseURL != "https://a.com" || got.APIKey != "sk-a" {
		t.Errorf("model-a not loaded correctly: %+v", got)
	}
}

// TestLoadModelsFileNotExist 测试文件不存在时加载
func TestLoadModelsFileNotExist(t *testing.T) {
	r, err := LoadModels("/nonexistent/path/models.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if len(r.List()) != 0 {
		t.Errorf("expected empty registry, got %d models", len(r.List()))
	}
}

// TestSaveModelsEmptyRegistry 测试空注册表保存
func TestSaveModelsEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	r := NewModelRegistry()
	if err := SaveModels(path, r); err != nil {
		t.Fatalf("SaveModels failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	expected := `{
  "models": {}
}`
	if string(data) != expected {
		t.Errorf("expected %q, got %q", expected, string(data))
	}
}

// --- FormatModelList 测试 ---

// TestFormatModelListOnlyRegistered 测试只显示已注册模型
func TestFormatModelListOnlyRegistered(t *testing.T) {
	reg := NewModelRegistry()
	reg.Add(&ModelConfig{Name: "qwen3.5-flash", Provider: ProviderOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", ContextWindow: 1048576, MaxOutputTokens: 65536})
	reg.Add(&ModelConfig{Name: "qwen3.5-35b-a3b", Provider: ProviderOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", ContextWindow: 262144, MaxOutputTokens: 65536})

	models := []string{"qwen3.5-flash", "qwen3.5-35b-a3b"}
	output := FormatModelList("qwen3.5-35b-a3b", models, reg)
	if len(output) == 0 {
		t.Fatal("expected non-empty output")
	}
	// should not contain claude 或 gpt
	if containsIgnoreCase(output, "claude") {
		t.Error("output should not contain 'claude'")
	}
	if containsIgnoreCase(output, "gpt-4o") {
		t.Error("output should not contain 'gpt-4o'")
	}
	if !containsIgnoreCase(output, "qwen3.5-flash") {
		t.Error("output should contain 'qwen3.5-flash'")
	}
	if !containsIgnoreCase(output, "◄ 当前") {
		t.Error("output should contain current marker")
	}
	// 应该包含 provider 和 base URL
	if !containsIgnoreCase(output, "openai") {
		t.Error("output should contain provider 'openai'")
	}
	if !containsIgnoreCase(output, "dashscope") {
		t.Error("output should contain base URL with 'dashscope'")
	}
}

// TestFormatModelListEmpty 测试空注册表
func TestFormatModelListEmpty(t *testing.T) {
	reg := NewModelRegistry()
	output := FormatModelList("test", []string{}, reg)
	// 空列表回退显示全部内置规格
	if !containsIgnoreCase(output, "可用模型列表") {
		t.Error("output should contain header")
	}
}

// --- API Key 脱敏测试 ---

// TestMaskAPIKeyStandard 测试标准 key
// 注意：使用占位符 sk-test... 避免泄漏真实 API Key（参见 docs/使用说明/07-挂网前补充建议.md §1.4 敏感信息扫描）
func TestMaskAPIKeyStandard(t *testing.T) {
	result := MaskAPIKey("sk-test1234567890abcdef1234567890")
	if result != "sk-t***7890" {
		t.Errorf("expected 'sk-t***7890', got '%s'", result)
	}
}

// TestMaskAPIKeyShort 测试短 key
func TestMaskAPIKeyShort(t *testing.T) {
	result := MaskAPIKey("sk-abcd")
	if result != "***" {
		t.Errorf("expected '***', got '%s'", result)
	}
}

// TestMaskAPIKeyEmpty 测试空 key
func TestMaskAPIKeyEmpty(t *testing.T) {
	result := MaskAPIKey("")
	if result != "***" {
		t.Errorf("expected '***', got '%s'", result)
	}
}

// --- ParseModelAddArgs 测试 ---

// TestParseModelAddFull 测试完整参数
func TestParseModelAddFull(t *testing.T) {
	cfg, err := ParseModelAddArgs("deepseek-chat --base-url https://api.deepseek.com --api-key sk-yyy --context-window 65536")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "deepseek-chat" {
		t.Errorf("expected name deepseek-chat, got %s", cfg.Name)
	}
	if cfg.BaseURL != "https://api.deepseek.com" {
		t.Errorf("expected baseURL https://api.deepseek.com, got %s", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-yyy" {
		t.Errorf("expected apiKey sk-yyy, got %s", cfg.APIKey)
	}
	if cfg.ContextWindow != 65536 {
		t.Errorf("expected contextWindow 65536, got %d", cfg.ContextWindow)
	}
}

// TestParseModelAddMinimal 测试最小参数
func TestParseModelAddMinimal(t *testing.T) {
	cfg, err := ParseModelAddArgs("deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "deepseek-chat" {
		t.Errorf("expected name deepseek-chat, got %s", cfg.Name)
	}
	// 其他字段为空，将由 FillDefaults padding
	if cfg.BaseURL != "" {
		t.Errorf("expected empty baseURL, got %s", cfg.BaseURL)
	}
}

// TestParseModelAddProvider 测试带 provider
func TestParseModelAddProvider(t *testing.T) {
	cfg, err := ParseModelAddArgs("claude-sonnet --provider anthropic --api-key sk-ant-xxx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("expected provider anthropic, got %s", cfg.Provider)
	}
	if cfg.APIKey != "sk-ant-xxx" {
		t.Errorf("expected apiKey sk-ant-xxx, got %s", cfg.APIKey)
	}
}

// TestParseModelAddMissingName 测试缺少模型名称
func TestParseModelAddMissingName(t *testing.T) {
	_, err := ParseModelAddArgs("--base-url https://api.test.com")
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	// --base-url 被当作名称后，解析器会报 "未知参数" 或 "缺少值"
	// 因为 "https://api.test.com" 被当作 --base-url 的值，而名称是 "--base-url"
	// actual行为：名称为 "--base-url"，后面跟着它的值，不会报错
	// 所以我们改为测试真正的空参数
	_, err = ParseModelAddArgs("")
	if err == nil {
		t.Fatal("expected error for empty args")
	}
}

// --- FormatModelInfo 测试 ---

// TestFormatModelInfo 测试模型详情格式化
func TestFormatModelInfo(t *testing.T) {
	cfg := &ModelConfig{
		Name:            "deepseek-chat",
		Provider:        ProviderOpenAI,
		BaseURL:         "https://api.deepseek.com",
		APIKey:          "sk-yyyyzzzz",
		ContextWindow:   65536,
		MaxOutputTokens: 8192,
	}
	output := FormatModelInfo(cfg)
	if !containsIgnoreCase(output, "deepseek-chat") {
		t.Error("output should contain model name")
	}
	if !containsIgnoreCase(output, "openai") {
		t.Error("output should contain provider")
	}
	if !containsIgnoreCase(output, "api.deepseek.com") {
		t.Error("output should contain base URL")
	}
	if !containsIgnoreCase(output, "sk-y***zzzz") {
		t.Error("output should contain masked API key")
	}
	if !containsIgnoreCase(output, "65536") {
		t.Error("output should contain context window")
	}
	// should not contain价格信息
	if containsIgnoreCase(output, "$") || containsIgnoreCase(output, "价格") {
		t.Error("output should not contain pricing info")
	}
}

// --- ParseModelEditArgs 测试 ---

// TestParseModelEditFull 测试完整编辑参数
func TestParseModelEditFull(t *testing.T) {
	name, updates, err := ParseModelEditArgs("deepseek-chat --base-url https://api.new.com --context-window 128000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "deepseek-chat" {
		t.Errorf("expected name deepseek-chat, got %s", name)
	}
	if updates["base_url"] != "https://api.new.com" {
		t.Errorf("expected base_url https://api.new.com, got %s", updates["base_url"])
	}
	if updates["context_window"] != "128000" {
		t.Errorf("expected context_window 128000, got %s", updates["context_window"])
	}
}

// TestParseModelEditNoUpdates 测试没有指定修改字段
func TestParseModelEditNoUpdates(t *testing.T) {
	_, _, err := ParseModelEditArgs("deepseek-chat")
	if err == nil {
		t.Fatal("expected error when no updates specified")
	}
}

// TestParseModelEditEmpty 测试空参数
func TestParseModelEditEmpty(t *testing.T) {
	_, _, err := ParseModelEditArgs("")
	if err == nil {
		t.Fatal("expected error for empty args")
	}
}

// --- ApplyModelUpdates 测试 ---

// TestApplyModelUpdatesBaseURL 测试修改 BaseURL
func TestApplyModelUpdatesBaseURL(t *testing.T) {
	cfg := &ModelConfig{Name: "test", Provider: ProviderOpenAI, BaseURL: "https://old.com", ContextWindow: 128000}
	err := ApplyModelUpdates(cfg, map[string]string{"base_url": "https://new.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://new.com" {
		t.Errorf("expected base_url https://new.com, got %s", cfg.BaseURL)
	}
	// 其他字段不变
	if cfg.ContextWindow != 128000 {
		t.Errorf("expected context_window 128000, got %d", cfg.ContextWindow)
	}
}

// TestApplyModelUpdatesContextWindow 测试修改 ContextWindow
func TestApplyModelUpdatesContextWindow(t *testing.T) {
	cfg := &ModelConfig{Name: "test", Provider: ProviderOpenAI, ContextWindow: 128000}
	err := ApplyModelUpdates(cfg, map[string]string{"context_window": "262144"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ContextWindow != 262144 {
		t.Errorf("expected context_window 262144, got %d", cfg.ContextWindow)
	}
}

// TestApplyModelUpdatesInvalidContextWindow 测试无效的 ContextWindow
func TestApplyModelUpdatesInvalidContextWindow(t *testing.T) {
	cfg := &ModelConfig{Name: "test", ContextWindow: 128000}
	err := ApplyModelUpdates(cfg, map[string]string{"context_window": "abc"})
	if err == nil {
		t.Fatal("expected error for invalid context_window")
	}
}

// TestApplyModelUpdatesMultiple 测试同时修改多个字段
func TestApplyModelUpdatesMultiple(t *testing.T) {
	cfg := &ModelConfig{Name: "test", Provider: ProviderOpenAI, BaseURL: "https://old.com", ContextWindow: 128000, MaxOutputTokens: 8192}
	err := ApplyModelUpdates(cfg, map[string]string{
		"base_url":          "https://new.com",
		"context_window":    "262144",
		"max_output_tokens": "32768",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://new.com" {
		t.Errorf("expected base_url https://new.com, got %s", cfg.BaseURL)
	}
	if cfg.ContextWindow != 262144 {
		t.Errorf("expected context_window 262144, got %d", cfg.ContextWindow)
	}
	if cfg.MaxOutputTokens != 32768 {
		t.Errorf("expected max_output_tokens 32768, got %d", cfg.MaxOutputTokens)
	}
}

// === 模型规格匹配测试 ===
//
// ★ D 批：原先这里是 5-E-1 的「自定义定价接线」测试组
// （SetCustomPricing / PricingForModel / GetTotalCost）。自定义定价与成本计算
// 已随成本线整体删除，故那一组连同其全局注入点一起移除。
// 保留下来的匹配器（matchSpec）只回答上下文窗口 / 最大输出，测试随之改名。

// TestSpecForModel_UsesDefaultSpecs 验证未注入任何覆盖时仍能从内置规格表命中。
func TestSpecForModel_UsesDefaultSpecs(t *testing.T) {
	s := SpecForModel("claude-sonnet-4-20250514")
	if s == nil {
		t.Fatal("SpecForModel 返回 nil")
	}
	if s.Name != "claude-sonnet-4-20250514" {
		t.Errorf("Name = %q, want claude-sonnet-4-20250514", s.Name)
	}
}

// TestMatchSpec_PreservesMatchStrategy 验证 matchSpec 保持 exact→包含→前缀 三级匹配。
func TestMatchSpec_PreservesMatchStrategy(t *testing.T) {
	table := []ModelSpec{
		{Name: "claude-sonnet-4", ContextWindow: 200000},
		{Name: "gpt-4o", ContextWindow: 128000},
	}

	// 1. exact match（大小写不敏感）
	s := matchSpec("claude-sonnet-4", table)
	if s == nil || s.Name != "claude-sonnet-4" {
		t.Errorf("exact match 失败: got %+v", s)
	}

	// 2. 包含 match（table 名是查询的子串，大小写不敏感）
	s = matchSpec("CLAUDE-SONNET-4-20250514", table)
	if s == nil {
		t.Errorf("包含 match 失败")
	}

	// 3. 前缀 match（名字前 8 字符）
	s = matchSpec("gpt-4o-2024-08-06", table)
	if s == nil {
		t.Errorf("前缀 match 失败")
	}

	// 4. 未命中返回 nil
	s = matchSpec("nonexistent-model-xyz", table)
	if s != nil {
		t.Errorf("未命中应返回 nil，got %+v", s)
	}
}
