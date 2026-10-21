package ai

import "strings"

// DefaultMinCacheableTokens 是 Anthropic Prompt Caching 的最小缓存前缀 token 阈值。
//
// 低于此阈值的 cache_control 标记不会命中缓存，因此放置 breakpoint 前需校验前缀 token 数。
//
// 官方源：https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching @2026-07-20
const DefaultMinCacheableTokens = 1024

// DefaultMaxBreakpointsPerRequest 是 Anthropic API 单请求允许的最大 breakpoint 数。
//
// 超过此上限的 breakpoint 会被 API 拒绝，因此放置前需截断。
//
// 官方源：https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching @2026-07-20
const DefaultMaxBreakpointsPerRequest = 4

// CacheBreakpointConfig 是 cache breakpoint 配置（控制 cache_control 标记放置策略）。
//
// SNAPSHOT：新增 MinCacheableTokens 字段（最小缓存前缀 token 阈值，默认 1024）。
// MaxBreakpointsPerRequest 默认值由 1 变更为 4（对齐 Anthropic API 上限）。
type CacheBreakpointConfig struct {
	// Enabled whether cache breakpoints are enabled
	Enabled bool
	// MaxBreakpointsPerRequest max breakpoints per request (default 4, )
	MaxBreakpointsPerRequest int
	// CacheScope cache scope ("global" or "org")
	CacheScope string
	// MinCacheableTokens 是最小缓存前缀 token 阈值（SNAPSHOT）。
	// 零值时使用 DefaultMinCacheableTokens=1024。
	// REF-外部-Anthropic @2026-07-20 https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
	MinCacheableTokens int
}

// CacheBreakpointPlacement 负责 cache_control breakpoint 放置。
//
// 新增 tokenSource / probe 私有字段（构造函数注入，不暴露 setter）。
// 构造后只读，多 goroutine 调用 PlaceBreakpointHybrid 安全（无共享可变状态）。
type CacheBreakpointPlacement struct {
	// config breakpoint configuration
	config CacheBreakpointConfig
	// tokenSource 用于精确 token 计数；nil 时 PlaceBreakpointHybrid 跳过 token 阈值校验
	tokenSource TokenSource
	// probe 用于查询模型缓存能力；nil 时使用全局单例 getGlobalCapabilityProbe()
	probe CacheCapabilityProbe
}

// NewCacheBreakpointPlacement 创建 cache breakpoint 放置器（向后兼容构造函数）。
//
// MaxBreakpointsPerRequest 默认值由 1 变更为 4（对齐 Anthropic API 上限）。
// 旧调用方显式传 1 时保留；不传或传 0 时用默认值 4。
// 不注入 TokenSource / CapabilityProbe（nil 时 PlaceBreakpointHybrid 降级原逻辑）。
func NewCacheBreakpointPlacement(config CacheBreakpointConfig) *CacheBreakpointPlacement {
	// Set defaults
	if config.MaxBreakpointsPerRequest <= 0 {
		config.MaxBreakpointsPerRequest = DefaultMaxBreakpointsPerRequest
	}
	if config.CacheScope == "" {
		config.CacheScope = "org"
	}
	if config.MinCacheableTokens <= 0 {
		config.MinCacheableTokens = DefaultMinCacheableTokens
	}
	return &CacheBreakpointPlacement{
		config: config,
	}
}

// NewCacheBreakpointPlacementWithDeps 创建带依赖注入的 cache breakpoint 放置器。
//
// 通过构造函数注入 TokenSource + CacheCapabilityProbe，不暴露 setter，保证构造后只读，
// 多 goroutine 调用 PlaceBreakpointHybrid 安全。
//
// 参数：
// - config: breakpoint 配置（MinCacheableTokens / MaxBreakpointsPerRequest / CacheScope）
// - tokenSource: TokenSource 实例（nil 时降级原逻辑，不校验 token 阈值）
// - probe: CacheCapabilityProbe 实例（nil 时使用全局单例）
//
// 返回带依赖注入的 CacheBreakpointPlacement 实例。
func NewCacheBreakpointPlacementWithDeps(
	config CacheBreakpointConfig,
	tokenSource TokenSource,
	probe CacheCapabilityProbe,
) *CacheBreakpointPlacement {
	if config.MaxBreakpointsPerRequest <= 0 {
		config.MaxBreakpointsPerRequest = DefaultMaxBreakpointsPerRequest
	}
	if config.CacheScope == "" {
		config.CacheScope = "org"
	}
	if config.MinCacheableTokens <= 0 {
		config.MinCacheableTokens = DefaultMinCacheableTokens
	}
	return &CacheBreakpointPlacement{
		config:      config,
		tokenSource: tokenSource,
		probe:       probe,
	}
}

// PlaceBreakpoint places cache breakpoints in the message list.
//
// FACT-AI-002 fix: cache_control is placed at the content block level (inside
// the last content block of the message), NOT at the message level. This
// matches the Anthropic API contract: {type:"ephemeral", ttl:"1h", scope:"global"}.
//
// FACT-AI-004 fix: the cache_control map now includes a "ttl" field (default
// "1h") so the 1h TTL gating mechanism is represented in the breakpoint output.
//
// Places at most config.MaxBreakpointsPerRequest breakpoints per request
// (default 1). When MaxBreakpointsPerRequest > 1, breakpoints are distributed
// from the end of the message list.
func (p *CacheBreakpointPlacement) PlaceBreakpoint(messages []map[string]interface{}) []map[string]interface{} {
	if !p.config.Enabled || len(messages) == 0 {
		return messages
	}

	result := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		copied := make(map[string]interface{}, len(msg))
		for k, v := range msg {
			copied[k] = v
		}
		result[i] = copied
	}

	maxBP := p.config.MaxBreakpointsPerRequest
	if maxBP <= 0 {
		maxBP = 1
	}
	scope := p.config.CacheScope

	placed := 0
	for i := len(result) - 1; i >= 0 && placed < maxBP; i-- {
		if p.messageHasBlockCacheControl(result[i]) {
			continue
		}
		p.injectBlockCacheControl(result[i], scope)
		placed++
	}

	return result
}

// messageHasBlockCacheControl reports whether the message already has a
// cache_control marker on any of its content blocks. This prevents double
// placement when the caller pre-populated a breakpoint.
func (p *CacheBreakpointPlacement) messageHasBlockCacheControl(msg map[string]interface{}) bool {
	switch content := msg["content"].(type) {
	case []map[string]interface{}:
		for _, b := range content {
			if _, ok := b["cache_control"]; ok {
				return true
			}
		}
	case []interface{}:
		for _, b := range content {
			if bm, ok := b.(map[string]interface{}); ok {
				if _, ok := bm["cache_control"]; ok {
					return true
				}
			}
		}
	}
	return false
}

// injectBlockCacheControl ensures the message's content is in content-block
// form and adds a cache_control marker ({type, ttl, scope}) to the last block.
// If the content is a plain string it is wrapped into a single text block
// before the marker is attached.
func (p *CacheBreakpointPlacement) injectBlockCacheControl(msg map[string]interface{}, scope string) {
	cc := map[string]interface{}{
		"type":  "ephemeral",
		"ttl":   "1h",
		"scope": scope,
	}

	switch content := msg["content"].(type) {
	case string:
		msg["content"] = []map[string]interface{}{
			{"type": "text", "text": content, "cache_control": cc},
		}
	case []map[string]interface{}:
		blocks := make([]map[string]interface{}, len(content))
		for j, b := range content {
			blocks[j] = make(map[string]interface{}, len(b))
			for k, v := range b {
				blocks[j][k] = v
			}
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = cc
		}
		msg["content"] = blocks
	case []interface{}:
		blocks := make([]map[string]interface{}, len(content))
		for j, b := range content {
			if bm, ok := b.(map[string]interface{}); ok {
				blocks[j] = make(map[string]interface{}, len(bm))
				for k, v := range bm {
					blocks[j][k] = v
				}
			} else {
				blocks[j] = map[string]interface{}{}
			}
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = cc
		}
		msg["content"] = blocks
	}
}

// SplitSystemPrompt splits system prompt into static and dynamic parts by boundary
// Static part (content before boundary) does not change with conversation, suitable for long-term caching
// Dynamic part (content after boundary) may change each turn, lower caching value
// If boundary not found, all content is treated as static part
func (p *CacheBreakpointPlacement) SplitSystemPrompt(systemPrompt string, boundary string) (static string, dynamic string) {
	if boundary == "" {
		return systemPrompt, ""
	}

	idx := strings.Index(systemPrompt, boundary)
	if idx < 0 {
		return systemPrompt, ""
	}

	static = strings.TrimRight(systemPrompt[:idx], "\n")
	dynamic = strings.TrimLeft(systemPrompt[idx:], "\n")

	return static, dynamic
}

// AssignCacheScope assigns cache scope based on content type and provider support
// Returns "global" for static content when provider supports global caching, otherwise "org"
// Global scope cache is shared among all users with the same API key, higher hit rate
func (p *CacheBreakpointPlacement) AssignCacheScope(isStatic bool, providerSupportsGlobal bool) string {
	if isStatic && providerSupportsGlobal {
		return "global"
	}
	return "org"
}

// PlaceBreakpointHybrid 在分层 prompt block 上放置 cache_control 断点（混合策略，）。
//
// 决策顺序（5 策略顺序兜底）：
// 1. 识别 CacheBoundary=true 显式标记（优先级最高，用户意图优先）
// 2. 按层稳定性（Stable=true）+ token 阈值推断（前缀累计 token ≥ MinCacheableTokens）
// 3. 受 MaxBreakpointsPerRequest 上限保护（≤4，超过截断）
// 4. CachingStyle != AnthropicEphemeral 时跳过 cache_control 注入
// （OpenAIPrefixMatch / PromptCacheKey 走各自路径）
// 5. tokenSource nil 时跳过 token 阈值校验，仅按显式标记 + Stable 推断
//
// 返回新的 blocks 切片（不修改输入），每个 block 新增 CacheControl 字段。
//
// 官方源：https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching @2026-07-20
func (p *CacheBreakpointPlacement) PlaceBreakpointHybrid(
	blocks []PromptLayerBlock,
	model string,
) []PromptLayerBlock {
	if !p.config.Enabled || len(blocks) == 0 {
		return blocks
	}

	// 跨模型适配：CachingStyle != AnthropicEphemeral 时跳过 cache_control 注入
	if !p.shouldInjectCacheControl(model) {
		return blocks
	}

	// 复制 blocks 切片（不修改输入）
	result := make([]PromptLayerBlock, len(blocks))
	copy(result, blocks)

	maxBP := p.config.MaxBreakpointsPerRequest
	if maxBP <= 0 {
		maxBP = DefaultMaxBreakpointsPerRequest
	}
	if maxBP > DefaultMaxBreakpointsPerRequest {
		// 截断到 Anthropic API 上限（优化 2 吸纳）
		maxBP = DefaultMaxBreakpointsPerRequest
	}

	placed := 0
	// 从低层（稳定）到高层（动态）遍历，按显式标记 + token 阈值放置 breakpoint
	for i := range result {
		if placed >= maxBP {
			break
		}

		// 策略 1：显式 CacheBoundary=true 标记（优先级最高，用户意图优先）
		if result[i].CacheBoundary {
			// 避免重复放置（若该层已有 CacheControl）
			if result[i].CacheControl == nil {
				result[i].CacheControl = p.buildCacheControlMap()
				placed++
			}
			continue
		}

		// 策略 2：Stable=true + token 阈值推断
		if !result[i].Stable {
			continue
		}

		// 避免重复放置
		if result[i].CacheControl != nil {
			continue
		}

		// token 阈值校验（tokenSource nil 时跳过，仅按 Stable 推断）
		if p.tokenSource != nil {
			prefixTokens := p.computePrefixTokens(result[:i+1], model)
			if prefixTokens < p.config.MinCacheableTokens {
				// 前缀 token 不足，跳过此层（避免放置无效 breakpoint）
				continue
			}
		}

		result[i].CacheControl = p.buildCacheControlMap()
		placed++
	}

	return result
}

// shouldInjectCacheControl 判断模型是否应注入 cache_control 标记（优化 5 吸纳）。
//
// 跨模型适配：
// - CachingAnthropicEphemeral: 注入 cache_control（claude）
// - CachingOpenAIPrefixMatch: 跳过（gpt 走前缀匹配，cached_tokens 自动计费）
// - CachingPromptCacheKey: 跳过（deepseek 走 prompt_cache_key 字段）
// - CachingNone: 跳过
//
// probe nil 时使用全局单例 getGlobalCapabilityProbe()。
func (p *CacheBreakpointPlacement) shouldInjectCacheControl(model string) bool {
	probe := p.probe
	if probe == nil {
		probe = getGlobalCapabilityProbe()
	}
	cap := probe.Probe(model)
	return cap.CachingStyle == CachingAnthropicEphemeral
}

// computePrefixTokens 计算前缀 blocks 的累计 token 数。
//
// 通过 TokenSource.ContextCount 计算（已落地接口）。
// 调用频次：4 层 → 4 次调用（优化 4 吸纳，调用频次上限 ≤4）。
// PromptLayerBlock → MessageEntry 转换：role="system"，content=blk.Text（优化 1 吸纳）。
// 空 Text 跳过（不计入 messages）。
func (p *CacheBreakpointPlacement) computePrefixTokens(blocks []PromptLayerBlock, model string) int {
	if p.tokenSource == nil {
		return 0
	}
	// PromptLayerBlock → MessageEntry 转换
	messages := make([]MessageEntry, 0, len(blocks))
	for _, blk := range blocks {
		if blk.Text != "" {
			messages = append(messages, MessageEntry{Role: "system", Content: blk.Text})
		}
	}
	return p.tokenSource.ContextCount(messages, model)
}

// buildCacheControlMap 构建 cache_control map（{type, ttl, scope} 格式，）。
//
// 与现有 injectBlockCacheControl 一致的格式（FACT-AI-002 修复）。
// ttl 默认 "1h"（claude 支持 1h TTL，cache_capability.go 已配置 Supports1hTTL=true）。
func (p *CacheBreakpointPlacement) buildCacheControlMap() map[string]interface{} {
	return map[string]interface{}{
		"type":  "ephemeral",
		"ttl":   "1h",
		"scope": p.config.CacheScope,
	}
}
