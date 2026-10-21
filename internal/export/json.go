package export

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/session"
)

// exportJSONMeta 导出 JSON 的元信息头部
type exportJSONMeta struct {
	ExportedAt    string `json:"exported_at"`
	MessageCount  int    `json:"message_count"`
	RedactedCount int    `json:"redacted_count"`
}

// exportJSONRoot 导出 JSON 的根结构
type exportJSONRoot struct {
	Meta     exportJSONMeta    `json:"meta"`
	Messages []session.Message `json:"messages"`
}

// FormatJSON 将消息列表序列化为 JSON
//
// 默认跳过 Metadata 和 Usage 字段（除非 fullMeta=true）。
// 结构：{"meta": {"exported_at": "...", "message_count": N, "redacted_count": N}, "messages": [...]}
//
// 参数：
//   - messages: 待序列化的消息列表
//   - fullMeta: 是否保留完整元数据（Metadata、Usage 等）
//   - redactor: 脱敏器，用于对文本内容脱敏
//
// 返回：
//   - string: 序列化后的 JSON 字符串
//   - int: 脱敏替换次数
func FormatJSON(messages []session.Message, fullMeta bool, redactor Redactor) (string, int) {
	totalRedacted := 0

	redactedMsgs := make([]session.Message, 0, len(messages))
	for _, msg := range messages {
		redactedMsg, count := redactMessage(msg, redactor)
		totalRedacted += count

		if !fullMeta {
			redactedMsg.Metadata = nil
			redactedMsg.Usage = nil
			redactedMsg.Model = ""
			redactedMsg.Tags = nil
			redactedMsg.Subtype = ""
			redactedMsg.UUID = ""
			redactedMsg.ParentUUID = ""
		}

		redactedMsgs = append(redactedMsgs, redactedMsg)
	}

	root := exportJSONRoot{
		Meta: exportJSONMeta{
			ExportedAt:    time.Now().Format(time.RFC3339),
			MessageCount:  len(redactedMsgs),
			RedactedCount: totalRedacted,
		},
		Messages: redactedMsgs,
	}

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":"json 序列化失败: %s"}`, strings.ReplaceAll(err.Error(), `"`, `\"`)), 0
	}

	return string(data), totalRedacted
}

// redactMessage 对单条消息的所有文本内容进行脱敏
//
// 遍历消息的所有 ContentBlock，对 text、thinking、input、output 等
// 文本字段执行脱敏。返回脱敏后的消息副本和替换次数。
func redactMessage(msg session.Message, redactor Redactor) (session.Message, int) {
	count := 0
	redactedMsg := msg
	redactedMsg.Blocks = make([]session.ContentBlock, len(msg.Blocks))
	copy(redactedMsg.Blocks, msg.Blocks)

	for i := range redactedMsg.Blocks {
		block := &redactedMsg.Blocks[i]
		switch block.Type {
		case "text":
			text, c := redactor.Scrub(block.Text)
			block.Text = text
			count += c
		case "thinking":
			thinking, c := redactor.Scrub(block.Thinking)
			block.Thinking = thinking
			count += c
		case "tool_use":
			input, c := redactor.Scrub(block.Input)
			block.Input = input
			count += c
		case "tool_result":
			output, c := redactor.Scrub(block.Output)
			block.Output = output
			count += c
		}
	}

	return redactedMsg, count
}
