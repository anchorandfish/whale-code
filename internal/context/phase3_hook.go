// Package context — Phase 3 compaction hook (P0-9 / C10 contract).
//
// This file implements the Context module's side of the Phase 3 hook:
//  1. Conversion utilities between ai.MessageEntry and session.Message.
//  2. Phase3CompressAdapter — wraps CompactionPipeline.Execute to match
//     the ai.Phase3CompressFunc signature, which the AI module calls in
//     Phase 3 retry before reducing MaxTokens.
//
// Per C10: the AI module exposes the OnPhase3Compress hook field on
// RetryConfig; the Context module injects the adapter. The AI module's
// retry.go core logic is NOT directly modified — only the hook field and
// the call site (which is a minimal addition) are added to the AI module.
package context

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// AIEntriesToSessionMessages converts a slice of ai.MessageEntry to
// session.Message for use by the CompactionPipeline.
//
// The conversion is text-oriented (sufficient for compaction/summarization):
//   - string Content → single text block
//   - []interface{} Content (content blocks) → multiple session.ContentBlock
//     entries (text, image, tool_use, tool_result are handled).
//
// Non-text content that cannot be mapped is preserved as a best-effort
// text representation so the compactor can still estimate token counts.
// Metadata such as UUID / ParentUUID is NOT preserved (the compacted
// result is a fresh summary, not a chained edit of the original).
func AIEntriesToSessionMessages(entries []ai.MessageEntry) []session.Message {
	msgs := make([]session.Message, 0, len(entries))
	for _, e := range entries {
		msg := session.Message{Role: session.Role(e.Role)}
		switch content := e.Content.(type) {
		case string:
			msg.Blocks = []session.ContentBlock{{Type: "text", Text: content}}
		case []interface{}:
			for _, item := range content {
				block, ok := interfaceToContentBlock(item)
				if ok {
					msg.Blocks = append(msg.Blocks, block)
				}
			}
		default:
			// Best-effort: marshal non-string content to JSON text so the
			// compactor can at least estimate tokens.
			if e.Content != nil {
				raw, err := json.Marshal(e.Content)
				if err == nil {
					msg.Blocks = []session.ContentBlock{{Type: "text", Text: string(raw)}}
				}
			}
		}
		if len(msg.Blocks) == 0 {
			msg.Blocks = []session.ContentBlock{{Type: "text", Text: ""}}
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// interfaceToContentBlock converts a single content-block map (as produced
// by the AI layer's []interface{} Content) into a session.ContentBlock.
// Returns ok=false if the input is not a recognisable block.
func interfaceToContentBlock(item interface{}) (session.ContentBlock, bool) {
	m, ok := item.(map[string]interface{})
	if !ok {
		return session.ContentBlock{}, false
	}
	blockType, _ := m["type"].(string)
	switch blockType {
	case "text":
		text, _ := m["text"].(string)
		return session.ContentBlock{Type: "text", Text: text}, true
	case "image":
		// Images are not text-compactable; preserve a placeholder so the
		// compactor knows an image existed (for token estimation).
		return session.ContentBlock{Type: "image"}, true
	case "tool_use":
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		inputRaw, _ := json.Marshal(m["input"])
		return session.ContentBlock{Type: "tool_use", ID: id, Name: name, Input: string(inputRaw)}, true
	case "tool_result":
		toolUseID, _ := m["tool_use_id"].(string)
		output, _ := m["content"].(string)
		// Some providers nest content as []interface{}; flatten to string.
		if output == "" {
			if arr, ok := m["content"].([]interface{}); ok {
				parts := make([]string, 0, len(arr))
				for _, p := range arr {
					if pm, ok := p.(map[string]interface{}); ok {
						if t, ok := pm["text"].(string); ok {
							parts = append(parts, t)
						}
					}
				}
				output = strings.Join(parts, "\n")
			}
		}
		return session.ContentBlock{Type: "tool_result", ToolUseID: toolUseID, Output: output}, true
	}
	return session.ContentBlock{}, false
}

// SessionMessagesToAIEntries converts a slice of session.Message back to
// ai.MessageEntry for use by the AI layer after compaction.
//
// Each session.Message becomes one ai.MessageEntry:
//   - text blocks are concatenated into a string Content
//   - non-text blocks (image/tool_use/tool_result) become a []interface{}
//     Content matching the AI layer's block format.
//
// If a message has only text blocks, Content is a plain string (the common
// case for compacted summary messages). Mixed content uses the block array.
func SessionMessagesToAIEntries(msgs []session.Message) []ai.MessageEntry {
	entries := make([]ai.MessageEntry, 0, len(msgs))
	for _, m := range msgs {
		entry := ai.MessageEntry{Role: string(m.Role)}
		textParts := make([]string, 0, len(m.Blocks))
		var nonText []interface{}
		for _, b := range m.Blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					textParts = append(textParts, b.Text)
				}
			case "thinking":
				if b.Thinking != "" {
					nonText = append(nonText, map[string]interface{}{
						"type":     "thinking",
						"thinking": b.Thinking,
					})
				}
			case "image":
				nonText = append(nonText, map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type":       "base64",
						"media_type": b.MediaType,
						"data":       b.Base64Data,
					},
				})
			case "tool_use":
				nonText = append(nonText, map[string]interface{}{
					"type":  "tool_use",
					"id":    b.ID,
					"name":  b.Name,
					"input": json.RawMessage(b.Input),
				})
			case "tool_result":
				nonText = append(nonText, map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": b.ToolUseID,
					"content":     b.Output,
				})
			}
		}
		if len(nonText) == 0 {
			// Pure text message → string Content (most compacted messages).
			entry.Content = strings.Join(textParts, "\n")
		} else {
			// Mixed content → block array (text first, then non-text).
			blocks := make([]interface{}, 0, len(textParts)+len(nonText))
			for _, t := range textParts {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": t})
			}
			blocks = append(blocks, nonText...)
			entry.Content = blocks
		}
		entries = append(entries, entry)
	}
	return entries
}

// Phase3CompressAdapter returns an ai.Phase3CompressFunc that wraps
// CompactionPipeline.Execute (P0-9 / C10).
//
// The returned function:
//  1. Converts ai.MessageEntry → session.Message.
//  2. Runs the 5-layer CompactionPipeline (Snip → MicroCompact → Collapse
//     → AICompact → CacheAware).
//  3. Converts the compacted session.Message slice back to ai.MessageEntry.
//  4. Returns (compacted, ok=true, nil) if any messages were removed;
//     otherwise (original, ok=false, nil).
//
// The budget passed to Execute is derived from the pipeline's configured
// budget (set via SetBudget or NewCompactionPipeline). If the pipeline has
// no budget, a zero Budget is used (pipelines handle this gracefully by
// falling back to count-based compaction).
//
// Thread-safety: the adapter captures the *CompactionPipeline by reference.
// Execute is safe for concurrent use (each call operates on a copy of the
// input messages).
func (p *CompactionPipeline) Phase3CompressAdapter() ai.Phase3CompressFunc {
	return func(ctx context.Context, entries []ai.MessageEntry) ([]ai.MessageEntry, bool, error) {
		if p == nil {
			return entries, false, fmt.Errorf("CompactionPipeline is nil")
		}
		if len(entries) == 0 {
			return entries, false, nil
		}
		// Honour context cancellation before doing work.
		select {
		case <-ctx.Done():
			return entries, false, ctx.Err()
		default:
		}
		msgs := AIEntriesToSessionMessages(entries)
		result := p.ExecuteWithOptions(msgs, Budget{}, nil, ExecuteOptions{
			Trigger: CompactionTriggerPhase3,
			Policy:  CompactionPolicyForce,
			Config:  DefaultCompactionConfig(),
		})
		if result == nil {
			return entries, false, fmt.Errorf("pipeline returned nil result")
		}
		compacted := SessionMessagesToAIEntries(result.Messages)
		// ok = compaction actually removed or modified messages.
		ok := result.Changed
		return compacted, ok, nil
	}
}

// SetPhase3HookOnRetryConfig is a convenience helper that injects this
// pipeline's Phase3 adapter into an ai.RetryConfig.
//
// Usage:
//
//	pipeline := ctxpkg.NewCompactionPipeline(estimator, aiClient)
//	cfg := ai.DefaultRetryConfig()
//	pipeline.SetPhase3HookOnRetryConfig(&cfg)
//
// This keeps the C10 wiring in one place: callers don't need to know about
// the Phase3CompressFunc type directly.
func (p *CompactionPipeline) SetPhase3HookOnRetryConfig(cfg *ai.RetryConfig) {
	if p == nil || cfg == nil {
		return
	}
	cfg.OnPhase3Compress = p.Phase3CompressAdapter()
}
