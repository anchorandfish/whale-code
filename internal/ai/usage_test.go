package ai

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestUsageTrackerRecordUsage 测试记录 API 用量
func TestUsageTrackerRecordUsage(t *testing.T) {
	tracker := NewUsageTracker("")

	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 0, 0)

	input, output, _, _ := tracker.GetSnapshot()
	if input != 1000 {
		t.Errorf("Expected input=1000, got %d", input)
	}
	if output != 500 {
		t.Errorf("Expected output=500, got %d", output)
	}
}

// TestUsageTrackerCacheTokenTracking 测试 Cache Token 追踪
func TestUsageTrackerCacheTokenTracking(t *testing.T) {
	tracker := NewUsageTracker("")

	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 200, 100)

	_, _, cacheCreation, cacheRead := tracker.GetSnapshot()
	if cacheCreation != 200 {
		t.Errorf("Expected cache_creation=200, got %d", cacheCreation)
	}
	if cacheRead != 100 {
		t.Errorf("Expected cache_read=100, got %d", cacheRead)
	}
}

// TestUsageTrackerPersistence test persistence
func TestUsageTrackerPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "usage.json")

	tracker := NewUsageTracker(storePath)
	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 0, 0)

	if err := tracker.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := LoadUsageTracker(storePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	input, output, _, _ := loaded.GetSnapshot()
	if input != 1000 {
		t.Errorf("Expected loaded input=1000, got %d", input)
	}
	if output != 500 {
		t.Errorf("Expected loaded output=500, got %d", output)
	}
}

// TestUsageTrackerModelUsage 测试按模型统计
func TestUsageTrackerModelUsage(t *testing.T) {
	tracker := NewUsageTracker("")

	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 0, 0)
	tracker.RecordUsage("gpt-4o", 2000, 1000, 0, 0)

	if len(tracker.ModelUsage) != 2 {
		t.Errorf("Expected 2 models, got %d", len(tracker.ModelUsage))
	}

	if tracker.ModelUsage["claude-sonnet-4-20250514"].APICalls != 1 {
		t.Errorf("Expected 1 API call for claude, got %d", tracker.ModelUsage["claude-sonnet-4-20250514"].APICalls)
	}
}

// TestUsageTrackerFormatReportNoLocalCost 报告不得包含本地估算费用：
// provider 不返回费用详情，本地价格表估算与真实账单不符，会误导用户
// （用户实测：/usage 的 Cost 与实际供应商计费对不上）。
// 同时验证 token 数显示为精确带逗号格式（12461 → "12,461"），
// 与 /context 的 formatIntWithComma 口径一致，避免 "12.5k vs 12,461 对不上" 的误解。
func TestUsageTrackerFormatReportNoLocalCost(t *testing.T) {
	tracker := NewUsageTracker("")
	tracker.RecordUsage("claude-sonnet-4-20250514", 12461, 72, 0, 0)

	report := tracker.FormatUsageReport()
	if strings.Contains(report, "Cost") {
		t.Errorf("报告不应包含本地估算费用 Cost 行, got:\n%s", report)
	}
	if !strings.Contains(report, "Input: 12,461") {
		t.Errorf("Input 应显示精确带逗号值 12,461, got:\n%s", report)
	}
	if strings.Contains(report, "12.5k") {
		t.Errorf("Input 不应显示缩略值 12.5k, got:\n%s", report)
	}
}

// TestUsageTrackerFormatReport 测试格式化报告
func TestUsageTrackerFormatReport(t *testing.T) {
	tracker := NewUsageTracker("")

	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 200, 100)

	report := tracker.FormatUsageReport()
	if !strings.Contains(report, "Token Usage") {
		t.Error("Expected report to contain 'Token Usage'")
	}
	if !strings.Contains(report, "Input") {
		t.Error("Expected report to contain 'Input'")
	}
	if !strings.Contains(report, "Cache Create") {
		t.Error("Expected report to contain 'Cache Create'")
	}
	if !strings.Contains(report, "Cache Read") {
		t.Error("Expected report to contain 'Cache Read'")
	}
}

// TestUsageTrackerReset 测试重置
func TestUsageTrackerReset(t *testing.T) {
	tracker := NewUsageTracker("")
	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 0, 0)

	tracker.Reset()

	input, output, _, _ := tracker.GetSnapshot()
	if input != 0 || output != 0 {
		t.Errorf("Expected reset to zero, got input=%d output=%d", input, output)
	}
}

// TestUsageTrackerLoadNonexistent 测试加载不存在的文件
func TestUsageTrackerLoadNonexistent(t *testing.T) {
	tracker, err := LoadUsageTracker("/nonexistent/path/usage.json")
	if err != nil {
		t.Fatalf("Should not error on nonexistent file: %v", err)
	}
	if tracker == nil {
		t.Fatal("Expected non-nil tracker")
	}
}

// TestUsageTrackerMultipleRecords 测试多次记录累加
func TestUsageTrackerMultipleRecords(t *testing.T) {
	tracker := NewUsageTracker("")

	tracker.RecordUsage("claude-sonnet-4-20250514", 1000, 500, 0, 0)
	tracker.RecordUsage("claude-sonnet-4-20250514", 2000, 1000, 0, 0)

	input, output, _, _ := tracker.GetSnapshot()
	if input != 3000 {
		t.Errorf("Expected input=3000, got %d", input)
	}
	if output != 1500 {
		t.Errorf("Expected output=1500, got %d", output)
	}
	if tracker.ModelUsage["claude-sonnet-4-20250514"].APICalls != 2 {
		t.Errorf("Expected 2 API calls, got %d", tracker.ModelUsage["claude-sonnet-4-20250514"].APICalls)
	}
}

// TestUsageTrackerAPIDuration 测试 API duration记录
func TestUsageTrackerAPIDuration(t *testing.T) {
	tracker := NewUsageTracker("")
	tracker.RecordAPIDuration(1500)
	tracker.RecordAPIDuration(2000)

	if tracker.TotalAPIDurationMs != 3500 {
		t.Errorf("Expected 3500ms, got %d", tracker.TotalAPIDurationMs)
	}
}

// TestUsageTrackerConcurrentAccess 测试并发访问
func TestUsageTrackerConcurrentAccess(t *testing.T) {
	tracker := NewUsageTracker("")

	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func() {
			tracker.RecordUsage("claude-sonnet-4-20250514", 10, 5, 0, 0)
			tracker.GetSnapshot()
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	input, output, _, _ := tracker.GetSnapshot()
	if input != 1000 {
		t.Errorf("Expected input=1000, got %d", input)
	}
	if output != 500 {
		t.Errorf("Expected output=500, got %d", output)
	}
}

// TestRecursiveUsageMerge 测试子追踪器用量合并到父追踪器
func TestRecursiveUsageMerge(t *testing.T) {
	parent := NewUsageTracker("")
	child := NewUsageTracker("")

	child.RecordUsage("model-a", 1000, 500, 200, 3000)

	parent.MergeFrom(child)

	if parent.TotalInputTokens != 1000 {
		t.Errorf("Expected TotalInputTokens=1000, got %d", parent.TotalInputTokens)
	}
	if parent.TotalOutputTokens != 500 {
		t.Errorf("Expected TotalOutputTokens=500, got %d", parent.TotalOutputTokens)
	}
	if parent.TotalCacheRead != 3000 {
		t.Errorf("Expected TotalCacheRead=3000, got %d", parent.TotalCacheRead)
	}
}

// TestAgentCostAttribution 测试子proxy成本归属追踪
func TestAgentCostAttribution(t *testing.T) {
	tracker := NewUsageTracker("")

	tracker.RecordUsage("main-model", 5000, 2000, 0, 0)
	tracker.RecordAgentUsage("agent-1", "main-model", 1000, 500, 200, 3000)
	tracker.RecordAgentUsage("agent-2", "main-model", 800, 400, 100, 2000)

	// 总量should containproxy成本
	if tracker.TotalInputTokens != 6800 {
		t.Errorf("Expected TotalInputTokens=6800, got %d", tracker.TotalInputTokens)
	}

	// proxy明细应可用
	agents := tracker.GetAgentBreakdown()
	if len(agents) != 2 {
		t.Errorf("Expected 2 agents in breakdown, got %d", len(agents))
	}
}

// TestForkCostInParent 测试 Fork 成本出现在父追踪器中
func TestForkCostInParent(t *testing.T) {
	parent := NewUsageTracker("")
	fork := NewUsageTracker("")

	fork.RecordUsage("model-a", 2000, 1000, 500, 4000)

	parent.MergeAgentUsage("fork-1", fork)

	if parent.TotalInputTokens != 2000 {
		t.Errorf("Expected TotalInputTokens=2000, got %d", parent.TotalInputTokens)
	}

	// 应有proxy明细
	agents := parent.GetAgentBreakdown()
	if _, ok := agents["fork-1"]; !ok {
		t.Error("Fork agent should appear in breakdown")
	}
}

// TestMultiAgentCostAggregation 测试多proxy成本aggregation
func TestMultiAgentCostAggregation(t *testing.T) {
	parent := NewUsageTracker("")
	parent.RecordUsage("model-a", 1000, 500, 0, 0) // 主会话

	for i := 0; i < 3; i++ {
		child := NewUsageTracker("")
		child.RecordUsage("model-a", 500, 200, 100, 1000)
		parent.MergeAgentUsage(fmt.Sprintf("agent-%d", i), child)
	}

	// 主: 1000+500, proxy: 3*(500+200) = 2100
	totalExpected := 1000 + 3*500
	if parent.TotalInputTokens != totalExpected {
		t.Errorf("Expected TotalInputTokens=%d, got %d", totalExpected, parent.TotalInputTokens)
	}
}

// TestFormatRecursiveUsageReport 测试递归用量报告格式
func TestFormatRecursiveUsageReport(t *testing.T) {
	tracker := NewUsageTracker("")
	tracker.RecordUsage("model-a", 1000, 500, 0, 0)
	tracker.RecordAgentUsage("agent-1", "model-a", 500, 200, 100, 1000)

	report := tracker.FormatUsageReport()
	if !strings.Contains(report, "Agent") && !strings.Contains(report, "agent") {
		t.Error("Usage report should contain agent breakdown")
	}
}
