package util

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadWithRecovery_ValidJSON 正常 JSON 文件加载成功且不创建隔离目录。
func TestLoadWithRecovery_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	data := []byte(`{"name":"whale","version":1}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	backupDir := filepath.Join(dir, "corrupt")
	if err := LoadWithRecovery(path, &got, backupDir); err != nil {
		t.Fatalf("LoadWithRecovery err: %v", err)
	}
	if got.Name != "whale" || got.Version != 1 {
		t.Errorf("got = %+v, want {Name:whale Version:1}", got)
	}
	// 不应创建隔离目录（正常文件不隔离）
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Errorf("不应创建隔离目录（正常文件不隔离）")
	}
}

// TestLoadWithRecovery_CorruptedJSON 损坏 JSON 文件被隔离到 backupDir 并返回 ErrFileCorrupted。
func TestLoadWithRecovery_CorruptedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"name":broken`), 0644); err != nil {
		t.Fatal(err)
	}
	var got struct{ Name string }
	backupDir := filepath.Join(dir, "corrupt")
	err := LoadWithRecovery(path, &got, backupDir)
	if err != ErrFileCorrupted {
		t.Fatalf("err = %v, want ErrFileCorrupted", err)
	}
	// 原文件应被移动（不存在）
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("损坏文件应被隔离（原路径应不存在）")
	}
	// 隔离目录应存在且包含原文件
	info, err := os.Stat(backupDir)
	if err != nil {
		t.Fatalf("隔离目录应存在: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("隔离目录应为目录")
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bad.json" {
		t.Errorf("隔离目录内容 = %v, want [bad.json]", entries)
	}
}

// TestLoadWithRecovery_NonExistent 不存在文件返回 nil（非错误）。
func TestLoadWithRecovery_NonExistent(t *testing.T) {
	var got struct{ Name string }
	backupDir := filepath.Join(t.TempDir(), "corrupt")
	err := LoadWithRecovery(filepath.Join(t.TempDir(), "nope.json"), &got, backupDir)
	if err != nil {
		t.Errorf("err = %v, want nil（不存在文件非错误）", err)
	}
}

// TestLoadWithRecovery_EmptyPath 空路径返回 nil（非错误）。
func TestLoadWithRecovery_EmptyPath(t *testing.T) {
	var got struct{ Name string }
	if err := LoadWithRecovery("", &got, t.TempDir()); err != nil {
		t.Errorf("err = %v, want nil（空路径非错误）", err)
	}
}

// TestLoadWithRecovery_EmptyFile 空文件（0 字节）返回 nil（视为无数据，非损坏）。
func TestLoadWithRecovery_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	var got struct{ Name string }
	backupDir := filepath.Join(dir, "corrupt")
	if err := LoadWithRecovery(path, &got, backupDir); err != nil {
		t.Errorf("err = %v, want nil（空文件视为无数据）", err)
	}
	// 不应隔离
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Errorf("空文件不应隔离")
	}
}

// TestLoadWithRecovery_MkdirFail 隔离目录创建失败时返回组合错误（不 panic）。
func TestLoadWithRecovery_MkdirFail(t *testing.T) {
	// 用一个已存在文件作为 backupDir（MkdirAll 应失败）
	dir := t.TempDir()
	blockerPath := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blockerPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(srcPath, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	var got struct{ Name string }
	// backupDir 是一个已存在的文件路径，MkdirAll 应失败
	err := LoadWithRecovery(srcPath, &got, blockerPath)
	if err == nil {
		t.Fatal("err = nil, want non-nil（MkdirAll 失败应返回错误）")
	}
	if err == ErrFileCorrupted {
		t.Errorf("不应返回 ErrFileCorrupted（隔离失败时应返回组合错误）")
	}
}

// TestLoadJSONLWithRecovery_Valid 正常 JSONL 逐行处理，无跳过。
func TestLoadJSONLWithRecovery_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.jsonl")
	content := `{"id":1}
{"id":2}
{"id":3}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var ids []int
	handler := func(line []byte) error {
		var item struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		ids = append(ids, item.ID)
		return nil
	}
	skipped, err := LoadJSONLWithRecovery(path, handler, filepath.Join(dir, "corrupt"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[2] != 3 {
		t.Errorf("ids = %v, want [1 2 3]", ids)
	}
}

// TestLoadJSONLWithRecovery_CorruptedLines 损坏行跳过，正常行保留。
func TestLoadJSONLWithRecovery_CorruptedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	content := `{"id":1}
{broken line
{"id":2}
also broken
{"id":3}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var ids []int
	handler := func(line []byte) error {
		var item struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		ids = append(ids, item.ID)
		return nil
	}
	skipped, err := LoadJSONLWithRecovery(path, handler, filepath.Join(dir, "corrupt"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[2] != 3 {
		t.Errorf("ids = %v, want [1 2 3]", ids)
	}
}

// TestLoadJSONLWithRecovery_EmptyFile 空文件返回 (0, nil)，handler 不被调用。
func TestLoadJSONLWithRecovery_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	called := false
	handler := func(line []byte) error {
		called = true
		return nil
	}
	skipped, err := LoadJSONLWithRecovery(path, handler, filepath.Join(dir, "corrupt"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if called {
		t.Errorf("handler 不应被调用（空文件）")
	}
}

// TestLoadJSONLWithRecovery_CRLF Windows 换行符 (\r\n) 兼容。
func TestLoadJSONLWithRecovery_CRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.jsonl")
	content := "{\"id\":1}\r\n{\"id\":2}\r\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var ids []int
	handler := func(line []byte) error {
		var item struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		ids = append(ids, item.ID)
		return nil
	}
	skipped, err := LoadJSONLWithRecovery(path, handler, filepath.Join(dir, "corrupt"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %v, want 2 items", ids)
	}
}

// TestLoadJSONLWithRecovery_NonExistent 不存在文件返回 (0, nil)。
func TestLoadJSONLWithRecovery_NonExistent(t *testing.T) {
	handler := func(line []byte) error { return nil }
	skipped, err := LoadJSONLWithRecovery(filepath.Join(t.TempDir(), "nope.jsonl"), handler, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
}

// TestRepairCorrupted_NoCorrupted 无损坏文件时返回空列表。
func TestRepairCorrupted_NoCorrupted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), []byte(`{"a":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok2.json"), []byte(`{"b":2}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RepairCorrupted(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty（无损坏文件）", got)
	}
}

// TestRepairCorrupted_WithCorrupted 有损坏文件被隔离，正常文件保留。
func TestRepairCorrupted_WithCorrupted(t *testing.T) {
	dir := t.TempDir()
	goodPath := filepath.Join(dir, "ok.json")
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(goodPath, []byte(`{"a":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RepairCorrupted(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got = %v, want 1 项", got)
	}
	if !strings.HasSuffix(got[0], "bad.json") {
		t.Errorf("got[0] = %q, want 以 bad.json 结尾", got[0])
	}
	// bad.json 应被移动
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Errorf("bad.json 应被隔离（原路径不存在）")
	}
	// ok.json 应保留
	if _, err := os.Stat(goodPath); err != nil {
		t.Errorf("ok.json 应保留: %v", err)
	}
	// 隔离目录应存在
	corruptDir := filepath.Join(dir, "corrupt")
	if _, err := os.Stat(corruptDir); err != nil {
		t.Errorf("隔离目录应存在: %v", err)
	}
}

// TestRepairCorrupted_NestedDirs 递归扫描子目录中的损坏文件。
func TestRepairCorrupted_NestedDirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sessions", "v1")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(sub, "nested_bad.json")
	if err := os.WriteFile(badPath, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RepairCorrupted(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got = %v, want 1 项", got)
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Errorf("嵌套损坏文件应被隔离")
	}
}

// TestRepairCorrupted_SkipsCorruptDir 不扫描隔离目录（避免重复隔离）。
func TestRepairCorrupted_SkipsCorruptDir(t *testing.T) {
	dir := t.TempDir()
	// 预先创建 corrupt 目录并放入"损坏"文件（模拟已隔离文件）
	corruptSub := filepath.Join(dir, "corrupt", "20260101-000000")
	if err := os.MkdirAll(corruptSub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptSub, "already.json"), []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RepairCorrupted(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 不应再次隔离已隔离的文件
	if len(got) != 0 {
		t.Errorf("got = %v, want empty（不应扫描 corrupt 目录）", got)
	}
}

// TestRepairCorrupted_EmptyDir 空路径返回 nil。
func TestRepairCorrupted_EmptyDir(t *testing.T) {
	got, err := RepairCorrupted("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("got = %v, want nil", got)
	}
}

// TestRepairCorrupted_NonExistent 不存在的目录返回 nil。
func TestRepairCorrupted_NonExistent(t *testing.T) {
	got, err := RepairCorrupted(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("got = %v, want nil", got)
	}
}

// TestRepairCorrupted_SkipsBakFiles 跳过 .bak 文件（备份文件不隔离）。
func TestRepairCorrupted_SkipsBakFiles(t *testing.T) {
	dir := t.TempDir()
	// .bak 文件即使是损坏的也不应被隔离（备份文件）
	bakPath := filepath.Join(dir, "config.json.bak")
	if err := os.WriteFile(bakPath, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RepairCorrupted(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty（.bak 不应被扫描）", got)
	}
	// .bak 应保留
	if _, err := os.Stat(bakPath); err != nil {
		t.Errorf(".bak 文件应保留: %v", err)
	}
}

// TestRepairCorrupted_SkipsEmptyFiles 跳过空文件（0 字节非损坏）。
func TestRepairCorrupted_SkipsEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(emptyPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RepairCorrupted(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty（空文件非损坏）", got)
	}
	// 空文件应保留
	if _, err := os.Stat(emptyPath); err != nil {
		t.Errorf("空文件应保留: %v", err)
	}
}

// TestIsJSONFileCorrupted_Valid 正常 JSON 返回 false。
func TestIsJSONFileCorrupted_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if IsJSONFileCorrupted(path) {
		t.Errorf("正常 JSON 应返回 false")
	}
}

// TestIsJSONFileCorrupted_Corrupted 损坏 JSON 返回 true。
func TestIsJSONFileCorrupted_Corrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsJSONFileCorrupted(path) {
		t.Errorf("损坏 JSON 应返回 true")
	}
}

// TestIsJSONFileCorrupted_Unreadable 不可读文件返回 false（不归类为损坏）。
func TestIsJSONFileCorrupted_Unreadable(t *testing.T) {
	// 不存在文件：读取失败，返回 false
	if IsJSONFileCorrupted(filepath.Join(t.TempDir(), "nope.json")) {
		t.Errorf("不可读文件应返回 false（不归类为损坏）")
	}
}
