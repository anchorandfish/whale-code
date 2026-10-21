package tui

// on-failure 沙箱升级的 bubbletea 主线接线（残留 #1）。
//
// plain 模式已接 OnSandboxEscalate（终端 Y/N）；bubbletea 主线（bridge 模式）
// 当时因 runner_core.go 被并行会话占用而未接 ⇒ 主线用户被沙箱 EPERM 卡住时
// 只看到诚实报告，没有「出沙箱重跑」的口子。本文件按 agent-confirm 桥同款
// 机制（program.Send + replyCh 阻塞 + 复用确认 overlay）接线并判别：
//
//	app.go case sandboxEscalateRequestMsg → 占确认槽 → handleSandboxEscalateKeyMsg
//	bridge sandboxEscalateBridge 阻塞等待决策；无 program / 超时 ⇒ false（fail-closed）
//
// 判别纪律：Update 驱动（不是手工构造 PendingConfirm）——这正是 bubbletea
// 事件循环做的事；跳过这一跳测到的只是半条生产路径。

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// w094EscalatePending 构造处于「沙箱升级确认中」状态的 Model（HOME 指向临时目录，
// 生产退出收尾同 deltaF：CancelWiring 防后台落盘协程写临时目录）。
func w094EscalatePending(t *testing.T, command string) (Model, chan bool, string) {
	t.Helper()
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)

	m := newApprovalTestModel(t)
	guTrackModelTeardown(t, &m)
	m.reassignServices()

	replyCh := make(chan bool, 1)
	updated, _ := m.Update(sandboxEscalateRequestMsg{
		toolName: "bash",
		command:  command,
		reason:   "sandbox kernel denied (EPERM)",
		replyCh:  replyCh,
	})
	m2 := r3AsModel(t, updated)
	if m2.toolService.PendingConfirm() == nil {
		t.Fatal("前置失败：沙箱升级确认槽未按预期占用")
	}
	if m2.sessionService.State() != StateWaitingConfirm {
		t.Fatalf("前置失败：状态 = %v，want StateWaitingConfirm", m2.sessionService.State())
	}
	return m2, replyCh, home
}

// TestW094R1_OverlayOpensOnRequest 升级请求弹确认面板（不是静默吞掉）。
func TestW094R1_OverlayOpensOnRequest(t *testing.T) {
	m, replyCh, _ := w094EscalatePending(t, "touch ~/w094_marker")
	pc := m.toolService.PendingConfirm()
	if pc.SandboxEscalateCh == nil {
		t.Fatal("PendingConfirm.SandboxEscalateCh 为 nil：按键分发无法路由到升级分支")
	}
	if len(pc.Options) == 0 {
		t.Fatal("升级面板没有选项")
	}
	// 面板必须让用户看懂「这是沙箱升级、只对本条生效」——ToolInput 带命令原文。
	if pc.ToolInput == "" {
		t.Fatal("面板 ToolInput 为空：用户看不到被卡住的命令")
	}
	select {
	case v := <-replyCh:
		t.Fatalf("面板刚打开就收到回传 %v：请求被静默处置了", v)
	default:
	}
}

// TestW094R1_ApproveRepliesTrueAndReruns y / enter ⇒ 回传 true（出沙箱重跑这一条）。
func TestW094R1_ApproveRepliesTrueAndReruns(t *testing.T) {
	for _, key := range []string{"y", "enter"} {
		t.Run(key, func(t *testing.T) {
			m, replyCh, _ := w094EscalatePending(t, "touch ~/w094_marker")
			updated, _ := m.Update(deltaFKey(key))
			m2 := r3AsModel(t, updated)
			select {
			case v := <-replyCh:
				if !v {
					t.Fatalf("%s 应回传 true（批准升级），got false", key)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s 后 1s 内无回传：决策没有送到 replyCh", key)
			}
			if m2.toolService.PendingConfirm() != nil {
				t.Fatal("决策后确认槽未清空")
			}
			if st := m2.sessionService.State(); st != StateToolExec {
				t.Fatalf("决策后状态 = %v，want StateToolExec（主流仍在工具执行 goroutine 中）", st)
			}
		})
	}
}

// TestW094R1_DenyAndEscRepliesFalse n / esc ⇒ 回传 false（维持拒绝，fail-closed）。
func TestW094R1_DenyAndEscRepliesFalse(t *testing.T) {
	for _, key := range []string{"n", "esc"} {
		t.Run(key, func(t *testing.T) {
			m, replyCh, _ := w094EscalatePending(t, "touch ~/w094_marker")
			updated, _ := m.Update(deltaFKey(key))
			m2 := r3AsModel(t, updated)
			select {
			case v := <-replyCh:
				if v {
					t.Fatalf("%s 应回传 false（维持拒绝），got true", key)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s 后 1s 内无回传", key)
			}
			if m2.toolService.PendingConfirm() != nil {
				t.Fatal("决策后确认槽未清空")
			}
		})
	}
}

// TestW094R1_SlotOccupiedDenied 确认槽已被占用时禁止覆盖（镜像 agent confirm 的
// 单槽守卫）：覆盖会让被顶掉的请求方悬挂到超时。
func TestW094R1_SlotOccupiedDenied(t *testing.T) {
	m, _, _ := w094EscalatePending(t, "touch ~/first")
	second := make(chan bool, 1)
	updated, _ := m.Update(sandboxEscalateRequestMsg{
		toolName: "bash",
		command:  "touch ~/second",
		reason:   "sandbox kernel denied (EPERM)",
		replyCh:  second,
	})
	m2 := r3AsModel(t, updated)
	if got := m2.toolService.PendingConfirm().ToolInput; got != "touch ~/first" {
		t.Fatalf("第二个请求覆盖了面板：ToolInput = %q，want 原命令", got)
	}
	select {
	case v := <-second:
		if v {
			t.Fatal("槽位占用时应回传 false（fail-closed），got true")
		}
	default:
		t.Fatal("槽位占用时第二个请求没有立即收到拒绝回传（将悬挂到超时）")
	}
}

// TestW094R1_BridgeNoProgramFailsClosed program 未绑定时桥必须返回 false 且不 panic
// （fail-closed：用户没被问过 ≠ 批准）。
func TestW094R1_BridgeNoProgramFailsClosed(t *testing.T) {
	home := newIsolatedTestHome(t)
	t.Setenv("HOME", home)
	m := newApprovalTestModel(t)
	guTrackModelTeardown(t, &m)
	m.reassignServices()
	// m.program 未绑定（NewModel 后未 SetProgram）——生产装配缺陷形态。
	if m.program.get() != nil {
		t.Skip("测试模型意外绑定了 program，无法构造无 program 形态")
	}
	got := m.sandboxEscalateBridge("bash", "touch ~/x", "EPERM")
	if got {
		t.Fatal("无 program 时桥返回 true = 未问先批，违背 fail-closed")
	}
}

// TestW094R1_EscalateDecisionsAudited 两态决策都留痕（delta F 纪律：拒绝不留痕
// ⇒「弹了窗却不知为何被拒」无从查起）。
func TestW094R1_EscalateDecisionsAudited(t *testing.T) {
	cases := []struct {
		key      string
		decision string
	}{
		{"y", "approved"},
		{"n", "denied"},
		{"esc", "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			m, replyCh, home := w094EscalatePending(t, "touch ~/w094_marker")
			updated, _ := m.Update(deltaFKey(tc.key))
			r3AsModel(t, updated)
			<-replyCh
			raw := string(deltaFRawAudit(t, home))
			if !containsAll(raw, tc.decision) {
				t.Fatalf("决策 %q 未留痕：审计文件不含 %q\n审计内容：\n%s", tc.key, tc.decision, raw)
			}
		})
	}
}

func containsAll(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 编译期锚：确认需要的类型/方法在本轮落地（改前此文件编译失败 = 红）。
var (
	_ = sandboxEscalateRequestMsg{}
	_ = (*Model).sandboxEscalateBridge
	_ = (*Model).handleSandboxEscalateKeyMsg
	_ tools.ConfirmOutcome
	_ tea.KeyMsg
)
