// Package context — findCacheBoundary 5 策略测试。
//
// 测试覆盖：
// - 策略 1: 显式 cache_control metadata 标记
// - 策略 2: RoleSystem 消息（compaction summary）
// - 策略 3: system prompt 末尾（prompt_layer=core_identity 标记）
// - 策略 4: tool defs 末尾（prompt_layer=tool_defs 标记）
// - 策略 5: 首条 RoleUser 消息兜底
// - 多策略混合 + 向后兼容
package context

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// TestFindCacheBoundary_5Strategies 测试 findCacheBoundary 5 策略顺序兜底。
//
// 验证：5 策略依次覆盖；高优先级策略命中时低优先级策略不再触发。
func TestFindCacheBoundary_5Strategies(t *testing.T) {
	compactor := NewCacheAwareCompactor(ai.NewTokenEstimator())

	// 策略 1：显式 cache_control metadata 标记
	t.Run("策略1_显式cache_control标记", func(t *testing.T) {
		messages := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "msg2"}}, Metadata: map[string]interface{}{"cache_control": true}},
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg3"}}},
		}
		boundary := compactor.findCacheBoundary(messages)
		if boundary != 2 {
			t.Errorf("策略1: boundary = %d, 期望 2（cache_control 标记位置 +1）", boundary)
		}
	})

	// 策略 2：RoleSystem 消息（compaction summary）
	t.Run("策略2_RoleSystem消息", func(t *testing.T) {
		messages := []session.Message{
			{Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "compaction summary"}}},
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "msg2"}}},
		}
		boundary := compactor.findCacheBoundary(messages)
		if boundary != 1 {
			t.Errorf("策略2: boundary = %d, 期望 1（RoleSystem 消息位置 +1）", boundary)
		}
	})

	// 策略 3：system prompt 末尾（prompt_layer=core_identity 标记）
	t.Run("策略3_prompt_layer_core_identity", func(t *testing.T) {
		messages := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "core identity"}}, Metadata: map[string]interface{}{"prompt_layer": "core_identity"}},
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
		}
		boundary := compactor.findCacheBoundary(messages)
		if boundary != 1 {
			t.Errorf("策略3: boundary = %d, 期望 1（prompt_layer=core_identity 标记位置 +1）", boundary)
		}
	})

	// 策略 4：tool defs 末尾（prompt_layer=tool_defs 标记）
	t.Run("策略4_prompt_layer_tool_defs", func(t *testing.T) {
		messages := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "core identity"}}, Metadata: map[string]interface{}{"prompt_layer": "core_identity"}},
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "tool defs"}}, Metadata: map[string]interface{}{"prompt_layer": "tool_defs"}},
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
		}
		boundary := compactor.findCacheBoundary(messages)
		// core_identity 和 tool_defs 都命中，boundary 应为最后一个 +1 = 2
		if boundary != 2 {
			t.Errorf("策略4: boundary = %d, 期望 2（prompt_layer=tool_defs 标记位置 +1）", boundary)
		}
	})

	// 策略 5：首条 RoleUser 消息兜底（无任何标记）
	t.Run("策略5_首条RoleUser兜底", func(t *testing.T) {
		messages := []session.Message{
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
			{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "msg2"}}},
			{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg3"}}},
		}
		boundary := compactor.findCacheBoundary(messages)
		if boundary != 1 {
			t.Errorf("策略5: boundary = %d, 期望 1（首条 RoleUser 消息位置 +1）", boundary)
		}
	})

	// 策略 5：空消息列表（boundary=0）
	t.Run("策略5_空消息列表", func(t *testing.T) {
		messages := []session.Message{}
		boundary := compactor.findCacheBoundary(messages)
		if boundary != 0 {
			t.Errorf("空消息列表: boundary = %d, 期望 0", boundary)
		}
	})
}

// TestFindCacheBoundary_PromptLayerMetadata 测试 prompt_layer metadata 标记识别。
//
// 验证：仅识别 "core_identity" 和 "tool_defs" 两个值；其他值不触发 boundary 更新。
func TestFindCacheBoundary_PromptLayerMetadata(t *testing.T) {
	compactor := NewCacheAwareCompactor(ai.NewTokenEstimator())

	// project_ctx 层不应触发 boundary（仅 core_identity / tool_defs 触发）
	messages := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "project"}}, Metadata: map[string]interface{}{"prompt_layer": "project_ctx"}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
	}
	boundary := compactor.findCacheBoundary(messages)
	// project_ctx 不触发策略 3/4，回退到策略 5（首条 RoleUser）
	if boundary != 1 {
		t.Errorf("project_ctx 不应触发 boundary: boundary = %d, 期望 1（策略5 兜底）", boundary)
	}

	// 非字符串 prompt_layer 值不应触发
	messages2 := []session.Message{
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "x"}}, Metadata: map[string]interface{}{"prompt_layer": 123}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "y"}}},
	}
	boundary2 := compactor.findCacheBoundary(messages2)
	if boundary2 != 1 {
		t.Errorf("非字符串 prompt_layer 不应触发: boundary = %d, 期望 1（策略5 兜底）", boundary2)
	}
}

// TestFindCacheBoundary_BackwardCompatible 测试向后兼容性。
//
// 验证：未设置 prompt_layer metadata 的消息仍走原策略 1/2/5。
func TestFindCacheBoundary_BackwardCompatible(t *testing.T) {
	compactor := NewCacheAwareCompactor(ai.NewTokenEstimator())

	// 现有调用路径（无 prompt_layer metadata，仅 cache_control metadata + RoleSystem）
	messages := []session.Message{
		{Role: session.RoleSystem, Blocks: []session.ContentBlock{{Type: "text", Text: "system"}}, Metadata: map[string]interface{}{"cache_control": true}},
		{Role: session.RoleUser, Blocks: []session.ContentBlock{{Type: "text", Text: "msg1"}}},
		{Role: session.RoleAssistant, Blocks: []session.ContentBlock{{Type: "text", Text: "msg2"}}},
	}
	boundary := compactor.findCacheBoundary(messages)
	// 策略 1 & 2 都在 msg[0] 命中，boundary = 1
	if boundary != 1 {
		t.Errorf("向后兼容: boundary = %d, 期望 1", boundary)
	}
}
