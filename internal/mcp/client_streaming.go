// Package mcp — Client 流式工具调用实现（T1-P1-A3 /）
//
// 本文件实现 Client.CallToolStream 方法，复用 T1-C 已落地的 BackpressurePipe
// （internal/ai/stream.go:723-830）做背压保护，对接 StreamingTransport 子接口。
//
// 对标 MCP 规范 2025-06-18 §"Tools"（访问日期 2026-07-21）：
// - tools/call 请求携带 _meta.progressToken
// - server 通过 notifications/progress 推送进度
// - server 最终返回 tools/call 响应（含 content / structuredContent / isError）
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// 编译时断言：Client 同时实现 MCPClient 与 MCPStreamingClient
var _ MCPStreamingClient = (*Client)(nil)

// CallToolStream 流式调用工具（/ T1-P1-A3）。
//
// 流程：
// 1. 检查 transport 是否实现 StreamingTransport，未实现则降级为 callToolStreamFallback
// 2. 生成 progressToken（复用 serverID 计数器，确保唯一）
// 3. 构造 tools/call 请求，_meta.progressToken 携带 token
// 4. 调 transport.SendStream 拿到 <-chan StreamFrame
// 5. 启动 goroutine：StreamFrame → StreamChunk 转换 + 消毒 + 写入 BackpressurePipe.in
// 6. BackpressurePipe.Run(ctx) 返回 out channel，转成 <-chan StreamChunk 返回
//
// goroutine 生命周期（吸纳审查优化 8）：
// - ctx 取消 → goroutine 退出 → 关闭 in channel → BackpressurePipe.Run 退出 → close(out)
// - transport channel 关闭 → goroutine 退出 → 关闭 in channel → close(out)
// - 双触发机制确保 goroutine 不泄漏
//
// 背压策略（吸纳审查优化 7）：
// - BackpressurePipe 在 Client 层接线（非 transport 层）
// - isProtectedStreamChunk 回调：Type=="result" || Type=="error" → 永不丢弃
// - Type=="progress" chunk 在 drop 策略下可丢弃
//
// 对标 MCP 规范 2025-06-18 §"Tools" + §"Progress Notifications"（访问日期 2026-07-21）。
func (c *Client) CallToolStream(ctx context.Context, name string, arguments map[string]interface{}) (<-chan StreamChunk, error) {
	// 1. 检查 transport 是否支持流式
	st, ok := c.trans.(StreamingTransport)
	if !ok {
		return c.callToolStreamFallback(ctx, name, arguments)
	}

	// 2. 生成 progressToken（复用 serverID 计数器，确保全局唯一）
	progressToken := c.serverID.Add(1)
	requestID := c.serverID.Add(1)

	// 3. 构造 tools/call 请求，_meta.progressToken 携带 token
	params := map[string]interface{}{
		"name":      name,
		"arguments": arguments,
		"_meta": map[string]interface{}{
			"progressToken": progressToken,
		},
	}

	// 4. 调 transport.SendStream
	frameCh, err := st.SendStream(ctx, requestID, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("send stream: %w", err)
	}

	// 5. BackpressurePipe 接线（Client 层，吸纳审查优化 7）
	bpCfg := ai.DefaultBackpressureConfig()
	in := make(chan StreamChunk, bpCfg.BufferSize)
	pipe := ai.NewBackpressurePipe[StreamChunk](in, bpCfg, isProtectedStreamChunk)
	out := pipe.Run(ctx)

	// 6. 启动 goroutine：StreamFrame → StreamChunk 转换 + 消毒 + 写入 in
	go func() {
		defer close(in)
		for {
			select {
			case <-ctx.Done():
				// ctx 取消：直接退出，close(in) 触发 BackpressurePipe.Run 退出
				return
			case frame, ok := <-frameCh:
				if !ok {
					// transport channel 关闭：流正常结束，退出 goroutine
					return
				}
				chunk := c.frameToChunk(frame)
				if chunk == nil {
					// 跳过无法解析的帧（如无关通知）
					continue
				}
				select {
				case in <- *chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// callToolStreamFallback 当 transport 不支持 StreamingTransport 时的降级路径。
// 内部调用 CallTool 一次性返回，包装为 1-chunk 流（result chunk 或 error chunk）后关闭 channel。
//
// 对标 MCP 规范 2025-06-18：旧 server 不支持流式时，client 应透明降级
// （规范未强制要求 server 支持流式，client 降级行为符合规范）。
func (c *Client) callToolStreamFallback(ctx context.Context, name string, arguments map[string]interface{}) (<-chan StreamChunk, error) {
	result, err := c.CallTool(ctx, name, arguments)
	ch := make(chan StreamChunk, 1)
	if err != nil {
		// 错误也通过 channel 返回，保持调用方代码统一
		ch <- StreamChunk{Type: StreamChunkError, Error: err, Message: err.Error()}
	} else {
		ch <- StreamChunk{Type: StreamChunkResult, Result: result}
	}
	close(ch)
	return ch, nil
}

// frameToChunk 把 StreamFrame 转换为 StreamChunk 并消毒。
//
// 转换规则：
// - frame.Error != nil → StreamChunk{Type: "error", Error: frame.Error}
// - frame.Result 非空 → StreamChunk{Type: "result", Result: SanitizeToolResult(...)}
// - frame.Progress != nil → StreamChunk{Type: "progress", Progress: ..., Message: SanitizeString(...)}
// - 其他（无关通知等） → nil（调用方跳过）
//
// 消毒（吸纳审查优化 4）：
// - result 走 SanitizeToolResult（R1-R5 管线，与 CallTool 一致）
// - progress Message 走 SanitizeString（防止 server 注入）
func (c *Client) frameToChunk(frame StreamFrame) *StreamChunk {
	// 错误优先
	if frame.Error != nil {
		return &StreamChunk{
			Type:    StreamChunkError,
			Error:   frame.Error,
			Message: frame.Error.Error(),
		}
	}

	// 最终结果
	if len(frame.Result) > 0 {
		var resp ToolResult
		if err := json.Unmarshal(frame.Result, &resp); err != nil {
			return &StreamChunk{
				Type:  StreamChunkError,
				Error: fmt.Errorf("parse tool result: %w", err),
			}
		}
		sanitized, err := SanitizeToolResult(&resp)
		if err != nil {
			return &StreamChunk{
				Type:  StreamChunkError,
				Error: fmt.Errorf("sanitize tool result: %w", err),
			}
		}
		if sanitized.IsError {
			return &StreamChunk{
				Type:    StreamChunkError,
				Error:   fmt.Errorf("tool error: %s", sanitized.Text()),
				Message: sanitized.Text(),
			}
		}
		return &StreamChunk{
			Type:   StreamChunkResult,
			Result: sanitized,
		}
	}

	// 进度通知
	if frame.Progress != nil {
		progress := frame.Progress.Progress
		total := frame.Progress.Total
		message := SanitizeString(frame.Progress.Message) // 进度消息消毒（吸纳审查优化 4）
		// 转换为百分比：若 total > 0，progress/total*100；否则原值
		var percent float64
		if total > 0 {
			percent = (progress / total) * 100
		} else {
			percent = progress
		}
		return &StreamChunk{
			Type:     StreamChunkProgress,
			Progress: &percent,
			Total:    &total,
			Message:  message,
		}
	}

	// 其他通知（如 notifications/cancelled 由 transport 层处理为 Error frame，不会到达此处）
	return nil
}

// isProtectedStreamChunk 判断 StreamChunk 是否受保护（永不丢弃）。
//
// 受保护（吸纳审查优化 7）：
// - Type == "result"：最终结果必须到达调用方
// - Type == "error"：错误必须到达调用方
//
// 可丢弃：
// - Type == "progress"：背压触发时丢弃进度通知，不影响最终结果
func isProtectedStreamChunk(chunk StreamChunk) bool {
	return chunk.Type == StreamChunkResult || chunk.Type == StreamChunkError
}
