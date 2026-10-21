package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 本文件复现用户 2026-09-13 手测 T-B/T-F 的疑问：
// "配了 deny 规则，工具却还要人工允许一次，然后才被权限拒绝。"
// 期望（对齐业内既有实现 Decision::Forbidden 不进审批流）：deny 规则命中 → 立即拒绝，
// 绝不弹出人工确认（主会话确认 overlay / 子代理 agentConfirmBridge 都不得被触发）。
//
// 生产接线断言（R1）：
//
//	生产入口：internal/tui/tool_exec.go:211 executeToolsDirect
//	  → 调用点：internal/tools/executor.go:819 ExecuteWithConfirm
//	    → internal/tools/pipeline.go:127 Pipeline.Execute（规则前置门 pipeline.go:152）
//	子代理入口：internal/tools/agent.go:808 AgentContext.executeToolCall
//	  → 调用点：internal/tools/executor.go:801 ExecuteWithPipeline → 同一个 Pipeline.Execute
//	规则来源：internal/tui/lifecycle.go:391 policy.WireRuleStore(homeDir, workDir)
//	管线装配：internal/tui/lifecycle.go:425 executor.InitPipelineWithHooks(policy, workDir)
//	分支守卫：pipeline.go:153 `if p.policy != nil` —— 生产恒为 true（lifecycle.go:425 传入 m.policy）

// writeProjectSettings 写入生产同款项目级配置文件 <workDir>/.whale/settings.json。
func writeProjectSettings(t *testing.T, workDir, body string) {
	t.Helper()
	dir := filepath.Join(workDir, ".whale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

// newProductionExecutor 用与生产完全相同的初始化顺序装配 Executor：
// NewPolicy → WireRuleStore（lifecycle.go:391）→ InitPipelineWithHooks（lifecycle.go:425）。
// 不使用任何手工拼装（不置空字段、不替换依赖），以保证脚手架与生产接线一致（R2/R3④）。
func newProductionExecutor(t *testing.T, workDir, settings string) *Executor {
	t.Helper()
	homeDir := t.TempDir() // 用户级配置为空目录，避免污染规则来源
	writeProjectSettings(t, workDir, settings)

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	if err := policy.WireRuleStore(homeDir, workDir); err != nil {
		t.Fatalf("WireRuleStore: %v", err)
	}

	exec := NewExecutor(NewRegistry(), workDir)
	exec.InitPipelineWithHooks(policy, workDir)
	if exec.Pipeline == nil {
		t.Fatal("Pipeline 未初始化：前提失效，本用例无法代表生产路径")
	}
	return exec
}

// newConfirmProbe 装配一个子代理确认通道探针：记录确认请求次数。
// 返回 (fn, func() int)。deny 规则命中时调用次数必须为 0。
func newConfirmProbe() (func(context.Context, string, string) ConfirmOutcome, func() int) {
	var (
		calls  int
		inputs []string
	)
	fn := func(_ context.Context, name, input string) ConfirmOutcome {
		calls++
		inputs = append(inputs, name)
		// 用户"允许"，用于暴露"允许后仍被拒"的现象
		return ConfirmOutcome{Decision: DecisionApproved, Source: SourceUser}
	}
	return fn, func() int {
		_ = inputs
		return calls
	}
}

// TestDenyRule_ReadFile_NoHumanPrompt_MainPath 覆盖 T-B 主会话路径：
// read_file 命中 deny 规则时，ExecuteWithConfirm 必须直接返回拒绝且 NeedConfirm=false
// （NeedConfirm=true 即会在 TUI 渲染"允许/拒绝"overlay，用户可见现象）。
func TestDenyRule_ReadFile_NoHumanPrompt_MainPath(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(target, []byte("# Whale Code\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"]},"priorityMode":"posix"}`)

	input := fmt.Sprintf(`{"path":%q,"limit":1}`, target)
	res := exec.ExecuteWithConfirm(context.Background(), Call{
		ID: "call-main-read", Name: "read_file", Input: input,
	})

	if res.NeedConfirm {
		t.Fatalf("deny 规则命中时不得请求人工确认（TUI 会弹允许框）；stageLogs=%+v", res.StageLogs)
	}
	if res.Proceed {
		t.Fatalf("deny 规则命中时必须拒绝；stageLogs=%+v", res.StageLogs)
	}
	if !strings.Contains(res.Rejection, "blocked by explicit deny rule") {
		t.Fatalf("拒绝理由应来自规则前置门，实际=%q", res.Rejection)
	}
	if res.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("FailureKind 应为 PolicyDenied，实际=%v", res.FailureKind)
	}
}

// TestDenyRule_ReadFile_NoHumanPrompt_SubAgentPath 覆盖 T-B 子代理路径：
// 子代理的 read_file 命中 deny 规则时，必须直接失败，且不得走 agentConfirmBridge
// （确认通道被调用 = 用户在 TUI 被要求"允许"，即"等待人工允许才拒绝"的现象）。
func TestDenyRule_ReadFile_NoHumanPrompt_SubAgentPath(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(target, []byte("# Whale Code\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"]},"priorityMode":"posix"}`)

	probe, confirmCalls := newConfirmProbe()
	exec.AgentConfirmFn = probe // 与 lifecycle.go:419 同位置

	agent := NewAgentContext(exec, "读取 README 第一行", "读取README第一行", workDir, 1)

	out, err := agent.executeToolCall(context.Background(), "read_file",
		json.RawMessage(fmt.Sprintf(`{"path":%q,"limit":1}`, target)))

	if confirmCalls() != 0 {
		t.Fatalf("deny 规则命中时子代理不得请求人工确认，实际确认通道被调用 %d 次", confirmCalls())
	}
	if err == nil {
		t.Fatalf("deny 规则命中时子代理必须失败，实际返回 output=%q", out)
	}
	if !strings.Contains(err.Error(), "blocked by explicit deny rule") {
		t.Fatalf("失败理由应来自规则前置门，实际 err=%v", err)
	}
}

// TestDenyRule_BashRm_NoHumanPrompt_SubAgentPath 覆盖 T-F 子代理路径（rm -rf）：
// 这是用户实际观察到"弹了确认框才被拒"的场景（T-F 结果：
// "tool bash rejected by user (sub-agent confirmation)" —— 该文案来自 agent.go:855，
// 只有 pr.NeedConfirm==true 才会走到，是"允许后才被拒"的直接证据）。
func TestDenyRule_BashRm_NoHumanPrompt_SubAgentPath(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir,
		`{"permissions":{"deny":["bash(rm:*)","read_file(*)"]},"priorityMode":"posix"}`)

	probe, confirmCalls := newConfirmProbe()
	exec.AgentConfirmFn = probe

	agent := NewAgentContext(exec, "删除 /tmp/abc", "删除目录", workDir, 1)

	out, err := agent.executeToolCall(context.Background(), "bash",
		json.RawMessage(`{"command":"rm -rf /tmp/abc"}`))

	if confirmCalls() != 0 {
		t.Fatalf("deny 规则命中时子代理不得弹确认框，实际确认通道被调用 %d 次 —— "+
			"这正是用户报告的「先要人工允许、再被权限拒绝」", confirmCalls())
	}
	if err == nil {
		t.Fatalf("deny 规则命中时子代理必须失败，实际返回 output=%q", out)
	}
	if !strings.Contains(err.Error(), "blocked by explicit deny rule") {
		t.Fatalf("失败理由应来自规则前置门，实际 err=%v", err)
	}
}

// 阳性对照（adversarial positive control）：去掉 deny 规则后，子代理的 rm -rf
// 必须仍然请求人工确认。用于证明上面的探针"测得着"——不是因为它永远返回 0。
func TestBashRm_NoRule_StillAsksHuman_SubAgentPath(t *testing.T) {
	workDir := t.TempDir()
	exec := newProductionExecutor(t, workDir, `{"permissions":{}}`)

	probe, confirmCalls := newConfirmProbe()
	exec.AgentConfirmFn = probe

	agent := NewAgentContext(exec, "删除 /tmp/abc", "删除目录", workDir, 1)

	_, _ = agent.executeToolCall(context.Background(), "bash",
		json.RawMessage(`{"command":"rm -rf /tmp/abc"}`))

	if confirmCalls() == 0 {
		t.Fatal("阳性对照失败：无规则时破坏性命令仍应请求人工确认，探针未触发")
	}
}
