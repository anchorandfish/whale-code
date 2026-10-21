package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
	"github.com/anchorandfish/whale-code/internal/util"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"
	"github.com/mattn/go-runewidth"
)

// renderInputArea render input area with textarea and send/stop button
func (m Model) renderInputArea() string {
	// P0-8/9 新增：渲染排队输入预览（在 textarea 上方，与同类 CLI 一致 "above the composer"）。
	// P0 阶段仅 queued messages 段有数据；pending steers / rejected steers 恒为 nil。
	previewView := RenderPendingInputPreview(
		m.QueuedInputsSnapshot(),
		nil, // pendingSteers（P0 恒空，预留扩展点）
		nil, // rejectedSteers（P0 恒空，预留扩展点）
		m.uiService.Width()-4,
	)

	// textarea.View already pads every rendered row to the configured width.
	// Let the border infer that width instead of applying Lipgloss Width here:
	// Width would run a second word-wrap pass, where '-' is always a breakpoint.
	// 输入框选区（composer_selection.go）：有活动选区时在 textarea 视图上叠加
	// 高亮；选区失效/映射失配时原样返回，不改变渲染。
	// ⑧：统一走 composerDecoratedView —— 占位符成块与超链接在所有出口都必须一致。
	taView := inputBoxStyle.Render(m.composerDecoratedView())

	// Place the button against the available row directly. Wrapping it in a
	// second fixed-width style can clip the rounded right/bottom border when a
	// narrow terminal is resized during rendering.
	btnRowWidth := m.uiService.Width() - 4
	btnRow := renderInputActionRow(btnRowWidth, m.sessionService.State() != StateIdle,
		m.uiService.InputFocusedOnStop() || m.uiService.InputFocusedOnSend(), m.pressedBtn)

	// N5 typeahead：若有补全候选，在 textarea 下方追加弹窗渲染
	popupView := ""
	if m.suggestionState != nil && m.suggestionState.Visible {
		popupView = m.suggestionState.renderCompletionPopup(m.uiService.Width() - 4)
	}

	// 拼接顺序：preview(上方) → textarea → btnRow → 剪贴板图片提示(下方) → popupView
	// 与同类 CLI bottom_pane 一致：preview 位于 composer 上方，typeahead 弹窗在下方。
	// ②：剪贴板有图 + 终端聚焦 + 输入框可编辑 ⇒ 提示按键（同类 CLI 模式）。
	// 无定时轮询，状态只随 FocusMsg 探针/贴图消费/空剪贴板报错更新。
	parts := []string{}
	if previewView != "" {
		parts = append(parts, previewView)
	}
	parts = append(parts, taView, btnRow)
	if m.clipboardHasImage && m.terminalFocused && m.canAcceptComposerText() {
		parts = append(parts, pasteHintStyle.Render(bi(
			"Image in clipboard — press Ctrl+V to paste",
			"剪贴板里有图片 —— 按 Ctrl+V 贴上",
		)))
	}
	if popupView != "" {
		parts = append(parts, popupView)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderInputActionRow places transcript copy and send controls on the same
// stable three-row toolbar. Copy all is only shown while idle; the active-turn
// toolbar keeps Stop as its sole command.
//
// pressed 是「鼠标按住中」的按钮身份（档B，见 Model.pressedBtn）：
// 按下即高亮、松开才开火 ⇒ 渲染面必须能把按下态画出来，用户才看得见反馈。
func renderInputActionRow(availableWidth int, stopping, focused bool, pressed string) string {
	action := renderActionButton(availableWidth, stopping, focused, pressed)
	if availableWidth <= 0 || lipgloss.Width(action) > availableWidth {
		return action
	}
	if stopping {
		return lipgloss.PlaceHorizontal(availableWidth, lipgloss.Right, action)
	}

	copySpace := availableWidth - lipgloss.Width(action) - 1
	copy := renderCopyAllButton(copySpace, pressed == btnIdentityCopy)
	if copy == "" {
		return lipgloss.PlaceHorizontal(availableWidth, lipgloss.Right, action)
	}
	gap := strings.Repeat(" ", availableWidth-lipgloss.Width(copy)-lipgloss.Width(action))
	return lipgloss.JoinHorizontal(lipgloss.Top, copy, gap, action)
}

// renderCopyAllButton progressively compacts the label and hides it when the
// terminal is too narrow to keep both action buttons complete. pressed 为真时
// 以 active 样式绘制（：与 Tab 焦点/悬停同一套「engaged」高亮口径）。
func renderCopyAllButton(availableWidth int, pressed bool) string {
	if availableWidth <= 0 {
		return ""
	}
	style := copyBtnStyle
	if pressed {
		style = copyBtnActiveStyle
	}
	for _, label := range []string{" Copy all ", " Copy ", " C "} {
		view := style.Render(label)
		if lipgloss.Width(view) <= availableWidth {
			return view
		}
	}
	return ""
}

// renderActionButton keeps the rounded border intact at narrow widths by
// progressively shortening the label and padding before rendering.
//
// engaged（高亮）= Tab 焦点（focused）或鼠标按下（pressed 命中当前这颗按钮）——
// 两者语义都是「这颗按钮被盯着」，共用 active 样式，不搞两套视觉。
func renderActionButton(availableWidth int, stopping, focused bool, pressed string) string {
	style := sendBtnStyle
	label := " Send ⏎ "
	if stopping {
		style = stopBtnStyle
		label = " Stop ■ "
	}
	engaged := focused
	if !stopping && pressed == btnIdentitySend {
		engaged = true
	}
	if stopping && pressed == btnIdentityStop {
		engaged = true
	}
	if engaged {
		if stopping {
			style = stopBtnActiveStyle
		} else {
			style = sendBtnActiveStyle
		}
	}
	view := style.Render(label)
	if availableWidth > 0 && lipgloss.Width(view) > availableWidth {
		compactLabel := "Send"
		if stopping {
			compactLabel = "Stop"
		}
		view = style.Copy().Padding(0, 1).Render(compactLabel)
	}
	if availableWidth > 0 && lipgloss.Width(view) > availableWidth {
		compactLabel := "S"
		if stopping {
			compactLabel = "X"
		}
		view = style.Copy().Padding(0, 0).Render(compactLabel)
	}
	return view
}

// renderChoiceUI render interactive choice UI
func (m Model) renderChoiceUI() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FFD700")).
		Padding(0, 2)

	var content strings.Builder
	content.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFD700")).Render(m.toolService.PendingChoice().Prompt))
	content.WriteString("\n\n")

	for i, opt := range m.toolService.PendingChoice().Options {
		if i == m.toolService.ChoiceIdx() {
			cursor := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00ADD8")).Render("❯ ")
			label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00ADD8")).Render(opt.Label)
			content.WriteString(cursor + label)
		} else {
			content.WriteString(" " + opt.Label)
		}
		if opt.Description != "" {
			content.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Render(" — " + opt.Description))
		}
		content.WriteString("\n")
		// 中文辅助层：缩进另起一行（形态 SSOT = bilingual.go）。
		// 为空则什么都不加 ⇒ 未填 DescriptionZH 的既有菜单外观完全不变。
		if opt.DescriptionZH != "" {
			content.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Render(copyIndent + opt.DescriptionZH))
			content.WriteString("\n")
		}
	}

	content.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	content.WriteString(hintStyle.Render("↑↓ select · Enter apply · Esc cancel"))

	// 含 Other 哨兵的提问（AskUser 日常对话）追加「按 i 补充自由输入」提示。
	//
	// ★ 这条提示原先承载在 renderPendingChoice —— 该函数**生产零调用**（调用点只有测试），
	// 整个函数被 linker 丢弃（`go tool nm whale | grep -c renderPendingChoice` = 0，
	// 件内文案字节 `补充自由输入` = 0）⇒ 用户界面上从未显示过，功能无从发现。
	// 现并入**唯一活线**（本函数，真实渲染点见 app.go 的 StateWaitingChoice 分支），并删除死线。
	//
	// 子态下改显示输入提示；内容为空时给出「先输入」的明确指引
	//（此时按 enter 会被空答案守卫拦下，不会把空答案回传给模型 —— 见）。
	if m.choiceFreeTextMode {
		if strings.TrimSpace(m.uiService.Textarea().Value()) == "" {
			content.WriteString("\n" + hintStyle.Render(bi("Type your answer, then Enter · Esc to go back", "请先输入内容 · Enter 提交 · Esc 返回选择")))
		} else {
			content.WriteString("\n" + hintStyle.Render(bi("Enter submit · Esc back to options", "Enter 提交 · Esc 返回选择")))
		}
	} else if pc := m.toolService.PendingChoice(); isNoteCapableChoice(pc) {
		// 2026-09-23 · ：两个入口必须说不同的话 ——
		// 修前本行是 `[i] Provide your own answer`，与 Other 选项的 Description 逐字相同
		//（`tool_exec.go` 的 appendOtherOption），于是「给所选方案补一句」和「我自己重新答」
		// 在界面上成了同一件事，用户搞不清按 i 之后到底是「又选又补」还是「另起一个答案」。
		//
		// 语义分工（对齐 同类 CLI request_user_input）：
		// · 按 i → note：依附于当前选择的补充说明（回传 `user_note: ` 独立一条）
		// · 选 Other → 自己作答：回传 otherOptionLabel（"None of the above"）+ 用户写的那句话
		// 同类 CLI 同口径的占位文案见 mod.rs:55 的 "Select an option to add notes"。
		//
		// 2026-09-23 · 两处收口：
		// ① 条件由 `hasOtherOption` 改为 `isNoteCapableChoice` —— Other 只在模型声明
		// `allow_free_text=true` 时才存在，而 note 入口与它无关（同类 CLI 原文：
		// "selecting one option **and/or** providing notes"）。用「有没有 Other」代理
		// 「能不能补 note」，会漏掉 `allow_free_text=false` 的提问面板（有选项、无 Other）。
		// ② 中文辅助行按「本面板有没有 Other」分档 —— 没有 Other 时不再指路
		// 「要自己作答请选 None of the above」（那条路根本不存在）。
		hintZH := "按 i 给所选方案补充一句说明"
		if hasOtherOption(pc.Options) {
			hintZH += " · 要自己作答请选 " + otherOptionLabel
		}
		content.WriteString("\n" + hintStyle.Render(bi("[i] Add a note to your choice", hintZH)))
	}

	box := boxStyle.Width(m.uiService.Width() - 4 - boxStyle.GetHorizontalFrameSize()).Render(content.String())

	// 补充输入子态下，把**真实输入框**渲染在选项框下方。
	//
	// 修前：本函数只渲染提示文案，而 View() 在 StateWaitingChoice 用本函数
	// **完全替换**输入区（app.go:2781 的 if-else 链；renderInputArea 是唯一渲染
	// textarea 的地方）⇒ textarea 内容**无处显示**。字符确实进了 textarea
	//（已保证），但用户按 i 后敲的字在屏幕上完全不可见（手测现象
	//「按 i 之后打的字看不见」）。
	//
	// 渲染复用正常输入框的同一形态（inputBoxStyle + composerDecoratedView），
	// 与 renderInputArea 外观/宽度口径一致。★ 必须同源：绕开装饰层会让
	// 占位符 [Image #N] 在此子态退化成纯文本（⑧ 链接 / ⑨ 成块各半修）。
	if m.choiceFreeTextMode {
		box += "\n" + inputBoxStyle.Render(m.composerDecoratedView())
	}
	return box
}

// renderConfirmUI render safety confirmation UI with interactive selection
// P0-6/P0-7：按 PendingConfirm.Kind 分支渲染
// - ApprovalKindMcpElicitation → renderElicitationUI（3 选项 + Esc 硬编码 Cancel）
// - 其他 Kind → 旧 Y/N 路径（向后兼容，Kind 零值 ApprovalKindExec 走此路径）
func (m Model) renderConfirmUI() string {
	pc := m.toolService.PendingConfirm()
	if pc != nil && pc.Kind == ApprovalKindMcpElicitation {
		return m.renderElicitationUI()
	}
	return m.renderLegacyConfirmUI()
}

// buildApprovalOptions 按审批类型构造动态选项列表，对标同类 CLI build_options
// （REF-外部-同类 CLI-3c7f013f9735）
//
// 参数：
// - kind 审批类型
//
// 返回该类型对应的选项列表。elicitation 类型强制包含 Cancel 选项且 Esc 硬编码映射到它。
func buildApprovalOptions(kind ApprovalKind) []ApprovalOption {
	switch kind {
	case ApprovalKindMcpElicitation:
		// 对标同类 CLI elicitation_options：3 选项，Esc 强制 Cancel
		//（REF-外部-同类 CLI-3c7f013f9735）
		return []ApprovalOption{
			{Label: "✅ Accept (provide info)", Decision: DecisionApproveOnce, Shortcut: "y"},
			{Label: "⛔ Decline (continue without)", Decision: DecisionDeny, Shortcut: "n"},
			{Label: "❌ Cancel", Decision: DecisionCancel, Shortcut: "esc"},
		}
	default:
		// Exec / FileModify / Pipeline: one-time, session, or deny.
		// ★ ：Label 英文主行 + LabelZH 中文辅行
		//（renderLegacyConfirmUI 缩进另行渲染）—— 修前是 "[Y] Allow once / 允许本次"
		// 行内并列（2026-09-18 用户已否决的形态）。
		return []ApprovalOption{
			{Label: "[Y] Allow once", LabelZH: "允许本次", Decision: DecisionApproveOnce, Shortcut: "y"},
			{Label: "[A] Allow for this session", LabelZH: "允许本会话", Decision: DecisionApproveSession, Shortcut: "a"},
			{Label: "[N] Deny, do not run", LabelZH: "拒绝，不执行", Decision: DecisionDeny, Shortcut: "n"},
		}
	}
}

// approvalOptionsFor 是审批面板选项的**唯一生产构造入口**（，2026-10-06）。
//
// 在 `buildApprovalOptions(kind)` 给出的基础集上，仅当**三件事同时成立**时追加
// 「允许该命令前缀」：
// 1. 审批类型是命令类（Exec / Pipeline）—— elicitation / FileModify 不加；
// 2. 被确认的工具是**命令载体**（`permission.IsCommandBearingTool` = {bash, Monitor}）；
// 3. 该命令**确实存在一个前缀**（`cmdrules.CommandHead(toolInput) != ""`，即命令是
// **单一简单命令**）。
//
// ★ 条件 3 是**诚实性要求**，不是优化：复合命令（`sleep 70 && echo x`）、含替换
// （`echo $(date)`）、含重定向（`cmd > f`）**结构性地进不了前缀范围**（`CommandHead`
// 返回空）。若此时仍显示 [P]，用户点了它**什么也不会发生** —— 在安全 UI 上给一个
// "看起来能放宽、实际是空操作"的档位，比不显示更糟。⇒ 只在**真能生效**时显示。
//
// 为什么要按工具过滤（条件 2）：`pipelineConfirmMsg` 承载的是**任意工具**的确认
// （write_file / git_commit 都会走这条），而"允许该命令前缀"对它们没有意义。
//
// 为什么是**收窄包装**而非重写：基础集仍只有一个声明（`buildApprovalOptions`），
// 本函数只做"在 Deny 之前插一项"的追加，不重复任何选项语义（避免选项集双实现漂移，
// 见 双实现坑）。
func approvalOptionsFor(kind ApprovalKind, toolName, toolInput string) []ApprovalOption {
	opts := buildApprovalOptions(kind)
	if kind == ApprovalKindMcpElicitation || kind == ApprovalKindFileModify {
		return opts
	}
	if !permission.IsCommandBearingTool(toolName) {
		return opts
	}
	if cmdrules.CommandHead(permission.ExtractCommand(toolInput)) == "" {
		return opts // 没有可授予的前缀 ⇒ 不显示（诚实性，见上）
	}
	prefixOpt := ApprovalOption{
		Label:    "[P] Allow this command prefix",
		LabelZH:  "允许该命令前缀",
		Decision: DecisionApproveSessionPrefix,
		Shortcut: "p",
	}
	out := make([]ApprovalOption, 0, len(opts)+1)
	inserted := false
	for _, o := range opts {
		// 插在 Deny 之前 ⇒ 顺序 [Y] 本次 / [A] 本会话 / [P] 前缀 / [N] 拒绝
		if !inserted && o.Decision == DecisionDeny {
			out = append(out, prefixOpt)
			inserted = true
		}
		out = append(out, o)
	}
	if !inserted {
		out = append(out, prefixOpt)
	}
	return out
}

// renderElicitationUI 渲染 MCP elicitation 审批 UI，对标同类 CLI build_header McpElicitation 分支
// （REF-外部-同类 CLI-3c7f013f9735）
//
// 显示 server_name + message + 3 选项列表，Esc 提示强制 Cancel。
func (m Model) renderElicitationUI() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#BD93F9")).
		Padding(0, 2)

	pc := m.toolService.PendingConfirm()
	var content strings.Builder
	content.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#BD93F9")).Render("🔔 MCP Elicitation Request"))
	content.WriteString("\n\n")
	if pc != nil {
		// 渲染前二次净化（防御纵深）。McpServerName/McpMessage 虽已在
		// mcp 层 dispatchRequest 净化过，但此处再 strip 一次，确保任何消费点
		// 都不会把外部不可信文本原样写进终端（\r 伪造 / \x1b 清屏 / \x1b]0; 改标题）。
		content.WriteString(fmt.Sprintf(" Server: %s\n", util.SanitizeExternalText(pc.McpServerName)))
		// P0-1 修复：用 truncateRunes 替代 len()+切片，避免 CJK 文本产生 invalid UTF-8
		msgPreview := truncateRunes(util.SanitizeExternalText(pc.McpMessage), 200)
		content.WriteString(fmt.Sprintf(" Message: %s\n", msgPreview))
	}
	content.WriteString("\n")

	selStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00ADD8"))
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	selIdx := 0
	if pc != nil {
		selIdx = pc.SelectIdx
	}

	options := pc.Options
	if len(options) == 0 {
		// 降级：Options 未设置时用 buildApprovalOptions 构造默认选项
		options = buildApprovalOptions(ApprovalKindMcpElicitation)
	}

	for i, opt := range options {
		if i == selIdx {
			content.WriteString(" " + selStyle.Render("❯ "+opt.Label))
		} else {
			content.WriteString(" " + normalStyle.Render("  "+opt.Label))
		}
		content.WriteString("\n")
	}

	content.WriteString("\n")
	content.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Render("↑↓ Select · Enter Confirm · Esc=Cancel (forced)"))

	return boxStyle.Width(m.uiService.Width() - 4 - boxStyle.GetHorizontalFrameSize()).Render(content.String())
}

// renderLegacyConfirmUI renders command/file approvals. The function name is
// retained for compatibility, but the UI now uses the dynamic option list.
func (m Model) renderLegacyConfirmUI() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FF6B6B")).
		Padding(0, 2)

	var content strings.Builder
	// ★ ：框体文案按双语形态 SSOT（bi/biSame）——
	// 英文主行 + 缩进中文辅行，修前是 "Safety confirmation / 安全确认" 行内并列。
	content.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF6B6B")).Render(bi("Safety confirmation", "安全确认")))
	content.WriteString("\n\n")
	pc := m.toolService.PendingConfirm()
	if pc != nil {
		content.WriteString(approvalTargetSummary(pc.ToolName, pc.ToolInput))
		// Risk 行：RiskLevel 已是管线拼好的双语串（英文主行 + 缩进中文辅行，
		// 见 tools/pipeline.go 的 注释）⇒ 原样渲染、只加英文标签。
		// 上限 200→400：双语串比旧的纯中文串长，200 runes 会把中文辅行切断。
		content.WriteString("\nRisk: " + truncateRunes(pc.RiskLevel, 400) + "\n")
		// 「等，但出声」②：面板显示等待计时（由 approvalWaitTick 逐秒刷新），
		// 让 「我在等人、已等多久」可见。不做超时自决。
		if m.approvalWaitSecs > 0 {
			content.WriteString(biSame("Waiting for confirmation (%ds)", "等待确认中（已等 %ds）", m.approvalWaitSecs) + "\n")
		}
	}
	// ★ ：fine print 英文单行（高度预算，同 approval_ui.go 的 B' 决策：
	// 中文辅行集中在标题/选项/Risk 这些决策要素上；这两行是说明性 fine print，
	// 辅行价值低而弹窗高度是 30 行小终端的硬预算）。
	content.WriteString("Decision flow: permission approval first, then sandbox and network checks\n")
	content.WriteString("Approval scope: this tool call only; sandbox and network restrictions still apply\n")
	content.WriteString("\n")

	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#98C379"))
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	selIdx := 0
	if pc != nil {
		selIdx = pc.SelectIdx
	}
	options := buildApprovalOptions(ApprovalKindExec)
	if pc != nil {
		if len(pc.Options) > 0 {
			options = pc.Options
		} else {
			// 命令载体（bash / Monitor）的确认面板才提供「允许该命令前缀」，
			// 且仅当该命令**确实存在前缀**（单一简单命令）时才显示。
			options = approvalOptionsFor(pc.Kind, pc.ToolName, pc.ToolInput)
		}
	}
	for i, option := range options {
		label := "  " + option.Label
		style := normalStyle
		if i == selIdx {
			label = "❯ " + option.Label
			style = selectedStyle
		}
		content.WriteString(" " + style.Render(label) + "\n")
		// 中文辅行缩进另起一行（形态 SSOT = bilingual.go，与 renderChoiceUI 同款）。
		if option.LabelZH != "" {
			content.WriteString(" " + normalStyle.Render(copyIndent+option.LabelZH) + "\n")
		}
	}
	content.WriteString("\n")
	content.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Render("↑↓ select · Enter confirm · Y/A/P/N shortcut · Esc deny"))

	return boxStyle.Width(m.uiService.Width() - 4 - boxStyle.GetHorizontalFrameSize()).Render(content.String())
}

// handleOpenDocNav opens the document navigation panel
func (m Model) handleOpenDocNav() (Model, tea.Cmd) {
	links := ExtractFileLinksFromMessages(m.sessionService.Messages())
	if len(links) == 0 {
		m.addMessage(lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Render("📂 No file links found in conversation"))
		return m, nil
	}
	m.uiService.SetDocLinks(links)
	m.uiService.SetDocLinkIdx(0)
	m.sessionService.SetState(StateViewingDoc)
	m.recalcViewportHeight()
	return m, nil
}

// renderDocNavUI render the document navigation panel
func (m Model) renderDocNavUI() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#8AADF4")).
		Padding(0, 2)

	var content strings.Builder
	content.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#8AADF4")).Render("📂 File References"))
	content.WriteString("\n\n")

	for i, link := range m.uiService.DocLinks() {
		label := FormatFileLinkChoice(link)
		if i == m.uiService.DocLinkIdx() {
			cursor := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00ADD8")).Render("❯ ")
			styled := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00ADD8")).Render(label)
			content.WriteString(cursor + styled)
		} else {
			content.WriteString(" " + label)
		}
		content.WriteString("\n")
	}

	content.WriteString("\n")
	content.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086")).Render("↑↓ Select · Enter Open · Esc Back"))

	return boxStyle.Width(m.uiService.Width() - 4 - boxStyle.GetHorizontalFrameSize()).Render(content.String())
}

// monitorStatusLabel 是 Monitor 状态 →（图标, 文案）的**唯一文案源**。
// 事件回执（handleMonitorEvent）与主界面摘要（renderMonitorStrip）、
// /monitors tail（renderMonitorTail）共用它，满足 ：同一事实跨入口同源，不得各写各的措辞。
//
// ★ B2：`running` 的图标/文案也收进本表。
// 改造前它落到 default 分支（`🏁 running`），摘要区在本函数返回后**再覆盖**一次
// （`icon = "⏳"`）—— 那是同一事实有**两个来源**，正是 要消除的形态。
func monitorStatusLabel(status monitor.MonitorStatus, exitCode int) (string, string) {
	switch status {
	case monitor.StatusRunning:
		return "⏳", "running"
	case monitor.StatusFinished:
		return "✅", "stream ended"
	case monitor.StatusFailed:
		return "❌", fmt.Sprintf("script failed (exit %d)", exitCode)
	case monitor.StatusStopped:
		return "⏹️", "stopped"
	default:
		return "🏁", string(status)
	}
}

// monitorLines 从 Monitor 的**权威副本**读累计输出行数（只读）。
//
// 返回 -1 表示进程已不在管理器里（被 Remove）。调用方必须能区分
// "0 行"与"查不到"，否则会把"刚启动"和"已被清理"显示成同一个样子。
func (m *Model) monitorLines(id string) int {
	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		return -1
	}
	proc, ok := mgr.Get(id)
	if !ok {
		return -1
	}
	return proc.GetLines()
}

// handleMonitorEvent 处理后台 Monitor 事件。
//
// ★ A-2：**output 事件不再写入主对话流**。
//
// 旧实现（本函数改造前）对每一行输出执行
//
//	m.addMessage(monitorStyle.Render(fmt.Sprintf("📡 [%s] %s", ev.monitorID, preview)))
//
// 于是**每一行输出都获得了一条对话消息的身份** ⇒ 它获得了作者身份、获得了解释权，
// 并顺理成章地不受 turn 边界约束。真机实证（I-7.1）：1 行/秒 ⇒ 5 分钟 300 条；
// `Turn complete 02:22:27` 之后 `02:22:30` 仍在写。
//
// 定性（精确版）：**输出本来就有有界权威副本**（internal/monitor:DefaultMaxLines=10000
// + GetOutput/GetLines/List/Get），问题不是"输出没有界"，而是**显示层绕过了权威副本、
// 另建了一份无界副本（对话消息）**。
//
// 新口径（对标 同类 CLI BackgroundTask：主界面零输出、只有一行摘要 + 计数）：
// - 主界面只显示**有界摘要**，且内容**直接投影自权威副本**（见 renderMonitorStrip），
// 本层不再保存任何输出文本 ⇒ 第二份副本从结构上不存在。
// - 要看内容：`/monitors tail <id>`；要停止：`/monitors stop <id>`。
// - 输出的**界**变成用户可见的事实：摘要给行数，列表给"保留 N 行 / 共 M 行"。
//
// done（进程结束）是**状态迁移**，与"丢采样"不同性质：它必须到达界面，
// 否则界面会永久停在 running 而进程其实已退出。因此它仍是一次 Async 作者的
// 写入尝试，交给 A-1 的边界判据裁决 —— turn 结束后不会污染主对话流。
func (m *Model) handleMonitorEvent(ev monitorEvent) tea.Cmd {
	switch ev.eventType {
	case "output":
		// 有意为空实现，且**不是**静默丢弃（后者是本仓明确禁止的形态）：
		// 1) 数据没丢 —— 该行已在 Monitor 的有界权威副本里（LINES_MAX=10000），
		// 摘要与 /monitors 都从那里读；
		// 2) 本分支的作用是"催一次重绘"，因此显式计数（monitorOutputSeen），
		// 可由 /monitors 与判据核对，不会变成不可见事件。
		m.stats().outputSeen.Add(1)
	case "done":
		icon, statusText := monitorStatusLabel(ev.status, ev.exitCode)
		lines := m.monitorLines(ev.monitorID)
		lineInfo := "lines unknown"
		if lines >= 0 {
			lineInfo = fmt.Sprintf("%d lines", lines)
		}
		// ★ 单行、有界：不再把 500 字输出塞进对话流（那也是第二份无界副本）。
		// 全文留在权威副本，读取入口在提示里点名。
		m.addMessageFrom(AuthorAsync, monitorStyle.Render(fmt.Sprintf(
			"📡 %s %s · %s · %s · /monitors tail %s",
			ev.monitorID, icon, statusText, lineInfo, ev.monitorID,
		)))
		// ★ C（2026-10-13，手测卡② §19.5）：同时把这条**事实**（id/终态/退码）记入
		// "待推给模型"队列，等下一次构建请求时注入模型上下文 —— 显示层与模型层由此
		// 各得其所：屏幕得到一行摘要，模型得到"该去看哪台"的触发点（不再盲猜时机）。
		// 只记事实、不带内容（ev.output 不参与）。
		m.recordMonitorCompletion(ev)
	}

	// N-MOUSE-ROW：Monitor 摘要区的行数随 running 台数变化，
	// 而它的高度占的是 viewport 的预算（见 recalcViewportHeight）。
	// 只有在这里重算，View() 的总行数才会重新等于终端高度 ——
	// 否则 bubbletea 砍掉顶部多出的行 ⇒ 鼠标拖选位移（用户报障）。
	m.recalcViewportHeight()

	// Continue waiting for next monitor event
	return waitForMonitorEvent(m.toolService.MonitorCh())
}

// monitorStripMaxRows 是主界面 Monitor 摘要区的**详情行**上限。
//
// ★ B2：从 3 收到 **1** —— 常驻面恒为「一行详情 + 一行溢出计数」。
// 口径对齐两类竞品（皆 file:line 实读）：
// - 同类 CLI：对标证据件 —— 恒为单行 DIM
// （`N background terminal(s) running · /ps to view · /stop to close`）；
// - 同类 CLI：src/components/tasks/BackgroundTaskStatus.tsx —— 单行 pill。
//
// 多台时的具体 id 交给按需出口 `/monitors`（它列全部并带启动时长），不挤在常驻面。
const monitorStripMaxRows = 1

// renderMonitorStrip 渲染主界面的 Monitor 摘要区（A-2 的"一行摘要 + 计数"）。
//
// 关键结构性质：**数据全部现读自权威副本**（internal/monitor.Manager），
// 本层不缓存、不累积任何输出文本。因此：
// - 事件被丢弃不会造成"界面与真相对不上"（下一次读到就是真值）；
// - 进程在 turn 结束后才退出，摘要照样能反映（renderMonitorStrip 每次 View 都重算）。
//
// ★ B2：**只渲染 running**（见函数体内的过滤）。
//
//	常驻面是"正在发生什么"，不是"曾经发生过什么" —— 历史走按需出口。
//
// 与状态栏角标分工：这里给"每台一行 + 计数"，状态栏给"停止入口"。
// 无**运行中**的 Monitor 时返回空串（不占屏）。
func (m *Model) renderMonitorStrip() string {
	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		return ""
	}
	// ★ B2：**只渲染 running**。
	// 改造前直接渲染 mgr.List() 全量、不看状态 ⇒ 已停止/已结束的条目永久占屏，
	// 用户真机报障：「我 stop 了它也没有消失，只是显示 stop 的状态」。
	// 口径对齐 同类 CLI（footer 只列 running terminals）/ 同类 CLI（无运行任务则整块消失）。
	// 历史记录仍在权威副本里（有界保留，见 monitor.DefaultMaxRetainedFinished），
	// 读取出口是 /monitors（按需列表）与 /monitors tail <id>，不占常驻面。
	var procs []*monitor.Process
	for _, p := range mgr.List() {
		if p.GetStatus() == monitor.StatusRunning {
			procs = append(procs, p)
		}
	}
	if len(procs) == 0 {
		return ""
	}
	// 稳定顺序：List 走 map 遍历，顺序随机 ⇒ 必须排序，否则摘要区每次重绘都在跳。
	sort.Slice(procs, func(i, j int) bool { return procs[i].ID < procs[j].ID })

	width := m.uiService.Width() - 4
	if width <= 0 {
		return ""
	}

	var lines []string
	for i, proc := range procs {
		if i >= monitorStripMaxRows {
			break
		}
		// 状态文案走 SSOT；这里**不再覆盖** icon —— running 的 ⏳ 已由
		// monitorStatusLabel 给出（B2 收口了那处"同一事实双来源"）。
		icon, statusText := monitorStatusLabel(proc.GetStatus(), proc.ExitCode)
		desc := truncateRunewidthWithEllipsis(proc.Description, 24)
		if desc == "" {
			desc = "(no description)"
		}
		lines = append(lines, monitorStyle.Render(fmt.Sprintf(
			"📡 %s · %s · %s · %d lines", proc.ID, desc, icon+" "+statusText, proc.GetLines(),
		)))
	}
	if extra := len(procs) - monitorStripMaxRows; extra > 0 {
		lines = append(lines, monitorStyle.Render(fmt.Sprintf("   … +%d more (/monitors)", extra)))
	}
	// A-5：把"被丢弃的通知"变成可见事实（可丢，但不可不可见）。
	lines = append(lines, monitorDroppedNote(m))

	for i := range lines {
		lines[i] = truncateRunewidthWithEllipsis(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

// monitorDroppedNote 渲染丢弃计数说明；无丢弃时返回空串。
// 区分两类（严重性不同，见 app.go 字段注释）：丢采样可辩护，丢迁移是异常。
func monitorDroppedNote(m *Model) string {
	var parts []string
	if n := m.droppedOutput(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d output notifications coalesced", n))
	}
	if n := m.droppedDone(); n > 0 {
		parts = append(parts, fmt.Sprintf("⚠️ %d completion events LOST", n))
	}
	if n := m.droppedLoop(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d loop triggers dropped", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return monitorStyle.Render("   " + strings.Join(parts, " · "))
}

// monitorBadge 返回状态栏的 Monitor 角标；无**运行中**的 Monitor 时返回空串（A-4）。
//
// 为什么必须常驻：`/monitors stop <id>` 是后台进程**唯一**的停止出口，
// 而它所在的命令在命令面被 Hidden 藏起 ⇒ 用户既不知道该敲什么，也不知道
// "有东西正在后台跑"。角标把"存在后台进程"和"怎么停"同时印在常驻可见处。
//
// ★ B2：**删掉**了 `running == 0` 时的
// `📡 N monitor(s) done (/monitors)` 残留分支 —— 正是它让"全部停了"仍占着状态栏
// （用户报障：「我 stop 了它也没有消失」）。角标只回答"现在有几个在跑"；
// 已完成的记录仍在权威副本里（有界保留），要看走 `/monitors`。
//
// ★ 只做 TUI 这一侧的显性化：是否把 /monitors 的 Hidden 改为 false
// 不由本会话决定（属命令面，另一会话/人工拍板）。
func (m *Model) monitorBadge() string {
	mgr := m.toolService.MonitorMgr()
	if mgr == nil {
		return ""
	}
	running := mgr.RunningCount()
	if running == 0 {
		return ""
	}
	return monitorStyle.Render(fmt.Sprintf(
		"📡 %d monitor%s running · stop: /monitors stop <id>", running, pluralS(running),
	))
}

// statusBarPart 状态栏组件（带优先级，用于窄屏降级）
type statusBarPart struct {
	text     string // 显示文本
	priority int    // 优先级（越小越重要，0=必须保留永不丢弃）
}

// renderStatusBar 渲染状态栏（窄屏按优先级降级，对标同类 CLI footer 逐级降级）。
// 降级策略：用 runewidth.StringWidth 计算各 part 宽度，从最低优先级开始丢弃，
// 直到总宽度 <= 可用宽度；仅剩 priority=0 的 parts 仍超宽时，截断追加 …。
// 优先级表：0=model+turn+ctx（永不丢弃）/ 1=state+vim / 2=in/out / 3=cost / 4=policy / 5=monitor / 6=🔒local
// v1.2：恢复 turn 为独立 part（priority=0），解决 合并导致 model 行变长挤掉 cost 问题。
// REF-外部: 同类 CLI single_line_footer_layout
// REF-外部: 同类 CLI truncate_line_with_ellipsis_if_overflow
func (m *Model) renderLegacyStatusBar() string {
	m.reassignServices() // 修复服务指针别名：指向当前实例自身子结构体
	// 口径一致性：与 renderStatusBar / 预警判定一致，使用完整估算（system+tools+messages）。
	estimated := m.estimateCurrentInputContextTokens()
	usagePercent := m.aiService.Budget().UsagePercent(estimated)
	availableWidth := m.uiService.Width() - 4
	// 子审查致命漏洞1修复：availableWidth <= 0 兜底（极端窄屏或初始化阶段 Width()=0）
	if availableWidth <= 0 {
		return ""
	}

	// 按 priority 构建 parts（0=必须保留，1=关键状态，2-6=次要）
	var parts []statusBarPart
	// v1.2：恢复 turn 为独立 part（priority=0），model 行恢复短长度避免挤掉 cost
	parts = append(parts, statusBarPart{fmt.Sprintf("🐋 Whale %s", m.sessionService.ModelName()), 0})
	parts = append(parts, statusBarPart{fmt.Sprintf("ctx %d/%d(%.0f%%)", estimated, m.sessionService.Config().ContextWindow, usagePercent), 0})
	// turn 独立 part（priority=0 永不丢弃），解决窄屏缩放 turn 消失问题
	parts = append(parts, statusBarPart{fmt.Sprintf("turn %d", m.sessionService.CurrentSession().Metadata.TurnCount), 0})

	if m.sessionService.InputTokens() > 0 || m.sessionService.OutputTokens() > 0 {
		parts = append(parts, statusBarPart{fmt.Sprintf("in:%d out:%d", m.sessionService.InputTokens(), m.sessionService.OutputTokens()), 2})
		// cost 字段已移除：provider 不返回费用详情，本地价格表估算与真实账单不符。
	}
	// policy 单独保留（priority=4，窄屏可丢）
	parts = append(parts, statusBarPart{m.toolService.Policy().Mode().String(), 4})

	if m.toolService.MonitorMgr() != nil {
		running := m.toolService.MonitorMgr().RunningCount()
		if running > 0 {
			parts = append(parts, statusBarPart{fmt.Sprintf("📡 %d monitor%s", running, pluralS(running)), 5})
		}
	}

	// 状态指示符（priority=1，仅次于品牌和上下文）
	if m.sessionService.State() == StateStreaming {
		spinner := spinnerChars[m.uiService.SpinnerIdx()]
		parts = append(parts, statusBarPart{fmt.Sprintf("%s thinking", spinner), 1})
	} else if m.sessionService.State() == StateToolExec {
		spinner := spinnerChars[m.uiService.SpinnerIdx()]
		parts = append(parts, statusBarPart{fmt.Sprintf("%s running tools", spinner), 1})
	} else if m.sessionService.State() == StateWaitingChoice {
		parts = append(parts, statusBarPart{"⏳ waiting for your choice", 1})
	} else if m.sessionService.State() == StateWaitingConfirm {
		parts = append(parts, statusBarPart{"⚠️ safety: Y=approve N=cancel", 1})
	} else if m.sessionService.State() == StateIdle && len(m.sessionService.Messages()) > 0 {
		parts = append(parts, statusBarPart{"✅ ready", 1})
	}

	// onboarding 完成后追加本地模式标识（priority=6 最低，窄屏最先丢弃）
	if m.onboardingCompleted {
		parts = append(parts, statusBarPart{"🔒local", 6})
	}

	// Vim mode（priority=1，vim 用户必须看到当前模式）
	if m.uiService.VimMode() {
		modeStr := "-- INSERT --"
		if m.uiService.VimModeState() == VimNormal {
			modeStr = "-- NORMAL --"
		}
		parts = append(parts, statusBarPart{modeStr, 1})
	}

	separator := " | "
	sepWidth := runewidth.StringWidth(separator)

	// 窄屏降级：从最高 priority（最次要）开始丢弃，直到总宽度 <= 可用宽度或只剩 priority=0
	for {
		totalWidth := computeStatusBarWidth(parts, separator, sepWidth)
		if totalWidth <= availableWidth {
			break
		}
		// 找到最高 priority（最次要）的非 priority=0 part 丢弃
		dropIdx := -1
		maxPriority := 0
		for i, p := range parts {
			if p.priority == 0 {
				continue // 必须保留
			}
			if p.priority >= maxPriority {
				maxPriority = p.priority
				dropIdx = i
			}
		}
		if dropIdx == -1 {
			break // 只剩 priority=0 的 parts，无法再丢
		}
		parts = append(parts[:dropIdx], parts[dropIdx+1:]...)
	}

	// 收集保留的 parts
	visibleParts := make([]string, 0, len(parts))
	for _, p := range parts {
		visibleParts = append(visibleParts, p.text)
	}

	joined := strings.Join(visibleParts, separator)
	// 若仍超宽（仅剩必须保留的 parts），截断追加 …
	if runewidth.StringWidth(joined) > availableWidth {
		joined = truncateRunewidthWithEllipsis(joined, availableWidth)
	}
	return statusStyle.Width(availableWidth).Render(joined)
}

// renderStatusBar renders a fixed footer with required runtime information.
// Each required group is wrapped independently, so narrow terminals keep
// fallback, fast mode, permission, and workspace visible instead of dropping
// fields by priority.
//
// G12：footer 必须**常驻**同时报出两个正交轴 ——
// `Permission:` 是审批轴（问不问），`Sandbox:` 是能力轴（拦不拦得住）。
// 只报前者会让用户以为"权限"已经把沙箱包含在内（实测用户反馈的误解点），
// 而两轴模型只有在两轴都看得见时才诚实。沙箱关闭时该字段带 T1 标记
// （关沙箱 = 危险命令降级到权限层，属"降级不得静默"的可见性要求）。
func (m *Model) renderStatusBar() string {
	m.reassignServices()
	contentWidth := m.uiService.Width() - statusStyle.GetHorizontalFrameSize()
	if contentWidth <= 0 {
		return ""
	}
	// 口径一致性：与预警/自动压缩判定一致，使用完整估算（system+tools+messages），
	// 避免出现"状态栏 2% 但预警说 75%"的自相矛盾（对标同类 CLI footer 显示完整请求 token）。
	estimated := m.estimateCurrentInputContextTokens()
	usagePercent := m.aiService.Budget().UsagePercent(estimated)
	cfg := m.sessionService.Config()
	fallbackConfigured := "none"
	contextMax := 0
	fastModel := "unset"
	fastEnabled := false
	if cfg != nil {
		if cfg.FallbackModel != "" {
			fallbackConfigured = cfg.FallbackModel
		}
		contextMax = cfg.ContextWindow
		if cfg.FastModel != "" {
			fastModel = cfg.FastModel
		}
		fastEnabled = cfg.FastModeEnabled
	}
	if fastModel == "" || fastModel == "unset" {
		if configured := m.sessionService.FastModel(); configured != "" {
			fastModel = configured
		}
	}
	fallbackState := "standby"
	if tracker := m.aiService.Fallback(); tracker != nil && tracker.IsDegraded() {
		fallbackState = "active"
	}
	permissionMode := "unknown"
	if policy := m.toolService.Policy(); policy != nil {
		permissionMode = policy.Mode().String()
	}
	workspace := m.sessionService.WorkDir()
	if workspace == "" {
		workspace = "(unset)"
	} else if home := m.sessionService.HomeDir(); home != "" && strings.HasPrefix(workspace, home) {
		workspace = "~" + strings.TrimPrefix(workspace, home)
	}

	workspaceField := singleLineStatusField("Workspace: ", workspace, contentWidth)
	fields := []string{
		fmt.Sprintf("Model: %s", m.sessionService.ModelName()),
		fmt.Sprintf("Fallback: %s (%s)", fallbackConfigured, fallbackState),
		fmt.Sprintf("Fast: %s (%s)", fastModel, boolStatus(fastEnabled)),
		fmt.Sprintf("Context: %d/%d (%.0f%%)", estimated, contextMax, usagePercent),
		fmt.Sprintf("Permission: %s", permissionMode),
		// G12：能力轴常驻字段。走 sandboxAxisBadge（sandbox_toggle.go 的单点判定），
		// **不**在这里另写一处 exec.Sandbox.Enabled() —— 那正是 A1 修过的假报面。
		fmt.Sprintf("Sandbox: %s", sandboxAxisBadge(m.toolService.Executor())),
		workspaceField,
	}
	// A-4：Monitor 角标 —— "有东西在后台跑" + "怎么停"都必须在常驻可见处。
	// 置顶与 unread 角标同级：它是"界面之外正在发生的事"的唯一可见信号。
	if badge := m.monitorBadge(); badge != "" {
		fields = append([]string{badge}, fields...)
	}
	// 未读角标**置顶** —— 它是"你看不见的那部分内容"的唯一可见信号，
	// 必须优先占用状态栏第一行，不能被 Model/Context 等常驻字段挤到下一行。
	// 可见条件含 `!AtBottom()`：用户按 End 回到底部的**当帧**角标即消失，不必等下一次 Update 清零。
	if m.unreadBelow > 0 && !m.uiService.Viewport().AtBottom() {
		// P3：回底提示必须写出**本平台物理键盘上真实存在**的那个键。
		// 修前这里硬编码 "(End)"，而 mac 笔记本**没有独立的 End 键**（End = Fn+→）
		// ⇒ 全应用唯一教你回底的提示，在用户手上那台机器上**按不出来**
		//（正是 「同一语义两处实现」：键名在 viewportNavKey 里另写了一份）。
		// 键名与文案的唯一声明 = jumpToBottomHintFor（app.go，与 viewportNavKey 相邻）；
		// 本处只取 Label，不再出现任何键名字面量。
		hint := jumpToBottomHint()
		fields = append([]string{
			unreadStyle.Render(fmt.Sprintf("⏬ Unread: %d below (%s)", m.unreadBelow, hint.Label)),
		}, fields...)
	}
	if m.uiService.VimMode() {
		mode := "INSERT"
		if m.uiService.VimModeState() == VimNormal {
			mode = "NORMAL"
		}
		fields = append(fields, "Mode: "+mode)
	}
	lines := packStatusFields(fields, " │ ", contentWidth)
	return statusStyle.Width(contentWidth).Render(strings.Join(lines, "\n"))
}

func boolStatus(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func singleLineStatusField(label, value string, width int) string {
	field := label + value
	if width <= 0 || runewidth.StringWidth(field) <= width {
		return field
	}
	labelWidth := runewidth.StringWidth(label)
	valueWidth := runewidth.StringWidth(value)
	availableValueWidth := width - labelWidth
	if availableValueWidth <= 1 {
		return truncateRunewidthWithEllipsis(field, width)
	}
	// Keep the final path component visible; it usually carries more useful
	// workspace identity than the repeated parent-directory prefix.
	cutWidth := valueWidth - availableValueWidth + 1 // +1 reserves the ellipsis.
	return label + runewidth.TruncateLeft(value, cutWidth, "…")
}

func packStatusFields(fields []string, separator string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	current := ""
	for _, field := range fields {
		if runewidth.StringWidth(field) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			lines = append(lines, wrapStatusLine(field, width)...)
			continue
		}
		candidate := field
		if current != "" {
			candidate = current + separator + field
		}
		if runewidth.StringWidth(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = field
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

// wrapStatusLine wraps by terminal cell width, including CJK and emoji.
func wrapStatusLine(line string, width int) []string {
	if width <= 0 {
		return nil
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return []string{""}
	}
	var out []string
	for len(runes) > 0 {
		used, end := 0, 0
		for end < len(runes) {
			w := runewidth.RuneWidth(runes[end])
			if end > 0 && used+w > width {
				break
			}
			used += w
			end++
			if used >= width {
				break
			}
		}
		if end == 0 {
			end = 1
		}
		out = append(out, string(runes[:end]))
		runes = runes[end:]
	}
	return out
}

// computeStatusBarWidth 计算状态栏 parts 的总显示宽度（含分隔符，用 runewidth 正确处理 CJK）
func computeStatusBarWidth(parts []statusBarPart, separator string, sepWidth int) int {
	total := 0
	for _, p := range parts {
		total += runewidth.StringWidth(p.text)
	}
	if len(parts) > 1 {
		total += sepWidth * (len(parts) - 1)
	}
	return total
}

// truncateRunewidthWithEllipsis 按显示宽度截断并追加 …（对标同类 CLI truncate_line_with_ellipsis_if_overflow）
// REF-外部: 同类 CLI
func truncateRunewidthWithEllipsis(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 1 {
		return "…"
	}
	return runewidth.Truncate(s, maxWidth-1, "…")
}

// renderMessages render message list with visual separation and horizontal offset
//
// ★ ：**只有一条渲染路径** —— 全量拼接。
//
// 为什么删掉 N13 的「超阈值只渲染可见窗口」分支（第一性原理）：
// - 显示缓冲（SessionModel.messages）里存的**已经是渲染完成**的显示串
// （表头 + glamour + 分隔线），没有剩下可"虚拟渲染"的工作；
// - 本仓的滚动语义**全部**建立在 bubbles viewport 的「内容长度驱动」模型上
// （viewportNavKey 导航键 / 未读角标 / 滚轮 / resize 保距 / resume GotoBottom
// 都读写 Viewport().YOffset / AtBottom()）；
// - 只把可见窗口喂给 viewport ⇒ TotalLineCount ≈ Height ⇒ maxOffset = 0
// ⇒ AtBottom() 恒真、YOffset 恒 0 ⇒ 永远停在**最旧一屏**且无法下滑
// （真机报障「历史跳回去、切不回来」）。
// - 对照：同类 CLI 的 fullscreen 渲染同样"只画可见消息"，但它**自己持有滚动偏移**
// （应用内滚动 + auto-follow），不把可见窗口喂给内容长度驱动的视口 —— 两种滚动模型
// 不能混用。
//
// 该分支另有两处同样只在 >50 条时现形的错，删分支后一并消失：
// ① 对已渲染串再跑一遍 glamour（双重渲染）；② 丢掉消息间的分隔线（下面全量路径有）。
//
// 遗留：engine.go / virtual_scroll.go 的 VirtualScroll 子系统仍在盘（判据保留），
// 但**不再参与转录输出**（其 PrefetchBuffer 从未接线）。彻底清理属独立一项，勿在此恢复旧分支。
func (m *Model) renderMessages() string {
	text, _ := m.renderTranscript()
	return text
}

// renderTranscript 是 renderMessages 的**带 source map**版本：返回转录文本 +
// **逐逻辑行**的真值来源（与 strings.Split(text,"\n") 等长）。
//
// 全量拼接（：只有一条渲染路径）。分隔线（`─` / 空行）本身不是内容，
// 其来源为零值（复制按可见字面取，与旧行为一致）。
//
// 「逐逻辑行」是硬折行的关键：本函数产出的行还没经过 Hardwrap，
// 折行后的 N 条视觉行由 wrapTranscriptWithMap 统一回指**同一条逻辑行**，
// 于是复制按逻辑行取数、天然只取一次（读不到折后产物）。
func (m *Model) renderTranscript() (string, []lineSource) {
	// A-3：渲染条目 = 显示消息 + 轮末结构标记。
	// 标记**不是消息**（不进 messageBuffer、不进模型侧 session），只在这里被投影出来。
	entries := m.displayEntries()

	var sb strings.Builder
	srcs := make([]lineSource, 0, 64)
	put := func(offset int, s lineSource) {
		for len(srcs) <= offset {
			srcs = append(srcs, lineSource{})
		}
		srcs[offset] = s
	}
	// 排版收敛：assistant 消息尾部已自带横线（format.go），join 的
	// 单 ─ 若紧跟其后即成连续双线（用户可见噪音，判据 TestTurnLayout_NoConsecutiveRuleLines）
	// ⇒ 前一条消息末行已是横线时，改为以空行分隔（保留呼吸感，不再叠线）。
	line := 0
	for i, entry := range entries {
		if i > 0 {
			var sep string
			if isHorizontalRuleLine(lastNonBlankLine(entries[i-1].text)) {
				sep = "\n\n"
			} else {
				sep = "\n" + separatorStyle.Render("─") + "\n"
			}
			sb.WriteString(sep)
			line += strings.Count(sep, "\n")
		}
		sb.WriteString(entry.text)
		count := strings.Count(entry.text, "\n")
		for j := 0; j <= count; j++ {
			put(line+j, sourceAt(entry.sources, j))
		}
		line += count
	}
	joined := sb.String()
	if m.uiService.XOffset() > 0 {
		joined = applyXOffset(joined, m.uiService.XOffset())
	}
	return joined, srcs
}

// wrapTranscriptWithMap 对**逻辑转录行**逐行硬折行，产出「折行文本 + 逐视觉行的
// 真值来源 + 视觉行→逻辑行号」。
//
// 折行实现与 wrapViewportContent **同源**（TextMeasurer.WrapText，全仓唯一折行实现），
// 只是多吐一张 owner 表。N 条视觉行共享同一条逻辑行的来源 ⇒ 复制按逻辑行取数，
// 硬折行不再需要单独规则，也不必去改 WrapText（已拍板"勿改 WrapText"）。
//
// 为什么逐行折行等价于整篇折行：底层 ansi.Hardwrap 遇 \n 即重置行宽
// （vendor/.../ansi/wrap.go:83-86），整篇折行 = 逐逻辑行折行后按原顺序拼接。
func (m *Model) wrapTranscriptWithMap(text string, srcs []lineSource) (string, []lineSource, []int) {
	logical := strings.Split(text, "\n")
	width := m.contentWrapWidth()

	out := make([]string, 0, len(logical))
	owner := make([]int, 0, len(logical))
	for i, line := range logical {
		var wrapped string
		switch {
		case width <= 0:
			wrapped = line
		case m.textMeasurer != nil:
			wrapped = m.textMeasurer.WrapText(line, width)
		default:
			wrapped = cellbuf.Wrap(line, width, "")
		}
		parts := strings.Split(wrapped, "\n")
		for range parts {
			owner = append(owner, i)
		}
		out = append(out, parts...)
	}

	visualSrcs := make([]lineSource, len(owner))
	for k, li := range owner {
		visualSrcs[k] = sourceAt(srcs, li)
	}
	return strings.Join(out, "\n"), visualSrcs, owner
}

// applyXOffset 应用水平偏移（按 rune 切片，保留旧逻辑）。
//
// 提取为独立函数便于 renderMessages 旧路径与虚拟滚动路径复用。
func applyXOffset(s string, xOffset int) string {
	if xOffset <= 0 {
		return s
	}
	var sb strings.Builder
	for _, line := range strings.Split(s, "\n") {
		runes := []rune(line)
		if xOffset < len(runes) {
			sb.WriteString(string(runes[xOffset:]))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// contentWrapWidth 返回视口内容的统一折行宽度，是所有自绘元素宽度的唯一口径。
//
// wrapViewportContent 按输入框文本宽度（viewport.Width - inputBoxStyle 横向 frame）
// 硬折行（ansi.Hardwrap）；思考边框等自绘元素必须按同一宽度绘制，否则边框行超出
// 折行宽度，右侧角标（╮/│/╯）会被折到下一行错位（2026-09-08 修复）。
func (m *Model) contentWrapWidth() int {
	width := m.uiService.Viewport().Width
	// The composer has a border and left padding, so its actual text width is
	// smaller than the conversation viewport. Reflow history to the same width
	// to keep the line breaks stable before and after a message is sent.
	if w := width - inputBoxStyle.GetHorizontalFrameSize(); w > 0 {
		return w
	}
	return width
}

// wrapViewportContent wraps content to viewport width so that logical lines
// match physical lines and the viewport does not truncate bottom content.
//
// N13 G-05：cellbuf.Wrap 按 rune 数切片，CJK 宽字符错位；
// 改用 TextMeasurer.WrapText 按显示宽度换行。未初始化时回退 cellbuf.Wrap。
func (m *Model) wrapViewportContent(content string) string {
	width := m.contentWrapWidth()
	if width <= 0 {
		return content
	}
	if m.textMeasurer != nil {
		return m.textMeasurer.WrapText(content, width)
	}
	// 降级路径：textMeasurer 未初始化时回退 cellbuf.Wrap。
	return cellbuf.Wrap(content, width, "")
}

// formatStatus format full status info
func (m *Model) formatStatus() string {
	var sb strings.Builder
	// 5维度审查修复：统一引用 m.cmdRegistry.Version，消除硬编码 v0.1.0
	if m.cmdRegistry != nil && m.cmdRegistry.Version != "" {
		sb.WriteString(fmt.Sprintf("Whale Code v%s\n", m.cmdRegistry.Version))
	} else {
		sb.WriteString("Whale Code\n")
	}
	if m.sessionService.CurrentSession().Metadata.Name != "" {
		sb.WriteString(fmt.Sprintf("Session: %s\n", m.sessionService.CurrentSession().Metadata.Name))
	}
	if m.sessionService.CurrentSession().Metadata.CreatedAt > 0 {
		created := time.UnixMilli(m.sessionService.CurrentSession().Metadata.CreatedAt)
		sb.WriteString(fmt.Sprintf("Created: %s\n", created.Format("2006-01-02 15:04:05")))
	}
	if m.sessionService.CurrentSession().Metadata.UpdatedAt > 0 {
		updated := time.UnixMilli(m.sessionService.CurrentSession().Metadata.UpdatedAt)
		sb.WriteString(fmt.Sprintf("Updated: %s\n", updated.Format("2006-01-02 15:04:05")))
	}
	sb.WriteString(fmt.Sprintf("Model: %s\n", m.sessionService.ModelName()))
	sb.WriteString(fmt.Sprintf("Permission: %s\n", m.toolService.Policy().Mode()))
	budget := m.aiService.Budget()
	estimated := m.estimateCurrentTokens()
	sb.WriteString(fmt.Sprintf("Context: %.0f%% (%d/%d tokens)\n",
		budget.UsagePercent(estimated),
		estimated,
		m.sessionService.Config().ContextWindow))
	sb.WriteString(fmt.Sprintf("Tokens: in %d / out %d\n", m.sessionService.InputTokens(), m.sessionService.OutputTokens()))
	sb.WriteString(fmt.Sprintf("Turns: %d\n", m.sessionService.CurrentSession().Metadata.TurnCount))
	sb.WriteString(fmt.Sprintf("Messages: %d\n", len(m.sessionService.CurrentSession().GetMessages())))
	sb.WriteString(fmt.Sprintf("Workdir: %s\n", m.sessionService.WorkDir()))
	// ★ 三态文案与 /think 回执**同源**（thinkingModeLabel，见 format.go）——
	// 满足 ：同一事实跨入口必须同源，不得各写各的措辞。
	sb.WriteString(fmt.Sprintf("Thinking: %s\n", thinkingModeLabel(m.sessionService.ThinkingEnabled())))
	if m.aiService.Fallback().IsDegraded() {
		sb.WriteString(fmt.Sprintf("! Model degraded: %s -> %s\n",
			m.sessionService.Config().Model, m.aiService.Fallback().ActiveModel()))
	}
	return sb.String()
}

// categoryTags 返回 category 对应的（英文主行标签, 中文辅行标签）（REF-T3-S2-新增）。
// 5 类 category 分别对应：
// - clarify：编排提问-澄清
// - plan-confirm：编排提问-方案确认
// - cost-warning：编排提问-成本警告
// - scope-confirm：编排提问-范围确认
// - dialogue-clarify：对话澄清（日常对话场景）
// 空字符串或未知值返回 ("", "")（向后兼容）。
//
// ★ ：原 `renderCategoryLabel` 只返回**纯中文**标签
// （如 `[编排提问-澄清] `）。现拆成 (英文主行, 中文辅行) 两侧，
// 由 `renderAskQuestionLine` 拼成双语形态 SSOT（不再单侧中文、也不做行内并列）。
func categoryTags(category string) (en, zh string) {
	switch category {
	case "clarify":
		return "[Orchestrator · Clarify]", "[编排提问-澄清]"
	case "plan-confirm":
		return "[Orchestrator · Plan confirm]", "[编排提问-方案确认]"
	case "cost-warning":
		return "[Orchestrator · Cost warning]", "[编排提问-成本警告]"
	case "scope-confirm":
		return "[Orchestrator · Scope confirm]", "[编排提问-范围确认]"
	case "dialogue-clarify":
		return "[Dialogue · Clarify]", "[对话澄清]"
	default:
		return "", ""
	}
}

// renderAskQuestionLine 渲染「❓ 提问行」的双语形态：
// 英文主行 = 英文标签 + 提问；中文辅行 = 缩进的中文标签（空 category 时不产生孤立行）。
//
// ★ 形态理由：本行把「类别标签」与「模型写的提问」拼在一行。若直接把中文标签
// 塞在标签位，提问会**掉到中文辅行上** ⇒ 英文主行读不出问题本身。故把**提问留在英文主行**，
// 中文标签单独缩进一行。提问是模型产出，不属形态门管辖（技能 §4 的例外）。
func renderAskQuestionLine(category, prompt string) string {
	en, zh := categoryTags(category)
	if en == "" {
		return "❓ " + prompt
	}
	line := "❓ " + en + " " + prompt
	if zh != "" {
		line += "\n" + copyIndent + zh
	}
	return line
}

// hasOtherOption 判断选项列表末尾是否含 Other 哨兵项（对齐 同类 CLI is_other）。
func hasOtherOption(options []ChoiceOption) bool {
	return len(options) > 0 && options[len(options)-1].Value == otherOptionValue
}

// isNoteCapableChoice 报告该选择面板是否允许用户补充说明（note）。
//
// ★ 这是「note 入口是否可用」的**唯一判定** —— `renderChoiceUI` 的 `[i]` 提示行与
// `app.go` 里 `i` 键的进入分支**必须都走它**。修前两处各写一份条件（提示看 `hasOtherOption`、
// 按键看 ToolID 白名单），两边一旦分叉就会出现「界面不提示、按键却有反应」的隐性功能
// （2026-09-23 · 收口；同一天 的病根也是「两个入口说不清各是什么」）。
//
// 返回 false 的四类：
// - nil：没有面板；
// - 本地菜单（权限模式 / 会话选择 / 安全套餐）：TUI 自己的选择器，没有「向模型补充说明」的语义；
// - 编排器提问（`orch-ask-*` / `orch-contract`）：其回传结构 `orchestrator.ClarifyAnswer` 只有
// `Value` 一个字段，note 到那里就丢了 ⇒ 不提示一个送不到的能力。要开放需先给 `ClarifyAnswer`
// 加 FreeText 字段并接线（见 报告「编排器路径丢 note」，独立拍板项）；
// - ToolID 为空：非生产装配点（测试/兜底构造），不作为可交互面板。
//
// ★ 改本函数时请一并核对 `choiceResultMsg` 的分派分支（app.go: StateWaitingChoice 的 case）——
// 两者是同一件事的两面：「能不能按 i 补 note」与「回传时 note 送不送得到」。
func isNoteCapableChoice(pc *PendingChoice) bool {
	if pc == nil || pc.ToolID == "" {
		return false
	}
	if strings.HasPrefix(pc.ToolID, "orch-ask-") || pc.ToolID == "orch-contract" {
		return false
	}
	switch pc.ToolID {
	case permissionModeChoiceToolID, sessionPickerChoiceToolID, securityPresetChoiceToolID,
		rewindPickerChoiceToolID:
		return false
	}
	return true
}
