package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
)

// TestResolveModel_ProductionPath 模型生效值的生产链路判据。
//
// 背景（2026-09-23 取证：模型生效值_写入路径不可见_半落地与三方对标_2026-09-23.md）：
// main.go 的 resolveModel 原实现【无条件】读 WHALE_MODEL 环境变量 ⇒ 覆盖 settings.json；
// 而 /model 的写路径只落 settings.json（internal/tui/commands_model.go:137 handleModelSwitch
// → loader.Save）⇒ 用户切换模型后冷启动被 env 盖回，即"写了个读不到的地方"。
//
// 已拍板方向 = 内部交付台账:454-469
// （P7 候选 A：settings.json 为真相源 + env fallback），且 config 层已按此实现
// （internal/config/config.go:610-621）+ 已有真钉死测试（config_test.go:161-197）
// ⇒ 本测试补的是 main 层缺失的那一环。
//
// ★ 被本测试替换掉的旧 TestResolveModel 是【假测试】：它只断言 os.Getenv("WHALE_MODEL")
// 等于它上一行自己 set 的值，从不调用 resolveModel ⇒ 恒绿、物理上不可能变红。
//
// 判别力要求：第一个子用例在修复前必须【红】（返回 env-model），修复后必须【绿】；
// 若修复前就绿 ⇒ 判别器无效，不得当作通过。
func TestResolveModel_ProductionPath(t *testing.T) {
	t.Run("settings_json_is_source_of_truth_over_env", func(t *testing.T) {
		// /model 的落点（settings.json）必须能被冷启动读到 ⇒ 不得被 env 盖掉。
		// 期望：file-model。修复前实际：env-model（红）。
		_, got := resolveModelViaProdChain(t, `{"model":"file-model"}`, "env-model")
		if got != "file-model" {
			t.Errorf("resolveModel() = %q, want %q（settings.json 是真相源，env 只是兜底）", got, "file-model")
		}
	})

	t.Run("env_fallback_when_settings_has_no_model", func(t *testing.T) {
		// env 兜底语义必须【保留】：settings.json 没有 model 键时，WHALE_MODEL 仍生效。
		// 本用例在修复前后都应绿 —— 它防的是"把 env 能力一起删掉"的过度修。
		_, got := resolveModelViaProdChain(t, `{"permission_mode":"workspace-write"}`, "env-model")
		if got != "env-model" {
			t.Errorf("resolveModel() = %q, want %q（settings 无 model 时应回退到 env）", got, "env-model")
		}
	})

	t.Run("three_models_share_one_precedence", func(t *testing.T) {
		// 同源：主/降级/快速三模型不得出现"主模型 env 优先、另两个 settings 优先"的口径分裂。
		settings := `{"model":"file-main","fallback_model":"file-fallback","fast_model":"file-fast"}`
		cfg, got := resolveModelViaProdChain(t, settings, "env-main")
		if got != "file-main" {
			t.Errorf("resolveModel() = %q, want %q", got, "file-main")
		}
		if cfg.FallbackModel != "file-fallback" {
			t.Errorf("cfg.FallbackModel = %q, want %q", cfg.FallbackModel, "file-fallback")
		}
		if cfg.FastModel != "file-fast" {
			t.Errorf("cfg.FastModel = %q, want %q", cfg.FastModel, "file-fast")
		}
	})
}

// resolveModelViaProdChain 走【生产读取链】：落真 settings.json → config.NewLoader().Load()
// （内部已含 applyEnvironmentOverrides）→ resolveModel()。
//
// ★ 禁止手工构造 config.Config 直传 resolveModel —— 那样测的是"我造的路径"，
// 会漏掉 applyEnvironmentOverrides 的参与（本坑正出在两端优先级相反上）。
func resolveModelViaProdChain(t *testing.T, settingsJSON, envModel string) (*config.Config, string) {
	t.Helper()

	projectDir := t.TempDir()
	whaleDir := filepath.Join(projectDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0o755); err != nil {
		t.Fatalf("创建 .whale 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("写入 settings.json 失败: %v", err)
	}

	// t.Setenv 自动还原；空串等价于"未设置"（生产判定是 v != ""）。
	t.Setenv("WHALE_MODEL", envModel)
	t.Setenv("WHALE_FALLBACK_MODEL", envModel)
	t.Setenv("WHALE_FAST_MODEL", envModel)

	loader := config.NewLoader("", projectDir)
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("config.Load 失败: %v", err)
	}
	return cfg, resolveModel(cfg)
}
