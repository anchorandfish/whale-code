// Package ai — recovery coordinator for retry/degrade/compress orchestration
package ai

import (
	"sync"
	"time"
)

// RecoveryState represents the current recovery state
type RecoveryState int

const (
	StateNormal      RecoveryState = iota // 正常
	StateRetrying                         // 重试中
	StateDegrading                        // 降级中
	StateCompressing                      // 压缩中
	StateFailed                           // 彻底失败
)

// String returns the string representation of RecoveryState
func (s RecoveryState) String() string {
	switch s {
	case StateNormal:
		return "normal"
	case StateRetrying:
		return "retrying"
	case StateDegrading:
		return "degrading"
	case StateCompressing:
		return "compressing"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// RecoveryCoordinator coordinates retry, degradation, and compression recovery strategies
// to prevent cascading failures and infinite recovery loops.
// State machine: Normal → Retrying → Degrading → Compressing → Failed
// Each transition is one-way; success resets to Normal.
type RecoveryCoordinator struct {
	mu                sync.Mutex
	state             RecoveryState
	consecutiveErrors int
	lastRecoveryTime  time.Time
	cooldown          time.Duration
}

// NewRecoveryCoordinator creates a recovery coordinator with default cooldown
func NewRecoveryCoordinator() *RecoveryCoordinator {
	return &RecoveryCoordinator{
		state:    StateNormal,
		cooldown: 30 * time.Second,
	}
}

// NewRecoveryCoordinatorWithCooldown creates a recovery coordinator with custom cooldown
func NewRecoveryCoordinatorWithCooldown(cooldown time.Duration) *RecoveryCoordinator {
	return &RecoveryCoordinator{
		state:    StateNormal,
		cooldown: cooldown,
	}
}

// BeforeRecovery checks whether a recovery strategy is allowed before executing it.
// The state machine only allows specific transitions:
//   - Normal → retry
//   - Retrying → degrade
//   - Degrading → compress
//   - Compressing → nothing (terminal before Failed)
//
// Cooldown period prevents rapid successive recovery attempts.
func (rc *RecoveryCoordinator) BeforeRecovery(strategy string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Check cooldown period
	if !rc.lastRecoveryTime.IsZero() && time.Since(rc.lastRecoveryTime) < rc.cooldown {
		return false
	}

	// State machine transitions
	switch rc.state {
	case StateNormal:
		return strategy == "retry"
	case StateRetrying:
		return strategy == "degrade"
	case StateDegrading:
		return strategy == "compress"
	case StateCompressing:
		return false // No more recovery allowed
	case StateFailed:
		return false
	}
	return false
}

// RecordRetryAttempt records a failed same-model retry attempt (StreamWithRetry
// Phase 1) WITHOUT advancing the recovery phase state machine and WITHOUT arming
// the escalation cooldown.
//
// Rationale (S17 G1 / HG1-A "解耦重试计数"):
//   - The same-model retry budget is owned by RetryConfig.MaxRetries in
//     StreamWithRetry Phase 1, not by this state machine.
//   - Feeding every attempt into RecordFailure used to burn the phase machine
//     Normal→Retrying→Degrading→Compressing within one Phase-1 burst, which
//     (a) made BeforeRecovery("retry") return false and aborted Phase 1 after a
//     SINGLE attempt, and (b) left the degrade/compress gates unreachable.
//     (业界对标同类 CLI: retry is a bounded counter — `if *retries >= max_retries`
//     in— decoupled from任何阶段状态机。)
//   - Retry attempts are accounting, not phase escalations.
//
// consecutiveErrors is still incremented for observability. lastRecoveryTime is
// intentionally NOT touched: same-model retries must not trip the escalation
// cooldown. The single phase advance after Phase-1 exhaustion is done by
// RecordFailure in StreamWithRetry.
func (rc *RecoveryCoordinator) RecordRetryAttempt() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.consecutiveErrors++
}

// RecordSuccess records a successful operation, resetting state to Normal
func (rc *RecoveryCoordinator) RecordSuccess() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.state = StateNormal
	rc.consecutiveErrors = 0
}

// AdvancePhase advances the one-way phase machine by exactly one step
// (Normal→Retrying→Degrading→Compressing→Failed) and counts one error,
// but does NOT arm the escalation cooldown.
//
// Rationale（S17 HG5=A+B「升级机可达性」）：
//   - A：阶段推进的语义是"该阶段**已被尝试**"，而不是"模型确实切换了"。
//     旧实现把 Phase2 的记账嵌在 fallbackModel != req.Model 的 if 内 ⇒ 非 529 错误
//     （FallbackChain 不降级）时永不推进 Degrading ⇒ Phase3 的 compress 门恒为 false，
//     degrade/compress 在生产退化为死代码。
//   - B：**请求内**的阶段步进不得武装冷却 —— 冷却只用于**跨请求**节流。
//     否则 Phase1 耗尽时的那一次步进会武装 30s 冷却，使同一请求内紧随其后的
//     degrade/compress 门全部返 false（G1b 的原因②）。
//
// 跨请求节流仍由 RecordFailure（Phase3 失败路径）武装 —— 那里是最后一级升级，
// 武装冷却不会阻断请求内的连续升级。
func (rc *RecoveryCoordinator) AdvancePhase() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.consecutiveErrors++

	// Advance state machine（与 RecordFailure 同一张迁移表）
	switch rc.state {
	case StateNormal:
		rc.state = StateRetrying
	case StateRetrying:
		rc.state = StateDegrading
	case StateDegrading:
		rc.state = StateCompressing
	case StateCompressing:
		rc.state = StateFailed
	}
}

// RecordFailure records a failure, advancing the state machine **and arming the
// escalation cooldown** (cross-request throttling).
//
// ⚠️ 请求内的阶段步进请用 AdvancePhase（不武装冷却，见其注释）。
// 本方法只在"最后一级升级失败"（Phase3 压缩尝试失败）时使用。
func (rc *RecoveryCoordinator) RecordFailure() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.consecutiveErrors++
	rc.lastRecoveryTime = time.Now()

	// Advance state machine
	switch rc.state {
	case StateNormal:
		rc.state = StateRetrying
	case StateRetrying:
		rc.state = StateDegrading
	case StateDegrading:
		rc.state = StateCompressing
	case StateCompressing:
		rc.state = StateFailed
	}
}

// State returns the current recovery state
func (rc *RecoveryCoordinator) State() RecoveryState {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.state
}

// ConsecutiveErrors returns the count of consecutive errors
func (rc *RecoveryCoordinator) ConsecutiveErrors() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.consecutiveErrors
}

// Reset resets the coordinator to Normal state
func (rc *RecoveryCoordinator) Reset() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.state = StateNormal
	rc.consecutiveErrors = 0
	rc.lastRecoveryTime = time.Time{}
}
