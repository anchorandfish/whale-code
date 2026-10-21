package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	daemonEnvWorkDir     = "WHALE_DAEMON_WORK_DIR"
	daemonEnvPIDFile     = "WHALE_DAEMON_PID_FILE"
	daemonEnvLogFile     = "WHALE_DAEMON_LOG_FILE"
	daemonEnvSocketPath  = "WHALE_DAEMON_SOCKET_PATH"
	daemonEnvCLIPath     = "WHALE_DAEMON_CLI_PATH"
	daemonEnvMaxWorkers  = "WHALE_DAEMON_MAX_WORKERS"
	daemonEnvTaskTimeout = "WHALE_DAEMON_TASK_TIMEOUT"
	daemonEnvMetricsAddr = "WHALE_DAEMON_METRICS_ADDR"
	daemonEnvMetricsAuth = "WHALE_DAEMON_METRICS_AUTH_TOKEN"
)

const (
	controllerRequestTimeout = 6 * time.Second
	controllerUDSTimeout     = time.Second
)

var ErrAlreadyRunning = errors.New("daemon is already running")

// Controller manages a standalone daemon process. It owns no daemon runtime
// resources; all operations after Start are performed over authenticated IPC.
type Controller struct {
	cfg          DaemonConfig
	startTimeout time.Duration
}

func NewController(cfg DaemonConfig) *Controller {
	// NewDaemon normalizes an unspecified CLI path to the legacy "whale"
	// placeholder. A controller must start the exact current binary so TUI
	// launches also work from ./whale, app bundles, and renamed executables.
	if cfg.CliPath == "" || cfg.CliPath == "whale" {
		if executable, err := os.Executable(); err == nil {
			cfg.CliPath = executable
		}
	}
	normalized := NewDaemon(cfg).GetConfig()
	return &Controller{cfg: normalized, startTimeout: 5 * time.Second}
}

func (c *Controller) Config() DaemonConfig { return c.cfg }

func (c *Controller) IsRunning() bool { return IsRunning(c.cfg.PIDFilePath) }

func (c *Controller) Start(ctx context.Context) error {
	if c.IsRunning() {
		verifyCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := c.verifiedStatus(verifyCtx)
		cancel()
		if err == nil {
			return ErrAlreadyRunning
		}
	}

	if err := os.MkdirAll(filepath.Dir(c.cfg.LogFilePath), 0700); err != nil {
		return fmt.Errorf("create daemon log directory: %w", err)
	}
	bootstrapLog := c.cfg.LogFilePath + ".bootstrap"
	logFile, err := os.OpenFile(bootstrapLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open daemon bootstrap log: %w", err)
	}

	cmd := exec.Command(c.cfg.CliPath, "--daemon")
	if c.cfg.WorkDir != "" {
		cmd.Dir = c.cfg.WorkDir
	}
	cmd.Env = append(os.Environ(), c.configEnv()...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon process: %w", err)
	}
	_ = logFile.Close()

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	deadline := time.NewTimer(c.startTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = killProcessTree(cmd.Process.Pid)
			return ctx.Err()
		case err := <-exitCh:
			if err == nil {
				err = errors.New("process exited before becoming ready")
			}
			return fmt.Errorf("daemon startup failed (see %s): %w", bootstrapLog, err)
		case <-deadline.C:
			_ = killProcessTree(cmd.Process.Pid)
			return fmt.Errorf("daemon did not become ready within %s (see %s)", c.startTimeout, bootstrapLog)
		case <-ticker.C:
			if c.IsRunning() {
				verifyCtx, cancel := context.WithTimeout(ctx, time.Second)
				_, verifyErr := c.verifiedStatus(verifyCtx)
				cancel()
				if verifyErr == nil {
					return nil
				}
			}
		}
	}
}

func (c *Controller) Stop(ctx context.Context) error {
	if !c.IsRunning() {
		return nil
	}
	if _, err := c.sendCommandContext(ctx, "shutdown", nil); err != nil {
		return fmt.Errorf("request daemon shutdown: %w", err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for daemon shutdown: %w", ctx.Err())
		case <-ticker.C:
			if !c.IsRunning() {
				return nil
			}
		}
	}
}

func (c *Controller) Restart(ctx context.Context) error {
	if err := c.Stop(ctx); err != nil {
		return err
	}
	return c.Start(ctx)
}

func (c *Controller) Status() (DaemonStatus, error) {
	if !c.IsRunning() {
		return DaemonStatus{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), controllerRequestTimeout)
	defer cancel()
	return c.verifiedStatus(ctx)
}

func (c *Controller) verifiedStatus(ctx context.Context) (DaemonStatus, error) {
	metadataData, err := os.ReadFile(filepath.Join(filepath.Dir(c.cfg.PIDFilePath), "daemon.ready.json"))
	if err != nil {
		return DaemonStatus{}, fmt.Errorf("read daemon identity: %w", err)
	}
	var metadata readyMetadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		return DaemonStatus{}, fmt.Errorf("decode daemon identity: %w", err)
	}
	result, err := c.sendCommandContext(ctx, "status", nil)
	if err != nil {
		return DaemonStatus{}, err
	}
	var status DaemonStatus
	if err := decodeRPCResult(result, &status); err != nil {
		return DaemonStatus{}, fmt.Errorf("decode daemon status: %w", err)
	}
	if status.PID != metadata.PID || status.InstanceID != metadata.InstanceID || status.ProtocolVersion != metadata.ProtocolVersion {
		return DaemonStatus{}, fmt.Errorf("daemon identity mismatch")
	}
	return status, nil
}

func (c *Controller) AddTask(prompt, workDir string, taskType TaskType, priority TaskPriority) (*Task, error) {
	if workDir == "" {
		workDir = c.cfg.WorkDir
	}
	result, err := c.sendCommand("add_task", map[string]interface{}{
		"prompt": prompt, "workdir": workDir, "type": string(taskType), "priority": int(priority),
	})
	if err != nil {
		return nil, err
	}
	var task Task
	if err := decodeRPCResult(result, &task); err != nil {
		return nil, fmt.Errorf("decode added task: %w", err)
	}
	return &task, nil
}

func (c *Controller) ListTasks() ([]Task, error) {
	var tasks []Task
	cursor := ""
	for {
		page, err := c.ListTaskPage(cursor, maxTaskPageSize)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, page.Tasks...)
		if page.NextCursor == "" {
			return tasks, nil
		}
		cursor = page.NextCursor
	}
}

func (c *Controller) ListTaskPage(cursor string, limit int) (TaskPage, error) {
	result, err := c.sendCommand("list_tasks", map[string]interface{}{"cursor": cursor, "limit": limit})
	if err != nil {
		return TaskPage{}, err
	}
	var page TaskPage
	if err := decodeRPCResult(result, &page); err != nil {
		return TaskPage{}, fmt.Errorf("decode task page: %w", err)
	}
	return page, nil
}

func (c *Controller) CancelTask(id string) error {
	_, err := c.sendCommand("cancel_task", map[string]interface{}{"id": id})
	return err
}

func (c *Controller) sendCommand(method string, params map[string]interface{}) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controllerRequestTimeout)
	defer cancel()
	return c.sendCommandContext(ctx, method, params)
}

func (c *Controller) sendCommandContext(ctx context.Context, method string, params map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	udsTimeout := boundedTransportTimeout(ctx, controllerUDSTimeout)
	udsClient := NewUDSClient(c.cfg.SocketPath)
	udsClient.SetTimeout(udsTimeout)
	udsResult, udsErr := udsClient.SendCommand(method, params)
	if udsErr == nil {
		return udsResult, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fileClient := NewIPCClient(c.cfg.SocketPath)
	fileClient.SetTimeout(boundedTransportTimeout(ctx, controllerRequestTimeout))
	result, fileErr := fileClient.SendCommand(method, params)
	if fileErr == nil {
		return result, nil
	}
	return nil, fmt.Errorf("UDS failed: %v; file IPC failed: %w", udsErr, fileErr)
}

func boundedTransportTimeout(ctx context.Context, maximum time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < maximum {
			if remaining <= 0 {
				return time.Millisecond
			}
			return remaining
		}
	}
	return maximum
}

func decodeRPCResult(result interface{}, target interface{}) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (c *Controller) configEnv() []string {
	return []string{
		daemonEnvWorkDir + "=" + c.cfg.WorkDir,
		daemonEnvPIDFile + "=" + c.cfg.PIDFilePath,
		daemonEnvLogFile + "=" + c.cfg.LogFilePath,
		daemonEnvSocketPath + "=" + c.cfg.SocketPath,
		daemonEnvCLIPath + "=" + c.cfg.CliPath,
		daemonEnvMaxWorkers + "=" + strconv.Itoa(c.cfg.MaxWorkers),
		daemonEnvTaskTimeout + "=" + c.cfg.TaskTimeout.String(),
		daemonEnvMetricsAddr + "=" + c.cfg.MetricsAddr,
		daemonEnvMetricsAuth + "=" + c.cfg.MetricsAuthToken,
	}
}

// ConfigFromEnv returns the standalone daemon configuration passed by a
// Controller, or secure defaults for a direct `whale --daemon` invocation.
func ConfigFromEnv(defaultWorkDir, executable string) DaemonConfig {
	cfg := DaemonConfig{WorkDir: defaultWorkDir, CliPath: executable}
	if value := os.Getenv(daemonEnvWorkDir); value != "" {
		cfg.WorkDir = value
	}
	cfg.PIDFilePath = os.Getenv(daemonEnvPIDFile)
	cfg.LogFilePath = os.Getenv(daemonEnvLogFile)
	cfg.SocketPath = os.Getenv(daemonEnvSocketPath)
	if value := os.Getenv(daemonEnvCLIPath); value != "" {
		cfg.CliPath = value
	}
	if value := os.Getenv(daemonEnvMaxWorkers); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.MaxWorkers = parsed
		}
	}
	if value := os.Getenv(daemonEnvTaskTimeout); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			cfg.TaskTimeout = parsed
		}
	}
	cfg.MetricsAddr = os.Getenv(daemonEnvMetricsAddr)
	cfg.MetricsAuthToken = os.Getenv(daemonEnvMetricsAuth)
	return NewDaemon(cfg).GetConfig()
}
