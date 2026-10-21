package tui

// /archive —— 归档只读查看与导出（2026-09-07，archive-on-compact 配套）。
//
// 设计契约：本命令是纯只读视图，绝不把归档消息放回主文件/模型上下文
//（真还原是另一个形态，见 archive.go 头注释的契约边界）。
// - /archive 列出压缩记录（次数/时间/消息数）
// - /archive view N 查看第 N 段记录的原始消息（单条截断，全文用 export）
// - /archive export [p] 导出全部归档为 markdown（复用 export.FormatMarkdown，
// 含默认脱敏），默认路径 ./<会话名>.archive.md
//
// 批 3：屏幕输出文案已双语化（英文主行 + 缩进中文辅行，形态 SSOT = util.Bi）。
// ★ 例外（有意保留中文单侧）：**导出文件正文**（`# 会话归档导出：` / `- 导出时间：` /
// `## 压缩记录 #…`）属"导出文件内容"，口径**未拍板**（报告 §5.4）⇒ 不在本批范围，
// 门 `b3_command_output_bilingual_test.go` 按内容显式豁免这三条。

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	exportpkg "github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/session"
)

// archiveViewTruncate view 模式单条消息的显示截断长度（rune）。
const archiveViewTruncate = 500

func (m *Model) handleArchive(args string) string {
	return handleArchiveViewShared(m.sessionService.Store(), m.sessionService.SessionName(), args)
}

func (c *runnerCore) handleArchive(args string) string {
	return handleArchiveViewShared(c.Store, c.SessionName, args)
}

// handleArchiveViewShared TUI 与 plain 共用的 /archive 实现（行为一致性
// 由 TestPlainRunnerHandleArchiveViewExport 锁定）。
func handleArchiveViewShared(store *session.Store, sessionName, args string) string {
	if store == nil {
		return bi("📦 Archive: session store is not initialized", "📦 归档: session store 未初始化")
	}
	if sessionName == "" {
		return bi("📦 Archive: the current session has not been saved yet (no archive)",
			"📦 归档: 当前会话尚未保存（无归档）")
	}
	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = parts[0]
	}
	switch sub {
	case "", "list":
		return archiveListSummary(store, sessionName)
	case "view":
		n := 1
		if len(parts) > 1 {
			parsed, err := strconv.Atoi(parts[1])
			if err != nil || parsed < 1 {
				return biSame("📦 Archive: invalid record number %q (usage: /archive view N, N is a positive integer)",
					"📦 归档: 无效的记录号 %q（用法：/archive view N，N 为正整数）", parts[1])
			}
			n = parsed
		}
		return archiveViewRecord(store, sessionName, n)
	case "export":
		path := ""
		if len(parts) > 1 {
			path = parts[1]
		}
		return archiveExport(store, sessionName, path)
	default:
		return bi("📦 Archive usage:\n"+
			"  /archive            list compaction records\n"+
			"  /archive view N     show the raw messages of record N\n"+
			"  /archive export [p] export all archives as markdown",
			"📦 归档用法:\n"+
				"  /archive            列出压缩记录\n"+
				"  /archive view N     查看第 N 段记录的原始消息\n"+
				"  /archive export [p] 导出全部归档为 markdown")
	}
}

// archiveListSummary 列出全部压缩记录。
func archiveListSummary(store *session.Store, sessionName string) string {
	recs, err := store.ReadArchive(sessionName)
	if err != nil {
		return biSame("📦 Archive read failed: %v", "📦 归档读取失败: %v", err)
	}
	if len(recs) == 0 {
		return bi("📦 Archive: no archive file (this session has not been compacted, or the archive was purged)",
			"📦 归档: 无归档文件（本会话尚未发生压缩或已清理）")
	}
	total := 0
	var b strings.Builder
	// 头部用「英文主行 + 缩进中文辅行」；逐条记录行只出英文主行（与 /session list 同口径：
	// 列表逐行不重复堆中文，标题/表头才带辅行）。
	b.WriteString(biSame("📦 Compaction archive (%s, %d records)",
		"📦 压缩归档（%s，共 %d 段记录）", sessionName, len(recs)))
	b.WriteString("\n")
	for i, rec := range recs {
		ts := ""
		if rec.Header.Timestamp > 0 {
			ts = time.Unix(rec.Header.Timestamp, 0).Format("01-02 15:04:05")
		}
		fmt.Fprintf(&b, "  #%d %s compaction #%d, %d messages\n",
			i+1, ts, rec.Header.CompactionCount, len(rec.Messages))
		total += len(rec.Messages)
	}
	b.WriteString(biSame("Total %d archived messages. Use /archive view N to inspect, /archive export [path] to export",
		"合计 %d 条归档消息。/archive view N 查看，/archive export [路径] 导出", total))
	return b.String()
}

// archiveViewRecord 查看第 n 段（1-based）记录的原始消息。
func archiveViewRecord(store *session.Store, sessionName string, n int) string {
	recs, err := store.ReadArchive(sessionName)
	if err != nil {
		return biSame("📦 Archive read failed: %v", "📦 归档读取失败: %v", err)
	}
	if len(recs) == 0 {
		return bi("📦 Archive: no archive file (this session has not been compacted, or the archive was purged)",
			"📦 归档: 无归档文件（本会话尚未发生压缩或已清理）")
	}
	if n > len(recs) {
		return biSame("📦 Archive: invalid record number %d (valid range: 1-%d)",
			"📦 归档: 无效的记录号 %d（范围 1-%d）", n, len(recs))
	}
	rec := recs[n-1]
	ts := ""
	if rec.Header.Timestamp > 0 {
		ts = time.Unix(rec.Header.Timestamp, 0).Format("01-02 15:04:05")
	}
	var b strings.Builder
	b.WriteString(biSame("📦 Archive record #%d/%d (%s, compaction #%d, %d messages)",
		"📦 归档记录 #%d/%d（%s，第 %d 次压缩，%d 条消息）",
		n, len(recs), ts, rec.Header.CompactionCount, len(rec.Messages)))
	b.WriteString("\n")
	for i, msg := range rec.Messages {
		text := []rune(msg.TextContent())
		if len(text) == 0 {
			// 对抗式审查 3.6 修复：tool/多模态消息无 text 块时给占位提示，
			// 避免 "[N] tool: " 空行的静默内容省略。
			fmt.Fprintf(&b, "  [%d] %s: [no text content (tool/multimodal blocks; see /archive export for the full text)]\n", i+1, msg.Role)
			continue
		}
		truncated := false
		if len(text) > archiveViewTruncate {
			text = text[:archiveViewTruncate]
			truncated = true
		}
		fmt.Fprintf(&b, "  [%d] %s: %s", i+1, msg.Role, string(text))
		if truncated {
			b.WriteString(" …(truncated; see /archive export for the full text)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// archiveExport 全部归档导出为 markdown（复用 export.FormatMarkdown，含脱敏）。
func archiveExport(store *session.Store, sessionName, path string) string {
	recs, err := store.ReadArchive(sessionName)
	if err != nil {
		return biSame("📦 Archive export failed: %v", "📦 归档导出失败: %v", err)
	}
	if len(recs) == 0 {
		return bi("📦 Archive: no archive file (this session has not been compacted, or the archive was purged)",
			"📦 归档: 无归档文件（本会话尚未发生压缩或已清理）")
	}
	if path == "" {
		// 对抗式审查 4.3 P1 修复：默认路径不得用未消毒的会话名拼 CWD 相对路径
		//（会话名可含 / 导致写失败或逃逸、中文正常名会污染工作区）。对齐 /export
		// 惯例：落 ~/.whale/exports/<清洗名>.archive.md（store.Dir 的父目录即 ~/.whale）。
		exportsDir := filepath.Join(filepath.Dir(store.Dir()), "exports")
		if err := os.MkdirAll(exportsDir, 0o755); err != nil {
			return biSame("📦 Archive export failed: cannot create the export directory: %v",
				"📦 归档导出失败: 无法创建导出目录: %v", err)
		}
		path = filepath.Join(exportsDir, archiveSafeFilename(sessionName)+".archive.md")
	}
	redactor := &exportpkg.DefaultRedactor{}
	var b strings.Builder
	// 以下三条是**导出文件正文**（不是屏幕文案）⇒ 口径未拍板，本批有意不改（门内显式豁免）。
	fmt.Fprintf(&b, "# 会话归档导出：%s\n\n", sessionName)
	fmt.Fprintf(&b, "- 导出时间：%s\n", time.Now().Format("2006-01-02 15:04:05"))
	redactedTotal := 0
	msgTotal := 0
	for i, rec := range recs {
		msgTotal += len(rec.Messages)
		ts := ""
		if rec.Header.Timestamp > 0 {
			ts = time.Unix(rec.Header.Timestamp, 0).Format("2006-01-02 15:04:05")
		}
		// 嵌入 FormatMarkdown 输出（自带 H1 与消息分节；多 H1 在 markdown 中合法，
		// 换取脱敏逻辑单点复用，不在此重复实现消息渲染）。
		body, redacted := exportpkg.FormatMarkdown(rec.Messages, redactor)
		redactedTotal += redacted
		fmt.Fprintf(&b, "\n## 压缩记录 #%d（%s，第 %d 次压缩，%d 条消息）\n\n",
			i+1, ts, rec.Header.CompactionCount, len(rec.Messages))
		b.WriteString(body)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return biSame("📦 Archive export failed: %v", "📦 归档导出失败: %v", err)
	}
	return biSame("📦 Exported %d records / %d messages to %s (%d redactions)",
		"📦 已导出 %d 段记录 / %d 条消息到 %s（脱敏 %d 处）",
		len(recs), msgTotal, path, redactedTotal)
}

// archiveSafeFilename 清洗会话名为安全文件名片段：剔除路径敌对字符
// （保留 CJK——中文会话名是常态），去首尾点号防 .. 与隐藏文件，限长 80 rune。
func archiveSafeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	)
	cleaned := strings.Trim(strings.TrimSpace(replacer.Replace(name)), ".")
	if cleaned == "" {
		cleaned = "session"
	}
	if r := []rune(cleaned); len(r) > 80 {
		cleaned = string(r[:80])
	}
	return cleaned
}
