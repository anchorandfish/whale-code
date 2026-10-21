package session

import "strings"

// reasoning_tags.go — (b)：把「推理边界标记」挡在**会话落盘**之外。
//
// 现象（总账 明细）：模型正文本该是一段正常回答，却混进一行 `</think>`。
// 实测该行会**上屏 → 写进会话存档 → 下一轮回灌给模型**（把网关的脏数据喂回去）。
// 全仓 `Grep "</think>"` = 0 命中 ⇒ 标记**不是 whale 产出**，是上游/网关泄漏。
//
// 根因：推理与正文在**装配成块之后**才分家；在这之前，原始文本里的标记只是普通字符
// ⇒ 只要上游把它写进 text 块，whale 就一路当真。
//
// ★ 为什么清洗点在这里（`internal/session`）而不是渲染层：
// 1. 落盘点**不止一个** —— `tui/stream.go`（流式回合收尾）· `tui/plain.go` ×2 · `tui/bridge.go`
// 全都经 `AssistantWithTools` 组装。只在渲染层补丁 ⇒ plain / bridge 两条路照样脏；
// 2. `tui/stream.go` 里**渲染与落盘共用同一个切片**（`persisted`）⇒ 在 `WithThinkingBlock`
// 里清洗，屏幕与磁盘天然同源（``：同一事实只声明一次）。
//
// ★ 为什么规则要"窄"（宁可少剥，不要误伤正文）：
// 只剥**孤立成行/成块**的边界标记。行内出现的（`a</think>b`）与**代码块内**出现的
// **一律不剥** —— 模型可能正在讨论这个标签本身，或示例代码里就写着它。
// 判据 `reasoning_tags_test.go` 的反例组钉住这两条（否则"任何出现都删"也能全绿）。

// reasoningBoundaryTags 是「推理边界标记」的**唯一声明**（本仓唯一）。
//
// 为什么用整行精确匹配而不是子串：子串会吃掉正文（见上"窄规则"）。
// 覆盖 `think` / `thinking` / `reasoning` 三族的开闭标记 —— 都是已知网关会泄漏的形态。
var reasoningBoundaryTags = map[string]bool{
	"</think>":     true,
	"<think>":      true,
	"</thinking>":  true,
	"<thinking>":   true,
	"</reasoning>": true,
	"<reasoning>":  true,
}

// sanitizeAssistantBlocks 对**已组装**的 assistant 内容块做一次入口清洗。
//
// 语义（逐块）：
// - 非 `text` 块（thinking / tool_use / image …）**原样保留**；
// - `text` 块：按行剥掉孤立边界标记行（跳过围栏代码块内部）；
// - 整块只剩标记（剥完为空白）⇒ **丢块**，不留空 text 块。
//
// 幂等：清洗过的切片再清洗一次结果不变（故 `WithThinkingBlock` 与
// `AssistantWithTools` 可以都调用它 —— 同一条链上不产生"二次副作用"）。
//
// 无改动时**返回原切片**（不重新分配）—— 便于调用方按同一性判断"这次有没有脏"。
func sanitizeAssistantBlocks(blocks []ContentBlock) []ContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	changedAny := false
	out := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" {
			out = append(out, b)
			continue
		}
		cleaned, changed := stripStrayReasoningLines(b.Text)
		if !changed {
			out = append(out, b)
			continue
		}
		changedAny = true
		if strings.TrimSpace(cleaned) == "" {
			continue // 整块只有标记 ⇒ 丢块
		}
		b.Text = cleaned
		out = append(out, b)
	}
	if !changedAny {
		return blocks
	}
	return out
}

// stripStrayReasoningLines 剥掉文本里**孤立成行**的推理边界标记。
//
// 围栏代码块（``` / ~~~）内部**不剥** —— 那里可能是模型正在展示/讨论这个标签。
// 返回 (清理后文本, 是否有改动)；无改动时原样返回入参。
func stripStrayReasoningLines(text string) (string, bool) {
	if text == "" {
		return text, false
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	inFence := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if isFenceLine(t) {
			inFence = !inFence
			out = append(out, ln)
			continue
		}
		if !inFence && reasoningBoundaryTags[t] {
			changed = true
			continue
		}
		out = append(out, ln)
	}
	if !changed {
		return text, false
	}
	// 剥完顺手去掉因删行产生的首尾空行（只动换行，不动缩进/内容）
	return strings.Trim(strings.Join(out, "\n"), "\n"), true
}

// isFenceLine 报告一行是否为 Markdown 围栏（``` 或 ~~~，可带语言标注）。
func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}
