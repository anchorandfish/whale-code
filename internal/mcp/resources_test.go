// Package mcp — / M17-01 资源订阅测试
//
// 测试覆盖：
// - Subscribe：能力检查 / 幂等去重 / JSON-RPC 请求发送 / 错误返回
// - Unsubscribe：幂等返回 / best-effort 清理 / JSON-RPC 请求发送
// - SetResourceHandler + dispatchNotification：updated / list_changed 通知拦截与转发
// - handler=nil 静默丢弃通知
//
// 对标 MCP 规范 2025-06-18 §"Resources" resources/subscribe（访问日期 2026-07-21）。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// newTestClientWithCapabilities 构造一个带 MockTransport 和预设能力的测试 Client。
// 跳过 initialize()，直接设置 capabilities 字段，用于隔离测试资源订阅逻辑。
func newTestClientWithCapabilities(caps ServerCapabilities) (*Client, *MockTransport) {
	mock := &MockTransport{}
	c := &Client{
		name:              "test-server",
		trans:             mock,
		transportType:     "mock",
		capabilities:      caps,
		subscriptions:     make(map[string]bool),
		elicitationRouter: NewElicitationRequestRouter(),
	}
	mock.SetNotificationHandler(c.dispatchNotification)
	mock.SetRequestHandler(c.dispatchRequest)
	return c, mock
}

// TestSubscribe_NotSupported 测试 server 不支持 resources/subscribe 时返回 ErrSubscribeNotSupported。
func TestSubscribe_NotSupported(t *testing.T) {
	t.Run("capabilities.Resources=nil", func(t *testing.T) {
		c, _ := newTestClientWithCapabilities(ServerCapabilities{})
		err := c.Subscribe(context.Background(), "file:///test")
		if !errors.Is(err, ErrSubscribeNotSupported) {
			t.Errorf("Subscribe with nil Resources: got %v, want ErrSubscribeNotSupported", err)
		}
	})
	t.Run("capabilities.Resources.Subscribe=false", func(t *testing.T) {
		c, _ := newTestClientWithCapabilities(ServerCapabilities{
			Resources: &ResourceCapabilities{Subscribe: false},
		})
		err := c.Subscribe(context.Background(), "file:///test")
		if !errors.Is(err, ErrSubscribeNotSupported) {
			t.Errorf("Subscribe with Subscribe=false: got %v, want ErrSubscribeNotSupported", err)
		}
	})
}

// TestSubscribe_EmptyURI 测试空 URI 返回错误。
func TestSubscribe_EmptyURI(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{
		Resources: &ResourceCapabilities{Subscribe: true},
	})
	err := c.Subscribe(context.Background(), "")
	if err == nil {
		t.Error("Subscribe with empty URI should return error")
	}
}

// TestSubscribe_Success 测试成功订阅发送 resources/subscribe 请求并记录到 map。
func TestSubscribe_Success(t *testing.T) {
	var capturedMethod string
	var capturedParams map[string]interface{}
	mock := &MockTransport{
		sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
			capturedMethod = method
			capturedParams = params.(map[string]interface{})
			return json.RawMessage(`{}`), nil
		},
	}
	c := &Client{
		name:          "test-server",
		trans:         mock,
		transportType: "mock",
		capabilities: ServerCapabilities{
			Resources: &ResourceCapabilities{Subscribe: true},
		},
		subscriptions: make(map[string]bool),
	}
	mock.SetNotificationHandler(c.dispatchNotification)

	uri := "file:///test/resource.txt"
	err := c.Subscribe(context.Background(), uri)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if capturedMethod != "resources/subscribe" {
		t.Errorf("JSON-RPC method = %s, want resources/subscribe", capturedMethod)
	}
	if capturedParams["uri"] != uri {
		t.Errorf("JSON-RPC params uri = %v, want %s", capturedParams["uri"], uri)
	}
	// 验证已记录到 subscriptions map
	c.subMu.RLock()
	subscribed := c.subscriptions[uri]
	c.subMu.RUnlock()
	if !subscribed {
		t.Error("URI not recorded in subscriptions map")
	}
}

// TestSubscribe_Idempotent 测试重复订阅同一 URI 幂等返回 nil 且不重复发送请求。
func TestSubscribe_Idempotent(t *testing.T) {
	callCount := 0
	mock := &MockTransport{
		sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
			callCount++
			return json.RawMessage(`{}`), nil
		},
	}
	c := &Client{
		name:          "test-server",
		trans:         mock,
		transportType: "mock",
		capabilities: ServerCapabilities{
			Resources: &ResourceCapabilities{Subscribe: true},
		},
		subscriptions: make(map[string]bool),
	}
	mock.SetNotificationHandler(c.dispatchNotification)

	uri := "file:///test/idempotent.txt"
	// 第一次订阅
	if err := c.Subscribe(context.Background(), uri); err != nil {
		t.Fatalf("First Subscribe failed: %v", err)
	}
	// 第二次订阅（幂等）
	if err := c.Subscribe(context.Background(), uri); err != nil {
		t.Fatalf("Second Subscribe failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("JSON-RPC call count = %d, want 1 (idempotent)", callCount)
	}
}

// TestUnsubscribe_NotSubscribed 测试未订阅的 URI 幂等返回 nil。
func TestUnsubscribe_NotSubscribed(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{
		Resources: &ResourceCapabilities{Subscribe: true},
	})
	err := c.Unsubscribe(context.Background(), "file:///not/subscribed")
	if err != nil {
		t.Errorf("Unsubscribe not-subscribed URI: got %v, want nil", err)
	}
}

// TestUnsubscribe_EmptyURI 测试空 URI 返回错误。
func TestUnsubscribe_EmptyURI(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{
		Resources: &ResourceCapabilities{Subscribe: true},
	})
	err := c.Unsubscribe(context.Background(), "")
	if err == nil {
		t.Error("Unsubscribe with empty URI should return error")
	}
}

// TestUnsubscribe_Success 测试成功取消订阅发送 resources/unsubscribe 请求并从 map 移除。
func TestUnsubscribe_Success(t *testing.T) {
	var capturedMethod string
	mock := &MockTransport{
		sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
			capturedMethod = method
			return json.RawMessage(`{}`), nil
		},
	}
	c := &Client{
		name:          "test-server",
		trans:         mock,
		transportType: "mock",
		capabilities: ServerCapabilities{
			Resources: &ResourceCapabilities{Subscribe: true},
		},
		subscriptions: make(map[string]bool),
	}
	mock.SetNotificationHandler(c.dispatchNotification)

	uri := "file:///test/unsub.txt"
	// 先订阅
	if err := c.Subscribe(context.Background(), uri); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	// 取消订阅
	if err := c.Unsubscribe(context.Background(), uri); err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}
	if capturedMethod != "resources/unsubscribe" {
		t.Errorf("JSON-RPC method = %s, want resources/unsubscribe", capturedMethod)
	}
	// 验证已从 map 移除
	c.subMu.RLock()
	_, stillSubscribed := c.subscriptions[uri]
	c.subMu.RUnlock()
	if stillSubscribed {
		t.Error("URI still in subscriptions map after Unsubscribe")
	}
}

// TestUnsubscribe_BestEffort 测试 server 返回错误时仍从本地 map 移除（best-effort）。
func TestUnsubscribe_BestEffort(t *testing.T) {
	mock := &MockTransport{
		sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
			// resources/subscribe 返回成功，resources/unsubscribe 返回错误
			if method == "resources/unsubscribe" {
				return nil, &RPCError{Code: -32601, Message: "method not found"}
			}
			return json.RawMessage(`{}`), nil
		},
	}
	c := &Client{
		name:          "test-server",
		trans:         mock,
		transportType: "mock",
		capabilities: ServerCapabilities{
			Resources: &ResourceCapabilities{Subscribe: true},
		},
		subscriptions: make(map[string]bool),
	}
	mock.SetNotificationHandler(c.dispatchNotification)

	uri := "file:///test/besteffort.txt"
	// 先订阅
	if err := c.Subscribe(context.Background(), uri); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	// 取消订阅（server 返回错误）
	err := c.Unsubscribe(context.Background(), uri)
	// best-effort：返回错误但本地 map 已清理
	if err == nil {
		t.Error("Unsubscribe with server error should return non-nil error")
	}
	// 验证本地 map 已清理
	c.subMu.RLock()
	_, stillSubscribed := c.subscriptions[uri]
	c.subMu.RUnlock()
	if stillSubscribed {
		t.Error("URI should be removed from map (best-effort cleanup)")
	}
}

// TestSetResourceHandler_AndDispatch 测试 SetResourceHandler 注册回调后，
// dispatchNotification 拦截 resources/updated 和 resources/list_changed 通知并转发给 handler。
func TestSetResourceHandler_AndDispatch(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{
		Resources: &ResourceCapabilities{Subscribe: true},
	})

	var mu sync.Mutex
	var receivedNotifications []struct {
		method string
		uri    string
	}
	handler := func(method string, uri string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		receivedNotifications = append(receivedNotifications, struct {
			method string
			uri    string
		}{method: method, uri: uri})
	}
	c.SetResourceHandler(handler)

	// 模拟 server 推送 notifications/resources/updated
	updatedParams, _ := json.Marshal(map[string]string{"uri": "file:///test/updated.txt"})
	c.dispatchNotification("notifications/resources/updated", updatedParams)

	// 模拟 server 推送 notifications/resources/list_changed
	c.dispatchNotification("notifications/resources/list_changed", json.RawMessage(`{}`))

	mu.Lock()
	defer mu.Unlock()
	if len(receivedNotifications) != 2 {
		t.Fatalf("Received %d notifications, want 2", len(receivedNotifications))
	}
	if receivedNotifications[0].method != "notifications/resources/updated" {
		t.Errorf("Notification 0 method = %s, want notifications/resources/updated", receivedNotifications[0].method)
	}
	if receivedNotifications[0].uri != "file:///test/updated.txt" {
		t.Errorf("Notification 0 uri = %s, want file:///test/updated.txt", receivedNotifications[0].uri)
	}
	if receivedNotifications[1].method != "notifications/resources/list_changed" {
		t.Errorf("Notification 1 method = %s, want notifications/resources/list_changed", receivedNotifications[1].method)
	}
	if receivedNotifications[1].uri != "" {
		t.Errorf("Notification 1 uri = %s, want empty (list_changed has no uri)", receivedNotifications[1].uri)
	}
}

// TestSetResourceHandler_Nil 测试 handler=nil 时通知被静默丢弃。
func TestSetResourceHandler_Nil(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{
		Resources: &ResourceCapabilities{Subscribe: true},
	})
	// 不注册 handler（默认 nil）
	// 模拟 server 推送通知，不应 panic
	updatedParams, _ := json.Marshal(map[string]string{"uri": "file:///test/silent.txt"})
	c.dispatchNotification("notifications/resources/updated", updatedParams)
	c.dispatchNotification("notifications/resources/list_changed", json.RawMessage(`{}`))
	// 到这里没 panic 即通过
}

// TestSetResourceHandler_Overwrite 测试重复调用 SetResourceHandler 覆盖旧回调。
func TestSetResourceHandler_Overwrite(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{
		Resources: &ResourceCapabilities{Subscribe: true},
	})

	var callCount int
	var mu sync.Mutex
	oldHandler := func(method string, uri string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		callCount += 100 // 标记旧 handler
	}
	newHandler := func(method string, uri string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		callCount += 1 // 标记新 handler
	}
	c.SetResourceHandler(oldHandler)
	c.SetResourceHandler(newHandler) // 覆盖

	updatedParams, _ := json.Marshal(map[string]string{"uri": "file:///test/overwrite.txt"})
	c.dispatchNotification("notifications/resources/updated", updatedParams)

	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (only new handler should fire)", callCount)
	}
}

// TestDispatchNotification_OtherMethods 测试非资源通知不被资源 handler 拦截。
func TestDispatchNotification_OtherMethods(t *testing.T) {
	c, _ := newTestClientWithCapabilities(ServerCapabilities{})

	handlerCalled := false
	c.SetResourceHandler(func(method string, uri string, params json.RawMessage) {
		handlerCalled = true
	})
	// 推送非资源通知
	c.dispatchNotification("notifications/tools/list_changed", json.RawMessage(`{}`))
	c.dispatchNotification("notifications/progress", json.RawMessage(`{}`))
	if handlerCalled {
		t.Error("Resource handler should not be called for non-resource notifications")
	}
}
