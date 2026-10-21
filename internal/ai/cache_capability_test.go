package ai

import "testing"

// TestProbeCacheControl_QwenReturnsFalse 测试 qwen 模型不支持 cache_control
func TestProbeCacheControl_QwenReturnsFalse(t *testing.T) {
	probe := newCacheCapabilityProbe()

	qwenModels := []string{"qwen-max", "qwen-plus", "qwen-turbo", "qwen2.5-72b-instruct"}
	for _, model := range qwenModels {
		result := probe.ProbeCacheControl(model)
		if result {
			t.Errorf("qwen model '%s' should not support cache_control", model)
		}
	}

	// test passed前缀匹配的未知 qwen 模型
	result := probe.ProbeCacheControl("qwen-some-future-model")
	if result {
		t.Error("Unknown qwen model should not support cache_control")
	}
}

// TestProbeCacheControl_ClaudeReturnsTrue 测试 claude 模型支持 cache_control
func TestProbeCacheControl_ClaudeReturnsTrue(t *testing.T) {
	probe := newCacheCapabilityProbe()

	claudeModels := []string{"claude-sonnet-4-20250514", "claude-3-5-sonnet-latest", "claude-3-opus-20240229"}
	for _, model := range claudeModels {
		result := probe.ProbeCacheControl(model)
		if !result {
			t.Errorf("claude model '%s' should support cache_control", model)
		}
	}

	// test passed前缀匹配的未知 claude 模型
	result := probe.ProbeCacheControl("claude-some-future-model")
	if !result {
		t.Error("Unknown claude model should support cache_control via pattern matching")
	}
}

// TestProbeCacheEditing_QwenReturnsFalse 测试 qwen 模型不支持 cache_editing
func TestProbeCacheEditing_QwenReturnsFalse(t *testing.T) {
	probe := newCacheCapabilityProbe()

	result := probe.ProbeCacheEditing("qwen-max")
	if result {
		t.Error("qwen model should not support cache_editing")
	}

	// claude 模型也不支持 cache_editing（尚未implementation）
	result = probe.ProbeCacheEditing("claude-sonnet-4")
	if result {
		t.Error("claude model should not support cache_editing (not yet implemented)")
	}
}

// TestGetCapability_UnknownModel_DefaultsToFalse 测试未知模型默认全部返回 false
// P0-6 (C6) 修复后：gpt-/qwen-/claude-/gemini-/deepseek- 前缀模型由 probeByPattern 识别。
// 使用不匹配任何前缀的模型名测试"真正未知"的行为。
func TestGetCapability_UnknownModel_DefaultsToFalse(t *testing.T) {
	probe := newCacheCapabilityProbe()

	cap := probe.GetCapability("some-unknown-model-xyz")
	if cap.SupportsCacheControl {
		t.Error("Unknown model should not support cache_control")
	}
	if cap.SupportsCacheEditing {
		t.Error("Unknown model should not support cache_editing")
	}
	if cap.SupportsCacheReference {
		t.Error("Unknown model should not support cache_reference")
	}
	if cap.ProviderName != "unknown" {
		t.Errorf("Unknown model provider should be 'unknown', got '%s'", cap.ProviderName)
	}
}

// TestGetCapability_QwenModel 测试 qwen 模型的完整能力信息
func TestGetCapability_QwenModel(t *testing.T) {
	probe := newCacheCapabilityProbe()

	cap := probe.GetCapability("qwen-max")
	if cap.SupportsCacheControl {
		t.Error("qwen model should not support cache_control")
	}
	if cap.SupportsCacheEditing {
		t.Error("qwen model should not support cache_editing")
	}
	if cap.SupportsCacheReference {
		t.Error("qwen model should not support cache_reference")
	}
	if cap.ProviderName != "dashscope" {
		t.Errorf("qwen model provider should be 'dashscope', got '%s'", cap.ProviderName)
	}

	// 测试 claude 模型
	cap = probe.GetCapability("claude-sonnet-4")
	if !cap.SupportsCacheControl {
		t.Error("claude model should support cache_control")
	}
	if cap.SupportsCacheEditing {
		t.Error("claude model should not support cache_editing")
	}
	if cap.ProviderName != "anthropic" {
		t.Errorf("claude model provider should be 'anthropic', got '%s'", cap.ProviderName)
	}
}
