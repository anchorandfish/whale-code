//go:build !windows

package platform

import "syscall"

// processGroupsAreAddressable：POSIX 平台支持进程组（kill(-pgid, sig)）。
const processGroupsAreAddressable = true

// ProcessAlive 报告 pid 对应的进程当前是否存在（Unix 实现）。
//
// 用 `kill(pid, 0)` 不发送实际信号，只问内核"这个 pid 我能碰到吗"：
// - `ESRCH` ⇒ 进程不存在 ⇒ false
// - `EPERM` ⇒ **进程存在**，只是本进程无权给它发信号 ⇒ true（契约见 process_alive.go）
// - `nil` ⇒ 存在 ⇒ true
//
// ★ `pid <= 0` 必须先拒：`kill(0, 0)` 的语义是"调用者所在**进程组**"、
// `kill(-pid, 0)` 是"进程组 |pid|"，两者在 sig=0 下都返回 nil ⇒ 不守卫就会把
// 「0」判成"存活"。`daemon` 的 pid 来自磁盘 pid 文件（`strconv.Atoi`，无正数过滤），
// 文件内容为 "0" 时会因此误报"已有实例在跑"。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false
		}
		// EPERM 等：进程存在但无权限发送信号 ⇒ 视为 alive。
		return true
	}
	return true
}

// ProcessGroupAlive 报告 pid 所在**进程组**是否还有成员存活（Unix 实现）。
//
// 与 ProcessAlive 的两点差异都来自语义本身，不是两套判据：
// 1. 取负号：`kill(-pgid, 0)`（进程组由 `sandbox.StartCommandWithProcessGroup` 建立）；
// 2. 这里是**进程组**而非单个进程 —— 组内任一成员在，就算 alive。
//
// ★ `pid <= 0` 守卫是 ** 实测补上的**：修复前本函数没有它，而相邻 8 行的
// `ProcessAlive` 有、且在注释里解释了为什么必须有 —— 「同一不变量在两处实现、
// 只写在一处」的经典形态。取负后 `kill(-(-1), 0)` = `kill(1, 0)` 会打到 launchd
// ⇒ 对 **任何** 用户都返 EPERM ⇒ 被本函数判成"进程组还活着" ⇒ 调用方
// （`killProcessTree` 的宽限期轮询）永远等不到"已退出"。
func ProcessGroupAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || err == syscall.EPERM
}
