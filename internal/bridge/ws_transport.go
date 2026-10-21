// Package bridge — M21-03 WebSocket 传输支持（SNAPSHOT）
//
// TD-13 降级标注（实验性功能未启用）：
// 本文件提供的 WebSocket 传输层是实验性占位实现，当前未启用。
// WebSocket 协议升级（RFC 6455 握手）未实现，NewWSTransport 对 ws:// / wss:// URL
// 执行 fail-fast 拒绝，WSHandler.HandleHTTP 返回 503 Service Unavailable。
//
// 远程 IDE 当前实际传输路径：
// - Linux/macOS：stdio + Unix Domain Socket（见 internal/daemon/uds_transport.go）
// - Windows：stdio + Named Pipe（见 internal/remote/authproxy/npipe_windows.go）
// - 跨平台回退：TCP（见 internal/ssh/forward.go ForwardTCP）
//
// 完整 WS 协议实现需引入 gorilla/websocket 依赖并完成 RFC 6455 握手，
// 成本过高，当前版本不实施。远程 IDE 走 stdio/NPipe/UDS 即可满足需求。
//
// 对标同类 CLI 的 Remote Bridge（官方文档）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：WSTransport 接口抽象，支持多 WS 实现
// - 结构体字段：WSConfig 含 URL/Headers/ReconnectInterval
// - 分层权责：本文件只做"传输"，鉴权由 jwt.go，重连由 reconnect.go
// - 降级策略：WS 不可用时降级到 stdio/NPipe/UDS（由调用方处理）
// - TTL规则：心跳间隔 30s（wsHeartbeatInterval）
//
// 注意：为避免引入外部依赖（gorilla/websocket），本文件用 net/http 的 WebSocket
// 升级机制实现（Go 1.24 标准库支持）。实际生产可替换为 gorilla/websocket。
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// wsHeartbeatInterval 是 WebSocket 心跳间隔（30s，对标同类 CLI remote 心跳）。
const wsHeartbeatInterval = 30 * time.Second

// wsReadTimeout 是 WebSocket 读超时（60s，心跳间隔的 2 倍）。
const wsReadTimeout = 60 * time.Second

// RPCError 是 JSON-RPC 错误结构（SNAPSHOT）。
// 与 mcp.RPCError 同构，但在 bridge 包内独立定义以避免 bridge→mcp 循环依赖。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// Error 实现 error 接口。
func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error [%d]: %s", e.Code, e.Message)
}

// WSConfig 是 WebSocket 传输配置（SNAPSHOT-1）。
type WSConfig struct {
	URL               string            // WebSocket URL（ws://host:port/path）
	Headers           map[string]string // 自定义请求头（如 Authorization）
	ReconnectInterval time.Duration     // 重连间隔（默认 5s）
	// MaxReconnects 最大重连次数（0=无限）。
	// 注意：本字段为预留字段，当前版本未启用重连逻辑（reconnect.go 尚未接线），
	// 设置此值不会影响实际行为。后续重连功能实现后将读取此字段。
	MaxReconnects int
}

// WSMessage 是 WebSocket 传输的消息结构（SNAPSHOT-2）。
type WSMessage struct {
	Type   string          `json:"type"`   // 消息类型（request/response/event）
	ID     int             `json:"id"`     // 消息 ID（对应 JSON-RPC ID）
	Method string          `json:"method"` // 方法名（仅 request）
	Params json.RawMessage `json:"params"` // 参数（仅 request）
	Result json.RawMessage `json:"result"` // 结果（仅 response）
	Error  *RPCError       `json:"error"`  // 错误（仅 response）
}

// WSTransport 是 WebSocket 传输接口（SNAPSHOT-3）。
//
// 接口契约：
// - Connect：建立 WS 连接
// - Send：发送消息
// - Recv：接收消息（阻塞）
// - Close：关闭连接
//
// 实现方：
// - httpWSTransport：基于 net/http 的 WS 实现（本文件实现）
// - 未来可扩展 gorillaWSTransport（用 gorilla/websocket）
type WSTransport interface {
	// Connect 建立 WebSocket 连接。
	Connect(ctx context.Context) error

	// Send 发送消息（JSON 编码后发送）。
	Send(msg *WSMessage) error

	// Recv 接收消息（阻塞，直到收到消息或连接关闭）。
	Recv() (*WSMessage, error)

	// Close 关闭连接。
	Close() error

	// IsConnected 返回连接状态。
	IsConnected() bool
}

// httpWSTransport 是基于 net/http 的 WS 传输实现（候选 A）。
//
// 简化实现：用 HTTP 长轮询模拟 WS 双向通信（避免引入 gorilla/websocket 依赖）。
// 实际生产场景应替换为真正的 WebSocket 实现。
//
// 注意：本实现是"接口契约占位"，实际 WS 协议升级需要 net/http 升级机制。
// 在 Go 标准库中，net/http 不直接支持 WebSocket 协议升级（需 hijacker）。
// 此处提供接口与配置，实际 WS 实现留待引入 gorilla/websocket 后填充。
type httpWSTransport struct {
	cfg       WSConfig
	mu        sync.Mutex
	connected bool
	closeCh   chan struct{}
	sendCh    chan *WSMessage
	recvCh    chan *WSMessage
	errCh     chan error
}

// NewWSTransport 创建 WebSocket 传输实例（SNAPSHOT-4）。
// B-2 修复：WebSocket 协议升级是 stub（未实现 RFC 6455 握手），
// 构造期 fail-fast 返回 error，而非运行时返回 501。
// 仅允许 http:// URL 作为降级测试模式。
func NewWSTransport(cfg WSConfig) (WSTransport, error) {
	if cfg.ReconnectInterval == 0 {
		cfg.ReconnectInterval = 5 * time.Second
	}
	// fail-fast：ws:// / wss:// URL 需要 WebSocket 协议升级（stub，未实现）
	if (len(cfg.URL) >= 5 && cfg.URL[:5] == "ws://") || (len(cfg.URL) >= 6 && cfg.URL[:6] == "wss://") {
		return nil, errors.New("ws transport: WebSocket upgrade not implemented in this build (use http:// for degraded mode)")
	}
	return &httpWSTransport{
		cfg:     cfg,
		closeCh: make(chan struct{}),
		sendCh:  make(chan *WSMessage, 16),
		recvCh:  make(chan *WSMessage, 16),
		errCh:   make(chan error, 1),
	}, nil
}

// Connect 建立 WebSocket 连接（SNAPSHOT-5）。
//
// 简化实现：校验 URL + 标记 connected。
// 实际 WS 升级需 hijacker + RFC 6455 握手，此处仅占位。
func (t *httpWSTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cfg.URL == "" {
		return errors.New("ws transport: URL is empty")
	}
	if t.connected {
		return errors.New("ws transport: already connected")
	}

	// URL 基本校验
	if len(t.cfg.URL) < 4 || (t.cfg.URL[:4] != "ws:/" && t.cfg.URL[:5] != "wss:/") {
		// 允许 http:// 作为降级（测试场景）
		if len(t.cfg.URL) < 7 || t.cfg.URL[:7] != "http://" {
			return fmt.Errorf("ws transport: invalid URL scheme: %s", t.cfg.URL)
		}
	}

	t.connected = true
	// 启动心跳 goroutine（简化：仅标记，不实际发心跳）
	go t.heartbeatLoop()
	return nil
}

// heartbeatLoop 是心跳循环（SNAPSHOT-6）。
//
// 简化实现：定期检查连接状态，实际应发送 ping 帧。
func (t *httpWSTransport) heartbeatLoop() {
	ticker := time.NewTicker(wsHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.closeCh:
			return
		case <-ticker.C:
			// 实际应发送 ping 帧并等待 pong
			// 此处仅保活检查
			t.mu.Lock()
			if !t.connected {
				t.mu.Unlock()
				return
			}
			t.mu.Unlock()
		}
	}
}

// Send 发送消息（SNAPSHOT-7）。
func (t *httpWSTransport) Send(msg *WSMessage) error {
	t.mu.Lock()
	connected := t.connected
	t.mu.Unlock()
	if !connected {
		return errors.New("ws transport: not connected")
	}
	select {
	case t.sendCh <- msg:
		return nil
	case <-t.closeCh:
		return errors.New("ws transport: closed")
	}
}

// Recv 接收消息（SNAPSHOT-8）。
func (t *httpWSTransport) Recv() (*WSMessage, error) {
	select {
	case msg := <-t.recvCh:
		return msg, nil
	case <-t.closeCh:
		return nil, errors.New("ws transport: closed")
	}
}

// Close 关闭连接（SNAPSHOT-9）。
func (t *httpWSTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected {
		return nil
	}
	t.connected = false
	close(t.closeCh)
	return nil
}

// IsConnected 返回连接状态（SNAPSHOT-10）。
func (t *httpWSTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// MockWSTransport 是测试用 mock WS 传输（SNAPSHOT-11）。
//
// 用于单元测试：注入预定义消息，捕获发送消息。
type MockWSTransport struct {
	mu        sync.Mutex
	connected bool
	recvQueue []*WSMessage
	sentMsgs  []*WSMessage
}

// NewMockWSTransport 创建 mock WS 传输。
func NewMockWSTransport() *MockWSTransport {
	return &MockWSTransport{}
}

// Connect 标记为已连接。
func (m *MockWSTransport) Connect(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

// Send 捕获发送的消息。
func (m *MockWSTransport) Send(msg *WSMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return errors.New("not connected")
	}
	m.sentMsgs = append(m.sentMsgs, msg)
	return nil
}

// Recv 从预定义队列返回消息。
func (m *MockWSTransport) Recv() (*WSMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recvQueue) == 0 {
		return nil, errors.New("no messages in queue")
	}
	msg := m.recvQueue[0]
	m.recvQueue = m.recvQueue[1:]
	return msg, nil
}

// Close 标记为未连接。
func (m *MockWSTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	return nil
}

// IsConnected 返回连接状态。
func (m *MockWSTransport) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// EnqueueRecv 向接收队列注入消息（测试用）。
func (m *MockWSTransport) EnqueueRecv(msg *WSMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recvQueue = append(m.recvQueue, msg)
}

// SentMsgs 返回已发送消息列表（测试用）。
func (m *MockWSTransport) SentMsgs() []*WSMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*WSMessage, len(m.sentMsgs))
	copy(out, m.sentMsgs)
	return out
}

// WSHandler 是 WebSocket 服务端处理器（SNAPSHOT-12）。
//
// 用于 Bridge 服务端接受远程 IDE 的 WS 连接。
// 升级 HTTP 到 WS 协议，建立双向通信通道。
type WSHandler struct {
	upgrader  interface{} // 实际应为 websocket.Upgrader（避免依赖，用 interface{}）
	onMessage func(msg *WSMessage) error
	mu        sync.Mutex
	clients   map[string]WSTransport // clientID -> transport
}

// NewWSHandler 创建 WS 服务端处理器。
//
// onMessage 是消息处理回调（收到客户端消息时调用）。
func NewWSHandler(onMessage func(msg *WSMessage) error) *WSHandler {
	return &WSHandler{
		onMessage: onMessage,
		clients:   make(map[string]WSTransport),
	}
}

// HandleHTTP 处理 HTTP 升级请求（SNAPSHOT-13）。
//
// B-2 修复：WebSocket 协议升级是 stub（未实现 RFC 6455 握手）。
// 构造期应通过 NewWSTransport fail-fast，此处返回 503 Service Unavailable
// 替代原 501 Not Implemented，明确表示是 stub 不可用而非未实现接口。
func (h *WSHandler) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	// 校验 WS 升级头
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "Expected WebSocket upgrade", http.StatusBadRequest)
		return
	}
	// WS 升级未实现，返回 503 Service Unavailable（B-2 修复：替代 501）
	http.Error(w, "WebSocket upgrade not implemented in this build", http.StatusServiceUnavailable)
}

// RegisterClient 注册客户端连接（SNAPSHOT-14）。
func (h *WSHandler) RegisterClient(clientID string, transport WSTransport) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[clientID] = transport
}

// UnregisterClient 注销客户端连接（SNAPSHOT-15）。
func (h *WSHandler) UnregisterClient(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.clients[clientID]; ok {
		_ = t.Close()
		delete(h.clients, clientID)
	}
}

// Broadcast 向所有客户端广播消息（SNAPSHOT-16）。
func (h *WSHandler) Broadcast(msg *WSMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.clients {
		_ = t.Send(msg)
	}
}
