package command

import (
	"strings"
	"testing"
)

// sandbox_status_discoverability_test.go —— `/sandbox status` 可发现性（2026-10-14，用户直报）。
//
// # 缺陷（与 /monitors 09-30 判例同族）
//
// `/sandbox status` 在实现里**一直能用**（handleSandbox 的 default 分支即状态查询），
// 但三个可见出口一个都没提它：
//
//	① `/` 补全弹窗：只按注册名前缀匹配 ⇒ 未注册全名 = 永远提示不出来；
//	② /help：Usage 写的是 `/sandbox on|off` —— `status` 压根漏写；
//	③ 命令回执：unknown 参数只回状态、不回用法。
//
// 本仓既有判例（commands_core.go /monitors 注释）：**唯一出口不可发现 = 该能力不存在**。
//
// 判别性：before（未注册 "sandbox status"、Usage 为 "/sandbox on|off"）下本文件全红；
// 实测见 内部工作回执 回执。

// TestSandboxUsageDeclaresStatusSubcommand 出口②：父命令 Usage 必须写全 on|off|status。
func TestSandboxUsageDeclaresStatusSubcommand(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("sandbox")
	if !ok {
		t.Fatal("/sandbox 未注册（判据前置）")
	}
	if !strings.Contains(cmd.Usage, "status") {
		t.Errorf("/sandbox 的 Usage=%q 未声明 status 子命令 —— /help 的 Usage 行是子命令形态"+
			"唯一的说明书位，漏写 = 用户少知一个功能（本轮用户直报）", cmd.Usage)
	}
	if !strings.Contains(cmd.Usage, "on") || !strings.Contains(cmd.Usage, "off") {
		t.Errorf("/sandbox 的 Usage=%q 应同时声明 on|off（既有能力不得因改动丢失）", cmd.Usage)
	}
}

// TestSandboxStatusRegisteredAsFullName 出口①：注册全名（弹窗可提示 + Parse 可切分）。
func TestSandboxStatusRegisteredAsFullName(t *testing.T) {
	r := NewRegistry()
	cmd, ok := r.Get("sandbox status")
	if !ok {
		t.Fatal("/sandbox status 未注册为全名 —— 弹窗按注册名前缀匹配，" +
			"不注册 = 敲 /sandbox s 永远提示不出来（/monitors 判例）")
	}
	if cmd.Hidden || cmd.Stub {
		t.Errorf("/sandbox status 不得 Hidden/Stub（它是 v1 /sandbox 家族的既有能力，登记即承诺）")
	}
	if cmd.Handler == nil {
		t.Error("/sandbox status 的 handler 不应为 nil")
	}
	// 弹窗元数据：主行取 DescriptionEN（completion_popup.go:255），缺失 = 弹窗主行空白。
	if en := englishCommandDescriptions["sandbox status"]; strings.TrimSpace(en) == "" {
		t.Error("englishCommandDescriptions 缺 sandbox status 条目（弹窗主行取它，双向守卫）")
	}
	// 声明面：一级前缀（prefixMatch 路径，command.go:544 对注册全名做前缀匹配 —— 与
	// 弹窗 SuggestCommands 同一匹配语义）。⚠️ 两段输入（"sandbox s"）走的是
	// commandCompletions 静态子命令树（command.go:470），sandbox 不在那棵树上，
	// 不是弹窗的生产匹配面，不得用作判据（首版判据踩过，已修正观测面）。
	if got := r.Completion("sandbox"); !containsSlice(got, "sandbox status") {
		t.Errorf("补全 \"sandbox\" 应给出 sandbox status，实际 %v", got)
	}
}

// TestSandboxStatusShownInDefaultHelp 出口②（结果面）：默认 /help 必须能看到
// status 子命令（英文主行 + 父命令 Usage 行写全）。
func TestSandboxStatusShownInDefaultHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	if !strings.Contains(help, "Show sandbox gate status") {
		t.Error("默认 /help 应列出 /sandbox status（英文主行）—— v1 家族能力不可发现 = 不存在")
	}
	if !strings.Contains(help, "/sandbox [on|off|status]") {
		t.Error("默认 /help 中 /sandbox 的 Usage 行应写全 [on|off|status]")
	}
}

// TestUsageAudit_DeclaresHandlerSubcommands 2026-10-14 全量 Usage 审计的**守卫化结论**。
//
// 审计方法：55 条可见命令逐条对照 TUI/runnerCore handler 实际接受的参数分支
// （app.go switch + 各 handleXxx），漏写的 Usage 本轮已补 —— 本表把结论钉住，
// 防将来 handler 加子命令、Usage 不同步（同类问题第 N 次复发）。
//
// ⚠️ 反向（Usage 承诺 > 实现）未在本表覆盖：/model [add|remove|edit|info] 已逐字核对
// 四个子命令全部有实现；其余命令未逐一反向核对，见本轮回执「未验证项」。
func TestUsageAudit_DeclaresHandlerSubcommandsDeclared(t *testing.T) {
	r := NewRegistry()
	cases := []struct{ name, wantContains string }{
		{"help", "--all"},         // FormatHelp 既有 flag（/M09-04）
		{"help", "--search"},      //
		{"help", "--filter"},      //
		{"theme", "mono"},         // handleTheme 接受 light|dark|mono
		{"tool", "[N]"},           // handleToolCommand 回看第 N 条
		{"skills", "[name]"},      // 带名 = 开关技能（handleSkillToggle）
		{"onboarding", "skip"},    // handleOnboarding("skip")
		{"diff", "--stat"},        // parseDiffArgs
		{"diff", "--file"},        //
		{"sandbox", "status"},     // 本轮主修复
		{"permissions", "status"}, // 既有（G12b）
		{"monitors", "stop"},      // 既有（09-30 判例）
		{"security-preset", "unrestricted"},
	}
	for _, tc := range cases {
		cmd, ok := r.Get(tc.name)
		if !ok {
			t.Errorf("/%s 未注册（判据前置失真）", tc.name)
			continue
		}
		if !strings.Contains(cmd.Usage, tc.wantContains) {
			t.Errorf("/%s 的 Usage=%q 未声明 %q —— handler 支持的形态必须在 Usage 可见"+
				"（/help 的 Usage 行是子命令唯一的说明书位）", tc.name, cmd.Usage, tc.wantContains)
		}
	}
}
