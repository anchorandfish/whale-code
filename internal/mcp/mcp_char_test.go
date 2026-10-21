package mcp

// 工具执行MCP域 · characterization + Fix 验证（AUDIT_ROADMAP §3 / S6）。
//
// 命名约定：
//
//	TestChar_* —— 钉住"当前仍成立的行为"（含负面对照）；
//	TestFix_*  —— ④/G3 修复后应成立的行为。
//
// 生产路径级别的验证（沙箱接线 + 注册名）在 internal/tools/mcp_audit_fix_test.go，
// 从 DiscoverAndRegisterMCPTools 真实入口进入。

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// TestChar_MCPStdioRunsUnsandboxedWhenManagerNil 是 I8 的负面对照（characterization）。
//
// 仍然成立的行为（也是 transport API 的既有契约）：当 sbMgr == nil 时，
// NewStdioTransport 回退 os.Environ()（宿主完整环境，含 *_API_KEY/*_TOKEN）
// 且不调用 OSSandbox.Restrict（无 OS 级隔离）。
//
// 保留它的意义：这正是"生产绝不能传 nil"的证据。生产侧由
// Executor.mcpSandboxManager 保证永不返回 nil（G1 决策），
// 端到端断言见 tools 包的 TestAudit_MCP_SandboxEngagedAndQualifiedName。
func TestChar_MCPStdioRunsUnsandboxedWhenManagerNil(t *testing.T) {
	// "true" is a no-op that exists on every POSIX host; we only need the process
	// to start so the transport populates cmd.Env before Close().
	tr, err := NewStdioTransport(context.Background(), "true", nil, nil, nil, sandbox.SandboxLevelRelaxed)
	if err != nil {
		t.Skipf("cannot start 'true' on this host, skipping env probe: %v", err)
		return
	}
	defer func() { _ = tr.Close() }()

	hostEnv := os.Environ()
	if len(hostEnv) == 0 {
		t.Skip("host environment empty, cannot characterize")
		return
	}

	// PINNED CURRENT BEHAVIOR: nil manager → subprocess env is the full host env.
	missing := 0
	for _, kv := range hostEnv {
		found := false
		for _, got := range tr.cmd.Env {
			if got == kv {
				found = true
				break
			}
		}
		if !found {
			missing++
		}
	}
	if missing > 0 {
		t.Fatalf("unexpected: MCP subprocess env is NOT the full host env (missing %d vars) — nil-manager fallback changed?", missing)
	}
	t.Logf("PINNED I8 negative control: sbMgr=nil → env = os.Environ() (%d vars, secrets unfiltered). Production never passes nil (see mcpSandboxManager).", len(hostEnv))
}

// TestFix_MCPQualifiedNameMatchesDenyContract 钉住 ④ 修复后的 deny 契约。
//
// 修复前：动态注册用单下划线 `mcp_<server>_<tool>`，而权限 deny 契约
// （normalization.go:19,57 + permission/rules.go:167-172 的 `mcp__server` 前缀匹配）
// 用双下划线 → 按文档写的 deny 规则静默命中不了（外部工具无法被 deny）。
//
// 修复后：注册统一走 BuildMcpToolName → 全限定名可被 deny 规则命中。
func TestFix_MCPQualifiedNameMatchesDenyContract(t *testing.T) {
	qualified := BuildMcpToolName("srv", "do")
	if qualified != "mcp__srv__do" {
		t.Fatalf("BuildMcpToolName(^) = %q, want mcp__srv__do", qualified)
	}

	pol := permission.NewPolicy(permission.LevelWorkspaceWrite)
	if err := pol.AddRule(permission.PermissionRule{
		ToolName: "mcp__srv", // 文档形式：按 server 前缀 deny
		Behavior: permission.RuleDeny,
		Source:   permission.SourceUserSettings,
	}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if allowed, _ := pol.Check(qualified, ""); allowed {
		t.Fatalf("deny 规则 %q 未命中全限定名 %q", "mcp__srv", qualified)
	}
	// 反例：旧单下划线命名不会被该规则命中 —— 说明必须"对齐"而非两格式并存。
	//
	// ⚠️ 反例必须在 **danger-full-access** 档做：workspace-write 下"未定级工具"
	// 一律 fail-closed 拒绝（requiredForTool 返回最高档），旧命名也会被拒，
	// 那样测到的是模式门而不是"规则是否命中"，前置假设就失效了。
	polFull := permission.NewPolicy(permission.LevelDangerFullAccess)
	if err := polFull.AddRule(permission.PermissionRule{
		ToolName: "mcp__srv",
		Behavior: permission.RuleDeny,
		Source:   permission.SourceUserSettings,
	}); err != nil {
		t.Fatalf("AddRule(full): %v", err)
	}
	if allowed, _ := polFull.Check("mcp_srv_do", ""); !allowed {
		t.Fatalf("前置假设不成立：旧命名 mcp_srv_do 竟被 mcp__srv 命中")
	}
	if allowed, _ := polFull.Check(qualified, ""); allowed {
		t.Fatalf("deny 规则 %q 未命中全限定名 %q（danger 档下规则必须仍然生效）", "mcp__srv", qualified)
	}
}

// TestFix_MCPElicitationSwitchDefaultsOnAndGatesDispatch 钉住 G3 开关（默认开启）。
//
// 对齐业内既有实现 GranularApprovalConfig.mcp_elicitations：
// 关闭时 elicitation/create 自动 decline，不提示用户（不转发 TUI handler）。
func TestFix_MCPElicitationSwitchDefaultsOnAndGatesDispatch(t *testing.T) {
	// 默认开启：零值 Client / nil 字段 Config 都视为开启。
	if !(&Client{}).ElicitationsEnabled() {
		t.Fatal("G3: 零值 Client 的 elicitation 应默认开启")
	}
	if !(&Config{}).ElicitationsEnabled() {
		t.Fatal("G3: 未配置 elicitations 时应默认开启")
	}
	f := false
	if (&Config{Elicitations: &f}).ElicitationsEnabled() {
		t.Fatal("G3: 显式 elicitations=false 应为关闭")
	}

	params := json.RawMessage(`{"message":"need your input"}`)
	id := json.RawMessage(`"req-1"`)

	// 关闭：不转发 handler，直接 decline（fail-safe）。
	off := newClientWithTransport(&nonStreamingTransport{})
	off.elicitationRouter = NewElicitationRequestRouter()
	off.SetElicitationsEnabled(false)
	called := false
	off.SetElicitationHandler(func(string, json.RawMessage, string) { called = true })
	res, err := off.dispatchRequest("elicitation/create", params, id)
	if err != nil {
		t.Fatalf("G3: 关闭时应返回 decline 而非 error: %v", err)
	}
	if called {
		t.Fatal("G3 GAP: 开关关闭仍把 elicitation 转发给了 TUI handler")
	}
	m, ok := res.(map[string]interface{})
	if !ok || m["action"] != string(ElicitationActionDecline) {
		t.Fatalf("G3: want decline, got %#v", res)
	}

	// 开启（默认）+ handler：仍走正常 pending 路径（返回哨兵 + handler 被调用）。
	on := newClientWithTransport(&nonStreamingTransport{})
	on.elicitationRouter = NewElicitationRequestRouter()
	onCalled := false
	on.SetElicitationHandler(func(string, json.RawMessage, string) { onCalled = true })
	if _, err := on.dispatchRequest("elicitation/create", params, id); err == nil {
		t.Fatal("G3: 开启且 handler 存在时应返回 errElicitationPending")
	}
	if !onCalled {
		t.Fatal("G3: 开启时 handler 应被调用")
	}
	// 清理 pending goroutine（否则等待 300s TTL）。
	_ = on.ResolveElicitation(ElicitationResponse{RequestID: id, Action: ElicitationActionDecline})
}
