package agentguard

import (
	"fmt"
	"strings"
	"testing"
)

// 本文件是 P0-4 空转信号的**第四个判据**（同一工具 + 同一参数反复调用）的判据集。
//
// 背景（2026-09-30 真机手测 I-8.2）：模型对 `echo hi > out/i8_created.txt`
// 连发 4 次，第 5 次才自己停下。该场景对既有三条空转判据**全部不可见**：
// - similarOutputs()：窗口内输出两两行级 Jaccard ≥ 0.9 才算，且**空输出直接跳过**；
// 模型每次的思考文本并不相同 ⇒ 不命中
// - rereadExceeded()：只按文件路径计数；bash 的 `command` 提不出路径 ⇒ readCounts 恒 0
// - oscillation：改动集合恒为空 ⇒ 组不出 A,B,A,B
// ⇒ "强制面已由 agentguard 承担"（2026-09-20 S17 纵审 §6）这一前提对
// **非读写工具**不成立：循环无人接住，只能等模型自己停。本判据补上这块盲区。
//
// 判据走生产对象（NewGuard / BeginRequest / AfterTurn / BeforeModelCall），
// 不置空依赖、不替换空实现、不构造前提。

const repeatedBashInput = `{"command":"echo hi > out/i8_created.txt"}`

// repeatedCallTurn 构造一轮观测：无文件读写、失败测试未知，输出文本逐轮不同
// （刻意避开 similarOutputs —— 本判据的红/绿只应取决于「重复调用」这一条）。
func repeatedCallTurn(i int, calls []ToolCallKey) TurnObservation {
	return TurnObservation{
		FailingTests: -1,
		Output:       fmt.Sprintf("phase %d: executing the requested step now", i*i+7),
		ToolCalls:    calls,
	}
}

// TestRepeatedToolCallsAreSpinning 主判据：同一条非只读调用累计 5 轮 ⇒ 判空转停机。
func TestRepeatedToolCallsAreSpinning(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	for i := 0; i < 5; i++ {
		g.AfterTurn(repeatedCallTurn(i, []ToolCallKey{{Tool: "bash", Input: repeatedBashInput}}))
	}

	d := g.BeforeModelCall()
	if d.Verdict != VerdictStop {
		t.Fatalf("同一条 bash 已累计执行 5 次（无文件读写、输出逐轮不同）⇒ 必须判空转停机；实际 verdict=%v reason=%q",
			d.Verdict, d.Reason)
	}
	if d.Report == nil {
		t.Fatal("空转停机必须附进度快照（用户要知道卡在哪）")
	}
	if !strings.Contains(d.Report.Cause, "空转") {
		t.Fatalf("停机原因须来自 P0-4 空转信号，实际 %q", d.Report.Cause)
	}
	if !strings.Contains(d.Report.StuckAt, "反复") {
		t.Fatalf("快照必须点明「反复执行同一工具」，实际 StuckAt=%q Tried=%v",
			d.Report.StuckAt, d.Report.Tried)
	}
}

// TestRepeatedToolCallsThresholdBoundary 阈值边界：4 次**不得**停机（阈值 5），
// 第 5 次才停 —— 防「阈值写错一位」的假绿（例如误用 > 或 >= 之外的比较）。
func TestRepeatedToolCallsThresholdBoundary(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	for i := 0; i < 4; i++ {
		g.AfterTurn(repeatedCallTurn(i, []ToolCallKey{{Tool: "bash", Input: repeatedBashInput}}))
	}
	if d := g.BeforeModelCall(); d.Verdict == VerdictStop {
		t.Fatalf("第 4 次相同调用不得停机（阈值 %d）；实际 %v（%s）",
			maxSameToolCalls, d.Verdict, d.Reason)
	}

	g.AfterTurn(repeatedCallTurn(4, []ToolCallKey{{Tool: "bash", Input: repeatedBashInput}}))
	if d := g.BeforeModelCall(); d.Verdict != VerdictStop {
		t.Fatalf("第 %d 次相同调用必须停机；实际 %v", maxSameToolCalls, d.Verdict)
	}
}

// TestSameTurnParallelCallsAreNotRepeated 反例守卫：**同一轮**内多个同参数并列调用
// 是合法并发，不得判重复。
//
// 这是 loop_detector.go CheckLoopBatch 文档记录过的真实误报形态（并发派多个
// 同参数 explore 子 agent 被判成"循环 3/4/5/6"）在 P0-4 上的同型风险：
// 若不去重，一轮 6 个并列调用会让计数直接 +6 而立刻超阈。⇒ Observe 必须**批内去重**。
func TestSameTurnParallelCallsAreNotRepeated(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	calls := make([]ToolCallKey, 0, 6)
	for i := 0; i < 6; i++ {
		calls = append(calls, ToolCallKey{
			Tool:  "Agent",
			Input: `{"prompt":"read README first line","agent_type":"explore"}`,
		})
	}
	g.AfterTurn(repeatedCallTurn(1, calls))

	if d := g.BeforeModelCall(); d.Verdict == VerdictStop {
		t.Fatalf("同批并列的同参数调用是合法并发，不得判空转；实际 %v（%s）", d.Verdict, d.Reason)
	}
}

// TestDistinctInputsAreNotRepeated 反例守卫：同工具但**参数不同**不得累计
// —— 否则"依次跑多个不同测试命令"会被误停。
func TestDistinctInputsAreNotRepeated(t *testing.T) {
	g := NewGuard(DefaultConfig())
	g.BeginRequest()

	for i := 0; i < 8; i++ {
		g.AfterTurn(repeatedCallTurn(i, []ToolCallKey{
			{Tool: "bash", Input: fmt.Sprintf(`{"command":"go test ./internal/pkg%d/"}`, i)},
		}))
	}

	if d := g.BeforeModelCall(); d.Verdict == VerdictStop {
		t.Fatalf("参数各不相同的调用不是重复，不得停机；实际 %v（%s）", d.Verdict, d.Reason)
	}
}
