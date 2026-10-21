// Package context — cache-aware compaction
package context

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/session"
)

// CacheAwareCompactor cache-aware compactor
// modeled after a comparable CLI CACHED_MICROCOMPACT: uses API-returned cache info to optimize compaction timing
// delays boundary messages until after API response, maximizing cache hits
type CacheAwareCompactor struct {
	estimator    *ai.TokenEstimator
	inner        *Compactor
	breakHandler *CacheBreakHandler // 方案 D：缓存断裂时降级为普通压缩（M4）
}

// NewCacheAwareCompactor creates cache-aware compactor
func NewCacheAwareCompactor(estimator *ai.TokenEstimator) *CacheAwareCompactor {
	return &CacheAwareCompactor{
		estimator: estimator,
		inner:     NewCompactor(estimator),
	}
}

// SetBreakHandler 注入 CacheBreakHandler（方案 D）。
// 注入后 CompactWithCacheInfoInternal 入口会检查 IsCacheBroken，
// 断裂时降级为 inner.Compact（不对齐 cache boundary，保留更多近期消息）。
// 为 nil 时不检查（向后兼容）。
func (c *CacheAwareCompactor) SetBreakHandler(h *CacheBreakHandler) {
	c.breakHandler = h
}

// Compact executes cache-aware compaction
// when cacheInfo is available, uses cache boundary alignment to preserve cacheable prefix messages
// and only compacts messages after the cache boundary, maximizing cache hit rate
func (c *CacheAwareCompactor) Compact(messages []session.Message, cfg CompactionConfig) *CompactResult {
	return c.CompactWithCacheInfoInternal(messages, cfg, nil, 0)
}

// CompactWithCacheInfoInternal executes cache-aware compaction using cache boundary info
// if cacheInfo indicates a cache boundary, preserves messages before the boundary to maintain cache hits
//
// 候选 B：缓存恢复冷却机制。
// 入口先调用 TryRecoverCache：冷却窗口已过则清空断裂历史，重新走 cache boundary 对齐路径。
// 仍在冷却窗口内则降级为 inner.Compact（保留原方案 D 行为）。
func (c *CacheAwareCompactor) CompactWithCacheInfoInternal(
	messages []session.Message,
	cfg CompactionConfig,
	cacheInfo *ai.CacheInfo,
	usagePercent float64,
) *CompactResult {
	if cfg.Trigger == "" {
		cfg.Trigger = CompactionTriggerAuto
	}
	cfg.PreserveRecentMessages = cfg.EffectivePreserveCount()
	// User-intent preservation has a strict ordering and token budget. Let the
	// shared heuristic compactor own that behavior instead of reintroducing a
	// cache-specific "recent five" policy.
	if cfg.Strategy == config.CompactPreserveUser {
		return c.inner.Compact(messages, cfg)
	}
	// 候选 B：缓存恢复冷却 + 主动重建。
	// 入口先尝试恢复（冷却窗口已过则清空断裂历史）。
	// breakHandler 为 nil 时跳过检查（向后兼容）。
	if c.breakHandler != nil {
		if c.breakHandler.TryRecoverCache() {
			// 缓存刚恢复，重新对齐 cache boundary（下次 API 请求重建 cache prefix）
			// 此处不强制注入 cache_control（由调用方在构建请求时通过 InjectCacheControlMarkers 完成）
			// 落到下方正常 cache-aware 路径
		} else if c.breakHandler.IsCacheBroken() {
			// 缓存仍断裂（在冷却窗口内），降级为普通压缩（保留原方案 D 行为）
			return c.inner.Compact(messages, cfg)
		}
	}

	if cacheInfo == nil || !cacheInfo.HasCacheInfo() {
		return c.inner.Compact(messages, cfg)
	}

	if len(messages) <= cfg.PreserveRecentMessages {
		return &CompactResult{
			CompactedMessages: messages,
			RemovedCount:      0,
			NoOpReason:        CompactionNoOpInsufficientMessages,
			EligibleCount:     len(messages),
			PreserveCount:     cfg.EffectivePreserveCount(),
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
		}
	}

	cacheBoundary := c.findCacheBoundary(messages)
	preserved := messages[len(messages)-cfg.PreserveRecentMessages:]

	if cacheBoundary > 0 && cacheBoundary < len(messages)-cfg.PreserveRecentMessages {
		cachePrefix := messages[:cacheBoundary]
		compactable := messages[cacheBoundary : len(messages)-cfg.PreserveRecentMessages]

		if len(compactable) == 0 {
			return &CompactResult{
				CompactedMessages: messages,
				RemovedCount:      0,
				NoOpReason:        CompactionNoOpNoCompressibleData,
				PreMessageCount:   len(messages),
				PostMessageCount:  len(messages),
			}
		}

		summary := c.inner.generateSummary(compactable)
		summaryMsg := session.Message{
			Role: session.RoleSystem,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: summary},
			},
		}

		result := make([]session.Message, 0, len(cachePrefix)+1+len(preserved))
		result = append(result, cachePrefix...)
		result = append(result, summaryMsg)
		result = append(result, preserved...)
		preTokens := estimateSessionMessagesTokens(c.estimator, messages)
		postTokens := estimateSessionMessagesTokens(c.estimator, result)
		SetCompactBoundaryMetadata(&result[len(cachePrefix)], CompactMetadata{
			Trigger:            string(cfg.Trigger),
			PreTokens:          preTokens,
			PostTokens:         postTokens,
			MessagesSummarized: len(compactable),
		})

		return &CompactResult{
			CompactedMessages: result,
			RemovedCount:      len(compactable),
			Summary:           summary,
			Changed:           true,
			PreTokens:         preTokens,
			PostTokens:        postTokens,
			EligibleCount:     len(messages),
			PreserveCount:     cfg.EffectivePreserveCount(),
			PreMessageCount:   len(messages),
			PostMessageCount:  len(result),
		}
	}

	return c.inner.Compact(messages, cfg)
}

// findCacheBoundary finds the cache boundary index in the message list.
//
// 升级为 5 策略顺序兜底（保持向后兼容）：
// 1. 显式 cache_control metadata 标记（msg.Metadata["cache_control"]）
// 2. RoleSystem 消息（compaction summary）作为自然 cache 边界
// 3. system prompt 末尾（若消息含 prompt_layer=core_identity 标记）
// 4. tool defs 末尾（若消息含 prompt_layer=tool_defs 标记）
// 5. 首条 RoleUser 消息兜底（fallback）
//
// 策略 3/4 通过 msg.Metadata["prompt_layer"] 标记识别（由 N15 在构建消息时设置，
// 未设置时回退到策略 1/2/5，向后兼容）。
//
// P0-7 (FACT-AI-002): previously only looked for RoleSystem messages, returning 0
// when no system message existed.
//
// Returns the index AFTER the last cache-boundary message (exclusive).
func (c *CacheAwareCompactor) findCacheBoundary(messages []session.Message) int {
	boundary := 0
	// Strategy 1 & 2: explicit cache_control markers and system messages
	for i, msg := range messages {
		if _, hasCC := msg.Metadata["cache_control"]; hasCC {
			boundary = i + 1
		}
		if msg.Role == session.RoleSystem {
			boundary = i + 1
		}
	}
	// Strategy 3 & 4: system prompt 末尾 / tool defs 末尾
	// 通过 Metadata["prompt_layer"] 标记识别，由 N15 在构建消息时设置
	// 修复 B2-P1-1：原实现无条件覆盖策略 1&2 的 boundary，导致 cache prefix 被截短。
	// 现仅当 i+1 > boundary 时更新，保留更靠后的策略 1&2 边界。
	for i, msg := range messages {
		if layer, ok := msg.Metadata["prompt_layer"]; ok {
			if layerStr, ok := layer.(string); ok {
				if layerStr == "core_identity" || layerStr == "tool_defs" {
					if i+1 > boundary {
						boundary = i + 1
					}
				}
			}
		}
	}
	// Strategy 5: fallback — first user message is a natural cache breakpoint
	if boundary == 0 {
		for i, msg := range messages {
			if msg.Role == session.RoleUser {
				boundary = i + 1
				break
			}
		}
	}
	return boundary
}

// ShouldCompactBasedOnCache determines if compaction is needed based on cache state
// when cacheDeletedInputTokens is high, it indicates cache is being invalidated, should compact ASAP
// modeled after a comparable CLI cache-aware compaction decision
func (c *CacheAwareCompactor) ShouldCompactBasedOnCache(cacheInfo *ai.CacheInfo, usagePercent float64) bool {
	if cacheInfo == nil || !cacheInfo.HasCacheInfo() {
		return false
	}

	// rule 1: cache deletion rate exceeds 50%, compact immediately
	if cacheInfo.CacheDeletionRatio() > 0.5 {
		return true
	}

	// rule 2: low cache hit rate + high context usage
	if cacheInfo.CacheHitRate() < 0.3 && usagePercent > 0.70 {
		return true
	}

	return false
}

// CompactWithCacheInfo is a compaction decision with cache info
// if cache info indicates compaction is needed, execute cache-aware compaction; otherwise skip
// returns compaction result and whether compaction was executed
func (c *CacheAwareCompactor) CompactWithCacheInfo(
	messages []session.Message,
	cfg CompactionConfig,
	cacheInfo *ai.CacheInfo,
	usagePercent float64,
) (*CompactResult, bool) {
	if !c.ShouldCompactBasedOnCache(cacheInfo, usagePercent) {
		return &CompactResult{
			CompactedMessages: messages,
			RemovedCount:      0,
		}, false
	}

	result := c.CompactWithCacheInfoInternal(messages, cfg, cacheInfo, usagePercent)
	return result, true
}

// EstimateCacheSavings estimates token savings from cache optimization
// considers cache prefix tokens (previously cached) and cache read tokens (retrieved from cache)
// savings = cache_read_tokens * cache_hit_rate, capped at total estimated tokens
func (c *CacheAwareCompactor) EstimateCacheSavings(
	messages []session.Message,
	cacheInfo *ai.CacheInfo,
) int {
	if cacheInfo == nil || !cacheInfo.HasCacheInfo() {
		return 0
	}

	totalTokens := 0
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if block.Text != "" {
				totalTokens += c.estimator.EstimateTextTokens(block.Text)
			}
			if block.Output != "" {
				totalTokens += c.estimator.EstimateTextTokens(block.Output)
			}
		}
	}

	// Savings proportional to cache read tokens (tokens that were served from cache)
	// and cache hit rate, capped at total token count
	hitRate := cacheInfo.CacheHitRate()
	cacheReadTokens := cacheInfo.CacheReadTokens
	savings := float64(cacheReadTokens) * hitRate
	if int(savings) > totalTokens {
		return totalTokens
	}
	return int(savings)
}

// GetCacheControl returns the unified cache_control for the given model cache
// capability.
//
// GetCacheControl 根据模型缓存能力探测结果返回 CacheControl（C6 契约）。
//
// 接受 ai.CacheCapabilityProbe 接口 + modelName 字符串，通过 probe.Probe(modelName)
// 获取模型的完整缓存能力，再委托 ai.DefaultCacheControl 生成 CacheControl。
// 此签名符合 C6 契约要求："通过 probe 接口注入"，使 Context 无需自行调用探测，
// 且 probe 行为可 mock。
//
// P0-4 (X4): 委托 ai.DefaultCacheControl，Context 模块使用 AI 模块权威的
// CacheControl 类型。
// 返回 {type:"ephemeral", ttl:"5m"/"1h", scope:""} 用于 Anthropic 风格缓存，
// 或空 CacheControl（不注入）用于其他风格。
func (c *CacheAwareCompactor) GetCacheControl(probe ai.CacheCapabilityProbe, modelName string) ai.CacheControl {
	return ai.DefaultCacheControl(probe.Probe(modelName))
}

// InjectCacheControlMarkers sets cache_control metadata on boundary messages
// so findCacheBoundary can locate cache breakpoints (P0-4 / P0-7).
//
// The cache_control value is stored in msg.Metadata["cache_control"] as an
// ai.CacheControl struct. Messages before and including the boundary index
// receive the marker; this aligns with Anthropic's ephemeral cache breakpoint
// semantics where the prefix up to the marked message is cached.
//
// Returns a new slice (does not mutate the input).
func (c *CacheAwareCompactor) InjectCacheControlMarkers(
	messages []session.Message,
	boundaryIdx int,
	cc ai.CacheControl,
) []session.Message {
	if cc.IsEmpty() || boundaryIdx < 0 || boundaryIdx >= len(messages) {
		return messages
	}
	result := make([]session.Message, len(messages))
	copy(result, messages)
	// Mark the boundary message with cache_control metadata
	if result[boundaryIdx].Metadata == nil {
		result[boundaryIdx].Metadata = make(map[string]interface{})
	}
	result[boundaryIdx].Metadata["cache_control"] = cc
	return result
}

// CacheBreakHandler subscribes to bus.CacheBreakEvent and notifies the
// Context module when cache breaks are detected (P0-5 / C18 contract).
//
// Per C18: the AI module is the SOLE publisher of CacheBreakEvent.
// The Context module subscribes via this handler and reacts by either
// triggering cache-aware compaction or marking the cache as broken —
// it does NOT republish the event.
//
// 候选 B：缓存恢复冷却机制。
// recoveryCooldown 定义冷却窗口（默认 5min，对齐 Anthropic ephemeral cache TTL）。
// 当 LastBreakAge > recoveryCooldown 时视为已恢复，TryRecoverCache 清空 recentBreaks。
//
// Thread-safe: Start/Stop/RecentBreaks/IsCacheBroken/IsCacheHealthy/TryRecoverCache
// may be called concurrently from multiple goroutines.
type CacheBreakHandler struct {
	bus     *bus.Bus
	onBreak func(evt bus.CacheBreakEvent)

	mu           sync.Mutex
	started      bool
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	eventCh      <-chan bus.Event
	recentBreaks []bus.CacheBreakEvent
	maxHistory   int

	// SNAPSHOT-3：本轮新增字段
	// recoveryCooldown 缓存恢复冷却窗口（默认 5min，对齐 Anthropic ephemeral cache TTL）
	// 通过 SetCacheRecoveryCooldown 注入；零值时 IsCacheHealthy/TryRecoverCache 使用默认 5min
	recoveryCooldown time.Duration
}

// NewCacheBreakHandler creates a new CacheBreakHandler.
//
// onBreak is invoked synchronously from the event loop goroutine for each
// CacheBreakEvent received. If onBreak is nil, a no-op is used (the caller
// can still poll via RecentBreaks/IsCacheBroken).
func NewCacheBreakHandler(b *bus.Bus, onBreak func(evt bus.CacheBreakEvent)) *CacheBreakHandler {
	if onBreak == nil {
		onBreak = func(evt bus.CacheBreakEvent) {}
	}
	return &CacheBreakHandler{
		bus:        b,
		onBreak:    onBreak,
		maxHistory: 16,
	}
}

// Start subscribes to the bus and begins processing events in a goroutine.
// Returns an error if already started or if the bus is nil.
// The goroutine exits when Stop is called or the bus channel is closed.
func (h *CacheBreakHandler) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return fmt.Errorf("CacheBreakHandler already started")
	}
	if h.bus == nil {
		return fmt.Errorf("bus is nil")
	}
	h.eventCh = h.bus.Subscribe(64)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.started = true
	h.wg.Add(1)
	go h.loop(ctx)
	return nil
}

// eventCh is set in Start and read in loop. It is stored on the struct
// (rather than a local) so tests can inspect subscription state if needed.
// Field is intentionally unexported and only mutated under h.mu in Start.

// SetBus allows late binding of the bus (useful for tests that construct
// the handler before the bus). Calling after Start returns an error.
func (h *CacheBreakHandler) SetBus(b *bus.Bus) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return fmt.Errorf("cannot set bus after Start")
	}
	h.bus = b
	return nil
}

// Stop signals the event loop goroutine to exit and waits for it to drain.
// Safe to call multiple times; subsequent calls are no-ops.
//
// 修复 P0-15：在 loop 退出后调用 bus.Unsubscribe 取消订阅并关闭 channel，
// 从 bus.subscribers 切片中移除引用，避免长生命周期进程的内存泄漏。
func (h *CacheBreakHandler) Stop() {
	h.mu.Lock()
	if !h.started {
		h.mu.Unlock()
		return
	}
	h.started = false
	cancel := h.cancel
	// 修复 P0-15：保存 eventCh 引用，用于 Unsubscribe（必须在 mu 保护下读取）
	eventCh := h.eventCh
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
	// 修复 P0-15：loop 已退出，安全地取消订阅并关闭 channel
	if h.bus != nil && eventCh != nil {
		h.bus.Unsubscribe(eventCh)
	}
}

// loop runs the event processing loop until ctx is cancelled or the
// subscription channel is closed. Only one instance runs per handler.
func (h *CacheBreakHandler) loop(ctx context.Context) {
	defer h.wg.Done()
	ch := h.eventCh
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if cbEvt, isCB := evt.(bus.CacheBreakEvent); isCB {
				h.recordBreak(cbEvt)
				h.onBreak(cbEvt)
			}
		}
	}
}

// recordBreak appends the event to recentBreaks with bounded history.
// Caller: loop goroutine only. Uses h.mu for safety against concurrent
// readers of RecentBreaks/IsCacheBroken.
func (h *CacheBreakHandler) recordBreak(evt bus.CacheBreakEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recentBreaks = append(h.recentBreaks, evt)
	if len(h.recentBreaks) > h.maxHistory {
		h.recentBreaks = h.recentBreaks[len(h.recentBreaks)-h.maxHistory:]
	}
}

// RecentBreaks returns a copy of the recent cache break events.
// Thread-safe.
func (h *CacheBreakHandler) RecentBreaks() []bus.CacheBreakEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]bus.CacheBreakEvent, len(h.recentBreaks))
	copy(out, h.recentBreaks)
	return out
}

// IsCacheBroken returns true if any recent break event indicates significant
// cache degradation (CacheReadDrop > 0.3). Thread-safe.
//
// Used by the compaction pipeline to decide whether to skip cache-aware
// compaction (no point aligning to a broken cache) and fall back to the
// inner compactor instead.
func (h *CacheBreakHandler) IsCacheBroken() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, evt := range h.recentBreaks {
		if evt.CacheReadDrop > 0.3 {
			return true
		}
	}
	return false
}

// LastBreakAge returns the time since the most recent cache break event,
// or a large duration (1 hour) if no break has been recorded. Thread-safe.
//
// Callers can use this to expire "broken" status after a cooldown window
// (e.g. treat cache as healthy again if LastBreakAge > 5 minutes).
func (h *CacheBreakHandler) LastBreakAge() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.recentBreaks) == 0 {
		return time.Hour
	}
	last := h.recentBreaks[len(h.recentBreaks)-1]
	return time.Since(last.OccurredAt)
}

// ClearHistory discards all recorded break events. Thread-safe.
// Useful after a successful compaction that re-establishes a clean cache.
func (h *CacheBreakHandler) ClearHistory() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recentBreaks = h.recentBreaks[:0]
}

// SetCacheRecoveryCooldown 注入缓存恢复冷却窗口（候选 B）。
// 默认 5min（对齐 Anthropic ephemeral cache 5m TTL，@2026-07-21）。
// 测试可通过此方法设置 100ms 等短冷却。
// 线程安全：通过 h.mu 保护。
func (h *CacheBreakHandler) SetCacheRecoveryCooldown(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recoveryCooldown = d
}

// IsCacheHealthy 缓存是否健康（候选 B）。
//
// 返回 true 的条件（任一满足）：
// - recentBreaks 为空（无断裂历史）
// - 最近一次 break 距今超过 cooldown（已恢复）
//
// cooldown <= 0 时使用默认 5min（对齐 Anthropic ephemeral cache TTL）。
// 与 IsCacheBroken 的区别：IsCacheBroken 不考虑冷却窗口，永久返回 true 直到 ClearHistory；
// IsCacheHealthy 考虑冷却窗口，长会话内可自动恢复。
// 线程安全：通过 h.mu 保护。
func (h *CacheBreakHandler) IsCacheHealthy(cooldown time.Duration) bool {
	if cooldown <= 0 {
		// 读取注入的 cooldown，若未注入则用默认 5min
		h.mu.Lock()
		cooldown = h.recoveryCooldown
		h.mu.Unlock()
		if cooldown <= 0 {
			cooldown = 5 * time.Minute
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.recentBreaks) == 0 {
		return true
	}
	last := h.recentBreaks[len(h.recentBreaks)-1]
	return time.Since(last.OccurredAt) > cooldown
}

// TryRecoverCache 缓存恢复（候选 B）。
//
// 检查 IsCacheHealthy，若恢复则 ClearHistory 并返回 true。
// 调用方在 CompactWithCacheInfoInternal 入口调用。
//
// 落实审查建议优化 2：不先持锁再调 ClearHistory（ClearHistory 内部已持 h.mu）。
// 本方法仅读 h.recoveryCooldown 时持锁，其余操作委托给现有方法（IsCacheHealthy / ClearHistory）。
//
// 返回 true 表示刚从断裂恢复（recentBreaks 已清空）；
// 返回 false 表示仍在冷却窗口内或无断裂历史（无需恢复）。
func (h *CacheBreakHandler) TryRecoverCache() bool {
	// 仅当 recentBreaks 非空时才需要尝试恢复
	if breaks := h.RecentBreaks(); len(breaks) == 0 {
		return false
	}
	if !h.IsCacheHealthy(0) {
		return false
	}
	// 恢复：清空断裂历史（ClearHistory 内部已持 h.mu，避免双重加锁）
	h.ClearHistory()
	return true
}
