package export

import (
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// FormatMarkdown 将消息列表格式化为 Markdown
//
// 在现有 session.ExportMarkdown 基础上扩展：
//   - thinking 块：用 <details><summary>思考过程</summary>...</details> 折叠
//   - image 块：输出 [图片: {hash}]
//   - tool_use 块：输出 ### 工具调用: {name} + 参数
//   - tool_result 块：输出 ### 工具结果 + 代码块
//   - 元信息头：# Whale Code 导出 + 时间 + 消息数 + 脱敏数
//
// 参数：
//   - messages: 待格式化的消息列表
//   - redactor: 脱敏器，用于对文本内容脱敏
//
// 返回：
//   - string: 格式化后的 Markdown 文本
//   - int: 脱敏替换次数
func FormatMarkdown(messages []session.Message, redactor Redactor) (string, int) {
	var sb strings.Builder
	totalRedacted := 0

	sb.WriteString("# Whale Code 导出\n\n")
	sb.WriteString(fmt.Sprintf("- 导出时间：%s\n", time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("- 消息数量：%d\n", len(messages)))

	for _, msg := range messages {
		switch msg.Role {
		case session.RoleUser:
			sb.WriteString("\n## 用户\n\n")
			text, count := redactMessageText(msg, redactor)
			totalRedacted += count
			sb.WriteString(text)
			sb.WriteString("\n")
			writeImageBlocks(&sb, msg)
		case session.RoleAssistant:
			sb.WriteString("\n## 助手\n\n")
			for _, block := range msg.Blocks {
				switch block.Type {
				case "text":
					text, count := redactor.Scrub(block.Text)
					totalRedacted += count
					sb.WriteString(text)
					sb.WriteString("\n")
				case "thinking":
					thinking, count := redactor.Scrub(block.Thinking)
					totalRedacted += count
					sb.WriteString("<details><summary>思考过程</summary>\n\n")
					sb.WriteString(thinking)
					sb.WriteString("\n\n</details>\n")
				case "tool_use":
					input, count := redactor.Scrub(block.Input)
					totalRedacted += count
					sb.WriteString(fmt.Sprintf("### 工具调用: %s\n\n", block.Name))
					sb.WriteString("```json\n")
					sb.WriteString(input)
					sb.WriteString("\n```\n")
				case "image":
					hash := block.Base64Hash
					if hash == "" {
						hash = "未知"
					}
					sb.WriteString(fmt.Sprintf("\n[图片: %s]\n", hash))
				}
			}
		case session.RoleTool:
			for _, block := range msg.Blocks {
				if block.Type == "tool_result" {
					output, count := redactor.Scrub(block.Output)
					totalRedacted += count
					sb.WriteString(fmt.Sprintf("\n### 工具结果: %s\n\n", block.ToolName))
					sb.WriteString("```\n")
					sb.WriteString(output)
					sb.WriteString("\n```\n")
				}
			}
		case session.RoleSystem:
			sb.WriteString("\n## 系统\n\n")
			text, count := redactMessageText(msg, redactor)
			totalRedacted += count
			sb.WriteString(text)
			sb.WriteString("\n")
		}
	}

	sb.WriteString(fmt.Sprintf("\n---\n\n> 本次导出共脱敏 %d 处敏感信息\n", totalRedacted))

	return sb.String(), totalRedacted
}

// redactMessageText 提取消息中的文本内容并脱敏
//
// 遍历所有 text 类型的 ContentBlock，拼接文本后执行脱敏。
func redactMessageText(msg session.Message, redactor Redactor) (string, int) {
	var texts []string
	totalCount := 0
	for _, block := range msg.Blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if joined != "" {
		scrubbed, count := redactor.Scrub(joined)
		totalCount += count
		return scrubbed, totalCount
	}
	return "", 0
}

// writeImageBlocks 将消息中的 image 块写入 StringBuilder
//
// 输出格式：[图片: {hash}]，hash 为空时显示"未知"。
func writeImageBlocks(sb *strings.Builder, msg session.Message) {
	for _, block := range msg.Blocks {
		if block.Type == "image" {
			hash := block.Base64Hash
			if hash == "" {
				hash = "未知"
			}
			sb.WriteString(fmt.Sprintf("\n[图片: %s]\n", hash))
		}
	}
}
