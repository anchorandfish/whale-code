// Package sandbox — Windows 进程管理实现（C11/B3）
//
//go:build windows

package sandbox

import (
	"fmt"
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// startWithProcessGroup 启动命令并设置 CREATE_NEW_PROCESS_GROUP 标志。
func startWithProcessGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
	return cmd.Start()
}

// killProcessGroup 通过 taskkill /T /F 终止进程树（P0 降级方案）。
// Phase C 将改为 TerminateJobObject（更可靠，无需依赖 taskkill 在 PATH 中）。
// 进程已退出时返回 nil（幂等）。
func killProcessGroup(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	// 先尝试直接终止主进程（幂等）
	_ = cmd.Process.Kill()

	// taskkill /T /F 终止整个进程树
	kill := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid))
	if err := kill.Run(); err != nil {
		// taskkill 失败通常意味着进程已不存在；视为已终止（幂等）
		return nil
	}
	return nil
}

// processGroupArmed 报告 cmd 是否已在**自己的独立进程组**中启动。
//
// Windows 侧无 PIT-005 自杀问题（按 PID 定位进程树，不是组内广播），
// 但本函数仍作为 `WaitProcessGroupWithContext` 的前置校验：
// 判定依据 = 启动时是否设置了 CREATE_NEW_PROCESS_GROUP。
func processGroupArmed(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil || cmd.SysProcAttr == nil {
		return false
	}
	return cmd.SysProcAttr.CreationFlags&createNewProcessGroup != 0
}
