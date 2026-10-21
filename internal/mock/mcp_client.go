// Package mock — test mocks for isolating external dependencies
// mcp_client.go — MCP 客户端 Mock
package mock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/mcp"
)

// Verify MockMCPClient implements mcp.MCPClient at compile time
var _ mcp.MCPClient = (*MockMCPClient)(nil)

// Verify MockMCPClient implements mcp.MCPStreamingClient at compile time ()
var _ mcp.MCPStreamingClient = (*MockMCPClient)(nil)

// MockMCPClient is a mock implementation of mcp.MCPClient for testing
type MockMCPClient struct {
	mu            sync.Mutex
	ToolsList     []mcp.ToolInfo
	CallResults   map[string]*mcp.ToolResult   // toolName → result
	StreamResults map[string][]mcp.StreamChunk // toolName → 流式分块序列（，可选）
	Err           error                        // 可注入错误
	ready         bool
	name          string
}

// NewMockMCPClient creates a mock MCP client
func NewMockMCPClient() *MockMCPClient {
	return &MockMCPClient{
		CallResults: make(map[string]*mcp.ToolResult),
		ready:       true,
		name:        "mock-mcp-server",
	}
}

// Ping checks server connection (always succeeds in mock)
func (m *MockMCPClient) Ping(ctx context.Context) error {
	if m.Err != nil {
		return m.Err
	}
	return nil
}

// StartHeartbeat starts periodic ping (no-op in mock)
func (m *MockMCPClient) StartHeartbeat(interval time.Duration) {
	// no-op in mock
}

// ListTools lists available tools from MCP server
func (m *MockMCPClient) ListTools(ctx context.Context) ([]mcp.ToolInfo, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.ToolsList, nil
}

// CallTool calls a tool on the MCP server
func (m *MockMCPClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if result, ok := m.CallResults[name]; ok {
		return result, nil
	}
	return nil, fmt.Errorf("tool %s not found", name)
}

// CallToolStream 流式调用工具（Mock 实现， / T1-P1-A3）。
//
// Mock 行为：
// - 若 StreamResults[name] 已注入分块序列：按顺序推送所有 chunk 后关闭 channel
// - 否则降级为一次性返回（直接访问 CallResults 字段，避免与 CallTool 重复加锁死锁）
//
// 测试用例通过 StreamResults 字段注入多 chunk 流以模拟 server 流式行为。
func (m *MockMCPClient) CallToolStream(ctx context.Context, name string, arguments map[string]interface{}) (<-chan mcp.StreamChunk, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// 注入式流式：按预设序列推送
	if m.StreamResults != nil {
		if chunks, ok := m.StreamResults[name]; ok {
			ch := make(chan mcp.StreamChunk, len(chunks))
			for _, c := range chunks {
				ch <- c
			}
			close(ch)
			return ch, nil
		}
	}

	// 默认降级：直接访问 CallResults 字段（避免调 CallTool 重复加锁死锁）
	ch := make(chan mcp.StreamChunk, 1)
	if result, ok := m.CallResults[name]; ok {
		ch <- mcp.StreamChunk{Type: mcp.StreamChunkResult, Result: result}
	} else {
		err := fmt.Errorf("tool %s not found", name)
		ch <- mcp.StreamChunk{Type: mcp.StreamChunkError, Error: err, Message: err.Error()}
	}
	close(ch)
	return ch, nil
}

// ListPrompts lists available prompts from MCP server (returns empty in mock)
func (m *MockMCPClient) ListPrompts(ctx context.Context) ([]mcp.PromptInfo, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return []mcp.PromptInfo{}, nil
}

// GetPrompt gets a prompt from MCP server (returns empty in mock)
func (m *MockMCPClient) GetPrompt(ctx context.Context, name string, arguments map[string]string) ([]mcp.PromptMessage, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return []mcp.PromptMessage{}, nil
}

// ListResources lists available resources from MCP server (returns empty in mock)
func (m *MockMCPClient) ListResources(ctx context.Context) ([]mcp.Resource, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return []mcp.Resource{}, nil
}

// ReadResource reads a resource from MCP server (returns empty in mock)
func (m *MockMCPClient) ReadResource(ctx context.Context, uri string) (string, error) {
	if m.Err != nil {
		return "", m.Err
	}
	return "", nil
}

// Capabilities returns server capabilities (returns empty in mock)
func (m *MockMCPClient) Capabilities() mcp.ServerCapabilities {
	return mcp.ServerCapabilities{}
}

// HasTools checks if the server has tools
func (m *MockMCPClient) HasTools() bool {
	return len(m.ToolsList) > 0
}

// HasResources checks if the server has resources (always false in mock)
func (m *MockMCPClient) HasResources() bool {
	return false
}

// HasPrompts checks if the server has prompts (always false in mock)
func (m *MockMCPClient) HasPrompts() bool {
	return false
}

// Tools returns cached tool list
func (m *MockMCPClient) Tools() []mcp.ToolInfo {
	return m.ToolsList
}

// IsReady checks if the client is ready
func (m *MockMCPClient) IsReady() bool {
	return m.ready
}

// Name returns the server name
func (m *MockMCPClient) Name() string {
	return m.name
}

// Transport returns the transport type
func (m *MockMCPClient) Transport() string {
	return "mock"
}

// ServerURL returns the server URL (empty in mock)
func (m *MockMCPClient) ServerURL() string {
	return ""
}

// Subscribe 订阅资源变更通知（/ M17-01 Mock 实现）。
// Mock 默认不支持资源订阅，返回 ErrSubscribeNotSupported。
// 测试需要模拟订阅行为时，可通过嵌入覆盖此方法。
func (m *MockMCPClient) Subscribe(ctx context.Context, uri string) error {
	return mcp.ErrSubscribeNotSupported
}

// Unsubscribe 取消资源变更订阅（/ M17-01 Mock 实现）。
// Mock 幂等返回 nil（未订阅也返回成功）。
func (m *MockMCPClient) Unsubscribe(ctx context.Context, uri string) error {
	return nil
}

// SetResourceHandler 注册资源变更回调（/ M17-01 Mock 实现）。
// Mock 不处理资源通知，此方法为 no-op。
func (m *MockMCPClient) SetResourceHandler(handler mcp.ResourceChangeHandler) {
	// no-op in mock
}

// SetElicitationHandler 注册 elicitation 请求回调（P0-7 Mock 实现）。
// Mock 不处理 elicitation 请求，此方法为 no-op。
func (m *MockMCPClient) SetElicitationHandler(handler mcp.ElicitationHandler) {
	// no-op in mock
}

// ResolveElicitation 异步回传 elicitation 响应（P0-7 Mock 实现）。
// Mock 不维护 elicitation 路由表，直接返回 nil（无操作）。
func (m *MockMCPClient) ResolveElicitation(resp mcp.ElicitationResponse) error {
	return nil
}

// Close closes the client connection (no-op in mock)
func (m *MockMCPClient) Close() error {
	return nil
}

// SetReady sets the ready state for testing
func (m *MockMCPClient) SetReady(ready bool) {
	m.ready = ready
}

// SetName sets the server name for testing
func (m *MockMCPClient) SetName(name string) {
	m.name = name
}

// MockMCPServer is kept for backward compatibility; prefer MockMCPClient
// Deprecated: Use MockMCPClient instead
type MockMCPServer = MockMCPClient
