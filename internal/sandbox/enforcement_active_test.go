// Package sandbox — 「OS 级沙箱是否真在强制」的判据测试。
//
// 这一问的存在理由：审批判据从「工具声明了多高能力档位」改成「**这一次调用**是否能被兜住」
// 之后，三问里的第三问（① 越界了吗 ② 不可逆吗 ③ **强制机制真的生效吗**）必须有事实来源。
// 而 ③ 是**硬前提**：`degradedSandbox.Restrict()` 是 `return nil`（零强制，os_sandbox.go:184）
// —— 若略去 ③，在"沙箱装不上的机器"上豁免就变成「也不问，也没人兜」= 静默放行任意命令。
//
// 对标 同类 CLI `core/src/safety.rs:78-98`：只有**真能强制**才 AutoApprove，否则 AskUser。
package sandbox

import "testing"

// TestOSEnforcementSufficient 是**纯粹判据**的 table-driven 钉死。
//
// 为什么必须 table-driven 而不是"在本机跑一次真实 manager"：
// 安全相关分支里最要紧的两条 —— **Windows 必须为 false**、**degraded 必须为 false**
// —— 在 macOS 开发机与 CI 上**无法**通过真实 manager 触达（`newPlatformSandbox`
// 按构建平台选实现，本机永远是 darwin-seatbelt）。只靠真机集成测试 ⇒ 这两条分支
// 只能靠"人记得"维持，改动时没有任何测试会红。
func TestOSEnforcementSufficient(t *testing.T) {
	cases := []struct {
		name string
		st   IsolationStatus
		want bool
	}{
		{
			name: "darwin seatbelt 正常（可强制）",
			st:   IsolationStatus{Platform: "darwin-seatbelt", Features: []string{"seatbelt(workspace-write)", "rlimit(cpu,nofile,fsize)"}},
			want: true,
		},
		{
			name: "darwin 用户放行网络 ⇒ 不可强制（网络是第二条通道）",
			st:   IsolationStatus{Platform: "darwin-seatbelt", Features: []string{"seatbelt(workspace-write)", "network=on"}},
			want: false,
		},
		{
			name: "linux namespaces + landlock（可强制）",
			st:   IsolationStatus{Platform: "linux-namespaces", Features: []string{"namespaces(pid,net,uts)", "landlock", "seccomp"}},
			want: true,
		},
		{
			name: "linux 只有 namespaces、无 landlock ⇒ 文件系统不受限 ⇒ 不可强制",
			st:   IsolationStatus{Platform: "linux-namespaces", Features: []string{"namespaces(pid,net,uts)"}},
			want: false,
		},
		{
			name: "linux 放行网络 ⇒ 不可强制",
			st:   IsolationStatus{Platform: "linux-namespaces", Features: []string{"namespaces(pid,uts)", "landlock", "network=on"}},
			want: false,
		},
		{
			name: "★ windows 必须为 false（Job Object 不动文件系统/网络，GAP-WIN-P3）",
			st:   IsolationStatus{Platform: "windows", Features: []string{"jobobject(memory,processes)"}},
			want: false,
		},
		{
			name: "★ degraded 必须为 false（degradedSandbox.Restrict 是 return nil，零强制）",
			st:   IsolationStatus{Platform: "degraded"},
			want: false,
		},
		{
			name: "unknown（类型断言失败/未初始化）⇒ false",
			st:   IsolationStatus{Platform: "unknown"},
			want: false,
		},
	}

	for _, c := range cases {
		if got := osEnforcementSufficient(c.st); got != c.want {
			t.Errorf("%s: osEnforcementSufficient(%+v) = %v, want %v", c.name, c.st, got, c.want)
		}
	}
}

// TestEnforcementActive_NilDisabledForeign 三个"显然不可强制"的入口：
// nil、级别 Disabled、非 *manager 的外来实现，全部必须 false（fail-closed）。
func TestEnforcementActive_NilDisabledForeign(t *testing.T) {
	if EnforcementActive(nil) {
		t.Error("nil manager 必须为 false（fail-closed）")
	}

	// 级别 Disabled：即便平台看起来正常，也不得声称可强制。
	disabled := NewManager(&SandboxConfig{}, SandboxLevelDisabled)
	if EnforcementActive(disabled) {
		t.Error("SandboxLevelDisabled 必须为 false")
	}

	// 外来实现（IsolationStatusOf 会得到 Platform=unknown）：不得 panic，且为 false。
	if EnforcementActive(foreignMgr{}) {
		t.Error("外实现（Platform=unknown）必须为 false")
	}
}

// TestEnforcementActive_ProductionManagerIfAvailable 接线断言（**全平台可编译**）：
// 生产装配上 EnforcementActive 的结论必须与纯粹判据**逐字一致** ——
// 不一致只有一种成因：EnforcementActive 里漏了/多了某条前置（如逃生开关判定）。
//
// 注意刻意**不用** `sandboxExecAvailable()`：它只在 darwin 构建里存在，
// 用了会让本文件在 Linux/Windows 上编译失败，从而连带把上面的 table-driven
// 判据测试一起挡在门外（那正是本文件要避免的：安全分支必须处处可断言）。
func TestEnforcementActive_ProductionManagerIfAvailable(t *testing.T) {
	mgr := NewProductionSandboxManager(t.TempDir())
	st := IsolationStatusOf(mgr)

	got := EnforcementActive(mgr)
	want := osEnforcementSufficient(st)
	// 把本机实测结论打出来：手测清单要靠它判断"沙箱内免问"在本机是否应当生效。
	t.Logf("本机生产装配实测：platform=%q EnforcementActive=%v features=%v degradations=%v",
		st.Platform, got, st.Features, st.Degradations)
	if got != want {
		t.Fatalf("EnforcementActive 与纯粹判据结论不一致：got=%v want=%v（是否漏了/多了某条前置？）st=%+v",
			got, want, st)
	}
	if (st.Platform == "darwin-seatbelt" || st.Platform == "linux-namespaces") && !want {
		// 环境限制（无 sandbox-exec / 无 landlock / 放行了网络），不是判据错误。
		// 明确打出来，免得把"环境不具备"误读成"豁免功能坏了"。
		t.Skipf("本机装配按判据不可强制（环境限制，非判据错误）: platform=%q features=%v", st.Platform, st.Features)
	}
}

// TestGuard_EnforcementActive_CustomSeatbeltProfileDisablesExemption 逃生开关必须关掉豁免。
//
// 自定义 Seatbelt profile 走 `sandbox-exec -f <文件>`，规则由用户自己写、本包**不解析**
// ⇒ 边界不可核实。⚠️ 而 `describeIsolationFeatures` 在这种情况下**刻意不列** network=on
// （banner 不得说谎）⇒ 光看 Features 完全看不出用户 profile 是否放行了网络。
// ⇒ 若不单独判这一条，就等于把"不可核实"读成"已强制" = 静默放行任意命令。
//
// 单变量对照：同一个生产 manager，**只**改逃生开关一项，结论必须翻转 true → false。
func TestGuard_EnforcementActive_CustomSeatbeltProfileDisablesExemption(t *testing.T) {
	mgr := NewProductionSandboxManager(t.TempDir())
	st := IsolationStatusOf(mgr)
	if st.Platform != "darwin-seatbelt" {
		t.Skipf("自定义 Seatbelt profile 是 darwin 专属逃生开关，本机平台 %q 不适用", st.Platform)
	}

	m, ok := mgr.(*manager)
	if !ok || m == nil || m.config == nil {
		t.Skip("生产 manager 结构不匹配，本用例需读回 config —— 跳过（环境限制）")
	}

	if !EnforcementActive(mgr) {
		t.Skip("本机生产装配本就不可强制，单变量对照不成立（环境限制，非判据错误）")
	}
	if m.config.OSLevel.Darwin.SeatbeltProfile != "" {
		t.Fatal("前置不成立：生产装配不应预设自定义 profile")
	}

	// 单变量：只打开逃生开关。
	prev := m.config.OSLevel.Darwin.SeatbeltProfile
	m.config.OSLevel.Darwin.SeatbeltProfile = "/tmp/whale-n040-fake-custom-profile.sb"
	defer func() { m.config.OSLevel.Darwin.SeatbeltProfile = prev }()

	if EnforcementActive(mgr) {
		t.Fatal("启用用户自定义 Seatbelt profile 后不得再声称可强制：" +
			"profile 规则本包不解析 ⇒ 边界不可核实 ⇒ 必须退回询问（fail-closed）")
	}
}
