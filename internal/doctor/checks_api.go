package doctor

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// checkAPI 测试 API 连通性（1 项）。
// 复用 main.go:741-786 checkAPI 的 Stream 调用逻辑 + main.go:790-875 printAPIErrorHelp 的错误分类。
// 不自实现 HTTP 调用，仅复用 aiClient.Stream()。
// API key 缺失或 aiClient 为 nil 时返回 fatal，不 panic（对标改进方案 §4.1 风险控制）。
func (d *Doctor) checkAPI(ctx context.Context) []CheckResult {
	var results []CheckResult

	// aiClient 为 nil 时标记 fatal（不 panic）
	if d.aiClient == nil {
		results = append(results, CheckResult{
			Category:   "API",
			Name:       "AI Client",
			Severity:   SeverityFatal,
			Message:    "not initialized",
			FixSuggest: "check API key in environment or settings.json",
		})
		return results
	}

	// API key 缺失时跳过实际请求（避免无意义失败）
	// P5 修复：环境变量 + settings.json api_key 均空时才跳过
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	settingsKey := ""
	if d.cfg != nil {
		settingsKey = d.cfg.APIKey
	}
	if anthropicKey == "" && openaiKey == "" && settingsKey == "" {
		results = append(results, CheckResult{
			Category:   "API",
			Name:       "Connectivity",
			Severity:   SeverityFatal,
			Message:    "skipped (API key not set)",
			FixSuggest: "set ANTHROPIC_API_KEY or OPENAI_API_KEY env var, or api_key in settings.json",
		})
		return results
	}

	// Model 缺失时跳过实际请求
	model := ""
	if d.cfg != nil {
		model = d.cfg.Model
	}
	if model == "" {
		results = append(results, CheckResult{
			Category:   "API",
			Name:       "Connectivity",
			Severity:   SeverityFatal,
			Message:    "skipped (model not set)",
			FixSuggest: "set model in settings.json or WHALE_MODEL environment variable",
		})
		return results
	}

	// 最小请求测试（对标 main.go:769-785）
	req := ai.Request{
		SystemPrompt: []string{"Reply with exactly: OK"},
		MaxTokens:    10,
		Model:        model,
	}

	events, err := d.aiClient.Stream(ctx, req)
	if err != nil {
		results = append(results, CheckResult{
			Category:   "API",
			Name:       "Connectivity",
			Severity:   SeverityError,
			Message:    redactAPIKey(err.Error()),
			Detail:     classifyAPIError(err),
			FixSuggest: suggestAPIFix(err),
		})
		return results
	}

	// 消费事件流（对标 main.go:787-810）
	received := false
	var apiErr error
	for evt := range events {
		if _, ok := evt.(ai.TextDeltaEvent); ok {
			received = true
		}
		if errEvt, ok := evt.(ai.ErrorEvent); ok {
			apiErr = errEvt.Err
		}
	}

	if apiErr != nil {
		results = append(results, CheckResult{
			Category:   "API",
			Name:       "Connectivity",
			Severity:   SeverityError,
			Message:    redactAPIKey(apiErr.Error()),
			Detail:     classifyAPIError(apiErr),
			FixSuggest: suggestAPIFix(apiErr),
		})
		return results
	}

	if received {
		results = append(results, CheckResult{
			Category: "API",
			Name:     "Connectivity",
			Severity: SeverityOK,
			Message:  "API responded successfully",
		})
	} else {
		results = append(results, CheckResult{
			Category:   "API",
			Name:       "Connectivity",
			Severity:   SeverityWarn,
			Message:    "connected but no response received",
			FixSuggest: "the API might be slow or the model name might be wrong",
		})
	}
	return results
}

// classifyAPIError 对 API 错误进行分类。
// 复用 main.go:814-898 printAPIErrorHelp 的字符串匹配逻辑（403/401/429/timeout/DNS/connection refused/model overloaded）。
// 返回错误类别字符串（如 "403 Forbidden"、"401 Unauthorized" 等）。
func classifyAPIError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "403") || strings.Contains(msg, "AllocationQuota"):
		return "403 Forbidden (quota or permission issue)"
	case strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized") || strings.Contains(msg, "invalid api key"):
		return "401 Unauthorized (invalid or expired API key)"
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "Rate limit"):
		return "429 Rate Limited"
	case strings.Contains(msg, "context canceled"):
		return "Request canceled (timeout or interruption)"
	case strings.Contains(msg, "context deadline exceeded"):
		return "Timeout (30s exceeded)"
	case strings.Contains(msg, "lookup ") || strings.Contains(msg, "no such host"):
		return "DNS resolution failed"
	case strings.Contains(msg, "connection refused"):
		return "Connection refused"
	case strings.Contains(msg, "timeout"):
		return "Connection timeout"
	case strings.Contains(msg, "model overloaded") || strings.Contains(msg, "529"):
		return "Model overloaded (529)"
	default:
		return "Unknown error"
	}
}

// suggestAPIFix 根据 API 错误类型提供修复建议。
// 复用 main.go:814-898 printAPIErrorHelp 的建议逻辑。
func suggestAPIFix(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "403") || strings.Contains(msg, "AllocationQuota"):
		return "enable paid access in provider console or switch to a model with available quota"
	case strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized") || strings.Contains(msg, "invalid api key"):
		return "check API key is correct, regenerate in provider dashboard, ensure key matches provider"
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "Rate limit"):
		return "wait and retry, reduce request frequency, or upgrade API plan"
	case strings.Contains(msg, "context canceled"):
		return "check internet connection, set HTTP_PROXY/HTTPS_PROXY if behind proxy"
	case strings.Contains(msg, "context deadline exceeded"):
		return "check internet connection, API server may be slow, set HTTP_PROXY if behind proxy"
	case strings.Contains(msg, "lookup ") || strings.Contains(msg, "no such host"):
		return "check internet connection, set HTTP_PROXY/HTTPS_PROXY if behind proxy"
	case strings.Contains(msg, "connection refused"):
		return "check OPENAI_BASE_URL is correct, server may be down"
	case strings.Contains(msg, "timeout"):
		return "check internet connection, API server may be slow or blocked"
	case strings.Contains(msg, "model overloaded") || strings.Contains(msg, "529"):
		return "wait and retry, or try fallback model"
	default:
		return "check internet connection, verify API key and base URL, try a different model"
	}
}

// redactAPIKey 对字符串中的 API Key 进行脱敏，防止在诊断输出中泄露。
// 使用正则匹配 sk- 前缀的 key（如 Anthropic/OpenAI 格式），替换为 sk-***。
// 同时处理 Bearer token 格式（Authorization: Bearer sk-...）。
func redactAPIKey(s string) string {
	if s == "" {
		return s
	}
	re := regexp.MustCompile(`sk-[a-zA-Z0-9-_]+`)
	return re.ReplaceAllString(s, "sk-***")
}
