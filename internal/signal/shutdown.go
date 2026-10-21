// Package signal 提供跨模式的统一信号处理与优雅退出协调器。
//
// 本包解决以下问题：
//   - 6 种运行模式中仅 Daemon+Plain 有 signal.Notify，TUI/Bridge/CI/Prompt 均无信号处理
//   - 退出时无分层语义（一次=优雅，二次=强制）
//   - 缺少统一的 cancel/cleanup 注册入口
//   - M22-04 P0：仅 SIGINT/SIGTERM 无 SIGHUP/SIGUSR1，无配置热重载入口
//
// GracefulShutdown 协调器保证：
//   - 优雅清理（cancelFuncs + cleanupFuncs）最多执行一次（sync.Once）
//   - 强制退出（force=true）跳过 cleanup 直接 os.Exit(130)，不受 once 限制
//   - 分层信号语义：首次 SIGINT/SIGTERM 优雅退出，2s 内第二次强制退出
//   - M22-04 P0：SIGHUP 触发配置热重载 hooks（不退出进程）
//   - M22-04 P0：SIGUSR1 触发日志级别切换 hooks（不退出进程）
package signal

import (
	"context"
	"log"
	"os"
	ossignal "os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// 跨平台 SIGHUP / SIGUSR1 信号值（M22-04 P0 引入）。
//
// Go 标准库 syscall 包在 Windows 上未定义 SIGHUP/SIGUSR1 常量，使用 syscall.Signal(N) 兜底。
// Windows 平台上 os/signal.Notify 注册这些信号不会触发（Windows 不支持），
// 但代码可跨平台编译。在 Linux/macOS/WSL 上正常工作。
//   - SIGHUP = 1（POSIX 标准，守护进程惯例触发配置重载）
//   - SIGUSR1 = 10（Linux）/ 30（Darwin/BSD），用于日志级别切换
const (
	sigHUP = syscall.Signal(0x1)
)

func sigUSR1() os.Signal {
	// SIGUSR1 is 10 on Linux and 30 on Darwin/BSD. Windows does not deliver
	// this signal, but retaining the POSIX value keeps the code compilable.
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" {
		return syscall.Signal(30)
	}
	return syscall.Signal(10)
}

// GracefulShutdown 是跨模式的优雅退出协调器。
// 通过 RegisterCancel/RegisterCleanup 收集需在退出时执行的取消与清理动作，
// 由 Shutdown 统一调度，sync.Once 保证优雅清理只执行一次。
//
// M22-04 P0 新增：RegisterReloadHook 收集 SIGHUP 触发的配置热重载 hook，
// RegisterLogLevelToggleHook 收集 SIGUSR1 触发的日志级别切换 hook。
type GracefulShutdown struct {
	// cancelFuncs 保存所有需取消的流/工具 context 的 CancelFunc
	cancelFuncs []context.CancelFunc
	// cleanupFuncs 保存资源清理函数（如 AIClient.Close / FileCache.Persist）
	cleanupFuncs []func() error
	// reloadHooks 保存 SIGHUP 触发的配置热重载回调（M22-04 P0）
	reloadHooks []func()
	// logLevelToggleHooks 保存 SIGUSR1 触发的日志级别切换回调（M22-04 P0）
	logLevelToggleHooks []func()
	// shutdownTimeout 优雅清理的超时兜底，默认 5s
	shutdownTimeout time.Duration
	// once 保证优雅清理逻辑最多执行一次
	once sync.Once

	// mu 保护 cancelFuncs/cleanupFuncs/reloadHooks/logLevelToggleHooks/lastSignal/exited 的并发访问
	mu sync.Mutex
	// lastSignal 最近一次收到 SIGINT/SIGTERM 的时间，用于判断二次强制退出
	lastSignal time.Time
	// exited 标记强制退出是否已触发，避免重复 os.Exit
	exited bool
	// exitFn 强制退出时调用的退出函数，默认 os.Exit；测试可覆盖以避免进程退出
	exitFn func(int)
}

// NewGracefulShutdown 创建协调器并设置默认 5s 超时。
func NewGracefulShutdown() *GracefulShutdown {
	return &GracefulShutdown{
		shutdownTimeout: 5 * time.Second,
		exitFn:          os.Exit,
	}
}

// RegisterCancel 注册一个 context.CancelFunc，退出时用于中断进行中的流或工具执行。
func (sh *GracefulShutdown) RegisterCancel(cancel context.CancelFunc) {
	if cancel == nil {
		return
	}
	sh.mu.Lock()
	sh.cancelFuncs = append(sh.cancelFuncs, cancel)
	sh.mu.Unlock()
}

// RegisterCleanup 注册一个资源清理函数，优雅退出时执行。
// 返回的取消函数可将该清理函数从列表中移除（用于可重入场景）。
func (sh *GracefulShutdown) RegisterCleanup(fn func() error) {
	if fn == nil {
		return
	}
	sh.mu.Lock()
	sh.cleanupFuncs = append(sh.cleanupFuncs, fn)
	sh.mu.Unlock()
}

// RegisterReloadHook 注册一个 SIGHUP 触发的配置热重载回调（M22-04 P0）。
//
// 收到 SIGHUP 时所有已注册的 reloadHook 会被调用，按注册顺序执行。
// 用于在长会话中切换配置而无需退出重启（对标守护进程 SIGHUP 惯例）。
// hook 内部应处理错误（如配置文件解析失败）并记录日志，不应 panic 影响后续 hook。
func (sh *GracefulShutdown) RegisterReloadHook(fn func()) {
	if fn == nil {
		return
	}
	sh.mu.Lock()
	sh.reloadHooks = append(sh.reloadHooks, fn)
	sh.mu.Unlock()
}

// RegisterLogLevelToggleHook 注册一个 SIGUSR1 触发的日志级别切换回调（M22-04 P0）。
//
// 收到 SIGUSR1 时所有已注册的 logLevelToggleHook 会被调用，按注册顺序执行。
// 典型用法：在 Debug 和 Info 之间切换 log.SetLevel，便于运维临时抓取 debug 日志。
func (sh *GracefulShutdown) RegisterLogLevelToggleHook(fn func()) {
	if fn == nil {
		return
	}
	sh.mu.Lock()
	sh.logLevelToggleHooks = append(sh.logLevelToggleHooks, fn)
	sh.mu.Unlock()
}

// runCancels 执行所有已注册的 cancelFuncs（中断流/工具）。
// 调用方需持锁外调用，内部加锁拷贝快照后执行。
func (sh *GracefulShutdown) runCancels() {
	sh.mu.Lock()
	cancels := append([]context.CancelFunc(nil), sh.cancelFuncs...)
	sh.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// runCleanupsWithTimeout 执行所有清理函数，受 shutdownTimeout 超时兜底保护。
// 超时后不再等待清理完成（避免卡死），但已启动的清理 goroutine 会继续在后台运行。
func (sh *GracefulShutdown) runCleanupsWithTimeout() {
	sh.mu.Lock()
	cleanups := append([]func() error(nil), sh.cleanupFuncs...)
	sh.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for _, fn := range cleanups {
			if err := fn(); err != nil {
				log.Printf("[signal] cleanup error: %v", err)
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(sh.shutdownTimeout):
		log.Printf("[signal] graceful cleanup timed out after %s", sh.shutdownTimeout)
	}
}

// runReloadHooks 执行所有 SIGHUP 注册的配置热重载回调（M22-04 P0）。
// 按 hook 注册顺序同步执行；hook 错误不影响后续 hook，仅记录日志。
func (sh *GracefulShutdown) runReloadHooks() {
	sh.mu.Lock()
	hooks := append([]func(){}, sh.reloadHooks...)
	sh.mu.Unlock()
	for _, fn := range hooks {
		// hook 内部应处理错误，此处用 defer recover 兜底防止单个 hook panic 影响后续 hook
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[signal] reload hook panic: %v", r)
				}
			}()
			fn()
		}()
	}
	log.Printf("[signal] SIGHUP reload hooks executed (%d)", len(hooks))
}

// runLogLevelToggleHooks 执行所有 SIGUSR1 注册的日志级别切换回调（M22-04 P0）。
// 按 hook 注册顺序同步执行；hook 错误不影响后续 hook，仅记录日志。
func (sh *GracefulShutdown) runLogLevelToggleHooks() {
	sh.mu.Lock()
	hooks := append([]func(){}, sh.logLevelToggleHooks...)
	sh.mu.Unlock()
	for _, fn := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[signal] log level toggle hook panic: %v", r)
				}
			}()
			fn()
		}()
	}
	log.Printf("[signal] SIGUSR1 log level toggle hooks executed (%d)", len(hooks))
}

// Shutdown 执行退出清理。
//   - force=false：调所有 cancelFuncs（中断流）+ 执行 cleanupFuncs（清理资源），5s 超时兜底；
//     优雅清理受 once 保护，最多执行一次。
//   - force=true：调所有 cancelFuncs + 跳过 cleanupFuncs + os.Exit(130)；
//     强制退出不受 once 限制，确保用户二次中断时能立即退出。
func (sh *GracefulShutdown) Shutdown(force bool) {
	if force {
		// 强制退出：先取消所有流，再直接 os.Exit(130)，跳过 cleanup
		sh.mu.Lock()
		if sh.exited {
			sh.mu.Unlock()
			return
		}
		sh.exited = true
		sh.mu.Unlock()
		sh.runCancels()
		sh.exitFn(130)
		return
	}

	// 优雅退出：once 保证清理逻辑只执行一次
	sh.once.Do(func() {
		sh.runCancels()
		sh.runCleanupsWithTimeout()
	})
}

// HandleSignal 处理收到的信号，实现分层语义。
//   - 第一次 SIGINT/SIGTERM：调 Shutdown(false) 优雅退出，返回 true（已处理）。
//   - 第二次 SIGINT/SIGTERM（2s 内）：调 Shutdown(true) 强制退出。
//   - SIGHUP：触发 reloadHooks 配置热重载（不退出进程），返回 true（已处理）。
//   - SIGUSR1：触发 logLevelToggleHooks 日志级别切换（不退出进程），返回 true（已处理）。
//   - 非目标信号：返回 false（未处理）。
//
// 返回值表示该信号是否被本协调器处理。
func (sh *GracefulShutdown) HandleSignal(sig os.Signal) bool {
	// M22-04 P0：SIGHUP 触发配置热重载（不退出）
	if sig == sigHUP {
		log.Printf("[signal] received SIGHUP, triggering config reload")
		sh.runReloadHooks()
		return true
	}
	// M22-04 P0：SIGUSR1 触发日志级别切换（不退出）
	if sig == sigUSR1() {
		log.Printf("[signal] received SIGUSR1, triggering log level toggle")
		sh.runLogLevelToggleHooks()
		return true
	}
	if sig != syscall.SIGINT && sig != syscall.SIGTERM {
		return false
	}

	now := time.Now()
	sh.mu.Lock()
	doublePress := !sh.lastSignal.IsZero() && now.Sub(sh.lastSignal) < 2*time.Second
	sh.lastSignal = now
	sh.mu.Unlock()

	if doublePress {
		// 2s 内第二次中断：强制退出
		sh.Shutdown(true)
		return true
	}
	// 首次中断：优雅退出
	sh.Shutdown(false)
	return true
}

// InstallSignalHandler 注册 SIGINT/SIGTERM/SIGHUP/SIGUSR1 监听，收到信号后转发给 HandleSignal。
// 在独立 goroutine 中运行，不阻塞调用方。
//
// M22-04 P0：新增 SIGHUP/SIGUSR1 监听，分别触发配置热重载与日志级别切换。
// Windows 平台不支持 SIGHUP/SIGUSR1 信号（os/signal 包注册但不触发），Linux/macOS/WSL 正常工作。
func InstallSignalHandler(sh *GracefulShutdown) {
	installSignalHandler(sh, true)
}

// InstallSignalHandlerWithoutInterrupt registers the signals needed by an
// interactive TUI without consuming SIGINT. In raw mode Ctrl+C is delivered
// to Bubble Tea as a key event so the model can stop the active turn.
func InstallSignalHandlerWithoutInterrupt(sh *GracefulShutdown) {
	installSignalHandler(sh, false)
}

func installSignalHandler(sh *GracefulShutdown, includeInterrupt bool) {
	sigCh := make(chan os.Signal, 1)
	signals := []os.Signal{syscall.SIGTERM, sigHUP, sigUSR1()}
	if includeInterrupt {
		signals = append([]os.Signal{syscall.SIGINT}, signals...)
	}
	ossignal.Notify(sigCh, signals...)
	go func() {
		for sig := range sigCh {
			sh.HandleSignal(sig)
		}
	}()
}
