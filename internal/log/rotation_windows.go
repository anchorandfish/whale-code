// Package log — O-1 Windows 平台磁盘空间检查实现（桶 X6）
//
// 在 Windows 系统上通过 syscall.GetDiskFreeSpaceEx 获取分区剩余空间。
// 本文件通过 build tag //go:build windows 限制仅在 Windows 平台编译。

//go:build windows

package log

import (
	"fmt"
	"syscall"
	"unsafe"
)

// init 注册 Windows 平台的 diskFreeBytes 实现。
// 通过 syscall.GetDiskFreeSpaceEx 获取指定目录所在分区的剩余字节数。
func init() {
	platformDiskFreeBytes = windowsDiskFreeBytes
}

// windowsDiskFreeBytes 返回指定目录所在分区的剩余字节数（Windows 实现）。
//
// 实现：
//   - 调用 kernel32.dll 的 GetDiskFreeSpaceEx API
//   - 返回 lpFreeBytesAvailable（普通用户可用剩余字节数）
//
// 参数：
//   - dir：已存在的目录路径（Windows 接受盘符或 UNC 路径）
//
// 返回：
//   - 剩余字节数
//   - 错误（目录不存在、权限不足等）
func windowsDiskFreeBytes(dir string) (int64, error) {
	// 加载 kernel32.dll
	kernel32, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return 0, fmt.Errorf("load kernel32.dll: %w", err)
	}
	defer kernel32.Release()

	// 查找 GetDiskFreeSpaceExW（Unicode 版本）
	proc, err := kernel32.FindProc("GetDiskFreeSpaceExW")
	if err != nil {
		return 0, fmt.Errorf("find GetDiskFreeSpaceExW: %w", err)
	}

	// 准备参数：目录路径（转为 UTF16 指针）+ 3 个 uint64 接收变量
	pathPtr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, fmt.Errorf("utf16 from %q: %w", dir, err)
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	// 调用 GetDiskFreeSpaceExW
	// BOOL GetDiskFreeSpaceExW(
	//   _In_opt_  LPCWSTR         lpDirectoryName,
	//   _Out_opt_ PULARGE_INTEGER lpFreeBytesAvailable,
	//   _Out_opt_ PULARGE_INTEGER lpTotalNumberOfBytes,
	//   _Out_opt_ PULARGE_INTEGER lpTotalNumberOfFreeBytes
	// );
	r1, _, err := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW(%q): %w", dir, err)
	}

	// freeBytesAvailable = 普通用户可用剩余字节数（不含配额外空间）
	return int64(freeBytesAvailable), nil
}
