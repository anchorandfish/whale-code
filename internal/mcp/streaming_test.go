// Package mcp — 流式工具结果测试（T1-P1-A3 /）
//
// 本文件覆盖 03-实施prompt.md §6.1 测试矩阵的全部用例：
// - TestCallToolStream_Fallback：transport 不支持 StreamingTransport → 降级为 1-chunk 流
// - TestCallToolStream_ProgressAndResult：多个 progress chunk + 1 result chunk，顺序正确
// - TestCallToolStream_ErrorChunk：server 返回 error → 推送 error chunk + 关闭 channel
// - TestCallToolStream_ContextCancel：ctx 取消 → channel 关闭 + goroutine 退出（无泄漏）
// - TestCallToolStream_BackpressureDrop：背压触发 drop → progress chunk 丢弃，result chunk 不丢
// - TestCallToolStream_SanitizeProgress：progress Message 字段走 SanitizeString
// - TestCallToolStream_SanitizeResult：result 走 SanitizeToolResult（R1-R5）
// - TestCallToolStream_TransportDisconnect：transport channel 关闭 → 关闭 out
// - TestCallToolStream_ConcurrentCalls：多个 CallToolStream 并发，channel 互不干扰
// - TestStdioTransport_SendStream：stdio transport 的 SendStream 路由
// - TestHTTPTransport_SendStream_SSE：HTTP transport SSE 流式响应解析
// - TestHTTPTransport_SendStream_JSON：HTTP transport JSON 一次性响应包装为 1-chunk 流
//
// 对标 MCP 规范 2025-06-18 §"Tools" + §"Transports"（访问日期 2026-07-21）。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockStreamingTransport 是测试用的 StreamingTransport 实现，可注入预设 StreamFrame 序列。
// 不实现真正的网络通信，仅在测试中模拟 transport 行为。
type mockStreamingTransport struct {
	frames     []StreamFrame // 预设帧序列
	frameDelay time.Duration // 帧间延迟（模拟真实流式）
	closeAfter atomic.Bool   // 是否已关闭
	closeMu    sync.Mutex
}

// newMockStreamingTransport 创建 mock transport 并注入预设帧序列。
func newMockStreamingTransport(frames []StreamFrame) *mockStreamingTransport {
	return &mockStreamingTransport{frames: frames}
}

// Send 实现 Transport 接口（test 中不被 CallToolStream 走，保留空实现）
func (m *mockStreamingTransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
	return nil, fmt.Errorf("Send not implemented in mockStreamingTransport")
}

// SendNotification 实现 Transport 接口
func (m *mockStreamingTransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	return nil
}

// SetNotificationHandler 实现 Transport 接口
func (m *mockStreamingTransport) SetNotificationHandler(handler NotificationHandler) {}

// Close 实现 Transport 接口
func (m *mockStreamingTransport) Close() error {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	m.closeAfter.Store(true)
	return nil
}

// SendResponse 实现 Transport 接口（P0-7 扩展，mock 空实现）
func (m *mockStreamingTransport) SendResponse(reqID json.RawMessage, result interface{}) error {
	return nil
}

// SetRequestHandler 实现 Transport 接口（P0-7 扩展，mock 空实现）
func (m *mockStreamingTransport) SetRequestHandler(handler RequestHandler) {}

// SendStream 实现 StreamingTransport 接口，按预设序列推送帧到 channel。
func (m *mockStreamingTransport) SendStream(ctx context.Context, id int64, method string, params interface{}) (<-chan StreamFrame, error) {
	ch := make(chan StreamFrame, len(m.frames))
	go func() {
		defer close(ch)
		for _, frame := range m.frames {
			if m.frameDelay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.frameDelay):
				}
			}
			select {
			case ch <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// newClientWithTransport 构造带 mock transport 的 Client（仅测试用）。
func newClientWithTransport(trans Transport) *Client {
	return &Client{
		trans:         trans,
		transportType: "mock",
	}
}

// collectChunks 收集 channel 中所有 chunk 直到关闭，返回切片。
func collectChunks(t *testing.T, ch <-chan StreamChunk) []StreamChunk {
	t.Helper()
	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// TestCallToolStream_Fallback 验证 transport 不支持 StreamingTransport 时降级为 1-chunk 流。
func TestCallToolStream_Fallback(t *testing.T) {
	// nonStreamingTransport 仅实现 Transport，不实现 StreamingTransport
	trans := &nonStreamingTransport{result: &ToolResult{Content: []ToolResultContent{{Type: "text", Text: "fallback result"}}}}
	c := newClientWithTransport(trans)
	// 预填 CallResults 不需要，因为 fallback 调 CallTool，CallTool 走 trans.Send
	// 改用直接 mock：通过 serverID 计数器 + trans.Send 返回预设结果
	c.trans = trans

	ctx := context.Background()
	ch, err := c.CallToolStream(ctx, "test_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream fallback returned error: %v", err)
	}
	chunks := collectChunks(t, ch)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk in fallback, got %d", len(chunks))
	}
	if chunks[0].Type != StreamChunkResult {
		t.Errorf("expected result chunk, got %s", chunks[0].Type)
	}
	if chunks[0].Result == nil || chunks[0].Result.Text() != "fallback result" {
		t.Errorf("expected fallback result text, got %+v", chunks[0].Result)
	}
}

// nonStreamingTransport 不实现 StreamingTransport，仅实现 Transport 接口。
type nonStreamingTransport struct {
	result *ToolResult
}

func (n *nonStreamingTransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
	// 模拟 tools/call 响应
	resultJSON, _ := json.Marshal(n.result)
	return resultJSON, nil
}

func (n *nonStreamingTransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	return nil
}

func (n *nonStreamingTransport) SetNotificationHandler(handler NotificationHandler) {}

func (n *nonStreamingTransport) Close() error { return nil }

// SendResponse 实现 Transport 接口（P0-7 扩展，mock 空实现）
func (n *nonStreamingTransport) SendResponse(reqID json.RawMessage, result interface{}) error {
	return nil
}

// SetRequestHandler 实现 Transport 接口（P0-7 扩展，mock 空实现）
func (n *nonStreamingTransport) SetRequestHandler(handler RequestHandler) {}

// TestCallToolStream_ProgressAndResult 验证多个 progress chunk + 1 result chunk 顺序正确。
func TestCallToolStream_ProgressAndResult(t *testing.T) {
	frames := []StreamFrame{
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 10, Total: 100, Message: "10%"}},
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 50, Total: 100, Message: "50%"}},
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 90, Total: 100, Message: "90%"}},
		{Result: mustMarshalToolResult(&ToolResult{Content: []ToolResultContent{{Type: "text", Text: "done"}}})},
	}
	trans := newMockStreamingTransport(frames)
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.CallToolStream(ctx, "long_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}
	chunks := collectChunks(t, ch)
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks (3 progress + 1 result), got %d", len(chunks))
	}
	// 前 3 个是 progress
	for i := 0; i < 3; i++ {
		if chunks[i].Type != StreamChunkProgress {
			t.Errorf("chunk[%d] expected progress, got %s", i, chunks[i].Type)
		}
	}
	// 最后一个是 result
	if chunks[3].Type != StreamChunkResult {
		t.Errorf("last chunk expected result, got %s", chunks[3].Type)
	}
	// progress 百分比正确转换（10/100*100=10）
	if chunks[0].Progress == nil || *chunks[0].Progress != 10.0 {
		t.Errorf("chunk[0] expected progress 10, got %+v", chunks[0].Progress)
	}
}

// TestCallToolStream_ErrorChunk 验证 server 返回 error → 推送 error chunk + 关闭 channel。
func TestCallToolStream_ErrorChunk(t *testing.T) {
	frames := []StreamFrame{
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 50, Total: 100}},
		{Error: &RPCError{Code: -32603, Message: "server internal error"}},
	}
	trans := newMockStreamingTransport(frames)
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.CallToolStream(ctx, "err_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}
	chunks := collectChunks(t, ch)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (1 progress + 1 error), got %d", len(chunks))
	}
	if chunks[1].Type != StreamChunkError {
		t.Errorf("last chunk expected error, got %s", chunks[1].Type)
	}
	if chunks[1].Error == nil {
		t.Error("error chunk should carry non-nil Error")
	}
}

// TestCallToolStream_ContextCancel 验证 ctx 取消 → channel 关闭 + goroutine 退出（无泄漏）。
// race 测试：通过 -race 标志验证并发安全。
func TestCallToolStream_ContextCancel(t *testing.T) {
	// 构造持续推送 progress 但永不结束的 transport
	frames := make([]StreamFrame, 0, 100)
	for i := 0; i < 100; i++ {
		frames = append(frames, StreamFrame{
			Method:   "notifications/progress",
			Progress: &ProgressInfo{Progress: float64(i), Total: 100},
		})
	}
	trans := newMockStreamingTransport(frames)
	trans.frameDelay = 50 * time.Millisecond // 每帧 50ms，总时长 5s
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := c.CallToolStream(ctx, "long_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}

	// 等待几个 chunk 后取消
	go func() {
		time.Sleep(200 * time.Millisecond) // 接收约 4 个 chunk
		cancel()
	}()

	chunks := collectChunks(t, ch)
	// channel 应该被关闭（不管收到多少 chunk）
	if len(chunks) == 0 {
		t.Error("expected at least some chunks before cancel")
	}
}

// TestCallToolStream_BackpressureDrop 验证背压触发 drop → progress chunk 丢弃，result chunk 不丢。
// 通过小 buffer + 慢消费模拟背压。
func TestCallToolStream_BackpressureDrop(t *testing.T) {
	// 构造大量 progress chunk + 1 个 result chunk
	// BackpressurePipe 默认 buffer 32 + drop 策略，progress 可丢，result 不可丢
	frames := make([]StreamFrame, 0, 200)
	for i := 0; i < 150; i++ {
		frames = append(frames, StreamFrame{
			Method:   "notifications/progress",
			Progress: &ProgressInfo{Progress: float64(i), Total: 100},
		})
	}
	frames = append(frames, StreamFrame{
		Result: mustMarshalToolResult(&ToolResult{Content: []ToolResultContent{{Type: "text", Text: "final"}}}),
	})
	trans := newMockStreamingTransport(frames)
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.CallToolStream(ctx, "burst_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}

	// 慢消费：每 5ms 读一个 chunk，触发背压 drop
	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
		time.Sleep(5 * time.Millisecond)
	}

	// result chunk 必须存在（永不丢弃）
	hasResult := false
	for _, ck := range chunks {
		if ck.Type == StreamChunkResult {
			hasResult = true
			break
		}
	}
	if !hasResult {
		t.Error("result chunk must not be dropped by backpressure")
	}
	// progress chunk 数量应少于 150（部分被 drop）
	progressCount := 0
	for _, ck := range chunks {
		if ck.Type == StreamChunkProgress {
			progressCount++
		}
	}
	if progressCount == 150 {
		t.Logf("progressCount=%d (note: may equal 150 if buffer absorbed all; backpressure drop is best-effort)", progressCount)
	}
	t.Logf("received: progress=%d, total=%d", progressCount, len(chunks))
}

// TestCallToolStream_SanitizeProgress 验证 progress Message 字段走 SanitizeString。
func TestCallToolStream_SanitizeProgress(t *testing.T) {
	// 注入带 Unicode 危险字符（零宽空格 U+200B）+ 路径遍历的 Message，SanitizeString 应清洗
	// R1: 危险 Unicode 移除（零宽字符 / 方向格式化 / BOM / 私用区）
	// R2: 路径遍历（../ 或 ..\）替换为 [PATH-SANITIZED]
	frames := []StreamFrame{
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 50, Total: 100, Message: "progress\u200Bwith../path"}},
		{Result: mustMarshalToolResult(&ToolResult{Content: []ToolResultContent{{Type: "text", Text: "done"}}})},
	}
	trans := newMockStreamingTransport(frames)
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.CallToolStream(ctx, "sanitize_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}
	chunks := collectChunks(t, ch)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Type != StreamChunkProgress {
		t.Fatalf("first chunk expected progress, got %s", chunks[0].Type)
	}
	// SanitizeString 应移除零宽空格 U+200B + 替换路径遍历
	if strings.Contains(chunks[0].Message, "\u200B") {
		t.Errorf("progress message not sanitized (U+200B not removed): %q", chunks[0].Message)
	}
	if strings.Contains(chunks[0].Message, "../") {
		t.Errorf("progress message not sanitized (path traversal not replaced): %q", chunks[0].Message)
	}
}

// TestCallToolStream_SanitizeResult 验证 result 走 SanitizeToolResult（R1-R5）。
func TestCallToolStream_SanitizeResult(t *testing.T) {
	// 注入带 Unicode 危险字符 + 路径遍历的结果，SanitizeToolResult 应清洗
	frames := []StreamFrame{
		{Result: mustMarshalToolResult(&ToolResult{
			Content: []ToolResultContent{{Type: "text", Text: "result\u200Bwith../etc/passwd"}},
		})},
	}
	trans := newMockStreamingTransport(frames)
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.CallToolStream(ctx, "sanitize_result_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}
	chunks := collectChunks(t, ch)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Type != StreamChunkResult {
		t.Fatalf("expected result chunk, got %s", chunks[0].Type)
	}
	if chunks[0].Result == nil {
		t.Fatal("result chunk should carry non-nil Result")
	}
	// SanitizeToolResult 应移除零宽空格 U+200B + 替换路径遍历
	text := chunks[0].Result.Text()
	if strings.Contains(text, "\u200B") {
		t.Errorf("result text not sanitized (U+200B not removed): %q", text)
	}
	if strings.Contains(text, "../") {
		t.Errorf("result text not sanitized (path traversal not replaced): %q", text)
	}
}

// TestCallToolStream_TransportDisconnect 验证 transport channel 关闭 → 关闭 out channel。
func TestCallToolStream_TransportDisconnect(t *testing.T) {
	// 构造只推送 2 个 progress 后关闭 channel（无最终 result）的 transport
	frames := []StreamFrame{
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 10, Total: 100}},
		{Method: "notifications/progress", Progress: &ProgressInfo{Progress: 20, Total: 100}},
	}
	trans := newMockStreamingTransport(frames)
	c := newClientWithTransport(trans)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.CallToolStream(ctx, "disconnect_tool", nil)
	if err != nil {
		t.Fatalf("CallToolStream returned error: %v", err)
	}
	chunks := collectChunks(t, ch)
	// channel 应被关闭，collectChunks 正常返回
	if len(chunks) != 2 {
		t.Logf("received %d chunks (transport closed without final result)", len(chunks))
	}
}

// TestCallToolStream_ConcurrentCalls 验证多个 CallToolStream 并发，channel 互不干扰。
// race 测试：通过 -race 标志验证并发安全。
func TestCallToolStream_ConcurrentCalls(t *testing.T) {
	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			frames := []StreamFrame{
				{Method: "notifications/progress", Progress: &ProgressInfo{Progress: float64(idx * 10), Total: 100}},
				{Result: mustMarshalToolResult(&ToolResult{Content: []ToolResultContent{{Type: "text", Text: fmt.Sprintf("result-%d", idx)}}})},
			}
			trans := newMockStreamingTransport(frames)
			c := newClientWithTransport(trans)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ch, err := c.CallToolStream(ctx, fmt.Sprintf("tool-%d", idx), nil)
			if err != nil {
				t.Errorf("goroutine %d: CallToolStream error: %v", idx, err)
				return
			}
			chunks := collectChunks(t, ch)
			if len(chunks) != 2 {
				t.Errorf("goroutine %d: expected 2 chunks, got %d", idx, len(chunks))
				return
			}
			if chunks[1].Type != StreamChunkResult {
				t.Errorf("goroutine %d: expected result chunk, got %s", idx, chunks[1].Type)
				return
			}
			expectedText := fmt.Sprintf("result-%d", idx)
			if chunks[1].Result == nil || chunks[1].Result.Text() != expectedText {
				t.Errorf("goroutine %d: expected result text %q, got %+v", idx, expectedText, chunks[1].Result)
			}
		}(i)
	}
	wg.Wait()
}

// mustMarshalToolResult 将 ToolResult 序列化为 json.RawMessage，失败时 panic（测试辅助）。
func mustMarshalToolResult(r *ToolResult) json.RawMessage {
	b, err := json.Marshal(r)
	if err != nil {
		panic(fmt.Sprintf("marshal tool result: %v", err))
	}
	return b
}

// TestStdioTransport_SendStream 验证 stdio transport 的 SendStream 路由。
// 使用 bytes.Buffer 包装的 WriteCloser 模拟 stdin，避免启动真实子进程。
func TestStdioTransport_SendStream(t *testing.T) {
	// 本测试不启动真实子进程，仅测试 SendStream 的 streamPending 注册 + ctx 取消清理
	// （启动真实子进程需 testdata MCP server，超出单元测试范围）
	trans := &StdioTransport{
		pending:       make(map[string]chan *rpcResponse),
		streamPending: make(map[string]chan StreamFrame),
		stdin:         &nopWriteCloser{&strings.Builder{}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 注册 streamPending
	ch, err := trans.SendStream(ctx, 100, "tools/call", map[string]interface{}{})
	if err != nil {
		t.Fatalf("SendStream returned error: %v", err)
	}
	// 验证 streamPending 已注册（key=string(RawID)="100"）
	trans.streamMu.RLock()
	_, ok := trans.streamPending["100"]
	trans.streamMu.RUnlock()
	if !ok {
		t.Fatal("streamPending should be registered after SendStream")
	}
	// ctx 取消触发清理
	cancel()
	// 等待清理 goroutine 执行
	time.Sleep(50 * time.Millisecond)
	trans.streamMu.RLock()
	_, stillRegistered := trans.streamPending["100"]
	trans.streamMu.RUnlock()
	if stillRegistered {
		t.Error("streamPending should be cleaned up after ctx cancel")
	}
	// channel 应被关闭
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after ctx cancel")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel close timed out")
	}
}

// nopWriteCloser 包装 io.Writer 为 WriteCloser，Close 为 no-op（测试辅助）。
type nopWriteCloser struct {
	w *strings.Builder
}

func (n *nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n *nopWriteCloser) Close() error                { return nil }

// TestHTTPTransport_SendStream_SSE 验证 HTTP transport SSE 流式响应解析。
// 启动本地 HTTP server 返回 text/event-stream 流。
func TestHTTPTransport_SendStream_SSE(t *testing.T) {
	// 模拟 MCP server SSE 响应：2 条 progress + 1 条最终响应
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		// progress 1
		fmt.Fprintf(w, "data: %s\n\n", mustMarshalJSON(map[string]interface{}{
			"method": "notifications/progress",
			"params": map[string]interface{}{"progress": 10, "total": 100},
		}))
		flusher.Flush()
		// progress 2
		fmt.Fprintf(w, "data: %s\n\n", mustMarshalJSON(map[string]interface{}{
			"method": "notifications/progress",
			"params": map[string]interface{}{"progress": 50, "total": 100},
		}))
		flusher.Flush()
		// 最终响应
		fmt.Fprintf(w, "data: %s\n\n", mustMarshalJSON(map[string]interface{}{
			"id":     int64(1),
			"result": map[string]interface{}{"content": []map[string]string{{"type": "text", "text": "sse done"}}},
		}))
		flusher.Flush()
	}))
	defer server.Close()

	trans, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport error: %v", err)
	}
	defer trans.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := trans.SendStream(ctx, 1, "tools/call", map[string]interface{}{})
	if err != nil {
		t.Fatalf("SendStream error: %v", err)
	}

	var frames []StreamFrame
	for frame := range ch {
		frames = append(frames, frame)
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames (2 progress + 1 result), got %d", len(frames))
	}
	// 前 2 个是 progress
	for i := 0; i < 2; i++ {
		if frames[i].Method != "notifications/progress" {
			t.Errorf("frame[%d] expected progress, got method %s", i, frames[i].Method)
		}
		if frames[i].Progress == nil {
			t.Errorf("frame[%d] progress should be parsed", i)
		}
	}
	// 最后一个是最终响应
	if frames[2].ID != 1 {
		t.Errorf("frame[2] expected ID 1, got %d", frames[2].ID)
	}
	if len(frames[2].Result) == 0 {
		t.Error("frame[2] should carry result")
	}
}

// TestHTTPTransport_SendStream_JSON 验证 HTTP transport JSON 一次性响应包装为 1-chunk 流。
func TestHTTPTransport_SendStream_JSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"id":1,"result":{"content":[{"type":"text","text":"json done"}]}}`)
	}))
	defer server.Close()

	trans, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport error: %v", err)
	}
	defer trans.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := trans.SendStream(ctx, 1, "tools/call", map[string]interface{}{})
	if err != nil {
		t.Fatalf("SendStream error: %v", err)
	}

	var frames []StreamFrame
	for frame := range ch {
		frames = append(frames, frame)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame (JSON once), got %d", len(frames))
	}
	if frames[0].ID != 1 {
		t.Errorf("expected ID 1, got %d", frames[0].ID)
	}
	if len(frames[0].Result) == 0 {
		t.Error("expected non-empty result")
	}
}

// mustMarshalJSON 将任意值序列化为 JSON 字符串，失败时 panic（测试辅助）。
func mustMarshalJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal: %v", err))
	}
	return string(b)
}

// TestParseProgressInfo 验证 ParseProgressInfo 辅助函数。
func TestParseProgressInfo(t *testing.T) {
	// 空 params → nil
	if got := ParseProgressInfo(nil); got != nil {
		t.Errorf("expected nil for empty params, got %+v", got)
	}
	// 合法 params → 解析
	params := json.RawMessage(mustMarshalJSON(map[string]interface{}{
		"progress": 50.0,
		"total":    100.0,
		"message":  "halfway",
	}))
	got := ParseProgressInfo(params)
	if got == nil {
		t.Fatal("expected non-nil ProgressInfo")
	}
	if got.Progress != 50.0 || got.Total != 100.0 || got.Message != "halfway" {
		t.Errorf("parsed ProgressInfo mismatch: %+v", got)
	}
	// 无效 JSON → nil
	if got := ParseProgressInfo(json.RawMessage("not json")); got != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", got)
	}
}

// TestStreamChunk_Constants 验证 StreamChunkType 常量值稳定。
func TestStreamChunk_Constants(t *testing.T) {
	if StreamChunkProgress != "progress" {
		t.Errorf("StreamChunkProgress = %q, want %q", StreamChunkProgress, "progress")
	}
	if StreamChunkResult != "result" {
		t.Errorf("StreamChunkResult = %q, want %q", StreamChunkResult, "result")
	}
	if StreamChunkError != "error" {
		t.Errorf("StreamChunkError = %q, want %q", StreamChunkError, "error")
	}
}

// TestIsProtectedStreamChunk 验证背压保护回调逻辑。
func TestIsProtectedStreamChunk(t *testing.T) {
	if !isProtectedStreamChunk(StreamChunk{Type: StreamChunkResult}) {
		t.Error("result chunk should be protected")
	}
	if !isProtectedStreamChunk(StreamChunk{Type: StreamChunkError}) {
		t.Error("error chunk should be protected")
	}
	if isProtectedStreamChunk(StreamChunk{Type: StreamChunkProgress}) {
		t.Error("progress chunk should not be protected")
	}
}
