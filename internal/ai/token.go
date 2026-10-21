// Package ai — CJK-aware Token estimation
//
// C16 冲突解决：AI 模块权威定义 TokenEstimator。
// TokenEstimator 的方法集构成跨模块共享的接口契约：
//
//	EstimateTextTokens(text string) int
//	EstimateMessagesTokens(messages []MessageEntry) int
//
// Context 模块和 MCP 模块引用 *ai.TokenEstimator 调用上述方法，
// 不重复实现 token 估算逻辑（MCP 移除"4 字节 ≈ 1 token"近似估算）。
//
// Token estimation uses heuristic character-based counting rather than
// a full tokenizer (e.g. tiktoken). This trades precision for simplicity:
//   - ASCII text: ~4 chars/token (matches most LLM tokenizers for English)
//   - CJK text:   ~1.5 chars/token (most models use 1-2 tokens per CJK character)
//   - Other text: ~2.5 chars/token (fallback for mixed/unknown scripts)
//
// Implications:
//   - May underestimate tokens for code-heavy content (code often uses 2-3 chars/token)
//   - May overestimate for repetitive content (where model BPE can compress further)
//   - Context budget management errs on the conservative side to avoid overflow
//
// For production accuracy on specific models (OpenAI GPT-4, 同类 CLI), consider
// integrating tiktoken-go or a model-specific tokenizer for critical models.
package ai

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/anchorandfish/whale-code/internal/session"
)

// TokenEstimator token estimator
type TokenEstimator struct{}

// NewTokenEstimator creates estimator
func NewTokenEstimator() *TokenEstimator {
	return &TokenEstimator{}
}

// EstimateSessionMessagesTokens converts persisted session messages to the
// provider-facing shape and estimates their input tokens. Provider usage is
// authoritative; this method is only for local budget and UI guidance.
func (e *TokenEstimator) EstimateSessionMessagesTokens(messages []session.Message) int {
	total := 0
	for _, msg := range messages {
		content := make([]interface{}, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch block.Type {
			case "image":
				content = append(content, map[string]interface{}{
					"type":   "image",
					"source": map[string]interface{}{"data": block.Base64Data},
				})
			case "tool_use":
				content = append(content, map[string]interface{}{"type": "input", "input": block.Name + block.Input})
			case "tool_result":
				content = append(content, map[string]interface{}{"type": "text", "text": block.Output})
			default:
				text := block.Text + block.Thinking + block.Input + block.Output
				content = append(content, map[string]interface{}{"type": "text", "text": text})
			}
		}
		total += e.EstimateMessageTokens(MessageEntry{Role: string(msg.Role), Content: content})
	}
	return total
}

// EstimateTextTokens estimates token count for text (CJK-aware)
// ASCII text ~4 chars/token, CJK text ~1.5 chars/token
func (e *TokenEstimator) EstimateTextTokens(text string) int {
	if text == "" {
		return 0
	}

	asciiCount := 0
	cjkCount := 0
	otherCount := 0

	for _, r := range text {
		switch {
		case r < 128:
			asciiCount++
		case isCJK(r):
			cjkCount++
		default:
			otherCount++
		}
	}

	asciiTokens := float64(asciiCount) / 4.0
	cjkTokens := float64(cjkCount) / 1.5
	otherTokens := float64(otherCount) / 2.5

	total := asciiTokens + cjkTokens + otherTokens
	if total < 1 {
		return 1
	}

	return int(total)
}

// EstimateMessagesTokens estimates token count for message list
func (e *TokenEstimator) EstimateMessagesTokens(messages []MessageEntry) int {
	total := 0
	for _, msg := range messages {
		total += e.EstimateMessageTokens(msg)
	}
	return total
}

// EstimateMessageTokens estimates token count for a single message
func (e *TokenEstimator) EstimateMessageTokens(msg MessageEntry) int {
	overhead := 4 // role + formatting overhead

	switch content := msg.Content.(type) {
	case string:
		return overhead + e.EstimateTextTokens(content)
	case []interface{}:
		textTokens := 0
		for _, item := range content {
			if block, ok := item.(map[string]interface{}); ok {
				blockType, _ := block["type"].(string)
				switch blockType {
				case "text":
					if text, ok := block["text"].(string); ok {
						textTokens += e.EstimateTextTokens(text)
					}
				case "input":
					if input, ok := block["input"].(string); ok {
						textTokens += e.EstimateTextTokens(input)
					}
				case "image", "image_url":
					textTokens += e.EstimateImageTokens(block)
				// ★ 以下三种块在 2026-10-14 之前被**静默漏计**（走不到任何 case ⇒ 只算 4 的
				// 消息开销）。它们不是边角料 —— tool_result 恰恰是编码 agent 上下文里最大的
				// 一块（sessionMessagesToAPI 对 RoleTool 就产出 tool_result），tool_use/thinking
				// 是助手侧的真实载荷。漏计的后果落在 estimateCurrentInputContextTokens() ⇒
				// ShouldAutoCompact 上：压缩会在**真实占用远超阈值**时才触发。
				// 判据：token_blocktypes_test.go（修复前三种块全计 0）。
				case "tool_result":
					if content, ok := block["content"].(string); ok {
						textTokens += e.EstimateTextTokens(content)
					}
				case "thinking":
					if th, ok := block["thinking"].(string); ok {
						textTokens += e.EstimateTextTokens(th)
					}
				case "tool_use":
					if name, ok := block["name"].(string); ok {
						textTokens += e.EstimateTextTokens(name)
					}
					// input 有两形态：tui 路径是**已反序列化的 JSON 值**（map/slice），
					// 旧的会话块路径是**字符串**。两者都要算，否则又漏一块。
					switch in := block["input"].(type) {
					case string:
						textTokens += e.EstimateTextTokens(in)
					case nil:
						// 无入参（如无参工具），不补零。
					default:
						if encoded, err := json.Marshal(in); err == nil {
							textTokens += e.EstimateTextTokens(string(encoded))
						}
					}
				}
			}
		}
		return overhead + textTokens
	default:
		return overhead
	}
}

// EstimateImageTokens estimates token count for an image content block
// Anthropic: ~85 tokens for small images, up to ~170 for large (based on pixels)
// OpenAI: low detail = 85 tokens, high detail = 85-170+ depending on resolution
//
// Since we don't have actual image dimensions, we estimate from the base64
// payload size. Base64 length ≈ raw bytes * 4/3. Assuming an average 3:1
// compression ratio for JPEG/PNG and 3 bytes/pixel (RGB), pixel count ≈
// base64Len * 3/4. Anthropic token formula is pixels/750, giving roughly
// base64Len/1000. We clamp to [85, 1600] to stay within sane bounds.
func (e *TokenEstimator) EstimateImageTokens(block map[string]interface{}) int {
	// Try Anthropic image format: { "source": { "data": "base64..." } }
	if source, ok := block["source"].(map[string]interface{}); ok {
		if data, ok := source["data"].(string); ok && data != "" {
			return estimateImageTokensFromBase64(data)
		}
	}
	// Try OpenAI image_url format: { "image_url": { "url": "data:image/...;base64,..." } }
	if imgURL, ok := block["image_url"].(map[string]interface{}); ok {
		if url, ok := imgURL["url"].(string); ok && url != "" {
			// Extract base64 part from data URI
			if idx := strings.Index(url, "base64,"); idx >= 0 {
				return estimateImageTokensFromBase64(url[idx+7:])
			}
			// Remote URL: use default estimate
			return 85
		}
	}
	// Fallback for unknown format
	return 128
}

// estimateImageTokensFromBase64 estimates tokens from a base64-encoded image
func estimateImageTokensFromBase64(b64 string) int {
	const (
		minTokens = 85
		maxTokens = 1600
	)
	// token ≈ base64Len / 1000, clamped to [minTokens, maxTokens]
	tokens := len(b64) / 1000
	if tokens < minTokens {
		return minTokens
	}
	if tokens > maxTokens {
		return maxTokens
	}
	return tokens
}

// EstimateSystemPromptTokens estimates token count for system prompt
func (e *TokenEstimator) EstimateSystemPromptTokens(prompts []string) int {
	total := 0
	for _, p := range prompts {
		total += e.EstimateTextTokens(p)
	}
	return total
}

// TruncateToTokenLimit truncates text to specified token limit
func (e *TokenEstimator) TruncateToTokenLimit(text string, maxTokens int) string {
	runes := []rune(text)
	estimated := e.EstimateTextTokens(text)
	if estimated <= maxTokens {
		return text
	}

	ratio := float64(maxTokens) / float64(estimated)
	targetRunes := int(float64(len(runes)) * ratio * 0.9) // Leave 10% margin

	if targetRunes >= len(runes) {
		return text
	}
	if targetRunes < 1 {
		targetRunes = 1
	}

	return string(runes[:targetRunes])
}

// isCJK determines if it is a CJK character
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Unified Ideographs Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Unified Ideographs Extension B
		(r >= 0x2A700 && r <= 0x2B73F) || // CJK Unified Ideographs Extension C
		(r >= 0x2B740 && r <= 0x2B81F) || // CJK Unified Ideographs Extension D
		(r >= 0x2B820 && r <= 0x2CEAF) || // CJK Unified Ideographs Extension E
		(r >= 0x3040 && r <= 0x309F) || // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) || // Katakana
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul Syllables
		(r >= 0xFF00 && r <= 0xFFEF) // Halfwidth and Fullwidth Forms
}

// RuneCount character count (not byte count)
func RuneCount(text string) int {
	return utf8.RuneCountInString(text)
}
