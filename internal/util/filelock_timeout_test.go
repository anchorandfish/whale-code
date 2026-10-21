// Package util — M23-05 文件锁超时测试（SNAPSHOT）
//
// 测试覆盖：
// - WithFileLockTimeout 基本 fn 执行
// - timeout=0 阻塞模式（向后兼容）
// - timeout>0 立即获取成功
// - 超时返回 *LockTimeoutError
// - LockTimeoutError 的 Error/Is 方法
// - fn 返回错误时锁被释放
// - 并发场景：第二个调用方等待
package util

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWithFileLockTimeout_BasicLock 基本 fn 执行
func TestWithFileLockTimeout_BasicLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.txt")
	called := false
	err := WithFileLockTimeout(path, 5*time.Second, func(f *os.File) error {
		called = true
		_, err := f.Write([]byte("locked"))
		return err
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !called {
		t.Error("fn not called")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	if string(got) != "locked" {
		t.Errorf("content = %q, want %q", got, "locked")
	}
}

// TestWithFileLockTimeout_BlockingMode timeout=0 阻塞模式
func TestWithFileLockTimeout_BlockingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "block.txt")
	err := WithFileLockTimeout(path, 0, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Fatalf("blocking mode err: %v", err)
	}
}

// TestWithFileLockTimeout_ImmediateAcquire timeout>0 立即获取
func TestWithFileLockTimeout_ImmediateAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "immediate.txt")
	err := WithFileLockTimeout(path, 100*time.Millisecond, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Fatalf("immediate acquire err: %v", err)
	}
}

// TestWithFileLockTimeout_TimeoutReturnsError 超时返回 *LockTimeoutError
func TestWithFileLockTimeout_TimeoutReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows LockFileEx LOCKFILE_FAIL_IMMEDIATELY 行为不同，跳过超时测试")
	}

	path := filepath.Join(t.TempDir(), "timeout.txt")

	// 第一个 goroutine 持有锁
	acquired := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = WithFileLockTimeout(path, 0, func(f *os.File) error {
			close(acquired)
			<-release
			return nil
		})
	}()
	<-acquired

	// 第二个 goroutine 应超时
	start := time.Now()
	err := WithFileLockTimeout(path, 200*time.Millisecond, func(f *os.File) error {
		return nil
	})
	elapsed := time.Since(start)
	close(release)

	if err == nil {
		t.Fatal("err = nil, want LockTimeoutError")
	}
	var timeoutErr *LockTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err type = %T, want *LockTimeoutError", err)
	}
	if timeoutErr.Path != path {
		t.Errorf("Path = %q, want %q", timeoutErr.Path, path)
	}
	if timeoutErr.Timeout != 200*time.Millisecond {
		t.Errorf("Timeout = %v, want 200ms", timeoutErr.Timeout)
	}
	// 实际等待时间应 >= timeout（允许少许偏差）
	if elapsed < 150*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 150ms", elapsed)
	}
}

// TestWithFileLockTimeout_FnError fn 返回错误时锁被释放
func TestWithFileLockTimeout_FnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "err.txt")
	expectedErr := errors.New("custom error")
	err := WithFileLockTimeout(path, 0, func(f *os.File) error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Errorf("err = %v, want %v", err, expectedErr)
	}

	// 锁应已释放，可以再次获取
	err = WithFileLockTimeout(path, 5*time.Second, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Errorf("second lock failed (lock not released): %v", err)
	}
}

// TestLockTimeoutError_Error 验证 Error 方法格式
func TestLockTimeoutError_Error(t *testing.T) {
	e := &LockTimeoutError{
		Path:    "/tmp/foo",
		Timeout: 100 * time.Millisecond,
		Waited:  150 * time.Millisecond,
	}
	msg := e.Error()
	if msg == "" {
		t.Error("Error() returned empty string")
	}
	if !strings.Contains(msg, "/tmp/foo") {
		t.Errorf("Error() = %q, missing path", msg)
	}
}

// TestLockTimeoutError_Is 支持 errors.Is
func TestLockTimeoutError_Is(t *testing.T) {
	e1 := &LockTimeoutError{Path: "/a"}
	e2 := &LockTimeoutError{Path: "/b"}
	// 不同实例也应匹配（按类型匹配）
	if !errors.Is(e1, e2) {
		t.Error("errors.Is should match *LockTimeoutError types")
	}
	// 普通 error 不应匹配
	if errors.Is(e1, errors.New("plain")) {
		t.Error("errors.Is should not match plain error")
	}
}

// TestWithFileLockTimeout_ConcurrentSameFile 并发同文件：第二个等待第一个释放后获取
func TestWithFileLockTimeout_ConcurrentSameFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows LockFileEx 行为不同，跳过并发测试")
	}

	path := filepath.Join(t.TempDir(), "conc.txt")
	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	acquired := make(chan struct{})
	release := make(chan struct{})

	// 第一个 goroutine 获取锁后通过 channel 建立确定的先后关系。
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = WithFileLockTimeout(path, 0, func(f *os.File) error {
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			close(acquired)
			<-release
			return nil
		})
	}()

	<-acquired

	// 第二个 goroutine 应等待
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = WithFileLockTimeout(path, 5*time.Second, func(f *os.File) error {
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
		t.Errorf("order = %v, want [1, 2]", order)
	}
}

// TestWithFileLockTimeout_CreatesParentDir 自动创建父目录
func TestWithFileLockTimeout_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "lock.txt")
	err := WithFileLockTimeout(path, 0, func(f *os.File) error {
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file not created: %v", err)
	}
}
