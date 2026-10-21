//go:build !windows

// Package npipe — 非 Windows 平台 Named Pipe 传输层 stub.
//
// Named Pipe 是 Windows 平台特有的 IPC 机制，Linux/macOS 不支持。
// 本文件提供 Listen 方法的 stub 实现，返回 ErrNamedPipeNotSupported。
// 非 Windows 平台应使用 UnixSocketTransport 或 TCPTransport。
package npipe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
)

// ErrNamedPipeNotSupported 表示当前平台不支持 Windows Named Pipe。
var ErrNamedPipeNotSupported = errors.New("npipe: named pipe not supported on this platform")

// Listen 在非 Windows 平台返回错误。
//
// Named Pipe 是 Windows 特有 IPC 机制，Linux/macOS 不支持。
// 非 Windows 平台应使用 UnixSocketTransport 或 TCPTransport。
func (t *NamedPipeTransport) Listen(ctx context.Context) (net.Listener, error) {
	return nil, fmt.Errorf("%w (current OS: %s)", ErrNamedPipeNotSupported, runtime.GOOS)
}

func (t *NamedPipeTransport) Dial(ctx context.Context) (net.Conn, error) {
	return nil, fmt.Errorf("%w (current OS: %s)", ErrNamedPipeNotSupported, runtime.GOOS)
}
