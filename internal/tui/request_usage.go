package tui

import (
	"context"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// recordProviderUsage records one provider-reported request total. Usage is a
// billing fact and is recorded even when a later stream event reports an
// error. Session-local counters are updated separately by the caller after an
// epoch check.
func recordProviderUsage(ctx context.Context, model string, usage ai.UsageEvent, tracker *ai.UsageTracker, caching *ai.CachingIntegration) {
	agentID := tools.ExtractAgentID(ctx)
	if tracker != nil {
		if agentID != "" {
			tracker.RecordAgentUsage(agentID, model, usage.InputTokens, usage.OutputTokens, usage.CacheCreation, usage.CacheRead)
		} else {
			tracker.RecordUsage(model, usage.InputTokens, usage.OutputTokens, usage.CacheCreation, usage.CacheRead)
		}
	}
	if caching != nil {
		caching.RecordCacheMetrics(usage.InputTokens, usage.OutputTokens, usage.CacheRead)
	}

	// D 批：这里原先还调 ai.CostCentsFor 算出一笔"金额"塞进记录。
	// 那笔钱是我们自己按价格表估的（provider 不上报、也不等于账单，且混币种），
	// 已随成本线整体删除。四项 token 是 provider 真报的事实，照记。
	metrics.Default.RecordUsage(metrics.UsageRecord{
		AgentID:       agentID,
		Model:         model,
		InputTokens:   int64(usage.InputTokens),
		OutputTokens:  int64(usage.OutputTokens),
		CacheCreation: int64(usage.CacheCreation),
		CacheRead:     int64(usage.CacheRead),
		Timestamp:     time.Now(),
	})
}

func (c *runnerCore) finalizeRequestUsage(ctx context.Context, acc *ai.RequestUsageAccumulator, epoch uint64) (ai.UsageEvent, bool) {
	usage, ok := acc.Finalize()
	if !ok {
		return ai.UsageEvent{}, false
	}
	recordProviderUsage(ctx, c.ModelName, usage, c.UsageTracker, c.Caching)
	if c.sessionEpoch.Load() == epoch {
		c.LastInputTokens = usage.InputTokens
		c.LastOutputTokens = usage.OutputTokens
		c.InputTokens += usage.InputTokens
		c.OutputTokens += usage.OutputTokens
		c.RequestCount++
	}
	return usage, true
}

func (m *Model) beginRequestUsage(requestID string) {
	m.SessionModel.requestUsage = ai.RequestUsageAccumulator{}
	m.SessionModel.requestEpoch = m.SessionModel.sessionEpoch
	m.SessionModel.usageRequestID = requestID
	m.SessionModel.lastInputTokens = 0
	m.SessionModel.lastOutputTokens = 0
}

func (m *Model) finalizeRequestUsage(requestID string) (ai.UsageEvent, bool) {
	if requestID == "" || requestID != m.SessionModel.usageRequestID {
		return ai.UsageEvent{}, false
	}
	usage, ok := m.SessionModel.requestUsage.Finalize()
	if !ok {
		return ai.UsageEvent{}, false
	}
	recordProviderUsage(context.Background(), m.sessionService.ModelName(), usage, m.sessionService.UsageTracker(), m.aiService.Caching())
	if m.SessionModel.requestEpoch == m.SessionModel.sessionEpoch {
		m.SessionModel.lastInputTokens = usage.InputTokens
		m.SessionModel.lastOutputTokens = usage.OutputTokens
		m.sessionService.SetInputTokens(m.sessionService.InputTokens() + usage.InputTokens)
		m.sessionService.SetOutputTokens(m.sessionService.OutputTokens() + usage.OutputTokens)
		m.sessionService.SetRequestCount(m.sessionService.RequestCount() + 1)
	}
	return usage, true
}
