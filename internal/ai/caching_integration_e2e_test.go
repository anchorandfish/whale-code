package ai

import (
	"context"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/bus"
)

// TestStreamWithRetry_CacheBreakIntegration 端到端验证：
// StreamWithRetry + RetryConfig.OnCachingIntegration → 检测到断裂 → bus 收到 CacheBreakEvent。
// 复用 ai_test.go 中已定义的 mockClient（同包），通过预填充 successCh 模拟事件流。
func TestStreamWithRetry_CacheBreakIntegration(t *testing.T) {
	b := bus.NewBus()
	sub := b.Subscribe(16)

	ci := NewCachingIntegration("claude-3-5-sonnet", 0.05)
	ci.SetBus(b)

	// 预先建立高基线：cacheRead=10000
	ci.RecordStateBeforeRequest("p1", nil, "claude-3-5-sonnet")
	ci.CheckBreakAfterResponse(10000) // 基线 = 10000

	// mock client 返回 UsageEvent.CacheRead=100（暴跌 90%，absDecrease=9900 > 2000）
	evCh := make(chan Event, 2)
	evCh <- UsageEvent{InputTokens: 1000, OutputTokens: 500, CacheRead: 100}
	evCh <- MessageStopEvent{}
	close(evCh)
	mock := &mockClient{successCh: evCh}

	cfg := RetryConfig{
		MaxRetries:           0,
		OnCachingIntegration: ci,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := StreamWithRetry(ctx, mock, Request{
		SystemPrompt: []string{"p1"},
		Model:        "claude-3-5-sonnet",
	}, cfg, nil, nil)
	if err != nil {
		t.Fatalf("StreamWithRetry failed: %v", err)
	}

	// 消费完事件流（触发 wrapWithCacheBreakCheck 的检测逻辑）
	count := 0
	for range events {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 events forwarded, got %d", count)
	}

	// 验证 bus 收到 CacheBreakEvent
	select {
	case evt := <-sub:
		cbEvt, ok := evt.(bus.CacheBreakEvent)
		if !ok {
			t.Fatalf("expected CacheBreakEvent, got %T", evt)
		}
		if cbEvt.Model != "claude-3-5-sonnet" {
			t.Errorf("Model = %q, want claude-3-5-sonnet", cbEvt.Model)
		}
		if cbEvt.CacheReadDrop < 0.89 {
			t.Errorf("CacheReadDrop = %.2f, want >= 0.89", cbEvt.CacheReadDrop)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout: CacheBreakEvent not published")
	}
}

func TestHistoricalCacheMetricsDoNotReplaceSnapshot(t *testing.T) {
	ci := NewCachingIntegration("claude-3-5-sonnet", 0.05)
	ci.RecordCacheMetrics(100, 10, 80)
	before := ci.GetCacheInfo()
	ci.RecordHistoricalCacheMetrics(999, 99, 1)
	after := ci.GetCacheInfo()
	if before == nil || after == nil || *after != *before {
		t.Fatalf("historical usage changed active cache snapshot: before=%#v after=%#v", before, after)
	}
	if got := ci.GetMetrics().TotalRequests; got != 2 {
		t.Fatalf("historical usage was not included in aggregate metrics: requests=%d", got)
	}
}

func TestModelAndSessionSwitchClearCacheSnapshot(t *testing.T) {
	ci := NewCachingIntegration("claude-3-5-sonnet", 0.05)
	ci.RecordCacheMetrics(100, 10, 80)
	ci.UpdateModel("gpt-4")
	if ci.GetCacheInfo() != nil {
		t.Fatal("model switch retained the previous model cache snapshot")
	}

	b := bus.NewBus()
	ci.SubscribeSessionEvents(b)
	ci.RecordCacheMetrics(200, 20, 120)
	b.Publish(bus.SessionSwitchEvent{OldSessionID: "a", NewSessionID: "b", OccurredAt: time.Now()})
	deadline := time.Now().Add(time.Second)
	for ci.GetCacheInfo() != nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ci.GetCacheInfo() != nil {
		t.Fatal("session switch retained the previous session cache snapshot")
	}
}

// TestStreamWithRetry_NoCachingIntegration_BackwardCompat 验证：
// RetryConfig.OnCachingIntegration = nil 时，行为与修复前完全一致（事件流原样透传）。
func TestStreamWithRetry_NoCachingIntegration_BackwardCompat(t *testing.T) {
	evCh := make(chan Event, 2)
	evCh <- TextDeltaEvent{Content: "hello"}
	evCh <- MessageStopEvent{}
	close(evCh)
	mock := &mockClient{successCh: evCh}

	cfg := RetryConfig{MaxRetries: 0} // OnCachingIntegration 为 nil

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := StreamWithRetry(ctx, mock, Request{Model: "x"}, cfg, nil, nil)
	if err != nil {
		t.Fatalf("StreamWithRetry failed: %v", err)
	}

	count := 0
	for evt := range events {
		count++
		if count == 1 {
			if _, ok := evt.(TextDeltaEvent); !ok {
				t.Errorf("first event should be TextDelta, got %T", evt)
			}
		}
	}
	if count != 2 {
		t.Errorf("expected 2 events, got %d", count)
	}
}

// TestStreamWithRetry_ConsumerEarlyExit_NoLeak 验证：
// 消费者提前退出（ctx 取消）后，wrapWithCacheBreakCheck 的 goroutine 能通过 ctx 退出，不 panic、不死锁。
func TestStreamWithRetry_ConsumerEarlyExit_NoLeak(t *testing.T) {
	ci := NewCachingIntegration("claude-3-5-sonnet", 0.05)
	ci.SetBus(bus.NewBus())

	evCh := make(chan Event, 2)
	evCh <- UsageEvent{CacheRead: 100}
	evCh <- MessageStopEvent{}
	close(evCh)
	mock := &mockClient{successCh: evCh}

	cfg := RetryConfig{MaxRetries: 0, OnCachingIntegration: ci}

	ctx, cancel := context.WithCancel(context.Background())
	events, err := StreamWithRetry(ctx, mock, Request{Model: "x"}, cfg, nil, nil)
	if err != nil {
		t.Fatalf("StreamWithRetry failed: %v", err)
	}

	// 只消费 1 个事件就取消 ctx
	<-events
	cancel()

	// 给 goroutine 一点退出时间；无法直接检测 goroutine 数，
	// 通过不阻塞、不 panic 验证（-race + CI 验证无泄漏）
	time.Sleep(100 * time.Millisecond)

	// 取消后 out channel 最终会被 close（goroutine 退出时 defer close(out)）
	// 验证不会死锁：在超时内能 drain 完或检测到关闭
	select {
	case _, ok := <-events:
		// ok=false 表示已关闭，正常；ok=true 表示还有残留事件也允许
		_ = ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deadlock: events channel not drained/closed after cancel")
	}
}
