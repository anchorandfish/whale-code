package tools

// C-P0-1 反例测试：后台任务沙箱绕过修复
//
// 测试目标：
// - 后台任务（executeTask）接入沙箱链路后，deny 命令被拦截
// - 无 Executor 注入时向后兼容（仅进程组隔离，命令正常执行）
//
// 修复前：executeTask 仅调 StartCommandWithProcessGroup，跳过 prepareSandbox，
// 后台任务可绕过沙箱 deny 列表执行任意命令。
// 修复后：executeTask 复用 Executor.prepareSandbox，deny 命令被拦截，任务标记为 failed。

import (
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// waitForTaskStatus 轮询等待任务状态变化，超时返回当前状态。
//
// 参数：
// - tm: 后台任务管理器
// - id: 后台任务 ID
// - timeout: 最大等待时间
//
// 返回：任务最终快照；任务不存在时返回 nil。
func waitForTaskStatus(tm *TaskManager, id string, timeout time.Duration) *BackgroundTask {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, ok := tm.Get(id)
		if !ok {
			return nil
		}
		if task.Status != "running" {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	task, _ := tm.Get(id)
	return task
}

// TestCP0_1_TaskSandboxDenyCommand 反例测试（C-P0-1）：后台任务 deny 命令被沙箱拦截。
//
// 修复前：executeTask 不调 prepareSandbox，curl（默认 deny 列表）可正常执行。
// 修复后：executeTask 复用 Executor.prepareSandbox，curl 被拦截，任务标记为 failed。
func TestCP0_1_TaskSandboxDenyCommand(t *testing.T) {
	// 构造带沙箱的 Executor（Legacy Sandbox 模式，curl 在默认 deny 列表）
	workDir := t.TempDir()
	sb := sandbox.NewSandbox(workDir)
	sb.SetEnabled(true)

	e := &Executor{
		Sandbox: sb,
		workDir: workDir,
	}

	tm := NewTaskManager()
	tm.SetExecutor(e)

	// 创建一个 deny 命令的后台任务（curl 在默认 deny 列表中）
	task := tm.Create("curl http://evil.com", "test deny command", 5)

	// 等待任务完成（异步执行）
	completed := waitForTaskStatus(tm, task.ID, 3*time.Second)
	if completed == nil {
		t.Fatal("C-P0-1 反例失败: 后台任务丢失")
	}

	// 验证任务被沙箱拒绝
	if completed.Status != "failed" {
		t.Errorf("C-P0-1 反例失败: deny 命令的后台任务未被沙箱拦截\n"+
			"  命令: curl http://evil.com\n"+
			"  期望: status=failed (sandbox denied)\n"+
			"  实际: status=%s, output=%s", completed.Status, completed.Output)
	}
	if !strings.Contains(completed.Output, "sandbox") && !strings.Contains(completed.Output, "denied") && !strings.Contains(completed.Output, "blocked") {
		t.Errorf("C-P0-1 反例失败: 任务失败原因不是沙箱拦截\n"+
			"  期望: output 包含 'sandbox'/'denied'/'blocked'\n"+
			"  实际: output=%s", completed.Output)
	}
	// G2 加强（2026-09-15 · O-T 轮，非放宽）：上面的散文断言只证明"文案里提到了沙箱"，
	// 上层据此判断拒绝类型就是族D-2 禁止的「结构化事实由散文关键词断言」。
	// 现补结构化通道断言 —— 这才是编排/UI 可直接消费的判据。
	if completed.FailureKind != FailureKindSandboxDenied {
		t.Errorf("G2 反例失败: 后台任务被沙箱拒绝应携带结构化 FailureKind=%q，实际 %q",
			FailureKindSandboxDenied, completed.FailureKind)
	}
}

// TestCP0_1_TaskNoExecutorBackwardCompat 验证无 Executor 时向后兼容（C-P0-1）。
//
// TaskManager 无 Executor 注入时，executeTask 退化为仅进程组隔离（向后兼容），
// 不调用 prepareSandbox，安全命令正常执行。
func TestCP0_1_TaskNoExecutorBackwardCompat(t *testing.T) {
	tm := NewTaskManager()
	// 不调用 SetExecutor，executor 为 nil（向后兼容）

	task := tm.Create("echo hello_backward_compat", "backward compat test", 5)

	// 等待任务完成
	completed := waitForTaskStatus(tm, task.ID, 3*time.Second)
	if completed == nil {
		t.Fatal("C-P0-1 向后兼容失败: 后台任务丢失")
	}

	// 无 Executor 时命令应正常执行
	if completed.Status != "completed" {
		t.Errorf("C-P0-1 向后兼容失败: 无 Executor 时安全命令应正常完成\n"+
			"  期望: status=completed\n"+
			"  实际: status=%s, output=%s", completed.Status, completed.Output)
	}
}

// TestCP0_1_TaskSandboxAllowsSafeCommand 验证沙箱允许安全命令通过（C-P0-1 回归测试）。
//
// 带沙箱的 Executor 不应误拦安全命令（如 echo），任务应正常完成。
func TestCP0_1_TaskSandboxAllowsSafeCommand(t *testing.T) {
	workDir := t.TempDir()
	sb := sandbox.NewSandbox(workDir)
	sb.SetEnabled(true)

	e := &Executor{
		Sandbox: sb,
		workDir: workDir,
	}

	tm := NewTaskManager()
	tm.SetExecutor(e)

	task := tm.Create("echo safe_command_test", "safe command test", 5)

	completed := waitForTaskStatus(tm, task.ID, 3*time.Second)
	if completed == nil {
		t.Fatal("C-P0-1 回归失败: 后台任务丢失")
	}

	if completed.Status != "completed" {
		t.Errorf("C-P0-1 回归失败: 安全命令被沙箱误拦\n"+
			"  命令: echo safe_command_test\n"+
			"  期望: status=completed\n"+
			"  实际: status=%s, output=%s", completed.Status, completed.Output)
	}
}
