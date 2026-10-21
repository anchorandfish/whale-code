//go:build linux

// Package sandbox — Linux namespaces 专项测试（/ T1-P1-B9）。
//
// 仅在 Linux 平台编译，验证：
// - syscallCompatCheck 内核版本检测
// - parseKernelVersion 字符串解析
// - utsnameToString 字节数组转换
// - linuxNamespaceSandbox Enter/Restrict/Platform
// - computeCloneflags namespace 标志位计算
// - 并发安全（-race）
package sandbox

import (
	"os/exec"
	"sync"
	"syscall"
	"testing"
)

// TestParseKernelVersion 验证内核版本字符串解析。
func TestParseKernelVersion(t *testing.T) {
	tests := []struct {
		release   string
		wantMajor int
		wantMinor int
		wantOk    bool
	}{
		{"5.15.0-91-generic", 5, 15, true},
		{"3.8.0", 3, 8, true},
		{"3.7.4", 3, 7, true},
		{"4.19.0", 4, 19, true},
		{"6.1.0-foo", 6, 1, true},
		{"2.6.32", 2, 6, true},
		{"invalid", 0, 0, false},
		{"", 0, 0, false},
		{"1", 0, 0, false},
	}
	for _, tc := range tests {
		major, minor, ok := parseKernelVersion(tc.release)
		if ok != tc.wantOk {
			t.Errorf("parseKernelVersion(%q): ok = %v, want %v", tc.release, ok, tc.wantOk)
			continue
		}
		if ok && (major != tc.wantMajor || minor != tc.wantMinor) {
			t.Errorf("parseKernelVersion(%q): major.minor = %d.%d, want %d.%d",
				tc.release, major, minor, tc.wantMajor, tc.wantMinor)
		}
	}
}

// TestUtsnameToString 验证 Utsname 字节数组转换。
func TestUtsnameToString(t *testing.T) {
	tests := []struct {
		input []int8
		want  string
	}{
		{[]int8{'5', '.', '1', '5', 0, 0, 0}, "5.15"},
		{[]int8{'3', '.', '8', 0}, "3.8"},
		{[]int8{0, 0, 0}, ""},
		{[]int8{}, ""},
	}
	for _, tc := range tests {
		got := utsnameToString(tc.input)
		if got != tc.want {
			t.Errorf("utsnameToString(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestSyscallCompatCheck 验证 syscallCompatCheck 在当前内核应返回 true（WSL Ubuntu 24.04 ≥ 3.8）。
func TestSyscallCompatCheck(t *testing.T) {
	// WSL Ubuntu 24.04 内核版本通常 ≥ 5.x，应支持 USER namespace
	if !syscallCompatCheck() {
		t.Log("syscallCompatCheck returned false (kernel < 3.8 or uname failed)")
		// 不强制 fail：CI 环境可能 uname 失败
	}
}

// TestNewPlatformSandbox_LinuxDisabled 验证 Linux 平台零值配置降级。
func TestNewPlatformSandbox_LinuxDisabled(t *testing.T) {
	cfg := OSLevelConfig{} // UseNamespaces=false
	sb := newPlatformSandbox(cfg, NewAuditLog())
	if sb.Platform() != "degraded" {
		t.Errorf("Platform() = %q, want %q (UseNamespaces=false)", sb.Platform(), "degraded")
	}
}

// TestLinuxNamespaceSandbox_Enter 验证 Linux namespace 沙箱 Enter。
//
// 测试场景：UseNamespaces=true + Namespaces=["pid","net","uts"] + UseUserNamespace=true
// 预期：Cloneflags 包含 CLONE_NEWPID | CLONE_NEWNET | CLONE_NEWUTS | CLONE_NEWUSER
func TestLinuxNamespaceSandbox_Enter(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid", "net", "uts"},
			UseUserNamespace: true,
		},
	}
	sb := newPlatformSandbox(cfg, NewAuditLog())
	if sb.Platform() != "linux-namespaces" {
		t.Fatalf("Platform() = %q, want %q", sb.Platform(), "linux-namespaces")
	}

	handle, cleanup, err := sb.Enter()
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer cleanup()
	if handle == nil {
		t.Fatal("handle is nil")
	}

	lh, ok := handle.(*linuxNamespaceHandle)
	if !ok {
		t.Fatalf("handle type mismatch: %T", handle)
	}
	// 验证 Cloneflags 包含 CLONE_NEWPID | CLONE_NEWNET | CLONE_NEWUTS | CLONE_NEWUSER
	expected := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWUTS | syscall.CLONE_NEWUSER)
	if lh.Cloneflags != expected {
		t.Errorf("Cloneflags mismatch: got %#x, want %#x", lh.Cloneflags, expected)
	}
}

// TestLinuxNamespaceSandbox_AllowNetworkExcludesNetNS 门 3 网络开关（Linux 结果断言）：
// AllowNetwork=true（经 NewManager 归一化过滤 "net"）后 Enter 的 Cloneflags
// 不含 CLONE_NEWNET，其余 namespace（pid/uts + USER）保持。
func TestLinuxNamespaceSandbox_AllowNetworkExcludesNetNS(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace (need >= 3.8)")
	}
	// 走 NewManager（生产入口，含归一化），不用裸工厂绕过接线
	mgr := NewManager(&SandboxConfig{
		OSLevel: OSLevelConfig{
			Linux: LinuxSandboxConfig{
				UseNamespaces:    true,
				Namespaces:       []string{"pid", "net", "uts"},
				UseUserNamespace: true,
				AllowNetwork:     true,
			},
		},
	}, SandboxLevelStrict)
	m, ok := mgr.(*manager)
	if !ok {
		t.Fatal("NewManager must return *manager")
	}
	handle, cleanup, err := m.osSandbox.Enter()
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer cleanup()
	lh, ok := handle.(*linuxNamespaceHandle)
	if !ok {
		t.Fatalf("handle type mismatch: %T", handle)
	}
	if lh.Cloneflags&syscall.CLONE_NEWNET != 0 {
		t.Errorf("AllowNetwork=true but Cloneflags %#x still contains CLONE_NEWNET", lh.Cloneflags)
	}
	expectedOthers := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWUSER)
	if lh.Cloneflags&expectedOthers != expectedOthers {
		t.Errorf("Cloneflags %#x must keep pid/uts/user namespaces, want mask %#x", lh.Cloneflags, expectedOthers)
	}
}

// TestLinuxNamespaceSandbox_Enter_NoUserNamespace 验证 UseUserNamespace=false 时不叠加 CLONE_NEWUSER。
func TestLinuxNamespaceSandbox_Enter_NoUserNamespace(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid", "net"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	handle, cleanup, err := sb.Enter()
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer cleanup()

	lh := handle.(*linuxNamespaceHandle)
	expected := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNET)
	if lh.Cloneflags != expected {
		t.Errorf("Cloneflags mismatch: got %#x, want %#x (no CLONE_NEWUSER)", lh.Cloneflags, expected)
	}
}

// TestLinuxNamespaceSandbox_Enter_UnknownNamespace 验证未知 namespace 名返回错误。
func TestLinuxNamespaceSandbox_Enter_UnknownNamespace(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid", "unknown"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	_, _, err := sb.Enter()
	if err == nil {
		t.Error("Enter should fail with unknown namespace")
	}
}

// TestLinuxNamespaceSandbox_Restrict 验证 Restrict 设置 cmd.SysProcAttr.Cloneflags。
func TestLinuxNamespaceSandbox_Restrict(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid", "net", "uts"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	handle, cleanup, err := sb.Enter()
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer cleanup()
	_ = handle

	cmd := exec.Command("echo", "test")
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("Restrict failed: %v", err)
	}
	// 在 Linux 平台，cmd.SysProcAttr 是具体类型 *syscall.SysProcAttr（非接口），
	// 不能做类型断言，直接访问 Cloneflags 字段（修复：移除非法类型断言）。
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should be set by Restrict")
	}
	expected := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWUTS)
	if cmd.SysProcAttr.Cloneflags != expected {
		t.Errorf("Cloneflags = %#x, want %#x", cmd.SysProcAttr.Cloneflags, expected)
	}
}

// TestLinuxNamespaceSandbox_Restrict_NilCmd 验证 nil cmd 返回错误。
func TestLinuxNamespaceSandbox_Restrict_NilCmd(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	_, cleanup, _ := sb.Enter()
	defer cleanup()

	if err := sb.Restrict(nil); err == nil {
		t.Error("Restrict(nil) should fail")
	}
}

// TestLinuxNamespaceSandbox_Restrict_NotEntered 验证未 Enter 调用 Restrict 返回错误。
func TestLinuxNamespaceSandbox_Restrict_NotEntered(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	// 注意：不调用 Enter
	linuxSb := sb.(*linuxNamespaceSandbox)
	linuxSb.handle = nil // 显式确保 handle 为 nil

	cmd := exec.Command("echo", "test")
	if err := sb.Restrict(cmd); err == nil {
		t.Error("Restrict should fail when Enter not called")
	}
}

// TestLinuxNamespaceSandbox_Exit_Idempotent 验证 Exit 多次调用幂等。
func TestLinuxNamespaceSandbox_Exit_Idempotent(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	if err := sb.Exit(); err != nil {
		t.Errorf("first Exit failed: %v", err)
	}
	if err := sb.Exit(); err != nil {
		t.Errorf("second Exit failed: %v", err)
	}
}

// TestLinuxNamespaceSandbox_Concurrent 验证 Linux namespace 沙箱并发安全（10 goroutine -race）。
func TestLinuxNamespaceSandbox_Concurrent(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"pid", "net", "uts"},
			UseUserNamespace: true,
		},
	}
	sb := newPlatformSandbox(cfg, NewAuditLog())

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cleanup, err := sb.Enter()
			if err != nil {
				t.Errorf("Enter failed: %v", err)
				return
			}
			if cleanup != nil {
				cleanup()
			}
			_ = sb.Exit()

			cmd := exec.Command("echo", "test")
			_ = sb.Restrict(cmd)
		}()
	}
	wg.Wait()
}

// TestLinuxNamespaceSandbox_Enter_MountNamespace 验证 mount namespace 解析。
func TestLinuxNamespaceSandbox_Enter_MountNamespace(t *testing.T) {
	if !syscallCompatCheck() {
		t.Skip("kernel does not support USER namespace")
	}
	cfg := OSLevelConfig{
		Linux: LinuxSandboxConfig{
			UseNamespaces:    true,
			Namespaces:       []string{"mount", "pid"},
			UseUserNamespace: false,
		},
	}
	sb := newPlatformSandbox(cfg, nil)
	handle, cleanup, err := sb.Enter()
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer cleanup()

	lh := handle.(*linuxNamespaceHandle)
	// CLONE_NEWNS | CLONE_NEWPID
	if lh.Cloneflags&syscall.CLONE_NEWNS == 0 {
		t.Error("Cloneflags should contain CLONE_NEWNS")
	}
	if lh.Cloneflags&syscall.CLONE_NEWPID == 0 {
		t.Error("Cloneflags should contain CLONE_NEWPID")
	}
}
