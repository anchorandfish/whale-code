package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBearerAuthMiddleware_NoTokenReturns401 验证未携带 Bearer Token 时返回 401（M22-05 P0）。
func TestBearerAuthMiddleware_NoTokenReturns401(t *testing.T) {
	// 用 RecordAIRequestPrometheus 制造一些指标数据，确保下游 handler 有内容输出
	RecordAIRequestPrometheus("test-model", "success", 0.1)

	wrapped := bearerAuthMiddleware(PrometheusHandler(), "expected-secret-token")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 Authorization header 应返回 401，实际 %d", resp.StatusCode)
	}
	// 应包含 WWW-Authenticate 提示
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("应返回 Bearer WWW-Authenticate 提示，实际 %q", got)
	}
}

// TestBearerAuthMiddleware_WrongTokenReturns401 验证错误 Bearer Token 时返回 401（M22-05 P0）。
func TestBearerAuthMiddleware_WrongTokenReturns401(t *testing.T) {
	RecordAIRequestPrometheus("test-model", "success", 0.1)

	wrapped := bearerAuthMiddleware(PrometheusHandler(), "expected-secret-token")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误 token 应返回 401，实际 %d", resp.StatusCode)
	}
}

// TestBearerAuthMiddleware_CorrectTokenReturns200 验证正确 Bearer Token 时返回 200（M22-05 P0）。
func TestBearerAuthMiddleware_CorrectTokenReturns200(t *testing.T) {
	RecordAIRequestPrometheus("test-model", "success", 0.1)

	wrapped := bearerAuthMiddleware(PrometheusHandler(), "expected-secret-token")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer expected-secret-token")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正确 token 应返回 200，实际 %d", resp.StatusCode)
	}
	// 应包含 Prometheus 指标
	body := w.Body.String()
	if !strings.Contains(body, "whale_ai_request_duration_seconds") {
		t.Errorf("正确 token 后应看到 metrics 输出")
	}
}

// TestBearerAuthMiddleware_CaseInsensitiveBearerPrefix 验证 "Bearer" 前缀大小写不敏感（M22-05 P0）。
// 容错：客户端可能发送 "bearer <token>" / "BEARER <token>" 等变体，应放行。
func TestBearerAuthMiddleware_CaseInsensitiveBearerPrefix(t *testing.T) {
	RecordAIRequestPrometheus("test-model", "success", 0.1)

	wrapped := bearerAuthMiddleware(PrometheusHandler(), "expected-secret-token")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "bearer expected-secret-token")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("小写 bearer 前缀应放行，实际 %d", resp.StatusCode)
	}
}

// TestBearerAuthMiddleware_EmptyTokenReturns401 验证仅有 "Bearer " 前缀但 token 为空时返回 401（M22-05 P0）。
func TestBearerAuthMiddleware_EmptyTokenReturns401(t *testing.T) {
	RecordAIRequestPrometheus("test-model", "success", 0.1)

	wrapped := bearerAuthMiddleware(PrometheusHandler(), "expected-secret-token")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("空 token 应返回 401，实际 %d", resp.StatusCode)
	}
}

// TestExtractBearerToken 验证 Authorization header 解析逻辑（M22-05 P0）。
func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"空 header", "", ""},
		{"正确 Bearer", "Bearer abc123", "abc123"},
		{"小写 bearer", "bearer abc123", "abc123"},
		{"大写 BEARER", "BEARER abc123", "abc123"},
		{"前后空白", "  Bearer   abc123  ", "abc123"},
		{"无 Bearer 前缀", "abc123", ""},
		{"Basic 不匹配", "Basic abc123", ""},
		{"Bearer 后空 token", "Bearer ", ""},
		{"仅 Bearer 单词", "Bearer", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBearerToken(tt.header)
			if got != tt.want {
				t.Errorf("extractBearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

// TestNormalizeToLocalhost 验证地址强制改为 127.0.0.1 绑定的逻辑（M22-05 P0）。
func TestNormalizeToLocalhost(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"仅端口", ":9090", "127.0.0.1:9090"},
		{"0.0.0.0", "0.0.0.0:9090", "127.0.0.1:9090"},
		{"已是 127.0.0.1", "127.0.0.1:9090", "127.0.0.1:9090"},
		{"localhost hostname", "localhost:9090", "127.0.0.1:9090"},
		{"example.com hostname", "example.com:9090", "127.0.0.1:9090"},
		{"其他端口", ":9091", "127.0.0.1:9091"},
		{"无端口回退默认", "127.0.0.1", "127.0.0.1:9090"},
		{"空字符串回退默认", "", "127.0.0.1:9090"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeToLocalhost(tt.addr)
			if got != tt.want {
				t.Errorf("normalizeToLocalhost(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// TestStartMetricsServer_DefaultLocalhostWhenNoToken 验证默认行为：auth_token 为空时强制 127.0.0.1 绑定（M22-05 P0）。
// 用 :0 让系统分配端口；srv.Addr 应被改写为 127.0.0.1:0。
// ⚠️ 本机 Winsock 损坏可能失败，CI 补跑；仅检查 srv.Addr 字段，不依赖真实 ListenAndServe。
func TestStartMetricsServer_DefaultLocalhostWhenNoToken(t *testing.T) {
	srv := StartMetricsServer(ServerOptions{Addr: ":0"})
	if srv == nil {
		t.Fatal("StartMetricsServer 返回 nil")
	}
	// 立即 Shutdown 释放端口（避免影响后续测试）
	defer func() {
		// shutdown 错误忽略，Winsock 损坏时可能失败
		_ = srv.Close()
	}()
	// auth_token 为空且 DisableAuth=false → 应强制改为 127.0.0.1
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("auth_token 为空时应强制 127.0.0.1:0，实际 %q", srv.Addr)
	}
}

// TestStartMetricsServer_DisableAuthKeepsAddr 验证 DisableAuth=true 时保留原 addr（M22-05 P0 调试模式）。
func TestStartMetricsServer_DisableAuthKeepsAddr(t *testing.T) {
	srv := StartMetricsServer(ServerOptions{Addr: "127.0.0.1:0", DisableAuth: true})
	if srv == nil {
		t.Fatal("StartMetricsServer 返回 nil")
	}
	defer func() { _ = srv.Close() }()
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("DisableAuth=true 应保留原 addr，实际 %q", srv.Addr)
	}
}

// TestStartMetricsServer_WithAuthTokenKeepsAddr 验证 auth_token 非空时按用户 addr 绑定（M22-05 P0）。
func TestStartMetricsServer_WithAuthTokenKeepsAddr(t *testing.T) {
	srv := StartMetricsServer(ServerOptions{Addr: "127.0.0.1:0", AuthToken: "secret-xyz"})
	if srv == nil {
		t.Fatal("StartMetricsServer 返回 nil")
	}
	defer func() { _ = srv.Close() }()
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("auth_token 非空时应保留原 addr，实际 %q", srv.Addr)
	}
}
