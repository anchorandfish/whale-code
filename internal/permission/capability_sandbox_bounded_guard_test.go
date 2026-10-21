// Package permission — 「沙箱边界内工具」判据的守卫。
//
// 为什么需要这组守卫：`sandboxBoundedTools` 是审批**免问**的依据 —— 它一旦放宽，
// 被放宽的那个工具就会在"沙箱真在强制"时**不再询问用户**。而它与
// `CapabilitiesOf()`/`CapExec`（deny 规则的 SSOT）**刻意不共用一张表**，
// 所以两者之间的包含关系没有任何编译期保证 ⇒ 必须由测试钉住。
//
// 三条守卫各自防一种失效：
// 1. 包含关系：本表的工具**必须是** CapExec ⇒ 防止"效果只有一个受沙箱约束的进程"
// 这一前提被悄悄破坏（例如把某个纯进程内工具塞进来）。
// 2. 集合钉子：新增成员必须是一次**显式决策**（改测试）而非随手加一行。
// 3. 负例：已知"有沙箱管不住的效果通道"的工具不得混入。
package permission

import (
	"reflect"
	"testing"
)

// TestGuard_SandboxBoundedToolsAreExecCapable 是本文件的核心不变量：
// **沙箱边界内工具集 ⊆ CapExec**。
//
// 理由：免问的正当性来自「它的可观察效果**只有**『跑一个被沙箱包住的进程』这一条通道」。
// 若某工具并不具备在本机执行的能力（或压根未登记能力），那"被沙箱兜住"就无从谈起 ⇒
// 免问失去依据。反向**不成立**（CapExec 里可以有本表之外的成员，如 `TaskCreate`），
// 所以只断言单向包含。
func TestGuard_SandboxBoundedToolsAreExecCapable(t *testing.T) {
	for _, tool := range SandboxBoundedTools() {
		caps := CapabilitiesOf(tool)
		if len(caps) == 0 {
			t.Fatalf("sandbox-bounded 工具 %q 未登记在 toolCapabilities 中：既无法证明它的效果全在沙箱内，"+
				"也让 deny 规则（含 Exec(*)）漏掉它", tool)
		}
		found := false
		for _, c := range caps {
			if c == CapExec {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("sandbox-bounded 工具 %q 的能力是 %v，不含 CapExec ⇒ "+
				"「效果只有一个受沙箱约束的进程」这一前提不成立，免问判据失效", tool, caps)
		}
	}
}

// TestGuard_SandboxBoundedToolSetIsPinned 钉死当前集合。
//
// 这个测试**故意**写死字面量（而不是从表派生）：它的作用就是让任何新增/删除
// 都必须来改这一行 —— 强迫作者面对"这个新工具真的没有第二条绕过沙箱的通道吗"。
// 若你正在读这条失败信息：请先确认该工具**没有**进程内持久化写、没有直连网络，
// 再更新 want 并在 §4 登记表单独立项。
func TestGuard_SandboxBoundedToolSetIsPinned(t *testing.T) {
	want := []string{"bash", "monitor"}
	got := SandboxBoundedTools()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox-bounded 集合已变：got=%v want=%v\n"+
			"新增成员等于**扩大免问面**，必须是一次显式决策（改本行 + 登记单独立项），不得随手加。", got, want)
	}
}

// TestGuard_SandboxBoundedExcludesInProcessOrUnwrappedTools 负例：
// 有"沙箱管不住的效果通道"的工具不得混入本表。
//
// 每项都写明**为什么**它不能进来 —— 否则后人只会看到一串"不许"，不知道边界在哪。
func TestGuard_SandboxBoundedExcludesInProcessOrUnwrappedTools(t *testing.T) {
	cases := []struct {
		tool string
		why  string
	}{
		{"taskcreate", "会跑后台命令**且**把任务状态写进 ~/.whale/（进程内，沙箱管不到）"},
		{"todowrite", "写 agent 自身 todo 状态（进程内）"},
		{"crondelete", "改进程内/磁盘上的 cron 状态"},
		{"schedule_cron", "注册持久化定时任务（跨会话生效）"},
		{"config_set", "可改 permission_level 等持久配置，属提权面"},
		{"write_file", "写的是**用户数据**：沙箱只管「能不能写」，管不了「该不该写这个文件」"},
		{"edit_file", "同上"},
		{"apply_unified_diff", "同上（且目标路径从补丁文本扫描得出）"},
		{"git_commit", "提交是**仓库历史**层面的不可逆动作，沙箱不承担该判断"},
		{"mcpcalltool", "RPC 落到**外部 MCP 服务**，whale-code 不保证它被本机 OS 沙箱包住"},
	}

	for _, c := range cases {
		if IsSandboxBoundedTool(c.tool) {
			t.Fatalf("%q 不得进 sandbox-bounded 集合：%s", c.tool, c.why)
		}
	}
}

// TestGuard_IsSandboxBoundedToolNormalizesAndFailsClosed 判据的规范化与默认拒绝：
// - 大小写/首尾空白不敏感（工具名在链路上大小写不一，见 executor.go 的 "Monitor"）；
// - **未登记的工具一律 false**（fail-closed）—— 不认识的工具保持原有确认行为。
func TestGuard_IsSandboxBoundedToolNormalizesAndFailsClosed(t *testing.T) {
	for _, name := range []string{"bash", "Bash", "BASH", "  bash  ", "Monitor", "monitor", "\tMONITOR\n"} {
		if !IsSandboxBoundedTool(name) {
			t.Errorf("IsSandboxBoundedTool(%q) = false, want true（规范化应与 CapabilitiesOf 一致）", name)
		}
	}
	for _, name := range []string{"", "   ", "read_file", "webfetch", "n040-does-not-exist", "bash2"} {
		if IsSandboxBoundedTool(name) {
			t.Errorf("IsSandboxBoundedTool(%q) = true, want false（未登记工具必须 fail-closed）", name)
		}
	}
}
