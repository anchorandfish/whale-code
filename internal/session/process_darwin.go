//go:build darwin

package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// getProcessStartTimeOS 返回 macOS 进程启动时间的毫秒时间戳。
// 使用 x/sys/unix 的 SysctlKinfoProc 读取系统定义的 kinfo_proc，避免依赖
// Go 新版本已不再导出的 syscall.SYS_SYSCTL 以及不稳定的结构体硬编码偏移。
func getProcessStartTimeOS(pid int) (int64, error) {
	kinfo, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	if kinfo == nil || int(kinfo.Proc.P_pid) != pid {
		return 0, fmt.Errorf("process %d not found", pid)
	}

	tvSec := int64(kinfo.Proc.P_starttime.Sec)
	tvUsec := int64(kinfo.Proc.P_starttime.Usec)

	// Sanity: tvSec should be a plausible Unix timestamp (after 2001-01-01,
	// the darwin epoch reference). If it's clearly wrong, fall back to a
	// weak identifier rather than returning garbage.
	if tvSec < 978307200 { // 2001-01-01 00:00:00 UTC
		return 0, fmt.Errorf("implausible p_starttime %d for pid %d", tvSec, pid)
	}
	// Combine sec + usec into a millisecond timestamp for comparability.
	return tvSec*1000 + tvUsec/1000, nil
}
