package context

import (
	"reflect"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

func TestCompactionPipelineCandidateDoesNotAliasInput(t *testing.T) {
	messages := []session.Message{
		{
			Role:   session.RoleUser,
			Blocks: []session.ContentBlock{{Type: "text", Text: "original"}},
			Tags:   []string{"keep"},
			Metadata: map[string]interface{}{
				"nested": map[string]interface{}{"value": "original"},
				"list":   []interface{}{map[string]interface{}{"value": "original"}},
			},
		},
	}
	want := session.CloneMessages(messages)
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	result := pipeline.ExecuteWithOptions(messages, DefaultBudget(131072), nil, manualOptions())
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("pipeline mutated input:\n got %#v\nwant %#v", messages, want)
	}

	result.Messages[0].Blocks[0].Text = "changed"
	result.Messages[0].Tags[0] = "changed"
	result.Messages[0].Metadata["nested"].(map[string]interface{})["value"] = "changed"
	result.Messages[0].Metadata["list"].([]interface{})[0].(map[string]interface{})["value"] = "changed"
	if !reflect.DeepEqual(messages, want) {
		t.Fatal("mutating a speculative candidate changed the source transcript")
	}
}

func TestCompactionPipelineDoesNotMutateCollapseManager(t *testing.T) {
	messages := markerConversation(8)
	cfg := DefaultCollapseConfig()
	cfg.PreserveCount = 2
	mgr := NewContextCollapseManager(cfg, ai.NewTokenEstimator(), DefaultBudget(131072))
	if err := mgr.StageCollapse(messages); err != nil {
		t.Fatal(err)
	}
	beforePhase := mgr.GetPhase()
	beforeStaged := mgr.GetCommitLog().StagedLen()
	if beforeStaged == 0 {
		t.Fatal("test setup did not stage a collapse")
	}

	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	pipeline.SetCollapseManager(mgr)
	_ = pipeline.Execute(messages, DefaultBudget(131072), nil)
	if got := mgr.GetPhase(); got != beforePhase {
		t.Fatalf("pipeline changed collapse phase: %v -> %v", beforePhase, got)
	}
	if got := mgr.GetCommitLog().StagedLen(); got != beforeStaged {
		t.Fatalf("pipeline changed staged commits: %d -> %d", beforeStaged, got)
	}
}

func TestFinalizeCompactResultEnforcesBudgetAfterRecovery(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	base := []session.Message{session.UserText("base")}
	keep := session.UserText("keep")
	skip := session.UserText(strings.Repeat("large recovery ", 100))
	maxTokens := estimator.EstimateSessionMessagesTokens(base) + estimator.EstimateSessionMessagesTokens([]session.Message{keep})
	result := &CompactResult{
		Changed: true, CompactedMessages: base,
		PreTokens: 100, PreMessageCount: 3,
	}
	cfg := DefaultCompactionConfig()
	cfg.MaxEstimatedTokens = maxTokens
	if !FinalizeCompactResult(result, nil, []session.Message{keep, skip}, cfg, estimator) {
		t.Fatal("candidate should fit after skipping oversized recovery")
	}
	if len(result.CompactedMessages) != 2 || result.CompactedMessages[0].TextContent() != "keep" {
		t.Fatalf("unexpected recovery selection: %#v", result.CompactedMessages)
	}
	if result.PostTokens > maxTokens {
		t.Fatalf("final tokens %d exceed budget %d", result.PostTokens, maxTokens)
	}

	overBudget := &CompactResult{
		Changed: true, CompactedMessages: []session.Message{skip},
		PreTokens: 100, PreMessageCount: 2,
	}
	if FinalizeCompactResult(overBudget, nil, nil, cfg, estimator) {
		t.Fatal("over-budget core candidate must be rejected")
	}
	if overBudget.Changed || overBudget.NoOpReason != CompactionNoOpBudgetUnachievable {
		t.Fatalf("rejection state = changed:%v reason:%q", overBudget.Changed, overBudget.NoOpReason)
	}
}

func TestFinalizeCompactResultCountsEverySystemLayerWrapper(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	base := []session.Message{{
		Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "summary"}},
	}}
	recovery := session.Message{
		Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "recovered plan"}},
	}
	cfg := DefaultCompactionConfig()
	cfg.SystemMessageOverheadTokens = 20
	cfg.MaxEstimatedTokens = estimator.EstimateSessionMessagesTokens(append(base, recovery)) + 20
	result := &CompactResult{
		Changed: true, CompactedMessages: base,
		PreTokens: 1000, PreMessageCount: 3,
	}
	if !FinalizeCompactResult(result, nil, []session.Message{recovery}, cfg, estimator) {
		t.Fatal("base summary should fit after skipping recovery wrapper overflow")
	}
	if len(result.CompactedMessages) != 1 {
		t.Fatalf("recovery with a second system wrapper bypassed the budget: %#v", result.CompactedMessages)
	}
}

func TestFinalizeCompactResultRejectsNonManualGrowthAfterRecovery(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	base := []session.Message{session.UserText("small candidate")}
	recovery := session.UserText(strings.Repeat("recovery ", 40))
	baseTokens := estimator.EstimateSessionMessagesTokens(base)
	for _, trigger := range []CompactionTrigger{CompactionTriggerAuto, CompactionTriggerReactive, CompactionTriggerPhase3} {
		result := &CompactResult{
			Changed: true, CompactedMessages: base,
			PreTokens: baseTokens + 1, PreMessageCount: 3,
		}
		cfg := DefaultCompactionConfig()
		cfg.Trigger = trigger
		cfg.MaxEstimatedTokens = 10000
		if FinalizeCompactResult(result, nil, []session.Message{recovery}, cfg, estimator) {
			t.Fatalf("%s compaction must reject recovery that makes the final context grow", trigger)
		}
		if result.NoOpReason != CompactionNoOpNotBeneficial || result.Changed {
			t.Fatalf("%s growth state = changed:%v reason:%q", trigger, result.Changed, result.NoOpReason)
		}
	}
}

func TestFinalizeCompactResultAllowsManualGrowth(t *testing.T) {
	estimator := ai.NewTokenEstimator()
	base := []session.Message{session.UserText("small candidate")}
	recovery := session.UserText(strings.Repeat("recovery ", 40))
	result := &CompactResult{
		Changed: true, CompactedMessages: base,
		PreTokens: estimator.EstimateSessionMessagesTokens(base) + 1, PreMessageCount: 3,
	}
	cfg := DefaultCompactionConfig()
	cfg.Trigger = CompactionTriggerManual
	cfg.MaxEstimatedTokens = 10000
	if !FinalizeCompactResult(result, nil, []session.Message{recovery}, cfg, estimator) {
		t.Fatal("manual compaction should preserve the explicit allow-growth behavior")
	}
}

func TestCacheNotificationsOnlyPublishAfterCommit(t *testing.T) {
	spy := &cacheNotificationSpy{}
	pipeline := NewCompactionPipelineNoAI(ai.NewTokenEstimator())
	pipeline.cachingIntegration = spy
	_ = pipeline.PrepareCommit([]session.Message{session.UserText("candidate")})
	if spy.deletions != 0 || spy.compactions != 0 {
		t.Fatalf("PrepareCommit published notifications: %#v", spy)
	}
	pipeline.CommitPrepared()
	if spy.deletions != 1 || spy.compactions != 1 {
		t.Fatalf("CommitPrepared notifications = %d/%d, want 1/1", spy.deletions, spy.compactions)
	}
}
