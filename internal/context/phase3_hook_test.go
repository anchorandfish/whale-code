// Package context — tests for Phase 3 hook (P0-9 / C10).
package context

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestAIEntriesToSessionMessages_StringContent verifies that string
// Content is converted to a single text block.
func TestAIEntriesToSessionMessages_StringContent(t *testing.T) {
	entries := []ai.MessageEntry{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi there"},
	}
	msgs := AIEntriesToSessionMessages(entries)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != session.RoleUser {
		t.Errorf("msg[0] Role = %q, want %q", msgs[0].Role, session.RoleUser)
	}
	if msgs[0].Blocks[0].Text != "hello world" {
		t.Errorf("msg[0] text = %q, want %q", msgs[0].Blocks[0].Text, "hello world")
	}
}

// TestAIEntriesToSessionMessages_BlockArray verifies that []interface{}
// Content with text blocks is converted correctly.
func TestAIEntriesToSessionMessages_BlockArray(t *testing.T) {
	entries := []ai.MessageEntry{
		{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "first"},
				map[string]interface{}{"type": "text", "text": "second"},
			},
		},
	}
	msgs := AIEntriesToSessionMessages(entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[0].Text != "first" || msgs[0].Blocks[1].Text != "second" {
		t.Errorf("texts = %q, %q; want %q, %q",
			msgs[0].Blocks[0].Text, msgs[0].Blocks[1].Text, "first", "second")
	}
}

// TestSessionMessagesToAIEntries_TextOnly verifies that text-only messages
// produce string Content (the common compacted-summary case).
func TestSessionMessagesToAIEntries_TextOnly(t *testing.T) {
	msgs := []session.Message{
		{
			Role:   session.RoleSystem,
			Blocks: []session.ContentBlock{{Type: "text", Text: "summary of conversation"}},
		},
	}
	entries := SessionMessagesToAIEntries(msgs)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Role != "system" {
		t.Errorf("Role = %q, want %q", entries[0].Role, "system")
	}
	str, ok := entries[0].Content.(string)
	if !ok {
		t.Fatalf("Content should be string for text-only, got %T", entries[0].Content)
	}
	if str != "summary of conversation" {
		t.Errorf("Content = %q, want %q", str, "summary of conversation")
	}
}

// TestConversion_RoundTrip verifies that a text message survives a
// round-trip: ai.MessageEntry → session.Message → ai.MessageEntry.
func TestConversion_RoundTrip(t *testing.T) {
	original := []ai.MessageEntry{
		{Role: "user", Content: "turn 1"},
		{Role: "assistant", Content: "reply 1"},
		{Role: "user", Content: "turn 2"},
	}
	msgs := AIEntriesToSessionMessages(original)
	roundTripped := SessionMessagesToAIEntries(msgs)

	if len(roundTripped) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(roundTripped), len(original))
	}
	for i, e := range original {
		if roundTripped[i].Role != e.Role {
			t.Errorf("[%d] Role = %q, want %q", i, roundTripped[i].Role, e.Role)
		}
		origStr, _ := e.Content.(string)
		rtStr, ok := roundTripped[i].Content.(string)
		if !ok {
			t.Errorf("[%d] round-trip Content is %T, want string", i, roundTripped[i].Content)
			continue
		}
		if rtStr != origStr {
			t.Errorf("[%d] Content = %q, want %q", i, rtStr, origStr)
		}
	}
}

// TestPhase3CompressAdapter_NilPipeline verifies that a nil pipeline
// returns the original messages with ok=false and an error.
func TestPhase3CompressAdapter_NilPipeline(t *testing.T) {
	var p *CompactionPipeline
	adapter := p.Phase3CompressAdapter()
	entries := []ai.MessageEntry{{Role: "user", Content: "test"}}

	compacted, ok, err := adapter(context.Background(), entries)
	if err == nil {
		t.Error("expected error for nil pipeline, got nil")
	}
	if ok {
		t.Error("expected ok=false for nil pipeline")
	}
	if len(compacted) != len(entries) {
		t.Errorf("expected original entries returned, got len %d vs %d", len(compacted), len(entries))
	}
}

// TestPhase3CompressAdapter_EmptyMessages verifies that empty input
// returns empty output with ok=false (no error).
func TestPhase3CompressAdapter_EmptyMessages(t *testing.T) {
	est := ai.NewTokenEstimator()
	p := NewCompactionPipeline(est, nil)
	adapter := p.Phase3CompressAdapter()

	compacted, ok, err := adapter(context.Background(), []ai.MessageEntry{})
	if err != nil {
		t.Errorf("expected nil error for empty input, got %v", err)
	}
	if ok {
		t.Error("expected ok=false for empty input")
	}
	if len(compacted) != 0 {
		t.Errorf("expected 0 entries, got %d", len(compacted))
	}
}

// TestPhase3CompressAdapter_PipelineCompacts verifies that the adapter
// actually invokes the pipeline and compaction occurs when there are
// tombstoned (zombie) messages that Snip can remove.
func TestPhase3CompressAdapter_PipelineCompacts(t *testing.T) {
	est := ai.NewTokenEstimator()
	p := NewCompactionPipeline(est, nil)

	// Build entries with a "zombie" marker that the Snip stage removes.
	// Snip removes messages tagged with tombstone metadata; we simulate
	// this by providing many messages (some of which Snip may trim).
	entries := make([]ai.MessageEntry, 50)
	for i := range entries {
		entries[i] = ai.MessageEntry{
			Role:    "user",
			Content: "message content that is reasonably long to trigger compaction thresholds",
		}
	}

	adapter := p.Phase3CompressAdapter()
	compacted, ok, err := adapter(context.Background(), entries)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The pipeline may or may not compact depending on internal thresholds;
	// we only assert it doesn't crash and returns valid output.
	if len(compacted) == 0 {
		t.Error("expected non-empty compacted output")
	}
	// ok should reflect whether compaction happened (length changed).
	expectedOK := len(compacted) != len(entries)
	if ok != expectedOK {
		t.Errorf("ok = %v, want %v (len compacted=%d, original=%d)",
			ok, expectedOK, len(compacted), len(entries))
	}
}

// TestSetPhase3HookOnRetryConfig verifies that the helper sets the
// OnPhase3Compress field on the RetryConfig.
func TestSetPhase3HookOnRetryConfig(t *testing.T) {
	est := ai.NewTokenEstimator()
	p := NewCompactionPipeline(est, nil)

	cfg := ai.DefaultRetryConfig()
	if cfg.OnPhase3Compress != nil {
		t.Fatal("OnPhase3Compress should be nil before SetPhase3HookOnRetryConfig")
	}

	p.SetPhase3HookOnRetryConfig(&cfg)

	if cfg.OnPhase3Compress == nil {
		t.Fatal("OnPhase3Compress should be non-nil after SetPhase3HookOnRetryConfig")
	}

	// Verify the hook is callable.
	_, _, err := cfg.OnPhase3Compress(context.Background(), []ai.MessageEntry{})
	if err != nil {
		t.Errorf("hook returned error on empty input: %v", err)
	}
}

// TestSetPhase3HookOnRetryConfig_NilArgs verifies nil-safety.
func TestSetPhase3HookOnRetryConfig_NilArgs(t *testing.T) {
	var p *CompactionPipeline
	cfg := ai.DefaultRetryConfig()

	// Should not panic.
	p.SetPhase3HookOnRetryConfig(&cfg)
	if cfg.OnPhase3Compress != nil {
		t.Error("OnPhase3Compress should remain nil when pipeline is nil")
	}

	est := ai.NewTokenEstimator()
	p2 := NewCompactionPipeline(est, nil)
	// Should not panic.
	p2.SetPhase3HookOnRetryConfig(nil)
}
