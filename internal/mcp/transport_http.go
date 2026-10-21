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
	"strings"
	"sync"
	"time"
)

// HTTPTransport is the Streamable HTTP based MCP transport implementation (MCP spec 2025-03-26)
// Supports JSON direct response and SSE stream response, manages session via Mcp-Session-Id header
type HTTPTransport struct {
	serverURL      string              // Server URL
	sessionID      string              // MCP session ID
	httpClient     *http.Client        // HTTP client
	headers        map[string]string   // Custom request headers
	notifHandler   NotificationHandler // Notification callback
	requestHandler RequestHandler      // Server-side 请求回调（第3轮修复：致命漏洞 #1）
	lastEventID    string              // Last received SSE event ID (for reconnection recovery)
	mu             sync.RWMutex        // Protects sessionID, lastEventID, notifHandler, requestHandler
	closed         bool                // Whether closed
	closeMu        sync.Mutex          // Protects close operation
	sseCancel      context.CancelFunc  // Cancel SSE listening
}

// NewHTTPTransport creates Streamable HTTP based MCP transport
// Supports both JSON direct response and SSE stream response modes
func NewHTTPTransport(ctx context.Context, serverURL string, headers map[string]string) (*HTTPTransport, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("HTTP server URL cannot be empty")
	}
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		return nil, fmt.Errorf("HTTP server URL must start with http:// or https://")
	}

	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		MaxConnsPerHost:     10,
		IdleConnTimeout:     90 * time.Second,
	}
	return &HTTPTransport{
		serverURL:  serverURL,
		httpClient: &http.Client{Transport: transport},
		headers:    headers,
	}, nil
}

// Send sends JSON-RPC request and waits for response
// Auto-handles JSON direct response or SSE stream response based on server Content-Type
func (t *HTTPTransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.serverURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	// Set session ID
	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.lastEventID != "" {
		httpReq.Header.Set("Last-Event-ID", t.lastEventID)
	}
	t.mu.RUnlock()

	// Set custom request headers
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Save session ID
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")

	// Handle based on response type
	if strings.Contains(contentType, "text/event-stream") {
		return t.handleSSEResponse(resp, id)
	}

	// JSON direct response
	return t.handleJSONResponse(resp)
}

// handleJSONResponse handles JSON direct response
func (t *HTTPTransport) handleJSONResponse(resp *http.Response) (json.RawMessage, error) {
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
		return nil, &RPCError{Code: result.Error.Code, Message: result.Error.Message}
	}

	return result.Result, nil
}

// handleSSEResponse handles SSE stream response
// Reads response matching request ID from SSE event stream
func (t *HTTPTransport) handleSSEResponse(resp *http.Response, requestID int64) (json.RawMessage, error) {
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// Parse SSE event ID
		if strings.HasPrefix(line, "id:") {
			eventID := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			t.mu.Lock()
			t.lastEventID = eventID
			t.mu.Unlock()
			continue
		}

		// Parse SSE event data
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			var msg struct {
				ID     int64           `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *RPCError       `json:"error"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal([]byte(data), &msg); err != nil {
				continue
			}

			// Handle notification
			if msg.Method != "" && t.notifHandler != nil {
				t.notifHandler(msg.Method, msg.Params)
				continue
			}

			// Match request ID
			if msg.ID == requestID {
				if msg.Error != nil {
					return nil, msg.Error
				}
				return msg.Result, nil
			}
		}
	}

	return nil, fmt.Errorf("SSE stream ended without receiving response for request %d", requestID)
}

// SendNotification sends JSON-RPC notification (no ID, no response expected)
func (t *HTTPTransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.serverURL, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Set session ID
	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.RUnlock()

	// Set custom request headers
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	defer resp.Body.Close()

	// Notifications typically return 202 Accepted
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// SetNotificationHandler sets notification callback
// 并发安全：通过 mu 锁保护 notifHandler 字段，避免与 StartSSEListener 并发读竞态（建议优化 #5）
func (t *HTTPTransport) SetNotificationHandler(handler NotificationHandler) {
	t.mu.Lock()
	t.notifHandler = handler
	t.mu.Unlock()
}

// SetRequestHandler 注册 server-side 请求回调（第3轮修复：新引入致命漏洞 #1）
// 对标 SetNotificationHandler 模式：handler=nil 清除回调
// StartSSEListener 收到 id + method 同时存在的消息时调用此 handler
//
// 并发安全：通过 mu 锁保护 requestHandler 字段，避免与 StartSSEListener 并发读竞态（建议优化 #2）
func (t *HTTPTransport) SetRequestHandler(handler RequestHandler) {
	t.mu.Lock()
	t.requestHandler = handler
	t.mu.Unlock()
}

// SendResponse 发送 JSON-RPC success response（第2轮修复：致命漏洞 #1 前置条件）
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
// awaitElicitationResponse goroutine 通过此方法异步发送响应
//
// HTTP transport 通过 HTTP POST 发送响应（与请求同路径，server 应能识别 response 消息）
func (t *HTTPTransport) SendResponse(reqID json.RawMessage, result interface{}) error {
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
func (t *HTTPTransport) sendSuccessResponse(reqID json.RawMessage, result interface{}) {
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
	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.RUnlock()
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
func (t *HTTPTransport) sendErrorResponse(reqID json.RawMessage, rpcErr *RPCError) {
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
	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.RUnlock()
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
func (t *HTTPTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true

	if t.sseCancel != nil {
		t.sseCancel()
	}

	if transport, ok := t.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}

	return nil
}

// SessionID returns the current MCP session ID
func (t *HTTPTransport) SessionID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionID
}

// SetLastEventID sets the last received SSE event ID (for reconnection recovery)
func (t *HTTPTransport) SetLastEventID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastEventID = id
}

// SendStream 发送 JSON-RPC 请求并返回流式响应 channel（/ T1-P1-A3）。
//
// HTTP Streamable 传输的流式语义（对标 MCP 规范 2025-06-18 §"Transports"，访问日期 2026-07-21）：
// - 请求带 Accept: text/event-stream, application/json
// - server 返回 text/event-stream：持续读取 SSE 事件，每条 data: 推一个 StreamFrame
// - server 返回 application/json：包装为 1-chunk 流（仅含最终响应）
// - SSE 事件可能是 notifications/progress（推 Progress StreamFrame）或最终响应（推 Result StreamFrame）
//
// goroutine 生命周期：
// - SSE 读取完毕或 JSON 解析完成 → close(ch) 退出
// - ctx 取消 → resp.Body.Close() 触发读取错误 → close(ch) 退出
func (t *HTTPTransport) SendStream(ctx context.Context, id int64, method string, params interface{}) (<-chan StreamFrame, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.serverURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, application/json")

	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.lastEventID != "" {
		httpReq.Header.Set("Last-Event-ID", t.lastEventID)
	}
	t.mu.RUnlock()

	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")
	ch := make(chan StreamFrame, 16)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		if strings.Contains(contentType, "text/event-stream") {
			t.streamSSEEvents(resp, id, ch, ctx)
		} else {
			t.streamJSONOnce(resp, id, ch)
		}
	}()

	return ch, nil
}

// streamSSEEvents 从 HTTP SSE 响应流中解析事件并推送到 channel。
//
// 对标 MCP 规范 2025-06-18 §"Transports" Streamable HTTP（访问日期 2026-07-21）：
// - 每条 SSE data: 行是一个 JSON-RPC 消息（响应或通知）
// - 匹配请求 ID 的响应 → StreamFrame{ID, Result, Error}（最终结果）
// - notifications/progress 通知 → StreamFrame{Method, Params, Progress}
// - 其他通知 → 转发给 notifHandler（若有）
func (t *HTTPTransport) streamSSEEvents(resp *http.Response, requestID int64, ch chan<- StreamFrame, ctx context.Context) {
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		// 解析 SSE 事件 ID（用于重连恢复）
		if strings.HasPrefix(line, "id:") {
			eventID := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			t.mu.Lock()
			t.lastEventID = eventID
			t.mu.Unlock()
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var msg struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}

		// 进度通知 → StreamFrame{Progress}
		if msg.Method == "notifications/progress" {
			frame := StreamFrame{
				Method:   msg.Method,
				Params:   msg.Params,
				Progress: ParseProgressInfo(msg.Params),
			}
			select {
			case ch <- frame:
			case <-ctx.Done():
				return
			}
			continue
		}

		// 其他通知 → 转发给 notifHandler（不推送到 stream channel）
		if msg.Method != "" {
			if t.notifHandler != nil {
				t.notifHandler(msg.Method, msg.Params)
			}
			continue
		}

		// 匹配请求 ID 的响应 → StreamFrame{ID, Result, Error}（最终结果）
		if msg.ID == requestID {
			frame := StreamFrame{
				ID:     msg.ID,
				Result: msg.Result,
				Error:  msg.Error,
			}
			select {
			case ch <- frame:
			case <-ctx.Done():
				return
			}
			return // 最终结果已推送，流结束
		}
	}
}

// streamJSONOnce 处理 JSON 一次性响应，包装为 1-chunk 流。
//
// 当 server 不支持 SSE 流式（返回 application/json）时使用此路径。
// 对标 MCP 规范 2025-06-18：server 可以选择不流式，client 应透明处理。
func (t *HTTPTransport) streamJSONOnce(resp *http.Response, requestID int64, ch chan<- StreamFrame) {
	var result struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		ch <- StreamFrame{
			ID:    requestID,
			Error: &RPCError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)},
		}
		return
	}
	ch <- StreamFrame{
		ID:     result.ID,
		Result: result.Result,
		Error:  result.Error,
	}
}

// StartSSEListener 启动 SSE 事件监听 goroutine，接收 server 主动推送的通知与 server-side 请求。
//
// 第3轮修复（致命漏洞 #1/#2）：StartSSEListener 改为三路径分发
// - 路径 1：id != nil && method != "" → 请求（调用 requestHandler，识别 errElicitationPending 哨兵不发响应）
// - 路径 2：id != nil && method == "" → 响应（HTTP SSE listener 不处理响应，响应由 Send/handleSSEResponse 内联处理；此处仅记录日志后忽略）
// - 路径 3：id == nil && method != "" → 通知（转发给 notifHandler）
// 分发优先级：请求 > 响应 > 通知（避免 server-side 请求被误判为响应）
//
// 并发安全（B3a-P1-3）：写入 sseCancel 时持有 closeMu 锁，与 Close() 读取 sseCancel 形成互斥，
// 消除 sseCancel 字段的 data race。
func (t *HTTPTransport) StartSSEListener(ctx context.Context) {
	sseCtx, cancel := context.WithCancel(ctx)
	// B3a-P1-3：closeMu 保护 sseCancel 写入，避免与 Close() 并发读竞态
	t.closeMu.Lock()
	t.sseCancel = cancel
	t.closeMu.Unlock()

	baseDelay := 1 * time.Second
	maxDelay := 30 * time.Second
	delay := baseDelay

	for {
		select {
		case <-sseCtx.Done():
			return
		default:
		}

		req, err := http.NewRequestWithContext(sseCtx, "GET", t.serverURL, nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "text/event-stream")

		t.mu.RLock()
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		if t.lastEventID != "" {
			req.Header.Set("Last-Event-ID", t.lastEventID)
		}
		t.mu.RUnlock()

		for k, v := range t.headers {
			req.Header.Set(k, v)
		}

		resp, err := t.httpClient.Do(req)
		if err != nil {
			select {
			case <-sseCtx.Done():
				return
			default:
				time.Sleep(delay)
				delay = minDuration(delay*2, maxDelay)
				continue
			}
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "id:") {
				eventID := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
				t.mu.Lock()
				t.lastEventID = eventID
				t.mu.Unlock()
				continue
			}

			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")

				// 第3轮修复：msg 结构改用 readLoopMsg（RawID 字段）
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
					// HTTP SSE listener 不处理响应，响应由 Send/handleSSEResponse 内联处理
					// 此处忽略，避免误将响应路由到 notifHandler
				} else if msg.Method != "" {
					// 路径 3：通知（id 不存在，method 存在）
					t.handleNotification(msg)
				}
			}
		}
		resp.Body.Close()
		delay = baseDelay
	}
}

// handleServerSideRequest 处理 server-side JSON-RPC 请求（第3轮修复：致命漏洞 #1）
// 调用 requestHandler 处理请求，按返回值契约决定是否发送响应
// 建议优化 #2：读取 requestHandler 时加 RLock 保护，避免与 SetRequestHandler 并发写竞态
func (t *HTTPTransport) handleServerSideRequest(msg readLoopMsg) {
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

// handleNotification 处理 JSON-RPC 通知（原 StartSSEListener 的 msg.Method != "" 路径）
// 原逻辑保留，仅参数类型从匿名 struct 改为 readLoopMsg
func (t *HTTPTransport) handleNotification(msg readLoopMsg) {
	t.mu.RLock()
	handler := t.notifHandler
	t.mu.RUnlock()
	if handler != nil {
		handler(msg.Method, msg.Params)
	}
}
