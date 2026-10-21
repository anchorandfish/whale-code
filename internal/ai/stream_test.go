package ai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestParseAPIError_529Overload 验证 529 响应解析为 *APIError{ErrorID:ErrAIApiOverload}
func TestParseAPIError_529Overload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		w.Write([]byte(`{"error":{"type":"overloaded_error","message":"server overloaded"}}`))
	}))
	defer ts.Close()

	resp, _ := http.Get(ts.URL)
	apiErr := ParseAPIError(resp)

	if apiErr.StatusCode != 529 {
		t.Errorf("StatusCode = %d, 期望 529", apiErr.StatusCode)
	}
	if apiErr.Type != "overloaded_error" {
		t.Errorf("Type = %s, 期望 overloaded_error", apiErr.Type)
	}
	if apiErr.ErrorID != ErrAIApiOverload {
		t.Errorf("ErrorID = %s, 期望 %s", apiErr.ErrorID, ErrAIApiOverload)
	}
}

// TestParseAPIError_OpenAIPath 验证 OpenAI 路径识别 529/context overflow
// D4 修复后：500/502/504 → ErrAIRetryable，仅 503 → ErrAIApiServer
func TestParseAPIError_OpenAIPath(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		wantID ErrorID
	}{
		{"529 overloaded", 529, `{"error":{"type":"overloaded_error"}}`, ErrAIApiOverload},
		{"429 ratelimit", 429, `{"error":{"type":"rate_limit_error"}}`, ErrAIApiRateLimit},
		{"500 server", 500, `{"error":{"type":"server_error"}}`, ErrAIRetryable},
		{"503 unavailable", 503, `{"error":{"type":"service_unavailable"}}`, ErrAIApiServer},
		{"401 unauthorized", 401, `{"error":{"type":"invalid_request_error"}}`, ErrAIFatal},
		{"403 forbidden", 403, `{"error":{"type":"permission_error"}}`, ErrAIFatal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer ts.Close()

			resp, _ := http.Get(ts.URL)
			apiErr := ParseAPIError(resp)

			if apiErr.ErrorID != tt.wantID {
				t.Errorf("ErrorID = %s, 期望 %s", apiErr.ErrorID, tt.wantID)
			}
		})
	}
}

// TestMapHTTPStatusToErrorID 验证状态码到 ErrorID 的映射
// D4 修复后：500/502/504 → ErrAIRetryable，仅 503 → ErrAIApiServer
// B1 修复后：未映射状态码兜底 → ErrAIRetryable（非 ErrAIFatal）
func TestMapHTTPStatusToErrorID(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		bodyType string
		wantID   ErrorID
	}{
		{"529", 529, "", ErrAIApiOverload},
		{"429", 429, "", ErrAIApiRateLimit},
		{"500", 500, "", ErrAIRetryable},
		{"502", 502, "", ErrAIRetryable},
		{"503", 503, "", ErrAIApiServer},
		{"504", 504, "", ErrAIRetryable},
		{"401", 401, "", ErrAIFatal},
		{"403", 403, "", ErrAIFatal},
		{"413", 413, "", ErrAIFatal},
		{"overload type", 200, "overloaded_error", ErrAIApiOverload},
		{"ratelimit type", 200, "rate_limit_error", ErrAIApiRateLimit},
		{"unknown status fallback", 302, "", ErrAIRetryable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := MapHTTPStatusToErrorID(tt.status, tt.bodyType)
			if id != tt.wantID {
				t.Errorf("MapHTTPStatusToErrorID(%d, %s) = %s, 期望 %s", tt.status, tt.bodyType, id, tt.wantID)
			}
		})
	}
}

// TestIsRetryableError_ConsistentWithAPIErrorMethod 反例测试（P1 修复核心验证）。
// 验证 IsRetryableError(statusCode) 与 APIError.IsRetryable() 在 StatusCode 降级路径上行为一致。
// 修复前：IsRetryableError 对 429/529 返回 true，APIError.IsRetryable 对 429/529 返回 false（矛盾）。
// 修复后：两者对 429/529 均返回 false（策略反转统一）。
func TestIsRetryableError_ConsistentWithAPIErrorMethod(t *testing.T) {
	// 测试所有关键状态码：构造无 ErrorID 的 APIError（走 StatusCode 降级路径），
	// 验证 IsRetryableError(code) == apiErr.IsRetryable()
	statusCodes := []int{429, 529, 500, 502, 503, 504, 408, 425, 400, 401, 403, 404, 200}

	for _, code := range statusCodes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			// 构造无 ErrorID 的 APIError（走 StatusCode 降级路径）
			apiErr := &APIError{StatusCode: code}
			methodResult := apiErr.IsRetryable()
			funcResult := IsRetryableError(code)

			if methodResult != funcResult {
				t.Errorf("状态码 %d: IsRetryableError()=%v != APIError.IsRetryable()=%v\n"+
					"  原因：两个函数对 429/529 行为不一致（修复前 IsRetryableError 返回 true，方法返回 false）\n"+
					"  修复：统一为 false（策略反转，429 等待 RetryAfter，529 直接降级）",
					code, funcResult, methodResult)
			}
		})
	}
}

// TestIsRetryableError_429And529NotRetryable 反例测试（P1 修复辅助验证）。
// 直接验证 429 和 529 在修复后均返回 false。
func TestIsRetryableError_429And529NotRetryable(t *testing.T) {
	if IsRetryableError(429) {
		t.Error("IsRetryableError(429) 应为 false（策略反转：429 不可直接重试，等待 RetryAfter 专用路径）")
	}
	if IsRetryableError(529) {
		t.Error("IsRetryableError(529) 应为 false（策略反转：529 不可重试，直接降级）")
	}
}
