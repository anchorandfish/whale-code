// Package secret — 密钥脱敏正则集与扫描函数
package secret

import (
	"regexp"
	"strings"
)

// secretPattern 定义单个密钥检测规则。
type secretPattern struct {
	id    string         // 规则 ID（如 "anthropic_api_key"）
	label string         // 人类可读标签（如 "Anthropic API Key"）
	re    *regexp.Regexp // 编译后的正则
}

// sensitivePatterns 是所有密钥检测规则的有序列表。
// 迁移自 log.go 的 sensitivePatterns + H3 新增 Anthropic/PEM/GCP。
var sensitivePatterns = []secretPattern{
	{
		id:    "generic_api_key",
		label: "Generic API Key",
		re:    regexp.MustCompile(`(?i)(api[_-]?key|apikey|auth[_-]?token|access[_-]?token|secret[_-]?key|private[_-]?key)\s*[:=]\s*['"]?[^\s,;'"}]+\s*['"]?`),
	},
	{
		id:    "bearer_token",
		label: "Bearer Token",
		re:    regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9._\-]+`),
	},
	{
		id:    "openai_key",
		label: "OpenAI API Key",
		re:    regexp.MustCompile(`sk-[a-zA-Z0-9\-]{20,}`),
	},
	{
		id:    "password_in_connstr",
		label: "Password in Connection String",
		re:    regexp.MustCompile(`(?i)password\s*[:=]\s*['"]?[^\s,;'"}]+\s*['"]?`),
	},
	{
		id:    "jwt_token",
		label: "JWT Token",
		re:    regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}`),
	},
	{
		id:    "github_token",
		label: "GitHub Token",
		re:    regexp.MustCompile(`(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}`),
	},
	{
		id:    "aws_access_key",
		label: "AWS Access Key ID",
		re:    regexp.MustCompile(`\b(AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ASCA)[A-Z0-9]{16}\b`),
	},
	{
		id:    "slack_token",
		label: "Slack Token",
		re:    regexp.MustCompile(`xox[bpoau]-[A-Za-z0-9-]{10,72}`),
	},
	{
		id:    "url_userinfo",
		label: "URL Userinfo",
		re:    regexp.MustCompile(`(?i)([a-z][a-z0-9+\-.]*://)([^\s:/@]+):([^\s/@]+)@`),
	},
	// H3 新增：Anthropic API Key（whale-code 主用）
	{
		id:    "anthropic_api_key",
		label: "Anthropic API Key",
		re:    regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
	},
	// H3 新增：PEM 私钥
	{
		id:    "pem_private_key",
		label: "PEM Private Key",
		re:    regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	},
	// H3 新增：GCP API Key
	{
		id:    "gcp_api_key",
		label: "GCP API Key",
		re:    regexp.MustCompile(`AIza[A-Za-z0-9_-]{35}`),
	},
}

// RedactSensitive 对文本做密钥脱敏，命中规则的部分替换为 [REDACTED]。
// 守 02 §2.6.6 + H2 Redactor 契约：通用文本脱敏唯一入口。
// SSH 私钥 / H2 校验日志 / N6 错误日志 / N21 导出共用。
func RedactSensitive(s string) string {
	for _, sp := range sensitivePatterns {
		s = sp.re.ReplaceAllStringFunc(s, func(match string) string {
			return redactMatch(match)
		})
	}
	return s
}

// redactMatch 对单个匹配做上下文感知脱敏（迁自 log.go redactSensitive 逻辑）。
// 保留 key/type 前缀，仅脱敏值部分；URL userinfo 保留 scheme。
func redactMatch(match string) string {
	// URL userinfo 分支（scheme://user:pass@）
	// 仅当 match 真正以 scheme 开头（:// 前无 =）时才走此分支，
	// 避免 api_key=postgres://... 这类 match 被误判为 URL userinfo。
	if atIdx := strings.Index(match, "@"); atIdx > 0 {
		if schemeIdx := strings.Index(match, "://"); schemeIdx >= 0 && schemeIdx < atIdx {
			if !strings.Contains(match[:schemeIdx], "=") {
				// 保留 scheme，脱敏 user:pass
				return match[:schemeIdx+3] + "[REDACTED]:[REDACTED]@"
			}
		}
	}
	// Preserve the key/type prefix but redact the value
	if idx := strings.Index(match, "="); idx > 0 {
		return match[:idx+1] + "[REDACTED]"
	}
	if idx := strings.Index(match, ":"); idx > 0 {
		return match[:idx+1] + " [REDACTED]"
	}
	if strings.HasPrefix(strings.ToLower(match), "bearer ") {
		return "Bearer [REDACTED]"
	}
	return "[REDACTED]"
}

// SecretMatch 表示文本中检测到的密钥命中信息（不返回匹配原文）。
type SecretMatch struct {
	RuleID string // 规则 ID（如 "anthropic_api_key" / "github_token"）
	Label  string // 人类可读标签（如 "Anthropic API Key"）
}

// ScanSecrets 扫描文本中的密钥，返回命中的规则 ID 列表（不返回原文）。
// 对标同类 CLI scanForSecrets（故意不返回匹配文本）。
func ScanSecrets(s string) []SecretMatch {
	var matches []SecretMatch
	seen := make(map[string]bool)
	for _, sp := range sensitivePatterns {
		if sp.re.MatchString(s) && !seen[sp.id] {
			matches = append(matches, SecretMatch{
				RuleID: sp.id,
				Label:  sp.label,
			})
			seen[sp.id] = true
		}
	}
	return matches
}

// IsSecretLeak 判断文本是否疑似包含未脱敏密钥。
// 用于密钥泄露检测：返回 true 时调用方应 wrap N6 ErrSecretLeak。
func IsSecretLeak(s string) bool {
	for _, sp := range sensitivePatterns {
		if sp.re.MatchString(s) {
			return true
		}
	}
	return false
}
