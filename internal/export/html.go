// Package export — M23-04 HTML 导出（SNAPSHOT）
//
// 本文件实现 HTML 格式导出，含基本样式与消息渲染。
// 对标同类 CLI 的 HTML 导出（官方文档）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：FormatHTML 函数，返回完整 HTML 文档字符串
// - 结构体字段：HTMLOption 含 Title/IncludeStyle/Redact
// - 分层权责：本文件只做"HTML 格式化"，脱敏由 Redactor
// - 降级策略：HTML 转义失败时用 text/template 兜底
// - TTL规则：无 TTL（每次导出独立）
package export

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// HTMLOption 是 HTML 导出选项（SNAPSHOT-1）。
type HTMLOption struct {
	Title        string // HTML 标题（默认 "Whale Code 会话导出"）
	IncludeStyle bool   // 是否包含内联 CSS 样式（默认 true）
	IncludeMeta  bool   // 是否包含 meta 信息（时间戳等，默认 true）
}

// DefaultHTMLOption 返回默认 HTML 导出选项（SNAPSHOT-2）。
func DefaultHTMLOption() HTMLOption {
	return HTMLOption{
		Title:        "Whale Code 会话导出",
		IncludeStyle: true,
		IncludeMeta:  true,
	}
}

// FormatHTML 将会话消息格式化为 HTML 文档（SNAPSHOT-3）。
//
// 行为：
// - 过滤已 Tombstoned 的消息（由调用方处理，本函数仅格式化）
// - 每条消息渲染为 <div class="message role-{role}">
// - 文本内容 HTML 转义（防 XSS）
// - 工具调用/结果用 <details> 折叠
// - 脱敏在格式化前执行（由 Redactor 处理）
//
// 参数：
// - msgs: 已过滤的消息列表
// - redactor: 脱敏器（可为 nil，表示不脱敏）
//
// 返回：完整 HTML 文档字符串 + 脱敏计数
func FormatHTML(msgs []session.Message, redactor Redactor) (string, int) {
	opt := DefaultHTMLOption()
	return FormatHTMLWithOption(msgs, redactor, opt)
}

// FormatHTMLWithOption 带选项的 HTML 格式化（SNAPSHOT-4）。
func FormatHTMLWithOption(msgs []session.Message, redactor Redactor, opt HTMLOption) (string, int) {
	var sb strings.Builder
	redactedCount := 0

	// HTML 头部
	sb.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n")
	sb.WriteString(fmt.Sprintf("<meta charset=\"UTF-8\">\n"))
	if opt.IncludeMeta {
		sb.WriteString(fmt.Sprintf("<meta name=\"generator\" content=\"whale-code\">\n"))
		sb.WriteString(fmt.Sprintf("<meta name=\"exported-at\" content=\"%s\">\n", time.Now().Format(time.RFC3339)))
	}
	title := opt.Title
	if title == "" {
		title = "Whale Code 会话导出"
	}
	sb.WriteString(fmt.Sprintf("<title>%s</title>\n", html.EscapeString(title)))

	// 内联样式
	if opt.IncludeStyle {
		sb.WriteString(htmlStyle())
	}
	sb.WriteString("</head>\n<body>\n")
	sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", html.EscapeString(title)))

	if opt.IncludeMeta {
		sb.WriteString(fmt.Sprintf("<div class=\"meta\">导出时间: %s | 消息数: %d</div>\n",
			time.Now().Format("2006-01-02 15:04:05"), len(msgs)))
	}

	sb.WriteString("<div class=\"conversation\">\n")

	// 渲染每条消息
	for _, msg := range msgs {
		role := html.EscapeString(string(msg.Role))
		sb.WriteString(fmt.Sprintf("<div class=\"message role-%s\">\n", role))
		sb.WriteString(fmt.Sprintf("<div class=\"role-label\">%s</div>\n", role))

		// 渲染 blocks
		for _, b := range msg.Blocks {
			text := extractBlockText(b)
			if text == "" {
				continue
			}
			// 脱敏
			if redactor != nil {
				scrubbed, n := redactor.Scrub(text)
				text = scrubbed
				redactedCount += n
			}
			// HTML 转义
			escaped := html.EscapeString(text)

			switch b.Type {
			case "text":
				sb.WriteString(fmt.Sprintf("<div class=\"block text-block\">%s</div>\n", escaped))
			case "thinking":
				sb.WriteString(fmt.Sprintf("<details class=\"block thinking-block\"><summary>思考过程</summary><pre>%s</pre></details>\n", escaped))
			case "tool_use":
				toolName := html.EscapeString(b.Name)
				sb.WriteString(fmt.Sprintf("<details class=\"block tool-use-block\"><summary>工具调用: %s</summary><pre>%s</pre></details>\n", toolName, escaped))
			case "tool_result":
				toolName := html.EscapeString(b.ToolName)
				sb.WriteString(fmt.Sprintf("<details class=\"block tool-result-block\"><summary>工具结果: %s</summary><pre>%s</pre></details>\n", toolName, escaped))
			default:
				sb.WriteString(fmt.Sprintf("<div class=\"block\">%s</div>\n", escaped))
			}
		}

		sb.WriteString("</div>\n") // .message
	}

	sb.WriteString("</div>\n") // .conversation
	sb.WriteString("</body>\n</html>\n")

	return sb.String(), redactedCount
}

// extractBlockText 从消息 block 提取文本内容（内部工具函数）。
func extractBlockText(b session.ContentBlock) string {
	switch b.Type {
	case "text":
		return b.Text
	case "thinking":
		return b.Thinking
	case "tool_use":
		if b.Input != "" {
			return b.Input
		}
		return ""
	case "tool_result":
		if b.Output != "" {
			return b.Output
		}
		return ""
	default:
		return ""
	}
}

// htmlStyle 返回内联 CSS 样式（SNAPSHOT-5）。
//
// 样式对标同类 CLI 导出的简洁现代风格：
// - 等宽字体（代码友好）
// - role-user / role-assistant 不同背景色区分
// - details 折叠块
func htmlStyle() string {
	return `<style>
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  max-width: 900px;
  margin: 0 auto;
  padding: 20px;
  background: #fafafa;
  color: #333;
  line-height: 1.6;
}
h1 { color: #2c3e50; border-bottom: 2px solid #3498db; padding-bottom: 10px; }
.meta { color: #7f8c8d; font-size: 0.9em; margin-bottom: 20px; }
.conversation { display: flex; flex-direction: column; gap: 12px; }
.message { padding: 12px 16px; border-radius: 8px; border-left: 4px solid #ccc; }
.role-user { background: #e3f2fd; border-left-color: #2196f3; }
.role-assistant { background: #f1f8e9; border-left-color: #4caf50; }
.role-system { background: #fff3e0; border-left-color: #ff9800; }
.role-label { font-weight: bold; text-transform: uppercase; font-size: 0.8em; margin-bottom: 8px; color: #666; }
.block { margin: 8px 0; }
.text-block { white-space: pre-wrap; }
details { background: rgba(0,0,0,0.03); padding: 8px; border-radius: 4px; margin: 4px 0; }
summary { cursor: pointer; font-weight: 500; }
pre { white-space: pre-wrap; word-wrap: break-word; margin: 8px 0 0 0; font-family: "SF Mono", Consolas, monospace; font-size: 0.9em; }
</style>
`
}
