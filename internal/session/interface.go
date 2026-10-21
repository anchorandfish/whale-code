// Package session — SessionStore interface for dependency injection and testing
package session

// SessionReader 只读会话操作接口（候选 A，ISP 接口隔离）。
// 调用方只需读取会话时依赖此接口，降低 mock 成本。
// 对标 Go io.Reader 小接口设计（@https://pkg.go.dev/io @2026-07-21）。
type SessionReader interface {
	// Load loads a complete session
	Load(sessionID string) (*Session, error)
	// LoadWithReport loads a session and performs S3 crash recovery (.tmp
	// leftover promotion). The returned LoadReport describes any recovery
	// actions or corrupt lines so callers (e.g. TUI) can surface them.
	LoadWithReport(sessionID string) (*Session, *LoadReport, error)
	// List lists all session summaries
	List() ([]SessionSummary, error)
	// Exists checks if a session exists
	Exists(sessionID string) bool
	// Dir returns the storage directory path
	Dir() string
}

// SessionWriter 会话写操作接口（候选 A，ISP 接口隔离）。
// 调用方只需写入/修改会话时依赖此接口。
type SessionWriter interface {
	// Append appends a message to a session file
	Append(sessionID string, msg Message) error
	// AppendBatch batch appends messages (atomic: all-or-nothing)
	AppendBatch(sessionID string, msgs []Message) error
	// Save saves a session using atomic write
	Save(sessionID string, session *Session) error
	// TranscriptVersion returns the full-file hash used by Rewrite CAS.
	TranscriptVersion(sessionID string) (TranscriptVersion, error)
	// Rewrite atomically replaces a transcript if both disk and memory still
	// match the compaction snapshot.
	Rewrite(sessionID string, expected TranscriptVersion, live *Session, expectedRevision uint64, candidate *Session) error
	// Delete deletes a session
	Delete(sessionID string) error
	// Rename renames a session
	Rename(oldID, newID string) error
	// Import imports a session (batch write messages)
	Import(sessionID string, messages []Message) error
	// Compact rewrites the session file keeping only the active message chain
	// (C8 contract: called by Context after inserting compact_boundary).
	// Returns the number of messages removed.
	Compact(sessionID string) (int, error)
	// ForkSession forks a session with a new ID
	ForkSession(origID string) (string, error)
	// ForkSessionByID forks a session by specified ID
	ForkSessionByID(sessionID string) (string, error)
}

// SessionLocker 会话锁操作接口（候选 A，ISP 接口隔离）。
// 调用方只需会话锁管理时依赖此接口。
type SessionLocker interface {
	// SetLockMode sets the lock mode
	SetLockMode(mode LockMode)
	// AcquireLock acquires a session file lock
	AcquireLock(sessionID string) (*SessionLock, error)
	// IsLocked checks if the session is locked by another process
	IsLocked(sessionName string) (bool, int, error)
	// HoldSession 长期持有会话占用锁（打开会话即占用，直到进程退出）。
	//
	// 这是「会话被打开」这件事在系统里的唯一承载体。已被另一个存活进程
	// 占用时返回 *SessionInUseError（调用方应拒绝进入并给出出路）。
	HoldSession(sessionID string) error
	// ReleaseHeldSession 释放本进程长期持有的会话占用锁（切会话 / 开新会话时调用）。
	ReleaseHeldSession()
}

// SessionSearcher 会话搜索操作接口（候选 A，ISP 接口隔离）。
// 调用方只需搜索会话时依赖此接口。
type SessionSearcher interface {
	// SearchByTag searches sessions by tag
	SearchByTag(tag string) ([]SessionSummary, error)
}

// SessionStore defines the interface for session persistence operations.
// The concrete Store type implements this interface; mock implementations
// can be used in tests to isolate external dependencies.
//
// 候选 A（ISP 接口隔离）：SessionStore 拆分为 4 个聚焦子接口的组合：
// - SessionReader（5 只读方法）
// - SessionWriter（9 写方法）
// - SessionLocker（3 锁方法）
// - SessionSearcher（1 搜索方法）
//
// 组合接口方法集与原 19 方法完全一致，向后兼容（所有现有调用方无需修改）。
// 新代码/测试可依赖更小的子接口，降低 mock 成本。
type SessionStore interface {
	SessionReader
	SessionWriter
	SessionLocker
	SessionSearcher
}
