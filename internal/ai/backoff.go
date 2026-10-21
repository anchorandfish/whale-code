// Package ai — T1-P1-A1（C10 冲突解决）：统一退避策略接口 BackoffStrategy
//
// 设计要点（吸纳审查优化 6）：
//   - 接口签名用显式 attempt 参数（而非内部状态），避免并发竞态
//   - ExponentialBackoff 实现：base × factor^(attempt-1)，封顶 MaxDelay，加 ±JitterFactor 抖动
//   - rng *rand.Rand 为 nil 时用 math/rand 全局源（Go 1.20+ 自动 seed）
//   - mu sync.Mutex 保护 rng（rand.Rand 非线程安全）
//   - Reset() 为 noop（ExponentialBackoff 无内部 attempt 状态），但满足接口契约
//
// 业内对标：
//   - golang.org/x/time/backoff ExponentialBackOff
//     https://pkg.go.dev/golang.org/x/time/backoff
//   - gRPC BackoffStrategy（gRPC 内部退避策略接口）
//     https://grpc.io/docs/guides/backoff/
//   - AWS SDK v2 retryer（IsErrorRetryable + RetryDelay 指数退避 + jitter）
//     https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws/retry
//
// 归属仲裁（T0-2 §2 C10）：本接口归属 M21 主导（A1 已落地 BackpressurePipe），
// M17-01/M21-03/M24-03/remote/heartbeat 等模块在 P2 实施时复用本接口，禁止再独立实现退避逻辑。
package ai

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// BackoffStrategy 是统一退避策略接口（C10 冲突解决，M21 主导，其他模块复用）。
//
// 设计原则：
//   - NextDelay(attempt) 用显式 attempt 参数（从 1 开始），避免内部状态在并发下出 bug
//   - Reset() 重置内部状态（成功后调用）；无内部状态的实现可为 noop
//
// 业内对标：golang.org/x/time/backoff.BackOff 接口（NextBackOff() / Reset()），
// 本接口用 attempt int 显式参数替代 BackOff 的内部状态，更易并发安全。
//
// 复用承诺（C10）：M17-01/M21-03/M24-03/remote/heartbeat 在 P2 实施时必须复用本接口。
type BackoffStrategy interface {
	// NextDelay 计算第 attempt 次重试的延迟（attempt 从 1 开始）。
	// 实现方应保证：attempt <= 0 时返回 BaseDelay（防御性）；attempt 超大时返回 MaxDelay（封顶）。
	NextDelay(attempt int) time.Duration
	// Reset 重置内部状态（成功后调用）。无内部状态的实现可为 noop。
	Reset()
}

// ExponentialBackoff 是指数退避 + 抖动实现，对标 golang.org/x/time/backoff.ExponentialBackOff。
//
// 字段说明：
//   - BaseDelay：初始延迟（attempt=1 时返回 BaseDelay）
//   - MaxDelay：最大延迟（封顶值）
//   - Factor：退避因子（base × factor^(attempt-1)）
//   - JitterFactor：抖动因子（±JitterFactor，0 表示无抖动）
//   - rng：随机数源（nil 时用 math/rand 全局源，Go 1.20+ 自动 seed）
//   - mu：保护 rng（rand.Rand 非线程安全）
//
// 线程安全：NextDelay 可并发调用（内部 mu 保护 rng）。
type ExponentialBackoff struct {
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	Factor       float64
	JitterFactor float64
	rng          *rand.Rand
	mu           sync.Mutex
}

// NewExponentialBackoff 创建指数退避实例。
//   - base：初始延迟（<=0 时默认 1s）
//   - max：最大延迟（<=0 时默认 30s）
//   - factor：退避因子（<=0 时默认 2.0）
//   - jitter：抖动因子（<0 时默认 0；>1 时截断为 1）
//   - rng：随机数源（nil 时用 math/rand 全局源）
func NewExponentialBackoff(base, max time.Duration, factor, jitter float64, rng *rand.Rand) *ExponentialBackoff {
	if base <= 0 {
		base = 1 * time.Second
	}
	if max <= 0 {
		max = 30 * time.Second
	}
	if factor <= 0 {
		factor = 2.0
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	return &ExponentialBackoff{
		BaseDelay:    base,
		MaxDelay:     max,
		Factor:       factor,
		JitterFactor: jitter,
		rng:          rng,
	}
}

// NextDelay 计算第 attempt 次重试的延迟（指数退避 + 抖动）。
//
// 计算公式：
//   - 基础延迟 = BaseDelay × Factor^(attempt-1)
//   - 封顶 MaxDelay
//   - 抖动 = 基础延迟 × JitterFactor × (2×rand - 1)（即 ±JitterFactor）
//   - 抖动后再次封顶 MaxDelay
//
// 防御性：attempt <= 0 时按 attempt=1 处理（返回 BaseDelay ± 抖动）。
func (b *ExponentialBackoff) NextDelay(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	// 指数退避：base × factor^(attempt-1)
	delay := float64(b.BaseDelay) * math.Pow(b.Factor, float64(attempt-1))
	// 封顶 MaxDelay
	if delay > float64(b.MaxDelay) {
		delay = float64(b.MaxDelay)
	}
	// 抖动：±JitterFactor
	if b.JitterFactor > 0 {
		jitter := delay * b.JitterFactor * (2*b.randFloat64() - 1)
		result := delay + jitter
		// 抖动后再次封顶 MaxDelay
		if result > float64(b.MaxDelay) {
			result = float64(b.MaxDelay)
		}
		// 抖动后不应为负（极端情况下 jitter 让 result 为负）
		if result < 0 {
			result = 0
		}
		return time.Duration(result)
	}
	return time.Duration(delay)
}

// randFloat64 返回 [0.0, 1.0) 随机数，线程安全（mu 保护 rng）。
// rng 为 nil 时用 math/rand 全局源（Go 1.20+ 自动 seed，无需手动 Seed）。
func (b *ExponentialBackoff) randFloat64() float64 {
	if b.rng == nil {
		// math/rand 全局函数内部自带头部锁，并发安全
		return rand.Float64()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rng.Float64()
}

// Reset 重置内部状态。
// ExponentialBackoff 无内部 attempt 状态（NextDelay 用显式 attempt 参数），
// 因此 Reset 为 noop，但满足 BackoffStrategy 接口契约。
func (b *ExponentialBackoff) Reset() {
	// noop：无内部状态需重置
}
