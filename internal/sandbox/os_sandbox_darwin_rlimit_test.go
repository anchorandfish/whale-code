//go:build darwin

// Package sandbox — macOS rlimit re-exec helper 端到端测试（v1 门 1，TDD）。
//
// 测试哲学：结果断言（ulimit 内核读回值 / 文件实际大小 / 命令实际退出码），
// 走生产调用链 NewManager → Prepare → Restrict → Start（与 bash.go 相同），
// 不 mock、不手工构造 helper 参数。
//
// 背景：修复前 macOS 无任何资源限制（resource_enforce_darwin.go post-start
// no-op，注释错误指向不存在的 Seatbelt (limit) rlimit 能力）。
// 修复后 Restrict 将命令改写为：
//
//	<exe> __whale-rlimit -- sandbox-exec -p <profile> -- <target>...
//
// helper 内 setrlimit（CPU/NOFILE/FSIZE）后 exec，限制随 execve 链继承。
// RLIMIT_AS（dyld 虚拟地址预留会误杀正常二进制）与 RLIMIT_NPROC（按 UID
// 全局计数，低于用户现有进程数会误杀沙箱内所有 fork）在 macOS 跳过；
// 降级记录走**沙箱初始化侧的审计通道**（os_sandbox_darwin.go Enter →
// audit.LogDegradation），**不写目标进程 stderr**（静默防护教训：降级必须有出口）。
// ★ 2026-09-30 B-2：此前 helper 进程会把这两条说明写到目标命令的 stderr，
// 被 Monitor 当成"子进程输出"回显给用户；该出口已收敛，判据见
// b2_rlimit_notice_channel_test.go。
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newRlimitTestManager 构造带资源限制的生产形态沙箱管理器。
func newRlimitTestManager(t *testing.T, ws string, limits ResourceLimits) Manager {
	t.Helper()
	return NewManager(&SandboxConfig{
		WorkDir: ws,
		OSLevel: OSLevelConfig{Darwin: DarwinSandboxConfig{
			UseSeatbelt:    true,
			ResourceLimits: limits,
		}},
	}, SandboxLevelStrict)
}

// runInSandbox 走生产调用链（Prepare → Restrict → Start → Wait）执行脚本。
func runInSandbox(t *testing.T, mgr Manager, ws, script string) ([]byte, error) {
	t.Helper()
	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelStrict, ws)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer cleanup()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = ws
	if err := sbCtx.OSSandbox().Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	return cmd.CombinedOutput()
}

// TestRlimitE2E_UlimitValuesApplied 结果断言：限制真实生效于内核
// （目标命令内 ulimit 读回 soft limit，与配置值逐一比对）。
func TestRlimitE2E_UlimitValuesApplied(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)
	ws := t.TempDir()
	mgr := newRlimitTestManager(t, ws, ResourceLimits{
		CPUMaxSeconds:     120,
		FileDescriptorMax: 128,
		FileSizeMaxBytes:  10 * 1024 * 1024,
	})
	out, err := runInSandbox(t, mgr, ws, "ulimit -n; ulimit -t; ulimit -f")
	if err != nil {
		t.Fatalf("script failed: %v, output: %s", err, out)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) != 3 {
		t.Fatalf("want 3 ulimit values, got %d: %q", len(lines), string(out))
	}
	// nofile / cpu(秒) / fsize(macOS ulimit -f 以 1024B 块计，实测)
	want := []int64{128, 120, 10 * 1024 * 1024 / 1024}
	names := []string{"nofile", "cpu", "fsize"}
	for i, w := range want {
		got, perr := strconv.ParseInt(lines[i], 10, 64)
		if perr != nil {
			t.Fatalf("parse %s: %v (output %q)", names[i], perr, string(out))
		}
		if got != w {
			t.Errorf("%s limit not applied in kernel: got %d, want %d", names[i], got, w)
		}
	}
}

// TestRlimitE2E_FileSizeEnforced 结果断言：写超限文件被内核截断（SIGXFSZ）。
// 主断言是文件实际大小（结果），退出码非零为辅。
func TestRlimitE2E_FileSizeEnforced(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)
	ws := t.TempDir()
	mgr := newRlimitTestManager(t, ws, ResourceLimits{
		FileSizeMaxBytes: 1024 * 1024, // 1MB
	})
	out, err := runInSandbox(t, mgr, ws, "head -c 3145728 /dev/zero > big.bin")
	big := filepath.Join(ws, "big.bin")
	info, statErr := os.Stat(big)
	if statErr != nil {
		t.Fatalf("expected truncated file to exist: %v (output %q)", statErr, string(out))
	}
	// 容差 1.5MB：写块粒度可能略超 1MB，但不允许长到 3MB
	if info.Size() >= 1536*1024 {
		t.Errorf("SECURITY: FSIZE not enforced, file grew to %d bytes (limit 1MB)", info.Size())
	}
	if err == nil {
		t.Errorf("write beyond RLIMIT_FSIZE should fail (SIGXFSZ), exited 0")
	}
}

// TestRlimitE2E_SkippedLimitsDoNotBreakNormal 结果断言（不误伤层）：
// 被跳过的 AS/NPROC 配置存在时，正常命令与子进程 spawn 不受影响。
func TestRlimitE2E_SkippedLimitsDoNotBreakNormal(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)
	ws := t.TempDir()
	mgr := newRlimitTestManager(t, ws, DefaultResourceLimits())
	out, err := runInSandbox(t, mgr, ws, "echo ok && (echo subshell-spawn-ok)")
	if err != nil || !strings.Contains(string(out), "subshell-spawn-ok") {
		t.Errorf("normal commands broken by skipped limits: err=%v out=%q", err, string(out))
	}
}

// TestRlimitE2E_SpecEnvNotLeaked 结果断言：spec env 不泄漏给目标命令
// （helper exec 前必须剥离，防配置信息侧信道）。
func TestRlimitE2E_SpecEnvNotLeaked(t *testing.T) {
	skipIfSeatbeltApplyDenied(t)
	ws := t.TempDir()
	mgr := newRlimitTestManager(t, ws, ResourceLimits{CPUMaxSeconds: 120})
	out, err := runInSandbox(t, mgr, ws, "env | grep -c WHALE_SANDBOX_RLIMIT_SPEC || true")
	if err != nil {
		t.Fatalf("script failed: %v, out: %s", err, out)
	}
	if n := strings.TrimSpace(string(out)); n != "0" {
		t.Errorf("spec env leaked to target command: count=%s", n)
	}
}

// TestRlimitHelperDirect_NoSeatbelt 结果断言（受限 runner 也可跑）：
// 不经 sandbox-exec，直接走 wrapWithRlimitHelper → Start → helper exec，
// 验证 helper 机制本身（setrlimit 生效 + ulimit 内核读回 + env 剥离）。
// 生产组合路径（seatbelt + rlimit 经 Restrict）由 TestRlimitE2E_* 在普通终端验证
// （受限 runner 中 sandbox_apply 被拒，只会得到错误结论——见 skipIfSeatbeltApplyDenied）。
func TestRlimitHelperDirect_NoSeatbelt(t *testing.T) {
	ws := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "ulimit -n; ulimit -t; ulimit -f; env | grep -c WHALE_SANDBOX_RLIMIT_SPEC || true")
	cmd.Dir = ws
	if err := wrapWithRlimitHelper(cmd, ResourceLimits{
		CPUMaxSeconds:     120,
		FileDescriptorMax: 128,
		FileSizeMaxBytes:  10 * 1024 * 1024,
	}); err != nil {
		t.Fatalf("wrapWithRlimitHelper: %v", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper command failed: %v, output: %s", err, out)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) != 4 {
		t.Fatalf("want 4 values (n/t/f/spec-count), got %d: %q", len(lines), string(out))
	}
	// fsize 以 1024B 块计（macOS 实测）
	want := []int64{128, 120, 10 * 1024 * 1024 / 1024, 0}
	names := []string{"nofile", "cpu", "fsize", "spec-env-count"}
	for i, w := range want {
		got, perr := strconv.ParseInt(lines[i], 10, 64)
		if perr != nil {
			t.Fatalf("parse %s: %v (output %q)", names[i], perr, string(out))
		}
		if got != w {
			t.Errorf("%s: got %d, want %d", names[i], got, w)
		}
	}
}

// TestMain 拦截 re-exec helper 调用。
//
// ★ 后这已是**冗余保险**：分发由本包 init 完成（helper_dispatch.go），
// 任何链接了 internal/sandbox 的二进制都自动覆盖。保留显式调用仅为可读性。
func TestMain(m *testing.M) {
	HandleSandboxHelperArg(os.Args)
	os.Exit(m.Run())
}
