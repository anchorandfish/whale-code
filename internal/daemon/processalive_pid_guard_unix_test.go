//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// TestIsRunningRejectsNonPositivePID 守住一条不变量：
// pid <= 0 绝不能被判为「本应用的一个活实例」。
//
// 为什么这条测试是必须的（判别性说明）：
// - POSIX kill(2) 中 pid=0 表示「调用者所在进程组」、pid<0 表示「进程组 |pid|」，
// 二者在 sig=0（只做错误检查、不发信号）下都会成功返回 ⇒ 没有 pid<=0 守卫时，
// platform.ProcessAlive 会返回 true。
// - daemon 侧的 pid 来自磁盘 pid 文件，readPIDFile 用 strconv.Atoi 解析
// （internal/daemon/daemon.go:772-784）：内容为 "0" / "-1" 时能解析成功、
// 不返回 error，直达 platform.ProcessAlive，中间没有任何 pid>0 过滤。
// - ⇒ 没有守卫时，Daemon.IsRunning() 会把一个 0 误报成「已有实例在跑」，
// 调用方据此拒绝启动，而报错里看不到根因。无编译错、无测试红。
//
// 本测试从生产入口 Daemon.IsRunning() 进入，而不是直接调用私有 platform.ProcessAlive，
// 以保证覆盖的是生产真正执行的那条路径。
func TestIsRunningRejectsNonPositivePID(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"pid 为 0（kill(0,0) 命中调用者所在进程组）", "0"},
		{"pid 为 -1（kill(-1,0) 命中所有可发信号进程）", "-1"},
		{"pid 为其他负数（进程组形态）", "-12345"},
		{"pid 为 0 且带尾随空白（pid 文件常见形态）", "0\n"},
		{"pid 为 -1 且带尾随空白", "-1\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			pidPath := filepath.Join(tmpDir, "daemon.pid")
			if err := os.WriteFile(pidPath, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("写 pid 文件失败: %v", err)
			}

			d := &Daemon{cfg: DaemonConfig{PIDFilePath: pidPath}}

			if d.IsRunning() {
				t.Errorf("pid 文件内容为 %q 时 Daemon.IsRunning() 必须返回 false，"+
					"否则会把「没有实例」误报成「已有实例在跑」并拒绝启动", tc.content)
			}
		})
	}
}

// TestIsRunningAcceptsLivePID 是上一条测试的反向守卫：
// 证明新增的 pid<=0 守卫没有把判定写成恒 false —— 对真实存活的 pid 仍须返回 true。
//
// 用一个真实启动的 sleep 子进程取 pid，而不是 os.Getpid()：
// Daemon.IsRunning() 对 pid == os.Getpid() 会短路到 d.running（本测试里为 false），
// 走不到存活判定路径，会得到假结论。
func TestIsRunningAcceptsLivePID(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动辅助进程，跳过反向守卫: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("写 pid 文件失败: %v", err)
	}

	d := &Daemon{cfg: DaemonConfig{PIDFilePath: pidPath}}

	if !d.IsRunning() {
		t.Errorf("对真实存活的 pid %d，Daemon.IsRunning() 必须返回 true"+
			"（用于证明 pid<=0 守卫未退化为恒 false）", cmd.Process.Pid)
	}
}

// TestIsRunning_EPERMPIDIsAlive 钉住 修复点在本域的形态：
// 「进程存在但不可发信号（EPERM）」必须判 alive。
//
// 标本 pid=1（init/launchd）：一定存在；对非 root 一律 EPERM
// （本机 2026-09-20 实测 uid=501 ⇒ kill(1,0) -> EPERM）。
// 走**生产入口** Daemon.IsRunning()（不是直接调私有 isProcessAlive），
// 与上面两条用例同一路径：readPIDFile → isProcessAlive → platform.ProcessAlive。
//
// root 下 kill(1,0) 成功 ⇒ 用例失去判别性 ⇒ Skip 并说明，不静默变绿。
func TestIsRunning_EPERMPIDIsAlive(t *testing.T) {
	if err := syscall.Kill(1, 0); err == nil {
		t.Skip("当前以 root 运行：kill(1,0) 不返回 EPERM ⇒ 本用例失去判别性，跳过而非假绿")
	}

	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1"), 0o644); err != nil {
		t.Fatalf("写 pid 文件失败: %v", err)
	}

	d := &Daemon{cfg: DaemonConfig{PIDFilePath: pidPath}}
	if !d.IsRunning() {
		t.Errorf("pid 文件为 1（进程存在但不可发信号，EPERM）时 Daemon.IsRunning() 必须为 true；" +
			"判 false 会把「已有实例在跑」误报成「没有实例」⇒ 允许启动第二个 daemon")
	}
}
