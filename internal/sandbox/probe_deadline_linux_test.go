//go:build linux

// Package sandbox — 判别器：seccomp 探针等待必须有 per-call deadline。
//
// 形态背景（定谳，2026-10-12 Linux VM 真机）：旧探针载荷在对照组用
// PTRACE_TRACEME 自我 trace ⇒ tracer=测试主进程 ⇒ Go runtime 的 SIGURG
// 抢占信号把载荷子进程打进 ptrace-stop（轻症=无输出 fail-loud；重症=管道
// 不关、cmd.Wait 永挂；20 连跑实测 4 挂死 + 10 慢轮）。2026-10-12 根治 =
// 载荷改 PTRACE_ATTACH 自己的哑孙进程（tracer=短命载荷子进程，测试主进程
// 永不承担 trace 角色，毒根拔除）；本文件的 deadline watchdog 保留作纵深防御。
//
// 判别性自检记录（R4-7/R11）：mutant（WithTimeout→WithCancel，deadline 失效）
// 下本文件 TestW080_RunSeccompProbe_BoundedByDeadline_NeverHangs 永挂，
// 以 `go test -timeout 15s` 收 panic 红 —— 红 = 挂死，与 原始现象同形。
package sandbox

import (
	"os"
	"testing"
	"time"
)

// TestW080_RunSeccompProbe_BoundedByDeadline_NeverHangs 钉住：
// 子进程在窗口内不退出、管道不关时（WHALE_GUARD_PROBE_STALL_SEC=60 模拟，
// 对父进程而言与 ptrace-stop 挂死同形），helper 必须在 deadline 内返回
// timedOut=true，而不是永挂拖死整个套件。
func TestW080_RunSeccompProbe_BoundedByDeadline_NeverHangs(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil || selfExe == "" {
		t.Skipf("os.Executable unavailable: %v", err)
	}

	const deadline = 2 * time.Second
	start := time.Now()
	res := runSeccompProbeTimeout(t, selfExe, t.TempDir(), nil,
		[]string{"WHALE_GUARD_PROBE_STALL_SEC=60"}, deadline)
	elapsed := time.Since(start)

	if !res.timedOut {
		t.Fatalf("子进程被钉住 60s 而 helper 在 %v 内返回 timedOut=false —— deadline 机制未生效，判据不可信（raw=%q）",
			elapsed, res.raw)
	}
	if elapsed > deadline+5*time.Second {
		t.Fatalf("helper 耗时 %v（deadline=%v + 5s 容差）——看门狗触发过晚", elapsed, deadline)
	}
	t.Logf("看门狗在 %v 触发（deadline=%v），挂死形态被兜住", elapsed, deadline)
}

// TestW080_HealthyProbe_NotTrippedByWatchdog 是判别器的反例守卫（R9 配对）：
// 健康探针（对照组、无 stall）必须照常产出 errno 且 timedOut=false ——
// 证明 deadline 机制没有误伤正常路径（否则上面那条只是"一律报超时"的假修复）。
func TestW080_HealthyProbe_NotTrippedByWatchdog(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil || selfExe == "" {
		t.Skipf("os.Executable unavailable: %v", err)
	}

	res := runSeccompProbe(t, selfExe, t.TempDir(), nil, nil)
	if res.timedOut {
		t.Fatalf("健康探针被看门狗误杀（deadline=%v）——对照对照环境过慢或阈值过紧", seccompProbeDefaultDeadline)
	}
	if res.controlErrno != 0 {
		t.Fatalf("对照组 getpid errno=%d（want 0）", res.controlErrno)
	}
	if res.ptraceErrno != 0 {
		t.Skipf("宿主已限制 ptrace（对照组 errno=%d，可能已在某层容器/沙箱内），探针无法判别", res.ptraceErrno)
	}
	t.Logf("健康探针照常：PTRACE_ERRNO=0 CONTROL_ERRNO=0，看门狗未误伤")
}
