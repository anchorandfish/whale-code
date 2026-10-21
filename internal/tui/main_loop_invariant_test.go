package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ── · 主循环不变量守卫门 ───────────────────────────────
//
// 不变量（本批修复要钉死的东西）：
//
//	**Update 只做「把事件折叠成新状态」，代价必须 O(事件) ——
//	不得在返回前完成请求构造、磁盘 I/O、LLM 调用这类重活。**
//
// 为什么这道门必须存在：
// `stream.go` 里 `append(cmds, m.startAIStream(), …)` 会**同步求值函数体**
// （Go 先算参数、再收集 Cmd），于是 `buildRequest()` 压在主循环上。
// 光把它挪走不够 —— 没有门，下一个人会顺手再加回来（这就是"缝补"与"根治"的分界）。
//
// 判别方式：复用埋点通道（`perf_probe.go`），阈值置 0 ⇒ **所有被接线的段都会上报**。
// - 走**生产入口** `Update(tea.KeyMsg{Enter})`（Enter = 发送，见 `app.go` 的 `case "enter"`）。
// - 请求构造若仍同步 ⇒ `buildRequest.*` 段会在 Update 返回前上报 ⇒ 本门红。
// - 请求构造已移入 Cmd ⇒ Update 返回前不上报（Cmd 由 bubbletea 在返回后才执行）⇒ 绿。
//
// ★ 抗"假绿":同时断言 `startAIStream.total` **必须**上报 —— 若发送路径压根没跑，
// 那就不是"没同步构造"，而是"什么都没发生"，本门会当场点破（否则是空转发绿）。

// mainLoopAllowedSegments 是**允许在发送那一刻同步上报**的段的白名单。
//
// 每一条都必须能自证「O(1) 或与输入无关的常数代价」，否则不变量就被违反：
// - `update` —— 总闸自身（量的是 Update 全程，含本白名单内的段）；
// - `startAIStream.total` —— 同步体只剩「防护闸门判定 / ctx 装配 / 快照局部化」，O(1)；
// - `addMessageFrom.render` —— 把刚加入的那条消息渲染进转录（实测 600 条 × 4KB 仅 ~27µs，
// 且 `renderStreamContent` 另有增量缓存）。★ 这是**已知被容忍**的成本，非零；
// 若将来它涨到毫秒级，应把它也移出主循环，而不是把本白名单放宽。
//
// ★ 白名单以外的任何段同步上报 = 有人又把重活（I/O / LLM / 全量遍历）接回 Update ⇒ 本门红。
var mainLoopAllowedSegments = map[string]bool{
	"update":                true,
	"startAIStream.total":   true,
	"addMessageFrom.render": true,
}

func TestMainLoopDoesNotConstructRequestSynchronously(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.uiService.Textarea().SetValue("hello ")

	events := capturePerf(t)
	setPerfThreshold(t, 0) // 0 ⇒ 全上报

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	got := perfNames(events())
	t.Logf("发送时同步上报的段：%v", got)

	// ① 抗假绿：发送路径必须真的执行了（否则本门无判别力）。
	sawSendPath := false
	for _, n := range got {
		if n == "startAIStream.total" {
			sawSendPath = true
		}
	}
	if !sawSendPath {
		t.Fatalf("发送路径未执行（未见 startAIStream.total）⇒ 本门失去判别力；实得 %v", got)
	}

	// ② 不变量：白名单以外的段不得在 Update 返回前上报（= 主循环内不得做重活）。
	var offending []string
	for _, n := range got {
		if !mainLoopAllowedSegments[n] {
			offending = append(offending, n)
		}
	}
	if len(offending) > 0 {
		t.Fatalf("主循环内同步执行了重活（段 %v）⇒ Update 不是 O(事件)，界面会被阻塞；实得 %v",
			offending, got)
	}

	if cmd == nil {
		t.Fatal("发送应返回非 nil Cmd（否则 AI 流无法启动）")
	}
}
