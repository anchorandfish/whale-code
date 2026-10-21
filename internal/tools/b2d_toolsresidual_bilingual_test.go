package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

// 双语文案守卫（tools 包收尾：参数校验 / 空列表 / sandbox 确认台 / 补丁报错）。
//
// 形态 SSOT = internal/util/bilingual.go（英文主行 + 3 空格缩进中文辅行）。
// 分工原则同 B2c：**字符集同源**（util.HasHan）、**判据逻辑独立**（本文件自带扫描循环）。
//
// ★ 结构门**复用** B2c 的 b2cUnwrappedHanEmit —— 同一实现、两个文件集：
// 避免 式的「判据双实现必漂移」。该门自带**判别性自证**（合成源码，见 B2c）。

var b2dFiles = []string{"pipeline.go", "cron.go", "task.go", "loop.go", "file.go", "apply_diff.go"}

// TestW083B2d_TouchedFilesExist 前置自检：被判文件必须真在，否则遍历零命中 = 假绿。
func TestW083B2d_TouchedFilesExist(t *testing.T) {
	for _, f := range b2dFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("被守卫的源文件读不到（%s）: %v", f, err)
		}
		if len(src) == 0 {
			t.Fatalf("被守卫的源文件为空（%s）", f)
		}
	}
}

// TestW083B2d_NoUnwrappedHanEmit 结构面：六个文件里不得有「直接发射未包装中文」的写法。
func TestW083B2d_NoUnwrappedHanEmit(t *testing.T) {
	for _, fn := range b2dFiles {
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatalf("读 %s: %v", fn, err)
		}
		for _, v := range b2cUnwrappedHanEmit(src) {
			t.Errorf("%s: 中文被直接发射且未经 util.Bi/BiSame 包装: %s", fn, v)
		}
	}
}

// TestW083B2d_EnglishMainLinesPresent 正向：改造后的英文主行字面量必须在。
// （挡「悄悄退回中文为主」这类回归 —— 与结构门互补，各挡一种回归。）
func TestW083B2d_EnglishMainLinesPresent(t *testing.T) {
	want := map[string][]string{
		"pipeline.go": {
			"missing required field: %s",
			"sandbox rule %s requires confirmation (hit=%q): approving runs this one command outside the sandbox",
		},
		"cron.go":       {"No scheduled tasks"},
		"task.go":       {"No background tasks"},
		"loop.go":       {"No active loop tasks"},
		"file.go":       {"found %d matches; provide a more specific search text"},
		"apply_diff.go": {"Files written: %d/%d. No rollback performed"},
	}
	for fn, subs := range want {
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatalf("读 %s: %v", fn, err)
		}
		for _, sub := range subs {
			if !strings.Contains(string(src), sub) {
				t.Errorf("%s: 英文主行字面量缺失（%q）—— 疑似退回「中文为主」", fn, sub)
			}
		}
	}
}

// assertB2dBilingual 断言 out 同时含英文主行与中文辅行。
func assertB2dBilingual(t *testing.T, what, out, enSub, zhSub string) {
	t.Helper()
	if !strings.Contains(out, enSub) {
		t.Errorf("%s：缺英文主行（%q）\n  got=%q", what, enSub, out)
	}
	if !strings.Contains(out, zhSub) {
		t.Errorf("%s：缺中文辅行（%q）\n  got=%q", what, zhSub, out)
	}
}

// --- 行为面 ---

// TestW083B2d_EmptyListMessagesBilingual 空列表的三种提示。
func TestW083B2d_EmptyListMessagesBilingual(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	if out, err := e.taskListHandler()(context.Background(), json.RawMessage("{}")); err != nil {
		t.Fatalf("TaskList: %v", err)
	} else {
		assertB2dBilingual(t, "TaskList(empty)", out, "No background tasks", "没有后台任务")
	}

	if out, err := e.cronListHandler()(context.Background(), json.RawMessage("{}")); err != nil {
		t.Fatalf("CronList: %v", err)
	} else {
		assertB2dBilingual(t, "CronList(empty)", out, "No scheduled tasks", "没有定时任务")
	}

	out := NewLoopScheduler(func(taskID, prompt string) {}, "").FormatList()
	assertB2dBilingual(t, "LoopScheduler.FormatList(empty)", out, "No active loop tasks", "没有活跃的循环任务")
}

// TestW083B2d_SchemaValidateMissingFieldBilingual 必填字段缺失的拒绝理由。
func TestW083B2d_SchemaValidateMissingFieldBilingual(t *testing.T) {
	stage := &SchemaValidateStage{registry: NewRegistry()}
	sr := stage.Execute(context.Background(), Call{ID: "1", Name: "bash", Input: `{"timeout": 30}`}, &PipelineState{})
	if sr.Proceed {
		t.Fatalf("缺必填字段应 Proceed=false")
	}
	assertB2dBilingual(t, "SchemaValidate(required)", sr.Rejection, "missing required field: command", "缺少必填字段: command")
}

// TestW083B2d_SandboxConfirmMessageBilingual 沙箱可批准档的确认文案（用户可见的「批准即沙箱外执行」）。
func TestW083B2d_SandboxConfirmMessageBilingual(t *testing.T) {
	sbx := sandbox.NewSandbox(t.TempDir())
	sbx.SetEnabled(true)
	stage := &SandboxPrecheckStage{sandbox: sbx}

	sr := stage.Execute(context.Background(),
		Call{ID: "1", Name: "bash", Input: `{"command":"chmod +x run.sh"}`},
		&PipelineState{})
	if !sr.NeedConfirm {
		t.Fatalf("可批准档（chmod）应 NeedConfirm=true，实得 Message=%q", sr.Message)
	}
	assertB2dBilingual(t, "SandboxPrecheck(confirm)", sr.Message,
		"requires confirmation", "命中沙箱规则")
}

// TestW083B2d_EditFileMultipleMatchBilingual 多处匹配的报错文案。
func TestW083B2d_EditFileMultipleMatchBilingual(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "multi.txt")
	if err := os.WriteFile(file, []byte("hello world\nhello world"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	e := NewExecutor(NewRegistry(), tmp)
	res := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "edit_file",
		Input: mustMarshal(map[string]string{"path": file, "old_text": "hello", "new_text": "hi"}),
	})
	if !res.IsError {
		t.Fatalf("多处匹配应返回 error")
	}
	assertB2dBilingual(t, "edit_file(multi-match)", res.Output, "found 2 matches", "找到 2 处匹配")
}
