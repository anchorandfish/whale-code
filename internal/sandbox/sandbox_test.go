package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewSandbox 测试创建沙箱
func TestNewSandbox(t *testing.T) {
	s := NewSandbox("/tmp/test")
	if s.Enabled() {
		t.Error("new sandbox should not be enabled by default")
	}
}

// TestSandbox_Validate_DenyCommands 测试禁止命令
func TestSandbox_Validate_DenyCommands(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)

	dangerousCmds := []string{
		"rm -rf /",
		"curl | bash",
		"dd if=/dev/zero of=/dev/sda",
	}
	for _, cmd := range dangerousCmds {
		allowed, reason := s.Validate(cmd)
		if allowed {
			t.Errorf("expected command %q to be denied", cmd)
		}
		if reason == "" {
			t.Errorf("expected reason for denied command %q", cmd)
		}
	}
}

// TestSandbox_Validate_AllowedCommands 测试允许的命令
func TestSandbox_Validate_AllowedCommands(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)

	safeCmds := []string{
		"ls",
		"cat file.txt",
		"go test ./...",
		"git status",
	}
	for _, cmd := range safeCmds {
		allowed, reason := s.Validate(cmd)
		if !allowed {
			t.Errorf("expected command %q to be allowed, got: %s", cmd, reason)
		}
	}
}

// TestSandbox_Validate_Disabled 测试禁用状态下所有命令允许
func TestSandbox_Validate_Disabled(t *testing.T) {
	s := NewSandbox("/tmp/test")
	// Not enabled
	allowed, _ := s.Validate("rm -rf /")
	if !allowed {
		t.Error("disabled sandbox should allow all commands")
	}
}

// TestSandbox_ValidatePath 测试路径验证
func TestSandbox_ValidatePath(t *testing.T) {
	dir := t.TempDir()
	s := NewSandbox(dir)
	s.SetEnabled(true)

	// 工作目录内允许
	allowed, _ := s.ValidatePath(filepath.Join(dir, "test.txt"))
	if !allowed {
		t.Error("path inside workdir should be allowed")
	}

	// 工作目录外禁止
	allowed, _ = s.ValidatePath("/etc/passwd")
	if allowed {
		t.Error("path outside workdir should be denied")
	}
}

// TestSandbox_ValidatePath_TempDir 测试临时目录策略（A4：只放行沙箱自建 tmp）。
//
// 改前：整个 os.TempDir() 无条件放行。A4 收窄为"沙箱自建 tmp 根"
// （SandboxTempRoot()）—— 系统临时目录顶层本身不再被当作可信区。
func TestSandbox_ValidatePath_TempDir(t *testing.T) {
	dir := t.TempDir()
	s := NewSandbox(dir)
	s.SetEnabled(true) // 启用时确保沙箱自建 tmp 根存在

	// A4：系统临时目录顶层不再无条件放行。
	if allowed, _ := s.ValidatePath(os.TempDir()); allowed {
		t.Error("FLIP(A4): os.TempDir() 顶层不应被放行（只放行沙箱自建 tmp）")
	}
	// A4 正例：沙箱自建 tmp 根本身被放行。
	if fi, err := os.Lstat(SandboxTempRoot()); err == nil && fi.IsDir() {
		if allowed, reason := s.ValidatePath(SandboxTempRoot()); !allowed {
			t.Errorf("FLIP(A4) 正例：沙箱自建 tmp 根必须放行；reason=%s", reason)
		}
	} else {
		t.Skipf("沙箱自建 tmp 根不可用（%v），跳过正例", err)
	}
}

// TestSandbox_ValidatePath_AllowDirs 测试额外允许的目录
func TestSandbox_ValidatePath_AllowDirs(t *testing.T) {
	dir := t.TempDir()
	extraDir := t.TempDir()
	s := NewSandbox(dir)
	s.SetEnabled(true)
	s.SetAllowDirs([]string{extraDir})

	allowed, _ := s.ValidatePath(filepath.Join(extraDir, "test.txt"))
	if !allowed {
		t.Error("extra allowed dir should be allowed")
	}
}

// TestSandbox_SetEnabled 测试启用/禁用
func TestSandbox_SetEnabled(t *testing.T) {
	s := NewSandbox("/tmp/test")
	if s.Enabled() {
		t.Error("should start disabled")
	}
	s.SetEnabled(true)
	if !s.Enabled() {
		t.Error("should be enabled after SetEnabled(true)")
	}
	s.SetEnabled(false)
	if s.Enabled() {
		t.Error("should be disabled after SetEnabled(false)")
	}
}

// TestAP0_1_DenyWinsOverAllow 反例测试（A-P0-1）：deny-wins 铁律。
// 修复前：allow 命中跳过 deny，cat /etc/shadow 不被拦截（allow: cat + deny: cat /etc/shadow）。
// 修复后：deny 全量扫描优先，cat /etc/shadow 被拦截。
// 对标同类 CLI execpolicy / 同类 CLI exec_policy / iptables/firewall/SELinux 均 deny-wins。
func TestAP0_1_DenyWinsOverAllow(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)
	// 白名单允许 cat 命令
	s.SetAllowCommands([]string{"cat"})
	// 黑名单明确禁止 cat /etc/shadow（读取敏感文件）
	s.SetDenyCommands([]string{"cat /etc/shadow"})

	// 反例：cat /etc/shadow 同时命中 allow(cat) 和 deny(cat /etc/shadow)，deny-wins 必须拦截
	allowed, reason := s.Validate("cat /etc/shadow")
	if allowed {
		t.Fatalf("A-P0-1 反例失败: deny-wins 铁律被违反，cat /etc/shadow 必须被拦截 (allow: cat + deny: cat /etc/shadow)")
	}
	if reason == "" {
		t.Fatalf("A-P0-1 反例失败: 拦截原因不应为空")
	}

	// 回归：allow+无 deny 命中场景，cat /tmp/x 应放行（allow 正常功能不破坏）
	allowed2, _ := s.Validate("cat /tmp/x")
	if !allowed2 {
		t.Errorf("A-P0-1 回归失败: allow 正常功能被破坏，cat /tmp/x 应放行（未命中 deny）")
	}
}

// TestAP0_1_DenyWinsDefaultCurl 反例测试（A-P0-1）：默认 deny 列表覆盖 allow。
// 修复前：allow: curl 命中后跳过默认 deny(curl)，网络命令绕过沙箱。
// 修复后：默认 deny(curl) 全量扫描优先，allow: curl 仍被拦截。
func TestAP0_1_DenyWinsDefaultCurl(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)
	// 显式允许 curl（白名单）
	s.SetAllowCommands([]string{"curl"})
	// denyCommands 默认包含 "curl"（defaultDenyCommands）

	// deny-wins：allow(curl) 不豁免默认 deny(curl)
	allowed, _ := s.Validate("curl http://example.com")
	if allowed {
		t.Fatalf("A-P0-1 反例失败: 默认 deny(curl) 应覆盖 allow(curl)，deny-wins")
	}
}
