package context

import (
	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

func estimateCompactCandidateTokens(estimator *ai.TokenEstimator, messages []session.Message, cfg CompactionConfig) int {
	tokens := estimateSessionMessagesTokens(estimator, messages)
	if cfg.SystemMessageOverheadTokens <= 0 {
		return tokens
	}
	for _, msg := range messages {
		if msg.Role == session.RoleSystem && !msg.Tombstoned {
			tokens += cfg.SystemMessageOverheadTokens
		}
	}
	return tokens
}

// FinalizeCompactResult adds optional recovery messages in stable priority
// order and enforces MaxEstimatedTokens on the exact candidate that will be
// persisted. Recovery entries that do not fit are skipped; the compacted
// conversation itself is never silently truncated here.
func FinalizeCompactResult(result *CompactResult, pipeline *CompactionPipeline, recovery []session.Message, cfg CompactionConfig, estimator *ai.TokenEstimator) bool {
	if result == nil || !result.Changed {
		return false
	}
	base := session.CloneMessages(result.CompactedMessages)
	if pipeline != nil {
		base = pipeline.PrepareCommit(base)
	}
	baseTokens := estimateCompactCandidateTokens(estimator, base, cfg)
	if cfg.MaxEstimatedTokens > 0 && baseTokens > cfg.MaxEstimatedTokens {
		result.Changed = false
		result.NoOpReason = CompactionNoOpBudgetUnachievable
		result.CompactedMessages = nil
		result.PostTokens = result.PreTokens
		result.PostMessageCount = result.PreMessageCount
		return false
	}

	accepted := make([]session.Message, 0, len(recovery))
	for _, recovered := range session.CloneMessages(recovery) {
		candidate := make([]session.Message, 0, len(accepted)+1+len(base))
		candidate = append(candidate, accepted...)
		candidate = append(candidate, recovered)
		candidate = append(candidate, base...)
		if cfg.MaxEstimatedTokens <= 0 || estimateCompactCandidateTokens(estimator, candidate, cfg) <= cfg.MaxEstimatedTokens {
			accepted = append(accepted, recovered)
		}
	}

	final := make([]session.Message, 0, len(accepted)+len(base))
	final = append(final, accepted...)
	final = append(final, base...)
	final = session.NormalizeMessageChain(final)
	finalTokens := estimateSessionMessagesTokens(estimator, final)
	finalBudgetTokens := estimateCompactCandidateTokens(estimator, final, cfg)
	if cfg.MaxEstimatedTokens > 0 && finalBudgetTokens > cfg.MaxEstimatedTokens {
		result.Changed = false
		result.NoOpReason = CompactionNoOpBudgetUnachievable
		result.CompactedMessages = nil
		result.PostTokens = result.PreTokens
		result.PostMessageCount = result.PreMessageCount
		return false
	}
	if cfg.Trigger != CompactionTriggerManual &&
		(finalTokens > result.PreTokens || (finalTokens == result.PreTokens && len(final) >= result.PreMessageCount)) {
		result.Changed = false
		result.NoOpReason = CompactionNoOpNotBeneficial
		result.CompactedMessages = nil
		result.PostTokens = result.PreTokens
		result.PostMessageCount = result.PreMessageCount
		return false
	}
	result.CompactedMessages = final
	result.PostTokens = finalTokens
	result.PostMessageCount = len(final)
	result.NoOpReason = CompactionNoOpNone
	return true
}
