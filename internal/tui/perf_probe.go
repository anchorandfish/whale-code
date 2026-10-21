package tui

import (
	"fmt"
	"time"

	whalelog "github.com/anchorandfish/whale-code/internal/log"
)

// ── 主循环耗时埋点（· 2026-10-07） ──────────────────────────────────
//
// 本文件是「主循环不变量」在**测量侧**的载体。不变量本身：
//
//	主循环（Update / View）只做「把事件折叠成新状态」，
//	代价必须 O(事件) —— 与会话长度、工作区规模无关。
//
// 为什么需要埋点：在这条不变量被写下之前，「卡」只能靠读代码推断。
// 本仓此前**没有任何** Update/View 耗时测量（全 tui 无一处对主循环计时），
// 于是「发送段慢」与「收尾段慢」在日志里完全无法区分。
// 埋点把「哪一段慢」变成可观测事实 —— 它同时是后续修复的**判据来源**：
// 没有测量，修复就无法自证「真的不卡了」。
//
// 纪律：埋点**只上报、不改行为**。任何一段的上报都必须具备判别性
// （见 perf_probe_test.go：慢路径必上报 / 快路径必静默）。

// perfProbeThreshold 是上报阈值。
//
// 依据：bubbletea 目标帧率 120fps（main.go `WithFPS(120)`）⇒ 一帧 ≈ 8.3ms。
// 取 2 帧（16ms）＝ 用户可感知的掉帧；低于它的段不记，避免日志噪声淹没真问题。
//
// ★ 测试可将其置 0 ⇒ **所有被接线的段都会上报**，用于断言
// 「某段确实接在生产路径上」（R1 生产接线断言，防埋点成死代码）。
var perfProbeThreshold = 16 * time.Millisecond

// perfEmitter 是埋点输出端。生产写 whale.log；测试可替换以捕获事件。
//
// 单独抽出成变量（而非直接调用 logger）是为了让判据能**观测上报本身**，
// 而不是去解析日志文件 —— 后者在测试环境下没有 sink（见 R6b）。
var perfEmitter = func(name string, d time.Duration, detail string) {
	msg := fmt.Sprintf("[perf] %s %s", name, d.Round(time.Millisecond))
	if detail != "" {
		msg += " " + detail
	}
	whalelog.Info(msg)
}

// perfReport 上报一段耗时（自 start 起）。未达阈值 ⇒ 静默。
// 返回实测耗时，便于调用方继续使用（例如写进 Msg 或统计）。
//
// detail 只在**决定上报后**才需要使用；调用方若在热路径上构造 detail，
// 请用 perfSpan（延迟到收尾时才求值）。
func perfReport(name string, start time.Time, detail string) time.Duration {
	d := time.Since(start)
	if perfProbeThreshold > 0 && d < perfProbeThreshold {
		return d
	}
	perfEmitter(name, d, detail)
	return d
}

// perfSpan 返回一个收尾函数，用法：
//
//	defer perfSpan("buildRequest.total")()
//
// 收尾时按阈值决定是否上报（快路径零开销，只做一次 time.Since）。
func perfSpan(name string) func() {
	start := time.Now()
	return func() { perfReport(name, start, "") }
}
