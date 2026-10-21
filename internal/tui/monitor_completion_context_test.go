package tui

// ── C 判据：后台完成事实必须能到达**模型上下文**（有界、恰好一次、不带内容） ────
//
// 背景（2026-10-13 手测卡② §19.5 选项 C）：进程结束时把一条**极小结构化事实**
// 推给模型，让它能被"叫醒"（知道该去 MonitorTail 看了）。
//
// 为什么落在 buildMessages：模型上下文 = sessionMessagesToAPI(CurrentSession().GetMessages())
// （stream.go:663-665），而 append 到**显示缓冲**（SessionModel.messages）的东西模型看不到
// （卡② §18.2 已证：done 只走显示缓冲）。buildMessages 是每轮请求构建的**唯一**入口
// （生产调用点只有 stream.go:443）⇒ 在此注入 = 只在"模型要看"时注入。
//
// 三条断言（缺一即半修）：
// 1. 能到：done 事件之后，下一轮 buildMessages 里含 <monitor-completion> 事实；
// 2. 恰好一次：再构建一次不再重复（有界 —— 消费即清）；
// 3. 不带内容：事实里**不得**含进程输出正文（被禁的是"内容"，不是"事实"，§19.3）。
//
// before 自检（R4-7）：改动前 buildMessages 只做纯映射、无注入点 ⇒ 断言 1 在 before 态**红**。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/session"
)

// monitorCompletionText 在 provider 消息列表里找含完成标记的 user 消息，返回其文本。
func monitorCompletionText(entries []ai.MessageEntry) (string, bool) {
	for _, e := range entries {
		if e.Role != "user" {
			continue
		}
		s, ok := e.Content.(string)
		if ok && strings.Contains(s, "<monitor-completion>") {
			return s, true
		}
	}
	return "", false
}

func TestMonitorCompletion_ReachesModelContext(t *testing.T) {
	m := newTUIRegressionModel(t)

	// 夹具健全性：注入前不含标记。
	if _, ok := monitorCompletionText(m.buildMessages()); ok {
		t.Fatal("夹具不健全：注入前模型上下文已含 <monitor-completion>")
	}

	// 走生产入口喂一条 done —— output 刻意塞一个哨兵串，用于验证"事实不带内容"。
	const sentinel = "SECRET-OUTPUT-LINE-must-not-reach-model"
	m = feedMonitorEventThroughProduction(t, m, monitorEvent{
		eventType: "done",
		monitorID: "mon_9",
		status:    monitor.StatusFinished,
		exitCode:  0,
		output:    sentinel + "\n",
	})

	// 模拟用户的下一条输入（真实流程：turn 外完成 → 用户再发话 → buildMessages）。
	m.sessionService.CurrentSession().AddMessage(session.UserText("后台那个跑完了吗？"))

	entries := m.buildMessages()
	text, ok := monitorCompletionText(entries)
	if !ok {
		t.Fatal("后台完成事实没有到达模型上下文 ⇒ 模型对后台结束一无所知（选项 C 未生效）")
	}
	// 事实应点名 monitor id 与结果（模型据此决定要不要 MonitorTail）。
	if !strings.Contains(text, "mon_9") {
		t.Errorf("完成事实未点名 monitor id：\n%s", text)
	}
	// 边界：不得把输出正文当事实带进模型上下文。
	if strings.Contains(text, sentinel) {
		t.Errorf("完成事实里带进了进程输出正文（违反「有界、不带内容」）：\n%s", text)
	}

	// 恰好一次：再构建不应重复注入。
	if _, again := monitorCompletionText(m.buildMessages()); again {
		t.Error("完成事实被重复注入（应恰好一次、消费即清）")
	}
}
