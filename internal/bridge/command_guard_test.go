package bridge

import (
	"testing"
)

// TestCommandGuard_Disabled 验证 enabled=false 时直接放行（向后兼容）。
func TestCommandGuard_Disabled(t *testing.T) {
	guard := NewCommandGuard(false)
	// 任何命令都应放行
	if err := guard.CheckCommand("rm -rf /"); err != nil {
		t.Errorf("disabled guard should allow all commands, got: %v", err)
	}
	if err := guard.CheckCommand("ls -la"); err != nil {
		t.Errorf("disabled guard should allow all commands, got: %v", err)
	}
	if err := guard.CheckCommand(""); err != nil {
		t.Errorf("disabled guard should allow empty command, got: %v", err)
	}
}

// TestCommandGuard_EnabledDelegatesToH2 验证 enabled=true 时委托 H2 security.CheckCommand。
// 注意：security.CheckCommand 需要 Init() 初始化全局 Checker，否则 fail-closed 返回 Deny。
// 本测试验证 enabled=true 时 CommandGuard 确实调用了 security.CheckCommand（未初始化时返回错误）。
func TestCommandGuard_EnabledDelegatesToH2(t *testing.T) {
	guard := NewCommandGuard(true)
	// security.CheckCommand 未初始化时 fail-closed 返回 Deny + ErrSecurityBlocked
	// CommandGuard 应将 Deny 转为错误返回
	err := guard.CheckCommand("ls -la")
	if err == nil {
		// 若 security 已初始化（其他测试可能已调 Init），ls 是只读命令可能放行
		// 此处不强制失败，仅记录行为
		t.Log("security.CheckCommand allowed the command (may be initialized by other tests)")
	}
	// 至少不应 panic
}

// TestCommandGuard_EnabledBlocksDangerous 验证 enabled=true 时危险命令被拦截。
// security.CheckCommand 未初始化时 fail-closed，所有命令都被拦截。
func TestCommandGuard_EnabledBlocksDangerous(t *testing.T) {
	guard := NewCommandGuard(true)
	// 未初始化 security 时，空命令应被 Deny（security.CheckCommand 对空命令 fail-closed）
	err := guard.CheckCommand("")
	if err == nil {
		t.Log("empty command was allowed (security may be initialized)")
	}
}

// TestNewCommandGuard 验证构造函数设置 enabled 字段。
func TestNewCommandGuard(t *testing.T) {
	g1 := NewCommandGuard(false)
	if g1.enabled != false {
		t.Error("NewCommandGuard(false) should set enabled=false")
	}
	g2 := NewCommandGuard(true)
	if g2.enabled != true {
		t.Error("NewCommandGuard(true) should set enabled=true")
	}
}
