package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockWSServer 创建mock WebSocket MCP server
func mockWSServer() *httptest.Server {
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	var mu sync.Mutex
	tools := []map[string]interface{}{
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
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var req struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(message, &req); err != nil {
				continue
			}

			if req.ID > 0 {
				var result interface{}
				switch req.Method {
				case "initialize":
					result = map[string]interface{}{
						"protocolVersion": "2024-11-05",
						"capabilities": map[string]interface{}{
							"tools":     map[string]interface{}{},
							"resources": map[string]interface{}{},
							"prompts":   map[string]interface{}{},
						},
						"serverInfo": map[string]string{
							"name":    "mock-ws-server",
							"version": "1.0.0",
						},
					}
				case "ping":
					result = map[string]interface{}{}
				case "tools/list":
					mu.Lock()
					result = map[string]interface{}{"tools": tools}
					mu.Unlock()
				case "tools/call":
					var input struct {
						Name      string                 `json:"name"`
						Arguments map[string]interface{} `json:"arguments"`
					}
					json.Unmarshal(req.Params, &input)
					if input.Name == "echo" {
						msg, _ := input.Arguments["message"].(string)
						result = map[string]interface{}{
							"content": []map[string]interface{}{
								{"type": "text", "text": msg},
							},
						}
					} else {
						result = map[string]interface{}{
							"content": []map[string]interface{}{
								{"type": "text", "text": "unknown tool"},
							},
							"isError": true,
						}
					}
				default:
					result = map[string]interface{}{}
				}

				resultJSON, _ := json.Marshal(result)
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, string(resultJSON))
				conn.WriteMessage(websocket.TextMessage, []byte(resp))
			}
		}
	})

	return httptest.NewServer(handler)
}

// wsURL 将 http URL 转换为 ws URL
func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

// TestWebSocketTransport_Connection 测试 WebSocket 传输连接建立
func TestWebSocketTransport_Connection(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	if !transport.IsConnected() {
		t.Error("连接建立后应为已连接状态")
	}
}

// TestWebSocketTransport_SendAndReceive 测试 WebSocket 传输发送和接收消息
func TestWebSocketTransport_SendAndReceive(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 测试 initialize
	result, err := transport.Send(ctx, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	var initResp struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &initResp); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}

	if initResp.ProtocolVersion != "2024-11-05" {
		t.Errorf("ProtocolVersion = %s, expected 2024-11-05", initResp.ProtocolVersion)
	}
}

// TestWebSocketTransport_SendNotification 测试 WebSocket 传输发送通知
func TestWebSocketTransport_SendNotification(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = transport.SendNotification(ctx, "notifications/initialized", map[string]interface{}{})
	if err != nil {
		t.Fatalf("SendNotification 失败: %v", err)
	}
}

// TestWebSocketTransport_MultipleRequests 测试 WebSocket 传输多次请求
func TestWebSocketTransport_MultipleRequests(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 发送多个请求
	for i := int64(1); i <= 5; i++ {
		result, err := transport.Send(ctx, i, "ping", map[string]interface{}{})
		if err != nil {
			t.Fatalf("第 %d 次请求失败: %v", i, err)
		}
		if result == nil {
			t.Errorf("第 %d 次请求结果为 nil", i)
		}
	}
}

// TestWebSocketTransport_NotificationDelivery 测试 WebSocket 传输通知传递
func TestWebSocketTransport_NotificationDelivery(t *testing.T) {
	notifReceived := make(chan struct {
		method string
		params json.RawMessage
	}, 1)

	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin:  func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 发送一个通知
		notifParams, _ := json.Marshal(map[string]interface{}{"progress": 50})
		notif := fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":%s}`, string(notifParams))
		conn.WriteMessage(websocket.TextMessage, []byte(notif))

		// 继续处理请求
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			json.Unmarshal(message, &req)
			if req.ID > 0 {
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
				conn.WriteMessage(websocket.TextMessage, []byte(resp))
			}
		}
	}))
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	transport.SetNotificationHandler(func(method string, params json.RawMessage) {
		notifReceived <- struct {
			method string
			params json.RawMessage
		}{method: method, params: params}
	})

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

// TestWebSocketTransport_Close 测试 WebSocket 传输关闭
func TestWebSocketTransport_Close(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}

	if err := transport.Close(); err != nil {
		t.Errorf("Close 返回错误: %v", err)
	}

	if transport.IsConnected() {
		t.Error("关闭后不应为已连接状态")
	}
}

// TestWebSocketTransport_InvalidURL 测试无效 URL
func TestWebSocketTransport_InvalidURL(t *testing.T) {
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
			_, err := NewWebSocketTransport(context.Background(), tt.url, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewWebSocketTransport() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestWebSocketTransport_Subprotocol 测试 WebSocket 子协议协商
func TestWebSocketTransport_Subprotocol(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	// 验证连接使用了 mcp 子协议
	subprotocol := transport.Subprotocol()
	if subprotocol != "mcp" {
		t.Errorf("子协议 = %s, expected mcp", subprotocol)
	}
}

// TestWebSocketTransport_ContextCancellation test context取消
func TestWebSocketTransport_ContextCancellation(t *testing.T) {
	server := mockWSServer()
	defer server.Close()

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = transport.Send(ctx, 1, "ping", nil)
	if err == nil {
		t.Error("取消的上下文should return error")
	}
}

// TestWebSocketTransport_CustomHeaders 测试 WebSocket 传输的自定义request header
func TestWebSocketTransport_CustomHeaders(t *testing.T) {
	var receivedAuth string

	upgrader := websocket.Upgrader{
		Subprotocols: []string{"mcp"},
		CheckOrigin:  func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer server.Close()

	headers := map[string]string{
		"Authorization": "Bearer test-token",
	}

	transport, err := NewWebSocketTransport(context.Background(), wsURL(server.URL), headers)
	if err != nil {
		t.Fatalf("NewWebSocketTransport 失败: %v", err)
	}
	defer transport.Close()

	if receivedAuth != "Bearer test-token" {
		t.Errorf("Authorization 头 = %s, expected Bearer test-token", receivedAuth)
	}
}
