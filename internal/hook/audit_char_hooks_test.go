// Package hook — Hooks 域审计 characterization 测试（AUDIT_ROADMAP §3 Snapshot）
//
// 目的：在不动任何生产源码的前提下，钉死 whale-code 当前 Hooks 子系统在
// I8（外部代码独立信任域）/ I10（agent 不自提权 + 抗间接注入）/ I2（默认拒绝）三个
// 不变量上的真实行为。这些测试必须在改码前全绿 —— 绿了才叫"钉住了"。
//
// 对照基准（同类 CLI 源码，见 Hooks_STATUS.md Anchor 段）：
// - 同类 CLI 有 startup_hooks_review + "Hooks can run outside the sandbox after you trust them"
// 的信任评审门；whale-code 无对应信任门。
// - 同类 CLI 区分 FailedContinue / FailedAbort；whale-code 的 hook 出错走 ActionWarn→放行。
//
// 本文件只新增测试，不修改任何非 _test.go 源码；运行后 `git diff`（或工作树比对）应仅多出本文件。
package hook

import (
	"os"
	"strings"
	"testing"
)

// TestChar_HookCommand_DefaultUnsandboxed 钉死：未调用 SetSandboxManager 时，
// 命令 hook 以 agent 权限直接执行（沙箱默认 disabled，不隔离）。
// 这是 I8 信任域的核心事实：hook 默认跑在 agent 同一信任域，无独立隔离。
func TestChar_HookCommand_DefaultUnsandboxed(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{HookBase: HookBase{Action: ActionAllow}, Command: "echo unsandboxed"}
	res := m.executeCommandHook(h, HookPayload{}, EventPreToolUse)
	if res.Action != ActionAllow {
		t.Errorf("expected Allow, got %v (err=%v)", res.Action, res.Error)
	}
	if !strings.Contains(res.Output, "unsandboxed") {
		t.Errorf("command did not actually run; output=%q", res.Output)
	}
}

// TestChar_PreToolUse_HookRewritesInput 钉死 I10：PreToolUse hook 可通过 stdout 返回
// {"updatedInput":"..."} 改写工具入参；该值最终经 pipeline.go 替换 normalizedCall.Input。
// 注意：updatedInput 是 json.RawMessage，经 HookJSONControl.UpdatedInput 原样传递，
// 因此实际拿到的 UpdatedInput 是带 JSON 引号的字符串（"INJECTED" 而非 INJECTED）。
// 即：外部/config 控制的 hook 代码可操纵 agent 下一次工具调用的真实输入。
func TestChar_PreToolUse_HookRewritesInput(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetConfig(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "audit_tool", Action: ActionAllow}, Command: `echo '{"updatedInput":"INJECTED"}'`},
		},
	})
	res := m.RunPreHooks("audit_tool", `{"path":"/tmp/orig"}`)
	if res.UpdatedInput == nil {
		t.Fatalf("expected UpdatedInput set by hook, but nil (hook may not have matched/run)")
	}
	// json.RawMessage 保留原 JSON 编码 → 期望值含引号
	if *res.UpdatedInput != `"INJECTED"` {
		t.Errorf("UpdatedInput = %q, want %q", *res.UpdatedInput, `"INJECTED"`)
	}
}

// TestChar_PostBash_HookRewritesOutput 钉死 I10：PostBash/PostToolUse hook 的 stdout
// 经 result.Modified 回写，最终替换工具结果输出（pipeline.go: state.Result.Output = *result.Modified）。
// 即：hook 可改写喂回模型上下文的工具输出 → 间接注入面。
func TestChar_PostBash_HookRewritesOutput(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetConfig(HookConfig{
		PostBash: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "bash", Action: ActionAllow}, Command: `echo REPLACED_OUTPUT`},
		},
	})
	res := m.RunPostBashHooks("echo hi", "original output")
	if res.Modified == nil {
		t.Fatalf("expected Modified set by hook, but nil")
	}
	if *res.Modified != "REPLACED_OUTPUT\n" {
		t.Errorf("Modified = %q, want \"REPLACED_OUTPUT\\n\"", *res.Modified)
	}
}

// TestChar_PreToolUse_HookDenyViaStdoutBlocks 钉死：PreToolUse hook 通过 stdout
// {"decision":"deny"} 可阻断工具调用（命令成功执行但显式 deny）。
func TestChar_PreToolUse_HookDenyViaStdoutBlocks(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{HookBase: HookBase{Action: ActionAllow}, Command: `echo '{"decision":"deny","message":"nope"}'`}
	res := m.executeCommandHook(h, HookPayload{}, EventPreToolUse)
	if res.Action != ActionBlock {
		t.Errorf("expected ActionBlock, got %v", res.Action)
	}
}

// TestChar_ExecuteCommandHook_NonZeroExitIsWarn 钉死 I2（失败语义）：
// 命令 hook 以非 2 的非零码退出时，结果是 ActionWarn 而非 Block。
//
// ⚠️ 夹具更正（2026-09-22 · P-015 **触发暴露**，A/B 实测：修前 PASS / 修后 FAIL）：
// 原夹具是 `false` —— 它此前能通过 hook 的安全门**只因 P-015 的缺陷**：
// `false` 被判只读是因为它含 `ls` 两个字母（f-a-**ls**-e），并非因为它真的只读。
// 修掉裸子串匹配后 `false` 属破坏性档 ⇒ hook 走 `Ask ⇒ block`（无交互能力，fail-closed）
// ⇒ 本用例没跑到"非零退出"那条路。故换成语义等价的夹具：`head <缺文件>` 是真只读命令
// （`head` 在只读表内），退出码 **1**（≠2）—— 与 `false` 的行为契约逐字相同。
func TestChar_ExecuteCommandHook_NonZeroExitIsWarn(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{HookBase: HookBase{Action: ActionAllow}, Command: "head /nonexistent-hook-fixture"}
	res := m.executeCommandHook(h, HookPayload{}, EventPreToolUse)
	if res.Action != ActionWarn {
		t.Errorf("expected ActionWarn for non-zero exit, got %v (err=%v)", res.Action, res.Error)
	}
}

// TestChar_PreToolUse_HookErrorFailOpen 钉死 I2（作为策略门的失败语义）：
// 当 PreToolUse hook 自身执行失败（exit!=0），RunPreHooks 返回非 Block → 工具照常放行（fail-open）。
//
// ⚠️ 夹具同 TestChar_ExecuteCommandHook_NonZeroExitIsWarn（原 `false` 靠 P-015 缺陷过关）。
func TestChar_PreToolUse_HookErrorFailOpen(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetConfig(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{HookBase: HookBase{Matcher: "audit_tool", Action: ActionAllow}, Command: "head /nonexistent-hook-fixture"},
		},
	})
	res := m.RunPreHooks("audit_tool", `{}`)
	if res.Action == ActionBlock {
		t.Fatalf("unexpected Block on hook error; this would be fail-closed. got reason=%q", res.Reason)
	}
	t.Logf("hook error => action=%v (current fail-open behavior characterized)", res.Action)
}

// TestChar_LifecycleEvent_DenyIgnored 钉死：生命周期事件（如 SessionStop）忽略 deny，
// 纯阻断型 hook 在该类事件上被强制放行（与两家同类 CLI 行为一致，防阻断关停）。
func TestChar_LifecycleEvent_DenyIgnored(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{HookBase: HookBase{Action: ActionBlock, Reason: "should be ignored"}}
	res := m.ExecuteHookWithPayload(h, HookPayload{}, EventSessionStop)
	if res.Action != ActionAllow {
		t.Errorf("lifecycle deny should be ignored, got %v (reason=%q)", res.Action, res.Reason)
	}
}

// TestChar_Hooks_NoTrustGate_AcceptsAndRunsUserHook 钉死 I8（信任门缺失）：
// LoadConfigFromBytes 接收任意用户 hook 配置并立即可执行，全程无任何信任评审/
// 哈希/来源确认步骤（对照同类 CLI startup_hooks_review 的显式 trust 门）。
// 同时证明 hook 以 agent 真实环境运行（能看到 agent 的 $HOME，无信任域隔离）。
func TestChar_Hooks_NoTrustGate_AcceptsAndRunsUserHook(t *testing.T) {
	m := NewManager(t.TempDir())
	cfgJSON := `{"pre_tool_use":[{"type":"command","matcher":"audit_tool","command":"echo $HOME","action":"allow"}]}`
	if err := m.LoadConfigFromBytes([]byte(cfgJSON)); err != nil {
		t.Fatalf("LoadConfigFromBytes unexpected error: %v", err)
	}
	if got := len(m.Config().PreToolUse); got != 1 {
		t.Fatalf("expected 1 hook loaded (no trust gate should filter), got %d", got)
	}
	res := m.RunPreHooks("audit_tool", `{}`)
	if res.Action == ActionBlock {
		t.Fatalf("hook unexpectedly blocked: %v", res.Reason)
	}
	if !strings.Contains(res.Output, os.Getenv("HOME")) {
		t.Errorf("hook did not run in agent env (no trust-domain isolation): output=%q home=%q",
			res.Output, os.Getenv("HOME"))
	}
}

// TestChar_AddHook_SourceIsConfig 钉死 I8 来源标记：AddHook 将 hook 来源标为 SourceConfig，
// 现有来源追踪机制可用（但注意：来源仅为标签，无对应的信任/权限差异对待）。
func TestChar_AddHook_SourceIsConfig(t *testing.T) {
	m := NewManager(t.TempDir())
	h := &HookCommand{HookBase: HookBase{Matcher: "audit_tool", Action: ActionAllow}, Command: "echo hi"}
	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook error: %v", err)
	}
	cfg := m.Config()
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.PreToolUse))
	}
	if cfg.PreToolUse[0].Base().Source != SourceConfig {
		t.Errorf("expected SourceConfig, got %q", cfg.PreToolUse[0].Base().Source)
	}
}
