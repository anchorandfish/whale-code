package export

import (
	"strings"
	"testing"
)

// TestRedact_APIKey 测试 API Key 脱敏
func TestRedact_APIKey(t *testing.T) {
	r := &DefaultRedactor{}
	input := "api_key=sk-1234567890abcdef1234567890abcdef"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("API Key 未被脱敏")
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("脱敏结果中不包含 [REDACTED]，实际：%s", result)
	}
	if strings.Contains(result, "sk-1234567890") {
		t.Errorf("API Key 明文仍然存在")
	}
}

// TestRedact_GitHubToken 测试 GitHub Token 脱敏
func TestRedact_GitHubToken(t *testing.T) {
	r := &DefaultRedactor{}
	input := "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("GitHub Token 未被脱敏")
	}
	if strings.Contains(result, "ghp_") {
		t.Errorf("GitHub Token 明文仍然存在")
	}
}

// TestRedact_BearerToken 测试 Bearer Token 脱敏
func TestRedact_BearerToken(t *testing.T) {
	r := &DefaultRedactor{}
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgN_5QGWgMqjMqjMqjMqjMqjMqjM"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("Bearer Token 未被脱敏")
	}
	if !strings.Contains(result, "Bearer [REDACTED]") {
		t.Errorf("Bearer Token 脱敏格式不正确，实际：%s", result)
	}
}

// TestRedact_Password 测试密码脱敏
func TestRedact_Password(t *testing.T) {
	r := &DefaultRedactor{}
	input := "password=mySecretPassword123"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("密码未被脱敏")
	}
	if strings.Contains(result, "mySecretPassword") {
		t.Errorf("密码明文仍然存在，实际：%s", result)
	}
	if !strings.Contains(result, "password=[REDACTED]") {
		t.Errorf("密码脱敏格式不正确，实际：%s", result)
	}
}

// TestRedact_NoMatch 测试普通文本不变
func TestRedact_NoMatch(t *testing.T) {
	r := &DefaultRedactor{}
	input := "这是一段普通文本，不包含任何敏感信息。hello world 12345"
	result, count := r.Scrub(input)
	if count != 0 {
		t.Errorf("普通文本不应被脱敏，count=%d", count)
	}
	if result != input {
		t.Errorf("普通文本不应被修改，期望：%s，实际：%s", input, result)
	}
}

// TestRedact_MixedContent 测试混合文本
func TestRedact_MixedContent(t *testing.T) {
	r := &DefaultRedactor{}
	input := "配置文件内容：\napi_key=sk-test123456789012345678901234567890\npassword=secret123\n普通描述信息\n"
	result, count := r.Scrub(input)
	if count < 2 {
		t.Errorf("混合文本应至少脱敏 2 处，实际：%d", count)
	}
	if strings.Contains(result, "sk-test123456") {
		t.Errorf("API Key 明文仍然存在")
	}
	if strings.Contains(result, "secret123") {
		t.Errorf("密码明文仍然存在")
	}
	if !strings.Contains(result, "普通描述信息") {
		t.Errorf("普通文本不应被修改")
	}
}

// TestRedact_AWSKey 测试 AWS Access Key 脱敏
func TestRedact_AWSKey(t *testing.T) {
	r := &DefaultRedactor{}
	input := "AWS Key: AKIAIOSFODNN7EXAMPLE"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("AWS Key 未被脱敏")
	}
	if strings.Contains(result, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS Key 明文仍然存在")
	}
}

// TestRedact_URLUserinfo 测试 URL userinfo 脱敏
func TestRedact_URLUserinfo(t *testing.T) {
	r := &DefaultRedactor{}
	input := "postgres://user:pass@localhost:5432/db"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("URL userinfo 未被脱敏")
	}
	if !strings.Contains(result, "[REDACTED]:[REDACTED]@") {
		t.Errorf("URL userinfo 脱敏格式不正确，实际：%s", result)
	}
	if strings.Contains(result, "user:pass@") {
		t.Errorf("URL userinfo 明文仍然存在")
	}
}

// TestRedact_AnthropicAPIKey 测试 Anthropic API Key 脱敏（H3 新增）
func TestRedact_AnthropicAPIKey(t *testing.T) {
	r := &DefaultRedactor{}
	input := "anthropic_key=sk-ant-api03-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("Anthropic API Key 未被脱敏")
	}
	if strings.Contains(result, "sk-ant-") {
		t.Errorf("Anthropic API Key 明文仍然存在，实际：%s", result)
	}
}

// TestRedact_PEMPrivateKey 测试 PEM 私钥脱敏（H3 新增）
func TestRedact_PEMPrivateKey(t *testing.T) {
	r := &DefaultRedactor{}
	input := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0Z3v5j8K\n-----END RSA PRIVATE KEY-----"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("PEM 私钥未被脱敏")
	}
	if strings.Contains(result, "MIIEpAIBAAKCAQEA0Z3v5j8K") {
		t.Errorf("PEM 私钥明文仍然存在")
	}
}

// TestRedact_GCPAPIKey 测试 GCP API Key 脱敏（H3 新增）
// GCP API Key 正则 AIza[A-Za-z0-9_-]{35} 要求 AIza 后跟 35 字符（共 39 字符），
// 测试输入需补足长度，否则正则不匹配（与 secret 包 TestRedactSensitive_GCPAPIKey 对齐）。
func TestRedact_GCPAPIKey(t *testing.T) {
	r := &DefaultRedactor{}
	input := "gcp_key=AIzaSyA1234567890abcdefghijklmnopqrstuvwxyz"
	result, count := r.Scrub(input)
	if count == 0 {
		t.Errorf("GCP API Key 未被脱敏")
	}
	if strings.Contains(result, "AIzaSy") {
		t.Errorf("GCP API Key 明文仍然存在，实际：%s", result)
	}
}
