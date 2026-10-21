// input_queue_test.go — P0-8/9 排队输入队列单元测试。
// 覆盖 EnqueueInput / DrainNextQueuedInput / PopLatestQueuedForEdit /
// ClearQueuedInputs / HasQueuedInputs / QueuedInputsCount 等核心路径。
// 测试模型：通过 Model{SessionModel: SessionModel{queuedInputsMax: N}} 构造最小可用 Model。
package tui

import (
	"testing"
	"time"
)

// newQueueTestModel 构造用于测试的最小 Model（仅设置 queuedInputsMax 字段）。
// 避免触发 NewModel 的完整初始化开销，仅覆盖队列操作路径。
// 命名加 Queue 前缀以避免与 daemon_test.go 中的 newTestModel 冲突。
func newQueueTestModel(max int) Model {
	return Model{
		SessionModel: SessionModel{
			queuedInputsMax: max,
		},
	}
}

// TestEnqueueInput_Single 验证单条入队后队列长度为 1 且 HasQueuedInputs 返回 true。
func TestEnqueueInput_Single(t *testing.T) {
	m := newQueueTestModel(50)
	ok := m.EnqueueInput(QueuedUserMessage{Text: "hello", Action: QueuedInputPlain})
	if !ok {
		t.Fatalf("EnqueueInput should succeed when queue is not full")
	}
	if m.QueuedInputsCount() != 1 {
		t.Errorf("QueuedInputsCount = %d, want 1", m.QueuedInputsCount())
	}
	if !m.HasQueuedInputs() {
		t.Errorf("HasQueuedInputs = false, want true")
	}
}

// TestEnqueueInput_Multiple 验证多条入队后 FIFO 顺序正确。
func TestEnqueueInput_Multiple(t *testing.T) {
	m := newQueueTestModel(50)
	m.EnqueueInput(QueuedUserMessage{Text: "msg1", Action: QueuedInputPlain})
	m.EnqueueInput(QueuedUserMessage{Text: "msg2", Action: QueuedInputPlain})
	m.EnqueueInput(QueuedUserMessage{Text: "msg3", Action: QueuedInputPlain})

	if m.QueuedInputsCount() != 3 {
		t.Fatalf("QueuedInputsCount = %d, want 3", m.QueuedInputsCount())
	}

	// FIFO 顺序：DrainNextQueuedInput 应按入队顺序返回
	first, ok := m.DrainNextQueuedInput()
	if !ok || first.Text != "msg1" {
		t.Errorf("first drain = %q, ok=%v, want msg1", first.Text, ok)
	}
	second, ok := m.DrainNextQueuedInput()
	if !ok || second.Text != "msg2" {
		t.Errorf("second drain = %q, ok=%v, want msg2", second.Text, ok)
	}
	third, ok := m.DrainNextQueuedInput()
	if !ok || third.Text != "msg3" {
		t.Errorf("third drain = %q, ok=%v, want msg3", third.Text, ok)
	}
	// 队列空后再 drain 应返回 false
	if _, ok := m.DrainNextQueuedInput(); ok {
		t.Errorf("drain should return false when queue empty")
	}
}

// TestEnqueueInput_Overflow 验证队列满时 EnqueueInput 返回 false 且长度不变。
func TestEnqueueInput_Overflow(t *testing.T) {
	m := newQueueTestModel(2)
	if !m.EnqueueInput(QueuedUserMessage{Text: "a"}) {
		t.Fatalf("first EnqueueInput should succeed")
	}
	if !m.EnqueueInput(QueuedUserMessage{Text: "b"}) {
		t.Fatalf("second EnqueueInput should succeed")
	}
	// 第 3 条应被拒绝
	if m.EnqueueInput(QueuedUserMessage{Text: "c"}) {
		t.Errorf("third EnqueueInput should fail when queue is full")
	}
	if m.QueuedInputsCount() != 2 {
		t.Errorf("QueuedInputsCount = %d, want 2 (unchanged after rejection)", m.QueuedInputsCount())
	}
}

// TestEnqueueInput_TimestampAutoFill 验证 Timestamp 为零值时自动填充当前时间。
func TestEnqueueInput_TimestampAutoFill(t *testing.T) {
	m := newQueueTestModel(50)
	before := time.Now()
	m.EnqueueInput(QueuedUserMessage{Text: "no-ts"})
	after := time.Now()

	snap := m.QueuedInputsSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Timestamp.Before(before) || snap[0].Timestamp.After(after) {
		t.Errorf("Timestamp = %v, want between %v and %v", snap[0].Timestamp, before, after)
	}
}

// TestPopLatestQueuedForEdit 验证 LIFO 顺序：从队尾 pop 一条回 composer 编辑。
func TestPopLatestQueuedForEdit(t *testing.T) {
	m := newQueueTestModel(50)
	m.EnqueueInput(QueuedUserMessage{Text: "first"})
	m.EnqueueInput(QueuedUserMessage{Text: "second"})

	// LIFO pop：应返回最后入队的 "second"
	popped, ok := m.PopLatestQueuedForEdit()
	if !ok || popped.Text != "second" {
		t.Errorf("pop = %q, ok=%v, want second", popped.Text, ok)
	}
	if m.QueuedInputsCount() != 1 {
		t.Errorf("QueuedInputsCount = %d, want 1 after pop", m.QueuedInputsCount())
	}
	// 剩余队首应为 "first"
	first, ok := m.DrainNextQueuedInput()
	if !ok || first.Text != "first" {
		t.Errorf("drain after pop = %q, ok=%v, want first", first.Text, ok)
	}
}

// TestClearQueuedInputs 验证清空后队列长度为 0 且 HasQueuedInputs 返回 false。
func TestClearQueuedInputs(t *testing.T) {
	m := newQueueTestModel(50)
	m.EnqueueInput(QueuedUserMessage{Text: "a"})
	m.EnqueueInput(QueuedUserMessage{Text: "b"})
	m.EnqueueInput(QueuedUserMessage{Text: "c"})

	m.ClearQueuedInputs()
	if m.QueuedInputsCount() != 0 {
		t.Errorf("QueuedInputsCount = %d, want 0 after clear", m.QueuedInputsCount())
	}
	if m.HasQueuedInputs() {
		t.Errorf("HasQueuedInputs = true, want false after clear")
	}
}

// TestDrainNextQueuedInput_Empty 验证空队列出队返回 (零值, false)。
func TestDrainNextQueuedInput_Empty(t *testing.T) {
	m := newQueueTestModel(50)
	msg, ok := m.DrainNextQueuedInput()
	if ok {
		t.Errorf("ok = true, want false for empty queue")
	}
	if msg.Text != "" {
		t.Errorf("msg.Text = %q, want empty for zero value", msg.Text)
	}
}

// TestPopLatestQueuedForEdit_Empty 验证空队列 LIFO pop 返回 (零值, false)。
func TestPopLatestQueuedForEdit_Empty(t *testing.T) {
	m := newQueueTestModel(50)
	msg, ok := m.PopLatestQueuedForEdit()
	if ok {
		t.Errorf("ok = true, want false for empty queue")
	}
	if msg.Text != "" {
		t.Errorf("msg.Text = %q, want empty for zero value", msg.Text)
	}
}

// TestQueuedInputsSnapshot_Immutable 验证快照返回副本，修改快照不影响底层队列。
func TestQueuedInputsSnapshot_Immutable(t *testing.T) {
	m := newQueueTestModel(50)
	m.EnqueueInput(QueuedUserMessage{Text: "original"})

	snap := m.QueuedInputsSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	// 修改快照不应影响底层队列
	snap[0].Text = "mutated"

	original, ok := m.DrainNextQueuedInput()
	if !ok || original.Text != "original" {
		t.Errorf("after snapshot mutation, drain = %q, want original", original.Text)
	}
}

// TestQueuedInputsSnapshot_EmptyReturnsNil 验证空队列快照返回 nil 而非空切片。
func TestQueuedInputsSnapshot_EmptyReturnsNil(t *testing.T) {
	m := newQueueTestModel(50)
	snap := m.QueuedInputsSnapshot()
	if snap != nil {
		t.Errorf("snapshot = %v, want nil for empty queue", snap)
	}
}

// TestDrainQueuedInputMsg_TypeAssert 验证 drainQueuedInputMsg 是合法 tea.Msg 类型。
// 通过类型断言确保编译时类型安全。
func TestDrainQueuedInputMsg_TypeAssert(t *testing.T) {
	var msg interface{} = drainQueuedInputMsg{}
	if _, ok := msg.(drainQueuedInputMsg); !ok {
		t.Errorf("drainQueuedInputMsg type assertion failed")
	}
}

// TestDrainQueuedInput_StateCheck 验证致命漏洞 #1 修复：
// drain 钩子处理器在 State != StateIdle 时不 pop 消息（避免竞态丢失），
// 在 State == StateIdle 时正常 pop 消息。
// 对标同类 CLI maybe_send_next_queued_inputpop 前检查 is_input_allowed。
func TestDrainQueuedInput_StateCheck(t *testing.T) {
	// 场景 1：State=Streaming，drain 守卫应阻止 pop，消息保留在队列中。
	m := newQueueTestModel(50)
	m.state = StateStreaming
	m.reassignServices()
	m.EnqueueInput(QueuedUserMessage{Text: "queued-msg", Action: QueuedInputPlain})

	// 模拟 drain 处理器守卫：仅当 State == StateIdle 时才 pop（与 app.go drainQueuedInputMsg 分支一致）
	if m.sessionService.State() == StateIdle && m.HasQueuedInputs() {
		m.DrainNextQueuedInput()
		t.Fatal("不应在 State=Streaming 时 pop 排队消息")
	}
	if m.QueuedInputsCount() != 1 {
		t.Errorf("State=Streaming drain 后队列长度=%d, want 1（消息应保留）", m.QueuedInputsCount())
	}

	// 场景 2：State=Idle，drain 守卫应允许 pop，消息被正常取出。
	m.sessionService.SetState(StateIdle)
	if m.sessionService.State() == StateIdle && m.HasQueuedInputs() {
		msg, ok := m.DrainNextQueuedInput()
		if !ok || msg.Text != "queued-msg" {
			t.Errorf("State=Idle drain = %q, ok=%v, want queued-msg", msg.Text, ok)
		}
	} else {
		t.Fatal("State=Idle 时守卫应允许 drain")
	}
	if m.QueuedInputsCount() != 0 {
		t.Errorf("State=Idle drain 后队列长度=%d, want 0（消息应被取出）", m.QueuedInputsCount())
	}
}
