package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anchorandfish/whale-code/internal/hook"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// 复现 A/B 测试中暴露的真实路径缺陷：
// 显式 deny 规则命中时，DestructiveCheckStage（破坏性命令：rm）在 PermissionStage
// 之前就返回 NeedConfirm=true，ToolPipeline.Execute 随即短路返回，
// 导致持有规则的 PermissionStage 根本没机会参与决策 —— 表现就是"规则配了，弹窗照弹"。
// 对齐业内既有实现：Decision::Forbidden 绝不进入审批流。
func TestPipeline_DenyRuleMustNotAskForConfirmation(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	p := NewToolPipeline(r, e, hook.NewManager(tmp), policy, tmp)
	res := p.Execute(context.Background(), Call{
		ID:    "call-1",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`,
	})

	if res.NeedConfirm {
		t.Fatalf("deny 规则命中时不应请求用户确认（不得弹框），stageLogs=%+v", res.StageLogs)
	}
	if res.Proceed {
		t.Fatalf("deny 规则命中时应被拒绝，不应放行执行，stageLogs=%+v", res.StageLogs)
	}
}

// allow 规则命中且规则本身不要求确认时，应跳过破坏性/风险确认，直接放行。
// 对齐业内既有实现：allow 只能免问，危险命令仍然拦（见 ruleCheckLocked 的 needConfirm 分支）。
func TestPipeline_AllowRuleSkipsConfirmation(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)
	e.RegisterHandler("bash", func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	})

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleAllow, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	p := NewToolPipeline(r, e, hook.NewManager(tmp), policy, tmp)
	res := p.Execute(context.Background(), Call{
		ID:    "call-2",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`,
	})

	if res.NeedConfirm {
		t.Fatalf("allow 规则已明确放行时不应再请求用户确认，stageLogs=%+v", res.StageLogs)
	}
}

// 生产入口层断言（流程改进 T2：红测必须落在生产真正调用的入口，而不是被测函数所在层）。
//
// 上一轮的教训：`internal/tui/tool_exec_rule_gate_test.go` 为了让被测代码跑到，
// 手工把 `m.ToolModel.executor = &tools.Executor{}` 置空 Pipeline —— 测试是绿的，
// 但生产 TUI 里 `lifecycle.go:418` 无条件 `InitPipelineWithHooks`，那个守卫恒为假，
// 被测分支是死代码。测试脚手架与生产接线不一致 = 绿灯毫无意义。
//
// 本用例改从生产真实入口进入：`Executor.InitPipelineWithHooks`（生产初始化方式）
// + `ExecuteWithConfirm`（`internal/tui/tool_exec.go:executeToolsDirect` 实际调用的入口），
// 不再手工拼装内部结构。
func TestExecutor_ExecuteWithConfirm_DenyRuleNoConfirmation(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	rule, err := permission.ParseRuleString("bash(rm:*)", permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// 与生产一致的初始化方式
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：本用例的前提失效，无法代表生产路径")
	}

	res := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "call-4",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`,
	})

	if res.NeedConfirm {
		t.Fatalf("生产入口 ExecuteWithConfirm：deny 规则命中时不应请求确认，stageLogs=%+v", res.StageLogs)
	}
	if res.Proceed {
		t.Fatalf("生产入口 ExecuteWithConfirm：deny 规则命中时应被拒绝，stageLogs=%+v", res.StageLogs)
	}
}

// 回归保护：无规则命中时，破坏性命令仍应请求确认（不得因为加了规则前置检查而丢掉确认）。
func TestPipeline_NoRuleKeepsDestructiveConfirmation(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	p := NewToolPipeline(r, e, hook.NewManager(tmp), policy, tmp)
	res := p.Execute(context.Background(), Call{
		ID:    "call-3",
		Name:  "bash",
		Input: `{"command":"rm /tmp/whale-ab/victim.txt"}`,
	})

	if !res.NeedConfirm {
		t.Fatalf("无规则命中时破坏性命令仍应请求确认，stageLogs=%+v", res.StageLogs)
	}
}
