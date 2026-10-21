// Package command — slash command registration and processing
package command

import (
	"fmt"
	"strings"
)

// shellSplitState 表示 ShellSplit 解析状态机的当前状态
type shellSplitState int

const (
	shellStateNormal      shellSplitState = iota // 普通状态（不在任何引号内）
	shellStateSingleQuote                        // 单引号内（所有字符字面解释）
	shellStateDoubleQuote                        // 双引号内（仅 $ ` " \ 可被反斜杠转义）
)

// ShellSplit 将字符串按 POSIX shell 引用规则切分为参数列表
// 规则（参考 POSIX V3_chap02 token recognition）：
//   - 单引号 '...'：引号内所有字符按字面解释（包括 \ 和双引号），不处理转义
//   - 双引号 "..."：引号内 \ 仅在 $ ` " \ 前 有特殊含义（其他情况保留字面 \）
//   - 反斜杠 \：双引号外可转义任何字符；\<newline> 表示行续接（删除 \ 和换行）
//   - 空白字符（空格/制表符/换行等）在引号外作为参数分隔符
//   - 未闭合引号返回错误（推荐报错语义，便于上层提示用户）
//   - 空字符串参数 ” 或 "" 被保留为长度 0 的参数
//   - 多字节字符（如中文）按 rune 逐字符处理，不会被截断
func ShellSplit(s string) ([]string, error) {
	var result []string
	var current strings.Builder
	state := shellStateNormal
	hasContent := false // 区分「未开始一个参数」与「参数为空字符串」

	runes := []rune(s)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		switch state {
		case shellStateNormal:
			switch {
			case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f':
				// 空白字符：结束当前 word（若有）
				if hasContent {
					result = append(result, current.String())
					current.Reset()
					hasContent = false
				}
				i++
			case r == '\'':
				// 进入单引号
				state = shellStateSingleQuote
				hasContent = true
				i++
			case r == '"':
				// 进入双引号
				state = shellStateDoubleQuote
				hasContent = true
				i++
			case r == '\\':
				// 反斜杠转义
				if i+1 < n {
					next := runes[i+1]
					if next == '\n' {
						// 行续接：跳过 \ 和换行
						i += 2
						hasContent = true
					} else {
						// 转义下一字符（字面解释，包括空白和引号）
						current.WriteRune(next)
						i += 2
						hasContent = true
					}
				} else {
					// 行尾单独的 \ 不规范，报错
					return nil, fmt.Errorf("shellquote: trailing backslash at end of input")
				}
			default:
				current.WriteRune(r)
				hasContent = true
				i++
			}

		case shellStateSingleQuote:
			// 单引号内：所有字符字面解释，包括 \ 和 "
			if r == '\'' {
				// 退出单引号
				state = shellStateNormal
				i++
			} else {
				current.WriteRune(r)
				i++
			}

		case shellStateDoubleQuote:
			// 双引号内：仅 $ ` " \ 可被 \ 转义，其他 \ 保留字面
			if r == '"' {
				// 退出双引号
				state = shellStateNormal
				i++
			} else if r == '\\' {
				if i+1 < n {
					next := runes[i+1]
					switch next {
					case '$', '`', '"', '\\':
						// 转义：写入 next 字符（字面）
						current.WriteRune(next)
						i += 2
					case '\n':
						// 行续接：跳过 \ 和换行
						i += 2
					default:
						// 其他情况：保留字面 \（不消耗下一字符）
						current.WriteRune('\\')
						i++
					}
				} else {
					// 双引号末尾的 \：保留字面
					current.WriteRune('\\')
					i++
				}
			} else {
				current.WriteRune(r)
				i++
			}
		}
	}

	// 检查未闭合引号
	if state != shellStateNormal {
		return nil, fmt.Errorf("shellquote: unterminated quote in input: %q", s)
	}

	// 处理最后一个 word
	if hasContent {
		result = append(result, current.String())
	}

	return result, nil
}

// ShellSplitOrFields 优先用 ShellSplit 解析，失败时降级到 strings.Fields
// 用于向后兼容旧行为：未闭合引号等错误场景回退到 strings.Fields，
// 避免破坏既有调用方（如 /hooks、/mcp 等命令处理器）。
// 注意：降级路径会丢失引号语义，但保证不阻塞命令执行
func ShellSplitOrFields(s string) []string {
	parts, err := ShellSplit(s)
	if err != nil {
		return strings.Fields(s)
	}
	return parts
}
