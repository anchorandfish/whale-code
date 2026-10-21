package tui

import (
	"strings"
	"unicode/utf8"
)

// cjk_kinsoku.go — CJK 行首禁则（kinsoku shori）声明面+变换。
//
// 第 4 件：中文排版禁则 —— 闭合标点/句读**不得居行首**。
// 折行算法（TextMeasurer.WrapText → ansi.Hardwrap）只按显示宽度切分，不看字符语义，
// 于是全角 `）` 会被折到下一行行首（真机 T-14a 观察）。
//
// 本文件是「哪些字符不得居行首」的**唯一声明**；判定/变换见同文件下方。
// 对照：同类 CLI 等同类 CLI 未处理此面（西文无禁则），故本仓自持一份清单。

// cjkLineStartForbidden 是不得出现在行首的字符集合（CJK 闭合标点 + 句读 + 收尾符号）。
//
// 取值依据：中文/日文排版的「行头禁则」常用集 —— 闭合类〕〉》」』】】｝｠｣〉》…
// 与句读类（、。，．：；！？）及省略/破折收尾。
const cjkLineStartForbidden = "、。，．：；！？）〕〉》」』】〗｝｠｣»·～ー" +
	"ヽヾ々" +
	"%‰℃" +
	",.;:!?)]}"

// isCJKLineStartForbidden 报告 r 是否为「不得居行首」的字符。
func isCJKLineStartForbidden(r rune) bool {
	return r != 0 && containsRune(cjkLineStartForbidden, r)
}

// applyCJKLineStartKinsoku 消除「禁则字符独占行首」：逐行扫描，若某行以禁则字符开头，
// 就把它**上提**到上一行行尾（悬挂 / ぶら下げ），于是没有一行以标点起首。
//
// 遵守两条硬约束：
// 1. **逐字守恒** —— 只移动换行位置，绝不删改任何字符（join 掉一个 \n 而已）。
// 2. **不碰着色行** —— 上一行若含 ANSI 转义（ESC），则**跳过**该次调整：跨转义序列搬字
// 极易把 SGR 前缀/复位搬到别的行、污染着色。着色行保持原样（仅少一处化妆品改善，
// 方向 fail-open：宁可不动，不可弄坏）。
//
// 悬挂会让上一行**多一个宽字符**（超出折行宽度约 2 列）；本仓折行宽度取
// `viewport.Width - 输入框横向 frame` ⇒ 相对终端宽度本就有余量（见 contentWrapWidth）。
func applyCJKLineStartKinsoku(wrapped string) string {
	lines := strings.Split(wrapped, "\n")
	out := make([]string, 0, len(lines))
	moved := false
	for _, orig := range lines {
		cur := orig
		// 内层循环：连续多个禁则字符（如 `），`）时逐个上提，直到行首不再是禁则字符。
		if len(out) > 0 {
			for {
				r, size := utf8.DecodeRuneInString(cur) // 空串 ⇒ (RuneError,0)，不命中禁则集 ⇒ 自然 break
				if !isCJKLineStartForbidden(r) {
					break
				}
				prev := out[len(out)-1]
				if prev == "" || strings.ContainsRune(prev, 0x1b) {
					break
				}
				out[len(out)-1] = prev + cur[:size]
				cur = cur[size:]
				moved = true
			}
		}
		// 本行字符被整行上提 ⇒ **不留空行**（空行只是换一种展示噪声）；
		// 用 `orig != ""` 区分「被我们搬空的」与「本来就是空行」。
		if cur == "" && orig != "" {
			continue
		}
		out = append(out, cur)
	}
	if !moved {
		return wrapped
	}
	return strings.Join(out, "\n")
}

// containsRune 是 strings.ContainsRune 的本地薄封装（保持调用点简短）。
func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
