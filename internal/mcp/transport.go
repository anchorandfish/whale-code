package mcp

import (
	"context"
	"encoding/json"
)

// RequestHandler 处理 server-side 请求（如 elicitation/create）
// 第3轮修复（新引入致命漏洞 #1）：新增此类型，对标 SetNotificationHandler 模式
// readLoop 收到同时含 id + method 的 JSON-RPC 2.0 请求时调用此 handler
//
// 第3轮修复（新引入致命漏洞 #2）：reqID 类型为 json.RawMessage
// 对标同类 CLI RequestId enum（String(String) | Integer(i64)；REF-外部-同类 CLI-RequestId）
// 保留原始 JSON 字节，transport 层发送响应时原样回传，无损支持 string/number 双类型
//
// 返回值契约：
// - result 非 nil + err=nil：transport 层发送 success response（用 reqID 原样回传）
// - err 非 nil 且 err != errElicitationPending：transport 层发送 error response
// - err == errElicitationPending：transport 层不发任何响应（由调用方异步发送，对标第2轮致命漏洞 #1 哨兵契约）
type RequestHandler func(method string, params json.RawMessage, reqID json.RawMessage) (interface{}, error)

// Transport is the MCP transport layer interface, unifying stdio/SSE/HTTP/WS transports
type Transport interface {
	// Send sends JSON-RPC request and waits for response
	Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error)
	// SendNotification sends JSON-RPC notification (no ID, no response expected)
	SendNotification(ctx context.Context, method string, params interface{}) error
	// SetNotificationHandler sets notification callback
	SetNotificationHandler(handler NotificationHandler)
	// Close closes transport connection
	Close() error

	// SendResponse 发送 JSON-RPC success response 给 server
	// 第2轮修复（致命漏洞 #1 前置条件）：用于 awaitElicitationResponse 异步发送响应
	// 第3轮修复（致命漏洞 #2）：reqID 类型为 json.RawMessage，原样写入响应消息的 id 字段
	// 对标同类 CLI rmcp 库基于 make_sender 返回值自动发送 response 的机制
	//
	// 参数：
	//   - reqID 请求 ID（原始 JSON 字节，string/number 双类型）
	//   - result 响应结果（会被 JSON marshal 后写入 result 字段）
	//
	// 返回：error，nil 表示发送成功
	SendResponse(reqID json.RawMessage, result interface{}) error

	// SetRequestHandler 注册 server-side 请求回调
	// 第3轮修复（新引入致命漏洞 #1）：readLoop 收到 id + method 同时存在的消息时调用此 handler
	// handler=nil 清除回调，server-side 请求将返回 -32601 method not found（fail-safe，对标 SetNotificationHandler 模式）
	//
	// 参数：
	//   - handler 请求回调（不能阻塞 readLoop，handler 内部应异步处理）
	SetRequestHandler(handler RequestHandler)
}
