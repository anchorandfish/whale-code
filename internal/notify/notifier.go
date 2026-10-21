// Package notify 提供 UI 通知队列系统，在 TUI 中以 toast 形式呈现非阻塞通知。
// 对标同类 CLI notifications.tsx 的 4 级优先级 + fold 去重 + invalidates 失效 + timeout 机制。
// whale-code 自创：fold 内置 Count+1 合并策略 + history 环形缓冲 + /notify 命令交互。
package notify

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 默认超时时间（毫秒），对标 notifications.tsx DEFAULT_TIMEOUT_MS=8000
const DefaultTimeoutMs = 8000

// 默认队列上限
const defaultMaxQueue = 50

// 默认历史记录上限
const defaultMaxHistory = 100

// Priority 通知优先级（对标 notifications.tsx 的 immediate/high/medium/low）
type Priority int

const (
	// PriorityImmediate 立即显示，打断当前，触发 invalidates 失效非 Immediate 通知
	PriorityImmediate Priority = iota
	// PriorityHigh 高优先级（错误通知）
	PriorityHigh
	// PriorityMedium 中优先级（默认，compact 通知等）
	PriorityMedium
	// PriorityLow 低优先级
	PriorityLow
)

// Notification 单条通知
// 设计定位：借鉴 notifications.tsx 的 key/priority/timeoutMs/fold 机制，
// 实现细节为 whale-code 自创——源码 BaseNotification 仅 key/invalidates/priority/timeoutMs/fold 五字段，
// whale-code 改用 Title/Message 结构化字段 + ID/Count 管理字段。
type Notification struct {
	// ID 唯一标识（自增序号），whale-code 自创（源码无 ID）
	ID int64
	// Key 标识键（源码 key 必填用于 fold/invalidates 匹配；whale-code 复用为去重键，同 key 触发 fold 合并）
	Key string
	// Priority 通知优先级
	Priority Priority
	// Title 标题，whale-code 自创（源码用 text/color 或 jsx，无 Title）
	Title string
	// Message 消息，whale-code 自创（源码用 text/jsx，无 Message）
	Message string
	// TimeoutMs 显示时长；0=缺省（用 DefaultTimeoutMs=8000）；-1=持久（手动关闭）。
	// 注：源码 0 是"立即触发"，whale-code 重新定义
	TimeoutMs int
	// CreatedAt 创建时间
	CreatedAt time.Time
	// Count fold 合并计数（同 key 累加，初始 1），whale-code 自创（源码 fold 是用户回调，无 Count 字段）
	Count int
}

// nextID 全局自增 ID 计数器
var nextID int64

// genID 生成唯一通知 ID（线程安全）
func genID() int64 {
	return atomic.AddInt64(&nextID, 1)
}

// Notifier 通知队列管理器
// 设计定位：借鉴 notifications.tsx 的 queue+current 双字段管理 + processQueue 流转机制；
// 实现细节为 whale-code 自创——源码是 React Context，whale-code 改为线程安全的 Notifier 结构体（sync.Mutex 保护），
// 并自创 history 环形缓冲 + onPush 回调订阅。
type Notifier struct {
	mu         sync.Mutex
	queue      []*Notification     // 待显示队列（按 Priority 降序 + CreatedAt 升序，sort.SliceStable）
	history    []*Notification     // 历史记录（环形缓冲，maxHistory 上限）
	current    *Notification       // 当前显示中
	maxQueue   int                 // 队列上限（默认 50）
	maxHistory int                 // 历史上限（默认 100）
	onPush     func(*Notification) // 推送回调（供 TUI toast 订阅，nil=no-op）
}

// NewNotifier 创建通知管理器（默认 maxQueue=50, maxHistory=100）
func NewNotifier() *Notifier {
	return &Notifier{
		queue:      make([]*Notification, 0),
		history:    make([]*Notification, 0),
		maxQueue:   defaultMaxQueue,
		maxHistory: defaultMaxHistory,
	}
}

// Push 入队通知（含 fold 合并 + invalidates 失效逻辑）
// 行为规约（whale-code 自创策略，借鉴 notifications.tsx 机制语义）：
//   - 若 Key 非空且队列/current 中存在同 Key 通知：fold 合并——Count+1、Message 保留最新、Priority 取较高
//   - 若 Priority=Immediate：失效队列中所有非 Immediate 通知（移到 history）
//   - 队列超 maxQueue 时丢弃最旧的非 Immediate 通知
//   - 触发 onPush 回调（锁外调用，避免死锁）
func (n *Notifier) Push(nf Notification) {
	nf.ID = genID()
	nf.CreatedAt = time.Now()
	if nf.Count == 0 {
		nf.Count = 1
	}

	// 处理 TimeoutMs 语义：0=缺省用 DefaultTimeoutMs
	if nf.TimeoutMs == 0 {
		nf.TimeoutMs = DefaultTimeoutMs
	}

	n.mu.Lock()

	// Immediate 通知：失效队列中所有非 Immediate 通知
	if nf.Priority == PriorityImmediate {
		var remaining []*Notification
		for _, item := range n.queue {
			if item.Priority != PriorityImmediate {
				n.appendToHistory(item)
			} else {
				remaining = append(remaining, item)
			}
		}
		n.queue = remaining

		// 如果当前显示的也是非 Immediate，移入 history
		if n.current != nil && n.current.Priority != PriorityImmediate {
			n.appendToHistory(n.current)
			n.current = nil
		}
	}

	// fold 合并：同 Key 通知 Count+1、Message 保留最新、Priority 取较高
	if nf.Key != "" {
		// 检查 current 中是否有同 Key
		if n.current != nil && n.current.Key == nf.Key {
			n.current.Count++
			if nf.Message != "" {
				n.current.Message = nf.Message
			}
			if nf.Priority < n.current.Priority {
				n.current.Priority = nf.Priority
			}
			n.mu.Unlock()
			// onPush 在锁外调用
			n.fireOnPush(&nf)
			return
		}

		// 检查 queue 中是否有同 Key
		for i, item := range n.queue {
			if item.Key == nf.Key {
				n.queue[i].Count++
				if nf.Message != "" {
					n.queue[i].Message = nf.Message
				}
				if nf.Priority < n.queue[i].Priority {
					n.queue[i].Priority = nf.Priority
				}
				n.mu.Unlock()
				// onPush 在锁外调用
				n.fireOnPush(&nf)
				return
			}
		}
	}

	// 入队
	n.queue = append(n.queue, &nf)

	// 排序：Priority 降序（数字越小优先级越高）+ CreatedAt 升序（FIFO）
	sort.SliceStable(n.queue, func(i, j int) bool {
		if n.queue[i].Priority != n.queue[j].Priority {
			return n.queue[i].Priority < n.queue[j].Priority
		}
		return n.queue[i].CreatedAt.Before(n.queue[j].CreatedAt)
	})

	// 队列超上限时丢弃最旧的非 Immediate 通知
	for len(n.queue) > n.maxQueue {
		// 找到最旧的非 Immediate（排序后非 Immediate 在后面）
		idx := -1
		for i := len(n.queue) - 1; i >= 0; i-- {
			if n.queue[i].Priority != PriorityImmediate {
				idx = i
				break
			}
		}
		if idx == -1 {
			// 全是 Immediate，丢弃最后一个
			idx = len(n.queue) - 1
		}
		n.queue = append(n.queue[:idx], n.queue[idx+1:]...)
	}

	n.mu.Unlock()

	// onPush 在锁外调用
	n.fireOnPush(&nf)
}

// fireOnPush 触发 onPush 回调（调用方需确保不在锁内）
func (n *Notifier) fireOnPush(nf *Notification) {
	n.mu.Lock()
	fn := n.onPush
	n.mu.Unlock()
	if fn != nil {
		fn(nf)
	}
}

// Next 取下一个待显示通知（按优先级降序 + 同优先级 FIFO）
// 行为规约：从 queue 头部取出，设为 current；空队列返回 nil
func (n *Notifier) Next() *Notification {
	n.mu.Lock()
	defer n.mu.Unlock()

	if len(n.queue) == 0 {
		return nil
	}

	nf := n.queue[0]
	n.queue = n.queue[1:]
	n.current = nf
	return nf
}

// Current 返回当前显示中通知（无则 nil）
func (n *Notifier) Current() *Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.current
}

// Dismiss 关闭当前通知（手动或 timeout 触发），移入 history
// 行为规约：current 置 nil，旧 current 追加到 history（超 maxHistory 丢弃最旧）
func (n *Notifier) Dismiss() {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.current != nil {
		n.appendToHistory(n.current)
		n.current = nil
	}
}

// appendToHistory 将通知追加到历史记录（环形缓冲，超 maxHistory 丢弃最旧）
// 调用方需持有锁
func (n *Notifier) appendToHistory(nf *Notification) {
	n.history = append(n.history, nf)
	if len(n.history) > n.maxHistory {
		n.history = n.history[1:]
	}
}

// List 返回当前队列快照（供 /notify list，按优先级排序）
func (n *Notifier) List() []*Notification {
	n.mu.Lock()
	defer n.mu.Unlock()

	result := make([]*Notification, len(n.queue))
	copy(result, n.queue)
	return result
}

// History 返回历史通知（最近 N 条，按时间倒序）
func (n *Notifier) History() []*Notification {
	n.mu.Lock()
	defer n.mu.Unlock()

	result := make([]*Notification, len(n.history))
	copy(result, n.history)

	// 按时间倒序
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

// Invalidate 失效指定 Key 的所有通知（对标 invalidates，按 Key）
// 行为规约：从 queue 移除匹配 Key 的通知，移入 history
func (n *Notifier) Invalidate(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	var remaining []*Notification
	for _, item := range n.queue {
		if item.Key == key {
			n.appendToHistory(item)
		} else {
			remaining = append(remaining, item)
		}
	}
	n.queue = remaining

	// 如果 current 匹配，也移入 history
	if n.current != nil && n.current.Key == key {
		n.appendToHistory(n.current)
		n.current = nil
	}
}

// SetOnPush 设置推送回调（TUI toast 订阅，nil=no-op）
func (n *Notifier) SetOnPush(fn func(*Notification)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onPush = fn
}

// ClearHistory 清空历史
func (n *Notifier) ClearHistory() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.history = n.history[:0]
}

// --- 便捷构造函数 ---

// NewNotification 创建默认通知（Medium 优先级 + DefaultTimeoutMs 超时）
func NewNotification(key, title, message string) Notification {
	return Notification{
		Key:       key,
		Priority:  PriorityMedium,
		Title:     title,
		Message:   message,
		TimeoutMs: DefaultTimeoutMs,
		Count:     1,
	}
}

// NewImmediateNotification 创建立即通知（Immediate 优先级 + TimeoutMs=-1 持久，需手动关闭）
func NewImmediateNotification(key, title, message string) Notification {
	return Notification{
		Key:       key,
		Priority:  PriorityImmediate,
		Title:     title,
		Message:   message,
		TimeoutMs: -1,
		Count:     1,
	}
}

// NewErrorNotification 创建错误通知（High 优先级，对标 N6 ErrorID）
// Key 用 "error:<errorID>" 格式（同 ErrorID fold 去重）
func NewErrorNotification(errorID, message string) Notification {
	return Notification{
		Key:       "error:" + errorID,
		Priority:  PriorityHigh,
		Title:     "Error",
		Message:   message,
		TimeoutMs: DefaultTimeoutMs,
		Count:     1,
	}
}

// NewTaskNotification 创建任务事件通知（对标 N1 任务完成/失败）
// status: "completed"/"failed"/"cancelled"
// Key 用 "task:<taskID>" 格式（同任务 ID fold 去重）
func NewTaskNotification(taskID, status string) Notification {
	var title string
	switch status {
	case "completed":
		title = "Task Completed"
	case "failed":
		title = "Task Failed"
	case "cancelled":
		title = "Task Cancelled"
	default:
		title = "Task Event"
	}

	priority := PriorityMedium
	if status == "failed" {
		priority = PriorityHigh
	}

	return Notification{
		Key:       "task:" + taskID,
		Priority:  priority,
		Title:     title,
		Message:   status,
		TimeoutMs: DefaultTimeoutMs,
		Count:     1,
	}
}
