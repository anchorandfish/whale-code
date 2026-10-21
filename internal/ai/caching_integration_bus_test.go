package ai

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/bus"
)

// TestCachingIntegration_SessionSwitchReset 验证 SessionSwitchEvent 触发后 Reset 基线（方案 E）。
// 发布 SessionSwitchEvent 后，CachingIntegration 内部 breakDetector 应被重置（prevPromptState=nil）。
func TestCachingIntegration_SessionSwitchReset(t *testing.T) {
	b := bus.NewBus()
	ci := NewCachingIntegration("claude-sonnet-4-5", 0.05)
	ci.SetBus(b)
	ci.SubscribeSessionEvents(b)

	// 建立基线
	ci.RecordStateBeforeRequest("sys", nil, "claude-sonnet-4-5")

	// 发布 SessionSwitchEvent
	b.Publish(bus.SessionSwitchEvent{
		OldSessionID: "old",
		NewSessionID: "new",
		OccurredAt:   time.Now(),
	})

	// 等待 goroutine 处理
	time.Sleep(100 * time.Millisecond)

	// 再次 RecordStateBeforeRequest 应视为首次调用（基线已重置）
	// 通过 CheckBreakAfterResponse 验证：基线重置后应返回 nil（首次调用）
	result := ci.CheckBreakAfterResponse(1000)
	if result != nil {
		t.Errorf("SessionSwitch 后基线应重置，CheckBreakAfterResponse 应返回 nil（方案 E），实际 %v", result)
	}
}

// TestCachingIntegration_SessionSwitchInFlightDrop 验证 SessionSwitch 期间 in-flight 请求结果丢弃（方案 R-3）。
// 建立基线后，模拟 in-flight 请求开始（记录 startEpoch），期间发生 SessionSwitch，
// 请求完成时 epoch 已变化，结果应丢弃（返回 nil）。
func TestCachingIntegration_SessionSwitchInFlightDrop(t *testing.T) {
	b := bus.NewBus()
	ci := NewCachingIntegration("claude-sonnet-4-5", 0.05)
	ci.SetBus(b)
	ci.SubscribeSessionEvents(b)

	// 建立基线：RecordStateBeforeRequest + 第一次 CheckBreakAfterResponse 建立 cacheReadTokens 基线
	ci.RecordStateBeforeRequest("sys", nil, "claude-sonnet-4-5")
	ci.CheckBreakAfterResponse(10000) // 建立基线

	// 模拟 in-flight 请求开始（CheckBreakAfterResponse 读取 startEpoch）
	// 在请求完成前发布 SessionSwitchEvent
	b.Publish(bus.SessionSwitchEvent{
		OldSessionID: "old",
		NewSessionID: "new",
		OccurredAt:   time.Now(),
	})
	time.Sleep(100 * time.Millisecond)

	// in-flight 请求完成：cacheReadTokens 大幅下降（正常应报告断裂）
	// 但 epoch 已变化，结果应丢弃（返回 nil）
	result := ci.CheckBreakAfterResponse(100) // 大幅下降
	if result != nil {
		t.Errorf("SessionSwitch 期间 in-flight 请求结果应丢弃（方案 R-3），实际 result=%v", result)
	}
}

// TestCachingIntegration_EpochIncrement 验证每次 SessionSwitchEvent 递增 epoch（方案 R-3）。
func TestCachingIntegration_EpochIncrement(t *testing.T) {
	b := bus.NewBus()
	ci := NewCachingIntegration("claude-sonnet-4-5", 0.05)
	ci.SetBus(b)
	ci.SubscribeSessionEvents(b)

	ci.mu.RLock()
	epoch0 := ci.sessionSwitchEpoch
	ci.mu.RUnlock()

	// 发布 3 次 SessionSwitchEvent
	for i := 0; i < 3; i++ {
		b.Publish(bus.SessionSwitchEvent{
			OldSessionID: "old",
			NewSessionID: "new",
			OccurredAt:   time.Now(),
		})
		time.Sleep(50 * time.Millisecond)
	}

	ci.mu.RLock()
	epoch3 := ci.sessionSwitchEpoch
	ci.mu.RUnlock()

	if epoch3-epoch0 != 3 {
		t.Errorf("3 次 SessionSwitchEvent 应递增 epoch 3 次（方案 R-3），实际 epoch0=%d epoch3=%d", epoch0, epoch3)
	}
}

func TestCachingIntegrationCapabilityEpochChangesWithModel(t *testing.T) {
	ci := NewCachingIntegration("gpt-4", 0.05)
	before := ci.CapabilityEpoch()
	ci.UpdateModel("claude-sonnet-4")
	if after := ci.CapabilityEpoch(); after != before+1 {
		t.Fatalf("capability epoch=%d, want %d", after, before+1)
	}
	stable := ci.CapabilityEpoch()
	ci.UpdateModel("claude-sonnet-4")
	if ci.CapabilityEpoch() != stable {
		t.Fatal("setting the same model changed capability epoch")
	}
}

// TestCachingIntegration_FirstRequestAfterSwitchReturnsNil 验证 SessionSwitch 后首次请求返回 nil（方案 R-3）。
// 切换后基线重置，首次 CheckBreakAfterResponse 视为首次调用（无 prevPromptState），返回 nil。
func TestCachingIntegration_FirstRequestAfterSwitchReturnsNil(t *testing.T) {
	b := bus.NewBus()
	ci := NewCachingIntegration("claude-sonnet-4-5", 0.05)
	ci.SetBus(b)
	ci.SubscribeSessionEvents(b)

	// 建立基线
	ci.RecordStateBeforeRequest("sys", nil, "claude-sonnet-4-5")
	ci.CheckBreakAfterResponse(10000)

	// 发布 SessionSwitchEvent
	b.Publish(bus.SessionSwitchEvent{
		OldSessionID: "old",
		NewSessionID: "new",
		OccurredAt:   time.Now(),
	})
	time.Sleep(100 * time.Millisecond)

	// 切换后首次 CheckBreakAfterResponse 应返回 nil（基线已重置）
	result := ci.CheckBreakAfterResponse(5000)
	if result != nil {
		t.Errorf("SessionSwitch 后首次请求应返回 nil（方案 R-3），实际 %v", result)
	}
}
