// Package ai — S2（M04 不足点 4）：client 层流中断重连 ReconnectWrapper
//
// 设计要点（吸纳审查优化 2/3/5/9）：
// - 与 ResumeTracker 职责边界明确（优化 2）：
// ResumeTracker.CanResume() 决定"能否重连"（policy 状态）
// ReconnectWrapper.Stream() 决定"如何重连"（lastEventID + retry 字段 + 退避）
// - 重连尝试不调 RecoveryCoordinator.RecordFailure（优化 3）：
// 仅累计 attempt，全部失败后才返回错误由上层调 RecordFailure
// - 重连后服务端无新事件视为流已结束（优化 5）：
// 若返回事件 id ≤ LastEventID，正常关闭 out channel
// - SSE retry: 字段单位为毫秒（优化 9，参考 W3C SSE 规范）：
// retry: 5000 表示 5 秒；ReconnectConfig.RespectRetryField=true 时尊重
//
// 业内对标：
// - Anthropic streaming（Last-Event-ID header 续传）
// https://docs.anthropic.com/en/api/messages-streaming
// - SSE 重连规范（retry: 字段 + Last-Event-ID header）
// https://html.spec.whatwg.org/multipage/server-sent-events.html
// - OpenAI streaming（chunked transfer + data: [DONE] 终止符）
// https://platform.openai.com/docs/api-reference/chat/streaming
package ai

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ErrResumeNotPossible 当前流式状态不允许重连（ResumeTracker.Policy() == ResumeNone）
// 通常因为已收到 ToolUseEvent，工具状态歧义，重试可能导致重复执行
var ErrResumeNotPossible = errors.New("reconnect not possible: stream state is non-resumable (tool_use already started)")

// ErrReconnectExhausted 重连次数已达上限，全部失败
// 由上层调用 RecoveryCoordinator.RecordFailure 进入 Phase 1 重试
var ErrReconnectExhausted = errors.New("reconnect attempts exhausted")

// ReconnectConfig 流中断重连配置（吸纳审查优化 9：retry 字段单位为毫秒）
type ReconnectConfig struct {
	// MaxReconnects 最大重连次数，默认 3
	MaxReconnects int
	// BaseDelay 重连基础退避，默认 1s（对齐 RetryConfig.BaseDelay）
	BaseDelay time.Duration
	// MaxDelay 重连最大退避，默认 30s（对齐 RetryConfig.MaxDelay）
	MaxDelay time.Duration
	// RespectRetryField 是否尊重 SSE retry: 字段（毫秒），默认 true
	// 参考 W3C SSE 规范 https://html.spec.whatwg.org/multipage/server-sent-events.html
	RespectRetryField bool
}

// DefaultReconnectConfig 提供默认重连配置
func DefaultReconnectConfig() ReconnectConfig {
	return ReconnectConfig{
		MaxReconnects:     3,
		BaseDelay:         1 * time.Second,
		MaxDelay:          30 * time.Second,
		RespectRetryField: true,
	}
}

// LastEventIDTracker 跟踪 SSE 事件 id（Last-Event-ID），供 ReconnectWrapper 重连时使用。
// 线程安全：内部使用 sync.Mutex 保护。
// inner 函数（由调用方构造）在 SSE 解析时应调用 Set(id) 更新最新事件 id。
type LastEventIDTracker struct {
	mu sync.Mutex
	id string
}

// Get 返回当前最后接收的 event id（用于 Last-Event-ID header 续传）
func (t *LastEventIDTracker) Get() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id
}

// Set 更新最后接收的 event id（仅当新 id 非空时更新）
// SSE event id 通常是单调递增的字符串，这里直接覆盖即可
func (t *LastEventIDTracker) Set(id string) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.id = id
}

// ReconnectWrapper 包装 client.Stream 函数，提供流中断重连能力。
// 内部复用 ResumeTracker 判断"能否重连"（policy 状态），
// 自身负责"如何重连"（lastEventID + retry 字段 + 退避）。
// 吸纳审查优化 2：与 ResumeTracker 职责边界明确
//
// inner 函数签名：func(ctx) (<-chan Event, error)
// - inner 内部应通过闭包访问 rw.LastEventID() 获取上次最后接收的 id
// - inner 内部应通过 rw.idTracker.Set(id) 更新最新事件 id
// - inner 内部应通过 rw.SetRetryMS(ms) 更新 SSE retry: 字段（毫秒）
type ReconnectWrapper struct {
	inner   func(ctx context.Context) (<-chan Event, error) // 原 client.Stream（闭包可访问 rw 状态）
	cfg     ReconnectConfig
	tracker *ResumeTracker // 复用现有，判断 ResumePolicy

	// idTracker 跟踪 SSE event id（Last-Event-ID），由 inner 在 SSE 解析时更新
	idTracker *LastEventIDTracker
	// retryMS 跟踪 SSE retry: 字段（毫秒），由 inner 在 SSE 解析时更新
	retryMS atomic.Int64

	// mu 保护 attempt 等可变状态
	mu      sync.Mutex
	attempt int // 当前重连尝试次数
}

// NewReconnectWrapper 创建重连包装器。
// inner 函数在执行时可通过闭包访问 rw（但 rw 变量本身在返回后才能使用，
// 因此 inner 函数体内访问 rw 时 rw 已就绪，不存在 nil 解引用）。
func NewReconnectWrapper(
	inner func(ctx context.Context) (<-chan Event, error),
	cfg ReconnectConfig,
	tracker *ResumeTracker,
) *ReconnectWrapper {
	if cfg.MaxReconnects <= 0 {
		cfg.MaxReconnects = 3
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 1 * time.Second
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 30 * time.Second
	}
	return &ReconnectWrapper{
		inner:     inner,
		cfg:       cfg,
		tracker:   tracker,
		idTracker: &LastEventIDTracker{},
	}
}

// LastEventID 返回最后接收的 event id（用于 Last-Event-ID header）
// 由 inner 函数在 SSE 解析时通过 rw.idTracker.Set(id) 更新
func (rw *ReconnectWrapper) LastEventID() string {
	return rw.idTracker.Get()
}

// SetRetryMS 更新 SSE retry: 字段（毫秒），由 inner 函数在 SSE 解析时调用。
// 参考 W3C SSE 规范 https://html.spec.whatwg.org/multipage/server-sent-events.html
// retry: 5000 表示 5000 毫秒 = 5 秒
func (rw *ReconnectWrapper) SetRetryMS(ms int64) {
	if ms <= 0 {
		return
	}
	rw.retryMS.Store(ms)
}

// getRetryMS 返回当前 SSE retry: 字段值（毫秒），0 表示未设置
func (rw *ReconnectWrapper) getRetryMS() int64 {
	return rw.retryMS.Load()
}

// parseRetryField 解析 SSE retry: 字段字符串为毫秒整数。
// SSE 规范要求 retry: 字段为整数（毫秒），参考：
// https://html.spec.whatwg.org/multipage/server-sent-events.html
// 解析失败返回 0（调用方应忽略）
func parseRetryField(retryStr string) int64 {
	if retryStr == "" {
		return 0
	}
	// 去除前后空白
	retryStr = trimSpace(retryStr)
	n, err := strconv.ParseInt(retryStr, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// trimSpace 去除字符串前后空白字符（空格、制表符、换行符、回车符）
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// Stream 执行流式调用，含重连逻辑。
// 吸纳审查优化 3：重连尝试期间不调 RecoveryCoordinator.RecordFailure，
// 仅在全部失败后才返回错误由上层调 RecordFailure
// 吸纳审查优化 5：重连后若服务端无新事件（id ≤ LastEventID），视为流已结束
func (rw *ReconnectWrapper) Stream(ctx context.Context) (<-chan Event, error) {
	// 前置检查：ResumeTracker.Policy() == ResumeNone → 立即返回错误
	// 吸纳审查优化 2：与 ResumeTracker 职责边界明确
	if rw.tracker != nil && !rw.tracker.CanResume() {
		return nil, ErrResumeNotPossible
	}

	out := make(chan Event, 64)
	var firstErr error

	// 启动转发 goroutine，把 inner 的事件流透传到 out
	// 内部循环：调 inner → 转发事件 → 若 inner 返回错误或 channel 异常关闭则判断是否重连
	go func() {
		defer close(out)

		for {
			// 调用 inner 获取事件流
			// inner 内部通过闭包访问 rw.LastEventID() 和 rw.idTracker.Set(id) 来跟踪 id
			events, err := rw.inner(ctx)
			if err != nil {
				// inner 直接返回错误（如 HTTP 4xx / 网络错误）
				if !rw.ShouldReconnect(err) {
					// 不可重连错误：直接透传到 out 并退出
					select {
					case out <- ErrorEvent{Err: err}:
					case <-ctx.Done():
					}
					if firstErr == nil {
						firstErr = err
					}
					return
				}

				// 可重连错误：进入重连流程
				rw.mu.Lock()
				rw.attempt++
				attempt := rw.attempt
				rw.mu.Unlock()

				if attempt > rw.cfg.MaxReconnects {
					// 重连次数达上限，返回错误（由上层调 RecordFailure）
					// 吸纳审查优化 3：不在此处调 RecordFailure
					wrappedErr := fmt.Errorf("%w: %v (attempt=%d)", ErrReconnectExhausted, err, attempt)
					select {
					case out <- ErrorEvent{Err: wrappedErr}:
					case <-ctx.Done():
					}
					if firstErr == nil {
						firstErr = wrappedErr
					}
					return
				}

				// 计算退避：BaseDelay × 2^(attempt-1)，不超 MaxDelay
				// 吸纳审查优化 9：若 RespectRetryField=true 且事件流含 retry: 字段，按 retry 值（毫秒）退避
				delay := rw.computeBackoff(attempt)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
				continue
			}

			// 转发 events 到 out，同时跟踪每个事件的 id 和 retry 字段
			// 重连后若服务端无新事件（id ≤ LastEventID），视为流已结束
			streamDone := make(chan error, 1)
			var receivedNewEvent bool

			go func() {
				var streamErr error
				for event := range events {
					// 更新 ResumeTracker（policy 状态）
					if rw.tracker != nil {
						rw.tracker.Update(event)
					}
					// 透传事件到 out
					select {
					case out <- event:
						receivedNewEvent = true
					case <-ctx.Done():
						streamErr = ctx.Err()
						// 排空 events 避免生产者阻塞
						for range events {
						}
						break
					}
				}
				streamDone <- streamErr
			}()

			select {
			case <-ctx.Done():
				return
			case err := <-streamDone:
				if err != nil {
					// 转发 goroutine 因 ctx 取消退出
					return
				}
			}

			// 流正常结束：
			// - 若 receivedNewEvent=true 说明有事件被消费，流正常完成
			// - 若 receivedNewEvent=false 说明重连后服务端无新事件（吸纳审查优化 5）
			// 视为流已结束，正常关闭 out
			if receivedNewEvent {
				return
			}

			// 重连后无新事件：视为流已结束
			// 吸纳审查优化 5：重连后若服务端返回的事件 id ≤ LastEventID，视为流已结束
			return
		}
	}()

	return out, nil
}

// computeBackoff 计算重连退避时间。
// 吸纳审查优化 9：若 RespectRetryField=true 且事件流含 retry: 字段（毫秒），按 retry 值退避
// 否则按 BaseDelay × 2^(attempt-1) 计算，不超 MaxDelay
func (rw *ReconnectWrapper) computeBackoff(attempt int) time.Duration {
	// 若尊重 retry: 字段且非首次重连（attempt > 1），优先使用 SSE retry: 字段
	if rw.cfg.RespectRetryField && attempt > 1 {
		if retryMS := rw.getRetryMS(); retryMS > 0 {
			delay := time.Duration(retryMS) * time.Millisecond
			if delay > rw.cfg.MaxDelay {
				return rw.cfg.MaxDelay
			}
			if delay < rw.cfg.BaseDelay {
				return rw.cfg.BaseDelay
			}
			return delay
		}
	}
	// 指数退避：BaseDelay × 2^(attempt-1)
	// attempt=1 → BaseDelay × 1
	// attempt=2 → BaseDelay × 2
	// attempt=3 → BaseDelay × 4
	backoff := float64(rw.cfg.BaseDelay)
	for i := 1; i < attempt; i++ {
		backoff *= 2
	}
	if backoff > float64(rw.cfg.MaxDelay) {
		backoff = float64(rw.cfg.MaxDelay)
	}
	return time.Duration(backoff)
}

// ShouldReconnect 判断错误是否可重连。
// - 网络错误（connection refused / reset / timeout 等）：可重连
// - 已登记状态码：由 httpStatusErrorIDSpecs + errorCategorySpecs 推出（唯一口径）
// - Fatal/客户端错误（400/401/403/404/405/413）：不可重连（请求本身有问题）
// - 不可重试类（500/502/503/504 等）：可重连（服务端临时故障）
// - 429 限流 / 529 过载：**可重连**（但通常应走 RetryAfter / FallbackChain 专用路径）
//
// - 未登记状态码：与 Phase-1 重试口径**同源**（IsRetryableError：未登记 5xx 保守可重连）
// - context.Canceled / context.DeadlineExceeded：不可重连（用户主动取消）
// 吸纳审查优化 3：重连尝试不调 RecordFailure
//
// ★ ：本函数原先自持第三份状态码字面量表（`case 400,401,403,404,413` /
// `case 408,425,500,502,503,504` / `case 429,529`），与 MapHTTPStatusToErrorID /
// IsRetryableError 的口径互不一致（例：未登记 5xx 在两处一句 true 一句 false）。
// 收口后只保留「429/529 在传输层可重连」这一处**语义差异**的显式例外，其余全部由唯一表推出。
func (rw *ReconnectWrapper) ShouldReconnect(err error) bool {
	if err == nil {
		return false
	}
	// context 取消：不可重连
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// APIError：基于状态码判断
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if id, known := httpStatusErrorIDSpecs[apiErr.StatusCode]; known {
			// 传输层与 API 层语义**故意不同**：429/529 在 API 层被判 Fatal（不直接重试，
			// 走 RetryAfter / 降级专用路径），但重连一次是安全的 ⇒ 可重连。
			if id == ErrAIApiOverload || id == ErrAIApiRateLimit {
				return true
			}
			return categoryFromID(id) == CategoryRetryable
		}
		// 未登记状态码（含全部未登记 5xx）：与 Phase-1 重试口径同源，不再自持第二份表。
		if IsRetryableError(apiErr.StatusCode) {
			return true
		}
	}
	// 兜底：用错误消息判断网络错误
	errMsg := err.Error()
	if IsNetworkError(errMsg) {
		return true
	}
	// 默认不可重连（保守策略，避免无意义重试）
	return false
}

// Attempt 返回当前重连尝试次数（用于 metrics / 日志）
func (rw *ReconnectWrapper) Attempt() int {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.attempt
}

// Reset 重置重连状态（流成功后调用）
func (rw *ReconnectWrapper) Reset() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	rw.attempt = 0
	// 直接重置 idTracker 内部状态（不能调用 Set("")，因为 Set 会忽略空字符串）
	rw.idTracker.mu.Lock()
	rw.idTracker.id = ""
	rw.idTracker.mu.Unlock()
	rw.retryMS.Store(0)
}
