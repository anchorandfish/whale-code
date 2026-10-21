// Package lsp — 服务器崩溃自动恢复测试（T1-P1-A2 /）
package lsp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestCalculateBackoff_FirstRestart 验证首次重启（count=0）立即执行无退避
func TestCalculateBackoff_FirstRestart(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: 5, BackoffDelayMs: 1000},
	}
	c := &Client{cfg: cfg, restartCount: 0}

	if d := c.calculateBackoff(); d != 0 {
		t.Errorf("count=0 backoff = %v, want 0 (first restart immediate)", d)
	}
}

// TestCalculateBackoff_ExponentialSequence 验证指数退避序列
// 期望：1s, 2s, 4s, 8s, 16s
func TestCalculateBackoff_ExponentialSequence(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: 10, BackoffDelayMs: 1000},
	}
	c := &Client{cfg: cfg}

	expected := []time.Duration{
		0,                        // count=0
		1000 * time.Millisecond,  // count=1: 1s
		2000 * time.Millisecond,  // count=2: 2s
		4000 * time.Millisecond,  // count=3: 4s
		8000 * time.Millisecond,  // count=4: 8s
		16000 * time.Millisecond, // count=5: 16s
	}
	for i, want := range expected {
		c.restartCount = i
		got := c.calculateBackoff()
		if got != want {
			t.Errorf("count=%d backoff = %v, want %v", i, got, want)
		}
	}
}

// TestCalculateBackoff_CapAt30s 验证退避上限 30s（吸纳优化 4）
func TestCalculateBackoff_CapAt30s(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: 100, BackoffDelayMs: 1000},
	}
	c := &Client{cfg: cfg, restartCount: 10} // 2^9 * 1000 = 512s，应封顶 30s

	got := c.calculateBackoff()
	want := time.Duration(MaxBackoffMs) * time.Millisecond
	if got != want {
		t.Errorf("count=10 backoff = %v, want %v (capped)", got, want)
	}
}

// TestCalculateBackoff_CustomBase 验证自定义 BackoffDelayMs 基础值
func TestCalculateBackoff_CustomBase(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: 5, BackoffDelayMs: 100}, // 100ms 基础
	}
	c := &Client{cfg: cfg, restartCount: 2} // 100 * 2^1 = 200ms

	got := c.calculateBackoff()
	want := 200 * time.Millisecond
	if got != want {
		t.Errorf("count=2 backoff = %v, want %v (custom base)", got, want)
	}
}

// TestCalculateBackoff_NilCfg 验证 cfg 为 nil 时使用默认基础值 1000ms
func TestCalculateBackoff_NilCfg(t *testing.T) {
	c := &Client{cfg: nil, restartCount: 1} // 默认 1000ms

	got := c.calculateBackoff()
	want := 1000 * time.Millisecond
	if got != want {
		t.Errorf("nil cfg backoff = %v, want %v (default base)", got, want)
	}
}

// TestRestart_MaxRestartsExceeded 验证超 maxRestarts 返回错误并永久标记 crashed
func TestRestart_MaxRestartsExceeded(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: 2, BackoffDelayMs: 1}, // 加速测试
	}
	c := &Client{
		cfg:          cfg,
		stopChan:     make(chan struct{}),
		restartCount: 2, // 已达上限
	}

	ctx := context.Background()
	err := c.Restart(ctx)
	if err == nil {
		t.Error("expected error for max restarts exceeded")
	}
	// 应永久标记 crashed
	if !c.crashed.Load() {
		t.Error("expected crashed=true after max restarts exceeded")
	}
}

// TestRestart_UnlimitedMaxRestarts 验证 maxRestarts=-1 不限制重启
func TestRestart_UnlimitedMaxRestarts(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: -1, BackoffDelayMs: 1},
	}
	c := &Client{
		cfg:      cfg,
		stopChan: make(chan struct{}),
		// restartCount=0 使 calculateBackoff 返回 0（跳过 backoff 等待），
		// 避免 restartCount=1000 触发 30s backoff 导致测试超时。
		// maxRestarts=-1 的不限制语义由 Restart() 内部检查验证：
		// restart.go:63 MaxRestarts != -1 为 false → 不触发 max restarts error。
		restartCount: 0,
	}
	// 注意：此测试只验证 maxRestarts 检查通过，不实际执行 restart（doRestart 会失败）
	// 实际 restart 会失败在 findServerFromConfig（c.language 为空）
	ctx := context.Background()
	err := c.Restart(ctx)
	// 应该进入 doRestart 而非 maxRestarts 检查
	if err == nil {
		t.Error("expected error from doRestart (no language configured)")
	}
	// 错误消息不应包含 "max restarts"
	if err != nil && contains(err.Error(), "max restarts") {
		t.Errorf("unlimited maxRestarts should not trigger max restarts error, got: %v", err)
	}
}

// TestRestart_StopChanClosed 验证 stopChan 已关闭时跳过 restart（吸纳优化 1）
func TestRestart_StopChanClosed(t *testing.T) {
	cfg := DefaultLSPConfig()
	c := &Client{
		cfg:      cfg,
		stopChan: make(chan struct{}),
	}
	close(c.stopChan) // 模拟 Close 已执行

	ctx := context.Background()
	err := c.Restart(ctx)
	if err == nil {
		t.Error("expected error when stopChan closed")
	}
	if err != nil && !contains(err.Error(), "closing") {
		t.Errorf("error should mention 'closing', got: %v", err)
	}
}

// TestRestart_NilStopChan 验证未初始化 client（stopChan=nil）返回错误
func TestRestart_NilStopChan(t *testing.T) {
	cfg := DefaultLSPConfig()
	c := &Client{
		cfg:      cfg,
		stopChan: nil, // 未初始化
	}

	ctx := context.Background()
	err := c.Restart(ctx)
	if err == nil {
		t.Error("expected error for nil stopChan")
	}
}

// TestRestart_ContextCancelledDuringBackoff 验证退避期间 ctx 取消返回 ctx.Err()
func TestRestart_ContextCancelledDuringBackoff(t *testing.T) {
	cfg := &LSPConfig{
		Recovery: RecoveryConfig{MaxRestarts: 5, BackoffDelayMs: 10000}, // 10s 退避
	}
	c := &Client{
		cfg:          cfg,
		stopChan:     make(chan struct{}),
		restartCount: 1, // 触发 10s 退避
	}

	ctx, cancel := context.WithCancel(context.Background())
	// 100ms 后取消 ctx
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := c.Restart(ctx)
	if err == nil {
		t.Error("expected error from cancelled ctx")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestHealthCheck_NotStarted 验证未启动时返回 false
func TestHealthCheck_NotStarted(t *testing.T) {
	c := &Client{} // cmd 为 nil
	if c.HealthCheck() {
		t.Error("expected false for unstarted client")
	}
}

// TestIsCrashed_DefaultFalse 验证新 client 默认未崩溃
func TestIsCrashed_DefaultFalse(t *testing.T) {
	c := &Client{}
	if c.IsCrashed() {
		t.Error("expected false for new client")
	}
}

// TestIsCrashed_SetTrue 验证设置 crashed=true 后 IsCrashed 返回 true
func TestIsCrashed_SetTrue(t *testing.T) {
	c := &Client{}
	c.crashed.Store(true)
	if !c.IsCrashed() {
		t.Error("expected true after crashed.Store(true)")
	}
}

// TestAtomicBoolCompileTimeCheck 编译期验证 atomic.Bool 类型存在
// 仅用于防止 Go 版本兼容问题（Go 1.19+ 支持 atomic.Bool）
func TestAtomicBoolCompileTimeCheck(t *testing.T) {
	var b atomic.Bool
	b.Store(true)
	if !b.Load() {
		t.Error("atomic.Bool should work")
	}
}

// contains 简单字符串包含检查（避免引入 strings 包）
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
