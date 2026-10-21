// Package context — tests for CacheBreakHandler (P0-5 / C18 contract).
package context

import (
	"sync"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bus"
)

// stubNonCacheBreakEvent 是测试专用桩事件，用于验证 CacheBreakHandler 过滤非 CacheBreak 事件。
// 不放入 bus 包生产代码，避免再次制造死事件定义。
type stubNonCacheBreakEvent struct {
	occurredAt time.Time
}

// Type 返回事件类型标识。
func (e stubNonCacheBreakEvent) Type() string { return "stub_non_cache_break" }

// Timestamp 返回事件发生时间。
func (e stubNonCacheBreakEvent) Timestamp() time.Time { return e.occurredAt }

// TestCacheBreakHandler_ReceivesEvent verifies that a CacheBreakEvent
// published on the bus is delivered to the handler's onBreak callback
// and recorded in RecentBreaks.
func TestCacheBreakHandler_ReceivesEvent(t *testing.T) {
	b := bus.NewBus()

	var (
		mu       sync.Mutex
		got      []bus.CacheBreakEvent
		callback = func(evt bus.CacheBreakEvent) {
			mu.Lock()
			got = append(got, evt)
			mu.Unlock()
		}
	)

	h := NewCacheBreakHandler(b, callback)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// Publish a CacheBreakEvent from the "AI module" (per C18, AI is sole publisher).
	evt := bus.CacheBreakEvent{
		Model:         "claude-3-5-sonnet",
		BreakReason:   "tool_use boundary shifted",
		CacheReadDrop: 0.55,
		OccurredAt:    time.Now(),
	}
	b.Publish(evt)

	// Wait for delivery (non-blocking bus may drop, so retry-publish a few times).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 callback invocation, got %d", len(got))
	}
	if got[0].Model != evt.Model {
		t.Errorf("Model mismatch: got %q want %q", got[0].Model, evt.Model)
	}

	recent := h.RecentBreaks()
	if len(recent) != 1 {
		t.Errorf("RecentBreaks len = %d, want 1", len(recent))
	}
	if !h.IsCacheBroken() {
		t.Error("IsCacheBroken = false, want true (CacheReadDrop=0.55 > 0.3)")
	}
}

// TestCacheBreakHandler_FiltersNonCacheBreak verifies that non-CacheBreakEvent
// events on the bus are ignored by the handler.
func TestCacheBreakHandler_FiltersNonCacheBreak(t *testing.T) {
	b := bus.NewBus()

	h := NewCacheBreakHandler(b, nil)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// Publish an unrelated event type.
	b.Publish(stubNonCacheBreakEvent{occurredAt: time.Now()})

	// Give the handler a moment to (not) process it.
	time.Sleep(50 * time.Millisecond)

	if len(h.RecentBreaks()) != 0 {
		t.Errorf("RecentBreaks should be empty for non-CacheBreak events, got %d", len(h.RecentBreaks()))
	}
	if h.IsCacheBroken() {
		t.Error("IsCacheBroken should be false with no CacheBreak events")
	}
}

// TestCacheBreakHandler_DoubleStartReturnsError verifies that calling Start
// twice returns an error and does not spawn a second goroutine.
func TestCacheBreakHandler_DoubleStartReturnsError(t *testing.T) {
	b := bus.NewBus()
	h := NewCacheBreakHandler(b, nil)
	if err := h.Start(); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	defer h.Stop()

	if err := h.Start(); err == nil {
		t.Error("second Start should return an error, got nil")
	}
}

// TestCacheBreakHandler_NilBusStartFails verifies that Start fails when
// no bus has been provided.
func TestCacheBreakHandler_NilBusStartFails(t *testing.T) {
	h := NewCacheBreakHandler(nil, nil)
	if err := h.Start(); err == nil {
		t.Error("Start with nil bus should return an error")
		defer h.Stop() // defensive cleanup in case it erroneously started
	}
}

// TestCacheBreakHandler_ClearHistory verifies that ClearHistory wipes
// the recorded break events.
func TestCacheBreakHandler_ClearHistory(t *testing.T) {
	b := bus.NewBus()
	h := NewCacheBreakHandler(b, nil)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	b.Publish(bus.CacheBreakEvent{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now()})

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && len(h.RecentBreaks()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	if len(h.RecentBreaks()) != 1 {
		t.Fatalf("expected 1 event before ClearHistory, got %d", len(h.RecentBreaks()))
	}

	h.ClearHistory()
	if len(h.RecentBreaks()) != 0 {
		t.Errorf("after ClearHistory, expected 0 events, got %d", len(h.RecentBreaks()))
	}
	if h.IsCacheBroken() {
		t.Error("IsCacheBroken should be false after ClearHistory")
	}
}

// TestCacheBreakHandler_StopIsIdempotent verifies that Stop can be called
// multiple times without panicking.
func TestCacheBreakHandler_StopIsIdempotent(t *testing.T) {
	b := bus.NewBus()
	h := NewCacheBreakHandler(b, nil)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	h.Stop()
	h.Stop() // should not panic
	h.Stop() // should not panic
}

// TestCacheBreakHandler_SetBusAfterStartFails verifies that SetBus
// returns an error after Start has been called.
func TestCacheBreakHandler_SetBusAfterStartFails(t *testing.T) {
	b1 := bus.NewBus()
	b2 := bus.NewBus()
	h := NewCacheBreakHandler(b1, nil)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	if err := h.SetBus(b2); err == nil {
		t.Error("SetBus after Start should return an error")
	}
}

// TestCompactWithCacheInfo_BrokenCacheFallback 验证缓存断裂时压缩降级为普通压缩（方案 D）。
// 断裂时 cache prefix 失效，对齐 boundary 无意义，应走 inner.Compact 保留更多近期消息。
func TestCompactWithCacheInfo_BrokenCacheFallback(t *testing.T) {
	b := bus.NewBus()
	h := NewCacheBreakHandler(b, nil)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	compactor := NewCacheAwareCompactor(nil) // estimator 为 nil，仅测试降级路径
	compactor.SetBreakHandler(h)

	// 构造 cacheInfo 使 HasCacheInfo() 为 true（走 cache-aware 分支）
	cacheInfo := &ai.CacheInfo{CacheReadTokens: 1000}

	// 未断裂时：应走 cache-aware 路径（cacheInfo 非空）
	// 由于 messages 为空，两者都返回空结果，无法区分。此处主要验证断裂后降级不 panic。
	cfg := CompactionConfig{PreserveRecentMessages: 1}

	// 断裂前：cacheInfo 非空，走 cache-aware 分支（不 panic 即可）
	_ = compactor.CompactWithCacheInfoInternal(nil, cfg, cacheInfo, 0)

	// 发布 CacheBreakEvent 触发断裂
	b.Publish(bus.CacheBreakEvent{
		Model:         "claude-sonnet-4-5",
		BreakReason:   "test break",
		CacheReadDrop: 0.5,
		OccurredAt:    time.Now(),
	})

	// 等待事件处理
	time.Sleep(100 * time.Millisecond)

	// 断裂后：应降级为 inner.Compact（不走 cache-aware 对齐）
	if !h.IsCacheBroken() {
		t.Fatal("发布 CacheBreakEvent 后 IsCacheBroken 应为 true")
	}
	// 降级路径应正常返回（不 panic）
	result := compactor.CompactWithCacheInfoInternal(nil, cfg, cacheInfo, 0)
	if result == nil {
		t.Error("断裂降级应返回非 nil 结果")
	}
}

// === 候选 B 测试：缓存恢复冷却机制 ===

// TestCacheBreakHandler_IsCacheHealthy 验证 IsCacheHealthy 冷却窗口逻辑（候选 B）。
// 测试场景：
// 1. 无断裂历史 → 健康
// 2. 断裂在冷却窗口内 → 不健康
// 3. 断裂超过冷却窗口 → 健康（自动恢复）
// 4. cooldown=0 边界：使用注入的 recoveryCooldown，未注入则用默认 5min
// 5. 显式 cooldown > 0 时使用显式值
func TestCacheBreakHandler_IsCacheHealthy(t *testing.T) {
	t.Run("无断裂历史_健康", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		if !h.IsCacheHealthy(5 * time.Minute) {
			t.Error("无断裂历史时 IsCacheHealthy 应为 true")
		}
	})

	t.Run("断裂在冷却窗口内_不健康", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		// 直接构造 recentBreaks（绕过 bus，简化测试）
		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now()},
		}
		h.mu.Unlock()

		// cooldown=5min，刚断裂 → 不健康
		if h.IsCacheHealthy(5 * time.Minute) {
			t.Error("断裂在 5min 冷却窗口内时 IsCacheHealthy 应为 false")
		}
	})

	t.Run("断裂超过冷却窗口_健康", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		// 断裂发生在 10 分钟前，cooldown=5min → 已恢复
		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-10 * time.Minute)},
		}
		h.mu.Unlock()

		if !h.IsCacheHealthy(5 * time.Minute) {
			t.Error("断裂超过 5min 冷却窗口时 IsCacheHealthy 应为 true")
		}
	})

	t.Run("cooldown为0_使用注入的recoveryCooldown", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		// 注入 recoveryCooldown=100ms
		h.SetCacheRecoveryCooldown(100 * time.Millisecond)

		// 断裂发生在 50ms 前，注入 cooldown=100ms → 在窗口内 → 不健康
		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-50 * time.Millisecond)},
		}
		h.mu.Unlock()

		if h.IsCacheHealthy(0) {
			t.Error("cooldown=0 + 注入 100ms + 断裂 50ms 前 → 应在窗口内（不健康）")
		}

		// 等待超过 100ms 冷却
		time.Sleep(150 * time.Millisecond)

		if !h.IsCacheHealthy(0) {
			t.Error("cooldown=0 + 注入 100ms + 断裂 200ms 前 → 应已恢复（健康）")
		}
	})

	t.Run("cooldown为0_未注入_使用默认5min", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		// 不注入 recoveryCooldown（保持零值）

		// 断裂发生在 1 分钟前
		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-1 * time.Minute)},
		}
		h.mu.Unlock()

		// cooldown=0 + 未注入 → 使用默认 5min → 1min < 5min → 不健康
		if h.IsCacheHealthy(0) {
			t.Error("cooldown=0 + 未注入 + 断裂 1min 前 + 默认 5min → 应在窗口内（不健康）")
		}
	})

	t.Run("显式cooldown优先于注入值", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		// 注入 recoveryCooldown=10min
		h.SetCacheRecoveryCooldown(10 * time.Minute)

		// 断裂发生在 3 分钟前
		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-3 * time.Minute)},
		}
		h.mu.Unlock()

		// 显式 cooldown=1min（< 3min）→ 已恢复 → 健康（显式值优先于注入的 10min）
		if !h.IsCacheHealthy(1 * time.Minute) {
			t.Error("显式 cooldown=1min + 断裂 3min 前 → 应已恢复（健康，显式值优先）")
		}

		// 显式 cooldown=5min（> 3min）→ 在窗口内 → 不健康
		if h.IsCacheHealthy(5 * time.Minute) {
			t.Error("显式 cooldown=5min + 断裂 3min 前 → 应在窗口内（不健康）")
		}
	})
}

// TestCacheBreakHandler_TryRecoverCache 验证 TryRecoverCache 缓存恢复（候选 B）。
// 测试场景：
// 1. 无断裂历史 → 返回 false（无需恢复）
// 2. 断裂在冷却窗口内 → 返回 false（未恢复）
// 3. 断裂超过冷却窗口 → 返回 true + recentBreaks 清空
// 4. recoveryCooldown=0 边界（落实审查建议优化 4）：使用默认 5min
func TestCacheBreakHandler_TryRecoverCache(t *testing.T) {
	t.Run("无断裂历史_返回false", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		if h.TryRecoverCache() {
			t.Error("无断裂历史时 TryRecoverCache 应返回 false")
		}
	})

	t.Run("断裂在冷却窗口内_返回false", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		h.SetCacheRecoveryCooldown(5 * time.Minute)

		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now()},
		}
		h.mu.Unlock()

		if h.TryRecoverCache() {
			t.Error("断裂在冷却窗口内时 TryRecoverCache 应返回 false")
		}
		// recentBreaks 不应被清空
		if len(h.RecentBreaks()) != 1 {
			t.Errorf("未恢复时 recentBreaks 应保留，len = %d, want 1", len(h.RecentBreaks()))
		}
	})

	t.Run("断裂超过冷却窗口_返回true_清空历史", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		h.SetCacheRecoveryCooldown(100 * time.Millisecond)

		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-200 * time.Millisecond)},
		}
		h.mu.Unlock()

		if !h.TryRecoverCache() {
			t.Error("断裂超过冷却窗口时 TryRecoverCache 应返回 true")
		}
		// recentBreaks 应被清空
		if len(h.RecentBreaks()) != 0 {
			t.Errorf("恢复后 recentBreaks 应清空，len = %d, want 0", len(h.RecentBreaks()))
		}
		// 恢复后 IsCacheBroken 应为 false
		if h.IsCacheBroken() {
			t.Error("恢复后 IsCacheBroken 应为 false")
		}
	})

	t.Run("recoveryCooldown为0_使用默认5min_落实审查建议优化4", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		// 不注入 recoveryCooldown（保持零值）

		// 断裂发生在 1 分钟前
		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-1 * time.Minute)},
		}
		h.mu.Unlock()

		// recoveryCooldown=0 + 未注入 → 使用默认 5min → 1min < 5min → 不恢复
		if h.TryRecoverCache() {
			t.Error("recoveryCooldown=0 + 未注入 + 断裂 1min 前 + 默认 5min → 应返回 false（不恢复）")
		}
		// recentBreaks 不应被清空
		if len(h.RecentBreaks()) != 1 {
			t.Errorf("未恢复时 recentBreaks 应保留，len = %d, want 1", len(h.RecentBreaks()))
		}
	})

	t.Run("多次恢复幂等", func(t *testing.T) {
		h := NewCacheBreakHandler(nil, nil)
		h.SetCacheRecoveryCooldown(100 * time.Millisecond)

		h.mu.Lock()
		h.recentBreaks = []bus.CacheBreakEvent{
			{Model: "m", BreakReason: "r", CacheReadDrop: 0.5, OccurredAt: time.Now().Add(-200 * time.Millisecond)},
		}
		h.mu.Unlock()

		// 第一次恢复：成功
		if !h.TryRecoverCache() {
			t.Error("第一次 TryRecoverCache 应返回 true")
		}
		// 第二次恢复：无断裂历史 → 返回 false（幂等）
		if h.TryRecoverCache() {
			t.Error("第二次 TryRecoverCache 应返回 false（已恢复，无断裂历史）")
		}
	})
}

// TestCacheBreakHandler_SetCacheRecoveryCooldown 验证 SetCacheRecoveryCooldown 注入（候选 B）。
func TestCacheBreakHandler_SetCacheRecoveryCooldown(t *testing.T) {
	h := NewCacheBreakHandler(nil, nil)

	// 初始应为零值
	h.mu.Lock()
	initial := h.recoveryCooldown
	h.mu.Unlock()
	if initial != 0 {
		t.Errorf("initial recoveryCooldown should be 0, got %v", initial)
	}

	// 注入
	h.SetCacheRecoveryCooldown(42 * time.Second)

	h.mu.Lock()
	got := h.recoveryCooldown
	h.mu.Unlock()
	if got != 42*time.Second {
		t.Errorf("after SetCacheRecoveryCooldown, recoveryCooldown = %v, want 42s", got)
	}
}
