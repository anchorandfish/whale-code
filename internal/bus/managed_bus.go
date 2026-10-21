// Package bus — M22-01 死事件主动清理（SNAPSHOT）
//
// 本文件为 Bus 增加"订阅者退出后主动清理"能力，避免死事件堆积。
// 对标 Node EventEmitter removeListener（https://nodejs.org/api/events.html#emitterremovelistener).
//
// 设计要点（候选 A 选定）：
// - 接口签名：Subscription 句柄，持 Unsubscribe 方法
// - 结构体字段：subEntry 含 channel + activeFlag
// - 分层权责：本文件只做"订阅管理"，事件发布仍由 bus.go
// - 降级策略：Unsubscribe 失败仅记录日志，不阻断
// - TTL规则：inactive 订阅超过 subscriberTTL 自动清理（5 分钟）
package bus

import (
	"sync"
	"sync/atomic"
	"time"
)

// subscriberTTL 是非活跃订阅者清理 TTL（5 分钟，对标 Node EventEmitter maxListeners 超时）。
const subscriberTTL = 5 * time.Minute

// subscriberCleanupInterval 是清理循环间隔（1 分钟）。
const subscriberCleanupInterval = 1 * time.Minute

// subEntry 是订阅者条目（SNAPSHOT-1）。
//
// 并发安全（TD-10）：lastActive 改为 atomic.Int64 存储 UnixNano 时间戳，
// 允许 Publish 在 RLock 下并发更新 lastActive 而不产生 data race。
// 读取侧（cleanupInactive）通过 atomic.Load 读取，无需持锁。
type subEntry struct {
	ch         chan Event
	active     bool         // 订阅者是否活跃
	lastActive atomic.Int64 // 最后活跃时间（UnixNano），原子读写避免 RLock 下并发写竞态
}

// ManagedBus 是带订阅者管理的 Bus（SNAPSHOT-2）。
//
// 在 Bus 基础上增加：
// - SubscribeManaged：返回 Subscription 句柄（持 Unsubscribe）
// - 死订阅自动清理（inactive 超过 subscriberTTL）
// - 健康检查（返回活跃订阅者数）
//
// 线程安全：内部 mutex 保护订阅者列表。
type ManagedBus struct {
	mu      sync.RWMutex
	subs    []*subEntry
	stopCh  chan struct{}
	stopped bool
}

// Subscription 是订阅句柄（SNAPSHOT-3）。
//
// 持有 Unsubscribe 方法，调用后从 Bus 移除订阅并关闭 channel。
type Subscription struct {
	bus   *ManagedBus
	entry *subEntry
}

// NewManagedBus 创建带订阅者管理的 Bus（SNAPSHOT-4）。
//
// 启动后台清理 goroutine，定期清理非活跃订阅者。
func NewManagedBus() *ManagedBus {
	b := &ManagedBus{stopCh: make(chan struct{})}
	go b.cleanupLoop()
	return b
}

// SubscribeManaged 订阅事件总线，返回 Subscription 句柄（SNAPSHOT-5）。
//
// bufSize 指定 channel 缓冲区大小。
// 调用方应通过 defer sub.Unsubscribe() 清理订阅。
//
// 并发安全（TD-10）：lastActive 为 atomic.Int64，通过 Store 设置初始值。
func (b *ManagedBus) SubscribeManaged(bufSize int) (*Subscription, <-chan Event) {
	if bufSize < 0 {
		bufSize = 0
	}
	ch := make(chan Event, bufSize)
	entry := &subEntry{
		ch:     ch,
		active: true,
	}
	// TD-10：atomic.Int64 不能在 struct literal 中初始化，需通过 Store 设置
	entry.lastActive.Store(time.Now().UnixNano())
	b.mu.Lock()
	b.subs = append(b.subs, entry)
	b.mu.Unlock()
	return &Subscription{bus: b, entry: entry}, ch
}

// Publish 非阻塞发布事件到所有活跃订阅者（SNAPSHOT-6）。
//
// 行为：
// - 仅向 active=true 的订阅者发送
// - channel 满时丢弃事件（不阻塞发布方）
// - 发送后更新 lastActive（标记订阅者活跃）
//
// 并发安全（TD-10）：lastActive 通过 atomic.Store 更新，
// 允许在 RLock 下并发写而不产生 data race（原 time.Time 字段在 RLock 下并发写会导致竞态）。
func (b *ManagedBus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if !sub.active {
			continue
		}
		select {
		case sub.ch <- e:
			// TD-10：原子更新 lastActive，避免 RLock 下并发写竞态
			sub.lastActive.Store(time.Now().UnixNano())
		default:
			// channel 满，丢弃（不阻塞）
		}
	}
}

// Unsubscribe 取消订阅（SNAPSHOT-7）。
//
// 行为：
// - 标记 entry.active=false
// - 关闭 channel（通知订阅者退出）
// - 从 subs 列表移除
// - 幂等（重复调用安全）
func (s *Subscription) Unsubscribe() {
	if s == nil || s.bus == nil || s.entry == nil {
		return
	}
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if !s.entry.active {
		return // 已取消订阅，幂等
	}
	s.entry.active = false
	close(s.entry.ch)
	// 从列表移除
	for i, sub := range s.bus.subs {
		if sub == s.entry {
			s.bus.subs = append(s.bus.subs[:i], s.bus.subs[i+1:]...)
			break
		}
	}
}

// ActiveSubscribers 返回活跃订阅者数量（SNAPSHOT-8）。
func (b *ManagedBus) ActiveSubscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	count := 0
	for _, sub := range b.subs {
		if sub.active {
			count++
		}
	}
	return count
}

// cleanupLoop 定期清理非活跃订阅者（SNAPSHOT-9）。
func (b *ManagedBus) cleanupLoop() {
	ticker := time.NewTicker(subscriberCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.cleanupInactive()
		}
	}
}

// cleanupInactive 清理非活跃订阅者（SNAPSHOT-10）。
//
// 清理条件：
// - active=false（已 Unsubscribe 但未从列表移除的残留）
// - active=true 但 lastActive 超过 subscriberTTL（长时间未消费）
//
// 并发安全（TD-10）：lastActive 通过 atomic.Load 读取，与 Publish 的 atomic.Store 形成原子读写。
func (b *ManagedBus) cleanupInactive() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	var kept []*subEntry
	for _, sub := range b.subs {
		if !sub.active {
			continue // 已清理
		}
		// TD-10：通过 atomic.Load 读取 UnixNano 时间戳，还原为 time.Time
		lastActive := time.Unix(0, sub.lastActive.Load())
		if now.Sub(lastActive) > subscriberTTL {
			// 长时间未消费，标记为非活跃并关闭 channel
			sub.active = false
			close(sub.ch)
			continue
		}
		kept = append(kept, sub)
	}
	b.subs = kept
}

// Stop 停止 Bus，清理所有订阅者（SNAPSHOT-11）。
func (b *ManagedBus) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.stopped = true
	close(b.stopCh)
	for _, sub := range b.subs {
		if sub.active {
			sub.active = false
			close(sub.ch)
		}
	}
	b.subs = nil
}

// MarkActive 标记订阅者为活跃（SNAPSHOT-12）。
//
// 供订阅者主动报告活跃状态（避免被 cleanup 误清理）。
//
// 并发安全（TD-10）：lastActive 通过 atomic.Store 更新。
func (s *Subscription) MarkActive() {
	if s == nil || s.entry == nil {
		return
	}
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if s.entry.active {
		// TD-10：原子更新 lastActive
		s.entry.lastActive.Store(time.Now().UnixNano())
	}
}
