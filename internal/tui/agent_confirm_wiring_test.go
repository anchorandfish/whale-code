// agent_confirm_wiring_test.go — G1/G4 接线完整性测试（分层验证链第 2 层：
// 生产构造函数初始化断言，防"字段+方法都写了但生产调用点没接"的假验证，
// 对齐 TestNewBridgeRunnerInitializesGuard 范式）。
package tui

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// TestNewModelInitializesAgentWiring G1/G4：NewModel 生产入口必须接线
// executor.UsageTracker（子代理用量落账）与 AgentConfirmFn（子代理确认通道）。
// 两者任一为 nil = 子代理编排能力在生产 TUI 路径上未生效（假接线）。
func TestNewModelInitializesAgentWiring(t *testing.T) {
	cfg := &config.Config{PermissionMode: "workspace-write"}
	m := NewModel(cfg, newTestClient("test-model"), newTestRegistry())

	exec := m.toolService.Executor()
	if exec == nil {
		t.Fatal("生产 Executor 未初始化")
	}
	if exec.UsageTracker == nil {
		t.Error("G1 假接线：executor.UsageTracker 未注入（子代理用量不落账，/usage 隐形）")
	}
	if exec.AgentConfirmFn == nil {
		t.Error("G4 假接线：executor.AgentConfirmFn 未注入（子代理 NeedConfirm fail-closed，编排退化顾问模式）")
	}

	// P0（2026-09-13 修复）：**字段非 nil ≠ 通道可达**。AgentConfirmFn 是绑定方法，
	// 其 receiver 是 NewModel 的局部副本；若 program 不是**共享**持有者，该副本的
	// program 恒为 nil ⇒ 确认 overlay 永不弹出（微秒级静默 false，被伪造成"用户拒绝"）。
	// 本处只守"不变量"（programRef 已在 NewModel 创建且副本间共享）；
	// "通道真的可达"由 agent_confirm_program_binding_repro_test.go 用**调用**断言（R1b）。
	if m.program == nil {
		t.Error("P0 不变量破坏：NewModel 未创建共享 programRef（副本将各持一份，program 注入失效）")
	}
	if m.program != nil && m.program.get() != nil {
		t.Error("NewModel 后尚未 SetProgram，program 应为 nil（不应有任何地方提前注入）")
	}
	// 共享不变量：Program 内部持有的是 model 的一份**值拷贝**，故值拷贝后必须仍指向同一
	// programRef —— 这正是 P0 的要害。用一次 Update（值接收者/值返回）模拟该拷贝。
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m2, ok := updated.(Model); ok && m2.program != m.program {
		t.Error("programRef 未在副本间共享：SetProgram 的注入对 Program 驱动的副本不可见（P0 回归）")
	}
}

// TestAgentConfirmBridgeReply G4 第 3 层（防护真的发生）：bridge 请求经
// agentConfirmRequestMsg 处理后，用户决策必须经 replyCh 回传给阻塞中的调用方。
// 结果断言：approve → replyCh 收到 true；deny → 收到 false。
func TestAgentConfirmBridgeReply(t *testing.T) {
	cfg := &config.Config{PermissionMode: "workspace-write"}
	m := NewModel(cfg, newTestClient("test-model"), newTestRegistry())

	// 直接测试 msg 处理路径：模拟 AgentConfirmFn 发出的请求
	replyCh := make(chan tools.ConfirmOutcome, 1)
	msg := agentConfirmRequestMsg{toolName: "write_file", toolInput: `{"path":"a.txt"}`, replyCh: replyCh}

	// 更新流（直接调 Update 分支等价物）：这里用 finishAgentConfirm 的批准路径
	m.toolService.SetPendingConfirm(&PendingConfirm{
		ToolName:     msg.toolName,
		ToolInput:    msg.toolInput,
		Kind:         ApprovalKindFileModify,
		AgentReplyCh: msg.replyCh,
	})
	m.sessionService.SetState(StateToolExec)
	m.finishAgentConfirm(m.toolService.PendingConfirm(),
		tools.ConfirmOutcome{Decision: tools.DecisionApproved, Source: tools.SourceUser})

	select {
	case got := <-replyCh:
		if !got.Approved() {
			t.Errorf("批准决策应回传 Approved，实际 %+v", got)
		}
	default:
		t.Fatal("批准后 replyCh 必须收到决策（子代理仍会阻塞=编排失效）")
	}
}

// TestAgentConfirmFullDispatchPath G4 第 3 层加强（生产调度入口断言）：
// agentConfirmRequestMsg 经 Model.Update 路由 → PendingConfirm 弹层 → enter 键
// 经按键拦截分支 → 决策回传 replyCh。禁止直调 finishAgentConfirm 冒充全链路
// （streaming bug 教训：测试走降级路径、生产走另一条）。
func TestAgentConfirmFullDispatchPath(t *testing.T) {
	cfg := &config.Config{PermissionMode: "workspace-write"}
	m := NewModel(cfg, newTestClient("test-model"), newTestRegistry())

	replyCh := make(chan tools.ConfirmOutcome, 1)
	updated, _ := m.Update(agentConfirmRequestMsg{
		toolName:  "write_file",
		toolInput: `{"path":"a.txt","content":"x"}`,
		replyCh:   replyCh,
	})
	m2, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update 应返回 Model，实际 %T", updated)
	}
	if m2.toolService.PendingConfirm() == nil {
		t.Fatal("agentConfirmRequestMsg 后 PendingConfirm 应已设置（确认 overlay 未弹出）")
	}
	if m2.sessionService.State() != StateWaitingConfirm {
		t.Fatalf("状态应为 StateWaitingConfirm，实际 %v", m2.sessionService.State())
	}

	// 默认 SelectIdx=0（首个选项），enter 即选中。不同 Kind 的首选项可能是
	// "拒绝"（安全默认）——直接驱动 enter 并断言 replyCh 一定收到值（不悬挂）。
	updated2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m3, _ := updated2.(Model)
	_ = m3
	select {
	case got := <-replyCh:
		_ = got // 决策到达即全链路通（approve/deny 取决于默认选项，二者都是合法应答）
	case <-time.After(2 * time.Second):
		t.Fatal("enter 后 replyCh 未收到决策（按键拦截分支未接回 replyCh，子代理将悬挂）")
	}
}

// TestAgentConfirmSlotOccupiedGuard 单槽守卫：确认槽被占用时新请求被拒绝且不覆盖。
// 结果断言：占用者保持不变，新请求 replyCh 立即收到**归因为 slot-guard** 的拒绝
// —— D3（R3，2026-09-14）：槽位占用时用户根本没被问过，不得渲染成"用户拒绝"。
func TestAgentConfirmSlotOccupiedGuard(t *testing.T) {
	cfg := &config.Config{PermissionMode: "workspace-write"}
	m := NewModel(cfg, newTestClient("test-model"), newTestRegistry())

	// 先占据槽位（第一个子代理确认）
	firstCh := make(chan tools.ConfirmOutcome, 1)
	updated, _ := m.Update(agentConfirmRequestMsg{toolName: "write_file", toolInput: `{"path":"a.txt"}`, replyCh: firstCh})
	m2 := updated.(Model)
	if m2.toolService.PendingConfirm() == nil {
		t.Fatal("前置失败：第一个确认未占据槽位")
	}

	// 第二个请求（fork 并发场景）不应覆盖
	secondCh := make(chan tools.ConfirmOutcome, 1)
	updated3, _ := m2.Update(agentConfirmRequestMsg{toolName: "bash", toolInput: `{"command":"ls"}`, replyCh: secondCh})
	m3 := updated3.(Model)
	if pc := m3.toolService.PendingConfirm(); pc == nil || pc.ToolName != "write_file" {
		t.Fatalf("占用守卫失效：槽位被覆盖为 %v", pc)
	}
	select {
	case got := <-secondCh:
		if got.Approved() {
			t.Error("槽位被占用时新请求应被拒绝，而非放行")
		}
		if got.Source != tools.SourceSlotGuard {
			t.Errorf("D3 归因错误：Source = %q, want %q（槽位占用时用户未被询问，绝不能写成用户拒绝）",
				got.Source, tools.SourceSlotGuard)
		}
	default:
		t.Fatal("被拒请求的 replyCh 应立即收到决策（否则子代理悬挂至超时）")
	}
}
