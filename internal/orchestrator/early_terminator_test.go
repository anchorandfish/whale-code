// early_terminator_test.go — H10 Phase 5 桶 4 早期终止检查器反例测试（TDD 先行）。
// 覆盖致命反例：
// - 致命反例 4：80% 误判（连续 3 次选继续 → 禁用 80% 检查点）
// - 致命反例 6：80% 5min 去重（同 taskID 5min 内只问 1 次）
// - 致命反例 9：流式 usage 不可用降级（10s 轮询）
// - 致命反例 3 关联：并发 CheckAt80Percent 无竞态（-race）
// line 996-审查 v1 注明"按 token 消耗 / tokens_cap 比例实现"；落地：三档检查
// + 误判降级；落地：CheckAt20/50/80Percent
// （3 次禁用 80%）；落地：miscalibrateCount + disabled80Percent
// usage 降级；落地：subscribeStreamingUsage + fallbackPoll
// SNAPSHOT: tests={80PercentMiscalibrate_3Times_Disable(致命4), 80PercentHasDraft_Continue, 80PercentDedup_5min(致命6), 50PercentTraceError_Warning, 20Percent_LogOnly, StreamingFallback_Poll10s(致命9), ConcurrentCheck80_NoRace(致命3关联), 80PercentUserTerminate_ResetMiscalibrate, 50PercentNoTrace_NoWarning, UserAskerNil_Degrade}

package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockTraceQueryer mock TraceQueryer 实现（测试用）。
type mockTraceQueryer struct {
	events []PhaseTraceEvent
	err    error
}

func (m *mockTraceQueryer) QueryBySubtask(subtaskID string) ([]PhaseTraceEvent, error) {
	return m.events, m.err
}

// mockOutputStore mock OutputStore 实现（测试用）。
type mockOutputStore struct {
	hasDraft bool
}

func (m *mockOutputStore) HasDraft(taskID string) bool {
	return m.hasDraft
}

// mockUserAsker mock UserAsker 实现（测试用）。
type mockUserAsker struct {
	terminate bool // 返回值：true=终止，false=继续
	called    int
}

func (m *mockUserAsker) AskBatchTerminate(taskID string) bool {
	m.called++
	return m.terminate
}

// mockStreamingUsageProvider mock StreamingUsageProvider 实现（测试用）。
type mockStreamingUsageProvider struct {
	supported bool // SubscribeUsage 返回值：true=支持流式，false=不支持
	called    int
}

func (m *mockStreamingUsageProvider) SubscribeUsage(taskID string, callback func(usedTokens int64)) bool {
	m.called++
	if !m.supported {
		return false
	}
	// 模拟流式回调（异步调用一次 callback）
	go func() {
		callback(1000)
	}()
	return true
}

// newTestEarlyTerminator 创建测试用 EarlyTerminator（注入固定时间便于去重测试）。
func newTestEarlyTerminator(tq TraceQueryer, os OutputStore, ua UserAsker, sp StreamingUsageProvider) *EarlyTerminator {
	e := NewEarlyTerminator(tq, os, ua, sp)
	// 注入固定时间（便于 5min 去重窗口测试）
	now := time.Now()
	e.SetNowFunc(func() time.Time { return now })
	return e
}

// TestEarlyTerminator_20Percent_LogOnly 20% 检查点仅日志无动作。
// 验证：CheckAt20Percent 不返回 error，不终止，无副作用。
func TestEarlyTerminator_20Percent_LogOnly(t *testing.T) {
	e := newTestEarlyTerminator(nil, nil, nil, nil)
	// 应不 panic 不 error
	e.CheckAt20Percent("task-1", 1000)
	// 无状态变更验证（miscalibrateCount 不变）
	if e.MiscalibrateCount() != 0 {
		t.Errorf("20%% 检查点不应改变 miscalibrateCount，got=%d", e.MiscalibrateCount())
	}
	if e.IsDisabled80Percent() {
		t.Error("20% 检查点不应禁用 80%")
	}
}

// TestEarlyTerminator_50PercentTraceError_Warning 50% 检查点：trace 有 error_type → 返回 warning。
func TestEarlyTerminator_50PercentTraceError_Warning(t *testing.T) {
	// 构造含 error_type 的 trace 事件
	tq := &mockTraceQueryer{
		events: []PhaseTraceEvent{
			{
				EventType: "dispatch",
				SubtaskID: "task-1",
				Payload:   map[string]interface{}{"error_type": ErrorTypeCompileError},
			},
		},
	}
	e := newTestEarlyTerminator(tq, nil, nil, nil)

	warning, reason := e.CheckAt50Percent("task-1", 5000)
	if !warning {
		t.Error("trace 含 error_type=compile_error 应返回 warning=true")
	}
	if reason == "" {
		t.Error("warning reason 应非空")
	}
}

// TestEarlyTerminator_50PercentNoTrace_NoWarning 50% 检查点：trace 无 error_type → 无 warning。
func TestEarlyTerminator_50PercentNoTrace_NoWarning(t *testing.T) {
	tq := &mockTraceQueryer{
		events: []PhaseTraceEvent{
			{
				EventType: "dispatch",
				SubtaskID: "task-1",
				Payload:   map[string]interface{}{"error_type": ErrorTypeNone},
			},
		},
	}
	e := newTestEarlyTerminator(tq, nil, nil, nil)

	warning, reason := e.CheckAt50Percent("task-1", 5000)
	if warning {
		t.Error("trace error_type=none 应返回 warning=false")
	}
	if reason != "" {
		t.Errorf("无 warning 时 reason 应空，got=%s", reason)
	}
}

// TestEarlyTerminator_50PercentTraceQueryerNil_NoWarning traceQueryer 为 nil → 降级无 warning。
func TestEarlyTerminator_50PercentTraceQueryerNil_NoWarning(t *testing.T) {
	e := newTestEarlyTerminator(nil, nil, nil, nil)
	warning, _ := e.CheckAt50Percent("task-1", 5000)
	if warning {
		t.Error("traceQueryer 为 nil 应降级返回 warning=false")
	}
}

// TestEarlyTerminator_80PercentHasDraft_Continue 80% 检查点：output.json 有雏形 → 不终止。
func TestEarlyTerminator_80PercentHasDraft_Continue(t *testing.T) {
	os := &mockOutputStore{hasDraft: true}
	ua := &mockUserAsker{terminate: true} // 即使用户选终止
	e := newTestEarlyTerminator(nil, os, ua, nil)

	shouldTerm, err := e.CheckAt80Percent("task-1", 8000)
	if err != nil {
		t.Fatalf("CheckAt80Percent: %v", err)
	}
	if shouldTerm {
		t.Error("HasDraft=true 有雏形应返回 false（让任务继续），不应终止")
	}
	// 验证未问用户（HasDraft 短路）
	if ua.called != 0 {
		t.Errorf("HasDraft=true 应短路不问用户，AskBatchTerminate called=%d", ua.called)
	}
}

// TestEarlyTerminator_80PercentMiscalibrate_3Times_Disable 致命反例 4：
// 连续 3 次用户选继续（误判）→ 禁用 80% 检查点。
// 零容忍：3 次误判必须禁用 80%。
func TestEarlyTerminator_80PercentMiscalibrate_3Times_Disable(t *testing.T) {
	ua := &mockUserAsker{terminate: false} // 用户始终选继续（误判）
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// 第 1 次：用户选继续 → miscalibrateCount=1
	shouldTerm, _ := e.CheckAt80Percent("task-1", 8000)
	if shouldTerm {
		t.Error("用户选继续应返回 false")
	}
	if e.MiscalibrateCount() != 1 {
		t.Errorf("第 1 次误判后 miscalibrateCount 应=1，got=%d", e.MiscalibrateCount())
	}
	if e.IsDisabled80Percent() {
		t.Error("第 1 次误判不应禁用 80%")
	}

	// 第 2 次：用不同 taskID（避免 5min 去重）→ miscalibrateCount=2
	shouldTerm, _ = e.CheckAt80Percent("task-2", 8000)
	if shouldTerm {
		t.Error("用户选继续应返回 false")
	}
	if e.MiscalibrateCount() != 2 {
		t.Errorf("第 2 次误判后 miscalibrateCount 应=2，got=%d", e.MiscalibrateCount())
	}
	if e.IsDisabled80Percent() {
		t.Error("第 2 次误判不应禁用 80%")
	}

	// 第 3 次：miscalibrateCount=3 → 禁用 80%
	shouldTerm, _ = e.CheckAt80Percent("task-3", 8000)
	if shouldTerm {
		t.Error("用户选继续应返回 false")
	}
	if e.MiscalibrateCount() != 3 {
		t.Errorf("第 3 次误判后 miscalibrateCount 应=3，got=%d", e.MiscalibrateCount())
	}
	if !e.IsDisabled80Percent() {
		t.Fatal("致命反例 4：连续 3 次误判必须禁用 80% 检查点")
	}

	// 第 4 次：80% 已禁用 → 直接返回 false，不再问用户
	ua.called = 0 // 重置计数
	shouldTerm, _ = e.CheckAt80Percent("task-4", 8000)
	if shouldTerm {
		t.Error("80% 禁用后应返回 false")
	}
	if ua.called != 0 {
		t.Errorf("致命反例 4：80%% 禁用后不应问用户，AskBatchTerminate called=%d", ua.called)
	}
}

// TestEarlyTerminator_80PercentUserTerminate_ResetMiscalibrate 用户选终止 → 重置误判计数。
func TestEarlyTerminator_80PercentUserTerminate_ResetMiscalibrate(t *testing.T) {
	ua := &mockUserAsker{terminate: false}
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// 2 次误判
	_, _ = e.CheckAt80Percent("task-1", 8000)
	_, _ = e.CheckAt80Percent("task-2", 8000)
	if e.MiscalibrateCount() != 2 {
		t.Fatalf("2 次误判后 miscalibrateCount 应=2，got=%d", e.MiscalibrateCount())
	}

	// 第 3 次用户选终止 → 重置误判计数
	ua.terminate = true
	shouldTerm, _ := e.CheckAt80Percent("task-3", 8000)
	if !shouldTerm {
		t.Error("用户选终止应返回 true")
	}
	if e.MiscalibrateCount() != 0 {
		t.Errorf("用户选终止后 miscalibrateCount 应重置为 0，got=%d", e.MiscalibrateCount())
	}
	if e.IsDisabled80Percent() {
		t.Error("用户选终止后 80% 不应禁用")
	}
}

// TestEarlyTerminator_80PercentDedup_5min 致命反例 6：
// 5min 内重复触发 → 跳过（同 taskID 只问 1 次）。
// 零容忍：5min 窗口内不能重复问用户。
func TestEarlyTerminator_80PercentDedup_5min(t *testing.T) {
	ua := &mockUserAsker{terminate: false}
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// 固定时间起点
	baseTime := time.Now()
	e.SetNowFunc(func() time.Time { return baseTime })

	// 第 1 次：问用户
	_, _ = e.CheckAt80Percent("task-1", 8000)
	if ua.called != 1 {
		t.Errorf("第 1 次应问用户，called=%d", ua.called)
	}

	// 第 2 次：5min 内（baseTime + 4min）→ 跳过
	e.SetNowFunc(func() time.Time { return baseTime.Add(4 * time.Minute) })
	_, _ = e.CheckAt80Percent("task-1", 8000)
	if ua.called != 1 {
		t.Errorf("致命反例 6：5min 内应跳过不问用户，called=%d", ua.called)
	}

	// 第 3 次：5min 后（baseTime + 6min）→ 重新问用户
	e.SetNowFunc(func() time.Time { return baseTime.Add(6 * time.Minute) })
	_, _ = e.CheckAt80Percent("task-1", 8000)
	if ua.called != 2 {
		t.Errorf("5min 后应重新问用户，called=%d", ua.called)
	}
}

// TestEarlyTerminator_80PercentDifferentTaskID_NoDedup 不同 taskID 不去重（各自独立 5min 窗口）。
func TestEarlyTerminator_80PercentDifferentTaskID_NoDedup(t *testing.T) {
	ua := &mockUserAsker{terminate: false}
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// task-1 问 1 次
	_, _ = e.CheckAt80Percent("task-1", 8000)
	// task-2 不同 taskID → 应问（不去重）
	_, _ = e.CheckAt80Percent("task-2", 8000)
	if ua.called != 2 {
		t.Errorf("不同 taskID 不应去重，应 called=2，got=%d", ua.called)
	}
}

// TestEarlyTerminator_UserAskerNil_Degrade userAsker 为 nil → 降级不终止不问用户。
func TestEarlyTerminator_UserAskerNil_Degrade(t *testing.T) {
	e := newTestEarlyTerminator(nil, nil, nil, nil) // userAsker=nil
	shouldTerm, err := e.CheckAt80Percent("task-1", 8000)
	if err != nil {
		t.Fatalf("userAsker=nil 降级不应返回 error: %v", err)
	}
	if shouldTerm {
		t.Error("userAsker=nil 降级应返回 false（不终止）")
	}
	// 误判计数不应增加（未实际问用户）
	if e.MiscalibrateCount() != 0 {
		t.Errorf("userAsker=nil 降级不应增加误判计数，got=%d", e.MiscalibrateCount())
	}
}

// TestEarlyTerminator_StreamingFallback_Poll10s 致命反例 9：
// N1 不支持 streaming → 降级 10s 轮询。
// 零容忍：流式不可用时必须降级到周期性轮询（<10s 延迟）。
func TestEarlyTerminator_StreamingFallback_Poll10s(t *testing.T) {
	// streaming 为 nil → 直接降级轮询
	e := newTestEarlyTerminator(nil, nil, nil, nil)

	// 验证 IsStreamingSupported 返回 false
	if e.IsStreamingSupported() {
		t.Error("streaming=nil 时 IsStreamingSupported 应=false")
	}

	// 启动降级轮询（用短超时 ctx 验证 callback 被调用）
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var callbackCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.SubscribeStreamingUsage(ctx, "task-1", 10000, func(usedTokens int64) {
			callbackCount.Add(1)
		})
	}()

	// 等待至少 1 次 callback（10s 轮询，12s 超时）
	// 注意：由于轮询间隔是 10s，需要等待 ~10s 才能触发第 1 次 callback
	// 用 deadline 等待
	deadline := time.Now().Add(11 * time.Second)
	for time.Now().Before(deadline) {
		if callbackCount.Load() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel() // 停止轮询
	wg.Wait()

	if callbackCount.Load() == 0 {
		t.Fatal("致命反例 9：streaming=nil 应降级到 10s 轮询并触发 callback")
	}
}

// TestEarlyTerminator_StreamingSupported_Subscribe 流式可用 → 订阅成功不轮询。
func TestEarlyTerminator_StreamingSupported_Subscribe(t *testing.T) {
	sp := &mockStreamingUsageProvider{supported: true}
	e := newTestEarlyTerminator(nil, nil, nil, sp)

	if !e.IsStreamingSupported() {
		t.Error("streaming 非 nil 时 IsStreamingSupported 应=true")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var callbackCount atomic.Int64
	e.SubscribeStreamingUsage(ctx, "task-1", 10000, func(usedTokens int64) {
		callbackCount.Add(1)
	})

	// 等待异步 callback（mockStreamingUsageProvider 异步调用 1 次）
	time.Sleep(100 * time.Millisecond)

	if sp.called != 1 {
		t.Errorf("SubscribeUsage 应被调用 1 次，got=%d", sp.called)
	}
	if callbackCount.Load() != 1 {
		t.Errorf("流式 callback 应被调用 1 次，got=%d", callbackCount.Load())
	}
}

// TestEarlyTerminator_StreamingSubscribeFail_Fallback 订阅失败 → 降级轮询。
func TestEarlyTerminator_StreamingSubscribeFail_Fallback(t *testing.T) {
	sp := &mockStreamingUsageProvider{supported: false} // 订阅失败
	e := newTestEarlyTerminator(nil, nil, nil, sp)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var callbackCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.SubscribeStreamingUsage(ctx, "task-1", 10000, func(usedTokens int64) {
			callbackCount.Add(1)
		})
	}()

	// 等待至少 1 次 callback
	deadline := time.Now().Add(11 * time.Second)
	for time.Now().Before(deadline) {
		if callbackCount.Load() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	if sp.called != 1 {
		t.Errorf("SubscribeUsage 应被尝试调用 1 次，got=%d", sp.called)
	}
	if callbackCount.Load() == 0 {
		t.Error("订阅失败应降级到轮询并触发 callback")
	}
}

// TestEarlyTerminator_ConcurrentCheck80_NoRace 致命反例 3 关联：
// 3 goroutine 同时 CheckAt80Percent，-race 无竞态。
// 零容忍：并发 CheckAt80Percent 必须无数据竞争。
func TestEarlyTerminator_ConcurrentCheck80_NoRace(t *testing.T) {
	t.Parallel()
	ua := &mockUserAsker{terminate: false}
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// 固定时间（避免 5min 去重影响）
	now := time.Now()
	e.SetNowFunc(func() time.Time { return now })

	const N = 3
	var wg sync.WaitGroup
	wg.Add(N)
	// 3 goroutine 用不同 taskID（避免 5min 去重）
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			taskID := "task-" + string(rune('A'+idx))
			_, _ = e.CheckAt80Percent(taskID, 8000)
		}(i)
	}

	// 等待完成（防死锁：用 timeout）
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// 正常完成
	case <-time.After(5 * time.Second):
		t.Fatal("致命反例 3 关联：CheckAt80Percent 死锁（5s 超时）")
	}

	// 3 次误判后应禁用 80%
	if !e.IsDisabled80Percent() {
		t.Error("3 次误判后应禁用 80%")
	}
}

// TestEarlyTerminator_ResetMiscalibrate 重置误判状态。
func TestEarlyTerminator_ResetMiscalibrate(t *testing.T) {
	ua := &mockUserAsker{terminate: false}
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// 3 次误判禁用 80%
	_, _ = e.CheckAt80Percent("task-1", 8000)
	_, _ = e.CheckAt80Percent("task-2", 8000)
	_, _ = e.CheckAt80Percent("task-3", 8000)
	if !e.IsDisabled80Percent() {
		t.Fatal("3 次误判后应禁用 80%")
	}

	// 重置
	e.ResetMiscalibrate()
	if e.IsDisabled80Percent() {
		t.Error("ResetMiscalibrate 后 IsDisabled80Percent 应=false")
	}
	if e.MiscalibrateCount() != 0 {
		t.Errorf("ResetMiscalibrate 后 MiscalibrateCount 应=0，got=%d", e.MiscalibrateCount())
	}
}

// TestEarlyTerminator_ClearRecentlyAsked 清理 recentlyAskedTerminate。
func TestEarlyTerminator_ClearRecentlyAsked(t *testing.T) {
	ua := &mockUserAsker{terminate: false}
	e := newTestEarlyTerminator(nil, nil, ua, nil)

	// 问 1 次
	_, _ = e.CheckAt80Percent("task-1", 8000)
	if ua.called != 1 {
		t.Fatalf("第 1 次应问用户，called=%d", ua.called)
	}

	// 清理后再问同 taskID → 应重新问
	e.ClearRecentlyAsked()
	_, _ = e.CheckAt80Percent("task-1", 8000)
	if ua.called != 2 {
		t.Errorf("ClearRecentlyAsked 后应重新问用户，called=%d", ua.called)
	}
}
