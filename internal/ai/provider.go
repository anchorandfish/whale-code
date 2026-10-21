package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/log"
)

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"

	anthropicBaseURL  = "https://api.anthropic.com"
	anthropicVersion  = "2023-06-01"
	openAIDefaultBase = "https://api.openai.com"
	defaultMaxTokens  = 16384

	// Anthropic extended thinking 的两个硬边界（步骤 4，2026-10-05）：
	// `budget_tokens` 必须 **≥ anthropicThinkingBudgetMin 且 < max_tokens**，否则服务端 400。
	// - anthropicThinkingBudgetMin：官方下界（≥1024）。
	// - anthropicThinkingBudgetFallback：未显式给预算时的兜底（即原 `budget <= 0 ⇒ 10000`）。
	anthropicThinkingBudgetMin      = 1024
	anthropicThinkingBudgetFallback = 10000
	// P0-2（D5-1）修复：将原 300s 全局超时拆分为连接超时 + 首字节超时两层。
	// defaultDialTimeout 控制 TCP 连接建立阶段，defaultResponseHeaderTimeout 控制首字节到达阶段。
	// 流式请求整体时长由调用方 ctx 控制，不再使用 http.Client.Timeout（设为 0）避免长流被截断。
	defaultDialTimeout           = 30 * time.Second
	defaultResponseHeaderTimeout = 120 * time.Second
	defaultTLSHandshakeTimeout   = 30 * time.Second
)

// clientSnapshot holds a consistent snapshot of client fields read under RLock
type clientSnapshot struct {
	model     string
	provider  string
	baseURL   string
	apiKey    string
	sessionID string // 方案 A：会话级稳定 ID，用于 prompt_cache_key 生成
	userID    string // 方案 A（R-1）：用户标识，用于 prompt_cache_key 生成（避免 PII）
}

// snapshot returns a consistent snapshot of mutable client fields under RLock
func (c *SimpleClient) snapshot() clientSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return clientSnapshot{
		model:     c.model,
		provider:  c.provider,
		baseURL:   c.baseURL,
		apiKey:    c.apiKey,
		sessionID: c.sessionID,
		userID:    c.userID,
	}
}

// SimpleClient is a client supporting Anthropic and OpenAI compatible APIs
//
// T1-BSNAPSHOT：新增 tokenSource 字段（依赖注入，可选）
// 注入后 stream.go parseSSEEvent 解析到 UsageEvent 时同步调用 RecordRemoteUsage 校准反馈。
// 为 nil 时跳过校准（向后兼容）。
type SimpleClient struct {
	mu                   sync.RWMutex
	model                string
	apiKey               string
	baseURL              string
	provider             string
	httpClient           *http.Client
	cacheControlProbed   bool
	cacheControlResult   bool
	cacheControlDisabled bool
	// nativeSearchDisabled 是 L1 原生检索的**运行时降级**标记（对齐上面 cacheControlDisabled 的既有手法）。
	// 端点回 400 且错误点名 enable_search ⇒ 置位；此后不再注入，并让本次请求可重试。
	// 进程内**不自动恢复**：端点能力不会因我们重试而改变（与 cache_control 同理）。
	nativeSearchDisabled bool
	sessionID            string                    // 方案 A：会话级稳定 ID，用于 DeepSeek prompt_cache_key 生成
	userID               string                    // 方案 A（R-1）：用户标识，用于 prompt_cache_key 生成（避免 PII）
	cacheRefRegistry     *CacheReferenceRegistry   // 方案 F：tool_use_id → tool_result 位置注册表
	cacheBuilder         *CacheAwareRequestBuilder // 方案 G：分层 system prompt 构造器
	tokenSource          TokenSource               // T1-B：TokenSource 依赖注入，nil 时跳过 RecordRemoteUsage
	// cacheBreakpointPlacement B-P1 cache_control 修复：分层 system block 的 cache_control 混合放置器。
	// buildAnthropicBody 分层路径通过 PlaceBreakpointHybrid 按稳定层/显式标记放置 cache_control，
	// 受 MaxBreakpointsPerRequest 上限保护，动态层不再注入（对齐 Anthropic Prompt Caching 最佳实践）。
	cacheBreakpointPlacement *CacheBreakpointPlacement
}

// NewSimpleClient creates an AI client, auto-selects API format by provider
// Cache-control capability is probed lazily on first request.
//
// P0-2（D5-1）修复：httpClient 不再使用 http.Client.Timeout（原 300s），改用 Transport 层
// 分层超时：DialContext 30s（连接建立）、ResponseHeaderTimeout 120s（首字节到达）、
// TLSHandshakeTimeout 30s。流式请求整体时长由调用方 ctx 控制，避免长流被截断。
// defaultBaseURLForProvider 是「provider 未配 base_url 时用哪个端点」的**唯一**判定。
//
// A2抽出：此前这段 switch 在 `NewSimpleClient` 与 `ModelConfig.FillDefaults`
// 里各写一份（同语义多处实现）。A2 之后端点**参与能力判定**（见 endpoint_capability.go），
// 于是"这个客户端实际会打哪个端点"从显示细节变成了判定输入 ⇒ 必须有唯一权威，
// 否则会出现"显示说官方端点、运行按另一个端点判能力"这类只能靠猜解释的现象。
func defaultBaseURLForProvider(provider string) string {
	if provider == ProviderAnthropic {
		return anthropicBaseURL
	}
	return openAIDefaultBase
}

func NewSimpleClient(model, apiKey, baseURL, provider string) *SimpleClient {
	if baseURL == "" {
		baseURL = defaultBaseURLForProvider(provider)
	}
	return &SimpleClient{
		model:    model,
		apiKey:   apiKey,
		baseURL:  strings.TrimRight(baseURL, "/"),
		provider: provider,
		httpClient: &http.Client{
			// Timeout=0 表示不设整体超时（流式请求由 ctx 控制），避免长流被截断
			Timeout: 0,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   defaultDialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ResponseHeaderTimeout: defaultResponseHeaderTimeout,
				TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
			},
		},
		cacheRefRegistry: NewCacheReferenceRegistry(),   // 方案 F：tool_use_id 注册表
		cacheBuilder:     NewCacheAwareRequestBuilder(), // 方案 G：分层 system prompt 构造器
		// B-P1 cache_control 修复：默认启用 PlaceBreakpointHybrid 混合放置（Enabled=true，MaxBP=4，scope=org）。
		cacheBreakpointPlacement: NewCacheBreakpointPlacement(CacheBreakpointConfig{Enabled: true}),
	}
}

// Model returns the current model name
func (c *SimpleClient) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// SetModel switches model, resets cache capability probe
func (c *SimpleClient) SetModel(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = m
	c.cacheControlProbed = false
	c.cacheControlDisabled = false
	// 换模型 ⇒ 能力位可能不同 ⇒ 降级标记必须一起复位，否则旧模型的失败会永久粘住新模型。
	c.nativeSearchDisabled = false
}

// SetSessionID 设置会话级稳定 ID，用于 DeepSeek prompt_cache_key 生成（方案 A）。
// 同一会话内 key 稳定不变，跨会话 key 不同，实现 cache 隔离。
// 并发安全：使用写锁保护。
func (c *SimpleClient) SetSessionID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = id
}

// SetUserID 设置用户标识，用于 prompt_cache_key 生成（方案 A R-1）。
// userID 参与哈希但不直接出现在 key 中，避免 PII 泄漏。
// 并发安全：使用写锁保护。
func (c *SimpleClient) SetUserID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userID = id
}

// SetTokenSource 注入 TokenSource 实例（依赖注入，T1-B）。
//
// 注入后 stream.go parseSSEEvent 解析到 UsageEvent 时同步调用 RecordRemoteUsage，
// 即使调用方不消费完所有 events 也能完成 EMA offset 校准（吸纳优化 4）。
// 为 nil 时跳过校准（向后兼容，不阻塞主流程）。
//
// §4 仲裁：TokenSource 接口归属 M04，SimpleClient 持有引用。
func (c *SimpleClient) SetTokenSource(ts TokenSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokenSource = ts
}

// TokenSource 返回已注入的 TokenSource 实例（与 SetTokenSource 对应的读取入口）。
// 未注入时返回 nil（调用方需 nil 防御）。供 TUI 层读取 EMA 校准偏移，
// 应用到上下文估算（ApplyCalibration），使 UI 显示与真实 usage 收敛。
func (c *SimpleClient) TokenSource() TokenSource {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tokenSource
}

// SetModelConfig switches full model config (takes effect at runtime, also updates baseURL/apiKey/provider)
// Resets cache capability probe for the new model.
func (c *SimpleClient) SetModelConfig(cfg *ModelConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = cfg.Name
	c.baseURL = strings.TrimRight(cfg.BaseURL, "/")
	c.apiKey = cfg.GetAPIKey()
	c.provider = cfg.Provider
	c.cacheControlProbed = false
	c.cacheControlDisabled = false
	c.nativeSearchDisabled = false
}

// Close closes the client, releases connections
func (c *SimpleClient) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// SupportsCacheControl returns whether the current model supports cache_control cache markers
// Probes lazily on first call. Uses double-check locking to avoid write-lock on read path.
func (c *SimpleClient) SupportsCacheControl() bool {
	c.mu.RLock()
	if c.cacheControlDisabled {
		c.mu.RUnlock()
		return false
	}
	if c.cacheControlProbed {
		result := c.cacheControlResult
		c.mu.RUnlock()
		return result
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cacheControlDisabled {
		return false
	}
	if c.cacheControlProbed {
		return c.cacheControlResult
	}
	probe := getGlobalCapabilityProbe()
	c.cacheControlResult = probe.ProbeCacheControl(c.model)
	c.cacheControlProbed = true
	return c.cacheControlResult
}

// disableCacheControl disables cache_control injection for the current model
// Called when the API returns a cache_control-related error.
// Must be called with the lock held or from a context where concurrent access is safe.
func (c *SimpleClient) disableCacheControlLocked() {
	c.cacheControlDisabled = true
}

// isCacheControlError checks if the error message indicates cache_control is not supported
func isCacheControlError(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	return strings.Contains(lower, "cache_control") &&
		(strings.Contains(lower, "not supported") ||
			strings.Contains(lower, "invalid") ||
			strings.Contains(lower, "unknown") ||
			strings.Contains(lower, "not recognized"))
}

// buildAnthropicURL builds the Anthropic API request URL from snapshot
// Auto-handles the case where baseURL already contains /v1
func buildAnthropicURL(snap clientSnapshot) string {
	base := strings.TrimRight(snap.baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// buildOpenAIURL builds the OpenAI compatible API request URL from snapshot
// Auto-handles the case where baseURL already contains /v1
func buildOpenAIURL(snap clientSnapshot) string {
	base := strings.TrimRight(snap.baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// Stream calls AI API in streaming mode, returns event channel
// Only anthropic uses Anthropic Messages API, all others use OpenAI compatible format
// Takes a snapshot of mutable fields under RLock to avoid data races with SetModel/SetModelConfig.
// / T1-C：req.BaseURL 非空时优先使用（由 RegionRouter 注入，用于 region 切换）。
//
// 性能计时点（路径2-AI 请求）：记录请求发送时刻 reqStart，透传给 doStream/doStreamOpenAI，
// 供 goroutine 内测量 TTFT（首字节到达）与 stream_done（流式完成）。
// 仅 DEBUG 级别输出，默认 Info 级别下被过滤，不影响生产吞吐。
func (c *SimpleClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	reqStart := time.Now()
	snap := c.snapshot()
	if req.BaseURL != "" {
		// 去除末尾 /，与 NewSimpleClient 保持一致
		snap.baseURL = strings.TrimRight(req.BaseURL, "/")
	}
	// B-P4 口径修复：EstimatedInputTokens 必须与 actual 同口径（完整请求输入
	// system+tools+messages）。B-P2 旧修复只估 messages（首轮≈10 token），
	// ratio = 6220/10 ≈ 622 命中脏数据上界 → 校准永不生效（实测恒差 585 不收敛）。
	// EstimateRequestInput 为原始估算（不应用校准，防递归污染样本）。
	if req.EstimatedInputTokens <= 0 {
		c.mu.RLock()
		ts := c.tokenSource
		c.mu.RUnlock()
		if ts != nil {
			req.EstimatedInputTokens = ts.EstimateRequestInput(req)
		}
	}
	log.Debug("[perf] ai: request send",
		"phase", "request_send",
		"model", snap.model,
	)
	switch snap.provider {
	case ProviderAnthropic:
		return c.streamAnthropic(ctx, req, snap, reqStart)
	default:
		return c.streamOpenAI(ctx, req, snap, reqStart)
	}
}

// streamAnthropic calls Anthropic Messages API and parses SSE stream
//
// T1-B：传递 model + EstimatedInputTokens 到 doStream，
// 供 parseSSEEvent 解析到 UsageEvent 时同步调用 RecordRemoteUsage 校准反馈。
//
// 性能计时点：reqStart 由 Stream 入口透传，继续传给 doStream 供 goroutine 测 TTFT/stream_done。
func (c *SimpleClient) streamAnthropic(ctx context.Context, req Request, snap clientSnapshot, reqStart time.Time) (<-chan Event, error) {
	body := c.buildAnthropicBody(req, snap)
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildAnthropicURL(snap), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", snap.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	return c.doStream(httpReq, snap.model, req.EstimatedInputTokens, reqStart)
}

// buildAnthropicBody builds the Anthropic Messages API request body
// When model supports cache_control, auto-injects cache markers at end of system/tools/messages.
// P0-6：统一 cache_control 格式为 {type:"ephemeral", ttl:"5m"/"1h", scope:""}（FACT-AI-002）。
func (c *SimpleClient) buildAnthropicBody(req Request, snap clientSnapshot) map[string]interface{} {
	useCache := c.SupportsCacheControl()
	// P0-6：根据模型缓存能力生成统一的 CacheControl
	var cc CacheControl
	if useCache {
		cap := getGlobalCapabilityProbe().GetCapability(snap.model)
		cc = DefaultCacheControl(cap)
	}
	body := map[string]interface{}{
		"model":      snap.model,
		"max_tokens": defaultMaxTokens,
		"stream":     true,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.SystemPrompt) > 0 {
		systemText := strings.Join(req.SystemPrompt, "\n\n")
		if useCache && !cc.IsEmpty() {
			// 方案 G（B-1 修复）+ B-P1 cache_control 修复：接入 PlaceBreakpointHybrid 混合放置策略。
			// 若 cacheBuilder 已注入且 SystemPromptLayers 非空，走分层 block 构造 +
			// PlaceBreakpointHybrid（仅稳定层/显式 breakpoint 放置 cache_control，受 MaxBreakpointsPerRequest 上限保护）。
			// 旧实现给每个 block（含动态层）都注入 cache_control，浪费 breakpoint 配额且违反 Anthropic 最佳实践；
			// 修复后仅稳定层/显式标记层注入，动态层不注入。cacheBuilder 为 nil 或 Layers 为空时降级为单 block（向后兼容）。
			if c.cacheBuilder != nil && len(req.SystemPromptLayers) > 0 {
				body["system"] = c.buildLayeredSystemBlocksForAPI(req.SystemPromptLayers, snap.model)
			} else {
				body["system"] = []map[string]interface{}{
					{
						"type":          "text",
						"text":          systemText,
						"cache_control": cc.ToMap(),
					},
				}
			}
		} else {
			body["system"] = systemText
		}
	}
	if len(req.Messages) > 0 {
		messages := convertMessagesForAnthropic(req.Messages)
		// 方案 F（T-1 修复）：对 cache prefix 内的 tool_result blocks 注册到 CacheReferenceRegistry。
		// 仅遍历 cacheBoundary 之前的消息（cache prefix），避免注册已不在 cache 范围内的旧 tool_result。
		// 决策（，2026-07-27）：cache_reference 非 Anthropic 公开 API 字段
		// （官方文档 @2026-07-27 无此字段），
		// 同类 CLI 本地快照 @2026-03-31 亦无 cache_reference 实现（对标证据件 仅 714 行，Grep 0 命中）。
		// 快照 @2026-03-31 + 官方文档 @2026-07-27 双源印证且官方文档更新于快照之后，结论时间鲁棒。
		// 现有 cache_control 机制已能缓存 tool_result（官方文档明确 tool_result 在 messages.content 可被缓存），
		// 故不实施 cache_reference API 注入。Registry 保留骨架（Register/Lookup/LRU 基础设施已完备），
		// 真正阻断未来启用的是 API 字段名/值未知，而非代码结构——待官方支持后启用 Lookup 注入。
		// cacheRefRegistry 为 nil 时跳过（向后兼容）。
		if c.cacheRefRegistry != nil {
			c.registerToolResultsForCache(req.Messages)
		}
		if useCache {
			messages = injectMessagesCacheControl(messages, cc)
		}
		body["messages"] = messages
	}
	if req.ResponseSchema != nil {
		structuredTool := map[string]interface{}{
			"name":         "structured_output",
			"description":  req.ResponseSchema.Description,
			"input_schema": req.ResponseSchema.Schema,
		}
		tools := []map[string]interface{}{structuredTool}
		if useCache {
			injectToolsCacheControl(tools, cc)
		}
		body["tools"] = tools
		body["tool_choice"] = map[string]interface{}{
			"type": "tool",
			"name": "structured_output",
		}
	} else if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			})
		}
		if useCache {
			injectToolsCacheControl(tools, cc)
		}
		body["tools"] = tools
	}
	// thinking 三态渲染（唯一判据 ThinkingConfig.Resolve，见 client.go）：
	// On → {type:"enabled", budget_tokens:N}（受下面两条静态护栏约束）；
	// Off → {type:"disabled"} ★「显式关闭」必须能下发；线值对齐 同类 CLI
	// 自己的客户端（对标证据件）；
	// 未指定 → 不下发该字段，由服务端默认决定。
	//
	// ★ 步骤 4· 两条 Anthropic 静态护栏 —— 两者都在**同一个 body 内**
	// 可静态证伪，必须在构造时消解，否则发出去必被服务端 400 拒绝：
	//
	// 3.1 强制 tool_choice × thinking enabled **互斥**。官方：manual extended thinking
	// 只支持 `tool_choice: auto | none`；而结构化输出路径下发的是
	// `tool_choice:{type:"tool"}`（见上）⇒ 同开 = 400。
	// 裁决：结构化输出是调用方的**硬需求**、思考是**偏好** ⇒ **思考降级为 disabled**。
	// 3.2 `budget_tokens` 必须 **≥1024 且 < max_tokens**，否则 400。改前两处各写各的
	// （budget 在此兜底 10000、max_tokens 在上方写死/由 req 覆盖）⇒ 调用方压低
	// `req.MaxTokens` 时会撞车 —— 「默认值碰巧安全 ≠ 结构安全」。
	//
	// 判据：`anthropic_thinking_guard_test.go`（生产构造点 + `Stream` 线上抓包两态）。
	forcedToolChoice := false
	if tc, ok := body["tool_choice"].(map[string]interface{}); ok {
		if t, _ := tc["type"].(string); t == "tool" || t == "any" {
			forcedToolChoice = true
		}
	}
	switch req.Thinking.Resolve() {
	case ThinkingOn:
		if forcedToolChoice {
			// 3.1：不得同开 ⇒ 思考让路（结构化输出优先）
			body["thinking"] = map[string]interface{}{"type": "disabled"}
			break
		}
		budget := req.Thinking.BudgetTokens
		if budget <= 0 {
			budget = anthropicThinkingBudgetFallback
		}
		if budget < anthropicThinkingBudgetMin {
			budget = anthropicThinkingBudgetMin
		}
		if maxTok, ok := body["max_tokens"].(int); ok && maxTok > 0 && budget >= maxTok {
			// 3.2 上界：budget 必须 < max_tokens
			budget = maxTok - 1
			if budget < anthropicThinkingBudgetMin {
				// 无解：无法同时满足「≥1024」与「<max_tokens」⇒ 不启用，
				// 而不是发出一个必然被 400 的 body。
				body["thinking"] = map[string]interface{}{"type": "disabled"}
				break
			}
		}
		body["thinking"] = map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": budget,
		}
	case ThinkingOff:
		body["thinking"] = map[string]interface{}{
			"type": "disabled",
		}
	}
	return body
}

// buildLayeredSystemBlocksForAPI 构造分层 system block 列表并接入 PlaceBreakpointHybrid 混合放置策略（B-P1 cache_control 修复）。
//
// 流程：
// 1. BuildSystemPromptBlocks 产出带 Stable 字段的 []PromptLayerBlock（含层稳定性信息）
// 2. PlaceBreakpointHybrid 按混合策略（显式标记 + 稳定层 + token 阈值 + MaxBreakpointsPerRequest 上限）注入 CacheControl
// 3. 转换为 Anthropic API system block 格式（仅 CacheControl 非空的 block 携带 cache_control 字段）
//
// 与旧实现的差异：旧实现给每个 block 都注入 cache_control（含动态层，浪费 breakpoint 配额）；
// 修复后仅稳定层/显式标记层注入，动态层不注入（对齐 Anthropic Prompt Caching 最佳实践）。
// cacheBreakpointPlacement 为 nil 时降级为不给任何 block 注入 cache_control（向后兼容，由调用方保证非 nil）。
func (c *SimpleClient) buildLayeredSystemBlocksForAPI(layers []PromptComponent, model string) []map[string]interface{} {
	blocks := c.cacheBuilder.BuildSystemPromptBlocks(layers)
	// B-P1 cache_control 修复：接入 PlaceBreakpointHybrid 混合放置策略
	if c.cacheBreakpointPlacement != nil {
		blocks = c.cacheBreakpointPlacement.PlaceBreakpointHybrid(blocks, model)
	}
	result := make([]map[string]interface{}, 0, len(blocks))
	for _, blk := range blocks {
		entry := map[string]interface{}{
			"type": "text",
			"text": blk.Text,
		}
		// 仅当 PlaceBreakpointHybrid 注入了 CacheControl 时携带（动态层不注入）
		if blk.CacheControl != nil {
			entry["cache_control"] = blk.CacheControl
		}
		result = append(result, entry)
	}
	return result
}

// registerToolResultsForCache 遍历消息列表，将 cache prefix 内的 tool_result blocks 注册到 CacheReferenceRegistry（方案 F + T-1）。
// 注册位置（msgIndex, contentIndex）供未来 cache_reference 注入复用。
// 仅注册 user 角色消息中 content 为 []interface{} 且 block.type == "tool_result" 的条目。
// T-1 修复：仅遍历 cacheBoundary 之前的消息（cache prefix），避免注册已不在 cache 范围内的旧 tool_result。
// 副作用：无（LRU evict 由注册表内部管理）。
func (c *SimpleClient) registerToolResultsForCache(msgs []MessageEntry) {
	boundary := computeCacheBoundary(msgs)
	for i := 0; i < boundary; i++ {
		msg := msgs[i]
		if msg.Role != "user" {
			continue
		}
		blocks, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for j, b := range blocks {
			block, ok := b.(map[string]interface{})
			if !ok {
				continue
			}
			if block["type"] != "tool_result" {
				continue
			}
			toolUseID, _ := block["tool_use_id"].(string)
			if toolUseID == "" {
				continue
			}
			c.cacheRefRegistry.Register(toolUseID, i, j)
		}
	}
}

// computeCacheBoundary 计算 cache prefix 边界（T-1）。
// Anthropic cache_control 注入在最后一个 user 消息末尾，cache prefix 即该消息之前的所有消息。
// 此处返回最后一个 user 消息的索引（不含该消息本身），即 cache prefix 范围 [0, boundary)。
// 若无 user 消息，返回 len(msgs)（全量降级，等价于不限边界）。
func computeCacheBoundary(msgs []MessageEntry) int {
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return len(msgs)
	}
	return lastUser
}

// streamOpenAI calls OpenAI compatible Chat Completions API
//
// T1-B：传递 model + EstimatedInputTokens 到 doStreamOpenAI，
// 供 parseSSEEvent 解析到 UsageEvent 时同步调用 RecordRemoteUsage 校准反馈。
//
// 性能计时点：reqStart 由 Stream 入口透传，继续传给 doStreamOpenAI 供 goroutine 测 TTFT/stream_done。
func (c *SimpleClient) streamOpenAI(ctx context.Context, req Request, snap clientSnapshot, reqStart time.Time) (<-chan Event, error) {
	body := c.buildOpenAIBody(req, snap)
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildOpenAIURL(snap), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+snap.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	return c.doStreamOpenAI(httpReq, snap.model, req.EstimatedInputTokens, reqStart)
}

// buildPromptCacheKey 生成 prompt_cache_key（方案 A R-1）。
// 使用 sha256(userID + ":" + sessionID) 前 8 字节（16 hex 字符 = 64 bit 熵），
// 避免直接暴露原始标识符（PII 保护）。
// 参考同类实现 prompt_cache_key_override 机制（whale-code 暂不实施 override）。
func buildPromptCacheKey(userID, sessionID string) string {
	h := sha256.Sum256([]byte(userID + ":" + sessionID))
	return hex.EncodeToString(h[:8]) // 16 字符 = 64 bit 熵
}

// injectPromptCacheKeyForDeepSeek 按 CachingStyle 注入 prompt_cache_key（方案 A）。
// 仅当模型探测结果为 CachingPromptCacheKey 时注入，复用会话级稳定 key。
// key 来源：buildPromptCacheKey(snap.userID, snap.sessionID)。
// 副作用：无；非 DeepSeek 模型（CachingStyle != CachingPromptCacheKey）时跳过。
func (c *SimpleClient) injectPromptCacheKeyForDeepSeek(body map[string]interface{}, snap clientSnapshot) {
	cap := getGlobalCapabilityProbe().GetCapability(snap.model)
	if cap.CachingStyle != CachingPromptCacheKey {
		return
	}
	// 会话级稳定 key：sha256(userID+sessionID)[:16]，同会话同模型 key 不变
	body["prompt_cache_key"] = buildPromptCacheKey(snap.userID, snap.sessionID)
}

// nativeSearchInjectAllowed 报告"此刻该不该注入 enable_search"。
//
// ★ 两条协议（OpenAI 兼容 / DashScope 原生）**必须共用本判定**：
// 注入位置可以不同（顶层 vs parameters），结论必须一致 —— 各判一次就是 那一族事故。
// A2：判定需要**端点**，故签名带 baseURL（由调用方从同一份 snap 取，
// 与 model 同源）。两条协议共用本方法 ⇒ 结论必然一致（注入位置可变、结论必须同源）。
func (c *SimpleClient) nativeSearchInjectAllowed(model, baseURL string) bool {
	c.mu.RLock()
	disabled := c.nativeSearchDisabled
	c.mu.RUnlock()
	if disabled {
		return false
	}
	return NativeSearchEnabledFor(model, baseURL)
}

// injectNativeSearchForCompat 给 OpenAI **兼容**协议注入 L1 原生检索开关。
//
// ★ 位置 = 请求体**顶层** `enable_search`。这不是随手挑的，是探针实测出的**唯一有效位置**：
// `parameters.enable_search` 与 `extra_body.enable_search` 在本端点被**静默丢弃**
// （200 但 prompt_tokens 不变）⇒ 写到那两处等于没接线，**而且不报错**（最难查的一类）。
//
// ⚠️ 与 DashScope **原生**协议的位置不同（那边是 `parameters.enable_search`，见 adapter_dashscope.go）。
// 两者对应官方文档的两套协议，**不要"统一"成一处**。
func (c *SimpleClient) injectNativeSearchForCompat(body map[string]interface{}, snap clientSnapshot) {
	if !c.nativeSearchInjectAllowed(snap.model, snap.baseURL) {
		return
	}
	body["enable_search"] = true
}

// isNativeSearchUnsupportedError 判断错误是否说明"本端点不认原生检索参数"。
//
// 判据刻意**收窄**（误判的代价是平白丢掉一个能力，且不再重试）：
// 必须同时出现 `enable_search` **与**一个明确的"不认识/非法"词。
// 参考实测报文（V4/B1）：
//
//	Input should be a valid boolean, unable to interpret input: parameters.enable_search
func isNativeSearchUnsupportedError(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	if !strings.Contains(lower, "enable_search") {
		return false
	}
	for _, marker := range []string{
		"not supported", "unsupported", "invalid", "unknown",
		"not recognized", "unable to interpret", "unexpected", "extra forbidden",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// disableNativeSearchLocked 置位运行时降级标记。**调用方须已持写锁**。
// 命名对齐 disableCacheControlLocked：`Locked` 后缀表示"调用方已持锁"，不是"本函数加锁"。
func (c *SimpleClient) disableNativeSearchLocked() {
	c.nativeSearchDisabled = true
}

// buildOpenAIBody builds OpenAI Chat Completions API request body
func (c *SimpleClient) buildOpenAIBody(req Request, snap clientSnapshot) map[string]interface{} {
	// 能力判定**只取一次**（A1 门 / 同源）：下发侧 `enable_thinking` 与
	// 回传侧 `preserve_thinking` 必须消费**同一份**结论。取值点只此一处，
	// 下方两处使用点（ResolveThinkingReplay / enable_thinking 三态块）不得各算一份。
	// ★ A2：判据带 `snap.baseURL`（端点维度）—— 仍**只这一行**取值 ⇒
	// 端点降级自动贯通下发侧与回传侧两侧，且两侧仍严格同源。
	capSupported := ThinkingSupportFor(snap.model, snap.baseURL) == ThinkingCapSupported

	// 可选回传面（opt_in）要求的**请求顶层开关**（如 preserve_thinking）。
	// 载体写在 assistant 消息上（见下），而开关是**请求级**的 ⇒ 循环内收集、循环后注入。
	// 字段名**只来自唯一裁决**，此处的两个变量只是它的搬运工。
	var replaySwitchField string
	var replaySwitchOn bool

	messages := make([]map[string]interface{}, 0)
	if len(req.SystemPrompt) > 0 {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": strings.Join(req.SystemPrompt, "\n\n"),
		})
	}
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			if blocks, ok := msg.Content.([]interface{}); ok && len(blocks) > 0 {
				for _, b := range blocks {
					if block, ok := b.(map[string]interface{}); ok {
						toolCallID, _ := block["tool_use_id"].(string)
						content, _ := block["output"].(string)
						if content == "" {
							content, _ = block["content"].(string)
						}
						messages = append(messages, map[string]interface{}{
							"role":         "tool",
							"tool_call_id": toolCallID,
							"content":      content,
						})
					}
				}
			} else {
				messages = append(messages, map[string]interface{}{
					"role":    msg.Role,
					"content": msg.Content,
				})
			}
		} else if msg.Role == "assistant" {
			if blocks, ok := msg.Content.([]interface{}); ok {
				var textParts []string
				var toolCalls []map[string]interface{}
				// thinkingParts 只做**收集**：要不要回传给服务端由
				// ResolveThinkingReplay 统一裁决（唯一判据），此处不判。
				var thinkingParts []string
				for _, b := range blocks {
					block, ok := b.(map[string]interface{})
					if !ok {
						continue
					}
					blockType, _ := block["type"].(string)
					switch blockType {
					case "thinking":
						// 历史思考块（形态与 session.WithThinkingBlock 存储的一致）。
						if th, ok := block["thinking"].(string); ok {
							thinkingParts = append(thinkingParts, th)
						}
					case "text":
						if text, ok := block["text"].(string); ok {
							textParts = append(textParts, text)
						}
					case "tool_use":
						id, _ := block["id"].(string)
						name, _ := block["name"].(string)
						inputRaw, _ := block["input"]
						arguments := ensureValidArguments(inputRaw)
						toolCalls = append(toolCalls, map[string]interface{}{
							"id":   id,
							"type": "function",
							"function": map[string]interface{}{
								"name":      name,
								"arguments": arguments,
							},
						})
					}
				}
				entry := map[string]interface{}{"role": "assistant"}
				if len(textParts) > 0 {
					entry["content"] = strings.Join(textParts, "")
				} else {
					entry["content"] = nil
				}
				if len(toolCalls) > 0 {
					entry["tool_calls"] = toolCalls
				}
				// 历史思考回传 —— **唯一裁决** ResolveThinkingReplay（声明表见
				// thinking_replay.go）。DeepSeek 面（required + with_tools）不回传
				// 即 HTTP 400，是硬伤；百炼面（opt_in + On 档 + 能力确认）写载体
				// 并回收集顶层开关 preserve_thinking（循环后注入 body）。
				//
				// hasTools 必须取"本请求**真的会发出** tools"：结构化输出会跳过
				// tools（见本函数末尾 if/else），此时不构成"带 tools 的请求"。
				//
				// 档位取自 req.Thinking、能力取自本函数开头算好的 capSupported ——
				// 与下方 enable_thinking 的注入（同为 req.Thinking.Resolve() + 同一 capSupported）
				// **同源**：不允许在此另算一份。
				hasTools := req.ResponseSchema == nil && len(req.Tools) > 0
				mode := req.Thinking.Resolve()
				if d := ResolveThinkingReplay(snap.model, hasTools, mode, thinkingParts, capSupported); d.Apply {
					entry[d.Field] = d.Text
					if d.SwitchField != "" {
						replaySwitchField, replaySwitchOn = d.SwitchField, d.SwitchOn
					}
				}
				messages = append(messages, entry)
			} else {
				messages = append(messages, map[string]interface{}{
					"role":    msg.Role,
					"content": msg.Content,
				})
			}
		} else {
			// Handle image content blocks in user messages
			if blocks, ok := msg.Content.([]interface{}); ok {
				converted := convertContentBlocksForOpenAI(blocks)
				messages = append(messages, map[string]interface{}{
					"role":    msg.Role,
					"content": converted,
				})
			} else {
				messages = append(messages, map[string]interface{}{
					"role":    msg.Role,
					"content": msg.Content,
				})
			}
		}
	}

	body := map[string]interface{}{
		"model":      snap.model,
		"messages":   messages,
		"max_tokens": defaultMaxTokens,
		"stream":     true,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	// Structured output: add response_format and skip tools
	if req.ResponseSchema != nil {
		body["response_format"] = map[string]interface{}{
			"type": "json_schema",
			"json_schema": map[string]interface{}{
				"name":        req.ResponseSchema.Name,
				"description": req.ResponseSchema.Description,
				"schema":      req.ResponseSchema.Schema,
				"strict":      req.ResponseSchema.Strict,
			},
		}
	} else if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		body["tools"] = tools
	}
	// thinking 三态渲染（唯一判据 ThinkingConfig.Resolve，见 client.go）：
	// On → enable_thinking=true（+ thinking_budget）；★ 需模型确实支持，见下；
	// Off → enable_thinking=false ★ 这是 `/think off` 真正生效的前提：
	// 服务端默认（qwen3.7 系列）是**开**，"不传" 不等于「关」；
	// 「显式关闭」对任何模型都安全 ⇒ 不做能力门控；
	// 未指定 → 不下发，由服务端默认决定。
	//
	// ★ SupportsThinking 同源（/ A3 2026-10-04；B2 换判据 2026-10-05）：
	// `enable_thinking` 是 DashScope 系私有字段，对不支持 thinking 的模型下发会被
	// 服务端拒绝（adapter_dashscope.go 的注释即为此警告）。此前 DashScope 原生
	// （adapter_dashscope.go:135）与 Gemini（adapter_gemini.go:142）都查能力表，
	// **只有本 OpenAI 兼容路径不查** —— 而生产走的正是这条路径。
	// 三处判据 = ThinkingSupportFor（capability_declaration.go）——**唯一权威入口**：
	// ① 先看用户声明（`/model add --supports-thinking`，B1 声明优先于推断）；
	// ② 再看静态表/前缀推断；③ 把「不知道」单独留一档 ⇒ 不下发。
	// ⚠️ 不要再直接读 `cap.SupportsThinking` —— 那个 bool 把「已知不支持」与
	// 「不知道」压成同一个值，三态信息就是这么丢的（B2 的理由）。
	// 未知 ⇒ fail-safe = 不下发：最坏「不生效」，绝不因误发厂商不认的字段而 400。
	switch req.Thinking.Resolve() {
	case ThinkingOn:
		if capSupported {
			body["enable_thinking"] = true
			if req.Thinking.BudgetTokens > 0 {
				body["thinking_budget"] = req.Thinking.BudgetTokens
			}
		}
	case ThinkingOff:
		body["enable_thinking"] = false
	}
	// 可选回传开关（preserve_thinking）—— 与 enable_thinking 同为 DashScope 系参数，
	// 同注在请求体**顶层**。字段名来自唯一裁决 ResolveThinkingReplay（不得在此硬写）。
	// ★ 只在裁决带开关时注入（当前 = opt_in 面 + On 档 + 能力确认）：
	// 其余档位**不发该字段** —— 显式发 false 会改掉那些服务端默认 true 的模型
	// 本来在做的事（另一种"静默改行为"）。理由见 ResolveThinkingReplay 文档。
	if replaySwitchField != "" {
		body[replaySwitchField] = replaySwitchOn
	}
	// 方案 A：DeepSeek 路径注入 prompt_cache_key（非 DeepSeek 模型自动跳过）
	c.injectPromptCacheKeyForDeepSeek(body, snap)
	// S6 / L1：原生检索（服务端联网搜索）—— 复用用户已配好的那条**模型 Key**，零额外凭据。
	c.injectNativeSearchForCompat(body, snap)
	return body
}

// doStream executes Anthropic SSE streaming request
//
// T1-B：解析到 UsageEvent 时同步调用 RecordRemoteUsage（在 events chan 发送前）。
// 吸纳优化 4：确保即使调用方不消费 events 也能校准 EMA offset。
// model + estimatedInputTokens 从 streamAnthropic 透传，供 RecordRemoteUsage 调用。
//
// 性能计时点（路径2-AI 请求）：reqStart 由 streamAnthropic 透传，
// goroutine 内首次发送 event 时记录 TTFT（首字节到达），
// goroutine 退出前记录 stream_done（流式完成）+ 总耗时。
func (c *SimpleClient) doStream(httpReq *http.Request, model string, estimatedInputTokens int, reqStart time.Time) (<-chan Event, error) {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		apiErr := ParseAPIError(resp)
		if apiErr == nil {
			apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
		}
		// 结构化优先 —— apiErr 已带 StatusCode/ErrorID，
		// 不再用 Message 散文判据抢在结构化分类之前。
		if apiErr.IsContextOverflow() {
			apiErr.ErrorID = ErrAIFatal
		}
		if isCacheControlError(apiErr.Message) {
			c.mu.Lock()
			c.disableCacheControlLocked()
			c.mu.Unlock()
			// 方案 M：cache 探测失败 → 禁用 cache 后可重试，标记 ErrAIRetryable（02 §2.3.2 契约）。
			// retry.go 识别此 ErrorID + isCacheControlError 后重试（下次请求不带 cache_control）。
			apiErr.ErrorID = ErrAIRetryable
		}
		// S6 / L1：端点不认原生检索参数（400）⇒ 只摘掉**这一个能力**并重试，
		// 而不是把整个会话打死。与上面的 cache_control 同款手法（降级粒度 = 单个能力）。
		if isNativeSearchUnsupportedError(apiErr.Message) {
			c.mu.Lock()
			c.disableNativeSearchLocked()
			c.mu.Unlock()
			apiErr.ErrorID = ErrAIRetryable
		}
		return nil, apiErr
	}

	// T1-B：捕获 tokenSource（持 RLock 快照，避免 goroutine 内长时间持锁）
	c.mu.RLock()
	tokenSource := c.tokenSource
	c.mu.RUnlock()

	events := make(chan Event, 64)
	reqCtx := httpReq.Context()
	go func() {
		// Ensure resp.Body is always closed to avoid connection leaks
		defer resp.Body.Close()
		defer close(events)
		// 性能计时点：流式完成（goroutine 退出前记录，含 error 退出路径）。
		defer func() {
			log.Debug("[perf] ai: stream done",
				"phase", "stream_done",
				"model", model,
				"elapsed_ms", time.Since(reqStart).Milliseconds(),
			)
		}()
		firstByteLogged := false
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
			if tokenSource != nil && event != nil {
				if usageEvt, ok := event.(UsageEvent); ok {
					// B-P2 口径修复：Anthropic input_tokens 不含 cache 部分，
					// 完整输入 = input + cache_read + cache_creation（与本地
					// 全量上下文估算同口径），详见 adapter_anthropic.go 同名注释。
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
				// 性能计时点：首字节到达（TTFT），仅首次发送 event 时记录。
				if !firstByteLogged {
					firstByteLogged = true
					log.Debug("[perf] ai: first byte (TTFT)",
						"phase", "ttft",
						"model", model,
						"elapsed_ms", time.Since(reqStart).Milliseconds(),
					)
				}
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

// doStreamOpenAI executes OpenAI SSE streaming request
//
// T1-B：解析到 UsageEvent 时同步调用 RecordRemoteUsage（在 events chan 发送前）。
// 吸纳优化 4：确保即使调用方不消费 events 也能校准 EMA offset。
// model + estimatedInputTokens 从 streamOpenAI 透传，供 RecordRemoteUsage 调用。
//
// 性能计时点（路径2-AI 请求）：reqStart 由 streamOpenAI 透传，
// goroutine 内首次发送 event 时记录 TTFT（首字节到达），
// goroutine 退出前记录 stream_done（流式完成）+ 总耗时。
func (c *SimpleClient) doStreamOpenAI(httpReq *http.Request, model string, estimatedInputTokens int, reqStart time.Time) (<-chan Event, error) {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		apiErr := ParseAPIError(resp)
		if apiErr == nil {
			apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
		}
		// 结构化优先 —— apiErr 已带 StatusCode/ErrorID，
		// 不再用 Message 散文判据抢在结构化分类之前。
		if apiErr.IsContextOverflow() {
			apiErr.ErrorID = ErrAIFatal
		}
		if isCacheControlError(apiErr.Message) {
			c.mu.Lock()
			c.disableCacheControlLocked()
			c.mu.Unlock()
			// 方案 M：cache 探测失败 → 禁用 cache 后可重试，标记 ErrAIRetryable（02 §2.3.2 契约）。
			// retry.go 识别此 ErrorID + isCacheControlError 后重试（下次请求不带 cache_control）。
			apiErr.ErrorID = ErrAIRetryable
		}
		// S6 / L1：端点不认原生检索参数（400）⇒ 只摘掉**这一个能力**并重试，
		// 而不是把整个会话打死。与上面的 cache_control 同款手法（降级粒度 = 单个能力）。
		if isNativeSearchUnsupportedError(apiErr.Message) {
			c.mu.Lock()
			c.disableNativeSearchLocked()
			c.mu.Unlock()
			apiErr.ErrorID = ErrAIRetryable
		}
		return nil, apiErr
	}

	// T1-B：捕获 tokenSource（持 RLock 快照，避免 goroutine 内长时间持锁）
	c.mu.RLock()
	tokenSource := c.tokenSource
	c.mu.RUnlock()

	events := make(chan Event, 64)
	reqCtx := httpReq.Context()
	go func() {
		// Ensure resp.Body is always closed to avoid connection leaks
		defer resp.Body.Close()
		defer close(events)
		// 性能计时点：流式完成（goroutine 退出前记录，含 error/DONE 退出路径）。
		defer func() {
			log.Debug("[perf] ai: stream done",
				"phase", "stream_done",
				"model", model,
				"elapsed_ms", time.Since(reqStart).Milliseconds(),
			)
		}()
		firstByteLogged := false
		// logFirstByte 首次调用时记录 TTFT（首字节到达），后续调用为空操作。
		// 闭包捕获 firstByteLogged 与 reqStart，避免在多个发送点重复判断。
		logFirstByte := func() {
			if !firstByteLogged {
				firstByteLogged = true
				log.Debug("[perf] ai: first byte (TTFT)",
					"phase", "ttft",
					"model", model,
					"elapsed_ms", time.Since(reqStart).Milliseconds(),
				)
			}
		}
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
					logFirstByte()
					select {
					case events <- ToolUseEvent{ID: id, Name: name, Input: args}:
					case <-reqCtx.Done():
						return
					}
				}
				logFirstByte()
				select {
				case events <- MessageStopEvent{FinishReason: lastFinishReason}:
				case <-reqCtx.Done():
					return
				}
				return
			}
			evts, _, chunkFinishReason := parseOpenAIDeltaWithAccum(sse.Data, pendingToolArgs, pendingToolNames, pendingToolIDs)
			// T1-B：UsageEvent 同步调用 RecordRemoteUsage（在 events chan 发送前）
			if chunkFinishReason != "" {
				lastFinishReason = chunkFinishReason
			}
			// 一帧可能产出多个事件（同帧 reasoning + content），逐个按原逻辑处理。
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
					logFirstByte()
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

// parseOpenAIDeltaWithAccum parses OpenAI SSE delta data, accumulates tool_calls incremental arguments
// Returns ([]Event, unused, finishReason); tool_use events are deferred to [DONE] to avoid duplicate emission
// NOTE: The pendingXxx maps are not goroutine-safe — callers must ensure single-goroutine access.
//
// 返回切片而非单个 Event：一帧 delta 允许同时携带多种内容。
//
// 修复（同类隐患，与 kimi-k2.7-code 同帧 bug 同源）：
// 原实现在 content 非空时立即 `return TextDeltaEvent`，同帧的 reasoning_content
// 被静默丢弃——这正是"一帧只含一种内容"假设的残留。部分 OpenAI 兼容层
// （实测阿里云百炼兼容端点）在"思考结束 → 正文开始"的切换边界会把最后一段
// reasoning 与第一段正文合并进同一帧下发，且用空字符串而非省略字段表示"这一路没有内容"。
// 修复前该帧的 reasoning 永久丢失，用户看到思考过程被截断，且不报任何错误。
func parseOpenAIDeltaWithAccum(data string, pendingArgs map[int]string, pendingNames map[int]string, pendingIDs map[int]string) ([]Event, int, string) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, -1, ""
	}

	choices, ok := raw["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		usage, ok := raw["usage"].(map[string]interface{})
		if ok {
			promptTokens, _ := usage["prompt_tokens"].(float64)
			completionTokens, _ := usage["completion_tokens"].(float64)
			promptTokensDetails, _ := usage["prompt_tokens_details"].(map[string]interface{})
			cacheCreation := 0
			cacheRead := 0
			if promptTokensDetails != nil {
				if cc, ok := promptTokensDetails["cached_tokens"].(float64); ok {
					cacheRead = int(cc)
				}
			}
			return []Event{UsageEvent{
				InputTokens:   int(promptTokens),
				OutputTokens:  int(completionTokens),
				CacheCreation: cacheCreation,
				CacheRead:     cacheRead,
			}}, -1, ""
		}
		return nil, -1, ""
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil, -1, ""
	}

	// finish_reason 与 delta 可能同 chunk 出现；调用方取最后一次非空值
	finishReason, _ := choice["finish_reason"].(string)

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return nil, -1, finishReason
	}

	// tool_calls 累积必须放在 text / reasoning 的提前 return 之前。
	//
	// 部分 OpenAI 兼容厂商（实测 kimi-k2.7-code）会在同一帧 delta 里同时下发
	// reasoning_content 与 tool_calls，且 tool_calls 的 id / name 只出现在这一帧，
	// 后续帧仅有 arguments 且 id / name 为空字符串。
	// 修复前本函数在 reasoning_content 非空时立即 return，同帧的 tool_calls 被整个丢弃，
	// 导致 [DONE] flush 出 Name="" 的 ToolUseEvent，执行器找不到工具而丢弃该调用，
	// 最终用户只看到 "AI returned an empty response."（finish_reason 仍是 tool_calls，极具误导性）。
	//
	// 累积是纯副作用，不影响本帧返回哪个展示型事件，因此提前执行是安全的。
	if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
		for _, tcRaw := range toolCalls {
			tc, ok := tcRaw.(map[string]interface{})
			if !ok {
				continue
			}
			idx := 0
			if f, ok := tc["index"].(float64); ok {
				idx = int(f)
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]interface{})
			if fn != nil {
				name, _ := fn["name"].(string)
				args, _ := fn["arguments"].(string)
				pendingArgs[idx] += args
				if name != "" {
					pendingNames[idx] = name
				}
				if id != "" {
					pendingIDs[idx] = id
				}
			} else if args, ok := tc["arguments"].(string); ok {
				pendingArgs[idx] += args
			}
		}
	}

	// 两种展示型内容不再互斥：同帧可能同时携带（兼容层在思考/正文切换边界常见）。
	// thinking 排在 text 之前，与模型实际生成顺序一致。
	var out []Event

	if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
		out = append(out, ThinkingDeltaEvent{Content: reasoning})
	}

	if content, ok := delta["content"].(string); ok && content != "" {
		out = append(out, TextDeltaEvent{Content: content})
	}

	return out, -1, finishReason
}

// convertMessagesForAnthropic converts message list to Anthropic API format
// Handle image content blocks: convert ImageContent type to Anthropic image+base64 format
func convertMessagesForAnthropic(msgs []MessageEntry) []MessageEntry {
	result := make([]MessageEntry, len(msgs))
	for i, msg := range msgs {
		result[i] = msg
		if blocks, ok := msg.Content.([]interface{}); ok {
			converted := make([]interface{}, 0, len(blocks))
			hasImage := false
			strippedThinking := false
			for _, b := range blocks {
				switch v := b.(type) {
				case ImageContent:
					hasImage = true
					converted = append(converted, map[string]interface{}{
						"type": "image",
						"source": map[string]interface{}{
							"type":       v.Source.Type,
							"media_type": v.Source.MediaType,
							"data":       v.Source.Data,
						},
					})
				case TextContent:
					converted = append(converted, map[string]interface{}{
						"type": "text",
						"text": v.Text,
					})
				case map[string]interface{}:
					// Already map format, check if it contains images
					blockType, _ := v["type"].(string)
					// ★ 硬约束面护栏（thinking 回传）：Anthropic 要求回传的 thinking 块
					// 必须带 signature 且不得修改；本仓 ContentBlock 没有签名位，
					// 原样透传一个无签名的 thinking 块 ⇒ 服务端必然 400
					// （messages.N.content.M.thinking.signature: Field required）。
					// 故此处**必须剥离** —— 这是 ResolveThinkingReplay 对 claude 面的
					// 结论（face_forbids）在渲染侧的落地，不是第二处判据。
					if blockType == "thinking" || blockType == "redacted_thinking" {
						strippedThinking = true
						continue
					}
					if blockType == "image" {
						hasImage = true
					}
					converted = append(converted, v)
				default:
					converted = append(converted, v)
				}
			}
			// ★ 剥离也算"需要回写"：原实现只在 hasImage 时回写 Content，
			// 于是"仅含 thinking 块（无图片）"的消息即使被剥离，converted 也被**丢弃不用**
			// ⇒ 护栏静默失效。加上 strippedThinking 才让它真正生效。
			if hasImage || strippedThinking {
				result[i].Content = converted
			}
		}
	}
	return result
}

// injectToolsCacheControl adds cache_control on the last tool definition in the tools array.
// P0-6：签名变更为接受 CacheControl，统一格式为 {type, ttl, scope}。
// 方案 H（T-2 修复）：遍历找最后一个非动态工具索引，cache_control 锚定其上。
// 避免 cache_control 位置随动态工具增删而漂移导致 cache 失效。
// 反例 [core, dyn, dyn]：旧实现回退 1 位锚定 tools[1]（dyn），修复后遍历锚定 tools[0]（core）。
// 全为动态工具时降级为末位（保证总有注入点）。
// 当 cc 为空（IsEmpty）时不注入。
func injectToolsCacheControl(tools []map[string]interface{}, cc CacheControl) {
	if len(tools) == 0 || cc.IsEmpty() {
		return
	}
	// 方案 H（T-2）：遍历找最后一个非动态工具索引
	lastStable := -1
	for i, t := range tools {
		if !isDynamicTool(t) {
			lastStable = i
		}
	}
	if lastStable < 0 {
		// 全为动态工具，降级为末位
		lastStable = len(tools) - 1
	}
	tools[lastStable]["cache_control"] = cc.ToMap()
}

// isDynamicTool 判断 tool 是否为动态注入工具（方案 H）。
// 动态工具特征：name 以 "dynamic_" 前缀（如 dynamic_fetch）或 name == "web_search"。
// 这些工具按需注入，存在/顺序不稳定，cache_control 不应锚定其上。
// T-3 留痕：方案伪代码原定义 advisor_/extra_ 前缀，whale-code 实际无此类工具；
// 改用 dynamic_/web_search 对齐 whale-code 真实动态工具命名（见 internal/tools 注册）。
func isDynamicTool(tool map[string]interface{}) bool {
	name, _ := tool["name"].(string)
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "dynamic_") {
		return true
	}
	return name == "web_search"
}

// injectMessagesCacheControl adds cache_control at the end of the last user message content.
// P0-6：签名变更为接受 CacheControl，统一格式为 {type, ttl, scope}（FACT-AI-002）。
// If the last message is not user, fall back to the first user message from the end.
// If content is string, convert to content blocks array format.
// Returns a new slice without modifying the original messages.
// 当 cc 为空（IsEmpty）时不注入，直接返回拷贝。
func injectMessagesCacheControl(messages []MessageEntry, cc CacheControl) []MessageEntry {
	if len(messages) == 0 {
		return messages
	}

	result := make([]MessageEntry, len(messages))
	copy(result, messages)

	if cc.IsEmpty() {
		return result
	}

	targetIdx := -1
	for i := len(result) - 1; i >= 0; i-- {
		if result[i].Role == "user" {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return result
	}

	msg := &result[targetIdx]
	ccMap := cc.ToMap()

	switch c := msg.Content.(type) {
	case string:
		msg.Content = []map[string]interface{}{
			{
				"type":          "text",
				"text":          c,
				"cache_control": ccMap,
			},
		}
	case []interface{}:
		blocks := make([]map[string]interface{}, len(c))
		hasCache := false
		for i, b := range c {
			bm, ok := b.(map[string]interface{})
			if !ok {
				blocks[i] = map[string]interface{}{}
				continue
			}
			blocks[i] = bm
			if _, ok := bm["cache_control"]; ok {
				hasCache = true
			}
		}
		if !hasCache && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = ccMap
		}
		msg.Content = blocks
	}

	return result
}

// convertContentBlocksForOpenAI converts content block list to OpenAI API format
// Convert ImageContent to OpenAI image_url format (data URI)
func convertContentBlocksForOpenAI(blocks []interface{}) []interface{} {
	converted := make([]interface{}, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.(type) {
		case ImageContent:
			// Build data URI: data:image/png;base64,...
			dataURI := "data:" + v.Source.MediaType + ";base64," + v.Source.Data
			converted = append(converted, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": dataURI,
				},
			})
		case TextContent:
			converted = append(converted, map[string]interface{}{
				"type": "text",
				"text": v.Text,
			})
		case map[string]interface{}:
			blockType, _ := v["type"].(string)
			if blockType == "image" {
				source, _ := v["source"].(map[string]interface{})
				if source != nil {
					mediaType, _ := source["media_type"].(string)
					data, _ := source["data"].(string)
					dataURI := "data:" + mediaType + ";base64," + data
					converted = append(converted, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": dataURI,
						},
					})
				} else {
					converted = append(converted, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": "",
						},
					})
				}
			} else {
				converted = append(converted, v)
			}
		default:
			converted = append(converted, b)
		}
	}
	return converted
}

// getStructuredOutputMethod determines structured output implementation based on current provider
func (c *SimpleClient) getStructuredOutputMethod(schema *ResponseSchema) StructuredOutputMethod {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch c.provider {
	case ProviderOpenAI:
		return MethodOpenAINative
	case ProviderAnthropic:
		return MethodAnthropicToolUse
	default:
		return MethodFallbackPrompt
	}
}

// StreamStructured calls AI API in streaming mode and returns structured output result
// Auto-selects best structured output method by provider (OpenAI native / Anthropic Tool Use / Prompt fallback)
func (c *SimpleClient) StreamStructured(ctx context.Context, req Request) (*StructuredOutputResult, error) {
	if req.ResponseSchema == nil {
		return nil, fmt.Errorf("ResponseSchema cannot be empty")
	}

	method := c.getStructuredOutputMethod(req.ResponseSchema)

	// Prompt fallback mode: inject schema instructions into system prompt
	if method == MethodFallbackPrompt {
		structuredPrompt := buildStructuredOutputPrompt(req.ResponseSchema)
		req.SystemPrompt = append(req.SystemPrompt, structuredPrompt)
	}

	eventCh, err := c.Stream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("streaming request failed: %w", err)
	}

	var fullText string
	var toolInput string

	for event := range eventCh {
		switch e := event.(type) {
		case TextDeltaEvent:
			fullText += e.Content
		case ToolUseEvent:
			if e.Name == "structured_output" {
				if e.Input != "" {
					toolInput = e.Input
				}
			}
		case ToolInputDeltaEvent:
			toolInput += e.PartialJSON
		case ErrorEvent:
			return nil, fmt.Errorf("streaming response error: %w", e.Err)
		}
	}

	switch method {
	case MethodOpenAINative, MethodFallbackPrompt:
		jsonStr, err := extractJSON(fullText)
		if err != nil {
			return nil, fmt.Errorf("JSON extraction failed: %w", err)
		}
		return c.parseStructuredResult(jsonStr, req.ResponseSchema, method)

	case MethodAnthropicToolUse:
		if toolInput == "" {
			jsonStr, err := extractJSON(fullText)
			if err != nil {
				return nil, fmt.Errorf("structured_output tool call not found and cannot extract JSON from text: %w", err)
			}
			// Validate schema in fallback path since model had no schema constraint
			result, parseErr := c.parseStructuredResult(jsonStr, req.ResponseSchema, MethodFallbackPrompt)
			if parseErr != nil && result != nil && len(result.ValidationErrors) > 0 {
				return result, fmt.Errorf("fallback JSON parse/schema validation failed: %s", strings.Join(result.ValidationErrors, "; "))
			}
			return result, parseErr
		}
		return c.parseStructuredResult(toolInput, req.ResponseSchema, method)

	default:
		return nil, fmt.Errorf("unknown structured output method: %s", method)
	}
}

// parseStructuredResult parses structured output JSON string and performs schema validation
func (c *SimpleClient) parseStructuredResult(jsonStr string, schema *ResponseSchema, method StructuredOutputMethod) (*StructuredOutputResult, error) {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return &StructuredOutputResult{
			Data:             nil,
			Raw:              jsonStr,
			Validated:        false,
			ValidationErrors: []string{fmt.Sprintf("JSON parse failed: %v", err)},
			ProviderUsed:     method,
		}, fmt.Errorf("JSON parse failed: %w", err)
	}

	validationErrors := validateAgainstSchema(data, schema.Schema)

	return &StructuredOutputResult{
		Data:             data,
		Raw:              jsonStr,
		Validated:        len(validationErrors) == 0,
		ValidationErrors: validationErrors,
		ProviderUsed:     method,
	}, nil
}

// extractStructuredOutputFromToolUse extracts structured output from tool call events
func extractStructuredOutputFromToolUse(toolName string, toolInput string, fullText string) (string, error) {
	if toolName == "structured_output" && toolInput != "" {
		return toolInput, nil
	}
	// Fallback: extract JSON from text
	return extractJSON(fullText)
}
