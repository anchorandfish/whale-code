//go:build windows

package util

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileWithFlags 是 Windows LockFileEx 的**单一声明**（批2 ·）。
//
// 病灶记录：原 lockFile（filelock_windows.go）与 tryLockFile
// （filelock_timeout_windows.go）各自拼一份标志位调同一 syscall ——
// 同一把锁两套实现。tryLockFile 缺「立刻失败」位时症状最隐蔽：
// best-effort 调用方阻塞等满超时后被当成「锁不可用」继续写。
// 收敛后两个入口只是薄转发，标志位只有这一处可改。
func lockFileWithFlags(f *os.File, flags uint32) error {
	var ol windows.Overlapped
	// 锁定范围 0xFFFFFFFF:0xFFFFFFFF 表示整个文件
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		&ol,
	)
}

// lockFile 对文件加阻塞式排他锁（WithFileLock 用）。
func lockFile(f *os.File) error {
	return lockFileWithFlags(f, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

// unlockFile 释放文件锁（Windows 实现）
// 使用 UnlockFileEx 释放之前通过 LockFileEx 加的排他锁
func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		&ol,
	)
}
