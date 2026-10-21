// Package lumberjack 是 gopkg.in/natefinch/lumberjack.v2 的本地 stub。
//
// H2 临时 replace：本机 Winsock 损坏无法 go get lumberjack v2.2.1，
// 此 stub 提供编译所需的最小 API（Logger 类型 + Write/Close 方法）。
// 仅用于编译验证，不含日志轮转/压缩/归档功能。
// CI 环境（DNS 正常）应移除 replace 指令使用上游 lumberjack。
package lumberjack

import (
	"os"
	"sync"
)

// Logger 是日志轮转器的 stub 实现，字段与上游 lumberjack.Logger 兼容。
// 字段仅保留兼容性，stub 实现不执行轮转/压缩逻辑。
type Logger struct {
	// Filename 日志文件路径。
	Filename string
	// MaxSize 单文件最大 MB（stub 不实现轮转）。
	MaxSize int
	// MaxBackups 保留的旧日志数量（stub 不实现）。
	MaxBackups int
	// MaxAge 保留天数（stub 不实现）。
	MaxAge int
	// Compress 是否 gzip 压缩旧日志（stub 不实现）。
	Compress bool
	// LocalTime 是否使用本地时间（stub 不实现）。
	LocalTime bool

	mu   sync.Mutex
	file *os.File
}

// Write 实现 io.Writer，将日志写入文件（追加模式）。
// 若文件未打开则先打开（按 Filename 路径，0755 目录 + 0644 文件）。
func (l *Logger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		f, err := os.OpenFile(l.Filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return 0, err
		}
		l.file = f
	}
	return l.file.Write(p)
}

// Close 实现 io.Closer，关闭底层文件句柄。
// 可多次安全调用。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}
