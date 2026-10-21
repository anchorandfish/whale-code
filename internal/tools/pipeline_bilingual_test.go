// pipeline_bilingual_test.go — 双语文案守卫。
//
// 守两条不变式（全仓形态 SSOT = internal/util/bilingual.go：英文主行 + 缩进中文辅行，
// 含汉字的行必须以 CopyIndent 开头）：
//
//	A. 数据层：cmdrules 的每条规则 Note（英文主行，不得含汉字）+ NoteZH（中文辅行，必须含汉字）；
//	B. 展示层：两个确认阶段产出的 Message（确认弹窗的 Risk 行）必须过形态门。
//
// ★ 判别力自证（不红 = 假绿）：每个判据都配一个**反例**——把改造前的真实文案
// （行内并列 / 纯中文 Note）喂给门，必须被抓到。反例用的是改前原样文本
// （见 内部工作回执 回执），不是臆造。
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/security/cmdrules"
	"github.com/anchorandfish/whale-code/internal/util"
)

// patternNoteViolation 报告一条规则的双语数据违规（空 = 合规）。
// 判据与实现分离：这里自己写扫描逻辑，不调用生产的形态函数（共享 bug ⇒ 假绿），
// 但汉字判定走 util.HasHan（字符集同源，技能 §4）。
func patternNoteViolation(id, note, noteZH string) string {
	if strings.TrimSpace(note) == "" {
		return "Note 为空（英文主行缺失）"
	}
	if util.HasHan(note) {
		return "Note 含汉字 —— 英文主行必须是可独立读懂的英文"
	}
	if strings.TrimSpace(noteZH) == "" {
		return "NoteZH 为空（中文辅行缺失）"
	}
	if !util.HasHan(noteZH) {
		return "NoteZH 不含汉字（中文辅行形同虚设）"
	}
	return ""
}

// TestW083B1_CmdrulesNotesAreBilingualPair 危险/破坏性规则表的每一条理由
// 都必须是 (英文主行, 中文辅行) 两侧。覆盖三处来源：导出的危险档全表、
// MatchDestructive 现场合成的 Pattern（PowerShell cmdlet / 命令名 / 变体）。
func TestW083B1_CmdrulesNotesAreBilingualPair(t *testing.T) {
	t.Parallel()
	for _, p := range cmdrules.DangerousPatterns() {
		if v := patternNoteViolation(p.ID, p.Note, p.NoteZH); v != "" {
			t.Errorf("dangerousPatterns[%s]: %s", p.ID, v)
		}
	}
	// 现场合成面（MatchDestructive 的三条 return 各构造一个 Pattern）
	synthesized := []struct{ cmd, wantID string }{
		{"rm somefile", "rm"},
		{"Remove-Item -Path x", "ps-remove-item"},
		{"mkfs.ext4 /dev/sdb", "mkfs"},
	}
	for _, tc := range synthesized {
		p, ok := cmdrules.MatchDestructive(tc.cmd)
		if !ok {
			t.Errorf("前置不成立：%q 应命中破坏性档（否则本判据不可判别）", tc.cmd)
			continue
		}
		if p.ID != tc.wantID {
			t.Errorf("前置不成立：%q 命中 %q，want %q（判据钉错了规则）", tc.cmd, p.ID, tc.wantID)
			continue
		}
		if v := patternNoteViolation(p.ID, p.Note, p.NoteZH); v != "" {
			t.Errorf("MatchDestructive(%q) 合成 Pattern: %s", tc.cmd, v)
		}
	}
}

// TestW083B1_PatternNoteGateCatchesCounterExample 判别力自证：
// 改造前的真实数据（纯中文 Note、无 NoteZH）必须被门抓到。
func TestW083B1_PatternNoteGateCatchesCounterExample(t *testing.T) {
	t.Parallel()
	// 反例 1：改前的 rm-rf-root —— Note 是纯中文（"递归删除根路径（同时覆盖 rm -rf /<任意子路径>）"）
	if v := patternNoteViolation("rm-rf-root", "递归删除根路径（同时覆盖 rm -rf /<任意子路径>）", ""); v == "" {
		t.Error("纯中文 Note 必须被判违规（否则数据门是假绿）")
	}
	// 反例 2：英文主行混入汉字（行内并列的数据形态）
	if v := patternNoteViolation("x", "Force push / 强制推送", "强制推送"); v == "" {
		t.Error("Note 内含汉字必须被判违规")
	}
	// 反例 3：NoteZH 是英文（辅行没有中文）
	if v := patternNoteViolation("x", "Force push", "Force push"); v == "" {
		t.Error("NoteZH 不含汉字必须被判违规")
	}
}

// TestW083B1_CheckBashDestructiveReturnsBilingualPair checkBashDestructive 出
// (英文主行, 中文辅行) 两侧：英文侧可独立读懂且不含汉字；中文侧含汉字。
// git 子命令理由是语言中性的命令文本 ⇒ 两侧相同是允许的（判据只查英文侧无汉字）。
func TestW083B1_CheckBashDestructiveReturnsBilingualPair(t *testing.T) {
	t.Parallel()
	en, zh := checkBashDestructive("rm -rf /")
	if en == "" || zh == "" {
		t.Fatalf("rm -rf / 应命中：en=%q zh=%q", en, zh)
	}
	if util.HasHan(en) {
		t.Errorf("英文主行不得含汉字：%q", en)
	}
	// 破坏性档按**命令位**命中（rm），故主行是 "bash rm (…)" 而非带参数的原文
	if !strings.Contains(en, "bash rm (") {
		t.Errorf("英文主行应含命令位与英文理由：%q", en)
	}
	if !util.HasHan(zh) {
		t.Errorf("中文辅行应含汉字：%q", zh)
	}
	// 语言中性的 git 理由：两侧相同、且英文侧无汉字
	gen, gzh := checkBashDestructive("git reset --hard HEAD")
	if gen == "" || gen != gzh {
		t.Errorf("git 理由应两侧相同且非空：en=%q zh=%q", gen, gzh)
	}
	if util.HasHan(gen) {
		t.Errorf("git 理由不应含汉字：%q", gen)
	}
}

// TestW083B1_ConfirmMessagesPassFormGate 两个确认阶段的 Message（确认弹窗 Risk 行）
// 必须过形态门；且英文主行含可定位的框架文案、中文辅行缩进。
// 走**生产入口**（stage.Execute），不自己拼消息。
func TestW083B1_ConfirmMessagesPassFormGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// ① 危险档：rm -rf / ⇒ DangerousGuardStage
	dg := &DangerousGuardStage{}
	sr := dg.Execute(ctx, Call{Name: "bash", Input: `{"command":"rm -rf /"}`}, &PipelineState{})
	if !sr.NeedConfirm {
		t.Fatal("前置不成立：rm -rf / 应触发危险档确认（否则本判据不可判别）")
	}
	if v := util.AssertChineseOnlyOnIndentedLines(sr.Message); v != "" {
		t.Errorf("DangerousGuardStage.Message 形态违规行: %q\n全量:\n%s", v, sr.Message)
	}
	firstLine := strings.SplitN(sr.Message, "\n", 2)[0]
	if !strings.Contains(firstLine, "dangerous command requires confirmation") ||
		!strings.Contains(firstLine, "rm-rf-root") {
		t.Errorf("英文主行应含框架文案与规则 ID：%q", firstLine)
	}
	if !strings.Contains(sr.Message, util.CopyIndent+"危险命令需要确认") {
		t.Errorf("应有缩进的中文辅行：\n%s", sr.Message)
	}

	// ② 破坏性档：rm file.txt ⇒ DestructiveCheckStage（policy=nil 时不做单向收窄判定）
	ds := &DestructiveCheckStage{}
	sr2 := ds.Execute(ctx, Call{Name: "bash", Input: `{"command":"rm file.txt"}`}, &PipelineState{})
	if !sr2.NeedConfirm {
		t.Fatal("前置不成立：rm file.txt 应触发破坏性档确认（否则本判据不可判别）")
	}
	if v := util.AssertChineseOnlyOnIndentedLines(sr2.Message); v != "" {
		t.Errorf("DestructiveCheckStage.Message 形态违规行: %q\n全量:\n%s", v, sr2.Message)
	}
	firstLine2 := strings.SplitN(sr2.Message, "\n", 2)[0]
	if !strings.Contains(firstLine2, "destructive operation detected") {
		t.Errorf("英文主行应含框架文案：%q", firstLine2)
	}
	if !strings.Contains(sr2.Message, util.CopyIndent+"检测到破坏性操作") {
		t.Errorf("应有缩进的中文辅行：\n%s", sr2.Message)
	}
}

// TestW083B1_FormGateCatchesOldInlineMessages 判别力自证：改造前的真实 Message
// （行内并列 / 纯中文理由）必须被形态门抓到。
func TestW083B1_FormGateCatchesOldInlineMessages(t *testing.T) {
	t.Parallel()
	olds := []string{
		// 改前 DestructiveCheckStage 的真实 Message（cmdrules 中文 Note 直接拼入）
		"destructive operation detected: bash rm (破坏性档命令名) (user confirmation required)",
		// 改前 DangerousGuardStage 的真实 Message
		"dangerous command requires confirmation: rule=rm-rf-root (递归删除根路径（同时覆盖 rm -rf /<任意子路径>）)",
		// 改前确认框框体的行内并列
		"Decision flow / 判定流程: permission approval first, then sandbox and network checks / 先审批权限，再检查沙箱和网络",
		"[Y] Allow once / 允许本次",
	}
	for _, old := range olds {
		if v := util.AssertChineseOnlyOnIndentedLines(old); v == "" {
			t.Errorf("改前行内并列文案必须被判违规：%q", old)
		}
	}
}
