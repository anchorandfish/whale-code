// Package daemon — M24-03 ReconnectManager 测试（SNAPSHOT）
//
// 测试覆盖：
// - NextBackoff 指数增长
// - Jitter 在范围内
// - RecordSuccess 重置 attempt
// - RecordFailure 增长 attempt
// - ShouldGiveUp 阈值判断
// - MaxInterval 封顶
// - 默认配置填充
package daemon

import (
	"testing"
	"time"
)

// TestReconnectManager_InitialBackoff 首次退避应为基础间隔（含 jitter）
func TestReconnectManager_InitialBackoff(t *testing.T) {
	cfg := BackoffConfig{
		BaseInterval: 100 * time.Millisecond,
		MaxInterval:  60 * time.Second,
		JitterFactor: 0.0, // 关闭 jitter 便于精确断言
		Multiplier:   2.0,
		MaxAttempts:  10,
	}
	mgr := NewReconnectManager(cfg)

	backoff := mgr.NextBackoff()
	if backoff != 100*time.Millisecond {
		t.Errorf("initial backoff = %v, want 100ms", backoff)
	}
}

// TestReconnectManager_ExponentialGrowth 指数增长
func TestReconnectManager_ExponentialGrowth(t *testing.T) {
	cfg := BackoffConfig{
		BaseInterval: 100 * time.Millisecond,
		MaxInterval:  60 * time.Second,
		JitterFactor: 0.0, // 关闭 jitter
		Multiplier:   2.0,
		MaxAttempts:  10,
	}
	mgr := NewReconnectManager(cfg)

	// attempt=0: 100ms
	b0 := mgr.NextBackoff()
	if b0 != 100*time.Millisecond {
		t.Errorf("attempt 0: %v, want 100ms", b0)
	}

	mgr.RecordFailure()
	// attempt=1: 200ms
	b1 := mgr.NextBackoff()
	if b1 != 200*time.Millisecond {
		t.Errorf("attempt 1: %v, want 200ms", b1)
	}

	mgr.RecordFailure()
	// attempt=2: 400ms
	b2 := mgr.NextBackoff()
	if b2 != 400*time.Millisecond {
		t.Errorf("attempt 2: %v, want 400ms", b2)
	}

	mgr.RecordFailure()
	// attempt=3: 800ms
	b3 := mgr.NextBackoff()
	if b3 != 800*time.Millisecond {
		t.Errorf("attempt 3: %v, want 800ms", b3)
	}
}

// TestReconnectManager_MaxIntervalCap 退避值封顶
func TestReconnectManager_MaxIntervalCap(t *testing.T) {
	cfg := BackoffConfig{
		BaseInterval: 100 * time.Millisecond,
		MaxInterval:  500 * time.Millisecond, // 故意小，便于触发封顶
		JitterFactor: 0.0,
		Multiplier:   2.0,
		MaxAttempts:  10,
	}
	mgr := NewReconnectManager(cfg)

	// 连续失败直到封顶
	for i := 0; i < 10; i++ {
		mgr.RecordFailure()
	}
	backoff := mgr.NextBackoff()
	if backoff > 500*time.Millisecond {
		t.Errorf("backoff = %v, want <= 500ms (capped)", backoff)
	}
}

// TestReconnectManager_JitterInRange jitter 应在 ±jitterFactor 范围内
func TestReconnectManager_JitterInRange(t *testing.T) {
	cfg := BackoffConfig{
		BaseInterval: 100 * time.Millisecond,
		MaxInterval:  60 * time.Second,
		JitterFactor: 0.2, // ±20%
		Multiplier:   2.0,
		MaxAttempts:  10,
	}
	mgr := NewReconnectManager(cfg)

	// 多次采样验证 jitter 范围
	for i := 0; i < 100; i++ {
		backoff := mgr.NextBackoff()
		// base=100ms, jitter=±20ms, range=[80ms, 120ms]
		if backoff < 80*time.Millisecond || backoff > 120*time.Millisecond {
			t.Errorf("backoff %v out of jitter range [80ms, 120ms]", backoff)
		}
	}
}

// TestReconnectManager_RecordSuccessReset 成功后重置 attempt
func TestReconnectManager_RecordSuccessReset(t *testing.T) {
	mgr := NewReconnectManager(DefaultBackoffConfig())

	for i := 0; i < 5; i++ {
		mgr.RecordFailure()
	}
	if mgr.Attempt() != 5 {
		t.Fatalf("Attempt = %d, want 5", mgr.Attempt())
	}

	mgr.RecordSuccess()
	if mgr.Attempt() != 0 {
		t.Errorf("after RecordSuccess Attempt = %d, want 0", mgr.Attempt())
	}
}

// TestReconnectManager_ShouldGiveUp 达到 MaxAttempts 时放弃
func TestReconnectManager_ShouldGiveUp(t *testing.T) {
	cfg := BackoffConfig{
		BaseInterval: 100 * time.Millisecond,
		MaxInterval:  60 * time.Second,
		JitterFactor: 0.0,
		Multiplier:   2.0,
		MaxAttempts:  3,
	}
	mgr := NewReconnectManager(cfg)

	if mgr.ShouldGiveUp() {
		t.Error("ShouldGiveUp = true, want false (attempt=0)")
	}

	mgr.RecordFailure()
	mgr.RecordFailure()
	if mgr.ShouldGiveUp() {
		t.Error("ShouldGiveUp = true, want false (attempt=2)")
	}

	mgr.RecordFailure()
	if !mgr.ShouldGiveUp() {
		t.Error("ShouldGiveUp = false, want true (attempt=3 >= MaxAttempts)")
	}
}

// TestReconnectManager_Reset 显式重置
func TestReconnectManager_Reset(t *testing.T) {
	mgr := NewReconnectManager(DefaultBackoffConfig())

	for i := 0; i < 5; i++ {
		mgr.RecordFailure()
	}
	mgr.Reset()
	if mgr.Attempt() != 0 {
		t.Errorf("after Reset Attempt = %d, want 0", mgr.Attempt())
	}
}

// TestReconnectManager_DefaultConfigFill 默认配置填充
func TestReconnectManager_DefaultConfigFill(t *testing.T) {
	// 空配置应填充默认值
	mgr := NewReconnectManager(BackoffConfig{})

	if mgr.cfg.BaseInterval != DefaultBaseInterval {
		t.Errorf("BaseInterval = %v, want %v", mgr.cfg.BaseInterval, DefaultBaseInterval)
	}
	if mgr.cfg.MaxInterval != DefaultMaxInterval {
		t.Errorf("MaxInterval = %v, want %v", mgr.cfg.MaxInterval, DefaultMaxInterval)
	}
	// JitterFactor=0 是有效值（关闭抖动），空配置保留 0=关闭抖动。
	// 显式 0.2 需 DefaultBackoffConfig()。负数或 >1 才填充为默认值。
	if mgr.cfg.JitterFactor != 0 {
		t.Errorf("JitterFactor = %v, want 0 (disabled, empty config default)", mgr.cfg.JitterFactor)
	}
	if mgr.cfg.Multiplier != DefaultMultiplier {
		t.Errorf("Multiplier = %v, want %v", mgr.cfg.Multiplier, DefaultMultiplier)
	}
	// MaxAttempts=0 表示无限重试（空配置默认无限；显式 10 次需 DefaultBackoffConfig()）
	if mgr.cfg.MaxAttempts != 0 {
		t.Errorf("MaxAttempts = %v, want 0 (infinite, empty config default)", mgr.cfg.MaxAttempts)
	}
}

// TestReconnectManager_ZeroMaxAttemptsInfinite MaxAttempts=0 表示无限重试
func TestReconnectManager_ZeroMaxAttemptsInfinite(t *testing.T) {
	cfg := DefaultBackoffConfig()
	cfg.MaxAttempts = 0
	mgr := NewReconnectManager(cfg)

	for i := 0; i < 100; i++ {
		mgr.RecordFailure()
	}
	if mgr.ShouldGiveUp() {
		t.Error("ShouldGiveUp = true, want false (MaxAttempts=0 = infinite)")
	}
}

// TestReconnectManager_AttemptNotChangedByNextBackoff NextBackoff 不改变 attempt
func TestReconnectManager_AttemptNotChangedByNextBackoff(t *testing.T) {
	mgr := NewReconnectManager(DefaultBackoffConfig())

	_ = mgr.NextBackoff()
	_ = mgr.NextBackoff()
	_ = mgr.NextBackoff()

	if mgr.Attempt() != 0 {
		t.Errorf("Attempt = %d, want 0 (NextBackoff should not change attempt)", mgr.Attempt())
	}
}
