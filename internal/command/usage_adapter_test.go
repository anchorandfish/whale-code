package command

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// === N2 反例/正例测试（审查 prompt §2.6-2.7）===

// TestUsageAdapter_FieldMapping 验证 ai.UsageTracker → command.UsageTrackerInterface 字段映射（审查 prompt §2.6）。
// 关键映射决策（改进方案 §3.3）：
// - CacheWriteTokens ← cacheCreation（a comparable CLI cache write = Anthropic cache creation）
// - CacheReadTokens ← cacheRead
// - Calls ← GetTotalAPICalls()
//
// ★ D 批：原先还有 `Cost ← GetTotalCost()` 一条映射，已随成本线删除。
//
// 反例：若映射写反（CacheWrite=cacheRead），stats.CacheWriteTokens 应为 200 而非 300，测试失败。
func TestUsageAdapter_FieldMapping(t *testing.T) {
	tracker := ai.NewUsageTracker("")
	// RecordUsage(model, inputTokens, outputTokens, cacheCreation, cacheRead)
	tracker.RecordUsage("claude-sonnet-4-20250514", 100, 50, 200, 300)

	adapter := NewUsageTrackerAdapter(tracker)
	stats := adapter.GetSessionStats("test-session")

	// 字段映射验证
	if stats.InputTokens != 100 {
		t.Fatalf("InputTokens mismatch: got %d, want 100", stats.InputTokens)
	}
	if stats.OutputTokens != 50 {
		t.Fatalf("OutputTokens mismatch: got %d, want 50", stats.OutputTokens)
	}
	if stats.CacheReadTokens != 300 {
		t.Fatalf("CacheReadTokens mismatch: got %d, want 300", stats.CacheReadTokens)
	}
	// 关键断言：CacheWriteTokens 必须等于 cacheCreation（200），不是 cacheRead（300）
	if stats.CacheWriteTokens != 200 {
		t.Fatalf("CacheWriteTokens should equal CacheCreation (200), got %d", stats.CacheWriteTokens)
	}
	// 全局聚合统计
	total := adapter.GetTotalStats()
	if total.Calls != 1 {
		t.Fatalf("TotalAPICalls mismatch: got %d, want 1", total.Calls)
	}
}

// TestUsageAdapter_NilTracker 反例：nil tracker 不应 panic（审查 prompt §2.7）。
// 设计决策：构造函数返回携带 nil tracker 的非 nil 适配器，方法内 nil 检查返回零值。
// 反例：若构造函数返回 nil 接口，调用 GetSessionStats 会 nil 接口方法调用 panic。
func TestUsageAdapter_NilTracker(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("adapter should not panic on nil tracker: %v", r)
		}
	}()

	var nilTracker *ai.UsageTracker = nil
	adapter := NewUsageTrackerAdapter(nilTracker)

	// 调用两个方法都不应 panic，返回零值
	stats := adapter.GetSessionStats("test")
	if stats != (SessionUsageStats{}) {
		t.Fatalf("nil tracker GetSessionStats should return zero value, got %+v", stats)
	}

	total := adapter.GetTotalStats()
	if total != (TotalUsageStats{}) {
		t.Fatalf("nil tracker GetTotalStats should return zero value, got %+v", total)
	}
}
