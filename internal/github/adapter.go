package github

import (
	"context"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// AIAdapter adapts ai.Client to github.AIClient interface
// Used to bridge internal/ai and internal/github packages in tools.go
type AIAdapter struct {
	client          ai.Client
	coordinator     *ai.RecoveryCoordinator
	retryConfig     ai.RetryConfig
	fallbackTracker *ai.FallbackTracker
}

// NewAIAdapter creates an AI adapter, converting ai.Client to AIClient
func NewAIAdapter(client ai.Client) *AIAdapter {
	return &AIAdapter{
		client:      client,
		retryConfig: ai.DefaultRetryConfig(),
	}
}

// SetRecoveryConfig sets the recovery coordinator, retry config, and fallback tracker
func (a *AIAdapter) SetRecoveryConfig(coordinator *ai.RecoveryCoordinator, retryConfig ai.RetryConfig, fallbackTracker *ai.FallbackTracker) {
	a.coordinator = coordinator
	a.retryConfig = retryConfig
	a.fallbackTracker = fallbackTracker
}

// Stream implements AIClient interface, converting internal AI requests to ai.Client calls
func (a *AIAdapter) Stream(ctx context.Context, req AIRequest) (<-chan AIEvent, error) {
	// Convert message format
	messages := make([]ai.MessageEntry, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = ai.MessageEntry{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	aiReq := ai.Request{
		SystemPrompt: req.SystemPrompt,
		Messages:     messages,
		MaxTokens:    req.MaxTokens,
		Model:        req.Model,
	}

	// Always use StreamWithRetry for consistent retry/degrade/compress behavior.
	// When coordinator is nil, only Phase 1 retry runs (no degrade/compress).
	rc := a.retryConfig
	if rc.MaxRetries == 0 {
		rc = ai.DefaultRetryConfig()
	}
	aiEvents, err := ai.StreamWithRetry(ctx, a.client, aiReq, rc, a.fallbackTracker, a.coordinator)
	if err != nil {
		return nil, err
	}

	// Convert event channel
	// B5-P1-11: 改为阻塞发送，避免静默丢弃 AI 事件导致审查结果不完整。
	// 阻塞发送提供背压，当消费者慢时上游 StreamWithRetry 自然减速。
	// context 取消时退出 goroutine，防止永久阻塞。
	out := make(chan AIEvent, 64)
	go func() {
		defer close(out)
		for event := range aiEvents {
			aiEvent := convertAIEvent(event)
			if aiEvent != nil {
				select {
				case out <- aiEvent:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// convertAIEvent converts ai.Event to github.AIEvent
func convertAIEvent(event ai.Event) AIEvent {
	switch e := event.(type) {
	case ai.TextDeltaEvent:
		return AITextDeltaEvent{Content: e.Content}
	case ai.ErrorEvent:
		return AIErrorEvent{Err: e.Err}
	case ai.MessageStopEvent:
		return AIMessageStopEvent{}
	default:
		return nil
	}
}
