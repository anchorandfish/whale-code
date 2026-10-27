package bridge

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// mockRefreshFn 创建一个mock的刷新函数，返回指定 token 和错误
func mockRefreshFn(token string, err error) func() (string, error) {
	return func() (string, error) {
		return token, err
	}
}

// waitUntil 在超时前轮询 cond，返回是否满足。
//
// 为什么不用固定 time.Sleep（2026-10-21 实测红过）：调度器按墙钟定时，
// `sleep(50ms)` + 断言「已刷新」把「调度器延迟上界」写成了「机器负载上界」——
// 全量并行跑测试时定时器晚触发几十毫秒就假红（TestRefreshScheduler_ScheduleFromExpiry
// 在导出树全量跑里偶发失败，单独重跑 3 次全绿）。轮询保留「最终必须刷新」的语义，
// 同时把「多久算久」放宽到超时值，不再依赖调度时机。
// 「不许太早刷新」的下界断言仍用固定短 sleep 直接判（那是上界，负载不影响结论）。
func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// alwaysFailRefreshFn 创建一个始终失败的刷新函数
func alwaysFailRefreshFn() func() (string, error) {
	return func() (string, error) {
		return "", errors.New("refresh always fails")
	}
}

// alwaysSucceedRefreshFn 创建一个始终成功的刷新函数
// 返回一个在 1 小时后过期的 JWT token
func alwaysSucceedRefreshFn() func() (string, error) {
	return func() (string, error) {
		token := createTestJWTExpiringIn(1 * time.Hour)
		return token, nil
	}
}

// newTestScheduler creates a test调度器，使用极短的延迟参数
func newTestScheduler(refreshFn func() (string, error)) *RefreshScheduler {
	config := RefreshSchedulerConfig{
		RefreshBufferMs:     5, // 5ms 缓冲
		MaxRefreshFailures:  3,
		RefreshRetryDelayMs: 10, // 10ms 重试延迟
		FallbackIntervalMs:  30, // 30ms 兜底间隔
	}
	return NewRefreshScheduler(config, refreshFn)
}

// TestRefreshScheduler_NormalRefresh 测试正常的刷新流程
func TestRefreshScheduler_NormalRefresh(t *testing.T) {
	var refreshCount int32
	refreshFn := func() (string, error) {
		atomic.AddInt32(&refreshCount, 1)
		return createTestJWTExpiringIn(1 * time.Hour), nil
	}

	s := newTestScheduler(refreshFn)

	// 安排在 20ms 后刷新（TTL=20ms，buffer=5ms，actual延迟=15ms）
	s.ScheduleFromExpiresIn(20)

	// 等待刷新执行
	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&refreshCount) < 1 {
		t.Error("expected at least one refresh")
	}

	s.CancelAll()
}

// TestRefreshScheduler_GenerationCounter 测试代际计数器机制
// 新的调度应使旧的调度失效
func TestRefreshScheduler_GenerationCounter(t *testing.T) {
	var refreshCount int32
	refreshFn := func() (string, error) {
		atomic.AddInt32(&refreshCount, 1)
		return createTestJWTExpiringIn(1 * time.Hour), nil
	}

	s := newTestScheduler(refreshFn)

	// 安排第一个调度（较长延迟）
	s.ScheduleFromExpiresIn(50)

	// 立即安排第二个调度（较短延迟），应使第一个失效
	s.ScheduleFromExpiresIn(20)

	// 等待第二个调度执行
	time.Sleep(40 * time.Millisecond)

	count := atomic.LoadInt32(&refreshCount)
	if count != 1 {
		t.Errorf("expected exactly 1 refresh (from second schedule), got %d", count)
	}

	s.CancelAll()
}

// TestRefreshScheduler_MaxConsecutiveFailures 测试连续失败达到上限后停止重试
func TestRefreshScheduler_MaxConsecutiveFailures(t *testing.T) {
	s := newTestScheduler(alwaysFailRefreshFn())

	// 安排一个立即刷新（TTL 小于 buffer）
	s.ScheduleFromExpiresIn(1)

	// 等待足够时间让重试耗尽
	time.Sleep(100 * time.Millisecond)

	state := s.GetState()
	if state.ConsecFailures < 3 {
		t.Errorf("expected at least 3 consecutive failures, got %d", state.ConsecFailures)
	}

	s.CancelAll()
}

// TestRefreshScheduler_RetryDelayOnFailure 测试刷新失败后的重试延迟
func TestRefreshScheduler_RetryDelayOnFailure(t *testing.T) {
	var refreshTimes []time.Time
	var refreshCount int32

	refreshFn := func() (string, error) {
		atomic.AddInt32(&refreshCount, 1)
		refreshTimes = append(refreshTimes, time.Now())
		return "", errors.New("refresh failed")
	}

	s := newTestScheduler(refreshFn)

	// 安排立即刷新
	s.ScheduleFromExpiresIn(1)

	// 等待至少两次重试
	time.Sleep(80 * time.Millisecond)

	count := atomic.LoadInt32(&refreshCount)
	if count < 2 {
		t.Errorf("expected at least 2 refresh attempts, got %d", count)
	}

	s.CancelAll()
}

// TestRefreshScheduler_FallbackInterval 测试兜底刷新间隔
func TestRefreshScheduler_FallbackInterval(t *testing.T) {
	var refreshCount int32
	refreshFn := func() (string, error) {
		atomic.AddInt32(&refreshCount, 1)
		// 返回一个没有 exp 的 token，触发兜底间隔
		return createTestJWT(map[string]interface{}{"sub": "no-exp"}), nil
	}

	s := newTestScheduler(refreshFn)

	// 使用 ScheduleOrFallback 传入无 exp 的 token
	token := createTestJWT(map[string]interface{}{"sub": "no-exp"})
	s.ScheduleOrFallback(token)

	// 等待兜底间隔触发
	time.Sleep(60 * time.Millisecond)

	count := atomic.LoadInt32(&refreshCount)
	if count < 1 {
		t.Error("expected at least one refresh via fallback interval")
	}

	s.CancelAll()
}

// TestRefreshScheduler_CancelSpecificSchedule test cancelled特定调度
func TestRefreshScheduler_CancelSpecificSchedule(t *testing.T) {
	var refreshCount int32
	refreshFn := func() (string, error) {
		atomic.AddInt32(&refreshCount, 1)
		return createTestJWTExpiringIn(1 * time.Hour), nil
	}

	s := newTestScheduler(refreshFn)

	// 安排一个较长延迟的刷新
	s.ScheduleFromExpiresIn(50)

	// 立即取消
	s.Cancel()

	time.Sleep(80 * time.Millisecond)

	count := atomic.LoadInt32(&refreshCount)
	if count != 0 {
		t.Errorf("expected 0 refreshes after cancel, got %d", count)
	}
}

// TestRefreshScheduler_CancelAllSchedules test cancelled所有调度并重置状态
func TestRefreshScheduler_CancelAllSchedules(t *testing.T) {
	s := newTestScheduler(alwaysFailRefreshFn())

	// 安排刷新，让它失败几次
	s.ScheduleFromExpiresIn(1)
	time.Sleep(50 * time.Millisecond)

	// 取消所有
	s.CancelAll()

	state := s.GetState()
	if state.ConsecFailures != 0 {
		t.Errorf("expected 0 failures after CancelAll, got %d", state.ConsecFailures)
	}
	if state.IsScheduled {
		t.Error("expected no active schedule after CancelAll")
	}
}

// TestRefreshScheduler_ScheduleFromExpiresIn 测试基于 TTL 安排刷新
func TestRefreshScheduler_ScheduleFromExpiresIn(t *testing.T) {
	var refreshed atomic.Bool
	refreshFn := func() (string, error) {
		refreshed.Store(true)
		return createTestJWTExpiringIn(1 * time.Hour), nil
	}

	s := newTestScheduler(refreshFn)

	// 第一段：「没到点不会提前刷」。
	// ★ 不要用「排 20ms + 睡 10ms 再断言还没刷」：测试配置的 RefreshBufferMs 是 5ms，
	// 20ms TTL 的实际延迟只有 15ms ⇒ 这个断言只剩 5ms 余量。机器一忙，连
	// time.Sleep(10ms) 自己都会 overshoot 过 15ms，用例于是假红
	// （实测 2026-10-27：6 个包并发编译时 30 次里 3 次报 "refresh happened too early"）。
	// 改成给一个 60s 的 TTL：余量从 5ms 变成约 60s，"提前刷新"才真的是提前。
	s.ScheduleFromExpiresIn(60_000)
	if !s.GetState().IsScheduled {
		t.Fatal("ScheduleFromExpiresIn 之后应当处于已排定状态")
	}
	time.Sleep(10 * time.Millisecond)
	if refreshed.Load() {
		t.Error("refresh happened too early (TTL=60s)")
	}

	// 第二段：「到点会刷」。20ms TTL ⇒ 缓冲 5ms ⇒ 约 15ms 后触发。
	// 这里只等**事件发生**（waitUntil 自带 2s 上界），不再断言"某个时刻之前它没发生"。
	s.ScheduleFromExpiresIn(20)
	if !waitUntil(2*time.Second, refreshed.Load) {
		t.Error("expected refresh to have occurred")
	}

	s.CancelAll()
}

// TestRefreshScheduler_ScheduleFromExpiry 测试基于绝对expiration time安排刷新
func TestRefreshScheduler_ScheduleFromExpiry(t *testing.T) {
	var refreshed atomic.Bool
	refreshFn := func() (string, error) {
		refreshed.Store(true)
		return createTestJWTExpiringIn(1 * time.Hour), nil
	}

	s := newTestScheduler(refreshFn)

	// 设置 20ms 后过期
	expiry := time.Now().Add(20 * time.Millisecond).Unix()
	s.ScheduleFromExpiry(expiry)

	// 等待刷新（轮询到 2s 上界，见 waitUntil 注释）
	if !waitUntil(2*time.Second, refreshed.Load) {
		t.Error("expected refresh to have occurred")
	}

	s.CancelAll()
}

// TestRefreshScheduler_RefreshSuccessResetsFailures 测试刷新成功后重置失败计数
func TestRefreshScheduler_RefreshSuccessResetsFailures(t *testing.T) {
	var callCount int32

	refreshFn := func() (string, error) {
		count := atomic.AddInt32(&callCount, 1)
		// 前两次失败，第三次成功
		if count <= 2 {
			return "", errors.New("temporary failure")
		}
		return createTestJWTExpiringIn(1 * time.Hour), nil
	}

	s := newTestScheduler(refreshFn)

	// 安排立即刷新
	s.ScheduleFromExpiresIn(1)

	// 等待足够时间让前两次失败和第三次成功
	time.Sleep(100 * time.Millisecond)

	state := s.GetState()
	if state.ConsecFailures != 0 {
		t.Errorf("expected 0 failures after successful refresh, got %d", state.ConsecFailures)
	}

	s.CancelAll()
}

// TestRefreshScheduler_OnTokenUpdateCallback 测试 Token 更新回调
func TestRefreshScheduler_OnTokenUpdateCallback(t *testing.T) {
	var updatedToken atomic.Value

	refreshFn := func() (string, error) {
		token := createTestJWTExpiringIn(1 * time.Hour)
		return token, nil
	}

	s := newTestScheduler(refreshFn)
	s.SetOnTokenUpdate(func(token string) {
		updatedToken.Store(token)
	})

	// 安排立即刷新
	s.ScheduleFromExpiresIn(1)

	// 等待刷新执行
	time.Sleep(30 * time.Millisecond)

	stored := updatedToken.Load()
	if stored == nil {
		t.Error("expected onTokenUpdate callback to be called")
	}

	s.CancelAll()
}

// TestRefreshScheduler_ScheduleOrFallback 测试 ScheduleOrFallback 方法
func TestRefreshScheduler_ScheduleOrFallback(t *testing.T) {
	t.Run("with valid exp", func(t *testing.T) {
		var refreshed atomic.Bool
		refreshFn := func() (string, error) {
			refreshed.Store(true)
			return createTestJWTExpiringIn(1 * time.Hour), nil
		}

		s := newTestScheduler(refreshFn)

		// 使用有 exp 的 token
		token := createTestJWTExpiringIn(20 * time.Millisecond)
		s.ScheduleOrFallback(token)

		time.Sleep(50 * time.Millisecond)
		if !refreshed.Load() {
			t.Error("expected refresh via exp-based schedule")
		}

		s.CancelAll()
	})

	t.Run("with no exp - fallback", func(t *testing.T) {
		var refreshed atomic.Bool
		refreshFn := func() (string, error) {
			refreshed.Store(true)
			return createTestJWT(map[string]interface{}{"sub": "no-exp"}), nil
		}

		s := newTestScheduler(refreshFn)

		// 使用无 exp 的 token，应使用兜底间隔
		token := createTestJWT(map[string]interface{}{"sub": "no-exp"})
		s.ScheduleOrFallback(token)

		time.Sleep(60 * time.Millisecond)
		if !refreshed.Load() {
			t.Error("expected refresh via fallback interval")
		}

		s.CancelAll()
	})
}
