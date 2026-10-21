package bridge

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===== 桶 X3 病灶5 反例测试：bridge 验签失败处理 =====
//
// 本文件覆盖桶 X3 病灶5 的修复反例测试：
//   - 病灶5（bridge 验签失败处理）：runAsyncReconnect 验签失败时
//     应通过 NotifyReconnectFailure 通知 IDE 真实失败状态，
//     而非让 IDE 收到虚假的 success 通知
//
// 测试约束（项目宪法）：
//   - 所有新增 Go 函数必须添加中文函数级注释
//   - 反例测试必须验证修复后的行为（fail-closed / 真实失败通知）

// callbackRecorder 记录 OnReconnectCompleteCallback 的所有调用（桶 X3 病灶5 测试辅助）。
//
// 用于验证 NotifyReconnectFailure 和 finishAsyncReconnect 的回调触发顺序：
//   - 线程安全（mutex 保护 calls 切片）
//   - 记录每次调用的 (success, err) 参数
//   - 提供 WaitN 方法阻塞等待 N 次调用（带超时）
type callbackRecorder struct {
	mu    sync.Mutex
	calls []callbackCall
	done  chan struct{}
}

// callbackCall 记录单次回调调用的参数。
type callbackCall struct {
	success bool
	err     error
}

// newCallbackRecorder 创建回调记录器（桶 X3 病灶5 测试辅助）。
func newCallbackRecorder() *callbackRecorder {
	return &callbackRecorder{
		done: make(chan struct{}),
	}
}

// Record 记录一次回调调用。
func (cr *callbackRecorder) Record(success bool, err error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.calls = append(cr.calls, callbackCall{success: success, err: err})
}

// AsCallback 返回 OnReconnectCompleteCallback 类型的回调函数。
func (cr *callbackRecorder) AsCallback() OnReconnectCompleteCallback {
	return func(success bool, err error) {
		cr.Record(success, err)
		select {
		case cr.done <- struct{}{}:
		default:
		}
	}
}

// Calls 返回所有已记录的回调调用（快照）。
func (cr *callbackRecorder) Calls() []callbackCall {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	result := make([]callbackCall, len(cr.calls))
	copy(result, cr.calls)
	return result
}

// LastCall 返回最后一次回调调用（若无返回 zero value）。
func (cr *callbackRecorder) LastCall() (callbackCall, bool) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if len(cr.calls) == 0 {
		return callbackCall{}, false
	}
	return cr.calls[len(cr.calls)-1], true
}

// WaitCount 阻塞等待至少 N 次回调调用，超时返回 false（桶 X3 病灶5 测试辅助）。
func (cr *callbackRecorder) WaitCount(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cr.mu.Lock()
		count := len(cr.calls)
		cr.mu.Unlock()
		if count >= n {
			return true
		}
		select {
		case <-cr.done:
		case <-time.After(10 * time.Millisecond):
		}
	}
	return false
}

// ===== 病灶5 反例测试 =====

// TestNotifyReconnectFailureX3_NilCallback_NoPanic 验证 onComplete=nil 时
// NotifyReconnectFailure 不 panic（桶 X3 病灶5 反例）。
//
// 反例场景：ReconnectManager 未注入 onComplete 回调 → NotifyReconnectFailure 应安全跳过，
// 而非 nil panic。
//
// 验证：
//   - 不 panic
//   - 无副作用
func TestNotifyReconnectFailureX3_NilCallback_NoPanic(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}
	rm := NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})
	// 不调用 SetOnReconnectComplete → onComplete=nil

	// 应不 panic
	rm.NotifyReconnectFailure(errors.New("verification failed"))
	rm.NotifyReconnectFailure(nil) // nil err 也应不 panic
}

// TestNotifyReconnectFailureX3_WithCallback_TriggersFalse 验证 onComplete 非 nil 时
// NotifyReconnectFailure 触发回调并传入 (false, err)（桶 X3 病灶5 反例）。
//
// 反例场景：新 token 验签失败 → NotifyReconnectFailure 应通知 IDE (false, err)，
// 覆盖之前 finishAsyncReconnect 发送的虚假 (true, nil)。
//
// 验证：
//   - 回调被调用
//   - success=false
//   - err 非 nil 且包含失败原因
func TestNotifyReconnectFailureX3_WithCallback_TriggersFalse(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}
	rm := NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})

	recorder := newCallbackRecorder()
	rm.SetOnReconnectComplete(recorder.AsCallback())

	verifyErr := errors.New("token signature verification failed")
	rm.NotifyReconnectFailure(verifyErr)

	calls := recorder.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 callback call, got %d", len(calls))
	}
	if calls[0].success {
		t.Error("expected success=false on NotifyReconnectFailure")
	}
	if calls[0].err == nil {
		t.Error("expected non-nil err on NotifyReconnectFailure")
	}
	if calls[0].err.Error() != verifyErr.Error() {
		t.Errorf("err = %q, want %q", calls[0].err.Error(), verifyErr.Error())
	}
}

// TestNotifyReconnectFailureX3_NilErr_DefaultMessage 验证 err=nil 时
// NotifyReconnectFailure 使用默认错误消息（桶 X3 病灶5 反例）。
//
// 反例场景：调用方传 nil err → 应使用默认消息而非 panic。
//
// 验证：
//   - 回调被调用
//   - success=false
//   - err 非 nil（使用默认消息）
func TestNotifyReconnectFailureX3_NilErr_DefaultMessage(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}
	rm := NewReconnectManager(config, func() (string, error) {
		return "", errors.New("unused")
	})

	recorder := newCallbackRecorder()
	rm.SetOnReconnectComplete(recorder.AsCallback())

	rm.NotifyReconnectFailure(nil) // nil err → 应使用默认消息

	calls := recorder.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 callback call, got %d", len(calls))
	}
	if calls[0].success {
		t.Error("expected success=false on NotifyReconnectFailure(nil)")
	}
	if calls[0].err == nil {
		t.Error("expected non-nil default err when NotifyReconnectFailure(nil)")
	}
}

// TestRunAsyncReconnectX3_VerificationFail_NotifiesFalse 验证 runAsyncReconnect 在
// 新 token 验签失败时通过 NotifyReconnectFailure 通知 IDE 真实失败状态（桶 X3 病灶5 核心反例）。
//
// 反例场景：
//  1. refreshToken 回调返回一个用错误密钥签名的 JWT（格式正确但验签失败）
//  2. ReconnectOnAuthErrorAsync goroutine 认为 refresh 成功 → 调用 finishAsyncReconnect(true, nil)
//  3. runAsyncReconnect 收到结果后调 tokenMgr.SetToken → 验签失败
//  4. 修复前：仅 return，IDE 收到虚假 success 通知
//  5. 修复后：调 NotifyReconnectFailure(err) → IDE 收到 (false, err) 覆盖虚假 success
//
// 注意：不调用 provider.SetReconnectManager(rm)，因为它会覆盖 rm 的 refresh 函数
// （委托到 scheduler.refreshToken）。直接设置 provider.reconnectMgr 字段保留测试 refresh 函数。
//
// 验证：
//   - 回调至少被调用 2 次（第一次 success=true 虚假通知，第二次 success=false 真实失败）
//   - 最后一次调用的 success=false
//   - 最后一次调用的 err 非 nil 且包含 "verification failed"
func TestRunAsyncReconnectX3_VerificationFail_NotifiesFalse(t *testing.T) {
	// 构造用错误密钥签名的 JWT（格式正确但验签会失败）
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	wrongSecretToken := signJWT(payload, "wrong-secret")

	// ReconnectManager 配置：refreshFn 返回错误密钥签名的 token
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}
	rm := NewReconnectManager(config, func() (string, error) {
		return wrongSecretToken, nil // 返回错误密钥签名的 token（refresh 成功）
	})

	// TokenManager 配置：使用正确密钥验签（token 用错误密钥签名 → 验签失败）
	tokenMgr := NewTokenManagerWithVerify(nil, TokenManagerOptions{
		Secret:           "correct-secret",
		ClockSkewSeconds: 30,
	})
	defer tokenMgr.Stop()

	// 记录回调
	recorder := newCallbackRecorder()
	rm.SetOnReconnectComplete(recorder.AsCallback())

	// 构造 AuthProvider（用于调用 runAsyncReconnect）
	// 直接设置字段（不调 SetReconnectManager），避免覆盖 rm 的 refresh 函数
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	provider.tokenMgr = tokenMgr
	provider.reconnectMgr = rm

	// 启动异步重连
	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed when no inflight reconnect")
	}

	// 在 goroutine 中调用 runAsyncReconnect（它会阻塞等待 channel 结果）
	go provider.runAsyncReconnect(ctx, rm, tokenMgr, "test-source")

	// 等待至少 2 次回调调用（第一次 finishAsyncReconnect 的 true，第二次 NotifyReconnectFailure 的 false）
	// 超时 5s（goroutine 内有退避延迟）
	if !recorder.WaitCount(2, 5*time.Second) {
		calls := recorder.Calls()
		t.Fatalf("expected at least 2 callback calls within 5s, got %d: %+v", len(calls), calls)
	}

	calls := recorder.Calls()
	lastCall, ok := recorder.LastCall()
	if !ok {
		t.Fatal("expected at least one callback call")
	}

	// 验证最后一次调用为 success=false
	if lastCall.success {
		t.Error("last callback should be success=false (verification failure notification)")
	}
	if lastCall.err == nil {
		t.Error("last callback should have non-nil err")
	} else if !strings.Contains(lastCall.err.Error(), "verification failed") {
		t.Errorf("last callback err should contain 'verification failed', got: %q", lastCall.err.Error())
	}

	// 验证至少有 2 次调用（第一次 true 虚假通知 + 第二次 false 真实失败）
	if len(calls) < 2 {
		t.Errorf("expected at least 2 callback calls (false success + real failure), got %d", len(calls))
	}

	_ = ctx
}

// TestRunAsyncReconnectX3_VerificationSuccess_NoExtraFailureNotification 验证 runAsyncReconnect
// 在验签成功时不触发 NotifyReconnectFailure（桶 X3 病灶5 反例对照）。
//
// 反例对照场景：新 token 验签成功 → 不应触发额外的 NotifyReconnectFailure，
// 仅 finishAsyncReconnect 的 (true, nil) 通知应被调用。
//
// 验证：
//   - 回调仅被调用 1 次（finishAsyncReconnect 的 success=true）
//   - 不存在 success=false 的调用
func TestRunAsyncReconnectX3_VerificationSuccess_NoExtraFailureNotification(t *testing.T) {
	// 构造用正确密钥签名的 JWT（验签会成功）
	payload := JwtPayload{
		Sub: "user1",
		Exp: time.Now().Add(1 * time.Hour).Unix(),
	}
	correctSecret := "correct-secret"
	validToken := signJWT(payload, correctSecret)

	// ReconnectManager 配置：refreshFn 返回正确密钥签名的 token
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  1,
	}
	rm := NewReconnectManager(config, func() (string, error) {
		return validToken, nil
	})

	// TokenManager 配置：使用正确密钥验签
	tokenMgr := NewTokenManagerWithVerify(nil, TokenManagerOptions{
		Secret:           correctSecret,
		ClockSkewSeconds: 30,
	})
	defer tokenMgr.Stop()

	// 记录回调
	recorder := newCallbackRecorder()
	rm.SetOnReconnectComplete(recorder.AsCallback())

	// 构造 AuthProvider
	// 直接设置字段（不调 SetReconnectManager），避免覆盖 rm 的 refresh 函数
	provider := NewAuthProvider(DefaultAuthProviderConfig())
	provider.tokenMgr = tokenMgr
	provider.reconnectMgr = rm

	// 启动异步重连
	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}

	// 在 goroutine 中调用 runAsyncReconnect
	go provider.runAsyncReconnect(ctx, rm, tokenMgr, "test-source")

	// 等待至少 1 次回调调用
	if !recorder.WaitCount(1, 3*time.Second) {
		calls := recorder.Calls()
		t.Fatalf("expected at least 1 callback call within 3s, got %d", len(calls))
	}

	// 等待额外 200ms 确保没有第二次调用（验签成功不应触发 NotifyReconnectFailure）
	time.Sleep(200 * time.Millisecond)

	calls := recorder.Calls()
	// 验证仅 1 次调用（finishAsyncReconnect 的 success=true）
	if len(calls) != 1 {
		t.Errorf("expected exactly 1 callback call on verification success, got %d: %+v", len(calls), calls)
	}
	if len(calls) > 0 && !calls[0].success {
		t.Error("first callback should be success=true on verification success")
	}

	_ = ctx
}
