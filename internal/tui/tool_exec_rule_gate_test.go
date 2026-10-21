package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// 本文件覆盖 TUI 交互确认门（tool_exec.go:executeTools）与规则系统的关系。
//
// 背景：executeTools 在弹出确认框之前，只看 AnalyzeBashCommand 的风险等级和
// Policy.Has()（会话记忆），从不查询规则系统；Policy.Check() 位于
// executeToolsDirect 内部，只有用户点了确认之后才会被调用。
// 结果就是：显式 deny 规则命中时仍然弹框，只有用户先按 Y 才会被拦下。
//
// 对标同类 CLI：
// 规则裁决发生在「是否弹窗」之前，Forbidden 直接拒绝、绝不进入审批流程。

const rmVictimInput = `{"command":"rm /tmp/whale-ab/victim.txt"}`

// newRuleGateTestModel 构造一个走 TUI 预检路径（Pipeline == nil）的 Model。
// Pipeline 为 nil 正是 executeTools 内部预检生效的前提条件。
func newRuleGateTestModel(t *testing.T) *Model {
	t.Helper()
	m := newApprovalTestModel(t)
	// 关键：newApprovalTestModel 返回的是 Model 的副本，副本里的 toolService
	// 仍指向原临时对象的 ToolModel（自引用指针在拷贝后失效）。
	// 必须重新 reassignServices()，否则设置的数据对 executeTools 不可见。
	m.reassignServices()
	// Pipeline == nil 是 executeTools 内部预检生效的前提条件。
	m.ToolModel.executor = &tools.Executor{}
	if m.ToolModel.policy == nil {
		m.ToolModel.policy = permission.NewPolicy(permission.LevelWorkspaceWrite)
	}
	return &m
}

// setRule 往当前 Policy 上装一条规则。
func setRule(t *testing.T, m *Model, ruleStr string, behavior permission.RuleBehavior) {
	t.Helper()
	r, err := permission.ParseRuleString(ruleStr, behavior, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString(%q) failed: %v", ruleStr, err)
	}
	rs := permission.NewRuleSet()
	rs.Add(*r)
	m.toolService.Policy().SetRuleSet(rs)
}

func setPendingBash(m *Model, input string) {
	m.toolService.SetPendingTools([]session.ContentBlock{
		{ID: "call-1", Name: "bash", Input: input},
	})
}

// TestExecuteTools_DenyRuleMustNotShowConfirmDialog 核心契约：
// 显式 deny 规则命中时，绝不弹确认框，直接返回错误结果。
// 对应同类 CLI：Decision::Forbidden 不进入审批流程。
func TestExecuteTools_DenyRuleMustNotShowConfirmDialog(t *testing.T) {
	m := newRuleGateTestModel(t)
	setRule(t, m, "bash(rm:*)", permission.RuleDeny)
	setPendingBash(m, rmVictimInput)

	cmd := m.executeTools()

	if m.toolService.PendingConfirm() != nil {
		t.Fatal("deny rule matched but a confirm dialog was created (rule must beat the dialog)")
	}
	if m.sessionService.State() == StateWaitingConfirm {
		t.Fatal("deny rule matched but state is StateWaitingConfirm")
	}
	if cmd == nil {
		t.Fatal("expected a tool result command, got nil")
	}

	msg := cmd()
	res, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg, got %T", msg)
	}
	if len(res.results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res.results))
	}
	if !res.results[0].IsError {
		t.Error("denied tool must return an error result")
	}
	if !strings.Contains(res.results[0].Output, "Blocked by permission rule") {
		t.Errorf("expected rule-block message, got: %q", res.results[0].Output)
	}
}

// TestExecuteTools_AllowRuleSkipsConfirmDialog 契约：
// 显式 allow 规则命中且规则本身不要求确认时，不应再弹确认框。
// 注意：allow + 危险命令（ruleCheckLocked 返回 needConfirm=true）仍要弹框，
// 对齐业内既有实现「allow 只能免问、不能免掉危险确认」。
func TestExecuteTools_AllowRuleSkipsConfirmDialog(t *testing.T) {
	m := newRuleGateTestModel(t)
	setRule(t, m, "bash(rm:*)", permission.RuleAllow)
	setPendingBash(m, rmVictimInput)

	cmd := m.executeTools()

	if m.toolService.PendingConfirm() != nil {
		t.Error("allow rule matched but a confirm dialog was still created")
	}
	// allow 规则命中后应直接进入执行（executeToolsDirect）
	if cmd == nil {
		t.Error("expected execution command, got nil")
	}
}

// TestExecuteTools_NoRuleKeepsExistingConfirmBehavior 回归保护：
// 没有任何规则命中时，破坏性命令仍然弹确认框（原有行为不得改变）。
func TestExecuteTools_NoRuleKeepsExistingConfirmBehavior(t *testing.T) {
	m := newRuleGateTestModel(t)
	m.toolService.Policy().SetRuleSet(nil)
	setPendingBash(m, rmVictimInput)

	cmd := m.executeTools()

	if m.toolService.PendingConfirm() == nil {
		t.Fatal("no rule matched: destructive command must still show the confirm dialog")
	}
	if m.sessionService.State() != StateWaitingConfirm {
		t.Errorf("expected StateWaitingConfirm, got %v", m.sessionService.State())
	}
	if cmd != nil {
		t.Error("while waiting for confirmation no execution command should be returned")
	}
}

// TestExecuteTools_DenyRuleDoesNotExecuteTool 端到端性质：
// deny 规则命中时，工具执行器不应被调用（不产生副作用）。
func TestExecuteTools_DenyRuleDoesNotExecuteTool(t *testing.T) {
	m := newRuleGateTestModel(t)
	setRule(t, m, "bash(rm:*)", permission.RuleDeny)
	setPendingBash(m, rmVictimInput)

	cmd := m.executeTools()
	if cmd == nil {
		t.Fatal("expected a result command, got nil")
	}
	msg := cmd()
	res, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg, got %T", msg)
	}
	if len(res.results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res.results))
	}
	if res.results[0].Name != "bash" {
		t.Errorf("expected result for bash, got %q", res.results[0].Name)
	}
	var _ tea.Msg = msg
}

// TestExecuteTools_AllowRuleStillBlocksDangerousCommand 边界：
// allow 规则不能豁免危险命令。ruleCheckLocked 对「allow + 危险命令」
// 返回 needConfirm=true，此时 ruleAllowsWithoutConfirm 必须为 false，
// 危险命令仍应落入 BashRiskDangerous 拦截，而不是被 allow 放行。
// 对齐业内既有实现：approval 只能免问，不能免掉危险确认。
func TestExecuteTools_AllowRuleStillBlocksDangerousCommand(t *testing.T) {
	m := newRuleGateTestModel(t)
	setRule(t, m, "bash(rm:*)", permission.RuleAllow)
	// 危险命令：rm -rf /
	setPendingBash(m, `{"command":"rm -rf /"}`)

	cmd := m.executeTools()
	if cmd == nil {
		t.Fatal("dangerous command must be blocked, expected a result command")
	}
	msg := cmd()
	res, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg, got %T", msg)
	}
	if len(res.results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res.results))
	}
	if !res.results[0].IsError {
		t.Error("dangerous command must be blocked with an error result even under allow rule")
	}
	if m.toolService.PendingConfirm() != nil {
		t.Error("dangerous command should be blocked outright, not show a confirm dialog")
	}
}
