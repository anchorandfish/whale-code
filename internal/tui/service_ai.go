package tui

import (
	"context"
	"fmt"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
)

// StreamRequest sends a streaming AI request with retry/degrade/compress recovery
func (a *AIModel) StreamRequest(ctx context.Context, req *ai.Request) (<-chan ai.Event, error) {
	return ai.StreamWithRetry(ctx, a.aiClient, *req, a.retryConfig, a.fallback, a.recoveryCoordinator)
}

// HandleStreamError processes streaming errors
func (a *AIModel) HandleStreamError(err error) error {
	return fmt.Errorf("stream error: %w", err)
}

// Client returns the AI client
func (a *AIModel) Client() *ai.SimpleClient {
	return a.aiClient
}

// ModelRegistry returns the model registry
func (a *AIModel) ModelRegistry() *ai.ModelRegistry {
	return a.modelRegistry
}

// Budget returns the context budget
func (a *AIModel) Budget() ctxpkg.Budget {
	return a.budget
}

// SetBudget sets the context budget
func (a *AIModel) SetBudget(budget ctxpkg.Budget) {
	a.budget = budget
}

// Estimator returns the token estimator
func (a *AIModel) Estimator() *ai.TokenEstimator {
	return a.estimator
}

// Compactor returns the context compactor
func (a *AIModel) Compactor() *ctxpkg.Compactor {
	return a.compactor
}

// AICompactor returns the AI compactor
func (a *AIModel) AICompactor() *ctxpkg.AICompactor {
	return a.aiCompactor
}

// Fallback returns the fallback tracker
func (a *AIModel) Fallback() *ai.FallbackTracker {
	return a.fallback
}

// RecoveryCoordinator returns the recovery coordinator
func (a *AIModel) RecoveryCoordinator() *ai.RecoveryCoordinator {
	return a.recoveryCoordinator
}

// SetStreamCancel sets the stream cancellation function
func (a *AIModel) SetStreamCancel(cancel context.CancelFunc) {
	a.streamCancel = cancel
}

// StreamCancel returns the stream cancellation function
func (a *AIModel) StreamCancel() context.CancelFunc {
	return a.streamCancel
}

// MagicDocs returns the magic docs manager
func (a *AIModel) MagicDocs() *config.MagicDocs {
	return a.magicDocs
}

// MemoryStore returns the unified memory store
func (a *AIModel) MemoryStore() *config.MemoryStore {
	return a.memStore
}

// MemoryIndex returns the MEMORY.md index
func (a *AIModel) MemoryIndex() *config.MemoryIndex {
	return a.memIndex
}

// MemSelector returns the memory selector
func (a *AIModel) MemSelector() *config.MemorySelector {
	return a.memSelector
}

// MemExtractor returns the auto memory extractor
func (a *AIModel) MemExtractor() *config.AutoMemoryExtractor {
	return a.memExtractor
}

// RequestID returns the current request ID
func (a *AIModel) RequestID() string {
	return a.requestID
}

// SetRequestID sets the current request ID
func (a *AIModel) SetRequestID(id string) {
	a.requestID = id
}

// RetryConfig returns the retry configuration
func (a *AIModel) RetryConfig() ai.RetryConfig {
	return a.retryConfig
}

// SetRetryConfig sets the retry configuration
func (a *AIModel) SetRetryConfig(config ai.RetryConfig) {
	a.retryConfig = config
}

// Caching returns the caching integration layer (N11 修复 N2：打通 TUI 路径 cacheInfo 通路)
func (a *AIModel) Caching() *ai.CachingIntegration {
	return a.caching
}
