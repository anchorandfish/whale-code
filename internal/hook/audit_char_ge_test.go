package hook

// =============================================================================
// G-E（插件 / skill / hook）修复轮 —— 决策项判据测试
//
// 覆盖的决策项（集中拍板_决策表 §2 的 G-E 行）：
// D1（S7-P0-③）未信任 hook 跳过时**不得**短路整条 PreToolUse 链
// D2（S7-P1-①）`enabled:false` 必须真的不运行（修复前是死配置）
// D3（S7-P1-②）HookResult.Messages 的结构化出口（修复前产出后零消费）
// B5（S19-H-E · E3）信任键并入来源身份（id + source）
//
// 判据形态（对齐 §硬规则 1 / 6）：
// - 每项都能在"改前"变体下**精确打红**（仓库根 `反向重建工具` 反向重建 before + `-overlay`）；
// - 另附**反向守卫**（不该翻转的行为）证明不是靠"放松断言"变绿。
//
// ⚠️ 信任库一律指向 t.TempDir()，不触碰真实 ~/.whale/hooks.trust.json。
// =============================================================================

import (
	"os"
	"path/filepath"
	"testing"
)

// geManager 返回信任门已开启、信任库隔离到临时目录的 Manager。
func geManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	m.SetTrustStorePath(filepath.Join(t.TempDir(), "hooks.trust.json"))
	m.EnableTrustGate()
	return m
}

// ---------- D1：跳过 ≠ 决策，不得短路整条链 ----------

// TestGE_D1_UntrustedSkipDoesNotShortCircuitPreToolUseChain
//
// 场景 = S7-P0-③ 的红测：[未信任 allow(带 command), 已信任 block]。
// 修复前：第一条被信任门跳过（返回 ActionAllow + 非空 Command）⇒ shouldReturnResult 为真
// ⇒ RunPreHooks 直接 return Allow，第二条已信任的 block **根本没执行**（期望拦、实际放行）。
// 修复后：跳过带 Skipped 标记 ⇒ 循环 continue ⇒ 第二条 block 生效。
func TestGE_D1_UntrustedSkipDoesNotShortCircuitPreToolUseChain(t *testing.T) {
	m := geManager(t)

	untrusted := &HookCommand{
		HookBase: HookBase{ID: "ge-untrusted", Matcher: "*", Action: ActionAllow},
		Command:  "echo i-am-not-trusted",
	}
	blocker := &HookCommand{
		HookBase: HookBase{ID: "ge-blocker", Matcher: "*", Action: ActionBlock, Reason: "deny-by-trusted-hook"},
	}
	if err := m.AddHook("PreToolUse", untrusted); err != nil {
		t.Fatalf("AddHook(untrusted): %v", err)
	}
	if err := m.AddHook("PreToolUse", blocker); err != nil {
		t.Fatalf("AddHook(blocker): %v", err)
	}
	if err := m.TrustHookByID("ge-blocker"); err != nil {
		t.Fatalf("TrustHookByID(ge-blocker): %v", err)
	}
	// 前提断言：第一条确实未信任（否则本用例测的不是 D1）
	if st := m.TrustStatus(untrusted); st == TrustTrusted {
		t.Fatalf("前提失效：第一条 hook 不应被信任，实际 %s", st)
	}

	res := m.RunPreHooks("bash", `{"command":"ls"}`)
	if res.Action != ActionBlock {
		t.Fatalf("D1 未修复：未信任 hook 的跳过短路了整条链 —— "+
			"期望已信任的 block 生效(ActionBlock)，实际 %v (reason=%q)",
			res.Action, res.Reason)
	}
	if res.Reason != "deny-by-trusted-hook" {
		t.Fatalf("拦下来了但原因不对：%q", res.Reason)
	}
}

// TestGE_D1_LifecycleChainKeepsEvaluatingAfterSkip
//
// 同一个短路根因在生命周期路径（runLifecycleHooks）上的表现。
// 观测量用 D3 的结构化出口：链里"真正被求值的 hook 个数"。
// 修复前：第一条被跳过即 return ⇒ 只产生 1 条 outcome；修复后：2 条。
func TestGE_D1_LifecycleChainKeepsEvaluatingAfterSkip(t *testing.T) {
	m := geManager(t)

	untrusted := &HookCommand{
		HookBase: HookBase{ID: "ge-lc-untrusted", Action: ActionAllow},
		Command:  "echo lc-untrusted",
	}
	second := &HookCommand{
		HookBase: HookBase{ID: "ge-lc-second", Action: ActionAllow},
		Command:  "echo lc-second",
	}
	if err := m.AddHook("Stop", untrusted); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.AddHook("Stop", second); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.TrustHookByID("ge-lc-second"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}

	m.RunStopHooks()

	ids := map[string]bool{}
	for _, o := range m.PeekOutcomes() {
		ids[o.HookID] = true
	}
	if !ids["ge-lc-second"] {
		t.Fatalf("D1 未修复：前一条未信任 hook 跳过即 return，第二条 hook 从未被求值（outcomes=%v）", ids)
	}
}

// TestGE_D1_UntrustedHookAloneStillDoesNotBlock（反向守卫：跳过语义本身没被改成 block）
func TestGE_D1_UntrustedHookAloneStillDoesNotBlock(t *testing.T) {
	m := geManager(t)
	h := &HookCommand{
		HookBase: HookBase{ID: "ge-solo-untrusted", Matcher: "*", Action: ActionBlock, Reason: "would-block"},
		Command:  "echo solo",
	}
	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	res := m.RunPreHooks("bash", `{}`)
	if res.Action == ActionBlock {
		t.Fatalf("未信任 hook 不得阻断工具调用（Q1 语义：跳过=无权参与决策），实际 %v", res.Action)
	}
	if !res.Skipped || res.SkipReason != SkipUntrusted {
		t.Fatalf("跳过必须结构化可见：Skipped=%v reason=%q", res.Skipped, res.SkipReason)
	}
}

// ---------- D2：enabled:false 必须真的不运行 ----------

// TestGE_D2_ExplicitDisabledHookDoesNotRun
//
// 修复前：`enabled:false` 是死配置 —— hook 照跑，而 `/hooks` 列表已显示 (disabled)。
// 判据用"命令是否真的落地"（sentinel 文件），而不是只看返回结构。
func TestGE_D2_ExplicitDisabledHookDoesNotRun(t *testing.T) {
	m := NewManager(t.TempDir())
	sentinel := filepath.Join(t.TempDir(), "ran.txt")

	cfg := `{"pre_tool_use":[{"id":"ge-disabled","type":"command","matcher":"*",` +
		`"action":"allow","enabled":false,"command":"echo ran > ` + sentinel + `"}]}`
	if err := m.LoadConfigFromBytes([]byte(cfg)); err != nil {
		t.Fatalf("LoadConfigFromBytes: %v", err)
	}

	hooks, err := m.GetHooksByEvent("PreToolUse")
	if err != nil || len(hooks) != 1 {
		t.Fatalf("前提失效：hooks=%d err=%v", len(hooks), err)
	}
	if hooks[0].Base().IsEnabled() {
		t.Fatalf("前提失效：配置 enabled:false 未被解析为禁用")
	}

	m.RunPreHooks("bash", `{}`)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("D2 未修复：enabled:false 的 hook 仍然执行了（死配置）—— %s 被创建", sentinel)
	}
}

// TestGE_D2_DisabledHookSkipIsStructured（跳过原因必须是结构化枚举，不是散文）
func TestGE_D2_DisabledHookSkipIsStructured(t *testing.T) {
	m := NewManager(t.TempDir())
	cfg := `{"pre_tool_use":[{"id":"ge-disabled-2","type":"command","matcher":"*",` +
		`"action":"block","enabled":false,"command":"echo nope"}]}`
	if err := m.LoadConfigFromBytes([]byte(cfg)); err != nil {
		t.Fatalf("LoadConfigFromBytes: %v", err)
	}

	res := m.RunPreHooks("bash", `{}`)
	if res.Action == ActionBlock {
		t.Fatalf("被显式禁用的 hook 不得参与决策（更不得拦），实际 %v", res.Action)
	}
	if !res.Skipped || res.SkipReason != SkipDisabled {
		t.Fatalf("禁用跳过必须结构化可见：Skipped=%v reason=%q", res.Skipped, res.SkipReason)
	}
	var found bool
	for _, o := range m.PeekOutcomes() {
		if o.SkipReason == SkipDisabled && o.HookID == "ge-disabled-2" {
			found = true
		}
	}
	if !found {
		t.Fatal("D3 出口未记录禁用跳过（PeekOutcomes 里找不到 SkipDisabled）")
	}
}

// TestGE_D2_DisabledHookDoesNotShortCircuitChain（与 D1 同类的链式回归）
func TestGE_D2_DisabledHookDoesNotShortCircuitChain(t *testing.T) {
	m := geManager(t)

	disabledCfg := `{"pre_tool_use":[{"id":"ge-disabled-3","type":"command","matcher":"*",` +
		`"action":"allow","enabled":false,"command":"echo skip-me"}]}`
	if err := m.LoadConfigFromBytes([]byte(disabledCfg)); err != nil {
		t.Fatalf("LoadConfigFromBytes: %v", err)
	}
	blocker := &HookCommand{
		HookBase: HookBase{ID: "ge-blocker-2", Matcher: "*", Action: ActionBlock, Reason: "deny-by-trusted-2"},
	}
	if err := m.AddHook("PreToolUse", blocker); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.TrustHookByID("ge-blocker-2"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}

	res := m.RunPreHooks("bash", `{}`)
	if res.Action != ActionBlock {
		t.Fatalf("被禁用的 hook 跳过时短路了链，已信任 block 未生效：action=%v reason=%q", res.Action, res.Reason)
	}
}

// TestGE_D2_AbsentEnabledFieldStillRuns（反向守卫：**未写** enabled 的配置绝不能被静默禁用）
//
// 这是 D2 最危险的实施失误：bool 零值 = false，若直接把"零值"当禁用，
// 现存所有 hooks.json（都没写这个字段）会集体停摆。本用例把它钉死。
func TestGE_D2_AbsentEnabledFieldStillRuns(t *testing.T) {
	m := NewManager(t.TempDir())
	sentinel := filepath.Join(t.TempDir(), "ran-default.txt")

	cfg := `{"pre_tool_use":[{"id":"ge-default-on","type":"command","matcher":"*",` +
		`"action":"allow","command":"echo ran > ` + sentinel + `"}]}`
	if err := m.LoadConfigFromBytes([]byte(cfg)); err != nil {
		t.Fatalf("LoadConfigFromBytes: %v", err)
	}
	hooks, _ := m.GetHooksByEvent("PreToolUse")
	if len(hooks) != 1 || !hooks[0].Base().IsEnabled() {
		t.Fatalf("未写 enabled 字段的 hook 必须默认启用（IsEnabled=%v）", hooks[0].Base().IsEnabled())
	}

	m.RunPreHooks("bash", `{}`)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("回归：未写 enabled 字段的 hook 被静默禁用了（%v）", err)
	}
}

// TestGE_D2_ProgrammaticHookDefaultsEnabled（反向守卫：程序化构造默认启用 —— 插件注入形态）
func TestGE_D2_ProgrammaticHookDefaultsEnabled(t *testing.T) {
	// 照抄 tools/executor.go injectPluginHooks 的构造形态（不设 Enabled）
	h := &HookCommand{
		HookBase: HookBase{Matcher: "bash", Pattern: "*", Action: ActionAllow, Source: SourcePlugin},
		Command:  "echo plugin-shape",
	}
	if !h.Base().IsEnabled() {
		t.Fatal("程序化构造（未设 Enabled）必须默认启用；否则插件 hook 会集体静默失效")
	}
	// 显式禁用路径必须可用（/hooks add enabled=false 的 API 面）
	h.Base().SetEnabled(false)
	if h.Base().IsEnabled() {
		t.Fatal("SetEnabled(false) 之后必须为禁用")
	}
}

// ---------- D3：结构化事实必须有出口 ----------

// TestGE_D3_SkipTimeoutAndFailureReachOutcomeSink
//
// 修复前：HookResult.Messages 有 ~25 处写入、**零消费**（生产代码无读者）——
// 跳过 / 超时 / 失败全部静默。修复后必须能在结构化出口读到，且原因/身份是机器可读字段。
func TestGE_D3_SkipTimeoutAndFailureReachOutcomeSink(t *testing.T) {
	m := geManager(t)

	// ① 未信任被跳过
	skipped := &HookCommand{
		HookBase: HookBase{ID: "ge-d3-skipped", Matcher: "*", Action: ActionAllow},
		Command:  "echo skipped",
	}
	if err := m.AddHook("PreToolUse", skipped); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	// ② 非零退出（失败）
	failing := &HookCommand{
		HookBase: HookBase{ID: "ge-d3-failing", Matcher: "*", Action: ActionAllow},
		Command:  "head /nonexistent-hook-fixture", // P-015：原 "false" 仅因"含 ls 被判只读"才过安全门（见 audit_char_hooks_test.go 说明）；本夹具是真只读命令 + 退出码 1
	}
	if err := m.AddHook("PreToolUse", failing); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.TrustHookByID("ge-d3-failing"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}

	m.RunPreHooks("bash", `{}`)

	outcomes := m.PeekOutcomes()
	var sawSkip, sawFailure bool
	for _, o := range outcomes {
		if o.HookID == "ge-d3-skipped" && o.Skipped && o.SkipReason == SkipUntrusted && len(o.Messages) > 0 {
			sawSkip = true
		}
		if o.HookID == "ge-d3-failing" && !o.Skipped && len(o.Messages) > 0 {
			sawFailure = true
		}
	}
	if !sawSkip {
		t.Fatalf("D3：未信任跳过没有到达结构化出口（outcomes=%v）", outcomes)
	}
	if !sawFailure {
		t.Fatalf("D3：hook 失败消息没有到达结构化出口（outcomes=%v）", outcomes)
	}
	if len(m.DrainOutcomes()) == 0 {
		t.Fatal("DrainOutcomes 应能取走结果")
	}
	if got := len(m.PeekOutcomes()); got != 0 {
		t.Fatalf("DrainOutcomes 后缓冲应为空，实际 %d", got)
	}
}

// TestGE_D3_SinkIsBounded（族 E 守卫：出口不能无界增长）
func TestGE_D3_SinkIsBounded(t *testing.T) {
	m := geManager(t)
	h := &HookCommand{
		HookBase: HookBase{ID: "ge-d3-bounded", Matcher: "*", Action: ActionAllow},
		Command:  "head /nonexistent-hook-fixture", // P-015：原 "false" 仅因"含 ls 被判只读"才过安全门（见 audit_char_hooks_test.go 说明）；本夹具是真只读命令 + 退出码 1
	}
	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.TrustHookByID("ge-d3-bounded"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}
	for i := 0; i < HookOutcomeBufferSize+10; i++ {
		m.RunPreHooks("bash", `{}`)
	}
	if got := len(m.PeekOutcomes()); got > HookOutcomeBufferSize {
		t.Fatalf("环形缓冲越界：%d > %d", got, HookOutcomeBufferSize)
	}
	if m.DroppedOutcomeCount() == 0 {
		t.Fatal("溢出的丢弃计数应可见（观测面，不是静默丢）")
	}
}

// ---------- B5（E3）：信任键并入来源身份 ----------

// TestGE_B5_TrustKeyIncludesIDAndSource
//
// 修复前：指纹只有内容 ⇒「信任 A 插件/cfg 的 hook」=「信任任意来源的同内容 hook」。
// 修复后：id 与 source 都在键里 ⇒ 同内容不同 ID ⇒ 不共享；同 ID 同内容不同来源 ⇒ 不共享。
func TestGE_B5_TrustKeyIncludesIDAndSource(t *testing.T) {
	m := geManager(t)

	const cmd = "echo b5-shared-content"

	cfgHook := &HookCommand{
		HookBase: HookBase{ID: "b5-cfg-1", Matcher: "bash", Pattern: "*", Action: ActionAllow},
		Command:  cmd,
	}
	if err := m.AddHook("PreToolUse", cfgHook); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.TrustHookByID("b5-cfg-1"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}

	// 同内容、不同 ID ⇒ 指纹不同 ⇒ 不得自动继承信任
	sameContentOtherID := &HookCommand{
		HookBase: HookBase{ID: "b5-cfg-2", Matcher: "bash", Pattern: "*", Action: ActionAllow},
		Command:  cmd,
	}
	if st := m.TrustStatus(sameContentOtherID); st == TrustTrusted {
		t.Fatalf("B5-E3 未修复：同内容不同 ID 的 hook 共享了信任记录（%s）", st)
	}

	// 同 ID、同内容、不同来源（builtin vs config）⇒ 指纹不同 ⇒ 不得自动继承信任
	sameContentOtherSource := &HookCommand{
		HookBase: HookBase{ID: "b5-cfg-1", Matcher: "bash", Pattern: "*", Action: ActionAllow, Source: SourceBuiltin},
		Command:  cmd,
	}
	if st := m.TrustStatus(sameContentOtherSource); st == TrustTrusted {
		t.Fatalf("B5-E3 未修复：同 ID 同内容但不同来源的 hook 共享了信任记录（%s）", st)
	}

	// 反向守卫：完全同一身份 + 同一内容 ⇒ 必须是 Trusted（否则信任门变成永远失败）
	if st := m.TrustStatus(cfgHook); st != TrustTrusted {
		t.Fatalf("同一身份的内容寻址信任失效：%s", st)
	}
}

// TestGE_B5_ContentChangeStillRequiresReTrust（反向守卫：内容变更即失效，语义未回退）
func TestGE_B5_ContentChangeStillRequiresReTrust(t *testing.T) {
	m := geManager(t)
	h := &HookCommand{
		HookBase: HookBase{ID: "b5-mod", Matcher: "bash", Pattern: "*", Action: ActionAllow},
		Command:  "echo v1",
	}
	if err := m.AddHook("PreToolUse", h); err != nil {
		t.Fatalf("AddHook: %v", err)
	}
	if err := m.TrustHookByID("b5-mod"); err != nil {
		t.Fatalf("TrustHookByID: %v", err)
	}
	changed := &HookCommand{
		HookBase: HookBase{ID: "b5-mod", Matcher: "bash", Pattern: "*", Action: ActionAllow},
		Command:  "echo v2-evil",
	}
	if st := m.TrustStatus(changed); st != TrustModified {
		t.Fatalf("内容变更后应判定为 modified（需重新 review），实际 %s", st)
	}
}
