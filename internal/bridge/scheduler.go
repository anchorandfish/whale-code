package bridge

import (
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// RefreshSchedulerConfig defines the behavior parameters of the Token refresh scheduler
type RefreshSchedulerConfig struct {
	RefreshBufferMs     int64 // Advance refresh buffer time (ms), default 300000 (5 minutes)
	MaxRefreshFailures  int   // Max consecutive failures, default 3
	RefreshRetryDelayMs int64 // Failure retry interval (ms), default 60000 (60 seconds)
	FallbackIntervalMs  int64 // Fallback refresh interval (ms), default 1800000 (30 minutes)
}

// DefaultRefreshSchedulerConfig returns default refresh scheduler config
func DefaultRefreshSchedulerConfig() RefreshSchedulerConfig {
	return RefreshSchedulerConfig{
		RefreshBufferMs:     300000, // 5 minutes
		MaxRefreshFailures:  3,
		RefreshRetryDelayMs: 60000,   // 60 seconds
		FallbackIntervalMs:  1800000, // 30 minutes
	}
}

// SchedulerState represents the current state of the scheduler
type SchedulerState struct {
	Generation     uint64 // Current generation counter
	ConsecFailures int    // Consecutive failure count
	IsScheduled    bool   // Whether there is an active schedule
	NextRefreshAt  string // Next refresh time (ISO format), empty string means no schedule
}

// RefreshScheduler manages automatic JWT token refresh scheduling
// Uses generation counter to prevent stale async refreshes from overwriting newer schedules
type RefreshScheduler struct {
	mu            sync.Mutex
	config        RefreshSchedulerConfig
	generation    uint64                 // Generation counter, incremented on each new schedule
	failCount     int                    // Consecutive failure count
	timer         *time.Timer            // Current timer
	refreshToken  func() (string, error) // Callback to refresh Token
	onTokenUpdate func(string)           // Token update success callback
	onStateChange func(SchedulerState)   // State change callback
}

// NewRefreshScheduler creates a new Token refresh scheduler
// config is the scheduler config, refreshToken is the callback to refresh Token
func NewRefreshScheduler(config RefreshSchedulerConfig, refreshToken func() (string, error)) *RefreshScheduler {
	return &RefreshScheduler{
		config:       config,
		refreshToken: refreshToken,
	}
}

// ScheduleFromExpiresIn schedules refresh based on TTL duration (milliseconds)
// ttlMs is milliseconds until expiration, scheduler will refresh RefreshBufferMs milliseconds early
func (s *RefreshScheduler) ScheduleFromExpiresIn(ttlMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.generation++
	currentGen := s.generation

	var delayMs int64
	if ttlMs <= s.config.RefreshBufferMs {
		delayMs = 0
	} else {
		delayMs = ttlMs - s.config.RefreshBufferMs
	}

	s.scheduleWithDelay(delayMs, currentGen)
}

// ScheduleFromExpiry schedules refresh based on absolute expiration timestamp (Unix seconds)
// expiryUnix is the Unix timestamp of JWT exp claim
func (s *RefreshScheduler) ScheduleFromExpiry(expiryUnix int64) {
	now := time.Now().Unix()
	ttlMs := (expiryUnix - now) * 1000
	s.ScheduleFromExpiresIn(ttlMs)
}

// ScheduleOrFallback tries to extract exp from token to schedule refresh, falls back to fallback interval on failure
// First tries to decode JWT to get exp claim, schedules by exp on success; schedules by FallbackIntervalMs on failure
func (s *RefreshScheduler) ScheduleOrFallback(token string) {
	exp, err := DecodeJWTExpiry(token)
	if err == nil && exp > 0 {
		s.ScheduleFromExpiry(exp)
		return
	}
	// Cannot get exp, use fallback interval
	s.ScheduleFromExpiresIn(s.config.FallbackIntervalMs)
}

// Cancel stops the current schedule timer
func (s *RefreshScheduler) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.notifyStateChange()
}

// CancelAll stops all scheduling and resets state
// Increments generation counter to invalidate in-flight async refreshes, and resets failure count
func (s *RefreshScheduler) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.generation++
	s.failCount = 0
	s.notifyStateChange()
}

// GetState gets the current scheduler state
func (s *RefreshScheduler) GetState() SchedulerState {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := SchedulerState{
		Generation:     s.generation,
		ConsecFailures: s.failCount,
		IsScheduled:    s.timer != nil,
	}
	return state
}

// SetOnTokenUpdate sets the Token update success callback
func (s *RefreshScheduler) SetOnTokenUpdate(fn func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTokenUpdate = fn
}

// SetOnStateChange sets the state change callback
func (s *RefreshScheduler) SetOnStateChange(fn func(SchedulerState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onStateChange = fn
}

// scheduleWithDelay executes refresh after specified delay (milliseconds)
// Generation counter ensures only the latest schedule actually executes
func (s *RefreshScheduler) scheduleWithDelay(delayMs int64, gen uint64) {
	if s.timer != nil {
		s.timer.Stop()
	}

	delay := time.Duration(delayMs) * time.Millisecond
	s.timer = time.AfterFunc(delay, func() {
		s.doRefresh(gen)
	})
	s.notifyStateChange()
}

// doRefresh performs the actual Token refresh
// Generation check ensures stale schedules do not execute; stops retry when consecutive failures reach limit
func (s *RefreshScheduler) doRefresh(gen uint64) {
	s.mu.Lock()
	// Generation check: if this schedule has been superseded by a newer one, skip
	if gen != s.generation {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Execute refresh callback
	newToken, err := s.refreshToken()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generation check: new schedule may have been created during refresh
	if gen != s.generation {
		return
	}

	if err != nil {
		s.failCount++

		if s.failCount >= s.config.MaxRefreshFailures {
			s.notifyStateChange()
			return
		}

		s.generation++
		newGen := s.generation
		s.timer = time.AfterFunc(
			time.Duration(s.config.RefreshRetryDelayMs)*time.Millisecond,
			func() { s.doRefresh(newGen) },
		)
		s.notifyStateChange()
		return
	}

	s.failCount = 0

	if s.onTokenUpdate != nil {
		s.onTokenUpdate(newToken)
	}

	exp, expErr := DecodeJWTExpiry(newToken)
	if expErr == nil && exp > 0 {
		currentGen := s.generation
		s.mu.Unlock()
		s.ScheduleFromExpiry(exp)
		s.mu.Lock()
		if s.generation != currentGen {
			return
		}
	} else {
		s.generation++
		newGen := s.generation
		s.scheduleWithDelay(s.config.FallbackIntervalMs, newGen)
	}

	s.notifyStateChange()
}

// notifyStateChange notifies state change (must be called while holding lock)
func (s *RefreshScheduler) notifyStateChange() {
	if s.onStateChange != nil {
		state := SchedulerState{
			Generation:     s.generation,
			ConsecFailures: s.failCount,
			IsScheduled:    s.timer != nil,
		}
		// Call callback in new goroutine to avoid deadlock
		go s.onStateChange(state)
	}
}

// formatError 将 bridge 错误码包装为 BridgeError 并附加 N6 ErrorID（P0-5，v2 R2）。
// 返回 ai.WithID 双层包装：外层 *ai.Error 供 ai.GetErrorID 提取，
// 内层 *BridgeError 供 errors.As 提取与 ErrorID() 方法直接读取。
func formatError(code int, message string) error {
	return ai.WithID(NewBridgeError(code, message), MapErrorCodeToErrorID(code))
}

// TaskScheduler manages task scheduling with deduplication based on task type and parameters
type TaskScheduler struct {
	mu      sync.Mutex
	running map[string]bool
}

// NewTaskScheduler creates a new task scheduler with deduplication
func NewTaskScheduler() *TaskScheduler {
	return &TaskScheduler{
		running: make(map[string]bool),
	}
}

// taskDedupKey generates a deduplication key from task type and parameters
func taskDedupKey(taskType string, taskParams interface{}) string {
	return fmt.Sprintf("%s:%v", taskType, taskParams)
}

// Submit submits a task for execution, returns false if a duplicate task is already running
func (s *TaskScheduler) Submit(taskType string, taskParams interface{}, fn func()) bool {
	s.mu.Lock()
	key := taskDedupKey(taskType, taskParams)
	if s.running[key] {
		s.mu.Unlock()
		return false
	}
	s.running[key] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[scheduler] task %q panicked: %v\nstack:\n%s", key, r, debug.Stack())
			}
			s.mu.Lock()
			delete(s.running, key)
			s.mu.Unlock()
		}()
		fn()
	}()
	return true
}

// IsRunning checks if a task with the given type and parameters is currently running
func (s *TaskScheduler) IsRunning(taskType string, taskParams interface{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := taskDedupKey(taskType, taskParams)
	return s.running[key]
}
