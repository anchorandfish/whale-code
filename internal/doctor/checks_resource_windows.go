//go:build windows

package doctor

import (
	"fmt"
	"syscall"
	"unsafe"
)

// memoryStatusEx 对应 Windows API MEMORYSTATUSEX 结构体。
// 用于 GlobalMemoryStatusEx 获取物理内存信息。
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64 // 总物理内存（字节）
	ullAvailPhys            uint64 // 可用物理内存（字节）
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// getMemoryInfo 获取系统内存信息（Windows 平台）。
// 使用 GlobalMemoryStatusEx 获取物理内存总量和可用量。
// 返回：总内存(MB) + 可用内存(MB) + 错误。
func getMemoryInfo() (totalMB, availMB int, err error) {
	var memInfo memoryStatusEx
	memInfo.dwLength = uint32(unsafe.Sizeof(memInfo))

	// 加载 kernel32.dll 并查找 GlobalMemoryStatusEx
	kernel32, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return 0, 0, fmt.Errorf("load kernel32.dll: %w", err)
	}
	defer kernel32.Release()

	proc, err := kernel32.FindProc("GlobalMemoryStatusEx")
	if err != nil {
		return 0, 0, fmt.Errorf("find GlobalMemoryStatusEx: %w", err)
	}

	ret, _, err := proc.Call(uintptr(unsafe.Pointer(&memInfo)))
	if ret == 0 {
		return 0, 0, fmt.Errorf("GlobalMemoryStatusEx failed: %w", err)
	}

	totalMB = int(memInfo.ullTotalPhys / 1024 / 1024)
	availMB = int(memInfo.ullAvailPhys / 1024 / 1024)
	return totalMB, availMB, nil
}

// getDiskInfo 获取指定路径所在磁盘的剩余空间（Windows 平台）。
// 使用 GetDiskFreeSpaceExW API 获取磁盘剩余空间。
// 返回：剩余空间(MB) + 错误。
func getDiskInfo(path string) (freeMB int, err error) {
	kernel32, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return 0, fmt.Errorf("load kernel32.dll: %w", err)
	}
	defer kernel32.Release()

	proc, err := kernel32.FindProc("GetDiskFreeSpaceExW")
	if err != nil {
		return 0, fmt.Errorf("find GetDiskFreeSpaceExW: %w", err)
	}

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("convert path to UTF16: %w", err)
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	ret, _, err := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW failed: %w", err)
	}

	freeMB = int(freeBytesAvailable / 1024 / 1024)
	return freeMB, nil
}
