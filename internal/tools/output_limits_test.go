// 判别器（2026-09-20 批 D 落盘）。
//
// 缺陷：`ToolLimits` / `OutputLimits` 的字段**零消费**（`LoadFromEnv()` 全仓零调用）
// ⇒ `WHALE_BASH_MAX_OUTPUT` / `WHALE_TASK_MAX_OUTPUT` 两个 env「设了不生效」。
//
// ★ 判据纪律：**必须测行为变化**（设 env ⇒ 输出真被截），
// **不得**只测「env 被读到了」（那是恒真装饰）。
// ★ 每条都用**配对反证**：不设 env ⇒ 输出完整 ⇒ 证明「截断」不是恒有。
package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

// resetOutputLimitsForTest 把包级 SSOT 复位到默认值（同包测试可访问未导出状态）。
//
// 必要性：`LoadOutputLimitsFromEnv()` 写的是**包级**变量 ⇒ 测试之间会互串（假绿/假红）。
func resetOutputLimitsForTest() {
	outputLimitsMu.Lock()
	outputLimits = DefaultOutputLimits()
	outputLimitsMu.Unlock()
}

// TestN080_BashOutputLimitEnvTakesEffect 端到端：`WHALE_BASH_MAX_OUTPUT` 必须真的截断 bash 输出。
func TestN080_BashOutputLimitEnvTakesEffect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("命令用 seq/sh；Windows 侧未手测")
	}
	e := NewExecutor(NewRegistry(), t.TempDir())
	const cmd = "seq 1 4000" // ≈ 20000+ 字符，足以超过 2000 上限

	run := func(id string) string {
		return e.Execute(context.Background(), Call{
			ID:    id,
			Name:  "bash",
			Input: mustMarshal(map[string]interface{}{"command": cmd, "timeout": 30}),
		}).Output
	}

	// ① 未设 env（默认 50000）⇒ 输出完整、**不含**截断标记
	base := run("n080-base")
	if strings.Contains(base, "(truncated ") {
		t.Fatalf("未设 env 时不该截断（默认上限 %d）；实际输出尾部 = %q",
			DefaultBashMaxOutput, tail(base, 80))
	}
	if len(base) < 2000 {
		t.Fatalf("基准命令输出过短（%d 字符）⇒ 本判据无法区分，请换命令", len(base))
	}

	// ② 设 env ⇒ 必须真的被截到 2000
	t.Setenv(EnvBashMaxOutput, "2000")
	LoadOutputLimitsFromEnv()
	defer resetOutputLimitsForTest()

	got := run("n080-env")
	if !strings.Contains(got, "(truncated ") {
		t.Errorf("设了 %s=2000 但输出未被截断 ⇒ env 不生效；实际输出头部 = %q",
			EnvBashMaxOutput, head(got, 80))
	}
	if len(got) > 2000+64 {
		t.Errorf("设了 %s=2000 但输出长度 = %d（应 ≤ ~2000 + 截断标记）",
			EnvBashMaxOutput, len(got))
	}
}

// TestN080_TaskOutputLimitEnvTakesEffect 端到端：`WHALE_TASK_MAX_OUTPUT` 必须真的截断后台任务输出。
func TestN080_TaskOutputLimitEnvTakesEffect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("命令用 seq/sh；Windows 侧未手测")
	}
	e := NewExecutor(NewRegistry(), t.TempDir())
	const cmd = "seq 1 4000"

	waitDone := func(id string) *BackgroundTask {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if tk, ok := e.TaskMgr.Get(id); ok && tk.Status != "running" {
				return tk
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("后台任务 %s 未在 30s 内结束", id)
		return nil
	}

	// ① 未设 env（默认 30000）⇒ 完整输出、无截断标记
	base := waitDone(e.TaskMgr.Create(cmd, "n080 task base", 60).ID)
	if strings.Contains(base.Output, "(truncated ") {
		t.Fatalf("未设 env 时不该截断任务输出；实际 = %q", tail(base.Output, 80))
	}

	// ② 设 env ⇒ 真被截到 100
	t.Setenv(EnvTaskMaxOutput, "100")
	LoadOutputLimitsFromEnv()
	defer resetOutputLimitsForTest()

	got := waitDone(e.TaskMgr.Create(cmd, "n080 task env", 60).ID)
	if !strings.Contains(got.Output, "(truncated ") {
		t.Errorf("设了 %s=100 但任务输出未被截断 ⇒ env 不生效；实际 = %q",
			EnvTaskMaxOutput, head(got.Output, 120))
	}
	if len(got.Output) > 100+64 {
		t.Errorf("设了 %s=100 但任务输出长度 = %d", EnvTaskMaxOutput, len(got.Output))
	}
}

// TestN080_LoadOutputLimitsFromEnvClamps 结构面：env 超过上界必须被 clamp（防 OOM）。
func TestN080_LoadOutputLimitsFromEnvClamps(t *testing.T) {
	t.Setenv(EnvBashMaxOutput, "999999999")
	t.Setenv(EnvTaskMaxOutput, "999999999")
	ol := LoadOutputLimitsFromEnv()
	defer resetOutputLimitsForTest()

	if ol.BashMaxOutput != MaxBashMaxOutput {
		t.Errorf("BashMaxOutput = %d, want clamp 到 %d", ol.BashMaxOutput, MaxBashMaxOutput)
	}
	if ol.TaskMaxOutput != MaxTaskMaxOutput {
		t.Errorf("TaskMaxOutput = %d, want clamp 到 %d", ol.TaskMaxOutput, MaxTaskMaxOutput)
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
