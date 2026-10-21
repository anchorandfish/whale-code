package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/session"
)

// ── 埋点判据 ───────────────────────────────────────────
//
// 本文件证明三件事，缺一条埋点就不可信：
// 1. 阈值语义**可判别**（快静默 / 慢上报）—— 不是恒绿装饰；
// 2. 埋点**真的接在生产路径上**（buildRequest 各段、Update 总闸）——
// R1 生产接线断言，防止"加了埋点但没接线"变成死代码；
// 3. 埋点能测出**成本随内容量增长**（判据 4 附病历：最初"渲染 O(条数)"的
// 假设已被实测证伪，诊断方向据此修正到 buildRequest 的扫盘）。

type perfEvent struct {
	name   string
	dur    time.Duration
	detail string
}

// capturePerf 替换 perfEmitter 以捕获上报事件，返回读取函数。
func capturePerf(t *testing.T) func() []perfEvent {
	t.Helper()
	var mu sync.Mutex
	var got []perfEvent
	prev := perfEmitter
	perfEmitter = func(name string, d time.Duration, detail string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, perfEvent{name: name, dur: d, detail: detail})
	}
	t.Cleanup(func() { perfEmitter = prev })
	return func() []perfEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]perfEvent, len(got))
		copy(out, got)
		return out
	}
}

// setPerfThreshold 覆盖上报阈值（t.Cleanup 自动恢复）。
// 0 ⇒ 全上报（用于证明接线完整）；极大 ⇒ 全静默（用于证明快路径不吵）。
func setPerfThreshold(t *testing.T, d time.Duration) {
	t.Helper()
	prev := perfProbeThreshold
	perfProbeThreshold = d
	t.Cleanup(func() { perfProbeThreshold = prev })
}

func perfNames(events []perfEvent) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.name)
	}
	return names
}

// ── 判据 1：阈值语义可判别 ────────────────────────────────────────────────

func TestPerfReportThresholdIsDiscriminating(t *testing.T) {
	events := capturePerf(t)
	setPerfThreshold(t, 50*time.Millisecond)

	perfReport("fast", time.Now(), "")                                            // < 阈值 ⇒ 必须静默
	perfReport("slow", time.Now().Add(-100*time.Millisecond), "")                 // > 阈值 ⇒ 必须上报
	perfReport("exactly_at_first_tick", time.Now().Add(-50*time.Millisecond), "") // == 阈值 ⇒ 上报

	got := events()
	if len(got) != 2 {
		t.Fatalf("阈值语义失效：期望恰 2 条上报（slow + 等于阈值），实得 %v", perfNames(got))
	}
	if got[0].name != "slow" {
		t.Fatalf("首条应是 slow，实得 %q", got[0].name)
	}
}

// ── 判据 2 + 3：生产接线 + 快路径静默（配对，缺一则不可判别） ────────────

// TestPerfProbeWiredIntoBuildRequest 是 R1 生产接线断言：
// 阈值置 0 ⇒ 真实 buildRequest 的**每一段**都必须上报。
// 少一段 = 那段埋点没接线（或被人删了）⇒ 立刻红。
func TestPerfProbeWiredIntoBuildRequest(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.CurrentSession().SetMessages([]session.Message{session.UserText("hello")})

	events := capturePerf(t)
	setPerfThreshold(t, 0)

	_ = m.buildRequest()

	want := []string{
		"buildRequest.total",
		"buildRequest.systemPrompt",
		"buildRequest.renderedPromptHash",
		"buildRequest.messages",
		"buildRequest.toolDefs",
		"buildRequest.snapshot",
	}
	seen := map[string]bool{}
	for _, e := range events() {
		seen[e.name] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("埋点未接在生产路径上：buildRequest 缺段 %q（实得 %v）", w, perfNames(events()))
		}
	}
}

// TestPerfProbeSilentOnRealBuildRequest 是判据 2 的配对半边：
// 同一入口、阈值极高 ⇒ 真实（快）路径必须**零上报**。
// 两条一起才证明判据可判别 —— 只有"全上报"会掩盖"恒上报"的假绿。
func TestPerfProbeSilentOnRealBuildRequest(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.CurrentSession().SetMessages([]session.Message{session.UserText("hello")})

	events := capturePerf(t)
	setPerfThreshold(t, time.Hour)

	_ = m.buildRequest()

	if got := events(); len(got) != 0 {
		t.Fatalf("短会话 buildRequest 不该上报（阈值 1h），实得 %v", perfNames(got))
	}
}

// TestPerfProbeWiredIntoUpdate 证明主循环总闸接线：
// Update 有几十条提前 return，总闸必须覆盖全部 —— 这里驱动一条最普通的 resize。
func TestPerfProbeWiredIntoUpdate(t *testing.T) {
	m := newTUIRegressionModel(t)

	events := capturePerf(t)
	setPerfThreshold(t, 0)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if updated == nil {
		t.Fatal("Update 返回 nil model")
	}

	seen := false
	for _, e := range events() {
		if e.name == "update" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("Update 总闸未接线（实得 %v）", perfNames(events()))
	}
}

// ── 判据 4：埋点测出的成本关系 ───────────────────────────────────────────
//
// ★★ 病历（2026-10-07 实测，改本条前务必先读）：
//
//	本条原写作「渲染成本随**会话长度（条数）**增长 ⇒ 证实 O(会话长度)」。
//	实测**证伪**：600 条短消息 12.674µs · 5 条 10.268µs（仅 1.23×）。
//	根因：`displayMessages()` 返回的是**早已渲染好的字符串**（渲染发生在
//	sessionService.AddMessage 内），renderMessages 只做**拼接**，
//	而 600 次 WriteString 是纳秒级。
//
//	⇒ 本条不再断言条数关系（那会变成"假绿 + 日志说谎"），改断言**内容量**关系
// （成本真正由字节数驱动：ansi.Strip 是逐字符正则）。
//	⇒ 同时把"卡"的嫌疑从「渲染 O(条数)」移回 **buildRequest 的扫盘**
// （见 config/magic_docs_cache_test.go）。诊断方向据此修正。

const perfFiller = "这是一段用于测量渲染成本的填充文本，包含中英文 mixed content 与标点。"

// perfPayload 造一个约 bytes 字节的消息体。
func perfPayload(bytes int) string {
	unit := "填充 mixed content 12345 "
	n := bytes/len(unit) + 1
	return strings.Repeat(unit, n)
}

// measureAddMessageRender 在真实 Model 上量一次 addMessageFrom 的渲染耗时。
func measureAddMessageRender(t *testing.T, msgCount, payloadBytes int) time.Duration {
	t.Helper()
	m := newTUIRegressionModel(t)
	// 先给窗口尺寸，让 wrapViewportContent 这条硬折行路径也参与（否则测的只是拼接）。
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	payload := perfPayload(payloadBytes)
	msgs := make([]session.Message, 0, msgCount)
	for i := 0; i < msgCount; i++ {
		msgs = append(msgs, session.UserText(fmt.Sprintf("%s%s #%d", perfFiller, payload, i)))
	}
	m.sessionService.CurrentSession().SetMessages(msgs)

	read := capturePerf(t)
	setPerfThreshold(t, 0) // 全上报 ⇒ 拿到真实耗时

	m.addMessageFrom(AuthorTurn, "tail")

	for _, e := range read() {
		if e.name == "addMessageFrom.render" {
			return e.dur
		}
	}
	t.Fatal("未捕获 addMessageFrom.render —— 埋点未接线（判据本身失效）")
	return 0
}

// renderCostReps 每种 payload 的独立采样次数（交替次序 + 取 min，见下）。
const renderCostReps = 9

// measurePayloadPair 交替测量两种 payload，各自返回**多次采样的最小值**。
//
// ★ 为什么是"交替次序 + 取 min"（2026-10-09 实治病历）：
// 本条原先每种 payload 只测一次就比大小。实测暴露两种独立失真：
//
//	① 噪声盖过信号：同一 payload 的 600 条渲染在 11µs ~ 74µs 间乱跳，
//
// 「60B/条 > 4KB/条」的**伪反向**在 20 次里出现 4~8 次（约 35~40% 假红）。
// ⇒ 取 min（最佳情形估计）滤掉被调度 / GC 打断的脏样本。
//
//	② 次序偏置：先小后大时，**第二次**（大）即使在 payload 相同时也系统性偏慢
//
// （变异实验：令 perfPayload 忽略 bytes，两种 payload 完全相同，仍 8/8 全绿
// ⇒ 该 PASS 与内容量无关，是次序造成的假绿）。⇒ 单靠 min 不够。
// ⇒ 奇数轮调换测量次序，使两种 payload 各占一半的"第一 / 第二"位置，
// 抵消随测试进程推进的单调漂移。
//
// ★ A/B 归因（防把"既有 flaky"算成"本次引入"）：批 C 前的基线 worktree 跑同样
// 20 次，7 次伪反向 ⇒ 与本批改动无关，是**测法本身**的缺陷。
func measurePayloadPair(t *testing.T) (small, big time.Duration) {
	t.Helper()
	for i := 0; i < renderCostReps; i++ {
		var dS, dB time.Duration
		if i%2 == 0 {
			dS = measureAddMessageRender(t, 600, 60)
			dB = measureAddMessageRender(t, 600, 4000)
		} else {
			dB = measureAddMessageRender(t, 600, 4000)
			dS = measureAddMessageRender(t, 600, 60)
		}
		if i == 0 || dS < small {
			small = dS
		}
		if i == 0 || dB < big {
			big = dB
		}
	}
	return small, big
}

func TestRenderCostGrowsWithPayloadSize(t *testing.T) {
	if raceEnabled {
		// -race 会给每次内存访问插桩，插桩开销随分配/拷贝量非线性增长，
		// 完全盖过被测渲染路径本身 ⇒ 在 -race 下比较两个耗时没有判别力。
		// 本探针只在无 -race 的常规测试里判别（CI 另有非 race 的 `go test ./...`）。
		t.Skip("skip under -race: 插桩会扭曲渲染耗时对比")
	}
	smallPayload, bigPayload := measurePayloadPair(t)

	t.Logf("渲染成本实测（同为 600 条，交替次序各取 %d 次 min）：60B/条=%v · 4KB/条=%v",
		renderCostReps, smallPayload, bigPayload)

	if bigPayload <= smallPayload {
		t.Fatalf("内容量放大 ~66× 后渲染成本未增长 ⇒ 埋点未测到预期成本（%v vs %v）",
			smallPayload, bigPayload)
	}
}
