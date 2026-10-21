// Package context — tests for compactBoundary semantic slicing (P1-1).
package context

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// makeBoundaryMsg creates a session.Message marked as a compact_boundary
// with the given summary text.
func makeBoundaryMsg(text string) session.Message {
	msg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: text},
		},
	}
	SetCompactBoundaryMetadata(&msg, CompactMetadata{
		Trigger:            "auto",
		PreTokens:          1000,
		PostTokens:         200,
		MessagesSummarized: 10,
	})
	return msg
}

// TestGetMessagesAfterCompactBoundary_NoBoundary verifies that when no
// compact_boundary marker exists, the entire slice is returned unchanged.
func TestGetMessagesAfterCompactBoundary_NoBoundary(t *testing.T) {
	msgs := makeMessages(5)
	result := GetMessagesAfterCompactBoundary(msgs)
	if len(result) != 5 {
		t.Errorf("expected 5 messages (no boundary), got %d", len(result))
	}
}

// TestGetMessagesAfterCompactBoundary_BoundaryInMiddle verifies that
// messages are sliced from the boundary onwards (inclusive).
func TestGetMessagesAfterCompactBoundary_BoundaryInMiddle(t *testing.T) {
	msgs := makeMessages(5)
	boundary := makeBoundaryMsg("summary")
	msgs = append(msgs[:2], append([]session.Message{boundary}, msgs[2:]...)...)
	// msgs is now: [m0, m1, boundary, m2, m3, m4] (6 messages)

	result := GetMessagesAfterCompactBoundary(msgs)
	if len(result) != 4 {
		t.Errorf("expected 4 messages (boundary + 3 after), got %d", len(result))
	}
	if result[0].Subtype != SubtypeCompactBoundary {
		t.Error("first message of result should be the boundary marker")
	}
}

// TestGetMessagesAfterCompactBoundary_BoundaryAtStart verifies that when
// the boundary is at index 0, the entire slice is returned (no removal).
func TestGetMessagesAfterCompactBoundary_BoundaryAtStart(t *testing.T) {
	boundary := makeBoundaryMsg("summary")
	msgs := append([]session.Message{boundary}, makeMessages(3)...)

	result := GetMessagesAfterCompactBoundary(msgs)
	if len(result) != 4 {
		t.Errorf("expected 4 messages (boundary at start), got %d", len(result))
	}
	if result[0].Subtype != SubtypeCompactBoundary {
		t.Error("first message should be boundary")
	}
}

// TestGetMessagesAfterCompactBoundary_BoundaryAtEnd verifies that when
// the boundary is the last message, only that message is returned.
func TestGetMessagesAfterCompactBoundary_BoundaryAtEnd(t *testing.T) {
	msgs := makeMessages(3)
	boundary := makeBoundaryMsg("summary")
	msgs = append(msgs, boundary)

	result := GetMessagesAfterCompactBoundary(msgs)
	if len(result) != 1 {
		t.Errorf("expected 1 message (boundary at end), got %d", len(result))
	}
	if result[0].Subtype != SubtypeCompactBoundary {
		t.Error("result should be the boundary marker")
	}
}

// TestGetMessagesAfterCompactBoundary_MultipleBoundaries verifies that
// the LAST boundary is used (most recent compaction).
func TestGetMessagesAfterCompactBoundary_MultipleBoundaries(t *testing.T) {
	b1 := makeBoundaryMsg("summary1")
	b2 := makeBoundaryMsg("summary2")
	msgs := []session.Message{b1, makeMessages(2)[0], makeMessages(2)[1], b2, makeMessages(2)[0]}
	// msgs: [b1, m0, m1, b2, m0] (5 messages)

	result := GetMessagesAfterCompactBoundary(msgs)
	if len(result) != 2 {
		t.Errorf("expected 2 messages (b2 + m0), got %d", len(result))
	}
	if result[0].Subtype != SubtypeCompactBoundary {
		t.Error("first message should be b2 (last boundary)")
	}
	// Verify it's the second boundary by checking the summary text
	if result[0].Blocks[0].Text != "summary2" {
		t.Errorf("expected summary2, got %q", result[0].Blocks[0].Text)
	}
}

// TestGetMessagesAfterCompactBoundary_EmptySlice verifies behavior on
// an empty message slice.
func TestGetMessagesAfterCompactBoundary_EmptySlice(t *testing.T) {
	result := GetMessagesAfterCompactBoundary([]session.Message{})
	if len(result) != 0 {
		t.Errorf("expected 0 messages for empty input, got %d", len(result))
	}
}

// TestFindLastCompactBoundaryIndex_Found verifies the index is correctly
// returned when a boundary exists.
func TestFindLastCompactBoundaryIndex_Found(t *testing.T) {
	msgs := makeMessages(5)
	boundary := makeBoundaryMsg("summary")
	msgs = append(msgs[:2], append([]session.Message{boundary}, msgs[2:]...)...)

	idx := FindLastCompactBoundaryIndex(msgs)
	if idx != 2 {
		t.Errorf("expected index 2, got %d", idx)
	}
}

// TestFindLastCompactBoundaryIndex_NotFound verifies -1 is returned when
// no boundary exists.
func TestFindLastCompactBoundaryIndex_NotFound(t *testing.T) {
	msgs := makeMessages(5)
	idx := FindLastCompactBoundaryIndex(msgs)
	if idx != -1 {
		t.Errorf("expected -1 (no boundary), got %d", idx)
	}
}

// TestHasCompactBoundary_True verifies true is returned when a boundary
// marker exists.
func TestHasCompactBoundary_True(t *testing.T) {
	msgs := makeMessages(3)
	boundary := makeBoundaryMsg("summary")
	msgs = append(msgs, boundary)

	if !HasCompactBoundary(msgs) {
		t.Error("expected HasCompactBoundary=true")
	}
}

// TestHasCompactBoundary_False verifies false is returned when no boundary
// marker exists.
func TestHasCompactBoundary_False(t *testing.T) {
	msgs := makeMessages(5)
	if HasCompactBoundary(msgs) {
		t.Error("expected HasCompactBoundary=false")
	}
}

// TestSetCompactBoundaryMetadata_SetsFields verifies that all metadata
// fields are correctly set on the message.
func TestSetCompactBoundaryMetadata_SetsFields(t *testing.T) {
	msg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "summary"},
		},
	}
	meta := CompactMetadata{
		Trigger:            "manual",
		PreTokens:          5000,
		PostTokens:         800,
		UserContext:        "focus on auth module",
		MessagesSummarized: 20,
	}
	SetCompactBoundaryMetadata(&msg, meta)

	if msg.Subtype != SubtypeCompactBoundary {
		t.Errorf("expected Subtype=%q, got %q", SubtypeCompactBoundary, msg.Subtype)
	}
	if msg.Metadata == nil {
		t.Fatal("Metadata should not be nil")
	}
	if msg.Metadata["compact_trigger"] != "manual" {
		t.Errorf("expected trigger=manual, got %v", msg.Metadata["compact_trigger"])
	}
	if msg.Metadata["compact_pre_tokens"] != 5000 {
		t.Errorf("expected pre_tokens=5000, got %v", msg.Metadata["compact_pre_tokens"])
	}
	if msg.Metadata["compact_post_tokens"] != 800 {
		t.Errorf("expected post_tokens=800, got %v", msg.Metadata["compact_post_tokens"])
	}
	if msg.Metadata["compact_messages_summarized"] != 20 {
		t.Errorf("expected messages_summarized=20, got %v", msg.Metadata["compact_messages_summarized"])
	}
	if msg.Metadata["compact_user_context"] != "focus on auth module" {
		t.Errorf("expected user_context set, got %v", msg.Metadata["compact_user_context"])
	}
}

// TestSetCompactBoundaryMetadata_EmptyUserContext verifies that an empty
// UserContext is NOT stored in the metadata map (keeps the map clean).
func TestSetCompactBoundaryMetadata_EmptyUserContext(t *testing.T) {
	msg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "summary"},
		},
	}
	meta := CompactMetadata{
		Trigger:            "auto",
		PreTokens:          1000,
		PostTokens:         200,
		UserContext:        "", // empty
		MessagesSummarized: 5,
	}
	SetCompactBoundaryMetadata(&msg, meta)

	if _, exists := msg.Metadata["compact_user_context"]; exists {
		t.Error("compact_user_context should NOT be set when UserContext is empty")
	}
}

// TestSetCompactBoundaryMetadata_PreservesExistingMetadata verifies that
// existing metadata keys are preserved when setting boundary metadata.
func TestSetCompactBoundaryMetadata_PreservesExistingMetadata(t *testing.T) {
	msg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: "summary"},
		},
		Metadata: map[string]interface{}{
			"custom_key": "custom_value",
		},
	}
	SetCompactBoundaryMetadata(&msg, CompactMetadata{
		Trigger:            "auto",
		PreTokens:          1000,
		PostTokens:         200,
		MessagesSummarized: 5,
	})

	if msg.Metadata["custom_key"] != "custom_value" {
		t.Error("existing metadata key should be preserved")
	}
	if msg.Subtype != SubtypeCompactBoundary {
		t.Error("Subtype should be set to compact_boundary")
	}
}

// TestAICompactorCompact_SetsBoundaryMetadata verifies that AICompactor.Compact
// marks the summary message with Subtype="compact_boundary" and populates
// the metadata fields (P1-1 integration).
func TestAICompactorCompact_SetsBoundaryMetadata(t *testing.T) {
	mockClient := &mockAIClient{
		streamResult: makeMockStream("AI generated summary"),
	}
	estimator := ai.NewTokenEstimator()
	compactor := NewAICompactor(mockClient, estimator)

	msgs := makeMessages(20)
	config := CompactionConfig{
		PreserveRecentMessages: 5,
		MaxEstimatedTokens:     10000,
	}

	result := compactor.Compact(msgs, config)
	if result == nil || len(result.CompactedMessages) == 0 {
		t.Fatal("expected non-nil result with messages")
	}

	// The first message of the result should be the summary, marked as
	// a compact_boundary.
	summaryMsg := result.CompactedMessages[0]
	if summaryMsg.Subtype != SubtypeCompactBoundary {
		t.Errorf("expected summary Subtype=%q, got %q", SubtypeCompactBoundary, summaryMsg.Subtype)
	}
	if summaryMsg.Metadata == nil {
		t.Fatal("summary message should have metadata set")
	}
	if summaryMsg.Metadata["compact_trigger"] != "auto" {
		t.Errorf("expected trigger=auto, got %v", summaryMsg.Metadata["compact_trigger"])
	}
	if summaryMsg.Metadata["compact_messages_summarized"] != 15 {
		t.Errorf("expected messages_summarized=15, got %v", summaryMsg.Metadata["compact_messages_summarized"])
	}
	// pre_tokens and post_tokens should be non-zero
	preTokens, _ := summaryMsg.Metadata["compact_pre_tokens"].(int)
	if preTokens <= 0 {
		t.Errorf("expected pre_tokens > 0, got %v", summaryMsg.Metadata["compact_pre_tokens"])
	}
}

// TestCompactionPipelineExecute_Step0SlicesBoundary verifies that
// CompactionPipeline.Execute step 0 slices the conversation at the
// compact_boundary marker, avoiding re-compaction of the summarized
// prefix (P1-1 integration).
func TestCompactionPipelineExecute_Step0SlicesBoundary(t *testing.T) {
	// Build a pipeline with a nil AI compactor (we only test step 0).
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	// Construct messages: [m0, m1, boundary, m2, m3, m4]
	// The prefix [m0, m1] should be sliced off by step 0.
	msgs := makeMessages(5)
	boundary := makeBoundaryMsg("previous summary")
	msgs = append(msgs[:2], append([]session.Message{boundary}, msgs[2:]...)...)
	// msgs = [m0, m1, boundary, m2, m3, m4] (6 messages, boundary at index 2)

	budget := DefaultBudget(200000)
	// Use a large context window so no compaction stages trigger — we
	// only want to verify step 0 slicing.
	result := pipeline.Execute(msgs, budget, nil)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Step 0 should have sliced off 2 messages (m0, m1).
	if len(result.Messages) != 4 {
		t.Errorf("expected 4 messages after step 0 slice, got %d", len(result.Messages))
	}
	if result.Messages[0].Subtype != SubtypeCompactBoundary {
		t.Error("first message should be the boundary marker")
	}
	// Verify compact_boundary_slice stage was executed.
	found := false
	for _, stage := range result.StagesExecuted {
		if stage == "compact_boundary_slice" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'compact_boundary_slice' in StagesExecuted")
	}
}

// TestCompactionPipelineExecute_Step0NoBoundary verifies that step 0
// does nothing when there is no compact_boundary marker.
func TestCompactionPipelineExecute_Step0NoBoundary(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	msgs := makeMessages(5)
	budget := DefaultBudget(200000)

	result := pipeline.Execute(msgs, budget, nil)

	// No slicing should occur — all 5 messages should remain.
	if len(result.Messages) != 5 {
		t.Errorf("expected 5 messages (no boundary), got %d", len(result.Messages))
	}
	for _, stage := range result.StagesExecuted {
		if stage == "compact_boundary_slice" {
			t.Error("compact_boundary_slice should NOT be in StagesExecuted when no boundary")
		}
	}
}

// TestCompactionPipelineExecute_Step0BoundaryAtStart verifies that when
// the boundary is at index 0, no slicing occurs (the entire slice is
// already the post-boundary set).
func TestCompactionPipelineExecute_Step0BoundaryAtStart(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	pipeline := NewCompactionPipelineNoAI(estimator)

	boundary := makeBoundaryMsg("summary")
	msgs := append([]session.Message{boundary}, makeMessages(3)...)
	// msgs = [boundary, m0, m1, m2] (4 messages, boundary at index 0)

	budget := DefaultBudget(200000)

	result := pipeline.Execute(msgs, budget, nil)

	// Boundary at index 0 means idx=0, which is NOT > 0, so no slicing.
	if len(result.Messages) != 4 {
		t.Errorf("expected 4 messages (boundary at start, no slice), got %d", len(result.Messages))
	}
	for _, stage := range result.StagesExecuted {
		if stage == "compact_boundary_slice" {
			t.Error("compact_boundary_slice should NOT occur when boundary is at index 0")
		}
	}
}
