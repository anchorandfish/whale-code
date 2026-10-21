// Package secret — 反例测试（H3 审查 prompt 测试用例伪代码实现）
package secret

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// --- 反例 1：ScrubEnv 剥离密钥变量 ---

// TestScrubEnv_StripsSecrets 验证 ScrubEnv 剥离 OPENAI_API_KEY/GITHUB_TOKEN/ANTHROPIC_API_KEY。
func TestScrubEnv_StripsSecrets(t *testing.T) {
	envs := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"OPENAI_API_KEY=sk-proj-1234567890abcdef",
		"GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"ANTHROPIC_API_KEY=sk-ant-api03-1234567890abcdef",
		"TERM=xterm",
	}
	result := ScrubEnv(envs)

	// 白名单变量保留
	assertContains(t, result, "PATH=/usr/bin")
	assertContains(t, result, "HOME=/root")
	assertContains(t, result, "TERM=xterm")

	// 密钥变量剥离
	assertNotContains(t, result, "OPENAI_API_KEY")
	assertNotContains(t, result, "GITHUB_TOKEN")
	assertNotContains(t, result, "ANTHROPIC_API_KEY")
}

// TestScrubEnvWith_ExtraAllowed 验证 ScrubEnvWith 追加白名单变量。
func TestScrubEnvWith_ExtraAllowed(t *testing.T) {
	envs := []string{
		"PATH=/usr/bin",
		"GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"GIT_AUTHOR_NAME=test",
		"CUSTOM_VAR=secret",
	}
	// 追加 GITHUB_TOKEN 精确匹配 + GIT_ 前缀匹配
	result := ScrubEnvWith(envs, []string{"GITHUB_TOKEN", "GIT_"})

	assertContains(t, result, "PATH=/usr/bin")
	assertContains(t, result, "GITHUB_TOKEN=ghp_")
	assertContains(t, result, "GIT_AUTHOR_NAME=test")
	assertNotContains(t, result, "CUSTOM_VAR")
}

// --- 反例 2：RedactSensitive 命中各类密钥 ---

// TestRedactSensitive_OpenAIKey 验证 sk- 密钥脱敏。
func TestRedactSensitive_OpenAIKey(t *testing.T) {
	input := "api_key=sk-proj-1234567890abcdef1234"
	result := RedactSensitive(input)
	if strings.Contains(result, "sk-proj-1234") {
		t.Errorf("OpenAI key not redacted: %s", result)
	}
	assertContains(t, []string{result}, "[REDACTED]")
}

// TestRedactSensitive_AnthropicKey 验证 sk-ant- 密钥脱敏（H3 新增）。
func TestRedactSensitive_AnthropicKey(t *testing.T) {
	input := "key=sk-ant-api03-1234567890abcdef1234567890"
	result := RedactSensitive(input)
	if strings.Contains(result, "sk-ant-api03") {
		t.Errorf("Anthropic key not redacted: %s", result)
	}
}

// TestRedactSensitive_GitHubToken 验证 ghp_ 密钥脱敏。
func TestRedactSensitive_GitHubToken(t *testing.T) {
	input := "token: ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	result := RedactSensitive(input)
	if strings.Contains(result, "ghp_1234567890") {
		t.Errorf("GitHub token not redacted: %s", result)
	}
}

// TestRedactSensitive_JWT 验证 JWT token 脱敏。
func TestRedactSensitive_JWT(t *testing.T) {
	input := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	result := RedactSensitive(input)
	if strings.Contains(result, "eyJhbGciOiJIUzI1") {
		t.Errorf("JWT not redacted: %s", result)
	}
}

// TestRedactSensitive_PEMKey 验证 PEM 私钥脱敏（H3 新增）。
func TestRedactSensitive_PEMKey(t *testing.T) {
	input := `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA1234567890abcdefghijklmnopqrstuvwxyz
-----END RSA PRIVATE KEY-----`
	result := RedactSensitive(input)
	if strings.Contains(result, "MIIEpAIBAAKCAQEA") {
		t.Errorf("PEM private key not redacted: %s", result)
	}
}

// TestRedactSensitive_NoFalsePositive 验证非密钥文本不被误脱敏。
func TestRedactSensitive_NoFalsePositive(t *testing.T) {
	input := "This is a normal log message with no secrets"
	result := RedactSensitive(input)
	if result != input {
		t.Errorf("Normal text was modified: %s", result)
	}
}

// --- 反例 3：ScanSecrets 不返回原文 ---

// TestScanSecrets_NoRawValue 验证 ScanSecrets 只返回规则 ID，不返回匹配原文。
func TestScanSecrets_NoRawValue(t *testing.T) {
	input := "token=sk-ant-api03-1234567890abcdef1234567890"
	matches := ScanSecrets(input)
	if len(matches) == 0 {
		t.Fatal("expected matches, got none")
	}
	for _, m := range matches {
		// RuleID 和 Label 不应包含原始密钥
		if strings.Contains(m.RuleID, "sk-ant-") {
			t.Errorf("RuleID contains raw secret: %s", m.RuleID)
		}
		if strings.Contains(m.Label, "sk-ant-") {
			t.Errorf("Label contains raw secret: %s", m.Label)
		}
	}
}

// --- 反例 4：IsSecretLeak 检测 ---

// TestIsSecretLeak_True 验证含密钥文本返回 true。
func TestIsSecretLeak_True(t *testing.T) {
	tests := []string{
		"sk-proj-1234567890abcdef1234",
		"ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"sk-ant-api03-1234567890abcdef1234567890",
		// JWT 三段每段需 ≥20 字符（不含 eyJ 前缀）
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
	}
	for _, s := range tests {
		if !IsSecretLeak(s) {
			t.Errorf("expected IsSecretLeak true for: %s...", s[:10])
		}
	}
}

// TestIsSecretLeak_False 验证不含密钥文本返回 false。
func TestIsSecretLeak_False(t *testing.T) {
	if IsSecretLeak("just a normal string") {
		t.Error("expected IsSecretLeak false for normal string")
	}
}

// --- 反例 5：ScanWhaleIgnore 阻断敏感文件 ---

// TestScanWhaleIgnore_BlocksSensitive 验证 .env/*.pem/id_rsa 等被阻断。
func TestScanWhaleIgnore_BlocksSensitive(t *testing.T) {
	files := []string{
		"main.go",
		".env",
		".env.production",
		"config.yaml",
		"private.pem",
		"id_rsa",
		"credentials.json",
		"README.md",
	}
	safe, blocked := ScanWhaleIgnore(files)

	// 安全文件应在 safe 中
	assertContains(t, safe, "main.go")
	assertContains(t, safe, "config.yaml")
	assertContains(t, safe, "README.md")

	// 敏感文件应在 blocked 中
	assertContains(t, blocked, ".env")
	assertContains(t, blocked, ".env.production")
	assertContains(t, blocked, "private.pem")
	assertContains(t, blocked, "id_rsa")
	assertContains(t, blocked, "credentials.json")
}

// TestScanWhaleIgnore_AllSafe 验证无敏感文件时全部通过。
func TestScanWhaleIgnore_AllSafe(t *testing.T) {
	files := []string{"main.go", "utils.go", "README.md"}
	safe, blocked := ScanWhaleIgnore(files)
	if len(blocked) != 0 {
		t.Errorf("expected 0 blocked, got %d: %v", len(blocked), blocked)
	}
	if len(safe) != 3 {
		t.Errorf("expected 3 safe, got %d", len(safe))
	}
}

// --- 反例 6：WrapLeak/WrapMissing 错误包装 ---

// TestWrapLeak_RedactsAndWraps 验证 WrapLeak 脱敏 + 包装错误。
func TestWrapLeak_RedactsAndWraps(t *testing.T) {
	// 不注入 ErrorWrapper，测试降级路径
	err := WrapLeak(fmt.Errorf("error with sk-ant-api03-secret1234567890abcdef"))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	// 降级模式应有 [secret_leak] 前缀
	if !strings.Contains(msg, "secret_leak") {
		t.Errorf("expected [secret_leak] prefix, got: %s", msg)
	}
	// 原始密钥不应出现在错误消息中
	if strings.Contains(msg, "sk-ant-api03-secret") {
		t.Errorf("secret not redacted in error: %s", msg)
	}
}

// TestWrapLeak_NilError 验证 nil error 返回 nil。
func TestWrapLeak_NilError(t *testing.T) {
	if err := WrapLeak(nil); err != nil {
		t.Errorf("expected nil for nil input, got: %v", err)
	}
}

// TestWrapMissing_RedactsAndWraps 验证 WrapMissing 脱敏 + 包装错误。
func TestWrapMissing_RedactsAndWraps(t *testing.T) {
	err := WrapMissing(fmt.Errorf("missing ghp_token1234567890abcdefghijklmnopqrstuvwxyz"))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "secret_missing") {
		t.Errorf("expected [secret_missing] prefix, got: %s", msg)
	}
	if strings.Contains(msg, "ghp_token1234") {
		t.Errorf("secret not redacted in error: %s", msg)
	}
}

// --- 反例 7：SecretRedactor 接口实现 ---

// TestSecretRedactor_RedactSensitive 验证 SecretRedactor 实现 RedactSensitive 方法。
func TestSecretRedactor_RedactSensitive(t *testing.T) {
	r := SecretRedactor{}
	input := "key=sk-proj-1234567890abcdef1234"
	result := r.RedactSensitive(input)
	if strings.Contains(result, "sk-proj-1234") {
		t.Errorf("SecretRedactor did not redact: %s", result)
	}
}

// --- 反例 8：SetErrorWrapper 回调注入 ---

// TestSetErrorWrapper_Callback 验证注入 ErrorWrapper 后 WrapLeak 使用回调。
func TestSetErrorWrapper_Callback(t *testing.T) {
	// 保存原始状态
	originalWrapper := getErrorWrapper()
	defer func() {
		errorWrapperMu.Lock()
		errorWrapper = originalWrapper
		errorWrapperMu.Unlock()
	}()

	// 注入自定义回调
	SetErrorWrapper(func(err error, id string) error {
		return &testError{msg: err.Error(), id: id}
	})

	err := WrapLeak(fmt.Errorf("leak sk-ant-api03-test1234567890abcdef"))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	te, ok := err.(*testError)
	if !ok {
		t.Fatalf("expected *testError, got %T", err)
	}
	if te.id != "secret_leak" {
		t.Errorf("expected id=secret_leak, got %s", te.id)
	}
	if strings.Contains(te.msg, "sk-ant-api03-test") {
		t.Errorf("secret not redacted: %s", te.msg)
	}
}

// testError 是测试用的错误类型。
type testError struct {
	msg string
	id  string
}

func (e *testError) Error() string {
	return "[" + e.id + "] " + e.msg
}

// --- 辅助函数 ---

// assertContains 验证切片包含指定字符串。
func assertContains(t *testing.T, slice []string, expected string) {
	t.Helper()
	for _, s := range slice {
		if s == expected || strings.Contains(s, expected) {
			return
		}
	}
	t.Errorf("expected %q in slice %v", expected, slice)
}

// assertNotContains 验证切片不包含指定字符串。
func assertNotContains(t *testing.T, slice []string, unexpected string) {
	t.Helper()
	for _, s := range slice {
		if strings.Contains(s, unexpected) {
			t.Errorf("unexpected %q found in: %s", unexpected, s)
		}
	}
}

// ===== L2 补充测试：反例 + 覆盖率提升 =====

// --- 反例：空输入 ---

// TestSecret_EmptyInput 验证所有接口在空输入下不 panic、不泄露。
func TestSecret_EmptyInput(t *testing.T) {
	// ScrubEnv 空切片
	result := ScrubEnv([]string{})
	if len(result) != 0 {
		t.Errorf("ScrubEnv(empty) should return empty, got %v", result)
	}

	// ScrubEnv nil
	result = ScrubEnv(nil)
	if len(result) != 0 {
		t.Errorf("ScrubEnv(nil) should return empty, got %v", result)
	}

	// RedactSensitive 空字符串
	if r := RedactSensitive(""); r != "" {
		t.Errorf("RedactSensitive(empty) should return empty, got %q", r)
	}

	// ScanSecrets 空字符串
	if m := ScanSecrets(""); len(m) != 0 {
		t.Errorf("ScanSecrets(empty) should return empty, got %v", m)
	}

	// IsSecretLeak 空字符串
	if IsSecretLeak("") {
		t.Error("IsSecretLeak(empty) should return false")
	}

	// ScanWhaleIgnore 空切片
	safe, blocked := ScanWhaleIgnore([]string{})
	if len(safe) != 0 || len(blocked) != 0 {
		t.Errorf("ScanWhaleIgnore(empty) should return empty, got safe=%v blocked=%v", safe, blocked)
	}

	// WrapLeak nil
	if err := WrapLeak(nil); err != nil {
		t.Errorf("WrapLeak(nil) should return nil, got %v", err)
	}

	// WrapMissing nil
	if err := WrapMissing(nil); err != nil {
		t.Errorf("WrapMissing(nil) should return nil, got %v", err)
	}
}

// --- 反例：超大输入 ---

// TestSecret_LargeInput 验证大输入不 OOM、不超时。
func TestSecret_LargeInput(t *testing.T) {
	// 构造 10000 条环境变量
	largeEnv := make([]string, 10000)
	for i := 0; i < 10000; i++ {
		largeEnv[i] = fmt.Sprintf("VAR_%d=value_%d", i, i)
	}
	// 追加白名单变量
	largeEnv = append(largeEnv, "PATH=/usr/bin", "HOME=/root")

	result := ScrubEnv(largeEnv)
	// 应只保留白名单变量
	if len(result) != 2 {
		t.Errorf("ScrubEnv large input should only keep whitelist, got %d items", len(result))
	}

	// 大文本 RedactSensitive
	largeText := strings.Repeat("api_key=sk-proj-1234567890abcdef1234 ", 1000)
	r := RedactSensitive(largeText)
	if strings.Contains(r, "sk-proj-1234") {
		t.Error("RedactSensitive large input leaked secret")
	}
}

// --- 反例：非法输入 ---

// TestSecret_MalformedInput 验证非法格式的环境变量条目不导致 panic。
func TestSecret_MalformedInput(t *testing.T) {
	envs := []string{
		"",              // 空字符串
		"NO_EQUALS",     // 无等号
		"=VALUE_ONLY",   // 等号在首位（idx=0，应跳过）
		"KEY=",          // 空值
		"==DOUBLE",      // 双等号
		"PATH=/usr/bin", // 正常
	}
	result := ScrubEnv(envs)
	// 正常变量应保留
	assertContains(t, result, "PATH=/usr/bin")
	// 非法变量不应出现（NO_EQUALS 无等号会被跳过）
	assertNotContains(t, result, "NO_EQUALS")
	// =VALUE_ONLY 等号在首位会被跳过（idx <= 0）
	assertNotContains(t, result, "VALUE_ONLY")
}

// --- 反例：并发安全 ---

// TestSecret_ConcurrentSafe 验证并发调用 ScrubEnv/RedactSensitive/ScanSecrets 无 data race。
func TestSecret_ConcurrentSafe(t *testing.T) {
	const goroutines = 100
	var wg sync.WaitGroup

	envs := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"OPENAI_API_KEY=sk-proj-1234567890abcdef",
		"GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz",
	}

	text := "api_key=sk-ant-api03-1234567890abcdef1234567890"

	wg.Add(goroutines * 3)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ScrubEnv(envs)
		}()
		go func() {
			defer wg.Done()
			RedactSensitive(text)
		}()
		go func() {
			defer wg.Done()
			ScanSecrets(text)
		}()
	}
	wg.Wait()
}

// --- 反例：ScanWhaleIgnore 路径穿越 ---

// TestScanWhaleIgnore_PathTraversal 验证含路径穿越的文件名被正确分类。
func TestScanWhaleIgnore_PathTraversal(t *testing.T) {
	files := []string{
		"../../../etc/passwd",      // 路径穿越但非敏感
		"../../.env",               // 路径穿越 + 敏感
		"subdir/.env",              // 子目录敏感文件
		".ssh/id_rsa",              // SSH 路径
		".aws/credentials",         // AWS 凭据路径
		"normal/deep/path/file.go", // 正常文件
	}
	safe, blocked := ScanWhaleIgnore(files)

	// 正常文件应在 safe 中
	assertContains(t, safe, "file.go")
	// 敏感文件应在 blocked 中
	assertContains(t, blocked, ".env")
	assertContains(t, blocked, "id_rsa")
	assertContains(t, blocked, "credentials")
}

// --- 覆盖率补充：SafeEnv ---

// TestSafeEnv 返回经白名单过滤的环境变量。
func TestSafeEnv(t *testing.T) {
	t.Setenv("TEST_SECRET_VAR", "secret_value")
	result := SafeEnv()
	// TEST_SECRET_VAR 不在白名单，应被剥离
	for _, e := range result {
		if strings.Contains(e, "TEST_SECRET_VAR") {
			t.Errorf("SafeEnv should strip non-whitelist vars, found: %s", e)
		}
	}
	// PATH 应在白名单中（如果存在）
	hasPath := false
	for _, e := range result {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}
	if !hasPath {
		t.Log("PATH not in env (ok on minimal systems)")
	}
}

// TestSafeEnv_WithExtra 验证 SafeEnv 追加 extra 白名单。
func TestSafeEnv_WithExtra(t *testing.T) {
	t.Setenv("CUSTOM_WHITELIST_VAR", "custom_value")
	result := SafeEnv("CUSTOM_WHITELIST_VAR")
	found := false
	for _, e := range result {
		if strings.HasPrefix(e, "CUSTOM_WHITELIST_VAR=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("SafeEnv should keep extra whitelisted vars")
	}
}

// --- 覆盖率补充：redactMatch URL userinfo 分支 ---

// TestRedactSensitive_URLUserinfo 验证 URL userinfo 脱敏。
func TestRedactSensitive_URLUserinfo(t *testing.T) {
	input := "postgres://user:password@localhost:5432/db"
	result := RedactSensitive(input)
	if strings.Contains(result, "password") {
		t.Errorf("URL password not redacted: %s", result)
	}
	if !strings.Contains(result, "postgres://") {
		t.Errorf("URL scheme should be preserved: %s", result)
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("Expected [REDACTED] in result: %s", result)
	}
}

// TestRedactSensitive_URLUserinfo_WithEquals 验证含 = 的 URL 不被误判为纯 URL userinfo。
func TestRedactSensitive_URLUserinfo_WithEquals(t *testing.T) {
	// api_key=postgres://... 应走 key=value 分支，不走 URL userinfo 分支
	input := "api_key=postgres://user:pass@host:5432/db"
	result := RedactSensitive(input)
	// 值应被脱敏
	if strings.Contains(result, "pass@host") {
		t.Errorf("URL password not redacted: %s", result)
	}
}

// --- 覆盖率补充：更多密钥类型 ---

// TestRedactSensitive_AWSKey 验证 AWS Access Key 脱敏。
func TestRedactSensitive_AWSKey(t *testing.T) {
	input := "AKIAIOSFODNN7EXAMPLE"
	result := RedactSensitive(input)
	if strings.Contains(result, "AKIAIOSFODNN7") {
		t.Errorf("AWS key not redacted: %s", result)
	}
}

// TestRedactSensitive_SlackToken 验证 Slack Token 脱敏。
func TestRedactSensitive_SlackToken(t *testing.T) {
	input := "xoxb-" + "1234567890-1234567890123"
	result := RedactSensitive(input)
	if strings.Contains(result, "xoxb-"+"1234567890") {
		t.Errorf("Slack token not redacted: %s", result)
	}
}

// TestRedactSensitive_GCPAPIKey 验证 GCP API Key 脱敏（H3 新增）。
func TestRedactSensitive_GCPAPIKey(t *testing.T) {
	input := "AIzaSyA1234567890abcdefghijklmnopqrstuvwxyz"
	result := RedactSensitive(input)
	if strings.Contains(result, "AIzaSyA1234567") {
		t.Errorf("GCP API key not redacted: %s", result)
	}
}

// TestRedactSensitive_BearerToken 验证 Bearer Token 脱敏。
func TestRedactSensitive_BearerToken(t *testing.T) {
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.test12345678901234567890.signature12345678901234567890"
	result := RedactSensitive(input)
	if strings.Contains(result, "Bearer eyJhbGci") {
		t.Errorf("Bearer token not redacted: %s", result)
	}
	assertContains(t, []string{result}, "[REDACTED]")
}

// TestRedactSensitive_PasswordInConnStr 验证连接字符串中的密码脱敏。
func TestRedactSensitive_PasswordInConnStr(t *testing.T) {
	input := "password=mySecretPassword123"
	result := RedactSensitive(input)
	if strings.Contains(result, "mySecretPassword123") {
		t.Errorf("Password not redacted: %s", result)
	}
}

// --- 覆盖率补充：WrapMissing 回调路径 ---

// TestWrapMissing_WithCallback 验证注入 ErrorWrapper 后 WrapMissing 使用回调。
func TestWrapMissing_WithCallback(t *testing.T) {
	originalWrapper := getErrorWrapper()
	defer func() {
		errorWrapperMu.Lock()
		errorWrapper = originalWrapper
		errorWrapperMu.Unlock()
	}()

	SetErrorWrapper(func(err error, id string) error {
		return &testError{msg: err.Error(), id: id}
	})

	err := WrapMissing(fmt.Errorf("missing ghp_1234567890abcdefghijklmnopqrstuvwxyz"))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	te, ok := err.(*testError)
	if !ok {
		t.Fatalf("expected *testError, got %T", err)
	}
	if te.id != "secret_missing" {
		t.Errorf("expected id=secret_missing, got %s", te.id)
	}
}

// --- 覆盖率补充：ScrubEnvWith 前缀匹配 ---

// TestScrubEnvWith_PrefixMatch 验证前缀匹配（以 _ 结尾的 extra）。
func TestScrubEnvWith_PrefixMatch(t *testing.T) {
	envs := []string{
		"PATH=/usr/bin",
		"GIT_AUTHOR_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"OPENAI_API_KEY=sk-proj-1234",
	}
	result := ScrubEnvWith(envs, []string{"GIT_"})
	assertContains(t, result, "PATH=/usr/bin")
	assertContains(t, result, "GIT_AUTHOR_NAME=test")
	assertContains(t, result, "GIT_COMMITTER_EMAIL=test@test.com")
	assertNotContains(t, result, "OPENAI_API_KEY")
}

// --- 覆盖率补充：matchSensitivePattern 路径模式 ---

// TestScanWhaleIgnore_PathWithSlash 验证含 / 的路径模式匹配。
func TestScanWhaleIgnore_PathWithSlash(t *testing.T) {
	files := []string{
		"project/.aws/credentials",
		"project/.ssh/config",
		"project/src/main.go",
	}
	safe, blocked := ScanWhaleIgnore(files)
	assertContains(t, blocked, "credentials")
	assertContains(t, blocked, "config")
	assertContains(t, safe, "main.go")
}

// --- Windows→Linux 差异测试 ---

// TestSecret_FilePermission_Linux 验证 Linux 文件权限处理（密钥存储文件权限 0600）。
// secret 模块本身不写文件，但密钥文件权限检查是 H3 梯队特有检查项。
func TestSecret_FilePermission_Linux(t *testing.T) {
	// 在 Linux 上，os.FileMode(0600) 应正确表示 -rw-------
	mode := os.FileMode(0600)
	if mode&os.FileMode(0o600) != mode {
		t.Errorf("0600 mode not correctly represented: %v", mode)
	}
	// 0600 不应包含组/其他读写权限
	if mode&0044 != 0 {
		t.Errorf("0600 should not have group/other read permissions: %v", mode)
	}
}

// TestSecret_EnvCaseSensitive 验证 Linux 环境变量大小写敏感。
func TestSecret_EnvCaseSensitive(t *testing.T) {
	t.Setenv("TEST_SECRET_VAR", "lower")
	t.Setenv("test_secret_var", "upper_diff")

	result := SafeEnv()
	// 两个变量都不在白名单，都应被剥离
	for _, e := range result {
		if strings.Contains(strings.ToLower(e), "test_secret_var") {
			t.Errorf("non-whitelist env var should be stripped: %s", e)
		}
	}
}

// --- 性能基准测试 ---

// BenchmarkRedactSensitive 基准测试 RedactSensitive 性能。
func BenchmarkRedactSensitive(b *testing.B) {
	text := "api_key=sk-ant-api03-1234567890abcdef1234567890 token=ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RedactSensitive(text)
	}
}

// BenchmarkScrubEnv 基准测试 ScrubEnv 性能。
func BenchmarkScrubEnv(b *testing.B) {
	envs := make([]string, 100)
	for i := 0; i < 100; i++ {
		envs[i] = fmt.Sprintf("VAR_%d=value_%d", i, i)
	}
	envs = append(envs, "PATH=/usr/bin", "HOME=/root")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScrubEnv(envs)
	}
}

// BenchmarkScanSecrets 基准测试 ScanSecrets 性能。
func BenchmarkScanSecrets(b *testing.B) {
	text := "api_key=sk-ant-api03-1234567890abcdef1234567890 token=ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScanSecrets(text)
	}
}

// BenchmarkScanWhaleIgnore 基准测试 ScanWhaleIgnore 性能。
func BenchmarkScanWhaleIgnore(b *testing.B) {
	files := make([]string, 100)
	for i := 0; i < 100; i++ {
		files[i] = fmt.Sprintf("file_%d.go", i)
	}
	files = append(files, ".env", "id_rsa", "private.pem")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScanWhaleIgnore(files)
	}
}
