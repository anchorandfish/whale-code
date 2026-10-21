// g_b_s18g4_perms_test.go — G-B（O-B）S18-G4：遗忘标记文件/目录权限卫生。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md §6 G-B 组行含 S18-G4。
// 现状核实：`updateLastScanAt` 已按 S18/G4 修为 目录 0700 / 文件 0600
// （见 forgetting.go 头部注释），Markdown 侧已有 `TestS18_Memory_ForgettingMarkerPerms0600`
// 钉住**文件**权限；本文件补齐此前**缺失的两条**判别性断言：
// 1. 标记文件**目录**（含多级 MkdirAll 路径）必须是 0700，不得世界可枚举；
// 2. 归档目录 `_archive/`（E3 新增）同样必须是 0700、归档件 0600（同一权限卫生族）。
//
// 权限不是「风格」问题：_user/ 与 project scope 目录列出即暴露用户记忆条目名。
package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestG_B_S18G4_ForgettingMarkerDirIs0700 —— 标记文件所在目录不得世界可读/可枚举。
// 注意：只断言 updateLastScanAt 亲自创建的目录（base 由 t.TempDir 创建，不属被测行为）。
func TestG_B_S18G4_ForgettingMarkerDirIs0700(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "scope", "inner")

	if err := updateLastScanAt(nested); err != nil {
		t.Fatalf("updateLastScanAt 失败: %v", err)
	}

	for _, dir := range []string{filepath.Dir(nested), nested} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("目录不存在 %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("S18-G4：目录 %s 权限 = %o，期望 0700", dir, perm)
		}
	}

	fi, err := os.Stat(filepath.Join(nested, lastScanAtFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("S18-G4：标记文件权限 = %o，期望 0600", perm)
	}
}

// TestG_B_S18G4_ArchiveDirAndFilePerms —— E3 归档目录/文件的权限卫生。
func TestG_B_S18G4_ArchiveDirAndFilePerms(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStoreForScope(dir, "user", false)
	if err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(store.Dir(), "g4-archive-me.md")
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("body\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	archPath, err := archiveFile(store.Dir(), src)
	if err != nil {
		t.Fatalf("archiveFile 失败: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Join(store.Dir(), memoryArchiveDirName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("S18-G4：归档目录权限 = %o，期望 0700", perm)
	}
	fileInfo, err := os.Stat(archPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("S18-G4：归档件权限 = %o，期望 0600", perm)
	}
}
