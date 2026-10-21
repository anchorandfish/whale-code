package ai

import (
	"errors"
	"testing"
	"time"
)

// TestCalculateBackoff_RetryAfter 验证 retry-after 优先于指数退避
func TestCalculateBackoff_RetryAfter(t *testing.T) {
	config := RetryConfig{
		BaseDelay: 1 * time.Second,
		MaxDelay:  30 * time.Second,
	}

	// retry-after = 5s 应优先返回
	result := CalculateBackoff(0, config, 5*time.Second)
	if result != 5*time.Second {
		t.Errorf("CalculateBackoff with retry-after 5s = %v, 期望 5s", result)
	}

	// retry-after = 60s 超过 MaxDelay，应返回 MaxDelay
	result = CalculateBackoff(0, config, 60*time.Second)
	if result != 30*time.Second {
		t.Errorf("CalculateBackoff with retry-after 60s = %v, 期望 30s (MaxDelay)", result)
	}

	// retry-after = 0 应使用指数退避
	result = CalculateBackoff(2, config, 0)
	expected := config.BaseDelay * 4 // 2^2 = 4
	if result < expected {
		t.Errorf("CalculateBackoff without retry-after = %v, 期望 >= %v", result, expected)
	}
}

// Test529Degradation_ErrorIDDriven 验证 529 降级基于 ErrorID 不基于字符串
func Test529Degradation_ErrorIDDriven(t *testing.T) {
	apiErr := &APIError{
		StatusCode: 529,
		ErrorID:    ErrAIApiOverload,
		Message:    "request failed",
	}

	if apiErr.ErrorID != ErrAIApiOverload {
		t.Errorf("529 响应 ErrorID = %s, 期望 %s", apiErr.ErrorID, ErrAIApiOverload)
	}

	if GetErrorID(apiErr) != ErrAIApiOverload {
		t.Errorf("GetErrorID(529) = %s, 期望 %s", GetErrorID(apiErr), ErrAIApiOverload)
	}
}

// Test529_BackgroundDrop 验证 Background 场景 529 drop 标记 ErrAIFatal
func Test529_BackgroundDrop(t *testing.T) {
	apiErr := &APIError{
		StatusCode: 529,
		ErrorID:    ErrAIApiOverload,
	}

	result := WithID(apiErr, ErrAIFatal)

	var e *Error
	if !errors.As(result, &e) {
		t.Fatal("errors.As 提取失败")
	}

	if e.ID != ErrAIFatal {
		t.Errorf("Background 529 应标记为 ErrAIFatal, 实际 = %s", e.ID)
	}
}
