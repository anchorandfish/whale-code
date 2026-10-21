// Package feedback — S14「密钥管理」域 characterization 测试：/feedback 上报脱敏。
//
// 生产路径：command 收集用户输入 → feedback.NewEntry（构造时脱敏）→ SubmitToGitHub / HTTP POST。
// 该链路是**跨信任边界**（用户机器的文本 → 外部服务），I7 要求脱敏先于外发。
//
// ⚠️PIN_DEFECT 用例钉住当前**有缺陷**的行为（见 secret/audit_char_s14_test.go 顶部约定）。
package feedback

import (
	"strings"
	"testing"
)

// TestS14Char_FeedbackRedactor_MainstreamCovered 钉住当前**有效**覆盖的模式。
// 方案 A 后：标签统一为 [REDACTED]（不带类型），且保留 key 前缀。
func TestS14Char_FeedbackRedactor_MainstreamCovered(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"anthropic", "sk-ant-api03-AAAABBBBCCCCDDDDEEEE", "[REDACTED]"},
		{"github_exact", "ghp_" + strings.Repeat("a", 36), "[REDACTED]"},
		{"bearer", "Authorization: Bearer my-secret-token", "Authorization: Bearer [REDACTED]"},
		{"password", "password=secret123", "password=[REDACTED]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RedactSensitive(c.in); got != c.want {
				t.Errorf("RedactSensitive(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestS14Char_FeedbackRedactor_MissingPatterns 已由 方案 A 翻转：
// feedback 现在委托 secret.RedactSensitive（唯一 SSOT），因此 gcp_api_key / pem_private_key
// 两条规则**已生效**，这两类密钥经 /feedback 会**被脱敏**（不再是明文外发）。
//
// 原为 PIN_DEFECT（钉住"缺失 gcp/pem 两条规则"的缺陷）；Fix 阶段按登记表 §7 附录 A.5 有意翻转。
func TestS14Char_FeedbackRedactor_MissingPatterns(t *testing.T) {
	gcp := "AIza" + strings.Repeat("B", 35)
	if got := RedactSensitive(gcp); strings.Contains(got, gcp) {
		t.Fatalf("gcp key not redacted: RedactSensitive(gcp) = %q, want redacted", got)
	}
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAxxxx\n-----END RSA PRIVATE KEY-----"
	if got := RedactSensitive(pem); strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("pem key not redacted: RedactSensitive(pem) = %q, want redacted", got)
	}
}

// TestS14Char_PIN_DEFECT_FeedbackRedactor_TailResidue 已由 方案 A 翻转：
// secret 侧 github_token 正则 `(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}` 与前身同源，
// 超出 {36} 的尾字符仍会残留；但整体 key 本体已被脱敏（不再明文外发）。
func TestS14Char_PIN_DEFECT_FeedbackRedactor_TailResidue(t *testing.T) {
	in := "ghp_" + strings.Repeat("a", 36) + "ZZZZ"
	got := RedactSensitive(in)
	// key 本体必须被脱敏（不得再出现 ghp_ 前缀 + 36 字符明文）
	if strings.Contains(got, "ghp_"+strings.Repeat("a", 36)) {
		t.Fatalf("github token body not redacted: RedactSensitive(github+tail) = %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker, got %q", got)
	}
}
