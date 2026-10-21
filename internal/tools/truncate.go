package tools

import (
	"fmt"
	"regexp"
	"strconv"
)

// 收口：全仓「按上限砍尾 + 追加截断标记」语义的**唯一权威**。
//
// 根因回顾（T-9 真机，2026-10-07）：同一个 50000 上限曾在 bash.go 与 tools.go 两处
// 独立声明、各砍一刀 —— 第一刀追加的标记恰好 23 字符，第二刀把它切掉后按自己的
// 口径追加 `(truncated 23 characters)` ⇒ 屏幕与模型看到的「23」是上游标记串自身的
// 长度，真实丢弃量 ≈29,974（差三个数量级）。同族 ad-hoc 站点（compressor/agent/
// task/git…）各自 `output[:N] + "…(output truncated)"`，同一语义无唯一实现。
//
// 收口后：砍尾只此一处；标记形态唯一定义在本文件（写入与解析同源）；
// 下游遇到上游已砍过的输出不再二次追加，而是把上游计数并入单一标记 ——
// 「两层标记互相覆盖」在结构上不可能再现。旧文案 `(output truncated)`（无计数）
// 一并退役：它连丢弃量都不报，正是本次误读的温床。

// truncMarkerFmt 截断标记的唯一形态：换行 + 真实丢弃量（rune 数）。
const truncMarkerFmt = "\n... (truncated %d characters)"

// truncMarkerRe 只匹配「标记紧贴字符串结尾」的形态。
// 追加在标记之后的附注（如 timeout clamp 说明）不构成结尾 ⇒ 不误并。
var truncMarkerRe = regexp.MustCompile(`\n\.\.\. \(truncated (\d+) characters\)$`)

// TruncationDropped 识别本权威产出的截断标记，返回其声明的丢弃量。
// 返回 ok=false 表示尾部没有本权威的标记（含旧 ad-hoc 文案 —— 无计数即非权威）。
// 仅供 TruncateTail 合并计数；调用方不得用它做业务判断（防字符串嗅探回潮）。
func TruncationDropped(output string) (int, bool) {
	m := truncMarkerRe.FindStringSubmatch(output)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// TruncateTail 把 output 按 rune 砍到 max 内，并追加带「真实丢弃量」的截断标记。
//
// 语义（对本仓所有截断站点统一）：
// - max <= 0 ⇒ 用 MaxToolResultLength 兜底；未超限 ⇒ 原样返回。
// - 按 rune 切（旧实现多处按字节切，会切断 UTF-8 多字节字符）。
// - **合并语义**：output 尾部已带本权威标记 ⇒ 视为上游已砍过一刀，
// 本次只补砍真实内容、计数 = 上游丢弃量 + 本次补砍量，输出仍是单一标记。
// 切点永远不会落在标记串内部（宁可少砍 30 字符，不产半截标记）。
//
// 上限值由调用方传入（bash/task/agent 等各自的 knob），本函数不持有任何阈值 ——
// 阈值仍各归其声明处，这里收口的是「砍 + 报数」这一个动作。
func TruncateTail(output string, max int) string {
	if max <= 0 {
		max = MaxToolResultLength
	}
	runes := []rune(output)
	if len(runes) <= max {
		return output
	}
	if up, ok := TruncationDropped(output); ok {
		markerLen := len([]rune(fmt.Sprintf(truncMarkerFmt, up)))
		contentLen := len(runes) - markerLen
		cut := max
		if cut > contentLen {
			cut = contentLen
		}
		total := up + (contentLen - cut)
		return string(runes[:cut]) + fmt.Sprintf(truncMarkerFmt, total)
	}
	return string(runes[:max]) + fmt.Sprintf(truncMarkerFmt, len(runes)-max)
}
