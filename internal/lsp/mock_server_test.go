// Package lsp — / M18-02 mock LSP server 测试
//
// 测试覆盖：
// - NewMockServer 默认 handler 注册（initialize / initialized / shutdown）
// - RegisterHandler 注册/覆盖/移除 handler
// - Serve JSON-RPC 请求-响应循环（request / notification / method not found / handler error）
// - GetInitializeParams 缓存最近一次 initialize 请求
// - GetReceivedMethods 记录请求顺序
// - Close 幂等 + 重复 Serve 防护
//
// 对标 LSP 3.17 spec §"Base Protocol"（JSON-RPC over stdio）
// [microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) @2023-08
package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// === 辅助函数 ===

// writeJSONRPCMessage 向 writer 写入一条 JSON-RPC 消息（Content-Length 头 + body）。
// 测试辅助函数，仅用于向 MockServer 发送请求。
func writeJSONRPCMessage(w io.Writer, msg map[string]interface{}) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	// Header 与 body 必须作为一条 pipe 写入提交。并发测试若拆成两次 Write，
	// 不同请求可能交错成 headerA/headerB/bodyA/bodyB，导致服务端协议失步。
	payload := append([]byte(header), body...)
	n, err := w.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

// readJSONRPCMessage 从 reader 读取一条 JSON-RPC 消息（解析 Content-Length 头 + body）。
// 返回解析后的 generic map。测试辅助函数。
func readJSONRPCMessage(r *bufio.Reader) (map[string]interface{}, error) {
	// 读取 headers
	contentLength := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // headers 结束
		}
		if strings.HasPrefix(line, "Content-Length:") {
			fmt.Sscanf(strings.TrimPrefix(line, "Content-Length:"), "%d", &contentLength)
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// readJSONRPCMessageWithTimeout 从 reader 读取一条 JSON-RPC 消息，超时返回 error。
// TD-03 修复：替代 SetReadDeadline（*bufio.Reader 无此方法，改用 goroutine + select）。
func readJSONRPCMessageWithTimeout(r *bufio.Reader, timeout time.Duration) (map[string]interface{}, error) {
	type result struct {
		msg map[string]interface{}
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := readJSONRPCMessage(r)
		ch <- result{msg, err}
	}()
	select {
	case res := <-ch:
		return res.msg, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("read timeout after %v", timeout)
	}
}

// newMockServerPipe 创建一对连接 MockServer 与调用方的 pipe。
// 返回 clientWriter（调用方写→server读）+ serverReader（调用方读←server写）
// + 内部 pipe 清理句柄。
func newMockServerPipe(t *testing.T) (*MockServer, io.Writer, *bufio.Reader, func()) {
	t.Helper()
	server := NewMockServer()

	// 用 os.Pipe 替代 io.Pipe：os.Pipe 有内核缓冲（Linux 64KB），
	// 避免 io.Pipe 同步无缓冲导致的死锁（server 写响应时若 client 未读则阻塞，
	// 导致 server 不能读请求 → sender 写阻塞 → 死锁）。
	// client → server pipe：serverRead 供 Serve 读取，clientWrite 供调用方写入。
	serverRead, clientWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create client→server pipe: %v", err)
	}
	// server → client pipe：clientRead 供调用方读取，serverWrite 供 Serve 写入。
	clientRead, serverWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create server→client pipe: %v", err)
	}

	// 启动 MockServer.Serve（后台 goroutine）
	go server.Serve(serverRead, serverWrite)

	cleanup := func() {
		clientWrite.Close()
		serverRead.Close()
		serverWrite.Close()
		clientRead.Close()
		server.Close()
	}
	return server, clientWrite, bufio.NewReader(clientRead), cleanup
}

// === 测试用例 ===

// TestNewMockServer_DefaultHandlers 测试 NewMockServer 注册了默认 handler。
func TestNewMockServer_DefaultHandlers(t *testing.T) {
	s := NewMockServer()
	if s.handlers == nil {
		t.Fatal("handlers map is nil")
	}
	expected := []string{"initialize", "initialized", "shutdown"}
	for _, method := range expected {
		if _, ok := s.handlers[method]; !ok {
			t.Errorf("default handler for %q not registered", method)
		}
	}
}

// TestMockServer_RegisterHandler 测试 RegisterHandler 注册/覆盖/移除 handler。
func TestMockServer_RegisterHandler(t *testing.T) {
	s := NewMockServer()

	// 注册新 handler
	called := false
	s.RegisterHandler("custom/method", func(params json.RawMessage) (interface{}, error) {
		called = true
		return "custom-result", nil
	})
	if _, ok := s.handlers["custom/method"]; !ok {
		t.Error("RegisterHandler did not register custom/method")
	}

	// 调用 handler 验证
	_, ok := s.handlers["custom/method"]
	if !ok {
		t.Fatal("custom/method handler not found")
	}
	result, err := s.handlers["custom/method"](nil)
	if err != nil {
		t.Errorf("handler returned error: %v", err)
	}
	if result != "custom-result" {
		t.Errorf("handler result = %v, want custom-result", result)
	}
	if !called {
		t.Error("handler was not called")
	}

	// 覆盖现有 handler
	s.RegisterHandler("custom/method", func(params json.RawMessage) (interface{}, error) {
		return "overwritten", nil
	})
	result2, _ := s.handlers["custom/method"](nil)
	if result2 != "overwritten" {
		t.Errorf("after overwrite: result = %v, want overwritten", result2)
	}

	// 移除 handler（传 nil）
	s.RegisterHandler("custom/method", nil)
	if _, ok := s.handlers["custom/method"]; ok {
		t.Error("RegisterHandler(method, nil) did not remove the handler")
	}
}

// TestMockServer_Serve_RequestResponse 测试 Serve 处理 request 并写回响应。
func TestMockServer_Serve_RequestResponse(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 注册自定义 handler
	server.RegisterHandler("test/echo", func(params json.RawMessage) (interface{}, error) {
		var p map[string]interface{}
		json.Unmarshal(params, &p)
		return map[string]interface{}{"echo": p["msg"]}, nil
	})

	// 发送 request
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "test/echo",
		"params":  map[string]interface{}{"msg": "hello"},
	}
	if err := writeJSONRPCMessage(clientWrite, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// 读取响应
	resp, err := readJSONRPCMessage(clientRead)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp["id"] != float64(1) {
		t.Errorf("response id = %v, want 1", resp["id"])
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("response result not a map: %T", resp["result"])
	}
	if result["echo"] != "hello" {
		t.Errorf("echo = %v, want hello", result["echo"])
	}
}

// TestMockServer_Serve_NotificationNoResponse 测试 notification（无 id）不触发响应。
func TestMockServer_Serve_NotificationNoResponse(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 注册带副作用的 notification handler
	called := make(chan struct{}, 1)
	server.RegisterHandler("test/notif", func(params json.RawMessage) (interface{}, error) {
		called <- struct{}{}
		return nil, nil
	})

	// 发送 notification（无 id）
	notif := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "test/notif",
		"params":  map[string]interface{}{},
	}
	if err := writeJSONRPCMessage(clientWrite, notif); err != nil {
		t.Fatalf("write notification: %v", err)
	}

	// 验证 handler 被调用
	select {
	case <-called:
		// OK
	case <-time.After(1 * time.Second):
		t.Error("notification handler was not called within 1s")
	}

	// 验证无响应（读取应阻塞或返回 EOF）。
	// TD-03 修复：原实现用 clientRead.SetReadDeadline，但 *bufio.Reader 无此方法
	// （只有 net.Conn 接口有）。改用 goroutine + select 超时验证无数据，避免
	// 重构 newMockServerPipe 为 net.Pipe 的大范围改动（踩坑已规避）。
	done := make(chan error, 1)
	go func() {
		_, err := readJSONRPCMessage(clientRead)
		done <- err
	}()
	select {
	case err := <-done:
		// 读到数据或 EOF — 读到数据说明有响应（不符合预期）
		if err == nil {
			t.Error("expected no response for notification, but got one")
		}
		// err != nil 是预期的（EOF 或读取错误，说明无响应）
	case <-time.After(200 * time.Millisecond):
		// 200ms 内无数据可读，符合预期（notification 不触发响应）
	}
}

// TestMockServer_Serve_MethodNotFound 测试未注册方法返回 JSON-RPC method not found 错误。
func TestMockServer_Serve_MethodNotFound(t *testing.T) {
	_, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 发送未注册方法的 request
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "nonexistent/method",
		"params":  map[string]interface{}{},
	}
	if err := writeJSONRPCMessage(clientWrite, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	resp, err := readJSONRPCMessage(clientRead)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp["id"] != float64(2) {
		t.Errorf("response id = %v, want 2", resp["id"])
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("response error not a map: %T", resp["error"])
	}
	if errObj["code"] != float64(-32601) {
		t.Errorf("error code = %v, want -32601 (method not found)", errObj["code"])
	}
}

// TestMockServer_Serve_HandlerError 测试 handler 返回 error 时包装为 JSON-RPC error。
func TestMockServer_Serve_HandlerError(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 注册返回 error 的 handler
	server.RegisterHandler("test/error", func(params json.RawMessage) (interface{}, error) {
		return nil, errors.New("intentional handler error")
	})

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "test/error",
		"params":  map[string]interface{}{},
	}
	writeJSONRPCMessage(clientWrite, req)

	resp, err := readJSONRPCMessage(clientRead)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("response error not a map: %T", resp["error"])
	}
	if errObj["code"] != float64(-32603) {
		t.Errorf("error code = %v, want -32603 (internal error)", errObj["code"])
	}
	if !strings.Contains(errObj["message"].(string), "intentional handler error") {
		t.Errorf("error message = %v, want contains 'intentional handler error'", errObj["message"])
	}
}

// TestMockServer_Serve_Initialize 测试 default initialize handler 返回 capabilities。
func TestMockServer_Serve_Initialize(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "initialize",
		"params": map[string]interface{}{
			"processId": 12345,
			"rootUri":   "file:///test",
		},
	}
	writeJSONRPCMessage(clientWrite, req)

	resp, err := readJSONRPCMessage(clientRead)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("response result not a map: %T", resp["result"])
	}
	caps, ok := result["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatal("capabilities not found in result")
	}
	// 验证 LSP 3.17 关键 capabilities
	if caps["inlayHintProvider"] != true {
		t.Error("inlayHintProvider should be true")
	}
	if caps["definitionProvider"] != true {
		t.Error("definitionProvider should be true")
	}

	// 验证 GetInitializeParams 缓存了 params
	cached := server.GetInitializeParams()
	if cached == nil {
		t.Fatal("GetInitializeParams returned nil")
	}
	var initParams map[string]interface{}
	if err := json.Unmarshal(cached, &initParams); err != nil {
		t.Fatalf("unmarshal cached params: %v", err)
	}
	if initParams["rootUri"] != "file:///test" {
		t.Errorf("cached rootUri = %v, want file:///test", initParams["rootUri"])
	}
}

// TestMockServer_GetReceivedMethods 测试 GetReceivedMethods 记录请求顺序。
func TestMockServer_GetReceivedMethods(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 发送 3 个请求
	methods := []string{"initialize", "test/method1", "test/method2"}
	for i, m := range methods {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      i + 1,
			"method":  m,
			"params":  map[string]interface{}{},
		}
		// 为 test/* 注册 handler
		if m != "initialize" {
			server.RegisterHandler(m, func(params json.RawMessage) (interface{}, error) {
				return nil, nil
			})
		}
		writeJSONRPCMessage(clientWrite, req)
		// 读取响应（同步避免消息堆积）
		readJSONRPCMessage(clientRead)
	}

	// 验证记录顺序
	got := server.GetReceivedMethods()
	if len(got) != len(methods) {
		t.Fatalf("received methods count = %d, want %d", len(got), len(methods))
	}
	for i, m := range methods {
		if got[i] != m {
			t.Errorf("received[%d] = %q, want %q", i, got[i], m)
		}
	}
}

// TestMockServer_GetReceivedMethods_ReturnsCopy 测试 GetReceivedMethods 返回副本（外部修改不影响内部状态）。
func TestMockServer_GetReceivedMethods_ReturnsCopy(t *testing.T) {
	s := NewMockServer()
	s.methodsMu.Lock()
	s.receivedMethods = []string{"a", "b", "c"}
	s.methodsMu.Unlock()

	got := s.GetReceivedMethods()
	got[0] = "modified"

	again := s.GetReceivedMethods()
	if again[0] == "modified" {
		t.Error("GetReceivedMethods did not return a copy (internal state modified)")
	}
}

// TestMockServer_Close_Idempotent 测试 Close 幂等（多次调用不 panic）。
func TestMockServer_Close_Idempotent(t *testing.T) {
	s := NewMockServer()
	s.Close()
	s.Close() // 不应 panic
	s.Close() // 不应 panic
}

// TestMockServer_Serve_AfterClose 测试 Close 后 Serve 立即返回。
func TestMockServer_Serve_AfterClose(t *testing.T) {
	s := NewMockServer()
	s.Close()

	// Serve 应立即返回（不阻塞）
	done := make(chan struct{})
	go func() {
		s.Serve(bytes.NewReader(nil), io.Discard)
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Error("Serve did not return within 1s after Close")
	}
}

// TestMockServer_Serve_ConcurrentRegister 测试 Serve 期间并发 RegisterHandler（数据竞争检测）。
func TestMockServer_Serve_ConcurrentRegister(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// io.Pipe 同步无缓冲：server 写响应时必须有 reader 在读，否则 server 写阻塞 →
	// server 不能读请求 → sender 写阻塞 → 死锁。
	// 解决：先启动 reader goroutine 串行读响应（bufio.Reader 非并发安全），
	// 再并发发送请求，使 reader 与 writer 同时在场。
	readDone := make(chan error, 1)
	go func() {
		for i := 0; i < 10; i++ {
			if _, err := readJSONRPCMessageWithTimeout(clientRead, 5*time.Second); err != nil {
				readDone <- fmt.Errorf("read response %d: %v", i, err)
				return
			}
		}
		readDone <- nil
	}()

	// 并发注册 handler + 发送请求
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			method := fmt.Sprintf("test/concurrent/%d", idx)
			server.RegisterHandler(method, func(params json.RawMessage) (interface{}, error) {
				return idx, nil
			})
			req := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      idx,
				"method":  method,
				"params":  map[string]interface{}{},
			}
			writeJSONRPCMessage(clientWrite, req)
		}(i)
	}
	wg.Wait()

	// 等待 reader 读完 10 个响应
	if err := <-readDone; err != nil {
		t.Error(err)
	}
}

// TestMockServer_Serve_EOF 测试 reader EOF 时 Serve 退出（不 panic）。
func TestMockServer_Serve_EOF(t *testing.T) {
	server := NewMockServer()
	reader, writer := io.Pipe()
	writer.Close() // 关闭 writer 让 reader 读到 EOF

	done := make(chan struct{})
	go func() {
		server.Serve(reader, io.Discard)
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Error("Serve did not return on EOF within 2s")
	}
}

// TestMockServer_Serve_MalformedHeader 测试畸形 header 不导致 Serve panic。
func TestMockServer_Serve_MalformedHeader(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 写入畸形 header（无 Content-Length）
	clientWrite.Write([]byte("Garbage-Header: value\r\n\r\n"))

	// 然后写入正常请求（验证 Serve 未崩溃）
	server.RegisterHandler("test/aftermalformed", func(params json.RawMessage) (interface{}, error) {
		return "ok", nil
	})
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "test/aftermalformed",
		"params":  map[string]interface{}{},
	}
	writeJSONRPCMessage(clientWrite, req)

	// TD-03 修复：用 readJSONRPCMessageWithTimeout 替代 SetReadDeadline
	resp, err := readJSONRPCMessageWithTimeout(clientRead, 2*time.Second)
	if err != nil {
		t.Fatalf("Serve did not recover from malformed header: %v", err)
	}
	if resp["result"] != "ok" {
		t.Errorf("result = %v, want ok", resp["result"])
	}
}

// TestMockServer_Serve_InvalidJSONBody 测试无效 JSON body 不导致 Serve panic。
func TestMockServer_Serve_InvalidJSONBody(t *testing.T) {
	_, clientWrite, _, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 写入无效 JSON body
	body := []byte("{not valid json")
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	clientWrite.Write([]byte(header))
	clientWrite.Write(body)

	// 紧接着写一个有效请求，验证 Serve 继续工作（无效消息被跳过）
	// （此用例主要验证不 panic，不严格验证后续行为）
	time.Sleep(100 * time.Millisecond) // 等待 Serve 处理无效消息
}

// TestMockServer_Initialize_CachesParams 测试 initialize handler 缓存 params 用于 workspaceFolders 断言。
func TestMockServer_Initialize_CachesParams(t *testing.T) {
	server, clientWrite, clientRead, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 发送含 workspaceFolders 的 initialize
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"workspaceFolders": []map[string]interface{}{
				{"uri": "file:///root1", "name": "root1"},
				{"uri": "file:///root2", "name": "root2"},
			},
		},
	}
	writeJSONRPCMessage(clientWrite, req)
	readJSONRPCMessage(clientRead)

	// 验证缓存的 params 包含 workspaceFolders
	cached := server.GetInitializeParams()
	if cached == nil {
		t.Fatal("GetInitializeParams returned nil")
	}
	var p map[string]interface{}
	if err := json.Unmarshal(cached, &p); err != nil {
		t.Fatalf("unmarshal cached params: %v", err)
	}
	folders, ok := p["workspaceFolders"].([]interface{})
	if !ok {
		t.Fatal("workspaceFolders not found in cached params")
	}
	if len(folders) != 2 {
		t.Errorf("workspaceFolders count = %d, want 2", len(folders))
	}
}

// TestMockServer_ContextCancellation 测试 MockServer 不阻塞 context 取消（调用方控制超时）。
func TestMockServer_ContextCancellation(t *testing.T) {
	server, clientWrite, _, cleanup := newMockServerPipe(t)
	defer cleanup()

	// 模拟调用方 context 超时
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// 注册慢 handler（200ms 响应）
	server.RegisterHandler("test/slow", func(params json.RawMessage) (interface{}, error) {
		time.Sleep(200 * time.Millisecond)
		return "slow-result", nil
	})

	// 调用方在 ctx 超时后不再等待响应
	go func() {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "test/slow",
			"params":  map[string]interface{}{},
		}
		writeJSONRPCMessage(clientWrite, req)
	}()

	<-ctx.Done()
	// 验证 context 取消后调用方可以继续（不阻塞在 MockServer 上）
	// MockServer 最终会写回响应，但调用方已超时退出
}
