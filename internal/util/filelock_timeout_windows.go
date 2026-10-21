//go:build windows

// Package util — M23-05 文件锁超时（Windows 平台实现， SNAPSHOT）
package util

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func isLockContended(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// tryLockFile 非阻塞尝试加排他锁（Windows 实现， SNAPSHOT-1）。
//
// 标志位经 lockFileWithFlags（filelock_windows.go 单一声明）发出，
// 必须带 LOCKFILE_FAIL_IMMEDIATELY (0x01)：LockFileEx 默认阻塞，
// 缺该位时「非阻塞尝试」会挂住调用方直到锁释放或超时 ——
// best-effort 语义会被悄悄破坏（批2 收敛此坑的根）。
// - 加锁成功返回 nil
// - 锁被占用返回 windows.ERROR_LOCK_VIOLATION
// - 其他错误返回原 error
func tryLockFile(f *os.File) error {
	const lockFileFailImmediately = 0x01
	const lockFileExclusiveLock = 0x02
	return lockFileWithFlags(f, lockFileExclusiveLock|lockFileFailImmediately)
}
