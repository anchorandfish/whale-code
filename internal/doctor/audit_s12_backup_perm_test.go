// audit_s12_backup_perm_test.go — S12-G6 类（doctor 侧）
//
// 目的：钉死 `whale doctor --config-migrate` 的迁移前备份权限。
// settings.json 可能含 api_key / bridge.jwt_secret，0644 备份会让同机其他用户可读。
// 修复前：0644（红）；修复后：0600 + 原子写（绿）。
package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS12_DoctorBackupSettingsFile_Perm0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 POSIX 权限位（Go 建文件恒 0666），0600 断言仅适用 POSIX")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	data := []byte(`{"config_version":0,"api_key":"sk-secret"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	bak, err := backupSettingsFile(path, data)
	if err != nil {
		t.Fatalf("backupSettingsFile: %v", err)
	}
	if bak == "" {
		t.Fatal("expected a backup path")
	}
	fi, serr := os.Stat(bak)
	if serr != nil {
		t.Fatalf("stat backup: %v", serr)
	}
	if mode := fi.Mode().Perm(); mode != 0600 {
		t.Errorf("doctor settings backup perm = %o, want 0600 (S12-G6 class fix)", mode)
	}
	got, rerr := os.ReadFile(bak)
	if rerr != nil {
		t.Fatalf("read backup: %v", rerr)
	}
	if string(got) != string(data) {
		t.Errorf("backup content mismatch: got %q, want %q", string(got), string(data))
	}
}
