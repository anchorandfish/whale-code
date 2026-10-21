// evolution_loop_test.go — H12 自我进化闭环触发器反例测试（TDD 先行，§3.1）。
// 覆盖：
// - 闭环不收敛：ConvergenceGuard.ShouldStop=true 时 WeightTuner 跳过调优
// - 降级：enabled=false / nil receiver / nil deps 不 panic
// - taskID 空：返回 error
// - Feature Flag 关闭：enabled=false 时 Run/ValidatePattern/IncrementTaskCount 全降级
// - OnTaskDone 编排器集成：异步触发 Run，不阻塞主流程
// - StartBackgroundScanner：启动/停止/重复启动
//
// 设计说明 §3.1；落地：四步闭环触发器 + Feature Flag 默认 false
// SNAPSHOT: tests={DefaultFlags_Off, IsEnabled_True_WhenEnabled, IsAutoRun_True_WhenAutoRun, Run_EmptyTaskID_ReturnsErr, Run_CtxCancelled_ReturnsErr, Run_Disabled_ReturnsNil, Run_NilReceiver_IsEnabled_ReturnsFalse, Run_Enabled_ExtractAndTuneCalled, Run_NilDeps_Degrade_NoPanic, Run_ConvergenceGuardShouldStop_NoTuning, ValidatePattern_Disabled_ReturnsNil, ValidatePattern_NilValidator_Degrades, ValidatePattern_Enabled_PassesToValidator, IncrementTaskCount_Disabled_NoOp, IncrementTaskCount_Enabled_PassesToValidator, StartBackgroundScanner_Disabled_NoOp, StartBackgroundScanner_NilScanner_NoOp, StartBackgroundScanner_Enabled_StartsAndStops, StartBackgroundScanner_AlreadyRunning_ReturnsErr, SetNowFunc_OverridesTime, SetTTLScanInterval_OverridesInterval, OnTaskDone_NoLoop_ReturnsNil, OnTaskDone_EmptyTaskID_ReturnsErr, OnTaskDone_CtxCancelled_ReturnsErr, OnTaskDone_DisabledLoop_ReturnsNil, OnTaskDone_AutoRunFalse_DoesNotTriggerRun, OnTaskDone_Enabled_AsyncRun}
package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestEvolutionLoop 构造测试用 EvolutionLoop（全 deps 已注入 temp dir）。
// enabled/autoRun 控制两个 Feature Flag。
func newTestEvolutionLoop(t *testing.T, enabled, autoRun bool) (*EvolutionLoop, *ExperienceStore, *QualityStore, *L1ScorerExt, *ConvergenceGuard) {
	t.Helper()
	s := newTestExperienceStore(t)
	q := newTestQualityStore(t)
	scorer := NewL1ScorerExt(filepath.Join(t.TempDir(), "l1_weights.json"))
	guard := NewConvergenceGuard(filepath.Join(t.TempDir(), "last_tune.json"))

	extractor := NewPatternExtractor(s)
	validator := NewPatternValidator(s, q)
	ttlScanner := NewPatternTTLScanner(s)
	weightTuner := NewWeightTuner(scorer, q, guard)

	loop := NewEvolutionLoop(extractor, weightTuner, validator, ttlScanner, guard, enabled, autoRun)
	return loop, s, q, scorer, guard
}

// TestEvolutionLoop_DefaultFlags_Off 默认构造 enabled=false, autoRun=false。
func TestEvolutionLoop_DefaultFlags_Off(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, false, false)
	if loop.IsEnabled() {
		t.Error("默认 enabled 应为 false（ Feature Flag 默认关闭）")
	}
	if loop.IsAutoRun() {
		t.Error("默认 autoRun 应为 false（ Feature Flag 默认关闭）")
	}
}

// TestEvolutionLoop_IsEnabled_True_WhenEnabled enabled=true 时 IsEnabled 返回 true。
func TestEvolutionLoop_IsEnabled_True_WhenEnabled(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, false)
	if !loop.IsEnabled() {
		t.Error("enabled=true 时 IsEnabled 应返回 true")
	}
}

// TestEvolutionLoop_IsAutoRun_True_WhenAutoRun autoRun=true 时 IsAutoRun 返回 true。
func TestEvolutionLoop_IsAutoRun_True_WhenAutoRun(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	if !loop.IsAutoRun() {
		t.Error("autoRun=true 时 IsAutoRun 应返回 true")
	}
}

// TestEvolutionLoop_Run_EmptyTaskID_ReturnsErr taskID 空返回 error（v0.2 优化 3）。
func TestEvolutionLoop_Run_EmptyTaskID_ReturnsErr(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	err := loop.Run(context.Background(), "", nil)
	if err == nil {
		t.Fatal("taskID 空必须返回 error")
	}
	if !strings.Contains(err.Error(), "taskID") {
		t.Errorf("error 应含 taskID 关键字，got=%v", err)
	}
}

// TestEvolutionLoop_Run_CtxCancelled_ReturnsErr ctx 超时返回 ctx.Err()（v0.2 优化 3）。
func TestEvolutionLoop_Run_CtxCancelled_ReturnsErr(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := loop.Run(ctx, "task-1", nil)
	if err == nil {
		t.Fatal("ctx 超时必须返回 error")
	}
	if err != context.Canceled {
		t.Errorf("应返回 context.Canceled，got=%v", err)
	}
}

// TestEvolutionLoop_Run_Disabled_ReturnsNil Feature Flag 关闭时 Run 返回 nil（零成本降级）。
func TestEvolutionLoop_Run_Disabled_ReturnsNil(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, false, false)
	if err := loop.Run(context.Background(), "task-1", nil); err != nil {
		t.Errorf("enabled=false 时 Run 应返回 nil，got=%v", err)
	}
}

// TestEvolutionLoop_Run_NilReceiver_IsEnabled_ReturnsFalse nil receiver 调 IsEnabled 不 panic 返回 false。
func TestEvolutionLoop_Run_NilReceiver_IsEnabled_ReturnsFalse(t *testing.T) {
	var nilLoop *EvolutionLoop
	if nilLoop.IsEnabled() {
		t.Error("nil receiver IsEnabled 应返回 false（防 panic）")
	}
	if nilLoop.IsAutoRun() {
		t.Error("nil receiver IsAutoRun 应返回 false（防 panic）")
	}
}

// TestEvolutionLoop_Run_Enabled_ExtractAndTuneCalled enabled=true 时 Extract + Tune 都被调用。
// 验证：trace 注入后 ExperienceStore 应有新 Pattern（Extract 写入）。
func TestEvolutionLoop_Run_Enabled_ExtractAndTuneCalled(t *testing.T) {
	loop, s, _, _, _ := newTestEvolutionLoop(t, true, true)
	trace := makeTraceEntries()
	if err := loop.Run(context.Background(), "task-ev-1", trace); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 验证 Extract 已执行：ExperienceStore 应有新 Pattern
	patterns, err := s.ListByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	if len(patterns) == 0 {
		t.Error("Extract 应已写入新 Pattern，但 ListByScope 返回 0 条")
	}
}

// TestEvolutionLoop_Run_NilDeps_Degrade_NoPanic deps 全 nil 时 Run 不 panic 不返回错误。
func TestEvolutionLoop_Run_NilDeps_Degrade_NoPanic(t *testing.T) {
	loop := NewEvolutionLoop(nil, nil, nil, nil, nil, true, true)
	// 不应 panic，也不应返回错误（任一步骤失败 warn 不阻断）
	err := loop.Run(context.Background(), "task-nil-deps", makeTraceEntries())
	if err != nil {
		t.Errorf("nil deps 应降级返回 nil，got=%v", err)
	}
}

// TestEvolutionLoop_Run_ConvergenceGuardShouldStop_NoTuning 闭环不收敛：guard 触发 ShouldStop 后 Tune 跳过调优。
// 这是 §2.5 收敛保护三件套之一：权重变化 <1% 连续 2 次停止。
func TestEvolutionLoop_Run_ConvergenceGuardShouldStop_NoTuning(t *testing.T) {
	loop, _, q, scorer, guard := newTestEvolutionLoop(t, true, true)

	// 注入足够 L2 样本（≥100 + drift>0.2）使 Tune 想触发调优
	now := time.Now().UTC()
	for i := 0; i < 60; i++ {
		appendL2Record(t, q, "task-conv-a-"+string(rune('A'+i%26)), "pid", L2Adopted, 90, now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 0; i < 60; i++ {
		appendL2Record(t, q, "task-conv-d-"+string(rune('A'+i%26)), "pid", L2Discarded, 20, now.Add(-time.Duration(i)*time.Hour))
	}

	// 预先让 guard 进入 ShouldStop 状态：连续 2 次 <1% 变化
	before := map[string]int{"a": 50, "b": 50}
	afterSmall1 := map[string]int{"a": 50, "b": 50}
	afterSmall2 := map[string]int{"a": 50, "b": 50}
	_ = guard.ShouldStop(before, afterSmall1)
	if !guard.ShouldStop(before, afterSmall2) {
		t.Fatal("预置失败：连续 2 次小变化应触发 ShouldStop=true")
	}

	// 记录调优前权重版本
	beforeWeights := scorer.GetWeights()

	// Run 调 Tune，但 guard.ShouldStop=true 应跳过 SaveVersionedPublic
	if err := loop.Run(context.Background(), "task-conv-stop", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 验证：权重版本未变（ShouldStop 阻止了 SaveVersionedPublic）
	afterWeights := scorer.GetWeights()
	if afterWeights.Version != beforeWeights.Version {
		t.Errorf("ShouldStop=true 时 Tune 不应改变权重版本，before=%d after=%d",
			beforeWeights.Version, afterWeights.Version)
	}
}

// TestEvolutionLoop_ValidatePattern_Disabled_ReturnsNil enabled=false 时 ValidatePattern 返回 nil。
func TestEvolutionLoop_ValidatePattern_Disabled_ReturnsNil(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, false, false)
	if err := loop.ValidatePattern(context.Background(), "pid-1"); err != nil {
		t.Errorf("enabled=false 时 ValidatePattern 应返回 nil，got=%v", err)
	}
}

// TestEvolutionLoop_ValidatePattern_NilValidator_Degrades validator=nil 时降级返回 nil。
func TestEvolutionLoop_ValidatePattern_NilValidator_Degrades(t *testing.T) {
	loop := NewEvolutionLoop(nil, nil, nil, nil, nil, true, true)
	if err := loop.ValidatePattern(context.Background(), "pid-1"); err != nil {
		t.Errorf("validator=nil 应降级返回 nil，got=%v", err)
	}
}

// TestEvolutionLoop_ValidatePattern_Enabled_PassesToValidator enabled=true 时 ValidatePattern 转发给 validator。
// 验证：空 patternID 应返回 validator 的错误（说明 validator 被调用）。
func TestEvolutionLoop_ValidatePattern_Enabled_PassesToValidator(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	// 先 IncrementTaskCount 到 10 以上（绕过冷启动保护），让 Validate 真正校验 patternID
	for i := 0; i < PatternValidatorColdStartThreshold+1; i++ {
		loop.IncrementTaskCount()
	}
	// 空 patternID → validator 应返回 error（说明转发成功）
	err := loop.ValidatePattern(context.Background(), "")
	if err == nil {
		t.Fatal("enabled=true 且 validator 已被调用时，空 patternID 应返回 error")
	}
	if !strings.Contains(err.Error(), "patternID") {
		t.Errorf("error 应含 patternID 关键字，got=%v", err)
	}
}

// TestEvolutionLoop_IncrementTaskCount_Disabled_NoOp enabled=false 时 IncrementTaskCount 不转发。
func TestEvolutionLoop_IncrementTaskCount_Disabled_NoOp(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, false, false)
	// 应不 panic，且不改变 validator 内部计数
	loop.IncrementTaskCount()
	// 因 enabled=false 不转发，validator 的计数应为 0
	if loop.validator.GetTaskCount() != 0 {
		t.Errorf("enabled=false 时 IncrementTaskCount 不应转发到 validator，got=%d",
			loop.validator.GetTaskCount())
	}
}

// TestEvolutionLoop_IncrementTaskCount_Enabled_PassesToValidator enabled=true 时 IncrementTaskCount 转发到 validator。
func TestEvolutionLoop_IncrementTaskCount_Enabled_PassesToValidator(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	before := loop.validator.GetTaskCount()
	loop.IncrementTaskCount()
	after := loop.validator.GetTaskCount()
	if after != before+1 {
		t.Errorf("enabled=true 时 IncrementTaskCount 应使 validator 计数 +1，before=%d after=%d",
			before, after)
	}
}

// TestEvolutionLoop_StartBackgroundScanner_Disabled_NoOp enabled=false 时 StartBackgroundScanner 返回 no-op stop。
func TestEvolutionLoop_StartBackgroundScanner_Disabled_NoOp(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, false, false)
	stop, err := loop.StartBackgroundScanner(context.Background())
	if err != nil {
		t.Errorf("enabled=false 应返回 no-op stop 无错误，got=%v", err)
	}
	if stop == nil {
		t.Fatal("stop func 不应为 nil")
	}
	stop() // 应可调用不 panic
}

// TestEvolutionLoop_StartBackgroundScanner_NilScanner_NoOp ttlScanner=nil 时返回 no-op stop。
func TestEvolutionLoop_StartBackgroundScanner_NilScanner_NoOp(t *testing.T) {
	loop := NewEvolutionLoop(nil, nil, nil, nil, nil, true, true)
	stop, err := loop.StartBackgroundScanner(context.Background())
	if err != nil {
		t.Errorf("nil ttlScanner 应返回 no-op stop 无错误，got=%v", err)
	}
	if stop == nil {
		t.Fatal("stop func 不应为 nil")
	}
	stop()
}

// TestEvolutionLoop_StartBackgroundScanner_Enabled_StartsAndStops enabled=true 时启动后台 goroutine 并能优雅停止。
func TestEvolutionLoop_StartBackgroundScanner_Enabled_StartsAndStops(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	// 用极短间隔便于快速验证
	loop.SetTTLScanInterval(50 * time.Millisecond)

	stop, err := loop.StartBackgroundScanner(context.Background())
	if err != nil {
		t.Fatalf("StartBackgroundScanner: %v", err)
	}
	if stop == nil {
		t.Fatal("stop func 不应为 nil")
	}

	// 等待至少 2 个 tick（确保 goroutine 已启动并在跑）
	time.Sleep(120 * time.Millisecond)

	// 优雅停止（应不阻塞）
	stop()

	// 验证 scanRunning=false
	if loop.scanRunning {
		t.Error("stop 后 scanRunning 应为 false")
	}
}

// TestEvolutionLoop_StartBackgroundScanner_AlreadyRunning_ReturnsErr 重复启动返回错误。
func TestEvolutionLoop_StartBackgroundScanner_AlreadyRunning_ReturnsErr(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	loop.SetTTLScanInterval(1 * time.Second) // 长间隔避免测试中触发实际扫描

	stop1, err := loop.StartBackgroundScanner(context.Background())
	if err != nil {
		t.Fatalf("首次 StartBackgroundScanner: %v", err)
	}
	defer stop1()

	// 第二次启动应返回错误
	stop2, err := loop.StartBackgroundScanner(context.Background())
	if err == nil {
		stop2()
		t.Fatal("重复启动应返回 error")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error 应含 'already running'，got=%v", err)
	}
}

// TestEvolutionLoop_SetNowFunc_OverridesTime SetNowFunc 注入时间函数。
func TestEvolutionLoop_SetNowFunc_OverridesTime(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	fixed := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	loop.SetNowFunc(func() time.Time { return fixed })
	// 验证：调用后 nowFunc 返回固定时间
	if got := loop.nowFunc(); !got.Equal(fixed) {
		t.Errorf("SetNowFunc 后 nowFunc = %v, want %v", got, fixed)
	}
}

// TestEvolutionLoop_SetTTLScanInterval_OverridesInterval SetTTLScanInterval 注入扫描间隔。
func TestEvolutionLoop_SetTTLScanInterval_OverridesInterval(t *testing.T) {
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	custom := 5 * time.Minute
	loop.SetTTLScanInterval(custom)
	if loop.ttlScanInterval != custom {
		t.Errorf("SetTTLScanInterval 后 ttlScanInterval = %v, want %v",
			loop.ttlScanInterval, custom)
	}
}

// TestEvolutionLoop_OnTaskDone_NoLoop_ReturnsNil 编排器未注入 loop 时 OnTaskDone 返回 nil（零成本降级）。
func TestEvolutionLoop_OnTaskDone_NoLoop_ReturnsNil(t *testing.T) {
	o := newTestOrchestrator(t)
	// 不调 SetEvolutionLoop，evolutionLoop 为 nil
	if err := o.OnTaskDone(context.Background(), "task-1", nil); err != nil {
		t.Errorf("未注入 loop 时 OnTaskDone 应返回 nil，got=%v", err)
	}
}

// TestEvolutionLoop_OnTaskDone_EmptyTaskID_ReturnsErr taskID 空返回 error。
func TestEvolutionLoop_OnTaskDone_EmptyTaskID_ReturnsErr(t *testing.T) {
	o := newTestOrchestrator(t)
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	o.SetEvolutionLoop(loop)
	err := o.OnTaskDone(context.Background(), "", nil)
	if err == nil {
		t.Fatal("taskID 空应返回 error")
	}
	if !strings.Contains(err.Error(), "taskID") {
		t.Errorf("error 应含 taskID 关键字，got=%v", err)
	}
}

// TestEvolutionLoop_OnTaskDone_CtxCancelled_ReturnsErr ctx 超时返回 ctx.Err()。
func TestEvolutionLoop_OnTaskDone_CtxCancelled_ReturnsErr(t *testing.T) {
	o := newTestOrchestrator(t)
	loop, _, _, _, _ := newTestEvolutionLoop(t, true, true)
	o.SetEvolutionLoop(loop)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := o.OnTaskDone(ctx, "task-1", nil)
	if err == nil {
		t.Fatal("ctx 超时应返回 error")
	}
	if err != context.Canceled {
		t.Errorf("应返回 context.Canceled，got=%v", err)
	}
}

// TestEvolutionLoop_OnTaskDone_DisabledLoop_ReturnsNil loop 未启用时 OnTaskDone 返回 nil（不触发 Run）。
func TestEvolutionLoop_OnTaskDone_DisabledLoop_ReturnsNil(t *testing.T) {
	o := newTestOrchestrator(t)
	loop, s, _, _, _ := newTestEvolutionLoop(t, false, false)
	o.SetEvolutionLoop(loop)

	beforeCount := len(mustListPatterns(t, s))
	if err := o.OnTaskDone(context.Background(), "task-disabled", makeTraceEntries()); err != nil {
		t.Errorf("disabled loop 时 OnTaskDone 应返回 nil，got=%v", err)
	}
	// 等待潜在异步 Run 完成（不应触发 Extract，因 enabled=false）
	time.Sleep(50 * time.Millisecond)
	afterCount := len(mustListPatterns(t, s))
	if afterCount != beforeCount {
		t.Errorf("disabled loop 时 OnTaskDone 不应触发 Extract，before=%d after=%d",
			beforeCount, afterCount)
	}
}

// TestEvolutionLoop_OnTaskDone_AutoRunFalse_DoesNotTriggerRun 验证 autoRun=false 时 OnTaskDone 不触发 Run。
// 反例：Feature Flag 双开关语义（§3.1+§4.2），enabled=true + autoRun=false 时即使 H12 已启用，
// 也不应自动触发 Run（用户意图：仅手动调用 Run）。
// 验证手段：注入可触发 Extract 的 trace，调 OnTaskDone 后等待潜在异步 Run 完成，
// 断言 ExperienceStore 中无新 Pattern 写入（即 Run 未被调用 → Extract 未执行）。
func TestEvolutionLoop_OnTaskDone_AutoRunFalse_DoesNotTriggerRun(t *testing.T) {
	o := newTestOrchestrator(t)
	// 关键：enabled=true + autoRun=false（反例核心：启用 H12 但仅手动触发）
	loop, s, _, _, _ := newTestEvolutionLoop(t, true, false)
	o.SetEvolutionLoop(loop)

	// 双重确认 Feature Flag 状态
	if !loop.IsEnabled() {
		t.Fatal("前置失败：enabled 应为 true")
	}
	if loop.IsAutoRun() {
		t.Fatal("前置失败：autoRun 应为 false")
	}

	beforeCount := len(mustListPatterns(t, s))

	// 调 OnTaskDone（应立即返回 nil，且不应启动异步 Run）
	if err := o.OnTaskDone(context.Background(), "task-autorun-false", makeTraceEntries()); err != nil {
		t.Fatalf("OnTaskDone 应返回 nil，got=%v", err)
	}

	// 等待足够时间，确保即使有泄漏的异步 Run 也已落盘（与 Enabled_AsyncRun 测试对称）
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		// 主动让出 CPU，给潜在的异步 goroutine 机会执行
		time.Sleep(50 * time.Millisecond)
	}

	afterCount := len(mustListPatterns(t, s))
	if afterCount != beforeCount {
		t.Errorf("autoRun=false 时 OnTaskDone 不应触发 Run → Extract，before=%d after=%d（应相等）",
			beforeCount, afterCount)
	}
}

// TestEvolutionLoop_OnTaskDone_Enabled_AsyncRun loop 启用时 OnTaskDone 异步触发 Run。
// 验证：异步 Run 后 ExperienceStore 应有新 Pattern（Extract 写入）。
func TestEvolutionLoop_OnTaskDone_Enabled_AsyncRun(t *testing.T) {
	o := newTestOrchestrator(t)
	loop, s, _, _, _ := newTestEvolutionLoop(t, true, true)
	o.SetEvolutionLoop(loop)

	beforeCount := len(mustListPatterns(t, s))

	// 调 OnTaskDone（应立即返回，不阻塞）
	done := make(chan error, 1)
	go func() {
		done <- o.OnTaskDone(context.Background(), "task-async-1", makeTraceEntries())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OnTaskDone 应返回 nil，got=%v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("OnTaskDone 应立即返回不阻塞")
	}

	// 等待异步 Run 完成（最长 2 秒）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mustListPatterns(t, s)) > beforeCount {
			break // 已观察到新 Pattern 写入
		}
		time.Sleep(10 * time.Millisecond)
	}

	afterCount := len(mustListPatterns(t, s))
	if afterCount <= beforeCount {
		t.Errorf("异步 Run 应通过 Extract 写入新 Pattern，before=%d after=%d",
			beforeCount, afterCount)
	}
}

// TestEvolutionLoop_OnTaskDone_Concurrent_NoRace 并发调 OnTaskDone 无竞态（-race 验证）。
// 注：OnTaskDone 内部异步 goroutine 触发 loop.Run，需等待所有异步 Run 完成后再退出，
// 否则 t.TempDir() 清理时仍有 goroutine 在写 ExperienceStore → "directory not empty"。
func TestEvolutionLoop_OnTaskDone_Concurrent_NoRace(t *testing.T) {
	o := newTestOrchestrator(t)
	loop, s, _, _, _ := newTestEvolutionLoop(t, true, true)
	o.SetEvolutionLoop(loop)

	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			_ = o.OnTaskDone(context.Background(), "task-race-"+string(rune('A'+idx)), makeTraceEntries())
		}(i)
	}
	wg.Wait()

	// 等待异步 goroutine 完成：轮询 ExperienceStore 直到 pattern 数量稳定（达到 N 个）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(mustListPatterns(t, s)) >= N {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 额外等待 200ms 确保所有 in-flight 文件写入完成（fsync）
	time.Sleep(200 * time.Millisecond)
}

// mustListPatterns 辅助：列 ExperienceStore 所有 project scope Pattern，失败 Fatal。
func mustListPatterns(t *testing.T, s *ExperienceStore) []*Pattern {
	t.Helper()
	patterns, err := s.ListByScope(ScopeProject)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	return patterns
}

// === B-P0-1/3 反例测试（桶 X2 修复回核）===

// makeTraceEntriesWithPatternID 构造含 PatternID 的 trace（B-P0-3 字段）。
// 用于 B-P0-1/3 反例测试：验证 EvolutionLoop.Run 从 trace 提取 PatternID 并调 Validate。
func makeTraceEntriesWithPatternID(patternID string) []TraceEntry {
	entries := makeTraceEntries()
	for i := range entries {
		entries[i].PatternID = patternID
	}
	return entries
}

// primeValidatorForValidate 预置 validator 状态使其能真正执行 Validate（绕过冷启动保护）。
// 注入 1 个 unverified Pattern + 2 条 L2=adopted + L1Score>=60 评分 → Validate 应将 Pattern 升级到 provisionally_verified。
// 返回注入的 patternID 供调用方构造 trace。
func primeValidatorForValidate(t *testing.T, loop *EvolutionLoop, s *ExperienceStore, q *QualityStore) string {
	t.Helper()
	pid := "pid-bp013-validate"
	pattern := makeTestPattern(pid, "edit auth handler", ScopeProject, VerifiedStateUnverified)
	if err := s.Save(pattern); err != nil {
		t.Fatalf("Save pattern: %v", err)
	}
	// 注入 2 条 L2=adopted + L1Score>=60（满足升级条件 PatternValidatorUpgradeConsecutive=2）
	now := time.Now().UTC()
	appendL2Record(t, q, "task-bp013-a", pid, L2Adopted, 75, now.Add(-2*time.Hour))
	appendL2Record(t, q, "task-bp013-b", pid, L2Adopted, 80, now.Add(-1*time.Hour))
	// 累计 IncrementTaskCount 超过冷启动阈值（PatternValidatorColdStartThreshold=10）
	for i := 0; i < PatternValidatorColdStartThreshold+1; i++ {
		loop.IncrementTaskCount()
	}
	return pid
}

// TestEvolutionLoop_Run_WithPatternID_CallsValidate B-P0-1/3 正例：
// trace 含 PatternID 时，EvolutionLoop.Run 应调 validator.Validate，Pattern 升级到 provisionally_verified。
// 验证目标（B-P0-1/3 修复）：Run 第⑥步从 trace 提取 PatternID 并逐一调 Validate（失败 warn 不阻断）。
func TestEvolutionLoop_Run_WithPatternID_CallsValidate(t *testing.T) {
	loop, s, q, _, _ := newTestEvolutionLoop(t, true, true)
	pid := primeValidatorForValidate(t, loop, s, q)

	// 构造含 PatternID 的 trace（B-P0-3 字段）
	trace := makeTraceEntriesWithPatternID(pid)

	if err := loop.Run(context.Background(), "task-bp013-pos", trace); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 验证 Pattern 已升级到 provisionally_verified（说明 Validate 被调用并执行了流转）
	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get pattern %s: %v", pid, err)
	}
	if got.VerifiedState != VerifiedStateProvisionallyVerified {
		t.Errorf("B-P0-1/3 正例：trace 含 PatternID 时 Run 应调 Validate 升级 Pattern，want=%s got=%s",
			VerifiedStateProvisionallyVerified, got.VerifiedState)
	}
}

// TestEvolutionLoop_Run_WithoutPatternID_DoesNotCallValidate B-P0-1/3 反例：
// trace 不含 PatternID 时，EvolutionLoop.Run 不应调 validator.Validate，Pattern 保持 unverified。
// 验证目标（B-P0-1/3 修复）：extractHitPatternIDs 返回空 → Validate 步骤无操作。
// 反例核心：即便 validator 已就绪（冷启动已过 + L2 评分充足），trace 不含 PatternID 也不应触发 Validate。
func TestEvolutionLoop_Run_WithoutPatternID_DoesNotCallValidate(t *testing.T) {
	loop, s, q, _, _ := newTestEvolutionLoop(t, true, true)
	pid := primeValidatorForValidate(t, loop, s, q)

	// 构造不含 PatternID 的 trace（makeTraceEntries 默认 PatternID 为空）
	trace := makeTraceEntries()

	if err := loop.Run(context.Background(), "task-bp013-neg", trace); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 验证 Pattern 仍是 unverified（说明 Validate 未被调用，未触发流转）
	got, err := s.Get(pid)
	if err != nil {
		t.Fatalf("Get pattern %s: %v", pid, err)
	}
	if got.VerifiedState != VerifiedStateUnverified {
		t.Errorf("B-P0-1/3 反例：trace 不含 PatternID 时 Run 不应调 Validate，Pattern 应保持 unverified，want=%s got=%s",
			VerifiedStateUnverified, got.VerifiedState)
	}
}

// TestEvolutionLoop_ExtractHitPatternIDs_Dedup B-P0-3 辅助函数：
// extractHitPatternIDs 去重保序，避免同一 PatternID 多次触发 Validate。
func TestEvolutionLoop_ExtractHitPatternIDs_Dedup(t *testing.T) {
	trace := []TraceEntry{
		{SubTaskID: "s1", PatternID: "pid-a"},
		{SubTaskID: "s2", PatternID: "pid-b"},
		{SubTaskID: "s3", PatternID: "pid-a"}, // 重复
		{SubTaskID: "s4", PatternID: ""},      // 空跳过
		{SubTaskID: "s5", PatternID: "pid-c"},
	}
	ids := extractHitPatternIDs(trace)
	want := []string{"pid-a", "pid-b", "pid-c"}
	if len(ids) != len(want) {
		t.Fatalf("extractHitPatternIDs 应返回 %d 个唯一 ID，got %d (%v)", len(want), len(ids), ids)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("extractHitPatternIDs[%d] = %q, want %q（去重保序）", i, ids[i], w)
		}
	}
}
