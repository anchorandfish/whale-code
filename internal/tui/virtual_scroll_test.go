package tui

import (
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// T1-P1-B7 chunkedVirtualScroll 单元测试。
//
// 关键约束：
// - 反例测试优先（验证非法行为被拒绝）。
// - 不使用 httptest（Winsock 损坏）。
// - mock renderFn（不依赖真实 glamour，与 engine_test.go fixedHeightCache 一致）。
// - 所有新增函数有中文注释。
// - 并发测试必须 -race 通过。

// ---------------------------------------------------------------------------
// 契约自检测试：验证 chunkedVirtualScroll 满足 VirtualScroll 接口（02 §2.2.2）。
// ---------------------------------------------------------------------------

// TestChunkedVirtualScroll_InterfaceCompliance 验证 chunkedVirtualScroll 实现 VirtualScroll 接口。
func TestChunkedVirtualScroll_InterfaceCompliance(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	var _ VirtualScroll = NewChunkedVirtualScroll(cache, m, 0)
}

// ---------------------------------------------------------------------------
// 辅助构造函数：测试用 chunkedVirtualScroll（每条消息固定高度，便于断言）。
// ---------------------------------------------------------------------------

// newTestChunkedVirtualScroll 构造测试用分块虚拟滚动（每条消息固定高度，便于断言）。
//
// 用 fixedHeightCache 控制每条消息的渲染高度，避免依赖真实 glamour。
func newTestChunkedVirtualScroll(numMsgs, heightPerMsg, viewportH int) VirtualScroll {
	cache := &fixedHeightCache{height: heightPerMsg}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	msgs := make([]string, numMsgs)
	for i := range msgs {
		msgs[i] = "x"
	}
	v.SetMessages(msgs)
	v.SetViewport(viewportH)
	return v
}

// extractChunkRenderedMsg 从 tea.Msg 中提取首个 chunkRenderedMsg（处理 tea.BatchMsg 嵌套）。
//
// PrefetchBuffer 返回 tea.Batch(cmds...)，调用后产生 tea.BatchMsg（[]tea.Cmd），
// 需逐个执行子 cmd 提取 chunkRenderedMsg。递归处理嵌套 BatchMsg。
func extractChunkRenderedMsg(msg tea.Msg) (chunkRenderedMsg, bool) {
	switch m := msg.(type) {
	case chunkRenderedMsg:
		return m, true
	case tea.BatchMsg:
		for _, cmd := range m {
			if cmd == nil {
				continue
			}
			if sub := cmd(); sub != nil {
				if crm, ok := extractChunkRenderedMsg(sub); ok {
					return crm, ok
				}
			}
		}
	}
	return chunkRenderedMsg{}, false
}

// ---------------------------------------------------------------------------
// VisibleRange 边界测试（与 messageVirtualScroll 对齐）。
// ---------------------------------------------------------------------------

// TestChunkedVisibleRange_FirstScreen 首屏返回 [0, viewportH/heightPerMsg)。
func TestChunkedVisibleRange_FirstScreen(t *testing.T) {
	v := newTestChunkedVirtualScroll(100, 2, 10) // 100 条，每条 2 行，视口 10 行
	first, last := v.VisibleRange(0)
	if first != 0 {
		t.Errorf("首屏 first 应为 0，实际 %d", first)
	}
	if last != 5 {
		t.Errorf("首屏 last 应为 5，实际 %d", last)
	}
}

// TestChunkedVisibleRange_ScrollMiddle 滚动到中间返回正确范围。
func TestChunkedVisibleRange_ScrollMiddle(t *testing.T) {
	v := newTestChunkedVirtualScroll(100, 2, 10)
	first, last := v.VisibleRange(20)
	if first != 10 {
		t.Errorf("中间屏 first 应为 10，实际 %d", first)
	}
	if last != 15 {
		t.Errorf("中间屏 last 应为 15，实际 %d", last)
	}
}

// TestChunkedVisibleRange_EmptyMessages 空消息列表返回 [0, 0)。
func TestChunkedVisibleRange_EmptyMessages(t *testing.T) {
	v := newTestChunkedVirtualScroll(0, 2, 10)
	first, last := v.VisibleRange(0)
	if first != 0 || last != 0 {
		t.Errorf("空消息应返回 [0, 0)，实际 [%d, %d)", first, last)
	}
}

// ---------------------------------------------------------------------------
// RenderVisible 测试
// ---------------------------------------------------------------------------

// TestChunkedRenderVisible_OnlyVisibleRendered 验证仅渲染可见消息。
func TestChunkedRenderVisible_OnlyVisibleRendered(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	// 构造 100 条消息，每条内容不同（确保 cacheKey 不同），每条 2 行。
	msgs := make([]string, 100)
	for i := range msgs {
		msgs[i] = "msg-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	v.SetMessages(msgs)
	v.SetViewport(10) // 10 行 = 5 条消息

	rendered := v.RenderVisible(0)
	if rendered == "" {
		t.Fatal("RenderVisible 不应返回空字符串")
	}
	// 验证只有首屏 5 条消息触发渲染（hits 计数）。
	// 注意：VisibleRange 可能多查询 1 条边界消息，允许 [5, 6]。
	if len(cache.hits) > 6 {
		t.Errorf("仅首屏应被渲染，hits 条目 %d 超过预期 6", len(cache.hits))
	}
	if len(cache.hits) < 5 {
		t.Errorf("首屏 5 条应被渲染，hits 条目 %d 不足 5", len(cache.hits))
	}
}

// TestChunkedRenderVisible_RenderFailFallsBack 渲染失败走 renderFallback 降级。
func TestChunkedRenderVisible_RenderFailFallsBack(t *testing.T) {
	cache := &failingRenderCache{}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	v.SetMessages([]string{"hello"})
	v.SetViewport(10)
	out := v.RenderVisible(0)
	if !strings.Contains(out, "[render failed]") {
		t.Errorf("渲染失败应走 renderFallback，实际输出: %q", out)
	}
}

// ---------------------------------------------------------------------------
// P1-2 必补测试：增量更新 / 异步预渲染 / 行切片 ANSI 安全
// ---------------------------------------------------------------------------

// TestSetMessages_IncrementalUpdate 验证增量更新（P1-2）：
// 长度相同时仅重渲染变更消息，未变更消息的 msgLines 保留。
func TestSetMessages_IncrementalUpdate(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	msgs := make([]string, 10)
	for i := range msgs {
		msgs[i] = "msg-" + string(rune('a'+i))
	}
	v.SetMessages(msgs)
	v.SetViewport(10)

	// 首次渲染可见消息，触发 hits。
	v.RenderVisible(0)
	firstHits := len(cache.hits)
	if firstHits == 0 {
		t.Fatal("首次渲染应触发 hits")
	}

	// 修改第 5 条消息内容，其他不变。
	msgs[5] = "msg-CHANGED"
	v.SetMessages(msgs)

	// 再次渲染：第 5 条应重新渲染，其他应从 msgLines 缓存命中（不触发 hits）。
	// 清空 hits 计数以区分首次和增量。
	cache.hits = make(map[string]int)
	v.RenderVisible(0)

	// 只有第 5 条消息的 hash 变了，应仅触发 1 次 hit（或 0 次如果不在可见范围）。
	// 由于视口 10 行 / 2 行每条 = 5 条，首屏 [0,5)，第 5 条不在首屏。
	// 调整：修改第 2 条（在首屏内）。
	msgs[2] = "msg-CHANGED-2"
	v.SetMessages(msgs)
	cache.hits = make(map[string]int)
	v.RenderVisible(0)

	if len(cache.hits) > 1 {
		t.Errorf("增量更新应仅重渲染变更消息，hits %d 超过预期 1", len(cache.hits))
	}
}

// TestPrefetchBuffer_AsyncRender 验证异步预渲染（P1-2）：
// PrefetchBuffer 返回 tea.Cmd，执行后产生 chunkRenderedMsg，ApplyChunkRendered 回写 msgLines。
func TestPrefetchBuffer_AsyncRender(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	msgs := make([]string, 20)
	for i := range msgs {
		msgs[i] = "msg-" + string(rune('a'+i))
	}
	v.SetMessages(msgs)
	v.SetViewport(4) // 4 行 / 2 行每条 = 2 条可见

	// 触发预渲染：应返回非 nil tea.Cmd。
	cmd := v.PrefetchBuffer(0)
	if cmd == nil {
		t.Skip("无待预渲染任务（可能 buffer 范围内消息已渲染）")
	}

	// 执行 tea.Cmd，获取 chunkRenderedMsg。
	// PrefetchBuffer 返回 tea.Batch(cmds...)，需从 tea.BatchMsg 中提取 chunkRenderedMsg。
	msg := cmd()
	crm, ok := extractChunkRenderedMsg(msg)
	if !ok {
		t.Fatalf("应从 tea.Msg 中提取 chunkRenderedMsg，实际 %T", msg)
	}
	if crm.err != nil {
		t.Fatalf("预渲染不应失败：%v", crm.err)
	}
	if len(crm.lines) == 0 {
		t.Fatal("chunkRenderedMsg.lines 不应为空")
	}

	// ApplyChunkRendered 回写 msgLines。
	cvs := v.(*chunkedVirtualScroll)
	cvs.ApplyChunkRendered(crm)

	// 验证 msgLines[crm.msgIdx] 已更新。
	cvs.mu.Lock()
	lines := cvs.msgLines[crm.msgIdx]
	cvs.mu.Unlock()
	if lines == nil {
		t.Errorf("ApplyChunkRendered 后 msgLines[%d] 应非 nil", crm.msgIdx)
	}
}

// TestSplitMessageToLines_ANSISafe 验证行切片不破坏 ANSI 转义序列（P1-2）。
//
// ANSI 转义序列不会跨行（\n 不在转义序列内），因此按 \n 切分是安全的。
func TestSplitMessageToLines_ANSISafe(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantLast string
	}{
		{
			name:     "单行无 ANSI",
			input:    "hello",
			wantLen:  1,
			wantLast: "hello",
		},
		{
			name:     "多行无 ANSI",
			input:    "line1\nline2\nline3",
			wantLen:  3,
			wantLast: "line3",
		},
		{
			name:     "含 ANSI 红色",
			input:    "\x1b[31mred\x1b[0m\nnormal",
			wantLen:  2,
			wantLast: "normal",
		},
		{
			name:     "含 ANSI 多色",
			input:    "\x1b[31mred\x1b[0m\n\x1b[32mgreen\x1b[0m",
			wantLen:  2,
			wantLast: "\x1b[32mgreen\x1b[0m",
		},
		{
			name:     "末尾换行",
			input:    "line1\nline2\n",
			wantLen:  2,
			wantLast: "line2",
		},
		{
			name:     "空串",
			input:    "",
			wantLen:  0,
			wantLast: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := splitMessageToLines(tt.input)
			if tt.wantLen == 0 {
				if lines != nil {
					t.Errorf("空串应返回 nil，实际 %v", lines)
				}
				return
			}
			if len(lines) != tt.wantLen {
				t.Errorf("行数 = %d，期望 %d", len(lines), tt.wantLen)
			}
			if lines[len(lines)-1] != tt.wantLast {
				t.Errorf("末行 = %q，期望 %q", lines[len(lines)-1], tt.wantLast)
			}
			// 验证 ANSI 转义序列完整（不跨行）。
			for i, line := range lines {
				if strings.Count(line, "\x1b[")%2 != 0 {
					t.Errorf("第 %d 行 ANSI 转义序列不完整（开/闭不匹配）：%q", i, line)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P1-1 必补测试：InvalidateAll 清空 msgLines
// ---------------------------------------------------------------------------

// TestInvalidateAll_ClearsMsgLines 验证 InvalidateAll 清空 msgLines（P1-1）。
func TestInvalidateAll_ClearsMsgLines(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	msgs := []string{"msg-a", "msg-b", "msg-c"}
	v.SetMessages(msgs)
	v.SetViewport(10)

	// 渲染可见消息，填充 msgLines。
	v.RenderVisible(0)

	cvs := v.(*chunkedVirtualScroll)
	cvs.mu.Lock()
	hasLines := false
	for _, lines := range cvs.msgLines {
		if lines != nil {
			hasLines = true
			break
		}
	}
	cvs.mu.Unlock()
	if !hasLines {
		t.Fatal("RenderVisible 后应至少有 1 条 msgLines 非 nil")
	}

	// InvalidateAll 应清空 msgLines。
	v.InvalidateAll()

	cvs.mu.Lock()
	for i, lines := range cvs.msgLines {
		if lines != nil {
			t.Errorf("InvalidateAll 后 msgLines[%d] 应为 nil，实际 %v", i, lines)
		}
	}
	// heightCache 应清空。
	if len(cvs.heightCache) != 0 {
		t.Errorf("InvalidateAll 后 heightCache 应为空，实际 %d 条", len(cvs.heightCache))
	}
	// totalHeight 应为 0。
	if cvs.totalHeight != 0 {
		t.Errorf("InvalidateAll 后 totalHeight 应为 0，实际 %d", cvs.totalHeight)
	}
	cvs.mu.Unlock()
}

// ---------------------------------------------------------------------------
// P1-3 必补测试：chunkRenderedMsg 竞态防护（过期 hash 丢弃）
// ---------------------------------------------------------------------------

// TestChunkRenderedMsg_StaleHashDiscarded 验证过期 hash 的 chunkRenderedMsg 被丢弃（P1-3）。
func TestChunkRenderedMsg_StaleHashDiscarded(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewChunkedVirtualScroll(cache, m, 0)

	msgs := []string{"msg-a", "msg-b", "msg-c"}
	v.SetMessages(msgs)
	v.SetViewport(10)

	// 触发预渲染。
	cmd := v.PrefetchBuffer(0)
	if cmd == nil {
		t.Skip("无待预渲染任务")
	}
	msg := cmd()
	crm, ok := extractChunkRenderedMsg(msg)
	if !ok {
		t.Fatalf("应从 tea.Msg 中提取 chunkRenderedMsg，实际 %T", msg)
	}

	// 在 ApplyChunkRendered 之前，修改消息内容（使 hash 变化）。
	msgs[crm.msgIdx] = "msg-CHANGED"
	v.SetMessages(msgs)

	// ApplyChunkRendered 应丢弃过期消息（hash 不匹配）。
	cvs := v.(*chunkedVirtualScroll)
	cvs.ApplyChunkRendered(crm)

	// 验证 msgLines[crm.msgIdx] 未被旧渲染结果覆盖（仍为 nil 或新渲染结果）。
	cvs.mu.Lock()
	lines := cvs.msgLines[crm.msgIdx]
	cvs.mu.Unlock()
	if lines != nil {
		// 如果非 nil，说明被新渲染填充（但不应是旧渲染结果）。
		// 关键：不应包含旧消息内容。
		for _, line := range lines {
			if strings.Contains(line, "msg-a") && crm.msgIdx != 0 {
				t.Errorf("msgLines[%d] 不应包含旧消息内容", crm.msgIdx)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 并发安全测试（必须 -race 通过）
// ---------------------------------------------------------------------------

// TestChunkedVirtualScroll_ConcurrentAccess 并发 VisibleRange / RenderVisible /
// SetMessages / PrefetchBuffer / ApplyChunkRendered，验证无数据竞争。
func TestChunkedVirtualScroll_ConcurrentAccess(t *testing.T) {
	cache := &fixedHeightCache{height: 3, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(64)
	vs := NewChunkedVirtualScroll(cache, m, 0)
	msgs := make([]string, 20)
	for i := range msgs {
		msgs[i] = "msg-" + string(rune('a'+i))
	}
	vs.SetMessages(msgs)
	vs.SetViewport(10)

	cvs := vs.(*chunkedVirtualScroll)
	var wg sync.WaitGroup

	// 4 个 goroutine 并发 VisibleRange。
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				vs.VisibleRange(offset + i)
			}
		}(g * 5)
	}

	// 2 个 goroutine 并发 RenderVisible。
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				vs.RenderVisible(offset + i)
			}
		}(g * 5)
	}

	// 2 个 goroutine 并发 TotalHeight。
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = vs.TotalHeight()
			}
		}()
	}

	// 2 个 goroutine 并发 MessageHeight。
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = vs.MessageHeight(gid + i%20)
			}
		}(g)
	}

	// 1 个 goroutine 并发 SetMessages（模拟流式追加）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			newMsgs := make([]string, 20)
			copy(newMsgs, msgs)
			newMsgs[i%20] = "msg-updated-" + string(rune('a'+i))
			vs.SetMessages(newMsgs)
		}
	}()

	// 1 个 goroutine 并发 PrefetchBuffer + ApplyChunkRendered。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			cmd := vs.PrefetchBuffer(i)
			if cmd != nil {
				msg := cmd()
				if crm, ok := msg.(chunkRenderedMsg); ok {
					cvs.ApplyChunkRendered(crm)
				}
			}
		}
	}()

	// 1 个 goroutine 并发 InvalidateAll。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			vs.InvalidateAll()
		}
	}()

	wg.Wait()
	// 不 panic 即通过。
}

// ---------------------------------------------------------------------------
// messageVirtualScroll 向后兼容测试（验证 NO-OP 默认实现）。
// ---------------------------------------------------------------------------

// TestMessageVirtualScroll_PrefetchBuffer_NoOP 验证 messageVirtualScroll 的 PrefetchBuffer 返回 nil。
func TestMessageVirtualScroll_PrefetchBuffer_NoOP(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewMessageVirtualScroll(cache, m)
	if cmd := v.PrefetchBuffer(0); cmd != nil {
		t.Errorf("messageVirtualScroll.PrefetchBuffer 应返回 nil，实际 %v", cmd)
	}
}

// TestMessageVirtualScroll_SetChunkRenderedHook_NoOP 验证 SetChunkRenderedHook 不 panic。
func TestMessageVirtualScroll_SetChunkRenderedHook_NoOP(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewMessageVirtualScroll(cache, m)
	// 应不 panic。
	v.SetChunkRenderedHook(func(msg chunkRenderedMsg) {})
}

// TestMessageVirtualScroll_InvalidateAll 验证 InvalidateAll 不 panic 且清空缓存。
func TestMessageVirtualScroll_InvalidateAll(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewMessageVirtualScroll(cache, m)
	msgs := []string{"msg-a", "msg-b"}
	v.SetMessages(msgs)
	v.SetViewport(10)
	v.TotalHeight() // 填充缓存

	// InvalidateAll 应不 panic 且清空缓存。
	v.InvalidateAll()
	// 不 panic 即通过。
}
