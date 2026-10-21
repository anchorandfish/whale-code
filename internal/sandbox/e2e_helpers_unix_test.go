//go:build !windows

// Package sandbox — P1-4 端到端测试 Unix 平台辅助函数。
//
// 提供 e2eSetProcessGroup 辅助函数，在调用 RunWithTimeout 前设置进程组属性。
// RunWithTimeout 内部调用 cmd.Start() 但不设置 Setpgid，
// 若不预设进程组，KillProcessGroup 会 kill 测试二进制自身的进程组。

package sandbox

import (
	"os/exec"
	"syscall"
)

// e2eSetProcessGroup 在 Unix 平台设置 cmd.SysProcAttr.Setpgid=true。
//
// RunWithTimeout 内部调用 cmd.Start()（非 StartCommandWithProcessGroup），
// 不会自动设置 Setpgid。若子进程与测试二进制同进程组，
// KillProcessGroup 的 kill(-pgid, SIGKILL) 会终止测试二进制本身。
// 本函数在调用 RunWithTimeout 前预设 Setpgid，确保子进程在独立进程组中。
func e2eSetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
