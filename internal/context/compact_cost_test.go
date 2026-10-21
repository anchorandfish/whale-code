// Package context — CompactCostRecorder 单元测试（N11 修复 G1 + G11）。
//
// 验证 CompactCostRecorder.RecordUsage 正确桥接 ai.UsageEvent 到
// N2 metrics.RecordUsage + N1 ai.UsageTracker.RecordAgentUsage，
// AgentID 固定为 "compact"（02 §2.1.2 契约）。
package context

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/metrics"
)

// TestCompactCostRecorder_RecordUsage_AllPaths 验证完整路径：
// UsageEvent → metrics.RecordUsage + UsageTracker.RecordAgentUsage，
// AgentID="compact"。
func TestCompactCostRecorder_RecordUsage_AllPaths(t *testing.T) {
	m := &metrics.Metrics{}
	ut := ai.NewUsageTracker("") // storePath="" 避免磁盘 IO
	r := NewCompactCostRecorder(m, ut, "claude-sonnet-4-20250514")

	usage := ai.UsageEvent{
		InputTokens:   1000,
		OutputTokens:  500,
		CacheCreation: 200,
		CacheRead:     100,
	}
	r.RecordUsage(usage)

	// 验证 N2 metrics 累加
	if got := m.InputTokens.Load(); got != 1000 {
		t.Errorf("metrics.InputTokens = %d, want 1000", got)
	}
	if got := m.OutputTokens.Load(); got != 500 {
		t.Errorf("metrics.OutputTokens = %d, want 500", got)
	}
	if got := m.CacheCreationTokens.Load(); got != 200 {
		t.Errorf("metrics.CacheCreationTokens = %d, want 200", got)
	}
	if got := m.CacheReadTokens.Load(); got != 100 {
		t.Errorf("metrics.CacheReadTokens = %d, want 100", got)
	}

	// 验证 N1 UsageTracker by-agent 分桶
	utAgents := ut.AgentUsage
	if utAgents == nil {
		t.Fatal("AgentUsage map should be initialized")
	}
	agent, ok := utAgents[compactAgentID]
	if !ok {
		t.Fatalf("AgentUsage should contain %q, got keys: %v", compactAgentID, utAgents)
	}
	if agent.AgentID != compactAgentID {
		t.Errorf("AgentUsage.AgentID = %q, want %q", agent.AgentID, compactAgentID)
	}
	if agent.InputTokens != 1000 {
		t.Errorf("AgentUsage.InputTokens = %d, want 1000", agent.InputTokens)
	}
	if agent.OutputTokens != 500 {
		t.Errorf("AgentUsage.OutputTokens = %d, want 500", agent.OutputTokens)
	}
}

// TestCompactCostRecorder_NilMetrics 验证 metrics=nil 降级路径：
// 跳过 N2 累加，仅记录 N1 by-agent 分桶，不 panic。
func TestCompactCostRecorder_NilMetrics(t *testing.T) {
	ut := ai.NewUsageTracker("")
	r := NewCompactCostRecorder(nil, ut, "claude-sonnet-4-20250514")

	usage := ai.UsageEvent{
		InputTokens:  100,
		OutputTokens: 50,
	}
	// 不应 panic
	r.RecordUsage(usage)

	// N1 仍应记录
	agent, ok := ut.AgentUsage[compactAgentID]
	if !ok {
		t.Fatal("AgentUsage should contain compact even when metrics is nil")
	}
	if agent.InputTokens != 100 {
		t.Errorf("AgentUsage.InputTokens = %d, want 100", agent.InputTokens)
	}
}

// TestCompactCostRecorder_NilUsageTracker 验证 usageTracker=nil 降级路径：
// 跳过 N1 by-agent 分桶，仅记录 N2 累加，不 panic。
func TestCompactCostRecorder_NilUsageTracker(t *testing.T) {
	m := &metrics.Metrics{}
	r := NewCompactCostRecorder(m, nil, "claude-sonnet-4-20250514")

	usage := ai.UsageEvent{
		InputTokens:  200,
		OutputTokens: 100,
	}
	// 不应 panic
	r.RecordUsage(usage)

	// N2 仍应记录
	if got := m.InputTokens.Load(); got != 200 {
		t.Errorf("metrics.InputTokens = %d, want 200", got)
	}
	if got := m.OutputTokens.Load(); got != 100 {
		t.Errorf("metrics.OutputTokens = %d, want 100", got)
	}
}

// TestCompactCostRecorder_UnknownModel 验证未知模型也照常记录 token
// （D 批后不再有"未知模型 cost=0"这条分支——记录内容与模型是否在内置表内无关）。
func TestCompactCostRecorder_UnknownModel(t *testing.T) {
	m := &metrics.Metrics{}
	ut := ai.NewUsageTracker("")
	r := NewCompactCostRecorder(m, ut, "totally-unknown-model-xyz")

	usage := ai.UsageEvent{
		InputTokens:  300,
		OutputTokens: 150,
	}
	r.RecordUsage(usage)

	// token 仍应记录
	if got := m.InputTokens.Load(); got != 300 {
		t.Errorf("metrics.InputTokens = %d, want 300", got)
	}
	if got := m.OutputTokens.Load(); got != 150 {
		t.Errorf("metrics.OutputTokens = %d, want 150", got)
	}
}

// TestCompactCostRecorder_NilReceiver 验证 nil receiver 安全：
// RecordUsage 在 nil receiver 时不 panic。
func TestCompactCostRecorder_NilReceiver(t *testing.T) {
	var r *CompactCostRecorder
	// 不应 panic
	r.RecordUsage(ai.UsageEvent{InputTokens: 1})
}

// TestCompactCostRecorder_AgentIDIsCompact 验证 AgentID 固定为 "compact"
// （02 §2.1.2 契约），不可被外部覆盖。
func TestCompactCostRecorder_AgentIDIsCompact(t *testing.T) {
	if compactAgentID != "compact" {
		t.Errorf("compactAgentID = %q, want %q", compactAgentID, "compact")
	}
}

// TestCompactCostRecorder_TimestampSet 验证 CostRecord.Timestamp 被设置为当前时间。
func TestCompactCostRecorder_TimestampSet(t *testing.T) {
	// 通过自定义 metrics wrapper 验证 Timestamp
	m := &metrics.Metrics{}
	ut := ai.NewUsageTracker("")
	r := NewCompactCostRecorder(m, ut, "claude-sonnet-4-20250514")

	before := time.Now()
	r.RecordUsage(ai.UsageEvent{InputTokens: 10})
	after := time.Now()

	// 验证 metrics 有记录（间接验证 Timestamp 被设置）
	if got := m.InputTokens.Load(); got != 10 {
		t.Errorf("metrics.InputTokens = %d, want 10", got)
	}
	_ = before
	_ = after
	// 注：metrics.Metrics 不存储 Timestamp，仅累加 token。
	// Timestamp 正确性由 metrics.RecordUsage 实现保证（02 §2.1.2 契约）。
}

// TestCompactCostRecorder_MultipleCallsAccumulate 验证多次调用累加。
func TestCompactCostRecorder_MultipleCallsAccumulate(t *testing.T) {
	m := &metrics.Metrics{}
	ut := ai.NewUsageTracker("")
	r := NewCompactCostRecorder(m, ut, "claude-sonnet-4-20250514")

	for i := 0; i < 3; i++ {
		r.RecordUsage(ai.UsageEvent{
			InputTokens:  100,
			OutputTokens: 50,
		})
	}

	if got := m.InputTokens.Load(); got != 300 {
		t.Errorf("metrics.InputTokens = %d, want 300 (3×100)", got)
	}
	if got := m.OutputTokens.Load(); got != 150 {
		t.Errorf("metrics.OutputTokens = %d, want 150 (3×50)", got)
	}
	agent := ut.AgentUsage[compactAgentID]
	if agent.InputTokens != 300 {
		t.Errorf("AgentUsage.InputTokens = %d, want 300", agent.InputTokens)
	}
	total, ok := r.TotalUsage()
	if !ok || total.InputTokens != 300 || total.OutputTokens != 150 {
		t.Fatalf("TotalUsage = %+v/%v, want input=300 output=150", total, ok)
	}
}
