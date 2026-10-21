package tui

// g10_axis_render_test.go — F1 + F2（2026-09-16，登记 /）。
//
// # 被修的两个缺陷
//
//	F1 · `/permissions doctor` 把**同一套系统状态渲染了两遍**：
// 上半段 `switch mode` 按审批档位硬编码散文（danger 档承诺
// "All operations will be executed without confirmation"），末尾又独立读
// `Sandbox.Enabled()` 打 "Sandbox: true — permissions are restricted"
// ⇒ danger 档下同屏互相打脸（真机 2026-09-16 02:45 被当成 bug 报）。
//	F2 · `/sandbox on|off` 连带把权限档改掉（on→read-only / off→cfg 默认
// workspace-write），却没有任何一行明说是**本命令**改的
// ⇒「danger 档 + 沙箱关」在 UI 上不可达，而用户看不见原因。
// ★ 2026-09-16 拍板 Opt-A 后**本项已被推翻**：连带改档不是"讲清楚"就够，
// 而是必须**摘掉**（它是 fail-open + 不可逆 + 取值来源错，见 G12）。
// 因此本文件原 F2 的两条"要求出现改档说明"断言已被**有意翻转**为
// "审批轴不得被写"，完整守卫在 `g12_axis_ownership_test.go`。
//
// # 生产入口（不手工构造前提）
//
// - Model 侧：`Model.handleCommand("permissions …")` / `("sandbox …")`
// （app.go:2695 分发 → handlePermissionsCommand:237 / handleSandbox:166）
// - runnerCore 侧：`runnerCore.handleSandbox("…")`，装配复用 `newTestRunnerCore()`
// （service_test.go:26 = plain/bridge 的生产装配形状，**同形同改的第二处**）
//
// # ★ 判别性（before 必须精确打红）
//
//	F1 = 结构化判据：doctor 必须按 SSOT `sandboxGateStatus()` 报出**两套门**
// （`legacy=` / `manager=`），且 danger 档的承诺句按能力轴**两态分流**。
// 旧实现两条都不满足（只会恒报 "Sandbox: true"、且承诺句不分流）。
// 反向守卫：沙箱仍在时**不得**出现"没有沙箱"与 T1 降级告警。
//	F2 = **已翻转**（见上）。现判据：`/sandbox on|off` 前后 `Policy().Mode()` 必须不变。
// 旧实现（连带改档）下必红 —— 该判据的 before 态打红记录在
// `g12_axis_ownership_test.go` 的 9/9 红里。
//
// # R4 说明
//
// 被测的两个函数**本身就是渲染层**，其交付物就是用户可见文本 —— 所以断言落在
// 文本上是对的。R4 禁止的是「从散文反推结构化事实」；本文件里所有"档位 / 门状态"
// 都来自 `Policy().Mode()` / `Sandbox.Enabled()` / `SandboxMgr`，不解析任何文案。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// g10Model 经生产入口装配 Model（HOME 隔离 + 生产退出收尾），复用 G-U 的测试基建。
func g10Model(t *testing.T) Model {
	t.Helper()
	m, _ := guModelIsolatedHome(t)
	if m.toolService.Executor() == nil {
		t.Fatal("前置失败：NewModel 生产装配应已装 executor")
	}
	return m
}

// ─────────────────────────────── F1 ───────────────────────────────

// TestG10_DoctorReportsGateStatusFromSSOT 判据一：能力轴必须来自 SSOT。
//
// 旧实现自己读 `Sandbox.Enabled()` ⇒ 只能报 legacy 门，看不见 Manager 门
// （A1 修过的同一个假报面，`内部工作回执`）。改后走 `sandboxGateStatus()`，
// 输出必含 `legacy=` 与 `manager=` 两个结构化字段。
func TestG10_DoctorReportsGateStatusFromSSOT(t *testing.T) {
	m := g10Model(t)

	m.handleCommand("permissions danger-full-access")
	out := m.handleCommand("permissions doctor")

	if !strings.Contains(out, "legacy=") || !strings.Contains(out, "manager=") {
		t.Errorf("F1：doctor 必须按 SSOT sandboxGateStatus() 报出两套门的状态"+
			"（输出应含 legacy= / manager=）。旧实现只报 `Sandbox: true`，看不见 Manager 门。"+
			"got %q", out)
	}
	if !strings.Contains(out, "Approval axis") || !strings.Contains(out, "Capability axis") {
		t.Errorf("F1：doctor 必须把两个正交轴分节渲染；got %q", out)
	}
}

// TestG10_DoctorDangerProseTracksCapabilityAxis 判据二（两态断言 · 本单核心）：
// danger 档的承诺句**必须随能力轴分流** ——
//
//	态一（沙箱仍在）：必须自我限定 + 不得宣称"没有沙箱"
//	态二（沙箱已关）：必须如实说明 + 必须复用 /sandbox off 的同一 T1 告警
//
// 旧实现两态输出**逐字相同** ⇒ 至少一态是错的 ⇒ 打红。
func TestG10_DoctorDangerProseTracksCapabilityAxis(t *testing.T) {
	m := g10Model(t)

	// 态一：danger 档 + 沙箱 ON（NewModel 生产装配默认两套门都在）。
	m.handleCommand("permissions danger-full-access")
	withSandbox := m.handleCommand("permissions doctor")

	if !strings.Contains(withSandbox, "still restricts them") {
		t.Fatalf("F1 态一：沙箱仍在时，审批轴的承诺句必须自我限定（否则与能力轴同屏打脸）；got %q", withSandbox)
	}
	if strings.Contains(withSandbox, "no sandbox is installed") {
		t.Fatalf("F1 态一反例守卫：沙箱仍在却宣称没有沙箱（把悲观分支写死）；got %q", withSandbox)
	}
	if strings.Contains(withSandbox, "permission layer") {
		t.Fatalf("F1 态一反例守卫：沙箱仍在却打了 T1 降级告警；got %q", withSandbox)
	}

	// 态二：先经生产入口关沙箱，再回到 danger 档。
	m.handleCommand("sandbox off")
	m.handleCommand("permissions danger-full-access")
	noSandbox := m.handleCommand("permissions doctor")

	if !strings.Contains(noSandbox, "no sandbox is installed") {
		t.Errorf("F1 态二：沙箱确已关闭时必须如实说明（两态分流正是本单判据）；got %q", noSandbox)
	}
	if !strings.Contains(noSandbox, "permission layer") {
		t.Errorf("F1 态二：沙箱关闭 = T1 降级，必须复用 /sandbox off 的同一告警文案（SSOT）；got %q", noSandbox)
	}
}

// TestG10_DoctorKeepsExistingSections 反例守卫：分节重构不得吃掉既有信息，
// 也不得把"有规则才出现"的两行变成无条件打印。
//
// 本用例**刻意不写任何规则** —— `/permissions add` 的项目级持久化会落到
// `<workDir>/.whale/settings.json`（`rules_store.go:397`），在测试里即仓库内的
// 真实文件（本仓纪律：探针不得写真实用户文件）。规则段的存在性由下面的
// "无规则时不得出现"反向覆盖，不需要真去加一条。
func TestG10_DoctorKeepsExistingSections(t *testing.T) {
	m := g10Model(t)

	out := m.handleCommand("permissions doctor")

	if !strings.Contains(out, "Mode: workspace-write") {
		t.Errorf("F1 反例守卫：doctor 仍必须报当前档位；got %q", out)
	}
	if !strings.Contains(out, "Standard mode") {
		t.Errorf("F1 反例守卫：workspace-write 档的审批轴说明必须仍在；got %q", out)
	}
	if strings.Contains(out, "Blocking rules") {
		t.Errorf("F1 反例守卫：无 deny 规则时不得打印 Blocking rules；got %q", out)
	}
	// 规则段仍必须读**真实条数**（`ruleSet.Count()` 的结构化事实），
	// 且默认装配下应为 0 —— 不得被分节重构顺手写死或吃掉。
	if !strings.Contains(out, "Custom Rules: 0") {
		t.Errorf("F1 反例守卫：doctor 必须报真实规则条数（默认 0 条）；got %q", out)
	}
}

// ─────────────────────────────── F2（已翻转） ───────────────────────────────
//
// ★ 有意翻转登记（2026-09-16 晚，拍板 Opt-A）：
//
//	原 F2 断言「/sandbox off 必须出现 "Permission mode was also changed"」
//	—— 它把"越界改档"冻结成了期望值。Opt-A 摘除越界写入后，这两条断言
//	**必须翻转**，否则会留下一组自相矛盾的守卫测试（生产码已不含该行为，
//	守卫却要求它出现）。
//
//	翻转后的契约（本文件保留最小版；完整版见 g12_axis_ownership_test.go）：
// `/sandbox on|off` 只写能力轴 ⇒ `Policy().Mode()` 前后不变。
//
//	被本项推翻并已随生产码删除的期望值还有（文档侧，需同步更新）：
// `G11_真机手测清单_2026-09-16.md:30,119`、
// `人工验收总清单_Round0至S6_2026-09-15.md:704,751`、
// `内部工作回执…md:172`。

// TestG10_SandboxToggleLeavesApprovalAxisAlone_ModelPath Model 侧（commands_plugin.go）。
//
// 从 danger 档出发才能暴露"档位被顺手拨走"。
func TestG10_SandboxToggleLeavesApprovalAxisAlone_ModelPath(t *testing.T) {
	m := g10Model(t)

	if !m.toolService.Policy().SetModeByName("danger-full-access") {
		t.Fatal("前置失败：danger-full-access 应可解析")
	}

	off := m.handleCommand("sandbox off")
	if got := m.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("F2（翻转后）：/sandbox off 不得写审批轴；实际 %s。输出=%q", got, off)
	}
	// 反例守卫：A1 的既有文案必须仍在（摘掉跨轴写不得顺手吃掉降级告警）。
	if !strings.Contains(off, "Sandbox OFF") {
		t.Errorf("F2 反例守卫：A1 的 `Sandbox OFF` 报告必须保留；got %q", off)
	}
	if !strings.Contains(off, "permission layer") {
		t.Errorf("F2 反例守卫：A1 的 T1 降级告警必须保留；got %q", off)
	}

	on := m.handleCommand("sandbox on")
	if got := m.toolService.Policy().Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("F2（翻转后）：/sandbox on 不得写审批轴；实际 %s。输出=%q", got, on)
	}
	if !strings.Contains(on, "manager=") {
		t.Errorf("F2 反例守卫：/sandbox on 仍必须报两套门状态；got %q", on)
	}
}

// TestG10_SandboxToggleLeavesApprovalAxisAlone_RunnerCorePath runnerCore 侧
// （runner_core.go，plain/bridge 路径）——**两处同形必须同改**，
// 只改 Model 侧 = 半修（本条正交于 Model 侧判据，单点回退必打红）。
func TestG10_SandboxToggleLeavesApprovalAxisAlone_RunnerCorePath(t *testing.T) {
	c := newTestRunnerCore()
	if c.Policy == nil || c.Executor == nil {
		t.Fatal("前置失败：newTestRunnerCore 未装配 Policy/Executor")
	}
	if !c.Policy.SetModeByName("danger-full-access") {
		t.Fatal("前置失败：danger-full-access 应可解析")
	}

	off := c.handleSandbox("off")
	if got := c.Policy.Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("F2（翻转后）：runnerCore 的 /sandbox off 必须与 Model 侧同形同改；"+
			"实际 %s。输出=%q", got, off)
	}
	if !strings.Contains(off, "Sandbox OFF") {
		t.Errorf("F2 反例守卫：runnerCore 侧 `Sandbox OFF` 报告必须保留；got %q", off)
	}

	on := c.handleSandbox("on")
	if got := c.Policy.Mode(); got != permission.LevelDangerFullAccess {
		t.Errorf("F2（翻转后）：runnerCore 的 /sandbox on 不得写审批轴；实际 %s。输出=%q", got, on)
	}
}
