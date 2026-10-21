// Package tools - background task management
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/safebuf"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/tools/powershell"
	"github.com/anchorandfish/whale-code/internal/util"
)

// BackgroundTask background task
type BackgroundTask struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	Description string `json:"description"`
	Status      string `json:"status"` // "running" | "completed" | "failed"
	Output      string `json:"output,omitempty"`
	ResultType  string `json:"result_type,omitempty"` // type name of the result for deserialization
	// FailureKind（G2，2026-09-15 · O-T 轮）：**结构化**的失败归类，与工具结果的
	// tools.Result.FailureKind 同域同语义（tools.go 的 FailureKind / failureKindSpecs 为唯一真相源）。
	//
	// 为什么需要它：executeTask 的每条拒绝路径原先只写 `Status="failed" + Output=人类可读文案`，
	// 上层要判断"是不是沙箱拒绝"只能去 grep 散文（`output 包含 'sandbox'/'denied'/'blocked'`
	// —— task_sandbox_test.go 当时就是这么断言的）。族D-2：结构化事实不得由散文关键词断言。
	// 空值 = 非拒绝型失败（命令自己非零退出 / 用户 Stop / 启动失败等）。
	FailureKind FailureKind `json:"failure_kind,omitempty"`
	StartTime   time.Time   `json:"start_time"`
	EndTime     time.Time   `json:"end_time,omitempty"`
	// TimeoutSeconds 是本次任务的**生效**超时秒数（，2026-09-20 批 D）。
	//
	// 为什么需要它：修复前后台任务「不填 timeout」= `WithCancel(Background())` = 永不超时，
	// 且填了也没有上界。生效值原是**不可观测的**（只能读代码）⇒ 判据不可判定。
	// 本字段把它变成结构化可观测量（与 FailureKind 同型：结构化事实不走散文）。
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	cancel         context.CancelFunc

	// C11: cmd 是通过进程组启动的命令实例，供 Stop 终止整个进程树。
	// cmdMu 单独保护 cmd 字段（executeTask 与 Stop 跨协程访问）。
	cmd   *exec.Cmd
	cmdMu sync.Mutex
}

// snapshotBackgroundTask returns a detached view of task state. Callers must
// hold TaskManager.mu while snapshotting a task owned by the manager.
func snapshotBackgroundTask(task *BackgroundTask) *BackgroundTask {
	return &BackgroundTask{
		ID:             task.ID,
		Command:        task.Command,
		Description:    task.Description,
		Status:         task.Status,
		Output:         task.Output,
		ResultType:     task.ResultType,
		FailureKind:    task.FailureKind,
		StartTime:      task.StartTime,
		EndTime:        task.EndTime,
		TimeoutSeconds: task.TimeoutSeconds,
	}
}

// TaskManager background taskmanager
type TaskManager struct {
	mu     sync.RWMutex
	tasks  map[string]*BackgroundTask
	nextID atomic.Int64
	// executor 是关联的 Executor 实例（C-P0-1 修复），用于复用 prepareSandbox 沙箱接入逻辑。
	// nil 时 executeTask 退化为仅进程组隔离（向后兼容）。
	// 通过 SetExecutor 两阶段初始化注入（避免 Executor 构造时的循环依赖）。
	executor *Executor
}

// NewTaskManager createtaskmanager
func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks: make(map[string]*BackgroundTask),
	}
}

// SetExecutor 注入 Executor 引用（C-P0-1 两阶段初始化）。
//
// 在 NewExecutor 末尾调用，使 TaskManager 能复用 Executor.prepareSandbox
// 接入完整沙箱链路（prepareSandbox → Restrict → Start → PostStartHook）。
// nil 时 executeTask 退化为仅进程组隔离（向后兼容）。
func (tm *TaskManager) SetExecutor(e *Executor) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.executor = e
}

// Create creates a background task and returns its initial detached snapshot.
// Use Get or List to observe subsequent state changes.
func (tm *TaskManager) Create(command, description string, timeout int) *BackgroundTask {
	id := fmt.Sprintf("task_%d", tm.nextID.Add(1))
	// 默认值兜底 ＋ 上界 clamp（单一声明在 limits.go 的 ClampTimeoutSeconds）。
	// 修复前：`timeout <= 0` ⇒ WithCancel(Background()) = **永不过期**；
	// 且无上界 ⇒ `Create(..., 36000)` 原样生效 10 小时。
	timeoutSeconds := ClampTimeoutSeconds(timeout, DefaultBackgroundTimeoutSeconds, MaxBackgroundTimeoutSeconds)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)

	task := &BackgroundTask{
		ID:             id,
		Command:        command,
		Description:    description,
		Status:         "running",
		StartTime:      time.Now(),
		TimeoutSeconds: timeoutSeconds,
		cancel:         cancel,
	}

	tm.mu.Lock()
	tm.tasks[id] = task
	snapshot := snapshotBackgroundTask(task)
	tm.mu.Unlock()

	// asynchronousexecutecommand
	go tm.executeTask(ctx, task)

	return snapshot
}

// failTaskLocked 把任务置为失败态并落**结构化** FailureKind（调用方须已持 tm.mu）。
//
// G2（2026-09-15 · O-T 轮）：本函数是 executeTask 的**唯一失败出口**。修复前每条拒绝路径
// 各自 `task.Status="failed" + task.Output=…` 直写状态机，失败归类只存在于人类可读文案里，
// 上层无法在不解析散文的前提下区分「沙箱拒绝」与「命令自己失败」。
// 与 tools.Result.FailureKind 同域同语义（tools.go 的 FailureKind / failureKindSpecs 是唯一真相源）；
// 与 agent_task.go:389 的 C2 做法（`out.FailureKind = tf.Kind`）同形。
//
// 守卫语义与各调用点原实现**逐字一致**：任务已不在 running（被 Stop / cancel 抢先）时不覆盖其终局状态。
func (tm *TaskManager) failTaskLocked(task *BackgroundTask, kind FailureKind, format string, args ...any) {
	if task.Status != "running" {
		return
	}
	task.Status = "failed"
	task.Output = truncateTaskOutput(fmt.Sprintf(format, args...))
	task.FailureKind = kind
	task.EndTime = time.Now()
}

// truncateTaskOutput 按 env 派生的 SSOT（WHALE_TASK_MAX_OUTPUT）截断后台任务输出。
//
// 修复前 `ToolLimits.TaskMaxOutput` **零消费**（`LoadFromEnv` 全仓零调用）⇒
// 该 env 设了不生效。本函数是它的**唯一消费点**（executeTask 出口 ＋ failTaskLocked）。
// 默认 30000 字符（DefaultTaskMaxOutput），env 超过 1000000 被 clamp。
func truncateTaskOutput(s string) string {
	max := GetOutputLimits().TaskMaxOutput
	if max <= 0 || len(s) <= max {
		return s
	}
	// 收口：走唯一权威 TruncateTail（真实丢弃量标记）。
	return TruncateTail(s, max)
}

// executeTask executebackground task
//
// C11: 通过 sandbox.StartCommandWithProcessGroup 启动命令（新进程组），
// 使 Stop / 超时可经 sandbox.KillProcessGroup 终止整个进程树，
// 避免子进程（如 sh -c 启动的 sleep）被孤立。
//
// C-P0-1 修复：接入完整沙箱链路（prepareSandbox → Restrict → Start → PostStartHook）。
// 修复前：executeTask 仅调 StartCommandWithProcessGroup，跳过 prepareSandbox/OSSandbox.Restrict/PostStartHook，
// 后台任务可绕过沙箱限制（deny 命令、OS 级隔离、资源限制）。
// 修复后：复用 Executor.prepareSandbox，与 bashHandler 保持一致的沙箱行为。
func (tm *TaskManager) executeTask(ctx context.Context, task *BackgroundTask) {
	tm.mu.RLock()
	executor := tm.executor
	tm.mu.RUnlock()

	// R4: PowerShell 安全检查（fail-closed，Critical 命中拒绝执行）
	if runtime.GOOS == "windows" {
		if violations := powershell.RunSecurityChecksShortCircuit(task.Command); len(violations) > 0 {
			tm.mu.Lock()
			defer tm.mu.Unlock()
			// G2：安全策略拒绝 ⇒ policy_denied（与权限层同域语义）。
			tm.failTaskLocked(task, FailureKindPolicyDenied,
				"powershell security blocked: %s", violations[0].Message)
			return
		}
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", task.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", task.Command)
	}

	// 手动捕获合并输出（替代 CombinedOutput，以便使用进程组启动）
	var buf safebuf.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// C-P0-1：接入沙箱链路（prepareSandbox → Restrict → Start → PostStartHook）
	var sbCtx sandbox.SandboxContext
	var cleanup func()
	var sandboxed bool
	if executor != nil {
		var sandboxErr error
		sbCtx, cleanup, sandboxed, sandboxErr = executor.prepareSandbox(ctx, task.Command, executor.workDir)
		if sandboxErr != nil {
			if cleanup != nil {
				cleanup()
			}
			tm.mu.Lock()
			defer tm.mu.Unlock()
			// G2（§2.7）：沙箱拒绝 ⇒ sandbox_denied（原先只写散文，上层靠 grep 文案判断）。
			tm.failTaskLocked(task, FailureKindSandboxDenied, "sandbox denied: %v", sandboxErr)
			return
		}
		// 沙箱上下文生效 — 设置工作目录、环境变量
		if sandboxed {
			if sbCtx.WorkDir != "" {
				cmd.Dir = sbCtx.WorkDir
			}
			if len(sbCtx.Env) > 0 {
				cmd.Env = sbCtx.Env
			}
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	// C-P0-1：应用 OS 级隔离（fail-closed，与 bashHandler 一致）
	if sandboxed {
		if osSb := sbCtx.OSSandbox(); osSb != nil {
			if restrictErr := osSb.Restrict(cmd); restrictErr != nil {
				tm.mu.Lock()
				defer tm.mu.Unlock()
				// G2：OS 级隔离施加失败 = 沙箱层拒绝放行（fail-closed，与 bashHandler 一致）。
				tm.failTaskLocked(task, FailureKindSandboxDenied,
					"sandbox: OS restrict failed: %v", restrictErr)
				return
			}
		}
	}

	// C11: 进程组启动
	startErr := sandbox.StartCommandWithProcessGroupContext(ctx, cmd)
	if startErr != nil {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		// G2：**非拒绝型**失败（命令没能起来），故 kind 留空 —— 有意区别于沙箱/策略拒绝，
		// 免得上层把它当"这条路本来就不允许"而终结本轮。
		tm.failTaskLocked(task, "", "command start failed: %v", startErr)
		return
	}

	// C-P0-1：Start 后调用 PostStartHook（Windows Job Object AssignProcess）。
	// 失败时 kill 进程并标记任务失败（fail-closed），避免进程在 Job 外运行无资源限制。
	if sandboxed && sbCtx.PostStartHook != nil {
		if hookErr := sbCtx.PostStartHook(cmd); hookErr != nil {
			_ = sandbox.KillProcessGroup(cmd)
			_ = cmd.Wait()
			tm.mu.Lock()
			defer tm.mu.Unlock()
			// G2：进程已在 Job/资源限制之外运行 ⇒ 沙箱层拒绝放行，kill 后按 sandbox_denied 记账。
			tm.failTaskLocked(task, FailureKindSandboxDenied,
				"sandbox: post-start hook failed: %v", hookErr)
			return
		}
	}

	// C-P0-1：应用资源限制（best-effort，不阻断执行，与 bashHandler 一致）
	if sandboxed && sbCtx.ResourceLimits != (sandbox.ResourceLimits{}) && executor != nil {
		_ = sandbox.ApplyResourceLimits(cmd, sbCtx.ResourceLimits, executor.getAuditLog())
	}

	// 记录 cmd 供 Stop 终止进程组
	task.cmdMu.Lock()
	task.cmd = cmd
	task.cmdMu.Unlock()

	// 与 bash.go 共用同一单一实现 —— ctx 到期/取消时**先杀整个进程组**再收尾。
	// 修复前此处是裸 `cmd.Wait()`：ctx 只杀 `sh` 组长，管道孙进程持有 stdout 写端 ⇒ Wait 挂住
	// ⇒ Stop() 与超时都要等命令自然结束才真正"生效"。
	// （cmd 由上面的 StartCommandWithProcessGroup 启动 ⇒ 独立进程组已武装。）
	_, err := sandbox.WaitProcessGroupWithContext(ctx, cmd)

	// 清理 cmd 引用（进程已退出）
	task.cmdMu.Lock()
	task.cmd = nil
	task.cmdMu.Unlock()

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// If the task was already stopped by Stop(), don't overwrite its state.
	// This prevents the race where Stop() sets "failed" + "task stopped by user"
	// and then executeTask overwrites it with the command output.
	if task.Status != "running" {
		return
	}

	if ctx.Err() == context.Canceled {
		task.Status = "failed"
		task.Output = "taskcancelled"
	} else if err != nil {
		task.Status = "failed"
		task.Output = truncateTaskOutput(buf.String() + "\n" + err.Error())
	} else {
		task.Status = "completed"
		task.Output = truncateTaskOutput(buf.String())
	}
	task.EndTime = time.Now()
}

// List lists detached snapshots of tasks matching status.
func (tm *TaskManager) List(status string) []*BackgroundTask {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var result []*BackgroundTask
	for _, task := range tm.tasks {
		if status == "" || status == "all" || task.Status == status {
			result = append(result, snapshotBackgroundTask(task))
		}
	}
	return result
}

// Stop stoptask
//
// C11: 先通过 sandbox.KillProcessGroup 终止整个进程树（含子进程），
// 再取消上下文并更新状态。tm.mu 持有期间 executeTask 的状态写入会阻塞，
// 释放后 executeTask 检测到 Status != "running" 即跳过覆盖（既有竞态守卫）。
func (tm *TaskManager) Stop(id string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, ok := tm.tasks[id]
	if !ok {
		return fmt.Errorf("task %s does not exist", id)
	}
	if task.Status != "running" {
		return fmt.Errorf("task %s is in %s state, cannot stop", id, task.Status)
	}

	// C11: 终止进程树（幂等：cmd 为 nil 或已退出时返回 nil）
	task.cmdMu.Lock()
	cmd := task.cmd
	task.cmdMu.Unlock()
	if cmd != nil {
		_ = sandbox.KillProcessGroup(cmd)
	}

	task.cancel()
	task.Status = "failed"
	task.Output = "task stopped by user"
	task.EndTime = time.Now()
	return nil
}

// Get fetches a detached snapshot of a task.
func (tm *TaskManager) Get(id string) (*BackgroundTask, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	task, ok := tm.tasks[id]
	if !ok {
		return nil, false
	}
	return snapshotBackgroundTask(task), true
}

func (tm *TaskManager) CleanCompleted() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	count := 0
	for id, task := range tm.tasks {
		if task.Status == "completed" || task.Status == "failed" {
			delete(tm.tasks, id)
			count++
		}
	}
	return count
}

// taskCreateHandler TaskCreate toolhandler
func (e *Executor) taskCreateHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Command     string `json:"command"`
			Description string `json:"description"`
			Timeout     int    `json:"timeout"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.Command == "" {
			return "", fmt.Errorf("command cannot be empty")
		}

		task := e.TaskMgr.Create(input.Command, input.Description, input.Timeout)
		result, _ := json.Marshal(map[string]string{
			"task_id": task.ID,
			"status":  task.Status,
		})
		return string(result), nil
	}
}

// taskListHandler TaskList toolhandler
func (e *Executor) taskListHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			Status string `json:"status"`
		}
		parseInput(raw, &input)

		tasks := e.TaskMgr.List(input.Status)
		if len(tasks) == 0 {
			return util.Bi("No background tasks", "没有后台任务"), nil
		}

		var results []string
		for _, t := range tasks {
			duration := ""
			if !t.EndTime.IsZero() {
				duration = fmt.Sprintf(" (duration %v)", t.EndTime.Sub(t.StartTime).Round(time.Second))
			}
			// G2：结构化失败归类的**出口** —— 任务失败前列表里没有任何原因（只有 command/description），
			// 模型无从知道"这条是被沙箱拒了、重试没用"。仅在非空时追加，非拒绝型失败文案不变。
			kind := ""
			if t.FailureKind != "" {
				kind = fmt.Sprintf(" failure_kind=%s", t.FailureKind)
			}
			results = append(results, fmt.Sprintf("%s [%s] %s%s%s — %s",
				t.ID, t.Status, t.Command, duration, kind, t.Description))
		}
		return fmt.Sprintf("background task list (%d):\n%s", len(tasks), joinLines(results)), nil
	}
}

// taskStopHandler TaskStop toolhandler
func (e *Executor) taskStopHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			TaskID string `json:"task_id"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.TaskID == "" {
			return "", fmt.Errorf("task_id cannot be empty")
		}

		if err := e.TaskMgr.Stop(input.TaskID); err != nil {
			return "", err
		}
		return fmt.Sprintf("task %s stopped", input.TaskID), nil
	}
}

// taskOutputHandler TaskOutput toolhandler（，2026-10-09）
//
// 补的是「交付」这一环：`BackgroundTask.Output` 早已采集并经 truncateTaskOutput 截断
// （的唯一 SSOT 在本文件 :152），但此前**没有任何工具面出口** ⇒ 模型被 sleep 门
// 推进 `run_in_background` 后，stdout 事实上不可达（TaskList 只给
// `task_1 [completed] … (duration 5s)`），于是出现「模型凭空断言输出为 hi」
// （2026-10-09 18:07 用户贴转录直报：机制上它不可能知道，碰巧猜对）。
//
// 方向 = **拉**（不推）。对齐 同类 CLI `write_stdin` 的"按 id 拉增量"
// （`core/src/tools/handlers/shell_spec.rs:110-152`：空 chars = 纯轮询）。
// whale 的后台命令是**一次性进程**（非交互会话）⇒ 不需要 stdin / yield_time_ms，
// `task_id → 已采集 Output` 即最小完备。
//
// ★ 权限面（方案对照 §0′ 第 2 条要求显式论证，不得默认）：
// 声明 LevelReadOnly，论据是「任务在 Create 时**已过**权限审批（TaskCreate 声明
// LevelWorkspaceWrite），读其**已采集的**输出不新增越权面」—— 与 同类 CLI write_stdin
// 跟随**已批准会话**同构。同理本工具在能力分桶（internal/permission/capability.go）
// 登记为 CapMeta（控制面读回，与 tasklist 同桶），**不**登记 CapRead。
//
// ★ 非阻塞：TaskManager.Get 只是一次 map 读 + 浅拷贝快照，**不等待任务结束**。
// running 任务返回"当前已有" —— executeTask 在**终局**才落 Output（本文件 :307/:310），
// 故 running 时通常为空；这条**如实**写进回执，不假装有内容。
func (e *Executor) taskOutputHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			TaskID string `json:"task_id"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.TaskID == "" {
			return "", fmt.Errorf("task_id cannot be empty")
		}

		task, ok := e.TaskMgr.Get(input.TaskID)
		if !ok {
			return "", fmt.Errorf("task %s does not exist", input.TaskID)
		}

		// 截断仍走 的唯一 SSOT（truncateTaskOutput）—— 本处**不得**另设上限，
		// 否则「声明的界」与「实际采集的界」会出现第二份真相源。
		output := truncateTaskOutput(task.Output)

		head := fmt.Sprintf("task %s [%s]\ncommand: %s\n", task.ID, task.Status, task.Command)
		if task.Status == "running" {
			head += "note: still running — background output is recorded only when the task finishes\n"
		}
		if output == "" {
			return head + "output: (empty)", nil
		}
		return fmt.Sprintf("%soutput (%d chars):\n%s", head, len(output), output), nil
	}
}

// joinLines joins multiple lines of text
func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}
