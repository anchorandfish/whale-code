// Package context - snip compaction
package context

import (
	"sort"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// messageImportanceWeight returns importance weight by role
func messageImportanceWeight(role session.Role) float64 {
	switch role {
	case session.RoleSystem:
		return 3.0
	case session.RoleUser:
		return 2.0
	case session.RoleAssistant:
		return 1.0
	case session.RoleTool:
		return 0.5
	default:
		return 1.0
	}
}

// weightedMessage is a message with importance weight and token count
type weightedMessage struct {
	Msg        session.Message
	Weight     float64
	TokenCount int
	OrigIndex  int
}

// SnipCompactor is the snip compactor
// modeled after a comparable CLI Snip Compact: removes zombie messages (empty content) and stale markers
type SnipCompactor struct {
	estimator *ai.TokenEstimator
}

// NewSnipCompactor creates a snip compactor
func NewSnipCompactor(estimator *ai.TokenEstimator) *SnipCompactor {
	return &SnipCompactor{estimator: estimator}
}

// SnipResult is the snip result
type SnipResult struct {
	// Messages is the message list after snipping
	Messages []session.Message
	// RemovedCount is the count of zombie messages removed
	RemovedCount int
}

// Snip removes zombie messages and stale markers
// filters out messages with empty content blocks and messages where all content blocks are empty
//
// N11 修复 G15：新增 Tombstoned 判断，移除软删除标记的消息。
// 契约：session.Message.Tombstoned 标记软删除（Compact 时跳过），
// 但现状 Snip 未处理此标记，导致测试 reactive_compact_test.go:172
// 设置 Tombstoned: true 后 Snip 不移除，测试与实现不一致。
// 修复后：Tombstoned=true 的消息在 Snip 阶段被移除（与 session 包
// 的 Compact 行为一致，session.go:286/305/342/398）。
func (s *SnipCompactor) Snip(messages []session.Message) *SnipResult {
	var result []session.Message
	removed := 0

	for _, msg := range messages {
		// G15 修复：移除 Tombstoned 软删除标记的消息
		if msg.Tombstoned {
			removed++
			continue
		}

		if len(msg.Blocks) == 0 {
			removed++
			continue
		}

		allEmpty := true
		for _, block := range msg.Blocks {
			if block.Text != "" || block.Type == "tool_use" || block.Type == "tool_result" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			removed++
			continue
		}

		result = append(result, msg)
	}

	return &SnipResult{
		Messages:     result,
		RemovedCount: removed,
	}
}

// SnipWithBudget removes zombie messages and truncates by importance when over budget
// messages are sorted by importance weight (system=3, user=2, assistant=1, tool=0.5)
// lower importance messages are removed first when budget is exceeded
// after greedy selection, ensures tool_use/tool_result pairs are kept or removed together
func (s *SnipCompactor) SnipWithBudget(messages []session.Message, maxTokens int) *SnipResult {
	snipResult := s.Snip(messages)
	filtered := snipResult.Messages

	if maxTokens <= 0 || len(filtered) == 0 {
		return snipResult
	}

	weighted := make([]weightedMessage, len(filtered))
	totalTokens := 0
	for i, msg := range filtered {
		tokens := s.estimateMessageTokens(msg)
		weighted[i] = weightedMessage{
			Msg:        msg,
			Weight:     messageImportanceWeight(msg.Role),
			TokenCount: tokens,
			OrigIndex:  i,
		}
		totalTokens += tokens
	}

	if totalTokens <= maxTokens {
		return snipResult
	}

	sort.Slice(weighted, func(i, j int) bool {
		wi := weighted[i].Weight / float64(max(weighted[i].TokenCount, 1))
		wj := weighted[j].Weight / float64(max(weighted[j].TokenCount, 1))
		if wi != wj {
			return wi > wj
		}
		return weighted[i].Weight > weighted[j].Weight
	})

	var kept []weightedMessage
	currentTokens := 0
	for _, wm := range weighted {
		if currentTokens+wm.TokenCount <= maxTokens {
			kept = append(kept, wm)
			currentTokens += wm.TokenCount
		}
	}

	kept = ensureToolPairCoherence(weighted, kept, &currentTokens, maxTokens)

	sort.Slice(kept, func(i, j int) bool {
		return kept[i].OrigIndex < kept[j].OrigIndex
	})

	result := make([]session.Message, len(kept))
	for i, wm := range kept {
		result[i] = wm.Msg
	}

	return &SnipResult{
		Messages:     result,
		RemovedCount: len(messages) - len(result),
	}
}

// ensureToolPairCoherence ensures tool_use and tool_result messages are kept or removed together
// if a tool_use message is kept, its corresponding tool_result must also be kept (and vice versa)
func ensureToolPairCoherence(all, kept []weightedMessage, currentTokens *int, maxTokens int) []weightedMessage {
	keptSet := make(map[int]bool)
	for _, wm := range kept {
		keptSet[wm.OrigIndex] = true
	}

	allByIndex := make(map[int]weightedMessage)
	for _, wm := range all {
		allByIndex[wm.OrigIndex] = wm
	}

	toolUseIDs := make(map[string]int)
	toolResultIDs := make(map[string]int)

	for i, msg := range allByIndex {
		for _, block := range msg.Msg.Blocks {
			if block.Type == "tool_use" && block.ID != "" {
				toolUseIDs[block.ID] = i
			}
			if block.Type == "tool_result" && block.ToolUseID != "" {
				toolResultIDs[block.ToolUseID] = i
			}
		}
	}

	changed := true
	for changed {
		changed = false
		var toAdd []weightedMessage

		for toolUseID, assistantIdx := range toolUseIDs {
			toolResultIdx, hasResult := toolResultIDs[toolUseID]
			if !hasResult {
				continue
			}
			assistantKept := keptSet[assistantIdx]
			resultKept := keptSet[toolResultIdx]

			if assistantKept && !resultKept {
				if wm, ok := allByIndex[toolResultIdx]; ok {
					toAdd = append(toAdd, wm)
				}
			} else if !assistantKept && resultKept {
				if wm, ok := allByIndex[assistantIdx]; ok {
					toAdd = append(toAdd, wm)
				}
			}
		}

		for _, wm := range toAdd {
			if !keptSet[wm.OrigIndex] {
				kept = append(kept, wm)
				keptSet[wm.OrigIndex] = true
				*currentTokens += wm.TokenCount
				changed = true
			}
		}
	}

	return kept
}

// estimateMessageTokens estimates token count for a session message
func (s *SnipCompactor) estimateMessageTokens(msg session.Message) int {
	total := 0
	for _, block := range msg.Blocks {
		if block.Text != "" {
			total += s.estimator.EstimateTextTokens(block.Text)
		}
		if block.Output != "" {
			total += s.estimator.EstimateTextTokens(block.Output)
		}
		if block.Input != "" {
			total += s.estimator.EstimateTextTokens(block.Input)
		}
	}
	return total
}
