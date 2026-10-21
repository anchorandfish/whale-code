package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 空响应观测缺口（2026-09-08 A2 重跑事故）：
// 模型在工具结果回传后返回空 completion（无文本/无工具调用），
// whale 只显示 "AI returned an empty response"，无 finish_reason 等诊断信息，
// 无法区分"模型主动停止 / 输出被截断 / 仅输出了 reasoning"。
// 本文件锁定契约：OpenAI SSE 流的 finish_reason 必须透传到 MessageStopEvent。

func TestParseOpenAIDeltaWithAccum_FinishReason(t *testing.T) {
	pendingArgs := map[int]string{}
	pendingNames := map[int]string{}
	pendingIDs := map[int]string{}

	// reasoning-only 被截断的典型形态：空 delta + finish_reason=length
	_, _, fr := parseOpenAIDeltaWithAccum(
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
		pendingArgs, pendingNames, pendingIDs)
	if fr != "length" {
		t.Fatalf("finishReason = %q, want %q", fr, "length")
	}

	// 普通文本 chunk 无 finish_reason
	_, _, fr2 := parseOpenAIDeltaWithAccum(
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		pendingArgs, pendingNames, pendingIDs)
	if fr2 != "" {
		t.Fatalf("无 finish_reason 应返回空串, got %q", fr2)
	}

	// usage-only chunk（choices 为空数组）
	_, _, fr3 := parseOpenAIDeltaWithAccum(
		`{"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		pendingArgs, pendingNames, pendingIDs)
	if fr3 != "" {
		t.Fatalf("usage-only chunk finishReason 应为空串, got %q", fr3)
	}

	// finish_reason 与内容共存于同一 chunk（部分 provider 行为）
	evts, _, fr4 := parseOpenAIDeltaWithAccum(
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
		pendingArgs, pendingNames, pendingIDs)
	if fr4 != "stop" {
		t.Fatalf("同 chunk finishReason = %q, want stop", fr4)
	}
	if len(evts) != 1 {
		t.Fatalf("该帧应产出 1 个事件, got %d 个：%#v", len(evts), evts)
	}
	if _, ok := evts[0].(TextDeltaEvent); !ok {
		t.Fatalf("内容事件不应被 finish_reason 吞掉, got %T", evts[0])
	}
}

// TestStreamOpenAI_FinishReasonPropagatedToMessageStop 走 SimpleClient.Stream 生产路径
// （doStreamOpenAI，kimi 等 openai 兼容模型实际路径）。
// A2 事故形态：空 delta + finish_reason + [DONE]，
// MessageStopEvent.FinishReason 必须携带 finish_reason。
func TestStreamOpenAI_FinishReasonPropagatedToMessageStop(t *testing.T) {
	const chunkLength = "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"
	const chunkText = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	const chunkStop = "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	const done = "data: [DONE]\n\n"

	cases := []struct {
		name       string
		sseBody    string
		wantReason string
	}{
		{name: "empty_stream_with_length_reason", sseBody: chunkLength + done, wantReason: "length"},
		{name: "normal_stop_reason_propagates", sseBody: chunkText + chunkStop + done, wantReason: "stop"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				fmt.Fprint(w, tc.sseBody)
			}))
			defer server.Close()

			client := NewSimpleClient("kimi-test", "test-key", server.URL, ProviderOpenAI)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			events, err := client.Stream(ctx, Request{
				Messages: []MessageEntry{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Stream 返回错误: %v", err)
			}

			var stopEvt MessageStopEvent
			stopSeen := false
			for e := range events {
				if s, ok := e.(MessageStopEvent); ok {
					stopEvt = s
					stopSeen = true
				}
			}
			if !stopSeen {
				t.Fatal("缺少 MessageStopEvent")
			}
			if stopEvt.FinishReason != tc.wantReason {
				t.Fatalf("FinishReason = %q, want %q", stopEvt.FinishReason, tc.wantReason)
			}
		})
	}
}

// TestStreamOpenAI_TruncatedStreamNoMessageStop 锁定"流截断"契约：
// SSE 流未发 [DONE] 就断开（网络截断/代理超时）时，events 通道关闭且
// 不产生 MessageStopEvent —— TUI 侧据 stopReceived=false 区分截断与真空响应。
func TestStreamOpenAI_TruncatedStreamNoMessageStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 只发一个 usage chunk，然后直接关闭连接（无 [DONE]）
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n")
	}))
	defer server.Close()

	client := NewSimpleClient("kimi-test", "test-key", server.URL, ProviderOpenAI)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := client.Stream(ctx, Request{
		Messages: []MessageEntry{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误: %v", err)
	}

	hasUsage := false
	for e := range events {
		if _, ok := e.(MessageStopEvent); ok {
			t.Fatal("截断的流不应产生 MessageStopEvent")
		}
		if _, ok := e.(UsageEvent); ok {
			hasUsage = true
		}
	}
	if !hasUsage {
		t.Fatal("截断前的事件应正常送达（usage chunk）")
	}
}
