package security

import "testing"

// === 端到端守卫（经生产装配 Init + CheckCommand） ===
//
// 为什么必须有这一层：validator 的 DANG-REDIR-AST 命中后，`CheckCommand` 会返回
// Decision{Action: Deny, DecidedBy: DecidedByValidator}
// —— 这是 **L1 层硬拒**，与「要不要问」「用户批不批」无关。这正是 里
// 「点了允许照样拒」的机理：审批账本记 approved_session，执行链仍被 L1 拦下。
//
// 因此只修 validator 单测不够 —— 必须从**生产装配**（security.Init + CheckCommand）
// 证明该命令不再落到 validator 层硬拒，且真写敏感路径仍被拦。
//
// 复用同包 harness initSecurityForProbe（见 p014_ask_semantics_test.go）。

// TestGuard_W016_FDDupNotHardBlockedByValidator：fd 复制形态不得再被 validator 层硬拒。
func TestGuard_W016_FDDupNotHardBlockedByValidator(t *testing.T) {
	initSecurityForProbe(true) // 沙箱在强制，贴近真机

	for _, cmd := range []string{
		"go test ./... -count=1 2>&1",
		"go build ./... 2>&1",
		"cmd >&2",
	} {
		d := CheckCommand(cmd)
		if d.DecidedBy == DecidedByValidator {
			t.Errorf("cmd=%q 仍被 validator 层硬拒（ 未修复）：Action=%v Reason=%q",
				cmd, d.Action, d.Reason)
		}
	}
}

// TestGuard_W016_RealWriteStillHardBlockedByValidator：真写敏感路径必须仍由 validator 层拦下。
// 反例方向 —— 防止"为了修 2>&1 把整个写重定向判定放松"。
func TestGuard_W016_RealWriteStillHardBlockedByValidator(t *testing.T) {
	initSecurityForProbe(true)

	for _, cmd := range []string{
		"echo x > /etc/passwd",
		"echo x 2> /etc/passwd",
		"echo x >&/etc/passwd", // bash 视作 &>file，真会建文件
	} {
		d := CheckCommand(cmd)
		if d.DecidedBy != DecidedByValidator {
			t.Errorf("cmd=%q 未由 validator 层拦下（安全语义被削弱）：Action=%v DecidedBy=%q Reason=%q",
				cmd, d.Action, d.DecidedBy, d.Reason)
		}
	}
}
