package tui

import "strings"

// exit_transcript.go —— 退出后把会话尾部回印到**主屏**（止血）。
//
// 现象：alt-screen（终端备用屏）模式下，终端在程序退出时会把屏幕恢复成**启动前**的样子，
// 用户看到"整屏消失、历史没了"（内容其实还在 `~/.whale/sessions/*.jsonl`）。
//
// 止血：退出后在主屏回印会话尾部若干行 ⇒ 内容落进终端原生 scrollback，可继续上翻。
//
// ★ 这是**止血，不是修根**。修根是"历史归属"重构 —— 把已提交的消息交给终端 scrollback、
// 视口只画活动区；本函数不改变运行期的历史归属（运行期仍由视口掌管，滚轮仍翻不到历史）。
// 依据与实测见 `大修方案_TUI历史归属与回执可见性_2026-09-23.md` 与
// `内部工作回执` 的闸门结论。

// ExitTranscriptTail 返回回印用的会话尾部：**最后** maxLines 行（保持原顺序）。
//
// 取值来源是 `viewportContent` —— 它与视口内容同源（见 setViewportContent），
// 因此回印出来的就是用户退出前看到的那份内容，不会再造一份"历史"（避免同语义第二份实现）。
//
// 边界：
// - maxLines <= 0 ⇒ 返回 ""（调用方据此可整体关掉回印）；
// - 内容为空/仅空白 ⇒ 返回 ""（不打印空壳分隔线）；
// - 尾部空行先剔除 —— 视口内容常以空行收尾，直接取尾部会顶出一行空白；
// - 行数不足 maxLines ⇒ 全部返回。
func (m Model) ExitTranscriptTail(maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	if strings.TrimSpace(m.viewportContent) == "" {
		return ""
	}
	lines := strings.Split(m.viewportContent, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
