// Package daemon — Whale Code daemon mode
// Provides background task queue, worker management, inter-process communication etc.
package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DaemonLogger is the daemon log recorder
// - Writes to log file
// - Supports log rotation with numbered backups
type DaemonLogger struct {
	mu          sync.Mutex
	file        *os.File
	logPath     string
	basePath    string
	maxFileSize int64
	maxBackups  int
	closed      bool
}

// NewDaemonLogger creates daemon logger
// logPath: log file path
func NewDaemonLogger(logPath string) (*DaemonLogger, error) {
	// Ensure log directory exists
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return &DaemonLogger{
		file:        file,
		logPath:     logPath,
		basePath:    logPath,
		maxFileSize: 10 * 1024 * 1024, // 默认 10MB
		maxBackups:  3,                // 保留最近 3 个轮转文件
		closed:      false,
	}, nil
}

// SetMaxFileSize 设置日志轮转的大小阈值（字节）。
// 允许调用方在创建 logger 后动态配置轮转大小，默认 10MB。
// 传入 size <= 0 时忽略，保持原值不变。
func (l *DaemonLogger) SetMaxFileSize(size int64) {
	if size <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxFileSize = size
}

// SetMaxBackups 设置保留的轮转备份数量。
// 允许调用方在创建 logger 后动态配置保留数量，默认 3。
// 传入 n <= 0 时忽略，保持原值不变。
func (l *DaemonLogger) SetMaxBackups(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxBackups = n
}

// Info logs info-level message
func (l *DaemonLogger) Info(format string, args ...interface{}) {
	l.writeLog("INFO", format, args...)
}

// Error logs error-level message
func (l *DaemonLogger) Error(format string, args ...interface{}) {
	l.writeLog("ERROR", format, args...)
}

// Close closes log file
func (l *DaemonLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true
	return l.file.Close()
}

// writeLog writes log entry with timestamp and level prefix
func (l *DaemonLogger) writeLog(level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return
	}

	// Check if rotation is needed
	if err := l.rotateIfNeeded(); err != nil {
		// Still try to write on rotation failure
		fmt.Fprintf(os.Stderr, "Log rotation failed: %v\n", err)
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] [%s] %s\n", timestamp, level, msg)

	if _, err := l.file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write log: %v\n", err)
	}
}

// rotateIfNeeded checks log file size and performs rotation when needed
func (l *DaemonLogger) rotateIfNeeded() error {
	info, err := l.file.Stat()
	if err != nil {
		return err
	}

	if info.Size() < l.maxFileSize {
		return nil
	}

	if err := l.file.Close(); err != nil {
		return err
	}

	oldestBackup := fmt.Sprintf("%s.%d", l.basePath, l.maxBackups)
	os.Remove(oldestBackup)

	for i := l.maxBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", l.basePath, i)
		newPath := fmt.Sprintf("%s.%d", l.basePath, i+1)
		os.Rename(oldPath, newPath)
	}

	backupPath := l.basePath + ".1"
	if err := os.Rename(l.basePath, backupPath); err != nil {
		l.file, _ = os.OpenFile(l.basePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		return err
	}

	file, err := os.OpenFile(l.basePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.file = file
	return nil
}

// Write implements io.Writer interface, convenient for integration with other log libraries
func (l *DaemonLogger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return 0, io.ErrClosedPipe
	}

	if err := l.rotateIfNeeded(); err != nil {
		fmt.Fprintf(os.Stderr, "Log rotation failed: %v\n", err)
	}

	return l.file.Write(p)
}
