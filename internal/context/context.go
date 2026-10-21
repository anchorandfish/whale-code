// Package context — context budget, compaction, recovery
package context

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// RecoveryBudgetTokens is the minimum context space to preserve after compaction
const RecoveryBudgetTokens = 50000

// DefaultAutoCompactBufferTokens is the dynamic buffer reserved before auto-compact triggers.
// auto-compact fires when estimatedTokens >= contextWindow - AutoCompactBufferTokens.
//
// P0-13 (FACT-ERR-004): replaces the fixed CompactPercent=0.85 threshold so the
// trigger adapts to different model context windows (e.g. 8K vs 200K). The 13000
// buffer covers response tokens + system prompt + tool definitions.
// Reference
//
// H8 ()：原 const AutoCompactBufferTokens 改名为 DefaultAutoCompactBufferTokens，
// 避免与 Budget struct 字段 AutoCompactBufferTokens 同名混淆（P2-4 修订）。
// 运行时实际值通过 Budget.AutoCompactBufferTokens 字段读取（NewBudgetFromConfig 注入）。
const DefaultAutoCompactBufferTokens = 13000

// DefaultCompactPercent 是 ShouldCompact 提示线（用户决策窗口）的默认百分比。
// 压缩体系重构：0.85 → 0.80。理由：现状 0.85 → 0.898（主力线）→ 0.97
// 三线间隔太窄（128K 下 85%~90% 仅约 5K tokens，一两轮就穿过），用户决策窗口形同虚设；
// 拉前到 0.80 后，0.80~0.90 之间约有 12K tokens 的真实决策窗口。
// 注意：CompactPercent 只驱动提示线（CheckState 的 StateShouldCompact），
// 不再驱动自动压缩触发（由 CompactThreshold 的 window-buffer 线承担，
// 仅在小窗口 window-buffer <= 0 时作为 CompactThreshold 的回退）。
const DefaultCompactPercent = 0.80

// DefaultBlockPercent 是 97% 兜底线（硬停，不再尝试压缩）的默认百分比。
// 原硬编码在 DefaultBudget / NewBudgetFromConfig 两处，2026-10-04 提为常量：
// 大窗口不变量（见下）需要读取它来推算「窗口到硬停线的距离」。
const DefaultBlockPercent = 0.97

// ── 大窗口不变量（2026-10-04 立，实测于 internal/context/budget_window_invariant_test.go）──
//
// auto 线（自动压缩触发）与硬停线（97%）必须满足 **auto 线 < 硬停线**：
// CheckState 先判 StateBlocked，而 checkAutoCompact（tui/commands_session.go）与该判定
// 之后的 autoCompactDone（tui/blocked_handoff.go autoCompactDue）都会在 Blocked 上短路
// ⇒ 一旦 auto 线越过硬停线，**自动压缩永不触发，直接硬停**。
//
// 历史缺陷：auto 线用绝对减法 window−13000、硬停线用比例 0.97×window，
// 两者在 window ≈ 433,333 处交叉。默认 131072 一直掩盖它；任何 >433K 窗口都会踩。
//
// 修法（本批）：让「默认值」随窗口自适应 —— 显式覆盖这两个字段的调用方行为完全不变。
// - 缓冲至少容纳「窗口到硬停线的距离 ×2」⇒ auto 线恒定落在约 94% 处
// - 压缩后保留至少「窗口的 1/8」⇒ 压缩余量不再恒定 37000

// Feature Flag environment variables (P0-1 / X1, P0-2 / X2, H13-G1).
//
// Flag default policy:
// - EnvPipeline (X1): N11 修复 G9，默认 ENABLED（生产启用 5 层管线）。
// 通过 WHALE_CTX_PIPELINE=0 可显式关闭回退旧路径。
// 历史背景：P0-1 阶段为安全灰度默认 DISABLED，N11 完成跨方向契约对接
// （cost/error/cache）后改为默认 ENABLED。
// - EnvReactiveCompact (X2): default ENABLED. Per spec (02-implementation-
// plan.md line 46: "受 WHALE_CTX_REACTIVE_COMPACT env 门控（默认 true）"),
// reactive 413-recovery is on by default. Set to "0"/"false" to disable.
// - EnvBodyAfterPrefix (H13-G1): default ENABLED. 扣除缓存命中前缀再计 compact 阈值，
// 避免"缓存命中越多越早触发压缩"反效果。Set to "0"/"false" to disable.
const (
	// EnvPipeline enables the 5-layer CompactionPipeline (Snip→MicroCompact→
	// Collapse→AICompact→CacheAware) in handleCompact. When disabled, the old
	// single-stage MicroCompact + Compact path is used.
	EnvPipeline = "WHALE_CTX_PIPELINE"
	// EnvReactiveCompact enables the reactive 413-recovery compaction chain.
	EnvReactiveCompact = "WHALE_CTX_REACTIVE_COMPACT"
	// EnvBodyAfterPrefix enables BodyAfterPrefix scope for compact threshold
	// (H13-G1). When enabled, cached input tokens are subtracted from estimated
	// tokens before comparing to the compact threshold.
	EnvBodyAfterPrefix = "WHALE_BODY_AFTER_PREFIX"
)

// IsPipelineEnabled returns true when WHALE_CTX_PIPELINE is not explicitly
// disabled.
// N11 修复 G9：默认从 DISABLED 改为 ENABLED（生产启用 5 层管线）。
// 显式设置 "0"/"false" 可关闭回退旧路径。
// P0-1 (X1): 历史 gate 函数，N11 后改为默认启用。
func IsPipelineEnabled() bool {
	v := strings.ToLower(os.Getenv(EnvPipeline))
	if v == "" {
		// G9 修复：默认启用 5 层管线（生产启用）
		return true
	}
	return v != "0" && v != "false"
}

// IsReactiveCompactEnabled returns true unless WHALE_CTX_REACTIVE_COMPACT is
// explicitly set to "0"/"false".
// P0-2 (X2): gates the reactive 413-recovery compaction chain.
// Default: enabled (per spec — "默认 true").
func IsReactiveCompactEnabled() bool {
	v := strings.ToLower(os.Getenv(EnvReactiveCompact))
	return v != "0" && v != "false"
}

// IsBodyAfterPrefixEnabled 返回是否启用 BodyAfterPrefix scope（H13-G1）。
// 默认 ENABLED：扣除缓存命中前缀再计 compact 阈值，避免"缓存命中越多越早触发压缩"反效果。
// 显式设置 "0"/"false" 可关闭回退旧逻辑（全量 estimatedTokens 计阈值）。
func IsBodyAfterPrefixEnabled() bool {
	v := strings.ToLower(os.Getenv(EnvBodyAfterPrefix))
	if v == "" {
		return true // 默认启用
	}
	return v != "0" && v != "false"
}

// GetCachedInputTokens 从 TokenSource 获取缓存命中 token 数（H13-G1 辅助函数）。
//
// 缓存命中数据通过 TokenSource.PostCallActual(model) 返回的 UnifiedUsage.CacheRead
// 字段获取（internal/ai/token_source.go:43,67）。
//
// 边界处理：cachedInputTokens > estimatedTokens（provider 异常上报）时 clamp 到 0
// 并通过 metricsFn 回调记录 anomaly（参考现有 cache_metrics.go:43 的 anomaly 处理）。
// TokenSource 为 nil 或 model 从未调用过时返回 0（降级为全量计阈值）。
//
// 参数：
// - tokenSource: TokenSource 接口实例（可为 nil，nil 时返回 0）
// - model: 当前模型名
// - estimatedTokens: 当前估算的 token 数（用于 anomaly 检测）
// - metricsFn: anomaly 回调函数（可为 nil，nil 时跳过 anomaly 记录）
func GetCachedInputTokens(tokenSource ai.TokenSource, model string, estimatedTokens int, metricsFn func()) int {
	if tokenSource == nil {
		return 0
	}
	usage, ok := tokenSource.PostCallActual(model)
	if !ok {
		return 0
	}
	cached := usage.CacheRead
	if cached > estimatedTokens && estimatedTokens > 0 {
		// provider 异常上报：cache > total，clamp 到 0 并记录 anomaly
		if metricsFn != nil {
			metricsFn()
		}
		return 0
	}
	return cached
}

// State represents context state
type State int

const (
	StateOk            State = iota
	StateWarning             // 75% usage
	StateShouldCompact       // 80% usage（提示线：用户决策窗口）
	StateBlocked             // 97% usage（兜底：只做硬停 + 最小交接模板）
)

// String returns state string
func (s State) String() string {
	switch s {
	case StateOk:
		return "ok"
	case StateWarning:
		return "warning"
	case StateShouldCompact:
		return "should_compact"
	case StateBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// Budget is the context budget
type Budget struct {
	ContextWindow  int
	WarnPercent    float64
	CompactPercent float64
	BlockPercent   float64
	RecoveryBudget int
	// AutoCompactBufferTokens 自动压缩 token 缓冲（H8 新增，原 const 13000）
	// P0-13 后驱动 compact 触发决策：estimatedTokens >= contextWindow - AutoCompactBufferTokens
	// 零值时 CompactThreshold 会 fallback 到 DefaultAutoCompactBufferTokens（向后兼容）
	//
	// 2026-10-04：本字段是**下界**而非定值 —— CompactThreshold 经
	// effectiveAutoCompactBuffer() 取 max(本字段, 2×窗口到硬停线的距离)，
	// 以保证 auto 线恒早于硬停线（大窗口不变量）。只会被抬升，不会被压低。
	AutoCompactBufferTokens int
}

// effectiveAutoCompactBuffer 返回实际用于 auto 线计算的缓冲。
//
// 取 max(配置缓冲, 2 × ceil(window × (1 − BlockPercent)))：
// 「窗口到硬停线的距离的两倍」保证 auto 线恒定落在窗口的约 94%
// （BlockPercent=0.97 时），既保住绝对缓冲的语义（请求侧固定开销），
// 又不会在大窗口下被比例制的硬停线反超。
//
// 窗口 ≤ ~216,667 时该下界小于等于 13000 ⇒ 结果与旧实现逐值相同（现有用户零影响）。
func (b Budget) effectiveAutoCompactBuffer() int {
	buf := b.AutoCompactBufferTokens
	if buf <= 0 {
		buf = DefaultAutoCompactBufferTokens
	}
	blockPercent := b.BlockPercent
	if blockPercent <= 0 || blockPercent >= 1 {
		blockPercent = DefaultBlockPercent
	}
	gap := int(math.Ceil(float64(b.ContextWindow) * (1 - blockPercent)))
	if scaled := 2 * gap; scaled > buf {
		return scaled
	}
	return buf
}

// effectiveRecoveryBudget 返回压缩后至少要保留的空闲 token 数。
//
// 取 max(RecoveryBudget, window/8)：固定 50000 在 128K 下占窗口 38%（合理），
// 在 1M 下只占 4.8% ⇒ 压缩后余量仅约 37000 tokens，一两轮就再次越线（挤牙膏）。
//
// 窗口 ≤ 400,000 时 window/8 ≤ 50000 ⇒ 结果与旧实现逐值相同。
func (b Budget) effectiveRecoveryBudget() int {
	recovery := b.RecoveryBudget
	if scaled := b.ContextWindow / 8; scaled > recovery {
		recovery = scaled
	}
	return recovery
}

// DefaultBudget 返回默认上下文预算（向后兼容内部调用）
// 压缩体系重构：CompactPercent 0.85 → 0.80，作为提示线
// （用户决策窗口）阈值；主力自动压缩线由 CompactThreshold（window-buffer）承担。
func DefaultBudget(contextWindow int) Budget {
	return Budget{
		ContextWindow:           contextWindow,
		WarnPercent:             0.75,
		CompactPercent:          DefaultCompactPercent,
		BlockPercent:            DefaultBlockPercent,
		RecoveryBudget:          RecoveryBudgetTokens,
		AutoCompactBufferTokens: DefaultAutoCompactBufferTokens,
	}
}

// NewBudgetFromConfig 从 Config 构造 Budget（H8 新增，推荐生产代码使用）
// 优先级：cfg 字段 > DefaultAutoCompactBufferTokens fallback
// 三个百分比字段（BudgetWarnPercent/CompactPercent/BlockPercent）直接从 cfg 读取
func NewBudgetFromConfig(contextWindow int, cfg *config.Config) Budget {
	bufTokens := 0
	if cfg != nil {
		bufTokens = cfg.AutoCompactBufferTokens
	}
	if bufTokens <= 0 {
		bufTokens = DefaultAutoCompactBufferTokens
	}
	b := Budget{
		ContextWindow:           contextWindow,
		RecoveryBudget:          RecoveryBudgetTokens,
		AutoCompactBufferTokens: bufTokens,
	}
	if cfg != nil {
		b.WarnPercent = cfg.BudgetWarnPercent
		b.CompactPercent = cfg.BudgetCompactPercent
		b.BlockPercent = cfg.BudgetBlockPercent
	}
	// cfg 为 nil 时 fallback 到 DefaultBudget 的硬编码值
	if cfg == nil {
		b.WarnPercent = 0.75
		b.CompactPercent = DefaultCompactPercent
		b.BlockPercent = DefaultBlockPercent
	}
	return b
}

// CheckState 根据估算 token 数返回当前上下文状态。
//
// 压缩体系重构后的三条线：
// - 提示线 StateShouldCompact：effectiveTokens >= CompactPercent*window（默认 0.80），
// 是用户决策窗口（可提前 /compact，或等待主力线自动压缩），不再触发自动压缩判定
// - 主力自动压缩线：contextWindow - effectiveAutoCompactBuffer()（默认 13000，
// 大窗口下按不变量下界抬升；见文件头「大窗口不变量」），
// 由 ShouldAutoCompact 独立判定（P0-13 的 window-buffer 语义保留不动）
// - 兜底线 StateBlocked：usage >= BlockPercent（0.97），只做硬停 + 最小交接模板，
// 不再承担压缩职责（97% 时窗口已满，AI 压缩请求本身可能撑爆/截断）
//
// H13-G1 (BodyAfterPrefix scope): 当 cachedInputTokens > 0 且 Feature Flag
// WHALE_BODY_AFTER_PREFIX 启用时，从 estimatedTokens 中扣除缓存命中前缀再与
// compact 阈值比较，避免"缓存命中越多越早触发压缩"反效果。
// cachedInputTokens <= 0 或 Feature Flag 关闭时回退旧逻辑（全量 estimatedTokens 计阈值）。
//
// WarnPercent (0.75) and BlockPercent (0.97) remain percentage-based.
func (b Budget) CheckState(estimatedTokens, cachedInputTokens int) State {
	if b.ContextWindow <= 0 {
		return StateOk
	}
	usage := float64(estimatedTokens) / float64(b.ContextWindow)

	// H13-G1: BodyAfterPrefix scope — 扣除缓存命中前缀再计 compact 阈值
	effectiveTokens := estimatedTokens
	if IsBodyAfterPrefixEnabled() && cachedInputTokens > 0 {
		if cachedInputTokens >= estimatedTokens {
			// 边界：provider 异常上报（cache > total），clamp 到 0
			// 调用方应通过 GetCachedInputTokens 辅助函数提前 clamp + 记录 anomaly
			effectiveTokens = 0
		} else {
			effectiveTokens = estimatedTokens - cachedInputTokens
		}
	}

	switch {
	case usage >= b.BlockPercent:
		return StateBlocked
	case effectiveTokens >= b.HintThreshold():
		return StateShouldCompact
	case usage >= b.WarnPercent:
		return StateWarning
	default:
		return StateOk
	}
}

// HintThreshold 返回提示线（用户决策窗口）的绝对 token 数：
// contextWindow * CompactPercent（默认 0.80）。
// CompactPercent 非法（<=0 或 >=1）时回退 DefaultCompactPercent。
func (b Budget) HintThreshold() int {
	if b.ContextWindow <= 0 {
		return 0
	}
	percent := b.CompactPercent
	if percent <= 0 || percent >= 1 {
		percent = DefaultCompactPercent
	}
	threshold := int(float64(b.ContextWindow) * percent)
	if threshold < 1 {
		return 1
	}
	return threshold
}

// ShouldAutoCompact 报告主力自动压缩线（contextWindow - AutoCompactBufferTokens，
// 128K 下 ≈89.8%）是否已越过。这是 AI 自动压缩的触发判定，独立于提示线
// （CheckState 的 StateShouldCompact，0.80）与兜底线（StateBlocked，0.97）。
//
// H13-G1: BodyAfterPrefix 启用且 cachedInputTokens > 0 时按扣除缓存后的
// effectiveTokens 判定，与 CheckState 同口径。
// 是否在 StateBlocked 时压缩是调用方策略（97% 只做硬停，见 tui 层 autoCompactDue）。
func (b Budget) ShouldAutoCompact(estimatedTokens, cachedInputTokens int) bool {
	threshold := b.CompactThreshold()
	if threshold <= 0 {
		return false
	}
	effectiveTokens := estimatedTokens
	if IsBodyAfterPrefixEnabled() && cachedInputTokens > 0 {
		if cachedInputTokens >= estimatedTokens {
			effectiveTokens = 0
		} else {
			effectiveTokens = estimatedTokens - cachedInputTokens
		}
	}
	return effectiveTokens >= threshold
}

// MainLinePercent 返回主力自动压缩线（contextWindow - AutoCompactBufferTokens）
// 相对窗口的百分比（0-100），供 UI 展示"约 X% 将自动压缩"的决策窗口提示。
// 窗口非法（<=0）或主力线非正（极小窗口回退百分比线）时返回 CompactPercent*100。
func (b Budget) MainLinePercent() float64 {
	if b.ContextWindow <= 0 {
		return 0
	}
	threshold := b.CompactThreshold()
	if threshold <= 0 {
		threshold = b.HintThreshold()
	}
	return float64(threshold) / float64(b.ContextWindow) * 100
}

// CompactThreshold returns the absolute token count at which auto-compact triggers.
// P0-13: contextWindow - AutoCompactBufferTokens (13000).
// H8：从 b.AutoCompactBufferTokens 字段读取（原 const AutoCompactBufferTokens），
// 字段零值时 fallback 到 DefaultAutoCompactBufferTokens（向后兼容旧调用方）
//
// 2026-10-04：缓冲改经 effectiveAutoCompactBuffer() 取「配置值 vs 不变量下界」的较大者
// ⇒ auto 线恒早于硬停线（见文件头「大窗口不变量」）。窗口 ≤ ~216,667 时逐值不变。
func (b Budget) CompactThreshold() int {
	if b.ContextWindow <= 0 {
		return 0
	}
	bufTokens := b.effectiveAutoCompactBuffer()
	threshold := b.ContextWindow - bufTokens
	if threshold > 0 {
		return threshold
	}
	percent := b.CompactPercent
	if percent <= 0 || percent >= 1 {
		percent = DefaultCompactPercent
	}
	threshold = int(float64(b.ContextWindow) * percent)
	if threshold < 1 {
		return 1
	}
	return threshold
}

// PostCompactMaxTokens returns max tokens allowed after compaction.
//
// 2026-10-04：RecoveryBudget 改经 effectiveRecoveryBudget() 取「配置值 vs window/8」的较大者
// ⇒ 大窗口下压缩后留出真实余量（旧实现固定 50000，1M 下只占 4.8%）。窗口 ≤ 400,000 时逐值不变。
func (b Budget) PostCompactMaxTokens() int {
	if b.ContextWindow <= 0 {
		return 0
	}
	maxTokens := b.ContextWindow - b.effectiveRecoveryBudget()
	if maxTokens > 0 {
		return maxTokens
	}
	// A fixed 50k recovery reserve cannot fit small-context models. Keep 25%
	// free in that case instead of producing a negative candidate budget.
	maxTokens = b.ContextWindow * 3 / 4
	if maxTokens < 1 {
		return 1
	}
	return maxTokens
}

// MeetsRecoveryBudget checks if recovery budget is met after compaction
func (b Budget) MeetsRecoveryBudget(postCompactTokens int) bool {
	return postCompactTokens <= b.PostCompactMaxTokens()
}

// UsagePercent returns usage percentage
func (b Budget) UsagePercent(estimatedTokens int) float64 {
	if b.ContextWindow <= 0 {
		return 0
	}
	return float64(estimatedTokens) / float64(b.ContextWindow) * 100
}

// CompactionTrigger identifies why a compaction run started.
type CompactionTrigger string

const (
	CompactionTriggerAuto     CompactionTrigger = "auto"
	CompactionTriggerManual   CompactionTrigger = "manual"
	CompactionTriggerReactive CompactionTrigger = "reactive"
	CompactionTriggerPhase3   CompactionTrigger = "phase3"
)

// CompactionPolicy controls whether context budget state may stop the pipeline.
type CompactionPolicy int

const (
	CompactionPolicyBudgeted CompactionPolicy = iota
	CompactionPolicyForce
)

// CompactionNoOpReason is a machine-readable explanation for an unchanged run.
type CompactionNoOpReason string

const (
	CompactionNoOpNone                 CompactionNoOpReason = ""
	CompactionNoOpWithinBudget         CompactionNoOpReason = "within_budget"
	CompactionNoOpInsufficientMessages CompactionNoOpReason = "insufficient_messages"
	CompactionNoOpNoCompressibleData   CompactionNoOpReason = "no_compressible_data"
	CompactionNoOpCircuitBreaker       CompactionNoOpReason = "circuit_breaker"
	CompactionNoOpNotBeneficial        CompactionNoOpReason = "not_beneficial"
	CompactionNoOpBudgetUnachievable   CompactionNoOpReason = "budget_unachievable"
)

// ExecuteOptions describes one pipeline invocation. Execute keeps the legacy
// auto/budgeted behavior; explicit callers should use ExecuteWithOptions.
type ExecuteOptions struct {
	Trigger CompactionTrigger
	Policy  CompactionPolicy
	Config  CompactionConfig
	// CostRecorder is scoped to this invocation. It must not be installed on
	// the shared pipeline because manual and automatic compactions may overlap.
	CostRecorder *CompactCostRecorder
}

// CompactionConfig is the compaction configuration
type CompactionConfig struct {
	PreserveRecentMessages int
	MaxEstimatedTokens     int
	// SystemMessageOverheadTokens accounts for moving each persisted system
	// message into the provider system-prompt layer with a wrapper prefix.
	SystemMessageOverheadTokens int
	// PreserveUserMaxTokens caps verbatim historical user messages within the
	// complete candidate budget used by the preserve-user strategy.
	PreserveUserMaxTokens int
	Strategy              config.CompactionStrategy
	Trigger               CompactionTrigger
	// UseAISummary forces the AI stage for an explicit /compact --ai request.
	// Without this flag the pipeline may finish after the cheaper structured
	// collapse stage, which is the intended default behavior.
	UseAISummary bool
}

// DefaultCompactionConfig returns default compaction config
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		PreserveRecentMessages: 10,
		PreserveUserMaxTokens:  10000,
		Strategy:               config.CompactBalanced,
		Trigger:                CompactionTriggerAuto,
	}
}

// WithBudget applies the model-specific post-compaction recovery budget when
// the caller did not provide a stricter explicit candidate limit.
func (c CompactionConfig) WithBudget(budget Budget) CompactionConfig {
	if c.MaxEstimatedTokens <= 0 {
		c.MaxEstimatedTokens = budget.PostCompactMaxTokens()
	}
	return c
}

// WithBudgetAndOverhead reserves request input that compaction cannot remove,
// such as the static system prompt and tool definitions. MaxEstimatedTokens
// remains the limit for the compacted session transcript itself.
func (c CompactionConfig) WithBudgetAndOverhead(budget Budget, overhead int) CompactionConfig {
	limit := budget.PostCompactMaxTokens()
	if limit <= 0 {
		return c
	}
	if overhead > 0 {
		limit -= overhead
	}
	if limit < 1 {
		limit = 1
	}
	if c.MaxEstimatedTokens <= 0 || limit < c.MaxEstimatedTokens {
		c.MaxEstimatedTokens = limit
	}
	return c
}

// EffectivePreserveCount returns the number of recent messages retained by
// the selected strategy.
func (c CompactionConfig) EffectivePreserveCount() int {
	preserve := c.PreserveRecentMessages
	if preserve <= 0 {
		preserve = DefaultCompactionConfig().PreserveRecentMessages
	}
	if c.Strategy == config.CompactAggressive {
		preserve = min(preserve, 3)
	}
	return preserve
}

// PreserveUserMessages reports whether the selected strategy retains older
// user messages in addition to the structured summary.
func (c CompactionConfig) PreserveUserMessages() bool {
	return c.Strategy == config.CompactPreserveUser
}

// PreservedUserTokenBudget caps verbatim user messages retained by the
// preserve-user strategy. The summary still contains the full extracted user
// intents when this budget is exhausted.
func (c CompactionConfig) PreservedUserTokenBudget() int {
	if c.PreserveUserMaxTokens > 0 {
		return c.PreserveUserMaxTokens
	}
	return DefaultCompactionConfig().PreserveUserMaxTokens
}

// Compactor is the context compactor
type Compactor struct {
	estimator      *ai.TokenEstimator
	circuitBreaker *CircuitBreaker
}

// NewCompactor creates a compactor
func NewCompactor(estimator *ai.TokenEstimator) *Compactor {
	return &Compactor{
		estimator:      estimator,
		circuitBreaker: NewCircuitBreaker(3),
	}
}

// CompactResult is the compaction result
type CompactResult struct {
	CompactedMessages []session.Message
	RemovedCount      int
	Summary           string
	Changed           bool
	NoOpReason        CompactionNoOpReason
	PreTokens         int
	PostTokens        int
	EligibleCount     int
	PreserveCount     int
	PreMessageCount   int
	PostMessageCount  int
	// MessagesSummarized is non-zero only when unique source messages are
	// known. Multi-stage pipelines leave it zero to avoid double counting.
	MessagesSummarized int
	// FallbackLevel 是本次压缩实际使用的 fallback 级别字符串
	// （none/ai_summary/key_context/rule_extract），供 UI 展示。
	FallbackLevel string
	// StagesExecuted 是 pipeline 实际执行过的阶段列表（如 snip/micro/collapse/ai/cache_aware），
	// 旧单阶段路径为空。供用户判断走了哪条压缩路径。
	StagesExecuted []string
}

// Compact executes compaction, selects different compaction strategy based on Strategy field
func (c *Compactor) Compact(messages []session.Message, cfg CompactionConfig) *CompactResult {
	if cfg.Trigger == "" {
		cfg.Trigger = CompactionTriggerAuto
	}
	// Calculate actual preserved message count based on strategy
	effectivePreserve := cfg.EffectivePreserveCount()

	if len(messages) <= effectivePreserve {
		tokens := estimateSessionMessagesTokens(c.estimator, messages)
		return &CompactResult{
			CompactedMessages: messages,
			RemovedCount:      0,
			NoOpReason:        CompactionNoOpInsufficientMessages,
			EligibleCount:     len(messages),
			PreserveCount:     effectivePreserve,
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
			PreTokens:         tokens,
			PostTokens:        tokens,
		}
	}

	if c.circuitBreaker.IsOpen() {
		tokens := estimateSessionMessagesTokens(c.estimator, messages)
		return &CompactResult{
			CompactedMessages: messages,
			RemovedCount:      0,
			NoOpReason:        CompactionNoOpCircuitBreaker,
			EligibleCount:     len(messages),
			PreserveCount:     effectivePreserve,
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
			PreTokens:         tokens,
			PostTokens:        tokens,
		}
	}

	preserved := messages[len(messages)-effectivePreserve:]
	compacted := messages[:len(messages)-effectivePreserve]

	summary := c.generateSummary(compacted)

	summaryMsg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: summary},
		},
	}

	var result []session.Message

	switch cfg.Strategy {
	case config.CompactPreserveUser:
		// preserve_user keeps old user turns first, but applies a deterministic
		// token cap. Reserve room for the summary and recent messages before
		// spending that cap on verbatim history; otherwise a long user turn can
		// consume the entire final-candidate budget by itself.
		userBudget := cfg.PreservedUserTokenBudget()
		if cfg.MaxEstimatedTokens > 0 {
			fixed := make([]session.Message, 0, 1+len(preserved))
			fixed = append(fixed, summaryMsg)
			fixed = append(fixed, preserved...)
			remaining := cfg.MaxEstimatedTokens - estimateSessionMessagesTokens(c.estimator, fixed)
			if remaining < userBudget {
				userBudget = max(remaining, 0)
			}
		}
		// The summary still contains extracted intents when a turn is truncated
		// or omitted.
		userMsgs := c.extractUserMessages(compacted, userBudget)
		result = make([]session.Message, 0, 1+len(userMsgs)+len(preserved))
		result = append(result, summaryMsg)
		result = append(result, userMsgs...)
		result = append(result, preserved...)
	default:
		// balanced and aggressive strategies: summary + preserve recent messages
		result = make([]session.Message, 0, 1+len(preserved))
		result = append(result, summaryMsg)
		result = append(result, preserved...)
	}

	c.circuitBreaker.RecordSuccess()
	preTokens := estimateSessionMessagesTokens(c.estimator, messages)
	postTokens := estimateSessionMessagesTokens(c.estimator, result)
	if cfg.MaxEstimatedTokens > 0 && postTokens > cfg.MaxEstimatedTokens {
		return &CompactResult{
			CompactedMessages: messages,
			NoOpReason:        CompactionNoOpBudgetUnachievable,
			PreTokens:         preTokens,
			PostTokens:        preTokens,
			EligibleCount:     len(messages),
			PreserveCount:     effectivePreserve,
			PreMessageCount:   len(messages),
			PostMessageCount:  len(messages),
		}
	}
	if len(result) > 0 {
		SetCompactBoundaryMetadata(&result[0], CompactMetadata{
			Trigger:            string(cfg.Trigger),
			PreTokens:          preTokens,
			PostTokens:         postTokens,
			MessagesSummarized: len(compacted),
		})
	}

	return &CompactResult{
		CompactedMessages:  result,
		RemovedCount:       len(compacted),
		Summary:            summary,
		Changed:            true,
		PreTokens:          preTokens,
		PostTokens:         postTokens,
		EligibleCount:      len(messages),
		PreserveCount:      effectivePreserve,
		PreMessageCount:    len(messages),
		PostMessageCount:   len(result),
		MessagesSummarized: len(compacted),
	}
}

// extractUserMessages extracts all user messages from message list
func (c *Compactor) extractUserMessages(messages []session.Message, tokenBudget int) []session.Message {
	var userMsgs []session.Message
	remaining := tokenBudget
	for _, msg := range messages {
		if msg.Role != session.RoleUser || remaining <= 0 {
			continue
		}
		cost := estimateSessionMessagesTokens(c.estimator, []session.Message{msg})
		if cost <= remaining {
			userMsgs = append(userMsgs, msg)
			remaining -= cost
			continue
		}
		// Keep the prefix of the first over-budget user message so early
		// markers (and their ordering) remain recoverable.
		copyMsg := session.CloneMessages([]session.Message{msg})[0]
		copyMsg.Blocks = nil
		for _, block := range msg.Blocks {
			candidate := copyMsg
			candidate.Blocks = append(append([]session.ContentBlock(nil), copyMsg.Blocks...), block)
			if estimateSessionMessagesTokens(c.estimator, []session.Message{candidate}) <= remaining {
				copyMsg = candidate
				continue
			}
			if block.Type == "text" {
				baseCost := estimateSessionMessagesTokens(c.estimator, []session.Message{copyMsg})
				textBudget := remaining - baseCost
				if textBudget > 0 {
					block.Text = c.estimator.TruncateToTokenLimit(block.Text, textBudget)
					candidate.Blocks = append(append([]session.ContentBlock(nil), copyMsg.Blocks...), block)
					if estimateSessionMessagesTokens(c.estimator, []session.Message{candidate}) <= remaining {
						copyMsg = candidate
					}
				}
			}
			break
		}
		if len(copyMsg.Blocks) > 0 && copyMsg.TextContent() != "" {
			userMsgs = append(userMsgs, copyMsg)
		}
		break
	}
	return userMsgs
}

// generateSummary generates compaction summary
func (c *Compactor) generateSummary(messages []session.Message) string {
	var parts []string
	parts = append(parts, "[Compaction Summary]")

	userCount := 0
	assistantCount := 0
	toolCount := 0
	var keyFiles []string
	var userIntents []string
	var keyDecisions []string

	for _, msg := range messages {
		switch msg.Role {
		case session.RoleUser:
			userCount++
			for _, block := range msg.Blocks {
				if block.Type == "text" && block.Text != "" {
					intent := truncateRunes(strings.TrimSpace(block.Text), 80)
					if intent != "" && len(userIntents) < 10 {
						userIntents = append(userIntents, intent)
					}
				}
			}
		case session.RoleAssistant:
			assistantCount++
			for _, block := range msg.Blocks {
				if block.Type == "tool_use" {
					toolCount++
				}
				if block.Type == "text" && block.Text != "" {
					text := block.Text
					if strings.Contains(text, "decided") || strings.Contains(text, "chose") ||
						strings.Contains(text, "approach") || strings.Contains(text, "solution") ||
						strings.Contains(text, "decision") || strings.Contains(text, "chose") {
						decision := truncateRunes(strings.TrimSpace(text), 100)
						if len(keyDecisions) < 5 {
							keyDecisions = append(keyDecisions, decision)
						}
					}
				}
			}
		case session.RoleTool:
			toolCount++
		}

		for _, block := range msg.Blocks {
			if block.Type == "text" {
				extractPaths(block.Text, &keyFiles)
			}
		}
	}

	parts = append(parts, fmt.Sprintf("%d user messages, %d assistant messages, %d tool calls total",
		userCount, assistantCount, toolCount))

	if len(userIntents) > 0 {
		parts = append(parts, "\nUser intent:")
		for i, intent := range userIntents {
			parts = append(parts, fmt.Sprintf("  %d. %s", i+1, intent))
		}
	}

	if len(keyDecisions) > 0 {
		parts = append(parts, "\nKey decisions:")
		for _, d := range keyDecisions {
			parts = append(parts, fmt.Sprintf("  - %s", d))
		}
	}

	if len(keyFiles) > 0 {
		uniqueFiles := uniqueStrings(keyFiles)
		if len(uniqueFiles) > 10 {
			uniqueFiles = uniqueFiles[:10]
		}
		parts = append(parts, fmt.Sprintf("\nFiles involved: %s", strings.Join(uniqueFiles, ", ")))
	}

	return strings.Join(parts, "\n")
}

// truncateRunes truncate string by rune count
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// extractPaths extracts file paths from text (heuristic)
func extractPaths(text string, paths *[]string) {
	words := strings.Fields(text)
	for _, w := range words {
		if len(w) > 3 && (strings.Contains(w, "/") || strings.Contains(w, "\\")) &&
			(strings.Contains(w, ".go") || strings.Contains(w, ".rs") ||
				strings.Contains(w, ".py") || strings.Contains(w, ".ts") ||
				strings.Contains(w, ".js") || strings.Contains(w, ".md") ||
				strings.Contains(w, ".json") || strings.Contains(w, ".yaml") ||
				strings.Contains(w, ".toml")) {
			*paths = append(*paths, w)
		}
	}
}

// CircuitBreaker is a circuit breaker with half-open state support
//
// P0-6 (X6) 并发保护：所有状态读写均通过 mu sync.Mutex 加锁，
// 避免 IsOpen 在读操作中修改 halfOpen 导致的数据竞争（FACT-ERR-005/FACT-CONC-001）。
type CircuitBreaker struct {
	mu           sync.Mutex // P0-6: 保护 failureCount/isOpen/halfOpen/lastFailure
	failureCount int
	threshold    int
	isOpen       bool
	halfOpen     bool
	lastFailure  time.Time
	cooldown     time.Duration
}

// NewCircuitBreaker creates a circuit breaker
func NewCircuitBreaker(threshold int) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		cooldown:  30 * time.Second,
	}
}

// RecordFailure records a failure and transitions to open if threshold is reached
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failureCount++
	cb.lastFailure = time.Now()
	cb.halfOpen = false
	if cb.failureCount >= cb.threshold {
		cb.isOpen = true
	}
}

// RecordSuccess resets the circuit breaker to closed state
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failureCount = 0
	cb.isOpen = false
	cb.halfOpen = false
}

// IsOpen checks if the circuit breaker is open, transitioning to half-open after cooldown
func (cb *CircuitBreaker) IsOpen() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.isOpen && !cb.halfOpen && time.Since(cb.lastFailure) >= cb.cooldown {
		cb.halfOpen = true
	}
	return cb.isOpen && !cb.halfOpen
}

// AllowProbe returns true only in half-open state, allowing a single probe request
func (cb *CircuitBreaker) AllowProbe() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.halfOpen {
		cb.halfOpen = false
		return true
	}
	return false
}

// WarningMessage 返回断路器告警消息（供 TUI 显示）（N11 修复 G10）。
//
// 契约：当断路器打开（连续失败达阈值且冷却期内）时返回用户可见告警，
// 提示用户 compact 已暂停，建议手动 /compact 或重启会话。
// 借鉴同类 CLI MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES=3
// 的静默停止机制，whale-code 在静默停止的同时向用户显示告警。
//
// 返回空字符串表示断路器未打开（无需告警）。
func (cb *CircuitBreaker) WarningMessage() string {
	if !cb.IsOpen() {
		return ""
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return fmt.Sprintf("compact 断路器已打开（连续失败 %d 次），自动 compact 已暂停。建议手动 /compact 或重启会话。", cb.failureCount)
}

// TruncationRecovery outputtruncation recovery
//
// P0-6 (X6) 并发保护：RemainingAttempts 读写通过 mu sync.Mutex 加锁，
// 避免 ShouldRetry/Consume/Reset 并发调用时的数据竞争（FACT-CONC-001）。
type TruncationRecovery struct {
	mu                sync.Mutex // P0-6: 保护 RemainingAttempts
	RemainingAttempts int
	MaxAttempts       int
}

// NewTruncationRecovery createtruncation recovery
func NewTruncationRecovery() *TruncationRecovery {
	return &TruncationRecovery{
		MaxAttempts:       3,
		RemainingAttempts: 3,
	}
}

// ShouldRetry determines whether to continue retrying
func (r *TruncationRecovery) ShouldRetry() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.RemainingAttempts > 0
}

// Consume consumes one recovery opportunity
func (r *TruncationRecovery) Consume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RemainingAttempts--
}

// Reset reset
func (r *TruncationRecovery) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RemainingAttempts = r.MaxAttempts
}

// HasUnclockedCodeBlock checks if there are unclosed code blocks
func HasUnclosedCodeBlock(text string) bool {
	count := strings.Count(text, "```")
	return count%2 != 0
}

// TruncationRecoveryMessage truncation recoveryprompt
const TruncationRecoveryMessage = "Your previous response was truncated. Please continue output from the truncation point, do not repeat already output content。"

// PipelineResult is the compaction pipeline execution result
// modeled after a comparable CLI 5-layer compaction pipeline: Snip -> MicroCompact -> Collapse -> AICompact -> CacheAware
type PipelineResult struct {
	// Messages is the post-compaction message list
	Messages []session.Message
	// TotalRemoved is the total count of removed messages
	TotalRemoved int
	// Changed reports any semantic reduction, including same-count micro compaction.
	Changed bool
	// StagesExecuted is the list of executed stages
	StagesExecuted []string
	// Summary is the final summary text
	Summary string
	// NoOpReason explains why an unchanged pipeline run stopped.
	NoOpReason       CompactionNoOpReason
	PreTokens        int
	PostTokens       int
	PreMessageCount  int
	PostMessageCount int
	// EligibleCount excludes system/boundary messages from the preserve threshold.
	EligibleCount      int
	PreserveCount      int
	MessagesSummarized int
}

type compactionCaching interface {
	NotifyCacheDeletion()
	NotifyCompaction()
	GetCapability() ai.CacheCapability
}

// CompactionPipeline is the compaction pipeline orchestrator
// modeled after a comparable CLI compaction pipeline: executes 5 compaction layers in order, checks if compaction is still needed after each
// pipeline order: Snip -> MicroCompact -> Collapse -> AICompact -> CacheAware
//
// N11 修复 G3：新增 cachingIntegration 字段，pipeline 在 stage 2 前调
// NotifyCacheDeletion，结束后调 NotifyCompaction + InjectCacheControlMarkers
// 重建 cache（02 §2.1.3 契约，复用 N3 机制）。
type CompactionPipeline struct {
	mu          sync.Mutex // serializes compaction to prevent concurrent file writes
	snip        *SnipCompactor
	microConfig MicroCompactConfig
	compactor   *Compactor
	collapser   *ContextCollapser
	aiCompactor *AICompactor
	cacheAware  *CacheAwareCompactor
	estimator   *ai.TokenEstimator
	collapseMgr *ContextCollapseManager

	// cachingIntegration 桥接 cache 重建到 N3（N11 修复 G3）。
	// 为 nil 时跳过 NotifyCacheDeletion/NotifyCompaction + 日志告警（降级路径）。
	cachingIntegration compactionCaching
}

// NewCompactionPipeline creates a compaction pipeline orchestrator
func NewCompactionPipeline(estimator *ai.TokenEstimator, aiClient ai.Client) *CompactionPipeline {
	return &CompactionPipeline{
		snip:        NewSnipCompactor(estimator),
		microConfig: DefaultMicroCompactConfig(),
		compactor:   NewCompactor(estimator),
		collapser:   NewContextCollapser(),
		aiCompactor: NewAICompactor(aiClient, estimator),
		cacheAware:  NewCacheAwareCompactor(estimator),
		estimator:   estimator,
	}
}

// NewCompactionPipelineNoAI creates a compaction pipeline without AI client (for no-API scenarios)
func NewCompactionPipelineNoAI(estimator *ai.TokenEstimator) *CompactionPipeline {
	return &CompactionPipeline{
		snip:        NewSnipCompactor(estimator),
		microConfig: DefaultMicroCompactConfig(),
		compactor:   NewCompactor(estimator),
		collapser:   NewContextCollapser(),
		cacheAware:  NewCacheAwareCompactor(estimator),
		estimator:   estimator,
	}
}

// SetCollapseManager sets the collapse manager
func (p *CompactionPipeline) SetCollapseManager(mgr *ContextCollapseManager) {
	p.collapseMgr = mgr
}

// SetCacheBreakHandler 注入 CacheBreakHandler 到内部 CacheAwareCompactor（方案 D）。
// 注入后 CompactWithCacheInfoInternal 入口会检查 IsCacheBroken，断裂时降级为普通压缩。
// 为 nil 时跳过检查（向后兼容）。
func (p *CompactionPipeline) SetCacheBreakHandler(h *CacheBreakHandler) {
	if p.cacheAware != nil {
		p.cacheAware.SetBreakHandler(h)
	}
}

// SetCachingIntegration 注入 N3 CachingIntegration（N11 修复 G3）。
//
// 契约：02 §2.1.3 要求 compact 后 cache 重建复用 N3 的 CacheBreakEvent 机制
// （N11 仅订阅消费，不发布 CacheBreakEvent）。
//
// 注入后 pipeline.Execute 行为：
// 1. stage 2（micro_compact）前调 NotifyCacheDeletion（删除 tool_result 前通知）
// 2. pipeline 结束后调 NotifyCompaction（重置 cache read baseline，避免误报断裂）
//
// 为 nil 时跳过 cache 重建 + 日志告警（降级路径，不阻塞 compact）。
func (p *CompactionPipeline) SetCachingIntegration(ci *ai.CachingIntegration) {
	p.cachingIntegration = ci
}

// maybeTriggerAutoCompact 检查是否应触发自动 compact（N11 修复 §3.14 / D2 修复）。
//
// 递归守卫：若当前 ctx 已标记为 compact 调用（AgentID="compact"），跳过 autocompact，
// 避免 compact→AI→413→compact 递归循环。借鉴同类 CLI
// querySource='compact' 隔离机制。
//
// 行为规约（改进方案 §3.14）：
// - tools.ExtractAgentID(ctx) == "compact" → return false（compact 调用内不触发）
// - token 未超阈值 → return false
// - token 超阈值且非 compact ctx → return true
//
// 仅 autocompact 路径检查递归守卫；手动 /compact 命令和 reactive compact（413 恢复）
// 不受守卫拦截（用户显式触发或错误恢复必须执行）。
//
// 注意：当前 autocompact 触发点在 TUI 层（checkAutoCompact），该方法为 pipeline 层
// 提供守卫能力，供未来 autocompact 路径接入或 TUI 层调用前检查。
func (p *CompactionPipeline) maybeTriggerAutoCompact(ctx context.Context, messages []session.Message, budget Budget) bool {
	// 递归守卫：compact 调用内不触发 autocompact（D2 修复，N1 合入后可用）
	if id := tools.ExtractAgentID(ctx); id == compactAgentID {
		return false
	}

	// token 阈值检查：主力线（window-buffer）越过即触发；
	// 提示线（0.80）与 Blocked（0.97 硬停）不在此判定范围内
	estimated := p.estimateMessagesTokens(messages)
	return budget.ShouldAutoCompact(estimated, 0)
}

// CircuitBreakerWarning 返回 pipeline 内部 AICompactor 断路器的告警消息
// （N11 修复 G10，供 TUI 显示）。
//
// 返回空字符串表示断路器未打开（无需告警）。
// 当 pipeline 未配置 AICompactor（NewCompactionPipelineNoAI 路径）时返回空。
func (p *CompactionPipeline) CircuitBreakerWarning() string {
	if p.aiCompactor == nil || p.aiCompactor.circuitBreaker == nil {
		return ""
	}
	return p.aiCompactor.circuitBreaker.WarningMessage()
}

// Execute executes the compaction pipeline in order
// budget: contextbudget
// cacheInfo: API-returned cache info (can be nil)
// modeled after a comparable CLI 5-layer compaction pipeline
//
// N11 修复（G3 + G5 + G6）：
// - G6：stage 1 改用 SnipWithBudget（按权重贪心选择，P1-3）
// - G3 + G5：stage 2 前调 NotifyCacheDeletion；stage 2 改用 CacheAwareMicroCompact（P1-5）；
// pipeline 结束后调 NotifyCompaction + InjectCacheControlMarkers 重建 cache
// （02 §2.1.3 契约，复用 N3 机制）
func normalizeExecuteOptions(opts ExecuteOptions) ExecuteOptions {
	defaults := DefaultCompactionConfig()
	if opts.Trigger == "" {
		opts.Trigger = CompactionTriggerAuto
	}
	if opts.Config.PreserveRecentMessages <= 0 {
		opts.Config.PreserveRecentMessages = defaults.PreserveRecentMessages
	}
	if opts.Config.Strategy == "" {
		opts.Config.Strategy = defaults.Strategy
	}
	opts.Config.Trigger = opts.Trigger
	return opts
}

func countCompactionEligible(messages []session.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != session.RoleSystem && !msg.Tombstoned {
			count++
		}
	}
	return count
}

func countSystemMessages(messages []session.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == session.RoleSystem {
			count++
		}
	}
	return count
}

func (p *CompactionPipeline) Execute(
	messages []session.Message,
	budget Budget,
	cacheInfo *ai.CacheInfo,
) *PipelineResult {
	return p.ExecuteWithOptions(messages, budget, cacheInfo, ExecuteOptions{
		Trigger: CompactionTriggerAuto,
		Policy:  CompactionPolicyBudgeted,
		Config:  DefaultCompactionConfig(),
	})
}

// ExecuteWithOptions runs the pipeline with an explicit trigger and budget
// policy. Manual and recovery callers use Force; automatic callers remain
// budget-gated.
func (p *CompactionPipeline) ExecuteWithOptions(
	messages []session.Message,
	budget Budget,
	cacheInfo *ai.CacheInfo,
	opts ExecuteOptions,
) (result *PipelineResult) {
	opts = normalizeExecuteOptions(opts)
	opts.Config = opts.Config.WithBudget(budget)
	// 性能计时点（路径3-上下文压缩）：压缩开始。
	// 在获取锁之前记录 start + 输入 token 数，避免影响锁持有时间。
	// 仅 DEBUG 级别输出，默认 Info 级别下被过滤。
	compactStart := time.Now()
	inputTokens := p.estimateMessagesTokens(messages)
	log.Debug("[perf] context: compact start",
		"phase", "compact_start",
		"input_tokens", inputTokens,
		"input_msgs", len(messages),
	)
	// 性能计时点：压缩完成（defer LIFO 在 Unlock 之后执行，锁外记录减少锁持有时间）。
	defer func() {
		if result == nil {
			return
		}
		outputTokens := p.estimateMessagesTokens(result.Messages)
		log.Debug("[perf] context: compact done",
			"phase", "compact_done",
			"elapsed_ms", time.Since(compactStart).Milliseconds(),
			"input_tokens", inputTokens,
			"output_tokens", outputTokens,
			"removed_msgs", len(messages)-len(result.Messages),
			"stages", len(result.StagesExecuted),
		)
	}()

	// Serialize compaction operations to prevent concurrent file writes
	p.mu.Lock()
	defer p.mu.Unlock()

	result = &PipelineResult{
		Messages:        session.CloneMessages(messages),
		PreTokens:       inputTokens,
		PreMessageCount: len(messages),
		PreserveCount:   opts.Config.EffectivePreserveCount(),
		EligibleCount:   countCompactionEligible(messages),
	}
	// Candidate transforms must not alias the live transcript.
	current := session.CloneMessages(messages)
	finish := func(reason CompactionNoOpReason) *PipelineResult {
		candidatePost := p.estimateMessagesTokens(current)
		noTokenBenefit := candidatePost > inputTokens || (candidatePost == inputTokens && len(current) >= len(messages))
		if opts.Policy == CompactionPolicyBudgeted && result.Changed && noTokenBenefit {
			// Automatic compaction must not grow the context. Reject the
			// candidate before cache notifications or session mutation.
			current = session.CloneMessages(messages)
			result.Changed = false
			result.TotalRemoved = 0
			result.StagesExecuted = nil
			reason = CompactionNoOpNotBeneficial
		}
		if result.Changed {
			result.NoOpReason = CompactionNoOpNone
		} else {
			result.NoOpReason = reason
		}
		result.Messages = current
		result.PostTokens = p.estimateMessagesTokens(current)
		result.PostMessageCount = len(current)
		result.EligibleCount = countCompactionEligible(current)
		return result
	}
	shouldFinishAtState := func(state State) bool {
		withinFinalBudget := opts.Config.MaxEstimatedTokens <= 0 ||
			estimateCompactCandidateTokens(p.estimator, current, opts.Config) <= opts.Config.MaxEstimatedTokens
		return state == StateOk && withinFinalBudget && (opts.Policy == CompactionPolicyBudgeted || result.Changed)
	}

	// step 0: compact_boundary slicing (P1-1) — if a prior compaction left a
	// compact_boundary marker, slice the conversation to start from that
	// boundary. This avoids re-compacting an already-summarized prefix:
	// the boundary message IS the summary of everything before it, so only
	// the messages after it need further compaction.
	if idx := FindLastCompactBoundaryIndex(current); idx > 0 {
		removed := idx
		current = current[idx:]
		result.TotalRemoved += removed
		result.Changed = true
		result.StagesExecuted = append(result.StagesExecuted, "compact_boundary_slice")
	}

	// Collapse staging only inhibits automatic, budgeted compaction. Explicit
	// manual/recovery requests must still be able to make progress.
	if opts.Policy == CompactionPolicyBudgeted && p.collapseMgr != nil && p.collapseMgr.IsEnabled() && !p.collapseMgr.ShouldAutoCompact() {
		// Existing staged collapses may be projected, but candidate preparation
		// never creates, drains, or resets collapse-manager state.
		projected := p.collapseMgr.ProjectView(current)
		if len(projected) < len(current) {
			result.TotalRemoved += len(current) - len(projected)
			current = session.CloneMessages(projected)
			result.Changed = true
			result.StagesExecuted = append(result.StagesExecuted, "collapse_projection")
			return finish(CompactionNoOpNone)
		}
		// Blocking stagecontinueexecutecompact
	}

	// stage 1: Snip - remove zombie messages
	// N11 修复 G6（P1-3）：从 Snip 改为 SnipWithBudget，按权重贪心选择释放 token。
	// maxTokens = contextWindow - recoveryBudget，超预算时低权重消息被丢弃。
	// budget.ContextWindow <= 0 时降级为普通 Snip（向后兼容）。
	var snipResult *SnipResult
	if budget.ContextWindow > 0 && budget.RecoveryBudget > 0 {
		maxTokens := opts.Config.MaxEstimatedTokens
		if maxTokens <= 0 {
			maxTokens = budget.PostCompactMaxTokens()
		}
		snipResult = p.snip.SnipWithBudget(current, maxTokens)
	} else {
		// 降级：budget 未初始化（如 ReactiveCompactor 调用时传空 Budget），用普通 Snip
		snipResult = p.snip.Snip(current)
	}
	if snipResult.RemovedCount > 0 {
		current = snipResult.Messages
		result.TotalRemoved += snipResult.RemovedCount
		result.Changed = true
		result.StagesExecuted = append(result.StagesExecuted, "snip")
	}

	// check if compaction is still needed
	estimatedTokens := p.estimateMessagesTokens(current)
	// H13-G1: BodyAfterPrefix scope — 使用 cacheInfo 中的 CacheReadTokens 作为 cachedInputTokens
	cachedTokens := 0
	if cacheInfo != nil {
		cachedTokens = cacheInfo.CacheReadTokens
	}
	state := budget.CheckState(estimatedTokens, cachedTokens)
	if !opts.Config.UseAISummary && shouldFinishAtState(state) {
		reason := CompactionNoOpNone
		if !result.Changed {
			reason = CompactionNoOpWithinBudget
		}
		return finish(reason)
	}

	// stage 2: MicroCompact - truncate old tool results
	// N11 修复 G5（P1-5）：从 MicroCompact 改为 CacheAwareMicroCompact，
	// cache 有效期内仅 compact cache boundary 之后的消息，保护 cache 前缀。
	// cacheInfo 为 nil 或 lastCacheTime 为零值时降级为普通 MicroCompact。
	microResult := p.cacheAwareMicroCompact(current, cacheInfo)
	if !messagesSliceEqual(current, microResult) {
		current = microResult
		result.Changed = true
		result.StagesExecuted = append(result.StagesExecuted, "micro_compact")
	}

	// check again
	estimatedTokens = p.estimateMessagesTokens(current)
	state = budget.CheckState(estimatedTokens, cachedTokens)
	if !opts.Config.UseAISummary && shouldFinishAtState(state) {
		reason := CompactionNoOpNone
		if !result.Changed {
			reason = CompactionNoOpWithinBudget
		}
		return finish(reason)
	}

	// A forced run still performs Snip/MicroCompact for short conversations.
	// Only after both stages are unchanged may message count make it a no-op.
	eligibleCount := countCompactionEligible(current)
	if opts.Policy == CompactionPolicyForce && !result.Changed && state == StateOk && eligibleCount <= result.PreserveCount {
		return finish(CompactionNoOpInsufficientMessages)
	}

	// stage 3: Collapse - collapse compaction (preserving markers)
	//
	// P0 修复（问题记录 §1/§2）：UseAISummary（/compact --ai）时跳过规则折叠，
	// 让 AI 层拿到原始消息而非折叠后列表，避免 SECRET-CODE 类信息先被 Collapse
	// 摘要吞掉、AI 再二次压缩导致的信息丢失。AI 层自身有 L2/L3 规则降级，
	// 跳过 Collapse 不会丢失兜底能力。
	if !opts.Config.UseAISummary {
		systemCount := countSystemMessages(current)
		collapsePreTokens := p.estimateMessagesTokens(current)
		var collapseResult *CollapseResult
		if opts.Config.Strategy == config.CompactPreserveUser && p.compactor != nil {
			heuristic := p.compactor.Compact(current, opts.Config)
			collapseResult = &CollapseResult{
				PreservedMessages: heuristic.CompactedMessages,
				CollapsedSummary:  heuristic.Summary,
				RemovedCount:      heuristic.RemovedCount,
			}
		} else {
			collapseResult = p.collapser.CollapseWithCacheBoundary(current, result.PreserveCount)
		}
		if collapseResult.RemovedCount > 0 {
			current = collapseResult.PreservedMessages
			result.TotalRemoved += collapseResult.RemovedCount
			result.MessagesSummarized = collapseResult.RemovedCount
			result.Changed = true
			result.StagesExecuted = append(result.StagesExecuted, "collapse")
			result.Summary = collapseResult.CollapsedSummary
			if systemCount >= 0 && systemCount < len(current) {
				SetCompactBoundaryMetadata(&current[systemCount], CompactMetadata{
					Trigger:            string(opts.Trigger),
					PreTokens:          collapsePreTokens,
					PostTokens:         p.estimateMessagesTokens(current),
					MessagesSummarized: collapseResult.RemovedCount,
				})
			}
		}

		// check again
		estimatedTokens = p.estimateMessagesTokens(current)
		state = budget.CheckState(estimatedTokens, cachedTokens)
		if shouldFinishAtState(state) {
			return finish(CompactionNoOpNone)
		}
	}

	// stage 4: AICompact - AI generates summary (most expensive, executed last)
	if p.aiCompactor != nil {
		aiConfig := opts.Config
		if aiConfig.MaxEstimatedTokens > 0 && aiConfig.SystemMessageOverheadTokens > 0 {
			aiConfig.MaxEstimatedTokens -= aiConfig.SystemMessageOverheadTokens
			if aiConfig.MaxEstimatedTokens < 1 {
				aiConfig.MaxEstimatedTokens = 1
			}
		}
		aiResult := p.aiCompactor.CompactWithCostRecorder(current, aiConfig, opts.CostRecorder)
		if aiResult.RemovedCount > 0 {
			current = aiResult.CompactedMessages
			result.TotalRemoved += aiResult.RemovedCount
			// A second summarization stage breaks the one-to-one source count.
			result.MessagesSummarized = 0
			result.Changed = true
			result.StagesExecuted = append(result.StagesExecuted, "ai_compact")
			// P0 修复（问题记录 §1）：AI 摘要语义保真度高于规则折叠摘要，
			// AI 层实际执行时应无条件覆盖显示摘要（原逻辑"Summary 为空才用"
			// 导致 AI 摘要被 Collapse 摘要静默丢弃，用户永远看不到 AI 输出）。
			if aiResult.Summary != "" {
				result.Summary = aiResult.Summary
			}
		}
	}

	// stage 5: CacheAware - cache-aware decision
	if cacheInfo != nil && cacheInfo.HasCacheInfo() {
		usagePercent := budget.UsagePercent(p.estimateMessagesTokens(current))
		if p.cacheAware.ShouldCompactBasedOnCache(cacheInfo, float64(usagePercent)/100.0) {
			cfg := opts.Config
			cacheResult, didCompact := p.cacheAware.CompactWithCacheInfo(
				current, cfg, cacheInfo, float64(usagePercent)/100.0,
			)
			if didCompact && cacheResult.RemovedCount > 0 {
				current = cacheResult.CompactedMessages
				result.TotalRemoved += cacheResult.RemovedCount
				result.MessagesSummarized = 0
				result.Changed = true
				result.StagesExecuted = append(result.StagesExecuted, "cache_aware")
			}
		}
	}

	reason := CompactionNoOpNoCompressibleData
	if !result.Changed && countCompactionEligible(current) <= result.PreserveCount {
		reason = CompactionNoOpInsufficientMessages
	}
	return finish(reason)
}

// PrepareCommit returns a deep-copied candidate with cache_control markers.
// It does not notify cache state and is safe to discard after a failed budget
// check or persistence conflict.
//
// 契约：02 §2.1.3 要求 compact 后 cache 重建复用 N3 机制：
// 1. CachingIntegration.NotifyCompaction()：重置 cache read baseline，避免
// compact 后 cache read 下降被误判为断裂（仅订阅消费，不发布 CacheBreakEvent）
// 2. CacheAwareCompactor.InjectCacheControlMarkers()：在 boundary 消息上重建
// ephemeral cache_control 标记，使下一轮请求能命中 cache prefix
//
// 降级：cachingIntegration 为 nil 时跳过 NotifyCompaction；cacheAware 为 nil
// 或模型不支持 cache_control（DefaultCacheControl 返回空值）时跳过标记注入。
// 均不阻塞 compact。
//
// 返回值：经过 cache_control 标记注入后的消息列表（可能为原切片的副本）。
// 调用方应使用返回值替换原 messages，以保留注入的标记。
func (p *CompactionPipeline) PrepareCommit(messages []session.Message) []session.Message {
	messages = session.CloneMessages(messages)

	// 重建 cache_control 标记：在 boundary 消息上设置 ephemeral breakpoint
	// 仅当 cacheAware 已注入、cachingIntegration 已注入（用于获取模型缓存能力）、
	// 且消息列表非空时执行
	if p.cacheAware != nil && p.cachingIntegration != nil && len(messages) > 0 {
		// 从 CachingIntegration 获取当前模型的缓存能力，生成对应的 CacheControl
		capability := p.cachingIntegration.GetCapability()
		cc := ai.DefaultCacheControl(capability)
		if cc.IsEmpty() {
			// 模型不支持 Anthropic 风格 cache_control（如 DeepSeek/OpenAI），跳过注入
			return messages
		}
		// 定位 cache boundary：使用 CacheAwareCompactor 的 findCacheBoundary
		// 找到最后一个 cache_control 标记或 system 消息位置。
		// findCacheBoundary 返回 exclusive index（边界消息的下一位置），
		// InjectCacheControlMarkers 需要 inclusive index（要标记的消息本身），
		// 因此减 1 转换。
		boundaryIdx := p.cacheAware.findCacheBoundary(messages)
		if boundaryIdx <= 0 || boundaryIdx > len(messages) {
			return messages
		}
		// 注入 cache_control 标记到 boundary 消息（返回新切片，不修改原切片）
		return p.cacheAware.InjectCacheControlMarkers(messages, boundaryIdx-1, cc)
	}

	return messages
}

// notifyCacheRebuild is retained for package tests and older internal callers;
// side effects moved to CommitPrepared.
func (p *CompactionPipeline) notifyCacheRebuild(messages []session.Message) []session.Message {
	return p.PrepareCommit(messages)
}

// CommitPrepared publishes side effects only after the candidate has been
// durably rewritten and installed as the live session.
func (p *CompactionPipeline) CommitPrepared() {
	if p == nil {
		return
	}
	if p.cachingIntegration != nil {
		p.cachingIntegration.NotifyCacheDeletion()
		p.cachingIntegration.NotifyCompaction()
	}
	if p.collapseMgr != nil {
		p.collapseMgr.ResetAfterCompact()
	}
}

// cacheAwareMicroCompact 在 cache 有效性感知下执行 micro-compact（N11 修复 G5，P1-5）。
//
// 行为：
// - cacheInfo 非 nil 且含有效 cache 信息时，使用 CacheAwareMicroCompact
// 保护 cache prefix（仅 compact cache boundary 之后的消息）
// - cacheInfo 为 nil 或 cache 未激活时，降级为普通 MicroCompact
func (p *CompactionPipeline) cacheAwareMicroCompact(messages []session.Message, cacheInfo *ai.CacheInfo) []session.Message {
	cacheConfig := DefaultCacheAwareMicroCompactConfig()

	// cacheInfo 为 nil 或无有效信息时降级为普通 MicroCompact
	if cacheInfo == nil || !cacheInfo.HasCacheInfo() {
		return MicroCompact(messages, p.microConfig)
	}

	// 计算 cache boundary index：使用 CacheAwareCompactor 的 findCacheBoundary
	// 定位最后一个 cache_control 标记位置
	boundaryIdx := 0
	if p.cacheAware != nil {
		boundaryIdx = p.cacheAware.findCacheBoundary(messages)
	}

	// D3 修复（G5 恒降级）：使用 cacheInfo.LastCacheTime 代替 time.Time{}，
	// 使 isCacheActive 能正确判断 cache 是否在 TTL 有效期内。
	// LastCacheTime 为零值时（cacheInfo 未填充时间戳），isCacheActive 返回 false，
	// 降级为普通 MicroCompact（向后兼容）。
	return CacheAwareMicroCompact(
		messages,
		cacheConfig,
		cacheInfo.LastCacheTime,
		time.Now(),
		boundaryIdx,
	)
}

// CompactViaPipeline is a convenience wrapper that runs the 5-layer pipeline
// and converts the result to *CompactResult for backward compatibility with
// existing handleCompact callers.
//
// P0-1 (X1): called by handleCompact when WHALE_CTX_PIPELINE is enabled,
// replacing the old single-stage MicroCompact + Compact path.
// Returns a CompactResult with CompactedMessages, RemovedCount, and Summary.
func (p *CompactionPipeline) CompactViaPipeline(
	messages []session.Message,
	budget Budget,
	cacheInfo *ai.CacheInfo,
) *CompactResult {
	result := compactResultFromPipeline(p.Execute(messages, budget, cacheInfo))
	if p.aiCompactor != nil {
		result.FallbackLevel = p.aiCompactor.LastFallbackLevel().String()
	}
	return result
}

// CompactViaPipelineWithOptions is the explicit-trigger counterpart used by
// manual and recovery callers.
func (p *CompactionPipeline) CompactViaPipelineWithOptions(
	messages []session.Message,
	budget Budget,
	cacheInfo *ai.CacheInfo,
	opts ExecuteOptions,
) *CompactResult {
	result := compactResultFromPipeline(p.ExecuteWithOptions(messages, budget, cacheInfo, opts))
	if p.aiCompactor != nil {
		result.FallbackLevel = p.aiCompactor.LastFallbackLevel().String()
	}
	return result
}

func compactResultFromPipeline(pipelineResult *PipelineResult) *CompactResult {
	return &CompactResult{
		CompactedMessages:  pipelineResult.Messages,
		RemovedCount:       pipelineResult.TotalRemoved,
		Summary:            pipelineResult.Summary,
		Changed:            pipelineResult.Changed,
		NoOpReason:         pipelineResult.NoOpReason,
		PreTokens:          pipelineResult.PreTokens,
		PostTokens:         pipelineResult.PostTokens,
		PreMessageCount:    pipelineResult.PreMessageCount,
		PostMessageCount:   pipelineResult.PostMessageCount,
		EligibleCount:      pipelineResult.EligibleCount,
		PreserveCount:      pipelineResult.PreserveCount,
		MessagesSummarized: pipelineResult.MessagesSummarized,
		StagesExecuted:     pipelineResult.StagesExecuted,
	}
}

// estimateMessagesTokens is shared by budget checks and user-visible
// PreTokens/PostTokens so one candidate never has two contradictory totals.
func (p *CompactionPipeline) estimateMessagesTokens(messages []session.Message) int {
	if p.estimator == nil {
		return 0
	}
	return p.estimator.EstimateSessionMessagesTokens(messages)
}

// messagesEqual determines if two single messages are equal
// compares message role, block count, and all content block fields
//
// P0-12: Base64Data comparison uses SHA256 hash (Base64Hash) for O(1)
// comparison when both blocks have hashes computed. Falls back to full
// string comparison when either hash is empty (backward compatibility).
func messagesEqual(a, b session.Message) bool {
	if a.Role != b.Role || len(a.Blocks) != len(b.Blocks) {
		return false
	}
	for i := range a.Blocks {
		ab, bb := a.Blocks[i], b.Blocks[i]
		if ab.Type != bb.Type ||
			ab.Text != bb.Text ||
			ab.Thinking != bb.Thinking ||
			ab.Output != bb.Output ||
			ab.Input != bb.Input ||
			ab.ID != bb.ID ||
			ab.Name != bb.Name ||
			ab.ToolUseID != bb.ToolUseID ||
			ab.ToolName != bb.ToolName ||
			ab.IsError != bb.IsError ||
			ab.MediaType != bb.MediaType {
			return false
		}
		// P0-12: Base64Data comparison via SHA256 hash (O(1)).
		// If both blocks have non-empty hashes, compare hashes only.
		// If either hash is empty, fall back to full string comparison
		// (backward compatibility for blocks where ComputeBase64Hash
		// was not called).
		if ab.Base64Hash != "" && bb.Base64Hash != "" {
			if ab.Base64Hash != bb.Base64Hash {
				return false
			}
		} else if ab.Base64Data != bb.Base64Data {
			return false
		}
	}
	return true
}

// messagesSliceEqual determines if two message slices are equal
func messagesSliceEqual(a, b []session.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !messagesEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// MessagesEqual reports whether two message slices have identical semantic
// content. It lets compatibility callers detect same-count MicroCompact work.
func MessagesEqual(a, b []session.Message) bool {
	return messagesSliceEqual(a, b)
}

// uniqueStrings returns a new slice containing the unique strings from s,
// preserving order of first occurrence.
//
// P0-13: returns a freshly allocated slice (make) instead of reusing the
// input's backing array (s[:0]). The old s[:0] approach mutated the input
// slice in place, which could surprise callers that retain the original
// slice. The new make approach guarantees the input is never modified.
func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}
