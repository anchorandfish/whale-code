package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/anchorandfish/whale-code/internal/diff"
	exportpkg "github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/anchorandfish/whale-code/internal/session"
)

// formatCompactBoundary 渲染压缩分界标记（切口③，UI-only）。
//
// 英文主行 + 中文缩进辅助（本仓惯例）。它标记「这条分界以上的历史内容已被
// 压缩为摘要，模型上下文不再包含原始内容」，让用户明确知道模型上下文到哪为止。
// 分界只进显示侧，永不写入模型侧 session（不变式 I5）。
func formatCompactBoundary() string {
	var sb strings.Builder
	sb.WriteString(separatorStyle.Render("━━━ Compacted 已压缩 ━━━"))
	sb.WriteString("\n")
	sb.WriteString(timestampStyle.Render("  Messages above were summarized; the model no longer sees them."))
	sb.WriteString("\n")
	sb.WriteString(timestampStyle.Render("  以上历史已压缩为摘要，模型不再看到这些原始内容。"))
	return sb.String()
}

// formatUserMessage keeps user and assistant turns structurally consistent:
// the speaker label occupies its own line and the message body starts below it.
func formatUserMessage(text string, timestamp int64) string {
	var sb strings.Builder
	sb.WriteString(userStyle.Render("👤 You:"))
	sb.WriteString("\n")
	sb.WriteString(text)
	if timestamp > 0 {
		sb.WriteString("\n")
		sb.WriteString(timestampStyle.Render(time.UnixMilli(timestamp).Format("2006-01-02 15:04:05")))
	}
	return sb.String()
}

// formatAssistantMessage 格式化助手消息用于显示（*Model 方法）。
// timestamp 为消息创建时间（UnixMilli），<=0 时不显示时间戳。
func (m *Model) formatAssistantMessage(blocks []session.ContentBlock, timestamp int64) string {
	text, _ := m.formatAssistantMessageSource(blocks, timestamp)
	return text
}

// formatAssistantMessageSource 是 formatAssistantMessage 的带 source 版本
// （宽度口径：contentWrapWidth，与 wrapViewportContent 折行宽度一致）。
func (m *Model) formatAssistantMessageSource(blocks []session.ContentBlock, timestamp int64) (string, []lineSource) {
	return formatAssistantMessageWithThinkingSource(blocks, "", timestamp, m.contentWrapWidth())
}

// formatAssistantMessageWithThinking 格式化带 thinking 内容的助手消息。
// timestamp 为消息创建时间（UnixMilli），<=0 时不显示时间戳。
// contentWidth 为内容折行宽度（m.contentWrapWidth()），思考边框按此宽度绘制，
// 保证与 wrapViewportContent 的硬折行宽度一致，边框不被二次折行。
// 新增 timestamp 参数，在分隔线前渲染 dim 灰色时间戳（YYYY-MM-DD HH:MM:SS）。
// v1.2：新增宽度参数，用 renderThinkingBlock 统一思考边框（顶底等宽+右侧闭合）。
func formatAssistantMessageWithThinking(blocks []session.ContentBlock, thinking string, timestamp int64, contentWidth int) string {
	text, _ := formatAssistantMessageWithThinkingSource(blocks, thinking, timestamp, contentWidth)
	return text
}

// formatAssistantMessageWithThinkingSource 是 formatAssistantMessageWithThinking 的
// **带 source map** 版本：除渲染文本外，还登记本条目内**每一个省略行**的真值
// （思考折叠预览 / 命令截断行），供拖选复制回取原文（见 transcript_source.go）。
//
// 谁做的省略，谁登记 —— 两处省略都在本函数内就地可得完整原文（思考全文 / 完整命令行），
// 因此不需要事后按屏幕字面反查。
func formatAssistantMessageWithThinkingSource(blocks []session.ContentBlock, thinking string, timestamp int64, contentWidth int) (string, []lineSource) {
	if !hasVisibleAssistantContent(blocks, thinking) {
		return "", nil
	}
	if !hasAssistantNarrativeContent(blocks, thinking) {
		var toolLines []string
		overrides := map[int]string{}
		line := 0
		for _, block := range blocks {
			if block.Type != "tool_use" {
				continue
			}
			rendered, full := renderToolUseCompactSource(block.Name, block.Input)
			if rendered == "" {
				continue
			}
			if full != rendered {
				overrides[line] = toolStyle.Render(full)
			}
			toolLines = append(toolLines, rendered)
			line++
		}
		text := toolStyle.Render(strings.Join(toolLines, "\n"))
		return text, lineSourcesFromOverrides(lineCountOf(text), overrides)
	}

	var sb strings.Builder
	overrides := map[int]string{}
	line := 0
	emit := func(s string) {
		sb.WriteString(s)
		line += strings.Count(s, "\n")
	}

	emit(assistantStyle.Render("🐋 Whale: "))

	// ★ 保留思考：门槛由**内容**决定，不再由客户端开关决定。
	// 原签名带 `showThinking bool`，调用方传的是 sessionService.ThinkingEnabled()，
	// 该值恒为 false ⇒ 流式期间看得到思考、回答一结束思考块就消失。
	// 现在「有思考内容就渲染」是不变式，调用方无从把它关掉（少一个可写错的参数）。
	//
	// 排版收敛（2026-09-25 用户拍板）：定稿消息的思考**默认折叠**为 dim 单行预览
	// —— 一句话的思想占一个全宽边框是本仓最大的排版噪音源（对标 同类 CLI /
	// 同类 CLI 的折叠态）。流式期间的完整框仍由 renderStreamContent（stream.go 内联）
	// 绘制；完整思考内容永远在会话存档 ~/.whale/sessions/*.jsonl 可查。
	// 「🧠 Thinking」标识保留 —— TestFinalizeKeepsThinkingBlock 契约（思考不得
	// 在 finalize 后凭空消失）继续成立，只是形态从框变行。
	if strings.TrimSpace(thinking) != "" {
		emit("\n")
		preview, collapsed := collapsedThinkingSource(thinking, collapsedThinkingPreviewCols)
		// 预览恒为单行 ⇒ 省略行 = 当前行号（emit 前后不变）。
		previewLine := line
		emit(thinkingStyle.Render("🧠 Thinking · " + preview))
		emit("\n")
		// 登记：该行真值 = 完整思考（同形但未截断）。 的一半（另一半是命令截断）。
		if collapsed != preview {
			overrides[previewLine] = thinkingStyle.Render("🧠 Thinking · " + collapsed)
		}
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Plain {
				// 程序生成的纯文本**不走 Markdown**。
				// 理由见 session.ContentBlock.Plain —— Markdown 会把
				// `util.Bi` 的「英文行 \n 三个空格+中文行」折成同一行并剥掉缩进。
				emit(renderPlainText(b.Text))
				continue
			}
			// 把真实内容宽度喂给 glamour（否则表格按内容自然宽度铺开、被硬切错位）。
			emit(renderTextContentWidth(b.Text, contentWidth))
		case "tool_use":
			// 紧凑渲染（对标同类 CLI AssistantToolUseMessage：符号+工具名+人可读参数）。
			// AskUser 返回空字符串 → 跳过（由 renderChoiceUI 交互 UI 接管，不暴露原始 JSON）。
			rendered, full := renderToolUseCompactSource(b.Name, b.Input)
			if rendered == "" {
				continue
			}
			emit("\n")
			toolLine := line
			emit(toolStyle.Render(rendered))
			// 登记：命令行被 160 rune 截断时，该行真值 = 完整命令行（的另一半）。
			if full != rendered {
				overrides[toolLine] = toolStyle.Render(full)
			}
		}
	}

	emit("\n")
	// 时间戳显示在分隔线之前，dim 灰色不突出
	if timestamp > 0 {
		emit(timestampStyle.Render(time.UnixMilli(timestamp).Format("2006-01-02 15:04:05")))
		emit("\n")
	}
	// 排版收敛：横线宽度自适应 contentWidth。固定 45 字面量在
	// contentWrapWidth < 45 的窄视口下会被 wrapViewportContent 硬折成多段
	// （判据 TestTurnLayout_SeparatorAdaptsToWrapWidth）。
	sepWidth := assistantSeparatorWidth
	if contentWidth > 0 && contentWidth < sepWidth {
		sepWidth = contentWidth
	}
	emit(separatorStyle.Render(strings.Repeat("─", sepWidth)))

	text := sb.String()
	return text, lineSourcesFromOverrides(lineCountOf(text), overrides)
}

// assistantSeparatorWidth 是 assistant 消息尾部横线的默认（宽视口）长度。
// 窄视口下按 contentWidth 收缩（见 formatAssistantMessageWithThinking）。
const assistantSeparatorWidth = 45

// collapsedThinkingPreviewCols 是定稿消息折叠思考预览的显示宽度预算（列）。
// 超出部分以 … 收尾；完整内容在会话存档可查。
const collapsedThinkingPreviewCols = 60

// collapsedThinkingPreview 是定稿消息折叠思考预览的**唯一构造点**。
//
// 第 2 件（2026-10-07 真机 T-14a）：原实现只把换行换成空格再截断，
// 于是思考里的 markdown 代码围栏 ``` 会原样成为可见噪声
// （屏幕上是 `…expected contents: ``` sid…`）。预览是给人看的**一句话摘要**，
// 不是 markdown 渲染面 ⇒ 先剥掉围栏行（保留围栏内的自然语言），再折叠空白，再按列截断。
func collapsedThinkingPreview(thinking string, cols int) string {
	preview, _ := collapsedThinkingSource(thinking, cols)
	return preview
}

// collapsedThinkingSource 返回折叠思考预览的「显示字面」与「完整单行原文」。
//
// 拆出来是为了让 formatAssistantMessageWithThinkingSource 就地登记省略行的真值
// （谁省略谁登记）：显示字面被 ansi.Truncate 截到 cols 列，完整形态是同一个单行串
// （未截断），二者同出一次 stripCodeFences + collapseToSingleLine，不会分叉。
func collapsedThinkingSource(thinking string, cols int) (preview, full string) {
	line := collapseToSingleLine(stripCodeFences(thinking))
	return ansi.Truncate(line, cols, "…"), line
}

// stripCodeFences 去掉 markdown 代码围栏标记（连续 ≥3 个 ` 或 ~），保留其余文本。
//
// ★ 关键是**行内**也要剥：真机形态是 `expected contents: ```json`（围栏紧贴在句尾），
// 只按「整行以 ``` 起首」判会漏掉它（首版就是这么漏的）。故按字符扫描，凡遇 ≥3 连
// 反引号/波浪号即整段丢弃；单个 ` 是行内代码标记，保留。
func stripCodeFences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == '`' || c == '~' {
			j := i
			for j < len(s) && s[j] == c {
				j++
			}
			if j-i >= 3 {
				i = j // 丢弃整个围栏标记段
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// collapseToSingleLine 把可能含换行/制表/连续空白的字符串压成**单行短语**。
//
// 第 3 件的共同底座：工具调用头是「一行头」，任何参数预览都不该自带换行。
func collapseToSingleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// isHorizontalRuleLine 判断一行（已去 ANSI、去空白）是否为纯横线分隔行。
// 渲染层用它识别「上一条消息已自带分隔线」，join 不再叠加单 ─（防连续双线）；
// 判定谓词单点收口在此，测试与渲染共用同一语义。
func isHorizontalRuleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	// ★ 用 rune 计数：`─`（U+2500）单字符占 3 字节，len() 会把单个 join ─ 误判成横线行。
	return utf8.RuneCountInString(trimmed) >= 2 && strings.Trim(trimmed, "─") == ""
}

// turnMarkDone / turnMarkInterrupted 是轮末结束标志的**唯一符号源**。
//
// 正常结束用 ✓、被用户中断用 ⏹ —— 只有符号不同，其余形态（单行 + 完整时刻）一致，
// 保证"每一轮恰好一个结束标志"。判据用同一形态的正则断言（turn_done / turn_interrupted）。
const (
	turnMarkDone        = "━━ ✓ "
	turnMarkInterrupted = "━━ ⏹ "
)

// formatTurnChangesCard 渲染轮末折叠卡片的**摘要行**。
//
// 形态决策（连同理由记录在案，便于一次改回）：
// - **逐文件清单 + 一行合计**（2026-10-08 用户二次拍板：「没有显示改了什么文件，改了多少」）——
// 初版只打一行合计（`▸ N files changed +X -Y`），用户真机看后指出"改了哪些文件、各改多少"
// 才是本轮汇报的本体 ⇒ 卡片扩展为多行：上方每文件一行（` 路径 +X -Y`，相对工作区），
// **合计行保持在最后一行**（紧邻结束标志，形态细则①的位置契约不变）。
// 展开体仍走 `/diff`（看逐行正文，不只看统计）。
// - **单行合计的由来（保留）**：用户 2026-10-07 刚把结束标志从两行收敛成一行
// （去掉轮次号与中英文重复，明确说"太啰嗦"）⇒ 合计行不再叠中文辅助行，免得把同一处
// 又变啰嗦。中文辅助行若要加，只需把合计行改成 `util.Bi(en, zh)`（一行改动）。
// - **语言中性**：正文只有符号 + 数字 + 路径 + 命令名（`/diff` 是要用户照着敲的字面量，
// 不是散文），因此不存在"英文主行/中文主行"的取舍。
// - **指路复用既有折叠口径**：`(/diff)` 与工具结果折叠行的"括号里给命令"同一形态
// （`tools.foldHintText`），用户不必学第二套展开方式。
// - **零噪音**：`changes` 为 nil（取不到事实）或本轮无变更 ⇒ 返回空串，调用方整行不输出。
//
// ★ 卡片只有统计、**不隐藏 diff 正文**（正文在 `/diff` 里以完整消息打印）⇒ 天然没有
// "屏幕上有省略行、拖选复制拿不到"的折叠陷阱（的教训在此形态下不适用）。
func formatTurnChangesCard(changes *diff.TurnDiff, workDir string) string {
	if changes == nil || changes.Stats.FilesChanged == 0 {
		return ""
	}
	var sb strings.Builder
	// 逐文件清单行：与 `/diff --stat` 同一形态（` 路径 +X -Y`）、同一统计源
	// （`diff.CountChanges`，与合计行同出 `NewTurnDiff` 的口径 ⇒ 同源）。
	for _, f := range changes.Files {
		added, removed := diff.CountChanges(f)
		sb.WriteString(fmt.Sprintf("  %s  +%d -%d\n", displayPath(f.Path, workDir), added, removed))
	}
	sb.WriteString(fmt.Sprintf("▸ %d files changed  %s  (/diff)",
		changes.Stats.FilesChanged, diff.RenderStats(changes.Stats)))
	return sb.String()
}

// formatTurnCompleteBoundary 渲染轮末结束标志。
//
// A-3改造：**入参由 `time.Now()` 改为显式时刻** ——
// 标记的时间必须来自"回合结束那一瞬"（m.turn.closedAt / closeTurn 的 at），而不是渲染时刻。
// 否则每次重绘都会跳字，且判据无法固定时间戳。
//
// 2026-10-07（用户拍板）契约变更：由「两行 + 轮次号 + 中英文各一遍」
// （`━━ ✓ Turn #N complete · HH:MM:SS ━━` ＋ ` 第 N 轮回答已结束`）收敛为**单行**：
// - 去掉轮次号（用户反馈"第几轮没必要出现"）；
// - 去掉中英文重复（用户反馈"太啰嗦"）；
// - 时刻带**完整年月日**（`2006-01-02 15:04:05`），便于跨天回看定位；
// - 中断轮也留标志（符号 ⏹），替掉原先单独打印的 `Stopped │ … │ interrupted` 行
// —— 于是"每一轮恰好一个结束标志"成为统一事实（判据 TestTurnBoundary_InterruptedTurnLeavesMark）。
//
// 本函数只负责**渲染**；它不被 addMessage 调用 —— 边界不占消息身份。
// 调用点：Model.displayMessages()（渲染侧从 m.turn.marks 逐条投影）。
func formatTurnCompleteBoundary(at time.Time, interrupted bool) string {
	prefix := turnMarkDone
	if interrupted {
		prefix = turnMarkInterrupted
	}
	return separatorStyle.Render(prefix + at.Format("2006-01-02 15:04:05") + " ━━")
}

// displayMessages / displayEntries：见 transcript_source.go。
//
// 2026-10-06 起边界标记由 m.turn.marks 承担、按 after 锚点插回原位；
// 2026-10-08（批 C）起显示条目升格为 displayEntry（文本 + 逐行 source map），
// displayMessages 仅作"只要文本"的薄投影。
//
// thinkingModeLabel 是「Thinking 处于哪一档」的**唯一文案源**。
//
// 状态面板（render_extras.go）与 /think、/thinkback 回执共用它，满足 ：
// 同一事实跨入口必须同源，不得各写各的措辞。
// 三态：nil=未指定（交给服务端默认）· true=显式开 · false=显式关。
//
// ★ 2026-09-27「是否显示思考」三来源死活核查（TUI thinking 收尾三小件之三）：
// 1. SessionModel.ThinkingEnabled()　　 —— **活线，但只有 3 个读取点，且渲染侧已摘除**：
// 渲染侧（"是否显示思考"）已于 2026-09-24 改为**内容驱动**；本方法现仍被
// ① 请求装配（stream.go 的 `ai.ThinkingConfig{Enabled: …}`）、② 状态面板 `Thinking:` 行、
// ③ `/thinkback` 首行 读取 ⇒ **是请求侧的唯一三态来源，不得当死码删**。
// 2. command.OutputStyleRenderConfig.ShowThinking（internal/command/output_style_render.go:14）
// ⇒ **死字段**（零生产读取点），详见下方核查结论。
// 3. runnerCore.ThinkingEnabled（runner_core.go:147 字段 / :860 读入请求）—— plain 路径的真实开关，活线。
//
// 第 2 来源的 Grep 工具全仓复核（含 _test.go）：`ShowThinking` 共 **10 处** ——
// `output_style_render.go` 6 处（1 注释 + 1 字段定义 + 在 GetOutputStyleRenderConfig 内的 4 处赋值）、
// `output_style_render_test.go` 2 处（**唯一读取点**，且只读它自己构造的配置）、
// **本注释 2 处**（自指；写进本行是为了让下次复核的计数可逐字对上）。
// 连 `GetOutputStyleRenderConfig` 本身也**零生产调用点**（该文件头写「待 N9 合入后对接」，
// 而 N9 消费方从未合入）⇒ 字段与整个 OutputStyleRenderConfig 都是**未接线 SSOT**。
// 未在本轮删除：它落在 `internal/command`（超出本线 `internal/tui/**` 边界），
// 删除须由该包单写者执行；已登记 → `内部工作回执`。
func thinkingModeLabel(enabled *bool) string {
	switch {
	case enabled == nil:
		return "default (server-decided)"
	case *enabled:
		return "ON"
	default:
		return "OFF"
	}
}

// hasAssistantNarrativeContent distinguishes an assistant answer from a
// tool-only event. Tool-only events stay within the current user turn and do
// not create another Whale speaker header or timestamp.
//
// 2026-09-24：去掉 showThinking 入参 —— 有思考内容即视为叙事内容（保留思考）。
func hasAssistantNarrativeContent(blocks []session.ContentBlock, thinking string) bool {
	if strings.TrimSpace(thinking) != "" {
		return true
	}
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

// hasVisibleAssistantContent reports whether a finalized assistant turn has
// anything the transcript can show. AskUser calls intentionally render nothing
// because their separate choice UI owns the interaction; without this guard they
// left behind an empty "Whale" label and timestamp before the follow-up response.
//
// 2026-09-24：去掉 showThinking 入参 —— 有思考内容即视为可见（保留思考）。
func hasVisibleAssistantContent(blocks []session.ContentBlock, thinking string) bool {
	if strings.TrimSpace(thinking) != "" {
		return true
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				return true
			}
		case "tool_use":
			if renderToolUseCompact(block.Name, block.Input) != "" {
				return true
			}
		}
	}
	return false
}

// renderTextContent renders text with Markdown and file link support.
//
// width <= 0（旧口径）：glamour WordWrap=0，折行交给 TUI 的 Hardwrap。
// 需要按真实宽度排版表格/段落时用 renderTextContentWidth。
func renderTextContent(text string) string {
	return renderTextContentWidth(text, 0)
}

// renderTextContentWidth 按给定的**内容折行宽度**渲染 markdown 正文（修法 A）。
//
// 宽度口径与 wrapViewportContent 一致（contentWrapWidth），使 glamour 排版出的行宽
// ≤ 后续 TUI 折行宽度 ⇒ 表格列分隔符对齐、不再被拦腰硬切。
func renderTextContentWidth(text string, width int) string {
	// Interrupted streams may end inside a fenced Markdown block. Balance the
	// fence before passing text to glamour so the delimiter is not rendered as
	// literal punctuation.
	result := closeUnterminatedMarkdownFence(text)

	if r := rendererForWidth(width); r != nil {
		if rendered, err := r.Render(result); err == nil {
			result = rendered
		}
	}

	result = RenderTextWithLinks(result)

	return result
}

// renderPlainText 把**程序生成的纯文本**原样交给显示层（Plain 块的唯一渲染出口）。
//
// 为什么必须"原样"（，2026-10-13）：Markdown 会给段落内的软换行补一个空格、
// 并剥掉行首 ≤3 个空格 —— 恰好毁掉 `util.Bi` 的双语形态（英文主行 + 3 空格缩进的
// 中文辅助行）。这里不做任何重排，换行与缩进逐字保留。
//
// 宽度折行不在这里做：转录在 `wrapViewportContent` 处统一按 contentWrapWidth
// **硬折行**（：全仓唯一折行点），口径与 renderTextContentWidth 的 width 一致
// ⇒ 纯文本行不会比 Markdown 行更宽。
//
// 也刻意不做文件链接高亮（renderTextContentWidth 里的 RenderTextWithLinks）——
// Plain 的语义就是"逐字显示"，不引入任何变换。
func renderPlainText(text string) string {
	return strings.TrimRight(text, "\n")
}

func closeUnterminatedMarkdownFence(text string) string {
	if strings.Count(text, "```")%2 == 0 {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n```"
}

// handleCopy 将脱敏后的会话内容异步复制到系统剪贴板。
// /copy - copy last AI response
// /copy last - copy last AI response
// /copy all - copy entire conversation
// /copy N - copy message #N (1-based index)
func (m *Model) handleCopy(args string) string {
	var content string

	switch args {
	case "all":
		content = m.formatAllForCopy()
	case "last", "":
		content = m.formatLastForCopy()
	default:
		var n int
		if _, err := fmt.Sscanf(args, "%d", &n); err != nil || n <= 0 {
			return "Usage: /copy [last|all|N] (N = message number)"
		}
		content = m.formatMessageNForCopy(n)
	}

	if content == "" {
		return "Nothing to copy"
	}

	content, redactedCount := (&exportpkg.DefaultRedactor{}).Scrub(content)
	clipboard := exportpkg.NewClipboard()
	m.pendingCommandCmd = func() tea.Msg {
		return clipboardWriteResultMsg{err: clipboard.Copy(content)}
	}

	preview := content
	preview = truncateRunes(preview, 60)
	if redactedCount > 0 {
		return fmt.Sprintf("Copied to clipboard: %s（已脱敏 %d 处）", preview, redactedCount)
	}
	return fmt.Sprintf("Copied to clipboard: %s", preview)
}

// copyConversation starts a clipboard write for a key binding or mouse click.
// Slash commands collect pendingCommandCmd in sendInput; direct interactions
// must return it immediately or they only render a success message.
func (m *Model) copyConversation(args string) tea.Cmd {
	status := m.handleCopy(args)
	if m.notifier != nil {
		notification := notify.NewNotification("copy", "Clipboard", status)
		notification.Priority = notify.PriorityLow
		notification.TimeoutMs = 2500
		m.notifier.Push(notification)
	} else {
		m.addMessage(status)
	}
	cmd := m.pendingCommandCmd
	m.pendingCommandCmd = nil
	return cmd
}

// formatAllForCopy 将**屏幕所见**整份复制到剪贴板（：所见即所得）。
//
// 取数源 = displayEntries()（显示缓冲 ＋ 轮末边界标记投影），不再读模型侧
// session。屏幕上有的都进剪贴板：UI 回执、轮末标记、思考预览；省略行按
// source map 还原成完整原文（与鼠标拖选同一套机制，见 transcript_source.go）
// ⇒ 两个复制入口对同一条内容给出同一份真值（不变式 I-A 的 Copy all 侧）。
//
// 与旧口径的差（用户 2026-10-12 拍板「所见即所得」后**有意改变**，勿当回归）：
// - 旧源只有 user/assistant/tool 三类消息 ⇒ 回执 / 标记 / 预览天然缺席；
// - 旧前缀 [User]/[Whale] 与屏幕 👤 You:/🐋 Whale: 不一致；
// - 旧实现按消息间插空行，与屏幕排版不符（现在逐行直拼，同屏同文）。
//
// 边界（诚实声明）：流式进行中的尾部文本尚未定稿入显示缓冲，不进剪贴板
// （旧实现同样不进 —— 未定稿内容本就不该被复制走）。
func (m *Model) formatAllForCopy() string {
	entries := m.displayEntries()
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		lines := strings.Split(e.text, "\n")
		for i, ln := range lines {
			// 省略行 ⇒ 整段替换为登记的真值（摘要的列号与原文无对应关系，不按列切片）。
			if src := sourceAt(e.sources, i); src.hasOverride() {
				parts = append(parts, src.full)
				continue
			}
			parts = append(parts, strings.TrimRight(ansi.Strip(ln), " "))
		}
	}
	return strings.Join(parts, "\n")
}

// formatLastForCopy format last AI response for clipboard
func (m *Model) formatLastForCopy() string {
	for i := len(m.sessionService.CurrentSession().GetMessages()) - 1; i >= 0; i-- {
		msg := m.sessionService.CurrentSession().GetMessages()[i]
		if msg.Role == session.RoleAssistant {
			return msg.TextContent()
		}
	}
	return ""
}

// handleToolCommand 处理 /tool 命令：从 session 权威源现查工具结果完整输出。
//
// 用法：
//
//	/tool — 最近一条工具结果（完整原始输出）
//	/tool <N> — 第 N 条工具结果（按时间正序，1-based）
//
// 它是切口②的落地：不再依赖 lastToolResult 这个易失的截断副本（第二真相），
// 而是每次从 CurrentSession().GetMessages() 现查 RoleTool 消息的完整 Output。
// 完整输出在 session 里始终保留（不变式 I2），与显示侧 head+tail 折叠无关。
func (m *Model) handleToolCommand(args string) string {
	msgs := m.sessionService.CurrentSession().GetMessages()

	// 收集所有工具结果（保持时间正序）。
	// ★ 走 SSOT `toolResultSeq`：这里的序号与折叠提示里的 "/tool <N>" **同源**。
	// 两处各数一遍就是「提示指东、返回打西」的根源（C1 切口②），故不再就地遍历。
	seq := newToolResultSeq(msgs)
	toolsList := seq.entries

	if len(toolsList) == 0 {
		return "No tool results yet"
	}

	// 默认最近一条；/tool <N> 回看第 N 条。
	selected := toolsList[len(toolsList)-1]
	if args != "" {
		var n int
		if _, err := fmt.Sscanf(args, "%d", &n); err != nil || n <= 0 {
			return "Usage: /tool [N] (N = tool result number, 1-based)"
		}
		if n > len(toolsList) {
			return fmt.Sprintf("Only %d tool result(s) available (requested #%d)", len(toolsList), n)
		}
		selected = toolsList[n-1]
	}

	icon := "📋"
	if selected.err {
		icon = "❌"
	}
	return fmt.Sprintf("%s [%d/%d] %s:\n%s", icon, selected.idx, len(toolsList), selected.name, selected.out)
}

// monitorTailLines 是 `/monitors tail <id>` 每次回显的行数上限。
//
// 取值对标 同类 CLI `ShellDetailDialog.tsx`：面板每次只读最后 8 KB、
// **只渲染最后 10 行**，并注明总量（`:329` 的 10 行循环、`:371` 的
// `Showing ${n} lines`）。whale 取同一个数，理由相同：输出是**证据**不是**陈述**，
// 用户要的是"现在长什么样"，不是"从头到现在每一行"。
const monitorTailLines = 10

// monitorStopHint 是「怎么停下来」的**唯一文案源**（A-4，2026-09-30）。
//
// 为什么要点名：`/monitors stop <id>` 是后台进程**唯一**的停止出口，
// 而该命令在命令面是 `Hidden: true`（internal/command/commands_core.go:60）
// ⇒ 用户在帮助里看不到它。真机手测 I-7.1 的结论是"用户无法停止"，
// 所以凡是要用户去停的地方，都必须把命令**原样印出来**，不能只说"用停止命令"。
func monitorStopHint(id string) string {
	if strings.TrimSpace(id) == "" {
		return "/monitors stop <id>"
	}
	return "/monitors stop " + id
}

// handleMonitors handles /monitors command
//
// A-4：列表底部的停止提示**必须**保留（它原本就在，别删）；
// 新增 `tail <id>` 子命令 —— 因为 A-2 之后主界面**不再显示任何输出行**，
// 必须给用户一个"要查就走这里"的显性出口，否则切断直推等于把输出藏起来。
func (m *Model) handleMonitors(args string) string {
	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		return "Monitor manager not initialized"
	}
	args = strings.TrimSpace(args)

	// 动词分派：`stop <id>` / `tail <id>` / `show <id>`（`show` 是 tail 的**兼容别名**，
	// 有意不登记为独立命令、不在任何提示里宣传 ⇒ 不构成做不到的承诺）。
	//
	// ★ 2026-09-30：改为按 Fields 取动词，而不是 `HasPrefix(args, "stop ")`。
	// 旧写法要求动词后**必须带尾随空格**才进 stop 分支，于是「动词在、id 缺」的
	// `/monitors stop` 会掉进列表分支 —— 用户敲了一条明确的停止命令，却拿到一张进程列表。
	// 现在是显式用法提示。同一个 bug 也被"注册全名"放大：`/monitors stop` 现在是一个
	// 独立命令（args 为空），不再有任何理由让它表现为"列表"。
	verb, id := "", ""
	if fields := strings.Fields(args); len(fields) > 0 {
		verb = fields[0]
		id = strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
	}
	switch verb {
	case "stop":
		if id == "" {
			return "Usage: /monitors stop <id>"
		}
		if err := mgr.Stop(id); err != nil {
			return errorStyle.Render(fmt.Sprintf("❌ Failed to stop monitor: %v", err))
		}
		return fmt.Sprintf("⏹️ Monitor %s stopped", id)
	case "tail", "show":
		if id == "" {
			return "Usage: /monitors tail <id>"
		}
		// A-2 的读取出口；只读权威副本，不新增任何副本。
		return m.renderMonitorTail(id)
	}
	// 无动词（或未知动词）⇒ 列出全部监控进程。
	processes := mgr.List()
	if len(processes) == 0 {
		return "📡 No monitoring processes running\n\nUse the Monitor tool to start monitoring (e.g. watch logs, poll APIs, etc.)"
	}
	// 稳定顺序：List 走 map 遍历，顺序随机 ⇒ 不排序的话每次回执的行序都在变。
	sort.Slice(processes, func(i, j int) bool { return processes[i].ID < processes[j].ID })

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📡 Monitors (%d)\n\n", len(processes)))

	for _, proc := range processes {
		// 保留行数 vs 累计行数：这是 W3「把输出的界变成用户可见的事实」的落点 ——
		// 旧实现只报一个 `%d lines`，用户无从知道有没有被截。
		retained := retainedLineCount(proc.GetOutput())
		sb.WriteString(fmt.Sprintf(" %s | %s | %s | %d lines (kept %d) | %s\n",
			proc.ID,
			proc.GetStatus(),
			proc.Description,
			proc.GetLines(),
			retained,
			time.Since(proc.StartTime).Round(time.Second),
		))
	}

	// A-5：把"丢了多少"变成可见事实（可丢，但不可不可见）。
	if note := monitorDroppedNote(m); note != "" {
		sb.WriteString("\n" + note + "\n")
	}

	// 尾部两条出口提示（保留）：
	// - `tail <id>`：A-2 之后主界面不再直推任何输出行 ⇒ 必须点名"要查就走这里"；
	// - `Stop a monitor: /monitors stop <id>`：本命令历史上有过"用提示代替可达性"的时期
	// （原注释：★ 停止入口显性化 —— 命令面把它 Hidden 了，这里是用户唯一能看到它的地方）。
	// 2026-09-30 已取消隐藏 + 登记家族（`/help` 与 `/` 弹窗都能看到它）⇒ 该"唯一可见处"
	// 的说法**不再成立**。提示仍保留：它带的是**本次真实 id 的用法形态**，
	// 比弹窗里的占位 `<id>` 更具体，不是同一件事的重复。
	sb.WriteString(fmt.Sprintf("\nView output: /monitors tail <id>\nStop a monitor: %s", monitorStopHint("")))

	return sb.String()
}

// retainedLineCount 数一段输出实际保留了多少行（用于把"界"显示成事实）。
func retainedLineCount(output string) int {
	if output == "" {
		return 0
	}
	return len(strings.Split(strings.TrimRight(output, "\n"), "\n"))
}

// renderMonitorTail 渲染某台 Monitor 的**尾部**输出（有界：最后 monitorTailLines 行）。
//
// 数据来源 = internal/monitor 的权威副本（proc.GetOutput / GetLines），只读；
// 本函数不缓存、不复制输出，因此不构成第二份副本。
func (m *Model) renderMonitorTail(id string) string {
	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		return "Monitor manager not initialized"
	}
	if id == "" {
		return "Usage: /monitors tail <id>"
	}
	proc, ok := mgr.Get(id)
	if !ok {
		return errorStyle.Render(fmt.Sprintf("❌ No monitor %q (see /monitors)", id))
	}

	total := proc.GetLines()
	var tail []string
	if all := strings.Split(strings.TrimRight(proc.GetOutput(), "\n"), "\n"); len(all) > 0 && all[0] != "" {
		start := 0
		if len(all) > monitorTailLines {
			start = len(all) - monitorTailLines
		}
		tail = all[start:]
	}

	icon, statusText := monitorStatusLabel(proc.GetStatus(), proc.ExitCode)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📡 %s · %s · %s\n",
		proc.ID, icon+" "+statusText, truncateRunewidthWithEllipsis(proc.Description, 60)))
	// 界要写清楚：显示了几行、缓冲区里有多少、累计产出多少。
	sb.WriteString(fmt.Sprintf("   showing last %d line(s) · buffer holds %d · %d produced in total\n\n",
		len(tail), retainedLineCount(proc.GetOutput()), total))
	if len(tail) == 0 {
		sb.WriteString("   (no output yet)\n")
	} else {
		sb.WriteString(strings.Join(tail, "\n"))
		sb.WriteString("\n")
	}
	sb.WriteString("\n" + monitorStopHint(proc.ID))
	return sb.String()
}

// formatMessageNForCopy format message #N for clipboard (1-based)
func (m *Model) formatMessageNForCopy(n int) string {
	idx := n - 1
	if idx < 0 || idx >= len(m.sessionService.CurrentSession().GetMessages()) {
		return ""
	}
	msg := m.sessionService.CurrentSession().GetMessages()[idx]
	return fmt.Sprintf("[%s] %s", msg.Role, msg.TextContent())
}

// copyToClipboard copy text to system clipboard
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("clip")
	case "darwin":
		cmd = exec.Command("pbcopy")
	default:
		cmd = exec.Command("xclip", "-selection", "clipboard")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// renderThinkingBlock 渲染思考边框（顶底等宽+右侧闭合）。
//
// ⚠️ 当前无生产调用点（2026-09-25 排版收敛：finalize 态思考折叠为单行，
// 见 formatAssistantMessageWithThinking；流式框是 stream.go 的内联实现，不走本函数）。
// 保留作「折叠态 → 展开态」交互扩展的现成渲染器；若届时仍不接线，应整函数删除
// （教训：为死函数写测试 = 测不跑的那一份）。
// 宽度用 contentWidth（= 调用方传入的折行宽度），右侧闭合 │，顶底等宽。
// 对标同类 CLI with_border_internal（末尾补空格对齐右竖线）。
// REF-外部: 同类 CLI
func renderThinkingBlock(content string, contentWidth int, spinner string) string {
	// 顶行最小宽度 = 5 + labelWidth(11) = 16；不足时画框必然残缺（顶行超宽被
	// Hardwrap 折断），整体退化为纯文本（对标同类 CLI card_inner_width < 4 返回 None）。
	if contentWidth < 16 {
		return content
	}
	var sb strings.Builder
	label := "🧠 Thinking"
	labelWidth := runewidth.StringWidth(label)
	// 顶部：╭─ label ──...─╮（总宽 = contentWidth）
	// 组成：╭(1) + ─(1) + space(1) + label(labelWidth) + space(1) + dashes(topDashCount) + ╮(1) = 5 + labelWidth + topDashCount
	topDashCount := contentWidth - 5 - labelWidth
	if topDashCount < 0 {
		topDashCount = 0
	}
	sb.WriteString(thinkingBorderStyle.Render("╭─ "))
	sb.WriteString(thinkingLabelStyle.Render(label))
	sb.WriteString(thinkingBorderStyle.Render(" " + strings.Repeat("─", topDashCount) + "╮"))
	sb.WriteString("\n")
	// 中间：│ content padding │（每行右侧补空格，总宽 = contentWidth）
	// 组成：│(1) + space(1) + content(usedWidth) + padding(padCount) + space(1) + │(1) = 4 + usedWidth + padCount
	// F1（2026-09-08 对抗式审查）：长内容行先硬折到框内宽度 contentWidth-4，
	// 否则整行超宽会被 wrapViewportContent 的 Hardwrap 二次折断，右侧 │ 掉到下一行。
	inner := contentWidth - 4
	for _, line := range strings.Split(content, "\n") {
		for _, seg := range strings.Split(ansi.Hardwrap(line, inner, true), "\n") {
			usedWidth := runewidth.StringWidth(seg)
			padCount := inner - usedWidth
			if padCount < 0 {
				padCount = 0
			}
			sb.WriteString(thinkingStyle.Render("│ " + seg + strings.Repeat(" ", padCount) + " │"))
			sb.WriteString("\n")
		}
	}
	// 底部：╰───...───╯（总宽 = contentWidth）
	// 组成：╰(1) + dashes(contentWidth-2) + ╯(1) = contentWidth
	bottom := "╰" + strings.Repeat("─", contentWidth-2) + "╯"
	sb.WriteString(thinkingBorderStyle.Render(bottom))
	return sb.String()
}
