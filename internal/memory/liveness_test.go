// liveness_test.go — P2 有效性判定唯一权威的判别器。
//
// 判别目标：LiveAt 必须对「作废」与「过期」两种失效都给 false，对正常给 true。
// 现状（接线前）读路径根本不做这层判定（P1 判别器 I3 已证明 contradicted 会被返回），
// 所以本判据是新增行为的判据，不是对既有行为的确认。
package memory

import (
	"testing"
	"time"
)

func TestP2_LiveAt(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	t.Run("nil_entry_is_not_live", func(t *testing.T) {
		var e *MemoryEntry
		if e.LiveAt(now) {
			t.Fatal("nil entry 应判 false（防御：调用方不应因此 panic 或误判存活）")
		}
	})

	t.Run("normal_entry_is_live", func(t *testing.T) {
		e := &MemoryEntry{Name: "a", Verification: VerificationVerified, ValidUntil: nil}
		if !e.LiveAt(now) {
			t.Fatal("verified 且无过期时间 ⇒ 应存活")
		}
	})

	t.Run("empty_verification_treated_as_unverified_still_live", func(t *testing.T) {
		e := &MemoryEntry{Name: "a", Verification: ""}
		if !e.LiveAt(now) {
			t.Fatal("空 verification 按 unverified 处理，不应判失效")
		}
	})

	t.Run("contradicted_is_not_live", func(t *testing.T) {
		e := &MemoryEntry{Name: "a", Verification: VerificationContradicted}
		if e.LiveAt(now) {
			t.Fatal("contradicted 是终态失效 ⇒ 应判 false")
		}
	})

	t.Run("expired_valid_until_is_not_live", func(t *testing.T) {
		vu := past
		e := &MemoryEntry{Name: "a", Verification: VerificationUnverified, ValidUntil: &vu}
		if e.LiveAt(now) {
			t.Fatal("valid_until 已过 ⇒ 应判 false")
		}
	})

	t.Run("future_valid_until_is_live", func(t *testing.T) {
		vu := future
		e := &MemoryEntry{Name: "a", Verification: VerificationUnverified, ValidUntil: &vu}
		if !e.LiveAt(now) {
			t.Fatal("valid_until 未到 ⇒ 应存活")
		}
	})

	t.Run("valid_until_exactly_now_is_not_live", func(t *testing.T) {
		vu := now
		e := &MemoryEntry{Name: "a", Verification: VerificationUnverified, ValidUntil: &vu}
		if e.LiveAt(now) {
			t.Fatal("valid_until == now（闭区间右端）⇒ 应判失效")
		}
	})
}
