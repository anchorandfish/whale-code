// verification_test.go — H11/N27 Phase A A3 verification 流转矩阵测试（含终态反例）。
package memory

import (
	"errors"
	"testing"
	"time"
)

// TestTransitionVerification_UnverifiedToVerified unverified→verified 合法流转，ValidFrom 首次设置。
func TestTransitionVerification_UnverifiedToVerified(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("to-verify", VerificationUnverified)
	// 清空 ValidFrom 测试首次设置
	entry.ValidFrom = nil
	RegisterCapabilityForTest("to-verify", entry)

	if err := TransitionVerification("to-verify", VerificationVerified, "probe success"); err != nil {
		t.Fatalf("unverified→verified should succeed, got: %v", err)
	}
	if entry.Verification != VerificationVerified {
		t.Errorf("Verification = %q, expected verified", entry.Verification)
	}
	if entry.ValidFrom == nil {
		t.Error("ValidFrom should be set on first transition to verified")
	}
}

// TestTransitionVerification_UnverifiedToContradicted unverified→contradicted 合法流转，ValidUntil 设置。
func TestTransitionVerification_UnverifiedToContradicted(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("to-contradict", VerificationUnverified)
	RegisterCapabilityForTest("to-contradict", entry)

	if err := TransitionVerification("to-contradict", VerificationContradicted, "3 probe fails"); err != nil {
		t.Fatalf("unverified→contradicted should succeed, got: %v", err)
	}
	if entry.Verification != VerificationContradicted {
		t.Errorf("Verification = %q, expected contradicted", entry.Verification)
	}
	if entry.ValidUntil == nil {
		t.Error("ValidUntil should be set on transition to contradicted")
	}
}

// TestTransitionVerification_VerifiedToContradicted verified→contradicted 合法流转，ValidUntil 设置。
func TestTransitionVerification_VerifiedToContradicted(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("verified-to-contradict", VerificationVerified)
	RegisterCapabilityForTest("verified-to-contradict", entry)

	if err := TransitionVerification("verified-to-contradict", VerificationContradicted, "user invalidate"); err != nil {
		t.Fatalf("verified→contradicted should succeed, got: %v", err)
	}
	if entry.Verification != VerificationContradicted {
		t.Errorf("Verification = %q, expected contradicted", entry.Verification)
	}
	if entry.ValidUntil == nil {
		t.Error("ValidUntil should be set on transition to contradicted")
	}
}

// TestTransitionVerification_ContradictedToVerified_Rejected contradicted→verified 终态不可流转。
// 致命反例：contradicted 是终态，不可流转（防失效能力被错误恢复为可用）。
func TestTransitionVerification_ContradictedToVerified_Rejected(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("terminal-state", VerificationContradicted)
	RegisterCapabilityForTest("terminal-state", entry)

	err := TransitionVerification("terminal-state", VerificationVerified, "attempt revive")
	if err == nil {
		t.Fatal("contradicted→verified should be rejected (terminal state)")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("should return ErrInvalidTransition, got: %v", err)
	}
	// verification 不应被修改
	if entry.Verification != VerificationContradicted {
		t.Errorf("Verification = %q, should remain contradicted", entry.Verification)
	}
}

// TestTransitionVerification_ContradictedToUnverified_Rejected contradicted→unverified 终态不可流转。
// 致命反例：contradicted 是终态，不可流转。
func TestTransitionVerification_ContradictedToUnverified_Rejected(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("terminal-2", VerificationContradicted)
	RegisterCapabilityForTest("terminal-2", entry)

	err := TransitionVerification("terminal-2", VerificationUnverified, "attempt decay")
	if err == nil {
		t.Fatal("contradicted→unverified should be rejected (terminal state)")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("should return ErrInvalidTransition, got: %v", err)
	}
}

// TestTransitionVerification_VerifiedToUnverified_Rejected verified→unverified 公共 API 拒绝。
// 致命反例：verified→unverified 仅 ApplyForgetting 内部通道触发，公共 API 必须拒绝。
func TestTransitionVerification_VerifiedToUnverified_Rejected(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("verified-only", VerificationVerified)
	RegisterCapabilityForTest("verified-only", entry)

	err := TransitionVerification("verified-only", VerificationUnverified, "attempt public decay")
	if err == nil {
		t.Fatal("verified→unverified should be rejected by public API (only ApplyForgetting internal)")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("should return ErrInvalidTransition, got: %v", err)
	}
	// verification 不应被修改
	if entry.Verification != VerificationVerified {
		t.Errorf("Verification = %q, should remain verified", entry.Verification)
	}
}

// TestTransitionVerification_VerifiedToVerified_Rejected verified→verified（非法），返回 error。
// 同时验证已存在的 ValidFrom 不被覆盖。
func TestTransitionVerification_VerifiedToVerified_NoValidFromOverwrite(t *testing.T) {
	ResetCapabilityRegistryForTest()
	originalValidFrom := mustParseTime(t, "2026-01-01T00:00:00Z")
	entry := &MemoryEntry{
		Name:         "already-verified",
		Verification: VerificationVerified,
		ValidFrom:    &originalValidFrom,
	}
	RegisterCapabilityForTest("already-verified", entry)

	err := TransitionVerification("already-verified", VerificationVerified, "redundant")
	if err == nil {
		t.Fatal("verified→verified should be rejected (invalid transition)")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("should return ErrInvalidTransition, got: %v", err)
	}
	// ValidFrom 不应被覆盖
	if entry.ValidFrom == nil {
		t.Fatal("ValidFrom should not be nil")
	}
	if !entry.ValidFrom.Equal(originalValidFrom) {
		t.Errorf("ValidFrom was overwritten: original=%v, now=%v", originalValidFrom, *entry.ValidFrom)
	}
}

// TestTransitionVerification_NotFound name 不存在返回 error。
func TestTransitionVerification_NotFound(t *testing.T) {
	ResetCapabilityRegistryForTest()

	err := TransitionVerification("nonexistent", VerificationVerified, "test")
	if err == nil {
		t.Fatal("nonexistent name should return error")
	}
	if !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("should return ErrEntryNotFound, got: %v", err)
	}
}

// TestIsValidTransition_AllPairs 遍历 3×3 流转矩阵，验证 9 种组合的合法性。
// 流转矩阵：
//
//	from \ to | verified | unverified | contradicted
//	-----------|---------|------------|-------------
//	unverified | ✅ | - | ✅
//	verified | - | -（仅内部） | ✅
//	contradicted| ❌ | ❌ | ❌
func TestIsValidTransition_AllPairs(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		// unverified →
		{"unverified→verified", VerificationUnverified, VerificationVerified, true},
		{"unverified→unverified", VerificationUnverified, VerificationUnverified, false},
		{"unverified→contradicted", VerificationUnverified, VerificationContradicted, true},
		// verified →
		{"verified→verified", VerificationVerified, VerificationVerified, false},
		{"verified→unverified", VerificationVerified, VerificationUnverified, false}, // 公共 API 拒绝（仅内部）
		{"verified→contradicted", VerificationVerified, VerificationContradicted, true},
		// contradicted →（终态）
		{"contradicted→verified", VerificationContradicted, VerificationVerified, false},
		{"contradicted→unverified", VerificationContradicted, VerificationUnverified, false},
		{"contradicted→contradicted", VerificationContradicted, VerificationContradicted, false},
		// 未知状态
		{"unknown→verified", "unknown", VerificationVerified, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidTransition(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("isValidTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

// TestTransitionVerification_ReasonLogged reason 参数被接收但不影响逻辑（仅用于日志）。
// 反例防护：reason 为空字符串也应正常流转。
func TestTransitionVerification_ReasonLogged(t *testing.T) {
	ResetCapabilityRegistryForTest()
	entry := newTestCapabilityEntry("reason-test", VerificationUnverified)
	RegisterCapabilityForTest("reason-test", entry)

	// reason 为空字符串
	if err := TransitionVerification("reason-test", VerificationVerified, ""); err != nil {
		t.Fatalf("empty reason should not cause error: %v", err)
	}
	if entry.Verification != VerificationVerified {
		t.Errorf("Verification = %q, expected verified", entry.Verification)
	}
}

// TestTransitionVerification_UpdatedAtRefreshed 流转后 UpdatedAt 应刷新。
func TestTransitionVerification_UpdatedAtRefreshed(t *testing.T) {
	ResetCapabilityRegistryForTest()
	oldUpdated := mustParseTime(t, "2020-01-01T00:00:00Z")
	entry := &MemoryEntry{
		Name:         "update-test",
		Verification: VerificationUnverified,
		UpdatedAt:    oldUpdated,
	}
	RegisterCapabilityForTest("update-test", entry)

	if err := TransitionVerification("update-test", VerificationVerified, "test"); err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	if entry.UpdatedAt.Equal(oldUpdated) {
		t.Error("UpdatedAt should be refreshed after transition")
	}
}

// mustParseTime 解析 RFC3339 时间字符串，失败时 t.Fatal。
func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return v
}
