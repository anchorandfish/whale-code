// Package util 提供跨进程文件锁能力。
// Windows 用 LockFileEx，Unix 用 flock，语义一致（独占锁）。
package util

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrFileLocked = errors.New("file is already locked")

// HeldFileLock is an OS lock whose lifetime is controlled explicitly by the
// caller. The backing file must not be removed while any process may hold it.
type HeldFileLock struct {
	file *os.File
	once sync.Once
	err  error
}

// TryAcquireFileLock acquires an exclusive cross-process lock without waiting.
func TryAcquireFileLock(filePath string) (*HeldFileLock, error) {
	if err := os.MkdirAll(filepath.Dir(filePath), LockDirPerm); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLockFile(f); err != nil {
		_ = f.Close()
		if isLockContended(err) {
			return nil, fmt.Errorf("%w: %v", ErrFileLocked, err)
		}
		return nil, fmt.Errorf("acquire file lock: %w", err)
	}
	return &HeldFileLock{file: f}, nil
}

// WriteMetadata replaces and fsyncs diagnostic lock metadata while retaining
// the OS lock on the same file handle.
func (l *HeldFileLock) WriteMetadata(data []byte) error {
	if l == nil || l.file == nil {
		return errors.New("file lock is not held")
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := l.file.Write(data); err != nil {
		return err
	}
	return l.file.Sync()
}

func (l *HeldFileLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file == nil {
			return
		}
		if err := unlockFile(l.file); err != nil {
			l.err = err
		}
		if err := l.file.Close(); l.err == nil && err != nil {
			l.err = err
		}
		l.file = nil
	})
	return l.err
}

// WithFileLock 在持锁状态下执行函数 fn。
// 打开（或创建）指定文件，加排他锁后执行 fn，执行完毕后释放锁并关闭文件。
// 锁文件权限 0600，避免同机其他用户读取；**锁目录**权限取唯一声明
// `LockDirPerm`（F11 L2 —— 修复前此处 0755 与 TryAcquireFileLock 的 0700 不一致，
// 最终权限取决于谁先 MkdirAll）。
func WithFileLock(filePath string, fn func(*os.File) error) error {
	// 确保锁文件所在目录存在
	if err := os.MkdirAll(filepath.Dir(filePath), LockDirPerm); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}

	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer unlockFile(f)

	return fn(f)
}
