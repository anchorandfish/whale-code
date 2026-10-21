// Package ai — OpenAIAdapter 真实实现（§5.3，T1-A S4 落地）。
//
// 本文件实现 OpenAIAdapter 的 ProviderAdapter interface 4 方法：
// - BuildRequest：复用 SimpleClient.buildOpenAIBody 构造 HTTP 请求
// - ParseStream：迁移 SimpleClient.doStreamOpenAI 的 SSE 解析逻辑（含 tool_calls 增量累积 + tokenSource 校准）
// - MapError：复用 ParseAPIError + cache_control 错误标记 ErrAIRetryable + disableCacheControlLocked
// - InjectPrivateFields：OpenAI/GLM 无私有字段，no-op；DeepSeek 的 prompt_cache_key 由 DeepSeekAdapter 覆写
//
// SNAPSHOT: 原 SimpleClient.streamOpenAI / buildOpenAIBody / doStreamOpenAI 保留不变，
// OpenAIAdapter 通过转发复用其逻辑，不破坏现有测试与 SimpleClient.Stream 路径。
// OpenAIAdapter.Stream 走 streamViaAdapter 通用路径（BuildRequest + httpClient.Do + MapError + ParseStream）。
//
// GLM 复用此适配器（Provider="openai" + model 以 "glm" 开头，无额外私有字段）。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ============================================================
// BuildRequest — 构造 OpenAI Chat Completions API HTTP 请求
// ============================================================

// buildOpenAIRequest 构造 OpenAI Chat Completions API 的 HTTP 请求（§5.3 S4）。
// 复用 SimpleClient.buildOpenAIBody 构造请求体（含 response_format/tools/enable_thinking/prompt_cache_key），
// 序列化为 JSON 后构造 *http.Request 并设置 OpenAI 必需 headers。
//
// 473 buildOpenAIBody
// 219 buildOpenAIURL
//
// 参数：
// - ctx: 请求上下文（用于超时与取消）
// - req: 统一 Request 结构（SystemPrompt/Messages/Tools/ResponseSchema/Thinking 等）
// - snap: SimpleClient 字段快照（model/apiKey/baseURL 等只读视图）
//
// 返回：构造完成的 *http.Request，调用方负责 httpClient.Do 发送。
func (a *OpenAIAdapter) buildOpenAIRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	// 复用 SimpleClient.buildOpenAIBody 构造请求体
	// 注：buildOpenAIBody 内部已处理 enable_thinking 注入与 injectPromptCacheKeyForDeepSeek（OpenAI 分支下 deepseek 路径）
	body := a.SimpleClient.buildOpenAIBody(req, snap)

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildOpenAIURL(snap), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create openai request: %w", err)
	}

	// OpenAI Chat Completions API 必需 headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+snap.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	return httpReq, nil
}

// ============================================================
// ParseStream — 迁移 doStreamOpenAI 的 SSE 解析逻辑
// ============================================================

// parseOpenAIStream 解析 OpenAI SSE 流并转为统一 Event 通道（§5.3 S4）。
// 迁移自 SimpleClient.doStreamOpenAI（provider.go:715），保留以下关键逻辑：
// - SSEParser 解析 SSE 事件流
// - parseOpenAIDeltaWithAccum 转换为统一 Event（含 tool_calls 增量累积）
// - [DONE] 标记触发 tool_use 事件 flush + MessageStopEvent
// - T1-B：UsageEvent 同步调用 tokenSource.RecordRemoteUsage 校准 EMA offset
//
// 715 doStreamOpenAI
// 827 parseOpenAIDeltaWithAccum
// SSEParser
//
// 参数：
// - resp: HTTP 200 成功响应（非 200 由 streamViaAdapter 走 MapError 路径，不进入此函数）
//
// 返回：统一 Event 通道（goroutine 内异步推送，调用方消费完自动 close）。
func (a *OpenAIAdapter) parseOpenAIStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
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

		// OpenAI tool_calls 增量累积：在 [DONE] 时 flush 为完整 ToolUseEvent
		pendingToolArgs := make(map[int]string)
		pendingToolNames := make(map[int]string)
		pendingToolIDs := make(map[int]string)
		lastFinishReason := ""

		for sse := range sseCh {
			if sse.Event == "error" {
				select {
				case events <- ErrorEvent{Err: fmt.Errorf("sse error: %w", fmt.Errorf("%s", sse.Data))}:
				case <-reqCtx.Done():
					return
				}
				return
			}

			// [DONE] 标记：flush tool_calls 累积 + MessageStopEvent
			if sse.Data == "[DONE]" {
				for idx, args := range pendingToolArgs {
					name := pendingToolNames[idx]
					id := pendingToolIDs[idx]
					if id == "" {
						id = fmt.Sprintf("call_%d", idx)
					}
					if strings.TrimSpace(args) == "" {
						args = "{}"
					}
					select {
					case events <- ToolUseEvent{ID: id, Name: name, Input: args}:
					case <-reqCtx.Done():
						return
					}
				}
				select {
				case events <- MessageStopEvent{FinishReason: lastFinishReason}:
				case <-reqCtx.Done():
					return
				}
				return
			}

			evts, _, chunkFinishReason := parseOpenAIDeltaWithAccum(sse.Data, pendingToolArgs, pendingToolNames, pendingToolIDs)

			if chunkFinishReason != "" {
				lastFinishReason = chunkFinishReason
			}
			// 一帧可能产出多个事件（同帧 reasoning + content），逐个按原逻辑处理。
			// T1-B：UsageEvent 同步调用 RecordRemoteUsage（在 events chan 发送前）
			// 吸纳优化 4：确保即使调用方不消费 events 也能校准 EMA offset。
			// B-P1 EMA 修复：透传 estimatedInputTokens（由 streamViaAdapter 从 req.EstimatedInputTokens 传入），
			// 使 RecordRemoteUsage 能校准 EMA offset（旧实现硬编码 0 导致 offset 校准被跳过）。
			for _, event := range evts {
				if tokenSource != nil && event != nil {
					if usageEvt, ok := event.(UsageEvent); ok {
						actualUsage := UnifiedUsage{
							InputTokens:   usageEvt.InputTokens,
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
		}
	}()

	return events, nil
}

// ============================================================
// MapError — 映射 OpenAI 错误码到 *APIError
// ============================================================

// mapOpenAIError 映射 OpenAI API 错误响应到 *APIError（§5.3 S4）。
// 复用 ParseAPIError 解析 JSON body，附加 OpenAI 特有的错误处理：
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
func (a *OpenAIAdapter) mapOpenAIError(resp *http.Response) *APIError {
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

	// cache_control 错误：禁用 cache_control 并标记可重试（OpenAI 分支下基本不会触发，保留以备 OpenAI prefix cache 异常）
	if isCacheControlError(apiErr.Message) {
		a.SimpleClient.mu.Lock()
		a.SimpleClient.disableCacheControlLocked()
		a.SimpleClient.mu.Unlock()
		apiErr.ErrorID = ErrAIRetryable
	}

	// S6 / L1：端点不认 enable_search ⇒ 摘掉这一个能力并重试（与 cacheControl 同款降级粒度）。
	// ★ 这条路径是**用户实际走的那条**（provider:"openai" + 自定义 base_url）。
	if isNativeSearchUnsupportedError(apiErr.Message) {
		a.SimpleClient.mu.Lock()
		a.SimpleClient.disableNativeSearchLocked()
		a.SimpleClient.mu.Unlock()
		apiErr.ErrorID = ErrAIRetryable
	}

	return apiErr
}

// ============================================================
// InjectPrivateFields — OpenAI/GLM 私有字段注入（no-op）
// ============================================================

// injectOpenAIPrivateFields 是 OpenAI/GLM 的私有字段注入实现（§5.3 S4）。
// OpenAI/GLM 的 PrivateFields=[]，无任何私有高级字段需要注入。
//
// 注：DeepSeek 的 prompt_cache_key 不在此适配器（OpenAIAdapter）注入，
// 而由 DeepSeekAdapter.InjectPrivateFields 覆写注入。
// buildOpenAIBody 内部的 injectPromptCacheKeyForDeepSeek 仅对 CachingStyle=CachingPromptCacheKey 生效，
// OpenAI 分支下 deepseek 模型仍会注入 prompt_cache_key，但 DeepSeekAdapter 通过覆写 InjectPrivateFields
// 提供独立的注入入口（S5 落地）。
//
// 参数：
// - body: 请求体 map（不修改）
// - req: 统一 Request 结构（不读取）
// - cap: 模型能力描述（不读取）
func (a *OpenAIAdapter) injectOpenAIPrivateFields(body map[string]interface{}, req Request, cap ModelCapability) {
	// no-op：OpenAI/GLM PrivateFields=[]，无私有字段需注入。
	// 保留方法签名以满足 ProviderAdapter interface 契约。
}
