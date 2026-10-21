package tui

import (
	"encoding/json"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/session"
)

// sessionSystemPromptBlocks moves persisted session system messages, including
// compaction summaries, into the dynamic system-prompt layer. Provider message
// arrays must contain only conversation roles supported by every backend.
func sessionSystemPromptBlocks(messages []session.Message) []ai.PromptLayerBlock {
	components := make([]ai.PromptComponent, 0)
	for _, msg := range messages {
		if msg.Role != session.RoleSystem {
			continue
		}
		content := strings.TrimSpace(msg.TextContent())
		if content == "" {
			continue
		}
		components = append(components, ai.PromptComponent{
			Name:    "session_conversation_context",
			Content: "Compacted conversation context from earlier messages:\n\n" + content,
			Stable:  false,
			Layer:   ai.LayerSessionState,
		})
	}
	if len(components) == 0 {
		return nil
	}
	components = prompt.SanitizeComponent(components)
	return ai.NewCacheAwareRequestBuilder().BuildSystemPromptBlocks(components)
}

func estimateSessionSystemPromptTokens(estimator *ai.TokenEstimator, messages []session.Message) int {
	if estimator == nil {
		return 0
	}
	total := 0
	for _, block := range sessionSystemPromptBlocks(messages) {
		if block.Text != "" {
			total += estimator.EstimateTextTokens(block.Text) + 4
		}
	}
	return total
}

// compactStaticInputOverhead returns request input that compaction cannot
// remove. The small summary-layer delta accounts for moving a persisted system
// summary into the provider system prompt with its explanatory prefix.
func compactRequestOverheads(estimator *ai.TokenEstimator, snapshot contextRequestSnapshot) (static, perSystem int) {
	if estimator == nil || !snapshot.Valid {
		return 0, 0
	}
	baseSystem := snapshot.SystemPrompt - snapshot.SessionSystem
	if baseSystem < 0 {
		baseSystem = 0
	}
	probe := session.Message{
		Role:   session.RoleSystem,
		Blocks: []session.ContentBlock{{Type: "text", Text: "summary-probe"}},
	}
	wrapped := estimateSessionSystemPromptTokens(estimator, []session.Message{probe})
	raw := estimator.EstimateSessionMessagesTokens([]session.Message{probe})
	wrapperOverhead := wrapped - raw
	if wrapperOverhead < 0 {
		wrapperOverhead = 0
	}
	return baseSystem + snapshot.Tools, wrapperOverhead
}

// sessionMessagesToAPI converts persisted conversation messages to the common
// provider-neutral request form. System messages are carried by
// sessionSystemPromptBlocks instead of appearing in the provider message list.
func sessionMessagesToAPI(messages []session.Message) []ai.MessageEntry {
	entries := make([]ai.MessageEntry, 0, len(messages))
	for _, msg := range messages {
		entry := ai.MessageEntry{Role: string(msg.Role)}
		switch msg.Role {
		case session.RoleUser:
			hasImage := false
			for _, b := range msg.Blocks {
				if b.Type == "image" {
					hasImage = true
					break
				}
			}
			if hasImage {
				blocks := make([]interface{}, 0, len(msg.Blocks))
				for _, b := range msg.Blocks {
					switch b.Type {
					case "text":
						blocks = append(blocks, map[string]interface{}{
							"type": "text",
							"text": b.Text,
						})
					case "image":
						blocks = append(blocks, map[string]interface{}{
							"type": "image",
							"source": map[string]interface{}{
								"type":       "base64",
								"media_type": b.MediaType,
								"data":       b.Base64Data,
							},
						})
					}
				}
				entry.Content = blocks
			} else {
				entry.Content = msg.TextContent()
			}
		case session.RoleAssistant:
			// 思考块要**如实带下去**：该不该写进请求体是「模型面」的事，
			// 唯一裁决在 ai.ResolveThinkingReplay。上游只负责不失真传递 ——
			// 此前这里只映射 text/tool_use，thinking 被静默丢弃，
			// 于是 DeepSeek 面（required：不回传即 400）根本拿不到内容可回传。
			if len(msg.ToolUses()) > 0 || msg.HasThinking() {
				blocks := make([]interface{}, 0, len(msg.Blocks))
				for _, b := range msg.Blocks {
					switch b.Type {
					case session.ThinkingBlockType:
						if strings.TrimSpace(b.Thinking) != "" {
							blocks = append(blocks, map[string]interface{}{
								"type":     "thinking",
								"thinking": b.Thinking,
							})
						}
					case "text":
						blocks = append(blocks, map[string]interface{}{
							"type": "text",
							"text": b.Text,
						})
					case "tool_use":
						var input interface{}
						if err := json.Unmarshal([]byte(b.Input), &input); err != nil {
							input = b.Input
						}
						blocks = append(blocks, map[string]interface{}{
							"type":  "tool_use",
							"id":    b.ID,
							"name":  b.Name,
							"input": input,
						})
					}
				}
				entry.Content = blocks
			} else {
				entry.Content = msg.TextContent()
			}
		case session.RoleTool:
			blocks := make([]interface{}, 0, len(msg.Blocks))
			for _, b := range msg.Blocks {
				blocks = append(blocks, map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": b.ToolUseID,
					"content":     b.Output,
				})
			}
			entry.Content = blocks
		default:
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}
