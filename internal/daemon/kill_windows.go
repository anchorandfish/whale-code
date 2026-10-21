//go:build windows

package daemon

import (
	"fmt"
	"os/exec"
)

// killProcessTree 在 Windows 上杀死指定进程及其所有子进程。
// 使用 taskkill /F /T /PID 命令：/F 强制终止，/T 递归终止进程树。
func killProcessTree(pid int) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid)).Run()
}

// 注：本文件原先还自带 `processAlive`（tasklist 实现）。2026-09-20（收敛）
// 已删除 —— 判据统一到 internal/platform.ProcessAlive（其 Windows 分支用
// OpenProcess，比解析 tasklist 文本更稳）。daemon 包内一律经 daemon.isProcessAlive
// 薄转发进入，勿在本包内另起一份平台分支。
