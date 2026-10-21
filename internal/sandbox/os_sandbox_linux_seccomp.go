//go:build linux

// Package sandbox — Linux seccomp-bpf 系统调用过滤（P2-3）。
//
// # 它补的是前两层物理上覆盖不到的那一层
//
// 三层隔离不是「叠加更严」，而是三个正交的判定面：
//
//	namespaces  决定进程「看得见」什么（PID/NET/UTS/MNT 视图）—— 管不到 syscall
//	Landlock    决定「路径」能否读写/执行                        —— 管不到 syscall
//	seccomp     决定「能否发起某类内核操作」                     —— 不认路径
//
// 因此缺 seccomp 时存在前两层都拦不住的真实逃逸面，按危害排序：
//
//  1. ptrace / process_vm_readv / process_vm_writev
//     沙箱进程与宿主**同 uid** ⇒ 可读写宿主任意同 uid 进程的内存，包括
//     whale-code 本体与 ssh-agent ⇒ 完整逃逸（改掉 agent 的内存即改掉决策）。
//     Landlock 只看路径、netns 只看 IP，两层对此完全无感。
//  2. io_uring_setup / enter / register
//     io_uring 请求由内核 worker 线程代为执行，seccomp 过滤器只在**发起**
//     syscall 时求值 ⇒ 异步执行路径上进程级限制（及部分 LSM 检查）不生效。
//     内核为此另设全局开关并补齐 LSM 钩子，可见其性质特殊。
//
// # 动作与形态的两个决定（都不是随手选的）
//
//   - **denylist（默认放行）而非 allowlist**：本沙箱要跑任意用户工具链，
//     allowlist 会把内核新增 syscall 静默拒掉、工具链大面积假失败。
//     allowlist 适用于单一用途二进制（man seccomp(2) 的语境）。
//     先例：同类 CLI 的 Linux 沙箱（对标证据件，
//     本地源码实读）与 Docker 默认 profile 均为 denylist。
//   - **命中返回 EPERM 而非 KILL**：KILL 会把「某工具顺带碰了一下被禁 syscall」
//     变成「整条命令被杀且看不出原因」。EPERM 可诊断、可修复。
//     同类 CLI 同样选 Errno(EPERM)（landlock.rs:252-253）。唯一用 KILL 的是
//     ABI 错位（见 seccomp_program.go 的 arch 守卫）——那种情形 nr 语义整体
//     不可信，必须终止。
//
// # 官方源
//
//   - man seccomp(2): https://man7.org/linux/man-pages/man2/seccomp.2.html @2026-09-19
//   - 同类实现（本地源码实读）:
//     对标证据件
package sandbox

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// x32SyscallBit 是 amd64 的 __X32_SYSCALL_BIT。
//
// x32（ILP32 ABI）的 syscall 号 = 本 ABI 号 | 0x40000000；若只按裸 nr 比对，
// 攻击者用 0x40000065 就能绕过对 101(ptrace) 的拒绝（man seccomp(2) 明确
// 提示该绕过）。seccomp_program.go 用一条 JGE 守卫整体拒绝该号段。
const x32SyscallBit = 0x40000000

// SECCOMP_SET_MODE_FILTER（cBPF 过滤器）需内核 ≥ 3.17。
const (
	seccompMinKernelMajor = 3
	seccompMinKernelMinor = 17
)

// seccompTargetArch 返回当前构建目标的 seccomp 架构标识与 x32 位。
//
// ok=false 表示该架构没有可信的 seccomp 过滤器形态（不支持的架构不得
// 「凑合装一个」——装错了反而给出虚假的保护感）。
func seccompTargetArch() (arch uint32, x32Bit uint32, ok bool) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, x32SyscallBit, true
	case "arm64":
		// arm64 无 x32 ABI ⇒ 不插该守卫。
		return unix.AUDIT_ARCH_AARCH64, 0, true
	default:
		return 0, 0, false
	}
}

// seccompDeniedSyscalls 返回本沙箱拒绝的 syscall 号（denylist 单一事实源）。
//
// 收录判据（三条全满足才收录）：
//  1. 沙箱进程能主动发起；
//  2. 与宿主同 uid ⇒ 能借此跨出隔离边界（读写宿主进程 / 让内核代执行）；
//  3. 前两层（namespaces、Landlock）结构性拦不住。
//
// 逐条理由与先例：
//
//	ptrace              同 uid 进程注入与内存读写；同类 CLI landlock.rs:179 无条件禁
//	process_vm_readv    同上，且无需 attach 握手；同类 CLI landlock.rs:180
//	process_vm_writev   同上，写入方向危害更大；同类 CLI landlock.rs:181
//	io_uring_setup      内核 worker 代执行的 I/O 通道，绕过进程级过滤；
//	                    同类 CLI landlock.rs:182-184 无条件禁
//	io_uring_enter
//	io_uring_register
//
// ★ 刻意**不**收录的三类（写清楚是为了防止后人「顺手补上」而制造重复判定）：
//
//   - 网络类（connect/bind/listen/accept/sendto/shutdown/getsock*）：
//     本仓断网由 CLONE_NEWNET 完成，比逐 syscall 更彻底（netns 里根本没有
//     出口）。且 WHALE_SANDBOX_ALLOW_NETWORK=1 时必须放行 ⇒ 一旦写进
//     seccomp 就出现「第二个网络开关」，违反本仓单一事实源纪律。
//     同类 CLI 需要它，是因为它用 ProxyRouted 模式而非 netns 断网。
//   - socket/socketpair 的地址族过滤（只放行 AF_UNIX）：
//     同上，IP 已由 netns 覆盖；而「AF_UNIX 仍能连到宿主 socket」
//     （如 /var/run/docker.sock）本质是**挂载视图**问题——本仓 mount
//     namespace 不做 pivot_root / bind-mount，尚能看到宿主路径。
//     它的正解在挂载层，硬塞进 seccomp 只会把 dbus/nss 一起打掉，
//     属另一层的修法，不在本次范围内混入。
//   - CAP 门控的全局 syscall（mount/reboot/init_module/kexec_load/…）：
//     本沙箱运行在自建 user namespace 内，`capable(CAP_SYS_*)` 面向
//     init_user_ns 校验 ⇒ 内核已结构性拒绝；且挂载动作被限制在本进程私有
//     mount namespace 内，对宿主无副作用。再列一遍只是重复内核既有判定。
//
// 另有四类**考虑过、本次不加**（属纵深防御/利用辅助而非边界绕过，且与
// 工具链存在真实冲突面，需参数级匹配，应作独立一次改动单独评估）：
// userfaultfd（qemu 迁移会用）、perf_event_open（性能剖析）、kcmp（信息oracle）、
// 内核 keyring（add_key/keyctl/request_key）。
func seccompDeniedSyscalls() []uint32 {
	return []uint32{
		unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_IO_URING_SETUP,
		unix.SYS_IO_URING_ENTER,
		unix.SYS_IO_URING_REGISTER,
	}
}

// seccompSupported 报告当前内核 + 构建目标是否具备安装 cBPF 过滤器的条件。
//
// 探测口径（放在工厂期，用户可见）：
//   - 构建目标有可信架构形态（seccompTargetArch）
//   - 内核 ≥ 3.17（SECCOMP_SET_MODE_FILTER / cBPF）
//
// 为什么不在工厂期「真装一个过滤器试一下」：安装动作不可逆，且会污染当前
// 进程（PR_SET_NO_NEW_PRIVS 一经设置即无法撤回，会永久影响 CLI 本体的
// setuid 语义）。因此工厂期只做无副作用的能力判断；真安装时的失败由 helper
// 以 fail-closed 方式暴露（见 applySeccompFilter 与 RunPreExecHelper）。
func seccompSupported() bool {
	if _, _, ok := seccompTargetArch(); !ok {
		return false
	}
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return false // 探测失败，按不支持处理（降级可观测，不做无根据的宣称）
	}
	major, minor, ok := parseKernelVersion(utsnameToString(uname.Release[:]))
	if !ok {
		return false
	}
	return major > seccompMinKernelMajor ||
		(major == seccompMinKernelMajor && minor >= seccompMinKernelMinor)
}

// buildSeccompPlan 组装当前平台的过滤器策略。
func buildSeccompPlan() (seccompPlan, error) {
	arch, x32Bit, ok := seccompTargetArch()
	if !ok {
		return seccompPlan{}, fmt.Errorf("seccomp: unsupported architecture %q", runtime.GOARCH)
	}
	return seccompPlan{
		Arch:          arch,
		X32SyscallBit: x32Bit,
		Denied:        seccompDeniedSyscalls(),
	}, nil
}

// toSockFilters 把平台无关的指令序列搬进 unix.SockFilter。
//
// 字段一一对应（内核 struct sock_filter 与 unix.SockFilter 同序同宽），
// 搬运正确性由 os_sandbox_linux_seccomp_test.go 的逐字段断言钉住。
func toSockFilters(insns []seccompInsn) []unix.SockFilter {
	out := make([]unix.SockFilter, len(insns))
	for i, in := range insns {
		out[i] = unix.SockFilter{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	return out
}

// applySeccompFilter 在当前线程安装 seccomp 过滤器（restrict self）。
//
// 前置：调用者是即将 exec 目标命令的 helper 进程，且已准备好 no_new_privs。
// 内核要求：安装 cBPF 过滤器前必须 PR_SET_NO_NEW_PRIVS（否则 EACCES）——
// Landlock 路径已设过一次；此处再设一次是幂等的，且保证「只启用 seccomp、
// 不启用 Landlock」的内核（< 5.13）也会被正确设置。
//
// 后置：过滤器对当前线程生效，且**跨 execve 保留**、由子进程继承
// （man seccomp(2)）。因此 exec 出去的真实命令及其全部后代都在名单约束下。
//
// 为什么必须在 execve 之前安装：过滤器一经安装不能移除，若在目标命令启动后
// 安装，则「安装前的那段窗口」不受约束；helper 形态天然把窗口压到零。
//
// 失败即返回错误：调用方（RunPreExecHelper）会打印原因并以 126 退出。
// fail-closed 的理由与 Landlock 一致——过滤器没装上却继续执行，等于让用户
// 以为sandbox 生效了（虚假保护比明确失败更危险）。
func applySeccompFilter() error {
	plan, err := buildSeccompPlan()
	if err != nil {
		return err
	}
	insns, err := buildSeccompProgram(plan)
	if err != nil {
		return fmt.Errorf("seccomp: build program: %w", err)
	}
	filters := toSockFilters(insns)

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("seccomp: PR_SET_NO_NEW_PRIVS: %w", err)
	}

	fprog := unix.SockFprog{
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER), 0,
		uintptr(unsafe.Pointer(&fprog))); errno != 0 {
		return fmt.Errorf("seccomp: SECCOMP_SET_MODE_FILTER: %w", errno)
	}
	// 过滤器指向 filters 底层数组，内核在 syscall 内已复制；
	// 显式 KeepAlive 防止编译器判定 filters 已死而回收其内存。
	runtime.KeepAlive(filters)
	return nil
}
