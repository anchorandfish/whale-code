package tools

// ── / / ：后台产出「读回」工具判据（一律拉、不推）──────────────
//
// 缺陷（2026-10-09 家族扫描）：三条异步产出在 whale 里**只写不可读** ——
// 后台命令 stdout（BackgroundTask.Output 已采集、已截断，零出口）
// fork 子 agent 结论（ForkManager.GetTask 生产零调用点）
// Monitor 流式输出（Process.GetOutput 权威副本，模型侧无入口）
//
// 修复方向（方案对照 §0′ 自审修订）：给模型**拉取工具**，不把内容塞回对话流 ——
// 后者撞 internal/tui/app.go:3528-3552「AuthorAsync 出 turn 不得写主消息流」硬规则，
// 且违反已拍板原则「对话流有界、内容留权威副本、按需读取」（render_extras.go:546-547）。
//
// ── before 必须真红（本文件的红测形态）─────────────────────────────────────
// 改造前这三个工具**根本不存在** ⇒ 生产入口 `Executor.Execute` 落到
// executor.go:790 的 `unknown tool: <name>` 出口（IsError=true）。
// 所以主判据在改造前**天然真红**，不需要额外构造 before 态。
//
// ── 反例注入法（production-path-tdd R6/R11 判别性自证）───────────────────────
// 改造后自证判别性用 `go test -overlay` 派生 mutant，**不碰磁盘源码**
// （脚本 `/tmp/ab_w058/mk_mutants.py`；每条替换断言命中 == 1，且 diff 只差一个 hunk）：
// ① ：在 `task.go` 的 `output := truncateTaskOutput(task.Output)` 之后
// 插 `output = ""`（静音读回内容，状态仍如实上报）
// ⇒ 只应让 TestW058_TaskOutput_ReadsBackgroundOutput 转红；
// ② ：把 `monitor.go` 的 tail 截取块改成 `tail = nil`（tail 恒空）
// ⇒ 只应让 TestW060_MonitorTail_ReadsBoundedTail 转红；
// ③ ：在 `agent.go` 的 `result := truncateTaskOutput(task.Result)` 之后
// 插 `result = ""`（静音 fork 结论）
// ⇒ 只应让 TestW059_ForkStatus_* 转红。
// 反向核对（R4-10）：三族 mutant 的红集**互不相交**（实测 A→{}、B→{}、
// C→{}），否则说明判据互相兜底、归因不成立。
//
// ── 权限面论证（方案对照 §0′ 第 2 条要求显式写出，不得默认）──────────────────
// 三条读回工具都声明 LevelReadOnly，论据是：
// 「产出物在**创建时**已各自过权限审批（TaskCreate=LevelWorkspaceWrite /
// Monitor=LevelWorkspaceWrite / Agent=LevelWorkspaceWrite），
// 读其**已采集的结果**不新增越权面」
// ——与 同类 CLI `write_stdin` 跟随**已批准会话**同构（shell_spec.rs:110-152）。
// 能力分桶（internal/permission/capability.go）登记为 **CapMeta**（控制面读回，
// 与既有 `tasklist` 同桶），**不**登记 CapRead —— 于是既不进 `Read(*)` 家族、
// 也不进 tui 的「原地打转」进展检测面，避免顺带改变既有权限/收敛语义。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// newReadbackExecutor 用**生产构造方式**建 Executor（NewExecutor ⇒ 注册表 + handler
// + TaskMgr + MonitorMgr 全部按生产装配）。判据一律经它走 `Execute` 生产入口，
// 不直调 handler、不直调 TaskManager.Get（R2：红测落在生产入口层）。
func newReadbackExecutor(t *testing.T) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), t.TempDir())
	// 夹具纪律（R4 附录）：Monitor 会起真实后台进程 ⇒ 必须配对生产退出收尾。
	// t.Cleanup 是 LIFO ⇒ 本行注册在 t.TempDir() 之后，先停进程再删目录。
	t.Cleanup(e.MonitorMgr.StopAll)
	return e
}

func rbCall(t *testing.T, name string, input interface{}) Call {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input for %s: %v", name, err)
	}
	return Call{ID: "rb_" + name, Name: name, Input: string(b)}
}

// rbParseTaskIDFromBashReceipt 从「后台任务已启动: <id>」回执里取出 task_id。
func rbParseTaskIDFromBashReceipt(t *testing.T, receipt string) string {
	t.Helper()
	const prefix = "后台任务已启动: "
	i := strings.Index(receipt, prefix)
	if i < 0 {
		t.Fatalf("后台回执未含任务 id（前缀 %q）；got=%q", prefix, receipt)
	}
	rest := receipt[i+len(prefix):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	id := strings.TrimSpace(rest)
	if id == "" {
		t.Fatalf("后台回执里的 task id 为空；got=%q", receipt)
	}
	return id
}

// rbWaitForTaskOutput 轮询 TaskOutput 直到任务**离开 running**（到达终态），返回最后一次全文。
func rbWaitForTaskOutput(t *testing.T, e *Executor, taskID string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for {
		r := e.Execute(context.Background(), rbCall(t, "TaskOutput", map[string]string{"task_id": taskID}))
		if r.IsError {
			t.Fatalf("TaskOutput(%s) 报错：%s", taskID, r.Output)
		}
		last = r.Output
		if !strings.Contains(last, "[running]") {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("超时：任务 %s 仍未离开 running；最后一次输出=%q", taskID, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// rbOutputBody 从 TaskOutput 回执里取出 **output 段落**。
//
// ★ 为什么必须这么取：命令自身（`echo <marker>`）就含 marker，
// 若直接对全文断言 `Contains(marker)`，"读到的是 command 行"会被读成"读到了输出" ⇒ 假绿。
// 本函数把断言面收敛到 output 段落，命令行的 echo 不再能蒙对它。
func rbOutputBody(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "output (")
	if i < 0 {
		t.Fatalf("回执里没有 output 段落（形态变了？）：%q", out)
	}
	return out[i:]
}

// ════════════════════════════════════════════════════════════════════════════
// · TaskOutput
// ════════════════════════════════════════════════════════════════════════════

// TestW058_TaskOutput_ReadsBackgroundOutput 是 的主判据：
// 走**真实用户路径** —— bash(run_in_background) 起后台任务 → TaskOutput 按 id 取回 stdout。
func TestW058_TaskOutput_ReadsBackgroundOutput(t *testing.T) {
	e := newReadbackExecutor(t)
	ctx := context.Background()

	const marker = "w058-marker-echo"
	res := e.Execute(ctx, rbCall(t, "bash", map[string]interface{}{
		"command":           "echo " + marker,
		"run_in_background": true,
	}))
	if res.IsError {
		t.Fatalf("bash(run_in_background) 失败：%s", res.Output)
	}
	taskID := rbParseTaskIDFromBashReceipt(t, res.Output)

	out := rbWaitForTaskOutput(t, e, taskID)
	if !strings.Contains(out, "[completed]") {
		t.Errorf("读完的输出应如实上报终态 completed；got=%q", out)
	}
	// ★ 断言面收敛到 output 段落：命令行里也有 marker，
	// 对全文断言 Contains 会被"读到的其实是 command 行"蒙对（假绿）。
	if body := rbOutputBody(t, out); !strings.Contains(body, marker) {
		t.Errorf("未从 output 段落读到命令的真实 stdout %q；output 段落=%q", marker, body)
	}
}

// TestW058_TaskOutput_UnknownTaskIDIsError 反例守卫：不存在的 id **必须报错**，
// 不得静默返回空串（否则模型会把"读不到"当成"没有输出"）。
func TestW058_TaskOutput_UnknownTaskIDIsError(t *testing.T) {
	e := newReadbackExecutor(t)
	r := e.Execute(context.Background(), rbCall(t, "TaskOutput", map[string]string{
		"task_id": "task_does_not_exist_9999",
	}))
	if !r.IsError {
		t.Fatalf("不存在的 task_id 必须报错；got IsError=false, output=%q", r.Output)
	}
	if strings.TrimSpace(r.Output) == "" {
		t.Fatal("错误信息不得为空")
	}
	// ★ 正向留痕（R3-③）：负向断言极容易被**更早的短路**蒙对 ——
	// 改造前 TaskOutput 根本不存在，`unknown tool` 也满足 IsError=true。
	// 故必须证明这个错误来自**读回层**（回显了被查的 id），而不是"工具不存在"。
	if strings.Contains(r.Output, "unknown tool") {
		t.Fatalf("错误来自 unknown tool 出口 ⇒ 本判据不具判别性（工具未注册）；got=%q", r.Output)
	}
	if !strings.Contains(r.Output, "task_does_not_exist_9999") {
		t.Errorf("错误信息应回显被查的 task_id（证明是读回层查无此 id）；got=%q", r.Output)
	}
}

// TestW058_TaskOutput_EmptyTaskIDIsError 空 id 必须报错（与 TaskStop 同型）。
func TestW058_TaskOutput_EmptyTaskIDIsError(t *testing.T) {
	e := newReadbackExecutor(t)
	r := e.Execute(context.Background(), rbCall(t, "TaskOutput", map[string]string{"task_id": ""}))
	if !r.IsError {
		t.Fatalf("空 task_id 必须报错；got output=%q", r.Output)
	}
	// ★ 正向留痕（R3-③）：负向断言会被"工具不存在"这个更早的出口蒙对 ——
	// 改造前 `unknown tool` 同样满足 IsError=true。必须证明错误来自读回层。
	if strings.Contains(r.Output, "unknown tool") {
		t.Fatalf("错误来自 unknown tool 出口 ⇒ 判据不具判别性（工具未注册）；got=%q", r.Output)
	}
}

// TestW058_TaskOutput_RunningTaskDoesNotBlock：running 任务**不阻塞**地返回当前已有输出。
// 判据含耗时上界 —— 若实现退化成"等待任务结束"，本用例转红。
func TestW058_TaskOutput_RunningTaskDoesNotBlock(t *testing.T) {
	e := newReadbackExecutor(t)
	ctx := context.Background()

	res := e.Execute(ctx, rbCall(t, "TaskCreate", map[string]interface{}{
		"command":     "sleep 5",
		"description": "w058 running probe",
	}))
	if res.IsError {
		t.Fatalf("TaskCreate 失败：%s", res.Output)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil || created.TaskID == "" {
		t.Fatalf("TaskCreate 回执解析失败（err=%v）：%q", err, res.Output)
	}
	t.Cleanup(func() {
		_ = e.Execute(context.Background(), rbCall(t, "TaskStop", map[string]string{"task_id": created.TaskID}))
	})

	start := time.Now()
	r := e.Execute(ctx, rbCall(t, "TaskOutput", map[string]string{"task_id": created.TaskID}))
	elapsed := time.Since(start)

	if r.IsError {
		t.Fatalf("对 running 任务读回不得报错：%s", r.Output)
	}
	if elapsed > time.Second {
		t.Fatalf("对 running 任务必须立刻返回（非阻塞）；实际耗时 %v（输出=%q）", elapsed, r.Output)
	}
	if !strings.Contains(r.Output, "running") {
		t.Errorf("running 任务的状态必须如实上报为 running；got=%q", r.Output)
	}
}

// TestW058_BashSleepGatePointsToTaskOutput 文案判据（配套死路 ①）：
// sleep 门的指引必须指向**真实存在**的读回入口，且修掉 `"...true option;for..."`
// 的缺空格/语义别扭（旧串里 `option;` 与 `for` 之间无空格）。
func TestW058_BashSleepGatePointsToTaskOutput(t *testing.T) {
	e := newReadbackExecutor(t)
	r := e.Execute(context.Background(), rbCall(t, "bash", map[string]interface{}{"command": "sleep 5"}))
	if !r.IsError {
		t.Fatalf("前台 sleep 5 应被 sleep 门拦下（IsError=true）；got=%q", r.Output)
	}
	// 正向留痕：确认这确实是 sleep 门的拒绝，而不是更早的短路（R3-③）。
	if !strings.Contains(r.Output, "sleep pattern blocked") {
		t.Fatalf("不是 sleep 门的拒绝 ⇒ 本判据会被更早的短路蒙对；got=%q", r.Output)
	}
	for _, want := range []string{"TaskOutput", "Monitor", "second(s) or less"} {
		if !strings.Contains(r.Output, want) {
			t.Errorf("sleep 门文案未含真实可用入口/上界 %q；got=%q", want, r.Output)
		}
	}
	if strings.Contains(r.Output, "option;for") {
		t.Errorf("旧串的缺空格形态 `option;for` 仍在；got=%q", r.Output)
	}
}

// TestW058_BackgroundReceiptPointsToTaskOutput 文案判据（配套死路 ②）：
// 后台启动回执不能只说"用 TaskList 查看状态"——状态 ≠ 输出，必须点名 TaskOutput。
func TestW058_BackgroundReceiptPointsToTaskOutput(t *testing.T) {
	e := newReadbackExecutor(t)
	r := e.Execute(context.Background(), rbCall(t, "bash", map[string]interface{}{
		"command":           "echo w058-receipt",
		"run_in_background": true,
	}))
	if r.IsError {
		t.Fatalf("bash(run_in_background) 失败：%s", r.Output)
	}
	if !strings.Contains(r.Output, "TaskOutput") {
		t.Errorf("后台回执未点名输出读回入口 TaskOutput ⇒ 模型只能看状态、看不到输出；got=%q", r.Output)
	}
	if !strings.Contains(r.Output, "TaskList") {
		t.Errorf("后台回执仍应保留状态查询入口 TaskList；got=%q", r.Output)
	}
}

// TestW058_TaskCreateSpec_TimeoutHonest 同族小项：spec 文案必须与 实际 clamp 一致。
// 旧文案 "0 means no timeout" 与实现（0 ⇒ 默认 600s、上界 7200s）矛盾，
// 会诱导模型对用户承诺"不会超时"。
func TestW058_TaskCreateSpec_TimeoutHonest(t *testing.T) {
	spec, ok := NewRegistry().Get("TaskCreate")
	if !ok {
		t.Fatal("Registry.Get(\"TaskCreate\") 未命中 ⇒ 落点找错，本判据无效")
	}
	props, ok := spec.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("InputSchema.properties 形态异常：%#v", spec.InputSchema["properties"])
	}
	timeout, ok := props["timeout"].(map[string]interface{})
	if !ok {
		t.Fatalf("InputSchema.properties.timeout 形态异常：%#v", props["timeout"])
	}
	desc, _ := timeout["description"].(string)
	if strings.Contains(desc, "no timeout") {
		t.Errorf("TaskCreate.timeout 文案仍声称 'no timeout'（与  clamp 矛盾）；got=%q", desc)
	}
	if !strings.Contains(desc, "600") {
		t.Errorf("文案应给出真实默认值 600s；got=%q", desc)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// · ForkStatus
// ════════════════════════════════════════════════════════════════════════════

// rbParseForkIDFromReceipt 从 fork 回执里取出 task id。
func rbParseForkIDFromReceipt(t *testing.T, receipt string) string {
	t.Helper()
	const prefix = "Task ID: "
	i := strings.Index(receipt, prefix)
	if i < 0 {
		t.Fatalf("fork 回执未含 %q；got=%q", prefix, receipt)
	}
	rest := receipt[i+len(prefix):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	id := strings.TrimSpace(rest)
	if id == "" {
		t.Fatalf("fork 回执里的 task id 为空；got=%q", receipt)
	}
	return id
}

// TestW059_ForkStatus_EndToEndViaAgentFork 走生产入口：Agent(agent_type=fork) 起子 agent
// → 用回执里的 task id 经 ForkStatus 取回子 agent 的**结论**。
// 判据同时钉住回执文案不再指向不存在的通道（"check status later"）。
func TestW059_ForkStatus_EndToEndViaAgentFork(t *testing.T) {
	e := newReadbackExecutor(t)
	e.AIClient = &agentMockClient{responses: []<-chan ai.Event{makeTextStream("w059-fork-result")}}

	res := e.Execute(context.Background(), rbCall(t, "Agent", map[string]interface{}{
		"prompt":     "produce the w059 marker",
		"agent_type": "fork",
	}))
	if res.IsError {
		t.Fatalf("Agent(agent_type=fork) 失败：%s", res.Output)
	}
	if strings.Contains(res.Output, "check status later") {
		t.Errorf("fork 回执仍指向不存在的通道（'check status later'）；got=%q", res.Output)
	}
	if !strings.Contains(res.Output, "ForkStatus") {
		t.Errorf("fork 回执必须点名读回入口 ForkStatus；got=%q", res.Output)
	}

	forkID := rbParseForkIDFromReceipt(t, res.Output)

	deadline := time.Now().Add(20 * time.Second)
	var last string
	for {
		r := e.Execute(context.Background(), rbCall(t, "ForkStatus", map[string]string{"task_id": forkID}))
		if r.IsError {
			t.Fatalf("ForkStatus(%s) 报错：%s", forkID, r.Output)
		}
		last = r.Output
		if strings.Contains(last, "w059-fork-result") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("超时未从 ForkStatus(%s) 读回结论；最后一次=%q", forkID, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(last, "completed") {
		t.Errorf("ForkStatus 应如实上报终态 completed；got=%q", last)
	}
}

// rbSeedForkTask 用 ForkManager 的**公开注册入口**造一条任务，再按生产同一形态
// （agent.go:895-903 的 timed_out 分支**不经** CompleteTask/FailTask，直接写内部 map）
// 落到指定终态。这不是"手工构造前提"：被测分支是**读回工具的格式化**，
// 状态来源仍是生产同一个 ForkManager 实例（R2 允许注入"事实"，不允许替换"判定"）。
func rbSeedForkTask(t *testing.T, e *Executor, id, status, result string) {
	t.Helper()
	fm := e.getOrCreateForkManager()
	fm.RegisterTask(&ForkTask{ID: id, Prompt: "seeded", Status: "running", StartTime: time.Now()})
	switch status {
	case "completed":
		fm.CompleteTask(id, result)
	case "failed":
		fm.FailTask(id, fmt.Errorf("%s", result))
	default:
		fm.mu.Lock()
		if tf, ok := fm.tasks[id]; ok {
			tf.Status = status
			tf.Result = result
			tf.EndTime = time.Now()
		}
		fm.mu.Unlock()
	}
}

// TestW059_ForkStatus_CoversFailedAndTimedOut：初版方案只设计了成功路径，
// 本判据补上 failed / timed_out 两态（失败比成功更需要让模型知道）。
func TestW059_ForkStatus_CoversFailedAndTimedOut(t *testing.T) {
	e := newReadbackExecutor(t)
	ctx := context.Background()

	rbSeedForkTask(t, e, "fork_w059_failed", "failed", "w059-fail-reason")
	r := e.Execute(ctx, rbCall(t, "ForkStatus", map[string]string{"task_id": "fork_w059_failed"}))
	if r.IsError {
		t.Fatalf("ForkStatus(failed) 不得报错：%s", r.Output)
	}
	for _, want := range []string{"failed", "w059-fail-reason"} {
		if !strings.Contains(r.Output, want) {
			t.Errorf("fork failed 态必须可读 %q；got=%q", want, r.Output)
		}
	}

	rbSeedForkTask(t, e, "fork_w059_timeout", "timed_out", "w059-timeout-partial")
	r2 := e.Execute(ctx, rbCall(t, "ForkStatus", map[string]string{"task_id": "fork_w059_timeout"}))
	if r2.IsError {
		t.Fatalf("ForkStatus(timed_out) 不得报错：%s", r2.Output)
	}
	for _, want := range []string{"timed_out", "w059-timeout-partial"} {
		if !strings.Contains(r2.Output, want) {
			t.Errorf("fork timed_out 态必须可读 %q；got=%q", want, r2.Output)
		}
	}
}

// TestW059_ForkStatus_UnknownTaskIDIsError 反例守卫。
func TestW059_ForkStatus_UnknownTaskIDIsError(t *testing.T) {
	e := newReadbackExecutor(t)
	r := e.Execute(context.Background(), rbCall(t, "ForkStatus", map[string]string{
		"task_id": "fork_does_not_exist_9999",
	}))
	if !r.IsError {
		t.Fatalf("不存在的 fork task_id 必须报错；got output=%q", r.Output)
	}
	// ★ 正向留痕（R3-③）：必须证明错误来自读回层，而不是 unknown-tool 出口。
	if strings.Contains(r.Output, "unknown tool") {
		t.Fatalf("错误来自 unknown tool 出口 ⇒ 判据不具判别性（工具未注册）；got=%q", r.Output)
	}
	if !strings.Contains(r.Output, "fork_does_not_exist_9999") {
		t.Errorf("错误信息应回显被查的 fork task_id；got=%q", r.Output)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// · MonitorTail
// ════════════════════════════════════════════════════════════════════════════

// rbParseMonitorIDFromReceipt 从「📡 监控已启动 (id: mon_1)」回执里取出 monitor id。
func rbParseMonitorIDFromReceipt(t *testing.T, receipt string) string {
	t.Helper()
	const open = "(id: "
	i := strings.Index(receipt, open)
	if i < 0 {
		t.Fatalf("监控回执未含 %q；got=%q", open, receipt)
	}
	rest := receipt[i+len(open):]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		t.Fatalf("监控回执 id 未闭合；got=%q", receipt)
	}
	id := strings.TrimSpace(rest[:j])
	if id == "" {
		t.Fatalf("监控 id 为空；got=%q", receipt)
	}
	return id
}

// rb30LineCommand 造一条打印 line-1..line-30 的命令（纯 printf，不用 shell 算术/
// 命令替换 —— 避免触发 security 的 CMD-SUBST 硬拒，那会让本判据红在错误的一维）。
func rb30LineCommand() string {
	var sb strings.Builder
	sb.WriteString("printf '%s\\n'")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&sb, " line-%d", i)
	}
	return sb.String()
}

// TestW060_MonitorTail_ReadsBoundedTail 是 的主判据：
// 走生产入口 Monitor 起进程 → MonitorTail 按 id 读回**尾部**内容，且**有界**（最后 10 行）。
func TestW060_MonitorTail_ReadsBoundedTail(t *testing.T) {
	e := newReadbackExecutor(t)
	ctx := context.Background()

	res := e.Execute(ctx, rbCall(t, "Monitor", map[string]interface{}{
		"command":     rb30LineCommand(),
		"description": "w060 30-line probe",
	}))
	if res.IsError {
		t.Fatalf("Monitor 启动失败：%s", res.Output)
	}
	monID := rbParseMonitorIDFromReceipt(t, res.Output)

	deadline := time.Now().Add(20 * time.Second)
	var last string
	for {
		r := e.Execute(ctx, rbCall(t, "MonitorTail", map[string]string{"monitor_id": monID}))
		if r.IsError {
			t.Fatalf("MonitorTail(%s) 报错：%s", monID, r.Output)
		}
		last = r.Output
		if strings.Contains(last, "line-30") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("超时未从 MonitorTail(%s) 读到 line-30；最后一次=%q", monID, last)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 有界性：tail 上限 = 10 行（与 `/monitors tail <id>` 的 monitorTailLines=10 同构）。
	// 双向断言：既证明它读到了尾部，也证明它**没有**把全部 30 行灌进上下文。
	if !strings.Contains(last, "line-21") {
		t.Errorf("tail 必须包含第 21 行（最后 10 行的首行）；got=%q", last)
	}
	if strings.Contains(last, "line-20") {
		t.Errorf("tail 越界：包含了第 20 行（上限 10 行）；got=%q", last)
	}
	// 总量必须可见（否则模型不知道被截了多少）。
	if !strings.Contains(last, "30") {
		t.Errorf("tail 回执必须给出总行数 30（否则截断不可见）；got=%q", last)
	}
}

// TestW060_MonitorTail_UnknownMonitorIDIsError 反例守卫。
func TestW060_MonitorTail_UnknownMonitorIDIsError(t *testing.T) {
	e := newReadbackExecutor(t)
	r := e.Execute(context.Background(), rbCall(t, "MonitorTail", map[string]string{
		"monitor_id": "mon_does_not_exist_9999",
	}))
	if !r.IsError {
		t.Fatalf("不存在的 monitor_id 必须报错；got output=%q", r.Output)
	}
	// ★ 正向留痕（R3-③）：必须证明错误来自读回层，而不是 unknown-tool 出口。
	if strings.Contains(r.Output, "unknown tool") {
		t.Fatalf("错误来自 unknown tool 出口 ⇒ 判据不具判别性（工具未注册）；got=%q", r.Output)
	}
	if !strings.Contains(r.Output, "mon_does_not_exist_9999") {
		t.Errorf("错误信息应回显被查的 monitor_id；got=%q", r.Output)
	}
}

// TestW060_MonitorReceipt_PointsToModelReadableEntry 文案判据（配套死路 ③）：
// 启动回执里的读取入口必须对**模型**可走（工具 MonitorTail），
// 而 `/monitors` 是人类斜杠命令 —— 必须显式标注为"用户命令"，
// 否则对模型就是一条做不到的指引（文案可执行性不变式）。
func TestW060_MonitorReceipt_PointsToModelReadableEntry(t *testing.T) {
	e := newReadbackExecutor(t)
	res := e.Execute(context.Background(), rbCall(t, "Monitor", map[string]interface{}{
		"command":     "echo w060-receipt",
		"description": "w060 receipt probe",
	}))
	if res.IsError {
		t.Fatalf("Monitor 启动失败：%s", res.Output)
	}
	// 正向留痕：这确实是"启动成功"的结果（负向断言易被更早的失败蒙对）。
	if !strings.Contains(res.Output, "监控已启动") {
		t.Fatalf("不是启动成功的结果 ⇒ 判据会被提前失败蒙对；got=%q", res.Output)
	}
	if !strings.Contains(res.Output, "MonitorTail") {
		t.Errorf("启动回执未给出模型可走的读回入口 MonitorTail；got=%q", res.Output)
	}
	// 人向入口保留，但必须标注读者（既有判据 b1 钉住 /monitors 与 /monitors stop 必须仍在）。
	for _, keep := range []string{"/monitors", "/monitors stop"} {
		if !strings.Contains(res.Output, keep) {
			t.Errorf("人向入口 %q 被误删（b1_monitor_result_copy_test.go 的判据会红）；got=%q", keep, res.Output)
		}
	}
}

// ════════════════════════════════════════════════════════════════════════════
// 三条工具的工具面/权限面登记（注册点 + 执行门 + spec）
// ════════════════════════════════════════════════════════════════════════════

// TestW058W060_ReadbackTools_SpecAndHandlerRegistered：
// 三条工具必须同时出现在 **spec 表**（模型可见）与 **handler 注册表**（可执行）。
// 只做一半 = 模型看得见却调不动（或反过来）。
func TestW058W060_ReadbackTools_SpecAndHandlerRegistered(t *testing.T) {
	want := []struct {
		name string
		perm Level
		safe bool
	}{
		{"TaskOutput", LevelReadOnly, true},
		{"MonitorTail", LevelReadOnly, true},
		{"ForkStatus", LevelReadOnly, true},
	}

	r := NewRegistry()
	e := NewExecutor(NewRegistry(), t.TempDir())
	t.Cleanup(e.MonitorMgr.StopAll)

	for _, w := range want {
		spec, ok := r.Get(w.name)
		if !ok {
			t.Errorf("spec 表缺 %q（模型看不到这个工具）", w.name)
			continue
		}
		if spec.RequiredPermission != w.perm {
			t.Errorf("%s: 期望权限档 %d，实际 %d", w.name, w.perm, spec.RequiredPermission)
		}
		if spec.ConcurrencySafe != w.safe {
			t.Errorf("%s: 期望 ConcurrencySafe=%v，实际 %v", w.name, w.safe, spec.ConcurrencySafe)
		}
		e.mu.RLock()
		_, registered := e.handlers[w.name]
		e.mu.RUnlock()
		if !registered {
			t.Errorf("handler 注册表缺 %q（模型调不动，落到 unknown tool）", w.name)
		}
	}
}
