package mcp

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// MockTransport 用于测试的mock传输implementation
type MockTransport struct {
	sendFunc        func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error)
	sendNotifFunc   func(ctx context.Context, method string, params interface{}) error
	notifHandler    NotificationHandler
	closed          atomic.Bool
	setHandlerCalls int
	// P0-7：elicitation 相关字段
	requestHandler   RequestHandler
	sendResponseFunc func(reqID json.RawMessage, result interface{}) error
}

// Send mock发送 JSON-RPC 请求
func (m *MockTransport) Send(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, id, method, params)
	}
	result, _ := json.Marshal(map[string]interface{}{})
	return result, nil
}

// SendNotification mock发送 JSON-RPC 通知
func (m *MockTransport) SendNotification(ctx context.Context, method string, params interface{}) error {
	if m.sendNotifFunc != nil {
		return m.sendNotifFunc(ctx, method, params)
	}
	return nil
}

// SetNotificationHandler 设置通知回调
func (m *MockTransport) SetNotificationHandler(handler NotificationHandler) {
	m.notifHandler = handler
	m.setHandlerCalls++
}

// Close mock关闭传输连接
func (m *MockTransport) Close() error {
	m.closed.Store(true)
	return nil
}

// SendResponse mock 发送 JSON-RPC 响应（P0-7 / 第3轮修复：Transport 接口扩展）
// 若 sendResponseFunc 已注入则调用，否则返回 nil（默认成功）
func (m *MockTransport) SendResponse(reqID json.RawMessage, result interface{}) error {
	if m.sendResponseFunc != nil {
		return m.sendResponseFunc(reqID, result)
	}
	return nil
}

// SetRequestHandler mock 注册 server-side 请求回调（P0-7 / 第3轮修复：Transport 接口扩展）
func (m *MockTransport) SetRequestHandler(handler RequestHandler) {
	m.requestHandler = handler
}

// TestTransportInterface 测试 Transport interface的基本contract
func TestTransportInterface(t *testing.T) {
	// 验证 MockTransport implementation了 Transport interface
	var _ Transport = &MockTransport{}

	t.Run("Send 返回有效 JSON", func(t *testing.T) {
		mock := &MockTransport{}
		ctx := context.Background()

		result, err := mock.Send(ctx, 1, "ping", nil)
		if err != nil {
			t.Fatalf("Send 返回错误: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("结果不是有效 JSON: %v", err)
		}
	})

	t.Run("Send 支持自定义响应", func(t *testing.T) {
		expectedResult := map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
		}
		expectedJSON, _ := json.Marshal(expectedResult)

		mock := &MockTransport{
			sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
				return expectedJSON, nil
			},
		}

		result, err := mock.Send(context.Background(), 1, "initialize", nil)
		if err != nil {
			t.Fatalf("Send 返回错误: %v", err)
		}

		if string(result) != string(expectedJSON) {
			t.Errorf("Send 结果 = %s, expected %s", string(result), string(expectedJSON))
		}
	})

	t.Run("Send 支持错误返回", func(t *testing.T) {
		mock := &MockTransport{
			sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
				return nil, &RPCError{Code: -32600, Message: "Invalid Request"}
			},
		}

		_, err := mock.Send(context.Background(), 1, "invalid", nil)
		if err == nil {
			t.Error("expected返回错误，但得到 nil")
		}

		rpcErr, ok := err.(*RPCError)
		if !ok {
			t.Errorf("错误类型应为 *RPCError，actual为 %T", err)
		}
		if rpcErr.Code != -32600 {
			t.Errorf("错误码 = %d, expected -32600", rpcErr.Code)
		}
	})

	t.Run("SendNotification 正常工作", func(t *testing.T) {
		mock := &MockTransport{}
		ctx := context.Background()

		err := mock.SendNotification(ctx, "notifications/initialized", map[string]interface{}{})
		if err != nil {
			t.Errorf("SendNotification 返回错误: %v", err)
		}
	})

	t.Run("SetNotificationHandler 被调用", func(t *testing.T) {
		mock := &MockTransport{}
		handler := func(method string, params json.RawMessage) {}

		mock.SetNotificationHandler(handler)

		if mock.setHandlerCalls != 1 {
			t.Errorf("SetNotificationHandler 调用次数 = %d, expected 1", mock.setHandlerCalls)
		}
		if mock.notifHandler == nil {
			t.Error("notifHandler 不should be nil")
		}
	})

	t.Run("Close 标记为已关闭", func(t *testing.T) {
		mock := &MockTransport{}

		if mock.closed.Load() {
			t.Error("初始状态不应为已关闭")
		}

		if err := mock.Close(); err != nil {
			t.Errorf("Close 返回错误: %v", err)
		}

		if !mock.closed.Load() {
			t.Error("Close 后应为已关闭状态")
		}
	})
}

// TestTransportContextCancellation 测试传输层对上下文取消的处理
func TestTransportContextCancellation(t *testing.T) {
	mock := &MockTransport{
		sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mock.Send(ctx, 1, "ping", nil)
	if err == nil {
		t.Error("取消的上下文should return error")
	}
	if err != context.Canceled {
		t.Errorf("错误应为 context.Canceled，actual为 %v", err)
	}
}

// TestTransportTimeout 测试传输层对超时的处理
func TestTransportTimeout(t *testing.T) {
	mock := &MockTransport{
		sendFunc: func(ctx context.Context, id int64, method string, params interface{}) (json.RawMessage, error) {
			select {
			case <-time.After(5 * time.Second):
				return json.RawMessage(`{}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := mock.Send(ctx, 1, "slow-method", nil)
	if err == nil {
		t.Error("超时should return error")
	}
}

// TestTransportNotificationDelivery 测试通知从传输层传递到处理器
func TestTransportNotificationDelivery(t *testing.T) {
	received := make(chan struct {
		method string
		params json.RawMessage
	}, 1)

	mock := &MockTransport{}
	mock.SetNotificationHandler(func(method string, params json.RawMessage) {
		received <- struct {
			method string
			params json.RawMessage
		}{method: method, params: params}
	})

	// mock传输层收到通知并调用处理器
	testParams, _ := json.Marshal(map[string]interface{}{"key": "value"})
	if mock.notifHandler != nil {
		mock.notifHandler("notifications/progress", testParams)
	}

	select {
	case msg := <-received:
		if msg.method != "notifications/progress" {
			t.Errorf("通知方法 = %s, expected notifications/progress", msg.method)
		}
	case <-time.After(time.Second):
		t.Error("未收到通知")
	}
}
