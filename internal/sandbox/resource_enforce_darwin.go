// Package sandbox — macOS 资源限制实现（P1-1 / v1 门 1 修复）。
//
// macOS 不支持 prlimit(2) 作用于其他进程，setrlimit 仅影响调用进程；
// 资源限制由 __whale-rlimit re-exec helper 在 exec 前应用
//（见 os_sandbox_darwin_rlimit.go），本文件 post-start 阶段为 no-op。
//
// 官方源：https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setrlimit.2.html @2026-07-21

//go:build darwin

package sandbox

import (
	"os/exec"
)

// platformApplyResourceLimits 在 macOS 上为 no-op（v1 门 1 修复）。
//
// macOS 不支持 prlimit(2) 作用于其他进程；资源限制已由 __whale-rlimit
// re-exec helper 在 exec 前应用（见 os_sandbox_darwin_rlimit.go，
// CPU/NOFILE/FSIZE 生效；AS/NPROC 平台限制跳过 + canary）。
// 此处不再记降级日志：限制已正确应用，post-start 阶段无事可做。
// （修复前：此处注释错误指向不存在的 Seatbelt (limit) rlimit 能力，
// 且每条命令打一条误导性降级日志。）
func platformApplyResourceLimits(cmd *exec.Cmd, limits ResourceLimits, audit *AuditLog) error {
	// 正常路径：helper 已包装（cmd.Args[1] == __whale-rlimit），限制已应用
	if cmd != nil && len(cmd.Args) > 1 && cmd.Args[1] == RlimitHelperArg {
		return nil
	}
	// 分歧 canary（对抗式审查三-❌ 修复）：配置了顶层 ResourceLimits 但
	// helper 未包装（OSLevel.Darwin.ResourceLimits 未设置/零值）→
	// macOS 无资源限制；此分支堵死"零限制且完全静默"的通道。
	if limits != (ResourceLimits{}) && audit != nil {
		audit.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict,
			"darwin rlimit divergence: ResourceLimits configured but __whale-rlimit helper not applied (OSLevel.Darwin.ResourceLimits unset?)")
	}
	return nil
}
