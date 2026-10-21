//go:build linux

// Package sandbox — SBX-020 判别性测试：pre-exec helper 必须**无条件**清掉
// core dump 能力（RLIMIT_CORE=0），且**不得**把无效的 dumpable 动作当成加固。
//
// # 病灶（修复前）
//
// helper（os_sandbox_linux_preexec.go）只施加信封里要求的 Landlock / seccomp /
// 资源上限，**没有**防 core dump 动作 ⇒ 宿主 `ulimit -c` 非 0 时，沙箱内命令
// 崩溃会把整块内存镜像按 core_pattern 落盘（可能正好落在工作区）。
//
// # 判据设计（为什么这些可判别）
//
// 每条行为判据都带**对照组**：先在同一进程内证明"能构造出非默认基线"，再证明
// helper 把它改掉。没有对照组的老写法（直接断言读数 == 0）会在宿主默认值恰好
// 为 0 时恒绿 —— 那正是本仓反复吃亏的假绿（本容器 `ulimit -c` 默认就是 0）。
//
//	判据① CORE（E2E）：先把自己 RLIMIT_CORE 软上限抬到 256MiB；控制组（不经
//	   helper）必须读到 256MiB；实验组（经 helper）必须读到 0。
//	   before 红：stub 不做任何事 ⇒ 实验组读到 268435456 ⇒ Fail
//	判据② 无条件性：信封只带 seccomp（**不带** resource_limits）时，CORE 仍
//	   必须被清 0 —— 防"后来者把加固挂到 spec.ResourceLimits != nil 上"。
//	判据③ 反假加固门（记录性）：pre-exec 阶段设的 prctl(PR_SET_DUMPABLE, 0)
//	   **活不过 execve**（此判据自证：同一进程内 AFTER-SET 必须为 0，
//	   AFTER-EXEC 必须回到 1）。它把"为什么本文件不做 dumpable"变成可执行的
//	   门：将来有人把 prctl 加进 helper 并宣称它保护目标命令，这条与其文件头
//	   说明会一起拦住他；反之若某内核真的开始保留 dumpable，它会变红并指向
//	   文件头，提示"现在可以做了"。
//	判据④ 空信封回归：SBX-020 不给信封加字段 ⇒ isEmpty 语义不得变宽。
//
// 反例注入（判别力自证）：删掉 RunPreExecHelper 第 7 步整块 ⇒ 判据①②红；
// 把 applyCoreDumpHardeningToSelf 改成只在 spec.ResourceLimits != nil 时调用
// ⇒ 只有判据②红（恰好证明它覆盖的是"无条件性"这一层）。
//
// ⚠️ 判据①在部分环境（宿主 RLIMIT_CORE 硬上限为 0，无法抬软上限）**不可构造
// 基线**，此时显式 Skip 并说明 —— 不许静默跳过。
//
// # I-8.7 端到端（真起 helper + 真发 SIGSEGV，见本文件末尾）
//
// 判据①②③④ 都止步于"限制被设上了"（读回 RLIMIT_CORE = 0）。人工用例 I-8.7 要求的
// 是**结果面**：真起 helper、ulimit -c = 0、真发 SIGSEGV ⇒ 磁盘上不落 core。
// 该判据已在 TestSBX020_E2E_SegfaultLeavesNoCore 内下移为容器可跑的 Go 端到端测试，
// 人工面只剩"目标发行版差异"（内核 core_pattern 策略是否允许落盘）。
//
// ★ 该用例先探 /proc/sys/kernel/core_pattern，并把两类**假绿源**显式点名后 Skip：
//   - 以 "|" 开头 ⇒ core 交给管道程序，磁盘上必然没有 core 文件；
//   - 含 "/"     ⇒ core 不落在目标进程的 cwd，用例在工作目录里必然观测不到。
//     两者都会让"没找到 core 文件"恒真。替代判据 = 读回目标进程 RLIMIT_CORE 实际值
//     （由判据①与本用例的 rlimit 子断言承担，且该判据对 core_pattern 不敏感）。
//     该识别器自身由判据⑤（TestSBX020_DiskSignalUsability）覆盖 —— 否则"识别假绿源"
//     这段逻辑永远只在特定宿主机上被执行，无从证明它对。
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// SBX-020 dumpable 实验用的 prctl 选项号（只在本测试文件用，故不引入包级常量）。
const (
	sbx020PRGetDumpable = 0x3
	sbx020PRSetDumpable = 0x4
)

// 判据③ 的阶段哨兵环境变量。
const (
	sbx020StageEnv    = "WHALE_SBX020_DUMPABLE_STAGE"
	sbx020TestNameEnv = "WHALE_SBX020_TESTNAME"
)

// sbx020RaiseCoreLimit 把**本进程** RLIMIT_CORE 的软上限抬到 want（字节），
// 硬上限保持不变（只动软上限 ⇒ 恢复永远可逆，不会因压低硬上限而在测试结束后
// 无法还原）。返回 (restore, ok)：ok=false 表示该环境不允许构造非零基线。
func sbx020RaiseCoreLimit(want uint64) (func(), bool) {
	var orig unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &orig); err != nil {
		return nil, false
	}
	if orig.Max != unix.RLIM_INFINITY && want > orig.Max {
		return nil, false // 非特权进程不能把软上限抬到硬上限之上
	}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: want, Max: orig.Max}); err != nil {
		return nil, false
	}
	return func() { _ = unix.Setrlimit(unix.RLIMIT_CORE, &orig) }, true
}

// sbx020ReadCoreLimit 起一个 `sh -c cat /proc/self/limits` 子进程并读回它的
// "Max core file size"。cfg 为 nil 时**不经** helper（控制组）。
//
// 取 sh 的子进程 cat 而非 sh 自身：顺带证明限制随 execve + fork 继承。
func sbx020ReadCoreLimit(t *testing.T, cfg *LinuxSandboxConfig) (uint64, bool) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh 不可用，跳过: %v", err)
	}
	cmd := exec.Command("sh", "-c", "cat /proc/self/limits")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin"}
	if cfg != nil {
		if err := wrapWithPreExecHelper(cmd, *cfg); err != nil {
			t.Fatalf("wrapWithPreExecHelper: %v", err)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("子进程执行失败: %v\n--- 输出 ---\n%s", err, out)
	}
	// parseProcSelfLimits 不收录 "unlimited" ⇒ 缺席即视为 unlimited。
	v, ok := parseProcSelfLimits(string(out))["Max core file size"]
	return v, ok
}

// sbx020CoreBaseline 抬高本进程软上限并返回 (restore, 基线读数是否可用)。
func sbx020CoreBaseline(t *testing.T, want uint64) func() {
	t.Helper()
	restore, ok := sbx020RaiseCoreLimit(want)
	if !ok {
		t.Skipf("宿主 RLIMIT_CORE 硬上限不允许把软上限抬到 %d 字节 ⇒ 本环境无法构造非零基线，"+
			"CORE 判据不具判别性（未验证边界记入回执）", want)
	}
	plain, okPlain := sbx020ReadCoreLimit(t, nil)
	if !okPlain || plain != want {
		restore()
		t.Skipf("控制组 RLIMIT_CORE 读数 ok=%v value=%d，期望 %d ⇒ 本环境无法构造可判别基线", okPlain, plain, want)
	}
	return restore
}

// TestSBX020_CoreLimitClearedByHelper 判据①：helper 必须把 RLIMIT_CORE 清 0。
//
// 控制组是这条判据不可省略的一半 —— 它证明"本环境确实能构造出非零读数"，
// 否则实验组的 0 可能只是宿主默认值，判据变恒真。
func TestSBX020_CoreLimitClearedByHelper(t *testing.T) {
	if _, err := os.Stat("/proc/self/limits"); err != nil {
		t.Skipf("/proc 不可用，跳过: %v", err)
	}
	const want = 256 << 20
	restore := sbx020CoreBaseline(t, want)
	defer restore()

	cfg := LinuxSandboxConfig{ResourceLimits: ResourceLimits{CPUMaxSeconds: 300}}
	got, okGot := sbx020ReadCoreLimit(t, &cfg)
	if !okGot {
		t.Fatalf("SBX-020 未生效：helper 子进程读回 unlimited（控制组读到 %d），want 0", want)
	}
	if got != 0 {
		t.Fatalf("SBX-020 未生效：helper 子进程 RLIMIT_CORE=%d，want 0", got)
	}
}

// TestSBX020_CoreLimitClearedWithoutResourceLimits 判据②：加固必须**无条件**。
//
// 场景：信封只带 seccomp，**不带** resource_limits。若后来者把
// applyCoreDumpHardeningToSelf 挂到 `spec.ResourceLimits != nil` 上，
// 本判据变红而判据①仍绿 —— 这正是本判据存在的意义。
func TestSBX020_CoreLimitClearedWithoutResourceLimits(t *testing.T) {
	if !seccompSupported() {
		t.Skip("内核/架构不支持 seccomp ⇒ 无法构造『只带 seccomp 的信封』，判据②不适用")
	}
	const want = 256 << 20
	restore := sbx020CoreBaseline(t, want)
	defer restore()

	cfg := LinuxSandboxConfig{UseSeccomp: true}
	got, okGot := sbx020ReadCoreLimit(t, &cfg)
	if !okGot {
		t.Fatalf("SBX-020 未生效（无条件性被破坏）：只带 seccomp 时读回 unlimited，want 0" +
			"—— 加固不得依赖 resource_limits 字段")
	}
	if got != 0 {
		t.Fatalf("SBX-020 未生效（无条件性被破坏）：只带 seccomp 时 RLIMIT_CORE=%d，want 0"+
			"—— 加固不得依赖 resource_limits 字段", got)
	}
}

// sbx020RunProbeStages 是判据③ 的子进程侧实现（一次性实验，见测试函数注释）。
func sbx020RunProbeStages() {
	switch os.Getenv(sbx020StageEnv) {
	case "read":
		v, _, _ := syscall.Syscall(syscall.SYS_PRCTL, uintptr(sbx020PRGetDumpable), 0, 0)
		fmt.Printf("SBX020_AFTER_EXEC=%d\n", int(v))
		os.Exit(0)
	case "set":
		v0, _, _ := syscall.Syscall(syscall.SYS_PRCTL, uintptr(sbx020PRGetDumpable), 0, 0)
		fmt.Printf("SBX020_BEFORE_SET=%d\n", int(v0))
		_, _, _ = syscall.Syscall(syscall.SYS_PRCTL, uintptr(sbx020PRSetDumpable), 0, 0)
		v1, _, _ := syscall.Syscall(syscall.SYS_PRCTL, uintptr(sbx020PRGetDumpable), 0, 0)
		fmt.Printf("SBX020_AFTER_SET=%d\n", int(v1))

		exe, err := os.Executable()
		if err != nil {
			fmt.Printf("SBX020_INFRA_ERR=%v\n", err)
			os.Exit(1)
		}
		args := []string{exe}
		if name := os.Getenv(sbx020TestNameEnv); name != "" {
			args = append(args, "-test.run=^"+name+"$")
		}
		_ = os.Setenv(sbx020StageEnv, "read")
		if err := syscall.Exec(exe, args, os.Environ()); err != nil {
			fmt.Printf("SBX020_INFRA_ERR=exec:%v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// TestSBX020_PreExecDumpableDoesNotSurviveExecve 判据③：反"假加固"门（记录性）。
//
// 断言内核事实：普通 execve 会把 `prctl(PR_SET_DUMPABLE, 0)` 复位为 1。因此
// **不能**在 pre-exec helper 里通过它保护目标命令 —— 该动作物理无效，留着就是
// SBX-014 式的谎报。本判据把这条知识变成可执行的门：
//
//	before/after 均应通过（它记录的是内核行为，不是本仓改动）
//	若变红 ⇒ 内核语义变了（dumpable 开始活过 execve）：此时应重读
//	  core_dump_hardening_linux.go 文件头，评估是否可把 dumpable 加固真正做上
//	  —— 这正是要它变红的价值。
//
// 自证机制：AFTER-SET 必须为 0（证明实验真的执行到了 prctl），AFTER-EXEC
// 必须为 1（证明被 execve 复位）。
func TestSBX020_PreExecDumpableDoesNotSurviveExecve(t *testing.T) {
	if os.Getenv(sbx020StageEnv) != "" {
		sbx020RunProbeStages()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^"+t.Name()+"$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), sbx020StageEnv+"=set", sbx020TestNameEnv+"="+t.Name())
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	if runErr != nil {
		t.Fatalf("实验子进程失败: %v\n--- 输出 ---\n%s", runErr, text)
	}

	num := func(key string) (int, bool) {
		for _, line := range strings.Split(text, "\n") {
			v, found := strings.CutPrefix(strings.TrimSpace(line), key+"=")
			if !found {
				continue
			}
			n, perr := strconv.Atoi(strings.TrimSpace(v))
			if perr != nil {
				t.Fatalf("解析 %s 失败: %q", key, line)
			}
			return n, true
		}
		return 0, false
	}

	beforeSet, okB := num("SBX020_BEFORE_SET")
	afterSet, okA := num("SBX020_AFTER_SET")
	afterExec, okE := num("SBX020_AFTER_EXEC")
	if !okB || !okA || !okE {
		t.Fatalf("实验输出不完整（before=%v after=%v exec=%v）:\n%s", okB, okA, okE, text)
	}
	if beforeSet != 1 {
		t.Skipf("本进程初始 dumpable=%d，期望 1 ⇒ 本环境无法构造可判别基线", beforeSet)
	}
	if afterSet != 0 {
		t.Fatalf("实验无效：prctl 设置后读数 %d，期望 0（说明 prctl 没被执行到）", afterSet)
	}
	if afterExec != 1 {
		t.Fatalf("内核行为已变：pre-exec 设的 dumpable=%d **活过了 execve**。\n"+
			"⇒ 现在或许可以把 prctl(PR_SET_DUMPABLE,0) 真正施加到目标命令上；\n"+
			"   请重读 internal/sandbox/core_dump_hardening_linux.go 文件头（SBX-020 记录：\n"+
			"   内核 5.10/6.x 在 begin_new_exec 里把 dumpable 复位为 1），再决定是否补上。", afterExec)
	}
}

// TestSBX020_EmptySpecStillRejected 判据④：回归 —— SBX-020 未给信封加字段
// ⇒ 空信封语义不得被放宽（helper 仍须 fail-closed 拒绝"什么都没要求"的调用）。
func TestSBX020_EmptySpecStillRejected(t *testing.T) {
	empty := PreExecSpec{}
	if !empty.isEmpty() {
		t.Fatal("空信封必须仍被判定为 isEmpty（SBX-020 不给信封加字段，语义不得变宽）")
	}
}

// ===========================================================================
// I-8.7 端到端（真起 helper + 真发 SIGSEGV）：结果面
// ===========================================================================

// sbx020CorePattern 读回内核的 core dump 落盘策略（/proc/sys/kernel/core_pattern）。
func sbx020CorePattern() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/core_pattern")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// sbx020DiskSignalUnusableReason 判定「cwd 内有没有 core 文件」这一信号在当前
// core_pattern 下能否用作判据。
//
// 返回非空字符串 = **不可用**，内容是假绿源说明；返回空 = 可用。
//
// ★ 为什么抽成纯函数：这段判断若内联在 t.Skipf 里，它**永远只在特定宿主机上被执行**
// —— 本仓无从证明它对，也就无法排除"I-8.7 在管道式 coredump 的宿主机上静默通过
// （假绿）"。抽出来后可被 TestSBX020_DiskSignalUsability 覆盖，三种输入必须产生
// 不同结论（判别器非恒真）。
func sbx020DiskSignalUnusableReason(pattern string) string {
	if strings.HasPrefix(pattern, "|") {
		return fmt.Sprintf("core_pattern=%q 以 \"|\" 开头 ⇒ 内核把 core 交给管道程序读走，"+
			"磁盘上**永远不会**出现 core 文件，与 RLIMIT_CORE 是否被清 0 无关", pattern)
	}
	if strings.Contains(pattern, "/") {
		return fmt.Sprintf("core_pattern=%q 含路径分隔符 ⇒ core 不落在目标进程的 cwd"+
			"（如 /var/crash/...），本用例刻意不在其它路径搜索以避免误判", pattern)
	}
	return ""
}

// sbx020SegvObs 是一次「真发 SIGSEGV」实验的观测结果。
type sbx020SegvObs struct {
	ulimitCLine string   // 目标进程自报的 `ulimit -c` 原样读数
	ulimitN     uint64   // 上面读数的数值（blocks；本用例只判等零/非零）
	ulimitOK    bool     // 读数是否可解析
	cores       []string // 崩溃后其 cwd 内出现的 core* 文件
	waitErr     error    // 目标进程的退出信息（SIGSEGV 时非 nil，属预期）
	raw         string   // 合并后的原始输出（排障用）
}

// sbx020RunSegvTarget 真起一个目标进程：先自报 RLIMIT_CORE，再给自己发 SIGSEGV。
//
// cfg == nil ⇒ 控制组（不经 helper）；否则经 wrapWithPreExecHelper（生产同一入口）。
//
// ★ 顺序刻意如此：**先读限制、后崩溃**，两次观测来自同一次进程执行 ⇒ 不存在
// "读的是限制生效前 / 落 core 是生效后"的时序错配（这正是老式裸 prlimit 的坑）。
func sbx020RunSegvTarget(t *testing.T, dir string, cfg *LinuxSandboxConfig) sbx020SegvObs {
	t.Helper()
	cmd := exec.Command("sh", "-c", "ulimit -c; kill -SEGV $$")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin"}
	if cfg != nil {
		if err := wrapWithPreExecHelper(cmd, *cfg); err != nil {
			t.Fatalf("wrapWithPreExecHelper: %v", err)
		}
	}
	out, waitErr := cmd.CombinedOutput()

	obs := sbx020SegvObs{waitErr: waitErr, raw: string(out)}
	// 首行是 `ulimit -c` 的读数（SIGSEGV 的诊断信息走 stderr，已合并进来）
	obs.ulimitCLine = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if n, perr := strconv.ParseUint(obs.ulimitCLine, 10, 64); perr == nil {
		obs.ulimitN, obs.ulimitOK = n, true
	}
	entries, rerr := os.ReadDir(dir)
	if rerr == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "core") {
				obs.cores = append(obs.cores, e.Name())
			}
		}
	}
	return obs
}

// TestSBX020_E2E_SegfaultLeavesNoCore 端到端（生产真实入口）：
// 真起 helper + ulimit -c = 0 + 真发 SIGSEGV ⇒ cwd 内不得出现 core 文件。
//
// 与判据①②③④的分工：那四条止步于"限制被设上了"（读回 RLIMIT_CORE=0），本条打到
// **结果面**（真的崩溃了、真的没落盘）。二者都绿才排除"读数对了却拦不住 core"
// 这类缺陷（例如加固被放在 fork 之后、或内核层面 core_pattern 绕过 rlimit）。
//
// 走生产同一条路：wrapWithPreExecHelper（Restrict 内部调用它）→ 真实执行 → helper
// 在 execve 前 applyCoreDumpHardeningToSelf → 目标 `sh` 真发 SIGSEGV。
//
// ★ 控制组不可省：先证明"本环境确实能落出 core"（宿主 RLIMIT_CORE 非零 + core_pattern
// 可落盘）。没有这一半，实验组的"没落 core"可能只是宿主默认就不落盘 ⇒ 判据恒真。
//
// 反例注入（判别力自证）：删掉 RunPreExecHelper 第 7 步整块 ⇒ 目标继承测试进程被抬高的
// RLIMIT_CORE ⇒ 既读到非零、又真的落出 core ⇒ 两个子断言全红。
//
// 假绿源（两类，均须显式退避而非静默通过）：core_pattern 以 "|" 开头（core 交给管道
// 程序，磁盘上必然没有）、含 "/"（core 落在别处，cwd 里必然没有）。两者的替代判据
// 都是"读回目标进程 RLIMIT_CORE 实际值"（rlimit-readback 子断言，对 core_pattern 不敏感）。
func TestSBX020_E2E_SegfaultLeavesNoCore(t *testing.T) {
	if _, err := os.Stat("/proc/self/limits"); err != nil {
		t.Skipf("/proc 不可用，跳过 E2E: %v", err)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh 不可用，跳过 E2E: %v", err)
	}

	// ---- 先探 core_pattern（决定"落盘"信号在本环境是否可用）----
	pattern, perr := sbx020CorePattern()
	if perr != nil {
		t.Skipf("读不到 /proc/sys/kernel/core_pattern（%v）⇒ 无法判断 core 落盘策略，本判据不适用", perr)
	}
	t.Logf("core_pattern = %q", pattern)

	// 抬高本进程 RLIMIT_CORE **软上限**（只动软上限 ⇒ 恢复永远可逆），
	// 使控制组真的能落出 core。
	const want = 256 << 20
	restore, ok := sbx020RaiseCoreLimit(want)
	if !ok {
		t.Skipf("宿主 RLIMIT_CORE 硬上限不允许把软上限抬到 %d 字节 ⇒ 无法构造「能落 core」的基线，"+
			"本判据不具判别性（未验证边界记入回执）", want)
	}
	defer restore()

	// ---- 控制组：不经 helper ⇒ 必须真的落出 core ----
	ctrlDir := t.TempDir()
	ctrl := sbx020RunSegvTarget(t, ctrlDir, nil)
	t.Logf("控制组（不经 helper）: ulimit -c=%q 可解析=%v cores=%v wait=%v",
		ctrl.ulimitCLine, ctrl.ulimitOK, ctrl.cores, ctrl.waitErr)
	if !ctrl.ulimitOK || ctrl.ulimitN == 0 {
		t.Skipf("控制组 `ulimit -c` 读数 %q 不能确认非零 ⇒ 基线不成立（此时实验组读到 0 说明不了"+
			"任何事），本判据不具判别性", ctrl.ulimitCLine)
	}

	// ---- 实验组：经 pre-exec helper（生产同一入口）----
	expDir := t.TempDir()
	// ResourceLimits 非零 ⇒ helper 必被召唤（与 SBX-001 同入口）。
	limits := ResourceLimits{CPUMaxSeconds: 300}
	exp := sbx020RunSegvTarget(t, expDir, &LinuxSandboxConfig{ResourceLimits: limits})
	t.Logf("实验组（经 helper）: ulimit -c=%q 可解析=%v cores=%v wait=%v",
		exp.ulimitCLine, exp.ulimitOK, exp.cores, exp.waitErr)

	// 子断言 1 —— 替代判据（对 core_pattern 不敏感，任何环境都成立）：
	// 经 helper 的目标进程自报的 RLIMIT_CORE 必须是 0。
	t.Run("rlimit-readback", func(t *testing.T) {
		if !exp.ulimitOK || exp.ulimitN != 0 {
			t.Fatalf("SBX-020 未生效：经 helper 的目标进程 `ulimit -c`=%q，want 0（控制组读到 %q）"+
				"—— 加固未在 execve 前施加到目标命令上", exp.ulimitCLine, ctrl.ulimitCLine)
		}
	})

	// 子断言 2 —— 结果面：真发 SIGSEGV 后，目标进程的 cwd 内不得出现 core 文件。
	t.Run("no-core-file", func(t *testing.T) {
		// ★ 假绿源必须先排除：core_pattern 若是「管道式」（交给 coredump 程序读走）
		// 或「绝对路径式」（落在别处），那么"目标 cwd 内没找到 core"是**必然**结果，
		// 与 RLIMIT_CORE 是否被清 0 无关 ⇒ 判据恒真、不具判别力。
		// 此处显式识别并退避到替代判据，不得静默通过。
		if why := sbx020DiskSignalUnusableReason(pattern); why != "" {
			t.Skipf("假绿源：%s。替代判据 = 读回目标进程 RLIMIT_CORE 实际值"+
				"（见 TestSBX020_CoreLimitClearedByHelper 与本次 rlimit-readback 子断言）", why)
		}
		if len(ctrl.cores) == 0 {
			t.Skipf("控制组未在 %s 落出 core（pattern=%q）⇒ 本环境的「落盘」信号不可用，无法区分"+
				"「helper 生效」与「本来就不落盘」，结果面判据失效（未验证边界记入回执）",
				ctrlDir, pattern)
		}
		if len(exp.cores) != 0 {
			t.Fatalf("SBX-020 未生效：经 helper 的目标真发 SIGSEGV 后，仍在 %s 落出 core %v"+
				"（控制组 %v）—— 沙箱进程的内存镜像会被写进工作区", expDir, exp.cores, ctrl.cores)
		}
	})
}

// TestSBX020_DiskSignalUsability 判据⑤：假绿源识别器**自身**的判别力。
//
// 上面那条 E2E 的"结果面"子断言依赖一个前提：core_pattern 允许 core 落进目标进程的
// cwd。若不成立，它必须**显式 Skip** 而不是静默通过。本用例证明那个识别器真的能区分
// 三种输入 —— 三种输入各自产生不同结论 ⇒ 判别器非恒真：
//
//	落盘式（"core" / "core.%p"）          ⇒ 可用（返回空）
//	管道式（"|/usr/lib/systemd/..."）     ⇒ 不可用（返回原因，含"永远"字样）
//	绝对路径式（"/var/crash/core.%p"）    ⇒ 不可用（返回原因）
//
// 反例注入：把 sbx020DiskSignalUnusableReason 改成恒返回 "" ⇒ 管道式/绝对路径式两条
// 子用例变红（它们期望"不可用"）。
func TestSBX020_DiskSignalUsability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pattern  string
		unusable bool
	}{
		{"落盘式_本容器实际值", "core", false},
		{"落盘式_带pid占位", "core.%p", false},
		{"管道式_systemd-coredump", "|/usr/lib/systemd/systemd-coredump %P %u %g", true},
		{"绝对路径式", "/var/crash/core.%p", true},
		{"管道式_任意外部程序", "|/usr/bin/my-coredumpd %p", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sbx020DiskSignalUnusableReason(tc.pattern)
			if tc.unusable && got == "" {
				t.Fatalf("pattern=%q 应被判为不可用（假绿源），却判为可用"+
					"⇒ I-8.7 在该宿主机上会**静默通过**而不是显式 Skip", tc.pattern)
			}
			if !tc.unusable && got != "" {
				t.Fatalf("pattern=%q 应判为可用，却被判不可用: %s", tc.pattern, got)
			}
		})
	}
}
