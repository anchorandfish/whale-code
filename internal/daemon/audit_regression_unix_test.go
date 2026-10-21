//go:build !windows

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "whale-daemon-")
	if err != nil {
		t.Fatalf("create short socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestUDSAuthenticatedRoundTrip(t *testing.T) {
	dir := shortSocketDir(t)
	token := "test-auth-token"
	if err := os.WriteFile(filepath.Join(dir, AuthTokenFile), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{running: true, started: time.Now(), stopCh: make(chan struct{})}
	server := NewUDSServer(UDSConfig{SocketPath: dir, AuthToken: token}, d)
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	result, err := NewUDSClient(dir).SendCommand("status", nil)
	if err != nil {
		t.Fatalf("authenticated status: %v", err)
	}
	var status DaemonStatus
	data, _ := json.Marshal(result)
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Running {
		t.Fatal("UDS status did not reach daemon handler")
	}
}

func TestUDSStopClosesActiveConnection(t *testing.T) {
	dir := shortSocketDir(t)
	server := NewUDSServer(UDSConfig{SocketPath: dir, AuthToken: "token"}, &Daemon{})
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, UDSSocketFile))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		accepted := len(server.conns) == 1
		server.mu.Unlock()
		if accepted || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	started := time.Now()
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Stop waited for read timeout instead of closing the active connection: %v", elapsed)
	}
}

func TestUDSOversizedRequestIsRejectedAndConnectionRemainsUsable(t *testing.T) {
	dir := shortSocketDir(t)
	d := &Daemon{running: true, started: time.Now(), stopCh: make(chan struct{})}
	server := NewUDSServer(UDSConfig{SocketPath: dir, AuthToken: "token", MaxMessageSize: 128}, d)
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	conn, err := net.Dial("unix", filepath.Join(dir, UDSSocketFile))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := fmt.Fprintf(conn, "%s\n", strings.Repeat("x", 1024)); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var oversized JSONRPCResponse
	if err := json.Unmarshal(line, &oversized); err != nil || oversized.Error == nil || oversized.Error.Code != -32604 {
		t.Fatalf("unexpected oversized response: %s (decode err=%v)", line, err)
	}
	req := JSONRPCRequest{JSONRPC: "2.0", Method: "status", ID: 2, Token: "token"}
	data, _ := json.Marshal(req)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("connection was not reusable: %v", err)
	}
	var statusResp JSONRPCResponse
	if err := json.Unmarshal(line, &statusResp); err != nil || statusResp.Error != nil {
		t.Fatalf("status after oversized request failed: %s (decode err=%v)", line, err)
	}
}

func TestCancelRunningTaskKillsIgnoringProcessGroup(t *testing.T) {
	dir := t.TempDir()
	parentPIDPath := filepath.Join(dir, "parent.pid")
	childPIDPath := filepath.Join(dir, "child.pid")
	scriptPath := filepath.Join(dir, "fake-whale")
	script := "#!/bin/sh\n" +
		"trap '' TERM\n" +
		"printf '%s' \"$$\" > \"$WHALE_TEST_PARENT_PID\"\n" +
		"sh -c 'trap \"\" TERM; printf \"%s\" \"$$\" > \"$WHALE_TEST_CHILD_PID\"; while :; do sleep 1; done' &\n" +
		"wait\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHALE_TEST_PARENT_PID", parentPIDPath)
	t.Setenv("WHALE_TEST_CHILD_PID", childPIDPath)

	queue := NewTaskQueue(filepath.Join(dir, "tasks.json"))
	pool := NewWorkerPool(1, scriptPath, 30*time.Second)
	pool.Start(context.Background(), queue)
	t.Cleanup(pool.Stop)
	d := &Daemon{queue: queue, pool: pool, events: NewEventBus()}
	task := queue.AddWithNotify("ignored", dir, TaskDream, PriorityNormal)

	waitForAuditCondition(t, 3*time.Second, func() bool {
		current := queue.Get(task.ID)
		return current != nil && current.Status == TaskRunning &&
			auditPIDReady(parentPIDPath) && auditPIDReady(childPIDPath)
	}, "task and child process did not start")
	parentPID := readAuditPID(t, parentPIDPath)
	childPID := readAuditPID(t, childPIDPath)

	if err := d.CancelTask(task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitForAuditCondition(t, 3*time.Second, func() bool {
		return pool.RunningCount() == 0 && !isProcessAlive(parentPID) && !isProcessAlive(childPID)
	}, "cancelled process group remained alive")
	current := queue.Get(task.ID)
	if current == nil || current.Status != TaskCancelled {
		t.Fatalf("worker completion overwrote cancellation: %#v", current)
	}
}

func readAuditPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func auditPIDReady(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return err == nil && pid > 0
}

func waitForAuditCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}
