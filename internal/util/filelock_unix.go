//go:build !windows

package util

import (
	"os"
	"syscall"
)

// lockFile 对文件加排他锁（Unix/Linux/macOS 实现）
// 使用 flock(2) 进行跨进程独占锁定
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile 释放文件锁（Unix/Linux/macOS 实现）
// 使用 flock(2) 的 LOCK_UN 解锁
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
