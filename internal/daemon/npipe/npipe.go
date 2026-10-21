package npipe

// Package npipe — Windows Named Pipe IPC 传输（原 internal/remote/authproxy 迁出）。
//
// 2026-10-11 整面砍除 /remote 时随迁：daemon 的 Windows UDS 传输
// （uds_transport_windows.go）借用 NamedPipeTransport，与已砍除的 remote 面无关。
// 原包中的 API Key 代理（Proxy）与 UnixSocket/TCP 传输属 remote 面，随砍除删除。

// NamedPipeTransport Windows Named Pipe 传输层实现。
//
// Listen/Dial 方法按平台分别在 npipe_windows.go（实际实现，使用 SECURITY_ATTRIBUTES
// 限制 DACL 为当前用户 SID）与 npipe_other.go（stub 返回错误）中定义。
type NamedPipeTransport struct {
	// name Named Pipe 名称（如 whale-auth，对应 \\.\pipe\whale-auth）
	name string
}

// NewNamedPipeTransport 创建 Named Pipe 传输层。
//
// name 为 pipe 名称（不含 \\.\pipe\ 前缀）。
func NewNamedPipeTransport(name string) *NamedPipeTransport {
	return &NamedPipeTransport{name: name}
}
