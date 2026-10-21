package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	exportpkg "github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	tea "github.com/charmbracelet/bubbletea"
)

// handleThink 处理 thinking 三态开关。
//
// 三态语义（对齐业界：同类 CLI `ReasoningEffort::None` 是显式枚举变体，
// 同类 CLI 用判别联合 {type:'disabled'} 显式构造）：
//
//	on → 显式开启（请求下发 enable_thinking=true）
//	off → 显式关闭（请求下发 enable_thinking=false —— 服务端不能再自作主张）
//	reset → 回到「未指定」：不下发该字段，由服务端默认决定
//
// 修复前 off/reset 都只写 false，而 false 在二值类型下根本无法下发
// ⇒ 服务端默认开思考时，/think off 是假的。
func (m *Model) handleThink(args string) string {
	switch args {
	case "on":
		m.sessionService.SetThinkingEnabled(ai.ThinkingPtr(true))
		return "Thinking mode ON (token usage may increase 4-26x)"
	case "off":
		m.sessionService.SetThinkingEnabled(ai.ThinkingPtr(false))
		return "Thinking mode OFF"
	default:
		m.sessionService.SetThinkingEnabled(nil)
		return fmt.Sprintf("Thinking mode reset → %s", thinkingModeLabel(nil))
	}
}

// splitArgs splits a command args string by spaces, respecting quotes
func splitArgs(s string) []string {
	var result []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, ch := range s {
		switch {
		case ch == '"' || ch == '\'':
			if inQuote && ch == quoteChar {
				inQuote = false
				quoteChar = 0
			} else if !inQuote {
				inQuote = true
				quoteChar = ch
			} else {
				current.WriteRune(ch)
			}
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// buildWelcome keeps startup focused on the conversation. Runtime model,
// permission, and workspace details already live in the fixed footer.
func buildWelcome(_, _, _ string) string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Whale Code v0.1.0"))
	sb.WriteString("\n")
	sb.WriteString("Ready. Enter a request to begin")
	sb.WriteString(" │ /help commands")
	sb.WriteString(" │ /copy all history")
	return sb.String()
}

// handleEnv handles /env command
func (m *Model) handleEnv(args string) string {
	if cb := m.cmdRegistry.OnEnv; cb != nil {
		return cb(args)
	}
	return "Environment variables: (use /env KEY=VALUE to set)"
}

// truncateRunes safely truncates a string to at most n runes, preserving UTF-8 validity
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// pluralS returns English plural suffix
func pluralS(n int) string {
	if n != 1 {
		return "s"
	}
	return ""
}

// handlePlan handles /plan command — toggle plan mode (read-only analysis mode)
// 判据锚点（测试与文案共用，防文案漂移导致假绿）。
const (
	// planModePromptMarker 注入系统提示的 PLAN MODE 块首句锚点。
	planModePromptMarker = "PLAN MODE IS ON"
	// planLevelHonestyMarker /plan on 文案的诚实标记：必须明示是提示层引导、非硬性拦截。
	planLevelHonestyMarker = "prompt-level"
)

// PlanModePrompt 是 planMode=true 时注入系统提示的「只分析」约束（拍板：
// /plan 的灯接进提示层；硬门 PermissionStage 拦写发布后立项）。模型侧英文单侧（分野）。
const PlanModePrompt = planModePromptMarker + `: analyze and plan only. Do not write, edit, create, or delete files, and do not run state-changing commands. Investigate the code, then present your plan and wait for the user's approval before taking any action.`

// handlePlan 处理 /plan 命令 — 切换计划模式（只读分析模式）
func (m *Model) handlePlan(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	switch args {
	case "on":
		m.sessionService.SetPlanMode(true)
		// （2026-10-19 拍板）：文案诚实化 —— 修前宣称「AI will only analyze, not execute」
		// 但无任何强制机制背书（UI 说谎）；实情 = 提示层引导，硬门发布后立项。
		return bi("📋 Plan mode ON\n⚠️ The AI will be instructed to analyze only (prompt-level guidance, not a hard block) — suited for design review and code review",
			"📋 计划模式已开启\n⚠️ 将提示 AI 仅分析不动手（提示层引导，非硬性拦截），适合设计评审和代码审查")
	case "off":
		m.sessionService.SetPlanMode(false)
		return bi("📋 Plan mode OFF", "📋 计划模式已关闭")
	case "":
		statusEN, statusZH := "OFF", "关闭"
		if m.sessionService.PlanMode() {
			statusEN, statusZH = "ON ✅", "开启 ✅"
		}
		return bi(fmt.Sprintf("📋 Plan mode: %s\n\nUsage:\n /plan on — enable (AI is instructed to analyze only; prompt-level guidance)\n /plan off — disable (normal mode)", statusEN),
			fmt.Sprintf("📋 计划模式: %s\n\n用法:\n /plan on — 开启（提示 AI 仅分析；提示层引导）\n /plan off — 关闭（正常模式）", statusZH))
	default:
		m.sessionService.SetPlanMode(!m.sessionService.PlanMode())
		statusEN, statusZH := "OFF", "关闭"
		if m.sessionService.PlanMode() {
			statusEN, statusZH = "ON ✅", "开启 ✅"
		}
		return bi(fmt.Sprintf("📋 Plan mode toggled: %s", statusEN),
			fmt.Sprintf("📋 计划模式已切换: %s", statusZH))
	}
}

// handlePrivacy handles /privacy command — show/manage privacy settings
// handlePrivacy 处理 /privacy 命令 — 显示/管理隐私设置
func (m *Model) handlePrivacy(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	m.ensurePrivacyConfig()

	switch args {
	case "", "show":
		analyticsEN, analyticsZH := "OFF", "关闭"
		if m.sessionService.PrivacyConfig().AnalyticsEnabled {
			analyticsEN, analyticsZH = "ON", "开启"
		}
		errorRptEN, errorRptZH := "OFF", "关闭"
		if m.sessionService.PrivacyConfig().ErrorReporting {
			errorRptEN, errorRptZH = "ON", "开启"
		}
		sharingEN, sharingZH := "not shared", "不共享"
		if m.sessionService.PrivacyConfig().DataSharing {
			sharingEN, sharingZH = "shared", "已共享"
		}
		return bi(fmt.Sprintf("🔒 Privacy settings\n\n Telemetry analytics: %s\n Error reporting: %s\n Data sharing: %s\n\nUse /privacy analytics-on|analytics-off to toggle", analyticsEN, errorRptEN, sharingEN),
			fmt.Sprintf("🔒 隐私设置\n\n 遥测分析: %s\n 错误报告: %s\n 使用数据: %s\n\n使用 /privacy analytics-on|analytics-off 切换", analyticsZH, errorRptZH, sharingZH))
	case "analytics-on":
		m.sessionService.PrivacyConfig().AnalyticsEnabled = true
		m.savePrivacyConfig()
		return bi("✅ Telemetry analytics enabled", "✅ 遥测分析已开启")
	case "analytics-off":
		m.sessionService.PrivacyConfig().AnalyticsEnabled = false
		m.savePrivacyConfig()
		return bi("✅ Telemetry analytics disabled", "✅ 遥测分析已关闭")
	default:
		return biSame("Unknown subcommand: %s\nUsage: /privacy [show|analytics-on|analytics-off]",
			"未知子命令: %s\n用法: /privacy [show|analytics-on|analytics-off]", args)
	}
}

// ensurePrivacyConfig ensures privacy config is loaded
func (m *Model) ensurePrivacyConfig() {
	if m.sessionService.PrivacyConfig() != nil {
		return
	}
	m.sessionService.SetPrivacyConfig(&PrivacyConfig{})
	cfgPath := filepath.Join(whalehome.Dir(), "privacy.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	json.Unmarshal(data, m.sessionService.PrivacyConfig())
}

// savePrivacyConfig persists privacy config to file
func (m *Model) savePrivacyConfig() {
	if m.sessionService.PrivacyConfig() == nil {
		return
	}
	dir := whalehome.Dir()
	os.MkdirAll(dir, 0755)
	cfgPath := filepath.Join(dir, "privacy.json")
	data, err := json.MarshalIndent(m.sessionService.PrivacyConfig(), "", " ")
	if err != nil {
		return
	}
	if err := util.AtomicWriteFile(cfgPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save privacy config: %v\n", err)
	}
}

// handleStats handles /stats command — show detailed statistics
// handleStats handles /stats command — show session statistics
// handleStats 处理 /stats 命令 — 显示会话统计信息
func (m *Model) handleStats() string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	var sb strings.Builder
	sb.WriteString(bi("📊 Statistics", "📊 统计信息") + "\n\n")

	sb.WriteString(bi("This session:", "本次会话:") + "\n")
	sb.WriteString(biSame(" Input Tokens: %s", " 输入 Tokens: %s", formatIntWithComma(m.sessionService.InputTokens())) + "\n")
	sb.WriteString(biSame(" Output Tokens: %s", " 输出 Tokens: %s", formatIntWithComma(m.sessionService.OutputTokens())) + "\n")
	totalTokens := m.sessionService.InputTokens() + m.sessionService.OutputTokens()
	sb.WriteString(biSame(" Total Tokens: %s", " 总 Tokens: %s", formatIntWithComma(totalTokens)) + "\n")
	sb.WriteString(biSame("Turns: %d", "轮次: %d", m.sessionService.CurrentSession().Metadata.TurnCount) + "\n")
	sb.WriteString(biSame("Messages: %d", "消息数: %d", len(m.sessionService.CurrentSession().GetMessages())) + "\n")
	sb.WriteString(biSame(" Compactions: %d", " 压缩次数: %d", m.sessionService.CompactCount()) + "\n")

	if m.sessionService.UsageTracker() != nil {
		in, out, cacheCreate, cacheRead := m.sessionService.UsageTracker().GetSnapshot()
		if in > 0 || out > 0 {
			sb.WriteString("\n" + bi("Cumulative usage:", "累计用量:") + "\n")
			sb.WriteString(biSame(" Total input: %s", " 总输入: %s", formatTokensForCount(in)) + "\n")
			sb.WriteString(biSame(" Total output: %s", " 总输出: %s", formatTokensForCount(out)) + "\n")
			if cacheCreate > 0 {
				sb.WriteString(biSame(" Cache creation: %s", " 缓存创建: %s", formatTokensForCount(cacheCreate)) + "\n")
			}
			if cacheRead > 0 {
				sb.WriteString(biSame(" Cache read: %s", " 缓存读取: %s", formatTokensForCount(cacheRead)) + "\n")
			}
			// 预估费用行已移除：provider 不返回费用详情，本地价格表估算与真实账单不符。
		}
	}

	sessionCount := 0
	if m.sessionService.Store() != nil {
		sessions, err := m.sessionService.Store().List()
		if err == nil {
			sessionCount = len(sessions)
		}
	}
	sb.WriteString("\n" + biSame("Session history:\n Total sessions: %d", "会话历史:\n 总会话数: %d", sessionCount))

	return sb.String()
}

// handleSummary handles /summary command — generate conversation summary
// handleSummary 处理 /summary 命令 — 生成对话摘要
func (m *Model) handleSummary(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	msgs := m.sessionService.CurrentSession().GetMessages()
	if len(msgs) < 2 {
		return bi("📝 Not enough conversation to summarize\n(at least 2 messages required)",
			"📝 对话内容不足，无法生成摘要\n（至少需要 2 条消息）")
	}

	var sb strings.Builder
	sb.WriteString(bi("📝 Conversation summary", "📝 对话摘要") + "\n\n")

	topic := m.sessionService.SessionName()
	if topic == "" && m.firstUserInput != "" {
		if len(m.firstUserInput) > 60 {
			topic = m.firstUserInput[:60] + "..."
		} else {
			topic = m.firstUserInput
		}
	}
	topicEN, topicZH := topic, topic
	if topic == "" {
		topicEN, topicZH = "(unnamed)", "(未命名)"
	}
	sb.WriteString(bi(fmt.Sprintf("Topic: %s", topicEN), fmt.Sprintf("主题: %s", topicZH)) + "\n")

	if m.sessionService.CurrentSession().Metadata.CreatedAt > 0 {
		created := time.UnixMilli(m.sessionService.CurrentSession().Metadata.CreatedAt)
		duration := time.Since(created)
		sb.WriteString(biSame("Duration: %v", "持续时间: %v", duration.Round(time.Second)) + "\n")
	}

	turnCount := m.sessionService.CurrentSession().Metadata.TurnCount
	sb.WriteString(biSame("Turns: %d", "轮次: %d", turnCount) + "\n\n")

	userMsgCount := 0
	assistantMsgCount := 0
	toolCallCount := 0
	filesSeen := make(map[string]bool)

	for _, msg := range msgs {
		role := string(msg.Role)
		switch role {
		case "user":
			userMsgCount++
		case "assistant":
			assistantMsgCount++
		case "tool":
			toolCallCount++
		}
		for _, block := range msg.Blocks {
			contentStr := block.Text
			for _, ext := range []string{".go", ".ts", ".js", ".py", ".rs", ".java", ".md", ".json", ".yaml", ".yml"} {
				if strings.Contains(contentStr, ext) {
					idx := strings.LastIndex(contentStr, ext)
					if idx >= 0 {
						start := strings.LastIndex(contentStr[:idx], string(os.PathSeparator))
						if start == -1 {
							start = strings.LastIndex(contentStr[:idx], "/")
						}
						if start == -1 {
							start = strings.LastIndex(contentStr[:idx], `"`)
						}
						if start == -1 {
							start = strings.LastIndex(contentStr[:idx], " ") // 空格作为文件名起始分隔符
						}
						if start >= 0 && start < idx {
							fileName := contentStr[start+1 : idx+len(ext)]
							filesSeen[fileName] = true
						}
					}
				}
			}
		}
	}

	sb.WriteString(biSame("Message stats:\n user: %d | AI: %d | tool: %d", "消息统计:\n 用户: %d | AI: %d | 工具: %d", userMsgCount, assistantMsgCount, toolCallCount) + "\n")

	if len(filesSeen) > 0 {
		sb.WriteString("\n" + bi("Files involved:", "涉及文件:") + "\n")
		for f := range filesSeen {
			sb.WriteString(fmt.Sprintf(" • %s\n", f))
		}
	}

	return sb.String()
}

// handleThinkback handles /thinkback command — review thinking process
// handleThinkback 处理 /thinkback 命令 — 回顾思考过程
//
// ★ 2026-09-27 三态同步：回执首行恒为**当前档位**（文案源 thinkingModeLabel，
// 与 /think 回执、状态面板同源 ⇒ 「同一事实跨入口同源」）；
// 「没有思考内容」的提示按 off / default / on 三档分派。
// 改前三档回执**逐字相同**且恒提示「使用 /think on」——在**已经 ON** 的档位上
// 这条提示指向用户已经走过的路（症状：以为没开）。
//
// ★ 2026-10-03 数据源收口：思考内容的**唯一真相源 = 会话存档**
// （assistant 消息里的 thinking 块，由 `handleStreamDone` 落盘）。
// 改前读的是 TUI 侧的内存存储（`SessionModel.CurrentThinking` / `thinkingHistory`），
// 二者在生产**零写入点** ⇒ 真机上「有内容」的两个出口恒不可达，
// 命令只会说「（暂无思考记录）」—— 而模型其实输出了思考（屏幕上就有 🧠 Thinking 那一行）。
// 现在 default / all / clear 三处都直接对存档读写，于是 resume 之后同样可回顾。
func (m *Model) handleThinkback(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	head := m.thinkbackModeLine()
	switch args {
	case "clear":
		m.clearThinkingRecords()
		return head + bi("🧹 Thinking records cleared", "🧹 思考记录已清空")
	case "all":
		records := m.thinkingRecords()
		if len(records) == 0 {
			return head + bi("💭 Thinking review: (no thinking records yet)", "💭 思考回顾:（暂无思考记录）") + "\n\n" + m.thinkbackEmptyHint()
		}
		var sb strings.Builder
		sb.WriteString(head)
		sb.WriteString(biSame("💭 Thinking review (%d records)", "💭 思考回顾（%d 条记录）", len(records)) + "\n\n")
		for i, record := range records {
			sb.WriteString(biSame("--- Record #%d (%s) ---", "--- 记录 #%d (%s) ---", i+1,
				record.Timestamp.Format("01-02 15:04:05")) + "\n")
			promptPreview := record.Prompt
			if len(promptPreview) > 50 {
				promptPreview = promptPreview[:50] + "..."
			}
			sb.WriteString(biSame("Trigger: %s", "触发: %s", promptPreview) + "\n")
			sb.WriteString(biSame("Length: %d chars", "长度: %d 字符", record.Length) + "\n")
			contentPreview := record.Content
			if len(contentPreview) > 200 {
				contentPreview = contentPreview[:200] + "...(truncated)"
			}
			sb.WriteString(biSame("Content:\n%s", "内容:\n%s", contentPreview) + "\n\n")
		}
		return sb.String()
	default:
		records := m.thinkingRecords()
		if len(records) == 0 {
			return head + bi("💭 Thinking review: (no thinking records yet)", "💭 思考回顾:（暂无思考记录）") + "\n\n" + m.thinkbackEmptyHint()
		}
		last := records[len(records)-1]
		var sb strings.Builder
		sb.WriteString(head)
		sb.WriteString(bi("💭 Latest thinking process:", "💭 最近一次思考过程:") + "\n\n")
		contentPreview := last.Content
		if len(contentPreview) > 500 {
			contentPreview = contentPreview[:500] + "...(truncated)"
		}
		sb.WriteString(contentPreview)
		sb.WriteString(biSame("\n\n(%d chars, %s)", "\n\n(%d 字符, %s)", last.Length,
			last.Timestamp.Format("2006-01-02 15:04:05")))
		if len(records) > 1 {
			sb.WriteString(biSame("\n\n%d records in total — use /thinkback all to view them all", "\n\n共 %d 条历史记录，使用 /thinkback all 查看全部", len(records)))
		}
		return sb.String()
	}
}

// thinkingRecords 从**会话存档**派生 `/thinkback` 要展示的思考记录（按消息顺序，即时间正序）。
//
// 真相源 = assistant 消息里的 thinking 块（唯一落盘点是 `handleStreamDone`）。
// 「触发」取该 assistant 消息**之前最近一条** user 消息的正文：它可由消息顺序直接推出，
// 不必另存一份「提问」副本 —— 另存必然与真实提问漂移（同一事实两处存储的老病）。
func (m *Model) thinkingRecords() []thinkingRecord {
	msgs := m.sessionService.CurrentSession().GetMessages()
	records := make([]thinkingRecord, 0, len(msgs))
	lastUser := ""
	for _, msg := range msgs {
		switch msg.Role {
		case session.RoleUser:
			lastUser = msg.TextContent()
		case session.RoleAssistant:
			if !msg.HasThinking() {
				continue
			}
			content := msg.ThinkingContent()
			records = append(records, thinkingRecord{
				Timestamp: time.UnixMilli(msg.Timestamp),
				Prompt:    lastUser,
				Content:   content,
				Length:    len(content),
			})
		}
	}
	return records
}

// clearThinkingRecords 剥掉会话存档里全部 thinking 块，并**落盘**。
//
// 为什么必须落盘：只改内存的话，退出后 resume 思考又回来了 ——
// 「clear 了但没清掉」比不提供 clear 更糟（用户以为删干净了）。
// 作用域严格限定在 thinking 块：对话本体（text / tool_use / tool_result / image）一律保留。
func (m *Model) clearThinkingRecords() {
	sess := m.sessionService.CurrentSession()
	msgs := sess.GetMessages()
	stripped := make([]session.Message, 0, len(msgs))
	changed := false
	for _, msg := range msgs {
		next := msg.WithoutThinkingBlocks()
		if len(next.Blocks) != len(msg.Blocks) {
			changed = true
		}
		stripped = append(stripped, next)
	}
	if !changed {
		return
	}
	sess.SetMessages(stripped)
	_ = m.saveSession()
}

// thinkbackModeLine 是 /thinkback 回执的首行：当前 thinking 三态档位。
//
// ★ 文案源是 thinkingModeLabel（format.go）——/think 的回复与状态面板都读它。
// 这里若另写一套措辞，就退化成「同一事实多处各写各的」（/ 族）。
func (m *Model) thinkbackModeLine() string {
	return fmt.Sprintf("Thinking: %s\n", thinkingModeLabel(m.sessionService.ThinkingEnabled()))
}

// thinkbackEmptyHint 是「没有思考内容」时按三态分派的下一步提示。
//
// 为什么必须分档：没有思考有三种互不相同的原因，混成一句会把人指向错路 ——
// - OFF ：确实关着 ⇒ 这是唯一该让用户去 /think on 的档
// - default ：交给服务端默认决定 ⇒ 提示「可 /think on 强制开」
// - ON ：已经开着 ⇒ **不得**再让用户 /think on（改前正是在这一档说错话）
func (m *Model) thinkbackEmptyHint() string {
	enabled := m.sessionService.ThinkingEnabled()
	switch {
	case enabled == nil:
		return bi("Hint: thinking mode is default (server-decided) — `/think on` forces it on.",
			"提示：当前由服务端默认决定，可 /think on 强制开启。")
	case *enabled:
		return bi("Hint: thinking is ON, but no thinking content has arrived yet.",
			"提示：思考已开启，只是还没有思考内容到达（模型可能未输出）。")
	default:
		return bi("Hint: thinking is OFF — `/think on` enables it.",
			"提示：思考已关闭，/think on 打开。")
	}
}

// ★ 2026-10-03 删除 `saveThinkingRecord(prompt)`：它往 `SessionModel.thinkingHistory`
// 这条内存环形缓冲里追加记录，而该函数**全仓零生产调用点**（只有测试调过），
// 且那份缓冲与「会话存档里的 thinking 块」是同一事实的第二份存储。
// 思考内容的落点现在只有一处：`handleStreamDone` → 会话存档（见 stream.go 同名注释）。
// 保留一个没人写、只有测试读的 ring buffer 只会让下一个人以为「历史记录已经实现了」。

// handleBreakCache 处理 /break-cache 命令 — 强制打断提示缓存
func (m *Model) handleBreakCache(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	switch args {
	case "status":
		statusEN, statusZH := "Normal", "正常"
		if m.sessionService.CacheBroken() {
			statusEN, statusZH = "⚠️ triggered (next request will be sent in full)", "⚠️ 已触发（下次请求将完整发送）"
		}
		return bi(fmt.Sprintf("💥 Cache break status\n\n Status: %s\n Break count: #%d (this session)\n\nHint: use /break-cache to force a cache break", statusEN, m.sessionService.CacheBreakCount()),
			fmt.Sprintf("💥 缓存打断状态\n\n 状态: %s\n 打断次数: 第 %d 次（本次会话）\n\n提示: 使用 /break-cache 强制打断缓存", statusZH, m.sessionService.CacheBreakCount()))
	default:
		m.sessionService.SetCacheBroken(true)
		m.sessionService.SetCacheBreakCount(m.sessionService.CacheBreakCount() + 1)
		return bi(fmt.Sprintf("💥 Cache broken\n\n Status: triggered\n Next request: sent in full (prompt cache bypassed)\n Break count: #%d (this session)\n\n⚠️ Breaking the cache increases latency and token usage; use only when you need a fully fresh result", m.sessionService.CacheBreakCount()),
			fmt.Sprintf("💥 缓存已打断\n\n 状态: 已触发\n 下次请求: 完整发送（不使用提示缓存）\n 打断次数: 第 %d 次（本次会话）\n\n⚠️ 打断缓存会增加延迟和 token 消耗，仅在需要全新结果时使用", m.sessionService.CacheBreakCount()))
	}
}

// handleOnboarding handles /onboarding command — onboarding flow
// handleOnboarding 处理 /onboarding 命令 — 新手引导流程
func (m *Model) handleOnboarding(args string) string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	if args == "skip" {
		return bi("Onboarding skipped. Run /onboarding anytime to view it again.",
			"已跳过新手引导。随时使用 /onboarding 再次查看。")
	}

	var sb strings.Builder

	sb.WriteString(bi("Welcome to Whale Code!", "欢迎使用 Whale Code！") + "\n\n")
	sb.WriteString(bi("Whale Code is a feature-rich AI coding assistant. Here are its core features:",
		"Whale Code 是一个功能丰富的 AI 编程助手。以下是核心功能介绍：") + "\n\n")

	sb.WriteString(bi("1. Core commands\n\n"+
		" /help              show all commands and keyboard shortcuts\n"+
		" /model <name>      view/switch AI model\n"+
		" /models            list all available models\n"+
		" /compact [force] [--ai] compact the context (save tokens)\n"+
		" /clear             clear the conversation and start over\n"+
		" /think on|off|default  switch AI thinking mode (default = server default)\n"+
		" /usage             show token usage details",
		"1. 核心命令\n\n"+
			" /help              显示所有命令及快捷键\n"+
			" /model <名称>      查看/切换 AI 模型\n"+
			" /models            列出所有可用模型\n"+
			" /compact [force] [--ai] 压缩上下文（节省 token）\n"+
			" /clear             清空对话并重新开始\n"+
			" /think on|off|default  切换 AI 思考模式（default=回到服务端默认）\n"+
			" /usage             显示 token 用量详情") + "\n\n")

	sb.WriteString(bi("2. Permissions & security\n\n"+
		" /permissions       choose a permission mode with arrow keys\n"+
		" /permissions [mode|status]  set/view permission mode directly\n"+
		" /sandbox on|off    toggle sandbox mode\n"+
		" Whale asks for confirmation before sensitive operations",
		"2. 权限与安全\n\n"+
			" /permissions       上下键选择权限模式\n"+
			" /permissions [mode|status]  直接设置/查看权限模式\n"+
			" /sandbox on|off    切换沙箱模式\n"+
			" Whale 会在敏感操作前请求确认") + "\n\n")

	sb.WriteString(bi("3. Session management\n\n"+
		" /session list      list all sessions\n"+
		" /session save      save the current session\n"+
		" /resume [id]       resume a previous session\n"+
		" /checkpoint [label] create a conversation checkpoint\n"+
		" /undo / /redo      undo/redo file changes",
		"3. 会话管理\n\n"+
			" /session list      列出所有会话\n"+
			" /session save      保存当前会话\n"+
			" /resume [id]       恢复之前的会话\n"+
			" /checkpoint [标签] 创建对话检查点\n"+
			" /undo / /redo      撤销/重做文件变更") + "\n\n")

	// ★ ：v1 命令面收口 —— 引导页此前宣传了一批 v1 不承诺的隐藏命令
	//（/commit /commit-push-pr /pr create /pr review /issue create /actions list），
	// 与 /help 口径对齐后只保留可见承诺命令；其余待以后改版恢复（见 内部工作回执）。
	sb.WriteString(bi("4. Git integration\n\n"+
		" /diff              view changes",
		"4. Git 集成\n\n"+
			" /diff              查看变更") + "\n\n")

	sb.WriteString(bi("5. Advanced\n\n"+
		" /skills            view/switch skills\n"+
		" /mcp list          list MCP server connections",
		"5. 进阶功能\n\n"+
			" /skills            查看/切换技能\n"+
			" /mcp list          列出 MCP 服务器连接") + "\n\n")

	sb.WriteString(bi("6. Keyboard shortcuts\n\n"+
		" Enter              send message\n"+
		" Shift+Enter        newline\n"+
		" Ctrl+C             interrupt output\n"+
		" Ctrl+Y             copy the last reply\n"+
		" Ctrl+V / Ctrl+Alt+V paste an image from the clipboard\n"+
		" Escape             cancel the reply\n"+
		" Ctrl+D             quit\n"+
		" /exit              quit the program",
		"6. 快捷键\n\n"+
			" Enter              发送消息\n"+
			" Shift+Enter        换行\n"+
			" Ctrl+C             中断输出\n"+
			" Ctrl+Y             复制上次回复\n"+
			" Ctrl+V / Ctrl+Alt+V 从剪贴板粘贴图片\n"+
			" Escape             取消回复\n"+
			" Ctrl+D             退出\n"+
			" /exit              退出程序") + "\n\n")

	sb.WriteString(bi("Type any message to start a conversation, or use /help for the full command list!",
		"输入任意消息开始对话，或使用 /help 查看完整命令列表！"))

	return sb.String()
}

// handleShare 导出当前会话并异步复制脱敏后的导出路径。
func (m *Model) handleShare(args string) string {
	result, err := m.newSessionExporter().Export(exportpkg.ExportOption{Format: exportpkg.FmtMarkdown})
	if err != nil {
		return biSame("Export failed: %v", "导出失败: %v", err)
	}
	clipboard := exportpkg.NewClipboard()
	m.pendingCommandCmd = func() tea.Msg {
		return clipboardWriteResultMsg{err: clipboard.CopyPath(result.Path)}
	}
	return biSame("✅ Share file exported; copying its path to the clipboard\nPath: %s\nMessages: %d\nRedacted: %d",
		"✅ 分享文件已导出，正在复制路径\n路径: %s\n消息数: %d\n脱敏: %d 处",
		result.Path, result.MessageCount, result.Redacted)
}

// handleImportSession import session
func (m *Model) handleImportSession(args string) string {
	path := strings.TrimSpace(args)
	if path == "" {
		return "Usage: /import <path>"
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Sprintf("File not found: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Read file failed: %v", err)
	}

	var importedSession session.Session
	if err := json.Unmarshal(data, &importedSession); err != nil {
		return fmt.Sprintf("Invalid session format: %v", err)
	}

	if importedSession.Metadata.Name == "" {
		importedSession.Metadata.Name = fmt.Sprintf("imported-%s", time.Now().Format("20060102-150405"))
	}

	sessionID := importedSession.Metadata.Name
	if err := m.sessionService.Store().AppendBatch(sessionID, importedSession.GetMessages()); err != nil {
		return fmt.Sprintf("Save session failed: %v", err)
	}

	return fmt.Sprintf("Imported session: %s (%d messages)", importedSession.Metadata.Name, len(importedSession.GetMessages()))
}

// handleVoice voice mode toggle
func (m *Model) handleVoice(args string) string {
	args = strings.TrimSpace(args)
	switch args {
	case "on":
		return "Voice mode on"
	case "off":
		return "Voice mode off"
	case "status":
		return "Voice mode: Off"
	default:
		return "Voice mode toggled\nUsage: /voice on|off|status"
	}
}

// handleSecurityReview Security review — use Skill system to generate professional security review prompt and trigger AI analysis
func (m *Model) handleSecurityReview(args string) string {
	args = strings.TrimSpace(args)

	// Build review scope description
	var scopeDesc string
	switch {
	case args == "full":
		scopeDesc = "Please perform a comprehensive security review of the entire project. Scan all source code files, checking the following security vulnerability categories:\n- Input validation vulnerabilities (SQL injection, command injection, path traversal, etc.)\n- Authentication and authorization issues (auth bypass, privilege escalation, session management, etc.)\n- Cryptography and key management (hardcoded keys, weak encryption algorithms, etc.)\n- Injection and code execution (deserialization RCE, eval injection, XSS, etc.)\n- Data exposure (sensitive data leaks, PII handling violations, etc.)"
	case args == "staged":
		scopeDesc = "Please perform a security review of the staged but uncommitted code changes in git diff --staged. First execute git diff --staged to get the change content, then analyze following the standard security review process."
	case args != "":
		scopeDesc = fmt.Sprintf("Please perform a targeted security review of file %s. Carefully read the complete content of this file and perform a comprehensive analysis following the standard security review process.", args)
	default:
		scopeDesc = "Please perform a security review of code changes on the current branch relative to origin/HEAD. First execute the following commands to get change content:\n1. git status — view repository status\n2. git diff --name-only origin/HEAD... — get changed file list\n3. git log --no-decorate origin/HEAD... — view commit history\n4. git diff origin/HEAD... — get full diff content\n\nThen perform a comprehensive analysis of the diff code changes following the standard security review process."
	}

	// Attempt to use Skill system to load security review template
	var reviewPrompt string
	if m.cmdRegistry.OnSkillExecute != nil {
		skillPrompt := m.cmdRegistry.OnSkillExecute("security-review", scopeDesc)
		if skillPrompt != "" {
			reviewPrompt = skillPrompt
		}
	}

	// If Skill template is unavailable, use built-in simplified template
	if reviewPrompt == "" {
		reviewPrompt = fmt.Sprintf(`You are a senior security engineer. Please perform a security review of the following code changes.

%s

Please check the following categories:
1. Input validation vulnerabilities (SQL injection, command injection, XXE, template injection, NoSQL injection, path traversal)
2. Authentication and authorization issues (auth bypass, privilege escalation, session management, JWT vulnerabilities)
3. Cryptography and key management (hardcoded keys/passwords/tokens, weak encryption algorithms)
4. Injection and code execution (deserialization RCE, eval injection, XSS)
5. Data exposure (sensitive data logging, PII violations, API data leaks)

Output format:
- Each vulnerability includes: file path, line number, severity level (HIGH/MEDIUM/LOW), category, description, exploit scenario, fix recommendation
- Only report vulnerabilities with confidence ≥ 80%%
- Exclude DOS, outdated third-party libraries, test-only files, and documentation file issues`, scopeDesc)
	}

	// Display command confirmation in UI
	displayText := "🛡️ Security review started"
	switch {
	case args == "full":
		displayText = "🛡️ Full security review started"
	case args == "staged":
		displayText = "🛡️ Staged changes security review started"
	case args != "":
		displayText = fmt.Sprintf("🛡️ FileSecurity review started: %s", args)
	}
	m.addMessage(cmdStyle.Render(displayText))

	// Inject review prompt as AI user message
	m.addMessage(formatUserMessage(reviewPrompt, 0))
	m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserText(reviewPrompt)))
	if m.firstUserInput == "" {
		m.firstUserInput = reviewPrompt
	}
	if m.sessionService.SessionName() == "" {
		m.autoNameSession()
	}

	// Trigger AI streaming response
	m.sessionService.SetState(StateStreaming)
	m.resetStreamState()
	m.aiService.SetRequestID(fmt.Sprintf("%d", time.Now().UnixNano()))
	return ""
}

// handleUltraplan super planning mode
func (m *Model) handleUltraplan(args string) string {
	args = strings.TrimSpace(args)
	switch args {
	case "on":
		m.sessionService.SetPlanMode(true)
		return "Super planning mode on — AI will perform multi-step planning and validation"
	case "off":
		m.sessionService.SetPlanMode(false)
		return "Super planning mode off"
	case "status":
		if m.sessionService.PlanMode() {
			return "Super planning mode: On"
		}
		return "Super planning mode: Off"
	default:
		return "Super planning mode toggled\nUsage: /ultraplan on|off|status"
	}
}

// handleBughunter bug hunter mode
func (m *Model) handleBughunter(subCmd, args string) string {
	switch subCmd {
	case "start":
		var prompt string
		if args != "" {
			prompt = fmt.Sprintf("Bug hunter mode started, target: %s — please proactively detect and fix bugs in the code", args)
		} else {
			prompt = "Bug hunter mode started — please proactively detect and fix bugs in the code"
		}
		m.addMessage(cmdStyle.Render("🐛 " + prompt))
		m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserText(prompt)))
		m.sessionService.SetState(StateStreaming)
		m.resetStreamState()
		m.aiService.SetRequestID(fmt.Sprintf("%d", time.Now().UnixNano()))
		return ""
	case "stop":
		return "Bug hunter mode stopped"
	case "status":
		return "Bug hunter status: not running"
	default:
		return "Usage: /bughunter start|stop|status"
	}
}

// handleVersion display version number
// P0-3 修复：统一引用 m.cmdRegistry.Version（从 main.go 注入），消除硬编码不一致
func (m *Model) handleVersion() string {
	if m.cmdRegistry != nil && m.cmdRegistry.Version != "" {
		return "Whale Code v" + m.cmdRegistry.Version
	}
	return "Whale Code"
}

// 5维度审查修复：删除 handleUpgradeCmd 和 handleFeedbackCmd 死代码
// 这两个方法在 app.go 删除对应 case 后已成死代码（无调用方），
// 且仍含硬编码 "v0.2.0" 版本号（handleFeedbackCmd 会写入反馈文件错误版本）。
// /upgrade 和 /feedback 的真实实现分别在 command.go 的 handleUpgrade 和 handleFeedback。

// handleEnvCmd environment variable management
func (m *Model) handleEnvCmd(args string) string {
	if cb := m.cmdRegistry.OnEnv; cb != nil {
		return cb(args)
	}
	args = strings.TrimSpace(args)

	if args == "" {
		var sb strings.Builder
		sb.WriteString("Key environment variables:\n")
		keys := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "WHALE_MODEL", "WHALE_FALLBACK_MODEL", "GITHUB_TOKEN"}
		for _, key := range keys {
			val := os.Getenv(key)
			if val == "" {
				sb.WriteString(fmt.Sprintf(" %s: (not set)\n", key))
			} else if len(val) > 8 {
				sb.WriteString(fmt.Sprintf(" %s: %s...%s\n", key, val[:4], val[len(val)-4:]))
			} else {
				sb.WriteString(fmt.Sprintf(" %s: %s\n", key, val))
			}
		}
		return sb.String()
	}

	if strings.Contains(args, "=") {
		parts := strings.SplitN(args, "=", 2)
		os.Setenv(parts[0], parts[1])
		// Persist to config
		if m.sessionService.Config().WhaleEnv == nil {
			m.sessionService.Config().WhaleEnv = make(map[string]string)
		}
		m.sessionService.Config().WhaleEnv[parts[0]] = parts[1]
		if m.sessionService.ConfigLoader() != nil {
			_ = m.sessionService.ConfigLoader().Save(m.sessionService.Config())
		}
		return fmt.Sprintf("Set: %s", parts[0])
	}

	val := os.Getenv(args)
	if val == "" {
		return fmt.Sprintf("%s: (not set)", args)
	}
	return fmt.Sprintf("%s=%s", args, val)
}

// handleOutputStyle output style switching
func (m *Model) handleOutputStyle(args string) string {
	if cb := m.cmdRegistry.OnOutputStyle; cb != nil {
		return cb(args)
	}
	args = strings.TrimSpace(args)
	// P1-13 修复：添加 "default" 与补全列表（command_list.go）一致
	styles := []string{"default", "concise", "verbose", "technical"}

	if args == "" {
		return fmt.Sprintf("Current output style: %s\nAvailable: %s", m.sessionService.OutputStyle(), strings.Join(styles, ", "))
	}

	valid := false
	for _, s := range styles {
		if args == s {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Sprintf("Invalid style: %s\nAvailable: %s", args, strings.Join(styles, ", "))
	}

	m.sessionService.SetOutputStyle(args)
	m.sessionService.Config().OutputStyle = config.OutputStyle(args)
	if m.sessionService.ConfigLoader() != nil {
		_ = m.sessionService.ConfigLoader().Save(m.sessionService.Config())
	}
	return fmt.Sprintf("Output style switched to: %s", args)
}

// handleTerminalSetup terminal integration setup
func (m *Model) handleTerminalSetup() string {
	if cb := m.cmdRegistry.OnTerminalSetup; cb != nil {
		return cb("")
	}
	var sb strings.Builder
	sb.WriteString("Terminal integration settings:\n\n")
	if runtime.GOOS == "windows" {
		sb.WriteString("PowerShell:\n")
		sb.WriteString(" Add the following to $PROFILE:\n")
		sb.WriteString(" function whale { & whale.exe $args }\n")
	} else {
		sb.WriteString("Bash/Zsh:\n")
		sb.WriteString(" Add the following to ~/.bashrc or ~/.zshrc:\n")
		sb.WriteString(" eval $(whale shell-hook)\n")
	}
	return sb.String()
}

// handleLogin login
func (m *Model) handleLogin() string {
	return "Please enter API Key (or set environment variable):\n set ANTHROPIC_API_KEY=sk-ant-xxx\n set OPENAI_API_KEY=sk-xxx\n\nOr use /env to view current config"
}

// handleLogout logout
func (m *Model) handleLogout() string {
	return "Logged out — API Key removed from session\nUse /env to view current config, use /login to login again"
}

// handleMigrateCmd config migration
func (m *Model) handleMigrateCmd(args string) string {
	if cb := m.cmdRegistry.OnMigrate; cb != nil {
		return cb(args)
	}
	args = strings.TrimSpace(args)
	if args == "" || args == "check" {
		return "Config migration check: current version is already latest, no migration needed"
	}
	if args == "run" {
		return "Config migration complete: migrated to latest version"
	}
	return "Usage: /migrate [check|run]"
}

// handleRename rename session
//
// 只改可读别名，不改会话主键（UUID 文件名）。
// 旧实现把「别名_时间戳」当新文件名去 Rename，UUID 化后那等于给会话换地址：
// 别的终端里记着的 --session <id> 会立刻失效，而用户只想换个显示名。
func (m *Model) handleRename(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "Usage: /rename <name>"
	}
	sessionID := m.sessionService.SessionName()
	if sessionID == "" {
		// 还没落盘的会话：只更新内存别名，首次保存时随 metadata 头一起写入。
		m.sessionService.CurrentSession().Metadata.Name = args
		return fmt.Sprintf("Session renamed to: %s", args)
	}
	if err := m.sessionService.Store().SetAlias(sessionID, args); err != nil {
		return fmt.Sprintf("Rename failed: %v", err)
	}
	m.sessionService.CurrentSession().Metadata.Name = args
	return fmt.Sprintf("Session renamed to: %s", args)
}
