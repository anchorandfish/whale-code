// Package sandbox — M13-02 资源强制限制默认平台实现（/）。
//
// 本文件仅包含非 Linux/非 macOS 平台的 platformApplyResourceLimits 默认实现。
// 类型定义、公共函数（ResourceLimits/DefaultResourceLimits/ApplyResourceLimits）、
// AuditIndex、RunWithTimeout 已移至 resource_enforce_common.go（无 build tag，全平台编译）。
//
// 平台特定实现：
// - resource_enforce_linux.go (//go:build linux)
// - resource_enforce_darwin.go (//go:build darwin)
//
// 官方源对标：
// - Linux rlimit: https://man7.org/linux/man-pages/man2/setrlimit.2.html @2026-07-21
// - Windows Job Object: https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects @2026-07-21

//go:build !linux && !darwin && !windows

package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
)

// platformApplyResourceLimits 是平台特定的资源限制应用函数（默认实现）。
//
// 默认实现（本文件）：no-op + AuditLog 记录降级。
// Linux/macOS 平台用 build tag 覆盖：
// - resource_enforce_linux.go (//go:build linux) — 使用 unix.Prlimit
// - resource_enforce_darwin.go (//go:build darwin) — 降级 no-op（用 Seatbelt profile）
//
// Windows 不实现本函数（依赖 Job Object 在 os_sandbox_windows.go）。
//
// 参数：
// - cmd: 已启动的 exec.Cmd（cmd.Process != nil）
// - limits: 资源上限配置
// - audit: 审计日志实例（可为 nil）
func platformApplyResourceLimits(cmd *exec.Cmd, limits ResourceLimits, audit *AuditLog) error {
	if audit != nil {
		audit.LogDegradation(SandboxLevelIsolated, SandboxLevelRelaxed,
			fmt.Sprintf("resource limits not enforced on %s (no-op)", runtime.GOOS))
	}
	return nil
}
