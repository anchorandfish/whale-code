//go:build linux

// Package sandbox — SBX-022 判别性测试：Linux 侧 RLIMIT_NPROC **退役**。
//
// # 病灶（CI 实证，不是推断）
//
// RLIMIT_NPROC 的计量单位是「该**真实 UID** 在整机上持有的 task 总数（含线程）」，
// 不是「本进程能开多少子进程」。把它设成 256（`DefaultResourceLimits().ProcessMax`）
// 等于宣称「我这个用户全机只许有 256 个 task」。
//
// 桌面 Linux 用户常态就有几百个 task（浏览器 / IDE / 终端），于是沙箱内每条命令
// 一 fork 就 EAGAIN：
//
//	/usr/bin/sh: 1: Cannot fork
//
// 2026-10-22 CI 注解原文（SBX-001 E2E 因此变红）：
//
//	env: ambient RLIMIT_NPROC soft=58121 hard=58121 uid=1001
//	helper 执行失败: exit status 2 / /usr/bin/sh: 1: Cannot fork
//
// # 为何是「退役」而不是「调大」/「改成相对值」
//
// 用户 2026-10-22 拍板方案 A：Linux 侧不再用该限制；进程数门改由 cgroup v2
// `pids.max` 承担 —— 后者的计量单位是**本沙箱的 cgroup**，语义正确
// （与 SBX-021 内存门同一条路线 SBX-019，均尚未落地）。
//
//   - 调大：只是把"饿死线"挪高，fork bomb 同样能开更多，量纲没变；
//   - 改相对值（ambient+N）：重定义了"上限"的语义，用户以为的绝对值其实随时漂移；
//   - 退役：承认这把尺子量错了对象（对**共享 UID** 收绝对值），与 SBX-021 一致的处置。
//
// # 判据设计（结构面 + 申报面 + 端到端）
//
//	判据①  resourceLimitTargets 不得含 RLIMIT_NPROC        （结构面）
//	判据②  申报必须点明 NPROC 退役，且不得再宣称它在施加   （申报面：不许静默）
//	判据③  生产默认 ProcessMax 不得出现在目标命令的 limits  （端到端 + 正向对照）
//
// 每条都写明 before 红与反例注入方式。
package sandbox

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestSBX022_NPROCNotInTargets 判据①（结构面）：Linux 侧不得再对 RLIMIT_NPROC 施加限制。
//
// 断言 `resourceLimitTargets`（helper 主路径与 prlimit 兜底的**单一维护点**）不含
// RLIMIT_NPROC。用**资源号**而非显示名比较：改名绕不过本判据。
//
//	before 红：NPROC 行仍在表中 ⇒ 命中 ⇒ Fail
//	反例注入：把 {unix.RLIMIT_NPROC, limits.ProcessMax, "NPROC"} 加回该表 ⇒ 必须变红
func TestSBX022_NPROCNotInTargets(t *testing.T) {
	limits := DefaultResourceLimits() // 生产默认：ProcessMax=256，正是会饿死桌面用户的那个值
	for _, target := range resourceLimitTargets(limits) {
		if target.resource == unix.RLIMIT_NPROC {
			t.Fatalf("Linux 侧仍在施加 RLIMIT_NPROC（name=%q value=%d）—— SBX-022 退役未生效；"+
				"该限制按**真实 UID** 的全机 task 总数计量，不是本进程额度，"+
				"共享 UID 上会把命令饿死（/usr/bin/sh: 1: Cannot fork）",
				target.name, target.value)
		}
	}
}

// TestSBX022_RetirementIsAnnounced 判据②（申报面）：配置了资源限制时必须申报 NPROC 退役。
//
// 依据："无出口的降级不可见"（status.go 文件头，静默防护教训）。
// 既不能静默消失，也不能"一边宣称在施加、一边实际退役" —— 后者比不说更糟。
//
//	before 红：旧文案写的是 "CPU/NOFILE/NPROC/FSIZE enforced"，不含 "RLIMIT_NPROC" ⇒ Fail
//	反例注入：让 linuxRlimitDegradationNotice 回到旧文案（宣称 NPROC enforced）⇒ 必须变红
func TestSBX022_RetirementIsAnnounced(t *testing.T) {
	notice := linuxRlimitDegradationNotice(DefaultResourceLimits())
	if notice == "" {
		t.Fatal("配置了资源限制却无降级申报 —— NPROC 退役成了**静默降级**（用户会以为进程数门还在）")
	}
	if !strings.Contains(notice, "RLIMIT_NPROC") {
		t.Fatalf("申报未点明被退役的限制名（用户无法据此判断失去的是哪道门）：%q", notice)
	}
	if !strings.Contains(notice, "retired") {
		t.Fatalf("申报点了名但未说明已退役，用户读不出「这道门没了」：%q", notice)
	}
	// 反向断言：不许留下"NPROC 仍在施加"的旧说法（两个事实同时出现在一条申报里 = 自相矛盾）
	if strings.Contains(notice, "NPROC/FSIZE enforced") {
		t.Fatalf("申报自相矛盾：一边说 NPROC 已退役，一边仍宣称它在施加：%q", notice)
	}
}

// TestSBX022_E2E_ProcessMaxNotApplied 判据③（端到端，生产真实路径）。
//
// 走生产同一条路径：wrapWithPreExecHelper → 真实执行 → 在目标进程内读 /proc/self/limits。
// 配置用 `DefaultResourceLimits()`（生产默认，含 ProcessMax=256）。
//
// ★ 必须有**正向对照**：同一份输出里 CPU / NOFILE / FSIZE 三项**必须**等于配置值。
// 否则"Max processes 没变"可能只是 helper 压根没跑（空跑假绿）。
//
// 断言（两种环境都成立）：
//
//   - 父进程 NPROC 有限 ⇒ 子进程必须等于父进程值（继承，未被改写）；
//
//   - 父进程 NPROC unlimited ⇒ 子进程也不得出现有限值。
//
//     before 红：helper 会把 NPROC 设成 256 ⇒ 子进程读到 256（≠ 父进程 ambient），
//     或在 CI 上直接 `/usr/bin/sh: 1: Cannot fork`（exit 2）
//     反例注入：把 NPROC 行加回 resourceLimitTargets ⇒ 必须变红
func TestSBX022_E2E_ProcessMaxNotApplied(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh 不可用，跳过 E2E: %v", err)
	}
	if _, err := os.Stat("/proc/self/limits"); err != nil {
		t.Skipf("/proc 不可用，跳过 E2E: %v", err)
	}

	ambient, ambientUnlimited, ok := ambientNproc(t)
	if !ok {
		return // 已在内部 Skip
	}
	logAmbientProcLimits(t) // 环境证据随失败一起打印

	limits := DefaultResourceLimits()
	cmd := exec.Command("sh", "-c", "cat /proc/self/limits")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin"}
	if err := wrapWithPreExecHelper(cmd, LinuxSandboxConfig{ResourceLimits: limits}); err != nil {
		t.Fatalf("wrapWithPreExecHelper: %v", err)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		hint := "属实现缺陷"
		if strings.Contains(string(out), "Cannot fork") || strings.Contains(string(out), "Resource temporarily unavailable") {
			hint = "目标进程无法 fork ⇒ NPROC 仍被施加（按 UID 的绝对额度把命令饿死了）"
		}
		t.Fatalf("helper 执行失败（%s）: %v\n--- 输出 ---\n%s", hint, runErr, out)
	}

	got := parseProcSelfLimits(string(out))

	// 正向对照：证明 helper 真的执行并施加了**该施加的**那几项（防"helper 没跑"假绿）
	for _, tc := range []struct {
		name string
		want uint64
	}{
		{"Max cpu time", limits.CPUMaxSeconds},
		{"Max open files", limits.FileDescriptorMax},
		{"Max file size", limits.FileSizeMaxBytes},
	} {
		v, ok := got[tc.name]
		if !ok {
			t.Fatalf("/proc/self/limits 未解析出 %q（正向对照失败：helper 可能没跑）；原始输出:\n%s", tc.name, out)
		}
		if v != tc.want {
			t.Errorf("%s = %d, want %d —— 该施加的限制没在 execve 前设上", tc.name, v, tc.want)
		}
	}

	// 判据③：NPROC 必须保持父进程原值（= 没被施加）
	if ambientUnlimited {
		if v, ok := got["Max processes"]; ok {
			t.Errorf("Max processes = %d —— 父进程为 unlimited，退役后子进程不得被改成有限值", v)
		}
		return
	}
	v, ok := got["Max processes"]
	if !ok {
		t.Errorf("父进程 NPROC=%d（有限），子进程却显示 unlimited —— 说明限制被改写过", ambient)
		return
	}
	if v != ambient {
		t.Errorf("Max processes = %d, 期望继承父进程 %d —— 生产默认 ProcessMax=%d 不得被施加"+
			"（RLIMIT_NPROC 按 UID 全机 task 数计量，不是本进程额度）",
			v, ambient, limits.ProcessMax)
	}
}

// ambientNproc 读取父进程（= 未来子进程的继承基线）的 RLIMIT_NPROC 软上限。
//
// 第二个返回值表示是否为 unlimited；第三个为 false 表示已 Skip（读不到就不敢断言）。
func ambientNproc(t *testing.T) (uint64, bool, bool) {
	t.Helper()
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &rl); err != nil {
		t.Skipf("Getrlimit(RLIMIT_NPROC) 失败，无法确定继承基线: %v", err)
		return 0, false, false
	}
	if rl.Cur == unix.RLIM_INFINITY {
		return 0, true, true
	}
	return rl.Cur, false, true
}
