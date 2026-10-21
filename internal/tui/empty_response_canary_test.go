package tui

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/session"
)

// captureLogCanary 捕获标准库 log 输出（canary 日志结果断言用）。
func captureLogCanary(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// TestHandleStreamDone_EmptyResponseLogsCanary（2026-09-08 A2 事故观测缺口）
// 空响应路径此前零日志：模型真空响应 / 流截断 / 仅 reasoning 三种情况不可区分。
// 断言结果：空 blocks 时必须打 canary，包含定位所需字段（stopReceived/model/requestID）。
func TestHandleStreamDone_EmptyResponseLogsCanary(t *testing.T) {
	t.Run("truncated_stream", func(t *testing.T) {
		buf := captureLogCanary(t)
		m := newTUIRegressionModel(t)
		m.reassignServices()
		m.state = StateStreaming

		_ = m.handleStreamDone(streamDoneMsg{
			blocks:    []session.ContentBlock{},
			usage:     &session.TokenUsage{},
			model:     "kimi-k2.7-code",
			requestID: "empty-req-1",
			// stopReceived=false 模拟流截断（channel 关闭且无 MessageStopEvent）
			stopReceived: false,
		})

		out := buf.String()
		for _, want := range []string{
			"empty response",
			"stopReceived=false",
			"kimi-k2.7-code",
			"empty-req-1",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("canary 日志缺少 %q，实际输出：%s", want, out)
			}
		}
	})

	t.Run("clean_stop_with_usage_and_thinking", func(t *testing.T) {
		buf := captureLogCanary(t)
		m := newTUIRegressionModel(t)
		m.reassignServices()
		m.state = StateStreaming
		// 注入本轮 usage（usageSeen=true 的信号源）
		m.aiService.SetRequestID("empty-req-2")
		m.beginRequestUsage("empty-req-2")
		m.SessionModel.requestUsage.Add(ai.UsageEvent{InputTokens: 10128, OutputTokens: 2})

		_ = m.handleStreamDone(streamDoneMsg{
			blocks:       []session.ContentBlock{},
			usage:        &session.TokenUsage{InputTokens: 10128, OutputTokens: 2},
			model:        "kimi-k2.7-code",
			requestID:    "empty-req-2",
			thinking:     "模型只输出了思考内容",
			stopReceived: true,
			finishReason: "length",
		})

		out := buf.String()
		for _, want := range []string{
			"empty response",
			"stopReceived=true",
			"finishReason=\"length\"",
			"usageSeen=true",
			"inputTokens=10128",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("canary 日志缺少 %q，实际输出：%s", want, out)
			}
		}
	})
}

// TestHandleStreamDone_NonEmptyResponseNoCanary：正常响应不得误打 canary。
func TestHandleStreamDone_NonEmptyResponseNoCanary(t *testing.T) {
	buf := captureLogCanary(t)
	m := newTUIRegressionModel(t)
	m.reassignServices()
	m.state = StateStreaming

	_ = m.handleStreamDone(streamDoneMsg{
		blocks:    []session.ContentBlock{{Type: "text", Text: "hi"}},
		usage:     &session.TokenUsage{},
		model:     "kimi-k2.7-code",
		requestID: "req-ok",
	})

	if strings.Contains(buf.String(), "empty response") {
		t.Errorf("非空响应不应打 canary，实际输出：%s", buf.String())
	}
}

// TestFinalizeStream_CarriesStopDiagnostics：MessageStopEvent 路径必须携带
// stopReceived=true + finishReason（finalizeStream 闭包产出 streamDoneMsg）。
func TestFinalizeStream_CarriesStopDiagnostics(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.reassignServices()
	m.state = StateStreaming

	events := make(chan ai.Event)
	cmd := m.handleStreamEvent(ai.MessageStopEvent{FinishReason: "length"}, events)
	if cmd == nil {
		t.Fatal("handleStreamEvent(MessageStopEvent) 应返回 cmd")
	}
	msg := cmd()
	done, ok := msg.(streamDoneMsg)
	if !ok {
		t.Fatalf("期望 streamDoneMsg，实际 %T", msg)
	}
	if !done.stopReceived {
		t.Error("MessageStopEvent 正常结束应标记 stopReceived=true")
	}
	if done.finishReason != "length" {
		t.Errorf("finishReason = %q, want length", done.finishReason)
	}
}

// TestReadNextEvent_ClosedChannelMarksTruncated：通道关闭（无 MessageStopEvent）
// 产生的 streamDoneMsg 必须标记 stopReceived=false（流截断信号）。
func TestReadNextEvent_ClosedChannelMarksTruncated(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.reassignServices()

	ch := make(chan ai.Event)
	close(ch)
	cmd := m.readNextEvent(ch)
	if cmd == nil {
		t.Fatal("通道关闭应产生 streamDoneMsg cmd")
	}
	msg := cmd()
	done, ok := msg.(streamDoneMsg)
	if !ok {
		t.Fatalf("期望 streamDoneMsg，实际 %T", msg)
	}
	if done.stopReceived {
		t.Error("通道关闭路径应标记 stopReceived=false（流截断）")
	}
}

// TestProcessStreamEvents_EmptyResponseLogsCanary：plain/bridge 共用路径
// （processStreamEvents）在流结束但无文本无工具时必须打 canary，
// 并携带 stopReceived/finishReason/usage 定位字段。
func TestProcessStreamEvents_EmptyResponseLogsCanary(t *testing.T) {
	t.Run("clean_stop_with_length", func(t *testing.T) {
		buf := captureLogCanary(t)
		core := newTestRunnerCore()
		core.ModelName = "kimi-k2.7-code"

		events := eventChannel([]ai.Event{
			ai.UsageEvent{InputTokens: 200, OutputTokens: 40},
			ai.MessageStopEvent{FinishReason: "length"},
		})
		text, _, _, err := core.processStreamEvents(context.Background(), events, &mockOutputHandler{})
		if err != nil {
			t.Fatalf("processStreamEvents: %v", err)
		}
		if text != "" {
			t.Fatalf("空流不应产出文本, got %q", text)
		}

		out := buf.String()
		for _, want := range []string{
			"empty response",
			"stopReceived=true",
			"finishReason=\"length\"",
			"inputTokens=200",
			"outputTokens=40",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("canary 日志缺少 %q，实际输出：%s", want, out)
			}
		}
	})

	t.Run("truncated_no_message_stop", func(t *testing.T) {
		buf := captureLogCanary(t)
		core := newTestRunnerCore()
		core.ModelName = "kimi-k2.7-code"

		// 通道关闭前只有 usage 事件，无 MessageStopEvent（流截断形态）
		events := eventChannel([]ai.Event{
			ai.UsageEvent{InputTokens: 150, OutputTokens: 3},
		})
		text, _, _, err := core.processStreamEvents(context.Background(), events, &mockOutputHandler{})
		if err != nil {
			t.Fatalf("processStreamEvents: %v", err)
		}
		if text != "" {
			t.Fatalf("空流不应产出文本, got %q", text)
		}

		out := buf.String()
		for _, want := range []string{
			"empty response",
			"stopReceived=false",
			"inputTokens=150",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("canary 日志缺少 %q，实际输出：%s", want, out)
			}
		}
	})

	t.Run("non_empty_response_no_canary", func(t *testing.T) {
		buf := captureLogCanary(t)
		core := newTestRunnerCore()
		core.ModelName = "kimi-k2.7-code"

		events := eventChannel([]ai.Event{
			ai.TextDeltaEvent{Content: "hi"},
			ai.MessageStopEvent{FinishReason: "stop"},
		})
		text, _, _, err := core.processStreamEvents(context.Background(), events, &mockOutputHandler{})
		if err != nil {
			t.Fatalf("processStreamEvents: %v", err)
		}
		if text != "hi" {
			t.Fatalf("应产出文本 hi, got %q", text)
		}
		if strings.Contains(buf.String(), "empty response") {
			t.Errorf("非空响应不应打 canary，实际输出：%s", buf.String())
		}
	})
}

// TestReadNextEvent_TruncationCapturesThinking：流截断（通道关闭）路径
// 必须携带已流出的 thinking——否则 canary 的 thinkingLen 恒为 0，
// 无法区分"仅输出了思考后被截断"与"什么都没输出"。
func TestReadNextEvent_TruncationCapturesThinking(t *testing.T) {
	m := newTUIRegressionModel(t)
	m.reassignServices()
	m.state = StateStreaming

	// 模拟截断前已有 thinking 流出（生产路径：ThinkingDeltaEvent → activeCell.AppendThinking）
	if m.activeCell != nil {
		m.activeCell.AppendThinking("部分思考内容")
	} else {
		m.sessionService.SetCurrentThinking("部分思考内容")
	}

	ch := make(chan ai.Event)
	close(ch)
	cmd := m.readNextEvent(ch)
	msg := cmd()
	done, ok := msg.(streamDoneMsg)
	if !ok {
		t.Fatalf("期望 streamDoneMsg，实际 %T", msg)
	}
	if done.stopReceived {
		t.Error("通道关闭路径应标记 stopReceived=false")
	}
	if done.thinking != "部分思考内容" {
		t.Errorf("截断路径应保留已流出的 thinking, got %q", done.thinking)
	}
}

// TestEmptyResponseCanary_FullChainUsageNotZeroed：生产同构全链路
// UsageEvent → MessageStopEvent（首次 finalizeRequestUsage，真值落 lastInputTokens）
// → finalizeStream（捕获真值进 msg.usage）→ handleStreamDone（canary 必须打真值，
// 不能因 Finalize 一次性而打零值——对抗式审查发现的观测失真缺陷回归测试）。
func TestEmptyResponseCanary_FullChainUsageNotZeroed(t *testing.T) {
	buf := captureLogCanary(t)
	m := newTUIRegressionModel(t)
	m.reassignServices()
	m.state = StateStreaming

	events := make(chan ai.Event)
	m.aiService.SetRequestID("chain-req-1")
	m.beginRequestUsage("chain-req-1")

	// 生产序列：先 UsageEvent，再 MessageStopEvent
	_ = m.handleStreamEvent(ai.UsageEvent{InputTokens: 9000, OutputTokens: 7}, events)
	cmd := m.handleStreamEvent(ai.MessageStopEvent{FinishReason: "stop"}, events)
	if cmd == nil {
		t.Fatal("MessageStopEvent 应产生 streamDoneMsg cmd")
	}
	msg := cmd()
	done, ok := msg.(streamDoneMsg)
	if !ok {
		t.Fatalf("期望 streamDoneMsg，实际 %T", msg)
	}
	if len(done.blocks) != 0 {
		t.Fatalf("应无 blocks（空响应形态）, got %d", len(done.blocks))
	}
	_ = m.handleStreamDone(done)

	out := buf.String()
	for _, want := range []string{
		"stopReceived=true",
		"finishReason=\"stop\"",
		"inputTokens=9000",
		"outputTokens=7",
		"usageSeen=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("canary 缺少 %q，实际输出：%s", want, out)
		}
	}
}
