package mcp

// S6/G8 Patch ①（补覆盖）—— 逐写点的**字节级**帧格式断言。
//
// 为什么需要：方案 §7.1「只改 6 个写点中的任意一个回 Content-Length ⇒ 通知/流式单测必红」
// 预设了「通知/流式用例」存在，实测**全仓零命中**（`grep -rn 'SendNotification\|SendStream'
// --include='*_test.go'` 返回空）⇒ 该行原本不可判别。本文件为每个写点各补一条。
//
// 判别性（每条都能被打红）：把对应写点改回 CL 帧 ⇒ 该写点的帧里会出现 `Content-Length` ⇒ 必红。
// 覆盖：writeMessage / Send / SendNotification / sendSuccessResponse / sendErrorResponse / SendStream。
//
// 与 stdio_wire_e2e_test.go 的分工：
//   · 本文件 = 用**捕获型 writer**做逐写点的字节级断言（不需要真子进程，可精确到"这个写点"）；
//   · e2e 文件 = 用**真 Python server**跑完整生产装配（证明整体线协议对得上）。
// 两者互补：前者能定位到具体写点，后者能证明真实对端认可该帧格式。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// captureWriter 只把写出的字节累积到内存，用于断言帧格式。
type captureWriter struct{ buf []byte }

func (w *captureWriter) Write(p []byte) (int, error) { w.buf = append(w.buf, p...); return len(p), nil }
func (w *captureWriter) Close() error                { return nil }

func newCaptureTransport() (*StdioTransport, *captureWriter) {
	w := &captureWriter{}
	return &StdioTransport{
		stdin:          w,
		maxFrameLength: defaultMaxFrameLength,
		pending:        make(map[string]chan *rpcResponse),
		streamPending:  make(map[string]chan StreamFrame),
	}, w
}

// assertNewlineFrame 断言 raw 是一帧合规的 newline-delimited JSON。
func assertNewlineFrame(t *testing.T, label string, raw []byte, wantSubstr string) {
	t.Helper()
	s := string(raw)
	if s == "" {
		t.Fatalf("%s: 未写出任何字节（该写点未生效？）", label)
	}
	if strings.Contains(s, "Content-Length") {
		t.Fatalf("%s: 帧里出现 Content-Length 头 ⇒ 该写点仍是 LSP 风格 CL 帧: %q", label, s)
	}
	if !strings.HasSuffix(s, "\n") {
		t.Fatalf("%s: 帧未以 '\\n' 结尾（合规 server 会认为消息未结束）: %q", label, s)
	}
	if n := strings.Count(s, "\n"); n != 1 {
		t.Fatalf("%s: 帧内有 %d 个换行（消息体含裸换行会截断帧）: %q", label, n, s)
	}
	body := strings.TrimSuffix(s, "\n")
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%s: 帧体不是合法 JSON: %v (%q)", label, err, body)
	}
	if !strings.Contains(s, wantSubstr) {
		t.Fatalf("%s: 帧体缺预期内容 %q: %q", label, wantSubstr, s)
	}
}

func TestWritePoints_WriteMessage(t *testing.T) {
	tr, w := newCaptureTransport()
	if err := tr.writeMessage([]byte(`{"a":1}`)); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	assertNewlineFrame(t, "writeMessage", w.buf, `"a":1`)
}

func TestWritePoints_Send(t *testing.T) {
	tr, w := newCaptureTransport()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	// 无对端 ⇒ 期望 ctx 超时；但**写侧必须已经落下**一帧合规格式。
	if _, err := tr.Send(ctx, 42, "initialize", map[string]any{}); err == nil {
		t.Fatal("Send 无对端响应时应返回错误（ctx 超时）")
	}
	assertNewlineFrame(t, "Send", w.buf, `"method":"initialize"`)
}

func TestWritePoints_SendNotification(t *testing.T) {
	tr, w := newCaptureTransport()
	if err := tr.SendNotification(context.Background(), "notifications/initialized", map[string]any{}); err != nil {
		t.Fatalf("SendNotification: %v", err)
	}
	assertNewlineFrame(t, "SendNotification", w.buf, `"method":"notifications/initialized"`)
}

func TestWritePoints_SendSuccessResponse(t *testing.T) {
	tr, w := newCaptureTransport()
	tr.sendSuccessResponse(json.RawMessage(`1`), map[string]any{"ok": true})
	assertNewlineFrame(t, "sendSuccessResponse", w.buf, `"ok":true`)
}

func TestWritePoints_SendErrorResponse(t *testing.T) {
	tr, w := newCaptureTransport()
	tr.sendErrorResponse(json.RawMessage(`1`), &RPCError{Code: -32601, Message: "method not found"})
	assertNewlineFrame(t, "sendErrorResponse", w.buf, `-32601`)
}

func TestWritePoints_SendStream(t *testing.T) {
	tr, w := newCaptureTransport()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := tr.SendStream(ctx, 7, "tools/call", map[string]any{"n": 1}); err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	assertNewlineFrame(t, "SendStream", w.buf, `"method":"tools/call"`)
}
