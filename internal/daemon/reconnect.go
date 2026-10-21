// Package daemon — M24-03 心跳重连退避 jitter + 自适应（SNAPSHOT）
//
// 本文件实现指数退避 + 抖动 + 自适应的重连管理器，替代固定退避策略。
// 对标同类 CLI ReconnectManager（官方仓库）与 gRPC backoff。
//
// 设计要点（候选 A 选定）：
// - 接口签名：ReconnectManager 提供 NextBackoff / RecordSuccess / RecordFailure
// - 结构体字段：BackoffConfig 含 BaseInterval/MaxInterval/JitterFactor/Multiplier
// - 分层权责：本文件只做"退避计算"，实际重连由调用方驱动
// - 降级策略：jitter 计算失败时回退到无 jitter 的纯指数退避
// - TTL规则：成功后重置 attempt，失败后 attempt++（自适应）
//
// SNAPSHOT-1：BackoffConfig 配置结构
// SNAPSHOT-2：ReconnectManager 核心实现（指数 + jitter + 自适应）
// SNAPSHOT-3：NextBackoff 返回下次退避时间
// SNAPSHOT-4：RecordSuccess/RecordFailure 调整 attempt
package daemon

import (
	"math/rand"
	"sync"
	"time"
)

// DefaultBaseInterval 是默认基础退避间隔（1s）。
const DefaultBaseInterval = 1 * time.Second

// DefaultMaxInterval 是默认最大退避间隔（60s，防雪崩）。
const DefaultMaxInterval = 60 * time.Second

// DefaultJitterFactor 是默认抖动因子（0.2 = ±20%）。
const DefaultJitterFactor = 0.2

// DefaultMultiplier 是默认退避倍数（2.0 = 指数翻倍）。
const DefaultMultiplier = 2.0

// DefaultMaxAttempts 是默认最大重试次数（10 次，超过则放弃）。
const DefaultMaxAttempts = 10

// BackoffConfig 是退避策略配置（SNAPSHOT-1）。
type BackoffConfig struct {
	BaseInterval time.Duration // 基础退避间隔（首次失败后等待时间）
	MaxInterval  time.Duration // 最大退避间隔（封顶，防雪崩）
	JitterFactor float64       // 抖动因子（0.0~1.0，0.2=±20%）
	Multiplier   float64       // 退避倍数（2.0=指数翻倍）
	MaxAttempts  int           // 最大重试次数（0=无限）
}

// DefaultBackoffConfig 返回默认退避配置（SNAPSHOT-2）。
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		BaseInterval: DefaultBaseInterval,
		MaxInterval:  DefaultMaxInterval,
		JitterFactor: DefaultJitterFactor,
		Multiplier:   DefaultMultiplier,
		MaxAttempts:  DefaultMaxAttempts,
	}
}

// ReconnectManager 是心跳重连退避管理器（SNAPSHOT-3）。
//
// 行为：
// - 指数退避：base * multiplier^attempt，封顶 maxInterval
// - 抖动：在退避值上叠加 ±jitterFactor 的随机扰动
// - 自适应：成功后重置 attempt=0，失败后 attempt++
// - 超过 MaxAttempts 时 ShouldGiveUp 返回 true
//
// 线程安全：所有方法加锁保护。
type ReconnectManager struct {
	mu      sync.Mutex
	cfg     BackoffConfig
	attempt int
	rng     *rand.Rand
}

// NewReconnectManager 创建重连管理器（SNAPSHOT-4）。
//
// 参数：
// - cfg: 退避配置（空字段用默认值填充）
func NewReconnectManager(cfg BackoffConfig) *ReconnectManager {
	if cfg.BaseInterval <= 0 {
		cfg.BaseInterval = DefaultBaseInterval
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = DefaultMaxInterval
	}
	if cfg.JitterFactor < 0 || cfg.JitterFactor > 1 {
		cfg.JitterFactor = DefaultJitterFactor
	}
	if cfg.Multiplier <= 1 {
		cfg.Multiplier = DefaultMultiplier
	}
	// MaxAttempts=0 表示无限重试（见 BackoffConfig 注释 + ShouldGiveUp 处理），
	// 不填充默认值，保留 0=无限语义。显式 10 次需调用 DefaultBackoffConfig()。

	return &ReconnectManager{
		cfg: cfg,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// NextBackoff 返回下次退避时间（SNAPSHOT-5）。
//
// 计算公式：
//
//	raw = base * multiplier^attempt
//	capped = min(raw, maxInterval)
//	jitter = capped * jitterFactor * (2*rng - 1) // [-jitterFactor, +jitterFactor]
//	result = capped + jitter
//
// 注意：调用 NextBackoff 不会增加 attempt，调用方需显式 RecordFailure。
func (r *ReconnectManager) NextBackoff() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 指数退避
	raw := float64(r.cfg.BaseInterval)
	for i := 0; i < r.attempt && raw < float64(r.cfg.MaxInterval); i++ {
		raw *= r.cfg.Multiplier
	}
	if raw > float64(r.cfg.MaxInterval) {
		raw = float64(r.cfg.MaxInterval)
	}

	// 抖动：[-jitterFactor, +jitterFactor] * capped
	jitterRange := raw * r.cfg.JitterFactor
	jitter := (2*r.rng.Float64() - 1) * jitterRange

	result := raw + jitter
	if result < 0 {
		result = raw // 降级：负数时回退到无 jitter
	}
	return time.Duration(result)
}

// RecordSuccess 记录成功，重置 attempt（SNAPSHOT-6）。
//
// 自适应：成功后重置 attempt=0，使下次退避从基础间隔开始。
func (r *ReconnectManager) RecordSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt = 0
}

// RecordFailure 记录失败，attempt++（SNAPSHOT-7）。
//
// 自适应：失败后 attempt++，使下次退避时间增长。
func (r *ReconnectManager) RecordFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt++
}

// Attempt 返回当前失败次数（SNAPSHOT-8）。
func (r *ReconnectManager) Attempt() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempt
}

// ShouldGiveUp 判断是否应放弃重连（SNAPSHOT-9）。
//
// 当 MaxAttempts > 0 且 attempt >= MaxAttempts 时返回 true。
func (r *ReconnectManager) ShouldGiveUp() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg.MaxAttempts <= 0 {
		return false // 0=无限重试
	}
	return r.attempt >= r.cfg.MaxAttempts
}

// Reset 重置 attempt 计数（SNAPSHOT-10）。
func (r *ReconnectManager) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt = 0
}
