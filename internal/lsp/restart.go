// Package lsp — LSP 服务器崩溃自动恢复（T1-P1-A2 /）
//
// 崩溃恢复机制：
// 1. readLoop 检测 EOF → 设置 crashed=true（仅当不是主动 Close 触发的退出）
// 2. 调用方查询时检测 IsCrashed()==false && IsReady()==false → 触发 Restart
// 3. Restart 按指数退避（1s/2s/4s/8s/16s/30s 封顶）等待
// 4. 重启次数超 maxRestarts → 永久标记 crashed，返回错误
// 5. healthCheck（可选，默认禁用）周期性探测子进程存活
//
// 对标 VS Code LanguageClient maxRestartCount + 指数退避：
// - 官方源：https://github.com/microsoft/vscode-languageserver-node（访问日期 2026-07-21）
// - VS Code 默认 maxRestartCount=-1（无限），本方案默认 5（避免无限重启消耗资源）
// - VS Code 无指数退避封顶，本方案 30s 封顶（吸纳优化 4）
//
// 并发安全（吸纳优化 1）：
// - Restart 入口先检查 stopChan（select default），已关闭则跳过
// - restartMu 串行化 restart 调用，防止并发重启
// - Close 持 stopChan close 优先于 restart（双检：入口 + 持锁后）
package lsp

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// MaxBackoffMs 退避算法上限（吸纳优化 4）
// 30s 封顶，避免极端场景 60s+ 不可用
const MaxBackoffMs = 30000

// Restart 触发子进程重建，按 maxRestarts + 指数退避
// 调用方在 IsReady()==false 且 IsCrashed()==false 时调用
// 返回 nil 表示重启成功，client 可用
// 返回 error 表示重启失败（超 maxRestarts 或子进程启动失败或 client 已 Close）
// §3.1.1
func (c *Client) Restart(ctx context.Context) error {
	// 入口先检查 stopChan（吸纳优化 1：避免与 Close 竞争）
	if c.stopChan == nil {
		return fmt.Errorf("client not initialized, cannot restart")
	}
	select {
	case <-c.stopChan:
		return fmt.Errorf("client is closing, skip restart")
	default:
	}

	c.restartMu.Lock()
	defer c.restartMu.Unlock()

	// 再次检查 stopChan（双检：持锁期间 Close 可能已执行）
	select {
	case <-c.stopChan:
		return fmt.Errorf("client is closing, skip restart")
	default:
	}

	// 检查 maxRestarts
	if c.cfg != nil && c.cfg.Recovery.MaxRestarts != -1 && c.restartCount >= c.cfg.Recovery.MaxRestarts {
		c.crashed.Store(true)
		return fmt.Errorf("LSP server crashed, max restarts (%d) exceeded", c.cfg.Recovery.MaxRestarts)
	}

	// 指数退避
	backoff := c.calculateBackoff()
	if backoff > 0 {
		log.Printf("[lsp] restart backoff %v (restart count: %d)", backoff, c.restartCount)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		case <-c.stopChan:
			return fmt.Errorf("client is closing during backoff")
		}
	}

	// 执行重启（关闭旧进程 + 创建新进程 + 重新 initialize）
	if err := c.doRestart(ctx); err != nil {
		return err
	}

	c.restartCount++
	c.lastRestart = time.Now()
	c.crashed.Store(false)
	log.Printf("[lsp] restart success (restart count: %d)", c.restartCount)
	return nil
}

// calculateBackoff 计算指数退避延迟
// 公式：min(backoffDelayMs * 2^(restartCount-1), MaxBackoffMs)
// restartCount=0 时返回 0（首次立即重启）
// 吸纳优化 4：30s 封顶
// §3.1.5
func (c *Client) calculateBackoff() time.Duration {
	if c.restartCount == 0 {
		return 0
	}
	base := 1000
	if c.cfg != nil && c.cfg.Recovery.BackoffDelayMs > 0 {
		base = c.cfg.Recovery.BackoffDelayMs
	}
	// 指数退避：base * 2^(count-1)
	// count=1 → base, count=2 → 2*base, count=3 → 4*base, ...
	shift := c.restartCount - 1
	if shift > 20 {
		// 防止溢出（2^20 = 1M，足够）
		shift = 20
	}
	delayMs := int64(base) * (1 << shift)
	if delayMs > MaxBackoffMs || delayMs < 0 {
		delayMs = MaxBackoffMs
	}
	return time.Duration(delayMs) * time.Millisecond
}

// doRestart 执行实际重启逻辑
// 1. 关闭旧 stdin（让旧 server 退出）
// 2. 等待 readLoop 退出（1s 超时）
// 3. kill 旧进程
// 4. 清理 pending
// 5. 创建新 stopChan（旧 stopChan 已被 readLoop defer 检测）
// 6. 创建新子进程 + stdin/stdout pipe + readLoop + initialize
func (c *Client) doRestart(ctx context.Context) error {
	// 1. 关闭旧 stdin（让旧 server 退出）
	if c.stdin != nil {
		// stdin.Close 是幂等的（io.WriteCloser 实现保证）
		c.stdin.Close()
	}

	// 2. 等 readLoop 退出（带 1s 超时）
	// 注意：readLoop 在 stdin Close 后会因 stdout EOF 退出
	waitDone := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(1 * time.Second):
		// readLoop 卡死，强制 kill 进程后 stdout pipe 关闭，readLoop 退出
		if c.cmd != nil && c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
	}

	// 3. kill 旧进程
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			c.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			c.cmd.Process.Kill()
			<-done
		}
	}

	// LSP-1 修复（全模块审查）：确保旧 readLoop 完全退出后再替换 wg。
	// 原实现在 step 2 超时后 kill 进程，但不等待 readLoop 退出就调用 spawnAndInitialize，
	// spawnAndInitialize 中 c.wg = sync.WaitGroup{} 替换旧 wg，
	// 旧 readLoop 的 defer c.wg.Done() 会作用到新 wg 导致计数错乱（panic 或误判）。
	// 修复：kill 进程后再次等待 waitDone（readLoop 退出信号），设 2s 超时兜底。
	select {
	case <-waitDone:
		// 旧 readLoop 已退出，安全替换 wg
	case <-time.After(2 * time.Second):
		// readLoop 仍卡死（极端场景，如 stdout pipe 未正确关闭），
		// 继续执行会导致 goroutine 泄漏但避免阻塞 Restart。
		// 此情况极罕见，记录日志后继续。
		log.Printf("[lsp] WARNING: old readLoop did not exit after kill (2s timeout), proceeding with wg replacement (possible goroutine leak)")
	}

	// 4. 清理 pending（参考 Close 第 5 步）
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()

	// 5. 创建新 stopChan（旧 stopChan 已被 readLoop defer 检测，需重建）
	// 注意：readLoop defer 检查 stopChan 时旧 stopChan 仍未关闭，
	// 走 default 分支清理 pending（与上面第 4 步重复，但 close channel 是幂等的）
	// 此处不 close 旧 stopChan（避免 readLoop defer 再次 close panic）
	// 直接用新 stopChan 替换
	c.stopChan = make(chan struct{})

	// 6. 创建新子进程 + 重新 initialize
	return c.spawnAndInitialize(ctx)
}

// spawnAndInitialize 创建新子进程并完成 LSP initialize 握手
// 被 NewClient（首次启动）和 doRestart（重启）共用
// 失败时清理已分配资源（stdin/stdout/readLoop/cmd）
// 使用 c.cfg / c.language / c.workspaceRoot 字段（在 NewClient 时存储）
func (c *Client) spawnAndInitialize(ctx context.Context) error {
	// 查找 server 命令（用 c.cfg 与 c.language）
	serverCmd, serverArgs, err := findServerFromConfig(c.cfg, c.language)
	if err != nil {
		return fmt.Errorf("find server: %w", err)
	}

	// 重新创建 cancel context
	childCtx, cancel := context.WithCancel(ctx)
	c.cancelPending = cancel

	// 创建新 cmd
	cmd := exec.CommandContext(childCtx, serverCmd, serverArgs...)
	cmd.Dir = c.workspaceRoot
	cmd.Stderr = os.Stderr

	// stdin pipe
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}

	// stdout pipe
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		stdinPipe.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	// 启动子进程
	if err := cmd.Start(); err != nil {
		cancel()
		stdinPipe.Close()
		return fmt.Errorf("start server: %w", err)
	}

	// 更新 Client 字段（旧字段已被 doRestart 第 1-4 步清理）
	c.cmd = cmd
	c.stdin = stdinPipe
	c.stdout = bufio.NewReader(stdoutPipe)
	c.ready = false

	// 重置 wg 并启动新 readLoop
	// 注意：sync.WaitGroup 不能复用（需重新初始化），用 new(WaitGroup) 替换
	c.wg = sync.WaitGroup{}
	c.wg.Add(1)
	go c.readLoop()

	// 重新 initialize
	if err := c.initialize(childCtx, c.rootURI); err != nil {
		// initialize 失败，清理（参考 NewClient 失败路径）
		// 注意：此处不 close stopChan（doRestart 已创建新 stopChan，避免 readLoop defer panic）
		// 改为：stdin.Close 触发 stdout EOF 让 readLoop 退出
		if c.stdin != nil {
			c.stdin.Close()
		}
		c.wg.Wait()
		cancel()
		if c.cmd != nil && c.cmd.Process != nil {
			c.cmd.Process.Kill()
			c.cmd.Wait()
		}
		return fmt.Errorf("initialize: %w", err)
	}

	c.ready = true
	return nil
}

// IsCrashed 返回 client 是否已崩溃（无法恢复，超 maxRestarts）
// 调用方检测到 IsCrashed()==true 时应从缓存移除并返回错误
// §3.1.1
func (c *Client) IsCrashed() bool {
	return c.crashed.Load()
}

// HealthCheck 探测子进程是否存活
// 跨平台实现：检查 cmd.ProcessState.Exited()（吸纳优化 2：Windows 不支持 syscall.Signal(0)）
// 默认禁用（HealthCheckMs=0），仅 readLoop EOF 触发 Restart
// 返回 true 表示子进程存活，false 表示已退出
// §3.1.1
func (c *Client) HealthCheck() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	// 用 ProcessState 检查（跨平台，Windows 兼容）
	// ProcessState 在 cmd.Wait() 返回后才非 nil，进程刚启动时为 nil
	if c.cmd.ProcessState != nil {
		return !c.cmd.ProcessState.Exited()
	}
	// 进程刚启动还未 Wait，假定存活
	return true
}

// startHealthChecker 启动独立健康检查 goroutine（仅 HealthCheckMs > 0 时启用）
// 默认禁用，避免 Windows 兼容问题（吸纳优化 2）
// 探测失败时设置 crashed=true 并异步触发 Restart
func (c *Client) startHealthChecker(interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !c.HealthCheck() {
					log.Printf("[lsp] health check failed, triggering restart")
					c.crashed.Store(true)
					// 触发异步 restart，不阻塞 health check goroutine
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := c.Restart(ctx); err != nil {
							log.Printf("[lsp] health check restart failed: %v", err)
						}
					}()
					return
				}
			case <-c.stopChan:
				return
			}
		}
	}()
}
