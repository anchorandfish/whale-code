// Package sandbox — OS 级隔离统一测试（/ T1-P1-B9）。
//
// 测试覆盖：
// - OSLevelConfig 零值默认
// - degradedSandbox 全方法（Enter/Exit/Restrict/Platform）
// - degradedSandbox 并发安全（-race）
// - newPlatformSandbox 工厂选择（跨平台编译验证）
// - Linux 平台：syscallCompatCheck / parseKernelVersion / linuxNamespaceSandbox
// - Windows 平台：windowsJobSandbox 句柄管理（仅 Windows 编译时启用）
// - macOS 平台：darwinSeatbeltSandbox profile 生成（仅 darwin 编译时启用）
//
// 测试策略：
// - 跨平台测试（无 build tag）：验证 degradedSandbox + OSLevelConfig + 工厂接口
// - 平台测试（带 build tag）：验证各平台特定行为
package sandbox

import (
	"context"
	"os/exec"
	"sync"
	"testing"
)

// TestOSLevelConfig_ZeroValues 验证零值配置所有开关 false。
func TestOSLevelConfig_ZeroValues(t *testing.T) {
	var cfg OSLevelConfig
	if cfg.Linux.UseNamespaces {
		t.Error("Linux.UseNamespaces should be false by default")
	}
	if cfg.Linux.UseUserNamespace {
		t.Error("Linux.UseUserNamespace should be false by default")
	}
	if cfg.Windows.UseJobObject {
		t.Error("Windows.UseJobObject should be false by default")
	}
	if cfg.Windows.MemoryLimit != 0 {
		t.Error("Windows.MemoryLimit should be 0 by default")
	}
	if cfg.Windows.MaxProcesses != 0 {
		t.Error("Windows.MaxProcesses should be 0 by default")
	}
	if cfg.Darwin.UseSeatbelt {
		t.Error("Darwin.UseSeatbelt should be false by default")
	}
}

// TestDefaultOSLevelConfig 验证默认配置函数。
func TestDefaultOSLevelConfig(t *testing.T) {
	cfg := DefaultOSLevelConfig()
	// 默认应禁用所有 OS 级隔离（与现状一致）
	if cfg.Linux.UseNamespaces {
		t.Error("default Linux.UseNamespaces should be false")
	}
	if cfg.Windows.UseJobObject {
		t.Error("default Windows.UseJobObject should be false")
	}
	if cfg.Darwin.UseSeatbelt {
		t.Error("default Darwin.UseSeatbelt should be false")
	}
	// UseUserNamespace 默认 true（rootless 友好）
	if !cfg.Linux.UseUserNamespace {
		t.Error("default Linux.UseUserNamespace should be true (rootless friendly)")
	}
}

// TestDefaultConstants 验证默认常量值。
func TestDefaultConstants(t *testing.T) {
	if DefaultLinuxNamespaces != "pid,net,uts" {
		t.Errorf("DefaultLinuxNamespaces = %q, want %q", DefaultLinuxNamespaces, "pid,net,uts")
	}
	if DefaultWindowsMemoryLimit != 512*1024*1024 {
		t.Errorf("DefaultWindowsMemoryLimit = %d, want %d", DefaultWindowsMemoryLimit, 512*1024*1024)
	}
	if DefaultWindowsMaxProcesses != 64 {
		t.Errorf("DefaultWindowsMaxProcesses = %d, want %d", DefaultWindowsMaxProcesses, 64)
	}
}

// TestDegradedSandbox_Enter 验证 degradedSandbox.Enter 返回 nil + noop cleanup。
func TestDegradedSandbox_Enter(t *testing.T) {
	sb := newDegradedSandbox("test reason", nil)
	handle, cleanup, err := sb.Enter()
	if err != nil {
		t.Errorf("Enter should not fail: %v", err)
	}
	if handle != nil {
		t.Errorf("handle should be nil, got %T", handle)
	}
	if cleanup == nil {
		t.Error("cleanup should not be nil")
	}
	// cleanup 可安全调用
	cleanup()
}

// TestDegradedSandbox_Exit_Idempotent 验证 Exit 多次调用幂等。
func TestDegradedSandbox_Exit_Idempotent(t *testing.T) {
	sb := newDegradedSandbox("test", nil)
	if err := sb.Exit(); err != nil {
		t.Errorf("first Exit should not fail: %v", err)
	}
	if err := sb.Exit(); err != nil {
		t.Errorf("second Exit should not fail: %v", err)
	}
	if err := sb.Exit(); err != nil {
		t.Errorf("third Exit should not fail: %v", err)
	}
}

// TestDegradedSandbox_Restrict 验证 Restrict 不修改 cmd。
func TestDegradedSandbox_Restrict(t *testing.T) {
	sb := newDegradedSandbox("test", nil)
	cmd := exec.Command("echo", "hello")
	origPath := cmd.Path
	origArgs := len(cmd.Args)
	if err := sb.Restrict(cmd); err != nil {
		t.Errorf("Restrict should not fail: %v", err)
	}
	if cmd.Path != origPath {
		t.Errorf("cmd.Path should not change: got %q, want %q", cmd.Path, origPath)
	}
	if len(cmd.Args) != origArgs {
		t.Errorf("cmd.Args length should not change: got %d, want %d", len(cmd.Args), origArgs)
	}
}

// TestDegradedSandbox_Platform 验证 Platform 返回 "degraded"。
func TestDegradedSandbox_Platform(t *testing.T) {
	sb := newDegradedSandbox("test", nil)
	if got := sb.Platform(); got != "degraded" {
		t.Errorf("Platform() = %q, want %q", got, "degraded")
	}
}

// TestDegradedSandbox_Enter_Idempotent 验证 Enter 多次调用幂等（sync.Once）。
func TestDegradedSandbox_Enter_Idempotent(t *testing.T) {
	sb := newDegradedSandbox("test", NewAuditLog())
	// 多次 Enter 应安全（sync.Once 保证只记录一次降级）
	h1, c1, _ := sb.Enter()
	h2, c2, _ := sb.Enter()
	if h1 != nil || h2 != nil {
		t.Error("handle should always be nil for degradedSandbox")
	}
	c1()
	c2()
}

// TestOSSandbox_Concurrent 测试 OSSandbox 并发安全（10 goroutine -race）。
//
// 子审查优化 5 吸纳：覆盖 Enter/Exit/Restrict 并发调用。
func TestOSSandbox_Concurrent(t *testing.T) {
	sb := newDegradedSandbox("concurrent test", NewAuditLog())

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
			if err := sb.Exit(); err != nil {
				t.Errorf("Exit failed: %v", err)
			}

			cmd := exec.Command("echo", "test")
			if err := sb.Restrict(cmd); err != nil {
				t.Errorf("Restrict failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestNewPlatformSandbox_DefaultDisabled 验证零值配置时工厂返回 degradedSandbox。
//
// 此测试在所有平台编译，验证默认行为（OS 级隔离禁用）。
func TestNewPlatformSandbox_DefaultDisabled(t *testing.T) {
	cfg := OSLevelConfig{} // 零值
	sb := newPlatformSandbox(cfg, NewAuditLog())
	if sb == nil {
		t.Fatal("newPlatformSandbox should not return nil")
	}
	// 零值配置应降级到 degradedSandbox
	if sb.Platform() != "degraded" {
		// 在 Linux/Windows/darwin 平台，零值配置仍应降级到 degradedSandbox
		// 因为 UseNamespaces/UseJobObject/UseSeatbelt 都是 false
		// 但若 Platform() 不是 "degraded"，则可能是因为平台工厂选择了平台实现
		// 但 cfg 是零值，所以 UseNamespaces=false，应该返回 degradedSandbox
		t.Errorf("Platform() = %q, want %q (zero config should degrade)", sb.Platform(), "degraded")
	}
}

// TestNewPlatformSandbox_NilAudit 验证 audit 为 nil 时不 panic。
func TestNewPlatformSandbox_NilAudit(t *testing.T) {
	cfg := OSLevelConfig{}
	sb := newPlatformSandbox(cfg, nil)
	if sb == nil {
		t.Fatal("newPlatformSandbox should not return nil even with nil audit")
	}
	// Enter 不应 panic（audit 为 nil 时 LogDegradation 应短路）
	_, cleanup, err := sb.Enter()
	if err != nil {
		t.Errorf("Enter should not fail with nil audit: %v", err)
	}
	if cleanup != nil {
		cleanup()
	}
	_ = sb.Exit()
}

// TestManager_Prepare_OSSandboxField 验证 Manager.Prepare 填充 SandboxContext._osSandbox 字段。
//
// 即使 OS 级隔离未启用（零值配置），_osSandbox 也应是非 nil 的 degradedSandbox。
func TestManager_Prepare_OSSandboxField(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(&SandboxConfig{WorkDir: dir}, SandboxLevelRelaxed)

	// 注意：必须传非 nil context，否则 context.WithCancel(nil) 会 panic
	ctx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelRelaxed, dir)
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer cleanup()

	// _osSandbox 应为非 nil（degradedSandbox）
	osSandbox := ctx.OSSandbox()
	if osSandbox == nil {
		t.Fatal("OSSandbox() should not return nil (should be degradedSandbox)")
	}
	// 默认配置应为 degraded
	if osSandbox.Platform() != "degraded" {
		t.Errorf("Platform() = %q, want %q", osSandbox.Platform(), "degraded")
	}
}
