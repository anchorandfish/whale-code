// Package ai — GeminiNativeAdapter 真实实现（§5.3，T1-A S5 落地）。
//
// 本文件实现 GeminiNativeAdapter 的 ProviderAdapter interface 4 方法：
// - BuildRequest：构造 Gemini generateContent API 请求（contents/parts/tools/generationConfig）
// - ParseStream：单独实现 JSON 数组流解析器（Gemini 返回 [{...},{...}] 非 SSE）
// - MapError：Gemini 错误码映射（error.code → ErrorID）
// - InjectPrivateFields：注入 generationConfig.thinkingConfig.thinkingBudget（检查 SupportsThinking）
//
// SNAPSHOT: Gemini 协议与 Anthropic/OpenAI 差异较大（JSON 数组流 vs SSE），不复用 SSEParser。
// usage 字段统一转换：usageMetadata.promptTokenCount/candidatesTokenCount/cachedContentTokenCount → UsageEvent
//
// REF-外部-Gemini API @官方URL https://ai.google.dev/api/generate-content（访问日期 2026-07-20）
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ============================================================
// BuildRequest — 构造 Gemini generateContent API HTTP 请求
// ============================================================

// buildGeminiRequest 构造 Gemini generateContent API 的 HTTP 请求（§5.3 S5）。
// Gemini API 端点：POST {baseURL}/v1beta/models/{model}:streamGenerateContent
// 请求体结构：{ contents: [...], tools: [...], generationConfig: {...}, systemInstruction: {...} }
// API Key 通过 x-goog-api-key header 传递（B1-P1-2 修复：不再使用 URL query 参数，
// 避免 API Key 泄漏到服务器/代理日志）。
//
// REF-外部-Gemini API @官方URL https://ai.google.dev/api/generate-content#method:-models.streamgeneratecontent
//
// 参数：
// - ctx: 请求上下文
// - req: 统一 Request 结构
// - snap: SimpleClient 字段快照
//
// 返回：构造完成的 *http.Request。
func (a *GeminiNativeAdapter) buildGeminiRequest(ctx context.Context, req Request, snap clientSnapshot) (*http.Request, error) {
	body := a.buildGeminiBody(req, snap)

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize gemini request: %w", err)
	}

	// B1-P1-2 修复：API Key 不再通过 URL query 参数传递，改用 x-goog-api-key header。
	// 原 ?key={apiKey} 会泄漏到服务器日志/代理日志/Referer 头。
	geminiURL := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent",
		strings.TrimRight(snap.baseURL, "/"),
		snap.model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// B1-P1-2 修复：通过 x-goog-api-key header 传递 API Key（Gemini 官方支持的 header 认证方式）
	httpReq.Header.Set("x-goog-api-key", snap.apiKey)

	return httpReq, nil
}

// buildGeminiBody 构造 Gemini generateContent API 请求体（§5.3 S5）。
// 将统一 Request 转换为 Gemini API 格式：
// - SystemPrompt → systemInstruction.parts[{text}]
// - Messages → contents[{role, parts[{text}]}]（user/assistant → user/model）
// - Tools → tools[{functionDeclarations[{name, description, parameters}]}]
// - Thinking → generationConfig.thinkingConfig.thinkingBudget
// - MaxTokens → generationConfig.maxOutputTokens
func (a *GeminiNativeAdapter) buildGeminiBody(req Request, snap clientSnapshot) map[string]interface{} {
	body := map[string]interface{}{}

	// systemInstruction
	if len(req.SystemPrompt) > 0 {
		parts := []map[string]interface{}{
			{"text": strings.Join(req.SystemPrompt, "\n\n")},
		}
		body["systemInstruction"] = map[string]interface{}{"parts": parts}
	}

	// contents
	contents := make([]map[string]interface{}, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}
		text := ""
		switch v := msg.Content.(type) {
		case string:
			text = v
		case []interface{}:
			for _, b := range v {
				if block, ok := b.(map[string]interface{}); ok {
					if t, ok := block["text"].(string); ok {
						text += t
					}
				}
			}
		}
		contents = append(contents, map[string]interface{}{
			"role":  role,
			"parts": []map[string]interface{}{{"text": text}},
		})
	}
	body["contents"] = contents

	// tools
	if len(req.Tools) > 0 {
		funcDecls := make([]map[string]interface{}, 0, len(req.Tools))
		for _, t := range req.Tools {
			funcDecls = append(funcDecls, map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			})
		}
		body["tools"] = []map[string]interface{}{
			{"functionDeclarations": funcDecls},
		}
	}

	// generationConfig
	genConfig := map[string]interface{}{}
	if req.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = req.MaxTokens
	}
	// thinking 三态渲染（唯一判据 ThinkingConfig.Resolve，见 client.go）：
	// On → thinkingConfig.thinkingBudget = budget；需模型确实支持（否则不下发）；
	// Off → thinkingConfig.thinkingBudget = 0 —— Gemini 语义里 0 就是「关闭思考」，
	// 这是「显式关闭」在本协议上的线值，必须能下发；
	// 未指定 → 不下发，由服务端默认决定。
	// 判据 = ThinkingSupportFor（唯一权威入口，B2：用户声明 > 端点白名单 > 静态表 >
	// 前缀推断，且「不知道」单独一档 ⇒ 不下发）。勿直接读 cap.SupportsThinking。
	// ★ A2签名加端点维：本适配器**非生产路径**（B4 定案 §0 冻结），
	// 此处只做**签名适配**，不改变"冻结"的含义（判据只能有一份签名）。
	switch req.Thinking.Resolve() {
	case ThinkingOn:
		if ThinkingSupportFor(snap.model, snap.baseURL) == ThinkingCapSupported {
			budget := req.Thinking.BudgetTokens
			if budget <= 0 {
				budget = 10000
			}
			genConfig["thinkingConfig"] = map[string]interface{}{
				"thinkingBudget": budget,
			}
		}
	case ThinkingOff:
		genConfig["thinkingConfig"] = map[string]interface{}{
			"thinkingBudget": 0,
		}
	}
	if len(genConfig) > 0 {
		body["generationConfig"] = genConfig
	}

	return body
}

// ============================================================
// ParseStream — Gemini JSON 数组流解析器（非 SSE）
// ============================================================

// parseGeminiStream 解析 Gemini JSON 数组流并转为统一 Event 通道（§5.3 S5）。
// Gemini streamGenerateContent 返回 JSON 数组流（[{...},{...}]），每个数组元素是一个完整 JSON 对象：
// - candidates[0].content.parts[0].text → TextDeltaEvent
// - candidates[0].content.parts[0].thought → ThinkingDeltaEvent（thinking 模式）
// - candidates[0].content.parts[0].functionCall → ToolUseEvent
// - usageMetadata → UsageEvent
//
// 解析策略：使用 bufio.Reader 逐字符读取，按 JSON 对象边界分割（顶层 [ ] 数组内多个 {}）。
// 不复用 SSEParser（Gemini 不返回 SSE 格式）。
//
// REF-外部-Gemini streaming @官方URL https://ai.google.dev/api/generate-content#method:-models.streamgeneratecontent
func (a *GeminiNativeAdapter) parseGeminiStream(resp *http.Response, estimatedInputTokens int) (<-chan Event, error) {
	events := make(chan Event, 64)
	reqCtx := resp.Request.Context()

	go func() {
		defer resp.Body.Close()
		defer close(events)

		// Gemini JSON 数组流解析：逐字符读取，按 { } 嵌套深度分割 JSON 对象
		reader := bufio.NewReader(resp.Body)
		var buf strings.Builder
		depth := 0
		// 修复 P0-1：增加字符串字面量状态跟踪，避免字符串内的 { } 影响 depth 计数。
		// 当 Gemini 响应文本含花括号（如代码片段 "function() { return 1; }"）时，
		// 原逻辑会错误地增减 depth，导致流解析断裂。此处通过 inString/escape 状态
		// 跳过字符串字面量内的花括号。
		inString := false
		escape := false

		for {
			select {
			case <-reqCtx.Done():
				return
			default:
			}

			r, _, err := reader.ReadRune()
			if err != nil {
				if err != io.EOF {
					select {
					case events <- ErrorEvent{Err: fmt.Errorf("gemini stream read error: %w", err)}:
					case <-reqCtx.Done():
					}
				}
				return
			}

			// 修复 P0-1：处理字符串字面量内的转义字符和引号
			if escape {
				escape = false
				if depth > 0 {
					buf.WriteRune(r)
				}
				continue
			}
			if r == '\\' {
				escape = true
				if depth > 0 {
					buf.WriteRune(r)
				}
				continue
			}
			if r == '"' {
				inString = !inString
				if depth > 0 {
					buf.WriteRune(r)
				}
				continue
			}
			if inString {
				if depth > 0 {
					buf.WriteRune(r)
				}
				continue
			}

			switch r {
			case '{':
				depth++
				buf.WriteRune(r)
			case '}':
				buf.WriteRune(r)
				depth--
				if depth == 0 {
					// 完整 JSON 对象读取完毕，解析并转换
					objStr := buf.String()
					buf.Reset()
					// B-P4：透传 model 与 estimatedInputTokens（EMA 校准回流）
					if err := a.parseGeminiObject(objStr, events, reqCtx, a.Model(), estimatedInputTokens); err != nil {
						return
					}
				}
			default:
				if depth > 0 {
					buf.WriteRune(r)
				}
				// depth == 0 时跳过 [ ] , 空白等非对象字符
			}
		}
	}()

	return events, nil
}

// parseGeminiObject 解析单个 Gemini JSON 对象并推送事件到 events 通道。
// 提取 candidates[0].content.parts[0] 的 text/thought/functionCall 与 usageMetadata。
//
// B-P4 审查修复：解析到 usageMetadata 时同步调用 tokenSource.RecordRemoteUsage
// 回流 EMA 校准样本（与 Anthropic/OpenAI/DashScope 适配器对齐）——此前 Gemini
// 路径从不回流，校准链路断裂（/context 永远 no samples）。
// 口径：Gemini 的 promptTokenCount 已含 cached content（cachedContentTokenCount
// 为其子集，类似 OpenAI cached_tokens），actual 直接用 InputTokens，不并回 CacheRead。
func (a *GeminiNativeAdapter) parseGeminiObject(objStr string, events chan<- Event, ctx context.Context, model string, estimatedInputTokens int) error {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(objStr), &obj); err != nil {
		// 跳过无法解析的对象（可能是部分片段）
		return nil
	}

	// 提取 usageMetadata
	if usage, ok := obj["usageMetadata"].(map[string]interface{}); ok {
		inputTokens, _ := usage["promptTokenCount"].(float64)
		outputTokens, _ := usage["candidatesTokenCount"].(float64)
		cacheRead, _ := usage["cachedContentTokenCount"].(float64)
		// B-P4：EMA 校准回流（estimated 由 streamViaAdapter 从 req.EstimatedInputTokens 传入）
		if ts := a.TokenSource(); ts != nil && estimatedInputTokens > 0 {
			ts.RecordRemoteUsage(model, UnifiedUsage{
				InputTokens:   int(inputTokens),
				OutputTokens:  int(outputTokens),
				CacheRead:     int(cacheRead),
				CacheCreation: 0,
			}, estimatedInputTokens)
		}
		select {
		case events <- UsageEvent{
			InputTokens:   int(inputTokens),
			OutputTokens:  int(outputTokens),
			CacheRead:     int(cacheRead),
			CacheCreation: 0,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 提取 candidates[0].content.parts
	candidates, ok := obj["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return nil
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return nil
	}
	parts, ok := content["parts"].([]interface{})
	if !ok {
		return nil
	}

	for _, p := range parts {
		part, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		// functionCall → ToolUseEvent
		if fc, ok := part["functionCall"].(map[string]interface{}); ok {
			name, _ := fc["name"].(string)
			args, _ := fc["args"]
			argsJSON := "{}"
			if args != nil {
				if b, err := json.Marshal(args); err == nil {
					argsJSON = string(b)
				}
			}
			select {
			case events <- ToolUseEvent{Name: name, Input: argsJSON}:
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// thought → ThinkingDeltaEvent
		if thought, ok := part["thought"].(bool); ok && thought {
			if text, ok := part["text"].(string); ok && text != "" {
				select {
				case events <- ThinkingDeltaEvent{Content: text}:
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
		}

		// text → TextDeltaEvent
		if text, ok := part["text"].(string); ok && text != "" {
			select {
			case events <- TextDeltaEvent{Content: text}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// 检查 finishReason
	if reason, ok := candidate["finishReason"].(string); ok && reason == "STOP" {
		select {
		case events <- MessageStopEvent{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// ============================================================
// MapError — Gemini 错误码映射
// ============================================================

// mapGeminiError 映射 Gemini API 错误响应到 *APIError（§5.3 S5）。
// Gemini 错误响应格式：{error: {code: int, message: string, status: string}}
// status 值：INVALID_ARGUMENT / UNAUTHENTICATED / PERMISSION_DENIED / RESOURCE_EXHAUSTED / INTERNAL / UNAVAILABLE
//
// 映射规则（§6.2）：
// - 400 INVALID_ARGUMENT → ErrConfigInvalid
// - 401 UNAUTHENTICATED → ErrSecretMissing
// - 403 PERMISSION_DENIED → ErrSecurityBlocked
// - 429 RESOURCE_EXHAUSTED → ErrAIApiRateLimit
// - 500 INTERNAL → ErrAIApiServer
// - 503 UNAVAILABLE → ErrAIApiOverload
// - 兜底 → ErrAIFatal
func (a *GeminiNativeAdapter) mapGeminiError(resp *http.Response) *APIError {
	// 先保存 body 字节（ParseAPIError 会消费 resp.Body），再恢复给 ParseAPIError 使用
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	apiErr := ParseAPIError(resp)
	if apiErr == nil {
		apiErr = &APIError{StatusCode: resp.StatusCode, ErrorID: ErrAIFatal}
	}

	// Gemini 特有错误码按 status 字段映射（覆盖 ParseAPIError 的通用映射）
	var geminiErr struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(bodyBytes, &geminiErr) == nil && geminiErr.Error.Status != "" {
		switch geminiErr.Error.Status {
		case "INVALID_ARGUMENT":
			apiErr.ErrorID = ErrConfigInvalid
		case "UNAUTHENTICATED":
			apiErr.ErrorID = ErrSecretMissing
		case "PERMISSION_DENIED":
			apiErr.ErrorID = ErrSecurityBlocked
		case "RESOURCE_EXHAUSTED":
			apiErr.ErrorID = ErrAIApiRateLimit
		case "INTERNAL":
			apiErr.ErrorID = ErrAIApiServer
		case "UNAVAILABLE":
			apiErr.ErrorID = ErrAIApiOverload
		}
	}

	return apiErr
}

// ============================================================
// InjectPrivateFields — Gemini 私有字段注入
// ============================================================

// injectGeminiPrivateFields 注入 Gemini 私有字段 thinkingBudget（§5.3 S5）。
// 检查 cap.SupportsThinking，true 且 req.Thinking.Enabled 时注入 generationConfig.thinkingConfig.thinkingBudget。
// 不支持时静默跳过（§5.4 降级策略）。
//
// 注：当前 buildGeminiBody 已内部处理 thinkingConfig 注入，此处为 no-op（与 Anthropic 模式一致）。
// 保留方法签名以满足 ProviderAdapter interface 契约。
func (a *GeminiNativeAdapter) injectGeminiPrivateFields(body map[string]interface{}, req Request, cap ModelCapability) {
	// no-op：thinkingConfig 已由 buildGeminiBody 内部注入。
	// 保留方法签名以满足 ProviderAdapter interface 契约。
}
