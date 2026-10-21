package ai

import "github.com/anchorandfish/whale-code/internal/session"

// SessionMessageTokens 把一段对话的**内容 token** 按承载它的块类型拆开。
//
// 为什么需要它（第一性原理）：省 token 只能从"钱花在哪"下手，而 /context 原有的
// Composition 只给到 System / Tools / Messages 三档 —— Messages 是一整块不透明的，
// 可对编码 agent 来说，最大、也最可过滤的一块恰恰是**工具结果**（一次 `go test`
// 或 `cat` 就可能上万 token）。把它单独量出来，「收紧工具输出」这类改动才有可比的基线。
//
// 口径（必须与调用方约定清楚）：
//   - 这是**本地估算**（TokenEstimator 的字数启发式），provider 的 usage 才是权威；
//   - `Total == Conversation + ToolUse + ToolResults + Thinking + Images`（自洽，
//     用户把子项加起来必须等于总数）；
//   - **不含**每消息的 role/格式开销（那 4 token 属于消息信封，不属于任何内容类型）。
//     因此 Total 与 EstimateSessionMessagesTokens 差 `4 × MessageCount`。
//
// ★ 本结构只服务"看清构成"。它**不得**被接进任何驱动压缩/预算的判定 ——
// 那条链的口径是 estimateCurrentInputContext / EstimateSessionMessagesTokens，
// 改动它会改变 ShouldAutoCompact 的触发时机（= 行为变更，不是加仪表）。
type SessionMessageTokens struct {
	Conversation int // 用户 + 助手 text 块（对话正文）
	ToolUse      int // tool_use 块：助手发起的工具调用（含 Input 载荷，如 Write 的文件内容）
	ToolResults  int // tool_result 块：回喂给模型的工具输出
	Thinking     int // thinking / 推理块
	Images       int // 图片块
	MessageCount int // 参与统计的消息条数
	Total        int // 五个桶之和
}

// EstimateSessionMessageTokens 遍历会话消息，按块类型归因内容 token。
//
// 归因规则与本包既有的 EstimateSessionMessagesTokens 保持**同源**（同一种块 → 同一段
// 文本），只是把它拆桶，避免出现"两套各算各的"：
//
//	image       → Images      （base64 长度估算；空 base64 不计，见下）
//	tool_use    → ToolUse     （Name + Input，与既有实现逐字一致）
//	tool_result → ToolResults （Output）
//	thinking    → Thinking    （Thinking）
//	其它/default → Conversation（Text + Thinking + Input + Output）
//
// 空 Base64Data 的 image 块**不计**：既有 EstimateImageTokens 对空串会落到 85 的下限，
// 那会在归因里凭空造出 token（一个空块污染整段比例）。
func (e *TokenEstimator) EstimateSessionMessageTokens(messages []session.Message) SessionMessageTokens {
	var r SessionMessageTokens
	for _, msg := range messages {
		r.MessageCount++
		for _, block := range msg.Blocks {
			switch block.Type {
			case "image":
				if block.Base64Data != "" {
					r.Images += estimateImageTokensFromBase64(block.Base64Data)
				}
			case "tool_use":
				r.ToolUse += e.EstimateTextTokens(block.Name + block.Input)
			case "tool_result":
				r.ToolResults += e.EstimateTextTokens(block.Output)
			case session.ThinkingBlockType:
				r.Thinking += e.EstimateTextTokens(block.Thinking)
			default:
				r.Conversation += e.EstimateTextTokens(block.Text + block.Thinking + block.Input + block.Output)
			}
		}
	}
	r.Total = r.Conversation + r.ToolUse + r.ToolResults + r.Thinking + r.Images
	return r
}
