package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/testutil"
)

// ========== PID 文件管理测试 ==========

// TestWriteAndReadPIDFile 测试 PID 文件的写入和读取
func TestWriteAndReadPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")

	d := &Daemon{
		cfg: DaemonConfig{
			PIDFilePath: pidPath,
		},
	}

	// 写入 PID 文件
	if err := d.writePIDFile(); err != nil {
		t.Fatalf("写入 PID 文件失败: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Fatal("PID 文件不存在")
	}

	// 读取 PID 文件
	pid, err := d.readPIDFile()
	if err != nil {
		t.Fatalf("读取 PID 文件失败: %v", err)
	}

	if pid != os.Getpid() {
		t.Errorf("PID 不匹配: expected %d, actual %d", os.Getpid(), pid)
	}
}

// TestReadPIDFileNotExist 测试读取不存在的 PID 文件
func TestReadPIDFileNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "nonexistent.pid")

	d := &Daemon{
		cfg: DaemonConfig{
			PIDFilePath: pidPath,
		},
	}

	_, err := d.readPIDFile()
	if err == nil {
		t.Error("expected返回错误，但返回 nil")
	}
}

// TestCleanupStalePIDFile test cleanup过期的 PID 文件
func TestCleanupStalePIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")

	// 写入一个不存在的 PID
	os.WriteFile(pidPath, []byte("99999999"), 0644)

	d := &Daemon{
		cfg: DaemonConfig{
			PIDFilePath: pidPath,
		},
	}

	// 清理过期 PID 文件
	err := d.CleanupStale()
	if err != nil {
		t.Fatalf("清理过期 PID 文件失败: %v", err)
	}

	// 验证文件已被删除
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("过期 PID 文件未被清理")
	}
}

// TestIsRunningNoPIDFile 测试无 PID 文件时 IsRunning 返回 false
func TestIsRunningNoPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")

	d := &Daemon{
		cfg: DaemonConfig{
			PIDFilePath: pidPath,
		},
	}

	if d.IsRunning() {
		t.Error("PID 文件不存在时 IsRunning should return false")
	}
}

// ========== 任务队列 CRUD 测试 ==========

// TestTaskQueueAdd 测试添加任务
func TestTaskQueueAdd(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	task := q.Add("hello world", "/tmp", TaskDream, PriorityNormal)
	if task == nil {
		t.Fatal("添加任务返回 nil")
	}
	if task.ID == "" {
		t.Error("任务 ID should not be empty")
	}
	if task.Prompt != "hello world" {
		t.Errorf("Prompt 不匹配: expected 'hello world', actual '%s'", task.Prompt)
	}
	if task.Status != TaskPending {
		t.Errorf("状态应为 pending, actual %s", task.Status)
	}
	if task.WorkDir != "/tmp" {
		t.Errorf("WorkDir 不匹配: expected '/tmp', actual '%s'", task.WorkDir)
	}
}

// TestTaskQueueGet 测试获取任务
func TestTaskQueueGet(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	task := q.Add("test prompt", "/work", TaskDream, PriorityNormal)

	// 通过 ID 获取
	found := q.Get(task.ID)
	if found == nil {
		t.Fatal("未找到已添加的任务")
	}
	if found.ID != task.ID {
		t.Errorf("ID 不匹配: expected %s, actual %s", task.ID, found.ID)
	}

	// 不存在的 ID
	notFound := q.Get("nonexistent")
	if notFound != nil {
		t.Error("不存在的 ID should return nil")
	}
}

// TestTaskQueueList 测试列出所有任务
func TestTaskQueueList(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	q.Add("task1", "/dir1", TaskDream, PriorityNormal)
	q.Add("task2", "/dir2", TaskDream, PriorityNormal)
	q.Add("task3", "/dir3", TaskDream, PriorityNormal)

	list := q.List()
	if len(list) != 3 {
		t.Errorf("任务数量不匹配: expected 3, actual %d", len(list))
	}
}

// TestTaskQueueNext 测试获取下一个待执行任务
func TestTaskQueueNext(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	// 空队列
	if next := q.Next(); next != nil {
		t.Error("空队列should return nil")
	}

	// 添加任务
	task1 := q.Add("first", "/dir1", TaskDream, PriorityNormal)
	q.Add("second", "/dir2", TaskDream, PriorityNormal)

	// 获取第一个待执行任务
	next := q.Next()
	if next == nil || next.ID != task1.ID {
		t.Error("Next should return第一个 pending 任务")
	}

	// 更新状态后获取下一个
	q.UpdateStatus(task1.ID, TaskRunning, "")
	next2 := q.Next()
	if next2 == nil || next2.Status != TaskPending {
		t.Error("Next should return下一个 pending 任务")
	}
}

// TestTaskQueueUpdateStatus 测试更新任务状态
func TestTaskQueueUpdateStatus(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	task := q.Add("test", "/dir", TaskDream, PriorityNormal)

	// 更新为 running
	err := q.UpdateStatus(task.ID, TaskRunning, "")
	if err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}
	found := q.Get(task.ID)
	if found.Status != TaskRunning {
		t.Errorf("状态应为 running, actual %s", found.Status)
	}
	if found.StartedAt == nil {
		t.Error("StartedAt 不should be nil")
	}

	// 更新为 completed
	err = q.UpdateStatus(task.ID, TaskCompleted, "done")
	if err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}
	found = q.Get(task.ID)
	if found.Status != TaskCompleted {
		t.Errorf("状态应为 completed, actual %s", found.Status)
	}
	if found.Result != "done" {
		t.Errorf("Result 不匹配: expected 'done', actual '%s'", found.Result)
	}
	if found.CompletedAt == nil {
		t.Error("CompletedAt 不should be nil")
	}

	// 不存在的任务
	err = q.UpdateStatus("nonexistent", TaskRunning, "")
	if err == nil {
		t.Error("不存在的任务should return error")
	}
}

// TestTaskQueueCancel test cancelled任务
func TestTaskQueueCancel(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	task := q.Add("test", "/dir", TaskDream, PriorityNormal)

	// 取消 pending 任务
	err := q.Cancel(task.ID)
	if err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	found := q.Get(task.ID)
	if found.Status != TaskCancelled {
		t.Errorf("状态应为 cancelled, actual %s", found.Status)
	}

	// 取消已完成的任务应失败
	task2 := q.Add("test2", "/dir", TaskDream, PriorityNormal)
	q.UpdateStatus(task2.ID, TaskCompleted, "done")
	err = q.Cancel(task2.ID)
	if err == nil {
		t.Error("取消已完成的任务should return error")
	}
}

// ========== 任务持久化和恢复测试 ==========

// TestTaskQueueSaveAndLoad 测试任务队列持久化和恢复
func TestTaskQueueSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")

	// 创建队列并添加任务
	q1 := NewTaskQueue(storePath)
	// ★ （Windows CI）：SQLite 存储句柄先关，t.TempDir 清理才能删掉 tasks.db
	// （Windows 不许删除仍被打开的文件；POSIX 容忍 ⇒ mac 上测不出）。
	t.Cleanup(func() { _ = q1.Close() })
	task1 := q1.Add("task one", "/dir1", TaskDream, PriorityNormal)
	q1.Add("task two", "/dir2", TaskDream, PriorityNormal)
	q1.UpdateStatus(task1.ID, TaskRunning, "")

	// 保存
	if err := q1.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(storePath); os.IsNotExist(err) {
		t.Fatal("存储文件不存在")
	}

	// 加载到新队列
	q2 := NewTaskQueue(storePath)
	t.Cleanup(func() { _ = q2.Close() })
	if err := q2.Load(); err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	// 验证数据
	list := q2.List()
	if len(list) != 2 {
		t.Errorf("任务数量不匹配: expected 2, actual %d", len(list))
	}

	found := q2.Get(task1.ID)
	if found == nil {
		t.Fatal("未找到任务")
	}
	if found.Status != TaskFailed {
		t.Errorf("恢复后的状态不匹配: expected failed, actual %s", found.Status)
	}
	if !strings.Contains(found.Result, "interrupted") {
		t.Errorf("恢复后的结果应说明任务被中断: %q", found.Result)
	}
}

// TestTaskQueueLoadNotExist 测试加载不存在的文件
func TestTaskQueueLoadNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "nonexistent.json")

	q := NewTaskQueue(storePath)
	t.Cleanup(func() { _ = q.Close() })
	if err := q.Load(); err != nil {
		t.Fatalf("加载不存在的文件不应报错: %v", err)
	}
	if len(q.List()) != 0 {
		t.Error("空队列应有 0 个任务")
	}
}

// TestTaskQueueLoadEmptyFile 测试加载空文件
func TestTaskQueueLoadEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "empty.json")
	os.WriteFile(storePath, []byte(""), 0644)

	q := NewTaskQueue(storePath)
	t.Cleanup(func() { _ = q.Close() })
	if err := q.Load(); err != nil {
		t.Fatalf("加载空文件不应报错: %v", err)
	}
}

// ========== 工作器测试 ==========

// TestNewWorker 测试创建工作器
func TestNewWorker(t *testing.T) {
	w := NewWorker(1, "whale", 30*time.Minute)
	if w.ID() != 1 {
		t.Errorf("ID 不匹配: expected 1, actual %d", w.ID())
	}
	if w.IsBusy() {
		t.Error("新工作器不应为忙")
	}
}

// TestNewWorkerDefaults 测试工作器default value
func TestNewWorkerDefaults(t *testing.T) {
	w := NewWorker(0, "", 0)
	if w.ID() != 0 {
		t.Errorf("ID 不匹配: expected 0, actual %d", w.ID())
	}
}

// TestWorkerPoolCreation 测试工作器池创建
func TestWorkerPoolCreation(t *testing.T) {
	pool := NewWorkerPool(3, "whale", 10*time.Minute)
	if pool == nil {
		t.Fatal("工作器池创建失败")
	}
	if pool.maxWorkers != 3 {
		t.Errorf("maxWorkers 不匹配: expected 3, actual %d", pool.maxWorkers)
	}
}

// TestWorkerPoolDefaults 测试工作器池default value
func TestWorkerPoolDefaults(t *testing.T) {
	pool := NewWorkerPool(0, "", 0)
	if pool.maxWorkers != 2 {
		t.Errorf("默认 maxWorkers 应为 2, actual %d", pool.maxWorkers)
	}
	if pool.cliPath != "whale" {
		t.Errorf("默认 cliPath 应为 'whale', actual '%s'", pool.cliPath)
	}
}

// TestWorkerPoolRunningCount 测试工作器池运行计数
func TestWorkerPoolRunningCount(t *testing.T) {
	pool := NewWorkerPool(2, "whale", 30*time.Minute)
	if pool.RunningCount() != 0 {
		t.Error("初始运行计数应为 0")
	}
}

// ========== IPC 测试 ==========

// TestIPCJSONRPCRequest 测试 JSON-RPC 请求序列化
func TestIPCJSONRPCRequest(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "status",
		ID:      1,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}

	var decoded JSONRPCRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	if decoded.Method != "status" {
		t.Errorf("Method 不匹配: expected 'status', actual '%s'", decoded.Method)
	}
}

// TestIPCJSONRPCResponse 测试 JSON-RPC 响应序列化
func TestIPCJSONRPCResponse(t *testing.T) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  "ok",
		ID:      1,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}

	var decoded JSONRPCResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	if decoded.Result != "ok" {
		t.Errorf("Result 不匹配: expected 'ok', actual '%v'", decoded.Result)
	}
}

// TestIPCServerErrorResponse 测试error response
func TestIPCJSONRPCErrorResponse(t *testing.T) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Error:   &RPCError{Code: -32601, Message: "未知方法"},
		ID:      1,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}

	var decoded JSONRPCResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}
	if decoded.Error == nil {
		t.Fatal("Error 不should be nil")
	}
	if decoded.Error.Code != -32601 {
		t.Errorf("错误码不匹配: expected -32601, actual %d", decoded.Error.Code)
	}
}

// TestIPCClientSendTimeout 测试 IPC client发送超时
func TestIPCClientSendTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	client := NewIPCClient(tmpDir)

	// 没有server运行，应超时
	_, err := client.SendCommand("status", nil)
	if err == nil {
		t.Error("没有server时should return超时错误")
	}
}

// TestDefaultSocketPath 测试默认 Socket 路径
func TestDefaultSocketPath(t *testing.T) {
	path := DefaultSocketPath()
	if path == "" {
		t.Error("默认路径should not be empty")
	}
}

// ========== 日志测试 ==========

// TestDaemonLogger 测试日志记录器基本功能
func TestDaemonLogger(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "daemon.log")

	logger, err := NewDaemonLogger(logPath)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}

	logger.Info("测试信息: %s", "hello")
	logger.Error("test error: %d", 42)

	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志失败: %v", err)
	}

	// 验证日志文件内容
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}

	content := string(data)
	if len(content) == 0 {
		t.Error("日志文件should not be empty")
	}
}

// TestDaemonLoggerDirectoryCreation 测试日志目录自动创建
func TestDaemonLoggerDirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "subdir", "deep", "daemon.log")

	logger, err := NewDaemonLogger(logPath)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}
	defer logger.Close()

	logger.Info("测试")

	if _, err := os.Stat(filepath.Dir(logPath)); os.IsNotExist(err) {
		t.Error("日志目录未被创建")
	}
}

// ========== 守护进程配置测试 ==========

// TestDaemonConfigDefaults 测试守护进程配置default value
func TestDaemonConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
	})

	cfg := d.GetConfig()
	if cfg.MaxWorkers != 2 {
		t.Errorf("默认 MaxWorkers 应为 2, actual %d", cfg.MaxWorkers)
	}
	if cfg.TaskTimeout != 30*time.Minute {
		t.Errorf("默认 TaskTimeout 应为 30m, actual %v", cfg.TaskTimeout)
	}
	if cfg.CliPath != "whale" {
		t.Errorf("默认 CliPath 应为 'whale', actual '%s'", cfg.CliPath)
	}
}

// TestDaemonStatus 测试守护进程状态
func TestDaemonStatus(t *testing.T) {
	tmpDir := t.TempDir()
	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
	})

	status := d.Status()
	if status.Running {
		t.Error("未启动的守护进程状态应为 false")
	}
}

// ========== 并发security test ==========

// TestTaskQueueConcurrentAccess 测试任务队列并发访问
func TestTaskQueueConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	var wg sync.WaitGroup
	numGoroutines := 10
	tasksPerGoroutine := 100

	// 并发添加任务
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < tasksPerGoroutine; j++ {
				q.Add("concurrent task", "/dir", TaskDream, PriorityNormal)
			}
		}(i)
	}
	wg.Wait()

	list := q.List()
	expectedCount := numGoroutines * tasksPerGoroutine
	if len(list) != expectedCount {
		t.Errorf("任务数量不匹配: expected %d, actual %d", expectedCount, len(list))
	}
}

// TestTaskQueueConcurrentReadWrite 测试任务队列并发读写
func TestTaskQueueConcurrentReadWrite(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	// 先添加一些任务
	for i := 0; i < 100; i++ {
		q.Add("task", "/dir", TaskDream, PriorityNormal)
	}

	var wg sync.WaitGroup
	// 并发读取
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				q.List()
				q.PendingCount()
				q.RunningCount()
			}
		}()
	}
	// 并发写入
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				q.Add("new task", "/dir", TaskDream, PriorityNormal)
			}
		}()
	}
	wg.Wait()
}

// TestTaskQueueConcurrentSaveAndLoad 测试并发保存和加载
func TestTaskQueueConcurrentSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)
	// ★ ：SQLite 句柄先关再清理（Windows 不许删打开中的文件）。
	t.Cleanup(func() { _ = q.Close() })

	// 添加任务
	for i := 0; i < 50; i++ {
		q.Add("task", "/dir", TaskDream, PriorityNormal)
	}

	// 保存
	if err := q.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 并发加载
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q2 := NewTaskQueue(storePath)
			// ★ ：goroutine 内联关闭（不用 t.Cleanup：它不保证 goroutine 安全，
			// 且 wg.Wait() 先于测试结束，内联关闭时序更明确）。
			defer func() { _ = q2.Close() }()
			if err := q2.Load(); err != nil {
				t.Errorf("加载失败: %v", err)
			}
		}()
	}
	wg.Wait()
}

// ========== 任务 ID 格式测试 ==========

// TestTaskIDFormat 测试任务 ID 格式
func TestTaskIDFormat(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	task := q.Add("test", "/dir", TaskDream, PriorityNormal)

	// ID 应以 "task-" 开头
	if len(task.ID) < 5 || task.ID[:5] != "task-" {
		t.Errorf("任务 ID 格式不正确: %s", task.ID)
	}
}

// TestTaskIDUniqueness 测试任务 ID 唯一性
func TestTaskIDUniqueness(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		task := q.Add("test", "/dir", TaskDream, PriorityNormal)
		if ids[task.ID] {
			t.Errorf("重复的任务 ID: %s", task.ID)
		}
		ids[task.ID] = true
	}
}

// ========== 任务计数测试 ==========

// TestTaskQueueCounts 测试任务计数
func TestTaskQueueCounts(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "tasks.json")
	q := NewTaskQueue(storePath)

	t1 := q.Add("task1", "/dir", TaskDream, PriorityNormal)
	t2 := q.Add("task2", "/dir", TaskDream, PriorityNormal)
	q.Add("task3", "/dir", TaskDream, PriorityNormal)

	if q.PendingCount() != 3 {
		t.Errorf("PendingCount 不匹配: expected 3, actual %d", q.PendingCount())
	}

	q.UpdateStatus(t1.ID, TaskRunning, "")
	if q.PendingCount() != 2 {
		t.Errorf("PendingCount 不匹配: expected 2, actual %d", q.PendingCount())
	}
	if q.RunningCount() != 1 {
		t.Errorf("RunningCount 不匹配: expected 1, actual %d", q.RunningCount())
	}

	q.UpdateStatus(t2.ID, TaskCompleted, "done")
	if q.PendingCount() != 1 {
		t.Errorf("PendingCount 不匹配: expected 1, actual %d", q.PendingCount())
	}
}

// ========== IPC server测试 ==========

// TestIPCServerStartStop 测试 IPC server启停
func TestIPCServerStartStop(t *testing.T) {
	tmpDir := t.TempDir()
	ipcDir := filepath.Join(tmpDir, "ipc")

	d := &Daemon{
		cfg: DaemonConfig{
			SocketPath: ipcDir,
		},
		queue: NewTaskQueue(filepath.Join(tmpDir, "tasks.json")),
	}

	server := NewIPCServer(ipcDir, d)

	// 启动
	if err := server.Start(); err != nil {
		t.Fatalf("启动 IPC server失败: %v", err)
	}

	// 重复启动应报错
	if err := server.Start(); err == nil {
		t.Error("重复启动should return error")
	}

	// 停止
	if err := server.Stop(); err != nil {
		t.Fatalf("停止 IPC server失败: %v", err)
	}
}

// TestIPCClientServerCommunication 测试 IPC client-server通信
func TestIPCClientServerCommunication(t *testing.T) {
	tmpDir := t.TempDir()
	ipcDir := filepath.Join(tmpDir, "ipc")

	d := &Daemon{
		cfg: DaemonConfig{
			SocketPath:  ipcDir,
			MaxWorkers:  2,
			PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
			LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		},
		queue: NewTaskQueue(filepath.Join(tmpDir, "tasks.json")),
	}
	// ★ （Windows CI）：SQLite 句柄与日志句柄先关，t.TempDir 清理才删得掉
	// tasks.db / *.log（Windows 不许删除仍被打开的文件）。
	t.Cleanup(func() { _ = d.queue.Close() })

	server := NewIPCServer(ipcDir, d)
	logger, _ := NewDaemonLogger(filepath.Join(tmpDir, "ipc.log"))
	server.SetLogger(logger)
	t.Cleanup(func() { _ = logger.Close() })

	if err := server.Start(); err != nil {
		t.Fatalf("启动 IPC server失败: %v", err)
	}

	client := NewIPCClient(ipcDir)

	// 测试 status 命令
	result, err := client.SendCommand("status", nil)
	if err != nil {
		t.Fatalf("发送 status 命令失败: %v", err)
	}
	if result == nil {
		t.Error("status 命令should return结果")
	}

	// 测试 add_task 命令
	result, err = client.SendCommand("add_task", map[string]interface{}{
		"prompt":  "test task",
		"workdir": "/tmp",
	})
	if err != nil {
		t.Fatalf("发送 add_task 命令失败: %v", err)
	}
	if result == nil {
		t.Error("add_task 命令should return结果")
	}

	// 测试 list_tasks 命令
	result, err = client.SendCommand("list_tasks", nil)
	if err != nil {
		t.Fatalf("发送 list_tasks 命令失败: %v", err)
	}

	// 测试未知命令
	_, err = client.SendCommand("unknown_method", nil)
	if err == nil {
		t.Error("未知命令should return error")
	}

	// 测试缺少参数的 add_task
	_, err = client.SendCommand("add_task", map[string]interface{}{})
	if err == nil {
		t.Error("缺少 prompt 参数should return error")
	}

	// 先停止server再关闭日志
	server.Stop()
	logger.Close()
}

// ========== 守护进程启停测试 ==========

// TestDaemonStartAndStop 测试守护进程完整启停流程
func TestDaemonStartAndStop(t *testing.T) {
	tmpDir := t.TempDir()

	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		MaxWorkers:  2,
		CliPath:     "whale",
	})

	// 启动
	if err := d.Start(); err != nil {
		t.Fatalf("启动守护进程失败: %v", err)
	}

	// 验证 PID 文件存在
	if _, err := os.Stat(filepath.Join(tmpDir, "daemon.pid")); os.IsNotExist(err) {
		t.Error("PID 文件未创建")
	}

	// 验证状态
	status := d.Status()
	if !status.Running {
		t.Error("守护进程状态应为 running")
	}

	// 停止
	if err := d.Stop(); err != nil {
		t.Fatalf("停止守护进程失败: %v", err)
	}

	// 验证 PID 文件已删除
	if _, err := os.Stat(filepath.Join(tmpDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Error("PID 文件未被删除")
	}
}

// TestDaemonDoubleStart 测试重复启动守护进程
func TestDaemonDoubleStart(t *testing.T) {
	tmpDir := t.TempDir()

	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		MaxWorkers:  2,
	})

	if err := d.Start(); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	defer d.Stop()

	// 重复启动应报错
	if err := d.Start(); err == nil {
		t.Error("重复启动should return error")
	}
}

// TestDaemonStopNotRunning 测试停止未运行的守护进程
func TestDaemonStopNotRunning(t *testing.T) {
	tmpDir := t.TempDir()

	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
	})

	if err := d.Stop(); err != nil {
		t.Errorf("停止未运行的守护进程应幂等，got: %v", err)
	}
}

// ========== Daemon startup with channel-based wait tests ==========

// TestDaemonStartupNormal tests that daemon starts within 2 seconds and ready marker is written
func TestDaemonStartupNormal(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")

	d := NewDaemon(DaemonConfig{
		PIDFilePath: pidPath,
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		MaxWorkers:  1,
	})

	// Start daemon in background goroutine
	startErr := make(chan error, 1)
	go func() {
		startErr <- d.Start()
	}()

	// ★ 2026-10-27 ：原来是「起一个 goroutine 每 100ms 轮询 IsReady，再用 select 等它」——
	// 两层等待、预算 5s/2s 拍出来，且失败时看不到 Start 返回了什么。
	// 现在只等**一个事件**（ready 标记就绪），预算由 testutil 按 t.Deadline 推导。
	testutil.EventuallyObserving(t, "daemon 写出 ready 标记（pid 文件可判定为就绪）", func() bool {
		return IsReady(pidPath)
	}, func() string {
		select {
		case err := <-startErr:
			return testutil.FormatObserved("daemon_start_returned", true, "start_err", err)
		default:
			return testutil.FormatObserved("daemon_start_returned", false)
		}
	})

	// Verify daemon is running
	if !d.Status().Running {
		t.Error("daemon should be running after successful start")
	}

	// Verify ready marker file exists
	readyPath := filepath.Join(tmpDir, "daemon.ready")
	if _, err := os.Stat(readyPath); os.IsNotExist(err) {
		t.Error("ready marker file should exist after daemon starts")
	}

	// Verify IsRunning returns true
	if !IsRunning(pidPath) {
		t.Error("IsRunning should return true after daemon starts")
	}

	// Cleanup
	d.Stop()
}

// TestDaemonStartupTimeout tests that startup returns timeout when daemon takes too long
func TestDaemonStartupTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")

	// No daemon started, so IsReady will never return true
	// Simulate the timeout scenario
	//
	// ★ 2026-10-27 ：原实现是「轮询 5 次 IsReady（100ms 一次），再 select 等 2s 证明什么都没发生」。
	// 那 2.5 秒测的是"这两秒内没有别的东西写这个文件"，与"启动超时"这条语义无关 ——
	// 机器再慢它也不会红，机器再快它也不证明什么（SLEEP_NEG 型：负向断言靠等时间成立）。
	//
	// 真正钉住的不变量是：**没有 daemon 在跑 ⇒ ready 判定必须为 false**。
	// `IsReady → IsRunning → isProcessAlive(pid)`，pid 文件不存在就是 false，不需要等。
	if IsReady(pidPath) {
		t.Fatal("没有 daemon 运行时 IsReady 必须为 false（pid 文件不存在）")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("未启动任何 daemon，pid 文件不应存在：%v", err)
	}
}

// TestDaemonStartupFailure tests that startup returns error when daemon process exits
func TestDaemonStartupFailure(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")

	// Write a PID file pointing to a non-existent process
	// This simulates a stale daemon that has exited
	os.WriteFile(pidPath, []byte("99999999"), 0600)

	d := NewDaemon(DaemonConfig{
		PIDFilePath: pidPath,
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		MaxWorkers:  1,
	})

	// CleanupStale should succeed (the PID doesn't exist)
	if err := d.CleanupStale(); err != nil {
		t.Fatalf("CleanupStale should succeed for stale PID: %v", err)
	}

	// Now start should work since stale PID was cleaned up
	if err := d.Start(); err != nil {
		t.Fatalf("Start should succeed after stale cleanup: %v", err)
	}
	d.Stop()
}

// TestDaemonStartupAlreadyRunning tests that starting an already-running daemon returns error
func TestDaemonStartupAlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()

	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		MaxWorkers:  1,
	})

	// Start daemon first time
	if err := d.Start(); err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	defer d.Stop()

	// Try to start again - should fail
	if err := d.Start(); err == nil {
		t.Error("starting an already-running daemon should return error")
	}
}

// TestDaemonReadyMarkerFile tests that the ready marker file is written on start and removed on stop
func TestDaemonReadyMarkerFile(t *testing.T) {
	tmpDir := t.TempDir()
	readyPath := filepath.Join(tmpDir, "daemon.ready")

	d := NewDaemon(DaemonConfig{
		PIDFilePath: filepath.Join(tmpDir, "daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "daemon.log"),
		SocketPath:  filepath.Join(tmpDir, "ipc"),
		MaxWorkers:  1,
	})

	// Before start, ready marker should not exist
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		t.Error("ready marker should not exist before daemon starts")
	}

	// Start daemon
	if err := d.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	// After start, ready marker should exist
	if _, err := os.Stat(readyPath); os.IsNotExist(err) {
		t.Error("ready marker should exist after daemon starts")
	}

	// Ready marker should contain the PID
	data, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatalf("failed to read ready marker: %v", err)
	}
	pidStr := string(data)
	if pidStr != strconv.Itoa(os.Getpid()) {
		t.Errorf("ready marker contains %q, want %d", pidStr, os.Getpid())
	}

	// Stop daemon
	if err := d.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// After stop, ready marker should be removed
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		t.Error("ready marker should be removed after daemon stops")
	}
}

// TestIsRunningUsesReadyMarker tests that the package-level IsRunning checks the ready marker file
func TestIsRunningUsesReadyMarker(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "daemon.pid")
	readyPath := filepath.Join(tmpDir, "daemon.ready")

	// No PID file and no ready marker → not running
	if IsRunning(pidPath) {
		t.Error("IsRunning should return false when no files exist")
	}

	// Create PID file but no ready marker → not running (IsRunning checks ready marker)
	os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600)
	if IsRunning(pidPath) {
		t.Error("IsRunning should return false when only PID file exists (no ready marker)")
	}

	// Create ready marker with current PID → running
	os.WriteFile(readyPath, []byte(strconv.Itoa(os.Getpid())), 0600)
	if !IsRunning(pidPath) {
		t.Error("IsRunning should return true when ready marker exists with valid PID")
	}

	// Clean up
	os.Remove(pidPath)
	os.Remove(readyPath)
}
