//go:build windows

package signal

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestHandleSignal_InterruptGraceful_Windows 验证 Windows 上 HandleSignal(os.Interrupt) 触发优雅退出（B6-P1-4）。
// os.Interrupt 在 Windows 上等价于 syscall.SIGINT，应被 HandleSignal 处理并触发 cleanup。
func TestHandleSignal_InterruptGraceful_Windows(t *testing.T) {
	sh := NewGracefulShutdown()
	var cleanupCalled int32
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&cleanupCalled, 1)
		return nil
	})

	handled := sh.HandleSignal(os.Interrupt)

	if !handled {
		t.Fatal("os.Interrupt 应被处理（返回 true）")
	}
	if atomic.LoadInt32(&cleanupCalled) != 1 {
		t.Fatal("首次 os.Interrupt 应触发优雅 cleanup")
	}
}

// TestHandleSignal_InterruptForce_Windows 验证 Windows 上 2s 内第二次 os.Interrupt 触发强制退出（B6-P1-4）。
func TestHandleSignal_InterruptForce_Windows(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }

	// 首次：优雅
	sh.HandleSignal(os.Interrupt)
	// 立即二次：强制
	sh.HandleSignal(os.Interrupt)

	if got := atomic.LoadInt32(&exitCode); got != 130 {
		t.Fatalf("二次 os.Interrupt 应以 130 强制退出，实际 %d", got)
	}
}

// TestHandleSignal_SIGINTGraceful_Windows 验证 Windows 上 syscall.SIGINT 直接触发优雅退出（B6-P1-4）。
func TestHandleSignal_SIGINTGraceful_Windows(t *testing.T) {
	sh := NewGracefulShutdown()
	var cleanupCalled int32
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&cleanupCalled, 1)
		return nil
	})

	handled := sh.HandleSignal(syscall.SIGINT)

	if !handled {
		t.Fatal("SIGINT 应被处理")
	}
	if atomic.LoadInt32(&cleanupCalled) != 1 {
		t.Fatal("SIGINT 应触发优雅 cleanup")
	}
}

// TestHandleSignal_CancelInvoked_Windows 验证 Windows 上优雅退出时 cancel 被调用（B6-P1-4）。
func TestHandleSignal_CancelInvoked_Windows(t *testing.T) {
	sh := NewGracefulShutdown()
	ctx, cancel := context.WithCancel(context.Background())
	sh.RegisterCancel(cancel)

	select {
	case <-ctx.Done():
		t.Fatal("cancel 不应在 HandleSignal 前被调用")
	default:
	}

	sh.HandleSignal(os.Interrupt)

	select {
	case <-ctx.Done():
		// 预期：ctx 已被取消
	case <-time.After(time.Second):
		t.Fatal("HandleSignal 后 cancel 未被调用")
	}
}

// TestHandleSignal_NonTargetUnhandled_Windows 验证 Windows 上非目标信号返回未处理（B6-P1-4）。
func TestHandleSignal_NonTargetUnhandled_Windows(t *testing.T) {
	sh := NewGracefulShutdown()
	// Windows 上 syscall.Signal(99) 不是目标信号
	handled := sh.HandleSignal(syscall.Signal(99))
	if handled {
		t.Fatal("非目标信号应返回未处理")
	}
}
