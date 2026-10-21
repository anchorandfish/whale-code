//go:build !windows

package daemon

import "path/filepath"

// Unix retains file polling as a compatibility fallback beside its
// permission-restricted Unix domain socket.
func nativeTransportStarted(_ *IPCServer) {}

func filePollingInitiallyEnabled() bool { return true }

func nativeTransportFailed(_ *IPCServer) error { return nil }

func nativeTransportAddress(socketPath string) string {
	return filepath.Join(socketPath, UDSSocketFile)
}
