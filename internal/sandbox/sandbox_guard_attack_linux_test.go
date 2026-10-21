//go:build linux

// Package sandbox — Linux 内核级攻击用例（Guard Attack Tests）。
//
// 与 e2e_runtime_linux_test.go 的区别：此处载荷模拟真实攻击
// （fork bomb / 内存炸弹 / 网络外联 / 超大文件写入 / 逃逸原语调用），
// 验证 prlimit、namespace 与 seccomp 在内核层真实拦截，而非"配置已写入"。
//
// 子进程载荷通过 re-exec 测试二进制自身（WHALE_GUARD_CHILD env 分发，
// 见 os_sandbox_linux_landlock_test.go 的 TestMain），避免依赖 VM 中
// python/perl 等工具。
//
// 同步机制：ApplyResourceLimits 在子进程启动后经 unix.Prlimit 打限制，
// 存在竞态——载荷先轮询 getrlimit 直到与父进程传入的期望值一致才开始攻击
// （WHALE_GUARD_EXPECT_* env），确保测的是"限制生效后"的内核行为。
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// guardChildMain 是 re-exec 子进程载荷入口（TestMain 分发）。
func guardChildMain(mode string) {
	switch mode {
	case "membomb":
		waitRlimitMatches(unix.RLIMIT_AS, expectEnvU64("WHALE_GUARD_EXPECT_AS"))
		// 先小分配（对照：限制不是全阻断），再大分配（应被 RLIMIT_AS 拒）
		small := make([]byte, 16<<20)
		for i := 0; i < len(small); i += 4096 {
			small[i] = 1
		}
		fmt.Println("SMALL_OK")
		big := make([]byte, 3<<30) // 3GB commit
		for i := 0; i < len(big); i += 4096 {
			big[i] = 1
		}
		fmt.Println("MEM_PASSED") // 不应到达
		os.Exit(0)
	case "forkbomb":
		// 同步等待 prlimit 生效，再尝试并发 spawn 40 个保活子进程。
		// RLIMIT_NPROC 按 real-UID 计全局任务数（含线程），
		// 超额 fork 在内核层被拒（EAGAIN）。
		waitRlimitMatches(unix.RLIMIT_NPROC, expectEnvU64("WHALE_GUARD_EXPECT_NPROC"))
		successes := 0
		var kids []*exec.Cmd
		for i := 0; i < 40; i++ {
			c := exec.Command("sleep", "10")
			if err := c.Start(); err == nil {
				successes++
				kids = append(kids, c)
			}
		}
		fmt.Println("SPAWNED", successes)
		time.Sleep(3 * time.Second) // 保活供父进程观察，再统一清理
		for _, c := range kids {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
		os.Exit(0)
	case "iflist":
		// 列出网络接口（NEWNET 内应仅 lo）——不需要外网即可验证隔离
		ifaces, err := net.Interfaces()
		if err != nil {
			fmt.Println("IFACES_ERR:" + err.Error())
			os.Exit(0)
		}
		names := make([]string, 0, len(ifaces))
		for _, i := range ifaces {
			names = append(names, i.Name)
		}
		sort.Strings(names)
		fmt.Println("IFACES " + strings.Join(names, ","))
		os.Exit(0)
	case "nsprobe":
		// SBX-018 E2E 载荷：打印**自身** ipc namespace 的 inode 引用
		// （形如 "ipc:[4026532375]"），由父进程与宿主侧读数比对。
		//
		// 为什么用 re-exec 自测试二进制而不是 `sh -c readlink`：不依赖沙箱内
		// PATH，也不依赖外部工具的存在（容器最小环境友好），且与既有 iflist /
		// seccompprobe 载荷同形 —— 判据只归因于 namespace，不掺工具可用性。
		if v, err := os.Readlink("/proc/self/ns/ipc"); err == nil {
			fmt.Println("NS_IPC " + v)
		} else {
			fmt.Println("NS_IPC_ERR " + err.Error())
		}
		os.Exit(0)
	case "dial":
		conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 3*time.Second)
		if err != nil {
			fmt.Println("DENIED:" + err.Error())
			os.Exit(0)
		}
		conn.Close()
		fmt.Println("CONNECTED") // 沙箱内不应出现
		os.Exit(0)
	case "seccompprobe":
		// 守卫旋钮：N>0 时先钉住 N 秒再干活——复刻"子进程窗口内不退出、
		// 管道不关"的可观测形态（ptrace-stop 挂死与 sleep 在父进程眼中同形：
		// 都是不退出 + 管道不关）。仅守卫用例设置，真实探针不经过此路径。
		if s := os.Getenv("WHALE_GUARD_PROBE_STALL_SEC"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		// seccomp 探针：三条独立观测，各自打印 errno 由父进程断言。
		// - PTRACE: ATTACH 到自己的哑孙进程（ptracetarget 模式）。seccomp
		// denylist 按 SYS_PTRACE 号拦截（os_sandbox_linux_seccomp.go:123）⇒
		// 与旧 TRACEME 观测按号等价。
		// ★ 根治：不用 PTRACE_TRACEME。旧形态下对照组 child 自己
		// TRACEME ⇒ tracer=测试主进程 ⇒ Go runtime 的 SIGURG 抢占信号把
		// child 打进 ptrace-stop（轻症=无输出 fail，重症=管道不关永挂）。
		// 新形态 tracer=短命的 child 自己：打印 errno 即 SIGKILL 哑孙并退出
		// ⇒ 测试主进程永远不承担 tracer/tracee 角色，毒根拔除。
		// - IO_URING_SETUP: params=NULL ⇒ 无过滤时进入内核实现并返回 EFAULT(14)；
		// 装了过滤器应在 syscall 入口就被 EPERM 拦下。
		// - GETPID: 阴性对照，必须照常成功 —— 证明确实只拒了名单内的项，
		// 而不是把 syscall 面整体砸掉（denylist 形态的核心性质）。
		selfPath, err := os.Executable()
		if err != nil || selfPath == "" {
			fmt.Printf("PTRACE_ERRNO -3\n")
			os.Exit(0)
		}
		dummy := exec.Command(selfPath)
		dummy.Env = []string{"WHALE_GUARD_CHILD=ptracetarget"}
		dummy.Stdout = nil
		dummy.Stderr = nil
		if derr := dummy.Start(); derr != nil {
			fmt.Printf("PTRACE_ERRNO -4\n")
			os.Exit(0)
		}
		_, _, e1 := unix.Syscall(unix.SYS_PTRACE, uintptr(unix.PTRACE_ATTACH), uintptr(dummy.Process.Pid), 0)
		fmt.Printf("PTRACE_ERRNO %d\n", int(e1))
		_ = dummy.Process.Kill() // ATTACH 成功时哑孙停在 ptrace-stop：SIGKILL 是唯一保证穿透的信号
		_ = dummy.Wait()
		_, _, e2 := unix.Syscall(unix.SYS_IO_URING_SETUP, 0, 0, 0)
		fmt.Printf("IOURING_ERRNO %d\n", int(e2))
		pid, _, e3 := unix.Syscall(unix.SYS_GETPID, 0, 0, 0)
		fmt.Printf("CONTROL_ERRNO %d PID %d\n", int(e3), int(pid))
		os.Exit(0)
	case "ptracetarget":
		// 哑目标：seccompprobe 的 ATTACH 对象。睡到被杀（父进程
		// SIGKILL 收尸）；不可用 select{}（编译器死锁检查会 panic）。
		time.Sleep(120 * time.Second)
		os.Exit(0)
	}
	fmt.Println("UNKNOWN_MODE:" + mode)
	os.Exit(64)
}

// expectEnvU64 读取父进程传入的期望 rlimit 值（0 表示无期望，跳过同步）。
func expectEnvU64(key string) uint64 {
	v, _ := strconv.ParseUint(os.Getenv(key), 10, 64)
	return v
}

// waitRlimitMatches 轮询 getrlimit 直到 Cur == expect 或超时（10s）。
// 超时不报错：限制可能已在位（轮询窗口错位），攻击照常进行，
// 由结果断言兜底。
func waitRlimitMatches(res int, expect uint64) {
	if expect == 0 {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rl syscall.Rlimit
		if err := syscall.Getrlimit(res, &rl); err == nil && rl.Cur == expect {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// countUserTasks 统计指定 UID 的内核任务数（进程 × 线程）。
// RLIMIT_NPROC 检查按 real-UID 全局任务计数，上限必须相对当前值设置，
// 否则桌面会话（systemd/gnome 等几十个任务）已耗尽配额 → 连 shell 都无法 fork。
func countUserTasks(uid int) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 128 // /proc 不可读时的保守回退
	}
	total := 0
	for _, e := range entries {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		fi, err := os.Stat("/proc/" + name)
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			continue
		}
		tasks, err := os.ReadDir("/proc/" + name + "/task")
		if err != nil {
			total++ // 进程退出中，按 1 个任务计
			continue
		}
		total += len(tasks)
	}
	return total
}

// TestGuard_Attack_MemoryBombRLimitAS 内存炸弹攻击：RLIMIT_AS=2GB 下
// 子进程先分配 16MB（对照）再分配 3GB（应被拒）。
//
// 结果断言：输出不得包含 MEM_PASSED。
// - SMALL_OK 出现 → 精确拦截（小分配过、大分配拦）
// - SMALL_OK 缺失 → Go runtime 自身被限（限制生效但粒度粗，记录提示）
func TestGuard_Attack_MemoryBombRLimitAS(t *testing.T) {
	// ★ SBX-021：RLIMIT_AS 已在 Linux 侧**退役** ——
	// 它量的是虚拟地址空间而非实际占用，生产默认 1GiB 会误杀
	// `go build ./internal/...`（实测坐实，见 resourceLimitTargets 注释）。
	//
	// 本测试**刻意保留而不删除**：它记录了"AS 这道门曾经确实能拦住内存炸弹"
	// （断言 MEM_PASSED 不出现）。等 cgroup v2 memory.max（SBX-019 路线 C）
	// 落地后，把本测试改写为 cgroup 版本即恢复其判别力。
	t.Skip("SBX-021：RLIMIT_AS 在 Linux 已退役（量纲错 + 误杀实证）；" +
		"内存门待 cgroup v2 memory.max（SBX-019 路线 C）落地后改写本测试")

	const limit = 2 << 30 // 2GB
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"WHALE_GUARD_CHILD=membomb",
		fmt.Sprintf("WHALE_GUARD_EXPECT_AS=%d", limit))
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Fatalf("start membomb child: %v", err)
	}
	if err := ApplyResourceLimits(cmd, ResourceLimits{MemoryMaxBytes: limit}, NewAuditLog()); err != nil {
		t.Fatalf("ApplyResourceLimits: %v", err)
	}
	waitErr := cmd.Wait()
	out := buf.String()
	t.Logf("membomb 输出（wait=%v）:\n%s", waitErr, out)

	if strings.Contains(out, "MEM_PASSED") {
		t.Fatal("内核级旁路成功：RLIMIT_AS=2GB 下 3GB 分配+touch 成功（限制未生效！）")
	}
	if strings.Contains(out, "SMALL_OK") {
		t.Log("精确拦截：16MB 分配成功（限制非全阻断），3GB 分配被内核拒绝")
	} else {
		t.Log("粗粒度拦截：Go runtime 自身受 RLIMIT_AS 约束未能启动到小分配阶段（限制已生效）")
	}
}

// TestGuard_Attack_ForkBombProcessLimit fork bomb 攻击：RLIMIT_NPROC=当前任务数+16
// 下子进程尝试并发 spawn 40 个保活 sleep。
//
// 关键修正（VM 实测教训）：上限必须相对 real-UID 当前任务数设置——
// RLIMIT_NPROC 是全局（per-UID）计数而非进程树隔离，固定小值会让
// 桌面会话进程直接耗尽配额（sh: Cannot fork），无法测出攻击拦截。
//
// 结果断言：spawn 成功数 < 40（内核拒绝部分 fork）。
func TestGuard_Attack_ForkBombProcessLimit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 豁免 RLIMIT_NPROC（内核行为）；生产为非 root rootless 路径，请以普通用户运行验证")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not available: %v", err)
	}

	taskCount := countUserTasks(os.Getuid())
	limit := taskCount + 16 // 余量 16：载荷自身线程 + 会话正常波动
	t.Logf("当前 UID 任务数=%d（含线程），设置 RLIMIT_NPROC=%d", taskCount, limit)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"WHALE_GUARD_CHILD=forkbomb",
		fmt.Sprintf("WHALE_GUARD_EXPECT_NPROC=%d", limit))
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Fatalf("start forkbomb child: %v", err)
	}
	if err := ApplyResourceLimits(cmd, ResourceLimits{ProcessMax: uint64(limit)}, NewAuditLog()); err != nil {
		t.Fatalf("ApplyResourceLimits: %v", err)
	}
	waitErr := cmd.Wait()
	out := buf.String()
	t.Logf("forkbomb 输出（wait=%v）:\n%s", waitErr, out)

	spawned := -1
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "SPAWNED ") {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "SPAWNED "))); err == nil {
				spawned = n
			}
		}
	}
	if spawned < 0 {
		t.Fatalf("未能解析 SPAWNED 计数，输出: %s", out)
	}
	if spawned >= 40 {
		t.Fatalf("内核级旁路成功：RLIMIT_NPROC=%d 下 40 个子进程全部 spawn 成功（限制未生效！）", limit)
	}
	if spawned == 0 {
		t.Logf("全部 fork 被拒（UID 任务数在扫描后已超过上限 %d）——限制生效，但余量被会话占用", limit)
		return
	}
	if spawned > 24 {
		t.Logf("警告：spawn 成功 %d 超出预期余量（16+churn），请检查测试期间是否有大量进程启动", spawned)
	}
	t.Logf("fork bomb 被限制：尝试 40，实际成功 %d（RLIMIT_NPROC=%d 内核拒绝超额 fork，含 UID 全局任务计数）", spawned, limit)
}

// TestGuard_Attack_NetworkIsolatedInNetNS 网络隔离攻击：CLONE_NEWNET 内
// 子进程枚举网络接口。
//
// 结果断言：沙箱内接口列表 == [lo]（宿主存在非 lo 接口为对照前提）。
// 相比外联拨号测试，接口枚举不依赖外网（VM 无外网时可验证），
// 且证明隔离发生在网络栈层（接口不可见 → 物理上无法外联）。
func TestGuard_Attack_NetworkIsolatedInNetNS(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	// 对照组：宿主必须存在非 lo 接口，否则无法区分"隔离"与"本来就没网"
	hostIfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("无法枚举宿主接口: %v", err)
	}
	hostExternal := make([]string, 0, 2)
	for _, i := range hostIfaces {
		if i.Name != "lo" {
			hostExternal = append(hostExternal, i.Name)
		}
	}
	if len(hostExternal) == 0 {
		t.Skip("宿主仅 lo 接口（无网络环境），无法对比验证隔离")
	}
	t.Logf("宿主非 lo 接口: %v", hostExternal)

	workDir := t.TempDir()
	cfg := &SandboxConfig{
		WorkDir: workDir,
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces:    true,
				Namespaces:       []string{"pid", "net", "uts"},
				UseUserNamespace: true,
			},
		},
	}
	mgr := NewManager(cfg, SandboxLevelIsolated)
	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelIsolated, workDir)
	if err != nil {
		t.Fatalf("Manager.Prepare: %v", err)
	}
	defer cleanup()
	osSB := sbCtx.OSSandbox()
	if osSB == nil || osSB.Platform() != "linux-namespaces" {
		t.Fatalf("OSSandbox 降级（platform=%v），网络隔离测试无意义", osSB)
	}

	// re-exec 必须用绝对路径：cmd.Dir=workDir 后相对路径 os.Args[0] 失效
	// （VM 实测：./sb_linux2 在临时工作目录下 ENOENT）
	selfExe, exeErr := os.Executable()
	if exeErr != nil {
		t.Skipf("os.Executable unavailable: %v", exeErr)
	}
	cmd := exec.Command(selfExe)
	cmd.Env = append(sbCtx.Env, "WHALE_GUARD_CHILD=iflist")
	cmd.Dir = workDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := osSB.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Skipf("namespace 启动被环境限制: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Logf("iflist child 退出码非零: %v", err)
	}
	out := buf.String()
	t.Logf("iflist 输出:\n%s", out)

	if strings.Contains(out, "IFACES_ERR") {
		t.Fatalf("载荷无法枚举接口: %s", out)
	}
	ifaceLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "IFACES ") {
			ifaceLine = strings.TrimSpace(strings.TrimPrefix(line, "IFACES "))
		}
	}
	if ifaceLine == "" {
		t.Fatalf("载荷无 IFACES 输出: %s", out)
	}
	ifaces := strings.Split(ifaceLine, ",")
	if len(ifaces) == 1 && ifaces[0] == "lo" {
		t.Logf("网络隔离生效：沙箱内仅 lo（宿主接口 %v 不可见）", hostExternal)
		return
	}
	t.Fatalf("内核级旁路成功：CLONE_NEWNET 沙箱内可见非 lo 接口 %v（网络栈未隔离！）", ifaces)
}

// TestGuard_Attack_FileSizeLimit 超大文件写入攻击：RLIMIT_FSIZE=1MB 下
// head 尝试写 8MB 文件。
//
// 关键修正（VM 实测教训）：内核拦截的正确证据是 head 收到 SIGXFSZ
// （退出码 153 = 128+25）且文件被截断在 1MB——shell 退出码不可靠
// （echo 成功使 shell 最终 exit 0），必须解析载荷打印的 HEAD_EXIT
// 并校验实际文件大小。
func TestGuard_Attack_FileSizeLimit(t *testing.T) {
	if _, err := exec.LookPath("head"); err != nil {
		t.Skipf("head not available: %v", err)
	}
	ws := t.TempDir()
	bigFile := ws + "/big.bin"
	// sleep 0.7 等 prlimit 生效；exit $rc 传播 head 的真实退出码
	script := fmt.Sprintf("sleep 0.7; head -c 8388608 /dev/zero > %s; rc=$?; echo HEAD_EXIT=$rc; exit $rc", bigFile)
	cmd := exec.Command("sh", "-c", script)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := ApplyResourceLimits(cmd, ResourceLimits{FileSizeMaxBytes: 1 << 20}, NewAuditLog()); err != nil {
		t.Fatalf("ApplyResourceLimits: %v", err)
	}
	waitErr := cmd.Wait()
	out := buf.String()
	t.Logf("filesize 攻击输出（wait=%v）:\n%s", waitErr, out)

	// 解析载荷打印的 head 退出码
	headExit := -1
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "HEAD_EXIT=") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "HEAD_EXIT="), "\r")); err == nil {
				headExit = n
			}
		}
	}
	if headExit < 0 {
		t.Fatalf("未能解析 HEAD_EXIT，输出: %s", out)
	}

	const sigxfsz = 25 // SIGXFSZ
	if headExit == 0 {
		t.Errorf("head 零退出（未见 SIGXFSZ）——需依赖文件大小校验判定")
	} else if headExit >= 128 {
		t.Logf("SIGXFSZ 生效：head 被信号终止（exit=%d = 128+%d）", headExit, headExit-128)
		if headExit-128 != sigxfsz {
			t.Logf("注意：终止信号 %d 非 SIGXFSZ(%d)，请人工确认", headExit-128, sigxfsz)
		}
	} else {
		t.Logf("head 非零退出 %d（非信号终止），依赖文件大小校验判定", headExit)
	}

	// 文件大小是最终事实标准
	if info, err := os.Stat(bigFile); err == nil {
		const slack = 64 << 10 // 内核按页粒度截断的容差
		if info.Size() > (1<<20)+slack {
			t.Fatalf("内核级旁路成功：文件大小 %d > 1MB 限制（限制未生效！）", info.Size())
		}
		t.Logf("文件大小限制生效：实际 %d bytes ≤ 1MB+64KB（RLIMIT_FSIZE 截断）", info.Size())
	} else {
		t.Logf("文件未创建（写入前即被拒）: %v", err)
	}
}

// seccompProbeResult 是一次 seccompprobe 载荷的观测结果。
type seccompProbeResult struct {
	ptraceErrno  int
	ioUringErrno int
	controlErrno int
	// timedOut：探针超过 deadline 未退出、被看门狗 SIGKILL。
	// 此时 raw/errno 字段不可信，调用方必须 fail-loud，不得继续做 errno 断言。
	timedOut bool
	raw      string
}

// seccompProbeDefaultDeadline 是单次探针的等待上限。
// 正常探针 ~0.5s、负载慢轮 3-6s（SIGURG 竞争），30s 留足余量又兜住套件。
const seccompProbeDefaultDeadline = 30 * time.Second

// runSeccompProbe 运行 seccompprobe 载荷；restrict 为 nil 时在无沙箱条件下运行（对照组）。
func runSeccompProbe(t *testing.T, selfExe, workDir string, restrict func(*exec.Cmd) error, env []string) seccompProbeResult {
	t.Helper()
	return runSeccompProbeTimeout(t, selfExe, workDir, restrict, env, seccompProbeDefaultDeadline)
}

// runSeccompProbeTimeout 是带 per-call deadline 的探针运行器（修复）。
//
// 根因备忘：对照组子进程 PTRACE_TRACEME 成功后成为本测试进程的 tracee；
// Go runtime 的 SIGURG 抢占信号一送达，子进程即进入 ptrace-stop，而 Go 的
// forkExec 父进程从不履行 tracer 职责（不会 PTRACE_CONT）⇒ 无 deadline 的
// cmd.Wait() 永挂（管道不关、wait4 无事件）。SIGKILL 是唯一能穿透
// ptrace-stop 的信号，因此 deadline 到点后由父进程 SIGKILL 兜底。
//
// 行业参照：对标证据件 `kill_on_drop(true)`
// + :101-102 `timeout(timeout_duration, child.wait_with_output())` —— 子进程
// 等待必须带 deadline、生命周期必须有保底击杀，"永挂"是最坏的失败形态。
func runSeccompProbeTimeout(t *testing.T, selfExe, workDir string, restrict func(*exec.Cmd) error, env []string, timeout time.Duration) seccompProbeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, selfExe)
	// 管道兜底：进程被杀后若管道仍被拖住，WaitDelay 强制 Wait 返回。
	cmd.WaitDelay = 3 * time.Second
	base := env
	if len(base) == 0 {
		base = os.Environ()
	}
	cmd.Env = append(append([]string{}, base...), "WHALE_GUARD_CHILD=seccompprobe")
	cmd.Dir = workDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if restrict != nil {
		if err := restrict(cmd); err != nil {
			t.Fatalf("Restrict: %v", err)
		}
	}
	if err := StartCommandWithProcessGroup(cmd); err != nil {
		t.Skipf("进程启动被环境限制（namespace/userns 不可用？）: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Logf("probe 超过 %v 未退出（已 SIGKILL）——疑似 ptrace-stop 挂死形态（ 看门狗触发）", timeout)
			return seccompProbeResult{raw: buf.String(), ptraceErrno: -1, ioUringErrno: -1, controlErrno: -1, timedOut: true}
		}
		t.Logf("probe 子进程退出码非零: %v", err)
	}

	out := buf.String()
	res := seccompProbeResult{raw: out, ptraceErrno: -1, ioUringErrno: -1, controlErrno: -1}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "PTRACE_ERRNO":
			res.ptraceErrno = probeErrno(t, f[1])
		case "IOURING_ERRNO":
			res.ioUringErrno = probeErrno(t, f[1])
		case "CONTROL_ERRNO":
			res.controlErrno = probeErrno(t, f[1])
		}
	}
	return res
}

func probeErrno(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("解析探针输出失败 %q: %v", s, err)
	}
	return n
}

// TestGuard_Attack_SeccompBlocksEscapePrimitives 内核级判据：装了过滤器之后，
// 逃逸原语（ptrace / io_uring_setup）必须真的被内层拦下，且不误伤普通 syscall。
//
// 判据设计要求（否则是假绿）：
// - 必须先有**对照组**（同一载荷、无沙箱）证明探针"在无防护时是通的"。
// 否则实验组的 EPERM 可能来自宿主环境（例如 sysctl io_uring_disabled=1
// 会让 io_uring_setup 在沙箱外也直接返回 EPERM），那与我们的过滤器无关。
// - 必须带**阴性对照**（getpid）：denylist 形态若把 syscall 面整体砸掉，
// 工具链会大面积假失败，同样是不能接受的退化。
//
// 说明：本用例只开 seccomp、**关掉 Landlock**，以单独判定 seccomp 这一层的
// 效果，并顺带覆盖「内核 <5.13 时 helper 仍须被召唤」的那条路径。
func TestGuard_Attack_SeccompBlocksEscapePrimitives(t *testing.T) {
	if !seccompSupported() {
		t.Skip("kernel does not support seccomp cBPF (need >= 3.17)")
	}
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	selfExe, err := os.Executable()
	if err != nil || selfExe == "" {
		t.Skipf("os.Executable unavailable: %v", err)
	}

	// ---- 对照组：无沙箱 ----
	control := runSeccompProbe(t, selfExe, t.TempDir(), nil, nil)
	if control.timedOut {
		t.Fatalf("对照组探针超过 %v 未完成（ 看门狗 SIGKILL）——ptrace-stop 挂死形态，观测值不可信", seccompProbeDefaultDeadline)
	}
	t.Logf("对照组（无沙箱）:\n%s", control.raw)
	if control.controlErrno != 0 {
		t.Fatalf("对照组 getpid errno=%d（want 0），探针本身不可用", control.controlErrno)
	}
	if control.ptraceErrno != 0 {
		t.Skipf("宿主已限制 ptrace（对照组 errno=%d，可能已在某层容器/沙箱内），探针无法判别", control.ptraceErrno)
	}
	ioUringDiscriminating := control.ioUringErrno != int(syscall.EPERM)
	if !ioUringDiscriminating {
		t.Logf("宿主已禁用 io_uring（对照组 errno=EPERM，疑 sysctl io_uring_disabled），" +
			"该探针不可判别，仅以 ptrace 作判据")
	}

	// ---- 实验组：namespaces + seccomp（Landlock 关闭）----
	workDir := t.TempDir()
	mgr := newSeccompTestManager(t, workDir, false /* useLandlock */)
	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelIsolated, workDir)
	if err != nil {
		t.Fatalf("Manager.Prepare: %v", err)
	}
	defer cleanup()
	osSB := sbCtx.OSSandbox()
	if osSB == nil || osSB.Platform() != "linux-namespaces" {
		t.Fatalf("OSSandbox 降级（platform=%v），seccomp 攻击测试无意义", osSB)
	}

	sandboxed := runSeccompProbe(t, selfExe, workDir, osSB.Restrict, sbCtx.Env)
	if sandboxed.timedOut {
		t.Fatalf("实验组探针超过 %v 未完成（ 看门狗 SIGKILL）——ptrace-stop 挂死形态，观测值不可信", seccompProbeDefaultDeadline)
	}
	t.Logf("实验组（namespaces + seccomp）:\n%s", sandboxed.raw)

	if sandboxed.ptraceErrno != int(syscall.EPERM) {
		t.Fatalf("内核级旁路：沙箱内 ptrace 未被拒（errno=%d，want EPERM=%d）——"+
			"同 uid 进程内存读写逃逸面仍敞开", sandboxed.ptraceErrno, int(syscall.EPERM))
	}
	if ioUringDiscriminating && sandboxed.ioUringErrno != int(syscall.EPERM) {
		t.Errorf("沙箱内 io_uring_setup 未被拒（errno=%d，want EPERM=%d）",
			sandboxed.ioUringErrno, int(syscall.EPERM))
	}
	if sandboxed.controlErrno != 0 {
		t.Errorf("阴性对照失败：沙箱内 getpid errno=%d（want 0）——"+
			"过滤器误伤非名单 syscall，工具链会大面积假失败", sandboxed.controlErrno)
	}
}
