package bridge

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// TestReconnectOnAuthError_Success 验证重连成功返回新 token（v2 R5 b）。
func TestReconnectOnAuthError_Success(t *testing.T) {
	newToken := "refreshed-token"
	callCount := 0
	refreshFn := func() (string, error) {
		callCount++
		return newToken, nil
	}

	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond, // 测试用极短延迟
		MaxDelay:     10 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, refreshFn)

	result, err := rm.ReconnectOnAuthError()
	if err != nil {
		t.Fatalf("reconnect should succeed, got: %v", err)
	}
	if result != newToken {
		t.Errorf("result = %s, want %s", result, newToken)
	}
	if callCount != 1 {
		t.Errorf("refreshFn called %d times, want 1", callCount)
	}
}

// TestReconnectOnAuthError_ExceedMaxAttempts 验证超过 MaxAttempts 返回 ErrBridgeReconnect。
func TestReconnectOnAuthError_ExceedMaxAttempts(t *testing.T) {
	refreshFn := func() (string, error) {
		return "", errRefreshUnavailable
	}

	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, refreshFn)

	result, err := rm.ReconnectOnAuthError()
	if err == nil {
		t.Fatal("should fail after exceeding MaxAttempts")
	}
	if result != "" {
		t.Errorf("result should be empty on failure, got %s", result)
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeReconnect)
	}
}

// TestReconnectOnAuthError_AttemptReset 验证成功后 attempt 计数器重置。
func TestReconnectOnAuthError_AttemptReset(t *testing.T) {
	newToken := "refreshed-token"
	refreshFn := func() (string, error) {
		return newToken, nil
	}

	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, refreshFn)

	// 第一次重连成功
	_, err := rm.ReconnectOnAuthError()
	if err != nil {
		t.Fatalf("first reconnect should succeed: %v", err)
	}

	// 第二次重连也应成功（attempt 已重置）
	_, err = rm.ReconnectOnAuthError()
	if err != nil {
		t.Errorf("second reconnect should succeed (attempt reset): %v", err)
	}
}

// TestCalculateDelay_RespectsMaxDelay 验证延迟不超过 MaxDelay（v2 P2-3）。
func TestCalculateDelay_RespectsMaxDelay(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Second,
		MaxDelay:     5 * time.Second,
		Factor:       2.0,
		JitterFactor: 0.2,
		MaxAttempts:  10,
	}
	rm := NewReconnectManager(config, nil)

	// 模拟高 attempt 值
	for attempt := 1; attempt <= 20; attempt++ {
		rm.attempt = attempt
		delay := rm.calculateDelay()
		if delay > config.MaxDelay {
			t.Errorf("attempt %d: delay %v exceeds MaxDelay %v", attempt, delay, config.MaxDelay)
		}
	}
}

// TestCalculateDelay_ExponentialBackoff 验证指数退避基本趋势（延迟随 attempt 严格增长）。
// 关闭抖动（JitterFactor=0）以便验证严格指数倍增关系。
func TestCalculateDelay_ExponentialBackoff(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    100 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		Factor:       2.0,
		JitterFactor: 0.0, // 关闭抖动以便验证严格指数
		MaxAttempts:  5,
	}
	rm := NewReconnectManager(config, nil)

	rm.attempt = 1
	d1 := rm.calculateDelay()
	rm.attempt = 2
	d2 := rm.calculateDelay()
	rm.attempt = 3
	d3 := rm.calculateDelay()

	// 无抖动时应严格指数增长：d1=100ms, d2=200ms, d3=400ms
	if d2 != 2*d1 {
		t.Errorf("d2=%v, want 2*d1=%v (strict doubling without jitter)", d2, 2*d1)
	}
	if d3 != 2*d2 {
		t.Errorf("d3=%v, want 2*d2=%v (strict doubling without jitter)", d3, 2*d2)
	}
	if d3 <= d2 || d2 <= d1 {
		t.Errorf("delays should be strictly increasing: d1=%v d2=%v d3=%v", d1, d2, d3)
	}
}

// TestDefaultReconnectConfig 验证默认配置值。
func TestDefaultReconnectConfig(t *testing.T) {
	if DefaultReconnectConfig.BaseDelay != 1*time.Second {
		t.Errorf("BaseDelay = %v, want 1s", DefaultReconnectConfig.BaseDelay)
	}
	if DefaultReconnectConfig.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", DefaultReconnectConfig.MaxDelay)
	}
	if DefaultReconnectConfig.Factor != 2.0 {
		t.Errorf("Factor = %v, want 2.0", DefaultReconnectConfig.Factor)
	}
	if DefaultReconnectConfig.JitterFactor != 0.2 {
		t.Errorf("JitterFactor = %v, want 0.2", DefaultReconnectConfig.JitterFactor)
	}
	if DefaultReconnectConfig.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %v, want 5", DefaultReconnectConfig.MaxAttempts)
	}
}

// TestReconnectOnAuthError_RetryThenSucceed 验证先失败后成功的重连场景。
func TestReconnectOnAuthError_RetryThenSucceed(t *testing.T) {
	newToken := "refreshed-token"
	callCount := 0
	refreshFn := func() (string, error) {
		callCount++
		if callCount < 3 {
			return "", errRefreshUnavailable
		}
		return newToken, nil
	}

	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  5,
	}
	rm := NewReconnectManager(config, refreshFn)

	result, err := rm.ReconnectOnAuthError()
	if err != nil {
		t.Fatalf("should succeed on 3rd attempt: %v", err)
	}
	if result != newToken {
		t.Errorf("result = %s, want %s", result, newToken)
	}
	if callCount != 3 {
		t.Errorf("refreshFn called %d times, want 3", callCount)
	}
}

// errRefreshUnavailable 测试用 error 常量。
var errRefreshUnavailable = &testError{msg: "refresh unavailable"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// TestCalculateDelay_JitterWithinRange 验证抖动后的延迟在合理范围内。
func TestCalculateDelay_JitterWithinRange(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Second,
		MaxDelay:     30 * time.Second,
		Factor:       2.0,
		JitterFactor: 0.2,
		MaxAttempts:  5,
	}
	rm := NewReconnectManager(config, nil)

	rm.attempt = 1
	baseDelay := float64(config.BaseDelay) // 1s

	for i := 0; i < 100; i++ {
		delay := rm.calculateDelay()
		// 抖动 ±20%，延迟应在 [0.8s, 1.2s] 范围内
		minDelay := time.Duration(baseDelay * 0.8)
		maxDelay := time.Duration(baseDelay * 1.2)
		if delay < minDelay || delay > maxDelay {
			t.Errorf("delay %v outside expected range [%v, %v]", delay, minDelay, maxDelay)
		}
	}
}

// TestReconnectOnAuthError_ZeroMaxAttempts 验证 MaxAttempts=0 时立即失败。
func TestReconnectOnAuthError_ZeroMaxAttempts(t *testing.T) {
	refreshFn := func() (string, error) {
		t.Error("refreshFn should not be called when MaxAttempts=0")
		return "", nil
	}

	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  0,
	}
	rm := NewReconnectManager(config, refreshFn)

	_, err := rm.ReconnectOnAuthError()
	if err == nil {
		t.Fatal("should fail immediately with MaxAttempts=0")
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeReconnect)
	}
}

// TestReconnectOnAuthError_NilRefreshToken 验证 refreshToken 为 nil 时返回错误而非 panic（B2 修复方案 B）。
func TestReconnectOnAuthError_NilRefreshToken(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, nil) // refreshToken 故意传 nil

	result, err := rm.ReconnectOnAuthError()
	if err == nil {
		t.Fatal("should return error when refreshToken is nil, not panic")
	}
	if result != "" {
		t.Errorf("result should be empty on nil refreshToken, got %s", result)
	}
	if id := ai.GetErrorID(err); id != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeReconnect)
	}
}

// TestSetRefreshToken 验证 SetRefreshToken 注入回调后重连正常工作（B2 修复方案 A）。
func TestSetRefreshToken(t *testing.T) {
	newToken := "injected-refresh-token"
	callCount := 0
	refreshFn := func() (string, error) {
		callCount++
		return newToken, nil
	}

	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.1,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, nil) // 初始为 nil
	rm.SetRefreshToken(refreshFn)          // 后续注入

	result, err := rm.ReconnectOnAuthError()
	if err != nil {
		t.Fatalf("reconnect should succeed after SetRefreshToken: %v", err)
	}
	if result != newToken {
		t.Errorf("result = %s, want %s", result, newToken)
	}
	if callCount != 1 {
		t.Errorf("refreshFn called %d times, want 1", callCount)
	}
}

// ---- T1-P1-A1 异步重连 + BackoffStrategy 注入测试 ----

// mockBackoff 是测试用 BackoffStrategy 实现，返回固定延迟（用于验证 BackoffStrategy 注入生效）。
type mockBackoff struct {
	delay time.Duration
}

// NextDelay 返回固定延迟（忽略 attempt）。
func (m *mockBackoff) NextDelay(attempt int) time.Duration {
	return m.delay
}

// Reset 是 noop。
func (m *mockBackoff) Reset() {}

// TestReconnectOnAuthErrorAsync_Success 验证异步重连成功返回新 token + 回调被调用。
func TestReconnectOnAuthErrorAsync_Success(t *testing.T) {
	newToken := "async-refreshed-token"
	refreshFn := func() (string, error) {
		return newToken, nil
	}
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, refreshFn)

	callbackFired := make(chan struct{}, 1)
	rm.SetOnReconnectComplete(func(success bool, err error) {
		if success && err == nil {
			callbackFired <- struct{}{}
		}
	})

	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := rm.ReconnectOnAuthErrorAsync(ctx)
	result := <-resultCh

	if result.Err != nil {
		t.Fatalf("async reconnect should succeed, got: %v", result.Err)
	}
	if result.NewToken != newToken {
		t.Errorf("NewToken = %s, want %s", result.NewToken, newToken)
	}
	if result.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", result.Attempt)
	}

	select {
	case <-callbackFired:
		// 回调已触发
	case <-time.After(2 * time.Second):
		t.Fatal("onComplete callback did not fire within 2s on success")
	}
}

// TestReconnectOnAuthErrorAsync_ExceedMaxAttempts 验证异步重连超 MaxAttempts 后返回错误。
func TestReconnectOnAuthErrorAsync_ExceedMaxAttempts(t *testing.T) {
	refreshFn := func() (string, error) {
		return "", errRefreshUnavailable
	}
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  2,
	}
	rm := NewReconnectManager(config, refreshFn)

	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := rm.ReconnectOnAuthErrorAsync(ctx)
	result := <-resultCh

	if result.Err == nil {
		t.Fatal("should fail after exceeding MaxAttempts")
	}
	if result.NewToken != "" {
		t.Errorf("NewToken should be empty on failure, got %s", result.NewToken)
	}
	if id := ai.GetErrorID(result.Err); id != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeReconnect)
	}
}

// TestReconnectOnAuthErrorAsync_CtxCancelled 验证 ctx 取消时立即返回错误。
func TestReconnectOnAuthErrorAsync_CtxCancelled(t *testing.T) {
	refreshFn := func() (string, error) {
		return "", errRefreshUnavailable
	}
	config := ReconnectConfig{
		BaseDelay:    200 * time.Millisecond, // 较长延迟以便在等待期间取消
		MaxDelay:     500 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  10,
	}
	rm := NewReconnectManager(config, refreshFn)

	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}

	resultCh := rm.ReconnectOnAuthErrorAsync(ctx)

	// 短暂等待后取消 ctx
	time.Sleep(50 * time.Millisecond)
	rm.Stop()

	result := <-resultCh
	if result.Err == nil {
		t.Fatal("should fail with ctx cancellation error")
	}
	if id := ai.GetErrorID(result.Err); id != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeReconnect)
	}
}

// TestReconnectOnAuthErrorAsync_NilRefreshToken 验证 refreshToken 为 nil 时立即返回错误而非 panic。
func TestReconnectOnAuthErrorAsync_NilRefreshToken(t *testing.T) {
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, nil)

	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := rm.ReconnectOnAuthErrorAsync(ctx)
	result := <-resultCh

	if result.Err == nil {
		t.Fatal("should return error when refreshToken is nil")
	}
	if id := ai.GetErrorID(result.Err); id != ai.ErrBridgeReconnect {
		t.Errorf("ErrorID = %s, want %s", id, ai.ErrBridgeReconnect)
	}
}

// TestTryStartAsyncReconnect_InflightDedup 验证 inflight 去重（防 401 风暴）。
func TestTryStartAsyncReconnect_InflightDedup(t *testing.T) {
	refreshFn := func() (string, error) {
		return "token", nil
	}
	config := ReconnectConfig{
		BaseDelay:    50 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  3,
	}
	rm := NewReconnectManager(config, refreshFn)

	// 注册回调以信号 finishAsyncReconnect 完成（inflight 清理）
	done := make(chan struct{}, 1)
	rm.SetOnReconnectComplete(func(bool, error) {
		done <- struct{}{}
	})

	// 第一次 TryStartAsyncReconnect 应成功
	ctx1, ok1 := rm.TryStartAsyncReconnect()
	if !ok1 {
		t.Fatal("first TryStartAsyncReconnect should succeed")
	}

	// 第二次 TryStartAsyncReconnect 应失败（inflight 去重）
	_, ok2 := rm.TryStartAsyncReconnect()
	if ok2 {
		t.Error("second TryStartAsyncReconnect should fail (inflight dedup)")
	}

	// 启动异步重连
	resultCh := rm.ReconnectOnAuthErrorAsync(ctx1)
	<-resultCh

	// 等待 finishAsyncReconnect 清理 inflight
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("finishAsyncReconnect did not complete within 2s")
	}

	// 重连完成后 inflight 应被清理，第三次 TryStartAsyncReconnect 应成功
	_, ok3 := rm.TryStartAsyncReconnect()
	if !ok3 {
		t.Error("third TryStartAsyncReconnect should succeed after inflight cleared")
	}
}

// TestTryStartAsyncReconnect_Stopped 验证 Stop 后 TryStartAsyncReconnect 返回 false。
func TestTryStartAsyncReconnect_Stopped(t *testing.T) {
	rm := NewReconnectManager(DefaultReconnectConfig, nil)
	rm.Stop()

	_, ok := rm.TryStartAsyncReconnect()
	if ok {
		t.Error("TryStartAsyncReconnect should fail after Stop")
	}
}

// TestSetBackoffStrategy 验证注入 BackoffStrategy 后 calculateDelayLocked 使用其 NextDelay。
func TestSetBackoffStrategy(t *testing.T) {
	rm := NewReconnectManager(ReconnectConfig{
		BaseDelay:    1 * time.Second, // config 公式 attempt=1 返回 ~1s
		MaxDelay:     30 * time.Second,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  5,
	}, nil)

	// 注入自定义 mockBackoff 返回固定 7s（与 config 公式 1s 区分）
	rm.SetBackoffStrategy(&mockBackoff{delay: 7 * time.Second})

	delay := rm.calculateDelayLocked(1)
	if delay != 7*time.Second {
		t.Errorf("delay = %v, want 7s (from mockBackoff)", delay)
	}
}

// TestSetBackoffStrategy_NilIgnored 验证 SetBackoffStrategy(nil) 不改变现有行为（向后兼容）。
func TestSetBackoffStrategy_NilIgnored(t *testing.T) {
	rm := NewReconnectManager(ReconnectConfig{
		BaseDelay:    1 * time.Second,
		MaxDelay:     30 * time.Second,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  5,
	}, nil)

	rm.SetBackoffStrategy(nil) // nil 应被忽略

	// calculateDelayLocked 应使用 config 公式（attempt=1 → 1s）
	delay := rm.calculateDelayLocked(1)
	if delay != 1*time.Second {
		t.Errorf("delay = %v, want 1s (config formula, nil backoff ignored)", delay)
	}
}

// TestSetOnReconnectComplete_CallbackFiredOnFailure 验证失败时回调被调用且 success=false。
func TestSetOnReconnectComplete_CallbackFiredOnFailure(t *testing.T) {
	refreshFn := func() (string, error) {
		return "", errRefreshUnavailable
	}
	config := ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  2,
	}
	rm := NewReconnectManager(config, refreshFn)

	callbackFired := make(chan struct{}, 1)
	var cbSuccess bool
	var cbErr error
	rm.SetOnReconnectComplete(func(success bool, err error) {
		cbSuccess = success
		cbErr = err
		callbackFired <- struct{}{}
	})

	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := rm.ReconnectOnAuthErrorAsync(ctx)
	<-resultCh

	select {
	case <-callbackFired:
		if cbSuccess {
			t.Errorf("expected callback success=false, got true")
		}
		if cbErr == nil {
			t.Error("expected callback err non-nil on failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not fire within 2s on failure")
	}
}

// TestStop_CancelInProgress 验证 Stop 取消正在进行的异步重连（幂等）。
func TestStop_CancelInProgress(t *testing.T) {
	refreshFn := func() (string, error) {
		return "", errRefreshUnavailable
	}
	config := ReconnectConfig{
		BaseDelay:    200 * time.Millisecond,
		MaxDelay:     500 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  10,
	}
	rm := NewReconnectManager(config, refreshFn)

	ctx, ok := rm.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := rm.ReconnectOnAuthErrorAsync(ctx)

	// 等待 goroutine 启动后 Stop
	time.Sleep(30 * time.Millisecond)
	rm.Stop()
	rm.Stop() // 二次调用应幂等，不 panic

	result := <-resultCh
	if result.Err == nil {
		t.Fatal("should fail after Stop")
	}
}

// TestMapErrorCodeToErrorID_ErrBridgeReconnecting 验证 ErrBridgeReconnecting 映射到 ErrBridgeReconnect。
func TestMapErrorCodeToErrorID_ErrBridgeReconnecting(t *testing.T) {
	id := MapErrorCodeToErrorID(ErrBridgeReconnecting)
	if id != ai.ErrBridgeReconnect {
		t.Errorf("ErrBridgeReconnecting maps to %s, want %s", id, ai.ErrBridgeReconnect)
	}
}
