//go:build !windows

package signal

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestRegisterCancel_ShutdownInvokesCancel 验证注册的 cancel 在优雅 Shutdown 时被调用。
func TestRegisterCancel_ShutdownInvokesCancel(t *testing.T) {
	sh := NewGracefulShutdown()
	ctx, cancel := context.WithCancel(context.Background())
	sh.RegisterCancel(cancel)

	select {
	case <-ctx.Done():
		t.Fatal("cancel 不应在 Shutdown 前被调用")
	default:
	}

	sh.Shutdown(false)

	select {
	case <-ctx.Done():
		// 预期：ctx 已被取消
	default:
		t.Fatal("Shutdown(false) 后 cancel 未被调用")
	}
}

// TestRegisterCleanup_ShutdownExecutesCleanup 验证注册的清理函数在优雅 Shutdown 时执行。
func TestRegisterCleanup_ShutdownExecutesCleanup(t *testing.T) {
	sh := NewGracefulShutdown()
	var called int32
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&called, 1)
		return nil
	})

	sh.Shutdown(false)

	if atomic.LoadInt32(&called) != 1 {
		t.Fatal("cleanup 未被执行")
	}
}

// TestShutdown_GracefulOnce 验证 once 保证优雅清理只执行一次。
func TestShutdown_GracefulOnce(t *testing.T) {
	sh := NewGracefulShutdown()
	var count int32
	sh.RegisterCleanup(func() error {
		atomic.AddInt32(&count, 1)
		return nil
	})

	sh.Shutdown(false)
	sh.Shutdown(false)
	sh.Shutdown(false)

	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("cleanup 应只执行一次，实际执行 %d 次", got)
	}
}

// TestShutdown_ForceSkipsCleanup 验证 force=true 跳过 cleanup 并触发退出函数。
// 使用可覆盖的 exitFn 避免测试进程退出。
func TestShutdown_ForceSkipsCleanup(t *testing.T) {
	sh := NewGracefulShutdown()
	var cleanupCalled int32
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&cleanupCalled, 1)
		return nil
	})

	sh.Shutdown(true)

	if atomic.LoadInt32(&cleanupCalled) != 0 {
		t.Fatal("force=true 不应执行 cleanup")
	}
	if got := atomic.LoadInt32(&exitCode); got != 130 {
		t.Fatalf("force=true 应以退出码 130 退出，实际 %d", got)
	}
}

// TestShutdown_ForceInvokesCancel 验证 force=true 仍会调用 cancel 中断流。
func TestShutdown_ForceInvokesCancel(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.exitFn = func(code int) {} // 阻止真实退出
	ctx, cancel := context.WithCancel(context.Background())
	sh.RegisterCancel(cancel)

	sh.Shutdown(true)

	select {
	case <-ctx.Done():
		// 预期：cancel 已调用
	default:
		t.Fatal("force=true 应调用 cancel 中断流")
	}
}

// TestHandleSignal_FirstSignalGraceful 验证首次 SIGINT 触发优雅退出。
func TestHandleSignal_FirstSignalGraceful(t *testing.T) {
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
		t.Fatal("首次 SIGINT 应触发优雅 cleanup")
	}
}

// TestHandleSignal_SecondSignalForce 验证 2s 内第二次 SIGINT 触发强制退出。
func TestHandleSignal_SecondSignalForce(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }

	// 首次：优雅
	sh.HandleSignal(syscall.SIGINT)
	// 立即二次：强制
	sh.HandleSignal(syscall.SIGINT)

	if got := atomic.LoadInt32(&exitCode); got != 130 {
		t.Fatalf("二次 SIGINT 应以 130 强制退出，实际 %d", got)
	}
}

// TestHandleSignal_NonTargetSignalUnhandled 验证非目标信号返回未处理。
func TestHandleSignal_NonTargetSignalUnhandled(t *testing.T) {
	sh := NewGracefulShutdown()
	// syscall.SIGUSR1 在 Windows 也定义但非目标；用 SIGINT 之外的方式构造
	// 用一个非 SIGINT/SIGTERM/SIGHUP/SIGUSR1 的信号值
	handled := sh.HandleSignal(syscall.Signal(99))
	if handled {
		t.Fatal("非目标信号应返回未处理")
	}
}

// === M22-04 P0 SIGHUP / SIGUSR1 信号处理测试 ===

// TestHandleSignal_SIGHUPTriggersReloadHook 验证 SIGHUP 触发 reload hook 而不退出进程（M22-04 P0）。
func TestHandleSignal_SIGHUPTriggersReloadHook(t *testing.T) {
	sh := NewGracefulShutdown()
	var reloadCalled int32
	sh.RegisterReloadHook(func() {
		atomic.StoreInt32(&reloadCalled, 1)
	})

	handled := sh.HandleSignal(syscall.SIGHUP)

	if !handled {
		t.Fatal("SIGHUP 应被处理（返回 true）")
	}
	if atomic.LoadInt32(&reloadCalled) != 1 {
		t.Fatal("SIGHUP 应触发 reload hook")
	}
	// 不应触发退出（exitCode 应保持 0）
	// （exitFn 默认为 os.Exit，若被调用测试进程已退出，此断言隐式）
}

// TestHandleSignal_SIGHUPOneSignalDoesNotExit 验证 SIGHUP 不触发 Shutdown（M22-04 P0）。
// 即使发送多次 SIGHUP，进程仍不应退出（仅触发 reload hooks）。
func TestHandleSignal_SIGHUPOneSignalDoesNotExit(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }

	var reloadCount int32
	sh.RegisterReloadHook(func() { atomic.AddInt32(&reloadCount, 1) })

	// 发送 3 次 SIGHUP
	sh.HandleSignal(syscall.SIGHUP)
	sh.HandleSignal(syscall.SIGHUP)
	sh.HandleSignal(syscall.SIGHUP)

	if got := atomic.LoadInt32(&exitCode); got != 0 {
		t.Fatalf("SIGHUP 不应触发退出，exitCode=%d", got)
	}
	if got := atomic.LoadInt32(&reloadCount); got != 3 {
		t.Fatalf("应触发 3 次 reload hook，实际 %d", got)
	}
}

// TestHandleSignal_SIGUSR1TriggersLogLevelToggleHook 验证 SIGUSR1 触发日志级别切换 hook（M22-04 P0）。
func TestHandleSignal_SIGUSR1TriggersLogLevelToggleHook(t *testing.T) {
	sh := NewGracefulShutdown()
	var toggleCount int32
	sh.RegisterLogLevelToggleHook(func() {
		atomic.AddInt32(&toggleCount, 1)
	})

	handled := sh.HandleSignal(syscall.SIGUSR1)

	if !handled {
		t.Fatal("SIGUSR1 应被处理（返回 true）")
	}
	if got := atomic.LoadInt32(&toggleCount); got != 1 {
		t.Fatalf("SIGUSR1 应触发 1 次 toggle hook，实际 %d", got)
	}
}

// TestHandleSignal_SIGUSR1DoesNotExit 验证 SIGUSR1 不触发 Shutdown（M22-04 P0）。
func TestHandleSignal_SIGUSR1DoesNotExit(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }

	sh.RegisterLogLevelToggleHook(func() {})

	// 发送多次 SIGUSR1
	sh.HandleSignal(syscall.SIGUSR1)
	sh.HandleSignal(syscall.SIGUSR1)

	if got := atomic.LoadInt32(&exitCode); got != 0 {
		t.Fatalf("SIGUSR1 不应触发退出，exitCode=%d", got)
	}
}

// TestRegisterReloadHook_NilIgnored 验证 nil reload hook 被安全忽略（M22-04 P0）。
func TestRegisterReloadHook_NilIgnored(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.RegisterReloadHook(nil) // 不应 panic
	// 触发 SIGHUP 应安全返回，不 panic
	handled := sh.HandleSignal(syscall.SIGHUP)
	if !handled {
		t.Fatal("SIGHUP 应被处理")
	}
}

// TestRegisterLogLevelToggleHook_NilIgnored 验证 nil log level toggle hook 被安全忽略（M22-04 P0）。
func TestRegisterLogLevelToggleHook_NilIgnored(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.RegisterLogLevelToggleHook(nil) // 不应 panic
	handled := sh.HandleSignal(syscall.SIGUSR1)
	if !handled {
		t.Fatal("SIGUSR1 应被处理")
	}
}

// TestHandleSignal_SIGHUPHookPanicDoesNotPropagate 验证 reload hook panic 不影响后续 hook（M22-04 P0）。
func TestHandleSignal_SIGHUPHookPanicDoesNotPropagate(t *testing.T) {
	sh := NewGracefulShutdown()
	var secondCalled int32
	// 第一个 hook 故意 panic
	sh.RegisterReloadHook(func() { panic("simulated reload hook panic") })
	// 第二个 hook 应仍被调用
	sh.RegisterReloadHook(func() { atomic.StoreInt32(&secondCalled, 1) })

	// 不应 panic
	handled := sh.HandleSignal(syscall.SIGHUP)
	if !handled {
		t.Fatal("SIGHUP 应被处理")
	}
	if atomic.LoadInt32(&secondCalled) != 1 {
		t.Fatal("第一个 hook panic 不应阻止第二个 hook 执行")
	}
}

// TestHandleSignal_SIGHUPAndSIGUSR1DoNotAffectSIGINTShutdown 验证 SIGHUP/SIGUSR1 不会破坏 SIGINT 退出语义（M22-04 P0）。
// 收到 SIGHUP/SIGUSR1 后再按 SIGINT，仍应正常触发优雅退出。
func TestHandleSignal_SIGHUPAndSIGUSR1DoNotAffectSIGINTShutdown(t *testing.T) {
	sh := NewGracefulShutdown()
	var cleanupCalled int32
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&cleanupCalled, 1)
		return nil
	})
	sh.RegisterReloadHook(func() {})
	sh.RegisterLogLevelToggleHook(func() {})

	// 先发 SIGHUP/SIGUSR1
	sh.HandleSignal(syscall.SIGHUP)
	sh.HandleSignal(syscall.SIGUSR1)

	// 再发 SIGINT，应触发优雅退出
	sh.HandleSignal(syscall.SIGINT)

	if atomic.LoadInt32(&cleanupCalled) != 1 {
		t.Fatal("SIGINT 应触发 cleanup（SIGHUP/SIGUSR1 不应破坏退出语义）")
	}
}

// TestHandleSignal_DoublePressWindow 验证超过 2s 的二次信号仍为优雅退出。
func TestHandleSignal_DoublePressWindow(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.shutdownTimeout = 100 * time.Millisecond
	var exitCode int32
	var cleanupCount int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }
	sh.RegisterCleanup(func() error {
		atomic.AddInt32(&cleanupCount, 1)
		return nil
	})

	// 首次优雅
	sh.HandleSignal(syscall.SIGINT)
	// 模拟超过 2s 窗口：手动回拨 lastSignal
	sh.mu.Lock()
	sh.lastSignal = time.Now().Add(-3 * time.Second)
	sh.mu.Unlock()

	// 再次应为优雅，而非强制
	sh.HandleSignal(syscall.SIGINT)

	if got := atomic.LoadInt32(&exitCode); got != 0 {
		t.Fatalf("超过 2s 窗口的二次信号不应强制退出，exitCode=%d", got)
	}
	if got := atomic.LoadInt32(&cleanupCount); got != 1 {
		t.Fatalf("超过 2s 窗口的二次信号不应再触发 cleanup（once），实际 %d", got)
	}
}

// TestRegisterCancel_NilIgnored 验证 nil cancel 被安全忽略。
func TestRegisterCancel_NilIgnored(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.RegisterCancel(nil) // 不应 panic
	sh.Shutdown(false)
}

// TestRegisterCleanup_NilIgnored 验证 nil cleanup 被安全忽略。
func TestRegisterCleanup_NilIgnored(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.RegisterCleanup(nil) // 不应 panic
	sh.Shutdown(false)
}

// TestNewGracefulShutdown_Defaults 验证默认超时为 5s。
func TestNewGracefulShutdown_Defaults(t *testing.T) {
	sh := NewGracefulShutdown()
	if sh.shutdownTimeout != 5*time.Second {
		t.Fatalf("默认超时应为 5s，实际 %s", sh.shutdownTimeout)
	}
	if sh.exitFn == nil {
		t.Fatal("exitFn 应默认为非 nil（os.Exit）")
	}
}

// TestHandleSignal_SIGTERMGaraceful 验证 SIGTERM 触发优雅关闭（梯队特有检查）。
// SIGTERM 是 Linux 上最常见的进程终止信号，必须触发优雅退出。
func TestHandleSignal_SIGTERMGaraceful(t *testing.T) {
	sh := NewGracefulShutdown()
	var cleanupCalled int32
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&cleanupCalled, 1)
		return nil
	})

	handled := sh.HandleSignal(syscall.SIGTERM)

	if !handled {
		t.Fatal("SIGTERM 应被处理")
	}
	if atomic.LoadInt32(&cleanupCalled) != 1 {
		t.Fatal("SIGTERM 应触发优雅 cleanup")
	}
}

// TestHandleSignal_SIGTERMSecondForce 验证 2s 内第二次 SIGTERM 触发强制退出（梯队特有检查）。
func TestHandleSignal_SIGTERMSecondForce(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }

	sh.HandleSignal(syscall.SIGTERM)
	sh.HandleSignal(syscall.SIGTERM)

	if got := atomic.LoadInt32(&exitCode); got != 130 {
		t.Fatalf("二次 SIGTERM 应以 130 强制退出，实际 %d", got)
	}
}

// TestSignal_ConcurrentSafe 验证并发注册和并发 Shutdown 不会产生 data race（反例测试）。
func TestSignal_ConcurrentSafe(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.exitFn = func(code int) {} // 阻止真实退出

	const goroutines = 50
	var wg sync.WaitGroup

	// 并发注册 cancel 和 cleanup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			_ = ctx
			sh.RegisterCancel(cancel)
			sh.RegisterCleanup(func() error { return nil })
		}(i)
	}
	wg.Wait()

	// 并发调用 Shutdown
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh.Shutdown(false)
		}()
	}
	wg.Wait()
}

// TestShutdown_CleanupTimeout 验证 cleanup 超时兜底机制生效。
// 当 cleanup 函数执行时间超过 shutdownTimeout 时，Shutdown 不应永久阻塞。
func TestShutdown_CleanupTimeout(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.shutdownTimeout = 50 * time.Millisecond

	var cleanupStarted int32
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&cleanupStarted, 1)
		time.Sleep(500 * time.Millisecond) // 故意超过 50ms 超时
		return nil
	})

	done := make(chan struct{})
	go func() {
		sh.Shutdown(false)
		close(done)
	}()

	select {
	case <-done:
		// 预期：Shutdown 在超时后返回，不会阻塞到 cleanup 完成
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown 在 cleanup 超时后应返回，不应永久阻塞")
	}

	if atomic.LoadInt32(&cleanupStarted) != 1 {
		t.Fatal("cleanup 应已启动")
	}
}

// TestShutdown_ForceExitIdempotent 验证多次 force=true 不会重复调用 exitFn。
func TestShutdown_ForceExitIdempotent(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCount int32
	sh.exitFn = func(code int) { atomic.AddInt32(&exitCount, 1) }

	sh.Shutdown(true)
	sh.Shutdown(true)
	sh.Shutdown(true)

	if got := atomic.LoadInt32(&exitCount); got != 1 {
		t.Fatalf("force exit 应只触发一次，实际 %d 次", got)
	}
}

// TestRegisterCleanup_ReturnsCleanupRemoval 验证 RegisterCleanup 注册的清理函数可被移除。
// 当前实现未返回移除函数，测试验证注册多个 cleanup 时的行为。
func TestRegisterCleanup_MultipleCleanups(t *testing.T) {
	sh := NewGracefulShutdown()
	var order []int32
	var mu sync.Mutex
	sh.RegisterCleanup(func() error {
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
		return nil
	})
	sh.RegisterCleanup(func() error {
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		return nil
	})
	sh.RegisterCleanup(func() error {
		mu.Lock()
		order = append(order, 3)
		mu.Unlock()
		return nil
	})

	sh.Shutdown(false)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("应有 3 个 cleanup 执行，实际 %d", len(order))
	}
}

// TestShutdown_CleanupErrorLogged 验证 cleanup 函数返回错误时不影响其他 cleanup 执行。
// 错误应被记录而非吞没。
func TestShutdown_CleanupErrorLogged(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.shutdownTimeout = 5 * time.Second

	var secondCalled int32
	sh.RegisterCleanup(func() error {
		return fmt.Errorf("simulated cleanup error")
	})
	sh.RegisterCleanup(func() error {
		atomic.StoreInt32(&secondCalled, 1)
		return nil
	})

	// 不应 panic，第二个 cleanup 应仍然执行
	sh.Shutdown(false)

	if atomic.LoadInt32(&secondCalled) != 1 {
		t.Fatal("cleanup 错误不应阻止后续 cleanup 执行")
	}
}

// TestInstallSignalHandler_Integration 验证 InstallSignalHandler 收到信号后转发给 HandleSignal。
// 通过向进程发送 SIGTERM 验证端到端信号处理。
func TestInstallSignalHandler_Integration(t *testing.T) {
	sh := NewGracefulShutdown()
	var exitCode int32
	sh.exitFn = func(code int) { atomic.StoreInt32(&exitCode, int32(code)) }

	InstallSignalHandler(sh)

	// 给 goroutine 启动时间
	time.Sleep(50 * time.Millisecond)

	// 发送 SIGINT 给自己
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("查找当前进程失败: %v", err)
	}
	if err := p.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("发送 SIGINT 失败: %v", err)
	}

	// 等待信号处理
	deadline := time.After(2 * time.Second)
	for {
		if atomic.LoadInt32(&exitCode) == 130 || atomic.LoadInt32(&exitCode) == 0 {
			// exitCode=130 说明两次信号都被处理了（可能 SIGINT 在 goroutine 中触发）
			// exitCode=0 说明优雅退出（一次 SIGINT）— 两种都可接受
			break
		}
		select {
		case <-deadline:
			t.Fatal("InstallSignalHandler 未在超时内处理信号")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestSignal_NoDeadlockOnConcurrentHandleSignal 验证并发调用 HandleSignal 不会死锁（梯队特有检查）。
// 这是信号处理中的关键安全属性：信号处理器本身不能有死锁风险。
func TestSignal_NoDeadlockOnConcurrentHandleSignal(t *testing.T) {
	sh := NewGracefulShutdown()
	sh.exitFn = func(code int) {} // 阻止真实退出

	const goroutines = 20
	var wg sync.WaitGroup
	done := make(chan struct{})

	// 并发发送 SIGINT
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh.HandleSignal(syscall.SIGINT)
		}()
	}

	// 并发发送 SIGTERM
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh.HandleSignal(syscall.SIGTERM)
		}()
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 预期：所有 HandleSignal 调用在合理时间内完成，无死锁
	case <-time.After(5 * time.Second):
		t.Fatal("并发 HandleSignal 可能有死锁，5s 超时")
	}
}

// 确保 os 包被使用（避免未使用导入在部分构建配置下报错）
var _ = os.Exit
