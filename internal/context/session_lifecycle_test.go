// Package context — tests for SessionContextManager and
// SessionLifecycleHandler (C8 / C20).
package context

import (
	"sync"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestSessionContextManager_ClearSessionCaches verifies that
// ClearSessionCaches resets all component caches.
func TestSessionContextManager_ClearSessionCaches(t *testing.T) {
	est := newTestEstimator(t)
	pipeline := NewCompactionPipeline(est, nil)
	reactive := NewReactiveCompactor(pipeline, nil)
	cacheBreak := NewCacheBreakHandler(nil, nil)
	collapseMgr := newTestCollapseManager(t)

	mgr := NewSessionContextManager(collapseMgr, cacheBreak, reactive)

	// Set some state.
	reactive.hasAttemptedReactiveCompact = true
	cacheBreak.recentBreaks = append(cacheBreak.recentBreaks, bus.CacheBreakEvent{CacheReadDrop: 0.5})

	// Clear.
	mgr.ClearSessionCaches()

	// Verify state was cleared.
	if reactive.HasAttempted() {
		t.Error("reactive.HasAttempted should be false after ClearSessionCaches")
	}
	if len(cacheBreak.RecentBreaks()) != 0 {
		t.Errorf("cacheBreak.RecentBreaks should be empty, got %d", len(cacheBreak.RecentBreaks()))
	}
}

// TestSessionContextManager_ReloadFromSession verifies that
// ReloadFromSession loads messages from the store and updates internal state.
func TestSessionContextManager_ReloadFromSession(t *testing.T) {
	store := newTestStore(t)
	sess := session.NewSession()
	sess.AddMessage(session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "test message"}},
	})
	if err := store.Save("test-session", sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	mgr := NewSessionContextManager(nil, nil, nil)
	msgs, err := mgr.ReloadFromSession(store, "test-session")
	if err != nil {
		t.Fatalf("ReloadFromSession failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if mgr.CurrentSessionID() != "test-session" {
		t.Errorf("CurrentSessionID = %q, want %q", mgr.CurrentSessionID(), "test-session")
	}
	current := mgr.CurrentMessages()
	if len(current) != 1 {
		t.Errorf("CurrentMessages len = %d, want 1", len(current))
	}
}

// TestSessionContextManager_ReloadFromSession_NilStore verifies error
// handling for nil store.
func TestSessionContextManager_ReloadFromSession_NilStore(t *testing.T) {
	mgr := NewSessionContextManager(nil, nil, nil)
	_, err := mgr.ReloadFromSession(nil, "x")
	if err == nil {
		t.Error("expected error for nil store")
	}
}

// TestSessionContextManager_ReloadFromSession_EmptyID verifies error
// handling for empty session ID.
func TestSessionContextManager_ReloadFromSession_EmptyID(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSessionContextManager(nil, nil, nil)
	_, err := mgr.ReloadFromSession(store, "")
	if err == nil {
		t.Error("expected error for empty session ID")
	}
}

// TestSessionContextManager_ReloadFromSession_NotFound verifies error
// handling for a non-existent session.
func TestSessionContextManager_ReloadFromSession_NotFound(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSessionContextManager(nil, nil, nil)
	_, err := mgr.ReloadFromSession(store, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

// TestSessionLifecycleHandler_ForkEvent verifies that a SessionForkEvent
// triggers ClearSessionCaches.
func TestSessionLifecycleHandler_ForkEvent(t *testing.T) {
	b := bus.NewBus()
	est := newTestEstimator(t)
	pipeline := NewCompactionPipeline(est, nil)
	reactive := NewReactiveCompactor(pipeline, nil)
	mgr := NewSessionContextManager(nil, nil, reactive)

	h := NewSessionLifecycleHandler(b, nil, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// Set state that ClearSessionCaches should reset.
	reactive.hasAttemptedReactiveCompact = true

	// Publish fork event.
	b.Publish(bus.SessionForkEvent{
		ParentSessionID: "parent",
		ForkSessionID:   "fork",
		OccurredAt:      time.Now(),
	})

	// Wait for processing.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && reactive.HasAttempted() {
		time.Sleep(5 * time.Millisecond)
	}

	if reactive.HasAttempted() {
		t.Error("HasAttempted should be false after fork event (ClearSessionCaches)")
	}
}

// TestSessionLifecycleHandler_SwitchEvent verifies that a
// SessionSwitchEvent triggers ClearSessionCaches + ReloadFromSession.
func TestSessionLifecycleHandler_SwitchEvent(t *testing.T) {
	b := bus.NewBus()
	store := newTestStore(t)

	// Create a session to switch to.
	sess := session.NewSession()
	sess.AddMessage(session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "switched"}},
	})
	if err := store.Save("new-session", sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	est := newTestEstimator(t)
	pipeline := NewCompactionPipeline(est, nil)
	reactive := NewReactiveCompactor(pipeline, nil)
	mgr := NewSessionContextManager(nil, nil, reactive)

	h := NewSessionLifecycleHandler(b, store, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	reactive.hasAttemptedReactiveCompact = true

	// Publish switch event.
	b.Publish(bus.SessionSwitchEvent{
		OldSessionID: "old",
		NewSessionID: "new-session",
		OccurredAt:   time.Now(),
	})

	// Wait for processing.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.CurrentSessionID() == "new-session" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if mgr.CurrentSessionID() != "new-session" {
		t.Errorf("CurrentSessionID = %q, want %q", mgr.CurrentSessionID(), "new-session")
	}
	if reactive.HasAttempted() {
		t.Error("HasAttempted should be false after switch event")
	}
	msgs := mgr.CurrentMessages()
	if len(msgs) != 1 {
		t.Errorf("CurrentMessages len = %d, want 1", len(msgs))
	}
}

// TestSessionLifecycleHandler_EndToEnd_ForkEvent verifies the full C20
// wiring: store.ForkSession → publishFork → bus.Publish(SessionForkEvent)
// → SessionLifecycleHandler.handleEvent → ClearSessionCaches.
//
// 与 TestSessionLifecycleHandler_ForkEvent 的区别：前者直接调
// b.Publish(...)，跳过 Session 层；本测试通过 store.ForkSession()
// 触发真实事件流，覆盖 store.SetBus 注入 + publishFork 发布 +
// Context 订阅的全链路。若 store 未注入 bus（SetBus 未调用）或
// publishFork 被删除，本测试将失败。
func TestSessionLifecycleHandler_EndToEnd_ForkEvent(t *testing.T) {
	b := bus.NewBus()

	// 注入 bus 到 Store —— 这是 C20 端到端链路的关键。
	store := newTestStore(t)
	store.SetBus(b)

	// 准备原始会话。
	sess := session.NewSession()
	sess.AddMessage(session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "fork source"}},
	})
	const origID = "fork-source"
	if err := store.Save(origID, sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	est := newTestEstimator(t)
	pipeline := NewCompactionPipeline(est, nil)
	reactive := NewReactiveCompactor(pipeline, nil)
	mgr := NewSessionContextManager(nil, nil, reactive)

	h := NewSessionLifecycleHandler(b, store, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// 预置状态：HasAttempted = true，事件处理后应被清空。
	reactive.hasAttemptedReactiveCompact = true

	// 通过真实生产路径触发 fork 事件（不直接调 b.Publish）。
	newID, err := store.ForkSession(origID)
	if err != nil {
		t.Fatalf("ForkSession failed: %v", err)
	}
	if newID == "" || newID == origID {
		t.Fatalf("ForkSession returned invalid newID %q", newID)
	}

	// 等待 Context 处理事件（ClearSessionCaches 把 hasAttempted 清零）。
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && reactive.HasAttempted() {
		time.Sleep(5 * time.Millisecond)
	}

	if reactive.HasAttempted() {
		t.Error("HasAttempted should be false after ForkSession (C20 end-to-end)")
	}
}

// TestSessionLifecycleHandler_EndToEnd_SwitchEvent verifies the full C20
// wiring: store.NotifySessionSwitch → bus.Publish(SessionSwitchEvent)
// → SessionLifecycleHandler.handleEvent → ClearSessionCaches +
// ReloadFromSession. 覆盖 store.SetBus 注入 + NotifySessionSwitch
// 发布 + Context 订阅与 reload 的全链路。
func TestSessionLifecycleHandler_EndToEnd_SwitchEvent(t *testing.T) {
	b := bus.NewBus()

	store := newTestStore(t)
	store.SetBus(b)

	// 准备切换目标会话（带一条消息，验证 ReloadFromSession 生效）。
	sess := session.NewSession()
	sess.AddMessage(session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "switched-to"}},
	})
	const newID = "switch-target"
	if err := store.Save(newID, sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	est := newTestEstimator(t)
	pipeline := NewCompactionPipeline(est, nil)
	reactive := NewReactiveCompactor(pipeline, nil)
	mgr := NewSessionContextManager(nil, nil, reactive)

	h := NewSessionLifecycleHandler(b, store, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// 预置状态：HasAttempted = true，切换后应被清空。
	reactive.hasAttemptedReactiveCompact = true

	// 通过真实生产路径触发 switch 事件（不直接调 b.Publish）。
	store.NotifySessionSwitch("old-session", newID)

	// 等待 Context 处理事件（ClearSessionCaches + ReloadFromSession）。
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.CurrentSessionID() == newID {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if mgr.CurrentSessionID() != newID {
		t.Errorf("CurrentSessionID = %q, want %q (C20 end-to-end switch)", mgr.CurrentSessionID(), newID)
	}
	if reactive.HasAttempted() {
		t.Error("HasAttempted should be false after NotifySessionSwitch (C20 end-to-end)")
	}
	msgs := mgr.CurrentMessages()
	if len(msgs) != 1 {
		t.Errorf("CurrentMessages len = %d, want 1 (ReloadFromSession should have loaded switched session)", len(msgs))
	}
}

// TestSessionLifecycleHandler_EndToEnd_NoBusIsolation is the anti-example
// (反例测试): 当 Store 未注入 bus 时，ForkSession 不应触发 Context 清缓存。
// 证明清缓存确实由 bus 事件驱动（而非其他副作用），满足宪法 §6.2 第 2 条
// 反例测试先行要求。
func TestSessionLifecycleHandler_EndToEnd_NoBusIsolation(t *testing.T) {
	b := bus.NewBus()

	// 注意：这里故意不调用 store.SetBus(b)，模拟 bus 未注入的场景。
	store := newTestStore(t)

	sess := session.NewSession()
	sess.AddMessage(session.Message{
		Role:   session.RoleUser,
		Blocks: []session.ContentBlock{{Type: "text", Text: "no-bus"}},
	})
	const origID = "no-bus-src"
	if err := store.Save(origID, sess); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	est := newTestEstimator(t)
	pipeline := NewCompactionPipeline(est, nil)
	reactive := NewReactiveCompactor(pipeline, nil)
	mgr := NewSessionContextManager(nil, nil, reactive)

	// Handler 仍订阅 bus（即使 store 未注入同一个 bus）。
	h := NewSessionLifecycleHandler(b, store, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	reactive.hasAttemptedReactiveCompact = true

	// 调用 ForkSession —— 因 store 未注入 bus，publishFork 应静默跳过。
	if _, err := store.ForkSession(origID); err != nil {
		t.Fatalf("ForkSession failed: %v", err)
	}

	// 等待足够时间确认事件不会到达（应保持 true）。
	time.Sleep(150 * time.Millisecond)

	if !reactive.HasAttempted() {
		t.Error("HasAttempted should remain true when store has no bus injected (proves bus drives the clearing)")
	}
}

// TestSessionLifecycleHandler_StartErrors verifies error handling for
// nil bus and nil manager.
func TestSessionLifecycleHandler_StartErrors(t *testing.T) {
	// Nil bus.
	h1 := NewSessionLifecycleHandler(nil, nil, NewSessionContextManager(nil, nil, nil))
	if err := h1.Start(); err == nil {
		t.Error("Start with nil bus should return error")
	}

	// Nil manager.
	b := bus.NewBus()
	h2 := NewSessionLifecycleHandler(b, nil, nil)
	if err := h2.Start(); err == nil {
		t.Error("Start with nil manager should return error")
	}
}

// TestSessionLifecycleHandler_DoubleStart verifies that Start returns
// an error when called twice.
func TestSessionLifecycleHandler_DoubleStart(t *testing.T) {
	b := bus.NewBus()
	mgr := NewSessionContextManager(nil, nil, nil)
	h := NewSessionLifecycleHandler(b, nil, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	defer h.Stop()
	if err := h.Start(); err == nil {
		t.Error("second Start should return error")
	}
}

// TestSessionLifecycleHandler_StopIsIdempotent verifies that Stop can be
// called multiple times without panicking.
func TestSessionLifecycleHandler_StopIsIdempotent(t *testing.T) {
	b := bus.NewBus()
	mgr := NewSessionContextManager(nil, nil, nil)
	h := NewSessionLifecycleHandler(b, nil, mgr)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	h.Stop()
	h.Stop()
}

// --- test helpers ---

// mockCacheClearer 记录 Clear() 调用次数的 cacheClearer 测试替身。
type mockCacheClearer struct {
	mu    sync.Mutex
	count int
}

// Clear 实现 cacheClearer 接口，递增内部计数。
func (m *mockCacheClearer) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count++
}

// Count 返回 Clear() 被调用的次数（线程安全）。
func (m *mockCacheClearer) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// TestSessionLifecycleHandler_ForkEvent_ClearsExternalCaches 验证 fork 事件
// 触发已注入的 cacheClearer.Clear()（B0 §3.1 第3项）。
func TestSessionLifecycleHandler_ForkEvent_ClearsExternalCaches(t *testing.T) {
	b := bus.NewBus()
	mgr := NewSessionContextManager(nil, nil, nil)
	mock := &mockCacheClearer{}

	h := NewSessionLifecycleHandler(b, nil, mgr)
	h.SetCacheClearer(mock)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// 发布 fork 事件，应触发 ClearSessionCaches + clearExternalCaches。
	b.Publish(bus.SessionForkEvent{
		ParentSessionID: "parent",
		ForkSessionID:   "fork",
		OccurredAt:      time.Now(),
	})

	// 轮询等待 Clear() 被调用。
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && mock.Count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if mock.Count() == 0 {
		t.Error("cacheClearer.Clear() 应在 fork 事件后被调用")
	}
}

// TestSessionLifecycleHandler_SwitchEvent_ClearsExternalCaches 验证 switch 事件
// 触发已注入的 cacheClearer.Clear()（B0 §3.1 第3项）。
// store 传 nil：ReloadFromSession 被跳过，但 clearExternalCaches 仍应执行。
func TestSessionLifecycleHandler_SwitchEvent_ClearsExternalCaches(t *testing.T) {
	b := bus.NewBus()
	mgr := NewSessionContextManager(nil, nil, nil)
	mock := &mockCacheClearer{}

	h := NewSessionLifecycleHandler(b, nil, mgr)
	h.SetCacheClearer(mock)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	// 发布 switch 事件，应触发 ClearSessionCaches + clearExternalCaches。
	b.Publish(bus.SessionSwitchEvent{
		OldSessionID: "old",
		NewSessionID: "new",
		OccurredAt:   time.Now(),
	})

	// 轮询等待 Clear() 被调用。
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && mock.Count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if mock.Count() == 0 {
		t.Error("cacheClearer.Clear() 应在 switch 事件后被调用")
	}
}

// TestSessionLifecycleHandler_MultipleCacheClearers 验证注入多个 clearer 时
// 单次事件全部被调用（覆盖 SetCacheClearer 变参 append 语义）。
func TestSessionLifecycleHandler_MultipleCacheClearers(t *testing.T) {
	b := bus.NewBus()
	mgr := NewSessionContextManager(nil, nil, nil)
	mock1 := &mockCacheClearer{}
	mock2 := &mockCacheClearer{}

	h := NewSessionLifecycleHandler(b, nil, mgr)
	h.SetCacheClearer(mock1, mock2)
	if err := h.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer h.Stop()

	b.Publish(bus.SessionForkEvent{
		ParentSessionID: "p",
		ForkSessionID:   "f",
		OccurredAt:      time.Now(),
	})

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && (mock1.Count() == 0 || mock2.Count() == 0) {
		time.Sleep(5 * time.Millisecond)
	}
	if mock1.Count() == 0 {
		t.Error("mock1.Clear() 应被调用")
	}
	if mock2.Count() == 0 {
		t.Error("mock2.Clear() 应被调用")
	}
}

// newTestStore creates a temporary session store for testing.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := session.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return store
}

// newTestEstimator creates a token estimator for testing.
func newTestEstimator(t *testing.T) *ai.TokenEstimator {
	t.Helper()
	return ai.NewTokenEstimator()
}

// newTestCollapseManager creates a collapse manager for testing.
func newTestCollapseManager(t *testing.T) *ContextCollapseManager {
	t.Helper()
	est := ai.NewTokenEstimator()
	return NewContextCollapseManager(DefaultCollapseConfig(), est, Budget{ContextWindow: 200000})
}

// Ensure time import is used.
var _ = time.Now
