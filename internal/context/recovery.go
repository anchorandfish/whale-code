package context

import (
	"fmt"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// RecoveryType recovery strategytype
type RecoveryType string

const (
	RecoveryTypeNone             RecoveryType = "none"
	RecoveryTypeCompact          RecoveryType = "compact"
	RecoveryTypeEscalateTokens   RecoveryType = "escalate_tokens"
	RecoveryTypeContinueOutput   RecoveryType = "continue_output"
	RecoveryTypeRetryWithBackoff RecoveryType = "retry_with_backoff"
	RecoveryTypeNetworkError     RecoveryType = "network_error"
	RecoveryTypeRateLimit        RecoveryType = "rate_limit"
)

// RecoveryStrategy recovery strategy
type RecoveryStrategy struct {
	Type        RecoveryType
	CanRecover  bool
	Description string
}

// FormatAction formats recovery action description
func (s RecoveryStrategy) FormatAction() string {
	switch s.Type {
	case RecoveryTypeCompact:
		return "🔄 Auto-compacting context to free space..."
	case RecoveryTypeEscalateTokens:
		return "⬆️ Escalating max output tokens..."
	case RecoveryTypeContinueOutput:
		return "✂️ Continuing truncated output..."
	case RecoveryTypeRetryWithBackoff:
		return "🔄 Retrying with backoff..."
	case RecoveryTypeNetworkError:
		return "🌐 Network error detected, retrying with backoff..."
	case RecoveryTypeRateLimit:
		return "⏳ Rate limited, backing off and retrying..."
	default:
		return ""
	}
}

// CompactStrategy compaction strategy
type CompactStrategy struct {
	PreserveRecentMessages int
	UseAISummary           bool
}

// MoreAggressive returns a more aggressive compaction strategy
func (s CompactStrategy) MoreAggressive() CompactStrategy {
	newPreserve := s.PreserveRecentMessages
	if newPreserve > 3 {
		newPreserve = newPreserve / 2
		if newPreserve < 3 {
			newPreserve = 3
		}
	}
	return CompactStrategy{
		PreserveRecentMessages: newPreserve,
		UseAISummary:           s.UseAISummary,
	}
}

// ContextOverflowRecovery is the context overflow recovery handler
type ContextOverflowRecovery struct {
	mu                sync.Mutex
	MaxAttempts       int
	RemainingAttempts int
	CompactStrategy   CompactStrategy
}

// NewContextOverflowRecovery creates a context overflow recovery handler
func NewContextOverflowRecovery(maxAttempts int) *ContextOverflowRecovery {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	return &ContextOverflowRecovery{
		MaxAttempts:       maxAttempts,
		RemainingAttempts: maxAttempts,
		CompactStrategy: CompactStrategy{
			PreserveRecentMessages: 10,
			UseAISummary:           true,
		},
	}
}

// ShouldRetry determines whether to continue retrying
func (r *ContextOverflowRecovery) ShouldRetry() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.RemainingAttempts > 0
}

// Consume consumes one recovery opportunity
func (r *ContextOverflowRecovery) Consume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RemainingAttempts--
	r.CompactStrategy = r.CompactStrategy.MoreAggressive()
}

// Reset resets the recovery handler
func (r *ContextOverflowRecovery) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RemainingAttempts = r.MaxAttempts
	r.CompactStrategy = CompactStrategy{
		PreserveRecentMessages: 10,
		UseAISummary:           true,
	}
}

// MaxOutputTokensRecovery is the output truncation recovery handler
type MaxOutputTokensRecovery struct {
	mu               sync.Mutex
	CurrentMaxTokens int
	AbsoluteMax      int
	HasEscalated     bool
	escalationCount  int
}

// NewMaxOutputTokensRecovery creates an output truncation recovery handler
func NewMaxOutputTokensRecovery(absoluteMax int) *MaxOutputTokensRecovery {
	return &MaxOutputTokensRecovery{
		CurrentMaxTokens: 16384,
		AbsoluteMax:      absoluteMax,
	}
}

// Escalate progressively upgrades max_tokens: 16384 -> 32768 -> 65536 (or AbsoluteMax)
func (r *MaxOutputTokensRecovery) Escalate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.escalationCount++
	switch r.escalationCount {
	case 1:
		r.CurrentMaxTokens = 32768
		r.HasEscalated = true
	case 2:
		target := 65536
		if r.AbsoluteMax < target {
			target = r.AbsoluteMax
		}
		r.CurrentMaxTokens = target
	default:
		// reached maximum, no further upgrade
	}
	return r.CurrentMaxTokens
}

// ErrorRecoveryOrchestrator is the error recovery orchestrator
type ErrorRecoveryOrchestrator struct {
	mu               sync.Mutex
	overflowRecovery *ContextOverflowRecovery
	totalRecoveries  int
	maxRecoveries    int
}

// NewErrorRecoveryOrchestrator creates an error recovery orchestrator
// maxRecoveries defaults to 3 if 0 or negative
func NewErrorRecoveryOrchestrator() *ErrorRecoveryOrchestrator {
	return NewErrorRecoveryOrchestratorWithMax(3)
}

// NewErrorRecoveryOrchestratorWithMax creates an error recovery orchestrator with a custom max recovery count
func NewErrorRecoveryOrchestratorWithMax(maxRecoveries int) *ErrorRecoveryOrchestrator {
	if maxRecoveries <= 0 {
		maxRecoveries = 3
	}
	return &ErrorRecoveryOrchestrator{
		overflowRecovery: NewContextOverflowRecovery(2),
		maxRecoveries:    maxRecoveries,
	}
}

// GetRecoveryStrategy fetches recovery strategy based on error type
func (o *ErrorRecoveryOrchestrator) GetRecoveryStrategy(errMsg string) RecoveryStrategy {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.totalRecoveries >= o.maxRecoveries {
		return RecoveryStrategy{
			Type:        RecoveryTypeNone,
			CanRecover:  false,
			Description: fmt.Sprintf("Max recovery attempts (%d) reached", o.maxRecoveries),
		}
	}

	lower := strings.ToLower(errMsg)

	if ai.IsContextOverflowError(lower) {
		if o.overflowRecovery.ShouldRetry() {
			return RecoveryStrategy{
				Type:        RecoveryTypeCompact,
				CanRecover:  true,
				Description: "Auto-compacting context to free space",
			}
		}
		return RecoveryStrategy{
			Type:        RecoveryTypeNone,
			CanRecover:  false,
			Description: "Context overflow recovery exhausted",
		}
	}

	if strings.Contains(lower, "max_tokens") ||
		strings.Contains(lower, "output length") ||
		strings.Contains(lower, "output_tokens") {
		return RecoveryStrategy{
			Type:        RecoveryTypeEscalateTokens,
			CanRecover:  true,
			Description: "Escalating max output tokens and continuing",
		}
	}

	if ai.IsOverloadedError(lower) || ai.IsRetryableErrorFromMsg(lower) {
		return RecoveryStrategy{
			Type:        RecoveryTypeRetryWithBackoff,
			CanRecover:  true,
			Description: "Retrying with exponential backoff",
		}
	}

	if ai.IsNetworkError(lower) {
		return RecoveryStrategy{
			Type:        RecoveryTypeNetworkError,
			CanRecover:  true,
			Description: "Network error detected, retrying with exponential backoff",
		}
	}

	if ai.IsRateLimitErrorFromMsg(lower) {
		return RecoveryStrategy{
			Type:        RecoveryTypeRateLimit,
			CanRecover:  true,
			Description: "API rate limit hit, backing off and retrying",
		}
	}

	return RecoveryStrategy{
		Type:        RecoveryTypeNone,
		CanRecover:  false,
		Description: "Unrecoverable error",
	}
}

// RecordRecovery records a recovery
func (o *ErrorRecoveryOrchestrator) RecordRecovery(recoveryType RecoveryType) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.totalRecoveries++
	if recoveryType == RecoveryTypeCompact {
		o.overflowRecovery.Consume()
	}
}

// Reset resets the orchestrator
func (o *ErrorRecoveryOrchestrator) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.totalRecoveries = 0
	o.overflowRecovery.Reset()
}

// BuildOverflowErrorMessage builds an overflow error message
func BuildOverflowErrorMessage(totalAttempts, remainingAttempts int) string {
	return fmt.Sprintf("context overflow: attempted %d recoveries, %d remaining. Use /compact for manual compaction, or /clear to clear session。",
		totalAttempts, remainingAttempts)
}
