package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	exportpkg "github.com/anchorandfish/whale-code/internal/export"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/util"
	tea "github.com/charmbracelet/bubbletea"
)

// splitExportArgs 分割导出命令的参数
//
// 使用 strings.Fields 按空白分割，用于解析 /export、/export-user、/export-format
// 等命令的参数（如 --format、--full、filename 等）。
func splitExportArgs(args string) []string {
	return strings.Fields(args)
}

// newSessionExporter 为当前会话创建带默认脱敏器的导出器，避免复用初始化期旧会话指针。
func (m *Model) newSessionExporter() *exportpkg.Exporter {
	return exportpkg.NewExporter(m.sessionService.CurrentSession(), &exportpkg.DefaultRedactor{}, m.sessionService.HomeDir())
}

// handleExportCommand 处理 /export 参数并按指定格式导出当前会话。
func (m *Model) handleExportCommand(args string) string {
	parts := splitExportArgs(args)
	format := exportpkg.FmtMarkdown
	outPath := ""
	for i := 0; i < len(parts); i++ {
		switch {
		case parts[i] == "--format" && i+1 < len(parts):
			format = exportpkg.Format(parts[i+1])
			i++
		case parts[i] == "--full":
		case !strings.HasPrefix(parts[i], "--"):
			outPath = parts[i]
		}
	}
	result, err := m.newSessionExporter().Export(exportpkg.ExportOption{
		Format:     format,
		OutputPath: outPath,
		FullMeta:   strings.Contains(args, "--full"),
	})
	if err != nil {
		return biSame("Export failed: %v", "导出失败: %v", err)
	}
	return biSame("✅ Export succeeded\nFormat: %s\nPath: %s\nMessages: %d\nRedacted: %d",
		"✅ 导出成功\n格式: %s\n路径: %s\n消息数: %d\n脱敏: %d 处",
		result.Format, result.Path, result.MessageCount, result.Redacted)
}

// handleExportUserCommand 仅导出当前会话中的用户消息并执行脱敏。
func (m *Model) handleExportUserCommand(args string) string {
	parts := splitExportArgs(args)
	outPath := ""
	for _, part := range parts {
		if !strings.HasPrefix(part, "--") {
			outPath = part
			break
		}
	}
	result, err := m.newSessionExporter().Export(exportpkg.ExportOption{
		Format:     exportpkg.FmtMarkdown,
		UserOnly:   true,
		OutputPath: outPath,
	})
	if err != nil {
		return biSame("Export failed: %v", "导出失败: %v", err)
	}
	return biSame("✅ Export succeeded (user messages only)\nPath: %s\nMessages: %d\nRedacted: %d",
		"✅ 导出成功（仅用户消息）\n路径: %s\n消息数: %d\n脱敏: %d 处",
		result.Path, result.MessageCount, result.Redacted)
}

// handleSessionList handle session list (deprecated, use handleSessions)
func (m *Model) handleSessionList() string {
	return m.handleSessions()
}

// handleResume restores a previous session
func (m *Model) handleResume(args string) string {
	if m.sessionService.Store() == nil {
		return "Session store not initialized"
	}

	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		// List recent sessions for selection
		summaries, err := m.sessionService.Store().List()
		if err != nil {
			return fmt.Sprintf("Failed to get session list: %v", err)
		}
		if len(summaries) == 0 {
			return "No restorable sessions"
		}

		maxShow := 10
		if len(summaries) < maxShow {
			maxShow = len(summaries)
		}
		shown := summaries[:maxShow]
		// 句柄按**展示出来的这一组**算（：组内撞车时自动加长），
		// 保证这一段里不会出现两个一样的 `[xxxx]`。
		handles := sessionHandles(shown)

		var sb strings.Builder
		sb.WriteString("Restorable sessions (use /resume <ID> to restore):\n")
		for i, s := range shown {
			sb.WriteString(formatSessionLine(i+1, s, handles[s.ID]))
		}
		return sb.String()
	}

	// 点名恢复：统一走 Resolve，接受 UUID / UUID 前缀 / 可读别名 / 旧式文件名。
	// 改造前这里直接把 args 当文件名 Load，别名（与存储键分离后）会被当成
	// 不存在的文件。
	resolved, err := m.sessionService.Store().Resolve(sessionID)
	if err != nil {
		return fmt.Sprintf("Failed to resolve session %q: %v", sessionID, err)
	}

	// Load specified session
	loaded, err := m.sessionService.Store().Load(resolved)
	if err != nil {
		return fmt.Sprintf("Failed to load session: %v", err)
	}

	// D4 修复：切走之前，当前会话必须已落盘（否则最后几轮丢失）。
	<-m.saveSession()

	// 装载动作收敛到 applyResumedSession（与 -c / --session 同一条路径），
	// 本函数只负责「解析 + 落盘旧会话 + 决定提示语」。
	// 装载前会尝试占用该会话；被另一个窗口占着则拒绝并给出出路。
	if err := m.applyResumedSession(resolved, loaded,
		fmt.Sprintf("Session restored: %s (%d turns)", resolved, loaded.Metadata.TurnCount)); err != nil {
		return resumeErrorMessage("Failed to restore session", err)
	}
	return ""
}

// sessionPickerChoiceToolID 是交互式会话选择器的 PendingChoice.ToolID。
//
// 与 permissionModeChoiceToolID / securityPresetChoiceToolID 同类：TUI 本地选择，
// 不进 AskUser 工具链路，由 app.go 的 choiceResultMsg 分支按 ToolID 分派。
const sessionPickerChoiceToolID = "__whale_session_picker__"

// sessionPickerMaxRows 是选择器一次展示的最大会话数。
// 列表按 UpdatedAt 降序，超出部分用 /session list 查看全量。
const sessionPickerMaxRows = 12

// handleSessionsPicker 打开交互式会话选择器（↑↓ 选择 · Enter 载入 · Esc 取消）。
//
// 为什么复用 PendingChoice 而不是自造一套选择 UI：键盘（↑↓/j/k/Enter/Esc）、
// 鼠标点击、渲染、高度计算在既有实现里都已就位且被测试覆盖，复用它等于
// 让会话选择器**免费**获得这些行为，也避免第三份「列表选择」实现（本仓已因
// 双实现漂移吃过亏，见 session_name_slash_test.go）。
//
// 选中后的载入**走 handleSessionSwitch**，与 /session switch <id> 是同一条
// 代码路径 —— 菜单只是输入 id 的另一种方式，不重复实现载入逻辑。
func (m *Model) handleSessionsPicker() string {
	if m.sessionService.Store() == nil {
		return "Session store not initialized"
	}
	summaries, err := m.sessionService.Store().List()
	if err != nil {
		return fmt.Sprintf("Failed to get session list: %v", err)
	}
	if len(summaries) == 0 {
		return "No saved sessions"
	}

	shown := summaries
	if len(shown) > sessionPickerMaxRows {
		shown = shown[:sessionPickerMaxRows]
	}

	current := m.sessionService.SessionName()
	// 与 /session list 同源：句柄按展示出来的这一组算。
	handles := sessionHandles(shown)
	options := make([]ChoiceOption, 0, len(shown))
	selected := 0
	for i, s := range shown {
		name := s.Name
		if name == "" {
			name = s.ID
		}
		timeStr := time.UnixMilli(s.UpdatedAt).Format("01-02 15:04")
		options = append(options, ChoiceOption{
			Label: fmt.Sprintf("%s  [%s]  %d turns · %s", name, handles[s.ID], s.TurnCount, timeStr),
			Value: s.ID,
		})
		if s.ID == current {
			selected = i
		}
	}

	m.toolService.SetPendingChoice(&PendingChoice{
		Prompt:     "Select a session to load",
		Options:    options,
		ToolID:     sessionPickerChoiceToolID,
		DefaultIdx: selected,
		Category:   "session",
	})
	m.toolService.SetChoiceIdx(selected)
	m.sessionService.SetState(StateWaitingChoice)
	m.recalcViewportHeight()
	return biSame("Select a session (↑↓ select · Enter load · Esc cancel)",
		"选择要载入的会话（↑↓ 选择 · Enter 载入 · Esc 取消）") + "\n" +
		bi(fmt.Sprintf("Showing the %d most recent of %d sessions", len(shown), len(summaries)),
			fmt.Sprintf("展示最近 %d 个，共 %d 个会话", len(shown), len(summaries)))
}

// handleSessions list all saved sessions
func (m *Model) handleSessions() string {
	if m.sessionService.Store() == nil {
		return "Session store not initialized"
	}

	summaries, err := m.sessionService.Store().List()
	if err != nil {
		return fmt.Sprintf("Failed to get session list: %v", err)
	}

	if len(summaries) == 0 {
		return "No saved sessions"
	}

	handles := sessionHandles(summaries)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Total %d sessions:\n", len(summaries)))
	for i, s := range summaries {
		sb.WriteString(formatSessionLine(i+1, s, handles[s.ID]))
	}
	sb.WriteString("\nUse /resume <ID or name> to restore a specific session")
	return sb.String()
}

// handleSessionSave handle session save
func (m *Model) handleSessionSave() string {
	if len(m.sessionService.CurrentSession().GetMessages()) == 0 {
		return "Empty session"
	}

	// D4 修复：异步保存，不阻塞主循环（用户主动保存，fire-and-forget）
	_ = m.saveSession()
	return fmt.Sprintf("Saved: %s", m.sessionService.SessionName())
}

// handleExport handle export
func (m *Model) handleExport(filename string) string {
	if filename == "" {
		filename = "whale-export.md"
	}

	if !filepath.IsAbs(filename) {
		filename = filepath.Join(m.sessionService.WorkDir(), filename)
	}

	var sb strings.Builder
	sb.WriteString("# Whale Code Export\n\n")
	for _, msg := range m.sessionService.CurrentSession().GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			sb.WriteString(fmt.Sprintf("## User\n\n%s\n\n", msg.TextContent()))
		case session.RoleAssistant:
			sb.WriteString(fmt.Sprintf("## Assistant\n\n%s\n\n", msg.TextContent()))
		case session.RoleTool:
			for _, b := range msg.Blocks {
				sb.WriteString(fmt.Sprintf("## Tool: %s\n\n```\n%s\n```\n\n", b.ToolName, b.Output))
			}
		}
	}

	if err := util.AtomicWriteFile(filename, []byte(sb.String()), 0644); err != nil {
		return fmt.Sprintf("Export failed: %v", err)
	}
	return fmt.Sprintf("Exported: %s", filename)
}

// handleExportUser handle user message export
func (m *Model) handleExportUser(args string) string {
	timeline := false
	filename := "whale-user-messages.md"

	if args != "" {
		parts := strings.Fields(args)
		for _, p := range parts {
			if p == "--timeline" {
				timeline = true
			} else if !strings.HasPrefix(p, "--") {
				filename = p
			}
		}
	}

	if !filepath.IsAbs(filename) {
		filename = filepath.Join(m.sessionService.WorkDir(), filename)
	}

	var sb strings.Builder
	sb.WriteString("# User Messages Export\n\n")

	for _, msg := range m.sessionService.CurrentSession().GetMessages() {
		if msg.Role == session.RoleUser {
			if timeline {
				t := time.UnixMilli(msg.Timestamp).Format("2006-01-02 15:04:05")
				sb.WriteString(fmt.Sprintf("- [%s] %s\n\n", t, msg.TextContent()))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n\n", msg.TextContent()))
			}
		}
	}

	if err := util.AtomicWriteFile(filename, []byte(sb.String()), 0644); err != nil {
		return fmt.Sprintf("Export failed: %v", err)
	}
	return fmt.Sprintf("Exported: %s", filename)
}

// checkAutoCompact 检查是否需要自动压缩，返回异步压缩的 tea.Cmd（P0-1 修复）。
//
// P0-1（D4-1）修复：将同步 handleCompact 改为异步执行。
// 主循环中仅显示"压缩中..."提示并设置 cooldown，压缩在 goroutine 中执行，
// 完成后通过 autoCompactResultMsg 回写结果到 Update 循环应用。
// goroutine 不直接操作 model，消除数据竞态。
// 返回非 nil tea.Cmd 表示有异步压缩正在进行，调用方应将其 batch 到返回值中。
func (m *Model) checkAutoCompact() tea.Cmd {
	// Do not re-compact during cooldown
	if m.sessionService.AutoCompactCooldown() {
		return nil
	}

	estimated := m.estimateCurrentInputContextTokens()
	budget := m.aiService.Budget()
	// H13-G1: BodyAfterPrefix scope — 获取 cachedInputTokens
	cached := 0
	if tracker := m.sessionService.UsageTracker(); tracker != nil {
		if usage, ok := tracker.GetRecentModelUsage(m.sessionService.ModelName()); ok {
			cached = usage.CacheRead
			if cached > estimated && estimated > 0 {
				cached = 0
			}
		}
	}
	state := budget.CheckState(estimated, cached)
	defer func() { m.lastContextState = state }()
	percent := budget.UsagePercent(estimated)

	// P1 预警（问题记录 §3）：状态首次进入 Warning（默认 75%）时提示一次，
	// 仅提醒不执行，给用户留出 /copy、/clear 等提前处理的窗口。
	// 门控见 shouldShowApproachingWarning：直跳 ShouldCompact/Blocked 时跳过（"接近"文案误导）。
	if shouldShowApproachingWarning(state, m.lastContextState) {
		m.addMessage(toolStyle.Render(fmt.Sprintf(
			"⚠️ Context at %.0f%% 上下文接近压缩阈值 Context approaching compact threshold\n"+
				"  可提前处理 options: /compact --ai (AI 摘要) · /copy (导出对话) · /clear (新会话)",
			percent)))
	}

	// 97% 兜底：只做硬停 + 最小交接模板，不再尝试压缩（窗口已满时压缩请求
	// 本身可能撑爆/截断，教训：8000 token 窗口压缩失败死锁）。
	if state == ctxpkg.StateBlocked {
		m.addMessage(errorStyle.Render(blockedHandoffMessage(
			percent, m.sessionService.FirstUserInput(), m.sessionService.CurrentSession().GetMessages())))
		return nil
	}

	if autoCompactDue(m.sessionService.Config(), budget, estimated, cached) {
		// 主力线（window-buffer，128K 下 ≈89.8%）触发即自动 AI 压缩，
		// 此时仍有余量做 AI 摘要（保语义、SECRET-CODE 不丢）。
		m.addMessage(toolStyle.Render(fmt.Sprintf(
			"⚡ Context at %.0f%%, auto-compacting with AI summary (threshold reached)...", percent)))
		m.sessionService.SetAutoCompactCooldown(true)
		return m.startAsyncCompact("--ai")
	} else if state == ctxpkg.StateShouldCompact {
		// 0.80 提示线（用户决策窗口）：真实决策机会，主力自动压缩线之前
		m.addMessage(toolStyle.Render(fmt.Sprintf(
			"[!] Context at %.0f%%（决策窗口）：可 /compact 提前压缩，或继续（约 %.0f%% 将自动压缩）",
			percent, budget.MainLinePercent())))
	}
	return nil
}

// shouldShowApproachingWarning 报告是否显示"接近压缩阈值"一次性预警（问题①）。
// 仅在恰好处于 Warning 档且此前未达 Warning 时提示；直接跳到 ShouldCompact/Blocked
// 时跳过——此时"接近（approaching）"文案具有误导性（如固定开销超窗口导致 4127% 直跳
// Blocked），后续分支会直接宣布压缩动作。
func shouldShowApproachingWarning(state, lastState ctxpkg.State) bool {
	return state == ctxpkg.StateWarning && lastState < ctxpkg.StateWarning
}

// fixedOverheadExceedsWindow 报告固定开销（静态 system prompt + 工具定义）是否已达到
// context_window（问题②）。压缩只能作用于对话消息，fixed >= window 时压缩无法缓解，
// 每轮对话都会重复触发 Blocked。快照无效（尚未发请求）或窗口未配置时返回 false。
func (m *Model) fixedOverheadExceedsWindow() (fixed, window int, exceeded bool) {
	snapshot := m.SessionModel.requestSnapshot
	if !snapshot.Valid {
		return 0, 0, false
	}
	window = m.aiService.Budget().ContextWindow
	if window <= 0 {
		return 0, 0, false
	}
	fixed = fixedRequestOverhead(snapshot)
	return fixed, window, fixed >= window
}

// startAsyncCompact 在主循环中捕获压缩所需的输入，返回在 goroutine 中执行压缩的 tea.Cmd。
//
// P0-1（D4-1）修复：所有 model 状态读取在主循环中完成（捕获到闭包变量），
// goroutine 仅使用捕获的输入调用 pipeline/compactor（不操作 model），
// 压缩结果通过 autoCompactResultMsg 在 Update 循环中应用到 model。
// 保持现有压缩逻辑（CompactViaPipeline / Compactor.Compact）不变，只改调用方式。
func (m *Model) startAsyncCompact(args string) tea.Cmd {
	capturedRequestID := m.aiService.RequestID()
	capturedSessionEpoch := m.SessionModel.sessionEpoch
	capturedModelEpoch := m.SessionModel.modelEpoch
	capturedCacheEpoch := cacheCapabilityEpoch(m.aiService.Caching())
	expectedVersion, expectedRevision, captureErr := m.captureCompactState()
	if captureErr != nil {
		return func() tea.Msg {
			return autoCompactResultMsg{
				err: captureErr, requestID: capturedRequestID,
				sessionEpoch: capturedSessionEpoch, modelEpoch: capturedModelEpoch, cacheEpoch: capturedCacheEpoch,
				expectedRevision: expectedRevision,
			}
		}
	}
	compactConfig := ctxpkg.DefaultCompactionConfig()
	if args == "--ai" {
		compactConfig.UseAISummary = true
	}
	compactConfig.Strategy = m.sessionService.Config().CompactionStrategy

	// 在主循环中捕获消息快照（避免 goroutine 中读取 model）
	messages := m.sessionService.CurrentSession().GetMessages()
	budget := m.aiService.Budget()
	staticOverhead, systemOverhead := m.compactRequestOverheads(budget)
	compactConfig = compactConfig.WithBudgetAndOverhead(budget, staticOverhead)
	compactConfig.SystemMessageOverheadTokens = systemOverhead
	costRecorder := newCompactCostRecorder(m.sessionService.ModelName(), m.sessionService.UsageTracker())
	finishMsg := func(msg autoCompactResultMsg) autoCompactResultMsg {
		msg.requestID = capturedRequestID
		msg.sessionEpoch = capturedSessionEpoch
		msg.modelEpoch = capturedModelEpoch
		msg.cacheEpoch = capturedCacheEpoch
		msg.expectedVersion = expectedVersion
		msg.expectedRevision = expectedRevision
		msg.compactUsage, msg.hasCompactUsage = costRecorder.TotalUsage()
		return msg
	}
	// Pipeline 路径（WHALE_CTX_PIPELINE 启用时，可能调用 AI，是 P0-1 主要修复目标）
	if ctxpkg.IsPipelineEnabled() && m.aiService.Estimator() != nil && m.aiService.Client() != nil {
		if m.pipeline == nil {
			m.pipeline = ctxpkg.NewCompactionPipeline(m.aiService.Estimator(), m.aiService.Client())
			if m.aiService.Caching() != nil {
				m.pipeline.SetCachingIntegration(m.aiService.Caching())
			}
		}
		pipeline := m.pipeline
		var cacheInfo *ai.CacheInfo
		if m.aiService.Caching() != nil {
			cacheInfo = m.aiService.Caching().GetCacheInfo()
		}

		return func() tea.Msg {
			// goroutine 中执行压缩（仅使用捕获的输入，不操作 model）
			result := pipeline.CompactViaPipelineWithOptions(
				messages,
				budget,
				cacheInfo,
				ctxpkg.ExecuteOptions{
					Trigger:      ctxpkg.CompactionTriggerAuto,
					Policy:       ctxpkg.CompactionPolicyBudgeted,
					Config:       compactConfig,
					CostRecorder: costRecorder,
				},
			)
			if !result.Changed {
				return finishMsg(autoCompactResultMsg{nothingToCompact: true})
			}
			return finishMsg(autoCompactResultMsg{result: result, config: compactConfig, pipeline: pipeline})
		}
	}

	// 旧路径（无 pipeline 时）：heuristic 压缩，通常很快但为一致性也异步执行
	compactor := m.aiService.Compactor()
	return func() tea.Msg {
		microConfig := ctxpkg.DefaultMicroCompactConfig()
		microCompacted := ctxpkg.MicroCompact(messages, microConfig)
		microChanged := !ctxpkg.MessagesEqual(messages, microCompacted)
		result := compactor.Compact(microCompacted, compactConfig)
		result.Changed = result.Changed || microChanged
		if !result.Changed {
			return finishMsg(autoCompactResultMsg{nothingToCompact: true})
		}
		return finishMsg(autoCompactResultMsg{result: result, config: compactConfig})
	}
}

// cleanupOnExit cleanup on exit - remove empty sessions
// 补全三大项：先中断流/工具（cancel）→ 关闭 AI 客户端（Close）→ 持久化文件缓存（Persist），
// 再执行原有清理逻辑。cancel 先执行避免 Close 时流仍在写入。
func (m *Model) cleanupOnExit() {
	// 0. A-5：先解开 monitor done 的阻塞发送逃生口，再 StopAll。
	// 顺序不能颠倒：StopAll 会终止所有后台进程 ⇒ 每个 waitProcess goroutine
	// 都会发一条 done（现在是不丢的阻塞发送）；此时 TUI 主循环即将退出、
	// 不再有人读 monitorCh ⇒ 若不先开逃生口，这些 goroutine 会永久阻塞在发送上，
	// waitProcess 的 defer 归还并发令牌也就永远执行不到。
	m.toolService.MonitorEscape().close()
	// 1. 中断进行中的 AI 流与工具执行（streamCancel/toolCancel 此前为死字段，此处接通）
	if m.aiService.StreamCancel() != nil {
		m.aiService.StreamCancel()()
		m.aiService.SetStreamCancel(nil)
	}
	if m.toolService.ToolCancel() != nil {
		m.toolService.ToolCancel()()
		m.toolService.SetToolCancel(nil)
	}
	// 2. 关闭 AI 客户端，释放底层 HTTP 连接（Close 定义于 internal/ai/provider.go）
	if m.aiService.Client() != nil {
		_ = m.aiService.Client().Close()
	}
	// 3. 持久化文件缓存元数据，避免下次启动重复读取
	if m.toolService.Executor() != nil && m.toolService.Executor().FileCache != nil {
		_ = m.toolService.Executor().FileCache.Persist()
	}

	if m.toolService.MonitorMgr() != nil {
		m.toolService.MonitorMgr().StopAll()
	}
	if m.toolService.LoopScheduler() != nil {
		m.toolService.LoopScheduler().Stop()
	}
	// The daemon is a standalone process controlled over IPC. Exiting the TUI
	// must not terminate background tasks.
	if m.sessionService.UsageTracker() != nil {
		_ = m.sessionService.UsageTracker().Close()
	}
	if m.sessionService.SessionName() != "" && m.sessionService.FirstUserInput() == "" {
		m.sessionService.Store().Delete(m.sessionService.SessionName())
		m.sessionService.SetSessionName("")
	} else if m.sessionService.SessionName() != "" {
		// D4 修复：同步等待保存完成，确保退出前会话已落盘
		<-m.saveSession()
	}
}

// autoNameSession 给会话打上身份：UUID 主键 + 从首条输入派生的可读别名。
//
// 与 runner 路径（runner_core.go autoNameSession）共用 makeSessionIdentity，
// 两条路径的规则不再各写一份（历史漂移见 session_name_slash_test.go 注释）。
//
// 幂等守卫：已有身份就返回。**这不是可省的防御** —— 会话主键是 UUID，每次调用都会
// 铸出一个**新**的 UUID，重复调用会把刚写下的会话文件变成孤儿（下一次 save 落到
// 新文件、旧文件再也不会被续写）。改造前名字是「输入+时间戳」，重复调用至少还
// 收敛到同一个名字，所以这个守卫是 UUID 化**引入的新前提**。
// 全部 12 个现有调用点都已有 `if SessionName() == ""` 外判；守卫把该前提从
// 「调用方必须记得」变成「结构上不可能违反」。
func (m *Model) autoNameSession() {
	if m.sessionService.SessionName() != "" {
		return
	}
	id, alias := makeSessionIdentity(m.sessionService.FirstUserInput())
	if id == "" {
		return
	}
	m.sessionService.SetSessionName(id)
	m.sessionService.CurrentSession().Metadata.Name = alias
}

// saveSession 增量保存当前会话（D4 修复 2026-07-31：异步化 + saveMu 保护）。
//
// 返回 done channel：调用方可选择忽略（异步 fire-and-forget）或 <-done 同步等待。
// - 主循环 turn 结束（stream.go）、handleSessionSave（用户主动保存）：忽略 done，异步执行。
// - handleResume/handleSessionSwitch/cleanupOnExit：<-done 同步等待，确保切换/退出前保存完成。
//
// 并发安全：
// - saveMu *sync.Mutex 保护 SavedMsgCount 读-改-写区间（NewModel 初始化为 &sync.Mutex{}）。
// - **所有字段快照在启动 goroutine 之前完成**（svc/mu），goroutine 内只碰局部变量。
// 理由：Bubble Tea 主循环在 goroutine 运行期间仍会继续 Update 并调用 reassignServices
// 改写 m.sessionService / m.uiService 等字段 —— 旧实现在 goroutine 里读这些字段，
// -race 实测 "saveSession.func1 读 vs reassignServices 写"。
// - Store.mu 已保护 AppendBatch，无需重复加锁。
//
// 降级策略：异步失败时通过 addMessage 提示用户（非阻塞，不影响主循环）。
func (m *Model) saveSession() <-chan struct{} {
	// ★ 字段快照必须在 goroutine 之前：接口字段会被 reassignServices 重新赋值。
	svc := m.sessionService
	mu := m.saveMu
	if mu == nil {
		// 懒初始化放在调用方 goroutine 里（原实现放在 goroutine 内会与主循环竞争写字段）。
		mu = &sync.Mutex{}
		m.saveMu = mu
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// saveMu 保护整个读-改-写区间，防止多个 saveSession goroutine 并发写入。
		mu.Lock()
		defer mu.Unlock()

		// 快照所有依赖，消除 handleSessionSwitch 的 SessionName 竞态
		name := svc.SessionName()
		// 别名随消息一起持久化（写入会话文件首行的 session_metadata 记录）。
		// 之所以在这里读而不是只在命名时读：autoNameSession 常常已经先跑过，
		// 别名此刻在 Metadata 上而非局部变量里。
		alias := svc.CurrentSession().Metadata.Name
		if name == "" {
			// autoNameSession 依赖 FirstUserInput，快照后命名。
			// 逻辑与 autoNameSession 共用 makeSessionIdentity，不再各写一份。
			id, derived := makeSessionIdentity(svc.FirstUserInput())
			if id == "" {
				id = session.GenerateID()
			}
			name = id
			svc.SetSessionName(name)
			if derived != "" {
				alias = derived
				svc.CurrentSession().Metadata.Name = derived
			}
		}

		msgs := svc.CurrentSession().GetMessages()
		saved := svc.SavedMsgCount()
		store := svc.Store()

		if store == nil {
			return
		}

		if saved < len(msgs) {
			pending := msgs[saved:]
			if len(pending) > 0 {
				if err := store.AppendBatchNamed(name, alias, pending); err != nil {
					m.addMessage(errorStyle.Render(fmt.Sprintf("Save failed: %v", err)))
					return
				}
				svc.SetSavedMsgCount(len(msgs))
			}
		}
	}()
	return done
}

// resumeErrorMessage 把会话装载失败渲染成用户可读文案。
//
// 占用冲突用错误自带的 UserMessage（**唯一实现**，与 package main 的启动路径
// 共用同一份文案 —— 同一事实跨入口必须同源）；其余失败（解析/加载/权限）保留
// 调用方给的动词前缀 + 原始错误，那是诊断信息，不该被措辞抹掉。
func resumeErrorMessage(prefix string, err error) string {
	var inUse *session.SessionInUseError
	if errors.As(err, &inUse) {
		return inUse.UserMessage()
	}
	return fmt.Sprintf("%s: %v", prefix, err)
}

// handleSessionSwitch switch to a saved session
func (m *Model) handleSessionSwitch(args string) string {
	if args == "" {
		return "Usage: /session switch <session-id>"
	}
	// 统一走 Store.Resolve（完整 UUID / UUID 前缀 / 可读别名 / 旧式文件名）。
	//
	// 改造前这里是「遍历 summaries 比 s.Name，再拿原样的 args 去 Load」：
	// 别名与存储键分离后，这种写法会把别名当文件名用，且它自己那份匹配规则
	// 与 /resume、--session 各写一份 —— 现在三者共用同一个解析器。
	sessionID, err := m.sessionService.Store().Resolve(args)
	if err != nil {
		return fmt.Sprintf("%v\nUse /session list to see available sessions", err)
	}

	// D4 修复：同步等待保存完成，确保切换前旧会话已落盘
	<-m.saveSession()

	loaded, err := m.sessionService.Store().Load(sessionID)
	if err != nil {
		return fmt.Sprintf("Load failed: %v", err)
	}

	// 装载动作收敛到 applyResumedSession（与 -c / --session / /resume 同一路径）。
	// 被另一个窗口占用时不切换，并把出路回给用户。
	if err := m.applyResumedSession(sessionID, loaded,
		fmt.Sprintf("Switched to session: %s (%d messages)", sessionID, len(loaded.GetMessages()))); err != nil {
		return resumeErrorMessage("Switch failed", err)
	}
	return ""
}

// handleSessionMerge merge two sessions
func (m *Model) handleSessionMerge(args string) string {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return "Usage: /session merge <id1> <id2>"
	}
	id1, id2 := parts[0], parts[1]
	// 两个入参都先解析（接受别名/短码），解析失败必须报错而不是把别名当文件名。
	r1, err := m.sessionService.Store().Resolve(id1)
	if err != nil {
		return fmt.Sprintf("Resolve failed for %q: %v", id1, err)
	}
	r2, err := m.sessionService.Store().Resolve(id2)
	if err != nil {
		return fmt.Sprintf("Resolve failed for %q: %v", id2, err)
	}
	sess1, err1 := m.sessionService.Store().Load(r1)
	sess2, err2 := m.sessionService.Store().Load(r2)
	if err1 != nil || err2 != nil {
		return fmt.Sprintf("Load failed: id1=%v, id2=%v", err1, err2)
	}
	mergedID := session.GenerateID()
	merged := session.Merge([]*session.Session{sess1, sess2}, mergedID)
	if err := m.sessionService.Store().Save(mergedID, merged); err != nil {
		return fmt.Sprintf("Save failed: %v", err)
	}
	return fmt.Sprintf("Merged:\n %s (%d msgs)\n %s (%d msgs)\n-> %s (%d msgs)",
		id1, len(sess1.GetMessages()), id2, len(sess2.GetMessages()), mergedID, len(merged.GetMessages()))
}

// handleSessionSplit cut current session
func (m *Model) handleSessionSplit(args string) string {
	if len(m.sessionService.CurrentSession().GetMessages()) < 2 {
		return "Not enough session messages to split (at least 2 messages required)"
	}

	splitIndex := len(m.sessionService.CurrentSession().GetMessages()) / 2
	if args != "" {
		if idx, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && idx > 0 && idx < len(m.sessionService.CurrentSession().GetMessages()) {
			splitIndex = idx
		} else {
			return fmt.Sprintf("Invalid split position: %s\nPlease enter a valid message index (1-%d)", args, len(m.sessionService.CurrentSession().GetMessages())-1)
		}
	}

	result := session.SplitAt(m.sessionService.CurrentSession(), []int{splitIndex})
	if len(result.Sessions) != 2 {
		return "Split failed, please retry"
	}

	originalName := m.sessionService.SessionName()
	if originalName == "" {
		originalName = "session"
	}

	newName := fmt.Sprintf("%s-split-%d", originalName, time.Now().Unix())

	// P0-8/9 新增：会话切换时清空排队队列（避免跨会话污染）
	m.ClearQueuedInputs()

	m.sessionService.SetCurrentSession(result.Sessions[0])
	m.sessionService.CurrentSession().Metadata.Name = originalName

	_ = m.sessionService.Store().Save(m.sessionService.SessionName(), result.Sessions[0])

	newSessionID := newName
	result.Sessions[1].Metadata.Name = newName
	_ = m.sessionService.Store().Save(newSessionID, result.Sessions[1])

	m.sessionService.SetSavedMsgCount(len(m.sessionService.CurrentSession().GetMessages()))

	return fmt.Sprintf("✂️ Session split\n Original session: %s (%d messages)\n New session: %s (%d messages)\n Use /session switch %s to switch to new session",
		originalName, len(result.Sessions[0].GetMessages()), newName, len(result.Sessions[1].GetMessages()), newSessionID)
}

// handleExportFormat 按显式格式导出当前会话并执行脱敏。
func (m *Model) handleExportFormat(args string) string {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "Usage: /export-format json|markdown|html [path]"
	}

	format := exportpkg.Format(fields[0])
	path := ""
	if len(fields) > 1 {
		path = fields[1]
	}
	if format != exportpkg.FmtJSON && format != exportpkg.FmtMarkdown && format != exportpkg.FmtHTML {
		return fmt.Sprintf("Invalid format: %s\nSupported formats: json, markdown, html", fields[0])
	}
	result, err := m.newSessionExporter().Export(exportpkg.ExportOption{
		Format:     format,
		OutputPath: path,
		FullMeta:   strings.Contains(args, "--full"),
	})
	if err != nil {
		return biSame("Export failed: %v", "导出失败: %v", err)
	}
	return biSame("✅ Export succeeded\nFormat: %s\nPath: %s\nMessages: %d\nRedacted: %d",
		"✅ 导出成功\n格式: %s\n路径: %s\n消息数: %d\n脱敏: %d 处",
		result.Format, result.Path, result.MessageCount, result.Redacted)
}
