// Package lsp — / M18-02 mock LSP server（测试 L3 层）
//
// 提供可注入到 Client 的 mock LSP server，用于在不依赖真实 gopls/pyright 等
// 语言服务器的环境下测试 LSP 客户端逻辑（initialize 握手 / 3.17 新特性 / 诊断推送）。
//
// 设计原则（02-审查报告 §2.5 过度设计检查）：
// - 仅实现测试必需方法（initialize + RegisterHandler 注册的自定义方法）
// - 不实现全协议（无 textDocument/synchronization 完整状态机）
// - 仅在 _test.go 中引用（不暴露到生产）
//
// 对标 LSP 3.17 spec §"Base Protocol"（JSON-RPC over stdio）
// [microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) @2023-08
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// MockHandler 处理一个 JSON-RPC 请求并返回响应结果或错误。
// params 为请求 params（已反序列化为 json.RawMessage，由 handler 自行解析）。
// 返回 (result, err)：err != nil 时包装为 JSON-RPC error 响应。
type MockHandler func(params json.RawMessage) (result interface{}, err error)

// MockServer mock LSP server（JSON-RPC over stdio）。
//
// 使用方式（测试中）：
//
//	server := NewMockServer()
//	server.RegisterHandler("textDocument/inlayHint", func(params json.RawMessage) (interface{}, error) {
//
// return []InlayHint{{Position: Position{Line: 0, Character: 0}, Label: ": int"}}, nil
//
//	})
//	go server.Serve(stdinWriter, stdoutReader)
//
// 通过 RegisterHandler 注册自定义响应，未注册的方法返回 JSON-RPC method not found。
type MockServer struct {
	handlers map[string]MockHandler
	mu       sync.RWMutex

	// stopped 防止 Serve 重复启动 + Close 幂等
	stopped bool
	stopMu  sync.Mutex

	// served 标记 Serve 已启动（Close 时判断是否需要等待）
	served   bool
	servedMu sync.Mutex

	// initializeParams 缓存最近一次 initialize 请求参数（用于测试断言）
	initializeParams json.RawMessage
	initMu           sync.Mutex

	// receivedMethods 记录所有已收到的请求 method（用于测试断言顺序）
	receivedMethods []string
	methodsMu       sync.Mutex
}

// NewMockServer 创建一个空 handler 表的 mock LSP server。
//
// 默认注册 initialize / initialized / shutdown 三个标准方法，
// 业务方法（inlayHint/semanticTokens/codeLens 等）由调用方 RegisterHandler 注入。
func NewMockServer() *MockServer {
	s := &MockServer{
		handlers: make(map[string]MockHandler),
	}
	// 默认 initialize handler：返回最小 capabilities
	s.handlers["initialize"] = func(params json.RawMessage) (interface{}, error) {
		s.initMu.Lock()
		s.initializeParams = params
		s.initMu.Unlock()
		return map[string]interface{}{
			"capabilities": map[string]interface{}{
				"textDocumentSync":       1, // Full sync
				"inlayHintProvider":      true,
				"semanticTokensProvider": map[string]interface{}{"full": true},
				"codeLensProvider":       map[string]interface{}{},
				"definitionProvider":     true,
				"referencesProvider":     true,
				"hoverProvider":          true,
				"completionProvider":     map[string]interface{}{},
				"renameProvider":         true,
				"documentSymbolProvider": true,
			},
			"serverInfo": map[string]interface{}{
				"name":    "mock-lsp-server",
				"version": "0.0.1-mock",
			},
		}, nil
	}
	// initialized 是 notification（无 id），handler 返回 nil + nil 即可（不发响应）
	s.handlers["initialized"] = func(params json.RawMessage) (interface{}, error) {
		return nil, nil
	}
	// shutdown 返回 nil（LSP 规范要求）
	s.handlers["shutdown"] = func(params json.RawMessage) (interface{}, error) {
		return nil, nil
	}
	return s
}

// RegisterHandler 注册或覆盖指定 method 的 handler。
//
// handler 为 nil 时移除该 method（后续请求返回 method not found）。
// 并发安全：可在 Serve 启动后动态注册（用于按 case 调整响应）。
func (s *MockServer) RegisterHandler(method string, handler MockHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if handler == nil {
		delete(s.handlers, method)
		return
	}
	s.handlers[method] = handler
}

// GetInitializeParams 返回最近一次 initialize 请求的 params（测试断言用）。
// 未收到 initialize 时返回 nil。
func (s *MockServer) GetInitializeParams() json.RawMessage {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	return s.initializeParams
}

// GetReceivedMethods 返回所有已收到的请求 method（按顺序，测试断言用）。
// 返回副本避免外部修改内部状态。
func (s *MockServer) GetReceivedMethods() []string {
	s.methodsMu.Lock()
	defer s.methodsMu.Unlock()
	out := make([]string, len(s.receivedMethods))
	copy(out, s.receivedMethods)
	return out
}

// Serve 在给定 io.Reader/io.Writer 上启动 JSON-RPC over stdio 循环。
//
// 协议格式：每条消息以 `Content-Length: N\r\n\r\n` 头 + N 字节 JSON body 组成。
// 阻塞直到 reader EOF 或 Close 被调用。
// 并发安全：可重复调用（但通常只调用一次）。
func (s *MockServer) Serve(r io.Reader, w io.Writer) {
	s.stopMu.Lock()
	if s.stopped {
		s.stopMu.Unlock()
		return
	}
	s.stopMu.Unlock()

	s.servedMu.Lock()
	s.served = true
	s.servedMu.Unlock()

	reader := bufio.NewReader(r)
	for {
		s.stopMu.Lock()
		if s.stopped {
			s.stopMu.Unlock()
			return
		}
		s.stopMu.Unlock()

		line, err := reader.ReadString('\n')
		if err != nil {
			return // EOF 或错误
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "Content-Length:") {
			continue
		}
		lengthStr := strings.TrimPrefix(line, "Content-Length:")
		length, err := strconv.Atoi(strings.TrimSpace(lengthStr))
		if err != nil {
			continue
		}
		// 读取空行（\r\n）
		reader.ReadString('\n')
		// 读取 body
		buf := make([]byte, length)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return
		}
		s.handleMessage(buf, w)
	}
}

// handleMessage 解析一条 JSON-RPC 消息并写出响应。
// notification（无 id）不发响应；request（有 id）必须发响应。
func (s *MockServer) handleMessage(body []byte, w io.Writer) {
	var msg struct {
		ID     *int64          `json:"id"` // 指针：nil=notification
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return
	}

	s.methodsMu.Lock()
	s.receivedMethods = append(s.receivedMethods, msg.Method)
	s.methodsMu.Unlock()

	// notification（无 id）：调用 handler 但不发响应
	if msg.ID == nil {
		s.mu.RLock()
		handler, ok := s.handlers[msg.Method]
		s.mu.RUnlock()
		if ok {
			_, _ = handler(msg.Params)
		}
		return
	}

	// request（有 id）：必须发响应
	s.mu.RLock()
	handler, ok := s.handlers[msg.Method]
	s.mu.RUnlock()

	var response map[string]interface{}
	if !ok {
		// method not found
		response = map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      *msg.ID,
			"error": map[string]interface{}{
				"code":    -32601, // method not found
				"message": fmt.Sprintf("method not found: %s", msg.Method),
			},
		}
	} else {
		result, err := handler(msg.Params)
		if err != nil {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"error": map[string]interface{}{
					"code":    -32603, // internal error
					"message": err.Error(),
				},
			}
		} else {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result":  result,
			}
		}
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		return
	}
	content := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(respBytes), string(respBytes))
	_, _ = w.Write([]byte(content))
}

// Close 停止 mock server（幂等）。
//
// 设置 stopped 标志让 Serve 循环在下一次迭代退出。
// 注意：Serve 当前可能阻塞在 reader.ReadString，需由调用方关闭 reader 触发 EOF。
func (s *MockServer) Close() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.stopped = true
}
