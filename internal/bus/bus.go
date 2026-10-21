// Package bus provides a cross-module event bus for decoupling decision events
// between ai, context, session, tui, and other modules.
//
// 这是 C18/C20 冲突解决的权威定义方。
// AI 模块和 Context 模块都应使用 bus.Bus 发布/订阅事件，
// 而非各自实现 DecisionBus。
package bus

import (
	"sync"
	"time"
)

// Event 是所有总线事件的通用接口。
// 每个事件必须能报告自己的类型和发生时间。
type Event interface {
	// Type 返回事件类型标识（如 "retry", "degrade", "compact"）。
	Type() string
	// Timestamp 返回事件发生的时间。
	Timestamp() time.Time
}

// Bus 是非阻塞的事件总线。
// 发布方调用 Publish，订阅方通过 Subscribe 获取只读 channel。
// 当 channel 满时，事件被丢弃（非阻塞语义），避免拖慢发布方。
//
// 并发安全：Publish 和 Subscribe 可被多个 goroutine 并发调用。
type Bus struct {
	mu          sync.RWMutex
	subscribers []chan Event
}

// NewBus 创建一个新的事件总线。
func NewBus() *Bus {
	return &Bus{}
}

// Publish 非阻塞地发布一个事件到所有订阅者。
// 如果某个订阅者的 channel 已满，该事件对该订阅者被丢弃（不阻塞发布方）。
// 并发安全。
//
// 修复 P0-15：增加 recover 保护，防止向已关闭 channel 发送导致 panic。
// 虽然 Publish 持有 RLock 与 Unsubscribe 的 Lock 互斥，理论上 channel 不会在
// 发送期间被关闭，但作为防御性编程，仍对每次发送进行 recover 保护，
// 确保任何意外关闭的 channel 不会导致进程 panic。
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		// 修复 P0-15：recover 保护，防止向已关闭 channel 发送导致 panic
		func() {
			defer func() {
				_ = recover()
			}()
			select {
			case ch <- e:
			default:
				// channel 满时丢弃，不阻塞
			}
		}()
	}
}

// Subscribe 订阅事件总线，返回一个只读 channel。
// bufSize 指定 channel 的缓冲区大小。
// 订阅者应尽快消费事件，否则事件可能被丢弃。
// 返回的 channel 不会被关闭（除非未来显式关闭总线）。
// 订阅者在停止时应调用 Unsubscribe 释放资源，避免内存泄漏。
func (b *Bus) Subscribe(bufSize int) <-chan Event {
	if bufSize < 0 {
		bufSize = 0
	}
	ch := make(chan Event, bufSize)
	b.mu.Lock()
	b.subscribers = append(b.subscribers, ch)
	b.mu.Unlock()
	return ch
}

// Unsubscribe 取消订阅并关闭 channel。
// 调用后该 channel 不再接收新事件，Publish 时跳过已关闭的 channel。
// 并发安全。
//
// 修复 P0-15：解决 Bus 无 Unsubscribe 机制导致的内存泄漏。
// 长生命周期进程（daemon）的订阅者切片会线性增长，导致内存泄漏和
// Publish 退化为 O(N)。Unsubscribe 从切片中移除订阅者并关闭 channel。
func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subscribers {
		// 双向 channel 转换为只读 channel 进行比较
		if (<-chan Event)(sub) == ch {
			// 从切片中移除该订阅者
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			close(sub)
			return
		}
	}
}
