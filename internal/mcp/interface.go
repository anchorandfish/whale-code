// Package mcp — MCPClient interface for dependency injection and testing
package mcp

import (
	"context"
	"encoding/json"
	"time"
)

// ResourceChangeHandler 资源变更回调（/ M17-01）。
// 当 server 推送 notifications/resources/updated 或 notifications/resources/list_changed 时，
// Client 通过此回调将通知转发给调用方。
// method 参数为通知方法名，uri 为资源 URI（list_changed 时为空），params 为原始 JSON 参数。
// 调用方负责消毒与异步处理（同步处理会阻塞 readLoop）。
type ResourceChangeHandler func(method string, uri string, params json.RawMessage)

// MCPClient defines the interface for MCP client operations.
// The concrete Client type implements this interface; mock implementations
// can be used in tests to isolate external dependencies.
//
// SNAPSHOT：新增 Subscribe / Unsubscribe / SetResourceHandler 3 方法（M17-01 资源订阅）。
// 现有 mock 实现需补 3 方法（返回 ErrSubscribeNotSupported / nil）。
type MCPClient interface {
	// Ping checks server connection
	Ping(ctx context.Context) error
	// StartHeartbeat starts periodic ping to detect connection issues
	StartHeartbeat(interval time.Duration)
	// ListTools lists available tools from MCP server
	ListTools(ctx context.Context) ([]ToolInfo, error)
	// CallTool calls a tool on the MCP server
	CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error)
	// ListPrompts lists available prompts from MCP server
	ListPrompts(ctx context.Context) ([]PromptInfo, error)
	// GetPrompt gets a prompt from MCP server
	GetPrompt(ctx context.Context, name string, arguments map[string]string) ([]PromptMessage, error)
	// ListResources lists available resources from MCP server
	ListResources(ctx context.Context) ([]Resource, error)
	// ReadResource reads a resource from MCP server
	ReadResource(ctx context.Context, uri string) (string, error)
	// Capabilities returns server capabilities
	Capabilities() ServerCapabilities
	// HasTools checks if the server has tools
	HasTools() bool
	// HasResources checks if the server has resources
	HasResources() bool
	// HasPrompts checks if the server has prompts
	HasPrompts() bool
	// Tools returns cached tool list
	Tools() []ToolInfo
	// IsReady checks if the client is ready
	IsReady() bool
	// Name returns the server name
	Name() string
	// Transport returns the transport type
	Transport() string
	// ServerURL returns the server URL
	ServerURL() string
	// Close closes the client connection
	Close() error
	// Subscribe 订阅资源变更通知（/ M17-01）。
	// server capabilities.resources.subscribe=false 时返回 ErrSubscribeNotSupported。
	// 重复订阅同一 URI 幂等（map 去重）。
	// 对标 MCP 规范 2025-06-18 §"Resources" resources/subscribe（访问日期 2026-07-21）。
	Subscribe(ctx context.Context, uri string) error
	// Unsubscribe 取消资源变更订阅（/ M17-01）。
	// 未订阅的 URI 返回 nil（幂等）。
	// 对标 MCP 规范 2025-06-18 §"Resources" resources/unsubscribe（访问日期 2026-07-21）。
	Unsubscribe(ctx context.Context, uri string) error
	// SetResourceHandler 注册资源变更回调（/ M17-01）。
	// handler=nil 清除回调（subscriptions map 不清理，仍保持订阅状态）。
	// 调用方应在异步 goroutine 中处理通知（同步处理阻塞 readLoop）。
	SetResourceHandler(handler ResourceChangeHandler)
	// SetElicitationHandler 注册 elicitation 请求回调（P0-7 / 第2轮修复）。
	// handler=nil 清除回调，elicitation 请求将返回 decline 响应（fail-safe）。
	// 对标同类 CLI ElicitationRequestManager。
	SetElicitationHandler(handler ElicitationHandler)
	// ResolveElicitation 异步回传 elicitation 响应给 MCP server（P0-7 / 第2轮修复）。
	// 由 TUI 层在用户决策后通过 elicitationResponseMsg 触发调用。
	// 对标同类 CLI ElicitationRequestManager::resolve。
	ResolveElicitation(resp ElicitationResponse) error
}
