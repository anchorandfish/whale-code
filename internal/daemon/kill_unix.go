//go:build !windows

package daemon

import (
	"syscall"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
)

const processTreeGracePeriod = 300 * time.Millisecond

// killProcessTree 在 Unix 平台上杀死指定进程所在的整个进程组及其子进程。
// 先向进程组发送 SIGTERM，给短暂宽限时间；仍存活则对整个进程组发送
// SIGKILL。进程组由 sandbox.StartCommandWithProcessGroup 建立。
func killProcessTree(pid int) error {
	if pid <= 0 {
		return syscall.EINVAL
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		// The command may not have reached process-group setup. Kill the direct
		// process as a final fallback.
		if err2 := syscall.Kill(pid, syscall.SIGKILL); err2 != nil && err2 != syscall.ESRCH {
			return err2
		}
		return nil
	}

	deadline := time.Now().Add(processTreeGracePeriod)
	for time.Now().Before(deadline) {
		if !platform.ProcessGroupAlive(pid) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
