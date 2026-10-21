package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/prompt"
	"github.com/anchorandfish/whale-code/internal/session"
)

func persistedCompactRunner(t *testing.T) *runnerCore {
	t.Helper()
	core := newTestRunnerCore()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	core.Store = store
	core.SessionName = "compact-test"
	core.PromptBuilder = prompt.NewBuilder(core.WorkDir, core.ModelName, core.MagicDocs, core.MemSelector, core.Executor, 100*1024)
	core.Session.SetMessages(markerMessagesForTUI(6))
	if err := store.Save(core.SessionName, core.Session); err != nil {
		t.Fatal(err)
	}
	return core
}

func TestSixTurnCompactPersistsCountAndFirstMarker(t *testing.T) {
	core := persistedCompactRunner(t)
	wantSnapshot := contextRequestSnapshot{SystemPrompt: 2000, Tools: 3000, Model: core.ModelName, Valid: true}
	core.requestSnapshot = wantSnapshot
	got := core.handleCompact("")
	// 批 3：改为「英文主行 + 缩进中文辅行」，断言意图不变（覆盖不缩水）。
	if !strings.Contains(got, "Messages: 12 -> 11") || !strings.Contains(got, "消息数：12 -> 11") {
		t.Fatalf("compact result missing exact message counts: %s", got)
	}
	if len(core.Session.GetMessages()) != 11 {
		t.Fatalf("live messages = %d, want 11", len(core.Session.GetMessages()))
	}
	if core.requestSnapshot != wantSnapshot {
		t.Fatalf("compact cleared reusable system/tools request snapshot: got %#v, want %#v", core.requestSnapshot, wantSnapshot)
	}
	contextAfterCompact := core.handleContextInfo()
	for _, want := range []string{"系统提示词: ~", "工具定义: ~3,000", "11 条", "已压缩: 1 times 次"} {
		if !strings.Contains(contextAfterCompact, want) {
			t.Fatalf("post-compact /context missing %q:\n%s", want, contextAfterCompact)
		}
	}
	if strings.Contains(contextAfterCompact, "系统提示词: 未知") || strings.Contains(contextAfterCompact, "工具定义: 未知") {
		t.Fatalf("post-compact /context discarded the last request snapshot:\n%s", contextAfterCompact)
	}
	loaded, err := core.Store.Load(core.SessionName)
	if err != nil {
		t.Fatal(err)
	}
	msgs := loaded.GetMessages()
	if len(msgs) != 11 || loaded.Metadata.CompactionCount != 1 {
		t.Fatalf("resumed messages/count = %d/%d, want 11/1", len(msgs), loaded.Metadata.CompactionCount)
	}
	if !strings.Contains(msgs[0].TextContent(), "CONTEXT-A") {
		t.Fatalf("first marker missing from summary: %q", msgs[0].TextContent())
	}
	assertCompactedContextInRequest(t, core.buildRequest(), "CONTEXT-A")
	removed, err := core.Store.Compact(core.SessionName)
	if err != nil || removed != 0 {
		t.Fatalf("normalized compact transcript lost messages: removed=%d err=%v", removed, err)
	}
}

func TestFullTUIBuildRequestCarriesCompactedContextAsDynamicSystemLayer(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.CurrentSession().SetMessages([]session.Message{
		{
			Role:    session.RoleSystem,
			Subtype: "compact_boundary",
			Blocks:  []session.ContentBlock{{Type: "text", Text: "User intent: CONTEXT-A"}},
		},
		session.UserText("What was the first marker?"),
	})

	assertCompactedContextInRequest(t, m.buildRequest(), "CONTEXT-A")
}

func assertCompactedContextInRequest(t *testing.T, req ai.Request, marker string) {
	t.Helper()
	for _, msg := range req.Messages {
		if msg.Role == string(session.RoleSystem) {
			t.Fatalf("provider messages contain unsupported system role: %#v", msg)
		}
	}
	if !strings.Contains(strings.Join(req.SystemPrompt, "\n"), marker) {
		t.Fatalf("fallback system prompt does not contain compacted marker %q", marker)
	}
	foundDynamicLayer := false
	for _, layer := range req.SystemPromptLayers {
		if strings.Contains(layer.Content, marker) {
			if layer.Layer != ai.LayerSessionState || layer.Stable {
				t.Fatalf("compacted context layer = %#v, want dynamic session state", layer)
			}
			foundDynamicLayer = true
		}
	}
	if !foundDynamicLayer {
		t.Fatalf("layered system prompt does not contain compacted marker %q", marker)
	}
}

func TestCompactCommitRejectsConcurrentMessage(t *testing.T) {
	core := persistedCompactRunner(t)
	expectedVersion, expectedRevision, err := core.captureCompactState()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ctxpkg.DefaultCompactionConfig()
	cfg.Trigger = ctxpkg.CompactionTriggerManual
	result := core.Compactor.Compact(core.Session.GetMessages(), cfg)
	if !result.Changed {
		t.Fatal("test setup did not produce a compact candidate")
	}
	core.Session.AddMessage(session.UserText("arrived while compacting"))

	err = core.commitCompactResult(result, cfg, nil, expectedVersion, expectedRevision)
	if !errors.Is(err, session.ErrSessionRewriteConflict) {
		t.Fatalf("commit error = %v, want conflict", err)
	}
	if got := core.Session.GetMessages(); len(got) != 13 || got[12].TextContent() != "arrived while compacting" {
		t.Fatalf("concurrent live message was overwritten: %#v", got)
	}
	loaded, err := core.Store.Load(core.SessionName)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GetMessages()) != 12 || loaded.Metadata.CompactionCount != 0 {
		t.Fatalf("failed commit changed disk messages/count = %d/%d", len(loaded.GetMessages()), loaded.Metadata.CompactionCount)
	}
}

func TestAutoCompactCaptureErrorIsNotHiddenByRevisionChange(t *testing.T) {
	m := newCompactTestModel(t)
	m.session.AddMessage(session.UserText("revision changed"))
	model, _ := m.Update(autoCompactResultMsg{
		err:              errors.New("capture failed"),
		sessionEpoch:     m.SessionModel.sessionEpoch,
		expectedRevision: 0,
	})
	updated := model.(Model)
	messages := updated.SessionModel.messages.All()
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "capture failed") {
		t.Fatalf("capture error was hidden: %#v", messages)
	}
}

func TestAutoCompactDueHonorsConfigAndBlocked(t *testing.T) {
	cfg := config.DefaultConfig()
	window := 100000
	budget := ctxpkg.DefaultBudget(window)

	// 主力线 = window - 13000 = 87000
	cases := []struct {
		name           string
		autoCompact    bool
		tokens         int
		expected       bool
		expectedReason string
	}{
		{"低于提示线不压缩", true, 70000, false, "80% 提示线 = 80000"},
		{"提示线区间不压缩", true, 82000, false, "0.80~0.898 决策窗口，只提示"},
		{"越过主力线压缩", true, 88000, true, "window-buffer = 87000"},
		{"Blocked 不压缩", true, 97000, false, "97% 只做硬停"},
		{"AutoCompact 关", false, 92000, false, "配置关闭"},
	}
	for _, tc := range cases {
		cfg.AutoCompact = tc.autoCompact
		got := autoCompactDue(&cfg, budget, tc.tokens, 0)
		if got != tc.expected {
			t.Errorf("%s: autoCompactDue(%d) = %v, want %v (%s)",
				tc.name, tc.tokens, got, tc.expected, tc.expectedReason)
		}
	}
}

func TestReactiveRetryRequiresCommittedCompaction(t *testing.T) {
	sess := session.NewSession()
	sess.SetMessages([]session.Message{session.UserText("still too large")})
	before := sessionRevision(sess)
	if compactCommittedSince(sess, before) {
		t.Fatal("unchanged non-empty session must not be retried")
	}
	sess.SetMessages([]session.Message{session.UserText("compacted")})
	if !compactCommittedSince(sess, before) {
		t.Fatal("successfully installed compact transcript should be retried")
	}
}

func TestRecoveryForCandidateKeepsPendingPlanWithoutReadFiles(t *testing.T) {
	source := session.NewSession()
	source.SetMessages([]session.Message{
		session.AssistantText("TODO: finish compact validation", "model", nil),
	})
	recovered := recoveryForCandidate(source.GetMessages(), source, nil)
	if len(recovered) != 1 || !strings.Contains(recovered[0].TextContent(), "Recovered pending plan") {
		t.Fatalf("pending plan was skipped without read files: %#v", recovered)
	}
}

func TestAICompactKeepsStrategyPreserveCount(t *testing.T) {
	for _, strategy := range []config.CompactionStrategy{config.CompactBalanced, config.CompactPreserveUser, config.CompactAggressive} {
		cfg, usage := parseManualCompactConfig("--ai", strategy)
		if usage != "" || !cfg.UseAISummary {
			t.Fatalf("strategy %s did not enable AI summary: cfg=%+v usage=%q", strategy, cfg, usage)
		}
		want := 10
		if strategy == config.CompactAggressive {
			want = 3
		}
		if got := cfg.EffectivePreserveCount(); got != want {
			t.Fatalf("strategy %s preserve count=%d, want %d", strategy, got, want)
		}
	}
}

func TestCompactUsageEntersCurrentSessionTotals(t *testing.T) {
	m := newCompactTestModel(t)
	recorder := newCompactCostRecorder("test-model", nil)
	recorder.RecordUsage(ai.UsageEvent{InputTokens: 321, OutputTokens: 45, CacheRead: 12})
	m.applyCompactRecorderUsage(recorder, m.SessionModel.sessionEpoch, m.SessionModel.modelEpoch, cacheCapabilityEpoch(m.aiService.Caching()))
	if m.sessionService.InputTokens() != 321 || m.sessionService.OutputTokens() != 45 {
		t.Fatalf("TUI compact usage totals = %d/%d, want 321/45", m.sessionService.InputTokens(), m.sessionService.OutputTokens())
	}
	if m.sessionService.RequestCount() != 1 {
		t.Fatalf("TUI compact request count = %d, want 1", m.sessionService.RequestCount())
	}

	core := newTestRunnerCore()
	coreRecorder := newCompactCostRecorder("test-model", nil)
	coreRecorder.RecordUsage(ai.UsageEvent{InputTokens: 123, OutputTokens: 54})
	core.applyCompactRecorderUsage(coreRecorder, core.sessionEpoch.Load(), core.ModelName, cacheCapabilityEpoch(core.Caching))
	if core.InputTokens != 123 || core.OutputTokens != 54 {
		t.Fatalf("runner compact usage totals = %d/%d, want 123/54", core.InputTokens, core.OutputTokens)
	}
	if core.RequestCount != 1 {
		t.Fatalf("runner compact request count = %d, want 1", core.RequestCount)
	}
}

func TestStaleAsyncCompactUsageIsCountedButCandidateIsDiscarded(t *testing.T) {
	m := newCompactTestModel(t)
	capturedModelEpoch := m.SessionModel.modelEpoch
	m.sessionService.SetModelName("other-model")
	if m.SessionModel.modelEpoch == capturedModelEpoch {
		t.Fatal("model switch did not advance the model epoch")
	}
	model, _ := m.Update(autoCompactResultMsg{
		sessionEpoch:    m.SessionModel.sessionEpoch,
		modelEpoch:      capturedModelEpoch,
		compactUsage:    ai.UsageEvent{InputTokens: 77, OutputTokens: 9},
		hasCompactUsage: true,
	})
	updated := model.(Model)
	if updated.sessionService.InputTokens() != 77 || updated.sessionService.OutputTokens() != 9 {
		t.Fatalf("stale compact billing fact was lost: %d/%d", updated.sessionService.InputTokens(), updated.sessionService.OutputTokens())
	}
	if updated.compactCount != 0 {
		t.Fatal("candidate built under a stale model epoch was committed")
	}
	if caching := updated.aiService.Caching(); caching != nil && caching.GetCacheInfo() != nil {
		t.Fatal("stale compact usage replaced the active cache snapshot")
	}
}
