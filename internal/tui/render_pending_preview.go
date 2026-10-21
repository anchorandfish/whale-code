package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// pendingPreviewLineLimit 每条排队消息预览的最大行数（超出显示 "…"）。
// 对标同类 CLI PREVIEW_LINE_LIMIT = 3。
const pendingPreviewLineLimit = 3

// RenderPendingInputPreview 渲染排队输入预览（纯函数，不修改 Model 状态）。
// 渲染顺序：pending steers（P0 恒空）→ rejected steers（P0 恒空）→ queued messages。
// 仅当任一队列非空时返回非空字符串；否则返回空字符串（不渲染）。
// 对标同类 CLI as_renderable。
//
// 参数：
//   - queued: 排队消息列表（P0 仅此段有数据）
//   - pendingSteers: 待提交 steers（P0 恒为 nil，预留扩展点）
//   - rejectedSteers: 被拒绝 steers（P0 恒为 nil，预留扩展点）
//   - width: 渲染宽度（用于 wrap）
//
// 返回：渲染后的字符串（含 ANSI 样式），或空字符串。
func RenderPendingInputPreview(
	queued []QueuedUserMessage,
	pendingSteers []string,
	rejectedSteers []string,
	width int,
) string {
	if len(queued) == 0 && len(pendingSteers) == 0 && len(rejectedSteers) == 0 {
		return ""
	}
	if width < 20 {
		// 窄终端降级：仅显示条数（对标同类 CLI width < 4 降级）
		return renderNarrowPreview(len(queued))
	}

	var lines []string

	// Section 1: pending steers（P0 恒空，预留扩展点）
	if len(pendingSteers) > 0 {
		lines = append(lines, renderSectionHeader("Messages to be submitted after next tool call"))
		for _, steer := range pendingSteers {
			lines = append(lines, renderTruncatedMessage(steer, width, false))
		}
	}

	// Section 2: rejected steers（P0 恒空，预留扩展点）
	if len(rejectedSteers) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, renderSectionHeader("Messages to be submitted at end of turn"))
		for _, steer := range rejectedSteers {
			lines = append(lines, renderTruncatedMessage(steer, width, false))
		}
	}

	// Section 3: queued messages（P0 主路径）
	if len(queued) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, renderSectionHeader("Queued follow-up inputs"))
		for _, msg := range queued {
			lines = append(lines, renderTruncatedMessage(msg.Text, width, true))
		}
		// Alt+Up hint（仅当有 queued messages 时显示）
		lines = append(lines, renderEditHint())
	}

	return strings.Join(lines, "\n")
}

// renderSectionHeader 渲染段落标题（对标同类 CLI push_section_header）。
func renderSectionHeader(text string) string {
	// Faint = dim 效果（lipgloss v1.1.1 无 Dim 方法，Faint 是等价 ANSI 属性）
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Faint(true)
	return style.Render("• " + text)
}

// renderTruncatedMessage 渲染单条消息（带 "↳ " 前缀，超过 3 行显示 "…"）。
// italic=true 时斜体渲染（queued messages 段，对标同类 CLI）。
func renderTruncatedMessage(text string, width int, italic bool) string {
	// Faint = dim 效果（lipgloss v1.1.1 无 Dim 方法，Faint 是等价 ANSI 属性）
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Faint(true)
	if italic {
		style = style.Italic(true)
	}

	// 按宽度 wrap（P0 走 rune 切片降级路径，P1 可升级为 RunewidthMeasurer）
	wrapped := wrapTextSimple(text, width-4)
	wrappedLines := strings.Split(wrapped, "\n")

	var result []string
	for i, line := range wrappedLines {
		if i >= pendingPreviewLineLimit {
			ellipsis := style.Render("    …")
			result = append(result, ellipsis)
			break
		}
		prefix := "  ↳ "
		if i > 0 {
			prefix = "    "
		}
		result = append(result, style.Render(prefix+line))
	}
	return strings.Join(result, "\n")
}

// renderEditHint 渲染 Alt+Up 编辑提示（对标同类 CLI）。
func renderEditHint() string {
	// Faint = dim 效果（lipgloss v1.1.1 无 Dim 方法，Faint 是等价 ANSI 属性）
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Faint(true)
	return style.Render("    Alt+Up edit last queued message")
}

// renderNarrowPreview 窄终端降级渲染（仅显示条数）。
func renderNarrowPreview(count int) string {
	// Faint = dim 效果（lipgloss v1.1.1 无 Dim 方法，Faint 是等价 ANSI 属性）
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Faint(true)
	return style.Render(fmt.Sprintf("%d messages queued…", count))
}

// wrapTextSimple 简单文本 wrap（按 rune 切片，P1 可升级为 RunewidthMeasurer）。
func wrapTextSimple(text string, width int) string {
	if width <= 0 {
		return text
	}
	var result strings.Builder
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)
		for len(runes) > width {
			result.WriteString(string(runes[:width]))
			result.WriteString("\n")
			runes = runes[width:]
		}
		result.WriteString(string(runes))
		result.WriteString("\n")
	}
	return strings.TrimRight(result.String(), "\n")
}
