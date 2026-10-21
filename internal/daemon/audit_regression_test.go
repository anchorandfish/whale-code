package daemon

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func auditTestConfig(t *testing.T) DaemonConfig {
	t.Helper()
	dir := t.TempDir()
	return DaemonConfig{
		WorkDir:     dir,
		MaxWorkers:  1,
		TaskTimeout: time.Second,
		PIDFilePath: filepath.Join(dir, "daemon.pid"),
		LogFilePath: filepath.Join(dir, "daemon.log"),
		SocketPath:  filepath.Join(dir, "ipc"),
		MetricsAddr: "127.0.0.1:0",
	}
}

func TestDaemonStartupRollbackAfterMetricsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve metrics address: %v", err)
	}
	defer listener.Close()

	cfg := auditTestConfig(t)
	cfg.MetricsAddr = listener.Addr().String()
	d := NewDaemon(cfg)
	if err := d.Start(); err == nil {
		t.Fatal("Start should fail when the metrics address is already in use")
	}
	if d.Status().Running {
		t.Fatal("daemon must not report running after startup rollback")
	}
	for _, path := range []string{
		cfg.PIDFilePath,
		filepath.Join(filepath.Dir(cfg.PIDFilePath), "daemon.ready"),
		filepath.Join(cfg.SocketPath, AuthTokenFile),
		filepath.Join(cfg.SocketPath, UDSSocketFile),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("startup rollback left resource %s (stat err=%v)", path, err)
		}
	}
	if d.pool != nil && d.pool.RunningCount() != 0 {
		t.Fatal("startup rollback left an active worker")
	}
}

func TestDaemonCorruptQueueFailsWithoutReadyMarker(t *testing.T) {
	cfg := auditTestConfig(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.PIDFilePath), "tasks.json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(cfg)
	if err := d.Start(); err == nil {
		t.Fatal("Start should fail instead of silently discarding a corrupt queue")
	}
	if IsReady(cfg.PIDFilePath) {
		t.Fatal("corrupt queue startup must not publish a ready marker")
	}
	if _, err := os.Stat(cfg.PIDFilePath); !os.IsNotExist(err) {
		t.Fatalf("PID file survived failed startup: %v", err)
	}
}

func TestDaemonIdentityHandshakeAndLifetimeSingleton(t *testing.T) {
	cfg := auditTestConfig(t)
	first := NewDaemon(cfg)
	if err := first.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	controller := NewController(cfg)
	status, err := controller.Status()
	if err != nil {
		_ = first.Stop()
		t.Fatalf("verified status: %v", err)
	}
	if status.InstanceID == "" || status.InstanceID != first.instanceID || status.ProtocolVersion != daemonProtocolVersion {
		_ = first.Stop()
		t.Fatalf("invalid verified identity: %#v", status)
	}

	second := NewDaemon(cfg)
	if err := second.Start(); err == nil {
		_ = second.Stop()
		_ = first.Stop()
		t.Fatal("second daemon acquired a lifetime-held singleton lock")
	}
	if err := first.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("lock was not released after Stop: %v", err)
	}
	if err := second.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestIPCStopWaitsBeforeCleanup(t *testing.T) {
	dir := t.TempDir()
	server := NewIPCServer(dir, &Daemon{})
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := os.WriteFile(server.cmdPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("IPC poller recreated storage after Stop: %v", err)
	}
}

func TestIPCDisablePollingKeepsTokenUntilStop(t *testing.T) {
	dir := t.TempDir()
	server := NewIPCServer(dir, &Daemon{})
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	token, err := ReadAuthToken(dir)
	if err != nil || token == "" {
		t.Fatalf("ReadAuthToken before disable = %q, %v", token, err)
	}
	server.DisablePolling()
	if got, err := ReadAuthToken(dir); err != nil || got != token {
		t.Fatalf("native transport token after disable = %q, %v; want %q", got, err, token)
	}

	request, err := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "status",
		ID:      1,
		Token:   token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.cmdPath, request, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(server.respPath); !os.IsNotExist(err) {
		t.Fatalf("disabled file transport produced a response: %v", err)
	}
	if _, err := os.Stat(server.cmdPath); err != nil {
		t.Fatalf("disabled file transport consumed command: %v", err)
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, path := range []string{server.cmdPath, filepath.Join(dir, AuthTokenFile)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Stop left IPC file %s: %v", path, err)
		}
	}
}

func TestIPCFilePollingStartsOnlyWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{queue: NewTaskQueue(filepath.Join(dir, "tasks.json"))}
	server := NewIPCServer(dir, d)
	if err := server.start(false); err != nil {
		t.Fatalf("token-only start: %v", err)
	}

	token, err := ReadAuthToken(dir)
	if err != nil {
		t.Fatalf("ReadAuthToken: %v", err)
	}
	request, err := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", Method: "status", ID: 1, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.cmdPath, request, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(server.respPath); !os.IsNotExist(err) {
		t.Fatalf("token-only IPC processed a file request: %v", err)
	}

	if err := server.enablePolling(); err != nil {
		t.Fatalf("enablePolling: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(server.respPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("enabled file poller did not process the pending request")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWritePIDFileClaimsSingleton(t *testing.T) {
	cfg := auditTestConfig(t)
	first := NewDaemon(cfg)
	second := NewDaemon(cfg)
	if err := first.writePIDFile(); err != nil {
		t.Fatalf("first PID claim: %v", err)
	}
	t.Cleanup(first.cleanup)
	if err := second.writePIDFile(); err == nil {
		t.Fatal("second PID claim unexpectedly overwrote the first")
	}
}

func TestControllerResolvesNormalizedCLIPlaceholder(t *testing.T) {
	normalized := NewDaemon(DaemonConfig{WorkDir: t.TempDir()}).GetConfig()
	if normalized.CliPath != "whale" {
		t.Fatalf("test precondition changed: CliPath=%q", normalized.CliPath)
	}
	controller := NewController(normalized)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if controller.Config().CliPath != executable {
		t.Fatalf("controller CliPath=%q, want current executable %q", controller.Config().CliPath, executable)
	}
}

func TestTaskQueueAddPersistedRollsBackOnSaveFailure(t *testing.T) {
	queue := NewTaskQueue(t.TempDir()) // A directory cannot be replaced by tasks.json.

	if _, err := queue.AddPersistedWithNotify("do not run", t.TempDir(), TaskDream, PriorityNormal); err == nil {
		t.Fatal("expected persistence failure")
	}
	if tasks := queue.List(); len(tasks) != 0 {
		t.Fatalf("failed durable add left %d in-memory tasks", len(tasks))
	}
}

func TestTaskQueueClaimPersistedRollsBackOnSaveFailure(t *testing.T) {
	queue := NewTaskQueue(t.TempDir()) // A directory cannot be replaced by tasks.json.
	task := queue.Add("do not run", t.TempDir(), TaskDream, PriorityNormal)

	if claimed, err := queue.ClaimNextPersisted(); err == nil || claimed != nil {
		t.Fatalf("ClaimNextPersisted() = (%v, %v), want (nil, error)", claimed, err)
	}
	stored := queue.Get(task.ID)
	if stored == nil || stored.Status != TaskPending || stored.StartedAt != nil {
		t.Fatalf("failed durable claim was not rolled back: %+v", stored)
	}
}

func TestTaskQueueCancelPersistedRollsBackOnSaveFailure(t *testing.T) {
	queue := NewTaskQueue(t.TempDir()) // A directory cannot be replaced by tasks.json.
	task := queue.Add("must remain pending", t.TempDir(), TaskDream, PriorityNormal)

	if _, err := queue.CancelPersisted(task.ID); err == nil {
		t.Fatal("expected persistence failure")
	}
	stored := queue.Get(task.ID)
	if stored == nil || stored.Status != TaskPending || stored.CompletedAt != nil {
		t.Fatalf("failed durable cancellation was not rolled back: %+v", stored)
	}
}

func TestIPCAddTaskReturnsErrorWhenPersistenceFails(t *testing.T) {
	queue := NewTaskQueue(t.TempDir()) // A directory cannot be replaced by tasks.json.
	d := NewDaemon(DaemonConfig{})
	d.queue = queue
	server := NewIPCServer(t.TempDir(), d)

	resp := server.handleRequest(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "add_task",
		Params: map[string]interface{}{
			"prompt":   "do not run",
			"workdir":  t.TempDir(),
			"type":     string(TaskDream),
			"priority": float64(PriorityNormal),
		},
	})
	if resp.Error == nil {
		t.Fatal("add_task acknowledged an unpersisted task")
	}
	if tasks := queue.List(); len(tasks) != 0 {
		t.Fatalf("failed add_task left %d in-memory tasks", len(tasks))
	}
}

func TestWorkerOutputCaptureIsBoundedWithoutShortWrites(t *testing.T) {
	var output boundedOutputBuffer
	if _, bypassesWrite := interface{}(&output).(io.ReaderFrom); bypassesWrite {
		t.Fatal("bounded output buffer exposes io.ReaderFrom and can bypass Write limits")
	}
	payload := make([]byte, workerOutputCaptureLimit+4096)

	written, err := output.Write(payload)
	if err != nil || written != len(payload) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", written, err, len(payload))
	}
	if output.Len() != workerOutputCaptureLimit {
		t.Fatalf("captured %d bytes, want cap %d", output.Len(), workerOutputCaptureLimit)
	}
	written, err = output.Write(payload[:1024])
	if err != nil || written != 1024 || output.Len() != workerOutputCaptureLimit {
		t.Fatalf("post-cap Write() = (%d, %v), captured=%d", written, err, output.Len())
	}
}
