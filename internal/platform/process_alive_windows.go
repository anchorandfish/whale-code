//go:build windows

package platform

import "syscall"

// processGroupsAreAddressable：Windows 无 POSIX 进程组语义
// （`killProcessTree` 走 `taskkill /F /T`，按进程树而非进程组）。
// ⇒ `ProcessGroupAlive` 在本平台**不声明**，调用方据本常量选择路径。
const processGroupsAreAddressable = false

// ProcessAlive 报告 pid 对应的进程当前是否存在（Windows 实现）。
//
// 用 `OpenProcess(SYNCHRONIZE)` 探句柄：句柄拿得到 ⇒ 进程存在。
// 刻意**不** spawn `tasklist`/`wmic` 子进程（两者都有额外开销，
// 且 `wmic` 在 Windows 11 24H2+ 已弃用）—— 这正是修复前 `daemon`
// 与 `config`/`util` 三份实现的分歧点之一。
//
// 官方源：OpenProcess API
// https://learn.microsoft.com/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocess @2026-07-21
//
// ★ `pid <= 0` 守卫（实测：修复前 `daemon/kill_windows.go` 的
// `tasklist /FI "PID eq 0"` 会匹配到 System Idle Process 那一行 ⇒ 恒 true）。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// SYNCHRONIZE (0x00100000) 权限足以检测进程存活，且大多数 PID 可获取。
	const synchronize = 0x00100000
	handle, err := syscall.OpenProcess(synchronize, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER (87) ⇒ 进程不存在；其余错误同样按"不在"处理
		// （fail-closed：宁可认为不在，也不要凭空认为在）。
		return false
	}
	syscall.CloseHandle(handle)
	return true
}
