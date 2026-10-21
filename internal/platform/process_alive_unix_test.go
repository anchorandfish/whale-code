//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// ── 判别器（EPERM ⇒ alive）─────────────────────────────────────────────
//
// 缺陷本体在 internal/session/process.go 的旧实现：
//
//	proc, err := os.FindProcess(pid)
//	if err != nil { return false }
//	return proc.Signal(syscall.Signal(0)) == nil
//
// `os.FindProcess` 在 Unix 上**永不失败**（它不查进程是否存在，只构造句柄），
// 判定完全落在 `Signal(0)`；而 `Signal(0)` 走 `syscall.Kill`，**EPERM 会作为
// error 返回** ⇒ 「存在但无权发信号」被判成**已退出**。

// legacySignalZeroAlive 复刻**修复前**的判据，仅用于在测试内**自证判别性**
// （证明新旧两份实现在同一 pid 上给出相反答案），不参与生产。
func legacySignalZeroAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// TestProcessAlive_EPERMIsAlive 钉住 的修复点：EPERM ⇒ 进程存在 ⇒ alive。
//
// 标本取 pid=1（init/launchd）：它一定存在，且**对非 root 一律返回 EPERM**
// （本机 2026-09-20 实测 uid=501 ⇒ kill(1,0) -> EPERM）。这不是边角情形 ——
// 与 pid=1 比较时**任何**普通用户都会走到 EPERM 分支。
//
// 以 root 运行时 kill(1,0) 会成功（不返回 EPERM），此时本用例无法区分新旧实现
// ⇒ 明确 Skip 并说明，而不是让它静默变绿（那会变成假绿守卫）。
func TestProcessAlive_EPERMIsAlive(t *testing.T) {
	if err := syscall.Kill(1, 0); err == nil {
		t.Skip("当前以 root 运行：kill(1,0) 不返回 EPERM ⇒ 本用例失去判别性，跳过而非假绿")
	}

	if !ProcessAlive(1) {
		t.Errorf("ProcessAlive(1) = false，但 pid 1 存在、只是本进程无权给它发信号（EPERM）⇒ 必须判 alive。\n" +
			"  判死正是 ：session 域据此认为锁持有者已退出 ⇒ 夺锁 ⇒ 与 F11 L1「误夺锁 ⇒ 数据丢失」同向。")
	}

	// 判别性自证：若修复前的判据在本机竟也判 pid 1 为 alive，说明本用例已不再
	// 能区分新旧实现（例如实现被换成了 EPERM 宽容版本）——让测试自己喊出来。
	if legacySignalZeroAlive(1) {
		t.Fatalf("前提失效：修复前的 os.FindProcess+Signal(0) 判据在本机也判 pid 1 为 alive，" +
			"本用例已无法区分新旧实现，失去判别性（须换标本或换判据）")
	}
}

// ── ProcessGroupAlive ───────────────────────────────────────────────────────

// TestProcessGroupAlive_NonPositivePIDIsDead 钉住 实测补上的 `pid <= 0` 守卫。
//
// 依据：`ProcessGroupAlive` 会对 pid **取负**再 `kill`：
// - pid=0 ⇒ kill(-0,0) = kill(0,0) ⇒ OK ⇒ 无守卫即误判 alive；
// - pid=-1 ⇒ kill(-(-1),0) = kill(1,0) ⇒ EPERM ⇒ 被 `err == EPERM` 判 alive，
// 而它打到的是 launchd ⇒ **任何**用户都命中此路。
//
// 生产后果：调用方是 `daemon.killProcessTree` 的宽限期轮询
// （`for ... if !ProcessGroupAlive(pid) return nil`）—— 判错就**永远等不到「已退出」**。
func TestProcessGroupAlive_NonPositivePIDIsDead(t *testing.T) {
	for _, pid := range []int{0, -1, -12345} {
		if ProcessGroupAlive(pid) {
			t.Errorf("ProcessGroupAlive(%d) = true，必须为 false："+
				"pid<=0 取负后会打到调用者自身进程组（0）或 launchd（-1）⇒ 恒定 alive，轮询永不结束。", pid)
		}
	}
}

// ── 正反向守卫：不是恒 true、也不是恒 false ─────────────────────────────────

// startSleep 启动一个「一定活着」的辅助进程（sleep 30s），返回其 pid。
// t.Cleanup 负责回收，避免留孤儿进程。
func startSleep(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动辅助进程，跳过: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// TestProcessAlive_LiveProcessIsAlive 证明实现不是恒 false。
func TestProcessAlive_LiveProcessIsAlive(t *testing.T) {
	pid := startSleep(t)
	if !ProcessAlive(pid) {
		t.Errorf("ProcessAlive(%d) = false，但该进程刚启动且未退出（应判 alive）。", pid)
	}
}

// TestProcessAlive_ExitedProcessIsDead 是上一条的反向守卫，证明实现不是恒 true。
//
// 用「先启动、再 wait 到退出」的真实 pid，而不是 `99999 大概率不存在` 这种
// 不确定样本 —— 后者无法通过注入反例打红（本仓已清理过一条同形假绿用例）。
// wait 返回后进程已被**回收**（不再是僵尸），pid 切实不存在。
func TestProcessAlive_ExitedProcessIsDead(t *testing.T) {
	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动辅助进程，跳过: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("等待辅助进程退出失败: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !ProcessAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("ProcessAlive(%d) 在进程 wait 退出 2 秒后仍为 true（应判 dead）。", pid)
}
