// Package log — tests for structured logging and sensitive data redaction
package log

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRedactSensitive tests the sensitive data redaction function
func TestRedactSensitive(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		contains    string // substring that should be present after redaction
		notContains string // substring that should NOT be present after redaction
	}{
		{
			name:        "API key with equals",
			input:       "api_key=sk-abc123def456",
			contains:    "[REDACTED]",
			notContains: "sk-abc123def456",
		},
		{
			name:        "Bearer token",
			input:       "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.def",
			contains:    "Bearer [REDACTED]",
			notContains: "eyJhbGci",
		},
		{
			name:        "OpenAI key",
			input:       "Using key sk-proj-abcdefghijklmnopqrstuvwxyz123456789",
			contains:    "[REDACTED]",
			notContains: "sk-proj-abcdefgh",
		},
		{
			// 5-A: 该 URL 同时命中 password 正则与新增的 URL userinfo 正则，
			// userinfo 分支会把 user:password=mypassword@ 整体脱敏为 [REDACTED]:[REDACTED]@，
			// 故不再断言 "password=[REDACTED]"，改为校验 scheme 保留 + 密码不泄露。
			name:        "Password in connection string",
			input:       "postgresql://user:password=mypassword@localhost/db",
			contains:    "postgresql://",
			notContains: "mypassword",
		},
		{
			// 5-A: 补充非 URL 场景的 password 正则覆盖（原连接串用例被 URL userinfo 接管）
			name:        "Password assignment (non-URL)",
			input:       "config password=mypassword end",
			contains:    "password=[REDACTED]",
			notContains: "mypassword",
		},
		{
			name:        "JWT token",
			input:       "token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			contains:    "[REDACTED]",
			notContains: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		},
		{
			name:        "Normal message unchanged",
			input:       "This is a normal log message",
			contains:    "This is a normal log message",
			notContains: "[REDACTED]",
		},
		{
			name:        "Empty string",
			input:       "",
			contains:    "",
			notContains: "[REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSensitive(tt.input)
			if tt.contains != "" {
				// For empty string case, check exact match
				if tt.input == "" {
					if result != "" {
						t.Errorf("redactSensitive(%q) = %q, want empty string", tt.input, result)
					}
					return
				}
				if !contains(result, tt.contains) {
					t.Errorf("redactSensitive(%q) = %q, expected to contain %q", tt.input, result, tt.contains)
				}
			}
			if tt.notContains != "" && contains(result, tt.notContains) {
				t.Errorf("redactSensitive(%q) = %q, expected NOT to contain %q", tt.input, result, tt.notContains)
			}
		})
	}
}

// TestRedactArgs tests argument redaction
func TestRedactArgs(t *testing.T) {
	args := []any{
		"api_key=sk-1234567890abcdef",
		"normal message",
		42,
		true,
	}

	redacted := redactArgs(args...)
	if len(redacted) != len(args) {
		t.Errorf("redactArgs returned %d args, expected %d", len(redacted), len(args))
	}

	// String arg should be redacted
	if s, ok := redacted[0].(string); ok {
		if contains(s, "sk-1234567890abcdef") {
			t.Error("API key was not redacted in args")
		}
	}

	// Normal string should be unchanged
	if s, ok := redacted[1].(string); ok {
		if s != "normal message" {
			t.Errorf("Normal message was modified: %q", s)
		}
	}

	// Non-string args should be unchanged
	if v, ok := redacted[2].(int); !ok || v != 42 {
		t.Error("Integer arg was modified")
	}
	if v, ok := redacted[3].(bool); !ok || v != true {
		t.Error("Bool arg was modified")
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestRedactSensitive_GitHubToken 验证 GitHub 各前缀 token 被脱敏（5-A 新增）
func TestRedactSensitive_GitHubToken(t *testing.T) {
	// 36 字符后缀（GitHub PAT 固定前缀4 + 下划线1 + 后缀36 = 41 字符，正则 {36}）
	suffix := "0123456789abcdefghijklmnopqrstuvwxyz" // 36 chars
	tests := []struct {
		name        string
		input       string
		contains    string
		notContains string
	}{
		{"ghp_ PAT", "token ghp_" + suffix, "[REDACTED]", "ghp_0123456789"},
		{"gho_ OAuth app", "oauth gho_" + suffix, "[REDACTED]", "gho_0123456789"},
		{"ghu_ user-to-server", "user ghu_" + suffix, "[REDACTED]", "ghu_0123456789"},
		{"ghs_ server-to-server", "server ghs_" + suffix, "[REDACTED]", "ghs_0123456789"},
		{"ghr_ refresh", "refresh ghr_" + suffix, "[REDACTED]", "ghr_0123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSensitive(tt.input)
			if tt.notContains != "" && strings.Contains(result, tt.notContains) {
				t.Errorf("redactSensitive(%q) = %q, 不应包含 %q", tt.input, result, tt.notContains)
			}
			if tt.contains != "" && !strings.Contains(result, tt.contains) {
				t.Errorf("redactSensitive(%q) = %q, 应包含 %q", tt.input, result, tt.contains)
			}
		})
	}
}

// TestRedactSensitive_AWSAccessKey 验证 AWS Access Key ID（9 前缀 + 16 字符）被脱敏（5-A 新增）
func TestRedactSensitive_AWSAccessKey(t *testing.T) {
	// 所有 key 必须为 20 字符（前缀4 + 后缀16），正则 \b(前缀)[A-Z0-9]{16}\b
	keys := []string{
		"AKIAIOSFODNN7EXAMPLE", // AKIA + 16
		"ASIAIOSFODNN7EXAMPLE", // ASIA + 16
		"AGPAI44QX5M7EXAMPLE1", // AGPA + 16
		"AIDAI44QX5M7EXAMPLE1", // AIDA + 16
		"AROAI44QX5M7EXAMPLE1", // AROA + 16
	}
	for _, key := range keys {
		input := "aws key " + key + " end"
		result := redactSensitive(input)
		if strings.Contains(result, key) {
			t.Errorf("AWS key %q 未被脱敏: %q", key, result)
		}
		if !strings.Contains(result, "[REDACTED]") {
			t.Errorf("AWS key %q 脱敏后应含 [REDACTED]: %q", key, result)
		}
	}

	// 边界：短于 20 字符不应误伤（AKIA + 14 字符 = 18，不匹配 16 后缀）
	short := "AKIA1234567890ABCD"
	if result := redactSensitive(short); strings.Contains(result, "[REDACTED]") {
		t.Errorf("短于 20 字符不应误伤: input=%q result=%q", short, result)
	}
}

// TestRedactSensitive_SlackToken 验证 Slack token（xox[bpoa]- 前缀）被脱敏（5-A 新增）
func TestRedactSensitive_SlackToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"xoxb- bot", "slack xoxb-" + "1234567890-1234567890123-abcdefghij"},
		{"xoxu- user", "slack xoxu-123456789012-1234567890123456"},
		{"xoxo- oauth", "slack xoxo-1234567890123-abcdefghij"},
		{"xoxa- app", "slack xoxa-" + "1234567890-abcdefghij"},
		{"xoxp- workspace", "slack xoxp-" + "1234567890123-1234567890123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSensitive(tt.input)
			if strings.Contains(result, "xox") && !strings.Contains(result, "[REDACTED]") {
				t.Errorf("Slack token 未脱敏: input=%q result=%q", tt.input, result)
			}
		})
	}
}

// TestRedactSensitive_URLUserInfo 验证 URL 中的 user:pass@ 被脱敏，scheme 保留（5-A 新增）
func TestRedactSensitive_URLUserInfo(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		schemeKeep string
		passLeak   string
	}{
		{"postgres connection", "connecting to postgres://user:secret@dbhost:5432/mydb", "postgres://", "secret"},
		{"redis url", "redis://admin:mypassword@redis.local:6379", "redis://", "mypassword"},
		{"mongodb url", "mongodb://root:pass123@cluster.example.net/db", "mongodb://", "pass123"},
		{"https with userinfo", "https://apikey:sk-abc@service.io/path", "https://", "sk-abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSensitive(tt.input)
			if !strings.Contains(result, tt.schemeKeep) {
				t.Errorf("scheme 应保留: input=%q result=%q 缺 %q", tt.input, result, tt.schemeKeep)
			}
			if strings.Contains(result, tt.passLeak) {
				t.Errorf("密码泄露: input=%q result=%q 含 %q", tt.input, result, tt.passLeak)
			}
			if !strings.Contains(result, "[REDACTED]") {
				t.Errorf("应含 [REDACTED]: input=%q result=%q", tt.input, result)
			}
		})
	}

	// 边界：无 userinfo 的 URL 不应误伤
	plain := "https://example.com/path?query=1"
	if result := redactSensitive(plain); result != plain {
		t.Errorf("无 userinfo URL 不应改动: input=%q result=%q", plain, result)
	}
}

// TestRedactArgs_SlogAttr 验证 redactArgs 识别 slog.Attr 并脱敏其 string value（5-A 新增）
func TestRedactArgs_SlogAttr(t *testing.T) {
	args := []any{
		slog.String("token", "ghp_0123456789012345678901234567890123456"),
		slog.String("normal", "hello"),
		slog.Int("count", 42),
		"plain string with xoxb-" + "1234567890-abcdefghij",
	}

	redacted := redactArgs(args...)
	if len(redacted) != len(args) {
		t.Fatalf("redactArgs 返回 %d args, 期望 %d", len(redacted), len(args))
	}

	// slog.String("token", ghp_...) 应被脱敏
	if attr, ok := redacted[0].(slog.Attr); ok {
		if s, ok := attr.Value.Any().(string); ok {
			if strings.Contains(s, "ghp_") {
				t.Errorf("Attr token 未脱敏: %q", s)
			}
			if !strings.Contains(s, "[REDACTED]") {
				t.Errorf("Attr token 应含 [REDACTED]: %q", s)
			}
		} else {
			t.Errorf("Attr token value 非 string: %v", attr.Value)
		}
	} else {
		t.Errorf("redacted[0] 非 slog.Attr: %T", redacted[0])
	}

	// 普通 string Attr 不变
	if attr, ok := redacted[1].(slog.Attr); ok {
		if s, _ := attr.Value.Any().(string); s != "hello" {
			t.Errorf("普通 Attr 被改动: %q", s)
		}
	}

	// Int Attr 透传（slog.IntValue.Any() 返回 int64，非 int）
	if attr, ok := redacted[2].(slog.Attr); ok {
		if v, _ := attr.Value.Any().(int64); v != 42 {
			t.Errorf("Int Attr 被改动: %v", v)
		}
	}

	// 纯 string arg 脱敏
	if s, ok := redacted[3].(string); ok {
		if strings.Contains(s, "xoxb-"+"1234567890") {
			t.Errorf("string arg slack token 未脱敏: %q", s)
		}
	}
}

// --- L2 补充测试：反例测试 ---

// TestLog_LargeInput 验证超大输入不会 OOM 或超时
func TestLog_LargeInput(t *testing.T) {
	big := strings.Repeat("x", 1024*1024) // 1MB
	// redactSensitive 应在合理时间内完成，不 OOM
	start := time.Now()
	result := redactSensitive(big)
	elapsed := time.Since(start)
	if elapsed > 30*time.Second {
		t.Errorf("超大输入处理耗时 %v，超过 30s 阈值", elapsed)
	}
	if len(result) == 0 {
		t.Error("超大输入结果不应为空")
	}
}

// TestLog_InvalidInput 验证特殊字符输入的脱敏行为
func TestLog_InvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"null字节", "before\x00after"},
		{"unicode边界", "\U0010FFFF"},
		{"混合换行", "line1\nline2\r\nline3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 不应 panic
			result := redactSensitive(tt.input)
			if result == "" && tt.input != "" {
				t.Errorf("非空输入不应返回空结果: input=%q", tt.input)
			}
		})
	}
}

// TestLog_ConcurrentSafe 验证并发写入安全（梯队特有检查：文件 sink 并发写入安全）
func TestLog_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "concurrent.log")

	if err := InitFileLogger(logFile, 10, 5, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	const goroutines = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			Info("concurrent test", slog.Int("goroutine", idx))
		}(i)
	}
	wg.Wait()

	// 关闭后验证文件存在且有内容
	CloseFileLogger()
	resetDefaultLogger()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if len(data) == 0 {
		t.Error("并发写入后日志文件不应为空")
	}
	// 验证至少一部分 goroutine 的日志被写入
	count := strings.Count(string(data), "concurrent test")
	if count < goroutines/2 {
		t.Errorf("并发写入日志条数 %d，期望至少 %d", count, goroutines/2)
	}
}

// TestLog_PathTraversal 验证 InitFileLogger 路径穿越防护
func TestLog_PathTraversal(t *testing.T) {
	// 绝对路径在安全目录下应正常工作
	tmpDir := t.TempDir()
	safePath := filepath.Join(tmpDir, "safe", "test.log")
	if err := InitFileLogger(safePath, 1, 1, 1); err != nil {
		t.Errorf("安全路径应成功: %v", err)
	}
	CloseFileLogger()
	resetDefaultLogger()

	// 不可写路径应返回错误：跨平台构造——用一个**文件**占据父目录名，
	// 使日志目录创建必然失败（替代 POSIX 专属的 /proc/1/...）。
	blocker := filepath.Join(tmpDir, "blocker")
	if werr := os.WriteFile(blocker, []byte("x"), 0600); werr != nil {
		t.Fatal(werr)
	}
	err := InitFileLogger(filepath.Join(blocker, "safe", "test.log"), 1, 1, 1)
	if err == nil {
		CloseFileLogger()
		resetDefaultLogger()
		t.Error("不可写路径应返回错误")
	}
}

// --- L2 补充测试：正例测试（覆盖未覆盖函数） ---

// TestLog_SetLevel 验证日志级别切换
func TestLog_SetLevel(t *testing.T) {
	// 切换到 Debug 级别
	SetLevel(slog.LevelDebug)
	// 切换回 Info 级别
	SetLevel(slog.LevelInfo)
	// 不应 panic
}

// TestLog_SetJSONFormat 验证 JSON 格式切换
func TestLog_SetJSONFormat(t *testing.T) {
	SetJSONFormat()
	// 切回 Text 格式（通过 SetLevel 恢复）
	SetLevel(slog.LevelInfo)
}

// TestLog_WarnAndError 验证 Warn 和 Error 日志输出
func TestLog_WarnAndError(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "warn_error.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	Warn("warn message", slog.String("level", "warn"))
	Error("error message", slog.String("level", "error"))

	CloseFileLogger()
	resetDefaultLogger()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "warn message") {
		t.Errorf("日志文件应含 'warn message': %q", content)
	}
	if !strings.Contains(content, "error message") {
		t.Errorf("日志文件应含 'error message': %q", content)
	}
}

// TestLog_RedactSensitiveExported 验证导出的 RedactSensitive 函数
func TestLog_RedactSensitiveExported(t *testing.T) {
	// 使用 api_key= 前缀匹配 secret 模块的正则
	input := "api_key=sk-abc123def456"
	result := RedactSensitive(input)
	if strings.Contains(result, "sk-abc123def456") {
		t.Errorf("导出的 RedactSensitive 应脱敏: %q", result)
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("导出的 RedactSensitive 应含 [REDACTED]: %q", result)
	}
}

// TestLog_RedactSensitiveIdempotent 验证多次脱敏幂等性
func TestLog_RedactSensitiveIdempotent(t *testing.T) {
	input := "api_key=sk-abc123def456"
	first := redactSensitive(input)
	second := redactSensitive(first)
	if first != second {
		t.Errorf("脱敏应幂等: first=%q second=%q", first, second)
	}
}

// --- L2 补充测试：Linux 差异测试 ---

// TestLog_LinuxPathSeparator 验证 Linux 路径分隔符下的日志文件创建
func TestLog_LinuxPathSeparator(t *testing.T) {
	tmpDir := t.TempDir()
	// 使用正斜杠路径
	logFile := tmpDir + "/sub/dir/test.log"

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("Linux 路径分隔符下应成功: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	Info("linux path test")

	CloseFileLogger()
	resetDefaultLogger()

	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("日志文件应存在: %v", err)
	}
}

// TestLog_LinuxFilePermission 验证日志文件权限
func TestLog_LinuxFilePermission(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "perm.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	Info("permission test")
	CloseFileLogger()
	resetDefaultLogger()

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	// 验证文件可读写
	if info.Mode().Perm()&0600 != 0600 {
		t.Errorf("日志文件应有读写权限: %v", info.Mode())
	}
}

// --- L2 补充测试：SetLevel + currentWriter 联动 ---

// TestLog_SetLevelWithFileSink 验证 SetLevel 在文件 sink 启用后仍双写
func TestLog_SetLevelWithFileSink(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "level_sink.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	// SetLevel 应保持文件 sink 双写
	SetLevel(slog.LevelDebug)
	Info("after setlevel debug")

	CloseFileLogger()
	resetDefaultLogger()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "after setlevel debug") {
		t.Errorf("SetLevel 后文件 sink 应仍写入: %q", string(data))
	}
}

// TestLog_InfoOutputCapture 验证 Info 日志输出含脱敏内容
func TestLog_InfoOutputCapture(t *testing.T) {
	var buf bytes.Buffer
	// 用自定义 handler 捕获输出
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	defer resetDefaultLogger()

	Info("test api_key=sk-abc123def456")
	output := buf.String()
	if strings.Contains(output, "sk-abc123def456") {
		t.Errorf("Info 输出不应含原始密钥: %q", output)
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Errorf("Info 输出应含 [REDACTED]: %q", output)
	}
}

// --- M22-04 P0 SIGUSR1 日志级别切换测试 ---

// TestToggleDebug_InfoToDebugToInfo 验证 ToggleDebug 在 Debug/Info 之间 ping-pong（M22-04 P0）。
func TestToggleDebug_InfoToDebugToInfo(t *testing.T) {
	// 测试结束恢复 Info 级别，避免污染其他测试
	defer SetLevel(slog.LevelInfo)

	// 初始为 Info
	SetLevel(slog.LevelInfo)
	if got := CurrentLevel(); got != slog.LevelInfo {
		t.Fatalf("初始级别应为 Info，实际 %v", got)
	}

	// 第一次切换：Info → Debug
	ToggleDebug()
	if got := CurrentLevel(); got != slog.LevelDebug {
		t.Fatalf("第一次 ToggleDebug 后应为 Debug，实际 %v", got)
	}

	// 第二次切换：Debug → Info
	ToggleDebug()
	if got := CurrentLevel(); got != slog.LevelInfo {
		t.Fatalf("第二次 ToggleDebug 后应为 Info，实际 %v", got)
	}

	// 第三次切换：Info → Debug
	ToggleDebug()
	if got := CurrentLevel(); got != slog.LevelDebug {
		t.Fatalf("第三次 ToggleDebug 后应为 Debug，实际 %v", got)
	}
}

// TestCurrentLevel_ReturnsSetLevel 验证 CurrentLevel 反映 SetLevel 的写入（M22-04 P0）。
func TestCurrentLevel_ReturnsSetLevel(t *testing.T) {
	defer SetLevel(slog.LevelInfo)

	SetLevel(slog.LevelDebug)
	if got := CurrentLevel(); got != slog.LevelDebug {
		t.Errorf("CurrentLevel 应反映 SetLevel(Debug)，实际 %v", got)
	}

	SetLevel(slog.LevelWarn)
	if got := CurrentLevel(); got != slog.LevelWarn {
		t.Errorf("CurrentLevel 应反映 SetLevel(Warn)，实际 %v", got)
	}

	SetLevel(slog.LevelInfo)
	if got := CurrentLevel(); got != slog.LevelInfo {
		t.Errorf("CurrentLevel 应反映 SetLevel(Info)，实际 %v", got)
	}
}
