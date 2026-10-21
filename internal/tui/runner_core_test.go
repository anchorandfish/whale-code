package tui

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// TestFriendlyError_529Overload 验证 529 显示错误码 + 建议动作
func TestFriendlyError_529Overload(t *testing.T) {
	err := &ai.APIError{
		StatusCode: 529,
		ErrorID:    ai.ErrAIApiOverload,
	}

	core := &runnerCore{}
	result := core.friendlyError(err)

	if !contains(result, "[AI-API-OVERLOAD]") {
		t.Errorf("529 错误消息应包含错误码 [AI-API-OVERLOAD], 实际 = %s", result)
	}
	if !contains(result, "建议") {
		t.Errorf("529 错误消息应包含建议动作, 实际 = %s", result)
	}
}

// TestFriendlyError_FallbackNetwork 验证 ErrorID 为空时降级网络错误识别
// D9 修复后：使用词边界正则匹配 "500"，友好消息为 "API server error:..."
func TestFriendlyError_FallbackNetwork(t *testing.T) {
	err := &ai.APIError{
		StatusCode: 500,
		ErrorID:    "",
	}

	core := &runnerCore{}
	result := core.friendlyError(err)

	// ErrorID 为空时降级为字符串匹配，应识别 500 并返回服务端错误友好消息
	if !contains(result, "API server error") {
		t.Errorf("无 ErrorID 时应降级为字符串匹配 500, 实际 = %s", result)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
