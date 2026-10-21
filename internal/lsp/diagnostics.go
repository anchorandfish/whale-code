// Package lsp — / M18-06 诊断渲染到 TUI 辅助函数
//
// 提供 LSP Diagnostic 的格式化渲染辅助：
// - SeverityToLabel：将 LSP Severity 数字（1-4）转为可读标签（Error/Warning/Information/Hint）
// - FormatDiagnostic：将单条 Diagnostic 格式化为单行字符串（供 TUI 编辑器展示）
// - FormatDiagnostics：将多条 Diagnostic 批量格式化为多行字符串（供 TUI diagnostics 面板展示）
//
// 设计原则（02-审查报告 §2.5 过度设计检查）：
// - 仅提供格式化函数，不引入复杂渲染引擎
// - 不直接依赖 TUI 框架（TUI 调用方自行选择展示样式）
// - 消毒：消息内容经 SanitizeString 过滤控制字符与超长行（防 TUI 渲染异常）
//
// 对标 LSP 3.17 spec §"DiagnosticSeverity"
// [microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) @2023-08
//
// 对标 VS Code Problems 面板渲染风格
// [code.visualstudio.com/docs/editor/editingevolved#_errors-warnings](https://code.visualstudio.com/docs/editor/editingevolved#_errors-warnings) @2026-07-21
package lsp

import (
	"fmt"
	"strings"
	"unicode"
)

// LSP Severity 枚举值（与 LSP 3.17 spec §"DiagnosticSeverity" 一致）
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
	SeverityHint        = 4
)

// maxDiagnosticMessageLen 单条诊断消息渲染时的最大长度（防止超长消息破坏 TUI 布局）
const maxDiagnosticMessageLen = 200

// SeverityToLabel 将 LSP Severity 数字（1-4）转为可读标签。
//
// 输入 1=Error / 2=Warning / 3=Information / 4=Hint。
// 未识别的 Severity（0 或 >4）返回 "Unknown"。
// diagnostics.go @2026-07-21 SNAPSHOT: Severity 标签映射
func SeverityToLabel(severity int) string {
	switch severity {
	case SeverityError:
		return "Error"
	case SeverityWarning:
		return "Warning"
	case SeverityInformation:
		return "Information"
	case SeverityHint:
		return "Hint"
	default:
		return "Unknown"
	}
}

// SeverityToIcon 将 LSP Severity 数字转为单字符图标（供 TUI 紧凑展示）。
//
// 输入 1=E / 2=W / 3=I / 4=H。未识别返回 "?"。
// diagnostics.go @2026-07-21 SNAPSHOT: Severity 图标映射
func SeverityToIcon(severity int) string {
	switch severity {
	case SeverityError:
		return "E"
	case SeverityWarning:
		return "W"
	case SeverityInformation:
		return "I"
	case SeverityHint:
		return "H"
	default:
		return "?"
	}
}

// FormatDiagnostic 将单条 Diagnostic 格式化为单行字符串。
//
// 格式：`<icon> [<source>] <file>:<line>:<col>: <message>`
// source 为空时省略 `[<source>]` 段。
// 消息内容经 SanitizeString 过滤（截断超长 + 过滤控制字符）。
// diagnostics.go @2026-07-21 SNAPSHOT: 诊断格式化
func FormatDiagnostic(d Diagnostic, filePath string) string {
	icon := SeverityToIcon(d.Severity)
	msg := SanitizeString(d.Message, maxDiagnosticMessageLen)

	line := d.Range.Start.Line + 1 // LSP position 0-based → 用户可读 1-based
	col := d.Range.Start.Character + 1

	var sb strings.Builder
	sb.WriteString(icon)
	sb.WriteString(" ")
	if d.Source != "" {
		sb.WriteString("[")
		sb.WriteString(SanitizeString(d.Source, 30))
		sb.WriteString("] ")
	}
	if filePath != "" {
		sb.WriteString(filePath)
		sb.WriteString(":")
	}
	sb.WriteString(fmt.Sprintf("%d:%d", line, col))
	sb.WriteString(": ")
	sb.WriteString(msg)
	return sb.String()
}

// FormatDiagnostics 将多条 Diagnostic 批量格式化为多行字符串。
//
// 每行一条诊断（FormatDiagnostic 格式），行间以 `\n` 分隔。
// 输入 nil 或空切片返回空字符串。
// diagnostics.go @2026-07-21 SNAPSHOT: 诊断批量格式化
func FormatDiagnostics(diagnostics []Diagnostic, filePath string) string {
	if len(diagnostics) == 0 {
		return ""
	}
	lines := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		lines = append(lines, FormatDiagnostic(d, filePath))
	}
	return strings.Join(lines, "\n")
}

// SanitizeString 消毒字符串：过滤控制字符 + 截断超长。
//
// 用于渲染 LSP 返回的 message/source 等用户可见字段，防止 TUI 布局被破坏。
// 保留 \t（制表符）和 \n（换行符，渲染时由调用方处理）；其他控制字符替换为空格。
// 超过 maxLen 字符时截断并追加 "..."。
// diagnostics.go @2026-07-21 SNAPSHOT: 字符串消毒
func SanitizeString(s string, maxLen int) string {
	if s == "" {
		return ""
	}
	// 过滤控制字符
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n':
			sb.WriteRune(r)
		case unicode.IsControl(r):
			sb.WriteRune(' ') // 替换为空格
		default:
			sb.WriteRune(r)
		}
	}
	cleaned := sb.String()
	// 截断超长
	if maxLen > 0 && len(cleaned) > maxLen {
		cleaned = cleaned[:maxLen] + "..."
	}
	return cleaned
}
