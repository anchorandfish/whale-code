package approval

import (
	"context"
	"testing"
)

// 阶段 3 · 同源点穷举（R3⑤）：审批缓存的「记住授权」键 + 危险判定。
//
// 生产装配：runner_core.go:576 policy = approval.PolicyOnRequest，
// runner_core.go:562 → Pipeline.SetApprovalManager。PermissionStage 在 needConfirm 时
// 调用 Manager.Check（manager.go:49），其缓存键为 sha256(**整段 input**)（manager.go:50,166），
// 危险判定为 isDangerous(toolName)（只按工具名）。
//
// 与 permission.Memory 是**同一语义的第二处实现**：
//   - Agent prompt 每次都不同 → 缓存键每次都不同 → 「始终允许」永远不命中，反复询问；
//   - Agent 按工具名判危险 → 只读子代理也被当危险工具（与权限层「只读不弹」不一致）。
//
// 两处都收敛到 permission 的同一真相源：缓存键用 permission.ApprovalKeyFor（Agent 归一化到
// agent_type），危险判定用 permission.IsDangerousToolFor（输入感知）。

func newAgentAskCounter() (*Manager, *int) {
	calls := 0
	ask := func(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
		calls++
		return DecisionAllow, "allow (test)"
	}
	return NewManager(PolicyOnRequest, ask), &calls
}

// 核心：可写子代理的「始终允许」应按 agent_type 命中缓存，不因 prompt 不同而反复询问。
// 改动前：两次 prompt 不同 → 缓存键不同 → askFn 被调用两次。
func TestManager_AgentAlwaysAllowKeyedByAgentType(t *testing.T) {
	m, calls := newAgentAskCounter()
	ctx := context.Background()

	d1, _, err := m.Check(ctx, "Agent", `{"prompt":"第一个任务","agent_type":"coder"}`)
	if err != nil || d1 != DecisionAllow {
		t.Fatalf("第一次 Agent(coder) 应放行：decision=%v err=%v", d1, err)
	}

	d2, reason, err := m.Check(ctx, "Agent", `{"prompt":"完全不同的第二个任务","agent_type":"coder"}`)
	if err != nil || d2 != DecisionAllow {
		t.Fatalf("第二次 Agent(coder) 应放行：decision=%v err=%v", d2, err)
	}
	if *calls != 1 {
		t.Errorf("同一 agent_type 的第二次应命中缓存、不再询问：askFn 调用次数=%d, want 1（reason=%q）", *calls, reason)
	}
}

// 隔离：不同 agent_type 不得共享授权。
func TestManager_AgentAlwaysAllowIsolatedByAgentType(t *testing.T) {
	m, calls := newAgentAskCounter()
	ctx := context.Background()

	if _, _, err := m.Check(ctx, "Agent", `{"prompt":"编码","agent_type":"coder"}`); err != nil {
		t.Fatalf("Agent(coder) err=%v", err)
	}
	if _, _, err := m.Check(ctx, "Agent", `{"prompt":"对抗验证","agent_type":"verification"}`); err != nil {
		t.Fatalf("Agent(verification) err=%v", err)
	}

	if *calls != 2 {
		t.Errorf("不同 agent_type 必须各自询问一次：askFn 调用次数=%d, want 2（coder 的授权不得外溢到 verification）", *calls)
	}
}

// 只读子代理不算危险工具：OnRequest 策略下直接放行、不询问（与权限层「只读不弹」一致）。
// 改动前：isDangerous 只看工具名 → Agent 恒为危险 → 询问一次。
func TestManager_ReadOnlyAgentNotDangerous(t *testing.T) {
	m, calls := newAgentAskCounter()
	ctx := context.Background()

	for _, at := range []string{"explore", "plan"} {
		d, reason, err := m.Check(ctx, "Agent", `{"prompt":"探索","agent_type":"`+at+`"}`)
		if err != nil || d != DecisionAllow {
			t.Fatalf("只读子代理 agent_type=%q 应直接放行：decision=%v err=%v", at, d, err)
		}
		_ = reason
	}
	if *calls != 0 {
		t.Errorf("只读子代理不应触发审批询问：askFn 调用次数=%d, want 0", *calls)
	}
}

// 回归保护：普通危险工具（bash）仍按工具名询问，行为不变。
func TestManager_DangerousToolStillAsks(t *testing.T) {
	m, calls := newAgentAskCounter()
	if _, _, err := m.Check(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`); err != nil {
		t.Fatalf("bash err=%v", err)
	}
	if *calls != 1 {
		t.Errorf("bash 仍应按危险工具询问一次：askFn 调用次数=%d, want 1", *calls)
	}
}
