// Package mcp — elicitation 请求路由与响应测试（P0-7 / 第2轮修复）
//
// 本文件覆盖 elicitation 核心路径：
//   - TestElicitationRequestRouter_RegisterResolve：register + resolve 正常路径
//   - TestElicitationRequestRouter_ResolveNotFound：resolve 未知 reqID 返回 false
//   - TestElicitationRequestRouter_Unregister：unregister 后 resolve 返回 false
//   - TestElicitationRequestRouter_ChannelFull：channel 满时 resolve 返回 false
//   - TestDispatchRequest_NilHandler_Decline：无 handler 时 fail-safe 返回 decline
//   - TestDispatchRequest_NilRouter_Decline：router 为 nil 时 fail-safe 返回 decline
//   - TestDispatchRequest_UnsupportedMethod：不支持的方法返回 error
//   - TestDispatchRequest_HappyPath_ElicitationPending：正常路径返回 errElicitationPending
//   - TestDispatchRequest_NumberReqID：number 类型 reqID 也能正常路由
//   - TestResolveElicitation_NotFound：未知 reqID 返回 error
//   - TestSetElicitationHandler：SetElicitationHandler 设置/清除 handler
//
// 对标同类 CLI ElicitationRequestManager（elicitation.rs）。
package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestElicitationRequestRouter_RegisterResolve 验证 register + resolve 正常路径。
// resolve 后 pending 条目应被清理，再次 resolve 同一 reqID 返回 false。
func TestElicitationRequestRouter_RegisterResolve(t *testing.T) {
	r := NewElicitationRequestRouter()
	ch := make(chan ElicitationResponse, 1)
	r.register("req-1", ch)

	resp := ElicitationResponse{Action: ElicitationActionAccept}
	if !r.resolve("req-1", resp) {
		t.Fatal("resolve should return true for registered reqID")
	}

	// resolve 后 pending 应被清理（对标同类 CLI router.requests.remove）
	if r.resolve("req-1", resp) {
		t.Fatal("resolve should return false after already resolved (cleaned up)")
	}

	// channel 应收到响应
	select {
	case got := <-ch:
		if got.Action != ElicitationActionAccept {
			t.Errorf("expected action %s, got %s", ElicitationActionAccept, got.Action)
		}
	default:
		t.Fatal("channel should receive response")
	}
}

// TestElicitationRequestRouter_ResolveNotFound 验证 resolve 未知 reqID 返回 false。
func TestElicitationRequestRouter_ResolveNotFound(t *testing.T) {
	r := NewElicitationRequestRouter()
	if r.resolve("unknown", ElicitationResponse{}) {
		t.Fatal("resolve should return false for unknown reqID")
	}
}

// TestElicitationRequestRouter_Unregister 验证 unregister 后 resolve 返回 false。
func TestElicitationRequestRouter_Unregister(t *testing.T) {
	r := NewElicitationRequestRouter()
	ch := make(chan ElicitationResponse, 1)
	r.register("req-2", ch)
	r.unregister("req-2")

	if r.resolve("req-2", ElicitationResponse{}) {
		t.Fatal("resolve should return false after unregister")
	}
}

// TestElicitationRequestRouter_ChannelFull 验证 channel 满时 resolve 返回 false。
// 对标同类 CLI select! default 分支
func TestElicitationRequestRouter_ChannelFull(t *testing.T) {
	r := NewElicitationRequestRouter()
	// buffer=1 的 channel，先填满
	ch := make(chan ElicitationResponse, 1)
	ch <- ElicitationResponse{Action: ElicitationActionDecline}
	r.register("req-full", ch)

	// channel 已满，resolve 应走 default 分支返回 false
	if r.resolve("req-full", ElicitationResponse{Action: ElicitationActionAccept}) {
		t.Fatal("resolve should return false when channel is full")
	}

	// pending 条目应已被 delete（resolve 先 delete 再 select）
	r.mu.Lock()
	_, exists := r.pending["req-full"]
	r.mu.Unlock()
	if exists {
		t.Fatal("pending entry should be deleted even if channel full")
	}
}

// TestDispatchRequest_NilHandler_Decline 验证无 handler 时返回 decline（fail-safe）。
// 对标同类 CLI 无 sender 时自动 decline 的行为。
func TestDispatchRequest_NilHandler_Decline(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	c.elicitationRouter = NewElicitationRequestRouter()
	// 不设置 handler（nil）

	result, err := c.dispatchRequest("elicitation/create",
		json.RawMessage(`{"message":"hi"}`),
		json.RawMessage(`"req-1"`))
	if err != nil {
		t.Fatalf("dispatchRequest should not return error for nil handler, got: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result should be map[string]interface{}, got %T", result)
	}
	if m["action"] != string(ElicitationActionDecline) {
		t.Errorf("expected action %s, got %v", ElicitationActionDecline, m["action"])
	}
}

// TestDispatchRequest_NilRouter_Decline 验证 router 为 nil 时返回 decline（fail-safe）。
// 覆盖第3轮修复的 nil 保护路径（测试中未初始化 elicitationRouter 的场景）。
func TestDispatchRequest_NilRouter_Decline(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	// 不初始化 router（nil）
	c.SetElicitationHandler(func(serverName string, requestID json.RawMessage, message string) {})

	result, err := c.dispatchRequest("elicitation/create",
		json.RawMessage(`{"message":"hi"}`),
		json.RawMessage(`"req-1"`))
	if err != nil {
		t.Fatalf("dispatchRequest should not return error for nil router, got: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result should be map[string]interface{}, got %T", result)
	}
	if m["action"] != string(ElicitationActionDecline) {
		t.Errorf("expected action %s, got %v", ElicitationActionDecline, m["action"])
	}
}

// TestDispatchRequest_UnsupportedMethod 验证不支持的方法返回 error。
func TestDispatchRequest_UnsupportedMethod(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	c.elicitationRouter = NewElicitationRequestRouter()

	_, err := c.dispatchRequest("unknown/method", nil, json.RawMessage(`"req-1"`))
	if err == nil {
		t.Fatal("dispatchRequest should return error for unsupported method")
	}
	if !strings.Contains(err.Error(), "unsupported server request") {
		t.Errorf("error should contain 'unsupported server request', got: %v", err)
	}
}

// TestDispatchRequest_HappyPath_ElicitationPending 验证正常路径返回 errElicitationPending。
// handler 被调用，且 router 中注册了 pending 条目。
// 测试结束后通过 ResolveElicitation 清理 goroutine，避免泄漏。
func TestDispatchRequest_HappyPath_ElicitationPending(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	c.elicitationRouter = NewElicitationRequestRouter()

	handlerCalled := false
	var gotServerName string
	var gotMessage string
	c.SetElicitationHandler(func(serverName string, requestID json.RawMessage, message string) {
		handlerCalled = true
		gotServerName = serverName
		gotMessage = message
	})

	_, err := c.dispatchRequest("elicitation/create",
		json.RawMessage(`{"message":"please provide info"}`),
		json.RawMessage(`"req-1"`))
	if !errors.Is(err, errElicitationPending) {
		t.Fatalf("expected errElicitationPending, got: %v", err)
	}
	if !handlerCalled {
		t.Fatal("handler should have been called")
	}
	if gotMessage != "please provide info" {
		t.Errorf("expected message %q, got %q", "please provide info", gotMessage)
	}

	// 解析 elicitation 以清理 goroutine（避免泄漏）
	err = c.ResolveElicitation(ElicitationResponse{
		ServerName: gotServerName,
		RequestID:  json.RawMessage(`"req-1"`),
		Action:     ElicitationActionAccept,
	})
	if err != nil {
		t.Fatalf("ResolveElicitation failed: %v", err)
	}

	// 等待 goroutine 退出（awaitElicitationResponse 接收 respCh 后调用 sendElicitationResponse）
	time.Sleep(50 * time.Millisecond)
}

// TestDispatchRequest_NumberReqID 验证 number 类型 reqID 也能正常路由。
// 对标同类 CLI RequestId enum 双类型（String | Integer）。
func TestDispatchRequest_NumberReqID(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	c.elicitationRouter = NewElicitationRequestRouter()

	handlerCalled := false
	c.SetElicitationHandler(func(serverName string, requestID json.RawMessage, message string) {
		handlerCalled = true
		// 验证 reqID 原样透传（number 类型）
		if string(requestID) != "42" {
			t.Errorf("expected reqID %q, got %q", "42", string(requestID))
		}
	})

	// number 类型的 reqID（无引号）
	_, err := c.dispatchRequest("elicitation/create",
		json.RawMessage(`{"message":"test"}`),
		json.RawMessage(`42`))
	if !errors.Is(err, errElicitationPending) {
		t.Fatalf("expected errElicitationPending, got: %v", err)
	}
	if !handlerCalled {
		t.Fatal("handler should have been called for number reqID")
	}

	// 清理 goroutine
	c.ResolveElicitation(ElicitationResponse{
		RequestID: json.RawMessage(`42`),
		Action:    ElicitationActionDecline,
	})
	time.Sleep(50 * time.Millisecond)
}

// TestResolveElicitation_NotFound 验证 ResolveElicitation 未知 reqID 返回 error。
func TestResolveElicitation_NotFound(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	c.elicitationRouter = NewElicitationRequestRouter()

	err := c.ResolveElicitation(ElicitationResponse{
		RequestID: json.RawMessage(`"unknown"`),
		Action:    ElicitationActionAccept,
	})
	if err == nil {
		t.Fatal("ResolveElicitation should return error for unknown reqID")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %v", err)
	}
}

// TestSetElicitationHandler 验证 SetElicitationHandler 正确设置和清除 handler。
func TestSetElicitationHandler(t *testing.T) {
	c := newClientWithTransport(&nonStreamingTransport{})
	c.elicitationRouter = NewElicitationRequestRouter()

	// 初始 handler 为 nil
	c.elicitationMu.Lock()
	h := c.elicitationHandler
	c.elicitationMu.Unlock()
	if h != nil {
		t.Fatal("initial handler should be nil")
	}

	// 设置 handler
	called := false
	c.SetElicitationHandler(func(serverName string, requestID json.RawMessage, message string) {
		called = true
	})

	// 验证 handler 被设置
	c.elicitationMu.Lock()
	h = c.elicitationHandler
	c.elicitationMu.Unlock()
	if h == nil {
		t.Fatal("handler should not be nil after SetElicitationHandler")
	}

	// 调用 dispatchRequest 验证 handler 被触发
	c.dispatchRequest("elicitation/create",
		json.RawMessage(`{"message":"test"}`),
		json.RawMessage(`"req-1"`))
	if !called {
		t.Fatal("handler should have been called")
	}

	// 清理 goroutine
	c.ResolveElicitation(ElicitationResponse{
		RequestID: json.RawMessage(`"req-1"`),
		Action:    ElicitationActionAccept,
	})
	time.Sleep(50 * time.Millisecond)

	// 清除 handler
	c.SetElicitationHandler(nil)
	c.elicitationMu.Lock()
	h = c.elicitationHandler
	c.elicitationMu.Unlock()
	if h != nil {
		t.Fatal("handler should be nil after SetElicitationHandler(nil)")
	}
}
