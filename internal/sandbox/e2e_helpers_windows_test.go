//go:build windows

// Package sandbox — P1-4 端到端测试 Windows 平台辅助函数。
//
// 提供 e2eSetProcessGroup 辅助函数，在调用 RunWithTimeout 前设置进程组属性。
// RunWithTimeout 内部调用 cmd.Start() 但不设置 CREATE_NEW_PROCESS_GROUP，
// 若不预设进程组，KillProcessGroup 的 taskkill /T /F 可能无法正确终止进程树。

package sandbox

import (
	"os/exec"
	"syscall"
)

// e2eSetProcessGroup 在 Windows 平台设置 cmd.SysProcAttr.CreationFlags |= CREATE_NEW_PROCESS_GROUP。
//
// RunWithTimeout 内部调用 cmd.Start()（非 StartCommandWithProcessGroup），
// 不会自动设置 CREATE_NEW_PROCESS_GROUP。本函数在调用 RunWithTimeout 前预设该标志，
// 确保子进程在独立进程组中，使 KillProcessGroup 的 taskkill /T /F 能正确终止进程树。
func e2eSetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup // 0x00000200
}
