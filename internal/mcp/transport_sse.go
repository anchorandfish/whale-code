package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ssePendingEntry struct {
	ch      chan *rpcResponse
	created time.Time
}

// SSETransport is the SSE (Server-Sent Events) based MCP transport implementation
// Uses HTTP POST to send requests, GET /events to listen for server-pushed notifications
type SSETransport struct {
	serverURL      string                      // SSE server URL
	httpClient     *http.Client                // HTTP client
	pending        map[string]*ssePendingEntry // Pending request mapping；key=string(RawID)，向后兼容客户端 number ID
	mu             sync.RWMutex                // Protects pending map + notifHandler + requestHandler
	notifHandler   NotificationHandler         // Notification callback
	requestHandler RequestHandler              // Server-side 请求回调（第3轮修复：致命漏洞 #1）
	headers        map[string]string           // Custom request headers
	closed         bool                        // Whether closed
	closeMu        sync.Mutex                  // Protects close operation
	pendingTTL     time.Duration               // TTL for pending entries
	// 流式请求路由表（SendStream 路径，从 sseListen 路由进度通知）
	streamPending map[string]chan StreamFrame // 流式请求 ID → channel 映射；key=string(RawID)
	streamMu      sync.RWMutex                // 保护 streamPending map
}

// NewSSETransport creates SSE-based MCP transport
// Sends requests via HTTP POST, receives notifications via SSE event stream
func NewSSETransport(ctx context.Context, serverURL string, headers map[string]string) (*SSETransport, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("SSE server URL cannot be empty")
	}
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		return nil, fmt.Errorf("SSE server URL must start with http:// or https://")
	}

	t := &SSETransport{
		serverURL:     serverURL,
		httpClient:    &http.Client{},
		pending:       make(map[string]*ssePendingEntry),
		headers:       headers,
		pendingTTL:    5 * time.Minute,
		streamPending: make(map[string]chan StreamFrame),
	}

	go t.sseListen(ctx)
	go t.cleanupPending(ctx)

	return t, nil
}

// Send sends JSON-RPC request and waits for response
// Sends request via HTTP POST, gets result directly from HTTP response
func (t *SSETransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	return t.sendHTTP(ctx, req)
}

// SendNotification sends JSON-RPC notification (no ID, no response expected)
func (t *SSETransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	_, err := t.sendHTTP(ctx, req)
	return err
}

// SetNotificationHandler sets notification callback
// 并发安全：通过 mu 锁保护 notifHandler 字段，避免与 sseListen 并发读竞态（建议优化 #5）
func (t *SSETransport) SetNotificationHandler(handler NotificationHandler) {
	t.mu.Lock()
	t.notifHandler = handler
	t.mu.Unlock()
}

// SetRequestHandler 注册 server-side 请求回调（第3轮修复：新引入致命漏洞 #1）
// 对标 SetNotificationHandler 模式：handler=nil 清除回调
// sseListen 收到 id + method 同时存在的消息时调用此 handler
//
// 并发安全：通过 mu 锁保护 requestHandler 字段，避免与 sseListen 并发读竞态（建议优化 #2）
func (t *SSETransport) SetRequestHandler(handler RequestHandler) {
	t.mu.Lock()
	t.requestHandler = handler
	t.mu.Unlock()
}

// SendResponse 发送 JSON-RPC success response（第2轮修复：致命漏洞 #1 前置条件）
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
// awaitElicitationResponse goroutine 通过此方法异步发送响应
//
// SSE transport 通过 HTTP POST 发送响应（与请求同路径，server 应能识别 response 消息）
func (t *SSETransport) SendResponse(reqID json.RawMessage, result interface{}) error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return fmt.Errorf("transport is closed")
	}
	t.closeMu.Unlock()
	t.sendSuccessResponse(reqID, result)
	return nil
}

// sendSuccessResponse 通过 HTTP POST 发送 JSON-RPC success response
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
//
// 并发安全（B3a-P1-4）：使用 context.WithTimeout(30s) 替代 context.Background()，
// 避免 MCP server 不响应时 goroutine 永久阻塞在 httpClient.Do()。
func (t *SSETransport) sendSuccessResponse(reqID json.RawMessage, result interface{}) {
	respMsg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(reqID),
		"result":  result,
	}
	respJSON, err := json.Marshal(respMsg)
	if err != nil {
		return
	}
	// B3a-P1-4：30s 超时，避免 server 不响应导致 goroutine 永久阻塞
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.serverURL, bytes.NewReader(respJSON))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return
	}
	httpResp.Body.Close()
}

// sendErrorResponse 通过 HTTP POST 发送 JSON-RPC error response
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
//
// 并发安全（B3a-P1-4）：使用 context.WithTimeout(30s) 替代 context.Background()，
// 避免 MCP server 不响应时 goroutine 永久阻塞在 httpClient.Do()。
func (t *SSETransport) sendErrorResponse(reqID json.RawMessage, rpcErr *RPCError) {
	respMsg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(reqID),
		"error":   rpcErr,
	}
	respJSON, err := json.Marshal(respMsg)
	if err != nil {
		return
	}
	// B3a-P1-4：30s 超时，避免 server 不响应导致 goroutine 永久阻塞
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.serverURL, bytes.NewReader(respJSON))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return
	}
	httpResp.Body.Close()
}

// Close closes transport connection
func (t *SSETransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	return nil
}

// sendHTTP sends JSON-RPC request via HTTP POST
func (t *SSETransport) sendHTTP(ctx context.Context, req interface{}) (json.RawMessage, error) {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.serverURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Set custom request headers
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", result.Error.Code, result.Error.Message)
	}

	return result.Result, nil
}

// sseListen listens to SSE event stream
// Continuously connects to /events endpoint, receives server-pushed notifications and responses
//
// 第3轮修复（致命漏洞 #1/#2）：sseListen 改为三路径分发
// - 路径 1：id != nil && method != "" → 请求（调用 requestHandler，识别 errElicitationPending 哨兵不发响应）
// - 路径 2：id != nil && method == "" → 响应（路由到 pending/streamPending channel）
// - 路径 3：id == nil && method != "" → 通知（转发给 notifHandler）
// 分发优先级：请求 > 响应 > 通知（避免 server-side 请求被误判为响应）
func (t *SSETransport) sseListen(ctx context.Context) {
	baseDelay := 1 * time.Second
	maxDelay := 30 * time.Second
	delay := baseDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", t.serverURL+"/events", nil)
		if err != nil {
			time.Sleep(delay)
			delay = minDuration(delay*2, maxDelay)
			continue
		}
		req.Header.Set("Accept", "text/event-stream")

		for k, v := range t.headers {
			req.Header.Set(k, v)
		}

		resp, err := t.httpClient.Do(req)
		if err != nil {
			time.Sleep(delay)
			delay = minDuration(delay*2, maxDelay)
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var msg readLoopMsg
			if err := json.Unmarshal([]byte(data), &msg); err != nil {
				continue
			}

			// === 第3轮修复：三路径分发 ===
			if msg.hasID() && msg.Method != "" {
				// 路径 1：server-side 请求（id + method 同时存在）
				t.handleServerSideRequest(msg)
			} else if msg.hasID() {
				// 路径 2：响应（id 存在，method 为空）
				t.handleResponse(msg)
			} else if msg.Method != "" {
				// 路径 3：通知（id 不存在，method 存在）
				t.handleNotification(msg)
			}
		}
		resp.Body.Close()

		t.mu.Lock()
		for id, entry := range t.pending {
			close(entry.ch)
			delete(t.pending, id)
		}
		t.mu.Unlock()

		// 连接断开时清理 streamPending（推送 Error 后关闭）
		t.streamMu.Lock()
		for id, ch := range t.streamPending {
			close(ch)
			delete(t.streamPending, id)
		}
		t.streamMu.Unlock()

		delay = baseDelay
	}
}

// handleServerSideRequest 处理 server-side JSON-RPC 请求（第3轮修复：致命漏洞 #1）
// SSE transport 版本：调用 requestHandler 处理请求，按返回值契约决定是否发送响应
// 建议优化 #2：读取 requestHandler 时加 RLock 保护，避免与 SetRequestHandler 并发写竞态
func (t *SSETransport) handleServerSideRequest(msg readLoopMsg) {
	t.mu.RLock()
	handler := t.requestHandler
	t.mu.RUnlock()
	if handler == nil {
		// fail-safe：无 requestHandler 时发送 method not found error response
		t.sendErrorResponse(msg.RawID, &RPCError{
			Code:    -32601,
			Message: "method not found",
		})
		return
	}
	result, err := handler(msg.Method, msg.Params, msg.RawID)
	if errors.Is(err, errElicitationPending) {
		// 哨兵：不发响应，由 awaitElicitationResponse goroutine 异步发送
		return
	}
	if err != nil {
		t.sendErrorResponse(msg.RawID, &RPCError{
			Code:    -32603,
			Message: err.Error(),
		})
		return
	}
	if result != nil {
		t.sendSuccessResponse(msg.RawID, result)
	}
}

// handleResponse 处理 JSON-RPC 响应（原 sseListen 的 msg.ID > 0 路径）
// 第3轮修复（致命漏洞 #2）：ID 索引从 int64 改为 string(RawID)
//
// P0-6 修复：防止 send on closed channel panic（三方竞态）。
// streamPending 路径：SendStream 清理 goroutine（ctx 取消）或 sseListen 连接断开清理
// 可能已关闭 streamCh，导致 send panic。
// pending 路径：cleanupPending（TTL 过期）或 sseListen 连接断开清理可能已关闭 entry.ch。
// 使用 defer recover() 防护 send，并在 close 前检查 map 是否仍持有 entry（避免 double close）。
// 注意：不使用 select+default 丢弃帧（会导致调用方永久阻塞等待最终响应）。
func (t *SSETransport) handleResponse(msg readLoopMsg) {
	idKey := string(msg.RawID)
	// 优先检查 streamPending（SendStream 路径）
	t.streamMu.RLock()
	streamCh, streamOk := t.streamPending[idKey]
	t.streamMu.RUnlock()
	if streamOk {
		frame := StreamFrame{
			ID:     parseIDToInt64(msg.RawID),
			Result: msg.Result,
		}
		// P0-6 修复：defer recover 防止 send on closed channel panic
		func() {
			defer func() {
				_ = recover()
			}()
			streamCh <- frame
		}()
		// 清理：仅当 channel 仍在 map 中时才 delete + close（避免 double close）
		t.streamMu.Lock()
		if _, ok := t.streamPending[idKey]; ok {
			delete(t.streamPending, idKey)
			close(streamCh)
		}
		t.streamMu.Unlock()
		return
	}

	rpcResp := &rpcResponse{
		ID:     parseIDToInt64(msg.RawID),
		Result: msg.Result,
	}
	t.mu.RLock()
	entry, ok := t.pending[idKey]
	t.mu.RUnlock()
	if ok {
		// P0-6 修复：defer recover 防止 send on closed channel panic
		// cleanupPending / sseListen 连接断开清理可能已关闭 entry.ch
		func() {
			defer func() {
				_ = recover()
			}()
			entry.ch <- rpcResp
		}()
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
	}
}

// handleNotification 处理 JSON-RPC 通知（原 sseListen 的 msg.Method != "" 路径）
// 原逻辑保留，仅参数类型从匿名 struct 改为 readLoopMsg
func (t *SSETransport) handleNotification(msg readLoopMsg) {
	// notifications/progress 路由到所有活跃 streamPending
	if msg.Method == "notifications/progress" {
		frame := StreamFrame{
			Method:   msg.Method,
			Params:   msg.Params,
			Progress: ParseProgressInfo(msg.Params),
		}
		t.streamMu.RLock()
		for _, ch := range t.streamPending {
			select {
			case ch <- frame:
			default:
				// channel 满，跳过
			}
		}
		t.streamMu.RUnlock()
	}
	// 转发给外部通知回调
	t.mu.RLock()
	handler := t.notifHandler
	t.mu.RUnlock()
	if handler != nil {
		handler(msg.Method, msg.Params)
	}
}

// SendStream 发送 JSON-RPC 请求并返回流式响应 channel（/ T1-P1-A3）。
//
// SSE 传输的流式语义（对标 MCP 规范 2025-06-18 §"Transports"，访问日期 2026-07-21）：
// - 请求通过 HTTP POST 发送（sendHTTP）
// - 响应 + notifications/progress 通过 sseListen 从 /events 路由到 streamPending[id]
// - channel 在 sseListen 收到匹配 ID 的响应后关闭
//
// 注意：SSE 传输的 sendHTTP 会从 POST 响应体读取同步响应；
// 若 POST 响应已包含结果，SendStream 把它作为首个 frame 推送；
// sseListen 的 /events 流提供额外的 progress 通知。
//
// goroutine 生命周期：
// - ctx 取消 → 清理 streamPending + 关闭 channel
// - sseListen 收到最终响应 → 关闭 channel + 清理 streamPending
// - sseListen 连接断开 → defer 清理所有 streamPending
func (t *SSETransport) SendStream(ctx context.Context, id int64, method string, params interface{}) (<-chan StreamFrame, error) {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	t.closeMu.Unlock()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	// 预校验 req 可序列化（实际字节由 sendHTTP 内部再次 marshal，此处仅做前置检查）
	if _, err := json.Marshal(req); err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan StreamFrame, 16)
	idKey := strconv.FormatInt(id, 10)
	t.streamMu.Lock()
	t.streamPending[idKey] = ch
	t.streamMu.Unlock()

	// 发送请求（sendHTTP 同步返回 POST 响应）
	go func() {
		defer func() {
			t.streamMu.Lock()
			if _, ok := t.streamPending[idKey]; ok {
				delete(t.streamPending, idKey)
				close(ch)
			}
			t.streamMu.Unlock()
		}()

		result, err := t.sendHTTP(ctx, req)
		if err != nil {
			select {
			case ch <- StreamFrame{ID: id, Error: &RPCError{Code: -32603, Message: err.Error()}}:
			case <-ctx.Done():
			}
			return
		}
		// POST 响应作为最终结果推送（若 sseListen 未抢先处理）
		t.streamMu.Lock()
		_, stillPending := t.streamPending[idKey]
		t.streamMu.Unlock()
		if stillPending {
			select {
			case ch <- StreamFrame{ID: id, Result: result}:
			case <-ctx.Done():
			}
		}
	}()

	// 启动 goroutine 监听 ctx 取消
	go func() {
		<-ctx.Done()
		t.streamMu.Lock()
		if streamCh, ok := t.streamPending[idKey]; ok {
			delete(t.streamPending, idKey)
			close(streamCh)
		}
		t.streamMu.Unlock()
	}()

	return ch, nil
}

// cleanupPending periodically cleans up expired pending entries
func (t *SSETransport) cleanupPending(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.mu.Lock()
			now := time.Now()
			for id, entry := range t.pending {
				if now.Sub(entry.created) > t.pendingTTL {
					close(entry.ch)
					delete(t.pending, id)
				}
			}
			t.mu.Unlock()
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
