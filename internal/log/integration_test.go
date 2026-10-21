// Package log — 集成测试：验证 log 与 secret 模块的委托链路
package log

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// TestLog_Integration_SecretRedact 验证 log.RedactSensitive 与 secret.RedactSensitive 行为一致
func TestLog_Integration_SecretRedact(t *testing.T) {
	// 多种敏感模式，log 和 secret 的脱敏结果应完全一致
	inputs := []string{
		"api_key=sk-abc123def456",
		"Bearer eyJhbGciOiJIUzI1NiJ9.abc.def",
		"postgresql://user:secret@dbhost:5432/mydb",
		"token ghp_0123456789012345678901234567890123456",
		"aws key AKIAIOSFODNN7EXAMPLE end",
		"slack xoxb-" + "1234567890-1234567890123-abcdefghij",
		"normal message without secrets",
	}

	for _, input := range inputs {
		logResult := RedactSensitive(input)
		secretResult := secret.RedactSensitive(input)
		if logResult != secretResult {
			t.Errorf("log.RedactSensitive 与 secret.RedactSensitive 结果不一致:\n  input=%q\n  log=%q\n  secret=%q",
				input, logResult, secretResult)
		}
	}
}

// TestLog_Integration_SecretContract 验证 log 包守 02 §2.3.1 契约：脱敏唯一入口为 secret
func TestLog_Integration_SecretContract(t *testing.T) {
	// 契约要求：H3 是密钥脱敏的唯一入口，log.RedactSensitive 委托 secret.RedactSensitive
	// 验证：敏感信息经 log 包脱敏后不泄露
	sensitive := "api_key=sk-proj-abcdefghijklmnopqrstuvwxyz123456789"
	result := RedactSensitive(sensitive)
	if strings.Contains(result, "sk-proj-abcdefgh") {
		t.Errorf("log.RedactSensitive 未脱敏 secret 模块管辖的密钥: %q", result)
	}
}
