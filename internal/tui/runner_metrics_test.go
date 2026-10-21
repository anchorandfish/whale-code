package tui

import (
	"context"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/metrics"
)

// === processStreamEvents 桥接 metrics 测试 ===
//
// 测试策略：runnerCore.processStreamEvents 接受 <-chan ai.Event（channel），
// 用现有 eventChannel helper 构造。UsageEvent 是结构体值，直接作为 ai.Event 传入。
// metrics.Default 是包级全局变量，测试间需重置（t.Cleanup 还原）。
//
// Snapshot 返回 map[string]interface{}：
// - input_tokens_total / output_tokens_total / cache_creation_tokens / cache_read_tokens 为 int64
//
// ★ D 批：原还有一条 `total_cost`（string，本地价格表估算）。
// 已随成本线删除 —— 本文件同时断言该键**不存在**，防止它被悄悄加回来。

// TestProcessStreamEvents_BridgesMetrics 验证 UsageEvent 同步记录到 metrics.Default。
func TestProcessStreamEvents_BridgesMetrics(t *testing.T) {
	// 重置 metrics.Default
	originalDefault := metrics.Default
	metrics.Default = &metrics.Metrics{}
	t.Cleanup(func() { metrics.Default = originalDefault })

	rc := &runnerCore{
		ModelName:    "claude-sonnet-4-20250514",
		UsageTracker: ai.NewUsageTracker(""),
	}

	events := eventChannel([]ai.Event{
		ai.UsageEvent{
			InputTokens:   1000,
			OutputTokens:  500,
			CacheCreation: 200,
			CacheRead:     100,
		},
	})

	handler := &mockOutputHandler{}
	_, _, _, err := rc.processStreamEvents(context.Background(), events, handler)
	if err != nil {
		t.Fatalf("processStreamEvents 返回错误: %v", err)
	}

	// 验证 metrics.Default 记录了 4 token 字段
	snap := metrics.Default.Snapshot()
	if v, ok := snap["input_tokens_total"].(int64); !ok || v != 1000 {
		t.Errorf("metrics input_tokens_total = %v, want 1000", snap["input_tokens_total"])
	}
	if v, ok := snap["output_tokens_total"].(int64); !ok || v != 500 {
		t.Errorf("metrics output_tokens_total = %v, want 500", snap["output_tokens_total"])
	}
	if v, ok := snap["cache_creation_tokens"].(int64); !ok || v != 200 {
		t.Errorf("metrics cache_creation_tokens = %v, want 200", snap["cache_creation_tokens"])
	}
	if v, ok := snap["cache_read_tokens"].(int64); !ok || v != 100 {
		t.Errorf("metrics cache_read_tokens = %v, want 100", snap["cache_read_tokens"])
	}
	// 成本字段必须不存在（本地价格表估算已删）
	if v, ok := snap["total_cost"]; ok {
		t.Errorf("metrics 不应再有 total_cost（成本线已删），got %v", v)
	}
}

// TestProcessStreamEvents_UnknownModel_StillRecordsTokens 验证未知模型也照常记录 token
// （D 批后不再有"未知模型不计 cost"这条分支）。
func TestProcessStreamEvents_UnknownModel_StillRecordsTokens(t *testing.T) {
	originalDefault := metrics.Default
	metrics.Default = &metrics.Metrics{}
	t.Cleanup(func() { metrics.Default = originalDefault })

	rc := &runnerCore{
		ModelName:    "totally-unknown-model-xyz",
		UsageTracker: ai.NewUsageTracker(""),
	}

	events := eventChannel([]ai.Event{
		ai.UsageEvent{
			InputTokens:  100,
			OutputTokens: 50,
		},
	})

	handler := &mockOutputHandler{}
	_, _, _, err := rc.processStreamEvents(context.Background(), events, handler)
	if err != nil {
		t.Fatalf("processStreamEvents 返回错误: %v", err)
	}

	snap := metrics.Default.Snapshot()
	// token 仍记录
	if v, ok := snap["input_tokens_total"].(int64); !ok || v != 100 {
		t.Errorf("input_tokens_total = %v, want 100（token 应记录）", snap["input_tokens_total"])
	}
	if v, ok := snap["output_tokens_total"].(int64); !ok || v != 50 {
		t.Errorf("output_tokens_total = %v, want 50（token 应记录）", snap["output_tokens_total"])
	}
}
