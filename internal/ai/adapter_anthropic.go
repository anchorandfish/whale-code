// Package ai — AnthropicAdapter 真实实现（§5.3，T1-A S3 落地）。
//
// 本文件实现 AnthropicAdapter 的 ProviderAdapter interface 4 方法：
// - BuildRequest：复用 SimpleClient.buildAnthropicBody 构造 HTTP 请求
// - ParseStream：迁移 SimpleClient.doStream 的 SSE 解析逻辑（含 cache_control 错误降级 + tokenSource 校准）
// - MapError：复用 ParseAPIError + cache_control 错误标记 ErrAIRetryable + disableCacheControlLocked
// - InjectPrivateFields：thinking 字段已由 buildAnthropicBody 内部注入，此处为 no-op（向后兼容）
//
// SNAPSHOT: 原 SimpleClient.streamAnthropic / buildAnthropicBody / doStream 保留不变，
// AnthropicAdapter 通过转发复用其逻辑，不破坏现有测试与 SimpleClient.Stream 路径。
// AnthropicAdapter.Stream 走 streamViaAdapter 通用路径（BuildRequest + httpClient.Do + MapError + ParseStream）。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ============================================================
// BuildRequest — 构造 Anthropic Messages API HTTP 请求
// ============================================================

// buildAnthropicRequest 构造 Anthropic Messages API 的 HTTP 请求（§5.3 S3）。
// 复用 SimpleClient.buildAnthropicBody 构造请求体（含 cache_control 注入、分层 system prompt、thinking 字段），
// 序列化为 JSON 后构造 *http.Request 并设置 Anthropic 必需 headers。
//
// 273 buildAnthropicBody
// 209 buildAnthropicURL
//
// 参数：
// - ctx: 请求上下文（用于超时与取消）
// - req: 统一 Request 结构（SystemPrompt/Messages/Tools/Thinking 等）
// - snap: SimpleClient 字段快照（model/apiKey/baseURL 等只读视图）
//
// 返回：构造完成的 *http.Request，调用方负责 httpClient.Do 发送。
func (a *AnthropicAdapter) buildAnthropicRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	// 复用 SimpleClient.buildAnthropicBody 构造请求体（含 thinking 注入）
	body := a.SimpleClient.buildAnthropicBody(req, snap)

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildAnthropicURL(snap), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create anthropic request: %w", err)
	}

	// Anthropic Messages API 必需 headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", snap.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	return httpReq, nil
}

// ============================================================
// ParseStream — 迁移 doStream 的 SSE 解析逻辑
// ============================================================

// parseAnthropicStream 解析 Anthropic SSE 流并转为统一 Event 通道（§5.3 S3）。
// 迁移自 SimpleClient.doStream（provider.go:619），保留以下关键逻辑：
// - SSEParser 解析 SSE 事件流
// - ParseAnthropicEvent 转换为统一 Event
// - T1-B：UsageEvent 同步调用 tokenSource.RecordRemoteUsage 校准 EMA offset
// - cache_control 错误降级（在 MapError 中处理，此处仅负责成功响应解析）
//
// 619 doStream
// SSEParser / ParseAnthropicEvent
//
// 参数：
// - resp: HTTP 200 成功响应（非 200 由 streamViaAdapter 走 MapError 路径，不进入此函数）
//
// 返回：统一 Event 通道（goroutine 内异步推送，调用方消费完自动 close）。
func (a *AnthropicAdapter) parseAnthropicStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	// 捕获 tokenSource（持 RLock 快照，避免 goroutine 内长时间持锁）
	a.SimpleClient.mu.RLock()
	tokenSource := a.SimpleClient.tokenSource
	model := a.SimpleClient.model
	a.SimpleClient.mu.RUnlock()

	events := make(chan Event, 64)
	reqCtx := resp.Request.Context()

	go func() {
		// Ensure resp.Body is always closed to avoid connection leaks
		defer resp.Body.Close()
		defer close(events)

		sseCh := make(chan SSEEvent, 64)
		go func() {
			defer close(sseCh)
			parser := NewSSEParser()
			if err := parser.ParseStream(resp.Body, sseCh); err != nil {
				select {
				case sseCh <- SSEEvent{Event: "error", Data: err.Error()}:
				case <-reqCtx.Done():
					return
				default:
				}
			}
		}()

		for sse := range sseCh {
			if sse.Event == "error" {
				select {
				case events <- ErrorEvent{Err: fmt.Errorf("sse error: %w", fmt.Errorf("%s", sse.Data))}:
				case <-reqCtx.Done():
					return
				}
				return
			}

			event, parseErr := ParseAnthropicEvent(sse.Data)
			if parseErr != nil {
				select {
				case events <- ErrorEvent{Err: parseErr}:
				case <-reqCtx.Done():
					return
				}
				return
			}

			// T1-B：UsageEvent 同步调用 RecordRemoteUsage（在 events chan 发送前）
			// 吸纳优化 4：确保即使调用方不消费 events 也能校准 EMA offset。
			// B-P1 EMA 修复：透传 estimatedInputTokens（由 streamViaAdapter 从 req.EstimatedInputTokens 传入），
			// 使 RecordRemoteUsage 能校准 EMA offset（旧实现硬编码 0 导致 offset 校准被跳过）。
			if tokenSource != nil && event != nil {
				if usageEvt, ok := event.(UsageEvent); ok {
					// B-P2 口径修复：Anthropic input_tokens 不含 cache 部分，
					// 完整输入 = input + cache_read + cache_creation（与本地
					// 全量上下文估算同口径）。cache 命中时若不并回，offset 会
					// 大幅偏负把校准值拉低。UsageTracker 记账在 TUI 侧用原始
					// usage，此处仅影响 EMA offset 计算。
					actualUsage := UnifiedUsage{
						InputTokens:   usageEvt.InputTokens + usageEvt.CacheRead + usageEvt.CacheCreation,
						OutputTokens:  usageEvt.OutputTokens,
						CacheCreation: usageEvt.CacheCreation,
						CacheRead:     usageEvt.CacheRead,
					}
					tokenSource.RecordRemoteUsage(model, actualUsage, estimatedInputTokens)
				}
			}

			if event != nil {
				select {
				case events <- event:
				case <-reqCtx.Done():
					return
				}
			}
		}
	}()

	return events, nil
}

// ============================================================
// MapError — 映射 Anthropic 错误码到 *APIError
// ============================================================

// mapAnthropicError 映射 Anthropic API 错误响应到 *APIError（§5.3 S3）。
// 复用 ParseAPIError 解析 JSON body，附加 Anthropic 特有的错误处理：
// - IsContextOverflowError → ErrorID = ErrAIFatal（不可重试）
// - isCacheControlError → disableCacheControlLocked + ErrorID = ErrAIRetryable（重试时跳过 cache_control）
//
// 69 ParseAPIError
// 541 IsContextOverflowError
// 198 isCacheControlError
// 193 disableCacheControlLocked
//
// 参数：
// - resp: 非 200 HTTP 响应（streamViaAdapter 在 resp.StatusCode != 200 时调用）
//
// 返回：填充完整的 *APIError；解析失败时返回兜底 ErrAIFatal。
func (a *AnthropicAdapter) mapAnthropicError(resp *http.Response) *APIError {
	apiErr := ParseAPIError(resp)
	if apiErr == nil {
		apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
	}

	// context overflow 标记为不可恢复错误
	// 结构化优先 —— apiErr 已带 StatusCode/ErrorID（Message 里含裸 "4132" 这类
	// 数字时，旧散文判据会把一次可重试的 5xx 误升为不可重试）。
	if apiErr.IsContextOverflow() {
		apiErr.ErrorID = ErrAIFatal
	}

	// cache_control 错误：禁用 cache_control 并标记可重试（retry.go 识别后重试时不带 cache_control）
	if isCacheControlError(apiErr.Message) {
		a.SimpleClient.mu.Lock()
		a.SimpleClient.disableCacheControlLocked()
		a.SimpleClient.mu.Unlock()
		// 方案 M：cache 探测失败 → 禁用 cache 后可重试，标记 ErrAIRetryable（02 §2.3.2 契约）。
		apiErr.ErrorID = ErrAIRetryable
	}

	return apiErr
}

// ============================================================
// InjectPrivateFields — Anthropic 私有字段注入
// ============================================================

// injectAnthropicPrivateFields 注入 Anthropic 私有高级字段（§5.3 S3）。
//
// Anthropic 的私有字段为 thinking.budget_tokens（仅 SupportsThinking=true 的模型支持）。
// 但当前实现中 buildAnthropicBody 已内部处理 thinking 注入（provider.go:362-371），
// 为保持向后兼容（现有 SimpleClient.streamAnthropic 路径不变），此处为 no-op。
//
// 未来重构方向（§5.3 注释）：将 thinking 注入从 buildAnthropicBody 提取到此处，
// 让 InjectPrivateFields 成为唯一的私有字段注入入口，buildAnthropicBody 仅构造公共字段。
//
// 参数：
// - body: 请求体 map（buildAnthropicBody 已填充 thinking 字段，此处不修改）
// - req: 统一 Request 结构（含 Thinking 配置，此处不读取）
// - cap: 模型能力描述（含 SupportsThinking 标记，此处不读取）
func (a *AnthropicAdapter) injectAnthropicPrivateFields(body map[string]interface{}, req Request, cap ModelCapability) {
	// no-op：thinking 字段已由 buildAnthropicBody 内部注入（provider.go:362-371）
	// 保留方法签名以满足 ProviderAdapter interface 契约。
	// 未来重构时将 thinking 注入逻辑迁移至此处。
}
