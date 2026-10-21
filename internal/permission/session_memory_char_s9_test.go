package permission

import (
	"path/filepath"
	"testing"
)

// ⚠️ 本文件是 **S9 Fix 的 overlay 版**（对应磁盘 internal/permission/session_memory_char_s9_test.go）。
//
// 与磁盘原版的差异（**delta E 之后，只剩注释性修正**）：
//   - TestChar_S9_WholeInputKeyNoDedupForBash → 改名 TestChar_S9_DangerousBashNotDeduped：
//     断言（rm 不同目标不命中）在 Fix 前后**都成立**，保留为 fail-closed 护栏；
//     但原注释宣称"归一化尚未落地"在 Fix 后**已是错的**，故改名 + 更正注释。
//     ⇒ 这是**纯文档性**替换。若想减少 churn，跳过本文件替换也不会红（见 README §3 备注）。
//   - TestChar_S9_SessionMemoryNotPersisted → **原样恢复**。
//     Round2 的 G1 曾删除它（当时"落盘"是正确行为）；delta E 撤销 G1 后，
//     它重新成为**正确行为的规格**，必须留作护栏（防 G1 被误接回）。
//   - TestChar_S9_AgentKeyNormalizedByAgentType → 原样保留（防 Fix 误改回整段键）。

// S9 characterization（HG-A2 护栏）：**危险类**命令仍按完整命令记忆 → 不同目标不命中。
// 只读类的归一化见 session_memory_fix_s9_test.go。
func TestChar_S9_DangerousBashNotDeduped(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.Remember("bash", `{"command":"rm /tmp/a"}`, true)

	if p.Has("bash", `{"command":"rm /tmp/b"}`) {
		t.Fatal("GOT: 不同目标被去重命中; EXPECT: destructive 类整段作键 → 不命中（fail-closed）")
	}
	if !p.Has("bash", `{"command":"rm /tmp/a"}`) {
		t.Fatal("EXPECT: 完全相同 input 命中")
	}
}

// S9 characterization：Agent 已做**窄归一化**（approvalKeyFor → agent_type）。
// （由并行会话「阶段 3」落地：permission.go approvalKeyFor / canonicalAgentType。）
// 钉死此现状，避免 Fix 误改回整段键。
func TestChar_S9_AgentKeyNormalizedByAgentType(t *testing.T) {
	p := NewPolicy(LevelWorkspaceWrite)
	p.Remember("Agent", `{"prompt":"任务一","agent_type":"coder"}`, true)

	if !p.Has("Agent", `{"prompt":"完全不同的任务二","agent_type":"coder"}`) {
		t.Fatal("EXPECT: Agent 记忆按 agent_type 归一化 → 命中")
	}
	if p.Has("Agent", `{"prompt":"任务一","agent_type":"explore"}`) {
		t.Fatal("EXPECT: 不同 agent_type 不得共享记忆")
	}
}

// S9 characterization（delta E · 严格会话级）：会话记忆**不落盘** → 重启后为空。
//
// 本用例复刻**不经过审批入口**的路径（直接 Remember 后新建 Policy 读取），
// 与 session_memory_fix_s9_test.go 的 TestFix_S9_ApprovalDoesNotSurviveRestart 互补：
// 那条走审批入口断言"无落盘副作用"，本条断言"读路径也拿不到"。
//
// 若 G1（Remember → SaveMemory）被重新接线，本用例变红。
func TestChar_S9_SessionMemoryNotPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")

	p1 := NewPolicy(LevelWorkspaceWrite)
	p1.SetMemoryStorePath(path)
	p1.Remember("bash", `{"command":"ls -la"}`, true)

	p2 := NewPolicy(LevelWorkspaceWrite)
	p2.SetMemoryStorePath(path)
	if err := p2.LoadMemory(); err != nil {
		t.Logf("LoadMemory 报错（文件不存在，符合未落盘预期）: %v", err)
	}
	if p2.Has("bash", `{"command":"ls -la"}`) {
		t.Fatal("GOT: 记忆跨重启存活; EXPECT: 严格会话级 → 不落盘（delta E）")
	}
}
