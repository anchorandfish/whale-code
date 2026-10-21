// Package tui — T1-P1-A1 Bridge 重连异步化 TUI 层测试
//
// 验证 OnReconnectComplete 回调注册后发 reconnectStatus JSON-RPC 通知给 IDE：
//   - 成功时 success=true，无 error 字段
//   - 失败时 success=false，error 字段含脱敏后的错误信息
package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/bridge"
)

// TestBridgeReconnectStatusNotification_Failure 验证重连失败时发 reconnectStatus 通知（success=false + error）。
//
// 测试路径：
//   - newTestBridgeRunner 创建 runner（authProvider 未初始化）
//   - 手动初始化 authProvider + reconnectMgr（模拟 NewBridgeRunner 接线）
//   - 注册 OnReconnectComplete 回调（与 NewBridgeRunner 一致）
//   - 触发异步重连（refreshFn 总是失败）
//   - 验证 buffer 中有 reconnectStatus 通知 + success=false + error 字段
//
// 注意：SetReconnectManager 会自动覆盖 refreshFn 委托到 scheduler.refreshToken，
// 故在 SetReconnectManager 之后需重新注入测试用 refreshFn 以控制成功/失败。
func TestBridgeReconnectStatusNotification_Failure(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	// 手动初始化 authProvider + reconnectMgr（模拟 NewBridgeRunner 相关逻辑）
	runner.authProvider = bridge.NewAuthProvider(bridge.DefaultAuthProviderConfig())
	reconnectMgr := bridge.NewReconnectManager(bridge.ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  2,
	}, nil)
	runner.authProvider.SetReconnectManager(reconnectMgr)
	// SetReconnectManager 已将 refreshFn 委托到 scheduler.refreshToken（默认返回 error），
	// 此处显式重新注入总是失败的 refreshFn 以确保测试确定性
	reconnectMgr.SetRefreshToken(func() (string, error) {
		return "", fmt.Errorf("refresh failed")
	})

	// 注册回调（与 NewBridgeRunner 一致，但不用 secret.RedactSensitive 简化测试）
	callbackFired := make(chan struct{}, 1)
	runner.authProvider.SetOnReconnectComplete(func(success bool, err error) {
		notif := map[string]interface{}{"success": success}
		if err != nil {
			notif["error"] = err.Error()
		}
		runner.sendNotification("reconnectStatus", notif)
		callbackFired <- struct{}{}
	})

	// 触发异步重连
	ctx, ok := reconnectMgr.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := reconnectMgr.ReconnectOnAuthErrorAsync(ctx)
	<-resultCh

	// 等待回调触发
	select {
	case <-callbackFired:
	case <-time.After(2 * time.Second):
		t.Fatal("OnReconnectComplete callback did not fire within 2s")
	}

	// 验证 buffer 中有 reconnectStatus 通知
	msgs := parseJSONRPCMessages(buf)
	notifs := findNotifications(msgs, "reconnectStatus")
	if len(notifs) == 0 {
		t.Fatal("should send reconnectStatus notification")
	}
	params, ok := notifs[0]["params"].(map[string]interface{})
	if !ok {
		t.Fatal("reconnectStatus params should be an object")
	}
	if params["success"] != false {
		t.Errorf("expected success=false, got %v", params["success"])
	}
	if params["error"] == nil {
		t.Error("expected error field to be present on failure")
	}
}

// TestBridgeReconnectStatusNotification_Success 验证重连成功时发 reconnectStatus 通知（success=true，无 error）。
func TestBridgeReconnectStatusNotification_Success(t *testing.T) {
	runner, buf := newTestBridgeRunner()

	runner.authProvider = bridge.NewAuthProvider(bridge.DefaultAuthProviderConfig())
	reconnectMgr := bridge.NewReconnectManager(bridge.ReconnectConfig{
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		Factor:       2.0,
		JitterFactor: 0.0,
		MaxAttempts:  2,
	}, nil)
	runner.authProvider.SetReconnectManager(reconnectMgr)
	// SetReconnectManager 已将 refreshFn 委托到 scheduler.refreshToken（默认返回 error），
	// 此处显式重新注入总是成功的 refreshFn 以验证成功路径
	reconnectMgr.SetRefreshToken(func() (string, error) {
		return "new-valid-token", nil
	})

	callbackFired := make(chan struct{}, 1)
	runner.authProvider.SetOnReconnectComplete(func(success bool, err error) {
		notif := map[string]interface{}{"success": success}
		if err != nil {
			notif["error"] = err.Error()
		}
		runner.sendNotification("reconnectStatus", notif)
		callbackFired <- struct{}{}
	})

	ctx, ok := reconnectMgr.TryStartAsyncReconnect()
	if !ok {
		t.Fatal("TryStartAsyncReconnect should succeed")
	}
	resultCh := reconnectMgr.ReconnectOnAuthErrorAsync(ctx)
	<-resultCh

	select {
	case <-callbackFired:
	case <-time.After(2 * time.Second):
		t.Fatal("OnReconnectComplete callback did not fire within 2s")
	}

	msgs := parseJSONRPCMessages(buf)
	notifs := findNotifications(msgs, "reconnectStatus")
	if len(notifs) == 0 {
		t.Fatal("should send reconnectStatus notification")
	}
	params, ok := notifs[0]["params"].(map[string]interface{})
	if !ok {
		t.Fatal("reconnectStatus params should be an object")
	}
	if params["success"] != true {
		t.Errorf("expected success=true, got %v", params["success"])
	}
	if _, hasError := params["error"]; hasError {
		t.Error("expected no error field on success")
	}
}
