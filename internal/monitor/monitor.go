// Package monitor - background process monitoring manager
// implements a peer-CLI-style Monitor: starts background process, streams stdout，
// notifies AI when process ends. Used to replace sleep polling mode。
package monitor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/security"
)

// MonitorStatus monitoringprocessstate
type MonitorStatus string

const (
	StatusRunning  MonitorStatus = "running"
	StatusStopped  MonitorStatus = "stopped"
	StatusFinished MonitorStatus = "finished"
	StatusFailed   MonitorStatus = "failed"
)

// Process represents a monitored background process
type Process struct {
	ID          string
	Command     string
	Description string
	Status      MonitorStatus
	ExitCode    int
	StartTime   time.Time
	Output      []string
	Lines       int
	cancel      context.CancelFunc
	cmd         *exec.Cmd
	mu          sync.Mutex
	maxLines    int
	// cleanup 是沙箱资源清理函数（C-P0-2 修复），进程退出后在 waitProcess 中调用。
	cleanup func()
}

// GetOutput fetchprocessoutput（thread-safe）
func (p *Process) GetOutput() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return joinLines(p.Output)
}

// AddLine adds an output line (thread-safe), discards oldest lines when exceeding maxLines
func (p *Process) AddLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	max := p.maxLines
	if max <= 0 {
		max = DefaultMaxLines
	}
	if len(p.Output) >= max {
		discard := len(p.Output) - max + 1
		p.Output = p.Output[discard:]
	}
	p.Output = append(p.Output, line)
	p.Lines++
}

// GetLines fetches output line count (thread-safe)
func (p *Process) GetLines() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Lines
}

// GetStatus fetchprocessstate（thread-safe）
func (p *Process) GetStatus() MonitorStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Status
}

// GetExitCode fetchprocessexitcode（thread-safe）。
//
// ★ ：新增用途 —— 模型向读回入口 MonitorTail 要如实报告
// "进程怎么结束的"（finished vs failed + 退出码）。ExitCode 由 waitProcess 在
// **持 p.mu** 时写入，故读取也必须持锁（与 GetStatus/GetLines 同型）；
// 不加这个访问器而直接读字段，会在并发读（读回工具 vs 进程收尾）时构成 data race。
func (p *Process) GetExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ExitCode
}

// WaitForStatus 轮询等待进程达到目标状态，超时返回 false
// 用于替代固定 time.Sleep，避免因 PowerShell/cmd 启动延迟导致的时序竞态
// 内部固定 50ms 轮询间隔，timeout 为总超时时间
func (p *Process) WaitForStatus(target MonitorStatus, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.GetStatus() == target {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return p.GetStatus() == target
}

// WaitForDone 轮询等待进程结束（status 不再为 running），超时返回 false
// 进程结束后状态可能为 finished/failed/stopped，均视为已结束
func (p *Process) WaitForDone(timeout time.Duration) (MonitorStatus, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := p.GetStatus()
		if status != StatusRunning {
			return status, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	finalStatus := p.GetStatus()
	return finalStatus, finalStatus != StatusRunning
}

// OutputCallback outputcallbackfunctiontype
type OutputCallback func(monitorID, line string)

// DoneCallback is the completion callback function type
type DoneCallback func(monitorID string, status MonitorStatus, exitCode int, output string)

// DefaultInterval is the default collection interval
const DefaultInterval = 5 * time.Second

// DefaultMaxLines is the default maximum output lines per process
const DefaultMaxLines = 10000

// DefaultMaxRetainedFinished 是**已完成**监控记录的保留上限（B1，2026-09-30）。
//
// 病灶：Manager.processes 只增不减 —— waitProcess 从不 delete，Remove() 全仓零生产调用
// ⇒ 已完成记录无界累积，每条最多 DefaultMaxLines(10000) 行输出常驻内存。
//
// 为什么是「有界保留」而**不是**「结束即删」：`/monitors tail <id>` 是监控输出**唯一**的
// 读取出口（A-2 之后输出不再进对话流），而 done 回执恰好提示 `tail <id>`
// ⇒ 结束即删会让「刚结束」的进程立刻不可 tail、回执指向不存在的目标 = 用户可见退化。
// 病根是「保留无界」，所以这里只给它一个**界**；淘汰**最旧的**。
//
// 形态对齐竞品：同类 CLI 的常驻 pill 只数 running，历史留在按需对话框（BackgroundTasksDialog）；
// 同类 CLI 的 footer 只列 running terminals（进程结束即 retain 移除，因其输出已进对话流）。
//
// 淘汰依据：Process.StartTime（buildAndStartProcess 里 time.Now() 设置，见下方）。
const DefaultMaxRetainedFinished = 10

// DefaultMaxConcurrency 默认最大并发进程数（新增）
// 平衡资源占用与并发吞吐：10 进程 × ~30MB RSS = ~300MB 上限
const DefaultMaxConcurrency = 10

// minMaxConcurrency 最小并发上限守卫（新增）
// 防止 WithMaxConcurrency(0) 导致无缓冲通道 Start 永久阻塞
const minMaxConcurrency = 1

// ErrConcurrencyLimit 并发上限已达上限，TryStart/TryAcquire 失败时返回（新增）
var ErrConcurrencyLimit = errors.New("monitor: concurrency limit reached")

// Manager monitoringprocessmanager
type Manager struct {
	processes map[string]*Process
	mu        sync.RWMutex
	nextID    atomic.Int64
	onOutput  OutputCallback
	onDone    DoneCallback
	Interval  time.Duration

	// sem 信号量通道（新增）
	// 缓冲大小 = maxConcurrency，用作并发令牌桶
	// Start 时发送令牌（占位），waitProcess 时接收令牌（释放）
	sem chan struct{}
	// maxConcurrency 最大并发进程数（新增）
	maxConcurrency int
}

// ManagerOption 监控管理器配置选项（新增）
type ManagerOption func(*Manager)

// SandboxConfig 是传递给 monitor 的沙箱配置（C-P0-2 修复）。
//
// 由 tools 包的 monitorHandler 从 prepareSandbox 结果中提取并注入，
// 使后台监控进程接入完整沙箱链路（Restrict → Start → PostStartHook → cleanup）。
type SandboxConfig struct {
	// RestrictFunc 在 cmd.Start() 前调用，应用 OS 级隔离标志（SysProcAttr.Cloneflags / CreationFlags）。
	RestrictFunc func(*exec.Cmd) error
	// PostStartHook 在 cmd.Start() 后调用，应用 Job Object 等 post-start 操作。
	PostStartHook func(*exec.Cmd) error
	// Cleanup 在进程退出后调用，释放沙箱资源（如 Job Object handle）。
	Cleanup func()
	// WorkDir 沙箱工作目录（覆盖 cmd.Dir）。
	WorkDir string
	// Env 沙箱环境变量（覆盖 cmd.Env）。
	Env []string
}

// StartOption 是 Start 方法的可选参数（C-P0-2 修复，向后兼容）。
type StartOption func(*startConfig)

// startConfig 是 Start 方法的内部配置。
type startConfig struct {
	sandbox *SandboxConfig
	// userApproved 表示"本命令的沙箱可批准档已获用户**逐条**批准"（续）。
	// 只有工具层（monitorHandler）在 sandboxSanctioned(ctx) 为真时才会设置它。
	userApproved bool
}

// WithSandbox 设置沙箱配置（C-P0-2 修复）。
//
// 使 buildAndStartProcess 在启动前应用 OS 级隔离（Restrict），
// 启动后调用 PostStartHook，进程退出后调用 Cleanup。
func WithSandbox(cfg SandboxConfig) StartOption {
	return func(c *startConfig) {
		c.sandbox = &cfg
	}
}

// WithUserApproval 声明"本次命令的沙箱可批准档已获用户逐条批准"（续，2026-10-06）。
//
// 为什么需要它：Monitor 的执行链闸口本应与 bash **同源** —— 工具层 `monitorHandler`
// 在调用 `Start` 之前已经过 `prepareSandbox`，它用 **internal/sandbox 规则引擎**裁决，
// 并且认得用户的批准（`sandboxSanctioned(ctx)` ⇒ `ValidateSanctioned`）。
// 但 `Start` 内部**另挂着一道** `security.CheckCommand`（bash 的执行链没有这一道），
// 用的是**另一套引擎**：`$()` 命令替换在 sandbox 引擎里是**可批准档**（拍板），
// 在 security 引擎里仍是 L1 硬拒 ⇒ 用户按下允许后 Monitor 照样
// `startmonitoringfailed`，与 bash「批准即生效」**不同源**。
// 这正是用户 2026-10-05 报的"允许了还会被拒绝"在 Monitor 上仍未修掉的那一半
// （`docs/人工测试记录/沙箱限制太多对话.md` 断点处）。
//
// 语义边界（刻意收窄，缺一不可）：
// - **只由用户逐条批准驱动**：唯一设置点是 `monitorHandler`，
// 条件是 `sandboxSanctioned(ctx)` —— 它只由 `ExecuteWithConfirm` 的 Yes 分支
// 产生，且仅当 `sandbox.Classify(cmd) == TierConfirm`（见 sandbox_sanction.go）；
// - 非管线调用方（`TryStart`、Doctor 并发检测等）**不设**此选项 ⇒ 本层闸口原样保留；
// - 硬拒档（`rm/dd/mkfs/format`、反向 shell、监听态）在 `SandboxPrecheckStage`
// 已 `Proceed:false`，根本到不了 `Start`；`prepareSandbox` 还会再挡一次
// Critical 级逃逸/exfil 载体（bash.go:1012-1024）。
func WithUserApproval() StartOption {
	return func(c *startConfig) {
		c.userApproved = true
	}
}

// WithMaxConcurrency 设置最大并发进程数（新增）
// n <= 0 时守卫为 minMaxConcurrency（=1），避免无缓冲通道 Start 永久阻塞
// 默认值为 DefaultMaxConcurrency（=10）
func WithMaxConcurrency(n int) ManagerOption {
	return func(m *Manager) {
		if n < minMaxConcurrency {
			n = minMaxConcurrency
		}
		m.maxConcurrency = n
	}
}

// NewManager 创建监控进程管理器（改造为 variadic）
// opts 为可选配置，零参数时使用默认值（maxConcurrency=10）
// 向后兼容：现有调用方 monitor.NewManager() 零感知
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		processes:      make(map[string]*Process),
		Interval:       DefaultInterval,
		maxConcurrency: DefaultMaxConcurrency,
	}
	for _, opt := range opts {
		opt(m)
	}
	m.sem = make(chan struct{}, m.maxConcurrency)
	return m
}

// SetOutputCallback sets the output callback
func (m *Manager) SetOutputCallback(cb OutputCallback) {
	m.onOutput = cb
}

// SetDoneCallback sets the completion callback
func (m *Manager) SetDoneCallback(cb DoneCallback) {
	m.onDone = cb
}

// Start 启动监控进程，带命令安全校验和并发上限（改造 / C-P0-2 沙箱接入）
// 并发上限满时阻塞等待（默认 10，可配置 WithMaxConcurrency）
// 令牌释放在 waitProcess goroutine 末尾（cmd.Wait 返回后），保证进程结束立即归还配额
//
// C-P0-2 修复：opts 可传入 WithSandbox 配置，使后台监控进程接入完整沙箱链路。
//
// 落实优化 2：cmd.Start/StdoutPipe/StderrPipe 失败时显式释放令牌
func (m *Manager) Start(command, description, workDir string, opts ...StartOption) (*Process, error) {
	// C-P0-2：解析可选沙箱配置
	sc := &startConfig{}
	for _, opt := range opts {
		opt(sc)
	}

	// H2：委托 security.CheckCommand 做命令安全校验（02 §2.3.1 契约）。
	//
	// ★ P-014 A1：本层**只消费「命令本身不可执行」这一与询问无关的硬事实**
	// （`Decision.HardBlock()` = L1 注入/混淆 + L2 沙箱白黑名单/网络 + 判定链未就绪）。
	//
	// L3 的两档都不再是本层的拒绝依据：
	// - `Ask`（破坏性档）：**事实**，不是指令（见 `security.Action` 的语义说明）；
	// - 危险档 `Deny`：定义是「**强制确认**（永不免问）」，**不是硬拒绝**（`-R1`）——
	// 管线会先强制确认，用户批准后带 `Approved=true` 重跑并放行到本层；
	// 本层若在此再硬拒，等于**否掉用户的批准**，并与 `bash`（含 `run_in_background`）不同源。
	//
	// 修复前的形态：`Deny ⇒ 报错` ＋ `Ask ⇒ fail-closed 拒绝`。而管线对
	// `npm run dev` 这类命令判「沙箱内免问、直接放行」（`sandbox-bounded: OS enforcement active,
	// confirmation not required`）⇒ **那次"确认"永远不会发生** ⇒ `Ask` 分支永远命中 ⇒
	// Monitor 的主业（盯长跑进程）**恒定失败**，报错还指向一个不存在的动作（P-014 缺陷本体）。
	decision := security.CheckCommand(command)
	if decision.HardBlock() && !sc.userApproved {
		return nil, fmt.Errorf("command safety check failed: %w", decision.Err)
	}
	if decision.Blocked() || decision.NeedsConfirmation() {
		// 留痕：本层**没有**拒绝，只是把门报出的事实记下来（可观测性靠它）。
		// 归因用结构化字段（层/风险档），不用 Reason 散文反推（R4）。
		//
		// ★ 续：`sc.userApproved` 为真时本层**让路**（不拒绝），但仍照常留痕 ——
		// 让路的是"裁决权"，不是"可观测性"。理由见 `WithUserApproval` 的类型注释。
		log.Printf("[monitor] security 门报出处置事实（层=%s 风险档=%s userApproved=%v）：%s；"+
			"是否询问由管线裁决，本层不重复裁决", decision.DecidedBy, decision.Risk, sc.userApproved, decision.Reason)
	}

	// 阻塞获取信号量令牌（新增）
	m.sem <- struct{}{}

	// 落实优化 2：失败分支显式释放令牌
	return m.buildAndStartProcess(command, description, workDir, sc.sandbox)
}

// TryStart 非阻塞启动监控进程（新增）
// 并发上限满时立即返回 ErrConcurrencyLimit，不阻塞等待
// 适用于调用方需要感知降级场景（如 Doctor 并发检测）
func (m *Manager) TryStart(command, description, workDir string) (*Process, error) {
	// H2：委托 security.CheckCommand 进行命令安全校验。
	// ★ P-014 A1：与 Start **同形同源** —— 只消费 `HardBlock()`（L1/L2 硬拒绝），
	// L3 的 Ask / 危险档交由管线裁决（理由见 Start 处的长注释）。
	// 只改 Start 不改本函数 = "半修"，会把同一缺陷留在本实现里（双实现漂移）。
	decision := security.CheckCommand(command)
	if decision.HardBlock() {
		return nil, fmt.Errorf("command safety check failed: %w", decision.Err)
	}

	// 非阻塞获取信号量令牌（新增）
	select {
	case m.sem <- struct{}{}:
		// 获取成功
	default:
		return nil, ErrConcurrencyLimit
	}

	// 落实优化 2：失败分支显式释放令牌
	return m.buildAndStartProcess(command, description, workDir, nil)
}

// buildAndStartProcess 构建并启动进程（新增，Start/TryStart 共享逻辑 / C-P0-2 沙箱接入）
// 调用前必须已获取信号量令牌；失败时通过 defer 归还令牌
//
// C-P0-2 修复：sb 参数非 nil 时，在启动前应用 OS 级隔离（Restrict），
// 启动后调用 PostStartHook，进程退出后调用 Cleanup。
// nil 时退化为仅进程组隔离（向后兼容，使用 sandbox.StartCommandWithProcessGroup）。
func (m *Manager) buildAndStartProcess(command, description, workDir string, sb *SandboxConfig) (*Process, error) {
	// 落实优化 2：失败时通过 defer 释放令牌
	started := false
	defer func() {
		if !started {
			<-m.sem
		}
	}()

	id := fmt.Sprintf("mon_%d", m.nextID.Add(1))

	ctx, cancel := context.WithCancel(context.Background())

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	// C-P0-2：沙箱配置覆盖工作目录和环境变量
	if sb != nil {
		if sb.WorkDir != "" {
			cmd.Dir = sb.WorkDir
		} else if workDir != "" {
			cmd.Dir = workDir
		}
		if len(sb.Env) > 0 {
			cmd.Env = sb.Env
		}
	} else if workDir != "" {
		cmd.Dir = workDir
	}

	proc := &Process{
		ID:          id,
		Command:     command,
		Description: description,
		Status:      StatusRunning,
		StartTime:   time.Now(),
		Output:      make([]string, 0),
		cancel:      cancel,
		cmd:         cmd,
		// C-P0-2：存储 cleanup 函数，进程退出后由 waitProcess 调用
		cleanup: nil,
	}
	if sb != nil && sb.Cleanup != nil {
		proc.cleanup = sb.Cleanup
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cannot get stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cannot get stderr pipe: %w", err)
	}

	// C-P0-2：应用 OS 级隔离（fail-closed，与 bashHandler 一致）
	if sb != nil && sb.RestrictFunc != nil {
		if restrictErr := sb.RestrictFunc(cmd); restrictErr != nil {
			cancel()
			return nil, fmt.Errorf("sandbox: OS restrict failed: %w", restrictErr)
		}
	}

	// C-P0-2：使用进程组启动（替代 bare cmd.Start()，确保可终止整个进程树）
	if err := sandbox.StartCommandWithProcessGroup(cmd); err != nil {
		cancel()
		return nil, fmt.Errorf("cannot start process: %w", err)
	}

	// C-P0-2：Start 后调用 PostStartHook（Windows Job Object AssignProcess）。
	// 失败时 kill 进程并返回错误（fail-closed），避免进程在 Job 外运行无资源限制。
	if sb != nil && sb.PostStartHook != nil {
		if hookErr := sb.PostStartHook(cmd); hookErr != nil {
			_ = sandbox.KillProcessGroup(cmd)
			cancel()
			return nil, fmt.Errorf("sandbox: post-start hook failed: %w", hookErr)
		}
	}

	// 标记成功，defer 不再释放令牌（令牌由 waitProcess defer 释放）
	started = true

	m.mu.Lock()
	m.processes[id] = proc
	m.mu.Unlock()

	// read stdout
	go m.readStream(id, stdout, false)

	// read stderr
	go m.readStream(id, stderr, true)

	// wait for process to end
	go m.waitProcess(id, cmd, cancel)

	return proc, nil
}

// Acquire 阻塞获取一个信号量令牌（新增，C17 Doctor 复用）
// ctx 取消时返回 ctx.Err()，已获取的令牌不占用（select 守卫）
// 供 C17 Doctor 在 P2-G7 复用：Doctor 不启动子进程，仅用信号量限制并发检测数
func (m *Manager) Acquire(ctx context.Context) error {
	select {
	case m.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryAcquire 非阻塞获取一个信号量令牌（新增，C17 Doctor 复用）
// 失败返回 ErrConcurrencyLimit，供 C17 Doctor 探测可用配额
func (m *Manager) TryAcquire() error {
	select {
	case m.sem <- struct{}{}:
		return nil
	default:
		return ErrConcurrencyLimit
	}
}

// Release 释放一个信号量令牌（新增，C17 Doctor 复用）
// 供 C17 Doctor 在 P2-G7 复用：检测完成后归还令牌
// 注意：调用方需保证 Release 与 Acquire/TryAcquire 配对，否则会导致令牌泄漏或计数错乱
func (m *Manager) Release() {
	select {
	case <-m.sem:
		// 释放成功
	default:
		// 防御性：若无令牌可释放，不阻塞（避免 panic）
	}
}

// MaxConcurrency 返回当前最大并发上限（新增）
func (m *Manager) MaxConcurrency() int {
	return m.maxConcurrency
}

// readStream readprocessoutputflow
func (m *Manager) readStream(monitorID string, stream interface{ Read([]byte) (int, error) }, isStderr bool) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if isStderr {
			line = "[stderr] " + line
		}

		m.mu.RLock()
		proc, ok := m.processes[monitorID]
		m.mu.RUnlock()

		if !ok {
			return
		}

		proc.AddLine(line)

		if m.onOutput != nil {
			m.onOutput(monitorID, line)
		}
	}
}

// waitProcess 等待进程结束，更新状态并释放信号量令牌（改造）
// 落实优化 1：用 defer 兜底释放令牌，确保 panic 也能归还
func (m *Manager) waitProcess(monitorID string, cmd *exec.Cmd, cancel context.CancelFunc) {
	// 兜底释放令牌：无论正常返回还是 panic，都归还令牌（优化 1）
	defer func() {
		<-m.sem
	}()

	err := cmd.Wait()

	m.mu.RLock()
	proc, ok := m.processes[monitorID]
	m.mu.RUnlock()

	if !ok {
		return
	}

	// C-P0-2：进程退出后释放沙箱资源（如 Job Object handle）
	if proc.cleanup != nil {
		proc.cleanup()
	}

	proc.mu.Lock()
	// 状态守卫：若 Stop() 已将状态置为 StatusStopped，则不覆盖为 StatusFailed，
	// 避免 waitProcess 在 cmd.Wait() 返回 error 时误标主动停止的进程为失败
	if proc.Status == StatusStopped {
		proc.mu.Unlock()
		// B1：被 Stop 的进程同样是一条「已完成」记录，同样受保留上限约束。
		// （Stop 路径不调 onDone —— 用户主动停止的回执由命令面 `/monitors stop <id>` 给。）
		m.mu.Lock()
		m.pruneFinishedLocked()
		m.mu.Unlock()
		return
	}
	if err != nil {
		proc.Status = StatusFailed
		if exitErr, ok := err.(*exec.ExitError); ok {
			proc.ExitCode = exitErr.ExitCode()
		} else {
			proc.ExitCode = -1
		}
	} else {
		proc.Status = StatusFinished
		proc.ExitCode = 0
	}
	output := joinLines(proc.Output)
	status := proc.Status
	exitCode := proc.ExitCode
	proc.mu.Unlock()

	cancel()

	// B1：先做本地收尾（裁剪已完成记录），再把状态迁移通知外界。
	// 顺序有意如此：① onDone 可能**阻塞**（A-5：done 不得丢 ⇒ 阻塞发送 + 逃生口），
	// 裁剪不该被 TUI 的消费速度拖住；② 这个顺序让「收到 done」蕴含「裁剪已完成」，
	// 判据可以精确同步而不必靠 sleep 猜时机。
	m.mu.Lock()
	m.pruneFinishedLocked()
	m.mu.Unlock()

	if m.onDone != nil {
		m.onDone(monitorID, status, exitCode, output)
	}
}

// killTree 是「终止一个被监控进程」的**唯一实现**（双实现防线）。
//
// Stop（用户命令 `/monitors stop <id>`）与 StopAll（退出 whale 的清理路径）都薄调用它 ——
// 修复前两者各写各的：Stop 内联 KillProcessGroup（杀整棵树），StopAll 只 `cancel()`
// （exec.CommandContext 仅杀**直接子进程** sh）⇒ 同一语义两份实现、行为还不同，
// 退出 whale 时**孙进程成孤儿继续在用户机器上跑**（等价模型实测：复合命令
// `( while :; do echo tick >> hb; done ) & wait` 在 cancel 之后心跳仍在长）。
//
// 为什么必须组杀：Monitor 用 `sandbox.StartCommandWithProcessGroup` 启动（Setpgid），
// 真实长跑命令（`npm run dev` / `while` 循环 / `a & b`）都有孙进程；只杀组长等于没停。
// 形态对齐：同类 CLI `utils/pty/src/process_group.rs`（terminate/kill 整组）与本仓既有权威写法
// `sandbox/process_wait.go:61`（`cmd.Cancel ⇒ KillProcessGroup`）。
//
// 顺序有意如此：① 组杀（SIGKILL 整棵树）→ ② cancel()（释放 ctx 资源；组杀已生效时幂等 no-op）。
// ★ **不能**反过来只依赖 `cmd.Cancel`：waitProcess 是在 `cmd.Wait()` 返回**之后**才 cancel()，
// 那一刻 pid 已被回收，组杀会落在**可能被复用**的 pgid 上（危险）；组杀必须在进程仍存活时做。
//
// 调用方须已在 proc.mu 下把 Status 置为 StatusStopped（供 waitProcess 的状态守卫使用）；
// 本方法只取 p.mu 读一次不可变字段，不进行任何 m.mu 与 p.mu 的反向嵌套（锁序安全）。
func (p *Process) killTree() {
	p.mu.Lock()
	cmd := p.cmd
	cancel := p.cancel
	p.mu.Unlock()

	if cmd != nil {
		_ = sandbox.KillProcessGroup(cmd)
	}
	if cancel != nil {
		cancel()
	}
}

// Stop stops a monitoring process
func (m *Manager) Stop(id string) error {
	m.mu.RLock()
	proc, ok := m.processes[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("monitoring process %s does not exist", id)
	}

	proc.mu.Lock()
	if proc.Status != StatusRunning {
		proc.mu.Unlock()
		return fmt.Errorf("monitoring process %s is no longer running", id)
	}
	proc.Status = StatusStopped
	proc.mu.Unlock()

	// C-P0-2：使用进程组终止（替代仅 cancel），确保子进程也被终止。
	// 与 StopAll **同源** —— 「终止」只有 killTree 一份实现。
	proc.killTree()

	return nil
}

// StopAll stops all monitoring processes
//
// ★ 修复（2026-10-13，第一性原理 + 参考 同类 CLI）：修复前本函数只 `proc.cancel()` ——
// cancel 来自 `exec.CommandContext`，只杀**直接子进程**（`sh`），**杀不到孙进程** ⇒
// 退出 whale 时后台的真实长跑命令（`npm run dev` / `while` 循环 / `a & b`）成孤儿、
// 继续跑在用户机器上（判据 `stopall_process_group_test.go` 修复前实测红：心跳 8→21）。
// 现与 Stop **同源**：都薄调用 `proc.killTree()`（组杀整棵树）⇒ 「停止」只有一份实现。
func (m *Manager) StopAll() {
	// 先在锁内取快照、出锁后再逐台终止：killTree 走 syscall，不应在持 m.mu 时做。
	m.mu.RLock()
	procs := make([]*Process, 0, len(m.processes))
	for _, proc := range m.processes {
		procs = append(procs, proc)
	}
	m.mu.RUnlock()

	for _, proc := range procs {
		proc.mu.Lock()
		if proc.Status != StatusRunning {
			proc.mu.Unlock()
			continue
		}
		proc.Status = StatusStopped
		proc.mu.Unlock()

		proc.killTree()
	}
}

// Get fetchmonitoringprocessinfo
func (m *Manager) Get(id string) (*Process, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	proc, ok := m.processes[id]
	return proc, ok
}

// List lists all monitoring processes
func (m *Manager) List() []*Process {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Process, 0, len(m.processes))
	for _, proc := range m.processes {
		result = append(result, proc)
	}
	return result
}

// RunningCount returns the count of running monitoring processes
func (m *Manager) RunningCount() int {
	m.mu.RLock()
	list := make([]*Process, 0, len(m.processes))
	for _, proc := range m.processes {
		list = append(list, proc)
	}
	m.mu.RUnlock()

	count := 0
	for _, proc := range list {
		proc.mu.Lock()
		if proc.Status == StatusRunning {
			count++
		}
		proc.mu.Unlock()
	}
	return count
}

// Remove removes a single monitoring process record by id.
//
// ★ ：本函数**不是**「进程结束后的清理入口」，别把它接到 Stop()/waitProcess 里 ——
// 那会让 waitProcess 在 `if !ok { return }` 处提前返回、**跳过 proc.cleanup()**，泄漏沙箱资源。
// 结束后的自动裁剪走 pruneFinishedLocked（唯一调用点 = waitProcess 的两条终态路径）。
// 保留本函数是给「显式丢弃某条记录」用的；当前生产无调用点（Grep 核实）。
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processes, id)
}

// pruneFinishedLocked 把**已完成**的记录裁剪到 DefaultMaxRetainedFinished 以内，
// 淘汰最旧的（按 Process.StartTime，即启动顺序）。必须在持有 m.mu **写锁**时调用。
//
// ★ 调用点只有一处：waitProcess —— 两条终态路径（自然结束 / 被 Stop）都必须经过它。
// 为什么不能放在 Stop() 里删：Stop 只置状态并杀进程，真正的收尾在 waitProcess
// （cmd.Wait() 返回之后）。若 Stop 先删掉 map 条目，waitProcess 会在
// `if !ok { return }` 处**提前返回** ⇒ **跳过 proc.cleanup()** ⇒ 沙箱资源泄漏。
// 所以「移除」的唯一正确位置就在 waitProcess 内 —— 它是「进程已终结」的唯一汇聚点。
//
// 锁顺序：m.mu → p.mu。与既有代码一致（waitProcess / RunningCount 都是先释放 m.mu
// 再取 p.mu），全仓无 p.mu → m.mu 的反向嵌套，故不会死锁。
func (m *Manager) pruneFinishedLocked() {
	finished := make([]*Process, 0, len(m.processes))
	for _, p := range m.processes {
		if p.GetStatus() != StatusRunning {
			finished = append(finished, p)
		}
	}
	if len(finished) <= DefaultMaxRetainedFinished {
		return
	}
	sort.Slice(finished, func(i, j int) bool {
		return finished[i].StartTime.Before(finished[j].StartTime)
	})
	for _, p := range finished[:len(finished)-DefaultMaxRetainedFinished] {
		delete(m.processes, p.ID)
	}
}

// GetInterval returns the collection interval, using default if not set
func (m *Manager) GetInterval() time.Duration {
	if m.Interval <= 0 {
		return DefaultInterval
	}
	return m.Interval
}

// SetInterval sets the collection interval
func (m *Manager) SetInterval(d time.Duration) {
	m.Interval = d
}

// CollectOutput periodically collects output from all running processes
func (m *Manager) CollectOutput(ctx context.Context) {
	ticker := time.NewTicker(m.GetInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			for _, proc := range m.processes {
				proc.mu.Lock()
				if proc.Status == StatusRunning && m.onOutput != nil {
					m.onOutput(proc.ID, fmt.Sprintf("[collect] %d lines output", proc.Lines))
				}
				proc.mu.Unlock()
			}
			m.mu.RUnlock()
		}
	}
}

// joinLines joins line array into string
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// MaxAllowedSleepSeconds 是 `sleep` 阻断判定的**唯一权威上界**（single source of truth）。
//
// 语义**只在这里声明一次**（调用点不得复述其数值）：命令里出现的 sleep 秒数
// **大于**本值即被拦 —— 即 `sleep 1` 放行、`sleep 2` 被拒。
// 口径出处：docs/使用说明/模块测试/M24-Daemon与远程.md（TC-M24-037：sleep ≥ 2s 被检测）。
//
// ★ 病历：本值原先只活在判定里（`seconds >= 2`），而提示文案在
// `internal/tools/{monitor,bash}.go` 各写死一份 —— 中文写「2 秒以内」（错，像 ≤2）、
// 英文写 "under 2 seconds"（对，<2）⇒ 一个事实三份声明、两句人话互相矛盾。
// 用户照中文提示做（`sleep 2`）再被拒一次。修法 = 本常量 ＋ `SleepBlock.LimitSeconds`，
// 提示文案**由它派生**（：实现与文案都收进共享函数，调用点只薄转发）。
const MaxAllowedSleepSeconds = 1

// SleepBlock 描述一次命中的「阻断 sleep 模式」。
//
// ★ 返回**生效上界**（而不是只回一个原因词）是本类型唯一的存废理由：
// 让调用点能渲染出与判定**同源**的提示，从而「照提示做必然能过」。
type SleepBlock struct {
	Reason       string // 命中的模式（如 "sleep in command chain"）
	LimitSeconds int    // 放行上界 = MaxAllowedSleepSeconds；提示文案必须由它派生
}

// blockedSleepPatterns are regex patterns for detecting blocked sleep commands
var blockedSleepPatterns = []struct {
	regex   *regexp.Regexp
	message string
}{
	{regexp.MustCompile(`^sleep\s+(\d+)`), "sleep at start of command"},
	{regexp.MustCompile(`;\s*sleep\s+(\d+)`), "sleep in command chain"},
	{regexp.MustCompile(`&&\s*sleep\s+(\d+)`), "sleep in command chain"},
}

// DetectBlockedSleep 检测命令链中是否存在超过 MaxAllowedSleepSeconds 的 sleep。
//
// ★ 与旧 `DetectBlockedSleepPattern` 的两点差别：
// 1. 返回**结构化**结果（含生效上界），供调用点派生本地化文案 ——
// 调用点不得再自写秒数，否则文案又会与判定漂开。
// 2. 取**全部命中的最大秒数**：旧实现用 `FindStringSubmatch`（只取首个命中），
// `echo x && sleep 1 && sleep 200` 会因首个命中是 `sleep 1` 而**漏拦**。
func DetectBlockedSleep(command string) (SleepBlock, bool) {
	if command == "" {
		return SleepBlock{}, false
	}

	maxSeconds := -1
	reason := ""
	for _, p := range blockedSleepPatterns {
		for _, m := range p.regex.FindAllStringSubmatch(command, -1) {
			if len(m) < 2 {
				continue
			}
			seconds := 0
			for _, ch := range m[1] {
				seconds = seconds*10 + int(ch-'0')
			}
			if seconds > maxSeconds {
				maxSeconds = seconds
				reason = p.message
			}
		}
	}

	if maxSeconds > MaxAllowedSleepSeconds {
		return SleepBlock{Reason: reason, LimitSeconds: MaxAllowedSleepSeconds}, true
	}
	return SleepBlock{}, false
}

// ExtractSleepSeconds extracts the number of seconds from a sleep command
// Returns 0 if no sleep pattern is found
func ExtractSleepSeconds(command string, startIdx int) int {
	if command == "" {
		return 0
	}

	// Check if command at startIdx is a sleep command
	trimmed := strings.TrimSpace(command[startIdx:])
	re := regexp.MustCompile(`^sleep\s+(\d+)`)
	matches := re.FindStringSubmatch(trimmed)
	if len(matches) >= 2 {
		seconds := 0
		for _, ch := range matches[1] {
			seconds = seconds*10 + int(ch-'0')
		}
		return seconds
	}
	return 0
}
