package tui

import (
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// N13 L0 引擎单元测试（反例优先）。
//
// 关键约束：
// - 反例测试优先（验证非法行为被拒绝）。
// - 不使用 httptest（Winsock 损坏）。
// - mock renderFn（不依赖真实 glamour）。
// - 所有新增函数有中文注释。

// ---------------------------------------------------------------------------
// 契约自检测试：验证三个实现满足 L0 接口契约（02 §2.2.2）。
// ---------------------------------------------------------------------------

// TestVirtualScroll_InterfaceCompliance 验证 messageVirtualScroll 实现 VirtualScroll 接口。
func TestVirtualScroll_InterfaceCompliance(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	var _ VirtualScroll = NewMessageVirtualScroll(cache, m)
}

// TestRenderCache_InterfaceCompliance 验证 lruRenderCache 实现 RenderCache 接口。
func TestRenderCache_InterfaceCompliance(t *testing.T) {
	var _ RenderCache = NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
}

// TestTextMeasurer_InterfaceCompliance 验证 runewidthMeasurer 实现 TextMeasurer 接口。
func TestTextMeasurer_InterfaceCompliance(t *testing.T) {
	var _ TextMeasurer = NewRunewidthMeasurer(defaultWidthCacheSize)
}

// ---------------------------------------------------------------------------
// 虚拟滚动测试
// ---------------------------------------------------------------------------

// fixedHeightCache 是测试用的 mock RenderCache，每条消息返回固定高度。
//
// 不依赖真实 glamour，确保 VisibleRange 测试可预测。
// T1-P1-B7 ：新增 mu 串行化 GetOrRender（含 renderFn 调用），
// 与真实 lruRenderCache 行为一致（renderFn 内部调 glamour.Render，
// glamour 内部 buffer 非线程安全，需 RenderCache 串行化）。
type fixedHeightCache struct {
	mu     sync.Mutex
	height int
	hits   map[string]int // cacheKey → 命中次数
}

// GetOrRender 返回固定高度（不调 renderFn）。
//
// T1-P1-B7 ：持 mu 锁串行化 renderFn 调用，避免并发测试中
// glamour 内部 buffer 数据竞争（与真实 lruRenderCache 行为一致）。
func (c *fixedHeightCache) GetOrRender(cacheKey string, renderFn func() (string, int, error)) (string, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hits == nil {
		c.hits = make(map[string]int)
	}
	c.hits[cacheKey]++
	// 调一次 renderFn 获取内容（但用固定 height 覆盖）。
	rendered, _, err := renderFn()
	if err != nil {
		return "", 0, err
	}
	return rendered, c.height, nil
}
func (c *fixedHeightCache) Invalidate(string) {}
func (c *fixedHeightCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits = make(map[string]int)
}
func (c *fixedHeightCache) Stats() CacheStats { return CacheStats{} }

// newTestVirtualScroll 构造测试用虚拟滚动（每条消息固定高度，便于断言）。
//
// 用 fixedHeightCache 控制每条消息的渲染高度，避免依赖真实 glamour。
func newTestVirtualScroll(numMsgs, heightPerMsg, viewportH int) VirtualScroll {
	cache := &fixedHeightCache{height: heightPerMsg}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewMessageVirtualScroll(cache, m)

	msgs := make([]string, numMsgs)
	for i := range msgs {
		msgs[i] = "x"
	}
	v.SetMessages(msgs)
	v.SetViewport(viewportH)
	return v
}

// TestVisibleRange_FirstScreen 首屏返回 [0, viewportH/heightPerMsg)。
func TestVisibleRange_FirstScreen(t *testing.T) {
	v := newTestVirtualScroll(100, 2, 10) // 100 条消息，每条 2 行，视口 10 行
	first, last := v.VisibleRange(0)
	// 首屏：从 0 开始，10 行 / 2 行每条 = 5 条。
	if first != 0 {
		t.Errorf("首屏 first 应为 0，实际 %d", first)
	}
	if last != 5 {
		t.Errorf("首屏 last 应为 5，实际 %d", last)
	}
}

// TestVisibleRange_ScrollMiddle 滚动到中间返回正确范围。
func TestVisibleRange_ScrollMiddle(t *testing.T) {
	v := newTestVirtualScroll(100, 2, 10) // 每条 2 行
	// 滚动偏移 20 行 = 跳过前 10 条消息，首屏从第 10 条开始。
	first, last := v.VisibleRange(20)
	if first != 10 {
		t.Errorf("中间屏 first 应为 10，实际 %d", first)
	}
	if last != 15 {
		t.Errorf("中间屏 last 应为 15，实际 %d", last)
	}
}

// TestVisibleRange_LastScreen 末屏返回 [last, len)。
func TestVisibleRange_LastScreen(t *testing.T) {
	v := newTestVirtualScroll(10, 2, 10) // 10 条消息，每条 2 行，总 20 行
	// 滚动到末尾：偏移 18 行 = 跳过前 9 条，剩第 9 条（1 条）。
	first, last := v.VisibleRange(18)
	if first != 9 {
		t.Errorf("末屏 first 应为 9，实际 %d", first)
	}
	if last != 10 {
		t.Errorf("末屏 last 应为 10，实际 %d", last)
	}
}

// TestVisibleRange_EmptyMessages 空消息列表返回 [0, 0)。
func TestVisibleRange_EmptyMessages(t *testing.T) {
	v := newTestVirtualScroll(0, 2, 10)
	first, last := v.VisibleRange(0)
	if first != 0 || last != 0 {
		t.Errorf("空消息应返回 [0, 0)，实际 [%d, %d)", first, last)
	}
}

// TestRenderVisible_OnlyVisibleRendered 验证仅渲染可见消息。
//
// 用 fixedHeightCache 的 hits 计数器验证不可见消息不触发渲染。
func TestRenderVisible_OnlyVisibleRendered(t *testing.T) {
	cache := &fixedHeightCache{height: 2}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewMessageVirtualScroll(cache, m)

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

// TestRenderVisible_RenderFailFallsBack 渲染失败走 renderFallback 降级。
func TestRenderVisible_RenderFailFallsBack(t *testing.T) {
	// 用自定义 RenderCache 让 renderFn 失败。
	cache := &failingRenderCache{}
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	v := NewMessageVirtualScroll(cache, m)

	v.SetMessages([]string{"hello"})
	v.SetViewport(10)
	out := v.RenderVisible(0)
	if !strings.Contains(out, "[render failed]") {
		t.Errorf("渲染失败应走 renderFallback，实际输出: %q", out)
	}
}

// failingRenderCache 让所有 GetOrRender 调用返回 error。
type failingRenderCache struct{}

func (f *failingRenderCache) GetOrRender(string, func() (string, int, error)) (string, int, error) {
	return "", 0, errRenderFail
}
func (f *failingRenderCache) Invalidate(string) {}
func (f *failingRenderCache) InvalidateAll()    {}
func (f *failingRenderCache) Stats() CacheStats { return CacheStats{} }

// errRenderFail 测试用渲染失败错误。
var errRenderFail = &simpleError{"render failed"}

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// 渲染缓存测试
// ---------------------------------------------------------------------------

// TestGetOrRender_HitSkipsRenderFn 命中时跳过 renderFn。
func TestGetOrRender_HitSkipsRenderFn(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	calls := 0
	renderFn := func() (string, int, error) {
		calls++
		return "rendered", 1, nil
	}
	// 首次未命中，调 renderFn。
	cache.GetOrRender("key1", renderFn)
	if calls != 1 {
		t.Fatalf("首次应调 renderFn，实际 %d 次", calls)
	}
	// 二次命中，跳过 renderFn。
	cache.GetOrRender("key1", renderFn)
	if calls != 1 {
		t.Errorf("命中应跳过 renderFn，实际调用 %d 次", calls)
	}
}

// TestGetOrRender_MissCallsRenderFn 未命中时调 renderFn。
func TestGetOrRender_MissCallsRenderFn(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, defaultCacheMaxBytes)
	calls := 0
	renderFn := func() (string, int, error) {
		calls++
		return "rendered", 1, nil
	}
	cache.GetOrRender("key1", renderFn)
	cache.GetOrRender("key2", renderFn)
	if calls != 2 {
		t.Errorf("两次不同 key 应调 2 次 renderFn，实际 %d 次", calls)
	}
}

// TestLRUEviction_OverMaxEntries 超限淘汰最久未访问。
func TestLRUEviction_OverMaxEntries(t *testing.T) {
	cache := NewLRURenderCache(3, 0).(*lruRenderCache)
	renderFn := func() (string, int, error) { return "x", 1, nil }
	cache.GetOrRender("k1", renderFn)
	cache.GetOrRender("k2", renderFn)
	cache.GetOrRender("k3", renderFn)
	// 插入第 4 条，淘汰 k1（最久未访问）。
	cache.GetOrRender("k4", renderFn)
	if cache.entries.Len() != 3 {
		t.Errorf("LRU 上限 3，实际 %d", cache.entries.Len())
	}
	if _, ok := cache.index["k1"]; ok {
		t.Error("k1 应被淘汰")
	}
	if _, ok := cache.index["k4"]; !ok {
		t.Error("k4 应存在")
	}
}

// TestInvalidate_SingleKey 失效单个 key。
func TestInvalidate_SingleKey(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, 0).(*lruRenderCache)
	renderFn := func() (string, int, error) { return "x", 1, nil }
	cache.GetOrRender("k1", renderFn)
	cache.GetOrRender("k2", renderFn)
	cache.Invalidate("k1")
	if _, ok := cache.index["k1"]; ok {
		t.Error("k1 失效后应不存在")
	}
	if _, ok := cache.index["k2"]; !ok {
		t.Error("k2 应仍存在")
	}
}

// TestInvalidateAll_ClearsAll 清空全部缓存。
func TestInvalidateAll_ClearsAll(t *testing.T) {
	cache := NewLRURenderCache(defaultCacheEntries, 0).(*lruRenderCache)
	renderFn := func() (string, int, error) { return "x", 1, nil }
	cache.GetOrRender("k1", renderFn)
	cache.GetOrRender("k2", renderFn)
	cache.InvalidateAll()
	if cache.entries.Len() != 0 {
		t.Errorf("InvalidateAll 后条目应为 0，实际 %d", cache.entries.Len())
	}
	if len(cache.index) != 0 {
		t.Errorf("InvalidateAll 后 index 应为空，实际 %d", len(cache.index))
	}
}

// ---------------------------------------------------------------------------
// 文本测量测试
// ---------------------------------------------------------------------------

// TestStringWidth_CJK_ReturnsDoubleWidth CJK 宽字符返回 2（"中文" → 4）。
func TestStringWidth_CJK_ReturnsDoubleWidth(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	w := m.StringWidth("中文")
	if w != 4 {
		t.Errorf("CJK \"中文\" 宽度应为 4，实际 %d", w)
	}
}

// TestStringWidth_LRU_CacheHit LRU 缓存命中跳过 runewidth。
func TestStringWidth_LRU_CacheHit(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize).(*runewidthMeasurer)
	m.StringWidth("hello")
	// 二次查询应命中缓存。
	w := m.StringWidth("hello")
	if w != 5 {
		t.Errorf("\"hello\" 宽度应为 5，实际 %d", w)
	}
	// 验证缓存中有该条目。
	if _, ok := m.widthCache.Get("hello"); !ok {
		t.Error("LRU 缓存应命中")
	}
}

// TestWrapText_CJK_NotSplitInMiddle CJK 宽字符不在中间被切断。
func TestWrapText_CJK_NotSplitInMiddle(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	// "中文中文" 宽度 8，limit=4 应在第 2 个字符后换行（"中文\n中文"）。
	out := m.WrapText("中文中文", 4)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("应换行 2 行，实际 %d 行: %q", len(lines), out)
	}
	if lines[0] != "中文" {
		t.Errorf("第 1 行应为 \"中文\"，实际 %q", lines[0])
	}
	if lines[1] != "中文" {
		t.Errorf("第 2 行应为 \"中文\"，实际 %q", lines[1])
	}
}

// TestWrapText_ANSIStyledCJKPreservesSequences 验证 Markdown/颜色渲染后的
// ANSI 序列不会被换行器从中间切断并泄漏 m、k 等控制序列字符。
func TestWrapText_ANSIStyledCJKPreservesSequences(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	plain := "西风漫卷梧桐叶"
	styled := "\x1b[38;2;249;38;114m" + plain + "\x1b[0m"

	out := m.WrapText(styled, 8)
	gotPlain := strings.ReplaceAll(ansi.Strip(out), "\n", "")
	if gotPlain != plain {
		t.Fatalf("ANSI 换行后出现控制序列残片\nwant: %q\n got: %q\n raw: %q", plain, gotPlain, out)
	}
	if len(strings.Split(out, "\n")) != 2 {
		t.Fatalf("中文宽度 14、limit=8 应换成 2 行，got %q", out)
	}
}

func TestStringWidth_IgnoresANSISequences(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	if got := m.StringWidth("\x1b[31m中文\x1b[0m"); got != 4 {
		t.Fatalf("ANSI 样式不应计入显示宽度，want 4, got %d", got)
	}
}

// TestTruncate_Utf8RuneBoundarySafe 截断不产生半个字符。
func TestTruncate_Utf8RuneBoundarySafe(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	// "中文" 宽度 4，maxWidth=3 应截断为 "中"（宽度 2），不能产生半个字符。
	out := m.Truncate("中文", 3)
	if out != "中" {
		t.Errorf("maxWidth=3 应截断为 \"中\"，实际 %q", out)
	}
}

// TestTruncate_CJK_RespectDisplayWidth 按显示宽度截断（非 rune 数）。
func TestTruncate_CJK_RespectDisplayWidth(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	// "abc中" 宽度 5（3+2），maxWidth=4 应截断为 "abc"（宽度 3，不能包含 "中" 因 3+2=5>4）。
	out := m.Truncate("abc中", 4)
	if out != "abc" {
		t.Errorf("maxWidth=4 应截断为 \"abc\"，实际 %q", out)
	}
}

// TestTruncate_EmptyString 空字符串截断返回空。
func TestTruncate_EmptyString(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	if out := m.Truncate("", 10); out != "" {
		t.Errorf("空字符串截断应返回空，实际 %q", out)
	}
}

// TestTruncate_ZeroMaxWidth maxWidth=0 返回空。
func TestTruncate_ZeroMaxWidth(t *testing.T) {
	m := NewRunewidthMeasurer(defaultWidthCacheSize)
	if out := m.Truncate("hello", 0); out != "" {
		t.Errorf("maxWidth=0 应返回空，实际 %q", out)
	}
}

// ---------------------------------------------------------------------------
// 消息裁剪测试
// ---------------------------------------------------------------------------

// TestAppend_EvictOldest_WhenOverMax 超限时淘汰最旧。
func TestAppend_EvictOldest_WhenOverMax(t *testing.T) {
	b := NewMessageBuffer(3, nil)
	b.Append("msg1")
	b.Append("msg2")
	b.Append("msg3")
	b.Append("msg4") // 淘汰 msg1
	all := b.All()
	if len(all) != 3 {
		t.Fatalf("裁剪后应剩 3 条，实际 %d", len(all))
	}
	if all[0] != "msg2" {
		t.Errorf("淘汰最旧后首条应为 msg2，实际 %q", all[0])
	}
	if all[2] != "msg4" {
		t.Errorf("末条应为 msg4，实际 %q", all[2])
	}
}

// TestAppend_TriggerOnEvict 淘汰时触发 onEvict 回调。
func TestAppend_TriggerOnEvict(t *testing.T) {
	var evicted []string
	var mu sync.Mutex
	b := NewMessageBuffer(2, func(s string) {
		mu.Lock()
		evicted = append(evicted, s)
		mu.Unlock()
	})
	b.Append("msg1")
	b.Append("msg2")
	b.Append("msg3") // 淘汰 msg1
	b.Append("msg4") // 淘汰 msg2
	mu.Lock()
	defer mu.Unlock()
	if len(evicted) != 2 {
		t.Fatalf("应触发 2 次 onEvict，实际 %d", len(evicted))
	}
	if evicted[0] != "msg1" || evicted[1] != "msg2" {
		t.Errorf("淘汰顺序应为 [msg1, msg2]，实际 %v", evicted)
	}
}

// TestMessageBuffer_AllReturnsCopy All 返回副本，调用方修改不影响内部状态。
func TestMessageBuffer_AllReturnsCopy(t *testing.T) {
	b := NewMessageBuffer(10, nil)
	b.Append("msg1")
	all := b.All()
	all[0] = "modified"
	// 内部状态不应被影响。
	if b.All()[0] != "msg1" {
		t.Error("All 应返回副本，调用方修改不应影响内部状态")
	}
}

// ---------------------------------------------------------------------------
// 降级测试
// ---------------------------------------------------------------------------

// TestRenderFallback_NoPanic 渲染失败不 panic。
func TestRenderFallback_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("renderFallback 不应 panic，实际 recover: %v", r)
		}
	}()
	out := renderFallback("some message content")
	if !strings.Contains(out, "[render failed]") {
		t.Errorf("降级输出应包含 [render failed]，实际 %q", out)
	}
}

// TestRenderFallback_TruncateSafe 降级截断 utf8 rune 边界安全。
func TestRenderFallback_TruncateSafe(t *testing.T) {
	// 构造超长中文消息，验证截断不产生半个字符。
	longMsg := strings.Repeat("中文", 200) // 400 个 rune，800 字节
	out := renderFallback(longMsg)
	// 应被截断到 200 rune + "..."。
	if !strings.HasSuffix(out, "...") {
		t.Errorf("降级截断应以 ... 结尾，实际 %q", out[len(out)-10:])
	}
	// 验证不含半个字符（解码成功即可）。
	// 排除前缀 "[render failed] " 后检查 rune 数。
	prefix := "[render failed] "
	body := strings.TrimPrefix(out, prefix)
	body = strings.TrimSuffix(body, "...")
	runes := []rune(body)
	if len(runes) != 200 {
		t.Errorf("降级截断应保留 200 rune，实际 %d", len(runes))
	}
}

// TestTruncateForFallback_ShortString 短字符串不截断。
func TestTruncateForFallback_ShortString(t *testing.T) {
	out := truncateForFallback("short", 200)
	if out != "short" {
		t.Errorf("短字符串不应截断，实际 %q", out)
	}
}

// TestTruncateForFallback_NegativeMax 负 maxRunes 返回空。
func TestTruncateForFallback_NegativeMax(t *testing.T) {
	out := truncateForFallback("any", -1)
	if out != "" {
		t.Errorf("负 maxRunes 应返回空，实际 %q", out)
	}
}

// ---------------------------------------------------------------------------
// 辅助函数测试
// ---------------------------------------------------------------------------

// TestHashMessage_Stable 相同输入产生相同 hash。
func TestHashMessage_Stable(t *testing.T) {
	h1 := hashMessage("hello")
	h2 := hashMessage("hello")
	if h1 != h2 {
		t.Error("相同输入应产生相同 hash")
	}
}

// TestHashMessage_DifferentInput 不同输入产生不同 hash。
func TestHashMessage_DifferentInput(t *testing.T) {
	h1 := hashMessage("hello")
	h2 := hashMessage("world")
	if h1 == h2 {
		t.Error("不同输入应产生不同 hash")
	}
}

// TestCountLines 统计行数。
func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"single line", "hello", 1},
		{"two lines", "hello\nworld", 2},
		{"trailing newline", "hello\n", 1},
		{"three lines", "a\nb\nc", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countLines(tt.s); got != tt.want {
				t.Errorf("countLines(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 并发安全测试（v1 审查 B-1 / 缺失测试补充）。
// 验证 RenderCache / messageBuffer / messageVirtualScroll 在多 goroutine 并发访问下不触发 data race。
// 本机无 gcc 无法跑 -race，CI 补跑；这些测试至少保证逻辑正确性。
// ---------------------------------------------------------------------------

// TestRenderCache_ConcurrentAccess 并发读写 RenderCache，验证 LRU 操作线程安全。
//
// v1 审查 §反例测试缺失：补充 TestRenderCache_ConcurrentAccess。
func TestRenderCache_ConcurrentAccess(t *testing.T) {
	cache := NewLRURenderCache(100, 0)
	var wg sync.WaitGroup
	// 8 个 goroutine 并发 GetOrRender 不同 key
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := hashMessage("msg-" + string(rune('a'+gid)) + string(rune('a'+i%26)))
				_, _, _ = cache.GetOrRender(key, func() (string, int, error) {
					return "rendered", 1, nil
				})
			}
		}(g)
	}
	// 2 个 goroutine 并发 Invalidate
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				cache.Invalidate(hashMessage("msg-" + string(rune('a'+gid)) + string(rune('a'+i%26))))
			}
		}(g)
	}
	// 1 个 goroutine 并发 Stats
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = cache.Stats()
		}
	}()
	wg.Wait()
	// 不 panic 即通过；Stats.Entries 应 <= maxEntries=100
	stats := cache.Stats()
	if stats.Entries > 100 {
		t.Errorf("并发后 Entries=%d 超过上限 100", stats.Entries)
	}
}

// TestMessageBuffer_ConcurrentAppend 并发 Append messageBuffer，验证 FIFO 裁剪线程安全。
//
// v1 审查 §反例测试缺失：补充 TestMessageBuffer_ConcurrentAppend。
func TestMessageBuffer_ConcurrentAppend(t *testing.T) {
	var evicted []string
	var evictMu sync.Mutex
	buf := NewMessageBuffer(50, func(s string) {
		evictMu.Lock()
		evicted = append(evicted, s)
		evictMu.Unlock()
	})
	var wg sync.WaitGroup
	// 10 个 goroutine 各追加 10 条
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				buf.Append("msg-" + string(rune('a'+gid)) + string(rune('a'+i)))
			}
		}(g)
	}
	wg.Wait()
	// 总追加 100 条，max=50，应保留 50 条，淘汰 50 条
	if got := buf.Len(); got != 50 {
		t.Errorf("并发后 Len=%d，期望 50", got)
	}
	evictMu.Lock()
	evictedCount := len(evicted)
	evictMu.Unlock()
	if evictedCount != 50 {
		t.Errorf("onEvict 回调次数=%d，期望 50", evictedCount)
	}
}

// TestMessageBuffer_ConcurrentSetAll 并发 SetAll + All，验证批量替换线程安全。
func TestMessageBuffer_ConcurrentSetAll(t *testing.T) {
	buf := NewMessageBuffer(10, nil)
	var wg sync.WaitGroup
	// 5 个 goroutine 并发 SetAll
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			msgs := make([]string, 15)
			for i := range msgs {
				msgs[i] = "msg-" + string(rune('a'+gid)) + string(rune('a'+i))
			}
			buf.SetAll(msgs)
		}(g)
	}
	// 5 个 goroutine 并发 All
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = buf.All()
			}
		}()
	}
	wg.Wait()
	// 不 panic 且 Len <= maxMessages=10 即通过
	if got := buf.Len(); got > 10 {
		t.Errorf("并发后 Len=%d 超过上限 10", got)
	}
}

// TestVirtualScroll_ConcurrentAccess 并发 VisibleRange / TotalHeight / MessageHeight，验证 B-1 修复后无数据竞争。
//
// v1 审查 B-1：原实现 RLock 下写 heightCache，修复为 Lock。本测试验证并发调用不 panic。
func TestVirtualScroll_ConcurrentAccess(t *testing.T) {
	cache := &fixedHeightCache{height: 3, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(64)
	vs := NewMessageVirtualScroll(cache, m)
	msgs := make([]string, 20)
	for i := range msgs {
		msgs[i] = "msg-" + string(rune('a'+i))
	}
	vs.SetMessages(msgs)
	vs.SetViewport(10)

	var wg sync.WaitGroup
	// 4 个 goroutine 并发 VisibleRange
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				vs.VisibleRange(offset + i)
			}
		}(g * 5)
	}
	// 2 个 goroutine 并发 TotalHeight
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = vs.TotalHeight()
			}
		}()
	}
	// 2 个 goroutine 并发 MessageHeight
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = vs.MessageHeight(gid + i%20)
			}
		}(g)
	}
	wg.Wait()
	// 不 panic 即通过
}

// ---------------------------------------------------------------------------
// T-1 回归测试：SetMessages 内容未变时不重置 heightCache / totalHeight。
// ---------------------------------------------------------------------------

// TestSetMessages_UnchangedPreservesCache 验证 v1 审查 T-1 修复：
// 相同内容再次 SetMessages 时，heightCache 与 totalHeight 保留，避免缓存优化失效。
func TestSetMessages_UnchangedPreservesCache(t *testing.T) {
	cache := &fixedHeightCache{height: 2, hits: make(map[string]int)}
	m := NewRunewidthMeasurer(64)
	vs := NewMessageVirtualScroll(cache, m)
	msgs := []string{"msg-a", "msg-b", "msg-c"}
	vs.SetMessages(msgs)
	vs.SetViewport(5)

	// 首次计算 TotalHeight，触发渲染
	firstTotal := vs.TotalHeight()
	if firstTotal != 6 { // 3 条 × 高度 2
		t.Fatalf("首次 TotalHeight=%d，期望 6", firstTotal)
	}
	firstHits := 0
	for _, n := range cache.hits {
		firstHits += n
	}
	if firstHits == 0 {
		t.Fatal("首次应触发渲染")
	}

	// 再次 SetMessages 相同内容，缓存应保留
	vs.SetMessages(msgs)
	secondTotal := vs.TotalHeight()
	if secondTotal != firstTotal {
		t.Errorf("相同内容 TotalHeight 应不变：first=%d second=%d", firstTotal, secondTotal)
	}
	// totalHeight 缓存命中，不应重新触发渲染（hits 不应增长）
	secondHits := 0
	for _, n := range cache.hits {
		secondHits += n
	}
	if secondHits != firstHits {
		t.Errorf("相同内容 SetMessages 后 hits 从 %d 增长到 %d，缓存未保留", firstHits, secondHits)
	}
}
