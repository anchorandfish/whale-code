package tui

// sandbox_axis_notice_test.go — `/sandbox off` 输出的**跨轴后果**判据。
//
// # 缺陷（用户真机手测暴露，I-2 取证回执同源）
//
// `sandboxAxisNotice` 原文只声明「审批轴不受影响 / the approval axis is untouched」。
// 该句在**档位层为真**（`SetMode` 确实没被调用），但在**单次调用的结论层为假**：
// `/sandbox off` 会让 `sandboxBoundedTools`（`bash`/`monitor`）失去免问前提（出口 (c)，
// `internal/tools/pipeline.go` 的 `IsSandboxBoundedTool && enforcementActive`），
// 于是 workspace-write 档下**每一次** shell 调用都重新弹确认。
// 用户据此把「关沙箱」读成了「减少限制」—— 方向恰好相反。
// ⇒ 修法：**保留**轴归属声明（G12 契约，不得删），**追加**按档位分叉的后果句。
//
// # 为什么必须按档位分叉（补一句万能话会说出假话）
//
// 三档下「关沙箱的后果」**互不相同**，由 `permission.checkModeLocked`
// （`internal/permission/permission.go:201-247`）决定；而 `bash` 的档位是**静态定级**
// （`requiredForTool` 只对 `Agent` 走输入感知，其余走 `RequiredLevelOf`，见 `:335-343`）：
//
//	read-only : required(bash)=workspace-write > read-only ⇒ (false,false) **拒绝**
// ⇒ 关沙箱**不改变询问**
//	workspace-write : 未免问 ⇒ (true, required>read-only) = (true,true) ⇒ **每次都要确认**
//	danger-full-access : (true,false) ⇒ 本来就不问；关沙箱**不会多弹**，抽走的是隔离
//
// 所以 mode-blind 的 `sandboxDisableNotice` 说不出这件事 —— 它被 4 处复用
// （含 `securityPresetAppliedNotice` 的 `unrestricted` 档回执），在那里会**说出假话**。
// 落点必须是**唯一带 `mode`** 的 `sandboxAxisNotice`。
//
// # 本条判据的自检（R4-7）
//
// 第一条是**事实前置**，不是文案判据：它让「三档确实不同」由测试自己证明。
// 若三档同色，则分叉文案与分叉判据都是过度设计 —— 这条会先红，提醒先改文案再谈判据。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 判据字面量（与 `sandbox_toggle.go` 的成文 SSOT 一致；改文案必须同步这里）。
const (
	noticeWWShellExemption = "exempt from confirmation only while the sandbox is enforced"
	noticeDangerNoExtra    = "removes the isolation behind them rather than adding prompts"
	noticeReadOnlyBlocked  = "already blocks shell commands"
)

// TestSandboxAxisNotice_Premise_ModesDiffer 是**事实前置**（不是文案判据）。
//
// 它钉住「bash / monitor 在三档下的裁决确实不同」。依据 `checkModeLocked`
// 与 `RequiredLevelOf`；事实若变，先改文案再改本判据。
func TestSandboxAxisNotice_Premise_ModesDiffer(t *testing.T) {
	type verdict struct{ allowed, needConfirm bool }
	cases := []struct {
		mode permission.Level
		want verdict
	}{
		// read-only：shell 类命令越档 ⇒ **拒绝**（不是"问"）。
		{permission.LevelReadOnly, verdict{false, false}},
		// workspace-write：允许但未免问 ⇒ **必问**。这条正是用户观测到的现象。
		{permission.LevelWorkspaceWrite, verdict{true, true}},
		// danger-full-access：全放行免问 ⇒ 关沙箱**不会多弹**。
		{permission.LevelDangerFullAccess, verdict{true, false}},
	}

	// `sandboxBoundedTools` 的两个成员都必须满足 —— 文案里点名了它们两个。
	// ★ 必须用**注册表里的生产名**：`Monitor`（`internal/tools/tools.go:921`）。
	// 能力表用的是小写 `"monitor"`（`internal/permission/capability.go:307`），
	// 但定级表 `LevelOf` 是精确名匹配 ⇒ 小写会 fail-closed 到最高档，
	// 得到的裁决**不是生产形态**（R2）。此处传生产名。
	for _, tool := range []string{"bash", "Monitor"} {
		for _, c := range cases {
			pol := permission.NewPolicy(c.mode)
			allowed, needConfirm := pol.Check(tool, `{"command":"ls -la"}`)
			if allowed != c.want.allowed || needConfirm != c.want.needConfirm {
				t.Errorf("事实前置不成立：%s 档下 %s 裁决 = (allowed=%v, needConfirm=%v)，"+
					"期望 (%v,%v)。文案的档位分叉依赖这条事实。",
					c.mode, tool, allowed, needConfirm, c.want.allowed, c.want.needConfirm)
			}
		}
	}
}

// TestSandboxAxisNotice_WorkspaceWrite_StatesShellExemptionDependsOnSandbox 主判据（Model 侧）。
//
// **这一条在改动前必红** —— 原文只写「审批轴不受影响」，没有一个字提到免问前提失效。
func TestSandboxAxisNotice_WorkspaceWrite_StatesShellExemptionDependsOnSandbox(t *testing.T) {
	m := g10Model(t)
	if !m.toolService.Policy().SetModeByName("workspace-write") {
		t.Fatal("前置失败：workspace-write 应可解析")
	}

	out := m.handleCommand("sandbox off")

	// 既有契约必须仍在 —— `g12_axis_ownership_test.go:144` 明示「加新句不得吃掉旧句」。
	for _, keep := range []string{
		"capability axis only", "approval axis", "Sandbox OFF", "permission layer",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("加新句吃掉了旧句：输出应仍含 %q；got %q", keep, out)
		}
	}

	if !strings.Contains(out, noticeWWShellExemption) {
		t.Errorf("workspace-write 档下 `/sandbox off` 必须报出**跨轴后果**："+
			"shell 类命令（bash/monitor）的免问以「沙箱在强制」为前提 ⇒ 关掉后每次调用都会重新确认。"+
			"原文只说 approval axis is untouched，用户会读成「关沙箱 = 减少限制」（方向相反）；got %q", out)
	}
	if strings.Contains(out, noticeDangerNoExtra) {
		t.Errorf("档位分叉失效：workspace-write 档不该出现 danger 档的说法；got %q", out)
	}
}

// TestSandboxAxisNotice_DangerFullAccess_StatesNoExtraPrompts 是分叉的另一半。
//
// 它证明「那句话是**按档位算出来的**」，而不是一句到处都印的常量 ——
// 否则上面那条判据无法排除"补了句万能话"。反过来它也可能被 `danger` 的说法蒙对。
func TestSandboxAxisNotice_DangerFullAccess_StatesNoExtraPrompts(t *testing.T) {
	m := g10Model(t)
	if !m.toolService.Policy().SetModeByName("danger-full-access") {
		t.Fatal("前置失败：danger-full-access 应可解析")
	}

	out := m.handleCommand("sandbox off")

	if !strings.Contains(out, noticeDangerNoExtra) {
		t.Errorf("danger-full-access 档下必须说清「关沙箱**不会**多弹窗，抽走的是背后的隔离」—— "+
			"该档 `checkModeLocked` 返回 (true,false)，本来就免问；对这类用户说「每次都会重新确认」是假话。got %q", out)
	}
	if strings.Contains(out, noticeWWShellExemption) {
		t.Errorf("档位分叉失效：danger-full-access 档不该出现 workspace-write 档的说法；got %q", out)
	}
}

// TestSandboxAxisNotice_ReadOnly_StatesPromptsUnchanged 补齐三档（read-only）。
func TestSandboxAxisNotice_ReadOnly_StatesPromptsUnchanged(t *testing.T) {
	m := g10Model(t)
	if !m.toolService.Policy().SetModeByName("read-only") {
		t.Fatal("前置失败：read-only 应可解析")
	}

	out := m.handleCommand("sandbox off")

	if !strings.Contains(out, noticeReadOnlyBlocked) {
		t.Errorf("read-only 档下必须说清「shell 类命令本来就被拒绝，关沙箱不改变询问」—— "+
			"该档 `checkModeLocked` 对越档工具返回 (false,false)，与沙箱开关无关。got %q", out)
	}
}

// TestSandboxAxisNotice_RunnerCorePath_SameWording 两侧都验（plain/bridge 路径）。
//
// 同一命令在两个 UI 里必须同源。`plain.go` 的 `"sandbox"` 表项只做薄转发
// （`plain.go:655` → `runnerCore.handleSandbox`），本判据从**转发入口的下游**进入，
// 证明收口后两侧共用同一份 `sandboxAxisNotice`；只改一侧会在这里打红。
func TestSandboxAxisNotice_RunnerCorePath_SameWording(t *testing.T) {
	c := newTestRunnerCore()
	if c.Policy == nil {
		t.Fatal("前置失败：newTestRunnerCore 未装配 Policy")
	}
	if !c.Policy.SetModeByName("workspace-write") {
		t.Fatal("前置失败：workspace-write 应可解析")
	}

	out := c.handleSandbox("off")

	if !strings.Contains(out, noticeWWShellExemption) {
		t.Errorf("runnerCore（--plain / bridge 路径）必须与 Model 侧同源同文案；"+
			"只改一侧 = 半修。got %q", out)
	}
	if !strings.Contains(out, "approval axis") {
		t.Errorf("runnerCore 侧同样必须保留轴归属声明；got %q", out)
	}
}
