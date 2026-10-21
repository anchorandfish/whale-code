// Package log — M22 扩展功能测试
package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestManagedBus 死事件清理测试在 bus 包，这里测试 log 包的脱敏与采样。

// TestRedactHandler_StructField 测试结构体字段脱敏（M22-02）。
type sensitiveStruct struct {
	Username string
	Password string
	Token    string
	APIKey   string `json:"api_key"`
}

func TestRedactHandler_StructField(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewRedactHandler(inner)
	logger := slog.New(handler)

	s := sensitiveStruct{
		Username: "alice",
		Password: "super-secret-pw",
		Token:    "ghp_abcdefghijklmnopqrstuvwx",
		APIKey:   "sk-1234567890abcdef",
	}
	logger.Info("test", "struct", s)

	output := buf.String()
	if strings.Contains(output, "super-secret-pw") {
		t.Errorf("password not redacted: %s", output)
	}
	if strings.Contains(output, "ghp_abcdefghijklmnopqrstuvwx") {
		t.Errorf("token not redacted: %s", output)
	}
	if strings.Contains(output, "sk-1234567890abcdef") {
		t.Errorf("api key not redacted: %s", output)
	}
	if !strings.Contains(output, "alice") {
		t.Errorf("username should be preserved: %s", output)
	}
}

// TestRedactHandler_StringValue 测试字符串值脱敏（M22-02）。
func TestRedactHandler_StringValue(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewRedactHandler(inner)
	logger := slog.New(handler)

	logger.Info("test", "token", "ghp_abcdefghijklmnopqrstuvwx", "password", "secret123")

	output := buf.String()
	if strings.Contains(output, "ghp_abcdefghijklmnopqrstuvwx") {
		t.Errorf("token not redacted: %s", output)
	}
	if strings.Contains(output, "secret123") {
		t.Errorf("password not redacted: %s", output)
	}
}

// TestRedactHandler_NestedStruct 测试嵌套结构体脱敏（M22-02）。
type nestedStruct struct {
	Outer struct {
		Token  string // 字段名 "Token" 命中 sensitiveKeys["token"]，验证嵌套递归脱敏
		Normal string
	}
}

func TestRedactHandler_NestedStruct(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewRedactHandler(inner)
	logger := slog.New(handler)

	n := nestedStruct{}
	n.Outer.Token = "ghp_secret_inner_token_value"
	n.Outer.Normal = "visible"

	logger.Info("test", "nested", n)

	output := buf.String()
	if strings.Contains(output, "ghp_secret_inner_token_value") {
		t.Errorf("nested token not redacted: %s", output)
	}
}

// TestRedactHandler_MapField 测试 map 字段脱敏（M22-02）。
func TestRedactHandler_MapField(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewRedactHandler(inner)
	logger := slog.New(handler)

	m := map[string]interface{}{
		"username": "bob",
		"password": "secret-pw",
		"token":    "ghp_map_token_value",
	}
	logger.Info("test", "map", m)

	output := buf.String()
	if strings.Contains(output, "secret-pw") {
		t.Errorf("map password not redacted: %s", output)
	}
	if strings.Contains(output, "ghp_map_token_value") {
		t.Errorf("map token not redacted: %s", output)
	}
}

// TestIsSensitiveKey 测试敏感字段名判断（M22-02）。
func TestIsSensitiveKey(t *testing.T) {
	if !IsSensitiveKey("password") {
		t.Error("password should be sensitive")
	}
	if !IsSensitiveKey("TOKEN") {
		t.Error("TOKEN should be sensitive (case insensitive)")
	}
	if IsSensitiveKey("username") {
		t.Error("username should not be sensitive")
	}
}

// TestFormatRedactedStruct 测试结构体格式化脱敏（M22-02）。
func TestFormatRedactedStruct(t *testing.T) {
	s := sensitiveStruct{
		Username: "alice",
		Password: "secret",
	}
	result := FormatRedactedStruct(s)
	if strings.Contains(result, "secret") {
		t.Errorf("password not redacted in format: %s", result)
	}
}

// TestSamplerHandler_Basic 测试基本采样（M22-03）。
func TestSamplerHandler_Basic(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewSamplerHandler(inner, SamplerConfig{
		Rate:  10, // 每 10 条记录 1 条
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler)

	for i := 0; i < 100; i++ {
		logger.Info("test message")
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// 100 条采样 1/10，应该约 10 条
	if len(lines) < 5 || len(lines) > 20 {
		t.Errorf("expected ~10 sampled logs, got %d", len(lines))
	}
}

// TestSamplerHandler_BelowLevel 测试低于采样级别全部记录（M22-03）。
func TestSamplerHandler_BelowLevel(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewSamplerHandler(inner, SamplerConfig{
		Rate:  10,
		Level: slog.LevelWarn, // 只对 Warn 及以上采样
	})
	logger := slog.New(handler)

	for i := 0; i < 50; i++ {
		logger.Info("info message") // 低于 Warn，全部记录
	}
	for i := 0; i < 50; i++ {
		logger.Warn("warn message") // Warn 采样
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// 50 Info 全部 + 50 Warn 采样 ~5 条
	if len(lines) < 50 || len(lines) > 60 {
		t.Errorf("expected 50-60 logs (50 info + ~5 sampled warn), got %d", len(lines))
	}
}

// TestSamplerHandler_Disabled 测试采样禁用（M22-03）。
func TestSamplerHandler_Disabled(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewSamplerHandler(inner, SamplerConfig{
		Rate: 1, // Rate<=1 禁用采样
	})
	logger := slog.New(handler)

	for i := 0; i < 100; i++ {
		logger.Info("test message")
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 100 {
		t.Errorf("expected 100 logs (no sampling), got %d", len(lines))
	}
}

// TestSamplerHandler_CountValue 测试计数器值（M22-03）。
func TestSamplerHandler_CountValue(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewSamplerHandler(inner, SamplerConfig{
		Rate:  5,
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler)

	for i := 0; i < 20; i++ {
		logger.Info("test")
	}

	if handler.CountValue() != 20 {
		t.Errorf("expected count 20, got %d", handler.CountValue())
	}
}

// TestChainedHandler 测试链式组合（M22-03）。
func TestChainedHandler(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewChainedHandler(inner, SamplerConfig{
		Rate:  5,
		Level: slog.LevelInfo,
	}, true)
	logger := slog.New(handler)

	logger.Info("test", "password", "secret123")

	output := buf.String()
	if strings.Contains(output, "secret123") {
		t.Errorf("chained handler should redact: %s", output)
	}
}

// TestSamplerHandler_WindowReset 测试窗口重置（M22-03）。
func TestSamplerHandler_WindowReset(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewSamplerHandler(inner, SamplerConfig{
		Rate:       1000, // 高采样率，几乎全丢弃
		Level:      slog.LevelInfo,
		WindowSize: 10 * time.Millisecond, // 短窗口测试重置
	})
	logger := slog.New(handler)

	logger.Info("test1")
	time.Sleep(20 * time.Millisecond) // 等待窗口过期
	logger.Info("test2")

	// 窗口重置后第一条应被记录
	output := buf.String()
	if !strings.Contains(output, "test1") {
		t.Errorf("first message in window should be logged: %s", output)
	}
	if !strings.Contains(output, "test2") {
		t.Errorf("first message in new window should be logged: %s", output)
	}
}

// 确保 context 被使用（避免 import 未使用错误）
var _ = context.Background
