package tui

import (
	"container/list"
	"sync"
)

// lruRenderCache 是 RenderCache 的 LRU 实现。
//
// 上限 maxEntries，超限淘汰最久未访问。
// 对标同类 CLI ink.tsx resetPools 的定期重置机制。
//
// 关键约束：
// - LRU 上限 500 条（defaultCacheEntries），超限淘汰最久未访问。
// - cacheKey 用 SHA-256 hash（与 N4 一致，碰撞概率 2^128 可忽略）。
// - sync.Mutex 保护并发访问。
// - （M10-02）：新增 maxBytes 字节上限（默认 64MB），双约束淘汰
// （条目数 OR 字节数超限均触发），对标 Redis maxmemory + allkeys-lru。
//
// 流式追加缓存策略（回核评估）：
// 流式文本（CurrentText）每次 tick 由 renderStreamContent 实时拼接，不经 RenderCache；
// 仅 finalize 后的不可变消息经 RenderCache 缓存，其内容 hash 稳定。
// 因此无需"临时 cacheKey 后缀 + 完成后 Invalidate"机制——流式中间态不产生缓存条目，
// 最终消息的 hash 与任何中间态不同（因内容不同），不会命中旧缓存。
// 若未来流式消息需缓存，应引入流式专用 key 前缀（如 "stream:" + sessionMsgID），
// 完成后 Invalidate 该前缀并缓存最终版本。
type lruRenderCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int64                    // （M10-02）：字节上限，0 表示不限
	entries    *list.List               // LRU 链表（前端 = 最近使用）
	index      map[string]*list.Element // cacheKey → 链表节点
	stats      CacheStats
}

// cacheEntry 缓存条目。
type cacheEntry struct {
	key      string
	rendered string
	height   int
	bytes    int // 近似内存占用
}

// NewLRURenderCache 构造 LRU 渲染缓存。
//
// maxEntries 建议 500（覆盖长会话可见范围 + 历史回滚）。
// maxBytes 建议 64MB（M10-02，对标 Redis maxmemory 生产实践 50-100MB）；
// 传 0 表示不启用字节上限（仅条目约束）。
func NewLRURenderCache(maxEntries int, maxBytes int64) RenderCache {
	if maxEntries <= 0 {
		maxEntries = defaultCacheEntries
	}
	return &lruRenderCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entries:    list.New(),
		index:      make(map[string]*list.Element),
	}
}

// GetOrRender 获取或渲染消息。
//
// 命中：移到链表前端，返回缓存结果。
// 未命中：调 renderFn，写入缓存，超限淘汰链表尾部。
func (c *lruRenderCache) GetOrRender(cacheKey string, renderFn func() (string, int, error)) (string, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 命中：移到链表前端。
	if elem, ok := c.index[cacheKey]; ok {
		c.entries.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		c.stats.Hits++
		c.updateHitRate()
		return entry.rendered, entry.height, nil
	}

	// 未命中：调 renderFn。
	c.stats.Misses++
	rendered, height, err := renderFn()
	if err != nil {
		c.updateHitRate()
		return "", 0, err
	}

	// 写入缓存。
	entry := &cacheEntry{
		key:      cacheKey,
		rendered: rendered,
		height:   height,
		bytes:    len(rendered),
	}
	elem := c.entries.PushFront(entry)
	c.index[cacheKey] = elem
	c.stats.MemoryBytes += int64(entry.bytes)

	// 淘汰：超限时移除链表尾部。
	// （M10-02）：双约束淘汰——条目数超限 OR 字节数超限均触发。
	// 字节约束循环保证 `MemoryBytes <= maxBytes`（maxBytes=0 时跳过），
	// `entries.Len() > 1` 防止单条超 maxBytes 时无限淘汰空缓存。
	for c.entries.Len() > c.maxEntries {
		c.evictBack()
	}
	if c.maxBytes > 0 {
		for c.stats.MemoryBytes > c.maxBytes && c.entries.Len() > 1 {
			c.evictBack()
		}
	}
	c.stats.Entries = c.entries.Len()
	c.updateHitRate()
	return rendered, height, nil
}

// evictBack 淘汰链表尾部条目（调用方持锁）。
// （M10-02）：抽取共用淘汰逻辑，供条目约束 + 字节约束复用。
func (c *lruRenderCache) evictBack() {
	back := c.entries.Back()
	if back == nil {
		return
	}
	oldEntry := back.Value.(*cacheEntry)
	c.entries.Remove(back)
	delete(c.index, oldEntry.key)
	c.stats.MemoryBytes -= int64(oldEntry.bytes)
}

// Invalidate 使指定 cacheKey 失效。
func (c *lruRenderCache) Invalidate(cacheKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.index[cacheKey]; ok {
		entry := elem.Value.(*cacheEntry)
		c.entries.Remove(elem)
		delete(c.index, cacheKey)
		c.stats.MemoryBytes -= int64(entry.bytes)
		c.stats.Entries = c.entries.Len()
	}
}

// InvalidateAll 清空全部缓存。
func (c *lruRenderCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.Init()
	c.index = make(map[string]*list.Element)
	c.stats = CacheStats{}
}

// Stats 返回缓存统计。
func (c *lruRenderCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// updateHitRate 重算命中率（调用方持锁）。
func (c *lruRenderCache) updateHitRate() {
	total := c.stats.Hits + c.stats.Misses
	if total > 0 {
		c.stats.HitRate = float64(c.stats.Hits) / float64(total)
	} else {
		c.stats.HitRate = 0
	}
	c.stats.Entries = c.entries.Len()
}
