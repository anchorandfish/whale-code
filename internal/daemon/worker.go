package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// Worker executes background tasks
// - Starts whale --ci subprocess
// - Captures output
// - Terminates on timeout
// - Recovers from panics to prevent worker pool degradation
type Worker struct {
	mu         sync.Mutex
	id         int                // Worker ID
	cliPath    string             // CLI executable path (default "whale")
	timeout    time.Duration      // Task timeout
	cmd        *exec.Cmd          // Currently running command
	task       *Task              // Currently executing task
	busy       bool               // Whether executing a task
	cancel     context.CancelFunc // Cancel function
	workerType WorkerType         // Worker type
	queue      *TaskQueue         // Task queue reference for status updates
	logger     *DaemonLogger      // Logger reference
	registry   *WorkerRegistry    // Worker registry reference, used to record completed tasks
	events     *EventBus          // Task lifecycle events
	// sandboxMgr is an optional worker-level sandbox for embedded deployments.
	// 非 nil 且 sandboxLevel != SandboxLevelDisabled 时，Start 走完整沙箱链路：
	// Prepare → OSSandbox.Restrict → StartCommandWithProcessGroup → PostStartHook → ApplyResourceLimits
	// Nil or Disabled leaves the trusted Whale CI orchestrator unsandboxed;
	// commands inside it still use the normal CI permission/sandbox pipeline.
	sandboxMgr   sandbox.Manager
	sandboxLevel sandbox.SandboxLevel
}

const workerOutputCaptureLimit = 1 << 20 // 1 MiB per stdout/stderr stream

// boundedOutputBuffer retains a useful prefix while reporting every write as
// consumed. The child process can keep running, but cannot grow daemon memory
// without bound before its timeout or cancellation is observed.
type boundedOutputBuffer struct {
	buf bytes.Buffer
}

func (b *boundedOutputBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := workerOutputCaptureLimit - b.Len()
	if remaining <= 0 {
		return written, nil
	}
	if remaining < len(p) {
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return written, nil
}

func (b *boundedOutputBuffer) Len() int       { return b.buf.Len() }
func (b *boundedOutputBuffer) String() string { return b.buf.String() }

// SetSandboxManager 注入沙箱管理器与默认级别（桶 X3 病灶3 接入点）。
//
// 由 WorkerPool.SetSandboxManager 在 pool.Start 创建每个 worker 时转发注入；
// daemon 启动时由 Daemon.SetSandboxManager 注入到 pool，pool 再注入到每个 worker。
//
// 参数：
// - mgr: 沙箱管理器实例（nil=不应用 worker 外层沙箱）
// - level: 默认沙箱级别（SandboxLevelDisabled=跳过沙箱）
func (w *Worker) SetSandboxManager(mgr sandbox.Manager, level sandbox.SandboxLevel) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sandboxMgr = mgr
	w.sandboxLevel = level
}

// NewWorker creates worker instance
// id: worker number
// cliPath: whale CLI path, default "whale"
// timeout: task timeout
func NewWorker(id int, cliPath string, timeout time.Duration) *Worker {
	if cliPath == "" {
		cliPath = "whale"
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Worker{
		id:         id,
		cliPath:    cliPath,
		timeout:    timeout,
		workerType: WorkerTypeCode, // Default code worker
	}
}

// NewTypedWorker creates worker of specified type
func NewTypedWorker(id int, cliPath string, timeout time.Duration, workerType WorkerType) *Worker {
	w := NewWorker(id, cliPath, timeout)
	w.workerType = workerType
	return w
}

// Type returns worker type
func (w *Worker) Type() WorkerType {
	return w.workerType
}

// Start starts task execution
// ctx: parent context for overall cancellation
// task: task to execute
//
// 桶 X3 病灶3 修复：接入完整沙箱链路（/）。
// 修复前：直接 exec.CommandContext + cmd.Start()，跳过沙箱，daemon 后台任务可执行任意命令。
// 修复后：sandboxMgr != nil 且 level != Disabled 时，调用
//
//	Prepare → OSSandbox.Restrict → StartCommandWithProcessGroup → PostStartHook
//
// 完整隔离链路；sandboxMgr == nil 或 Disabled 时不应用 worker 外层沙箱。
//
// 一旦显式配置了 sandbox manager，Prepare/Restrict/PostStartHook 任一步失败都
// fail-closed，避免绕过用户声明的安全边界。
func (w *Worker) Start(ctx context.Context, task *Task) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.busy {
		return fmt.Errorf("worker %d is busy", w.id)
	}
	if task == nil {
		return fmt.Errorf("worker %d cannot start a nil task", w.id)
	}

	taskSnapshot := *task
	w.task = &taskSnapshot
	w.busy = true

	cancelCtx, cancel := context.WithCancel(ctx)
	taskCtx, timeoutCancel := context.WithTimeout(cancelCtx, w.timeout)
	w.cancel = func() {
		cancel()
		timeoutCancel()
	}

	// Keep the prompt out of argv and process inspection. The internal CI mode
	// reads a bounded prompt from this private stdin stream.
	cmd := exec.CommandContext(taskCtx, w.cliPath, "--ci-prompt-stdin")
	cmd.Stdin = strings.NewReader(taskSnapshot.Prompt)
	cmd.Dir = taskSnapshot.WorkDir

	// Capture stdout and stderr
	var stdout, stderr boundedOutputBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.Cancel = func() error {
		if cmd.Process != nil {
			if err := killProcessTree(cmd.Process.Pid); err != nil {
				return cmd.Process.Kill()
			}
		}
		return nil
	}

	// 桶 X3 病灶3：接入沙箱链路（Prepare → Restrict → StartWithProcessGroup → PostStartHook）
	// sandboxMgr==nil 或 level==Disabled 时不应用 worker 外层沙箱。
	if w.sandboxMgr != nil && w.sandboxLevel != sandbox.SandboxLevelDisabled {
		workDir := taskSnapshot.WorkDir
		if workDir == "" {
			// WorkDir 为空时降级到系统临时目录，保证 Prepare 不失败
			workDir = os.TempDir()
			cmd.Dir = workDir
		}
		sbCtx, cleanup, err := w.sandboxMgr.Prepare(taskCtx, w.sandboxLevel, workDir)
		if err != nil {
			// A configured sandbox is an explicit security boundary. Running the
			// task unsandboxed after preparation fails violates that contract.
			log.Printf("[worker %d] ERROR: sandbox Prepare failed: %v", w.id, err)
			if w.logger != nil {
				w.logger.Error("Worker %d sandbox Prepare failed: %v", w.id, err)
			}
			w.resetStartFailureLocked()
			cancel()
			timeoutCancel()
			return fmt.Errorf("prepare sandbox: %w", err)
		} else {
			// 应用沙箱上下文：Env / Restrict / StartWithProcessGroup / PostStartHook
			cmd.Env = sbCtx.Env
			if os := sbCtx.OSSandbox(); os != nil {
				if restrictErr := os.Restrict(cmd); restrictErr != nil {
					log.Printf("[worker %d] ERROR: sandbox Restrict failed: %v", w.id, restrictErr)
					if w.logger != nil {
						w.logger.Error("Worker %d sandbox Restrict failed: %v", w.id, restrictErr)
					}
					cleanup()
					w.resetStartFailureLocked()
					cancel()
					timeoutCancel()
					return fmt.Errorf("restrict sandbox: %w", restrictErr)
				}
			}
			// 沙箱内启动：使用 StartCommandWithProcessGroup 替代 cmd.Start，
			// 确保子进程在新进程组中（killProcessTree 可达）。
			if startErr := sandbox.StartCommandWithProcessGroup(cmd); startErr != nil {
				cleanup()
				w.resetStartFailureLocked()
				cancel()
				timeoutCancel()
				return fmt.Errorf("failed to start subprocess in sandbox: %w", startErr)
			}
			// PostStartHook：Windows Job Object AssignProcess（必须在 Start 后调用）
			if sbCtx.PostStartHook != nil {
				if hookErr := sbCtx.PostStartHook(cmd); hookErr != nil {
					// On Windows the process is still suspended until this hook
					// succeeds. Fail closed and reap it instead of leaking a process.
					_ = killProcessTree(cmd.Process.Pid)
					_ = cmd.Wait()
					log.Printf("[worker %d] ERROR: sandbox PostStartHook failed: %v", w.id, hookErr)
					if w.logger != nil {
						w.logger.Error("Worker %d sandbox PostStartHook failed: %v", w.id, hookErr)
					}
					cleanup()
					w.resetStartFailureLocked()
					cancel()
					timeoutCancel()
					return fmt.Errorf("sandbox post-start hook: %w", hookErr)
				}
			}
			if limitErr := sandbox.ApplyResourceLimits(cmd, sbCtx.ResourceLimits, sandbox.NewAuditLog()); limitErr != nil {
				_ = killProcessTree(cmd.Process.Pid)
				_ = cmd.Wait()
				cleanup()
				w.resetStartFailureLocked()
				cancel()
				timeoutCancel()
				return fmt.Errorf("apply sandbox resource limits: %w", limitErr)
			}
			// 把 cleanup 绑定到 waitTask，确保命令结束后释放沙箱资源
			w.cmd = cmd
			// 在独立 goroutine 中等待子进程完成，并执行 cleanup
			go w.waitTaskWithCleanup(taskCtx, cmd, taskSnapshot, &stdout, &stderr, w.cancel, cleanup)
			return nil
		}
	} else if w.logger != nil {
		// Worker 外层沙箱未启用；Whale CI 内层权限与沙箱仍然生效。
		w.logger.Info("Worker %d starting task %s without sandbox (sandbox disabled or not injected)", w.id, task.ID)
	}

	w.cmd = cmd

	// Start subprocess（无沙箱路径）
	if err := sandbox.StartCommandWithProcessGroup(cmd); err != nil {
		w.resetStartFailureLocked()
		cancel()
		timeoutCancel()
		return fmt.Errorf("failed to start subprocess: %w", err)
	}

	// Asynchronously wait for subprocess to complete
	go w.waitTask(taskCtx, cmd, taskSnapshot, &stdout, &stderr, w.cancel)

	return nil
}

func (w *Worker) resetStartFailureLocked() {
	w.busy = false
	w.task = nil
	w.cmd = nil
	w.cancel = nil
}

// waitTaskWithCleanup 在 waitTask 基础上确保沙箱 cleanup 函数被调用（桶 X3 病灶3）。
// 复用 waitTask 的 panic 恢复、超时处理、状态更新逻辑，仅在 defer 链末尾追加 cleanup 调用。
// cleanup 为 nil 时无操作（向后兼容 waitTask 调用方未传 cleanup 的场景）。
func (w *Worker) waitTaskWithCleanup(ctx context.Context, cmd *exec.Cmd, task Task, stdout, stderr *boundedOutputBuffer, cancel context.CancelFunc, cleanup func()) {
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	w.waitTask(ctx, cmd, task, stdout, stderr, cancel)
}

// waitTask asynchronously waits for subprocess to complete and updates task status.
// Includes panic recovery to prevent a single task failure from crashing the worker.
//
// 修复（全模块审查）：原实现 w.cmd.Wait() 可能永久阻塞（子进程卡死但未退出），
// 虽然 taskCtx 有超时，但 cmd.Wait() 不响应 context（仅 cmd.Cancel 在 ctx.Done 时触发）。
// 修复：增加 select 兜底，cmd.Wait() 在独立 goroutine 中执行，
// 通过 select 监听 ctx.Done() 或 waitDone，确保 ctx 超时/取消时能退出 waitTask。
func (w *Worker) waitTask(ctx context.Context, cmd *exec.Cmd, task Task, stdout, stderr *boundedOutputBuffer, cancel context.CancelFunc) {
	defer func() {
		if r := recover(); r != nil {
			result := fmt.Sprintf("Worker panic recovered: %v", r)
			w.finishTask(task.ID, TaskFailed, result, -1)
			if w.logger != nil {
				w.logger.Error("Worker %d panic recovered: %v", w.id, r)
			}
		}
		if cancel != nil {
			cancel()
		}

		// 接通 RecordCompletedTask：任务进入任何终态（Completed/Failed/Cancelled
		// 以及 panic 恢复）后，记录到注册表以便后续过期清理跟踪。
		// 此前该调用缺失导致 registry.completedTasks 永远为空。
		if w.registry != nil {
			w.registry.RecordCompletedTask(task.ID)
			workerID := fmt.Sprintf("worker-%d", w.id)
			_ = w.registry.SetStatus(workerID, "idle")
			_ = w.registry.SetCurrentTask(workerID, "")
			_ = w.registry.Heartbeat(workerID)
		}

		w.mu.Lock()
		w.busy = false
		w.task = nil
		w.cmd = nil
		w.cancel = nil
		w.mu.Unlock()
	}()

	// 修复：cmd.Wait() 在独立 goroutine 中执行，避免永久阻塞 waitTask。
	// 通过 select 监听 ctx.Done() 或 waitDone，确保 ctx 超时/取消时能退出。
	type waitResult struct{ err error }
	waitCh := make(chan waitResult, 1)
	go func() {
		waitCh <- waitResult{err: cmd.Wait()}
	}()

	var err error
	select {
	case res := <-waitCh:
		err = res.err
	case <-ctx.Done():
		// ctx 超时/取消，强制 kill 进程树并等待 Wait 返回（避免僵尸进程）
		if cmd.Process != nil {
			_ = killProcessTree(cmd.Process.Pid)
		}
		// 等待 Wait 返回（kill 后应很快返回），设 2s 超时兜底防卡死
		select {
		case res := <-waitCh:
			err = res.err
		case <-time.After(1 * time.Second):
			err = fmt.Errorf("worker %d: cmd.Wait() did not return after kill (1s timeout)", w.id)
		}
	}

	// Determine if timeout/cancelled or normal completion
	select {
	case <-ctx.Done():
		// Timed out or cancelled
		if ctx.Err() == context.DeadlineExceeded {
			w.finishTask(task.ID, TaskFailed, fmt.Sprintf("Task timed out (%v)", w.timeout), -1)
		} else {
			w.finishTask(task.ID, TaskCancelled, "Task cancelled", -1)
		}
	default:
		// Normal completion or error exit
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if ok {
				exitCode := exitErr.ExitCode()
				output := stdout.String() + stderr.String()
				if len(output) > 10000 {
					// 收口：砍尾走 tools.TruncateTail 唯一权威（真实丢弃量标记）。
					output = tools.TruncateTail(output, 10000)
				}
				result := fmt.Sprintf("Exit code %d: %s\nOutput:\n%s", exitCode, err.Error(), output)
				w.finishTask(task.ID, TaskFailed, result, exitCode)
			} else {
				result := fmt.Sprintf("Execution error: %v\nOutput:\n%s", err, stderr.String())
				w.finishTask(task.ID, TaskFailed, result, -1)
			}
		} else {
			output := stdout.String()
			if len(output) > 10000 {
				// 收口：砍尾走 tools.TruncateTail 唯一权威（真实丢弃量标记）。
				output = tools.TruncateTail(output, 10000)
			}
			w.finishTask(task.ID, TaskCompleted, output, 0)
		}
	}
}

func (w *Worker) finishTask(taskID string, status TaskStatus, result string, exitCode int) {
	if w.queue == nil {
		return
	}
	updated, err := w.queue.FinishRunning(taskID, status, result, exitCode)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("Failed to update task %s status: %v", taskID, err)
		}
		return
	}
	if !updated {
		return
	}
	if w.events != nil {
		eventType := EventTaskCompleted
		if status == TaskFailed {
			eventType = EventTaskFailed
		} else if status == TaskCancelled {
			eventType = EventTaskCancelled
		}
		w.events.Publish(Event{Type: eventType, Payload: map[string]interface{}{"task_id": taskID, "status": status}})
	}
	// A freed worker can accept the next queued task immediately instead of
	// waiting for the scheduler's two-second recovery poll.
	w.queue.notifyScheduler()
}

// Stop terminates the currently executing task
func (w *Worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.busy || w.cancel == nil {
		return
	}

	// Trigger termination via context cancellation
	w.cancel()

	if w.cmd != nil && w.cmd.Process != nil {
		killProcessTree(w.cmd.Process.Pid)
	}
}

// IsBusy returns whether worker is executing a task
func (w *Worker) IsBusy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.busy
}

// ID returns worker number
func (w *Worker) ID() int {
	return w.id
}

// CurrentTask returns currently executing task
func (w *Worker) CurrentTask() *Task {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.task == nil {
		return nil
	}
	result := *w.task
	return &result
}

// WorkerPool manages multiple concurrent workers
type WorkerPool struct {
	mu          sync.RWMutex
	wg          sync.WaitGroup
	workers     []*Worker
	maxWorkers  int
	cliPath     string
	timeout     time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
	queue       *TaskQueue
	logger      *DaemonLogger
	loggerOwned bool
	registry    *WorkerRegistry // Worker registry, injected into each worker for completed-task tracking
	events      *EventBus
	// sandboxMgr/sandboxLevel 是 daemon 注入的沙箱管理器（桶 X3 病灶3）。
	// 在 pool.Start 创建每个 worker 时通过 SetSandboxManager 转发注入到 worker。
	// sandboxMgr==nil 或 level==Disabled 时不应用 worker 外层沙箱。
	sandboxMgr   sandbox.Manager
	sandboxLevel sandbox.SandboxLevel
}

// NewWorkerPool creates worker pool
// maxWorkers: max worker count
// cliPath: whale CLI path
// timeout: single task timeout
func NewWorkerPool(maxWorkers int, cliPath string, timeout time.Duration) *WorkerPool {
	if maxWorkers <= 0 {
		maxWorkers = 2
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	if cliPath == "" {
		cliPath = "whale"
	}

	pool := &WorkerPool{
		workers:    make([]*Worker, 0, maxWorkers),
		maxWorkers: maxWorkers,
		cliPath:    cliPath,
		timeout:    timeout,
	}

	return pool
}

// Start starts worker pool, begins fetching and executing tasks from queue
// ctx: parent context
// queue: task queue reference
func (p *WorkerPool) Start(ctx context.Context, queue *TaskQueue) {
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.queue = queue
	if p.logger == nil {
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("whale-daemon-pool-%d-%p.log", os.Getpid(), p))
		p.logger, _ = NewDaemonLogger(logPath)
		p.loggerOwned = p.logger != nil
	}
	p.workers = make([]*Worker, 0, p.maxWorkers)

	// Initialize workers
	for i := 0; i < p.maxWorkers; i++ {
		w := NewWorker(i+1, p.cliPath, p.timeout)
		w.queue = queue
		w.logger = p.logger
		// 将 pool 持有的 registry 注入到每个 worker，使其能在任务终态时记录到注册表
		w.registry = p.registry
		w.events = p.events
		// 桶 X3 病灶3：转发沙箱管理器到每个 worker
		// pool.sandboxMgr==nil 时不应用 worker 外层沙箱
		w.SetSandboxManager(p.sandboxMgr, p.sandboxLevel)
		p.workers = append(p.workers, w)
	}
	p.mu.Unlock()

	// Start scheduling loop
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.scheduleLoop()
	}()
}

// SetSandboxManager 注入沙箱管理器到 pool（桶 X3 病灶3 接入点）。
//
// 必须在 Start 之前调用，以确保 pool.Start 创建每个 worker 时 sandboxMgr 已就位。
// daemon 启动时由 Daemon.SetSandboxManager 注入到 pool，pool 再转发到每个 worker。
//
// 参数：
// - mgr: 沙箱管理器实例（nil=不应用 worker 外层沙箱）
// - level: 默认沙箱级别（SandboxLevelDisabled=跳过沙箱）
func (p *WorkerPool) SetSandboxManager(mgr sandbox.Manager, level sandbox.SandboxLevel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sandboxMgr = mgr
	p.sandboxLevel = level
	// 若 pool 已启动（workers 已创建），同步注入到现有 worker 实例
	for _, w := range p.workers {
		w.SetSandboxManager(mgr, level)
	}
}

// SetLogger sets logger
func (p *WorkerPool) SetLogger(logger *DaemonLogger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logger = logger
}

// SetRegistry 注入 worker 注册表，使 pool 启动时创建的每个 worker 都能引用注册表，
// 以便任务完成后调用 RecordCompletedTask 记录终态任务。
// 必须在 Start 之前调用，以确保 worker 创建时 registry 已就位。
func (p *WorkerPool) SetRegistry(registry *WorkerRegistry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry = registry
}

// SetEventBus injects the daemon event bus into workers.
func (p *WorkerPool) SetEventBus(events *EventBus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = events
}

// scheduleLoop scheduling loop: event-driven + timed polling
func (p *WorkerPool) scheduleLoop() {
	ticker := time.NewTicker(2 * time.Second) // Reduced-frequency polling as fallback
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.queue.Notifications(): // Event-driven: schedule immediately when new task arrives
			p.dispatchAvailable()
		case <-ticker.C: // Timed polling fallback
			p.syncRegistry()
			p.dispatchAvailable()
		}
	}
}

func (p *WorkerPool) dispatchAvailable() {
	for p.dispatchOne() {
	}
}

// dispatchOne tries to assign a pending task to an idle worker
func (p *WorkerPool) dispatchOne() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Find an idle worker
	var idleWorker *Worker
	for _, w := range p.workers {
		if !w.IsBusy() {
			idleWorker = w
			break
		}
	}
	if idleWorker == nil {
		return false
	}

	// Claiming and marking running is one queue operation, so cancellation
	// cannot race between selection and the state transition.
	nextTask, err := p.queue.ClaimNextPersisted()
	if err != nil {
		if p.logger != nil {
			p.logger.Error("Failed to claim next task: %v", err)
		}
		return false
	}
	if nextTask == nil {
		return false
	}

	// Assign to worker for execution
	if err := idleWorker.Start(p.ctx, nextTask); err != nil {
		// Assignment failed, rollback status
		_, _ = p.queue.RequeueRunning(nextTask.ID)
		if p.logger != nil {
			p.logger.Error("Failed to start worker: %v", err)
		}
		return false
	}
	if p.registry != nil {
		workerID := fmt.Sprintf("worker-%d", idleWorker.ID())
		_ = p.registry.Heartbeat(workerID)
		_ = p.registry.SetStatus(workerID, "busy")
		_ = p.registry.SetCurrentTask(workerID, nextTask.ID)
	}
	if p.events != nil {
		p.events.Publish(Event{Type: EventTaskStarted, Payload: map[string]interface{}{"task_id": nextTask.ID, "worker_id": idleWorker.ID()}})
	}

	if p.logger != nil {
		p.logger.Info("Worker %d started task %s", idleWorker.ID(), nextTask.ID)
	}
	return true
}

func (p *WorkerPool) syncRegistry() {
	p.mu.RLock()
	workers := append([]*Worker(nil), p.workers...)
	registry := p.registry
	p.mu.RUnlock()
	if registry == nil {
		return
	}
	for _, worker := range workers {
		workerID := fmt.Sprintf("worker-%d", worker.ID())
		_ = registry.Heartbeat(workerID)
		if task := worker.CurrentTask(); task != nil {
			_ = registry.SetStatus(workerID, "busy")
			_ = registry.SetCurrentTask(workerID, task.ID)
		} else {
			_ = registry.SetStatus(workerID, "idle")
			_ = registry.SetCurrentTask(workerID, "")
		}
	}
}

// CancelTask terminates the worker executing taskID. Pending tasks have no
// worker and return false.
func (p *WorkerPool) CancelTask(taskID string) bool {
	p.mu.RLock()
	workers := append([]*Worker(nil), p.workers...)
	p.mu.RUnlock()
	for _, worker := range workers {
		current := worker.CurrentTask()
		if current == nil || current.ID != taskID {
			continue
		}
		worker.Stop()
		return true
	}
	return false
}

// workerGracefulTimeout 是 worker 优雅退出的最大等待时间（3s）。
// daemon 收到取消信号后给 worker 3s 退出时间，超时后强制 kill。
// 设计依据：3s worker 退出 + < 1s 其他清理 < signal 的 5s 强制退出超时。
const workerGracefulTimeout = 3 * time.Second

// Stop 停止所有 workers，在 3s 内优雅退出，超时后强制 kill 仍在执行的 worker。
// 协调退出超时：signal 的 shutdownTimeout 为 5s，daemon 的 worker pool
// 在收到取消后给 worker 3s 退出时间，3s 后强制杀死仍在执行的 worker。
// 这样 daemon 在 signal 的 5s 强制退出之前完成优雅退出。
func (p *WorkerPool) Stop() {
	p.stopWithTimeout(workerGracefulTimeout)
}

// stopWithTimeout 取消 pool 上下文并等待 workers 优雅退出，超时后强制 kill。
// 调用 p.cancel() 后，所有 worker 的 taskCtx 被取消，waitTask goroutine
// 会检测到 ctx.Done() 并 kill 进程树、等待 cmd.Wait() 返回（最多 2s）。
// 本方法轮询 worker busy 状态，全部 idle 则提前返回。
func (p *WorkerPool) stopWithTimeout(timeout time.Duration) {
	p.mu.Lock()
	if p.cancel == nil {
		p.mu.Unlock()
		return // pool 未启动
	}
	cancel := p.cancel
	p.cancel = nil
	workers := append([]*Worker(nil), p.workers...)
	logger := p.logger
	loggerOwned := p.loggerOwned
	p.loggerOwned = false
	p.mu.Unlock()

	// Stop the scheduler before waiting on workers so no task can be assigned
	// after shutdown begins.
	cancel()
	p.wg.Wait()

	// 等待 workers 优雅退出（轮询 busy 状态）
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allIdle := true
		for _, w := range workers {
			if w.IsBusy() {
				allIdle = false
				break
			}
		}
		if allIdle {
			if loggerOwned && logger != nil {
				_ = logger.Close()
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 超时后强制 kill 仍在执行的 workers
	for _, w := range workers {
		w.Stop()
	}
	// waitTask has a one-second post-kill guard. Wait beyond that guard so no
	// worker can persist state or write logs after Daemon.Stop returns.
	forceDeadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(forceDeadline) {
		allIdle := true
		for _, w := range workers {
			if w.IsBusy() {
				allIdle = false
				break
			}
		}
		if allIdle {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if loggerOwned && logger != nil {
		_ = logger.Close()
	}
}

// RunningCount returns currently running worker count
func (p *WorkerPool) RunningCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, w := range p.workers {
		if w.IsBusy() {
			count++
		}
	}
	return count
}

// Workers returns status info of all workers
func (p *WorkerPool) Workers() []WorkerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]WorkerInfo, len(p.workers))
	for i, w := range p.workers {
		result[i].ID = w.ID()
		result[i].Type = w.Type()
		result[i].Busy = w.IsBusy()
		if t := w.CurrentTask(); t != nil {
			result[i].TaskID = t.ID
		}
	}
	return result
}

// WorkerInfo worker status info
type WorkerInfo struct {
	ID     int        `json:"id"`
	Type   WorkerType `json:"type"`
	Busy   bool       `json:"busy"`
	TaskID string     `json:"task_id,omitempty"`
}
