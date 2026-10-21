// Package secret — L3 集成测试：验证密钥全链路传递（secret→hook→plugin→bridge→github）
package secret

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// ===== L3-A 内部模块集成 =====

// --- 集成链路 1：secret → hook（env 脱敏）---

// TestIntegration_SecretToHook_EnvScrubbing 验证 hook 模块使用的 ScrubEnv 接口行为正确。
// hook.go:1600 调用 secret.ScrubEnv(os.Environ()) 构建 hook 子进程环境变量。
func TestIntegration_SecretToHook_EnvScrubbing(t *testing.T) {
	// 模拟 os.Environ() 返回的环境变量（含密钥）
	envs := []string{
		"PATH=/usr/local/bin:/usr/bin",
		"HOME=/root",
		"TERM=xterm",
		"OPENAI_API_KEY=sk-proj-1234567890abcdef",
		"ANTHROPIC_API_KEY=sk-ant-api03-secret",
		"GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}

	// 调用 ScrubEnv（与 hook.go:1600 完全一致）
	result := ScrubEnv(envs)

	// 白名单变量保留
	found := 0
	for _, e := range result {
		switch {
		case strings.HasPrefix(e, "PATH="):
			found++
		case strings.HasPrefix(e, "HOME="):
			found++
		case strings.HasPrefix(e, "TERM="):
			found++
		}
	}
	if found != 3 {
		t.Errorf("expected 3 whitelist vars, got %d: %v", found, result)
	}

	// 所有密钥变量被剥离
	for _, e := range result {
		if strings.Contains(e, "OPENAI_API_KEY") ||
			strings.Contains(e, "ANTHROPIC_API_KEY") ||
			strings.Contains(e, "GITHUB_TOKEN") ||
			strings.Contains(e, "AWS_SECRET") {
			t.Errorf("secret var not stripped: %s", e)
		}
	}
}

// TestIntegration_SecretToHook_DefaultAllowedEnv 验证 hook 引用的 DefaultAllowedEnv 变量一致。
// hook.go:1581: var safeEnvWhitelist = secret.DefaultAllowedEnv
func TestIntegration_SecretToHook_DefaultAllowedEnv(t *testing.T) {
	// 验证 DefaultAllowedEnv 包含基本系统变量
	expected := []string{"PATH", "HOME", "USER", "TERM", "SHELL"}
	envSet := make(map[string]bool)
	for _, e := range DefaultAllowedEnv {
		envSet[e] = true
	}
	for _, e := range expected {
		if !envSet[e] {
			t.Errorf("DefaultAllowedEnv missing required var: %s", e)
		}
	}
}

// --- 集成链路 2：secret → plugin（SafeEnv with GIT_）---

// TestIntegration_SecretToPlugin_SafeEnv 验证 plugin 模块使用的 SafeEnv("GIT_") 接口行为正确。
// plugin.go:436,459,472 调用 secret.SafeEnv("GIT_") 构建 git 子进程环境变量。
func TestIntegration_SecretToPlugin_SafeEnv(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test.com")
	t.Setenv("OPENAI_API_KEY", "sk-proj-1234567890abcdef")

	result := SafeEnv("GIT_")

	// GIT_ 前缀变量保留
	gitFound := false
	for _, e := range result {
		if strings.HasPrefix(e, "GIT_AUTHOR_NAME=") {
			gitFound = true
		}
	}
	if !gitFound {
		t.Error("GIT_AUTHOR_NAME should be kept by SafeEnv(GIT_)")
	}

	// 密钥变量被剥离
	for _, e := range result {
		if strings.Contains(e, "OPENAI_API_KEY") {
			t.Errorf("OPENAI_API_KEY should be stripped: %s", e)
		}
	}
}

// --- 集成链路 3：secret → github（SafeEnv + ScanWhaleIgnore + WrapLeak）---

// TestIntegration_SecretToGithub_SafeEnvWithToken 验证 github 模块使用的 SafeEnv("GIT_", "GITHUB_TOKEN") 接口行为正确。
// pr.go:275,298,309,328 调用 secret.SafeEnv("GIT_", "GITHUB_TOKEN") 构建 github 子进程环境变量。
func TestIntegration_SecretToGithub_SafeEnvWithToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_1234567890abcdefghijklmnopqrstuvwxyz")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("OPENAI_API_KEY", "sk-proj-1234567890abcdef")

	result := SafeEnv("GIT_", "GITHUB_TOKEN")

	// GITHUB_TOKEN 精确匹配保留
	tokenFound := false
	for _, e := range result {
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			tokenFound = true
		}
	}
	if !tokenFound {
		t.Error("GITHUB_TOKEN should be kept by SafeEnv(GIT_, GITHUB_TOKEN)")
	}

	// GIT_ 前缀变量保留
	gitFound := false
	for _, e := range result {
		if strings.HasPrefix(e, "GIT_AUTHOR_NAME=") {
			gitFound = true
		}
	}
	if !gitFound {
		t.Error("GIT_AUTHOR_NAME should be kept by SafeEnv(GIT_, GITHUB_TOKEN)")
	}

	// 非白名单密钥变量被剥离
	for _, e := range result {
		if strings.Contains(e, "OPENAI_API_KEY") {
			t.Errorf("OPENAI_API_KEY should be stripped: %s", e)
		}
	}
}

// TestIntegration_SecretToGithub_ScanWhaleIgnore 验证 github PR 流程使用 ScanWhaleIgnore 阻断敏感文件。
// pr.go:265 调用 secret.ScanWhaleIgnore(files) 检查文件列表。
func TestIntegration_SecretToGithub_ScanWhaleIgnore(t *testing.T) {
	// 模拟 PR 文件列表
	files := []string{
		"src/main.go",
		"src/utils.go",
		".env",
		".env.production",
		"config/private.pem",
		"id_rsa",
		".aws/credentials",
		"README.md",
	}

	safe, blocked := ScanWhaleIgnore(files)

	// 安全文件在 safe 中
	for _, f := range []string{"main.go", "utils.go", "README.md"} {
		found := false
		for _, s := range safe {
			if strings.HasSuffix(s, f) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("safe file %s should be in safe list", f)
		}
	}

	// 敏感文件在 blocked 中
	for _, f := range []string{".env", "private.pem", "id_rsa", "credentials"} {
		found := false
		for _, b := range blocked {
			if strings.Contains(b, f) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("sensitive file %s should be in blocked list", f)
		}
	}
}

// TestIntegration_SecretToGithub_WrapLeak 验证 github PR 流程使用 WrapLeak 包装错误。
// pr.go:267 调用 secret.WrapLeak(fmt.Errorf("sensitive files detected: %v", blocked))
func TestIntegration_SecretToGithub_WrapLeak(t *testing.T) {
	blockedFiles := []string{".env", "id_rsa", "private.pem"}
	// 模拟 pr.go:267 的调用
	err := WrapLeak(fmt.Errorf("sensitive files detected: %v", blockedFiles))

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	msg := err.Error()

	// 降级模式应有 [secret_leak] 前缀
	if !strings.Contains(msg, "secret_leak") {
		t.Errorf("expected [secret_leak] prefix, got: %s", msg)
	}

	// 被阻断的文件名不应包含密钥特征（但文件名本身不含密钥，所以应该保留）
	if !strings.Contains(msg, "sensitive files detected") {
		t.Errorf("error message should contain context: %s", msg)
	}
}

// --- 集成链路 4：secret → bridge（密钥传递设计验证）---

// TestIntegration_SecretToBridge_DesignVerify 验证 bridge 的密钥传递设计。
// bridge/jwt.go:214 注释："密钥来源：调用方通过 H3 secret.SafeEnv 获取环境变量，传入构造函数。"
// bridge 本身不直接 import secret，而是由调用方使用 secret.SafeEnv 获取密钥后传入。
// 本测试验证此设计模式可行：SafeEnv 能正确提取 JWT 密钥。
func TestIntegration_SecretToBridge_DesignVerify(t *testing.T) {
	// 模拟调用方使用 SafeEnv 获取环境变量
	t.Setenv("BRIDGE_JWT_SECRET", "my-jwt-secret-key")
	t.Setenv("OPENAI_API_KEY", "sk-proj-1234567890abcdef")

	// 调用方通过 SafeEnv 获取白名单环境变量
	// bridge 的 JWT secret 不在默认白名单中，需要通过 extra 传入
	env := SafeEnv("BRIDGE_JWT_SECRET")

	// BRIDGE_JWT_SECRET 应在结果中
	jwtSecretFound := false
	for _, e := range env {
		if strings.HasPrefix(e, "BRIDGE_JWT_SECRET=") {
			jwtSecretFound = true
			// 调用方从 env 中提取密钥值，传入 bridge.NewTokenManagerWithVerify
			break
		}
	}
	if !jwtSecretFound {
		t.Error("BRIDGE_JWT_SECRET should be in SafeEnv result when passed as extra")
	}

	// 非白名单密钥变量被剥离
	for _, e := range env {
		if strings.Contains(e, "OPENAI_API_KEY") {
			t.Errorf("OPENAI_API_KEY should be stripped: %s", e)
		}
	}
}

// ===== L3 跨模块契约验证 =====

// TestContract_ScrubEnvSignature 验证 ScrubEnv 接口签名符合 02 §2.3.1 契约。
// 契约定义：secret.ScrubEnv(env []string) []string
func TestContract_ScrubEnvSignature(t *testing.T) {
	// 编译时验证签名
	var fn func([]string) []string = ScrubEnv
	if fn == nil {
		t.Error("ScrubEnv signature mismatch")
	}
}

// TestContract_RedactSensitiveIsUniqueEntry 验证 RedactSensitive 是密钥脱敏唯一入口。
// 02 §2.3.1：H3 是密钥脱敏的唯一入口，N6 的错误日志脱敏必须委托 H3。
func TestContract_RedactSensitiveIsUniqueEntry(t *testing.T) {
	// 验证所有敏感模式都能被 RedactSensitive 命中
	testCases := []struct {
		name  string
		input string
	}{
		{"openai", "sk-proj-1234567890abcdef1234"},
		{"anthropic", "sk-ant-api03-1234567890abcdef1234567890"},
		{"github", "ghp_1234567890abcdefghijklmnopqrstuvwxyz"},
		{"aws", "AKIAIOSFODNN7EXAMPLE"},
		{"slack", "xoxb-" + "1234567890-1234567890123"},
		{"gcp", "AIzaSyA1234567890abcdefghijklmnopqrstuvwxyz"},
		{"jwt", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1234567890\n-----END RSA PRIVATE KEY-----"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !IsSecretLeak(tc.input) {
				t.Errorf("pattern %s not detected by IsSecretLeak", tc.name)
			}
			redacted := RedactSensitive(tc.input)
			if redacted == tc.input {
				t.Errorf("pattern %s not redacted by RedactSensitive", tc.name)
			}
		})
	}
}

// TestContract_ErrorIDs 验证 ErrorID 使用符合 02 §2.3.2 契约。
// H3 必须使用 N6 定义的 ErrSecretLeak="secret_leak" 和 ErrSecretMissing="secret_missing"。
func TestContract_ErrorIDs(t *testing.T) {
	// 降级模式下验证 ErrorID
	err := WrapLeak(fmt.Errorf("test error"))
	if !strings.Contains(err.Error(), "secret_leak") {
		t.Errorf("WrapLeak should use ErrorID 'secret_leak', got: %s", err.Error())
	}

	err = WrapMissing(fmt.Errorf("test error"))
	if !strings.Contains(err.Error(), "secret_missing") {
		t.Errorf("WrapMissing should use ErrorID 'secret_missing', got: %s", err.Error())
	}
}

// TestContract_SSHPrivateKeyRedaction 验证 SSH 私钥管理复用 H3 secret.RedactSensitive。
// 02 §2.6.6：SSH 私钥管理必须复用 H3 secret.RedactSensitive。
func TestContract_SSHPrivateKeyRedaction(t *testing.T) {
	// SSH 私钥文件内容
	sshKey := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACA1234567890abcdefghijklmnopqrstuvwxyz
-----END OPENSSH PRIVATE KEY-----`

	// 验证 RedactSensitive 能脱敏 SSH 私钥
	result := RedactSensitive(sshKey)
	if strings.Contains(result, "b3BlbnNzaC1rZXktdjE") {
		t.Errorf("SSH private key not redacted: %s", result)
	}
	assertContains(t, []string{result}, "[REDACTED]")

	// 验证 IsSecretLeak 能检测 SSH 私钥
	if !IsSecretLeak(sshKey) {
		t.Error("IsSecretLeak should detect SSH private key")
	}
}

// ===== L3 端到端密钥传递链路验证 =====

// TestE2E_SecretPipeline 验证密钥全链路：env → ScrubEnv → hook/plugin/github 子进程。
// 模拟完整的密钥传递链路：
// 1. 系统环境变量中包含密钥
// 2. secret.ScrubEnv/SafeEnv 过滤后传递给子进程
// 3. 子进程环境变量中不包含密钥
func TestE2E_SecretPipeline(t *testing.T) {
	// 模拟系统环境变量
	originalEnv := os.Environ()

	// 追加测试密钥
	testEnvs := []string{
		"OPENAI_API_KEY=sk-proj-1234567890abcdef",
		"ANTHROPIC_API_KEY=sk-ant-api03-secret1234567890",
		"GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"GIT_AUTHOR_NAME=testuser",
	}

	// 模拟 hook 子进程环境构建（hook.go:1600）
	hookEnv := ScrubEnv(append(originalEnv, testEnvs...))

	// 验证 hook 子进程环境不含密钥
	for _, e := range hookEnv {
		if strings.Contains(e, "sk-proj-") || strings.Contains(e, "sk-ant-") || strings.Contains(e, "ghp_") {
			t.Errorf("hook env leaked secret: %s", e)
		}
	}

	// 模拟 github 子进程环境构建（pr.go:275）
	t.Setenv("GITHUB_TOKEN", "ghp_1234567890abcdefghijklmnopqrstuvwxyz")
	t.Setenv("GIT_AUTHOR_NAME", "testuser")
	githubEnv := SafeEnv("GIT_", "GITHUB_TOKEN")

	// GITHUB_TOKEN 和 GIT_ 变量保留
	tokenFound := false
	gitFound := false
	for _, e := range githubEnv {
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			tokenFound = true
		}
		if strings.HasPrefix(e, "GIT_AUTHOR_NAME=") {
			gitFound = true
		}
	}
	if !tokenFound {
		t.Error("GITHUB_TOKEN should be in github env")
	}
	if !gitFound {
		t.Error("GIT_AUTHOR_NAME should be in github env")
	}

	// 模拟 plugin 子进程环境构建（plugin.go:436）
	pluginEnv := SafeEnv("GIT_")

	// GIT_ 变量保留
	gitFound = false
	for _, e := range pluginEnv {
		if strings.HasPrefix(e, "GIT_AUTHOR_NAME=") {
			gitFound = true
		}
	}
	if !gitFound {
		t.Error("GIT_AUTHOR_NAME should be in plugin env")
	}
}
