package command

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/util"
)

// OutputStyleSetter is the interface for setting output style
type OutputStyleSetter interface {
	// SetOutputStyle sets the output style
	SetOutputStyle(style string) error
	// GetOutputStyle gets the current output style
	GetOutputStyle() string
}

// NewOutputStyleCommandHandler creates the /output-style command handler
// 契约：02 §2.5 命令注册；02 §2.3.2 错误走 N6（§3.9 方案：handler 保持返回字符串格式，
// 错误消息中嵌入 [ErrorID] 标识，由调用方/Registry 层解析 wrap）。
func NewOutputStyleCommandHandler(setter OutputStyleSetter) func(args string) string {
	validStyles := []string{"default", "concise", "verbose", "technical"}

	return func(args string) string {
		args = strings.TrimSpace(strings.ToLower(args))
		if args == "" {
			current := "default"
			if setter != nil {
				current = setter.GetOutputStyle()
			}
			return util.BiSame("Current output style: %s\nOptions: default, concise, verbose, technical", "当前输出风格：%s\n可选：default, concise, verbose, technical", current)
		}

		if !isValidStyle(args, validStyles) {
			// §3.9：错误消息嵌入 ErrorID 标识，Registry 层可识别并 wrap
			return util.BiSame("[config_invalid] invalid output style: %s\nOptions: default, concise, verbose, technical", "[config_invalid] 无效的输出风格：%s\n可选：default, concise, verbose, technical", args)
		}

		if setter != nil {
			if err := setter.SetOutputStyle(args); err != nil {
				return util.BiSame("[config_invalid] failed to set output style: %v", "[config_invalid] 设置输出风格失败：%v", err)
			}
		}

		return util.BiSame("Output style switched to: %s\n  Saved to config file", "输出风格已切换为：%s\n  已保存到配置文件", args)
	}
}

// isValidStyle checks if the style is valid
func isValidStyle(style string, validStyles []string) bool {
	for _, s := range validStyles {
		if style == s {
			return true
		}
	}
	return false
}

// WrapOutputStyleError 检查 output-style handler 返回的字符串是否为错误消息，
// 若是则 wrap 为 N6 ErrorID 错误返回；否则返回原字符串和 nil。
// §3.9 方案实现：Registry 层调用 NewOutputStyleCommandHandler 后经此函数 wrap。
func WrapOutputStyleError(result string) (string, error) {
	if strings.HasPrefix(result, "["+string(ai.ErrConfigInvalid)+"]") {
		return "", ai.WithID(fmt.Errorf("%s", strings.TrimPrefix(result, "["+string(ai.ErrConfigInvalid)+"] ")), ai.ErrConfigInvalid)
	}
	return result, nil
}

// OutputStyleDescription 返回 output style 的中文 system prompt 描述。
// 契约：N14 自定义边界（非 02 §2.1.3，M4 修复），N15 消费此描述注入 system prompt。
// 修改点：英文 → 中文增强描述（G14 修复），更好地指导 AI 行为。
// 待 N15 合入后对接：N15 构造 system prompt 时调用此函数，default 返回空字符串时不插入。
func OutputStyleDescription(style string) string {
	switch strings.ToLower(style) {
	case "concise":
		return "输出风格：简洁模式。请保持回答简洁明了，只提供关键信息，减少冗余解释和背景铺垫，用最少的文字传达核心内容。"
	case "verbose":
		return "输出风格：详细模式。请提供详尽的解释和背景信息，包含完整的上下文、推理过程和多个示例，确保用户全面理解。"
	case "technical":
		return "输出风格：技术模式。请深入技术细节，包含代码示例、最佳实践、性能考量和架构分析，面向专业开发者输出高质量技术内容。"
	default:
		return ""
	}
}
