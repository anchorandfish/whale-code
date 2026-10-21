package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/anchorandfish/whale-code/internal/typeahead"
)

// typeaheadDebounceDuration 50ms debounce（对标 useTypeahead.tsx:511）
const typeaheadDebounceDuration = 50 * time.Millisecond

// typeaheadTickMsg 是带世代号的补全 debounce 消息，旧世代到达时直接忽略。
type typeaheadTickMsg struct {
	seq uint64
}

// debounceTypeaheadCmd 递增补全世代号，并返回 50ms 后的最新候选检测命令。
func (m *Model) debounceTypeaheadCmd() tea.Cmd {
	m.typeaheadSeq++
	seq := m.typeaheadSeq
	return tea.Tick(typeaheadDebounceDuration, func(time.Time) tea.Msg {
		return typeaheadTickMsg{seq: seq}
	})
}

// shouldDebounceTypeahead 判断本次文本变化是否可能影响 @ 文件、# skill 或 slash command 补全。
// 普通输入不再创建 timer；触发符首次出现或已有补全上下文时才调度。
func (m *Model) shouldDebounceTypeahead(delta string) bool {
	if m.suggestionState == nil || m.sessionService.State() != StateIdle {
		return false
	}
	if m.suggestionState.Visible || m.suggestionState.CurrentTrigger != typeahead.TriggerNone {
		return true
	}
	return strings.ContainsAny(delta, "@#/")
}

// applyTypeaheadCompletion 将选中的补全项应用到 textarea 输入（对标 useTypeahead.tsx:237）
// 从 suggestionState 获取 token 位置信息，调用 typeahead.ApplySuggestion 替换输入
func (m *Model) applyTypeaheadCompletion(suggestion typeahead.Completion) {
	if m.suggestionState == nil || !m.suggestionState.Visible {
		return
	}

	input := m.uiService.Textarea().Value()
	tokenLen := m.suggestionState.TokenLen
	if suggestion.Trigger == typeahead.TriggerSlash {
		if expandedLen := slashCommandTokenLength(input, m.suggestionState.TokenStart, suggestion.DisplayText); expandedLen >= 0 {
			tokenLen = expandedLen
		}
	}
	newInput, newCursor := typeahead.ApplySuggestion(
		input,
		suggestion,
		m.suggestionState.TokenStart,
		tokenLen,
	)

	setTextareaCursorByteOffset(m.uiService.Textarea(), newInput, newCursor)

	// 清空弹窗
	m.suggestionState.Clear()
	m.recalcViewportHeight()
}

// typeaheadSelectionAlreadyTyped 判断当前输入是否已与选中补全项完全一致。
// 一致时再应用补全只会追加一个分隔空格（表现为“第一次回车变成空格”），
// 应当直接关闭弹窗并放行 Enter 走正常发送路径。
func (m *Model) typeaheadSelectionAlreadyTyped(suggestion typeahead.Completion) bool {
	if m.suggestionState == nil || !m.suggestionState.Visible {
		return false
	}
	input := m.uiService.Textarea().Value()
	tokenStart := m.suggestionState.TokenStart
	if tokenStart < 0 || tokenStart >= len(input) {
		return false
	}
	tokenLen := m.suggestionState.TokenLen
	if suggestion.Trigger == typeahead.TriggerSlash {
		if expandedLen := slashCommandTokenLength(input, tokenStart, suggestion.DisplayText); expandedLen >= 0 {
			tokenLen = expandedLen
		}
	}
	replaceStart := tokenStart + 1
	if replaceStart > len(input) {
		return false
	}
	replaceEnd := replaceStart + tokenLen
	if replaceEnd > len(input) {
		replaceEnd = len(input)
	}
	currentToken := input[replaceStart:replaceEnd]
	if !strings.EqualFold(strings.TrimSpace(currentToken), strings.TrimSpace(suggestion.DisplayText)) {
		return false
	}
	// token 之后仅剩空白时，应用补全只会追加分隔符，直接发送即可
	return strings.TrimSpace(input[replaceEnd:]) == ""
}

// applySlashPrefix extends an ambiguous command prefix without treating it as
// an executable command. The popup stays open so the user can continue typing
// or choose a concrete subcommand.
func (m *Model) applySlashPrefix(prefix string) {
	if m.suggestionState == nil || !m.suggestionState.Visible || prefix == "" {
		return
	}
	input := m.uiService.Textarea().Value()
	tokenStart := m.suggestionState.TokenStart
	if tokenStart < 0 || tokenStart >= len(input) {
		return
	}
	tokenLen := m.suggestionState.TokenLen
	if expandedLen := slashCommandTokenLength(input, tokenStart, prefix); expandedLen >= 0 {
		tokenLen = expandedLen
	}
	replaceStart := tokenStart + 1
	replaceEnd := replaceStart + tokenLen
	if replaceEnd > len(input) {
		replaceEnd = len(input)
	}
	after := input[replaceEnd:]
	after = strings.TrimLeftFunc(after, isHorizontalWhitespace)
	trimmedPrefix := strings.TrimRightFunc(prefix, unicode.IsSpace)
	separator := ""
	if len(trimmedPrefix) != len(prefix) && !startsLineBreak(after) {
		separator = " "
	}
	newInput := input[:replaceStart] + trimmedPrefix + separator + after
	setTextareaCursorByteOffset(m.uiService.Textarea(), newInput, len(input[:replaceStart])+len(trimmedPrefix)+len(separator))
	m.updateTypeaheadSuggestions()
}

// slashCommandTokenLength returns the full command portion at the slash, including
// partially typed right-hand tokens when the cursor is in the middle of a command.
func slashCommandTokenLength(input string, tokenStart int, command string) int {
	if tokenStart < 0 || tokenStart >= len(input) || command == "" {
		return -1
	}
	offset := tokenStart + 1
	for range strings.Fields(command) {
		for offset < len(input) {
			runeValue, size := utf8.DecodeRuneInString(input[offset:])
			if !isHorizontalWhitespace(runeValue) {
				break
			}
			offset += size
		}
		if offset >= len(input) {
			break
		}
		runeValue, _ := utf8.DecodeRuneInString(input[offset:])
		if unicode.IsSpace(runeValue) {
			break
		}
		for offset < len(input) {
			runeValue, size := utf8.DecodeRuneInString(input[offset:])
			if unicode.IsSpace(runeValue) {
				if !isHorizontalWhitespace(runeValue) {
					return offset - tokenStart - 1
				}
				break
			}
			offset += size
		}
	}
	return offset - tokenStart - 1
}

func isHorizontalWhitespace(r rune) bool {
	if !unicode.IsSpace(r) {
		return false
	}
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return false
	default:
		return true
	}
}

func startsLineBreak(input string) bool {
	if input == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(input)
	switch r {
	case '\n', '\r', '\u0085', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}

// slashCommandItems 把命令注册表转换为 typeahead 候选集。
// 弹窗候选（updateTypeaheadSuggestions）与回车展开语义（app.go Enter 分支的
// HasSubcommands 判定）共用同一份候选集构建，stub/hidden 过滤在此处统一生效，避免双实现漂移。
func (m *Model) slashCommandItems() []typeahead.CommandItem {
	registry := m.toolService.CmdRegistry()
	if registry == nil {
		return nil
	}
	commands := registry.All()
	items := make([]typeahead.CommandItem, 0, len(commands))
	for _, cmd := range commands {
		if cmd.Stub || cmd.Hidden { // V0.1-HIDDEN: 弹窗不提示桩命令与隐藏命令
			continue
		}
		primary := cmd.DescriptionEN
		secondary := cmd.Description
		if primary == "" {
			primary = secondary
			secondary = ""
		}
		items = append(items, typeahead.CommandItem{
			Name:                 cmd.Name,
			Description:          primary,
			SecondaryDescription: secondary,
			Priority:             cmd.SuggestionPriority,
		})
	}
	return items
}

// updateTypeaheadSuggestions 检测当前输入中的 @ / # / slash token 并更新补全候选（对标 useTypeahead.tsx:41）
// 在 textarea.Update 后调用，负责 ExtractTokenWithTrigger + SuggestWithTrigger 两步
// P0-08 扩展：支持 @ 文件、# skill 和 slash command 触发符
func (m *Model) updateTypeaheadSuggestions() {
	if m.suggestionState == nil {
		return
	}
	// Popup rows are part of the input area. Recalculate immediately whenever
	// the debounced state changes so the transcript cannot sit underneath it.
	defer m.recalcViewportHeight()

	// 仅在 idle 状态触发补全
	if m.sessionService.State() != StateIdle {
		m.suggestionState.Clear()
		return
	}

	input := m.uiService.Textarea().Value()
	if input == "" {
		m.suggestionState.Clear()
		return
	}

	// textarea 未直接导出全局光标偏移，按逻辑行和当前列换算为 UTF-8 byte offset。
	cursorOffset := textareaCursorByteOffset(m.uiService.Textarea())

	// 提取 cursor 处的触发符 token（@ 文件、# skill 或 / command）
	token, startPos, trigger := typeahead.ExtractTokenWithTrigger(input, cursorOffset)
	if trigger == typeahead.TriggerNone {
		m.suggestionState.Clear()
		return
	}

	selectionContextChanged := m.suggestionState.TokenStart != startPos ||
		m.suggestionState.TokenLen != len(token) || m.suggestionState.CurrentTrigger != trigger
	// 保存 token 位置信息
	m.suggestionState.TokenStart = startPos
	m.suggestionState.TokenLen = len(token)
	m.suggestionState.CurrentTrigger = trigger
	if selectionContextChanged {
		m.suggestionState.Selected = 0
	}

	var completions []typeahead.Completion
	if trigger == typeahead.TriggerSlash {
		completions = typeahead.SuggestCommands(token, m.slashCommandItems())
	} else {
		completions = typeahead.SuggestWithTrigger(trigger, token)
	}
	m.suggestionState.UpdateCompletions(completions)
}

// N5 ：补全弹窗最多可见行数（超出滚动）
const maxVisibleSuggestions = 7

// popupBorderStyle 弹窗边框样式（对标 lipgloss 自渲染）
var popupBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("#494D64")).
	Padding(0, 1)

// popupSelectedStyle 选中项反色样式（对标 useTypeahead.tsx 选中态）
var popupSelectedStyle = lipgloss.NewStyle().
	Bold(true).
	Background(lipgloss.Color("#00ADD8")).
	Foreground(lipgloss.Color("#1A1B26"))

// popupNormalStyle 未选中项样式
var popupNormalStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#CAD3F5"))

// popupDirStyle 目录标记样式
var popupDirStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#8BE9FD"))

// popupDescStyle skill 描述副标题样式（P0-08 新增）
var popupDescStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#6E738D"))

// popupSkillTagStyle skill 触发符标记样式（P0-08 新增，区分 @ 文件与 # skill）
var popupSkillTagStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#50FA7B"))

// popupCommandTagStyle slash command 触发符样式。
var popupCommandTagStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#8BE9FD"))

// SuggestionState 补全弹窗状态（N5 typeahead 集成）
// P0-08 扩展：CurrentTrigger 标识当前弹窗来源（@ 文件 / # skill / slash command）
type SuggestionState struct {
	Completions    []typeahead.Completion // 候选列表
	Selected       int                    // 当前选中索引
	TokenStart     int                    // 触发符（@、# 或 /）在输入中的位置
	TokenLen       int                    // 被补全 token 的长度
	Visible        bool                   // 弹窗是否可见
	CurrentTrigger typeahead.Trigger      // 当前弹窗的触发符类型（P0-08 新增）
}

// NewSuggestionState 创建空的补全状态
func NewSuggestionState() *SuggestionState {
	return &SuggestionState{
		Completions:    nil,
		Selected:       0,
		TokenStart:     -1,
		TokenLen:       0,
		Visible:        false,
		CurrentTrigger: typeahead.TriggerNone,
	}
}

// UpdateCompletions 更新候选列表并保持选中项在有效范围
func (s *SuggestionState) UpdateCompletions(completions []typeahead.Completion) {
	s.Completions = completions
	if len(completions) == 0 {
		s.Visible = false
		s.Selected = 0
		return
	}
	s.Visible = true
	if s.Selected >= len(completions) {
		s.Selected = 0
	}
}

// MoveUp 选中项上移（对标 useTypeahead.tsx ↑ 处理）
func (s *SuggestionState) MoveUp() {
	if !s.Visible || len(s.Completions) == 0 {
		return
	}
	s.Selected--
	if s.Selected < 0 {
		s.Selected = len(s.Completions) - 1
	}
}

// MoveDown 选中项下移（对标 useTypeahead.tsx ↓ 处理）
func (s *SuggestionState) MoveDown() {
	if !s.Visible || len(s.Completions) == 0 {
		return
	}
	s.Selected++
	if s.Selected >= len(s.Completions) {
		s.Selected = 0
	}
}

// CurrentSelection 返回当前选中的候选项，无候选返回 nil
func (s *SuggestionState) CurrentSelection() *typeahead.Completion {
	if !s.Visible || len(s.Completions) == 0 || s.Selected < 0 || s.Selected >= len(s.Completions) {
		return nil
	}
	return &s.Completions[s.Selected]
}

// Clear 清空补全状态
func (s *SuggestionState) Clear() {
	s.Completions = nil
	s.Selected = 0
	s.TokenStart = -1
	s.TokenLen = 0
	s.Visible = false
	s.CurrentTrigger = typeahead.TriggerNone
}

// scrollOffset 计算滚动偏移，确保选中项始终可见
func (s *SuggestionState) scrollOffset() int {
	if len(s.Completions) <= maxVisibleSuggestions {
		return 0
	}
	// 选中项在可见窗口中间
	half := maxVisibleSuggestions / 2
	if s.Selected <= half {
		return 0
	}
	if s.Selected >= len(s.Completions)-half {
		return len(s.Completions) - maxVisibleSuggestions
	}
	return s.Selected - half
}

// renderCompletionPopup 渲染补全弹窗（对标 lipgloss 自渲染）
// width: 弹窗宽度（通常与 textarea 宽度一致）
// P0-08 扩展：根据 CurrentTrigger 区分文件、skill 与 slash command 渲染
func (s *SuggestionState) renderCompletionPopup(width int) string {
	if !s.Visible || len(s.Completions) == 0 {
		return ""
	}

	offset := s.scrollOffset()
	end := offset + maxVisibleSuggestions
	if end > len(s.Completions) {
		end = len(s.Completions)
	}

	isSkill := s.CurrentTrigger == typeahead.TriggerHash
	isCommand := s.CurrentTrigger == typeahead.TriggerSlash
	popupWidth, ok := completionPopupContentWidth(width)
	if !ok {
		return ""
	}

	var lines []string
	for i := offset; i < end; i++ {
		c := s.Completions[i]
		display := c.DisplayText
		if isCommand {
			marker := "  "
			if i == s.Selected {
				marker = popupSelectedStyle.Render("❯ ")
			}

			commandBudget := popupWidth - 2
			commandText := "/" + display
			if c.Description != "" {
				maxCommandWidth := popupWidth / 3
				if maxCommandWidth < 8 {
					maxCommandWidth = 8
				}
				if commandBudget > maxCommandWidth {
					commandBudget = maxCommandWidth
				}
			}
			commandText = truncateRunewidthWithEllipsis(commandText, commandBudget)
			line := marker + popupCommandTagStyle.Render(commandText)

			detailsBudget := popupWidth - 2 - lipgloss.Width(commandText) - 2
			if c.Description != "" && detailsBudget > 0 {
				if c.SecondaryDescription != "" && detailsBudget >= 12 {
					textBudget := detailsBudget - 3 // " / " separator
					secondaryBudget := textBudget / 3
					if secondaryBudget < 6 {
						secondaryBudget = 6
					}
					primaryBudget := textBudget - secondaryBudget
					primary := truncateRunewidthWithEllipsis(c.Description, primaryBudget)
					secondary := truncateRunewidthWithEllipsis(c.SecondaryDescription, secondaryBudget)
					line += "  " + popupDescStyle.Render(primary+" / "+secondary)
				} else {
					details := truncateRunewidthWithEllipsis(c.Description, detailsBudget)
					line += "  " + popupDescStyle.Render(details)
				}
			}
			lines = append(lines, line)
			continue
		}

		if isSkill {
			// skill 补全：显示 "name — description"（对标同类 CLI skill 弹窗）
			tag := popupSkillTagStyle.Render("#")
			nameBudget := popupWidth - 4 // 选中前缀 2 列 + "# " 2 列
			if c.Description != "" {
				nameBudget = (popupWidth - 7) / 2 // 另预留 " — " 3 列和至少一半描述空间
			}
			if nameBudget < 1 {
				nameBudget = 1
			}
			name := truncateRunewidthWithEllipsis(display, nameBudget)
			if c.Description != "" {
				// 描述按终端显示宽度截断，避免中文被按 UTF-8 字节切坏或撑爆窄终端。
				descBudget := popupWidth - 7 - lipgloss.Width(name)
				if descBudget > 0 {
					desc := truncateRunewidthWithEllipsis(c.Description, descBudget)
					line := fmt.Sprintf("%s %s — %s", tag, name, popupDescStyle.Render(desc))
					if i == s.Selected {
						lines = append(lines, popupSelectedStyle.Render("❯ ")+line)
					} else {
						lines = append(lines, "  "+line)
					}
					continue
				}
			}
			name = truncateRunewidthWithEllipsis(display, popupWidth-4)
			line := fmt.Sprintf("%s %s", tag, name)
			if i == s.Selected {
				lines = append(lines, popupSelectedStyle.Render("❯ ")+line)
			} else {
				lines = append(lines, "  "+line)
			}
			continue
		}

		// 文件补全：保持原渲染逻辑
		// 目录加 / 后缀（对标 useTypeahead.tsx:237）
		if c.IsDir {
			display += "/"
		}
		display = truncateRunewidthWithEllipsis(display, popupWidth-2)

		// 选中项反色，未选中项正常
		if i == s.Selected {
			lines = append(lines, popupSelectedStyle.Render("❯ "+display))
		} else {
			lines = append(lines, popupNormalStyle.Render("  "+display))
		}
	}

	// 滚动提示
	if len(s.Completions) > maxVisibleSuggestions {
		indicator := fmt.Sprintf(" (%d/%d)", s.Selected+1, len(s.Completions))
		lines = append(lines, popupDirStyle.Render(truncateRunewidthWithEllipsis(indicator, popupWidth)))
	}

	content := strings.Join(lines, "\n")

	return popupBorderStyle.Width(popupWidth).Render(content)
}

// completionPopupContentWidth 返回补全弹窗内容宽度；过窄时返回 false 以避免边框和前缀撑爆终端。
func completionPopupContentWidth(width int) (int, bool) {
	popupWidth := width - popupBorderStyle.GetHorizontalFrameSize()
	if popupWidth < 5 {
		return 0, false
	}
	return popupWidth, true
}

// textareaCursorByteOffset 把 textarea 的逻辑行/列光标位置换算为完整输入的 UTF-8 byte offset。
func textareaCursorByteOffset(input *textarea.Model) int {
	value := input.Value()
	lines := strings.Split(value, "\n")
	row := input.Line()
	if row < 0 {
		row = 0
	}
	if row >= len(lines) {
		return len(value)
	}
	lineInfo := input.LineInfo()
	column := lineInfo.StartColumn + lineInfo.ColumnOffset
	lineRunes := []rune(lines[row])
	if column < 0 {
		column = 0
	}
	if column > len(lineRunes) {
		column = len(lineRunes)
	}
	offset := 0
	for index := 0; index < row; index++ {
		offset += len(lines[index]) + 1
	}
	return offset + len(string(lineRunes[:column]))
}

// setTextareaCursorByteOffset 设置 textarea 内容，并把光标恢复到指定 UTF-8 byte offset。
func setTextareaCursorByteOffset(input *textarea.Model, value string, byteOffset int) {
	if byteOffset < 0 {
		byteOffset = 0
	}
	if byteOffset > len(value) {
		byteOffset = len(value)
	}
	for byteOffset > 0 && !utf8.ValidString(value[:byteOffset]) {
		byteOffset--
	}
	prefix := value[:byteOffset]
	targetRow := strings.Count(prefix, "\n")
	lastNewline := strings.LastIndex(prefix, "\n")
	targetColumn := utf8.RuneCountInString(prefix[lastNewline+1:])

	input.SetValue(value)
	for input.Line() > targetRow {
		input.CursorStart()
		input.CursorUp()
	}
	input.SetCursor(targetColumn)
}
