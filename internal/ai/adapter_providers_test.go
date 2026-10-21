// Package ai — T1-A M04 Provider 多样性测试（S6.4 / 03-实施prompt.md §6.4）。
//
// 测试覆盖 5 Provider × 3 类用例（happy/降级/边界）：
// - Anthropic：thinking 注入 / 不支持 thinking 时跳过 / 529 错误映射
// - OpenAI：response_format 注入 / GLM 复用 / 429 错误映射
// - Gemini：JSON 流解析 / thinking 降级 / 429 错误映射
// - DashScope：enable_thinking 注入 / qwen 从 Anthropic 迁移 / 限流错误映射
// - DeepSeek：prompt_cache_key 注入 / vLLM 降级 / 429 retry_after 解析
//
// 测试策略：使用 NewLLMClient 工厂创建适配器，直接调用 BuildRequest 检查 body，
// 构造 mock *http.Response 测试 MapError，使用 httptest.Server 测试 ParseStream。
package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ============================================================
// §1 Capability 测试（5 Provider 覆盖）
// ============================================================

// TestCapability_Anthropic 验证 Anthropic 模型能力描述正确。
func TestCapability_Anthropic(t *testing.T) {
	cap := GetModelCapability("claude-sonnet-4-20250514")
	if cap.ProtocolStyle != ProtocolStyleAnthropic {
		t.Errorf("ProtocolStyle = %q, 期望 anthropic", cap.ProtocolStyle)
	}
	if !cap.SupportsThinking {
		t.Error("claude-sonnet-4 应支持 thinking")
	}
	if !cap.HasPrivateField("thinking") {
		t.Error("claude-sonnet-4 应有 thinking 私有字段")
	}
}

// TestCapability_OpenAI 验证 OpenAI 模型能力描述正确。
func TestCapability_OpenAI(t *testing.T) {
	cap := GetModelCapability("gpt-4o")
	if cap.ProtocolStyle != ProtocolStyleOpenAI {
		t.Errorf("ProtocolStyle = %q, 期望 openai", cap.ProtocolStyle)
	}
	// ★ B4-A1：原 `SupportsJSON` 断言随字段一并删除 —— 该字段
	// 零生产读取（整仓无任何非测试使用点），留着只会让人误以为它参与决策。
	if len(cap.PrivateFields) != 0 {
		t.Errorf("gpt-4o PrivateFields 应为空, 实际 %v", cap.PrivateFields)
	}
}

// TestCapability_Gemini 验证 Gemini 模型能力描述正确。
func TestCapability_Gemini(t *testing.T) {
	cap := GetModelCapability("gemini-2.0-flash")
	if cap.ProtocolStyle != ProtocolStyleGeminiNative {
		t.Errorf("ProtocolStyle = %q, 期望 gemini-native", cap.ProtocolStyle)
	}
	if cap.Provider != ProviderGemini {
		t.Errorf("Provider = %q, 期望 gemini", cap.Provider)
	}
}

// TestCapability_DashScope 验证 DashScope qwen 系列模型能力描述正确。
func TestCapability_DashScope(t *testing.T) {
	cap := GetModelCapability("qwen-max")
	if cap.ProtocolStyle != ProtocolStyleDashScopeNative {
		t.Errorf("ProtocolStyle = %q, 期望 dashscope-native", cap.ProtocolStyle)
	}
	if cap.Provider != ProviderDashScope {
		t.Errorf("Provider = %q, 期望 dashscope", cap.Provider)
	}
}

// TestCapability_DeepSeek 验证 DeepSeek 模型能力描述正确。
func TestCapability_DeepSeek(t *testing.T) {
	cap := GetModelCapability("deepseek-chat")
	if cap.ProtocolStyle != ProtocolStyleOpenAI {
		t.Errorf("ProtocolStyle = %q, 期望 openai（DeepSeek 复用 OpenAI 协议）", cap.ProtocolStyle)
	}
	if !cap.SupportsPromptCacheKey {
		t.Error("deepseek-chat 应支持 prompt_cache_key")
	}
	if !cap.HasPrivateField("prompt_cache_key") {
		t.Error("deepseek-chat 应有 prompt_cache_key 私有字段")
	}
}

// TestCapability_GLM 验证 GLM 模型能力描述正确（复用 OpenAI 协议，无私有字段）。
func TestCapability_GLM(t *testing.T) {
	cap := GetModelCapability("glm-4-plus")
	if cap.ProtocolStyle != ProtocolStyleOpenAI {
		t.Errorf("ProtocolStyle = %q, 期望 openai（GLM 复用 OpenAI 协议）", cap.ProtocolStyle)
	}
	if len(cap.PrivateFields) != 0 {
		t.Errorf("glm-4-plus PrivateFields 应为空, 实际 %v", cap.PrivateFields)
	}
}

// TestCapability_UnknownModel 验证未知模型降级为最保守值不 panic。
func TestCapability_UnknownModel(t *testing.T) {
	cap := GetModelCapability("unknown-model-xyz")
	if cap.SupportsThinking {
		t.Error("未知模型 SupportsThinking 应为 false（最保守）")
	}
}

// ============================================================
// §2 工厂函数测试（NewLLMClient 返回正确适配器类型）
// ============================================================

// TestNewLLMClient_Anthropic 验证工厂创建 AnthropicAdapter。
func TestNewLLMClient_Anthropic(t *testing.T) {
	c := NewLLMClient("claude-sonnet-4-5", "sk-ant-test", "", ProviderAnthropic)
	if _, ok := c.(*AnthropicAdapter); !ok {
		t.Errorf("期望 *AnthropicAdapter, 实际 %T", c)
	}
	if c.ProtocolStyle() != ProtocolStyleAnthropic {
		t.Errorf("ProtocolStyle = %q, 期望 anthropic", c.ProtocolStyle())
	}
}

// TestNewLLMClient_OpenAI 验证工厂创建 OpenAIAdapter。
func TestNewLLMClient_OpenAI(t *testing.T) {
	c := NewLLMClient("gpt-4o", "sk-test", "", ProviderOpenAI)
	if _, ok := c.(*OpenAIAdapter); !ok {
		t.Errorf("期望 *OpenAIAdapter, 实际 %T", c)
	}
}

// TestNewLLMClient_Gemini 验证工厂创建 GeminiNativeAdapter。
func TestNewLLMClient_Gemini(t *testing.T) {
	c := NewLLMClient("gemini-2.0-flash", "AIzaSyTest", "", ProviderGemini)
	if _, ok := c.(*GeminiNativeAdapter); !ok {
		t.Errorf("期望 *GeminiNativeAdapter, 实际 %T", c)
	}
}

// TestNewLLMClient_DashScope 验证工厂创建 DashScopeNativeAdapter。
func TestNewLLMClient_DashScope(t *testing.T) {
	c := NewLLMClient("qwen-max", "sk-test", "", ProviderDashScope)
	if _, ok := c.(*DashScopeNativeAdapter); !ok {
		t.Errorf("期望 *DashScopeNativeAdapter, 实际 %T", c)
	}
}

// TestNewLLMClient_DeepSeek 验证工厂创建 DeepSeekAdapter。
func TestNewLLMClient_DeepSeek(t *testing.T) {
	c := NewLLMClient("deepseek-chat", "sk-test", "", ProviderDeepSeek)
	if _, ok := c.(*DeepSeekAdapter); !ok {
		t.Errorf("期望 *DeepSeekAdapter, 实际 %T", c)
	}
}

// TestGeminiStream_RecordsRemoteUsage B-P4 审查修复：Gemini 路径 EMA 校准回流。
//
// 审查发现：parseGeminiStream 签名收了 estimatedInputTokens 但 parseGeminiObject
// 从不调用 tokenSource.RecordRemoteUsage → Gemini 用户的校准链路断裂（永远 no samples）。
// 修复后：解析到 usageMetadata 时同步回流（与 Anthropic/OpenAI/DashScope 适配器对齐）。
//
// 口径注意：Gemini 的 promptTokenCount 已含 cached content（cachedContentTokenCount
// 为其子集，类似 OpenAI cached_tokens），actual 直接用 InputTokens，不并回 CacheRead。
func TestGeminiStream_RecordsRemoteUsage(t *testing.T) {
	adapter := NewLLMClient("gemini-2.0-flash", "AIzaSyTest", "", ProviderGemini)
	gemini := adapter.(*GeminiNativeAdapter)

	ts := NewTokenSource(NewTokenEstimator(), NewUsageTracker(""), false, 30*time.Minute)
	gemini.SetTokenSource(ts)

	// estimated=2000, actual=2200 → ratio=1.1（健康样本区间）
	events := make(chan Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	obj := `{"usageMetadata":{"promptTokenCount":2200,"candidatesTokenCount":50,"cachedContentTokenCount":300}}`
	if err := gemini.parseGeminiObject(obj, events, ctx, "gemini-2.0-flash", 2000); err != nil {
		t.Fatalf("parseGeminiObject error: %v", err)
	}

	// 断言结果（非执行）：CalibrationOffset 已写入且 ratio 健康
	impl := ts.(*tokenSourceImpl)
	impl.offsetsMu.RLock()
	off, ok := impl.offsets["gemini-2.0-flash"]
	impl.offsetsMu.RUnlock()
	if !ok {
		t.Fatal("CalibrationOffset 未写入：Gemini 路径未调用 RecordRemoteUsage，校准链路断裂")
	}
	if off.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", off.SampleCount)
	}
	if off.Ratio < 1.05 || off.Ratio > 1.15 {
		t.Errorf("Ratio = %f, want ≈1.1（2200/2000）", off.Ratio)
	}
}

// TestNewLLMClient_QwenMigrationFromAnthropic 验证 qwen 从 Anthropic 分支自动迁移到 DashScope。
func TestNewLLMClient_QwenMigrationFromAnthropic(t *testing.T) {
	c := NewLLMClient("qwen-max", "sk-test", "", ProviderAnthropic)
	if _, ok := c.(*DashScopeNativeAdapter); !ok {
		t.Errorf("qwen + anthropic 应自动迁移到 *DashScopeNativeAdapter, 实际 %T", c)
	}
	if c.ProtocolStyle() != ProtocolStyleDashScopeNative {
		t.Errorf("ProtocolStyle = %q, 期望 dashscope-native（迁移后）", c.ProtocolStyle())
	}
}

// TestNewLLMClient_UnknownProviderFallback 验证未知 provider 降级为 OpenAIAdapter。
func TestNewLLMClient_UnknownProviderFallback(t *testing.T) {
	c := NewLLMClient("some-model", "sk-test", "", "unknown-provider")
	if _, ok := c.(*OpenAIAdapter); !ok {
		t.Errorf("未知 provider 应降级为 *OpenAIAdapter, 实际 %T", c)
	}
}

// ============================================================
// §3 Anthropic 测试（happy/降级/边界）
// ============================================================

// TestAnthropicAdapter_ThinkingInjection 验证 claude-sonnet-4 的 thinking 字段注入。
func TestAnthropicAdapter_ThinkingInjection(t *testing.T) {
	adapter := NewAnthropicAdapter("claude-sonnet-4-20250514", "sk-ant-test", "https://api.anthropic.com", ProviderAnthropic)
	adapter.SetSessionID("sess-test")
	adapter.SetUserID("user-test")
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
	}
	body := adapter.buildAnthropicBody(req, snap)
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("thinking 字段未注入（claude-sonnet-4 支持 thinking）")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, 期望 enabled", thinking["type"])
	}
	if thinking["budget_tokens"] != 10000 {
		t.Errorf("thinking.budget_tokens = %v, 期望 10000", thinking["budget_tokens"])
	}
}

// TestAnthropicAdapter_InjectPrivateFieldsNoop 验证 Anthropic InjectPrivateFields 为 no-op（thinking 由 buildAnthropicBody 内部注入）。
func TestAnthropicAdapter_InjectPrivateFieldsNoop(t *testing.T) {
	adapter := NewAnthropicAdapter("claude-sonnet-4-20250514", "sk-ant-test", "", ProviderAnthropic)
	cap := GetModelCapability("claude-sonnet-4-20250514")
	body := map[string]interface{}{"model": "claude-sonnet-4-20250514"}
	originalBody := body["model"]
	adapter.InjectPrivateFields(body, Request{}, cap, adapter.SimpleClient.snapshot())
	// no-op 不应修改 body（thinking 已由 buildAnthropicBody 内部注入）
	if body["model"] != originalBody {
		t.Error("InjectPrivateFields 应为 no-op, 不应修改 body")
	}
	if _, has := body["thinking"]; has {
		t.Error("InjectPrivateFields 应为 no-op, 不应注入 thinking（由 buildAnthropicBody 处理）")
	}
}

// TestAnthropicAdapter_MapError529 验证 529 错误映射为 ErrAIApiOverload。
func TestAnthropicAdapter_MapError529(t *testing.T) {
	adapter := NewAnthropicAdapter("claude-sonnet-4-5", "sk-ant-test", "", ProviderAnthropic)
	resp := &http.Response{
		StatusCode: 529,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"overloaded_error","message":"server overloaded"}}`)),
		Header:     make(http.Header),
	}
	apiErr := adapter.mapAnthropicError(resp)
	if apiErr == nil {
		t.Fatal("mapAnthropicError 返回 nil")
	}
	if apiErr.ErrorID != ErrAIApiOverload {
		t.Errorf("ErrorID = %q, 期望 ai_api_overload", apiErr.ErrorID)
	}
}

// ============================================================
// §4 OpenAI 测试（happy/降级/边界）
// ============================================================

// TestOpenAIAdapter_BodyConstruction 验证 OpenAI 请求体构造。
func TestOpenAIAdapter_BodyConstruction(t *testing.T) {
	adapter := NewOpenAIAdapter("gpt-4o", "sk-test", "https://api.openai.com", ProviderOpenAI)
	adapter.SetSessionID("sess-test")
	adapter.SetUserID("user-test")
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		MaxTokens: 4096,
		Messages:  []MessageEntry{{Role: "user", Content: "hello"}},
	}
	body := adapter.buildOpenAIBody(req, snap)
	if body["model"] != "gpt-4o" {
		t.Errorf("model = %v, 期望 gpt-4o", body["model"])
	}
	if body["max_tokens"] != 4096 {
		t.Errorf("max_tokens = %v, 期望 4096", body["max_tokens"])
	}
}

// TestOpenAIAdapter_GLMReuse 验证 GLM 复用 OpenAIAdapter 且无私有字段。
func TestOpenAIAdapter_GLMReuse(t *testing.T) {
	adapter := NewOpenAIAdapter("glm-4-plus", "sk-test", "https://open.bigmodel.cn", ProviderOpenAI)
	cap := adapter.Capability()
	if cap.ProtocolStyle != ProtocolStyleOpenAI {
		t.Errorf("GLM ProtocolStyle = %q, 期望 openai", cap.ProtocolStyle)
	}
	if len(cap.PrivateFields) != 0 {
		t.Errorf("GLM PrivateFields 应为空, 实际 %v", cap.PrivateFields)
	}
}

// TestOpenAIAdapter_MapError429 验证 429 错误映射为 ErrAIApiRateLimit。
func TestOpenAIAdapter_MapError429(t *testing.T) {
	adapter := NewOpenAIAdapter("gpt-4o", "sk-test", "", ProviderOpenAI)
	resp := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`)),
		Header:     make(http.Header),
	}
	apiErr := adapter.mapOpenAIError(resp)
	if apiErr == nil {
		t.Fatal("mapOpenAIError 返回 nil")
	}
	if apiErr.ErrorID != ErrAIApiRateLimit {
		t.Errorf("ErrorID = %q, 期望 ai_api_ratelimit", apiErr.ErrorID)
	}
}

// ============================================================
// §5 Gemini 测试（happy/降级/边界）
// ============================================================

// TestGeminiAdapter_JSONStreamParse 验证 Gemini JSON 数组流解析。
func TestGeminiAdapter_JSONStreamParse(t *testing.T) {
	// 构造 mock Gemini JSON 数组流响应
	jsonStream := `[
		{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}
	]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(jsonStream))
	}))
	defer server.Close()

	adapter := NewGeminiNativeAdapter("gemini-2.0-flash", "AIzaSyTest", server.URL, ProviderGemini)
	events, err := adapter.Stream(context.Background(), Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}

	var hasText, hasUsage, hasStop bool
	for evt := range events {
		switch e := evt.(type) {
		case TextDeltaEvent:
			hasText = true
			if e.Content != "hello" {
				t.Errorf("text = %q, 期望 hello", e.Content)
			}
		case UsageEvent:
			hasUsage = true
			if e.InputTokens != 5 {
				t.Errorf("input_tokens = %d, 期望 5", e.InputTokens)
			}
		case MessageStopEvent:
			hasStop = true
		}
	}
	if !hasText {
		t.Error("未收到 TextDeltaEvent")
	}
	if !hasUsage {
		t.Error("未收到 UsageEvent")
	}
	if !hasStop {
		t.Error("未收到 MessageStopEvent")
	}
}

// TestGeminiAdapter_NoThinkingFor15 验证 gemini-1.5 不支持 thinking 时静默跳过。
func TestGeminiAdapter_NoThinkingFor15(t *testing.T) {
	adapter := NewGeminiNativeAdapter("gemini-1.5-pro", "AIzaSyTest", "https://generativelanguage.googleapis.com", ProviderGemini)
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 10000},
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
	}
	body := adapter.buildGeminiBody(req, snap)
	genConfig, ok := body["generationConfig"].(map[string]interface{})
	if !ok {
		return // 无 generationConfig 也算通过（thinking 被跳过）
	}
	if _, hasThinking := genConfig["thinkingConfig"]; hasThinking {
		cap := GetModelCapability("gemini-1.5-pro")
		if !cap.SupportsThinking {
			t.Error("gemini-1.5-pro 不支持 thinking, thinkingConfig 应被静默跳过")
		}
	}
}

// TestGeminiAdapter_MapError429 验证 Gemini 429 错误映射。
func TestGeminiAdapter_MapError429(t *testing.T) {
	adapter := NewGeminiNativeAdapter("gemini-2.0-flash", "AIzaSyTest", "", ProviderGemini)
	resp := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)),
		Header:     make(http.Header),
	}
	apiErr := adapter.mapGeminiError(resp)
	if apiErr == nil {
		t.Fatal("mapGeminiError 返回 nil")
	}
	if apiErr.ErrorID != ErrAIApiRateLimit {
		t.Errorf("ErrorID = %q, 期望 ai_api_ratelimit", apiErr.ErrorID)
	}
}

// ============================================================
// §6 DashScope 测试（happy/降级/边界）
// ============================================================

// TestDashScopeAdapter_EnableThinkingInjection 验证 qwen3 的 enable_thinking 字段注入。
func TestDashScopeAdapter_EnableThinkingInjection(t *testing.T) {
	adapter := NewDashScopeNativeAdapter("qwen3", "sk-test", "https://dashscope.aliyuncs.com", ProviderDashScope)
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 5000},
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
	}
	body := adapter.buildDashScopeBody(req, snap)
	params, ok := body["parameters"].(map[string]interface{})
	if !ok {
		t.Fatal("parameters 字段缺失")
	}
	if enable, ok := params["enable_thinking"].(bool); !ok || !enable {
		t.Error("qwen3 应注入 enable_thinking=true（SupportsThinking=true）")
	}
	if budget, ok := params["thinking_budget"].(int); !ok || budget != 5000 {
		t.Errorf("thinking_budget = %v, 期望 5000", params["thinking_budget"])
	}
}

// TestDashScopeAdapter_NoThinkingForQwenMax 验证 qwen-max 不支持 thinking 时静默跳过。
func TestDashScopeAdapter_NoThinkingForQwenMax(t *testing.T) {
	adapter := NewDashScopeNativeAdapter("qwen-max", "sk-test", "https://dashscope.aliyuncs.com", ProviderDashScope)
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		Thinking: &ThinkingConfig{Enabled: ThinkingPtr(true), BudgetTokens: 5000},
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
	}
	body := adapter.buildDashScopeBody(req, snap)
	params, ok := body["parameters"].(map[string]interface{})
	if !ok {
		return
	}
	if enable, has := params["enable_thinking"]; has {
		if b, isBool := enable.(bool); isBool && b {
			cap := GetModelCapability("qwen-max")
			if !cap.SupportsThinking {
				t.Error("qwen-max 不支持 thinking, enable_thinking 应被静默跳过")
			}
		}
	}
}

// TestDashScopeAdapter_MapErrorRateQuota 验证 DashScope 限流错误映射。
func TestDashScopeAdapter_MapErrorRateQuota(t *testing.T) {
	adapter := NewDashScopeNativeAdapter("qwen-max", "sk-test", "", ProviderDashScope)
	resp := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(`{"code":"Throttling.RateQuota","message":"rate quota exceeded","request_id":"req-1"}`)),
		Header:     make(http.Header),
	}
	apiErr := adapter.mapDashScopeError(resp)
	if apiErr == nil {
		t.Fatal("mapDashScopeError 返回 nil")
	}
	if apiErr.ErrorID != ErrAIApiRateLimit {
		t.Errorf("ErrorID = %q, 期望 ai_api_ratelimit", apiErr.ErrorID)
	}
	if apiErr.Code != "Throttling.RateQuota" {
		t.Errorf("Code = %q, 期望 Throttling.RateQuota", apiErr.Code)
	}
}

// TestDashScopeAdapter_MapErrorInvalidApiKey 验证 DashScope InvalidApiKey 错误映射。
func TestDashScopeAdapter_MapErrorInvalidApiKey(t *testing.T) {
	adapter := NewDashScopeNativeAdapter("qwen-max", "sk-test", "", ProviderDashScope)
	resp := &http.Response{
		StatusCode: 401,
		Body:       io.NopCloser(strings.NewReader(`{"code":"InvalidApiKey","message":"invalid api key"}`)),
		Header:     make(http.Header),
	}
	apiErr := adapter.mapDashScopeError(resp)
	if apiErr == nil {
		t.Fatal("mapDashScopeError 返回 nil")
	}
	if apiErr.ErrorID != ErrSecretMissing {
		t.Errorf("ErrorID = %q, 期望 secret_missing", apiErr.ErrorID)
	}
}

// ============================================================
// §7 DeepSeek 测试（happy/降级/边界）
// ============================================================

// TestDeepSeekAdapter_PromptCacheKeyInjection 验证 deepseek-chat 的 prompt_cache_key 注入。
func TestDeepSeekAdapter_PromptCacheKeyInjection(t *testing.T) {
	adapter := NewDeepSeekAdapter("deepseek-chat", "sk-test", "https://api.deepseek.com", ProviderDeepSeek)
	adapter.SetSessionID("sess-123")
	adapter.SetUserID("user-456")
	snap := adapter.OpenAIAdapter.SimpleClient.snapshot()
	cap := GetModelCapability("deepseek-chat")
	body := map[string]interface{}{}
	adapter.InjectPrivateFields(body, Request{}, cap, snap)
	key, ok := body["prompt_cache_key"].(string)
	if !ok {
		t.Fatal("deepseek-chat 应注入 prompt_cache_key（SupportsPromptCacheKey=true）")
	}
	if len(key) != 16 {
		t.Errorf("prompt_cache_key 长度 = %d, 期望 16（sha256[:16]）", len(key))
	}
}

// TestDeepSeekAdapter_NoPromptCacheKeyForUnsupported 验证不支持 prompt_cache_key 时静默跳过。
func TestDeepSeekAdapter_NoPromptCacheKeyForUnsupported(t *testing.T) {
	adapter := NewDeepSeekAdapter("deepseek-chat", "sk-test", "https://api.deepseek.com", ProviderDeepSeek)
	adapter.SetSessionID("sess-123")
	adapter.SetUserID("user-456")
	snap := adapter.OpenAIAdapter.SimpleClient.snapshot()
	cap := GetModelCapability("deepseek-chat")
	cap.SupportsPromptCacheKey = false // 模拟 vLLM 自部署
	body := map[string]interface{}{}
	adapter.InjectPrivateFields(body, Request{}, cap, snap)
	if _, has := body["prompt_cache_key"]; has {
		t.Error("SupportsPromptCacheKey=false 时应静默跳过 prompt_cache_key 注入")
	}
}

// TestDeepSeekAdapter_MapError429 验证 DeepSeek 429 错误映射含 retry_after。
func TestDeepSeekAdapter_MapError429(t *testing.T) {
	adapter := NewDeepSeekAdapter("deepseek-chat", "sk-test", "", ProviderDeepSeek)
	header := make(http.Header)
	header.Set("retry-after", "30")
	resp := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`)),
		Header:     header,
	}
	apiErr := adapter.mapDeepSeekError(resp)
	if apiErr == nil {
		t.Fatal("mapDeepSeekError 返回 nil")
	}
	if apiErr.ErrorID != ErrAIApiRateLimit {
		t.Errorf("ErrorID = %q, 期望 ai_api_ratelimit", apiErr.ErrorID)
	}
	if apiErr.RetryAfter == 0 {
		t.Error("429 应解析 retry-after header, RetryAfter=0")
	}
}

// ============================================================
// §8 模型规格表测试（D 批 2026-10-05：定价字段已删，只剩窗口/输出规格）
// ============================================================

// TestSpec_Gemini 验证 gemini-2.0-flash 规格存在且窗口正确。
func TestSpec_Gemini(t *testing.T) {
	s := SpecForModel("gemini-2.0-flash")
	if s == nil {
		t.Fatal("gemini-2.0-flash 规格未找到")
	}
	if s.ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %v, 期望 1048576", s.ContextWindow)
	}
}

// TestSpec_QwenMax 验证 qwen-max 规格存在且窗口正确。
func TestSpec_QwenMax(t *testing.T) {
	s := SpecForModel("qwen-max")
	if s == nil {
		t.Fatal("qwen-max 规格未找到")
	}
	if s.ContextWindow != 32768 {
		t.Errorf("ContextWindow = %v, 期望 32768", s.ContextWindow)
	}
}

// TestSpec_GLM 验证 glm-4-plus 规格存在且窗口正确。
func TestSpec_GLM(t *testing.T) {
	s := SpecForModel("glm-4-plus")
	if s == nil {
		t.Fatal("glm-4-plus 规格未找到")
	}
	if s.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %v, 期望 131072", s.ContextWindow)
	}
}

// ============================================================
// §9 LLMClient interface 契约测试
// ============================================================

// TestLLMClient_InterfaceCompliance 验证 5 个适配器均实现 LLMClient interface。
func TestLLMClient_InterfaceCompliance(t *testing.T) {
	var _ LLMClient = (*AnthropicAdapter)(nil)
	var _ LLMClient = (*OpenAIAdapter)(nil)
	var _ LLMClient = (*GeminiNativeAdapter)(nil)
	var _ LLMClient = (*DashScopeNativeAdapter)(nil)
	var _ LLMClient = (*DeepSeekAdapter)(nil)
}

// TestLLMClient_ProtocolStyleConsistency 验证各适配器 ProtocolStyle 与 Capability 一致。
func TestLLMClient_ProtocolStyleConsistency(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"anthropic", ProviderAnthropic, "claude-sonnet-4-20250514"},
		{"openai", ProviderOpenAI, "gpt-4o"},
		{"gemini", ProviderGemini, "gemini-2.0-flash"},
		{"dashscope", ProviderDashScope, "qwen-max"},
		{"deepseek", ProviderDeepSeek, "deepseek-chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewLLMClient(tc.model, "key", "", tc.provider)
			cap := c.Capability()
			if c.ProtocolStyle() != cap.ProtocolStyle {
				t.Errorf("ProtocolStyle() = %q, Capability().ProtocolStyle = %q（应一致）",
					c.ProtocolStyle(), cap.ProtocolStyle)
			}
		})
	}
}

// ============================================================
// §10 ProviderAdapter interface 契约测试
// ============================================================

// TestProviderAdapter_InterfaceCompliance 验证 5 个适配器均实现 ProviderAdapter interface。
func TestProviderAdapter_InterfaceCompliance(t *testing.T) {
	var _ ProviderAdapter = (*AnthropicAdapter)(nil)
	var _ ProviderAdapter = (*OpenAIAdapter)(nil)
	var _ ProviderAdapter = (*GeminiNativeAdapter)(nil)
	var _ ProviderAdapter = (*DashScopeNativeAdapter)(nil)
	// DeepSeekAdapter 内嵌 *OpenAIAdapter，复用 BuildRequest/ParseStream/InjectPrivateFields
	// 但覆写 MapError，需单独验证
	adapter := NewDeepSeekAdapter("deepseek-chat", "sk-test", "", ProviderDeepSeek)
	var _ ProviderAdapter = adapter
}

// ============================================================
// §11 JSON 辅助验证测试
// ============================================================

// TestGeminiBody_SystemPromptInjection 验证 Gemini systemInstruction 注入。
func TestGeminiBody_SystemPromptInjection(t *testing.T) {
	adapter := NewGeminiNativeAdapter("gemini-2.0-flash", "AIzaSyTest", "https://generativelanguage.googleapis.com", ProviderGemini)
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		SystemPrompt: []string{"You are a helpful assistant."},
		Messages:     []MessageEntry{{Role: "user", Content: "hi"}},
	}
	body := adapter.buildGeminiBody(req, snap)
	sysInstr, ok := body["systemInstruction"].(map[string]interface{})
	if !ok {
		t.Fatal("systemInstruction 字段缺失")
	}
	parts, ok := sysInstr["parts"].([]map[string]interface{})
	if !ok || len(parts) == 0 {
		t.Fatal("systemInstruction.parts 缺失或为空")
	}
	if text, ok := parts[0]["text"].(string); !ok || text != "You are a helpful assistant." {
		t.Errorf("system text = %v, 期望 'You are a helpful assistant.'", parts[0]["text"])
	}
}

// TestDashScopeBody_ToolInjection 验证 DashScope tools 字段注入。
func TestDashScopeBody_ToolInjection(t *testing.T) {
	adapter := NewDashScopeNativeAdapter("qwen-max", "sk-test", "https://dashscope.aliyuncs.com", ProviderDashScope)
	snap := adapter.SimpleClient.snapshot()
	req := Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
		Tools: []ToolDefinition{
			{Name: "get_weather", Description: "Get weather", InputSchema: map[string]interface{}{"type": "object"}},
		},
	}
	body := adapter.buildDashScopeBody(req, snap)
	params, ok := body["parameters"].(map[string]interface{})
	if !ok {
		t.Fatal("parameters 字段缺失")
	}
	tools, ok := params["tools"].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools 应有 1 个工具, 实际 %v", params["tools"])
	}
	fn, ok := tools[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatal("function 字段缺失")
	}
	if fn["name"] != "get_weather" {
		t.Errorf("tool name = %v, 期望 get_weather", fn["name"])
	}
}

// TestDeepSeekAdapter_PromptCacheKeyJSON 验证 prompt_cache_key 可被 JSON 序列化。
func TestDeepSeekAdapter_PromptCacheKeyJSON(t *testing.T) {
	adapter := NewDeepSeekAdapter("deepseek-chat", "sk-test", "https://api.deepseek.com", ProviderDeepSeek)
	adapter.SetSessionID("sess-json")
	adapter.SetUserID("user-json")
	snap := adapter.OpenAIAdapter.SimpleClient.snapshot()
	cap := GetModelCapability("deepseek-chat")
	body := map[string]interface{}{
		"model":    "deepseek-chat",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}
	adapter.InjectPrivateFields(body, Request{}, cap, snap)
	// 验证可被 JSON 序列化（prompt_cache_key 是 string，应无问题）
	if _, err := json.Marshal(body); err != nil {
		t.Errorf("body JSON 序列化失败: %v", err)
	}
}
