package ai

// RequestUsageAccumulator merges partial cumulative usage snapshots emitted
// for one provider request. A zero field means "not present in this event";
// it must not erase a value received earlier in the stream.
type RequestUsageAccumulator struct {
	usage     UsageEvent
	seen      bool
	finalized bool
}

// Add merges one provider snapshot. max makes repeated cumulative snapshots
// idempotent and protects against stale/out-of-order delivery.
func (a *RequestUsageAccumulator) Add(event UsageEvent) {
	a.seen = true
	a.usage.InputTokens = maxUsageValue(a.usage.InputTokens, event.InputTokens)
	a.usage.OutputTokens = maxUsageValue(a.usage.OutputTokens, event.OutputTokens)
	a.usage.CacheCreation = maxUsageValue(a.usage.CacheCreation, event.CacheCreation)
	a.usage.CacheRead = maxUsageValue(a.usage.CacheRead, event.CacheRead)
}

// Finalize returns the request totals exactly once. Streams with no provider
// usage do not manufacture a zero-token API call.
func (a *RequestUsageAccumulator) Finalize() (UsageEvent, bool) {
	if a.finalized || !a.seen {
		return UsageEvent{}, false
	}
	a.finalized = true
	return a.usage, true
}

// Snapshot returns the current totals without finalizing the accumulator.
func (a *RequestUsageAccumulator) Snapshot() UsageEvent {
	return a.usage
}

func maxUsageValue(current, next int) int {
	if next > current {
		return next
	}
	return current
}
