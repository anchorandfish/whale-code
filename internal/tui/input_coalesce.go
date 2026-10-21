// input_coalesce.go — 输入合并器（REF-方案-TUI输入与工具调用渲染修复 §4.1）。
// 解决非 bracketed paste 终端（Windows cmd.exe/旧 conhost）把粘贴拆成大量 KeyRunes
// 导致"一个字一个字往外蹦"的问题：检测快速输入突发，缓冲 rune 批量刷新。
//
// 设计要点：
// - 终端无关：bracketed/非 bracketed 均生效（bracketed 单事件不触发突发模式）
// - 正常打字无延迟：单字符 + 间隔 > 窗口 → 不缓冲，直接 textarea.Update
// - 粘贴批量出现：多 rune 或间隔 < 窗口 → 缓冲，8ms tick 批量 InsertString
// - 突发期间跳过 typeahead debounce，减少 50ms timer goroutine 垃圾
//
// 对标同类 CLI 依赖终端 bracketed paste（无兜底）；本方案额外兜底非 bracketed 终端。
package tui

import (
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// coalesceWindow 突发检测窗口：两次 KeyRunes 间隔小于此值视为快速输入（粘贴）。
// 24ms 覆盖 Windows 终端在负载下分批投递粘贴事件的抖动；误判为突发时仅增加最多 8ms
// 的批量刷新延迟，不改变普通字符语义。
const coalesceWindow = 24 * time.Millisecond

// coalesceFlush 刷新延迟：缓冲后多少 ms 批量写入 textarea。
// 8ms ≈ 120fps 一帧，保证肉眼无延迟感；期间到达的 rune 一并刷新。
const coalesceFlush = 8 * time.Millisecond

// coalesceEnterMinRunes Enter 转换为粘贴换行前要求的最少连续字符数。
// 使用 3 个字符避免用户快速输入短消息（如 "ok" 后回车）被误判为多行粘贴。
const coalesceEnterMinRunes = 3

// coalesceEnterSingleEventRunes 单个 KeyRunes 事件达到该长度时，也可判定为粘贴而非 IME 提交。
const coalesceEnterSingleEventRunes = 16

// inputCoalesceState 输入合并器状态（Model 持有，bubbletea 主循环单线程访问，无竞态）。
type inputCoalesceState struct {
	buf          []rune    // 待刷新的 rune 缓冲
	active       bool      // 是否处于快速输入突发模式
	lastTime     time.Time // 上次 KeyRunes 到达时间（用于突发检测）
	flushPending bool      // 是否已有 flush tick 待触发（防重复调度）
	burstRunes   int       // 当前连续突发已观察到的 rune 数（含首次直接写入的字符）
	burstEvents  int       // 当前连续突发中的 KeyRunes 事件数（区分单次 IME 提交与真实粘贴流）
	flushSeq     uint64    // flush 世代号（同步刷新或新调度会使旧 tick 失效）
}

// coalesceFlushMsg 是带世代号的输入合并 flush 消息，旧世代到达时直接忽略。
type coalesceFlushMsg struct {
	seq uint64
}

// maybeCoalesceInput 判断当前 KeyRunes 是否应缓冲（快速输入突发）。
// 返回 true 表示已缓冲（调用方应跳过 textarea.Update，改为调度 flush tick）。
// 返回 false 表示正常打字（调用方直接 textarea.Update）。
// 检测条件（任一满足视为突发）：
// 1. 单事件多 rune（len>1）——非 bracketed paste 常见
// 2. 已处于突发模式（paste 进行中）
// 3. 与上次 KeyRunes 间隔 < coalesceWindow（快速连续）
func (m *Model) maybeCoalesceInput(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}
	now := time.Now()
	c := &m.inputCoalesce
	if c.lastTime.IsZero() || now.Sub(c.lastTime) > coalesceWindow {
		c.burstRunes = len(runes)
		c.burstEvents = 1
	} else {
		c.burstRunes += len(runes)
		c.burstEvents++
	}
	rapid := len(runes) > 1 || c.active || c.burstRunes >= 2
	c.lastTime = now
	if !rapid {
		return false
	}
	c.buf = append(c.buf, runes...)
	c.active = true
	return true
}

// coalesceFlushCmd 返回 flush tick 命令（8ms 后触发 coalesceFlushMsg）。
func (m *Model) coalesceFlushCmd() tea.Cmd {
	m.inputCoalesce.flushSeq++
	seq := m.inputCoalesce.flushSeq
	return tea.Tick(coalesceFlush, func(time.Time) tea.Msg {
		return coalesceFlushMsg{seq: seq}
	})
}

// handleCoalesceFlush 处理 flush tick：把缓冲的 rune 批量写入 textarea。
// 若 flush 期间有新 rune 到达（buf 非空），重新调度 flush；否则结束突发模式。
// 返回需要追加的 cmd（typeahead debounce 等）。
//
// 性能要点（对标同类 CLI textarea.insert_str / 同类 CLI onPaste(整串)）：
// - 用 InsertString 在 cursor 位置插入，O(插入长度)，不调 Reset，保留 wrap memoization 缓存
// - 旧实现 Value()+SetValue(current+buf) 是 O(n) 二次复杂度：
// Value() O(n) 遍历所有行 + SetValue 内部 Reset(清空 wrap 缓存) + 全量重建
// 导致"第一次快(n小)、后续慢(n大超 8ms 掉帧 + Reset 清缓存致 View 全量重算)"
// - "删除/重新打字慢"(wrap 缓存被清空后每次 View 都 O(n) 全量重算)
func (m *Model) handleCoalesceFlush(seq uint64) tea.Cmd {
	c := &m.inputCoalesce
	if seq != c.flushSeq {
		return nil
	}
	c.flushPending = false
	hadBuffer := len(c.buf) > 0
	flushedText := ""
	if hadBuffer {
		// 批量写入 textarea：InsertString 在 cursor 位置插入，不 Reset wrap 缓存（对标同类 CLI insert_str）
		flushedText = string(c.buf)
		m.uiService.Textarea().InsertString(flushedText)
		c.buf = nil
		// 调整 textarea 高度（粘贴可能增加行数）
		m.adjustInputHeightAfterCoalesce()
	}

	var cmds []tea.Cmd
	if hadBuffer && m.shouldDebounceTypeahead(flushedText) {
		// 突发期间每 flush 触发一次 typeahead debounce，对比每键一次大幅减少 timer 垃圾。
		cmds = append(cmds, m.debounceTypeaheadCmd())
	}
	if c.active && !c.lastTime.IsZero() && time.Since(c.lastTime) < coalesceIdleTimeout() {
		// Windows 非 bracketed paste 可能在数十毫秒内分批到达；保持活动窗口，
		// 但仍每 8ms 刷新已有字符，兼顾吞吐和视觉延迟。
		c.flushPending = true
		cmds = append(cmds, m.coalesceFlushCmd())
	} else {
		m.resetInputCoalesceState()
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// flushCoalescedInputSync 同步刷新缓冲到 textarea（非 tick，立即生效）。
// 用于用户在突发期间按下非 KeyRunes 键（如 Enter/Backspace/方向键）时，确保缓冲内容先写入再处理该键。
// 同样用 InsertString 避免 O(n) Value+SetValue（对标 handleCoalesceFlush）。
func (m *Model) flushCoalescedInputSync() {
	if !m.inputCoalesce.active || len(m.inputCoalesce.buf) == 0 {
		return
	}
	// InsertString 在 cursor 位置插入，不 Reset wrap 缓存（对标同类 CLI insert_str）
	m.uiService.Textarea().InsertString(string(m.inputCoalesce.buf))
	m.inputCoalesce.buf = nil
	m.inputCoalesce.active = false
	m.inputCoalesce.flushPending = false
	m.inputCoalesce.lastTime = time.Time{} // 重置 lastTime，避免删除后立即打字误判突发
	m.inputCoalesce.burstRunes = 0
	m.inputCoalesce.burstEvents = 0
	m.inputCoalesce.flushSeq++
	m.adjustInputHeightAfterCoalesce()
}

// shouldCoalesceEnter 判断 Enter 是否属于非 bracketed 多行粘贴。
// 至少需要 2 个快速 KeyRunes 事件；单事件只有达到 16 rune 才按粘贴处理，避免 IME 提交后回车误判。
func (m *Model) shouldCoalesceEnter() bool {
	c := &m.inputCoalesce
	pasteEvidence := c.burstEvents >= 2 || c.burstRunes >= coalesceEnterSingleEventRunes
	return c.active && pasteEvidence && c.burstRunes >= coalesceEnterMinRunes && !c.lastTime.IsZero() && time.Since(c.lastTime) < coalesceIdleTimeout()
}

// bufferCoalescedNewline 将粘贴流中的 Enter 转换为换行并继续批量刷新。
func (m *Model) bufferCoalescedNewline() tea.Cmd {
	c := &m.inputCoalesce
	c.buf = append(c.buf, '\n')
	c.active = true
	c.burstRunes++
	c.lastTime = time.Now()
	if c.flushPending {
		return nil
	}
	c.flushPending = true
	return m.coalesceFlushCmd()
}

// coalesceIdleTimeout 返回输入突发活动窗口。
// Windows 采用 60ms 兼容较慢的控制台粘贴投递，其他平台采用 16ms 减少误判窗口。
func coalesceIdleTimeout() time.Duration {
	if runtime.GOOS == "windows" {
		return 60 * time.Millisecond
	}
	return 16 * time.Millisecond
}

// resetInputCoalesceState 清空一次输入突发的分类状态。
func (m *Model) resetInputCoalesceState() {
	m.inputCoalesce.active = false
	m.inputCoalesce.lastTime = time.Time{}
	m.inputCoalesce.flushPending = false
	m.inputCoalesce.burstRunes = 0
	m.inputCoalesce.burstEvents = 0
	m.inputCoalesce.flushSeq++
}

// insertComposerText 在光标处批量插入文本，并以单次 O(n) 计数保留 50000 rune 输入上限。
// 返回 true 表示输入因上限被截断；该检查只在显式粘贴时执行，不进入每键热路径。
func (m *Model) insertComposerText(text string) bool {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	// ★ 同一不变量的第二个入口：显式粘贴进来的内容同样只能是文本。
	// 控制字符（ESC/C0/C1）不得进输入框——它们只可能来自被复制的终端渲染产物，
	// 是「渲染产物当数据」的老毛病（同族）。\n/\t 保留。
	text = string(stripControlRunes([]rune(text)))
	if text == "" {
		return false
	}
	currentRunes := utf8.RuneCountInString(m.uiService.Textarea().Value())
	remaining := maxInputRunes - currentRunes
	if remaining <= 0 {
		return true
	}
	runes := []rune(text)
	truncated := len(runes) > remaining
	if truncated {
		runes = runes[:remaining]
	}
	m.uiService.Textarea().InsertString(string(runes))
	m.adjustInputHeightAfterCoalesce()
	return truncated
}

// canAcceptComposerText 判断当前状态是否允许向输入编辑器追加文本。
// 确认、选择和文档导航状态保留键盘给专用 UI，避免异步粘贴结果污染隐藏输入框。
func (m *Model) canAcceptComposerText() bool {
	if !m.uiService.Textarea().Focused() || m.uiService.InputFocusedOnSend() || m.uiService.InputFocusedOnStop() {
		return false
	}
	switch m.sessionService.State() {
	case StateIdle, StateStreaming, StateToolExec:
		return true
	default:
		return false
	}
}

// showInputLimitWarning 显示输入长度上限提示，并保留用户当前的对话滚动位置。
func (m *Model) showInputLimitWarning() {
	viewport := m.uiService.Viewport()
	followTail := viewport.AtBottom()
	offset := viewport.YOffset
	m.addMessage(errorStyle.Render(fmt.Sprintf("⚠️ Input is limited to %d characters; shorten the message before sending.", maxInputRunes)))
	if !followTail {
		viewport.SetYOffset(offset)
	}
}

// desiredInputHeight counts the current logical lines plus the soft-wrapped
// rows of the line containing the cursor. LineInfo uses textarea's own wrap
// calculation, so this stays aligned with what is actually rendered without
// scanning the entire (potentially 50k-rune) input on every keystroke.
func (m *Model) desiredInputHeight() int {
	input := m.uiService.Textarea()
	visualLines := input.LineCount()
	if lineInfo := input.LineInfo(); lineInfo.Height > 1 {
		visualLines += lineInfo.Height - 1
	}
	newHeight := visualLines + 1
	if newHeight < 3 {
		newHeight = 3
	}
	if newHeight > maxInputHeight {
		newHeight = maxInputHeight
	}
	return newHeight
}

// adjustInputHeightAfterCoalesce 合并器刷新后调整 textarea 高度（复用既有逻辑）。
func (m *Model) adjustInputHeightAfterCoalesce() {
	newHeight := m.desiredInputHeight()
	if m.uiService.Textarea().Height() != newHeight {
		m.uiService.Textarea().SetHeight(newHeight)
		m.recalcViewportHeight()
	}
}
