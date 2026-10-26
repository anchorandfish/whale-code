package bridge

import (
	"context"
	stderrors "errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// ReconnectConfig 是重连策略配置（指数退避 + 抖动）。
type ReconnectConfig struct {
	BaseDelay    time.Duration // 初始延迟（默认 1s）
	MaxDelay     time.Duration // 最大延迟（默认 30s）
	Factor       float64       // 退避因子（默认 2.0）
	JitterFactor float64       // 抖动因子（默认 0.2，即 ±20%）
	MaxAttempts  int           // 最大重试次数（默认 5）
}

// DefaultReconnectConfig 是默认重连配置（对标同类 CLI 指数退避 + 抖动）。
var DefaultReconnectConfig = ReconnectConfig{
	BaseDelay:    1 * time.Second,
	MaxDelay:     30 * time.Second,
	Factor:       2.0,
	JitterFactor: 0.2,
	MaxAttempts:  5,
}

// ReconnectResult 是异步重连的结果（T1-P1-A1 新增）。
//
// 字段说明：
// - NewToken：成功时的新 token，失败时为空
// - Err：失败原因（带 ai.ErrBridgeReconnect ID）
// - Attempt：实际尝试次数（成功时为成功的那一次，失败时为 MaxAttempts）
// - TotalMs：总耗时（毫秒，用于 metrics / 日志）
type ReconnectResult struct {
	NewToken string
	Err      error
	Attempt  int
	TotalMs  int64
}

// OnReconnectCompleteCallback 是重连完成回调（T1-P1-A1 新增，吸纳审查优化 4）。
//
// 签名说明（优化 4）：
// - success=true 时 err 为 nil；success=false 时 err 为失败原因
// - 不传 token 原文：token 由 goroutine 内部存入 store，IDE 通过 GetStatus 查询
// - 回调内 panic 由 ReconnectManager 内部 defer recover() 防护（优化 4）
//
// 由 AuthProvider 注入 IDE 通知（TUI bridge.go 调 sendNotification("reconnectStatus", ...)）。
type OnReconnectCompleteCallback func(success bool, err error)

// ReconnectManager 管理 401/403 触发的重连流程（P1-4，；T1-P1-A1 异步化）。
//
// 线程安全（T1-P1-A1 吸纳审查优化 1）：
// - 同步版 ReconnectOnAuthError 仍非线程安全（向后兼容），外部 AuthProvider.mu 保护
// - 异步版 ReconnectOnAuthErrorAsync 通过内部 mu 自保护，可并发调用
// - TryStartAsyncReconnect 原子检查并设置 inflight，防 401 风暴触发多次重连（优化 2）
//
// ⚠️ v2 R6：本方法包含 time.Sleep（最坏累计 31s+），调用方不得持有 AuthProvider.mu。
// ⚠️ T1-P1-A1：异步版 ReconnectOnAuthErrorAsync 不阻塞调用方，goroutine 内部仍含退避延迟。
type ReconnectManager struct {
	config       ReconnectConfig
	attempt      int
	refreshToken func() (string, error) // 刷新 token 的回调

	// T1-P1-A1 新增字段（SNAPSHOT-1/2/3，）
	backoff    ai.BackoffStrategy          // 退避策略（nil 时用 config 构造默认 ExponentialBackoff，优化 6）
	mu         sync.Mutex                  // 保护 attempt + inflight + onComplete（异步版自保护，优化 1）
	inflight   bool                        // SNAPSHOT-2：是否已有异步重连在跑（去重，防 401 风暴，优化 2）
	onComplete OnReconnectCompleteCallback // SNAPSHOT-3：重连完成回调（不传 token，优化 4)
	cancelFn   context.CancelFunc          // SNAPSHOT-6：异步重连 ctx 取消函数（用于 Stop）
	stopped    atomic.Bool                 // SNAPSHOT-7：是否已 Stop（防止 Stop 后再启动）
}

// NewReconnectManager 创建重连管理器。
// refreshToken 可为 nil（后续通过 SetRefreshToken 注入），ReconnectOnAuthError 有 nil 防御（B2 修复）。
func NewReconnectManager(config ReconnectConfig, refreshToken func() (string, error)) *ReconnectManager {
	return &ReconnectManager{config: config, refreshToken: refreshToken}
}

// SetRefreshToken 注入刷新 token 的回调（B2 修复，方案 A）。
// 允许在 NewReconnectManager 创建后注入回调，避免 refreshToken 为 nil 时 panic。
// AuthProvider.SetReconnectManager 会自动调用此方法委托 scheduler 的刷新逻辑。
func (rm *ReconnectManager) SetRefreshToken(fn func() (string, error)) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.refreshToken = fn
}

// SetBackoffStrategy 注入退避策略（T1-P1-A1 新增，吸纳审查优化 6）。
// bs 为 nil 时忽略（保持现有 config 公式计算，向后兼容）。
// 由 AuthProvider.SetReconnectManager 后续注入统一 BackoffStrategy（C10 冲突解决）。
func (rm *ReconnectManager) SetBackoffStrategy(bs ai.BackoffStrategy) {
	if bs == nil {
		return
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.backoff = bs
}

// SetOnReconnectComplete 注入重连完成回调（T1-P1-A1 新增，吸纳审查优化 4）。
// cb 为 nil 时忽略（向后兼容，无回调时重连仍正常工作）。
// 由 AuthProvider.SetOnReconnectComplete 委托调用。
func (rm *ReconnectManager) SetOnReconnectComplete(cb OnReconnectCompleteCallback) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.onComplete = cb
}

// TryStartAsyncReconnect 原子检查并设置 inflight=true，并创建可取消的 ctx（T1-P1-A1 新增，吸纳审查优化 2）。
//
// 返回值：
// - ctx：用于传给 ReconnectOnAuthErrorAsync 的 context（含 cancel 函数，由 rm 内部管理）
// - ok=true：成功启动，调用方应启动 goroutine 跑 ReconnectOnAuthErrorAsync(ctx)
// - ok=false：已有异步重连在跑（inflight=true）或已 Stop，调用方应立即返回 ErrBridgeReconnecting（fail-fast）
//
// 线程安全：内部 mu 保护 inflight + cancelFn。
// 防御性：rm 已 Stop 时返回 false（防止 Stop 后再启动）。
// ctx 创建：用 context.WithCancel(context.Background())，cancel 函数存入 rm.cancelFn 供 Stop() 调用。
func (rm *ReconnectManager) TryStartAsyncReconnect() (ctx context.Context, ok bool) {
	if rm.stopped.Load() {
		return context.Background(), false
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.inflight {
		return context.Background(), false
	}
	ctx, cancel := context.WithCancel(context.Background())
	rm.inflight = true
	rm.cancelFn = cancel
	return ctx, true
}

// Stop 取消正在进行的异步重连（T1-P1-A1 新增）。
// 用于进程退出 / AuthProvider 释放时清理 goroutine。
// 重复调用安全（幂等）。
func (rm *ReconnectManager) Stop() {
	rm.stopped.Store(true)
	rm.mu.Lock()
	cancelFn := rm.cancelFn
	rm.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}
}

// ReconnectOnAuthErrorAsync 异步执行重连（T1-P1-A1 新增，候选 B 核心）。
//
// 行为：
// - 返回 buffered channel（cap=1），goroutine 完成时写入 ReconnectResult 后关闭
// - goroutine 内部循环 MaxAttempts 次：select ctx.Done / time.After(delay) / refreshToken()
// - 成功时 attempt 重置 + 返回 ReconnectResult{NewToken, Attempt, TotalMs}
// - 失败时 attempt++，超 MaxAttempts 则返回 ReconnectResult{Err: ErrBridgeReconnect}
// - ctx 取消时立即返回 ReconnectResult{Err: ctx.Err()}
// - goroutine 退出时：inflight=false + 调 onComplete（不传 token，优化 4）
//
// 顺序不变量：终局通知先发、结果后交付 —— 否则消费者补发的"验签失败"通知可能先到，
// IDE 以最后一条为准 ⇒ 停在假成功。
//
// 防御性（吸纳审查优化 4/5）：
// - defer recover() 防回调 panic 影响 goroutine
// - refreshToken 为 nil 时立即返回错误（B2 修复延续）
// - ctx 尊重：select <-ctx.Done() 优先（优化 5；refreshToken 内部 HTTP 超时 P2 改造）
//
// 线程安全：内部 mu 保护 attempt + inflight。
// 调用方无需持 AuthProvider.mu（与同步版 R6 一致）。
func (rm *ReconnectManager) ReconnectOnAuthErrorAsync(ctx context.Context) <-chan ReconnectResult {
	resultCh := make(chan ReconnectResult, 1)

	// B2 修复延续：refreshToken 未注入时立即返回错误
	rm.mu.Lock()
	refreshFn := rm.refreshToken
	rm.mu.Unlock()
	if refreshFn == nil {
		err := ai.WithID(fmt.Errorf("reconnect failed: refreshToken callback not configured"), ai.ErrBridgeReconnect)
		// 终局通知先于交付（不变量见下方注释）
		rm.finishAsyncReconnect(false, err)
		resultCh <- ReconnectResult{
			Err:     err,
			Attempt: 0,
			TotalMs: 0,
		}
		close(resultCh)
		return resultCh
	}

	startTime := time.Now()

	go func() {
		// 优化 4：defer recover() 防回调 panic 影响 goroutine
		defer func() {
			if r := recover(); r != nil {
				err := ai.WithID(fmt.Errorf("reconnect goroutine panic: %v", r), ai.ErrBridgeReconnect)
				// 终局通知先于交付（不变量见下方注释）
				rm.finishAsyncReconnect(false, err)
				resultCh <- ReconnectResult{
					Err:     err,
					Attempt: 0,
					TotalMs: time.Since(startTime).Milliseconds(),
				}
				close(resultCh)
			}
		}()

		var lastErr error
		var successToken string
		var successAttempt int
		var done bool

		for {
			// 检查 ctx 取消（优化 5）
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
				done = true
				break
			default:
			}
			if done {
				break
			}

			// 当前 attempt（从 1 开始）
			rm.mu.Lock()
			rm.attempt++
			currentAttempt := rm.attempt
			rm.mu.Unlock()

			// 超过 MaxAttempts 则失败
			if currentAttempt > rm.config.MaxAttempts {
				lastErr = fmt.Errorf("reconnect failed after %d attempts: %w", rm.config.MaxAttempts, lastErr)
				done = true
				break
			}

			// 计算退避延迟
			delay := rm.calculateDelayLocked(currentAttempt)

			// 等待延迟或 ctx 取消（优化 5）
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
				done = true
				break
			case <-time.After(delay):
			}
			if done {
				break
			}

			// 调用 refreshToken 回调
			newToken, err := refreshFn()
			if err == nil {
				// 成功
				rm.mu.Lock()
				rm.attempt = 0
				rm.mu.Unlock()
				successToken = newToken
				successAttempt = currentAttempt
				lastErr = nil
				done = true
				break
			}
			// 失败，记录错误，继续循环
			lastErr = err
		}

		// 构造结果
		result := ReconnectResult{
			Attempt: successAttempt,
			TotalMs: time.Since(startTime).Milliseconds(),
		}
		if lastErr == nil {
			result.NewToken = successToken
		} else {
			result.Err = ai.WithID(fmt.Errorf("reconnect failed: %w", lastErr), ai.ErrBridgeReconnect)
		}

		// ★ 不变量：终局通知必须**先于**结果交付。
		//
		// 消费者（AuthProvider.runAsyncReconnect）拿到结果后会去验签新 token；
		// 验签失败时它要补发一条 NotifyReconnectFailure(false, err) 覆盖这里的 success
		// （X3 病灶5：不能让 IDE 停在假成功）。若"交付"先于"通知"，这两条通知就分属
		// 两个 goroutine、中间没有任何同步点 —— 补发的失败可能先到，IDE 以最后一条为准
		// ⇒ 又变回"重连成功"。用程序顺序把终局通知钉死在前面，顺序就不再靠调度运气。
		//
		// 代价：onComplete 回调期间结果尚未交付，回调实现必须保持快速、不得阻塞等待。
		rm.finishAsyncReconnect(lastErr == nil, result.Err)
		resultCh <- result
		close(resultCh)
	}()

	return resultCh
}

// finishAsyncReconnect 清理 inflight 标志并调用 onComplete 回调（T1-P1-A1 内部方法）。
//
// 行为：
// - mu 保护下设置 inflight=false + cancelFn=nil（允许下一次重连启动）
// - 调用 onComplete 回调（不传 token，优化 4）
// - 回调 panic 由本方法 defer recover() 防护（优化 4）
//
// success=true 时 err 为 nil；success=false 时 err 为失败原因。
func (rm *ReconnectManager) finishAsyncReconnect(success bool, err error) {
	rm.mu.Lock()
	rm.inflight = false
	rm.cancelFn = nil
	cb := rm.onComplete
	rm.mu.Unlock()

	if cb == nil {
		return
	}
	// 优化 4：defer recover() 防回调 panic
	defer func() {
		_ = recover()
	}()
	cb(success, err)
}

// NotifyReconnectFailure 通知 IDE 重连后续阶段（如新 token 验签）失败（桶 X3 病灶5 修复）。
//
// 修复前问题：runAsyncReconnect 在新 token 验签失败时仅 return，
// 但 finishAsyncReconnect(true, nil) 已被 ReconnectOnAuthErrorAsync goroutine 调用，
// 导致 IDE 收到 reconnectStatus: success 的虚假通知（注释明写"P2 改造"）。
//
// 修复后：runAsyncReconnect 验签失败时调用本方法，再次触发 onComplete(false, err)
// 通知 IDE 真实失败状态。IDE 应以最后一次通知为准（success=false）。
//
// 与 finishAsyncReconnect 的关系：
// - finishAsyncReconnect 在 ReconnectOnAuthErrorAsync goroutine 退出时调用（清理 inflight）
// - 本方法不清理 inflight（已被 finishAsyncReconnect 清理），仅触发 onComplete 回调
// - 本方法幂等：多次调用安全（onComplete 仅记录最终状态）
//
// 参数：
// - err: 验签失败原因（传 nil 时默认为 ErrInvalidToken）
func (rm *ReconnectManager) NotifyReconnectFailure(err error) {
	if err == nil {
		err = fmt.Errorf("reconnect succeeded but new token verification failed")
	}
	rm.mu.Lock()
	cb := rm.onComplete
	rm.mu.Unlock()

	if cb == nil {
		return
	}
	// defer recover() 防回调 panic（与 finishAsyncReconnect 一致）
	defer func() {
		_ = recover()
	}()
	cb(false, err)
}

// ReconnectOnAuthError 在远程 401/403 错误时触发重连（v2 R5，同步版，向后兼容）。
//
// ⚠️ T1-P1-A1：本同步版仍保留供旧调用方使用（向后兼容，吸纳审查优化 1）。
// AuthProvider.SetToken 在 T1-P1-A1 后改用 ReconnectOnAuthErrorAsync（异步），不再调用本方法。
//
// ⚠️ v2 R5 修订：
// - 返回 (string, error)：成功时返回刷新得到的新 token（R5 b）
// - 触发源为远程 401/403（非本地 VerifyJWT 验签失败，R5 d）
// - 调用方（AuthProvider.SetToken）用新 token 调 tm.SetToken 验签+存储（R5 c）
//
// 指数退避 + 抖动，超过 MaxAttempts 后返回带 ErrBridgeReconnect 的错误（02 §2.3.2 契约）。
// ⚠️ v2 R6：本方法包含 time.Sleep（最坏累计 31s+），调用方不得持有 p.mu。
// ⚠️ B2 修复（方案 B 防御性 nil 检查）：refreshToken 未注入时直接返回错误，避免 panic。
// ⚠️ 非线程安全（向后兼容）：外部 AuthProvider.mu 保护；并发场景请用 ReconnectOnAuthErrorAsync。
func (rm *ReconnectManager) ReconnectOnAuthError() (string, error) {
	// B2 修复（方案 B）：未注入 refreshToken 回调时直接返回错误，避免 nil panic
	if rm.refreshToken == nil {
		return "", ai.WithID(fmt.Errorf("reconnect failed: refreshToken callback not configured"), ai.ErrBridgeReconnect)
	}
	var lastErr error
	for rm.attempt < rm.config.MaxAttempts {
		rm.attempt++
		delay := rm.calculateDelay()
		time.Sleep(delay)
		newToken, err := rm.refreshToken()
		if err == nil {
			rm.attempt = 0 // 重置计数器
			return newToken, nil
		}
		lastErr = err
	}
	rm.attempt = 0
	return "", ai.WithID(fmt.Errorf("reconnect failed after %d attempts: %w", rm.config.MaxAttempts, lastErr), ai.ErrBridgeReconnect)
}

// calculateDelay 计算本次退避延迟（指数退避 + 抖动，v2 P2-3）。
// 指数退避：base * factor^(attempt-1)，封顶 MaxDelay。
// 抖动：±JitterFactor，抖动后再次封顶 MaxDelay（P2-3）。
//
// ⚠️ 同步版专用（非线程安全，外部持锁调用）。
// 异步版用 calculateDelayLocked（内部持锁）。
func (rm *ReconnectManager) calculateDelay() time.Duration {
	return rm.calculateDelayLocked(rm.attempt)
}

// calculateDelayLocked 计算指定 attempt 的退避延迟（T1-P1-A1 新增，异步版用）。
//
// 行为（吸纳审查优化 6）：
// - rm.backoff != nil 时用 rm.backoff.NextDelay(attempt)（统一 BackoffStrategy，C10 冲突解决）
// - rm.backoff == nil 时用 config 公式计算（向后兼容）
//
// 调用方应持 rm.mu（异步版）或外部锁（同步版）。
func (rm *ReconnectManager) calculateDelayLocked(attempt int) time.Duration {
	if rm.backoff != nil {
		return rm.backoff.NextDelay(attempt)
	}
	// 向后兼容：用 config 公式计算
	delay := float64(rm.config.BaseDelay) * math.Pow(rm.config.Factor, float64(attempt-1))
	if delay > float64(rm.config.MaxDelay) {
		delay = float64(rm.config.MaxDelay)
	}
	jitter := delay * rm.config.JitterFactor * (2*rand.Float64() - 1)
	result := delay + jitter
	// P2-3：抖动后再次封顶 MaxDelay
	if result > float64(rm.config.MaxDelay) {
		result = float64(rm.config.MaxDelay)
	}
	return time.Duration(result)
}

// isAuthError 判断错误是否为远程 401/403 类认证错误（v2 R5 a/d）。
// 通过 errors.As 提取 *BridgeError，检查错误码是否属"认证失败可重连"类：
// - ErrInvalidToken(-32001)/ErrTokenExpired(-32002)/ErrAuthRequired(-32005)：远程拒绝，可重连
// - ErrRefreshFailed(-32003)/ErrRefreshUnavailable(-32004)：刷新失败，可重连
//
// 本地 VerifyJWT 失败（返回 ai.WithID 包装的 ErrBridgeJWT，非 *BridgeError）不触发 reconnect。
//
// T1-P1-A1：新增 ErrBridgeReconnecting(-32006) 不视为可重连错误（重连中不递归触发新重连）。
func isAuthError(err error) bool {
	var bridgeErr *BridgeError
	if stderrors.As(err, &bridgeErr) {
		switch bridgeErr.Code {
		case ErrInvalidToken, ErrTokenExpired, ErrAuthRequired, ErrRefreshFailed, ErrRefreshUnavailable:
			return true
		}
	}
	return false
}
