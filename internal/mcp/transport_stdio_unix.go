//go:build !windows

package mcp

import (
	"fmt"
	"os/exec"
	"syscall"
)

// signalProcessGroupGraceful 向进程组发送优雅终止信号（SIGTERM）。
//
// 用于 stdio 三阶段清理的 Stage 2（M5/C11）：
//
//	Stage 1: stdin Close（EOF）
//	Stage 2: 本函数 — SIGTERM 进程组 + grace period
//	Stage 3: sandbox.KillProcessGroup（SIGKILL 进程组）
//
// Unix 实现：子进程通过 sandbox.StartCommandWithProcessGroup 以 Setpgid:true 启动，
// 故 pgid == 子进程 pid。向 -pgid 发送 SIGTERM 可终止整个进程组（含子进程的子进程）。
// 进程已退出时返回 nil（幂等），避免 ESRCH 噪声。
func signalProcessGroupGraceful(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// 进程已退出或无法获取 pgid，视为已终止（幂等）
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		// ESRCH 表示进程已不存在，视为成功（幂等）
		if err != syscall.ESRCH {
			return fmt.Errorf("signal process group: %w", err)
		}
	}
	return nil
}
