// Package util — M23-05 文件锁超时（SNAPSHOT）
//
// 本文件为 WithFileLock 增加超时能力，避免死锁。
// 对标同类 CLI fs2 超时（https://github.com/async-rs/async-std）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：WithFileLockTimeout 函数，带 timeout 参数
// - 结构体字段：LockTimeoutError 含 Path/Timeout/Waited
// - 分层权责：本文件只做"超时控制"，锁实现复用 filelock_unix/windows.go
// - 降级策略：timeout=0 时阻塞等待（与 WithFileLock 一致）
// - TTL规则：超时后返回 LockTimeoutError（不阻塞调用方）
package util

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// defaultLockPollInterval 是超时模式的锁轮询间隔（50ms，平衡 CPU 与响应速度）。
const defaultLockPollInterval = 50 * time.Millisecond

// LockTimeoutError 是文件锁超时错误（SNAPSHOT-1）。
type LockTimeoutError struct {
	Path    string        // 锁文件路径
	Timeout time.Duration // 超时时间
	Waited  time.Duration // 实际等待时间
}

// Error 实现 error 接口（SNAPSHOT-2）。
func (e *LockTimeoutError) Error() string {
	return fmt.Sprintf("file lock timeout: path=%s timeout=%s waited=%s", e.Path, e.Timeout, e.Waited)
}

// Is 实现 errors.Is 支持（SNAPSHOT-3）。
//
// 允许调用方用 errors.Is(err, &LockTimeoutError{}) 判断是否超时。
func (e *LockTimeoutError) Is(target error) bool {
	_, ok := target.(*LockTimeoutError)
	return ok
}

// WithFileLockTimeout 在持锁状态下执行函数 fn，带超时控制（SNAPSHOT-4）。
//
// 行为：
// - timeout=0 时阻塞等待（与 WithFileLock 一致，向后兼容）
// - timeout>0 时尝试加锁，超时返回 *LockTimeoutError
// - 锁文件权限 0600，避免同机其他用户读取
//
// 参数：
// - filePath: 锁文件路径
// - timeout: 超时时间（0=阻塞等待）
// - fn: 持锁后执行的函数
//
// 返回：
// - 超时：*LockTimeoutError
// - 其他错误：原错误
func WithFileLockTimeout(filePath string, timeout time.Duration, fn func(*os.File) error) error {
	// 确保锁文件所在目录存在（权限取唯一声明 LockDirPerm，见 lock_verdict.go F11 L2）
	if err := os.MkdirAll(filepath.Dir(filePath), LockDirPerm); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}

	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()

	// timeout=0 时阻塞等待（向后兼容）
	if timeout <= 0 {
		if err := lockFile(f); err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}
		defer unlockFile(f)
		return fn(f)
	}

	// timeout>0 时尝试加锁，超时返回错误
	deadline := time.Now().Add(timeout)
	tryLock := func() error {
		return lockFileNonBlocking(f)
	}

	// 首次尝试
	if err := tryLock(); err == nil {
		defer unlockFile(f)
		return fn(f)
	}

	// 轮询尝试
	ticker := time.NewTicker(defaultLockPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				waited := time.Since(deadline.Add(-timeout))
				return &LockTimeoutError{
					Path:    filePath,
					Timeout: timeout,
					Waited:  waited,
				}
			}
			if err := tryLock(); err == nil {
				defer unlockFile(f)
				return fn(f)
			}
		}
	}
}

// lockFileNonBlocking 是非阻塞加锁尝试（SNAPSHOT-5）。
//
// 返回 nil 表示加锁成功，返回 error 表示锁被占用或失败。
// 内部调用 tryLockFile（平台实现）。
func lockFileNonBlocking(f *os.File) error {
	return tryLockFile(f)
}

// TryAcquireFileLockTimeout 带超时地获取一个跨进程排他锁，返回可持有的
// *HeldFileLock（调用方负责 Release）。与 WithFileLockTimeout 共享同一套语义：
// 50ms 轮询 + *LockTimeoutError（errors.Is 可判）。二者差异只在返回形态——
// 后者在持锁期间执行一个闭包，前者把锁「交出去」给调用方长期持有。
//
// - timeout <= 0 时退化为无限阻塞（与 TryAcquireFileLock 的语义不同，见下）。
// - 调用方**不得**传 0：0 在此处是「无限阻塞」，非「用默认」。需要默认值的
// 调用方应先自行归一化（见 network_fs.go 的 effectiveSessionLockTimeout）。
func TryAcquireFileLockTimeout(filePath string, timeout time.Duration) (*HeldFileLock, error) {
	// ★ 入口即拒 0：0 在此处是「无限阻塞」，非「用默认」。需要默认值的调用方应先
	// 自行归一化（见 network_fs.go 的 effectiveSessionLockTimeout）。在入口拒绝，
	// 而非等到锁被占用才发现 —— 否则锁空闲时静默成功、锁忙时才暴露，行为不一致。
	if timeout <= 0 {
		return nil, fmt.Errorf("acquire file lock timeout requires a positive timeout (got %v); refusing to block forever", timeout)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), LockDirPerm); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// 首次非阻塞尝试。
	if err := tryLockFile(f); err == nil {
		return &HeldFileLock{file: f}, nil
	} else if !isLockContended(err) {
		_ = f.Close()
		return nil, fmt.Errorf("acquire file lock: %w", err)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(defaultLockPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				waited := time.Since(deadline.Add(-timeout))
				_ = f.Close()
				return nil, &LockTimeoutError{Path: filePath, Timeout: timeout, Waited: waited}
			}
			if err := tryLockFile(f); err == nil {
				return &HeldFileLock{file: f}, nil
			} else if !isLockContended(err) {
				_ = f.Close()
				return nil, fmt.Errorf("acquire file lock: %w", err)
			}
		}
	}
}
