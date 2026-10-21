package bridge

import (
	"testing"
	"time"
)

// TestJtiCache_FirstUse 验证首次 jti 返回 false（非重放）。
func TestJtiCache_FirstUse(t *testing.T) {
	cache := NewJtiCache()
	exp := time.Now().Add(1 * time.Hour).Unix()
	if cache.IsReplayed("jti-001", exp) {
		t.Error("first use of jti-001 should return false (not replayed)")
	}
	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}
}

// TestJtiCache_Replay 验证重复 jti 返回 true（重放检测）。
func TestJtiCache_Replay(t *testing.T) {
	cache := NewJtiCache()
	exp := time.Now().Add(1 * time.Hour).Unix()
	cache.IsReplayed("jti-002", exp)
	if !cache.IsReplayed("jti-002", exp) {
		t.Error("duplicate jti-002 should return true (replayed)")
	}
}

// TestJtiCache_ExpiredCleanup 验证过期 jti 被惰性清理。
func TestJtiCache_ExpiredCleanup(t *testing.T) {
	cache := NewJtiCache()
	// 已过期的 exp
	pastExp := time.Now().Add(-1 * time.Hour).Unix()
	cache.IsReplayed("jti-expired", pastExp)
	// 过期条目应在下次调用时被清理
	if cache.Size() != 1 {
		t.Logf("Size before cleanup = %d (expired entry still present)", cache.Size())
	}
	// 再次调用触发惰性清理
	exp := time.Now().Add(1 * time.Hour).Unix()
	cache.IsReplayed("jti-new", exp)
	// 过期条目应被清理，只剩 jti-new
	if cache.Size() != 1 {
		t.Errorf("Size after cleanup = %d, want 1 (expired entry should be cleaned)", cache.Size())
	}
	// 过期的 jti-expired 可以重新使用（非重放）
	if cache.IsReplayed("jti-expired", exp) {
		t.Error("expired jti-002 should be reusable (not replayed)")
	}
}

// TestJtiCache_EmptyJti 验证空 jti 不触发重放检测。
func TestJtiCache_EmptyJti(t *testing.T) {
	cache := NewJtiCache()
	exp := time.Now().Add(1 * time.Hour).Unix()
	// 空 jti 不应被记录（VerifyJWT 中 payload.Jti != "" 才检查）
	// 但 JtiCache 本身不拦截空 jti，由 VerifyJWT 控制
	if cache.IsReplayed("", exp) {
		t.Error("empty jti should not be considered replayed on first use")
	}
}

// TestJtiCache_PermanentEntry 验证 exp <= 0 视为永久（不清理）。
func TestJtiCache_PermanentEntry(t *testing.T) {
	cache := NewJtiCache()
	// exp = 0 视为永久
	cache.IsReplayed("jti-permanent", 0)
	if !cache.IsReplayed("jti-permanent", 0) {
		t.Error("permanent jti (exp=0) should be replayed on second use")
	}
}

// TestJtiCache_ConcurrentSafe 验证并发调用不崩溃（基础并发安全）。
func TestJtiCache_ConcurrentSafe(t *testing.T) {
	cache := NewJtiCache()
	exp := time.Now().Add(1 * time.Hour).Unix()
	done := make(chan struct{})

	// 并发写入不同 jti
	go func() {
		for i := 0; i < 100; i++ {
			cache.IsReplayed("jti-concurrent", exp)
		}
		close(done)
	}()

	for i := 0; i < 100; i++ {
		cache.IsReplayed("jti-other", exp)
	}
	<-done
	// 不崩溃即通过
}
