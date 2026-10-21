// Package sandbox — seccomp BPF 程序构造的判据（平台无关，可在开发机直接跑）。
//
// 判据形态：内置一个迷你 BPF 解释器，把构造出来的程序**真实执行**一遍，
// 断言「给定 (syscall 号, ABI) ⇒ 返回哪个动作」。这比断言指令形状强得多：
//   - 跳转偏移算错 ⇒ 执行时走错分支，直接打红；
//   - 架构守卫写错 ⇒ i386 ABI 探针不再返回 KILL，直接打红；
//   - 漏加某条 syscall ⇒ 该探针返回 ALLOW，直接打红。
//
// ★ 可判别性自检（TestSeccompProgram_ProbeIsDiscriminating）：把同一条探针
// 跑在「加入该 syscall 的策略」与「不加入该策略」两套程序上，必须得到不同
// 结果——否则说明解释器或断言是恒真的假绿。
package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

// seccompData 是解释器视角的 struct seccomp_data 子集（只用 nr 与 arch）。
type seccompData struct {
	nr   uint32
	arch uint32
}

// seccompVerdict 是解释器对一次调用的判定结果。
type seccompVerdict struct {
	// Action 是 SECCOMP_RET_* 的动作位（已按 SECCOMP_RET_ACTION_FULL 掩码）。
	Action uint32
	// Data 是动作携带的数据（ERRNO 形态下即 errno）。
	Data uint32
}

// runSeccompProgram 是迷你 BPF 解释器：语义严格对齐内核 cBPF 求值规则
// （跳转偏移相对下一条指令；class 由 code 低 3 位决定）。
//
// 只实现本包会发射的三类指令；遇到其它指令返回错误而不是猜测。
func runSeccompProgram(insns []seccompInsn, data seccompData) (seccompVerdict, error) {
	const (
		bpfClassMask         = 0x07
		bpfClassLD           = 0x00
		bpfClassJMP          = 0x05
		bpfClassRET          = 0x06
		seccompRetActionFull = 0xffff0000
	)
	var acc uint32
	for pc := 0; pc < len(insns); {
		ins := insns[pc]
		switch ins.Code & bpfClassMask {
		case bpfClassLD:
			if ins.Code != bpfLD|bpfW|bpfABS {
				return seccompVerdict{}, fmt.Errorf("pc=%d: unsupported load mode %#x", pc, ins.Code)
			}
			switch ins.K {
			case seccompDataNrOffset:
				acc = data.nr
			case seccompDataArchOffset:
				acc = data.arch
			default:
				return seccompVerdict{}, fmt.Errorf("pc=%d: unsupported seccomp_data offset %d", pc, ins.K)
			}
			pc++
		case bpfClassJMP:
			var taken bool
			switch ins.Code {
			case bpfJMP | bpfJEQ | bpfK:
				taken = acc == ins.K
			case bpfJMP | bpfJGE | bpfK:
				taken = acc >= ins.K
			default:
				return seccompVerdict{}, fmt.Errorf("pc=%d: unsupported jump op %#x", pc, ins.Code)
			}
			step := int(ins.Jf)
			if taken {
				step = int(ins.Jt)
			}
			pc += 1 + step
		case bpfClassRET:
			return seccompVerdict{
				Action: ins.K & seccompRetActionFull,
				Data:   ins.K & seccompRetDataMask,
			}, nil
		default:
			return seccompVerdict{}, fmt.Errorf("pc=%d: unsupported class in code %#x", pc, ins.Code)
		}
		if pc < 0 || pc > len(insns) {
			return seccompVerdict{}, fmt.Errorf("jump out of program bounds (pc=%d, len=%d)", pc, len(insns))
		}
	}
	return seccompVerdict{}, fmt.Errorf("program ran off the end without RET (len=%d)", len(insns))
}

// 测试用 ABI 常量（与平台无关的字面值，等价性由 Linux 侧断言另行钉住）。
const (
	testArchX86_64  = 0xc000003e
	testArchAARCH64 = 0xc00000b7
	testArchI386    = 0x40000003
	testX32Bit      = 0x40000000
	testEPERM       = 1
)

// 测试用的少量 syscall 号（amd64 值；语义断言不依赖真实编号，只依赖一致性）。
const (
	testNrPtrace         = 101
	testNrProcessVMReadv = 310
	testNrIoUringSetup   = 425
	testNrGetpid         = 39
)

func mustBuildProgram(t *testing.T, plan seccompPlan) []seccompInsn {
	t.Helper()
	insns, err := buildSeccompProgram(plan)
	if err != nil {
		t.Fatalf("buildSeccompProgram failed: %v", err)
	}
	return insns
}

// TestSeccompProgram_Semantics 执行模型：断言「某次调用会得到什么动作」。
//
// 每条探针都在验证一个独立的安全性质：
//  1. 被禁 syscall（同 ABI）⇒ EPERM（可诊断，不杀进程）
//  2. 未列名 syscall ⇒ 放行（denylist 形态：内核新增 syscall 不会假失败）
//  3. ABI 错位 ⇒ KILL（man seccomp(2)：架构不匹配必须杀死，否则换 ABI 绕过名单）
//  4. x32 位变体 ⇒ 拒绝（否则 0x40000065 这类号会绕过按 nr 的匹配）
func TestSeccompProgram_Semantics(t *testing.T) {
	plan := seccompPlan{
		Arch:          testArchX86_64,
		X32SyscallBit: testX32Bit,
		Denied:        []uint32{testNrPtrace, testNrProcessVMReadv, testNrIoUringSetup},
	}
	prog := mustBuildProgram(t, plan)

	cases := []struct {
		name       string
		data       seccompData
		wantAction uint32
		wantData   uint32
	}{
		{"ptrace 被拒", seccompData{nr: testNrPtrace, arch: testArchX86_64}, seccompRetErrno, testEPERM},
		{"process_vm_readv 被拒", seccompData{nr: testNrProcessVMReadv, arch: testArchX86_64}, seccompRetErrno, testEPERM},
		{"io_uring_setup 被拒", seccompData{nr: testNrIoUringSetup, arch: testArchX86_64}, seccompRetErrno, testEPERM},
		{"未列名 syscall 放行", seccompData{nr: testNrGetpid, arch: testArchX86_64}, seccompRetAllow, 0},
		{"ABI 错位杀死进程", seccompData{nr: testNrPtrace, arch: testArchI386}, seccompRetKillProcess, 0},
		{"x32 位变体被拒", seccompData{nr: testNrPtrace | testX32Bit, arch: testArchX86_64}, seccompRetErrno, testEPERM},
		{"x32 位下的未列名号也被拒", seccompData{nr: testNrGetpid | testX32Bit, arch: testArchX86_64}, seccompRetErrno, testEPERM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runSeccompProgram(prog, tc.data)
			if err != nil {
				t.Fatalf("interpret: %v", err)
			}
			if got.Action != tc.wantAction || got.Data != tc.wantData {
				t.Errorf("verdict = action %#x data %#x, want action %#x data %#x",
					got.Action, got.Data, tc.wantAction, tc.wantData)
			}
		})
	}
}

// TestSeccompProgram_ProbeIsDiscriminating 判别器自检。
//
// 同一批探针在「含该 syscall 的策略」与「不含该 syscall 的策略」上必须给出
// 不同结果。若两边相同，说明解释器/断言恒真，上一条测试是假绿。
func TestSeccompProgram_ProbeIsDiscriminating(t *testing.T) {
	probe := seccompData{nr: testNrPtrace, arch: testArchX86_64}

	withPtrace := mustBuildProgram(t, seccompPlan{Arch: testArchX86_64, Denied: []uint32{testNrPtrace}})
	withoutPtrace := mustBuildProgram(t, seccompPlan{Arch: testArchX86_64, Denied: []uint32{testNrIoUringSetup}})

	gotWith, err := runSeccompProgram(withPtrace, probe)
	if err != nil {
		t.Fatalf("interpret(withPtrace): %v", err)
	}
	gotWithout, err := runSeccompProgram(withoutPtrace, probe)
	if err != nil {
		t.Fatalf("interpret(withoutPtrace): %v", err)
	}
	if gotWith.Action == gotWithout.Action {
		t.Fatalf("probe is not discriminating: both verdicts action=%#x (假绿：断言无法区分策略)", gotWith.Action)
	}
	if gotWith.Action != seccompRetErrno {
		t.Errorf("withPtrace action = %#x, want ERRNO", gotWith.Action)
	}
	if gotWithout.Action != seccompRetAllow {
		t.Errorf("withoutPtrace action = %#x, want ALLOW", gotWithout.Action)
	}
}

// TestSeccompProgram_NoX32GuardOnArm64 arm64 不该带 x32 守卫。
//
// 依据：__X32_SYSCALL_BIT 是 x86-64 专有位（ILP32 ABI），arm64 无该 ABI。
// 断言两件事：程序比 amd64 少一条指令；且高位置号在 arm64 上不被「顺手拒绝」。
func TestSeccompProgram_NoX32GuardOnArm64(t *testing.T) {
	denied := []uint32{testNrPtrace}
	amd64 := mustBuildProgram(t, seccompPlan{Arch: testArchX86_64, X32SyscallBit: testX32Bit, Denied: denied})
	arm64 := mustBuildProgram(t, seccompPlan{Arch: testArchAARCH64, X32SyscallBit: 0, Denied: denied})

	if len(amd64) != len(arm64)+1 {
		t.Errorf("amd64 program length = %d, arm64 = %d; want amd64 one instruction longer (x32 守卫)", len(amd64), len(arm64))
	}

	// aarch64 上被禁 syscall 仍按号拒绝。
	got, err := runSeccompProgram(arm64, seccompData{nr: testNrPtrace, arch: testArchAARCH64})
	if err != nil {
		t.Fatalf("interpret(arm64): %v", err)
	}
	if got.Action != seccompRetErrno {
		t.Errorf("arm64 ptrace action = %#x, want ERRNO", got.Action)
	}
	// 无守卫 ⇒ 高位置号不被拒绝（arm64 上它不是合法 syscall，交给内核 ENOSYS）。
	got, err = runSeccompProgram(arm64, seccompData{nr: testNrPtrace | testX32Bit, arch: testArchAARCH64})
	if err != nil {
		t.Fatalf("interpret(arm64, highbit): %v", err)
	}
	if got.Action != seccompRetAllow {
		t.Errorf("arm64 高位置号 action = %#x, want ALLOW（arm64 无 x32 ABI，不应带该守卫）", got.Action)
	}
}

// TestSeccompProgram_EmptyDenyListAllowsEverything 空名单 ⇒ 只留架构守卫。
func TestSeccompProgram_EmptyDenyListAllowsEverything(t *testing.T) {
	prog := mustBuildProgram(t, seccompPlan{Arch: testArchX86_64})
	got, err := runSeccompProgram(prog, seccompData{nr: testNrPtrace, arch: testArchX86_64})
	if err != nil {
		t.Fatalf("interpret: %v", err)
	}
	if got.Action != seccompRetAllow {
		t.Errorf("empty denylist action = %#x, want ALLOW", got.Action)
	}
	if _, err := buildSeccompProgram(seccompPlan{Arch: 0}); err == nil {
		t.Error("plan without Arch should fail (fail-closed：无架构守卫的程序等于可被换 ABI 绕过)")
	}
}

// TestSeccompProgram_JumpOffsetOverflowIsAnError 偏移越界必须报错而不是回绕。
//
// 背景：BPF 跳转偏移是 8 位，名单长到一定程度就绕回错误位置（静默出洞）。
// 这里构造超长名单，断言构造阶段硬失败——宁可拒绝启动，不要静默放行。
func TestSeccompProgram_JumpOffsetOverflowIsAnError(t *testing.T) {
	long := make([]uint32, 400)
	for i := range long {
		long[i] = uint32(1000 + i)
	}
	_, err := buildSeccompProgram(seccompPlan{Arch: testArchX86_64, Denied: long})
	if err == nil {
		t.Fatal("oversized denylist should fail (jump offset exceeds uint8), got nil error")
	}
	if !strings.Contains(err.Error(), "jump offset") {
		t.Errorf("error should mention jump offset for diagnosability, got: %v", err)
	}
}

// TestSeccompProgram_DenylistDeduped 重复项不该重复发指令。
func TestSeccompProgram_DenylistDeduped(t *testing.T) {
	one := mustBuildProgram(t, seccompPlan{Arch: testArchX86_64, Denied: []uint32{testNrPtrace}})
	dup := mustBuildProgram(t, seccompPlan{Arch: testArchX86_64,
		Denied: []uint32{testNrPtrace, testNrPtrace, testNrPtrace}})
	if len(one) != len(dup) {
		t.Errorf("dedupe failed: len(one)=%d, len(dup)=%d", len(one), len(dup))
	}
}
