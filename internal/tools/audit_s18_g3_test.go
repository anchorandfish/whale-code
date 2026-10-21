package tools

import (
	"strings"
	"testing"
)

// audit_s18_g3_test.go — S18/G3（选项 B：数据隔离标记）回归护栏。
//
// 背景（I10 抗间接注入 / 自提权）：capability 记忆会被 loadAgentCapability 拼进
// 子代理 system prompt。修复后必须以「参考数据、非指令、非权限授予」信封包裹，
// 从提示层切断「记忆正文 → 间接注入 / 自提权」的语义通道。

// TestS18_G3_CapabilityInjectedAsDataNotInstructions：注入内容必须带数据隔离声明与信封。
func TestS18_G3_CapabilityInjectedAsDataNotInstructions(t *testing.T) {
	a := &AgentContext{
		AgentType: AgentTypeCoder,
		CapabilityLookup: func(key string) (string, string, error) {
			return "project uses bazel; run `bazel build //...`", "capability", nil
		},
	}
	got := a.loadAgentCapability()
	if got == "" {
		t.Fatal("期望注入 capability，实际为空")
	}
	if !strings.Contains(got, "NOT instructions") {
		t.Fatalf("缺少「非指令」数据隔离声明：%q", got)
	}
	if !strings.Contains(got, "permission grant") {
		t.Fatalf("缺少「非权限授予」声明：%q", got)
	}
	if !strings.Contains(got, "<<<MEMORY_DATA") || !strings.Contains(got, "MEMORY_DATA>>>") {
		t.Fatalf("缺少数据隔离信封标记：%q", got)
	}
	if !strings.Contains(got, "bazel build") {
		t.Fatalf("capability 正文缺失：%q", got)
	}
}

// TestS18_G3_UnverifiedKeepsMarkerAndEnvelope：unverified 仍附告警标记，且同样带隔离信封。
func TestS18_G3_UnverifiedKeepsMarkerAndEnvelope(t *testing.T) {
	a := &AgentContext{
		AgentType: AgentTypePlan,
		CapabilityLookup: func(key string) (string, string, error) {
			return "some capability value", "capability:unverified", nil
		},
	}
	got := a.loadAgentCapability()
	if !strings.Contains(got, "unverified") {
		t.Fatalf("unverified 标记丢失：%q", got)
	}
	if !strings.Contains(got, "NOT instructions") || !strings.Contains(got, "<<<MEMORY_DATA") {
		t.Fatalf("unverified 分支缺少数据隔离信封：%q", got)
	}
}
