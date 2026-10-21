// Package ai — DashScopeNativeAdapter 真实实现（§5.3，T1-A S5 落地）。
//
// 本文件实现 DashScopeNativeAdapter 的 ProviderAdapter interface 4 方法：
// - BuildRequest：构造 DashScope generation API 请求（input.messages/parameters）
// - ParseStream：复用 SSEParser 解析 DashScope SSE 流（result.generate / result.done / result.error）
// - MapError：DashScope 错误码映射（code → ErrorID）
// - InjectPrivateFields：注入 parameters.enable_thinking / parameters.thinking_budget（检查 SupportsThinking）
//
// SNAPSHOT: qwen 系列从此适配器走原生 DashScope 协议（从 Anthropic 兼容分支迁出， §5.4 优化 5）。
// usage 字段统一转换：usage.input_tokens / usage.output_tokens → UsageEvent
//
// REF-外部-DashScope API @官方URL https://help.aliyun.com/zh/model-studio/developer-reference/use-qwen-by-calling-api（访问日期 2026-07-20）
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// ============================================================
// BuildRequest — 构造 DashScope generation API HTTP 请求
// ============================================================

// buildDashScopeRequest 构造 DashScope generation API 的 HTTP 请求（§5.3 S5）。
// DashScope API 端点：POST {baseURL}/api/v1/services/aigc/text-generation/generation
// 请求体结构：{model, input: {messages: [...]}, parameters: {...}}
// API Key 通过 Authorization: Bearer {apiKey} header 传递。
//
// REF-外部-DashScope API @官方URL https://help.aliyun.com/zh/model-studio/developer-reference/use-qwen-by-calling-api
//
// 参数：
// - ctx: 请求上下文
// - req: 统一 Request 结构
// - snap: SimpleClient 字段快照
//
// 返回：构造完成的 *http.Request。
func (a *DashScopeNativeAdapter) buildDashScopeRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	body := a.buildDashScopeBody(req, snap)

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize dashscope request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/services/aigc/text-generation/generation",
		strings.TrimRight(snap.baseURL, "/"))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create dashscope request: %w", err)
	}

	// DashScope 使用 Bearer token 认证 + X-DashScope-SSE 标识流式
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+snap.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	// X-DashScope-SSE: enable 告知 DashScope 返回 SSE 格式
	httpReq.Header.Set("X-DashScope-SSE", "enable")

	return httpReq, nil
}

// buildDashScopeBody 构造 DashScope generation API 请求体（§5.3 S5）。
// 将统一 Request 转换为 DashScope 格式：
// - SystemPrompt → input.messages[{role:system, content}]
// - Messages → input.messages[{role, content}]
// - Tools → parameters.tools[{type:function, function:{name, description, parameters}}]
// - Thinking → parameters.enable_thinking + parameters.thinking_budget
// - MaxTokens → parameters.max_tokens
func (a *DashScopeNativeAdapter) buildDashScopeBody(req Request, snap clientSnapshot) map[string]interface{} {
	// input.messages
	messages := make([]map[string]interface{}, 0)
	if len(req.SystemPrompt) > 0 {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": strings.Join(req.SystemPrompt, "\n\n"),
		})
	}
	for _, msg := range req.Messages {
		role := msg.Role
		if role == "assistant" {
			role = "assistant"
		}
		// 处理 content（string 或 blocks）
		content := msg.Content
		if blocks, ok := msg.Content.([]interface{}); ok {
			// 提取 text blocks
			var textParts []string
			for _, b := range blocks {
				if block, ok := b.(map[string]interface{}); ok {
					if t, ok := block["text"].(string); ok {
						textParts = append(textParts, t)
					}
				}
			}
			if len(textParts) > 0 {
				content = strings.Join(textParts, "")
			}
		}
		messages = append(messages, map[string]interface{}{
			"role":    role,
			"content": content,
		})
	}

	body := map[string]interface{}{
		"model": snap.model,
		"input": map[string]interface{}{
			"messages": messages,
		},
	}

	// parameters
	params := map[string]interface{}{}
	if req.MaxTokens > 0 {
		params["max_tokens"] = req.MaxTokens
	}
	params["stream"] = true
	params["incremental_output"] = true

	// thinking 三态渲染（唯一判据 ThinkingConfig.Resolve，见 client.go）：
	// On → parameters.enable_thinking=true（+ thinking_budget）；需模型确实支持，
	// 否则对不支持 thinking 的模型下发 enable 会被服务端拒绝；
	// Off → parameters.enable_thinking=false ★「显式关闭」必须能下发：
	// 「不要思考」对任何模型都是安全的，且它是用户明确表达的意思；
	// 未指定 → 不下发，由服务端默认决定。
	// 判据 = ThinkingSupportFor（唯一权威入口，B2：用户声明 > 端点白名单 > 静态表 >
	// 前缀推断，且「不知道」单独一档 ⇒ 不下发）。勿直接读 cap.SupportsThinking。
	// ★ A2签名加端点维：本适配器**非生产路径**（B4 定案 §0 冻结，
	// 生产走 SimpleClient.Stream 的 OpenAI 兼容分支），此处只做**签名适配**，
	// 不改变"冻结"的含义 —— Go 里判据只能有一份签名，见 B4 定案 §6 A2-2。
	switch req.Thinking.Resolve() {
	case ThinkingOn:
		if ThinkingSupportFor(snap.model, snap.baseURL) == ThinkingCapSupported {
			params["enable_thinking"] = true
			if req.Thinking.BudgetTokens > 0 {
				params["thinking_budget"] = req.Thinking.BudgetTokens
			}
		}
	case ThinkingOff:
		params["enable_thinking"] = false
	}

	// S6 / L1：原生检索 —— **原生协议的官方位置是 parameters.enable_search**。
	// ★ 与 OpenAI 兼容协议（请求体顶层）位置不同，是对应两套协议；
	// 但"该不该注入"共用同一判定 nativeSearchInjectAllowed（注入位置可变、结论必须同源）。
	if a.nativeSearchInjectAllowed(snap.model, snap.baseURL) {
		params["enable_search"] = true
	}

	if len(req.Tools) > 0 {
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
		params["tools"] = tools
	}

	body["parameters"] = params

	return body
}

// ============================================================
// ParseStream — DashScope SSE 流解析（复用 SSEParser）
// ============================================================

// parseDashScopeStream 解析 DashScope SSE 流并转为统一 Event 通道（§5.3 S5）。
// DashScope SSE 事件类型：
// - result.generate：增量内容，提取 output.text → TextDeltaEvent / output.thinking → ThinkingDeltaEvent
// - result.done：流结束，提取 usage → UsageEvent + MessageStopEvent
// - result.error：错误事件，转 ErrorEvent
//
// 复用 stream.go 的 SSEParser 解析 SSE 帧，DashScope 事件数据为 JSON 格式。
//
// REF-外部-DashScope SSE @官方URL https://help.aliyun.com/zh/model-studio/developer-reference/use-qwen-by-calling-api
func (a *DashScopeNativeAdapter) parseDashScopeStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	// 捕获 tokenSource（持 RLock 快照）
	a.SimpleClient.mu.RLock()
	tokenSource := a.SimpleClient.tokenSource
	model := a.SimpleClient.model
	a.SimpleClient.mu.RUnlock()

	events := make(chan Event, 64)
	reqCtx := resp.Request.Context()

	// DashScope tool_calls 增量累积状态（与 OpenAI 兼容路径 parseOpenAIDeltaWithAccum 同构）。
	//
	// 修复（同类隐患，kimi-k2.7-code 同帧 bug 的 DashScope 版本）：
	// 原实现在 parseDashScopeGenerate 里对每一帧的 tool_calls 直接 emit 一个
	// 完整 ToolUseEvent，隐含假设"一帧就带齐 id + name + 完整 arguments"。
	// 但 incremental_output=true 时 arguments 是逐帧分片的，且只有首帧带 id/name
	// （部分兼容层后续帧显式下发空字符串而非省略字段），于是：
	// - 首帧 arguments 为空 → 被 `args == "" → "{}"` 兜底成一个参数为空的工具调用
	// - 后续帧 name 为空 → 执行器找不到工具而丢弃
	// 最终表现与 kimi 一致：用户看到 "AI returned an empty response."，
	// 或更糟——真的执行了一个参数为空的 bash 调用。
	//
	// 现改为按 index 累积，在流结束（result.done / 流提前终止）时统一 flush。
	pendingToolArgs := make(map[int]string)
	pendingToolNames := make(map[int]string)
	pendingToolIDs := make(map[int]string)
	// flushOnce 保证 result.done 与流提前终止两条路径只 flush 一次。
	var flushOnce sync.Once
	flushToolCalls := func() {
		flushOnce.Do(func() {
			for idx, args := range pendingToolArgs {
				name := pendingToolNames[idx]
				id := pendingToolIDs[idx]
				if id == "" {
					id = fmt.Sprintf("call_%d", idx)
				}
				if name == "" {
					// 与 OpenAI 路径一致：拿不到工具名就丢弃，避免执行器收到空名调用。
					continue
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
		})
	}

	go func() {
		defer resp.Body.Close()
		// defer 为 LIFO：close(events) 先注册后执行，flushToolCalls 后注册先执行，
		// 保证 flush 一定发生在 close 之前（向已关闭的 channel 发送会 panic）。
		defer close(events)
		// 流提前终止（出错 / ctx 取消 / 连接断开）时也要 flush，
		// 否则已经累积到的工具调用会随 goroutine 退出一起丢失。
		defer flushToolCalls()

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
				case events <- ErrorEvent{Err: fmt.Errorf("dashscope sse error: %w", fmt.Errorf("%s", sse.Data))}:
				case <-reqCtx.Done():
					return
				}
				return
			}

			// DashScope event 类型在 sse.Event 字段
			switch sse.Event {
			case "result.error":
				select {
				case events <- ErrorEvent{Err: fmt.Errorf("dashscope error: %s", sse.Data)}:
				case <-reqCtx.Done():
					return
				}
				return
			case "result.done":
				// 流正常结束：先 flush 累积的 tool_calls，再发 usage / MessageStop。
				// 顺序不能反——执行器依赖 MessageStop 判定一轮结束。
				flushToolCalls()
				// 解析 usage + 发送 MessageStop
				a.parseDashScopeUsage(sse.Data, events, reqCtx, tokenSource, model, estimatedInputTokens)
				select {
				case events <- MessageStopEvent{}:
				case <-reqCtx.Done():
					return
				}
				return
			case "result.generate":
				// 解析增量内容（tool_calls 只累积，不在此处 emit）
				a.parseDashScopeGenerate(sse.Data, events, reqCtx, pendingToolArgs, pendingToolNames, pendingToolIDs)
			}
		}
	}()

	return events, nil
}

// parseDashScopeGenerate 解析 DashScope result.generate 事件数据，提取 text/thinking 内容。
//
// tool_calls 只做累积（写入 pendingArgs/pendingNames/pendingIDs），不在本帧 emit：
// incremental_output=true 时 arguments 逐帧分片，只有首帧带 id/name，
// 逐帧直发会拆出多个残缺的 ToolUseEvent（详见 parseDashScopeStream 中的修复说明）。
// 统一由 flushToolCalls 在流结束时拼合发出。
func (a *DashScopeNativeAdapter) parseDashScopeGenerate(data string, events chan<- Event, ctx context.Context,
	pendingArgs map[int]string, pendingNames map[int]string, pendingIDs map[int]string) {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return
	}

	output, ok := obj["output"].(map[string]interface{})
	if !ok {
		return
	}

	// thinking 内容
	if thinking, ok := output["thinking"].(string); ok && thinking != "" {
		select {
		case events <- ThinkingDeltaEvent{Content: thinking}:
		case <-ctx.Done():
			return
		}
	}

	// text 内容
	if text, ok := output["text"].(string); ok && text != "" {
		select {
		case events <- TextDeltaEvent{Content: text}:
		case <-ctx.Done():
			return
		}
	}

	// tool_calls：累积而非 emit。
	//
	// 兼容层行为不能想当然——同一家厂商的两种端点格式并不一致：
	// - DashScope OpenAI 兼容端点：output.choices[0].message.tool_calls
	// - 部分兼容实现 / 私有化部署：output.tool_calls（扁平化）
	// 两条路径都查，取先命中的那条。原来只认 output.tool_calls，
	// 走原生 choices 结构时会静默丢掉整个工具调用。
	for _, tc := range extractDashScopeToolCalls(output) {
		call, ok := tc.(map[string]interface{})
		if !ok {
			continue
		}
		idx := 0
		if f, ok := call["index"].(float64); ok {
			idx = int(f)
		}
		id, _ := call["id"].(string)
		if function, ok := call["function"].(map[string]interface{}); ok {
			name, _ := function["name"].(string)
			args, _ := function["arguments"].(string)
			pendingArgs[idx] += args
			// 空字符串不等于"有值"：该兼容层后续帧会显式下发 name="" ，
			// 若不加判断直接用空值覆盖，首帧拿到的真实工具名会被抹掉。
			if name != "" {
				pendingNames[idx] = name
			}
			if id != "" {
				pendingIDs[idx] = id
			}
		} else if args, ok := call["arguments"].(string); ok {
			// 无 function 包裹的扁平格式
			pendingArgs[idx] += args
			if id != "" {
				pendingIDs[idx] = id
			}
		}
	}
}

// extractDashScopeToolCalls 从 DashScope output 中提取 tool_calls 数组，兼容两种结构：
// 1. output.choices[0].message.tool_calls（原生 DashScope / OpenAI 兼容端点）
// 2. output.tool_calls（部分兼容层扁平化格式）
//
// 返回空切片表示本帧无工具调用。
func extractDashScopeToolCalls(output map[string]interface{}) []interface{} {
	if toolCalls, ok := output["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
		return toolCalls
	}
	choices, ok := output["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil
	}
	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return nil
	}
	if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
		return toolCalls
	}
	return nil
}

// parseDashScopeUsage 解析 DashScope result.done 事件中的 usage 字段并推送 UsageEvent。
// estimatedInputTokens 透传给 RecordRemoteUsage 校准 EMA offset（B-P1 EMA 修复）。
func (a *DashScopeNativeAdapter) parseDashScopeUsage(data string, events chan<- Event, ctx context.Context, ts TokenSource, model string, estimatedInputTokens int) {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return
	}

	usage, ok := obj["usage"].(map[string]interface{})
	if !ok {
		return
	}

	inputTokens, _ := usage["input_tokens"].(float64)
	outputTokens, _ := usage["output_tokens"].(float64)

	usageEvt := UsageEvent{
		InputTokens:   int(inputTokens),
		OutputTokens:  int(outputTokens),
		CacheCreation: 0,
		CacheRead:     0,
	}

	// T1-B：同步调用 RecordRemoteUsage
	// B-P1 EMA 修复：透传 estimatedInputTokens 校准 EMA offset（旧实现硬编码 0 导致 offset 校准被跳过）。
	if ts != nil {
		actualUsage := UnifiedUsage{
			InputTokens:   usageEvt.InputTokens,
			OutputTokens:  usageEvt.OutputTokens,
			CacheCreation: usageEvt.CacheCreation,
			CacheRead:     usageEvt.CacheRead,
		}
		ts.RecordRemoteUsage(model, actualUsage, estimatedInputTokens)
	}

	select {
	case events <- usageEvt:
	case <-ctx.Done():
	}
}

// ============================================================
// MapError — DashScope 错误码映射
// ============================================================

// mapDashScopeError 映射 DashScope API 错误响应到 *APIError（§5.3 S5）。
// DashScope 错误响应格式：{code: string, message: string, request_id: string}
// code 值（§6.2）：
// - InvalidApiKey → ErrSecretMissing
// - Throttling.RateQuota → ErrAIApiRateLimit
// - Throttling.AlreadyQuota → ErrAIApiRateLimit
// - RequestEntityTooLarge → ErrAIFatal
// - 兜底 → ErrAIFatal
func (a *DashScopeNativeAdapter) mapDashScopeError(resp *http.Response) *APIError {
	// 先保存 body 字节（ParseAPIError 会消费 resp.Body），再恢复给 ParseAPIError 使用
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	apiErr := ParseAPIError(resp)
	if apiErr == nil {
		apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
	}

	// DashScope 特有错误码按 code 字段映射
	var dashscopeErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(bodyBytes, &dashscopeErr) == nil && dashscopeErr.Code != "" {
		switch dashscopeErr.Code {
		case "InvalidApiKey":
			apiErr.ErrorID = ErrSecretMissing
		case "Throttling.RateQuota", "Throttling.AlreadyQuota":
			apiErr.ErrorID = ErrAIApiRateLimit
		case "RequestEntityTooLarge":
			apiErr.ErrorID = ErrAIFatal
		}
		apiErr.Code = dashscopeErr.Code
	}

	return apiErr
}

// ============================================================
// InjectPrivateFields — DashScope 私有字段注入
// ============================================================

// injectDashScopePrivateFields 注入 DashScope 私有字段 enable_thinking / thinking_budget（§5.3 S5）。
// 检查 cap.SupportsThinking，true 且 req.Thinking.Enabled 时注入 parameters.enable_thinking=true + parameters.thinking_budget。
// 不支持时静默跳过（§5.4 降级策略）。
//
// 注：当前 buildDashScopeBody 已内部处理 enable_thinking 注入，此处为 no-op（与 Anthropic/Gemini 模式一致）。
// 保留方法签名以满足 ProviderAdapter interface 契约。
func (a *DashScopeNativeAdapter) injectDashScopePrivateFields(body map[string]interface{}, req Request, cap ModelCapability) {
	// no-op：enable_thinking 已由 buildDashScopeBody 内部注入。
	// 保留方法签名以满足 ProviderAdapter interface 契约。
}
