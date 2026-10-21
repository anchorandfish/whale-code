// Package mcp — 流式工具结果支持（T1-P1-A3 /）
//
// 对标 MCP 规范 2025-06-18 §"Tools" + §"Transports"（访问日期 2026-07-21）：
// - Streamable HTTP 传输：server 可对 tools/call 返回 text/event-stream 流式响应
// - notifications/progress：长任务运行中 server 推送进度通知（progressToken 关联）
// - 四种传输（stdio/sse/http/ws）统一通过 SendStream 方法暴露流式能力
//
// 本文件定义流式相关的类型与接口，不修改现有 MCPClient / Transport 接口
// （C3 兼容性保持：新增 MCPStreamingClient / StreamingTransport 子接口）。
package mcp

import (
	"context"
	"encoding/json"
)

// StreamFrameType 流式帧类型枚举（用于内部识别）
const (
	// streamFrameResponse 表示该帧是 tools/call 的最终响应（含 Result 或 Error）
	streamFrameResponse = "response"
	// streamFrameProgress 表示该帧是 notifications/progress 进度通知
	streamFrameProgress = "progress"
)

// StreamChunkType 流式分块类型（暴露给调用方）
const (
	// StreamChunkProgress 进度通知分块（可被背压丢弃）
	StreamChunkProgress = "progress"
	// StreamChunkResult 最终结果分块（受保护，永不丢弃）
	StreamChunkResult = "result"
	// StreamChunkError 错误分块（受保护，永不丢弃）
	StreamChunkError = "error"
)

// StreamFrame 是 transport 层的流式帧，包含响应、通知、进度、错误四类语义。
// transport 层 SendStream 返回 <-chan StreamFrame，由 Client 层转换为 StreamChunk。
type StreamFrame struct {
	// ID 请求 ID（匹配 SendStream 调用时的 id）
	ID int64 `json:"id,omitempty"`
	// Method 通知方法（如 "notifications/progress"）
	Method string `json:"method,omitempty"`
	// Params 通知参数（progressToken / progress / total / message）
	Params json.RawMessage `json:"params,omitempty"`
	// Result 最终响应结果（tools/call 的 result 字段）
	Result json.RawMessage `json:"result,omitempty"`
	// Error 错误（JSON-RPC error 字段）
	Error *RPCError `json:"error,omitempty"`
	// Progress 进度信息（从 Method="notifications/progress" 的 Params 解析）
	Progress *ProgressInfo `json:"-"`
}

// ProgressInfo 是 notifications/progress 通知的解析结果。
// 对标 MCP 规范 2025-06-18 §"Progress Notifications"（访问日期 2026-07-21）。
type ProgressInfo struct {
	// ProgressToken 进度令牌（与请求 _meta.progressToken 一致）
	ProgressToken interface{} `json:"progressToken"`
	// Progress 当前进度（0-total）
	Progress float64 `json:"progress"`
	// Total 总量（0 表示未知）
	Total float64 `json:"total,omitempty"`
	// Message 进度消息（可选）
	Message string `json:"message,omitempty"`
}

// StreamChunk 是 Client 层暴露给调用方的流式分块。
// 调用方从 <-chan StreamChunk 读取分块，按 Type 分发处理。
type StreamChunk struct {
	// Type 分块类型："progress" | "result" | "error"
	Type string `json:"type"`
	// Progress 进度百分比 0-100（Type="progress"）
	Progress *float64 `json:"progress,omitempty"`
	// Total 总量（Type="progress"，可选，0 表示未知）
	Total *float64 `json:"total,omitempty"`
	// Message 进度消息（Type="progress"，已消毒）/ 错误消息（Type="error"）
	Message string `json:"message,omitempty"`
	// Result 最终结果（Type="result"，已消毒）
	Result *ToolResult `json:"result,omitempty"`
	// Error 错误（Type="error"）
	Error error `json:"-"`
}

// MCPStreamingClient 是支持流式工具调用的 MCP 客户端接口。
// 嵌入 MCPClient 保持向后兼容；Client 类型同时实现两个接口。
//
// 使用方式：
//
//	ch, err := client.CallToolStream(ctx, "long_running_tool", args)
//	if err != nil { ... }
//	for chunk := range ch {
//
// switch chunk.Type {
// case mcp.StreamChunkProgress:
// fmt.Printf("进度: %.1f%% - %s\n", *chunk.Progress, chunk.Message)
// case mcp.StreamChunkResult:
// fmt.Println("结果:", chunk.Result.Text())
// case mcp.StreamChunkError:
// fmt.Println("错误:", chunk.Error)
// }
//
//	}
type MCPStreamingClient interface {
	MCPClient
	// CallToolStream 流式调用工具，返回 <-chan StreamChunk 分块通道。
	// 调用方应 range channel 读取分块，channel 关闭表示流结束。
	// ctx 取消会关闭 channel 并退出 goroutine，无泄漏。
	// transport 不支持 StreamingTransport 时自动降级为 CallTool 一次性返回（包装为 1-chunk 流）。
	CallToolStream(ctx context.Context, name string, arguments map[string]interface{}) (<-chan StreamChunk, error)
}

// StreamingTransport 是支持流式响应的 transport 子接口。
// Transport 接口不动；4 个 transport 实现各自实现此子接口（可选）。
// CallToolStream 通过类型断言检查 transport 是否实现此接口，未实现则降级为 CallTool。
//
// 对标 MCP 规范 2025-06-18 §"Transports"（访问日期 2026-07-21）：
// - Streamable HTTP：server 可返回 text/event-stream 流式响应
// - stdio：通过 notifications/progress + 最终响应实现流式语义
// - SSE/WS：类似 stdio，单连接多路复用
type StreamingTransport interface {
	Transport
	// SendStream 发送 JSON-RPC 请求并返回流式响应 channel。
	// channel 推送 StreamFrame（响应 / 通知 / 进度 / 错误），关闭表示流结束。
	// transport 层负责根据 progressToken / 请求 ID 路由到对应 channel。
	// ctx 取消时 transport 应关闭 channel 并清理路由表。
	SendStream(ctx context.Context, id int64, method string, params interface{}) (<-chan StreamFrame, error)
}

// ParseProgressInfo 从 notifications/progress 的 params 中解析 ProgressInfo。
// 对标 MCP 规范 2025-06-18 §"Progress Notifications"（访问日期 2026-07-21）。
// 解析失败返回 nil（调用方应跳过该通知）。
func ParseProgressInfo(params json.RawMessage) *ProgressInfo {
	if len(params) == 0 {
		return nil
	}
	var info ProgressInfo
	if err := json.Unmarshal(params, &info); err != nil {
		return nil
	}
	return &info
}

// 编译时断言：4 个 transport 实现均实现 StreamingTransport 子接口。
// 任一 transport 未实现 SendStream 方法时编译失败，确保流式能力一致性。
var (
	_ StreamingTransport = (*StdioTransport)(nil)
	_ StreamingTransport = (*HTTPTransport)(nil)
	_ StreamingTransport = (*SSETransport)(nil)
	_ StreamingTransport = (*WebSocketTransport)(nil)
)
