package tui

// S16 审计 · guard 缺失语义（接线层 / tui 包）
//
// ★ 本文件已被**有意翻转**（A6 落地，2026-09-15，O-U 轮）——**下一个人别当回归**：
//
//	翻转前（S16 characterization，2026-09-14 16:30）钉的是「guard 未初始化 ⇒ P0 防护被
//	静默关闭（fail-open）」= 缺陷面基线。A6（S16-G2）拍板 = **`guard == nil` ⇒ stopped=true**，
//	族A 口诀「缺的是**保护器** → T3」⇒ 保护器不存在即不许开工。
//	故两条用例的期望值由「不拦截」翻成「必须拦截」，并补 T3 停机可见性断言。
//
// 生产路径行为不变：`NewModel` / `NewPlainRunner` / `NewBridgeRunner` 均经 `newAgentGuard`
// 构造（`TestNewPlainRunnerInitializesGuard` / `TestNewBridgeRunnerInitializesGuard` 守），
// nil 只存在于零值构造。此处刻意用**已接线的生产 runner** 再把 guard 置 nil，
// 以模拟"防护器缺失"这一被测条件本身（而不是手工拼一个半成品 runner）。
//
// 对照同类 CLI guardian「Fail closed on timeout, execution failure, or malformed output」
//。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestGU_A6_NilGuardStopsModelCallGate 判据：nil guard ⇒ 模型调用前闸门必须停机（T3）。
//
// ★ 此处**刻意不装配 Store**（Store 保持 nil）：A6 让「guard 缺失」也走 renderGuardStop，
// 若停机路径自身在最小化装配下 panic，那 fail-closed 就比被拒行为更危险 —— 等于
// 「为了更安全而崩溃」。本用例同时钉住「停机 + 不 panic」两条（renderGuardStop →
// saveSession 必须对 nil Store 返回 error 而非解引用，见 runner_core.saveSession 守卫）。
func TestGU_A6_NilGuardStopsModelCallGate(t *testing.T) {
	t.Setenv("HOME", newIsolatedTestHome(t))
	r := newTestPlainRunner()
	r.guard = nil // 被测条件：防护器未初始化

	if !r.guardBeforeModelCall() {
		t.Fatal("A6：nil guard 必须停机（fail-closed T3），不得退回放行")
	}
}

// TestGU_A6_NilGuardStopsToolRoundBreaker 判据：nil guard ⇒ 工具轮判定必须停机（T3）。
// 本用例**装配 Store**（与上一条互补）：已接线 runner 的停机收尾（IncrementTurn/
// saveSession/快照/钩子）仍须走通，不得因改动而漏掉持久化。
func TestGU_A6_NilGuardStopsToolRoundBreaker(t *testing.T) {
	t.Setenv("HOME", newIsolatedTestHome(t))
	r := newTestPlainRunner()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r.Store = store
	r.guard = nil

	if !r.guardAfterToolRound(nil, nil) {
		t.Fatal("A6：nil guard 的熔断判定必须停机（fail-closed T3），不得退回放行")
	}
}

// TestGU_A6_SaveSessionNilStoreIsErrorNotPanic 根因契约（A6 顺带修根）：
// saveSession 是 Store 的唯一解引用点，停机/收尾路径会无条件调它
// ⇒ 未装配 Store 时必须返回 error，**不得 panic**（否则 fail-closed 路径反而崩掉进程）。
func TestGU_A6_SaveSessionNilStoreIsErrorNotPanic(t *testing.T) {
	c := &runnerCore{Session: session.NewSession()}
	if err := c.saveSession(); err == nil {
		t.Fatal("A6：未装配 Store 时 saveSession 必须返回 error（持久化 best-effort），got nil")
	}
	cn := &runnerCore{} // 连 Session 都没有
	if err := cn.saveSession(); err == nil {
		t.Fatal("A6：无活动 Session 时 saveSession 必须返回 error，got nil")
	}
}

// TestGU_A6_NilGuardStopsBridgeGate bridge 路径同判据（三条路径同语义，缺一即漏）。
func TestGU_A6_NilGuardStopsBridgeGate(t *testing.T) {
	t.Setenv("HOME", newIsolatedTestHome(t))
	r, _ := newTestBridgeRunner()
	r.guard = nil

	if !r.guardBeforeModelCall() {
		t.Fatal("A6：bridge 路径 nil guard 必须停机（fail-closed T3）")
	}
	if !r.guardAfterToolRound(nil, nil) {
		t.Fatal("A6：bridge 路径 nil guard 的熔断判定必须停机（fail-closed T3）")
	}
}

// TestGU_A6_NilGuardStopsTuiGate TUI(Model) 路径同判据 + 停机原因必须可见（T3 不得静默）。
func TestGU_A6_NilGuardStopsTuiGate(t *testing.T) {
	m, _ := guModelIsolatedHome(t)
	m.guard = nil

	if _, stopped := m.guardBeforeModelCall(); !stopped {
		t.Fatal("A6：TUI 路径 nil guard 必须停机（fail-closed T3）")
	}
	if _, stopped := m.guardAfterToolRound(nil); !stopped {
		t.Fatal("A6：TUI 路径 nil guard 的熔断判定必须停机（fail-closed T3）")
	}
}

// TestGU_A6_NilGuardStopReasonIsVisible 可见性判据：T3 停机原因必须能对用户讲清
// 「防护器缺失」而不是一句空话（族A：拒绝必须可见）。
func TestGU_A6_NilGuardStopReasonIsVisible(t *testing.T) {
	d := guardNilStop("test")
	if d.Verdict != agentguard.VerdictStop {
		t.Fatalf("A6：必须构造 VerdictStop，got %v", d.Verdict)
	}
	if !strings.Contains(d.Reason, "T3") || !strings.Contains(d.Reason, "未初始化") {
		t.Fatalf("A6：停机原因须点明「防护器未初始化 + T3」；got %q", d.Reason)
	}
}
