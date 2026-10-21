package tui

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/mention"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/secret"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/typeahead"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// streamingText 返回当前流式文本的唯一生产读取源。
// 正常运行读取 ActiveCell；仅在未初始化 ActiveCell 的测试或降级场景读取 SessionModel。
func (m *Model) streamingText() string {
	if m.activeCell != nil {
		return m.activeCell.Text()
	}
	return m.sessionService.CurrentText()
}

// streamingThinking 返回当前流式思考内容的唯一生产读取源。
// 正常运行读取 ActiveCell；仅在未初始化 ActiveCell 的测试或降级场景读取 SessionModel。
func (m *Model) streamingThinking() string {
	if m.activeCell != nil {
		return m.activeCell.Thinking()
	}
	return m.sessionService.CurrentThinking()
}

// isStaleStreamRequest 判断异步流消息是否属于已经取消或被新请求替代的旧请求。
// 空 requestID 仅用于兼容既有单元测试和降级调用；生产流消息始终携带 requestID。
func (m *Model) isStaleStreamRequest(requestID string) bool {
	return requestID != "" && requestID != m.aiService.RequestID()
}

// stopExecution 取消当前 AI 流或工具执行，并保留已经显示的部分文本。
func (m Model) stopExecution() (tea.Model, tea.Cmd) {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	previousState := m.sessionService.State()
	if previousState == StateIdle {
		return m, nil
	}
	stoppedAt := time.Now()
	currentText := strings.TrimSpace(m.streamingText())
	hadUnterminatedFence := strings.Count(currentText, "```")%2 != 0
	pendingTools := append([]session.ContentBlock(nil), m.toolService.PendingTools()...)

	m.sessionService.SetState(StateIdle)
	// v1.2：resize 已改为完全同步（无 debounce），无需使 pending debounce 失效
	m.uiService.SetInputFocusedOnStop(false)
	m.uiService.SetInputFocusedOnSend(false)
	m.uiService.Textarea().Focus()
	// Usage already reported before cancellation remains a billing fact. Finalize
	// it before clearing request identity; late provider frames may never arrive
	// after the stream context is cancelled.
	m.finalizeRequestUsage(m.aiService.RequestID())
	m.aiService.SetRequestID("")
	if m.aiService.StreamCancel() != nil {
		m.aiService.StreamCancel()()
		m.aiService.SetStreamCancel(nil)
	}
	if m.toolService.ToolCancel() != nil {
		m.toolService.ToolCancel()()
		m.toolService.SetToolCancel(nil)
	}
	// Close any persisted assistant tool_use blocks before the next request.
	// Without matching tool_result messages, providers can reject the history.
	if previousState == StateToolExec && len(pendingTools) > 0 {
		msgs := m.sessionService.CurrentSession().GetMessages()
		for _, toolCall := range pendingTools {
			msgs = append(msgs, session.ToolResult(toolCall.ID, toolCall.Name,
				"Tool execution cancelled by user", true))
		}
		m.sessionService.CurrentSession().SetMessages(msgs)
	}

	// Persist an assistant-side terminal message so the stopped user prompt is
	// a completed turn. The next request therefore answers only the new input.
	contextText := "[Response interrupted by user before output]"
	if currentText != "" {
		currentText = closeUnterminatedMarkdownFence(currentText)
		contextText = currentText + "\n\n[Response interrupted by user]"
	}
	m.sessionService.CurrentSession().SetMessages(append(
		m.sessionService.CurrentSession().GetMessages(),
		session.AssistantText(contextText, m.sessionService.ModelName(), nil),
	))
	m.finishTurn(false)

	if currentText != "" {
		displayText := currentText
		if hadUnterminatedFence {
			// 宽度口径与定稿路径一致（contentWrapWidth）。
			displayText = renderTextContentWidth(currentText, m.contentWrapWidth())
		}
		m.addMessage(assistantStyle.Render("🐋 Whale: ") + displayText)
	}
	m.resetStreamState()
	m.toolService.SetPendingChoice(nil)
	m.toolService.SetPendingConfirm(nil)
	// 标记中断状态：handleStreamDone/handleStreamEvent 检查此标志，
	// 若为 true 则跳过后续事件处理，确保 stop 后不再输出 AI 残留结果。
	m.sessionService.SetInterrupted(true)
	// 失效流式渲染缓存（addMessage 改变了消息数量，缓存计数已过期）
	m.invalidateStreamHistoryCache()
	// 2026-10-07（用户拍板）：中断轮不再单独打印 `Stopped │ … │ interrupted` 行，
	// 改由轮末结束标志承担（符号 ⏹ + 完整年月日时刻）—— 每轮只有一个结束标志。
	// 落痕必须在此处（部分输出显示消息已入缓冲之后）：closeTurn 的 after 锚点 =
	// 显示缓冲末尾，early 调用会把标志错插到部分输出之前。
	m.closeTurn(stoppedAt, true)
	m.recalcViewportHeight()

	// P0-8/9 建议优化 #2：Ctrl+C 中断后也触发 drain 钩子。
	// 中断时 State 已设为 Idle，若队列中有排队消息，自动 drain 提交下一轮。
	// 依赖致命漏洞 #1 修复（drain 处理器会检查 State == Idle），此时 State 已设为 Idle，可安全 drain。
	if m.HasQueuedInputs() {
		// Keep temporary mouse capture enabled; the queued input immediately
		// starts another active turn and still needs a clickable Stop button.
		return m, func() tea.Msg { return drainQueuedInputMsg{} }
	}
	return m, m.restoreIdleMouse()
}

// sendInput 校验并发送当前输入框内容。
func (m Model) sendInput() (tea.Model, tea.Cmd) {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	// N5 typeahead：发送输入时清空补全弹窗
	if m.suggestionState != nil {
		m.suggestionState.Clear()
	}
	// 发送后 textarea 内容将被 Reset，选区锚点随之失效，显式清除选区。
	m.clearComposerSelection()
	if m.sessionService.State() != StateIdle {
		m.uiService.SetInputFocusedOnSend(false)
		m.uiService.SetInputFocusedOnStop(false)
		m.uiService.Textarea().Focus()
		return m, nil
	}
	rawInput := m.uiService.Textarea().Value()
	if utf8.RuneCountInString(rawInput) > maxInputRunes {
		m.showInputLimitWarning()
		return m, nil
	}
	input := strings.TrimSpace(rawInput)
	if input == "" {
		m.uiService.SetInputFocusedOnSend(false)
		m.uiService.SetInputFocusedOnStop(false)
		m.uiService.Textarea().Focus()
		return m, nil
	}
	// V3-6-3：检测用户是否输入强制执行确认词（拒绝覆盖机制）。
	// 用户输入"我确认强制执行"或"force execute"时，启用强制执行模式。
	// 后续 AI 请求的 system prompt 将注入强制执行指令，AI 不再拒绝执行。
	// 不发送此确认词到 AI，仅设置模式并提示用户重新输入请求。
	if m.refusalHandler != nil && m.refusalHandler.IsForceExecuteConfirm(input) {
		m.refusalHandler.SetForceExecuteMode(true)
		m.addMessage(formatUserMessage(input, time.Now().UnixMilli()))
		m.addMessage(toolStyle.Render("✅ Force confirmation recorded / 已记录强制确认。It only affects AI wording; permissions and sandbox still apply. / 仅影响 AI 的拒绝措辞，权限和沙箱仍然有效。Please resubmit your request / 请重新提交请求。"))
		m.uiService.SetInputFocusedOnSend(false)
		m.uiService.SetInputFocusedOnStop(false)
		m.uiService.Textarea().Reset()
		m.uiService.Textarea().SetHeight(3)
		m.uiService.Textarea().Focus()
		m.recalcViewportHeight()
		return m, nil
	}
	// T3 S4: 用户发送新消息时重置提问计数（REF-T3-新增，每条消息最多 ask_threshold 次提问）。
	// askCount 在 checkAskUserChoice 命中时 +1，重置后新一轮消息可重新提问 ask_threshold 次。
	m.askCount = 0
	m.uiService.SetInputFocusedOnSend(false)
	m.uiService.SetInputFocusedOnStop(false)
	m.uiService.Textarea().Reset()
	m.uiService.Textarea().SetHeight(3)
	m.uiService.Textarea().Focus()
	m.recalcViewportHeight()

	m.sessionService.SetInputHistory(append(m.sessionService.InputHistory(), input))
	// H8：InputHistoryLimit 从 Config 读取（原硬编码 500，<=0 时 fallback 到 500）
	historyLimit := 500
	if m.config.InputHistoryLimit > 0 {
		historyLimit = m.config.InputHistoryLimit
	}
	if len(m.sessionService.InputHistory()) > historyLimit {
		m.sessionService.SetInputHistory(m.sessionService.InputHistory()[len(m.sessionService.InputHistory())-historyLimit:])
	}
	m.sessionService.SetHistoryIdx(len(m.sessionService.InputHistory()))
	m.sessionService.SetHistoryDraft("")

	var cmds []tea.Cmd
	// 命令判定走唯一谓词 command.IsCommandInput（不再裸 HasPrefix）。
	// ★ 传 rawInput（未 TrimSpace）—— 前导空白是**正式逃逸语义**（` /permissions` 是普通文本），
	// 故判据必须在上面 TrimSpace **之前**取值；传 input 会把逃逸吃掉、与 --plain 侧打架。
	if command.IsCommandInput(rawInput) {
		m.addMessage(cmdStyle.Render("❯ " + input))
		cmdOutput := m.handleCommand(input)
		if m.pendingCommandCmd != nil {
			cmds = append(cmds, m.pendingCommandCmd)
			m.pendingCommandCmd = nil
		}
		if cmdOutput == "__SKILL_PROMPT__" {
			// Prompt Skill command: prompt injected as user message, start AI stream
			m.sessionService.SetState(StateStreaming)
			m.resetStreamState()
			m.resetAIDetectors() // V3-6：新用户轮次开始，重置循环检测器状态
			// P0 agent 防护：Skill 注入同样视为新用户请求，防护状态归零。
			m.guardBeginRequest()
			m.aiService.SetRequestID(fmt.Sprintf("%d", time.Now().UnixNano()))
			cmds = append(cmds, m.startAIStream(), m.restartTickCmd())
		} else {
			m.addMessage(cmdOutput)
			if cmdOutput == "__EXIT__" {
				m.sessionService.SetQuitting(true)
				return m, tea.Quit
			}
			// A queued slash command can complete without starting a new AI turn.
			// Restore macOS native selection before returning to the idle composer.
			if m.sessionService.State() == StateIdle {
				cmds = append(cmds, m.restoreIdleMouse())
			}
		}
	} else {
		m.addMessage(formatUserMessage(input, time.Now().UnixMilli()))
		// Parse mention, detect images
		mentions := mention.DetectMentions(input)
		resolved := mention.ResolveMentions(mentions, m.sessionService.WorkDir())
		expandedInput := mention.ExpandMentions(input, m.sessionService.WorkDir())

		// 修复：mention 解析失败时 findSimilarFile 兜底建议（对标 fileSuggestions.ts:587）
		for _, rm := range resolved {
			if rm.Error != nil {
				suggestion := typeahead.FindSimilarFile(rm.FilePath, m.sessionService.WorkDir())
				if suggestion != "" {
					m.addMessage(errorStyle.Render(fmt.Sprintf("💡 Did you mean: %s?", suggestion)))
				}
			}
		}

		// Build message content (use multimodal format when images present)
		hasImage := false
		for _, rm := range resolved {
			if rm.IsImage {
				hasImage = true
				break
			}
		}
		// Merge mention images and clipboard paste images
		hasPendingImages := len(m.uiService.PendingImages()) > 0
		if hasImage || hasPendingImages {
			// Extract text portion (remove raw image mention references)
			textPart := input
			for _, rm := range resolved {
				if rm.IsImage {
					textPart = strings.Replace(textPart, rm.Raw, "", 1)
				}
			}
			textPart = strings.TrimSpace(textPart)
			// ⑤：剪贴板贴图的 [Image #N] 占位符只承载输入框显示，
			// 发送时从文本剥离（图片本体经 PendingImages 附发，见下方合并循环）。
			textPart = stripImagePlaceholders(textPart)

			// Build image list (merge mention images and clipboard paste images)
			var images []session.ImageBlock
			for _, rm := range resolved {
				if rm.IsImage {
					images = append(images, session.ImageBlock{
						MediaType:  rm.MediaType,
						Base64Data: rm.Base64,
					})
				}
			}
			for _, pi := range m.uiService.PendingImages() {
				images = append(images, session.ImageBlock{
					MediaType:  pi.MimeType,
					Base64Data: pi.Base64,
				})
			}
			m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserWithImages(textPart, images)))
		} else {
			m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserText(expandedInput)))
		}
		// 双写期：设置当前消息 ID，供 FileHistory.Track/Snapshot 关联使用。
		// 必须在 startAIStream 之前设置，确保本轮所有工具执行（executeTools）都能读到 msgId。
		if exec := m.toolService.Executor(); exec != nil {
			msgs := m.sessionService.CurrentSession().GetMessages()
			if len(msgs) > 0 {
				exec.SetCurrentMessageId(msgs[len(msgs)-1].UUID)
			}
		}
		// Clear pending images after sending
		m.uiService.SetPendingImages(nil)
		// 取反侧同样走唯一谓词（与本函数上方判定侧同源；传 rawInput 保持一致）。
		if m.sessionService.FirstUserInput() == "" && !command.IsCommandInput(rawInput) {
			m.sessionService.SetFirstUserInput(input)
		}
		if m.sessionService.SessionName() == "" {
			m.autoNameSession()
		}
		m.sessionService.SetAutoCompactCooldown(false)
		m.sessionService.SetState(StateStreaming)
		m.resetStreamState()
		m.resetAIDetectors() // V3-6：新用户轮次开始，重置循环检测器状态
		// P0 agent 防护：新用户请求开始，防护状态归零（轮次/熔断/审查/进度）。
		m.guardBeginRequest()
		m.aiService.SetRequestID(fmt.Sprintf("%d", time.Now().UnixNano()))
		cmds = append(cmds, m.startAIStream(), m.restartTickCmd())
	}
	return m, tea.Batch(cmds...)
}

// startAIStream 启动带恢复策略的 AI 流式请求，并给异步消息绑定当前 requestID。
//
// 调用点 `append(cmds, m.startAIStream(), …)` 会让 Go **先同步求值
// 函数体**、才拿到返回的 Cmd（只有返回的闭包才异步）。因此本函数体必须保持 O(事件)：
// - 留在同步段的只有「防护闸门判定 / ctx 与 cancel 装配 / 句柄与取值快照的局部化」；
// - 请求构造（buildRequest：指令文件发现、工具 schema、全上下文 token 估算）
// **已移入返回的 Cmd 闭包**，由 bubbletea 在 Update 返回后执行 ⇒ 主循环立即返回。
//
// 判据：main_loop_invariant_test.go（修前红：buildRequest 段在主循环内上报）。
func (m *Model) startAIStream() tea.Cmd {
	defer perfSpan("startAIStream.total")()

	// P0 agent 防护统一闸门：每次模型调用前判定（轮次上限/审查超限/空转/已停机）。
	// Stop 时已渲染进度快照并交回控制权，返回停机收尾命令（drain 排队输入/
	// 异步自动压缩），不得发起模型调用。恢复指引：用户直接回复即开新预算段。
	if cmd, stopped := m.guardBeforeModelCall(); stopped {
		return cmd
	}
	// 防御性重置 Interrupted（子审查建议 #1）：stopExecution/Ctrl+C 会 SetInterrupted(true)，
	// 中断后立即发新消息（drain 队列或用户手动输入）时，新流的首个事件可能先于旧流的
	// streamDoneMsg 到达，若 Interrupted 仍为 true 会被 handleStreamEvent 静默丢弃。
	// 此处显式重置为 false，确保新流正常处理。幂等操作，不影响正常完成路径。
	m.sessionService.SetInterrupted(false)
	m.beginRequestUsage(m.aiService.RequestID())
	if m.aiService.StreamCancel() != nil {
		m.aiService.StreamCancel()()
		m.aiService.SetStreamCancel(nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.aiService.SetStreamCancel(cancel)

	// 现象②：记录本轮请求的发出时刻，供等待窗口渲染「已等待多久」。
	// 放在构造请求之前 ⇒ 计时从「用户提交/工具轮次接续」起算，含建请求与网关排队。
	// startAIStream 同时服务初轮（本文件上层）与工具执行后的续轮（tool_exec.go）
	// ⇒ 一处打点即覆盖**每一段**等待，无需在工具侧另加。
	m.waitingSince = time.Now()

	// ── ：请求构造移出 Update 主循环 ────────────────────
	//
	// 原来此处是 `req := m.buildRequest()` 直接调用；因调用点参数即时求值，它
	// 跑在 Update 的同步段里 —— 指令文件发现 + 工具 schema 拼装 + 全上下文 token
	// 估算全压住主循环，「按发送卡」即由此而来。
	// 现移入返回的 Cmd 闭包：bubbletea 在 Update 返回**之后**才执行它。
	//
	// ★ 竞态纪律（域「TUI按值复制陷阱」·）：异步闭包内**不得读 m 的字段**
	// —— reassignServices() 会无同步地重绑 m.<field>，而本函数接收者是 *Model
	// （即闭包捕获的是同一个 Model 地址）。故先取「值快照 reqModel」＋
	// 「服务句柄局部量 aiSvc」，闭包只碰这两个局部量，不碰 m。
	reqModel := *m       // 值快照：buildRequest 只读它，不读主循环的 m
	aiSvc := m.aiService // 句柄局部量：闭包内不再经 m.aiService 读字段
	capturedModel := reqModel.sessionService.ModelName()
	capturedRequestID := aiSvc.RequestID()

	streamCmd := func() tea.Msg {
		// 请求构造在主循环之外执行（这正是本批修复的落点）。
		req := reqModel.buildRequest()
		// buildRequest 的副作用之一（requestSnapshot）写在值快照上，
		// 须随首条消息回传主循环（见 streamEventMsg.snapshotKnown）。
		snap := reqModel.SessionModel.requestSnapshot

		events, err := aiSvc.StreamRequest(ctx, &req)
		if err != nil {
			cancel()
			return aiErrorMsg{err: err, requestID: capturedRequestID}
		}

		event, ok := <-events
		if !ok {
			cancel()
			return streamDoneMsg{
				blocks:    []session.ContentBlock{},
				usage:     &session.TokenUsage{},
				model:     capturedModel,
				thinking:  "",
				requestID: capturedRequestID,
			}
		}
		return streamEventMsg{
			event:           event,
			events:          events,
			requestID:       capturedRequestID,
			requestSnapshot: snap,
			snapshotKnown:   true,
		}
	}
	if mouseCmd := m.enableActiveMouse(); mouseCmd != nil {
		// Mouse reporting must be active before the first stream event can make
		// the Stop button visible and clickable.
		return tea.Sequence(mouseCmd, streamCmd)
	}
	return streamCmd
}

// buildRequest build AI request
func (m *Model) buildRequest() ai.Request {
	// 本函数在 sendInput / 工具续轮路径上被**主循环同步**调用（见 startAIStream）。
	// 分段埋点是为把「卡在哪一段」变成事实 —— 头号嫌疑是 systemPrompt
	// （内含从 CWD 逐层扫到根的指令文件发现，见 config.DiscoverInstructionFiles）
	// 与 snapshot（对整份上下文做 token 估算）。
	defer perfSpan("buildRequest.total")()

	tSystem := time.Now()
	layers := m.buildSystemPrompt()
	systemPrompt := layerBlocksToStrings(layers)
	perfReport("buildRequest.systemPrompt", tSystem, "")

	// Cache rendered system prompt for Fork child agent to inherit for prompt cache hits
	tHash := time.Now()
	if len(systemPrompt) > 0 && m.toolService.Executor() != nil {
		joined := strings.Join(systemPrompt, "\n")
		m.toolService.Executor().RenderedSystemPrompt = &ai.RenderedPrompt{
			Content:     joined,
			Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(joined))),
		}
	}
	perfReport("buildRequest.renderedPromptHash", tHash, "")

	tMsg := time.Now()
	messages := m.buildMessages()
	perfReport("buildRequest.messages", tMsg, "")

	tTools := time.Now()
	toolDefs := m.buildToolDefs()
	perfReport("buildRequest.toolDefs", tTools, "")

	maxTokens := m.aiService.Budget().PostCompactMaxTokens()
	if maxTokens <= 0 {
		maxTokens = m.sessionService.Config().ContextWindow - ctxpkg.RecoveryBudgetTokens
	}

	modelMaxOutput := ai.MaxOutputTokensForModel(m.sessionService.ModelName())
	if maxTokens > modelMaxOutput {
		maxTokens = modelMaxOutput
	}

	req := ai.Request{
		SystemPrompt:       systemPrompt,
		SystemPromptLayers: layerBlocksToComponents(layers),
		Messages:           messages,
		MaxTokens:          maxTokens,
		Tools:              toolDefs,
		Model:              m.sessionService.ModelName(),
		Thinking: &ai.ThinkingConfig{
			Enabled:      m.sessionService.ThinkingEnabled(),
			BudgetTokens: m.sessionService.ThinkingBudgetTokens(),
		},
	}
	// 本段对**整份上下文**做 token 估算（O(会话长度)）⇒ 会话越长越慢，
	// 是"长会话按发送更卡"的直接嫌疑。
	tSnap := time.Now()
	m.SessionModel.requestSnapshot = snapshotRequest(
		m.aiService.Estimator(), req, estimateSessionSystemPromptTokens(m.aiService.Estimator(), m.sessionService.CurrentSession().GetMessages()),
	)
	perfReport("buildRequest.snapshot", tSnap, "")
	return req
}

// buildSystemPrompt 构造分层 system prompt（N15：委托 prompt 包 helpers 统一构造）。
// 返回 []ai.PromptLayerBlock 供 buildRequest 填充 Request.SystemPromptLayers（N3 接入点）+ SystemPrompt（向后兼容）。
func (m *Model) buildSystemPrompt() []ai.PromptLayerBlock {
	var components []ai.PromptComponent

	// Layer 0: CoreIdentity（base prompt，稳定可信指令）
	baseParts := []string{
		"You are Whale Code, a personal AI coding assistant. You are good at code writing, debugging, refactoring, and explanation.",
		"You can use tools to read files, write files, execute commands, search code, etc.",
		"Please respond in Chinese unless the user uses another language.",
		"Be concise and direct. For simple requests (like viewing a directory, reading a file), just execute the command immediately without unnecessary explanation.",
		"When a user asks to run a Linux command that doesn't exist on Windows, silently use the PowerShell equivalent instead of explaining the difference. For example: lsof -> Get-NetTCPConnection, ls -> Get-ChildItem, cat -> Get-Content, grep -> Select-String, find -> Get-ChildItem -Recurse.",
		fmt.Sprintf("Working directory: %s", m.sessionService.WorkDir()),
		fmt.Sprintf("Current model: %s", m.sessionService.ModelName()),
		fmt.Sprintf("OS: %s", runtime.GOOS),
		"When you detect a strong user preference (e.g., language, framework, coding style, naming convention), use the /memory add command to save it. Example: /memory add preferred_language TypeScript. This helps personalize future conversations.",
		// 方案 A（2026-10-19 拍板）：范围纪律，SSOT = prompt.ScopeDisciplinePrompt（，与 Runner 侧同步）。
		prompt.ScopeDisciplinePrompt,
	}
	components = append(components, ai.PromptComponent{
		Name:    "core_identity",
		Content: strings.Join(baseParts, "\n\n"),
		Stable:  true,
		Layer:   ai.LayerCoreIdentity,
	})

	// Layer 2: ProjectCtx（project instructions + memory，稳定）
	// H4 ：magic_docs V2 优先（DiscoverInstructionFiles），V1 兜底逻辑在 prompt.MergeMagicDocs 内。
	if projectCtx := prompt.MergeMagicDocs(m.sessionService.WorkDir(), m.aiService.MagicDocs()); projectCtx != "" {
		components = append(components, ai.PromptComponent{
			Name:    "project_instructions",
			Content: prompt.TruncateBytes(projectCtx, 100*1024),
			Stable:  true,
			Layer:   ai.LayerProjectCtx,
		})
	}
	// N12: V2 统一记忆注入（MEMORY.md always loaded + AI 选择 top-K + drift caveat）。
	if memCtx, memErr := m.aiService.MemSelector().Select(context.Background(), m.sessionService.FirstUserInput()); memErr == nil && memCtx != "" {
		components = append(components, ai.PromptComponent{
			Name:    "user_memory",
			Content: prompt.TruncateBytes(memCtx, 100*1024),
			Stable:  true,
			Layer:   ai.LayerProjectCtx,
		})
	}

	// Layer 3: SessionState（search + nested rules + agent + skill，动态）
	var sessionParts []string
	sessionParts = append(sessionParts, tools.SearchStrategyPrompt)
	// 延迟工具目录：让模型知道哪些工具可用但需通过 ToolSearch 加载（~400 token）
	if dir := m.toolService.Registry().DeferredToolDirectory(); dir != "" {
		sessionParts = append(sessionParts, dir)
	}
	if searchCtx := m.toolService.Executor().SearchCtx.ContextForPrompt(); searchCtx != "" {
		sessionParts = append(sessionParts, searchCtx)
	}
	// V2 nested memory loading: load nested WHALE.md based on recently read files
	if m.toolService.Executor().SearchCtx != nil {
		recentFile := m.toolService.Executor().SearchCtx.LastReadFile()
		if recentFile != "" {
			nestedFiles := config.DiscoverNestedMemory(m.sessionService.WorkDir(), recentFile)
			if len(nestedFiles) > 0 {
				var nestedParts []string
				for _, nf := range nestedFiles {
					resolved := config.ResolveIncludes(nf.Content, filepath.Dir(nf.Path), 5, nil)
					if resolved != "" {
						nestedParts = append(nestedParts, resolved)
					}
				}
				if len(nestedParts) > 0 {
					sessionParts = append(sessionParts, "=== Nested Project Rules ===")
					sessionParts = append(sessionParts, strings.Join(nestedParts, "\n\n"))
				}
			}
		}
	}
	// Agent role
	if m.sessionService.CurrentAgent() != "" && m.sessionService.CurrentAgent() != "coder" {
		for _, a := range availableAgents {
			if a.Name == m.sessionService.CurrentAgent() {
				sessionParts = append(sessionParts, fmt.Sprintf("Current agent role: %s. %s", a.Name, a.SystemHint))
				break
			}
		}
	}
	// Skills
	if len(m.sessionService.ActiveSkills()) > 0 {
		var skillHints []string
		for _, sn := range m.sessionService.ActiveSkills() {
			for _, s := range availableSkills {
				if s.Name == sn {
					skillHints = append(skillHints, s.Name)
					break
				}
			}
		}
		if len(skillHints) > 0 {
			sessionParts = append(sessionParts, fmt.Sprintf("Active skills: %s. Apply domain-specific best practices for these areas.", strings.Join(skillHints, ", ")))
		}
	}
	if len(sessionParts) > 0 {
		components = append(components, ai.PromptComponent{
			Name:    "session_state",
			Content: strings.Join(sessionParts, "\n\n"),
			Stable:  false,
			Layer:   ai.LayerSessionState,
		})
	}

	// （2026-10-19 拍板）：/plan 的灯接进提示层 —— planMode=true 时注入「只分析」约束。
	// 修前 planMode 全仓零消费（开关没接灯，UI 硬承诺无机制背书）；硬门（PermissionStage 拦写）发布后立项。
	// SessionState 动态层（随开关破坏 cache，不该进稳定层）。
	if m.sessionService.PlanMode() {
		components = append(components, ai.PromptComponent{
			Name:    "plan_mode",
			Content: PlanModePrompt,
			Stable:  false,
			Layer:   ai.LayerSessionState,
		})
	}

	// T3 S4: 主动提问能力引导。
	// 引导 AI 在 confidence<0.6 时主动调 AskUser 工具（category=dialogue-clarify）。
	// 严格约束何时提问/如何提问/不要滥用，避免 ping-pong。
	components = append(components, ai.PromptComponent{
		Name:   "askuser_dialogue_guidance",
		Stable: false,
		Layer:  ai.LayerSessionState,
		Content: `## 主动提问能力（T3）

当用户输入模糊、多义或缺少关键参数时，你可以主动调用 AskUser 工具向用户提问澄清。

### 何时提问（confidence<0.6）

- 用户需求包含"等等"、"那个"、"帮我看看"等模糊词
- 用户提到多个可能的技术选型但未明确选择（如 "用 JWT 还是 Session？"）
- 用户缺少关键参数（如 "实现登录功能" 但未说明认证方式）
- 用户输入少于 10 字符且无上下文

### 如何提问

调用 AskUser 工具，参数：
- question: 简洁的问题文本（≤500 字符）
- options: 2-3 个互斥选项（每个 label ≤20 字符，description ≤80 字符；推荐项放第一个，不要加 Other，客户端会自动补）
- default_idx: 推荐选项的索引（0-based，-1 无推荐）
- category: 固定填 "dialogue-clarify"（日常对话场景）
- allow_free_text: false（让用户选择，除非真的需要自由输入）

### 不要滥用

- 每条用户消息最多调 1 次 AskUser（TUI 层会拦截超出的调用）
- 如果用户拒绝回答（cancelled=true），用你的最佳猜测继续，不要再追问
- 如果用户已经在消息中明确表达了意图，不要提问
- 编排场景（/orchestrate start）由 orchestrator 自动调 AskUserGateway，不要调用 AskUser 工具

### 示例

用户输入："实现登录功能"

你应该调 AskUser：
` + "```json" + `
{
  "question": "认证方式选哪种？",
  "options": [
    {"label": "JWT", "description": "无状态 Token，适合微服务", "value": "jwt"},
    {"label": "Session", "description": "服务端存储，传统 Web", "value": "session"},
    {"label": "OAuth2", "description": "第三方登录，社交账号", "value": "oauth2"}
  ],
  "default_idx": 0,
  "category": "dialogue-clarify",
  "allow_free_text": false
}
` + "```",
	})

	// V3-6-3：注入强制执行系统消息（若用户已确认强制执行）。
	// 仅在 forceExecuteMode 为 true 时注入，明确告知 AI 仍需遵守沙箱和权限约束。
	if m.refusalHandler != nil {
		if forceMsg := m.refusalHandler.GetForceExecuteSystemMessage(); forceMsg != "" {
			components = append(components, ai.PromptComponent{
				Name:    "force_execute",
				Content: forceMsg,
				Stable:  false,
				Layer:   ai.LayerSessionState,
			})
		}
	}

	// P1-1（v2 审查补充）：对非 CoreIdentity 层应用 prompt injection 防护。
	components = prompt.SanitizeComponent(components)
	blocks := ai.NewCacheAwareRequestBuilder().BuildSystemPromptBlocks(components)
	return append(blocks, sessionSystemPromptBlocks(m.sessionService.CurrentSession().GetMessages())...)
}

// buildMessages build message list for API
func (m *Model) buildMessages() []ai.MessageEntry {
	entries := sessionMessagesToAPI(m.sessionService.CurrentSession().GetMessages())
	// ★ C（2026-10-13，手测卡② §19.5 选项 C）：把已完成的 Monitor 事实并入本次请求。
	// buildMessages 是模型上下文的**唯一**构建入口（生产调用点只有本文件 :443），
	// 在此注入即"只在模型真要发请求时注入"，且 injectMonitorFacts 消费后清空 ⇒ 恰好一次。
	return m.injectMonitorFacts(entries)
}

// buildToolDefs build tool definitions for API
// buildToolDefs 构造 tools 数组（F1 修复：延迟工具加载）。
// 使用 AllDirect() 只加载 ExposureDirect 工具 + 已加载的延迟工具（LoadedDeferredToolNames）。
// 延迟工具通过 ToolSearch({"load":"工具名"}) 或 Execute 自动加载兜底触发加载。
// 对标同类 CLI defer_loading：减少每次请求的 tool schema token 开销。
func (m *Model) buildToolDefs() []ai.ToolDefinition {
	specs := m.toolService.Registry().AllDirect()
	// 合并已加载的延迟工具 schema（会话级状态，由 Executor.loadedDeferredTools 维护）
	if exec := m.toolService.Executor(); exec != nil {
		for _, name := range exec.LoadedDeferredToolNames() {
			if spec, ok := m.toolService.Registry().Get(name); ok {
				specs = append(specs, spec)
			}
		}
	}
	defs := make([]ai.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		desc := spec.Description

		if spec.Name == "bash" && runtime.GOOS == "windows" {
			desc = "Execute PowerShell commands on Windows. Use PowerShell cmdlets (Get-ChildItem, Get-Content, Select-String). Do NOT use Linux commands (ls, cat, grep)."
		}

		defs = append(defs, ai.ToolDefinition{
			Name:        spec.Name,
			Description: desc,
			// P0-2：与 runnerCore.buildToolDefs 共用同一投影（内建恒等；外部 MCP schema 降损）。
			InputSchema: tools.CompactSchemaForModel(spec.InputSchema),
		})
	}
	return defs
}

// handleStreamEvent 处理单个 AI 流事件并调度下一条事件。
func (m *Model) handleStreamEvent(event ai.Event, events <-chan ai.Event) tea.Cmd {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	if event == nil {
		return m.readNextEvent(events)
	}

	if m.sessionService.Interrupted() {
		return nil
	}

	switch e := event.(type) {
	case ai.TextDeltaEvent:
		if m.activeCell != nil {
			m.activeCell.AppendText(e.Content)
		} else {
			// 未初始化 ActiveCell 时保留兼容降级路径；生产路径不再复制完整字符串。
			m.sessionService.SetCurrentText(m.sessionService.CurrentText() + e.Content)
		}
		m.updateStreamingDisplay()

	case ai.ThinkingDeltaEvent:
		if m.activeCell != nil {
			m.activeCell.AppendThinking(e.Content)
		} else {
			// 未初始化 ActiveCell 时保留兼容降级路径；生产路径不再复制完整字符串。
			m.sessionService.SetCurrentThinking(m.sessionService.CurrentThinking() + e.Content)
		}
		m.updateStreamingDisplay()

	case ai.ToolUseEvent:
		if e.ID != "" && e.Name != "" {
			exists := false
			for _, t := range m.toolService.PendingTools() {
				if t.ID == e.ID {
					exists = true
					break
				}
			}
			if !exists {
				m.toolService.SetCurrentToolID(e.ID)
				m.toolService.ToolInputBuf()[e.ID] = ""
				if e.Input != "" {
					m.toolService.ToolInputBuf()[e.ID] = e.Input
				}
				m.toolService.SetPendingTools(append(m.toolService.PendingTools(), session.ContentBlock{
					Type: "tool_use",
					ID:   e.ID,
					Name: e.Name,
				}))
			} else {
				m.toolService.SetCurrentToolID(e.ID)
				if e.Input != "" {
					m.toolService.ToolInputBuf()[e.ID] = e.Input
				}
			}
		} else if e.Input != "" {
			toolID := e.ID
			if toolID == "" {
				toolID = m.toolService.CurrentToolID()
			}
			if toolID != "" {
				if buf, ok := m.toolService.ToolInputBuf()[toolID]; ok {
					m.toolService.ToolInputBuf()[toolID] = buf + e.Input
				}
			}
		}

	case ai.ToolInputDeltaEvent:
		toolID := m.toolService.CurrentToolID()
		if toolID != "" {
			if buf, ok := m.toolService.ToolInputBuf()[toolID]; ok {
				m.toolService.ToolInputBuf()[toolID] = buf + e.PartialJSON
			}
		}

	case ai.UsageEvent:
		m.SessionModel.requestUsage.Add(e)

	case ai.MessageStopEvent:
		m.finalizeRequestUsage(m.aiService.RequestID())
		return m.finalizeStream(e.FinishReason)

	case ai.ErrorEvent:
		m.finalizeRequestUsage(m.aiService.RequestID())
		m.sessionService.SetState(StateIdle)
		m.resetStreamState()
		m.recalcViewportHeight()
		m.addMessage(errorStyle.Render(fmt.Sprintf("AI Error: %v", e.Err)))
		return m.restoreIdleMouse()
	}

	return m.readNextEvent(events)
}

// readNextEvent 读取下一条流事件，并保留当前请求身份以隔离过期消息。
func (m *Model) readNextEvent(events <-chan ai.Event) tea.Cmd {
	capturedModel := m.sessionService.ModelName()
	capturedRequestID := m.aiService.RequestID()
	// 流截断（无 MessageStopEvent）诊断：预捕获已流出的 thinking（与 finalizeStream
	// 同步预计算模式一致，避免闭包内异步读 Model 与 Update 循环构成数据竞争），
	// 供空响应 canary 的 thinkingLen 区分"仅输出了思考"。
	capturedThinking := m.streamingThinking()
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return streamDoneMsg{
				blocks:       []session.ContentBlock{},
				usage:        &session.TokenUsage{},
				model:        capturedModel,
				thinking:     capturedThinking,
				requestID:    capturedRequestID,
				stopReceived: false,
			}
		}
		return streamEventMsg{event: event, events: events, requestID: capturedRequestID}
	}
}

// updateStreamingDisplay mark display as dirty for next tick render
func (m *Model) updateStreamingDisplay() {
	m.uiService.SetDirty(true)
}

// renderStreamContent 将流式活动内容渲染到 viewport，并仅在用户原本位于底部时跟随尾部。
// waitingIndicator 返回等待窗口（既无正文、也无思考内容）的可见文案：
// 英文主行 + 缩进中文辅助行（形态 SSOT = util.Bi/BiSame，经本包 bi/biSame 转发）。
//
// 现象②（2026-10-04 真机取证）：用户提交后到首 token 之间约 17s 界面无任何新信息，
// 用户无法判断请求是否已发出 ⇒ 自己按了中断。旧的 "AI is thinking..." 两不：
//
//	(1) 不说请求已发出 ⇒ 分不清「已发出、在等网关」与「压根没发出去」；
//	(2) 不带耗时 ⇒ 17s 的等待与卡死在视觉上完全一样（spinner 会动，但不进时间）。
//
// 故本函数把这两点补上：表明"已发送"，并把"已等待多久"直接摆出来。
//
// 退化规则（有意，非遗漏）：
// - waitingSince 为零值（未打点，如未走 startAIStream 的路径）⇒ 不带计时。
// 零值 time.Time 直接做 Since 会得到"几千年"的荒谬数字，必须先判 IsZero。
// - 不足 1s ⇒ 同样不带计时：否则在首个事件到达前一直闪 "(0s)"，没有信息量。
func (m *Model) waitingIndicator() string {
	const enNoTimer = "Sent, waiting for response..."
	const zhNoTimer = "已发送，等待响应..."
	if m.waitingSince.IsZero() {
		return bi(enNoTimer, zhNoTimer)
	}
	secs := int(time.Since(m.waitingSince).Seconds())
	if secs < 1 {
		return bi(enNoTimer, zhNoTimer)
	}
	return biSame("Sent, waiting for response... (%ds)", "已发送，等待响应...（%ds）", secs)
}

func (m *Model) renderStreamContent() {
	followTail := m.uiService.Viewport().AtBottom()
	// 增量渲染（2026-08-01 输入响应优化）：消息数量不变时复用缓存，
	// 避免 O(n) 全量调用 renderMessages()。消息列表在流式期间不变
	// （只有 CurrentText 增长），缓存安全。WindowSizeMsg 时已失效缓存。
	// REF-外部: 同类 CLI code Ink throttle(leading:true) 只更新变化部分
	// REF-外部: 同类 CLI active_cell 可变尾部 历史消息不变
	msgCount := len(m.sessionService.Messages())
	if msgCount != m.streamCachedMsgCount {
		m.streamCachedHistory, m.streamCachedHistorySrcs = m.renderTranscript()
		m.streamCachedMsgCount = msgCount
	}

	var sb strings.Builder

	spinner := spinnerChars[m.uiService.SpinnerIdx()]

	switch m.sessionService.State() {
	case StateStreaming:
		// 渲染源切换（IT1-014）：可变尾部从 sessionService 读取改为 activeCell 读取，
		// 让 activeCell 成为渲染真相源（对标同类 CLI active_cell）。
		// nil 守卫：activeCell 为 nil 时降级为 sessionService（保护未初始化场景）。
		// REF-外部: 同类 CLI "in-flight active cell that can mutate in place"
		thinkingPreview := m.streamingThinking()

		// 宽度口径：contentWrapWidth（与 wrapViewportContent 折行宽度一致）。
		// 阈值 16 = 顶行最小宽度（5 + label 11），不足则走下方纯文本退化分支（F3）。
		// ★ 2026-09-24「内容驱动」：门槛去掉 ThinkingEnabled()，只看「思考内容有没有到达」。
		// 依据（业界对照复核 thinking开关语义_业界对照与第一性原理复核_2026-09-24.md）：
		// 服务端 0.47s 起就下发 reasoning_content、事件侧已无条件累积（stream.go:672-677），
		// 原先再拿客户端开关当门槛 ⇒ 内容被挡住（表现为只显示 "AI is thinking..." 干等）。
		// 同类 CLI（只渲染服务端回传的 reasoning summary）与 同类 CLI（有 thinking block 即渲染）
		// 均由「内容是否到达」驱动；此处对齐。
		if thinkingPreview != "" && m.contentWrapWidth() >= 16 {
			// F1（2026-09-08 对抗式审查）：预览是单行设计——取首行并按显示宽度截断到
			// 框内可用宽度（前缀 "│ "+spinner+" " 4 列 + 后缀 " │" 2 列），否则整行超宽
			// 被 Hardwrap 折断右侧 │。
			preview := strings.SplitN(truncateRunes(thinkingPreview, 200), "\n", 2)[0]
			preview = ansi.Truncate(preview, m.contentWrapWidth()-6, "")
			// G1 思考边框宽度自适应（对标同类 CLI rendering.rs）：使用 runewidth.StringWidth
			// 动态计算显示宽度（🧠 emoji 占 2 列，rune 数 14 ≠ 显示宽度 15）。
			thinkingLabel := "╭─ 🧠 Thinking "
			thinkingLabelWidth := runewidth.StringWidth(thinkingLabel)
			sb.WriteString(thinkingBorderStyle.Render("╭─ "))
			sb.WriteString(thinkingLabelStyle.Render("🧠 Thinking"))
			// 修复：用 contentWrapWidth（viewport.Width - inputBoxStyle 横向 frame）而非
			// viewport.Width，与 wrapViewportContent 的硬折行宽度对齐；否则边框行超宽
			// 被 ansi.Hardwrap 折行，╮/│/╯ 角标错位掉到下一行（2026-09-08 修复）。
			// 加 ╮ 闭合顶部（-1 扣除 ╮ 宽度），对标同类 CLI with_border_internal 宽度契约
			// REF-外部: 同类 CLI with_border_internal
			thinkingWidth := m.contentWrapWidth() - thinkingLabelWidth - 1
			if thinkingWidth > 0 {
				sb.WriteString(thinkingBorderStyle.Render(" " + strings.Repeat("─", thinkingWidth) + "╮"))
			} else {
				sb.WriteString(thinkingBorderStyle.Render(" ╮"))
			}
			sb.WriteString("\n")
			// 中间行：│ content pad │（右侧闭合，对标 renderThinkingBlock 宽度契约）
			contentLine := fmt.Sprintf("│ %s %s", spinner, preview)
			usedWidth := runewidth.StringWidth(contentLine)
			padCount := m.contentWrapWidth() - 4 - usedWidth
			if padCount < 0 {
				padCount = 0
			}
			sb.WriteString(thinkingStyle.Render(contentLine + strings.Repeat(" ", padCount) + " │"))
			sb.WriteString("\n")
		} else if thinkingPreview != "" {
			// 极窄宽度（contentWrapWidth<16）不画框，仅输出纯文本（与 renderThinkingBlock 兜底对齐）
			sb.WriteString(thinkingStyle.Render(spinner + " " + truncateRunes(thinkingPreview, 200)))
			sb.WriteString("\n")
		}

		currentText := m.streamingText()

		if currentText != "" {
			sb.WriteString(assistantStyle.Render("🐋 Whale: "))
			// 流式过程中实时渲染 Markdown，避免 #、**、` 等符号原样显示给用户。
			// renderTextContent 内部已通过 closeUnterminatedMarkdownFence 处理
			// 未闭合的代码块；其他不完整 markdown（如 **bold 未闭合）glamour 会
			// 原样输出，等下一个 delta 补全后再次渲染即可正确呈现。
			// 流式正文同样按真实内容宽度喂 glamour（与定稿路径同一口径）。
			sb.WriteString(renderTextContentWidth(currentText, m.contentWrapWidth()))
		} else if thinkingPreview == "" {
			// 仅在「既无正文、也无思考内容」时才输出占位提示——
			// 有思考内容时上方已渲染思考框，此处若再判开关会出现「有内容却显示干等」的矛盾。
			// ★ 现象②：占位从「内容无关的一句话」升级为「已发送 + 已等待 Ns」，
			// 详见 waitingIndicator 的注释。spinner 仍是活动指示，未变。
			sb.WriteString(thinkingStyle.Render(fmt.Sprintf("%s %s", spinner, m.waitingIndicator())))
		}

		if len(m.toolService.PendingTools()) > 0 {
			for _, t := range m.toolService.PendingTools() {
				// 紧凑渲染（对标同类 CLI：spinner 替代 ⏺ 作为活动指示，参数人可读）。
				// AskUser 返回空 → 跳过（由交互 UI 接管，不暴露原始 JSON）。
				rendered := renderToolUseStreaming(spinner, t.Name, m.toolService.ToolInputBuf()[t.ID])
				if rendered == "" {
					continue
				}
				sb.WriteString("\n")
				sb.WriteString(toolStyle.Render(rendered))
			}
		}

	case StateToolExec:
		if len(m.toolService.PendingTools()) > 0 {
			for _, t := range m.toolService.PendingTools() {
				rendered := renderToolUseStreaming(spinner, t.Name, t.Input)
				if rendered == "" {
					continue
				}
				sb.WriteString(toolStyle.Render(rendered))
			}
		} else {
			sb.WriteString(toolStyle.Render(fmt.Sprintf("%s Executing tools...", spinner)))
		}
	}

	m.refreshStreamingViewport(m.streamCachedHistory, m.streamCachedHistorySrcs, sb.String())
	if followTail {
		m.uiService.Viewport().GotoBottom()
	}
}

// finalizeStream 捕获流式结果并构造完成消息；ActiveCell 延迟到完成消息提交后清理。
func (m *Model) finalizeStream(finishReason string) tea.Cmd {
	blocks := make([]session.ContentBlock, 0)
	capturedText := m.streamingText()
	capturedThinking := m.streamingThinking()

	if capturedText != "" {
		blocks = append(blocks, session.ContentBlock{
			Type: "text",
			Text: capturedText,
		})
	}

	pendingTools := m.toolService.PendingTools()
	for i := range pendingTools {
		input := m.toolService.ToolInputBuf()[pendingTools[i].ID]
		pendingTools[i].Input = input
		blocks = append(blocks, pendingTools[i])
	}

	usage := &session.TokenUsage{
		InputTokens:  m.SessionModel.lastInputTokens,
		OutputTokens: m.SessionModel.lastOutputTokens,
	}

	capturedModel := m.sessionService.ModelName()
	capturedRequestID := m.aiService.RequestID()

	// 不要在这里清空 activeCell：finalized 消息还要排队返回给 Update。
	// 如果用户在 MessageStopEvent 与 streamDoneMsg 之间按 Ctrl+C，stopExecution
	// 需要从 activeCell 读取已完成的文本；过早 Flush 会让这段文本丢失。
	// handleStreamDone 完成会话提交后通过 resetStreamState 统一清理活动 cell 和帧限制器。
	return func() tea.Msg {
		return streamDoneMsg{
			blocks:       blocks,
			usage:        usage,
			model:        capturedModel,
			thinking:     capturedThinking,
			requestID:    capturedRequestID,
			stopReceived: true,
			finishReason: finishReason,
		}
	}
}

// handleStreamDone 处理流式完成消息、持久化本轮结果并衔接工具或下一条排队输入。
func (m *Model) handleStreamDone(msg streamDoneMsg) tea.Cmd {
	// Clean channel close is also a request terminal. MessageStop normally
	// finalized this already, so this call is intentionally idempotent.
	usage, usageSeen := m.finalizeRequestUsage(msg.requestID)
	// Finalize() 一次性：MessageStop 正常路径下 finalizeRequestUsage 已在
	// handleStreamEvent 首次调用（真值落入 lastInputTokens），此处恒为零值；
	// 真值改取 msg.usage（finalizeStream 捕获）。截断路径 msg.usage 为零值、
	// 此处 finalizeRequestUsage 为首次调用，保持其返回值。
	if msg.stopReceived && msg.usage != nil {
		usage = ai.UsageEvent{InputTokens: msg.usage.InputTokens, OutputTokens: msg.usage.OutputTokens}
		usageSeen = msg.usage.InputTokens > 0 || msg.usage.OutputTokens > 0
	}
	if m.aiService.StreamCancel() != nil {
		m.aiService.StreamCancel()()
		m.aiService.SetStreamCancel(nil)
	}

	if m.sessionService.Interrupted() {
		m.sessionService.SetState(StateIdle)
		m.uiService.SetInputFocusedOnSend(false)
		m.uiService.SetInputFocusedOnStop(false)
		m.uiService.Textarea().Focus()
		m.resetStreamState()
		m.recalcViewportHeight()
		return m.restoreIdleMouse()
	}

	if len(msg.blocks) == 0 {
		// 空响应 canary（2026-09-08 A2 事故观测缺口）：
		// 此前该路径零日志，模型真空响应/流截断/仅 reasoning 三种情况不可区分。
		// stopReceived=false → 流截断（无 MessageStopEvent）；
		// finishReason=length → 输出被截断（典型为仅 reasoning_content）；
		// usageSeen=false → usage chunk 未到达（流早断）；thinkingLen>0 → 仅输出了思考。
		log.Printf("[tui] empty response (canary): model=%s requestID=%s stopReceived=%v finishReason=%q thinkingLen=%d inputTokens=%d outputTokens=%d usageSeen=%v",
			msg.model, msg.requestID, msg.stopReceived, msg.finishReason, len(msg.thinking), usage.InputTokens, usage.OutputTokens, usageSeen)
		m.sessionService.SetState(StateIdle)
		m.uiService.SetInputFocusedOnSend(false)
		m.uiService.SetInputFocusedOnStop(false)
		m.uiService.Textarea().Focus()
		// channel 关闭等异常路径不会经过 finalizeStream，仍需清理活动 cell，
		// 避免下一轮把上一轮未完成的尾部误显示出来。
		m.resetStreamState()
		m.addMessage(errorStyle.Render("AI returned an empty response. Please try again."))
		m.recalcViewportHeight()
		// P0-8/9 建议优化 #1：空响应早退路径也触发 drain 钩子。
		// 否则排队消息会"卡住"——Idle 状态下 Alt+Up 不触发（app.go:1192 检查 State != Idle）。
		// 依赖致命漏洞 #1 修复（drain 处理器会检查 State == Idle），此时 State 已设为 Idle，可安全 drain。
		if m.HasQueuedInputs() {
			return func() tea.Msg { return drainQueuedInputMsg{} }
		}
		return m.restoreIdleMouse()
	}

	// ★ 思考内容与正文走**同一条**落盘路径（2026-10-03 修复「思考在生产零持久化」）。
	//
	// 改动前的实测缺陷：`ThinkingDeltaEvent` 只写 `m.activeCell.AppendThinking()`（渲染态），
	// `ActiveCell.Flush()` 只返回 text、把 thinking `Reset()` 掉；会话消息里
	// `ContentBlock{Type:"thinking"}` 这个块类型**从来没有任何生产写入点**。
	// 后果：`export/{html,markdown,json}`、`session.TokenCount`、
	// `context.SessionMessagesToAIEntries` 这些早已认得该块类型的消费者全部读到空，
	// `/thinkback` 的两个「有内容」出口在真机恒不可达，而 `format.go` 里那句
	// 「完整思考内容永远在会话存档 `~/.whale/sessions/*.jsonl` 可查」一直是假话。
	//
	// 为什么落在这里：handleStreamDone 是回合收尾的**唯一**会话落盘点，也是思考文本
	// 最后一次可得的时刻 —— 紧随其后的 resetStreamState 会清掉 activeCell 里的思考。
	//
	// ★ 脱敏只做一次、且作用在**已组装**的切片上：思考同样落盘、同样会被 `/share` 导出，
	// 只扫 text 会让「思考里提到密钥」这条路径把明文写进会话存档。
	// 渲染与落盘随后共用同一个切片与同一个 ThinkingContent() ⇒ 不会出现
	// 「屏幕上显示明文密钥、磁盘上却是脱敏值」的输出面分歧。
	persisted := session.WithThinkingBlock(msg.blocks, msg.thinking)

	// R10 P0-13：输出泄露检测（fail-open，panic 时放行）
	redactSecretLeakInBlocks(persisted)

	assistantMsg := session.AssistantWithTools(persisted, msg.model, msg.usage)
	m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), assistantMsg))

	// 宽度口径：contentWrapWidth（与 wrapViewportContent 折行宽度一致，见 format.go formatAssistantMessage）。
	// ★ 保留思考：渲染门槛改成**内容驱动**，摘掉了原来的客户端开关入参
	// （原为 `m.sessionService.ThinkingEnabled()`，恒 false ⇒ 流式期间看得到思考、
	// 回答一结束思考块就消失）。对齐业界：同类 CLI 只渲染服务端回传的 reasoning summary，
	// 同类 CLI 有 thinking block 即渲染 —— 都由「内容是否到达」决定，不由客户端开关决定。
	// 渲染的思考文本从**已落盘的那条消息**取回（而非再读一次 msg.thinking）：
	// 让「屏幕显示」与「存档内容」成为同一份数据，脱敏结果不会只落在其中一侧。
	display, srcs := formatAssistantMessageWithThinkingSource(persisted, assistantMsg.ThinkingContent(), assistantMsg.Timestamp, m.contentWrapWidth())
	if display != "" {
		m.addMessageWithSources(display, srcs)
	}

	// V3-6-1/2/3：AI 安全检测（幻觉/循环无进展/拒绝）。
	// 在 resetStreamState 前调用，确保 PendingTools 状态可用于判断是否有待执行工具。
	m.checkAISafety(assistantMsg.TextContent(), len(m.toolService.PendingTools()) > 0)

	pendingTools := make([]session.ContentBlock, len(m.toolService.PendingTools()))
	copy(pendingTools, m.toolService.PendingTools())

	m.resetStreamState()

	if len(pendingTools) > 0 {
		m.toolService.SetPendingTools(pendingTools)
		m.sessionService.SetState(StateToolExec)
		return tea.Batch(m.executeTools(), m.restartTickCmd())
	}

	m.sessionService.SetState(StateIdle)
	m.uiService.SetInputFocusedOnSend(false)
	m.uiService.SetInputFocusedOnStop(false)
	m.uiService.Textarea().Focus()
	m.recalcViewportHeight()
	m.finishTurn(true)

	autoCompactCmd := m.checkAutoCompact()

	// V2 auto memory extraction: async background detection and extraction of user preferences
	if m.aiService.MemExtractor() != nil && m.sessionService.Config().AutoMemoryEnabled {
		go func() {
			_ = m.aiService.MemExtractor().ExtractAndSave(context.Background(), m.aiService.Client(), m.sessionService.CurrentSession().GetMessages())
		}()
	}

	// P0-8/9 新增：drain 钩子（异步触发，不跳过任何 turn 结束处理）。
	// 子审查建议优化1：必须在 IncrementTurn/saveSession/FileHistory Snapshot/
	// runTurnEndHooks/checkAutoCompact/auto memory extraction 全部完成后才返回。
	// 通过返回发出 drainQueuedInputMsg 的 tea.Cmd，由 Update 循环接收后再调用 sendInput，
	// 而非直接 return m.sendInput() 跳过上述关键步骤。
	if m.HasQueuedInputs() {
		drainCmd := func() tea.Msg { return drainQueuedInputMsg{} }
		if autoCompactCmd != nil {
			return tea.Sequence(autoCompactCmd, drainCmd)
		}
		return drainCmd
	}
	return tea.Batch(autoCompactCmd, m.restoreIdleMouse())
}

// finishTurn commits the common durable boundary for normal and interrupted
// turns. Interrupted turns skip automation hooks so Stop cannot trigger goal
// continuation, but they still receive metadata, persistence, and a snapshot.
func (m *Model) finishTurn(runHooks bool) {
	// 整段收尾都跑在主循环内。用户体感的"第二段卡"发生在回复**结束之后**，
	// 容易被与"按发送卡"合并记成同一个现象 ⇒ 分段以便把两段分开归因。
	defer perfSpan("finishTurn.total")()

	m.sessionService.CurrentSession().IncrementTurn()
	// Make the durable turn boundary synchronous before the caller mutates or
	// reuses this Model. The regular save helper remains asynchronous elsewhere.
	tSave := time.Now()
	<-m.saveSession()
	perfReport("finishTurn.saveSession", tSave, "")

	tSnap := time.Now()
	if exec := m.toolService.Executor(); exec != nil {
		if fh := exec.FileHistoryGetter(); fh != nil {
			msgID := exec.CurrentMessageId()
			msgCount := len(m.sessionService.CurrentSession().GetMessages())
			// 落盘标签 = 会话人话名（别名），不是会话主键 —— 的写侧修复。
			// 存主键会让 /checkpoint list 一行里出现两串 UUID（用户看不懂的直接原因）。
			alias := ""
			if sess := m.sessionService.CurrentSession(); sess != nil {
				alias = sess.Metadata.Name
			}
			label := snapshotLabelForStorage(alias, m.sessionService.SessionName())
			if _, err := fh.Snapshot(msgID, label, msgCount); err != nil {
				log.Printf("[tui] filehistory auto snapshot failed (non-fatal): %v", err)
			}
		}
	}
	perfReport("finishTurn.fileHistorySnapshot", tSnap, "")

	if runHooks {
		tHooks := time.Now()
		m.runTurnEndHooks(context.Background())
		perfReport("finishTurn.turnEndHooks", tHooks, "")
		// A-3：轮末边界的承载体从「一条消息」改为「一次状态迁移」。
		//
		// 旧实现 `m.addMessage(formatTurnCompleteBoundary())` 把"回合结束"表达成
		// **又一条对话消息** ⇒ 它与 Monitor 输出走同一函数、同一条流，物理上管不住
		// 随后到达的任何写入（真机手测 I-7.1：Turn complete 02:22:27 之后 02:22:30
		// 仍在往界面写）。旧判据 TestTurnLayout_TurnCompleteMarkerAppended 只能证明
		// "界面最后一行有这几个字"，证明不了"这轮之后写不进来了"。
		//
		// 现在：只在 m.turn 留痕（seq/closedAt），**不占消息身份**；轮末分隔线由
		// 渲染侧从该状态投影（displayMessages）。于是"本轮是否已结束"成为
		// 可判别的事实，同时它也是 A-1 授权判据（turnWriteOpen）的同一事实来源。
		// runHooks==true 的三个调用点（正常收尾 / blocked turn / 守卫停机）都是
		// 「整轮真正落地」，这里落一条 ✓ 结束标志；中断路径（finishTurn(false)）**不在此落痕**，
		// 由 stopExecution 在补完部分输出显示消息后自行 closeTurn(…, true) 落 ⏹ 标志
		// —— 两条收尾路径各自落一次，保证"每一轮恰好一个结束标志"（2026-10-07 用户拍板）。
		m.closeTurn(time.Now(), false)
	}
	// Force confirmation is scoped to one completed user turn. It can affect
	// the model's wording during this turn, but must not silently carry into the
	// next request.
	if m.refusalHandler != nil {
		m.refusalHandler.SetForceExecuteMode(false)
	}
}

// redactSecretLeakInBlocks 对 AI 消息的**可落盘文本**做密钥泄露检测与脱敏（R10 P0-13）。
// 命中密钥模式时，对文本调用 secret.Redact 脱敏并追加警告标记；fail-open：panic 时放行不阻断。
//
// ★ 2026-10-03 覆盖面扩到 thinking 块：思考内容自本日起也会写进会话存档并被 `/share` 导出，
// 它与正文处于**同一个泄露面**。此前只扫 `Type == "text"`，于是「模型把密钥写进推理过程」
// 这条路径会把明文留在 `~/.whale/sessions/*.jsonl` 里 —— 屏幕上被遮住、磁盘上却是明文。
//
// 两类块的处置**必须逐字相同**：这是同一个不变式（"落盘前脱敏"）在两处形态上的体现，
// 不是两条规则。新增可落盘块类型时，在下面同一个 switch 里加分支，不要另开一个函数。
func redactSecretLeakInBlocks(blocks []session.ContentBlock) {
	defer func() {
		if r := recover(); r != nil {
			// fail-open：panic 时放行，不阻断主流程
		}
	}()
	redact := func(text string) string {
		if text == "" {
			return text
		}
		labels, hit := secret.DetectLeakWithLabels(text)
		if !hit {
			return text
		}
		return secret.Redact(text) +
			fmt.Sprintf("\n\n⚠️ [SECRET_LEAK_DETECTED] 检测到密钥模式：%s（已脱敏）", labels)
	}
	for i := range blocks {
		switch blocks[i].Type {
		case "text":
			blocks[i].Text = redact(blocks[i].Text)
		case session.ThinkingBlockType:
			blocks[i].Thinking = redact(blocks[i].Thinking)
		}
	}
}

// resetStreamState 清空一轮流式状态，并重置活动 cell、帧限制器和 spinner 时钟。
func (m *Model) resetStreamState() {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	m.sessionService.SetCurrentText("")
	m.sessionService.SetCurrentThinking("")
	m.toolService.SetPendingTools(nil)
	m.toolService.SetCurrentToolID("")
	m.toolService.SetToolInputBuf(make(map[string]string))
	m.sessionService.SetInterrupted(false)
	m.uiService.SetInputFocusedOnStop(false)
	// active_cell/frame_limiter 重置（2026-08-01 批次2集成）：
	// 流式中断或重置时，清空缓冲标记 + 重置节流状态。
	// nil 守卫覆盖未初始化场景。
	if m.activeCell != nil {
		m.activeCell.Flush()
	}
	if m.frameLimiter != nil {
		m.frameLimiter.Reset()
	}
	m.lastSpinnerAt = time.Time{}
}

// resetAIDetectors 重置 AI 安全检测器状态（循环）。
// 在新用户轮次开始时调用，清空上一轮的循环计数。
// 拒绝处理器的 forceExecuteMode 在 finishTurn 处按单个用户回合清理。
// 所有检测器均为 nil 安全（未初始化时静默跳过）。
func (m *Model) resetAIDetectors() {
	if m.loopDetector != nil {
		m.loopDetector.Reset()
	}
	// R4：结构化拒绝账本按**用户回合**清理，故放在这里（本函数只在新用户轮次开始时调用），
	// 不能放进 resetStreamState —— 那个每轮 AI 流结束都跑，会把同一回合内
	// 前几个工具轮次登记到的拒绝事件清掉，导致合取判据永远看不到它。
	m.roundDeniedKind = ""
}

// checkAISafety 执行 AI 安全检测：无进展、拒绝。
// text: AI 文本响应；hasPendingTools: 当前响应是否含待执行工具调用。
// 检测到警告时追加到消息列表（不阻塞流程，仅提示用户）。
// 无进展检测：每轮 AI 响应均检查（有工具调用=有进展，无工具调用=递增计数）。
// 拒绝检测：仅在最终响应（无待执行工具调用）时执行，避免工具未完成时的误报。
// （幻觉检测已随 移除，2026-10-08。）
// 所有检测器均为 nil 安全（未初始化时静默跳过）。
func (m *Model) checkAISafety(text string, hasPendingTools bool) {
	// V3-6-2：无进展检测（每轮 AI 响应均检查）
	if m.loopDetector != nil {
		if hasPendingTools {
			// 有新工具调用 = 有进展，重置无进展计数
			m.loopDetector.CheckNoProgress(true, false)
		} else {
			// 无工具调用 = 可能无进展，递增计数
			if warn := m.loopDetector.CheckNoProgress(false, false); warn != "" {
				m.addMessage(errorStyle.Render(warn))
			}
		}
	}

	// 仅在最终响应（无待执行工具调用）时检测幻觉和拒绝
	// 避免工具调用未完成时的误报
	if hasPendingTools {
		return
	}

	// 无进展/拒绝检测在此执行。幻觉检测已随 整体移除（2026-10-08 用户拍板）：
	// 事实来源是 tool_result 与盘面状态，解析散文做「声称 vs 证据」比对两头都在猜
	// （mkdir 误报为第 5 例）；业内（两家同类 CLI）亦无此检测器。

	// 拒绝检测 —— 「执行被安全策略拦截」是**可判定的结构化事实**，不是文本印象：
	//
	//	⛔ 分支 = m.roundDeniedKind 非空（本轮真的发生过 sandbox_denied / policy_denied
	// 工具结果事件，由 toolResultMsg 漏斗写入）
	// ∧ 散文在谈安全约束（ai.MentionsSecurityConstraint，**辅助**合取项）
	//
	// 关键词只能"确认"一个已经存在的结构化事件，不能凭空造一个 ——
	// 不要再让它单独决定分支（那会把"任何**提到**沙箱的正常回答"判成执行被拦截）。
	//
	// ★ （2026-09-19 · 用户放行方案 A）：这里**还删掉了另一条独立分支** ——
	// 原先「散文命中拒绝词 ⇒ 断言 AI 拒绝执行 + 弹 force 提示」。删它的理由不是噪音，是：
	//
	//	① 它与本仓铁律冲突：`domains/结构化判据与散文关键词.md` 定的是"可判定的结构化事实
	// 必须由结构化通道断言"，而 **"模型主观选择拒绝"在本协议下没有结构化事实源**
	// （FinishReason 只能表达 content_filter）⇒ 按"证据不足时应静默"，该事实不得被自动断言；
	//	② 真机 T-1 实测它**必然误报**：模型如实回答「我没有 WebSearch 工具…所以我无法直接调用它」
	// —— 这正是 T-1 票面要求的期望行为 —— 却命中关键词「我无法」被判成 AI 拒绝；
	// ⇒ **T-1 通过 ⟺ 必然报误报**（判据自相矛盾）；
	//	③ 更糟的是误导：force 注入的是「请执行而非拒绝」，但此处模型**不是拒绝、是没有能力**，
	// 注入后它手上仍无该工具 ⇒ 只会被推向**编造结果**，即 T-1 票面明列的失败态②（幻觉）。
	// ⇒ 误报的代价不是"多一条提示"，是**把正确行为推向错误行为**。
	//
	// ★ 保留：用户**主动**输入「我确认强制执行」/「force execute」触发的强制覆盖（见 sendInput）；
	// 只是不再自动弹提示。该短语的可发现性走 `/help` 的提示区（internal/command）。
	//
	// 反例纪律：不要为了"覆盖更全"把合取放宽成析取，也不要把散文判据加回来 ——
	// T-1 同时满足「无工具调用 ∧ 无 pending ∧ 请求可执行」⇒ 它就是"更宽的关键词判据"的反例，
	// 加合取项只会把误报往后推。判据的通道错了就得换通道。
	if m.refusalHandler != nil && m.roundDeniedKind != "" && m.refusalHandler.MentionsSecurityConstraint(text) {
		m.addMessage(errorStyle.Render("⛔ Execution blocked by security policy; force confirmation cannot override it. / 执行被安全策略拦截，强制确认不能绕过该限制。"))
	}
}
