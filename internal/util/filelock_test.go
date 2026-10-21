package util

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestWithFileLock_BasicLock 基本加锁：fn 被执行且文件可读写
func TestWithFileLock_BasicLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.txt")
	called := false
	err := WithFileLock(path, func(f *os.File) error {
		called = true
		// 验证可以写入
		_, err := f.Write([]byte("locked"))
		return err
	})
	if err != nil {
		t.Fatalf("WithFileLock err: %v", err)
	}
	if !called {
		t.Error("fn 未被调用")
	}
	// 验证内容写入成功
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != "locked" {
		t.Errorf("content = %q, want %q", got, "locked")
	}
}

// TestWithFileLock_CreatesLockFile 锁文件被创建
func TestWithFileLock_CreatesLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "lock.txt")
	err := WithFileLock(path, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithFileLock err: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("锁文件未创建: %v", err)
	}
}

// TestWithFileLock_LockFilePermissions 锁文件权限 0600
func TestWithFileLock_LockFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 POSIX 权限位（Go 建文件恒 0666），0600 断言仅适用 POSIX")
	}
	path := filepath.Join(t.TempDir(), "perm_lock.txt")
	err := WithFileLock(path, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithFileLock err: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat err: %v", err)
	}
	// os.OpenFile 使用 0600 创建
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
}

// TestWithFileLock_CreatesParentDir 自动创建锁文件父目录
func TestWithFileLock_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "dir", "lock.txt")
	err := WithFileLock(path, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithFileLock err: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("锁文件未创建: %v", err)
	}
}

// TestWithFileLock_FnError fn 返回错误时锁被释放
func TestWithFileLock_FnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "err_lock.txt")
	expectedErr := os.ErrClosed
	err := WithFileLock(path, func(f *os.File) error {
		return expectedErr
	})
	if err != expectedErr {
		t.Errorf("err = %v, want %v", err, expectedErr)
	}
	// 锁应已释放，可以再次获取
	err = WithFileLock(path, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Errorf("第二次加锁失败（锁未释放）: %v", err)
	}
}

// TestWithFileLock_ConcurrentSameFile 并发对同一文件加锁：第二个应等待
func TestWithFileLock_ConcurrentSameFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows LockFileEx 行为不同，跳过")
	}
	path := filepath.Join(t.TempDir(), "concurrent.txt")
	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	acquired := make(chan struct{})
	release := make(chan struct{})

	// 第一个 goroutine 获取锁并持有
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = WithFileLock(path, func(f *os.File) error {
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			close(acquired)
			<-release
			return nil
		})
	}()

	<-acquired

	// 第二个 goroutine 尝试获取锁（应阻塞）
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = WithFileLock(path, func(f *os.File) error {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			return nil
		})
	}()

	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("order len = %d, want 2", len(order))
	}
	if order[0] != 1 || order[1] != 2 {
		t.Errorf("order = %v, want [1, 2]（第二个应等待第一个释放）", order)
	}
}

// TestWithFileLock_ConcurrentDifferentFiles 并发对不同文件加锁：无阻塞
func TestWithFileLock_ConcurrentDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	const goroutines = 20
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := filepath.Join(dir, "lock_"+time.Now().Format("150405.000000")+".txt")
			errs[idx] = WithFileLock(path, func(f *os.File) error {
				_, err := f.Write([]byte("data"))
				return err
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// TestWithFileLock_InvalidPath 无效路径返回错误
func TestWithFileLock_InvalidPath(t *testing.T) {
	// 跨平台构造无效路径：用一个**文件**占据父目录名，使 `dir/sub/lock.txt`
	// 的父目录创建必然失败（替代 POSIX 专属的 /dev/null/sub/...）。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	err := WithFileLock(filepath.Join(blocker, "sub", "lock.txt"), func(f *os.File) error {
		return nil
	})
	if err == nil {
		t.Error("err = nil, want non-nil（无效路径应报错）")
	}
}
