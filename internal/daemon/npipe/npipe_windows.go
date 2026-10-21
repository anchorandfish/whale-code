//go:build windows

// Package authproxy — Windows Named Pipe 传输层实现。
//
// 本文件在 Windows 平台上为 NamedPipeTransport 提供实际的 Listen 实现，
// 使用 golang.org/x/sys/windows.CreateNamedPipe 创建 Named Pipe 实例，
// 并通过 SECURITY_ATTRIBUTES 设置 DACL 限制为当前用户 SID，
// 防止同会话其他进程连接到 pipe（默认 SA 允许同会话匿名连接）。
//
// 安全设计：
//   - 通过 OpenCurrentProcessToken + GetTokenUser 获取当前用户 SID
//   - 使用 BuildSecurityDescriptor 构建 SD，DACL 仅包含一个 ACE：
//     当前用户 SID + GENERIC_ALL（完全控制）
//   - SD 的 Owner/Group 均设为当前用户 SID
//   - CreateNamedPipe 时传入该 SA，pipe 句柄仅当前用户可访问
//
// 官方源：
//   - https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipes @2026-07-21
//   - https://learn.microsoft.com/en-us/windows/win32/secauthz/security-descriptor-definition-language @2026-07-21
package npipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pipeBufferSize 是 Named Pipe 的输入/输出缓冲区大小（64KB）。
const pipeBufferSize = 65536

// npipeListener 实现 net.Listener 接口，监听 Windows Named Pipe。
//
// 使用 SECURITY_ATTRIBUTES 限制 DACL 为当前用户 SID，
// 防止同会话其他进程连接到 pipe。
type npipeListener struct {
	name      string                      // \\.\pipe\<name> 完整 pipe 路径
	sa        *windows.SecurityAttributes // 安全属性（含限制 DACL 的 SD）
	handleMu  sync.Mutex                  // 保护 handle 与 closed 字段
	handle    windows.Handle              // 当前等待连接的 pipe 实例句柄
	closed    bool                        // 是否已关闭
	closeOnce sync.Once                   // 保证 Close 幂等
}

// pipeAddr 实现 net.Addr，描述 Named Pipe 地址。
type pipeAddr struct{ name string }

// Network 返回网络类型标识。
func (a pipeAddr) Network() string { return "namedpipe" }

// String 返回 pipe 路径。
func (a pipeAddr) String() string { return a.name }

// Listen 在 Windows 上创建 Named Pipe listener。
//
// 行为：
//  1. 构建限制为当前用户 SID 的安全描述符（DACL 仅授权当前用户）
//  2. 用该 SD 创建 SECURITY_ATTRIBUTES
//  3. 调用 CreateNamedPipe 创建第一个 pipe 实例
//  4. 返回 npipeListener，后续 Accept 时创建新实例
//
// 安全：DACL 仅授权当前用户 SID，同会话其他进程无法连接。
//
// 官方源：https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createnamedpipea @2026-07-21
func (t *NamedPipeTransport) Listen(ctx context.Context) (net.Listener, error) {
	pipePath := `\\.\pipe\` + t.name

	// 构建限制为当前用户 SID 的安全描述符
	sd, err := buildCurrentUserSecurityDescriptor()
	if err != nil {
		return nil, fmt.Errorf("authproxy: build security descriptor failed: %w", err)
	}

	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
		InheritHandle:      0,
	}

	// 创建第一个 pipe 实例
	handle, err := createNamedPipeInstance(pipePath, sa)
	if err != nil {
		return nil, fmt.Errorf("authproxy: create named pipe failed: %w", err)
	}

	return &npipeListener{
		name:   pipePath,
		sa:     sa,
		handle: handle,
	}, nil
}

// Dial connects to this current-user-only named pipe. CreateFile is retried
// while every server instance is busy, bounded by the supplied context.
func (t *NamedPipeTransport) Dial(ctx context.Context) (net.Conn, error) {
	pipePath := `\\.\pipe\` + t.name
	namePtr, err := windows.UTF16PtrFromString(pipePath)
	if err != nil {
		return nil, err
	}
	for {
		handle, err := windows.CreateFile(
			namePtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			0,
			0,
		)
		if err == nil {
			return &npipeClientConn{handle: handle, name: pipePath}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("namedpipe: dial failed: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("namedpipe: dial: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// createNamedPipeInstance 创建一个 Named Pipe 实例，使用指定的 SECURITY_ATTRIBUTES。
//
// 参数：
//   - path: pipe 路径（如 \\.\pipe\whale-auth）
//   - sa: 安全属性（含限制 DACL 的 SD）
//
// 返回：pipe 句柄与错误
//
// 官方源：https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createnamedpipea @2026-07-21
func createNamedPipeInstance(path string, sa *windows.SecurityAttributes) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// PIPE_ACCESS_DUPLEX: 双向通信
	// PIPE_TYPE_BYTE | PIPE_WAIT: 字节流 + 阻塞模式
	// PIPE_UNLIMITED_INSTANCES: 多实例上限
	return windows.CreateNamedPipe(
		namePtr,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES,
		pipeBufferSize,
		pipeBufferSize,
		0, // default timeout
		sa,
	)
}

// Accept 阻塞等待客户端连接，返回封装为 net.Conn 的连接。
//
// 行为：
//  1. ConnectNamedPipe 阻塞等待客户端连接
//  2. 连接成功后，立即创建下一个 pipe 实例供下次 Accept 使用
//  3. 将当前已连接的 handle 封装为 npipeConn 返回
//
// ERROR_PIPE_CONNECTED (535) 不是错误，表示客户端在 ConnectNamedPipe
// 调用之前已连接，正常处理即可。
//
// 官方源：https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-connectnamedpipe @2026-07-21
func (l *npipeListener) Accept() (net.Conn, error) {
	l.handleMu.Lock()
	if l.closed {
		l.handleMu.Unlock()
		return nil, fmt.Errorf("namedpipe: listener closed")
	}
	handle := l.handle
	l.handleMu.Unlock()

	// 阻塞等待客户端连接
	err := windows.ConnectNamedPipe(handle, nil)
	if err != nil {
		// ERROR_PIPE_CONNECTED (535): 客户端在 ConnectNamedPipe 之前已连接，正常情况
		if errno, ok := err.(syscall.Errno); ok && errno == windows.ERROR_PIPE_CONNECTED {
			// 已连接，继续处理
		} else {
			// 其他错误：检查是否已关闭
			l.handleMu.Lock()
			closed := l.closed
			l.handleMu.Unlock()
			if closed {
				return nil, fmt.Errorf("namedpipe: listener closed")
			}
			return nil, fmt.Errorf("namedpipe: connect failed: %w", err)
		}
	}

	// 创建下一个 pipe 实例供下次 Accept 使用（复用同一 SA，保持 DACL 限制）
	nextHandle, createErr := createNamedPipeInstance(l.name, l.sa)
	if createErr != nil {
		// 创建失败：断开当前连接，返回错误
		_ = windows.DisconnectNamedPipe(handle)
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("namedpipe: create next instance failed: %w", createErr)
	}

	// 原子替换当前 handle
	l.handleMu.Lock()
	if l.closed {
		l.handleMu.Unlock()
		// 关闭中：清理新实例与当前已连接实例
		_ = windows.CloseHandle(nextHandle)
		_ = windows.DisconnectNamedPipe(handle)
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("namedpipe: listener closed")
	}
	l.handle = nextHandle
	l.handleMu.Unlock()

	return &npipeConn{handle: handle, name: l.name}, nil
}

// Close 关闭 listener，停止接受新连接。
//
// 关闭当前等待连接的 pipe 实例，使阻塞中的 ConnectNamedPipe 返回错误。
// 重复调用 Close 是安全的（sync.Once 保证幂等）。
func (l *npipeListener) Close() error {
	l.closeOnce.Do(func() {
		l.handleMu.Lock()
		l.closed = true
		handle := l.handle
		l.handle = 0
		l.handleMu.Unlock()
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	})
	return nil
}

// Addr 返回 listener 监听地址。
func (l *npipeListener) Addr() net.Addr {
	return pipeAddr{name: l.name}
}

// npipeConn 实现 net.Conn，包装 Named Pipe 已连接的句柄。
//
// Read/Write 直接调用 windows.ReadFile/WriteFile（阻塞模式）。
// Close 先 DisconnectNamedPipe 再 CloseHandle。
type npipeConn struct {
	handle    windows.Handle
	name      string
	closeOnce sync.Once
}

type npipeClientConn struct {
	handle    windows.Handle
	name      string
	closeOnce sync.Once
}

func (c *npipeClientConn) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(c.handle, b, &n, nil)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return 0, io.EOF
	}
	return int(n), err
}

func (c *npipeClientConn) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(c.handle, b, &n, nil)
	return int(n), err
}

func (c *npipeClientConn) Close() error {
	c.closeOnce.Do(func() { _ = windows.CloseHandle(c.handle) })
	return nil
}

func (c *npipeClientConn) LocalAddr() net.Addr              { return pipeAddr{name: c.name} }
func (c *npipeClientConn) RemoteAddr() net.Addr             { return pipeAddr{name: c.name} }
func (c *npipeClientConn) SetDeadline(time.Time) error      { return nil }
func (c *npipeClientConn) SetReadDeadline(time.Time) error  { return nil }
func (c *npipeClientConn) SetWriteDeadline(time.Time) error { return nil }

// Read 从 pipe 读取数据。
//
// ERROR_BROKEN_PIPE (232) 表示客户端已断开，返回 io.EOF。
func (c *npipeConn) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(c.handle, b, &n, nil)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == windows.ERROR_BROKEN_PIPE {
			return 0, io.EOF
		}
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

// Write 向 pipe 写入数据。
func (c *npipeConn) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(c.handle, b, &n, nil)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// Close 关闭连接：先断开 pipe 再关闭句柄。
//
// 重复调用是安全的（sync.Once 保证幂等）。
func (c *npipeConn) Close() error {
	c.closeOnce.Do(func() {
		_ = windows.DisconnectNamedPipe(c.handle)
		_ = windows.CloseHandle(c.handle)
	})
	return nil
}

// LocalAddr 返回本地地址（pipe 路径）。
func (c *npipeConn) LocalAddr() net.Addr { return pipeAddr{name: c.name} }

// RemoteAddr 返回远端地址（Named Pipe 无对端概念，返回 pipe 路径）。
func (c *npipeConn) RemoteAddr() net.Addr { return pipeAddr{name: c.name} }

// SetDeadline 设置读写截止时间（当前实现不支持，返回 nil）。
func (c *npipeConn) SetDeadline(t time.Time) error { return nil }

// SetReadDeadline 设置读截止时间（当前实现不支持，返回 nil）。
func (c *npipeConn) SetReadDeadline(t time.Time) error { return nil }

// SetWriteDeadline 设置写截止时间（当前实现不支持，返回 nil）。
func (c *npipeConn) SetWriteDeadline(t time.Time) error { return nil }

// buildCurrentUserSecurityDescriptor 构建限制为当前用户 SID 的安全描述符。
//
// 行为：
//  1. OpenCurrentProcessToken 打开当前进程 token
//  2. GetTokenUser 获取当前用户 SID
//  3. BuildSecurityDescriptor 构建 SD：
//     - Owner = Group = 当前用户 SID
//     - DACL 仅包含一个 ACE：当前用户 SID + GENERIC_ALL（完全控制）
//
// 返回的 SD 为 self-relative 形式，可直接用于 SECURITY_ATTRIBUTES.SecurityDescriptor。
//
// 安全：DACL 仅授权当前用户 SID，同会话其他进程无法访问 pipe。
//
// 官方源：
//   - https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-buildsecuritydescriptorw @2026-07-21
//   - https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-gettokeninformation @2026-07-21
func buildCurrentUserSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	// 获取当前进程 token
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("open process token: %w", err)
	}
	defer token.Close()

	// 获取当前用户 SID
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get token user: %w", err)
	}
	userSID := user.User.Sid
	if userSID == nil {
		return nil, fmt.Errorf("current user SID is nil")
	}

	// 构建 trustee: 当前用户（owner/group 共用）
	trustee := windows.TRUSTEE{
		TrusteeForm:  windows.TRUSTEE_IS_SID,
		TrusteeType:  windows.TRUSTEE_IS_USER,
		TrusteeValue: windows.TrusteeValueFromSID(userSID),
	}

	// DACL: 仅允许当前用户完全控制（GENERIC_ALL）
	explicitAccess := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee:           trustee,
		},
	}

	// 构建 SD：owner = current user, group = current user, DACL = current user only
	sd, err := windows.BuildSecurityDescriptor(&trustee, &trustee, explicitAccess, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build security descriptor: %w", err)
	}

	return sd, nil
}
