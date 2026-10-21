package ai

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TestEstimateSessionMessageTokens_SplitsByContentType 是本批「仪表」的判别器。
//
// 它钉的不是"总数对不对"，而是**归类对不对** —— 一段 8000 字符的 tool_result
// 必须落进 ToolResults，**不能**混进 Conversation。
//
// 为什么归类才是关键：/context 的 Messages 行本来就是一筆总数（而且它按"请求形态"
// 计算，口径与本函数不同）。用户真正缺的是"这笔总数里，工具结果占多少" ——
// 工具结果是编码 agent 里最大、也最可过滤的一块，只有把它单独量出来，
// 「收紧工具输出」这类改动才有可比的基线。
//
// ★ 判别性自证（R4-7）：把 tool_result 的分类改回 Conversation（见同目录
// context_breakdown_mutation_probe 的说明），本用例必须转红 —— 见下方
// TestEstimateSessionMessageTokens_ToolResultNotMisclassifiedAsConversation。
func TestEstimateSessionMessageTokens_SplitsByContentType(t *testing.T) {
	e := NewTokenEstimator()

	var (
		userText   = strings.Repeat("a", 4000) // ASCII ⇒ ~1000 tok
		asstText   = strings.Repeat("b", 400)  // ~100 tok
		toolInput  = strings.Repeat("c", 4000) // ~1000 tok
		toolResult = strings.Repeat("d", 8000) // ~2000 tok
		thinking   = strings.Repeat("e", 2000) // ~500 tok
	)

	msgs := []session.Message{
		session.UserText(userText),
		// 助手：正文 + 一次工具调用（Input 是文件内容那种大载荷）
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
			{Type: "text", Text: asstText},
			{Type: "tool_use", ID: "t1", Name: "read_file", Input: toolInput},
		}},
		// 工具结果（通常挂在 user 回合）
		{Role: session.RoleUser, Blocks: []session.ContentBlock{
			{Type: "tool_result", ToolUseID: "t1", ToolName: "read_file", Output: toolResult},
		}},
		// 助手：思考块（DeepSeek required 面会回传）
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{
			{Type: session.ThinkingBlockType, Thinking: thinking},
		}},
		// 多模态：一张有内容的图
		session.UserWithImages("pic", []session.ImageBlock{
			{MediaType: "image/png", Base64Data: strings.Repeat("A", 4000)},
		}),
	}

	got := e.EstimateSessionMessageTokens(msgs)

	// ---- 归类（判别面）----
	if got.ToolResults < 1500 || got.ToolResults > 2500 {
		t.Errorf("ToolResults=%d, want ~2000（8000 ASCII / 4）", got.ToolResults)
	}
	if got.Conversation < 900 || got.Conversation > 1200 {
		t.Errorf("Conversation=%d, want ~1100（1000 user + 100 assistant）。★ 若约 3100，"+
			"说明 tool_result 被误判进 Conversation —— 归类失败", got.Conversation)
	}
	if got.ToolUse < 900 || got.ToolUse > 1100 {
		t.Errorf("ToolUse=%d, want ~1000（Name+Input，含 read_file 名字的少量开销）", got.ToolUse)
	}
	if got.Thinking < 400 || got.Thinking > 600 {
		t.Errorf("Thinking=%d, want ~500", got.Thinking)
	}
	if got.Images < 85 {
		t.Errorf("Images=%d, want >= 85（按 base64 长度估算）", got.Images)
	}
	if got.MessageCount != len(msgs) {
		t.Errorf("MessageCount=%d, want %d", got.MessageCount, len(msgs))
	}

	// ---- 自洽：Total 必须等于各桶之和（否则用户把子项加起来对不上）----
	sum := got.Conversation + got.ToolUse + got.ToolResults + got.Thinking + got.Images
	if got.Total != sum {
		t.Errorf("Total=%d != Σbuckets=%d（子项与总数不自洽）", got.Total, sum)
	}
}

// TestEstimateSessionMessageTokens_ToolResultNotMisclassifiedAsConversation 是
// 上面那条的**判别性配对**：直接把 tool_result 块伪装成普通 text 块，
// 断言它仍然计入 Conversation 而不是 ToolResults —— 反过来证明主用例里
// 「Conversation ≈ 1100 且 ToolResults ≈ 2000」这两个界是真的在区分两个世界，
// 而不是两条恒真断言。
func TestEstimateSessionMessageTokens_ToolResultNotMisclassifiedAsConversation(t *testing.T) {
	e := NewTokenEstimator()
	payload := strings.Repeat("x", 8000) // ~2000 tok

	// 同一段内容，两种承载方式：Type="tool_result" vs Type="text"
	asToolResult := e.EstimateSessionMessageTokens([]session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "tool_result", Output: payload}}},
	})
	asPlainText := e.EstimateSessionMessageTokens([]session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: payload}}},
	})

	if asToolResult.ToolResults < 1900 {
		t.Fatalf("tool_result 承载：ToolResults=%d, want >=1900", asToolResult.ToolResults)
	}
	if asToolResult.Conversation != 0 {
		t.Fatalf("tool_result 承载：Conversation=%d, want 0（不得混入）", asToolResult.Conversation)
	}
	if asPlainText.Conversation < 1900 {
		t.Fatalf("text 承载：Conversation=%d, want >=1900", asPlainText.Conversation)
	}
	if asPlainText.ToolResults != 0 {
		t.Fatalf("text 承载：ToolResults=%d, want 0", asPlainText.ToolResults)
	}
}

// TestEstimateSessionMessageTokens_EmptyIsZero 空输入不得凭空产生 token。
// 旧 EstimateImageTokens 对**空 base64**会返回 85 的下限（见 token.go:184），
// 这里必须挡住，否则一个空的 image 块会污染归因。
func TestEstimateSessionMessageTokens_EmptyIsZero(t *testing.T) {
	e := NewTokenEstimator()

	if got := e.EstimateSessionMessageTokens(nil); got.Total != 0 || got.MessageCount != 0 {
		t.Fatalf("nil 会话应全 0，得 %#v", got)
	}
	if got := e.EstimateSessionMessageTokens([]session.Message{}); got.Total != 0 {
		t.Fatalf("空切片应全 0，得 %#v", got)
	}
	got := e.EstimateSessionMessageTokens([]session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "image"}}}, // 空 base64
	})
	if got.Images != 0 || got.Total != 0 {
		t.Errorf("空 base64 image 块不得计 token：Images=%d Total=%d", got.Images, got.Total)
	}
}
