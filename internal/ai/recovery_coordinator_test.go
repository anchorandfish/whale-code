package ai

import (
	"sync"
	"testing"
	"time"
)

// TestRecoveryCoordinator_NormalSuccess tests that a successful request keeps state Normal
func TestRecoveryCoordinator_NormalSuccess(t *testing.T) {
	rc := NewRecoveryCoordinator()

	if rc.State() != StateNormal {
		t.Errorf("initial state = %v, want Normal", rc.State())
	}

	rc.RecordSuccess()

	if rc.State() != StateNormal {
		t.Errorf("after success state = %v, want Normal", rc.State())
	}
	if rc.ConsecutiveErrors() != 0 {
		t.Errorf("consecutive errors = %d, want 0", rc.ConsecutiveErrors())
	}
}

// TestRecoveryCoordinator_RetrySuccess tests that retry succeeds and state returns to Normal
func TestRecoveryCoordinator_RetrySuccess(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	// Normal state allows "retry" strategy
	if !rc.BeforeRecovery("retry") {
		t.Error("BeforeRecovery(retry) in Normal state should return true")
	}

	// Record failure → state advances to Retrying
	rc.RecordFailure()
	if rc.State() != StateRetrying {
		t.Errorf("state after first failure = %v, want Retrying", rc.State())
	}

	// Success resets to Normal
	rc.RecordSuccess()
	if rc.State() != StateNormal {
		t.Errorf("state after success = %v, want Normal", rc.State())
	}
	if rc.ConsecutiveErrors() != 0 {
		t.Errorf("consecutive errors after success = %d, want 0", rc.ConsecutiveErrors())
	}
}

// TestRecoveryCoordinator_FullCascade tests the full cascade: retry → degrade → compress → failed
func TestRecoveryCoordinator_FullCascade(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	// Normal → allow retry
	if !rc.BeforeRecovery("retry") {
		t.Error("Normal state should allow retry")
	}
	rc.RecordFailure()
	if rc.State() != StateRetrying {
		t.Errorf("state = %v, want Retrying", rc.State())
	}

	// Retrying → allow degrade
	if !rc.BeforeRecovery("degrade") {
		t.Error("Retrying state should allow degrade")
	}
	rc.RecordFailure()
	if rc.State() != StateDegrading {
		t.Errorf("state = %v, want Degrading", rc.State())
	}

	// Degrading → allow compress
	if !rc.BeforeRecovery("compress") {
		t.Error("Degrading state should allow compress")
	}
	rc.RecordFailure()
	if rc.State() != StateCompressing {
		t.Errorf("state = %v, want Compressing", rc.State())
	}

	// Compressing → no more recovery allowed
	if rc.BeforeRecovery("compress") {
		t.Error("Compressing state should not allow any recovery")
	}
	rc.RecordFailure()
	if rc.State() != StateFailed {
		t.Errorf("state = %v, want Failed", rc.State())
	}

	// Failed → nothing allowed
	if rc.BeforeRecovery("retry") {
		t.Error("Failed state should not allow retry")
	}
	if rc.BeforeRecovery("degrade") {
		t.Error("Failed state should not allow degrade")
	}
	if rc.BeforeRecovery("compress") {
		t.Error("Failed state should not allow compress")
	}

	// Consecutive errors should be 4
	if rc.ConsecutiveErrors() != 4 {
		t.Errorf("consecutive errors = %d, want 4", rc.ConsecutiveErrors())
	}
}

// TestRecoveryCoordinator_InvalidTransitions tests that invalid state transitions are rejected
func TestRecoveryCoordinator_InvalidTransitions(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	// Normal state should NOT allow degrade or compress
	if rc.BeforeRecovery("degrade") {
		t.Error("Normal state should not allow degrade")
	}
	if rc.BeforeRecovery("compress") {
		t.Error("Normal state should not allow compress")
	}

	// Advance to Retrying
	rc.RecordFailure()

	// Retrying should NOT allow retry or compress
	if rc.BeforeRecovery("retry") {
		t.Error("Retrying state should not allow retry")
	}
	if rc.BeforeRecovery("compress") {
		t.Error("Retrying state should not allow compress")
	}

	// Advance to Degrading
	rc.RecordFailure()

	// Degrading should NOT allow retry or degrade
	if rc.BeforeRecovery("retry") {
		t.Error("Degrading state should not allow retry")
	}
	if rc.BeforeRecovery("degrade") {
		t.Error("Degrading state should not allow degrade")
	}
}

// TestRecoveryCoordinator_CooldownBlocksRecovery tests that cooldown period blocks new recovery attempts
func TestRecoveryCoordinator_CooldownBlocksRecovery(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(5 * time.Second)

	// First attempt should succeed
	if !rc.BeforeRecovery("retry") {
		t.Error("first BeforeRecovery should succeed")
	}

	// Record failure to set lastRecoveryTime
	rc.RecordFailure()

	// Second attempt within cooldown should be blocked
	if rc.BeforeRecovery("degrade") {
		t.Error("BeforeRecovery within cooldown should be blocked")
	}
}

// TestRecoveryCoordinator_CooldownExpires tests that recovery is allowed after cooldown expires
func TestRecoveryCoordinator_CooldownExpires(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(10 * time.Millisecond)

	// First attempt succeeds
	if !rc.BeforeRecovery("retry") {
		t.Error("first BeforeRecovery should succeed")
	}
	rc.RecordFailure()

	// Wait for cooldown to expire
	time.Sleep(20 * time.Millisecond)

	// Now degrade should be allowed
	if !rc.BeforeRecovery("degrade") {
		t.Error("BeforeRecovery after cooldown should succeed")
	}
}

// TestRecoveryCoordinator_ConcurrentSafety tests concurrent access with -race flag
func TestRecoveryCoordinator_ConcurrentSafety(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	var wg sync.WaitGroup
	numGoroutines := 50

	// Concurrent RecordFailure
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc.RecordFailure()
		}()
	}
	wg.Wait()

	if rc.ConsecutiveErrors() != numGoroutines {
		t.Errorf("consecutive errors = %d, want %d", rc.ConsecutiveErrors(), numGoroutines)
	}

	// Concurrent RecordSuccess
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc.RecordSuccess()
		}()
	}
	wg.Wait()

	if rc.State() != StateNormal {
		t.Errorf("state after concurrent success = %v, want Normal", rc.State())
	}

	// Concurrent BeforeRecovery — all should return true since state stays Normal
	// (BeforeRecovery does not change state, only checks it)
	rc.Reset()
	trueCount := 0
	var mu sync.Mutex
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rc.BeforeRecovery("retry") {
				mu.Lock()
				trueCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if trueCount != numGoroutines {
		t.Errorf("all BeforeRecovery calls should return true in Normal state, got %d/%d", trueCount, numGoroutines)
	}

	// Concurrent State() reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rc.State()
		}()
	}
	wg.Wait()
}

// TestRecoveryCoordinator_Reset tests that Reset returns to Normal state
func TestRecoveryCoordinator_Reset(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	// Advance to Failed state
	rc.RecordFailure() // Normal → Retrying
	rc.RecordFailure() // Retrying → Degrading
	rc.RecordFailure() // Degrading → Compressing
	rc.RecordFailure() // Compressing → Failed

	if rc.State() != StateFailed {
		t.Errorf("state before reset = %v, want Failed", rc.State())
	}
	if rc.ConsecutiveErrors() != 4 {
		t.Errorf("consecutive errors before reset = %d, want 4", rc.ConsecutiveErrors())
	}

	// Reset
	rc.Reset()

	if rc.State() != StateNormal {
		t.Errorf("state after reset = %v, want Normal", rc.State())
	}
	if rc.ConsecutiveErrors() != 0 {
		t.Errorf("consecutive errors after reset = %d, want 0", rc.ConsecutiveErrors())
	}

	// Should be able to retry again after reset
	if !rc.BeforeRecovery("retry") {
		t.Error("after reset, BeforeRecovery(retry) should return true")
	}
}

// TestRecoveryCoordinator_StateString tests the String method of RecoveryState
func TestRecoveryCoordinator_StateString(t *testing.T) {
	tests := []struct {
		state RecoveryState
		want  string
	}{
		{StateNormal, "normal"},
		{StateRetrying, "retrying"},
		{StateDegrading, "degrading"},
		{StateCompressing, "compressing"},
		{StateFailed, "failed"},
		{RecoveryState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("RecoveryState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// TestRecoveryCoordinator_RetryDegradeSuccess tests retry fails, degrade succeeds → state back to Normal
func TestRecoveryCoordinator_RetryDegradeSuccess(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	// Normal → retry allowed → fail → Retrying
	if !rc.BeforeRecovery("retry") {
		t.Fatal("Normal should allow retry")
	}
	rc.RecordFailure()
	if rc.State() != StateRetrying {
		t.Fatalf("state = %v, want Retrying", rc.State())
	}

	// Retrying → degrade allowed → succeed → Normal
	if !rc.BeforeRecovery("degrade") {
		t.Fatal("Retrying should allow degrade")
	}
	rc.RecordSuccess()
	if rc.State() != StateNormal {
		t.Fatalf("state after degrade success = %v, want Normal", rc.State())
	}
	if rc.ConsecutiveErrors() != 0 {
		t.Errorf("consecutive errors = %d, want 0", rc.ConsecutiveErrors())
	}
}

// TestRecoveryCoordinator_AllFail tests retry+degrade+compress all fail → state is Failed
func TestRecoveryCoordinator_AllFail(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	// Phase 1: retry fails
	if !rc.BeforeRecovery("retry") {
		t.Fatal("Normal should allow retry")
	}
	rc.RecordFailure()
	if rc.State() != StateRetrying {
		t.Fatalf("state = %v, want Retrying", rc.State())
	}

	// Phase 2: degrade fails
	if !rc.BeforeRecovery("degrade") {
		t.Fatal("Retrying should allow degrade")
	}
	rc.RecordFailure()
	if rc.State() != StateDegrading {
		t.Fatalf("state = %v, want Degrading", rc.State())
	}

	// Phase 3: compress fails
	if !rc.BeforeRecovery("compress") {
		t.Fatal("Degrading should allow compress")
	}
	rc.RecordFailure()
	if rc.State() != StateCompressing {
		t.Fatalf("state = %v, want Compressing", rc.State())
	}

	// Compressing → one more failure → Failed
	rc.RecordFailure()
	if rc.State() != StateFailed {
		t.Fatalf("state = %v, want Failed", rc.State())
	}

	// No more recovery allowed from Failed
	if rc.BeforeRecovery("retry") {
		t.Error("Failed should not allow retry")
	}
	if rc.BeforeRecovery("degrade") {
		t.Error("Failed should not allow degrade")
	}
	if rc.BeforeRecovery("compress") {
		t.Error("Failed should not allow compress")
	}
}

// TestRecoveryCoordinator_CooldownBlocksWithin30s tests that 30s cooldown blocks new recovery
func TestRecoveryCoordinator_CooldownBlocksWithin30s(t *testing.T) {
	rc := NewRecoveryCoordinator() // default 30s cooldown

	// First retry is allowed
	if !rc.BeforeRecovery("retry") {
		t.Fatal("first BeforeRecovery(retry) should succeed")
	}

	// Record failure to set lastRecoveryTime
	rc.RecordFailure()

	// Within cooldown, degrade should be blocked
	if rc.BeforeRecovery("degrade") {
		t.Error("BeforeRecovery within 30s cooldown should be blocked")
	}

	// Retry should also be blocked (state is Retrying, but even if state allowed it, cooldown blocks it)
	if rc.BeforeRecovery("retry") {
		t.Error("BeforeRecovery within 30s cooldown should be blocked")
	}
}

// TestRecoveryCoordinator_ConcurrentRecoveryRequests tests concurrent goroutine safety with -race
func TestRecoveryCoordinator_ConcurrentRecoveryRequests(t *testing.T) {
	rc := NewRecoveryCoordinatorWithCooldown(0)

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent BeforeRecovery + RecordFailure + RecordSuccess
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Mix of operations
			if idx%3 == 0 {
				rc.RecordFailure()
			} else if idx%3 == 1 {
				rc.RecordSuccess()
			} else {
				rc.BeforeRecovery("retry")
			}
			_ = rc.State()
			_ = rc.ConsecutiveErrors()
		}(i)
	}
	wg.Wait()

	// Final state should be valid (any of the defined states)
	state := rc.State()
	if state < StateNormal || state > StateFailed {
		t.Errorf("invalid final state = %v", state)
	}
}
