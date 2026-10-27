package daemon

import (
	"testing"
	"time"
)

// --- Worker Registry unit test ---

// TestWorkerRegistry_Register 测试注册工作器
func TestWorkerRegistry_Register(t *testing.T) {
	reg := NewWorkerRegistry()
	w := reg.Register("w1", WorkerTypeCode, WorkerCapabilities{
		Type:        WorkerTypeCode,
		CanExecute:  true,
		CanGenerate: true,
		MaxTokens:   8192,
	})
	if w.ID != "w1" {
		t.Errorf("Expected ID w1, got %s", w.ID)
	}
	if w.Type != WorkerTypeCode {
		t.Errorf("Expected type code, got %s", w.Type)
	}
	if w.Status != "idle" {
		t.Errorf("Expected status idle, got %s", w.Status)
	}
}

// TestWorkerRegistry_Unregister 测试注销工作器
func TestWorkerRegistry_Unregister(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	err := reg.Unregister("w1")
	if err != nil {
		t.Fatalf("Unregister failed: %v", err)
	}
	_, ok := reg.Get("w1")
	if ok {
		t.Error("Worker should be unregistered")
	}
}

// TestWorkerRegistry_UnregisterNonexistent 测试注销不存在的工作器
func TestWorkerRegistry_UnregisterNonexistent(t *testing.T) {
	reg := NewWorkerRegistry()
	err := reg.Unregister("nonexistent")
	if err == nil {
		t.Error("Should fail to unregister nonexistent worker")
	}
}

// TestWorkerRegistry_List 测试列出工作器
func TestWorkerRegistry_List(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	reg.Register("w2", WorkerTypeSearch, WorkerCapabilities{})
	reg.Register("w3", WorkerTypeAnalysis, WorkerCapabilities{})
	list := reg.List()
	if len(list) != 3 {
		t.Errorf("Expected 3 workers, got %d", len(list))
	}
}

// TestWorkerRegistry_ListByType 测试按类型列出工作器
func TestWorkerRegistry_ListByType(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	reg.Register("w2", WorkerTypeSearch, WorkerCapabilities{})
	reg.Register("w3", WorkerTypeCode, WorkerCapabilities{})
	codeWorkers := reg.ListByType(WorkerTypeCode)
	if len(codeWorkers) != 2 {
		t.Errorf("Expected 2 code workers, got %d", len(codeWorkers))
	}
}

// TestWorkerRegistry_Heartbeat 测试心跳更新
func TestWorkerRegistry_Heartbeat(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})

	// ★ 2026-10-27 ：原来是 `time.Sleep(10ms)` 后调 Heartbeat，再断言
	// `time.Since(LastHeartbeat) < 100ms` —— 这条断言**不具判别力**：
	// 即使 Heartbeat 一行都没执行，Register 刚写的时间戳也只差 10ms，照样绿。
	// 改法：把时间戳**置成一个很久以前的值**，再 Heartbeat；只有真更新过才会前进。
	// 全程零睡眠，判定只依赖"值变没变"。
	stale := time.Now().Add(-time.Hour)
	reg.mu.Lock()
	if worker, ok := reg.workers["w1"]; ok {
		worker.LastHeartbeat = stale
	}
	reg.mu.Unlock()

	if err := reg.Heartbeat("w1"); err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	w, _ := reg.Get("w1")
	if w == nil {
		t.Fatal("worker w1 应在注册表里")
	}
	if !w.LastHeartbeat.After(stale) {
		t.Fatalf("Heartbeat 没有更新 LastHeartbeat（仍是 %v）", w.LastHeartbeat)
	}
	if time.Since(w.LastHeartbeat) > time.Minute {
		t.Fatalf("Heartbeat 写入的时间戳不合理地旧：%v", w.LastHeartbeat)
	}
}

// TestWorkerRegistry_DetectStale 测试过期检测
func TestWorkerRegistry_DetectStale(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.heartbeatTimeout = 50 * time.Millisecond
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	// 手动设置过期心跳
	reg.mu.Lock()
	reg.workers["w1"].LastHeartbeat = time.Now().Add(-100 * time.Millisecond)
	reg.mu.Unlock()

	stale := reg.DetectStale()
	if len(stale) != 1 {
		t.Errorf("Expected 1 stale worker, got %d", len(stale))
	}
	if stale[0].Status != "stale" {
		t.Errorf("Expected status stale, got %s", stale[0].Status)
	}
}

// TestWorkerRegistry_FindIdleByType 测试按类型查找空闲工作器
func TestWorkerRegistry_FindIdleByType(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	reg.Register("w2", WorkerTypeSearch, WorkerCapabilities{})
	reg.SetStatus("w1", "active")

	w := reg.FindIdleByType(WorkerTypeCode)
	if w != nil {
		t.Error("w1 is active, should not be returned as idle")
	}
	w = reg.FindIdleByType(WorkerTypeSearch)
	if w == nil || w.ID != "w2" {
		t.Error("w2 should be idle")
	}
}

// TestWorkerRegistry_CountByType 测试按类型统计
func TestWorkerRegistry_CountByType(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	reg.Register("w2", WorkerTypeCode, WorkerCapabilities{})
	reg.Register("w3", WorkerTypeSearch, WorkerCapabilities{})
	if reg.CountByType(WorkerTypeCode) != 2 {
		t.Errorf("Expected 2 code workers")
	}
	if reg.CountByType(WorkerTypeSearch) != 1 {
		t.Errorf("Expected 1 search worker")
	}
}

// TestWorkerRegistry_ActiveCount 测试活跃工作器计数
func TestWorkerRegistry_ActiveCount(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	reg.Register("w2", WorkerTypeCode, WorkerCapabilities{})
	if reg.ActiveCount() != 2 {
		t.Errorf("Expected 2 active workers, got %d", reg.ActiveCount())
	}
}

// --- 优先级队列unit test ---

// TestTaskPriority_Constants 测试优先级常量
func TestTaskPriority_Constants(t *testing.T) {
	if PriorityLow >= PriorityNormal {
		t.Error("PriorityLow should be less than PriorityNormal")
	}
	if PriorityNormal >= PriorityHigh {
		t.Error("PriorityNormal should be less than PriorityHigh")
	}
	if PriorityHigh >= PriorityCritical {
		t.Error("PriorityHigh should be less than PriorityCritical")
	}
}

// TestTaskQueue_PriorityOrder 测试优先级排序
func TestTaskQueue_PriorityOrder(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	q.Add("low task", "/tmp", TaskDream, PriorityLow)
	q.Add("critical task", "/tmp", TaskDream, PriorityCritical)
	q.Add("normal task", "/tmp", TaskDream, PriorityNormal)

	next := q.Next()
	if next == nil {
		t.Fatal("Next should return a task")
	}
	if next.Priority != PriorityCritical {
		t.Errorf("Expected critical priority, got %d", next.Priority)
	}
	if next.Prompt != "critical task" {
		t.Errorf("Expected 'critical task', got %q", next.Prompt)
	}
}

// TestTaskQueue_SamePriorityFIFO 测试同优先级 FIFO
func TestTaskQueue_SamePriorityFIFO(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	q.Add("first", "/tmp", TaskDream, PriorityNormal)
	q.Add("second", "/tmp", TaskDream, PriorityNormal)

	next := q.Next()
	if next.Prompt != "first" {
		t.Errorf("Expected 'first', got %q", next.Prompt)
	}
}

// TestTaskType_Constants 测试任务类型常量
func TestTaskType_Constants(t *testing.T) {
	if TaskDream != "dream" {
		t.Errorf("TaskDream = %q, want %q", TaskDream, "dream")
	}
	if TaskShell != "shell" {
		t.Errorf("TaskShell = %q, want %q", TaskShell, "shell")
	}
	if TaskAnalysis != "analysis" {
		t.Errorf("TaskAnalysis = %q, want %q", TaskAnalysis, "analysis")
	}
}

// TestTaskQueue_TaskType 测试任务类型字段
func TestTaskQueue_TaskType(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	task := q.Add("shell command", "/tmp", TaskShell, PriorityNormal)
	if task.Type != TaskShell {
		t.Errorf("Expected type shell, got %s", task.Type)
	}
}

// TestTaskQueue_UpdateProgress 测试进度更新
func TestTaskQueue_UpdateProgress(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	task := q.Add("test", "/tmp", TaskDream, PriorityNormal)

	err := q.UpdateProgress(task.ID, 50, "halfway done")
	if err != nil {
		t.Fatalf("UpdateProgress failed: %v", err)
	}

	updated := q.Get(task.ID)
	if updated.Progress != 50 {
		t.Errorf("Expected progress 50, got %d", updated.Progress)
	}
	if updated.PartialOutput != "halfway done" {
		t.Errorf("Expected 'halfway done', got %q", updated.PartialOutput)
	}
}

// TestTaskQueue_UpdateProgressNonexistent 测试更新不存在任务的进度
func TestTaskQueue_UpdateProgressNonexistent(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	err := q.UpdateProgress("nonexistent", 50, "")
	if err == nil {
		t.Error("Should fail to update progress of nonexistent task")
	}
}

// TestTaskQueue_AddWithNotify 测试带通知的任务添加
func TestTaskQueue_AddWithNotify(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	task := q.AddWithNotify("test", "/tmp", TaskDream, PriorityNormal)
	if task == nil {
		t.Fatal("AddWithNotify should return a task")
	}
	// 验证通知 channel 收到信号
	select {
	case <-NotifyChannel():
		// 通知已收到
	default:
		// 通知可能在缓冲区中已被消费，这是可接受的
	}
}

// --- EventBus unit test ---

// TestEventBus_SubscribeAndPublish 测试订阅和发布
func TestEventBus_SubscribeAndPublish(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(EventTaskCompleted)
	bus.Publish(Event{
		Type:    EventTaskCompleted,
		Payload: "task-123",
	})

	select {
	case evt := <-ch:
		if evt.Type != EventTaskCompleted {
			t.Errorf("Expected EventTaskCompleted, got %s", evt.Type)
		}
		if evt.Payload != "task-123" {
			t.Errorf("Expected 'task-123', got %v", evt.Payload)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Should receive event within 100ms")
	}
}

// TestEventBus_MultipleSubscribers 测试多个订阅者
func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch1 := bus.Subscribe(EventTaskFailed)
	ch2 := bus.Subscribe(EventTaskFailed)

	bus.Publish(Event{
		Type:    EventTaskFailed,
		Payload: "error",
	})

	// 两个订阅者都应收到
	select {
	case <-ch1:
	default:
		t.Error("Subscriber 1 should receive event")
	}
	select {
	case <-ch2:
	default:
		t.Error("Subscriber 2 should receive event")
	}
}

// TestEventBus_NoSubscribers 测试无订阅者时发布
func TestEventBus_NoSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()
	// Should not panic
	bus.Publish(Event{Type: EventTaskStarted, Payload: "test"})
}

// TestEventBus_WrongEventType 测试订阅者只收到对应类型
func TestEventBus_WrongEventType(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(EventTaskCompleted)
	bus.Publish(Event{Type: EventTaskStarted, Payload: "test"})

	select {
	case <-ch:
		t.Error("Should not receive event of different type")
	case <-time.After(50 * time.Millisecond):
		// 正确：不应收到不同类型的事件
	}
}

// --- Worker 类型测试 ---

// TestWorkerType_Constants 测试工作器类型常量
func TestWorkerType_Constants(t *testing.T) {
	if WorkerTypeCode != "code" {
		t.Errorf("WorkerTypeCode = %q, want %q", WorkerTypeCode, "code")
	}
	if WorkerTypeSearch != "search" {
		t.Errorf("WorkerTypeSearch = %q, want %q", WorkerTypeSearch, "search")
	}
	if WorkerTypeAnalysis != "analysis" {
		t.Errorf("WorkerTypeAnalysis = %q, want %q", WorkerTypeAnalysis, "analysis")
	}
	if WorkerTypeShell != "shell" {
		t.Errorf("WorkerTypeShell = %q, want %q", WorkerTypeShell, "shell")
	}
}

// TestNewTypedWorker 测试创建指定类型工作器
func TestNewTypedWorker(t *testing.T) {
	w := NewTypedWorker(1, "whale", 30*time.Minute, WorkerTypeSearch)
	if w.Type() != WorkerTypeSearch {
		t.Errorf("Expected type search, got %s", w.Type())
	}
}

// TestWorker_DefaultType 测试默认工作器类型
func TestWorker_DefaultType(t *testing.T) {
	w := NewWorker(1, "whale", 30*time.Minute)
	if w.Type() != WorkerTypeCode {
		t.Errorf("Default type should be code, got %s", w.Type())
	}
}

// --- Daemon integration test ---

// TestDaemon_RegistryInitialized 测试守护进程初始化注册表
func TestDaemon_RegistryInitialized(t *testing.T) {
	dir := t.TempDir()
	cfg := DaemonConfig{
		WorkDir:     dir,
		MaxWorkers:  3,
		TaskTimeout: 5 * time.Minute,
		PIDFilePath: dir + "/daemon.pid",
		LogFilePath: dir + "/daemon.log",
		SocketPath:  dir,
	}
	d := NewDaemon(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer d.Stop()

	reg := d.Registry()
	if reg == nil {
		t.Fatal("Registry should be initialized")
	}
	workers := reg.List()
	if len(workers) != 3 {
		t.Errorf("Expected 3 default workers, got %d", len(workers))
	}
}

// TestDaemon_EventsInitialized 测试守护进程初始化事件总线
func TestDaemon_EventsInitialized(t *testing.T) {
	dir := t.TempDir()
	cfg := DaemonConfig{
		WorkDir:     dir,
		MaxWorkers:  2,
		TaskTimeout: 5 * time.Minute,
		PIDFilePath: dir + "/daemon.pid",
		LogFilePath: dir + "/daemon.log",
		SocketPath:  dir,
	}
	d := NewDaemon(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer d.Stop()

	events := d.Events()
	if events == nil {
		t.Fatal("Events should be initialized")
	}
}

// TestDaemon_Restart 测试重启守护进程
func TestDaemon_Restart(t *testing.T) {
	dir := t.TempDir()
	cfg := DaemonConfig{
		WorkDir:     dir,
		MaxWorkers:  2,
		TaskTimeout: 5 * time.Minute,
		PIDFilePath: dir + "/daemon.pid",
		LogFilePath: dir + "/daemon.log",
		SocketPath:  dir,
	}
	d := NewDaemon(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := d.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	status := d.Status()
	if !status.Running {
		t.Error("Daemon should be running after restart")
	}

	d.Stop()
}

// TestDaemon_RestartNotRunning 测试未运行时重启
func TestDaemon_RestartNotRunning(t *testing.T) {
	dir := t.TempDir()
	cfg := DaemonConfig{
		WorkDir:     dir,
		MaxWorkers:  2,
		TaskTimeout: 5 * time.Minute,
		PIDFilePath: dir + "/daemon.pid",
		LogFilePath: dir + "/daemon.log",
		SocketPath:  dir,
	}
	d := NewDaemon(cfg)
	err := d.Restart()
	if err == nil {
		t.Error("Should fail to restart when not running")
	}
}

// TestDaemon_SubmitTaskWithPriority 测试提交带优先级的任务
func TestDaemon_SubmitTaskWithPriority(t *testing.T) {
	dir := t.TempDir()
	cfg := DaemonConfig{
		WorkDir:     dir,
		MaxWorkers:  2,
		TaskTimeout: 5 * time.Minute,
		PIDFilePath: dir + "/daemon.pid",
		LogFilePath: dir + "/daemon.log",
		SocketPath:  dir,
	}
	d := NewDaemon(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer d.Stop()

	queue := d.Queue()
	task := queue.AddWithNotify("high priority task", dir, TaskDream, PriorityHigh)
	if task.Priority != PriorityHigh {
		t.Errorf("Expected high priority, got %d", task.Priority)
	}
	if task.Type != TaskDream {
		t.Errorf("Expected dream type, got %s", task.Type)
	}
}

// TestDaemon_EventNotification 测试任务完成事件通知
func TestDaemon_EventNotification(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(EventTaskCompleted)
	bus.Publish(Event{
		Type:    EventTaskCompleted,
		Payload: map[string]string{"task_id": "task-1", "status": "completed"},
	})

	select {
	case evt := <-ch:
		payload, ok := evt.Payload.(map[string]string)
		if !ok {
			t.Fatal("Payload should be map[string]string")
		}
		if payload["task_id"] != "task-1" {
			t.Errorf("Expected task_id task-1, got %s", payload["task_id"])
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Should receive event notification")
	}
}

// TestWorkerRegistry_SetCurrentTask test setup工作器当前任务
func TestWorkerRegistry_SetCurrentTask(t *testing.T) {
	reg := NewWorkerRegistry()
	reg.Register("w1", WorkerTypeCode, WorkerCapabilities{})
	err := reg.SetCurrentTask("w1", "task-123")
	if err != nil {
		t.Fatalf("SetCurrentTask failed: %v", err)
	}
	w, _ := reg.Get("w1")
	if w.CurrentTask != "task-123" {
		t.Errorf("Expected current_task task-123, got %s", w.CurrentTask)
	}
}

// TestTaskQueue_PriorityWithMultipleLevels 测试多优先级混合
func TestTaskQueue_PriorityWithMultipleLevels(t *testing.T) {
	q := NewTaskQueue(t.TempDir() + "/tasks.json")
	q.Add("low", "/tmp", TaskShell, PriorityLow)
	q.Add("critical", "/tmp", TaskDream, PriorityCritical)
	q.Add("high", "/tmp", TaskAnalysis, PriorityHigh)
	q.Add("normal", "/tmp", TaskDream, PriorityNormal)

	// 应按 critical → high → normal → low 顺序返回
	expected := []TaskPriority{PriorityCritical, PriorityHigh, PriorityNormal, PriorityLow}
	for _, expPriority := range expected {
		next := q.Next()
		if next == nil {
			t.Fatalf("Expected task with priority %d, got nil", expPriority)
		}
		if next.Priority != expPriority {
			t.Errorf("Expected priority %d, got %d", expPriority, next.Priority)
		}
		q.UpdateStatus(next.ID, TaskRunning, "")
	}
}
