package context

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

func markerConversation(turns int) []session.Message {
	messages := make([]session.Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		marker := fmt.Sprintf("CONTEXT-%c", 'A'+rune(i))
		messages = append(messages,
			session.UserText("Please reply only with "+marker+"."),
			session.AssistantText(marker, "test-model", nil),
		)
	}
	return messages
}

func manualOptions() ExecuteOptions {
	return ExecuteOptions{
		Trigger: CompactionTriggerManual,
		Policy:  CompactionPolicyForce,
		Config:  DefaultCompactionConfig(),
	}
}

type cacheNotificationSpy struct {
	deletions   int
	compactions int
}

func (s *cacheNotificationSpy) NotifyCacheDeletion() { s.deletions++ }
func (s *cacheNotificationSpy) NotifyCompaction()    { s.compactions++ }
func (s *cacheNotificationSpy) GetCapability() ai.CacheCapability {
	return ai.CacheCapability{}
}

func TestManualCompactForceCompactsStateOkConversation(t *testing.T) {
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	messages := markerConversation(6)
	result := pipeline.ExecuteWithOptions(messages, DefaultBudget(131072), nil, manualOptions())

	if !result.Changed {
		t.Fatalf("manual compaction should change a 12-message conversation: reason=%s", result.NoOpReason)
	}
	if result.TotalRemoved != 2 {
		t.Fatalf("removed=%d, want 2", result.TotalRemoved)
	}
	if len(result.Messages) >= len(messages) {
		t.Fatalf("message count did not decrease: %d -> %d", len(messages), len(result.Messages))
	}
	boundary := result.Messages[0]
	if boundary.Subtype != SubtypeCompactBoundary {
		t.Fatalf("first message subtype=%q, want %q", boundary.Subtype, SubtypeCompactBoundary)
	}
	if got := boundary.Metadata["compact_trigger"]; got != "manual" {
		t.Fatalf("compact_trigger=%v, want manual", got)
	}
	if !strings.Contains(boundary.TextContent(), "CONTEXT-A") {
		t.Fatalf("manual summary lost first marker: %q", boundary.TextContent())
	}
}

func TestManualCompactShortConversationReturnsStructuredNoOp(t *testing.T) {
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	cacheSpy := &cacheNotificationSpy{}
	pipeline.cachingIntegration = cacheSpy
	messages := markerConversation(4)
	result := pipeline.ExecuteWithOptions(messages, DefaultBudget(131072), nil, manualOptions())

	if result.Changed {
		t.Fatal("8-message conversation should be unchanged")
	}
	if result.NoOpReason != CompactionNoOpInsufficientMessages {
		t.Fatalf("reason=%q, want %q", result.NoOpReason, CompactionNoOpInsufficientMessages)
	}
	if result.EligibleCount != 8 || result.PreserveCount != 10 {
		t.Fatalf("eligible/preserve=%d/%d, want 8/10", result.EligibleCount, result.PreserveCount)
	}
	if !messagesSliceEqual(result.Messages, messages) {
		t.Fatal("no-op manual compaction changed messages")
	}
	if cacheSpy.deletions != 0 || cacheSpy.compactions != 0 {
		t.Fatalf("no-op emitted cache notifications: deletion=%d compaction=%d", cacheSpy.deletions, cacheSpy.compactions)
	}
}

func TestAutoCompactStateOkRemainsBudgeted(t *testing.T) {
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	messages := markerConversation(6)
	result := pipeline.Execute(messages, DefaultBudget(131072), nil)

	if result.Changed {
		t.Fatal("automatic compaction must remain budget-gated in StateOk")
	}
	if result.NoOpReason != CompactionNoOpWithinBudget {
		t.Fatalf("reason=%q, want %q", result.NoOpReason, CompactionNoOpWithinBudget)
	}
}

func TestManualCompactMicroOnlyCountsAsChanged(t *testing.T) {
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	messages := make([]session.Message, 6)
	for i := range messages {
		messages[i] = session.Message{
			Role: session.RoleTool,
			Blocks: []session.ContentBlock{{
				Type:      "tool_result",
				Output:    strings.Repeat("x", 600),
				ToolUseID: fmt.Sprintf("tool-%d", i),
				ToolName:  "read_file",
			}},
		}
	}
	result := pipeline.ExecuteWithOptions(messages, DefaultBudget(131072), nil, manualOptions())

	if !result.Changed {
		t.Fatalf("micro-only compaction should report Changed=true: reason=%s", result.NoOpReason)
	}
	if result.TotalRemoved != 0 {
		t.Fatalf("micro compaction removed %d messages, want 0", result.TotalRemoved)
	}
	if len(result.StagesExecuted) != 1 || result.StagesExecuted[0] != "micro_compact" {
		t.Fatalf("stages=%v, want [micro_compact]", result.StagesExecuted)
	}
	if result.PostTokens >= result.PreTokens {
		t.Fatalf("micro compaction tokens did not decrease: %d -> %d", result.PreTokens, result.PostTokens)
	}
}

func TestRepeatedManualCompactDoesNotDuplicateBoundary(t *testing.T) {
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	first := pipeline.ExecuteWithOptions(markerConversation(6), DefaultBudget(131072), nil, manualOptions())
	second := pipeline.ExecuteWithOptions(first.Messages, DefaultBudget(131072), nil, manualOptions())

	if second.Changed {
		t.Fatalf("second immediate compact should be a no-op, stages=%v", second.StagesExecuted)
	}
	if second.NoOpReason != CompactionNoOpInsufficientMessages {
		t.Fatalf("reason=%q, want %q", second.NoOpReason, CompactionNoOpInsufficientMessages)
	}
	boundaryCount := 0
	for _, msg := range second.Messages {
		if msg.Subtype == SubtypeCompactBoundary {
			boundaryCount++
		}
	}
	if boundaryCount != 1 {
		t.Fatalf("boundary count=%d, want 1", boundaryCount)
	}
}

func TestReactiveForceUsesReactiveBoundaryMetadata(t *testing.T) {
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	opts := manualOptions()
	opts.Trigger = CompactionTriggerReactive
	result := pipeline.ExecuteWithOptions(markerConversation(6), DefaultBudget(131072), nil, opts)

	if !result.Changed {
		t.Fatalf("reactive force did not compact: reason=%s", result.NoOpReason)
	}
	if got := result.Messages[0].Metadata["compact_trigger"]; got != "reactive" {
		t.Fatalf("compact_trigger=%v, want reactive", got)
	}
}
