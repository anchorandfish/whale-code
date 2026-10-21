package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestConfig_CompactModel_FromSettingsAndEnv 钉「压缩模型可配」的配置面：
//
//  1. settings.json 的 compact_model 能被读出；
//  2. WHALE_COMPACT_MODEL 仅在 settings.json 为空时兜底（与三模型字段同规矩）；
//  3. 都不配时为空 —— 语义 = 沿用主模型（**默认行为必须与今天逐字相同**）。
func TestConfig_CompactModel_FromSettingsAndEnv(t *testing.T) {
	writeSettings := func(t *testing.T, dir string, m map[string]interface{}) {
		t.Helper()
		whaleDir := filepath.Join(dir, ".whale")
		if err := os.MkdirAll(whaleDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), data, 0644); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}

	t.Run("settings.json 显式配置优先于 env", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, map[string]interface{}{"model": "m", "compact_model": "cheap-from-file"})
		t.Setenv("WHALE_COMPACT_MODEL", "cheap-from-env")

		cfg, err := NewLoader("", dir).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.CompactModel != "cheap-from-file" {
			t.Fatalf("CompactModel=%q, want cheap-from-file（settings.json 应优先）", cfg.CompactModel)
		}
	})

	t.Run("env 仅作 fallback", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, map[string]interface{}{"model": "m"})
		t.Setenv("WHALE_COMPACT_MODEL", "cheap-from-env")

		cfg, err := NewLoader("", dir).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.CompactModel != "cheap-from-env" {
			t.Fatalf("CompactModel=%q, want cheap-from-env", cfg.CompactModel)
		}
	})

	t.Run("都不配 = 空（沿用主模型，行为不变）", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, map[string]interface{}{"model": "m"})

		cfg, err := NewLoader("", dir).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.CompactModel != "" {
			t.Fatalf("CompactModel=%q, want \"\"（默认必须为空 ⇒ 沿用主模型）", cfg.CompactModel)
		}
	})
}
