package onboarding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/doctor"
)

// 反例 1：首次运行检测正确（哨兵文件 + settings.json + API key 三层检测）
// 验证：无哨兵文件 + settings.json 无 Onboarded 字段 + 无 API key 时触发 Onboarding；
// 写哨兵文件后 IsCompleted 返回 true；settings.json Onboarded=true 也返回 true；
// settings.json 有 API key 但 Onboarded=false 时也返回 true（第 3 层兼容性兜底）。
// 对标审查 prompt §反例 1。
func TestOnboarding_FirstRun_Detected(t *testing.T) {
	homeDir := t.TempDir() // 空目录，模拟首次运行
	// 哨兵文件与 settings.json 均从 whalehome.Dir()（$WHALE_HOME）解析，经环境变量注入
	t.Setenv("WHALE_HOME", homeDir)

	// 无哨兵文件 + 无 settings.json → 未完成
	if IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be false for fresh homeDir (no sentinel, no settings.json)")
	}

	// 写哨兵文件后 → 完成
	sentinel := sentinelPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(sentinel), 0700); err != nil {
		t.Fatalf("mkdir sentinel dir: %v", err)
	}
	if err := os.WriteFile(sentinel, []byte("test"), 0600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if !IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be true after sentinel file written")
	}

	// 删哨兵文件，写 settings.json Onboarded=true → 完成（第 2 层）
	if err := os.Remove(sentinel); err != nil {
		t.Fatalf("remove sentinel: %v", err)
	}
	loader := config.NewLoader(homeDir, "")
	cfg := config.DefaultConfigPtr()
	cfg.Onboarded = true
	if err := loader.Save(cfg); err != nil {
		t.Fatalf("save settings.json: %v", err)
	}
	if !IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be true when settings.json has onboarded:true")
	}

	// 重置：Onboarded=false 但有 API key → 完成（第 3 层兼容性兜底）
	cfg.Onboarded = false
	cfg.APIKey = "sk-ant-api-key-from-settings"
	if err := loader.Save(cfg); err != nil {
		t.Fatalf("save settings.json with API key: %v", err)
	}
	if !IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be true when settings.json has API key (3rd layer fallback)")
	}
}

// 反例 2：API key 缺失时不 os.Exit（边界 case，零容忍）
// 验证：通过 NewOnboardingWithStdin 注入 mock stdin，调用完整 ob.Run() 流程，
// 验证 settings.json 写入 + 哨兵文件写入，确保 API key 缺失时走 Onboarding 而非 os.Exit。
// 对标审查 prompt §反例 2。
func TestOnboarding_APIKeyMissing_NoOSExit(t *testing.T) {
	homeDir := t.TempDir()
	// 状态目录从 whalehome.Dir() 解析，经环境变量注入
	t.Setenv("WHALE_HOME", homeDir)
	workDir := t.TempDir()

	// 清除环境变量确保 API key 缺失
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	// 应检测为未完成 → 触发 Onboarding 而非 os.Exit
	if IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be false when API key missing and no onboarding completed")
	}

	// 构造 mock stdin（模拟用户选择 Anthropic + 输入 API key）
	// 步骤 2：选择 1（Anthropic）
	// 步骤 3：输入 API key
	mockInput := "1\nsk-ant-test-mock-key-12345\n"
	mockStdin := strings.NewReader(mockInput)

	// 使用 NewOnboardingWithStdin 注入 mock stdin（doctor=nil 跳过步骤1环境检测）
	ob := NewOnboardingWithStdin(nil, nil, workDir, homeDir, mockStdin)
	if ob == nil {
		t.Fatal("NewOnboardingWithStdin should not return nil")
	}

	// 调用完整 Run() 流程（审查 prompt 明确要求验证完整 Run）
	completed, err := ob.Run(context.Background())
	if err != nil {
		t.Fatalf("ob.Run() should succeed, got error: %v", err)
	}
	if !completed {
		t.Fatal("ob.Run() should return completed=true")
	}

	// 验证 settings.json 已写入 API key（步骤 4 配置生成）
	loader := config.NewLoader(homeDir, "")
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("load settings.json failed: %v", err)
	}
	if cfg.APIKey != "sk-ant-test-mock-key-12345" {
		t.Fatalf("cfg.APIKey = %q, want sk-ant-test-mock-key-12345", cfg.APIKey)
	}
	if cfg.Provider != "anthropic" {
		t.Fatalf("cfg.Provider = %q, want anthropic", cfg.Provider)
	}
	if !cfg.Onboarded {
		t.Fatal("cfg.Onboarded should be true after Run()")
	}

	// 验证哨兵文件存在
	if _, err := os.Stat(sentinelPath(homeDir)); err != nil {
		t.Fatalf("sentinel file should exist after Run(): %v", err)
	}

	// 验证 IsCompleted 返回 true（双保险）
	if !IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be true after Run()")
	}

	// State 应已初始化且有正确值
	state := ob.State()
	if state == nil {
		t.Fatal("Onboarding state should be initialized")
	}
	if state.APIKey != "sk-ant-test-mock-key-12345" {
		t.Fatalf("state.APIKey = %q, want sk-ant-test-mock-key-12345", state.APIKey)
	}
}

// 反例 3：已有配置用户自动跳过（兼容性）
// 验证：已有 settings.json + 环境变量 API key 的用户通过 MaybeMarkExistingUser 自动标记完成。
// 缓解改进方案 §3.3 兼容性风险：不强制现有用户重跑 Onboarding。
// 对标审查 prompt §反例 3。
func TestOnboarding_ExistingUser_Skip(t *testing.T) {
	homeDir := t.TempDir()
	// Loader.Save / 哨兵文件都从 whalehome.Dir()（$WHALE_HOME）解析，
	// 不再取 homeDir 参数 ⇒ 必须显式注入，否则本用例会写到宿主真实的 ~/.whale/
	// （实测：把用户 settings.json 的 model 覆写成 "test-model"、api_key 覆写成 "sk-test-key"）。
	t.Setenv("WHALE_HOME", homeDir)

	// 创建已有 settings.json（模拟现有用户）
	loader := config.NewLoader(homeDir, "")
	cfg := config.DefaultConfigPtr()
	cfg.Model = "test-model"
	if err := loader.Save(cfg); err != nil {
		t.Fatalf("save settings.json: %v", err)
	}

	// 设置环境变量 API key（模拟现有用户已配置）
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-existing")

	// MaybeMarkExistingUser 应自动标记完成
	if !MaybeMarkExistingUser(homeDir) {
		t.Fatal("MaybeMarkExistingUser should return true for existing user with settings.json + env API key")
	}

	// 标记后 IsCompleted 应返回 true
	if !IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be true after MaybeMarkExistingUser auto-marked")
	}
}

// 反例 4：reset 命令重置允许重跑（边界 case）
// 验证：/onboarding reset 删除哨兵文件，IsCompleted 返回 false，允许重跑。
// 对标审查 prompt §反例 4。
func TestOnboarding_Reset_AllowsRerun(t *testing.T) {
	homeDir := t.TempDir()
	// 状态目录从 whalehome.Dir() 解析，经环境变量注入
	t.Setenv("WHALE_HOME", homeDir)

	// 写哨兵文件
	sentinel := sentinelPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(sentinel), 0700); err != nil {
		t.Fatalf("mkdir sentinel dir: %v", err)
	}
	if err := os.WriteFile(sentinel, []byte("test"), 0600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// 确认已完成
	if !IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be true after sentinel written")
	}

	// Reset
	if err := Reset(homeDir); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// Reset 后应未完成（允许重跑）
	if IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be false after Reset")
	}

	// 重复 Reset 不报错（幂等）
	if err := Reset(homeDir); err != nil {
		t.Fatalf("Reset should be idempotent, got error: %v", err)
	}
}

// 反例 5：doctor 为 nil 时步骤 1 安全跳过（边界 case）
// 验证：doctor 未初始化时步骤 1 跳过，不阻塞 Onboarding。
// 对标审查 prompt §反例 5 + 改进方案 §1.5 doctor nil 降级处理。
func TestOnboarding_DoctorNil_SkipStep1(t *testing.T) {
	tmpDir := t.TempDir()
	ob := NewOnboarding(nil, nil, tmpDir, tmpDir)

	state := &OnboardingState{}
	err := ob.RunStepEnvironment(state)
	if err != nil {
		t.Fatalf("RunStepEnvironment should skip when doctor is nil, got error: %v", err)
	}
	if len(state.FatalErrors) != 0 {
		t.Fatalf("state.FatalErrors should be empty when doctor is nil, got %d items", len(state.FatalErrors))
	}
}

// 补充测试 1：RunStepConfig 写入 settings.json + 哨兵文件 + 完成标记
// 验证：步骤 4 执行后 settings.json 中 Onboarded=true 且哨兵文件存在。
// 审查 v1 §反例测试缺失项补充（非阻塞）。
func TestOnboarding_RunStepConfig_WritesSettingsAndSentinel(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	// RunStepConfig 写 settings.json + 哨兵都走 whalehome.Dir()（$WHALE_HOME）
	// ⇒ 必须显式注入，否则写到宿主真实 ~/.whale/（实测覆写用户 model/api_key）。
	t.Setenv("WHALE_HOME", homeDir)
	ob := NewOnboarding(nil, nil, workDir, homeDir)

	// 模拟步骤 2/3 收集的状态
	state := &OnboardingState{
		Provider:     "anthropic",
		APIKey:       "sk-test-key",
		APIKeySource: "settings",
		Model:        "claude-sonnet-4-5",
		EnvVar:       "ANTHROPIC_API_KEY",
		WorkDir:      workDir,
		HomeDir:      homeDir,
	}

	if err := ob.RunStepConfig(state); err != nil {
		t.Fatalf("RunStepConfig failed: %v", err)
	}

	// 验证哨兵文件存在
	sentinel := sentinelPath(homeDir)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel file should exist after RunStepConfig: %v", err)
	}

	// 验证 settings.json 中 Onboarded=true
	loader := config.NewLoader(homeDir, "")
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("load settings.json failed: %v", err)
	}
	if !cfg.Onboarded {
		t.Fatal("cfg.Onboarded should be true after RunStepConfig")
	}
	if cfg.Provider != "anthropic" {
		t.Fatalf("cfg.Provider = %q, want anthropic", cfg.Provider)
	}
	if cfg.APIKey != "sk-test-key" {
		t.Fatalf("cfg.APIKey = %q, want sk-test-key", cfg.APIKey)
	}
	if cfg.Model != "claude-sonnet-4-5" {
		t.Fatalf("cfg.Model = %q, want claude-sonnet-4-5", cfg.Model)
	}
}

// 补充测试 2：MarkComplete 双保险写入验证
// 验证：MarkComplete 同时写哨兵文件 + 更新 settings.json Onboarded 字段。
func TestOnboarding_MarkComplete_DualGuarantee(t *testing.T) {
	homeDir := t.TempDir()
	// MarkComplete 写哨兵 + settings.json 走 whalehome.Dir()（$WHALE_HOME）
	// ⇒ 必须显式注入，否则写到宿主真实 ~/.whale/（实测覆写用户 model 为 "test-model"）。
	t.Setenv("WHALE_HOME", homeDir)
	cfg := config.DefaultConfigPtr()
	cfg.Model = "test-model"

	if err := MarkComplete(homeDir, cfg); err != nil {
		t.Fatalf("MarkComplete failed: %v", err)
	}

	// 哨兵文件存在
	if _, err := os.Stat(sentinelPath(homeDir)); err != nil {
		t.Fatalf("sentinel file should exist after MarkComplete: %v", err)
	}

	// settings.json Onboarded=true
	loader := config.NewLoader(homeDir, "")
	loadedCfg, err := loader.Load()
	if err != nil {
		t.Fatalf("load settings.json failed: %v", err)
	}
	if !loadedCfg.Onboarded {
		t.Fatal("loadedCfg.Onboarded should be true after MarkComplete")
	}
}

// 补充测试 3：步骤 1 环境检测 fatal 时阻塞（边界 case）
// 验证：doctor.Run 返回 fatal 时 RunStepEnvironment 返回 error，不继续后续步骤。
func TestOnboarding_StepEnv_FatalBlocks(t *testing.T) {
	// 构造一个无 API key 的 doctor，预期 checkAPI 标记 fatal
	homeDir := t.TempDir()
	workDir := t.TempDir()
	settingsPath := filepath.Join(homeDir, ".whale", "settings.json")

	// 清除环境变量确保 API key 缺失
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	cfg := config.DefaultConfigPtr()
	doc := doctor.NewDoctor(cfg, nil, workDir, settingsPath)
	ob := NewOnboarding(cfg, doc, workDir, homeDir)

	state := &OnboardingState{}
	err := ob.RunStepEnvironment(state)

	// 应返回 error（API key 缺失为 fatal）
	if err == nil {
		t.Fatal("RunStepEnvironment should return error when fatal detected (API key missing)")
	}
	// state.FatalErrors 应非空
	if len(state.FatalErrors) == 0 {
		t.Fatal("state.FatalErrors should not be empty when fatal detected")
	}
}

// 补充测试 4：MaybeMarkExistingUser 无 settings.json 时不标记（边界 case）
// 验证：无 settings.json + 无环境变量 API key 时返回 false（真正的新用户）。
func TestOnboarding_MaybeMarkExistingUser_NoSettings(t *testing.T) {
	homeDir := t.TempDir() // 空目录
	// 状态目录从 whalehome.Dir() 解析，经环境变量注入
	t.Setenv("WHALE_HOME", homeDir)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	if MaybeMarkExistingUser(homeDir) {
		t.Fatal("MaybeMarkExistingUser should return false when no settings.json and no env API key")
	}
	if IsCompleted(homeDir) {
		t.Fatal("IsCompleted should be false after MaybeMarkExistingUser returned false")
	}
}
