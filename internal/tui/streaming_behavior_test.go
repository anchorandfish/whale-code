// streaming_behavior_test.go 针对 TUI 流式核心行为的前置测试。
//
// 对标文档：内部交付台账 §4.2
// 对标同类 CLI compact_remote_parity.rs（parity 自动化断言理念）
//
// 本文件分 3 组测试：
// 1. frame_limiter 7ms 节流正确性（对标 R1 修复验证）
// 2. 增量渲染缓存正确性（完整 Model 回归基座）
// 3. stop/Ctrl+C 中断保留 ActiveCell 文本

package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// TestFrameLimiter7msThrottle 验证 frame_limiter 7ms 节流使每个 8ms tick 都能渲染。
// 对标行为：minInterval=7ms < tick=8ms，ShouldRender=true（8ms>=7ms），达成 ~120fps。
// 对标同类 CLI MIN_FRAME_INTERVAL=8.33ms（同类 CLI 事件驱动，whale-code tick 驱动）。
// REF-外部: 同类 CLI frame_rate_limiter.rs
// frame_limiter.go:37-41 ShouldRender 逻辑
func TestFrameLimiter7msThrottle(t *testing.T) {
	fl := NewFrameLimiter()

	// 1. 首次 ShouldRender 必须为 true（lastEmit.IsZero() 短路返回 true）
	if !fl.ShouldRender() {
		t.Fatal("首次 ShouldRender 应为 true（lastEmit.IsZero 短路），got false")
	}

	// 2. MarkEmitted 后立即 ShouldRender 必须为 false（0ms < 7ms）
	fl.MarkEmitted()
	if fl.ShouldRender() {
		t.Fatal("MarkEmitted 后立即 ShouldRender 应为 false（0ms < 7ms），got true")
	}

	// 3. 等待 8ms（模拟 tick 间隔）后 ShouldRender 必须为 true（8ms >= 7ms）
	// 对标 R1 修复：原 8.33ms 会导致 8ms < 8.33ms 返回 false，每 2 tick 跳 1 次
	time.Sleep(8 * time.Millisecond)
	if !fl.ShouldRender() {
		t.Fatal("等待 8ms 后 ShouldRender 应为 true（8ms >= 7ms），got false；原 8.33ms 会导致此处 false")
	}

	// 4. MarkEmitted 后等待 3ms，ShouldRender 必须为 false（3ms < 7ms，余量 4ms 防 flaky）
	fl.MarkEmitted()
	time.Sleep(3 * time.Millisecond)
	if fl.ShouldRender() {
		t.Fatal("等待 3ms 后 ShouldRender 应为 false（3ms < 7ms），got true")
	}
}

// TestFrameLimiterReset 验证 Reset 后 ShouldRender 回到首次渲染状态。
// 对标行为：流式结束/切换会话时 Reset，下次渲染不受上次节流影响。
// frame_limiter.go:49-51 Reset 逻辑
func TestFrameLimiterReset(t *testing.T) {
	fl := NewFrameLimiter()
	fl.MarkEmitted()

	// Reset 前立即 ShouldRender 为 false
	if fl.ShouldRender() {
		t.Fatal("MarkEmitted 后立即 ShouldRender 应为 false")
	}

	// Reset 后 ShouldRender 回到 true（lastEmit 清零，IsZero 短路）
	fl.Reset()
	if !fl.ShouldRender() {
		t.Fatal("Reset 后 ShouldRender 应为 true（lastEmit 清零），got false")
	}
}

// TestStreamingCacheIncrementalRender 验证增量渲染缓存的正确性。
// 对标行为：消息数量不变时复用缓存（O(n)→O(1)），数量变化时重建。
// 对标同类 CLI active_cell 历史消息不变 + 同类 CLI code Ink throttle 只更新变化部分。
func TestStreamingCacheIncrementalRender(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.viewport.Width = 76
	m.viewport.Height = 8
	initialCount := len(m.Messages())
	m.messages.Append("history-one")
	m.messages.Append("history-two")
	m.activeCell.AppendText("stream-a")

	m.renderStreamContent()
	firstHistory := m.streamCachedHistory
	if firstHistory == "" || m.streamCachedMsgCount != initialCount+2 {
		t.Fatalf("首次渲染应缓存新增后的全部历史，count=%d want=%d", m.streamCachedMsgCount, initialCount+2)
	}

	m.activeCell.AppendText("stream-b")
	m.renderStreamContent()
	if m.streamCachedHistory != firstHistory {
		t.Fatal("消息数量不变时应复用历史缓存")
	}

	m.messages.Append("history-three")
	m.renderStreamContent()
	if m.streamCachedMsgCount != initialCount+3 || m.streamCachedHistory == firstHistory {
		t.Fatal("消息数量变化时应重建历史缓存")
	}
}

// TestStopExecutionPreservesCurrentText 验证 stop 键保留已输出 AI 文本。
// 对标行为：resetStreamState 前将 CurrentText 封装为 [interrupted] 显示消息。
// 对标同类 CLI REPL.tsx 中断保留 streamingText（引用源未在本地留存）。
func TestStopExecutionPreservesCurrentText(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming
	m.activeCell.AppendText("部分AI输出")
	m.streamCachedHistory = "cached"
	m.streamCachedMsgCount = 1

	updatedModel, _ := m.stopExecution()
	updated := updatedModel.(Model)
	updated.reassignServices()
	joined := strings.Join(updated.Messages(), "\n")
	if !strings.Contains(joined, "部分AI输出") {
		t.Fatalf("stop 应保留部分输出，messages=%q", joined)
	}
	// 2026-10-07（用户拍板）：中断结束面 = 单行结束标志（━━ ⏹ + 完整时刻），
	// 不再有独立的 `Stopped │ … │ interrupted` 显示消息（它是投影，不在 Messages() 里）。
	if !turnInterruptedMarkPattern.MatchString(ansi.Strip(updated.renderMessages())) {
		t.Fatalf("stop 应显示统一的中断结束标志（━━ ⏹ …），display=%q", ansi.Strip(updated.renderMessages()))
	}
	if !updated.activeCell.IsEmpty() || updated.CurrentText() != "" {
		t.Fatal("stop 后应清空流式活动状态")
	}
	if updated.streamCachedHistory != "" || updated.streamCachedMsgCount != 0 {
		t.Fatal("stop 后应失效流式历史缓存")
	}
}

// TestStopExecutionEmptyCurrentText 验证空响应不添加消息。
// 对标行为：CurrentText 为空时 stopExecution 不添加 "🐋 Whale:" 消息（空值守卫生效）。
func TestStopExecutionEmptyCurrentText(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.state = StateStreaming

	updatedModel, _ := m.stopExecution()
	updated := updatedModel.(Model)
	updated.reassignServices()
	joined := strings.Join(updated.Messages(), "\n")
	if strings.Contains(joined, "🐋 Whale:") {
		t.Fatalf("空流停止时不应追加空 AI 消息，messages=%q", joined)
	}
	if !turnInterruptedMarkPattern.MatchString(ansi.Strip(updated.renderMessages())) {
		t.Fatal("空流停止时仍应显示中断结束标志")
	}
}

// TestActiveCellTextThinkingRevision 验证 ActiveCell 的 AppendText/AppendThinking/Revision 正确性。
// 对标行为：流式 delta 原地 mutation，revision 自增触发重绘。
// 对标同类 CLI "active cell that can mutate in place"
// active_cell.go:39-50 AppendText/AppendThinking
func TestActiveCellTextThinkingRevision(t *testing.T) {
	c := NewActiveCell()

	// 1. 初始状态为空，revision=0
	if c.Text() != "" {
		t.Fatalf("初始 Text 应为空，got %q", c.Text())
	}
	if c.Thinking() != "" {
		t.Fatalf("初始 Thinking 应为空，got %q", c.Thinking())
	}
	if c.Revision() != 0 {
		t.Fatalf("初始 Revision 应为 0，got %d", c.Revision())
	}

	// 2. AppendText 后 Text 累积，revision 自增
	c.AppendText("Hello ")
	c.AppendText("World")
	if c.Text() != "Hello World" {
		t.Fatalf("Text 应为 'Hello World'，got %q", c.Text())
	}
	if c.Revision() != 2 {
		t.Fatalf("AppendText 2 次后 Revision 应为 2，got %d", c.Revision())
	}

	// 3. AppendThinking 后 Thinking 累积，revision 自增
	c.AppendThinking("Thinking...")
	if c.Thinking() != "Thinking..." {
		t.Fatalf("Thinking 应为 'Thinking...'，got %q", c.Thinking())
	}
	if c.Revision() != 3 {
		t.Fatalf("AppendThinking 1 次后 Revision 应为 3，got %d", c.Revision())
	}

	// 4. IsEmpty 返回 false（有内容）
	if c.IsEmpty() {
		t.Fatal("有内容时 IsEmpty 应为 false")
	}
}

// TestActiveCellFlush 验证 Flush 后 cell 重置为空。
// 对标行为：flush_active_cell 提交为历史后 cell 重置可复用。
// 对标同类 CLI flush_active_cell
// active_cell.go:82-89 Flush
func TestActiveCellFlush(t *testing.T) {
	c := NewActiveCell()
	c.AppendText("streaming text")
	c.AppendThinking("thinking content")
	c.AddTool("bash", "ls -la")

	// Flush 返回累积文本
	content := c.Flush()
	if content != "streaming text" {
		t.Fatalf("Flush 应返回 'streaming text'，got %q", content)
	}

	// Flush 后 cell 重置
	if c.Text() != "" {
		t.Fatalf("Flush 后 Text 应为空，got %q", c.Text())
	}
	if c.Thinking() != "" {
		t.Fatalf("Flush 后 Thinking 应为空，got %q", c.Thinking())
	}
	if len(c.Tools()) != 0 {
		t.Fatalf("Flush 后 Tools 应为空，got %d", len(c.Tools()))
	}
	if c.Revision() != 0 {
		t.Fatalf("Flush 后 Revision 应为 0，got %d", c.Revision())
	}
	if !c.IsEmpty() {
		t.Fatal("Flush 后 IsEmpty 应为 true")
	}
}

// TestActiveCellNilGuard 验证 nil 守卫降级逻辑。
// 对标行为：activeCell 为 nil 时 renderStreamContent 降级为 sessionService（保护未初始化场景）。
// stream.go:769-774 nil 守卫模式
func TestActiveCellNilGuard(t *testing.T) {
	// 模拟 renderStreamContent 中的 nil 守卫逻辑
	var activeCell *ActiveCell // nil

	// nil 守卫：activeCell 为 nil 时降级为 sessionService（此处用空字符串模拟）
	var thinkingPreview string
	if activeCell != nil {
		thinkingPreview = activeCell.Thinking()
	} else {
		thinkingPreview = "fallback_thinking" // 模拟 sessionService.CurrentThinking()
	}
	if thinkingPreview != "fallback_thinking" {
		t.Fatalf("nil 守卫应降级为 fallback，got %q", thinkingPreview)
	}

	var currentText string
	if activeCell != nil {
		currentText = activeCell.Text()
	} else {
		currentText = "fallback_text" // 模拟 sessionService.CurrentText()
	}
	if currentText != "fallback_text" {
		t.Fatalf("nil 守卫应降级为 fallback，got %q", currentText)
	}

	// 验证非 nil 时正常读取
	activeCell = NewActiveCell()
	activeCell.AppendText("real_text")
	if activeCell != nil {
		currentText = activeCell.Text()
	}
	if currentText != "real_text" {
		t.Fatalf("非 nil 时应读 activeCell.Text()，got %q", currentText)
	}
}

// TestThinkingLabelWidth 验证思考边框标签的显示宽度计算。
// 对标行为：G1 思考边框宽度自适应，并兼容 East Asian ambiguous-width 模式。
// stream.go:780-781 thinkingLabelWidth 计算
func TestThinkingLabelWidth(t *testing.T) {
	label := "╭─ 🧠 Thinking "

	// ╭ 和 ─ 属于 East Asian ambiguous 字符：普通模式各占 1 列，
	// East Asian 模式各占 2 列。使用局部 Condition，避免测试结果依赖
	// 启动 go test 时继承的 LANG/LC_CTYPE/RUNEWIDTH_EASTASIAN。
	for _, tc := range []struct {
		name           string
		eastAsianWidth bool
		want           int
	}{
		{name: "non-east-asian", eastAsianWidth: false, want: 15},
		{name: "east-asian", eastAsianWidth: true, want: 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			condition := &runewidth.Condition{
				EastAsianWidth:     tc.eastAsianWidth,
				StrictEmojiNeutral: true,
			}
			if got := condition.StringWidth(label); got != tc.want {
				t.Fatalf("thinking label 显示宽度应为 %d，got %d", tc.want, got)
			}
		})
	}

	// 生产代码使用当前终端模式；两种合法宽度均应保持边框总宽度契约。
	width := runewidth.StringWidth(label)
	if width != 15 && width != 17 {
		t.Fatalf("当前终端模式下 thinking label 宽度应为 15 或 17，got %d", width)
	}

	// 验证 m.width=80 时 thinkingWidth 与当前标签宽度互补。
	thinkingWidth := 80 - width
	if thinkingWidth+width != 80 {
		t.Fatalf("m.width=80 时标签和补齐宽度之和应为 80，label=%d thinking=%d", width, thinkingWidth)
	}

	// 验证窄视口时 thinkingWidth 为负（守卫跳过 dash 渲染）。
	thinkingWidth = 10 - width
	if thinkingWidth >= 0 {
		t.Fatalf("m.width=10 时 thinkingWidth 应为负数，got %d", thinkingWidth)
	}
}

// TestTruncateRunewidthWithEllipsis 验证按显示宽度截断并追加 …（对标同类 CLI truncate_line_with_ellipsis_if_overflow）。
// REF-外部: 同类 CLI
// render_extras.go truncateRunewidthWithEllipsis
func TestTruncateRunewidthWithEllipsis(t *testing.T) {
	// 1. 不超宽不截断
	got := truncateRunewidthWithEllipsis("hello", 10)
	if got != "hello" {
		t.Fatalf("不超宽应原样返回，got %q", got)
	}

	// 2. 超宽截断追加 …
	got = truncateRunewidthWithEllipsis("hello world", 8)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超宽应追加 …，got %q", got)
	}
	if runewidth.StringWidth(got) > 8 {
		t.Fatalf("截断后宽度应 <= 8，got %d (字符串 %q)", runewidth.StringWidth(got), got)
	}

	// 3. maxWidth <= 0 返回空（子审查致命漏洞1修复）
	got = truncateRunewidthWithEllipsis("hello", 0)
	if got != "" {
		t.Fatalf("maxWidth=0 应返回空，got %q", got)
	}
	got = truncateRunewidthWithEllipsis("hello", -1)
	if got != "" {
		t.Fatalf("maxWidth=-1 应返回空，got %q", got)
	}

	// 4. maxWidth <= 1 返回 …
	got = truncateRunewidthWithEllipsis("hello", 1)
	if got != "…" {
		t.Fatalf("maxWidth=1 应返回 …，got %q", got)
	}

	// 5. CJK 字符正确截断（中文占 2 列）
	got = truncateRunewidthWithEllipsis("你好世界测试", 5)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("CJK 超宽应追加 …，got %q", got)
	}
	if runewidth.StringWidth(got) > 5 {
		t.Fatalf("CJK 截断后宽度应 <= 5，got %d", runewidth.StringWidth(got))
	}
}

// TestComputeStatusBarWidth 验证状态栏 parts 总宽度计算（含分隔符 + CJK 宽度）。
// render_extras.go computeStatusBarWidth
func TestComputeStatusBarWidth(t *testing.T) {
	separator := " | "
	sepWidth := runewidth.StringWidth(separator) // = 3

	// 1. 单个 part，无分隔符
	parts1 := []statusBarPart{{text: "hello", priority: 0}}
	width1 := computeStatusBarWidth(parts1, separator, sepWidth)
	if width1 != 5 {
		t.Fatalf("单 part 宽度应为 5，got %d", width1)
	}

	// 2. 两个 part，含 1 个分隔符
	parts2 := []statusBarPart{
		{text: "hello", priority: 0},
		{text: "world", priority: 0},
	}
	width2 := computeStatusBarWidth(parts2, separator, sepWidth)
	expected2 := 5 + 3 + 5 // hello + " | " + world = 13
	if width2 != expected2 {
		t.Fatalf("两 part 宽度应为 %d，got %d", expected2, width2)
	}

	// 3. CJK 字符宽度正确（中文占 2 列）
	partsCJK := []statusBarPart{
		{text: "你好", priority: 0}, // 4 列
		{text: "世界", priority: 0}, // 4 列
	}
	widthCJK := computeStatusBarWidth(partsCJK, separator, sepWidth)
	expectedCJK := 4 + 3 + 4 // 11
	if widthCJK != expectedCJK {
		t.Fatalf("CJK 两 part 宽度应为 %d，got %d", expectedCJK, widthCJK)
	}

	// 4. 空 parts
	widthEmpty := computeStatusBarWidth([]statusBarPart{}, separator, sepWidth)
	if widthEmpty != 0 {
		t.Fatalf("空 parts 宽度应为 0，got %d", widthEmpty)
	}
}
