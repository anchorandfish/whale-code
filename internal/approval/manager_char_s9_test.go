package approval

import (
	"context"
	"testing"
)

// S9 characterization：证明 **approval.Manager.cache 在当前生产 Tool 审批路径上是惰性的**。
//
// 生产装配（runner_core.go:582）注入的 AskFn 是 newApprovalAskFn（runner_core.go:600-604），
// 它**恒返回 DecisionAsk**（把交互委托给 Pipeline 的 NeedConfirm 暂停流程）。
// 而 Manager.Check 只在 `decision == DecisionAllow` 时写缓存（manager.go:71-73）。
// 于是：生产路径下缓存**永不写入** → 永不命中 → 不构成会话记忆。
//
// 本用例用「与生产同形」的 AskFn（恒 DecisionAsk）钉死该现状。
func TestChar_S9_ManagerCacheInertUnderProductionAskFn(t *testing.T) {
	calls := 0
	// 与 internal/tui/runner_core.go:600-604 同形：恒 DecisionAsk
	prodLikeAskFn := func(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
		calls++
		return DecisionAsk, "delegate to pipeline need-confirm UI"
	}
	m := NewManager(PolicyOnRequest, prodLikeAskFn)

	for i := 0; i < 3; i++ {
		d, _, err := m.Check(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil {
			t.Fatalf("call %d err=%v", i, err)
		}
		if d != DecisionAsk {
			t.Fatalf("call %d: got %v, want DecisionAsk", i, d)
		}
	}
	if calls != 3 {
		t.Fatalf("askFn 调用次数=%d, want 3：生产 AskFn 恒 Ask，缓存既未写入也未命中（惰性）", calls)
	}
}

// S9 characterization：钉死「缓存旁路」机制确实存在（潜在、当前生产不可达）。
// 仅当 AskFn 返回 DecisionAllow 时缓存才写入；此后二次 Check 命中缓存、不再询问，
// 也不再重评 policy（manager.go:43-46 自述的 P2 限制）。供 HG-D 决策参考。
func TestChar_S9_ManagerCachePopulatedOnlyWhenAskAllows(t *testing.T) {
	calls := 0
	allowAskFn := func(ctx context.Context, toolName, input string) (ApprovalDecision, string) {
		calls++
		return DecisionAllow, "allow (test)"
	}
	m := NewManager(PolicyOnRequest, allowAskFn)

	if d, _, _ := m.Check(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`); d != DecisionAllow {
		t.Fatal("first should allow")
	}
	first := calls
	if d, _, _ := m.Check(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`); d != DecisionAllow {
		t.Fatal("second should be cached allow")
	}
	if calls != first {
		t.Fatalf("askFn 再次被调用（%d -> %d）：缓存未短路，与缓存命中预期不符", first, calls)
	}
}
