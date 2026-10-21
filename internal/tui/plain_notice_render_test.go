// plain_notice_render_test.go — 「程序说明走纯文本通道」的**渲染后**守卫。
//
// # 守的是什么
//
// 双语文案（`util.Bi`）的形态是「英文主行 \n 3 空格 + 中文辅助行」。而助手消息的
// 正文本体**经 Markdown（glamour）渲染**，Markdown 有两条规矩正好打在这个形态上：
//
//	① 段落内的软换行被折成**一个空格**；
//	② 行首 **≤3 个空格**被剥掉。
//
// ⇒ 屏幕上是 "English main 中文辅助" 挤在同一行，中文行也没了缩进 ——
// 正是用户 2026-09-18 明确否决的行内并列形态（真机报障，）。
//
// # 为什么必须新开一份门：旧门装错了层（结构性假绿）
//
// `confirm_bilingual_test.go` 的形态门门的是**渲染前的字符串**
// （`last.Blocks[0].Text`）—— 字符串里确实有换行与缩进 ⇒ 门**恒绿**，而屏幕是坏的。
// 不是门写错了，是门装在了"产地"而不是"消费地"。本文件把门挪到**渲染后的显示串**。
//
// # 判别力自证（两条，缺一即判据不可判别）
//
// 1. **对照组**：同一段文本以**普通块**（非 Plain）渲染 ⇒ 必须被 Markdown 压成
// 一行、且渲染后形态门必须**判红**（复现报障）。这同时证明"区分来自 Plain 标记，
// 不是来自文本内容"。
// 2. **旧门位置恒绿**：同一段文本（未渲染）在字符串级形态门上是**绿的** ——
// 这正是旧门抓不到 的原因。
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/util"
)

// w086BiSample 是报障文案的**短同形样本**（走生产 SSOT util.Bi，不手拼换行/缩进）。
//
// 用短样本是有意的：unit 判据要在宽终端下断言"被压成一行"，长样本会先被
// glamour 折行、看起来仍像两行 ⇒ 判据失去分辨力（这正是真机上"终端宽窄不同、
// 时好时坏"的成因）。
func w086BiSample() string {
	return util.Bi(
		"Command blocked by sandbox.",
		"命令已被沙箱拦截。")
}

// w086MergedFragment 是"被压成一行"后必然相邻的片段（跨软换行的接缝）。
const w086MergedFragment = "sandbox. 命令已被沙箱拦截"

// TestW086_PlainBlockKeepsBilingualForm 渲染后形态守卫（unit 面）。
func TestW086_PlainBlockKeepsBilingualForm(t *testing.T) {
	text := w086BiSample()
	if !strings.Contains(text, "\n"+util.CopyIndent) {
		t.Fatalf("夹具不健全：util.Bi 未产出「换行 + 缩进」形态：%q", text)
	}

	// 宽度足够大：让"合并成一行"的形态能完整落在一行内（否则折行会掩盖合并）。
	const width = 200

	plain := ansi.Strip(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "text", Text: text, Plain: true}}, "", 0, width))
	normal := ansi.Strip(formatAssistantMessageWithThinking(
		[]session.ContentBlock{{Type: "text", Text: text}}, "", 0, width))

	// ① Plain 块：换行与缩进逐字保留。
	if !strings.Contains(plain, "\n"+util.CopyIndent+"命令已被沙箱拦截。") {
		t.Errorf("Plain 块未保住双语形态（中文辅助行不再独立成缩进行）。输出：\n%s", plain)
	}
	if strings.Contains(plain, w086MergedFragment) {
		t.Errorf("Plain 块仍被 Markdown 处理（出现合并片段）：\n%s", plain)
	}

	// ② 对照组（判别性）：同文本、非 Plain ⇒ 必须被压成一行。
	if !strings.Contains(normal, w086MergedFragment) {
		t.Errorf("判别性失效：非 Plain 块未被压成一行 ⇒ 本判据区分不了两者"+
			"（若 glamour 行为已变，请重取合并片段）。输出：\n%s", normal)
	}
	if strings.Contains(normal, "\n"+util.CopyIndent+"命令") {
		t.Errorf("判别性失效：非 Plain 块也保住了缩进 ⇒ 无法把差异归因到 Plain 标记：\n%s", normal)
	}

	// ③ 渲染后形态门在两条路上的取值必须**相反**（否则门无分辨力）。
	if v := util.AssertChineseOnlyOnIndentedLines(normal); v == "" {
		t.Errorf("渲染后形态门在「被压成一行」的旧路上竟然判绿 ⇒ 门是假绿：\n%s", normal)
	}
	if v := util.AssertChineseOnlyOnIndentedLines(plain); v != "" {
		t.Errorf("渲染后形态门在 Plain 路上判红（违规行 %q）：\n%s", v, plain)
	}

	// ④ 旧门位置恒绿（这就是 逃过守卫的原因，钉住以防有人把门又挪回字符串层）。
	if v := util.AssertChineseOnlyOnIndentedLines(text); v != "" {
		t.Errorf("字符串级形态门对本样本应恒绿（否则说明样本不是旧门的盲区，本判据的前提不成立）：%q", v)
	}
}

// TestW086_BlockedTurnDisplayPassesFormGate 生产入口守卫：completeBlockedTurn。
//
// 走真方法（沙箱拒绝 / 权限拒绝两种），断言**渲染后的显示串**（不是 session 字符串）
// 既过形态门、又保住"中文辅助行独立成缩进行"。
func TestW086_BlockedTurnDisplayPassesFormGate(t *testing.T) {
	for _, kind := range []tools.FailureKind{tools.FailureKindSandboxDenied, tools.FailureKindPolicyDenied} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			m := newScreenSelectionFixture(t, 120, 30, 6)
			m.completeBlockedTurn(kind)

			entries := m.sessionService.MessageEntries()
			if len(entries) == 0 {
				t.Fatalf("夹具不健全：completeBlockedTurn 后显示缓冲应有条目")
			}
			display := ansi.Strip(entries[len(entries)-1].text)

			// 前提自检：这条显示串必须真的含中文辅助行（否则下面两条断言恒真=假绿）。
			if !util.HasHan(display) {
				t.Fatalf("夹具不健全：显示串不含中文，判据不可判别：\n%s", display)
			}
			if !strings.Contains(display, "\n"+util.CopyIndent) {
				t.Errorf("显示串里没有任何「换行 + CopyIndent」行 ⇒ 双语辅助行未独立成行"+
					"（疑似又被 Markdown 合并）：\n%s", display)
			}
			if v := util.AssertChineseOnlyOnIndentedLines(display); v != "" {
				t.Errorf("渲染后形态违规行 %q：\n%s", v, display)
			}
		})
	}
}
