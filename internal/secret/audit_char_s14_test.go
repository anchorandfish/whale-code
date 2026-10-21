// Package secret — S14「密钥管理」域 characterization 测试（审计第一轮）。
//
// 目的：在不改动任何生产源码的前提下，把 whale-code 当前的真实行为**钉死**，
// 作为后续 Fix 的 A/B 基线（对齐 AUDIT_ROADMAP §3 第 2 步 + §5 护栏 1）。
//
// 约定：
//   - 本文件只陈述"现在是什么"，不评判对错；
//   - 凡用例名含 PIN_DEFECT / PIN_MEMORY 的，钉住的是当前**有缺陷/可疑**的行为。
//     Fix 阶段一经用户拍板，应**刻意翻转**对应断言，并在 diff 说明中逐条给出理由；
//     "翻转前绿、翻转后绿"不算数——必须能看到断言方向反转。
//   - 所有断言均为确定性输入，不依赖 HOME / 网络 / 时间。
package secret

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 1. env 脱敏（I7：密钥不进子进程环境）
// ---------------------------------------------------------------------------

// TestS14Char_ScrubEnv_StripsSecretsKeepsWhitelist 钉住 ScrubEnv 的白名单语义。
func TestS14Char_ScrubEnv_StripsSecretsKeepsWhitelist(t *testing.T) {
	envs := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/Users/someone",
		"ANTHROPIC_API_KEY=sk-ant-api03-AAAABBBBCCCCDDDDEEEE",
		"OPENAI_API_KEY=sk-0123456789abcdefghij",
		"GITHUB_TOKEN=ghp_012345678901234567890123456789012345",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"MY_CUSTOM_SECRET=whatever",
	}
	got := ScrubEnv(envs)

	keep := map[string]bool{"PATH=/usr/bin:/bin": true, "HOME=/Users/someone": true}
	if len(got) != len(keep) {
		t.Fatalf("ScrubEnv kept %d vars %v, want only %d whitelist vars", len(got), got, len(keep))
	}
	for _, e := range got {
		if !keep[e] {
			t.Errorf("ScrubEnv leaked non-whitelist var: %q", e)
		}
	}
}

// TestS14Char_ScrubEnvWith_PrefixAndExactAllow 钉住 extra 白名单（前缀 + 精确名）。
func TestS14Char_ScrubEnvWith_PrefixAndExactAllow(t *testing.T) {
	envs := []string{
		"PATH=/usr/bin",
		"GIT_AUTHOR_NAME=Alice",
		"GITHUB_TOKEN=ghp_012345678901234567890123456789012345",
		"OTHER_TOKEN=leak-me",
	}
	got := ScrubEnvWith(envs, []string{"GIT_", "GITHUB_TOKEN"})
	set := map[string]bool{}
	for _, e := range got {
		set[e] = true
	}
	if !set["PATH=/usr/bin"] || !set["GIT_AUTHOR_NAME=Alice"] || !set["GITHUB_TOKEN=ghp_012345678901234567890123456789012345"] {
		t.Errorf("ScrubEnvWith missing expected vars, got %v", got)
	}
	if set["OTHER_TOKEN=leak-me"] {
		t.Errorf("ScrubEnvWith should not keep OTHER_TOKEN, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// 2. RedactSensitive 主流模式（应脱敏）
// ---------------------------------------------------------------------------

// TestS14Char_RedactSensitive_MainstreamPatterns 钉住各主流前缀的脱敏结果。
func TestS14Char_RedactSensitive_MainstreamPatterns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"openai_greedy", "sk-0123456789abcdefghijklmnop", "[REDACTED]"},
		{"anthropic_greedy", "sk-ant-api03-AAAABBBBCCCCDDDDEEEE", "[REDACTED]"},
		{"bearer", "Authorization: Bearer abcdef1234567890", "Authorization: Bearer [REDACTED]"},
		{"password_assign", "password: hunter2hunter2", "password: [REDACTED]"},
		{"kv_api_key", "ANTHROPIC_API_KEY=sk-ant-api03-AAAABBBBCCCCDDDD", "ANTHROPIC_API_KEY=[REDACTED]"},
		{"url_userinfo", "postgres://alice:s3cr3tpass@db.internal:5432/app", "postgres://[REDACTED]:[REDACTED]@db.internal:5432/app"},
		{"pem_block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAxxxx\n-----END RSA PRIVATE KEY-----", "[REDACTED]"},
		{"slack", "xoxb-" + "123456789012-" + "abcdefghijklmnop", "[REDACTED]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RedactSensitive(c.in); got != c.want {
				t.Errorf("RedactSensitive(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestS14Char_RedactSensitive_JSONField 钉住 JSON 形态（config.Save / 日志 JSON）脱敏。
func TestS14Char_RedactSensitive_JSONField(t *testing.T) {
	in := `{"api_key":"sk-ant-api03-AAAABBBBCCCCDDDD","model":"claude-sonnet-4-5"}`
	want := `{"api_key":"[REDACTED]","model":"claude-sonnet-4-5"}`
	if got := RedactSensitive(in); got != want {
		t.Errorf("RedactSensitive(json) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3. ⚠️PIN_DEFECT：定长前缀类密钥在脱敏输出中残留尾部字符
// ---------------------------------------------------------------------------

// TestS14Char_PIN_DEFECT_TailResidue_AfterRedaction 钉住一个**当前缺陷**：
// 定长量词正则（github_token {36} / gcp_api_key {35}）只能吃掉固定长度，
// ReplaceAllStringFunc 只替换命中片段 → 超出部分的**密钥尾字符原样保留**。
//
// 影响：该残留会随 RedactSensitive 的输出进入日志 / 导出 / 反馈（部分泄露）。
// Fix 阶段应把断言翻转为"输出中不含任何密钥字符"。
func TestS14Char_PIN_DEFECT_TailResidue_AfterRedaction(t *testing.T) {
	// github_token: (ghp|...) _ [A-Za-z0-9]{36} —— 后面多带 4 个字符
	gh := "ghp_" + strings.Repeat("A", 36) + "ZZZZ"
	ghOut := RedactSensitive(gh)
	if ghOut != "[REDACTED]ZZZZ" {
		t.Fatalf("current behavior changed: RedactSensitive(github) = %q, want %q", ghOut, "[REDACTED]ZZZZ")
	}
	if !strings.Contains(ghOut, "ZZZZ") {
		t.Errorf("expected tail residue to be present (pinned defect), got %q", ghOut)
	}

	// gcp_api_key: AIza[A-Za-z0-9_-]{35} —— 后面多带 2 个字符
	gcp := "AIza" + strings.Repeat("B", 35) + "ZZ"
	gcpOut := RedactSensitive(gcp)
	if gcpOut != "[REDACTED]ZZ" {
		t.Fatalf("current behavior changed: RedactSensitive(gcp) = %q, want %q", gcpOut, "[REDACTED]ZZ")
	}
}

// TestS14Char_PIN_DEFECT_AWSWordBoundaryMiss 钉住另一个**当前缺陷**：
// aws_access_key 正则带 `\b...\b`，当密钥与后续字母数字直接相连（无分隔）时，
// 末尾 \b 不成立 → 整条规则**不命中**，密钥完全不被脱敏。
func TestS14Char_PIN_DEFECT_AWSWordBoundaryMiss(t *testing.T) {
	standalone := "AKIA" + strings.Repeat("C", 16)
	if got := RedactSensitive(standalone); got != "[REDACTED]" {
		t.Fatalf("standalone AWS key should be redacted, got %q", got)
	}
	glued := "AKIA" + strings.Repeat("C", 16) + "DDD"
	if got := RedactSensitive(glued); got != glued {
		t.Fatalf("current behavior changed: glued AWS key = %q, want unchanged %q", got, glued)
	}
}

// ---------------------------------------------------------------------------
// 4. 检测入口一致性 / 不返回原文
// ---------------------------------------------------------------------------

// TestS14Char_DetectorsConsistent 钉住 IsSecretLeak / IsSecretLeakFast / ScanSecrets 一致。
func TestS14Char_DetectorsConsistent(t *testing.T) {
	inputs := []string{
		"nothing sensitive here",
		"x sk-ant-api03-AAAABBBBCCCCDDDD y",
		"token=ghp_012345678901234567890123456789012345",
	}
	for _, s := range inputs {
		slow, fast := IsSecretLeak(s), IsSecretLeakFast(s)
		if slow != fast {
			t.Errorf("IsSecretLeak=%v vs IsSecretLeakFast=%v for %q", slow, fast, s)
		}
		if slow != (len(ScanSecrets(s)) > 0) {
			t.Errorf("ScanSecrets disagrees with IsSecretLeak for %q", s)
		}
	}
}

// TestS14Char_DetectLeakWithLabels_NoRawSecretReturned 钉住"只返回标签、不返回原文"。
func TestS14Char_DetectLeakWithLabels_NoRawSecretReturned(t *testing.T) {
	const raw = "sk-ant-api03-AAAABBBBCCCCDDDD"
	labels, hit := DetectLeakWithLabels("key=" + raw)
	if !hit {
		t.Fatal("expected hit")
	}
	if strings.Contains(labels, raw) {
		t.Errorf("DetectLeakWithLabels returned raw secret in labels: %q", labels)
	}
	// 钉住现状：`sk-ant-...` 同时命中 openai_key 与 anthropic_api_key 两条规则，
	// 标签按 sensitivePatterns 声明顺序 join（openai 在 anthropic 之前）。
	if labels != "OpenAI API Key, Anthropic API Key" {
		t.Errorf("labels = %q, want %q", labels, "OpenAI API Key, Anthropic API Key")
	}
}

// TestS14Char_DetectLeakWithLabels_FailOpenOnLargeInput 钉住大输入不 panic
// （recover 兜底存在；注意该兜底语义是 fail-open → 见 STATUS 的 I2 Human gate）。
func TestS14Char_DetectLeakWithLabels_FailOpenOnLargeInput(t *testing.T) {
	large := strings.Repeat("A", 1<<20)
	labels, hit := DetectLeakWithLabels(large)
	if hit || labels != "" {
		t.Errorf("large plain input should be (%q,false), got (%q,%v)", "", labels, hit)
	}
}

// ---------------------------------------------------------------------------
// 5. ⚠️PIN_MEMORY：缓存脱敏器明文驻留内存
// ---------------------------------------------------------------------------

// TestS14Char_PIN_MEMORY_CachedRedactorRetainsPlaintext 钉住**当前实现细节**：
// RedactSensitiveCached 的 LRU 条目以"原文"为 key 且把原文存进 entry.input，
// 即**明文密钥在内存中驻留**（上限 256 条）。返回值本身已脱敏，故非文件泄露，
// 但属内存卫生问题（I7 边缘）。Fix 阶段若要改为只存哈希，需翻转此断言。
func TestS14Char_PIN_MEMORY_CachedRedactorRetainsPlaintext(t *testing.T) {
	const raw = "token=sk-ant-api03-AAAABBBBCCCCDDDD"
	ClearRedactCache()
	out := RedactSensitiveCached(raw)
	if out != "token=[REDACTED]" {
		t.Fatalf("RedactSensitiveCached = %q, want %q", out, "token=[REDACTED]")
	}
	redactCacheMu.RLock()
	entry, ok := redactCache[raw]
	redactCacheMu.RUnlock()
	if !ok {
		t.Fatal("expected cache entry keyed by raw text (pinned behavior)")
	}
	if entry.input != raw {
		t.Errorf("entry.input = %q, want raw plaintext %q (pinned behavior)", entry.input, raw)
	}
}

// ---------------------------------------------------------------------------
// 6. 敏感文件判定（未注入 global 时的回退）
// ---------------------------------------------------------------------------

// TestS14Char_IsSensitiveFile_FallbackWithoutGlobal 钉住未注入全局实例时的回退语义。
func TestS14Char_IsSensitiveFile_FallbackWithoutGlobal(t *testing.T) {
	saved := globalProjectIgnore
	globalProjectIgnore = nil
	t.Cleanup(func() { globalProjectIgnore = saved })

	for _, f := range []string{".env", ".env.local", "id_rsa", "deploy/app.pem", "config/.aws/credentials"} {
		if !IsSensitiveFile(f) {
			t.Errorf("IsSensitiveFile(%q) = false, want true", f)
		}
	}
	if IsSensitiveFile("README.md") {
		t.Error("IsSensitiveFile(README.md) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// 7. Redactor 默认实现等价性
// ---------------------------------------------------------------------------

// TestS14Char_DefaultRedactor_Equivalent 钉住 defaultRedactor / SecretRedactor 与 RedactSensitive 等价。
func TestS14Char_DefaultRedactor_Equivalent(t *testing.T) {
	in := "ANTHROPIC_API_KEY=sk-ant-api03-AAAABBBBCCCCDDDD"
	want := RedactSensitive(in)
	if got := NewRedactor().Redact(in); got != want {
		t.Errorf("defaultRedactor.Redact = %q, want %q", got, want)
	}
	if got := (SecretRedactor{}).RedactSensitive(in); got != want {
		t.Errorf("SecretRedactor.RedactSensitive = %q, want %q", got, want)
	}
	if got := Redact(in); got != want {
		t.Errorf("package-level Redact = %q, want %q", got, want)
	}
}
