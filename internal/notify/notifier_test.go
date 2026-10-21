package notify

import (
	"sync"
	"testing"
	"time"
)

// TestPush_FoldSameKey 同 Key 两次 Push，队列中应 1 条，Count=2
func TestPush_FoldSameKey(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "test", Title: "标题", Message: "消息1"})
	n.Push(Notification{Key: "test", Title: "标题", Message: "消息2"})

	list := n.List()
	if len(list) != 1 {
		t.Fatalf("期望队列 1 条，实际 %d 条", len(list))
	}
	if list[0].Count != 2 {
		t.Fatalf("期望 Count=2，实际 Count=%d", list[0].Count)
	}
	if list[0].Message != "消息2" {
		t.Fatalf("期望 Message 保留最新='消息2'，实际='%s'", list[0].Message)
	}
}

// TestPush_InvalidateOnImmediate Immediate 通知入队，队列中非 Immediate 应被移除
func TestPush_InvalidateOnImmediate(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "low1", Priority: PriorityLow, Title: "低", Message: "低优先级"})
	n.Push(Notification{Key: "med1", Priority: PriorityMedium, Title: "中", Message: "中优先级"})
	n.Push(Notification{Key: "imm1", Priority: PriorityImmediate, Title: "紧急", Message: "立即通知"})

	list := n.List()
	for _, nf := range list {
		if nf.Priority != PriorityImmediate {
			t.Fatalf("Immediate 入队后，队列中仍有非 Immediate 通知: Key=%s Priority=%d", nf.Key, nf.Priority)
		}
	}
	// 非 Immediate 应被移到 history
	history := n.History()
	if len(history) == 0 {
		t.Fatal("Immediate 入队后，history 应包含被失效的非 Immediate 通知")
	}
}

// TestPush_InvalidateOnImmediate_KeepsImmediate Immediate 保留所有 Immediate（invalidates 只失效非 Immediate）
func TestPush_InvalidateOnImmediate_KeepsImmediate(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "imm1", Priority: PriorityImmediate, Title: "紧急1", Message: "消息1"})
	n.Push(Notification{Key: "imm2", Priority: PriorityImmediate, Title: "紧急2", Message: "消息2"})

	list := n.List()
	immediateCount := 0
	for _, nf := range list {
		if nf.Priority == PriorityImmediate {
			immediateCount++
		}
	}
	if immediateCount != 2 {
		t.Fatalf("期望队列中有 2 个 Immediate 通知，实际 %d 个", immediateCount)
	}
}

// TestNext_PriorityOrder 高优先级先出队（即使后入队）
func TestNext_PriorityOrder(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "low", Priority: PriorityLow, Title: "低", Message: "低"})
	n.Push(Notification{Key: "high", Priority: PriorityHigh, Title: "高", Message: "高"})
	n.Push(Notification{Key: "med", Priority: PriorityMedium, Title: "中", Message: "中"})

	first := n.Next()
	if first.Priority != PriorityHigh {
		t.Fatalf("期望第一个出队为 High，实际为 %d", first.Priority)
	}
	second := n.Next()
	if second.Priority != PriorityMedium {
		t.Fatalf("期望第二个出队为 Medium，实际为 %d", second.Priority)
	}
}

// TestNext_FIFO_SamePriority 同优先级 FIFO
func TestNext_FIFO_SamePriority(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "a", Priority: PriorityMedium, Title: "A", Message: "A"})
	time.Sleep(time.Millisecond) // 确保时间不同
	n.Push(Notification{Key: "b", Priority: PriorityMedium, Title: "B", Message: "B"})

	first := n.Next()
	if first.Key != "a" {
		t.Fatalf("期望 FIFO 先出 'a'，实际为 '%s'", first.Key)
	}
}

// TestDismiss_MoveToHistory Dismiss 后 current=nil，history +1
func TestDismiss_MoveToHistory(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "test", Priority: PriorityMedium, Title: "测试", Message: "消息"})
	n.Next() // 设为 current

	n.Dismiss()
	if n.Current() != nil {
		t.Fatal("Dismiss 后 current 应为 nil")
	}
	history := n.History()
	if len(history) != 1 {
		t.Fatalf("期望 history 1 条，实际 %d 条", len(history))
	}
}

// TestInvalidate_RemovesByKey Invalidate 后队列中无该 Key 通知，其他 Key 保留
func TestInvalidate_RemovesByKey(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "a", Priority: PriorityMedium, Title: "A", Message: "A"})
	n.Push(Notification{Key: "b", Priority: PriorityMedium, Title: "B", Message: "B"})

	n.Invalidate("a")

	list := n.List()
	for _, nf := range list {
		if nf.Key == "a" {
			t.Fatal("Invalidate('a') 后队列中仍存在 Key='a' 的通知")
		}
	}
	// Key='b' 应保留
	found := false
	for _, nf := range list {
		if nf.Key == "b" {
			found = true
		}
	}
	if !found {
		t.Fatal("Invalidate('a') 后 Key='b' 应保留")
	}
}

// TestNotifier_ConcurrentPush 100 并发 Push 无 race（CI 补跑 -race）
func TestNotifier_ConcurrentPush(t *testing.T) {
	n := NewNotifier()
	n.maxQueue = 200 // 增大上限避免截断
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n.Push(Notification{Key: "", Priority: PriorityLow, Title: "并发", Message: "测试"})
		}(i)
	}
	wg.Wait()
	list := n.List()
	if len(list) != 100 {
		t.Fatalf("期望 100 条通知，实际 %d 条", len(list))
	}
}

// TestPush_PriorityUpgradeOnFold fold 合并时 Priority 取较高
func TestPush_PriorityUpgradeOnFold(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "test", Priority: PriorityLow, Title: "低", Message: "低"})
	n.Push(Notification{Key: "test", Priority: PriorityHigh, Title: "高", Message: "高"})

	list := n.List()
	if len(list) != 1 {
		t.Fatalf("期望队列 1 条，实际 %d 条", len(list))
	}
	if list[0].Priority != PriorityHigh {
		t.Fatalf("期望 fold 后 Priority=High，实际=%d", list[0].Priority)
	}
	if list[0].Count != 2 {
		t.Fatalf("期望 Count=2，实际=%d", list[0].Count)
	}
}

// TestHistory_RingBuffer 超 maxHistory 丢弃最旧
func TestHistory_RingBuffer(t *testing.T) {
	n := NewNotifier()
	n.maxHistory = 5 // 缩小上限方便测试

	// 推送 5 条并全部 Dismiss 到 history
	for i := 0; i < 5; i++ {
		n.Push(Notification{Key: "", Priority: PriorityLow, Title: "测试", Message: "消息"})
		n.Next()
		n.Dismiss()
	}
	if len(n.History()) != 5 {
		t.Fatalf("期望 history 5 条，实际 %d 条", len(n.History()))
	}

	// 再推送 1 条，应丢弃最旧的
	n.Push(Notification{Key: "", Priority: PriorityLow, Title: "新", Message: "新消息"})
	n.Next()
	n.Dismiss()
	if len(n.History()) != 5 {
		t.Fatalf("期望 history 仍为 5 条（环形缓冲），实际 %d 条", len(n.History()))
	}
}

// TestPush_TimeoutNegative_Persistent TimeoutMs=-1 不触发自动 Dismiss
func TestPush_TimeoutNegative_Persistent(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "persist", Priority: PriorityImmediate, Title: "持久", Message: "不自动关闭", TimeoutMs: -1})

	cur := n.Current()
	if cur == nil {
		// 可能还在 queue 中
		n.Next()
		cur = n.Current()
	}
	if cur == nil {
		t.Fatal("应有通知在 current 中")
	}
	if cur.TimeoutMs != -1 {
		t.Fatalf("期望 TimeoutMs=-1，实际=%d", cur.TimeoutMs)
	}
}

// TestPush_TimeoutZero_UsesDefault TimeoutMs=0 用 DEFAULT_TIMEOUT_MS=8000
func TestPush_TimeoutZero_UsesDefault(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "zero", Priority: PriorityMedium, Title: "零", Message: "用默认", TimeoutMs: 0})

	list := n.List()
	if len(list) != 1 {
		t.Fatalf("期望队列 1 条，实际 %d 条", len(list))
	}
	if list[0].TimeoutMs != DefaultTimeoutMs {
		t.Fatalf("期望 TimeoutMs=%d（默认），实际=%d", DefaultTimeoutMs, list[0].TimeoutMs)
	}
}

// TestNewNotification_Defaults 验证 NewNotification 默认值
func TestNewNotification_Defaults(t *testing.T) {
	nf := NewNotification("key", "标题", "消息")
	if nf.Priority != PriorityMedium {
		t.Fatalf("期望 Priority=Medium，实际=%d", nf.Priority)
	}
	if nf.TimeoutMs != DefaultTimeoutMs {
		t.Fatalf("期望 TimeoutMs=%d，实际=%d", DefaultTimeoutMs, nf.TimeoutMs)
	}
	if nf.Key != "key" {
		t.Fatalf("期望 Key='key'，实际='%s'", nf.Key)
	}
}

// TestNewErrorNotification_KeyFormat 验证 NewErrorNotification Key 格式
func TestNewErrorNotification_KeyFormat(t *testing.T) {
	nf := NewErrorNotification("ErrTimeout", "超时")
	if nf.Key != "error:ErrTimeout" {
		t.Fatalf("期望 Key='error:ErrTimeout'，实际='%s'", nf.Key)
	}
	if nf.Priority != PriorityHigh {
		t.Fatalf("期望 Priority=High，实际=%d", nf.Priority)
	}
}

// TestNewTaskNotification_KeyFormat 验证 NewTaskNotification Key 格式和优先级
func TestNewTaskNotification_KeyFormat(t *testing.T) {
	// 完成通知
	nf := NewTaskNotification("task-123", "completed")
	if nf.Key != "task:task-123" {
		t.Fatalf("期望 Key='task:task-123'，实际='%s'", nf.Key)
	}
	if nf.Priority != PriorityMedium {
		t.Fatalf("completed 期望 Priority=Medium，实际=%d", nf.Priority)
	}

	// 失败通知
	nfFail := NewTaskNotification("task-456", "failed")
	if nfFail.Priority != PriorityHigh {
		t.Fatalf("failed 期望 Priority=High，实际=%d", nfFail.Priority)
	}
}

// TestNewImmediateNotification_Persistent 验证 NewImmediateNotification 是持久的
func TestNewImmediateNotification_Persistent(t *testing.T) {
	nf := NewImmediateNotification("alert", "告警", "紧急")
	if nf.Priority != PriorityImmediate {
		t.Fatalf("期望 Priority=Immediate，实际=%d", nf.Priority)
	}
	if nf.TimeoutMs != -1 {
		t.Fatalf("期望 TimeoutMs=-1（持久），实际=%d", nf.TimeoutMs)
	}
}

// TestClearHistory 清空历史
func TestClearHistory(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "a", Priority: PriorityLow, Title: "A", Message: "A"})
	n.Next()
	n.Dismiss()
	if len(n.History()) != 1 {
		t.Fatal("前置条件：history 应有 1 条")
	}

	n.ClearHistory()
	if len(n.History()) != 0 {
		t.Fatalf("清空后 history 应为 0 条，实际 %d 条", len(n.History()))
	}
}

// TestPush_FoldSameKeyOnCurrent current 中同 Key 也应 fold
func TestPush_FoldSameKeyOnCurrent(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "test", Priority: PriorityMedium, Title: "测试", Message: "消息1"})
	n.Next() // 移到 current

	// 再次 Push 同 Key
	n.Push(Notification{Key: "test", Priority: PriorityMedium, Title: "测试", Message: "消息2"})

	cur := n.Current()
	if cur == nil {
		t.Fatal("current 应不为 nil")
	}
	if cur.Count != 2 {
		t.Fatalf("期望 current.Count=2，实际=%d", cur.Count)
	}
	if cur.Message != "消息2" {
		t.Fatalf("期望 current.Message='消息2'，实际='%s'", cur.Message)
	}
	// queue 应为空（fold 到 current，不入新队）
	if len(n.List()) != 0 {
		t.Fatalf("fold 到 current 后 queue 应为空，实际 %d 条", len(n.List()))
	}
}

// TestInvalidate_CurrentMatching current 匹配 Key 也移入 history
func TestInvalidate_CurrentMatching(t *testing.T) {
	n := NewNotifier()
	n.Push(Notification{Key: "target", Priority: PriorityMedium, Title: "目标", Message: "消息"})
	n.Next() // 移到 current

	n.Invalidate("target")

	if n.Current() != nil {
		t.Fatal("Invalidate 匹配 current 后，current 应为 nil")
	}
	if len(n.History()) != 1 {
		t.Fatalf("期望 history 1 条，实际 %d 条", len(n.History()))
	}
}
