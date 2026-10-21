package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// streamOnce 用真实 ai.SimpleClient（TUI/plain/bridge 共用的生产解析路径）
// 向 fakeServer 发一次请求并收集全部事件。测试入口与生产入口同一条
// SSE 解析路径（parseOpenAIDeltaWithAccum），禁止在测试里单独 mock。
func streamOnce(t *testing.T, srvURL, model string) []ai.Event {
	t.Helper()
	client := ai.NewSimpleClient(model, "test-key", srvURL, ai.ProviderOpenAI)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := client.Stream(ctx, ai.Request{
		Model:    model,
		Messages: []ai.MessageEntry{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}
	var events []ai.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

// 结果断言：fake-loop-fail 必须被真实客户端解析为 bash(false) ToolUseEvent。
// 这是全部防护用例确定性的地基——不是"server 发了数据"，而是
// "生产解析路径还原出了正确的工具调用"。
func TestFakeLoopFailEmitsBashFalseToolUse(t *testing.T) {
	srv := httptest.NewServer(newFakeServer("README.md"))
	defer srv.Close()

	events := streamOnce(t, srv.URL, "fake-loop-fail")
	var tool *ai.ToolUseEvent
	stopped := false
	for _, ev := range events {
		if tu, ok := ev.(ai.ToolUseEvent); ok {
			tool = &tu
		}
		if _, ok := ev.(ai.MessageStopEvent); ok {
			stopped = true
		}
	}
	if tool == nil {
		t.Fatalf("未解析出 ToolUseEvent, events: %v", events)
	}
	if tool.Name != "bash" {
		t.Errorf("tool name = %q, want bash", tool.Name)
	}
	if !strings.Contains(tool.Input, "false") {
		t.Errorf("tool input 应包含 false: %s", tool.Input)
	}
	if !stopped {
		t.Error("未收到 MessageStopEvent（流未正常收尾）")
	}
}

// 结果断言：fake-ask-loop 第 1 次请求是 AskUser，第 2 次起是 bash false
// （AskUser 记账用例 T3 的确定性前提）。
func TestFakeAskLoopFirstAskThenBash(t *testing.T) {
	srv := httptest.NewServer(newFakeServer("README.md"))
	defer srv.Close()

	first := streamOnce(t, srv.URL, "fake-ask-loop")
	var firstName string
	for _, ev := range first {
		if tu, ok := ev.(ai.ToolUseEvent); ok {
			firstName = tu.Name
		}
	}
	if firstName != "AskUser" {
		t.Fatalf("第 1 次请求工具 = %q, want AskUser", firstName)
	}

	second := streamOnce(t, srv.URL, "fake-ask-loop")
	var secondName string
	for _, ev := range second {
		if tu, ok := ev.(ai.ToolUseEvent); ok {
			secondName = tu.Name
		}
	}
	if secondName != "bash" {
		t.Fatalf("第 2 次请求工具 = %q, want bash", secondName)
	}
}

// 结果断言：fake-finish 只有文本、无工具调用（acceptance 冒烟场景）。
func TestFakeFinishTextOnlyNoTool(t *testing.T) {
	srv := httptest.NewServer(newFakeServer("README.md"))
	defer srv.Close()

	events := streamOnce(t, srv.URL, "fake-finish")
	var text strings.Builder
	toolSeen := false
	for _, ev := range events {
		if _, ok := ev.(ai.ToolUseEvent); ok {
			toolSeen = true
		}
		if td, ok := ev.(ai.TextDeltaEvent); ok {
			text.WriteString(td.Content)
		}
	}
	if toolSeen {
		t.Error("fake-finish 不应产生工具调用")
	}
	if !strings.Contains(text.String(), "完成") {
		t.Errorf("应有文本响应, got: %q", text.String())
	}
}

// 结果断言：未知场景返回 400 且列出全部可用场景（错误信息可用性）。
func TestUnknownScenarioReturns400WithOptions(t *testing.T) {
	srv := httptest.NewServer(newFakeServer("README.md"))
	defer srv.Close()

	client := ai.NewSimpleClient("fake-nope", "test-key", srv.URL, ai.ProviderOpenAI)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Stream(ctx, ai.Request{
		Model:    "fake-nope",
		Messages: []ai.MessageEntry{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("未知场景应返回错误")
	}
	errText := err.Error()
	for _, want := range []string{"fake-loop-fail", "fake-ask-loop", "fake-finish"} {
		if !strings.Contains(errText, want) {
			t.Errorf("错误信息应列出可用场景 %q, got: %s", want, errText)
		}
	}
}
