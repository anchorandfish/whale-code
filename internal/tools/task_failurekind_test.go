package tools

// G2（§2.7 S11 残余）· 后台任务的**拒绝上报等价结构化通道**。
//
// 决策项：G2 —— 「`task.go` 拒绝不经 executor 的 `Result` 通道（TaskManager 状态机直写）」
// ⇒ 「建等价结构化通道（比照 `agent_task.go` 的 C2 做法）」。
//
// 缺陷面（修改前）：executeTask 的每条拒绝路径各自 `task.Status="failed" + task.Output=散文`，
// 归类只存在于人类可读文案里 —— 上层要判断"是不是沙箱拒绝"只能 grep 散文
// （`task_sandbox_test.go` 当时正是 `output 包含 'sandbox'/'denied'/'blocked'`）。
// 族D-2：结构化事实不得由散文关键词断言。
//
// 判别性（A/B）：把 task.go 的 `tm.failTaskLocked(task, FailureKindSandboxDenied, …)` 单点
// 回退为「直接写 Status/Output、不落 kind」⇒ GT-G2-p1 打红。
//
// 生产入口：`Executor.TaskMgr.Create`（后台任务的唯一创建入口；`executor.go:304`
// 在构造器里已 `e.TaskMgr.SetExecutor(e)` 完成两阶段接线）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/testutil"
)

func gtTaskManagerWithLegacySandbox(t *testing.T) *Executor {
	t.Helper()
	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
	e := gtExecutorAtProductionEntry(t, t.TempDir())
	if e.Sandbox == nil {
		t.Fatal("前提失效：构造器未创建 legacy Sandbox")
	}
	// A1 拍板：沙箱是 opt-in（默认关）。本条要测的是"被沙箱拒绝时的上报形态"，
	// 故必须把这一层显式打开 —— 这是被测分支成立的必要前提，不是"构造出来的假场景"。
	e.Sandbox.SetEnabled(true)
	return e
}

// GT-G2-p1 · 判别器：沙箱拒绝必须落**结构化** FailureKind，而不是只写散文。
//
// curl 在 legacy 沙箱的默认 deny 列表里（同 task_sandbox_test.go 的 C-P0-1 夹具）。
func TestGT_G2_P1_TaskSandboxDenyCarriesStructuredFailureKind(t *testing.T) {
	e := gtTaskManagerWithLegacySandbox(t)

	task := e.TaskMgr.Create("curl http://gt-g2.invalid", "gt-g2 sandbox deny", 5)
	done := waitForTaskStatus(e.TaskMgr, task.ID, 5*time.Second)
	if done == nil {
		t.Fatal("后台任务丢失")
	}
	if done.Status != "failed" {
		t.Fatalf("前提失效：deny 命令未被沙箱拦下（status=%s output=%q）—— 本条会变成空测",
			done.Status, done.Output)
	}
	if done.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("G2 回归：后台任务被沙箱拒绝，但 FailureKind=%q（期望 %q）。\n"+
			"修复前只有散文 output=%q ⇒ 上层只能 grep 文案判断拒绝类型（族D-2）。",
			done.FailureKind, FailureKindSandboxDenied, done.Output)
	}
}

// GT-G2-p2 · 反向守卫（防越界 / 防"一律贴拒绝标签"）：安全命令正常完成且 **kind 为空**。
//
// 若把 kind 无条件写死成 sandbox_denied，本条打红。
func TestGT_G2_P2_SafeCommandKeepsEmptyFailureKind(t *testing.T) {
	e := gtTaskManagerWithLegacySandbox(t)

	task := e.TaskMgr.Create("echo gt_g2_safe", "gt-g2 safe command", 5)
	done := waitForTaskStatus(e.TaskMgr, task.ID, 5*time.Second)
	if done == nil {
		t.Fatal("后台任务丢失")
	}
	if done.Status != "completed" {
		t.Fatalf("安全命令应正常完成，实际 status=%s kind=%q output=%q",
			done.Status, done.FailureKind, done.Output)
	}
	if done.FailureKind != "" {
		t.Fatalf("普通成功任务不得携带 FailureKind，实际 %q", done.FailureKind)
	}
}

// GT-G2-p3 · 出口检查（族D-3：结构化事实必须真的有出口）。
//
// 只在 kind 非空时追加，非拒绝型失败的列表文案不变 ⇒ 不引入模型可见的噪声。
func TestGT_G2_P3_FailureKindIsSurfacedInTaskList(t *testing.T) {
	e := gtTaskManagerWithLegacySandbox(t)

	denied := e.TaskMgr.Create("curl http://gt-g2-list.invalid", "gt-g2 list deny", 5)
	if done := waitForTaskStatus(e.TaskMgr, denied.ID, 5*time.Second); done == nil || done.Status == "running" {
		t.Fatal("前提失效：deny 任务未收敛")
	}

	out, err := e.taskListHandler()(context.Background(), []byte(`{"status":""}`))
	if err != nil {
		t.Fatalf("taskListHandler: %v", err)
	}
	if !strings.Contains(out, "failure_kind="+string(FailureKindSandboxDenied)) {
		t.Fatalf("结构化失败归类没有出口 —— 列表里看不到它，模型仍无从判断「重试无用」。\n列表=%s", out)
	}
}
