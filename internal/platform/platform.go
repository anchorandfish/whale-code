// Package platform 提供跨平台路径处理与平台检测的集中式工具。
// 所有涉及路径处理的模块必须复用本包，不允许自实现（H6 §7.4 横切基础设施事实标准）。
package platform

import (
	"os"
	"runtime"
)

// OS 返回当前操作系统标识："windows" | "linux" | "darwin"
func OS() string {
	return runtime.GOOS
}

// IsWindows 判断是否为 Windows（编译期 + 运行时双重确认）
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

// IsDarwin 判断是否为 macOS
func IsDarwin() bool {
	return runtime.GOOS == "darwin"
}

// IsLinux 判断是否为 Linux
func IsLinux() bool {
	return runtime.GOOS == "linux"
}

// CaseInsensitiveFS 判断当前文件系统是否默认不区分大小写。
// Windows NTFS 默认 case-insensitive（true）；
// macOS HFS+/APFS 默认 case-insensitive 但可配置 case-sensitive（默认 true）；
// Linux ext4 默认 case-sensitive（false）。
// 用于 PathEqual 的平台感知决策。
// 可通过环境变量 WHALE_CASE_SENSITIVE=1 覆盖（边缘场景）。
func CaseInsensitiveFS() bool {
	if os.Getenv("WHALE_CASE_SENSITIVE") == "1" {
		return false
	}
	return IsWindows() || IsDarwin()
}
