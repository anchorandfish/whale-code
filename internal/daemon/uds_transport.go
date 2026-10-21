//go:build !windows

// Package daemon — M24-01 Unix Domain Socket 传输（SNAPSHOT）
//
// 本文件实现 Linux/macOS 上的 Unix Domain Socket IPC 传输，替代文件轮询。
// 对标同类 CLI IPC（官方仓库）与 docker daemon UDS。
//
// 设计要点（候选 A 选定）：
// - 接口签名：UDSTransport 接口抽象，server/client 双端
// - 结构体字段：UDSConfig 含 SocketPath/AuthToken/MaxMessageSize
// - 分层权责：本文件只做"UDS 传输层"，JSON-RPC 处理由 ipc.go
// - 降级策略：UDS 启动失败时回退到文件轮询（IPCServer）
// - TTL规则：连接无 TTL，监听器随 daemon 生命周期
//
// SNAPSHOT-1：UDS server 实现 net.ListenerAccept + goroutine per conn
// SNAPSHOT-2：UDS client 实现 net.Dial + Send/Recv
// SNAPSHOT-3：JSON-RPC 2.0 over line-delimited JSON 协议
// SNAPSHOT-4：Auth token 复用 ipc.go 的 generateAuthToken + ReadAuthToken
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UDSMaxConnections 是 UDS server 最大并发连接数（防资源耗尽）。
const UDSMaxConnections = 16

// UDSReadTimeout 是单次读操作超时（10s，防慢速攻击）。
const UDSReadTimeout = 10 * time.Second

// UDSWriteTimeout 是单次写操作超时（10s）。
const UDSWriteTimeout = 10 * time.Second

// UDSSocketFile 是 UDS socket 文件名。
const UDSSocketFile = "daemon.sock"

// UDSConfig 是 UDS 传输配置（SNAPSHOT-1）。
type UDSConfig struct {
	SocketPath     string // socket 目录路径（与 IPCServer 共用）
	AuthToken      string // 认证令牌（空则拒绝所有请求，fail-closed；见 authorize）
	MaxMessageSize int64  // 单条消息最大字节数（默认 MaxMessageSize）
}

// UDSServer 是 Unix Domain Socket 服务端（SNAPSHOT-2）。
//
// 行为：
// - 监听 ${SocketPath}/daemon.sock
// - 每个连接独立 goroutine 处理
// - 限制最大并发连接数 UDSMaxConnections
// - JSON-RPC 2.0 over line-delimited JSON
// - 复用 IPCServer.handleRequest 的请求分发逻辑
type UDSServer struct {
	cfg       UDSConfig
	daemon    *Daemon
	listener  net.Listener
	wg        sync.WaitGroup
	mu        sync.Mutex
	running   bool
	stopCh    chan struct{}
	closeOnce sync.Once
	logger    *DaemonLogger
	conns     map[net.Conn]struct{}
}

// NewUDSServer 创建 UDS 服务端（SNAPSHOT-3）。
//
// 参数：
// - cfg: UDS 配置（SocketPath 必填）
// - daemon: daemon 实例（用于 handleRequest 分发）
func NewUDSServer(cfg UDSConfig, daemon *Daemon) *UDSServer {
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = MaxMessageSize
	}
	return &UDSServer{
		cfg:    cfg,
		daemon: daemon,
		stopCh: make(chan struct{}),
		conns:  make(map[net.Conn]struct{}),
	}
}

// SetLogger 设置日志器（SNAPSHOT-4）。
func (s *UDSServer) SetLogger(logger *DaemonLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

// Start 启动 UDS 监听（SNAPSHOT-5）。
//
// 行为：
// - 创建 socket 目录
// - 移除残留 socket 文件（上次异常退出遗留）
// - net.Listen("unix", path) 开始监听
// - 启动 acceptLoop goroutine
func (s *UDSServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("UDS server is already running")
	}

	if err := os.MkdirAll(s.cfg.SocketPath, 0700); err != nil {
		return fmt.Errorf("UDS: mkdir: %w", err)
	}

	sockPath := filepath.Join(s.cfg.SocketPath, UDSSocketFile)
	// 移除残留 socket 文件（上次异常退出遗留）
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("UDS: listen: %w", err)
	}
	// socket 文件权限 0600，仅当前用户可读写
	if err := os.Chmod(sockPath, 0600); err != nil {
		listener.Close()
		return fmt.Errorf("UDS: chmod: %w", err)
	}

	s.listener = listener
	s.running = true
	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// Stop 停止 UDS 监听并清理 socket 文件（SNAPSHOT-6）。
func (s *UDSServer) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}

	s.running = false
	s.closeOnce.Do(func() { close(s.stopCh) })

	if s.listener != nil {
		_ = s.listener.Close()
	}
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()

	sockPath := filepath.Join(s.cfg.SocketPath, UDSSocketFile)
	os.Remove(sockPath)

	return nil
}

// acceptLoop 接受新连接并派发处理 goroutine（SNAPSHOT-7）。
//
// 限制最大并发连接数 UDSMaxConnections，超过则关闭新连接。
func (s *UDSServer) acceptLoop() {
	defer s.wg.Done()

	sem := make(chan struct{}, UDSMaxConnections)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				if s.logger != nil {
					s.logger.Error("UDS accept error: %v", err)
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		select {
		case sem <- struct{}{}:
			s.mu.Lock()
			if !s.running {
				s.mu.Unlock()
				<-sem
				_ = conn.Close()
				return
			}
			s.conns[conn] = struct{}{}
			s.wg.Add(1)
			s.mu.Unlock()
			go func(c net.Conn) {
				defer s.wg.Done()
				defer func() { <-sem }()
				defer func() {
					s.mu.Lock()
					delete(s.conns, c)
					s.mu.Unlock()
					_ = c.Close()
				}()
				s.handleConn(c)
			}(conn)
		default:
			// 超过最大并发连接数，拒绝
			conn.Close()
			if s.logger != nil {
				s.logger.Error("UDS: max connections reached, rejecting")
			}
		}
	}
}

// handleConn 处理单个连接（SNAPSHOT-8）。
//
// 协议：line-delimited JSON（每行一个 JSON-RPC 请求/响应）。
// 单连接可发送多个请求，连接关闭时结束。
func (s *UDSServer) handleConn(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(UDSReadTimeout)); err != nil {
			return
		}
		line, tooLarge, err := readLimitedLine(reader, s.cfg.MaxMessageSize)
		if err != nil {
			return // 连接关闭或超时
		}

		// 大小检查
		if tooLarge {
			s.writeResponse(conn, JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &RPCError{Code: -32604, Message: fmt.Sprintf("message exceeds maximum size of %d bytes", s.cfg.MaxMessageSize)},
				ID:      0,
			})
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeResponse(conn, JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &RPCError{Code: -32700, Message: "failed to parse request"},
				ID:      0,
			})
			continue
		}

		// 认证
		if !s.authenticate(req) {
			s.writeResponse(conn, JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &RPCError{Code: -32605, Message: "authentication failed: invalid or missing token"},
				ID:      req.ID,
			})
			continue
		}

		// 复用 IPCServer 的 handleRequest 逻辑
		ipcServer := &IPCServer{daemon: s.daemon}
		resp := ipcServer.handleRequest(req)
		s.writeResponse(conn, resp)
	}
}

func readLimitedLine(reader *bufio.Reader, maxSize int64) ([]byte, bool, error) {
	line := make([]byte, 0, min(int(maxSize), 4096))
	tooLarge := false
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil {
			return nil, false, err
		}
		if !tooLarge {
			if int64(len(line)+len(fragment)) > maxSize {
				tooLarge = true
			} else {
				line = append(line, fragment...)
			}
		}
		if !isPrefix {
			return line, tooLarge, nil
		}
	}
}

// authenticate 校验请求的 auth token（SNAPSHOT-9）。
func (s *UDSServer) authenticate(req JSONRPCRequest) bool {
	// PCR G-P1-1 收敛（I1）：委托给包内单一强制点 authorize（auth.go）。
	// 空 token 的 fail-closed 语义由 authorize 统一保证。
	return authorize(s.cfg.AuthToken, req.Token)
}

// writeResponse 写入 JSON-RPC 响应（SNAPSHOT-10）。
func (s *UDSServer) writeResponse(conn net.Conn, resp JSONRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	data = append(data, '\n')

	if err := conn.SetWriteDeadline(time.Now().Add(UDSWriteTimeout)); err != nil {
		return
	}
	if _, err := conn.Write(data); err != nil {
		if s.logger != nil {
			s.logger.Error("UDS: write response error: %v", err)
		}
	}
}

// UDSClient 是 Unix Domain Socket 客户端（SNAPSHOT-11）。
type UDSClient struct {
	socketPath string
	timeout    time.Duration
}

// NewUDSClient 创建 UDS 客户端（SNAPSHOT-12）。
//
// 参数：
// - socketPath: socket 目录路径（与 UDSServer 共用）
func NewUDSClient(socketPath string) *UDSClient {
	return &UDSClient{
		socketPath: socketPath,
		timeout:    10 * time.Second,
	}
}

// SetTimeout 设置单次请求超时（SNAPSHOT-13）。
func (c *UDSClient) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
}

// SendCommand 发送 JSON-RPC 命令并等待响应（SNAPSHOT-14）。
//
// 行为：
// - 读取 auth token（与 IPCClient 共用 token 文件）
// - 建立新连接（短连接模式，简单可靠）
// - 发送 line-delimited JSON 请求
// - 读取一行 JSON 响应
// - 关闭连接
func (c *UDSClient) SendCommand(method string, params map[string]interface{}) (interface{}, error) {
	sockPath := filepath.Join(c.socketPath, UDSSocketFile)
	conn, err := net.DialTimeout("unix", sockPath, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("UDS: dial: %w", err)
	}
	defer conn.Close()

	// 读取 auth token
	token, err := ReadAuthToken(c.socketPath)
	if err != nil {
		return nil, err
	}

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
		Token:   token,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("UDS: marshal request: %w", err)
	}
	data = append(data, '\n')

	if err := conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, fmt.Errorf("UDS: set write deadline: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("UDS: write request: %w", err)
	}

	// 读响应
	if err := conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, fmt.Errorf("UDS: set read deadline: %w", err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("UDS: read response: %w", err)
	}

	var resp JSONRPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("UDS: parse response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error [%d]: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}
