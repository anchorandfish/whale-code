// Package mock — test mocks for isolating external dependencies
// session_store.go — 会话存储 Mock
package mock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/anchorandfish/whale-code/internal/session"
)

// Verify MockSessionStore implements session.SessionStore at compile time
var _ session.SessionStore = (*MockSessionStore)(nil)

// MockSessionStore is a mock implementation of session.SessionStore for testing
type MockSessionStore struct {
	mu       sync.Mutex
	Sessions map[string]*session.Session
	versions map[string]uint64
	Err      error // 可注入错误
	lockMode session.LockMode
	dir      string

	// heldSession / SessionInUse 让测试可以注入「会话已被另一个进程占用」这一前提
	// （占用闸门）。SessionInUse 为真时 HoldSession 返回 *SessionInUseError。
	heldSession    string
	SessionInUse   bool
	SessionInUseID int // 占用者 PID（0 表示不可指名）
}

// NewMockSessionStore creates a mock session store
func NewMockSessionStore() *MockSessionStore {
	return &MockSessionStore{
		Sessions: make(map[string]*session.Session),
		versions: make(map[string]uint64),
		dir:      filepath.Join(os.TempDir(), "mock-sessions"), // 跨平台默认（Windows 无 /tmp）
	}
}

// Append appends a message to a session
func (m *MockSessionStore) Append(name string, msg session.Message) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[name]
	if !ok {
		s = session.NewSession()
		s.Metadata.Name = name
		m.Sessions[name] = s
	}
	s.AddMessage(msg)
	m.versions[name]++
	return nil
}

// AppendBatch batch appends messages to a session
func (m *MockSessionStore) AppendBatch(sessionID string, msgs []session.Message) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[sessionID]
	if !ok {
		s = session.NewSession()
		s.Metadata.Name = sessionID
		m.Sessions[sessionID] = s
	}
	for _, msg := range msgs {
		s.AddMessage(msg)
	}
	m.versions[sessionID]++
	return nil
}

// List returns all session summaries
func (m *MockSessionStore) List() ([]session.SessionSummary, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var summaries []session.SessionSummary
	for name, s := range m.Sessions {
		summaries = append(summaries, session.SessionSummary{
			ID:        name,
			Name:      s.Metadata.Name,
			TurnCount: s.Metadata.TurnCount,
			UpdatedAt: s.Metadata.UpdatedAt,
		})
	}
	return summaries, nil
}

// Load loads a session by ID
func (m *MockSessionStore) Load(id string) (*session.Session, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return s, nil
}

// LoadWithReport loads a session by ID. The mock does not perform crash
// recovery (there is no disk), so the report is always empty.
func (m *MockSessionStore) LoadWithReport(id string) (*session.Session, *session.LoadReport, error) {
	s, err := m.Load(id)
	if err != nil {
		return nil, nil, err
	}
	return s, &session.LoadReport{SessionID: id}, nil
}

// Compact removes dead-branch messages from a session (mock: no-op, returns 0).
func (m *MockSessionStore) Compact(sessionID string) (int, error) {
	if m.Err != nil {
		return 0, m.Err
	}
	return 0, nil
}

// Save saves a session
func (m *MockSessionStore) Save(id string, s *session.Session) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sessions[id] = s
	m.versions[id]++
	return nil
}

// TranscriptVersion returns the mock's monotonic content version.
func (m *MockSessionStore) TranscriptVersion(id string) (session.TranscriptVersion, error) {
	if m.Err != nil {
		return session.TranscriptVersion{}, m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Sessions[id]; !ok {
		return session.TranscriptVersion{}, nil
	}
	return session.TranscriptVersion{Exists: true, SHA256: fmt.Sprintf("mock-%d", m.versions[id])}, nil
}

// Rewrite checks both the mock transcript version and live session revision
// before installing the candidate.
func (m *MockSessionStore) Rewrite(id string, expected session.TranscriptVersion, live *session.Session, expectedRevision uint64, candidate *session.Session) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.Sessions[id]
	current := session.TranscriptVersion{}
	if exists {
		current = session.TranscriptVersion{Exists: true, SHA256: fmt.Sprintf("mock-%d", m.versions[id])}
	}
	if current != expected {
		return fmt.Errorf("%w: mock transcript version changed", session.ErrSessionRewriteConflict)
	}
	messages, metadata, _, _ := candidate.Snapshot()
	if !live.ReplaceIfRevision(expectedRevision, messages, metadata) {
		return fmt.Errorf("%w: mock in-memory revision changed", session.ErrSessionRewriteConflict)
	}
	m.Sessions[id] = live
	m.versions[id]++
	return nil
}

// Delete deletes a session
func (m *MockSessionStore) Delete(id string) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Sessions, id)
	delete(m.versions, id)
	return nil
}

// Rename renames a session
func (m *MockSessionStore) Rename(oldID, newID string) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[oldID]
	if !ok {
		return fmt.Errorf("session %s not found", oldID)
	}
	delete(m.Sessions, oldID)
	m.Sessions[newID] = s
	m.versions[newID] = m.versions[oldID] + 1
	delete(m.versions, oldID)
	return nil
}

// Exists checks if a session exists
func (m *MockSessionStore) Exists(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.Sessions[sessionID]
	return ok
}

// ForkSession forks a session with a new ID
func (m *MockSessionStore) ForkSession(origID string) (string, error) {
	if m.Err != nil {
		return "", m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[origID]
	if !ok {
		return "", fmt.Errorf("session %s not found", origID)
	}
	newID := session.GenerateID()
	m.Sessions[newID] = s
	m.versions[newID] = 1
	return newID, nil
}

// ForkSessionByID forks a session by specified ID
func (m *MockSessionStore) ForkSessionByID(sessionID string) (string, error) {
	return m.ForkSession(sessionID)
}

// SearchByTag searches sessions by tag (returns empty in mock)
func (m *MockSessionStore) SearchByTag(tag string) ([]session.SessionSummary, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return []session.SessionSummary{}, nil
}

// Import imports a session (batch write messages)
func (m *MockSessionStore) Import(sessionID string, messages []session.Message) error {
	if m.Err != nil {
		return m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := session.NewSession()
	s.Metadata.Name = sessionID
	for _, msg := range messages {
		s.AddMessage(msg)
	}
	m.Sessions[sessionID] = s
	m.versions[sessionID]++
	return nil
}

// Dir returns the mock storage directory path
func (m *MockSessionStore) Dir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dir
}

// SetDir 设置 mock 存储目录路径（测试用，建议传入 t.TempDir()）
func (m *MockSessionStore) SetDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dir = dir
}

// SetLockMode sets the lock mode (no-op in mock)
func (m *MockSessionStore) SetLockMode(mode session.LockMode) {
	m.lockMode = mode
}

// AcquireLock acquires a session file lock (always succeeds in mock)
func (m *MockSessionStore) AcquireLock(sessionID string) (*session.SessionLock, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return &session.SessionLock{}, nil
}

// IsLocked checks if the session is locked (always returns false in mock)
func (m *MockSessionStore) IsLocked(sessionName string) (bool, int, error) {
	return false, 0, nil
}

// HoldSession 长期持有会话占用锁（mock：无跨进程语义，见 SessionInUse 注入位）。
func (m *MockSessionStore) HoldSession(sessionID string) error {
	if m.Err != nil {
		return m.Err
	}
	if m.SessionInUse {
		return &session.SessionInUseError{SessionID: sessionID, PID: m.SessionInUseID}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heldSession = sessionID
	return nil
}

// ReleaseHeldSession 释放 mock 的占用状态。
func (m *MockSessionStore) ReleaseHeldSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heldSession = ""
}
