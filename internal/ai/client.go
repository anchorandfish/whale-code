// Package ai - AI client interface and event definitions
package ai

import "context"

// QuerySource 表示请求来源类型。
//
// C5 冲突解决：零值默认为 QuerySourceForeground（最安全，不 drop）。
// 529 处理逻辑：
// - Foreground：重试，连续 3 次 529 切 fallback
// - Agent：重试（与 Foreground 同），但失败不影响主请求
// - Background：直接 drop（事件总线不再预留 DropEvent，按需新增）
//
// 各调用方必须显式设置 QuerySource（即使与零值相同，也要显式设置以表明意图）：
// - context/ai_compact.go：QuerySourceForeground（压缩是用户可见的关键路径）
// - tools/agent.go：QuerySourceAgent
// - 主请求（TUI 发起）：QuerySourceForeground
// - 后台任务（daemon）：QuerySourceBackground
type QuerySource int

const (
	// QuerySourceForeground 前台请求（用户可见，零值默认）。
	// 529 时重试，连续 3 次切 fallback。
	QuerySourceForeground QuerySource = iota // 0，零值默认
	// QuerySourceBackground 后台请求（daemon 等）。
	// 529 时直接 drop，保护主请求不受后台任务拖累。
	QuerySourceBackground // 1
	// QuerySourceAgent Agent 子请求。
	// 529 时重试（与 Foreground 同），但失败不影响主请求。
	QuerySourceAgent // 2
)

// shouldRetryOn529 判断给定来源的请求在遇到 529 时是否应重试。
// 只有 Background 才 drop，其余均重试。
func shouldRetryOn529(source QuerySource) bool {
	return source != QuerySourceBackground
}

// Request is an AI request
//
// T1-BSNAPSHOT：新增 EstimatedInputTokens 字段（不序列化到 HTTP body）
// 供 stream.go parseSSEEvent 解析到 UsageEvent 时同步调用 RecordRemoteUsage 校准反馈。
//
// ★ 2026-10-04 删除EffortLevel 死字段（json tag 为 effort_level）（A4）：四家厂商都不认这个
// 字段名，且没有任何 buildXxxBody 读过它 ⇒ 它从未离开过进程（唯一"消费"它的是一条
// 断言 JSON 含 effort_level 的测试，也已一并翻转）。保留它会让人误以为「effort 已接线」
// ——那是假的可发现性。正确的做法是按模型能力协商厂商原生参数
// （见 内部工作回执 §2 B 批）。
type Request struct {
	SystemPrompt   []string         `json:"system_prompt"`
	Messages       []MessageEntry   `json:"messages"`
	MaxTokens      int              `json:"max_tokens"`
	Tools          []ToolDefinition `json:"tools,omitempty"`
	Thinking       *ThinkingConfig  `json:"thinking,omitempty"`
	Model          string           `json:"model"`
	ResponseSchema *ResponseSchema  `json:"response_schema,omitempty"`

	// SystemPromptLayers 分层 system prompt（方案 G）。
	// 非空时 buildAnthropicBody 走分层构造（每层独立 cache_control），
	// 为空时降级为 SystemPrompt 的 strings.Join（向后兼容）。
	SystemPromptLayers []PromptComponent `json:"-"`

	// QuerySource 请求来源（C5：零值默认 Foreground，不 drop）。
	QuerySource QuerySource `json:"-"`

	// EstimatedInputTokens 请求前估算的输入 token 数（T1-B SNAPSHOT）
	// 由 TokenSource.PreCheckEstimate 填充，不序列化到 HTTP body（json:"-"），
	// 仅用于 stream.go 解析到 UsageEvent 时同步调用 RecordRemoteUsage 校准反馈。
	// 吸纳优化 2：解决 SimpleClient.Stream 收到 UsageEvent 时不知道请求前估算值的问题。
	EstimatedInputTokens int `json:"-"`

	// BaseURL 覆盖默认 baseURL（/ T1-C SNAPSHOT）。
	// 非空时 SimpleClient.Stream 优先使用此 baseURL（用于 region 切换）。
	// 由 RegionRouter.SelectRegion 注入，对 Provider 适配器透明。
	// json:"-" 因为 baseURL 不应序列化到请求日志（可能含敏感路径）。
	BaseURL string `json:"-"`
}

// MessageEntry API messageformat
type MessageEntry struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// ToolDefinition is the tool definition (API format)
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ThinkingMode 是 thinking 的三态判定 —— **所有协议适配器共用的唯一判据**。
//
// 为什么必须三态：二值 bool 把「显式关闭」与「未指定」压成同一个值（false），
// 于是「关」在类型上不可表达 —— 任何「只在 true 时注入字段」的写法都必然把用户
// 的「关」静默丢掉，服务端于是按自己的默认（qwen3.7 系列默认**开**思考）继续思考，
// 客户端的 `/think off` 形同虚设。
//
// 业界对照（均为本地快照可核）：
// - 同类 CLI：`ReasoningEffort::None` 是枚举的**第一个变体**，线值 "none"
// （对标证据件）；
// - 同类 CLI：thinking 是**判别联合**，`{type:'disabled'}` 是显式构造出来的
// 对象，联合里没有「不传」这个状态
// （对标证据件）。
//
// 两类都把「关」当作一个**可表达的值**，而不是「字段缺席」。
type ThinkingMode int

const (
	// ThinkingUnspecified 未指定：不下发该字段，由服务端默认决定。
	ThinkingUnspecified ThinkingMode = iota
	// ThinkingOn 显式开启（BudgetTokens > 0 时一并下发预算）。
	ThinkingOn
	// ThinkingOff 显式关闭：必须真的把「关」下发到线上。
	ThinkingOff
)

// ThinkingConfig is the thinking mode config.
//
// Enabled 是**三态**：
// - nil → 未指定（不下发该字段，由服务端默认决定）
// - 指向 true → 显式开启
// - 指向 false → 显式关闭
//
// ★ 类型与 config.ThinkingEnabled（*bool）一致，做到 settings → 请求**全程零转换**
// （同语义同类型），避免再次出现「半路被压成二值、于是『关』丢失」这类缺陷。
type ThinkingConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`
	BudgetTokens int   `json:"budget_tokens,omitempty"`
}

// ThinkingPtr 返回 v 的地址，用于构造三态 thinking 设置。
// 不调用时（nil）表示「未指定」。
func ThinkingPtr(v bool) *bool { return &v }

// Resolve 把三态收敛为**唯一判定**。nil 接收者安全（等价于「未指定」）。
//
// ★ 这是「该不该思考」的唯一判定点：四个协议适配器都只允许读它，
// 不允许各自再看 Enabled —— 否则又会退化成「同一语义多处各写各的实现」。
func (t *ThinkingConfig) Resolve() ThinkingMode {
	if t == nil || t.Enabled == nil {
		return ThinkingUnspecified
	}
	if *t.Enabled {
		return ThinkingOn
	}
	return ThinkingOff
}

// Event is a streaming eventinterface
type Event interface{ eventType() string }

// TextDeltaEvent is a text delta
type TextDeltaEvent struct{ Content string }

func (TextDeltaEvent) eventType() string { return "text_delta" }

// ThinkingDeltaEvent is a thinking delta
type ThinkingDeltaEvent struct{ Content string }

func (ThinkingDeltaEvent) eventType() string { return "thinking_delta" }

// ToolUseEvent is a tool use event
type ToolUseEvent struct {
	ID    string
	Name  string
	Input string
}

func (ToolUseEvent) eventType() string { return "tool_use" }

// ToolInputDeltaEvent toolinputincrement（Anthropic input_json_delta）
type ToolInputDeltaEvent struct {
	PartialJSON string
	Index       int
}

func (ToolInputDeltaEvent) eventType() string { return "tool_input_delta" }

// UsageEvent is a partial, cumulative snapshot for one provider request.
// Providers may split fields across events (Anthropic sends input/cache at
// message_start and output at message_delta), but a non-zero field is always
// the cumulative value for that request, never a delta.
type UsageEvent struct {
	InputTokens   int
	OutputTokens  int
	CacheCreation int
	CacheRead     int
}

func (UsageEvent) eventType() string { return "usage" }

// MessageStopEvent is a message stop event
type MessageStopEvent struct {
	// FinishReason 携带 OpenAI 兼容流的 finish_reason（stop/length/content_filter 等）。
	// 空响应排障关键信号：length=输出被截断（如仅 reasoning）、content_filter=内容过滤。
	// Anthropic/DashScope/Gemini 路径暂未透传（已知缺口）。
	FinishReason string
}

func (MessageStopEvent) eventType() string { return "message_stop" }

// ErrorEvent is an error event
type ErrorEvent struct{ Err error }

func (ErrorEvent) eventType() string { return "error" }

// StructuredOutputEvent is a structured output event
type StructuredOutputEvent struct {
	Result *StructuredOutputResult
}

func (StructuredOutputEvent) eventType() string { return "structured_output" }

// ImageContent is the image content block（Anthropic format）
type ImageContent struct {
	Type   string      `json:"type"`
	Source ImageSource `json:"source"`
}

// ImageSource is the image source (Anthropic base64 format)
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ImageURLContent image URL content block（OpenAI format）
type ImageURLContent struct {
	Type     string   `json:"type"`
	ImageURL ImageURL `json:"image_url"`
}

// ImageURL image URL（OpenAI format）
type ImageURL struct {
	URL string `json:"url"`
}

// TextContent is a text content block
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Client is the AI client interface
type Client interface {
	// Stream is a streaming call - returns event channel
	Stream(ctx context.Context, req Request) (<-chan Event, error)
	// Model is the currently used model name
	Model() string
	// SetModel togglemodel
	SetModel(model string)
	// Close closes the client
	Close() error
}

// APIMessage converts internal message format to a provider-specific API message entry
type APIMessage interface {
	ToAPIFormat() MessageEntry
}
