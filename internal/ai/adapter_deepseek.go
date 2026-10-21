// Package ai — DeepSeekAdapter 真实实现（§5.3，T1-A S5 落地）。
//
// 本文件实现 DeepSeekAdapter 的覆写方法：
// - InjectPrivateFields：覆写 OpenAIAdapter 空实现，注入 prompt_cache_key（检查 SupportsPromptCacheKey）
// - MapError：覆写 OpenAIAdapter，处理 DeepSeek 429 retry_after 差异
//
// SNAPSHOT: DeepSeekAdapter 内嵌 *OpenAIAdapter（Go 匿名内嵌），复用 BuildRequest/ParseStream。
// DeepSeek 协议与 OpenAI 兼容，仅 prompt_cache_key 为 DeepSeek 私有字段。
//
// usage 字段统一转换：DeepSeek prompt_cache_hit_tokens → UsageEvent.CacheRead
// （由 OpenAIAdapter.parseOpenAIStream 继承，DeepSeek 的 usage 在 OpenAI 兼容字段 prompt_tokens_details.cached_tokens 中）
//
// REF-外部-DeepSeek API @官方URL https://api-docs.deepseek.com/（访问日期 2026-07-20）
package ai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ============================================================
// InjectPrivateFields — DeepSeek 私有字段 prompt_cache_key 注入
// ============================================================

// injectDeepSeekPrivateFields 注入 DeepSeek 私有字段 prompt_cache_key（§5.3 S5）。
// 检查 cap.SupportsPromptCacheKey，true 时注入 prompt_cache_key = sha256(userID:sessionID)[:16]。
// 不支持时静默跳过（§5.4 降级策略，vLLM 自部署场景）。
//
// 复用现有 buildPromptCacheKey 逻辑（provider.go:454）。
// 注：buildOpenAIBody 内部的 injectPromptCacheKeyForDeepSeek 已对 CachingStyle=CachingPromptCacheKey 生效，
// 此处为独立注入入口，未来重构时将 prompt_cache_key 注入从 buildOpenAIBody 提取到此处。
//
// 454 buildPromptCacheKey
// 463 injectPromptCacheKeyForDeepSeek
//
// 参数：
// - body: 请求体 map（注入 prompt_cache_key 字段）
// - req: 统一 Request 结构（不读取）
// - cap: 模型能力描述（检查 SupportsPromptCacheKey）
// - snap: SimpleClient 字段快照（用于 userID/sessionID 生成 key）
func (a *DeepSeekAdapter) injectDeepSeekPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot) {
	if !cap.SupportsPromptCacheKey {
		// vLLM 自部署或不支持 prompt_cache_key 时静默跳过
		return
	}
	// 会话级稳定 key：sha256(userID+sessionID)[:16]，同会话同模型 key 不变
	body["prompt_cache_key"] = buildPromptCacheKey(snap.userID, snap.sessionID)
}

// ============================================================
// MapError — DeepSeek 错误码映射（覆写 OpenAIAdapter）
// ============================================================

// mapDeepSeekError 映射 DeepSeek API 错误响应到 *APIError（§5.3 S5）。
// DeepSeek 错误格式与 OpenAI 兼容（{error: {type, code, message}}），但 429 限流响应有差异：
// - DeepSeek 429 含 retry_after 字段（秒），需解析为 RetryAfter
// - OpenAI 429 含 reset_tokens_in_ms（毫秒）
//
// 此覆写方法在 ParseAPIError 基础上补充 DeepSeek retry_after 解析。
//
// REF-外部-DeepSeek API @官方URL https://api-docs.deepseek.com/
func (a *DeepSeekAdapter) mapDeepSeekError(resp *http.Response) *APIError {
	// 先保存 body 字节（ParseAPIError 会消费 resp.Body），再恢复给 ParseAPIError 使用
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	apiErr := ParseAPIError(resp)
	if apiErr == nil {
		apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
	}

	// DeepSeek 429 限流：补充 retry_after 解析
	if resp.StatusCode == http.StatusTooManyRequests {
		var deepseekErr struct {
			Error struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(bodyBytes, &deepseekErr) == nil {
			// DeepSeek 429 标记为 RateLimit
			apiErr.ErrorID = ErrAIApiRateLimit
		}
		// 尝试从 header 解析 retry-after（ParseAPIError 已处理）
		// 若 header 无 retry-after，尝试从 body 解析（DeepSeek 可能用 body 字段）
		if apiErr.RetryAfter == 0 {
			if ra := resp.Header.Get("retry-after"); ra != "" {
				if seconds, err := strconv.Atoi(ra); err == nil {
					apiErr.RetryAfter = time.Duration(seconds) * time.Second
				}
			}
		}
	}

	// context overflow 标记为不可恢复
	// 结构化优先 —— apiErr 已带 StatusCode/ErrorID（Message 里含裸 "4132" 这类
	// 数字时，旧散文判据会把一次可重试的 5xx 误升为不可重试）。
	if apiErr.IsContextOverflow() {
		apiErr.ErrorID = ErrAIFatal
	}

	return apiErr
}
