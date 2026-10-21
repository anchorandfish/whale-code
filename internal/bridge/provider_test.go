package bridge

import (
	"testing"
	"time"
)

// TestAuthProvider_SetToken test setup Token
// P0-13 修复：fail-closed 后必须注入 TokenManager（测试用 NewTokenManager 不验签，模拟生产接线）
func TestAuthProvider_SetToken(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	// P0-13: 注入 TokenManager（测试用不验签版，避免 fail-closed 拒绝）
	provider.SetTokenManager(NewTokenManager(nil))

	token := createTestJWTExpiringIn(1 * time.Hour)
	notif, err := provider.SetToken(token, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !notif.IsValid {
		t.Error("expected token to be valid")
	}
	if notif.State != "valid" {
		t.Errorf("expected state=valid, got %s", notif.State)
	}
	if notif.Source != "test" {
		t.Errorf("expected source=test, got %s", notif.Source)
	}
	if notif.ExpiresIn <= 0 {
		t.Errorf("expected positive ExpiresIn, got %d", notif.ExpiresIn)
	}
}

// TestAuthProvider_GetStatus 测试获取 Token 状态
// P0-13 修复：fail-closed 后必须注入 TokenManager
func TestAuthProvider_GetStatus(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	// P0-13: 注入 TokenManager（测试用不验签版）
	provider.SetTokenManager(NewTokenManager(nil))

	token := createTestJWTExpiringIn(1 * time.Hour)
	provider.SetToken(token, "vscode")

	notif := provider.GetStatus()
	if notif == nil {
		t.Fatal("expected non-nil notification")
	}

	if !notif.IsValid {
		t.Error("expected token to be valid")
	}
	if notif.Source != "vscode" {
		t.Errorf("expected source=vscode, got %s", notif.Source)
	}
}

// TestAuthProvider_RefreshToken 测试手动刷新 Token
// P0-13 修复：fail-closed 后必须注入 TokenManager
func TestAuthProvider_RefreshToken(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	// P0-13: 注入 TokenManager（测试用不验签版）
	provider.SetTokenManager(NewTokenManager(nil))

	// 先设置一个 token
	token := createTestJWTExpiringIn(1 * time.Hour)
	provider.SetToken(token, "test")

	// 设置刷新函数
	newToken := createTestJWTExpiringIn(2 * time.Hour)
	provider.SetRefreshFunction(func() (string, error) {
		return newToken, nil
	})

	// 手动刷新
	notif, err := provider.RefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !notif.IsValid {
		t.Error("expected refreshed token to be valid")
	}
}

// TestAuthProvider_SetToken_InvalidToken test setup无效 Token
// P0-13 修复：fail-closed 后注入 TokenManager 以测试实际的 token 解析错误路径
func TestAuthProvider_SetToken_InvalidToken(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	// P0-13: 注入 TokenManager（测试用不验签版，使无效 token 走 DecodeJWTPayload 错误路径）
	provider.SetTokenManager(NewTokenManager(nil))

	tests := []struct {
		name  string
		token string
	}{
		{"empty token", ""},
		{"malformed token", "not-a-jwt"},
		{"two parts only", "part1.part2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.SetToken(tt.token, "test")
			if err == nil {
				t.Error("expected error for invalid token")
			}
		})
	}
}

// TestAuthProvider_GetStatus_NoToken 测试无 Token 时获取状态
func TestAuthProvider_GetStatus_NoToken(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())

	notif := provider.GetStatus()
	if notif == nil {
		t.Fatal("expected non-nil notification")
	}

	if notif.IsValid {
		t.Error("expected token to be invalid when no token is set")
	}
	if notif.State != "none" {
		t.Errorf("expected state=none, got %s", notif.State)
	}
	if notif.ExpiresIn != -1 {
		t.Errorf("expected ExpiresIn=-1, got %d", notif.ExpiresIn)
	}
}

// ---- T1-P1-A1 Provider 层异步重连测试 ----

// TestAuthProvider_SetOnReconnectComplete_Forwarded 验证 SetOnReconnectComplete 转发到 reconnectMgr。
// 场景：SetReconnectManager 后调 SetOnReconnectComplete，回调应转发到 reconnectMgr.onComplete。
func TestAuthProvider_SetOnReconnectComplete_Forwarded(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	reconnectMgr := NewReconnectManager(ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  3,
	}, func() (string, error) {
		return "token", nil
	})
	provider.SetReconnectManager(reconnectMgr)

	callbackFired := make(chan struct{}, 1)
	provider.SetOnReconnectComplete(func(success bool, err error) {
		callbackFired <- struct{}{}
	})

	// 触发异步重连以验证回调被转发
	ctx, ok := reconnectMgr.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := reconnectMgr.ReconnectOnAuthErrorAsync(ctx)
	<-resultCh

	select {
	case <-callbackFired:
		// 回调被转发并触发
	case <-time.After(2 * time.Second):
		t.Fatal("callback not forwarded to reconnectMgr within 2s")
	}
}

// TestAuthProvider_SetReconnectManager_InjectsBackoffStrategy 验证 SetReconnectManager 注入 BackoffStrategy。
// 场景：SetReconnectManager 后 reconnectMgr.calculateDelayLocked 应使用注入的 BackoffStrategy。
func TestAuthProvider_SetReconnectManager_InjectsBackoffStrategy(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	reconnectMgr := NewReconnectManager(ReconnectConfig{
		BaseDelay:    1 * time.Second, // config 公式 attempt=1 返回 ~1s
		MaxDelay:     30 * time.Second,
		Factor:       2.0,
		JitterFactor: 0.0, // 关闭抖动以便验证固定值
		MaxAttempts:  5,
	}, nil)
	provider.SetReconnectManager(reconnectMgr)

	// SetReconnectManager 应注入 ExponentialBackoff（base=1s, factor=2, jitter=0）
	// attempt=1 时 NextDelay 应返回 base=1s（无抖动）
	delay := reconnectMgr.calculateDelayLocked(1)
	if delay != 1*time.Second {
		t.Errorf("delay = %v, want 1s (from injected ExponentialBackoff, jitter=0)", delay)
	}
}

// TestAuthProvider_SetOnReconnectComplete_BeforeReconnectMgr 验证先注入回调后注入 reconnectMgr 也能转发。
// 场景：SetOnReconnectComplete 在 SetReconnectManager 之前调用，回调应在 SetReconnectManager 时转发。
func TestAuthProvider_SetOnReconnectComplete_BeforeReconnectMgr(t *testing.T) {
	provider := NewAuthProvider(DefaultAuthProviderConfig())

	callbackFired := make(chan struct{}, 1)
	provider.SetOnReconnectComplete(func(success bool, err error) {
		callbackFired <- struct{}{}
	})

	// 此时 reconnectMgr 为 nil，回调仅存储在 provider.onReconnectComplete
	reconnectMgr := NewReconnectManager(ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  3,
	}, func() (string, error) {
		return "token", nil
	})
	provider.SetReconnectManager(reconnectMgr) // 应转发已存储的回调

	// 触发异步重连以验证回调被转发
	ctx, ok := reconnectMgr.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := reconnectMgr.ReconnectOnAuthErrorAsync(ctx)
	<-resultCh

	select {
	case <-callbackFired:
		// 回调被转发并触发
	case <-time.After(2 * time.Second):
		t.Fatal("callback not forwarded during SetReconnectManager within 2s")
	}
}
