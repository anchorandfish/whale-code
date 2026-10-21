package ai

import "testing"

func TestRequestUsageAccumulatorMergesPartialCumulativeSnapshots(t *testing.T) {
	var acc RequestUsageAccumulator
	acc.Add(UsageEvent{InputTokens: 100, CacheCreation: 20, CacheRead: 30})
	acc.Add(UsageEvent{OutputTokens: 40})
	acc.Add(UsageEvent{InputTokens: 90, OutputTokens: 35}) // stale snapshot

	got, ok := acc.Finalize()
	if !ok {
		t.Fatal("Finalize reported no usage")
	}
	if got.InputTokens != 100 || got.OutputTokens != 40 || got.CacheCreation != 20 || got.CacheRead != 30 {
		t.Fatalf("usage = %+v, want in=100 out=40 create=20 read=30", got)
	}
	if _, ok := acc.Finalize(); ok {
		t.Fatal("second Finalize must be a no-op")
	}
}

func TestRequestUsageAccumulatorNoEvents(t *testing.T) {
	var acc RequestUsageAccumulator
	if _, ok := acc.Finalize(); ok {
		t.Fatal("empty accumulator must not create an API usage record")
	}
}
