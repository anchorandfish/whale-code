package daemon

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// IPC server and client
// Windows: current-user-only Named Pipe, with file polling only as a startup fallback
// Linux/Mac: Unix Socket plus file-polling compatibility fallback
// Protocol: JSON-RPC 2.0 over line-delimited JSON
//
// Security: IPC includes a shared secret token for authentication.
// On Unix the token file is mode 0600. On Windows it inherits the configured
// state directory ACL, which deployments must keep private to the user.
// Clients must include the token in their request to be authenticated.

const (
	// MaxMessageSize is the maximum allowed size for IPC messages (1 MB).
	MaxMessageSize = 1 * 1024 * 1024
	// AuthTokenFile is the name of the file storing the authentication token.
	AuthTokenFile = "auth.token"
)

// JSONRPCRequest is a JSON-RPC 2.0 request
type JSONRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      int                    `json:"id"`
	Token   string                 `json:"token,omitempty"` // Authentication token
}

// JSONRPCResponse is a JSON-RPC 2.0 response
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      int         `json:"id"`
}

// RPCError is a JSON-RPC error
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// IPCServer owns the shared authentication token and the file-polling
// compatibility transport.
type IPCServer struct {
	socketPath string // IPC path (directory path)
	daemon     *Daemon
	mu         sync.Mutex
	running    bool
	polling    bool
	pollClosed bool
	stopCh     chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
	logger     *DaemonLogger
	authToken  string // Shared secret for IPC authentication

	// File polling scheme paths
	cmdPath        string // Command file path
	respPath       string // Response file path
	lockPath       string // Lock file path
	clientLockPath string // Serializes file-transport clients across processes
}

// NewIPCServer creates an IPC server
// socketPath: IPC directory path
// daemon: daemon instance
func NewIPCServer(socketPath string, daemon *Daemon) *IPCServer {
	return &IPCServer{
		socketPath:     socketPath,
		daemon:         daemon,
		stopCh:         make(chan struct{}),
		cmdPath:        filepath.Join(socketPath, "cmd.json"),
		respPath:       filepath.Join(socketPath, "resp.json"),
		lockPath:       filepath.Join(socketPath, "ipc.lock"),
		clientLockPath: filepath.Join(socketPath, "client.lock"),
	}
}

// SetLogger sets the logger
func (s *IPCServer) SetLogger(logger *DaemonLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

// Start starts IPC listening.
// Generates an authentication token and writes it to a file for clients to use.
func (s *IPCServer) Start() error {
	return s.start(true)
}

// start initializes the shared token and optionally starts the file transport.
// The Windows daemon starts token-only and enables polling only if Named Pipe
// startup fails; direct IPCServer users retain Start's compatibility behavior.
func (s *IPCServer) start(enablePolling bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("IPC server is already running")
	}

	// Ensure directory exists
	if err := os.MkdirAll(s.socketPath, 0700); err != nil {
		return fmt.Errorf("failed to create IPC directory: %w", err)
	}

	// Clean up residual files from previous abnormal exit
	os.Remove(s.cmdPath)
	os.Remove(s.respPath)
	os.Remove(s.lockPath)
	os.Remove(s.clientLockPath)
	os.Remove(filepath.Join(s.socketPath, AuthTokenFile))

	// Generate authentication token
	var err error
	s.authToken, err = generateAuthToken()
	if err != nil {
		return fmt.Errorf("failed to generate auth token: %w", err)
	}
	if err := s.writeAuthToken(); err != nil {
		return fmt.Errorf("failed to write auth token: %w", err)
	}

	s.running = true
	s.pollClosed = false
	if enablePolling {
		s.startPollingLocked()
	}

	return nil
}

func (s *IPCServer) startPollingLocked() {
	s.polling = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.pollLoop()
	}()
}

func (s *IPCServer) enablePolling() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return fmt.Errorf("IPC server is not running")
	}
	if s.polling {
		return nil
	}
	if s.pollClosed {
		return fmt.Errorf("IPC file polling has already been permanently disabled")
	}
	s.startPollingLocked()
	return nil
}

// DisablePolling permanently closes the file-polling transport while keeping
// the server-owned authentication token available to the native transport.
// Stop must still be called to remove that token.
func (s *IPCServer) DisablePolling() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	wasPolling := s.polling
	s.polling = false
	s.pollClosed = true
	s.closeOnce.Do(func() { close(s.stopCh) })
	s.mu.Unlock()

	// Wait for an in-flight command to finish before removing transport files.
	if wasPolling {
		s.wg.Wait()
	}
	s.cleanupPollingFiles()
}

// Stop stops IPC listening and cleans up auth token
func (s *IPCServer) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}

	s.running = false
	s.polling = false
	s.pollClosed = true
	s.closeOnce.Do(func() { close(s.stopCh) })
	s.mu.Unlock()

	// Ensure processCommand has returned before removing files. Otherwise a
	// final response can recreate the test/user data directory after Stop.
	s.wg.Wait()

	s.cleanupPollingFiles()
	os.Remove(filepath.Join(s.socketPath, AuthTokenFile))

	return nil
}

func (s *IPCServer) cleanupPollingFiles() {
	os.Remove(s.cmdPath)
	os.Remove(s.respPath)
	os.Remove(s.lockPath)
	os.Remove(s.clientLockPath)
}

// pollLoop is the file polling loop, detecting commands written by client
func (s *IPCServer) pollLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.processCommand()
		}
	}
}

// processCommand processes a pending command with size limit and authentication
func (s *IPCServer) processCommand() {
	// Try to acquire lock
	if !s.acquireLock() {
		return
	}
	defer s.releaseLock()

	// Check if command file exists
	info, err := os.Stat(s.cmdPath)
	if err != nil {
		if !os.IsNotExist(err) && s.logger != nil {
			s.logger.Error("Failed to stat command file: %v", err)
		}
		return
	}

	// Enforce message size limit
	if info.Size() > MaxMessageSize {
		if s.logger != nil {
			s.logger.Error("Command file exceeds maximum size (%d > %d)", info.Size(), MaxMessageSize)
		}
		s.writeResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32604, Message: fmt.Sprintf("message too large: %d bytes (max %d)", info.Size(), MaxMessageSize)},
			ID:      0,
		})
		os.Remove(s.cmdPath)
		return
	}

	data, err := os.ReadFile(s.cmdPath)
	if err != nil {
		if !os.IsNotExist(err) && s.logger != nil {
			s.logger.Error("Failed to read command file: %v", err)
		}
		return
	}

	// Parse command
	var req JSONRPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.writeResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32700, Message: "failed to parse request"},
			ID:      0,
		})
		os.Remove(s.cmdPath)
		return
	}

	// Authenticate request
	if !s.authenticateRequest(req) {
		s.writeResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32605, Message: "authentication failed: invalid or missing token"},
			ID:      req.ID,
		})
		os.Remove(s.cmdPath)
		return
	}

	// Handle command
	resp := s.handleRequest(req)

	// Write response
	s.writeResponse(resp)

	// Delete processed command file
	os.Remove(s.cmdPath)
}

// handleRequest handles JSON-RPC request
func (s *IPCServer) handleRequest(req JSONRPCRequest) JSONRPCResponse {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
	}

	switch req.Method {
	case "status":
		resp.Result = s.daemon.Status()

	case "add_task":
		prompt, _ := req.Params["prompt"].(string)
		workDir, _ := req.Params["workdir"].(string)
		taskTypeStr, _ := req.Params["type"].(string)
		priorityFloat, _ := req.Params["priority"].(float64)
		if prompt == "" {
			resp.Error = &RPCError{Code: -32602, Message: "missing prompt parameter"}
			break
		}
		taskType := TaskType(taskTypeStr)
		if taskType == "" {
			taskType = TaskDream
		}
		priority := TaskPriority(int(priorityFloat))
		if priority < PriorityLow || priority > PriorityCritical {
			priority = PriorityNormal
		}
		// Persist before notifying the scheduler. A successful RPC response means
		// the task is durable and cannot be replayed from an older pending state.
		task, err := s.daemon.queue.AddPersistedWithNotify(prompt, workDir, taskType, priority)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("Failed to persist new task: %v", err)
			}
			resp.Error = &RPCError{Code: -32603, Message: err.Error()}
			break
		}
		resp.Result = task

	case "list_tasks":
		cursor, cursorSet := req.Params["cursor"].(string)
		limitFloat, limitSet := req.Params["limit"].(float64)
		if cursorSet || limitSet {
			page, err := s.daemon.queue.ListPage(cursor, int(limitFloat))
			if err != nil {
				resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			} else {
				resp.Result = page
			}
		} else {
			resp.Result = s.daemon.queue.List()
		}

	case "cancel_task":
		id, _ := req.Params["id"].(string)
		if id == "" {
			resp.Error = &RPCError{Code: -32602, Message: "missing id parameter"}
			break
		}
		if err := s.daemon.CancelTask(id); err != nil {
			resp.Error = &RPCError{Code: -32603, Message: err.Error()}
		} else {
			resp.Result = "ok"
		}

	case "shutdown":
		resp.Result = "ok"
		// Asynchronously stop daemon
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.daemon.NotifyStop()
		}()

	default:
		resp.Error = &RPCError{Code: -32601, Message: fmt.Sprintf("unknown method: %s", req.Method)}
	}

	return resp
}

// writeResponse writes response to response file
func (s *IPCServer) writeResponse(resp JSONRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to serialize response: %v", err)
		}
		return
	}

	if err := util.AtomicWriteFile(s.respPath, data, 0600); err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to write response file: %v", err)
		}
	}
}

// acquireLock tries to acquire IPC lock
func (s *IPCServer) acquireLock() bool {
	// Try to create lock file (exclusive mode)
	file, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	// Write current PID
	fmt.Fprintf(file, "%d", os.Getpid())
	file.Close()
	return true
}

// releaseLock releases IPC lock
func (s *IPCServer) releaseLock() {
	os.Remove(s.lockPath)
}

// IPCClient is the IPC client
// Communicates with daemon via file polling scheme
type IPCClient struct {
	socketPath     string
	cmdPath        string
	respPath       string
	lockPath       string
	clientLockPath string
	nextID         int
	mu             sync.Mutex
	timeout        time.Duration
}

// NewIPCClient creates an IPC client
// socketPath: IPC directory path
func NewIPCClient(socketPath string) *IPCClient {
	return &IPCClient{
		socketPath:     socketPath,
		cmdPath:        filepath.Join(socketPath, "cmd.json"),
		respPath:       filepath.Join(socketPath, "resp.json"),
		lockPath:       filepath.Join(socketPath, "ipc.lock"),
		clientLockPath: filepath.Join(socketPath, "client.lock"),
		nextID:         1,
		timeout:        10 * time.Second,
	}
}

// SetTimeout sets the total lock acquisition and response wait budget.
func (c *IPCClient) SetTimeout(timeout time.Duration) {
	if timeout > 0 {
		c.timeout = timeout
	}
}

// SendCommand sends command to daemon and waits for response.
// Automatically includes authentication token read from the daemon's token file.
// method: command method name
// params: command parameters
func (c *IPCClient) SendCommand(method string, params map[string]interface{}) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requestDeadline := time.Now().Add(c.timeout)
	if err := c.acquireRequestLock(time.Until(requestDeadline)); err != nil {
		return nil, err
	}
	defer os.Remove(c.clientLockPath)

	// Read auth token
	token, err := ReadAuthToken(c.socketPath)
	if err != nil {
		return nil, err
	}

	// Build request
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      c.nextID,
		Token:   token,
	}
	c.nextID++

	// Clean up old response file
	os.Remove(c.respPath)

	// Write command file
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request: %w", err)
	}
	if err := util.AtomicWriteFile(c.cmdPath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to write command file: %w", err)
	}

	for time.Now().Before(requestDeadline) {
		respData, err := os.ReadFile(c.respPath)
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("failed to read response file: %w", err)
		}

		// Parse response
		var resp JSONRPCResponse
		if err := json.Unmarshal(respData, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		// Clean up response file
		os.Remove(c.respPath)

		if resp.Error != nil {
			return nil, fmt.Errorf("RPC error [%d]: %s", resp.Error.Code, resp.Error.Message)
		}

		return resp.Result, nil
	}

	return nil, fmt.Errorf("waiting for response timed out")
}

// DefaultSocketPath returns the default IPC path
func DefaultSocketPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(whalehome.Dir(), "daemon")
	}
	return filepath.Join(whalehome.Dir(), "daemon")
}

// generateAuthToken 使用 crypto/rand 生成 32 字节的安全随机认证令牌。
// 安全关键场景：crypto/rand.Read 失败时直接返回 error，不降级到时间戳伪随机，
// 避免使用可预测的令牌导致 IPC 被伪造。令牌以 hex 编码字符串形式返回。
func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate auth token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// writeAuthToken writes the authentication token to a file with restrictive permissions
func (s *IPCServer) writeAuthToken() error {
	tokenPath := filepath.Join(s.socketPath, AuthTokenFile)
	if err := os.MkdirAll(s.socketPath, 0700); err != nil {
		return err
	}
	return util.AtomicWriteFile(tokenPath, []byte(s.authToken), 0600)
}

// authenticateRequest 校验请求携带的 auth token 是否有效。
//
// 修复：authToken 为空时 fail-closed 返回 false，拒绝所有请求。
// 正常流程下 daemon 启动时生成 token 并写入文件（writeAuthToken），
// 空字符串表示初始化异常或被绕过，不应放行。
func (s *IPCServer) authenticateRequest(req JSONRPCRequest) bool {
	// PCR G-P1-1 收敛（I1）：比较逻辑委托给包内单一强制点 authorize（auth.go），
	// 不再内联 subtle 比较，消除与 UDS / metrics 传输的实现漂移。
	return authorize(s.authToken, req.Token)
}

func (c *IPCClient) acquireRequestLock(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		file, err := os.OpenFile(c.clientLockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d", os.Getpid())
			_ = file.Close()
			return nil
		}
		if !os.IsExist(err) {
			return fmt.Errorf("failed to acquire IPC client lock: %w", err)
		}

		// Recover a lock left by a crashed client. A lock owned by this process
		// is still valid because separate IPCClient instances may run concurrently.
		if data, readErr := os.ReadFile(c.clientLockPath); readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid != os.Getpid() && !isProcessAlive(pid) {
				_ = os.Remove(c.clientLockPath)
				continue
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timed out acquiring IPC client lock")
}

// ReadAuthToken reads the authentication token from the daemon's token file
func ReadAuthToken(socketPath string) (string, error) {
	tokenPath := filepath.Join(socketPath, AuthTokenFile)
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to read auth token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
