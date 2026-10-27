//go:build !windows

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/testutil"
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

	connCount := func() int {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.conns)
	}
	// ★ 前提必须**被断言**，不能"等不到就算了"。
	// 原实现是 `if accepted || time.Now().After(deadline) { break }` —— 上限到了直接往下走，
	// 于是连接根本没被登记时，后面的 Stop 测的是"空集合上的 Stop 很快" ⇒ 假绿。
	testutil.EventuallyObserving(t, "UDS server 已 accept 并登记该连接", func() bool {
		return connCount() == 1
	}, func() string {
		return testutil.FormatObserved("registered_conns", connCount())
	})

	started := time.Now()
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(started)

	// ★ 判据是"Stop 没有等在读超时上"，所以上界只能是**读超时本身**
	// （`UDSReadTimeout` = 10s）。原来写 `elapsed > time.Second` 是拿"预期延迟"当阈值：
	// 机器一忙就可能 >1s，红的却是正常行为。
	if elapsed > UDSReadTimeout/2 {
		t.Fatalf("Stop 疑似等到了读超时才对端关闭（耗时 %v，读超时 %v）", elapsed, UDSReadTimeout)
	}

	// 机制面（比耗时更硬）：Stop 之后这条活跃连接必须**已经被服务端关掉**。
	// 只断言"快"不排除"很快返回但连接还开着"；读回 EOF/连接重置才是真关。
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, readErr := conn.Read(make([]byte, 1)); readErr == nil {
		t.Fatal("Stop 之后活跃连接仍可读：服务端没有主动关闭它")
	} else {
		var nerr net.Error
		if errors.As(readErr, &nerr) && nerr.Timeout() {
			t.Fatal("Stop 之后连接仍未关闭（一直等到读截止才返回 timeout）")
		}
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

	// ★ 2026-10-27 ：这里原来是 `waitForAuditCondition(t, 3*time.Second, ...)`。
	// 3s 是**拍出来的延迟阈值**，不是语义：实测（5 个包并发编译做负载）
	// 父进程写 PID 文件耗时从空闲 0.38s 波动到 **1.45–3.96s**，10 次里 4 次越过 3s
	// ⇒ 这条判据变成抛硬币（空闲 30/30 绿、带负载约 20% 红），CI 只能靠「失败包重试一次」兜着。
	// 现在：等事件（cond），预算交给 testutil 的**挂死兜底**（由 t.Deadline 推导）。
	testutil.EventuallyObserving(t, "任务进入 running 且父/子进程都已写出 PID 文件", func() bool {
		current := queue.Get(task.ID)
		return current != nil && current.Status == TaskRunning &&
			auditPIDReady(parentPIDPath) && auditPIDReady(childPIDPath)
	}, func() string {
		status := "<任务已不在队列>"
		if current := queue.Get(task.ID); current != nil {
			status = string(current.Status)
		}
		return testutil.FormatObserved(
			"status", status,
			"parent_pid_file", auditPIDReady(parentPIDPath),
			"child_pid_file", auditPIDReady(childPIDPath),
			"pool_running", pool.RunningCount(),
		)
	})
	parentPID := readAuditPID(t, parentPIDPath)
	childPID := readAuditPID(t, childPIDPath)

	if err := d.CancelTask(task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	testutil.EventuallyObserving(t, "取消后 worker 与整个进程组都已退出", func() bool {
		return pool.RunningCount() == 0 && !isProcessAlive(parentPID) && !isProcessAlive(childPID)
	}, func() string {
		return testutil.FormatObserved(
			"pool_running", pool.RunningCount(),
			"parent_alive", isProcessAlive(parentPID),
			"child_alive", isProcessAlive(childPID),
		)
	})
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

// ★ 2026-10-27 ：本文件原先自带一份 `waitForAuditCondition(t, timeout, cond, msg)`
// —— 「等待实现」的第 N 份拷贝。拷贝的害处不是重复几行，而是**预算口径可以各自漂移**
// （这里写 3s、别处写 100ms），于是同一个不变量在不同文件里判据强弱不同。
// 等待统一收口到 `internal/testutil.Eventually*`：一处实现、一套预算口径（挂死兜底）。
