package bridge

import (
	"os"
	"runtime"
	"sync"
	"time"
)

// TokenInfo contains the complete runtime info of a JWT token
type TokenInfo struct {
	RawToken   string      // Raw JWT string
	Payload    *JwtPayload // Decoded payload
	ObtainedAt time.Time   // Time obtained
	ExpiresAt  time.Time   // Expiration time
	Source     string      // Token source (e.g. "vscode", "cli" etc.)
}

// IsExpired checks if the token is expired
// No expiration time (zero value) is treated as not expired
func (t *TokenInfo) IsExpired() bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(t.ExpiresAt)
}

// IsExpiringSoon checks if the token is expiring within the specified buffer
// buffer is the lead time, e.g. 5*time.Minute means expiring within 5 minutes
// No expiration time (zero value) is treated as not expiring soon
func (t *TokenInfo) IsExpiringSoon(buffer time.Duration) bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(buffer).After(t.ExpiresAt)
}

// TokenStore provides thread-safe Token storage
type TokenStore struct {
	mu    sync.RWMutex
	token *TokenInfo
}

// NewTokenStore creates a new Token store instance
func NewTokenStore() *TokenStore {
	return &TokenStore{}
}

// Set stores TokenInfo into the store
func (s *TokenStore) Set(info *TokenInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = info
}

// Get gets the currently stored TokenInfo
// Returns nil if store is empty
func (s *TokenStore) Get() *TokenInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Clear clears the stored TokenInfo
func (s *TokenStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = nil
}

// FileStore provides atomic file-based persistence using temp file + rename
type FileStore struct {
	mu       sync.Mutex
	filePath string
}

// NewFileStore creates a new FileStore for the given file path
func NewFileStore(filePath string) *FileStore {
	return &FileStore{filePath: filePath}
}

// Save atomically writes data to the file using temp file + rename.
// H3/：文件权限 0600（仅 owner 可读），协同 H5 AtomicWriteFile。
func (fs *FileStore) Save(data []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	tmpFile := fs.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}

	// On Windows, os.Rename fails if the target file already exists.
	// Remove the target file first to ensure Rename succeeds.
	if runtime.GOOS == "windows" {
		os.Remove(fs.filePath)
	}

	err := os.Rename(tmpFile, fs.filePath)
	if err != nil {
		// Fallback: remove target and retry (for non-Windows edge cases)
		os.Remove(fs.filePath)
		err = os.Rename(tmpFile, fs.filePath)
		if err != nil {
			os.Remove(tmpFile)
		}
	}
	return err
}

// Load reads data from the file
func (fs *FileStore) Load() ([]byte, error) {
	data, err := os.ReadFile(fs.filePath)
	if err == nil {
		return data, nil
	}

	tmpFile := fs.filePath + ".tmp"
	tmpData, tmpErr := os.ReadFile(tmpFile)
	if tmpErr != nil {
		return nil, err
	}

	return tmpData, nil
}
