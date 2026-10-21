// Package ai — T1-P1-A1 BackoffStrategy 单元测试（含 -race 并发安全验证）
package ai

import (
	"math/rand"
	"sync"
	"testing"
	"time"
)

// TestExponentialBackoff_Defaults 验证 NewExponentialBackoff 零值参数时使用默认值。
func TestExponentialBackoff_Defaults(t *testing.T) {
	b := NewExponentialBackoff(0, 0, 0, 0, nil)
	if b.BaseDelay != 1*time.Second {
		t.Errorf("BaseDelay = %v, want 1s", b.BaseDelay)
	}
	if b.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", b.MaxDelay)
	}
	if b.Factor != 2.0 {
		t.Errorf("Factor = %v, want 2.0", b.Factor)
	}
	if b.JitterFactor != 0 {
		t.Errorf("JitterFactor = %v, want 0", b.JitterFactor)
	}
}

// TestExponentialBackoff_JitterTruncation 验证 JitterFactor >1 时截断为 1。
func TestExponentialBackoff_JitterTruncation(t *testing.T) {
	b := NewExponentialBackoff(1*time.Second, 10*time.Second, 2.0, 5.0, nil)
	if b.JitterFactor != 1 {
		t.Errorf("JitterFactor = %v, want 1 (truncated from 5.0)", b.JitterFactor)
	}
}

// TestExponentialBackoff_NextDelay_ExponentialGrowth 验证无抖动时延迟严格指数增长。
// 公式：base × factor^(attempt-1)
func TestExponentialBackoff_NextDelay_ExponentialGrowth(t *testing.T) {
	b := NewExponentialBackoff(100*time.Millisecond, 10*time.Second, 2.0, 0, nil)
	d1 := b.NextDelay(1)
	d2 := b.NextDelay(2)
	d3 := b.NextDelay(3)
	if d1 != 100*time.Millisecond {
		t.Errorf("d1 = %v, want 100ms", d1)
	}
	if d2 != 200*time.Millisecond {
		t.Errorf("d2 = %v, want 200ms", d2)
	}
	if d3 != 400*time.Millisecond {
		t.Errorf("d3 = %v, want 400ms", d3)
	}
}

// TestExponentialBackoff_NextDelay_RespectsMaxDelay 验证延迟封顶 MaxDelay。
func TestExponentialBackoff_NextDelay_RespectsMaxDelay(t *testing.T) {
	b := NewExponentialBackoff(1*time.Second, 5*time.Second, 2.0, 0, nil)
	for attempt := 1; attempt <= 20; attempt++ {
		delay := b.NextDelay(attempt)
		if delay > 5*time.Second {
			t.Errorf("attempt %d: delay %v exceeds MaxDelay 5s", attempt, delay)
		}
	}
}

// TestExponentialBackoff_NextDelay_JitterWithinRange 验证抖动后的延迟在 ±JitterFactor 范围内。
func TestExponentialBackoff_NextDelay_JitterWithinRange(t *testing.T) {
	b := NewExponentialBackoff(1*time.Second, 30*time.Second, 2.0, 0.2, nil)
	base := float64(1 * time.Second)
	minDelay := time.Duration(base * 0.8)
	maxDelay := time.Duration(base * 1.2)
	for i := 0; i < 100; i++ {
		delay := b.NextDelay(1)
		if delay < minDelay || delay > maxDelay {
			t.Errorf("delay %v outside expected range [%v, %v]", delay, minDelay, maxDelay)
		}
	}
}

// TestExponentialBackoff_NextDelay_NonPositiveAttempt 验证 attempt<=0 时按 attempt=1 处理（防御性）。
func TestExponentialBackoff_NextDelay_NonPositiveAttempt(t *testing.T) {
	b := NewExponentialBackoff(100*time.Millisecond, 10*time.Second, 2.0, 0, nil)
	d0 := b.NextDelay(0)
	dNeg := b.NextDelay(-5)
	d1 := b.NextDelay(1)
	if d0 != d1 {
		t.Errorf("NextDelay(0) = %v, want %v (same as attempt=1)", d0, d1)
	}
	if dNeg != d1 {
		t.Errorf("NextDelay(-5) = %v, want %v (same as attempt=1)", dNeg, d1)
	}
}

// TestExponentialBackoff_NextDelay_ConcurrentSafe 验证并发调用 NextDelay 不触发 race（-race 友好）。
// 同时验证带 rng 与不带 rng 两种路径。
func TestExponentialBackoff_NextDelay_ConcurrentSafe(t *testing.T) {
	// 路径 1：带独立 rng（mu 保护路径）
	rng := rand.New(rand.NewSource(42))
	b1 := NewExponentialBackoff(100*time.Millisecond, 10*time.Second, 2.0, 0.2, rng)
	// 路径 2：rng=nil（math/rand 全局源路径）
	b2 := NewExponentialBackoff(100*time.Millisecond, 10*time.Second, 2.0, 0.2, nil)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = b1.NextDelay(1)
		}()
		go func() {
			defer wg.Done()
			_ = b2.NextDelay(1)
		}()
	}
	wg.Wait()
}

// TestExponentialBackoff_Reset_NoOp 验证 Reset 不改变 NextDelay 行为（无内部状态）。
func TestExponentialBackoff_Reset_NoOp(t *testing.T) {
	b := NewExponentialBackoff(100*time.Millisecond, 10*time.Second, 2.0, 0, nil)
	d1 := b.NextDelay(1)
	b.Reset()
	d2 := b.NextDelay(1)
	if d1 != d2 {
		t.Errorf("Reset should be noop: d1=%v, d2=%v", d1, d2)
	}
}

// TestExponentialBackoff_ImplementsInterface 验证 ExponentialBackoff 实现 BackoffStrategy 接口（编译期保障）。
func TestExponentialBackoff_ImplementsInterface(t *testing.T) {
	var _ BackoffStrategy = (*ExponentialBackoff)(nil)
}
