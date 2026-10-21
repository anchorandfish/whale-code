// Package sandbox — 其他平台（非 Linux/Windows/darwin）的降级实现（/ T1-P1-B9）。
//
// 本文件提供 newPlatformSandbox 的 fallback 实现，仅在不支持 OS 级隔离的平台编译
// （如 FreeBSD/OpenBSD/NetBSD 等）。返回 degradedSandbox。
//
// build tag：!linux && !windows && !darwin
//
// 平台文件覆盖关系：
// - os_sandbox_linux.go (//go:build linux) → linuxNamespaceSandbox
// - os_sandbox_windows.go (//go:build windows) → windowsJobSandbox
// - os_sandbox_darwin.go (//go:build darwin) → darwinSeatbeltSandbox
// - 本文件 (//go:build !linux && !windows && !darwin) → degradedSandbox
//
//go:build !linux && !windows && !darwin

package sandbox

// newPlatformSandbox 是其他平台的降级工厂实现。
//
// 始终返回 degradedSandbox，记录"unsupported platform"原因。
// 调用方（Manager.Prepare）会得到 nil 句柄，与 P0 行为一致。
func newPlatformSandbox(cfg OSLevelConfig, audit *AuditLog) OSSandbox {
	_ = cfg // 显式忽略：其他平台不支持任何 OS 级隔离
	return newDegradedSandbox("unsupported platform for OS-level sandboxing", audit)
}

// describeIsolationFeatures 其他平台无 OS 级隔离特性（门 2 降级可见性；
// IsolationStatusOf 对 degraded 平台不会调用本函数，此处仅为编译完整性）。
func describeIsolationFeatures(cfg OSLevelConfig) []string { return nil }

// isLinuxLandlockBuild 报告当前构建是否含 Landlock 路径（仅 linux）。
func isLinuxLandlockBuild() bool { return false }
