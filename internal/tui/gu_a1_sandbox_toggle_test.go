package tui

// gu_a1_sandbox_toggle_test.go — A1（移交单 G0-002）：
// `/sandbox off` 必须**两套门同生同灭**，不再出现"报告 OFF、Manager 门仍生效"。
//
// 缺陷（G0 实测）：
//
//	Model.handleSandbox / runnerCore.handleSandbox 的 "off" 分支只翻
//	`Sandbox.SetEnabled(false)`（legacy 门），而 `tools/bash.go:987-991` 的
//	Manager 路径**只认** `SandboxMgr != nil && Level() != Disabled` ⇒
//	用户敲 `/sandbox off` 后 TUI 报告 "Sandbox OFF"，Manager 门却仍在校验并拦命令。
//
// 拍板：`集中拍板_决策表…§2` A1 = Opt-C —— 保留 opt-in，`/sandbox off` **同步清 `SandboxMgr`**；
// 未启用时危险命令走 **T1**（降级到权限层 + 告警），因此关闭路径必须给用户**可见的降级告警**。
//
// 生产入口：`Model.handleCommand("sandbox …")`（app.go:2742 分发 → app.go:2889 → handleSandbox）。
// 前置不手工构造：`NewModel` 的生产装配（lifecycle.go:502/509）本身就把两套门都装上，
// 这正是被测的起点。
//
// ★ 判别性：`guA1ManagerGateState` 是本单的**唯一判据**，它按"两态"断言——
// before 态必须观察到 `armed`（缺陷面），after 态必须观察到 `cleared`。改错方向两边都红。

import (
	"strings"
	"testing"
)

// guA1Model 经生产入口构造 Model（HOME 隔离到临时目录 + 生产退出收尾）。
func guA1Model(t *testing.T) Model {
	t.Helper()
	m, _ := guModelIsolatedHome(t)
	if m.toolService.Executor() == nil {
		t.Fatal("前置失败：Model 未装配 executor")
	}
	if m.toolService.Executor().SandboxMgr == nil {
		t.Fatal("前置失败：NewModel 生产装配应已装 SandboxMgr（两套门同装）")
	}
	return m
}

// TestGU_A1_Char_OffLeavesManagerGateArmed 特征化 + 判据（两态断言）：
//
//   - before 态：`/sandbox off` 后 Manager 门**仍装着** ⇒ 期望 "armed"
//     （这正是"报告 OFF 实际 ON"的可判别证据）
//   - after 态 ：必须 "cleared"
//
// 交付时本用例的期望值被**有意翻转**为 "cleared"，并在回执里登记。
func TestGU_A1_Char_OffLeavesManagerGateArmed(t *testing.T) {
	m := guA1Model(t)

	out := m.handleCommand("sandbox off")
	exec := m.toolService.Executor()

	if exec.Sandbox == nil || exec.Sandbox.Enabled() {
		t.Fatalf("legacy 门应被关闭；got enabled=%v", exec.Sandbox != nil && exec.Sandbox.Enabled())
	}
	state := "cleared"
	if exec.SandboxMgr != nil {
		state = "armed"
	}
	t.Logf("A1 判据：/sandbox off 后 manager 门状态 = %s（输出=%q）", state, out)

	// before：期望 armed（缺陷面）；after：期望 cleared（修复面）。
	const want = "cleared"
	if state != want {
		t.Fatalf("A1：/sandbox off 后 Manager 门状态 = %s，want %s —— "+
			"报告 OFF 却仍留着 Manager 门就是本单的缺陷面；输出=%q", state, want, out)
	}
	if !strings.Contains(out, "Sandbox OFF") {
		t.Errorf("关闭后应报告 OFF；got %q", out)
	}
}

// TestGU_A1_OnRestoresBothGates 回归判据：`/sandbox off` → `/sandbox on` 后
// 两套门都必须回来（只清不还 = 半死状态，比缺陷本身更糟）。
func TestGU_A1_OnRestoresBothGates(t *testing.T) {
	m := guA1Model(t)

	m.handleCommand("sandbox off")
	m.handleCommand("sandbox on")
	exec := m.toolService.Executor()

	if exec.Sandbox == nil || !exec.Sandbox.Enabled() {
		t.Fatal("A1：/sandbox on 后 legacy 门必须回来")
	}
	if exec.SandboxMgr == nil {
		t.Fatal("A1：/sandbox on 后 Manager 门必须回来（否则沙箱永久半死）")
	}
	if exec.SandboxMgr.Level().String() == "disabled" {
		t.Fatalf("A1：/sandbox on 后 Manager 等级不应是 disabled，got %s", exec.SandboxMgr.Level())
	}
}

// TestGU_A1_StatusReportsBothGates 可见性判据（A1 的"结构化可见"侧）：
// status 分支必须同时反映两套门的真实状态——此前只报 legacy，
// 才会出现"显示 ON 但 Manager 已卸"这类反向误报。严禁解析散文 Reason（R4）。
func TestGU_A1_StatusReportsBothGates(t *testing.T) {
	m := guA1Model(t)

	on := m.handleCommand("sandbox")
	if !strings.Contains(on, "manager=") {
		t.Errorf("A1：status 必须报告 manager 门状态；got %q", on)
	}

	m.handleCommand("sandbox off")
	off := m.handleCommand("sandbox")
	if !strings.Contains(off, "manager=none") {
		t.Errorf("A1：关闭后 status 必须显示 manager=none；got %q", off)
	}
	if strings.Contains(off, "Sandbox: ON") {
		t.Errorf("A1：关闭后不得报告 ON；got %q", off)
	}
}

// TestGU_A1_OffEmitsDegradeWarning 可见性判据：关闭沙箱 = 危险命令降级到
// 权限层（族A 的 T1）⇒ 必须**显式告警**，不得静默降档。
func TestGU_A1_OffEmitsDegradeWarning(t *testing.T) {
	m := guA1Model(t)
	out := m.handleCommand("sandbox off")
	if !strings.Contains(out, "permission layer") {
		t.Errorf("A1：关闭沙箱必须显式告警降级到权限层（T1 必须可见）；got %q", out)
	}
}
