// Package metrics — Prometheus metrics and health endpoint for monitoring
// server.go — 指标 HTTP 端点（M22-05 P0 增强：Bearer Token 鉴权 + 127.0.0.1 默认绑定）
package metrics

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// ServerOptions 是 StartMetricsServer 的可选参数（M22-05 P0 引入）。
//
// 字段语义：
//   - Addr：监听地址（如 ":9090" / "127.0.0.1:9090"）；为空时使用默认 ":9090"
//   - AuthToken：Bearer Token 鉴权令牌；非空时 /metrics 请求须携带
//     `Authorization: Bearer <AuthToken>` 才能访问
//   - DisableAuth：true 时禁用所有鉴权与 127.0.0.1 强制（仅用于本地调试，不推荐生产）
//
// 行为矩阵：
//   - AuthToken=="" && DisableAuth==false：强制 127.0.0.1 绑定（默认安全）
//   - AuthToken=="" && DisableAuth==true：按 Addr 绑定（调试模式，全网开放由调用方负责）
//   - AuthToken!=""：按 Addr 绑定 + 强制 Bearer Token 鉴权
type ServerOptions struct {
	Addr        string
	AuthToken   string
	DisableAuth bool
}

// StartMetricsServer 启动 metrics + health HTTP 端点（5-E-3 破坏性变更 + M22-05 P0 鉴权增强）。
//
// 签名变更历史：
//   - 5-E-3：从返回 error 改为返回 *http.Server（goroutine 内部 ListenAndServe）
//   - M22-05 P0：新增 opts ServerOptions 参数，支持 Bearer Token 鉴权与 127.0.0.1 默认绑定
//
// 调用方持有返回值并在退出路径调 srv.Shutdown(ctx) 实现优雅关闭。
// opts.Addr 与 opts.AuthToken 为空时按 ServerOptions 行为矩阵兜底。
func StartMetricsServer(opts ServerOptions) *http.Server {
	addr := opts.Addr
	if addr == "" {
		addr = ":9090"
	}

	// M22-05：默认安全（auth_token 为空且未禁用鉴权）→ 强制 127.0.0.1 绑定
	// 避免同机其他用户通过 :9090 / 0.0.0.0:9090 抓取 token 用量等敏感运维数据
	if opts.AuthToken == "" && !opts.DisableAuth {
		addr = normalizeToLocalhost(addr)
	}

	mux := http.NewServeMux()
	// /metrics 路径走鉴权 middleware（auth_token 非空时）
	metricsHandler := PrometheusHandler()
	if opts.AuthToken != "" {
		metricsHandler = bearerAuthMiddleware(metricsHandler, opts.AuthToken)
	}
	mux.Handle("/metrics", metricsHandler)
	mux.HandleFunc("/health", healthHandler)

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[metrics] HTTP server error: %v", err)
		}
	}()
	return srv
}

// bearerAuthMiddleware 包装下游 handler，要求请求携带正确的 Bearer Token。
//
// 校验规则：
//   - 从 `Authorization` header 提取 `Bearer <token>` 前缀
//   - 用 crypto/subtle.ConstantTimeCompare 做 constant-time 比较避免时序侧信道
//   - 不匹配时返回 401 Unauthorized + WWW-Authenticate 提示
//
// /health 端点不走此 middleware（健康检查不需要鉴权，便于探活）。
func bearerAuthMiddleware(next http.Handler, expectedToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r.Header.Get("Authorization"))
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="whale-code-metrics"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractBearerToken 从 Authorization header 提取 Bearer token 值。
//
// 期望格式：`Authorization: Bearer <token>`（大小写不敏感前缀，token 原样保留）。
// 不匹配前缀或空 token 时返回空字符串。
func extractBearerToken(headerValue string) string {
	if headerValue == "" {
		return ""
	}
	// 容忍前后空白
	v := strings.TrimSpace(headerValue)
	prefix := "Bearer "
	if len(v) < len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

// normalizeToLocalhost 将监听地址强制改为 127.0.0.1 绑定（保留端口）。
//
// 处理三种格式：
//   - ":9090"           → "127.0.0.1:9090"
//   - "0.0.0.0:9090"    → "127.0.0.1:9090"
//   - "127.0.0.1:9090"  → "127.0.0.1:9090"（不变）
//   - "example.com:9090"→ "127.0.0.1:9090"（hostname 也降级到 127.0.0.1）
//
// 端口解析失败时回退到默认 :9090。
func normalizeToLocalhost(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		// 无端口或格式异常：直接拼接 127.0.0.1:9090
		return "127.0.0.1:9090"
	}
	if port == "" {
		port = "9090"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// healthHandler 返回健康状态 JSON（不要求鉴权，便于探活）。
func healthHandler(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status":     "ok",
		"timestamp":  time.Now().Format(time.RFC3339),
		"goroutines": runtime.NumGoroutine(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
