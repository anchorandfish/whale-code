package tools

// fork_lifecycle_test.go — 拍板材料 §4（伴生②④⑤）判据。
//
// §4-② 超时终态分裂：流中 deadline exceeded 曾一律归 `failed`，轮间超时归 `timed_out`
// （agent.go RunForkAsync 两条写入路径）。修法 = 错误判别函数 + TimeoutTask，三态语义齐。
// §4-④ 登记即清场：RegisterTask 曾先清光全部终态任务再插入 ⇒ fork 结论保质期 =
// 到下一次 fork 为止。修法 = 删清场循环 + 上限最旧淘汰（终态优先淘汰）。
// §4-⑤ duration 秒级取整：短命任务显示 `0s`，读起来像"没干活"。修法 = <1s 显示毫秒。
//
// 判别性自证（R4-7/R11）：TestRegisterTask_KeepsTerminalResults 只用**既有**符号，
// 修复前即语义红；其余引用新符号的用例，收工时以 overlay mutant（掏空新函数行为）证明可判别。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestRegisterTask_KeepsTerminalResults §4-④：终态任务的读回窗口必须覆盖"下一次 fork 之后"。
//
// 修复前行为（红）：RegisterTask 清场循环把 completed 任务删光 ⇒ 第二次 fork 后读回报 does not exist
// （真机 2026-10-10 18:30 实锤：进程内 18:16 可读的 fork 到 18:2x 变 does not exist）。
// 修复后行为（绿）：终态结果保留到进程生命周期，由上限淘汰兜底。
func TestRegisterTask_KeepsTerminalResults(t *testing.T) {
	fm := NewForkManager()

	first := &ForkTask{ID: "fork_first", Prompt: "p1", Status: "running", StartTime: time.Now()}
	fm.RegisterTask(first)
	fm.CompleteTask("fork_first", "结论 A：root cause 在调度层")

	// 第二次 fork：修复前此刻会把 first 从 map 里清掉。
	second := &ForkTask{ID: "fork_second", Prompt: "p2", Status: "running", StartTime: time.Now()}
	fm.RegisterTask(second)

	got, ok := fm.GetTask("fork_first")
	if !ok {
		t.Fatal("§4-④ 同源破坏：第一次 fork 的终态结论在第二次 fork 后丢失（登记即清场仍在）")
	}
	if got.Status != "completed" || got.Result != "结论 A：root cause 在调度层" {
		t.Fatalf("§4-④ 终态结论不完整：status=%q result=%q", got.Status, got.Result)
	}
}

// TestRegisterTask_CapEviction §4-④ 配套：上限淘汰必须先淘汰**最旧的终态任务**，保住 running。
func TestRegisterTask_CapEviction(t *testing.T) {
	fm := NewForkManager()

	// 1 个 running（整体最旧）+ maxRetainedForkTasks-1 个终态（比 running 新）。
	running := &ForkTask{ID: "fork_running", Prompt: "r", Status: "running", StartTime: time.Now().Add(-time.Hour)}
	fm.RegisterTask(running)
	for i := 0; i < maxRetainedForkTasks-1; i++ {
		id := fmt.Sprintf("fork_done_%02d", i)
		task := &ForkTask{ID: id, Prompt: id, Status: "running", StartTime: time.Now().Add(time.Duration(i) * time.Second)}
		fm.RegisterTask(task)
		fm.CompleteTask(id, "done "+id)
	}

	// 超限一个：必须淘汰最旧的**终态**（fork_done_00），不能动 running。
	extra := &ForkTask{ID: "fork_extra", Prompt: "x", Status: "running", StartTime: time.Now()}
	fm.RegisterTask(extra)

	if _, ok := fm.GetTask("fork_done_00"); ok {
		t.Fatal("§4-④ 上限淘汰未生效：map 无界增长（应淘汰最旧终态任务）")
	}
	if _, ok := fm.GetTask("fork_running"); !ok {
		t.Fatal("§4-④ 淘汰越界：running 任务被终态任务还在时淘汰（必须终态优先）")
	}
	if _, ok := fm.GetTask("fork_extra"); !ok {
		t.Fatal("§4-④ 新登记任务丢失")
	}
}

// TestForkStatusForError §4-② 判别函数：同一物理原因（超时）不得按发生位置分裂成两种终态。
func TestForkStatusForError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"流中裸 deadline", context.DeadlineExceeded, "timed_out"},
		{"流中包装 deadline", fmt.Errorf("agent stream error: %w", context.DeadlineExceeded), "timed_out"},
		{"会话关闭取消（非超时）", context.Canceled, "failed"},
		{"普通错误", errors.New("model 500"), "failed"},
		{"nil（防御面，不该出现在调用点）", nil, "failed"},
	}
	for _, tc := range cases {
		if got := forkStatusForError(tc.err); got != tc.want {
			t.Errorf("%s: forkStatusForError(%v) = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestForkManager_TimeoutTask §4-② 承载体：TimeoutTask 写三态中的 timed_out，保留 result 与归因。
func TestForkManager_TimeoutTask(t *testing.T) {
	fm := NewForkManager()
	fm.RegisterTask(&ForkTask{ID: "fork_t", Prompt: "p", Status: "running", StartTime: time.Now()})

	cause := fmt.Errorf("agent stream error: %w", context.DeadlineExceeded)
	fm.TimeoutTask("fork_t", "部分结果", cause)

	got, ok := fm.GetTask("fork_t")
	if !ok {
		t.Fatal("task missing after TimeoutTask")
	}
	if got.Status != "timed_out" {
		t.Fatalf("status = %q, want timed_out", got.Status)
	}
	if got.Result != "部分结果" {
		t.Fatalf("result = %q, want 部分结果", got.Result)
	}
	if got.Error == nil || !errors.Is(got.Error, context.DeadlineExceeded) {
		t.Fatalf("error 应保留 deadline 归因（errors.Is 可判别），got %v", got.Error)
	}
	if got.EndTime.IsZero() {
		t.Fatal("EndTime 未写入")
	}
}

// TestForkManager_TimeoutTask_MissingID 防御面：对不存在的 ID 必须静默 no-op（与 FailTask 同语义）。
func TestForkManager_TimeoutTask_MissingID(t *testing.T) {
	fm := NewForkManager()
	fm.TimeoutTask("fork_absent", "r", context.DeadlineExceeded) // 不得 panic
}

// TestFormatForkDuration §4-⑤：<1s 显示毫秒，不再把"干了不到半秒"显示成 `0s`。
func TestFormatForkDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{300 * time.Millisecond, "300ms"},
		{873 * time.Millisecond, "873ms"},
		{1500 * time.Millisecond, "2s"}, // ≥1s 保持秒级取整（展示层口径不变）
		{90 * time.Second, "1m30s"},
	}
	for _, tc := range cases {
		if got := formatForkDuration(tc.in); got != tc.want {
			t.Errorf("formatForkDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestForkStatusHandler_ShowsSubSecondDuration §4-⑤ 落点核对（R4-9）：判据打在 forkStatusHandler
// 的真实输出上，不是打在格式化函数本身 —— 防止"改了 helper 没接到展示层"。
func TestForkStatusHandler_ShowsSubSecondDuration(t *testing.T) {
	e := NewExecutor(nil, "")
	h := e.forkStatusHandler()

	fm := e.getOrCreateForkManager()
	now := time.Now()
	fm.RegisterTask(&ForkTask{ID: "fork_fast", Prompt: "p", Status: "running", StartTime: now})
	fm.CompleteTask("fork_fast", "ok")

	// 把 EndTime-StartTime 压到 <1s（CompleteTask 刚写的 EndTime 与 StartTime 差值天然 <1s，
	// 但为确定性起见显式收紧：直接改 manager 内部任务的时间字段）。
	fm.mu.Lock()
	if tsk, ok := fm.tasks["fork_fast"]; ok {
		tsk.StartTime = now
		tsk.EndTime = now.Add(873 * time.Millisecond)
	}
	fm.mu.Unlock()

	out, err := h(context.Background(), []byte(`{"task_id":"fork_fast"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "duration: 873ms") {
		t.Fatalf("§4-⑤ 展示层未生效（仍秒级取整为 0s 或 1s）：\n%s", out)
	}
	if strings.Contains(out, "duration: 0s") || strings.Contains(out, "duration: 1s") {
		t.Fatalf("§4-⑤ 出现秒级取整的短命时长：\n%s", out)
	}
}
