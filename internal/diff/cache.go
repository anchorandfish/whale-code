package diff

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/hashicorp/golang-lru/v2"
)

// cacheEntry 缓存条目，同时存储渲染结果和行级 diff 结果
// CountChanges 可从 lineDiffs 直接统计，无需重新计算或解析渲染字符串
type cacheEntry struct {
	rendered  string
	lineDiffs []LineDiff
}

// RenderCache 渲染缓存，对标同类 CLI RENDER_CACHE（上限 4）
// 按 content hash + config hash 缓存渲染结果，避免重复计算
// 实现：用 hashicorp/golang-lru 的 lru.Cache（线程安全 + 内置 LRU 淘汰）
// 不用 sync.Map——sync.Map 无内置 LRU 淘汰逻辑，需自实现顺序链表+mutex，复杂且失去无锁优势
// 上限：默认 16（同类 CLI 上限 4 但 whale-code 场景含多文件 turn diff，更大）
type RenderCache struct {
	cache *lru.Cache[uint64, cacheEntry]
}

// NewRenderCache 创建新的渲染缓存
// max<=0 时使用默认值 16
func NewRenderCache(max int) *RenderCache {
	if max <= 0 {
		max = 16
	}
	cache, _ := lru.New[uint64, cacheEntry](max)
	return &RenderCache{cache: cache}
}

// Get 从缓存获取渲染结果
func (c *RenderCache) Get(key uint64) (cacheEntry, bool) {
	return c.cache.Get(key)
}

// Set 将渲染结果存入缓存
func (c *RenderCache) Set(key uint64, entry cacheEntry) {
	c.cache.Add(key, entry)
}

// globalCache 包级默认渲染缓存
// hashicorp/golang-lru 的 Cache 是并发安全的（内部加锁），Render 和 CountChanges
// 并发调用同一 FileChange 时可能出现 double-compute（两者都未命中、各自计算并写入），
// 但功能上无害（后写入者覆盖前者，结果一致）
var globalCache = NewRenderCache(16)

// contentHashIndex 内容哈希到缓存键的辅助索引
// CountChanges 无 RenderConfig 参数，无法构造完整 cacheKey，
// 通过此索引从 FileChange.Hash() 找到对应的 cacheKey，再从 globalCache 取 cacheEntry.lineDiffs
// 同一 FileChange 在不同 RenderConfig 下会产生不同 cacheKey，
// 索引只保留最近一次的映射（后写入者覆盖前者），lineDiffs 在所有 config 下相同，无功能影响
var contentHashIndex, _ = lru.New[uint64, uint64](32)

// cacheKey 计算缓存键，顺序写入 fnv 哈希（非 XOR，避免可交换性碰撞）
// 包含 FileChange 内容 + RenderConfig 关键参数，确保不同配置产生不同 key
func cacheKey(change FileChange, cfg RenderConfig) uint64 {
	h := fnv.New64()
	h.Write([]byte(change.Path))
	h.Write([]byte{0}) // 分隔符，防止 Path/OldContent 边界碰撞
	h.Write([]byte(change.OldContent))
	h.Write([]byte{0}) // 分隔符，防止 OldContent/NewContent 边界碰撞
	h.Write([]byte(change.NewContent))
	h.Write([]byte{0}) // 分隔符，防止 NewContent/Config 边界碰撞
	binary.Write(h, binary.LittleEndian, uint64(cfg.MaxLines))
	binary.Write(h, binary.LittleEndian, uint64(cfg.MaxFileSize))
	binary.Write(h, binary.LittleEndian, uint64(cfg.WordThreshold*1000)) // 避免浮点精度问题
	binary.Write(h, binary.LittleEndian, uint64(cfg.ContextLines))

	// 样式维度：Plain（纯文本 / 彩色）与 ColorBlind 不影响 lineDiffs，
	// 但**渲染结果字符串不同**，必须进 key。
	// 漏写的后果很隐蔽：两种渲染互相命中对方的缓存条目 ⇒ 第一次渲染是对的、
	// 第二次开始是错的，只在缓存命中时复现。
	// 由 TestCacheKeyDistinguishesStyleConfig 钉住。
	var styleBits uint64
	if cfg.Plain {
		styleBits |= 1
	}
	if cfg.ColorBlind {
		styleBits |= 2
	}
	binary.Write(h, binary.LittleEndian, styleBits)
	return h.Sum64()
}
