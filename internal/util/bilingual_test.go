package util

// bilingual_test.go — 钉「英文为主 + 中文为辅」文案形态的 **SSOT 侧**守卫（`N-039b`）。
//
// # 形态契约
//
//	English main line ← 主行：可独立读懂的英文，**不得含汉字**
// Chinese auxiliary line ← 辅助行：以 CopyIndent 开头，**中文只允许出现在这里**
//
// 等价说法：**任何含汉字的行，都必须以 CopyIndent 开头** —— 这就是"禁止行内并列"。
//
// # 为什么这组测试在 util 而不是 tui（两份的关系）
//
// 形态 SSOT 在本包（`bilingual.go`）。TUI 侧另有 `internal/tui/bilingual_guard_test.go`
// 钉**具体命令的输出**（`/security-permission` 等菜单、渲染层）。两份**不重复**：
// 本文件钉**函数形态本身**，那边钉**使用者的输出**。
//
// ★ 分工原则（防止"实现与判据共享 bug"）：
// - **字符集定义同源** ⇒ 都走 `util.HasHan`（TUI 侧经 `bilingual.go` 转发）；
// - **判据逻辑独立** ⇒ TUI 测试自己写扫描循环，不调用生产侧的 `AssertChineseOnlyOnIndentedLines`。
//
// 本文件对 `AssertChineseOnlyOnIndentedLines` 的验证**用反例输入**表达判别力
// （把 `English / 中文` 行内并列喂进去 ⇒ 必须返回违规行），因此不依赖任何注入工具。

import (
	"strings"
	"testing"
)

func TestBi_ShapeAndEmptyConvention(t *testing.T) {
	if got, want := Bi("Main line", "辅助行"), "Main line\n"+CopyIndent+"辅助行"; got != want {
		t.Fatalf("Bi 形态错误：\n got %q\nwant %q", got, want)
	}
	if got := Bi("only en", ""); got != "only en" {
		t.Fatalf("中文为空时应只返回英文，actual %q", got)
	}
	if got := Bi("", "只有中文"); !strings.HasPrefix(got, CopyIndent) {
		t.Fatalf("英文为空时中文仍必须缩进，actual %q", got)
	}
	if got := Bi("English", "第一行\n第二行"); !strings.Contains(got, CopyIndent+"第二行") {
		t.Fatalf("多行中文的每一行都要缩进，actual %q", got)
	}
	// 主行不许含汉字：本函数不做校验（校验归守卫），但契约要写在这里 ——
	// 见 TestBi_MainLineMustNotContainHan 的说明。
	if HasHan("Main line") {
		t.Fatal("HasHan 对纯 ASCII 误报")
	}
	if !HasHan("辅助行") {
		t.Fatal("HasHan 漏报汉字")
	}
}

func TestBiSame_SharesOneArgList(t *testing.T) {
	got := BiSame("Value: %s", "取值：%s", "x")
	want := "Value: x\n" + CopyIndent + "取值：x"
	if got != want {
		t.Fatalf("BiSame 形态错误：\n got %q\nwant %q", got, want)
	}
}

// TestBiIndent_ColumnAlignedShape —— SSOT 侧钉 `BiIndent`（按列缩进）的形态与空串约定。
//
// 引入背景（2026-10-14 用户直报「/help 的中文注释没有和英文对齐，而是直接在命令的下面」）：
// 表格化输出里主行内容有**固定列起点**，中文辅行必须缩到**同一列**才对得齐；
// 固定三空格（`CopyIndent`）会让中文退回行首。
//
// ★ 本用例同时钉住一条**关键不变量**：更深的缩进**仍然满足**既有形态门
// （门是 `HasPrefix(line, CopyIndent)`）⇒ 列对齐**不**会削弱"中文只许出现在缩进行"这条铁律。
// 若这条不变量破了，`/help` 那条使用侧形态门就形同虚设。
func TestBiIndent_ColumnAlignedShape(t *testing.T) {
	const col = 26
	indent := strings.Repeat(" ", col)

	got := BiIndent("  /checkpoint delete      Delete a session checkpoint", "删除指定检查点", indent)
	want := "  /checkpoint delete      Delete a session checkpoint\n" + indent + "删除指定检查点"
	if got != want {
		t.Fatalf("BiIndent 未按指定列缩进：\n got %q\nwant %q", got, want)
	}

	// 多行中文：每一非空行都按同一列缩进
	multi := BiIndent("en", "第一行\n第二行", indent)
	if want := "en\n" + indent + "第一行\n" + indent + "第二行"; multi != want {
		t.Fatalf("BiIndent 多行中文缩进错误：\n got %q\nwant %q", multi, want)
	}

	// 空串约定与 Bi 一致：任一侧为空则只返回另一侧（不产生孤立空行）
	if got := BiIndent("only en", "", indent); got != "only en" {
		t.Fatalf("中文为空时应只返回英文，actual %q", got)
	}
	if got := BiIndent("", "只有中文", indent); got != indent+"只有中文" {
		t.Fatalf("英文为空时中文仍必须按列缩进，actual %q", got)
	}

	// 薄转发等价性：Bi 必须 == BiIndent(..., CopyIndent)。（改 Bi 时别把它改坏）
	if Bi("en", "中文") != BiIndent("en", "中文", CopyIndent) {
		t.Fatal("Bi 与 BiIndent(..., CopyIndent) 必须逐字节等价（Bi 是本函数的薄转发）")
	}
	if IndentCopyLines("中文") != IndentCopyLinesWith("中文", CopyIndent) {
		t.Fatal("IndentCopyLines 与 IndentCopyLinesWith(..., CopyIndent) 必须等价")
	}

	// ★ 不变量：列对齐（更深的缩进）仍被形态门接受 ⇒ 使用侧的形态门不因本次改动失效
	if v := AssertChineseOnlyOnIndentedLines(got); v != "" {
		t.Fatalf("按列缩进的中文辅行被形态门误判为违规（不变量破了）：%q", v)
	}
	// 判别性反例：缩进为空 ⇒ 中文落到行首 ⇒ 形态门**必须**抓（否则上面那条"接受"是靠门失灵换来的）
	if v := AssertChineseOnlyOnIndentedLines(BiIndent("en", "中文", "")); v == "" {
		t.Fatal("零缩进的中文辅行必须被形态门抓住 —— 否则上一条的「接受」是假绿")
	}
}

// TestAssertChineseOnlyOnIndentedLines_DetectsInlineJuxtaposition 是**判别力自证**：
// 把一个"行内并列"（本形态铁律明确禁止的写法）喂进去，判据**必须**抓到。
//
// ★ 若这条变绿（即返回空违规行），说明 `AssertChineseOnlyOnIndentedLines` 是**假的** ——
// 那时 `N-039b` 对 `/help` 施加的形态门也不承重，必须重做判据而不是改测试。
func TestAssertChineseOnlyOnIndentedLines_DetectsInlineJuxtaposition(t *testing.T) {
	// ← 用户 2026-09-18 明确否决的写法（中文与英文同行）
	bad := "To override an AI-only refusal, type: force execute / 仅覆盖 AI 自身拒绝"
	if v := AssertChineseOnlyOnIndentedLines(bad); v == "" {
		t.Fatal("行内并列（English / 中文）必须被抓；返回空 = 形态门不承重")
	}
	// 第二形态：中文在主行，只是恰好没缩进
	bad2 := "This line has 中文 in the middle"
	if v := AssertChineseOnlyOnIndentedLines(bad2); v == "" {
		t.Fatal("主行混入汉字必须被抓")
	}
}

// TestAssertChineseOnlyOnIndentedLines_AcceptsCompliantForms 反向保：
// 合规形态**不得**被误判（否则守卫会把正确输出打红，人就会去关掉它）。
func TestAssertChineseOnlyOnIndentedLines_AcceptsCompliantForms(t *testing.T) {
	good := []string{
		Bi("To override an AI-only refusal, type: force execute", "覆盖 AI 自身拒绝：输入 force execute"),
		"pure ASCII line, no han here\nsecond ascii line", // 两行都是纯 ASCII ⇒ 不该被扫出违规
		"",                      // 空输出
		CopyIndent + "只有中文（缩进）", // 纯中文但缩进
		Bi("en", "第一行\n第二行"),    // 多行中文全缩进
	}
	for i, g := range good {
		if v := AssertChineseOnlyOnIndentedLines(g); v != "" {
			t.Errorf("合规形态[%d]被误判为违规：%q（输入 %q）", i, v, g)
		}
	}
}

// TestBi_ComplianceOfTheActualHelpCopy 用**真实要落盘的文案**跑一次形态门，
// 防止"测试用样例合规、生产文案不合规"这种脱节。
//
// ★ 这条是 `/help`（`internal/command/command.go`）那条提示的**文案副本** ——
// 若那边改了文案，这里要同步（command 侧另有一条从**真实输出**取样的判据，
// 见 `internal/command/.go`，两层都有才不漏）。
func TestBi_ComplianceOfTheActualHelpCopy(t *testing.T) {
	const (
		en = "💡 AI-only refusal can be overridden manually: type force execute"
		zh = "覆盖 AI 自身拒绝：输入「我确认强制执行」（或 force execute）\n" +
			"仅覆盖 AI 自身拒绝，不会绕过权限或沙箱"
	)
	got := Bi(en, zh)
	if v := AssertChineseOnlyOnIndentedLines(got); v != "" {
		t.Fatalf("生产文案不符合形态铁律：违规行 %q\n完整输出：\n%s", v, got)
	}
	if HasHan(en) {
		t.Fatalf("英文主行不得含汉字：%q", en)
	}
	if !strings.Contains(got, "force execute") {
		t.Fatal("主行必须保留可执行的英文触发串 force execute")
	}
}
