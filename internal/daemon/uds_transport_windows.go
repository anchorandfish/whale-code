//go:build windows

package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/daemon/npipe"
)

const (
	UDSMaxConnections = 16
	UDSReadTimeout    = 10 * time.Second
	UDSWriteTimeout   = 10 * time.Second
	UDSSocketFile     = "daemon.sock"
)

type UDSConfig struct {
	SocketPath     string
	AuthToken      string
	MaxMessageSize int64
}

// UDSServer retains the cross-platform API name but uses a Windows Named Pipe
// protected by a current-user-only DACL.
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

func NewUDSServer(cfg UDSConfig, daemon *Daemon) *UDSServer {
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = MaxMessageSize
	}
	return &UDSServer{
		cfg: cfg, daemon: daemon, stopCh: make(chan struct{}), conns: make(map[net.Conn]struct{}),
	}
}

func (s *UDSServer) SetLogger(logger *DaemonLogger) {
	s.mu.Lock()
	s.logger = logger
	s.mu.Unlock()
}

func (s *UDSServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("named pipe server is already running")
	}
	listener, err := npipe.NewNamedPipeTransport(daemonPipeName(s.cfg.SocketPath)).Listen(context.Background())
	if err != nil {
		return fmt.Errorf("named pipe listen: %w", err)
	}
	s.listener = listener
	s.running = true
	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

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
	return nil
}

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
					s.logger.Error("named pipe accept error: %v", err)
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
			_ = conn.Close()
		}
	}
}

func (s *UDSServer) handleConn(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		type lineResult struct {
			line     []byte
			tooLarge bool
			err      error
		}
		readCh := make(chan lineResult, 1)
		go func() {
			line, tooLarge, err := readLimitedLine(reader, s.cfg.MaxMessageSize)
			readCh <- lineResult{line: line, tooLarge: tooLarge, err: err}
		}()
		var read lineResult
		select {
		case read = <-readCh:
		case <-time.After(UDSReadTimeout):
			_ = conn.Close()
			return
		case <-s.stopCh:
			_ = conn.Close()
			return
		}
		if read.err != nil {
			return
		}
		if read.tooLarge {
			s.writeResponse(conn, JSONRPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32604, Message: fmt.Sprintf("message exceeds maximum size of %d bytes", s.cfg.MaxMessageSize)}})
			continue
		}
		var req JSONRPCRequest
		if err := json.Unmarshal(read.line, &req); err != nil {
			s.writeResponse(conn, JSONRPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32700, Message: "failed to parse request"}})
			continue
		}
		if !s.authenticate(req) {
			s.writeResponse(conn, JSONRPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: -32605, Message: "authentication failed: invalid or missing token"}, ID: req.ID})
			continue
		}
		resp := (&IPCServer{daemon: s.daemon}).handleRequest(req)
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

// PCR G-P1-1 收敛（I1）：与 !windows 变体共用包内单一强制点 authorize（auth.go）。
// 此前的单表达式副本与本变体因 build tag 互斥，任一时刻只有一份参与编译，
// 故二者之间的漂移无法被单平台测试发现——收敛后不再存在该盲区。
func (s *UDSServer) authenticate(req JSONRPCRequest) bool {
	return authorize(s.cfg.AuthToken, req.Token)
}

func (s *UDSServer) writeResponse(conn net.Conn, resp JSONRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	data = append(data, '\n')
	writeCh := make(chan error, 1)
	go func() {
		_, err := conn.Write(data)
		writeCh <- err
	}()
	select {
	case <-writeCh:
	case <-time.After(UDSWriteTimeout):
		_ = conn.Close()
	case <-s.stopCh:
		_ = conn.Close()
	}
}

type UDSClient struct {
	socketPath string
	timeout    time.Duration
}

func NewUDSClient(socketPath string) *UDSClient {
	return &UDSClient{socketPath: socketPath, timeout: 10 * time.Second}
}

func (c *UDSClient) SetTimeout(timeout time.Duration) {
	if timeout > 0 {
		c.timeout = timeout
	}
}

func (c *UDSClient) SendCommand(method string, params map[string]interface{}) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	conn, err := npipe.NewNamedPipeTransport(daemonPipeName(c.socketPath)).Dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	token, err := ReadAuthToken(c.socketPath)
	if err != nil {
		return nil, err
	}
	req := JSONRPCRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1, Token: token}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("named pipe write: %w", err)
	}
	type readResult struct {
		line []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		readCh <- readResult{line: line, err: err}
	}()
	var result readResult
	select {
	case result = <-readCh:
	case <-ctx.Done():
		_ = conn.Close()
		return nil, fmt.Errorf("named pipe response: %w", ctx.Err())
	}
	if result.err != nil {
		return nil, fmt.Errorf("named pipe read: %w", result.err)
	}
	var resp JSONRPCResponse
	if err := json.Unmarshal(result.line, &resp); err != nil {
		return nil, fmt.Errorf("named pipe parse response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error [%d]: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func daemonPipeName(socketPath string) string {
	normalized := strings.ToLower(filepath.Clean(socketPath))
	hash := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("whale-daemon-%x", hash[:8])
}
