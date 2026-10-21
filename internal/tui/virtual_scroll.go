package tui

import (
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// messageVirtualScroll 是 VirtualScroll 的默认实现（消息级窗口 + 行级缓存）。
//
// 维护 firstVisible / lastVisible 消息索引，仅渲染可见范围。
// 对标同类 CLI ScrollBox.tsx 的视口剔除 + pendingScrollDelta 排空机制。
//
// 关键约束：
// - 消息级窗口（仅渲染可见范围 ~50 条），不对标同类 CLI 行级虚拟滚动。
// - heightCache 作为 RenderCache 的二级缓存，仅在 messages 实际变化时重置。
// - sync.Mutex 保护并发访问（读写均持写锁，因 messageHeightLocked 会写 heightCache）。
type messageVirtualScroll struct {
	mu          sync.Mutex
	messages    []string     // 消息列表（值传递时复制，避免外部 slice 变更影响）
	viewportH   int          // 可视区域高度
	cache       RenderCache  // 渲染缓存（一级缓存）
	measurer    TextMeasurer // 文本测量
	heightCache map[int]int  // idx → 渲染后行数（二级缓存，避免重复计算）
	totalHeight int          // 总行数（缓存，messages 变更时重算）
}

// NewMessageVirtualScroll 构造消息级虚拟滚动实例。
//
// cache 为渲染缓存（一级缓存），measurer 为文本测量器。
func NewMessageVirtualScroll(cache RenderCache, measurer TextMeasurer) VirtualScroll {
	return &messageVirtualScroll{
		cache:       cache,
		measurer:    measurer,
		heightCache: make(map[int]int),
	}
}

// SetViewport 设置可视区域高度（由 viewport.Height() 传入）。
//
// viewport 变化不影响 heightCache，仅影响可见窗口计算。
func (v *messageVirtualScroll) SetViewport(height int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.viewportH != height {
		v.viewportH = height
	}
}

// SetMessages 设置消息列表（值传递，内部复制）。
//
// 调用方在 messages 追加 / 删除 / 修改后须再次调用。
// v1 审查 R2：值传递 + 复制避免外部 slice append 导致指针失效。
// v1 审查 T-1：仅在 messages 实际变化时重置 heightCache / totalHeight，
// 避免 renderMessages 每次调用 SetMessages 时缓存优化失效。
func (v *messageVirtualScroll) SetMessages(messages []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// 快速路径：长度不同直接变更。
	if len(messages) != len(v.messages) {
		v.messages = append([]string(nil), messages...)
		v.heightCache = make(map[int]int)
		v.totalHeight = 0
		return
	}
	// 长度相同：逐元素比较内容，仅当有差异时才重置缓存。
	changed := false
	for i := 0; i < len(messages); i++ {
		if v.messages[i] != messages[i] {
			changed = true
			break
		}
	}
	if !changed {
		return // 内容一致，保留 heightCache / totalHeight
	}
	v.messages = append([]string(nil), messages...)
	v.heightCache = make(map[int]int)
	v.totalHeight = 0
}

// VisibleRange 返回可见消息索引范围 [first, last)。
//
// scrollOffset 为 viewport 的滚动偏移（行数）。
// 算法：
// 1. 从 scrollOffset 开始，累计消息高度，跳过滚动偏移覆盖的消息。
// 2. 从 first 开始，累计直到填满 viewportH。
//
// v1 审查 B-1：使用 Lock 而非 RLock，因 messageHeightLocked 会写 heightCache。
func (v *messageVirtualScroll) VisibleRange(scrollOffset int) (first, last int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(v.messages) == 0 {
		return 0, 0
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	}

	// 跳过滚动偏移覆盖的消息。
	remaining := scrollOffset
	first = 0
	for i := 0; i < len(v.messages); i++ {
		h := v.messageHeightLocked(i)
		if h <= 0 {
			h = 1 // 防御性：空消息至少占 1 行，避免死循环
		}
		if remaining < h {
			first = i
			break
		}
		remaining -= h
		// 全部消息都被滚动偏移覆盖时，first 落到末尾。
		if i == len(v.messages)-1 {
			first = i + 1
		}
	}

	// 从 first 开始，累计直到填满 viewportH。
	viewportH := v.viewportH
	if viewportH <= 0 {
		viewportH = 1 // 防御性：未设置 viewport 时至少返回 1 条
	}
	filled := 0
	last = first
	for i := first; i < len(v.messages); i++ {
		h := v.messageHeightLocked(i)
		if h <= 0 {
			h = 1
		}
		filled += h
		last = i + 1
		if filled >= viewportH {
			break
		}
	}
	return first, last
}

// RenderVisible 渲染可见范围内的消息。
//
// 调用 RenderCache.GetOrRender 获取每条消息的缓存渲染结果。
// 渲染失败调 renderFallback 降级，不 panic。
func (v *messageVirtualScroll) RenderVisible(scrollOffset int) string {
	first, last := v.VisibleRange(scrollOffset)

	v.mu.Lock()
	msgs := append([]string(nil), v.messages...)
	v.mu.Unlock()

	if first >= len(msgs) {
		return ""
	}
	if last > len(msgs) {
		last = len(msgs)
	}

	var sb strings.Builder
	for i := first; i < last; i++ {
		cacheKey := hashMessage(msgs[i])
		rendered, _, err := v.cache.GetOrRender(cacheKey, func() (string, int, error) {
			r := renderMessageWithGlamour(msgs[i])
			return r, countLines(r), nil
		})
		if err != nil {
			// 渲染失败走 N6 错误结构化降级，不 panic 保证 TUI 可用。
			sb.WriteString(renderFallback(msgs[i]))
			sb.WriteString("\n")
			continue
		}
		sb.WriteString(rendered)
		sb.WriteString("\n")
	}
	return sb.String()
}

// MessageHeight 返回指定消息渲染后的行数。
//
// 优先从 heightCache 二级缓存获取，未命中时触发渲染并缓存。
//
// v1 审查 B-1：使用 Lock 而非 RLock，因 messageHeightLocked 会写 heightCache。
func (v *messageVirtualScroll) MessageHeight(idx int) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.messageHeightLocked(idx)
}

// messageHeightLocked 调用方持锁版本（二级缓存）。
//
// 优先从 heightCache 获取；未命中时调 RenderCache.GetOrRender 触发渲染，
// 并将结果写入 heightCache 二级缓存。
func (v *messageVirtualScroll) messageHeightLocked(idx int) int {
	if idx < 0 || idx >= len(v.messages) {
		return 0
	}
	if h, ok := v.heightCache[idx]; ok {
		return h
	}
	cacheKey := hashMessage(v.messages[idx])
	_, h, _ := v.cache.GetOrRender(cacheKey, func() (string, int, error) {
		rendered := renderMessageWithGlamour(v.messages[idx])
		return rendered, countLines(rendered), nil
	})
	if h <= 0 {
		h = 1 // 防御性：空消息至少 1 行
	}
	v.heightCache[idx] = h
	return h
}

// TotalHeight 返回所有消息渲染后的总行数。
//
// 结果缓存，messages 变更时（SetMessages）重置。
//
// v1 审查 B-1：使用 Lock 而非 RLock，因 messageHeightLocked 会写 heightCache。
func (v *messageVirtualScroll) TotalHeight() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.totalHeight > 0 {
		return v.totalHeight
	}
	if len(v.messages) == 0 {
		return 0
	}
	total := 0
	for i := 0; i < len(v.messages); i++ {
		total += v.messageHeightLocked(i)
	}
	v.totalHeight = total
	return total
}

// PrefetchBuffer 消息级虚拟滚动的 NO-OP 默认实现（T1-P1-B7 向后兼容）。
//
// messageVirtualScroll 不支持异步预渲染，恒返回 nil tea.Cmd。
// 调用方应检查返回值；nil 表示无异步任务。
func (v *messageVirtualScroll) PrefetchBuffer(scrollOffset int) tea.Cmd {
	return nil
}

// SetChunkRenderedHook 消息级虚拟滚动的 NO-OP 默认实现（T1-P1-B7 向后兼容）。
func (v *messageVirtualScroll) SetChunkRenderedHook(hook func(msg chunkRenderedMsg)) {
	// NO-OP：消息级虚拟滚动不支持异步渲染回调。
}

// InvalidateAll 消息级虚拟滚动的默认实现（T1-P1-B7 P1-1 修复）。
//
// 清空 heightCache / totalHeight；messages 保留（由 SetMessages 重新设置）。
// 终端宽度变化（WindowSizeMsg）时调用。
func (v *messageVirtualScroll) InvalidateAll() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.heightCache = make(map[int]int)
	v.totalHeight = 0
}

// ---------------------------------------------------------------------------
// chunkedVirtualScroll：分块渲染 + 视口缓冲 + 增量更新（T1-P1-B7）
// ---------------------------------------------------------------------------
//
// 维护 messages + msgLines（每条消息渲染后按行切片）+ msgHashes（内容 hash 用于增量更新）。
// VisibleRange 返回可见消息索引范围（不含 buffer，与 messageVirtualScroll 行为一致）。
// RenderVisible 仅拼接可见范围内的行切片，不重新渲染已缓存消息。
// PrefetchBuffer 返回 tea.Cmd 异步预渲染 buffer 范围内（上下各 bufferLines 行）的消息。
//
// 关键约束：
// - 行级切片按 \n 切分，不破坏 ANSI 转义序列（ANSI 不跨行）。
// - hash 对比识别变更消息，仅重渲染变更消息（增量更新）。
// - sync.Mutex 保护并发访问（msgLines / msgHashes / heightCache）。
// - chunkRenderedMsg 携带 hash 防竞态（P1-3）：消息变更后旧渲染结果丢弃。
// - 异步渲染通过 RenderCache.GetOrRender 串行化 glamour.Render 调用（避免 glamour 内部 buffer 竞态）。
type chunkedVirtualScroll struct {
	mu              sync.Mutex
	messages        []string                   // 消息列表（值传递时复制）
	msgLines        [][]string                 // 每条消息渲染后按行切片（行级 chunk），nil 表示未渲染
	msgHashes       []string                   // 每条消息的内容 hash（增量更新判定）
	viewportH       int                        // 可视区域高度
	bufferLines     int                        // 上下预渲染缓冲行数（默认 viewportH/3，至少 5）
	cache           RenderCache                // 渲染缓存（一级缓存，串行化 glamour.Render）
	measurer        TextMeasurer               // 文本测量
	heightCache     map[int]int                // idx → 渲染后行数（二级缓存）
	totalHeight     int                        // 总行数（缓存，messages 变更时重算）
	chunkRenderHook func(msg chunkRenderedMsg) // 异步渲染完成回调
	pendingPrefetch map[int]bool               // 去重未完成的预渲染请求（P2-2）
}

// NewChunkedVirtualScroll 构造分块虚拟滚动实例（T1-P1-B7）。
//
// cache 为渲染缓存（一级缓存，串行化 glamour.Render），measurer 为文本测量器。
// bufferLines <= 0 时默认 viewportH/3（在 SetViewport 后延迟计算，至少 5 行）。
func NewChunkedVirtualScroll(cache RenderCache, measurer TextMeasurer, bufferLines int) VirtualScroll {
	return &chunkedVirtualScroll{
		cache:           cache,
		measurer:        measurer,
		bufferLines:     bufferLines,
		heightCache:     make(map[int]int),
		pendingPrefetch: make(map[int]bool),
	}
}

// SetViewport 设置可视区域高度，并同步 bufferLines（T1-P1-B7）。
//
// viewport 变化不影响 heightCache / msgLines，仅影响可见窗口与 buffer 计算。
// bufferLines <= 0 时默认 viewportH/3（P2-1 可配置化），至少 5 行。
func (v *chunkedVirtualScroll) SetViewport(height int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.viewportH != height {
		v.viewportH = height
		if v.bufferLines <= 0 {
			v.bufferLines = height / 3
			if v.bufferLines < 5 {
				v.bufferLines = 5
			}
		}
	}
}

// SetMessages 设置消息列表（值传递，内部复制 + 增量更新，T1-P1-B7）。
//
// 增量更新策略（P1-3 关键）：
// - 长度不同：全量重建 msgLines + msgHashes。
// - 长度相同：逐元素对比 hash，仅标记变更消息的 msgLines[i] = nil（待重新渲染）。
// - hash 相同的 msgLines[i] 保留，不重新渲染。
func (v *chunkedVirtualScroll) SetMessages(messages []string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// 长度不同：全量重建。
	if len(messages) != len(v.messages) {
		v.messages = append([]string(nil), messages...)
		v.msgLines = make([][]string, len(messages))
		v.msgHashes = make([]string, len(messages))
		for i, msg := range messages {
			v.msgHashes[i] = hashMessage(msg)
		}
		v.heightCache = make(map[int]int)
		v.totalHeight = 0
		v.pendingPrefetch = make(map[int]bool)
		return
	}

	// 长度相同：逐元素对比 hash，仅标记变更消息。
	changed := false
	for i, msg := range messages {
		newHash := hashMessage(msg)
		if v.msgHashes[i] != newHash {
			v.messages[i] = msg
			v.msgHashes[i] = newHash
			v.msgLines[i] = nil // 标记需重新渲染
			delete(v.heightCache, i)
			changed = true
		}
	}
	if changed {
		v.totalHeight = 0 // 重算总高度
	}
}

// VisibleRange 返回可见消息索引范围 [first, last)（T1-P1-B7）。
//
// scrollOffset 为 viewport 的滚动偏移（行数）。
// 算法同 messageVirtualScroll.VisibleRange，仅渲染可见范围（不含 buffer）。
// buffer 范围由 PrefetchBuffer 内部计算。
//
// v1 审查 B-1：使用 Lock 而非 RLock，因 messageHeightLocked 会写 heightCache。
func (v *chunkedVirtualScroll) VisibleRange(scrollOffset int) (first, last int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.visibleRangeLocked(scrollOffset)
}

// visibleRangeLocked 调用方持锁版本（T1-P1-B7）。
func (v *chunkedVirtualScroll) visibleRangeLocked(scrollOffset int) (first, last int) {
	if len(v.messages) == 0 {
		return 0, 0
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	}

	// 跳过滚动偏移覆盖的消息。
	remaining := scrollOffset
	first = 0
	for i := 0; i < len(v.messages); i++ {
		h := v.messageHeightLocked(i)
		if h <= 0 {
			h = 1
		}
		if remaining < h {
			first = i
			break
		}
		remaining -= h
		if i == len(v.messages)-1 {
			first = i + 1
		}
	}

	// 从 first 开始，累计直到填满 viewportH。
	viewportH := v.viewportH
	if viewportH <= 0 {
		viewportH = 1
	}
	filled := 0
	last = first
	for i := first; i < len(v.messages); i++ {
		h := v.messageHeightLocked(i)
		if h <= 0 {
			h = 1
		}
		filled += h
		last = i + 1
		if filled >= viewportH {
			break
		}
	}
	return first, last
}

// RenderVisible 渲染可见范围内的消息（T1-P1-B7）。
//
// 仅触发未渲染消息的同步渲染（msgLines[i] == nil 时）；已渲染消息直接拼接行切片。
// 渲染失败走 renderFallback 降级，不 panic。
func (v *chunkedVirtualScroll) RenderVisible(scrollOffset int) string {
	first, last := v.VisibleRange(scrollOffset)

	v.mu.Lock()
	msgs := append([]string(nil), v.messages...)
	v.mu.Unlock()

	if first >= len(msgs) {
		return ""
	}
	if last > len(msgs) {
		last = len(msgs)
	}

	var sb strings.Builder
	for i := first; i < last; i++ {
		lines := v.getOrRenderLinesSync(i)
		for _, line := range lines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// getOrRenderLinesSync 同步获取或渲染消息的行切片（T1-P1-B7）。
//
// msgLines[i] == nil 时触发渲染（经 RenderCache 串行化 glamour.Render）；
// 否则直接返回缓存行切片。
// 调用方不需持锁；内部通过 RenderCache.GetOrRender 串行化渲染。
func (v *chunkedVirtualScroll) getOrRenderLinesSync(idx int) []string {
	v.mu.Lock()
	if idx < 0 || idx >= len(v.messages) {
		v.mu.Unlock()
		return nil
	}
	if v.msgLines[idx] != nil {
		lines := v.msgLines[idx]
		v.mu.Unlock()
		return lines
	}
	msg := v.messages[idx]
	cacheKey := v.msgHashes[idx]
	cache := v.cache
	v.mu.Unlock()

	// 经 RenderCache.GetOrRender 渲染（串行化 glamour.Render，避免竞态）。
	rendered, _, err := cache.GetOrRender(cacheKey, func() (string, int, error) {
		r := renderMessageWithGlamour(msg)
		return r, countLines(r), nil
	})

	v.mu.Lock()
	defer v.mu.Unlock()
	if err != nil {
		// 渲染失败走 renderFallback 降级，不 panic。
		fallback := renderFallback(msg)
		lines := splitMessageToLines(fallback)
		v.msgLines[idx] = lines
		return lines
	}
	lines := splitMessageToLines(rendered)
	v.msgLines[idx] = lines
	return lines
}

// MessageHeight 返回指定消息渲染后的行数（T1-P1-B7）。
//
// 优先从 heightCache 二级缓存获取；未命中时从 msgLines 获取或触发渲染。
func (v *chunkedVirtualScroll) MessageHeight(idx int) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.messageHeightLocked(idx)
}

// messageHeightLocked 调用方持锁版本（T1-P1-B7）。
//
// 优先从 heightCache 获取；未命中时从 msgLines 获取行数；
// msgLines[i] 也为 nil 时返回 1（防御性，触发渲染由 getOrRenderLinesSync 负责）。
func (v *chunkedVirtualScroll) messageHeightLocked(idx int) int {
	if idx < 0 || idx >= len(v.messages) {
		return 0
	}
	if h, ok := v.heightCache[idx]; ok {
		return h
	}
	if v.msgLines[idx] != nil {
		h := len(v.msgLines[idx])
		if h <= 0 {
			h = 1
		}
		v.heightCache[idx] = h
		return h
	}
	// msgLines 未渲染：经 RenderCache 获取行数（不存储 rendered 字符串，避免内存翻倍）。
	cacheKey := v.msgHashes[idx]
	_, h, _ := v.cache.GetOrRender(cacheKey, func() (string, int, error) {
		rendered := renderMessageWithGlamour(v.messages[idx])
		return rendered, countLines(rendered), nil
	})
	if h <= 0 {
		h = 1
	}
	v.heightCache[idx] = h
	return h
}

// TotalHeight 返回所有消息渲染后的总行数（T1-P1-B7）。
//
// 结果缓存，messages 变更时（SetMessages）重置。
func (v *chunkedVirtualScroll) TotalHeight() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.totalHeight > 0 {
		return v.totalHeight
	}
	if len(v.messages) == 0 {
		return 0
	}
	total := 0
	for i := 0; i < len(v.messages); i++ {
		total += v.messageHeightLocked(i)
	}
	v.totalHeight = total
	return total
}

// PrefetchBuffer 异步预渲染 buffer 范围内的消息（T1-P1-B7）。
//
// 返回 tea.Cmd，执行时通过 RenderCache.GetOrRender 串行化渲染 buffer 范围内未渲染的消息。
// 每条消息渲染完成后产生 chunkRenderedMsg，由 Update 路径处理回写。
// chunkRenderedMsg 携带 hash；若 SetMessages 已变更 msgHashes[i]，Update 路径丢弃过期消息（P1-3）。
// buffer 范围 = [first-bufferLines, last+bufferLines)（上下各 bufferLines 行）。
// 返回 nil 表示无待预渲染任务。
func (v *chunkedVirtualScroll) PrefetchBuffer(scrollOffset int) tea.Cmd {
	v.mu.Lock()
	first, last := v.visibleRangeLocked(scrollOffset)
	// 扩展 buffer 范围（上下各 bufferLines 行，按消息高度折算）。
	bufferFirst := v.expandBackwardLocked(first, v.bufferLines)
	bufferLast := v.expandForwardLocked(last, v.bufferLines)

	var pending []int
	for i := bufferFirst; i < bufferLast; i++ {
		if i < 0 || i >= len(v.messages) {
			continue
		}
		if v.msgLines[i] == nil && !v.pendingPrefetch[i] {
			v.pendingPrefetch[i] = true
			pending = append(pending, i)
		}
	}
	// 捕获副本用于闭包（避免闭包访问 v.messages 时竞态）。
	msgsCopy := make([]string, len(pending))
	hashesCopy := make([]string, len(pending))
	for j, idx := range pending {
		msgsCopy[j] = v.messages[idx]
		hashesCopy[j] = v.msgHashes[idx]
	}
	cache := v.cache
	v.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}

	var cmds []tea.Cmd
	for j, idx := range pending {
		idx := idx
		msg := msgsCopy[j]
		hash := hashesCopy[j]
		cmd := func() tea.Msg {
			// 经 RenderCache.GetOrRender 渲染（串行化 glamour.Render，避免竞态）。
			rendered, _, err := cache.GetOrRender(hash, func() (string, int, error) {
				r := renderMessageWithGlamour(msg)
				return r, countLines(r), nil
			})
			if err != nil {
				return chunkRenderedMsg{msgIdx: idx, hash: hash, err: err}
			}
			return chunkRenderedMsg{msgIdx: idx, hash: hash, lines: splitMessageToLines(rendered)}
		}
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// expandBackwardLocked 从 first 向前扩展 bufferLines 行（调用方持锁，T1-P1-B7）。
func (v *chunkedVirtualScroll) expandBackwardLocked(first, bufferLines int) int {
	if first <= 0 || bufferLines <= 0 {
		return first
	}
	remaining := bufferLines
	idx := first - 1
	for idx >= 0 && remaining > 0 {
		h := v.messageHeightLocked(idx)
		if h <= 0 {
			h = 1
		}
		remaining -= h
		idx--
	}
	return idx + 1
}

// expandForwardLocked 从 last 向后扩展 bufferLines 行（调用方持锁，T1-P1-B7）。
func (v *chunkedVirtualScroll) expandForwardLocked(last, bufferLines int) int {
	if last >= len(v.messages) || bufferLines <= 0 {
		return last
	}
	remaining := bufferLines
	idx := last
	for idx < len(v.messages) && remaining > 0 {
		h := v.messageHeightLocked(idx)
		if h <= 0 {
			h = 1
		}
		remaining -= h
		idx++
	}
	return idx
}

// SetChunkRenderedHook 设置异步渲染完成回调（T1-P1-B7）。
func (v *chunkedVirtualScroll) SetChunkRenderedHook(hook func(msg chunkRenderedMsg)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.chunkRenderHook = hook
}

// ApplyChunkRendered 处理 chunkRenderedMsg 回写 msgLines（T1-P1-B7 ，P1-3 竞态防护）。
//
// 由 Model.Update 路径在收到 chunkRenderedMsg 时调用。
// 验证 msgHashes[msgIdx] == msg.hash，不一致则丢弃（消息已变更，渲染结果过期）。
// 渲染失败（err != nil）时走 renderFallback 降级。
// 回写完成后清除 pendingPrefetch[msgIdx]，并触发 chunkRenderHook 通知调用方刷新。
func (v *chunkedVirtualScroll) ApplyChunkRendered(msg chunkRenderedMsg) {
	v.mu.Lock()
	// P1-3 竞态防护：验证 hash 一致性。
	if msg.msgIdx < 0 || msg.msgIdx >= len(v.msgHashes) {
		v.mu.Unlock()
		return
	}
	if v.msgHashes[msg.msgIdx] != msg.hash {
		// 消息已变更，渲染结果过期，丢弃。
		delete(v.pendingPrefetch, msg.msgIdx)
		v.mu.Unlock()
		return
	}
	if msg.err != nil {
		// 渲染失败走 renderFallback 降级。
		fallback := renderFallback(v.messages[msg.msgIdx])
		v.msgLines[msg.msgIdx] = splitMessageToLines(fallback)
	} else {
		v.msgLines[msg.msgIdx] = msg.lines
	}
	delete(v.heightCache, msg.msgIdx) // 高度缓存失效（重算）
	v.totalHeight = 0                 // 总高度重算
	delete(v.pendingPrefetch, msg.msgIdx)
	hook := v.chunkRenderHook
	v.mu.Unlock()

	// 锁外触发 hook，避免重入。
	if hook != nil {
		hook(msg)
	}
}

// InvalidateAll 清空全部渲染缓存（T1-P1-B7 ，P1-1 修复）。
//
// 清空 msgLines + heightCache + totalHeight + pendingPrefetch。
// 不清空 messages / msgHashes（消息列表不变，仅重新渲染）。
// 终端宽度变化（WindowSizeMsg）时调用。
func (v *chunkedVirtualScroll) InvalidateAll() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.msgLines {
		v.msgLines[i] = nil
	}
	v.heightCache = make(map[int]int)
	v.totalHeight = 0
	v.pendingPrefetch = make(map[int]bool)
}

// splitMessageToLines 按行切分渲染后字符串（行级 chunk，T1-P1-B7）。
//
// 按 \n 切分，不破坏 ANSI 转义序列（ANSI 转义不会跨行）。
// 末尾空行剔除（与 countLines 行为对齐）。
// 空串返回 nil（防御性，调用方应处理 nil）。
func splitMessageToLines(rendered string) []string {
	if rendered == "" {
		return nil
	}
	lines := strings.Split(rendered, "\n")
	// 剔除末尾空行（strings.Split 末尾 \n 会产生空元素）。
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
