package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
)

func TestFinalizeRequestUsageOldSessionRecordsCostOnly(t *testing.T) {
	core := newTestRunnerCore()
	core.UsageTracker = ai.NewUsageTracker("")
	epoch := core.sessionEpoch.Load()
	var usage ai.RequestUsageAccumulator
	usage.Add(ai.UsageEvent{InputTokens: 120, CacheRead: 20})
	usage.Add(ai.UsageEvent{OutputTokens: 30})
	usage.Add(ai.UsageEvent{InputTokens: 120, OutputTokens: 30})
	core.sessionEpoch.Add(1)

	got, ok := core.finalizeRequestUsage(context.Background(), &usage, epoch)
	if !ok || got.InputTokens != 120 || got.OutputTokens != 30 {
		t.Fatalf("finalized usage = %#v, %v", got, ok)
	}
	if core.InputTokens != 0 || core.OutputTokens != 0 {
		t.Fatalf("old request polluted new session totals: %d/%d", core.InputTokens, core.OutputTokens)
	}
	if core.UsageTracker.TotalAPICalls != 1 || core.UsageTracker.TotalInputTokens != 120 || core.UsageTracker.TotalOutputTokens != 30 {
		t.Fatalf("provider cost was not recorded exactly once: %#v", core.UsageTracker)
	}
	if _, ok := core.finalizeRequestUsage(context.Background(), &usage, epoch); ok {
		t.Fatal("usage finalized twice")
	}
}

func TestFinalizeRequestUsageCountsSessionRequests(t *testing.T) {
	core := newTestRunnerCore()
	epoch := core.sessionEpoch.Load()
	var usage ai.RequestUsageAccumulator
	usage.Add(ai.UsageEvent{InputTokens: 120, OutputTokens: 30})
	if _, ok := core.finalizeRequestUsage(context.Background(), &usage, epoch); !ok {
		t.Fatal("usage was not finalized")
	}
	if core.RequestCount != 1 {
		t.Fatalf("request count = %d, want 1", core.RequestCount)
	}

	// 旧 epoch 的迟到 usage 计入 provider 统计，但不计入新会话的请求数
	core.sessionEpoch.Add(1)
	var late ai.RequestUsageAccumulator
	late.Add(ai.UsageEvent{InputTokens: 5})
	if _, ok := core.finalizeRequestUsage(context.Background(), &late, epoch); !ok {
		t.Fatal("late usage was not finalized")
	}
	if core.RequestCount != 1 {
		t.Fatalf("stale request leaked into new session count: %d, want 1", core.RequestCount)
	}
}

func TestProcessStreamEventsRecordsReportedUsageOnError(t *testing.T) {
	core := newTestRunnerCore()
	core.UsageTracker = ai.NewUsageTracker("")
	events := eventChannel([]ai.Event{
		ai.UsageEvent{InputTokens: 200},
		ai.UsageEvent{OutputTokens: 40},
		ai.ErrorEvent{Err: errors.New("stream failed")},
	})
	_, _, _, err := core.processStreamEvents(context.Background(), events, &mockOutputHandler{})
	if err == nil {
		t.Fatal("expected stream error")
	}
	if core.InputTokens != 200 || core.OutputTokens != 40 {
		t.Fatalf("same-session error usage = %d/%d, want 200/40", core.InputTokens, core.OutputTokens)
	}
	if core.UsageTracker.TotalAPICalls != 1 {
		t.Fatalf("error request API calls = %d, want 1", core.UsageTracker.TotalAPICalls)
	}
}

func TestStopExecutionFinalizesReportedUsage(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.sessionService.SetState(StateStreaming)
	m.aiService.SetRequestID("cancel-request")
	m.beginRequestUsage("cancel-request")
	m.SessionModel.requestUsage.Add(ai.UsageEvent{InputTokens: 90, OutputTokens: 10})

	model, _ := m.stopExecution()
	stopped := model.(Model)
	if stopped.SessionModel.inputTokens != 90 || stopped.SessionModel.outputTokens != 10 {
		t.Fatalf("cancelled request usage = %d/%d, want 90/10", stopped.SessionModel.inputTokens, stopped.SessionModel.outputTokens)
	}
	if stopped.SessionModel.lastInputTokens != 90 || stopped.SessionModel.lastOutputTokens != 10 {
		t.Fatalf("cancelled last usage = %d/%d, want 90/10", stopped.SessionModel.lastInputTokens, stopped.SessionModel.lastOutputTokens)
	}
}
