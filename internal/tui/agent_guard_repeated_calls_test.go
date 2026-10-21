package tui

// 本文件是 P0-4 第四判据（同一工具 + 同一参数反复调用）的**接线层判据**。
//
// agentguard 包内的判据（repeated_calls_test.go）证明的是"检测器本身能判"；
// 本文件证明的是"生产装配下真的接上了" —— 两条缺一：检测器对但没接线 = 假修复。
//
// 背景：2026-09-30 真机手测 I-8.2，`echo hi > out/i8_created.txt` 连发 4 次，
// 既有三条空转判据全不命中（bash 既不读文件、输出又为空）⇒ 强制面形同不存在。

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// TestPlainGuardStopsOnRepeatedNonReadOnlyCall 端到端：plain 生产入口
// guardAfterToolRound 连喂 5 轮同一条 bash（成功、输出为空）后，
// **下一次模型调用前**的闸门必须停机。
//
// 停机点刻意选在 guardBeforeModelCall：空转是"前置停机"（P0-4 在
// BeforeModelCall 里判），不是工具轮熔断 ⇒ 本用例同时钉住"停在哪里"，
// 防止有人把判据挪进 AfterToolResults 而改变了治理时序。
func TestPlainGuardStopsOnRepeatedNonReadOnlyCall(t *testing.T) {
	t.Setenv("HOME", newIsolatedTestHome(t))
	r := newTestPlainRunner()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r.Store = store
	r.guard = agentguard.NewGuard(agentguard.DefaultConfig())

	const input = `{"command":"echo hi > out/i8_created.txt"}`
	call := session.ContentBlock{Type: "tool_use", ID: "call_i8", Name: "bash", Input: input}
	res := tools.Result{ID: "call_i8", Name: "bash", Output: "", IsError: false}

	for i := 0; i < 5; i++ {
		if stopped := r.guardAfterToolRound([]session.ContentBlock{call}, []tools.Result{res}); stopped {
			t.Fatalf("第 %d 轮：空转停机应发生在下一次模型调用前的闸门（BeforeModelCall），"+
				"而不是工具轮全失败熔断", i+1)
		}
	}
	if !r.guardBeforeModelCall() {
		t.Fatal("同一条 bash 已累计 5 轮（无文件读写、结果输出为空）⇒ 模型调用前闸门必须停机；" +
			"否则 I-8.2 那类循环仍然无人接住")
	}
}

// TestTurnObservationSeparatesReadOnlyFromRepeatTracking 判据：turnObservation 把
// 「只读面」与「重复调用面」分到两个字段，且两面的入选条件不同 ——
//
//   - FilesRead：只读工具 + 成功（本改动不得回退 2026-09-22「只读搜索也算进展」修复）
//   - ToolCalls：**非**只读工具 + 成功（只读面的重复归 rereadExceeded 按路径负责）
//
// 失败调用两面都不进（G9-③：无配对结果 / 结果失败 ⇒ 本轮没有产生进展）。
func TestTurnObservationSeparatesReadOnlyFromRepeatTracking(t *testing.T) {
	const bashInput = `{"command":"echo hi > out/x.txt"}`
	calls := []session.ContentBlock{
		{Type: "tool_use", ID: "a", Name: "bash", Input: bashInput},
		{Type: "tool_use", ID: "b", Name: "read_file", Input: `{"path":"README.md"}`},
		{Type: "tool_use", ID: "c", Name: "grep_search", Input: `{"pattern":"foo"}`},
		{Type: "tool_use", ID: "d", Name: "bash", Input: `{"command":"exit 1"}`},
	}
	results := []tools.Result{
		{ID: "a", Name: "bash", Output: "", IsError: false},
		{ID: "b", Name: "read_file", Output: "content", IsError: false},
		{ID: "c", Name: "grep_search", Output: "match", IsError: false},
		{ID: "d", Name: "bash", Output: "exit status 1", IsError: true},
	}

	obs := turnObservation(calls, results, "")

	if len(obs.ToolCalls) != 1 ||
		obs.ToolCalls[0].Tool != "bash" ||
		obs.ToolCalls[0].Input != bashInput {
		t.Fatalf("重复调用面只应记录「非只读 + 成功」的那 1 条（bash a）；实际 %+v", obs.ToolCalls)
	}
	if len(obs.FilesRead) != 2 {
		t.Fatalf("只读工具（read_file + grep_search）必须仍计入 FilesRead；实际 %v", obs.FilesRead)
	}
	if len(obs.FilesChanged) != 0 {
		t.Fatalf("失败调用不得计入进展；实际 FilesChanged=%v", obs.FilesChanged)
	}
}
