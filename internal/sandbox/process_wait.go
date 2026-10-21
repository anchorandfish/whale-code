// Package sandbox — 受 ctx 约束的进程组等待。
//
// 为什么必须有本文件：`exec.CommandContext` 在 ctx 到期时**只杀直接子进程**（`sh` 组长）。
// 若命令含管道（如 `find ~ | head -10`），管道另一端的孙进程仍持有 stdout 写端 ⇒
// `cmd.Wait()` 还要等 io 拷贝 goroutine 结束 ⇒ **永不返回**
// ⇒ 写在 `Wait()` 之后的 `KillProcessGroup` 物理上不可达，超时形同虚设。
//
// 本文件把「等待 ＋ 及时杀组」收成**唯一实现**：
// - 生产入口 `WaitProcessGroupWithContext`：供 bash / plugin / hook 三条执行路径薄调用；
// - 内部核心 `waitProcessGroup`：`RunWithTimeout` 亦委托到它
// —— 目的是不让仓内出现第二份「等待＋杀组」实现（「双实现」型复发坑）。
//
// 本文件不含 build tag，全平台编译（平台差异收在 process_unix.go / process_windows.go）。

package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// DefaultProcessWaitDelay 杀组之后 `Wait` 仍未返回时的兜底上限。
//
// 语义：Go 在 ctx 取消且已调用 `cmd.Cancel` 后，最多再等这么久就把 io 拷贝断开
// （`exec.Cmd.WaitDelay`）。它是**兜底**，不替代「显式杀组」。
const DefaultProcessWaitDelay = 5 * time.Second

// ProcessWaitResult 描述一次受 ctx 约束的进程组等待结果。
type ProcessWaitResult struct {
	// TimedOut 表示本次等待因 ctx 到期/取消而被强制终止（进程组已杀）。
	TimedOut bool
	// Err 是 `cmd.Wait()` 的返回值。
	Err error
}

// WaitProcessGroupWithContext 等待**已启动**的 cmd 结束；ctx 到期/取消时先终止整个进程组、再收尾。
//
// ⚠️ 前置条件：cmd 必须已用 `StartCommandWithProcessGroup` 启动（独立进程组）。
// 否则「杀组」会命中**调用方自己的进程组** ⇒ 杀掉调用方（PIT-005）。
// 本函数对该前置做 fail-closed 校验：未武装（未独立建组）时直接返回错误，**不执行等待**。
func WaitProcessGroupWithContext(ctx context.Context, cmd *exec.Cmd) (ProcessWaitResult, error) {
	if cmd == nil || cmd.Process == nil {
		return ProcessWaitResult{}, errors.New("sandbox: WaitProcessGroupWithContext requires a started cmd")
	}
	if !processGroupArmed(cmd) {
		return ProcessWaitResult{}, errors.New("sandbox: cmd not started in its own process group")
	}
	return waitProcessGroup(ctx, cmd)
}

// waitProcessGroup 是唯一核心：不变量 = 「ctx 到期 ⇒ **先杀组**，再等 Wait 返回」。
//
// 不做 armed 校验（调用方负责）：`RunWithTimeout` 的历史语义是「自行 Start、可能未建组」，
// 其安全性由 `killProcessGroup` 内的 PIT-005 防线兜住（见 process_unix.go）。
//
// ⚠️ 本函数**不得再写 cmd.Cancel / cmd.WaitDelay**：
// 这两个字段由 `StartCommandWithProcessGroupContext` 在 Start 之前绑定。
// Start 之后赋值会与 exec 的 watchCtx goroutine 竞争（-race 实测：
// `watchCtx 读 cmd.Cancel` vs `waitProcessGroup 写 cmd.Cancel`）。
// 取消语义仍由下面的显式 KillProcessGroup 兜底 —— 不依赖 Cancel 是否被 Go 调用。
func waitProcessGroup(ctx context.Context, cmd *exec.Cmd) (ProcessWaitResult, error) {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case err := <-waitCh:
		return ProcessWaitResult{Err: err}, err
	case <-ctx.Done():
		// 显式杀组（幂等）。**不依赖 cmd.Cancel 是否被 Go 调用** ——
		// 这样即使 Go 版本行为变化，也不会退化成「假修复」。
		_ = KillProcessGroup(cmd)
		err := <-waitCh
		return ProcessWaitResult{TimedOut: true, Err: err}, err
	}
}
