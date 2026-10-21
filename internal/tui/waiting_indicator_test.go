package tui

// 现象②「提交 → 首 token 之间」等待期反馈 —— 生产渲染活线判据。
//
// 真机取证（2026-10-04，用户报告 Monitor 后台任务）：
// 用户按 Enter 提交后，界面进入一段「什么都没发生」的状态；
// 实测该段约 17s（请求已发出、服务端未回首 token），用户在此期间自己按了中断。
// ⇒ 用户无法判断「请求已经发出去了，只是在等」还是「压根没发出去 / 卡死了」。
//
// ⚠️ 证据更正（2026-10-04 复核，务必让读的人知道）：
// 先前回执写「界面没有任何反馈（既无 spinner 也无'已发送'）」是**过度断言**。
// 等待提示其实是存在的：stream.go 在「既无正文、也无思考内容」时渲染
// `AI is thinking...`（且 thinking_content_driven_test.go 有守卫断言它）。
//
// ⇒ ② 的真实缺陷不是「零反馈」，而是这行提示**两不**：
// (1) 不说请求已发出：只说"在想"，没说"已发送" ⇒ 无法区分「在等网关」与「没发出去」；
// (2) 不带耗时：17s 的等待与卡死在视觉上完全一样（spinner 会动，但不进时间）。
//
// 本判据钉住这两点：等待行必须 (a) 表明"已发送/在等响应" (b) 带**真实走动**的秒数。
//
// 生产接线（R1）：判据走的是**活线渲染器** Model.renderStreamContent()（stream.go），
// 与既有守卫 thinking_content_driven_test.go 用同一套造模法（newTUIRegressionModel
// + reassignServices + state=StateStreaming + activeCell=NewActiveCell()）。
//
// ★ 判别性说明（R4-7，如实标注）：
// 本批新增 Model 字段 waitingSince ⇒ before 态（字段不存在）表现为**编译失败**，
// 这是 Go 中"新增承载体"型改动的正常红，但它只证明"字段不存在"，
// 不证明"判据盯着文案"。故落地后另做**反例注入自检**：
// 把 waitingIndicator() 临时改回返回旧文案 ⇒ 下列判据必须转红（见落地回执）。

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// newWaitingModel 复刻 thinking_content_driven_test.go:27-38 的造模法：
// 生产契约 app.go:1156（viewport.Width = 终端宽 - 4，此处模拟终端 120）。
func newWaitingModel(t *testing.T) *Model {
	t.Helper()
	m := newTUIRegressionModel(t)
	m.reassignServices()
	m.uiService.Viewport().Width = 116
	m.state = StateStreaming
	// 与既有守卫一致：thinking 保持 nil（未配置），渲染必须内容驱动。
	m.sessionService.SetThinkingEnabled(nil)
	m.activeCell = NewActiveCell()
	return m
}

// TestWaitingIndicator_ShowsElapsedSeconds 是主判据。
//
// 等待 17s（真机实测的那个量级）时，等待行必须：
// - 表明请求**已发出**、当前在**等响应**（旧文案 "AI is thinking..." 两者都没说）；
// - 把**已等了多久**直接摆出来。
func TestWaitingIndicator_ShowsElapsedSeconds(t *testing.T) {
	m := newWaitingModel(t)
	m.waitingSince = time.Now().Add(-17 * time.Second)

	m.renderStreamContent()
	content := ansi.Strip(m.viewportContent)

	if !strings.Contains(content, "Sent, waiting for response") {
		t.Fatalf("等待行必须表明「已发送、在等响应」——否则用户无法区分"+
			"「请求已发出在等网关」与「压根没发出去」\n实际输出：\n%s", content)
	}
	if !strings.Contains(content, "(17s)") {
		t.Fatalf("等待行必须显示已等待时长（17s）——否则 17 秒的等待与卡死在视觉上完全一样\n"+
			"实际输出：\n%s", content)
	}
}

// TestWaitingIndicator_ElapsedTracksRealTime 证明秒数是**真实走动**的计时，
// 不是写死的常量文本（常量的话改改文案就能蒙混过关）。
func TestWaitingIndicator_ElapsedTracksRealTime(t *testing.T) {
	m := newWaitingModel(t)
	m.waitingSince = time.Now().Add(-1200 * time.Millisecond)

	m.renderStreamContent()
	content := ansi.Strip(m.viewportContent)

	if !strings.Contains(content, "(1s)") {
		t.Fatalf("已等待约 1.2s 时应显示 (1s)，说明时长取的是真实经过时间\n实际输出：\n%s", content)
	}
	if strings.Contains(content, "(17s)") {
		t.Fatalf("时长疑似常量：本例只等了 1.2s 却渲染出 (17s)\n实际输出：\n%s", content)
	}
}

// TestWaitingIndicator_BilingualShape 钉住文案**形态**契约。
//
// 与 bilingual_guard_test.go 同源（SSOT 在 internal/util/bilingual.go）：
// 含汉字的辅助行必须以 copyIndent 缩进，且**不得**与英文并列在同一行。
// 本条把它钉在新文案上，防止后续改文案时退回「英文 / 中文」行内并列。
func TestWaitingIndicator_BilingualShape(t *testing.T) {
	m := newWaitingModel(t)
	m.waitingSince = time.Now().Add(-17 * time.Second)

	got := m.waitingIndicator()
	t.Logf("渲染形态（| 标出行首）：\n%s", strings.ReplaceAll(got, "\n", "⏎\n"))

	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("双语形态应为两行（英文主 + 中文辅），实际 %d 行：\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "Sent, waiting for response") {
		t.Fatalf("第一行应为英文主行，实际 %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], copyIndent) {
		t.Fatalf("第二行应为以 copyIndent 开头的中文辅助行（禁止行内并列），实际 %q", lines[1])
	}
	if !hasHan(lines[1]) {
		t.Fatalf("中文辅助行应含汉字，实际 %q", lines[1])
	}
}

// TestWaitingIndicator_ZeroSinceDegradesToNoTimer 是**反例守卫**（防过度改动）。
//
// waitingSince 为零值（未打点，例如某些未走 startAIStream 的调用路径）时：
// - 不得 panic（零值 time.Time 直接做 Since 会得到"几千年"的荒谬数字）；
// - 仍应输出等待行，只是退化为**不带计时**的版本。
//
// 本条在 before / after **两态都应绿**（不具判别性，只防"打点漏了就炸/就没提示"）。
func TestWaitingIndicator_ZeroSinceDegradesToNoTimer(t *testing.T) {
	m := newWaitingModel(t)
	// 不打点：waitingSince 保持零值。

	m.renderStreamContent()
	content := ansi.Strip(m.viewportContent)

	if !strings.Contains(content, "Sent, waiting for response") {
		t.Fatalf("未打点时仍应输出等待行（退化为不带计时）\n实际输出：\n%s", content)
	}
	if strings.Contains(content, "(") && strings.Contains(content, "s)") {
		t.Fatalf("未打点时不得输出计时（零值 time.Time 做 Since 会得到荒谬数字）\n"+
			"实际输出：\n%s", content)
	}
}
