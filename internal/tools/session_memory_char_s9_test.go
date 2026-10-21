package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// S9 characterization（会话记忆 / allow-once）—— 钉死"哪套存储在**生产路径**上真正生效"。
//
// 结论（与首轮 HANDOFF 的推断相反）：真正生效的会话审批记忆是
// **permission.Policy.memory**，不是 approval.Manager.cache。
//
// 读取点（生产）：
//   - internal/tools/pipeline.go:178 `p.policy.Has(name, call.Input)` → approved=true
//   - internal/permission/permission.go checkModeLocked 的 `p.memory.Has(...)` → needConfirm=false
//
// 写入点（生产）：TUI「本会话」→ tool_exec.go:189 / app.go:2208 / agent_confirm.go:64 Policy.Remember；
// RPC → remote/rpc/client.go:76。
//
// 入口与生产一致：Executor.InitPipelineWithHooks + ExecuteWithConfirm
// （internal/tui/tool_exec.go:executeToolsDirect 实际调用的入口）。
//
// 用自定义工具（LevelWorkspaceWrite）而非内置 bash：避免真正执行命令，也避开内置 handler 冲突。
// 本用例不改行为，仅钉住现状；跑绿即证明 Policy.memory 是生效的会话记忆。
func TestChar_S9_PolicyMemoryIsEffectiveSessionStore(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	const toolName = "s9_char_tool"
	if err := e.RegisterHandler(toolName, func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	// 注册到 Registry 以通过 Schema 校验（与 setupOnFailurePipeline 同法）
	r.builtin[toolName] = Spec{
		Name:               toolName,
		Description:        "S9 characterization tool (workspace-write)",
		InputSchema:        map[string]interface{}{"type": "object"},
		RequiredPermission: LevelWorkspaceWrite,
		ConcurrencySafe:    true,
	}

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	e.InitPipelineWithHooks(policy, tmp)
	if e.Pipeline == nil {
		t.Fatal("precondition: 生产装配下 Pipeline 必须非 nil，否则本用例不能代表生产路径")
	}

	input := `{"path":"/tmp/s9char/victim.txt"}`

	// 1) 无记忆：write 级别工具在 workspace-write 下必须请求确认
	without := e.ExecuteWithConfirm(context.Background(), Call{ID: "c1", Name: toolName, Input: input})
	if !without.NeedConfirm {
		t.Fatalf("baseline: 无记忆时应请求确认; stageLogs=%+v", without.StageLogs)
	}

	// 2) 写入会话记忆（等同 TUI 选「本会话」→ Policy.Remember）
	policy.Remember(toolName, input, true)

	// 3) 有记忆：应免除确认 —— 证明 Policy.memory 在 pipeline 读取点生效
	with := e.ExecuteWithConfirm(context.Background(), Call{ID: "c2", Name: toolName, Input: input})
	if with.NeedConfirm {
		t.Fatalf("Policy.memory 命中应免除确认（证明它是生效的会话记忆）; stageLogs=%+v", with.StageLogs)
	}
}

// S9 characterization（HG-D）：对**生效中的 Policy.memory** 而言，deny 已经"击穿"记忆——
// PermissionStage 先跑 CheckWithRules（pipeline.go:930）再走 call.Approved 快捷分支（948-951），
// 故"已记住本会话允许"遇到显式 deny 规则仍被拒绝。钉死该现状，避免 Fix 误破坏。
func TestChar_S9_MemoryDoesNotBypassDeny(t *testing.T) {
	tmp := t.TempDir()
	r := NewRegistry()
	e := NewExecutor(r, tmp)

	const toolName = "s9_char_tool"
	if err := e.RegisterHandler(toolName, func(ctx context.Context, input json.RawMessage) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	r.builtin[toolName] = Spec{
		Name:               toolName,
		Description:        "S9 deny-bypass characterization tool",
		InputSchema:        map[string]interface{}{"type": "object"},
		RequiredPermission: LevelWorkspaceWrite,
		ConcurrencySafe:    true,
	}

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	// 先记住「本会话允许」，再显式加 deny 规则
	policy.Remember(toolName, `{"path":"/tmp/x"}`, true)
	rule, err := permission.ParseRuleString(toolName, permission.RuleDeny, permission.SourceSession)
	if err != nil {
		t.Fatalf("ParseRuleString: %v", err)
	}
	if err := policy.AddRule(*rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	e.InitPipelineWithHooks(policy, tmp)
	res := e.ExecuteWithConfirm(context.Background(), Call{ID: "c3", Name: toolName, Input: `{"path":"/tmp/x"}`})
	if res.Proceed {
		t.Fatalf("deny 规则应击穿会话记忆并拒绝执行（HG-D 对 Policy.memory 已成立）; stageLogs=%+v", res.StageLogs)
	}
}
