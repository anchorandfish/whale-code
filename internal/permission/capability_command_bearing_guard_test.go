// Package permission —— 「命令载体」判据（IsCommandBearingTool）的守卫。
//
// 为什么需要这组守卫（P-002 / ★S-1 · 2026-09-22 反缝补扫描）：
// 「这个工具是不是命令载体」这一事实此**手抄了 10 处工具名对**
// （`x == "bash" || x == "PowerShell"` / `case "bash", "PowerShell":`），
// 而真实集合是 `{bash, monitor}`（monitor 是真工具、PowerShell 是幽灵名）。
// ⇒ 危险命令改走 `Monitor` 即绕过危险档 / 破坏性档 / allow 护栏 / 记忆禁令，
//
//	并被 PermissionStage 出口 (c) 的**真 SSOT** 免问放行（静默执行）。
//
// 根治方式是给这个事实一个**导出的、派生的**判据，10 处引用它。
// 本文件钉两件事，缺一条该判据就能悄悄退化：
//
//	① 派生恒等：IsCommandBearingTool 必须**等于**「具备 CapExec 的工具集」——
//	   防止有人日后把手抄名单塞回实现里（那时集合仍对，但派生关系断了，
//	   下一个人加 Exec 能力成员就会漏掉它）。
//	② 包含关系：sandbox-bounded ⊆ command-bearing（**被豁免的必被拦**）——
//	   这条不变量直接对应原缺陷：豁免出口认得 monitor、而拦截判定不认得。
package permission

import (
	"reflect"
	"testing"
)

// TestGuard_CommandBearingIsDerivedFromExecCapability 是派生恒等守卫。
//
// 直接遍历未导出的 toolCapabilities（同包测试可见），逐个工具核对：
// 「能力表里有 CapExec」 ⇔ 「IsCommandBearingTool 为真」。
//
// 为什么不用 `CommandBearingTools()` 自证：那会变成"拿派生结果验派生结果"的循环。
// 必须回到**能力表的原始事实**上比对。
func TestGuard_CommandBearingIsDerivedFromExecCapability(t *testing.T) {
	for tool, caps := range toolCapabilities {
		want := false
		for _, c := range caps {
			if c == CapExec {
				want = true
				break
			}
		}
		if got := IsCommandBearingTool(tool); got != want {
			t.Fatalf("IsCommandBearingTool(%q) = %v，但能力表 %v 的 CapExec 归属为 %v ⇒ "+
				"判据已与能力表脱钩（有人把手抄名单塞回了实现）", tool, got, caps, want)
		}
	}

	// 反向：集合视图必须与判据逐项一致（防止 CommandBearingTools 被单独改动）。
	fromSet := map[string]bool{}
	for _, tool := range CommandBearingTools() {
		fromSet[tool] = true
		if !IsCommandBearingTool(tool) {
			t.Fatalf("CommandBearingTools() 里的 %q 判据为 false ⇒ 两个视图不一致", tool)
		}
	}
	for tool := range toolCapabilities {
		if IsCommandBearingTool(tool) && !fromSet[tool] {
			t.Fatalf("判据为 true 的 %q 不在 CommandBearingTools() 里 ⇒ 两个视图不一致", tool)
		}
	}
}

// TestGuard_SandboxBoundedIsCommandBearing 是**承重**守卫：
// 凡"效果全在沙箱内因而可免问"的工具，必须同时被"危险/破坏性命令拦截"认出来。
//
// 若本条变红，含义是出现了「**免问面大于拦截面**」的缺口 —— 那就是 ★S-1 的原形：
// `Monitor` 被出口 (c) 免问，却不被 DangerousGuardStage / checkDestructive 认作命令载体，
// 于是 `Monitor({"command":"rm -rf x"})` 在沙箱生效时**零确认执行**。
// 修法有且只有两种：要么该工具退出 sandbox-bounded 表（不再免问），
// 要么给它补上命令载体归属 —— **不得**靠"再补一处工具名对"绕过本条。
func TestGuard_SandboxBoundedIsCommandBearing(t *testing.T) {
	for _, tool := range SandboxBoundedTools() {
		if !IsCommandBearingTool(tool) {
			t.Fatalf("sandbox-bounded 工具 %q 不是命令载体 ⇒ 免问面大于拦截面："+
				"它会被 PermissionStage 出口 (c) 免问，却逃过危险档/破坏性档判定的工具名匹配。"+
				"这正是 ★S-1（Monitor 绕过危险档）的缺口形态。", tool)
		}
	}
}

// TestGuard_IsCommandBearingToolNormalizesAndFailsClosed 规范化与默认拒绝：
//   - 大小写 / 首尾空白不敏感（链路里工具名大小写不一：注册名 `Monitor`、能力表键 `monitor`）；
//   - **未登记的工具一律 false**（fail-closed）—— 不认识的工具保持各档原有行为。
func TestGuard_IsCommandBearingToolNormalizesAndFailsClosed(t *testing.T) {
	for _, name := range []string{"bash", "Bash", "BASH", "  bash  ", "Monitor", "monitor", "\tMONITOR\n"} {
		if !IsCommandBearingTool(name) {
			t.Errorf("IsCommandBearingTool(%q) = false, want true（规范化应与 CapabilitiesOf 一致）", name)
		}
	}
	for _, name := range []string{"", "   ", "read_file", "write_file", "webfetch", "mcpcalltool", "n040-does-not-exist", "bash2"} {
		if IsCommandBearingTool(name) {
			t.Errorf("IsCommandBearingTool(%q) = true, want false（未登记工具必须 fail-closed）", name)
		}
	}
}

// TestGuard_CommandBearingToolSetIsPinned 钉死当前集合。
//
// 与 TestGuard_SandboxBoundedToolSetIsPinned 同一个理由：**故意**写死字面量，
// 让任何新增/删除都必须来改这一行。
//
// ⚠️ 注意本集合的语义方向与 sandbox-bounded **相反**：
//
//	sandbox-bounded 新增成员 = **扩大免问面**（更宽松，要挡住）；
//	命令载体新增成员        = **扩大拦截面**（更严格，通常无害，但要确认新成员
//	                         的入参真的只有一条命令串，否则 extractCommand 会取空）。
//
// 若你正在读这条失败信息：确认新工具入参确有 `command` 字段（`extractCommand` 才有东西可读），
// 再更新 want。
func TestGuard_CommandBearingToolSetIsPinned(t *testing.T) {
	want := []string{"bash", "monitor"}
	got := CommandBearingTools()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("命令载体集合已变：got=%v want=%v\n"+
			"新增成员 = 扩大**拦截**面（方向比 sandbox-bounded 安全），但仍须确认该工具入参含 command 字段。", got, want)
	}
}
