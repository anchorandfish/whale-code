package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/testutil"
)

// setTestHome 设置测试用 HOME/USERPROFILE，返回临时目录路径。
// 用于隔离 checkRecovery 对真实 ~/.whale/ 的依赖。
func setTestHome(t *testing.T) string {
	t.Helper()
	tmpHome := testutil.IsolatedHomeDir(t)
	// Unix 与 Windows 均覆盖（os.UserHomeDir 在 Unix 用 HOME，Windows 用 USERPROFILE）
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	return tmpHome
}

// TestDoctor_CheckRecovery_NoWhaleDir ~/.whale 不存在时返回 OK（未初始化）。
func TestDoctor_CheckRecovery_NoWhaleDir(t *testing.T) {
	setTestHome(t)

	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.checkRecovery()
	if len(results) != 1 {
		t.Fatalf("期望 1 项结果，实际 %d", len(results))
	}
	if results[0].Severity != SeverityOK {
		t.Errorf("期望 SeverityOK（~/.whale 不存在），实际 %v", results[0].Severity)
	}
}

// TestDoctor_CheckRecovery_CorruptedFound 检测到损坏文件时返回 Warn 并建议 --repair。
func TestDoctor_CheckRecovery_CorruptedFound(t *testing.T) {
	tmpHome := setTestHome(t)
	whaleDir := filepath.Join(tmpHome, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 写入损坏 JSON
	if err := os.WriteFile(filepath.Join(whaleDir, "bad.json"), []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.checkRecovery()
	if len(results) != 1 {
		t.Fatalf("期望 1 项结果，实际 %d", len(results))
	}
	if results[0].Severity != SeverityWarn {
		t.Errorf("期望 SeverityWarn（检测到损坏），实际 %v", results[0].Severity)
	}
	if !strings.Contains(results[0].FixSuggest, "--repair") {
		t.Errorf("期望 FixSuggest 含 '--repair'，实际 %q", results[0].FixSuggest)
	}
}

// TestDoctor_CheckRecovery_NoCorrupted 无损坏文件时返回 OK。
func TestDoctor_CheckRecovery_NoCorrupted(t *testing.T) {
	tmpHome := setTestHome(t)
	whaleDir := filepath.Join(tmpHome, ".whale")
	if err := os.MkdirAll(whaleDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "ok.json"), []byte(`{"a":1}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.checkRecovery()
	if len(results) != 1 {
		t.Fatalf("期望 1 项结果，实际 %d", len(results))
	}
	if results[0].Severity != SeverityOK {
		t.Errorf("期望 SeverityOK（无损坏），实际 %v", results[0].Severity)
	}
}

// TestDoctor_CheckRecovery_SkipsCorruptDir 不扫描已隔离的 corrupt 目录。
func TestDoctor_CheckRecovery_SkipsCorruptDir(t *testing.T) {
	tmpHome := setTestHome(t)
	whaleDir := filepath.Join(tmpHome, ".whale")
	corruptSub := filepath.Join(whaleDir, "corrupt", "20260101-000000")
	if err := os.MkdirAll(corruptSub, 0755); err != nil {
		t.Fatal(err)
	}
	// corrupt 子目录下的损坏文件不应被报告
	if err := os.WriteFile(filepath.Join(corruptSub, "already.json"), []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.checkRecovery()
	if len(results) != 1 {
		t.Fatalf("期望 1 项结果，实际 %d", len(results))
	}
	if results[0].Severity != SeverityOK {
		t.Errorf("期望 SeverityOK（corrupt 目录不扫描），实际 %v (msg: %s)", results[0].Severity, results[0].Message)
	}
}

// TestDoctor_CheckRecovery_NestedDirs 递归扫描子目录中的损坏文件。
func TestDoctor_CheckRecovery_NestedDirs(t *testing.T) {
	tmpHome := setTestHome(t)
	whaleDir := filepath.Join(tmpHome, ".whale")
	sub := filepath.Join(whaleDir, "sessions", "v1")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested_bad.json"), []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfigPtr()
	doc := NewDoctor(cfg, nil, t.TempDir(), "")
	results := doc.checkRecovery()
	if len(results) != 1 {
		t.Fatalf("期望 1 项结果，实际 %d", len(results))
	}
	if results[0].Severity != SeverityWarn {
		t.Errorf("期望 SeverityWarn（嵌套损坏），实际 %v", results[0].Severity)
	}
	if !strings.Contains(results[0].Detail, "nested_bad.json") {
		t.Errorf("期望 Detail 含 'nested_bad.json'，实际 %q", results[0].Detail)
	}
}
