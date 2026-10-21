package platform

import "testing"

// 本文件只放**跨平台**可判别的用例；平台分支用例见 process_alive_unix_test.go。
//
// 判别器纪律（本仓铁律）：每条用例都必须能通过**注入反例打红**，否则不得写
// —— 本仓已有「只 t.Log、永不失败」的假绿守卫被清理（见 session/store_gaps_test.go）。

// TestProcessAlive_NonPositivePIDIsDead 钉住 `pid <= 0` 守卫。
//
// 为什么必须守卫（**实测**，不靠推断）——2026-09-20 本机 macOS / uid=501 实测
// POSIX `kill(pid, 0)` 的返回值：
//
//	kill(0, 0)      -> OK      （pid=0 的语义是「调用者所在**进程组**」）
//	kill(-1, 0)     -> OK      （pid<0 的语义是「进程组 |pid|」，-1 = 所有有权限的进程）
//	kill(-12345, 0) -> ESRCH
//	kill(1, 0)      -> EPERM
//	kill(99999, 0)  -> ESRCH
//
// ⇒ 去掉守卫后 `ProcessAlive(0)` / `ProcessAlive(-1)` 都会返回 **true**，本用例即红。
//
// 生产后果（不是理论风险）：`daemon` 的 pid 来自磁盘 pid 文件
// （`readPIDFile` 用 `strconv.Atoi`，**无正数过滤**），文件内容为 "0" 时
// `Daemon.IsRunning()` 会误报「已有实例在跑」并拒绝启动，报错里看不到根因。
// Windows 同形：`tasklist /FI "PID eq 0"` 会命中 System Idle Process 那一行。
func TestProcessAlive_NonPositivePIDIsDead(t *testing.T) {
	for _, pid := range []int{0, -1, -12345} {
		if ProcessAlive(pid) {
			t.Errorf("ProcessAlive(%d) = true，必须为 false：\n"+
				"  pid<=0 在 POSIX 下是「进程组」形态（kill(0,0) / kill(-1,0) 均成功返回），\n"+
				"  在 Windows 下会命中 System Idle Process ⇒ 无守卫即误判「存活」。", pid)
		}
	}
}
