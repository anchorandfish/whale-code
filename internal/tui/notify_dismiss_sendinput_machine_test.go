package tui

// /notify dismiss「写丢失」行为断言机测。
//
// 背景面（值捕获 W 家族死角）：handleNotifyDismiss 里
// m.pendingCommandCmd = m.toast.dismissCurrent() (commands_notify.go:90)
// 这个 tea.Cmd（下一条通知的自动隐藏 timer）必须**经 sendInput 收集并交回 Bubble Tea**。
// 若哪天 /notify dismiss 被改走注册表回调（NewModel 里的值捕获闭包捕获的是
// NewModel 局部副本 —— 值字段写入落在弃置副本上；该接线已随 家族收口删除，
// 重新接回会被 AST 门按"新引入"打红），现象是：
// 队列照样前进（notifier/toast 是共享指针），但 timer cmd 凭空消失
// ⇒ 下一条带超时的通知永远不自动隐藏，用户只看到「已关闭通知」的成功文案。
// 既有测试（tui_regression_test.go:TestNotifyDismissAdvancesQueueAndRecalculatesLayout）
// 只直调 handleCommand 并断言 pendingCommandCmd != nil —— **没断言它真的被送回 Bubble Tea**。
// 本门补的就是这一刀：从 sendInput（生产唯一斜杠命令入口）拿返回 cmd，
// 执行它，断言 toastTimeoutMsg 真的能被产生。
//
// 生产路径（R1 断言）：
// m.sendInput() (stream.go:142，值接收者 —— 返回副本)
// → m.handleCommand(input) (stream.go:214，指针接收者 ⇒ 写在本副本上)
// → 收集 m.pendingCommandCmd (stream.go:213-215)
// → return m, tea.Batch(cmds...) (stream.go:322)
//
// 判别性自证（R4 通道 A）：overlay mutant 把 stream.go 收集段三行摘掉 ⇒
// toastTimeoutMsg 断言精确转红（队列断言仍绿 —— 正是「写丢失」形态的判别特征）。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/notify"
	tea "github.com/charmbracelet/bubbletea"
)

// findToastTimeout 执行 cmd 链，判断 toast 自动隐藏的 timer 消息能否被产生。
// tea.Batch 的返回值是 BatchMsg（[]tea.Cmd），需递归展开。
func findToastTimeout(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	switch b := msg.(type) {
	case tea.BatchMsg:
		for _, sub := range b {
			if findToastTimeout(sub) {
				return true
			}
		}
		return false
	default:
		if _, ok := msg.(toastTimeoutMsg); ok {
			return true
		}
		return false
	}
}

func notifyDismissFixture(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	resizedM := resized.(Model)
	resizedM.reassignServices()
	m = &resizedM

	first := notify.NewNotification("first", "First", "one")
	first.TimeoutMs = 5 // 短超时：执行 tea.Tick cmd 只阻塞几毫秒
	second := notify.NewNotification("second", "Second", "two")
	second.TimeoutMs = 5
	m.notifier.Push(first)
	m.notifier.Push(second)
	if cmd := m.toast.showNext(); cmd == nil {
		t.Fatal("前置失败：showNext 应显示首条通知并返回 timer cmd")
	}
	if cur := m.notifier.Current(); cur == nil || cur.Key != "first" {
		t.Fatalf("前置失败：当前通知应为 first，actual %+v", m.notifier.Current())
	}
	return m
}

func TestNotifyDismissViaSendInputHandsTimerBack(t *testing.T) {
	m := notifyDismissFixture(t)

	// ★ 生产入口：不是 handleCommand 直调，而是 sendInput 全链路
	m.textarea.SetValue("/notify dismiss")
	updated, cmd := m.sendInput()
	updatedM := updated.(Model)
	updatedM.reassignServices()

	// 断言 1：队列前进（notifier 是共享指针，经副本也可见）
	if cur := updatedM.notifier.Current(); cur == nil || cur.Key != "second" {
		t.Fatalf("dismiss 后第二条应成为当前通知，actual %+v", updatedM.notifier.Current())
	}
	// 断言 2（核心 —— 写丢失判别器）：sendInput 返回的 cmd 里必须能执行出
	// toastTimeoutMsg。若 dismiss 被改走孤儿副本闭包 / 收集段被摘，此断言红。
	if !findToastTimeout(cmd) {
		t.Fatal("写丢失：/notify dismiss 产生的 toast timer cmd 没有经 sendInput 交回 Bubble Tea" +
			"（下一条带超时的通知将永不自动隐藏，而界面仍显示「已关闭通知」）")
	}
	// 断言 3：成功文案如实播报被关闭的那条
	found := false
	for _, msg := range updatedM.Messages() {
		if strings.Contains(msg, "First") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dismiss 输出应提及被关闭通知的标题 First")
	}

	// 第二次 dismiss：队列清空，无 timer 需要交回
	updatedM.textarea.SetValue("/notify dismiss")
	updated2, cmd2 := updatedM.sendInput()
	updated2M := updated2.(Model)
	updated2M.reassignServices()
	if updated2M.toast.visible || updated2M.notifier.Current() != nil {
		t.Fatalf("队列清空后 toast 应完全隐藏：visible=%v current=%+v",
			updated2M.toast.visible, updated2M.notifier.Current())
	}
	if findToastTimeout(cmd2) {
		t.Fatal("队列已空，不应再产生 toast timer cmd")
	}
}
