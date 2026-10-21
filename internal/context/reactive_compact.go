// Package context — reactive compaction for 413 / prompt-too-long recovery
// (P0-2 / C10 contract).
//
// When a 413 or prompt-too-long error occurs, the ReactiveCompactor attempts
// to recover by:
// 1. Draining staged collapses via ContextCollapseManager.RecoverFromOverflow
// (cheapest — reuses already-staged collapse projections).
// 2. Running a progressive 4-level compaction chain ( 候选 B):
// - Level 1 Snip: 移除 tombstoned/zombie 消息（最便宜）
// - Level 2 MicroCompact: 截断旧 tool_result 内容
// - Level 3 Collapse: 结构化折叠 + 保留 markers
// - Level 4 AI 分段压缩: 分段独立 AICompact（最贵）
// 每级完成后做收敛检查（currentTokens <= originalTokens × ReductionTarget），
// 收敛即返回，避免过度压缩。
// 3. Returning the compacted messages so the caller can retry the request.
//
// A per-request `hasAttemptedReactiveCompact` flag prevents infinite recovery
// loops: once reactive compaction has been attempted for a request, it will
// not be attempted again until Reset() is called (typically after a
// successful response or a new request).
//
// Feature flag: WHALE_CTX_REACTIVE_COMPACT (default true). When disabled,
// TryReactiveCompact is a no-op returning (original, false).
//
// Thread-safety: ReactiveCompactor is safe for concurrent use. The
// hasAttemptedReactiveCompact flag is protected by a mutex. However, each
// request should use its own ReactiveCompactor instance (or call Reset()
// between requests) to avoid cross-request flag contamination.
package context

import (
	"fmt"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
	aierrors "github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/session"
)

// ProgressiveLevel 渐进式压缩级别（从便宜到贵， 候选 B）。
//
// 级别语义：
// - Level0None：未尝试
// - Level1Snip：仅 Snip（移除 tombstoned/zombie 消息，最便宜）
// - Level2MicroCompact：+ MicroCompact（截断旧 tool_result 内容）
// - Level3Collapse：+ Collapse（结构化折叠 + 保留 markers）
// - Level4AISegmented：+ AI 分段压缩（最贵，分段独立摘要）
type ProgressiveLevel int

const (
	Level0None ProgressiveLevel = iota
	Level1Snip
	Level2MicroCompact
	Level3Collapse
	Level4AISegmented
)

// String 返回级别的字符串表示（供可观测 / reason 字符串）。
func (l ProgressiveLevel) String() string {
	switch l {
	case Level0None:
		return "none"
	case Level1Snip:
		return "snip"
	case Level2MicroCompact:
		return "micro_compact"
	case Level3Collapse:
		return "collapse"
	case Level4AISegmented:
		return "ai_segmented"
	default:
		return "unknown"
	}
}

// ProgressiveConfig 渐进式压缩配置（候选 B 新增）。
type ProgressiveConfig struct {
	// ReductionTarget 收敛目标比例（currentTokens <= originalTokens × ReductionTarget 即停）。
	// 默认 0.6（压缩到原始 60%）。
	ReductionTarget float64
	// MaxLevel 最大允许级别（默认 Level4AISegmented）。
	MaxLevel ProgressiveLevel
	// SegmentConfig 分段压缩配置（仅 Level 4 使用）。
	SegmentConfig SegmentConfig
}

// SegmentConfig 分段 AI 压缩配置（候选 B 新增）。
type SegmentConfig struct {
	// SegmentSize 每段消息数（默认 15）。
	SegmentSize int
	// MaxSegments 最大段数（默认 4，避免过多 AI 调用）。
	MaxSegments int
	// PreserveRecent 保留最近 N 条消息不压缩（默认 10）。
	PreserveRecent int
	// MaxPasses 段内最大压缩轮数（默认 2，超长段可二轮分段）。
	MaxPasses int
}

// DefaultProgressiveConfig 返回默认渐进式配置（候选 B）。
//
// 默认值对齐：
// - ReductionTarget=0.6（压缩到原始 60%）
// - MaxLevel=Level4AISegmented（允许升级到分段 AI 压缩）
// - SegmentSize=15（每段 15 条消息，参考同类实现 分段约 10-20 条）
// - MaxSegments=4（最坏 4 次 AI 调用，成本可控）
// - PreserveRecent=10（与 DefaultCompactionConfig.PreserveRecentMessages 对齐）
// - MaxPasses=2（段内二轮压缩，避免无限递归）
func DefaultProgressiveConfig() ProgressiveConfig {
	return ProgressiveConfig{
		ReductionTarget: 0.6,
		MaxLevel:        Level4AISegmented,
		SegmentConfig: SegmentConfig{
			SegmentSize:    15,
			MaxSegments:    4,
			PreserveRecent: 10,
			MaxPasses:      2,
		},
	}
}

// ReactiveCompactor handles 413 / prompt-too-long recovery by triggering
// context compaction and returning compacted messages for retry (P0-2).
//
// 候选 B：渐进式 4 级压缩 + 分段 AI 压缩。
// Recovery order (cheapest first):
// 1. ContextCollapseManager.RecoverFromOverflow — drains staged collapses.
// 2. Progressive 4-level chain: Snip → MicroCompact → Collapse → AI Segmented.
//
// The hasAttemptedReactiveCompact flag ensures reactive compaction is
// attempted at most once per request lifecycle, preventing loops.
type ReactiveCompactor struct {
	mu          sync.Mutex
	pipeline    *CompactionPipeline
	collapseMgr *ContextCollapseManager

	// hasAttemptedReactiveCompact is set to true after the first
	// TryReactiveCompact call. Subsequent calls return (original, false)
	// until Reset() is called. This prevents infinite recovery loops.
	hasAttemptedReactiveCompact bool

	// SNAPSHOT-1：本轮新增字段
	// estimator 用于 token 估算（收敛检查），nil 时降级为消息数比较。
	estimator *ai.TokenEstimator
	// SNAPSHOT-2：渐进式配置（TryReactiveCompact 入口复制到局部变量，避免持锁调用 AI）。
	progressiveCfg ProgressiveConfig
	// SNAPSHOT-3：本次请求尝试到的最高级别（供 MaxAttemptedLevel 可观测）。
	maxAttemptedLevel ProgressiveLevel
	// SNAPSHOT-4：分段压缩复用的 AI 压缩器，nil 时 Level 4 降级为单段 Collapse。
	aiCompactor *AICompactor
}

// NewReactiveCompactor creates a new ReactiveCompactor.
//
// pipeline: the 5-layer CompactionPipeline (may be nil — TryReactiveCompact
//
//	will skip the pipeline stage and only use collapse recovery).
//
// collapseMgr: the ContextCollapseManager for staged-collapse drain
//
//	(may be nil — TryReactiveCompact will skip collapse recovery).
//
// 内部初始化默认 ProgressiveConfig（可通过 SetProgressiveConfig 覆盖）。
func NewReactiveCompactor(pipeline *CompactionPipeline, collapseMgr *ContextCollapseManager) *ReactiveCompactor {
	return &ReactiveCompactor{
		pipeline:       pipeline,
		collapseMgr:    collapseMgr,
		progressiveCfg: DefaultProgressiveConfig(),
	}
}

// TryReactiveCompact attempts to recover from a 413 / prompt-too-long error
// by compacting the message list (P0-2).
//
// 候选 B：渐进式 4 级压缩 + 分段 AI 压缩。
//
// Returns:
// - compacted: the compacted message list (may be the same as input if
// no compaction occurred).
// - ok: true if compaction actually reduced the message count.
// - reason: a human-readable description of what recovery was attempted.
//
// If the WHALE_CTX_REACTIVE_COMPACT feature flag is disabled (env set to
// "0"/"false"), returns (original, false, "reactive compact disabled...").
//
// If hasAttemptedReactiveCompact is already true, returns
// (original, false, "already attempted") to prevent loops.
//
// N11 修复 G2：reason 字符串通过 aierrors.WithID 包装为携带 ErrAIRetryable
// ErrorID 的错误后取 .Error()，使下游 friendlyError 可识别 ErrorID。
// 注意：reason 仍为 string 类型（向后兼容），但内含 [ai_retryable] 前缀。
func (r *ReactiveCompactor) TryReactiveCompact(original []session.Message) (compacted []session.Message, ok bool, reason string) {
	// Feature flag gate (default true — disabled only when env is "0"/"false").
	if !IsReactiveCompactEnabled() {
		// G2 修复：wrap ErrAIRetryable（可重试，用户可通过设置环境变量恢复）
		err := aierrors.WithID(fmt.Errorf("reactive compact disabled (WHALE_CTX_REACTIVE_COMPACT=0/false)"), aierrors.ErrAIRetryable)
		return original, false, err.Error()
	}

	// 落实建议优化 5：入口加锁复制配置到局部变量，后续执行无锁。
	r.mu.Lock()
	if r.hasAttemptedReactiveCompact {
		r.mu.Unlock()
		// G2 修复：wrap ErrAIRetryable（已尝试过，需等待 Reset 后重试）
		err := aierrors.WithID(fmt.Errorf("reactive compact already attempted for this request"), aierrors.ErrAIRetryable)
		return original, false, err.Error()
	}
	// Mark as attempted immediately to prevent re-entry even if compaction fails.
	r.hasAttemptedReactiveCompact = true
	cfg := r.progressiveCfg      // 复制配置（值类型，安全）
	estimator := r.estimator     // 复制引用（*ai.TokenEstimator 只读）
	aiCompactor := r.aiCompactor // 复制引用（*AICompactor 内部自有锁）
	pipeline := r.pipeline       // 复制引用
	collapseMgr := r.collapseMgr // 复制引用
	r.mu.Unlock()

	// Step 0: collapse recovery（最便宜路径，向后兼容原 Step 1）。
	if collapseMgr != nil {
		recovered, recoveredOK := collapseMgr.RecoverFromOverflow(original)
		if recoveredOK && len(recovered) < len(original) {
			r.setMaxAttemptedLevel(Level1Snip) // 任意级别标记（已通过 recovery 收敛）
			return recovered, true, "collapse recovery: drained staged collapses"
		}
		// If collapse recovery returned a projection (even if same length),
		// use it as input to the pipeline stage.
		if recoveredOK {
			original = recovered
		}
	}

	// 空切片防御。
	if len(original) == 0 {
		return original, false, "reactive compact: empty input"
	}

	// 计算收敛目标。
	originalTokens := r.estimateTokens(original, estimator)
	targetTokens := int(float64(originalTokens) * cfg.ReductionTarget)
	current := original
	stagesExecuted := []string{}

	// 辅助：检查是否收敛。
	checkConverged := func(msgs []session.Message) bool {
		if estimator != nil {
			return r.estimateTokens(msgs, estimator) <= targetTokens
		}
		// 降级：消息数比较（落实建议优化 6 的 0.8 阈值）。
		return len(msgs) < int(float64(len(original))*0.8)
	}

	// Level 1: Snip only（移除 tombstoned + zombie）。
	if cfg.MaxLevel >= Level1Snip && pipeline != nil && pipeline.snip != nil {
		snipResult := pipeline.snip.Snip(current)
		if snipResult != nil && snipResult.RemovedCount > 0 {
			current = snipResult.Messages
			stagesExecuted = append(stagesExecuted, "L1_snip")
			r.setMaxAttemptedLevel(Level1Snip)
			if checkConverged(current) {
				return current, true, r.buildReason("progressive converged at L1", stagesExecuted, originalTokens, current, estimator)
			}
		}
	}

	// Level 2: MicroCompact（截断旧 tool_result）。
	if cfg.MaxLevel >= Level2MicroCompact && pipeline != nil {
		microResult := MicroCompact(current, pipeline.microConfig)
		if !messagesSliceEqual(current, microResult) {
			current = microResult
			stagesExecuted = append(stagesExecuted, "L2_micro_compact")
			r.setMaxAttemptedLevel(Level2MicroCompact)
			if checkConverged(current) {
				return current, true, r.buildReason("progressive converged at L2", stagesExecuted, originalTokens, current, estimator)
			}
		}
	}

	// Level 3: Collapse（结构化折叠）。
	if cfg.MaxLevel >= Level3Collapse && pipeline != nil && pipeline.collapser != nil {
		collapseResult := pipeline.collapser.CollapseWithCacheBoundary(current, 10)
		if collapseResult != nil && collapseResult.RemovedCount > 0 {
			current = collapseResult.PreservedMessages
			stagesExecuted = append(stagesExecuted, "L3_collapse")
			r.setMaxAttemptedLevel(Level3Collapse)
			if checkConverged(current) {
				return current, true, r.buildReason("progressive converged at L3", stagesExecuted, originalTokens, current, estimator)
			}
		}
	}

	// Level 4: AI 分段压缩（最贵）。
	if cfg.MaxLevel >= Level4AISegmented {
		if aiCompactor != nil {
			segResult, segOK := r.segmentedAICompact(current, aiCompactor, cfg.SegmentConfig, estimator)
			if segOK && len(segResult) < len(current) {
				current = segResult
				stagesExecuted = append(stagesExecuted, "L4_ai_segmented")
				r.setMaxAttemptedLevel(Level4AISegmented)
				if checkConverged(current) {
					return current, true, r.buildReason("progressive converged at L4", stagesExecuted, originalTokens, current, estimator)
				}
				// 未收敛但已压缩，返回当前结果。
				return current, true, r.buildReason("progressive target not met at L4", stagesExecuted, originalTokens, current, estimator)
			}
		} else {
			// 降级：无 AI，Level 4 跳过（落实 §4.4 降级策略）。
			// 修复 B2-P1-3：接线遗漏时 aiCompactor 为 nil，Level 4 静默跳过无 warn 日志。
			// 现记录 warn 日志，便于运维发现接线遗漏（SetAICompactor 未调用）。
			log.Warn("[context] reactive compact L4 skipped: aiCompactor is nil (SetAICompactor not called)")
			// 注意：仅记录可观测，不影响 stagesExecuted 收敛判断。
			stagesExecuted = append(stagesExecuted, "L4_skipped_no_ai")
		}
	}

	// 检查是否有任何恢复机制可用（pipeline 或 collapseMgr）。
	recoveryMechanismAvailable := pipeline != nil || collapseMgr != nil
	if !recoveryMechanismAvailable {
		// G2 修复：无恢复机制可用，wrap ErrAIFatal（不可恢复，需用户干预）
		err := aierrors.WithID(fmt.Errorf("reactive compact: no recovery mechanism available"), aierrors.ErrAIFatal)
		return original, false, err.Error()
	}

	// 有压缩但未达目标。
	if len(current) < len(original) {
		return current, true, r.buildReason("progressive target not met", stagesExecuted, originalTokens, current, estimator)
	}

	// 无压缩效果（pipeline/collapse 跑了但没移除消息）。
	// G2 修复：pipeline 未能移除消息，wrap ErrAIRetryable（可重试，可能是消息数已最少）
	err := aierrors.WithID(fmt.Errorf("pipeline compaction: no messages removed"), aierrors.ErrAIRetryable)
	return current, false, err.Error()
}

// setMaxAttemptedLevel 原子更新最高尝试级别（线程安全）。
func (r *ReactiveCompactor) setMaxAttemptedLevel(level ProgressiveLevel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if level > r.maxAttemptedLevel {
		r.maxAttemptedLevel = level
	}
}

// MaxAttemptedLevel 返回本次请求内尝试到的最高级别（候选 B，供可观测）。
// 在 HasAttempted()==true 时有效；Reset 后归零。线程安全。
func (r *ReactiveCompactor) MaxAttemptedLevel() ProgressiveLevel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxAttemptedLevel
}

// buildReason 构造 reason 字符串（含级别 + 段数 + token 减少量）。
func (r *ReactiveCompactor) buildReason(msg string, stages []string, originalTokens int, current []session.Message, est *ai.TokenEstimator) string {
	currentTokens := r.estimateTokens(current, est)
	return fmt.Sprintf("%s: stages=%v tokens %d->%d (-%d)", msg, stages, originalTokens, currentTokens, originalTokens-currentTokens)
}

// HasAttempted returns true if TryReactiveCompact has been called at least
// once since the last Reset(). Thread-safe.
func (r *ReactiveCompactor) HasAttempted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hasAttemptedReactiveCompact
}

// Reset clears the hasAttemptedReactiveCompact flag and maxAttemptedLevel,
// allowing a new reactive compaction attempt. Should be called after a
// successful request response or when starting a new request. Thread-safe.
//
// 同时归零 maxAttemptedLevel（可观测字段）。
func (r *ReactiveCompactor) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hasAttemptedReactiveCompact = false
	r.maxAttemptedLevel = Level0None
}

// SetPipeline allows late binding of the compaction pipeline (useful when
// the pipeline is constructed after the ReactiveCompactor). Thread-safe.
func (r *ReactiveCompactor) SetPipeline(p *CompactionPipeline) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pipeline = p
}

// SetCollapseManager allows late binding of the collapse manager.
// Thread-safe.
func (r *ReactiveCompactor) SetCollapseManager(m *ContextCollapseManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collapseMgr = m
}

// SetEstimator 注入 token 估算器（候选 B，落实审查建议优化 2）。
// 为 nil 时收敛检查降级为消息数比较（currentMsgs < originalMsgs × 0.8 即收敛）。
// 线程安全。
func (r *ReactiveCompactor) SetEstimator(est *ai.TokenEstimator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.estimator = est
}

// SetAICompactor 注入 AI 压缩器（候选 B，落实审查建议优化 1）。
// 为 nil 时 Level 4 降级为单段 Collapse（不调 AI）。
// 线程安全。
func (r *ReactiveCompactor) SetAICompactor(c *AICompactor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aiCompactor = c
}

// SetProgressiveConfig 注入渐进式配置（候选 B）。
// 线程安全：TryReactiveCompact 入口会复制此配置到局部变量。
func (r *ReactiveCompactor) SetProgressiveConfig(cfg ProgressiveConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progressiveCfg = cfg
}

// segmentedAICompact 分段 AI 压缩（候选 B 核心）。
//
// 流程：
// 1. 保留最近 preserveRecent 条消息不压缩
// 2. 将剩余历史按 segmentSize 切分为 N 段（上限 maxSegments）
// 3. 每段独立调用 AICompactor.Compact 生成段摘要
// 4. 段内二轮压缩（MaxPasses=2）：超长段再次分段，二轮后比较 token 防反向膨胀
// 5. 拼接：[段1摘要] + [段2摘要] + ... + [段N摘要] + [保留最近消息]
//
// 降级（落实建议优化 3）：剩余消息数 <= segmentSize 时，作为单段调用 AICompact。
// 降级（落实建议优化 4）：二轮后 token 增加则回退一轮结果。
//
// 参数：
// - messages：当前消息列表
// - aiCompactor：AI 压缩器（非 nil，由调用方保证）
// - cfg：分段配置
// - est：token 估算器（nil 时跳过 token 比较，仅按消息数判断）
//
// 返回：
// - compacted：压缩后的消息列表
// - ok：是否实际压缩
func (r *ReactiveCompactor) segmentedAICompact(
	messages []session.Message,
	aiCompactor *AICompactor,
	cfg SegmentConfig,
	est *ai.TokenEstimator,
) ([]session.Message, bool) {
	if len(messages) == 0 || aiCompactor == nil {
		return messages, false
	}

	// 防御：消息数不足以分段。
	preserveRecent := cfg.PreserveRecent
	if preserveRecent < 0 {
		preserveRecent = 0
	}
	if len(messages) <= preserveRecent {
		return messages, false
	}

	// 切分：保留最近 preserveRecent 条，剩余按 segmentSize 分段。
	toCompress := messages[:len(messages)-preserveRecent]
	preserved := messages[len(messages)-preserveRecent:]

	// 落实建议优化 3：剩余消息数 <= segmentSize 时，作为单段调用 AICompact。
	segmentSize := cfg.SegmentSize
	if segmentSize <= 0 {
		segmentSize = 15
	}
	if len(toCompress) <= segmentSize {
		result := r.compactSingleSegment(toCompress, aiCompactor)
		if result == nil {
			return messages, false
		}
		return append(result, preserved...), true
	}

	// 分段：按 segmentSize 切分，上限 maxSegments 段。
	maxSegments := cfg.MaxSegments
	if maxSegments <= 0 {
		maxSegments = 4
	}
	segments := r.splitSegments(toCompress, segmentSize, maxSegments)

	// 逐段压缩。
	maxPasses := cfg.MaxPasses
	if maxPasses <= 0 {
		maxPasses = 2
	}

	var compactedSummaries []session.Message
	for i, seg := range segments {
		// 段内二轮压缩（MaxPasses=2）。
		prevTokens := r.estimateTokens(seg, est)
		currentSeg := seg
		for pass := 0; pass < maxPasses; pass++ {
			single := r.compactSingleSegment(currentSeg, aiCompactor)
			if single == nil {
				break
			}
			// 落实建议优化 4：二轮后 token 增加则回退一轮结果。
			// 修复 B2-P1-2：移除 pass > 0 条件，pass 0 也检查反向膨胀。
			// 原实现 pass 0 首轮膨胀结果被直接替换，导致 token 不降反升。
			currentTokens := r.estimateTokens(single, est)
			if est != nil && currentTokens > prevTokens {
				// 反向膨胀，回退（保留上一轮结果）。
				break
			}
			currentSeg = single
			prevTokens = currentTokens
			// 段内已收敛（1 条摘要消息），无需二轮。
			if len(currentSeg) <= 1 {
				break
			}
		}

		// 标记段摘要 boundary（trigger="reactive_segmented"）。
		if len(currentSeg) > 0 {
			r.markSegmentBoundary(&currentSeg[0], i+1, len(seg))
		}
		compactedSummaries = append(compactedSummaries, currentSeg...)
	}

	// 拼接：段摘要 + 保留最近消息。
	result := make([]session.Message, 0, len(compactedSummaries)+len(preserved))
	result = append(result, compactedSummaries...)
	result = append(result, preserved...)

	if len(result) < len(messages) {
		return result, true
	}
	return messages, false
}

// splitSegments 将消息列表按 segmentSize 切分为最多 maxSegments 段。
// 超过 maxSegments 段时，末段合并超出部分（保证不超 maxSegments 段）。
func (r *ReactiveCompactor) splitSegments(messages []session.Message, segmentSize, maxSegments int) [][]session.Message {
	if len(messages) == 0 || segmentSize <= 0 {
		return nil
	}
	var segments [][]session.Message
	for i := 0; i < len(messages) && len(segments) < maxSegments; i += segmentSize {
		end := i + segmentSize
		if end > len(messages) {
			end = len(messages)
		}
		// 末段：若剩余超出 maxSegments，合并到当前段。
		remaining := len(messages) - end
		if len(segments) == maxSegments-1 && remaining > 0 {
			end = len(messages) // 合并所有剩余到末段
		}
		segments = append(segments, messages[i:end])
	}
	return segments
}

// compactSingleSegment 对单段消息调用 AICompactor.Compact 生成摘要。
// 失败时返回 nil（调用方降级处理）。
func (r *ReactiveCompactor) compactSingleSegment(seg []session.Message, aiCompactor *AICompactor) []session.Message {
	if len(seg) == 0 || aiCompactor == nil {
		return nil
	}
	cfg := DefaultCompactionConfig()
	// 段内保留最近 2 条（段内上下文连贯），其余摘要。
	if len(seg) > 2 {
		cfg.PreserveRecentMessages = 2
	} else {
		cfg.PreserveRecentMessages = 1
	}
	result := aiCompactor.Compact(seg, cfg)
	if result == nil || result.RemovedCount == 0 {
		return nil
	}
	return result.CompactedMessages
}

// markSegmentBoundary 标记段摘要消息的 compact_boundary metadata。
// trigger="reactive_segmented"，记录段序号与段原始消息数。
func (r *ReactiveCompactor) markSegmentBoundary(msg *session.Message, segIndex, segOrigCount int) {
	SetCompactBoundaryMetadata(msg, CompactMetadata{
		Trigger:            "reactive_segmented",
		MessagesSummarized: segOrigCount,
	})
	// 追加段序号到 Metadata（供可观测）。
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}
	msg.Metadata["compact_segment_index"] = segIndex
}

// estimateTokens 估算消息列表的 token 数。
// est 为 nil 时返回消息数 × 100（粗略估算，仅用于降级路径比较）。
func (r *ReactiveCompactor) estimateTokens(messages []session.Message, est *ai.TokenEstimator) int {
	if est == nil {
		return len(messages) * 100
	}
	return estimateSessionMessagesTokens(est, messages)
}

// BuildPostCompactMessages wraps the compacted message list with a
// compact_boundary marker message (P0-2 / C7 contract).
//
// The marker is a system message with Subtype="compact_boundary" that
// signals to downstream consumers (session, TUI) where compaction occurred.
// This allows ReloadFromSession to reconstruct the logical conversation
// boundary.
//
// P1-1 (X12): uses SetCompactBoundaryMetadata to populate the standard
// CompactMetadata fields (compact_trigger="reactive", etc.) instead of
// ad-hoc Metadata["compact_boundary"]=true. This ensures consistency with
// AICompactor.Compact's boundary markers.
//
// If the compacted list is empty or no compaction occurred (ok=false),
// the original list is returned unchanged.
func BuildPostCompactMessages(compacted []session.Message, ok bool) []session.Message {
	if !ok || len(compacted) == 0 {
		return compacted
	}
	marker := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "[compact boundary — messages above were summarized]"},
		},
	}
	// P1-1: use the standard SetCompactBoundaryMetadata helper so the
	// marker has the same metadata schema as AICompactor.Compact's markers.
	SetCompactBoundaryMetadata(&marker, CompactMetadata{
		Trigger:            "reactive",
		MessagesSummarized: len(compacted),
	})
	// Insert marker at the beginning (before the compacted summary).
	result := make([]session.Message, 0, len(compacted)+1)
	result = append(result, marker)
	result = append(result, compacted...)
	return result
}
