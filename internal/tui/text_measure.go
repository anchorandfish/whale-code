package tui

import (
	"sync"

	"github.com/charmbracelet/x/ansi"
	lru "github.com/hashicorp/golang-lru/v2"
)

// runewidthMeasurer 是 TextMeasurer 的默认实现。
//
// 封装 github.com/mattn/go-runewidth + LRU 缓存。
// 对标同类 CLI measure-text.ts + line-width-cache.ts。
//
// 关键约束：
// - 使用 github.com/mattn/go-runewidth（纯 Go，无 cgo）。
// - LRU 缓存使用 github.com/hashicorp/golang-lru/v2（泛型版本）。
// - 支持 CJK 宽字符（width=2）。
// - emoji ZWJ 序列本批次降级处理（按单字符测宽），P2 引入 rivo/uniseg。
type runewidthMeasurer struct {
	widthCache *lru.Cache[string, int] // 字符串 → 显示宽度
	mu         sync.Mutex              // 保护 widthCache（hashicorp/golang-lru 自身线程安全，本锁保留作扩展点）
}

// NewRunewidthMeasurer 构造文本测量器。
//
// widthCacheSize 建议 4096（覆盖常见行宽查询）。
func NewRunewidthMeasurer(widthCacheSize int) TextMeasurer {
	if widthCacheSize <= 0 {
		widthCacheSize = defaultWidthCacheSize
	}
	c, err := lru.New[string, int](widthCacheSize)
	if err != nil {
		// 容量非正时 NewLRU 返回 error，已在前置校验避免；此处兜底用 1 容量。
		c, _ = lru.New[string, int](1)
	}
	return &runewidthMeasurer{
		widthCache: c,
	}
}

// StringWidth 返回字符串的显示宽度。
//
// 优先从 LRU 缓存获取，未命中时调 runewidth.StringWidth。
// 支持 CJK 宽字符（width=2）、emoji ZWJ 序列（按 grapheme cluster，本批次降级按单字符测宽）。
func (m *runewidthMeasurer) StringWidth(s string) int {
	if s == "" {
		return 0
	}
	if w, ok := m.widthCache.Get(s); ok {
		return w
	}
	// Messages may already contain ANSI styling from glamour/lipgloss. Counting
	// the printable bytes inside an escape sequence makes wrapping happen in the
	// middle of sequences such as "\x1b[38;2;...m", leaking trailing letters.
	w := ansi.StringWidth(s)
	m.widthCache.Add(s, w)
	return w
}

// WrapText 按显示宽度换行，返回换行后字符串。
//
// 替代 cellbuf.Wrap 的 rune 数切片（G-05：rune 数切片错位 CJK 宽字符）。
// 算法：按显示宽度累计，超限时换行（强制换行，不在词边界拆分，保持简单可控）。
//
// 第 4 件：底层 ansi.Hardwrap 只按宽度切分、不看字符语义，
// 会把全角 `）` 折到下一行**行首**（中文排版禁则）。折行后追加一遍
// applyCJKLineStartKinsoku 把禁则字符上提回上一行 —— 本函数是 wrapViewportContent
// 的唯一折行实现（全仓唯一调用点），故在此收口即覆盖所有转录折行。
func (m *runewidthMeasurer) WrapText(s string, limit int) string {
	if limit <= 0 || s == "" {
		return s
	}
	return applyCJKLineStartKinsoku(ansi.Hardwrap(s, limit, true))
}

// Truncate 按显示宽度截断字符串（utf8 rune 边界安全）。
//
// 对标项目记忆"diff 截断 utf8 安全"改进项。
// 不产生半个字符（rune 边界对齐）。
func (m *runewidthMeasurer) Truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if s == "" {
		return ""
	}
	return ansi.Truncate(s, maxWidth, "")
}
