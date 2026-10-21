// Package export — M23-04 HTML 导出测试（SNAPSHOT）
//
// 测试覆盖：
// - FormatHTML 基本 HTML 结构
// - HTML 转义（防 XSS）
// - 脱敏器调用
// - 不同 role 渲染（user/assistant/system/tool）
// - tool_use / tool_result 折叠块
// - thinking 折叠块
// - 选项控制（IncludeStyle/IncludeMeta）
package export

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// fakeRedactor 是测试用脱敏器，将 "SECRET" 替换为 "[REDACTED]"。
type fakeRedactor struct{}

// Scrub 实现 Redactor 接口
func (f *fakeRedactor) Scrub(text string) (string, int) {
	if !strings.Contains(text, "SECRET") {
		return text, 0
	}
	return strings.ReplaceAll(text, "SECRET", "[REDACTED]"), 1
}

// TestFormatHTML_BasicStructure 验证基本 HTML 结构
func TestFormatHTML_BasicStructure(t *testing.T) {
	msgs := []session.Message{
		{
			Role:      session.RoleUser,
			Blocks:    []session.ContentBlock{{Type: "text", Text: "hello"}},
			Timestamp: 1000,
		},
	}

	html, redacted := FormatHTML(msgs, nil)
	if redacted != 0 {
		t.Errorf("redacted = %d, want 0", redacted)
	}
	if !strings.HasPrefix(html, "<!DOCTYPE html>") {
		t.Error("missing DOCTYPE")
	}
	if !strings.Contains(html, "</html>") {
		t.Error("missing </html>")
	}
	if !strings.Contains(html, "hello") {
		t.Error("missing user text")
	}
}

// TestFormatHTML_XSSEscape 验证 HTML 转义防 XSS
func TestFormatHTML_XSSEscape(t *testing.T) {
	msgs := []session.Message{
		{
			Role: session.RoleUser,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: `<script>alert("xss")</script>`},
			},
			Timestamp: 1000,
		},
	}

	html, _ := FormatHTML(msgs, nil)
	// 原始 <script> 不应出现在 body 内容中（title 中也没有）
	// 注意：<style> 块可能含 < 转义，所以只检查内容
	if strings.Contains(html, "<script>alert") {
		t.Errorf("HTML contains unescaped <script>: %s", html)
	}
	// 应包含转义后的 &lt;script&gt;
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("HTML missing escaped &lt;script&gt;")
	}
}

// TestFormatHTML_RedactorCalled 验证脱敏器被调用
func TestFormatHTML_RedactorCalled(t *testing.T) {
	msgs := []session.Message{
		{
			Role: session.RoleUser,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: "my SECRET value"},
			},
			Timestamp: 1000,
		},
	}

	html, redacted := FormatHTML(msgs, &fakeRedactor{})
	if redacted != 1 {
		t.Errorf("redacted = %d, want 1", redacted)
	}
	if strings.Contains(html, "SECRET") {
		t.Errorf("HTML still contains SECRET")
	}
	if !strings.Contains(html, "[REDACTED]") {
		t.Errorf("HTML missing [REDACTED]")
	}
}

// TestFormatHTML_AllRoles 验证不同 role 都被渲染
func TestFormatHTML_AllRoles(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "u"}}, Timestamp: 1000},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "a"}}, Timestamp: 2000},
		{Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "s"}}, Timestamp: 3000},
	}

	html, _ := FormatHTML(msgs, nil)
	if !strings.Contains(html, "role-user") {
		t.Error("missing role-user")
	}
	if !strings.Contains(html, "role-assistant") {
		t.Error("missing role-assistant")
	}
	if !strings.Contains(html, "role-system") {
		t.Error("missing role-system")
	}
}

// TestFormatHTML_ToolUseBlock 验证 tool_use 渲染为折叠块
func TestFormatHTML_ToolUseBlock(t *testing.T) {
	msgs := []session.Message{
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "tool_use", Name: "bash", Input: "ls -la"},
			},
			Timestamp: 1000,
		},
	}

	html, _ := FormatHTML(msgs, nil)
	if !strings.Contains(html, "tool-use-block") {
		t.Error("missing tool-use-block class")
	}
	if !strings.Contains(html, "bash") {
		t.Error("missing tool name")
	}
	if !strings.Contains(html, "ls -la") {
		t.Error("missing tool input")
	}
	if !strings.Contains(html, "<details") {
		t.Error("tool_use should be in <details>")
	}
}

// TestFormatHTML_ToolResultBlock 验证 tool_result 渲染为折叠块
func TestFormatHTML_ToolResultBlock(t *testing.T) {
	msgs := []session.Message{
		{
			Role: session.RoleTool,
			Blocks: []session.ContentBlock{
				{Type: "tool_result", ToolName: "bash", Output: "file1\nfile2"},
			},
			Timestamp: 1000,
		},
	}

	html, _ := FormatHTML(msgs, nil)
	if !strings.Contains(html, "tool-result-block") {
		t.Error("missing tool-result-block class")
	}
	if !strings.Contains(html, "file1") {
		t.Error("missing tool output")
	}
}

// TestFormatHTML_ThinkingBlock 验证 thinking 渲染为折叠块
func TestFormatHTML_ThinkingBlock(t *testing.T) {
	msgs := []session.Message{
		{
			Role: session.RoleAssistant,
			Blocks: []session.ContentBlock{
				{Type: "thinking", Thinking: "let me think"},
			},
			Timestamp: 1000,
		},
	}

	html, _ := FormatHTML(msgs, nil)
	if !strings.Contains(html, "thinking-block") {
		t.Error("missing thinking-block class")
	}
	if !strings.Contains(html, "let me think") {
		t.Error("missing thinking content")
	}
}

// TestFormatHTML_IncludeStyleFalse 验证可关闭内联样式
func TestFormatHTML_IncludeStyleFalse(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}, Timestamp: 1000},
	}

	opt := HTMLOption{IncludeStyle: false, IncludeMeta: false}
	html, _ := FormatHTMLWithOption(msgs, nil, opt)
	if strings.Contains(html, "<style>") {
		t.Error("should not include <style> when IncludeStyle=false")
	}
}

// TestFormatHTML_IncludeMetaFalse 验证可关闭 meta 信息
func TestFormatHTML_IncludeMetaFalse(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}, Timestamp: 1000},
	}

	opt := DefaultHTMLOption()
	opt.IncludeMeta = false
	html, _ := FormatHTMLWithOption(msgs, nil, opt)
	if strings.Contains(html, `name="generator"`) {
		t.Error("should not include generator meta when IncludeMeta=false")
	}
}

// TestFormatHTML_EmptyMessages 空消息列表
func TestFormatHTML_EmptyMessages(t *testing.T) {
	html, redacted := FormatHTML(nil, nil)
	if redacted != 0 {
		t.Errorf("redacted = %d, want 0", redacted)
	}
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("missing DOCTYPE for empty messages")
	}
	if !strings.Contains(html, "消息数: 0") {
		t.Error("should show 0 messages")
	}
}

// TestFormatHTML_CustomTitle 自定义标题
func TestFormatHTML_CustomTitle(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}, Timestamp: 1000},
	}

	opt := HTMLOption{Title: "My Custom Title", IncludeStyle: false, IncludeMeta: false}
	html, _ := FormatHTMLWithOption(msgs, nil, opt)
	if !strings.Contains(html, "My Custom Title") {
		t.Error("missing custom title")
	}
}
