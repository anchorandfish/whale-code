// pattern_ttl_scanner_test.go — H12 Pattern TTL 扫描器反例测试（TDD 先行，§2.4）。
// 覆盖：
// - 30 天 TTL：unverified → deprecated
// - 60 天 TTL：provisionally_verified → unverified
// - 90 天 TTL：verified → provisionally_verified
// - 终态：deprecated 不流转
// - 反例：Pitfall 不被误删（永久保留）
// - 反例：LastUsedAt 空不触发 TTL
// - 反例：experience 为 nil 降级
//
// 设计说明 §2.4；落地：30/60/90 天 TTL + Pitfall 永久保留
// SNAPSHOT: tests={Unverified_30Days_ToDeprecated, ProvisionallyVerified_60Days_ToUnverified, Verified_90Days_ToProvisionallyVerified, Deprecated_NoTransition, Pitfall_NotScanned, EmptyLastUsedAt_NoOp, NilExperience_Degrade, CtxCancelled_ReturnsErr}
package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestPatternTTLScanner_Unverified_30Days_ToDeprecated unverified 30 天未用 → deprecated（§2.4）。
func TestPatternTTLScanner_Unverified_30Days_ToDeprecated(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	// 注入 unverified pattern，LastUsedAt=31 天前
	oldTime := now.Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("ttl-pid-1", "test sig", ScopeProject, VerifiedStateUnverified)
	p.LastUsedAt = oldTime
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got, err := s.Get("ttl-pid-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateDeprecated)
	}
}

// TestPatternTTLScanner_Unverified_29Days_NoTransition unverified 29 天未用不流转。
func TestPatternTTLScanner_Unverified_29Days_NoTransition(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	oldTime := now.Add(-29 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("ttl-pid-1b", "test sig", ScopeProject, VerifiedStateUnverified)
	p.LastUsedAt = oldTime
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got, err := s.Get("ttl-pid-1b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("VerifiedState = %q, want %q (29 days should not transition)", got.VerifiedState, VerifiedStateUnverified)
	}
}

// TestPatternTTLScanner_ProvisionallyVerified_60Days_ToUnverified provisionally_verified 60 天未用 → unverified（§2.4）。
func TestPatternTTLScanner_ProvisionallyVerified_60Days_ToUnverified(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	oldTime := now.Add(-61 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("ttl-pid-2", "test sig", ScopeProject, VerifiedStateProvisionallyVerified)
	p.LastUsedAt = oldTime
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got, err := s.Get("ttl-pid-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateUnverified)
	}
}

// TestPatternTTLScanner_Verified_90Days_ToProvisionallyVerified verified 90 天未用 → provisionally_verified（§2.4）。
func TestPatternTTLScanner_Verified_90Days_ToProvisionallyVerified(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	oldTime := now.Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("ttl-pid-3", "test sig", ScopeProject, VerifiedStateVerified)
	p.LastUsedAt = oldTime
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got, err := s.Get("ttl-pid-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateProvisionallyVerified {
		t.Errorf("VerifiedState = %q, want %q", got.VerifiedState, VerifiedStateProvisionallyVerified)
	}
}

// TestPatternTTLScanner_Deprecated_NoTransition deprecated 终态不流转（§2.4）。
func TestPatternTTLScanner_Deprecated_NoTransition(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	// deprecated + 超长未用（300 天）→ 应不流转
	oldTime := now.Add(-300 * 24 * time.Hour).UTC().Format(time.RFC3339)
	p := makeTestPattern("ttl-pid-4", "test sig", ScopeProject, VerifiedStateDeprecated)
	p.LastUsedAt = oldTime
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got, err := s.Get("ttl-pid-4")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("deprecated terminal state should not transition, got %q", got.VerifiedState)
	}
}

// TestPatternTTLScanner_Pitfall_NotScanned 反例：Pitfall 不被误删（永久保留，§2.4）。
func TestPatternTTLScanner_Pitfall_NotScanned(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	// 注入一个 Pitfall，LastSeenAt=300 天前
	pitfall := &Pitfall{
		PitfallID:     "pitfall-ttl-1",
		TaskSignature: "test pitfall sig",
		Symptom:       "test symptom",
		RootCause:     "test root cause",
		Mitigation:    "test mitigation",
		Occurrences:   1,
		LastSeenAt:    now.Add(-300 * 24 * time.Hour).UTC().Format(time.RFC3339),
		Scope:         ScopeProject,
	}
	if err := s.SavePitfall(pitfall); err != nil {
		t.Fatalf("SavePitfall: %v", err)
	}

	// 跑 Scan
	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// 验证：Pitfall 仍然存在
	pitfalls, err := s.ListPitfallsByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListPitfallsByScope: %v", err)
	}
	if len(pitfalls) != 1 {
		t.Errorf("pitfall should be preserved, got %d pitfalls", len(pitfalls))
	}
	if pitfalls[0].PitfallID != "pitfall-ttl-1" {
		t.Errorf("pitfall ID mismatch: got %q, want %q", pitfalls[0].PitfallID, "pitfall-ttl-1")
	}
}

// TestPatternTTLScanner_EmptyLastUsedAt_NoOp 反例：LastUsedAt 空不触发 TTL。
func TestPatternTTLScanner_EmptyLastUsedAt_NoOp(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	// LastUsedAt 空（视为从未使用，避免误淘汰新 Pattern）
	p := makeTestPattern("ttl-pid-5", "test sig", ScopeProject, VerifiedStateUnverified)
	p.LastUsedAt = ""
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got, err := s.Get("ttl-pid-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("empty LastUsedAt should not trigger TTL, got %q", got.VerifiedState)
	}
}

// TestPatternTTLScanner_NilExperience_Degrade experience 为 nil 降级。
func TestPatternTTLScanner_NilExperience_Degrade(t *testing.T) {
	scanner := &PatternTTLScanner{
		experience: nil,
		nowFunc:    time.Now,
	}
	if err := scanner.Scan(context.Background()); err != nil {
		t.Errorf("nil experience should degrade silently, got %v", err)
	}
}

// TestPatternTTLScanner_CtxCancelled_ReturnsErr ctx 取消返回 ctx.Err()。
func TestPatternTTLScanner_CtxCancelled_ReturnsErr(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanner.Scan(ctx); err == nil {
		t.Errorf("cancelled ctx should return ctx.Err()")
	}
}

// TestPatternTTLScanner_MultipleScopes_MultiScan global + project 双 scope 都扫描。
func TestPatternTTLScanner_MultipleScopes_MultiScan(t *testing.T) {
	s := newTestExperienceStore(t)
	scanner := NewPatternTTLScanner(s)

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	scanner.SetNowFunc(func() time.Time { return now })

	oldTime := now.Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339)

	// project scope
	p1 := makeTestPattern("ttl-multi-1", "project sig", ScopeProject, VerifiedStateUnverified)
	p1.LastUsedAt = oldTime
	if err := s.Save(p1); err != nil {
		t.Fatalf("Save p1: %v", err)
	}
	// global scope
	p2 := makeTestPattern("ttl-multi-2", "global sig", ScopeGlobal, VerifiedStateUnverified)
	p2.LastUsedAt = oldTime
	if err := s.Save(p2); err != nil {
		t.Fatalf("Save p2: %v", err)
	}

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// 两个 scope 都应被流转
	g1, err := s.Get("ttl-multi-1")
	if err != nil {
		t.Fatalf("Get g1: %v", err)
	}
	if g1.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("project pattern VerifiedState = %q, want %q", g1.VerifiedState, VerifiedStateDeprecated)
	}
	g2, err := s.Get("ttl-multi-2")
	if err != nil {
		t.Fatalf("Get g2: %v", err)
	}
	if g2.VerifiedState != VerifiedStateDeprecated {
		t.Errorf("global pattern VerifiedState = %q, want %q", g2.VerifiedState, VerifiedStateDeprecated)
	}
}
