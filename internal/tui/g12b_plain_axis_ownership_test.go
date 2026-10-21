package tui

import (
	"strings"
	"testing"
)

// ───────────────────────── G12b：--plain 路径的轴归属 ─────────────────────────
//
// # 为什么会有这一组判据（发现经过）
//
// G12 只覆盖了 TUI（`Model.handleCommand`）一侧，而 `whale --plain` 有**自己一套**
// inline 调度表（`plain.go` 的 `initDispatchHandlers`）——`/permissions` 在那里被
// **重新实现**了一遍，结果是：
//
//	① 输出完全没有轴归属文案（既不说"只写审批轴"，也不提沙箱）；
//	② 连 `status` 子命令都不认（返回 "Unknown mode: status"）。
//
// 用户要求「help 和代码提示都要显示具体是哪个轴」。`--plain` 是一等 UI，
// 不是调试后门 ⇒ 它必须与 TUI 同形。否则同一句话在两个 UI 里含义不同，
// 正是"文案面 I1 漂移"。
//
// 2026-09-16 真机探针实证（`HOME` 隔离 + 预置 settings.json）：
//
//	> /permissions status
//	Unknown mode: status ← 修复前
//	Available: read-only, workspace-write, danger-full-access

// newG12bPlainRunner 返回一个已装配 /permissions 所需依赖的 plain runner。
//
// 复用 plain_test.go 的 newTestPlainRunner（生产装配口径：Executor + Policy 都非 nil），
// 并显式初始化 dispatchHandlers —— 走**生产调度入口** dispatchCommand()，
// 而不是直接调 handler 闭包（避免"单测能构造、生产不走"）。
func newG12bPlainRunner(t *testing.T) *PlainRunner {
	t.Helper()
	r := newTestPlainRunner()
	if r.Executor == nil || r.Policy == nil {
		t.Fatal("前置失败：newTestPlainRunner 应已装 Executor 与 Policy")
	}
	r.dispatchHandlers = r.initDispatchHandlers()
	return r
}

// TestG12b_PlainPermissionsStatusDeclaresAxisOwnership 判据一：
// `--plain` 的 `/permissions status` 必须能跑通，且自报轴归属。
func TestG12b_PlainPermissionsStatusDeclaresAxisOwnership(t *testing.T) {
	r := newG12bPlainRunner(t)

	status := r.dispatchCommand("permissions", "status", "")

	if !strings.Contains(status, "approval axis only") {
		t.Errorf("G12b：--plain 的 /permissions status 必须显式声明它只写审批轴"+
			"（用户要求：区分单轴归属）；got %q", status)
	}
	if !strings.Contains(status, "sandbox") {
		t.Errorf("G12b：--plain 的轴归属声明必须点名沙箱（能力轴）不受影响；got %q", status)
	}
	if strings.Contains(status, "Unknown mode") {
		t.Errorf("G12b：--plain 必须支持 `status` 子命令；"+
			"修复前它返回 \"Unknown mode: status\"（真机探针实证）；got %q", status)
	}
}

// TestG12b_PlainPermissionsModeSwitchDeclaresAxisOwnership 判据二：
// 裸档位名切换（非 status）同样必须自报轴归属。
func TestG12b_PlainPermissionsModeSwitchDeclaresAxisOwnership(t *testing.T) {
	r := newG12bPlainRunner(t)

	switched := r.dispatchCommand("permissions", "read-only", "")

	if !strings.Contains(switched, "approval axis only") {
		t.Errorf("G12b：--plain 切换档位后的回执也必须自报轴归属；got %q", switched)
	}
	if !r.Policy.SetModeByName("workspace-write") {
		t.Fatal("前置失败：测试用 runner 的 Policy 应能解析 workspace-write")
	}
}

// TestG12b_PlainPermissionsDoesNotTouchSandbox 判据三（I1 行为面）：
// `--plain` 的 `/permissions` 只写审批轴，能力轴必须逐字不变。
//
// 这是 Opt-A 的**行为**判据——不是只看文案。若有人日后又把跨轴偷写加回
// plain 侧，本判据会红。
func TestG12b_PlainPermissionsDoesNotTouchSandbox(t *testing.T) {
	r := newG12bPlainRunner(t)

	beforeOn := sandboxCapabilityOn(r.Executor)
	beforeGate := sandboxGateStatus(r.Executor)

	r.dispatchCommand("permissions", "danger-full-access", "")
	r.dispatchCommand("permissions", "read-only", "")
	r.dispatchCommand("permissions", "status", "")

	if got := sandboxCapabilityOn(r.Executor); got != beforeOn {
		t.Errorf("G12b（I1）：/permissions 不得改动能力轴（沙箱）；before=%v after=%v", beforeOn, got)
	}
	if got := sandboxGateStatus(r.Executor); got != beforeGate {
		t.Errorf("G12b（I1）：/permissions 不得改动能力轴的门状态；before=%q after=%q", beforeGate, got)
	}
}

// TestG12b_PlainPermissionsRejectsPresetIDsOnSingleAxisCommand 判据四（I2）：
// `/permissions` 是单轴命令，**不得**接受跨轴命名空间（预设 id）——
// `safe` / `standard` / `unrestricted` 属于 `/security-preset`。
//
// 否则用户可以拿 `/permissions unrestricted` 绕过"写两轴要显式声明"的约定，
// 在命令名骗人的情况下把沙箱关掉。
func TestG12b_PlainPermissionsRejectsPresetIDsOnSingleAxisCommand(t *testing.T) {
	r := newG12bPlainRunner(t)

	beforeOn := sandboxCapabilityOn(r.Executor)
	beforeMode := r.Policy.Mode()

	for _, presetID := range []string{"safe", "standard", "unrestricted"} {
		out := r.dispatchCommand("permissions", presetID, "")

		if !strings.Contains(out, "Unknown") {
			t.Errorf("G12b（I2）：/permissions %s 必须被拒（预设 id 不是审批轴档名）；got %q",
				presetID, out)
		}
		if r.Policy.Mode() != beforeMode {
			t.Errorf("G12b（I2）：被拒后审批轴不得变化；preset=%s before=%v after=%v",
				presetID, beforeMode, r.Policy.Mode())
		}
		if got := sandboxCapabilityOn(r.Executor); got != beforeOn {
			t.Errorf("G12b（I2）：被拒后能力轴不得变化；preset=%s", presetID)
		}
	}
}

// TestG12b_PresetIDOnApprovalCommandPointsToTheRightCommand 判据五（I2 · 指路）：
// 把**预设 id** 敲在**单轴命令**上时，回执必须**指路**到 `/security-preset`
// 并说明"那个 id 写两轴"，而不是干巴巴一句 `Unknown mode`。
//
// 为什么这条值得单独设判据：`/permissions` 正是用户反馈里"最容易被当成总开关"
// 的那条命令。只回 "Unknown mode: safe" = 把"两套 id 各管什么"的困惑**原样还给
// 用户**——恰恰是本轮要消除的东西（用户原话："要显示具体是啥，是单沙箱还是
// 单表面的不包含沙箱"）。
//
// 判别性：修复前两侧都**不含** `/security-preset` 字样（plain 回
// `Unknown mode: safe`；TUI 回 `Unknown subcommand: safe`）⇒ before 必红。
func TestG12b_PresetIDOnApprovalCommandPointsToTheRightCommand(t *testing.T) {
	r := newG12bPlainRunner(t)

	out := r.dispatchCommand("permissions", "safe", "")
	if !strings.Contains(out, "/security-preset") {
		t.Errorf("G12b（I2·指路）：预设 id 敲在单轴命令上时必须指路到 /security-preset；got %q", out)
	}
	if !strings.Contains(out, "BOTH axes") && !strings.Contains(out, "两轴") {
		t.Errorf("G12b（I2·指路）：指路文案必须说明该 id 写**两轴**（否则用户不知道为何被拒）；got %q", out)
	}

	// 反向守卫：**普通**未知档名不得被误指路到预设命令，
	// 否则"指路"会退化成"什么都指向 /security-preset"，反而制造新混淆。
	junk := r.dispatchCommand("permissions", "not-a-preset", "")
	if strings.Contains(junk, "/security-preset") {
		t.Errorf("G12b（反向守卫）：普通未知档名不应被误指路到 /security-preset；got %q", junk)
	}
}

// TestG12b_TUIPermissionsErrorSharesTheSameGuidance 判据六（两 UI 同文案）：
// 同一份指路文案必须**同时**出现在 TUI 侧（`Model.handleCommand`）。
//
// 这条判据的存在理由就是 本身：本轮修的就是"两 UI 各自实现"。
// 所以新增行为必须**两侧都验**，不能只验 --plain。
func TestG12b_TUIPermissionsErrorSharesTheSameGuidance(t *testing.T) {
	m := g10Model(t)

	out := m.handleCommand("permissions safe")
	if !strings.Contains(out, "/security-preset") {
		t.Errorf("G12b（两 UI 同文案）：TUI 侧把预设 id 敲在 /permissions 上时也必须指路；got %q", out)
	}
}
