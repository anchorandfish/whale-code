// Package sandbox — Unix 进程管理实现（C11/B3）
//
//go:build !windows

package sandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// startWithProcessGroup 启动命令并设置 Setpgid，使子进程成为新进程组组长。
func startWithProcessGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return cmd.Start()
}

// killProcessGroup 通过 kill(-pgid, SIGKILL) 终止整个进程组。
// 进程已退出时返回 nil（幂等）。
//
// ★ PIT-005 自杀防线：若取到的 pgid **就是调用方自己的进程组**，
// 那么 `kill(-pgid, SIGKILL)` 会连 whale 自己一起杀掉。
// 触发条件：cmd 未用 `StartCommandWithProcessGroup` 启动（未 Setpgid）时，
// `Getpgid(child)` 返回的正是调用方的进程组。本仓既有测试注释已记载该坑
// （e2e_helpers_unix_test.go：「若不预设进程组，KillProcessGroup 会终止测试二进制本身」）。
// 处置：拒绝组杀，退回只杀该进程（fail-safe，宁可少杀不可自杀）。
func killProcessGroup(cmd *exec.Cmd) error {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// 进程已退出或无法获取 pgid：尝试直接 kill pid（幂等）
		if e := cmd.Process.Signal(syscall.SIGKILL); e != nil {
			// ESRCH 表示进程不存在，视为已终止
			if errno, ok := e.(syscall.Errno); ok && errno == syscall.ESRCH {
				return nil
			}
			return e
		}
		return nil
	}
	if own, ownErr := syscall.Getpgid(os.Getpid()); ownErr == nil && own == pgid {
		// PIT-005：目标是自己的进程组 ⇒ 拒绝组杀（否则等于自杀）。
		if e := cmd.Process.Signal(syscall.SIGKILL); e != nil {
			if errno, ok := e.(syscall.Errno); ok && errno == syscall.ESRCH {
				return nil
			}
			return e
		}
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		// ESRCH 表示进程组不存在，视为已终止（幂等）
		if errno, ok := err.(syscall.Errno); ok && errno == syscall.ESRCH {
			return nil
		}
		return err
	}
	return nil
}

// processGroupArmed 报告 cmd 是否**按启动时的声明**进入了独立进程组。
//
// 用途：`WaitProcessGroupWithContext` 的前置校验 —— 未武装时拒绝组杀（PIT-005，
// 避免「杀组 = 自杀」）。
//
// ★ 判据必须看 `cmd.SysProcAttr.Setpgid`（启动意图），**不能**在 Wait 阶段反查
// `Getpgid(cmd.Process.Pid)`：子进程可能已经退出，`Getpgid` 返回 ESRCH，于是
// 一条毫秒级的 `echo` 会被误判成"未武装" —— 表现为任务被记成
// `sandbox: cmd not started in its own process group`、
// 真实 stdout 丢失（读回用例实测）。
//
// "会不会杀到自己的进程组" 由 `killProcessGroup` 内部的 Getpgid 比对兜底 ——
// 那才是 PIT-005 的真正防线；本函数只回答"这条 cmd 是否被要求建新组"。
func processGroupArmed(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
}
