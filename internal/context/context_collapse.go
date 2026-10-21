// Package context — context collapse manager
// aligns with a comparable CLI's Context Collapse mechanism:
// - does not modify REPL message array, generates projection view via commit log
// - 90% starts async staging collapse, 95% blocking generation
// - suppresses Autocompact when active
// - drains staged collapses on 413 recovery
package context

import (
	"fmt"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// CollapsePhase represents collapse phase
type CollapsePhase int

const (
	// CollapsePhaseIdle idle, not activated
	CollapsePhaseIdle CollapsePhase = iota
	// CollapsePhaseStaging async staging (90% trigger)
	CollapsePhaseStaging
	// CollapsePhaseBlocking blocking generation (95% trigger)
	CollapsePhaseBlocking
	// CollapsePhaseDraining draining (413 recovery)
	CollapsePhaseDraining
)

// CollapseAction represents collapse action type
type CollapseAction int

const (
	// CollapseActionNone no collapse needed
	CollapseActionNone CollapseAction = iota
	// CollapseActionStage async staging collapse (90%)
	CollapseActionStage
	// CollapseActionBlocking blocking generation collapse (95%)
	CollapseActionBlocking
)

// CollapseConfig is the collapse configuration
type CollapseConfig struct {
	// Enabled whether to enable read-time projection collapse (default true, can be set to false for backward compatibility)
	Enabled bool
	// StageThreshold context usage threshold to start async staging collapse (default 0.90)
	StageThreshold float64
	// BlockThreshold context usage threshold for blocking generation collapse (default 0.95)
	BlockThreshold float64
	// PreserveCount number of recent messages to preserve during collapse (default 10)
	PreserveCount int
	// MaxStagedCommits max staged commits (default 5)
	MaxStagedCommits int
	// DrainRetryMax max drain retries for 413 recovery (default 2)
	DrainRetryMax int
}

// DefaultCollapseConfig returns default collapse config
func DefaultCollapseConfig() CollapseConfig {
	return CollapseConfig{
		Enabled:          true,
		StageThreshold:   0.90,
		BlockThreshold:   0.95,
		PreserveCount:    10,
		MaxStagedCommits: 5,
		DrainRetryMax:    2,
	}
}

// ContextCollapseManager is the context collapse manager
// Aligned with a comparable CLI Context Collapse mechanism
type ContextCollapseManager struct {
	// estimator token estimator
	estimator *ai.TokenEstimator
	// collapser collapse compressor
	collapser *ContextCollapser
	// budget context budget
	budget Budget
	// commitLog collapse commit log
	commitLog *CollapseCommitLog
	// phase current collapse phase
	phase CollapsePhase
	// config collapse config
	config CollapseConfig
	// mu mutex (protects commitLog and phase)
	mu sync.Mutex
}

// NewContextCollapseManager creates a new collapse manager
func NewContextCollapseManager(config CollapseConfig, estimator *ai.TokenEstimator, budget Budget) *ContextCollapseManager {
	return &ContextCollapseManager{
		estimator: estimator,
		collapser: NewContextCollapser(),
		budget:    budget,
		commitLog: NewCollapseCommitLog(),
		phase:     CollapsePhaseIdle,
		config:    config,
	}
}

// CheckCollapseState checks collapse state, returns the collapse action to execute
// estimatedTokens: current estimated token count
func (m *ContextCollapseManager) CheckCollapseState(estimatedTokens int) CollapseAction {
	if !m.config.Enabled {
		return CollapseActionNone
	}

	if m.budget.ContextWindow <= 0 {
		return CollapseActionNone
	}

	usage := float64(estimatedTokens) / float64(m.budget.ContextWindow)

	switch {
	case usage >= m.config.BlockThreshold: // 95%
		return CollapseActionBlocking
	case usage >= m.config.StageThreshold: // 90%
		return CollapseActionStage
	default:
		return CollapseActionNone
	}
}

// StageCollapse async staging collapse
// When 90% threshold is triggered, record collapse operation to commit log staging area
func (m *ContextCollapseManager) StageCollapse(original []session.Message) error {
	if !m.config.Enabled {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if staging area is full
	if m.commitLog.StagedLen() >= m.config.MaxStagedCommits {
		return fmt.Errorf("staging area full (%d/%d)", m.commitLog.StagedLen(), m.config.MaxStagedCommits)
	}

	// Calculate collapsible message range
	startIdx := m.commitLog.LastCommitEndIndex()
	endIdx := len(original) - m.config.PreserveCount

	if endIdx <= startIdx {
		return nil // No collapsible messages
	}

	// Execute collapse extraction
	foldable := original[startIdx:endIdx]
	result := m.collapser.Collapse(foldable, m.config.PreserveCount)

	// Create commit
	commit := CollapseCommit{
		StartIndex:         startIdx,
		EndIndex:           endIdx,
		Summary:            result.CollapsedSummary,
		PreservedFiles:     result.PreservedFiles,
		PreservedDecisions: result.PreservedDecisions,
		PreservedErrors:    result.PreservedErrors,
		PreservedPatterns:  result.PreservedPatterns,
		Timestamp:          time.Now().UnixMilli(),
		TokenSaved:         m.estimateTokenSavings(foldable, result),
	}

	_, err := m.commitLog.Commit(commit, m.config.MaxStagedCommits)
	if err != nil {
		return err
	}

	m.phase = CollapsePhaseStaging
	return nil
}

// BlockingSpawn blocking generation collapse
// When 95% threshold is triggered, immediately execute collapse and apply to API request
// Return projection view
func (m *ContextCollapseManager) BlockingSpawn(original []session.Message) ([]session.Message, error) {
	if !m.config.Enabled {
		return original, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// First move all staged commits to applied
	m.commitLog.Drain()

	// Calculate new collapse range
	startIdx := m.commitLog.LastCommitEndIndex()
	endIdx := len(original) - m.config.PreserveCount

	if endIdx <= startIdx {
		// No more collapsible messages, return current projection
		return m.commitLog.ProjectView(original), nil
	}

	// Execute collapse
	foldable := original[startIdx:endIdx]
	result := m.collapser.Collapse(foldable, m.config.PreserveCount)

	commit := CollapseCommit{
		StartIndex:         startIdx,
		EndIndex:           endIdx,
		Summary:            result.CollapsedSummary,
		PreservedFiles:     result.PreservedFiles,
		PreservedDecisions: result.PreservedDecisions,
		PreservedErrors:    result.PreservedErrors,
		PreservedPatterns:  result.PreservedPatterns,
		Timestamp:          time.Now().UnixMilli(),
		TokenSaved:         m.estimateTokenSavings(foldable, result),
	}

	// P0-11 (FACT-CONC-001): use AppendApplied instead of directly accessing
	// commitLog.nextID / commitLog.applied, so CollapseCommitLog.mu is respected.
	m.commitLog.AppendApplied(commit)

	m.phase = CollapsePhaseBlocking

	return m.commitLog.ProjectView(original), nil
}

// ShouldAutoCompact determines if Autocompact should be executed
// When Collapse is active, return false to suppress Autocompact
func (m *ContextCollapseManager) ShouldAutoCompact() bool {
	if !m.config.Enabled {
		return true // Collapse not enabled, Autocompact works normally
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// If in staging, blocking, or draining phase, suppress Autocompact
	if m.phase == CollapsePhaseStaging ||
		m.phase == CollapsePhaseBlocking ||
		m.phase == CollapsePhaseDraining {
		return false
	}

	// If there are applied or staged collapses, also suppress Autocompact
	if m.commitLog.AppliedLen() > 0 || m.commitLog.StagedLen() > 0 {
		return false
	}

	return true
}

// RecoverFromOverflow 413 recovery flow
// Drain all staged collapses, retry request with projection view
// Return projection view and whether recovery succeeded
func (m *ContextCollapseManager) RecoverFromOverflow(original []session.Message) ([]session.Message, bool) {
	if !m.config.Enabled {
		return original, false
	}

	m.mu.Lock()
	m.phase = CollapsePhaseDraining

	// Drain staged collapses
	drainedCount := m.commitLog.Drain()
	m.mu.Unlock()

	// Use projection view
	projected := m.commitLog.ProjectView(original)

	// If collapses were drained, recovery is possible
	recovered := drainedCount > 0 || m.commitLog.AppliedLen() > 0

	if recovered {
		m.mu.Lock()
		m.phase = CollapsePhaseIdle
		m.mu.Unlock()
	}

	return projected, recovered
}

// ResetAfterCompact resets collapse state after compaction
// Called after full Pipeline execution
func (m *ContextCollapseManager) ResetAfterCompact() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.commitLog.Reset()
	m.phase = CollapsePhaseIdle
}

// GetPhase returns the current collapse phase
func (m *ContextCollapseManager) GetPhase() CollapsePhase {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.phase
}

// IsEnabled returns whether collapse is enabled
func (m *ContextCollapseManager) IsEnabled() bool {
	return m.config.Enabled
}

// ReadOnlyCollapseCommitLog is a read-only view over CollapseCommitLog.
//
// P0-10 (FACT-CONC-003): GetCommitLog previously returned the internal
// *CollapseCommitLog reference, allowing callers to call Commit/Drain/Reset
// and bypass the ContextCollapseManager mutex. This wrapper exposes only
// read-only methods; Commit always returns an error so the bug-reproduction
// test (TestBug_GetCommitLogLeaksReference) passes.
type ReadOnlyCollapseCommitLog struct {
	inner *CollapseCommitLog
}

// Commit always fails on the read-only view.
// Callers who need to mutate the commit log must go through ContextCollapseManager
// methods (StageCollapse / BlockingSpawn / RecoverFromOverflow / ResetAfterCompact).
func (r *ReadOnlyCollapseCommitLog) Commit(commit CollapseCommit, maxStaged int) (int, error) {
	return -1, fmt.Errorf("read-only commit log: mutation is not allowed via GetCommitLog")
}

// ProjectView generates the projection view (delegates to inner log).
func (r *ReadOnlyCollapseCommitLog) ProjectView(messages []session.Message) []session.Message {
	return r.inner.ProjectView(messages)
}

// Len returns the total commit count (delegates to inner log).
func (r *ReadOnlyCollapseCommitLog) Len() int {
	return r.inner.Len()
}

// StagedLen returns the staged commit count (delegates to inner log).
func (r *ReadOnlyCollapseCommitLog) StagedLen() int {
	return r.inner.StagedLen()
}

// AppliedLen returns the applied commit count (delegates to inner log).
func (r *ReadOnlyCollapseCommitLog) AppliedLen() int {
	return r.inner.AppliedLen()
}

// LastCommitEndIndex returns the last commit's EndIndex (delegates to inner log).
func (r *ReadOnlyCollapseCommitLog) LastCommitEndIndex() int {
	return r.inner.LastCommitEndIndex()
}

// GetCommitLog returns a read-only view of the collapse commit log.
//
// P0-10 (FACT-CONC-003): previously returned the internal *CollapseCommitLog
// reference, leaking mutable state to callers. Now returns a
// *ReadOnlyCollapseCommitLog wrapper whose Commit method always errors,
// preventing callers from bypassing the manager mutex.
func (m *ContextCollapseManager) GetCommitLog() *ReadOnlyCollapseCommitLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &ReadOnlyCollapseCommitLog{inner: m.commitLog}
}

// CheckCollapsePhase determines current collapse phase based on usage percentage
// usagePercent: context usage (0.0~1.0)
func (m *ContextCollapseManager) CheckCollapsePhase(usagePercent float64) CollapsePhase {
	if !m.config.Enabled {
		return CollapsePhaseIdle
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case usagePercent >= m.config.BlockThreshold: // 95%
		m.phase = CollapsePhaseBlocking
	case usagePercent >= m.config.StageThreshold: // 90%
		m.phase = CollapsePhaseStaging
	default:
		m.phase = CollapsePhaseIdle
	}

	return m.phase
}

// ProjectView returns the projection view of current messages
func (m *ContextCollapseManager) ProjectView(messages []session.Message) []session.Message {
	return m.commitLog.ProjectView(messages)
}

// ShouldFallbackToPipeline determines if should fallback to full compaction pipeline
// When collapse cannot compress further but context is still tight
func (m *ContextCollapseManager) ShouldFallbackToPipeline(original []session.Message, estimatedTokens int) bool {
	if !m.config.Enabled {
		return false
	}

	if m.budget.ContextWindow <= 0 {
		return false
	}

	usage := float64(estimatedTokens) / float64(m.budget.ContextWindow)

	// Context exceeds 97% and no more collapsible messages
	if usage >= m.budget.BlockPercent {
		m.mu.Lock()
		lastEnd := m.commitLog.LastCommitEndIndex()
		m.mu.Unlock()

		preserveCount := m.config.PreserveCount
		if lastEnd >= len(original)-preserveCount {
			return true // No more collapsible messages, need Pipeline
		}
	}

	return false
}

// estimateTokenSavings estimates token savings from collapse
func (m *ContextCollapseManager) estimateTokenSavings(original []session.Message, result *CollapseResult) int {
	if m.estimator == nil {
		return 0
	}

	originalTokens := 0
	for _, msg := range original {
		for _, block := range msg.Blocks {
			if block.Text != "" {
				originalTokens += m.estimator.EstimateTextTokens(block.Text)
			}
			if block.Output != "" {
				originalTokens += m.estimator.EstimateTextTokens(block.Output)
			}
		}
	}

	// Token count of summary
	summaryTokens := m.estimator.EstimateTextTokens(result.CollapsedSummary)

	saved := originalTokens - summaryTokens
	if saved < 0 {
		return 0
	}
	return saved
}
