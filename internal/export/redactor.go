package export

import (
	"strings"

	"github.com/anchorandfish/whale-code/internal/secret"
)

// DefaultRedactor 默认脱敏器
//
// 委托 secret.RedactSensitive（H3 权威入口）执行脱敏，
// 消除正则漂移风险——secret 包新增模式时自动同步。
type DefaultRedactor struct{}

// Scrub 对文本执行敏感信息脱敏
//
// 委托 secret.RedactSensitive 执行实际脱敏（12 个正则模式），
// 返回脱敏后文本 + 替换次数。
//
// 参数：
//   - text: 待脱敏的原始文本
//
// 返回：
//   - string: 脱敏后的文本
//   - int: 替换次数
func (d *DefaultRedactor) Scrub(text string) (string, int) {
	result := secret.RedactSensitive(text)
	if result == text {
		return result, 0
	}
	// 统计脱敏次数：结果中新增的 [REDACTED] 数量
	count := strings.Count(result, "[REDACTED]") - strings.Count(text, "[REDACTED]")
	return result, count
}
