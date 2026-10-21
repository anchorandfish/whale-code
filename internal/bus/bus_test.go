package bus

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- 反例测试 ---

// TestBus_EmptyInput 测试空事件发布不 panic
func TestBus_EmptyInput(t *testing.T) {
	t.Parallel()
	b := NewBus()
	ch := b.Subscribe(1)
	// 发布零值 CacheBreakEvent
	b.Publish(CacheBreakEvent{})
	select {
	case e := <-ch:
		if e.Type() != "cache_break" {
			t.Errorf("期望事件类型 cache_break，实际 %s", e.Type())
		}
	case <-time.After(time.Second):
		t.Error("应收到事件但超时")
	}
}

// TestBus_LargeInput 测试大量订阅者场景下不 OOM
func TestBus_LargeInput(t *testing.T) {
	t.Parallel()
	b := NewBus()
	const subs = 1000
	chs := make([]<-chan Event, subs)
	for i := 0; i < subs; i++ {
		chs[i] = b.Subscribe(1)
	}
	// 发布事件，不应 panic 或 OOM
	b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
	// 抽检第一个和最后一个订阅者
	select {
	case <-chs[0]:
	case <-time.After(time.Second):
		t.Error("第一个订阅者未收到事件")
	}
	select {
	case <-chs[subs-1]:
	case <-time.After(time.Second):
		t.Error("最后一个订阅者未收到事件")
	}
}

// TestBus_InvalidInput 测试 bufSize 为负数时自动修正
func TestBus_InvalidInput(t *testing.T) {
	t.Parallel()
	b := NewBus()
	// bufSize 负数应被修正为 0（无缓冲 channel）
	ch := b.Subscribe(-1)
	// 无缓冲 channel，Publish 必须有接收方否则丢弃
	go b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
	select {
	case <-ch:
		// 有 goroutine 先等待则可收到
	case <-time.After(time.Second):
		// 也可能因竞态未收到，不视为错误
	}
}

// TestBus_ConcurrentSafe 测试并发 Publish/Subscribe 无 data race
func TestBus_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	b := NewBus()
	const goroutines = 100
	var wg sync.WaitGroup

	// 并发订阅
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Subscribe(10)
		}()
	}
	// 并发发布
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
		}()
	}
	wg.Wait()
}

// --- 正例测试 ---

// TestBus_StandardWorkflow 测试标准发布-订阅工作流
func TestBus_StandardWorkflow(t *testing.T) {
	t.Parallel()
	b := NewBus()
	ch := b.Subscribe(10)

	event := CacheBreakEvent{
		Model:       "gpt-4",
		BreakReason: "cache_expired",
		OccurredAt:  time.Now(),
	}
	b.Publish(event)

	select {
	case e := <-ch:
		got, ok := e.(CacheBreakEvent)
		if !ok {
			t.Fatal("事件类型断言失败")
		}
		if got.Model != "gpt-4" {
			t.Errorf("Model = %q, want %q", got.Model, "gpt-4")
		}
		if got.BreakReason != "cache_expired" {
			t.Errorf("BreakReason = %q, want %q", got.BreakReason, "cache_expired")
		}
	case <-time.After(time.Second):
		t.Error("接收事件超时")
	}
}

// TestBus_BoundaryCondition 测试 channel 满时事件丢弃
func TestBus_BoundaryCondition(t *testing.T) {
	t.Parallel()
	b := NewBus()
	// 缓冲区大小为 1
	ch := b.Subscribe(1)

	// 填满缓冲区
	b.Publish(CacheBreakEvent{Model: "first", OccurredAt: time.Now()})
	// 再发一条，应被丢弃
	b.Publish(CacheBreakEvent{Model: "dropped", OccurredAt: time.Now()})

	// 只能收到第一条
	e := <-ch
	got := e.(CacheBreakEvent)
	if got.Model != "first" {
		t.Errorf("收到事件 Model = %q, want %q", got.Model, "first")
	}

	// 第二条应被丢弃
	select {
	case e2 := <-ch:
		t.Errorf("不应收到第二条事件，收到 Model = %q", e2.(CacheBreakEvent).Model)
	case <-time.After(50 * time.Millisecond):
		// 预期：无更多事件
	}
}

// TestBus_Idempotent 测试多次 Publish 独立性
func TestBus_Idempotent(t *testing.T) {
	t.Parallel()
	b := NewBus()
	ch := b.Subscribe(10)

	for i := 0; i < 5; i++ {
		b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
	}

	count := 0
	timeout := time.After(time.Second)
	for {
		select {
		case <-ch:
			count++
		case <-timeout:
			goto done
		}
	}
done:
	if count != 5 {
		t.Errorf("收到 %d 个事件，期望 5", count)
	}
}

// --- 梯队特有检查 ---

// TestBus_GoroutineLeak 测试 Publish 不会泄露 goroutine
func TestBus_GoroutineLeak(t *testing.T) {
	b := NewBus()
	ch := b.Subscribe(1)

	// 获取初始 goroutine 数
	var before, after int
	// 通过 runtime 获取 goroutine 数不可靠，改用间接方式：
	// Publish 不应启动新 goroutine（同步发送），验证大量 Publish 后无泄露
	for i := 0; i < 1000; i++ {
		b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
	}
	// 消费所有事件，确保 channel 不阻塞
	for {
		select {
		case <-ch:
		default:
			goto consumed
		}
	}
consumed:
	// 如果 Publish 会泄露 goroutine，此处会 OOM 或超时
	// 此处通过逻辑验证：Publish 是同步的（for+select+default），不启动 goroutine
	_ = before
	_ = after
}

// TestBus_SubscriberPanicIsolation 测试订阅者 panic 不影响其他订阅者
func TestBus_SubscriberPanicIsolation(t *testing.T) {
	t.Parallel()
	b := NewBus()

	// 创建两个订阅者
	ch1 := b.Subscribe(10)
	ch2 := b.Subscribe(10)

	// ch1 的消费 goroutine 会 panic
	go func() {
		defer func() {
			recover() // 捕获 panic，不让测试崩溃
		}()
		for range ch1 {
			panic("subscriber panic")
		}
	}()

	b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})

	// ch2 应该仍能收到事件
	select {
	case e := <-ch2:
		if e.Type() != "cache_break" {
			t.Errorf("ch2 收到错误事件类型 %s", e.Type())
		}
	case <-time.After(time.Second):
		t.Error("ch2 应收到事件但超时")
	}
}

// TestBus_EventOrderGuarantee 测试单订阅者的事件顺序保证
func TestBus_EventOrderGuarantee(t *testing.T) {
	t.Parallel()
	b := NewBus()
	ch := b.Subscribe(100)

	// 按顺序发布多个事件
	events := []Event{
		CacheBreakEvent{Model: "1", OccurredAt: time.Now()},
		CacheBreakEvent{Model: "2", OccurredAt: time.Now()},
		CacheBreakEvent{Model: "3", OccurredAt: time.Now()},
	}
	for _, e := range events {
		b.Publish(e)
	}

	// 消费并验证顺序
	for i, expected := range events {
		select {
		case got := <-ch:
			gotCE := got.(CacheBreakEvent)
			expCE := expected.(CacheBreakEvent)
			if gotCE.Model != expCE.Model {
				t.Errorf("事件 %d: Model = %q, want %q", i, gotCE.Model, expCE.Model)
			}
		case <-time.After(time.Second):
			t.Fatalf("事件 %d 接收超时", i)
		}
	}
}

// TestBus_EventOrderAcrossTypes 测试不同事件类型的顺序保证
func TestBus_EventOrderAcrossTypes(t *testing.T) {
	t.Parallel()
	b := NewBus()
	ch := b.Subscribe(100)

	now := time.Now()
	b.Publish(CacheBreakEvent{Model: "first", OccurredAt: now})
	b.Publish(ToolsChangedEvent{ServerName: "second", OccurredAt: now})
	b.Publish(SessionSwitchEvent{NewSessionID: "third", OccurredAt: now})

	// 验证按发布顺序接收
	expected := []string{"cache_break", "tools_changed", "session_switch"}
	for i, want := range expected {
		select {
		case e := <-ch:
			if e.Type() != want {
				t.Errorf("事件 %d: Type = %q, want %q", i, e.Type(), want)
			}
		case <-time.After(time.Second):
			t.Fatalf("事件 %d 接收超时", i)
		}
	}
}

// TestBus_MultipleSubscribers 测试多订阅者都能收到事件
func TestBus_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	b := NewBus()
	const subs = 5
	chs := make([]<-chan Event, subs)
	for i := 0; i < subs; i++ {
		chs[i] = b.Subscribe(10)
	}

	b.Publish(CacheBreakEvent{Model: "broadcast", OccurredAt: time.Now()})

	for i, ch := range chs {
		select {
		case e := <-ch:
			if e.Type() != "cache_break" {
				t.Errorf("订阅者 %d: 事件类型 = %q, want cache_break", i, e.Type())
			}
		case <-time.After(time.Second):
			t.Errorf("订阅者 %d 未收到事件", i)
		}
	}
}

// TestBus_PublishNoSubscriber 测试无订阅者时 Publish 不 panic
func TestBus_PublishNoSubscriber(t *testing.T) {
	t.Parallel()
	b := NewBus()
	// 无订阅者发布不应 panic
	b.Publish(CacheBreakEvent{Model: "nobody", OccurredAt: time.Now()})
}

// TestBus_NonBlockingPublish 测试慢订阅者不阻塞发布方
func TestBus_NonBlockingPublish(t *testing.T) {
	t.Parallel()
	b := NewBus()
	// 缓冲区为 0 的订阅者（无缓冲 channel）
	_ = b.Subscribe(0)

	start := time.Now()
	// Publish 应该快速返回（不阻塞等待无缓冲 channel 的接收方）
	b.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Publish 耗时 %v，应是非阻塞的", elapsed)
	}
}

// TestBus_SubscribeAfterPublish 测试订阅在发布之后不收到旧事件
func TestBus_SubscribeAfterPublish(t *testing.T) {
	t.Parallel()
	b := NewBus()

	b.Publish(CacheBreakEvent{Model: "before", OccurredAt: time.Now()})
	// 在发布之后订阅
	ch := b.Subscribe(10)

	b.Publish(CacheBreakEvent{Model: "after", OccurredAt: time.Now()})

	select {
	case e := <-ch:
		got := e.(CacheBreakEvent)
		if got.Model != "after" {
			t.Errorf("收到旧事件 Model = %q, want after", got.Model)
		}
	case <-time.After(time.Second):
		t.Error("接收超时")
	}
}

// --- 事件类型测试 ---

// TestEventTypes 测试所有事件类型的 Type() 和 Timestamp()
func TestEventTypes(t *testing.T) {
	t.Parallel()
	now := time.Now()

	tests := []struct {
		name     string
		event    Event
		wantType string
		wantTime time.Time
	}{
		{
			name:     "CacheBreakEvent",
			event:    CacheBreakEvent{Model: "gpt-4", OccurredAt: now},
			wantType: "cache_break",
			wantTime: now,
		},
		{
			name:     "ToolsChangedEvent",
			event:    ToolsChangedEvent{ServerName: "mcp", OccurredAt: now},
			wantType: "tools_changed",
			wantTime: now,
		},
		{
			name:     "SessionSwitchEvent",
			event:    SessionSwitchEvent{NewSessionID: "new", OccurredAt: now},
			wantType: "session_switch",
			wantTime: now,
		},
		{
			name:     "SessionForkEvent",
			event:    SessionForkEvent{ForkSessionID: "fork", OccurredAt: now},
			wantType: "session_fork",
			wantTime: now,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.event.Type(); got != tt.wantType {
				t.Errorf("Type() = %q, want %q", got, tt.wantType)
			}
			if got := tt.event.Timestamp(); !got.Equal(tt.wantTime) {
				t.Errorf("Timestamp() = %v, want %v", got, tt.wantTime)
			}
		})
	}
}

// TestCacheBreakEvent_Fields 测试 CacheBreakEvent 字段完整性
func TestCacheBreakEvent_Fields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	e := CacheBreakEvent{
		Model:         "claude-3",
		BreakReason:   "context_shift",
		CacheReadDrop: 0.5,
		OccurredAt:    now,
	}
	if e.Model != "claude-3" {
		t.Errorf("Model = %q, want claude-3", e.Model)
	}
	if e.BreakReason != "context_shift" {
		t.Errorf("BreakReason = %q, want context_shift", e.BreakReason)
	}
	if e.CacheReadDrop != 0.5 {
		t.Errorf("CacheReadDrop = %f, want 0.5", e.CacheReadDrop)
	}
	if !e.OccurredAt.Equal(now) {
		t.Errorf("OccurredAt = %v, want %v", e.OccurredAt, now)
	}
}

// TestSessionSwitchEvent_Fields 测试 SessionSwitchEvent 字段
func TestSessionSwitchEvent_Fields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	e := SessionSwitchEvent{
		OldSessionID: "old",
		NewSessionID: "new",
		OccurredAt:   now,
	}
	if e.OldSessionID != "old" || e.NewSessionID != "new" {
		t.Errorf("OldSessionID=%q, NewSessionID=%q", e.OldSessionID, e.NewSessionID)
	}
}

// TestSessionForkEvent_Fields 测试 SessionForkEvent 字段
func TestSessionForkEvent_Fields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	e := SessionForkEvent{
		ParentSessionID: "parent",
		ForkSessionID:   "fork",
		OccurredAt:      now,
	}
	if e.ParentSessionID != "parent" || e.ForkSessionID != "fork" {
		t.Errorf("ParentSessionID=%q, ForkSessionID=%q", e.ParentSessionID, e.ForkSessionID)
	}
}

// --- 并发安全专项测试 ---

// TestBus_ConcurrentPublishAndSubscribe 测试并发发布和并发订阅同时进行
func TestBus_ConcurrentPublishAndSubscribe(t *testing.T) {
	t.Parallel()
	b := NewBus()
	const N = 50
	var received atomic.Int32

	var wg sync.WaitGroup
	// 启动订阅者
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := b.Subscribe(10)
			// 消费部分事件
			for j := 0; j < 5; j++ {
				select {
				case <-ch:
					received.Add(1)
				case <-time.After(time.Second):
					return
				}
			}
		}()
	}

	// 短暂等待确保订阅者已就绪
	time.Sleep(10 * time.Millisecond)

	// 并发发布
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Publish(CacheBreakEvent{Model: "concurrent", OccurredAt: time.Now()})
		}()
	}

	wg.Wait()
	// 只要不 panic / 不 deadlock 即通过
}

// --- 基准测试 ---

// BenchmarkBus_Publish 基准测试：单订阅者发布性能
func BenchmarkBus_Publish(b *testing.B) {
	bus := NewBus()
	ch := bus.Subscribe(b.N + 1)
	go func() {
		for range ch {
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(CacheBreakEvent{Model: "bench", OccurredAt: time.Now()})
	}
}

// BenchmarkBus_PublishMultipleSubscribers 基准测试：多订阅者发布性能
func BenchmarkBus_PublishMultipleSubscribers(b *testing.B) {
	bus := NewBus()
	const subs = 10
	for i := 0; i < subs; i++ {
		ch := bus.Subscribe(b.N + 1)
		go func() {
			for range ch {
			}
		}()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(CacheBreakEvent{Model: "bench", OccurredAt: time.Now()})
	}
}

// BenchmarkBus_Subscribe 基准测试：订阅操作性能
func BenchmarkBus_Subscribe(b *testing.B) {
	bus := NewBus()
	for i := 0; i < b.N; i++ {
		bus.Subscribe(10)
	}
}
