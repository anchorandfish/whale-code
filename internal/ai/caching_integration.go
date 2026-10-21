package ai

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/bus"
)

// CachingIntegration is the cache integration layer, coordinating breakpoint placement, break detection, and capability probing
type CachingIntegration struct {
	mu                 sync.RWMutex
	breakDetector      *CacheBreakDetector
	breakpointConfig   CacheBreakpointConfig
	capabilityProbe    *DefaultCacheCapabilityProbe // C6: 内部使用具体类型（有全部方法），外部通过接口引用
	metrics            *CacheMetrics
	enabled            bool
	model              string
	capability         CacheCapability
	capabilityEpoch    uint64 // increments whenever the active model capability changes
	currentPromptState PromptState
	bus                *bus.Bus // 事件总线（B4 批次 2：可为 nil，nil 时 CheckBreakAfterResponse 仍检测但不发布事件）
	sessionSwitchEpoch uint64   // 方案 R-3：SessionSwitch 计数器，in-flight 请求结果丢弃判定

	// N11 修复 N2：最近一次 API 响应的 CacheInfo 快照，供 handleCompact 传入
	// CompactionPipeline 使 CacheAwareMicroCompact 在生产路径生效。
	// 原实现 handleCompact 传 nil cacheInfo → G5 cache-aware 功能始终降级。
	// 每次 RecordCacheMetrics 时更新，GetCacheInfo() 返回只读副本。
	lastCacheInfo *CacheInfo
}

// NewCachingIntegration creates the cache integration layer
// Auto-detects cache capability by model name, enables caching only when model supports cache_control
// threshold is the cache break detection threshold (e.g., 0.05 = 5% drop triggers break detection)
func NewCachingIntegration(model string, threshold float64) *CachingIntegration {
	probe := getGlobalCapabilityProbe()
	capability := probe.GetCapability(model)
	enabled := capability.SupportsCacheControl

	// 方案 C：构造时传入 cachingStyle，DeepSeek 路径跳过被动检测
	breakDetector := NewCacheBreakDetectorWithStyle(threshold, capability.CachingStyle)

	return &CachingIntegration{
		breakDetector: breakDetector,
		breakpointConfig: CacheBreakpointConfig{
			Enabled:                  enabled,
			MaxBreakpointsPerRequest: 1,
			CacheScope:               "org",
		},
		capabilityProbe: probe,
		metrics:         NewCacheMetrics(),
		enabled:         enabled,
		model:           model,
		capability:      capability,
	}
}

// RecordStateBeforeRequest records state before API call (phase 1)
// Records hashes of system prompt and tool definitions for subsequent cache break detection.
// Auto-detects model switches and re-probes cache capability when the model changes.
func (ci *CachingIntegration) RecordStateBeforeRequest(systemPrompt string, toolDefs []ToolDefinition, model string) {
	ci.mu.RLock()
	enabled := ci.enabled
	ci.mu.RUnlock()

	if !enabled {
		return
	}

	// Auto-detect model change and update capability accordingly
	ci.mu.RLock()
	modelChanged := ci.model != model
	ci.mu.RUnlock()
	if modelChanged {
		ci.UpdateModel(model)
	}

	toolSchemas := make([]string, len(toolDefs))
	for i, td := range toolDefs {
		schemaJSON, _ := json.Marshal(td.InputSchema)
		toolSchemas[i] = td.Name + ":" + string(schemaJSON)
	}

	// Record state in detector (computes hashes internally)
	ci.breakDetector.RecordPromptState(systemPrompt, toolSchemas, model, "")

	// Use the detector's computed state to avoid double hashing
	ci.mu.Lock()
	if state := ci.breakDetector.GetLastPromptState(); state != nil {
		ci.currentPromptState = *state
	}
	ci.mu.Unlock()
}

// SetBus 延迟注入事件总线（B4 批次 2）。
// 允许在构造 CachingIntegration 之后、第一次请求之前注入 bus。
// bus 为 nil 时，CheckBreakAfterResponse 仍执行检测但不发布事件（向后兼容）。
// 并发安全：使用写锁保护。
func (ci *CachingIntegration) SetBus(b *bus.Bus) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	ci.bus = b
}

// SubscribeSessionEvents 订阅 SessionSwitchEvent，收到时重置断裂检测基线（方案 E + R-3）。
// 在 runner_core.go SetBus 之后调用。
// 行为：
//   - Reset() 清空基线（prevPromptState/prevCacheReadTokens）
//   - sessionSwitchEpoch++ 标记切换发生，in-flight 请求结果丢弃
//
// bus 为 nil 时跳过（向后兼容）。
func (ci *CachingIntegration) SubscribeSessionEvents(b *bus.Bus) {
	if b == nil {
		return
	}
	sub := b.Subscribe(8)
	go func() {
		for evt := range sub {
			if _, ok := evt.(bus.SessionSwitchEvent); ok {
				ci.mu.Lock()
				ci.breakDetector.Reset()
				ci.lastCacheInfo = nil
				ci.currentPromptState = PromptState{}
				ci.sessionSwitchEpoch++
				ci.mu.Unlock()
			}
		}
	}()
}

// CheckBreakAfterResponse 在 API 响应后检测缓存断裂（阶段2，B4 批次 2 接通发布链路）。
// 对比 API 返回的 cache read token 数与之前记录的基线，检测缓存是否断裂。
// 返回断裂检测结果；未检测到断裂时返回 nil。
// 若已注入 bus 且检测到断裂，同步发布 bus.CacheBreakEvent（C18：AI 模块为唯一发布方）。
// 方案 R-3：通过 sessionSwitchEpoch 检查 in-flight 请求与 SessionSwitch 的竞态，
// 期间发生 SessionSwitch 则结果丢弃（返回 nil）。
func (ci *CachingIntegration) CheckBreakAfterResponse(cacheReadTokens int) *CacheBreakResult {
	ci.mu.RLock()
	enabled := ci.enabled
	state := ci.currentPromptState
	busRef := ci.bus
	model := ci.model
	startEpoch := ci.sessionSwitchEpoch
	ci.mu.RUnlock()

	if !enabled {
		return nil
	}
	result := ci.breakDetector.CheckForCacheBreak(cacheReadTokens, state)
	if result == nil {
		return nil
	}

	// 方案 R-3：检测期间若发生 SessionSwitch，结果丢弃（in-flight 请求对新 session 无意义）
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	if ci.sessionSwitchEpoch != startEpoch {
		return nil
	}

	// 检测到断裂 → 发布事件（若 bus 已注入）。result != nil 即 IsBreak==true，再判一次更稳妥。
	if busRef != nil && result.IsBreak {
		busRef.Publish(bus.CacheBreakEvent{
			Model:         model,
			BreakReason:   result.BreakReason,
			CacheReadDrop: result.CacheReadDrop,
			OccurredAt:    time.Now(),
		})
	}
	return result
}

// RecordCacheMetrics records cache metrics
// Called after each request completes, tracks input/output token count and cache read token count
//
// N11 修复 N2：同时更新 lastCacheInfo 快照，供 GetCacheInfo() 返回给 handleCompact
// 传入 CompactionPipeline，使 CacheAwareMicroCompact 在生产路径生效。
func (ci *CachingIntegration) RecordCacheMetrics(inputTokens, outputTokens, cacheReadTokens int) {
	ci.metrics.RecordRequest(inputTokens, outputTokens, cacheReadTokens)

	// N2 修复：更新 lastCacheInfo 快照。
	// CacheCreationTokens = inputTokens - cacheReadTokens（标准公式，负数钳位为 0）
	// LastCacheTime = time.Now()（D3 修复要求 cacheInfo.LastCacheTime 非零值，
	// 使 isCacheActive 判断 cache TTL 时有效）
	cacheCreation := inputTokens - cacheReadTokens
	if cacheCreation < 0 {
		cacheCreation = 0
	}
	ci.mu.Lock()
	ci.lastCacheInfo = &CacheInfo{
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreation,
		TotalCachedTokens:   cacheReadTokens + cacheCreation,
		LastCacheTime:       time.Now(),
	}
	ci.mu.Unlock()
}

// RecordHistoricalCacheMetrics keeps process-wide accounting for a completed
// request whose session/model generation is stale, without replacing the
// active request's lastCacheInfo snapshot.
func (ci *CachingIntegration) RecordHistoricalCacheMetrics(inputTokens, outputTokens, cacheReadTokens int) {
	ci.metrics.RecordRequest(inputTokens, outputTokens, cacheReadTokens)
}

// NotifyCacheDeletion notifies cache deletion (micro-compaction operation)
// Called when cache deletion event is expected, preventing false cache break reports
func (ci *CachingIntegration) NotifyCacheDeletion() {
	ci.breakDetector.NotifyCacheDeletion()
}

// NotifyCompaction notifies compaction operation
// Compaction changes context structure, call this method to reset cache read baseline to avoid false reports
func (ci *CachingIntegration) NotifyCompaction() {
	ci.breakDetector.NotifyCompaction()
}

// IsEnabled returns whether caching is enabled
// Returns true only when current model supports cache_control
func (ci *CachingIntegration) IsEnabled() bool {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.enabled
}

// UpdateModel updates the model and re-probes cache capability
func (ci *CachingIntegration) UpdateModel(model string) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	if ci.model == model {
		return
	}
	ci.model = model
	capability := ci.capabilityProbe.GetCapability(model)
	ci.capability = capability
	ci.enabled = capability.SupportsCacheControl
	ci.breakpointConfig.Enabled = capability.SupportsCacheControl
	ci.capabilityEpoch++
	ci.lastCacheInfo = nil
	ci.currentPromptState = PromptState{}
	// 方案 R-2：模型切换时同步通知 breakDetector 更新 cachingStyle + 重置基线，
	// 避免跨模型 hash/token 比对产生误判。
	ci.breakDetector.UpdateCachingStyle(capability.CachingStyle)
}

// CapabilityEpoch returns a generation for model/cache-capability changes.
// Async compaction captures it and discards candidates built for stale cache
// semantics before PrepareCommit can inject markers.
func (ci *CachingIntegration) CapabilityEpoch() uint64 {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.capabilityEpoch
}

// GetMetrics returns the cache metrics tracker
// Can be used to get cache hit rate, total request count and other statistics
func (ci *CachingIntegration) GetMetrics() *CacheMetrics {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.metrics
}

// GetCapability returns cache capability info for the current model
// Including whether cache_control, cache_editing, cache_reference are supported etc.
func (ci *CachingIntegration) GetCapability() CacheCapability {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.capability
}

// SetBreakThreshold sets the cache break detection threshold
func (ci *CachingIntegration) SetBreakThreshold(threshold float64) {
	ci.breakDetector.SetBreakThreshold(threshold)
}

// GetCacheInfo 返回最近一次 API 响应的 CacheInfo 快照（N11 修复 N2）。
//
// 供 handleCompact 传入 CompactionPipeline.Execute，使 CacheAwareMicroCompact
// 在生产路径生效。原实现 handleCompact 传 nil cacheInfo → G5 cache-aware
// 功能始终降级为普通 MicroCompact。
//
// 返回 nil 表示尚无 API 响应记录（首次请求前），pipeline 会降级为普通 MicroCompact。
// 返回值是只读副本，调用方可安全持有。
func (ci *CachingIntegration) GetCacheInfo() *CacheInfo {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	if ci.lastCacheInfo == nil {
		return nil
	}
	// 返回副本，避免外部修改内部状态
	cp := *ci.lastCacheInfo
	return &cp
}

// RecordCacheAnomaly 记录一次缓存异常事件（H13-G1）。
//
// 当 cachedInputTokens > estimatedTokens（provider 异常上报）时，由
// GetCachedInputTokens 辅助函数通过此回调记录 anomaly。
// 内部委托给 CacheMetrics.RecordAnomaly 递增 TotalAnomalies 计数器。
func (ci *CachingIntegration) RecordCacheAnomaly() {
	if ci == nil || ci.metrics == nil {
		return
	}
	ci.metrics.RecordAnomaly()
}
