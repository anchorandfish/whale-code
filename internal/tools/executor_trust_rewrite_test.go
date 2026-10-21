package tools

// PR-A2 · ② 拍板的**生产层**证据（Q4 改写特权默认关、需单独授权）。
//
// production-path-tdd：
//   - 生产入口：InitPipelineWithHooks（tui/lifecycle.go:425 / runner_core.go:532 / bridge.go:274 汇聚于此），
//     它内部创建 hookMgr 并 EnableTrustGate()。
//   - 不 mock 任何守卫：hooks.json 真实落盘、hook 真实起子进程、改写真实经 pipeline 的
//     `normalizedCall.Input = state.UpdatedInput`（pipeline.go:201-202）落到工具 handler。
//   - 三态对照（A/B/C）才是完整证据：
//     A 未信任        → hook 根本不跑（sentinel 不出现）
//     B 仅信任"运行"  → hook 跑了（sentinel 出现）但改写被剥离（工具收到原始入参）
//     C 授予改写特权  → 工具收到改写后的入参
//     只测 B 无法区分"没跑"与"改不了"，只测 C 无法证明默认是关的。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

func TestE2E_RewritePrivilege_DefaultOff_ProductionPath(t *testing.T) {
	const toolName = "test_e2e_rw"
	tmpDir := t.TempDir()
	sentinel := filepath.Join(tmpDir, "HOOK_RAN")

	// hook 命令：先写 sentinel（证明真的执行），再把改写指令写到 stdout。
	// 两个约束：
	//   1) JSON 里不能带逗号 —— `{a,b}` 会命中 sandbox BRACE-EXP 检查
	//      （internal/sandbox/validator/security_checks.go:276）导致 hook 被安全门拦下。
	//   2) 改写值必须是**对象**（`{"command":...}`），否则替换后的工具入参过不了 schema 校验。
	hookCmd := "echo ran > " + sentinel + ` && echo '{"updatedInput":{"command":"REWRITTEN"}}'`
	cfg := map[string]interface{}{
		"pre_tool_use": []map[string]interface{}{
			{
				"id":      "rw-hook",
				"type":    "command",
				"matcher": toolName,
				"action":  "allow",
				"enabled": true,
				"command": hookCmd,
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal hooks.json: %v", err)
	}
	hooksDir := filepath.Join(tmpDir, ".whale")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), data, 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	r := NewRegistry()
	e := NewExecutor(r, tmpDir)
	r.builtin[toolName] = Spec{
		Name: toolName,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string"},
			},
		},
		RequiredPermission: LevelReadOnly,
		ConcurrencySafe:    true,
	}
	var gotInput string
	e.RegisterHandler(toolName, func(ctx context.Context, input json.RawMessage) (string, error) {
		gotInput = string(input)
		return "ok", nil
	})

	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	e.InitPipelineWithHooks(policy, tmpDir)
	if e.hookMgr == nil || !e.hookMgr.TrustGateEnabled() {
		t.Fatal("前提失效：生产接线未开启信任门")
	}
	// 信任库指向临时目录，避免污染真实 ~/.whale
	e.hookMgr.SetTrustStorePath(filepath.Join(tmpDir, "hooks.trust.json"))

	const original = `{"command":"ORIGINAL"}`
	run := func() {
		gotInput = ""
		pr := e.ExecuteWithPipeline(context.Background(), Call{
			ID: "1", Name: toolName, Input: original,
		})
		if !pr.Proceed {
			t.Fatalf("管道应通过: %s", pr.Rejection)
		}
	}

	// ---- A. 未信任：hook 不跑，工具收到原始入参 ----
	run()
	if gotInput != original {
		t.Errorf("[A untrusted] input = %q, want original %q", gotInput, original)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("[A untrusted] hook must not run at all, but sentinel exists")
	}

	// ---- B. 仅"运行"信任：hook 跑了，但改写被剥离 ----
	if err := e.hookMgr.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}
	run()
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("precondition failed: trusted hook did not run: %v", err)
	}
	if gotInput != original {
		t.Errorf("[B run-trusted only] hook rewrote tool input (%q) without authorization; rewrite must be opt-in", gotInput)
	}
	if e.hookMgr.RewriteGranted(e.hookMgr.Config().PreToolUse[0]) {
		t.Errorf("[B run-trusted only] rewrite must default to OFF")
	}

	// ---- C. 授予改写特权：改写生效 ----
	if err := e.hookMgr.GrantRewriteAll(); err != nil {
		t.Fatalf("GrantRewriteAll: %v", err)
	}
	run()
	if gotInput != `{"command":"REWRITTEN"}` {
		t.Errorf("[C rewrite granted] input = %q, want %q (hook rewrite must apply once authorized)", gotInput, `{"command":"REWRITTEN"}`)
	}
}

// TestE2E_HookTrustNotice_QuietByDefault PR-A2 · ①/③ 拍板的生产层证据：
// 启动提示**按需** —— 有未信任 hook 才出现，且给出一次性信任命令；全部信任后必须静默。
//
// 生产入口：InitPipelineWithHooks（TUI/plain/bridge 三条入口汇聚于此）→ HookTrustNotice()
// → PrintHookTrustNoticeIfAny()（打印到 fd 2，测试只断言字符串口径，不抓 fd 2）。
func TestE2E_HookTrustNotice_QuietByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, ".whale")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	cfg := `{"pre_tool_use":[{"id":"notice-hook","type":"command","matcher":"bash","action":"allow","enabled":true,"command":"echo hi"}]}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	e := NewExecutor(NewRegistry(), tmpDir)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelDangerFullAccess), tmpDir)
	if e.hookMgr == nil || !e.hookMgr.TrustGateEnabled() {
		t.Fatal("前提失效：生产接线未开启信任门")
	}
	e.hookMgr.SetTrustStorePath(filepath.Join(tmpDir, "hooks.trust.json"))

	notice := e.HookTrustNotice()
	if notice == "" {
		t.Fatal("存在未信任 hook 时应给出启动提示，实际为空")
	}
	if !strings.Contains(notice, "1 hook") {
		t.Errorf("提示应包含待审数量，实际 %q", notice)
	}
	if !strings.Contains(notice, "/hooks trust all") {
		t.Errorf("提示应包含一次性信任命令，实际 %q", notice)
	}

	if err := e.hookMgr.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}
	if again := e.HookTrustNotice(); again != "" {
		t.Errorf("全部信任后必须静默（无变更不打扰），实际 %q", again)
	}
}
