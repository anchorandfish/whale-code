//go:build linux

// Package sandbox — SBX-001 判别性测试：资源限制必须在 execve **之前**施加。
//
// # 病灶（修复前）
//
// 资源限制走 platformApplyResourceLimits → unix.Prlimit(pid, ...)，
// 而 Prlimit 只能作用于**已存在**的进程 ⇒ 调用点必然在 cmd.Start() 之后
// （resource_enforce_linux.go:19 明写"调用时机：cmd.Start() 之后"）。
// Start() 与 prlimit() 之间存在一段**无限制窗口**，进程炸弹专打这个窗口。
//
// # 正解
//
// 仓库已有「execve 前的加固阶段」——pre-exec helper（PreExecSpec 信封，
// 机制中立，见 os_sandbox_linux_preexec.go 文件头）。把资源限制作为信封的
// 一个新字段交给 helper，在 execve 前 setrlimit，窗口物理归零。
//
// # 判据设计（为什么这些可判别）
//
// 结构面判据（cmd.Args / cmd.Env / 分支可达性）：不打票面，每条都能被
// "删除实现"打红，且都在注释里写明反例注入方式。
//
// ⚠️ 边界：**不要**用「读自己 getrlimit 看值对不对」去区分「helper 版 vs
// prlimit 版」——两者设的是同一个数值，那种用法不可判别（会假绿）。
// 但同一条读数用在**另一个问题**上是可判别的：TestSBX001_E2E_* 问的是
// 「helper 里的 setrlimit 到底有没有被执行到」，反例注入 = 删掉
// RunPreExecHelper 第 6 步 ⇒ 读到宿主默认值 ⇒ 必红（且无 prlimit 补位）。
//
// ⚠️ E2E 使用**生产默认 MemoryMaxBytes（1GiB）**：这不是随手取的数，
// 而是要顺带回答一个真实风险——helper 自身是 Go 进程，虚拟地址空间占用
// 不小，若 AS 上限设得比它剩余需求还小，helper 会在 execve 前就挂掉。
// 用生产同值跑通，才算证明该上限不会自伤。
package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// newSBX001Sandbox 构造已 Enter 的 linuxNamespaceSandbox（不启动任何进程）。
func newSBX001Sandbox(cfg LinuxSandboxConfig) *linuxNamespaceSandbox {
	return &linuxNamespaceSandbox{
		cfg:    cfg,
		handle: &linuxNamespaceHandle{Namespaces: cfg.Namespaces},
	}
}

// specFromCmd 解析 cmd.Env 中的 pre-exec 信封；缺失或损坏时返回 nil。
func specFromCmd(cmd *exec.Cmd) *PreExecSpec {
	for _, kv := range cmd.Env {
		if !strings.HasPrefix(kv, preExecSpecEnv+"=") {
			continue
		}
		var spec PreExecSpec
		if err := json.Unmarshal([]byte(strings.TrimPrefix(kv, preExecSpecEnv+"=")), &spec); err != nil {
			return nil
		}
		return &spec
	}
	return nil
}

// TestSBX001_SpecCarriesResourceLimits 判据①：结构面 —— 信封必须携带 resource_limits。
//
// 断言：Restrict 后，信封 JSON 中的 resource_limits 与配置逐字段相等。
//
//	before 红：信封无该字段 ⇒ spec.ResourceLimits == nil ⇒ Fail
//	反例注入：删掉 buildPreExecSpec 中填充 ResourceLimits 的分支 ⇒ 必须变红
func TestSBX001_SpecCarriesResourceLimits(t *testing.T) {
	limits := ResourceLimits{
		CPUMaxSeconds:     42,
		MemoryMaxBytes:    1 << 30,
		FileDescriptorMax: 512,
		ProcessMax:        77,
		FileSizeMaxBytes:  10 << 20,
	}
	// UseSeccomp 让 helper 必被召唤（与本次改动正交，避免依赖 Landlock 路径解析）
	sb := newSBX001Sandbox(LinuxSandboxConfig{
		UseNamespaces:  true,
		Namespaces:     []string{"pid"},
		UseSeccomp:     true,
		ResourceLimits: limits,
	})

	cmd := exec.Command("/bin/true")
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}

	spec := specFromCmd(cmd)
	if spec == nil {
		t.Fatal("cmd.Env 未携带 pre-exec 信封 —— helper 未被召唤")
	}
	if spec.ResourceLimits == nil {
		t.Fatal("SBX-001 未修复：信封未携带 resource_limits ⇒ 资源限制只能靠 Start() 之后的 prlimit（有窗口）")
	}
	if *spec.ResourceLimits != limits {
		t.Fatalf("resource_limits 往返不一致:\n got %+v\nwant %+v", *spec.ResourceLimits, limits)
	}
}

// TestSBX001_HelperSummonedWhenOnlyResourceLimits 判据②：召唤条件 —— 只配资源限制也必须召唤 helper。
//
// 场景：Landlock 与 seccomp 都关（例如内核 <5.13 且用户关了 seccomp），
// 但配置了资源限制。修复前 helper 不会被召唤 ⇒ 资源限制只能落回 prlimit。
//
//	before 红：buildPreExecSpec 返回 "no pre-exec restriction requested" ⇒ Restrict 报错
//	反例注入：把 Restrict 召唤条件中的 needsResourceLimits 去掉 ⇒ 必须变红
func TestSBX001_HelperSummonedWhenOnlyResourceLimits(t *testing.T) {
	sb := newSBX001Sandbox(LinuxSandboxConfig{
		UseNamespaces:  true,
		Namespaces:     []string{"pid"},
		UseLandlock:    false,
		UseSeccomp:     false,
		ResourceLimits: ResourceLimits{CPUMaxSeconds: 10, ProcessMax: 8},
	})

	cmd := exec.Command("/bin/true")
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("SBX-001 未修复：只配资源限制时 helper 未被召唤（Restrict 报错）: %v", err)
	}
	if len(cmd.Args) < 2 || cmd.Args[1] != SandboxExecHelperArg {
		t.Fatalf("cmd.Args = %v；期望 cmd.Args[1] == %q（helper 未被召唤）", cmd.Args, SandboxExecHelperArg)
	}
	if spec := specFromCmd(cmd); spec == nil || spec.ResourceLimits == nil {
		t.Fatal("helper 已被召唤，但信封未携带 resource_limits")
	}
}

// TestSBX001_FallbackSkipsPrlimitWhenCarried 判据③：分流面 —— 已携带时不得再走 prlimit。
//
// 判别手法（不依赖竞态、不需要真跑进程）：
// 传入**未启动**的 cmd（cmd.Process == nil），于是两条分支的结果必然不同：
//
//	走 prlimit 分支 ⇒ 必然返回 "cmd or process is nil" 错误
//	走 no-op 分支 ⇒ 返回 nil
//
//	before 红：无分流逻辑 ⇒ 走 prlimit ⇒ 返回错误 ⇒ Fail
//	反例注入：删掉 platformApplyResourceLimits 开头的分流判断 ⇒ 必须变红
func TestSBX001_FallbackSkipsPrlimitWhenCarried(t *testing.T) {
	limits := ResourceLimits{CPUMaxSeconds: 42, ProcessMax: 16}
	sb := newSBX001Sandbox(LinuxSandboxConfig{
		UseNamespaces:  true,
		Namespaces:     []string{"pid"},
		UseSeccomp:     true,
		ResourceLimits: limits,
	})

	cmd := exec.Command("/bin/true")
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	if cmd.Process != nil {
		t.Fatal("前置条件不成立：cmd 不应已启动")
	}
	if err := platformApplyResourceLimits(cmd, limits, nil); err != nil {
		t.Fatalf("SBX-001 未修复：信封已携带资源限制，却仍走了 prlimit 兜底路径: %v", err)
	}
}

// TestSBX001_FallbackStillTriesWhenNotCarried 反向对照：防判据③恒真。
//
// 断言：信封**未**携带时，prlimit 兜底必须仍然尝试施加（裸 cmd 因 Process==nil 报错）。
// 这条在 before 与 after 都应通过 —— 它的作用是证明判据③的**两条分支确实不同**，
// 而不是"任何输入都返回 nil"的恒真判据（假绿防线）。
func TestSBX001_FallbackStillTriesWhenNotCarried(t *testing.T) {
	limits := ResourceLimits{CPUMaxSeconds: 42}
	cmd := exec.Command("/bin/true") // 裸 cmd：无信封

	err := platformApplyResourceLimits(cmd, limits, nil)
	if err == nil {
		t.Fatal("裸 cmd（无信封）应走 prlimit 分支并因 Process==nil 报错；" +
			"返回 nil 说明分流判据恒真，判据③失去判别力")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("期望 nil-process 错误，实际: %v", err)
	}
}

// TestSBX001_EmptySpecStillRejected 回归：空信封仍必须 fail-closed。
//
// 加了新字段后，isEmpty 的语义不能变宽到"信封完全为空也接受" ——
// 那会让 helper 在"什么都没装"的情况下 exec，制造虚假保护。
func TestSBX001_EmptySpecStillRejected(t *testing.T) {
	empty := PreExecSpec{}
	if !empty.isEmpty() {
		t.Fatal("空信封必须被判定为 isEmpty（helper 应 fail-closed 拒绝执行）")
	}
	withLimits := PreExecSpec{ResourceLimits: &ResourceLimits{CPUMaxSeconds: 1}}
	if withLimits.isEmpty() {
		t.Fatal("只携带 resource_limits 的信封不得被判为空（否则 helper 拒绝执行有效请求）")
	}
}

// TestSBX001_E2E_HelperAppliesRlimitsBeforeExec 端到端（生产真实路径）。
//
// 与上面三条结构面判据的分工：
// - 结构面证明「信封带上了 / helper 被召唤了 / 兜底让路了」；
// - 本测试证明「helper 真的把限制设上了，且目标命令及其后代继承」。
//
// 走的是生产同一条路径：wrapWithPreExecHelper（Restrict 内部调用它）
// → 真实执行 → 在目标进程内读回 /proc/self/limits。
// 目标是 sh 的**子进程** cat：它的 rlimit 承自 sh ⇒ 同时证明「限制随
// execve 与 fork 继承」，这正是生产里"命令及其后代都受限"所依赖的性质。
//
// ★ 判别力来源：断言全部用**非宿主默认值**（NPROC 见下方说明 / 321 / 111 / 64MiB）。
// 宿主默认多为 unlimited 或系统级上限，两者必不相等。
// 反例注入：删掉 RunPreExecHelper 的「第 6 步」（applyResourceLimitsToSelf
// 调用）⇒ 目标读到宿主默认值 ⇒ 本测试变红。
// 本测试不经 platformApplyResourceLimits，故不存在 prlimit 补位造成的假绿。
func TestSBX001_E2E_HelperAppliesRlimitsBeforeExec(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh 不可用，跳过 E2E: %v", err)
	}
	if _, err := os.Stat("/proc/self/limits"); err != nil {
		t.Skipf("/proc 不可用，跳过 E2E: %v", err)
	}
	// 环境前提随失败一起打印（t.Log 在通过时静默，不污染输出）：
	// RLIMIT_NPROC 是按 **UID** 统计的（内核 task 计数），不是按进程 ——
	// 共享 runner 上其他并发测试进程也算在这个 UID 头上，且「把硬上限设到
	// 比现有硬上限更高」会被内核拒绝。这两件事都会让本用例在 CI 上红，
	// 但都不是「限制没在 execve 前施加」⇒ 必须先有环境证据，才能判真缺陷。
	logAmbientProcLimits(t)

	const (
		// AS 用**生产默认值**（resource_enforce_common.go）：SBX-021 之后该值
		// **不应**被施加到命令上 —— 这里保留它正是为了做反向断言（见函数末尾）。
		wantAS     = 1 << 30
		wantNofile = 321
		wantCPU    = 111
		wantFSIZE  = 64 << 20
	)
	// ★ NPROC 不能像其他三项那样写死一个小常量（2026-10-22 CI 实测）：
	// 内核按**真实 UID** 统计 task 数（不是按进程），所以 RLIMIT_NPROC=77 的
	// 真实含义是"本 UID 全机只许 77 个 task"。CI runner 上同一个 UID 早就有
	// 上百个 task（`go test ./...` 并行包 + race 线程），于是 helper 起来后
	// `sh` 一 fork 就 EAGAIN：
	// /usr/bin/sh: 1: Cannot fork （exit 2，本用例红）
	// 这与被测性质（限制是否在 execve 前施加）无关，是"测试把自己饿死了"。
	// ⇒ 断言值取「当前 UID 已有 task 数 + 余量」：既远低于宿主默认值 58121
	// （保持判别力：真没施加就会读到 58121），又不会掐死自己的 fork。
	wantProcs, nprocOK := nprocWantForTest(t)
	if !nprocOK {
		return // 已在内部 Skip，给出原因
	}
	limits := ResourceLimits{
		MemoryMaxBytes:    wantAS,
		ProcessMax:        wantProcs,
		FileDescriptorMax: wantNofile,
		CPUMaxSeconds:     wantCPU,
		FileSizeMaxBytes:  wantFSIZE,
	}

	ws := t.TempDir()
	cmd := exec.Command("sh", "-c", "cat /proc/self/limits")
	cmd.Dir = ws
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin"}

	if err := wrapWithPreExecHelper(cmd, LinuxSandboxConfig{ResourceLimits: limits}); err != nil {
		t.Fatalf("wrapWithPreExecHelper: %v", err)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		// 两类原因必须分开说，否则会把人骗去查错地方（2026-10-22 实证）：
		// - "Cannot fork" ⇒ 进程配额（RLIMIT_NPROC / cgroup pids）不够，属环境或配置问题；
		// - 其它（含 exec 报错）⇒ 才是 helper 自身装不上，属实现缺陷。
		hint := "属实现缺陷"
		if strings.Contains(string(out), "Cannot fork") || strings.Contains(string(out), "Resource temporarily unavailable") {
			hint = "目标进程无法 fork：RLIMIT_NPROC 是**按 UID** 的绝对额度，当前 UID 已有 task 数超过它（环境/配置问题，不是 helper 没施加限制）"
		}
		t.Fatalf("helper 执行失败（%s）: %v\n--- 输出 ---\n%s", hint, runErr, out)
	}

	got := parseProcSelfLimits(string(out))
	for _, tc := range []struct {
		name string
		want uint64
	}{
		{"Max processes", wantProcs},
		{"Max open files", wantNofile},
		{"Max cpu time", wantCPU},
		{"Max file size", wantFSIZE},
		// ★ SBX-021：不再断言 "Max address space" —— RLIMIT_AS 已在 Linux 退役。
		// 它仍留在上面的 limits 里（模拟生产配置确实声明了内存上限），正是为了
		// 证明"声明了也不施加"。反向断言见本函数末尾。
	} {
		v, ok := got[tc.name]
		if !ok {
			t.Fatalf("/proc/self/limits 未解析出 %q；原始输出:\n%s", tc.name, out)
		}
		if v != tc.want {
			t.Errorf("%s = %d, want %d —— 限制未在 execve 前设上（或未继承到子进程）",
				tc.name, v, tc.want)
		}
	}

	// 反向断言（SBX-021）：配置里声明了 MemoryMaxBytes=1GiB，但 Linux 侧该限制已退役
	// ⇒ 目标进程**不得**读到这个值。宿主默认多为 unlimited（parseProcSelfLimits 不收录
	// 该情形），故"压根不存在"也算通过 —— 本断言只抓"退役后又被偷偷施加回去"。
	if v, ok := got["Max address space"]; ok && v == wantAS {
		t.Errorf("Max address space = %d（=配置值）—— RLIMIT_AS 已在 Linux 退役（SBX-021），不应被施加", v)
	}
}

// logAmbientProcLimits 打印本进程继承到的进程数上限与当前 cgroup pids 用量。
//
// 为什么需要（2026-10-22 CI 实测）：本用例在 CI 上红了，但注解里只有用例名 ——
// 分不清「helper 没在 execve 前设限制」（真缺陷）与「runner 环境不允许把
// RLIMIT_NPROC 设成 77」（环境前提不成立）。这两者的**判别证据**就是环境本身：
// - ambient hard < want ⇒ setrlimit 会 EPERM（我们的值是"抬高硬上限"，非法）
// - cgroup pids.current 已接近 want ⇒ fork 会 EAGAIN（不是 rlimit 的问题）
func logAmbientProcLimits(t *testing.T) {
	t.Helper()
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &rl); err == nil {
		t.Logf("env: ambient RLIMIT_NPROC soft=%d hard=%d uid=%d euid=%d",
			rl.Cur, rl.Max, os.Getuid(), os.Geteuid())
	} else {
		t.Logf("env: Getrlimit(RLIMIT_NPROC) 失败: %v", err)
	}
	for _, p := range []string{"/sys/fs/cgroup/pids.current", "/sys/fs/cgroup/pids.max"} {
		if b, err := os.ReadFile(p); err == nil {
			t.Logf("env: %s=%s", p, strings.TrimSpace(string(b)))
		}
	}
}

// nprocWantForTest 给出本用例应当断言的 RLIMIT_NPROC 值（第二个返回值为 false 表示已 Skip）。
//
// 为什么不写死常量（2026-10-22 CI 实测）：RLIMIT_NPROC 由内核按**真实 UID** 统计
// task 数，值是「这个 UID 全机还能有多少个 task」的**绝对上限**，不是"本进程额度"。
// 写死 77 在单机单人环境能过，在 CI runner（同 UID 跑着并行测试包 + race 线程，
// 实测 ambient soft/hard 均为 58121）上直接表现为 `/usr/bin/sh: 1: Cannot fork`，
// 失败信息还长得像"产品缺陷"。
//
// 判据要的是「配置值是否真的被施加」，所以值只需满足两点：
// 1. 与宿主默认值不同（否则"没施加"也会读到同一个数 ⇒ 假绿）；
// 2. 高于当前 UID 的 task 数 + 余量（否则测试自己 fork 不了 ⇒ 假红）。
//
// 环境两条都不满足时只能 Skip（并说明），不能改成"断言 <= 某值"——那会失去判别力。
func nprocWantForTest(t *testing.T) (uint64, bool) {
	const slack = 1024 // 并行测试包在测量后仍可能新增线程；1024 远小于宿主默认值 58121
	ambient, ok := ambientTaskCount()
	if !ok {
		// 数不出来就不敢断言（宁可 Skip，也不要用一个可能饿死自己的值）
		t.Skip("无法统计当前 UID 的 task 数，不能安全选择 RLIMIT_NPROC 断言值")
		return 0, false
	}
	want := uint64(ambient) + slack
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &rl); err == nil &&
		rl.Max != unix.RLIM_INFINITY && want >= rl.Max {
		t.Skipf("当前 UID 的 task 数=%d，加余量后 %d 已不低于硬上限 %d，无法既施加又不自伤",
			ambient, want, rl.Max)
		return 0, false
	}
	return want, true
}

// ambientTaskCount 统计当前**真实 UID** 的 task 数（内核 RLIMIT_NPROC 的计数口径）。
//
// 逐个 /proc/<pid>/status 取 Uid 行比对，命中后数 /proc/<pid>/task/* ——
// 直接按进程数统计会漏掉线程，而 NPROC 数的是 task。
func ambientTaskCount() (int, bool) {
	uid := os.Getuid()
	pids, err := filepath.Glob("/proc/[0-9]*")
	if err != nil || len(pids) == 0 {
		return 0, false
	}
	total := 0
	for _, pidDir := range pids {
		b, err := os.ReadFile(filepath.Join(pidDir, "status"))
		if err != nil {
			continue // 进程已退出：正常竞态
		}
		if !statusBelongsToUID(string(b), uid) {
			continue
		}
		tasks, err := filepath.Glob(filepath.Join(pidDir, "task", "[0-9]*"))
		if err != nil {
			continue
		}
		total += len(tasks)
	}
	return total, total > 0
}

// statusBelongsToUID 判断 /proc/<pid>/status 的 "Uid:" 行（四列：real/effective/saved/fs）
// 的第一列是否等于 uid。
func statusBelongsToUID(status string, uid int) bool {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) == 0 {
			return false
		}
		n, err := strconv.Atoi(fields[0])
		return err == nil && n == uid
	}
	return false
}

// parseProcSelfLimits 解析 /proc/self/limits 的「软限制」列。
//
// 内核用固定列宽输出：%-25s %-20s %-20s %-10s（name / soft / hard / units），
// 因此按列切分比按空白分词更稳（列名有 2 词也有 3 词）。
// 值为 "unlimited" 的项不收录（本测试断言的都是有限值）。
func parseProcSelfLimits(s string) map[string]uint64 {
	m := make(map[string]uint64)
	for _, line := range strings.Split(s, "\n") {
		if len(line) < 45 {
			continue
		}
		name := strings.TrimSpace(line[0:25])
		soft := strings.TrimSpace(line[25:45])
		if name == "" || name == "Limit" || soft == "" || soft == "unlimited" {
			continue
		}
		n, err := strconv.ParseUint(soft, 10, 64)
		if err != nil {
			continue
		}
		m[name] = n
	}
	return m
}
