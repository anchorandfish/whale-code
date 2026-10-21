package tui

import (
	"strings"
	"testing"
)

// monitors_discoverability_test.go —— `/monitors` 家族在**真机两个生产入口**上的可发现性。
//
// 生产入口（非手工构造）：
//
//	① 命令提示弹窗：`Model.updateTypeaheadSuggestions`（completion_popup.go:205）——
//
// 真机里用户按 `/` 后走的就是它；候选 = `registry.All()` 过滤 `Stub||Hidden`
// （completion_popup.go:252）→ `typeahead.SuggestCommands`。
//
//	② 命令分发：`Model.handleCommand`（app.go:3335）—— `command.Parse` → `Get(name)`
//
// → `isTUICommandHandledLocally(name)` ? 本地分支 : 注册侧 Handler。
//
// 改动前实测（用户真机反馈「命令提示那边没有」）：
//
//	弹窗候选里 `mon*` 为空（因为 /monitors 的 Hidden=true 被 completion_popup.go:252 跳过）；
//	默认 /help 也没有它（51 条可见命令里不含）。
//
// ★ 为什么必须**两条**子命令都登记成注册全名：弹窗匹配是**对注册名做前缀匹配**
// （typeahead.go:146），且 token 带尾随空格时会因"精确命中"直接返回 nil
// （typeahead.go:130-136）—— 所以只登记 `monitors` 时，敲 `/monitors ` 弹窗会**关闭**，
// 子命令永远不可能被提示。这是"登记了父命令 ≠ 子命令可见"的结构原因。
func TestMonitorsFamilyAppearsInSlashCompletionPopup(t *testing.T) {
	m := newTUIRegressionModel(t)

	// 用户敲 `/mon` 的那一刻，三个成员都必须在候选里（这才是"命令提示那边有"）。
	m.textarea.SetValue("/mon")
	m.updateTypeaheadSuggestions()

	state := m.suggestionState
	if !state.Visible {
		t.Fatal("`/mon` 未弹出命令提示（弹窗不可见）")
	}
	for _, want := range []string{"monitors", "monitors stop", "monitors tail"} {
		if findCompletion(state.Completions, want) == nil {
			t.Errorf("命令提示里没有 %q；实际候选 = %v",
				want, completionDisplayNames(state.Completions))
		}
	}

	// 子命令必须有英文主行（弹窗 primary 取 DescriptionEN，见 completion_popup.go:255）。
	stop := findCompletion(state.Completions, "monitors stop")
	if stop == nil {
		t.Fatal("monitors stop 缺失，后续断言无意义")
	}
	if strings.TrimSpace(stop.Description) == "" {
		t.Errorf("弹窗里 monitors stop 的英文描述为空：%+v", stop)
	}
}

// TestMonitorsSubcommandsDispatchToTUIHandler 分发面：家族子命令必须落到同一个本地实现。
//
// ★ 先纠正一个容易写错的因果（本文件第一版就写错了，用 overlay 变体 M 实测才发现）：
// `handleCommand` 的结构是「先判 `isTUICommandHandledLocally` 决定**是否执行注册侧 Handler**，
// 再无条件走 `switch name` 覆盖 `output`」（app.go:3347 与 :3355）。
// 因此「漏进本地列表」的后果**不是**用户看到 "not wired"（那段输出会被 switch 覆盖丢掉），
// 而是**注册侧 Handler 被多执行一次** —— 这正是 `isTUICommandHandledLocally` 注释写的
// "否则会产生双重撤销、重复安装等副作用"。对 monitors 家族，多执行 = 用空 id 走一次
// `OnMonitors("stop ")`（将来某入口一旦接线，就是真实的重复控制面调用）。
// 仓库已有 `TestTUICommandDispatchNeverFallsThroughToRegistryCallbacks` 守这条不变量，
// 本用例只补「monitors 家族也在其覆盖范围内」。
func TestMonitorsSubcommandsDispatchToTUIHandler(t *testing.T) {
	// ① 本地短路表必须含全家。
	for _, name := range []string{"monitors", "monitors stop", "monitors tail"} {
		if !isTUICommandHandledLocally(name) {
			t.Errorf("/%s 不在 isTUICommandHandledLocally 列表内 ⇒ 注册侧 Handler 会被多执行一次"+
				"（副作用重复；该表的语义见 app.go 该函数文档）", name)
		}
	}

	m := newTUIRegressionModel(t)
	if m.cmdRegistry == nil {
		t.Fatal("夹具失效：cmdRegistry 为 nil")
	}

	// ② 端到端：`handleCommand` 是 stream.go 里真机唯一转发点。
	// 缺 id 时必须给**用法**，而不是 Unknown / 未接线提示 / 「Failed to stop monitor:」。
	for _, c := range []struct{ input, wantUsage string }{
		{"/monitors stop", "Usage: /monitors stop <id>"},
		{"/monitors tail", "Usage: /monitors tail <id>"},
	} {
		out := m.handleCommand(c.input)
		if strings.Contains(out, "Unknown:") {
			t.Errorf("%s → %q：命令未注册（Parse/Dispatch 断链）", c.input, out)
		}
		// 这条防的是"只登记、没接 switch 分支"：那种情况下 output 会停在注册侧 Handler 的返回值上。
		// （注意它**不能**用来探测"漏进本地列表"——switch 会覆盖 output，见函数头注释。）
		if strings.Contains(out, "not wired") {
			t.Errorf("%s → %q：注册侧未接线提示漏到用户面前 —— 说明该名字被登记了却没有对应的 switch 分支", c.input, out)
		}
		if !strings.Contains(out, c.wantUsage) {
			t.Errorf("%s → %q：缺 id 时应回用法 %q", c.input, out, c.wantUsage)
		}
	}

	// ③ 反例守卫：未注册的家族成员不得被"宽容"——`/monitors show`（只在 handleMonitors 内部
	// 作为 tail 的兼容别名存在）不属于对外命令面，不应因本次改动而变成独立命令。
	out := m.handleCommand("/monitors show")
	if strings.Contains(out, "Unknown:") {
		t.Errorf("/monitors show → %q：兼容别名被本次改动破坏（应仍由 handleMonitors 兜住）", out)
	}
}
