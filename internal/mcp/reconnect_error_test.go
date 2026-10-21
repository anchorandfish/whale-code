// Package mcp — / M17-03 WS 重连错误分类测试
//
// 测试覆盖：
// - classifyReconnectError：HTTP 401/403 / WWW-Authenticate invalid_token / net.Error /
// websocket.CloseError 各 code / 错误消息模式 / 主动关闭
// - ReconnectError 类型：Error() 消息格式 / Unwrap() / errors.Is / errors.As
//
// 对标 gRPC status codes（[grpc.io/docs/guides/status-codes](https://grpc.io/docs/guides/status-codes/) @2026-07-21）。
package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestClassifyReconnectError_Closed 测试主动关闭返回 ReconnectErrorClosed。
func TestClassifyReconnectError_Closed(t *testing.T) {
	result := classifyReconnectError(fmt.Errorf("some error"), true, nil)
	if result != ReconnectErrorClosed {
		t.Errorf("classifyReconnectError(closed=true) = %v, want ReconnectErrorClosed", result)
	}
}

// TestClassifyReconnectError_Auth 测试认证错误分类（HTTP 401/403 + WWW-Authenticate invalid_token）。
func TestClassifyReconnectError_Auth(t *testing.T) {
	t.Run("HTTP 401", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusUnauthorized}
		result := classifyReconnectError(fmt.Errorf("401 unauthorized"), false, resp)
		if result != ReconnectErrorAuth {
			t.Errorf("HTTP 401: got %v, want ReconnectErrorAuth", result)
		}
	})
	t.Run("HTTP 403", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusForbidden}
		result := classifyReconnectError(fmt.Errorf("403 forbidden"), false, resp)
		if result != ReconnectErrorAuth {
			t.Errorf("HTTP 403: got %v, want ReconnectErrorAuth", result)
		}
	})
	t.Run("WWW-Authenticate invalid_token", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{},
		}
		resp.Header.Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="token expired"`)
		result := classifyReconnectError(fmt.Errorf("401"), false, resp)
		if result != ReconnectErrorAuth {
			t.Errorf("WWW-Authenticate invalid_token: got %v, want ReconnectErrorAuth", result)
		}
	})
}

// TestClassifyReconnectError_Server 测试服务端错误分类（HTTP 5xx）。
func TestClassifyReconnectError_Server(t *testing.T) {
	t.Run("HTTP 500", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusInternalServerError}
		result := classifyReconnectError(fmt.Errorf("500"), false, resp)
		if result != ReconnectErrorServer {
			t.Errorf("HTTP 500: got %v, want ReconnectErrorServer", result)
		}
	})
	t.Run("HTTP 503", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusServiceUnavailable}
		result := classifyReconnectError(fmt.Errorf("503"), false, resp)
		if result != ReconnectErrorServer {
			t.Errorf("HTTP 503: got %v, want ReconnectErrorServer", result)
		}
	})
}

// TestClassifyReconnectError_WebSocketClose 测试 websocket.CloseError 各 code 分类。
func TestClassifyReconnectError_WebSocketClose(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		expected ReconnectErrorType
	}{
		{"normal closure 1000", websocket.CloseNormalClosure, ReconnectErrorClosed},
		{"going away 1001", websocket.CloseGoingAway, ReconnectErrorClosed},
		{"internal server error 1011", websocket.CloseInternalServerErr, ReconnectErrorServer},
		{"abnormal closure 1006", 1006, ReconnectErrorNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wsErr := &websocket.CloseError{Code: tt.code, Text: "test"}
			result := classifyReconnectError(wsErr, false, nil)
			if result != tt.expected {
				t.Errorf("CloseError code %d: got %v, want %v", tt.code, result, tt.expected)
			}
		})
	}
}

// TestClassifyReconnectError_NetworkMessages 测试错误消息模式匹配网络错误。
func TestClassifyReconnectError_NetworkMessages(t *testing.T) {
	patterns := []string{
		"connection refused",
		"no such host",
		"dns resolution failed",
		"operation timeout",
		"broken pipe",
		"unexpected EOF",
	}
	for _, msg := range patterns {
		t.Run(msg, func(t *testing.T) {
			result := classifyReconnectError(fmt.Errorf("%s", msg), false, nil)
			if result != ReconnectErrorNetwork {
				t.Errorf("Error message %q: got %v, want ReconnectErrorNetwork", msg, result)
			}
		})
	}
}

// TestClassifyReconnectError_NilError 测试 err=nil + resp=nil 默认返回 ReconnectErrorServer。
func TestClassifyReconnectError_NilError(t *testing.T) {
	result := classifyReconnectError(nil, false, nil)
	if result != ReconnectErrorServer {
		t.Errorf("nil error + nil resp: got %v, want ReconnectErrorServer (default)", result)
	}
}

// TestClassifyReconnectError_NetTimeout 测试 net.Error Timeout 接口分类。
func TestClassifyReconnectError_NetTimeout(t *testing.T) {
	// 模拟 net.Error Timeout
	timeoutErr := &timeoutError{msg: "i/o timeout"}
	result := classifyReconnectError(timeoutErr, false, nil)
	if result != ReconnectErrorNetwork {
		t.Errorf("net.Error Timeout: got %v, want ReconnectErrorNetwork", result)
	}
}

// timeoutError 实现 net.Error 的 Timeout() bool 接口（用于测试）
type timeoutError struct {
	msg string
}

func (e *timeoutError) Error() string   { return e.msg }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return false }

// TestReconnectError_Error 测试 ReconnectError.Error() 消息格式。
func TestReconnectError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *ReconnectError
		contains string
	}{
		{"auth", &ReconnectError{Type: ReconnectErrorAuth, Cause: fmt.Errorf("401"), Attempt: 0}, "auth"},
		{"network", &ReconnectError{Type: ReconnectErrorNetwork, Cause: fmt.Errorf("timeout"), Attempt: 2}, "network"},
		{"server", &ReconnectError{Type: ReconnectErrorServer, Cause: fmt.Errorf("500"), Attempt: 1}, "server"},
		{"closed", &ReconnectError{Type: ReconnectErrorClosed, Cause: fmt.Errorf("closed"), Attempt: 0}, "closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			if !strings.Contains(msg, tt.contains) {
				t.Errorf("Error() = %q, want substring %q", msg, tt.contains)
			}
			if !strings.Contains(msg, "attempt=") {
				t.Errorf("Error() = %q, missing attempt= field", msg)
			}
		})
	}
}

// TestReconnectError_Unwrap 测试 Unwrap() 返回原始错误。
func TestReconnectError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("original error")
	err := &ReconnectError{Type: ReconnectErrorNetwork, Cause: cause, Attempt: 3}
	if !errors.Is(err, cause) {
		t.Error("errors.Is should match cause via Unwrap")
	}
}

// TestReconnectError_ErrorsAs 测试 errors.As 提取 ReconnectError 类型。
func TestReconnectError_ErrorsAs(t *testing.T) {
	cause := fmt.Errorf("dial failed")
	err := &ReconnectError{Type: ReconnectErrorAuth, Cause: cause, Attempt: 0}
	wrapped := fmt.Errorf("wrapper: %w", err)

	var rcErr *ReconnectError
	if !errors.As(wrapped, &rcErr) {
		t.Fatal("errors.As failed to extract ReconnectError")
	}
	if rcErr.Type != ReconnectErrorAuth {
		t.Errorf("Extracted Type = %v, want ReconnectErrorAuth", rcErr.Type)
	}
	if rcErr.Attempt != 0 {
		t.Errorf("Extracted Attempt = %d, want 0", rcErr.Attempt)
	}
}

// TestReconnectErrorType_Values 测试枚举值顺序（Auth=0, Network=1, Server=2, Closed=3）。
func TestReconnectErrorType_Values(t *testing.T) {
	if ReconnectErrorAuth != 0 {
		t.Errorf("ReconnectErrorAuth = %d, want 0", ReconnectErrorAuth)
	}
	if ReconnectErrorNetwork != 1 {
		t.Errorf("ReconnectErrorNetwork = %d, want 1", ReconnectErrorNetwork)
	}
	if ReconnectErrorServer != 2 {
		t.Errorf("ReconnectErrorServer = %d, want 2", ReconnectErrorServer)
	}
	if ReconnectErrorClosed != 3 {
		t.Errorf("ReconnectErrorClosed = %d, want 3", ReconnectErrorClosed)
	}
}
