package hook

// Hooks 信任门（PR-A）的 TDD 红测。
//
// production-path-tdd 约束：
//   - R2：全部从生产真实入口进入 —— NewManager(workDir) + LoadConfig(真实 hooks.json 文件) + RunPreHooks
//     （生产链：tui/lifecycle.go:425 → executor.go:569 NewManager → executor.go:574/581 LoadConfig）
//   - R2：断言里带决策依据 —— 每个用例先断言「hook 确实被加载进 config」，
//     否则配置格式写错会导致「没有 hook 可跑」而假绿。
//   - 用户可见现象：hook 命令是否真的落了地（sentinel 文件是否被创建）。

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHooksJSON 在 dir/.whale/hooks.json 写入一个 PreToolUse command hook。
// 命令落地方式是写 sentinel 文件 —— 这是「hook 到底跑没跑」的用户可见证据。
func writeHooksJSON(t *testing.T, dir, command string) string {
	t.Helper()
	hooksDir := filepath.Join(dir, ".whale")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir .whale: %v", err)
	}
	path := filepath.Join(hooksDir, "hooks.json")
	body := `{
  "pre_tool_use": [
    {
      "id": "audit-hook",
      "type": "command",
      "matcher": "bash",
      "action": "allow",
      "enabled": true,
      "command": ` + quoteJSON(command) + `
    }
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	return path
}

func quoteJSON(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(append(out, '"'))
}

// newGatedManager 构造一个启用信任门的 Manager（信任库落在临时目录，不污染用户 home）。
func newGatedManager(t *testing.T, dir string) *Manager {
	t.Helper()
	m := NewManager(dir)
	m.SetTrustStorePath(filepath.Join(dir, "hooks.trust.json"))
	m.EnableTrustGate()
	return m
}

// requireLoaded 断言 hooks.json 确实被解析进 config（防假绿）。
func requireLoaded(t *testing.T, m *Manager) {
	t.Helper()
	if n := len(m.Config().PreToolUse); n != 1 {
		t.Fatalf("precondition failed: hooks.json not loaded as expected, PreToolUse len = %d, want 1", n)
	}
}

// T1 · 核心红测：未信任的 hook 不得运行（Q1 拍板：不信任 → 不运行）。
func TestTrustGate_UntrustedHookFromHooksJSON_DoesNotRun(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")
	cfgPath := writeHooksJSON(t, dir, "echo ran > "+sentinel)

	m := newGatedManager(t, dir)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireLoaded(t, m)

	loaded := m.Config().PreToolUse[0]
	t.Logf("diagnostic(T1): loaded type=%q cmd=%q action=%v source=%q", loaded.Base().Type, loaded.(*HookCommand).Command, loaded.Base().Action, loaded.Base().Source)
	res := m.RunPreHooks("bash", `{"command":"echo hi"}`)
	t.Logf("diagnostic(T1): action=%v reason=%q messages=%v", res.Action, res.Reason, res.Messages)

	if res.Action != ActionAllow {
		t.Errorf("skipped hook must not change the decision: Action = %v, want Allow (silent skip)", res.Action)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("FAIL: untrusted hook executed (sentinel created at %s). Gate must prevent execution.", sentinel)
	}
}

// T2 · 反向对照：信任之后 hook 必须能跑。
// 这条同时是「防过度拦截」守卫 —— 如果 T1 靠「把 hook 全废掉」转绿，这条会红。
func TestTrustGate_TrustedHookRuns(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")
	cfgPath := writeHooksJSON(t, dir, "echo ran > "+sentinel)

	m := newGatedManager(t, dir)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireLoaded(t, m)

	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}

	m.RunPreHooks("bash", `{"command":"echo hi"}`)

	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("trusted hook should run, but sentinel missing: %v", err)
	}
}

// T3 · 变更即失效：hook 命令被改后必须重新信任（同类 CLI trusted_hash 语义）。
func TestTrustGate_ModifiedHookLosesTrust(t *testing.T) {
	dir := t.TempDir()
	sentinel1 := filepath.Join(dir, "SENTINEL1")
	sentinel2 := filepath.Join(dir, "SENTINEL2")

	cfgPath := writeHooksJSON(t, dir, "echo ran > "+sentinel1)
	m := newGatedManager(t, dir)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}

	// 攻击者/用户改了 hook 命令内容，重新加载
	writeHooksJSON(t, dir, "echo ran > "+sentinel2)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig (modified): %v", err)
	}

	m.RunPreHooks("bash", `{"command":"echo hi"}`)

	if _, err := os.Stat(sentinel2); err == nil {
		t.Errorf("FAIL: modified hook executed without re-trust (sentinel2 created). Trust must be invalidated on change.")
	}
}

// T4 · AddHook 路径（CLI /hooks add）：新增的 hook 默认未信任，不得运行。
func TestTrustGate_AddHookUntrusted_DoesNotRun(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")

	m := newGatedManager(t, dir)
	h := &HookCommand{HookBase: HookBase{ID: "cli-hook", Matcher: "bash", Action: ActionAllow, Enabled: true}, Command: "echo ran > " + sentinel}
	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	requireLoaded(t, m)

	t.Logf("diagnostic(T4): cmd=%q workDir=%q", m.Config().PreToolUse[0].(*HookCommand).Command, m.workDir)
	res := m.RunPreHooks("bash", `{"command":"echo hi"}`)
	t.Logf("diagnostic(T4): action=%v reason=%q messages=%v err=%v", res.Action, res.Reason, res.Messages, res.Error)

	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("FAIL: hook added via AddHook executed without trust (sentinel created)")
	}
}

// T5 · 未启用信任门时行为不变（向后兼容，避免一次性打断既有 50+ 测试）。
func TestTrustGate_DisabledByDefault_PreservesLegacyBehavior(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")
	cfgPath := writeHooksJSON(t, dir, "echo ran > "+sentinel)

	m := NewManager(dir) // 不调用 EnableTrustGate
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireLoaded(t, m)

	res := m.RunPreHooks("bash", `{"command":"echo hi"}`)

	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("gate disabled => legacy behavior, hook should run, but sentinel missing: %v", err)
		t.Logf("diagnostic: action=%v reason=%q messages=%v err=%v", res.Action, res.Reason, res.Messages, res.Error)
	}
}
