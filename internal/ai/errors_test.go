package ai

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestWithID_Wrap 验证 WithID 包装后 errors.As 能提取 *Error
func TestWithID_Wrap(t *testing.T) {
	original := fmt.Errorf("original error")
	wrapped := WithID(original, ErrAIFatal)

	var e *Error
	if !errors.As(wrapped, &e) {
		t.Error("WithID 包装后应能通过 errors.As 提取 *Error")
	}
	if e.ID != ErrAIFatal {
		t.Errorf("ErrorID = %s, 期望 %s", e.ID, ErrAIFatal)
	}
	if e.Cause != original {
		t.Error("Cause 应指向原始错误")
	}
}

// TestGetErrorID_AsChain 验证 GetErrorID 从 wrap 链提取 ErrorID
func TestGetErrorID_AsChain(t *testing.T) {
	original := fmt.Errorf("original")
	wrapped := fmt.Errorf("wrapped: %w", WithID(original, ErrAIApiOverload))

	id := GetErrorID(wrapped)
	if id != ErrAIApiOverload {
		t.Errorf("GetErrorID = %s, 期望 %s", id, ErrAIApiOverload)
	}
}

// TestError_IsRetryable 验证 529 不可重试，500 可重试
func TestError_IsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		id        ErrorID
		wantRetry bool
	}{
		{"529 overload", ErrAIApiOverload, false}, // 策略反转：529 不可重试，直接降级
		{"500 server", ErrAIApiServer, true},
		{"429 ratelimit", ErrAIApiRateLimit, false}, // 策略反转：429 不可重试，等待 RetryAfter
		{"retryable", ErrAIRetryable, true},
		{"fatal", ErrAIFatal, false},
		{"security blocked", ErrSecurityBlocked, false},
		{"secret leak", ErrSecretLeak, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WithID(fmt.Errorf("test"), tt.id)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatal("errors.As 提取失败")
			}
			if e.IsRetryable() != tt.wantRetry {
				t.Errorf("IsRetryable = %v, 期望 %v", e.IsRetryable(), tt.wantRetry)
			}
		})
	}
}

// TestWithID_RedactSecret 验证含 sk-xxx 的消息脱敏为 [REDACTED]
func TestWithID_RedactSecret(t *testing.T) {
	secretErr := fmt.Errorf("API key sk-proj-1234567890abcdef 无效")
	wrapped := WithID(secretErr, ErrAIFatal)

	var e *Error
	if !errors.As(wrapped, &e) {
		t.Fatal("errors.As 提取失败")
	}

	if strings.Contains(e.Message, "sk-proj-1234567890abcdef") {
		t.Error("消息中不应包含原始密钥，应被脱敏")
	}
	if !strings.Contains(e.Message, "[REDACTED]") {
		t.Error("消息中应包含 [REDACTED]")
	}
}
