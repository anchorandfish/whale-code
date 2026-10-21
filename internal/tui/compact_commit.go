package tui

import (
	"fmt"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/session"
)

func newCompactCostRecorder(model string, tracker *ai.UsageTracker) *ctxpkg.CompactCostRecorder {
	return ctxpkg.NewCompactCostRecorder(metrics.Default, tracker, model)
}

func cacheCapabilityEpoch(caching *ai.CachingIntegration) uint64 {
	if caching == nil {
		return 0
	}
	return caching.CapabilityEpoch()
}

func (m *Model) applyCompactUsage(usage ai.UsageEvent, ok bool, sessionEpoch, modelEpoch, cacheEpoch uint64) {
	if !ok {
		return
	}
	if caching := m.aiService.Caching(); caching != nil {
		if m.SessionModel.sessionEpoch == sessionEpoch &&
			m.SessionModel.modelEpoch == modelEpoch &&
			cacheCapabilityEpoch(caching) == cacheEpoch {
			caching.RecordCacheMetrics(usage.InputTokens, usage.OutputTokens, usage.CacheRead)
		} else {
			caching.RecordHistoricalCacheMetrics(usage.InputTokens, usage.OutputTokens, usage.CacheRead)
		}
	}
	if m.SessionModel.sessionEpoch == sessionEpoch {
		m.sessionService.SetInputTokens(m.sessionService.InputTokens() + usage.InputTokens)
		m.sessionService.SetOutputTokens(m.sessionService.OutputTokens() + usage.OutputTokens)
		m.sessionService.SetRequestCount(m.sessionService.RequestCount() + 1)
	}
}

func (m *Model) applyCompactRecorderUsage(recorder *ctxpkg.CompactCostRecorder, sessionEpoch, modelEpoch, cacheEpoch uint64) {
	usage, ok := recorder.TotalUsage()
	m.applyCompactUsage(usage, ok, sessionEpoch, modelEpoch, cacheEpoch)
}

func (c *runnerCore) applyCompactRecorderUsage(recorder *ctxpkg.CompactCostRecorder, sessionEpoch uint64, model string, cacheEpoch uint64) {
	usage, ok := recorder.TotalUsage()
	if !ok {
		return
	}
	if c.Caching != nil {
		if c.sessionEpoch.Load() == sessionEpoch && c.ModelName == model && cacheCapabilityEpoch(c.Caching) == cacheEpoch {
			c.Caching.RecordCacheMetrics(usage.InputTokens, usage.OutputTokens, usage.CacheRead)
		} else {
			c.Caching.RecordHistoricalCacheMetrics(usage.InputTokens, usage.OutputTokens, usage.CacheRead)
		}
	}
	if c.sessionEpoch.Load() == sessionEpoch {
		c.InputTokens += usage.InputTokens
		c.OutputTokens += usage.OutputTokens
		c.RequestCount++
	}
}

// shouldRunAutoCompact 已被 autoCompactDue（blocked_handoff.go）替代：
// 压缩体系重构后，自动压缩触发线（window-buffer）与提示线
// （StateShouldCompact 0.80）/兜底线（StateBlocked 0.97 硬停）职责分离，
// 仅凭 State 无法判定是否压缩，需传入 budget 与 token 估算值。

func sessionRevision(sess *session.Session) uint64 {
	if sess == nil {
		return 0
	}
	_, _, revision, _ := sess.Snapshot()
	return revision
}

// compactCommittedSince reports whether compaction actually installed a new
// transcript. Reactive retries must use this state transition, not formatted
// command text or a merely non-empty session.
func compactCommittedSince(sess *session.Session, before uint64) bool {
	return sess != nil && sessionRevision(sess) != before
}

func compactFallbackOverhead(budget ctxpkg.Budget) int {
	overhead := budget.AutoCompactBufferTokens
	if overhead <= 0 {
		overhead = ctxpkg.DefaultAutoCompactBufferTokens
	}
	return overhead
}

func systemMessageOverhead(estimator *ai.TokenEstimator) int {
	static, perSystem := compactRequestOverheads(estimator, contextRequestSnapshot{Valid: true})
	_ = static
	return perSystem
}

func (m *Model) compactRequestOverheads(budget ctxpkg.Budget) (int, int) {
	snapshot := m.SessionModel.requestSnapshot
	if !snapshot.Valid || snapshot.Model != m.sessionService.ModelName() {
		return compactFallbackOverhead(budget), systemMessageOverhead(m.aiService.Estimator())
	}
	return compactRequestOverheads(m.aiService.Estimator(), snapshot)
}

func (c *runnerCore) compactRequestOverheads(budget ctxpkg.Budget) (int, int) {
	snapshot := c.requestSnapshot
	if !snapshot.Valid || snapshot.Model != c.ModelName {
		return compactFallbackOverhead(budget), systemMessageOverhead(c.Estimator)
	}
	return compactRequestOverheads(c.Estimator, snapshot)
}

func compactCandidateSession(source *session.Session, messages []session.Message, count int) *session.Session {
	candidate := session.NewSession()
	candidate.Version = source.Version
	_, metadata, _, _ := source.Snapshot()
	candidate.Metadata = metadata
	candidate.Metadata.CompactionCount = count
	candidate.SetMessages(messages)
	return candidate
}

func recoveryForCandidate(messages []session.Message, source *session.Session, readFiles []string) []session.Message {
	_, metadata, _, _ := source.Snapshot()
	temp := compactCandidateSession(source, messages, metadata.CompactionCount)
	return ctxpkg.Recover(readFiles, temp, ctxpkg.DefaultPostCompactRecovery())
}

func (m *Model) captureCompactState() (session.TranscriptVersion, uint64, error) {
	current := m.sessionService.CurrentSession()
	if current == nil {
		return session.TranscriptVersion{}, 0, fmt.Errorf("no active session")
	}
	_, _, revision, _ := current.Snapshot()
	store := m.sessionService.Store()
	if store == nil {
		return session.TranscriptVersion{}, revision, nil
	}
	if m.sessionService.SessionName() == "" {
		m.autoNameSession()
	}
	version, err := store.TranscriptVersion(m.sessionService.SessionName())
	return version, revision, err
}

func (c *runnerCore) captureCompactState() (session.TranscriptVersion, uint64, error) {
	if c.Session == nil {
		return session.TranscriptVersion{}, 0, fmt.Errorf("no active session")
	}
	_, _, revision, _ := c.Session.Snapshot()
	if c.Store == nil {
		return session.TranscriptVersion{}, revision, nil
	}
	if c.SessionName == "" {
		c.autoNameSession()
		if c.SessionName == "" {
			c.SessionName = session.GenerateID()
		}
	}
	version, err := c.Store.TranscriptVersion(c.SessionName)
	return version, revision, err
}

func (m *Model) commitCompactResult(result *ctxpkg.CompactResult, cfg ctxpkg.CompactionConfig, pipeline *ctxpkg.CompactionPipeline, expectedVersion session.TranscriptVersion, expectedRevision uint64) error {
	current := m.sessionService.CurrentSession()
	if current == nil {
		return fmt.Errorf("no active session")
	}
	var readFiles []string
	if exec := m.toolService.Executor(); exec != nil && exec.SearchCtx != nil {
		readFiles = exec.SearchCtx.GetReadFiles()
	}
	recovery := recoveryForCandidate(result.CompactedMessages, current, readFiles)
	if !ctxpkg.FinalizeCompactResult(result, pipeline, recovery, cfg, m.aiService.Estimator()) {
		return nil
	}

	_, metadata, _, _ := current.Snapshot()
	count := metadata.CompactionCount + 1
	messages := session.NormalizeMessageChain(result.CompactedMessages)
	candidate := compactCandidateSession(current, messages, count)
	store := m.sessionService.Store()
	name := m.sessionService.SessionName()
	if name == "" {
		m.autoNameSession()
		name = m.sessionService.SessionName()
	}

	if m.saveMu == nil {
		m.saveMu = &sync.Mutex{}
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if store != nil && name != "" {
		if err := store.Rewrite(name, expectedVersion, current, expectedRevision, candidate); err != nil {
			return err
		}
	} else if !current.ReplaceIfRevision(expectedRevision, messages, candidate.Metadata) {
		return session.ErrSessionRewriteConflict
	}

	m.sessionService.SetCompactCount(count)
	m.sessionService.SetSavedMsgCount(len(messages))
	if pipeline != nil {
		pipeline.CommitPrepared()
	}
	return nil
}

func (c *runnerCore) commitCompactResult(result *ctxpkg.CompactResult, cfg ctxpkg.CompactionConfig, pipeline *ctxpkg.CompactionPipeline, expectedVersion session.TranscriptVersion, expectedRevision uint64) error {
	if c.Session == nil {
		return fmt.Errorf("no active session")
	}
	var readFiles []string
	if c.Executor != nil && c.Executor.SearchCtx != nil {
		readFiles = c.Executor.SearchCtx.GetReadFiles()
	}
	recovery := recoveryForCandidate(result.CompactedMessages, c.Session, readFiles)
	if !ctxpkg.FinalizeCompactResult(result, pipeline, recovery, cfg, c.Estimator) {
		return nil
	}

	_, metadata, _, _ := c.Session.Snapshot()
	count := metadata.CompactionCount + 1
	messages := session.NormalizeMessageChain(result.CompactedMessages)
	candidate := compactCandidateSession(c.Session, messages, count)
	if c.SessionName == "" {
		c.autoNameSession()
		if c.SessionName == "" {
			c.SessionName = session.GenerateID()
		}
	}
	if c.Store != nil {
		if err := c.Store.Rewrite(c.SessionName, expectedVersion, c.Session, expectedRevision, candidate); err != nil {
			return err
		}
	} else if !c.Session.ReplaceIfRevision(expectedRevision, messages, candidate.Metadata) {
		return session.ErrSessionRewriteConflict
	}

	c.SavedMsgCount = len(messages)
	if pipeline != nil {
		pipeline.CommitPrepared()
	}
	return nil
}
