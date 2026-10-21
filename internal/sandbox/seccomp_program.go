// Package sandbox — seccomp BPF 程序构造（平台无关，任意宿主可直接单测）。
//
// # 为什么把「构造程序」与「装进内核」拆成两个文件
//
// 构造 BPF 程序是纯数据变换（指令序列 + 跳转偏移回填），是整条链路里最容易
// 出错、也最值得有判据的一环；把它与 Linux 解耦后，开发机（darwin）就能用
// 一个内置解释器直接执行程序、断言语义，不必先等 VM。
// 真正需要内核的一步（SYS_SECCOMP 安装）留在 os_sandbox_linux_seccomp.go。
//
// # 为什么本文件就地声明 BPF / seccomp 常量而不复用 x/sys/unix
//
// 两条硬约束：
//  1. golang.org/x/sys/unix 在 windows 上**不可编译**，而本包必须为 windows
//     出构建（os_sandbox_windows.go）⇒ 任何无平台标签的文件都不能 import 它。
//  2. unix.SockFilter / unix.SockFprog 只存在于 ztypes_linux.go，darwin 上
//     没有这两个类型 ⇒ 复用会导致本文件无法在 darwin 编译。
//
// 就地声明 UAPI 常量是 seccomp 库的通行做法（libseccomp、Rust seccompiler
// 各自声明一份），这些值属于稳定 ABI。与 x/sys/unix 的一致性由 Linux 侧
// 等值断言钉住（os_sandbox_linux_seccomp_test.go），不靠人记。
//
// 官方源：
//   - man seccomp(2)（SECCOMP_RET_* 动作、seccomp_data 布局、架构不匹配必须
//     杀死、__X32_SYSCALL_BIT 绕过）:
//     https://man7.org/linux/man-pages/man2/seccomp.2.html @2026-09-19
//   - Linux 内核 BPF 指令编码（linux/bpf_common.h）:
//     https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/include/uapi/linux/bpf_common.h @2026-09-19
package sandbox

import (
	"errors"
	"fmt"
)

// ===== BPF 指令编码（linux/bpf_common.h）=====
//
// 一条指令的 code 由「类别 | 位宽/操作 | 寻址模式/源」三段按位或而成。
const (
	bpfLD  = 0x00 // 类别：载入
	bpfW   = 0x00 // 位宽：32 位
	bpfABS = 0x20 // 模式：从绝对偏移取

	bpfJMP = 0x05 // 类别：跳转
	bpfJEQ = 0x10 // 操作：相等
	bpfJGE = 0x30 // 操作：无符号大于等于

	bpfK   = 0x00 // 源：立即数
	bpfRET = 0x06 // 类别：返回
)

// struct seccomp_data 的字段偏移（man seccomp(2)）：
//
//	int nr; __u32 arch; __u64 instruction_pointer; __u64 args[6];
const (
	seccompDataNrOffset   = 0
	seccompDataArchOffset = 4
)

// SECCOMP_RET_* 动作值。
const (
	seccompRetKillProcess = 0x80000000
	seccompRetErrno       = 0x00050000
	seccompRetAllow       = 0x7fff0000
	seccompRetDataMask    = 0x0000ffff
)

// errnoEPERM 是 Linux EPERM（UAPI 值 1，全架构一致）。
//
// 为什么自身返回 EPERM 而不是 KILL：本沙箱要跑任意用户工具链，KILL 会把
// 「某个工具顺带碰了一下被禁 syscall」升级成「整条命令被杀、且看不出原因」。
// 返回 EPERM 让调用方看到明确错误、沙箱继续可用（与业内通行做法一致）。
const errnoEPERM = 1

// 两个汇合点标签：拒绝块与杀死块都落在程序尾部，由跳转偏移回填指向。
const (
	labelSeccompDeny = "deny"
	labelSeccompKill = "kill"
)

// seccompInsn 是一条 BPF 指令。
//
// 字段与序与内核 struct sock_filter { __u16 code; __u8 jt; __u8 jf; __u32 k; }
// 一一对应；Linux 侧会逐字段搬运进 unix.SockFilter（等值由 Linux 测试断言）。
type seccompInsn struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// seccompPlan 描述要构造的过滤器策略（与平台无关的纯输入）。
type seccompPlan struct {
	// Arch 期望的 AUDIT_ARCH_* 值。不匹配 ⇒ 杀死进程。
	Arch uint32
	// X32SyscallBit 非 0 时插入 x32 守卫（amd64 = 0x40000000；arm64 无 x32 ABI ⇒ 0）。
	X32SyscallBit uint32
	// Denied 要拒绝的 syscall 号（顺序无关，构造时去重）。
	Denied []uint32
}

// bpfReloc 是一条待回填的跳转：把 insns[idx] 的 Jt 或 Jf 指向 label。
type bpfReloc struct {
	idx   int
	label string
	useJt bool
}

// bpfMiniAsm 是够用的迷你汇编器：只做「发射指令 + 命名标签 + 回填跳转」。
//
// 为什么需要它：BPF 跳转偏移是相对**下一条指令**的 8 位无符号值，
// 手写常量极易算错，且超过 255 会静默回绕（黑名单一长就出洞）。
// 标签化 + 回填 + 越界报错把这两个坑一次性消掉。
type bpfMiniAsm struct {
	insns  []seccompInsn
	labels map[string]int
	relocs []bpfReloc
}

func newBpfMiniAsm() *bpfMiniAsm {
	return &bpfMiniAsm{labels: make(map[string]int, 2)}
}

// emit 追加一条无跳转指令。
func (a *bpfMiniAsm) emit(code uint16, k uint32) {
	a.insns = append(a.insns, seccompInsn{Code: code, K: k})
}

// jumpIf 追加一条条件跳转指令，命中（jt=true 用 Jt，否则用 Jf）时跳到 label。
func (a *bpfMiniAsm) jumpIf(label string, useJt bool, code uint16, k uint32) {
	a.insns = append(a.insns, seccompInsn{Code: code, K: k})
	a.relocs = append(a.relocs, bpfReloc{idx: len(a.insns) - 1, label: label, useJt: useJt})
}

// mark 在当前末尾打标签。
func (a *bpfMiniAsm) mark(name string) {
	a.labels[name] = len(a.insns)
}

// resolve 回填全部跳转；标签未定义或偏移越界都是硬错误（不给静默错答）。
func (a *bpfMiniAsm) resolve() error {
	for _, r := range a.relocs {
		target, ok := a.labels[r.label]
		if !ok {
			return fmt.Errorf("seccomp bpf: unresolved label %q", r.label)
		}
		off := target - (r.idx + 1)
		if off < 0 || off > 255 {
			return fmt.Errorf("seccomp bpf: jump offset %d out of uint8 range for label %q (denylist too long?)", off, r.label)
		}
		if r.useJt {
			a.insns[r.idx].Jt = uint8(off)
		} else {
			a.insns[r.idx].Jf = uint8(off)
		}
	}
	return nil
}

// buildSeccompProgram 构造 denylist 形态的 seccomp 过滤器。
//
// 程序形态（自上而下）：
//
//  0. 载入 seccomp_data.arch
//  1. arch != 期望值 ⇒ 跳到 kill 块
//  2. 载入 seccomp_data.nr
//  3. nr >= X32SyscallBit ⇒ 跳到 deny 块（仅 amd64）
//  4. 对每个被禁 syscall：nr == N ⇒ 跳到 deny 块
//  5. 兜底 RET ALLOW
//  6. deny 块：RET ERRNO(EPERM)
//  7. kill 块：RET KILL_PROCESS
//
// 为什么必须是 denylist（默认放行）而不是 allowlist：allowlist 面向单一用途
// 二进制；本沙箱要跑任意用户工具链（go/npm/cargo/make/…），内核新增 syscall
// 在 allowlist 下会被静默放行，工具链也会大面积假失败。先例：Docker 默认
// seccomp profile、同类 CLI 的 Linux 沙箱都是 denylist。
//
// 为什么 arch 检查是「不匹配即杀」而不只是「不匹配即拒」：nr 的语义按 ABI
// 不同，若不自证 ABI，攻击者可换用 i386 等 ABI 的同号 syscall 绕过名单
// （man seccomp(2) 明确要求此种情形 KILL）。x32 位变体（0x40000000）语义与
// 本 ABI 相同、只是匹配会错位，因此只拒绝、不杀死。
func buildSeccompProgram(plan seccompPlan) ([]seccompInsn, error) {
	if plan.Arch == 0 {
		return nil, errors.New("seccomp bpf: plan.Arch is unset")
	}
	denied := dedupeSyscallNrs(plan.Denied)

	asm := newBpfMiniAsm()

	// 1) 架构守卫。
	asm.emit(bpfLD|bpfW|bpfABS, seccompDataArchOffset)
	asm.jumpIf(labelSeccompKill, false, bpfJMP|bpfJEQ|bpfK, plan.Arch)

	// 2) syscall 号。
	asm.emit(bpfLD|bpfW|bpfABS, seccompDataNrOffset)

	// 3) x32 守卫。
	if plan.X32SyscallBit != 0 {
		asm.jumpIf(labelSeccompDeny, true, bpfJMP|bpfJGE|bpfK, plan.X32SyscallBit)
	}

	// 4) denylist。命中的一律跳 deny。
	for _, nr := range denied {
		asm.jumpIf(labelSeccompDeny, true, bpfJMP|bpfJEQ|bpfK, nr)
	}

	// 5) 默认放行。
	asm.emit(bpfRET|bpfK, seccompRetAllow)

	// 6) 拒绝块。
	asm.mark(labelSeccompDeny)
	asm.emit(bpfRET|bpfK, seccompRetErrno|(errnoEPERM&seccompRetDataMask))

	// 7) 杀死块。
	asm.mark(labelSeccompKill)
	asm.emit(bpfRET|bpfK, seccompRetKillProcess)

	if err := asm.resolve(); err != nil {
		return nil, err
	}
	return asm.insns, nil
}

// dedupeSyscallNrs 去重并保持升序（同号重复只会浪费指令与偏移预算）。
func dedupeSyscallNrs(in []uint32) []uint32 {
	seen := make(map[uint32]struct{}, len(in))
	out := make([]uint32, 0, len(in))
	for _, nr := range in {
		if _, ok := seen[nr]; ok {
			continue
		}
		seen[nr] = struct{}{}
		out = append(out, nr)
	}
	return out
}
