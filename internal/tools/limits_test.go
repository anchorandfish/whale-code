// Package tools — N16 ToolLimits 测试。
// 覆盖反例：per-tool 覆盖 / 未知工具默认值 / TurnLimiter 计数与重置 / 并发安全。
package tools

import (
	"sync"
	"testing"
	"time"
)

// TestGetToolLimits_PerToolOverride 反例 5：GetToolLimits 返回 per-tool 覆盖值。
func TestGetToolLimits_PerToolOverride(t *testing.T) {
	l := GetToolLimits("bash")
	if l.MaxConcurrency != 1 {
		t.Errorf("bash 应串行，got MaxConcurrency=%d", l.MaxConcurrency)
	}
	if l.Timeout != 120*time.Second {
		t.Errorf("bash 超时应 120s，got %v", l.Timeout)
	}
	l2 := GetToolLimits("read_file")
	if l2.MaxConcurrency != 0 {
		t.Errorf("read_file 应并发，got MaxConcurrency=%d", l2.MaxConcurrency)
	}
}

// TestGetToolLimits_UnknownReturnsDefault 反例 6：未知工具返回默认值。
func TestGetToolLimits_UnknownReturnsDefault(t *testing.T) {
	l := GetToolLimits("nonexistent_tool")
	if l.MaxResultChars != DefaultToolLimits.MaxResultChars {
		t.Error("未知工具应返回默认值")
	}
	if l.Timeout != DefaultToolLimits.Timeout {
		t.Error("未知工具应返回默认 Timeout")
	}
}

// TestGetToolLimits_AgentConcurrency 校验 Agent 工具并发上限为 3。
func TestGetToolLimits_AgentConcurrency(t *testing.T) {
	l := GetToolLimits("Agent")
	if l.MaxConcurrency != 3 {
		t.Errorf("Agent 应 MaxConcurrency=3，got %d", l.MaxConcurrency)
	}
	if l.Timeout != 300000000000 { // 300s in ns
		t.Errorf("Agent 应 Timeout=300s")
	}
}

// TestTurnLimiter_CheckAndRecord 校验 TurnLimiter 计数与超限检测。
func TestTurnLimiter_CheckAndRecord(t *testing.T) {
	// 默认 MaxCallsPerTurn=0，不启用限制
	tl := NewTurnLimiter()
	if err := tl.CheckLimit("bash"); err != nil {
		t.Errorf("默认不启用 per-turn 限制，不应报错: %v", err)
	}
	tl.Record("bash")
	tl.Record("bash")
	if err := tl.CheckLimit("bash"); err != nil {
		t.Errorf("MaxCallsPerTurn=0 时计数不影响 CheckLimit: %v", err)
	}
}

// TestTurnLimiter_Reset 校验 Reset 清空计数。
func TestTurnLimiter_Reset(t *testing.T) {
	tl := NewTurnLimiter()
	tl.Record("bash")
	tl.Record("bash")
	tl.Reset()
	// Reset 后计数归零（无法直接读 counts，但行为上 CheckLimit 仍通过）
	if err := tl.CheckLimit("bash"); err != nil {
		t.Errorf("Reset 后 CheckLimit 应通过: %v", err)
	}
}

// TestTurnLimiter_CountAndReset 反例 12：TurnLimiter 计数与重置（含超限检测）。
// 设置 MaxCallsPerTurn=2，第三次调用应超限，Reset 后应通过。
func TestTurnLimiter_CountAndReset(t *testing.T) {
	// 设置一个工具的 per-turn 限制
	original := PerToolLimits["test_tool"]
	PerToolLimits["test_tool"] = ToolLimits{MaxCallsPerTurn: 2}
	defer func() { PerToolLimits["test_tool"] = original }()

	tl := NewTurnLimiter()
	// 第一次调用应通过
	if err := tl.CheckLimit("test_tool"); err != nil {
		t.Errorf("第一次调用应通过，got err: %v", err)
	}
	tl.Record("test_tool")
	// 第二次调用应通过
	if err := tl.CheckLimit("test_tool"); err != nil {
		t.Errorf("第二次调用应通过，got err: %v", err)
	}
	tl.Record("test_tool")
	// 第三次调用应超限
	if err := tl.CheckLimit("test_tool"); err == nil {
		t.Error("第三次调用应超限返回 error")
	}
	// 重置后应通过
	tl.Reset()
	if err := tl.CheckLimit("test_tool"); err != nil {
		t.Errorf("重置后应通过，got err: %v", err)
	}
}

// TestTurnLimiter_ConcurrentSafe 反例 13：TurnLimiter 并发安全（破坏性测试）。
// 100 goroutine 并发 Record，校验最终计数为 100。
// 若 sync.Mutex 被移除，此测试会因 data race 或计数丢失而失败。
func TestTurnLimiter_ConcurrentSafe(t *testing.T) {
	tl := NewTurnLimiter()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tl.Record("concurrent_tool")
		}()
	}
	wg.Wait()
	// 校验计数为 100（同包测试可直接访问 counts）
	if tl.counts["concurrent_tool"] != 100 {
		t.Errorf("并发记录后应 100，got %d", tl.counts["concurrent_tool"])
	}
}
