//go:build windows

// Package sandbox — Windows 内核级攻击用例（Guard Attack Tests）。
//
// 载荷通过 re-exec 测试二进制自身（WHALE_GUARD_CHILD env 分发，见本文件
// TestMain），验证 Job Object 的 ActiveProcessLimit / ProcessMemoryLimit /
// CPU Rate HARD_CAP / KILL_ON_JOB_CLOSE 在内核层真实拦截（非配置读回）：
//   - TestGuard_Attack_JobProcessLimitForkBomb   fork bomb → ActiveProcessLimit
//   - TestGuard_Attack_JobMemoryBomb             内存炸弹 → ProcessMemoryLimit
//   - TestGuard_Attack_CPUThrottleMeasured       CPU 实测节流（CPU time 实测）
//   - TestGuard_Attack_KillOnCloseOrphans        Job 关闭 → 孤儿全灭
package sandbox

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestMain 分发 guard 攻击子进程载荷（Windows 无 Landlock helper，独立定义）。
func TestMain(m *testing.M) {
	if mode := os.Getenv("WHALE_GUARD_CHILD"); mode != "" {
		guardChildMain(mode)
		return
	}
	os.Exit(m.Run())
}

// guardChildMain 是 re-exec 子进程载荷入口。
func guardChildMain(mode string) {
	switch mode {
	case "membomb":
		small := make([]byte, 16<<20)
		for i := 0; i < len(small); i += 4096 {
			small[i] = 1
		}
		fmt.Println("SMALL_OK")
		big := make([]byte, 768<<20) // 768MB commit
		for i := 0; i < len(big); i += 4096 {
			big[i] = 1
		}
		fmt.Println("MEM_PASSED") // 不应到达
		os.Exit(0)
	case "forkbomb":
		// 尝试并发 spawn 40 个保活孙进程（ping 30s），统计成功数。
		// 孙进程继承 Job → ActiveProcessLimit 在内核层拒绝超额 CreateProcess。
		successes := 0
		for i := 0; i < 40; i++ {
			c := exec.Command("cmd", "/c", "ping -n 30 127.0.0.1 >nul")
			if err := c.Start(); err == nil {
				successes++
			}
		}
		fmt.Println("SPAWNED", successes)
		time.Sleep(5 * time.Second) // 保活供父进程读取输出
		os.Exit(0)
	case "spinburn":
		// 单线程 busy-loop 4 秒（无 HARD_CAP 时 CPU time ≈ 4s；10% cap ≈ 0.4s）
		deadline := time.Now().Add(4 * time.Second)
		x := 1
		for time.Now().Before(deadline) {
			x = x*1664525 + 1013904223
		}
		fmt.Println("SPUN", x)
		os.Exit(0)
	case "sleeper":
		// 孙进程（ping 60s 保活）+ 自身 sleep 60s；打印孙 PID 供父进程验证
		grand := exec.Command("cmd", "/c", "ping -n 60 127.0.0.1 >nul")
		if err := grand.Start(); err != nil {
			fmt.Println("GRANDCHILD_START_FAIL:", err)
			os.Exit(0)
		}
		fmt.Println("GRANDCHILD_PID", grand.Process.Pid)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	fmt.Println("UNKNOWN_MODE:" + mode)
	os.Exit(64)
}

// startInJob 在 Job Object 内启动 re-exec 载荷并 assign。
//
// 输出缓冲必须在 Start 之前挂载（Win10 真机 E2E 首跑教训：调用方在
// Start 后再设 cmd.Stdout 无效——管道在 Start 时已建立，载荷输出全部
// 丢失，forkbomb 的 SPAWNED 计数永远解析失败）。
func startInJob(t *testing.T, cfg WindowsSandboxConfig, mode string) (*windowsSandbox, *exec.Cmd, *bytes.Buffer, func()) {
	t.Helper()
	w := NewWindowsSandbox(cfg, NewAuditLog(), SandboxLevelStrict)
	_, cleanup, err := w.Enter()
	if err != nil || w.jobHandle == 0 {
		if cleanup != nil {
			cleanup()
		}
		t.Skipf("Job Object 创建失败（权限/环境受限，降级路径由 canary 测试覆盖）: %v", err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "WHALE_GUARD_CHILD="+mode)
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		cleanup()
		t.Fatalf("start %s child: %v", mode, err)
	}
	hook := w.PostStartHook()
	if hook != nil {
		if err := hook(cmd); err != nil {
			cleanup()
			t.Skipf("AssignProcessToJobObject 失败（环境受限）: %v", err)
		}
	}
	return w, cmd, buf, cleanup
}

// errWatchdogFired 是看门狗触发（载荷超时被 Job 关闭终结）的哨兵错误。
var errWatchdogFired = errors.New("watchdog fired: child terminated by job close after timeout")

// waitOrKillJob 等待载荷退出；超时则关闭 Job（TerminateJobObject 终结
// Job 内全体进程）并返回哨兵错误 errWatchdogFired。
//
// Win10 真机教训（8G 老机器，sb_windows3 实测）：membomb 载荷在内存压力/
// DLP 干扰下可能 wedge 不退出（同代码上轮 0.70s OOM 退出，本轮卡满
// 10 分钟包超时 panic，卡死整个测试二进制）。载荷永不退出 ≠ 防护失效
// （限制仍在生效），但无界 Wait 不可接受，必须带看门狗。
//
// 语义约定（调用方）：
//   - membomb：看门狗触发 = 合法 PASS（768MB touch 未完成即限制生效，
//     Job 关闭终结正是生产行为）
//   - forkbomb / spinburn：看门狗触发 = FAIL（载荷疑似被 DLP/AV 挂起，
//     明确报错而非假通过）
//
// cmd.Wait() 会等待 stdout/stderr 拷贝 goroutine 结束，返回后 buf 已完整。
func waitOrKillJob(cmd *exec.Cmd, cleanup func(), timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		cleanup() // TerminateJobObject → Job 内全体终结，Wait 随即返回
		<-done
		return errWatchdogFired
	}
}

// TestGuard_Attack_JobProcessLimitForkBomb fork bomb 攻击：
// ActiveProcessLimit=8 的 Job 内子进程尝试并发 spawn 40 个保活孙进程。
//
// 结果断言：spawn 成功数 ≤ 12（limit=8 含 child 自身与余量；40=未生效）。
func TestGuard_Attack_JobProcessLimitForkBomb(t *testing.T) {
	_, cmd, buf, cleanup := startInJob(t, WindowsSandboxConfig{
		UseJobObject: true, MemoryLimit: 256 << 20, MaxProcesses: 8,
	}, "forkbomb")
	defer cleanup()

	waitErr := waitOrKillJob(cmd, cleanup, 60*time.Second)
	out := buf.String()
	t.Logf("forkbomb 输出（wait=%v）:\n%s", waitErr, out)

	spawned := -1
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "SPAWNED ") {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "SPAWNED"))); err == nil {
				spawned = n
			}
		}
	}
	if spawned < 0 {
		if errors.Is(waitErr, errWatchdogFired) {
			t.Fatalf("载荷 60s 未完成已被看门狗终结（疑似被 DLP/AV 挂起，请重跑），输出: %s", out)
		}
		t.Fatalf("未能解析 SPAWNED 计数，输出: %s", out)
	}
	if spawned >= 40 {
		t.Fatalf("内核级旁路成功：ActiveProcessLimit=8 下 40 个孙进程全部 spawn 成功（限制未生效！）")
	}
	if spawned == 0 {
		t.Fatalf("spawn 全部失败（可能是 ping 不可用，载荷环境问题）: %s", out)
	}
	t.Logf("fork bomb 被限制：尝试 40，实际成功 %d（ActiveProcessLimit=8 内核拒绝超额 CreateProcess）", spawned)
}

// TestGuard_Attack_JobMemoryBomb 内存炸弹攻击：ProcessMemoryLimit=96MB 的
// Job 内子进程先分配 16MB（对照）再分配 768MB（应被拒）。
//
// 结果断言：输出不得包含 MEM_PASSED。
func TestGuard_Attack_JobMemoryBomb(t *testing.T) {
	_, cmd, buf, cleanup := startInJob(t, WindowsSandboxConfig{
		UseJobObject: true, MemoryLimit: 96 << 20, MaxProcesses: 16,
	}, "membomb")
	defer cleanup()

	waitErr := waitOrKillJob(cmd, cleanup, 60*time.Second)
	out := buf.String()
	t.Logf("membomb 输出（wait=%v）:\n%s", waitErr, out)

	if strings.Contains(out, "MEM_PASSED") {
		t.Fatal("内核级旁路成功：ProcessMemoryLimit=96MB 下 768MB 分配+touch 成功（限制未生效！）")
	}
	if errors.Is(waitErr, errWatchdogFired) {
		// 看门狗触发 = 合法拦截：载荷被内存限制 wedge（768MB touch 永未完成，
		// MEM_PASSED 未出现），由 Job 关闭终结——这正是生产沙箱对失控进程的行为。
		t.Log("看门狗拦截：载荷被内存限制 wedge 无法完成分配，已由 Job 关闭终结（限制生效）")
		return
	}
	if strings.Contains(out, "SMALL_OK") {
		t.Log("精确拦截：16MB 分配成功（限制非全阻断），768MB 被 Job 内存限制拒绝")
	} else {
		t.Logf("粗粒度拦截：子进程被 Job 内存限制早期终止（wait=%v，限制已生效）", waitErr)
	}
}

// TestGuard_Attack_CPUThrottleMeasured CPU 节流实测（区别于配置读回）：
// CPUMaxPercent=10 的 Job 内单线程 busy-loop 4 秒，测量子进程 CPU time。
//
// 结果断言：CPU time < 2s（4s wall 的 50% 余量；无节流时 ≈4s，10% cap ≈0.4s）。
// 这是"车真的 ≤ 限速"而非"限速牌写对了"的证明。
func TestGuard_Attack_CPUThrottleMeasured(t *testing.T) {
	_, cmd, _, cleanup := startInJob(t, WindowsSandboxConfig{
		UseJobObject: true, MemoryLimit: 256 << 20, MaxProcesses: 16, CPUMaxPercent: 10,
	}, "spinburn")
	defer cleanup()

	waitErr := waitOrKillJob(cmd, cleanup, 60*time.Second)
	if errors.Is(waitErr, errWatchdogFired) {
		t.Fatalf("spinburn 载荷 60s 未完成（4s busy-loop；疑似被 DLP/AV 挂起，请重跑）")
	}
	if cmd.ProcessState == nil {
		t.Fatal("ProcessState nil")
	}
	cpuTime := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
	wall := 4 * time.Second
	t.Logf("spinburn 完成（wait=%v）：CPU time=%v / wall≈%v", waitErr, cpuTime, wall)

	if cpuTime >= wall/2 {
		t.Fatalf("内核级节流未生效：CPU time=%v ≥ 2s（10%% HARD_CAP 预期 ≈0.4s，实测接近全速）", cpuTime)
	}
	t.Logf("CPU HARD_CAP 实测生效：CPU time=%v（预期 ≈0.4s @10%%，断言阈值 2s）", cpuTime)
}

// TestGuard_Attack_KillOnCloseOrphans Job 关闭孤儿清理攻击：
// Job 内 child spawn 孙进程（ping 60s），父进程关闭 Job 后
// child 与孙进程都必须被内核终结（KILL_ON_JOB_CLOSE + TerminateJobObject）。
func TestGuard_Attack_KillOnCloseOrphans(t *testing.T) {
	// 手动启动（需 StdoutPipe 在 Start 前建立）
	w := NewWindowsSandbox(WindowsSandboxConfig{
		UseJobObject: true, MemoryLimit: 256 << 20, MaxProcesses: 16,
	}, NewAuditLog(), SandboxLevelStrict)
	_, cleanup, err := w.Enter()
	if err != nil || w.jobHandle == 0 {
		if cleanup != nil {
			cleanup()
		}
		t.Skipf("Job Object 创建失败: %v", err)
	}
	defer cleanup()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "WHALE_GUARD_CHILD=sleeper")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	if hook := w.PostStartHook(); hook != nil {
		if err := hook(cmd); err != nil {
			t.Skipf("AssignProcessToJobObject 失败: %v", err)
		}
	}

	grandPID := 0
	lineCh := make(chan string, 4)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, rerr := reader.ReadString('\n')
			if rerr != nil {
				close(lineCh)
				return
			}
			lineCh <- line
		}
	}()
	for done := false; !done; {
		select {
		case line, ok := <-lineCh:
			if !ok {
				done = true
				break
			}
			if strings.HasPrefix(line, "GRANDCHILD_PID ") {
				if n, aerr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "GRANDCHILD_PID "))); aerr == nil {
					grandPID = n
					done = true
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("5 秒内未收到 GRANDCHILD_PID")
		}
	}
	if grandPID == 0 {
		t.Fatal("grandPID 解析失败")
	}
	t.Logf("孙进程 PID=%d 已启动（ping 60s 保活）", grandPID)

	// 前置断言（Win10 真机教训：TerminateJobObject 曾用退出码 0，被杀 child
	// 伪装 exit 0；且需排除 child 在关闭前自然退出的解释）：child 仍在运行，
	// 后续非零退出才是 Job 终结的结果证明。
	if !processStillRunning(cmd.Process.Pid) {
		t.Fatalf("child 已在 Job 关闭前退出（载荷异常，无法验证 KILL_ON_JOB_CLOSE）")
	}

	// 关闭 Job → 全体终结
	cleanup()
	waitErr := cmd.Wait()
	t.Logf("child Wait after job close: %v（预期非零：被 Job 终结）", waitErr)
	if waitErr == nil {
		t.Error("child 正常退出（exit 0）——预期被 Job 终结（KILL_ON_JOB_CLOSE 未生效？）")
	}

	// 轮询验证孙进程已死
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processStillRunning(grandPID) {
			t.Logf("孤儿清理生效：孙进程 PID=%d 已随 Job 关闭被终结", grandPID)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("孤儿存活：孙进程 PID=%d 在 Job 关闭 5 秒后仍在运行（KILL_ON_JOB_CLOSE 未生效！）", grandPID)
}

// processStillRunning 是**本测试专用**的严格存活判定：句柄可打开 **且**
// 退出码仍为 STILL_ACTIVE(259)。
//
// ★ 它**不是** internal/platform.ProcessAlive 的重复声明 —— 二者回答的**不是同一个问题**。
//
//	platform.ProcessAlive（Windows: OpenProcess(SYNCHRONIZE) 成功即活）
//	  语义 = 「PID 仍存在」，与 Unix 的 kill(pid,0) 对齐：
//	  Unix 上已终止但尚未 wait 的僵尸进程，kill(pid,0) 同样返回 nil ⇒ 判活。
//	  这是「进程句柄/锁是否仍被占用」类调用方（daemon/config/util/session）需要的问题。
//
//	processStillRunning（本函数）
//	  语义 = 「进程**仍在运行**」。本测试要证明 Job 关闭后 child / 孙进程**真的停止了**，
//	  而 Windows 上「PID 仍可解析」在这段窗口里对 child 恒为真：MSDN《Terminating a
//	  Process》明确 PID 一直有效到**所有句柄关闭、进程对象被销毁**为止，而第 320 行的
//	  前置断言发生在 cmd.Wait() **之前** —— Go 的 os/exec 此刻仍持有 child 句柄
//	  ⇒ OpenProcess 必然成功 ⇒ 用 platform.ProcessAlive 会让该断言**假通过**
//	  （正是 Win10 真机教训里"ExitCode=0 伪装"那一类掩盖）。故必须看退出码。
//
// ⇒ 收敛到 platform 会**削弱**本断言，因此保留为测试局部实现，仅改名以消除
// 「同名 ⇒ 疑似重复声明」的误判。若将来 platform 需要「仍在运行」（而非「PID 存在」）
// 语义，再把它提升为第二个**具名**声明，而不是复用 ProcessAlive。
func processStillRunning(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false // 句柄打不开视为已退出
	}
	defer windows.CloseHandle(h)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}
