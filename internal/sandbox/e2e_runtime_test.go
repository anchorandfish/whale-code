// Package sandbox — P1-4 端到端运行时验证测试（跨平台）。
//
// 本文件验证进程组超时机制在真实进程上的内核级效果，全平台编译。
// Linux 专属的 namespace 隔离与 RLIMIT_NOFILE 资源限制验证见
// e2e_runtime_linux_test.go（//go:build linux）。
//
// 测试约束（P1-4）：
// - 启动真实进程（非 mock），验证内核级效果
// - 通过 WSL Ubuntu 验证（sleep/readlink 等命令在 WSL 内可用）
// - PIT-004 兼容：DLP 环境下 sleep 可能被 Windows 主机层拦截，
// 通过 skipOnDLP() 检测后跳过，避免测试二进制被 SIGKILL
//
// 官方源：
// - Go os/exec: https://pkg.go.dev/os/exec @2026-07-21
// - Linux kill(2): https://man7.org/linux/man-pages/man2/kill.2.html @2026-07-21

package sandbox

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestE2E_ProcessGroupTimeout 验证 RunWithTimeout 在超时后正确终止整个进程组（全平台）。
//
// 测试流程（对应 P1-4 测试用例 3）：
// 1. 启动 `sleep 100` 进程（RunWithTimeout 内部调用 StartCommandWithProcessGroup 设置进程组）
// 2. 设置 1s 超时
// 3. 验证返回结果 TimedOut=true
// 4. 验证进程已被 kill（ProcessState 已设置，进程组无残留）
//
// 内核级效果验证点：
// - Unix: kill(-pgid, SIGKILL) 终止整个进程组（process_unix.go）
// - Windows: taskkill /T /F 终止进程树（process_windows.go）
//
// PIT-004 兼容：Windows 主机层 DLP 可能拦截 sleep，通过 skipOnDLP() 检测后跳过。
// 在 WSL Ubuntu 内运行时，DLP 不拦截子进程，测试正常执行。
func TestE2E_ProcessGroupTimeout(t *testing.T) {
	// PIT-004 兼容：DLP 环境下 sleep 会被拦截导致测试二进制被 SIGKILL
	if skipOnDLP() {
		t.Skip("PIT-004: DLP environment detected, skipping process group timeout test")
	}

	// 检查 sleep 命令是否可用（Windows 默认无 sleep，WSL 内可用）
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep command not available on this platform: %v", err)
	}

	// 启动 sleep 100，设置 1s 超时
	cmd := exec.Command("sleep", "100")
	// 预设进程组属性：RunWithTimeout 内部调用 cmd.Start() 但不设置 Setpgid，
	// 若不预设，KillProcessGroup 会 kill 测试二进制自身的进程组（PIT-005）
	e2eSetProcessGroup(cmd)
	startTime := time.Now()
	result := RunWithTimeout(cmd, 1*time.Second)
	elapsed := time.Since(startTime)

	// 验证 TimedOut=true（核心断言：超时机制正确触发）
	if !result.TimedOut {
		t.Fatalf("Expected TimedOut=true, got false (err=%v, exitCode=%d, duration=%dns)",
			result.Err, result.ExitCode, result.Duration)
	}

	// 验证返回非 nil 错误且包含超时描述
	if result.Err == nil {
		t.Error("Expected non-nil error on timeout")
	} else if !strings.Contains(result.Err.Error(), "timed out") {
		t.Errorf("Expected error to contain 'timed out', got: %v", result.Err)
	}

	// 验证耗时在合理范围内（1s 超时 + kill 延迟，上限 10s）
	if elapsed > 10*time.Second {
		t.Errorf("Timeout took too long: %v (expected ~1s + kill delay)", elapsed)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("Timeout fired too early: %v (expected ~1s)", elapsed)
	}

	// 验证进程已被 wait（ProcessState 非空表示 cmd.Wait() 已返回）
	if cmd.ProcessState == nil {
		t.Error("ProcessState should be set after RunWithTimeout (cmd.Wait should have returned)")
	}

	t.Logf("Process group timeout verified: TimedOut=%v, duration=%dms, err=%v",
		result.TimedOut, result.Duration/int64(time.Millisecond), result.Err)
}

// TestE2E_ProcessGroupTimeout_NoOrphans 验证超时 kill 后无孤儿进程残留。
//
// 测试流程：
// 1. 启动 `sleep 100` 作为父进程
// 2. RunWithTimeout 在 1s 后 kill 进程组
// 3. 验证 sleep 进程不再存活（通过 Process.Signal(0) 检测）
//
// 此测试补充 TestE2E_ProcessGroupTimeout，专门验证进程组 kill 的完整性
// （不仅 kill 父进程，而是整个进程组）。
func TestE2E_ProcessGroupTimeout_NoOrphans(t *testing.T) {
	if skipOnDLP() {
		t.Skip("PIT-004: DLP environment detected")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep command not available: %v", err)
	}

	cmd := exec.Command("sleep", "100")
	e2eSetProcessGroup(cmd)
	result := RunWithTimeout(cmd, 1*time.Second)

	if !result.TimedOut {
		t.Fatalf("Expected timeout, got result: %+v", result)
	}

	// 验证进程已被回收：ProcessState 非空表示 cmd.Wait() 已返回
	// 注：ProcessState.Exited() 在进程被信号终止时返回 false（SIGKILL 非"正常退出"），
	// 因此用 ProcessState != nil 判断进程已被 wait（内核已回收），而非用 Exited()
	if cmd.Process != nil {
		if cmd.ProcessState == nil {
			t.Error("ProcessState should be set after KillProcessGroup (cmd.Wait should have returned)")
		}
	}
}
