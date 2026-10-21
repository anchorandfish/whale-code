package permission

import (
	"testing"
)

func TestDenialTracker_RecordDenial(t *testing.T) {
	dt := NewDenialTracker()

	if dt.ConsecutiveDenials() != 0 || dt.TotalDenials() != 0 {
		t.Fatal("initial state should be zero")
	}

	dt.RecordDenial()
	if dt.ConsecutiveDenials() != 1 {
		t.Errorf("ConsecutiveDenials = %d, want 1", dt.ConsecutiveDenials())
	}
	if dt.TotalDenials() != 1 {
		t.Errorf("TotalDenials = %d, want 1", dt.TotalDenials())
	}
}

func TestDenialTracker_RecordSuccess(t *testing.T) {
	dt := NewDenialTracker()

	dt.RecordDenial()
	dt.RecordDenial()
	dt.RecordDenial()

	if dt.ConsecutiveDenials() != 3 {
		t.Errorf("ConsecutiveDenials = %d, want 3", dt.ConsecutiveDenials())
	}

	dt.RecordSuccess()
	if dt.ConsecutiveDenials() != 0 {
		t.Errorf("ConsecutiveDenials after success = %d, want 0", dt.ConsecutiveDenials())
	}
	if dt.TotalDenials() != 3 {
		t.Errorf("TotalDenials = %d, want 3 (should not reset)", dt.TotalDenials())
	}
}

func TestDenialTracker_ShouldFallback_Consecutive(t *testing.T) {
	dt := NewDenialTracker()

	for i := 0; i < MaxConsecutiveDenials-1; i++ {
		dt.RecordDenial()
	}

	if dt.IsConsecutiveFallback() {
		t.Error("should not fallback before threshold")
	}

	dt.RecordDenial() // 达到阈值
	if !dt.IsConsecutiveFallback() {
		t.Error("should fallback at threshold")
	}
}

func TestDenialTracker_ShouldFallback_Total(t *testing.T) {
	dt := NewDenialTracker()

	for i := 0; i < MaxTotalDenials-1; i++ {
		dt.RecordDenial()
		dt.RecordSuccess() // 重置连续计数
	}

	if dt.IsTotalFallback() {
		t.Error("should not fallback before total threshold")
	}

	dt.RecordDenial() // 达到总拒绝阈值
	if !dt.IsTotalFallback() {
		t.Error("should fallback at total threshold")
	}
}

func TestDenialTracker_ShouldFallback_Both(t *testing.T) {
	dt := NewDenialTracker()

	for i := 0; i < MaxConsecutiveDenials; i++ {
		dt.RecordDenial()
	}

	if !dt.ShouldFallback() {
		t.Error("should fallback when consecutive denials >= threshold")
	}
}

func TestDenialTracker_Clone(t *testing.T) {
	dt := NewDenialTracker()
	dt.RecordDenial()
	dt.RecordDenial()

	clone := dt.Clone()
	if clone.ConsecutiveDenials != 2 {
		t.Errorf("clone.ConsecutiveDenials = %d, want 2", clone.ConsecutiveDenials)
	}

	// 修改 clone 不影响原对象
	clone.ConsecutiveDenials = 0
	if dt.ConsecutiveDenials() != 2 {
		t.Error("modifying clone should not affect original")
	}
}

func TestDenialTracker_Reset(t *testing.T) {
	dt := NewDenialTracker()
	dt.RecordDenial()
	dt.RecordDenial()

	dt.Reset()
	if dt.ConsecutiveDenials() != 0 || dt.TotalDenials() != 0 {
		t.Error("reset should clear all counts")
	}
}

// TestDenialTracker_Concurrent tests thread safety
func TestDenialTracker_Concurrent(t *testing.T) {
	dt := NewDenialTracker()
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				dt.RecordDenial()
				dt.RecordSuccess()
				dt.ShouldFallback()
				_ = dt.Clone()
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	_ = dt.ConsecutiveDenials()
	_ = dt.TotalDenials()
}
