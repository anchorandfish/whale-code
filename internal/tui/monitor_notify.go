package tui

// monitor_notify.go — 把「后台进程完成」这一**有界事实**推给模型上下文（选项 C，2026-10-13）。
//
// 背景（手测卡② §19）：whale 的 Monitor 是**拉不推** —— 输出与完成事件都只进**显示缓冲**
// （SessionModel.messages），而模型上下文 = sessionMessagesToAPI(CurrentSession().GetMessages())
// （stream.go:663-665）是**另一个缓冲** ⇒ 模型对"后台跑完了"一无所知，只能盲猜时机调 MonitorTail。
//
// 「不推内容」是对的（§19.3：倒进对话流 = 第二份无界副本）；本文件补的只是**推那条有界的事实**：
// 让模型在下一轮被"叫醒"，知道该去 MonitorTail 看哪一台。
//
// 关键设计（第一性原理，非权宜）：
//   - **注入点在 buildMessages**：模型上下文的唯一构建入口（生产调用点只有 stream.go:443），
//     在这里注入 = 只在"模型真要发请求"时注入，不产生第二个权威。
//   - **合并进最后一条 user 消息、不新增条目**：新增会造出"连续两条 user"，且会写进会话历史
//     （污染 export/resume）。合并则一次请求一次、不落盘。
//   - **消费即清**：恰好一次，有界。
//   - **只带事实不带内容**：id / 终态 / 退码；ev.output 刻意不参与。

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/monitor"
)

// maxPendingMonitorFacts 是「待推给模型的后台完成事实」队列上限。
//
// 为什么需要界：任一时刻只有"最近若干次完成"对模型有意义 —— 模型被叫醒后会自己
// 用 MonitorTail 读具体某台。全量保留既无价值，又会让下一轮上下文无界增长
// （违反已拍板的"对话流有界"口径，§19.3）。
const maxPendingMonitorFacts = 20

// monitorCompletionMarker 是注入模型上下文的结构化标签名。
//
// ★ 用标签包裹（而非一句自然语言）的理由：读者是**模型** —— 标签让"这是系统事实、
// 不是用户说的话"在 token 层面就无歧义，也便于判据/排查机械识别（本仓 R4 口径：
// 结构化事实不得靠散文关键词断言）。
const monitorCompletionMarker = "monitor-completion"

// recordMonitorCompletion 记下一条后台完成事实，等下一次构建请求时推给模型。
//
// 调用点：handleMonitorEvent 的 "done" 分支（render_extras.go）。
// ★ 只记事实、**不带内容**：ev.output 刻意不参与 —— 把输出正文塞回上下文正是 §19.3
// 禁掉的"第二份无界副本"。
func (m *Model) recordMonitorCompletion(ev monitorEvent) {
	fact := formatMonitorCompletionFact(ev.monitorID, ev.status, ev.exitCode)
	m.pendingMonitorFacts = append(m.pendingMonitorFacts, fact)
	if n := len(m.pendingMonitorFacts); n > maxPendingMonitorFacts {
		drop := n - maxPendingMonitorFacts
		m.pendingMonitorFacts = m.pendingMonitorFacts[drop:]
		m.monitorFactsDropped += drop
	}
}

// formatMonitorCompletionFact 生成极小结构化事实（**不含输出内容**）。
func formatMonitorCompletionFact(id string, status monitor.MonitorStatus, exitCode int) string {
	return fmt.Sprintf("%s %s exit=%d", id, status, exitCode)
}

// injectMonitorFacts 把 pending 事实合并进 provider 消息列表，然后清空队列（恰好一次）。
//
// 合并策略：**拼进最后一条 user 消息**，而不是新增一条 —— 本仓每轮请求的最后一条
// user 消息就是用户刚发的输入（sessionMessagesToAPI 之后），拼进去既不新增条目、
// 也不改动会话历史。没有 user 消息时（罕见，如工具循环中途的请求）退化为追加一条。
func (m *Model) injectMonitorFacts(entries []ai.MessageEntry) []ai.MessageEntry {
	if len(m.pendingMonitorFacts) == 0 && m.monitorFactsDropped == 0 {
		return entries
	}
	var sb strings.Builder
	sb.WriteString("<" + monitorCompletionMarker + ">\n")
	for _, f := range m.pendingMonitorFacts {
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	if m.monitorFactsDropped > 0 {
		fmt.Fprintf(&sb, "(+%d earlier completions suppressed)\n", m.monitorFactsDropped)
	}
	sb.WriteString("</" + monitorCompletionMarker + ">")
	block := sb.String()

	// 消费即清（保证"恰好一次"）。
	m.pendingMonitorFacts = nil
	m.monitorFactsDropped = 0

	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Role != "user" {
			continue
		}
		switch c := entries[i].Content.(type) {
		case string:
			entries[i].Content = c + "\n\n" + block
		case []interface{}:
			entries[i].Content = append(c, map[string]interface{}{"type": "text", "text": block})
		default:
			entries[i].Content = fmt.Sprintf("%v\n\n%s", c, block)
		}
		return entries
	}
	return append(entries, ai.MessageEntry{Role: "user", Content: block})
}
