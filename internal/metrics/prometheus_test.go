package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// === 5-E-3 Prometheus registry 隔离测试 ===

// TestPrometheusRegistry_NoDuplicatePanic 验证用 NewRegistry 隔离后重复调用 PrometheusHandler 不 panic（5-E-3）。
// 原 init() 用全局 prometheus.MustRegister 会 panic "duplicate registration"，
// 改用 customRegistry.MustRegister 后多次 import / 多次调用 Handler 均安全。
func TestPrometheusRegistry_NoDuplicatePanic(t *testing.T) {
	// 多次调用 PrometheusHandler 不应 panic
	h1 := PrometheusHandler()
	h2 := PrometheusHandler()

	if h1 == nil || h2 == nil {
		t.Fatal("PrometheusHandler 返回 nil")
	}
	// 两个 handler 应共享同一 customRegistry，不重复注册
}

// TestPrometheusHandler_ServesMetrics 验证 handler 能响应 /metrics 请求并返回 Prometheus 格式指标（5-E-3）。
// 用 httptest.NewRequest/NewRecorder（不绑端口），本机 Winsock 损坏不影响。
func TestPrometheusHandler_ServesMetrics(t *testing.T) {
	// 记录一个 token usage（触发指标更新）
	AddTokenUsage("input", "test-model", 100)

	h := PrometheusHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "whale_token_usage_total") {
		t.Errorf("响应缺少 whale_token_usage_total 指标")
	}
}
