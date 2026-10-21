package session

import (
	"strings"
	"testing"
)

// (b) 入口清洗判别器 —— 「推理边界标记」必须挡在**会话落盘**之外。
//
// 现场（总账 明细）：会话存档里 L7 同时有 thinking 块**与**一个 text 块，
// 后者内容 = "\n</think>\n\n"（上游/网关泄漏）。该行会**上屏 → 落盘 → 下一轮回灌**。
//
// 清洗点是 `session` 的两个组装函数（见 reasoning_tags.go 的选址理由）：
// - `AssistantWithTools` = 四个落盘点共用（stream / plain×2 / bridge）；
// - `WithThinkingBlock` = `tui/stream.go` 里**渲染与落盘共用的那一份切片**。
//
// 判别力自证（-overlay mutant）：把 `sanitizeAssistantBlocks` 改成直返 blocks
// （其余全不动）⇒ 本文件的"判别器组"必须**精确打红**，反例组保持绿。

func txtBlocks(texts ...string) []ContentBlock {
	out := make([]ContentBlock, 0, len(texts))
	for _, t := range texts {
		out = append(out, ContentBlock{Type: "text", Text: t})
	}
	return out
}

// 判别器 ①：孤立成行的标记必须被剥掉（并经**生产组装函数**取回）。
func TestW018b_IsolatedTagIsStripped(t *testing.T) {
	leaks := []struct{ name, text string }{
		{"tag-only", "</think>"},
		{"leaked-shape-from-ledger", "\n</think>\n\n"}, // 总账 L7 的实测形态
		{"tag-then-answer", "</think>\n\n你好，这是回答。"},
		{"answer-then-tag", "回答正文。\n</think>"},
		{"open-tag-only", "<think>"},
		{"thinking-variant", "</thinking>\n正文"},
		{"reasoning-variant", "</reasoning>\n正文"},
	}
	for _, tc := range leaks {
		t.Run(tc.name, func(t *testing.T) {
			msg := AssistantWithTools(txtBlocks(tc.text), "m", nil)
			if got := msg.TextContent(); strings.Contains(got, "</think>") ||
				strings.Contains(got, "<think>") ||
				strings.Contains(got, "</thinking>") ||
				strings.Contains(got, "</reasoning>") {
				t.Fatalf("(b)：上游泄漏的孤立标记必须被入口清洗剥掉，实际落盘文本 = %q", got)
			}
			// 用户可见面：屏幕与落盘共用同一份 ⇒ 用 WithThinkingBlock 再验一次
			persisted := WithThinkingBlock(txtBlocks(tc.text), "")
			var sb strings.Builder
			for _, b := range persisted {
				if b.Type == "text" {
					sb.WriteString(b.Text)
				}
			}
			if strings.Contains(sb.String(), "</think>") {
				t.Fatalf("(b)：WithThinkingBlock 返回的切片（渲染与落盘共用）仍含孤立标记：%q", sb.String())
			}
		})
	}
}

// 判别器 ②：整块只剩标记 ⇒ **丢块**（不留空 text 块污染协议）。
func TestW018b_TagOnlyBlockIsDropped(t *testing.T) {
	msg := AssistantWithTools(txtBlocks("</think>"), "m", nil)
	if n := len(msg.Blocks); n != 0 {
		t.Fatalf("(b)：整块只有标记时应丢块，实际留下 %d 个块：%+v", n, msg.Blocks)
	}
	// 混合：一块是标记、一块是正文 ⇒ 只丢标记那块
	msg2 := AssistantWithTools(txtBlocks("</think>", "正文在这里"), "m", nil)
	if len(msg2.Blocks) != 1 || !strings.Contains(msg2.Blocks[0].Text, "正文在这里") {
		t.Fatalf("(b)：只应丢掉标记块，实际 %+v", msg2.Blocks)
	}
}

// ★ 反例组（证明规则是"窄"的，不是"任何出现都删"）。
//
// 若把 `stripStrayReasoningLines` 写成子串删除，本组立刻变红 —— 这正是要的护栏：
// 模型可能在**正文里引用**这个标签，或在**代码块里**展示它，那些都不该被动。
func TestW018b_InlineAndFencedTagsArePreserved(t *testing.T) {
	cases := []struct{ name, text, mustContain string }{
		{"inline-occurrence", "在回答里讨论 a</think>b 这种形态", "a</think>b"},
		{"inline-tag-in-sentence", "标记 </think> 的语义是结束推理段", "</think>"},
		{
			"fenced-code-block",
			"示例：\n```\n</think>\n```\n以上是网关泄漏的形态。",
			"```\n</think>\n```",
		},
		{
			"fenced-tilde",
			"~~~\n</think>\n~~~",
			"</think>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := AssistantWithTools(txtBlocks(tc.text), "m", nil)
			got := msg.TextContent()
			if !strings.Contains(got, tc.mustContain) {
				t.Fatalf("(b) 反例：%q 属**行内/代码块内**引用，不得被剥；实际 %q", tc.name, got)
			}
		})
	}
}

// 同源面：两个组装函数对**同一输入**给出同一份干净文本（防"只清了显示侧/只清了落盘侧"）。
func TestW018b_ScreenAndPersistedShareSameCleanedText(t *testing.T) {
	raw := txtBlocks("</think>", "正文")
	viaWithThinking := WithThinkingBlock(raw, "思考内容")
	viaAssistant := AssistantWithTools(viaWithThinking, "m", nil)

	collect := func(bs []ContentBlock) string {
		var sb strings.Builder
		for _, b := range bs {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	if a, b := collect(viaWithThinking), collect(viaAssistant.Blocks); a != b {
		t.Fatalf("(b) 同源面：渲染用的切片与落盘的切片正文不一致：%q vs %q", a, b)
	}
	if !strings.Contains(viaAssistant.TextContent(), "正文") {
		t.Fatalf("正常正文不得被误删，实际 %q", viaAssistant.TextContent())
	}
	// thinking 块不受清洗影响（它本来就不是 text 块）
	if !viaAssistant.HasThinking() || viaAssistant.ThinkingContent() != "思考内容" {
		t.Fatalf("thinking 块不得被入口清洗影响，实际 %q", viaAssistant.ThinkingContent())
	}
}

// 结构面：无改动时 `sanitizeAssistantBlocks` 必须**返回原切片**（不重新分配），
// 这条同时钉住"幂等 + 无副作用"——否则同一条链上二次调用会悄悄换掉调用方持有的切片。
func TestW018b_CleanInputIsReturnedUnchangedByIdentity(t *testing.T) {
	clean := txtBlocks("正常正文", "另一段")
	if out := sanitizeAssistantBlocks(clean); len(out) != len(clean) {
		t.Fatalf("(b)：干净输入不得改变块数")
	}
	// 幂等：清洗两次结果一致
	once := sanitizeAssistantBlocks(txtBlocks("</think>", "正文"))
	twice := sanitizeAssistantBlocks(once)
	if len(once) != len(twice) || once[0].Text != twice[0].Text {
		t.Fatalf("(b)：清洗必须幂等，once=%+v twice=%+v", once, twice)
	}
}

// ★ 边界：**用户消息**不参与清洗 —— 用户可能正当地在提问里写这个标签
// （例如"为什么我的输出里有 </think>"）。清洗只针对**模型产出**的正文。
func TestW018b_UserMessagesAreNotSanitized(t *testing.T) {
	u := UserText("为什么输出里会出现 </think>？")
	if !strings.Contains(u.TextContent(), "</think>") {
		t.Fatalf("(b)：用户消息不得被清洗（用户可正当讨论该标签），实际 %q", u.TextContent())
	}
}
