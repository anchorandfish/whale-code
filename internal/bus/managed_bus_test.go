// Package bus — M22-01 死事件清理测试
package bus

import (
	"sync"
	"testing"
	"time"
)

// TestManagedBus_SubscribeUnsubscribe 测试订阅/取消订阅（M22-01）。
func TestManagedBus_SubscribeUnsubscribe(t *testing.T) {
	bus := NewManagedBus()
	defer bus.Stop()

	sub, ch := bus.SubscribeManaged(10)
	if bus.ActiveSubscribers() != 1 {
		t.Errorf("expected 1 active subscriber, got %d", bus.ActiveSubscribers())
	}

	// 发布事件
	bus.Publish(CacheBreakEvent{Model: "test", BreakReason: "test", OccurredAt: time.Now()})

	select {
	case <-ch:
		// 收到事件
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event")
	}

	// 取消订阅
	sub.Unsubscribe()
	if bus.ActiveSubscribers() != 0 {
		t.Errorf("expected 0 active subscribers after unsubscribe, got %d", bus.ActiveSubscribers())
	}

	// channel 应被关闭
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

// TestManagedBus_UnsubscribeIdempotent 测试取消订阅幂等（M22-01）。
func TestManagedBus_UnsubscribeIdempotent(t *testing.T) {
	bus := NewManagedBus()
	defer bus.Stop()

	sub, _ := bus.SubscribeManaged(10)
	sub.Unsubscribe()
	sub.Unsubscribe() // 重复调用应安全

	if bus.ActiveSubscribers() != 0 {
		t.Errorf("expected 0 after double unsubscribe, got %d", bus.ActiveSubscribers())
	}
}

// TestManagedBus_MultipleSubscribers 测试多订阅者（M22-01）。
func TestManagedBus_MultipleSubscribers(t *testing.T) {
	bus := NewManagedBus()
	defer bus.Stop()

	sub1, ch1 := bus.SubscribeManaged(10)
	sub2, ch2 := bus.SubscribeManaged(10)
	defer sub1.Unsubscribe()
	defer sub2.Unsubscribe()

	if bus.ActiveSubscribers() != 2 {
		t.Errorf("expected 2 subscribers, got %d", bus.ActiveSubscribers())
	}

	bus.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})

	// 两个订阅者都应收到
	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case <-ch:
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d timeout", i)
		}
	}
}

// TestManagedBus_DeadEventCleanup 测试死事件清理（M22-01）。
func TestManagedBus_DeadEventCleanup(t *testing.T) {
	bus := NewManagedBus()
	defer bus.Stop()

	// 订阅但不消费（channel 会满）
	sub, _ := bus.SubscribeManaged(1) // bufSize=1
	defer sub.Unsubscribe()

	// 发送多个事件，channel 满后丢弃
	for i := 0; i < 10; i++ {
		bus.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
	}

	// bus 不应阻塞，且订阅者仍活跃
	if bus.ActiveSubscribers() != 1 {
		t.Errorf("expected 1 active subscriber, got %d", bus.ActiveSubscribers())
	}
}

// TestManagedBus_MarkActive 测试标记活跃（M22-01）。
func TestManagedBus_MarkActive(t *testing.T) {
	bus := NewManagedBus()
	defer bus.Stop()

	sub, _ := bus.SubscribeManaged(10)
	defer sub.Unsubscribe()

	sub.MarkActive()
	// 标记活跃后不应被清理（即使长时间未消费）
	if bus.ActiveSubscribers() != 1 {
		t.Errorf("expected 1 after MarkActive, got %d", bus.ActiveSubscribers())
	}
}

// TestManagedBus_Stop 测试停止 Bus（M22-01）。
func TestManagedBus_Stop(t *testing.T) {
	bus := NewManagedBus()

	sub, ch := bus.SubscribeManaged(10)
	_ = sub

	bus.Stop()

	// Stop 后 channel 应被关闭
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after Stop")
	}

	// 重复 Stop 安全
	bus.Stop()
}

// TestManagedBus_ConcurrentPublish 测试并发发布（M22-01）。
func TestManagedBus_ConcurrentPublish(t *testing.T) {
	bus := NewManagedBus()
	defer bus.Stop()

	sub, ch := bus.SubscribeManaged(100)
	defer sub.Unsubscribe()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				bus.Publish(CacheBreakEvent{Model: "test", OccurredAt: time.Now()})
			}
		}()
	}
	wg.Wait()

	// 应至少收到部分事件（非阻塞，可能丢弃）
	received := 0
	timeout := time.After(200 * time.Millisecond)
	for {
		select {
		case <-ch:
			received++
		case <-timeout:
			if received == 0 {
				t.Error("expected at least some events, got 0")
			}
			return
		}
	}
}
