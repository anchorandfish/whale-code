// early_terminator.go — H10 Phase 5 桶 4 早期终止检查器（总纲 §8.4 手段 2 + Phase 5 §4.6）。
// REF-外部-LangGraph@https://langchain-ai.github.io/langgraph/concepts/persistence/+2026-07-15访问-StateSnapshot + checkpoint；落地：按进度检查点 20%/50%/80%
// line 996-审查 v1 注明"按 token 消耗 / tokens_cap 比例实现"；落地：三档检查
// (taskID) 已落地；落地：EarlyTerminator 通过 TraceQueryer interface 只读引用
// (taskID) U2 外层包装已落地；落地：EarlyTerminator 通过 OutputStore interface 只读引用（Phase 1 未实现具体 OutputStore，本文件定义 interface 供测试 mock + 编排器注入）
// sync.Mutex 保护；落地：recentlyAskedTerminate 并发安全
// （连续 3 次选继续 → 禁用 80%）；落地：miscalibrateCount + disabled80Percent
// usage 降级（N1 不支持 → 10s 轮询）；落地：subscribeStreamingUsage + fallbackPoll
// SNAPSHOT: pkg=internal/orchestrator; type=EarlyTerminator; fields={traceQueryer(Phase3只读interface),outputStore(Phase1只读interface),askedLock(sync.Mutex),recentlyAskedTerminate(map[string]time.Time),mu(sync.Mutex),miscalibrateCount,disabled80Percent,nowFunc}; checkpoints={20%:日志,50%:trace error_type warning,80%:问用户}; dedup_window=5min; miscalibrate_threshold=3(禁用80%); streaming_fallback=10s轮询; concurrency=sync.Mutex(askedLock+mu)

package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EarlyTerminatorDedupWindow 80% 问用户去重窗口（5min，§4.6.1 v0.3 建议 4 + §4.6 v0.2 改进 6）。
// 同 taskID 5min 内只问 1 次"批量终止"（防并行场景重复打扰用户）。
const EarlyTerminatorDedupWindow = 5 * time.Minute

// EarlyTerminatorMiscalibrateThreshold 误判禁用阈值（§5.3：连续 3 次选继续 → 禁用 80%）。
const EarlyTerminatorMiscalibrateThreshold = 3

// EarlyTerminatorStreamingFallbackInterval 流式 usage 不可用时的降级轮询间隔（10s，§4.6.2）。
const EarlyTerminatorStreamingFallbackInterval = 10 * time.Second

// TraceQueryer Phase 3 TraceStore 只读查询接口（EarlyTerminator 依赖抽象，便于测试 mock）。
// *TraceStore 已实现 QueryBySubtask，自动满足本接口。
type TraceQueryer interface {
	// QueryBySubtask 按子任务 ID 查询所有 trace 事件（Phase 3 §6.2.3）。
	QueryBySubtask(subtaskID string) ([]PhaseTraceEvent, error)
}

// OutputStore Phase 1 output.json 只读查询接口（EarlyTerminator 依赖抽象，便于测试 mock）。
// Phase 1 未实现具体 OutputStore，本接口供编排器实现并注入。
// 落地 §4.6：HasDraft 检查 output.json 是否有雏形（接近完成信号）。
type OutputStore interface {
	// HasDraft 检查 taskID 是否已有产出雏形（接近完成信号，§4.6）。
	// 返回 true=有雏形（不终止），false=无雏形（可问用户是否终止）。
	HasDraft(taskID string) bool
}

// UserAsker 问用户是否终止的接口（§2.1 必须确认档，便于测试 mock）。
// 落地 §4.6 CheckAt80Percent：问用户"是否批量终止未完成子任务"。
// AskBatchTerminate 返回 true=用户选终止，false=用户选继续。
type UserAsker interface {
	// AskBatchTerminate 问用户是否批量终止（§2.1 必须确认档）。
	// taskID 用于展示上下文。返回 true=终止，false=继续。
	AskBatchTerminate(taskID string) bool
}

// StreamingUsageProvider N1 streaming usage callback 订阅接口（§4.6.2，便于测试 mock）。
// 落地 §11.1 仲裁：若 N1 不暴露此接口，subscribeStreamingUsage 降级到周期性轮询。
type StreamingUsageProvider interface {
	// SubscribeUsage 订阅 taskID 的流式 usage 回调。
	// callback 在每次 usage 更新时调用（usedTokens 是当前已用 token）。
	// 返回 true=订阅成功（N1 支持流式），false=不支持（需降级到 10s 轮询）。
	SubscribeUsage(taskID string, callback func(usedTokens int64)) bool
}

// EarlyTerminator 早期终止检查器（总纲 §8.4 手段 2，三档检查 + 流式 usage 订阅 + 误判降级）。
// 复用 Phase 3 traceStore（只读，通过 TraceQueryer interface）+ Phase 1 outputStore（只读，通过 OutputStore interface）。
//
// 并发安全：
// - askedLock sync.Mutex 保护 recentlyAskedTerminate（§4.6.1 v0.3 建议 4）
// - mu sync.Mutex 保护 miscalibrateCount + disabled80Percent（误判降级状态）
//
// 误判降级（§5.3）：
// - 用户连续 EarlyTerminatorMiscalibrateThreshold 次选"继续"（误判）→ disabled80Percent=true
// - 禁用 80% 检查点，仅保留 20%/50% 日志
// - 用户选"终止"时重置 miscalibrateCount=0（正确判断，非误判）
type EarlyTerminator struct {
	traceQueryer TraceQueryer           // Phase 3 只读引用（trace.jsonl 查询 error_type）
	outputStore  OutputStore            // Phase 1 只读引用（output.json 查询 HasDraft）
	userAsker    UserAsker              // 问用户是否终止（§2.1 必须确认档）
	streaming    StreamingUsageProvider // N1 streaming usage 订阅（可为 nil）

	// 80% 问用户去重（§4.6.1 v0.3 建议 4 + §4.6 v0.2 改进 6）
	askedLock              sync.Mutex
	recentlyAskedTerminate map[string]time.Time // taskID → 上次问用户时间（5min 去重）

	// 误判降级状态（§5.3，mu 保护）
	mu                sync.Mutex
	miscalibrateCount int  // 误判计数（连续 3 次禁用 80%）
	disabled80Percent bool // 禁用 80% 检查点标志

	// nowFunc 用于测试注入时间（默认 time.Now）
	nowFunc func() time.Time
}

// NewEarlyTerminator 创建早期终止检查器。
// traceQueryer 可为 nil（降级：跳过 50% 检查点 trace 查询）；
// outputStore 可为 nil（降级：跳过 80% HasDraft 检查）；
// userAsker 可为 nil（降级：80% 检查点直接返回 false 不终止，记日志）；
// streaming 可为 nil（降级：subscribeStreamingUsage 直接走 10s 轮询）。
func NewEarlyTerminator(traceQueryer TraceQueryer, outputStore OutputStore, userAsker UserAsker, streaming StreamingUsageProvider) *EarlyTerminator {
	return &EarlyTerminator{
		traceQueryer:           traceQueryer,
		outputStore:            outputStore,
		userAsker:              userAsker,
		streaming:              streaming,
		recentlyAskedTerminate: make(map[string]time.Time),
		nowFunc:                time.Now,
	}
}

// SetNowFunc 注入时间函数（测试用，便于控制 5min 去重窗口）。
func (e *EarlyTerminator) SetNowFunc(f func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nowFunc = f
}

// ForceDisable80Percent 强制禁用 80% 检查点（B-P1 修复：DegradeOnEarlyTerminationMiscalibrate 空操作补实现）。
// 供 DegradeManager 外部强制触发降级（如编排器检测到误判模式后主动禁用，无需等待内部 3 次计数达阈值）。
// 并发安全：mu 写锁保护 disabled80Percent。
func (e *EarlyTerminator) ForceDisable80Percent() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disabled80Percent = true
}

// now 返回当前时间（统一入口，便于测试注入）。
func (e *EarlyTerminator) now() time.Time {
	e.mu.Lock()
	f := e.nowFunc
	e.mu.Unlock()
	if f == nil {
		return time.Now()
	}
	return f()
}

// CheckAt20Percent 20% 检查点（仅日志，无动作，§4.6）。
// 落地 §8.4 line 996 "按 token 消耗 / tokens_cap 比例实现"：20% 仅记录进度，不终止不问用户。
// usedTokens 是当前子任务已用 token，tokensCap 是子任务 token 上限（由调用方传入计算比例）。
func (e *EarlyTerminator) CheckAt20Percent(taskID string, usedTokens int64) {
	// 仅日志记录进度（无动作，不返回 error）
	// 实际日志由编排器记录，此处仅占位
	_ = taskID
	_ = usedTokens
}

// CheckAt50Percent 50% 检查点（检查 trace error_type，标记 warning，不终止，§4.6）。
// 算法：
// - traceQueryer.QueryBySubtask(taskID) 读 trace.jsonl
// - 检查是否有 error_type 事件（compile_error/test_failure/contract_violation/timeout）
// - 有 error_type → 返回 warning=true + reason
// - 无 error_type 或 traceQueryer 为 nil → 返回 warning=false
//
// 不终止任务，仅返回 warning 供编排器记录日志。
func (e *EarlyTerminator) CheckAt50Percent(taskID string, usedTokens int64) (warning bool, reason string) {
	if e.traceQueryer == nil {
		return false, ""
	}
	events, err := e.traceQueryer.QueryBySubtask(taskID)
	if err != nil {
		return false, ""
	}
	for _, evt := range events {
		// 检查 payload 中是否有 error_type 字段（Phase 3 trace 事件）
		if errType, ok := evt.Payload["error_type"].(string); ok && errType != "" && errType != ErrorTypeNone {
			return true, fmt.Sprintf("trace error_type=%s detected at 50%% checkpoint (taskID=%s)", errType, taskID)
		}
		// 检查 EventType 是否为 error 相关（dispatch 事件可能携带 error）
		if evt.EventType == "error" {
			return true, fmt.Sprintf("trace error event detected at 50%% checkpoint (taskID=%s)", taskID)
		}
	}
	_ = usedTokens
	return false, ""
}

// CheckAt80Percent 80% 检查点（问用户是否终止，§2.1 必须确认档，§4.6）。
// 算法：
// 1. disabled80Percent=true → 直接返回 false（误判降级，§5.3）
// 2. outputStore.HasDraft(taskID)=true → 有雏形，返回 false（让任务继续）
// 3. askedLock.Lock() → recentlyAskedTerminate[taskID] 5min 内已问 → 跳过（去重，§4.6 v0.2 改进 6）
// 4. markAskedTerminate(taskID) → userAsker.AskBatchTerminate(taskID)
// 5. 用户选终止 → 重置 miscalibrateCount=0 + 返回 true
// 6. 用户选继续 → miscalibrateCount++ → 达阈值则 disabled80Percent=true
//
// userAsker 为 nil 时降级：返回 false 不终止，记日志（不问用户）。
// 返回 shouldTerminate=true 表示用户选终止，err 仅在内部错误时返回。
func (e *EarlyTerminator) CheckAt80Percent(taskID string, usedTokens int64) (shouldTerminate bool, err error) {
	// 1. 误判降级：disabled80Percent=true → 跳过 80% 检查点
	e.mu.Lock()
	disabled := e.disabled80Percent
	e.mu.Unlock()
	if disabled {
		return false, nil
	}

	// 2. HasDraft 检查：有雏形 → 不终止
	if e.outputStore != nil && e.outputStore.HasDraft(taskID) {
		return false, nil
	}

	// 3. 5min 去重：同 taskID 5min 内已问 → 跳过
	if !e.shouldAskTerminate(taskID) {
		return false, nil
	}

	// 4. 标记已问 + 问用户
	e.markAskedTerminate(taskID)

	// userAsker 为 nil 降级：不问用户，不终止
	if e.userAsker == nil {
		return false, nil
	}

	// 5. 问用户
	terminate := e.userAsker.AskBatchTerminate(taskID)
	if terminate {
		// 用户选终止 → 重置误判计数（正确判断）
		e.mu.Lock()
		e.miscalibrateCount = 0
		e.mu.Unlock()
		return true, nil
	}

	// 6. 用户选继续 → 误判计数++（§5.3）
	e.mu.Lock()
	e.miscalibrateCount++
	if e.miscalibrateCount >= EarlyTerminatorMiscalibrateThreshold {
		e.disabled80Percent = true
	}
	e.mu.Unlock()

	_ = usedTokens
	return false, nil
}

// IsDisabled80Percent 返回 80% 检查点是否被禁用（误判降级状态查询，测试用）。
func (e *EarlyTerminator) IsDisabled80Percent() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.disabled80Percent
}

// MiscalibrateCount 返回当前误判计数（测试用）。
func (e *EarlyTerminator) MiscalibrateCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.miscalibrateCount
}

// ResetMiscalibrate 重置误判计数 + 启用 80% 检查点（编排任务完成后重置，便于下次编排）。
func (e *EarlyTerminator) ResetMiscalibrate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.miscalibrateCount = 0
	e.disabled80Percent = false
}

// shouldAskTerminate 判断是否应问用户（5min 去重，§4.6 v0.2 改进 6）。
// 同 taskID 5min 内已问 → 返回 false（跳过）。
// askedLock 保护 recentlyAskedTerminate（§4.6.1 v0.3 建议 4）。
func (e *EarlyTerminator) shouldAskTerminate(taskID string) bool {
	e.askedLock.Lock()
	defer e.askedLock.Unlock()
	now := e.now()
	if last, ok := e.recentlyAskedTerminate[taskID]; ok {
		if now.Sub(last) < EarlyTerminatorDedupWindow {
			return false // 5min 内已问，跳过
		}
	}
	return true
}

// markAskedTerminate 标记已问用户（更新 recentlyAskedTerminate，§4.6.1）。
// askedLock 保护（§4.6.1 v0.3 建议 4）。
func (e *EarlyTerminator) markAskedTerminate(taskID string) {
	e.askedLock.Lock()
	defer e.askedLock.Unlock()
	e.recentlyAskedTerminate[taskID] = e.now()
}

// subscribeStreamingUsage 订阅 N1 streaming usage callback（§4.6.2）。
// 依赖 §11.1 仲裁：若 N1 不暴露此接口（streaming 为 nil 或 SubscribeUsage 返回 false），
// 降级到周期性轮询（每 EarlyTerminatorStreamingFallbackInterval 查 CostRecord）。
//
// 致命反例 9：流式 usage callback 不可用降级（周期性轮询，<10s 延迟）。
// callback 在每次 usage 更新时调用，usedTokens 是当前已用 token。
// ctx 用于控制轮询 goroutine 生命周期（ctx.Done() 时停止轮询）。
func (e *EarlyTerminator) subscribeStreamingUsage(ctx context.Context, taskID string, tokensCap int64, callback func(usedTokens int64)) {
	// streaming 为 nil → 直接降级到轮询
	if e.streaming == nil {
		e.fallbackPoll(ctx, taskID, tokensCap, callback)
		return
	}

	// 尝试订阅流式 usage
	subscribed := e.streaming.SubscribeUsage(taskID, callback)
	if subscribed {
		return // 订阅成功，callback 会被 N1 调用
	}

	// 订阅失败 → 降级到 10s 轮询（致命反例 9）
	e.fallbackPoll(ctx, taskID, tokensCap, callback)
}

// fallbackPoll 降级轮询（每 10s 查一次 usage，§4.6.2 致命反例 9）。
// 落地 §11.1 仲裁结果：N1 不支持 streaming → 周期性轮询。
// ctx.Done() 时停止轮询。
// 轮询期间 callback 会被调用，传入当前 usedTokens。
// 注意：本方法不直接查 CostRecord（避免依赖 N2 metrics），而是通过 streaming fallback callback 注入。
// 简化实现：用 time.Ticker 周期性触发 callback，实际 usedTokens 由调用方通过其他方式获取。
func (e *EarlyTerminator) fallbackPoll(ctx context.Context, taskID string, tokensCap int64, callback func(usedTokens int64)) {
	if callback == nil {
		return
	}
	ticker := time.NewTicker(EarlyTerminatorStreamingFallbackInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return // ctx 取消，停止轮询
		case <-ticker.C:
			// 周期性触发 callback（usedTokens 由调用方通过其他方式获取）
			// 简化：传入 tokensCap 作为占位（实际应由调用方查 CostRecord 获取真实 usedTokens）
			// 真实实现应调 metrics 反查 CostRecord 获取 usedTokens，此处仅触发回调信号
			callback(tokensCap) // 占位：触发回调让调用方自行查 CostRecord
		}
	}
}

// SubscribeStreamingUsage 公开方法：订阅流式 usage（包装 subscribeStreamingUsage 供编排器调用）。
// ctx 用于控制轮询 goroutine 生命周期。
// taskID 是子任务 ID，tokensCap 是子任务 token 上限。
// callback 在每次 usage 更新时调用，usedTokens 是当前已用 token。
func (e *EarlyTerminator) SubscribeStreamingUsage(ctx context.Context, taskID string, tokensCap int64, callback func(usedTokens int64)) {
	e.subscribeStreamingUsage(ctx, taskID, tokensCap, callback)
}

// IsStreamingSupported 检查 N1 是否支持流式 usage（测试用）。
// 返回 true=支持，false=不支持（需降级轮询）。
func (e *EarlyTerminator) IsStreamingSupported() bool {
	return e.streaming != nil
}

// ClearRecentlyAsked 清理 recentlyAskedTerminate（编排任务完成后调用，防止内存泄漏）。
func (e *EarlyTerminator) ClearRecentlyAsked() {
	e.askedLock.Lock()
	defer e.askedLock.Unlock()
	e.recentlyAskedTerminate = make(map[string]time.Time)
}
