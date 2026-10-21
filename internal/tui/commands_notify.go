package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/anchorandfish/whale-code/internal/session"
)

// handleNotifyList 处理 /notify list 命令，返回队列+历史通知列表（N24）
func (m *Model) handleNotifyList() string {
	if m.notifier == nil {
		return bi("Notification system is not initialized", "通知系统未初始化")
	}

	// 批 3：整块输出走 bi（英文块 + 缩进中文块），形态 SSOT = util.Bi。
	// 逐行不重复堆双语：块内行只说一种语言（列表行跟随所在块的语言）。
	var en, zh strings.Builder
	en.WriteString("📋 Notification queue:\n")
	zh.WriteString("📋 通知队列：\n")

	queue := m.notifier.List()
	if len(queue) == 0 {
		en.WriteString("  (empty)\n")
		zh.WriteString("  （空）\n")
	} else {
		for _, nf := range queue {
			en.WriteString(formatNotifyLine(nf))
			zh.WriteString(formatNotifyLine(nf))
		}
	}

	current := m.notifier.Current()
	if current != nil {
		en.WriteString("\n🔔 Currently displayed: " + formatNotifyLine(current))
		zh.WriteString("\n🔔 当前显示：" + formatNotifyLine(current))
	}

	history := m.notifier.History()
	en.WriteString(fmt.Sprintf("\n📜 Notification history (%d):\n", len(history)))
	zh.WriteString(fmt.Sprintf("\n📜 通知历史（%d 条）：\n", len(history)))
	if len(history) == 0 {
		en.WriteString("  (empty)\n")
		zh.WriteString("  （空）\n")
	} else {
		limit := 10
		if len(history) < limit {
			limit = len(history)
		}
		for i := 0; i < limit; i++ {
			en.WriteString(formatNotifyLine(history[i]))
			zh.WriteString(formatNotifyLine(history[i]))
		}
		if len(history) > limit {
			en.WriteString(fmt.Sprintf("  ... %d more\n", len(history)-limit))
			zh.WriteString(fmt.Sprintf("  ... 还有 %d 条\n", len(history)-limit))
		}
	}

	return bi(strings.TrimRight(en.String(), "\n"), strings.TrimRight(zh.String(), "\n"))
}

// formatNotifyLine 渲染一条通知列表行：优先级标签 + 标题 + 正文原文 + 折叠计数。
// ★ 标签一律**英文单侧**（`priorityLabel`）：列表行容不下双语辅行，塞中文就会被迫
// 行内并列（形态铁律 §0 明令禁止）；行内其余部分（标题/正文）是数据，原样透传。
func formatNotifyLine(nf *notify.Notification) string {
	line := fmt.Sprintf("  [%s] %s: %s", priorityLabel(nf.Priority), nf.Title, nf.Message)
	if nf.Count > 1 {
		line += fmt.Sprintf(" ×%d", nf.Count)
	}
	return line + "\n"
}

// handleNotifyClear 处理 /notify clear 命令，清空通知历史（N24）
func (m *Model) handleNotifyClear() string {
	if m.notifier == nil {
		return bi("Notification system is not initialized", "通知系统未初始化")
	}
	m.notifier.ClearHistory()
	return bi("Notification history cleared", "通知历史已清空")
}

// handleNotifyDismiss 处理 /notify dismiss 命令，关闭当前通知（N24）
func (m *Model) handleNotifyDismiss() string {
	if m.notifier == nil {
		return bi("Notification system is not initialized", "通知系统未初始化")
	}
	current := m.notifier.Current()
	if current == nil {
		return bi("No active notification", "当前无通知")
	}
	if m.toast != nil {
		m.pendingCommandCmd = m.toast.dismissCurrent()
	} else {
		m.notifier.Dismiss()
	}
	m.recalcViewportHeight()
	return biSame("Notification dismissed: %s", "已关闭通知：%s", current.Title)
}

// priorityLabel 返回优先级的**英文**标签（N24）。
// ★ 形态：本标签用于 `formatNotifyLine` 的单行列表（`[label] title: message`），
// 容不下缩进中文辅行 —— 塞中文就会被迫行内并列（形态铁律 §0 禁止）⇒ 一律英文单侧。
func priorityLabel(p notify.Priority) string {
	switch p {
	case notify.PriorityImmediate:
		return "Urgent"
	case notify.PriorityHigh:
		return "High"
	case notify.PriorityMedium:
		return "Medium"
	case notify.PriorityLow:
		return "Low"
	default:
		return "Unknown"
	}
}

func parseManualCompactConfig(args string, strategy config.CompactionStrategy) (ctxpkg.CompactionConfig, string) {
	cfg := ctxpkg.DefaultCompactionConfig()
	cfg.Strategy = strategy
	cfg.Trigger = ctxpkg.CompactionTriggerManual
	for _, arg := range strings.Fields(args) {
		switch arg {
		case "force":
			// Compatibility alias: an explicit /compact is already a forced run.
		case "--ai":
			cfg.UseAISummary = true
		default:
			return cfg, "Usage: /compact [force] [--ai]"
		}
	}
	return cfg, ""
}

func formatCompactResult(result *ctxpkg.CompactResult, cfg ctxpkg.CompactionConfig) string {
	if result == nil {
		return "Nothing to compact: compaction returned no result"
	}
	if !result.Changed {
		switch result.NoOpReason {
		case ctxpkg.CompactionNoOpInsufficientMessages:
			return fmt.Sprintf(
				"Nothing to compact: %d eligible messages; %s strategy preserves %d recent messages (need at least %d)",
				result.EligibleCount, displayCompactionStrategy(string(cfg.Strategy)), result.PreserveCount, result.PreserveCount+1,
			)
		case ctxpkg.CompactionNoOpWithinBudget:
			return "Nothing to compact: context is already within the automatic compaction budget"
		case ctxpkg.CompactionNoOpCircuitBreaker:
			return "Nothing to compact: the compaction circuit breaker is open"
		case ctxpkg.CompactionNoOpNotBeneficial:
			return "Nothing to compact: the automatic candidate would not reduce estimated context tokens"
		case ctxpkg.CompactionNoOpBudgetUnachievable:
			return fmt.Sprintf("Nothing to compact: no candidate fits the configured ~%d token limit", cfg.MaxEstimatedTokens)
		default:
			return "Nothing to compact: no compressible content was found"
		}
	}

	// 批 3：整块走 bi（英文块 + 缩进中文块）。逐行不做中英并列 ——
	// 旧形态 `Historical messages summarized 历史消息已摘要: …` 是形态铁律 §0 禁止的行内并列。
	var en, zh strings.Builder
	if result.MessagesSummarized > 0 {
		en.WriteString(fmt.Sprintf("Historical messages summarized: %d -> 1 summary", result.MessagesSummarized))
		zh.WriteString(fmt.Sprintf("历史消息已摘要：%d -> 1 条摘要", result.MessagesSummarized))
	} else {
		en.WriteString("Context compacted")
		zh.WriteString("上下文已压缩")
	}
	if result.PreMessageCount > 0 || result.PostMessageCount > 0 {
		en.WriteString(fmt.Sprintf("\nMessages: %d -> %d", result.PreMessageCount, result.PostMessageCount))
		zh.WriteString(fmt.Sprintf("\n消息数：%d -> %d", result.PreMessageCount, result.PostMessageCount))
	}
	delta := result.PostTokens - result.PreTokens
	en.WriteString(fmt.Sprintf("\nEstimated conversation tokens: ~%d -> ~%d", result.PreTokens, result.PostTokens))
	zh.WriteString(fmt.Sprintf("\n估算对话 token：~%d -> ~%d", result.PreTokens, result.PostTokens))
	switch {
	case delta > 0:
		en.WriteString(fmt.Sprintf(" (+%d; context increased)", delta))
		zh.WriteString(fmt.Sprintf("（+%d；上下文增加）", delta))
	case delta < 0:
		en.WriteString(fmt.Sprintf(" (%d; estimated reduction)", delta))
		zh.WriteString(fmt.Sprintf("（%d；估算减少）", delta))
	default:
		en.WriteString(" (no estimated change)")
		zh.WriteString("（无估算变化）")
	}
	// 调试信息：fallback 级别 + 实际执行的压缩阶段
	if result.FallbackLevel != "" {
		en.WriteString(fmt.Sprintf("\nFallback level: %s", result.FallbackLevel))
		zh.WriteString(fmt.Sprintf("\n降级级别：%s", result.FallbackLevel))
	}
	if len(result.StagesExecuted) > 0 {
		stages := strings.Join(result.StagesExecuted, " -> ")
		en.WriteString(fmt.Sprintf("\nStages: %s", stages))
		zh.WriteString(fmt.Sprintf("\n阶段：%s", stages))
	}
	out := bi(en.String(), zh.String())
	if result.Summary != "" {
		out += "\n" + result.Summary
	}
	return out
}

// handleCompact handle compact command
func (m *Model) handleCompact(args string) string {
	// /compact log — 查看本会话压缩历史记录（问题记录 §3 验收配套）。
	// 数据源：compact_boundary 消息上持久化的 CompactMetadata（trigger/pre/post/summarized）。
	if args == "log" || args == "history" {
		return m.handleCompactLog()
	}
	// /compact purge — 删除当前会话的压缩归档（归档式压缩，2026-09-07）。
	if args == "purge" {
		return handleCompactPurgeShared(m.sessionService.Store(), m.sessionService.SessionName())
	}
	return m.handleCompactWithTrigger(args, ctxpkg.CompactionTriggerManual, ctxpkg.CompactionPolicyForce)
}

// handleCompactLog 渲染本会话压缩历史：按时间列出每次压缩的触发方式、
// 压缩前后 token 估算、被摘要消息数，并给出累计次数（与 /context 的已压缩次数对账用）。
func (m *Model) handleCompactLog() string {
	msgs := m.sessionService.CurrentSession().GetMessages()
	type rec struct {
		ts                    int64
		trigger               string
		pre, post, summarized int
		hasTokens, hasSummary bool
	}
	var recs []rec
	for _, msg := range msgs {
		if msg.Subtype != ctxpkg.SubtypeCompactBoundary {
			continue
		}
		if len(msg.Metadata) == 0 {
			continue
		}
		trig, _ := msg.Metadata["compact_trigger"].(string)
		if trig == "" {
			continue
		}
		r := rec{ts: msg.Timestamp, trigger: trig}
		if v, ok := toInt(msg.Metadata["compact_pre_tokens"]); ok {
			r.pre, r.hasTokens = v, true
		}
		if v, ok := toInt(msg.Metadata["compact_post_tokens"]); ok {
			r.post = v
		}
		if v, ok := toInt(msg.Metadata["compact_messages_summarized"]); ok {
			r.summarized = v
		}
		recs = append(recs, r)
	}
	if len(recs) == 0 {
		return bi("📦 Compaction log: no compactions yet in this session", "📦 压缩记录：本会话尚未发生压缩")
	}
	// 批 3：整块走 bi（英文块 + 缩进中文块）。
	var en, zh strings.Builder
	en.WriteString(fmt.Sprintf("📦 Compaction log (%d entries)\n", len(recs)))
	zh.WriteString(fmt.Sprintf("📦 压缩记录（共 %d 次）\n", len(recs)))
	for i, r := range recs {
		ts := ""
		if r.ts > 0 {
			ts = time.Unix(r.ts, 0).Format("01-02 15:04:05")
		}
		if r.hasTokens {
			en.WriteString(fmt.Sprintf("  #%d %s trigger=%s  ~%d -> ~%d tokens, summarized %d messages\n",
				i+1, ts, r.trigger, r.pre, r.post, r.summarized))
			zh.WriteString(fmt.Sprintf("  #%d %s trigger=%s  ~%d -> ~%d tokens，摘要 %d 条消息\n",
				i+1, ts, r.trigger, r.pre, r.post, r.summarized))
		} else {
			en.WriteString(fmt.Sprintf("  #%d %s trigger=%s\n", i+1, ts, r.trigger))
			zh.WriteString(fmt.Sprintf("  #%d %s trigger=%s\n", i+1, ts, r.trigger))
		}
	}
	en.WriteString("Tip: /context shows the current context share and cumulative compaction count")
	zh.WriteString("提示：/context 可对照当前上下文占比与累计压缩次数")
	return bi(strings.TrimRight(en.String(), "\n"), strings.TrimRight(zh.String(), "\n"))
}

// toInt 兼容 JSON 反序列化后 Metadata 中 float64/int 两种数值形态。
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case int64:
		return int(n), true
	}
	return 0, false
}

func (m *Model) handleCompactWithTrigger(args string, trigger ctxpkg.CompactionTrigger, policy ctxpkg.CompactionPolicy) string {
	compactConfig, usage := parseManualCompactConfig(args, m.sessionService.Config().CompactionStrategy)
	if usage != "" {
		return usage
	}
	compactConfig.Trigger = trigger
	budget := m.aiService.Budget()
	staticOverhead, systemOverhead := m.compactRequestOverheads(budget)
	compactConfig = compactConfig.WithBudgetAndOverhead(budget, staticOverhead)
	compactConfig.SystemMessageOverheadTokens = systemOverhead
	expectedVersion, expectedRevision, err := m.captureCompactState()
	if err != nil {
		return fmt.Sprintf("Compaction failed: %v", err)
	}
	costRecorder := newCompactCostRecorder(m.sessionService.ModelName(), m.sessionService.UsageTracker())
	compactSessionEpoch := m.SessionModel.sessionEpoch
	compactModelEpoch := m.SessionModel.modelEpoch
	compactCacheEpoch := cacheCapabilityEpoch(m.aiService.Caching())
	defer m.applyCompactRecorderUsage(costRecorder, compactSessionEpoch, compactModelEpoch, compactCacheEpoch)
	// P0-1 (X1): when WHALE_CTX_PIPELINE is enabled, use the 5-layer
	// CompactionPipeline (Snip→MicroCompact→Collapse→AICompact→CacheAware)
	// instead of the old single-stage MicroCompact + Compact path.
	if ctxpkg.IsPipelineEnabled() && m.aiService.Estimator() != nil && m.aiService.Client() != nil {
		// N11 修复 N1：复用持久 Pipeline，断路器状态跨调用保留（同 runnerCore.Pipeline）。
		if m.pipeline == nil {
			m.pipeline = ctxpkg.NewCompactionPipeline(m.aiService.Estimator(), m.aiService.Client())
			// G3 修复：注入 CachingIntegration，使 compact 后 cache 重建复用 N3 机制
			if m.aiService.Caching() != nil {
				m.pipeline.SetCachingIntegration(m.aiService.Caching())
			}
		}
		// N11 修复 N2：从 CachingIntegration 获取最近一次 API 响应的 CacheInfo，
		// 传入 pipeline 使 CacheAwareMicroCompact 在生产路径生效。
		var cacheInfo *ai.CacheInfo
		if m.aiService.Caching() != nil {
			cacheInfo = m.aiService.Caching().GetCacheInfo()
		}
		pipelineResult := m.pipeline.CompactViaPipelineWithOptions(
			m.sessionService.CurrentSession().GetMessages(),
			m.aiService.Budget(),
			cacheInfo,
			ctxpkg.ExecuteOptions{
				Trigger:      trigger,
				Policy:       policy,
				Config:       compactConfig,
				CostRecorder: costRecorder,
			},
		)
		if !pipelineResult.Changed {
			return formatCompactResult(pipelineResult, compactConfig)
		}
		if err := m.commitCompactResult(pipelineResult, compactConfig, m.pipeline, expectedVersion, expectedRevision); err != nil {
			return fmt.Sprintf("Compaction failed: %v", err)
		}
		if !pipelineResult.Changed {
			return formatCompactResult(pipelineResult, compactConfig)
		}
		// N24 N11 集成：compact 完成后推送通知（Medium 优先级，Key=compact 可 fold 合并）
		if m.notifier != nil {
			m.notifier.Push(notify.NewNotification("compact", "Context Compacted", fmt.Sprintf("Messages: %d -> %d", pipelineResult.PreMessageCount, pipelineResult.PostMessageCount)))
		}
		return formatCompactResult(pipelineResult, compactConfig)
	}

	// Old single-stage path (default, backward-compatible)
	// First perform micro-compression: keep recent tool results, truncate old verbose content
	microConfig := ctxpkg.DefaultMicroCompactConfig()
	originalMessages := m.sessionService.CurrentSession().GetMessages()
	microMessages := ctxpkg.MicroCompact(originalMessages, microConfig)
	microChanged := !ctxpkg.MessagesEqual(originalMessages, microMessages)

	// Main compaction: prefer AI compactor; fallback to heuristic compactor on failure
	var result *ctxpkg.CompactResult
	aiRequestedButUnavailable := compactConfig.UseAISummary && (m.aiService.AICompactor() == nil || m.aiService.Client() == nil)
	if compactConfig.UseAISummary && !aiRequestedButUnavailable {
		result = m.aiService.AICompactor().CompactWithCostRecorder(microMessages, compactConfig, costRecorder)
		// 旧路径：从 AICompactor 取实际 fallback 级别供展示
		result.FallbackLevel = m.aiService.AICompactor().LastFallbackLevel().String()
	} else {
		result = m.aiService.Compactor().Compact(microMessages, compactConfig)
		if aiRequestedButUnavailable {
			result.FallbackLevel = "rule_extract"
		}
	}
	// 旧单阶段路径标记：仅 micro(+可选AI)，未走 5 层 pipeline
	result.StagesExecuted = []string{"micro"}
	if compactConfig.UseAISummary && !aiRequestedButUnavailable {
		result.StagesExecuted = []string{"micro", "ai"}
	}

	result.Changed = result.Changed || microChanged
	if !result.Changed {
		formatted := formatCompactResult(result, compactConfig)
		if aiRequestedButUnavailable {
			return "AI summary unavailable; used deterministic fallback.\n" + formatted
		}
		return formatted
	}

	if err := m.commitCompactResult(result, compactConfig, nil, expectedVersion, expectedRevision); err != nil {
		return fmt.Sprintf("Compaction failed: %v", err)
	}
	if !result.Changed {
		return formatCompactResult(result, compactConfig)
	}

	// N24 N11 集成：compact 完成后推送通知（旧路径）
	if m.notifier != nil {
		m.notifier.Push(notify.NewNotification("compact", "Context Compacted", fmt.Sprintf("Messages: %d -> %d", result.PreMessageCount, result.PostMessageCount)))
	}

	compactMessages := []string{titleStyle.Render(fmt.Sprintf("Compacted: messages %d -> %d", result.PreMessageCount, result.PostMessageCount))}
	// 问题②：压缩完成后检测固定开销是否已超窗口（此时压缩无法缓解，需提示用户增大窗口）
	if fixed, window, exceeded := m.fixedOverheadExceedsWindow(); exceeded {
		compactMessages = append(compactMessages, toolStyle.Render(formatFixedOverheadWarning(fixed, window)))
	}
	m.sessionService.SetMessages(compactMessages)
	for _, msg := range m.sessionService.CurrentSession().GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			m.sessionService.AddMessage(formatUserMessage(msg.TextContent(), msg.Timestamp))
		case session.RoleAssistant:
			display, srcs := m.formatAssistantMessageSource(msg.Blocks, msg.Timestamp)
			m.sessionService.AddMessageWithSources(display, srcs)
		case session.RoleTool:
			// 这里是压缩后重建显示侧的另一条口径：只给 200 rune 预览（与 formatToolResultMessage
			// 的 head+tail 折叠不同）。★ 批 C：**显示形态不动**，但把它也当"省略行"登记真值 ——
			// 否则拖选复制到的是 200 字截断桩（同 家族的第三种长相）。
			line := fmt.Sprintf("📋 %s", truncateRunes(msg.TextContent(), 200))
			full := fmt.Sprintf("📋 %s", msg.TextContent())
			var srcs []lineSource
			if full != line {
				srcs = lineSourcesFromOverrides(1, map[int]string{0: full})
			}
			m.sessionService.AddMessageWithSources(line, srcs)
		}
	}
	m.refreshTranscriptViewport()
	m.uiService.Viewport().GotoBottom()
	formatted := formatCompactResult(result, compactConfig)
	if aiRequestedButUnavailable {
		return "AI summary unavailable; used deterministic fallback.\n" + formatted
	}
	return formatted
}

// handleCompactStrategy switches compaction strategy and persists
func (m *Model) handleCompactStrategy(strategy string) string {
	strategyDescriptions := map[config.CompactionStrategy]string{
		config.CompactBalanced:     "Balanced — Keep recent messages + AI summary",
		config.CompactPreserveUser: "Preserve User — Prioritize user intent",
		config.CompactAggressive:   "Aggressive — Max compression, keep last 3 messages only",
	}

	validStrategies := []config.CompactionStrategy{
		config.CompactBalanced, config.CompactPreserveUser, config.CompactAggressive,
	}

	if strategy == "" {
		desc := strategyDescriptions[m.sessionService.Config().CompactionStrategy]
		return fmt.Sprintf("Current compaction strategy: %s\nDescription: %s\nAvailable: balanced, preserve-user, aggressive", displayCompactionStrategy(string(m.sessionService.Config().CompactionStrategy)), desc)
	}

	var target config.CompactionStrategy
	switch strategy {
	case "balanced":
		target = config.CompactBalanced
	case "preserve-user":
		target = config.CompactPreserveUser
	case "aggressive":
		target = config.CompactAggressive
	default:
		return fmt.Sprintf("Invalid strategy: %s\nAvailable: balanced, preserve-user, aggressive", strategy)
	}

	_ = validStrategies

	m.sessionService.Config().CompactionStrategy = target

	if m.sessionService.ConfigLoader() != nil {
		if err := m.sessionService.ConfigLoader().Save(m.sessionService.Config()); err != nil {
			return fmt.Sprintf("Compaction strategy switched to: %s（persist failed: %v）", displayCompactionStrategy(string(target)), err)
		}
	}

	desc := strategyDescriptions[target]
	return fmt.Sprintf("Compaction strategy switched to: %s（%s）", displayCompactionStrategy(string(target)), desc)
}

// handleCompactPurgeShared /compact purge 的共享实现（TUI 与 plain runner
// 共用，保证两条生产路径行为一致）：删除当前会话的压缩归档 sidecar
// <sessionID>.archive.jsonl。幂等：归档不存在时不报错。
func handleCompactPurgeShared(store *session.Store, sessionName string) string {
	if store == nil {
		return bi("📦 Archive purge failed: session store is not initialized", "📦 归档清理失败：session store 未初始化")
	}
	if sessionName == "" {
		return bi("📦 Archive purge: session is not saved yet (no archive to purge)", "📦 归档清理：当前会话尚未保存（无归档文件可清理）")
	}
	removed, freed, err := store.PurgeArchive(sessionName)
	if err != nil {
		return biSame("📦 Archive purge failed: %v", "📦 归档清理失败：%v", err)
	}
	if !removed {
		return bi("📦 Archive purge: no archive file (no compaction yet, or already purged)", "📦 归档清理：无归档文件（本会话尚未发生压缩或已清理过）")
	}
	return biSame("📦 Archive purge: removed %s.archive.jsonl, freed %d bytes", "📦 归档清理：已删除 %s.archive.jsonl，释放 %d 字节", sessionName, freed)
}
