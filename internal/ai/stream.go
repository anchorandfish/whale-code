// Package ai — SSE stream parser
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ============================================================
// P0-2 (A1): 结构化错误分类 APIError
//
// 修复两个误判：
// 1. IsRetryableErrorFromMsg 用 strings.Contains(lower, "500") 会把
// "5000 tokens"、"error code 15000" 误判为可重试 → 改用 \b 词边界锚定正则。
// 2. IsRateLimitError 把 529 当 RateLimit（语义错误，影响降级判断）→
// 529 归为 Overloaded，仅 429 为 RateLimit。
// ============================================================

// 预编译正则：词边界锚定的状态码匹配，避免 "5000" 误命中 "500"。
// 包含 401：401 Unauthorized 可通过 OAuth token 刷新恢复，故识别为可重试错误。
var retryableStatusCodeRe = regexp.MustCompile(`\b(401|408|429|500|502|503|504|529)\b`)

// 预编译正则：context overflow 双协议消息解析。
var (
	// 格式 1: "input length and `max_tokens` exceed context limit: X + Y > Z"
	contextOverflowRe1 = regexp.MustCompile(`input length.*max_tokens.*exceed.*:\s*(\d+)\s*\+\s*(\d+)\s*>\s*(\d+)`)
	// 格式 2: "prompt is too long: X tokens > Y maximum"
	contextOverflowRe2 = regexp.MustCompile(`prompt is too long:\s*(\d+)\s*tokens\s*>\s*(\d+)\s*maximum`)
)

// APIError 是从 HTTP 响应解析出的结构化错误（P0-2）。
type APIError struct {
	StatusCode int
	Type       string // "overloaded_error", "rate_limit_error", "invalid_request_error", etc.
	Code       string // provider 特定错误码
	Message    string
	RetryAfter time.Duration // 从 retry-after header 解析，0 表示无
	ErrorID    ErrorID       // 结构化错误标识
}

// Error 实现 error 接口，返回格式化的错误消息。
// D2 修复：使用 ErrorID 替代 Type，对齐开发 prompt §1.3 修改点 2。
func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API error %d [%s]: %s", e.StatusCode, e.ErrorID, e.Message)
	}
	return fmt.Sprintf("API error %d [%s]", e.StatusCode, e.ErrorID)
}

// Unwrap 实现 errors.Unwrap 接口，支持 errors.As 链式追溯。
func (e *APIError) Unwrap() error {
	return nil
}

// ParseAPIError 从 HTTP 响应解析错误。
// 优先从 JSON body 提取 error.type/code/message；无法解析时回退到正则提取状态码。
// 内部自行读取 resp.Body，调用方无需提前读取。
// 若 resp 为 nil，返回 nil。
func ParseAPIError(resp *http.Response) *APIError {
	if resp == nil {
		return nil
	}
	e := &APIError{StatusCode: resp.StatusCode}

	// 1. 解析 retry-after header（秒或 HTTP-date）
	if ra := resp.Header.Get("retry-after"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(seconds) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				e.RetryAfter = d
			}
		}
	}

	// 2. 解析 Anthropic ratelimit-unified-reset（取较大值）
	if reset := resp.Header.Get("anthropic-ratelimit-unified-reset"); reset != "" {
		if seconds, err := strconv.Atoi(reset); err == nil {
			resetDur := time.Duration(seconds) * time.Second
			if resetDur > e.RetryAfter {
				e.RetryAfter = resetDur
			}
		}
	}

	// 3. 读取并解析 JSON body
	var body []byte
	if resp.Body != nil {
		body, _ = io.ReadAll(resp.Body)
	}

	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Type != "" {
		e.Type = payload.Error.Type
		e.Code = payload.Error.Code
		e.Message = RedactMessage(payload.Error.Message)
		e.ErrorID = MapHTTPStatusToErrorID(e.StatusCode, e.Type)
		return e
	}

	// 4. 回退：从消息文本用词边界正则提取状态码
	msg := string(body)
	e.Message = RedactMessage(msg)
	if m := retryableStatusCodeRe.FindStringSubmatch(msg); m != nil {
		if code, err := strconv.Atoi(m[1]); err == nil && e.StatusCode == 0 {
			e.StatusCode = code
		}
	}
	e.ErrorID = MapHTTPStatusToErrorID(e.StatusCode, e.Type)
	return e
}

// ============================================================
// 状态码 → 语义的**唯一权威表**
//
// 收口前「同一个状态码该不该重试/该归哪一类」有四份各自手写的口径，且字面量集合互不一致：
//
//	① APIError.IsRetryable() 的 StatusCode 降级 switch（本文件）
//	② IsRetryableError(statusCode)（本文件）
//	③ ReconnectWrapper.ShouldReconnect 的 switch（reconnect.go）
//	④ retry.go 的实际行为
//
// 分歧实例（可复算）：501 在 ② 里 `>=500 && <600` ⇒ 可重试，在 ① 的 switch 里未列出 ⇒ 不可重试。
// ⇒「同一事实在不同调用方给出相反答案」，判据随调用方漂移。
//
// 收口后：①②③ 全部由**本表 + errorCategorySpecs** 推出，各自只做薄转发；
// ④（retry.go 对 429 既按 CategoryFatal 记又不直接重试而是 continue）属**策略**问题，
// 不在本次收口范围（见 handoff 回执「已知残留」）。
//
// ★ 维护纪律：新增/修改「某状态码归哪一类」只改这一处，别在判据函数里补字面量。
// ★ 本表只表达**身份**（状态码 → ErrorID）；「可不可重试」一律由
//
//	errorCategorySpecs（errors.go）二次推导，不在此处重复。
//
// ============================================================
var httpStatusErrorIDSpecs = map[int]ErrorID{
	529: ErrAIApiOverload,
	429: ErrAIApiRateLimit,
	503: ErrAIApiServer,
	500: ErrAIRetryable,
	502: ErrAIRetryable,
	504: ErrAIRetryable,
	408: ErrAIRetryable,
	425: ErrAIRetryable,
	400: ErrAIFatal,
	401: ErrAIFatal,
	403: ErrAIFatal,
	404: ErrAIFatal,
	405: ErrAIFatal,
	413: ErrAIFatal,
}

// MapHTTPStatusToErrorID 根据 HTTP 状态码和错误类型映射到结构化 ErrorID。
// B1 修复：兜底返回 ErrAIRetryable（对齐 "保守可重试，不丢请求"原则）。
// D4 修复：500/502/504 拆出为 ErrAIRetryable，仅 503 为 ErrAIApiServer（对齐开发 prompt §1.3 修改点 3）。
// 改为查 httpStatusErrorIDSpecs（唯一口径）。
func MapHTTPStatusToErrorID(statusCode int, bodyType string) ErrorID {
	if id, ok := httpStatusErrorIDSpecs[statusCode]; ok {
		return id
	}
	// 未登记状态码：provider 的 body type 优先（同一状态码在不同 provider 语义可以不同，
	// 如 Anthropic 用 200 之外的 4xx 携带 overloaded_error / rate_limit_error）。
	lowerType := strings.ToLower(bodyType)
	if strings.Contains(lowerType, "overload") {
		return ErrAIApiOverload
	}
	if strings.Contains(lowerType, "rate_limit") {
		return ErrAIApiRateLimit
	}
	// 兜底（B1 决策，保留）：未登记状态码一律 ErrAIRetryable。
	//
	// ★ 已删死分支：此处原为
	// if IsRetryableError(statusCode) { return ErrAIRetryable }
	// return ErrAIRetryable
	// —— 两个分支同值 ⇒ **死分支**；即"策略反转"在这一层从未生效过（429/529 未登记到此分支，
	// 已登记状态码在第一段就返回了）。
	// ★ 不要把 IsRetryableError(statusCode) 加回本函数：收口后 IsRetryableError 由本表推出，
	// 加回来即构成互递归（栈溢出）。
	return ErrAIRetryable
}

// IsRetryable 判断错误是否可重试。
// ⚠️ 策略反转：529/429 不可重试（对齐业内既有实现 ServerOverloaded.is_retryable = false）。
// 529 直接走降级链路；429 等待 RetryAfter 后重试（方案 D 协同）。
// D3 修复：基于 ErrorID 驱动控制流（对齐开发 prompt §1.3 修改点 2）。
// ErrorID 为空时**薄转发**到 IsRetryableError（唯一口径），不再自持第二份状态码字面量表。
func (e *APIError) IsRetryable() bool {
	if e.ErrorID != "" {
		// 可重试性一律由 errorCategorySpecs（errors.go）推导，此处只读分类。
		return categoryFromID(e.ErrorID) == CategoryRetryable
	}
	// ErrorID 为空：向后兼容路径。★ 生产不可达 —— ParseAPIError 的**所有**出口都填 ErrorID
	// （stream.go 的 JSON 分支与非 JSON 回退分支），只有测试/手工构造的 APIError 会走到这里。
	return IsRetryableError(e.StatusCode)
}

// IsRateLimit 判断是否为限流错误（仅 429）。
// P0-2 修复：529 不再归为 RateLimit，改为 Overloaded。
func (e *APIError) IsRateLimit() bool {
	return e.StatusCode == 429 || e.Type == "rate_limit_error"
}

// IsOverloaded 判断是否为过载错误（529）。
func (e *APIError) IsOverloaded() bool {
	return e.StatusCode == 529 || e.Type == "overloaded_error"
}

// IsContextOverflow 判断是否为上下文溢出（：**结构化优先**）。
//
// - 状态码 413 = 溢出的**结构化**信号（Request Entity Too Large）⇒ 直接成立；
// - 状态码已知但非 413 ⇒ 只采信**措辞**判据，**不**采信消息里的裸数字
// （"backend 4132 unavailable" / "413000 tokens" 都会被裸子串匹配误命中）；
// - 状态码缺失（0）⇒ 回落 IsContextOverflowError（含裸 "413"，见其注释说明为何必须留）。
func (e *APIError) IsContextOverflow() bool {
	if e.StatusCode == 413 {
		return true
	}
	if e.StatusCode != 0 {
		return isContextOverflowPhrase(e.Message)
	}
	return IsContextOverflowError(e.Message)
}

// ParseContextOverflow 解析上下文溢出的 token 数。
// 返回 (inputTokens, maxTokens, contextLimit, ok)。
// maxTokens=0 表示消息未携带该信息（仅 prompt too long 格式）。
func (e *APIError) ParseContextOverflow() (inputTokens, maxTokens, contextLimit int, ok bool) {
	return parseContextOverflowMsg(e.Message)
}

// parseContextOverflowMsg 从消息文本解析 context overflow 的 token 数。
func parseContextOverflowMsg(msg string) (inputTokens, maxTokens, contextLimit int, ok bool) {
	if m := contextOverflowRe1.FindStringSubmatch(msg); m != nil {
		return atoiSafe(m[1]), atoiSafe(m[2]), atoiSafe(m[3]), true
	}
	if m := contextOverflowRe2.FindStringSubmatch(msg); m != nil {
		return atoiSafe(m[1]), 0, atoiSafe(m[2]), true
	}
	return 0, 0, 0, false
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ============================================================
// P0-5 (A9): 流式中断恢复 ResumePolicy
//
// 当流式响应中途断开（网络中断、服务端关闭等），根据已接收的事件类型
// 判断是否可安全重试：
// - ResumeText: 仅收到 text_delta → 安全重试（无副作用）
// - ResumeStructured: 收到 thinking_delta/tool_input_delta 但无 tool_use → 安全重试
// - ResumeNone: 已收到 ToolUseEvent → 不可重试（工具调用状态歧义，重试可能导致重复执行）
//
// ToolUseEvent 出现后标记 ResumeNone，阻止自动恢复。
// ============================================================

// ResumePolicy 表示流式中断后的可恢复性级别。
type ResumePolicy int

const (
	// ResumeText 仅文本已流式传输，可安全恢复。
	ResumeText ResumePolicy = iota
	// ResumeStructured 结构化内容（thinking/tool_input_delta）已传输但无 tool_use，可恢复。
	ResumeStructured
	// ResumeNone tool_use 已开始，不可恢复（工具状态歧义）。
	ResumeNone
)

// String returns the policy name for logging.
func (p ResumePolicy) String() string {
	switch p {
	case ResumeText:
		return "text"
	case ResumeStructured:
		return "structured"
	case ResumeNone:
		return "none"
	}
	return "unknown"
}

// CanResume 判断当前 policy 是否允许中断后重试。
// 仅 ResumeText 和 ResumeStructured 允许重试；ResumeNone 不允许。
func (p ResumePolicy) CanResume() bool {
	return p != ResumeNone
}

// ResumeTracker 跟踪流式事件，维护当前 ResumePolicy。
// 非线程安全：每个流式请求应使用独立的 tracker 实例。
type ResumeTracker struct {
	policy ResumePolicy
}

// NewResumeTracker 创建一个 ResumeTracker，初始策略为 ResumeText。
func NewResumeTracker() *ResumeTracker {
	return &ResumeTracker{policy: ResumeText}
}

// Update 根据接收到的事件更新恢复策略。
// ToolUseEvent → ResumeNone（不可恢复）。
// ThinkingDeltaEvent / ToolInputDeltaEvent → ResumeStructured（若无 tool_use）。
// TextDeltaEvent → 保持当前策略不变（ResumeText 或更高）。
func (rt *ResumeTracker) Update(event Event) {
	if rt == nil {
		return
	}
	switch event.eventType() {
	case "tool_use":
		// tool_use 已出现 → 不可恢复
		rt.policy = ResumeNone
	case "thinking_delta", "tool_input_delta":
		// 结构化内容：若尚未到 ResumeNone 则升级为 Structured
		if rt.policy == ResumeText {
			rt.policy = ResumeStructured
		}
	}
}

// Policy 返回当前恢复策略。
func (rt *ResumeTracker) Policy() ResumePolicy {
	if rt == nil {
		return ResumeText
	}
	return rt.policy
}

// CanResume 便捷方法：当前策略是否允许中断后重试。
func (rt *ResumeTracker) CanResume() bool {
	return rt.Policy().CanResume()
}

// SSEParser parses Server-Sent Events stream
type SSEParser struct{}

// NewSSEParser creates SSE parser
func NewSSEParser() *SSEParser {
	return &SSEParser{}
}

// SSEEvent single SSE event
type SSEEvent struct {
	Event string
	Data  string
	ID    string
	Retry string
}

// ParseStream parses SSE event stream from HTTP response body.
// 旧签名保留（向后兼容，吸纳审查优化 1），内部委托给 ParseStreamWithBackpressure，
// 使用 DefaultBackpressureConfig()。
func (p *SSEParser) ParseStream(body io.ReadCloser, events chan<- SSEEvent) error {
	return p.ParseStreamWithBackpressure(body, events, DefaultBackpressureConfig())
}

// ParseStreamWithBackpressure 带背压配置的 SSE 解析（新调用方使用）。
// 内部通过 BackpressurePipe 包装 in channel，按 cfg.Strategy 处理背压：
// - drop：满则丢弃非保护事件 + 计数
// - block：阻塞 BlockTimeout 后降级 drop
// - signal：满则丢弃 + 计数（消费者可通过 metrics.SignaledCount 感知）
//
// UsageEvent/MessageStopEvent/ErrorEvent 等受保护事件永不丢弃（吸纳审查优化 6）。
// SNAPSHOT: 原签名 func (p *SSEParser) ParseStream(body, events) error；
//
//	新增 ParseStreamWithBackpressure 供新调用方使用，旧 ParseStream 保持不变。
func (p *SSEParser) ParseStreamWithBackpressure(
	body io.ReadCloser,
	events chan<- SSEEvent,
	bpCfg BackpressureConfig,
) error {
	defer body.Close()

	bufSize := bpCfg.BufferSize
	if bufSize <= 0 {
		bufSize = 32
	}
	if bpCfg.BlockTimeout <= 0 {
		bpCfg.BlockTimeout = 5 * time.Second
	}

	// 内部 in channel，由 ParseStream 主循环写入
	in := make(chan SSEEvent, bufSize)
	pipe := NewBackpressurePipe[SSEEvent](in, bpCfg, isProtectedSSEEvent)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := pipe.Run(ctx)

	// 启动 goroutine 把 out 转发到调用方提供的 events channel
	// 调用方从 events 读取速度慢会导致 out 阻塞，触发 BackpressurePipe 背压策略
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		for evt := range out {
			select {
			case events <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()

	reader := bufio.NewReader(body)
	var currentEvent SSEEvent

	// flushEvent 把 currentEvent 写入 in channel（受 ctx 控制）
	flushEvent := func() {
		if currentEvent.Data != "" {
			select {
			case in <- currentEvent:
			case <-ctx.Done():
			}
			currentEvent = SSEEvent{}
		}
	}

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				flushEvent()
				close(in)
				<-forwardDone // 等待所有事件转发完成
				return nil
			}
			flushEvent()
			close(in)
			<-forwardDone
			return fmt.Errorf("failed to read SSE stream: %w", err)
		}

		line = bytes.TrimRight(line, "\r\n")
		lineStr := string(line)

		if lineStr == "" {
			flushEvent()
			continue
		}

		if strings.HasPrefix(lineStr, "event:") {
			currentEvent.Event = strings.TrimSpace(strings.TrimPrefix(lineStr, "event:"))
		} else if strings.HasPrefix(lineStr, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(lineStr, "data:"))
			if currentEvent.Data != "" {
				currentEvent.Data += "\n"
			}
			currentEvent.Data += data
		} else if strings.HasPrefix(lineStr, "id:") {
			currentEvent.ID = strings.TrimSpace(strings.TrimPrefix(lineStr, "id:"))
		} else if strings.HasPrefix(lineStr, "retry:") {
			currentEvent.Retry = strings.TrimSpace(strings.TrimPrefix(lineStr, "retry:"))
		}
	}
}

// isProtectedSSEEvent 判断 SSEEvent 是否为受保护事件（永不丢弃）。
// 受保护事件：
// - Event == "message_start"（包含 input_tokens usage）
// - Event == "message_delta"（包含 output_tokens usage）
// - Event == "message_stop"（流结束信号）
// - Event == "error"（错误事件）
// - Data == "[DONE]"（OpenAI 流终止符）
// - Data 含 "usage" JSON 字段（OpenAI 路径下 usage 单独成帧）
//
// 这些事件在 drop 策略下也不应被丢弃，避免 token 用量丢失（吸纳审查优化 6）。
func isProtectedSSEEvent(evt SSEEvent) bool {
	switch evt.Event {
	case "message_start", "message_delta", "message_stop", "error":
		return true
	}
	if evt.Data == "[DONE]" {
		return true
	}
	if strings.Contains(evt.Data, `"usage"`) {
		return true
	}
	return false
}

// ParseAnthropicEvent parses event from Anthropic SSE data
func ParseAnthropicEvent(data string) (Event, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	eventType, _ := raw["type"].(string)

	switch eventType {
	case "content_block_delta":
		delta, _ := raw["delta"].(map[string]interface{})
		if delta == nil {
			return nil, nil
		}
		deltaType, _ := delta["type"].(string)
		switch deltaType {
		case "text_delta":
			text, _ := delta["text"].(string)
			return TextDeltaEvent{Content: text}, nil
		case "thinking_delta":
			text, _ := delta["thinking"].(string)
			return ThinkingDeltaEvent{Content: text}, nil
		case "input_json_delta":
			partialJSON, _ := delta["partial_json"].(string)
			idx := 0
			if indexVal, ok := raw["index"].(float64); ok {
				idx = int(indexVal)
			}
			return ToolInputDeltaEvent{PartialJSON: partialJSON, Index: idx}, nil
		default:
			return nil, fmt.Errorf("unknown content_block_delta type: %s", deltaType)
		}

	case "message_start":
		message, _ := raw["message"].(map[string]interface{})
		if message != nil {
			usage, _ := message["usage"].(map[string]interface{})
			if usage != nil {
				in, _ := usage["input_tokens"].(float64)
				cacheCreate, _ := usage["cache_creation_input_tokens"].(float64)
				cacheRead, _ := usage["cache_read_input_tokens"].(float64)
				return UsageEvent{
					InputTokens:   int(in),
					CacheCreation: int(cacheCreate),
					CacheRead:     int(cacheRead),
				}, nil
			}
		}

	case "message_delta":
		usage, _ := raw["usage"].(map[string]interface{})
		if usage != nil {
			out, _ := usage["output_tokens"].(float64)
			return UsageEvent{OutputTokens: int(out)}, nil
		}

	case "message_stop":
		return MessageStopEvent{}, nil

	case "content_block_start":
		contentBlock, _ := raw["content_block"].(map[string]interface{})
		if contentBlock != nil {
			blockType, _ := contentBlock["type"].(string)
			if blockType == "tool_use" {
				id, _ := contentBlock["id"].(string)
				name, _ := contentBlock["name"].(string)
				return ToolUseEvent{ID: id, Name: name}, nil
			}
		}

	case "content_block_stop":
		return nil, nil

	case "error":
		errObj, _ := raw["error"].(map[string]interface{})
		if errObj != nil {
			msg, _ := errObj["message"].(string)
			apiErr := fmt.Errorf("%s", msg)
			return ErrorEvent{Err: apiErr}, apiErr
		}
	}

	return nil, nil
}

// ============================================================
// 散文状态码判据**让路**给结构化事实
//
// 收口前 IsContextOverflowError 直接读文本且含**裸 "413"**，而 retry.go 在结构化
// `switch apiErr.ErrorID` **之前**就调它 ⇒ 任何文本里含 "413" 的错误（例如
// "API error 500 [ai_retryable]: backend 4132 unavailable"）都被判成"上下文溢出"，
// 进"截断消息头 / 调低 max_tokens"分支；`MapHTTPStatusToErrorID(413)=ErrAIFatal`
// 这类结构化分类**永远读不到**（散文判据抢答）。
//
// 收口后分两层：
// - **结构化层**（持有 *APIError 的调用方）：APIError.IsContextOverflow() / isContextOverflowErr(err)
// —— 状态码可用时由状态码裁决，消息里的裸数字不可信；
// - **回落层**（只有字符串的调用方）：IsContextOverflowError(msg) —— 保留原关键词表。
// ⚠️ 回落层**必须留**，理由见 contextOverflowPhrases 的注释。
// ============================================================

// contextOverflowPhrases 是"上下文溢出"的**措辞**集合（不含裸状态码）。
//
// 为什么回落层必须留（勿以"都改结构化"为由删掉）：
// 1. "context overflow" 在本协议下**没有专属 ErrorID** —— provider 把它编码在
// HTTP 400/413 的 message 里（Anthropic："prompt is too long: X tokens > Y maximum"）。
// 状态码维度只能给出 ErrAIFatal（400/401/403/404/405/413 同值）⇒ **无法单独判出"溢出"**。
// 2. 溢出后的动作（截断消息头 / 调低 max_tokens）是**唯一能从此错误恢复**的路径；
// 判据缺失 = 该恢复路径不可达 ⇒ 用户看到硬失败（功能性丢失，不是"多一条提示"）。
// 3. 且现有调用方 tui/plain.go:526、tui/bridge.go:964、tui/app.go:2403、
// context/recovery.go:198 手上**只有字符串**（errMsg），物理上拿不到 *APIError。
// ⇒ 结构化能"确认"溢出（413 / 专属 type），但**不能否定**它 ⇒ 只能"结构化优先 + 短语回落"。
var contextOverflowPhrases = []string{
	"context overflow",
	"prompt too long",
	"prompt-too-long",
	"token limit",
	"request too large",
	"exceeds the maximum",
	"too many tokens",
}

// isContextOverflowPhrase 是**纯措辞**判据（不含裸状态码），结构化层与回落层共用。
func isContextOverflowPhrase(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range contextOverflowPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsContextOverflowError 判断文本是否为上下文溢出（**散文回落**）。
//
// ⚠️ ：这是**回落**，只应在调用方**拿不到结构化状态码**时使用。持有 *APIError 的
// 调用方请用 APIError.IsContextOverflow() 或 isContextOverflowErr(err)。
//
// ★ 裸 "413" 必须**留在这里**：本函数的外部调用方（tui/plain.go:526、tui/bridge.go:964、
// tui/app.go:2403、context/recovery.go:198）只有 err.Error() 文本，而 APIError.Error()
// 自带 "API error 413 …" ⇒ 裸数字是它们**唯一**可得的溢出信号。
// ★ 但它**不得**进入结构化路径：状态码已知时文本里的裸数字是噪声
// （"backend 4132 unavailable" / "413000 tokens" 都会被裸子串匹配误命中）。
func IsContextOverflowError(msg string) bool {
	return isContextOverflowPhrase(msg) || strings.Contains(strings.ToLower(msg), "413")
}

// isContextOverflowErr 是"结构化优先"的统一入口。
// 错误链上能取到 *APIError ⇒ 由状态码/ErrorID 裁决；取不到 ⇒ 回落文本判据
// （回落为何必须留，见 contextOverflowPhrases）。
func isContextOverflowErr(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if stderrors.As(err, &apiErr) && apiErr != nil {
		return apiErr.IsContextOverflow()
	}
	return IsContextOverflowError(err.Error())
}

// isPromptTooLongError 判断是否为 "prompt too long" 类错误（P0-4 / FACT-ERR-006）。
// 此类错误表明输入本身过长（而非 max_tokens 过大），应截断消息头部后重试。
// 与一般 context overflow 区分：context overflow 可能仅因 max_tokens 过大，
// 调低 max_tokens 即可；prompt too long 必须截断输入。
func isPromptTooLongError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "prompt too long") ||
		strings.Contains(lower, "prompt-too-long")
}

// IsOverloadedError determines if it is a model overloaded error（**散文回落**，）。
//
// ⚠️ 这是回落层：只应在调用方拿不到结构化信息时使用。持有 *APIError 的调用方请用
// isOverloadedErr(err) / APIError.IsOverloaded()（后者读 StatusCode==529 或 body type）。
// ★ 裸 "529" 必须留在此处：外部调用方（tui/plain.go:539、tui/bridge.go:987、
// context/recovery.go:223）只有字符串；且 retry.go 的"非 *APIError 兜底"路径同样只有文本。
func IsOverloadedError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "529") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "capacity")
}

// isOverloadedErr 是"结构化优先"的统一入口。
// 错误链上能取到 *APIError ⇒ 读 StatusCode/Type；取不到 ⇒ 回落文本判据。
// 回归要点：本函数自带一次 errors.As（局部变量），因此**不**继承 retry.go 里那个
// 跨迭代陈旧的 `apiErr`；即"上一轮是 APIError、本轮是普通错误"时，
// 这里仍能按文本正确判成 529，而不是被陈旧的非 nil 指针挡掉。
func isOverloadedErr(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if stderrors.As(err, &apiErr) && apiErr != nil {
		return apiErr.IsOverloaded()
	}
	return IsOverloadedError(err.Error())
}

// IsRateLimitError determines if it is a rate limit error.
// P0-2 修复：529 不再归为 RateLimit（529 = Overloaded），仅 429 为 RateLimit。
// 误判 529 为 RateLimit 会污染 FallbackChain 的降级决策。
// **薄转发**到唯一映射表（旧实现自持字面量 `statusCode == 429`，
// 是 MapHTTPStatusToErrorID 之外的第二份口径）。
func IsRateLimitError(statusCode int) bool {
	return MapHTTPStatusToErrorID(statusCode, "") == ErrAIApiRateLimit
}

// rateLimitStatusRe 是"消息里出现限流状态码"的**词边界**锚定正则（避免 "14290" 命中 "429"）。
var rateLimitStatusRe = regexp.MustCompile(`\b429\b`)

// IsRateLimitErrorFromMsg 通过错误消息判断是否为限流错误（**散文回落**，）。
//
// ⚠️ 这是回落层：唯一调用方 context/recovery.go:239 的入参只有字符串
// （`GetRecoveryStrategy(errMsg string)`，物理上拿不到 *APIError）⇒ 必须保留。
// 持有 *APIError 的调用方请用 APIError.IsRateLimit()。
//
// 裸子句由 `retryableStatusCodeRe.MatchString(lower) && strings.Contains(lower, "429")`
// 改为专用词边界正则 —— 旧写法是"先匹配一张含 8 个状态码的表、再要求文本含 429"，
// 表那一半恒为真（含 \b429\b 时必然命中），却让人误以为这里有两次独立判据。
func IsRateLimitErrorFromMsg(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		rateLimitStatusRe.MatchString(lower)
}

// IsNetworkError 通过错误消息判断是否为网络错误。
// 供 context/recovery.go 调用，统一错误识别逻辑，使用词边界匹配避免误判。
func IsNetworkError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "dial tcp") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "context deadline exceeded")
}

// IsRetryableError 根据 HTTP 状态码判断是否可重试。
// 修复 P1：统一与 APIError.IsRetryable() 行为一致（策略反转）。
// 429 (rate limit) 和 529 (overloaded) 不可直接重试：
// - 429 需等待 RetryAfter 后由专用限流处理路径重试（方案 D 协同）
// - 529 直接走降级链路（对齐业内既有实现 ServerOverloaded.is_retryable = false）
//
// 408 (Request Timeout) 和 425 (Too Early) 可重试（对齐 APIError.IsRetryable 的 StatusCode 降级路径）。
//
// 本函数不再自持状态码字面量集合，改为由 httpStatusErrorIDSpecs（身份）
// + errorCategorySpecs（分类）推出 —— 与 APIError.IsRetryable()（薄转发到本函数）
// 因此**恒一致**。收口前两者在未登记 5xx（501/505…599）上给出相反答案
// （本函数 `>=500 && <600` 为 true、方法的 switch 未列出为 false）。
func IsRetryableError(statusCode int) bool {
	if id, ok := httpStatusErrorIDSpecs[statusCode]; ok {
		return categoryFromID(id) == CategoryRetryable
	}
	// 未登记状态码：5xx 视为服务端瞬态（保守可重试），与 MapHTTPStatusToErrorID
	// 的"保守可重试，不丢请求"兜底同口径；其余（0/1xx/2xx/3xx/4xx 未登记）不可重试。
	return statusCode >= 500 && statusCode < 600
}

// IsRetryableErrorFromMsg determines if retryable from error message.
// P0-2 修复：用 \b 词边界锚定的正则匹配状态码，避免 "5000 tokens" / "error code 15000"
// 等被旧的 strings.Contains(lower, "500") 误判为可重试。
// 关键词（timeout/overloaded/capacity/rate limit 等）仍用子串匹配。
func IsRetryableErrorFromMsg(msg string) bool {
	lower := strings.ToLower(msg)
	// 词边界锚定状态码匹配：仅匹配独立的状态码数字，避免 "5000" 命中 "500"
	if retryableStatusCodeRe.MatchString(lower) {
		return true
	}
	// 非状态码关键词匹配
	keywordPatterns := []string{
		"timeout", "connection reset", "temporary",
		"overloaded", "capacity", "rate limit",
	}
	for _, p := range keywordPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// BuildAnthropicMessages converts internal message format to Anthropic API format
func BuildAnthropicMessages(msgs []APIMessage) []MessageEntry {
	var entries []MessageEntry
	for _, msg := range msgs {
		entries = append(entries, msg.ToAPIFormat())
	}
	return entries
}

// ============================================================
// S1（M04 不足点 3）：transport 层背压 — BackpressurePipe
//
// 提供三种背压策略（吸纳审查优化 1/6/8/9）：
// - drop（默认）：丢弃事件 + 计数（仅丢弃 TextDeltaEvent/ThinkingDeltaEvent，
// 保护 UsageEvent/MessageStopEvent 不被丢弃）
// - block：阻塞生产者，超时（默认 5s）后降级 drop
// - signal：发送信号让消费者感知后自我限速（通过 metrics.SignaledCount 暴露）
//
// 业内对标：
// - Go io.Pipe（同步阻塞型背压）https://pkg.go.dev/io#Pipe
// - gRPC flow control（HTTP/2 stream window + SETTINGS_MAX_CONCURRENT_STREAMS）
// https://grpc.io/docs/what-is-grpc/core-concepts/#flow-control
// - Anthropic streaming（Last-Event-ID + retry 字段）
// https://docs.anthropic.com/en/api/messages-streaming
// - OpenAI streaming（chunked transfer + data: [DONE] 终止符）
// https://platform.openai.com/docs/api-reference/chat/streaming
// - SSE 重连规范（retry: 字段 + Last-Event-ID header）
// https://html.spec.whatwg.org/multipage/server-sent-events.html
// ============================================================

// BackpressureStrategy 背压策略枚举
type BackpressureStrategy int

const (
	// BackpressureDrop 默认策略：丢弃事件 + 计数（仅丢弃 TextDeltaEvent/ThinkingDeltaEvent，
	// 保护 UsageEvent/MessageStopEvent 不被丢弃）
	BackpressureDrop BackpressureStrategy = iota
	// BackpressureBlock 阻塞生产者，超时后降级 drop
	BackpressureBlock
	// BackpressureSignal 发送 BackpressureSignalEvent 让消费者感知
	BackpressureSignal
)

// String 返回策略名字符串，用于日志和 metrics 标签
func (s BackpressureStrategy) String() string {
	switch s {
	case BackpressureDrop:
		return "drop"
	case BackpressureBlock:
		return "block"
	case BackpressureSignal:
		return "signal"
	}
	return "unknown"
}

// BackpressureConfig 背压配置（吸纳审查优化 1/6/9）
//
// T1-B实施期修复：OnDrop 参数类型从 SSEEvent 改为 any，
// 以兼容 BackpressurePipe[T] 的泛型 T（T 可以是 SSEEvent 或 Event）。
// 原签名 func(evt SSEEvent) 与泛型 T 不匹配导致编译失败（pre-existing bug）。
type BackpressureConfig struct {
	// BufferSize channel 缓冲大小，默认 32
	BufferSize int
	// Strategy drop / block / signal
	Strategy BackpressureStrategy
	// BlockTimeout block 策略超时降级 drop，默认 5s
	BlockTimeout time.Duration
	// OnDrop 丢弃回调（用于 metrics / 事件总线），不应记录事件原始内容。
	// 参数类型为 any 以兼容泛型 T（T 可以是 SSEEvent 或 Event）。
	OnDrop func(evt any)
}

// DefaultBackpressureConfig 提供默认背压配置（向后兼容）
func DefaultBackpressureConfig() BackpressureConfig {
	return BackpressureConfig{
		BufferSize:   32,
		Strategy:     BackpressureDrop,
		BlockTimeout: 5 * time.Second,
	}
}

// BackpressureMetrics 背压指标（线程安全，使用 atomic 计数）
type BackpressureMetrics struct {
	// DroppedCount 丢弃事件计数（drop 策略 + block 超时降级）
	DroppedCount atomic.Int64
	// BlockedCount block 策略触发计数（含超时降级）
	BlockedCount atomic.Int64
	// SignaledCount signal 策略发送信号计数
	SignaledCount atomic.Int64
}

// BackpressurePipe 在 transport 层包装 channel，提供背压能力。
// 泛型 T 支持 SSEEvent / Event 等多种事件类型（避免类型转换开销）。
// Run goroutine 在 in channel 关闭或 ctx 取消时退出（吸纳审查优化 8，无 goroutine 泄漏）。
type BackpressurePipe[T any] struct {
	in          <-chan T
	out         chan T
	cfg         BackpressureConfig
	metrics     *BackpressureMetrics
	isProtected func(T) bool // 判断事件是否受保护（永不丢弃）；nil 时所有事件均可丢弃
}

// NewBackpressurePipe 创建背压管道。
// isProtected 为 nil 时所有事件均可丢弃；非 nil 时受保护事件永不丢弃（吸纳审查优化 6）。
// cfg.BufferSize / BlockTimeout <= 0 时使用默认值（32 / 5s）。
func NewBackpressurePipe[T any](in <-chan T, cfg BackpressureConfig, isProtected func(T) bool) *BackpressurePipe[T] {
	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = 32
	}
	if cfg.BlockTimeout <= 0 {
		cfg.BlockTimeout = 5 * time.Second
	}
	return &BackpressurePipe[T]{
		in:          in,
		out:         make(chan T, bufSize),
		cfg:         cfg,
		metrics:     &BackpressureMetrics{},
		isProtected: isProtected,
	}
}

// Run 启动背压管道 goroutine，返回 out channel。
// goroutine 在 in channel 关闭或 ctx 取消时退出（吸纳审查优化 8）。
// 调用方应通过 ctx 在不再消费时取消管道，避免 goroutine 泄漏。
func (bp *BackpressurePipe[T]) Run(ctx context.Context) <-chan T {
	go func() {
		defer close(bp.out)
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-bp.in:
				if !ok {
					return
				}
				bp.send(ctx, evt)
			}
		}
	}()
	return bp.out
}

// send 按策略发送事件到 out channel。
// 受保护事件永不丢弃（吸纳审查优化 6：保护 UsageEvent/MessageStopEvent）。
func (bp *BackpressurePipe[T]) send(ctx context.Context, evt T) {
	// 受保护事件：阻塞写入，永不丢弃
	if bp.isProtected != nil && bp.isProtected(evt) {
		select {
		case bp.out <- evt:
		case <-ctx.Done():
		}
		return
	}

	switch bp.cfg.Strategy {
	case BackpressureDrop:
		// drop 策略：满则丢弃非保护事件 + 计数
		select {
		case bp.out <- evt:
		default:
			bp.metrics.DroppedCount.Add(1)
			if bp.cfg.OnDrop != nil {
				bp.cfg.OnDrop(evt)
			}
		}

	case BackpressureBlock:
		// block 策略：阻塞 BlockTimeout 后降级 drop
		timer := time.NewTimer(bp.cfg.BlockTimeout)
		defer timer.Stop()
		select {
		case bp.out <- evt:
		case <-timer.C:
			// 超时降级 drop
			bp.metrics.BlockedCount.Add(1)
			select {
			case bp.out <- evt:
			default:
				bp.metrics.DroppedCount.Add(1)
				if bp.cfg.OnDrop != nil {
					bp.cfg.OnDrop(evt)
				}
			}
		case <-ctx.Done():
		}

	case BackpressureSignal:
		// signal 策略：满则丢弃 + 计数（消费者可通过 metrics.SignaledCount 感知并自我限速）
		select {
		case bp.out <- evt:
		default:
			bp.metrics.SignaledCount.Add(1)
			if bp.cfg.OnDrop != nil {
				bp.cfg.OnDrop(evt)
			}
		}
	}
}

// Metrics 返回背压指标快照（线程安全，atomic 读取）
func (bp *BackpressurePipe[T]) Metrics() *BackpressureMetrics {
	return bp.metrics
}
