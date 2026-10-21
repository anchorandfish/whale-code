// Package context — tests for ReactiveCompactor (P0-2).
package context

import (
	"os"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestReactiveCompactor_PreventsLoop verifies that the second call to
// TryReactiveCompact returns ok=false (loop prevention).
func TestReactiveCompactor_PreventsLoop(t *testing.T) {
	// Ensure feature flag is enabled.
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipeline(est, nil)
	r := NewReactiveCompactor(pipeline, nil)

	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	// First call: should attempt (ok may be true or false depending on
	// whether the pipeline removes anything, but HasAttempted should be true).
	_, _, reason1 := r.TryReactiveCompact(msgs)
	if !r.HasAttempted() {
		t.Error("HasAttempted should be true after first TryReactiveCompact")
	}

	// Second call: should be blocked by the loop-prevention flag.
	_, ok2, reason2 := r.TryReactiveCompact(msgs)
	if ok2 {
		t.Error("second TryReactiveCompact should return ok=false (loop prevention)")
	}
	// N11 G2 修复：reason 现在通过 aierrors.WithID 包装，携带 [ai_retryable] 前缀。
	// 验证 reason 包含原始消息且携带 ErrorID 前缀。
	if !strings.Contains(reason2, "reactive compact already attempted for this request") {
		t.Errorf("reason2 = %q, want loop-prevention message", reason2)
	}
	if !strings.HasPrefix(reason2, "[ai_retryable]") {
		t.Errorf("reason2 = %q, should have [ai_retryable] prefix (N11 G2)", reason2)
	}
	_ = reason1 // reason1 is informational
}

// TestReactiveCompactor_ResetAllowsNewAttempt verifies that after Reset(),
// a new reactive compaction attempt is allowed.
func TestReactiveCompactor_ResetAllowsNewAttempt(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipeline(est, nil)
	r := NewReactiveCompactor(pipeline, nil)

	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "hi"}}},
	}

	r.TryReactiveCompact(msgs)
	if !r.HasAttempted() {
		t.Fatal("HasAttempted should be true after first call")
	}

	r.Reset()
	if r.HasAttempted() {
		t.Fatal("HasAttempted should be false after Reset")
	}

	// Should be able to attempt again.
	_, _, _ = r.TryReactiveCompact(msgs)
	if !r.HasAttempted() {
		t.Error("HasAttempted should be true after second call post-Reset")
	}
}

// TestReactiveCompactor_FeatureFlagDisabled verifies that when
// WHALE_CTX_REACTIVE_COMPACT is explicitly set to "0", TryReactiveCompact
// is a no-op.
//
// P0-2 (X2): the flag defaults to ENABLED. To disable, the env must be
// explicitly set to "0" or "false". Unsetting the env means enabled.
func TestReactiveCompactor_FeatureFlagDisabled(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "0")
	defer os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil)
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}},
	}

	compacted, ok, reason := r.TryReactiveCompact(msgs)
	if ok {
		t.Error("ok should be false when feature flag is disabled")
	}
	if len(compacted) != len(msgs) {
		t.Error("compacted should be the original messages when flag is disabled")
	}
	// N11 G2 修复：reason 现在通过 aierrors.WithID 包装，携带 [ai_retryable] 前缀。
	if !strings.Contains(reason, "reactive compact disabled (WHALE_CTX_REACTIVE_COMPACT=0/false)") {
		t.Errorf("reason = %q, want disabled message", reason)
	}
	if !strings.HasPrefix(reason, "[ai_retryable]") {
		t.Errorf("reason = %q, should have [ai_retryable] prefix (N11 G2)", reason)
	}
	// HasAttempted should NOT be set when feature flag is off (the call
	// short-circuited before marking).
	if r.HasAttempted() {
		t.Error("HasAttempted should be false when feature flag is disabled")
	}
}

// TestReactiveCompactor_FeatureFlagDefaultEnabled verifies that when
// WHALE_CTX_REACTIVE_COMPACT is UNSET, the flag defaults to enabled
// (TryReactiveCompact proceeds past the flag check).
func TestReactiveCompactor_FeatureFlagDefaultEnabled(t *testing.T) {
	os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil)
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}},
	}

	_, _, reason := r.TryReactiveCompact(msgs)
	// With nil deps and flag enabled, the reason should be the
	// "no recovery mechanism" message (NOT the "disabled" message).
	if reason == "reactive compact disabled (WHALE_CTX_REACTIVE_COMPACT=0/false)" {
		t.Error("flag should default to enabled when env is unset")
	}
	// HasAttempted should be true since the flag was enabled and the call
	// proceeded past the flag check.
	if !r.HasAttempted() {
		t.Error("HasAttempted should be true when flag defaults to enabled")
	}
}

// TestReactiveCompactor_NilDependencies verifies that TryReactiveCompact
// handles nil pipeline and nil collapseMgr gracefully.
func TestReactiveCompactor_NilDependencies(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil)
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "y"}}},
	}

	compacted, ok, reason := r.TryReactiveCompact(msgs)
	if ok {
		t.Error("ok should be false with nil dependencies")
	}
	if len(compacted) != len(msgs) {
		t.Errorf("compacted len = %d, want %d", len(compacted), len(msgs))
	}
	// N11 G2 修复：reason 现在通过 aierrors.WithID 包装，携带 [ai_fatal] 前缀。
	if !strings.Contains(reason, "reactive compact: no recovery mechanism available") {
		t.Errorf("reason = %q, want no-recovery message", reason)
	}
	if !strings.HasPrefix(reason, "[ai_fatal]") {
		t.Errorf("reason = %q, should have [ai_fatal] prefix (N11 G2)", reason)
	}
}

// TestReactiveCompactor_PipelineCompacts verifies that when the pipeline
// removes messages, TryReactiveCompact returns ok=true.
func TestReactiveCompactor_PipelineCompacts(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipeline(est, nil)
	r := NewReactiveCompactor(pipeline, nil)

	// Build many messages so Snip/MicroCompact stages have work to do.
	// Include some tombstoned messages that Snip can remove.
	msgs := make([]session.Message, 30)
	for i := range msgs {
		msgs[i] = session.Message{
			Role:       session.RoleUser,
			Blocks:     []session.ContentBlock{{Type: "text", Text: "message body content"}},
			Tombstoned: i < 10, // first 10 are tombstoned → Snip removes them
		}
	}

	compacted, ok, reason := r.TryReactiveCompact(msgs)
	// Snip should remove the 10 tombstoned messages.
	if !ok {
		t.Errorf("expected ok=true (compaction should occur), got false; reason=%q", reason)
	}
	if len(compacted) >= len(msgs) {
		t.Errorf("expected compacted to be shorter: got len %d, original %d", len(compacted), len(msgs))
	}
}

// TestBuildPostCompactMessages_InsertsMarker verifies that the
// compact_boundary marker is inserted when ok=true, with the standard
// CompactMetadata schema (P1-1 / X12 integration).
func TestBuildPostCompactMessages_InsertsMarker(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "summary"}}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "recent"}}},
	}

	result := BuildPostCompactMessages(msgs, true)
	if len(result) != len(msgs)+1 {
		t.Fatalf("expected %d messages (marker + original), got %d", len(msgs)+1, len(result))
	}
	if result[0].Subtype != SubtypeCompactBoundary {
		t.Errorf("marker Subtype = %q, want %q", result[0].Subtype, SubtypeCompactBoundary)
	}
	if result[0].Role != session.RoleSystem {
		t.Errorf("marker Role = %q, want %q", result[0].Role, session.RoleSystem)
	}
	// P1-1: verify standard CompactMetadata schema (not ad-hoc compact_boundary=true).
	if result[0].Metadata == nil {
		t.Fatal("marker should have metadata")
	}
	if result[0].Metadata["compact_trigger"] != "reactive" {
		t.Errorf("compact_trigger = %v, want %q", result[0].Metadata["compact_trigger"], "reactive")
	}
	if result[0].Metadata["compact_messages_summarized"] != len(msgs) {
		t.Errorf("compact_messages_summarized = %v, want %d", result[0].Metadata["compact_messages_summarized"], len(msgs))
	}
	// The old ad-hoc key should NOT be present.
	if _, exists := result[0].Metadata["compact_boundary"]; exists {
		t.Error("ad-hoc compact_boundary=true should NOT be present — use SetCompactBoundaryMetadata schema")
	}
}

// TestBuildPostCompactMessages_NoMarkerWhenNotOk verifies that no marker
// is inserted when ok=false.
func TestBuildPostCompactMessages_NoMarkerWhenNotOk(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}},
	}

	result := BuildPostCompactMessages(msgs, false)
	if len(result) != len(msgs) {
		t.Errorf("expected %d messages (no marker), got %d", len(msgs), len(result))
	}
}

// TestBuildPostCompactMessages_EmptyInput verifies graceful handling of
// empty message lists.
func TestBuildPostCompactMessages_EmptyInput(t *testing.T) {
	result := BuildPostCompactMessages([]session.Message{}, true)
	if len(result) != 0 {
		t.Errorf("expected 0 messages for empty input, got %d", len(result))
	}
}

// TestReactiveCompactor_SetPipeline verifies late binding of the pipeline.
func TestReactiveCompactor_SetPipeline(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil)

	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipeline(est, nil)
	r.SetPipeline(pipeline)

	// After SetPipeline, TryReactiveCompact should use the pipeline.
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "z"}}},
	}
	_, _, reason := r.TryReactiveCompact(msgs)
	// With a single non-tombstoned message, pipeline won't remove anything,
	// but the reason should indicate the pipeline ran (not "no recovery mechanism").
	if reason == "reactive compact: no recovery mechanism available" {
		t.Errorf("reason should indicate pipeline ran, got %q", reason)
	}
}

// === N11 修复 G2 测试 ===

// TestReactiveCompactor_DisabledReasonHasErrorID 验证 disabled reason 携带
// ErrAIRetryable ErrorID（N11 修复 G2）。
func TestReactiveCompactor_DisabledReasonHasErrorID(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "0")
	defer os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil)
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}},
	}

	_, _, reason := r.TryReactiveCompact(msgs)
	// G2 修复：reason 应携带 [ai_retryable] 前缀
	if !strings.HasPrefix(reason, "[ai_retryable]") {
		t.Errorf("disabled reason should have [ai_retryable] prefix, got %q", reason)
	}
	if !strings.Contains(reason, "reactive compact disabled") {
		t.Errorf("disabled reason should contain 'reactive compact disabled', got %q", reason)
	}
}

// TestReactiveCompactor_AlreadyAttemptedReasonHasErrorID 验证 already-attempted
// reason 携带 ErrAIRetryable ErrorID（N11 修复 G2）。
func TestReactiveCompactor_AlreadyAttemptedReasonHasErrorID(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil)
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}},
	}

	// 第一次调用（标记 hasAttempted）
	r.TryReactiveCompact(msgs)
	// 第二次调用（应被 loop prevention 阻止）
	_, _, reason := r.TryReactiveCompact(msgs)

	if !strings.HasPrefix(reason, "[ai_retryable]") {
		t.Errorf("already-attempted reason should have [ai_retryable] prefix, got %q", reason)
	}
	if !strings.Contains(reason, "already attempted") {
		t.Errorf("already-attempted reason should contain 'already attempted', got %q", reason)
	}
}

// TestReactiveCompactor_NoRecoveryReasonHasErrorID 验证 no-recovery reason
// 携带 ErrAIFatal ErrorID（N11 修复 G2）。
func TestReactiveCompactor_NoRecoveryReasonHasErrorID(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	r := NewReactiveCompactor(nil, nil) // nil pipeline + nil collapseMgr
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}},
	}

	_, _, reason := r.TryReactiveCompact(msgs)

	// G2 修复：无恢复机制归为 ErrAIFatal（不可恢复，需用户干预）
	if !strings.HasPrefix(reason, "[ai_fatal]") {
		t.Errorf("no-recovery reason should have [ai_fatal] prefix, got %q", reason)
	}
	if !strings.Contains(reason, "no recovery mechanism available") {
		t.Errorf("no-recovery reason should contain 'no recovery mechanism available', got %q", reason)
	}
}

// TestReactiveCompactor_PipelineNoRemovalReasonHasErrorID 验证 pipeline 未移除消息时
// reason 携带 ErrAIRetryable ErrorID（N11 修复 G2）。
func TestReactiveCompactor_PipelineNoRemovalReasonHasErrorID(t *testing.T) {
	os.Setenv(EnvReactiveCompact, "1")
	defer os.Unsetenv(EnvReactiveCompact)

	est := ai.NewTokenEstimator()
	pipeline := NewCompactionPipeline(est, nil)
	r := NewReactiveCompactor(pipeline, nil)

	// 单条非 tombstoned 消息，pipeline 不会移除任何消息
	msgs := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "single msg"}}},
	}

	_, ok, reason := r.TryReactiveCompact(msgs)
	if ok {
		t.Error("ok should be false when pipeline removes nothing")
	}
	// G2 修复：pipeline 未能移除消息归为 ErrAIRetryable
	if !strings.HasPrefix(reason, "[ai_retryable]") {
		t.Errorf("pipeline-no-removal reason should have [ai_retryable] prefix, got %q", reason)
	}
	if !strings.Contains(reason, "pipeline compaction: no messages removed") {
		t.Errorf("reason should contain 'pipeline compaction: no messages removed', got %q", reason)
	}
}
