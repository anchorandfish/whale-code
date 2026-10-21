// Package context — session lifecycle management (C8 / C20 contracts).
//
// This file implements the Context module's response to session lifecycle
// events (fork, switch) and the ReloadFromSession data-flow contract.
//
// C8 contract: Session is the source of truth; Context holds an in-memory
// copy. After Compact/Fork/Switch, Context must reload from Session to stay
// in sync. ReloadFromSession(sessionID) loads messages from the session
// store and refreshes all Context-internal caches.
//
// C20 contract: Session does NOT directly call Context. Instead, Session
// publishes bus.SessionForkEvent / bus.SessionSwitchEvent, and Context
// subscribes via SessionLifecycleHandler to auto-trigger
// ClearSessionCaches + ReloadFromSession.
package context

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/session"
)

// SessionContextManager coordinates the Context module's session-dependent
// state: collapse commit log, cache break history, reactive compactor flag,
// and the in-memory message copy (C8 / C20).
//
// It provides:
//   - ClearSessionCaches: resets all session-scoped caches (call on fork/switch).
//   - ReloadFromSession: loads messages from the session store and updates
//     internal state (call after compact/fork/switch).
//
// Thread-safe: all methods are safe for concurrent use.
type SessionContextManager struct {
	mu sync.Mutex

	// collapseMgr: reset its commit log on ClearSessionCaches.
	collapseMgr *ContextCollapseManager
	// cacheBreakHandler: clear its break history on ClearSessionCaches.
	cacheBreakHandler *CacheBreakHandler
	// reactiveCompactor: reset its hasAttempted flag on ClearSessionCaches.
	reactiveCompactor *ReactiveCompactor

	// currentMessages: the in-memory copy of the session's messages (C8).
	// Updated by ReloadFromSession; read by CurrentMessages.
	currentMessages []session.Message
	// currentSessionID: the session ID of the last successful reload.
	currentSessionID string
}

// NewSessionContextManager creates a new SessionContextManager.
// Any of the component arguments may be nil; ClearSessionCaches will skip
// nil components gracefully.
func NewSessionContextManager(
	collapseMgr *ContextCollapseManager,
	cacheBreakHandler *CacheBreakHandler,
	reactiveCompactor *ReactiveCompactor,
) *SessionContextManager {
	return &SessionContextManager{
		collapseMgr:       collapseMgr,
		cacheBreakHandler: cacheBreakHandler,
		reactiveCompactor: reactiveCompactor,
	}
}

// ClearSessionCaches resets all session-scoped caches in the Context module
// (C20). Should be called when the session forks or switches to prevent
// stale cache entries from contaminating the new session.
//
// Resets:
//   - ContextCollapseManager: ResetAfterCompact (clears commit log).
//   - CacheBreakHandler: ClearHistory (discards break events).
//   - ReactiveCompactor: Reset (clears hasAttemptedReactiveCompact).
//   - Internal message copy: cleared (caller should follow with
//     ReloadFromSession to repopulate).
//
// Thread-safe.
func (m *SessionContextManager) ClearSessionCaches() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.collapseMgr != nil {
		m.collapseMgr.ResetAfterCompact()
	}
	if m.cacheBreakHandler != nil {
		m.cacheBreakHandler.ClearHistory()
	}
	if m.reactiveCompactor != nil {
		m.reactiveCompactor.Reset()
	}
	m.currentMessages = nil
	m.currentSessionID = ""
}

// ReloadFromSession loads messages from the session store for the given
// sessionID and updates the Context module's in-memory copy (C8).
//
// Per C8: Session is the source of truth; Context holds an in-memory copy.
// After Compact/Fork/Switch, call ReloadFromSession to resync.
//
// Parameters:
//   - store: the session store to load from (must not be nil).
//   - sessionID: the session ID to load (must not be empty).
//
// Returns the loaded messages and nil on success, or nil and an error if
// the store is nil, the sessionID is empty, or the store returns an error.
//
// Thread-safe. Does NOT call ClearSessionCaches (caller decides whether to
// clear before reloading — typically ClearSessionCaches is called first,
// then ReloadFromSession).
func (m *SessionContextManager) ReloadFromSession(store *session.Store, sessionID string) ([]session.Message, error) {
	if store == nil {
		return nil, fmt.Errorf("ReloadFromSession: store is nil")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("ReloadFromSession: sessionID is empty")
	}

	sess, err := store.Load(sessionID)
	if err != nil {
		return nil, fmt.Errorf("ReloadFromSession: failed to load session %q: %w", sessionID, err)
	}
	if sess == nil {
		return nil, fmt.Errorf("ReloadFromSession: session %q not found", sessionID)
	}

	msgs := sess.GetMessages()

	m.mu.Lock()
	m.currentMessages = msgs
	m.currentSessionID = sessionID
	m.mu.Unlock()

	return msgs, nil
}

// CurrentMessages returns a copy of the in-memory message list from the
// last ReloadFromSession call (C8). Returns nil if no session has been
// loaded. Thread-safe.
func (m *SessionContextManager) CurrentMessages() []session.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]session.Message, len(m.currentMessages))
	copy(out, m.currentMessages)
	return out
}

// CurrentSessionID returns the session ID of the last successful
// ReloadFromSession call. Returns "" if no session has been loaded.
// Thread-safe.
func (m *SessionContextManager) CurrentSessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentSessionID
}

// SetCollapseManager allows late binding of the collapse manager.
// Thread-safe.
func (m *SessionContextManager) SetCollapseManager(mgr *ContextCollapseManager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collapseMgr = mgr
}

// SetCacheBreakHandler allows late binding of the cache break handler.
// Thread-safe.
func (m *SessionContextManager) SetCacheBreakHandler(h *CacheBreakHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheBreakHandler = h
}

// SetReactiveCompactor allows late binding of the reactive compactor.
// Thread-safe.
func (m *SessionContextManager) SetReactiveCompactor(r *ReactiveCompactor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reactiveCompactor = r
}

// cacheClearer 会话切换时需清理缓存的外部组件（如 approval.Manager）。
// 采用接口解耦，避免 context 包反向依赖 approval 包（B0 §3.1 第3项）。
// 任何带 Clear() 方法的类型均可注入，由 SessionLifecycleHandler 在
// fork/switch 事件时统一回调。
type cacheClearer interface {
	Clear()
}

// SessionLifecycleHandler subscribes to bus.SessionForkEvent and
// bus.SessionSwitchEvent and triggers ClearSessionCaches + ReloadFromSession
// automatically (C20).
//
// Per C20: Session does NOT directly call Context. Session publishes events;
// this handler subscribes and reacts.
//
//   - SessionForkEvent → ClearSessionCaches (fork creates a new session
//     branch; stale caches must be cleared).
//   - SessionSwitchEvent → ClearSessionCaches + ReloadFromSession(newID)
//     (switching to a different session requires full cache reset + reload).
//
// Thread-safe: Start/Stop may be called concurrently. The event loop runs
// in a single goroutine.
type SessionLifecycleHandler struct {
	bus     *bus.Bus
	store   *session.Store
	manager *SessionContextManager

	// cacheClearers 会话切换时需清空缓存的外部组件列表（B0 §3.1 第3项）。
	// 由调用方通过 SetCacheClearer 注入，handleEvent 在 fork/switch 时遍历调用 Clear()。
	cacheClearers []cacheClearer

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	eventCh <-chan bus.Event
}

// NewSessionLifecycleHandler creates a new SessionLifecycleHandler.
//
// bus: the event bus to subscribe to (must not be nil for Start).
// store: the session store for ReloadFromSession (must not be nil for
//
//	SessionSwitchEvent handling; may be nil if only fork events are
//	expected, but ReloadFromSession will fail on switch events).
//
// manager: the SessionContextManager to call ClearSessionCaches /
//
//	ReloadFromSession on (must not be nil for Start).
func NewSessionLifecycleHandler(
	bus *bus.Bus,
	store *session.Store,
	manager *SessionContextManager,
) *SessionLifecycleHandler {
	return &SessionLifecycleHandler{
		bus:     bus,
		store:   store,
		manager: manager,
	}
}

// SetCacheClearer 注入会话切换时需清空缓存的外部组件（B0 §3.1 第3项）。
// 可接收多个 clearer，依次 append 到内部切片；handleEvent 在 fork/switch
// 事件时遍历调用其 Clear()。应在 Start 之前调用（与 manager 注入同期）。
// 线程安全。
func (h *SessionLifecycleHandler) SetCacheClearer(clearers ...cacheClearer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cacheClearers = append(h.cacheClearers, clearers...)
}

// Start subscribes to the bus and begins processing session lifecycle
// events in a goroutine. Returns an error if already started, or if the
// bus or manager is nil.
func (h *SessionLifecycleHandler) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return fmt.Errorf("SessionLifecycleHandler already started")
	}
	if h.bus == nil {
		return fmt.Errorf("bus is nil")
	}
	if h.manager == nil {
		return fmt.Errorf("manager is nil")
	}
	h.eventCh = h.bus.Subscribe(32)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.started = true
	h.wg.Add(1)
	go h.loop(ctx)
	return nil
}

// Stop signals the event loop goroutine to exit and waits for it to drain.
// Safe to call multiple times.
//
// 修复 P0-15：在 loop 退出后调用 bus.Unsubscribe 取消订阅并关闭 channel，
// 从 bus.subscribers 切片中移除引用，避免长生命周期进程的内存泄漏。
func (h *SessionLifecycleHandler) Stop() {
	h.mu.Lock()
	if !h.started {
		h.mu.Unlock()
		return
	}
	h.started = false
	cancel := h.cancel
	// 修复 P0-15：保存 eventCh 引用，用于 Unsubscribe（必须在 mu 保护下读取）
	eventCh := h.eventCh
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
	// 修复 P0-15：loop 已退出，安全地取消订阅并关闭 channel
	if h.bus != nil && eventCh != nil {
		h.bus.Unsubscribe(eventCh)
	}
}

// loop runs the event processing loop until ctx is cancelled or the
// subscription channel is closed.
func (h *SessionLifecycleHandler) loop(ctx context.Context) {
	defer h.wg.Done()
	ch := h.eventCh
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			h.handleEvent(evt)
		}
	}
}

// handleEvent dispatches a bus event to the appropriate handler.
func (h *SessionLifecycleHandler) handleEvent(evt bus.Event) {
	switch e := evt.(type) {
	case bus.SessionForkEvent:
		// C20: fork → clear caches (the forked session starts fresh).
		h.manager.ClearSessionCaches()
		// B0 §3.1 第3项：清空外部组件缓存（如 approval.Manager），与 ClearSessionCaches 并列。
		h.clearExternalCaches()
	case bus.SessionSwitchEvent:
		// C20 + C8: switch → clear caches + reload from new session.
		h.manager.ClearSessionCaches()
		if h.store != nil && e.NewSessionID != "" {
			// Best-effort reload; errors are non-fatal (the TUI layer
			// will retry or show an error to the user).
			_, _ = h.manager.ReloadFromSession(h.store, e.NewSessionID)
		}
		// B0 §3.1 第3项：清空外部组件缓存（如 approval.Manager），与 ClearSessionCaches 并列。
		h.clearExternalCaches()
	}
}

// clearExternalCaches 遍历已注入的 cacheClearers 调用 Clear()，清空外部
// 组件缓存（如 approval.Manager 的审批决策缓存）。避免新会话复用旧会话的
// 审批决策。在 mu 锁下快照切片后再回调，防止 SetCacheClearer 并发修改切片。
func (h *SessionLifecycleHandler) clearExternalCaches() {
	h.mu.Lock()
	clearers := make([]cacheClearer, len(h.cacheClearers))
	copy(clearers, h.cacheClearers)
	h.mu.Unlock()
	for _, c := range clearers {
		if c != nil {
			c.Clear()
		}
	}
}

// LastSwitchAge returns the time since the last SessionSwitchEvent was
// processed. Currently always returns a large duration (no tracking); this
// is a placeholder for future cache-expiry logic.
func (h *SessionLifecycleHandler) LastSwitchAge() time.Duration {
	return time.Hour
}
