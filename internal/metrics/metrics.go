// Package metrics — Prometheus metrics and health endpoint for monitoring
package metrics

import (
	"fmt"
	"sync/atomic"
	"time"
)

// Metrics holds all metric counters and gauges
type Metrics struct {
	// AI request metrics
	AIRequestCount      atomic.Int64
	AIRequestErrors     atomic.Int64
	AIRequestDurationMs atomic.Int64 // total duration in ms for averaging

	// Cache metrics
	CacheHits   atomic.Int64
	CacheMisses atomic.Int64

	// Tool execution metrics
	ToolExecutionCount  atomic.Int64
	ToolExecutionErrors atomic.Int64

	// Session metrics
	ActiveSessions atomic.Int64

	// Token usage
	InputTokens  atomic.Int64
	OutputTokens atomic.Int64

	// 5-E-4：cache token 维度，桥接自 ai.UsageTracker。
	//
	// ★ 2026-10-05（D 批）：此处原还有 `TotalCost atomic.Int64`（cents×10000），
	// 它是**本地价格表估算出来的金额**——不是 provider 上报、也不等于账单，
	// 且混用 USD/RMB 两种币种 ⇒ 已随成本线整体删除。token 四项是 provider 真报的，保留。
	CacheCreationTokens atomic.Int64
	CacheReadTokens     atomic.Int64

	// §4.6 安全事件计数器：记录安全拦截事件总数（SSRF/JWT/SSH 等）。
	// 按事件类型的分桶由 prometheus.go 的 securityEvents CounterVec 维护，
	// 此字段仅做全局总数累加，供 daemon metrics_server 手写格式输出。
	SecurityEvents atomic.Int64
}

// Global metrics instance
var Default = &Metrics{}

// RecordAIRequest records an AI request with duration
func (m *Metrics) RecordAIRequest(duration time.Duration, err error) {
	m.AIRequestCount.Add(1)
	m.AIRequestDurationMs.Add(duration.Milliseconds())
	if err != nil {
		m.AIRequestErrors.Add(1)
	}
}

// RecordCacheHit records a cache hit
func (m *Metrics) RecordCacheHit() {
	m.CacheHits.Add(1)
}

// RecordCacheMiss records a cache miss
func (m *Metrics) RecordCacheMiss() {
	m.CacheMisses.Add(1)
}

// RecordToolExecution records a tool execution
func (m *Metrics) RecordToolExecution(err error) {
	m.ToolExecutionCount.Add(1)
	if err != nil {
		m.ToolExecutionErrors.Add(1)
	}
}

// RecordSecurityEvent 记录一次安全拦截事件（§4.6）。
// eventType 标识事件类型（如 "ssrf"/"jwt"/"ssh"），用于 Prometheus 分桶；
// 本方法仅累加全局 SecurityEvents 总数，Prometheus 分桶由 RecordSecurityEventPrometheus 维护。
// 调用方应同时使用 internal/log 包记录 log.Warn 告警日志。
func (m *Metrics) RecordSecurityEvent(eventType string) {
	m.SecurityEvents.Add(1)
}

// UsageRecord 是 token 用量数据流的唯一载体结构。
// 所有 token 用量必须经 RecordUsage(record) 流转，不允许其他模块自维护 token 字段。
// AgentID 用于 N1 子任务用量归属（根任务为 ""，compact 为 "compact"）；
// by-model/by-agent 分桶由 ai.UsageTracker 维护，metrics 包仅全局累加。
//
// ★ 2026-10-05（D 批）：原名为 CostRecord 且带 `CostCents float64` 字段。
// 那个金额是我们自己按价格表估的（既非 provider 上报、也非账单，且混币种），
// 已随成本线删除；剩下的四项 token 是 provider 真报的事实，保留。
type UsageRecord struct {
	AgentID       string    // N1 的 Agent ID（子任务用量归属，根任务为 ""，compact 为 "compact"）
	Model         string    // 模型名（用于 by-model 分桶，可选）
	InputTokens   int64     // 输入 token 数
	OutputTokens  int64     // 输出 token 数
	CacheCreation int64     // N3 的 cache creation tokens
	CacheRead     int64     // N3 的 cache read tokens
	Timestamp     time.Time // 记录时间（用于未来日/月聚合）
}

// RecordUsage 累加 4 维 token 用量到全局 Metrics。
// AgentID/Model/Timestamp 当前仅记录，by-model/by-agent 分桶由 ai.UsageTracker 维护。
func (m *Metrics) RecordUsage(record UsageRecord) {
	m.InputTokens.Add(record.InputTokens)
	m.OutputTokens.Add(record.OutputTokens)
	m.CacheCreationTokens.Add(record.CacheCreation)
	m.CacheReadTokens.Add(record.CacheRead)
}

// CacheHitRate returns the cache hit rate (0-1)
func (m *Metrics) CacheHitRate() float64 {
	hits := m.CacheHits.Load()
	misses := m.CacheMisses.Load()
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// Snapshot returns a snapshot of all metrics
func (m *Metrics) Snapshot() map[string]interface{} {
	return map[string]interface{}{
		"ai_requests_total":     m.AIRequestCount.Load(),
		"ai_request_errors":     m.AIRequestErrors.Load(),
		"ai_avg_duration_ms":    m.avgAIDuration(),
		"cache_hits":            m.CacheHits.Load(),
		"cache_misses":          m.CacheMisses.Load(),
		"cache_hit_rate":        fmt.Sprintf("%.2f", m.CacheHitRate()),
		"tool_executions":       m.ToolExecutionCount.Load(),
		"tool_execution_errors": m.ToolExecutionErrors.Load(),
		"active_sessions":       m.ActiveSessions.Load(),
		"input_tokens_total":    m.InputTokens.Load(),
		"output_tokens_total":   m.OutputTokens.Load(),
		"cache_creation_tokens": m.CacheCreationTokens.Load(),
		"cache_read_tokens":     m.CacheReadTokens.Load(),
		"security_events_total": m.SecurityEvents.Load(),
	}
}

// avgAIDuration computes the average AI request duration in ms
func (m *Metrics) avgAIDuration() int64 {
	count := m.AIRequestCount.Load()
	if count == 0 {
		return 0
	}
	return m.AIRequestDurationMs.Load() / count
}
