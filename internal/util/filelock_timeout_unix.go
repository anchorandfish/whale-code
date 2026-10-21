//go:build !windows

// Package util — M23-05 文件锁超时（Unix 平台实现， SNAPSHOT）
package util

import (
	"errors"
	"os"
	"syscall"
)

func isLockContended(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)
}

// tryLockFile 非阻塞尝试加排他锁（Unix/Linux/macOS 实现， SNAPSHOT-1）。
//
// 使用 flock(2) 的 LOCK_EX | LOCK_NB 非阻塞模式：
// - 加锁成功返回 nil
// - 锁被占用返回 syscall.EAGAIN（或 EWOULDBLOCK）
// - 其他错误返回原 error
func tryLockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	// EAGAIN/EWOULDBLOCK 表示锁被占用（非错误，由调用方处理）
	return err
}
