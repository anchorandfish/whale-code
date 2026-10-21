// render_pending_preview_test.go — P0-8/9 排队输入预览渲染测试。
// 覆盖 RenderPendingInputPreview 的空队列 / 单条 / 多条 / 超长截断 / 窄终端 /
// Bridge 模式 / 三段渲染等核心路径。
// 由于 RenderPendingInputPreview 是纯函数，直接断言字符串包含关系即可。
package tui

import (
	"strings"
	"testing"
)

// TestRender_EmptyQueued 验证空队列返回空字符串（不渲染）。
func TestRender_EmptyQueued(t *testing.T) {
	got := RenderPendingInputPreview(nil, nil, nil, 80)
	if got != "" {
		t.Errorf("RenderPendingInputPreview(empty) = %q, want empty", got)
	}
}

// TestRender_SingleMessage 验证单条消息渲染含标题 + "↳" 前缀 + Alt+Up hint。
func TestRender_SingleMessage(t *testing.T) {
	queued := []QueuedUserMessage{{Text: "hello world", Action: QueuedInputPlain}}
	got := RenderPendingInputPreview(queued, nil, nil, 80)

	if got == "" {
		t.Fatalf("render should not be empty when queued has messages")
	}
	if !strings.Contains(got, "Queued follow-up inputs") {
		t.Errorf("render should contain section header 'Queued follow-up inputs', got: %s", got)
	}
	if !strings.Contains(got, "↳") {
		t.Errorf("render should contain '↳' prefix for queued message, got: %s", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("render should contain message text, got: %s", got)
	}
	if !strings.Contains(got, "Alt+Up") {
		t.Errorf("render should contain Alt+Up hint, got: %s", got)
	}
}

// TestRender_MultipleMessages 验证多条消息每条独立 "↳" 前缀。
func TestRender_MultipleMessages(t *testing.T) {
	queued := []QueuedUserMessage{
		{Text: "msg1", Action: QueuedInputPlain},
		{Text: "msg2", Action: QueuedInputPlain},
		{Text: "msg3", Action: QueuedInputPlain},
	}
	got := RenderPendingInputPreview(queued, nil, nil, 80)

	count := strings.Count(got, "↳")
	if count != 3 {
		t.Errorf("render should contain 3 '↳' prefixes, got %d (rendered: %s)", count, got)
	}
	if !strings.Contains(got, "msg1") || !strings.Contains(got, "msg2") || !strings.Contains(got, "msg3") {
		t.Errorf("render should contain all messages, got: %s", got)
	}
}

// TestRender_TruncateLongMessage 验证超长消息（>3 行 wrap）截断显示 "…"。
func TestRender_TruncateLongMessage(t *testing.T) {
	// 构造超过 3 行的文本（宽度 30 时，每行 26 字符，3 行仅容纳 78 字符）
	longText := strings.Repeat("a", 200)
	queued := []QueuedUserMessage{{Text: longText, Action: QueuedInputPlain}}
	got := RenderPendingInputPreview(queued, nil, nil, 30)

	if !strings.Contains(got, "…") {
		t.Errorf("render should contain '…' for truncated long message, got: %s", got)
	}
}

// TestRender_NarrowWidth 验证 width < 20 时降级为 "N messages queued…"。
func TestRender_NarrowWidth(t *testing.T) {
	queued := []QueuedUserMessage{
		{Text: "msg1", Action: QueuedInputPlain},
		{Text: "msg2", Action: QueuedInputPlain},
		{Text: "msg3", Action: QueuedInputPlain},
	}
	got := RenderPendingInputPreview(queued, nil, nil, 15)

	if !strings.Contains(got, "3 messages queued") {
		t.Errorf("narrow width render should contain '3 messages queued', got: %s", got)
	}
	// 窄终端不显示消息内容
	if strings.Contains(got, "msg1") {
		t.Errorf("narrow width render should not contain message content, got: %s", got)
	}
	// 窄终端不显示 Alt+Up hint
	if strings.Contains(got, "Alt+Up") {
		t.Errorf("narrow width render should not contain Alt+Up hint, got: %s", got)
	}
}

// TestRender_ThreeSections 验证 pendingSteers + rejectedSteers + queued 同时存在时三段顺序正确。
func TestRender_ThreeSections(t *testing.T) {
	queued := []QueuedUserMessage{{Text: "queued_msg", Action: QueuedInputPlain}}
	pendingSteers := []string{"pending_steer"}
	rejectedSteers := []string{"rejected_steer"}
	got := RenderPendingInputPreview(queued, pendingSteers, rejectedSteers, 80)

	// 三段标题均应出现
	if !strings.Contains(got, "Messages to be submitted after next tool call") {
		t.Errorf("render should contain pending steers header, got: %s", got)
	}
	if !strings.Contains(got, "Messages to be submitted at end of turn") {
		t.Errorf("render should contain rejected steers header, got: %s", got)
	}
	if !strings.Contains(got, "Queued follow-up inputs") {
		t.Errorf("render should contain queued messages header, got: %s", got)
	}
	// 三段内容均应出现
	if !strings.Contains(got, "pending_steer") || !strings.Contains(got, "rejected_steer") || !strings.Contains(got, "queued_msg") {
		t.Errorf("render should contain all three section contents, got: %s", got)
	}
	// 三段顺序：pending → rejected → queued
	pendingIdx := strings.Index(got, "Messages to be submitted after next tool call")
	rejectedIdx := strings.Index(got, "Messages to be submitted at end of turn")
	queuedIdx := strings.Index(got, "Queued follow-up inputs")
	if !(pendingIdx < rejectedIdx && rejectedIdx < queuedIdx) {
		t.Errorf("section order wrong: pending(%d) rejected(%d) queued(%d)", pendingIdx, rejectedIdx, queuedIdx)
	}
}

// TestRender_OnlyPendingSteers 验证仅 pendingSteers 非空时仍能渲染（不显示 Alt+Up hint）。
func TestRender_OnlyPendingSteers(t *testing.T) {
	pendingSteers := []string{"pending_only"}
	got := RenderPendingInputPreview(nil, pendingSteers, nil, 80)

	if !strings.Contains(got, "Messages to be submitted after next tool call") {
		t.Errorf("render should contain pending steers header, got: %s", got)
	}
	if !strings.Contains(got, "pending_only") {
		t.Errorf("render should contain pending steer content, got: %s", got)
	}
	// 仅 pending steers 时不显示 Alt+Up hint（hint 仅在 queued messages 非空时显示）
	if strings.Contains(got, "Alt+Up") {
		t.Errorf("render should not contain Alt+Up hint when only pending steers, got: %s", got)
	}
}

// TestRender_CJKMessage 验证 CJK 宽字符消息能正确渲染（不崩溃）。
func TestRender_CJKMessage(t *testing.T) {
	queued := []QueuedUserMessage{{Text: "你好，世界！这是一条中文排队消息。", Action: QueuedInputPlain}}
	got := RenderPendingInputPreview(queued, nil, nil, 40)

	if !strings.Contains(got, "你好") {
		t.Errorf("render should contain CJK text, got: %s", got)
	}
	if !strings.Contains(got, "↳") {
		t.Errorf("render should contain '↳' prefix for CJK message, got: %s", got)
	}
}

// TestRender_NegativeWidth 验证 width <= 0 时不崩溃（降级为不 wrap）。
func TestRender_NegativeWidth(t *testing.T) {
	queued := []QueuedUserMessage{{Text: "edge case", Action: QueuedInputPlain}}
	// width=0 触发窄终端降级（width < 20）
	got := RenderPendingInputPreview(queued, nil, nil, 0)
	if !strings.Contains(got, "1 messages queued") {
		t.Errorf("width=0 should trigger narrow preview, got: %s", got)
	}
}

// TestWrapTextSimple_Basic 验证 wrapTextSimple 基本换行逻辑。
func TestWrapTextSimple_Basic(t *testing.T) {
	got := wrapTextSimple("abcdefgh", 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("wrap should produce 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "abc" || lines[1] != "def" || lines[2] != "gh" {
		t.Errorf("wrap result wrong: %v", lines)
	}
}

// TestWrapTextSimple_Multiline 验证 wrapTextSimple 处理多行输入。
func TestWrapTextSimple_Multiline(t *testing.T) {
	got := wrapTextSimple("ab\ncdefgh", 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("wrap should produce 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "ab" || lines[1] != "cde" || lines[2] != "fgh" {
		t.Errorf("wrap result wrong: %v", lines)
	}
}

// TestWrapTextSimple_ZeroWidth 验证 width <= 0 时不 wrap（原样返回）。
func TestWrapTextSimple_ZeroWidth(t *testing.T) {
	got := wrapTextSimple("no wrap", 0)
	if got != "no wrap" {
		t.Errorf("wrap with width=0 should return original, got %q", got)
	}
}
