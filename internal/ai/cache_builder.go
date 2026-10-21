// Package ai — cache-aware request builder
package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// PromptLayer prompt layer
// Aligned with a comparable CLI layered system prompt: sorted by stability from high to low, maximizing prompt cache hits
type PromptLayer int

const (
	// LayerCoreIdentity core identity — most stable, rarely changes
	LayerCoreIdentity PromptLayer = 0
	// LayerToolDefs tool definitions — stable, unchanged within session
	LayerToolDefs PromptLayer = 1
	// LayerProjectCtx project context — semi-stable, MagicDocs/memory etc.
	LayerProjectCtx PromptLayer = 2
	// LayerSessionState session state — dynamic, changes each turn
	LayerSessionState PromptLayer = 3
)

// String returns layer name
func (l PromptLayer) String() string {
	switch l {
	case LayerCoreIdentity:
		return "core_identity"
	case LayerToolDefs:
		return "tool_defs"
	case LayerProjectCtx:
		return "project_ctx"
	case LayerSessionState:
		return "session_state"
	default:
		return "unknown"
	}
}

// PromptComponent prompt component
// Aligned with a comparable CLI prompt cache optimization: place stable content first, maximize cache hits
type PromptComponent struct {
	Name    string
	Content string
	Stable  bool // Whether stable content (does not change with conversation)
	Layer   PromptLayer
}

// LayeredPrompt layered prompt
// Organizes system prompt into 4 layers, each managed independently, concatenated by layer order when rendering
// Aligned with a comparable CLI layered system prompt architecture
type LayeredPrompt struct {
	Layers map[PromptLayer]string
}

// NewLayeredPrompt creates layered prompt
func NewLayeredPrompt() *LayeredPrompt {
	return &LayeredPrompt{
		Layers: make(map[PromptLayer]string),
	}
}

// SetLayer sets content for specified layer
func (lp *LayeredPrompt) SetLayer(layer PromptLayer, content string) {
	lp.Layers[layer] = content
}

// Render renders complete system prompt, concatenating from low to high layer
// Lower layers (stable) first, higher layers (dynamic) after, maximizing prompt cache prefix matching
func (lp *LayeredPrompt) Render() *RenderedPrompt {
	var parts []string
	layerSizes := make(map[PromptLayer]int)
	estimator := NewTokenEstimator()

	for layer := LayerCoreIdentity; layer <= LayerSessionState; layer++ {
		content, ok := lp.Layers[layer]
		if !ok || content == "" {
			continue
		}
		parts = append(parts, content)
		layerSizes[layer] = estimator.EstimateTextTokens(content)
	}

	fullContent := strings.Join(parts, "\n\n")

	// Compute SHA256 fingerprint (for cache matching verification)
	hash := sha256.Sum256([]byte(fullContent))
	fingerprint := hex.EncodeToString(hash[:])

	return &RenderedPrompt{
		Content:     fullContent,
		Fingerprint: fingerprint,
		LayerSizes:  layerSizes,
	}
}

// RenderedPrompt fully rendered prompt (with cache fingerprint)
// Aligned with a comparable CLI rendered system prompt: Fork sub-agents inherit this object for cache sharing
type RenderedPrompt struct {
	Content     string              // Fully rendered system prompt
	Fingerprint string              // SHA256 fingerprint (for cache matching)
	LayerSizes  map[PromptLayer]int // Token estimate per layer
}

// PromptLayerBlock 表示一个已渲染的 prompt 层块，供 N3 标记 cache_control。
// N15 产出该结构（BuildSystemPromptBlocks），N3 在 buildAnthropicBody 遍历每个 block 标记 cache_control（契约 §2.1.3 L113）。
// 与 LayeredSystemBlock 的区别：PromptLayerBlock 携带 Stable 字段，供 N3 按层稳定性差异化标记 cache_control。
//
// SNAPSHOT：新增 CacheBoundary + CacheControl 字段。
// - CacheBoundary: 显式标记此层为 cache breakpoint（N15 可设置以避免推断误差）
// - CacheControl: PlaceBreakpointHybrid 注入的 cache_control 标记（N3 透传到 API）
//
// 零值（CacheBoundary=false / CacheControl=nil）不影响现有逻辑（向后兼容）。
type PromptLayerBlock struct {
	Layer         PromptLayer            // 层级（CoreIdentity/ToolDefs/ProjectCtx/SessionState）
	Text          string                 // 已渲染的层文本
	Stable        bool                   // 是否稳定层（true=cached，false=uncached）
	CacheBoundary bool                   // SNAPSHOT：显式标记此层为 cache breakpoint
	CacheControl  map[string]interface{} // SNAPSHOT：PlaceBreakpointHybrid 注入的 cache_control 标记
}

// CacheAwareRequestBuilder cache-aware request builder
// Aligned with a comparable CLI cache-aware request builder:
// Reorders system prompt component sequence, placing stable content first, maximizing prompt cache hit rate
// Principle: Anthropic API prompt cache is based on request prefix matching,
// Placing stable content first means multiple requests share the same prefix, hitting cache
type CacheAwareRequestBuilder struct{}

// NewCacheAwareRequestBuilder creates cache-aware request builder
func NewCacheAwareRequestBuilder() *CacheAwareRequestBuilder {
	return &CacheAwareRequestBuilder{}
}

// OrderForCache reorders prompt component sequence to optimize cache hits
// Stable components (Stable=true) sorted first, dynamic components after
// This way multiple requests share the same prefix (stable part), hitting prompt cache
func (b *CacheAwareRequestBuilder) OrderForCache(components []PromptComponent) []PromptComponent {
	sorted := make([]PromptComponent, len(components))
	copy(sorted, components)

	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Layer != sorted[j].Layer {
			return sorted[i].Layer < sorted[j].Layer
		}
		return sorted[i].Stable && !sorted[j].Stable
	})

	return sorted
}

// BuildSystemPrompt 从已排序的组件构建 system prompt（向后兼容入口）。
// 内部委托 BuildSystemPromptBlocks 产出分层块后 strings.Join 拼接为单个字符串。
// 同层覆盖语义：同一 PromptLayer 内，若存在名称含 "override" 的组件，
// 则该组件覆盖同层其他组件（仅保留 override 内容，不拼接）；
// 若同层无 override 组件，则同层组件按 OrderForCache 排序后拼接。
// 这与同类 CLI 的分层覆盖链一致：override > coordinator > agent > custom > default。
func (b *CacheAwareRequestBuilder) BuildSystemPrompt(components []PromptComponent) string {
	blocks := b.BuildSystemPromptBlocks(components)
	parts := make([]string, 0, len(blocks))
	for _, blk := range blocks {
		if blk.Text != "" {
			parts = append(parts, blk.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// BuildSystemPromptBlocks 从已排序的组件构建分层 prompt 块列表。
// 同层覆盖语义与 BuildSystemPrompt 一致：同一 PromptLayer 内，若存在名称含 "override" 的组件，
// 则该组件覆盖同层其他组件（仅保留 override 内容，不拼接）。
// 返回 []PromptLayerBlock 供 N3 逐层标记 cache_control（契约 §2.1.3 L113）。
// Stable 字段语义：同层 override 时取 override 组件的 Stable；无 override 时取同层所有组件 Stable 的与值
// （任一动态组件则整层视为动态，保守策略以保证 cache 前缀一致性）。
func (b *CacheAwareRequestBuilder) BuildSystemPromptBlocks(components []PromptComponent) []PromptLayerBlock {
	ordered := b.OrderForCache(components)

	// 按 Layer 分组，保留 ordered 中的相对顺序
	byLayer := make(map[PromptLayer][]PromptComponent)
	for _, c := range ordered {
		byLayer[c.Layer] = append(byLayer[c.Layer], c)
	}

	var blocks []PromptLayerBlock
	// 从低层（稳定）到高层（动态）依次处理
	for layer := LayerCoreIdentity; layer <= LayerSessionState; layer++ {
		comps := byLayer[layer]
		if len(comps) == 0 {
			continue
		}
		// 同层覆盖：查找名称含 "override" 的组件
		var overrideComp *PromptComponent
		for i := range comps {
			if strings.Contains(strings.ToLower(comps[i].Name), "override") {
				overrideComp = &comps[i]
				break
			}
		}
		var content string
		var stable bool
		if overrideComp != nil {
			// override 组件覆盖同层其他组件
			content = overrideComp.Content
			stable = overrideComp.Stable
		} else {
			// 无 override，同层组件拼接；Stable 取与值（任一动态则整层动态）
			var parts []string
			stable = true
			for _, c := range comps {
				if c.Content != "" {
					parts = append(parts, c.Content)
				}
				if !c.Stable {
					stable = false
				}
			}
			content = strings.Join(parts, "\n\n")
		}
		if content == "" {
			continue
		}
		blocks = append(blocks, PromptLayerBlock{
			Layer:  layer,
			Text:   content,
			Stable: stable,
		})
	}
	return blocks
}

// BuildLayeredPrompt builds layered prompt from component list
// Group components by layer, merge content within same layer
func (b *CacheAwareRequestBuilder) BuildLayeredPrompt(components []PromptComponent) *LayeredPrompt {
	lp := NewLayeredPrompt()

	// Group by layer
	layerContents := make(map[PromptLayer][]string)
	for _, c := range components {
		if c.Content == "" {
			continue
		}
		layerContents[c.Layer] = append(layerContents[c.Layer], c.Content)
	}

	// Merge content within same layer
	for layer, contents := range layerContents {
		lp.SetLayer(layer, strings.Join(contents, "\n\n"))
	}

	return lp
}

// BuildLayeredSystemBlocks 按层生成独立的 system block 列表（方案 G B-1 修复）。
// 每层合并为单个 block，block 顺序从低层（稳定）到高层（动态）。
// 调用方为每个 block 单独注入 cache_control，稳定层 cache 命中率提升。
// 同层覆盖语义与 BuildSystemPrompt 一致：名称含 "override" 的组件覆盖同层。
// 返回值：每层一个 {layer, content} 元组，调用方按需构造 API block。
func (b *CacheAwareRequestBuilder) BuildLayeredSystemBlocks(components []PromptComponent) []LayeredSystemBlock {
	ordered := b.OrderForCache(components)
	byLayer := make(map[PromptLayer][]PromptComponent)
	for _, c := range ordered {
		byLayer[c.Layer] = append(byLayer[c.Layer], c)
	}

	var blocks []LayeredSystemBlock
	for layer := LayerCoreIdentity; layer <= LayerSessionState; layer++ {
		comps := byLayer[layer]
		if len(comps) == 0 {
			continue
		}
		// 同层覆盖：查找名称含 "override" 的组件
		var overrideComp *PromptComponent
		for i := range comps {
			if strings.Contains(strings.ToLower(comps[i].Name), "override") {
				overrideComp = &comps[i]
				break
			}
		}
		var content string
		if overrideComp != nil {
			content = overrideComp.Content
		} else {
			var parts []string
			for _, c := range comps {
				if c.Content != "" {
					parts = append(parts, c.Content)
				}
			}
			content = strings.Join(parts, "\n\n")
		}
		if content == "" {
			continue
		}
		blocks = append(blocks, LayeredSystemBlock{Layer: layer, Content: content})
	}
	return blocks
}

// LayeredSystemBlock 分层 system prompt 的单个 block（方案 G）。
// 调用方按 Layer 顺序构造 Anthropic API system 数组，每个 block 独立 cache_control。
type LayeredSystemBlock struct {
	Layer   PromptLayer
	Content string
}

// CacheInfo cache info returned by API
// Aligned with a comparable CLI cache_deleted_input_tokens tracking
type CacheInfo struct {
	// CacheCreationTokens newly created cache token count
	CacheCreationTokens int
	// CacheReadTokens cache read token count
	CacheReadTokens int
	// CacheDeletedTokens deleted cache token count (Anthropic specific)
	CacheDeletedTokens int
	// TotalCachedTokens total cached token count
	TotalCachedTokens int
	// LastCacheTime is the timestamp when cache was last created/read.
	// N11 修复 D3（G5 恒降级）：CacheAwareMicroCompact 用此字段判断 cache 是否
	// 仍在 TTL 有效期内（now - LastCacheTime < CacheTTL），避免始终传 time.Time{}
	// 导致 isCacheActive 恒 false → 降级为普通 MicroCompact。
	// 由 API 响应处理路径在收到 cache token 时设为 time.Now()。
	LastCacheTime time.Time
}

// HasCacheInfo whether there is valid cache info
func (ci *CacheInfo) HasCacheInfo() bool {
	return ci != nil && (ci.CacheCreationTokens > 0 || ci.CacheReadTokens > 0 || ci.CacheDeletedTokens > 0)
}

// CacheDeletionRatio cache deletion ratio
// Deletion ratio = cacheDeleted / totalCached
// High deletion ratio means cache is expiring, should compact as soon as possible
func (ci *CacheInfo) CacheDeletionRatio() float64 {
	if ci == nil || ci.TotalCachedTokens == 0 {
		return 0
	}
	return float64(ci.CacheDeletedTokens) / float64(ci.TotalCachedTokens)
}

// CacheHitRate cache hit rate
// Hit rate = cacheRead / (cacheCreation + cacheRead)
func (ci *CacheInfo) CacheHitRate() float64 {
	if ci == nil {
		return 0
	}
	total := ci.CacheCreationTokens + ci.CacheReadTokens
	if total == 0 {
		return 0
	}
	return float64(ci.CacheReadTokens) / float64(total)
}

// ShouldCompactBasedOnCache determines if compaction is needed based on cache status
// Decision rules:
// 1. cacheDeletionRatio > 0.5 → compact immediately (cache is expiring)
// 2. cacheHitRate < 0.3 and context usage > 70% → compact (cache efficiency low)
func (ci *CacheInfo) ShouldCompactBasedOnCache(usagePercent float64) bool {
	if ci == nil || !ci.HasCacheInfo() {
		return false
	}

	// Rule 1: cache deletion ratio too high
	if ci.CacheDeletionRatio() > 0.5 {
		return true
	}

	// Rule 2: low cache hit rate + high context usage
	if ci.CacheHitRate() < 0.3 && usagePercent > 0.70 {
		return true
	}

	return false
}
