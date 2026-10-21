// deltaF_probe_readback_test.go — delta F · T1 守卫的**判别力探针**。
//
// 归属：delta F。本文件**不测新功能**，只回答一个问题：
//
//	T1（TestDeltaF_T1_AuditIsNotReadBackAsAuthorization）是**真守卫**还是**假绿**？
//
// 项目铁律（`production-path-tdd`）：守卫测试必须证明可判别 —— 注入一个反例要能
// 把它打红。T1 的判据是「deny 之后同工具同入参**仍需确认**」。若这个判据对
// 「回读」完全不敏感（例如 Check 根本不看 memory），那么将来有人把审计回读成授权时
// T1 依然全绿 ⇒ 守卫形同虚设。
//
// 本探针的做法：在同一条生产路径（Model.Update → 确认 → 落账）跑完后，
// **手工注入"回读"这一反例**（把刚 deny 的这条记进 Policy 的会话记忆 —— 正是一个
// "贴心的错误改动"会做的事），然后断言 T1 的判据**必须翻红**。
//
// - 若判据翻红 ⇒ T1 有判别力（本探针通过）。
// - 若判据不翻红 ⇒ T1 是假绿（本探针失败），必须重写 T1 的判据。
//
// ⚠️ 为什么用"手工注入"而不是改生产码：生产码注入需要 overlay 改盘，属一次性实验；
// 本文件的注入发生在**测试进程内**，可长期随套件运行。两者互补 —— overlay 版证明
// "改错了生产码 T1 会红"，本版证明"判据本身对回读敏感"。overlay 版的执行记录见
// 交接文档 §4.3。
package tui

import "testing"

// TestDeltaF_Probe_ReadbackTurnsT1Red 证明 T1 的判据对「回读成授权」敏感。
func TestDeltaF_Probe_ReadbackTurnsT1Red(t *testing.T) {
	const tool = "bash"
	const input = `{"command":"ls"}`

	m, _, home := deltaFSubagentPending(t, tool, input)

	updated, _ := m.Update(deltaFKey("n")) // 明确拒绝
	m2 := r3AsModel(t, updated)

	// 前置：与 T1 同一条件（确实落了 1 条**决策**留痕）
	if got := len(deltaFDecisionEntries(t, home)); got != 1 {
		t.Fatalf("前置失败：应留痕 1 条（决策侧），实际 %d 条", got)
	}

	pol := m2.toolService.Policy()
	if pol == nil {
		t.Fatal("前置失败：Policy 为 nil，无法注入反例")
	}

	// ---- 步骤 1：确认基线 —— 未注入时，T1 的判据成立（仍需确认）----
	if allowed, needConfirm := pol.Check(tool, input); !needConfirm {
		t.Fatalf("基线不成立：当前 Check(%q) 已返回 needConfirm=false（allowed=%v）—— "+
			"说明 Policy 本就放行该调用，T1 的判据在本环境下无意义", tool, allowed)
	}

	// ---- 步骤 2：注入反例 —— 把这条 deny 的审计"回读"成授权 ----
	// 这正是"留痕退化为持久授权"的技术形态：审计文件被当成授权来源。
	pol.Remember(tool, input, true)

	// ---- 步骤 3：T1 的判据必须翻红 ----
	allowed, needConfirm := pol.Check(tool, input)
	if needConfirm {
		t.Fatalf("探针失效（T1 是假绿）：注入回读后 Check(%q) 仍返回 needConfirm=true（allowed=%v）⇒ "+
			"T1 的判据觉不出回读，无法拦住「把留痕当授权」的回归，必须重写 T1", tool, allowed)
	}
	t.Logf("判别力确认：注入回读后 T1 判据翻转为 needConfirm=false（allowed=%v）⇒ T1 能拦住该回归", allowed)
}
