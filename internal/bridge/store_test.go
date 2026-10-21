package bridge

import (
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// osStat 和 runtimeGOOS 是 os.Stat 和 runtime.GOOS 的别名，供测试引用。
var (
	osStat      = os.Stat
	runtimeGOOS = runtime.GOOS
)

// TestTokenInfo_Creation 测试 TokenInfo 的创建
func TestTokenInfo_Creation(t *testing.T) {
	now := time.Now()
	expiry := now.Add(1 * time.Hour)

	info := &TokenInfo{
		RawToken:   "test-token",
		Payload:    &JwtPayload{Sub: "user1", Exp: expiry.Unix()},
		ObtainedAt: now,
		ExpiresAt:  expiry,
		Source:     "test",
	}

	if info.RawToken != "test-token" {
		t.Errorf("expected RawToken=test-token, got %s", info.RawToken)
	}
	if info.Payload.Sub != "user1" {
		t.Errorf("expected Payload.Sub=user1, got %s", info.Payload.Sub)
	}
	if info.Source != "test" {
		t.Errorf("expected Source=test, got %s", info.Source)
	}
}

// TestTokenInfo_IsExpired 测试 TokenInfo 的过期判断
func TestTokenInfo_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		expected  bool
	}{
		{"expired token", time.Now().Add(-1 * time.Hour), true},
		{"valid token", time.Now().Add(1 * time.Hour), false},
		{"zero expiry (no expiry)", time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &TokenInfo{ExpiresAt: tt.expiresAt}
			if info.IsExpired() != tt.expected {
				t.Errorf("IsExpired() = %v, want %v", info.IsExpired(), tt.expected)
			}
		})
	}
}

// TestTokenInfo_IsExpiringSoon 测试 TokenInfo 的即将过期判断
func TestTokenInfo_IsExpiringSoon(t *testing.T) {
	buffer := 5 * time.Minute

	tests := []struct {
		name      string
		expiresAt time.Time
		expected  bool
	}{
		{"expiring within buffer", time.Now().Add(3 * time.Minute), true},
		{"not expiring within buffer", time.Now().Add(10 * time.Minute), false},
		{"already expired", time.Now().Add(-1 * time.Minute), true},
		{"zero expiry (no expiry)", time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &TokenInfo{ExpiresAt: tt.expiresAt}
			if info.IsExpiringSoon(buffer) != tt.expected {
				t.Errorf("IsExpiringSoon(%v) = %v, want %v", buffer, info.IsExpiringSoon(buffer), tt.expected)
			}
		})
	}
}

// TestTokenStore_SetAndGet 测试 TokenStore 的设置和获取
func TestTokenStore_SetAndGet(t *testing.T) {
	store := NewTokenStore()

	info := &TokenInfo{
		RawToken:   "test-token",
		ObtainedAt: time.Now(),
		ExpiresAt:  time.Now().Add(1 * time.Hour),
		Source:     "test",
	}

	store.Set(info)
	got := store.Get()

	if got == nil {
		t.Fatal("expected non-nil TokenInfo")
	}
	if got.RawToken != "test-token" {
		t.Errorf("expected RawToken=test-token, got %s", got.RawToken)
	}
}

// TestTokenStore_Clear 测试 TokenStore 的清除
func TestTokenStore_Clear(t *testing.T) {
	store := NewTokenStore()

	info := &TokenInfo{
		RawToken:   "test-token",
		ObtainedAt: time.Now(),
		Source:     "test",
	}
	store.Set(info)
	store.Clear()

	if got := store.Get(); got != nil {
		t.Error("expected nil after Clear()")
	}
}

// TestTokenStore_GetWhenEmpty 测试从空存储获取 Token
func TestTokenStore_GetWhenEmpty(t *testing.T) {
	store := NewTokenStore()

	if got := store.Get(); got != nil {
		t.Error("expected nil from empty store")
	}
}

// TestTokenStore_UpdateToken 测试更新存储中的 Token
func TestTokenStore_UpdateToken(t *testing.T) {
	store := NewTokenStore()

	info1 := &TokenInfo{
		RawToken:   "token-v1",
		ObtainedAt: time.Now(),
		Source:     "test",
	}
	store.Set(info1)

	info2 := &TokenInfo{
		RawToken:   "token-v2",
		ObtainedAt: time.Now(),
		Source:     "test",
	}
	store.Set(info2)

	got := store.Get()
	if got.RawToken != "token-v2" {
		t.Errorf("expected RawToken=token-v2, got %s", got.RawToken)
	}
}

// TestTokenStore_ConcurrentAccess 测试 TokenStore 的并发访问security
func TestTokenStore_ConcurrentAccess(t *testing.T) {
	store := NewTokenStore()
	var wg sync.WaitGroup

	// 并发写入
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			info := &TokenInfo{
				RawToken:   "concurrent-token",
				ObtainedAt: time.Now(),
				Source:     "concurrent-test",
			}
			store.Set(info)
		}(i)
	}

	// 并发读取
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.Get()
		}()
	}

	wg.Wait()

	// 验证最终状态有效
	got := store.Get()
	if got == nil {
		t.Error("expected non-nil TokenInfo after concurrent access")
	}
}

// TestFileStore_Permission0600 验证 FileStore.Save 写出的文件权限为 0600（P0-4，协同 H3）。
// Unix 系统检查 os.FileMode 0600；Windows 系统权限模型不同，仅验证文件可读写不报错。
func TestFileStore_Permission0600(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := tmpDir + "/test_token.json"

	fs := NewFileStore(filePath)
	data := []byte(`{"token":"test"}`)
	if err := fs.Save(data); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 读取文件信息检查权限
	info, err := osStat(filePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	// Unix 权限检查：0600（owner rw，group/other 无权限）
	if runtimeGOOS != "windows" {
		mode := info.Mode().Perm()
		if mode != 0600 {
			t.Errorf("file permission = %o, want 0600", mode)
		}
	}

	// 验证文件内容正确
	loaded, err := fs.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(loaded) != string(data) {
		t.Errorf("loaded content = %s, want %s", string(loaded), string(data))
	}
}
