package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/util"
)

const daemonProtocolVersion = 1

type readyMetadata struct {
	PID             int       `json:"pid"`
	InstanceID      string    `json:"instance_id"`
	ProtocolVersion int       `json:"protocol_version"`
	StartedAt       time.Time `json:"started_at"`
}

// DaemonConfig is the daemon configuration
type DaemonConfig struct {
	WorkDir          string        // Working directory
	MaxWorkers       int           // Max workers (default 2)
	TaskTimeout      time.Duration // Task timeout (default 30 minutes)
	PIDFilePath      string        // PID file path
	LogFilePath      string        // Log file path
	SocketPath       string        // IPC Socket/Pipe path
	CliPath          string        // whale CLI path (default "whale")
	MetricsAddr      string        // Metrics HTTP server address (default "127.0.0.1:0", OS-assigned port)
	MetricsAuthToken string        // Metrics Bearer Token (empty = bind 127.0.0.1 only)
}

// Validate 验证 DaemonConfig 关键字段范围，返回明确的错误信息。
// 验证项：MaxWorkers > 0（daemon worker 数）、TaskTimeout > 0（daemon 超时）。
// 应在 NewDaemon 自动回填默认值之后调用，确保最终配置合法。
func (cfg *DaemonConfig) Validate() error {
	if cfg.MaxWorkers <= 0 {
		return fmt.Errorf("daemon config: MaxWorkers must be > 0, got %d", cfg.MaxWorkers)
	}
	if cfg.TaskTimeout <= 0 {
		return fmt.Errorf("daemon config: TaskTimeout must be > 0, got %v", cfg.TaskTimeout)
	}
	return nil
}

// DaemonStatus is the daemon status
type DaemonStatus struct {
	Running         bool          `json:"running"`
	PID             int           `json:"pid"`
	Workers         int           `json:"workers"`
	PendingTasks    int           `json:"pending_tasks"`
	RunningTasks    int           `json:"running_tasks"`
	Uptime          time.Duration `json:"uptime"`
	MetricsAddr     string        `json:"metrics_addr,omitempty"`
	InstanceID      string        `json:"instance_id,omitempty"`
	ProtocolVersion int           `json:"protocol_version"`
	StorageHealth   string        `json:"storage_health,omitempty"`
}

// Daemon is the Whale Code daemon
// - Runs in background, manages task queue and workers
// - A lifetime-held OS lock enforces singleton; PID/ready files are metadata
// - Logs to file
// - Receives commands via IPC
type Daemon struct {
	lifecycleMu   sync.Mutex
	mu            sync.RWMutex
	cfg           DaemonConfig
	queue         *TaskQueue
	pool          *WorkerPool
	ipc           *IPCServer
	uds           *UDSServer // Native local transport: Unix socket or Windows Named Pipe.
	logger        *DaemonLogger
	registry      *WorkerRegistry
	events        *EventBus
	started       time.Time
	running       bool
	stopCh        chan struct{}
	stopOnce      sync.Once
	metricsServer *http.Server // Prometheus metrics HTTP server
	singletonLock *util.HeldFileLock
	instanceID    string
	// sandboxMgr/sandboxLevel is an optional outer worker sandbox for embedded
	// deployments. The standalone daemon relies on Whale CI's inner permission
	// and sandbox pipeline.
	sandboxMgr   sandbox.Manager
	sandboxLevel sandbox.SandboxLevel
}

// SetSandboxManager 注入沙箱管理器到 daemon（桶 X3 病灶3 接入点）。
//
// 必须在 daemon.Start 之前调用，以确保 daemon.Start 创建 worker pool 时
// 把沙箱管理器转发到每个 worker 实例。
//
// 参数：
// - mgr: 沙箱管理器实例（nil=不应用 worker 外层沙箱）
// - level: 默认沙箱级别（SandboxLevelDisabled=跳过沙箱）
//
// 接入示例（main.go 接线层）：
//
//	d := daemon.NewDaemon(cfg)
//	d.SetSandboxManager(sandboxMgr, sandbox.SandboxLevelRelaxed)
//	d.Start()
func (d *Daemon) SetSandboxManager(mgr sandbox.Manager, level sandbox.SandboxLevel) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sandboxMgr = mgr
	d.sandboxLevel = level
}

// NewDaemon creates a daemon instance
func NewDaemon(cfg DaemonConfig) *Daemon {
	// Set defaults
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 2
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = 30 * time.Minute
	}
	if cfg.CliPath == "" {
		cfg.CliPath = "whale"
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = defaultMetricsAddr
	}

	// Set default paths
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	whaleDir := filepath.Join(whalehome.Dir(), "daemon")

	if cfg.PIDFilePath == "" {
		cfg.PIDFilePath = filepath.Join(whaleDir, "daemon.pid")
	}
	if cfg.LogFilePath == "" {
		cfg.LogFilePath = filepath.Join(whaleDir, "daemon.log")
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = whaleDir
	}

	// Validate 确保自动回填后的配置合法（MaxWorkers > 0、TaskTimeout > 0）
	if err := cfg.Validate(); err != nil {
		// 记录日志但不阻断创建（保持向后兼容，auto-correction 已确保值合法）
		fmt.Fprintf(os.Stderr, "[daemon] %v\n", err)
	}

	return &Daemon{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// Start starts the daemon
// - Write PID file
// - Start IPC listener
// - Start workers
func (d *Daemon) Start() error {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return fmt.Errorf("daemon is already running")
	}
	d.stopCh = make(chan struct{})
	d.stopOnce = sync.Once{}

	// Ensure working directory exists
	if err := os.MkdirAll(filepath.Dir(d.cfg.PIDFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create working directory: %w", err)
	}

	instanceID, err := generateInstanceID()
	if err != nil {
		return fmt.Errorf("generate daemon instance ID: %w", err)
	}
	lock, err := util.TryAcquireFileLock(d.singletonLockPath())
	if err != nil {
		return fmt.Errorf("daemon is already running or its singleton lock is unavailable: %w", err)
	}
	d.singletonLock = lock
	d.instanceID = instanceID
	lockMetadata, _ := json.Marshal(readyMetadata{
		PID: os.Getpid(), InstanceID: instanceID, ProtocolVersion: daemonProtocolVersion, StartedAt: time.Now().UTC(),
	})
	if err := lock.WriteMetadata(lockMetadata); err != nil {
		d.releaseSingletonLocked()
		return fmt.Errorf("write daemon singleton metadata: %w", err)
	}

	// The held OS lock is authoritative for current daemons. A live legacy
	// daemon without identity metadata remains a conservative hard stop.
	if err := d.CleanupStale(); err != nil {
		d.releaseSingletonLocked()
		return err
	}

	// Initialize logger
	logger, err := NewDaemonLogger(d.cfg.LogFilePath)
	if err != nil {
		d.releaseSingletonLocked()
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	d.logger = logger
	d.logger.Info("Daemon starting...")

	// Write PID file
	if err := d.writePIDFile(); err != nil {
		d.logger.Error("Failed to write PID file: %v", err)
		d.logger.Close()
		d.releaseSingletonLocked()
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	// Initialize task queue
	storePath := filepath.Join(filepath.Dir(d.cfg.PIDFilePath), "tasks.json")
	d.queue = NewTaskQueue(storePath)
	if err := d.queue.Load(); err != nil {
		d.logger.Error("Failed to load task queue: %v", err)
		d.rollbackStartLocked()
		return fmt.Errorf("failed to load task queue: %w", err)
	}

	// Initialize worker pool
	d.pool = NewWorkerPool(d.cfg.MaxWorkers, d.cfg.CliPath, d.cfg.TaskTimeout)
	d.pool.SetLogger(d.logger)

	// Initialize worker registry
	d.registry = NewWorkerRegistry()

	// Initialize event bus
	d.events = NewEventBus()
	d.events.SetPersistPath(filepath.Join(filepath.Dir(d.cfg.PIDFilePath), "events.jsonl"))
	d.pool.SetEventBus(d.events)

	// Register default workers
	for i := 0; i < d.cfg.MaxWorkers; i++ {
		workerID := fmt.Sprintf("worker-%d", i+1)
		d.registry.Register(workerID, WorkerTypeCode, WorkerCapabilities{
			Type:        WorkerTypeCode,
			CanExecute:  true,
			CanSearch:   true,
			CanAnalyze:  true,
			CanGenerate: true,
			MaxTokens:   8192,
		})
	}

	// 将注册表注入 worker pool，使每个 worker 创建后能引用 registry 以记录已完成任务。
	// 必须在 pool.Start 之前调用，确保 worker 实例化时 registry 已就位。
	d.pool.SetRegistry(d.registry)

	// 桶 X3 病灶3：将 daemon 持有的沙箱管理器转发到 worker pool，
	// pool.Start 创建每个 worker 时会进一步注入到 worker 实例。
	// sandboxMgr==nil 或 level==Disabled 时不应用 worker 外层沙箱。
	// 必须在 pool.Start 之前调用，确保 worker 实例化时 sandboxMgr 已就位。
	if d.sandboxMgr != nil {
		d.pool.SetSandboxManager(d.sandboxMgr, d.sandboxLevel)
		if d.logger != nil {
			d.logger.Info("Worker pool sandbox manager injected (level=%s)", d.sandboxLevel.String())
		}
	} else if d.logger != nil {
		d.logger.Info("Worker outer sandbox not configured; using Whale CI permission and sandbox pipeline")
	}

	// Initialize IPC server
	d.ipc = NewIPCServer(d.cfg.SocketPath, d)
	d.ipc.SetLogger(d.logger)

	// Start IPC server
	if err := d.ipc.start(filePollingInitiallyEnabled()); err != nil {
		d.logger.Error("Failed to start IPC server: %v", err)
		d.rollbackStartLocked()
		return fmt.Errorf("failed to start IPC server: %w", err)
	}

	// Start the native local transport (Unix UDS or Windows Named Pipe).
	// The native transport reuses the token generated by IPCServer so every
	// client authenticates against the same per-instance secret.
	if err := d.startUDSServer(); err != nil {
		if fallbackErr := nativeTransportFailed(d.ipc); fallbackErr != nil {
			d.logger.Error("Failed to enable IPC file fallback: %v", fallbackErr)
			d.rollbackStartLocked()
			return fmt.Errorf("native local transport failed (%v) and file fallback failed: %w", err, fallbackErr)
		}
		if d.logger != nil {
			d.logger.Info("Native local transport not started, falling back to IPC file polling: %v", err)
		}
		// Native transport failure is non-fatal once file IPC is ready.
	} else {
		// Windows permanently keeps file polling closed once its current-user-only
		// Named Pipe is live. Unix retains the file fallback for compatibility.
		nativeTransportStarted(d.ipc)
	}

	// Start worker pool (using background context as root context)
	d.pool.Start(context.Background(), d.queue)

	// 接通 WorkerRegistry 健康检查：启动后台 goroutine 周期性检测 worker 心跳超时，
	// 此前 StartHealthCheck 从未被调用导致健康检查 goroutine 永不启动，注册表中的
	// 失败计数与 stale 标记无法自动维护。使用 background context 与 pool 保持一致。
	d.registry.StartHealthCheck(context.Background())

	// 启动 Prometheus metrics + health HTTP 端点（桶 X3 病灶4：fail-closed 鉴权检查）
	metricsSrv, err := d.startMetricsServer(d.cfg.MetricsAddr, d.cfg.MetricsAuthToken)
	if err != nil {
		// fail-closed：空 token + 非 127.0.0.1 绑定拒绝启动，清理已启动组件后返回错误
		d.rollbackStartLocked()
		return fmt.Errorf("metrics server start failed: %w", err)
	}
	d.metricsServer = metricsSrv
	if d.logger != nil {
		d.logger.Info("Metrics server listening on %s", metricsSrv.Addr)
	}

	d.running = true
	d.started = time.Now()

	d.logger.Info("Daemon started (PID=%d, workers=%d)", os.Getpid(), d.cfg.MaxWorkers)

	// Publish identity metadata first and the legacy numeric ready marker last.
	// The ready marker remains the atomic readiness edge for existing clients.
	metadata, err := json.Marshal(readyMetadata{
		PID: os.Getpid(), InstanceID: d.instanceID, ProtocolVersion: daemonProtocolVersion, StartedAt: d.started.UTC(),
	})
	if err != nil {
		d.running = false
		d.rollbackStartLocked()
		return fmt.Errorf("serialize ready metadata: %w", err)
	}
	if err := util.AtomicWriteFile(d.readyMetadataPath(), metadata, 0600); err != nil {
		d.logger.Error("Failed to write ready metadata: %v", err)
		d.running = false
		d.rollbackStartLocked()
		return fmt.Errorf("failed to write ready metadata: %w", err)
	}
	if err := util.AtomicWriteFile(d.readyFilePath(), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		d.logger.Error("Failed to write ready marker: %v", err)
		d.running = false
		d.rollbackStartLocked()
		return fmt.Errorf("failed to write ready marker: %w", err)
	}

	return nil
}

// rollbackStartLocked unwinds every component that may have started before a
// startup failure. The caller holds d.mu and lifecycleMu. Blocking shutdowns
// run without d.mu so an in-flight IPC/HTTP handler can finish Status calls.
func (d *Daemon) rollbackStartLocked() {
	d.running = false
	uds := d.uds
	ipc := d.ipc
	registry := d.registry
	pool := d.pool
	events := d.events
	logger := d.logger
	metricsServer := d.metricsServer
	queue := d.queue
	d.uds = nil
	d.metricsServer = nil
	d.mu.Unlock()

	if uds != nil {
		_ = uds.Stop()
	}
	if ipc != nil {
		_ = ipc.Stop()
	}
	shutdownMetricsServer(metricsServer)
	if registry != nil {
		registry.StopHealthCheck()
	}
	if pool != nil {
		pool.Stop()
	}
	if events != nil {
		events.Close()
	}
	if queue != nil {
		_ = queue.Close()
	}
	d.cleanup()
	if logger != nil {
		_ = logger.Close()
	}
	d.releaseSingletonLocked()

	d.mu.Lock()
}

// Stop stops the daemon
// - Notify workers to stop
// - Close IPC
// - Delete PID file
func (d *Daemon) Stop() error {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	d.NotifyStop()
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = false
	logger := d.logger
	registry := d.registry
	pool := d.pool
	queue := d.queue
	ipc := d.ipc
	uds := d.uds
	events := d.events
	metricsServer := d.metricsServer
	d.metricsServer = nil
	d.mu.Unlock()

	if logger != nil {
		logger.Info("Daemon stopping...")
	}

	// Stop accepting requests before draining workers. This prevents new tasks
	// from being added after the final queue save begins.
	if uds != nil {
		_ = uds.Stop()
	}
	if ipc != nil {
		_ = ipc.Stop()
	}
	shutdownMetricsServer(metricsServer)
	if registry != nil {
		registry.StopHealthCheck()
	}
	if pool != nil {
		pool.Stop()
	}

	// Persist task queue
	var saveErr error
	if queue != nil {
		if err := queue.Save(); err != nil {
			saveErr = err
			if logger != nil {
				logger.Error("Failed to persist task queue: %v", err)
			}
		}
		if err := queue.Close(); saveErr == nil && err != nil {
			saveErr = err
		}
	}

	// Clean up resources
	d.cleanup()

	// Close event bus
	if events != nil {
		events.Close()
	}
	if logger != nil {
		logger.Info("Daemon stopped")
		_ = logger.Close()
	}
	d.mu.Lock()
	d.releaseSingletonLocked()
	d.mu.Unlock()

	if saveErr != nil {
		return fmt.Errorf("persist task queue during stop: %w", saveErr)
	}
	return nil
}

// Status returns current daemon status with health check information
func (d *Daemon) Status() DaemonStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()

	status := DaemonStatus{
		Running:         d.running,
		PID:             os.Getpid(),
		InstanceID:      d.instanceID,
		ProtocolVersion: daemonProtocolVersion,
	}

	if d.running {
		status.Uptime = time.Since(d.started)
		if d.metricsServer != nil {
			status.MetricsAddr = d.metricsServer.Addr
		}
	}

	if d.pool != nil {
		status.Workers = d.cfg.MaxWorkers
		status.RunningTasks = d.pool.RunningCount()
	}

	if d.queue != nil {
		status.PendingTasks = d.queue.PendingCount()
		status.StorageHealth, _ = d.queue.StorageHealth()
	}

	return status
}

// HealthCheck performs a comprehensive health check of all daemon components
// Returns a map of component statuses suitable for health check endpoints
func (d *Daemon) HealthCheck() map[string]string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	health := make(map[string]string)

	if !d.running {
		health["daemon"] = "stopped"
		return health
	}

	health["daemon"] = "running"
	health["uptime"] = time.Since(d.started).String()

	// Check worker pool health
	if d.pool != nil {
		health["worker_pool"] = "healthy"
		health["active_workers"] = fmt.Sprintf("%d", d.pool.RunningCount())
	} else {
		health["worker_pool"] = "not_initialized"
	}

	// Check task queue health
	if d.queue != nil {
		storageHealth, storageError := d.queue.StorageHealth()
		health["task_queue"] = storageHealth
		health["pending_tasks"] = fmt.Sprintf("%d", d.queue.PendingCount())
		if storageError != "" {
			health["task_storage_error"] = storageError
		}
	} else {
		health["task_queue"] = "not_initialized"
	}

	// Check IPC server health
	if d.ipc != nil {
		health["ipc"] = "healthy"
	} else {
		health["ipc"] = "not_initialized"
	}

	// Check event bus health
	if d.events != nil {
		health["event_bus"] = "healthy"
	} else {
		health["event_bus"] = "not_initialized"
	}

	return health
}

// IsRunning checks if daemon is running (via PID file).
// 平台差异已收进 internal/platform.ProcessAlive（/ 收敛）。
func (d *Daemon) IsRunning() bool {
	pid, err := d.readPIDFile()
	if err != nil {
		return false
	}

	if pid == os.Getpid() {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return d.running
	}

	return isProcessAlive(pid)
}

// isProcessAlive reports whether a process with the given PID is alive.
//
// 本包装**不含判定逻辑**：唯一判据在 internal/platform.ProcessAlive
// （收敛 ＋ 拟 ，2026-09-20）。修复前本包自带一份实现
// （kill_windows.go 的 `processAlive`，走 tasklist）与 util/config/session
// 三域各写各的，「pid 还活着吗」在全仓有 7 份实现、其中 session 那份方向相反
// （EPERM ⇒ 判死）。保留本名只为让包内 6 个调用点语义不变。
//
// 历史注记（勿当作现状）：修复前这里曾用 proc.Signal(nil)，在 Linux 上即使进程
// 存在也返回 "os: unsupported signal type" ⇒ 误判 not running。system 的
// `syscall.Signal(0)`（POSIX kill(pid,0) 标准实现）正是 platform.ProcessAlive
// 的 Unix 分支所做的事，且额外正确处理 EPERM。
func isProcessAlive(pid int) bool {
	return platform.ProcessAlive(pid)
}

// IsRunning checks if the daemon process is currently running and ready
// by verifying the ready marker file exists and the daemon process is still alive.
// This is a package-level function that can be used without a Daemon instance.
func IsRunning(pidFilePath string) bool {
	readyPath := filepath.Join(filepath.Dir(pidFilePath), "daemon.ready")
	readyData, err := os.ReadFile(readyPath)
	if err != nil {
		return false
	}
	readyPID, err := strconv.Atoi(strings.TrimSpace(string(readyData)))
	if err != nil {
		return false
	}
	pidData, err := os.ReadFile(pidFilePath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || pid != readyPID {
		return false
	}
	metadataPath := filepath.Join(filepath.Dir(pidFilePath), "daemon.ready.json")
	if metadataData, metadataErr := os.ReadFile(metadataPath); metadataErr == nil {
		var metadata readyMetadata
		if json.Unmarshal(metadataData, &metadata) != nil || metadata.PID != pid || metadata.InstanceID == "" || metadata.ProtocolVersion <= 0 {
			return false
		}
	}
	return isProcessAlive(pid)
}

// IsReady checks if the daemon is ready by verifying the ready marker file exists
// and the daemon process is still running.
// This is a package-level function that can be used without a Daemon instance.
func IsReady(pidFilePath string) bool {
	return IsRunning(pidFilePath)
}

// CleanupStale cleans up stale PID files
func (d *Daemon) CleanupStale() error {
	if d.singletonLock != nil {
		// A successfully acquired OS lock proves no current-version daemon owns
		// this state directory. Preserve compatibility by refusing to clean a
		// live legacy daemon which has no identity sidecar.
		if _, metadataErr := os.Stat(d.readyMetadataPath()); os.IsNotExist(metadataErr) {
			if data, err := os.ReadFile(d.readyFilePath()); err == nil {
				if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && isProcessAlive(pid) {
					return fmt.Errorf("legacy daemon is running (PID=%d)", pid)
				}
			}
		}
		_ = os.Remove(d.cfg.PIDFilePath)
		d.cleanupStaleFiles()
		return nil
	}
	// A live ready marker is authoritative even if the PID file was removed or
	// damaged. Refuse to remove its socket and start a second daemon.
	if data, err := os.ReadFile(d.readyFilePath()); err == nil {
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && isProcessAlive(pid) {
			return fmt.Errorf("daemon is running (PID=%d)", pid)
		}
	}
	pid, err := d.readPIDFile()
	if err != nil {
		if !os.IsNotExist(err) {
			_ = os.Remove(d.cfg.PIDFilePath)
		}
		d.cleanupStaleFiles()
		return nil
	}

	if !isProcessAlive(pid) {
		_ = os.Remove(d.cfg.PIDFilePath)
		d.cleanupStaleFiles()
		return nil
	}

	return fmt.Errorf("daemon is running (PID=%d)", pid)
}

// Queue returns task queue reference
func (d *Daemon) Queue() *TaskQueue {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.queue
}

// CancelTask atomically marks a task cancelled, terminates its worker when it
// was already running, and persists the result before acknowledging the user.
func (d *Daemon) CancelTask(id string) error {
	d.mu.RLock()
	queue := d.queue
	pool := d.pool
	events := d.events
	d.mu.RUnlock()
	if queue == nil {
		return fmt.Errorf("daemon task queue is not initialized")
	}
	previous, err := queue.CancelPersisted(id)
	if err != nil {
		return err
	}
	if previous == TaskRunning && pool != nil {
		_ = pool.CancelTask(id)
	}
	if events != nil {
		events.Publish(Event{Type: EventTaskCancelled, Payload: map[string]interface{}{"task_id": id}})
	}
	return nil
}

// writePIDFile writes PID file atomically
func (d *Daemon) writePIDFile() error {
	pid := os.Getpid()
	data := []byte(strconv.Itoa(pid))

	dir := filepath.Dir(d.cfg.PIDFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	file, err := os.OpenFile(d.cfg.PIDFilePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = os.Remove(d.cfg.PIDFilePath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	removeOnError = false
	return nil
}

// readyFilePath returns the path to the daemon ready marker file
func (d *Daemon) readyFilePath() string {
	return filepath.Join(filepath.Dir(d.cfg.PIDFilePath), "daemon.ready")
}

func (d *Daemon) readyMetadataPath() string {
	return filepath.Join(filepath.Dir(d.cfg.PIDFilePath), "daemon.ready.json")
}

func (d *Daemon) singletonLockPath() string {
	return filepath.Join(filepath.Dir(d.cfg.PIDFilePath), "daemon.lock")
}

func (d *Daemon) releaseSingletonLocked() {
	if d.singletonLock != nil {
		_ = d.singletonLock.Release()
		d.singletonLock = nil
	}
}

func generateInstanceID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// readPIDFile reads PID file
func (d *Daemon) readPIDFile() (int, error) {
	data, err := os.ReadFile(d.cfg.PIDFilePath)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID file content: %s", string(data))
	}

	return pid, nil
}

// cleanup cleans up PID file, IPC files, and other resources.
// Worker processes are terminated by WorkerPool before this function runs.
func (d *Daemon) cleanup() {
	// Remove ready marker file
	os.Remove(d.readyFilePath())
	os.Remove(d.readyMetadataPath())

	os.Remove(d.cfg.PIDFilePath)

	// TD-14: Clean up UDS socket file（异常退出时残留）
	if d.cfg.SocketPath != "" {
		os.Remove(filepath.Join(d.cfg.SocketPath, UDSSocketFile))
	}

	// Clean up IPC residual files on every platform. File polling remains a
	// fallback beside UDS on Unix.
	if d.cfg.SocketPath != "" {
		ipcFiles := []string{
			filepath.Join(d.cfg.SocketPath, "cmd.json"),
			filepath.Join(d.cfg.SocketPath, "resp.json"),
			filepath.Join(d.cfg.SocketPath, "ipc.lock"),
			filepath.Join(d.cfg.SocketPath, "client.lock"),
			filepath.Join(d.cfg.SocketPath, AuthTokenFile),
		}
		for _, f := range ipcFiles {
			os.Remove(f)
		}
	}

}

func (d *Daemon) cleanupStaleFiles() {
	_ = os.Remove(d.readyFilePath())
	_ = os.Remove(d.readyMetadataPath())
	if d.cfg.SocketPath == "" {
		return
	}
	for _, name := range []string{"cmd.json", "resp.json", "ipc.lock", "client.lock", AuthTokenFile, UDSSocketFile} {
		_ = os.Remove(filepath.Join(d.cfg.SocketPath, name))
	}
}

// startUDSServer 尝试启动 UDS 传输层（TD-14 修复）。
//
// 行为：
// - 读取 IPC server 生成的 auth token（确保 UDS/IPC 客户端鉴权一致）
// - 创建 UDSConfig 并启动 UDSServer
// - Windows: current-user-only Named Pipe; file IPC only if Pipe startup fails
// - Unix: UDS; file IPC remains available as a fallback
//
// 返回 error 时调用方应记录日志但不阻断启动（IPC 已就绪作为回退）。
func (d *Daemon) startUDSServer() error {
	// 读取 IPC 生成的 auth token（IPC.Start 已写入 {socketPath}/auth.token）
	authToken, err := ReadAuthToken(d.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("read auth token for UDS: %w", err)
	}

	cfg := UDSConfig{
		SocketPath:     d.cfg.SocketPath,
		AuthToken:      authToken,
		MaxMessageSize: MaxMessageSize,
	}

	d.uds = NewUDSServer(cfg, d)
	d.uds.SetLogger(d.logger)

	if err := d.uds.Start(); err != nil {
		// Native transport failed; clear the reference and retain file IPC.
		d.uds = nil
		return fmt.Errorf("start native local transport: %w", err)
	}

	if d.logger != nil {
		d.logger.Info("Native local transport listening on %s", nativeTransportAddress(d.cfg.SocketPath))
	}

	return nil
}

// GetConfig returns a copy of daemon config
func (d *Daemon) GetConfig() DaemonConfig {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cfg
}

// Restart restarts the daemon.
// Acquires mu.Lock() briefly to check running state, then releases it before
// calling Stop() and Start() (which acquire their own locks internally).
//
// 修复（全模块审查）：Stop() 内部会 Close logger，
// 随后 Restart 用旧（已 Close 的）logger 记录 "Daemon restarted" 可能导致 nil panic。
// 修复：Stop 后 logger 可能已 Close/nil，所有后续日志调用前检查 nil；
// Start 失败时用 recover 保护避免旧 logger 写入 panic 导致 Restart 卡住。
func (d *Daemon) Restart() error {
	if !d.Status().Running {
		return fmt.Errorf("daemon is not running")
	}

	// 修复：在 Stop 前用 logger 记录（此时 logger 仍有效）
	if d.logger != nil {
		d.logger.Info("Restarting daemon...")
	} else {
		// logger 为 nil 时降级到标准日志（不 panic）
		fmt.Println("[daemon] Restarting daemon (logger not initialized)...")
	}

	if err := d.Stop(); err != nil {
		// Stop 失败时 logger 可能已 Close，用 fmt 降级记录
		fmt.Printf("[daemon] Restart stop failed: %v\n", err)
		return fmt.Errorf("stop failed: %w", err)
	}

	// 修复：Stop 已 Close logger，此时 d.logger 为已 Close 的旧实例。
	// Start 会重新初始化 logger，Start 失败时 d.logger 仍是旧的已 Close 实例。
	// 后续不再用旧 logger 记录 "Daemon restarted"，避免 nil panic。
	if err := d.Start(); err != nil {
		// Start 失败时 d.logger 可能是旧已 Close 的实例，用 fmt 降级记录
		fmt.Printf("[daemon] Restart start failed: %v\n", err)
		return fmt.Errorf("start failed: %w", err)
	}

	// 修复：Start 成功后 d.logger 是新实例，安全记录
	if d.logger != nil {
		d.logger.Info("Daemon restarted")
	}
	return nil
}

// Registry returns worker registry
func (d *Daemon) Registry() *WorkerRegistry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.registry
}

// Events returns event bus
func (d *Daemon) Events() *EventBus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.events
}

// Run blocks and runs daemon until stop signal is received
// Used for standalone running in --daemon mode
func (d *Daemon) Run() error {
	if err := d.Start(); err != nil {
		return err
	}

	// Start heartbeat detection
	go d.heartbeatLoop()

	// Block waiting for stop signal
	<-d.stopCh
	return d.Stop()
}

// heartbeatLoop heartbeat detection loop.
// Checks d.running before each tick to avoid accessing resources during shutdown.
func (d *Daemon) heartbeatLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.mu.RLock()
			running := d.running
			d.mu.RUnlock()
			if !running {
				continue
			}
			if d.registry != nil {
				stale := d.registry.DetectStale()
				for _, w := range stale {
					if d.logger != nil {
						d.logger.Info("Worker %s heartbeat timeout, marked as stale", w.ID)
					}
					if d.events != nil {
						d.events.Publish(Event{
							Type:    EventWorkerStale,
							Payload: w,
						})
					}
				}
			}
		}
	}
}

// NotifyStop notifies daemon to stop
func (d *Daemon) NotifyStop() {
	d.mu.RLock()
	stopCh := d.stopCh
	d.mu.RUnlock()
	if stopCh == nil {
		return
	}
	d.stopOnce.Do(func() { close(stopCh) })
}
