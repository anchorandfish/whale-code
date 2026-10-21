// audit_s12_backup_perm_test.go — S12-G6 类（/migrate 侧）
//
// 目的：钉死 `/migrate run` 的迁移前备份权限与就地重写权限。
// settings.json 可能含 api_key，0644 备份会让同机其他用户可读。
// 修复前：备份 0644（红）；修复后：备份 0600 + 原子写（绿）。
package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestS12_MigrateBackupPerm0600(t *testing.T) {
	home := t.TempDir()
	whaleDir := filepath.Join(home, ".whale")
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	// performMigration 从 whalehome.Dir()（$WHALE_HOME）解析状态目录，测试必须经环境变量注入
	t.Setenv("WHALE_HOME", whaleDir)
	cfgPath := filepath.Join(whaleDir, "settings.json")
	content := `{"config_version":0,"api_key":"sk-secret","permission_mode":"workspace-write"}`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := performMigration(home); err != nil {
		t.Fatalf("performMigration: %v", err)
	}

	// 1) 备份（{path}.{ts}.bak）必须 0600
	matches, _ := filepath.Glob(cfgPath + ".*.bak")
	if len(matches) == 0 {
		t.Fatalf("expected a migration backup, found none")
	}
	fi, serr := os.Stat(matches[0])
	if serr != nil {
		t.Fatalf("stat backup: %v", serr)
	}
	if mode := fi.Mode().Perm(); mode != 0600 {
		t.Errorf("migrate backup perm = %o, want 0600 (S12-G6 class fix)", mode)
	}

	// 2) 迁移后就地重写的 settings.json 仍须 0600 且可解析、api_key 保留
	sfi, serr2 := os.Stat(cfgPath)
	if serr2 != nil {
		t.Fatalf("stat settings: %v", serr2)
	}
	if mode := sfi.Mode().Perm(); mode != 0600 {
		t.Errorf("rewritten settings.json perm = %o, want 0600", mode)
	}
	b, rerr := os.ReadFile(cfgPath)
	if rerr != nil {
		t.Fatalf("read settings: %v", rerr)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("rewritten settings is not valid JSON: %v", err)
	}
	if raw["api_key"] != "sk-secret" {
		t.Errorf("api_key lost after migration: %v", raw["api_key"])
	}
	if v, ok := raw["config_version"].(float64); !ok || int(v) != CurrentConfigVersion {
		t.Errorf("config_version not migrated: %v", raw["config_version"])
	}
}
