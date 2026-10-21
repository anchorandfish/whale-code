// Package daemon — Prometheus metrics 与 health HTTP 端点
//
// 在 daemon 中启动 HTTP server，提供 /metrics 和 /health 端点。
// /metrics 返回 Prometheus 文本格式指标（手写文本格式，不依赖 prometheus client 库）。
// /health 返回 daemon.HealthCheck() 的 JSON。
// 鉴权：authToken 非空时要求 Authorization: Bearer <token>；为空时强制绑定 127.0.0.1。
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/metrics"
)

// 默认 metrics 监听地址（仅本地回环，安全默认值）
const defaultMetricsAddr = "127.0.0.1:0"

// startMetricsServer 启动 metrics + health HTTP 端点。
// 在 daemon.Start() 中调用，返回 *http.Server 供 daemon.Stop() 关闭。
// addr 为空时使用默认 "127.0.0.1:0"，由操作系统分配空闲端口。
// authToken 非空时要求请求携带 Authorization: Bearer <token>；为空时强制绑定 127.0.0.1。
//
// 桶 X3 病灶4 修复（fail-closed 鉴权）：
// - 空 token + 非 127.0.0.1 绑定 → 返回 error 拒绝启动（fail-closed，防无鉴权网络暴露）
// - 空 token + 127.0.0.1（或 ::1）绑定 → 允许（仅本机访问，无鉴权但安全）
// - 非空 token + 任意绑定 → 允许（有鉴权保护）
//
// B5-P1-12 修复：isLocalhostAddr 不再接受 "localhost"（可被 /etc/hosts 篡改），仅允许 127.0.0.1 / ::1。
//
// 修复前：仅在 addr==":9090" 时强制改 127.0.0.1，用户配置 0.0.0.0:9090 + 空 token 仍会暴露。
// 修复后：任何非 127.0.0.1/::1 绑定 + 空 token 都拒绝启动。
func (d *Daemon) startMetricsServer(addr string, authToken string) (*http.Server, error) {
	if addr == "" {
		addr = defaultMetricsAddr
	}
	// 修复：用户配置 "localhost:9090" 时自动改写为 "127.0.0.1:9090"。
	// B5-P1-12 原修复移除了 "localhost" case（fail-closed），但用户 settings.json
	// 可能配置 "localhost:9090"，升级后会启动失败。自动改写保持 fail-closed 安全
	// 语义（仍只绑定回环地址），同时提供向后兼容（用户无需手动改配置）。
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "localhost" {
		if d.logger != nil {
			d.logger.Info("WARN: metrics_addr contains 'localhost', auto-rewriting to '127.0.0.1' for security (B5-P1-12 fail-closed compatibility)")
		}
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	// 桶 X3 病灶4：fail-closed 检查 — 空 token + 非 127.0.0.1 绑定拒绝启动
	if authToken == "" && !isLocalhostAddr(addr) {
		return nil, fmt.Errorf("metrics server: auth token is empty but bind addr %q is not localhost (fail-closed; set MetricsAuthToken or bind to 127.0.0.1)", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", d.withAuth(authToken, d.handleMetrics))
	mux.HandleFunc("/health", d.withAuth(authToken, d.handleHealth))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics server: listen on %s: %w", addr, err)
	}
	srv.Addr = listener.Addr().String()

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			if d.logger != nil {
				d.logger.Error("Metrics server error: %v", err)
			}
		}
	}()

	return srv, nil
}

// isLocalhostAddr 判断 addr 是否为本机回环地址。
// B5-P1-12 修复：移除 "localhost" case，仅允许 127.0.0.1 / ::1（数字 IP）。
// localhost 可能被 /etc/hosts 篡改解析到非回环地址，导致空 token 绑定暴露到外网。
// addr 格式："host:port"，提取 host 部分判断。
func isLocalhostAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		// An empty host (":9090") binds all interfaces, not localhost.
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// withAuth 是 Bearer Token 鉴权中间件。
//
// authToken 为空时直接放行（Q1=A：回环=受信域）。该分流的安全性由
// startMetricsServer 的 fail-closed 检查兜底：空 token + 非回环绑定会拒绝启动，
// 因此"空 token 直通"不可能暴露到回环之外（对齐业内既有实现
// is_unauthenticated_non_loopback_listener）。
//
// authToken 非空时检查 Authorization: Bearer <token>，不匹配返回 401。
// PCR G-P1-1 收敛（I1）：比较逻辑委托给包内单一强制点 authorize（auth.go）。
func (d *Daemon) withAuth(authToken string, next http.HandlerFunc) http.HandlerFunc {
	if authToken == "" {
		return next
	}
	expected := "Bearer " + authToken
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(expected, r.Header.Get("Authorization")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// handleMetrics 返回 Prometheus 文本格式指标。
// 手写文本格式，不依赖 prometheus client 库。
// 暴露以下指标：
// - whale_daemon_uptime_seconds（gauge）：daemon 运行时间（秒）
// - whale_daemon_workers_active（gauge）：活跃 worker 数
// - whale_daemon_tasks_pending（gauge）：队列中待处理任务数
// - whale_daemon_tasks_running（gauge）：正在执行的任务数
// - whale_daemon_storage_degraded（gauge）：持久化存储是否降级
//
// ★ D 批：原 whale_cost_total_cents（美分累计成本）已删除。
func (d *Daemon) handleMetrics(w http.ResponseWriter, r *http.Request) {
	status := d.Status()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")

	// whale_daemon_uptime_seconds
	uptime := status.Uptime.Seconds()
	fmt.Fprintf(w, "# HELP whale_daemon_uptime_seconds Daemon uptime in seconds\n")
	fmt.Fprintf(w, "# TYPE whale_daemon_uptime_seconds gauge\n")
	fmt.Fprintf(w, "whale_daemon_uptime_seconds %.4f\n", uptime)

	// whale_daemon_workers_active
	fmt.Fprintf(w, "# HELP whale_daemon_workers_active Number of active (busy) workers\n")
	fmt.Fprintf(w, "# TYPE whale_daemon_workers_active gauge\n")
	fmt.Fprintf(w, "whale_daemon_workers_active %d\n", status.RunningTasks)

	// whale_daemon_tasks_pending
	fmt.Fprintf(w, "# HELP whale_daemon_tasks_pending Number of pending tasks in queue\n")
	fmt.Fprintf(w, "# TYPE whale_daemon_tasks_pending gauge\n")
	fmt.Fprintf(w, "whale_daemon_tasks_pending %d\n", status.PendingTasks)

	// whale_daemon_tasks_running
	fmt.Fprintf(w, "# HELP whale_daemon_tasks_running Number of currently running tasks\n")
	fmt.Fprintf(w, "# TYPE whale_daemon_tasks_running gauge\n")
	fmt.Fprintf(w, "whale_daemon_tasks_running %d\n", status.RunningTasks)

	storageDegraded := 0
	if status.StorageHealth != "healthy" {
		storageDegraded = 1
	}
	fmt.Fprintf(w, "# HELP whale_daemon_storage_degraded Whether durable task storage is degraded\n")
	fmt.Fprintf(w, "# TYPE whale_daemon_storage_degraded gauge\n")
	fmt.Fprintf(w, "whale_daemon_storage_degraded %d\n", storageDegraded)

	// D 批：原 `whale_cost_total_cents` 指标已删除。
	// 它是本地价格表的估算金额（provider 不上报、也不等于账单，且混币种），
	// 暴露出来只会让监控面板看到一个来源不明的数字。
	// 增加 nil 检查，防止 metrics.Default 未初始化时 panic
	if metrics.Default != nil {
		// §4.6 安全事件总数（SSRF/JWT/SSH 等拦截事件）
		// 按事件类型的分桶由 Prometheus CounterVec 维护，此处仅输出全局总数。
		fmt.Fprintf(w, "# HELP whale_security_events_total Total security events blocked\n")
		fmt.Fprintf(w, "# TYPE whale_security_events_total counter\n")
		fmt.Fprintf(w, "whale_security_events_total %d\n", metrics.Default.SecurityEvents.Load())
	}
}

// handleHealth 返回 daemon.HealthCheck() 的 JSON。
func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := d.HealthCheck()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// stopMetricsServer 优雅关闭 metrics HTTP server（最多等待 2s）。
func (d *Daemon) stopMetricsServer() {
	d.mu.Lock()
	server := d.metricsServer
	d.metricsServer = nil
	d.mu.Unlock()
	shutdownMetricsServer(server)
}

func shutdownMetricsServer(server *http.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
