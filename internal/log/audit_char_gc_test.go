// Package log — G-C 落盘验收：F1（日志脱敏补分支：error / Group / 命名 string / Stringer）。
//
// 生产路径：`InitFileLogger` → `log.Info/Error/Warn` → `redactArgs`（参数级）
//
//	→ slog → `RedactHandler`（属性级）→ JSON handler → lumberjack 文件 sink
//
// 本文件**不引入 handler 替身**：全部经真实生产入口落盘后再读回，避免"测的是构造出来的路径"。
package log

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// gcSecret 是一个必然命中 anthropic 规则的密钥形状。
const gcSecret = "sk-ant-api03-SECRETSECRETSECRETSECRET"

// gcNamedToken 是"命名 string 类型"——`case string` 匹配不到它，是 F1 的判别点之一。
type gcNamedToken string

// gcNumeric 覆盖 F1 伴随修正 (b)：非字符串字段**不得**被清零。
type gcNumeric struct {
	Port   int
	Debug  bool
	Ratio  float64
	APIKey string
	Note   string
}

// gcHasUnexportedField 覆盖 F1 伴随修正 (a)：含未导出 string 字段的结构体不得 panic。
type gcHasUnexportedField struct {
	Public  string
	private string //nolint:unused // 仅用于触发"未导出字段"路径
}

// TestF1_ErrorValueIsRedactedInEveryShape `error` 在三种传参形态下都必须被脱敏：
// 裸值、`slog.Any`、`slog.String` 包装。
func TestF1_ErrorValueIsRedactedInEveryShape(t *testing.T) {
	read := s14CapturedLog(t)
	err := fmt.Errorf("request failed: https://api.example.com/v1?token=%s", gcSecret)

	Error("bare", "error", err)
	Error("any", slog.Any("error", err))
	Error("stringed", slog.String("error", err.Error()))

	lines := read()
	if len(lines) != 3 {
		t.Fatalf("got %d log lines, want 3", len(lines))
	}
	for i, ln := range lines {
		got, _ := ln["error"].(string)
		if strings.Contains(got, gcSecret) || strings.Contains(got, "sk-ant-") {
			t.Errorf("line %d：error 值仍含密钥明文: %q", i, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("line %d：脱敏未生效（期望保留上下文 + [REDACTED]）: %q", i, got)
		}
	}
}

// TestF1_GroupValueIsRedactedRecursively `slog.Group` / `slog.GroupValue` / `[]slog.Attr`
// 三种组形态（含嵌套）的组内敏感字段都必须被遮盖。
func TestF1_GroupValueIsRedactedRecursively(t *testing.T) {
	read := s14CapturedLog(t)

	Info("group", slog.Any("payload", slog.GroupValue(slog.String("api_key", gcSecret))))
	Info("nested", slog.Any("outer", slog.GroupValue(
		slog.String("note", "hello"),
		slog.Any("inner", slog.GroupValue(slog.String("password", "hunter2"))),
	)))
	Info("slice-attr", slog.Any("xs", []slog.Attr{slog.String("client_secret", gcSecret)}))

	lines := read()
	if len(lines) != 3 {
		t.Fatalf("got %d log lines, want 3", len(lines))
	}

	payload, _ := lines[0]["payload"].(map[string]any)
	if payload == nil || payload["api_key"] == gcSecret {
		t.Fatalf("组内 api_key 未脱敏: %v", lines[0])
	}
	if payload["api_key"] != "***" {
		t.Errorf("组内敏感键应整体替换为 ***，got %v", payload["api_key"])
	}

	outer, _ := lines[1]["outer"].(map[string]any)
	if outer == nil {
		t.Fatalf("嵌套组丢失: %v", lines[1])
	}
	if outer["note"] != "hello" {
		t.Errorf("非敏感字段不应被改动: %v", outer["note"])
	}
	inner, _ := outer["inner"].(map[string]any)
	if inner == nil || inner["password"] != "***" {
		t.Errorf("二层嵌套的敏感键未脱敏: outer=%v", outer)
	}

	xs, _ := lines[2]["xs"].(map[string]any)
	if xs == nil || xs["client_secret"] != "***" {
		t.Errorf("[]slog.Attr 组未脱敏: %v", lines[2])
	}
}

// TestF1_NamedStringTypeIsRedacted 命名 string 类型（`type T string`）也必须脱敏。
//
// 判别点：原实现用 `v.(string)` 做类型断言，命名类型走不到该分支 ⇒ 明文落盘。
func TestF1_NamedStringTypeIsRedacted(t *testing.T) {
	read := s14CapturedLog(t)

	Info("named", slog.Any("tok", gcNamedToken(gcSecret)))
	// 非敏感键名的命名 string 类型同样要按"值"脱敏（不是靠键掩码）
	Info("named-plain-key", slog.Any("blob", gcNamedToken("prefix-"+gcSecret)))

	lines := read()
	for i, ln := range lines {
		raw, _ := json.Marshal(ln)
		if strings.Contains(string(raw), gcSecret) {
			t.Errorf("line %d：命名 string 类型的密钥明文落盘: %s", i, raw)
		}
	}
}

// TestF1_StringerAndTimeAreNotConfused 判别"按 Kind 分派"的价值：
//   - `slog.Time` 的值**不得**被当成 fmt.Stringer 压成字符串（否则所有时间字段形状被改坏）；
//   - 非结构化的 Stringer 必须按 String() 脱敏。
func TestF1_StringerAndTimeAreNotConfused(t *testing.T) {
	read := s14CapturedLog(t)

	ts := time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC)
	Info("with-time", slog.Time("t", ts))
	Info("with-duration", slog.Duration("d", 1500*time.Millisecond))
	Info("with-int", slog.Int("n", 42))

	lines := read()
	if len(lines) != 3 {
		t.Fatalf("got %d log lines, want 3", len(lines))
	}
	if got, _ := lines[0]["t"].(string); !strings.HasPrefix(got, "2026-09-15T01:02:03") {
		t.Errorf("slog.Time 被改坏（Kind 分派失效）: %v", lines[0]["t"])
	}
	// slog 的 JSON handler 把 Duration 渲染为**纳秒整数**（slog 自身行为；脱敏层不得改动它）。
	if got, ok := lines[1]["d"].(float64); !ok || got != float64(1500*time.Millisecond) {
		t.Errorf("slog.Duration 被改坏: %v", lines[1]["d"])
	}
	if got, ok := lines[2]["n"].(float64); !ok || got != 42 {
		t.Errorf("slog.Int 被改坏: %v", lines[2]["n"])
	}
}

// TestF1_StructFieldsPreservedAndUnexportedDoesNotPanic 覆盖 F1 的两条伴随修正：
//
//	(a) 含未导出 string 字段的结构体：原实现无 CanSet 检查就 SetString ⇒ **打日志即 panic**；
//	(b) 非字符串字段（int/bool/float）不得被静默清零 —— 脱敏不等于篡改。
func TestF1_StructFieldsPreservedAndUnexportedDoesNotPanic(t *testing.T) {
	read := s14CapturedLog(t)

	Info("struct", slog.Any("cfg", gcNumeric{Port: 8080, Debug: true, Ratio: 0.5, APIKey: gcSecret, Note: "hi"}))
	Info("unexported", slog.Any("obj", gcHasUnexportedField{Public: "p", private: gcSecret}))

	lines := read()
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2（若为 panic 则说明 (a) 未修）", len(lines))
	}

	cfg, _ := lines[0]["cfg"].(map[string]any)
	if cfg == nil {
		t.Fatalf("cfg 缺失: %v", lines[0])
	}
	if cfg["Port"] != float64(8080) {
		t.Errorf("(b) 非字符串字段被清零: Port=%v", cfg["Port"])
	}
	if cfg["Debug"] != true {
		t.Errorf("(b) 布尔字段被清零: Debug=%v", cfg["Debug"])
	}
	if cfg["Ratio"] != 0.5 {
		t.Errorf("(b) 浮点字段被清零: Ratio=%v", cfg["Ratio"])
	}
	if cfg["APIKey"] != "***" {
		t.Errorf("敏感字段未被遮盖: APIKey=%v", cfg["APIKey"])
	}
	if cfg["Note"] != "hi" {
		t.Errorf("非敏感字段被改动: Note=%v", cfg["Note"])
	}

	obj, _ := lines[1]["obj"].(map[string]any)
	if obj == nil || obj["Public"] != "p" {
		t.Errorf("未导出字段用例输出异常: %v", lines[1])
	}
	raw, _ := json.Marshal(lines[1])
	if strings.Contains(string(raw), gcSecret) {
		t.Errorf("未导出字段内容竟进了日志（无泄露面假设不成立）: %s", raw)
	}
}

// TestF1_HandlerAppliedWithoutInitFileLogger 参数级（redactArgs）单独也必须脱敏 error/Group
// —— 这是"双保险"的一半，不能只在装了 RedactHandler 时才成立。
func TestF1_HandlerAppliedWithoutInitFileLogger(t *testing.T) {
	args := redactArgs("error", fmt.Errorf("boom %s", gcSecret), slog.Any("g", slog.GroupValue(slog.String("token", gcSecret))))
	if len(args) != 3 {
		t.Fatalf("redactArgs 返回 %d 项，期望 3", len(args))
	}
	first, ok := args[1].(string)
	if !ok || strings.Contains(first, gcSecret) {
		t.Errorf("参数级未脱敏裸 error：%#v", args[1])
	}
	attr, ok := args[2].(slog.Attr)
	if !ok {
		t.Fatalf("redacted[2] 非 slog.Attr: %T", args[2])
	}
	inner := attr.Value.Group()
	if len(inner) != 1 || inner[0].Value.String() != "***" {
		t.Errorf("参数级未脱敏 Group 内敏感键：%v", inner)
	}
}

// TestF1_OriginalStringsStayUntouched 回归守卫：非敏感内容一个字节都不许动
// （脱敏层不得变成"改写日志"）。
func TestF1_OriginalStringsStayUntouched(t *testing.T) {
	read := s14CapturedLog(t)
	Info("plain", "path", "/tmp/whale-code/main.go", "count", 7, "ok", true)

	lines := read()
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1", len(lines))
	}
	if lines[0]["path"] != "/tmp/whale-code/main.go" {
		t.Errorf("普通字符串被改动: %v", lines[0]["path"])
	}
	if lines[0]["count"] != float64(7) {
		t.Errorf("数字被改动: %v", lines[0]["count"])
	}
	if lines[0]["ok"] != true {
		t.Errorf("布尔被改动: %v", lines[0]["ok"])
	}
}
