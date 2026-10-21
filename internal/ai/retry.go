// Package ai — exponential backoff retry + model fallback
//
// ============================================================
// C3/C4/C5 兼容性承诺（跨模块契约对齐 — Phase 0）
// ============================================================
//
// C3 — StreamWithRetry 签名承诺：
//
//	包级函数 ai.StreamWithRetry 签名保持不变，context/tools 继续使用。
//	签名：func StreamWithRetry(ctx, client, req, config, fallback, coordinator) (<-chan Event, error)
//	AI 模块内部如需方法形式（c.StreamWithRetry），应委托到包级函数，不破坏现有调用方。
//
// C4 — FallbackTracker 兼容性承诺：
//
//	type FallbackTracker = FallbackChain // 类型别名，保证类型名兼容
//	func (f *FallbackChain) Record529() bool // 旧签名保留（向后兼容）
//	func (f *FallbackChain) Record529WithModel() (bool, string) // 新增方法（AI 内部用）
//	旧调用方（context/tools/tui）只需知道"是否降级"，不需要知道"降级到哪个模型"。
//	模型信息通过 ActiveModel() 查询。
//
// C5 — QuerySource 零值承诺：
//
//	QuerySource 零值 = QuerySourceForeground（最安全，不 drop）。
//	529 处理：只有 QuerySourceBackground 才 drop，其余均重试。
//	各调用方应显式设置 QuerySource 以表明意图。
package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/metrics"
)

// FloorOutputTokens is the lower limit for max_tokens compression (P0-4 / P0-8).
// Phase 3 压缩 max_tokens 时每次 ×0.75，但不低于此下限。
const FloorOutputTokens = 3000

// Phase3CompressFunc is the callback signature for Phase 3 context compaction
// (P0-9 / C10 contract).
//
// The AI module calls this hook before reducing MaxTokens in Phase 3 retry.
// The Context module injects CompactionPipeline.Execute via this hook so that
// Phase 3 triggers REAL context compaction (not just MaxTokens reduction).
//
// Parameters:
// - ctx: request context (for cancellation / deadline).
// - messages: current request messages (ai.MessageEntry format).
//
// Returns:
// - compacted: the compacted message list (may be shorter than input).
// - ok: true if compaction actually occurred (messages were modified).
// - err: non-nil if compaction failed; the AI module falls back to
// MaxTokens reduction in that case.
//
// Thread-safety: the hook MUST be safe to call concurrently from multiple
// goroutines (StreamWithRetry may be invoked concurrently across requests).
type Phase3CompressFunc func(ctx context.Context, messages []MessageEntry) (compacted []MessageEntry, ok bool, err error)

// RetryConfig is the retry configuration.
//
// P0 Feature Flags（正向命名，默认 true 通过 DefaultRetryConfig 设置）：
// 零值（false）= 禁用（向后兼容旧行为）。
// 现有测试使用字面量 RetryConfig{...} 构造时，flag 为 false → 旧行为。
// 新代码应使用 DefaultRetryConfig() 获取默认配置（flag=true → 新行为）。
type RetryConfig struct {
	MaxRetries  int           `json:"max_retries"`
	BaseDelay   time.Duration `json:"base_delay"`
	MaxDelay    time.Duration `json:"max_delay"`
	JitterRatio float64       `json:"jitter_ratio"`

	// EnableContextOverflowRetry: context overflow 时自动调低 max_tokens 后重试（P0-4 / FACT-ERR-003/006）。
	// false = 立即返回错误（旧行为）。
	EnableContextOverflowRetry bool `json:"enable_context_overflow_retry"`
	// EnableMidStreamResume: 流式中断恢复（P0-5 / FACT-RESUME）。
	// false = 不跟踪可恢复性，中断后不重试。
	EnableMidStreamResume bool `json:"enable_mid_stream_resume"`
	// EnableMultiLevelFallback: 多级模型降级链（P0-1）。
	// false = 仅 Phase 1 同模型重试，不进入 Phase 2 降级。
	EnableMultiLevelFallback bool `json:"enable_multi_level_fallback"`

	// OnPhase3Compress: Phase 3 上下文压缩 hook（P0-9 / C10）。
	// 若设置，StreamWithRetry 在 Phase 3 降低 MaxTokens 之前先调用此 hook
	// 触发真正的上下文压缩（由 Context 模块注入 CompactionPipeline.Execute）。
	// 若 hook 为 nil、返回 ok=false 或返回 err != nil，降级为旧的 MaxTokens *= 0.75。
	// json:"-" 因为函数不可序列化。
	OnPhase3Compress Phase3CompressFunc `json:"-"`

	// OnCachingIntegration: 缓存集成层 hook（B4 批次 2 / B0 断链修复）。
	// 若设置，StreamWithRetry 在请求前调用 RecordStateBeforeRequest 建立基线，
	// 在响应事件流结束后（收到 UsageEvent 或 MessageStopEvent）调用 CheckBreakAfterResponse 检测断裂。
	// hook 为 nil 时跳过缓存断裂检测（向后兼容旧行为）。
	// json:"-" 因为 *CachingIntegration 不可序列化。
	OnCachingIntegration *CachingIntegration `json:"-"`

	// OnRegionRouter 多 region 路由注入（/ T1-C SNAPSHOT）。
	// 若设置，StreamWithRetry 在 529 时优先切换 region（同模型不同 baseURL），
	// 所有 region 均熔断（ErrAllRegionsFailed）才走 FallbackChain.Record529 切模型。
	// nil 时跳过 region 路由（向后兼容旧行为，单 baseURL）。
	// 与 OnPhase3Compress / OnCachingIntegration 模式一致，C3 兼容性保持。
	// json:"-" 因为 RegionRouter 接口不可序列化。
	OnRegionRouter RegionRouter `json:"-"`

	// OnReconnectWrapper 流中断重连包装器注入（/ T1-C SNAPSHOT）。
	// 若设置，client.Stream 内部应用 ReconnectWrapper 提供流中断重连能力。
	// nil 时跳过重连（向后兼容旧行为，流中断即失败）。
	// 与 OnRegionRouter 模式一致，C3 兼容性保持。
	// json:"-" 因为 *ReconnectWrapper 不可序列化。
	OnReconnectWrapper *ReconnectWrapper `json:"-"`
}

// DefaultRetryConfig returns default retry config with all P0 feature flags enabled.
//
// P0-2（D5-1）修复：MaxRetries 从 3 降为 2，减少卡死请求的重试累积时间。
// 重试间隔由 BaseDelay=1s + 指数退避计算：1s → 2s → 4s（attempt 0/1/2）。
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:                 2,
		BaseDelay:                  1 * time.Second,
		MaxDelay:                   30 * time.Second,
		JitterRatio:                0.25,
		EnableContextOverflowRetry: true,
		EnableMidStreamResume:      true,
		EnableMultiLevelFallback:   true,
	}
}

// recomputeMaxTokensForContextOverflow 根据解析的 token 数重算 max_tokens（P0-4）。
// 每次压缩 ×0.75；若消息携带精确 token 数则用 contextLimit - inputTokens 作为上限。
// 下限 FloorOutputTokens。
func recomputeMaxTokensForContextOverflow(currentMaxTokens, inputTokens, contextLimit int) int {
	newMax := int(float64(currentMaxTokens) * 0.75)
	if contextLimit > 0 && inputTokens > 0 {
		available := contextLimit - inputTokens
		if available < newMax {
			newMax = available
		}
	}
	if newMax < FloorOutputTokens {
		newMax = FloorOutputTokens
	}
	return newMax
}

// truncateHeadForPTLRetry 截断消息头部以应对 prompt too long 错误（P0-4 / FACT-ERR-006）。
// 保留后半部分消息（至少保留最后一条），截断前面的历史。
func truncateHeadForPTLRetry(messages []MessageEntry) []MessageEntry {
	if len(messages) <= 1 {
		return messages
	}
	keep := len(messages) / 2
	if keep < 1 {
		keep = 1
	}
	return messages[len(messages)-keep:]
}

// CalculateBackoff calculates backoff time with RetryAfter preference
func CalculateBackoff(attempt int, config RetryConfig, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > config.MaxDelay {
			return config.MaxDelay
		}
		return retryAfter
	}
	return calculateBackoffExponential(attempt, config)
}

// CalculateBackoffLegacy 保留旧签名，向后兼容
func CalculateBackoffLegacy(attempt int, config RetryConfig) time.Duration {
	return calculateBackoffExponential(attempt, config)
}

// calculateBackoffExponential 指数退避计算
func calculateBackoffExponential(attempt int, config RetryConfig) time.Duration {
	delay := float64(config.BaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(config.MaxDelay) {
		delay = float64(config.MaxDelay)
	}

	jitter := delay * config.JitterRatio * (2*rand.Float64() - 1)
	delay += jitter

	if delay < 0 {
		delay = 0
	}

	return time.Duration(delay)
}

// FallbackChain is the model fallback tracker (C4: 原 FallbackTracker 重命名)。
// Thread-safe for concurrent use across multiple requests.
//
// P0-1 (A6)：支持多级 fallback 链。
// 默认链 [primary]（不降级）；配置 fallback 后链为 [primary, fallback1, fallback2, ...]。
// 连续 529 达到 degradationThreshold 时降级到下一级；非 529 错误重置计数。
type FallbackChain struct {
	mu                     sync.Mutex
	consecutive529Count    int
	degradationThreshold   int
	isDegraded             bool
	originalModel          string
	fallbackModel          string   // 旧字段，兼容 NewFallbackTracker(original, fallback)
	fallbackModels         []string // P0-1: 多级 fallback 链（不含 primary）
	currentFallbackIdx     int      // P0-1: 当前降级到 fallbackModels 的第几级（-1 = 未降级）
	successCountOnFallback int
	autoRestoreThreshold   int
}

// FallbackTracker is a type alias for FallbackChain (C4 向后兼容)。
// 旧代码引用 *ai.FallbackTracker 仍可编译（通过别名解析为 *FallbackChain）。
type FallbackTracker = FallbackChain

// NewFallbackTracker creates a fallback tracker (C4: 保留旧构造函数名)。
// 默认链为 [originalModel, fallbackModel]；fallbackModel 为空时链为 [originalModel]（不降级）。
func NewFallbackTracker(originalModel, fallbackModel string) *FallbackTracker {
	chain := []string{}
	if fallbackModel != "" {
		chain = append(chain, fallbackModel)
	}
	return &FallbackChain{
		degradationThreshold: 3,
		autoRestoreThreshold: 5,
		originalModel:        originalModel,
		fallbackModel:        fallbackModel,
		fallbackModels:       chain,
		currentFallbackIdx:   -1,
	}
}

// NewFallbackChain creates a multi-level fallback chain (P0-1 新增)。
// primary 为首选模型，fallbacks 为降级链（按优先级排序）。
func NewFallbackChain(primary string, fallbacks ...string) *FallbackChain {
	return &FallbackChain{
		degradationThreshold: 3,
		autoRestoreThreshold: 5,
		originalModel:        primary,
		fallbackModel:        firstNonEmpty(fallbacks),
		fallbackModels:       filterNonEmpty(fallbacks),
		currentFallbackIdx:   -1,
	}
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func filterNonEmpty(ss []string) []string {
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

// Record529 records a 529 error, returns whether to fallback (C4: 旧签名保留)。
// 旧调用方只需知道"是否降级"，不需要知道"降级到哪个模型"。
// P0-1：支持多级降级——首次降级到 fallbackModels[0]，再次降级到 fallbackModels[1]，依此类推。
func (f *FallbackChain) Record529() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutive529Count++
	if f.consecutive529Count >= f.degradationThreshold &&
		len(f.fallbackModels) > 0 &&
		f.currentFallbackIdx < len(f.fallbackModels)-1 {
		f.currentFallbackIdx++
		f.isDegraded = true
		return true
	}
	return false
}

// Record529WithModel records a 529 error, returns whether to fallback and the target model (C4: 新增方法)。
// 供 AI 模块内部需要立即知道降级目标的场景使用。
func (f *FallbackChain) Record529WithModel() (degraded bool, toModel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutive529Count++
	if f.consecutive529Count >= f.degradationThreshold &&
		len(f.fallbackModels) > 0 &&
		f.currentFallbackIdx < len(f.fallbackModels)-1 {
		f.currentFallbackIdx++
		f.isDegraded = true
		return true, f.fallbackModels[f.currentFallbackIdx]
	}
	return false, ""
}

// RecordNon529Error records a non-529 error, resetting consecutive529Count (P0-1 / FACT-ERR-002)。
// 任何非 529 错误（如 500、timeout）都应重置 529 连续计数器，
// 避免间歇性 529 累积导致误降级。
func (f *FallbackChain) RecordNon529Error() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutive529Count = 0
}

// RecordSuccess records success, resets count and auto-restores if threshold met
func (f *FallbackChain) RecordSuccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutive529Count = 0
	if f.isDegraded {
		f.successCountOnFallback++
		if f.successCountOnFallback >= f.autoRestoreThreshold {
			f.isDegraded = false
			f.currentFallbackIdx = -1
			f.successCountOnFallback = 0
		}
	}
}

// ActiveModel is the currently active model.
// 返回 string 而非 ModelConfig，以保持与 tui/context/tools 调用方的向后兼容（C4）。
func (f *FallbackChain) ActiveModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.isDegraded && f.currentFallbackIdx >= 0 && f.currentFallbackIdx < len(f.fallbackModels) {
		return f.fallbackModels[f.currentFallbackIdx]
	}
	// 兼容旧字段 fallbackModel（当 fallbackModels 未设置时）
	if f.isDegraded && f.fallbackModel != "" && len(f.fallbackModels) == 0 {
		return f.fallbackModel
	}
	return f.originalModel
}

// IsDegraded checks if degraded to fallback
func (f *FallbackChain) IsDegraded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.isDegraded
}

// Restore recovers to the original model
func (f *FallbackChain) Restore() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.isDegraded = false
	f.consecutive529Count = 0
	f.currentFallbackIdx = -1
}

// SetOriginalModel updates the original model name
func (f *FallbackChain) SetOriginalModel(model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.originalModel = model
	if f.isDegraded {
		f.isDegraded = false
		f.consecutive529Count = 0
		f.currentFallbackIdx = -1
	}
}

// StreamWithRetry is a streaming call with retry, degrade, and compress recovery.
// Recovery phases coordinated by RecoveryCoordinator:
// 1. Retry phase: retry with same model (coordinator strategy "retry")
// 2. Degrade phase: switch to fallback model (coordinator strategy "degrade")
// 3. Compress phase: reduce max tokens (coordinator strategy "compress")
//
// P0-4 (A8) 三阶段重试：
// - Phase 1: 同模型重试（带退避 + retry-after 优先）。
// context overflow 时（若 EnableContextOverflowRetry）自动调低 max_tokens 后重试；
// prompt too long 时截断消息头部后重试。
// 529 时按 QuerySource 区分：Background 直接 drop，其余重试/降级。
// - Phase 2: 模型降级（若 EnableMultiLevelFallback）。
// - Phase 3: 压缩 MaxTokens（MaxRetries 次带退避，每次 ×0.75，下限 FloorOutputTokens）。
//
// C3 兼容性：包级函数签名保持不变，context/tools 继续使用。
//
// Model recovery: when FallbackTracker is degraded, the client model is NOT
// restored in defer — this allows FallbackTracker's autoRestore mechanism
// (triggered after autoRestoreThreshold consecutive successes) to manage
// recovery. On the next call, ActiveModel() reflects the restored state.
func StreamWithRetry(ctx context.Context, client Client, req Request, config RetryConfig, fallback *FallbackTracker, coordinator *RecoveryCoordinator) (events <-chan Event, err error) {
	// B6-P1-1: 接线 RecordAIRequest，使用命名返回 + defer 在所有返回路径统一记录指标。
	// duration 衡量从请求入口到流建立（或最终失败）的耗时，含重试等待。
	start := time.Now()
	defer func() {
		metrics.Default.RecordAIRequest(time.Since(start), err)
	}()

	// Sync client model with FallbackTracker state so autoRestore takes effect
	if fallback != nil {
		activeModel := fallback.ActiveModel()
		if activeModel != "" && activeModel != client.Model() {
			client.SetModel(activeModel)
			req.Model = activeModel
		}
	}

	originalModel := client.Model()
	defer func() {
		// If FallbackTracker is degraded, leave the model as-is so the
		// autoRestore mechanism can recover it after enough successes.
		if fallback != nil && fallback.IsDegraded() {
			return
		}
		if client.Model() != originalModel {
			client.SetModel(originalModel)
		}
	}()

	var lastErr error
	var apiErr *APIError

	// / T1-C：region 路由状态（吸纳审查优化 4）
	// currentRegion 当前正在使用的 region 名（首次为空，529 后切换）
	// excludedRegions 已失败的 region 列表（避免重复选择）
	var currentRegion string
	var excludedRegions []string
	// 若配置了 OnRegionRouter，首次调用前选择初始 region
	// 这样 529 时可以标记当前 region 失败并切到下一个
	if config.OnRegionRouter != nil {
		if baseURL, region, err := config.OnRegionRouter.SelectRegion(req.Model, excludedRegions); err == nil {
			currentRegion = region
			req.BaseURL = baseURL
		}
		// ErrAllRegionsFailed 时跳过 region 路由，使用 client 默认 baseURL
	}

	// ================================================================
	// Phase 1: Retry with same model (P0-4 / A8)
	// ================================================================
	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// S17 G1 / HG1-A：Phase1 的同模型重试预算完全由 RetryConfig.MaxRetries 驱动。
			// 这里**不再**咨询 coordinator.BeforeRecovery("retry")：该状态机在首个失败后
			// 即推进到 Retrying，使 BeforeRecovery("retry") 返回 false → 重试循环在此 break，
			// 把 MaxRetries+1 次同模型尝试压扁为 1 次，瞬态 5xx 直接失败。
			// coordinator 仅在 Phase1 耗尽后参与 degrade/compress 升级判定（见循环后）。
			// 业界对标同类 CLI（有界计数器）。
			var retryAfter time.Duration
			if apiErr != nil {
				retryAfter = apiErr.RetryAfter
			}
			delay := CalculateBackoff(attempt-1, config, retryAfter)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		events, err := client.Stream(ctx, req)
		if err == nil {
			if fallback != nil {
				fallback.RecordSuccess()
			}
			if coordinator != nil {
				coordinator.RecordSuccess()
			}

			// ===== B4 批次 2 新增：接通缓存断裂检测 =====
			// 阶段 1：请求前记录状态（SystemPrompt 是 []string，RecordStateBeforeRequest 接收 string，需 join）
			// 阶段 2：包装事件流，拦截 UsageEvent 提取 CacheReadTokens，流结束时调 CheckBreakAfterResponse
			if config.OnCachingIntegration != nil {
				config.OnCachingIntegration.RecordStateBeforeRequest(
					strings.Join(req.SystemPrompt, "\n"),
					req.Tools,
					req.Model,
				)
				events = wrapWithCacheBreakCheck(ctx, events, config.OnCachingIntegration)
			}
			// ===== B4 批次 2 新增结束 =====

			return events, nil
		}

		// 防御性双保险：ctx 取消时直接返回 raw ctx.Err()，
		// 避免 context.Canceled 进入下方重试分类逻辑。errors.Is 路径已可正确穿透
		// （见 app.go:1433），此处提前返回提供更干净的错误对象，并防御未来 %w 链被破坏。
		// DeadlineExceeded 同理短路，由上层 formatFriendlyError 正常显示（不静默）。
		// 补充 fallback/coordinator 状态更新（实施子审查建议1），避免跨请求状态污染。
		if ctx.Err() != nil {
			if fallback != nil {
				fallback.RecordNon529Error()
			}
			if coordinator != nil {
				coordinator.RecordRetryAttempt()
			}
			return nil, ctx.Err()
		}

		lastErr = err
		errMsg := err.Error()

		errors.As(err, &apiErr)

		// --- Context overflow handling (P0-4 / FACT-ERR-003/006) ---
		// **结构化优先** —— err 链上带 *APIError 时由 StatusCode 裁决，拿不到才回落文本。
		// 收口前这里直接读 errMsg = err.Error()，而 APIError.Error() 自带 "API error 413 …"
		// ⇒ 裸 "413" 子串一命中就抢答（例如 500 的 body 里带 request-id 4132），
		// 使下方 `switch apiErr.ErrorID` 的结构化分类**永远读不到**。
		if isContextOverflowErr(err) {
			if !config.EnableContextOverflowRetry {
				// 旧行为：立即返回（向后兼容字面量 RetryConfig{...}）
				return nil, fmt.Errorf("context overflow: %w", err)
			}
			// prompt too long → 截断消息头部后重试
			if isPromptTooLongError(errMsg) {
				truncated := truncateHeadForPTLRetry(req.Messages)
				if len(truncated) < len(req.Messages) {
					req.Messages = truncated
					if fallback != nil {
						fallback.RecordNon529Error()
					}
					if coordinator != nil {
						coordinator.RecordRetryAttempt()
					}
					continue
				}
				// 消息已无法截断 → 进入 Phase 3 压缩
				break
			}
			// context overflow → 重算 max_tokens 后重试
			inputT, _, ctxLimit, _ := parseContextOverflowMsg(errMsg)
			newMax := recomputeMaxTokensForContextOverflow(req.MaxTokens, inputT, ctxLimit)
			if newMax >= req.MaxTokens {
				// 已到下限，无法继续压缩 → 进入 Phase 3
				break
			}
			req.MaxTokens = newMax
			if fallback != nil {
				fallback.RecordNon529Error()
			}
			if coordinator != nil {
				coordinator.RecordRetryAttempt()
			}
			continue
		}

		// --- 529 overloaded handling (P0-3 / A7 / FACT-ERR-001) ---
		// 基于 ErrorID 驱动，而非字符串匹配
		if apiErr != nil && apiErr.ErrorID == ErrAIApiOverload {
			// C5: 只有 Background 才 drop，其余重试
			if !shouldRetryOn529(req.QuerySource) {
				if coordinator != nil {
					coordinator.RecordRetryAttempt()
				}
				return nil, WithID(err, ErrAIFatal)
			}

			// / T1-C：region 优先切换（吸纳审查优化 4）
			// 529 时优先切到下一个 region（同模型不同 baseURL），所有 region 熔断才走 FallbackChain 切模型
			// 不进入 Phase 1 重试（避免重试已熔断的 region）
			// region 切换是重试优化而非阶段失败，故不计入 coordinator（Phase1 同模型重试
			// 预算由 config.MaxRetries 独立限界，见 HG1-A）；仅在所有 region 熔断、走
			// FallbackChain 切模型时才走下方记账。
			if config.OnRegionRouter != nil && currentRegion != "" {
				// 标记当前 region 失败（用于熔断）
				config.OnRegionRouter.MarkRegionFailed(currentRegion)
				excludedRegions = append(excludedRegions, currentRegion)
				// 选择下一个 region（同模型不同 baseURL）
				if newBaseURL, newRegion, regionErr := config.OnRegionRouter.SelectRegion(req.Model, excludedRegions); regionErr == nil {
					currentRegion = newRegion
					req.BaseURL = newBaseURL
					// 不计入 coordinator：region 切换仍属于 Phase 1 重试（由 MaxRetries 限界）
					continue
				}
				// ErrAllRegionsFailed → 走 FallbackChain.Record529 切模型（不进入 Phase 1 重试）
			}

			// Foreground/Agent: 尝试降级
			if fallback != nil && fallback.Record529() {
				fallbackModel := fallback.ActiveModel()
				if fallbackModel != "" && fallbackModel != req.Model {
					req.Model = fallbackModel
					client.SetModel(fallbackModel)
					if coordinator != nil {
						coordinator.RecordRetryAttempt()
					}
					continue
				}
			}
			// 无 fallback 或未达降级阈值：同模型重试
			if coordinator != nil {
				coordinator.RecordRetryAttempt()
			}
			continue
		}

		// --- 529 回落：结构化分类未判过载时，按文本兜底 ---
		// 策略反转后，529 仍需走降级链路而非无条件重试。
		// 收口前这里是 `apiErr == nil && IsOverloadedError(errMsg)` —— 用**跨迭代可能陈旧**的
		// apiErr 当门：上一轮是 APIError、本轮是普通 "529 …" 文本时，
		// 该门会把本轮的文本判据整条挡掉。改用结构化优先入口
		// （isOverloadedErr 内部自带一次 errors.As，读的是**本轮** err）。
		if isOverloadedErr(err) {
			if !shouldRetryOn529(req.QuerySource) {
				if coordinator != nil {
					coordinator.RecordRetryAttempt()
				}
				return nil, WithID(err, ErrAIFatal)
			}

			// / T1-C：region 优先切换（同上 ErrorID 驱动路径，字符串兜底路径同样适用）
			// 同样不计入 coordinator：region 切换仍属于 Phase 1 重试（由 MaxRetries 限界）
			if config.OnRegionRouter != nil && currentRegion != "" {
				config.OnRegionRouter.MarkRegionFailed(currentRegion)
				excludedRegions = append(excludedRegions, currentRegion)
				if newBaseURL, newRegion, regionErr := config.OnRegionRouter.SelectRegion(req.Model, excludedRegions); regionErr == nil {
					currentRegion = newRegion
					req.BaseURL = newBaseURL
					continue
				}
			}

			if fallback != nil && fallback.Record529() {
				fallbackModel := fallback.ActiveModel()
				if fallbackModel != "" && fallbackModel != req.Model {
					req.Model = fallbackModel
					client.SetModel(fallbackModel)
					if coordinator != nil {
						coordinator.RecordRetryAttempt()
					}
					continue
				}
			}
			if coordinator != nil {
				coordinator.RecordRetryAttempt()
			}
			continue
		}

		// --- 基于 ErrorID 的错误分类处理 ---
		if apiErr != nil {
			switch apiErr.ErrorID {
			case ErrAIApiRateLimit:
				if coordinator != nil {
					coordinator.RecordRetryAttempt()
				}
				continue
			case ErrAIApiServer, ErrAIRetryable:
				// 方案 M：cache 探测失败（ErrAIRetryable + isCacheControlError）时，
				// cache 已在上层 disableCacheControlLocked 禁用，直接重试（下次不带 cache_control）。
				// 不走退避：cache 探测失败是确定性可恢复，重试即可。
				// S6 / L1：原生检索被端点拒绝时同样属"确定性可恢复"
				// （上层已 disableNativeSearchLocked，重试时不再注入 enable_search）⇒ 走同一条快路径。
				if apiErr.ErrorID == ErrAIRetryable &&
					(isCacheControlError(apiErr.Message) || isNativeSearchUnsupportedError(apiErr.Message)) {
					if coordinator != nil {
						coordinator.RecordRetryAttempt()
					}
					continue
				}
				if fallback != nil {
					fallback.RecordNon529Error()
				}
				if coordinator != nil {
					coordinator.RecordRetryAttempt()
				}
				continue
			case ErrAIFatal:
				if coordinator != nil {
					coordinator.RecordRetryAttempt()
				}
				return nil, err
			}
		}

		// --- 非 APIError 错误：重置 consecutive529Count (P0-1 / FACT-ERR-002) ---
		if fallback != nil {
			fallback.RecordNon529Error()
		}

		// --- 非结构化错误：保留字符串匹配作为兜底 ---
		if !IsRetryableErrorFromMsg(errMsg) {
			if coordinator != nil {
				coordinator.RecordRetryAttempt()
			}
			return nil, err
		}

		// Phase1 同模型重试记账（不推进状态机；预算由 config.MaxRetries 限界）。
		if coordinator != nil {
			coordinator.RecordRetryAttempt()
		}
	}

	// ================================================================
	// Phase 1 exhausted（同模型重试已耗尽，每个 attempt 均失败）
	//
	// S17 G1 / HG1-A：Phase1 期间只做 RecordRetryAttempt 记账、不推进状态机；
	// 此处**单次**推进一个阶段（Normal→Retrying），使下面的 degrade/compress 升级门
	// 依据"一次阶段步进"而非"每次尝试"判定。若摘掉此处接线，状态停留 Normal →
	// BeforeRecovery("degrade"/"compress") 均返 false → Phase2/3 退化为死代码。
	// 业界对标同类 CLI（重试=有界计数器）。
	//
	// S17 HG5=B：这里用 AdvancePhase（**不武装冷却**）而非 RecordFailure ——
	// 请求内的阶段步进若武装 30s 冷却，紧随其后的 degrade/compress 门会被自己的
	// 冷却挡掉（G1b 的原因②）。冷却仅由"最后一级升级失败"（Phase3）武装，用于跨请求节流。
	// ================================================================
	if coordinator != nil && lastErr != nil {
		coordinator.AdvancePhase()
	}

	// ================================================================
	// Phase 2: Attempt degradation via coordinator (P0-1 / A8)
	// ================================================================
	if config.EnableMultiLevelFallback && coordinator != nil && coordinator.BeforeRecovery("degrade") {
		// S17 HG5=A：degrade 阶段**已被批准进入** ⇒ 立即推进阶段机（Retrying→Degrading），
		// 不再依赖"模型是否真的切换"。旧实现把这次推进嵌在下面的
		// `fallbackModel != "" && fallbackModel != req.Model` 内 ⇒ 非 529 错误
		// （FallbackChain 不降级）时永不推进 ⇒ Phase3 的 compress 门恒为 false。
		// 本步进不武装冷却（HG5=B），故同一请求内 Phase3 立即可达。
		coordinator.AdvancePhase()

		if fallback != nil {
			fallbackModel := fallback.ActiveModel()
			if fallbackModel != "" && fallbackModel != req.Model {
				req.Model = fallbackModel
				client.SetModel(fallbackModel)

				for degradeAttempt := 0; degradeAttempt <= config.MaxRetries; degradeAttempt++ {
					if degradeAttempt > 0 {
						delay := CalculateBackoff(degradeAttempt-1, config, 0)
						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case <-time.After(delay):
						}
					}

					events, err := client.Stream(ctx, req)
					if err == nil {
						coordinator.RecordSuccess()
						fallback.RecordSuccess()
						return events, nil
					}
					lastErr = err

					// Context overflow on fallback model: 若启用重试则调低 max_tokens
					// 结构化优先（降级模型返回的错误同样可能是 *APIError）。
					if isContextOverflowErr(err) {
						if config.EnableContextOverflowRetry {
							newMax := recomputeMaxTokensForContextOverflow(req.MaxTokens, 0, 0)
							if newMax < req.MaxTokens {
								req.MaxTokens = newMax
								continue
							}
						}
						break
					}
					if !IsRetryableErrorFromMsg(err.Error()) {
						break
					}
					if fallback != nil {
						fallback.RecordNon529Error()
					}
				}
				// S17 HG5=A：阶段推进已上移到"degrade 阶段已进入"处（见本块开头），
				// 不再在此处（"模型确实切换"之后）记账 —— 否则未切换时不推进 Degrading，
				// Phase3 的 compress 门恒 false。
			}
		}
	}

	// ================================================================
	// Phase 3: Compress MaxTokens (P0-8 / A10, P0-9 / C10)
	// P0-9: 若注册了 OnPhase3Compress hook，第一次尝试前先触发真正的上下文压缩
	// （由 Context 模块注入 CompactionPipeline.Execute）。
	// MaxRetries 次带退避循环，每次 ×0.75，下限 FloorOutputTokens。
	// context overflow 且已到下限 → break。
	// ================================================================
	if coordinator != nil && coordinator.BeforeRecovery("compress") {
		for compressAttempt := 0; compressAttempt < config.MaxRetries; compressAttempt++ {
			// P0-9 / C10: 第一次尝试前触发真正的上下文压缩（hook 注入）
			if compressAttempt == 0 && config.OnPhase3Compress != nil {
				compacted, ok, compErr := config.OnPhase3Compress(ctx, req.Messages)
				if compErr == nil && ok && len(compacted) > 0 {
					req.Messages = compacted
				}
				// 压缩失败或未发生 → 降级到下面的 MaxTokens 压缩
			}
			// 压缩 max_tokens: ×0.75, 下限 FloorOutputTokens
			newMax := int(float64(req.MaxTokens) * 0.75)
			if newMax < FloorOutputTokens {
				newMax = FloorOutputTokens
			}
			// 已到下限且上次错误为 context overflow → 无法恢复，break
			// 结构化优先（与上方 Phase1 的判定同源）。
			if newMax >= req.MaxTokens && lastErr != nil && isContextOverflowErr(lastErr) {
				break
			}
			req.MaxTokens = newMax

			// 退避
			delay := CalculateBackoff(compressAttempt, config, 0)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}

			events, err := client.Stream(ctx, req)
			if err == nil {
				coordinator.RecordSuccess()
				return events, nil
			}
			lastErr = err
			// Phase3 是升级阶段：失败应推进状态机（Retrying→Degrading→…），
			// 与 Phase1 的同模型重试记账（RecordRetryAttempt）区分。
			coordinator.RecordFailure()

			// 非可重试且非 context overflow → break
			// 结构化优先；IsRetryableErrorFromMsg 仍是文本判据（本轮未收口，见回执）。
			isCtxOverflow := isContextOverflowErr(err)
			isRetryable := IsRetryableErrorFromMsg(err.Error())
			if !isCtxOverflow && !isRetryable {
				break
			}
		}
	}

	return nil, fmt.Errorf("still failed after %d retries: %w", config.MaxRetries, lastErr)
}

// wrapWithCacheBreakCheck 包装事件流，拦截 UsageEvent 提取 CacheReadTokens，
// 在流结束（收到 MessageStopEvent 或上游 channel 关闭）时调用 CheckBreakAfterResponse（B4 批次 2）。
//
// 设计要点：
// - 不阻塞主事件流：消费者仍按原速消费，事件透传到 out channel。
// - UsageEvent 可能不出现（部分 provider 不返回 usage），此时流结束不触发检测。
// - 兜底策略：channel 关闭时若已收到 usage，触发一次检测；MessageStopEvent 到达时若已有 usage 也触发一次。
// 第二次调用时基线已被第一次更新为当前 cacheRead，drop=0，不会误报。
// - goroutine 安全：响应 ctx 取消和上游 channel 关闭，不会泄漏。
// - CheckBreakAfterResponse 内部已加锁，可在多 goroutine 并发重试场景下安全调用。
//
// / T1-C 改造（吸纳审查优化 8）：
// SNAPSHOT: 原 out := make(chan Event, 16) 直接缓冲；新内部用 BackpressurePipe[Event] 包装 events，
//
//	应用背压策略（drop/block/signal），保护 UsageEvent/MessageStopEvent 不被丢弃。
//	BackpressurePipe.Run goroutine 在 events channel 关闭或 ctx 取消时退出，无 goroutine 泄漏。
func wrapWithCacheBreakCheck(ctx context.Context, events <-chan Event, ci *CachingIntegration) <-chan Event {
	// BackpressurePipe 包装 events，应用背压策略保护 UsageEvent 不被丢弃
	pipe := NewBackpressurePipe[Event](events, DefaultBackpressureConfig(), isProtectedEvent)
	pipeOut := pipe.Run(ctx)

	// interceptedOut 是最终返回给调用方的 channel，拦截 UsageEvent 用于 cache break 检测
	// 缓冲 16 与原实现保持一致，避免消费者偶发慢导致 BackpressurePipe 触发背压
	interceptedOut := make(chan Event, 16)
	go func() {
		defer close(interceptedOut)
		var lastCacheRead int
		var hasUsage bool
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-pipeOut:
				if !ok {
					// pipeOut 关闭（events 关闭或 ctx 取消），触发兜底检测
					// 兜底：部分 provider 在 MessageStop 后才发 Usage
					if hasUsage {
						ci.CheckBreakAfterResponse(lastCacheRead)
					}
					return
				}
				// 透传事件给消费者（ctx 取消时放弃，避免阻塞导致 goroutine 泄漏）
				select {
				case interceptedOut <- evt:
				case <-ctx.Done():
					return
				}
				// 拦截 UsageEvent 提取 CacheRead
				if ue, isUsage := evt.(UsageEvent); isUsage {
					lastCacheRead = ue.CacheRead
					hasUsage = true
				}
				// 收到 MessageStopEvent 也触发检测（部分 provider 在 stop 前已发 usage）
				if _, isStop := evt.(MessageStopEvent); isStop && hasUsage {
					ci.CheckBreakAfterResponse(lastCacheRead)
				}
			}
		}
	}()
	return interceptedOut
}

// isProtectedEvent 判断 Event 是否为受保护事件（永不丢弃，吸纳审查优化 6）。
// 受保护事件：
// - UsageEvent（token 用量记录，丢失会导致 UsageTracker 无法记录）
// - MessageStopEvent（流结束信号，丢失会导致消费者无法感知流结束）
// - ErrorEvent（错误事件，丢失会导致错误被吞）
// - ToolUseEvent（工具调用事件，丢失会导致工具状态歧义）
// - StructuredOutputEvent（结构化输出，丢失会导致结果不完整）
func isProtectedEvent(evt Event) bool {
	switch evt.(type) {
	case UsageEvent, MessageStopEvent, ErrorEvent, ToolUseEvent, StructuredOutputEvent:
		return true
	}
	return false
}
