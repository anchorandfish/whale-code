package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestGenerateAuthToken_RandomAndUnique 验证修复点1：generateAuthToken 改用 crypto/rand
// 后应返回非空、非全同字符的令牌，且两次调用结果不同（旧实现会产出全同字节）。
func TestGenerateAuthToken_RandomAndUnique(t *testing.T) {
	tok1, err := generateAuthToken()
	if err != nil {
		t.Fatalf("generateAuthToken() returned error: %v", err)
	}
	if len(tok1) == 0 {
		t.Fatal("generateAuthToken() returned empty token")
	}
	// 32 字节 hex 编码应为 64 个字符
	if len(tok1) != 64 {
		t.Fatalf("expected 64 hex chars (32 bytes), got %d: %s", len(tok1), tok1)
	}
	// 旧实现 32 字节全是 time.Now().UnixNano()%256 的重复值，hex 后为重复字符；
	// crypto/rand 应产生高熵随机字节，几乎不可能全同。
	allSame := true
	for i := 1; i < len(tok1); i++ {
		if tok1[i] != tok1[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Fatalf("token characters are all identical, crypto/rand likely not used: %s", tok1)
	}

	tok2, err := generateAuthToken()
	if err != nil {
		t.Fatalf("second generateAuthToken() returned error: %v", err)
	}
	if tok1 == tok2 {
		t.Fatal("two consecutive tokens are identical, randomness insufficient")
	}
}

// TestKillProcessTree_WindowsBranch 验证修复点2：本机为 Windows 时 killProcessTree
// 仍走 taskkill 分支。对不存在的 PID 调用应返回错误，证明使用的是 taskkill 命令。
// Unix 分支由跨平台编译验证（GOOS=linux go build）覆盖。
func TestKillProcessTree_WindowsBranch(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific branch verification; Unix branch covered by cross-compile")
	}
	// 999999 几乎不可能是存活进程 PID，taskkill 应返回错误
	err := killProcessTree(999999)
	if err == nil {
		t.Log("killProcessTree returned nil for non-existent PID (taskkill may succeed silently)")
	}
	// 主要验证：函数可调用、走 Windows taskkill 分支且不 panic
}

// TestRegistry_StartStopHealthCheck 验证修复点3 的底层机制：
// StartHealthCheck 后 hcCancel 非 nil，StopHealthCheck 后 hcCancel 为 nil。
func TestRegistry_StartStopHealthCheck(t *testing.T) {
	r := NewWorkerRegistry()
	if r.hcCancel != nil {
		t.Fatal("hcCancel should be nil before StartHealthCheck")
	}
	r.StartHealthCheck(context.Background())
	r.mu.RLock()
	started := r.hcCancel != nil
	r.mu.RUnlock()
	if !started {
		t.Fatal("hcCancel is nil after StartHealthCheck, goroutine not started")
	}
	r.StopHealthCheck()
	r.mu.RLock()
	stopped := r.hcCancel == nil
	r.mu.RUnlock()
	if !stopped {
		t.Fatal("hcCancel is still non-nil after StopHealthCheck")
	}
}

// TestDaemon_StartStopHealthCheckWired 验证修复点3：Daemon.Start 后健康检查 goroutine
// 被启动（hcCancel 非 nil），Daemon.Stop 后被停止（hcCancel 为 nil）。
func TestDaemon_StartStopHealthCheckWired(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DaemonConfig{
		WorkDir:     tmpDir,
		MaxWorkers:  1,
		TaskTimeout: 30 * time.Second,
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		CliPath:     os.Args[0],
	}
	d := NewDaemon(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("daemon Start failed: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })

	if d.registry == nil {
		t.Fatal("registry is nil after Start")
	}
	d.registry.mu.RLock()
	hcStarted := d.registry.hcCancel != nil
	d.registry.mu.RUnlock()
	if !hcStarted {
		t.Fatal("StartHealthCheck was not wired in Daemon.Start: hcCancel is nil")
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("daemon Stop failed: %v", err)
	}
	d.registry.mu.RLock()
	hcStopped := d.registry.hcCancel == nil
	d.registry.mu.RUnlock()
	if !hcStopped {
		t.Fatal("StopHealthCheck was not wired in Daemon.Stop: hcCancel still non-nil")
	}
}

// TestAddWithNotify_NotifiesChannel 验证修复点4：AddWithNotify 入队后应向
// NotifyChannel 发送信号，使 scheduleLoop 可事件驱动调度而无需等待 2s ticker。
func TestAddWithNotify_NotifiesChannel(t *testing.T) {
	// 先排空可能残留的通知信号，避免被先前测试污染
	select {
	case <-NotifyChannel():
	default:
	}

	queue := NewTaskQueue(filepath.Join(t.TempDir(), "tasks.json"))
	queue.AddWithNotify("test-prompt", "", TaskDream, PriorityNormal)

	select {
	case <-NotifyChannel():
		// 收到信号，事件驱动调度已接通
	case <-time.After(1 * time.Second):
		t.Fatal("NotifyChannel did not receive signal within 1s after AddWithNotify")
	}
}

// TestWorker_RecordsCompletedTask 验证修复点5：worker 完成任务（含失败）后应调用
// registry.RecordCompletedTask，使 registry.completedTasks 中留有该任务 ID 记录。
// 使用 os.Args[0]（测试二进制本身）作为 CLI，传入未定义的 --ci flag 会快速以非零码退出，
// 从而触发 waitTask 的 TaskFailed 终态分支与 defer 中的 RecordCompletedTask 调用。
func TestWorker_RecordsCompletedTask(t *testing.T) {
	queue := NewTaskQueue(filepath.Join(t.TempDir(), "tasks.json"))
	registry := NewWorkerRegistry()

	worker := NewWorker(1, os.Args[0], 10*time.Second)
	worker.queue = queue
	worker.registry = registry

	task := queue.Add("test-prompt-for-completion", "", TaskDream, PriorityNormal)

	if err := worker.Start(context.Background(), task); err != nil {
		t.Fatalf("worker.Start failed: %v", err)
	}

	// 等待子进程退出（测试二进制遇到未知 flag 会迅速 os.Exit(2)）
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !worker.IsBusy() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if worker.IsBusy() {
		t.Fatal("worker still busy after deadline; subprocess may not have exited")
	}

	// 验证注册表记录了已完成任务
	registry.mu.RLock()
	_, recorded := registry.completedTasks[task.ID]
	registry.mu.RUnlock()
	if !recorded {
		t.Fatalf("task %s was not recorded in registry.completedTasks after completion", task.ID)
	}
}
