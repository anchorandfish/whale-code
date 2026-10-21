// Package log — S13 日志/审计 表征测试（Snapshot 步，仅钉当前行为，不改源码）
//
// 钉住 internal/log 的"敏感数据脱敏"契约（I7 密钥不泄露，是审计/日志内容不可泄密的根基）。
// 与 internal/bridge/audit_char_s13_test.go 互补：后者钉"专用审计子系统"，本文件钉
// "通用日志的脱敏层"。两文件都只加测试、不改源码，全部用例当前必须 GREEN。
package log

import (
	"strings"
	"testing"
)

// L1：敏感字段名判定表已接线（I7）。authorization 命中、非敏感名如 username 不命中。
func TestCharS13_SensitiveKeyTableWired(t *testing.T) {
	if !IsSensitiveKey("authorization") {
		t.Fatal("authorization should be a sensitive key")
	}
	if !IsSensitiveKey("client_secret") {
		t.Fatal("client_secret should be a sensitive key")
	}
	if IsSensitiveKey("username") {
		t.Fatal("username must NOT be treated as sensitive by the static key table")
	}
}

// L2：结构体脱敏会遮盖命名字段的值（如 Password），且不泄漏原文、保留非敏感字段。
func TestCharS13_FormatRedactedStructMasksSensitiveField(t *testing.T) {
	type cred struct {
		Username string
		Password string
	}
	out := FormatRedactedStruct(cred{Username: "alice", Password: "hunter2"})
	if !strings.Contains(out, "alice") {
		t.Fatalf("non-sensitive field should be preserved, got %q", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("password value leaked into redacted output: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("sensitive field should be masked with ***, got %q", out)
	}
}
