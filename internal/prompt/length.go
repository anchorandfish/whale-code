// Package prompt 提供 system prompt 统一构造引擎。
// 收口原 tui 包的 4 个分散构造函数，对标同类 CLI getSystemPrompt 单入口 + 同类 CLI load_project_instructions 单入口。
package prompt

import (
	"log"
	"unicode/utf8"
)

// TruncateBytes 按 utf8 rune 边界安全截断字符串到指定字节长度。
// 对标同类 CLI project_doc_max_bytes + data.truncate(max) + warn。
// 超长时截断 + 日志，不报错。maxBytes <= 0 时不截断。
// 预期输入 <100KB，当前逐 rune 回退循环可接受（P2-6）；若需优化可改为正向扫描 O(n)。
func TruncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	const marker = "\n[... truncated ...]"
	// P2-5：maxBytes 过小（不足以容纳 marker）时直接截断不追加 marker，避免截断后仍超 maxBytes
	appendMarker := maxBytes >= 32
	// 按 utf8 rune 边界安全截断
	limit := maxBytes
	if appendMarker {
		limit = maxBytes - len(marker)
		if limit <= 0 {
			limit = maxBytes
			appendMarker = false
		}
	}
	truncated := s
	for len(truncated) > limit {
		// 回退到最后一个完整 rune，避免切断多字节字符
		r, size := utf8.DecodeLastRuneInString(truncated)
		if r == utf8.RuneError && size == 1 {
			// 无效 utf8，直接截 byte
			truncated = truncated[:len(truncated)-1]
		} else {
			truncated = truncated[:len(truncated)-size]
		}
	}
	log.Printf("[prompt] content truncated from %d to %d bytes (maxBytes=%d)", len(s), len(truncated), maxBytes)
	if appendMarker {
		return truncated + marker
	}
	return truncated
}
