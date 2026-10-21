package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/daemon"
)

// newTestDaemon 在临时目录下创建测试用守护进程，不auto start
func newTestDaemon(t *testing.T) *daemon.Daemon {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := daemon.DaemonConfig{
		WorkDir:     tmpDir,
		MaxWorkers:  1,
		PIDFilePath: filepath.Join(tmpDir, "Daemon.pid"),
		LogFilePath: filepath.Join(tmpDir, "Daemon.log"),
		SocketPath:  tmpDir,
		MetricsAddr: "127.0.0.1:0",
	}
	d := daemon.NewDaemon(cfg)
	t.Cleanup(func() { _ = d.Stop() })
	return d
}

// newTestDaemonStarted 创建并启动测试守护进程
func newTestDaemonStarted(t *testing.T) *daemon.Daemon {
	t.Helper()
	d := newTestDaemon(t)
	if err := d.Start(); err != nil {
		t.Fatalf("启动测试守护进程失败: %v", err)
	}
	return d
}

// localDaemonController keeps command tests in-process. Production uses
// daemon.Controller, whose responsibility is covered by daemon package tests.
type localDaemonController struct{ daemon *daemon.Daemon }

func (c *localDaemonController) IsRunning() bool { return c.daemon.Status().Running }
func (c *localDaemonController) Start(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return c.daemon.Start()
	}
}
func (c *localDaemonController) Stop(context.Context) error { return c.daemon.Stop() }
func (c *localDaemonController) Restart(ctx context.Context) error {
	if err := c.Stop(ctx); err != nil {
		return err
	}
	return c.Start(ctx)
}
func (c *localDaemonController) Status() (daemon.DaemonStatus, error) {
	return c.daemon.Status(), nil
}
func (c *localDaemonController) AddTask(prompt, workDir string, taskType daemon.TaskType, priority daemon.TaskPriority) (*daemon.Task, error) {
	if !c.IsRunning() {
		return nil, fmt.Errorf("daemon is not running")
	}
	task := c.daemon.Queue().AddWithNotify(prompt, workDir, taskType, priority)
	return task, c.daemon.Queue().Save()
}
func (c *localDaemonController) ListTasks() ([]daemon.Task, error) {
	if !c.IsRunning() {
		return nil, fmt.Errorf("daemon is not running")
	}
	return c.daemon.Queue().List(), nil
}
func (c *localDaemonController) CancelTask(id string) error  { return c.daemon.CancelTask(id) }
func (c *localDaemonController) Config() daemon.DaemonConfig { return c.daemon.GetConfig() }

// newTestModel creates a minimal Model with daemon for testing
func newTestModel(t *testing.T) *Model {
	t.Helper()
	d := newTestDaemon(t)
	tm := ToolModel{daemon: d, daemonControl: &localDaemonController{daemon: d}}
	return &Model{
		ToolModel:   tm,
		toolService: &tm,
	}
}

// newTestModelStarted creates a Model with a started daemon
func newTestModelStarted(t *testing.T) *Model {
	t.Helper()
	d := newTestDaemonStarted(t)
	tm := ToolModel{daemon: d, daemonControl: &localDaemonController{daemon: d}}
	return &Model{
		ToolModel:    tm,
		toolService:  &tm,
		SessionModel: SessionModel{workDir: t.TempDir()},
	}
}

// ========== handleDaemon unit test ==========

func TestHandleDaemonStart(t *testing.T) {
	m := newTestModel(t)

	result := m.handleDaemon("start")
	if !strings.Contains(result, "守护进程已启动") {
		t.Errorf("handleDaemon('start') = %q, expected包含 '守护进程已启动'", result)
	}

	status := m.daemon.Status()
	if !status.Running {
		t.Error("守护进程未在运行状态")
	}
}

func TestHandleDaemonStartAlreadyRunning(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("start")
	if !strings.Contains(result, "失败") && !strings.Contains(result, "已在运行") {
		t.Errorf("handleDaemon('start') 在已运行时 = %q, expected包含错误提示", result)
	}
}

func TestHandleDaemonStop(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("stop")
	if !strings.Contains(result, "守护进程已停止") {
		t.Errorf("handleDaemon('stop') = %q, expected包含 '守护进程已停止'", result)
	}

	status := m.daemon.Status()
	if status.Running {
		t.Error("守护进程应该已停止")
	}

	cfg := m.daemon.GetConfig()
	if _, err := os.Stat(cfg.PIDFilePath); !os.IsNotExist(err) {
		t.Error("PID 文件应该被清理")
	}
}

func TestHandleDaemonStopNotRunning(t *testing.T) {
	m := newTestModel(t)

	result := m.handleDaemon("stop")
	if !strings.Contains(result, "失败") && !strings.Contains(result, "未在运行") {
		t.Errorf("handleDaemon('stop') 未运行时 = %q, expected包含错误提示", result)
	}
}

func TestHandleDaemonStatusRunning(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("status")

	checks := []string{"守护进程状态", "运行中", "PID", "工作器", "运行时间"}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("status 输出缺少 '%s': %s", check, result)
		}
	}
}

func TestHandleDaemonStatusNotRunning(t *testing.T) {
	m := newTestModel(t)

	result := m.handleDaemon("status")
	if !strings.Contains(result, "未运行") {
		t.Errorf("handleDaemon('status') 未运行时 = %q, expected包含 '未运行'", result)
	}
}

func TestHandleDaemonAdd(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("add 测试任务提示词")
	if !strings.Contains(result, "任务已添加") {
		t.Errorf("handleDaemon('add ...') = %q, expected包含 '任务已添加'", result)
	}

	if tasks := m.daemon.Queue().List(); len(tasks) != 1 || tasks[0].Prompt != "测试任务提示词" {
		t.Errorf("任务未正确进入 daemon 队列: %#v", tasks)
	}
}

func TestHandleDaemonAddNotRunning(t *testing.T) {
	m := newTestModel(t)

	result := m.handleDaemon("add 测试任务")
	if !strings.Contains(result, "失败") && !strings.Contains(result, "未在运行") && !strings.Contains(result, "未初始化") {
		t.Errorf("handleDaemon('add ...') 未运行时 = %q, expected包含错误提示", result)
	}
}

func TestHandleDaemonAddEmptyPrompt(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("add ")
	if !strings.Contains(result, "请输入") && !strings.Contains(result, "任务描述") {
		t.Errorf("handleDaemon('add ') = %q, expected提示输入任务描述", result)
	}
}

func TestHandleDaemonList(t *testing.T) {
	m := newTestModelStarted(t)

	m.daemon.Queue().Add("任务A", m.workDir, daemon.TaskDream, daemon.PriorityNormal)
	m.daemon.Queue().Add("任务B", m.workDir, daemon.TaskAnalysis, daemon.PriorityHigh)

	result := m.handleDaemon("list")

	checks := []string{"后台任务列表", "(共 2 个)", "任务A", "任务B"}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("list 输出缺少 '%s': %s", check, result)
		}
	}
}

func TestHandleDaemonListEmpty(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("list")
	if !strings.Contains(result, "暂无") && !strings.Contains(result, "(共 0") {
		t.Errorf("handleDaemon('list') 空队列时 = %q, expected无任务提示", result)
	}
}

func TestHandleDaemonCancel(t *testing.T) {
	m := newTestModelStarted(t)

	task := m.daemon.Queue().Add("待取消任务", m.workDir, daemon.TaskDream, daemon.PriorityNormal)
	result := m.handleDaemon("cancel " + task.ID)

	if !strings.Contains(result, "已取消") {
		t.Errorf("handleDaemon('cancel ...') = %q, expected包含 '已取消'", result)
	}

	cancelledTask := m.daemon.Queue().Get(task.ID)
	if cancelledTask.Status != daemon.TaskCancelled {
		t.Errorf("任务状态 = %s, expected %s", cancelledTask.Status, daemon.TaskCancelled)
	}
}

func TestHandleDaemonCancelInvalidID(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("cancel nonexistent-id")
	if !strings.Contains(result, "失败") && !strings.Contains(result, "不存在") {
		t.Errorf("handleDaemon('cancel invalid') = %q, expected包含错误提示", result)
	}
}

func TestHandleDaemonCancelEmptyID(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("cancel ")
	if !strings.Contains(result, "用法") {
		t.Errorf("handleDaemon('cancel ') = %q, expected显示用法", result)
	}
}

func TestHandleDaemonLog(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("log")

	if strings.Contains(result, "日志文件不存在") {
		t.Log("日志文件尚未生成（守护进程刚启动）")
	} else if strings.Contains(result, "守护进程日志") || strings.Contains(result, "(空)") {
		t.Log("日志返回正常")
	} else {
		t.Errorf("handleDaemon('log') = %q, 非预期输出", result)
	}
}

func TestHandleDaemonLogFileNotExist(t *testing.T) {
	m := newTestModel(t)
	m.daemon = newTestDaemon(t)

	result := m.handleDaemon("log")
	if !strings.Contains(result, "日志文件不存在") && !strings.Contains(result, "日志文件") {
		t.Errorf("handleDaemon('log') 无日志文件时 = %q, expected '日志文件不存在'", result)
	}
}

func TestHandleDaemonUnknownSubcommand(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("invalid_command")
	if !strings.Contains(result, "未知") {
		t.Errorf("handleDaemon('invalid') = %q, expected包含 '未知'", result)
	}
}

func TestHandleDaemonEmpty(t *testing.T) {
	m := newTestModelStarted(t)

	result := m.handleDaemon("")
	if !strings.Contains(result, "用法") && !strings.Contains(result, "start") {
		t.Errorf("handleDaemon('') = %q, expected显示帮助", result)
	}
}

// ========== formatDaemonStatus unit test ==========

func TestFormatDaemonStatusRunning(t *testing.T) {
	m := newTestModelStarted(t)
	time.Sleep(10 * time.Millisecond)

	status := m.daemon.Status()
	result := m.formatDaemonStatus(status)

	checks := []string{
		"守护进程状态",
		fmt.Sprintf("PID: %d", status.PID),
		"运行中",
		"工作器",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("formatDaemonStatus 运行中缺少 '%s': %s", check, result)
		}
	}
}

func TestFormatDaemonStatusNotRunning(t *testing.T) {
	m := newTestModel(t)
	status := m.daemon.Status()
	result := m.formatDaemonStatus(status)

	if !strings.Contains(result, "未运行") {
		t.Errorf("formatDaemonStatus 未运行时 = %q, expected包含 '未运行'", result)
	}
}

// ========== formatDaemonTaskList unit test ==========

func TestFormatDaemonTaskListEmpty(t *testing.T) {
	m := newTestModel(t)
	result := m.formatDaemonTaskList(nil)

	if !strings.Contains(result, "暂无") {
		t.Errorf("formatDaemonTaskList 空列表 = %q, expected '暂无'", result)
	}
}

func TestFormatDaemonTaskListWithTasks(t *testing.T) {
	m := newTestModelStarted(t)

	tasks := []daemon.Task{
		{
			ID:        "task-20260130120000-a1b2c3d4",
			Prompt:    "测试任务A",
			Status:    daemon.TaskCompleted,
			Type:      daemon.TaskDream,
			CreatedAt: time.Now(),
		},
		{
			ID:        "task-20260130120001-e5f6g7h8",
			Prompt:    "测试任务B",
			Status:    daemon.TaskRunning,
			Type:      daemon.TaskAnalysis,
			CreatedAt: time.Now(),
		},
	}

	result := m.formatDaemonTaskList(tasks)

	checks := []string{
		"后台任务列表",
		"(共 2 个)",
		"task-20260130120000-a1b2c3d4",
		"task-20260130120001-e5f6g7h8",
		"测试任务A",
		"测试任务B",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("formatDaemonTaskList 有任务时缺少 '%s': %s", check, result)
		}
	}
}

// ========== integration test: 守护进程生命周期 ==========

func TestDaemonLifecycleStartStopRestart(t *testing.T) {
	m := newTestModel(t)

	// 1. 启动
	result := m.handleDaemon("start")
	if !strings.Contains(result, "守护进程已启动") {
		t.Fatalf("启动失败: %s", result)
	}
	if !m.daemon.Status().Running {
		t.Fatal("启动后状态应为运行中")
	}

	// 2. 查看状态
	result = m.handleDaemon("status")
	if !strings.Contains(result, "运行中") {
		t.Errorf("状态应显示运行中: %s", result)
	}

	// 3. 停止
	result = m.handleDaemon("stop")
	if !strings.Contains(result, "守护进程已停止") {
		t.Fatalf("停止失败: %s", result)
	}
	if m.daemon.Status().Running {
		t.Fatal("停止后状态应为未运行")
	}

	// 4. 再次查看状态
	result = m.handleDaemon("status")
	if !strings.Contains(result, "未运行") {
		t.Errorf("停止后状态应显示未运行: %s", result)
	}

	// 5. 重新启动
	result = m.handleDaemon("start")
	if !strings.Contains(result, "守护进程已启动") && !strings.Contains(result, "已在运行") {
		t.Errorf("重新启动: %s", result)
	}
}

func TestDaemonTaskFlow(t *testing.T) {
	m := newTestModelStarted(t)

	// 1. 添加任务 A
	resultA := m.handleDaemon("add 任务A描述")
	if !strings.Contains(resultA, "任务已添加") {
		t.Fatalf("添加任务A失败: %s", resultA)
	}

	// 2. 添加任务 B
	resultB := m.handleDaemon("add 任务B描述")
	if !strings.Contains(resultB, "任务已添加") {
		t.Fatalf("添加任务B失败: %s", resultB)
	}

	// 3. 列出任务
	listResult := m.handleDaemon("list")
	if !strings.Contains(listResult, "任务A") || !strings.Contains(listResult, "任务B") {
		t.Errorf("任务列表should contain两个任务: %s", listResult)
	}

	// 4. 提取任务 ID 并取消任务 A
	tasks := m.daemon.Queue().List()
	if len(tasks) < 2 {
		t.Fatalf("队列应有至少 2 个任务，actual %d", len(tasks))
	}
	taskAID := tasks[0].ID

	cancelResult := m.handleDaemon("cancel " + taskAID)
	if !strings.Contains(cancelResult, "已取消") {
		t.Errorf("取消任务A失败: %s", cancelResult)
	}

	// 5. 验证任务 A 已取消
	cancelledTask := m.daemon.Queue().Get(taskAID)
	if cancelledTask.Status != daemon.TaskCancelled {
		t.Errorf("任务A状态 = %s, expected cancelled", cancelledTask.Status)
	}
}
