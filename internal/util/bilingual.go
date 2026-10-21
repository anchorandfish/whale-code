package util

// bilingual.go — 全仓「英文为主 + 中文为辅」的**唯一**文案形态（2026-09-18 用户拍板）。
//
// # 形态
//
//	English main line
// 中文辅助行（缩进）
//
// 主行是**可独立读懂的英文**（只读主行就够用，**不得含汉字**）；中文辅助行是**按需的释义层**，
// 缩进另起一行，**不与英文同行并列**。
//
// # 缩进量：默认 3 空格；表格化输出必须按列对齐（2026-10-14 用户直报）
//
// 默认缩进 = `CopyIndent`（3 空格），适用于主行内容**从行首开始**的场景。
// 但表格化输出（`/help` 的命令表、快捷键表）主行内容有**固定列起点**
// （如 ` /checkpoint delete Delete a session checkpoint` 的英文列 = 第 25 列）。
// 此时必须用 `BiIndent(en, zh, <同列缩进>)`，否则中文辅行会退回行首 ——
// 用户看到的就是"中文在命令的下面，没有和英文对齐"。
// ★ 两种情况都**不许**行内并列：列对齐 ≠ 同行并排。
//
// # 为什么不许行内并列（`English / 中文`）
//
// 行内并列让主行长度翻倍（中文更宽），在 TUI 里被硬换行切成不可读的碎片 ——
// 这正是用户实测反馈的问题。缩进换行让主行保持可扫读。
//
// # 为什么放在 util 而不是 tui（2026-09-19 下沉，N-039b）
//
// 形态最初只在 `internal/tui/bilingual.go`（包私有）。但 `internal/command` 的 `/help`
// 同样要输出这个形态，而 **`internal/tui` 已经 import `internal/command`**
// （`app.go` / `bridge.go` / `lifecycle.go` / `plain.go` …）⇒ command 反向 import tui **必成环**。
//
// `util` 是**不 import 任何 internal 包的叶子包** ⇒ 是全仓唯一能让
// 「TUI 运行时输出」与「命令输出」共用**同一个**形态函数的位置。
//
// ⇒ 结果：`tui/bilingual.go` 保留 `bi`/`biSame` 小写别名做**薄转发**（既有调用点零改动），
// 两侧都**不许**自己拼 `"\n" + " "` —— 那正是 `` 的「双实现必漂移」形态。
//
// # 空串约定
//
// 任一侧为空则只返回另一侧（不产生孤立空行）⇒ 调用方无需为"这条没有中文"写分支。
//
// # 与既有出口的关系（不是新发明）
//
// slash 补全弹窗**早已**是这个形态：`internal/tui/completion_popup.go` 用
// `primary = DescriptionEN`（英文主）+ `secondary = Description`（中文辅）。
// 本文件把那套形态从「弹窗」推广到**全仓运行时输出**，使全仓只有一个形态。

import (
	"fmt"
	"strings"
)

// CopyIndent 是中文辅助行的统一缩进（与既有 listing 条目缩进一致）。
//
// ⚠️ 它只是**默认**缩进。当主行的内容不在行首、而是有固定列起点时（表格化输出，
// 如 `/help` 的命令表），必须用 `BiIndent` 传入**与主行内容同列**的缩进 ——
// 否则中文辅行会退回行首，视觉上"落在命令名下面"而不是"对齐英文描述列"。
const CopyIndent = "   "

// Bi 渲染「英文主行 + 缩进中文辅助行」。任一侧为多行时，中文每行都缩进。
//
// ⚠️ 形态契约（守卫见 `bilingual_test.go` 与 `internal/tui/bilingual_guard_test.go`）：
// **任何含汉字的行，都必须以 CopyIndent 开头** —— 这就是"禁止行内并列"的机械表达。
// ⇒ 传给 `en` 的字符串**不得含汉字**（含汉字就必须放到 `zh`）。
//
// 等价于 `BiIndent(en, zh, CopyIndent)`；需要按列对齐时用 `BiIndent`。
func Bi(en, zh string) string {
	return BiIndent(en, zh, CopyIndent)
}

// BiIndent 是 `Bi` 的**可指定缩进**变体：中文辅行按 `indent` 缩进，而非固定三空格。
//
// 存在理由（2026-10-14，用户直报「/help 的中文注释没有和英文对齐，而是直接在命令的下面」）：
// 表格化输出里主行的内容**不在行首**，而有固定列起点 —— 例如
//
//	/checkpoint delete Delete a session checkpoint
//
// 删除指定检查点 ← 与英文同一列起点（对齐）
//
// 英文描述列起点 = 第 25 列。此时中文辅行若仍用 `CopyIndent`（3 空格）就会落到**行首**，
// 即用户看到的"在命令的下面"（错位）。传入与主行内容同一列起点的缩进 ⇒ 中英左边缘对齐。
//
// ⚠️ **形态铁律不变**：中文仍**不与英文同行并列**，只是缩进量随列宽走。
// 因此 `AssertChineseOnlyOnIndentedLines` 依旧成立 —— 更深的缩进同样是"缩进辅行"
// （判据用的是 `HasPrefix(line, CopyIndent)`，25 空格行照样满足"以 3 空格开头"）。
//
// ⚠️ 空串约定与 `Bi` 一致：任一侧为空则只返回另一侧（不产生孤立空行）。
//
// ⚠️ `indent` 必须是**空白**（空格）：非空白会让中文行不再满足"以 CopyIndent 开头"的形态门。
func BiIndent(en, zh, indent string) string {
	en = strings.TrimRight(en, "\n")
	zh = strings.TrimRight(zh, "\n")
	switch {
	case zh == "":
		return en
	case en == "":
		return IndentCopyLinesWith(zh, indent)
	}
	return en + "\n" + IndentCopyLinesWith(zh, indent)
}

// BiSame 渲染双语提示，两侧共用同一组格式化参数。
//
// 存在的理由：绝大多数提示的英文行与中文行只差语言、参数相同（如 `Current: %s` /
// `当前：%s`）。共用参数可避免两处各写一遍参数列表，进而产生"英文报 A、中文报 B"
// 的漂移 —— 那正是本仓反复出现的"同一事实两处各自维护"形态。
func BiSame(formatEN, formatZH string, args ...any) string {
	return Bi(fmt.Sprintf(formatEN, args...), fmt.Sprintf(formatZH, args...))
}

// IndentCopyLines 给中文辅助行的每一非空行加**默认**缩进（CopyIndent）。
// 需要按列对齐时用 `IndentCopyLinesWith`。
func IndentCopyLines(s string) string {
	return IndentCopyLinesWith(s, CopyIndent)
}

// IndentCopyLinesWith 给中文辅助行的每一非空行加 `indent` 缩进（见 `BiIndent`）。
func IndentCopyLinesWith(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

// HasHan reports whether s contains any CJK Unified Ideograph.
//
// 导出它是因为形态门（"含汉字的行必须缩进"）需要在**多个包**里表达：
// 形态 SSOT 在本包，而各包的输出各自被钉 ⇒ 判定字符集也必须同源，
// 否则会出现"测试用正则数汉字、生产用区间判"这类两套标准。
func HasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// AssertChineseOnlyOnIndentedLines 检查 out 的每一行：**含汉字的行必须以 CopyIndent 开头**。
// 返回第一条违规行（无违规则返回空串）。这是形态铁律的**机械门**，供各包测试复用。
//
// 反向保：只断言"含汉字的行必须缩进"，**不**要求"所有中文都必须保留" ——
// 纯英文输出同样是合规形态，不得被误判。
func AssertChineseOnlyOnIndentedLines(out string) (violation string) {
	for _, line := range strings.Split(out, "\n") {
		if !HasHan(line) {
			continue
		}
		if !strings.HasPrefix(line, CopyIndent) {
			return line
		}
	}
	return ""
}
