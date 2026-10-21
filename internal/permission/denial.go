package permission

import (
	"fmt"
	"sync"
	"time"
)

// DenialState is the denial tracking state
type DenialState struct {
	ConsecutiveDenials int       // Consecutive denial count
	TotalDenials       int       // Total session denial count
	LastDenialTime     time.Time // Last denial time
}

// DenialLimits are the denial limits
const (
	MaxConsecutiveDenials = 5  // Max consecutive denials
	MaxTotalDenials       = 25 // Max total session denials
)

// DenialTracker tracks denial events and triggers fallback
type DenialTracker struct {
	mu    sync.RWMutex
	state DenialState
}

// NewDenialTracker creates a denial tracker
func NewDenialTracker() *DenialTracker {
	return &DenialTracker{
		state: DenialState{},
	}
}

// RecordDenial records a denial event
func (dt *DenialTracker) RecordDenial() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.state.ConsecutiveDenials++
	dt.state.TotalDenials++
	dt.state.LastDenialTime = time.Now()
}

// RecordSuccess records a success event, resetting consecutive denials
func (dt *DenialTracker) RecordSuccess() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.state.ConsecutiveDenials = 0
}

// ShouldFallback checks if auto mode should fall back to manual confirmation
func (dt *DenialTracker) ShouldFallback() bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.state.ConsecutiveDenials >= MaxConsecutiveDenials ||
		dt.state.TotalDenials >= MaxTotalDenials
}

// IsConsecutiveFallback checks if the consecutive denial threshold has been reached
func (dt *DenialTracker) IsConsecutiveFallback() bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.state.ConsecutiveDenials >= MaxConsecutiveDenials
}

// IsTotalFallback checks if the total denial threshold has been reached
func (dt *DenialTracker) IsTotalFallback() bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.state.TotalDenials >= MaxTotalDenials
}

// ConsecutiveDenials returns the current consecutive denial count
func (dt *DenialTracker) ConsecutiveDenials() int {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.state.ConsecutiveDenials
}

// TotalDenials returns the total denial count
func (dt *DenialTracker) TotalDenials() int {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.state.TotalDenials
}

// Clone returns a deep copy of the denial state
func (dt *DenialTracker) Clone() DenialState {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return DenialState{
		ConsecutiveDenials: dt.state.ConsecutiveDenials,
		TotalDenials:       dt.state.TotalDenials,
		LastDenialTime:     dt.state.LastDenialTime,
	}
}

// DenialReason represents the reason for a permission denial
type DenialReason string

const (
	DenialReasonRuleBlock      DenialReason = "rule_block"
	DenialReasonModeRestrict   DenialReason = "mode_restrict"
	DenialReasonDangerousCmd   DenialReason = "dangerous_command"
	DenialReasonConsecutiveMax DenialReason = "consecutive_max"
	DenialReasonTotalMax       DenialReason = "total_max"
)

// DenialSuggestion maps denial reasons to suggested actions
var denialSuggestions = map[DenialReason]struct {
	Action string
	DocURL string
}{
	DenialReasonRuleBlock: {
		Action: "Check your permission rules in .whale/settings.json and remove or modify the blocking rule.",
		DocURL: "https://github.com/anchorandfish/whale-code/blob/main/docs/permissions.md#rules",
	},
	DenialReasonModeRestrict: {
		Action: "Switch to a less restrictive mode using /permissions <mode> (e.g., accept-edits or workspace-write).",
		DocURL: "https://github.com/anchorandfish/whale-code/blob/main/docs/permissions.md#modes",
	},
	DenialReasonDangerousCmd: {
		Action: "Review the command carefully. If you trust it, you can approve it manually when prompted.",
		DocURL: "https://github.com/anchorandfish/whale-code/blob/main/docs/permissions.md#dangerous-commands",
	},
	DenialReasonConsecutiveMax: {
		Action: "Too many consecutive denials detected. Consider switching mode or reviewing the denied operations.",
		DocURL: "https://github.com/anchorandfish/whale-code/blob/main/docs/permissions.md#fallback",
	},
	DenialReasonTotalMax: {
		Action: "Total denial limit reached. The agent will stop to prevent unintended actions. Restart the session if needed.",
		DocURL: "https://github.com/anchorandfish/whale-code/blob/main/docs/permissions.md#fallback",
	},
}

// FormatDenialMessage generates a friendly denial message with reason, suggestion, and doc link
func FormatDenialMessage(reason DenialReason, toolName, detail string) string {
	suggestion, ok := denialSuggestions[reason]
	if !ok {
		return fmt.Sprintf("Permission denied for %s: %s", toolName, detail)
	}

	return fmt.Sprintf(
		"Permission denied for %s: %s\n\nSuggestion: %s\nDocs: %s",
		toolName,
		detail,
		suggestion.Action,
		suggestion.DocURL,
	)
}

// Reset clears all denial tracking data
func (dt *DenialTracker) Reset() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.state = DenialState{}
}
