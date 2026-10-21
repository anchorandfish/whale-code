package ai

import (
	"strings"
	"testing"
)

// 本文件是「循环告警去噪 + 文案与行为对齐」的判据（2026-09-30 · 真机 I-8.2）。
//
// 现场：同一条 `echo hi > out/i8_created.txt` 连发 4 次，TUI 刷出两条几乎相同的
// 告警（"已连续执行 3 次" / "已连续执行 4 次"），把窗口占满却没多给信息；
// 且文案问「是否中断？」—— 而消费点（tui/tool_exec.go）只 addMessage，
// **既不中断也不询问** ⇒ 文案承诺了一个不存在的交互。
//
// 修复后的语义：
// ① 同一串连续重复**只在首次跨阈报一次**（超阈不复读）；
// ② 被别的调用打断后重新累计到阈值会**再报**（那是新的、有信息量的重复）；
// ③ 文案给**真实可行**的动作（TUI 里 Esc / Ctrl+C 中断），不再问"是否中断？"。
//
// 分工声明：本判据只管**可见性**（软层）。强制停机不在这一层 —— 由
// agentguard P0-4 的「同一工具 + 同一参数反复调用」判据承担
// （internal/agentguard/repeated_calls_test.go）；S17 纵审 §6 的判定即
// "强制面由 agentguard 承担，loopDetector 的收益是让人看见正在发生的循环"。

// TestLoopAlertReportsOncePerRun ①：首次跨阈报一次，超阈不复读。
func TestLoopAlertReportsOncePerRun(t *testing.T) {
	d := NewLoopDetector(0, 0, 0) // 默认 3/5/5
	const input = `{"command":"echo hi > out/i8_created.txt"}`

	for i := 1; i <= 2; i++ {
		if w := d.CheckLoop("bash", input, true); w != "" {
			t.Fatalf("第 %d 次不应告警：%q", i, w)
		}
	}
	if w := d.CheckLoop("bash", input, true); w == "" {
		t.Fatal("第 3 次（首次跨阈）必须告警")
	}
	for i := 4; i <= 5; i++ {
		if w := d.CheckLoop("bash", input, true); w != "" {
			t.Fatalf("第 %d 次不得复读（首次跨阈已报过，复读只占窗口不多给信息）：%q", i, w)
		}
	}
}

// TestLoopAlertRepeatsAfterInterruption ② 不退化守卫（before/after 都必须绿）：
// 去噪**不得**退化成"整个请求只报一次" —— 被别的调用打断后重新累计到阈值，
// 那是新的一串重复，必须再次可见。
func TestLoopAlertRepeatsAfterInterruption(t *testing.T) {
	d := NewLoopDetector(0, 0, 0)
	const input = `{"command":"echo hi > out/x.txt"}`

	for i := 0; i < 3; i++ {
		d.CheckLoop("bash", input, true)
	}
	// 打断：换一条不同命令
	if w := d.CheckLoop("bash", `{"command":"ls"}`, true); w != "" {
		t.Fatalf("不同命令不应告警：%q", w)
	}
	for i := 1; i <= 2; i++ {
		if w := d.CheckLoop("bash", input, true); w != "" {
			t.Fatalf("打断后第 %d 次不应告警（尚未重新到阈值）：%q", i, w)
		}
	}
	if w := d.CheckLoop("bash", input, true); w == "" {
		t.Fatal("打断后重新累计到阈值应再次告警 —— 否则同一请求内的第二轮循环无人看见")
	}
}

// TestLoopAlertCopyDoesNotPromisePhantomInteraction ③ 文案判据：
// 不得问"是否中断？"（代码不会中断），且必须给出真实可行的动作。
//
// 该文案只在 TUI 面出现：plain/bridge 零构造 loopDetector（既有项），
// 故"按 Esc 中断"对**实际会看到这句话的入口**是准确指引。
func TestLoopAlertCopyDoesNotPromisePhantomInteraction(t *testing.T) {
	d := NewLoopDetector(0, 0, 0)
	const input = `{"path":"app.go"}`

	var last string
	for i := 0; i < 3; i++ {
		last = d.CheckLoop("read_file", input, true)
	}
	if last == "" {
		t.Fatal("第 3 次（首次跨阈）必须告警")
	}
	if strings.Contains(last, "是否中断") {
		t.Fatalf("文案不得问「是否中断？」—— 消费点只 addMessage，不会中断，这是承诺一个不存在的交互：%q", last)
	}
	if !strings.Contains(last, "Esc") {
		t.Fatalf("文案必须给出真实可行的中断动作（TUI 里是 Esc / Ctrl+C）：%q", last)
	}
}
