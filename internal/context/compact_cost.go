// Package context — compact 用量桥接层（N11 修复 G1 + G11）。
//
// 本文件桥接 compact AI 调用的 usage 事件到 N2 metrics.RecordUsage
// 与 N1 ai.UsageTracker.RecordAgentUsage，标记 AgentID="compact"。
//
// 契约：02 §2.1.2 要求 compact 用量走统一入口，AgentID="compact"，
// 不允许 N11 自维护计数器。
//
// ★ D 批：原先还记录一笔按本地价格表估算的"金额"（costCents）。
// 该字段已随成本线删除 —— provider 不上报费用，本地估算与账单不符且混币种。
// 类型名 CompactCostRecorder 为历史遗留（曾用于记录 cost），保留以免大范围改名；
// 现在它只记录 token 用量。
package context

import (
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/metrics"
)

// compactAgentID 是 compact AI 调用的固定 AgentID（02 §2.1.2 契约）。
// 通过此 ID 可在 UsageTracker 的 by-agent 分桶中识别 compact 来源。
const compactAgentID = "compact"

// CompactCostRecorder 桥接 compact AI 调用的 usage 事件到 N2 metrics.RecordUsage
// 与 N1 ai.UsageTracker.RecordAgentUsage，标记 AgentID="compact"。
//
// 契约：02 §2.1.2 要求 compact 用量走统一入口，AgentID="compact"。
// 不自维护计数器，全部委托给 N2/N1。（类型名为历史遗留，见包注释。）
//
// 并发安全：RecordUsage 由 generateAISummary 的 event loop 单 goroutine 调用，
// 无需加锁（参考 N11 改进方案 §5.3）。
type CompactCostRecorder struct {
	mu           sync.Mutex
	metrics      *metrics.Metrics // N2 全局累加（UsageRecord，含 AgentID/Model/Timestamp）
	usageTracker *ai.UsageTracker // N1 by-agent 分桶（双写）
	model        string           // 当前模型名，用于 RecordAgentUsage
	totalUsage   ai.UsageEvent
	hasUsage     bool
}

// NewCompactCostRecorder 创建 compact 用量记录器。
// m 为 nil 时 RecordUsage 跳过 N2 累加；ut 为 nil 时跳过 N1 by-agent 分桶。
func NewCompactCostRecorder(m *metrics.Metrics, ut *ai.UsageTracker, model string) *CompactCostRecorder {
	return &CompactCostRecorder{
		metrics:      m,
		usageTracker: ut,
		model:        model,
	}
}

// RecordUsage 处理 ai.UsageEvent，将用量记录到 N2 metrics + N1 UsageTracker。
// AgentID 固定为 "compact"（02 §2.1.2 契约）。
//
// 行为：
// 1. 调用 metrics.RecordUsage(UsageRecord{AgentID: "compact", ...})
// —— N2 唯一入口（02 §2.1.2 契约）
// 2. 调用 usageTracker.RecordAgentUsage("compact", model, input, output, cacheCreation, cacheRead)
// —— by-agent 双写
//
// 降级策略（N11 开发prompt 步骤 1）：
// - metrics 为 nil → 跳过 N2 累加 + 日志告警（不阻塞）
// - usageTracker 为 nil → 跳过 N1 by-agent 分桶 + 日志告警（不阻塞）
// - model 为空 → 只影响 by-agent 归属，token 计数照常
func (r *CompactCostRecorder) RecordUsage(usage ai.UsageEvent) {
	if r == nil {
		return
	}
	r.RecordUsageForModel(r.model, usage)
}

// RecordUsageForModel records one finalized provider attempt. The explicit
// model keeps fallback attempts attributed to the model that actually ran.
func (r *CompactCostRecorder) RecordUsageForModel(model string, usage ai.UsageEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.totalUsage.InputTokens += usage.InputTokens
	r.totalUsage.OutputTokens += usage.OutputTokens
	r.totalUsage.CacheCreation += usage.CacheCreation
	r.totalUsage.CacheRead += usage.CacheRead
	r.hasUsage = true
	r.mu.Unlock()

	// N2 累加（UsageRecord，含 AgentID="compact" + Model + Timestamp）
	//
	// D 批：此处原先用 ai.CostCentsFor 估出一笔"金额"一起记录，
	// 那笔钱是我们自己按价格表算的（provider 不上报、也不等于账单，且混币种），
	// 已随成本线整体删除。四项 token 是 provider 真报的事实，照记。
	if r.metrics != nil {
		r.metrics.RecordUsage(metrics.UsageRecord{
			AgentID:       compactAgentID,
			Model:         model,
			InputTokens:   int64(usage.InputTokens),
			OutputTokens:  int64(usage.OutputTokens),
			CacheCreation: int64(usage.CacheCreation),
			CacheRead:     int64(usage.CacheRead),
			Timestamp:     time.Now(),
		})
	} else {
		// 降级：N2 metrics 未注入，仅日志告警（不阻塞 compact）
		log.Warn("compact_cost: metrics nil, N2 累加跳过（降级路径）")
	}

	// N1 by-agent 分桶（双写）
	if r.usageTracker != nil {
		r.usageTracker.RecordAgentUsage(
			compactAgentID,
			model,
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheCreation,
			usage.CacheRead,
		)
	} else {
		// 降级：N1 UsageTracker 未注入，仅日志告警（不阻塞 compact）
		log.Warn("compact_cost: usageTracker nil, N1 by-agent 分桶跳过（降级路径）")
	}
}

// TotalUsage returns the sum of finalized provider attempts made by this
// compact operation. It is consumed by the TUI layer for session-local totals
// and cache metrics; global cost and UsageTracker records are already written.
func (r *CompactCostRecorder) TotalUsage() (ai.UsageEvent, bool) {
	if r == nil {
		return ai.UsageEvent{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalUsage, r.hasUsage
}
