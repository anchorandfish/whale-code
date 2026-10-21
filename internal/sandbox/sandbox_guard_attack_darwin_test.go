//go:build darwin

// Package sandbox — macOS 内核级攻击用例（Guard Attack Tests）。
//
// 攻击面设计：载荷用 python3 + ctypes 直接调用 libc open()/socket()，
// 完全绕开 whale 的 Go 层路径校验。若 Seatbelt 真实生效（sandbox-exec
// 内核策略），即使绕开应用层也必须被拒绝；若仅靠 Go validator，
// 此类载荷将成功 —— 测试即失败。
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuard_Attack_SeatbeltKernelLevelDenied 端到端内核级攻击验证。
//
// 预期（Seatbelt 真实生效）：
// - write $HOME/.whale_guard_attack_probe → libc open fd=-1（工作区外写拒绝）
// - read ~/.ssh/id_rsa（存在时） → libc open fd=-1（敏感读拒绝）
// - socket connect 8.8.8.8:53 → 立即 denied（网络全断）
func TestGuard_Attack_SeatbeltKernelLevelDenied(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec 不可用（降级路径由 canary 测试覆盖）")
	}
	// 探针：本会话若已处于受限安全上下文（如 IDE 内嵌沙箱/受管运行时），
	// sandbox_apply 会被内核拒绝（exit 71）。此环境下无法验证内核级拦截，
	// 必须在真实终端人工执行（见 docs/人工测试记录）。
	//
	// ★ ：探针抽到**生产码** `SandboxExecUsable` 了 ——
	// 修复前它是本文件内联的一份，与工厂的 `os.Stat` 判定**各说各话**
	//（"闸门装在测试里、没装在判定里"）。现在判定与测试**共用同一份实现**，
	// 因此测试里"探针能识别"与生产里"判定能识别"不再可能分叉。
	if ok, why := SandboxExecUsable(); !ok {
		t.Skipf("sandbox_apply 在当前环境不可用（%s）——本会话可能已在受限上下文中，"+
			"请于真实终端执行本用例验证内核级拦截", why)
	}

	ws := t.TempDir()
	audit := NewAuditLog()
	sb := newPlatformSandbox(OSLevelConfig{Darwin: DarwinSandboxConfig{UseSeatbelt: true}}, audit)
	if sb.Platform() != "darwin-seatbelt" {
		t.Fatalf("Platform=%s, want darwin-seatbelt（沙箱已降级，攻击测试无意义）", sb.Platform())
	}
	if _, cleanup, err := sb.Enter(); err != nil {
		t.Fatalf("Enter: %v", err)
	} else if cleanup != nil {
		defer cleanup()
	}

	home, _ := os.UserHomeDir()
	sshKey := filepath.Join(home, ".ssh", "id_rsa")
	sshExists := false
	if _, err := os.Stat(sshKey); err == nil {
		sshExists = true
	}

	readSSH := ""
	if sshExists {
		readSSH = fmt.Sprintf(`
fd2 = libc.open(%q.encode(), 0)
if fd2 >= 0: libc.close(fd2)
results.append(("read_ssh_key", fd2, ctypes.get_errno()))
`, sshKey)
	}

	py := fmt.Sprintf(`
import ctypes, socket, os
libc = ctypes.CDLL("libc.dylib", use_errno=True)
results = []
fd = libc.open(os.path.expanduser("~/.whale_guard_attack_probe").encode(),
               0x201, 0o644)
if fd >= 0: libc.close(fd)
results.append(("write_outside", fd, ctypes.get_errno()))
%[1]s
net_rc = "connected"
try:
    s = socket.create_connection(("8.8.8.8", 53), timeout=5)
    s.close()
except OSError as e:
    net_rc = "denied:" + str(e)
results.append(("network", net_rc, 0))
for r in results: print("ATTACK_RESULT|" + "|".join(map(str, r)))
`, readSSH)

	cmd := exec.Command("/usr/bin/python3", "-c", py)
	cmd.Dir = ws
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	out, err := cmd.CombinedOutput()
	t.Logf("攻击载荷输出（退出 err=%v）:\n%s", err, out)

	writeBlocked, sshBlocked, netBlocked := false, false, false
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "ATTACK_RESULT|") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(line, "ATTACK_RESULT|"), "|", 3)
		switch parts[0] {
		case "write_outside":
			writeBlocked = parts[1] == "-1"
			if !writeBlocked {
				t.Errorf("内核级旁路成功：工作区外写 libc open fd=%s（Seatbelt 未生效！）", parts[1])
			}
		case "read_ssh_key":
			sshBlocked = parts[1] == "-1"
			if !sshBlocked {
				t.Errorf("内核级旁路成功：~/.ssh/id_rsa 可读 fd=%s（敏感读未拒绝）", parts[1])
			}
		case "network":
			netBlocked = strings.HasPrefix(parts[1], "denied")
			if !netBlocked {
				t.Errorf("内核级旁路成功：沙箱内可外联 8.8.8.8:53 rc=%s（网络未隔离）", parts[1])
			}
		}
	}
	if !writeBlocked {
		t.Error("write_outside 攻击结果缺失（载荷未执行？）")
	}
	if sshExists && !sshBlocked {
		t.Error("read_ssh_key 攻击结果缺失")
	}
	if !netBlocked {
		t.Error("network 攻击结果缺失")
	}
	_ = os.Remove(filepath.Join(home, ".whale_guard_attack_probe"))
}

// TestGuard_Attack_SeatbeltWorkspaceWriteStillAllowed 反向用例：
// 沙箱内写工作区文件必须成功（防止 profile 过严导致正常命令不可用）。
func TestGuard_Attack_SeatbeltWorkspaceWriteStillAllowed(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec 不可用")
	}
	probeCmd := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "--", "/usr/bin/true")
	if perr := probeCmd.Run(); perr != nil {
		t.Skipf("sandbox_apply 在当前环境被拒绝（%v）——请在真实终端执行", perr)
	}
	ws := t.TempDir()
	sb := newPlatformSandbox(OSLevelConfig{Darwin: DarwinSandboxConfig{UseSeatbelt: true}}, NewAuditLog())
	if _, cleanup, err := sb.Enter(); err != nil {
		t.Fatalf("Enter: %v", err)
	} else if cleanup != nil {
		defer cleanup()
	}
	cmd := exec.Command("/bin/sh", "-c", "echo workspace_write_ok > inside_ws.txt && cat inside_ws.txt")
	cmd.Dir = ws
	if err := sb.Restrict(cmd); err != nil {
		t.Fatalf("Restrict: %v", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "workspace_write_ok") {
		t.Errorf("工作区写被误拒（过严降级）: err=%v out=%s", err, out)
	}
}
