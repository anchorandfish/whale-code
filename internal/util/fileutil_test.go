package util

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestAtomicWriteFile_BasicWrite 基本写入：写入后读取内容一致
func TestAtomicWriteFile_BasicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	data := []byte("hello world")
	if err := AtomicWriteFile(path, data, 0644); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", got, data)
	}
}

// TestAtomicWriteFile_OverwriteExisting 覆盖已有文件
func TestAtomicWriteFile_OverwriteExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overwrite.txt")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	newData := []byte("new content")
	if err := AtomicWriteFile(path, newData, 0644); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != string(newData) {
		t.Errorf("content = %q, want %q", got, newData)
	}
}

// TestAtomicWriteFile_Permissions 验证文件权限正确设置
func TestAtomicWriteFile_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 无 POSIX 权限位语义（/ 约束 C3）：Go 的 0600 在 Windows
		// 只影响只读位，新建文件 perm 恒 0666。这是平台语义差异，不是缺陷；
		// 生产码在 Windows 上无需（也无法）兑现 0600。Skip ≠ 通过：该平台上
		// 权限断言物理不可判别。
		t.Skip("Windows 无 POSIX 权限位语义：新建文件 perm 恒 0666")
	}
	path := filepath.Join(t.TempDir(), "perm.txt")
	if err := AtomicWriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat err: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
}

// TestAtomicWriteFile_EmptyData 写入空数据
func TestAtomicWriteFile_EmptyData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := AtomicWriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat err: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("size = %d, want 0", info.Size())
	}
}

// TestAtomicWriteFile_LargeData 写入大数据（1MB）
func TestAtomicWriteFile_LargeData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	data := []byte(strings.Repeat("A", 1024*1024))
	if err := AtomicWriteFile(path, data, 0644); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if len(got) != len(data) {
		t.Errorf("len = %d, want %d", len(got), len(data))
	}
}

// TestAtomicWriteFile_CreatesParentDir 自动创建父目录
func TestAtomicWriteFile_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "test.txt")
	if err := AtomicWriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

// TestAtomicWriteFile_NoTempFileLeak 验证临时文件被清理
func TestAtomicWriteFile_NoTempFileLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.txt")
	if err := AtomicWriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("AtomicWriteFile err: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir err: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("临时文件未清理: %s", e.Name())
		}
	}
}

// TestAtomicWriteFileNoFsync_BasicWrite 无 fsync 版本基本写入
func TestAtomicWriteFileNoFsync_BasicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nofsync.txt")
	data := []byte("no fsync data")
	if err := AtomicWriteFileNoFsync(path, data, 0644); err != nil {
		t.Fatalf("AtomicWriteFileNoFsync err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", got, data)
	}
}

// TestAtomicWriteFile_ConcurrentSafe 多 goroutine 并发写入不同文件
func TestAtomicWriteFile_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const goroutines = 50
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := filepath.Join(dir, "file_"+strconv.Itoa(idx)+".txt")
			data := []byte(strings.Repeat("x", 100))
			errs[idx] = AtomicWriteFile(path, data, 0644)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// TestAtomicWriteFile_Idempotent 多次写入同一文件结果一致
func TestAtomicWriteFile_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.txt")
	data := []byte("same content")
	for i := 0; i < 5; i++ {
		if err := AtomicWriteFile(path, data, 0644); err != nil {
			t.Fatalf("第 %d 次写入 err: %v", i, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", got, data)
	}
}

// TestAtomicWriteFile_InvalidPath 无效路径写入返回错误
func TestAtomicWriteFile_InvalidPath(t *testing.T) {
	// ★ ：跨平台「不可创建路径」构造法 —— 用一个**文件**占住目录名的位置，
	// MkdirAll 必失败（POSIX ENOTDIR / Windows 同样拒绝）。旧实现用 /dev/null/sub/…
	// 是 POSIX 专属：Windows 上 /dev 解析到当前盘根，目录可创建 ⇒ err=nil 假红。
	blocker := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(blocker, "sub", "test.txt")
	err := AtomicWriteFile(badPath, []byte("x"), 0644)
	if err == nil {
		t.Error("err = nil, want non-nil（无效路径应报错）")
	}
}

// TestAtomicWriteFileNoFsync_OverwriteExisting 无 fsync 版本覆盖已有文件
func TestAtomicWriteFileNoFsync_OverwriteExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nofsync_overwrite.txt")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	newData := []byte("new")
	if err := AtomicWriteFileNoFsync(path, newData, 0644); err != nil {
		t.Fatalf("AtomicWriteFileNoFsync err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != string(newData) {
		t.Errorf("content = %q, want %q", got, newData)
	}
}

// TestAtomicWriteFileNoFsync_NoTempFileLeak 无 fsync 版本临时文件清理
func TestAtomicWriteFileNoFsync_NoTempFileLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nofsync_clean.txt")
	if err := AtomicWriteFileNoFsync(path, []byte("x"), 0644); err != nil {
		t.Fatalf("AtomicWriteFileNoFsync err: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir err: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("临时文件未清理: %s", e.Name())
		}
	}
}
