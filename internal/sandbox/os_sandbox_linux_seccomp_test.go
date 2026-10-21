//go:build linux

// Package sandbox — seccomp 接线与常量等价性判据（P2-3）。
//
// 三层判据，各管一件事：
//
// 1. 等价性：本包就地声明的 UAPI 常量 / 结构偏移，与 x/sys/unix 及内核
// 布局必须逐项相等（就地声明是跨平台编译的代价，此处把它钉死）。
// 2. 生产清单语义：拿**真实的** seccompDeniedSyscalls() 构造程序，
// 用解释器执行，断言逃逸原语被拒、普通 syscall 放行、ABI 错位被杀。
// 这一条确保"生产用的那份名单"真的进了过滤器，而不是测试自造的名单。
// 3. 结构修复判别器：UseLandlock=false + UseSeccomp=true 时，
// pre-exec helper 仍必须被召唤，且信封里只有 seccomp。
// （改动前：helper 召唤条件只看 UseLandlock ⇒ 不召唤 ⇒ 本用例红。）
//
// 内核级 e2e（真实拦截 ptrace/io_uring）在沙箱守卫攻击测试中：
// sandbox_guard_attack_linux_test.go 的 TestGuard_Attack_SeccompBlocksEscapePrimitives。
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestSeccompUAPIConstants_MatchXSys 就地声明的常量必须与 x/sys/unix 逐项相等。
//
// 动机：seccomp_program.go 为了能在 windows/darwin 编译而自带一份 UAPI 常量。
// 自带即可能写错，写错的后果是"过滤器语义与预期不符"（最坏是静默放行）。
// 因此在 Linux 侧把它们与权威定义对齐比较。
func TestSeccompUAPIConstants_MatchXSys(t *testing.T) {
	consts := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"seccompRetKillProcess", seccompRetKillProcess, uint32(unix.SECCOMP_RET_KILL_PROCESS)},
		{"seccompRetErrno", seccompRetErrno, uint32(unix.SECCOMP_RET_ERRNO)},
		{"seccompRetAllow", seccompRetAllow, uint32(unix.SECCOMP_RET_ALLOW)},
		{"seccompRetDataMask", seccompRetDataMask, uint32(unix.SECCOMP_RET_DATA)},
		{"errnoEPERM", errnoEPERM, uint32(syscall.EPERM)},
	}
	for _, c := range consts {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x (x/sys/unix 为权威定义)", c.name, c.got, c.want)
		}
	}

	bpfConsts := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"bpfLD", bpfLD, unix.BPF_LD},
		{"bpfW", bpfW, unix.BPF_W},
		{"bpfABS", bpfABS, unix.BPF_ABS},
		{"bpfJMP", bpfJMP, unix.BPF_JMP},
		{"bpfJEQ", bpfJEQ, unix.BPF_JEQ},
		{"bpfJGE", bpfJGE, unix.BPF_JGE},
		{"bpfK", bpfK, unix.BPF_K},
		{"bpfRET", bpfRET, unix.BPF_RET},
	}
	for _, c := range bpfConsts {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestSeccompDataLayout_MatchesKernel 断言 seccomp_data 的字段偏移未写错。
//
// 偏移错了的后果极其隐蔽：过滤器会去比较 ip 或 args 而毫无报错，
// 名单全部静默失效。因此用与内核同构的结构体反推偏移。
func TestSeccompDataLayout_MatchesKernel(t *testing.T) {
	// 与内核 include/uapi/linux/seccomp.h 的 struct seccomp_data 同构。
	type seccompDataLayout struct {
		nr   int32
		arch uint32
		ip   uint64
		args [6]uint64
	}
	if got := unsafe.Offsetof(seccompDataLayout{}.nr); got != seccompDataNrOffset {
		t.Errorf("nr offset = %d, want %d", got, seccompDataNrOffset)
	}
	if got := unsafe.Offsetof(seccompDataLayout{}.arch); got != seccompDataArchOffset {
		t.Errorf("arch offset = %d, want %d", got, seccompDataArchOffset)
	}
	if got := unsafe.Sizeof(seccompDataLayout{}); got != 64 {
		t.Errorf("sizeof(seccomp_data) = %d, want 64", got)
	}
}

// TestToSockFilters_FieldMapping 指令搬运不得错位。
func TestToSockFilters_FieldMapping(t *testing.T) {
	insns := []seccompInsn{
		{Code: 0x20, Jt: 1, Jf: 2, K: 4},
		{Code: 0x15, Jt: 3, Jf: 4, K: 101},
		{Code: 0x06, Jt: 0, Jf: 0, K: seccompRetAllow},
	}
	got := toSockFilters(insns)
	if len(got) != len(insns) {
		t.Fatalf("len(SockFilter) = %d, want %d", len(got), len(insns))
	}
	for i := range insns {
		want := unix.SockFilter{Code: insns[i].Code, Jt: insns[i].Jt, Jf: insns[i].Jf, K: insns[i].K}
		if got[i] != want {
			t.Errorf("filters[%d] = %+v, want %+v", i, got[i], want)
		}
	}
	// 编译期兜底：字段宽度必须一致（宽度不同会静默截断）。
	if unsafe.Sizeof(unix.SockFilter{}) != unsafe.Sizeof(seccompInsn{}) {
		t.Errorf("sizeof(unix.SockFilter)=%d != sizeof(seccompInsn)=%d",
			unsafe.Sizeof(unix.SockFilter{}), unsafe.Sizeof(seccompInsn{}))
	}
}

// TestSeccompDenyList_CoversEscapePrimitives 生产名单必须覆盖全部逃逸原语。
//
// 精确集合断言（而非"至少包含"）：名单是安全边界，任何增删都应当是一次
// 有意识的改动 —— 若确需调整，请同步复核 os_sandbox_linux_seccomp.go 的
// 收录判据与本断言。
func TestSeccompDenyList_CoversEscapePrimitives(t *testing.T) {
	want := []uint32{
		unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_IO_URING_SETUP,
		unix.SYS_IO_URING_ENTER,
		unix.SYS_IO_URING_REGISTER,
	}
	got := seccompDeniedSyscalls()
	if len(got) != len(want) {
		t.Fatalf("denylist 长度 = %d, want %d；got=%v\n"+
			"若为有意增删，请同步更新本断言与收录判据注释", len(got), len(want), got)
	}
	set := make(map[uint32]bool, len(got))
	for _, nr := range got {
		set[nr] = true
	}
	for _, nr := range want {
		if !set[nr] {
			t.Errorf("denylist 缺少 syscall %d（逃逸原语）", nr)
		}
	}
}

// TestBuildSeccompPlan_ProductionListIsEnforced 用**生产名单**跑真实程序。
//
// 这是本文件的核心判据：不是测试自己编一份名单，而是拿
// buildSeccompPlan()（生产路径调用的同一个函数）构造程序，
// 再用解释器执行，断言语义。
func TestBuildSeccompPlan_ProductionListIsEnforced(t *testing.T) {
	plan, err := buildSeccompPlan()
	if err != nil {
		t.Skipf("buildSeccompPlan unsupported on this build: %v", err)
	}
	prog, err := buildSeccompProgram(plan)
	if err != nil {
		t.Fatalf("buildSeccompProgram: %v", err)
	}

	// 1) 名单里每个 syscall 都必须被拒。
	for _, nr := range seccompDeniedSyscalls() {
		got, err := runSeccompProgram(prog, seccompData{nr: nr, arch: plan.Arch})
		if err != nil {
			t.Fatalf("interpret nr=%d: %v", nr, err)
		}
		if got.Action != seccompRetErrno || got.Data != errnoEPERM {
			t.Errorf("nr=%d verdict = action %#x data %#x, want ERRNO(%d)",
				nr, got.Action, got.Data, errnoEPERM)
		}
	}

	// 2) 普通 syscall 必须放行（denylist 形态：不得误伤工具链）。
	for _, nr := range []uint32{uint32(unix.SYS_GETPID), uint32(unix.SYS_OPENAT), uint32(unix.SYS_CLONE)} {
		got, err := runSeccompProgram(prog, seccompData{nr: nr, arch: plan.Arch})
		if err != nil {
			t.Fatalf("interpret nr=%d: %v", nr, err)
		}
		if got.Action != seccompRetAllow {
			t.Errorf("普通 syscall nr=%d 被拒（action %#x），denylist 误伤工具链", nr, got.Action)
		}
	}

	// 3) ABI 错位必须杀死进程（换 ABI 绕过名单的唯一堵法）。
	got, err := runSeccompProgram(prog, seccompData{nr: uint32(unix.SYS_PTRACE), arch: plan.Arch ^ 1})
	if err != nil {
		t.Fatalf("interpret arch-mismatch: %v", err)
	}
	if got.Action != seccompRetKillProcess {
		t.Errorf("ABI 错位 verdict action = %#x, want KILL_PROCESS", got.Action)
	}
}

// TestSeccompSupported_Sanity 探测函数本身要能给出结论（不是恒 false）。
func TestSeccompSupported_Sanity(t *testing.T) {
	arch, x32, ok := seccompTargetArch()
	if !ok {
		// arch 是 uint32（seccomp AUDIT_ARCH_* 常量），%q 用错动词 ⇒ Go vet 的 printf 检查会在
		// linux 上把 CI 判红（macOS 本地 vet 不编译本文件，看不到）。
		t.Skipf("unsupported architecture %d", arch)
	}
	if arch == 0 {
		t.Error("seccompTargetArch 报告 ok 却返回 arch=0（会导致构造被拒）")
	}
	switch x32 {
	case 0:
		// arm64：不应有 x32 守卫
	case 0x40000000:
		// amd64：必须有
	default:
		t.Errorf("unexpected x32 bit %#x", x32)
	}
	if !seccompSupported() {
		t.Log("seccompSupported()=false：本内核/架构不支持 cBPF 过滤器，工厂会记降级（可观测）")
	}
}

// newSeccompTestManager 构造启用 namespaces + seccomp 的沙箱上下文。
func newSeccompTestManager(t *testing.T, workDir string, useLandlock bool) Manager {
	t.Helper()
	if !seccompSupported() {
		t.Skip("kernel does not support seccomp cBPF (need >= 3.17)")
	}
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	cfg := &SandboxConfig{
		WorkDir: workDir,
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces:    true,
				Namespaces:       []string{"pid", "net", "uts", "mount"},
				UseUserNamespace: true,
				UseLandlock:      useLandlock,
				UseSeccomp:       true,
			},
		},
	}
	return NewManager(cfg, SandboxLevelIsolated)
}

// TestRestrict_SeccompOnlyStillSummonsHelper 结构修复判别器（本次改动的核心）。
//
// 场景：内核不支持 Landlock（或配置只开 seccomp）⇒ UseLandlock=false，
// UseSeccomp=true。此时 pre-exec helper **必须照旧被召唤**，否则 seccomp
// 根本无处安装（helper 是唯一载体），而 banner 却会宣称 seccomp 生效。
//
// 改动前：Restrict 只在 l.cfg.UseLandlock 时包装 ⇒ cmd.Path 保持 "sh"
// ⇒ 本用例红。因此它是可判别的（不是恒真断言）。
func TestRestrict_SeccompOnlyStillSummonsHelper(t *testing.T) {
	workDir := t.TempDir()
	mgr := newSeccompTestManager(t, workDir, false /* useLandlock */)

	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelIsolated, workDir)
	if err != nil {
		t.Fatalf("Manager.Prepare: %v", err)
	}
	defer cleanup()

	osSB := sbCtx.OSSandbox()
	if osSB == nil || osSB.Platform() != "linux-namespaces" {
		t.Fatalf("OSSandbox 降级（platform=%v），无法验证 helper 召唤", osSB)
	}

	cmd := exec.Command("sh", "-c", "echo hi")
	cmd.Dir = workDir
	if len(sbCtx.Env) > 0 {
		cmd.Env = sbCtx.Env
	}
	if err := osSB.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}

	exePath, exeErr := os.Executable()
	if exeErr != nil || exePath == "" {
		t.Skipf("os.Executable unavailable: %v", exeErr)
	}
	if cmd.Path != exePath {
		t.Fatalf("cmd.Path = %q, want %q：只开 seccomp 时 helper 未被召唤，"+
			"seccomp 将无处安装（内核 <5.13 上会静默缺席）", cmd.Path, exePath)
	}
	if len(cmd.Args) < 2 || cmd.Args[1] != SandboxExecHelperArg {
		t.Fatalf("cmd.Args = %v, want argv[1]=%q", cmd.Args, SandboxExecHelperArg)
	}

	spec := mustReadSpecFromEnv(t, cmd.Env)
	if !spec.Seccomp {
		t.Error("信封未要求 seccomp（cfg.UseSeccomp=true 却漏装）")
	}
	if spec.Landlock != nil {
		t.Error("信封携带了 Landlock 规则（cfg.UseLandlock=false 时不应构造）")
	}
}

// TestRestrict_NoMechanismNoWrap 两个机制都关时不包装（不加无谓的进程层级）。
func TestRestrict_NoMechanismNoWrap(t *testing.T) {
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

	cmd := exec.Command("sh", "-c", "echo hi")
	cmd.Dir = workDir
	// exec.Command 在构造时即把 "sh" 经 LookPath 解析为绝对路径（任何 Linux 上
	// 都不是字面 "sh"）。「未插 helper」的可判别面 = Path/Args 在 Restrict 前后不被改写，
	// 而非「保持 sh 原样」（后者在物理上不可能成立）。
	wantPath := cmd.Path
	wantArgs := append([]string{}, cmd.Args...)
	if osSB := sbCtx.OSSandbox(); osSB != nil {
		if err := osSB.Restrict(cmd); err != nil {
			t.Fatalf("Restrict: %v", err)
		}
	}
	if cmd.Path != wantPath {
		t.Errorf("cmd.Path = %q, want 不被 Restrict 改写 %q（两机制都关时不应插入 helper）", cmd.Path, wantPath)
	}
	if len(cmd.Args) != len(wantArgs) {
		t.Errorf("cmd.Args = %v, want 不被 Restrict 改写 %v", cmd.Args, wantArgs)
	} else {
		for i := range wantArgs {
			if cmd.Args[i] != wantArgs[i] {
				t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], wantArgs[i])
			}
		}
	}
	if strings.Contains(strings.Join(cmd.Env, " "), preExecSpecEnv) {
		t.Error("信封 env 被写入，但没有任何机制被启用")
	}
}

// TestDescribeIsolationFeatures_SeccompVisibility 门 2：banner 特性必须与实际一致。
func TestDescribeIsolationFeatures_SeccompVisibility(t *testing.T) {
	base := OSLevelConfig{Linux: LinuxSandboxConfig{Namespaces: []string{"pid"}}}

	off := describeIsolationFeatures(base)
	for _, f := range off {
		if f == "seccomp" {
			t.Error("未启用 seccomp 却在 banner 宣称生效")
		}
	}

	on := base
	on.Linux.UseSeccomp = true
	found := false
	for _, f := range describeIsolationFeatures(on) {
		if f == "seccomp" {
			found = true
		}
	}
	if found != seccompSupported() {
		t.Errorf("banner 宣称 seccomp = %v，但 seccompSupported() = %v（口径必须一致：只列实际生效项）",
			found, seccompSupported())
	}
}
