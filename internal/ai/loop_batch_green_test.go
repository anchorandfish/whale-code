package ai

import (
	"strings"
	"testing"
)

// Fix 红测（方案1：批/轮语义）—— 修复目标是消除"同批并列委派被误判为循环"。
//
// 背景：用户 TUI 实测"并发 6 个 explore 读 README"时，LoopDetector 把同一条
// assistant 消息里的 6 个并列 Agent tool_use 当成 6 轮失控重复，连续弹出
// "循环 3/4/5/6"。根因：逐条喂 CheckLoop 只认指纹连续、无批/轮语义。
//
// 这些测试模拟修复后 tool_exec.go 的真实调用形态：一次喂"一轮"的全部结果
// （CheckLoopBatch）。测的是 production 对象 LoopDetector（NewLoopDetector 真实构造，
// 无 mock 无置空）。当前代码无 CheckLoopBatch 方法 → 编译级 RED。

// 场景 A · 同批并列（合法）：一轮 6 个同指纹 Agent 委派，必须 0 警告。
func TestFix_LoopDetector_SameBatchParallel_NoWarn(t *testing.T) {
	d := NewLoopDetector(0, 0, 0) // defaults 3/5/5
	calls := make([]Call, 6)
	for i := range calls {
		calls[i] = Call{ToolName: "Agent", Input: `{"prompt":"read README first line","agent_type":"explore"}`, Success: true}
	}
	warns := d.CheckLoopBatch(calls)
	if len(warns) != 0 {
		t.Fatalf("GAP-UNEXPECTED: same-turn parallel batch must NOT warn, got %d warnings: %v", len(warns), warns)
	}
}

// 场景 B · 跨轮失控（真循环）：3 个不同批次各自重复同一操作 → 第 3 批必须报循环。
func TestFix_LoopDetector_CrossBatchRunaway_StillWarns(t *testing.T) {
	d := NewLoopDetector(0, 0, 0)
	one := []Call{{ToolName: "Agent", Input: `{"prompt":"read README first line"}`, Success: true}}
	// 批 1、批 2：各 1 次，不触发
	for batch := 1; batch <= 2; batch++ {
		if w := d.CheckLoopBatch(one); len(w) != 0 {
			t.Fatalf("batch %d should not warn yet: %v", batch, w)
		}
	}
	// 批 3：跨 3 批连续同操作 → 触发
	warns := d.CheckLoopBatch(one)
	if len(warns) != 1 {
		t.Fatalf("batch 3 (runaway) must warn exactly once, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0], "循环行为") {
		t.Fatalf("warning should contain 循环行为: %q", warns[0])
	}
}

// 场景 C · 批内多种不同操作（合法）：一轮里并行读不同文件，0 警告且不互相污染。
func TestFix_LoopDetector_BatchDistinctOps_NoWarn(t *testing.T) {
	d := NewLoopDetector(0, 0, 0)
	calls := []Call{
		{ToolName: "Agent", Input: `{"prompt":"read A.go"}`, Success: true},
		{ToolName: "Agent", Input: `{"prompt":"read B.go"}`, Success: true},
		{ToolName: "Agent", Input: `{"prompt":"read C.go"}`, Success: true},
	}
	if w := d.CheckLoopBatch(calls); len(w) != 0 {
		t.Fatalf("distinct parallel ops must not warn: %v", w)
	}
	// 同批去重后只剩 1 条该指纹的连续记录，后续再来一轮不同操作也不应立刻触发
	if w := d.CheckLoopBatch([]Call{{ToolName: "Agent", Input: `{"prompt":"read D.go"}`, Success: true}}); len(w) != 0 {
		t.Fatalf("next distinct batch must not warn: %v", w)
	}
}
