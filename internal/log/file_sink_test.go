// Package log — file sink integration tests (5-A 新增)
package log

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetDefaultLogger 将全局 slog 默认 handler 重置为 stderr 文本 handler。
// InitFileLogger 会把全局 handler 切到 JSON+MultiWriter，测试结束后需复位，
// 避免后续测试向已关闭的 lumberjack writer 写入。
func resetDefaultLogger() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}

// TestInitFileLogger_WritesToFile 验证 InitFileLogger 后日志写入文件且为 JSON 格式
func TestInitFileLogger_WritesToFile(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	Info("test message", slog.String("key", "value"))

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "test message") {
		t.Errorf("日志文件不含 'test message': %q", string(data))
	}
	// JSON 格式应含 "key":"value"
	if !strings.Contains(string(data), `"key"`) {
		t.Errorf("日志文件应为 JSON 格式含 key: %q", string(data))
	}
}

// TestInitFileLogger_Rotation 验证文件超 maxSizeMB 时触发轮转
func TestInitFileLogger_Rotation(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "rotate.log")

	// maxSizeMB=1（1MB），写入超过 1MB 触发轮转
	if err := InitFileLogger(logFile, 1, 3, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	// 写入 ~2.5MB 日志触发至少一次轮转
	big := strings.Repeat("x", 500*1024) // 500KB 单条
	for i := 0; i < 5; i++ {
		Info("rotation test", slog.Int("i", i), slog.String("data", big))
	}

	CloseFileLogger()
	resetDefaultLogger()

	// 应存在 rotate.log（当前）和至少一个 rotate 前缀的备份文件
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	var backups []string
	for _, e := range entries {
		name := e.Name()
		if name != "rotate.log" && strings.HasPrefix(name, "rotate") {
			backups = append(backups, name)
		}
	}
	if len(backups) == 0 {
		t.Errorf("未触发轮转，无备份文件: %v", entries)
	}
}

// TestInitFileLogger_CloseIdempotent 验证 CloseFileLogger 可多次调用不 panic
func TestInitFileLogger_CloseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "idem.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}

	// 多次 Close 不应 panic
	CloseFileLogger()
	CloseFileLogger()
	CloseFileLogger()
	resetDefaultLogger()

	// 未初始化时 Close 也不应 panic（fileSink 为 nil，安全返回）
	CloseFileLogger()
}

// TestInitFileLogger_ReinitNoLeak 验证重复 InitFileLogger 关闭旧 sink 不泄漏
func TestInitFileLogger_ReinitNoLeak(t *testing.T) {
	tmpDir := t.TempDir()
	logFile1 := filepath.Join(tmpDir, "v1.log")
	logFile2 := filepath.Join(tmpDir, "v2.log")

	if err := InitFileLogger(logFile1, 1, 1, 1); err != nil {
		t.Fatalf("Init v1 失败: %v", err)
	}
	Info("first sink")

	// 重复 Init 应关闭旧 sink
	if err := InitFileLogger(logFile2, 1, 1, 1); err != nil {
		t.Fatalf("Init v2 失败: %v", err)
	}
	Info("second sink")
	CloseFileLogger()
	resetDefaultLogger()

	// v1 文件应有 "first sink"
	data1, err := os.ReadFile(logFile1)
	if err != nil {
		t.Fatalf("读取 v1 失败: %v", err)
	}
	if !strings.Contains(string(data1), "first sink") {
		t.Errorf("v1 文件应含 'first sink': %q", string(data1))
	}
	// v2 文件应有 "second sink"，不应有 "first sink"
	data2, err := os.ReadFile(logFile2)
	if err != nil {
		t.Fatalf("读取 v2 失败: %v", err)
	}
	if !strings.Contains(string(data2), "second sink") {
		t.Errorf("v2 文件应含 'second sink': %q", string(data2))
	}
	if strings.Contains(string(data2), "first sink") {
		t.Errorf("v2 文件不应含 'first sink': %q", string(data2))
	}
}

// TestInitFileLogger_CreatesDir 验证 InitFileLogger 自动创建不存在的嵌套目录
func TestInitFileLogger_CreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	// 嵌套不存在的目录
	logFile := filepath.Join(tmpDir, "nested", "deep", "dir", "test.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 应自动创建目录，失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	Info("dir created")

	CloseFileLogger()
	resetDefaultLogger()
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("日志文件应存在: %v", err)
	}
}

// TestInitFileLogger_FailureFallback 验证路径不可写时返回 error（降级 stderr）
func TestInitFileLogger_FailureFallback(t *testing.T) {
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	// 创建一个文件，然后用它作为目录路径（类型冲突应失败）
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("创建 blocker 失败: %v", err)
	}
	// blocker 是文件，blocker/sub.log 会因 blocker 不是目录而失败
	logFile := filepath.Join(blocker, "sub.log")

	err := InitFileLogger(logFile, 1, 1, 1)
	if err == nil {
		CloseFileLogger()
		resetDefaultLogger()
		t.Error("期望 InitFileLogger 失败（路径冲突），但成功了")
	}
}
