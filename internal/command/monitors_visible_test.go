package command

import (
	"strings"
	"testing"
)

// monitors_visible_test.go —— `/monitors` 家族的**命令面可发现性**判据。
//
// 背景（真机反馈，用户原话）：
//
//	「/monitors 的相关命令并没有登记到，我不知道 /help 里是否有，反正命令提示那边没有」
//
// 实测（改动前）：
//
//	r.Get("monitors").Hidden == true
//	默认 /help 可见 51 条，其中**没有** /monitors
//	弹窗候选（= registry.All() 过滤 Stub/Hidden）里 `mon*` **为空**
//	r.Completion("monitors ") == nil（commandCompletions 无该家族）
//
// 根因不是"漏注册"，而是**分类轴的取值本身有缺陷**：V0.1-HIDDEN 按「产品显要度」决定是否隐藏，
// 而 `/monitors stop <id>` 是**单个后台监控进程的唯一停止出口**（TUI 侧
// `internal/tui/format.go handleMonitors` 是 `MonitorMgr().Stop()` 的唯一用户入口）。
// 「唯一出口」这件事在「显要度」轴上没有取值 ⇒ 被当成"小众调参命令"隐藏。
// 上一轮已把这条申请写进 内部工作回执；
// 本单即该申请的落地。
//
// ★ 本文件只钉**注册面**（help / 补全 / 元数据）。真机弹窗与分发在
// internal/tui/monitors_discoverability_test.go（生产入口 `updateTypeaheadSuggestions` /
// `Model.handleCommand`）。
//
// ★★ 本文件同时**有意翻转**上一条回执的结论「Usage 保持 `/monitors [stop <id>]` 逐字不变、
// 不新增 tail/show」——翻转理由见 receipt（内部工作回执 §2）：
// 该结论的前提是「注册侧即使接线也无法提供 tail」，但 TUI 由 `isTUICommandHandledLocally("monitors")`
// **短路**、从不走注册侧 Handler ⇒ Usage 声明的是**命令面能力**而非**回调能力**；
// 而 TUI 确实支持 tail（`/monitors` 自己的输出就在宣传 `/monitors tail <id>`）。
// 「能做到却不声明」与「声明了做不到」是同一枚硬币的两面，都属 。

// monitorsFamily 是本轮登记进命令面的家族全名。
var monitorsFamily = []string{"monitors", "monitors stop", "monitors tail"}

// TestMonitorsFamilyRegisteredVisibleAndCarriable 结构面：家族成员齐备、可见、可承载。
func TestMonitorsFamilyRegisteredVisibleAndCarriable(t *testing.T) {
	r := NewRegistry()
	for _, name := range monitorsFamily {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("/%s 未注册 —— app.go handleCommand 的 cmdRegistry.Get(name) 闸会直接判 Unknown", name)
			continue
		}
		if cmd.Hidden {
			t.Errorf("/%s 仍为 Hidden: true ⇒ 不进 /help、不进 `/` 弹窗、不进 did-you-mean（用户看不到 = 等于没有）", name)
		}
		if cmd.Handler == nil {
			t.Errorf("/%s Handler 为 nil（手输也不可执行）", name)
		}
		if cmd.DescriptionEN == "" {
			t.Errorf("/%s 缺 DescriptionEN ⇒ 弹窗只能落回中文单语（TestBuiltinCommandsHaveBilingualSuggestionMetadata）", name)
		}
		if containsHan(cmd.DescriptionEN) {
			t.Errorf("/%s 的 DescriptionEN 含汉字：%q", name, cmd.DescriptionEN)
		}
		if cmd.Description == "" || isEnglish(cmd.Description) {
			t.Errorf("/%s 的 Description 必须为中文辅行，得到 %q", name, cmd.Description)
		}
	}
}

// TestMonitorsFamilyVisibleInDefaultHelp 结果面：默认 /help（不带 --all）必须列全家族。
//
// ★ 断言为什么不能用 `strings.Contains(help, " /monitors ")`：
// 那个串是 ` /monitors stop …` 的**前缀** ⇒ 家族根被隐藏、只剩子命令时**照样为真**（假绿）。
// 2026-09-30 用 overlay 变体 K（只把根重新标 Hidden）实测复现过这个假通过，故改为
// **整行对 Description 精确匹配 + Usage 原文匹配**：一行既要 `/name` 开头，又要以该命令
// 自己的中文描述**结尾**，这样 root / stop / tail 三行彼此不可互顶。
func TestMonitorsFamilyVisibleInDefaultHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")

	// 尾部提示双语化后主行为英文，中文为缩进辅行 —— 中文子串仍在。
	if !strings.Contains(help, "使用 /help --all 显示桩命令与隐藏命令") {
		t.Fatal("前置失败：默认 /help 尾部提示缺失，夹具可能已变")
	}
	for _, name := range monitorsFamily {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("/%s 未注册（本判据的前置）", name)
			continue
		}
		// ① Usage 原文必须出现（FormatHelp 把 Usage 单独打一行）。
		if usage := strings.TrimSpace(cmd.Usage); usage != "" && !strings.Contains(help, usage) {
			t.Errorf("默认 /help 未列出 /%s 的 Usage %q", name, usage)
		}
		// ② 必须存在一行：以 `/name` 开头、以该命令的**英文描述**结尾（双语化后主行 = EN）。
		found := false
		for _, line := range strings.Split(help, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "/"+name+" ") && strings.HasSuffix(trimmed, cmd.DescriptionEN) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("默认 /help 没有 `/%s …%s` 这一行（反例：把它重新标 Hidden，本条必红）",
				name, cmd.DescriptionEN)
		}
		// ③ 中文辅行：紧跟主行块内必须存在以缩进开头、以该命令中文描述结尾的行。
		foundZH := false
		for _, line := range strings.Split(help, "\n") {
			if strings.HasPrefix(line, "   ") && strings.HasSuffix(strings.TrimSpace(line), cmd.Description) {
				foundZH = true
				break
			}
		}
		if !foundZH {
			t.Errorf("默认 /help 缺少 /%s 的中文辅行（%q）—— 双语形态被破坏", name, cmd.Description)
		}
	}
}

// TestMonitorsFamilyHintedInCompletion 声明面：commandCompletions 必须声明家族子命令。
//
// ★ 这条**当前不驱动真机弹窗**：生产弹窗走 internal/tui/completion_popup.go:249 的
// `registry.All()` + typeahead.SuggestCommands，`Registry.Completion` 全仓**无生产调用点**
// （只有测试调用）。保留本判据是为了让"两份补全声明"不至于当场漂移；
// 「两套补全实现、其中一套是死码」这一结构问题已在回执 §5 单独登记，本单不拆。
func TestMonitorsFamilyHintedInCompletion(t *testing.T) {
	r := NewRegistry()
	// 一级（`Completion` 用 strings.Fields，尾随空格被吞 ⇒ 走 prefixMatch）：返回注册全名。
	if got := r.Completion("monitors "); !containsSlice(got, "monitors stop") || !containsSlice(got, "monitors tail") {
		t.Errorf("Completion(\"monitors \") 缺家族全名，实际 %v", got)
	}
	// 二级（两段输入）：返回声明表的子命令。
	if got := r.Completion("monitors s"); !containsSlice(got, "stop") {
		t.Errorf("Completion(\"monitors s\") 应含 stop，实际 %v", got)
	}
	if got := r.Completion("monitors t"); !containsSlice(got, "tail") {
		t.Errorf("Completion(\"monitors t\") 应含 tail，实际 %v", got)
	}
	// 声明面不得比能力面多：`show` 只是 handleMonitors 的兼容别名，未登记 ⇒ 不得在此被承诺。
	if got := r.Completion("monitors s"); containsSlice(got, "show") {
		t.Errorf("Completion(\"monitors s\") 不该含 show（未登记为命令、不对外宣传），实际 %v", got)
	}
}

// TestMonitorsSubcommandHandlersForwardArgs 能力面：注册侧能表达 stop/tail 的完整语义。
func TestMonitorsSubcommandHandlersForwardArgs(t *testing.T) {
	cases := map[string]string{
		"monitors":      "stop m-1",
		"monitors stop": "m-1",
		"monitors tail": "m-1",
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry()
			cmd, ok := r.Get(name)
			if !ok {
				t.Fatalf("/%s 未注册", name)
			}
			var got string
			called := false
			r.OnMonitors = func(a string) string { called = true; got = a; return "ok" }
			if _, err := cmd.Handler(args); err != nil {
				t.Fatalf("/%s 执行失败: %v", name, err)
			}
			if !called {
				t.Fatalf("已接线时 /%s 必须调用 OnMonitors", name)
			}
			// 家族子命令必须把**动词**补回给回调（回调只认 `stop <id>` 形态；
			// TUI 侧同一个 handleMonitors 也是按动词前缀分派的）。
			want := args
			if name == "monitors stop" {
				want = "stop " + args
			}
			if name == "monitors tail" {
				want = "tail " + args
			}
			if got != want {
				t.Errorf("/%s 转发的 args = %q，期望 %q", name, got, want)
			}
		})
	}
}
