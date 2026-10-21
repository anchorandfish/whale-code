package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ReconnectErrorType 重连错误类型枚举（/ M17-03）。
//
// 对标 gRPC status codes（[grpc.io/docs/guides/status-codes](https://grpc.io/docs/guides/status-codes/) @2026-07-21）：
// - ReconnectErrorAuth：HTTP 401/403 或 WWW-Authenticate Bearer error="invalid_token"
// （对标 gRPC UNAUTHENTICATED=16，立即放弃不重试）
// - ReconnectErrorNetwork：net.Error（Timeout / Temporary）或 DNS 解析失败
// （对标 gRPC UNAVAILABLE=14，指数退避重试）
// - ReconnectErrorServer：HTTP 5xx 或其他 server 返回错误
// （对标 gRPC INTERNAL=13，延长退避重试）
// - ReconnectErrorClosed：主动 Close 触发的退出（不重试）
type ReconnectErrorType int

const (
	// ReconnectErrorAuth 认证失败（401/403 或 invalid_token），立即放弃不重试
	ReconnectErrorAuth ReconnectErrorType = iota
	// ReconnectErrorNetwork 网络错误（DNS/Timeout/Connection refused），指数退避重试
	ReconnectErrorNetwork
	// ReconnectErrorServer 服务端错误（5xx），延长退避重试（base ×2）
	ReconnectErrorServer
	// ReconnectErrorClosed 主动关闭，不重试
	ReconnectErrorClosed
)

// ReconnectError 带类型的重连错误（/ M17-03）。
// 实现 error + Unwrap 接口，兼容 errors.Is/As（[go.dev/blog/go1.13-errors](https://go.dev/blog/go1.13-errors) @2019-10）。
type ReconnectError struct {
	// Type 错误类型
	Type ReconnectErrorType
	// Cause 原始错误
	Cause error
	// Attempt 失败时的重试次数（0-based）
	Attempt int
}

// Error 实现 error 接口
func (e *ReconnectError) Error() string {
	var typeStr string
	switch e.Type {
	case ReconnectErrorAuth:
		typeStr = "auth"
	case ReconnectErrorNetwork:
		typeStr = "network"
	case ReconnectErrorServer:
		typeStr = "server"
	case ReconnectErrorClosed:
		typeStr = "closed"
	default:
		typeStr = "unknown"
	}
	return fmt.Sprintf("reconnect failed (type=%s, attempt=%d): %v", typeStr, e.Attempt, e.Cause)
}

// Unwrap 实现 errors.Is/As 兼容（优化 4 吸纳）
func (e *ReconnectError) Unwrap() error {
	return e.Cause
}

// classifyReconnectError 根据原始错误分类重连错误类型（/ M17-03）。
//
// 分类规则：
// 1. 主动关闭（t.closed=true）→ ReconnectErrorClosed
// 2. HTTP 401/403 → ReconnectErrorAuth
// 3. WWW-Authenticate 头部 Bearer error="invalid_token" → ReconnectErrorAuth（RFC 6750）
// 4. net.Error（Timeout / Temporary）→ ReconnectErrorNetwork
// 5. websocket.CloseError（code 1006/1011）→ ReconnectErrorNetwork / ReconnectErrorServer
// 6. 其他错误 → ReconnectErrorServer（保守策略，重试但延长退避）
//
// 对标 gRPC status codes 标准实践（[grpc.io/docs/guides/status-codes](https://grpc.io/docs/guides/status-codes/) @2026-07-21）：
// - UNAUTHENTICATED=16 不重试（auth）
// - UNAVAILABLE=14 指数退避（network）
// - INTERNAL=13 延长退避（server）
func classifyReconnectError(err error, closed bool, resp *http.Response) ReconnectErrorType {
	// 主动关闭
	if closed {
		return ReconnectErrorClosed
	}
	if err == nil {
		return ReconnectErrorServer
	}
	// 检查 HTTP 响应状态码（dial 失败时 resp 可能为 nil）
	if resp != nil {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ReconnectErrorAuth
		}
		// RFC 6750 Bearer error="invalid_token" 检测（WWW-Authenticate 头部）
		if wwwAuth := resp.Header.Get("WWW-Authenticate"); wwwAuth != "" {
			if strings.Contains(strings.ToLower(wwwAuth), "invalid_token") {
				return ReconnectErrorAuth
			}
		}
		if resp.StatusCode >= 500 {
			return ReconnectErrorServer
		}
	}
	// websocket.CloseError 分类
	if wsErr, ok := err.(*websocket.CloseError); ok {
		// 1006=异常关闭（网络） / 1011=内部错误（server） / 1000=正常关闭
		switch wsErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway:
			return ReconnectErrorClosed
		case websocket.CloseInternalServerErr:
			return ReconnectErrorServer
		default:
			// 1006/1001/1005 等 = 网络异常
			return ReconnectErrorNetwork
		}
	}
	// net.Error 检测（Timeout / Temporary）
	if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
		return ReconnectErrorNetwork
	}
	if netErr, ok := err.(interface{ Temporary() bool }); ok && netErr.Temporary() {
		return ReconnectErrorNetwork
	}
	// 错误消息检测（DNS / connection refused / broken pipe 等）
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "dns") ||
		strings.Contains(errMsg, "timeout") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "eof") {
		return ReconnectErrorNetwork
	}
	// 默认按 server 错误处理（保守重试，延长退避）
	return ReconnectErrorServer
}

// WebSocketTransport is a WebSocket-based MCP transport implementation
// supports bidirectional JSON-RPC message transport, auto-reconnect and mcp sub-protocol
type WebSocketTransport struct {
	serverURL      string                       // WebSocket server URL (ws:// or wss://)
	conn           *websocket.Conn              // WebSocket connection
	pending        map[string]chan *rpcResponse // Pending request mapping；key=string(RawID)，向后兼容客户端 number ID
	mu             sync.RWMutex                 // Protects pending map + notifHandler + requestHandler
	notifHandler   NotificationHandler          // Notification callback
	requestHandler RequestHandler               // Server-side 请求回调（第3轮修复：致命漏洞 #1）
	reconnectMu    sync.Mutex                   // protects reconnect operations
	headers        map[string]string            // custom request headers
	closed         bool                         // Whether closed
	closeMu        sync.Mutex                   // Protects close operation
	readCancel     context.CancelFunc           // Cancel readLoop
	readCtx        context.Context              // Context for readLoop
	// 流式请求路由表（SendStream 路径，从 readLoop 路由进度通知与响应）
	// 第3轮修复（致命漏洞 #2）：key 从 int64 改为 string（用 string(RawID) 索引）
	streamPending map[string]chan StreamFrame // 流式请求 ID → channel 映射；key=string(RawID)
	streamMu      sync.RWMutex                // 保护 streamPending map
}

// NewWebSocketTransport creates a WebSocket-based MCP transport
// connects to WebSocket server and starts message read loop
func NewWebSocketTransport(ctx context.Context, serverURL string, headers map[string]string) (*WebSocketTransport, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("WebSocket server URL cannot be empty")
	}
	if !strings.HasPrefix(serverURL, "ws://") && !strings.HasPrefix(serverURL, "wss://") {
		return nil, fmt.Errorf("WebSocket server URL must start with ws:// or wss://")
	}

	t := &WebSocketTransport{
		serverURL:     serverURL,
		pending:       make(map[string]chan *rpcResponse),
		headers:       headers,
		streamPending: make(map[string]chan StreamFrame),
	}

	if _, err := t.connect(ctx); err != nil {
		// connect 返回 (*http.Response, error)， Dial 失败时 resp 可能为非 nil（如 401 响应）
		return nil, fmt.Errorf("connect: %w", err)
	}

	t.readCtx, t.readCancel = context.WithCancel(context.Background())
	go t.readLoop()

	return t, nil
}

// connect establishes WebSocket connection
// / M17-03：返回 HTTP 响应（供 classifyReconnectError 检查 401/403 头部）
func (t *WebSocketTransport) connect(ctx context.Context) (*http.Response, error) {
	dialer := websocket.DefaultDialer

	// build request headers
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "mcp")
	for k, v := range t.headers {
		header.Set(k, v)
	}

	conn, resp, err := dialer.DialContext(ctx, t.serverURL, header)
	if err != nil {
		return resp, fmt.Errorf("dial websocket: %w", err)
	}

	t.conn = conn
	t.conn.SetReadLimit(1 * 1024 * 1024) // 1MB max message size
	return resp, nil
}

// Send sends JSON-RPC request and waits for response
// Sends request via WebSocket, waits for corresponding ID response via pending map
func (t *WebSocketTransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
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

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan *rpcResponse, 1)
	idKey := strconv.FormatInt(id, 10)
	t.mu.Lock()
	t.pending[idKey] = ch
	t.mu.Unlock()

	t.reconnectMu.Lock()
	err = t.conn.WriteMessage(websocket.TextMessage, reqJSON)
	t.reconnectMu.Unlock()

	if err != nil {
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()

		// try reconnect
		if reconnectErr := t.reconnect(ctx); reconnectErr != nil {
			return nil, fmt.Errorf("write failed: %w, reconnect also failed: %w", err, reconnectErr)
		}

		// reconnect succeeded, resend
		t.mu.Lock()
		t.pending[idKey] = ch
		t.mu.Unlock()

		t.reconnectMu.Lock()
		err = t.conn.WriteMessage(websocket.TextMessage, reqJSON)
		t.reconnectMu.Unlock()

		if err != nil {
			t.mu.Lock()
			delete(t.pending, idKey)
			t.mu.Unlock()
			return nil, fmt.Errorf("write after reconnect: %w", err)
		}
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
		return nil, ctx.Err()
	}
}

// SendNotification sends JSON-RPC notification (no ID, no response expected)
func (t *WebSocketTransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return fmt.Errorf("transport is closed")
	}
	t.closeMu.Unlock()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	t.reconnectMu.Lock()
	err = t.conn.WriteMessage(websocket.TextMessage, reqJSON)
	t.reconnectMu.Unlock()

	return err
}

// SetNotificationHandler sets notification callback
// 并发安全：通过 mu 锁保护 notifHandler 字段，避免与 readLoop 并发读竞态（建议优化 #5）
func (t *WebSocketTransport) SetNotificationHandler(handler NotificationHandler) {
	t.mu.Lock()
	t.notifHandler = handler
	t.mu.Unlock()
}

// SetRequestHandler 注册 server-side 请求回调（第3轮修复：新引入致命漏洞 #1）
// 对标 SetNotificationHandler 模式：handler=nil 清除回调
// readLoop 收到 id + method 同时存在的消息时调用此 handler
//
// 并发安全：通过 mu 锁保护 requestHandler 字段，避免与 readLoop 并发读竞态（建议优化 #2）
func (t *WebSocketTransport) SetRequestHandler(handler RequestHandler) {
	t.mu.Lock()
	t.requestHandler = handler
	t.mu.Unlock()
}

// SendResponse 发送 JSON-RPC success response（第2轮修复：致命漏洞 #1 前置条件）
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
// awaitElicitationResponse goroutine 通过此方法异步发送响应
func (t *WebSocketTransport) SendResponse(reqID json.RawMessage, result interface{}) error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return fmt.Errorf("transport is closed")
	}
	t.closeMu.Unlock()
	t.sendSuccessResponse(reqID, result)
	return nil
}

// sendSuccessResponse 通过 WebSocket 发送 JSON-RPC success response
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
func (t *WebSocketTransport) sendSuccessResponse(reqID json.RawMessage, result interface{}) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(reqID),
		"result":  result,
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		return
	}
	t.reconnectMu.Lock()
	_ = t.conn.WriteMessage(websocket.TextMessage, respJSON)
	t.reconnectMu.Unlock()
}

// sendErrorResponse 通过 WebSocket 发送 JSON-RPC error response
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
func (t *WebSocketTransport) sendErrorResponse(reqID json.RawMessage, rpcErr *RPCError) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(reqID),
		"error":   rpcErr,
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		return
	}
	t.reconnectMu.Lock()
	_ = t.conn.WriteMessage(websocket.TextMessage, respJSON)
	t.reconnectMu.Unlock()
}

// Close closes transport connection
func (t *WebSocketTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true

	if t.readCancel != nil {
		t.readCancel()
	}

	if t.conn != nil {
		// send close frame
		t.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		return t.conn.Close()
	}
	return nil
}

// IsConnected checks if connected
func (t *WebSocketTransport) IsConnected() bool {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	return !t.closed && t.conn != nil
}

// Subprotocol returns the negotiated sub-protocol
func (t *WebSocketTransport) Subprotocol() string {
	if t.conn != nil {
		return t.conn.Subprotocol()
	}
	return ""
}

// reconnect uses exponential backoff strategy for auto-reconnect.
//
// / M17-03：重构为错误分类精细化重连（对标 gRPC status codes）：
// - ReconnectErrorAuth（401/403/invalid_token）：立即放弃，不重试（避免阻塞用户 2.5+ 分钟）
// - ReconnectErrorNetwork（DNS/Timeout/Connection refused）：指数退避重试（500ms/1s/2s/4s/8s，5 次）
// - ReconnectErrorServer（5xx）：延长退避重试（base ×2，即 1s/2s/4s/8s/16s，5 次）
// - ReconnectErrorClosed（主动关闭）：不重试，立即返回
//
// 返回 *ReconnectError（实现 error + Unwrap），调用方可通过 errors.As 提取 Type 字段决策。
func (t *WebSocketTransport) reconnect(ctx context.Context) error {
	t.reconnectMu.Lock()
	defer t.reconnectMu.Unlock()

	if t.readCancel != nil {
		t.readCancel()
	}

	t.closeMu.Lock()
	closed := t.closed
	t.closeMu.Unlock()

	// 主动关闭：立即返回，不重试
	if closed {
		return &ReconnectError{
			Type:    ReconnectErrorClosed,
			Cause:   fmt.Errorf("transport is closed"),
			Attempt: 0,
		}
	}

	maxRetries := 5
	baseDelay := 500 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return &ReconnectError{
				Type:    ReconnectErrorNetwork,
				Cause:   ctx.Err(),
				Attempt: i,
			}
		default:
		}

		// dial 尝试
		resp, err := t.connect(ctx)
		if err == nil {
			// 重连成功
			t.readCtx, t.readCancel = context.WithCancel(context.Background())
			go t.readLoop()
			return nil
		}

		// 错误分类
		errType := classifyReconnectError(err, closed, resp)

		// auth 错误：立即放弃，不重试
		if errType == ReconnectErrorAuth {
			return &ReconnectError{
				Type:    ReconnectErrorAuth,
				Cause:   err,
				Attempt: i,
			}
		}
		// closed 错误：立即返回，不重试
		if errType == ReconnectErrorClosed {
			return &ReconnectError{
				Type:    ReconnectErrorClosed,
				Cause:   err,
				Attempt: i,
			}
		}

		// 计算退避：network 用 base，server 用 base ×2（延长退避）
		delay := baseDelay * time.Duration(1<<uint(i))
		if errType == ReconnectErrorServer {
			delay = delay * 2
		}
		// 退避上限：30s（防止极端场景卡死）
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}

		select {
		case <-ctx.Done():
			return &ReconnectError{
				Type:    ReconnectErrorNetwork,
				Cause:   ctx.Err(),
				Attempt: i,
			}
		case <-time.After(delay):
			// 继续下一轮重试
		}
	}

	return &ReconnectError{
		Type:    ReconnectErrorNetwork,
		Cause:   fmt.Errorf("reconnect failed: exceeded max retry count %d", maxRetries),
		Attempt: maxRetries,
	}
}

// readLoop reads messages from WebSocket persistently
// resolves JSON-RPC responses and notifications, dispatches to corresponding handlers
//
// 同时支持 SendStream 路径——匹配 ID 的响应优先路由到 streamPending，
// 未在 streamPending 中的响应走原 pending 路径；notifications/progress 通知
// 路由到所有活跃 streamPending channel（WS 单连接多路复用）。
//
// 第3轮修复（致命漏洞 #1/#2）：readLoop 改为三路径分发
// - 路径 1：id != nil && method != "" → 请求（调用 requestHandler，识别 errElicitationPending 哨兵不发响应）
// - 路径 2：id != nil && method == "" → 响应（路由到 pending/streamPending channel）
// - 路径 3：id == nil && method != "" → 通知（转发给 notifHandler）
// 分发优先级：请求 > 响应 > 通知（避免 server-side 请求被误判为响应）
func (t *WebSocketTransport) readLoop() {
	defer func() {
		// readLoop 退出时清理 streamPending
		t.streamMu.Lock()
		for id, ch := range t.streamPending {
			close(ch)
			delete(t.streamPending, id)
		}
		t.streamMu.Unlock()
	}()

	for {
		select {
		case <-t.readCtx.Done():
			return
		default:
		}

		_, message, err := t.conn.ReadMessage()
		if err != nil {
			// connection disconnected, if not actively closed, try reconnect
			t.closeMu.Lock()
			wasClosed := t.closed
			t.closeMu.Unlock()

			if !wasClosed {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				t.reconnect(ctx)
			}
			return
		}

		// 第3轮修复：msg 结构改用 readLoopMsg（RawID 字段）
		var msg readLoopMsg
		if err := json.Unmarshal(message, &msg); err != nil {
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
}

// handleServerSideRequest 处理 server-side JSON-RPC 请求（第3轮修复：致命漏洞 #1）
// 调用 requestHandler 处理请求，按返回值契约决定是否发送响应
// 建议优化 #2：读取 requestHandler 时加 RLock 保护，避免与 SetRequestHandler 并发写竞态
func (t *WebSocketTransport) handleServerSideRequest(msg readLoopMsg) {
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

// handleResponse 处理 JSON-RPC 响应（原 readLoop 的 msg.ID > 0 路径）
// 第3轮修复（致命漏洞 #2）：ID 索引从 int64 改为 string(RawID)
//
// P0-5 修复：防止 send on closed channel panic。
// SendStream 的清理 goroutine（ctx 取消时 close）或 readLoop defer（连接断开时 close all）
// 可能已关闭 streamCh，导致 `streamCh <- frame` panic。
// 使用 defer recover() 防护 send，并在 close 前检查 map 是否仍持有 entry（避免 double close）。
// 注意：不使用 select+default 丢弃帧（会导致调用方永久阻塞等待最终响应）。
func (t *WebSocketTransport) handleResponse(msg readLoopMsg) {
	idKey := string(msg.RawID)
	// 优先检查 streamPending（SendStream 路径）
	t.streamMu.RLock()
	streamCh, streamOk := t.streamPending[idKey]
	t.streamMu.RUnlock()
	if streamOk {
		frame := StreamFrame{
			ID:     parseIDToInt64(msg.RawID),
			Result: msg.Result,
			Error:  msg.Error,
		}
		// P0-5 修复：defer recover 防止 send on closed channel panic
		// SendStream 清理 goroutine（ctx 取消）或 readLoop defer 可能已关闭 streamCh
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

	// 原 Send 路径
	resp := &rpcResponse{
		ID:     parseIDToInt64(msg.RawID),
		Result: msg.Result,
		Error:  msg.Error,
	}
	t.mu.RLock()
	ch, ok := t.pending[idKey]
	t.mu.RUnlock()
	if ok {
		ch <- resp
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
	}
}

// handleNotification 处理 JSON-RPC 通知（原 readLoop 的 msg.Method != "" 路径）
// 原逻辑保留，仅参数类型从匿名 struct 改为 readLoopMsg
func (t *WebSocketTransport) handleNotification(msg readLoopMsg) {
	// notifications/progress 路由到所有活跃 streamPending
	// （WS 单连接，多并发流式调用时进度通知广播到所有活跃 channel）
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
				// channel 满，跳过（背压由 Client 层 BackpressurePipe 处理）
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
// WS 传输的流式语义（对标 MCP 规范 2025-06-18 §"Transports"，访问日期 2026-07-21）：
// - 复用现有 readLoop，无需启动新 goroutine
// - 请求通过 WebSocket TextMessage 发送
// - 响应 + notifications/progress 通过 readLoop 路由到 streamPending[id] channel
// - channel 在 readLoop 收到匹配 ID 的最终响应后关闭
//
// goroutine 生命周期：
// - ctx 取消 → 启动清理 goroutine 关闭 channel + 清理 streamPending
// - readLoop 收到最终响应 → 关闭 channel + 清理 streamPending
// - readLoop 退出（连接断开）→ defer 清理所有 streamPending
//
// 注意：WS 写入失败不自动重试（与 Send 路径不同），重连由 readLoop 检测连接断开后
// 触发。流式调用重连后会丢失原 streamPending 上下文，调用方应通过 ctx 超时兜底。
func (t *WebSocketTransport) SendStream(ctx context.Context, id int64, method string, params interface{}) (<-chan StreamFrame, error) {
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
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan StreamFrame, 16)
	idKey := strconv.FormatInt(id, 10)
	t.streamMu.Lock()
	t.streamPending[idKey] = ch
	t.streamMu.Unlock()

	t.reconnectMu.Lock()
	err = t.conn.WriteMessage(websocket.TextMessage, reqJSON)
	t.reconnectMu.Unlock()

	if err != nil {
		t.streamMu.Lock()
		delete(t.streamPending, idKey)
		close(ch)
		t.streamMu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}

	// 启动 goroutine 监听 ctx 取消，清理 streamPending
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
