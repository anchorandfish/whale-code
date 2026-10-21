// Package mcp — Model Context Protocol client
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/bus"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/util"
)

// M6: MCP 协议版本协商。
//
// preferredProtocolVersion 是客户端请求的最新协议版本。
// supportedProtocolVersions 是客户端可接受的版本白名单（含旧版用于回退）。
// 当 server 返回的版本在白名单内则接受，否则报错（防止未知版本导致行为不可预测）。
const (
	preferredProtocolVersion = "2025-06-18"
)

// supportedProtocolVersions 返回客户端可接受的协议版本白名单。
// 2025-06-18 是最新版；2024-11-05 是前一版，用于与旧 server 的回退兼容。
func supportedProtocolVersions() map[string]bool {
	return map[string]bool{
		"2025-06-18": true,
		"2024-11-05": true,
	}
}

// ToolResultContent represents a single content item in a tool result
type ToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolResult represents the result of an MCP tool call
type ToolResult struct {
	Content []ToolResultContent `json:"content"`
	IsError bool                `json:"isError,omitempty"`
}

// Text returns the joined text of all content items
func (r *ToolResult) Text() string {
	var parts []string
	for _, content := range r.Content {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Resource MCP resource
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ToolInfo MCP tool info
type ToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
	Annotations map[string]interface{} `json:"annotations,omitempty"`
}

// PromptInfo MCP prompt info
type PromptInfo struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument MCP prompt argument
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage MCP prompt message
type PromptMessage struct {
	Role    string               `json:"role"`
	Content PromptMessageContent `json:"content"`
}

// PromptMessageContent MCP prompt message content
type PromptMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ServerCapabilities MCP server capabilities
type ServerCapabilities struct {
	Tools     *ToolCapabilities     `json:"tools,omitempty"`
	Resources *ResourceCapabilities `json:"resources,omitempty"`
	Prompts   *PromptCapabilities   `json:"prompts,omitempty"`
	Logging   *struct{}             `json:"logging,omitempty"`
}

// ToolCapabilities tool capabilities
// ListChanged 表示 server 支持 tools/list_changed 通知（C21）。
type ToolCapabilities struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourceCapabilities resource capabilities
type ResourceCapabilities struct {
	Subscribe bool `json:"subscribe,omitempty"`
}

// PromptCapabilities prompt capabilities
type PromptCapabilities struct{}

// RPCError JSON-RPC error
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// Error implements error interface
func (e *RPCError) Error() string {
	return fmt.Sprintf("MCP error [%d]: %s", e.Code, e.Message)
}

// ServerConfig MCP server configuration
type ServerConfig struct {
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	URL          string            `json:"url,omitempty"`
	Transport    string            `json:"transport,omitempty"` // "stdio", "sse", "http", "ws"
	Headers      map[string]string `json:"headers,omitempty"`
	OAuth        *OAuth2Config     `json:"oauth,omitempty"`
	Timeout      int               `json:"timeout,omitempty"`       // seconds
	SandboxLevel string            `json:"sandbox_level,omitempty"` // 默认 "disabled"（去沙箱）；显式 relaxed/strict/isolated 才套 OS 沙箱
}

// Config MCP configuration
type Config struct {
	Servers map[string]ServerConfig `json:"servers"`

	// Elicitations 是 MCP elicitation 交互的总开关（G3 决策，2026-09-10）。
	//
	// nil 或 true → 允许（默认开启）；显式 false → client 对所有 elicitation/create
	// 直接返回 decline（不弹窗、不转发 TUI）。
	//
	// 对标同类 CLI AskForApproval::Granular{ mcp_elicitations } 开关
	//"Whether to allow MCP elicitation prompts"；
	// 消费点 prompts/src/permissions_instructions.rs:363 allows_mcp_elicitations()）。
	Elicitations *bool `json:"elicitations,omitempty"`
}

// ElicitationsEnabled 返回 MCP elicitation 交互是否启用（默认 true）。
// nil 接收者/字段视为开启，保证未配置时行为与对齐业内既有实现 的默认一致。
func (c *Config) ElicitationsEnabled() bool {
	if c == nil || c.Elicitations == nil {
		return true
	}
	return *c.Elicitations
}

// LoadConfig load MCP configuration from file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if config.Servers == nil {
		config.Servers = make(map[string]ServerConfig)
	}

	return &config, nil
}

// LoadConfigFromDirs load MCP config from multiple directories
func LoadConfigFromDirs(dirs ...string) *Config {
	for _, dir := range dirs {
		for _, name := range []string{"mcp.json", "settings.json"} {
			path := fmt.Sprintf("%s%c%s", dir, os.PathSeparator, name)
			cfg, err := LoadConfig(path)
			if err == nil && len(cfg.Servers) > 0 {
				return cfg
			}
		}
	}
	return &Config{Servers: make(map[string]ServerConfig)}
}

// NotificationHandler handles MCP server notifications
type NotificationHandler func(method string, params json.RawMessage)

// Client MCP client
type Client struct {
	name            string
	trans           Transport           // Transport implementation
	transportType   string              // Transport type identifier: "stdio", "sse", "http", "ws"
	serverURL       string              // Server URL for SSE/HTTP/WS transport
	serverID        atomic.Int64        // Request ID counter
	ready           atomic.Bool         // Whether ready
	capabilities    ServerCapabilities  // Server capabilities
	tools           []ToolInfo          // Cached tool list
	onNotification  NotificationHandler // 外部通知回调（用户注册）
	heartbeatCancel context.CancelFunc  // Heartbeat cancellation function
	mu              sync.RWMutex        // Protects capabilities and tools
	protocolVersion string              // M6: 协商后的协议版本
	bus             *bus.Bus            // C21: 事件总线（可选，nil 时不发布事件）
	sbMgr           sandbox.Manager     // D2(C9): 沙箱管理器（可选，nil 时不启用沙箱隔离）
	// SNAPSHOT：资源订阅（M17-01）相关字段
	subscriptions   map[string]bool       // 已订阅资源 URI 集合（幂等去重）
	resourceHandler ResourceChangeHandler // 资源变更回调（nil=未注册）
	subMu           sync.RWMutex          // 保护 subscriptions + resourceHandler
	// 第2轮修复（致命漏洞 #2）：elicitation 相关字段
	// 对标同类 CLI ElicitationRequestManager
	elicitationRouter  *ElicitationRequestRouter // pending elicitation 请求路由表（对标同类 CLI ElicitationRequestRouter）
	elicitationHandler ElicitationHandler        // TUI 层注册的 elicitation 回调
	elicitationMu      sync.Mutex                // 保护 elicitationRouter + elicitationHandler 并发读写
	// G3 决策：MCP elicitation 总开关，默认开启。
	// 采用「否定式」字段，使零值 = 开启 —— 对齐业内既有实现 GranularApprovalConfig.mcp_elicitations
	// 的允许语义，并避免任何未经构造函数的 Client（如测试直建）意外关闭 elicitation。
	elicitationsDisabled atomic.Bool
}

// ErrSubscribeNotSupported 表示 server 不支持 resources/subscribe（/ M17-01）。
// 当 server capabilities.resources.subscribe=false 时由 Subscribe 返回。
var ErrSubscribeNotSupported = fmt.Errorf("server does not support resources/subscribe (capabilities.resources.subscribe=false)")

// ErrIssuerMismatch 表示 RFC 8414 metadata 的 issuer 字段与请求的 issuer URL 不匹配（/ M17-04）。
// 防钓鱼式 metadata 攻击（恶意 server 返回其他 issuer 的 metadata）。
var ErrIssuerMismatch = fmt.Errorf("RFC 8414 metadata issuer mismatch (possible phishing)")

// rpcResponse internal JSON-RPC response
type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// parseSandboxLevel 从 ServerConfig.SandboxLevel 字符串解析沙箱级别。
//
// （2026-10-10，对齐 同类 CLI）：**空串默认返回 SandboxLevelDisabled**
// —— MCP server 默认不套 OS 沙箱，联网型 server 开箱可用；密钥过滤由
// transport 的"只剥不缩"env（sandbox.UnsandboxedEnvConfig）继续保证。
// 解析失败（写成未知级别名）同样回退 Disabled —— 与"默认"一致，避免一个笔误
// 意外把 server 关进沙箱；需要隔离时显式写 "relaxed"/"strict"/"isolated"。
func parseSandboxLevel(levelStr string) sandbox.SandboxLevel {
	if levelStr == "" {
		return sandbox.SandboxLevelDisabled
	}
	level, err := sandbox.ParseSandboxMode(levelStr)
	if err != nil {
		return sandbox.SandboxLevelDisabled
	}
	return level
}

// NewClient create MCP client
// D2(C9): sbMgr 为可选 variadic 参数，传入 sandbox.Manager 时启用沙箱隔离。
// 不传或传 nil 时向后兼容（使用宿主环境变量）。
func NewClient(ctx context.Context, name string, config ServerConfig, sbMgr ...sandbox.Manager) (*Client, error) {
	var mgr sandbox.Manager
	if len(sbMgr) > 0 {
		mgr = sbMgr[0]
	}
	level := parseSandboxLevel(config.SandboxLevel)
	trans, err := NewStdioTransport(ctx, config.Command, config.Args, config.Env, mgr, level)
	if err != nil {
		return nil, err
	}

	c := &Client{
		name:              name,
		trans:             trans,
		transportType:     "stdio",
		sbMgr:             mgr,
		subscriptions:     make(map[string]bool),
		elicitationRouter: NewElicitationRequestRouter(),
	}
	trans.SetNotificationHandler(c.dispatchNotification)
	trans.SetRequestHandler(c.dispatchRequest)

	if err := c.initialize(ctx); err != nil {
		trans.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	c.ready.Store(true)
	return c, nil
}

// NewClientWithNotification create MCP client with notification handler
// D2(C9): sbMgr 为可选 variadic 参数，传入 sandbox.Manager 时启用沙箱隔离。
func NewClientWithNotification(ctx context.Context, name string, config ServerConfig, handler NotificationHandler, sbMgr ...sandbox.Manager) (*Client, error) {
	var mgr sandbox.Manager
	if len(sbMgr) > 0 {
		mgr = sbMgr[0]
	}
	level := parseSandboxLevel(config.SandboxLevel)
	trans, err := NewStdioTransport(ctx, config.Command, config.Args, config.Env, mgr, level)
	if err != nil {
		return nil, err
	}

	c := &Client{
		name:              name,
		trans:             trans,
		transportType:     "stdio",
		onNotification:    handler,
		sbMgr:             mgr,
		subscriptions:     make(map[string]bool),
		elicitationRouter: NewElicitationRequestRouter(),
	}
	trans.SetNotificationHandler(c.dispatchNotification)
	trans.SetRequestHandler(c.dispatchRequest)

	if err := c.initialize(ctx); err != nil {
		trans.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	c.ready.Store(true)
	return c, nil
}

// NewSSEClient creates an SSE-based MCP client
func NewSSEClient(ctx context.Context, name string, serverURL string, handler NotificationHandler) (*Client, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("SSE server URL cannot be empty")
	}
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		return nil, fmt.Errorf("SSE server URL must start with http:// or https://")
	}

	trans, err := NewSSETransport(ctx, serverURL, nil)
	if err != nil {
		return nil, err
	}

	c := &Client{
		name:              name,
		trans:             trans,
		transportType:     "sse",
		serverURL:         serverURL,
		onNotification:    handler,
		subscriptions:     make(map[string]bool),
		elicitationRouter: NewElicitationRequestRouter(),
	}
	trans.SetNotificationHandler(c.dispatchNotification)
	trans.SetRequestHandler(c.dispatchRequest)

	if err := c.initialize(ctx); err != nil {
		trans.Close()
		return nil, fmt.Errorf("initialize SSE client: %w", err)
	}

	c.ready.Store(true)
	return c, nil
}

// NewClientFromConfig creates MCP client from config
// Auto-detect transport type: Command != "" → stdio, Transport == "http" → HTTP,
// Transport == "ws" → WebSocket, URL != "" → SSE (backward compatible)
// D2(C9): sbMgr 为可选 variadic 参数，传入 sandbox.Manager 时启用沙箱隔离（仅 stdio 生效）。
func NewClientFromConfig(ctx context.Context, name string, config ServerConfig, sbMgr ...sandbox.Manager) (*Client, error) {
	// Determine transport type
	transportType := detectTransportType(config)
	if transportType == "" {
		return nil, fmt.Errorf("cannot determine transport type: Command or URL required")
	}

	switch transportType {
	case "stdio":
		return NewClient(ctx, name, config, sbMgr...)
	case "sse":
		trans, err := NewSSETransport(ctx, config.URL, config.Headers)
		if err != nil {
			return nil, err
		}
		c := &Client{
			name:              name,
			trans:             trans,
			transportType:     "sse",
			serverURL:         config.URL,
			subscriptions:     make(map[string]bool),
			elicitationRouter: NewElicitationRequestRouter(),
		}
		trans.SetNotificationHandler(c.dispatchNotification)
		trans.SetRequestHandler(c.dispatchRequest)
		if err := c.initialize(ctx); err != nil {
			trans.Close()
			return nil, fmt.Errorf("initialize SSE client: %w", err)
		}
		c.ready.Store(true)
		return c, nil
	case "http":
		trans, err := NewHTTPTransport(ctx, config.URL, config.Headers)
		if err != nil {
			return nil, err
		}
		c := &Client{
			name:              name,
			trans:             trans,
			transportType:     "http",
			serverURL:         config.URL,
			subscriptions:     make(map[string]bool),
			elicitationRouter: NewElicitationRequestRouter(),
		}
		trans.SetNotificationHandler(c.dispatchNotification)
		trans.SetRequestHandler(c.dispatchRequest)
		if err := c.initialize(ctx); err != nil {
			trans.Close()
			return nil, fmt.Errorf("initialize HTTP client: %w", err)
		}
		c.ready.Store(true)
		return c, nil
	case "ws":
		trans, err := NewWebSocketTransport(ctx, config.URL, config.Headers)
		if err != nil {
			return nil, err
		}
		c := &Client{
			name:              name,
			trans:             trans,
			transportType:     "ws",
			serverURL:         config.URL,
			subscriptions:     make(map[string]bool),
			elicitationRouter: NewElicitationRequestRouter(),
		}
		trans.SetNotificationHandler(c.dispatchNotification)
		trans.SetRequestHandler(c.dispatchRequest)
		if err := c.initialize(ctx); err != nil {
			trans.Close()
			return nil, fmt.Errorf("initialize WebSocket client: %w", err)
		}
		c.ready.Store(true)
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}
}

// detectTransportType auto-detects transport type from config
func detectTransportType(config ServerConfig) string {
	// Explicitly specified transport type
	if config.Transport != "" {
		switch config.Transport {
		case "stdio", "sse", "http", "ws":
			return config.Transport
		default:
			return ""
		}
	}

	// Auto-detect
	if config.Command != "" {
		return "stdio"
	}
	if config.URL != "" {
		// Backward compatible: default to SSE when only URL is present
		return "sse"
	}

	return ""
}

// sendRequest send JSON-RPC request
func (c *Client) sendRequest(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := c.serverID.Add(1)
	return c.trans.Send(ctx, id, method, params)
}

// sendNotification send JSON-RPC notification (no ID, no response expected)
func (c *Client) sendNotification(method string, params interface{}) error {
	ctx := context.Background()
	return c.trans.SendNotification(ctx, method, params)
}

// initialize 发送 initialize 请求并解析 server 能力（M6: 协议版本协商）。
//
// 客户端发送 preferredProtocolVersion，server 返回其支持的版本。
// 若 server 版本在 supportedProtocolVersions 白名单内则接受并存储；
// 否则返回错误（防止未知版本导致行为不可预测）。
// 回退兼容：旧 server 仅支持 2024-11-05，白名单包含该版本。
func (c *Client) initialize(ctx context.Context) error {
	params := map[string]interface{}{
		"protocolVersion": preferredProtocolVersion,
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{},
			"resources": map[string]interface{}{},
			"prompts":   map[string]interface{}{},
		},
		"clientInfo": map[string]string{
			"name":    "whale-code",
			"version": "0.1.0",
		},
	}

	result, err := c.sendRequest(ctx, "initialize", params)
	if err != nil {
		return err
	}

	var initResp struct {
		ProtocolVersion string             `json:"protocolVersion"`
		Capabilities    ServerCapabilities `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &initResp); err != nil {
		return fmt.Errorf("parse initialize response: %w", err)
	}

	// M6: 协议版本协商 — 校验 server 返回的版本是否在白名单内
	if initResp.ProtocolVersion == "" {
		// 旧版 server 可能不返回 protocolVersion，回退到客户端请求版本
		c.protocolVersion = preferredProtocolVersion
	} else if !supportedProtocolVersions()[initResp.ProtocolVersion] {
		return fmt.Errorf("unsupported protocol version: server returned %q, supported: %v",
			initResp.ProtocolVersion, supportedProtocolVersions())
	} else {
		c.protocolVersion = initResp.ProtocolVersion
	}

	c.mu.Lock()
	c.capabilities = initResp.Capabilities
	c.mu.Unlock()

	if err := c.sendNotification("notifications/initialized", map[string]interface{}{}); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	return nil
}

// Ping check server connection
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.sendRequest(ctx, "ping", map[string]interface{}{})
	return err
}

// StartHeartbeat 启动周期性 ping 以检测连接异常。
// M-2 修复（全模块审查）：ping 失败时不仅标记 ready=false，
// 还尝试重新 initialize（重新握手）以恢复连接；ping 成功时恢复 ready=true。
func (c *Client) StartHeartbeat(interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.heartbeatCancel = cancel
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := c.Ping(pingCtx); err != nil {
					// M-2 修复：ping 失败后尝试重新握手恢复连接。
					c.ready.Store(false)
					pingCancel()
					c.attemptReconnect()
					continue
				}
				// ping 成功，确保 ready=true（修复原实现永不恢复 ready 的缺陷）。
				c.ready.Store(true)
				pingCancel()
			}
		}
	}()
}

// attemptReconnect 尝试重新与 MCP server 握手以恢复连接（M-2 修复，）。
// 仅在 transport 仍存活时有效（stdio subprocess 未退出）。
// 成功则置 ready=true；失败则保持 ready=false，等待下次心跳周期重试。
func (c *Client) attemptReconnect() {
	reconnectCtx, reconnectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer reconnectCancel()
	if err := c.initialize(reconnectCtx); err != nil {
		fmt.Fprintf(os.Stderr, "[mcp] heartbeat reconnect failed (will retry next tick): %v\n", err)
		return
	}
	c.ready.Store(true)
	fmt.Fprintf(os.Stderr, "[mcp] heartbeat reconnect succeeded after ping failure\n")
}

// ListTools list available tools from MCP server
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	result, err := c.sendRequest(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var resp struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse tools response: %w", err)
	}

	c.mu.Lock()
	c.tools = resp.Tools
	c.mu.Unlock()
	return resp.Tools, nil
}

// CallTool call a tool on the MCP server
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	}

	result, err := c.sendRequest(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}

	var resp ToolResult
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse tool call response: %w", err)
	}

	sanitized, err := SanitizeToolResult(&resp)
	if err != nil {
		return nil, fmt.Errorf("sanitize tool result: %w", err)
	}

	if sanitized.IsError {
		return sanitized, fmt.Errorf("tool error: %s", sanitized.Text())
	}

	return sanitized, nil
}

// ListPrompts list available prompts from MCP server
func (c *Client) ListPrompts(ctx context.Context) ([]PromptInfo, error) {
	result, err := c.sendRequest(ctx, "prompts/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var resp struct {
		Prompts []PromptInfo `json:"prompts"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse prompts response: %w", err)
	}

	return resp.Prompts, nil
}

// GetPrompt get a prompt from MCP server
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) ([]PromptMessage, error) {
	params := map[string]interface{}{
		"name": name,
	}
	if len(arguments) > 0 {
		params["arguments"] = arguments
	}

	result, err := c.sendRequest(ctx, "prompts/get", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Description string          `json:"description,omitempty"`
		Messages    []PromptMessage `json:"messages"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse prompt response: %w", err)
	}

	return resp.Messages, nil
}

// ListResources list available resources
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	result, err := c.sendRequest(ctx, "resources/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var resp struct {
		Resources []Resource `json:"resources"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return resp.Resources, nil
}

// ReadResource read a resource
func (c *Client) ReadResource(ctx context.Context, uri string) (string, error) {
	result, err := c.sendRequest(ctx, "resources/read", map[string]string{"uri": uri})
	if err != nil {
		return "", err
	}

	var resp struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType,omitempty"`
			Text     string `json:"text,omitempty"`
			Blob     string `json:"blob,omitempty"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(resp.Contents) == 0 {
		return "", fmt.Errorf("no content returned")
	}

	var parts []string
	for _, content := range resp.Contents {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}

	output := strings.Join(parts, "\n")
	return SanitizeString(output), nil
}

// Capabilities return server capabilities
func (c *Client) Capabilities() ServerCapabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capabilities
}

// HasTools check if server supports tools
func (c *Client) HasTools() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capabilities.Tools != nil
}

// HasResources check if server supports resources
func (c *Client) HasResources() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capabilities.Resources != nil
}

// HasPrompts check if server supports prompts
func (c *Client) HasPrompts() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capabilities.Prompts != nil
}

// Tools return cached tools list
func (c *Client) Tools() []ToolInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools
}

// IsReady check if client is ready
func (c *Client) IsReady() bool {
	return c.ready.Load()
}

// Name return server name
func (c *Client) Name() string {
	return c.name
}

// Transport returns the client transport type
func (c *Client) Transport() string {
	return c.transportType
}

// ServerURL returns the server URL for SSE/WS transport
func (c *Client) ServerURL() string {
	return c.serverURL
}

// ProtocolVersion 返回 M6 协商后的协议版本（如 "2025-06-18"）。
// 在 initialize 完成后有效；initialize 前返回空串。
func (c *Client) ProtocolVersion() string {
	return c.protocolVersion
}

// SetBus 注入事件总线（C21）。
// 注入后，list_changed 通知会自动刷新工具列表并发布 bus.ToolsChangedEvent。
// 不注入（nil）时仅自动刷新工具列表，不发布事件。
// 必须在 initialize 前调用（构造函数之后通常无法直接调用，
// 故设计为可在 NewClient 返回后、ListTools 调用前设置）。
func (c *Client) SetBus(b *bus.Bus) {
	c.mu.Lock()
	c.bus = b
	c.mu.Unlock()
}

// SetSandboxManager 注入沙箱管理器（D2(C9)）。
// 用于在 Client 创建后存储沙箱管理器引用，供后续 transport 重建或查询使用。
// 注意：stdio transport 的沙箱隔离在 NewStdioTransport 构造时生效，
// 此 setter 主要用于存储引用，不影响已创建的 transport。
func (c *Client) SetSandboxManager(mgr sandbox.Manager) {
	c.mu.Lock()
	c.sbMgr = mgr
	c.mu.Unlock()
}

// Subscribe 订阅资源变更通知（/ M17-01）。
//
// 发送 resources/subscribe JSON-RPC 请求，将 URI 加入 subscriptions map（幂等去重）。
// 当 server capabilities.resources.subscribe=false 时返回 ErrSubscribeNotSupported。
// 重复订阅同一 URI 直接返回 nil（不重复发送 JSON-RPC 请求）。
//
// 对标 MCP 规范 2025-06-18 §"Resources" resources/subscribe（访问日期 2026-07-21）：
// - 请求参数：{"uri": "..."}
// - 响应：空结果（订阅成功）
// - 后续通知：notifications/resources/updated（资源内容变更）+
// notifications/resources/list_changed（资源列表变更）
//
// 降级策略：
// - server 不支持 → ErrSubscribeNotSupported（调用方应改用 ListResources 轮询）
// - 网络错误 → 返回原始错误（调用方可重试）
// - 已订阅 → 幂等返回 nil
func (c *Client) Subscribe(ctx context.Context, uri string) error {
	if uri == "" {
		return fmt.Errorf("uri cannot be empty")
	}
	// 检查 server 能力
	c.mu.RLock()
	resources := c.capabilities.Resources
	c.mu.RUnlock()
	if resources == nil || !resources.Subscribe {
		return ErrSubscribeNotSupported
	}
	// 幂等：已订阅直接返回
	c.subMu.RLock()
	alreadySubscribed := c.subscriptions[uri]
	c.subMu.RUnlock()
	if alreadySubscribed {
		return nil
	}
	// 发送 resources/subscribe 请求
	params := map[string]interface{}{"uri": uri}
	if _, err := c.sendRequest(ctx, "resources/subscribe", params); err != nil {
		return fmt.Errorf("subscribe %s: %w", uri, err)
	}
	// 记录订阅
	c.subMu.Lock()
	c.subscriptions[uri] = true
	c.subMu.Unlock()
	return nil
}

// Unsubscribe 取消资源变更订阅（/ M17-01）。
//
// 发送 resources/unsubscribe JSON-RPC 请求，从 subscriptions map 移除 URI。
// 未订阅的 URI 返回 nil（幂等）。
// 即使 server 不支持 resources/unsubscribe，也从本地 map 移除（best-effort）。
//
// 对标 MCP 规范 2025-06-18 §"Resources" resources/unsubscribe（访问日期 2026-07-21）：
// - 请求参数：{"uri": "..."}
// - 响应：空结果
//
// 降级策略：
// - 未订阅 → 幂等返回 nil（不发 JSON-RPC 请求）
// - server 不支持 unsubscribe → 从本地 map 移除 + 记录错误日志（best-effort）
// - 网络错误 → 从本地 map 移除（best-effort，避免本地状态与 server 不一致导致重连后重复推送）
func (c *Client) Unsubscribe(ctx context.Context, uri string) error {
	if uri == "" {
		return fmt.Errorf("uri cannot be empty")
	}
	// 幂等：未订阅直接返回
	c.subMu.RLock()
	subscribed := c.subscriptions[uri]
	c.subMu.RUnlock()
	if !subscribed {
		return nil
	}
	// 发送 resources/unsubscribe 请求（best-effort，失败也清理本地 map）
	params := map[string]interface{}{"uri": uri}
	_, err := c.sendRequest(ctx, "resources/unsubscribe", params)
	// 无论请求成功失败，都从本地 map 移除（避免 server 持续推送但本地无回调处理）
	c.subMu.Lock()
	delete(c.subscriptions, uri)
	c.subMu.Unlock()
	if err != nil {
		// best-effort：记录错误但不返回（本地状态已清理，调用方无需感知 server 端错误）
		return fmt.Errorf("unsubscribe %s (local map cleared): %w", uri, err)
	}
	return nil
}

// SetResourceHandler 注册资源变更回调（/ M17-01）。
//
// 当 server 推送 notifications/resources/updated 或 notifications/resources/list_changed 时，
// Client 通过此回调将通知转发给调用方。
//
// 行为：
// - handler=nil 清除回调（subscriptions map 不清理，仍保持订阅状态，仅停止本地通知转发）
// - 重复调用覆盖旧回调
// - 调用方应在异步 goroutine 中处理通知（同步处理会阻塞 readLoop）
//
// 降级策略：
// - handler=nil + 通知到达 → 通知被忽略（与 list_changed 当前行为一致）
// - 资源通知与外部 onNotification 共存（两者都被调用，互不影响）
func (c *Client) SetResourceHandler(handler ResourceChangeHandler) {
	c.subMu.Lock()
	c.resourceHandler = handler
	c.subMu.Unlock()
}

// publishResourceNotification 将资源变更通知转发给注册的 ResourceChangeHandler（/ M17-01）。
//
// 在 dispatchNotification 中调用，处理两类资源通知：
// - notifications/resources/updated：单个资源内容变更，params.uri 标识资源
// - notifications/resources/list_changed：资源列表变更（无 uri）
//
// 同步调用 handler（handler 在 readLoop goroutine 中执行），调用方应异步处理。
// handler=nil 时静默丢弃通知（与 list_changed 当前行为一致）。
func (c *Client) publishResourceNotification(method string, params json.RawMessage) {
	c.subMu.RLock()
	handler := c.resourceHandler
	c.subMu.RUnlock()
	if handler == nil {
		return
	}
	// 解析 uri（updated 通知含 uri 字段，list_changed 无）
	var parsed struct {
		URI string `json:"uri"`
	}
	uri := ""
	if len(params) > 0 {
		_ = json.Unmarshal(params, &parsed)
		uri = parsed.URI
	}
	handler(method, uri, params)
	// 可选：发布总线事件（异步，不阻塞 readLoop）
	c.mu.RLock()
	b := c.bus
	name := c.name
	c.mu.RUnlock()
	if b != nil {
		b.Publish(bus.ResourcesChangedEvent{
			ServerName: name,
			Method:     method,
			URI:        uri,
			OccurredAt: time.Now(),
		})
	}
}

// dispatchNotification 是内部通知分发器（C21 +）。
//
// 拦截 notifications/tools/list_changed 进行内部处理（自动刷新工具列表 + 发布事件），
// 拦截 notifications/cancelled 进行内部记录（server 主动取消请求，实际取消传播
// 由 transport 层 SendStream channel 关闭实现，此处仅转发给外部回调），
// 然后将所有通知转发给外部 onNotification 回调（若有）。
// 所有构造函数都将 transport 的通知处理器设置为此方法，确保 list_changed
// 不依赖用户手动注册即可生效。
//
// 对标 MCP 规范 2025-06-18 §"Cancellation"（访问日期 2026-07-21）：
// - notifications/cancelled 通知 params.requestId 标识被取消的请求
// - server 取消后不再发送最终响应，transport 层 streamPending channel 不会被
// 匹配 ID 的响应触发关闭；调用方应通过 ctx 超时或感知 notifications/cancelled
// 后主动取消 ctx 兜底
// - 本函数不直接取消 stream channel（避免与 transport 层并发关闭竞态），
// 仅将通知透传给外部回调，由调用方决定是否取消 ctx
func (c *Client) dispatchNotification(method string, params json.RawMessage) {
	// C21: 拦截 tools/list_changed 通知
	if method == "notifications/tools/list_changed" {
		c.refreshToolsAndNotify()
	}
	// / M17-01：拦截资源变更通知（updated + list_changed），转发给 ResourceChangeHandler
	if method == "notifications/resources/updated" || method == "notifications/resources/list_changed" {
		c.publishResourceNotification(method, params)
	}
	// notifications/cancelled 透传给外部回调（实际取消由调用方通过 ctx 触发）
	// 此处不直接操作 transport 层 streamPending，避免跨层并发竞态
	// 转发给外部回调（不阻塞内部处理）
	if c.onNotification != nil {
		c.onNotification(method, params)
	}
}

// refreshToolsAndNotify 刷新工具列表并发布 ToolsChangedEvent（C21）。
//
// 非阻塞语义：刷新失败仅记录错误，不阻塞后续通知处理；
// 事件发布通过 bus.Publish（非阻塞，channel 满时丢弃）。
// 仅当 server 声明 tools.listChanged 能力时才会被调用。
func (c *Client) refreshToolsAndNotify() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 自动刷新工具列表
	oldTools := c.Tools()
	_, err := c.ListTools(ctx)
	if err != nil {
		// 刷新失败：不阻塞，不发布事件（保守策略，避免噪声事件）
		return
	}

	// 发布 ToolsChangedEvent（信息性，非阻塞）
	// bus 为 nil 时不发布；bus.Publish 本身非阻塞（channel 满时丢弃）
	c.mu.RLock()
	b := c.bus
	name := c.name
	c.mu.RUnlock()

	if b != nil {
		change := "updated"
		// 简单变更类型推断（信息性，不要求精确）
		if len(c.Tools()) > len(oldTools) {
			change = "added"
		} else if len(c.Tools()) < len(oldTools) {
			change = "removed"
		}
		b.Publish(bus.ToolsChangedEvent{
			ServerName: name,
			Change:     change,
			OccurredAt: time.Now(),
		})
	}
}

// Close close MCP client gracefully
func (c *Client) Close() error {
	c.ready.Store(false)

	if c.heartbeatCancel != nil {
		c.heartbeatCancel()
	}

	// Send close notification
	c.sendNotification("notifications/cancelled", map[string]interface{}{
		"reason": "client shutting down",
	})

	// Close transport layer
	if c.trans != nil {
		return c.trans.Close()
	}
	return nil
}

// === 第2轮修复：elicitation 相关类型与方法（对标同类 CLI ElicitationRequestManager）===

// ElicitationAction MCP elicitation 响应动作，对标同类 CLI McpServerElicitationAction
// （REF-外部-MCP-Spec-2025-06-18 §"Elicitation" / REF-外部-同类 CLI-3c7f013f9735）
type ElicitationAction string

const (
	// ElicitationActionAccept 接受 elicitation 请求，提供请求的信息
	ElicitationActionAccept ElicitationAction = "accept"
	// ElicitationActionDecline 拒绝 elicitation 请求，继续执行
	ElicitationActionDecline ElicitationAction = "decline"
	// ElicitationActionCancel 取消 elicitation 请求（Esc 硬编码映射 / 300s TTL 超时）
	ElicitationActionCancel ElicitationAction = "cancel"
)

// ElicitationResponse elicitation 响应，回传给 MCP server
// 第3轮修复（致命漏洞 #2）：RequestID 类型从 string 改为 json.RawMessage
// 对标同类 CLI RequestId enum（String(String) | Integer(i64)；REF-外部-同类 CLI-RequestId）
// 保留原始 JSON 字节，transport 层发送响应时原样回传，无损支持 string/number 双类型
type ElicitationResponse struct {
	ServerName string
	RequestID  json.RawMessage // 原始 JSON 字节的请求 ID（string/number 双类型，对标同类 CLI RequestId enum）
	Action     ElicitationAction
	Content    json.RawMessage // Accept 时携带的用户输入
}

// ElicitationHandler elicitation 请求回调，由 TUI 层注册
// client 收到 elicitation/create 请求时调用此 handler，handler 应将请求转发到 TUI 层渲染
// handler 内部必须非阻塞（向 TUI channel 发送时 channel 满应 fail-safe 丢弃）
// 第3轮修复（致命漏洞 #2）：requestID 类型从 string 改为 json.RawMessage
// 对标同类 CLI RequestId enum 双类型，REF-外部-同类 CLI-RequestId）
type ElicitationHandler func(serverName string, requestID json.RawMessage, message string)

// errElicitationPending 特殊哨兵错误：dispatchRequest 已通过 goroutine 异步处理响应
// readLoop 调用方识别此错误后不发任何响应，响应由 awaitElicitationResponse 发送
// 第2轮修复（致命漏洞 #1）：原方案返回此 error 让 transport 发 error response，违反 JSON-RPC 协议
//
// 使用场景（NB-5）：
// - JSON-RPC 2.0 协议要求每个请求必须有且仅有一个响应（success 或 error）
// - MCP elicitation 异步模式下，dispatchRequest 立即返回此哨兵，响应由 awaitElicitationResponse
// 在用户操作完成后异步发送（避免阻塞 readLoop + 满足"一个请求一个响应"协议约束）
// - 4 个 transport 的 readLoop 收到 server-side 请求后调用 dispatchRequest，识别此哨兵跳过响应发送
//
// 替代方案对比（保留哨兵模式的原因）：
// - 方案 A（哨兵 error + errors.Is 识别）：当前实现。符合 Go 惯用法（参考 io.EOF / sql.ErrNoRows）
// - 方案 B（struct{} 信号通道）：替代方案。需修改 RequestHandler 签名增加返回 chan struct{}
// 破坏现有 4 transport + MockTransport 接口契约，改动面大
// - 方案 C（返回特殊值 *ElicitationResponse）：替代方案。需 result 类型断言，类型安全弱
// - 选定 A：最小侵入 + Go 惯用法 + 4 transport 复用同一识别逻辑
//
// 4 个 transport 的识别点（行号截至 2026-07-30，重命名时同步更新）：
// - transport_stdio.go:409 — readLoop 调用 handleServerSideRequest 后 errors.Is(err, errElicitationPending)
// - transport_sse.go:350 — 同上
// - transport_http.go:684 — 同上
// - transport_ws.go:603 — 同上
// - dispatchRequest 返回点：client.go:1206（elicitation/create 路径返回此哨兵）
//
// 对标同类 CLI elicitation.rs 用 Result<Option<Response>, Error> 替代哨兵
// （REF-外部-同类 CLI-elicitation）
var errElicitationPending = errors.New("elicitation request pending, response will be sent asynchronously")

// ElicitationRequestRouter pending elicitation 请求路由表
// 对标同类 CLI ElicitationRequestRouter（REF-外部-同类 CLI-elicitation）
// 每个 reqID 对应一个独立 channel，支持多请求并发 pending
type ElicitationRequestRouter struct {
	mu      sync.Mutex
	pending map[string]chan ElicitationResponse // key = reqID
}

// NewElicitationRequestRouter 创建 elicitation 路由器
// 返回初始化好的空 router，pending map 预分配
func NewElicitationRequestRouter() *ElicitationRequestRouter {
	return &ElicitationRequestRouter{pending: make(map[string]chan ElicitationResponse)}
}

// register 注册 pending elicitation 请求
// 对标同类 CLI router.requests.lock.insert
func (r *ElicitationRequestRouter) register(reqID string, ch chan ElicitationResponse) {
	r.mu.Lock()
	r.pending[reqID] = ch
	r.mu.Unlock()
}

// unregister 移除 pending 条目（超时/响应后清理）
// 对标同类 CLI router.requests.lock.remove
func (r *ElicitationRequestRouter) unregister(reqID string) {
	r.mu.Lock()
	delete(r.pending, reqID)
	r.mu.Unlock()
}

// resolve 路由响应到对应 reqID 的 channel
// 对标同类 CLI ElicitationRequestRouter::resolve
// 返回：true 表示路由成功，false 表示 reqID 不存在或 channel 已满（已超时被清理）
func (r *ElicitationRequestRouter) resolve(reqID string, resp ElicitationResponse) bool {
	r.mu.Lock()
	ch, ok := r.pending[reqID]
	if ok {
		delete(r.pending, reqID)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
		return true
	default:
		return false
	}
}

// SetElicitationHandler 注册 elicitation 请求回调（对标 SetResourceHandler 模式）
// handler=nil 清除回调，elicitation 请求将返回 decline 响应（fail-safe）
// 第2轮修复（致命漏洞 #2）：使用 elicitationMu 保护，不再用 c.mu
func (c *Client) SetElicitationHandler(handler ElicitationHandler) {
	c.elicitationMu.Lock()
	c.elicitationHandler = handler
	c.elicitationMu.Unlock()
}

// SetElicitationsEnabled 启用/禁用 MCP elicitation 交互（G3 决策，默认启用）。
//
// 禁用后在 dispatchRequest 中对 elicitation/create 直接返回 decline（fail-safe），
// 不再转发 TUI。对标同类 CLI GranularApprovalConfig.mcp_elicitations
// （protocol.rs:932-944）：false 表示该类审批自动拒绝而非提示用户。
func (c *Client) SetElicitationsEnabled(enabled bool) {
	c.elicitationsDisabled.Store(!enabled)
}

// ElicitationsEnabled 报告 MCP elicitation 交互是否启用（默认 true）。
func (c *Client) ElicitationsEnabled() bool {
	return !c.elicitationsDisabled.Load()
}

// dispatchRequest 处理 server-side 请求（elicitation/create 等）
// 与 dispatchNotification 区分：请求需要返回响应，通知不需要
// 对标 MCP 规范 2025-06-18 §"Elicitation"（REF-外部-MCP-Spec-2025-06-18）
// 对标同类 CLI ElicitationRequestManager::make_sender 异步模式
//
// 第2轮修复（致命漏洞 #1）：改为 ElicitationRequestRouter 异步模式
// 第3轮修复（致命漏洞 #2）：reqID 类型从 string 改为 json.RawMessage
//
// 此方法在 4 个构造函数中通过 trans.SetRequestHandler(c.dispatchRequest) 注册到 transport 层
// readLoop 收到 server-side 请求（id + method 同时存在）时调用此方法
func (c *Client) dispatchRequest(method string, params json.RawMessage, reqID json.RawMessage) (interface{}, error) {
	switch method {
	case "elicitation/create":
		// G3 决策：总开关关闭 → 直接 decline，不转发 TUI、不注册 pending。
		// 对齐业内既有实现 GranularApprovalConfig.mcp_elicitations=false 的"自动拒绝"语义。
		if !c.ElicitationsEnabled() {
			return map[string]interface{}{
				"action":  string(ElicitationActionDecline),
				"content": nil,
			}, nil
		}
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse elicitation params: %w", err)
		}
		// 信任域边界净化。message 来自外部 MCP server（不可信），
		// 在进入本进程信任域处剥离终端控制字符，防止 \r 伪造 / \x1b 清屏等注入。
		p.Message = util.SanitizeExternalText(p.Message)
		c.elicitationMu.Lock()
		handler := c.elicitationHandler
		c.elicitationMu.Unlock()
		if handler == nil {
			// 降级：无 handler 时返回 decline（fail-safe）
			return map[string]interface{}{
				"action":  string(ElicitationActionDecline),
				"content": nil,
			}, nil
		}
		// 注册 pending 条目到 router（对标同类 CLI router.requests.insert）
		// nil 保护：测试中未初始化 elicitationRouter 时返回 decline（fail-safe）
		if c.elicitationRouter == nil {
			return map[string]interface{}{
				"action":  string(ElicitationActionDecline),
				"content": nil,
			}, nil
		}
		respCh := make(chan ElicitationResponse, 1)
		c.elicitationRouter.register(string(reqID), respCh)
		// 转发到 TUI handler（handler 内部必须非阻塞）
		handler(c.name, reqID, p.Message)
		// 启动独立 goroutine 等待响应/300s 超时
		go c.awaitElicitationResponse(reqID, respCh)
		// 返回哨兵：readLoop 不发响应（致命漏洞 #1 修复核心）
		return nil, errElicitationPending
	default:
		return nil, fmt.Errorf("unsupported server request: %s", method)
	}
}

// awaitElicitationResponse 等待 TUI 响应或 300s TTL 超时，发送 JSON-RPC response
// 对标同类 CLI make_sender closure 的 rx.await
// 第2轮修复（致命漏洞 #4）：独立 300s TTL，与 executeToolsDirect 的 180s ctx 解耦
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样透传到 sendElicitationResponse
func (c *Client) awaitElicitationResponse(reqID json.RawMessage, respCh chan ElicitationResponse) {
	defer c.elicitationRouter.unregister(string(reqID))
	// 独立 300s TTL，不依赖 executeToolsDirect 的 180s ctx（致命漏洞 #4 修复核心）
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	var resp ElicitationResponse
	select {
	case resp = <-respCh:
		// TUI 回传响应（用户决策）
	case <-ctx.Done():
		// 300s TTL 超时，自动 cancel
		resp = ElicitationResponse{RequestID: reqID, Action: ElicitationActionCancel}
	}
	// 确保 resp.RequestID 已填充
	if len(resp.RequestID) == 0 {
		resp.RequestID = reqID
	}
	c.sendElicitationResponse(reqID, resp)
}

// sendElicitationResponse 通过 transport 直接发送 elicitation JSON-RPC response
// 对标同类 CLI rmcp 库基于 make_sender 返回值自动发送 response 的机制
// 第2轮修复（致命漏洞 #1）：绕过 dispatchRequest 返回值，直接调用 Transport.SendResponse
// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，transport 层原样回传
func (c *Client) sendElicitationResponse(reqID json.RawMessage, resp ElicitationResponse) {
	result := map[string]interface{}{
		"action":  string(resp.Action),
		"content": resp.Content,
	}
	if err := c.trans.SendResponse(reqID, result); err != nil {
		// 第3轮修复采纳第3轮审查报告 §4 优化 #4：不再 _ = 忽略错误，便于诊断
		log.Printf("SendResponse failed for elicitation %s: %v", string(reqID), err)
	}
}

// ResolveElicitation 异步回传 elicitation 响应给 MCP server
// 由 TUI 层在用户决策后通过 elicitationResponseMsg 触发调用
// 对标同类 CLI ElicitationRequestManager::resolve
// 第3轮修复（致命漏洞 #2）：resp.RequestID 类型为 json.RawMessage
func (c *Client) ResolveElicitation(resp ElicitationResponse) error {
	if !c.elicitationRouter.resolve(string(resp.RequestID), resp) {
		return fmt.Errorf("elicitation request not found or channel full: %s", string(resp.RequestID))
	}
	return nil
}
