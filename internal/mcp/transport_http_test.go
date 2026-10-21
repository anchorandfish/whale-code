package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newMockHTTPServer 创建mock Streamable HTTP MCP server
func newMockHTTPServer() *httptest.Server {
	var sessionID atomic.Int64
	sessionID.Store(1)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// 读取请求
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		// 设置 session ID（首次请求）
		if r.Header.Get("Mcp-Session-Id") == "" {
			sid := fmt.Sprintf("session-%d", sessionID.Add(1))
			w.Header().Set("Mcp-Session-Id", sid)
		}

		// 检查client是否发送了 session ID
		clientSessionID := r.Header.Get("Mcp-Session-Id")

		// 根据 Accept 头decided响应格式
		accept := r.Header.Get("Accept")

		result := handleMockHTTPRequest(req.Method, req.Params, clientSessionID)
		resultJSON, _ := json.Marshal(result)

		// 如果client接受 SSE，则返回 SSE 流
		if strings.Contains(accept, "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, string(resultJSON))
			w.Write([]byte(fmt.Sprintf("id: 1\nevent: message\ndata: %s\n\n", resp)))
			return
		}

		// 默认返回 JSON
		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, string(resultJSON))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	})

	return httptest.NewServer(mux)
}

// handleMockHTTPRequest 处理mock HTTP server的 JSON-RPC 请求
func handleMockHTTPRequest(method string, params json.RawMessage, sessionID string) interface{} {
	switch method {
	case "initialize":
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools":     map[string]interface{}{},
				"resources": map[string]interface{}{},
				"prompts":   map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "mock-http-server",
				"version": "1.0.0",
			},
		}
	case "ping":
		return map[string]interface{}{}
	case "tools/list":
		return map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "echo",
					"description": "Echo back the input",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"message": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		}
	case "tools/call":
		var input struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		json.Unmarshal(params, &input)
		if input.Name == "echo" {
			msg, _ := input.Arguments["message"].(string)
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": msg},
				},
			}
		}
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "unknown tool"},
			},
			"isError": true,
		}
	default:
		return map[string]interface{}{}
	}
}

// TestHTTPTransport_SessionIDManagement 测试 HTTP 传输的会话 ID 管理
func TestHTTPTransport_SessionIDManagement(t *testing.T) {
	server := newMockHTTPServer()
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	// 发送第一个请求，servershould return session ID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = transport.Send(ctx, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	// 验证 session ID 已被保存
	sid := transport.SessionID()
	if sid == "" {
		t.Error("发送请求后应保存 session ID")
	}
	t.Logf("Session ID: %s", sid)
}

// TestHTTPTransport_JSONDirectResponse 测试 HTTP 传输的 JSON 直接响应
func TestHTTPTransport_JSONDirectResponse(t *testing.T) {
	server := newMockHTTPServer()
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 发送请求，默认返回 JSON
	result, err := transport.Send(ctx, 1, "ping", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	if result == nil {
		t.Error("结果不should be nil")
	}
}

// TestHTTPTransport_SSEResponse 测试 HTTP 传输的 SSE 流响应
func TestHTTPTransport_SSEResponse(t *testing.T) {
	server := newMockHTTPServer()
	defer server.Close()

	// 创建接受 SSE 的 HTTP 传输
	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 发送请求
	result, err := transport.Send(ctx, 1, "tools/list", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	var resp struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}

	if len(resp.Tools) == 0 {
		t.Error("should return至少一个工具")
	}
}

// TestHTTPTransport_ErrorHandling 测试 HTTP 传输的错误处理
func TestHTTPTransport_ErrorHandling(t *testing.T) {
	t.Run("连接不存在的server", func(t *testing.T) {
		_, err := NewHTTPTransport(context.Background(), "http://127.0.0.1:1/mcp", nil)
		// NewHTTPTransport 本身不连接，但 Send 应该失败
		if err != nil {
			// 如果构造函数验证 URL，这是 OK 的
			return
		}
	})

	t.Run("server返回非 200 状态码", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal server error"))
		}))
		defer server.Close()

		transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
		if err != nil {
			t.Fatalf("NewHTTPTransport 失败: %v", err)
		}
		defer transport.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err = transport.Send(ctx, 1, "ping", nil)
		if err == nil {
			t.Error("server返回 500 should return error")
		}
	})

	t.Run("server返回 JSON-RPC 错误", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`))
		}))
		defer server.Close()

		transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
		if err != nil {
			t.Fatalf("NewHTTPTransport 失败: %v", err)
		}
		defer transport.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err = transport.Send(ctx, 1, "invalid", nil)
		if err == nil {
			t.Error("JSON-RPC 错误should return error")
		}
	})
}

// TestHTTPTransport_SendNotification 测试 HTTP 传输发送通知
func TestHTTPTransport_SendNotification(t *testing.T) {
	var receivedMethod string
	var _ json.RawMessage // 用于解码但不需要assert
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		receivedMethod = req.Method
		_ = req.Params

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = transport.SendNotification(ctx, "notifications/initialized", map[string]interface{}{})
	if err != nil {
		t.Fatalf("SendNotification 失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if receivedMethod != "notifications/initialized" {
		t.Errorf("收到方法 = %s, expected notifications/initialized", receivedMethod)
	}
}

// TestHTTPTransport_CustomHeaders 测试 HTTP 传输的自定义request header
func TestHTTPTransport_CustomHeaders(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()

	headers := map[string]string{
		"Authorization": "Bearer test-token",
	}

	transport, err := NewHTTPTransport(context.Background(), server.URL, headers)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = transport.Send(ctx, 1, "ping", nil)
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	if receivedAuth != "Bearer test-token" {
		t.Errorf("Authorization 头 = %s, expected Bearer test-token", receivedAuth)
	}
}

// TestHTTPTransport_LastEventID 测试 HTTP 传输的 Last-Event-ID 恢复
func TestHTTPTransport_LastEventID(t *testing.T) {
	var receivedLastEventID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedLastEventID = r.Header.Get("Last-Event-ID")

		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	// 设置 Last-Event-ID
	transport.SetLastEventID("event-42")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = transport.Send(ctx, 1, "ping", nil)
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	if receivedLastEventID != "event-42" {
		t.Errorf("Last-Event-ID = %s, expected event-42", receivedLastEventID)
	}
}

// TestHTTPTransport_SSEStreamParsing 测试 HTTP 传输解析 SSE 流
func TestHTTPTransport_SSEStreamParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		result := map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"serverInfo":      map[string]string{"name": "test", "version": "1.0"},
		}
		resultJSON, _ := json.Marshal(result)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// 发送多个 SSE 事件
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "id: %d\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":%s}\n\n", i, string(resultJSON))
			flusher.Flush()
		}
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := transport.Send(ctx, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	if result == nil {
		t.Error("结果不should be nil")
	}
}

// TestHTTPTransport_NotificationFromSSE 测试从 SSE 流接收通知
func TestHTTPTransport_NotificationFromSSE(t *testing.T) {
	notifReceived := make(chan struct {
		method string
		params json.RawMessage
	}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			// SSE 事件流
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")

			flusher, ok := w.(http.Flusher)
			if !ok {
				return
			}

			notifParams, _ := json.Marshal(map[string]interface{}{"progress": 50})
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":%s}\n\n", string(notifParams))
			flusher.Flush()
			return
		}

		// POST 请求
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	transport.SetNotificationHandler(func(method string, params json.RawMessage) {
		notifReceived <- struct {
			method string
			params json.RawMessage
		}{method: method, params: params}
	})

	// 启动 SSE 监听
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go transport.StartSSEListener(ctx)

	// 等待通知
	select {
	case msg := <-notifReceived:
		if msg.method != "notifications/progress" {
			t.Errorf("通知方法 = %s, expected notifications/progress", msg.method)
		}
	case <-time.After(3 * time.Second):
		t.Error("等待通知超时")
	}
}

// TestHTTPTransport_Close 测试 HTTP 传输关闭
func TestHTTPTransport_Close(t *testing.T) {
	server := newMockHTTPServer()
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}

	if err := transport.Close(); err != nil {
		t.Errorf("Close 返回错误: %v", err)
	}
}

// TestHTTPTransport_InvalidURL 测试无效 URL
func TestHTTPTransport_InvalidURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"空 URL", "", true},
		{"无效 URL", "not-a-url", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHTTPTransport(context.Background(), tt.url, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewHTTPTransport() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestHTTPTransport_ContextCancellation test context取消
func TestHTTPTransport_ContextCancellation(t *testing.T) {
	// 使用 channel 协调：server 端收到请求后通知，然后等待client取消
	requestReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 通知测试：请求已到达
		close(requestReceived)
		// 短暂等待client取消（client 100ms 超时，这里等 2s 足够）
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	// 不使用 defer server.Close()，因为 Close() 会等待活跃连接
	// 改为在测试结束后异步关闭
	defer func() { go server.Close() }()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = transport.Send(ctx, 1, "ping", nil)
	if err == nil {
		t.Error("超时should return error")
	}

	// 等待 server 端收到请求（确认请求确实发出了）
	select {
	case <-requestReceived:
		// 请求已到达 server，test passed
	case <-time.After(5 * time.Second):
		t.Error("请求未到达 server")
	}
}

// newMockSSEStreamServer 创建返回 SSE 流的mockserver
func newMockSSEStreamServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		if r.Method == "POST" {
			var req struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &req)

			result := handleMockHTTPRequest(req.Method, req.Params, "")
			resultJSON, _ := json.Marshal(result)

			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, string(resultJSON))
			fmt.Fprintf(w, "id: 1\nevent: message\ndata: %s\n\n", resp)
			flusher.Flush()
		}
	}))
}

// TestHTTPTransport_ReadSSEEvents 测试读取 SSE 事件
func TestHTTPTransport_ReadSSEEvents(t *testing.T) {
	// 使用 bufio.Scanner 测试 SSE 事件解析
	input := "id: 1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"
	scanner := bufio.NewScanner(strings.NewReader(input))

	eventData := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
		}
	}

	if eventData == "" {
		t.Error("应解析出 SSE 事件数据")
	}

	var msg struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(eventData), &msg); err != nil {
		t.Fatalf("解析 SSE 事件数据失败: %v", err)
	}

	if msg.ID != 1 {
		t.Errorf("事件 ID = %d, expected 1", msg.ID)
	}
}
