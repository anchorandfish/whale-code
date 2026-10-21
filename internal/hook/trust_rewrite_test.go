package hook

// PR-A2 的 TDD 红测：三条拍板的落地证据。
//
// 拍板依据（2026-09-10 用户）：
// ① 不加自动迁移：未信任不运行 + 启动提示 + 一键信任命令（不自动执行）。
// ② 插件 hook：**安装即信任"运行"**，但 Q4 改写输入/输出特权**仍默认关**、需单独授权。
// ③ TUI 按需提示：只与 RunTrustSummary() 对齐；无未信任/无变更 → 不打扰。
//
// production-path-tdd 约束：
// - 红测从生产真实入口进：NewManager + LoadConfig(真实 hooks.json) / SetConfig(插件注入形态) + RunPreHooks。
// - 先断言「hook 确实跑了」（sentinel 落地），否则「改写被剥离」与「hook 根本没跑」无法区分 —— 这正是
// 上一轮踩过的假绿陷阱。
// - 插件 hook 的生产形态照抄 tools/executor.go:1031 injectPluginHooks：HookCommand + Source=SourcePlugin + **无 ID**。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginPreHookJSON 插件 hook 的生产注入形态：无 ID、Source=plugin、Pattern="*"、命令型。
// 照抄 tools/executor.go:1031-1039 injectPluginHooks。
func pluginPreHookJSON(t *testing.T, m *Manager, matcher, command string) {
	t.Helper()
	m.SetConfig(HookConfig{
		PreToolUse: []Hook{
			&HookCommand{
				HookBase: HookBase{Matcher: matcher, Pattern: "*", Action: ActionAllow, Source: SourcePlugin, Enabled: true},
				Command:  command,
			},
		},
	})
}

// T6 · ② 拍板：插件 hook 安装即信任"运行" —— 无任何信任记录也必须能跑。
// 生产链：EnableAllPlugins → ReinjectPluginHooks → injectPluginHooks → SetConfig（executor.go:1024-1049）。
// 注意：插件 hook 带 Pattern="*"，因此必须用**带 path 的入参**触发（见 T13 钉死的现状）。
func TestRewriteGate_PluginHookRunsWithoutTrust(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")

	m := newGatedManager(t, dir)
	pluginPreHookJSON(t, m, "Read", "echo ran > "+sentinel)

	res := m.RunPreHooks("Read", `{"path":"/tmp/target"}`)
	t.Logf("diagnostic(T6): action=%v reason=%q messages=%v", res.Action, res.Reason, res.Messages)

	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("plugin hook must be trusted-for-run on install, but it did not run: %v", err)
	}
}

// T7 · ② 拍板：运行信任 ≠ 改写授权。已信任的 config hook 默认**不得**改写工具输入。
func TestRewriteGate_TrustedHookRewriteStrippedByDefault(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")
	// 命令先落地 sentinel（证明 hook 真的执行了），再在 stdout 上输出改写指令。
	// 注意：JSON 里**不能带逗号** —— `{a,b}` 会命中 sandbox 的 BRACE-EXP 检查（security_checks.go:276），
	// 导致 hook 被安全门拦下、根本跑不起来，测试就会退化成假绿。
	cfgPath := writeHooksJSON(t, dir, `echo ran > `+sentinel+` && echo '{"updatedInput":"REWRITTEN"}'`)

	m := newGatedManager(t, dir)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireLoaded(t, m)
	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}

	res := m.RunPreHooks("bash", `{"command":"original"}`)
	t.Logf("diagnostic(T7): action=%v updatedInput=%v messages=%v", res.Action, res.UpdatedInput, res.Messages)

	// 前置：hook 确实跑了（否则「未剥离」与「没跑」不可区分）
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("precondition failed: trusted hook did not run: %v", err)
	}
	if res.UpdatedInput != nil {
		t.Errorf("FAIL: trusted-but-unauthorized hook rewrote tool input (%q). Rewrite must be opt-in.", *res.UpdatedInput)
	}
}

// T8 · ② 拍板：Post 侧同理 —— 默认不得改写喂回模型的工具输出。
func TestRewriteGate_TrustedPostHookModifiedStrippedByDefault(t *testing.T) {
	dir := t.TempDir()
	m := newGatedManager(t, dir)
	m.SetConfig(HookConfig{
		PostBash: []Hook{
			&HookCommand{HookBase: HookBase{ID: "post-hook", Matcher: "bash", Action: ActionAllow, Enabled: true}, Command: `echo REPLACED_OUTPUT`},
		},
	})
	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}

	res := m.RunPostBashHooks("echo hi", "original output")
	t.Logf("diagnostic(T8): action=%v modified=%v output=%q messages=%v", res.Action, res.Modified, res.Output, res.Messages)

	if res.Output == "" {
		t.Fatalf("precondition failed: post hook did not run (empty output)")
	}
	if res.Modified != nil {
		t.Errorf("FAIL: trusted-but-unauthorized post hook rewrote model-visible output (%q). Rewrite must be opt-in.", *res.Modified)
	}
}

// T9 · ② 拍板：显式授权后，改写必须恢复（防「一律禁掉改写」的过度拦截）。
func TestRewriteGate_GrantedRewriteIsApplied(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")
	cfgPath := writeHooksJSON(t, dir, `echo ran > `+sentinel+` && echo '{"updatedInput":"REWRITTEN"}'`)

	m := newGatedManager(t, dir)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireLoaded(t, m)
	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}
	if err := m.GrantRewriteByID("audit-hook"); err != nil {
		t.Fatalf("GrantRewriteByID: %v", err)
	}

	res := m.RunPreHooks("bash", `{"command":"original"}`)
	if res.UpdatedInput == nil {
		t.Errorf("granted hook must be able to rewrite input, but UpdatedInput was stripped: messages=%v", res.Messages)
	}
}

// T10 · ② 拍板：插件 hook 即使可运行，改写特权**同样**默认关。
func TestRewriteGate_PluginHookRewriteAlsoStripped(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")

	m := newGatedManager(t, dir)
	pluginPreHookJSON(t, m, "Read", `echo ran > `+sentinel+` && echo '{"updatedInput":"REWRITTEN"}'`)

	res := m.RunPreHooks("Read", `{"path":"/tmp/target"}`)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("precondition failed: plugin hook did not run: %v", err)
	}
	if res.UpdatedInput != nil {
		t.Errorf("FAIL: plugin hook rewrote input without explicit authorization (%q)", *res.UpdatedInput)
	}
}

// T13 · 表征（钉死现状，非本 PR 修复目标）：插件 hook 带 Pattern="*"，
// 而无 path 的工具入参（如 bash）会让 RunPreHooks 的路径守卫直接跳过它 ——
//
//	lifecycle_hook.go:27 if b.Pattern != "" && filePath == "" { continue }
//
// 生产插件 hook 正是 Pattern="*"（executor.go:1034），所以插件 PreToolUse hook
// 对 bash 这类无 path 的工具**不会触发**。这是 S19/S7 交叉的既有缺陷，
// 本 PR 不改（避免越域），仅钉死以便下轮裁决。
func TestChar_PluginHookWithPatternSkippedForPathlessInput(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")

	m := newGatedManager(t, dir)
	pluginPreHookJSON(t, m, "bash", "echo ran > "+sentinel)

	m.RunPreHooks("bash", `{"command":"echo hi"}`)

	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("characterization stale: plugin hook with Pattern=* now DOES run for pathless input — update docs/tests")
	}
}

// T14 · 安全护栏：授予改写特权**不得**顺带把未信任的 hook 变成"可运行"。
//
// 为什么单列一条：信任记录既是"可运行"的凭证，又被改写授权复用为键。
// 如果 GrantRewrite 顺手创建记录，就等于提供了「用 allow-rewrite 绕过 review 门」的后门 ——
// 那会直接推翻 ① 拍板（未信任不运行）。这条钉死该不变量。
func TestRewriteGate_GrantDoesNotImplicitlyTrust(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeHooksJSON(t, dir, "echo hi")

	m := newGatedManager(t, dir)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireLoaded(t, m)
	h := m.Config().PreToolUse[0]

	// 未信任状态下尝试全量授权改写
	if err := m.GrantRewriteAll(); err != nil {
		t.Logf("GrantRewriteAll returned: %v", err) // 允许报错，但不允许静默提权
	}

	if st := m.TrustStatus(h); st == TrustTrusted {
		t.Errorf("SECURITY: GrantRewriteAll implicitly trusted an unreviewed hook (status=%s) — review gate bypassed", st)
	}
	if m.RewriteGranted(h) {
		t.Errorf("SECURITY: unreviewed hook got rewrite capability via GrantRewriteAll")
	}
}

// T11 · ① / ③ 拍板：启动提示按需 —— 无待审事项时**必须为空**（无变更不打扰）。
func TestTrustNotice_QuietWhenNothingPending(t *testing.T) {
	dir := t.TempDir()

	// a) 完全没有 hook
	m := newGatedManager(t, dir)
	if s := m.RunTrustSummary(); s != "" {
		t.Errorf("no hooks => no notice, got %q", s)
	}

	// b) 有 hook 但已全部信任
	cfgPath := writeHooksJSON(t, dir, "echo hi")
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}
	if s := m.RunTrustSummary(); s != "" {
		t.Errorf("all trusted => no notice, got %q", s)
	}

	// c) 有未信任 hook → 必须出现，且带一键信任命令
	m2 := newGatedManager(t, t.TempDir())
	cfgPath2 := writeHooksJSON(t, t.TempDir(), "echo hi")
	if err := m2.LoadConfig(cfgPath2); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	s := m2.RunTrustSummary()
	if s == "" {
		t.Fatalf("untrusted hook => notice expected, got empty")
	}
	if !strings.Contains(s, "1 hook") {
		t.Errorf("notice should state the count (1 hook), got %q", s)
	}
	if !strings.Contains(s, "/hooks trust all") {
		t.Errorf("notice should name the one-shot trust command, got %q", s)
	}
}

// T12 · ② 拍板：改写授权默认关闭，且**内容变更后失效**（指纹语义）。
func TestRewriteGate_GrantIsFingerprintBound(t *testing.T) {
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
	if err := m.GrantRewriteByID("audit-hook"); err != nil {
		t.Fatalf("GrantRewriteByID: %v", err)
	}
	if !m.RewriteGranted(m.Config().PreToolUse[0]) {
		t.Fatalf("precondition failed: rewrite should be granted right after GrantRewriteByID")
	}

	// 内容被改 → 指纹变化 → 授权自动失效（必须重新授权）
	//
	// 注意 LoadConfig 是**追加合并**模式（config_loader.go:161 "追加合并模式"），
	// 重新加载后新 hook 追加在列表末尾，index 0 仍是旧 hook —— 必须取末尾那条断言。
	writeHooksJSON(t, dir, "echo ran2 > "+sentinel)
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig (modified): %v", err)
	}
	hooks := m.Config().PreToolUse
	if len(hooks) < 2 {
		t.Fatalf("precondition failed: LoadConfig should append the modified hook, len = %d", len(hooks))
	}
	newHook := hooks[len(hooks)-1]
	if m.TrustStatus(newHook) == TrustTrusted {
		t.Fatalf("precondition failed: modified hook should not be trusted yet")
	}
	if m.RewriteGranted(newHook) {
		t.Errorf("FAIL: rewrite grant survived a content change; it must be fingerprint-bound")
	}
}
