// r3_refusal_transcript_test.go — R3 在 **internal/tui** 一侧的判别性验收：
// S17 §4.2 D2/D3 + §5.3「在 agent_confirm 生产调度路径上分别驱动
// replyCh=false / ctx 取消 / 槽位占用 → 断言三者文案互不相同且都留了 transcript 记录」。
//
// 生产入口（全部经 Model.Update 调度，不直调 finishAgentConfirm —— 见
// agent_confirm_wiring_test.go 的教训：直调等价物会掩盖"按键分支没接回 replyCh"）：
//
//	app.go:2154 case agentConfirmRequestMsg ← 子代理确认请求（占用确认槽）
//	app.go:1399 StateWaitingConfirm 分支 → handleAgentConfirmKeyMsg（用户决策）
//	app.go:2163 槽位占用守卫（D3）
//
// 修复前（D2）：子代理路径的拒绝**一条消息都不留**，用户侧表现为
// "弹了窗却不知道原因"（2026-09-13 现场排查的真实障碍）。
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// r3AsModel 把 Update 的返回值归一为 Model。
//
// ⚠️ 生产代码里 Update 各分支返回的**具体类型不一致**：
// `agentConfirmRequestMsg` 分支返回 `Model`（值），而按键分支经
// `handleAgentConfirmKeyMsg`（*Model 接收者）返回 `*Model`。
// 两者都满足 tea.Model，但直接 `updated.(Model)` 会在按键分支上失败。
// 本 helper 只做归一，不改生产行为；messages/toolService 均为指针字段，值拷贝后仍共享。
func r3AsModel(t *testing.T, v tea.Model) Model {
	t.Helper()
	switch m := v.(type) {
	case Model:
		return m
	case *Model:
		return *m
	default:
		t.Fatalf("Update 应返回 Model 或 *Model，实际 %T", v)
		return Model{}
	}
}

// r3RequestConfirm 经生产调度入口发一条子代理确认请求，返回 (Model, 请求前的 transcript 长度)。
func r3RequestConfirm(t *testing.T, toolName, input string, replyCh chan tools.ConfirmOutcome) (Model, int) {
	t.Helper()
	cfg := &config.Config{PermissionMode: "workspace-write"}
	m := NewModel(cfg, newTestClient("test-model"), newTestRegistry())
	before := len(m.messages.All())

	updated, _ := m.Update(agentConfirmRequestMsg{
		toolName: toolName, toolInput: input, replyCh: replyCh,
	})
	m2 := r3AsModel(t, updated)
	if m2.toolService.PendingConfirm() == nil {
		t.Fatal("前置失败：确认槽未被占用（agentConfirmRequestMsg 未生效）")
	}
	if m2.sessionService.State() != StateWaitingConfirm {
		t.Fatalf("前置失败：状态应为 StateWaitingConfirm，实际 %v", m2.sessionService.State())
	}
	return m2, before
}

// TestR3_D2_UserRejectionsLeaveDistinctTranscriptRecords 覆盖**用户亲自做出**的两种拒绝：
// - n → Denied / user → 文案须含 "denied by user"
// - esc→ Cancelled / user → 文案须含 "cancelled by user"（D1：中断 ≠ 拒绝）
//
// 三者（含槽位的 slot-guard）文案必须互不相同 —— 这是"用户按了却看不出原因"的直接解药。
func TestR3_D2_UserRejectionsLeaveDistinctTranscriptRecords(t *testing.T) {
	cases := []struct {
		name       string
		key        tea.KeyMsg
		wantSubstr string
	}{
		{"user-deny", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, "denied by user"},
		{"user-cancel", tea.KeyMsg{Type: tea.KeyEsc}, "cancelled by user"},
	}

	texts := make(map[string]string, len(cases)+1)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			replyCh := make(chan tools.ConfirmOutcome, 1)
			m, before := r3RequestConfirm(t, "bash", `{"command":"ls"}`, replyCh)

			updated, _ := m.Update(c.key)
			_ = r3AsModel(t, updated)

			all := m.messages.All()
			if len(all) <= before {
				t.Fatalf("D2 失败：%s 拒绝后 transcript **未新增任何记录**（before=%d after=%d）—— "+
					"这正是「弹了窗却不知道原因」的根因", c.name, before, len(all))
			}
			joined := strings.Join(all[before:], "\n")
			if !strings.Contains(joined, c.wantSubstr) {
				t.Fatalf("D2/D1 失败：%s 的 transcript 记录应含 %q，实际 %q", c.name, c.wantSubstr, joined)
			}
			if strings.Contains(joined, "denied by user") && c.name == "user-cancel" {
				t.Fatalf("D1 失败：esc 中断被写成了「用户拒绝」：%q", joined)
			}
			texts[c.name] = joined

			select {
			case got := <-replyCh:
				if got.Approved() {
					t.Fatalf("%s 应被拒绝，实际 %+v", c.name, got)
				}
				if got.Source != tools.SourceUser {
					t.Fatalf("%s 的 Source 应为 user，实际 %q", c.name, got.Source)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("按键后 replyCh 未收到决策（子代理将悬挂）")
			}
		})
	}

	// 两种用户决策的文案必须互不相同（D1 的核心：deny 与 interrupt 不能同款文案）。
	if texts["user-deny"] == texts["user-cancel"] {
		t.Fatalf("D1 失败：deny 与 cancel 的 transcript 文案相同（%q）", texts["user-deny"])
	}
}

// TestR3_D3_SlotGuardRejectionIsAttributedAndRecorded 覆盖槽位占用守卫（D3）+ 留痕（D2）：
// 确认槽已被占用时，新请求必须立即收到 **Source=slot-guard** 的拒绝（不是"用户拒绝"），
// 且该拒绝**必须留痕** —— 修复前该分支只发 replyCh + 一条 slog，请求在界面上凭空消失。
func TestR3_D3_SlotGuardRejectionIsAttributedAndRecorded(t *testing.T) {
	firstCh := make(chan tools.ConfirmOutcome, 1)
	m, before := r3RequestConfirm(t, "write_file", `{"path":"a.txt"}`, firstCh)

	// 第二个请求（另一个子代理并发）—— 槽位已占用，不得覆盖。
	secondCh := make(chan tools.ConfirmOutcome, 1)
	updated, _ := m.Update(agentConfirmRequestMsg{
		toolName: "bash", toolInput: `{"command":"ls"}`, replyCh: secondCh,
	})
	m2 := r3AsModel(t, updated)

	// 占用者不得被顶掉（顶掉 = 原请求方永远收不到决策 → 悬挂至超时/卡死）。
	if pc := m2.toolService.PendingConfirm(); pc == nil || pc.ToolName != "write_file" {
		t.Fatalf("D3 失败：占用守卫失效，槽位被覆盖为 %v", pc)
	}

	select {
	case got := <-secondCh:
		if got.Approved() {
			t.Fatal("D3 失败：槽位被占用时新请求应被拒绝，而非放行")
		}
		if got.Source != tools.SourceSlotGuard {
			t.Fatalf("D3 失败：Source = %q，want %q（槽位占用时用户从未被询问，绝不能说成用户拒绝）",
				got.Source, tools.SourceSlotGuard)
		}
	default:
		t.Fatal("D3 失败：被拒请求的 replyCh 未立即收到决策（子代理悬挂至超时）")
	}

	all := m.messages.All()
	if len(all) <= before {
		t.Fatalf("D2 失败：槽位占用拒绝**未留 transcript 记录**（before=%d after=%d）—— "+
			"请求在界面上凭空消失，用户无从得知", before, len(all))
	}
	joined := strings.Join(all[before:], "\n")
	if !strings.Contains(joined, "slot") && !strings.Contains(joined, "confirm slot") {
		t.Fatalf("D2/D3 失败：槽位占用记录应说明原因（slot busy），实际 %q", joined)
	}
	if strings.Contains(joined, "by user") {
		t.Fatalf("D3 失败：槽位占用时用户未被询问，文案不得写成「用户拒绝」：%q", joined)
	}
}
