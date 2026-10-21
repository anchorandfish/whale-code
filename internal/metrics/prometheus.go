// Package metrics — Prometheus metrics and health endpoint for monitoring
// prometheus.go — Prometheus 指标定义与注册
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// customRegistry 是 5-E-3 引入的独立 Prometheus 注册表，
// 替代 prometheus.DefaultRegistry，避免测试或多次 init() 重复注册导致 panic。
var customRegistry = prometheus.NewRegistry()

var (
	// aiRequestDuration tracks AI request duration in seconds by model and status
	aiRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "whale_ai_request_duration_seconds",
		Help:    "AI request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"model", "status"})

	// cacheHitRate tracks cache hit rate (0-1) by type
	cacheHitRate = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "whale_cache_hit_rate",
		Help: "Cache hit rate (0-1)",
	}, []string{"type"})

	// toolExecutionDuration tracks tool execution duration in seconds by tool name and status
	toolExecutionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "whale_tool_execution_duration_seconds",
		Help:    "Tool execution duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"tool_name", "status"})

	// activeSessions tracks the number of active sessions
	activeSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "whale_active_sessions",
		Help: "Number of active sessions",
	})

	// tokenUsage tracks total token usage by type and model
	tokenUsage = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "whale_token_usage_total",
		Help: "Total token usage",
	}, []string{"type", "model"})

	// securityEvents §4.6：安全拦截事件计数器（按事件类型分桶）。
	// type 标签值：ssrf（SSRF 拦截）/ jwt（JWT 拒绝）/ ssh（SSH 私钥权限检查失败）。
	// 供 Prometheus 抓取，运维可按 type 聚合查看各类安全事件发生频率。
	securityEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "whale_security_events_total",
		Help: "Total security events blocked (ssrf/jwt/ssh etc.)",
	}, []string{"type"})
)

func init() {
	// 5-E-3：改用 customRegistry.MustRegister 替代 prometheus.MustRegister，
	// 避免全局注册表重复注册 panic（测试多次 init 或集成场景下尤其重要）。
	customRegistry.MustRegister(aiRequestDuration, cacheHitRate, toolExecutionDuration, activeSessions, tokenUsage, securityEvents)
}

// RecordAIRequestPrometheus records an AI request duration in the Prometheus histogram
func RecordAIRequestPrometheus(model, status string, durationSeconds float64) {
	aiRequestDuration.WithLabelValues(model, status).Observe(durationSeconds)
}

// SetCacheHitRate sets the cache hit rate gauge
func SetCacheHitRate(cacheType string, rate float64) {
	cacheHitRate.WithLabelValues(cacheType).Set(rate)
}

// RecordToolExecutionPrometheus records a tool execution duration in the Prometheus histogram
func RecordToolExecutionPrometheus(toolName, status string, durationSeconds float64) {
	toolExecutionDuration.WithLabelValues(toolName, status).Observe(durationSeconds)
}

// SetActiveSessions sets the number of active sessions
func SetActiveSessions(count float64) {
	activeSessions.Set(count)
}

// AddTokenUsage adds token usage to the counter
func AddTokenUsage(tokenType, model string, count float64) {
	tokenUsage.WithLabelValues(tokenType, model).Add(count)
}

// RecordSecurityEventPrometheus 记录一次安全拦截事件到 Prometheus CounterVec（§4.6）。
// eventType 标识事件类型（ssrf/jwt/ssh），与 metrics.Metrics.RecordSecurityEvent 配对使用。
// 调用方应同时调用 metrics.Default.RecordSecurityEvent(eventType) 更新本地总数。
func RecordSecurityEventPrometheus(eventType string) {
	securityEvents.WithLabelValues(eventType).Inc()
}

// PrometheusHandler 返回基于 customRegistry 的 Prometheus HTTP handler（5-E-3 导出）。
// 替代原 promhttp.Handler()（依赖全局 registry），供 StartMetricsServer 与测试使用。
func PrometheusHandler() http.Handler {
	return promhttp.HandlerFor(customRegistry, promhttp.HandlerOpts{})
}

// newPrometheusHandler returns the Prometheus HTTP handler for serving metrics
// 保留旧函数名以兼容现有测试（server_test.go 调用 newPrometheusHandler），
// 内部转调 PrometheusHandler。
func newPrometheusHandler() http.Handler {
	return PrometheusHandler()
}
