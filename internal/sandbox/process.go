// Package sandbox — 进程管理 helper（C11/B3）
//
// 本文件是 C11 冲突解决的权威定义方。
// 统一进程树终止机制：Tools/Sandbox/MCP 三模块共用同一组 helper，
// 移除各自实现的 taskkill/setpgid/Pdeathsig 方案。
//
// 平台行为：
// - Unix: setpgid 创建新进程组；kill(-pgid, SIGKILL) 终止进程组
// - Windows: CREATE_NEW_PROCESS_GROUP 创建新进程组；taskkill /T /F 降级终止
//
// Windows Job Object 资源限制（P1，Phase C）将在 process_windows.go 增强，
// 当前 P0 仅实现进程组创建与进程树终止。
package sandbox

import (
	"context"
	"errors"
	"os/exec"
)

// StartCommandWithProcessGroup 启动命令并将其放入新的进程组。
//
// 前置条件：cmd 未启动（cmd.Process == nil）。
// 后置条件：cmd 已启动，且在新进程组中。
//
// 错误语义：返回 exec.Cmd.Start() 的错误，或平台设置失败错误。
// 平台特定实现见 process_unix.go / process_windows.go。
func StartCommandWithProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("sandbox: cmd is nil")
	}
	if cmd.Process != nil {
		return errors.New("sandbox: cmd already started")
	}
	return startWithProcessGroup(cmd)
}

// StartCommandWithProcessGroupContext 是 StartCommandWithProcessGroup 的
// 「ctx 取消 ⇒ 终止整个进程组」变体。
//
// 为什么必须在 **Start 之前**绑定（的 -race 修复）：
// `exec.Cmd.Start` 会另起 watchCtx goroutine，它在 ctx 到期时读 `cmd.Cancel`
// 与 `cmd.WaitDelay`。这两个字段**只能在 Start 前赋值**；Start 之后再写就是
// 数据竞争（实测 `go test -race ./internal/tools -run TestN019...` 报
// "watchCtx 读 vs waitProcessGroup 写"）。旧实现把赋值放在 Wait 阶段
// （`waitProcessGroup`），属于 UB —— 且可能让取消语义时有时无。
//
// Cancel 的语义与旧实现一致：先杀整个进程组，组杀失败再退回杀组长。
// WaitDelay 用于「孙进程仍持有 stdout 写端」时给 Wait 一个上界。
func StartCommandWithProcessGroupContext(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("sandbox: cmd is nil")
	}
	if cmd.Process != nil {
		return errors.New("sandbox: cmd already started")
	}
	cmd.Cancel = func() error {
		if err := KillProcessGroup(cmd); err != nil {
			if cmd.Process != nil {
				return cmd.Process.Kill() // 组杀失败 → 退回杀组长（保底）
			}
			return err
		}
		return nil
	}
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = DefaultProcessWaitDelay
	}
	return startWithProcessGroup(cmd)
}

// KillProcessGroup 终止命令所在的整个进程组（包括所有子进程）。
//
// 前置条件：cmd 已启动（cmd.Process != nil）。
// 后置条件：进程组内所有进程已终止（或已发送终止信号）。
//
// 错误语义：
// - 进程已退出：返回 nil（幂等）
// - 进程组不存在：返回 error
// - 权限不足：返回 error
//
// 平台特定实现见 process_unix.go / process_windows.go。
func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil // 幂等：未启动视为已终止
	}
	return killProcessGroup(cmd)
}
