package hook

// =============================================================================
// S19 外部代码信任域（插件 hook 的信任范围）— 表征测试（characterization）
//
// 日期：2026-09-10 域：S19（AUDIT_ROADMAP.md §2）；与 S7 共享 hook 包但视角不同：
// S7 审「hook 有没有信任门」；S19 审「外部来源（plugin）的 hook 在该门里是否有独立身份」。
//
// 钉死的当前行为：
// H1. hookFingerprint（信任键）**不覆盖 Source / ID** → 同内容的不同 hook 共享信任记录。
// H2. 「Trust all」把尚无信任记录的 hook 写入信任库。
//
// ⚠️ 2026-09-10 重大更新（S7 Hooks 域拍板 ②：插件 hook **安装即信任"运行"**）：
// 原文的 H1/H2 都建立在「插件 hook 未信任前是 Untrusted」这一前提上，该前提已被 ② 推翻。
// 两条用例已按新语义改写，并各自标注了被推翻的原断言（便于回溯 S19 的结论变化）。
// 残留护栏：运行信任 ≠ 改写授权 —— 插件 hook 的 `updatedInput` / `Modified` 仍默认关。
//
// ⚠️ 信任库一律指向 t.TempDir()，不触碰真实 ~/.whale/hooks.trust.json。
// =============================================================================

import (
	"path/filepath"
	"testing"
)

// newS19HookManager 返回信任库已隔离的 Manager。
//
// 注意：**开启信任门**（EnableTrustGate）—— 生产路径（Executor.InitPipelineWithHooks）就是这么做的，
// 且改写特权的判定只在门开启时生效（门关闭 = 兼容直通，恒 true）。S19 审的就是"门里的插件身份"，
// 不开门就没有讨论对象。
func newS19HookManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	m.SetTrustStorePath(filepath.Join(t.TempDir(), "hooks.trust.json"))
	m.EnableTrustGate()
	return m
}

// hmmPluginHookShape 构造生产里插件 hook 的注入形态（照抄 tools/executor.go injectPluginHooks）：
// 无 ID、Action=allow、Source=plugin、Type 未设置（默认 command）。
func hmmPluginHookShape(command string) *HookCommand {
	return &HookCommand{
		HookBase: HookBase{
			Matcher: "bash",
			Pattern: "*",
			Action:  ActionAllow,
			Source:  SourcePlugin,
		},
		Command: command,
	}
}

// H1 — ⚠️ 2026-09-15 **有意翻转**（G-E 修复轮 · B5 = S19-H-E · E3）：
// 本用例原名 `TestCharS19_HookTrust_NotScopedBySource_CrossSourceTransfer`，钉的是
// 「信任键 = 内容指纹，**不含** ID/来源 ⇒ 同内容的不同 hook 共享信任记录」。
// B5 拍板 = **把来源身份并入信任键**（对齐业内既有实现 `PluginHookSource{plugin_id, ...}` 的溯源姿态），
// 因此该断言**整体反向**：
// - 旧：信任 cfg-1 后，同内容不同 ID 的 cfg-2 **自动 Trusted**（跨身份继承）
// - 新：同内容不同 ID ⇒ 指纹不同 ⇒ **不得继承信任**（必须各自 review）
//
// 翻转依据：集中拍板_决策表 §2.2 B5（"E2 + E3：注入 plugin_id 且并入 hookFingerprint"）。
// 副作用已登记：指纹算法变更 ⇒ 既有 ~/.whale/hooks.trust.json 记录失配，用户需重新 `/hooks trust all`。
func TestCharS19_HookTrust_ScopedBySourceAndID(t *testing.T) {
	m := newS19HookManager(t)

	const cmd = "echo s19-shared-command"

	cfgHook := &HookCommand{
		HookBase: HookBase{ID: "cfg-1", Matcher: "bash", Pattern: "*", Action: ActionAllow, Enabled: true},
		Command:  cmd,
	}
	if err := m.AddHook("PreToolUse", cfgHook); err != nil {
		t.Fatalf("AddHook: %v", err)
	}

	// 用户显式信任 config hook（/hooks trust <id>）
	if err := m.TrustHookByID("cfg-1"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}

	// 同内容、不同 ID 的另一条 config hook —— 指纹含 ID ⇒ 不共享信任记录（B5 新语义）
	otherCfgHook := &HookCommand{
		HookBase: HookBase{ID: "cfg-2", Matcher: "bash", Pattern: "*", Action: ActionAllow, Enabled: true},
		Command:  cmd,
	}
	if st := m.TrustStatus(otherCfgHook); st == TrustTrusted {
		t.Fatalf("B5-E3：信任键含 ID，同内容不同 ID 的 hook **不得**自动继承信任，实际 %s", st)
	}

	// 反向对照（排除假绿）：**同一身份 + 同一内容** 必须仍是 Trusted ——
	// 否则信任门就成了"永不通过"，本用例就失去判别性。
	if st := m.TrustStatus(cfgHook); st != TrustTrusted {
		t.Fatalf("同身份的 hook 应命中信任记录，实际 %s", st)
	}
}

// H1b — ② 拍板：插件 hook **安装即信任"运行"**，不依赖任何信任记录。
func TestCharS19_PluginHook_RunTrustedByPolicy(t *testing.T) {
	m := newS19HookManager(t)

	if st := m.TrustStatus(hmmPluginHookShape("echo s19-plugin")); st != TrustTrusted {
		t.Fatalf("② 拍板：插件 hook 应安装即信任运行，实际 %s", st)
	}
	// 护栏：运行信任 ≠ 改写授权（Q4 仍默认关）。
	if m.RewriteGranted(hmmPluginHookShape("echo s19-plugin")) {
		t.Fatal("② 拍板：插件 hook 的改写特权必须默认关闭")
	}
}

// H2 — 「Trust all」扫入无 ID 的插件 hook；此后任何同内容插件 hook 均命中信任记录。
func TestCharS19_HookTrust_TrustAllSweepsPluginHooksWithoutID(t *testing.T) {
	m := newS19HookManager(t)

	const cmd = "echo s19-swept"
	pluginHook := hmmPluginHookShape(cmd)

	// 注入侧：把插件 hook 塞进 Manager（模拟 SetConfig/注入路径）
	cfg := m.Config()
	cfg.PreToolUse = append(cfg.PreToolUse, pluginHook)
	m.SetConfig(cfg)

	// ⚠️ 2026-09-10 更新（S7 决策 ②）：原文断言「信任前为 Untrusted、Trust all 后才 Trusted」。
	// 该前提已被 ② 拍板推翻 —— 插件 hook 安装即信任"运行"，与是否执行过 Trust all 无关。
	// 断言改为：**无需任何信任动作**即 Trusted；Trust all 不改变这一结论（幂等）。
	if st := m.TrustStatus(pluginHook); st != TrustTrusted {
		t.Fatalf("② 拍板：插件 hook 安装即信任运行（无需记录），实际 %s", st)
	}

	if err := m.TrustAllPending(); err != nil {
		t.Fatalf("TrustAllPending: %v", err)
	}

	if st := m.TrustStatus(pluginHook); st != TrustTrusted {
		t.Fatalf("Trust all 后插件 hook 仍应为 Trusted，实际 %s", st)
	}
	// 同内容、无 ID 的插件 hook 同样 Trusted（现在由 ② 策略保证，而非信任记录）
	if st := m.TrustStatus(hmmPluginHookShape(cmd)); st != TrustTrusted {
		t.Fatalf("② 拍板：同内容插件 hook 亦应 Trusted，实际 %s", st)
	}
	// 关键残留：Trust all **不会**顺带授予改写特权（Q4 仍默认关）。
	if m.RewriteGranted(pluginHook) {
		t.Fatal("② 拍板：Trust all 不得顺带打开插件 hook 的改写特权")
	}
}
