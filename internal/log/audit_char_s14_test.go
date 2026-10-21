// Package log — S14「密钥管理」域 characterization 测试：日志脱敏链路。
//
// 生产路径：log.Info/Warn/Error → redactArgs（参数级）→ slog → RedactHandler（属性级）
//
//	→ 底层 JSON/Text handler → lumberjack 文件 sink。
//
// 本文件从**真实生产入口**（InitFileLogger + log.Error）验证落盘内容，
// 不使用 handler 替身，避免"测的是构造出来的路径"。
//
// ⚠️PIN_DEFECT 用例钉住当前**有缺陷**的行为（见 audit_char_s14_test.go 顶部约定）。
//
// ★ 2026-09-15（G-C 轮 / 决策项 F1）：两条日志链路的 PIN_DEFECT 用例已**有意翻转**为
// 正向断言（`..._PIN_DEFECT_LogPath_ErrorValueNotRedacted` → `..._ErrorValueIsRedacted`；
// `..._GroupValueNotRedacted` → `..._GroupValueIsRedacted`），与 `internal/log/redact_handler.go`
// 的 F1 补分支同步。判别力见各用例注释。
package log

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s14CapturedLog 初始化文件日志并返回落盘内容读取函数。
func s14CapturedLog(t *testing.T) func() []map[string]any {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "whale.log")
	if err := InitFileLogger(path, 10, 1, 1); err != nil {
		t.Fatalf("InitFileLogger: %v", err)
	}
	t.Cleanup(func() {
		CloseFileLogger()
		SetLevel(slog.LevelInfo)
	})
	return func() []map[string]any {
		CloseFileLogger()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("log line is not JSON: %q (%v)", line, err)
			}
			out = append(out, m)
		}
		return out
	}
}

const s14Secret = "sk-ant-api03-SECRETSECRETSECRETSECRET"

// TestS14Char_LogPath_RedactsStringValueAndSensitiveKeys 钉住当前**有效**的脱敏面：
// string 值脱敏；敏感字段名（含 struct 字段）整体替换为 "***"。
func TestS14Char_LogPath_RedactsStringValueAndSensitiveKeys(t *testing.T) {
	read := s14CapturedLog(t)

	type creds struct {
		APIKey string
		Note   string
	}
	Info("A-string", "token", s14Secret)
	Info("B-struct", slog.Any("creds", creds{APIKey: s14Secret, Note: "hello"}))

	lines := read()
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}
	if lines[0]["token"] != "***" {
		t.Errorf("string token not masked: %v", lines[0]["token"])
	}
	creds0, _ := lines[1]["creds"].(map[string]any)
	if creds0["APIKey"] != "***" {
		t.Errorf("struct APIKey not masked: %v", creds0)
	}
	if creds0["Note"] != "hello" {
		t.Errorf("non-sensitive field should be kept: %v", creds0)
	}
}

// TestS14Char_LogPath_ErrorValueIsRedacted ★2026-09-15 有意翻转（决策项 F1 / S14-HG-2）。
//
// 原用例名 `..._PIN_DEFECT_LogPath_ErrorValueNotRedacted` 钉住的是**缺陷**：
// `log.Error(msg, "error", err)` 中 error 既非 string 也非 struct/map，
// 既不过 redactArgs 也不过 RedactHandler ⇒ 错误文本里的密钥原样落盘。
//
// 现断言方向**反转**：落盘内容不得含原始密钥；且必须保留上下文（`[REDACTED]` 可见，
// 不是整条丢掉）—— 对齐业内既有实现「日志只发经审查字段 + redacted error values」。
//
// 判别力：摘掉 `redactAny` 的 `case error` 分支 ⇒ 本用例立刻打红。
func TestS14Char_LogPath_ErrorValueIsRedacted(t *testing.T) {
	read := s14CapturedLog(t)

	err := errors.New("request failed: https://api.example.com/v1?token=" + s14Secret)
	Error("call failed", "error", err)

	lines := read()
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1", len(lines))
	}
	got, _ := lines[0]["error"].(string)
	if strings.Contains(got, s14Secret) || strings.Contains(got, "sk-ant-") {
		t.Fatalf("F1 失败：error 值仍含密钥明文 -> %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("脱敏后应保留可读上下文（含 [REDACTED]），got %q", got)
	}
}

// TestS14Char_LogPath_GroupValueIsRedacted ★2026-09-15 有意翻转（决策项 F1 / S14-HG-2）。
//
// 原用例名 `..._PIN_DEFECT_LogPath_GroupValueNotRedacted` 钉住的是**缺陷**：
// `slog.Group` 的 Value 是 group/`[]slog.Attr`，redactAttr / redactReflectValue
// 都不处理 ⇒ 组内敏感字段原样落盘。
//
// 现断言方向**反转**：组内敏感键必须被整体替换为 `"***"`。
//
// 判别力：摘掉 `redactValue` 的 `slog.KindGroup` 分支 ⇒ 本用例立刻打红。
func TestS14Char_LogPath_GroupValueIsRedacted(t *testing.T) {
	read := s14CapturedLog(t)

	Info("grouped", slog.Any("payload", slog.GroupValue(slog.String("api_key", s14Secret))))

	lines := read()
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1", len(lines))
	}
	payload, _ := lines[0]["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("payload missing: %v", lines[0])
	}
	if payload["api_key"] == s14Secret {
		t.Fatalf("F1 失败：组内敏感字段仍是明文 -> %v", payload)
	}
	if payload["api_key"] != "***" {
		t.Errorf("组内敏感键应被替换为 ***，got %v", payload["api_key"])
	}
}
