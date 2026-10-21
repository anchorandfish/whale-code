//go:build windows

package daemon

import "fmt"

// nativeTransportStarted removes the less secure file command channel after
// the current-user-only Named Pipe is listening. The auth token remains owned
// by IPCServer and is removed by Stop.
func nativeTransportStarted(ipc *IPCServer) {
	ipc.DisablePolling()
}

func filePollingInitiallyEnabled() bool { return false }

func nativeTransportFailed(ipc *IPCServer) error { return ipc.enablePolling() }

func nativeTransportAddress(socketPath string) string {
	return fmt.Sprintf(`\\.\pipe\%s`, daemonPipeName(socketPath))
}
