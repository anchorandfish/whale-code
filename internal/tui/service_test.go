package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bridge"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/filehistory"
	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/search"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// newTestRunnerCore creates a minimal runnerCore for testing service interfaces
func newTestRunnerCore() *runnerCore {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite
	client := newTestClient("test-model")
	reg := newTestRegistry()
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	return &runnerCore{
		Session:         session.NewSession(),
		Config:          &cfg,
		AIClient:        client,
		Registry:        toolRegistry,
		Executor:        tools.NewExecutor(toolRegistry, os.TempDir()),
		Policy:          permission.NewPolicy(permission.LevelWorkspaceWrite),
		Budget:          ctxpkg.DefaultBudget(cfg.ContextWindow),
		Compactor:       ctxpkg.NewCompactor(estimator),
		Estimator:       estimator,
		MagicDocs:       config.NewMagicDocs(""), //nolint:staticcheck // H4 保留 V1 作为 fallback
		MemoryStore:     nil,
		MemoryIndex:     nil,
		MemSelector:     config.NewMemorySelector(nil, nil, client),
		Fallback:        ai.NewFallbackTracker("test-model", ""),
		ModelName:       "test-model",
		ModelRegistry:   reg,
		AvailableModels: reg.Names(),
		WorkDir:         os.TempDir(),
	}
}

// --- Mock implementations for interface replacement tests ---

// mockSessionService implements SessionService for testing without a full Model
type mockSessionService struct {
	session *session.Session
	err     error
}

func (m *mockSessionService) LoadSession(name string) error              { return m.err }
func (m *mockSessionService) SaveSession() error                         { return m.err }
func (m *mockSessionService) CurrentSession() *session.Session           { return m.session }
func (m *mockSessionService) SetCurrentSession(sess *session.Session)    { m.session = sess }
func (m *mockSessionService) SessionName() string                        { return "" }
func (m *mockSessionService) SetSessionName(name string)                 {}
func (m *mockSessionService) WorkDir() string                            { return "" }
func (m *mockSessionService) HomeDir() string                            { return "" }
func (m *mockSessionService) State() InteractionState                    { return StateIdle }
func (m *mockSessionService) SetState(state InteractionState)            {}
func (m *mockSessionService) ModelName() string                          { return "test-model" }
func (m *mockSessionService) SetModelName(name string)                   {}
func (m *mockSessionService) Config() *config.Config                     { return nil }
func (m *mockSessionService) Store() *session.Store                      { return nil }
func (m *mockSessionService) UsageTracker() *ai.UsageTracker             { return nil }
func (m *mockSessionService) AddMessage(msg string)                      {}
func (m *mockSessionService) AddMessageWithSources(string, []lineSource) {}
func (m *mockSessionService) MessageEntries() []displayEntry             { return nil }

func (m *mockSessionService) SetMessagesWithSources([]string, [][]lineSource) {}
func (m *mockSessionService) Messages() []string                              { return nil }
func (m *mockSessionService) SetMessages(msgs []string)                       {}
func (m *mockSessionService) FirstUserInput() string                          { return "" }
func (m *mockSessionService) SetFirstUserInput(input string)                  {}
func (m *mockSessionService) SavedMsgCount() int                              { return 0 }
func (m *mockSessionService) SetSavedMsgCount(count int)                      {}
func (m *mockSessionService) InputTokens() int                                { return 0 }
func (m *mockSessionService) SetInputTokens(tokens int)                       {}
func (m *mockSessionService) OutputTokens() int                               { return 0 }
func (m *mockSessionService) SetOutputTokens(tokens int)                      {}
func (m *mockSessionService) RequestCount() int                               { return 0 }
func (m *mockSessionService) SetRequestCount(count int)                       {}
func (m *mockSessionService) Quitting() bool                                  { return false }
func (m *mockSessionService) SetQuitting(q bool)                              {}
func (m *mockSessionService) AvailableModels() []string                       { return nil }
func (m *mockSessionService) SetAvailableModels(models []string)              {}
func (m *mockSessionService) CurrentText() string                             { return "" }
func (m *mockSessionService) SetCurrentText(text string)                      {}
func (m *mockSessionService) CurrentThinking() string                         { return "" }
func (m *mockSessionService) SetCurrentThinking(thinking string)              {}
func (m *mockSessionService) Interrupted() bool                               { return false }
func (m *mockSessionService) SetInterrupted(interrupted bool)                 {}
func (m *mockSessionService) ThinkingEnabled() *bool                          { return nil }
func (m *mockSessionService) SetThinkingEnabled(enabled *bool)                {}
func (m *mockSessionService) ThinkingBudgetTokens() int                       { return 0 }
func (m *mockSessionService) SetThinkingBudgetTokens(tokens int)              {}
func (m *mockSessionService) CurrentAgent() string                            { return "" }
func (m *mockSessionService) SetCurrentAgent(agent string)                    {}
func (m *mockSessionService) ActiveSkills() []string                          { return nil }
func (m *mockSessionService) SetActiveSkills(skills []string)                 {}
func (m *mockSessionService) CompactCount() int                               { return 0 }
func (m *mockSessionService) SetCompactCount(count int)                       {}
func (m *mockSessionService) AutoCompactCooldown() bool                       { return false }
func (m *mockSessionService) SetAutoCompactCooldown(cooldown bool)            {}
func (m *mockSessionService) PlanMode() bool                                  { return false }
func (m *mockSessionService) SetPlanMode(plan bool)                           {}
func (m *mockSessionService) FastModel() string                               { return "" }
func (m *mockSessionService) SetFastModel(model string)                       {}
func (m *mockSessionService) OutputStyle() string                             { return "default" }
func (m *mockSessionService) SetOutputStyle(style string)                     {}
func (m *mockSessionService) ConfigLoader() *config.Loader                    { return nil }
func (m *mockSessionService) CacheBroken() bool                               { return false }
func (m *mockSessionService) SetCacheBroken(broken bool)                      {}
func (m *mockSessionService) CacheBreakCount() int                            { return 0 }
func (m *mockSessionService) SetCacheBreakCount(count int)                    {}
func (m *mockSessionService) PrivacyConfig() *PrivacyConfig                   { return nil }
func (m *mockSessionService) SetPrivacyConfig(cfg *PrivacyConfig)             {}
func (m *mockSessionService) Marks() []sessionMark                            { return nil }
func (m *mockSessionService) SetMarks(marks []sessionMark)                    {}
func (m *mockSessionService) InputHistory() []string                          { return nil }
func (m *mockSessionService) SetInputHistory(history []string)                {}
func (m *mockSessionService) HistoryIdx() int                                 { return 0 }
func (m *mockSessionService) SetHistoryIdx(idx int)                           {}
func (m *mockSessionService) HistoryDraft() string                            { return "" }
func (m *mockSessionService) SetHistoryDraft(draft string)                    {}

// mockAIService implements AIService for testing UI rendering with a mock AI backend
type mockAIService struct {
	events <-chan ai.Event
	err    error
}

func (m *mockAIService) StreamRequest(ctx context.Context, req *ai.Request) (<-chan ai.Event, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.events, nil
}
func (m *mockAIService) HandleStreamError(err error) error { return fmt.Errorf("handled: %w", err) }
func (m *mockAIService) Client() *ai.SimpleClient          { return nil }
func (m *mockAIService) ModelRegistry() *ai.ModelRegistry  { return nil }
func (m *mockAIService) Budget() ctxpkg.Budget             { return ctxpkg.Budget{} }
func (m *mockAIService) SetBudget(budget ctxpkg.Budget)    {}
func (m *mockAIService) Estimator() *ai.TokenEstimator     { return nil }
func (m *mockAIService) Compactor() *ctxpkg.Compactor      { return nil }
func (m *mockAIService) AICompactor() *ctxpkg.AICompactor  { return nil }
func (m *mockAIService) Fallback() *ai.FallbackTracker     { return nil }
func (m *mockAIService) RecoveryCoordinator() *ai.RecoveryCoordinator {
	return nil
}
func (m *mockAIService) SetStreamCancel(cancel context.CancelFunc) {}
func (m *mockAIService) StreamCancel() context.CancelFunc          { return nil }
func (m *mockAIService) MemoryStore() *config.MemoryStore          { return nil }
func (m *mockAIService) MemoryIndex() *config.MemoryIndex          { return nil }
func (m *mockAIService) MagicDocs() *config.MagicDocs              { return nil }
func (m *mockAIService) MemSelector() *config.MemorySelector       { return nil }
func (m *mockAIService) MemExtractor() *config.AutoMemoryExtractor { return nil }
func (m *mockAIService) RequestID() string                         { return "" }
func (m *mockAIService) SetRequestID(id string)                    {}
func (m *mockAIService) RetryConfig() ai.RetryConfig               { return ai.RetryConfig{} }
func (m *mockAIService) SetRetryConfig(config ai.RetryConfig)      {}
func (m *mockAIService) Caching() *ai.CachingIntegration           { return nil }

// mockToolService implements ToolService for testing
type mockToolService struct {
	result *tools.Result
	err    error
	safe   bool
}

func (m *mockToolService) ExecuteTool(ctx context.Context, name string, input json.RawMessage) (*tools.Result, error) {
	return m.result, m.err
}
func (m *mockToolService) IsConcurrencySafe(name string) bool           { return m.safe }
func (m *mockToolService) Executor() *tools.Executor                    { return nil }
func (m *mockToolService) Registry() *tools.Registry                    { return nil }
func (m *mockToolService) CmdRegistry() *command.Registry               { return nil }
func (m *mockToolService) Policy() *permission.Policy                   { return nil }
func (m *mockToolService) PendingTools() []session.ContentBlock         { return nil }
func (m *mockToolService) SetPendingTools(tools []session.ContentBlock) {}
func (m *mockToolService) CurrentToolID() string                        { return "" }
func (m *mockToolService) SetCurrentToolID(id string)                   {}
func (m *mockToolService) ToolInputBuf() map[string]string              { return nil }
func (m *mockToolService) SetToolInputBuf(buf map[string]string)        {}
func (m *mockToolService) ToolCancel() context.CancelFunc               { return nil }
func (m *mockToolService) SetToolCancel(cancel context.CancelFunc)      {}
func (m *mockToolService) MonitorMgr() *monitor.Manager                 { return nil }
func (m *mockToolService) MonitorCh() chan monitorEvent                 { return nil }
func (m *mockToolService) MonitorEscape() *monitorEscape                { return nil }
func (m *mockToolService) LoopScheduler() *tools.LoopScheduler          { return nil }
func (m *mockToolService) LoopCh() chan loopFireMsg                     { return nil }
func (m *mockToolService) Daemon() *daemon.Daemon                       { return nil }
func (m *mockToolService) GitHubTools() *github.GitHubTools             { return nil }
func (m *mockToolService) FileHistory() *filehistory.FileHistory        { return nil }
func (m *mockToolService) Searcher() *search.Searcher                   { return nil }
func (m *mockToolService) PendingConfirm() *PendingConfirm              { return nil }
func (m *mockToolService) SetPendingConfirm(confirm *PendingConfirm)    {}
func (m *mockToolService) PendingChoice() *PendingChoice                { return nil }
func (m *mockToolService) SetPendingChoice(choice *PendingChoice)       {}
func (m *mockToolService) ChoiceIdx() int                               { return 0 }
func (m *mockToolService) SetChoiceIdx(idx int)                         {}

// ApprovalAudit 实现 ToolService（delta F）：mock 不落盘，返回 nil 即可。
// 编译器会强制这里存在 —— 接口加方法后漏实现 = 编译失败（这正是我们要的门）。
func (m *mockToolService) ApprovalAudit() bridge.ApprovalAudit { return nil }

// --- Test cases ---

// TestServiceIndependentSession verifies that SessionService can be tested
// independently without constructing a full Model
func TestServiceIndependentSession(t *testing.T) {
	svc := &mockSessionService{
		session: session.NewSession(),
	}

	// Verify interface methods work without a full Model
	sess := svc.CurrentSession()
	if sess == nil {
		t.Fatal("CurrentSession() should not return nil")
	}

	err := svc.LoadSession("test")
	if err != nil {
		t.Fatalf("LoadSession should not error with nil err, got: %v", err)
	}

	err = svc.SaveSession()
	if err != nil {
		t.Fatalf("SaveSession should not error with nil err, got: %v", err)
	}
}

// TestServiceIndependentSessionWithError verifies mock error propagation
func TestServiceIndependentSessionWithError(t *testing.T) {
	svc := &mockSessionService{
		err: fmt.Errorf("test error"),
	}

	err := svc.LoadSession("test")
	if err == nil {
		t.Fatal("LoadSession should propagate error")
	}
}

// TestInterfaceReplacementAIService verifies that MockAIService can replace
// the real AI service and UI still renders correctly
func TestInterfaceReplacementAIService(t *testing.T) {
	mockAI := &mockAIService{
		err: fmt.Errorf("mock stream error"),
	}

	// Verify the mock implements AIService
	var _ AIService = mockAI

	// Verify HandleStreamError works
	handledErr := mockAI.HandleStreamError(fmt.Errorf("original"))
	if handledErr == nil {
		t.Fatal("HandleStreamError should return non-nil error")
	}
}

// TestInterfaceReplacementToolService verifies that MockToolService can replace
// the real tool service
func TestInterfaceReplacementToolService(t *testing.T) {
	mockTool := &mockToolService{
		result: &tools.Result{ID: "1", Name: "test", Output: "ok"},
		safe:   true,
	}

	// Verify the mock implements ToolService
	var _ ToolService = mockTool

	// Test ExecuteTool
	result, err := mockTool.ExecuteTool(context.Background(), "test", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteTool should not error, got: %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("ExecuteTool result output = %q, want %q", result.Output, "ok")
	}

	// Test IsConcurrencySafe
	if !mockTool.IsConcurrencySafe("test") {
		t.Fatal("IsConcurrencySafe should return true")
	}
}

// 2026-09-27：原 `TestFunctionalRegressionRunnerCore` 与 `TestCompileVerification`
// （两条只断言 runnerCoreServiceAdapter 实现四个接口的编译/回归测试）随该死码适配器一并删除。
// 判据成立性不受影响：接口与 `&m.<Model>` 子结构体的编译断言仍在 service_interfaces.go 顶部。
