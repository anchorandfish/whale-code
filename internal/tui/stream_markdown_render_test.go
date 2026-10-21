// stream_markdown_render_test.go 验证流式过程中 Markdown 实时渲染（修复"输出 markdown 符号"问题）。
//
// 问题背景：renderStreamContent 早期版本直接 WriteString(currentText)，
// AI 输出诗/文章时 #、**、` 等符号原样显示给用户，体验差。
// 修复后流式过程也调用 renderTextContent 渲染 Markdown。
//
// 对标行为：同类 CLI code Ink 在流式过程中实时渲染 Markdown。

package tui

import (
	"regexp"
	"strings"
	"testing"
)

// stripANSIMD 去除 ANSI 颜色/样式转义，便于断言可见文本。
func stripANSIMD(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	return re.ReplaceAllString(s, "")
}

// TestStreamingRenderMarkdownLive 验证流式过程中 Markdown 被实时渲染。
// 对标行为：AI 输出 "# 标题\n\n**粗体**" 时，viewport 中不应原样出现 ## 或 ** 符号。
func TestStreamingRenderMarkdownLive(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.ready = true
	m.state = StateStreaming
	m.viewport.Width = 76
	m.viewport.Height = 20

	// 模拟 AI 流式输出 markdown 内容
	m.activeCell.AppendText("# 诗\n\n**春眠不觉晓**\n\n`code`")
	m.renderStreamContent()

	plain := stripANSIMD(m.viewport.View())

	// 标题文本应出现
	if !strings.Contains(plain, "诗") {
		t.Errorf("流式渲染后应包含标题文本 '诗'，got: %q", truncateStr(plain, 200))
	}
	// 粗体文本应出现
	if !strings.Contains(plain, "春眠不觉晓") {
		t.Errorf("流式渲染后应包含粗体文本 '春眠不觉晓'，got: %q", truncateStr(plain, 200))
	}
	// 行内代码文本应出现
	if !strings.Contains(plain, "code") {
		t.Errorf("流式渲染后应包含行内代码 'code'，got: %q", truncateStr(plain, 200))
	}
	// ** 粗体标记不应原样出现（应被 glamour 渲染为样式）
	if strings.Contains(plain, "**") {
		t.Errorf("流式渲染后不应原样出现 ** markdown 符号，got: %q", truncateStr(plain, 200))
	}
	// ` 代码反引号不应原样出现（应被 glamour 渲染为样式）
	// 注意：glamour 可能保留某些 markdown，但行内代码的 ` 不应原样出现
	if strings.Contains(plain, "`code`") {
		t.Errorf("流式渲染后不应原样出现 `code` markdown 符号，got: %q", truncateStr(plain, 200))
	}
}

// TestStreamingRenderMarkdownHeadingSymbol 验证 markdown 标题符号 # 不原样显示。
func TestStreamingRenderMarkdownHeadingSymbol(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.ready = true
	m.state = StateStreaming
	m.viewport.Width = 76
	m.viewport.Height = 20

	m.activeCell.AppendText("# 大标题\n\n正文内容")
	m.renderStreamContent()

	plain := stripANSIMD(m.viewport.View())

	// # 标题标记不应原样出现（glamour 会渲染为带样式的标题）
	// 注意：glamour 输出可能保留 # 在某些主题下，但通常不会原样出现在文本开头
	if strings.Contains(plain, "# 大标题") {
		t.Errorf("流式渲染后不应原样出现 '# 大标题'，got: %q", truncateStr(plain, 200))
	}
	// 大标题文本应出现
	if !strings.Contains(plain, "大标题") {
		t.Errorf("流式渲染后应包含 '大标题' 文本，got: %q", truncateStr(plain, 200))
	}
}

// TestStreamingRenderMarkdownPreservesPlainText 验证纯文本流式输出不受渲染影响。
// 防止渲染逻辑误伤不含 markdown 的纯文本（如 AI 写诗时无格式）。
func TestStreamingRenderMarkdownPreservesPlainText(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.ready = true
	m.state = StateStreaming
	m.viewport.Width = 76
	m.viewport.Height = 20

	plainText := "春眠不觉晓，处处闻啼鸟。夜来风雨声，花落知多少。"
	m.activeCell.AppendText(plainText)
	m.renderStreamContent()

	plain := stripANSIMD(m.viewport.View())
	if !strings.Contains(plain, "春眠不觉晓") {
		t.Errorf("纯文本流式输出应保留原文，got: %q", truncateStr(plain, 200))
	}
	if !strings.Contains(plain, "花落知多少") {
		t.Errorf("纯文本流式输出应保留原文末尾，got: %q", truncateStr(plain, 200))
	}
}
