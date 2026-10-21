// Package ai — LLMClient interface + ProviderAdapter interface + NewLLMClient 工厂
//
// 落地：T1-A M04 Provider 多样性方案（候选 A 原生协议适配器）。
// 本文件定义统一的 LLMClient interface（接口组合 Client + 新增 ProtocolStyle/Capability 两方法），
// 定义 ProviderAdapter interface（4 方法：BuildRequest/ParseStream/MapError/InjectPrivateFields），
// 实现 NewLLMClient 工厂函数按 provider 创建对应适配器。
//
// SNAPSHOT: 原 Client interface（Stream/Model/SetModel/Close）保留不变；
// LLMClient 通过接口组合扩展，不破坏现有 mock client（仅实现 Client 4 方法）。
// 需要协议感知的上层（H10/H12）通过 LLMClient interface 引用。
package ai

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/anchorandfish/whale-code/internal/log"
)

// ============================================================
// LLMClient interface（§5.1 / 接口组合扩展）
// ============================================================

// LLMClient 是所有 Provider 适配器必须实现的统一接口（接口组合，向后兼容）。
// 现有 mock client 仅实现 Client（4 方法）不强制实现 LLMClient；
// 需要协议感知的上层（H10/H12）通过 LLMClient interface 引用。
//
// SNAPSHOT: 原 Client interface（Stream/Model/SetModel/Close）保留不变；
// LLMClient 通过接口组合扩展 2 新方法（ProtocolStyle/Capability），不破坏现有 mock client。
type LLMClient interface {
	// Client 内嵌原 4 方法（Stream/Model/SetModel/Close），向后兼容现有 mock client
	Client
	// ProtocolStyle 返回协议风格（anthropic/openai/gemini-native/dashscope-native）。
	// 决定上层如何处理流式事件格式与错误码。
	ProtocolStyle() string
	// Capability 返回当前模型的能力描述（M04 维护，H10/H12 只读）。
	// 调用方据此决定是否注入 thinking/prompt_cache_key 等私有字段。
	Capability() ModelCapability
}

// ProviderAdapter 是各 Provider 适配器的内部接口，封装协议差异（§5.1）。
// 上层不直接调用此接口，仅 LLMClient interface 对外暴露。
// 各适配器必须实现此 4 方法以支持协议适配。
type ProviderAdapter interface {
	// BuildRequest 构造该 Provider 的 HTTP 请求体（含 URL/Header/Body）。
	BuildRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error)
	// ParseStream 解析该 Provider 的流式响应，转成统一 Event 通道。
	// 各 Provider 流式格式差异（SSE/JSON 数组流）在此封装。
	// estimatedInputTokens 为请求前估算的输入 token 数（B-P1 EMA 修复：供 RecordRemoteUsage 校准 EMA offset）。
	ParseStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error)
	// MapError 映射该 Provider 的错误码到统一 *APIError（含 ErrorID）。
	MapError(resp *http.Response) *APIError
	// InjectPrivateFields 注入该 Provider 的私有高级字段（thinking/prompt_cache_key 等）。
	// 不支持时静默跳过（§5.4 降级策略）。
	InjectPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot)
}

// ============================================================
// NewLLMClient 工厂函数（§5.1 / 03-实施prompt.md §2.2）
// ============================================================

// NewLLMClient 按 provider 创建对应适配器（工厂函数， §5.1）。
// 工厂分支：
// - provider="anthropic" → 返回 *AnthropicAdapter
// - provider="openai" → 返回 *OpenAIAdapter（含 GLM）
// - provider="gemini" → 返回 *GeminiNativeAdapter
// - provider="dashscope" → 返回 *DashScopeNativeAdapter
// - provider="deepseek" → 返回 *DeepSeekAdapter（内嵌 OpenAIAdapter）
//
// 向后兼容（§5.4 / 优化 5 吸纳）：
// - provider="anthropic" + model 以 "qwen" 开头 → 自动迁移到 *DashScopeNativeAdapter
// - 打 WARN 日志提示用户更新 provider="dashscope"
//
// 未知 provider 降级为 *OpenAIAdapter（最保守，OpenAI 兼容协议）。
func NewLLMClient(model, apiKey, baseURL, provider string) LLMClient {
	// 向后兼容：qwen 从 Anthropic 分支迁出（§5.4 优化 5）
	if provider == ProviderAnthropic && strings.HasPrefix(strings.ToLower(model), "qwen") {
		log.Warn("qwen model detected under anthropic provider, migrating to dashscope-native",
			"model", model,
			"hint", "update settings to provider=dashscope for native DashScope protocol")
		provider = ProviderDashScope
	}

	switch provider {
	case ProviderAnthropic:
		return NewAnthropicAdapter(model, apiKey, baseURL, provider)
	case ProviderOpenAI:
		return NewOpenAIAdapter(model, apiKey, baseURL, provider)
	case ProviderGemini:
		return NewGeminiNativeAdapter(model, apiKey, baseURL, provider)
	case ProviderDashScope:
		return NewDashScopeNativeAdapter(model, apiKey, baseURL, provider)
	case ProviderDeepSeek:
		return NewDeepSeekAdapter(model, apiKey, baseURL, provider)
	default:
		// 未知 provider 降级为 OpenAIAdapter（最保守，OpenAI 兼容协议）
		log.Warn("unknown provider, falling back to OpenAI adapter",
			"provider", provider, "model", model)
		return NewOpenAIAdapter(model, apiKey, baseURL, ProviderOpenAI)
	}
}

// ============================================================
// adapterBase — 适配器公共基类（§5.3 / 03-实施prompt.md §2.2 步骤 5）
// ============================================================

// adapterBase 是各适配器的公共基类，复用 SimpleClient 的所有公共字段与逻辑。
// 各适配器内嵌 *SimpleClient（向后兼容现有测试与 SimpleClient.Stream() 转发兼容）。
//
// SNAPSHOT: 原 SimpleClient 保留为 AnthropicAdapter + OpenAIAdapter 的公共基类；
// 新增 3 适配器（Gemini/DashScope/DeepSeek）同样内嵌 *SimpleClient 复用 HTTP 连接池等公共字段。
//
// 注：adapterBase 仅为文档聚合概念，实际各 adapter struct 直接内嵌 *SimpleClient。
var _ ProviderAdapter = (*AnthropicAdapter)(nil)

// ============================================================
// AnthropicAdapter stub（S3 中替换为真实实现）
// ============================================================

// AnthropicAdapter 是 Anthropic 原生 Messages API 的协议适配器（§5.1）。
// 内嵌 *SimpleClient 复用公共字段（httpClient/cacheRefRegistry/cacheBuilder 等）。
// S3 中迁移 streamAnthropic / buildAnthropicBody / doStream 逻辑到 BuildRequest/ParseStream。
type AnthropicAdapter struct {
	*SimpleClient
}

// NewAnthropicAdapter 创建 AnthropicAdapter 实例（§5.1 工厂分支）。
// 复用 NewSimpleClient 初始化公共字段，provider 强制为 "anthropic"。
func NewAnthropicAdapter(model, apiKey, baseURL, provider string) *AnthropicAdapter {
	return &AnthropicAdapter{
		SimpleClient: NewSimpleClient(model, apiKey, baseURL, provider),
	}
}

// ProtocolStyle 返回 "anthropic"（§5.1）。
func (a *AnthropicAdapter) ProtocolStyle() string {
	return ProtocolStyleAnthropic
}

// Capability 返回当前模型的能力描述（§5.1）。
func (a *AnthropicAdapter) Capability() ModelCapability {
	return GetModelCapability(a.Model())
}

// BuildRequest 构造 Anthropic Messages API 的 HTTP 请求（S3 中实现真实逻辑）。
func (a *AnthropicAdapter) BuildRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	return a.buildAnthropicRequest(ctx, req, snap)
}

// ParseStream 解析 Anthropic SSE 流（S3 中实现真实逻辑）。
// estimatedInputTokens 透传给 parseAnthropicStream 供 RecordRemoteUsage 校准 EMA offset（B-P1 EMA 修复）。
func (a *AnthropicAdapter) ParseStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	return a.parseAnthropicStream(resp, estimatedInputTokens)
}

// MapError 映射 Anthropic 错误码到 *APIError（S3 中实现真实逻辑）。
func (a *AnthropicAdapter) MapError(resp *http.Response) *APIError {
	return a.mapAnthropicError(resp)
}

// InjectPrivateFields 注入 Anthropic 私有字段 thinking.budget_tokens（S3 中实现真实逻辑）。
func (a *AnthropicAdapter) InjectPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot) {
	a.injectAnthropicPrivateFields(body, req, cap)
}

// Stream 实现Client.Stream()，走 ProviderAdapter 路径（BuildRequest + ParseStream）。
// S3 已落地：通过 streamViaAdapter 调用 buildAnthropicRequest + parseAnthropicStream。
// 原 SimpleClient.Stream 路径仍保留（向后兼容，通过 NewSimpleClient 创建的客户端仍走旧路径）。
func (a *AnthropicAdapter) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return streamViaAdapter(a, a.SimpleClient, ctx, req)
}

// ============================================================
// OpenAIAdapter stub（S4 中替换为真实实现）
// ============================================================

// OpenAIAdapter 是 OpenAI Chat Completions API 的协议适配器（§5.1）。
// GLM 复用此适配器（Provider="openai" + model 以 "glm" 开头）。
type OpenAIAdapter struct {
	*SimpleClient
}

// NewOpenAIAdapter 创建 OpenAIAdapter 实例（§5.1 工厂分支，含 GLM）。
func NewOpenAIAdapter(model, apiKey, baseURL, provider string) *OpenAIAdapter {
	return &OpenAIAdapter{
		SimpleClient: NewSimpleClient(model, apiKey, baseURL, provider),
	}
}

// ProtocolStyle 返回 "openai"（§5.1）。
func (a *OpenAIAdapter) ProtocolStyle() string {
	return ProtocolStyleOpenAI
}

// Capability 返回当前模型的能力描述（§5.1）。
func (a *OpenAIAdapter) Capability() ModelCapability {
	return GetModelCapability(a.Model())
}

// BuildRequest 构造 OpenAI Chat Completions API 的 HTTP 请求（S4 中实现真实逻辑）。
func (a *OpenAIAdapter) BuildRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	return a.buildOpenAIRequest(ctx, req, snap)
}

// ParseStream 解析 OpenAI SSE 流（S4 中实现真实逻辑）。
// estimatedInputTokens 透传给 parseOpenAIStream 供 RecordRemoteUsage 校准 EMA offset（B-P1 EMA 修复）。
func (a *OpenAIAdapter) ParseStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	return a.parseOpenAIStream(resp, estimatedInputTokens)
}

// MapError 映射 OpenAI 错误码到 *APIError（S4 中实现真实逻辑）。
func (a *OpenAIAdapter) MapError(resp *http.Response) *APIError {
	return a.mapOpenAIError(resp)
}

// InjectPrivateFields OpenAI/GLM 无私有字段，空实现（§5.1）。
// 注：DeepSeek 的 prompt_cache_key 不在此适配器，由 DeepSeekAdapter 覆写注入。
func (a *OpenAIAdapter) InjectPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot) {
	a.injectOpenAIPrivateFields(body, req, cap)
}

// Stream 实现Client.Stream()，走 ProviderAdapter 路径（BuildRequest + ParseStream）。
// S4 已落地：通过 streamViaAdapter 调用 buildOpenAIRequest + parseOpenAIStream。
// 原 SimpleClient.Stream 路径仍保留（向后兼容，通过 NewSimpleClient 创建的客户端仍走旧路径）。
func (a *OpenAIAdapter) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return streamViaAdapter(a, a.SimpleClient, ctx, req)
}

// ============================================================
// GeminiNativeAdapter stub（S5 中替换为真实实现）
// ============================================================

// GeminiNativeAdapter 是 Gemini 原生 generateContent API 的协议适配器（§5.1）。
// 流式响应为 JSON 数组流（非 SSE），需单独实现 JSON 流解析器。
type GeminiNativeAdapter struct {
	*SimpleClient
}

// NewGeminiNativeAdapter 创建 GeminiNativeAdapter 实例（§5.1 工厂分支）。
// 默认 baseURL 为 Google Gemini 官方端点。
func NewGeminiNativeAdapter(model, apiKey, baseURL, provider string) *GeminiNativeAdapter {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	return &GeminiNativeAdapter{
		SimpleClient: NewSimpleClient(model, apiKey, baseURL, provider),
	}
}

// ProtocolStyle 返回 "gemini-native"（§5.1）。
func (a *GeminiNativeAdapter) ProtocolStyle() string {
	return ProtocolStyleGeminiNative
}

// Capability 返回当前模型的能力描述（§5.1）。
func (a *GeminiNativeAdapter) Capability() ModelCapability {
	return GetModelCapability(a.Model())
}

// BuildRequest 构造 Gemini generateContent API 的 HTTP 请求（S5 中实现真实逻辑）。
func (a *GeminiNativeAdapter) BuildRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	return a.buildGeminiRequest(ctx, req, snap)
}

// ParseStream 解析 Gemini JSON 数组流（S5 中实现真实逻辑，不复用 SSEParser）。
// estimatedInputTokens 透传给 parseGeminiStream 供 RecordRemoteUsage 校准 EMA offset（B-P1 EMA 修复）。
func (a *GeminiNativeAdapter) ParseStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	return a.parseGeminiStream(resp, estimatedInputTokens)
}

// MapError 映射 Gemini 错误码到 *APIError（S5 中实现真实逻辑）。
func (a *GeminiNativeAdapter) MapError(resp *http.Response) *APIError {
	return a.mapGeminiError(resp)
}

// InjectPrivateFields 注入 Gemini 私有字段 thinkingBudget（S5 中实现真实逻辑）。
func (a *GeminiNativeAdapter) InjectPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot) {
	a.injectGeminiPrivateFields(body, req, cap)
}

// Stream 实现Client.Stream()，走 ProviderAdapter 路径（BuildRequest + ParseStream）。
func (a *GeminiNativeAdapter) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return streamViaAdapter(a, a.SimpleClient, ctx, req)
}

// ============================================================
// DashScopeNativeAdapter stub（S5 中替换为真实实现）
// ============================================================

// DashScopeNativeAdapter 是 DashScope 原生 generation API 的协议适配器（§5.1）。
// qwen 系列从此适配器走原生 DashScope 协议（从 Anthropic 兼容分支迁出）。
type DashScopeNativeAdapter struct {
	*SimpleClient
}

// NewDashScopeNativeAdapter 创建 DashScopeNativeAdapter 实例（§5.1 工厂分支）。
// 默认 baseURL 为阿里云 DashScope 官方端点。
func NewDashScopeNativeAdapter(model, apiKey, baseURL, provider string) *DashScopeNativeAdapter {
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com"
	}
	return &DashScopeNativeAdapter{
		SimpleClient: NewSimpleClient(model, apiKey, baseURL, provider),
	}
}

// ProtocolStyle 返回 "dashscope-native"（§5.1）。
func (a *DashScopeNativeAdapter) ProtocolStyle() string {
	return ProtocolStyleDashScopeNative
}

// Capability 返回当前模型的能力描述（§5.1）。
func (a *DashScopeNativeAdapter) Capability() ModelCapability {
	return GetModelCapability(a.Model())
}

// BuildRequest 构造 DashScope generation API 的 HTTP 请求（S5 中实现真实逻辑）。
func (a *DashScopeNativeAdapter) BuildRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	return a.buildDashScopeRequest(ctx, req, snap)
}

// ParseStream 解析 DashScope SSE 流（S5 中实现真实逻辑，复用 SSEParser）。
// estimatedInputTokens 透传给 parseDashScopeStream 供 RecordRemoteUsage 校准 EMA offset（B-P1 EMA 修复）。
func (a *DashScopeNativeAdapter) ParseStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	return a.parseDashScopeStream(resp, estimatedInputTokens)
}

// MapError 映射 DashScope 错误码到 *APIError（S5 中实现真实逻辑）。
func (a *DashScopeNativeAdapter) MapError(resp *http.Response) *APIError {
	return a.mapDashScopeError(resp)
}

// InjectPrivateFields 注入 DashScope 私有字段 enable_thinking/thinking_budget（S5 中实现真实逻辑）。
func (a *DashScopeNativeAdapter) InjectPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot) {
	a.injectDashScopePrivateFields(body, req, cap)
}

// Stream 实现Client.Stream()，走 ProviderAdapter 路径（BuildRequest + ParseStream）。
func (a *DashScopeNativeAdapter) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return streamViaAdapter(a, a.SimpleClient, ctx, req)
}

// ============================================================
// DeepSeekAdapter stub（S5 中替换为真实实现）
// ============================================================

// DeepSeekAdapter 是 DeepSeek 协议适配器（§5.1 / §6.2 吸纳候选 B 思想）。
// 内嵌 *OpenAIAdapter 复用 BuildRequest/ParseStream/MapError，
// 仅覆写 InjectPrivateFields 注入 prompt_cache_key。
type DeepSeekAdapter struct {
	*OpenAIAdapter
}

// NewDeepSeekAdapter 创建 DeepSeekAdapter 实例（§5.1 工厂分支）。
// 内嵌 *OpenAIAdapter 复用 OpenAI 协议主体逻辑。
func NewDeepSeekAdapter(model, apiKey, baseURL, provider string) *DeepSeekAdapter {
	return &DeepSeekAdapter{
		OpenAIAdapter: NewOpenAIAdapter(model, apiKey, baseURL, provider),
	}
}

// ProtocolStyle 返回 "openai"（DeepSeek 复用 OpenAI 协议， §5.1）。
func (a *DeepSeekAdapter) ProtocolStyle() string {
	return ProtocolStyleOpenAI
}

// Capability 返回当前模型的能力描述（§5.1）。
func (a *DeepSeekAdapter) Capability() ModelCapability {
	return GetModelCapability(a.Model())
}

// InjectPrivateFields 覆写 OpenAIAdapter 空实现，注入 DeepSeek 私有 prompt_cache_key（S5 中实现真实逻辑）。
// 复用 buildPromptCacheKey 逻辑（sha256(userID+sessionID)[:16]）。
func (a *DeepSeekAdapter) InjectPrivateFields(body map[string]interface{}, req Request, cap ModelCapability, snap clientSnapshot) {
	a.injectDeepSeekPrivateFields(body, req, cap, snap)
}

// MapError 覆写 OpenAIAdapter，处理 DeepSeek 429 retry_after 差异（S5 中实现真实逻辑）。
func (a *DeepSeekAdapter) MapError(resp *http.Response) *APIError {
	return a.mapDeepSeekError(resp)
}

// Stream 实现Client.Stream()，通过 streamViaAdapter 走 ProviderAdapter 路径。
// 修复 B1-P1-1：原实现绕过 streamViaAdapter 走旧 SimpleClient.Stream 路径，
// 导致 mapDeepSeekError 从未被调用（错误映射死代码）。
// 现统一走 streamViaAdapter，使 BuildRequest/MapError/ParseStream 均经适配器动态分发。
func (a *DeepSeekAdapter) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return streamViaAdapter(a, a.OpenAIAdapter.SimpleClient, ctx, req)
}

// ============================================================
// streamViaAdapter — 适配器通用 Stream 转发逻辑（§5.3）
// ============================================================

// streamViaAdapter 是各适配器 Stream() 方法的通用转发逻辑：
// 调用 adapter.BuildRequest 构造请求 → 用 client.httpClient 发送 →
// 错误时调 adapter.MapError → 成功时调 adapter.ParseStream。
//
// 此函数供所有 5 个适配器使用，封装 ProviderAdapter 路径的通用控制流。
// 各适配器 Stream() 方法只需一行调用：return streamViaAdapter(a, a.SimpleClient, ctx, req)。
//
// 注：adapter 参数为 ProviderAdapter interface 类型，Go 会动态分发到具体适配器的方法实现。
//
// B-P4 口径修复：EstimatedInputTokens 填充（完整口径 system+tools+messages）。
// 此前 adapter 路径（DeepSeek/Gemini/DashScope 等）完全不填充（恒 0），
// RecordRemoteUsage 因 estimated<=0 跳过 → 这些 provider 的 EMA 校准从未启动。
// 与 SimpleClient.Stream 入口的填充逻辑同源（EstimateRequestInput 原始估算）。
func streamViaAdapter(adapter ProviderAdapter, client *SimpleClient, ctx context.Context, req Request) (<-chan Event, error) {
	snap := client.snapshot()
	// B-P4：adapter 路径统一填充估算值（完整口径，不应用校准防递归）
	if req.EstimatedInputTokens <= 0 {
		client.mu.RLock()
		ts := client.tokenSource
		client.mu.RUnlock()
		if ts != nil {
			req.EstimatedInputTokens = ts.EstimateRequestInput(req)
		}
	}
	httpReq, err := adapter.BuildRequest(ctx, req, snap)
	if err != nil {
		return nil, err
	}
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		apiErr := adapter.MapError(resp)
		if apiErr == nil {
			apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
		}
		return nil, apiErr
	}
	return adapter.ParseStream(resp, req.EstimatedInputTokens)
}

// ============================================================
// AnthropicAdapter 内部方法 — 真实实现见 adapter_anthropic.go（S3）
// ============================================================
// 注：buildAnthropicRequest / parseAnthropicStream / mapAnthropicError /
// injectAnthropicPrivateFields 在 adapter_anthropic.go 中实现。

// ============================================================
// OpenAIAdapter 内部方法 — 真实实现见 adapter_openai.go（S4）
// ============================================================
// 注：buildOpenAIRequest / parseOpenAIStream / mapOpenAIError /
// injectOpenAIPrivateFields 在 adapter_openai.go 中实现。

// ============================================================
// GeminiNativeAdapter 内部方法 — 真实实现见 adapter_gemini.go（S5 落地）
// ============================================================
// 注：buildGeminiRequest / parseGeminiStream / mapGeminiError /
// injectGeminiPrivateFields 在 adapter_gemini.go 中实现。

// ============================================================
// DashScopeNativeAdapter 内部方法 — 真实实现见 adapter_dashscope.go（S5 落地）
// ============================================================
// 注：buildDashScopeRequest / parseDashScopeStream / mapDashScopeError /
// injectDashScopePrivateFields 在 adapter_dashscope.go 中实现。

// ============================================================
// DeepSeekAdapter 内部方法 — 真实实现见 adapter_deepseek.go（S5 落地）
// ============================================================
// 注：injectDeepSeekPrivateFields / mapDeepSeekError 在 adapter_deepseek.go 中实现。

// ============================================================
// 兼容性：clientSnapshot 新增 protocolStyle 字段（§5.2 SNAPSHOT）
// ============================================================

// snapshotWithProtocol 返回带 protocolStyle 的快照（§5.2）。
// 各适配器 Stream 路径使用此方法获取完整快照（含 protocolStyle 字段）。
//
// SNAPSHOT: 原 clientSnapshot 6 字段保留不变（model/provider/baseURL/apiKey/sessionID/userID）；
// 本方法返回扩展快照但不修改原 struct 字段（Go 不支持 struct 字段动态扩展），
// 改为通过 GetModelCapability(snap.model).ProtocolStyle 获取协议风格。
//
// 注：本方法保留供未来扩展，当前 capability.go 的 GetModelCapability 已提供 ProtocolStyle。
func (c *SimpleClient) snapshotWithProtocol() (clientSnapshot, string) {
	snap := c.snapshot()
	protocolStyle := GetModelCapability(snap.model).ProtocolStyle
	return snap, protocolStyle
}

// adapterStreamMu 是适配器 Stream 转发的并发保护锁（避免 SimpleClient.httpClient 并发问题）。
// 当前 SimpleClient.httpClient 自身并发安全，此锁主要为未来扩展保留。
var adapterStreamMu sync.RWMutex
