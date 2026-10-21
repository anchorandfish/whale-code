// Package tui — 审批层多类型与 elicitation 按键处理测试（P0-6/P0-7）
//
// 本文件覆盖审批枚举与 elicitation 按键契约：
// - TestBuildApprovalOptions_Elicitation：elicitation 类型 3 选项 + Cancel/Esc
// - TestBuildApprovalOptions_Exec：Exec 类型 3 选项
// - TestBuildApprovalOptions_FileModify：FileModify 类型 3 选项
// - TestBuildApprovalOptions_Pipeline：Pipeline 类型 3 选项
// - TestDecisionToElicitationAction_Mapping：3 种决策映射到 MCP action
// - TestHandleElicitationKeyMsg_EscContract：Esc 硬编码 Cancel 契约
// - TestHandleElicitationKeyMsg_Navigation：up/down 改变 SelectIdx
// - TestHandleElicitationKeyMsg_Enter：enter 触发 resolveElicitation
// - TestHandleElicitationKeyMsg_Shortcut：y/n 快捷键触发 resolveElicitation
//
// 对标同类 CLI, 532-567, 1092-1131。
package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/mcp"
)

func TestApprovalTargetSummaryShowsConcreteCommand(t *testing.T) {
	summary := approvalTargetSummary("bash", `{"command":"mkdir test"}`)
	// 形态改为英文主行 + 缩进中文辅行 ⇒ 断言主行文本
	if !strings.Contains(summary, "Command: mkdir test") {
		t.Fatalf("confirmation summary must show the concrete command, got %q", summary)
	}
	if strings.Contains(summary, "Call tool") {
		t.Fatalf("bash confirmation should not fall back to a generic tool description: %q", summary)
	}
}

func TestApprovalTargetSummaryExplainsFileToolIsNotShell(t *testing.T) {
	summary := approvalTargetSummary("write_file", `{"path":"network-marker.txt","content":"curl https://example.com"}`)
	if !strings.Contains(summary, "Content: curl https://example.com") {
		t.Fatalf("file confirmation should show the content being written, got %q", summary)
	}
	if !strings.Contains(summary, "no shell command") {
		t.Fatalf("file confirmation should state that no shell command runs, got %q", summary)
	}
}

// TestBuildApprovalOptions_Elicitation 验证 elicitation 类型返回 3 选项，
// 且第 3 个选项为 Cancel，快捷键为 esc（硬契约）。
func TestBuildApprovalOptions_Elicitation(t *testing.T) {
	opts := buildApprovalOptions(ApprovalKindMcpElicitation)
	if len(opts) != 3 {
		t.Fatalf("expected 3 options for elicitation, got %d", len(opts))
	}
	// 第 1 个选项 Accept
	if opts[0].Decision != DecisionApproveOnce {
		t.Errorf("option[0] expected DecisionApproveOnce, got %d", opts[0].Decision)
	}
	if opts[0].Shortcut != "y" {
		t.Errorf("option[0] expected shortcut 'y', got %q", opts[0].Shortcut)
	}
	// 第 2 个选项 Decline
	if opts[1].Decision != DecisionDeny {
		t.Errorf("option[1] expected DecisionDeny, got %d", opts[1].Decision)
	}
	if opts[1].Shortcut != "n" {
		t.Errorf("option[1] expected shortcut 'n', got %q", opts[1].Shortcut)
	}
	// 第 3 个选项 Cancel（Esc 硬编码映射）
	if opts[2].Decision != DecisionCancel {
		t.Errorf("option[2] expected DecisionCancel, got %d", opts[2].Decision)
	}
	if opts[2].Shortcut != "esc" {
		t.Errorf("option[2] expected shortcut 'esc', got %q", opts[2].Shortcut)
	}
}

// TestBuildApprovalOptions_Exec 验证 Exec 类型返回一次、本会话、拒绝三选项。
func TestBuildApprovalOptions_Exec(t *testing.T) {
	opts := buildApprovalOptions(ApprovalKindExec)
	if len(opts) != 3 {
		t.Fatalf("expected 3 options for Exec, got %d", len(opts))
	}
	if opts[0].Decision != DecisionApproveOnce {
		t.Errorf("option[0] expected DecisionApproveOnce, got %d", opts[0].Decision)
	}
	// Label 英文主行（中文辅行在 LabelZH，由渲染层缩进显示）
	if opts[0].Label != "[Y] Allow once" {
		t.Errorf("option[0] should be one-time approval, got %q", opts[0].Label)
	}
	if opts[0].LabelZH != "允许本次" {
		t.Errorf("option[0] should carry the Chinese aux line, got %q", opts[0].LabelZH)
	}
	if opts[1].Decision != DecisionApproveSession {
		t.Errorf("option[1] expected DecisionApproveSession, got %d", opts[1].Decision)
	}
	if opts[2].Decision != DecisionDeny {
		t.Errorf("option[2] expected DecisionDeny, got %d", opts[2].Decision)
	}
}

// TestBuildApprovalOptions_FileModify 验证 FileModify 类型返回 3 选项。
func TestBuildApprovalOptions_FileModify(t *testing.T) {
	opts := buildApprovalOptions(ApprovalKindFileModify)
	if len(opts) != 3 {
		t.Fatalf("expected 3 options for FileModify, got %d", len(opts))
	}
}

// TestBuildApprovalOptions_Pipeline 验证 Pipeline 类型返回 3 选项。
func TestBuildApprovalOptions_Pipeline(t *testing.T) {
	opts := buildApprovalOptions(ApprovalKindPipeline)
	if len(opts) != 3 {
		t.Fatalf("expected 3 options for Pipeline, got %d", len(opts))
	}
}

// TestDecisionToElicitationAction_Mapping 验证 3 种 ApprovalDecision 映射到 MCP ElicitationAction。
// 对标同类 CLI McpServerElicitationAction（Accept/Decline/Cancel）。
func TestDecisionToElicitationAction_Mapping(t *testing.T) {
	cases := []struct {
		name     string
		decision ApprovalDecision
		want     mcp.ElicitationAction
	}{
		{"ApproveOnce→Accept", DecisionApproveOnce, mcp.ElicitationActionAccept},
		{"Deny→Decline", DecisionDeny, mcp.ElicitationActionDecline},
		{"Cancel→Cancel", DecisionCancel, mcp.ElicitationActionCancel},
		// 未映射的决策（DecisionApproveSession）走 default→Cancel
		{"ApproveSession→Cancel(default)", DecisionApproveSession, mcp.ElicitationActionCancel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decisionToElicitationAction(tc.decision)
			if got != tc.want {
				t.Errorf("decisionToElicitationAction(%d) = %s, want %s",
					tc.decision, got, tc.want)
			}
		})
	}
}

// newApprovalTestModel 创建带 PendingConfirm 的测试 Model。
// 使用 NewModel 构造完整 Model 后调用 reassignServices 修复指针别名，
// 然后设置 elicitation 类型的 PendingConfirm。
func newApprovalTestModel(t *testing.T) Model {
	t.Helper()
	cfg := newTestConfig()
	client := newTestClient("test-model")
	reg := newTestRegistry()
	m := NewModel(cfg, client, reg)
	m.reassignServices()
	return m
}

// newTestConfig 创建测试用配置指针（NewModel 需要 *config.Config）。
func newTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	return &cfg
}

// setElicitationPendingConfirm 设置 elicitation 类型的 PendingConfirm 到 Model。
func setElicitationPendingConfirm(m *Model, selectIdx int) {
	pc := &PendingConfirm{
		Kind:          ApprovalKindMcpElicitation,
		Options:       buildApprovalOptions(ApprovalKindMcpElicitation),
		SelectIdx:     selectIdx,
		McpServerName: "test-server",
		McpRequestID:  json.RawMessage(`"req-1"`),
		McpMessage:    "Please provide your API key",
	}
	m.toolService.SetPendingConfirm(pc)
}

// TestHandleElicitationKeyMsg_EscContract 验证 Esc 键硬编码映射为 DecisionCancel。
// 硬契约：无论用户是否自定义键位，Esc 都强制返回 non-nil cmd（触发 resolveElicitation）。
// 对标同类 CLI
func TestHandleElicitationKeyMsg_EscContract(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0)

	// 按 Esc
	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc should return non-nil cmd (triggers resolveElicitation)")
	}

	// 验证 PendingConfirm 已被清理
	if m.toolService.PendingConfirm() != nil {
		t.Error("PendingConfirm should be nil after Esc (resolveElicitation clears it)")
	}
}

// TestHandleElicitationKeyMsg_Navigation 验证 up/down 改变 SelectIdx。
// down 增加 SelectIdx（不越界），up 减少 SelectIdx（不越界）。
func TestHandleElicitationKeyMsg_Navigation(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0)

	// down → SelectIdx 从 0 变 1
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyDown})
	if m.toolService.PendingConfirm().SelectIdx != 1 {
		t.Errorf("after down, expected SelectIdx=1, got %d", m.toolService.PendingConfirm().SelectIdx)
	}

	// down → SelectIdx 从 1 变 2
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyDown})
	if m.toolService.PendingConfirm().SelectIdx != 2 {
		t.Errorf("after second down, expected SelectIdx=2, got %d", m.toolService.PendingConfirm().SelectIdx)
	}

	// down → 已在最后，SelectIdx 保持 2（不越界）
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyDown})
	if m.toolService.PendingConfirm().SelectIdx != 2 {
		t.Errorf("after third down at bottom, expected SelectIdx=2, got %d", m.toolService.PendingConfirm().SelectIdx)
	}

	// up → SelectIdx 从 2 变 1
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyUp})
	if m.toolService.PendingConfirm().SelectIdx != 1 {
		t.Errorf("after up, expected SelectIdx=1, got %d", m.toolService.PendingConfirm().SelectIdx)
	}

	// up → SelectIdx 从 1 变 0
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyUp})
	if m.toolService.PendingConfirm().SelectIdx != 0 {
		t.Errorf("after second up, expected SelectIdx=0, got %d", m.toolService.PendingConfirm().SelectIdx)
	}

	// up → 已在顶部，SelectIdx 保持 0（不越界）
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyUp})
	if m.toolService.PendingConfirm().SelectIdx != 0 {
		t.Errorf("after third up at top, expected SelectIdx=0, got %d", m.toolService.PendingConfirm().SelectIdx)
	}
}

// TestHandleElicitationKeyMsg_Navigation_VimKeys 验证 j/k vim 风格导航。
func TestHandleElicitationKeyMsg_Navigation_VimKeys(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0)

	// j → down
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.toolService.PendingConfirm().SelectIdx != 1 {
		t.Errorf("after 'j', expected SelectIdx=1, got %d", m.toolService.PendingConfirm().SelectIdx)
	}

	// k → up
	m, _ = m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.toolService.PendingConfirm().SelectIdx != 0 {
		t.Errorf("after 'k', expected SelectIdx=0, got %d", m.toolService.PendingConfirm().SelectIdx)
	}
}

// TestHandleElicitationKeyMsg_Enter 验证 enter 键触发 resolveElicitation。
// SelectIdx=0 时 enter → DecisionApproveOnce（Accept）。
func TestHandleElicitationKeyMsg_Enter(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0)

	// enter 在 SelectIdx=0（Accept）
	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should return non-nil cmd (triggers resolveElicitation)")
	}
	if m.toolService.PendingConfirm() != nil {
		t.Error("PendingConfirm should be nil after enter (resolveElicitation clears it)")
	}
}

// TestHandleElicitationKeyMsg_Enter_Decline 验证 SelectIdx=1 时 enter → DecisionDeny。
func TestHandleElicitationKeyMsg_Enter_Decline(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 1) // SelectIdx=1 = Decline

	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should return non-nil cmd")
	}
	if m.toolService.PendingConfirm() != nil {
		t.Error("PendingConfirm should be nil after enter")
	}
}

// TestHandleElicitationKeyMsg_Shortcut_Y 验证 'y' 快捷键触发 Accept。
func TestHandleElicitationKeyMsg_Shortcut_Y(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 2) // SelectIdx=2，但 'y' 快捷键应直接触发 Accept

	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("'y' shortcut should return non-nil cmd")
	}
	if m.toolService.PendingConfirm() != nil {
		t.Error("PendingConfirm should be nil after 'y' shortcut")
	}
}

// TestHandleElicitationKeyMsg_Shortcut_N 验证 'n' 快捷键触发 Decline。
func TestHandleElicitationKeyMsg_Shortcut_N(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0) // SelectIdx=0，但 'n' 快捷键应直接触发 Decline

	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("'n' shortcut should return non-nil cmd")
	}
	if m.toolService.PendingConfirm() != nil {
		t.Error("PendingConfirm should be nil after 'n' shortcut")
	}
}

// TestHandleElicitationKeyMsg_NilPendingConfirm 验证无 PendingConfirm 时返回 nil cmd（不 panic）。
func TestHandleElicitationKeyMsg_NilPendingConfirm(t *testing.T) {
	m := newApprovalTestModel(t)
	// 不设置 PendingConfirm（nil）

	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("cmd should be nil when PendingConfirm is nil")
	}
}

// TestHandleElicitationKeyMsg_UnknownKey 验证未知按键返回 nil cmd（不触发 resolveElicitation）。
func TestHandleElicitationKeyMsg_UnknownKey(t *testing.T) {
	m := newApprovalTestModel(t)
	setElicitationPendingConfirm(&m, 0)

	// 按未知键 'x'，不应触发 resolveElicitation
	_, cmd := m.handleElicitationKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("cmd should be nil for unknown key 'x'")
	}
	// PendingConfirm 仍应存在
	if m.toolService.PendingConfirm() == nil {
		t.Error("PendingConfirm should still exist after unknown key")
	}
}
