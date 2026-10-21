package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// N13-ink-engine L0 引擎抽象层
//
// 本文件是 02 §2.2.2 契约的唯一主设计方：定义 VirtualScroll / RenderCache /
// TextMeasurer 三个导出接口，供 B1-B4 L1 组件实现，**不直接操作终端**。
//
// 关联 ADR：
// - 渲染引擎选型（保留 bubbletea + glamour + L0 抽象层）
// - 布局算法选型（lipgloss 手动布局，不引入 yoga）
// - 虚拟滚动实现策略（消息级窗口 + 行级缓存）
// - 文本测量机制（go-runewidth + LRU 缓存）
//
// 关联痛点（对标分析 P0）：
// - G-01 无虚拟滚动 → VirtualScroll
// - G-02 无渲染缓存 → RenderCache
// - G-03 无增量渲染 → RenderCache + VirtualScroll
// - G-04 无消息裁剪 → messageBuffer（见 message_buffer.go）
// - G-05 rune 数切片错位 → TextMeasurer

// L0 引擎相关常量。
const (
	// defaultMaxMessages 消息缓冲区上限，覆盖 8h 长会话，内存上限可控。
	// 2026-10-08 用户真机报障拍板：1000 太小，长会话数百轮即触顶，
	// FIFO 淘汰最旧消息 ⇒ 「历史对话消失了很多」且（alt-screen 下）
	// 被淘汰的内容物理不可恢复。救急提到 10000（约 1MB 指针数组），
	// 治根另议。
	defaultMaxMessages = 10000
	// defaultCacheEntries RenderCache LRU 上限。
	defaultCacheEntries = 500
	// defaultCacheMaxBytes RenderCache 字节上限（M10-02）。
	// 64MB 对标 Redis maxmemory 生产实践 50-100MB；
	// 500 条 × 平均 128KB = 64MB，覆盖长会话可见范围 + 历史回滚。
	defaultCacheMaxBytes = 64 * 1024 * 1024
	// defaultWidthCacheSize 文本宽度 LRU 上限，覆盖常见行宽查询。
	defaultWidthCacheSize = 4096
	// defaultStreamTickMs 流式 tick 间隔（沿用现状 app.go tickCmd）。
	defaultStreamTickMs = 100
	// streamRenderThreshold 流式渲染消息数阈值，超过此值启用虚拟滚动。
	streamRenderThreshold = 50
)

// VirtualScroll 是消息级虚拟滚动接口（L0 引擎核心）。
//
// 实现方维护 firstVisible / lastVisible 消息索引，仅渲染可见范围。
// 对标同类 CLI ScrollBox.tsx 的视口剔除机制。
//
// 契约约束（02 §2.2.2）：N13 是 L0 引擎唯一主设计方；B1-B4 L1 组件必须
// 通过此接口注册与渲染，不直接操作终端（不直接调 viewport.SetContent /
// tea.Println / fmt.Fprint(os.Stdout)）。
//
// T1-P1-B7 扩展：新增 PrefetchBuffer / SetChunkRenderedHook，
// 支持异步预渲染 buffer 范围内的消息（chunkedVirtualScroll 实现）。
// messageVirtualScroll 提供 NO-OP 默认实现，向后兼容。
type VirtualScroll interface {
	// SetViewport 设置可视区域高度（行数），由 viewport.Height() 调用方传入。
	SetViewport(height int)

	// SetMessages 设置消息列表（值传递，内部复制）。
	// messages 变更时调用方须再次调用以更新内部索引。
	// 值传递 + 复制避免外部 slice append 导致指针失效（v1 审查 R2）。
	SetMessages(messages []string)

	// VisibleRange 返回当前可见消息索引范围 [first, last)。
	// 滚动时由调用方传入 scrollOffset，VirtualScroll 据此计算可见窗口。
	VisibleRange(scrollOffset int) (first, last int)

	// RenderVisible 渲染可见范围内的消息，返回拼接后的字符串。
	// 内部调用 RenderCache.GetOrRender 获取每条消息的缓存渲染结果。
	RenderVisible(scrollOffset int) string

	// MessageHeight 返回指定消息渲染后的行数（用于滚动计算）。
	// 优先从 RenderCache 获取，缓存未命中时触发渲染并缓存。
	MessageHeight(idx int) int

	// TotalHeight 返回所有消息渲染后的总行数（用于 viewport.TotalHeight）。
	TotalHeight() int

	// PrefetchBuffer 异步预渲染 buffer 范围内的消息（T1-P1-B7 新增）。
	//
	// 返回 tea.Cmd，由 Bubbletea 调度执行；执行完成后产生 chunkRenderedMsg，
	// 由 Model.Update 路径处理回写 msgLines。
	// scrollOffset 为当前滚动偏移；buffer 范围由实现方决定（默认 viewportH/3）。
	// 返回 nil 表示无待预渲染任务（旧实现 messageVirtualScroll 恒返回 nil）。
	PrefetchBuffer(scrollOffset int) tea.Cmd

	// SetChunkRenderedHook 设置异步渲染完成回调（T1-P1-B7 新增）。
	//
	// hook 在 chunkRenderedMsg 被处理时调用，用于通知调用方刷新 View。
	// 旧实现 messageVirtualScroll 为 NO-OP。
	SetChunkRenderedHook(hook func(msg chunkRenderedMsg))

	// InvalidateAll 清空全部渲染缓存（T1-P1-B7 新增，P1-1 修复）。
	//
	// 终端宽度变化（WindowSizeMsg）时调用，清空 msgLines / heightCache。
	// 旧实现 messageVirtualScroll 走 SetMessages(nil) 路径。
	InvalidateAll()
}

// chunkRenderedMsg 异步渲染完成消息（Bubbletea tea.Msg，T1-P1-B7）。
//
// 由 PrefetchBuffer 返回的 tea.Cmd 产生，Update 路径处理回写 msgLines。
// 携带 hash 用于竞态防护（P1-3）：若 msgHashes[msgIdx] != hash，丢弃此消息。
type chunkRenderedMsg struct {
	msgIdx int      // 消息索引
	hash   string   // 消息内容 hash（触发渲染时的 hash，用于过期判定）
	lines  []string // 渲染后按行切片（nil 表示渲染失败）
	err    error    // 渲染错误（nil 表示成功）
}

// RenderCache 是消息渲染缓存接口（L0 引擎核心）。
//
// 缓存 key = message hash（内容寻址），value = 渲染后 ANSI 字符串 + 行数。
// 对标同类 CLI ink.tsx 的池重置机制（防长会话无限增长）。
//
// 契约约束（02 §2.2.2）：N13 是 L0 引擎唯一主设计方。
type RenderCache interface {
	// GetOrRender 获取或渲染消息。
	// cacheKey 为消息内容 hash（调用方计算，支持内容寻址去重）。
	// renderFn 为渲染函数（glamour.Render + 文本换行）。
	// 缓存命中时直接返回，未命中时调 renderFn 并缓存结果。
	GetOrRender(cacheKey string, renderFn func() (rendered string, height int, err error)) (string, int, error)

	// Invalidate 使指定 cacheKey 失效（消息变更时调用，如流式追加）。
	Invalidate(cacheKey string)

	// InvalidateAll 清空全部缓存（切换会话 / 终端宽度变化时调用）。
	InvalidateAll()

	// Stats 返回缓存统计（命中率 / 条目数 / 内存占用近似），供调试。
	Stats() CacheStats
}

// TextMeasurer 是文本测量接口（L0 引擎核心）。
//
// 封装 go-runewidth + LRU 缓存，解决 G-05（rune 数切片错位 CJK 宽字符）。
// 对标同类 CLI measure-text.ts + line-width-cache.ts。
//
// 契约约束（02 §2.2.2）：N13 是 L0 引擎唯一主设计方。
type TextMeasurer interface {
	// StringWidth 返回字符串的显示宽度（考虑 CJK 宽字符、emoji ZWJ）。
	// 优先从 LRU 缓存获取，未命中时调 runewidth.StringWidth。
	StringWidth(s string) int

	// WrapText 按显示宽度换行，返回换行后字符串。
	// 替代 cellbuf.Wrap 的 rune 数切片，按显示宽度切片。
	WrapText(s string, limit int) string

	// Truncate 按显示宽度截断字符串（utf8 rune 边界安全）。
	Truncate(s string, maxWidth int) string
}

// CacheStats 缓存统计。
type CacheStats struct {
	Hits        int64   // 命中次数
	Misses      int64   // 未命中次数
	Entries     int     // 当前条目数
	HitRate     float64 // 命中率
	MemoryBytes int64   // 近似内存占用
}

// renderFallback 渲染失败时的降级渲染。
//
// 关联 N6 错误结构化（02 §2.2.2 关联约束）。
// 新增 ErrorID 需经 N6 仲裁：ErrTUIRenderFail / ErrTUIVirtualScroll。
// 仲裁前用临时字符串标识，仲裁后替换为 ai.WithID(err, ai.ErrTUIRenderFail)。
//
// 关键约束：不 panic，保证 TUI 可用（返回降级字符串）。
func renderFallback(msg string) string {
	return fmt.Sprintf("[render failed] %s", truncateForFallback(msg, 200))
}

// truncateForFallback 降级截断（utf8 rune 边界安全，不产生半个字符）。
//
// 关联关卡 3（渲染失败降级零容忍）：截断必须 utf8 rune 边界安全。
func truncateForFallback(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
