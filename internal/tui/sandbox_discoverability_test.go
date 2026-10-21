package tui

import (
	"strings"
	"testing"
)

// sandbox_discoverability_test.go —— `/sandbox status` 在两个生产 UI 上的可发现性
// （2026-10-14，用户直报「/sandbox status 显示里只有 /sandbox」）。
//
// 判例同构：monitors_discoverability_test.go（09-30，/monitors stop/tail 家族登记）。
// 三个出口：① `/` 弹窗（注册名前缀匹配）② /help（Usage 行，判据在 command 包）
// ③ 命令回执（unknown 参数回用法）。本文件盖 ①③ 与两个 UI 的分发/回执两侧。
//
// ★ 双实现纪律：/sandbox 在 TUI（Model.handleSandbox）与 plain
// （runnerCore.handleSandbox）各有一份实现 ⇒ 本文件**两侧都验**，只验一侧 = 半修。

// TestSandboxStatusAppearsInSlashCompletionPopup 出口①：敲 `/sandbox s` 时弹窗必须提示
// `sandbox status`（这正是用户报的「显示里只有 /sandbox」）。
func TestSandboxStatusAppearsInSlashCompletionPopup(t *testing.T) {
	m := newTUIRegressionModel(t)

	m.textarea.SetValue("/sandbox s")
	m.updateTypeaheadSuggestions()

	state := m.suggestionState
	if !state.Visible {
		t.Fatal("`/sandbox s` 未弹出命令提示（弹窗不可见）")
	}
	got := findCompletion(state.Completions, "sandbox status")
	if got == nil {
		t.Errorf("命令提示里没有 \"sandbox status\"；实际候选 = %v",
			completionDisplayNames(state.Completions))
		return
	}
	// 弹窗主行取 DescriptionEN（completion_popup.go:255）—— 缺失 = 主行空白。
	if strings.TrimSpace(got.Description) == "" {
		t.Errorf("弹窗里 sandbox status 的英文主行为空：%+v", got)
	}
}

// TestSandboxStatusDispatchesToTUIHandler TUI 分发面：注册全名后必须落到同一个本地实现，
// 且不得多执行注册侧 Handler（漏进 isTUICommandHandledLocally = 副作用重复，见 app.go 表注释）。
func TestSandboxStatusDispatchesToTUIHandler(t *testing.T) {
	for _, name := range []string{"sandbox", "sandbox status"} {
		if !isTUICommandHandledLocally(name) {
			t.Errorf("/%s 不在 isTUICommandHandledLocally 列表内 ⇒ 注册侧 Handler 会被多执行一次"+
				"（该表语义见 app.go 函数文档）", name)
		}
	}

	m := newTUIRegressionModel(t)
	if m.cmdRegistry == nil {
		t.Fatal("夹具失效：cmdRegistry 为 nil")
	}
	out := m.handleCommand("/sandbox status")
	if strings.Contains(out, "Unknown:") {
		t.Errorf("/sandbox status → %q：命令未注册或 switch 断链（Parse/Dispatch 断链）", out)
	}
	// 防止「只登记、没接 switch 分支」：那种情况 output 停在注册侧 Handler 的 TUI-only 提示上。
	if strings.Contains(out, "not wired") || strings.Contains(out, "only available in interactive") {
		t.Errorf("/sandbox status → %q：注册侧未接线提示漏到用户面前 —— 登记了名字却没有对应 switch 分支", out)
	}
	// 正向判据：状态查询必然带两套门的真实状态（sandboxGateStatus，sandbox_toggle.go）。
	if !strings.Contains(out, "legacy=") || !strings.Contains(out, "manager=") {
		t.Errorf("/sandbox status → %q：应显示两套门状态（legacy=…, manager=…）", out)
	}
}

// TestSandboxUnknownArgReturnsUsageHint 出口③（TUI 侧）：敲错参数必须回用法，
// 而不是把「用户敲错了」装作「status 查询」无声吞掉（对照 /mouse 的既有形态）。
func TestSandboxUnknownArgReturnsUsageHint(t *testing.T) {
	m := newTUIRegressionModel(t)
	out := m.handleCommand("/sandbox offf")
	if !strings.Contains(out, "Usage: /sandbox") {
		t.Errorf("/sandbox offf → %q：未知参数应回 Usage（出口③：命令要自我宣传用法）", out)
	}
	// 合法参数不得被误伤：on/off/status 与裸敲都不得出现 Usage 提示。
	for _, input := range []string{"/sandbox", "/sandbox on", "/sandbox off", "/sandbox status"} {
		if got := m.handleCommand(input); strings.Contains(got, "Usage: /sandbox") {
			t.Errorf("%s → 合法输入被误报用法提示：%q", input, got)
		}
	}
}

// TestPlainSandboxUnknownArgReturnsUsageHint 出口③（plain 侧）：同一份提示必须在
// --plain 侧同样出现（：同一命令两 UI 各写各的必然漂移，两侧都验）。
func TestPlainSandboxUnknownArgReturnsUsageHint(t *testing.T) {
	r := newG12bPlainRunner(t)
	out := r.dispatchCommand("sandbox", "offf", "")
	if !strings.Contains(out, "Usage: /sandbox") {
		t.Errorf("--plain /sandbox offf → %q：未知参数应回 Usage（与 TUI 侧同形）", out)
	}
	if got := r.dispatchCommand("sandbox", "status", ""); strings.Contains(got, "Usage: /sandbox") {
		t.Errorf("--plain /sandbox status → 合法输入被误报用法提示：%q", got)
	}
}

// TestPlainSandboxStatusDispatchesViaFullName plain 分发面：注册全名后
// `/sandbox status` 经 command.Parse 切成 (name="sandbox status") ⇒ plain 的调度表
// 必须有对应条目，否则用户看到的会是注册侧的 "only available in interactive TUI"
// —— 回归 today 之前 plain 本来就能跑的状态查询（这既是正向判据也是防回归判据）。
func TestPlainSandboxStatusDispatchesViaFullName(t *testing.T) {
	r := newG12bPlainRunner(t)
	// 模拟生产序列：handleCommand 先跑注册侧 Handler（TUI-only 提示），再 dispatchCommand 覆盖。
	const registryNotice = "STUB-REGISTRY-TUI-ONLY-NOTICE"
	out := r.dispatchCommand("sandbox status", "", registryNotice)
	if out == registryNotice {
		t.Fatal("plain 调度表缺 \"sandbox status\" 条目 ⇒ 全名分发断链，" +
			"用户会看到 TUI-only 提示而不是状态（today 之前 /sandbox status 在 plain 是能查的）")
	}
	if !strings.Contains(out, "legacy=") || !strings.Contains(out, "manager=") {
		t.Errorf("--plain /sandbox status → %q：应显示两套门状态（与 TUI 侧同源 sandboxGateStatus）", out)
	}
}
