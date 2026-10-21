package tui

// g13_security_preset_test.go — G13：**双轴**写入面 `/security-preset`。
//
// # 它解决什么
//
// G12（Opt-A）把 `/sandbox` 的跨轴偷写摘掉了，于是"一键变安全 / 一键完全放开"
// 这个**正当诉求**失去了出口。本命令就是它的**合法归宿**：名字声明写两轴，
// 预设 id 用独立命名空间（避免 `/permissions workspace-write` 与
// `/security-preset workspace-write` 指向不同事物）。
//
// # 生产入口（不手工构造前提）
//
// - Model 侧：`Model.handleCommand("security-preset …")`
// （app.go 命令名表 → dispatch → `handleSecurityPreset`）
// - runnerCore 侧：`runnerCore.handleSecurityPreset("…")`，装配复用 `newTestRunnerCore()`
//
// # ★ 判别性
//
// - `TestG13_UnrestrictedReachesFullAccess`：`danger-full-access + 沙箱关`
// 这个组合在旧实现里**UI 不可达**（`/sandbox off` 会先把档位拨回
// workspace-write），本命令让它可达 ⇒ 旧实现下必红（命令不存在/无此效果）。
// - `TestG13_ListingShowsCustomForMixedState`：I4 的用户可见面。
// 若 MatchSecurityPreset 退化成"只比审批档"，这条必红（会显示 unrestricted）。
// - `TestG13_UnknownPresetDoesNotChangeState`：I2 的用户可见面 ——
// 拿单轴档名当预设名必须被拒且**零副作用**（打错字不得悄悄改安全档）。
// - `TestG13_HelpDeclaresAxisOwnership`：用户明确要求 —— help 里必须写清
// 每个写入面到底写哪些轴（"单沙箱还是单表面不包含沙箱"）。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/permission"
)

// ────────────────── 判据一：双轴都被写 ──────────────────

// TestG13_PresetWritesBothAxes 三条预设逐条核对"两轴都到位"。
func TestG13_PresetWritesBothAxes(t *testing.T) {
	for _, preset := range permission.BuiltinSecurityPresets() {
		t.Run(preset.ID, func(t *testing.T) {
			m := g10Model(t)
			// 先推到与目标**相反**的状态，确保观测到的是本命令写进去的，而不是初始值。
			applySecurityPreset(m.toolService.Policy(), m.toolService.Executor(),
				permission.SecurityPreset{ID: "__opposite__", Level: flipLevel(preset.Level), SandboxOn: !preset.SandboxOn})

			out := m.handleCommand("security-preset " + preset.ID)

			if got := m.toolService.Policy().Mode(); got != preset.Level {
				t.Errorf("审批轴应为 %s，实际 %s；输出=%q", preset.Level, got, out)
			}
			exec := m.toolService.Executor()
			if got := sandboxCapabilityOn(exec); got != preset.SandboxOn {
				t.Errorf("能力轴应为 sandbox=%v，实际 %v [%s]；输出=%q",
					preset.SandboxOn, got, sandboxGateStatus(exec), out)
			}
		})
	}
}

// flipLevel 返回"另一个"审批档（测试里用来把初态推到与目标相反处）。
func flipLevel(l permission.Level) permission.Level {
	if l == permission.LevelDangerFullAccess {
		return permission.LevelReadOnly
	}
	return permission.LevelDangerFullAccess
}

// TestG13_UnrestrictedReachesFullAccess 判据一（本单最有价值的一条）。
//
// `danger-full-access + 沙箱关` = 两轴全放开的终态（既没有审批、也没有隔离）
// （`PermissionProfile::Disabled` + `AskForApproval::Never`，
// `utils/approval-presets/src/lib.rs:50-59`）。旧实现下这个组合在 UI 上**不可达**：
// danger 档用户敲 `/sandbox off`，档位会被拨回 workspace-write（F2 缺陷）。
func TestG13_UnrestrictedReachesFullAccess(t *testing.T) {
	m := g10Model(t)

	out := m.handleCommand("security-preset unrestricted")

	if got := m.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("unrestricted 应写入 danger-full-access，实际 %s；输出=%q", got, out)
	}
	exec := m.toolService.Executor()
	if exec.Sandbox != nil && exec.Sandbox.Enabled() {
		t.Errorf("unrestricted 应关闭 legacy 门；输出=%q", out)
	}
	if exec.SandboxMgr != nil {
		t.Errorf("unrestricted 应清掉 Manager 门（两门同灭，A1 语义）；输出=%q", out)
	}
	if !strings.Contains(out, "manager=none") {
		t.Errorf("回执必须如实报出 Manager 门已清；got %q", out)
	}
	// 沙箱关闭 = T1 降级，必须显式告警（不得静默）。
	if !strings.Contains(out, "permission layer") {
		t.Errorf("unrestricted 关掉沙箱必须打出 T1 降级告警；got %q", out)
	}
}

// TestG13_OutputDeclaresBothAxes 判据一（可读性）：回执必须**逐轴**报出实际值。
//
// 只说一句"已应用"是不够的 —— 用户必须能核对两轴各被写成了什么。
func TestG13_OutputDeclaresBothAxes(t *testing.T) {
	m := g10Model(t)
	out := m.handleCommand("security-preset safe")

	if !strings.Contains(out, "approval axis") {
		t.Errorf("回执必须逐轴报审批轴；got %q", out)
	}
	if !strings.Contains(out, "capability axis") {
		t.Errorf("回执必须逐轴报能力轴；got %q", out)
	}
	if !strings.Contains(out, "read-only") {
		t.Errorf("回执必须报出实际的审批档（read-only）；got %q", out)
	}
	if !strings.Contains(out, "BOTH axes") {
		t.Errorf("回执必须声明本命令写了两轴（否则用户会以为它只改了一轴）；got %q", out)
	}
}

// ────────────────── 判据二：I4 —— 混合态必须显示 Custom ──────────────────

// TestG13_ListingShowsCustomForMixedState 判据二：无预设覆盖的两轴组合
// 必须**显式**显示 Custom，不得四舍五入到最近的预设。
//
// 构造：审批轴 danger-full-access + 能力轴开（= 3 档 × 2 态里没有预设覆盖的那 3 态之一）。
func TestG13_ListingShowsCustomForMixedState(t *testing.T) {
	m := g10Model(t)

	// 用单轴命令组合出混合态 —— 这正是本仓"两轴可自由组合"的价值所在：
	// 只暴露打包预设的实现只能选预设，本仓两轴可以任意组合。
	if !m.toolService.Policy().SetModeByName("danger-full-access") {
		t.Fatal("前置失败：danger-full-access 应可解析")
	}
	m.handleCommand("sandbox on")
	if !sandboxCapabilityOn(m.toolService.Executor()) {
		t.Fatal("前置失败：沙箱应处于开状态")
	}

	out := m.handleCommand("security-preset")

	// 2026-09-18：TUI 无参改为**打开上下键选择器**，回执复用 securityPresetCurrentLine
	// ⇒ 主行改用英文主文案（中文降为缩进辅助行）。
	if !strings.Contains(out, "Current: Custom") {
		t.Errorf("I4：danger-full-access + 沙箱 ON 不属于任何预设，必须显示 Custom；got %q", out)
	}
	if strings.Contains(out, "Current: unrestricted") {
		t.Errorf("I4 违规：当前态被判成了 unrestricted，但沙箱其实是**开**的 —— "+
			"预设名开始骗人（与 F2 同类）。got %q", out)
	}
	// 两入口同源：plain 侧的 listing 必须给出同一判定。
	if listing := securityPresetListing(m.toolService.Policy(), m.toolService.Executor()); !strings.Contains(listing, "Current: Custom") {
		t.Errorf("plain 侧 listing 也必须显示 Custom；got %q", listing)
	}
}

// TestG13_ListingShowsPresetWhenMatched 反向守卫：真属于预设的状态必须报出预设名。
func TestG13_ListingShowsPresetWhenMatched(t *testing.T) {
	m := g10Model(t)

	// 启动装配（lifecycle.go:511/520 两门全开 + 默认 workspace-write）= standard。
	out := m.handleCommand("security-preset")
	// 2026-09-18：主行英文（见上一条测试的说明）。
	if !strings.Contains(out, "Current: standard") {
		t.Errorf("启动默认态应落在 standard；got %q", out)
	}
	if strings.Contains(out, "Custom") {
		t.Errorf("反向守卫：匹配到预设时不得同时出现 Custom；got %q", out)
	}
	// 两入口同源：plain 侧 listing 必须给出同一判定。
	if listing := securityPresetListing(m.toolService.Policy(), m.toolService.Executor()); !strings.Contains(listing, "Current: standard") {
		t.Errorf("plain 侧 listing 也必须显示 standard；got %q", listing)
	}
}

// ────────────────── 判据三：I2 —— 单轴档名不得当预设名 ──────────────────

// TestG13_UnknownPresetDoesNotChangeState 判据三：未知 id 必须**零副作用**。
//
// 重点覆盖 `read-only` / `workspace-write` / `danger-full-access` 这三个
// **单轴档名** —— 它们极易被误当预设名（用户习惯），而它们只该走 `/permissions`。
// 若这里静默回退到某个默认预设，打错一个词就会悄悄改变安全档。
func TestG13_UnknownPresetDoesNotChangeState(t *testing.T) {
	for _, badID := range []string{"read-only", "workspace-write", "danger-full-access", "typo"} {
		t.Run(badID, func(t *testing.T) {
			m := g10Model(t)
			if !m.toolService.Policy().SetModeByName("danger-full-access") {
				t.Fatal("前置失败：danger-full-access 应可解析")
			}
			beforeGates := sandboxCapabilityOn(m.toolService.Executor())

			out := m.handleCommand("security-preset " + badID)

			if !strings.Contains(out, "Unknown security preset") {
				t.Errorf("未知预设必须报错，不得静默回退；got %q", out)
			}
			if got := m.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
				t.Errorf("未知预设不得改动审批轴：%s → %s；got %q",
					permission.LevelDangerFullAccess, got, out)
			}
			if got := sandboxCapabilityOn(m.toolService.Executor()); got != beforeGates {
				t.Errorf("未知预设不得改动能力轴：%v → %v", beforeGates, got)
			}
			// 报错信息要能让用户找到正确入口。
			if !strings.Contains(out, "safe") || !strings.Contains(out, "standard") {
				t.Errorf("未知预设的报错应列出可用 id；got %q", out)
			}
		})
	}
}

// ────────────────── 判据四：help 必须写明轴归属（用户明确要求） ──────────────────

// TestG13_HelpDeclaresAxisOwnership 判据四：帮助文本必须把"谁写哪些轴"写出来。
//
// 用户原话：「permisions 很容易被认为是常规的权限，在 help 和代码提示中要显示，
// 具体是啥，是单沙箱还是单表面的不包含沙箱」。所以三行都必须带轴归属声明。
func TestG13_HelpDeclaresAxisOwnership(t *testing.T) {
	reg := command.NewRegistry()
	help := formatHelp(reg)

	// ★ 期望文案与注册表英文描述 SSOT（command.englishCommandDescriptions）**同源**。
	// 此前这里硬编码大写 "ONLY"，那是本文件配套的"第 4 份手抄 plain 命令表"的遗留写法；
	// C2把手抄表换成注册表驱动后，两个副本的大小写分歧暴露出来
	// （SSOT 用小写 only，另有 7 处运行期断言同用小写）。按 「同一事实跨入口必须同源」，
	// 收敛到 SSOT 口径，而不是再复制一份大写文案。
	// SSOT 自身由 internal/command/axis_attribution_test.go 守卫。
	for _, want := range []string{
		"Approval axis only", // /permissions：只写审批轴
		"Sandbox axis only",  // /sandbox：只写能力轴
		"BOTH axes at once",  // /security-preset：写两轴
		"/security-preset",   // 新命令必须出现在 help 里
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help 必须包含 %q（用户要求区分各写入面的轴归属）；help 片段=%q",
				want, g13ExtractPermissionSection(help))
		}
	}

	// 反向守卫：单轴命令的**帮助行**必须带 only 限定（不得宣称它管另一轴）。
	//
	// ★ 行定位改用注册表的真实 Usage，不写死字符串 —— 首版写死 "/permissions [mode]"
	// 而注册表实际是 "/permissions [mode|status]"，`strings.Contains(line, ...)` 永不命中
	// ⇒ 该分支恒真、从不报红（假守卫）。改为按 Usage 取行后，命令若改名/删除会前置失败。
	for _, name := range []string{"permissions", "sandbox"} {
		cmd, ok := reg.Get(name)
		if !ok {
			t.Fatalf("前置失败：%s 未注册，反向守卫会静默恒绿", name)
		}
		var line string
		for _, l := range strings.Split(help, "\n") {
			if strings.Contains(l, cmd.Usage) {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("前置失败：help 里找不到 %s 的行（usage=%q）—— 反向守卫会静默恒绿", name, cmd.Usage)
		}
		if !strings.Contains(line, "only") {
			t.Errorf("%s 帮助行必须带 only 限定（不得宣称它管另一轴）；got %q", name, line)
		}
	}
}

// g13ExtractPermissionSection 只用于失败信息：截出 help 里相关几行，便于定位。
func g13ExtractPermissionSection(help string) string {
	var out []string
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "axis") || strings.Contains(line, "security-preset") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, " | ")
}

// ────────────────── 判据五：plain/bridge 路径同形 ──────────────────

// TestG13_RunnerCoreParity 判据五：runnerCore 侧必须与 Model 侧同语义。
//
// 两侧都是**薄转发**到 `securityPresetCommand` —— 这条判据确认转发真的接上了
// （只接线 Model 侧 = plain/bridge 路径缺命令）。
func TestG13_RunnerCoreParity(t *testing.T) {
	c := newTestRunnerCore()
	if c.Policy == nil {
		t.Fatal("前置失败：newTestRunnerCore 未装配 Policy")
	}

	safe := c.handleSecurityPreset("safe")
	if got := c.Policy.Mode(); got != permission.LevelReadOnly {
		t.Errorf("runnerCore 侧 security-preset safe 应写入 read-only，实际 %s；输出=%q", got, safe)
	}
	if !strings.Contains(safe, "BOTH axes") {
		t.Errorf("runnerCore 侧回执应与 Model 侧同形；got %q", safe)
	}

	unknown := c.handleSecurityPreset("workspace-write")
	if !strings.Contains(unknown, "Unknown security preset") {
		t.Errorf("runnerCore 侧未知预设同样必须报错（单轴档名不是预设名）；got %q", unknown)
	}
	if got := c.Policy.Mode(); got != permission.LevelReadOnly {
		t.Errorf("runnerCore 侧未知预设不得改档；实际 %s", got)
	}
}
