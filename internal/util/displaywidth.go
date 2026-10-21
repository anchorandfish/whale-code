package util

// displaywidth.go — 终端显示宽度（display width）的**唯一**实现。
//
// # 为什么需要它
//
// 终端里汉字/全角字符占 2 列、ASCII 占 1 列。任何「按字符数排版」的代码遇到
// 中文都会对不齐（rune 数 ≠ 列数）。/help 的宽度门（，2026-10-12）需要
// 逐行计算显示宽度来钉住「任何一行不得超过 80 列」的排版预算。
//
// # 为什么放 util
//
// `util` 是不 import 任何 internal 包的叶子包（见 bilingual.go 同款论证）；
// /help 在 `internal/command`，TUI 在 `internal/tui`，两者都要用 ⇒ 只能放这里。
//
// # 覆盖范围（够用即可，不追求完整 East Asian Width 表）
//
// 宽度 2：CJK 统一表意文字（U+4E00–9FFF）、CJK 标点（U+3000–303F）、
// 全角形式（U+FF00–FF60）、制表符号 ━ 等（U+2500–257F，/help 分类标题在用）。
// 其余按 1 列。中文辅助行只含前两类（见仓内实际文案），覆盖面与仓内文案对齐；
// 若未来引入其他宽区（如谚文），先扩本表再引用 —— 别在调用方自己数。
//
// 注：本仓 vendored 依赖里没有 go-runewidth / mattn 系库，且 util 引外部依赖
// 会破坏「叶包零 internal 依赖」之外的纯净性，故自实现（~20 行，逻辑可枚举）。

// DisplayWidth 返回 s 在等宽终端里占的列数（CJK/全角/制表符按 2，其余按 1）。
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		if wideRange(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func wideRange(r rune) bool {
	switch {
	case r >= 0x4e00 && r <= 0x9fff: // CJK Unified Ideographs
		return true
	case r >= 0x3000 && r <= 0x303f: // CJK Symbols and Punctuation（，。；：）
		return true
	case r >= 0xff00 && r <= 0xff60: // Halfwidth and Fullwidth Forms
		return true
	case r >= 0x2500 && r <= 0x257f: // Box Drawing（/help 的 ━━━）
		return true
	}
	return false
}
