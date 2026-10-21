// Package ai — V3-6-3 AI 拒绝覆盖单元测试
//
// ★ （2026-09-19 · 方案 A）**有意翻转**（用户放行）：
// 本文件原先还有 4 个 `DetectRefusal` 用例（中文表 / 英文表 / 不误报 / 空输入 + 原因串）
// 和 2 个 `ForceExecutePrompt` 用例 —— 它们锁的是**已被整体删除**的散文关键词判据。
// 删除理由见 `refusal_handler.go` 包注释（三条）与 `internal/tui/stream.go checkAISafety`。
//
// 本文件现在只钉"保留下来的一半"：用户**主动**确认词 → 注入 → 复位；
// 以及辅助信号 `MentionsSecurityConstraint` 仍然是辅助（不得被当回判据）。
//
// 防回退的结构门（禁止把散文关键词表加回来）见 `.go`。
package ai

import (
	"strings"
	"testing"
)

// TestRefusal_IsForceExecuteConfirm 测试强制执行确认。
func TestRefusal_IsForceExecuteConfirm(t *testing.T) {
	h := NewRefusalHandler(true)
	tests := []struct {
		input string
		want  bool
	}{
		{"我确认强制执行", true},
		{"确认强制执行", true},
		{"force execute", true},
		{"FORCE EXECUTE", true},
		{"Force Execute", true},
		{"  我确认强制执行  ", true}, // 带空白
		{"我确认", false},
		{"强制执行", false},
		{"", false},
		{"yes", false},
		{"我确认强制执行吧", false}, // 不能是子串，需精确匹配
	}
	for _, tt := range tests {
		got := h.IsForceExecuteConfirm(tt.input)
		if got != tt.want {
			t.Errorf("IsForceExecuteConfirm(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestRefusal_SystemMessage 测试 system message 生成。
func TestRefusal_SystemMessage(t *testing.T) {
	h := NewRefusalHandler(true)

	// 未进入强制执行模式 → 空
	if msg := h.GetForceExecuteSystemMessage(); msg != "" {
		t.Errorf("未进入强制执行模式时应返回空: %q", msg)
	}

	// 进入强制执行模式 → 非空
	h.SetForceExecuteMode(true)
	msg := h.GetForceExecuteSystemMessage()
	if msg == "" {
		t.Errorf("进入强制执行模式时应返回非空")
	}
	if !strings.Contains(msg, "强制执行") {
		t.Errorf("system message 应包含'强制执行': %q", msg)
	}
	if !strings.Contains(msg, "沙箱") || !strings.Contains(msg, "权限") {
		t.Errorf("system message 应声明沙箱/权限约束不跳过: %q", msg)
	}

	// 退出强制执行模式 → 空
	h.SetForceExecuteMode(false)
	if msg := h.GetForceExecuteSystemMessage(); msg != "" {
		t.Errorf("退出强制执行模式时应返回空: %q", msg)
	}
}

// TestRefusal_SecurityConstraintTextIsAuxiliary 是 R4（S17 P3）**有意改写**的用例
// （原 TestRefusal_SecurityPolicyClassification，断言"素材文本 → IsSecurityPolicyRefusal==true"）：
// 关键词不再是权威判据，它只是**辅助**信号，因此断言也改成只描述"文本分类"这一层。
//
// 真正判"执行被安全策略拦截"的地方在 internal/tui/stream.go checkAISafety，
// 判据是 **结构化 FailureKind 事件 ∧ 本辅助信号**（合取）。本用例只钉住辅助信号本身，
// 防止有人把它当回判据用（名字已从 IsSecurityPolicyRefusal 改为 MentionsSecurityConstraint）。
//
// ★ 之后本用例的地位**更强了**：它现在是"辅助信号"这一层唯一剩下的守卫 ——
// 与它同族的 `DetectRefusal` 已因误报被整体删除，若有人再把散文匹配加回判据，
// 结构门 `.go` 会拦下。
func TestRefusal_SecurityConstraintTextIsAuxiliary(t *testing.T) {
	h := NewRefusalHandler(true)
	for _, text := range []string{
		"我无法执行，因为当前沙箱启用了网络隔离",
		"I cannot run this because it is blocked by the sandbox security policy",
	} {
		if !h.MentionsSecurityConstraint(text) {
			t.Errorf("这段话确实在谈安全约束，辅助信号应命中: %q", text)
		}
	}
	if h.MentionsSecurityConstraint("我无法决定使用哪种格式") {
		t.Fatal("未谈安全约束的普通拒绝不应命中辅助信号")
	}
	// 辅助信号的**天性**（也是它不能当判据的原因）：任何"提到"沙箱的正常说明都会命中。
	// 这条断言是防回退哨兵——若有人把它重新当成拒绝判据，必须先解释这条怎么办。
	// ★ 的真机反例正是这一类：模型如实说"我没有这个工具、无法调用"就是正常回答。
	if !h.MentionsSecurityConstraint("沙箱当前配置为 workspace-write，因此 curl 会失败；你可以改成显式放行。") {
		t.Fatal("辅助信号本就会命中'提到沙箱'的正常回答；它命中的是措辞，不是事实")
	}
}
