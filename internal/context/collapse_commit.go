// Package context — collapse commit log
// Maintains commit log of collapse operations, generates message view via read-time projection without modifying original message array
package context

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/session"
)

// CollapseCommit is a collapse commit record
// Records metadata of a collapse operation, used to generate projection view from original message array
type CollapseCommit struct {
	// ID is the unique commit identifier (incremental)
	ID int
	// StartIndex is the start message index of collapse (in original array)
	StartIndex int
	// EndIndex is the end message index of collapse (exclusive)
	EndIndex int
	// Summary is the collapse summary text
	Summary string
	// PreservedFiles are the extracted file references
	PreservedFiles []string
	// PreservedDecisions are the extracted key decisions
	PreservedDecisions []string
	// PreservedErrors are the extracted error fix information
	PreservedErrors []string
	// PreservedPatterns are the extracted code patterns
	PreservedPatterns []string
	// Timestamp is the commit timestamp
	Timestamp int64
	// TokenSaved is the estimated saved token count
	TokenSaved int
}

// CollapseCommitLog is the collapse commit log
// Maintains staged and applied collapse operations for generating projection view
type CollapseCommitLog struct {
	// staged collapses staged but not yet applied to API request
	staged []CollapseCommit
	// applied collapses applied to API request
	applied []CollapseCommit
	// nextID is the next commit ID
	nextID int
	// mu is the mutex protecting concurrent access
	mu sync.RWMutex
}

// NewCollapseCommitLog creates a new collapse commit log
func NewCollapseCommitLog() *CollapseCommitLog {
	return &CollapseCommitLog{
		staged:  make([]CollapseCommit, 0),
		applied: make([]CollapseCommit, 0),
		nextID:  0,
	}
}

// Commit adds a collapse commit to the staging area
// Returns commit ID; returns error if staging area is full (reached maxStaged)
func (l *CollapseCommitLog) Commit(commit CollapseCommit, maxStaged int) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.staged) >= maxStaged {
		return -1, fmt.Errorf("staging area full (%d/%d)", len(l.staged), maxStaged)
	}

	commit.ID = l.nextID
	l.nextID++
	l.staged = append(l.staged, commit)
	return commit.ID, nil
}

// AppendApplied appends a commit directly to the applied area, bypassing staging.
// Used by BlockingSpawn for immediate collapse application under the manager lock.
// Internally acquires the commit log's own mutex to safely mutate applied/nextID.
// Returns the assigned commit ID.
//
// P0-11 (FACT-CONC-001): provides a lock-respecting alternative to direct
// field access (commitLog.nextID / commitLog.applied) so that BlockingSpawn
// no longer bypasses CollapseCommitLog.mu.
func (l *CollapseCommitLog) AppendApplied(commit CollapseCommit) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	commit.ID = l.nextID
	l.nextID++
	l.applied = append(l.applied, commit)
	return commit.ID
}

// allCommits returns a copy of all commits (staged + applied)
func (l *CollapseCommitLog) allCommits() []CollapseCommit {
	all := make([]CollapseCommit, 0, len(l.staged)+len(l.applied))
	all = append(all, l.applied...)
	all = append(all, l.staged...)
	return all
}

// ProjectView generates projection view from original message array and commit log
// Does not modify original message array, only generates new message slice
func (l *CollapseCommitLog) ProjectView(messages []session.Message) []session.Message {
	l.mu.RLock()
	commits := l.allCommits()
	l.mu.RUnlock()

	if len(commits) == 0 {
		return messages
	}

	// 1. Mark collapsed message index ranges
	collapsedRanges := make(map[int]bool)
	for _, commit := range commits {
		for i := commit.StartIndex; i < commit.EndIndex; i++ {
			collapsedRanges[i] = true
		}
	}

	// 2. Collect non-collapsed messages
	var preserved []session.Message
	for i, msg := range messages {
		if !collapsedRanges[i] {
			preserved = append(preserved, msg)
		}
	}

	// 3. Sort commits by StartIndex
	sort.Slice(commits, func(i, j int) bool {
		return commits[i].StartIndex < commits[j].StartIndex
	})

	// 4. Build result by iterating original positions, inserting summaries at their StartIndex
	commitSummaryMap := make(map[int]session.Message)
	for _, commit := range commits {
		summaryText := buildProjectedSummary(&commit)
		commitSummaryMap[commit.StartIndex] = session.Message{
			Role: session.RoleSystem,
			Blocks: []session.ContentBlock{
				{Type: "text", Text: summaryText},
			},
			Timestamp: commit.Timestamp,
		}
	}

	result := make([]session.Message, 0, len(commits)+len(preserved))
	insertedSummaries := make(map[int]bool)
	for i, msg := range messages {
		if sm, ok := commitSummaryMap[i]; ok && !insertedSummaries[i] {
			result = append(result, sm)
			insertedSummaries[i] = true
		}
		if !collapsedRanges[i] {
			result = append(result, msg)
		}
	}
	for _, commit := range commits {
		if !insertedSummaries[commit.StartIndex] {
			summaryText := buildProjectedSummary(&commit)
			result = append(result, session.Message{
				Role: session.RoleSystem,
				Blocks: []session.ContentBlock{
					{Type: "text", Text: summaryText},
				},
				Timestamp: commit.Timestamp,
			})
		}
	}

	return result
}

// Drain drains the staging area, moving all staged commits to applied area
// Returns the number of drained commits
func (l *CollapseCommitLog) Drain() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := len(l.staged)
	l.applied = append(l.applied, l.staged...)
	l.staged = l.staged[:0]
	return count
}

// DrainCommits drains specified number of staged commits, for 413 recovery
// Returns list of drained commit records, also moves them to applied area
func (l *CollapseCommitLog) DrainCommits(n int) []CollapseCommit {
	l.mu.Lock()
	defer l.mu.Unlock()

	if n <= 0 || len(l.staged) == 0 {
		return nil
	}

	if n > len(l.staged) {
		n = len(l.staged)
	}

	drained := make([]CollapseCommit, n)
	copy(drained, l.staged[:n])
	l.applied = append(l.applied, l.staged[:n]...)
	l.staged = l.staged[n:]
	return drained
}

// Reset clears all commits (staged + applied), resets ID counter
// Called after full compaction
func (l *CollapseCommitLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.staged = l.staged[:0]
	l.applied = l.applied[:0]
	l.nextID = 0
}

// Len returns total count of all commits
func (l *CollapseCommitLog) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.staged) + len(l.applied)
}

// StagedLen returns the count of staged commits
func (l *CollapseCommitLog) StagedLen() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.staged)
}

// AppliedLen returns the count of applied commits
func (l *CollapseCommitLog) AppliedLen() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.applied)
}

// LastCommitEndIndex returns the EndIndex of the last commit
// Used to determine the start position of the next collapse
func (l *CollapseCommitLog) LastCommitEndIndex() int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	maxEnd := 0
	for _, c := range l.applied {
		if c.EndIndex > maxEnd {
			maxEnd = c.EndIndex
		}
	}
	for _, c := range l.staged {
		if c.EndIndex > maxEnd {
			maxEnd = c.EndIndex
		}
	}
	return maxEnd
}

// buildProjectedSummary builds projection summary from collapse commit record
func buildProjectedSummary(commit *CollapseCommit) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("[Collapse Summary #%d — %d messages]", commit.ID, commit.EndIndex-commit.StartIndex))

	if len(commit.PreservedDecisions) > 0 {
		parts = append(parts, "\nKey Decisions:")
		for _, d := range commit.PreservedDecisions {
			parts = append(parts, fmt.Sprintf("  - %s", d))
		}
	}
	if len(commit.PreservedFiles) > 0 {
		parts = append(parts, fmt.Sprintf("\nRelated Files: %s", strings.Join(commit.PreservedFiles, ", ")))
	}
	if len(commit.PreservedErrors) > 0 {
		parts = append(parts, "\nErrors and Fixes:")
		for _, e := range commit.PreservedErrors {
			parts = append(parts, fmt.Sprintf("  - %s", e))
		}
	}
	if len(commit.PreservedPatterns) > 0 {
		parts = append(parts, "\nCode Patterns:")
		for _, p := range commit.PreservedPatterns {
			parts = append(parts, fmt.Sprintf("  - %s", p))
		}
	}

	return strings.Join(parts, "\n")
}
