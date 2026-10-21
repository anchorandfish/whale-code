package command

import (
	"strings"
	"testing"
)

// V0.1-HIDDEN：v0.1 命令面收敛。默认 /help 与补全只暴露核心命令集，
// 其余命令标记 Hidden（handler 保留，手输仍可执行，/help --all 可见）。

// v01VisibleBudget 是默认 /help 可见命令数上限
// ★ 2026-10-03：55 → 70（10-03 登记批，见审计件）。
// ★ 2026-10-12（，用户口径「1 版有的所有命令都要有，其他无承诺的都不要有」）：
// 70 → 54 精确收敛 = 09-08 验收基线 48 + /monitors 家族 3（09-30 拍板）
// + /security-preset（10-10 判据册）+ /skill（10-11 O-2 拍板）+ /memory（10-12 14:50 拍板恢复）。
// 10-03 登记批其余 14 条（/fast /daemon×8 /task×3 /session /session pick）收回隐藏，
// 登记见 内部工作回执。
// ★ 2026-10-14 **有意翻转**（54 → 55，/sandbox status 登记）——下一个人别当回归：
// 用户直报「/sandbox status 显示里只有 /sandbox」，status 是 /sandbox 家族的既有能力
// （handleSandbox default 分支），按用户拍板注册全名补承诺（判例同构 /monitors 家族）。
const v01VisibleBudget = 55

// hiddenCommandSamples 各组隐藏命令的代表性样本（覆盖 GitHub/自动化/Git/插件/调参/杂项六组）
var hiddenCommandSamples = []string{
	// GitHub 集成
	"/pr create", "/issue create", "/actions list", "/github watch", "/commit-push-pr",
	// 自动化/编排
	// ★ "/daemon start" 2026-10-03 曾移入 coreCommandSamples；2026-10-12按
	// 「v1 无承诺的都不要有」收回（不在 10-10 判据册承诺清单）。
	"/daemon start", "/task list", "/loop", "/cron add", "/batch", "/orchestrate", "/workflow",
	"/goal", "/bughunter start", "/ultraplan",
	// 本地 Git 高级
	"/commit", "/branch list", "/worktree add",
	// 插件
	"/plugin install", "/reload-plugins",
	// 调参/调试
	// ★ "/fast" 2026-10-03 曾移入 coreCommandSamples；2026-10-12收回。
	"/fast", "/effort", "/compact-strategy", "/export-format", "/env", "/fcache",
	// ★ 2026-09-30 已翻转：`/monitors` 原在本组（按「产品显要度」归为"小众调参"），
	// 但它是**单个后台监控进程的唯一停止出口**（TUI `format.go handleMonitors` 是
	// `MonitorMgr().Stop()` 的唯一用户入口）—— 隐藏它 ⇒ 默认 /help 与 `/` 弹窗都看不到，
	// 用户真机反馈「命令提示那边没有」。已取消隐藏并移入下面 coreCommandSamples。
	// 下一个人别把它当"漏标"改回去。
	"/stats", "/summary", "/agents", "/hooks", "/mouse", "/insights",
	// 杂项小众
	"/notify list", "/alias", "/files", "/skillify", "/share", "/import",
	// ★ "/task list" 已于 2026-10-03 移入 coreCommandSamples（子 Agent 任务唯一出口 + 与 /tasks 消歧）
	"/export-user", "/remote", "/verify", "/simplify", "/security-review",
	"/thinkback", "/output-style", "/migrate", "/privacy",
	// 用户确认砍掉的第一批
	"/search", "/session-split",
}

// coreCommandSamples v0.1 必须可见的核心命令样本
var coreCommandSamples = []string{
	"/help", "/compact", "/archive", "/clear", "/exit", "/version", "/status",
	"/doctor", "/think", "/context", "/usage", "/onboarding", "/tasks",
	"/upgrade", "/feedback", "/model", "/models",
	"/sessions", "/session save", "/session switch", "/resume", "/rename",
	"/permissions", "/sandbox", "/export", "/copy", "/add-dir",
	"/mcp", "/terminal-setup", "/config", "/diff", "/skills", "/rewind",
	"/undo", "/redo", "/init", "/theme", "/tool", "/review", "/history",
	"/checkpoint",
	// 2026-09-30 从 hiddenCommandSamples 移入：唯一停止出口不得不可发现（见上注释）。
	"/monitors", "/monitors stop", "/monitors tail",
	// v1 承诺面（口径收口后仅存的 48 基线之外增量）：
	// /security-preset —— 10-10 判据册 §2 点名手测；/skill —— 10-11 O-2 拍板（信任门可见出口）。
	// 10-03 登记批（/memory /fast /daemon×8 /task×3）已于 2026-10-12 收回隐藏（移回上表）。
	"/security-preset", "/skill",
}

// TestFormatHelp_HideHiddenByDefault 结果断言：默认 /help 输出中隐藏组全部消失、核心组全部保留
func TestFormatHelp_HideHiddenByDefault(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")

	for _, name := range hiddenCommandSamples {
		if containsString(help, name) {
			t.Errorf("默认 /help 不应显示隐藏命令 %s", name)
		}
	}
	for _, name := range coreCommandSamples {
		if !containsString(help, name) {
			t.Errorf("默认 /help 应保留核心命令 %s", name)
		}
	}
}

// TestFormatHelp_VisibleBudget 结果断言：默认 /help 可见命令数收敛到 v0.1 预算内
func TestFormatHelp_VisibleBudget(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	count := 0
	for _, line := range strings.Split(help, "\n") {
		// 命令行格式：两个空格开头 + /name（快捷键行无 / 前缀，Usage 续行无 / 开头）
		if strings.HasPrefix(line, "  /") {
			count++
		}
	}
	if count == 0 {
		t.Fatal("默认 /help 应至少显示一条命令")
	}
	if count > v01VisibleBudget {
		t.Errorf("默认 /help 可见命令 %d 条，超出 v0.1 预算 %d 条", count, v01VisibleBudget)
	}
}

// TestFormatHelp_ShowAllWithHidden --all 显示隐藏命令及 [隐藏] 后缀
func TestFormatHelp_ShowAllWithHidden(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "--all")

	if !containsString(help, "/memory") {
		t.Error("/help --all 应显示隐藏命令 /memory")
	}
	// C-6双语化：标记改为 [hidden 隐藏]（★ 有意翻转，勿当回归）
	if !containsString(help, "[hidden 隐藏]") {
		t.Error("/help --all 输出应包含 [hidden 隐藏] 后缀")
	}
}

// TestCompletion_ExcludesHidden 结果断言：补全候选不含隐藏命令，可见命令不受影响
func TestCompletion_ExcludesHidden(t *testing.T) {
	r := NewRegistry()

	// ★ 2026-10-03 曾移除 memory/fast/daemon add/task list；2026-10-12随收回加回
	hiddenNames := map[string]bool{
		"loop": true, "commit": true,
		"output-style": true, "search": true, "privacy": true,
		"stats": true, "summary": true, "alias": true, "export-user": true,
		"pr create": true, "plugin install": true,
		"session merge": true, "worktree add": true,
		"fast": true, "daemon add": true, "task list": true,
		"session": true, "session pick": true,
	}

	// 一级补全（空输入与前缀输入）均不出现隐藏命令
	for _, input := range []string{"", "/", "s", "da", "pr", "me", "task"} {
		for _, name := range r.Completion(input) {
			if hiddenNames[name] {
				t.Errorf("补全输入 %q 不应返回隐藏命令 %q", input, name)
			}
		}
	}

	// 纯隐藏家族：一级前缀与二级子命令均为空
	// 注：尾随空格会被 strings.Fields 吞掉，"daemon " 走一级 prefixMatch，"daemon s" 走二级子命令过滤，两条路径都要盖住
	// ★ 2026-10-03 曾移出；2026-10-12随家族收回加回
	for _, input := range []string{
		"daemon ", "daemon s", "task l", "task c", "session p",
		"plugin ", "plugin l", "github ", "github w",
		"actions ", "actions l", "issue ", "issue c", "worktree ", "worktree a",
		"notify ", "notify l", "bughunter ", "bughunter s", "cron ", "cron a",
	} {
		if got := r.Completion(input); got != nil && len(got) > 0 {
			t.Errorf("隐藏家族补全输入 %q 应返回空，实际 %v", input, got)
		}
	}

	// 单命令隐藏但有 ArgCompleter 的根也返回空（★ "fast " 2026-10-12 随收回加回）
	for _, input := range []string{"effort ", "output-style ", "privacy ", "fast "} {
		if got := r.Completion(input); got != nil {
			t.Errorf("隐藏单命令补全输入 %q 应返回空，实际 %v", input, got)
		}
	}

	// 混合家族：session 根 + pick 已收回隐藏，merge 仍隐藏，save/list/switch 可见
	// 一级路径：全名级别无 "session merge"/"session pick"/"session"，有 "session save"/"session switch"
	prefixSubs := r.Completion("session ")
	if containsSlice(prefixSubs, "session merge") {
		t.Errorf("session 一级补全不应包含隐藏的 session merge，实际 %v", prefixSubs)
	}
	if containsSlice(prefixSubs, "session pick") {
		t.Errorf("session 一级补全不应包含收回的 session pick，实际 %v", prefixSubs)
	}
	if !containsSlice(prefixSubs, "session switch") || !containsSlice(prefixSubs, "session save") {
		t.Errorf("session 一级补全应保留 session switch/save，实际 %v", prefixSubs)
	}
	// 二级路径：session 根已收回隐藏 ⇒ Registry.Completion 整体返回 nil（V0.1-HIDDEN 自洽行为：
	// isRegisteredHidden(cmdName) 短路）。真机弹窗不受影响 —— 它走 registry.All() + typeahead
	// 按注册全名逐条过滤（completion_popup.go），save/switch 可见照常提示。
	subs := r.Completion("session s")
	if containsSlice(subs, "merge") || containsSlice(subs, "pick") {
		t.Errorf("session 子命令补全不应包含隐藏/收回子命令，实际 %v", subs)
	}

	// 可见家族不受影响
	if got := r.Completion("checkpoint "); len(got) == 0 {
		t.Error("可见命令 checkpoint 的子命令补全不应为空")
	}
	if got := r.Completion("mcp "); len(got) == 0 {
		t.Error("可见命令 mcp 的子命令补全不应为空")
	}
}

// TestHiddenCommands_StillCallable 隐藏命令 handler 保留，手输仍可执行（用户已人工验证过 /output-style、/search）
func TestHiddenCommands_StillCallable(t *testing.T) {
	r := NewRegistry()
	// ★ 2026-10-03 曾移除；2026-10-12随收回加回 —— 收回只影响可见性，手输仍可执行
	// （/memory 已于同日 14:50 拍板恢复可见 ⇒ 移出本"隐藏仍可调用"样本表）
	for _, name := range []string{"output-style", "search", "commit", "fast"} {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("隐藏命令 /%s 应在注册表中存在", name)
			continue
		}
		if !cmd.Hidden {
			t.Errorf("命令 /%s 应标记 Hidden: true", name)
		}
		if cmd.Handler == nil {
			t.Errorf("隐藏命令 /%s 的 handler 不应为 nil（手输仍需可执行）", name)
		}
	}
	// 已人工验证过的两条：handler 真实返回非空输出（/output-style 无参显示当前风格，/search CONTEXT 返回无匹配提示）
	for _, name := range []string{"output-style", "search"} {
		cmd, ok := r.Get(name)
		if !ok {
			continue
		}
		arg := "CONTEXT"
		if name == "output-style" {
			arg = ""
		}
		out, err := cmd.Handler(arg)
		if err != nil {
			t.Errorf("隐藏命令 /%s 调用失败: %v", name, err)
			continue
		}
		if out == "" {
			t.Errorf("隐藏命令 /%s 应返回非空输出", name)
		}
	}
}

// TestHiddenFamilies_UnhideRestoresCompletion 隐藏只影响可见性：取消隐藏后子命令补全必须恢复（保护 commandCompletions 接线不被隐藏破坏）
func TestHiddenFamilies_UnhideRestoresCompletion(t *testing.T) {
	r := NewRegistry()
	// 家族根名（如 daemon）本体未注册，注册的是全名（daemon start 等）：按前缀取消整个家族
	unhideFamily := func(root string) {
		found := false
		for name, cmd := range r.commands {
			if name == root || strings.HasPrefix(name, root+" ") {
				cmd.Hidden = false
				r.commands[name] = cmd
				found = true
			}
		}
		if !found {
			t.Fatalf("家族 /%s 应至少有一条命令注册", root)
		}
	}

	// 家族（SubCommands 型）：s 前缀根补全恢复
	for _, name := range []string{"orchestrate", "workflow"} {
		unhideFamily(name)
		if got := r.Completion(name + " s"); len(got) == 0 {
			t.Errorf("%s 取消隐藏后子命令补全应恢复（s 前缀）", name)
		}
	}
	// 每个家族用其实际存在的子命令前缀验证恢复
	// l 前缀：daemon list / plugin list / task list / loop list / notify list / cron list / actions logs / issue list / worktree list / hooks list
	for _, name := range []string{"daemon", "plugin", "task", "loop", "notify", "cron", "pr", "issue", "actions", "worktree", "hooks"} {
		unhideFamily(name)
		if got := r.Completion(name + " l"); len(got) == 0 {
			t.Errorf("%s 取消隐藏后子命令补全应恢复（l 前缀）", name)
		}
	}
	// s 前缀：github watch/stop/status、bughunter start/stop/status
	for _, name := range []string{"github", "bughunter"} {
		unhideFamily(name)
		if got := r.Completion(name + " s"); len(got) == 0 {
			t.Errorf("%s 取消隐藏后子命令补全应恢复（s 前缀）", name)
		}
	}

	// 混合家族：session merge 取消隐藏后 merge 子命令回归
	// ★ ：session 根也已收回隐藏 ⇒ 夹具须连根一起取消隐藏
	//（isRegisteredHidden(cmdName) 会把隐藏根的二级补全整体短路为 nil）。
	unhideFamily("session")
	if got := r.Completion("session m"); !containsSlice(got, "merge") {
		t.Errorf("session merge 取消隐藏后应回归子命令补全，实际 %v", got)
	}
}

func containsSlice(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// TestSuggest_ExcludesHiddenAndStub P1-1（对抗式审查）：Did-you-mean 建议不得指路隐藏/桩命令。
// 三个生产入口（TUI app.go / plain / bridge 的未知命令分支）都调 Suggest——
// 用户拼错隐藏命令前缀时若被主动建议，等于绕过命令面收敛。
func TestSuggest_ExcludesHiddenAndStub(t *testing.T) {
	r := NewRegistry()

	// 拼错的隐藏/桩命令前缀：结果中不得出现任何隐藏/桩命令
	for _, input := range []string{"privac", "memor", "effor", "stat", "lo", "pl", ""} {
		for _, name := range Suggest(input, r) {
			if cmd, ok := r.commands[name]; ok && (cmd.Hidden || cmd.Stub) {
				t.Errorf("Suggest(%q) 不应建议隐藏/桩命令 %q", input, name)
			}
		}
	}

	// 纯隐藏前缀：返回空（无可建议项）而非指路隐藏命令
	// ★ "memor" 2026-10-12 14:50 随 /memory 恢复可见，翻回正向断言「应建议 memory」
	if got := Suggest("privac", r); len(got) != 0 {
		t.Errorf("Suggest(\"privac\") 应为空，实际 %v", got)
	}
	if got := Suggest("memor", r); !containsSlice(got, "memory") {
		t.Errorf("Suggest(\"memor\") 应建议 memory，实际 %v", got)
	}

	// 可见命令仍被正常建议（正向对照，防止过滤误伤）
	for input, want := range map[string]string{"hel": "help", "mod": "model", "usag": "usage"} {
		if got := Suggest(input, r); !containsSlice(got, want) {
			t.Errorf("Suggest(%q) 应建议 %s，实际 %v", input, want, got)
		}
	}
}

// TestV01HiddenCommandExactCounts P1-2（对抗式审查）：精确计数兜底。
// 预算测试（≤55）容差 7 条——漏标非样本命令时全部测试静默通过。
// 此处收紧为精确值：新增隐藏命令时需同步维护；新增可见命令不影响本断言。
//
// ★ 已翻转：88 → 87。`/monitors` 取消隐藏（唯一停止出口不得不可发现，
// 见 hiddenCommandSamples/coreCommandSamples 的注释）；同批新增的 `monitors stop` /
// `monitors tail` 自注册起即为可见，故隐藏数只减 1。
// 本值**只应因"某条命令被取消隐藏/重新隐藏"而变**；若你只是新增命令而它变红了，
// 说明你把新命令标成了 Hidden —— 先问它是不是某个能力的唯一出口。
func TestV01HiddenCommandExactCounts(t *testing.T) {
	r := NewRegistry()
	hidden, stub := 0, 0
	for _, cmd := range r.All() {
		if cmd.Hidden {
			hidden++
		} else if cmd.Stub {
			stub++
		}
	}
	// ★ 2026-10-05（C 批 C1）：74 → 73。`/effort` 整条命令删除（D2=(b)：非功能命令
	// 不带进 v0.1，等作为新功能回归）；它原是 Hidden，故隐藏数只减 1。
	// ★ 2026-10-11（O-2 出口收口）：73 → 72。`/skill` 由隐藏桩改为**可见** Security 命令。
	// ★ 2026-10-12（，用户口径「v1 无承诺的都不要有」）：72 → 86。10-03 登记批其余
	// 14 条（/fast /daemon×8 /task×3 /session /session pick）收回隐藏（不在 10-10 判据册的
	// v1 承诺清单）；/memory 同日 14:50 经用户拍板单独恢复可见。登记见 回执。
	if hidden != 86 {
		t.Errorf("v0.1 隐藏命令数 = %d，应为 86（漏标/错标会静默通过预算测试）", hidden)
	}
	// ★ 2026-10-10（C-5 守卫门修复）：5 → 8。mark/unmark/tag 补注册为 Stub —— 此前三者在
	// TUI 有「诚实降级」case 但 command 包从未注册 ⇒ case 永远走不到（守卫门
	// internal/tui/command_registry_guard_test.go 首跑红）。注册侧补桩属**有意新增**，
	// 不是漏标/错标；Hidden 数不变（三个都是 Stub 非 Hidden）。
	if stub != 8 {
		t.Errorf("桩命令数 = %d，应为 8（mark/unmark/tag 于 2026-10-10 补注册）", stub)
	}
}
