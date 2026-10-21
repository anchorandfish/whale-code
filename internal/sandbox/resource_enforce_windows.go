//go:build windows

// Package sandbox — Windows 资源强制限制平台实现（/ / P2-GAP）。
//
// Windows 无 POSIX setrlimit(RLIMIT_NOFILE/RLIMIT_FSIZE) 原生等效：
//
//	[GAP-WIN-P2: fd 限制] → 无直接 syscall；间接方案：
//
// - JOB_OBJECT_LIMIT_JOB_MEMORY（P0 已实现）收敛内核句柄池占用。
// - 此处打 canary 降级日志（same-level sub-cap degrade），
// 保证用户 FileDescriptorMax 配置意图可观测（B4-2 不静默）。
//
//	[GAP-WIN-P2: 文件大小限制] → 无直接 syscall；间接方案：
//
// - NTFS Quota（需管理员，全局不可行）/ JobObjectIoRateControlInformation(Win10+)
// 限制写入速度（MB/s 吞吐限制替代单文件字节上限）。
// - 同样 canary 降级可观测。
//
// 其它资源（Memory / Process Count / CPU Rate）由 Job Object 在 Enter() 阶段
// 通过 SetInformationJobObject 预先应用，不属于本函数范围。
//
// B5-1 降级约定：本函数永不返回 error（fail-open），仅在配置意图无法原生满足时
// 打 AuditLog.LogDegradation（from==to 表示 same-level 子能力降级，而非安全层级跌落）。
package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
)

func platformApplyResourceLimits(cmd *exec.Cmd, limits ResourceLimits, audit *AuditLog) error {
	_ = cmd
	_ = runtime.GOOS

	if audit == nil {
		return nil
	}

	// GAP-WIN-P2 fd 限制：无 RLIMIT_NOFILE 等效。
	if limits.FileDescriptorMax > 0 {
		audit.LogDegradation(SandboxLevelIsolated, SandboxLevelIsolated,
			fmt.Sprintf("GAP-WIN-P2 FileDescriptorMax=%d not directly enforced on Windows; "+
				"indirect constraint: Job Object Memory limit constrains handle/kernel-pool usage "+
				"(see WindowsSandboxConfig doc comment / os_sandbox.go GAP section)",
				limits.FileDescriptorMax))
	}

	// GAP-WIN-P2 文件大小限制：无 RLIMIT_FSIZE 等效。
	if limits.FileSizeMaxBytes > 0 {
		audit.LogDegradation(SandboxLevelIsolated, SandboxLevelIsolated,
			fmt.Sprintf("GAP-WIN-P2 FileSizeMaxBytes=%d not directly enforced on Windows; "+
				"indirect constraint: Job Memory prevents OOM, "+
				"IORate (Win10+) limits write throughput (see setIORate canary)",
				limits.FileSizeMaxBytes))
	}

	// CPUMaxSeconds (总 CPU 秒预算)：由 Job Object 或 Manager 层面的 run-with-timeout 处理。
	// MemoryMaxBytes / ProcessMax：由 Job Object setExtendedLimits 在 Enter() 时预先写入。
	// CPU Rate（CPUMaxPercent）：由 Job Object setCPURate 在 Enter() 时预先写入。
	// → 此处不重复设置（重复调用 SetInformationJobObject 会覆盖而非叠加）。

	return nil // B5-1: fail-open，永远不返回错误
}
