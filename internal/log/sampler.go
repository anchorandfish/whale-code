// Package log — M22-03 日志采样 Sampler 中间件（SNAPSHOT）
//
// 本文件实现 slog Sampler 中间件，对高频日志进行采样以降低 I/O 开销。
// 对标 slog Sampler（https://pkg.go.dev/golang.org/x/exp/slog#Sampler）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：SamplerHandler 包装 slog.Handler，按策略采样
// - 结构体字段：SamplerConfig 含 Rate/Level/Counter
// - 分层权责：本文件只做"采样"，脱敏由 redact_handler.go
// - 降级策略：采样计数失败时全部记录（避免日志丢失）
// - TTL规则：计数窗口 1 分钟（samplerWindowSize）
package log

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// samplerWindowSize 是采样计数窗口（1 分钟，对标 prometheus 采样窗口）。
const samplerWindowSize = 1 * time.Minute

// SamplerConfig 是采样配置（SNAPSHOT-1）。
//
// 字段说明：
// - Rate: 采样率（1/Rate，如 100 表示每 100 条记录 1 条）
// - Level: 仅对该级别及以上采样（低于此级别全部记录）
// - WindowSize: 计数窗口（默认 1 分钟）
type SamplerConfig struct {
	Rate       int           // 采样率倒数（1/Rate），0 或 1 表示不采样
	Level      slog.Level    // 采样生效的最低级别
	WindowSize time.Duration // 计数窗口
}

// SamplerHandler 是采样 slog.Handler 包装器（SNAPSHOT-2）。
//
// 行为：
// - 低于 Level 的日志全部记录
// - 高于等于 Level 的日志按 1/Rate 采样
// - 计数窗口内 rate-1 条丢弃，第 rate 条记录
// - 窗口结束后计数重置
type SamplerHandler struct {
	inner       slog.Handler
	cfg         SamplerConfig
	mu          sync.Mutex
	count       atomic.Int64
	windowStart time.Time
}

// NewSamplerHandler 创建采样 handler 包装器（SNAPSHOT-3）。
//
// inner 是底层 handler。
// cfg.Rate <= 1 时禁用采样（全部记录）。
func NewSamplerHandler(inner slog.Handler, cfg SamplerConfig) *SamplerHandler {
	if cfg.WindowSize == 0 {
		cfg.WindowSize = samplerWindowSize
	}
	return &SamplerHandler{
		inner:       inner,
		cfg:         cfg,
		windowStart: time.Now(),
	}
}

// Enabled 委托给底层 handler（SNAPSHOT-4）。
func (h *SamplerHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle 按采样策略决定是否记录（SNAPSHOT-5）。
//
// 策略：
// - level < cfg.Level → 全部记录
// - level >= cfg.Level → 按 1/Rate 采样
// - Rate <= 1 → 全部记录
func (h *SamplerHandler) Handle(ctx context.Context, r slog.Record) error {
	// 不采样的情况
	if h.cfg.Rate <= 1 || r.Level < h.cfg.Level {
		return h.inner.Handle(ctx, r)
	}

	// 检查窗口是否过期
	h.mu.Lock()
	now := time.Now()
	if now.Sub(h.windowStart) >= h.cfg.WindowSize {
		h.windowStart = now
		h.count.Store(0)
	}
	h.mu.Unlock()

	// 原子递增并判断是否采样
	count := h.count.Add(1)
	// 每 Rate 条记录 1 条
	if count%int64(h.cfg.Rate) != 1 {
		return nil // 丢弃
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs 委托给底层 handler（SNAPSHOT-6）。
func (h *SamplerHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SamplerHandler{
		inner:       h.inner.WithAttrs(attrs),
		cfg:         h.cfg,
		windowStart: h.windowStart,
	}
}

// WithGroup 委托给底层 handler（SNAPSHOT-7）。
func (h *SamplerHandler) WithGroup(name string) slog.Handler {
	return &SamplerHandler{
		inner:       h.inner.WithGroup(name),
		cfg:         h.cfg,
		windowStart: h.windowStart,
	}
}

// CountValue 返回当前窗口计数（导出供测试用， SNAPSHOT-8）。
func (h *SamplerHandler) CountValue() int64 {
	return h.count.Load()
}

// NewChainedHandler 链式组合 RedactHandler + SamplerHandler（SNAPSHOT-9）。
//
// 组合顺序：Sampler → Redact → Inner
// 先采样再脱敏，避免脱敏 CPU 开销浪费在被丢弃的日志上。
//
// 参数：
// - inner: 最底层 handler（如 JSONHandler/TextHandler）
// - samplerCfg: 采样配置（Rate<=1 时跳过采样层）
// - redact: 是否启用脱敏层
func NewChainedHandler(inner slog.Handler, samplerCfg SamplerConfig, redact bool) slog.Handler {
	handler := inner
	if redact {
		handler = NewRedactHandler(handler)
	}
	if samplerCfg.Rate > 1 {
		handler = NewSamplerHandler(handler, samplerCfg)
	}
	return handler
}
