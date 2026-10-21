// .go — / N-039b：强制覆盖短语的**可发现性 + 文案形态**守卫。
//
// # 背景（两层，别混）
//
// - ****：方案 A 删掉了 TUI 里"命中拒绝措辞就自动弹提示"的那条自动断言
// （它把「如实说没有这个工具、无法调用」误判成 AI 拒绝，且弹出的提示机制上无效）。
// 强制覆盖**机制保留**，但改为用户主动输入 ⇒ 必须在 /help 里能被找到，
// 否则"删掉自动弹出"就等于"删功能"。
// - **N-039b**：该提示首版是**中文单语**（与 /help 里既有的 `💡` 区风格一致，但违反本仓
// 「英文为主、中文为辅」的**运行时**文案规范）⇒ 本轮改为 `util.Bi(en, zh)` 形态。
//
// # 判别力（两条各自独立）
//
// 1. **可发现性**： 修改前必红（/help 里没有这句提示）。
// 2. **形态**：N-039b 修改前必红 —— 旧文案是「中文单语 + 不缩进」，形态门
// `util.AssertChineseOnlyOnIndentedLines` 会直接抓出那一行。
//
// ★ 形态门**只施加在"这条提示"的片段上**，不是整个 /help ——
// /help 里命令列表（`cmd.Description`）与快捷键块按既有决策是**中文且不缩进**
// （快捷键块 = “「快捷键中文化」的有意决策；命令描述受
// `command_metadata_test.go` 的 `TestCommandDescriptions_AllChinese` 约束）。
// 对整个输出施加形态门会大面积误红，那属于**扩面**，本单不做。
package command

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// helpTipBlock 从 /help 全量输出中切出" 强制覆盖提示"那一块：
// 以含 `force execute` 的行为**英文主行**，其后**以 CopyIndent 开头的连续行**为中文辅助行。
//
// 用"锚行 + 缩进续行"而不是固定行号：文案调整行数时判据不会碎。
func helpTipBlock(help string) string {
	lines := strings.Split(help, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "force execute") {
			continue
		}
		block := []string{l}
		for j := i + 1; j < len(lines); j++ {
			if !strings.HasPrefix(lines[j], util.CopyIndent) {
				break
			}
			block = append(block, lines[j])
		}
		return strings.Join(block, "\n")
	}
	return ""
}

func TestN039_HelpAdvertisesManualForceExecuteOverride(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")

	block := helpTipBlock(help)
	if block == "" {
		t.Fatal("/help 必须提到 `force execute`；否则  删掉自动提示后，" +
			"该机制对用户不可发现 = 等于删功能")
	}
	// 中文触发串（用户实际会输入的中文短语）必须仍可发现 —— 它在中文辅助行上。
	if !strings.Contains(block, "我确认强制执行") {
		t.Fatalf("提示必须告诉中文用户可以直接输入「我确认强制执行」。当前片段：\n%s", block)
	}
	// 同一条提示必须声明它的边界（只覆盖 AI 自身措辞，不绕过权限与沙箱）——
	// 否则用户会以为它能突破权限层。
	if !strings.Contains(help, "不会绕过权限或沙箱") {
		t.Fatal("提示必须同时声明「不绕过权限或沙箱」，否则会误导用户以为它是提权后门")
	}
}

// TestN039b_HelpTipObeysBilingualShape 是 N-039b 的形态门。
//
// 契约（SSOT = `internal/util/bilingual.go`）：
//
//	English main line ← 主行，**不得含汉字**
//
// # Chinese auxiliary line ← 辅助行，以 CopyIndent 开头
//
// ★ 修前必红：旧文案 = `💡 AI 拒绝执行时，可直接输入「我确认强制执行」（或 force execute）覆盖；…`
// 是**中文主行且不缩进** ⇒ 形态门返回该行 ⇒ 本用例失败。
func TestN039b_HelpTipObeysBilingualShape(t *testing.T) {
	r := NewRegistry()
	block := helpTipBlock(FormatHelp(r, ""))

	if block == "" {
		t.Fatal("前置失败：没找到提示块（先让 TestN039_HelpAdvertisesManualForceExecuteOverride 过）")
	}

	// ① 形态门：含汉字的行必须缩进（= 禁止行内并列 / 禁止中文主行）。
	if v := util.AssertChineseOnlyOnIndentedLines(block); v != "" {
		t.Fatalf("提示未遵守「英文主行 + 缩进中文辅行」形态，违规行：%q\n完整片段：\n%s\n"+
			"（形态 SSOT 见 internal/util/bilingual.go；禁止行内并列 English / 中文）", v, block)
	}

	// ② 主行（第一行）必须是**可独立读懂的英文**：不得含汉字。
	mainLine := strings.SplitN(block, "\n", 2)[0]
	if util.HasHan(mainLine) {
		t.Fatalf("英文主行不得含汉字：%q", mainLine)
	}
	// ⚠️ 这里**只**断言主题词 `refusal`，不钉词形 ——
	// 首版钉了子串 `override`，而实现用的是 `overridden`（不含 `override`，只含 `overridd`）
	// ⇒ 判据把**正确实现**打红。这是判据自己的毛病（过窄），不是实现的问题。
	// 「主行可独立读懂」本质是散文性质、**不可机械判定** ⇒ 只用弱代理表达，
	// 不要为了"更严格"去钉具体词形（那只会造出脆弱判据）。
	if !strings.Contains(strings.ToLower(mainLine), "refusal") {
		t.Fatalf("主行应能独立说明这是关于「拒绝覆盖」的提示，actual %q", mainLine)
	}

	// ③ 可执行的触发串必须留在**主行**（只读主行的人也要能用）：
	// `force execute` 是 ASCII，不违反"主行不含汉字"。
	if !strings.Contains(mainLine, "force execute") {
		t.Fatalf("英文触发串 `force execute` 必须在主行上，actual %q", mainLine)
	}

	// ④ 中文辅助行必须**确实存在**（不是把中文删光了事 —— 那是"删文案"不是"改形态"）。
	rest := strings.TrimPrefix(block, mainLine)
	if !util.HasHan(rest) {
		t.Fatalf("中文辅助行缺失：本单要的是「英文主 + 中文辅」，不是删掉中文。片段：\n%s", block)
	}
}
